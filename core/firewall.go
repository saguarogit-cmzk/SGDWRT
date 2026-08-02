package main

import (
	"fmt"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Saguaro u fw4 upravlja isključivo vlastitim sekcijama: sag_pf_* (redirect)
// i sag_rl_* (rule). WireGuard modul ima svoje sag_wg_* sekcije koje se ovdje
// ne diraju, kao ni ručne/LuCI sekcije (D-011).
const pfPrefix = "sag_pf_"
const rlPrefix = "sag_rl_"

/* ---------- validacija ---------- */

var rePort = regexp.MustCompile(`^\d{1,5}(-\d{1,5})?$`)
var reZone = regexp.MustCompile(`^[a-zA-Z0-9_]{1,32}$`)

func validPortSpec(s string) bool {
	if !rePort.MatchString(s) {
		return false
	}
	for _, p := range strings.SplitN(s, "-", 2) {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

// validAddr prihvaća IP ili CIDR.
func validAddr(s string) bool {
	if net.ParseIP(s) != nil {
		return true
	}
	_, _, err := net.ParseCIDR(s)
	return err == nil
}

/* ---------- modeli ---------- */

type FWForward struct {
	UUID       string `json:"uuid"`
	Name       string `json:"name"`
	Proto      string `json:"proto"`
	SrcZone    string `json:"src_zone"`
	SrcDport   string `json:"src_dport"`
	DestZone   string `json:"dest_zone"`
	DestIP     string `json:"dest_ip"`
	DestPort   string `json:"dest_port"`
	SrcDIP     string `json:"src_dip"`
	Reflection bool   `json:"reflection"`
	Enabled    bool   `json:"enabled"`
	Notes      string `json:"notes"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

const fwdCols = `uuid, name, proto, src_zone, src_dport, dest_zone, dest_ip,
	COALESCE(dest_port,''), COALESCE(src_dip,''), reflection, enabled,
	COALESCE(notes,''), created_at, updated_at`

func scanForward(row interface{ Scan(...any) error }) (FWForward, error) {
	var f FWForward
	err := row.Scan(&f.UUID, &f.Name, &f.Proto, &f.SrcZone, &f.SrcDport,
		&f.DestZone, &f.DestIP, &f.DestPort, &f.SrcDIP, &f.Reflection, &f.Enabled,
		&f.Notes, &f.CreatedAt, &f.UpdatedAt)
	return f, err
}

type FWRule struct {
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	Family    string `json:"family"`
	Proto     string `json:"proto"`
	SrcZone   string `json:"src_zone"`
	SrcIP     string `json:"src_ip"`
	DestZone  string `json:"dest_zone"`
	DestIP    string `json:"dest_ip"`
	DestPort  string `json:"dest_port"`
	Target    string `json:"target"`
	StartTime string `json:"start_time"`
	StopTime  string `json:"stop_time"`
	Weekdays  string `json:"weekdays"`
	Enabled   bool   `json:"enabled"`
	Notes     string `json:"notes"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

const ruleCols = `uuid, name, family, proto, src_zone, COALESCE(src_ip,''),
	COALESCE(dest_zone,''), COALESCE(dest_ip,''), COALESCE(dest_port,''),
	target, COALESCE(start_time,''), COALESCE(stop_time,''),
	COALESCE(weekdays,''), enabled, COALESCE(notes,''), created_at, updated_at`

func scanRule(row interface{ Scan(...any) error }) (FWRule, error) {
	var f FWRule
	err := row.Scan(&f.UUID, &f.Name, &f.Family, &f.Proto, &f.SrcZone, &f.SrcIP,
		&f.DestZone, &f.DestIP, &f.DestPort, &f.Target, &f.StartTime,
		&f.StopTime, &f.Weekdays, &f.Enabled, &f.Notes,
		&f.CreatedAt, &f.UpdatedAt)
	return f, err
}

var reHHMM = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

var validDays = map[string]bool{"mon": true, "tue": true, "wed": true,
	"thu": true, "fri": true, "sat": true, "sun": true}

// validAddrOrAlias prihvaća IP/CIDR ili @alias.
func (s *server) validAddrOrAlias(v string) bool {
	if strings.HasPrefix(v, "@") {
		_, err := s.resolveAlias(v)
		return err == nil
	}
	return validAddr(v)
}

func (s *server) validateForward(w http.ResponseWriter, f *FWForward) bool {
	f.Name = strings.TrimSpace(f.Name)
	f.Proto = strings.TrimSpace(f.Proto)
	f.SrcZone = strings.TrimSpace(f.SrcZone)
	f.SrcDport = strings.TrimSpace(f.SrcDport)
	f.DestZone = strings.TrimSpace(f.DestZone)
	f.DestIP = strings.TrimSpace(f.DestIP)
	f.DestPort = strings.TrimSpace(f.DestPort)
	f.SrcDIP = strings.TrimSpace(f.SrcDIP)
	if f.Proto == "" {
		f.Proto = "tcp udp"
	}
	if f.SrcZone == "" {
		f.SrcZone = "wan"
	}
	if f.DestZone == "" {
		f.DestZone = "lan"
	}
	switch {
	case f.Name == "":
		writeErr(w, http.StatusBadRequest, "naziv je obavezan")
	case f.Proto != "tcp" && f.Proto != "udp" && f.Proto != "tcp udp":
		writeErr(w, http.StatusBadRequest, "protokol mora biti tcp, udp ili tcp udp")
	case !reZone.MatchString(f.SrcZone) || !reZone.MatchString(f.DestZone):
		writeErr(w, http.StatusBadRequest, "neispravno ime zone")
	case !validPortSpec(f.SrcDport):
		writeErr(w, http.StatusBadRequest, "neispravan vanjski port (npr. 443 ili 8000-8010)")
	case !strings.HasPrefix(f.DestIP, "@") && net.ParseIP(f.DestIP) == nil:
		writeErr(w, http.StatusBadRequest,
			"neispravna odredišna IP adresa (IP ili @alias s jednom adresom)")
	case strings.HasPrefix(f.DestIP, "@") && !s.aliasSingleIP(f.DestIP):
		writeErr(w, http.StatusBadRequest,
			"za forward alias mora postojati i imati točno jednu IP adresu")
	case f.DestPort != "" && !validPortSpec(f.DestPort):
		writeErr(w, http.StatusBadRequest, "neispravan odredišni port")
	case f.SrcDIP != "" && net.ParseIP(f.SrcDIP) == nil:
		writeErr(w, http.StatusBadRequest, "neispravna javna IP adresa (src_dip)")
	default:
		return true
	}
	return false
}

func (s *server) validateRule(w http.ResponseWriter, f *FWRule) bool {
	f.Name = strings.TrimSpace(f.Name)
	f.Family = strings.TrimSpace(f.Family)
	f.Proto = strings.TrimSpace(f.Proto)
	f.SrcZone = strings.TrimSpace(f.SrcZone)
	f.SrcIP = strings.TrimSpace(f.SrcIP)
	f.DestZone = strings.TrimSpace(f.DestZone)
	f.DestIP = strings.TrimSpace(f.DestIP)
	f.DestPort = strings.TrimSpace(f.DestPort)
	f.Target = strings.ToUpper(strings.TrimSpace(f.Target))
	f.StartTime = strings.TrimSpace(f.StartTime)
	f.StopTime = strings.TrimSpace(f.StopTime)
	f.Weekdays = strings.ToLower(strings.TrimSpace(f.Weekdays))
	if f.Family == "" {
		f.Family = "any"
	}
	if f.Proto == "" {
		f.Proto = "tcp udp"
	}
	if f.SrcZone == "" {
		f.SrcZone = "wan"
	}
	if f.Target == "" {
		f.Target = "ACCEPT"
	}
	protoOK := map[string]bool{"tcp": true, "udp": true, "tcp udp": true,
		"icmp": true, "all": true}[f.Proto]
	switch {
	case f.Name == "":
		writeErr(w, http.StatusBadRequest, "naziv je obavezan")
	case f.Family != "any" && f.Family != "ipv4" && f.Family != "ipv6":
		writeErr(w, http.StatusBadRequest, "family mora biti any, ipv4 ili ipv6")
	case !protoOK:
		writeErr(w, http.StatusBadRequest, "neispravan protokol")
	case f.SrcZone != "*" && !reZone.MatchString(f.SrcZone):
		writeErr(w, http.StatusBadRequest, "neispravna izvorišna zona")
	case f.SrcIP != "" && !s.validAddrOrAlias(f.SrcIP):
		writeErr(w, http.StatusBadRequest,
			"neispravna izvorišna adresa (IP, CIDR ili postojeći @alias)")
	case f.DestZone != "" && f.DestZone != "*" && !reZone.MatchString(f.DestZone):
		writeErr(w, http.StatusBadRequest, "neispravna odredišna zona")
	case f.DestIP != "" && !s.validAddrOrAlias(f.DestIP):
		writeErr(w, http.StatusBadRequest,
			"neispravna odredišna adresa (IP, CIDR ili postojeći @alias)")
	case f.DestPort != "" && !validPortSpec(f.DestPort):
		writeErr(w, http.StatusBadRequest, "neispravan odredišni port")
	case f.Target != "ACCEPT" && f.Target != "REJECT" && f.Target != "DROP":
		writeErr(w, http.StatusBadRequest, "akcija mora biti ACCEPT, REJECT ili DROP")
	case f.StartTime != "" && !reHHMM.MatchString(f.StartTime):
		writeErr(w, http.StatusBadRequest, "vrijeme od mora biti HH:MM")
	case f.StopTime != "" && !reHHMM.MatchString(f.StopTime):
		writeErr(w, http.StatusBadRequest, "vrijeme do mora biti HH:MM")
	case (f.StartTime == "") != (f.StopTime == ""):
		writeErr(w, http.StatusBadRequest, "vremensko ograničenje traži oba vremena (od i do)")
	default:
		for _, d := range strings.Fields(f.Weekdays) {
			if !validDays[d] {
				writeErr(w, http.StatusBadRequest,
					"dani moraju biti mon tue wed thu fri sat sun ("+d+")")
				return false
			}
		}
		return true
	}
	return false
}

/* ---------- status ---------- */

type fwZone struct {
	Section  string   `json:"section"`
	Name     string   `json:"name"`
	Input    string   `json:"input"`
	Output   string   `json:"output"`
	Forward  string   `json:"forward"`
	Masq     bool     `json:"masq"`
	Networks []string `json:"networks"`
	Managed  bool     `json:"managed_by_saguaro"`
}

func sectList(sec uciSection, key string) []string {
	out := []string{}
	switch v := sec[key].(type) {
	case string:
		out = append(out, v)
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func (s *server) handleFWStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := uciGetConfig(r.Context(), "firewall")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	zones := []fwZone{}
	defaults := map[string]any{}
	type sect struct {
		Section string `json:"section"`
		Name    string `json:"name"`
		Managed bool   `json:"managed_by_saguaro"`
	}
	redirects := []sect{}
	rules := []sect{}
	for name, sec := range cfg {
		switch sectStr(sec, ".type") {
		case "defaults":
			defaults = map[string]any{
				"input":   sectStr(sec, "input"),
				"output":  sectStr(sec, "output"),
				"forward": sectStr(sec, "forward"),
			}
		case "zone":
			zones = append(zones, fwZone{
				Section: name, Name: sectStr(sec, "name"),
				Input: sectStr(sec, "input"), Output: sectStr(sec, "output"),
				Forward:  sectStr(sec, "forward"),
				Masq:     sectStr(sec, "masq") == "1",
				Networks: sectList(sec, "network"),
				Managed:  strings.HasPrefix(name, sagPrefix),
			})
		case "redirect":
			if name == "sag_dmz" { // DMZ ima vlastiti pregled i primjenu
				continue
			}
			redirects = append(redirects, sect{name, sectStr(sec, "name"),
				strings.HasPrefix(name, pfPrefix) || strings.HasPrefix(name, n1dPrefix)})
		case "rule":
			if strings.HasPrefix(name, vrPrefix) { // VPN pristupna pravila — WireGuard tab
				continue
			}
			rules = append(rules, sect{name, sectStr(sec, "name"),
				strings.HasPrefix(name, rlPrefix)})
		}
	}
	sort.Slice(zones, func(i, j int) bool { return zones[i].Name < zones[j].Name })
	sort.Slice(redirects, func(i, j int) bool { return redirects[i].Name < redirects[j].Name })
	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })

	writeJSON(w, http.StatusOK, map[string]any{
		"defaults":  defaults,
		"zones":     zones,
		"redirects": redirects,
		"rules":     rules,
	})
}

/* ---------- CRUD: port forwardi ---------- */

type fwForwardIn struct {
	FWForward
	Enabled    *bool `json:"enabled"`
	Reflection *bool `json:"reflection"`
}

func enabledIntOf(e *bool) int {
	if e == nil || *e {
		return 1
	}
	return 0
}

func (s *server) handleFWForwardList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT ` + fwdCols + ` FROM fw_forwards ORDER BY src_dport`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	out := []FWForward{}
	for rows.Next() {
		f, err := scanForward(rows)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, f)
	}
	writeJSON(w, http.StatusOK, map[string]any{"forwards": out})
}

func (s *server) handleFWForwardCreate(w http.ResponseWriter, r *http.Request) {
	var in fwForwardIn
	if !decodeBody(w, r, &in) {
		return
	}
	f := &in.FWForward
	if !s.validateForward(w, f) {
		return
	}
	f.UUID = newUUID()
	_, err := s.db.Exec(`INSERT INTO fw_forwards
		(uuid, name, proto, src_zone, src_dport, dest_zone, dest_ip, dest_port,
		 src_dip, reflection, enabled, notes) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.UUID, f.Name, f.Proto, f.SrcZone, f.SrcDport, f.DestZone, f.DestIP,
		f.DestPort, f.SrcDIP, enabledIntOf(in.Reflection),
		enabledIntOf(in.Enabled), f.Notes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ff, _ := scanForward(s.db.QueryRow(
		`SELECT `+fwdCols+` FROM fw_forwards WHERE uuid=?`, f.UUID))
	writeJSON(w, http.StatusCreated, ff)
}

func (s *server) handleFWForwardUpdate(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	var in fwForwardIn
	if !decodeBody(w, r, &in) {
		return
	}
	f := &in.FWForward
	if !s.validateForward(w, f) {
		return
	}
	res, err := s.db.Exec(`UPDATE fw_forwards SET name=?, proto=?, src_zone=?,
		src_dport=?, dest_zone=?, dest_ip=?, dest_port=?, src_dip=?, reflection=?,
		enabled=?, notes=?, updated_at=datetime('now') WHERE uuid=?`,
		f.Name, f.Proto, f.SrcZone, f.SrcDport, f.DestZone, f.DestIP, f.DestPort,
		f.SrcDIP, enabledIntOf(in.Reflection), enabledIntOf(in.Enabled), f.Notes, uuid)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "zapis ne postoji")
		return
	}
	ff, _ := scanForward(s.db.QueryRow(
		`SELECT `+fwdCols+` FROM fw_forwards WHERE uuid=?`, uuid))
	writeJSON(w, http.StatusOK, ff)
}

