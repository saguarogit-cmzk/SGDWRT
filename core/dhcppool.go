package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Upravljanje DHCP poolovima (raspon adresa koje uređaj dijeli) i, jednako
// važno, **istina o tome radi li DHCP uopće**.
//
// OpenWrt pri pokretanju provjeri ima li na toj mreži već nekog tko dijeli
// adrese i, ako ima, svoj DHCP tiho ne pokrene. To je dobra zaštita — dva
// DHCP poslužitelja na istoj mreži rade nered koji se teško nalazi — ali u
// sučelju je dosad izgledalo kao da pool radi, a popis leaseova je stajao
// prazan bez ijedne riječi objašnjenja (nađeno na uređaju 05.08.2026.).

/* ---------- raspon: uci brojevi <-> adrese ---------- */

// ifaceIPv4 vraća adresu i mrežu sučelja iz network configa.
func ifaceIPv4(ctx context.Context, iface string) (net.IP, *net.IPNet) {
	cfg, err := uciGetConfig(ctx, "network")
	if err != nil {
		return nil, nil
	}
	sec, ok := cfg[iface]
	if !ok {
		return nil, nil
	}
	ip := net.ParseIP(sectStr(sec, "ipaddr"))
	mask := net.ParseIP(sectStr(sec, "netmask"))
	if ip == nil || ip.To4() == nil {
		return nil, nil
	}
	if mask == nil || mask.To4() == nil {
		mask = net.IP(net.CIDRMask(24, 32)) // uobičajeno kad maska nije upisana
	}
	m := net.IPMask(mask.To4())
	return ip.To4(), &net.IPNet{IP: ip.Mask(m), Mask: m}
}

// poolRange pretvara uci vrijednosti start/limit u prvu i zadnju adresu.
// OpenWrt broji od mrežne adrese: start=100 na 192.168.50.0/24 znači
// 192.168.50.100, a limit je koliko ih ima.
func poolRange(n *net.IPNet, start, limit int) (net.IP, net.IP) {
	if n == nil || start <= 0 || limit <= 0 {
		return nil, nil
	}
	base := n.IP.To4()
	if base == nil {
		return nil, nil
	}
	first := ipAdd(base, start)
	last := ipAdd(base, start+limit-1)
	if !n.Contains(first) || !n.Contains(last) {
		return first, last // vraćamo kakvi jesu; provjera je posao validacije
	}
	return first, last
}

func ipAdd(ip net.IP, n int) net.IP {
	v := ip.To4()
	if v == nil {
		return nil
	}
	x := uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
	x += uint32(n)
	return net.IPv4(byte(x>>24), byte(x>>16), byte(x>>8), byte(x)).To4()
}

func ipOffset(n *net.IPNet, ip net.IP) int {
	base, v := n.IP.To4(), ip.To4()
	if base == nil || v == nil {
		return -1
	}
	b := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	x := uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
	if x < b {
		return -1
	}
	return int(x - b)
}

func cidrOrEmpty(n *net.IPNet) string {
	if n == nil {
		return ""
	}
	return n.String()
}

func ipOrEmpty(ip net.IP) string {
	if ip == nil {
		return ""
	}
	return ip.String()
}

// devName vraća stvarni uređaj sučelja (lan -> br-lan). Dnevnik piše ime
// uređaja, a konfiguracija ime mreže, pa se to mora spojiti.
func devName(ctx context.Context, iface string) string {
	var dump ubusIfaceDump
	if ubusCall(ctx, "network.interface", "dump", &dump) != nil {
		return iface
	}
	for _, in := range dump.Interface {
		if in.Name == iface && in.Device != "" {
			return in.Device
		}
	}
	return iface
}

/* ---------- radi li DHCP stvarno ---------- */

// dhcpRanges čita raspone koje je dnsmasq stvarno dobio u svojoj konfiguraciji.
// Ako pool postoji u uci-ju, a ovdje ga nema, DHCP za tu mrežu **ne radi**.
func dhcpRanges() []string {
	out := []string{}
	ents, err := os.ReadDir("/var/etc")
	if err != nil {
		return out
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), "dnsmasq.conf") {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/var/etc", e.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "dhcp-range=") {
				out = append(out, strings.TrimPrefix(line, "dhcp-range="))
			}
		}
	}
	return out
}

