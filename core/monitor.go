package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Nadzor: ping praćenje hostova (netwatch), dnevnik događaja, alarm na
// nepoznat uređaj u mreži i potrošnja prometa po uređaju (nlbwmon).
// Obavijesti idu e-mailom (SMTP postavke u settings tablici).

/* ---------- događaji + e-mail ---------- */

func addEvent(s *server, level, message string) {
	s.db.Exec(`INSERT INTO events (level, message) VALUES (?,?)`, level, message)
	s.db.Exec(`DELETE FROM events WHERE id NOT IN
		(SELECT id FROM events ORDER BY id DESC LIMIT 500)`)
	go s.sendMail("Saguaro obavijest", message)
}

// sendMail šalje obavijest ako su SMTP postavke popunjene (best effort).
func (s *server) sendMail(subject, body string) {
	host := s.getSetting("smtp_host", "")
	to := s.getSetting("smtp_to", "")
	if host == "" || to == "" || s.getSetting("notify_email", "0") != "1" {
		return
	}
	port := s.getSetting("smtp_port", "587")
	from := s.getSetting("smtp_from", "saguaro@localhost")
	user := s.getSetting("smtp_user", "")
	pass := s.getSetting("smtp_pass", "")

	msg := []byte("From: " + from + "\r\nTo: " + to +
		"\r\nSubject: " + subject + "\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n")
	var auth smtp.Auth
	if user != "" {
		auth = smtp.PlainAuth("", user, pass, host)
	}
	if err := smtp.SendMail(host+":"+port, auth, from,
		strings.Fields(to), msg); err != nil {
		s.db.Exec(`INSERT INTO events (level, message) VALUES ('warning', ?)`,
			"Slanje e-maila nije uspjelo: "+err.Error())
	}
}

