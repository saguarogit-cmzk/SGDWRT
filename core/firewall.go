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
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	Proto     string `json:"proto"`
	SrcZone   string `json:"src_zone"`
	SrcDport  string `json:"src_dport"`
	DestZone  string `json:"dest_zone"`
	DestIP    string `json:"dest_ip"`
	DestPort  string `json:"dest_port"`
	Enabled   bool   `json:"enabled"`
	Notes     string `json:"notes"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

const fwdCols = `uuid, name, proto, src_zone, src_dport, dest_zone, dest_ip,
	COALESCE(dest_port,''), enabled, COALESCE(notes,''), created_at, updated_at`

func scanForward(row interface{ Scan(...any) error }) (FWForward, error) {
	var f FWForward
	err := row.Scan(&f.UUID, &f.Name, &f.Proto, &f.SrcZone, &f.SrcDport,
		&f.DestZone, &f.DestIP, &f.DestPort, &f.Enabled, &f.Notes,
		&f.CreatedAt, &f.UpdatedAt)
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
	Enabled   bool   `json:"enabled"`
	Notes     string `json:"notes"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

const ruleCols = `uuid, name, family, proto, src_zone, COALESCE(src_ip,''),
	COALESCE(dest_zone,''), COALESCE(dest_ip,''), COALESCE(dest_port,''),
	target, enabled, COALESCE(notes,''), created_at, updated_at`

func scanRule(row interface{ Scan(...any) error }) (FWRule, error) {
	var f FWRule
	err := row.Scan(&f.UUID, &f.Name, &f.Family, &f.Proto, &f.SrcZone, &f.SrcIP,
		&f.DestZone, &f.DestIP, &f.DestPort, &f.Target, &f.Enabled, &f.Notes,
		&f.CreatedAt, &f.UpdatedAt)
	return f, err
}

func validateForward(w http.ResponseWriter, f *FWForward) bool {
	f.Name = strings.TrimSpace(f.Name)
	f.Proto = strings.TrimSpace(f.Proto)
	f.SrcZone = strings.TrimSpace(f.SrcZone)
	f.SrcDport = strings.TrimSpace(f.SrcDport)
	f.DestZone = strings.TrimSpace(f.DestZone)
	f.DestIP = strings.TrimSpace(f.DestIP)
	f.DestPort = strings.TrimSpace(f.DestPort)
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
	case net.ParseIP(f.DestIP) == nil:
		writeErr(w, http.StatusBadRequest, "neispravna odredišna IP adresa")
	case f.DestPort != "" && !validPortSpec(f.DestPort):
		writeErr(w, http.StatusBadRequest, "neispravan odredišni port")
	default:
		return true
	}
	return false
}