func (s *server) handleFWForwardDelete(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.Exec(`DELETE FROM fw_forwards WHERE uuid=?`, r.PathValue("uuid"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "zapis ne postoji")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": r.PathValue("uuid")})
}

/* ---------- CRUD: pravila ---------- */

type fwRuleIn struct {
	FWRule
	Enabled *bool `json:"enabled"`
}

func (s *server) handleFWRuleList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT ` + ruleCols + ` FROM fw_rules ORDER BY name`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	out := []FWRule{}
	for rows.Next() {
		f, err := scanRule(rows)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, f)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": out})
}

func (s *server) handleFWRuleCreate(w http.ResponseWriter, r *http.Request) {
	var in fwRuleIn
	if !decodeBody(w, r, &in) {
		return
	}
	f := &in.FWRule
	if !s.validateRule(w, f) {
		return
	}
	f.UUID = newUUID()
	_, err := s.db.Exec(`INSERT INTO fw_rules
		(uuid, name, family, proto, src_zone, src_ip, dest_zone, dest_ip,
		 dest_port, target, start_time, stop_time, weekdays, enabled, notes)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.UUID, f.Name, f.Family, f.Proto, f.SrcZone, f.SrcIP, f.DestZone,
		f.DestIP, f.DestPort, f.Target, f.StartTime, f.StopTime, f.Weekdays,
		enabledIntOf(in.Enabled), f.Notes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ff, _ := scanRule(s.db.QueryRow(
		`SELECT `+ruleCols+` FROM fw_rules WHERE uuid=?`, f.UUID))
	writeJSON(w, http.StatusCreated, ff)
}