func (s *server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	if s.getSetting("smtp_host", "") == "" || s.getSetting("smtp_to", "") == "" {
		writeErr(w, http.StatusBadRequest, "prvo spremi SMTP postavke")
		return
	}
	host := s.getSetting("smtp_host", "")
	port := s.getSetting("smtp_port", "587")
	from := s.getSetting("smtp_from", "saguaro@localhost")
	user := s.getSetting("smtp_user", "")
	pass := s.getSetting("smtp_pass", "")
	to := s.getSetting("smtp_to", "")
	msg := []byte("From: " + from + "\r\nTo: " + to +
		"\r\nSubject: Saguaro — probna poruka\r\n\r\n" +
		"Obavijesti s uređaja rade.\r\n")
	var auth smtp.Auth
	if user != "" {
		auth = smtp.PlainAuth("", user, pass, host)
	}
	if err := smtp.SendMail(host+":"+port, auth, from,
		strings.Fields(to), msg); err != nil {
		writeErr(w, http.StatusBadGateway, "slanje nije uspjelo: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"sent": true})
}

func (s *server) handleSMTPSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled *bool  `json:"enabled"`
		Host    string `json:"host"`
		Port    int    `json:"port"`
		User    string `json:"user"`
		Pass    string `json:"pass"`
		From    string `json:"from"`
		To      string `json:"to"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "nedostaje polje enabled")
		return
	}
	if in.Port == 0 {
		in.Port = 587
	}
	vals := map[string]string{
		"notify_email": map[bool]string{true: "1", false: "0"}[*in.Enabled],
		"smtp_host":    strings.TrimSpace(in.Host),
		"smtp_port":    strconv.Itoa(in.Port),
		"smtp_user":    strings.TrimSpace(in.User),
		"smtp_from":    strings.TrimSpace(in.From),
		"smtp_to":      strings.TrimSpace(in.To),
	}
	if in.Pass != "" { // prazna lozinka = zadrži postojeću
		vals["smtp_pass"] = in.Pass
	}
	for k, v := range vals {
		if err := s.setSetting(k, v); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"saved": true})
}

/* ---------- netwatch petlja ---------- */

func pingOK(ctx context.Context, ip string) bool {
	c, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	return exec.CommandContext(c, "ping", "-c", "1", "-W", "2", ip).Run() == nil
}

// monitorLoop svake minute provjerava praćene hostove i nove MAC adrese.
func (s *server) monitorLoop() {
	for {
		ctx := context.Background()

		rows, err := s.db.Query(`SELECT uuid, name, ip, last_ok FROM nw_monitors
			WHERE enabled = 1`)
		if err == nil {
			type mon struct {
				uuid, name, ip string
				lastOK         *int64
			}
			mons := []mon{}
			for rows.Next() {
				var m mon
				if rows.Scan(&m.uuid, &m.name, &m.ip, &m.lastOK) == nil {
					mons = append(mons, m)
				}
			}
			rows.Close()
			for _, m := range mons {
				ok := pingOK(ctx, m.ip)
				okInt := int64(0)
				if ok {
					okInt = 1
				}
				if m.lastOK == nil || *m.lastOK != okInt {
					s.db.Exec(`UPDATE nw_monitors SET last_ok=?,
						last_change=datetime('now') WHERE uuid=?`, okInt, m.uuid)
					if m.lastOK != nil { // prva provjera nije "promjena"
						state := "ponovno dostupan"
						level := "info"
						if !ok {
							state = "NE ODGOVARA"
							level = "warning"
						}
						addEvent(s, level, fmt.Sprintf("Nadzor: %s (%s) %s",
							m.name, m.ip, state))
					}
				}
			}
		}

		// nepoznati uređaji: nova MAC adresa u DHCP leaseovima
		if s.getSetting("unknown_alert", "0") == "1" {
			for _, l := range parseLeases(leaseFile) {
				var n int
				s.db.QueryRow(`SELECT COUNT(*) FROM seen_macs WHERE mac=?`,
					l.MAC).Scan(&n)
				if n > 0 {
					continue
				}
				s.db.Exec(`INSERT OR IGNORE INTO seen_macs (mac) VALUES (?)`, l.MAC)
				var known int
				s.db.QueryRow(`SELECT COUNT(*) FROM hosts WHERE mac=?`,
					l.MAC).Scan(&known)
				if known == 0 {
					name := l.Hostname
					if name == "" {
						name = "bez imena"
					}
					addEvent(s, "warning", fmt.Sprintf(
						"Novi nepoznat uređaj u mreži: %s (%s, %s)",
						name, l.MAC, l.IP))
				}
			}
		}

		time.Sleep(60 * time.Second)
	}
}

/* ---------- API: monitori + događaji ---------- */

func (s *server) handleMonitorGet(w http.ResponseWriter, r *http.Request) {
	type mon struct {
		UUID       string `json:"uuid"`
		Name       string `json:"name"`
		IP         string `json:"ip"`
		Enabled    bool   `json:"enabled"`
		LastOK     *int64 `json:"last_ok"`
		LastChange string `json:"last_change"`
	}
	mons := []mon{}
	rows, err := s.db.Query(`SELECT uuid, name, ip, enabled, last_ok,
		COALESCE(last_change,'') FROM nw_monitors ORDER BY name`)
	if err == nil {
		for rows.Next() {
			var m mon
			if rows.Scan(&m.UUID, &m.Name, &m.IP, &m.Enabled, &m.LastOK,
				&m.LastChange) == nil {
				mons = append(mons, m)
			}
		}
		rows.Close()
	}

	type ev struct {
		TS      string `json:"ts"`
		Level   string `json:"level"`
		Message string `json:"message"`
	}
	evs := []ev{}
	rows, err = s.db.Query(`SELECT ts, level, message FROM events
		ORDER BY id DESC LIMIT 50`)
	if err == nil {
		for rows.Next() {
			var e ev
			if rows.Scan(&e.TS, &e.Level, &e.Message) == nil {
				evs = append(evs, e)
			}
		}
		rows.Close()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"monitors":      mons,
		"events":        evs,
		"unknown_alert": s.getSetting("unknown_alert", "0") == "1",
		"email": map[string]any{
			"enabled": s.getSetting("notify_email", "0") == "1",
			"host":    s.getSetting("smtp_host", ""),
			"port":    s.getSetting("smtp_port", "587"),
			"user":    s.getSetting("smtp_user", ""),
			"from":    s.getSetting("smtp_from", ""),
			"to":      s.getSetting("smtp_to", ""),
		},
	})
}

func (s *server) handleMonitorCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string `json:"name"`
		IP      string `json:"ip"`
		Enabled *bool  `json:"enabled"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.IP = strings.TrimSpace(in.IP)
	if in.Name == "" {
		writeErr(w, http.StatusBadRequest, "naziv je obavezan")
		return
	}
	if net.ParseIP(in.IP) == nil {
		writeErr(w, http.StatusBadRequest, "neispravna IP adresa")
		return
	}
	uuid := newUUID()
	en := 1
	if in.Enabled != nil && !*in.Enabled {
		en = 0
	}
	if _, err := s.db.Exec(`INSERT INTO nw_monitors (uuid, name, ip, enabled)
		VALUES (?,?,?,?)`, uuid, in.Name, in.IP, en); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"uuid": uuid})
}

