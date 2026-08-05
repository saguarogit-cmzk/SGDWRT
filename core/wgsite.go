package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Veza ured–ured (site-to-site): dvije poslovnice se ponašaju kao jedna mreža,
// bez ijednog programa na računalima. Razlika prema modulu WireGuard (udaljeni
// pristup) nije kozmetička, pa ovo ima **vlastito sučelje i vlastitu zonu**:
//
//   - kod udaljenog pristupa iza peera stoji jedan čovjek s jednom adresom u
//     tunelu; ovdje iza peera stoji **cijela mreža** druge poslovnice
//   - promet ide u **oba smjera** — i naši do njih i oni do nas — dok je kod
//     udaljenog pristupa smjer samo jedan
//
// Kad bi oboje dijelilo isto sučelje, prekidač „ograničeni pristup" na jednoj
// strani tiho bi mijenjao pravila drugoj. Zato zasebno.
const wgsIface = "sag_wgs0"
const wgsZone = "sagwgs"
const wgsPrefix = "sag_wgs"
const wgsDefaultPort = 51821

/* ---------- model ---------- */

type WGSite struct {
	UUID       string `json:"uuid"`
	Name       string `json:"name"`
	PublicKey  string `json:"public_key"`
	TunnelIP   string `json:"tunnel_ip"`
	Subnets    string `json:"subnets"`  // mreže iza druge strane, odvojene zarezom
	Endpoint   string `json:"endpoint"` // ime ili IP druge strane, po želji s :portom
	Keepalive  int64  `json:"keepalive"`
	Enabled    bool   `json:"enabled"`
	Notes      string `json:"notes"`
	HasPrivate bool   `json:"has_private"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

const wgSiteCols = `uuid, name, public_key, tunnel_ip, subnets,
	COALESCE(endpoint,''), COALESCE(keepalive,0), enabled, COALESCE(notes,''),
	private_key IS NOT NULL, created_at, updated_at`

func scanWGSite(row interface{ Scan(...any) error }) (WGSite, error) {
	var x WGSite
	err := row.Scan(&x.UUID, &x.Name, &x.PublicKey, &x.TunnelIP, &x.Subnets,
		&x.Endpoint, &x.Keepalive, &x.Enabled, &x.Notes, &x.HasPrivate,
		&x.CreatedAt, &x.UpdatedAt)
	return x, err
}

/* ---------- mreže ---------- */

// localIPv4Nets vraća mreže koje uređaj već poslužuje (LAN i segmenti), da se
// može odbiti veza s poslovnicom koja koristi isti raspon adresa.
func localIPv4Nets(ctx context.Context) []*net.IPNet {
	cfg, err := uciGetConfig(ctx, "network")
	if err != nil {
		return nil
	}
	out := []*net.IPNet{}
	for name, sec := range cfg {
		if name == "loopback" || sectStr(sec, ".type") != "interface" {
			continue
		}
		ipStr, maskStr := sectStr(sec, "ipaddr"), sectStr(sec, "netmask")
		if ipStr == "" || maskStr == "" {
			continue
		}
		ip, mask := net.ParseIP(ipStr), net.ParseIP(maskStr)
		if ip == nil || ip.To4() == nil || mask == nil || mask.To4() == nil {
			continue
		}
		m := net.IPMask(mask.To4())
		out = append(out, &net.IPNet{IP: ip.Mask(m), Mask: m})
	}
	return out
}

// routedIPv4Nets vraća mreže do kojih uređaj već ima rutu, bez zadane rute i
// bez ruta našeg vlastitog sučelja. Provjera po uci zapisima nije dovoljna:
// tuneli (OpenVPN, WireGuard za udaljeni pristup) svoje mreže ne drže u
// network configu, pa bi se veza ured–ured tiho posvađala s njima — nađeno
// upravo tako, mreža tunela 10.7.0.0/24 već je bila zauzeta OpenVPN-om.
type routedNet struct {
	net *net.IPNet
	dev string
}

func routedIPv4Nets(exceptDev string) []routedNet {
	out := []routedNet{}
	for _, r := range kernelRoutes() {
		if r.Family != "ipv4" || r.Device == exceptDev || r.Device == "lo" {
			continue
		}
		_, n, err := net.ParseCIDR(r.Target)
		if err != nil || n == nil {
			continue
		}
		if ones, _ := n.Mask.Size(); ones == 0 {
			continue // zadana ruta pokriva sve i nije sukob
		}
		out = append(out, routedNet{net: n, dev: r.Device})
	}
	return out
}

// netsOverlap kaže preklapaju li se dvije mreže u bilo kojem dijelu — dovoljno
// je da jedna sadrži početnu adresu druge.
func netsOverlap(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
}

// parseSubnetList prima popis mreža odvojen zarezima i vraća ih normalizirane
// na mrežnu adresu (10.0.0.5/24 postaje 10.0.0.0/24 — inače bi ista mreža
// upisana na dva načina prošla kao dvije različite).
func parseSubnetList(s string) ([]*net.IPNet, error) {
	out := []*net.IPNet{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		ip, n, err := net.ParseCIDR(part)
		if err != nil || ip.To4() == nil {
			return nil, fmt.Errorf("neispravna mreža %q — očekuje se npr. 192.168.60.0/24", part)
		}
		ones, _ := n.Mask.Size()
		if ones == 0 {
			return nil, fmt.Errorf("mreža 0.0.0.0/0 nije dopuštena — kroz tunel bi otišao sav promet, uključivo internet")
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("upiši bar jednu mrežu druge poslovnice")
	}
	return out, nil
}

func netListString(nets []*net.IPNet) string {
	parts := make([]string, 0, len(nets))
	for _, n := range nets {
		parts = append(parts, n.String())
	}
	return strings.Join(parts, ", ")
}

/* ---------- postavke sučelja ---------- */

func (s *server) wgsNet() *net.IPNet {
	cfg, err := uciGetConfig(context.Background(), "network")
	if err != nil {
		return nil
	}
	sec, ok := cfg[wgsIface]
	if !ok {
		return nil
	}
	for _, a := range uciList(sec, "addresses") {
		if _, n, err := net.ParseCIDR(a); err == nil && n.IP.To4() != nil {
			return n
		}
	}
	return nil
}

// uciList čita opciju koja može biti zapisana kao jedna vrijednost ili kao
// popis — uci oba oblika vraća drugačije, pa se to rješava na jednom mjestu.
func uciList(sec uciSection, key string) []string {
	switch v := sec[key].(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []any:
		out := []string{}
		for _, a := range v {
			if str, ok := a.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

func (s *server) handleWGSStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := uciGetConfig(r.Context(), "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	_, lookErr := exec.LookPath("wg")

	local := map[string]any{"configured": false}
	if sec, ok := cfg[wgsIface]; ok {
		pub := ""
		if pk := sectStr(sec, "private_key"); pk != "" {
			pub, _ = wgPubFromPriv(pk)
		}
		nextIP := ""
		if n := s.wgsNet(); n != nil {
			nextIP = nextFreeTunnelIP(n, s.usedTunnelIPs("wg_sites"))
		}
		local = map[string]any{
			"configured":     true,
			"listen_port":    sectStr(sec, "listen_port"),
			"addresses":      uciList(sec, "addresses"),
			"public_key":     pub,
			"next_tunnel_ip": nextIP,
			"endpoint_host":  s.getSetting("wgs_endpoint_host", ""),
			"allow_mgmt":     s.getSetting("wgs_allow_mgmt", "0") == "1",
		}
	}
	nets := []string{}
	for _, n := range localIPv4Nets(r.Context()) {
		nets = append(nets, n.String())
	}
	local["local_subnets"] = nets

	stats, running := wgsDump(r.Context())
	if stats == nil {
		stats = map[string]wgPeerStats{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installed": lookErr == nil,
		"local":     local,
		"running":   running,
		"stats":     stats,
		"now":       time.Now().Unix(),
	})
}

func wgsDump(ctx context.Context) (map[string]wgPeerStats, bool) {
	out, err := exec.CommandContext(ctx, "wg", "show", wgsIface, "dump").Output()
	if err != nil {
		return nil, false
	}
	stats := map[string]wgPeerStats{}
	for i, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if i == 0 || len(f) < 8 {
			continue
		}
		hs, _ := strconv.ParseInt(f[4], 10, 64)
		rx, _ := strconv.ParseInt(f[5], 10, 64)
		tx, _ := strconv.ParseInt(f[6], 10, 64)
		ep := f[2]
		if ep == "(none)" {
			ep = ""
		}
		stats[f[0]] = wgPeerStats{Endpoint: ep, LatestHandshake: hs, RxBytes: rx, TxBytes: tx}
	}
	return stats, true
}

type wgsLocalIn struct {
	ListenPort   int    `json:"listen_port"`
	Address      string `json:"address"` // naša adresa u tunelu, CIDR
	EndpointHost string `json:"endpoint_host"`
	AllowMgmt    bool   `json:"allow_mgmt"`
}

func (s *server) handleWGSLocalSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in wgsLocalIn
	if !decodeBody(w, r, &in) {
		return
	}
	if in.ListenPort == 0 {
		in.ListenPort = wgsDefaultPort
	}
	if in.ListenPort < 1 || in.ListenPort > 65535 {
		writeErr(w, http.StatusBadRequest, "neispravan port")
		return
	}
	ip, ipnet, err := net.ParseCIDR(strings.TrimSpace(in.Address))
	if err != nil || ip.To4() == nil {
		writeErr(w, http.StatusBadRequest,
			"adresa tunela mora biti IPv4 CIDR, npr. 10.7.0.1/24")
		return
	}
	// mreža tunela ne smije se preklopiti s vlastitim mrežama — inače uređaj
	// ne bi znao kamo poslati promet za te adrese
	for _, ln := range localIPv4Nets(ctx) {
		if netsOverlap(ln, ipnet) {
			writeErr(w, http.StatusBadRequest, "mreža tunela "+ipnet.String()+
				" preklapa se s vlastitom mrežom "+ln.String()+" — uzmi raspon koji nigdje nije u upotrebi")
			return
		}
	}
	for _, rn := range routedIPv4Nets(wgsIface) {
		if netsOverlap(rn.net, ipnet) {
			writeErr(w, http.StatusBadRequest, "mreža tunela "+ipnet.String()+
				" preklapa se s mrežom "+rn.net.String()+" koju uređaj već koristi ("+
				rn.dev+") — uzmi raspon koji nigdje nije u upotrebi")
			return
		}
	}
	// isti port kao udaljeni pristup ne može stajati, oba slušaju UDP
	if netCfg, err := uciGetConfig(ctx, "network"); err == nil {
		if sectStr(netCfg[wgIface], "listen_port") == strconv.Itoa(in.ListenPort) {
			writeErr(w, http.StatusBadRequest, "port "+strconv.Itoa(in.ListenPort)+
				" već koristi WireGuard za udaljeni pristup — uzmi drugi")
			return
		}
	}
	in.EndpointHost = strings.ToLower(strings.TrimSpace(in.EndpointHost))
	if in.EndpointHost != "" && net.ParseIP(in.EndpointHost) == nil &&
		!validDNSName(in.EndpointHost) {
		writeErr(w, http.StatusBadRequest, "neispravna javna adresa (IP ili ime)")
		return
	}

	netBackup, err := s.backupConfig(networkConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}
	fwBackup, err := s.backupConfig(firewallConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}

	cfg, err := uciGetConfig(ctx, "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	priv := sectStr(cfg[wgsIface], "private_key")
	if priv == "" {
		var genErr error
		priv, _, genErr = wgGenKey()
		if genErr != nil {
			writeErr(w, http.StatusInternalServerError, genErr.Error())
			return
		}
	}
	pub, err := wgPubFromPriv(priv)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	mask := strings.Split(ipnet.String(), "/")[1]
	wantAddr := ip.String() + "/" + mask

	// Podizanje sučelja prekine veze s poslovnicama dok se tuneli ponovno ne
	// dogovore — do pola minute. Zato se sučelje dira samo kad se mreža
	// stvarno promijenila; kvačica za pristup upravljanju mijenja jedino
	// firewall i ne smije rušiti vezu (nađeno mjerenjem na uređaju).
	netChanged := true
	if sec, ok := cfg[wgsIface]; ok {
		addrs := uciList(sec, "addresses")
		netChanged = sectStr(sec, "listen_port") != strconv.Itoa(in.ListenPort) ||
			len(addrs) != 1 || addrs[0] != wantAddr ||
			sectStr(sec, "private_key") != priv
	}

	if netChanged {
		var nb strings.Builder
		fmt.Fprintf(&nb, "set network.%s=interface\n", wgsIface)
		fmt.Fprintf(&nb, "set network.%s.proto=wireguard\n", wgsIface)
		fmt.Fprintf(&nb, "set network.%s.private_key=%s\n", wgsIface, priv)
		fmt.Fprintf(&nb, "set network.%s.listen_port=%d\n", wgsIface, in.ListenPort)
		if _, ok := cfg[wgsIface]["addresses"]; ok {
			fmt.Fprintf(&nb, "delete network.%s.addresses\n", wgsIface)
		}
		fmt.Fprintf(&nb, "add_list network.%s.addresses=%s\n", wgsIface, wantAddr)
		nb.WriteString("commit network\n")
		if err := uciBatch(ctx, nb.String()); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	fwCfg, _ := uciGetConfig(ctx, "firewall")
	var fb strings.Builder
	fmt.Fprintf(&fb, "set firewall.%s_rule=rule\n", wgsPrefix)
	fmt.Fprintf(&fb, "set firewall.%s_rule.name=%s\n", wgsPrefix, uciQuote("Saguaro ured-ured"))
	fmt.Fprintf(&fb, "set firewall.%s_rule.src=wan\n", wgsPrefix)
	fmt.Fprintf(&fb, "set firewall.%s_rule.proto=udp\n", wgsPrefix)
	fmt.Fprintf(&fb, "set firewall.%s_rule.dest_port=%d\n", wgsPrefix, in.ListenPort)
	fmt.Fprintf(&fb, "set firewall.%s_rule.target=ACCEPT\n", wgsPrefix)
	fmt.Fprintf(&fb, "set firewall.%s_zone=zone\n", wgsPrefix)
	fmt.Fprintf(&fb, "set firewall.%s_zone.name=%s\n", wgsPrefix, wgsZone)
	fmt.Fprintf(&fb, "delete firewall.%s_zone.network\n", wgsPrefix)
	fmt.Fprintf(&fb, "add_list firewall.%s_zone.network=%s\n", wgsPrefix, wgsIface)
	fmt.Fprintf(&fb, "set firewall.%s_zone.output=ACCEPT\n", wgsPrefix)
	fmt.Fprintf(&fb, "set firewall.%s_zone.forward=REJECT\n", wgsPrefix)
	vpnZoneInput(&fb, fwCfg, wgsPrefix, wgsZone, "Ured-ured", in.AllowMgmt)
	// promet ide na obje strane — to je cijela poanta veze ured–ured
	fmt.Fprintf(&fb, "set firewall.%s_to_lan=forwarding\n", wgsPrefix)
	fmt.Fprintf(&fb, "set firewall.%s_to_lan.src=%s\n", wgsPrefix, wgsZone)
	fmt.Fprintf(&fb, "set firewall.%s_to_lan.dest=lan\n", wgsPrefix)
	fmt.Fprintf(&fb, "set firewall.%s_from_lan=forwarding\n", wgsPrefix)
	fmt.Fprintf(&fb, "set firewall.%s_from_lan.src=lan\n", wgsPrefix)
	fmt.Fprintf(&fb, "set firewall.%s_from_lan.dest=%s\n", wgsPrefix, wgsZone)
	fb.WriteString("commit firewall\n")
	if err := uciBatch(ctx, fb.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	for k, v := range map[string]string{
		"wgs_endpoint_host": in.EndpointHost,
		"wgs_allow_mgmt":    boolSetting(in.AllowMgmt),
	} {
		if err := s.setSetting(k, v); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	if netChanged {
		if err := serviceReload(ctx, "network", "reload"); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := serviceReload(ctx, "firewall", "reload"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if netChanged {
		if err := exec.CommandContext(ctx, "ifup", wgsIface).Run(); err != nil {
			writeErr(w, http.StatusInternalServerError, "ifup: "+err.Error())
			return
		}
	}
	addEvent(s, "info", "Postavke veze ured-ured spremljene")
	writeJSON(w, http.StatusOK, map[string]any{
		"applied": true, "public_key": pub, "restarted": netChanged,
		"backups": []string{netBackup, fwBackup},
	})
}

/* ---------- poslovnice ---------- */

func (s *server) handleWGSiteList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT ` + wgSiteCols + ` FROM wg_sites ORDER BY name`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	out := []WGSite{}
	for rows.Next() {
		x, err := scanWGSite(rows)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sites": out})
}

type wgSiteIn struct {
	WGSite
	Enabled *bool `json:"enabled"`
}

// splitEndpoint razdvaja "ime:port" na dijelove; port je neobavezan.
func splitEndpoint(ep string) (host string, port int, err error) {
	ep = strings.TrimSpace(ep)
	if ep == "" {
		return "", 0, nil
	}
	host = ep
	if h, p, e := net.SplitHostPort(ep); e == nil {
		host = h
		if port, err = strconv.Atoi(p); err != nil || port < 1 || port > 65535 {
			return "", 0, fmt.Errorf("neispravan port u adresi druge strane")
		}
	}
	host = strings.ToLower(host)
	if net.ParseIP(host) == nil && !validDNSName(host) {
		return "", 0, fmt.Errorf("neispravna adresa druge strane (IP ili ime)")
	}
	return host, port, nil
}

// validateWGSite provjerava sve što bi tiho pokvarilo mrežu: adresu u tunelu,
// preklapanje mreža s vlastitima, s tunelom i s drugim poslovnicama.
func (s *server) validateWGSite(ctx context.Context, x *WGSite, selfUUID string) error {
	x.Name = strings.TrimSpace(x.Name)
	x.TunnelIP = strings.TrimSpace(x.TunnelIP)
	x.PublicKey = strings.TrimSpace(x.PublicKey)
	x.Endpoint = strings.TrimSpace(x.Endpoint)
	if x.Name == "" {
		return fmt.Errorf("naziv poslovnice je obavezan")
	}
	tn := s.wgsNet()
	if tn == nil {
		return fmt.Errorf("prvo spremi postavke tunela (naša adresa u tunelu)")
	}
	ip := net.ParseIP(x.TunnelIP)
	if ip == nil || ip.To4() == nil {
		return fmt.Errorf("neispravna adresa u tunelu")
	}
	if !tn.Contains(ip) {
		return fmt.Errorf("adresa %s nije u mreži tunela %s", x.TunnelIP, tn.String())
	}
	if tunnelIPReserved(tn, ip) {
		return fmt.Errorf("adresa %s je rezervirana (mrežna adresa, naša adresa u tunelu ili broadcast)", x.TunnelIP)
	}
	if x.Keepalive < 0 || x.Keepalive > 65535 {
		return fmt.Errorf("neispravan keepalive")
	}
	if _, _, err := splitEndpoint(x.Endpoint); err != nil {
		return err
	}

	nets, err := parseSubnetList(x.Subnets)
	if err != nil {
		return err
	}
	for _, n := range nets {
		if netsOverlap(n, tn) {
			return fmt.Errorf("mreža %s preklapa se s mrežom tunela %s", n.String(), tn.String())
		}
		for _, ln := range localIPv4Nets(ctx) {
			if netsOverlap(n, ln) {
				return fmt.Errorf("mreža %s preklapa se s našom mrežom %s — dvije poslovnice ne mogu koristiti isti raspon adresa, jednoj ga treba promijeniti", n.String(), ln.String())
			}
		}
		for _, rn := range routedIPv4Nets(wgsIface) {
			if netsOverlap(n, rn.net) {
				return fmt.Errorf("mreža %s preklapa se s mrežom %s koju uređaj već koristi (%s)", n.String(), rn.net.String(), rn.dev)
			}
		}
	}
	// preklapanje s već upisanim poslovnicama
	rows, err := s.db.Query(`SELECT uuid, name, subnets FROM wg_sites WHERE uuid <> ?`, selfUUID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var u, nm, sub string
		if err := rows.Scan(&u, &nm, &sub); err != nil {
			return err
		}
		other, err := parseSubnetList(sub)
		if err != nil {
			continue
		}
		for _, a := range nets {
			for _, b := range other {
				if netsOverlap(a, b) {
					return fmt.Errorf("mreža %s preklapa se s mrežom %s poslovnice %q", a.String(), b.String(), nm)
				}
			}
		}
	}
	x.Subnets = netListString(nets)
	return nil
}

func (s *server) handleWGSiteCreate(w http.ResponseWriter, r *http.Request) {
	var in wgSiteIn
	if !decodeBody(w, r, &in) {
		return
	}
	x := &in.WGSite
	if err := s.validateWGSite(r.Context(), x, ""); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	priv := ""
	if x.PublicKey == "" {
		// druga strana još nema ključeve — složimo ih mi i damo joj gotov config
		var pub string
		var err error
		priv, pub, err = wgGenKey()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		x.PublicKey = pub
	} else if !validWGKey(x.PublicKey) {
		writeErr(w, http.StatusBadRequest, "neispravan javni ključ druge strane")
		return
	}
	x.UUID = newUUID()
	var privVal, keepVal any
	if priv != "" {
		privVal = priv
	}
	if x.Keepalive > 0 {
		keepVal = x.Keepalive
	}
	_, err := s.db.Exec(`INSERT INTO wg_sites
		(uuid, name, public_key, private_key, tunnel_ip, subnets, endpoint,
		 keepalive, enabled, notes) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		x.UUID, x.Name, x.PublicKey, privVal, x.TunnelIP, x.Subnets, x.Endpoint,
		keepVal, enabledIntOf(in.Enabled), x.Notes)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict,
				"poslovnica s tim ključem ili adresom u tunelu već postoji")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	addEvent(s, "info", "Dodana poslovnica "+x.Name+" ("+x.Subnets+")")
	xx, _ := scanWGSite(s.db.QueryRow(`SELECT `+wgSiteCols+` FROM wg_sites WHERE uuid=?`, x.UUID))
	writeJSON(w, http.StatusCreated, xx)
}

func (s *server) handleWGSiteUpdate(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	var in wgSiteIn
	if !decodeBody(w, r, &in) {
		return
	}
	x := &in.WGSite
	if err := s.validateWGSite(r.Context(), x, uuid); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var keepVal any
	if x.Keepalive > 0 {
		keepVal = x.Keepalive
	}
	res, err := s.db.Exec(`UPDATE wg_sites SET name=?, tunnel_ip=?, subnets=?,
		endpoint=?, keepalive=?, enabled=?, notes=?, updated_at=datetime('now')
		WHERE uuid=?`, x.Name, x.TunnelIP, x.Subnets, x.Endpoint, keepVal,
		enabledIntOf(in.Enabled), x.Notes, uuid)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "poslovnica s tom adresom u tunelu već postoji")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "poslovnica ne postoji")
		return
	}
	xx, _ := scanWGSite(s.db.QueryRow(`SELECT `+wgSiteCols+` FROM wg_sites WHERE uuid=?`, uuid))
	writeJSON(w, http.StatusOK, xx)
}

func (s *server) handleWGSiteDelete(w http.ResponseWriter, r *http.Request) {
	var name string
	s.db.QueryRow(`SELECT name FROM wg_sites WHERE uuid=?`, r.PathValue("uuid")).Scan(&name)
	res, err := s.db.Exec(`DELETE FROM wg_sites WHERE uuid=?`, r.PathValue("uuid"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "poslovnica ne postoji")
		return
	}
	// zapamćeno stanje veze ide s njom, da se ne vuče u nedogled
	s.db.Exec(`DELETE FROM settings WHERE key IN (?,?)`,
		"wgs_up_"+r.PathValue("uuid"), "wgs_lastup_"+r.PathValue("uuid"))
	addEvent(s, "warning", "Obrisana poslovnica "+name+" (veza prestaje raditi nakon Primijeni)")
	writeJSON(w, http.StatusOK, map[string]string{"deleted": r.PathValue("uuid")})
}

/* ---------- config za drugu stranu ---------- */

// handleWGSiteConfig sastavlja gotovu konfiguraciju za uređaj u drugoj
// poslovnici. Sve što druga strana treba je u toj jednoj datoteci — ključevi,
// adrese, naše mreže i naša javna adresa.
func (s *server) handleWGSiteConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var name, priv, tunnelIP, subnets, endpointTheirs string
	var keep int64
	err := s.db.QueryRow(`SELECT name, COALESCE(private_key,''), tunnel_ip,
		subnets, COALESCE(endpoint,''), COALESCE(keepalive,0) FROM wg_sites
		WHERE uuid=?`, r.PathValue("uuid")).Scan(&name, &priv, &tunnelIP,
		&subnets, &endpointTheirs, &keep)
	if err != nil {
		writeErr(w, http.StatusNotFound, "poslovnica ne postoji")
		return
	}
	cfg, err := uciGetConfig(ctx, "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	sec, ok := cfg[wgsIface]
	if !ok {
		writeErr(w, http.StatusConflict, "prvo spremi postavke tunela")
		return
	}
	ourPub, err := wgPubFromPriv(sectStr(sec, "private_key"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	port := sectStr(sec, "listen_port")
	mask := "24"
	tn := s.wgsNet()
	if tn != nil {
		mask = strings.Split(tn.String(), "/")[1]
	}
	endpoint := s.getSetting("wgs_endpoint_host", "")
	// naše mreže koje druga strana mora znati doseći kroz tunel
	ourNets := []string{}
	if tn != nil {
		ourNets = append(ourNets, tn.String())
	}
	for _, n := range localIPv4Nets(ctx) {
		ourNets = append(ourNets, n.String())
	}
	if keep == 0 {
		keep = 25
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Veza ured-ured prema poslovnici %q\n", name)
	b.WriteString("# Napravio Saguaro. Ovo se upisuje na uređaj DRUGE strane.\n")
	if priv == "" {
		b.WriteString("# Privatni ključ ove strane Saguaro nema (donijeli ste vlastiti\n" +
			"# javni ključ) — upiši ga niže umjesto oznake.\n")
	}
	b.WriteString("\n[Interface]\n")
	if priv != "" {
		fmt.Fprintf(&b, "PrivateKey = %s\n", priv)
	} else {
		b.WriteString("PrivateKey = <privatni ključ te strane>\n")
	}
	fmt.Fprintf(&b, "Address = %s/%s\n", tunnelIP, mask)
	// Port druga strana treba fiksirati samo ako je mi zovemo — tada mora
	// slušati točno ondje gdje ćemo pokucati. Ako ona zove nas, port neka
	// bira sama; upisivanje našeg porta ondje samo zbunjuje.
	if host, theirPort, err := splitEndpoint(endpointTheirs); err == nil && host != "" {
		if theirPort == 0 {
			theirPort = wgsDefaultPort
		}
		fmt.Fprintf(&b, "ListenPort = %d\n", theirPort)
	}
	b.WriteString("\n[Peer]\n")
	fmt.Fprintf(&b, "# ovo smo mi\nPublicKey = %s\n", ourPub)
	fmt.Fprintf(&b, "AllowedIPs = %s\n", strings.Join(ourNets, ", "))
	if endpoint != "" {
		fmt.Fprintf(&b, "Endpoint = %s:%s\n", endpoint, port)
	} else {
		b.WriteString("# Endpoint = <naša javna adresa>:" + port + "\n" +
			"# (upiši javnu adresu ovog uređaja u postavke tunela pa ponovi)\n")
	}
	fmt.Fprintf(&b, "PersistentKeepalive = %d\n", keep)

	writeJSON(w, http.StatusOK, map[string]any{
		"name":   name,
		"config": b.String(),
		"note": "Mreže koje ovaj uređaj objavljuje drugoj strani: " +
			strings.Join(ourNets, ", ") + ". Mreže druge strane: " + subnets + ".",
	})
}

/* ---------- primjena ---------- */

func (s *server) handleWGSApply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cfg, err := uciGetConfig(ctx, "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if _, ok := cfg[wgsIface]; !ok {
		writeErr(w, http.StatusConflict, "prvo spremi postavke tunela, pa primijeni poslovnice")
		return
	}
	rows, err := s.db.Query(`SELECT uuid, name, public_key, tunnel_ip, subnets,
		COALESCE(endpoint,''), COALESCE(keepalive,0) FROM wg_sites
		WHERE enabled = 1 ORDER BY tunnel_ip`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	type site struct {
		uuid, name, pub, ip, subnets, endpoint string
		keep                                   int64
	}
	sites := []site{}
	for rows.Next() {
		var x site
		if err := rows.Scan(&x.uuid, &x.name, &x.pub, &x.ip, &x.subnets,
			&x.endpoint, &x.keep); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		sites = append(sites, x)
	}

	backupName, err := s.backupConfig(networkConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}

	peerType := "wireguard_" + wgsIface
	var batch strings.Builder
	removed := 0
	for name, sec := range cfg {
		if strings.HasPrefix(name, sagPrefix) && sectStr(sec, ".type") == peerType {
			fmt.Fprintf(&batch, "delete network.%s\n", name)
			removed++
		}
	}
	for _, x := range sites {
		sn := sagPrefix + "s" + strings.ReplaceAll(x.uuid, "-", "")[:8]
		fmt.Fprintf(&batch, "set network.%s=%s\n", sn, peerType)
		fmt.Fprintf(&batch, "set network.%s.public_key=%s\n", sn, x.pub)
		fmt.Fprintf(&batch, "set network.%s.description=%s\n", sn, sanitizeDNSName(x.name))
		fmt.Fprintf(&batch, "add_list network.%s.allowed_ips=%s/32\n", sn, x.ip)
		nets, err := parseSubnetList(x.subnets)
		if err != nil {
			writeErr(w, http.StatusConflict, "poslovnica "+x.name+": "+err.Error())
			return
		}
		for _, n := range nets {
			fmt.Fprintf(&batch, "add_list network.%s.allowed_ips=%s\n", sn, n.String())
		}
		// rute prema mrežama druge poslovnice postavlja sam WireGuard
		fmt.Fprintf(&batch, "set network.%s.route_allowed_ips=1\n", sn)
		if host, port, err := splitEndpoint(x.endpoint); err == nil && host != "" {
			if port == 0 {
				port = wgsDefaultPort
			}
			fmt.Fprintf(&batch, "set network.%s.endpoint_host=%s\n", sn, host)
			fmt.Fprintf(&batch, "set network.%s.endpoint_port=%d\n", sn, port)
		}
		keep := x.keep
		if keep == 0 {
			keep = 25 // veza kroz NAT se bez toga zatvori nakon minute mira
		}
		fmt.Fprintf(&batch, "set network.%s.persistent_keepalive=%d\n", sn, keep)
	}
	batch.WriteString("commit network\n")
	if err := uciBatch(ctx, batch.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := serviceReload(ctx, "network", "reload"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := exec.CommandContext(ctx, "ifup", wgsIface).Run(); err != nil {
		writeErr(w, http.StatusInternalServerError, "ifup: "+err.Error())
		return
	}
	addEvent(s, "info", fmt.Sprintf("Primijenjeno veza ured-ured: %d poslovnica", len(sites)))
	writeJSON(w, http.StatusOK, map[string]any{
		"applied": len(sites), "removed": removed, "backup": backupName,
	})
}

/* ---------- nadzor veze ---------- */

// wgsSiteDownAfter je koliko dugo veza smije šutjeti prije nego se proglasi
// palom. WireGuard se javlja svakih 25 s (keepalive) i obnavlja ključeve
// svake 2 minute, pa je 5 minuta tišine pouzdan znak da veze nema.
const wgsSiteDownAfter = 5 * time.Minute

// checkSiteTunnels javlja kad veza s poslovnicom padne ili se vrati. Poziva se
// iz nadzorne petlje; stanje se pamti u settings da se poruka ne ponavlja.
func (s *server) checkSiteTunnels() {
	rows, err := s.db.Query(`SELECT uuid, name, public_key FROM wg_sites WHERE enabled = 1`)
	if err != nil {
		return
	}
	type site struct{ uuid, name, pub string }
	sites := []site{}
	for rows.Next() {
		var x site
		if rows.Scan(&x.uuid, &x.name, &x.pub) == nil {
			sites = append(sites, x)
		}
	}
	rows.Close()
	if len(sites) == 0 {
		return
	}
	stats, running := wgsDump(context.Background())
	now := time.Now().Unix()
	silence := int64(wgsSiteDownAfter / time.Second)
	for _, x := range sites {
		alive := false
		if running {
			if st, ok := stats[x.pub]; ok && st.LatestHandshake > 0 &&
				now-st.LatestHandshake < silence {
				alive = true
			}
		}
		// Kad se sučelje ponovno podigne, WireGuard zaboravi vrijeme zadnjeg
		// javljanja i veza na tren izgleda kao pala. Zato se pad ne mjeri po
		// tom broju nego po tome koliko dugo veze nema — inače svako spremanje
		// postavki pošalje lažnu uzbunu (nađeno mjerenjem na uređaju).
		seenKey := "wgs_lastup_" + x.uuid
		lastUp, _ := strconv.ParseInt(s.getSetting(seenKey, ""), 10, 64)
		if alive || lastUp == 0 {
			lastUp = now
			s.setSetting(seenKey, strconv.FormatInt(lastUp, 10))
		}
		up := alive || now-lastUp < silence

		key := "wgs_up_" + x.uuid
		prev := s.getSetting(key, "")
		cur := boolSetting(up)
		if prev == cur {
			continue
		}
		// prvi prolaz nakon dodavanja poslovnice ne javlja ništa: veza se tek
		// uspostavlja i lažna uzbuna bi stigla prije nego itko išta spoji
		if prev != "" {
			if up {
				s.alert("site_tunnel", "info", "Veza s poslovnicom "+x.name+" je uspostavljena")
			} else {
				s.alert("site_tunnel", "warning", "Veza s poslovnicom "+x.name+" je pala")
			}
		}
		s.setSetting(key, cur)
	}
}
