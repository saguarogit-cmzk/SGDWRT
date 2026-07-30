// saguaro-core — API servis Saguaro platforme na OpenWrt uređaju.
// Čita stanje sustava isključivo preko ubus-a (D-007) i servira REST API v1.
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const version = "0.11.0"

type server struct {
	tokenMu   sync.RWMutex
	token     string // API token za strojni pristup; GUI koristi sesije (auth.go)
	webDir    string
	etcDir    string
	dataDir   string
	backupDir string
	started   time.Time
	db        *sql.DB
}

func main() {
	listen := flag.String("listen", ":8443", "adresa:port za slušanje")
	etcDir := flag.String("etc", "/opt/saguaro/etc", "direktorij konfiguracije (token, certifikat)")
	webDir := flag.String("web", "/opt/saguaro/web", "direktorij statičkog weba")
	dataDir := flag.String("data", "/opt/saguaro/data", "direktorij podataka (SQLite)")
	backupDir := flag.String("backup", "/opt/saguaro/backup", "direktorij backupa konfiguracija")
	noTLS := flag.Bool("no-tls", false, "posluži bez TLS-a (samo za razvoj)")
	flag.Parse()

	if err := os.MkdirAll(*etcDir, 0o755); err != nil {
		log.Fatalf("etc direktorij: %v", err)
	}
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("data direktorij: %v", err)
	}
	if err := os.MkdirAll(*backupDir, 0o700); err != nil {
		log.Fatalf("backup direktorij: %v", err)
	}
	token, err := ensureToken(filepath.Join(*etcDir, "token"))
	if err != nil {
		log.Fatalf("token: %v", err)
	}
	db, err := openDB(filepath.Join(*dataDir, "saguaro.db"))
	if err != nil {
		log.Fatalf("baza: %v", err)
	}
	defer db.Close()

	s := &server{token: token, webDir: *webDir, etcDir: *etcDir,
		dataDir: *dataDir, backupDir: *backupDir, started: time.Now(), db: db}
	if err := s.ensureAdmin(token); err != nil {
		log.Fatalf("admin korisnik: %v", err)
	}

	{
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if err := s.ensureSelf(ctx); err != nil {
			// bez ubus-a (npr. razvoj na radnoj stanici) samoregistracija ne prolazi;
			// servis ipak kreće, zapis se popuni pri sljedećem startu na uređaju
			log.Printf("upozorenje: samoregistracija u inventory nije uspjela: %v", err)
		}
		cancel()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.Handle("POST /api/v1/auth/logout", s.auth(s.handleLogout))
	mux.Handle("POST /api/v1/auth/logout-others", s.auth(s.handleLogoutOthers))
	mux.Handle("GET /api/v1/auth/session", s.auth(s.handleSessionInfo))
	mux.Handle("POST /api/v1/auth/password", s.auth(s.handlePasswordChange))
	mux.Handle("GET /api/v1/settings/token", s.auth(s.handleTokenGet))
	mux.Handle("POST /api/v1/settings/token/regenerate", s.auth(s.handleTokenRegen))
	mux.Handle("POST /api/v1/dhcp/server", s.auth(s.handleDHCPServerSet))
	mux.Handle("GET /api/v1/system", s.auth(s.handleSystem))
	mux.Handle("GET /api/v1/system/status", s.auth(s.handleStatus))
	mux.Handle("GET /api/v1/storage", s.auth(s.handleStorage))
	mux.Handle("GET /api/v1/interfaces", s.auth(s.handleInterfaces))
	mux.Handle("GET /api/v1/identity", s.auth(s.handleIdentity))
	mux.Handle("GET /api/v1/inventory/devices", s.auth(s.handleDeviceList))
	mux.Handle("POST /api/v1/inventory/devices", s.auth(s.handleDeviceCreate))
	mux.Handle("GET /api/v1/inventory/devices/{uuid}", s.auth(s.handleDeviceGet))
	mux.Handle("PUT /api/v1/inventory/devices/{uuid}", s.auth(s.handleDeviceUpdate))
	mux.Handle("DELETE /api/v1/inventory/devices/{uuid}", s.auth(s.handleDeviceDelete))
	mux.Handle("GET /api/v1/inventory/hosts", s.auth(s.handleHostList))
	mux.Handle("POST /api/v1/inventory/hosts", s.auth(s.handleHostCreate))
	mux.Handle("PUT /api/v1/inventory/hosts/{uuid}", s.auth(s.handleHostUpdate))
	mux.Handle("DELETE /api/v1/inventory/hosts/{uuid}", s.auth(s.handleHostDelete))
	mux.Handle("GET /api/v1/dhcp/status", s.auth(s.handleDHCPStatus))
	mux.Handle("POST /api/v1/dhcp/apply", s.auth(s.handleDHCPApply))
	mux.Handle("GET /api/v1/dns/status", s.auth(s.handleDNSStatus))
	mux.Handle("GET /api/v1/dns/records", s.auth(s.handleDNSRecordList))
	mux.Handle("POST /api/v1/dns/records", s.auth(s.handleDNSRecordCreate))
	mux.Handle("PUT /api/v1/dns/records/{uuid}", s.auth(s.handleDNSRecordUpdate))
	mux.Handle("DELETE /api/v1/dns/records/{uuid}", s.auth(s.handleDNSRecordDelete))
	mux.Handle("POST /api/v1/dns/apply", s.auth(s.handleDNSApply))
	mux.Handle("GET /api/v1/wireguard/status", s.auth(s.handleWGStatus))
	mux.Handle("POST /api/v1/wireguard/server", s.auth(s.handleWGServerSet))
	mux.Handle("GET /api/v1/wireguard/peers", s.auth(s.handleWGPeerList))
	mux.Handle("POST /api/v1/wireguard/peers", s.auth(s.handleWGPeerCreate))
	mux.Handle("PUT /api/v1/wireguard/peers/{uuid}", s.auth(s.handleWGPeerUpdate))
	mux.Handle("DELETE /api/v1/wireguard/peers/{uuid}", s.auth(s.handleWGPeerDelete))
	mux.Handle("GET /api/v1/wireguard/peers/{uuid}/config", s.auth(s.handleWGPeerConfig))
	mux.Handle("GET /api/v1/wireguard/peers/{uuid}/rules", s.auth(s.handleWGPeerRuleList))
	mux.Handle("POST /api/v1/wireguard/peers/{uuid}/rules", s.auth(s.handleWGPeerRuleCreate))
	mux.Handle("DELETE /api/v1/wireguard/rules/{uuid}", s.auth(s.handleWGRuleDelete))
	mux.Handle("POST /api/v1/wireguard/access", s.auth(s.handleWGAccessSet))
	mux.Handle("POST /api/v1/wireguard/apply", s.auth(s.handleWGApply))
	mux.Handle("GET /api/v1/openvpn/status", s.auth(s.handleOvpnStatus))
	mux.Handle("POST /api/v1/openvpn/server", s.auth(s.handleOvpnServerSet))
	mux.Handle("POST /api/v1/openvpn/access", s.auth(s.handleOvpnAccessSet))
	mux.Handle("GET /api/v1/openvpn/clients", s.auth(s.handleOvpnClientList))
	mux.Handle("POST /api/v1/openvpn/clients", s.auth(s.handleOvpnClientCreate))
	mux.Handle("PUT /api/v1/openvpn/clients/{uuid}", s.auth(s.handleOvpnClientUpdate))
	mux.Handle("DELETE /api/v1/openvpn/clients/{uuid}", s.auth(s.handleOvpnClientDelete))
	mux.Handle("GET /api/v1/openvpn/clients/{uuid}/config", s.auth(s.handleOvpnClientConfig))
	mux.Handle("GET /api/v1/openvpn/clients/{uuid}/rules", s.auth(s.handleOvpnClientRuleList))
	mux.Handle("POST /api/v1/openvpn/clients/{uuid}/rules", s.auth(s.handleOvpnClientRuleCreate))
	mux.Handle("DELETE /api/v1/openvpn/rules/{uuid}", s.auth(s.handleOvpnRuleDelete))
	mux.Handle("POST /api/v1/openvpn/apply", s.auth(s.handleOvpnApply))
	mux.Handle("GET /api/v1/network/lan", s.auth(s.handleNetworkLanGet))
	mux.Handle("POST /api/v1/network/lan", s.auth(s.handleNetworkLanSet))
	mux.Handle("GET /api/v1/firewall/status", s.auth(s.handleFWStatus))
	mux.Handle("GET /api/v1/firewall/forwards", s.auth(s.handleFWForwardList))
	mux.Handle("POST /api/v1/firewall/forwards", s.auth(s.handleFWForwardCreate))
	mux.Handle("PUT /api/v1/firewall/forwards/{uuid}", s.auth(s.handleFWForwardUpdate))
	mux.Handle("DELETE /api/v1/firewall/forwards/{uuid}", s.auth(s.handleFWForwardDelete))
	mux.Handle("GET /api/v1/firewall/rules", s.auth(s.handleFWRuleList))
	mux.Handle("POST /api/v1/firewall/rules", s.auth(s.handleFWRuleCreate))
	mux.Handle("PUT /api/v1/firewall/rules/{uuid}", s.auth(s.handleFWRuleUpdate))
	mux.Handle("DELETE /api/v1/firewall/rules/{uuid}", s.auth(s.handleFWRuleDelete))
	mux.Handle("POST /api/v1/firewall/apply", s.auth(s.handleFWApply))
	mux.Handle("GET /api/v1/firewall/dmz", s.auth(s.handleDMZGet))
	mux.Handle("POST /api/v1/firewall/dmz", s.auth(s.handleDMZSet))
	mux.Handle("GET /api/v1/firewall/nat11", s.auth(s.handleNAT11List))
	mux.Handle("POST /api/v1/firewall/nat11", s.auth(s.handleNAT11Create))
	mux.Handle("PUT /api/v1/firewall/nat11/{uuid}", s.auth(s.handleNAT11Update))
	mux.Handle("DELETE /api/v1/firewall/nat11/{uuid}", s.auth(s.handleNAT11Delete))
	mux.Handle("GET /api/v1/network/vlans", s.auth(s.handleVlanList))
	mux.Handle("POST /api/v1/network/vlans", s.auth(s.handleVlanCreate))
	mux.Handle("DELETE /api/v1/network/vlans/{vid}", s.auth(s.handleVlanDelete))
	mux.Handle("GET /api/v1/network/wans", s.auth(s.handleWANList))
	mux.Handle("POST /api/v1/network/wans/{name}", s.auth(s.handleWANSet))
	mux.Handle("DELETE /api/v1/network/wans/{name}", s.auth(s.handleWANDelete))
	mux.Handle("GET /api/v1/backup/archives", s.auth(s.handleBackupList))
	mux.Handle("POST /api/v1/backup/create", s.auth(s.handleBackupCreate))
	mux.Handle("GET /api/v1/backup/download/{name}", s.auth(s.handleBackupDownload))
	mux.Handle("POST /api/v1/backup/upload", s.auth(s.handleBackupUpload))
	mux.Handle("POST /api/v1/backup/restore", s.auth(s.handleBackupRestore))
	mux.Handle("DELETE /api/v1/backup/archives/{name}", s.auth(s.handleBackupDelete))
	mux.HandleFunc("/", s.handleRoot)

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	if *noTLS {
		log.Printf("saguaro-core %s sluša na %s (BEZ TLS-a)", version, *listen)
		err = srv.ListenAndServe()
	} else {
		certFile, keyFile, cerr := ensureCert(*etcDir)
		if cerr != nil {
			log.Fatalf("certifikat: %v", cerr)
		}
		log.Printf("saguaro-core %s sluša na %s (TLS)", version, *listen)
		err = srv.ListenAndServeTLS(certFile, keyFile)
	}
	if err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// ensureToken učita API token ili ga generira pri prvom startu.
func ensureToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		t := strings.TrimSpace(string(b))
		if t != "" {
			return t, nil
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	t := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(t+"\n"), 0o600); err != nil {
		return "", err
	}
	log.Printf("generiran novi API token u %s", path)
	return t, nil
}

// auth propušta strojni API token ili valjanu GUI sesiju.
func (s *server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if ok && subtle.ConstantTimeCompare([]byte(got), []byte(s.apiToken())) == 1 {
			next(w, r)
			return
		}
		if ok && s.sessionUser(got) != "" {
			next(w, r)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	})
}

func (s *server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if fi, err := os.Stat(filepath.Join(s.webDir, "index.html")); err == nil && !fi.IsDir() {
		http.FileServer(http.Dir(s.webDir)).ServeHTTP(w, r)
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><title>Saguaro</title>
<h1>Saguaro Infrastructure</h1><p>saguaro-core %s — API v1:</p>
<ul><li>GET /api/v1/health</li><li>GET /api/v1/system</li>
<li>GET /api/v1/system/status</li><li>GET /api/v1/storage</li>
<li>GET /api/v1/interfaces</li></ul>
<p>Svi endpointi osim /health traže <code>Authorization: Bearer &lt;token&gt;</code>.</p>`, version)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