// dhcpBlockedIfaces vraća sučelja na kojima je OpenWrt odustao od DHCP-a jer
// je zatekao tuđi poslužitelj. Poruka stoji u sistemskom dnevniku; drugog
// strojno čitljivog traga nema.
func dhcpBlockedIfaces(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b, err := exec.CommandContext(c, "logread").Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.Contains(line, "found already running DHCP-server") {
			continue
		}
		// ... on interface 'br-lan' refusing to start ...
		i := strings.Index(line, "interface '")
		if i < 0 {
			continue
		}
		rest := line[i+len("interface '"):]
		if j := strings.Index(rest, "'"); j > 0 {
			out[rest[:j]] = true
		}
	}
	return out
}

/* ---------- postavljanje raspona ---------- */

type dhcpPoolIn struct {
	Interface string `json:"interface"`
	FirstIP   string `json:"first_ip"`
	LastIP    string `json:"last_ip"`
	LeaseTime string `json:"leasetime"`
	Enabled   *bool  `json:"enabled"`
	// što uređaj javlja klijentima; prazno = javi sam sebe (uobičajeno)
	Gateway string `json:"gateway"`
	DNS     string `json:"dns"` // jedna ili više adresa, razmakom odvojene
	Domain  string `json:"domain"`
}

// dhcpOptions slaže uci `dhcp_option` popis iz polja koja korisnik razumije.
// Brojevi su standardni DHCP kodovi: 3 = gateway, 6 = DNS, 15 = domena.
func dhcpOptions(in dhcpPoolIn) ([]string, error) {
	out := []string{}
	if g := strings.TrimSpace(in.Gateway); g != "" {
		if net.ParseIP(g) == nil || net.ParseIP(g).To4() == nil {
			return nil, fmt.Errorf("gateway mora biti IPv4 adresa")
		}
		out = append(out, "3,"+g)
	}
	if d := strings.Fields(in.DNS); len(d) > 0 {
		for _, a := range d {
			if net.ParseIP(a) == nil || net.ParseIP(a).To4() == nil {
				return nil, fmt.Errorf("DNS %q nije IPv4 adresa", a)
			}
		}
		if len(d) > 3 {
			return nil, fmt.Errorf("najviše tri DNS adrese")
		}
		out = append(out, "6,"+strings.Join(d, ","))
	}
	if dom := strings.TrimSpace(strings.ToLower(in.Domain)); dom != "" {
		if !validDNSName(dom) {
			return nil, fmt.Errorf("domena %q nije ispravna", dom)
		}
		out = append(out, "15,"+dom)
	}
	return out, nil
}

// parseDHCPOptions čita natrag ono što je gore zapisano, za prikaz u sučelju.
func parseDHCPOptions(list []string) (gateway, dns, domain string) {
	for _, o := range list {
		parts := strings.SplitN(o, ",", 2)
		if len(parts) != 2 {
			continue
		}
		switch strings.TrimSpace(parts[0]) {
		case "3":
			gateway = parts[1]
		case "6":
			dns = strings.ReplaceAll(parts[1], ",", " ")
		case "15":
			domain = parts[1]
		}
	}
	return
}

