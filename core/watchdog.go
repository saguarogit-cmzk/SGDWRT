package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Nadzorna petlja: sve što uređaj sam primijeti i o čemu može javiti.
// Vrti se svakih 60 sekundi; skuplje provjere (javna adresa, certifikati)
// imaju vlastiti razmak. Svaka promjena prolazi kroz s.alert, koji brine o
// tome je li ta vrsta upozorenja uključena i da se ista poruka ne ponavlja.

const watchInterval = 60 * time.Second

// pubIPServices su izvori za očitanje javne adrese. Redom se pokušava dok
// jedan ne odgovori — da jedan pokvaren servis ne znači lažnu uzbunu.
var pubIPServices = []string{
	"https://api.ipify.org",
	"https://ifconfig.me/ip",
	"https://icanhazip.com",
}

func (s *server) watchdogLoop() {
	// prvi krug čeka da se sustav slegne nakon pokretanja
	time.Sleep(20 * time.Second)
	s.checkReboot()
	tick := 0
	for {
		ctx := context.Background()
		s.checkWAN(ctx)
		s.checkServices(ctx)
		s.checkResources(ctx)
		s.checkVPNClients(ctx)
		s.checkFailedLogins(ctx)
		s.checkScanners(ctx)
		s.reportSample(ctx)
		if tick%5 == 0 { // svakih 5 minuta
			s.checkPublicIP(ctx)
			s.checkIPv6Prefix(ctx)
		}
		if tick%720 == 0 { // jednom dnevno
			s.checkCerts()
			s.reportDue(ctx)
		}
		tick++
		time.Sleep(watchInterval)
	}
}

/* ---------- ponovno pokretanje uređaja ---------- */

// checkReboot javlja da se uređaj digao. Prepoznaje se po tome što je uptime
// kratak, a zadnji viđeni uptime bio dulji — tako se restart Saguara samog
// ne prijavljuje kao restart uređaja.
func (s *server) checkReboot() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var info struct {
		Uptime int64 `json:"uptime"`
	}
	if err := ubusCall(ctx, "system", "info", &info); err != nil {
		return
	}
	prev, _ := strconv.ParseInt(s.getSetting("last_uptime", ""), 10, 64)
	s.setSetting("last_uptime", strconv.FormatInt(info.Uptime, 10))
	if info.Uptime < 300 && (prev == 0 || prev > info.Uptime) {
		s.alert("reboot", "warning", fmt.Sprintf(
			"Uređaj se ponovno pokrenuo (radi %d sekundi).", info.Uptime))
	}
}

/* ---------- internet veza ---------- */

// checkWAN prati stanje svake WAN veze. Izvor je mwan3 ako je uključen (on
// već mjeri pingom), inače stanje sučelja iz netifda.
func (s *server) checkWAN(ctx context.Context) {
	states := map[string]bool{}

	out, err := exec.CommandContext(ctx, "mwan3", "status").Output()
	if err == nil && strings.Contains(string(out), "interface ") {
		for _, l := range strings.Split(string(out), "\n") {
			l = strings.TrimSpace(l)
			if !strings.HasPrefix(l, "interface ") {
				continue
			}
			f := strings.Fields(l)
			if len(f) < 4 {
				continue
			}
			states[f[1]] = f[3] == "online"
		}
	} else {
		var dump struct {
			Interface []struct {
				Interface string `json:"interface"`
				Up        bool   `json:"up"`
			} `json:"interface"`
		}
		if ubusCall(ctx, "network.interface", "dump", &dump) != nil {
			return
		}
		for _, i := range dump.Interface {
			if reWanName.MatchString(i.Interface) {
				states[i.Interface] = i.Up
			}
		}
	}

	for name, up := range states {
		val := "down"
		if up {
			val = "up"
		}
		changed, prev := s.alertValue("wan:"+name, val)
		if !changed {
			continue
		}
		if up {
			s.alert("wan", "info", fmt.Sprintf(
				"Internet veza '%s' je opet dostupna (prije: %s).", name, prev))
		} else {
			s.alert("wan", "warning", fmt.Sprintf(
				"Internet veza '%s' je pala.", name))
		}
	}
}

