package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// OpenVPN modul: vlastiti PKI (CA + certifikati, sve u Go-u), server kroz
// uci openvpn sekciju sag_server, fiksne adrese klijenata kroz CCD datoteke
// (ccd-exclusive: klijent bez CCD-a se ne može spojiti — to je i "opoziv"),
// pristupna pravila po klijentu kao sag_or_* firewall sekcije.
const ovpnUciSection = "sag_server"
const ovpnDev = "tun_sag"
const ovpnIface = "sag_ovpn"
const ovpnStatusFile = "/tmp/sag_ovpn.status"
const ovpnConfigFile = "/etc/config/openvpn"
const orPrefix = "sag_or_"

var reClientName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)

func (s *server) ovpnDir() string { return filepath.Join(s.etcDir, "ovpn") }
func (s *server) ccdDir() string  { return filepath.Join(s.ovpnDir(), "ccd") }

/* ---------- PKI (ECDSA P-256, potpisivanje vlastitim CA-om) ---------- */

func writePEM(path, blockType string, der []byte, mode os.FileMode) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), mode)
}

func newSerial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	return n
}

// ensureOvpnPKI stvara CA, serverski certifikat i tls-crypt ključ pri prvom pozivu.
func (s *server) ensureOvpnPKI() error {
	dir := s.ovpnDir()
	if err := os.MkdirAll(s.ccdDir(), 0o755); err != nil {
		return err
	}
	// OpenVPN nakon pokretanja odustaje od root ovlasti (user nobody), pa mora
	// moći ući u ovaj direktorij i čitati CCD datoteke. 0751 dopušta samo
	// prolaz, ne i ispis sadržaja; privatni ključevi ostaju 0600.
	_ = os.Chmod(dir, 0o751)
	_ = os.Chmod(s.ccdDir(), 0o755)
	caCrt, caKey := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	if _, err := os.Stat(caCrt); err != nil {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return err
		}
		tpl := &x509.Certificate{
			SerialNumber:          newSerial(),
			Subject:               pkix.Name{CommonName: "Saguaro VPN CA"},
			NotBefore:             time.Now().Add(-time.Hour),
			NotAfter:              time.Now().AddDate(10, 0, 0),
			IsCA:                  true,
			BasicConstraintsValid: true,
			KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		}
		der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
		if err != nil {
			return err
		}
		kder, _ := x509.MarshalECPrivateKey(key)
		if err := writePEM(caKey, "EC PRIVATE KEY", kder, 0o600); err != nil {
			return err
		}
		if err := writePEM(caCrt, "CERTIFICATE", der, 0o644); err != nil {
			return err
		}
	}
	// serverski certifikat
	srvCrt, srvKey := filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")
	if _, err := os.Stat(srvCrt); err != nil {
		certPEM, keyPEM, err := s.ovpnSignCert("saguaro-server",
			x509.ExtKeyUsageServerAuth)
		if err != nil {
			return err
		}
		if err := os.WriteFile(srvKey, []byte(keyPEM), 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(srvCrt, []byte(certPEM), 0o644); err != nil {
			return err
		}
	}
	// tls-crypt ključ (OpenVPN static key format: 256 bajtova heksadecimalno)
	tcKey := filepath.Join(dir, "tc.key")
	if _, err := os.Stat(tcKey); err != nil {
		raw := make([]byte, 256)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		var b strings.Builder
		b.WriteString("-----BEGIN OpenVPN Static key V1-----\n")
		h := hex.EncodeToString(raw)
		for i := 0; i < len(h); i += 32 {
			b.WriteString(h[i:i+32] + "\n")
		}
		b.WriteString("-----END OpenVPN Static key V1-----\n")
		if err := os.WriteFile(tcKey, []byte(b.String()), 0o600); err != nil {
			return err
		}
	}
	// popis opozvanih certifikata mora postojati prije nego ga server traži
	return s.writeOvpnCRL()
}

/* ---------- opoziv certifikata (CRL) ---------- */

// crlPath je popis opozvanih certifikata koji OpenVPN provjerava pri svakom
// spajanju. Brisanje CCD datoteke sprječava spajanje, ali certifikat ostaje
// kriptografski valjan — tek upis u CRL ga trajno poništava.
func (s *server) crlPath() string { return filepath.Join(s.ovpnDir(), "crl.pem") }

