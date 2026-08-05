package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const pbkdf2Iter = 210000 // OWASP raspon; na Atomu E3845 provjera traje <0.5 s
const sessionTTLDays = 7
const loginMaxFails = 5
const loginBlock = 60 * time.Second

/* ---------- lozinke (PBKDF2-SHA256, std lib) ---------- */

func hashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iter, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2:%d:%x:%x", pbkdf2Iter, salt, key), nil
}

func verifyPassword(stored, pw string) bool {
	parts := strings.Split(stored, ":")
	if len(parts) != 4 || parts[0] != "pbkdf2" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false
	}
	salt, err1 := hex.DecodeString(parts[2])
	want, err2 := hex.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash služi izjednačavanju trajanja logina za nepostojećeg korisnika.
var dummyHash, _ = hashPassword("saguaro-dummy")

// ensureAdmin pri prvom startu kreira korisnika admin s lozinkom
// jednakom tadašnjem API tokenu (kontinuitet za postojeće korisnike).
func (s *server) ensureAdmin(initialPassword string) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	h, err := hashPassword(initialPassword)
	if err != nil {
		return err
	}
	// zadana lozinka dolazi s instalacije i javno je poznata — do njene
	// promjene korisnik ne može raditi ništa drugo
	if _, err := s.db.Exec(`INSERT INTO users (uuid, username, pass_hash, must_change_pw)
		VALUES (?, 'admin', ?, 1)`, newUUID(), h); err != nil {
		return err
	}
	log.Printf("kreiran korisnik 'admin' (zadana lozinka — obavezna promjena pri prvoj prijavi)")
	return nil
}

// resetAdminPassword postavlja novu lozinku korisniku admin s naredbenog retka
// (zastavica -reset-admin). Sve sesije padaju, a nova lozinka se mora
// promijeniti pri prvoj prijavi — jer je vidljiva u povijesti ljuske.
func (s *server) resetAdminPassword(pw string) error {
	if len([]rune(pw)) < 8 {
		return fmt.Errorf("lozinka mora imati bar 8 znakova")
	}
	h, err := hashPassword(pw)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE users SET pass_hash=?, must_change_pw=1,
		updated_at=datetime('now') WHERE username='admin'`, h)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("korisnik 'admin' ne postoji")
	}
	if _, err := s.db.Exec(`DELETE FROM sessions`); err != nil {
		return err
	}
	return nil
}

/* ---------- API token (promjenjiv u radu) ---------- */

func (s *server) apiToken() string {
	s.tokenMu.RLock()
	defer s.tokenMu.RUnlock()
	return s.token
}

func (s *server) setAPIToken(t string) {
	s.tokenMu.Lock()
	s.token = t
	s.tokenMu.Unlock()
}

/* ---------- ograničenje pokušaja prijave ---------- */

type limEntry struct {
	count int
	until time.Time
}

var (
	limMu    sync.Mutex
	limFails = map[string]*limEntry{}
)

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func loginAllowed(ip string) bool {
	limMu.Lock()
	defer limMu.Unlock()
	e := limFails[ip]
	return e == nil || e.count < loginMaxFails || time.Now().After(e.until)
}

func loginFailed(ip string) {
	limMu.Lock()
	defer limMu.Unlock()
	e := limFails[ip]
	if e == nil || time.Now().After(e.until) && e.count >= loginMaxFails {
		e = &limEntry{}
		limFails[ip] = e
	}
	e.count++
	if e.count >= loginMaxFails {
		e.until = time.Now().Add(loginBlock)
	}
}

func loginOK(ip string) {
	limMu.Lock()
	defer limMu.Unlock()
	delete(limFails, ip)
}

/* ---------- sesije ---------- */

func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// sessionUser vraća korisničko ime za valjanu sesiju ("" ako sesija ne postoji).
func (s *server) sessionUser(token string) string {
	name, _ := s.sessionUserState(token)
	return name
}

// sessionUserState uz korisničko ime vraća i mora li korisnik prvo promijeniti
// zadanu lozinku.
func (s *server) sessionUserState(token string) (string, bool) {
	name, mustChange, _ := s.sessionUserRole(token)
	return name, mustChange
}

// sessionUserRole vraća i ulogu — po njoj se odlučuje što korisnik smije.
// Isključen korisnik se tretira kao neprijavljen, i kad mu sesija još traje.
func (s *server) sessionUserRole(token string) (string, bool, string) {
	var username, role string
	var mustChange, disabled int
	err := s.db.QueryRow(`SELECT u.username, u.must_change_pw, u.role, u.disabled
		FROM sessions se JOIN users u ON u.uuid = se.user_uuid
		WHERE se.token_hash = ? AND se.expires_at > datetime('now')`,
		tokenHash(token)).Scan(&username, &mustChange, &role, &disabled)
	if err != nil || disabled == 1 {
		return "", false, ""
	}
	return username, mustChange == 1, role
}

func bearerToken(r *http.Request) string {
	got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return got
}

/* ---------- handleri ---------- */

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !loginAllowed(ip) {
		writeErr(w, http.StatusTooManyRequests,
			"previše neuspjelih pokušaja — pričekaj minutu")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.Username = strings.TrimSpace(in.Username)

	var userUUID, passHash, role string
	var mustChange, disabled int
	err := s.db.QueryRow(`SELECT uuid, pass_hash, must_change_pw, role, disabled
		FROM users WHERE username=?`, in.Username).
		Scan(&userUUID, &passHash, &mustChange, &role, &disabled)
	if err == sql.ErrNoRows {
		verifyPassword(dummyHash, in.Password) // izjednači trajanje
		loginFailed(ip)
		writeErr(w, http.StatusUnauthorized, "pogrešno korisničko ime ili lozinka")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !verifyPassword(passHash, in.Password) {
		loginFailed(ip)
		writeErr(w, http.StatusUnauthorized, "pogrešno korisničko ime ili lozinka")
		return
	}
	// isključen račun ne smije unutra ni s ispravnom lozinkom; poruka je ista
	// kao za krivu lozinku da se izvana ne razaznaje postoji li račun
	if disabled == 1 {
		loginFailed(ip)
		writeErr(w, http.StatusUnauthorized, "pogrešno korisničko ime ili lozinka")
		return
	}
	loginOK(ip)
	s.db.Exec(`UPDATE users SET last_login=datetime('now') WHERE uuid=?`, userUUID)

	s.db.Exec(`DELETE FROM sessions WHERE expires_at <= datetime('now')`)
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	token := hex.EncodeToString(raw)
	var expires string
	err = s.db.QueryRow(`INSERT INTO sessions (token_hash, user_uuid, expires_at)
		VALUES (?, ?, datetime('now', ?)) RETURNING expires_at`,
		tokenHash(token), userUUID,
		fmt.Sprintf("+%d days", sessionTTLDays)).Scan(&expires)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token, "username": in.Username, "expires_at": expires,
		"must_change_password": mustChange == 1, "role": role,
	})
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, tokenHash(bearerToken(r)))
	writeJSON(w, http.StatusOK, map[string]bool{"logged_out": true})
}

func (s *server) handleLogoutOthers(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash <> ?`,
		tokenHash(bearerToken(r)))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	n, _ := res.RowsAffected()
	writeJSON(w, http.StatusOK, map[string]any{"removed": n})
}