func validateRule(w http.ResponseWriter, f *FWRule) bool {
	f.Name = strings.TrimSpace(f.Name)
	f.Family = strings.TrimSpace(f.Family)
	f.Proto = strings.TrimSpace(f.Proto)
	f.SrcZone = strings.TrimSpace(f.SrcZone)
	f.SrcIP = strings.TrimSpace(f.SrcIP)
	f.DestZone = strings.TrimSpace(f.DestZone)
	f.DestIP = strings.TrimSpace(f.DestIP)
	f.DestPort = strings.TrimSpace(f.DestPort)
	f.Target = strings.ToUpper(strings.TrimSpace(f.Target))
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
	case f.SrcIP != "" && !validAddr(f.SrcIP):
		writeErr(w, http.StatusBadRequest, "neispravna izvorišna adresa (IP ili CIDR)")
	case f.DestZone != "" && f.DestZone != "*" && !reZone.MatchString(f.DestZone):
		writeErr(w, http.StatusBadRequest, "neispravna odredišna zona")
	case f.DestIP != "" && !validAddr(f.DestIP):
		writeErr(w, http.StatusBadRequest, "neispravna odredišna adresa (IP ili CIDR)")
	case f.DestPort != "" && !validPortSpec(f.DestPort):
		writeErr(w, http.StatusBadRequest, "neispravan odredišni port")
	case f.Target != "ACCEPT" && f.Target != "REJECT" && f.Target != "DROP":
		writeErr(w, http.StatusBadRequest, "akcija mora biti ACCEPT, REJECT ili DROP")
	default:
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
			redirects = append(redirects, sect{name, sectStr(sec, "name"),
				strings.HasPrefix(name, pfPrefix)})
		case "rule":
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
	Enabled *bool `json:"enabled"`
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
	if !validateForward(w, f) {
		return
	}
	f.UUID = newUUID()
	_, err := s.db.Exec(`INSERT INTO fw_forwards
		(uuid, name, proto, src_zone, src_dport, dest_zone, dest_ip, dest_port,
		 enabled, notes) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		f.UUID, f.Name, f.Proto, f.SrcZone, f.SrcDport, f.DestZone, f.DestIP,
		f.DestPort, enabledIntOf(in.Enabled), f.Notes)
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
	if !validateForward(w, f) {
		return
	}
	res, err := s.db.Exec(`UPDATE fw_forwards SET name=?, proto=?, src_zone=?,
		src_dport=?, dest_zone=?, dest_ip=?, dest_port=?, enabled=?, notes=?,
		updated_at=datetime('now') WHERE uuid=?`,
		f.Name, f.Proto, f.SrcZone, f.SrcDport, f.DestZone, f.DestIP, f.DestPort,
		enabledIntOf(in.Enabled), f.Notes, uuid)
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
	if !validateRule(w, f) {
		return
	}
	f.UUID = newUUID()
	_, err := s.db.Exec(`INSERT INTO fw_rules
		(uuid, name, family, proto, src_zone, src_ip, dest_zone, dest_ip,
		 dest_port, target, enabled, notes) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.UUID, f.Name, f.Family, f.Proto, f.SrcZone, f.SrcIP, f.DestZone,
		f.DestIP, f.DestPort, f.Target, enabledIntOf(in.Enabled), f.Notes)
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
	if !validateRule(w, f) {
		return
	}
	res, err := s.db.Exec(`UPDATE fw_rules SET name=?, family=?, proto=?,
		src_zone=?, src_ip=?, dest_zone=?, dest_ip=?, dest_port=?, target=?,
		enabled=?, notes=?, updated_at=datetime('now') WHERE uuid=?`,
		f.Name, f.Family, f.Proto, f.SrcZone, f.SrcIP, f.DestZone, f.DestIP,
		f.DestPort, f.Target, enabledIntOf(in.Enabled), f.Notes, uuid)
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
	removed := 0
	for name, sec := range cfg {
		t := sectStr(sec, ".type")
		if (strings.HasPrefix(name, pfPrefix) && t == "redirect") ||
			(strings.HasPrefix(name, rlPrefix) && t == "rule") {
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
		fmt.Fprintf(&b, "set firewall.%s.dest_ip=%s\n", sn, f.DestIP)
		if f.DestPort != "" {
			fmt.Fprintf(&b, "set firewall.%s.dest_port=%s\n", sn, f.DestPort)
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
			fmt.Fprintf(&b, "set firewall.%s.src_ip=%s\n", sn, f.SrcIP)
		}
		if f.DestZone != "" {
			fmt.Fprintf(&b, "set firewall.%s.dest=%s\n", sn, f.DestZone)
		}
		if f.DestIP != "" {
			fmt.Fprintf(&b, "set firewall.%s.dest_ip=%s\n", sn, f.DestIP)
		}
		if f.DestPort != "" {
			fmt.Fprintf(&b, "set firewall.%s.dest_port=%s\n", sn, f.DestPort)
		}
		fmt.Fprintf(&b, "set firewall.%s.target=%s\n", sn, f.Target)
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