// revokeOvpnCert upisuje serijski broj certifikata u tablicu opozvanih.
// Certifikat se čita iz spremljenog PEM-a, pa nije potrebno pamtiti serijski
// broj pri izdavanju.
func (s *server) revokeOvpnCert(name, certPEM string) error {
	blk, _ := pem.Decode([]byte(certPEM))
	if blk == nil {
		return fmt.Errorf("certifikat korisnika %s nije čitljiv", name)
	}
	crt, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT OR REPLACE INTO ovpn_revoked
		(serial, name, not_after, revoked_at) VALUES (?,?,?,datetime('now'))`,
		crt.SerialNumber.Text(16), name, crt.NotAfter.UTC().Format(time.RFC3339))
	return err
}

// writeOvpnCRL ispisuje crl.pem iz tablice opozvanih certifikata.
// Rok valjanosti popisa je namjerno dug: OpenVPN odbija sve veze ako je CRL
// istekao, pa bi kratak rok značio samonametnuti ispad.
func (s *server) writeOvpnCRL() error {
	dir := s.ovpnDir()
	caPEM, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, "ca.key"))
	if err != nil {
		return err
	}
	cb, _ := pem.Decode(caPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		return fmt.Errorf("CA certifikat ili ključ nisu čitljivi")
	}
	caCrt, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return err
	}
	caKey, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return err
	}

	revoked := []x509.RevocationListEntry{}
	rows, err := s.db.Query(`SELECT serial, revoked_at, not_after FROM ovpn_revoked`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var serial, revAt, notAfter string
			if rows.Scan(&serial, &revAt, &notAfter) != nil {
				continue
			}
			// certifikat koji je ionako istekao ne treba držati u popisu
			if t, err := time.Parse(time.RFC3339, notAfter); err == nil &&
				t.Before(time.Now()) {
				continue
			}
			n, ok := new(big.Int).SetString(serial, 16)
			if !ok {
				continue
			}
			at, err := time.Parse("2006-01-02 15:04:05", revAt)
			if err != nil {
				at = time.Now()
			}
			revoked = append(revoked, x509.RevocationListEntry{
				SerialNumber: n, RevocationTime: at.UTC(),
			})
		}
	}

	tpl := &x509.RevocationList{
		Number:                    newSerial(),
		ThisUpdate:                time.Now().Add(-time.Hour),
		NextUpdate:                time.Now().AddDate(10, 0, 0),
		RevokedCertificateEntries: revoked,
	}
	der, err := x509.CreateRevocationList(rand.Reader, tpl, caCrt, caKey)
	if err != nil {
		return err
	}
	return writePEM(s.crlPath(), "X509 CRL", der, 0o644)
}

// ovpnSignCert izdaje certifikat s danim CN-om potpisan našim CA-om.
func (s *server) ovpnSignCert(cn string, eku x509.ExtKeyUsage) (certPEM, keyPEM string, err error) {
	dir := s.ovpnDir()
	caCrtB, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		return "", "", err
	}
	caKeyB, err := os.ReadFile(filepath.Join(dir, "ca.key"))
	if err != nil {
		return "", "", err
	}
	caBlock, _ := pem.Decode(caCrtB)
	keyBlock, _ := pem.Decode(caKeyB)
	if caBlock == nil || keyBlock == nil {
		return "", "", fmt.Errorf("CA nije čitljiv")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return "", "", err
	}
	caKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return "", "", err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	tpl := &x509.Certificate{
		SerialNumber: newSerial(),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{eku},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return "", "", err
	}
	kder, _ := x509.MarshalECPrivateKey(key)
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
	return certPEM, keyPEM, nil
}

/* ---------- pomoćne ---------- */

// ovpnServerNet čita mrežu tunela iz uci openvpn sekcije ("10.7.0.0 255.255.255.0").
func (s *server) ovpnServerNet(cfg map[string]uciSection) *net.IPNet {
	sec, ok := cfg[ovpnUciSection]
	if !ok {
		return nil
	}
	f := strings.Fields(sectStr(sec, "server"))
	if len(f) != 2 {
		return nil
	}
	ip := net.ParseIP(f[0])
	maskIP := net.ParseIP(f[1])
	if ip == nil || maskIP == nil || maskIP.To4() == nil {
		return nil
	}
	return &net.IPNet{IP: ip.Mask(net.IPMask(maskIP.To4())), Mask: net.IPMask(maskIP.To4())}
}

/* ---------- status ---------- */

type ovpnConnected struct {
	Name      string `json:"name"`
	RealAddr  string `json:"real_addr"`
	TunnelIP  string `json:"tunnel_ip"`
	RxBytes   int64  `json:"rx_bytes"`
	TxBytes   int64  `json:"tx_bytes"`
	SinceUnix int64  `json:"since"`
}

// parseOvpnStatus čita OpenVPN status datoteku (status-version 2, CSV format
// dokumentiran u openvpn(8)) — management sučelje ne izlažemo.
func parseOvpnStatus(path string) []ovpnConnected {
	out := []ovpnConnected{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, ",")
		if len(f) < 9 || f[0] != "CLIENT_LIST" {
			continue
		}
		rx, _ := strconv.ParseInt(f[5], 10, 64)
		tx, _ := strconv.ParseInt(f[6], 10, 64)
		since, _ := strconv.ParseInt(f[8], 10, 64)
		out = append(out, ovpnConnected{
			Name: f[1], RealAddr: f[2], TunnelIP: strings.Split(f[3], ":")[0],
			RxBytes: rx, TxBytes: tx, SinceUnix: since,
		})
	}
	return out
}

func (s *server) handleOvpnStatus(w http.ResponseWriter, r *http.Request) {
	_, lookErr := exec.LookPath("openvpn")
	cfg, err := uciGetConfig(r.Context(), "openvpn")
	if err != nil {
		cfg = map[string]uciSection{}
	}

	srv := map[string]any{"configured": false}
	if sec, ok := cfg[ovpnUciSection]; ok {
		network, nextIP := "", ""
		if n := s.ovpnServerNet(cfg); n != nil {
			network = n.String()
			// prijedlog za sljedećeg klijenta: prva slobodna adresa u tunelu
			nextIP = nextFreeTunnelIP(n, s.usedTunnelIPs("ovpn_clients"))
		}
		srv = map[string]any{
			"configured":     true,
			"next_tunnel_ip": nextIP,
			"port":           sectStr(sec, "port"),
			"proto":          sectStr(sec, "proto"),
			"network":        network,
			"endpoint_host":  s.getSetting("ovpn_endpoint_host", ""),
			"client_dns":     s.getSetting("ovpn_client_dns", ""),
			"push_lan":       s.getSetting("ovpn_push_lan", "1") == "1",
			"allow_mgmt":     s.getSetting("ovpn_allow_mgmt", "0") == "1",
			"pass_auth":      s.ovpnPassAuth(),
		}
	}

	// radi li servis (procd instanca kroz ubus)
	running := false
	var svc map[string]struct {
		Instances map[string]struct {
			Running bool `json:"running"`
		} `json:"instances"`
	}
	if err := ubusCallArg(r.Context(), "service", "list",
		`{"name":"openvpn"}`, &svc); err == nil {
		for _, x := range svc["openvpn"].Instances {
			if x.Running {
				running = true
			}
		}
	}

	accessMode := "restricted"
	if fwCfg, err := uciGetConfig(r.Context(), "firewall"); err == nil {
		if _, ok := fwCfg["sag_ovpn_lan"]; ok {
			accessMode = "full"
		} else if _, ok := fwCfg["sag_ovpn_wan"]; ok {
			accessMode = "full"
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"installed":   lookErr == nil,
		"server":      srv,
		"running":     running,
		"access_mode": accessMode,
		"connected":   parseOvpnStatus(ovpnStatusFile),
	})
}

/* ---------- postavke poslužitelja ---------- */

type ovpnServerIn struct {
	Port         int    `json:"port"`
	Network      string `json:"network"` // CIDR mreže tunela, npr. 10.7.0.0/24
	EndpointHost string `json:"endpoint_host"`
	ClientDNS    string `json:"client_dns"`
	PushLan      *bool  `json:"push_lan"`
	// AllowMgmt otvara upravljanje uređajem (SSH, LuCI, Saguaro) VPN
	// korisnicima. Zadano isključeno — VPN je pristup mreži, ne upravljanju.
	AllowMgmt bool `json:"allow_mgmt"`
	// PassAuth traži korisničko ime i lozinku uz certifikat.
	PassAuth bool `json:"pass_auth"`
}

func (s *server) handleOvpnServerSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in ovpnServerIn
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Port == 0 {
		in.Port = 1194
	}
	if in.Port < 1 || in.Port > 65535 {
		writeErr(w, http.StatusBadRequest, "neispravan port")
		return
	}
	if in.Network == "" {
		in.Network = "10.7.0.0/24"
	}
	_, ipnet, err := net.ParseCIDR(strings.TrimSpace(in.Network))
	if err != nil || ipnet.IP.To4() == nil {
		writeErr(w, http.StatusBadRequest,
			"mreža tunela mora biti IPv4 CIDR, npr. 10.7.0.0/24")
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

	if err := s.ensureOvpnPKI(); err != nil {
		writeErr(w, http.StatusInternalServerError, "PKI: "+err.Error())
		return
	}

	backups := []string{}
	for _, cfgPath := range []string{ovpnConfigFile, networkConfig, firewallConfig} {
		if _, err := os.Stat(cfgPath); err == nil {
			bn, err := s.backupConfig(cfgPath)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
				return
			}
			backups = append(backups, bn)
		}
	}

	dir := s.ovpnDir()
	mask := net.IP(ipnet.Mask).String()
	lanNet := ""
	if netCfg, err := uciGetConfig(ctx, "network"); err == nil {
		if lan, ok := netCfg["lan"]; ok {
			lanIP := net.ParseIP(sectStr(lan, "ipaddr"))
			lanMask := net.ParseIP(sectStr(lan, "netmask"))
			if lanIP != nil && lanMask != nil {
				m := net.IPMask(lanMask.To4())
				lanNet = lanIP.Mask(m).String() + " " + lanMask.String()
			}
		}
	}

	ovCfg, _ := uciGetConfig(ctx, "openvpn")
	var b strings.Builder
	fmt.Fprintf(&b, "set openvpn.%s=openvpn\n", ovpnUciSection)
	fmt.Fprintf(&b, "set openvpn.%s.enabled=1\n", ovpnUciSection)
	fmt.Fprintf(&b, "set openvpn.%s.dev=%s\n", ovpnUciSection, ovpnDev)
	fmt.Fprintf(&b, "set openvpn.%s.dev_type=tun\n", ovpnUciSection)
	fmt.Fprintf(&b, "set openvpn.%s.proto=udp\n", ovpnUciSection)
	fmt.Fprintf(&b, "set openvpn.%s.port=%d\n", ovpnUciSection, in.Port)
	fmt.Fprintf(&b, "set openvpn.%s.topology=subnet\n", ovpnUciSection)
	fmt.Fprintf(&b, "set openvpn.%s.server=%s\n", ovpnUciSection,
		uciQuote(ipnet.IP.String()+" "+mask))
	fmt.Fprintf(&b, "set openvpn.%s.ca=%s\n", ovpnUciSection, filepath.Join(dir, "ca.crt"))
	fmt.Fprintf(&b, "set openvpn.%s.cert=%s\n", ovpnUciSection, filepath.Join(dir, "server.crt"))
	fmt.Fprintf(&b, "set openvpn.%s.key=%s\n", ovpnUciSection, filepath.Join(dir, "server.key"))
	fmt.Fprintf(&b, "set openvpn.%s.dh=none\n", ovpnUciSection)
	fmt.Fprintf(&b, "set openvpn.%s.tls_crypt=%s\n", ovpnUciSection, filepath.Join(dir, "tc.key"))
	// izričito propisana kriptografija umjesto oslanjanja na zadane vrijednosti
	// data_ciphers je u OpenWrt init skripti list-opcija, pa ide add_list-om
	fmt.Fprintf(&b, "delete openvpn.%s.data_ciphers\n", ovpnUciSection)
	fmt.Fprintf(&b, "add_list openvpn.%s.data_ciphers=%s\n", ovpnUciSection,
		uciQuote("AES-256-GCM:CHACHA20-POLY1305"))
	fmt.Fprintf(&b, "set openvpn.%s.data_ciphers_fallback=AES-256-GCM\n", ovpnUciSection)
	fmt.Fprintf(&b, "set openvpn.%s.tls_version_min=1.2\n", ovpnUciSection)
	fmt.Fprintf(&b, "set openvpn.%s.remote_cert_tls=client\n", ovpnUciSection)
	// opoziv certifikata i odustajanje od root ovlasti nakon pokretanja
	fmt.Fprintf(&b, "set openvpn.%s.crl_verify=%s\n", ovpnUciSection, s.crlPath())
	// drugi faktor uz certifikat: korisničko ime i lozinka. Provjeru radi
	// pomoćna skripta, jer OpenVPN nema vlastitu bazu korisnika.
	if in.PassAuth {
		fmt.Fprintf(&b, "set openvpn.%s.auth_user_pass_verify=%s\n", ovpnUciSection,
			uciQuote(s.ovpnAuthScript()+" via-file"))
		fmt.Fprintf(&b, "set openvpn.%s.script_security=2\n", ovpnUciSection)
		// ime iz prijave postaje CN, pa se CCD datoteke i pravila po korisniku
		// i dalje poklapaju s nazivom klijenta
		fmt.Fprintf(&b, "set openvpn.%s.username_as_common_name=1\n", ovpnUciSection)
	} else {
		for _, o := range []string{"auth_user_pass_verify", "script_security",
			"username_as_common_name"} {
			if _, has := ovCfg[ovpnUciSection][o]; has {
				fmt.Fprintf(&b, "delete openvpn.%s.%s\n", ovpnUciSection, o)
			}
		}
	}
	fmt.Fprintf(&b, "set openvpn.%s.user=nobody\n", ovpnUciSection)
	fmt.Fprintf(&b, "set openvpn.%s.group=nogroup\n", ovpnUciSection)
	fmt.Fprintf(&b, "set openvpn.%s.keepalive=%s\n", ovpnUciSection, uciQuote("10 60"))
	fmt.Fprintf(&b, "set openvpn.%s.persist_key=1\n", ovpnUciSection)
	fmt.Fprintf(&b, "set openvpn.%s.persist_tun=1\n", ovpnUciSection)
	fmt.Fprintf(&b, "set openvpn.%s.client_config_dir=%s\n", ovpnUciSection, s.ccdDir())
	fmt.Fprintf(&b, "set openvpn.%s.ccd_exclusive=1\n", ovpnUciSection)
	fmt.Fprintf(&b, "set openvpn.%s.status=%s\n", ovpnUciSection, ovpnStatusFile)
	fmt.Fprintf(&b, "set openvpn.%s.status_version=2\n", ovpnUciSection)
	fmt.Fprintf(&b, "set openvpn.%s.verb=2\n", ovpnUciSection)
	fmt.Fprintf(&b, "delete openvpn.%s.push\n", ovpnUciSection)
	pushLan := in.PushLan == nil || *in.PushLan
	if pushLan && lanNet != "" {
		fmt.Fprintf(&b, "add_list openvpn.%s.push=%s\n", ovpnUciSection,
			uciQuote("route "+lanNet))
	}
	if in.ClientDNS != "" {
		fmt.Fprintf(&b, "add_list openvpn.%s.push=%s\n", ovpnUciSection,
			uciQuote("dhcp-option DNS "+in.ClientDNS))
	}
	b.WriteString("commit openvpn\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// mrežno sučelje + firewall zona (jednom; idempotentno)
	var nb strings.Builder
	fmt.Fprintf(&nb, "set network.%s=interface\n", ovpnIface)
	fmt.Fprintf(&nb, "set network.%s.proto=none\n", ovpnIface)
	fmt.Fprintf(&nb, "set network.%s.device=%s\n", ovpnIface, ovpnDev)
	nb.WriteString("commit network\n")
	if err := uciBatch(ctx, nb.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var fb strings.Builder
	fb.WriteString("set firewall.sag_ovpn_zone=zone\n")
	fb.WriteString("set firewall.sag_ovpn_zone.name=sagovpn\n")
	fb.WriteString("delete firewall.sag_ovpn_zone.network\n")
	fmt.Fprintf(&fb, "add_list firewall.sag_ovpn_zone.network=%s\n", ovpnIface)
	fb.WriteString("set firewall.sag_ovpn_zone.output=ACCEPT\n")
	fb.WriteString("set firewall.sag_ovpn_zone.forward=REJECT\n")
	fb.WriteString("set firewall.sag_ovpn_rule=rule\n")
	fb.WriteString("set firewall.sag_ovpn_rule.name=Saguaro-OpenVPN\n")
	fb.WriteString("set firewall.sag_ovpn_rule.src=wan\n")
	fb.WriteString("set firewall.sag_ovpn_rule.proto=udp\n")
	fmt.Fprintf(&fb, "set firewall.sag_ovpn_rule.dest_port=%d\n", in.Port)
	fb.WriteString("set firewall.sag_ovpn_rule.target=ACCEPT\n")
	// pun pristup pri prvom postavljanju (kao WireGuard); mijenja se kroz /access
	fwCfg, _ := uciGetConfig(ctx, "firewall")
	vpnZoneInput(&fb, fwCfg, "sag_ovpn", "sagovpn", "OpenVPN", in.AllowMgmt)
	_, hasLan := fwCfg["sag_ovpn_lan"]
	_, hasWan := fwCfg["sag_ovpn_wan"]
	_, hadZone := fwCfg["sag_ovpn_zone"]
	if !hadZone || hasLan || hasWan {
		fb.WriteString("set firewall.sag_ovpn_lan=forwarding\n")
		fb.WriteString("set firewall.sag_ovpn_lan.src=sagovpn\n")
		fb.WriteString("set firewall.sag_ovpn_lan.dest=lan\n")
		fb.WriteString("set firewall.sag_ovpn_wan=forwarding\n")
		fb.WriteString("set firewall.sag_ovpn_wan.src=sagovpn\n")
		fb.WriteString("set firewall.sag_ovpn_wan.dest=wan\n")
	}
	fb.WriteString("commit firewall\n")
	if err := uciBatch(ctx, fb.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	for k, v := range map[string]string{
		"ovpn_endpoint_host": in.EndpointHost,
		"ovpn_client_dns":    in.ClientDNS,
		"ovpn_push_lan":      map[bool]string{true: "1", false: "0"}[pushLan],
		"ovpn_allow_mgmt":    boolSetting(in.AllowMgmt),
		"ovpn_pass_auth":     boolSetting(in.PassAuth),
	} {
		if err := s.setSetting(k, v); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	if err := serviceReload(ctx, "openvpn", "enable"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, svc := range [][2]string{{"firewall", "reload"}, {"network", "reload"},
		{"openvpn", "restart"}} {
		if err := serviceReload(ctx, svc[0], svc[1]); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": true, "backups": backups})
}

// handleOvpnAccessSet — pun/ograničen pristup, isti model kao WireGuard.
func (s *server) handleOvpnAccessSet(w http.ResponseWriter, r *http.Request) {
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
		b.WriteString("set firewall.sag_ovpn_lan=forwarding\n")
		b.WriteString("set firewall.sag_ovpn_lan.src=sagovpn\n")
		b.WriteString("set firewall.sag_ovpn_lan.dest=lan\n")
		b.WriteString("set firewall.sag_ovpn_wan=forwarding\n")
		b.WriteString("set firewall.sag_ovpn_wan.src=sagovpn\n")
		b.WriteString("set firewall.sag_ovpn_wan.dest=wan\n")
	} else {
		for _, sect := range []string{"sag_ovpn_lan", "sag_ovpn_wan"} {
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
	writeJSON(w, http.StatusOK, map[string]any{"mode": in.Mode, "backup": backupName})
}

/* ---------- klijenti ---------- */

type OvpnClient struct {
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	TunnelIP  string `json:"tunnel_ip"`
	Enabled   bool   `json:"enabled"`
	Notes     string `json:"notes"`
	HasPass   bool   `json:"has_pass"` // je li postavljena lozinka (sama se ne vraća)
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// certifikat i ključ se ne vraćaju kroz popis — samo kroz /config export
const ovpnCols = `uuid, name, tunnel_ip, enabled, COALESCE(notes,''),
	COALESCE(pass_hash,'') <> '', created_at, updated_at`

func scanOvpnClient(row interface{ Scan(...any) error }) (OvpnClient, error) {
	var c OvpnClient
	err := row.Scan(&c.UUID, &c.Name, &c.TunnelIP, &c.Enabled, &c.Notes,
		&c.HasPass, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func (s *server) handleOvpnClientList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT ` + ovpnCols + ` FROM ovpn_clients ORDER BY tunnel_ip`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	out := []OvpnClient{}
	for rows.Next() {
		c, err := scanOvpnClient(rows)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"clients": out})
}