func (s *server) handleSessionInfo(w http.ResponseWriter, r *http.Request) {
	username, _, role := s.sessionUserRole(bearerToken(r))
	if username == "" {
		// pristup strojnim tokenom, bez sesije — token ima puna prava
		username, role = "api-token", roleAdmin
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM sessions
		WHERE expires_at > datetime('now')`).Scan(&n)
	writeJSON(w, http.StatusOK, map[string]any{
		"username": username, "active_sessions": n,
		"role": role, "role_label": roleLabels[role],
	})
}

func (s *server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	username := s.sessionUser(token)
	if username == "" {
		writeErr(w, http.StatusForbidden,
			"promjena lozinke traži prijavu korisničkim računom, ne API tokenom")
		return
	}
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.New) < 8 {
		writeErr(w, http.StatusBadRequest, "nova lozinka mora imati bar 8 znakova")
		return
	}
	var userUUID, passHash string
	if err := s.db.QueryRow(`SELECT uuid, pass_hash FROM users WHERE username=?`,
		username).Scan(&userUUID, &passHash); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !verifyPassword(passHash, in.Current) {
		writeErr(w, http.StatusUnauthorized, "trenutna lozinka nije točna")
		return
	}
	h, err := hashPassword(in.New)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if in.New == in.Current {
		writeErr(w, http.StatusBadRequest, "nova lozinka mora biti različita od trenutne")
		return
	}
	// promjenom pada i obveza mijenjanja zadane lozinke s instalacije
	if _, err := s.db.Exec(`UPDATE users SET pass_hash=?, must_change_pw=0,
		updated_at=datetime('now') WHERE uuid=?`, h, userUUID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// ostale sesije padaju; trenutna ostaje prijavljena
	s.db.Exec(`DELETE FROM sessions WHERE user_uuid=? AND token_hash<>?`,
		userUUID, tokenHash(token))
	writeJSON(w, http.StatusOK, map[string]bool{"changed": true})
}

func (s *server) handleTokenGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"token": s.apiToken()})
}

func (s *server) handleTokenRegen(w http.ResponseWriter, r *http.Request) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	t := hex.EncodeToString(raw)
	path := filepath.Join(s.etcDir, "token")
	if err := os.WriteFile(path, []byte(t+"\n"), 0o600); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.setAPIToken(t)
	log.Printf("API token regeneriran")
	writeJSON(w, http.StatusOK, map[string]string{"token": t})
}
