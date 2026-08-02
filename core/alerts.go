package main

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/smtp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Sustav upozorenja: jedno mjesto kroz koje prolaze sve obavijesti.
// Svaka vrsta upozorenja može se zasebno uključiti, a ista se poruka ne
// ponavlja češće od zadanog razmaka — inače bi jedan pokvaren link zatrpao
// sandučić stotinama poruka.

type alertKind struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Default bool   `json:"default"`
}

// alertKinds je popis svega o čemu uređaj može javiti. Redoslijed je i
// redoslijed prikaza u sučelju.
var alertKinds = []alertKind{
	{"wan", "Internet veza je pala ili se vratila", true},
	{"pubip", "Javna IP adresa se promijenila", true},
	{"cgnat", "Uređaj je iza operaterskog NAT-a (CGNAT)", true},
	{"vpn_service", "VPN poslužitelj (WireGuard/OpenVPN) je prestao raditi", true},
	{"vpn_client", "VPN korisnik se spojio ili odspojio", false},
	{"reboot", "Uređaj se ponovno pokrenuo", true},
	{"config", "Promijenjena je konfiguracija (firewall, VPN, WAN)", false},
	{"login", "Netko se prijavio (Saguaro ili SSH)", false},
	{"login_failed", "Veći broj neuspjelih prijava", true},
	{"resources", "Zauzeće procesora, memorije ili diska je previsoko", true},
	{"backup", "Automatski backup nije uspio", true},
	{"cert", "VPN certifikat uskoro istječe", true},
	{"monitor", "Nadzirani uređaj ne odgovara", true},
	{"unknown_mac", "U mreži se pojavio nepoznat uređaj", false},
}

func alertKindValid(id string) bool {
	for _, k := range alertKinds {
		if k.ID == id {
			return true
		}
	}
	return false
}

// alertEnabled kaže šalje li se e-mail za tu vrstu. Zapis u events se piše
// uvijek — dnevnik je potpun i kad obavijesti nisu uključene.
func (s *server) alertEnabled(kind string) bool {
	def := "0"
	for _, k := range alertKinds {
		if k.ID == kind && k.Default {
			def = "1"
		}
	}
	return s.getSetting("alert_"+kind, def) == "1"
}

// alertQuiet je najmanji razmak između dviju istovjetnih poruka.
func (s *server) alertQuiet() time.Duration {
	n, err := strconv.Atoi(s.getSetting("alert_quiet_min", "30"))
	if err != nil || n < 1 {
		n = 30
	}
	return time.Duration(n) * time.Minute
}

// alert zapisuje događaj i, ako je ta vrsta uključena, šalje e-mail.
// Istovjetna poruka unutar tihog razdoblja preskače slanje (događaj se
// svejedno zapiše, pa se u dnevniku vidi da se ponovila).
func (s *server) alert(kind, level, message string) {
	s.db.Exec(`INSERT INTO events (level, message) VALUES (?,?)`, level, message)
	s.db.Exec(`DELETE FROM events WHERE id NOT IN
		(SELECT id FROM events ORDER BY id DESC LIMIT 500)`)
	if !s.alertEnabled(kind) {
		return
	}
	sum := sha256.Sum256([]byte(kind + "|" + message))
	key := "mail:" + hex.EncodeToString(sum[:8])
	if !s.alertDue(key, s.alertQuiet()) {
		return
	}
	subject := "Saguaro"
	if h := s.getSetting("device_label", ""); h != "" {
		subject += " (" + h + ")"
	}
	go s.sendMail(subject+" — "+alertSubject(kind), message)
}

func alertSubject(kind string) string {
	for _, k := range alertKinds {
		if k.ID == kind {
			return k.Label
		}
	}
	return "obavijest"
}

// alertDue javlja smije li se poruka poslati i odmah pamti trenutak slanja.
func (s *server) alertDue(key string, quiet time.Duration) bool {
	var last string
	err := s.db.QueryRow(`SELECT last_sent FROM alert_state WHERE key=?`,
		key).Scan(&last)
	if err == nil && last != "" {
		if t, perr := time.Parse("2006-01-02 15:04:05", last); perr == nil {
			if time.Since(t.UTC()) < quiet {
				return false
			}
		}
	}
	s.db.Exec(`INSERT INTO alert_state (key, last_sent) VALUES (?, datetime('now'))
		ON CONFLICT(key) DO UPDATE SET last_sent=datetime('now')`, key)
	return true
}

// alertValue pamti zadnje viđeno stanje (npr. javnu IP adresu) i javlja je li
// se promijenilo. Prvi poziv nikad nije "promjena" — samo zapamti vrijednost.
func (s *server) alertValue(key, value string) (changed bool, previous string) {
	err := s.db.QueryRow(`SELECT value FROM alert_state WHERE key=?`,
		key).Scan(&previous)
	first := err != nil
	if previous != value {
		s.db.Exec(`INSERT INTO alert_state (key, value, updated_at)
			VALUES (?,?,datetime('now'))
			ON CONFLICT(key) DO UPDATE SET value=excluded.value,
			updated_at=datetime('now')`, key, value)
	}
	return !first && previous != value, previous
}

/* ---------- slanje e-maila ---------- */

// sendMail šalje obavijest ako su SMTP postavke popunjene (best effort).
// Veza je obavezno šifrirana: port 465 znači TLS od prve sekunde, inače se
// traži STARTTLS i slanje se odustaje ako ga poslužitelj ne nudi — lozinka
// SMTP računa ne smije putovati u čistom obliku.
func (s *server) sendMail(subject, body string) {
	host := s.getSetting("smtp_host", "")
	to := s.getSetting("smtp_to", "")
	if host == "" || to == "" || s.getSetting("notify_email", "0") != "1" {
		return
	}
	if err := s.smtpSend(subject, body); err != nil {
		s.db.Exec(`INSERT INTO events (level, message) VALUES ('warning', ?)`,
			"Slanje e-maila nije uspjelo: "+err.Error())
	}
}

