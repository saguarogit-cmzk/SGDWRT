package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Zaštita prometa: banIP (blokada zloćudnih IP adresa + zemlje) i
// adblock-fast (blokada domena kroz dnsmasq). Oba paketa imaju vlastite
// uci konfiguracije koje su namijenjene uređivanju — mijenjamo samo
// ciljane opcije, uz backup (isti pristup kao dhcp.lan.ignore).
const banipConfig = "/etc/config/banip"
const adblockConfig = "/etc/config/adblock-fast"
const banipRuntime = "/var/run/banIP/banIP.runtime.json"
const banipAllowlist = "/etc/banip/banip.allowlist"

var reCountry = regexp.MustCompile(`^[a-z]{2}$`)

// Kurirani izbor banIP feedova (niska stopa lažnih pozitiva, uz opis za GUI).
var banipFeeds = []struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}{
	{"firehol1", "FireHOL level 1 — osnovni skup poznatih napadača (preporučeno)"},
	{"ipsum", "IPsum — agregat više crnih lista (preporučeno)"},
	{"dshield", "DShield — trenutno najaktivniji napadači"},
	{"feodo", "Feodo — upravljački poslužitelji bankovnog zloćudnog softvera"},
	{"urlhaus", "URLhaus — poslužitelji koji šire zloćudni softver"},
	{"doh", "Javni DoH poslužitelji — sprječava zaobilaženje DNS blokada"},
	{"proxy", "Otvoreni proxy poslužitelji"},
	{"tor", "Tor izlazni čvorovi"},
}

func validBanipFeed(id string) bool {
	for _, f := range banipFeeds {
		if f.ID == id {
			return true
		}
	}
	return false
}

/* ---------- pregled ---------- */

func (s *server) handleProtectionGet(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}

	// banIP
	banip := map[string]any{"installed": false}
	if _, err := os.Stat("/etc/init.d/banip"); err == nil {
		banip["installed"] = true
		if b, err := os.ReadFile(banipAllowlist); err == nil {
			ips := []string{}
			for _, l := range strings.Split(string(b), "\n") {
				l = strings.TrimSpace(l)
				if l != "" && !strings.HasPrefix(l, "#") {
					ips = append(ips, l)
				}
			}
			banip["allow_ips"] = strings.Join(ips, " ")
		}
		cfg, err := uciGetConfig(r.Context(), "banip")
		if err == nil {
			if g, ok := cfg["global"]; ok {
				banip["enabled"] = sectStr(g, "ban_enabled") == "1"
				feeds := []string{}
				for _, f := range sectList(g, "ban_feed") {
					if f != "country" {
						feeds = append(feeds, f)
					}
				}
				banip["feeds"] = feeds
				banip["countries"] = strings.Join(sectList(g, "ban_country"), " ")
			}
		}
		banip["available_feeds"] = banipFeeds
		// runtime izvještaj je JSON datoteka koju banIP sam održava
		if b, err := os.ReadFile(banipRuntime); err == nil {
			var rt any
			if json.Unmarshal(b, &rt) == nil {
				banip["runtime"] = rt
			}
		}
	}
	out["banip"] = banip

	// adblock-fast
	ad := map[string]any{"installed": false}
	if _, err := os.Stat("/etc/init.d/adblock-fast"); err == nil {
		ad["installed"] = true
		cfg, err := uciGetConfig(r.Context(), "adblock-fast")
		if err == nil {
			if g, ok := cfg["config"]; ok {
				ad["enabled"] = sectStr(g, "enabled") == "1"
				ad["allowed_domains"] = strings.Join(sectList(g, "allowed_domain"), " ")
			}
			type entry struct {
				Section string `json:"section"`
				Name    string `json:"name"`
				Size    string `json:"size"`
				Enabled bool   `json:"enabled"`
			}
			entries := []entry{}
			for name, sec := range cfg {
				if sectStr(sec, ".type") != "file_url" {
					continue
				}
				entries = append(entries, entry{
					Section: name, Name: sectStr(sec, "name"),
					Size:    sectStr(sec, "size"),
					Enabled: sectStr(sec, "enabled") == "1",
				})
			}
			sort.Slice(entries, func(i, j int) bool {
				return entries[i].Name < entries[j].Name
			})
			ad["entries"] = entries
		}
		// generirana dnsmasq datoteka = dokaz da blokada stvarno radi
		for _, p := range []string{"/var/run/adblock-fast/dnsmasq.servers",
			"/tmp/dnsmasq.d/adblock-fast", "/etc/dnsmasq.d/adblock-fast"} {
			if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
				ad["active_file"] = p
				ad["active_size"] = fi.Size()
				break
			}
		}
	}
	out["adblock"] = ad
	out["scan"] = s.scanState(r.Context())

	writeJSON(w, http.StatusOK, out)
}

