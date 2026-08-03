package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
)

// Saguaro upravlja vlastitim WireGuard sučeljem; ime nosi sag prefiks (D-011).
const wgIface = "sag_wg0"
const firewallConfig = "/etc/config/firewall"

/* ---------- ključevi (X25519, isti format kao wg genkey) ---------- */

func wgGenKey() (priv, pub string, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(k.Bytes()),
		base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

func wgPubFromPriv(privB64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(privB64))
	if err != nil {
		return "", err
	}
	k, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

func validWGKey(b64 string) bool {
	raw, err := base64.StdEncoding.DecodeString(b64)
	return err == nil && len(raw) == 32
}

/* ---------- postavke u bazi ---------- */

func (s *server) getSetting(key, def string) string {
	var v string
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v); err != nil {
		return def
	}
	return v
}

func (s *server) setSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value,
		updated_at=datetime('now')`, key, value)
	return err
}

/* ---------- model peera ---------- */

type WGPeer struct {
	UUID       string `json:"uuid"`
	Name       string `json:"name"`
	PublicKey  string `json:"public_key"`
	TunnelIP   string `json:"tunnel_ip"`
	Keepalive  int64  `json:"keepalive"`
	Enabled    bool   `json:"enabled"`
	Notes      string `json:"notes"`
	HasPrivate bool   `json:"has_private"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// privatni ključ se NIKAD ne vraća kroz API popisa — samo has_private zastavica
const wgPeerCols = `uuid, name, public_key, tunnel_ip, COALESCE(keepalive,0),
	enabled, COALESCE(notes,''), private_key IS NOT NULL, created_at, updated_at`

