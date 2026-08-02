package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// Očvršćivanje: sitne, dobro poznate mjere koje OpenWrt zadano ne uključuje.
// Svaka je zasebna kvačica s objašnjenjem što radi i što se gubi ako se
// uključi, jer neke ovise o tome kako je uređaj spojen.

const sysctlFile = "/etc/sysctl.d/99-saguaro.conf"
const bogonRule = "sag_hd_bogon"

// bogonNets su rasponi koji nikad ne smiju stići s interneta kao izvor:
// privatne mreže, loopback, link-local i sličan promet uvijek znači
// krivotvorenu adresu (spoofing).
var bogonNets = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	"127.0.0.0/8", "169.254.0.0/16", "0.0.0.0/8",
	"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24",
	"224.0.0.0/4", "240.0.0.0/4",
}

type hardItem struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Detail  string `json:"detail"`
	Enabled bool   `json:"enabled"`
	Note    string `json:"note,omitempty"`
}

/* ---------- očitanje stvarnog stanja ---------- */

func (s *server) hardeningState(ctx context.Context) []hardItem {
	fwCfg, _ := uciGetConfig(ctx, "firewall")
	dhcpCfg, _ := uciGetConfig(ctx, "dhcp")
	httpCfg, _ := uciGetConfig(ctx, "uhttpd")

	items := []hardItem{}

	// 1. rp_filter
	rp := readSysctl("net.ipv4.conf.all.rp_filter")
	items = append(items, hardItem{
		ID:    "rp_filter",
		Label: "Odbaci pakete s krivotvorenom izvorišnom adresom",
		Detail: "Jezgra provjerava dolazi li paket sučeljem kojim bi se " +
			"odgovorilo njegovom pošiljatelju. Postavlja se na 'labavo', jer " +
			"bi strogo razbilo prebacivanje između više internet veza.",
		Enabled: rp == "2",
		Note:    "trenutno: " + rpLabel(rp),
	})

	// 2. ograničenje pinga s interneta
	pingSect, pingLimit := findPingRule(fwCfg)
	items = append(items, hardItem{
		ID:    "icmp_limit",
		Label: "Ograniči ping s interneta",
		Detail: "Uređaj i dalje odgovara na ping (korisno za dijagnostiku), " +
			"ali najviše 10 puta u sekundi — poplava pinga ne može ga zaokupiti.",
		Enabled: pingLimit != "",
		Note:    pingNote(pingSect, pingLimit),
	})

	// 3. bogon filtar
	_, hasBogon := fwCfg[bogonRule]
	items = append(items, hardItem{
		ID:    "bogon",
		Label: "Odbaci promet s privatnih adresa koji stiže s interneta",
		Detail: "Paket koji na WAN dolazi s adrese tipa 192.168.x.x ili " +
			"10.x.x.x je krivotvoren. Ne uključuj ako je uređaj spojen iza " +
			"drugog routera — tada je takav promet normalan.",
		Enabled: hasBogon,
	})

	// 4. DNS ne sluša na WAN-u
	notIf := []string{}
	if ds := findDnsmasqSection(dhcpCfg); ds != "" {
		notIf = sectList(dhcpCfg[ds], "notinterface")
	}
	items = append(items, hardItem{
		ID:    "dns_wan",
		Label: "DNS ne osluškuje na internet sučelju",
		Detail: "Firewall to ionako blokira, ali servis koji uopće ne sluša " +
			"prema internetu ne može postati odskočna daska za napade ni ako " +
			"se firewall jednom pogrešno podesi.",
		Enabled: len(notIf) > 0,
		Note:    notIfNote(notIf),
	})

	// 5. LuCI samo preko HTTPS-a
	redirect := false
	if u, ok := httpCfg["main"]; ok {
		redirect = sectStr(u, "redirect_https") == "1"
	}
	items = append(items, hardItem{
		ID:    "luci_https",
		Label: "LuCI preusmjeri na šifriranu vezu (HTTPS)",
		Detail: "Bez toga se root lozinka pri prijavi na LuCI šalje mrežom u " +
			"čitljivom obliku. Preglednik će upozoriti na certifikat jer je " +
			"samopotpisan — to je očekivano.",
		Enabled: redirect,
	})

	// 6. zadana IPsec pravila bez IPseca
	_, espOK := findRuleByName(fwCfg, "Allow-IPSec-ESP")
	_, isaOK := findRuleByName(fwCfg, "Allow-ISAKMP")
	ipsecInstalled := fileExists("/etc/init.d/ipsec") ||
		fileExists("/etc/init.d/strongswan")
	items = append(items, hardItem{
		ID:    "drop_ipsec_rules",
		Label: "Ukloni zadana IPsec pravila (IPsec nije instaliran)",
		Detail: "OpenWrt zadano propušta IPsec promet s interneta prema " +
			"lokalnoj mreži. Bez instaliranog IPseca to su otvorena vrata " +
			"koja ništa ne koriste.",
		Enabled: !espOK && !isaOK,
		Note:    ipsecNote(ipsecInstalled),
	})

	return items
}