type ovpnClientIn struct {
	OvpnClient
	Enabled *bool `json:"enabled"`
	// prazno pri izmjeni = zadrži postojeću lozinku
	Password string `json:"password"`
	// izričito uklanjanje lozinke (npr. privremeno blokiranje korisnika)
	ClearPassword bool `json:"clear_password"`
}

// ovpnPassHash provjeri i pretvori lozinku u otisak; prazna lozinka vraća "".
func ovpnPassHash(w http.ResponseWriter, pw string) (string, bool) {
	if pw == "" {
		return "", true
	}
	if len([]rune(pw)) < 8 {
		writeErr(w, http.StatusBadRequest,
			"lozinka VPN korisnika mora imati bar 8 znakova")
		return "", false
	}
	if hasCtrl(pw) {
		writeErr(w, http.StatusBadRequest,
			"lozinka ne smije sadržavati prijelom retka")
		return "", false
	}
	h, err := hashPassword(pw)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return "", false
	}
	return h, true
}

func (s *server) validateOvpnClient(w http.ResponseWriter, c *OvpnClient, uuid string) bool {
	c.Name = strings.ToLower(strings.TrimSpace(c.Name))
	c.TunnelIP = strings.TrimSpace(c.TunnelIP)
	if !reClientName.MatchString(c.Name) {
		writeErr(w, http.StatusBadRequest,
			"naziv: 2-31 malih slova/znamenki/crtica, počinje slovom")
		return false
	}
	ip := net.ParseIP(c.TunnelIP)
	if ip == nil || ip.To4() == nil {
		writeErr(w, http.StatusBadRequest, "neispravna adresa u tunelu")
		return false
	}
	cfg, err := uciGetConfig(context.Background(), "openvpn")
	if err == nil {
		n := s.ovpnServerNet(cfg)
		if n != nil && !n.Contains(ip) {
			writeErr(w, http.StatusBadRequest,
				"adresa "+c.TunnelIP+" nije u mreži tunela "+n.String())
			return false
		}
		if tunnelIPReserved(n, ip) {
			writeErr(w, http.StatusBadRequest, "adresa "+c.TunnelIP+
				" je rezervirana (mrežna adresa, adresa uređaja u tunelu ili broadcast)")
			return false
		}
	}
	// dvije iste adrese u tunelu = promet jednog korisnika ide drugome
	var other string
	err = s.db.QueryRow(`SELECT name FROM ovpn_clients WHERE tunnel_ip = ? AND uuid <> ?`,
		c.TunnelIP, uuid).Scan(&other)
	if err == nil {
		writeErr(w, http.StatusConflict,
			"adresu "+c.TunnelIP+" već koristi klijent "+other)
		return false
	}
	return true
}

