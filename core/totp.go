package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Dvofaktorska prijava (TOTP, RFC 6238).
//
// Lozinka sama po sebi putuje, curi i ponavlja se — drugi faktor je jedina
// stvar koja ukradenu lozinku čini bezvrijednom. Koristi se standardni TOTP
// (SHA-1, 6 znamenki, 30 sekundi), pa radi sa svakom uobičajenom aplikacijom:
// Google Authenticator, Aegis, 1Password, Bitwarden…
//
// Sve je iz standardne biblioteke — HMAC-SHA1 i base32. Jedino QR kod radi
// vanjski alat (`qrencode`, 24 KiB); ako ga nema, tajna se prepiše rukom i
// prijava radi jednako.

const totpDigits = 6
const totpPeriod = 30
const totpWindow = 1 // dopušta jedan korak prije i poslije (sat na telefonu)

// totpChallengeTTL je koliko vrijedi međukorak između lozinke i koda.
const totpChallengeTTL = 3 * time.Minute

var totpB32 = base32.StdEncoding.WithPadding(base32.NoPadding)

/* ---------- sam algoritam ---------- */

// totpCode računa kod za zadani broj koraka. Izdvojeno zbog testiranja: RFC
// 6238 ima službene ispitne vrijednosti i one moraju proći.
func totpCode(secret []byte, counter uint64, digits int) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	m := hmac.New(sha1.New, secret)
	m.Write(buf[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	val := (uint32(sum[off]&0x7f) << 24) | (uint32(sum[off+1]) << 16) |
		(uint32(sum[off+2]) << 8) | uint32(sum[off+3])
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, val%mod)
}

// totpVerify provjerava kod i vraća broj koraka na kojem je pogodio.
// Vraćeni korak se pamti da se isti kod ne može upotrijebiti dvaput —
// bez toga bi presretnuti kod vrijedio do kraja svojih 30 sekundi.
func totpVerify(secret []byte, code string, now time.Time, lastUsed int64) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return 0, false
	}
	center := now.Unix() / totpPeriod
	for d := -totpWindow; d <= totpWindow; d++ {
		c := center + int64(d)
		if c <= lastUsed {
			continue // već iskorišten korak — ne prihvaća se ponovno
		}
		if subtle.ConstantTimeCompare([]byte(totpCode(secret, uint64(c), totpDigits)),
			[]byte(code)) == 1 {
			return c, true
		}
	}
	return 0, false
}

func newTOTPSecret() (string, error) {
	b := make([]byte, 20) // 160 bita, kako preporučuje RFC 4226
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return totpB32.EncodeToString(b), nil
}

// otpauthURI je ono što aplikacija pročita iz QR koda.
func otpauthURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprintf("%d", totpDigits))
	q.Set("period", fmt.Sprintf("%d", totpPeriod))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

const qrencodeBin = "/usr/bin/qrencode"

func qrencodeInstalled() bool {
	_, err := os.Stat(qrencodeBin)
	return err == nil
}

// qrSVG vraća QR kod kao SVG. Prazno ako alata nema — sučelje tada ponudi
// prepisivanje tajne, što radi jednako dobro, samo je dosadnije.
func qrSVG(text string) string {
	if !qrencodeInstalled() {
		return ""
	}
	out, err := exec.Command(qrencodeBin, "-t", "SVG", "-o", "-",
		"-m", "1", "-s", "5", "--", text).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

/* ---------- pričuvni kodovi ---------- */

// Ako se telefon izgubi ili resetira, bez pričuvnih kodova korisnik ostaje
// zaključan van i treba mu SSH. Kodovi se čuvaju samo kao sažetak i svaki
// vrijedi jednom.
const recoveryCodeCount = 8

func newRecoveryCodes() ([]string, error) {
	out := make([]string, 0, recoveryCodeCount)
	for i := 0; i < recoveryCodeCount; i++ {
		b := make([]byte, 5)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		h := strings.ToLower(hex.EncodeToString(b))
		out = append(out, h[:5]+"-"+h[5:])
	}
	return out, nil
}

func recoveryHash(code string) string {
	c := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(code, "-", "")))
	h := sha256.Sum256([]byte("saguaro-recovery:" + c))
	return hex.EncodeToString(h[:])
}