/* ---------- banIP postavke ---------- */

func (s *server) handleBanipSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Enabled   *bool    `json:"enabled"`
		Feeds     []string `json:"feeds"`
		Countries string   `json:"countries"`
		AllowIPs  string   `json:"allow_ips"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "nedostaje polje enabled")
		return
	}
	for _, f := range in.Feeds {
		if !validBanipFeed(f) {
			writeErr(w, http.StatusBadRequest, "nepoznat feed: "+f)
			return
		}
	}
	allowIPs := strings.Fields(in.AllowIPs)
	for _, a := range allowIPs {
		if !validAddr(a) {
			writeErr(w, http.StatusBadRequest, "neispravna iznimka (IP ili CIDR): "+a)
			return
		}
	}
	countries := strings.Fields(strings.ToLower(in.Countries))
	for _, c := range countries {
		if !reCountry.MatchString(c) {
			writeErr(w, http.StatusBadRequest,
				"zemlja mora biti dvoslovna oznaka (npr. ru cn): "+c)
			return
		}
	}
	if *in.Enabled && len(in.Feeds) == 0 && len(countries) == 0 {
		writeErr(w, http.StatusBadRequest,
			"uključi bar jedan izvor blokada ili navedi zemlje")
		return
	}

	backupName, err := s.backupConfig(banipConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}
	cfg, err := uciGetConfig(ctx, "banip")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	g := cfg["global"]

	var b strings.Builder
	en := "0"
	if *in.Enabled {
		en = "1"
	}
	b.WriteString("set banip.global.ban_enabled=" + en + "\n")
	// banIP sam pogađa koje je sučelje WAN, i to prema zadanoj ruti — ako
	// lokalna mreža ima gateway (npr. kao pričuvna veza), pogodi krivo i
	// filtrira LAN umjesto interneta. Zato mu izričito upisujemo WAN sučelja.
	writeBanipWAN(ctx, &b, g)
	if _, has := g["ban_feed"]; has {
		b.WriteString("delete banip.global.ban_feed\n")
	}
	for _, f := range in.Feeds {
		b.WriteString("add_list banip.global.ban_feed=" + f + "\n")
	}
	if _, has := g["ban_country"]; has {
		b.WriteString("delete banip.global.ban_country\n")
	}
	if len(countries) > 0 {
		b.WriteString("add_list banip.global.ban_feed=country\n")
		for _, c := range countries {
			b.WriteString("add_list banip.global.ban_country=" + c + "\n")
		}
	}
	b.WriteString("commit banip\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// iznimke: adrese koje se nikad ne blokiraju (banip.allowlist datoteka,
	// predviđena za uređivanje)
	var al strings.Builder
	al.WriteString("# Saguaro iznimke — adrese koje se nikad ne blokiraju\n")
	for _, a := range allowIPs {
		al.WriteString(a + "\n")
	}
	if err := os.WriteFile(banipAllowlist, []byte(al.String()), 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	action := "restart"
	if !*in.Enabled {
		action = "stop"
	}
	if err := serviceReload(ctx, "banip", action); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": *in.Enabled, "backup": backupName,
		"note": "preuzimanje lista traje minutu-dvije nakon uključivanja",
	})
}

/* ---------- adblock-fast postavke ---------- */

func (s *server) handleAdblockSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Enabled        *bool    `json:"enabled"`
		Sections       []string `json:"sections"` // uključene file_url sekcije
		AllowedDomains string   `json:"allowed_domains"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "nedostaje polje enabled")
		return
	}
	allowed := []string{}
	for _, d := range strings.Fields(strings.ToLower(in.AllowedDomains)) {
		if !validDNSName(d) {
			writeErr(w, http.StatusBadRequest, "neispravna domena u iznimkama: "+d)
			return
		}
		allowed = append(allowed, d)
	}
	cfg, err := uciGetConfig(ctx, "adblock-fast")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	wanted := map[string]bool{}
	for _, sect := range in.Sections {
		sec, ok := cfg[sect]
		if !ok || sectStr(sec, ".type") != "file_url" {
			writeErr(w, http.StatusBadRequest, "nepoznata lista: "+sect)
			return
		}
		wanted[sect] = true
	}
	if *in.Enabled && len(wanted) == 0 {
		writeErr(w, http.StatusBadRequest, "uključi bar jednu listu domena")
		return
	}

	backupName, err := s.backupConfig(adblockConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}
	var b strings.Builder
	en := "0"
	if *in.Enabled {
		en = "1"
	}
	b.WriteString("set adblock-fast.config.enabled=" + en + "\n")
	if g, ok := cfg["config"]; ok {
		if _, has := g["allowed_domain"]; has {
			b.WriteString("delete adblock-fast.config.allowed_domain\n")
		}
	}
	for _, d := range allowed {
		b.WriteString("add_list adblock-fast.config.allowed_domain=" + d + "\n")
	}
	for name, sec := range cfg {
		if sectStr(sec, ".type") != "file_url" {
			continue
		}
		v := "0"
		if wanted[name] {
			v = "1"
		}
		b.WriteString("set adblock-fast." + name + ".enabled=" + v + "\n")
	}
	b.WriteString("commit adblock-fast\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	action := "restart"
	if !*in.Enabled {
		action = "stop"
	}
	if err := serviceReload(ctx, "adblock-fast", action); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": *in.Enabled, "backup": backupName,
		"note": "preuzimanje i obrada lista traje minutu-dvije",
	})
}

