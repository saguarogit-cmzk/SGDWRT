package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Vanjski DNS — kome uređaj šalje upite koje ne zna sam odgovoriti.
//
// Ovdje je i **filtriranje sadržaja za odrasle**: najjeftiniji način na
// routeru je prepustiti posao resolveru koji takve domene ne odgovara. Uređaj
// ne troši ni memoriju ni procesor na liste, a filtar se održava sam.
//
// Adrese su provjerene kod izvora 05.08.2026. Lako se pogriješi: AdGuardov
// **94.140.15.15 je obični** poslužitelj bez filtra za odrasle, a ne obiteljski
// (obiteljski par je 94.140.14.15 + 94.140.15.16). Tko ih pomiješa, dobije
// filtar koji radi samo dok prvi poslužitelj odgovara.
type dnsPreset struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Servers []string `json:"servers"`
	Family  bool     `json:"family"` // blokira li sadržaj za odrasle
	Note    string   `json:"note"`
}

var dnsPresets = []dnsPreset{
	{"isp", "Od operatera (automatski)", nil, false,
		"Uređaj koristi DNS koji dobije od operatera. Bez filtriranja."},
	{"cloudflare", "Cloudflare", []string{"1.1.1.1", "1.0.0.1"}, false,
		"Brz i bez filtriranja sadržaja."},
	{"cloudflare_family", "Cloudflare Families — bez sadržaja za odrasle",
		[]string{"1.1.1.3", "1.0.0.3"}, true,
		"Blokira zloćudne stranice i sadržaj za odrasle."},
	{"adguard_family", "AdGuard Family — bez sadržaja za odrasle",
		[]string{"94.140.14.15", "94.140.15.16"}, true,
		"Blokira oglase, praćenje i sadržaj za odrasle. Pazi: 94.140.15.15 je " +
			"obični AdGuard poslužitelj bez tog filtra."},
	{"cleanbrowsing_family", "CleanBrowsing Family — bez sadržaja za odrasle",
		[]string{"185.228.168.168", "185.228.169.168"}, true,
		"Blokira sadržaj za odrasle i prisiljava sigurno pretraživanje."},
	{"opendns_family", "OpenDNS FamilyShield — bez sadržaja za odrasle",
		[]string{"208.67.222.123", "208.67.220.123"}, true,
		"Blokira sadržaj za odrasle."},
	{"quad9", "Quad9", []string{"9.9.9.9", "149.112.112.112"}, false,
		"Blokira poznate zloćudne domene, ne i sadržaj za odrasle."},
	{"google", "Google", []string{"8.8.8.8", "8.8.4.4"}, false,
		"Bez filtriranja."},
	{"custom", "Vlastite adrese", nil, false,
		"Upiši adrese svojih poslužitelja."},
}

func dnsPresetByID(id string) *dnsPreset {
	for i := range dnsPresets {
		if dnsPresets[i].ID == id {
			return &dnsPresets[i]
		}
	}
	return nil
}

// matchDNSPreset prepoznaje koji izbor odgovara upisanim adresama, da sučelje
// pokaže ono što je stvarno postavljeno, a ne uvijek "vlastite adrese".
func matchDNSPreset(servers []string) string {
	if len(servers) == 0 {
		return "isp"
	}
	for _, p := range dnsPresets {
		if len(p.Servers) != len(servers) {
			continue
		}
		same := true
		for i := range p.Servers {
			if p.Servers[i] != servers[i] {
				same = false
				break
			}
		}
		if same {
			return p.ID
		}
	}
	return "custom"
}

// dnsmasqSection nalazi glavnu dnsmasq sekciju (ime joj je nasumično).
func dnsmasqSection(cfg map[string]uciSection) string {
	for name, sec := range cfg {
		if sectStr(sec, ".type") == "dnsmasq" {
			return name
		}
	}
	return ""
}