func (s *server) handleMonitorDelete(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.Exec(`DELETE FROM nw_monitors WHERE uuid=?`, r.PathValue("uuid"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "monitor ne postoji")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": r.PathValue("uuid")})
}

func (s *server) handleMonitorSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UnknownAlert *bool `json:"unknown_alert"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.UnknownAlert != nil {
		if *in.UnknownAlert {
			// postojeće leaseove upiši kao viđene da ne okine lavinu alarma
			for _, l := range parseLeases(leaseFile) {
				s.db.Exec(`INSERT OR IGNORE INTO seen_macs (mac) VALUES (?)`, l.MAC)
			}
			s.setSetting("unknown_alert", "1")
		} else {
			s.setSetting("unknown_alert", "0")
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"unknown_alert": s.getSetting("unknown_alert", "0") == "1",
	})
}

/* ---------- sustavski log (prikaz) ---------- */

// handleSyslogView vraća zadnje linije sustavskog loga (logread) — samo za
// prikaz u GUI-ju, bez parsiranja odluka iz teksta.
func (s *server) handleSyslogView(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "logread", "-l", "150").Output()
	if err != nil {
		writeErr(w, http.StatusBadGateway, "logread: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"log": string(out)})
}

/* ---------- potrošnja prometa (nlbwmon) ---------- */

// handleTraffic vraća top potrošače iz nlbwmon-a (CSV izlaz `nlbw` alata —
// strojni format; nlbwmon nema ubus sučelje).
func (s *server) handleTraffic(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nlbw", "-c", "csv", "-g", "ip",
		"-o", "-rx_bytes").Output()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false, "hosts": []any{},
		})
		return
	}
	type th struct {
		IP      string `json:"ip"`
		RxBytes int64  `json:"rx_bytes"`
		TxBytes int64  `json:"tx_bytes"`
		Conns   int64  `json:"conns"`
	}
	hosts := []th{}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	colIdx := map[string]int{}
	for i, line := range lines {
		f := strings.Split(line, ";")
		if i == 0 {
			for n, c := range f {
				colIdx[strings.Trim(c, "\"")] = n
			}
			continue
		}
		get := func(col string) string {
			if idx, ok := colIdx[col]; ok && idx < len(f) {
				return strings.Trim(f[idx], "\"")
			}
			return ""
		}
		rx, _ := strconv.ParseInt(get("rx_bytes"), 10, 64)
		tx, _ := strconv.ParseInt(get("tx_bytes"), 10, 64)
		cn, _ := strconv.ParseInt(get("conns"), 10, 64)
		ip := get("ip")
		if ip == "" {
			continue
		}
		hosts = append(hosts, th{IP: ip, RxBytes: rx, TxBytes: tx, Conns: cn})
		if len(hosts) >= 15 {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "hosts": hosts})
}