/* ---------- javna IP adresa i CGNAT ---------- */

// publicIP dohvaća javnu adresu, po želji kroz određeno sučelje (za mjerenje
// po pojedinoj WAN vezi kod multi-WAN-a).
func publicIP(ctx context.Context, iface string) string {
	for _, url := range pubIPServices {
		c, cancel := context.WithTimeout(ctx, 8*time.Second)
		args := []string{"-s", "--max-time", "6"}
		if iface != "" {
			args = append(args, "--interface", iface)
		}
		args = append(args, url)
		out, err := exec.CommandContext(c, "curl", args...).Output()
		cancel()
		if err != nil {
			continue
		}
		ip := strings.TrimSpace(string(out))
		if net.ParseIP(ip) != nil {
			return ip
		}
	}
	return ""
}

// isCGNAT javlja je li adresa iz operaterskog raspona 100.64.0.0/10. Uređaj s
// takvom adresom nema pravu javnu adresu — port forwardi i VPN izvana neće
// raditi, a to je inače teško dijagnosticirati.
func isCGNAT(ip string) bool {
	p := net.ParseIP(ip)
	if p == nil || p.To4() == nil {
		return false
	}
	_, cg, _ := net.ParseCIDR("100.64.0.0/10")
	return cg.Contains(p)
}

// wanLocalAddr vraća adresu koju uređaj ima na WAN sučelju.
func wanLocalAddr(ctx context.Context, iface string) string {
	var st struct {
		IPv4 []struct {
			Address string `json:"address"`
		} `json:"ipv4-address"`
	}
	if ubusCallArg(ctx, "network.interface."+iface, "status", "{}", &st) != nil {
		return ""
	}
	if len(st.IPv4) > 0 {
		return st.IPv4[0].Address
	}
	return ""
}

func (s *server) checkPublicIP(ctx context.Context) {
	ip := publicIP(ctx, "")
	if ip == "" {
		return // bez interneta se ne javlja promjena — to pokriva checkWAN
	}
	s.setSetting("public_ip", ip)
	if changed, prev := s.alertValue("pubip", ip); changed {
		s.alert("pubip", "info", fmt.Sprintf(
			"Javna IP adresa se promijenila: %s → %s.\n\n"+
				"Ako se na uređaj spaja izvana (VPN, objavljeni serveri), "+
				"provjeri DNS zapis ili uključi dinamički DNS.", prev, ip))
	}

	// CGNAT: adresa na WAN sučelju nije ista kao javna, ili je iz 100.64/10
	local := wanLocalAddr(ctx, "wan")
	cg := isCGNAT(local) || (local != "" && local != ip && !isPrivateIP(ip))
	state := "ne"
	if cg {
		state = "da"
	}
	if changed, _ := s.alertValue("cgnat", state); changed && cg {
		what := "iza još jednog routera koji radi NAT"
		fix := "Ako uređaj mora biti dostupan izvana, na tom routeru treba " +
			"proslijediti portove na ovaj uređaj — ili ga staviti prvog u lanac."
		if isCGNAT(local) {
			what = "iza operaterskog NAT-a (CGNAT)"
			fix = "Rješenje je zatražiti javnu IP adresu od operatera."
		}
		s.alert("cgnat", "warning", fmt.Sprintf(
			"Uređaj je %s.\n\n"+
				"Adresa na WAN sučelju: %s\nStvarna javna adresa: %s\n\n"+
				"To znači da uređaj nema vlastitu javnu adresu: objavljeni "+
				"serveri i spajanje na VPN izvana neće raditi.\n\n%s",
			what, local, ip, fix))
	}
}

