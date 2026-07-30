package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
)

/* ---------- model ---------- */

type DNSRecord struct {
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Value     string `json:"value"`
	Notes     string `json:"notes"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

const dnsCols = `uuid, name, rtype, value, COALESCE(notes,''), enabled,
	created_at, updated_at`

func scanDNSRecord(row interface{ Scan(...any) error }) (DNSRecord, error) {
	var d DNSRecord
	err := row.Scan(&d.UUID, &d.Name, &d.Type, &d.Value, &d.Notes, &d.Enabled,
		&d.CreatedAt, &d.UpdatedAt)
	return d, err
}

// validDNSName provjerava hostname/FQDN po pravilima DNS labela
// (mala slova, znamenke, '-', labele odvojene točkom).
func validDNSName(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 ||
			label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// validateDNSRecord normalizira polja i vraća poruku greške ("" = u redu).
func validateDNSRecord(d *DNSRecord) string {
	d.Name = strings.ToLower(strings.TrimSpace(d.Name))
	d.Type = strings.ToUpper(strings.TrimSpace(d.Type))
	d.Value = strings.ToLower(strings.TrimSpace(d.Value))
	if d.Type == "" {
		d.Type = "A"
	}
	if !validDNSName(d.Name) {
		return "neispravan naziv (dozvoljena mala slova, znamenke, '-' i '.')"
	}
	switch d.Type {
	case "A":
		ip := net.ParseIP(d.Value)
		if ip == nil || ip.To4() == nil {
			return "za A zapis vrijednost mora biti IPv4 adresa"
		}
		d.Value = ip.To4().String()
	case "CNAME":
		if !validDNSName(d.Value) {
			return "za CNAME zapis vrijednost mora biti valjano ime"
		}
		if d.Value == d.Name {
			return "CNAME ne može pokazivati sam na sebe"
		}
	default:
		return "tip mora biti A ili CNAME"
	}
	return ""
}

/* ---------- CRUD ---------- */

func (s *server) handleDNSRecordList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT ` + dnsCols + ` FROM dns_records ORDER BY name, rtype`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	out := []DNSRecord{}
	for rows.Next() {
		d, err := scanDNSRecord(rows)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": out})
}

// dnsRecordIn razlikuje izostavljen "enabled" (nil → zadano uključeno) od false.
type dnsRecordIn struct {
	DNSRecord
	Enabled *bool `json:"enabled"`
}

func (in *dnsRecordIn) enabledInt() int {
	if in.Enabled == nil || *in.Enabled {
		return 1
	}
	return 0
}

func (s *server) handleDNSRecordCreate(w http.ResponseWriter, r *http.Request) {
	var in dnsRecordIn
	if !decodeBody(w, r, &in) {
		return
	}
	d := &in.DNSRecord
	if msg := validateDNSRecord(d); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	d.UUID = newUUID()
	enabled := in.enabledInt()
	_, err := s.db.Exec(`INSERT INTO dns_records (uuid, name, rtype, value, notes, enabled)
		VALUES (?,?,?,?,?,?)`, d.UUID, d.Name, d.Type, d.Value, d.Notes, enabled)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "zapis s tim nazivom i tipom već postoji")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dd, _ := scanDNSRecord(s.db.QueryRow(
		`SELECT `+dnsCols+` FROM dns_records WHERE uuid=?`, d.UUID))
	writeJSON(w, http.StatusCreated, dd)
}

func (s *server) handleDNSRecordUpdate(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	var in dnsRecordIn
	if !decodeBody(w, r, &in) {
		return
	}
	d := &in.DNSRecord
	// validacija je međuovisna (tip određuje oblik vrijednosti) pa PUT nosi cijeli zapis
	if msg := validateDNSRecord(d); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	enabled := in.enabledInt()
	res, err := s.db.Exec(`UPDATE dns_records SET name=?, rtype=?, value=?, notes=?,
		enabled=?, updated_at=datetime('now') WHERE uuid=?`,
		d.Name, d.Type, d.Value, d.Notes, enabled, uuid)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "zapis s tim nazivom i tipom već postoji")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "zapis ne postoji")
		return
	}
	dd, _ := scanDNSRecord(s.db.QueryRow(
		`SELECT `+dnsCols+` FROM dns_records WHERE uuid=?`, uuid))
	writeJSON(w, http.StatusOK, dd)
}