func (s *server) handleFWRuleUpdate(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	var in fwRuleIn
	if !decodeBody(w, r, &in) {
		return
	}
	f := &in.FWRule
	if !s.validateRule(w, f) {
		return
	}
	res, err := s.db.Exec(`UPDATE fw_rules SET name=?, family=?, proto=?,
		src_zone=?, src_ip=?, dest_zone=?, dest_ip=?, dest_port=?, target=?,
		start_time=?, stop_time=?, weekdays=?, enabled=?, notes=?,
		updated_at=datetime('now') WHERE uuid=?`,
		f.Name, f.Family, f.Proto, f.SrcZone, f.SrcIP, f.DestZone, f.DestIP,
		f.DestPort, f.Target, f.StartTime, f.StopTime, f.Weekdays,
		enabledIntOf(in.Enabled), f.Notes, uuid)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "zapis ne postoji")
		return
	}
	ff, _ := scanRule(s.db.QueryRow(
		`SELECT `+ruleCols+` FROM fw_rules WHERE uuid=?`, uuid))
	writeJSON(w, http.StatusOK, ff)
}

func (s *server) handleFWRuleDelete(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.Exec(`DELETE FROM fw_rules WHERE uuid=?`, r.PathValue("uuid"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "zapis ne postoji")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": r.PathValue("uuid")})
}

/* ---------- aliasi (imenovane grupe adresa) ---------- */

var reAliasName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,19}$`)

type FWAlias struct {
	UUID  string `json:"uuid"`
	Name  string `json:"name"`
	IPs   string `json:"ips"`
	Notes string `json:"notes"`
}

func (s *server) handleAliasList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT uuid, name, ips, COALESCE(notes,'')
		FROM fw_aliases ORDER BY name`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	out := []FWAlias{}
	for rows.Next() {
		var a FWAlias
		if rows.Scan(&a.UUID, &a.Name, &a.IPs, &a.Notes) == nil {
			out = append(out, a)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"aliases": out})
}