// checkIPv6Prefix javlja promjenu prefiksa dobivenog od pružatelja. Kod IPv6
// nema NAT-a, pa promjena prefiksa mijenja adrese *svih* uređaja u mreži —
// pravila i DNS zapisi koji spominju stare adrese prestaju vrijediti.
func (s *server) checkIPv6Prefix(ctx context.Context) {
	_, prefix := v6Addresses(ctx)
	if prefix == "" {
		return // IPv6 nije uključen ili pružatelj nije dodijelio prefiks
	}
	s.setSetting("ipv6_prefix", prefix)
	if changed, prev := s.alertValue("pubip6", prefix); changed && prev != "" {
		s.alert("pubip", "info", fmt.Sprintf(
			"IPv6 prefiks od pružatelja se promijenio: %s → %s.\n\n"+
				"Kod IPv6 nema NAT-a, pa su se promijenile adrese svih uređaja u "+
				"mreži. Provjeri pravila vatrozida i DNS zapise koji spominju "+
				"stare adrese.", prev, prefix))
	}
}

// isPrivateIP javlja je li adresa iz privatnog raspona (RFC1918).
func isPrivateIP(ip string) bool {
	p := net.ParseIP(ip)
	return p != nil && p.IsPrivate()
}

/* ---------- servisi ---------- */

// checkServices pazi rade li VPN poslužitelji koji su konfigurirani.
func (s *server) checkServices(ctx context.Context) {
	type svc struct {
		id, label, iface string
		check            func() bool
	}
	checks := []svc{
		{"wireguard", "WireGuard", "sag_wg0", func() bool {
			out, err := exec.CommandContext(ctx, "wg", "show", "sag_wg0").Output()
			return err == nil && len(out) > 0
		}},
		{"openvpn", "OpenVPN", "sag_ovpn", func() bool {
			return exec.CommandContext(ctx, "pgrep", "openvpn").Run() == nil
		}},
	}
	netCfg, err := uciGetConfig(ctx, "network")
	if err != nil {
		return
	}
	for _, c := range checks {
		if _, configured := netCfg[c.iface]; !configured {
			continue // nije postavljen — nema se što nadzirati
		}
		val := "down"
		if c.check() {
			val = "up"
		}
		changed, _ := s.alertValue("svc:"+c.id, val)
		if !changed {
			continue
		}
		if val == "up" {
			s.alert("vpn_service", "info", c.label+" poslužitelj opet radi.")
		} else {
			s.alert("vpn_service", "warning",
				c.label+" poslužitelj je prestao raditi.")
		}
	}
}

/* ---------- resursi ---------- */

func (s *server) checkResources(ctx context.Context) {
	var info struct {
		Load   [3]int64 `json:"load"`
		Memory struct {
			Total     int64 `json:"total"`
			Available int64 `json:"available"`
		} `json:"memory"`
		Root struct {
			Total int64 `json:"total"`
			Free  int64 `json:"free"`
		} `json:"root"`
	}
	if ubusCall(ctx, "system", "info", &info) != nil {
		return
	}
	limit := func(key, def string) float64 {
		n, err := strconv.Atoi(s.getSetting(key, def))
		if err != nil || n <= 0 {
			n, _ = strconv.Atoi(def)
		}
		return float64(n)
	}

	// opterećenje procesora: load po jezgri, izraženo u postotku
	cores := float64(numCPU())
	loadPct := 100 * (float64(info.Load[0]) / 65536.0) / cores
	s.resourceAlarm("cpu", loadPct, limit("alert_cpu_pct", "90"),
		fmt.Sprintf("Opterećenje procesora je %.0f %% (prosjek zadnje minute, %d jezgri).",
			loadPct, int(cores)))

	if info.Memory.Total > 0 {
		memPct := 100 * float64(info.Memory.Total-info.Memory.Available) /
			float64(info.Memory.Total)
		s.resourceAlarm("mem", memPct, limit("alert_mem_pct", "90"),
			fmt.Sprintf("Zauzeće memorije je %.0f %% (slobodno %s).",
				memPct, humanKB(info.Memory.Available/1024)))
	}
	if info.Root.Total > 0 {
		diskPct := 100 * float64(info.Root.Total-info.Root.Free) /
			float64(info.Root.Total)
		s.resourceAlarm("disk", diskPct, limit("alert_disk_pct", "90"),
			fmt.Sprintf("Zauzeće diska je %.0f %% (slobodno %s).",
				diskPct, humanKB(info.Root.Free)))
	}
}