func (s *server) storeRecoveryCodes(userUUID string, codes []string) error {
	if _, err := s.db.Exec(`DELETE FROM totp_recovery WHERE user_uuid=?`, userUUID); err != nil {
		return err
	}
	for _, c := range codes {
		if _, err := s.db.Exec(`INSERT INTO totp_recovery (user_uuid, code_hash)
			VALUES (?,?)`, userUUID, recoveryHash(c)); err != nil {
			return err
		}
	}
	return nil
}

// useRecoveryCode troši kod ako postoji i još nije upotrijebljen.
func (s *server) useRecoveryCode(userUUID, code string) bool {
	res, err := s.db.Exec(`DELETE FROM totp_recovery
		WHERE user_uuid=? AND code_hash=?`, userUUID, recoveryHash(code))
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

/* ---------- međukorak prijave ---------- */

// totpChallengeTries je koliko puta se kod smije utipkati po jednoj prijavi.
// Krivo prepisan kod je svakodnevna stvar, pa jedan promašaj ne smije rušiti
// prijavu; nekoliko pokušaja je i dalje predaleko od pogađanja šesteroznamenkastog
// koda.
const totpChallengeTries = 5

type totpChallenge struct {
	userUUID string
	username string
	role     string
	expires  time.Time
	tries    int
}

var (
	chMu   sync.Mutex
	chList = map[string]*totpChallenge{}
)

func newChallenge(userUUID, username, role string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b)
	chMu.Lock()
	defer chMu.Unlock()
	// usput se čiste istekli — popis je malen, pa je to dovoljno
	for k, v := range chList {
		if time.Now().After(v.expires) {
			delete(chList, k)
		}
	}
	chList[id] = &totpChallenge{userUUID: userUUID, username: username,
		role: role, expires: time.Now().Add(totpChallengeTTL)}
	return id, nil
}

// claimChallenge dohvaća međukorak i broji pokušaj. Nakon iscrpljenih pokušaja
// međukorak se briše, pa se kodovi ne mogu pogađati u nedogled s istim izazovom.
func claimChallenge(id string) *totpChallenge {
	chMu.Lock()
	defer chMu.Unlock()
	c := chList[id]
	if c == nil || time.Now().After(c.expires) {
		delete(chList, id)
		return nil
	}
	c.tries++
	if c.tries >= totpChallengeTries {
		delete(chList, id)
	}
	return c
}

// dropChallenge briše međukorak nakon uspješne prijave.
func dropChallenge(id string) {
	chMu.Lock()
	defer chMu.Unlock()
	delete(chList, id)
}

/* ---------- API: postavljanje ---------- */

func (s *server) totpState(username string) (enabled bool, pending bool) {
	var secret sql.NullString
	var en int
	s.db.QueryRow(`SELECT totp_secret, totp_enabled FROM users WHERE username=?`,
		username).Scan(&secret, &en)
	return en == 1, secret.Valid && secret.String != "" && en == 0
}

func (s *server) handleTOTPStatus(w http.ResponseWriter, r *http.Request) {
	username := s.sessionUser(bearerToken(r))
	if username == "" {
		writeErr(w, http.StatusForbidden, "dvofaktorska prijava traži prijavu računom")
		return
	}
	enabled, pending := s.totpState(username)
	var left int
	s.db.QueryRow(`SELECT COUNT(*) FROM totp_recovery
		WHERE user_uuid=(SELECT uuid FROM users WHERE username=?)`, username).Scan(&left)
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": enabled, "pending": pending,
		"recovery_left": left, "qr": qrencodeInstalled(),
	})
}

