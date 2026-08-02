package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Detekcija skeniranja portova — lagana zamjena za IDS.
//
// Ne gleda sadržaj prometa (to radi Suricata i za to treba puno procesora),
// nego samo ponašanje: tko s interneta u kratkom roku otvori neuobičajeno
// mnogo novih veza, radi izviđanje. Takav izvor se privremeno odbacuje.
// Sve se odvija u nftablesu, pa je trošak zanemariv i na slabom uređaju.
//
// Pravila idu u /etc/nftables.d, odakle ih fw4 sam uvlači u svoju tablicu —
// tako preživljavaju restart firewalla, a Saguaro ne dira tuđa pravila.

const scanNftFile = "/etc/nftables.d/20-saguaro-scan.nft"
const scanSetName = "sag_scanners"
const scanAllowSet = "sag_scan_allow"

// Zadane vrijednosti su namjerno blage: objavljeni web ili mail server kojemu
// posjetitelji dolaze kroz jedan operaterski NAT zna otvoriti puno veza u
// sekundi, a to nije napad.
const scanDefRate = 25
const scanDefBurst = 50
const scanDefBanMin = 60

var reScanElem = regexp.MustCompile(`(\d+\.\d+\.\d+\.\d+)`)

type scanSettings struct {
	Enabled  bool     `json:"enabled"`
	Rate     int      `json:"rate"`
	Burst    int      `json:"burst"`
	BanMin   int      `json:"ban_minutes"`
	AllowIPs string   `json:"allow_ips"`
	Blocked  []string `json:"blocked"`
	Devices  []string `json:"devices"`
}

func (s *server) scanState(ctx context.Context) scanSettings {
	st := scanSettings{
		Enabled:  s.getSetting("scan_enabled", "0") == "1",
		Rate:     atoiDef(s.getSetting("scan_rate", ""), scanDefRate),
		Burst:    atoiDef(s.getSetting("scan_burst", ""), scanDefBurst),
		BanMin:   atoiDef(s.getSetting("scan_ban_min", ""), scanDefBanMin),
		AllowIPs: s.getSetting("scan_allow", ""),
		Blocked:  []string{},
		Devices:  wanDevices(ctx),
	}
	// trenutno blokirani izvori se čitaju iz živog nftables seta
	out, err := exec.CommandContext(ctx, "nft", "list", "set", "inet", "fw4",
		scanSetName).Output()
	if err == nil {
		if i := strings.Index(string(out), "elements = {"); i >= 0 {
			for _, m := range reScanElem.FindAllString(string(out)[i:], -1) {
				st.Blocked = append(st.Blocked, m)
			}
		}
	}
	return st
}

