package main

import (
	"database/sql"
	"net/http"
	"regexp"
	"strings"
)

// Više korisnika i uloge.
//
// Dosad je postojao jedan račun (`admin`) i tko ga ima, ima sve. Na uređaju
// koji drži mrežu tvrtke to nije dovoljno: tehničar koji dodaje rezervaciju
// ne treba moći prepisati firmware, a netko tko samo gleda stanje ne treba
// moći ništa mijenjati. Uz to, promjene se u dnevniku pripisuju **osobi**, a
// ne svima pod istim imenom.
//
// Tri uloge, namjerno malo — više njih nitko ne bi ispravno postavio:
//
//	admin     sve, uključivo korisnike, API token i nepovratne zahvate
//	operator  svakodnevni rad: mreža, firewall, VPN, DHCP, backup, dijagnostika
//	viewer    samo gledanje (GET)

const (
	roleAdmin    = "admin"
	roleOperator = "operator"
	roleViewer   = "viewer"
)

var roleLabels = map[string]string{
	roleAdmin:    "Administrator — sve, uključivo korisnike i nepovratne zahvate",
	roleOperator: "Operater — svakodnevni rad, bez upravljanja korisnicima",
	roleViewer:   "Pregled — samo gledanje, ništa se ne mijenja",
}

func validRole(r string) bool {
	_, ok := roleLabels[r]
	return ok
}

var reUsername = regexp.MustCompile(`^[a-z][a-z0-9._-]{1,31}$`)

// adminOnlyPath nabraja ono što smije samo administrator. Popis je namjerno
// kratak i drži se dvije vrste opasnosti: preuzimanje potpune kontrole
// (korisnici, API token, lozinka uređaja) i nepovratni zahvati (upis
// firmwarea, dijeljenje diska, vraćanje backupa preko svega).
func adminOnlyPath(path string) bool {
	switch {
	case strings.HasPrefix(path, "/api/v1/users"):
		return true
	case strings.HasPrefix(path, "/api/v1/settings/token"):
		return true
	case path == "/api/v1/system/device-password":
		return true
	case path == "/api/v1/openwrt/flash":
		return true
	case path == "/api/v1/openwrt/datapart":
		return true
	case path == "/api/v1/backup/restore":
		return true
	}
	return false
}

// alwaysAllowedPath su radnje nad vlastitim računom — dopuštene su svakoj
// ulozi, inače se korisnik ne bi mogao ni odjaviti ni promijeniti lozinku.
func alwaysAllowedPath(path string) bool {
	switch path {
	case "/api/v1/auth/logout", "/api/v1/auth/logout-others",
		"/api/v1/auth/password", "/api/v1/auth/session":
		return true
	}
	return false
}

// permitted javlja smije li uloga izvesti poziv.
func permitted(role, method, path string) (bool, string) {
	if alwaysAllowedPath(path) {
		return true, ""
	}
	// Administratorske putanje su zatvorene i za čitanje: popis računa,
	// uloga i zadnjih prijava nije nešto što treba vidjeti tko god je
	// prijavljen, a API token je pročitan jednako opasan kao promijenjen.
	if role != roleAdmin && adminOnlyPath(path) {
		return false, "ovo smije samo administrator"
	}
	switch role {
	case roleAdmin, roleOperator:
		return true, ""
	case roleViewer:
		if method == http.MethodGet {
			return true, ""
		}
		return false, "ovaj račun ima pravo samo gledati"
	}
	return false, "nepoznata uloga"
}

/* ---------- zapisi ---------- */

type userRow struct {
	UUID      string `json:"uuid"`
	Username  string `json:"username"`
	FullName  string `json:"full_name"`
	Role      string `json:"role"`
	Disabled  bool   `json:"disabled"`
	MustChange bool  `json:"must_change_pw"`
	LastLogin string `json:"last_login"`
	Sessions  int    `json:"sessions"`
	CreatedAt string `json:"created_at"`
}

func (s *server) userList() ([]userRow, error) {
	rows, err := s.db.Query(`SELECT u.uuid, u.username, COALESCE(u.full_name,''),
		u.role, u.disabled, u.must_change_pw, COALESCE(u.last_login,''),
		u.created_at,
		(SELECT COUNT(*) FROM sessions se
		   WHERE se.user_uuid = u.uuid AND se.expires_at > datetime('now'))
		FROM users u ORDER BY u.username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []userRow{}
	for rows.Next() {
		var u userRow
		if err := rows.Scan(&u.UUID, &u.Username, &u.FullName, &u.Role,
			&u.Disabled, &u.MustChange, &u.LastLogin, &u.CreatedAt,
			&u.Sessions); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// adminCount broji koliko ima uključenih administratora — zadnji se ne smije
// ni obrisati, ni isključiti, ni degradirati.
func (s *server) adminCount(exceptUUID string) int {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM users
		WHERE role=? AND disabled=0 AND uuid<>?`, roleAdmin, exceptUUID).Scan(&n)
	return n
}

func (s *server) handleUserList(w http.ResponseWriter, r *http.Request) {
	list, err := s.userList()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	roles := []map[string]string{}
	for _, id := range []string{roleAdmin, roleOperator, roleViewer} {
		roles = append(roles, map[string]string{"id": id, "label": roleLabels[id]})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"users": list,
		"roles": roles,
		"me":    s.sessionUser(bearerToken(r)),
	})
}