func (s *server) handleOvpnClientCreate(w http.ResponseWriter, r *http.Request) {
	var in ovpnClientIn
	if !decodeBody(w, r, &in) {
		return
	}
	c := &in.OvpnClient
	if !s.validateOvpnClient(w, c, "") {
		return
	}
	if err := s.ensureOvpnPKI(); err != nil {
		writeErr(w, http.StatusInternalServerError, "PKI: "+err.Error())
		return
	}
	certPEM, keyPEM, err := s.ovpnSignCert(c.Name, x509.ExtKeyUsageClientAuth)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	passHash, okPw := ovpnPassHash(w, in.Password)
	if !okPw {
		return
	}
	if passHash == "" && s.ovpnPassAuth() {
		writeErr(w, http.StatusBadRequest,
			"poslužitelj traži lozinku uz certifikat — upiši lozinku za ovog korisnika")
		return
	}
	c.UUID = newUUID()
	_, err = s.db.Exec(`INSERT INTO ovpn_clients
		(uuid, name, cert_pem, key_pem, tunnel_ip, pass_hash, enabled, notes)
		VALUES (?,?,?,?,?,?,?,?)`,
		c.UUID, c.Name, certPEM, keyPEM, c.TunnelIP, passHash,
		enabledIntOf(in.Enabled), c.Notes)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict,
				"klijent s tim nazivom ili adresom već postoji")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cc, _ := scanOvpnClient(s.db.QueryRow(
		`SELECT `+ovpnCols+` FROM ovpn_clients WHERE uuid=?`, c.UUID))
	writeJSON(w, http.StatusCreated, cc)
}