func scanWGPeer(row interface{ Scan(...any) error }) (WGPeer, error) {
	var p WGPeer
	err := row.Scan(&p.UUID, &p.Name, &p.PublicKey, &p.TunnelIP, &p.Keepalive,
		&p.Enabled, &p.Notes, &p.HasPrivate, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

/* ---------- čitanje stanja ---------- */

type wgPeerStats struct {
	Endpoint        string `json:"endpoint"`
	LatestHandshake int64  `json:"latest_handshake"`
	RxBytes         int64  `json:"rx_bytes"`
	TxBytes         int64  `json:"tx_bytes"`
}

// wgRefresh forsira ponovno podizanje sučelja — `network reload` ne detektira
// promjene peer sekcija (proto handler ih čita tek pri setupu sučelja).
func wgRefresh(ctx context.Context) error {
	return exec.CommandContext(ctx, "ifup", wgIface).Run()
}

// wgDump čita runtime stanje kroz `wg show <if> dump` — strojno čitljiv,
// tab-odvojen format dokumentiran u wg(8); ubus ne izlaže WireGuard stanje.
// Prva linija (sučelje, sadrži privatni ključ) se preskače.
func wgDump(ctx context.Context) (map[string]wgPeerStats, bool) {
	out, err := exec.CommandContext(ctx, "wg", "show", wgIface, "dump").Output()
	if err != nil {
		return nil, false
	}
	stats := map[string]wgPeerStats{}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i, line := range lines {
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

func (s *server) handleWGStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := uciGetConfig(r.Context(), "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	_, lookErr := exec.LookPath("wg")
	srv := map[string]any{"configured": false}
	if sec, ok := cfg[wgIface]; ok {
		pub := ""
		if pk := sectStr(sec, "private_key"); pk != "" {
			pub, _ = wgPubFromPriv(pk)
		}
		addrs := []string{}
		switch v := sec["addresses"].(type) {
		case string:
			addrs = append(addrs, v)
		case []any:
			for _, a := range v {
				if str, ok := a.(string); ok {
					addrs = append(addrs, str)
				}
			}
		}
		// prijedlog za sljedećeg peera: prva slobodna adresa u tunelu
		nextIP := ""
		if n := s.wgServerNet(); n != nil {
			nextIP = nextFreeTunnelIP(n, s.usedTunnelIPs("wg_peers"))
		}
		srv = map[string]any{
			"configured":         true,
			"next_tunnel_ip":     nextIP,
			"listen_port":        sectStr(sec, "listen_port"),
			"addresses":          addrs,
			"public_key":         pub,
			"endpoint_host":      s.getSetting("wg_endpoint_host", ""),
			"client_dns":         s.getSetting("wg_client_dns", ""),
			"client_allowed_ips": s.getSetting("wg_client_allowed_ips", "0.0.0.0/0"),
			"allow_mgmt":         s.getSetting("wg_allow_mgmt", "0") == "1",
		}
	}

	uciPeers := 0
	for name, sec := range cfg {
		if strings.HasPrefix(name, sagPrefix) && sectStr(sec, ".type") == "wireguard_"+wgIface {
			uciPeers++
		}
	}

	// način pristupa: full ako postoje sagwg->lan/wan forwardinzi
	accessMode := "restricted"
	if fwCfg, err := uciGetConfig(r.Context(), "firewall"); err == nil {
		if _, ok := fwCfg["sag_wg_lan"]; ok {
			accessMode = "full"
		} else if _, ok := fwCfg["sag_wg_wan"]; ok {
			accessMode = "full"
		}
	}

	stats, running := wgDump(r.Context())
	if stats == nil {
		stats = map[string]wgPeerStats{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installed":   lookErr == nil,
		"server":      srv,
		"running":     running,
		"uci_peers":   uciPeers,
		"access_mode": accessMode,
		"stats":       stats,
	})
}

/* ---------- postavke poslužitelja ---------- */

type wgServerIn struct {
	ListenPort       int    `json:"listen_port"`
	Address          string `json:"address"` // CIDR, npr. 10.6.0.1/24
	EndpointHost     string `json:"endpoint_host"`
	ClientDNS        string `json:"client_dns"`
	ClientAllowedIPs string `json:"client_allowed_ips"`
	// AllowMgmt otvara upravljanje uređajem (SSH, LuCI, Saguaro) VPN
	// korisnicima. Zadano isključeno — VPN je pristup mreži, ne upravljanju.
	AllowMgmt bool `json:"allow_mgmt"`
}

func (s *server) handleWGServerSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in wgServerIn
	if !decodeBody(w, r, &in) {
		return
	}
	if in.ListenPort == 0 {
		in.ListenPort = 51820
	}
	if in.ListenPort < 1 || in.ListenPort > 65535 {
		writeErr(w, http.StatusBadRequest, "neispravan port")
		return
	}
	ip, ipnet, err := net.ParseCIDR(strings.TrimSpace(in.Address))
	if err != nil || ip.To4() == nil {
		writeErr(w, http.StatusBadRequest,
			"adresa tunela mora biti IPv4 CIDR, npr. 10.6.0.1/24")
		return
	}
	in.EndpointHost = strings.ToLower(strings.TrimSpace(in.EndpointHost))
	if in.EndpointHost != "" && net.ParseIP(in.EndpointHost) == nil &&
		!validDNSName(in.EndpointHost) {
		writeErr(w, http.StatusBadRequest, "neispravan endpoint (IP ili ime)")
		return
	}
	in.ClientDNS = strings.TrimSpace(in.ClientDNS)
	if in.ClientDNS != "" && net.ParseIP(in.ClientDNS) == nil {
		writeErr(w, http.StatusBadRequest, "neispravan DNS za klijente")
		return
	}
	// prazno = split tunnel: kroz tunel ide samo mreža tunela i LAN,
	// internet ostaje na klijentovoj vlastitoj vezi
	in.ClientAllowedIPs = strings.TrimSpace(in.ClientAllowedIPs)
	if in.ClientAllowedIPs == "" {
		parts := []string{ipnet.String()}
		if lanCfg, err := uciGetConfig(ctx, "network"); err == nil {
			if lan, ok := lanCfg["lan"]; ok {
				lanIP := net.ParseIP(sectStr(lan, "ipaddr"))
				lanMask := net.ParseIP(sectStr(lan, "netmask"))
				if lanIP != nil && lanMask != nil && lanMask.To4() != nil {
					m := net.IPMask(lanMask.To4())
					parts = append(parts, (&net.IPNet{
						IP: lanIP.Mask(m), Mask: m}).String())
				}
			}
		}
		in.ClientAllowedIPs = strings.Join(parts, ", ")
	}
	for _, c := range strings.Split(in.ClientAllowedIPs, ",") {
		if _, _, err := net.ParseCIDR(strings.TrimSpace(c)); err != nil {
			writeErr(w, http.StatusBadRequest, "neispravan AllowedIPs CIDR: "+c)
			return
		}
	}

	// backup oba configa prije izmjene (D-011)
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
	priv := ""
	if sec, ok := cfg[wgIface]; ok {
		priv = sectStr(sec, "private_key")
	}
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

	var nb strings.Builder
	fmt.Fprintf(&nb, "set network.%s=interface\n", wgIface)
	fmt.Fprintf(&nb, "set network.%s.proto=wireguard\n", wgIface)
	fmt.Fprintf(&nb, "set network.%s.private_key=%s\n", wgIface, priv)
	fmt.Fprintf(&nb, "set network.%s.listen_port=%d\n", wgIface, in.ListenPort)
	if _, ok := cfg[wgIface]["addresses"]; ok {
		fmt.Fprintf(&nb, "delete network.%s.addresses\n", wgIface)
	}
	fmt.Fprintf(&nb, "add_list network.%s.addresses=%s/%s\n", wgIface,
		ip.String(), strings.Split(ipnet.String(), "/")[1])
	nb.WriteString("commit network\n")
	if err := uciBatch(ctx, nb.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// vlastita firewall zona + pravilo za dolazni port; tuđe zone se ne diraju
	var fb strings.Builder
	fmt.Fprintf(&fb, "set firewall.sag_wg_rule=rule\n")
	fmt.Fprintf(&fb, "set firewall.sag_wg_rule.name=Saguaro-WireGuard\n")
	fmt.Fprintf(&fb, "set firewall.sag_wg_rule.src=wan\n")
	fmt.Fprintf(&fb, "set firewall.sag_wg_rule.proto=udp\n")
	fmt.Fprintf(&fb, "set firewall.sag_wg_rule.dest_port=%d\n", in.ListenPort)
	fmt.Fprintf(&fb, "set firewall.sag_wg_rule.target=ACCEPT\n")
	fmt.Fprintf(&fb, "set firewall.sag_wg_zone=zone\n")
	fmt.Fprintf(&fb, "set firewall.sag_wg_zone.name=sagwg\n")
	fmt.Fprintf(&fb, "delete firewall.sag_wg_zone.network\n")
	fmt.Fprintf(&fb, "add_list firewall.sag_wg_zone.network=%s\n", wgIface)
	fmt.Fprintf(&fb, "set firewall.sag_wg_zone.output=ACCEPT\n")
	fmt.Fprintf(&fb, "set firewall.sag_wg_zone.forward=REJECT\n")
	fwCfg, _ := uciGetConfig(ctx, "firewall")
	vpnZoneInput(&fb, fwCfg, "sag_wg", "sagwg", "WireGuard", in.AllowMgmt)
	fmt.Fprintf(&fb, "set firewall.sag_wg_lan=forwarding\n")
	fmt.Fprintf(&fb, "set firewall.sag_wg_lan.src=sagwg\n")
	fmt.Fprintf(&fb, "set firewall.sag_wg_lan.dest=lan\n")
	fmt.Fprintf(&fb, "set firewall.sag_wg_wan=forwarding\n")
	fmt.Fprintf(&fb, "set firewall.sag_wg_wan.src=sagwg\n")
	fmt.Fprintf(&fb, "set firewall.sag_wg_wan.dest=wan\n")
	fb.WriteString("commit firewall\n")
	if err := uciBatch(ctx, fb.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	for k, v := range map[string]string{
		"wg_endpoint_host":      in.EndpointHost,
		"wg_client_dns":         in.ClientDNS,
		"wg_client_allowed_ips": in.ClientAllowedIPs,
		"wg_allow_mgmt":         boolSetting(in.AllowMgmt),
	} {
		if err := s.setSetting(k, v); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	if err := serviceReload(ctx, "network", "reload"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := serviceReload(ctx, "firewall", "reload"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := wgRefresh(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "ifup: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"applied":    true,
		"public_key": pub,
		"backups":    []string{netBackup, fwBackup},
	})
}

/* ---------- peer CRUD ---------- */

func (s *server) wgServerNet() *net.IPNet {
	// mreža tunela iz uci konfiguracije sučelja (prva IPv4 adresa)
	cfg, err := uciGetConfig(context.Background(), "network")
	if err != nil {
		return nil
	}
	sec, ok := cfg[wgIface]
	if !ok {
		return nil
	}
	addrs := []string{sectStr(sec, "addresses")}
	if v, ok := sec["addresses"].([]any); ok {
		addrs = addrs[:0]
		for _, a := range v {
			if str, ok := a.(string); ok {
				addrs = append(addrs, str)
			}
		}
	}
	for _, a := range addrs {
		if _, ipnet, err := net.ParseCIDR(a); err == nil && ipnet.IP.To4() != nil {
			return ipnet
		}
	}
	return nil
}

func (s *server) handleWGPeerList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT ` + wgPeerCols + ` FROM wg_peers ORDER BY tunnel_ip`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	out := []WGPeer{}
	for rows.Next() {
		p, err := scanWGPeer(rows)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"peers": out})
}

type wgPeerIn struct {
	WGPeer
	Enabled *bool `json:"enabled"`
}

func (in *wgPeerIn) enabledInt() int {
	if in.Enabled == nil || *in.Enabled {
		return 1
	}
	return 0
}

func (s *server) validateWGPeer(w http.ResponseWriter, p *WGPeer) bool {
	p.Name = strings.TrimSpace(p.Name)
	p.TunnelIP = strings.TrimSpace(p.TunnelIP)
	p.PublicKey = strings.TrimSpace(p.PublicKey)
	if p.Name == "" {
		writeErr(w, http.StatusBadRequest, "naziv je obavezan")
		return false
	}
	ip := net.ParseIP(p.TunnelIP)
	if ip == nil || ip.To4() == nil {
		writeErr(w, http.StatusBadRequest, "neispravna adresa u tunelu")
		return false
	}
	n := s.wgServerNet()
	if n != nil && !n.Contains(ip) {
		writeErr(w, http.StatusBadRequest,
			"adresa "+p.TunnelIP+" nije u mreži tunela "+n.String())
		return false
	}
	if tunnelIPReserved(n, ip) {
		writeErr(w, http.StatusBadRequest, "adresa "+p.TunnelIP+
			" je rezervirana (mrežna adresa, adresa uređaja u tunelu ili broadcast)")
		return false
	}
	if p.Keepalive < 0 || p.Keepalive > 65535 {
		writeErr(w, http.StatusBadRequest, "neispravan keepalive")
		return false
	}
	return true
}

func (s *server) handleWGPeerCreate(w http.ResponseWriter, r *http.Request) {
	var in wgPeerIn
	if !decodeBody(w, r, &in) {
		return
	}
	p := &in.WGPeer
	if !s.validateWGPeer(w, p) {
		return
	}
	priv := ""
	if p.PublicKey == "" {
		var pub string
		var err error
		priv, pub, err = wgGenKey()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		p.PublicKey = pub
	} else if !validWGKey(p.PublicKey) {
		writeErr(w, http.StatusBadRequest, "neispravan javni ključ")
		return
	}
	p.UUID = newUUID()
	var privVal any
	if priv != "" {
		privVal = priv
	}
	var keepVal any
	if p.Keepalive > 0 {
		keepVal = p.Keepalive
	}
	_, err := s.db.Exec(`INSERT INTO wg_peers
		(uuid, name, public_key, private_key, tunnel_ip, keepalive, enabled, notes)
		VALUES (?,?,?,?,?,?,?,?)`,
		p.UUID, p.Name, p.PublicKey, privVal, p.TunnelIP, keepVal,
		in.enabledInt(), p.Notes)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict,
				"peer s tim ključem ili adresom u tunelu već postoji")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	pp, _ := scanWGPeer(s.db.QueryRow(
		`SELECT `+wgPeerCols+` FROM wg_peers WHERE uuid=?`, p.UUID))
	writeJSON(w, http.StatusCreated, pp)
}

func (s *server) handleWGPeerUpdate(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	var in wgPeerIn
	if !decodeBody(w, r, &in) {
		return
	}
	p := &in.WGPeer
	// ključevi su identitet peera i ne mijenjaju se kroz PUT
	if !s.validateWGPeer(w, p) {
		return
	}
	var keepVal any
	if p.Keepalive > 0 {
		keepVal = p.Keepalive
	}
	res, err := s.db.Exec(`UPDATE wg_peers SET name=?, tunnel_ip=?, keepalive=?,
		enabled=?, notes=?, updated_at=datetime('now') WHERE uuid=?`,
		p.Name, p.TunnelIP, keepVal, in.enabledInt(), p.Notes, uuid)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "peer s tom adresom u tunelu već postoji")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "peer ne postoji")
		return
	}
	pp, _ := scanWGPeer(s.db.QueryRow(
		`SELECT `+wgPeerCols+` FROM wg_peers WHERE uuid=?`, uuid))
	writeJSON(w, http.StatusOK, pp)
}

func (s *server) handleWGPeerDelete(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.Exec(`DELETE FROM wg_peers WHERE uuid=?`, r.PathValue("uuid"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "peer ne postoji")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": r.PathValue("uuid")})
}

/* ---------- klijentski config ---------- */

func (s *server) handleWGPeerConfig(w http.ResponseWriter, r *http.Request) {
	var name, priv, tunnelIP string
	var keep int64
	err := s.db.QueryRow(`SELECT name, COALESCE(private_key,''), tunnel_ip,
		COALESCE(keepalive,0) FROM wg_peers WHERE uuid=?`,
		r.PathValue("uuid")).Scan(&name, &priv, &tunnelIP, &keep)
	if err != nil {
		writeErr(w, http.StatusNotFound, "peer ne postoji")
		return
	}
	if priv == "" {
		writeErr(w, http.StatusConflict,
			"peer je donio vlastiti ključ — config se sastavlja na klijentu")
		return
	}

	cfg, err := uciGetConfig(r.Context(), "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	sec, ok := cfg[wgIface]
	if !ok {
		writeErr(w, http.StatusConflict, "WireGuard poslužitelj još nije konfiguriran")
		return
	}
	srvPub, err := wgPubFromPriv(sectStr(sec, "private_key"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	port := sectStr(sec, "listen_port")

	mask := "32"
	if n := s.wgServerNet(); n != nil {
		mask = strings.Split(n.String(), "/")[1]
	}
	endpoint := s.getSetting("wg_endpoint_host", "")
	if endpoint == "" {
		if lan, ok := cfg["lan"]; ok {
			endpoint = sectStr(lan, "ipaddr")
		}
	}
	if keep == 0 {
		keep = 25
	}

	var b strings.Builder
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", priv)
	fmt.Fprintf(&b, "Address = %s/%s\n", tunnelIP, mask)
	if dns := s.getSetting("wg_client_dns", ""); dns != "" {
		fmt.Fprintf(&b, "DNS = %s\n", dns)
	}
	b.WriteString("\n[Peer]\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", srvPub)
	fmt.Fprintf(&b, "AllowedIPs = %s\n", s.getSetting("wg_client_allowed_ips", "0.0.0.0/0"))
	fmt.Fprintf(&b, "Endpoint = %s:%s\n", endpoint, port)
	fmt.Fprintf(&b, "PersistentKeepalive = %d\n", keep)

	writeJSON(w, http.StatusOK, map[string]any{
		"name":   name,
		"config": b.String(),
	})
}

/* ---------- pristupna pravila po peeru ---------- */

const vrPrefix = "sag_vr_"

type WGPeerRule struct {
	UUID      string `json:"uuid"`
	PeerUUID  string `json:"peer_uuid"`
	DestZone  string `json:"dest_zone"`
	DestIP    string `json:"dest_ip"`
	DestPort  string `json:"dest_port"`
	Proto     string `json:"proto"`
	Enabled   bool   `json:"enabled"`
	Notes     string `json:"notes"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

const wgRuleCols = `uuid, peer_uuid, dest_zone, COALESCE(dest_ip,''),
	COALESCE(dest_port,''), proto, enabled, COALESCE(notes,''),
	created_at, updated_at`

func scanWGRule(row interface{ Scan(...any) error }) (WGPeerRule, error) {
	var x WGPeerRule
	err := row.Scan(&x.UUID, &x.PeerUUID, &x.DestZone, &x.DestIP, &x.DestPort,
		&x.Proto, &x.Enabled, &x.Notes, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}

func (s *server) validateWGRule(w http.ResponseWriter, x *WGPeerRule) bool {
	x.DestZone = strings.TrimSpace(x.DestZone)
	x.DestIP = strings.TrimSpace(x.DestIP)
	x.DestPort = strings.TrimSpace(x.DestPort)
	x.Proto = strings.TrimSpace(x.Proto)
	if x.DestZone == "" {
		x.DestZone = "lan"
	}
	if x.Proto == "" {
		x.Proto = "tcp udp"
	}
	protoOK := map[string]bool{"tcp": true, "udp": true, "tcp udp": true,
		"icmp": true, "all": true}[x.Proto]
	switch {
	case x.DestZone != "*" && !reZone.MatchString(x.DestZone):
		writeErr(w, http.StatusBadRequest, "neispravna odredišna zona")
	case x.DestIP != "" && !s.validAddrOrAlias(x.DestIP):
		writeErr(w, http.StatusBadRequest,
			"neispravna odredišna adresa (IP, CIDR ili postojeći @alias)")
	case x.DestPort != "" && !validPortSpec(x.DestPort):
		writeErr(w, http.StatusBadRequest, "neispravan port (npr. 443 ili 8000-8010)")
	case !protoOK:
		writeErr(w, http.StatusBadRequest, "neispravan protokol")
	default:
		return true
	}
	return false
}

func (s *server) handleWGPeerRuleList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT `+wgRuleCols+` FROM wg_peer_rules
		WHERE peer_uuid=? ORDER BY created_at`, r.PathValue("uuid"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	out := []WGPeerRule{}
	for rows.Next() {
		x, err := scanWGRule(rows)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": out})
}

type wgRuleIn struct {
	WGPeerRule
	Enabled *bool `json:"enabled"`
}

func (s *server) handleWGPeerRuleCreate(w http.ResponseWriter, r *http.Request) {
	peerUUID := r.PathValue("uuid")
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM wg_peers WHERE uuid=?`,
		peerUUID).Scan(&n); err != nil || n == 0 {
		writeErr(w, http.StatusNotFound, "peer ne postoji")
		return
	}
	var in wgRuleIn
	if !decodeBody(w, r, &in) {
		return
	}
	x := &in.WGPeerRule
	if !s.validateWGRule(w, x) {
		return
	}
	x.UUID = newUUID()
	_, err := s.db.Exec(`INSERT INTO wg_peer_rules
		(uuid, peer_uuid, dest_zone, dest_ip, dest_port, proto, enabled, notes)
		VALUES (?,?,?,?,?,?,?,?)`,
		x.UUID, peerUUID, x.DestZone, x.DestIP, x.DestPort, x.Proto,
		enabledIntOf(in.Enabled), x.Notes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	xx, _ := scanWGRule(s.db.QueryRow(
		`SELECT `+wgRuleCols+` FROM wg_peer_rules WHERE uuid=?`, x.UUID))
	writeJSON(w, http.StatusCreated, xx)
}

func (s *server) handleWGRuleDelete(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.Exec(`DELETE FROM wg_peer_rules WHERE uuid=?`, r.PathValue("uuid"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "pravilo ne postoji")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": r.PathValue("uuid")})
}

/* ---------- način pristupa (pun / ograničen) ---------- */

// handleWGAccessSet: "full" = forwardinzi sagwg->lan i sagwg->wan postoje
// (svi peerovi vide sve); "restricted" = forwardinzi se uklanjaju i vrijede
// samo pristupna pravila po peeru (sag_vr_*).
func (s *server) handleWGAccessSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Mode string `json:"mode"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Mode != "full" && in.Mode != "restricted" {
		writeErr(w, http.StatusBadRequest, "mode mora biti full ili restricted")
		return
	}
	backupName, err := s.backupConfig(firewallConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}
	cfg, err := uciGetConfig(ctx, "firewall")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var b strings.Builder
	if in.Mode == "full" {
		b.WriteString("set firewall.sag_wg_lan=forwarding\n")
		b.WriteString("set firewall.sag_wg_lan.src=sagwg\n")
		b.WriteString("set firewall.sag_wg_lan.dest=lan\n")
		b.WriteString("set firewall.sag_wg_wan=forwarding\n")
		b.WriteString("set firewall.sag_wg_wan.src=sagwg\n")
		b.WriteString("set firewall.sag_wg_wan.dest=wan\n")
	} else {
		for _, sect := range []string{"sag_wg_lan", "sag_wg_wan"} {
			if _, ok := cfg[sect]; ok {
				fmt.Fprintf(&b, "delete firewall.%s\n", sect)
			}
		}
	}
	if b.Len() == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"mode": in.Mode})
		return
	}
	b.WriteString("commit firewall\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := serviceReload(ctx, "firewall", "reload"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode": in.Mode, "backup": backupName,
	})
}

/* ---------- primjena peerova ---------- */

func (s *server) handleWGApply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	cfg, err := uciGetConfig(ctx, "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if _, ok := cfg[wgIface]; !ok {
		writeErr(w, http.StatusConflict,
			"prvo spremi postavke poslužitelja, pa primijeni peerove")
		return
	}

	rows, err := s.db.Query(`SELECT uuid, name, public_key, tunnel_ip,
		COALESCE(keepalive,0) FROM wg_peers WHERE enabled = 1 ORDER BY tunnel_ip`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	type pr struct {
		uuid, name, pub, ip string
		keep                int64
	}
	peers := []pr{}
	for rows.Next() {
		var x pr
		if err := rows.Scan(&x.uuid, &x.name, &x.pub, &x.ip, &x.keep); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		peers = append(peers, x)
	}

	backupName, err := s.backupConfig(networkConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}

	peerType := "wireguard_" + wgIface
	var batch strings.Builder
	removed := 0
	for name, sec := range cfg {
		if strings.HasPrefix(name, sagPrefix) && sectStr(sec, ".type") == peerType {
			fmt.Fprintf(&batch, "delete network.%s\n", name)
			removed++
		}
	}
	for _, x := range peers {
		sn := sagPrefix + "p" + strings.ReplaceAll(x.uuid, "-", "")[:8]
		fmt.Fprintf(&batch, "set network.%s=%s\n", sn, peerType)
		fmt.Fprintf(&batch, "set network.%s.public_key=%s\n", sn, x.pub)
		fmt.Fprintf(&batch, "set network.%s.description=%s\n", sn, sanitizeDNSName(x.name))
		fmt.Fprintf(&batch, "add_list network.%s.allowed_ips=%s/32\n", sn, x.ip)
		fmt.Fprintf(&batch, "set network.%s.route_allowed_ips=1\n", sn)
		if x.keep > 0 {
			fmt.Fprintf(&batch, "set network.%s.persistent_keepalive=%d\n", sn, x.keep)
		}
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
	if err := wgRefresh(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "ifup: "+err.Error())
		return
	}

	// pristupna pravila po peeru -> sag_vr_* rule sekcije (src_ip = tunel adresa)
	type vr struct {
		uuid, ip, zone, dip, dport, proto string
	}
	vrRows, err := s.db.Query(`SELECT r.uuid, p.tunnel_ip, r.dest_zone,
		COALESCE(r.dest_ip,''), COALESCE(r.dest_port,''), r.proto
		FROM wg_peer_rules r JOIN wg_peers p ON p.uuid = r.peer_uuid
		WHERE r.enabled = 1 AND p.enabled = 1 ORDER BY p.tunnel_ip`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	vrs := []vr{}
	for vrRows.Next() {
		var x vr
		if err := vrRows.Scan(&x.uuid, &x.ip, &x.zone, &x.dip, &x.dport, &x.proto); err != nil {
			vrRows.Close()
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		vrs = append(vrs, x)
	}
	vrRows.Close()

	fwBackup, err := s.backupConfig(firewallConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}
	fwCfg, err := uciGetConfig(ctx, "firewall")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var fb strings.Builder
	removedRules := 0
	for name, sec := range fwCfg {
		if strings.HasPrefix(name, vrPrefix) && sectStr(sec, ".type") == "rule" {
			fmt.Fprintf(&fb, "delete firewall.%s\n", name)
			removedRules++
		}
	}
	for _, x := range vrs {
		sn := vrPrefix + strings.ReplaceAll(x.uuid, "-", "")[:8]
		fmt.Fprintf(&fb, "set firewall.%s=rule\n", sn)
		fmt.Fprintf(&fb, "set firewall.%s.name=%s\n", sn, uciQuote("VPN "+x.ip+" -> "+x.zone))
		fmt.Fprintf(&fb, "set firewall.%s.src=sagwg\n", sn)
		fmt.Fprintf(&fb, "set firewall.%s.src_ip=%s\n", sn, x.ip)
		if x.zone != "*" {
			fmt.Fprintf(&fb, "set firewall.%s.dest=%s\n", sn, x.zone)
		} else {
			fmt.Fprintf(&fb, "set firewall.%s.dest=*\n", sn)
		}
		if x.dip != "" {
			addrs, aerr := s.resolveAlias(x.dip)
			if aerr != nil {
				writeErr(w, http.StatusConflict, "VPN pravilo: "+aerr.Error())
				return
			}
			for _, a := range addrs {
				fmt.Fprintf(&fb, "add_list firewall.%s.dest_ip=%s\n", sn, a)
			}
		}
		if x.dport != "" {
			fmt.Fprintf(&fb, "set firewall.%s.dest_port=%s\n", sn, x.dport)
		}
		if x.proto != "all" {
			fmt.Fprintf(&fb, "set firewall.%s.proto=%s\n", sn, uciQuote(x.proto))
		}
		fmt.Fprintf(&fb, "set firewall.%s.target=ACCEPT\n", sn)
	}
	fb.WriteString("commit firewall\n")
	if err := uciBatch(ctx, fb.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := serviceReload(ctx, "firewall", "reload"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"applied":       len(peers),
		"applied_rules": len(vrs),
		"removed":       removed + removedRules,
		"backup":        backupName,
		"backup_fw":     fwBackup,
	})
}
