package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Obrnuti proxy (reverse proxy).
//
// Iza jedne javne adrese može stajati više web servisa; port 443 je samo
// jedan, pa port forward tu više ne pomaže. Proxy sluša na 80 i 443, gleda
// koje je ime posjetitelj tražio i prosljeđuje ga pravom internom serveru.
//
// Za HTTPS se koristi prosljeđivanje bez otvaranja veze (SNI passthrough):
// certifikati ostaju na internim serverima, uređaj ne vidi sadržaj i ne treba
// mu nijedan ključ. HTTP stranice se usmjeravaju po Host zaglavlju.

const haproxyCfg = "/etc/haproxy.cfg"
const haproxyMarker = "# Saguaro reverse proxy"
const rpFwPrefix = "sag_rp_"

// Proxy ne sjeda na portove 80 i 443 — njih na uređaju drži LuCI. Umjesto
// premještanja upravljanja, proxy sluša na vlastitim portovima, a vatrozid
// promet s interneta preusmjeri na njih. LuCI ostaje netaknut na LAN-u.
const rpHTTPPort = 8080
const rpHTTPSPort = 8444

var reHostname = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)+$`)

type RPSite struct {
	UUID     string `json:"uuid"`
	Hostname string `json:"hostname"`
	Proto    string `json:"proto"` // https (SNI) | http
	DestIP   string `json:"dest_ip"`
	DestPort int    `json:"dest_port"`
	Enabled  bool   `json:"enabled"`
	Notes    string `json:"notes"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

const rpCols = `uuid, hostname, proto, dest_ip, dest_port, enabled,
	COALESCE(notes,''), created_at, updated_at`

