package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Saguaro upravlja isključivo vlastitim uci sekcijama s ovim prefiksom (D-011);
// ručne i LuCI sekcije se nikad ne diraju.
const sagPrefix = "sag_"

const leaseFile = "/tmp/dhcp.leases"
const dhcpConfig = "/etc/config/dhcp"
const backupKeep = 20

/* ---------- čitanje uci konfiguracije preko ubus-a ---------- */

type uciSection map[string]any

func uciGetConfig(ctx context.Context, config string) (map[string]uciSection, error) {
	var resp struct {
		Values map[string]uciSection `json:"values"`
	}
	err := ubusCallArg(ctx, "uci", "get", `{"config":"`+config+`"}`, &resp)
	return resp.Values, err
}

func sectStr(s uciSection, key string) string {
	switch v := s[key].(type) {
	case string:
		return v
	case []any: // uci list (npr. više MAC-ova) — uzmi prvi za prikaz
		if len(v) > 0 {
			if str, ok := v[0].(string); ok {
				return str
			}
		}
	}
	return ""
}

/* ---------- status ---------- */

type staticLease struct {
	Section string `json:"section"`
	MAC     string `json:"mac"`
	IP      string `json:"ip"`
	Name    string `json:"name"`
	Managed bool   `json:"managed_by_saguaro"`
}

type activeLease struct {
	MAC       string `json:"mac"`
	IP        string `json:"ip"`
	Hostname  string `json:"hostname"`
	ExpiresAt int64  `json:"expires_at"`
}

func (s *server) handleDHCPStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := uciGetConfig(r.Context(), "dhcp")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	// svi DHCP poolovi (jedan po sučelju/podmreži), ne samo lan
	servers := []map[string]any{}
	for name, sec := range cfg {
		if sectStr(sec, ".type") != "dhcp" {
			continue
		}
		iface := sectStr(sec, "interface")
		if iface == "" {
			iface = name
		}
		servers = append(servers, map[string]any{
			"section":   name,
			"interface": iface,
			"start":     sectStr(sec, "start"),
			"limit":     sectStr(sec, "limit"),
			"leasetime": sectStr(sec, "leasetime"),
			"ignore":    sectStr(sec, "ignore") == "1",
		})
	}
	sort.Slice(servers, func(i, j int) bool {
		return servers[i]["section"].(string) < servers[j]["section"].(string)
	})

	leases := []staticLease{}
	for name, sec := range cfg {
		if sectStr(sec, ".type") != "host" {
			continue
		}
		leases = append(leases, staticLease{
			Section: name,
			MAC:     strings.ToLower(sectStr(sec, "mac")),
			IP:      sectStr(sec, "ip"),
			Name:    sectStr(sec, "name"),
			Managed: strings.HasPrefix(name, sagPrefix),
		})
	}
	sort.Slice(leases, func(i, j int) bool { return leases[i].IP < leases[j].IP })

	writeJSON(w, http.StatusOK, map[string]any{
		"servers":       servers,
		"static_leases": leases,
		"active_leases": parseLeases(leaseFile),
	})
}

// parseLeases čita dnsmasq lease datoteku (format: expiry mac ip hostname clientid).
func parseLeases(path string) []activeLease {
	out := []activeLease{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		exp, _ := strconv.ParseInt(f[0], 10, 64)
		host := f[3]
		if host == "*" {
			host = ""
		}
		out = append(out, activeLease{
			MAC: strings.ToLower(f[1]), IP: f[2], Hostname: host, ExpiresAt: exp,
		})
	}
	return out
}

/* ---------- primjena rezervacija (prvo uci pisanje) ---------- */