func (s *server) handleDNSRecordDelete(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.Exec(`DELETE FROM dns_records WHERE uuid=?`, r.PathValue("uuid"))
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

/* ---------- status ---------- */

type dnsEntry struct {
	Section string `json:"section"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Value   string `json:"value"`
	Managed bool   `json:"managed_by_saguaro"`
}

func (s *server) handleDNSStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := uciGetConfig(r.Context(), "dhcp")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	settings := map[string]any{}
	entries := []dnsEntry{}
	for name, sec := range cfg {
		switch sectStr(sec, ".type") {
		case "dnsmasq":
			// DNSSEC provjera radi samo s dnsmasq-full (trust anchori uz paket)
			_, anchors := os.Stat("/usr/share/dnsmasq/trust-anchors.conf")
			settings = map[string]any{
				"domain":            sectStr(sec, "domain"),
				"local":             sectStr(sec, "local"),
				"rebind_protection": sectStr(sec, "rebind_protection") != "0",
				"dnssec":            sectStr(sec, "dnssec") == "1",
				"dnssec_supported":  anchors == nil,
			}
		case "domain":
			entries = append(entries, dnsEntry{
				Section: name, Type: "A",
				Name:    sectStr(sec, "name"),
				Value:   sectStr(sec, "ip"),
				Managed: strings.HasPrefix(name, sagPrefix),
			})
		case "cname":
			entries = append(entries, dnsEntry{
				Section: name, Type: "CNAME",
				Name:    sectStr(sec, "cname"),
				Value:   sectStr(sec, "target"),
				Managed: strings.HasPrefix(name, sagPrefix),
			})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	writeJSON(w, http.StatusOK, map[string]any{
		"dnsmasq": settings,
		"entries": entries,
	})
}

// handleDNSSECSet uključuje/isključuje DNSSEC provjeru potpisa u dnsmasq-u.
func (s *server) handleDNSSECSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		DNSSEC *bool `json:"dnssec"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.DNSSEC == nil {
		writeErr(w, http.StatusBadRequest, "nedostaje polje dnssec")
		return
	}
	if *in.DNSSEC {
		if _, err := os.Stat("/usr/share/dnsmasq/trust-anchors.conf"); err != nil {
			writeErr(w, http.StatusConflict,
				"DNSSEC traži paket dnsmasq-full (trust anchori nedostaju)")
			return
		}
	}
	cfg, err := uciGetConfig(ctx, "dhcp")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	section := ""
	for name, sec := range cfg {
		if sectStr(sec, ".type") == "dnsmasq" {
			section = name
			break
		}
	}
	if section == "" {
		writeErr(w, http.StatusNotFound, "dnsmasq sekcija ne postoji")
		return
	}
	backupName, err := s.backupConfig(dhcpConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}
	var b strings.Builder
	sec := cfg[section]
	if *in.DNSSEC {
		fmt.Fprintf(&b, "set dhcp.%s.dnssec=1\n", section)
		// DNSSEC traži upstream koji prosljeđuje DNSSEC zapise; kućni/ISP
		// routeri to često ne rade pa bi SVE domene padale. Ako korisnik
		// nema vlastite upstreame, postavljamo javne (i pamtimo da smo mi).
		if len(sectList(sec, "server")) == 0 {
			fmt.Fprintf(&b, "add_list dhcp.%s.server=1.1.1.1\n", section)
			fmt.Fprintf(&b, "add_list dhcp.%s.server=8.8.8.8\n", section)
			fmt.Fprintf(&b, "set dhcp.%s.noresolv=1\n", section)
			s.setSetting("dnssec_servers_added", "1")
		}
	} else {
		if sectStr(sec, "dnssec") != "" {
			fmt.Fprintf(&b, "delete dhcp.%s.dnssec\n", section)
		}
		if s.getSetting("dnssec_servers_added", "0") == "1" {
			fmt.Fprintf(&b, "del_list dhcp.%s.server=1.1.1.1\n", section)
			fmt.Fprintf(&b, "del_list dhcp.%s.server=8.8.8.8\n", section)
			if sectStr(sec, "noresolv") != "" {
				fmt.Fprintf(&b, "delete dhcp.%s.noresolv\n", section)
			}
			s.setSetting("dnssec_servers_added", "0")
		}
	}
	if sectStr(sec, "dnsseccheckunsigned") != "" {
		fmt.Fprintf(&b, "delete dhcp.%s.dnsseccheckunsigned\n", section)
	}
	b.WriteString("commit dhcp\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// promjena DNSSEC-a traži restart, reload nije dovoljan
	if err := serviceReload(ctx, "dnsmasq", "restart"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dnssec": *in.DNSSEC, "backup": backupName,
	})
}

/* ---------- primjena ---------- */

func (s *server) handleDNSApply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rows, err := s.db.Query(`SELECT uuid, name, rtype, value FROM dns_records
		WHERE enabled = 1 ORDER BY name`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	type rec struct{ uuid, name, rtype, value string }
	recs := []rec{}
	for rows.Next() {
		var x rec
		if err := rows.Scan(&x.uuid, &x.name, &x.rtype, &x.value); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		recs = append(recs, x)
	}

	// backup prije svake izmjene (D-011)
	backupName, err := s.backupConfig(dhcpConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}

	cfg, err := uciGetConfig(ctx, "dhcp")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	// lokalna domena — dnsmasq CNAME je exact-match (bez expand-hosts),
	// pa se imena bez točke proširuju domenom pri upisu
	domain := "lan"
	for _, sec := range cfg {
		if sectStr(sec, ".type") == "dnsmasq" {
			if d := sectStr(sec, "domain"); d != "" {
				domain = d
			}
		}
	}
	fqdn := func(name string) string {
		if strings.Contains(name, ".") {
			return name
		}
		return name + "." + domain
	}

	var batch strings.Builder
	removed := 0
	for name, sec := range cfg {
		t := sectStr(sec, ".type")
		if strings.HasPrefix(name, sagPrefix) && (t == "domain" || t == "cname") {
			fmt.Fprintf(&batch, "delete dhcp.%s\n", name)
			removed++
		}
	}
	for _, x := range recs {
		sn := sagPrefix + strings.ReplaceAll(x.uuid, "-", "")[:8]
		if x.rtype == "CNAME" {
			fmt.Fprintf(&batch, "set dhcp.%s=cname\n", sn)
			fmt.Fprintf(&batch, "set dhcp.%s.cname=%s\n", sn, fqdn(x.name))
			fmt.Fprintf(&batch, "set dhcp.%s.target=%s\n", sn, fqdn(x.value))
		} else {
			fmt.Fprintf(&batch, "set dhcp.%s=domain\n", sn)
			fmt.Fprintf(&batch, "set dhcp.%s.name=%s\n", sn, x.name)
			fmt.Fprintf(&batch, "set dhcp.%s.ip=%s\n", sn, x.value)
		}
	}
	batch.WriteString("commit dhcp\n")

	if err := uciBatch(ctx, batch.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := serviceReload(ctx, "dnsmasq", "reload"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"applied": len(recs),
		"removed": removed,
		"backup":  backupName,
	})
}