func scanRPSite(row interface{ Scan(...any) error }) (RPSite, error) {
	var s RPSite
	err := row.Scan(&s.UUID, &s.Hostname, &s.Proto, &s.DestIP, &s.DestPort,
		&s.Enabled, &s.Notes, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

type rpSiteIn struct {
	RPSite
	Enabled *bool `json:"enabled"`
}

func (s *server) rpSites() ([]RPSite, error) {
	rows, err := s.db.Query(`SELECT ` + rpCols + ` FROM rp_sites ORDER BY hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RPSite{}
	for rows.Next() {
		st, err := scanRPSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func haproxyInstalled() bool {
	_, err := exec.LookPath("haproxy")
	return err == nil
}

func haproxyRunning(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "pgrep", "-f", "/usr/sbin/haproxy").Output()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

func (s *server) handleProxyGet(w http.ResponseWriter, r *http.Request) {
	sites, err := s.rpSites()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	owned := false
	if b, err := os.ReadFile(haproxyCfg); err == nil {
		owned = strings.HasPrefix(string(b), haproxyMarker)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sites":          sites,
		"installed":      haproxyInstalled(),
		"running":        haproxyRunning(r.Context()),
		"config_managed": owned,
		"http_port":      rpHTTPPort,
		"https_port":     rpHTTPSPort,
		"wan_ips":        s.wanIPv4Addrs(r.Context()),
	})
}

func (s *server) handleProxyInstall(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !haproxyInstalled() {
		_ = exec.CommandContext(ctx, "apk", "update").Run()
		out, err := exec.CommandContext(ctx, "apk", "add", "haproxy").CombinedOutput()
		if err != nil {
			writeErr(w, http.StatusInternalServerError,
				"instalacija: "+err.Error()+": "+string(out))
			return
		}
	}
	// Paket dolazi s primjerom konfiguracije i sam se pokrene — s njime
	// HAProxy otvara portove 81, 444 i 60000 na svim adresama. Na uređaju koji
	// je firewall to ne smije ostati, pa se servis odmah gasi, a primjer
	// zamjenjuje našom (praznom) konfiguracijom. Servis kreće tek kad ga
	// korisnik konfigurira i primijeni.
	replaced := false
	if b, err := os.ReadFile(haproxyCfg); err != nil ||
		!strings.HasPrefix(string(b), haproxyMarker) {
		_ = exec.CommandContext(ctx, "/etc/init.d/haproxy", "stop").Run()
		_ = exec.CommandContext(ctx, "/etc/init.d/haproxy", "disable").Run()
		if err == nil {
			_, _ = s.backupConfig(haproxyCfg)
		}
		if err := os.WriteFile(haproxyCfg,
			[]byte(buildHaproxyConfig(nil)), 0o600); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		replaced = true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installed":        haproxyInstalled(),
		"example_replaced": replaced,
		"running":          haproxyRunning(ctx),
	})
}

func validateRPSite(w http.ResponseWriter, st *RPSite) bool {
	st.Hostname = strings.ToLower(strings.TrimSpace(st.Hostname))
	st.DestIP = strings.TrimSpace(st.DestIP)
	st.Proto = strings.TrimSpace(st.Proto)
	if st.Proto == "" {
		st.Proto = "https"
	}
	if st.DestPort == 0 {
		if st.Proto == "http" {
			st.DestPort = 80
		} else {
			st.DestPort = 443
		}
	}
	switch {
	case !reHostname.MatchString(st.Hostname):
		writeErr(w, http.StatusBadRequest,
			"ime mora biti puno ime domene, npr. mail.tvrtka.hr")
	case st.Proto != "https" && st.Proto != "http":
		writeErr(w, http.StatusBadRequest, "vrsta mora biti https ili http")
	case net.ParseIP(st.DestIP) == nil:
		writeErr(w, http.StatusBadRequest, "neispravna adresa internog servera")
	case st.DestPort < 1 || st.DestPort > 65535:
		writeErr(w, http.StatusBadRequest, "neispravan port internog servera")
	case hasCtrl(st.Notes):
		writeErr(w, http.StatusBadRequest, "napomena ne smije sadržavati prijelom retka")
	default:
		return true
	}
	return false
}

func (s *server) handleProxySiteCreate(w http.ResponseWriter, r *http.Request) {
	var in rpSiteIn
	if !decodeBody(w, r, &in) {
		return
	}
	st := &in.RPSite
	if !validateRPSite(w, st) {
		return
	}
	st.UUID = newUUID()
	_, err := s.db.Exec(`INSERT INTO rp_sites
		(uuid, hostname, proto, dest_ip, dest_port, enabled, notes)
		VALUES (?,?,?,?,?,?,?)`,
		st.UUID, st.Hostname, st.Proto, st.DestIP, st.DestPort,
		enabledIntOf(in.Enabled), st.Notes)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "to ime već postoji u popisu")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ss, _ := scanRPSite(s.db.QueryRow(`SELECT `+rpCols+` FROM rp_sites WHERE uuid=?`, st.UUID))
	writeJSON(w, http.StatusCreated, ss)
}

func (s *server) handleProxySiteUpdate(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	var in rpSiteIn
	if !decodeBody(w, r, &in) {
		return
	}
	st := &in.RPSite
	if !validateRPSite(w, st) {
		return
	}
	res, err := s.db.Exec(`UPDATE rp_sites SET hostname=?, proto=?, dest_ip=?,
		dest_port=?, enabled=?, notes=?, updated_at=datetime('now') WHERE uuid=?`,
		st.Hostname, st.Proto, st.DestIP, st.DestPort, enabledIntOf(in.Enabled),
		st.Notes, uuid)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "stranica ne postoji")
		return
	}
	ss, _ := scanRPSite(s.db.QueryRow(`SELECT `+rpCols+` FROM rp_sites WHERE uuid=?`, uuid))
	writeJSON(w, http.StatusOK, ss)
}

func (s *server) handleProxySiteDelete(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.Exec(`DELETE FROM rp_sites WHERE uuid=?`, r.PathValue("uuid"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "stranica ne postoji")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

/* ---------- generiranje konfiguracije ---------- */

func rpID(uuid string) string { return strings.ReplaceAll(uuid, "-", "")[:8] }

// buildHaproxyConfig sastavlja konfiguraciju iz popisa stranica.
func buildHaproxyConfig(sites []RPSite) string {
	var b strings.Builder
	b.WriteString(haproxyMarker + " — ovu datoteku piše Saguaro.\n")
	b.WriteString("# Ručne izmjene se gube pri sljedećoj primjeni iz sučelja.\n\n")
	b.WriteString("global\n\tlog /dev/log local0 notice\n\tmaxconn 4096\n")
	b.WriteString("\tuser nobody\n\tgroup nogroup\n\n")
	b.WriteString("defaults\n\tlog global\n\ttimeout connect 5s\n")
	b.WriteString("\ttimeout client 60s\n\ttimeout server 60s\n\n")

	var https, plain []RPSite
	for _, st := range sites {
		if !st.Enabled {
			continue
		}
		if st.Proto == "http" {
			plain = append(plain, st)
		} else {
			https = append(https, st)
		}
	}

	if len(https) > 0 {
		// SNI prosljeđivanje: veza se ne otvara, certifikat ostaje na serveru
		fmt.Fprintf(&b, "frontend sag_fe_https\n\tbind :%d\n\tmode tcp\n", rpHTTPSPort)
		b.WriteString("\toption tcplog\n\ttcp-request inspect-delay 5s\n")
		b.WriteString("\ttcp-request content accept if { req.ssl_hello_type 1 }\n")
		for _, st := range https {
			fmt.Fprintf(&b, "\tuse_backend sag_be_%s if { req.ssl_sni -i %s }\n",
				rpID(st.UUID), st.Hostname)
		}
		b.WriteString("\n")
	}

	// Port 80 (preusmjeren s vatrozida): poznata http imena idu svom serveru,
	// sve ostalo na https
	fmt.Fprintf(&b, "frontend sag_fe_http\n\tbind :%d\n\tmode http\n\toption httplog\n",
		rpHTTPPort)
	for _, st := range plain {
		fmt.Fprintf(&b, "\tuse_backend sag_beh_%s if { hdr(host) -i %s }\n",
			rpID(st.UUID), st.Hostname)
	}
	if len(https) > 0 {
		b.WriteString("\thttp-request redirect scheme https code 301\n")
	}
	b.WriteString("\n")

	for _, st := range https {
		fmt.Fprintf(&b, "backend sag_be_%s\n\tmode tcp\n\tserver srv %s:%d\n\n",
			rpID(st.UUID), st.DestIP, st.DestPort)
	}
	for _, st := range plain {
		fmt.Fprintf(&b, "backend sag_beh_%s\n\tmode http\n\tserver srv %s:%d\n\n",
			rpID(st.UUID), st.DestIP, st.DestPort)
	}
	return b.String()
}

// rpFirewall otvara ili zatvara portove 80 i 443 prema samom uređaju.
// Pravila nose vlastiti prefiks, pa ih primjena firewalla ne dira.
func rpFirewall(ctx context.Context, open bool) error {
	cfg, err := uciGetConfig(ctx, "firewall")
	if err != nil {
		return err
	}
	var b strings.Builder
	for name := range cfg {
		if strings.HasPrefix(name, rpFwPrefix) {
			fmt.Fprintf(&b, "delete firewall.%s\n", name)
		}
	}
	if open {
		// promet s interneta na 80/443 preusmjeri se na portove proxyja,
		// a ulaz na te portove se propusti
		for _, p := range []struct{ pub, local int }{
			{80, rpHTTPPort}, {443, rpHTTPSPort},
		} {
			sn := fmt.Sprintf("%sr%d", rpFwPrefix, p.pub)
			fmt.Fprintf(&b, "set firewall.%s=redirect\n", sn)
			fmt.Fprintf(&b, "set firewall.%s.name=%s\n", sn,
				uciQuote(fmt.Sprintf("Obrnuti proxy %d", p.pub)))
			fmt.Fprintf(&b, "set firewall.%s.src=wan\n", sn)
			fmt.Fprintf(&b, "set firewall.%s.proto=tcp\n", sn)
			fmt.Fprintf(&b, "set firewall.%s.src_dport=%d\n", sn, p.pub)
			fmt.Fprintf(&b, "set firewall.%s.dest_port=%d\n", sn, p.local)
			fmt.Fprintf(&b, "set firewall.%s.target=DNAT\n", sn)

			an := fmt.Sprintf("%sa%d", rpFwPrefix, p.local)
			fmt.Fprintf(&b, "set firewall.%s=rule\n", an)
			fmt.Fprintf(&b, "set firewall.%s.name=%s\n", an,
				uciQuote(fmt.Sprintf("Obrnuti proxy ulaz %d", p.local)))
			fmt.Fprintf(&b, "set firewall.%s.src=wan\n", an)
			fmt.Fprintf(&b, "set firewall.%s.proto=tcp\n", an)
			fmt.Fprintf(&b, "set firewall.%s.dest_port=%d\n", an, p.local)
			fmt.Fprintf(&b, "set firewall.%s.target=ACCEPT\n", an)
		}
	}
	if b.Len() == 0 {
		return nil
	}
	b.WriteString("commit firewall\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		return err
	}
	return serviceReload(ctx, "firewall", "reload")
}

func (s *server) handleProxyApply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !haproxyInstalled() {
		writeErr(w, http.StatusConflict, "HAProxy nije instaliran")
		return
	}
	sites, err := s.rpSites()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	active := 0
	for _, st := range sites {
		if st.Enabled {
			active++
		}
	}
	cfg := buildHaproxyConfig(sites)
	// provjera konfiguracije prije nego zamijeni onu koja radi
	tmp := "/tmp/saguaro-haproxy-check.cfg"
	if err := os.WriteFile(tmp, []byte(cfg), 0o600); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out, err := exec.CommandContext(ctx, "haproxy", "-c", "-f", tmp).CombinedOutput()
	os.Remove(tmp)
	if err != nil {
		writeErr(w, http.StatusConflict,
			"HAProxy je odbio konfiguraciju: "+strings.TrimSpace(string(out)))
		return
	}

	backup := ""
	if _, err := os.Stat(haproxyCfg); err == nil {
		if backup, err = s.backupConfig(haproxyCfg); err != nil {
			writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
			return
		}
	}
	if err := os.WriteFile(haproxyCfg, []byte(cfg), 0o600); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if active == 0 {
		// bez stranica proxy nema što raditi — servis i pravila se gase
		_ = exec.CommandContext(ctx, "/etc/init.d/haproxy", "stop").Run()
		_ = exec.CommandContext(ctx, "/etc/init.d/haproxy", "disable").Run()
		_ = rpFirewall(ctx, false)
		writeJSON(w, http.StatusOK, map[string]any{
			"sites": 0, "running": false, "backup": backup,
		})
		return
	}
	if err := exec.CommandContext(ctx, "/etc/init.d/haproxy", "enable").Run(); err != nil {
		writeErr(w, http.StatusInternalServerError, "enable: "+err.Error())
		return
	}
	if err := exec.CommandContext(ctx, "/etc/init.d/haproxy", "restart").Run(); err != nil {
		writeErr(w, http.StatusInternalServerError, "restart: "+err.Error())
		return
	}
	if err := rpFirewall(ctx, true); err != nil {
		writeErr(w, http.StatusInternalServerError, "firewall: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sites": active, "running": haproxyRunning(ctx), "backup": backup,
	})
}

// handleProxyConfig vraća generiranu konfiguraciju na uvid — bez SSH-a se
// inače ne može provjeriti što je uređaj zapravo dobio.
func (s *server) handleProxyConfig(w http.ResponseWriter, r *http.Request) {
	sites, err := s.rpSites()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	live := ""
	if b, err := os.ReadFile(haproxyCfg); err == nil {
		live = string(b)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"generated": buildHaproxyConfig(sites),
		"live":      live,
		"same":      strconv.FormatBool(live == buildHaproxyConfig(sites)),
	})
}