// handleTOTPSetup priprema novu tajnu, ali je JOŠ NE uključuje — dok korisnik
// ne dokaže kodom da mu aplikacija radi, prijava ostaje kakva je bila.
func (s *server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	username := s.sessionUser(bearerToken(r))
	if username == "" {
		writeErr(w, http.StatusForbidden, "traži prijavu računom")
		return
	}
	if enabled, _ := s.totpState(username); enabled {
		writeErr(w, http.StatusConflict,
			"dvofaktorska prijava je već uključena — prvo je isključi")
		return
	}
	secret, err := newTOTPSecret()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := s.db.Exec(`UPDATE users SET totp_secret=?, totp_enabled=0,
		updated_at=datetime('now') WHERE username=?`, secret, username); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	issuer := "Saguaro"
	if l := s.getSetting("device_label", ""); l != "" {
		issuer = "Saguaro " + l
	}
	uri := otpauthURI(issuer, username, secret)
	// tajna se prikazuje u skupinama po 4 — prepisivanje 32 znaka bez toga je
	// nepotrebno mučenje
	grouped := ""
	for i := 0; i < len(secret); i += 4 {
		j := i + 4
		if j > len(secret) {
			j = len(secret)
		}
		if grouped != "" {
			grouped += " "
		}
		grouped += secret[i:j]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"secret": secret, "secret_grouped": grouped, "uri": uri,
		"svg": qrSVG(uri), "digits": totpDigits, "period": totpPeriod,
	})
}