func rpLabel(v string) string {
	switch v {
	case "0":
		return "isključeno"
	case "1":
		return "strogo (razbija više internet veza)"
	case "2":
		return "labavo"
	}
	return "nepoznato"
}

func pingNote(sect, limit string) string {
	if sect == "" {
		return "pravilo za ping s interneta ne postoji"
	}
	if limit == "" {
		return "trenutno bez ograničenja"
	}
	return "trenutno: " + limit
}

func notIfNote(list []string) string {
	if len(list) == 0 {
		return "DNS trenutno sluša i na internet sučelju"
	}
	return "ne sluša na: " + strings.Join(list, ", ")
}

func ipsecNote(installed bool) string {
	if installed {
		return "IPsec je instaliran — pravila su potrebna, ne diraj"
	}
	return ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func readSysctl(key string) string {
	out, err := exec.Command("sysctl", "-n", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// findPingRule pronalazi zadano OpenWrt pravilo koje propušta ping s WAN-a.
func findPingRule(cfg map[string]uciSection) (sect, limit string) {
	for name, sec := range cfg {
		if sectStr(sec, ".type") != "rule" {
			continue
		}
		if sectStr(sec, "src") == "wan" && sectStr(sec, "proto") == "icmp" &&
			sectStr(sec, "target") == "ACCEPT" &&
			strings.Contains(strings.Join(sectList(sec, "icmp_type"), " ")+
				sectStr(sec, "icmp_type"), "echo-request") &&
			sectStr(sec, "family") != "ipv6" {
			return name, sectStr(sec, "limit")
		}
	}
	return "", ""
}

func findRuleByName(cfg map[string]uciSection, name string) (string, bool) {
	for sect, sec := range cfg {
		if sectStr(sec, "name") == name {
			return sect, true
		}
	}
	return "", false
}

/* ---------- API ---------- */

func (s *server) handleHardeningGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"items": s.hardeningState(r.Context()),
	})
}