// resourceAlarm javlja prelazak praga i povratak ispod njega — ne svaku minutu
// dok traje opterećenje.
func (s *server) resourceAlarm(id string, value, limit float64, message string) {
	state := "ok"
	if value >= limit {
		state = "high"
	}
	changed, _ := s.alertValue("res:"+id, state)
	if !changed {
		return
	}
	if state == "high" {
		s.alert("resources", "warning", message)
	} else {
		s.alert("resources", "info", "Stanje se vratilo u normalu: "+message)
	}
}

func humanKB(kb int64) string {
	switch {
	case kb > 1024*1024:
		return fmt.Sprintf("%.1f GB", float64(kb)/(1024*1024))
	case kb > 1024:
		return fmt.Sprintf("%.0f MB", float64(kb)/1024)
	}
	return fmt.Sprintf("%d kB", kb)
}

func numCPU() int {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return 1
	}
	n := strings.Count(string(b), "processor")
	if n < 1 {
		n = 1
	}
	return n
}

/* ---------- VPN korisnici ---------- */

// checkVPNClients javlja spajanje i odspajanje korisnika. Za WireGuard se
// gleda vrijeme zadnjeg rukovanja (peer bez prometa dvije minute smatra se
// odspojenim), za OpenVPN popis u status datoteci.
func (s *server) checkVPNClients(ctx context.Context) {
	online := map[string]string{} // ključ -> opis

	out, err := exec.CommandContext(ctx, "wg", "show", "sag_wg0", "dump").Output()
	if err == nil {
		for i, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if i == 0 { // prvi redak je sučelje, ne peer
				continue
			}
			f := strings.Split(l, "\t")
			if len(f) < 5 {
				continue
			}
			hs, _ := strconv.ParseInt(f[4], 10, 64)
			if hs > 0 && time.Since(time.Unix(hs, 0)) < 3*time.Minute {
				var name string
				s.db.QueryRow(`SELECT name FROM wg_peers WHERE public_key=?`,
					f[0]).Scan(&name)
				if name == "" {
					name = f[0][:8] + "…"
				}
				online["wg:"+f[0]] = "WireGuard korisnik " + name
			}
		}
	}

	if b, err := os.ReadFile(ovpnStatusFile); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Split(l, ",")
			if len(f) > 2 && f[0] == "CLIENT_LIST" {
				online["ovpn:"+f[1]] = "OpenVPN korisnik " + f[1]
			}
		}
	}

	// usporedba s prethodnim krugom
	prev := map[string]bool{}
	for _, k := range strings.Fields(s.getSetting("vpn_online", "")) {
		prev[k] = true
	}
	keys := make([]string, 0, len(online))
	for k, desc := range online {
		keys = append(keys, k)
		if !prev[k] {
			s.alert("vpn_client", "info", desc+" se spojio.")
		}
	}
	for k := range prev {
		if _, still := online[k]; !still {
			s.alert("vpn_client", "info", vpnLabel(k)+" se odspojio.")
		}
	}
	s.setSetting("vpn_online", strings.Join(keys, " "))
}

func vpnLabel(key string) string {
	if name, ok := strings.CutPrefix(key, "ovpn:"); ok {
		return "OpenVPN korisnik " + name
	}
	if k, ok := strings.CutPrefix(key, "wg:"); ok && len(k) > 8 {
		return "WireGuard korisnik " + k[:8] + "…"
	}
	return "VPN korisnik"
}

/* ---------- neuspjele prijave ---------- */