func (s *server) handleDHCPApply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rows, err := s.db.Query(`SELECT uuid, COALESCE(hostname,''), mac,
		COALESCE(ipv4,'') FROM hosts WHERE managed = 1 ORDER BY ipv4`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	type mh struct{ uuid, hostname, mac, ipv4 string }
	hosts := []mh{}
	skipped := []string{}
	for rows.Next() {
		var h mh
		if err := rows.Scan(&h.uuid, &h.hostname, &h.mac, &h.ipv4); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if h.ipv4 == "" {
			skipped = append(skipped, h.mac+" (nema IPv4)")
			continue
		}
		hosts = append(hosts, h)
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

	var batch strings.Builder
	removed := 0
	for name, sec := range cfg {
		if strings.HasPrefix(name, sagPrefix) && sectStr(sec, ".type") == "host" {
			fmt.Fprintf(&batch, "delete dhcp.%s\n", name)
			removed++
		}
	}
	for _, h := range hosts {
		sn := sagPrefix + strings.ReplaceAll(h.uuid, "-", "")[:8]
		fmt.Fprintf(&batch, "set dhcp.%s=host\n", sn)
		fmt.Fprintf(&batch, "set dhcp.%s.mac=%s\n", sn, h.mac)
		fmt.Fprintf(&batch, "set dhcp.%s.ip=%s\n", sn, h.ipv4)
		if n := sanitizeDNSName(h.hostname); n != "" {
			fmt.Fprintf(&batch, "set dhcp.%s.name=%s\n", sn, n)
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
		"applied": len(hosts),
		"removed": removed,
		"skipped": skipped,
		"backup":  backupName,
	})
}

// handleDHCPServerSet uključuje/isključuje DHCP pool danog sučelja
// (dhcp.<sekcija>.ignore). Izravna korisnička radnja nad jednom opcijom, uz backup.
func (s *server) handleDHCPServerSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Interface string `json:"interface"`
		Enabled   *bool  `json:"enabled"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "nedostaje polje enabled")
		return
	}
	if in.Interface == "" {
		in.Interface = "lan"
	}
	cfg, err := uciGetConfig(r.Context(), "dhcp")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	// sekcija po imenu (OpenWrt konvencija: sekcija = ime sučelja) ili po opciji interface
	section := ""
	if sec, ok := cfg[in.Interface]; ok && sectStr(sec, ".type") == "dhcp" {
		section = in.Interface
	} else {
		for name, sec := range cfg {
			if sectStr(sec, ".type") == "dhcp" && sectStr(sec, "interface") == in.Interface {
				section = name
				break
			}
		}
	}
	if section == "" {
		writeErr(w, http.StatusNotFound, "DHCP pool za sučelje "+in.Interface+" ne postoji")
		return
	}
	backupName, err := s.backupConfig(dhcpConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}
	var batch strings.Builder
	if *in.Enabled {
		if sectStr(cfg[section], "ignore") != "" {
			fmt.Fprintf(&batch, "delete dhcp.%s.ignore\n", section)
		}
	} else {
		fmt.Fprintf(&batch, "set dhcp.%s.ignore=1\n", section)
	}
	batch.WriteString("commit dhcp\n")
	if err := uciBatch(r.Context(), batch.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := serviceReload(r.Context(), "dnsmasq", "reload"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": *in.Enabled, "backup": backupName,
	})
}

// backupConfig kopira config u backup direktorij i čuva zadnjih backupKeep kopija.
func (s *server) backupConfig(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	base := filepath.Base(path)
	name := base + "-" + time.Now().Format("20060102-150405")
	if err := os.WriteFile(filepath.Join(s.backupDir, name), b, 0o600); err != nil {
		return "", err
	}
	// rotacija starih kopija istog configa
	entries, err := os.ReadDir(s.backupDir)
	if err == nil {
		var old []string
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), base+"-") {
				old = append(old, e.Name())
			}
		}
		sort.Strings(old)
		for len(old) > backupKeep {
			os.Remove(filepath.Join(s.backupDir, old[0]))
			old = old[1:]
		}
	}
	return name, nil
}

// sanitizeDNSName zadrži samo znakove valjane za dnsmasq hostname.
func sanitizeDNSName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == ' ' || r == '_':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 63 {
		out = out[:63]
	}
	return out
}