func (s *server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	username := s.sessionUser(bearerToken(r))
	if username == "" {
		writeErr(w, http.StatusForbidden, "traži prijavu računom")
		return
	}
	var in struct {
		Code string `json:"code"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	var uuid string
	var secret sql.NullString
	var en int
	if err := s.db.QueryRow(`SELECT uuid, totp_secret, totp_enabled FROM users
		WHERE username=?`, username).Scan(&uuid, &secret, &en); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if en == 1 {
		writeErr(w, http.StatusConflict, "već je uključena")
		return
	}
	if !secret.Valid || secret.String == "" {
		writeErr(w, http.StatusConflict, "prvo pokreni postavljanje")
		return
	}
	raw, err := totpB32.DecodeString(secret.String)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tajna nije čitljiva")
		return
	}
	step, ok := totpVerify(raw, in.Code, time.Now(), 0)
	if !ok {
		writeErr(w, http.StatusUnauthorized,
			"kod nije točan — provjeri je li sat na telefonu točan")
		return
	}
	codes, err := newRecoveryCodes()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.storeRecoveryCodes(uuid, codes); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.db.Exec(`UPDATE users SET totp_enabled=1, totp_last=?,
		updated_at=datetime('now') WHERE uuid=?`, step, uuid)
	addEvent(s, "warning", "Uključena dvofaktorska prijava za "+username)
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "recovery_codes": codes,
	})
}

func (s *server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	username := s.sessionUser(bearerToken(r))
	if username == "" {
		writeErr(w, http.StatusForbidden, "traži prijavu računom")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	var uuid, passHash string
	if err := s.db.QueryRow(`SELECT uuid, pass_hash FROM users WHERE username=?`,
		username).Scan(&uuid, &passHash); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// isključivanje drugog faktora traži lozinku — inače bi otvorena sesija
	// na tuđem računalu bila dovoljna da se zaštita makne
	if !verifyPassword(passHash, in.Password) {
		writeErr(w, http.StatusUnauthorized, "lozinka nije točna")
		return
	}
	s.db.Exec(`UPDATE users SET totp_enabled=0, totp_secret=NULL, totp_last=0,
		updated_at=datetime('now') WHERE uuid=?`, uuid)
	s.db.Exec(`DELETE FROM totp_recovery WHERE user_uuid=?`, uuid)
	addEvent(s, "warning", "Isključena dvofaktorska prijava za "+username)
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": false})
}

// handleTOTPRecoveryNew izdaje novi set pričuvnih kodova (stari prestaju
// vrijediti) — kad se potroše ili kad se posumnja da ih je netko vidio.
func (s *server) handleTOTPRecoveryNew(w http.ResponseWriter, r *http.Request) {
	username := s.sessionUser(bearerToken(r))
	if username == "" {
		writeErr(w, http.StatusForbidden, "traži prijavu računom")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	var uuid, passHash string
	var en int
	if err := s.db.QueryRow(`SELECT uuid, pass_hash, totp_enabled FROM users
		WHERE username=?`, username).Scan(&uuid, &passHash, &en); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if en != 1 {
		writeErr(w, http.StatusConflict, "dvofaktorska prijava nije uključena")
		return
	}
	if !verifyPassword(passHash, in.Password) {
		writeErr(w, http.StatusUnauthorized, "lozinka nije točna")
		return
	}
	codes, err := newRecoveryCodes()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.storeRecoveryCodes(uuid, codes); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

/* ---------- drugi korak prijave ---------- */

func (s *server) handleLoginTOTP(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !loginAllowed(ip) {
		writeErr(w, http.StatusTooManyRequests,
			"previše neuspjelih pokušaja — pričekaj minutu")
		return
	}
	var in struct {
		Challenge string `json:"challenge"`
		Code      string `json:"code"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	ch := claimChallenge(in.Challenge)
	if ch == nil {
		loginFailed(ip)
		writeErr(w, http.StatusUnauthorized,
			"prijava je istekla — upiši korisničko ime i lozinku ponovno")
		return
	}
	var secret sql.NullString
	var last int64
	var mustChange int
	if err := s.db.QueryRow(`SELECT totp_secret, totp_last, must_change_pw
		FROM users WHERE uuid=?`, ch.userUUID).Scan(&secret, &last, &mustChange); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok := false
	usedRecovery := false
	if secret.Valid && secret.String != "" {
		if raw, err := totpB32.DecodeString(secret.String); err == nil {
			if step, good := totpVerify(raw, in.Code, time.Now(), last); good {
				s.db.Exec(`UPDATE users SET totp_last=? WHERE uuid=?`, step, ch.userUUID)
				ok = true
			}
		}
	}
	if !ok && s.useRecoveryCode(ch.userUUID, in.Code) {
		ok, usedRecovery = true, true
		addEvent(s, "warning", "Prijava pričuvnim kodom: "+ch.username)
	}
	if !ok {
		loginFailed(ip)
		s.noteLoginFail(ch.username, ip)
		writeErr(w, http.StatusUnauthorized, "kod nije točan")
		return
	}
	loginOK(ip)
	dropChallenge(in.Challenge)
	token, expires, err := s.startSession(ch.userUUID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.noteLoginOK(ch.username, ip)
	s.db.Exec(`UPDATE users SET last_login=datetime('now') WHERE uuid=?`, ch.userUUID)
	var left int
	s.db.QueryRow(`SELECT COUNT(*) FROM totp_recovery WHERE user_uuid=?`,
		ch.userUUID).Scan(&left)
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token, "username": ch.username, "expires_at": expires,
		"must_change_password": mustChange == 1, "role": ch.role,
		"used_recovery": usedRecovery, "recovery_left": left,
	})
}

/* ---------- administrator: reset tuđeg drugog faktora ---------- */

// handleUserTOTPReset isključuje drugi faktor drugom korisniku — kad izgubi
// telefon i pričuvne kodove. Radnja se bilježi jer njome pada zaštita računa.
func (s *server) handleUserTOTPReset(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	var username string
	if err := s.db.QueryRow(`SELECT username FROM users WHERE uuid=?`, uuid).
		Scan(&username); err != nil {
		writeErr(w, http.StatusNotFound, "korisnik ne postoji")
		return
	}
	s.db.Exec(`UPDATE users SET totp_enabled=0, totp_secret=NULL, totp_last=0,
		updated_at=datetime('now') WHERE uuid=?`, uuid)
	s.db.Exec(`DELETE FROM totp_recovery WHERE user_uuid=?`, uuid)
	s.db.Exec(`DELETE FROM sessions WHERE user_uuid=?`, uuid)
	addEvent(s, "warning",
		"Administrator je isključio dvofaktorsku prijavu korisniku "+username)
	writeJSON(w, http.StatusOK, map[string]bool{"reset": true})
}