// checkFailedLogins broji neuspjele SSH i LuCI prijave u sistemskom logu.
// banIP takve izvore i banira, ali o tome nitko ne javlja — ovo je taj dio.
func (s *server) checkFailedLogins(ctx context.Context) {
	out, err := exec.CommandContext(ctx, "logread").Output()
	if err != nil {
		return
	}
	n := 0
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, "Bad password attempt") ||
			strings.Contains(l, "Exit before auth") ||
			strings.Contains(l, "luci: failed login") ||
			strings.Contains(l, "saguaro: failed login") {
			n++
		}
	}
	// log je kružni spremnik, pa broj može i pasti — javlja se samo porast
	prevN, _ := strconv.Atoi(s.getSetting("failed_logins", "0"))
	s.setSetting("failed_logins", strconv.Itoa(n))
	if prevN > 0 && n-prevN >= 10 {
		s.alert("login_failed", "warning", fmt.Sprintf(
			"Zabilježeno je %d novih neuspjelih pokušaja prijave u zadnjoj minuti.\n\n"+
				"banIP automatski banira takve izvore, ali provjeri je li "+
				"upravljanje uopće dostupno s interneta.", n-prevN))
	}
}

// checkScanners javlja kad detekcija skeniranja blokira nove izvore. Pravila
// u nftablesu rade i bez ovoga (drop + log u jezgru), ali dok se popis nije
// čitao, u sučelju i izvještaju nije bilo ni traga da se išta dogodilo.
// Javlja se samo porast broja blokiranih (set se sam prazni istekom).
func (s *server) checkScanners(ctx context.Context) {
	if s.getSetting("scan_enabled", "0") != "1" {
		return
	}
	out, err := exec.CommandContext(ctx, "nft", "list", "set", "inet", "fw4",
		scanSetName).Output()
	if err != nil {
		return
	}
	n := 0
	if i := strings.Index(string(out), "elements = {"); i >= 0 {
		n = len(reScanElem.FindAllString(string(out)[i:], -1))
	}
	prev, _ := strconv.Atoi(s.getSetting("scan_blocked_n", "0"))
	s.setSetting("scan_blocked_n", strconv.Itoa(n))
	if n > prev {
		s.alert("scan", "warning", fmt.Sprintf(
			"Detekcija skeniranja blokirala je nove izvore — trenutno blokiranih: %d.\n\n"+
				"Izvori se sami otpuštaju istekom vremena. Popis je u modulu "+
				"Scan detection.", n))
	}
}

/* ---------- istek certifikata ---------- */

func (s *server) checkCerts() {
	days, err := strconv.Atoi(s.getSetting("alert_cert_days", "30"))
	if err != nil || days < 1 {
		days = 30
	}
	files := map[string]string{
		"OpenVPN CA":               s.ovpnDir() + "/ca.crt",
		"OpenVPN poslužitelj":      s.ovpnDir() + "/server.crt",
		"Certifikat sučelja (TLS)": s.etcDir + "/cert.pem",
	}
	for label, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		blk, _ := pem.Decode(b)
		if blk == nil {
			continue
		}
		crt, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			continue
		}
		left := int(time.Until(crt.NotAfter).Hours() / 24)
		if left <= days {
			s.alert("cert", "warning", fmt.Sprintf(
				"%s istječe za %d dana (%s).", label, left,
				crt.NotAfter.Format("02.01.2006.")))
		}
	}
}

/* ---------- ručna provjera iz sučelja ---------- */

// handleWatchdogRun pokreće sve provjere odmah — za gumb "Provjeri sada",
// da korisnik ne mora čekati sljedeći krug.
func (s *server) handleWatchdogRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s.checkWAN(ctx)
	s.checkServices(ctx)
	s.checkResources(ctx)
	s.checkVPNClients(ctx)
	s.checkPublicIP(ctx)
	s.checkIPv6Prefix(ctx)
	s.checkCerts()
	writeJSON(w, http.StatusOK, map[string]any{
		"checked":   true,
		"public_ip": s.getSetting("public_ip", ""),
	})
}