func (s *server) handleOvpnClientUpdate(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	var in ovpnClientIn
	if !decodeBody(w, r, &in) {
		return
	}
	c := &in.OvpnClient
	// naziv je CN certifikata i ne mijenja se; mijenjaju se adresa/stanje/napomene
	var name string
	if err := s.db.QueryRow(`SELECT name FROM ovpn_clients WHERE uuid=?`,
		uuid).Scan(&name); err != nil {
		writeErr(w, http.StatusNotFound, "klijent ne postoji")
		return
	}
	c.Name = name
	if !s.validateOvpnClient(w, c, uuid) {
		return
	}
	passHash, okPw := ovpnPassHash(w, in.Password)
	if !okPw {
		return
	}
	var err error
	if in.ClearPassword {
		_, err = s.db.Exec(`UPDATE ovpn_clients SET tunnel_ip=?, enabled=?, notes=?,
			pass_hash=NULL, updated_at=datetime('now') WHERE uuid=?`,
			c.TunnelIP, enabledIntOf(in.Enabled), c.Notes, uuid)
	} else if passHash == "" { // prazno = zadrži postojeću lozinku
		_, err = s.db.Exec(`UPDATE ovpn_clients SET tunnel_ip=?, enabled=?, notes=?,
			updated_at=datetime('now') WHERE uuid=?`,
			c.TunnelIP, enabledIntOf(in.Enabled), c.Notes, uuid)
	} else {
		_, err = s.db.Exec(`UPDATE ovpn_clients SET tunnel_ip=?, enabled=?, notes=?,
			pass_hash=?, updated_at=datetime('now') WHERE uuid=?`,
			c.TunnelIP, enabledIntOf(in.Enabled), c.Notes, passHash, uuid)
	}
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "klijent s tom adresom već postoji")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cc, _ := scanOvpnClient(s.db.QueryRow(
		`SELECT `+ovpnCols+` FROM ovpn_clients WHERE uuid=?`, uuid))
	writeJSON(w, http.StatusOK, cc)
}