// writeBanipWAN upisuje banIP-u konkretna WAN sučelja umjesto automatskog
// pogađanja. Sučelja se čitaju iz wan firewall zone (ondje su i dodatni WAN-ovi
// za failover), a uređaji iz mrežne konfiguracije.
func writeBanipWAN(ctx context.Context, b *strings.Builder, g uciSection) {
	fwCfg, err := uciGetConfig(ctx, "firewall")
	if err != nil {
		return
	}
	netCfg, err := uciGetConfig(ctx, "network")
	if err != nil {
		return
	}
	var ifv4, ifv6, devs []string
	seen := map[string]bool{}
	for _, sec := range fwCfg {
		if sectStr(sec, ".type") != "zone" || sectStr(sec, "name") != "wan" {
			continue
		}
		for _, iface := range sectList(sec, "network") {
			ns, ok := netCfg[iface]
			if !ok {
				continue
			}
			// dhcpv6 / *6 sučelja idu u IPv6 popis
			if strings.Contains(sectStr(ns, "proto"), "6") || strings.HasSuffix(iface, "6") {
				ifv6 = append(ifv6, iface)
			} else {
				ifv4 = append(ifv4, iface)
			}
			if d := sectStr(ns, "device"); d != "" && !seen[d] {
				seen[d] = true
				devs = append(devs, d)
			}
		}
	}
	if len(ifv4) == 0 || len(devs) == 0 {
		return // ništa pouzdano — bolje ostaviti banIP-ovu automatiku
	}
	sort.Strings(ifv4)
	sort.Strings(ifv6)
	sort.Strings(devs)

	b.WriteString("set banip.global.ban_autodetect=0\n")
	for _, k := range []string{"ban_ifv4", "ban_ifv6", "ban_dev"} {
		if _, has := g[k]; has {
			b.WriteString("delete banip.global." + k + "\n")
		}
	}
	for _, v := range ifv4 {
		b.WriteString("add_list banip.global.ban_ifv4=" + v + "\n")
	}
	for _, v := range ifv6 {
		b.WriteString("add_list banip.global.ban_ifv6=" + v + "\n")
	}
	for _, v := range devs {
		b.WriteString("add_list banip.global.ban_dev=" + v + "\n")
	}
	// IPv6 se zadano ne filtrira, a WAN ga ima; blokade se zapisuju u log
	b.WriteString("set banip.global.ban_protov6=" +
		boolSetting(len(ifv6) > 0) + "\n")
	b.WriteString("set banip.global.ban_logprerouting=1\n")
}