func atoiDef(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// wanDevices vraća mrežne uređaje sučelja iz wan zone — samo se na njima
// gleda skeniranje, jer promet iz lokalne mreže nije predmet ove zaštite.
func wanDevices(ctx context.Context) []string {
	fwCfg, err := uciGetConfig(ctx, "firewall")
	if err != nil {
		return nil
	}
	netCfg, err := uciGetConfig(ctx, "network")
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	devs := []string{}
	for _, sec := range fwCfg {
		if sectStr(sec, ".type") != "zone" || sectStr(sec, "name") != "wan" {
			continue
		}
		for _, iface := range sectList(sec, "network") {
			d := sectStr(netCfg[iface], "device")
			if d != "" && !seen[d] {
				seen[d] = true
				devs = append(devs, d)
			}
		}
	}
	sort.Strings(devs)
	return devs
}

/* ---------- generiranje pravila ---------- */

func (s *server) writeScanRules(st scanSettings) error {
	if !st.Enabled {
		if _, err := os.Stat(scanNftFile); err == nil {
			return os.Remove(scanNftFile)
		}
		return nil
	}
	if len(st.Devices) == 0 {
		return fmt.Errorf("nijedno sučelje nije u internet (wan) zoni")
	}

	ifs := make([]string, 0, len(st.Devices))
	for _, d := range st.Devices {
		ifs = append(ifs, `"`+d+`"`)
	}
	iface := "iifname { " + strings.Join(ifs, ", ") + " }"

	var b strings.Builder
	b.WriteString("# Saguaro — detekcija skeniranja portova.\n")
	b.WriteString("# Datoteku održava Saguaro; ručne izmjene se gube pri sljedećoj primjeni.\n")
	b.WriteString("# fw4 je sam uvlači u tablicu inet fw4.\n\n")

	// iznimke: adrese koje se nikad ne označavaju kao skeneri
	fmt.Fprintf(&b, "set %s {\n\ttype ipv4_addr\n\tflags interval\n", scanAllowSet)
	allow := strings.Fields(st.AllowIPs)
	if len(allow) > 0 {
		fmt.Fprintf(&b, "\telements = { %s }\n", strings.Join(allow, ", "))
	}
	b.WriteString("}\n\n")

	// brojač po izvoru; kratak rok jer služi samo za mjerenje brzine
	fmt.Fprintf(&b, "set sag_scan_track {\n\ttype ipv4_addr\n"+
		"\tflags dynamic\n\ttimeout 1m\n}\n\n")

	// popis zatečenih skenera; iz njega ih vadi istek vremena
	fmt.Fprintf(&b, "set %s {\n\ttype ipv4_addr\n\tflags timeout\n"+
		"\ttimeout %dm\n}\n\n", scanSetName, st.BanMin)

	// hook prerouting prije prevođenja adresa (-150 < dstnat -100), pa se
	// skener odbacuje prije nego uopće dođe do objavljenog servera
	b.WriteString("chain sag_scan_detect {\n")
	b.WriteString("\ttype filter hook prerouting priority -150; policy accept;\n")
	fmt.Fprintf(&b, "\t%s ip saddr @%s return\n", iface, scanAllowSet)
	fmt.Fprintf(&b, "\t%s ip saddr @%s counter drop\n", iface, scanSetName)
	fmt.Fprintf(&b, "\t%s tcp flags & (fin|syn|rst|ack) == syn \\\n"+
		"\t\tadd @sag_scan_track { ip saddr limit rate over %d/second burst %d packets } \\\n"+
		"\t\tadd @%s { ip saddr } \\\n"+
		"\t\tlog prefix \"sag-scan: \" level warn limit rate 6/minute \\\n"+
		"\t\tcounter drop\n",
		iface, st.Rate, st.Burst, scanSetName)
	b.WriteString("}\n")

	return os.WriteFile(scanNftFile, []byte(b.String()), 0o644)
}

/* ---------- API ---------- */

func (s *server) handleScanSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Enabled  *bool  `json:"enabled"`
		Rate     int    `json:"rate"`
		Burst    int    `json:"burst"`
		BanMin   int    `json:"ban_minutes"`
		AllowIPs string `json:"allow_ips"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "nedostaje polje enabled")
		return
	}
	if in.Rate == 0 {
		in.Rate = scanDefRate
	}
	if in.Burst == 0 {
		in.Burst = scanDefBurst
	}
	if in.BanMin == 0 {
		in.BanMin = scanDefBanMin
	}
	switch {
	case in.Rate < 5 || in.Rate > 1000:
		writeErr(w, http.StatusBadRequest,
			"prag mora biti između 5 i 1000 novih veza u sekundi")
		return
	case in.Burst < in.Rate || in.Burst > 5000:
		writeErr(w, http.StatusBadRequest,
			"dopušteni nalet mora biti bar jednak pragu, a najviše 5000")
		return
	case in.BanMin < 1 || in.BanMin > 10080:
		writeErr(w, http.StatusBadRequest,
			"trajanje blokade mora biti između 1 minute i 7 dana")
		return
	}
	allow := strings.Fields(in.AllowIPs)
	for _, a := range allow {
		if !validAddr(a) {
			writeErr(w, http.StatusBadRequest, "neispravna iznimka (IP ili CIDR): "+a)
			return
		}
	}

	st := scanSettings{
		Enabled: *in.Enabled, Rate: in.Rate, Burst: in.Burst,
		BanMin: in.BanMin, AllowIPs: strings.Join(allow, " "),
		Devices: wanDevices(ctx),
	}
	if err := s.writeScanRules(st); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	for k, v := range map[string]string{
		"scan_enabled": boolSetting(st.Enabled),
		"scan_rate":    strconv.Itoa(st.Rate),
		"scan_burst":   strconv.Itoa(st.Burst),
		"scan_ban_min": strconv.Itoa(st.BanMin),
		"scan_allow":   st.AllowIPs,
	} {
		if err := s.setSetting(k, v); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	// uvlačenje datoteka iz /etc/nftables.d traži ponovno slaganje pravila
	if err := serviceReload(ctx, "firewall", "restart"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.scanState(ctx))
}

// handleScanClear prazni popis blokiranih izvora — za slučaj da je netko
// upao u zamku greškom (npr. alat za nadzor koji kuca prečesto).
func (s *server) handleScanClear(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := exec.CommandContext(ctx, "nft", "flush", "set", "inet", "fw4",
		scanSetName).Run(); err != nil {
		writeErr(w, http.StatusBadRequest,
			"popis nije dostupan — je li detekcija uključena?")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cleared": true})
}

// ensureScanRules vraća pravila nakon nadogradnje ili ručnog brisanja
// datoteke; poziva se pri pokretanju servisa.
func (s *server) ensureScanRules(ctx context.Context) {
	if s.getSetting("scan_enabled", "0") != "1" {
		return
	}
	if _, err := os.Stat(scanNftFile); err == nil {
		return
	}
	st := s.scanState(ctx)
	if err := s.writeScanRules(st); err != nil {
		return
	}
	serviceReload(ctx, "firewall", "restart")
}