func (s *server) handleOvpnClientDelete(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	// certifikat se prije brisanja upisuje u popis opozvanih (CRL)
	var name, certPEM string
	if err := s.db.QueryRow(`SELECT name, cert_pem FROM ovpn_clients WHERE uuid=?`,
		uuid).Scan(&name, &certPEM); err != nil {
		writeErr(w, http.StatusNotFound, "klijent ne postoji")
		return
	}
	if err := s.revokeOvpnCert(name, certPEM); err != nil {
		writeErr(w, http.StatusInternalServerError, "opoziv: "+err.Error())
		return
	}
	if _, err := s.db.Exec(`DELETE FROM ovpn_clients WHERE uuid=?`, uuid); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	revoked := true
	if err := s.writeOvpnCRL(); err != nil {
		// baza je već ažurirana; CRL se ponovno ispisuje pri sljedećem "Primijeni"
		addEvent(s, "warning", "Popis opozvanih certifikata nije osvježen: "+err.Error())
		revoked = false
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": uuid, "revoked": revoked,
	})
}

/* ---------- .ovpn export ---------- */

func (s *server) handleOvpnClientConfig(w http.ResponseWriter, r *http.Request) {
	var name, certPEM, keyPEM string
	err := s.db.QueryRow(`SELECT name, cert_pem, key_pem FROM ovpn_clients
		WHERE uuid=?`, r.PathValue("uuid")).Scan(&name, &certPEM, &keyPEM)
	if err != nil {
		writeErr(w, http.StatusNotFound, "klijent ne postoji")
		return
	}
	cfg, err := uciGetConfig(r.Context(), "openvpn")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	sec, ok := cfg[ovpnUciSection]
	if !ok {
		writeErr(w, http.StatusConflict, "OpenVPN poslužitelj još nije postavljen")
		return
	}
	port := sectStr(sec, "port")
	caB, err1 := os.ReadFile(filepath.Join(s.ovpnDir(), "ca.crt"))
	tcB, err2 := os.ReadFile(filepath.Join(s.ovpnDir(), "tc.key"))
	if err1 != nil || err2 != nil {
		writeErr(w, http.StatusInternalServerError, "PKI datoteke nedostupne")
		return
	}
	endpoint := s.getSetting("ovpn_endpoint_host", "")
	if endpoint == "" {
		if netCfg, err := uciGetConfig(r.Context(), "network"); err == nil {
			if lan, ok := netCfg["lan"]; ok {
				endpoint = sectStr(lan, "ipaddr")
			}
		}
	}

	body := s.buildOvpnClientConfig(endpoint, port, string(caB), certPEM,
		keyPEM, string(tcB))
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "config": body})
}

// buildOvpnClientConfig slaže .ovpn datoteku. Koriste je i sučelje i izvoz iz
// naredbenog retka, pa je sadržaj zajamčeno isti.
func (s *server) buildOvpnClientConfig(endpoint, port, ca, cert, key, tc string) string {
	var b strings.Builder
	b.WriteString("client\ndev tun\nproto udp\n")
	fmt.Fprintf(&b, "remote %s %s\n", endpoint, port)
	b.WriteString("resolv-retry infinite\nnobind\npersist-key\npersist-tun\n")
	b.WriteString("remote-cert-tls server\nverb 3\n")
	// ista propisana kriptografija kao na serveru
	b.WriteString("data-ciphers AES-256-GCM:CHACHA20-POLY1305\n")
	b.WriteString("tls-version-min 1.2\nauth-nocache\n")
	if s.ovpnPassAuth() {
		// aplikacija će pri spajanju zatražiti korisničko ime i lozinku
		b.WriteString("auth-user-pass\n")
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "<ca>\n%s</ca>\n", ca)
	fmt.Fprintf(&b, "<cert>\n%s</cert>\n", cert)
	fmt.Fprintf(&b, "<key>\n%s</key>\n", key)
	fmt.Fprintf(&b, "<tls-crypt>\n%s</tls-crypt>\n", tc)
	return b.String()
}