func validateAlias(w http.ResponseWriter, a *FWAlias) bool {
	a.Name = strings.ToLower(strings.TrimSpace(a.Name))
	a.IPs = strings.TrimSpace(a.IPs)
	if !reAliasName.MatchString(a.Name) {
		writeErr(w, http.StatusBadRequest,
			"naziv aliasa: 2-20 malih slova/znamenki/crtica, počinje slovom")
		return false
	}
	ips := strings.Fields(a.IPs)
	if len(ips) == 0 {
		writeErr(w, http.StatusBadRequest, "alias treba bar jednu adresu")
		return false
	}
	for _, ip := range ips {
		if !validAddr(ip) {
			writeErr(w, http.StatusBadRequest, "neispravna adresa: "+ip)
			return false
		}
	}
	return true
}

func (s *server) handleAliasCreate(w http.ResponseWriter, r *http.Request) {
	var a FWAlias
	if !decodeBody(w, r, &a) {
		return
	}
	if !validateAlias(w, &a) {
		return
	}
	a.UUID = newUUID()
	if _, err := s.db.Exec(`INSERT INTO fw_aliases (uuid, name, ips, notes)
		VALUES (?,?,?,?)`, a.UUID, a.Name, a.IPs, a.Notes); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "alias s tim nazivom već postoji")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *server) handleAliasUpdate(w http.ResponseWriter, r *http.Request) {
	var a FWAlias
	if !decodeBody(w, r, &a) {
		return
	}
	if !validateAlias(w, &a) {
		return
	}
	res, err := s.db.Exec(`UPDATE fw_aliases SET name=?, ips=?, notes=?,
		updated_at=datetime('now') WHERE uuid=?`,
		a.Name, a.IPs, a.Notes, r.PathValue("uuid"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "alias ne postoji")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *server) handleAliasDelete(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.Exec(`DELETE FROM fw_aliases WHERE uuid=?`, r.PathValue("uuid"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "alias ne postoji")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": r.PathValue("uuid")})
}

// aliasSingleIP: alias postoji i sadrži točno jednu IP adresu (za forwarde).
func (s *server) aliasSingleIP(val string) bool {
	addrs, err := s.resolveAlias(val)
	return err == nil && len(addrs) == 1 && net.ParseIP(addrs[0]) != nil
}

// resolveAlias vraća adrese aliasa za "@naziv" (ili samu vrijednost ako nije alias).
func (s *server) resolveAlias(val string) ([]string, error) {
	if !strings.HasPrefix(val, "@") {
		return []string{val}, nil
	}
	var ips string
	err := s.db.QueryRow(`SELECT ips FROM fw_aliases WHERE name=?`,
		strings.TrimPrefix(val, "@")).Scan(&ips)
	if err != nil {
		return nil, fmt.Errorf("alias %s ne postoji", val)
	}
	return strings.Fields(ips), nil
}

/* ---------- DMZ ---------- */

// DMZ: sav dolazni promet s WAN-a (koji nije uhvaćen drugim forwardima)
// preusmjeri na jedan interni host. Singleton sekcija sag_dmz.
func (s *server) handleDMZGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := uciGetConfig(r.Context(), "firewall")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	out := map[string]any{"enabled": false, "dest_ip": ""}
	if sec, ok := cfg["sag_dmz"]; ok {
		out["enabled"] = true
		out["dest_ip"] = sectStr(sec, "dest_ip")
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleDMZSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Enabled *bool  `json:"enabled"`
		DestIP  string `json:"dest_ip"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "nedostaje polje enabled")
		return
	}
	in.DestIP = strings.TrimSpace(in.DestIP)
	if *in.Enabled && net.ParseIP(in.DestIP) == nil {
		writeErr(w, http.StatusBadRequest, "neispravna DMZ IP adresa")
		return
	}
	backupName, err := s.backupConfig(firewallConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}
	cfg, err := uciGetConfig(ctx, "firewall")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var b strings.Builder
	if *in.Enabled {
		b.WriteString("set firewall.sag_dmz=redirect\n")
		b.WriteString("set firewall.sag_dmz.target=DNAT\n")
		b.WriteString("set firewall.sag_dmz.name=Saguaro-DMZ\n")
		b.WriteString("set firewall.sag_dmz.src=wan\n")
		b.WriteString("set firewall.sag_dmz.dest=lan\n")
		fmt.Fprintf(&b, "set firewall.sag_dmz.dest_ip=%s\n", in.DestIP)
		b.WriteString("set firewall.sag_dmz.proto='tcp udp'\n")
	} else {
		if _, ok := cfg["sag_dmz"]; !ok {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
			return
		}
		b.WriteString("delete firewall.sag_dmz\n")
	}
	b.WriteString("commit firewall\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := serviceReload(ctx, "firewall", "reload"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": *in.Enabled, "dest_ip": in.DestIP, "backup": backupName,
	})
}

/* ---------- 1:1 NAT ---------- */

const n1dPrefix = "sag_n1d_" // DNAT (redirect) polovica para
const n1sPrefix = "sag_n1s_" // SNAT (nat) polovica para

type NAT11 struct {
	UUID       string `json:"uuid"`
	Name       string `json:"name"`
	PublicIP   string `json:"public_ip"`
	InternalIP string `json:"internal_ip"`
	Zone       string `json:"zone"`
	Enabled    bool   `json:"enabled"`
	Notes      string `json:"notes"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

const nat11Cols = `uuid, name, public_ip, internal_ip, zone, enabled,
	COALESCE(notes,''), created_at, updated_at`

func scanNAT11(row interface{ Scan(...any) error }) (NAT11, error) {
	var n NAT11
	err := row.Scan(&n.UUID, &n.Name, &n.PublicIP, &n.InternalIP, &n.Zone,
		&n.Enabled, &n.Notes, &n.CreatedAt, &n.UpdatedAt)
	return n, err
}

func validateNAT11(w http.ResponseWriter, n *NAT11) bool {
	n.Name = strings.TrimSpace(n.Name)
	n.PublicIP = strings.TrimSpace(n.PublicIP)
	n.InternalIP = strings.TrimSpace(n.InternalIP)
	n.Zone = strings.TrimSpace(n.Zone)
	if n.Zone == "" {
		n.Zone = "wan"
	}
	switch {
	case n.Name == "":
		writeErr(w, http.StatusBadRequest, "naziv je obavezan")
	case net.ParseIP(n.PublicIP) == nil:
		writeErr(w, http.StatusBadRequest, "neispravna javna IP adresa")
	case net.ParseIP(n.InternalIP) == nil:
		writeErr(w, http.StatusBadRequest, "neispravna interna IP adresa")
	case !reZone.MatchString(n.Zone):
		writeErr(w, http.StatusBadRequest, "neispravno ime zone")
	default:
		return true
	}
	return false
}

type nat11In struct {
	NAT11
	Enabled *bool `json:"enabled"`
}

func (s *server) handleNAT11List(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT ` + nat11Cols + ` FROM fw_nat11 ORDER BY public_ip`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	out := []NAT11{}
	for rows.Next() {
		n, err := scanNAT11(rows)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, n)
	}
	writeJSON(w, http.StatusOK, map[string]any{"nat11": out})
}

func (s *server) handleNAT11Create(w http.ResponseWriter, r *http.Request) {
	var in nat11In
	if !decodeBody(w, r, &in) {
		return
	}
	n := &in.NAT11
	if !validateNAT11(w, n) {
		return
	}
	n.UUID = newUUID()
	_, err := s.db.Exec(`INSERT INTO fw_nat11
		(uuid, name, public_ip, internal_ip, zone, enabled, notes)
		VALUES (?,?,?,?,?,?,?)`,
		n.UUID, n.Name, n.PublicIP, n.InternalIP, n.Zone,
		enabledIntOf(in.Enabled), n.Notes)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "1:1 NAT za tu javnu adresu već postoji")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	nn, _ := scanNAT11(s.db.QueryRow(`SELECT `+nat11Cols+` FROM fw_nat11 WHERE uuid=?`, n.UUID))
	writeJSON(w, http.StatusCreated, nn)
}

func (s *server) handleNAT11Update(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	var in nat11In
	if !decodeBody(w, r, &in) {
		return
	}
	n := &in.NAT11
	if !validateNAT11(w, n) {
		return
	}
	res, err := s.db.Exec(`UPDATE fw_nat11 SET name=?, public_ip=?, internal_ip=?,
		zone=?, enabled=?, notes=?, updated_at=datetime('now') WHERE uuid=?`,
		n.Name, n.PublicIP, n.InternalIP, n.Zone, enabledIntOf(in.Enabled),
		n.Notes, uuid)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "1:1 NAT za tu javnu adresu već postoji")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if nRows, _ := res.RowsAffected(); nRows == 0 {
		writeErr(w, http.StatusNotFound, "zapis ne postoji")
		return
	}
	nn, _ := scanNAT11(s.db.QueryRow(`SELECT `+nat11Cols+` FROM fw_nat11 WHERE uuid=?`, uuid))
	writeJSON(w, http.StatusOK, nn)
}

func (s *server) handleNAT11Delete(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.Exec(`DELETE FROM fw_nat11 WHERE uuid=?`, r.PathValue("uuid"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "zapis ne postoji")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": r.PathValue("uuid")})
}

/* ---------- primjena ---------- */

func (s *server) handleFWApply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	fRows, err := s.db.Query(`SELECT ` + fwdCols + ` FROM fw_forwards
		WHERE enabled = 1 ORDER BY src_dport`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	forwards := []FWForward{}
	for fRows.Next() {
		f, err := scanForward(fRows)
		if err != nil {
			fRows.Close()
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		forwards = append(forwards, f)
	}
	fRows.Close()

	rRows, err := s.db.Query(`SELECT ` + ruleCols + ` FROM fw_rules
		WHERE enabled = 1 ORDER BY name`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	rules := []FWRule{}
	for rRows.Next() {
		f, err := scanRule(rRows)
		if err != nil {
			rRows.Close()
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		rules = append(rules, f)
	}
	rRows.Close()

	nRows, err := s.db.Query(`SELECT ` + nat11Cols + ` FROM fw_nat11
		WHERE enabled = 1 ORDER BY public_ip`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	nats := []NAT11{}
	for nRows.Next() {
		n, err := scanNAT11(nRows)
		if err != nil {
			nRows.Close()
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		nats = append(nats, n)
	}
	nRows.Close()

	backupName, err := s.backupConfig(firewallConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}

	cfg, err := uciGetConfig(ctx, "firewall")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	// interne zone za NAT reflection (sve osim izvorišnih/WAN zona)
	internalZones := []string{}
	for _, sec := range cfg {
		if sectStr(sec, ".type") != "zone" {
			continue
		}
		zn := sectStr(sec, "name")
		if zn == "" || zn == "wan" {
			continue
		}
		internalZones = append(internalZones, zn)
	}
	sort.Strings(internalZones)

	var b strings.Builder
	removed := 0
	for name, sec := range cfg {
		t := sectStr(sec, ".type")
		if (strings.HasPrefix(name, pfPrefix) && t == "redirect") ||
			(strings.HasPrefix(name, rlPrefix) && t == "rule") ||
			(strings.HasPrefix(name, n1dPrefix) && t == "redirect") ||
			(strings.HasPrefix(name, n1sPrefix) && t == "nat") {
			fmt.Fprintf(&b, "delete firewall.%s\n", name)
			removed++
		}
	}
	for _, f := range forwards {
		sn := pfPrefix + strings.ReplaceAll(f.UUID, "-", "")[:8]
		fmt.Fprintf(&b, "set firewall.%s=redirect\n", sn)
		fmt.Fprintf(&b, "set firewall.%s.target=DNAT\n", sn)
		fmt.Fprintf(&b, "set firewall.%s.name=%s\n", sn, uciQuote(f.Name))
		fmt.Fprintf(&b, "set firewall.%s.proto=%s\n", sn, uciQuote(f.Proto))
		fmt.Fprintf(&b, "set firewall.%s.src=%s\n", sn, f.SrcZone)
		fmt.Fprintf(&b, "set firewall.%s.src_dport=%s\n", sn, f.SrcDport)
		fmt.Fprintf(&b, "set firewall.%s.dest=%s\n", sn, f.DestZone)
		destIP := f.DestIP
		if strings.HasPrefix(destIP, "@") {
			addrs, err := s.resolveAlias(destIP)
			if err != nil || len(addrs) != 1 {
				writeErr(w, http.StatusConflict,
					"forward "+f.Name+": alias mora imati točno jednu adresu")
				return
			}
			destIP = addrs[0]
		}
		fmt.Fprintf(&b, "set firewall.%s.dest_ip=%s\n", sn, destIP)
		if f.DestPort != "" {
			fmt.Fprintf(&b, "set firewall.%s.dest_port=%s\n", sn, f.DestPort)
		}
		if f.SrcDIP != "" {
			fmt.Fprintf(&b, "set firewall.%s.src_dip=%s\n", sn, f.SrcDIP)
		}
		// hairpin NAT / NAT reflection: forward dostupan i iz unutarnjih mreža.
		// fw4 zadano radi reflection samo za odredišnu zonu, pa izrijekom
		// navodimo sve interne zone (VLAN-ovi, VPN) — inače korisnici izvan
		// LAN-a ne mogu do servera preko javne adrese.
		refl := "0"
		if f.Reflection {
			refl = "1"
		}
		fmt.Fprintf(&b, "set firewall.%s.reflection=%s\n", sn, refl)
		if f.Reflection {
			for _, z := range internalZones {
				fmt.Fprintf(&b, "add_list firewall.%s.reflection_zone=%s\n", sn, z)
			}
		}
	}
	for _, f := range rules {
		sn := rlPrefix + strings.ReplaceAll(f.UUID, "-", "")[:8]
		fmt.Fprintf(&b, "set firewall.%s=rule\n", sn)
		fmt.Fprintf(&b, "set firewall.%s.name=%s\n", sn, uciQuote(f.Name))
		if f.Family != "any" {
			fmt.Fprintf(&b, "set firewall.%s.family=%s\n", sn, f.Family)
		}
		if f.Proto != "all" {
			fmt.Fprintf(&b, "set firewall.%s.proto=%s\n", sn, uciQuote(f.Proto))
		}
		fmt.Fprintf(&b, "set firewall.%s.src=%s\n", sn, f.SrcZone)
		if f.SrcIP != "" {
			addrs, err := s.resolveAlias(f.SrcIP)
			if err != nil {
				writeErr(w, http.StatusConflict, "pravilo "+f.Name+": "+err.Error())
				return
			}
			for _, a := range addrs {
				fmt.Fprintf(&b, "add_list firewall.%s.src_ip=%s\n", sn, a)
			}
		}
		if f.DestZone != "" {
			fmt.Fprintf(&b, "set firewall.%s.dest=%s\n", sn, f.DestZone)
		}
		if f.DestIP != "" {
			addrs, err := s.resolveAlias(f.DestIP)
			if err != nil {
				writeErr(w, http.StatusConflict, "pravilo "+f.Name+": "+err.Error())
				return
			}
			for _, a := range addrs {
				fmt.Fprintf(&b, "add_list firewall.%s.dest_ip=%s\n", sn, a)
			}
		}
		if f.DestPort != "" {
			fmt.Fprintf(&b, "set firewall.%s.dest_port=%s\n", sn, f.DestPort)
		}
		if f.StartTime != "" && f.StopTime != "" {
			fmt.Fprintf(&b, "set firewall.%s.start_time=%s\n", sn, f.StartTime)
			fmt.Fprintf(&b, "set firewall.%s.stop_time=%s\n", sn, f.StopTime)
		}
		if f.Weekdays != "" {
			fmt.Fprintf(&b, "set firewall.%s.weekdays=%s\n", sn, uciQuote(f.Weekdays))
		}
		fmt.Fprintf(&b, "set firewall.%s.target=%s\n", sn, f.Target)
	}
	for _, n := range nats {
		id := strings.ReplaceAll(n.UUID, "-", "")[:8]
		// DNAT: sav promet na javnu adresu -> interni host
		fmt.Fprintf(&b, "set firewall.%s%s=redirect\n", n1dPrefix, id)
		fmt.Fprintf(&b, "set firewall.%s%s.target=DNAT\n", n1dPrefix, id)
		fmt.Fprintf(&b, "set firewall.%s%s.name=%s\n", n1dPrefix, id, uciQuote(n.Name+" DNAT"))
		fmt.Fprintf(&b, "set firewall.%s%s.src=%s\n", n1dPrefix, id, n.Zone)
		fmt.Fprintf(&b, "set firewall.%s%s.src_dip=%s\n", n1dPrefix, id, n.PublicIP)
		fmt.Fprintf(&b, "set firewall.%s%s.dest_ip=%s\n", n1dPrefix, id, n.InternalIP)
		fmt.Fprintf(&b, "set firewall.%s%s.proto=all\n", n1dPrefix, id)
		// SNAT: odlazni promet internog hosta izlazi s javne adrese
		fmt.Fprintf(&b, "set firewall.%s%s=nat\n", n1sPrefix, id)
		fmt.Fprintf(&b, "set firewall.%s%s.target=SNAT\n", n1sPrefix, id)
		fmt.Fprintf(&b, "set firewall.%s%s.name=%s\n", n1sPrefix, id, uciQuote(n.Name+" SNAT"))
		fmt.Fprintf(&b, "set firewall.%s%s.src=%s\n", n1sPrefix, id, n.Zone)
		fmt.Fprintf(&b, "set firewall.%s%s.src_ip=%s\n", n1sPrefix, id, n.InternalIP)
		fmt.Fprintf(&b, "set firewall.%s%s.snat_ip=%s\n", n1sPrefix, id, n.PublicIP)
		fmt.Fprintf(&b, "set firewall.%s%s.proto=all\n", n1sPrefix, id)
	}
	b.WriteString("commit firewall\n")

	if err := uciBatch(ctx, b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := serviceReload(ctx, "firewall", "reload"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"applied_forwards": len(forwards),
		"applied_rules":    len(rules),
		"applied_nat11":    len(nats),
		"removed":          removed,
		"backup":           backupName,
	})
}

// uciQuote štiti vrijednost s razmakom za uci batch liniju (jednostruki navodnici).
func uciQuote(s string) string {
	if !strings.ContainsAny(s, " \t") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "") + "'"
}