// validLeaseTime prihvaća oblik koji razumije dnsmasq: broj + m/h/d, ili
// "infinite". Kriva vrijednost ruši dnsmasq pri pokretanju, pa se provjerava.
func validLeaseTime(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "infinite" {
		return true
	}
	unit := s[len(s)-1]
	if unit != 'm' && unit != 'h' && unit != 'd' {
		return false
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	return err == nil && n > 0 && n <= 9999
}

func (s *server) handleDHCPPoolSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in dhcpPoolIn
	if !decodeBody(w, r, &in) {
		return
	}
	in.Interface = strings.TrimSpace(in.Interface)
	if in.Interface == "" {
		writeErr(w, http.StatusBadRequest, "nedostaje sučelje")
		return
	}
	if in.LeaseTime == "" {
		in.LeaseTime = "12h"
	}
	if !validLeaseTime(in.LeaseTime) {
		writeErr(w, http.StatusBadRequest,
			"trajanje leasea mora biti npr. 30m, 12h ili 2d")
		return
	}

	devIP, subnet := ifaceIPv4(ctx, in.Interface)
	if subnet == nil {
		writeErr(w, http.StatusBadRequest,
			"sučelje "+in.Interface+" nema statičku IPv4 adresu, pa ne može dijeliti adrese")
		return
	}
	first := net.ParseIP(strings.TrimSpace(in.FirstIP))
	last := net.ParseIP(strings.TrimSpace(in.LastIP))
	if first == nil || last == nil || first.To4() == nil || last.To4() == nil {
		writeErr(w, http.StatusBadRequest, "prva i zadnja adresa moraju biti IPv4")
		return
	}
	switch {
	case !subnet.Contains(first) || !subnet.Contains(last):
		writeErr(w, http.StatusBadRequest, "raspon mora biti unutar mreže "+subnet.String())
		return
	case ipOffset(subnet, last) < ipOffset(subnet, first):
		writeErr(w, http.StatusBadRequest, "zadnja adresa je manja od prve")
		return
	case devIP != nil && !first.Equal(devIP) && !last.Equal(devIP) &&
		ipOffset(subnet, devIP) >= ipOffset(subnet, first) &&
		ipOffset(subnet, devIP) <= ipOffset(subnet, last):
		writeErr(w, http.StatusBadRequest, "u rasponu je adresa samog uređaja ("+
			devIP.String()+") — pomakni početak ili kraj")
		return
	case first.Equal(subnet.IP):
		writeErr(w, http.StatusBadRequest, "prva adresa je mrežna adresa, uzmi sljedeću")
		return
	}
	start := ipOffset(subnet, first)
	limit := ipOffset(subnet, last) - start + 1
	if start <= 0 || limit <= 0 || limit > 4096 {
		writeErr(w, http.StatusBadRequest, "raspon nije razuman (najviše 4096 adresa)")
		return
	}

	opts, err := dhcpOptions(in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	cfg, err := uciGetConfig(ctx, "dhcp")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	section := ""
	for name, sec := range cfg {
		if sectStr(sec, ".type") != "dhcp" {
			continue
		}
		iface := sectStr(sec, "interface")
		if iface == in.Interface || (iface == "" && name == in.Interface) {
			section = name
			break
		}
	}
	created := false
	if section == "" {
		// pool za tu mrežu još ne postoji — stvara se pod našim prefiksom,
		// da se poslije vidi tko ga je napravio (D-011)
		section = sagPrefix + "dhcp_" + sanitizeDNSName(in.Interface)
		created = true
	}

	backupName, err := s.backupConfig(dhcpConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}
	var b strings.Builder
	if created {
		fmt.Fprintf(&b, "set dhcp.%s=dhcp\n", section)
		fmt.Fprintf(&b, "set dhcp.%s.interface=%s\n", section, in.Interface)
		fmt.Fprintf(&b, "set dhcp.%s.dhcpv4=server\n", section)
	}
	fmt.Fprintf(&b, "set dhcp.%s.start=%d\n", section, start)
	fmt.Fprintf(&b, "set dhcp.%s.limit=%d\n", section, limit)
	fmt.Fprintf(&b, "set dhcp.%s.leasetime=%s\n", section, in.LeaseTime)
	if _, ok := cfg[section]["dhcp_option"]; ok {
		fmt.Fprintf(&b, "delete dhcp.%s.dhcp_option\n", section)
	}
	for _, o := range opts {
		fmt.Fprintf(&b, "add_list dhcp.%s.dhcp_option=%s\n", section, uciQuote(o))
	}
	if in.Enabled != nil {
		if *in.Enabled {
			if sectStr(cfg[section], "ignore") != "" {
				fmt.Fprintf(&b, "delete dhcp.%s.ignore\n", section)
			}
		} else {
			fmt.Fprintf(&b, "set dhcp.%s.ignore=1\n", section)
		}
	}
	b.WriteString("commit dhcp\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := serviceReload(ctx, "dnsmasq", "restart"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	addEvent(s, "info", fmt.Sprintf("DHCP raspon za %s: %s–%s (%s)",
		in.Interface, first, last, in.LeaseTime))
	writeJSON(w, http.StatusOK, map[string]any{
		"saved": true, "start": start, "limit": limit,
		"created": created, "backup": backupName,
	})
}