func (s *server) smtpSend(subject, body string) error {
	host := s.getSetting("smtp_host", "")
	port := s.getSetting("smtp_port", "587")
	from := s.getSetting("smtp_from", "saguaro@localhost")
	user := s.getSetting("smtp_user", "")
	pass := s.getSetting("smtp_pass", "")
	to := strings.Fields(s.getSetting("smtp_to", ""))
	if host == "" || len(to) == 0 {
		return fmt.Errorf("SMTP postavke nisu popunjene")
	}
	insecure := s.getSetting("smtp_insecure", "0") == "1"

	msg := []byte("From: " + from + "\r\nTo: " + strings.Join(to, ", ") +
		"\r\nSubject: " + mimeHeader(subject) + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n")

	addr := host + ":" + port
	tlsCfg := &tls.Config{ServerName: host, InsecureSkipVerify: insecure}

	var c *smtp.Client
	var err error
	if port == "465" { // implicitni TLS
		conn, derr := tls.Dial("tcp", addr, tlsCfg)
		if derr != nil {
			return derr
		}
		c, err = smtp.NewClient(conn, host)
	} else {
		c, err = smtp.Dial(addr)
	}
	if err != nil {
		return err
	}
	defer c.Close()

	if port != "465" {
		ok, _ := c.Extension("STARTTLS")
		if !ok {
			return fmt.Errorf("poslužitelj ne nudi šifriranu vezu (STARTTLS) " +
				"— provjeri port (587 ili 465)")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return err
		}
	}
	if user != "" {
		if err := c.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write(msg); err != nil {
		wc.Close()
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// mimeHeader kodira zaglavlje ako sadrži znakove izvan ASCII-ja (naša slova).
func mimeHeader(s string) string {
	for _, r := range s {
		if r > 127 {
			return "=?UTF-8?B?" + b64(s) + "?="
		}
	}
	return s
}

/* ---------- postavke upozorenja ---------- */

func (s *server) handleAlertsGet(w http.ResponseWriter, r *http.Request) {
	kinds := make([]map[string]any, 0, len(alertKinds))
	for _, k := range alertKinds {
		kinds = append(kinds, map[string]any{
			"id": k.ID, "label": k.Label, "enabled": s.alertEnabled(k.ID),
		})
	}
	// pragovi i zadnje poznato stanje (za prikaz)
	state := map[string]string{}
	rows, err := s.db.Query(`SELECT key, COALESCE(value,'') FROM alert_state
		WHERE key NOT LIKE 'mail:%'`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var k, v string
			if rows.Scan(&k, &v) == nil && v != "" {
				state[k] = v
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"kinds":        kinds,
		"quiet_min":    s.getSetting("alert_quiet_min", "30"),
		"cpu_pct":      s.getSetting("alert_cpu_pct", "90"),
		"mem_pct":      s.getSetting("alert_mem_pct", "90"),
		"disk_pct":     s.getSetting("alert_disk_pct", "90"),
		"cert_days":    s.getSetting("alert_cert_days", "30"),
		"state":        state,
		"device_label": s.getSetting("device_label", ""),
		"public_ip":    s.getSetting("public_ip", ""),
		"public_ip6":   s.getSetting("public_ip6", ""),
	})
}

func (s *server) handleAlertsSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kinds     map[string]bool `json:"kinds"`
		QuietMin  int             `json:"quiet_min"`
		CPUPct    int             `json:"cpu_pct"`
		MemPct    int             `json:"mem_pct"`
		DiskPct   int             `json:"disk_pct"`
		CertDays  int             `json:"cert_days"`
		DeviceTag string          `json:"device_label"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	for id := range in.Kinds {
		if !alertKindValid(id) {
			writeErr(w, http.StatusBadRequest, "nepoznata vrsta upozorenja: "+id)
			return
		}
	}
	if in.QuietMin < 1 || in.QuietMin > 1440 {
		writeErr(w, http.StatusBadRequest,
			"razmak između istih poruka mora biti 1–1440 minuta")
		return
	}
	for _, p := range []struct {
		v     int
		label string
	}{{in.CPUPct, "procesor"}, {in.MemPct, "memorija"}, {in.DiskPct, "disk"}} {
		if p.v < 50 || p.v > 100 {
			writeErr(w, http.StatusBadRequest,
				"prag za "+p.label+" mora biti između 50 i 100 posto")
			return
		}
	}
	if in.CertDays < 1 || in.CertDays > 365 {
		writeErr(w, http.StatusBadRequest,
			"upozorenje o certifikatu mora biti 1–365 dana unaprijed")
		return
	}
	if hasCtrl(in.DeviceTag) || len([]rune(in.DeviceTag)) > 40 {
		writeErr(w, http.StatusBadRequest,
			"oznaka uređaja: najviše 40 znakova, bez prijeloma retka")
		return
	}

	ids := make([]string, 0, len(in.Kinds))
	for id := range in.Kinds {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := s.setSetting("alert_"+id, boolSetting(in.Kinds[id])); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	for k, v := range map[string]string{
		"alert_quiet_min": strconv.Itoa(in.QuietMin),
		"alert_cpu_pct":   strconv.Itoa(in.CPUPct),
		"alert_mem_pct":   strconv.Itoa(in.MemPct),
		"alert_disk_pct":  strconv.Itoa(in.DiskPct),
		"alert_cert_days": strconv.Itoa(in.CertDays),
		"device_label":    strings.TrimSpace(in.DeviceTag),
	} {
		if err := s.setSetting(k, v); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"saved": true})
}

func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