func (s *server) handleHardeningSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Items map[string]bool `json:"items"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	known := map[string]bool{}
	for _, i := range s.hardeningState(ctx) {
		known[i.ID] = true
	}
	ids := make([]string, 0, len(in.Items))
	for id := range in.Items {
		if !known[id] {
			writeErr(w, http.StatusBadRequest, "nepoznata stavka: "+id)
			return
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	applied := []string{}
	for _, id := range ids {
		if err := s.applyHardening(ctx, id, in.Items[id]); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		applied = append(applied, id)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"applied": applied,
		"items":   s.hardeningState(ctx),
	})
}

func (s *server) applyHardening(ctx context.Context, id string, on bool) error {
	switch id {
	case "rp_filter":
		return s.setRPFilter(on)
	case "icmp_limit":
		return s.setICMPLimit(ctx, on)
	case "bogon":
		return s.setBogon(ctx, on)
	case "dns_wan":
		return s.setDNSWan(ctx, on)
	case "luci_https":
		return s.setLuciHTTPS(ctx, on)
	case "drop_ipsec_rules":
		return s.setIPsecRules(ctx, on)
	}
	return fmt.Errorf("nepoznata stavka: %s", id)
}

/* ---------- pojedine mjere ---------- */

func (s *server) setRPFilter(on bool) error {
	if !on {
		os.Remove(sysctlFile)
		exec.Command("sysctl", "-w", "net.ipv4.conf.all.rp_filter=0").Run()
		exec.Command("sysctl", "-w", "net.ipv4.conf.default.rp_filter=0").Run()
		return nil
	}
	body := "# Saguaro: obrana od krivotvorenih izvorišnih adresa.\n" +
		"# Vrijednost 2 (labavo) je jedina ispravna kad postoji više internet\n" +
		"# veza — strogo (1) bi odbacivalo promet pri prebacivanju.\n" +
		"net.ipv4.conf.all.rp_filter=2\n" +
		"net.ipv4.conf.default.rp_filter=2\n"
	if err := os.WriteFile(sysctlFile, []byte(body), 0o644); err != nil {
		return err
	}
	if out, err := exec.Command("sysctl", "-p", sysctlFile).CombinedOutput(); err != nil {
		return fmt.Errorf("sysctl: %v: %s", err, out)
	}
	// postojeća sučelja imaju vlastitu vrijednost, pa ih treba proći redom
	entries, _ := os.ReadDir("/proc/sys/net/ipv4/conf")
	for _, e := range entries {
		exec.Command("sysctl", "-w",
			"net.ipv4.conf."+e.Name()+".rp_filter=2").Run()
	}
	return nil
}

func (s *server) setICMPLimit(ctx context.Context, on bool) error {
	cfg, err := uciGetConfig(ctx, "firewall")
	if err != nil {
		return err
	}
	sect, cur := findPingRule(cfg)
	if sect == "" {
		return fmt.Errorf("pravilo za ping s interneta ne postoji")
	}
	if _, err := s.backupConfig(firewallConfig); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	var b strings.Builder
	if on {
		fmt.Fprintf(&b, "set firewall.%s.limit=%s\n", sect, uciQuote("10/sec"))
	} else if cur != "" {
		fmt.Fprintf(&b, "delete firewall.%s.limit\n", sect)
	} else {
		return nil
	}
	b.WriteString("commit firewall\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		return err
	}
	return serviceReload(ctx, "firewall", "restart")
}

func (s *server) setBogon(ctx context.Context, on bool) error {
	cfg, err := uciGetConfig(ctx, "firewall")
	if err != nil {
		return err
	}
	_, exists := cfg[bogonRule]
	if !on {
		if !exists {
			return nil
		}
		if err := uciBatch(ctx, "delete firewall."+bogonRule+
			"\ncommit firewall\n"); err != nil {
			return err
		}
		return serviceReload(ctx, "firewall", "restart")
	}

	// zaštita od samonanesene štete: ako je WAN adresa privatna, uređaj je
	// iza drugog routera i ovakvo bi pravilo prekinulo vezu
	if priv, addr := wanIsPrivate(ctx); priv {
		return fmt.Errorf("internet sučelje ima privatnu adresu (%s) — "+
			"uređaj je iza drugog routera, pa bi ovo pravilo prekinulo vezu. "+
			"Uključi ga tek kad uređaj bude prvi u lancu, s javnom adresom", addr)
	}
	if _, err := s.backupConfig(firewallConfig); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "set firewall.%s=rule\n", bogonRule)
	fmt.Fprintf(&b, "set firewall.%s.name=%s\n", bogonRule,
		uciQuote("Saguaro: krivotvorene adrese"))
	fmt.Fprintf(&b, "set firewall.%s.src=wan\n", bogonRule)
	fmt.Fprintf(&b, "set firewall.%s.family=ipv4\n", bogonRule)
	fmt.Fprintf(&b, "set firewall.%s.target=DROP\n", bogonRule)
	fmt.Fprintf(&b, "delete firewall.%s.src_ip\n", bogonRule)
	for _, n := range bogonNets {
		fmt.Fprintf(&b, "add_list firewall.%s.src_ip=%s\n", bogonRule, n)
	}
	b.WriteString("commit firewall\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		return err
	}
	return serviceReload(ctx, "firewall", "restart")
}

// wanIsPrivate javlja ima li ijedno WAN sučelje privatnu adresu.
func wanIsPrivate(ctx context.Context) (bool, string) {
	fwCfg, err := uciGetConfig(ctx, "firewall")
	if err != nil {
		return false, ""
	}
	for _, sec := range fwCfg {
		if sectStr(sec, ".type") != "zone" || sectStr(sec, "name") != "wan" {
			continue
		}
		for _, iface := range sectList(sec, "network") {
			addr := wanLocalAddr(ctx, iface)
			if addr == "" {
				continue
			}
			if ip := net.ParseIP(addr); ip != nil && ip.IsPrivate() {
				return true, addr
			}
		}
	}
	return false, ""
}

func (s *server) setDNSWan(ctx context.Context, on bool) error {
	cfg, err := uciGetConfig(ctx, "dhcp")
	if err != nil {
		return err
	}
	ds := findDnsmasqSection(cfg)
	if ds == "" {
		return fmt.Errorf("dnsmasq konfiguracija nije pronađena")
	}
	cur := sectList(cfg[ds], "notinterface")
	if _, err := s.backupConfig(dhcpConfig); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	var b strings.Builder
	if len(cur) > 0 {
		fmt.Fprintf(&b, "delete dhcp.%s.notinterface\n", ds)
	}
	if on {
		fwCfg, err := uciGetConfig(ctx, "firewall")
		if err != nil {
			return err
		}
		wans := []string{}
		for _, sec := range fwCfg {
			if sectStr(sec, ".type") == "zone" && sectStr(sec, "name") == "wan" {
				wans = append(wans, sectList(sec, "network")...)
			}
		}
		if len(wans) == 0 {
			return fmt.Errorf("nijedno sučelje nije u internet (wan) zoni")
		}
		sort.Strings(wans)
		for _, wn := range wans {
			fmt.Fprintf(&b, "add_list dhcp.%s.notinterface=%s\n", ds, wn)
		}
	} else if len(cur) == 0 {
		return nil
	}
	b.WriteString("commit dhcp\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		return err
	}
	// promjena vezanja sučelja traži restart, reload nije dovoljan
	return serviceReload(ctx, "dnsmasq", "restart")
}

func (s *server) setLuciHTTPS(ctx context.Context, on bool) error {
	if _, err := s.backupConfig("/etc/config/uhttpd"); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if err := uciBatch(ctx, fmt.Sprintf(
		"set uhttpd.main.redirect_https=%s\ncommit uhttpd\n",
		boolSetting(on))); err != nil {
		return err
	}
	return serviceReload(ctx, "uhttpd", "restart")
}

func (s *server) setIPsecRules(ctx context.Context, on bool) error {
	cfg, err := uciGetConfig(ctx, "firewall")
	if err != nil {
		return err
	}
	if !on {
		// vraćanje zadanih pravila; radi se samo ako ih stvarno nema
		var b strings.Builder
		if _, ok := findRuleByName(cfg, "Allow-IPSec-ESP"); !ok {
			b.WriteString("set firewall.sag_ipsec_esp=rule\n")
			b.WriteString("set firewall.sag_ipsec_esp.name=Allow-IPSec-ESP\n")
			b.WriteString("set firewall.sag_ipsec_esp.src=wan\n")
			b.WriteString("set firewall.sag_ipsec_esp.dest=lan\n")
			b.WriteString("set firewall.sag_ipsec_esp.proto=esp\n")
			b.WriteString("set firewall.sag_ipsec_esp.target=ACCEPT\n")
		}
		if _, ok := findRuleByName(cfg, "Allow-ISAKMP"); !ok {
			b.WriteString("set firewall.sag_ipsec_isakmp=rule\n")
			b.WriteString("set firewall.sag_ipsec_isakmp.name=Allow-ISAKMP\n")
			b.WriteString("set firewall.sag_ipsec_isakmp.src=wan\n")
			b.WriteString("set firewall.sag_ipsec_isakmp.dest=lan\n")
			b.WriteString("set firewall.sag_ipsec_isakmp.dest_port=500\n")
			b.WriteString("set firewall.sag_ipsec_isakmp.proto=udp\n")
			b.WriteString("set firewall.sag_ipsec_isakmp.target=ACCEPT\n")
		}
		if b.Len() == 0 {
			return nil
		}
		b.WriteString("commit firewall\n")
		if err := uciBatch(ctx, b.String()); err != nil {
			return err
		}
		return serviceReload(ctx, "firewall", "restart")
	}

	if fileExists("/etc/init.d/ipsec") || fileExists("/etc/init.d/strongswan") {
		return fmt.Errorf("IPsec je instaliran — ta su pravila potrebna za rad")
	}
	var b strings.Builder
	for _, name := range []string{"Allow-IPSec-ESP", "Allow-ISAKMP"} {
		if sect, ok := findRuleByName(cfg, name); ok {
			fmt.Fprintf(&b, "delete firewall.%s\n", sect)
		}
	}
	if b.Len() == 0 {
		return nil
	}
	if _, err := s.backupConfig(firewallConfig); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	b.WriteString("commit firewall\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		return err
	}
	return serviceReload(ctx, "firewall", "restart")
}

// findDnsmasqSection pronalazi glavnu dnsmasq sekciju (ime joj je generirano,
// pa se traži po tipu).
func findDnsmasqSection(cfg map[string]uciSection) string {
	for name, sec := range cfg {
		if sectStr(sec, ".type") == "dnsmasq" {
			return name
		}
	}
	return ""
}