/* ---------- pristupna pravila po klijentu ---------- */

func (s *server) handleOvpnClientRuleList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT uuid, client_uuid, dest_zone,
		COALESCE(dest_ip,''), COALESCE(dest_port,''), proto, enabled,
		COALESCE(notes,''), created_at, updated_at
		FROM ovpn_client_rules WHERE client_uuid=? ORDER BY created_at`,
		r.PathValue("uuid"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	out := []WGPeerRule{}
	for rows.Next() {
		var x WGPeerRule
		if err := rows.Scan(&x.UUID, &x.PeerUUID, &x.DestZone, &x.DestIP,
			&x.DestPort, &x.Proto, &x.Enabled, &x.Notes,
			&x.CreatedAt, &x.UpdatedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": out})
}

func (s *server) handleOvpnClientRuleCreate(w http.ResponseWriter, r *http.Request) {
	clientUUID := r.PathValue("uuid")
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ovpn_clients WHERE uuid=?`,
		clientUUID).Scan(&n); err != nil || n == 0 {
		writeErr(w, http.StatusNotFound, "klijent ne postoji")
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
	_, err := s.db.Exec(`INSERT INTO ovpn_client_rules
		(uuid, client_uuid, dest_zone, dest_ip, dest_port, proto, enabled, notes)
		VALUES (?,?,?,?,?,?,?,?)`,
		x.UUID, clientUUID, x.DestZone, x.DestIP, x.DestPort, x.Proto,
		enabledIntOf(in.Enabled), x.Notes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"created": x.UUID})
}