func (s *server) handleDNSUpstreamGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := uciGetConfig(r.Context(), "dhcp")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	section := dnsmasqSection(cfg)
	servers := []string{}
	noresolv := false
	if section != "" {
		servers = uciList(cfg[section], "server")
		noresolv = sectStr(cfg[section], "noresolv") == "1"
	}
	// zapisi oblika /domena/adresa su split DNS, ne vanjski poslužitelji
	plain := []string{}
	for _, v := range servers {
		if !strings.HasPrefix(v, "/") {
			plain = append(plain, v)
		}
	}
	preset := matchDNSPreset(plain)
	family := false
	if p := dnsPresetByID(preset); p != nil {
		family = p.Family
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"presets":  dnsPresets,
		"preset":   preset,
		"servers":  plain,
		"noresolv": noresolv,
		"family":   family,
		// filtar na razini DNS-a vrijedi samo dok klijenti doista koriste naš
		// DNS; zato sučelje mora znati je li prisilni DNS uključen
		"forced_dns_enabled": s.forcedDNSGet().Enabled,
	})
}

func (s *server) handleDNSUpstreamSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Preset  string `json:"preset"`
		Servers string `json:"servers"` // za "custom", razmakom odvojene
	}
	if !decodeBody(w, r, &in) {
		return
	}
	p := dnsPresetByID(strings.TrimSpace(in.Preset))
	if p == nil {
		writeErr(w, http.StatusBadRequest, "nepoznat izbor DNS poslužitelja")
		return
	}
	servers := append([]string{}, p.Servers...)
	if p.ID == "custom" {
		for _, a := range strings.Fields(in.Servers) {
			ip := net.ParseIP(a)
			if ip == nil {
				writeErr(w, http.StatusBadRequest, "neispravna adresa: "+a)
				return
			}
			servers = append(servers, ip.String())
		}
		if len(servers) == 0 {
			writeErr(w, http.StatusBadRequest, "upiši bar jednu adresu")
			return
		}
		if len(servers) > 4 {
			writeErr(w, http.StatusBadRequest, "najviše četiri adrese")
			return
		}
	}

	cfg, err := uciGetConfig(ctx, "dhcp")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	section := dnsmasqSection(cfg)
	if section == "" {
		writeErr(w, http.StatusNotFound, "dnsmasq sekcija ne postoji")
		return
	}
	backupName, err := s.backupConfig(dhcpConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}

	// split DNS zapisi (/domena/adresa) ostaju netaknuti — njima upravlja
	// drugi dio modula i nisu vanjski poslužitelji
	keep := []string{}
	for _, v := range uciList(cfg[section], "server") {
		if strings.HasPrefix(v, "/") {
			keep = append(keep, v)
		}
	}

	var b strings.Builder
	if _, ok := cfg[section]["server"]; ok {
		fmt.Fprintf(&b, "delete dhcp.%s.server\n", section)
	}
	for _, v := range keep {
		fmt.Fprintf(&b, "add_list dhcp.%s.server=%s\n", section, uciQuote(v))
	}
	for _, v := range servers {
		fmt.Fprintf(&b, "add_list dhcp.%s.server=%s\n", section, v)
	}
	if len(servers) > 0 {
		// bez ovoga dnsmasq uz naše poslužitelje pita i one od operatera, pa
		// filtar radi samo ponekad — a to je najgora vrsta filtra
		fmt.Fprintf(&b, "set dhcp.%s.noresolv=1\n", section)
	} else if sectStr(cfg[section], "noresolv") != "" {
		fmt.Fprintf(&b, "delete dhcp.%s.noresolv\n", section)
	}
	b.WriteString("commit dhcp\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// DNSSEC više ne smije misliti da je te poslužitelje dodao on, inače bi ih
	// pri isključivanju DNSSEC-a obrisao i tiho ukinuo filtar
	s.setSetting("dnssec_servers_added", "0")

	if err := serviceReload(ctx, "dnsmasq", "restart"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	label := p.Label
	addEvent(s, "info", "Vanjski DNS: "+label)
	writeJSON(w, http.StatusOK, map[string]any{
		"saved": true, "servers": servers, "family": p.Family,
		"backup": backupName,
	})
}