func (s *server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		FullName string `json:"full_name"`
		Role     string `json:"role"`
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.Username = strings.ToLower(strings.TrimSpace(in.Username))
	if !reUsername.MatchString(in.Username) {
		writeErr(w, http.StatusBadRequest,
			"korisničko ime: mala slova, brojke, točka, crtica ili podvlaka; "+
				"počinje slovom, 2–32 znaka")
		return
	}
	if !validRole(in.Role) {
		writeErr(w, http.StatusBadRequest, "uloga mora biti admin, operator ili viewer")
		return
	}
	if len(in.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "lozinka mora imati bar 8 znakova")
		return
	}
	h, err := hashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// lozinku je postavio netko drugi, pa je korisnik mora promijeniti pri
	// prvoj prijavi — inače je zna dvoje ljudi
	uuid := newUUID()
	_, err = s.db.Exec(`INSERT INTO users
		(uuid, username, pass_hash, must_change_pw, role, full_name)
		VALUES (?,?,?,1,?,?)`, uuid, in.Username, h, in.Role,
		strings.TrimSpace(in.FullName))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "korisnik "+in.Username+" već postoji")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	addEvent(s, "warning", "Dodan korisnik "+in.Username+" ("+in.Role+")")
	writeJSON(w, http.StatusCreated, map[string]any{
		"uuid": uuid, "username": in.Username, "role": in.Role,
	})
}

func (s *server) handleUserUpdate(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	var in struct {
		FullName *string `json:"full_name"`
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	var username, role string
	var disabled int
	err := s.db.QueryRow(`SELECT username, role, disabled FROM users WHERE uuid=?`,
		uuid).Scan(&username, &role, &disabled)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "korisnik ne postoji")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	me := s.sessionUser(bearerToken(r))

	newRole := role
	if in.Role != nil {
		if !validRole(*in.Role) {
			writeErr(w, http.StatusBadRequest, "uloga mora biti admin, operator ili viewer")
			return
		}
		newRole = *in.Role
	}
	newDisabled := disabled == 1
	if in.Disabled != nil {
		newDisabled = *in.Disabled
	}
	// Vlastiti račun se ne smije degradirati ni isključiti: čovjek bi se time
	// zaključao van, a najčešće to ni ne primijeti dok ne bude kasno.
	if username == me && (newRole != roleAdmin || newDisabled) {
		writeErr(w, http.StatusConflict,
			"vlastitom računu ne možeš oduzeti administratorska prava ni isključiti ga")
		return
	}
	// Zadnji administrator mora ostati — inače uređajem više nitko ne upravlja.
	if role == roleAdmin && disabled == 0 && (newRole != roleAdmin || newDisabled) &&
		s.adminCount(uuid) == 0 {
		writeErr(w, http.StatusConflict,
			"ovo je jedini administrator — prvo postavi drugoga")
		return
	}

	if in.FullName != nil {
		s.db.Exec(`UPDATE users SET full_name=?, updated_at=datetime('now') WHERE uuid=?`,
			strings.TrimSpace(*in.FullName), uuid)
	}
	if in.Role != nil {
		s.db.Exec(`UPDATE users SET role=?, updated_at=datetime('now') WHERE uuid=?`,
			newRole, uuid)
	}
	if in.Disabled != nil {
		s.db.Exec(`UPDATE users SET disabled=?, updated_at=datetime('now') WHERE uuid=?`,
			boolSetting(newDisabled), uuid)
		if newDisabled {
			// isključen korisnik ne smije ostati prijavljen
			s.db.Exec(`DELETE FROM sessions WHERE user_uuid=?`, uuid)
		}
	}
	addEvent(s, "warning", "Promijenjen korisnik "+username)
	writeJSON(w, http.StatusOK, map[string]any{"updated": true})
}

func (s *server) handleUserPassword(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	var in struct {
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "lozinka mora imati bar 8 znakova")
		return
	}
	var username string
	if err := s.db.QueryRow(`SELECT username FROM users WHERE uuid=?`, uuid).
		Scan(&username); err != nil {
		writeErr(w, http.StatusNotFound, "korisnik ne postoji")
		return
	}
	h, err := hashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// lozinku zna i onaj tko ju je postavio, pa je korisnik mora promijeniti
	s.db.Exec(`UPDATE users SET pass_hash=?, must_change_pw=1,
		updated_at=datetime('now') WHERE uuid=?`, h, uuid)
	s.db.Exec(`DELETE FROM sessions WHERE user_uuid=?`, uuid)
	addEvent(s, "warning", "Postavljena nova lozinka korisniku "+username)
	writeJSON(w, http.StatusOK, map[string]bool{"changed": true})
}

func (s *server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	var username, role string
	var disabled int
	err := s.db.QueryRow(`SELECT username, role, disabled FROM users WHERE uuid=?`,
		uuid).Scan(&username, &role, &disabled)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "korisnik ne postoji")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if username == s.sessionUser(bearerToken(r)) {
		writeErr(w, http.StatusConflict, "vlastiti račun se ne može obrisati")
		return
	}
	if role == roleAdmin && disabled == 0 && s.adminCount(uuid) == 0 {
		writeErr(w, http.StatusConflict,
			"ovo je jedini administrator — prvo postavi drugoga")
		return
	}
	s.db.Exec(`DELETE FROM sessions WHERE user_uuid=?`, uuid)
	if _, err := s.db.Exec(`DELETE FROM users WHERE uuid=?`, uuid); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	addEvent(s, "warning", "Obrisan korisnik "+username)
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// handleUserSessions odjavljuje sve sesije korisnika — kad se sumnja da je
// nekome ukraden pristup, ili kad netko ode iz tvrtke.
func (s *server) handleUserSessions(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	res, err := s.db.Exec(`DELETE FROM sessions WHERE user_uuid=?`, uuid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	n, _ := res.RowsAffected()
	writeJSON(w, http.StatusOK, map[string]any{"closed": n})
}