func (s *server) handleOvpnRuleDelete(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.Exec(`DELETE FROM ovpn_client_rules WHERE uuid=?`, r.PathValue("uuid"))
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

/* ---------- primjena ---------- */

// handleOvpnApply sinkronizira CCD datoteke (fiksne adrese; ccd-exclusive
// znači da isključeni klijent gubi mogućnost spajanja) i sag_or_* pravila.
func (s *server) handleOvpnApply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ovpnCfg, err := uciGetConfig(ctx, "openvpn")
	if err != nil || ovpnCfg[ovpnUciSection] == nil {
		writeErr(w, http.StatusConflict, "prvo spremi postavke poslužitelja")
		return
	}
	ipnet := s.ovpnServerNet(ovpnCfg)
	if ipnet == nil {
		writeErr(w, http.StatusConflict, "mreža tunela nije čitljiva iz konfiguracije")
		return
	}
	// popis korisnika i pomoćna skripta za provjeru lozinke
	if err := s.writeOvpnAuthScript(); err != nil {
		writeErr(w, http.StatusInternalServerError, "auth skripta: "+err.Error())
		return
	}
	if err := s.writeOvpnUsers(); err != nil {
		writeErr(w, http.StatusInternalServerError, "popis korisnika: "+err.Error())
		return
	}
	mask := net.IP(ipnet.Mask).String()

	rows, err := s.db.Query(`SELECT name, tunnel_ip FROM ovpn_clients
		WHERE enabled = 1 ORDER BY tunnel_ip`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type cl struct{ name, ip string }
	clients := []cl{}
	for rows.Next() {
		var c cl
		if err := rows.Scan(&c.name, &c.ip); err != nil {
			rows.Close()
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		clients = append(clients, c)
	}
	rows.Close()

	// CCD: obriši sve postojeće pa zapiši aktivne
	if err := os.RemoveAll(s.ccdDir()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.MkdirAll(s.ccdDir(), 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, c := range clients {
		line := fmt.Sprintf("ifconfig-push %s %s\n", c.ip, mask)
		if err := os.WriteFile(filepath.Join(s.ccdDir(), c.name),
			[]byte(line), 0o644); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	// pristupna pravila -> sag_or_* firewall sekcije
	type vr struct{ uuid, ip, zone, dip, dport, proto string }
	vrRows, err := s.db.Query(`SELECT r.uuid, c.tunnel_ip, r.dest_zone,
		COALESCE(r.dest_ip,''), COALESCE(r.dest_port,''), r.proto
		FROM ovpn_client_rules r JOIN ovpn_clients c ON c.uuid = r.client_uuid
		WHERE r.enabled = 1 AND c.enabled = 1 ORDER BY c.tunnel_ip`)
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

	backupName, err := s.backupConfig(firewallConfig)
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
	removed := 0
	for name, sec := range fwCfg {
		if strings.HasPrefix(name, orPrefix) && sectStr(sec, ".type") == "rule" {
			fmt.Fprintf(&fb, "delete firewall.%s\n", name)
			removed++
		}
	}
	for _, x := range vrs {
		sn := orPrefix + strings.ReplaceAll(x.uuid, "-", "")[:8]
		fmt.Fprintf(&fb, "set firewall.%s=rule\n", sn)
		fmt.Fprintf(&fb, "set firewall.%s.name=%s\n", sn, uciQuote("OVPN "+x.ip+" -> "+x.zone))
		fmt.Fprintf(&fb, "set firewall.%s.src=sagovpn\n", sn)
		fmt.Fprintf(&fb, "set firewall.%s.src_ip=%s\n", sn, x.ip)
		if x.zone != "" {
			fmt.Fprintf(&fb, "set firewall.%s.dest=%s\n", sn, x.zone)
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
		"applied_clients": len(clients),
		"applied_rules":   len(vrs),
		"removed_rules":   removed,
		"backup":          backupName,
	})
}

/* ---------- korisničko ime i lozinka (drugi faktor uz certifikat) ---------- */

// Certifikat sam po sebi je "nešto što imaš" — tko dobije .ovpn datoteku,
// spojio se. Lozinka dodaje "nešto što znaš", pa ukradena datoteka više nije
// dovoljna. OpenVPN provjeru prepušta vanjskoj skripti (auth-user-pass-verify).
//
// Zamka: OpenVPN nakon pokretanja odustaje od root ovlasti, pa skripta radi
// kao nobody i ne može čitati Saguaro bazu (0600 root). Zato se otisci lozinki
// ispisuju u zasebnu datoteku čitljivu grupi nogroup — otisci, nikad lozinke.
func (s *server) ovpnUsersFile() string {
	return filepath.Join(s.ovpnDir(), "users")
}

func (s *server) ovpnAuthScript() string {
	return filepath.Join(s.ovpnDir(), "authverify.sh")
}

// writeOvpnUsers ispisuje "ime:otisak" za svakog uključenog klijenta s
// lozinkom. Klijenti bez lozinke se izostavljaju — kad je provjera uključena,
// oni se ne mogu prijaviti dok im se lozinka ne postavi.
func (s *server) writeOvpnUsers() error {
	rows, err := s.db.Query(`SELECT name, COALESCE(pass_hash,'') FROM ovpn_clients
		WHERE enabled = 1 AND COALESCE(pass_hash,'') <> '' ORDER BY name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var name, hash string
		if rows.Scan(&name, &hash) != nil {
			continue
		}
		b.WriteString(name + ":" + hash + "\n")
	}
	p := s.ovpnUsersFile()
	if err := os.WriteFile(p, []byte(b.String()), 0o640); err != nil {
		return err
	}
	// nogroup (65534) je grupa u koju OpenVPN prelazi nakon pokretanja
	_ = os.Chown(p, 0, 65534)
	return nil
}

// writeOvpnAuthScript stvara pomoćnu skriptu koju OpenVPN zove pri svakoj
// prijavi. Sama provjera otiska je u Saguaro binaryju (-ovpn-auth), jer je
// PBKDF2 u shellu neizvediv.
func (s *server) writeOvpnAuthScript() error {
	body := "#!/bin/sh\n" +
		"# Saguaro — provjera korisničkog imena i lozinke za OpenVPN.\n" +
		"# OpenVPN preda datoteku s imenom u prvom i lozinkom u drugom retku.\n" +
		"exec /opt/saguaro/bin/saguaro-core -ovpn-auth \"$1\" -ovpn-users " +
		s.ovpnUsersFile() + "\n"
	p := s.ovpnAuthScript()
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		return err
	}
	return nil
}

// verifyOvpnUser provjeri par ime/lozinka protiv datoteke s otiscima.
// Namjerno ne dira bazu: poziva se iz procesa koji radi kao nobody.
func verifyOvpnUser(credFile, usersFile string) error {
	raw, err := os.ReadFile(credFile)
	if err != nil {
		return fmt.Errorf("ne mogu pročitati podatke za prijavu: %w", err)
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	if len(lines) < 2 {
		return fmt.Errorf("neispravan zapis prijave")
	}
	user, pass := strings.TrimSpace(lines[0]), lines[1]

	ub, err := os.ReadFile(usersFile)
	if err != nil {
		return fmt.Errorf("popis korisnika nije dostupan: %w", err)
	}
	for _, l := range strings.Split(string(ub), "\n") {
		name, hash, ok := strings.Cut(strings.TrimSpace(l), ":")
		if !ok || name != user {
			continue
		}
		if verifyPassword(hash, pass) {
			return nil
		}
		return fmt.Errorf("pogrešna lozinka")
	}
	// isti trošak i za nepostojećeg korisnika, da se po trajanju ne može
	// zaključiti postoji li ime
	verifyPassword(dummyHash, pass)
	return fmt.Errorf("korisnik ne postoji ili nema postavljenu lozinku")
}

// ovpnPassAuth javlja traži li poslužitelj i lozinku uz certifikat.
func (s *server) ovpnPassAuth() bool {
	return s.getSetting("ovpn_pass_auth", "0") == "1"
}

// exportOvpnConfig ispisuje .ovpn datoteku za klijenta iz naredbenog retka.
// Korisno za skriptiranu isporuku i za provjeru bez preglednika; sadržaj je
// isti kao onaj koji nudi sučelje.
func (s *server) exportOvpnConfig(name, outPath string) error {
	var certPEM, keyPEM string
	if err := s.db.QueryRow(`SELECT cert_pem, key_pem FROM ovpn_clients
		WHERE name=?`, name).Scan(&certPEM, &keyPEM); err != nil {
		return fmt.Errorf("klijent %q ne postoji", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cfg, err := uciGetConfig(ctx, "openvpn")
	if err != nil {
		return err
	}
	sec, ok := cfg[ovpnUciSection]
	if !ok {
		return fmt.Errorf("OpenVPN poslužitelj još nije postavljen")
	}
	caB, err := os.ReadFile(filepath.Join(s.ovpnDir(), "ca.crt"))
	if err != nil {
		return err
	}
	tcB, err := os.ReadFile(filepath.Join(s.ovpnDir(), "tc.key"))
	if err != nil {
		return err
	}
	endpoint := s.getSetting("ovpn_endpoint_host", "")
	if endpoint == "" {
		if netCfg, nerr := uciGetConfig(ctx, "network"); nerr == nil {
			endpoint = sectStr(netCfg["lan"], "ipaddr")
		}
	}
	body := s.buildOvpnClientConfig(endpoint, sectStr(sec, "port"),
		string(caB), certPEM, keyPEM, string(tcB))
	if outPath == "" || outPath == "-" {
		fmt.Print(body)
		return nil
	}
	return os.WriteFile(outPath, []byte(body), 0o600)
}
