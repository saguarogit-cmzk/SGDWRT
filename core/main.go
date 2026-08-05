// saguaro-core — API servis Saguaro platforme na OpenWrt uređaju.
// Čita stanje sustava isključivo preko ubus-a (D-007) i servira REST API v1.
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
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

const version = "0.40.0"

type server struct {
	tokenMu       sync.RWMutex
	writeMu       sync.RWMutex
	lastWriteUser string    // zadnji korisnik koji je pisao kroz API
	lastWriteAt   time.Time // i kada — za pripisivanje promjene konfiguracije
	token         string    // API token za strojni pristup; GUI koristi sesije (auth.go)
	webDir        string
	etcDir        string
	dataDir       string
	backupDir     string
	started       time.Time
	db            *sql.DB
}

func main() {
	listen := flag.String("listen", ":8443", "adresa:port za slušanje")
	etcDir := flag.String("etc", defaultEtcDir, "direktorij konfiguracije (token, certifikat)")
	webDir := flag.String("web", "/opt/saguaro/web", "direktorij statičkog weba")
	dataDir := flag.String("data", defaultDataDir, "direktorij podataka (SQLite)")
	backupDir := flag.String("backup", "/opt/saguaro/backup", "direktorij backupa konfiguracija")
	noTLS := flag.Bool("no-tls", false, "posluži bez TLS-a (samo za razvoj)")
	resetAdmin := flag.String("reset-admin", "",
		"postavi novu lozinku za prijavu u Saguaro i izađi (izlaz iz nužde sa SSH-a)")
	decBackup := flag.String("decrypt-backup", "",
		"dešifriraj arhivu backupa i izađi")
	decOut := flag.String("out", "", "odredišna datoteka uz -decrypt-backup")
	decPass := flag.String("backup-pass", "",
		"lozinka arhive uz -decrypt-backup (prazno = uzmi spremljenu s uređaja)")
	ovpnAuth := flag.String("ovpn-auth", "",
		"provjeri OpenVPN prijavu iz datoteke i izađi (poziva ga OpenVPN)")
	ovpnUsers := flag.String("ovpn-users", "",
		"datoteka s otiscima lozinki uz -ovpn-auth")
	ovpnExport := flag.String("ovpn-export", "",
		"ispiši .ovpn datoteku za navedenog klijenta i izađi")
	flag.Parse()

	// Provjera VPN prijave mora proći PRIJE otvaranja baze: OpenVPN je zove
	// nakon što odustane od root ovlasti, pa proces ne može čitati bazu.
	// Sve što treba je datoteka s otiscima, čitljiva grupi nogroup.
	if *ovpnAuth != "" {
		if err := verifyOvpnUser(*ovpnAuth, *ovpnUsers); err != nil {
			fmt.Fprintln(os.Stderr, "prijava odbijena:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

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

	// izlaz iz nužde: zaboravljena lozinka za prijavu u Saguaro vraća se sa
	// SSH-a, bez dizanja servisa i bez diranja ostalih podataka
	if *resetAdmin != "" {
		if err := s.resetAdminPassword(*resetAdmin); err != nil {
			log.Fatalf("reset lozinke: %v", err)
		}
		fmt.Println("Lozinka korisnika 'admin' je postavljena.")
		fmt.Println("Sve postojeće sesije su odjavljene; pri prvoj prijavi tražit će se nova lozinka.")
		return
	}

	if *ovpnExport != "" {
		if err := s.exportOvpnConfig(*ovpnExport, *decOut); err != nil {
			log.Fatalf("%v", err)
		}
		return
	}

	// otvaranje šifrirane arhive; radi i na drugom uređaju ako se prenese
	// binary i navede lozinka (-backup-pass)
	if *decBackup != "" {
		pass := *decPass
		if pass == "" {
			pass = s.getSetting("backup_pass", "")
		}
		if pass == "" {
			log.Fatalf("nedostaje lozinka arhive (-backup-pass)")
		}
		if err := runDecrypt(*decBackup, *decOut, pass); err != nil {
			log.Fatalf("%v", err)
		}
		return
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

	// popis za sysupgrade se pise samo kad servis radi nad stvarnim
	// direktorijima; testna instanca s vlastitim putanjama ne smije
	// prepisati sistemsku datoteku svojim privremenim putanjama
	if *etcDir == defaultEtcDir && *dataDir == defaultDataDir {
		if err := ensureKeepList(*etcDir, *dataDir); err != nil {
			log.Printf("upozorenje: popis za sysupgrade nije zapisan: %v", err)
		}
	}

	{
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		s.ensureScanRules(ctx)
		cancel()
	}

	s.startChallengeServer()
	// put za ACME provjeru sučeljnog certifikata se uskladi pri svakom startu
	// (proxy se mogao uključiti ili isključiti dok servis nije radio)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.guiCertFirewallSync(ctx); err != nil {
			log.Printf("guicert firewall: %v", err)
		}
	}()
	go s.checkRootAfterUpgrade()
	go collectMetrics()
	go s.monitorLoop()
	go s.watchdogLoop()
	go s.auditLoop()

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
	mux.Handle("POST /api/v1/system/logstore", s.auth(s.handleLogStoreSet))
	mux.Handle("GET /api/v1/system/logfiles", s.auth(s.handleLogFiles))
	mux.Handle("GET /api/v1/system/logfiles/{name}", s.auth(s.handleLogDownload))
	mux.Handle("GET /api/v1/hardening", s.auth(s.handleHardeningGet))
	mux.Handle("POST /api/v1/hardening", s.auth(s.handleHardeningSet))
	mux.Handle("GET /api/v1/alerts", s.auth(s.handleAlertsGet))
	mux.Handle("POST /api/v1/alerts", s.auth(s.handleAlertsSet))
	mux.Handle("POST /api/v1/alerts/run", s.auth(s.handleWatchdogRun))
	mux.Handle("GET /api/v1/audit", s.auth(s.handleAuditList))
	mux.Handle("GET /api/v1/audit/{id}", s.auth(s.handleAuditDiff))
	mux.Handle("POST /api/v1/audit/run", s.auth(s.handleAuditRun))
	mux.Handle("GET /api/v1/backup/offsite", s.auth(s.handleOffsiteGet))
	mux.Handle("POST /api/v1/backup/offsite", s.auth(s.handleOffsiteSet))
	mux.Handle("POST /api/v1/backup/offsite/test", s.auth(s.handleOffsiteTest))
	mux.Handle("GET /api/v1/backup/mail", s.auth(s.handleBackupMailGet))
	mux.Handle("POST /api/v1/backup/mail", s.auth(s.handleBackupMailSet))
	mux.Handle("POST /api/v1/backup/mail/send", s.auth(s.handleBackupMailSend))
	mux.Handle("POST /api/v1/system/device-password", s.auth(s.handleDevicePasswordSet))
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
	mux.Handle("POST /api/v1/dns/dnssec", s.auth(s.handleDNSSECSet))
	mux.Handle("GET /api/v1/dns/split", s.auth(s.handleSplitList))
	mux.Handle("POST /api/v1/dns/split", s.auth(s.handleSplitCreate))
	mux.Handle("PUT /api/v1/dns/split/{uuid}", s.auth(s.handleSplitUpdate))
	mux.Handle("DELETE /api/v1/dns/split/{uuid}", s.auth(s.handleSplitDelete))
	mux.Handle("GET /api/v1/protection", s.auth(s.handleProtectionGet))
	mux.Handle("POST /api/v1/protection/banip", s.auth(s.handleBanipSet))
	mux.Handle("POST /api/v1/protection/adblock", s.auth(s.handleAdblockSet))
	mux.Handle("POST /api/v1/protection/scan", s.auth(s.handleScanSet))
	mux.Handle("POST /api/v1/protection/scan/clear", s.auth(s.handleScanClear))
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
	mux.Handle("GET /api/v1/proxy", s.auth(s.handleProxyGet))
	mux.Handle("GET /api/v1/proxy/config", s.auth(s.handleProxyConfig))
	mux.Handle("POST /api/v1/proxy/install", s.auth(s.handleProxyInstall))
	mux.Handle("POST /api/v1/proxy/apply", s.auth(s.handleProxyApply))
	mux.Handle("GET /api/v1/proxy/acme", s.auth(s.handleAcmeGet))
	mux.Handle("POST /api/v1/proxy/acme", s.auth(s.handleAcmeSet))
	mux.Handle("POST /api/v1/proxy/acme/install", s.auth(s.handleAcmeInstall))
	mux.Handle("POST /api/v1/proxy/acme/issue", s.auth(s.handleAcmeIssue))
	mux.Handle("POST /api/v1/proxy/sites", s.auth(s.handleProxySiteCreate))
	mux.Handle("PUT /api/v1/proxy/sites/{uuid}", s.auth(s.handleProxySiteUpdate))
	mux.Handle("DELETE /api/v1/proxy/sites/{uuid}", s.auth(s.handleProxySiteDelete))
	mux.Handle("GET /api/v1/ipv6", s.auth(s.handleIPv6Get))
	mux.Handle("POST /api/v1/ipv6", s.auth(s.handleIPv6Set))
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
	mux.Handle("GET /api/v1/dnsforce", s.auth(s.handleForcedDNSGet))
	mux.Handle("POST /api/v1/dnsforce", s.auth(s.handleForcedDNSSet))
	mux.Handle("GET /api/v1/connections", s.auth(s.handleConnections))
	mux.Handle("GET /api/v1/capture", s.auth(s.handleCaptureStatus))
	mux.Handle("POST /api/v1/capture/install", s.auth(s.handleCaptureInstall))
	mux.Handle("POST /api/v1/capture/start", s.auth(s.handleCaptureStart))
	mux.Handle("POST /api/v1/capture/stop", s.auth(s.handleCaptureStop))
	mux.Handle("GET /api/v1/capture/files/{name}", s.auth(s.handleCaptureDownload))
	mux.Handle("DELETE /api/v1/capture/files/{name}", s.auth(s.handleCaptureDelete))
	mux.Handle("GET /api/v1/routes", s.auth(s.handleRouteList))
	mux.Handle("POST /api/v1/routes", s.auth(s.handleRouteCreate))
	mux.Handle("PUT /api/v1/routes/{uuid}", s.auth(s.handleRouteUpdate))
	mux.Handle("DELETE /api/v1/routes/{uuid}", s.auth(s.handleRouteDelete))
	mux.Handle("POST /api/v1/routes/apply", s.auth(s.handleRouteApply))
	mux.Handle("GET /api/v1/firewall/nat11", s.auth(s.handleNAT11List))
	mux.Handle("POST /api/v1/firewall/nat11", s.auth(s.handleNAT11Create))
	mux.Handle("PUT /api/v1/firewall/nat11/{uuid}", s.auth(s.handleNAT11Update))
	mux.Handle("DELETE /api/v1/firewall/nat11/{uuid}", s.auth(s.handleNAT11Delete))
	mux.Handle("GET /api/v1/firewall/snat", s.auth(s.handleSNATList))
	mux.Handle("POST /api/v1/firewall/snat", s.auth(s.handleSNATCreate))
	mux.Handle("PUT /api/v1/firewall/snat/{uuid}", s.auth(s.handleSNATUpdate))
	mux.Handle("DELETE /api/v1/firewall/snat/{uuid}", s.auth(s.handleSNATDelete))
	mux.Handle("POST /api/v1/firewall/snat/{uuid}/move", s.auth(s.handleSNATMove))
	mux.Handle("GET /api/v1/ospf", s.auth(s.handleOspfGet))
	mux.Handle("POST /api/v1/ospf", s.auth(s.handleOspfSet))
	mux.Handle("GET /api/v1/multiwan", s.auth(s.handleMultiwanGet))
	mux.Handle("POST /api/v1/multiwan", s.auth(s.handleMultiwanSet))
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
	mux.Handle("GET /api/v1/backup/schedule", s.auth(s.handleBackupScheduleGet))
	mux.Handle("POST /api/v1/backup/schedule", s.auth(s.handleBackupScheduleSet))
	mux.Handle("GET /api/v1/settings/system", s.auth(s.handleSystemSettingsGet))
	mux.Handle("POST /api/v1/settings/hostname", s.auth(s.handleHostnameSet))
	mux.Handle("POST /api/v1/settings/syslog", s.auth(s.handleSyslogSet))
	mux.Handle("GET /api/v1/metrics/history", s.auth(s.handleMetricsHistory))
	mux.Handle("GET /api/v1/rollback", s.auth(s.handleRollbackStatus))
	mux.Handle("POST /api/v1/rollback/confirm", s.auth(s.handleRollbackConfirm))
	mux.Handle("GET /api/v1/firewall/aliases", s.auth(s.handleAliasList))
	mux.Handle("POST /api/v1/firewall/aliases", s.auth(s.handleAliasCreate))
	mux.Handle("PUT /api/v1/firewall/aliases/{uuid}", s.auth(s.handleAliasUpdate))
	mux.Handle("DELETE /api/v1/firewall/aliases/{uuid}", s.auth(s.handleAliasDelete))
	mux.Handle("GET /api/v1/qos", s.auth(s.handleQosGet))
	mux.Handle("POST /api/v1/qos", s.auth(s.handleQosSet))
	mux.Handle("GET /api/v1/ddns", s.auth(s.handleDdnsGet))
	mux.Handle("POST /api/v1/ddns", s.auth(s.handleDdnsSet))
	mux.Handle("GET /api/v1/monitor", s.auth(s.handleMonitorGet))
	mux.Handle("POST /api/v1/monitor", s.auth(s.handleMonitorCreate))
	mux.Handle("DELETE /api/v1/monitor/{uuid}", s.auth(s.handleMonitorDelete))
	mux.Handle("POST /api/v1/monitor/settings", s.auth(s.handleMonitorSettings))
	mux.Handle("POST /api/v1/settings/smtp", s.auth(s.handleSMTPSet))
	mux.Handle("POST /api/v1/notify/test", s.auth(s.handleNotifyTest))
	mux.Handle("GET /api/v1/traffic", s.auth(s.handleTraffic))
	mux.Handle("GET /api/v1/syslog", s.auth(s.handleSyslogView))
	mux.Handle("POST /api/v1/settings/mgmtacl", s.auth(s.handleMgmtACLSet))
	mux.Handle("POST /api/v1/settings/time", s.auth(s.handleTimeSet))
	mux.Handle("GET /api/v1/settings/guicert", s.auth(s.handleGuiCertGet))
	mux.Handle("POST /api/v1/settings/guicert", s.auth(s.handleGuiCertSet))
	mux.Handle("POST /api/v1/settings/guicert/issue", s.auth(s.handleGuiCertIssue))
	mux.Handle("GET /api/v1/update/status", s.auth(s.handleUpdateStatus))
	mux.Handle("POST /api/v1/update/upload", s.auth(s.handleUpdateUpload))
	mux.Handle("POST /api/v1/update/apply", s.auth(s.handleUpdateApply))
	mux.Handle("GET /api/v1/openwrt/disk", s.auth(s.handleDiskStatus))
	mux.Handle("GET /api/v1/openwrt/datapart", s.auth(s.handleDataPartStatus))
	mux.Handle("POST /api/v1/openwrt/datapart", s.auth(s.handleDataPartCreate))
	mux.Handle("GET /api/v1/openwrt/status", s.auth(s.handleOpenWrtStatus))
	mux.Handle("POST /api/v1/openwrt/build", s.auth(s.handleOpenWrtBuild))
	mux.Handle("POST /api/v1/openwrt/fetch", s.auth(s.handleOpenWrtFetch))
	mux.Handle("POST /api/v1/openwrt/upload", s.auth(s.handleOpenWrtUpload))
	mux.Handle("POST /api/v1/openwrt/flash", s.auth(s.handleOpenWrtFlash))
	mux.Handle("GET /api/v1/openwrt/packages", s.auth(s.handleOpenWrtPackages))
	mux.Handle("POST /api/v1/openwrt/packages/restore", s.auth(s.handleOpenWrtPackagesRestore))
	mux.HandleFunc("/", s.handleRoot)

	srv := &http.Server{
		Addr:              *listen,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
		// TLS 1.0 i 1.1 su odavno probijeni; sučelje za upravljanje ih ne treba
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
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
		// Certifikat ide kroz spremište s vrućom zamjenom: ACME certifikat za
		// sučelje (ako je zatražen) ima prednost, self-signed je pričuva, a
		// noćna obnova se pokupi bez restarta servisa.
		guiCerts = newCertStore(certFile, keyFile,
			func() string { return s.getSetting("gui_cert_host", "") })
		srv.TLSConfig.GetCertificate = guiCerts.getCertificate
		go guiCerts.watch()
		log.Printf("saguaro-core %s sluša na %s (TLS)", version, *listen)
		err = srv.ListenAndServeTLS("", "")
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
// allowedWithDefaultPassword nabraja jedine putanje dostupne korisniku koji još
// nije promijenio zadanu lozinku s instalacije.
// mutatingRequest javlja mijenja li poziv konfiguraciju. Nekoliko POST
// endpointa samo pokreće provjeru ili šalje probu — kad bi se i oni brojali,
// klik na "Provjeri sada" pripisao bi sebi tuđu promjenu.
func mutatingRequest(method, path string) bool {
	if method == http.MethodGet {
		return false
	}
	switch path {
	case "/api/v1/audit/run", "/api/v1/alerts/run", "/api/v1/notify/test",
		"/api/v1/rollback/confirm", "/api/v1/auth/logout",
		"/api/v1/auth/logout-others", "/api/v1/backup/offsite/test",
		"/api/v1/backup/mail/send",
		// priprema slike ne dira konfiguraciju uređaja
		"/api/v1/openwrt/build", "/api/v1/openwrt/fetch", "/api/v1/openwrt/upload",
		// snimanje prometa ne mijenja konfiguraciju
		"/api/v1/capture/install", "/api/v1/capture/start", "/api/v1/capture/stop":
		return false
	}
	return true
}

func allowedWithDefaultPassword(path string) bool {
	switch path {
	case "/api/v1/auth/password", "/api/v1/auth/logout", "/api/v1/auth/logout-others":
		return true
	}
	return false
}

func (s *server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if ok && subtle.ConstantTimeCompare([]byte(got), []byte(s.apiToken())) == 1 {
			// tko piše kroz API pamti se radi pripisivanja promjena
			// konfiguracije (audit.go); čitanje se ne bilježi
			if mutatingRequest(r.Method, r.URL.Path) {
				s.noteWrite("API token")
			}
			next(w, r)
			return
		}
		if user, mustChange := s.sessionUserState(got); user != "" {
			// dok je na snazi zadana lozinka s instalacije, dopuštena je samo
			// njena promjena i odjava — sve ostalo se odbija
			if mustChange && !allowedWithDefaultPassword(r.URL.Path) {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"error": "password_change_required",
					"detail": "Ova lozinka je privremena (zadana s instalacije ili " +
						"postavljena sa SSH-a) i mora se promijeniti prije " +
						"korištenja ostalih funkcija.",
				})
				return
			}
			if mutatingRequest(r.Method, r.URL.Path) {
				s.noteWrite(user)
			}
			next(w, r)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	})
}

func (s *server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if fi, err := os.Stat(filepath.Join(s.webDir, "index.html")); err == nil && !fi.IsDir() {
		// no-cache ne znači "ne spremaj", nego "prije upotrebe pitaj je li se
		// promijenilo". Bez toga preglednik nakon nadogradnje servira staro
		// sučelje dok korisnik ne napravi Ctrl+Shift+R; ovako sam povuče novo,
		// a nepromijenjene datoteke i dalje stižu kao jeftini 304.
		w.Header().Set("Cache-Control", "no-cache")
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

// securityHeaders dodaje zaglavlja koja preglednik koristi za ograničavanje
// štete: stranica se ne smije ugraditi u tuđi okvir, skripte i stilovi smiju
// doći samo s ovog uređaja, a adresa sučelja ne curi u vanjske poveznice.
// Sesijski token je u localStorage (nema kolačića, pa ni CSRF rizika), ali
// stroga politika sadržaja bitno smanjuje domet eventualnog XSS-a.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; "+
				"style-src 'self' 'unsafe-inline'; script-src 'self'; "+
				"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}
