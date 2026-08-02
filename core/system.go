package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Sustavne postavke: syslog forwarding (uci system sekcija) i povijest
// mjerenja za grafove na Dashboardu (kružni spremnik u memoriji).
const systemConfig = "/etc/config/system"

/* ---------- syslog forwarding ---------- */

func findSystemSection(cfg map[string]uciSection) string {
	for name, sec := range cfg {
		if sectStr(sec, ".type") == "system" {
			return name
		}
	}
	return ""
}

func (s *server) handleSystemSettingsGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := uciGetConfig(r.Context(), "system")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	syslog := map[string]any{"enabled": false, "host": "", "port": "514", "proto": "udp"}
	if sect := findSystemSection(cfg); sect != "" {
		sec := cfg[sect]
		if ip := sectStr(sec, "log_ip"); ip != "" {
			syslog = map[string]any{
				"enabled": true, "host": ip,
				"port":  sectStr(sec, "log_port"),
				"proto": sectStr(sec, "log_proto"),
			}
			if syslog["port"] == "" {
				syslog["port"] = "514"
			}
			if syslog["proto"] == "" {
				syslog["proto"] = "udp"
			}
		}
	}
	logstore := s.logStoreState(cfg)

	// stvarno stanje ACL-a iz firewalla (safe mode ga je mogao vratiti)
	aclActive := false
	if fwCfg, err := uciGetConfig(r.Context(), "firewall"); err == nil {
		_, aclActive = fwCfg["sag_acl_block"]
	}

	// vrijeme: zona, NTP poslužitelj i trenutni sat uređaja (samo za prikaz)
	zonename, ntpServer := "UTC", false
	if sect := findSystemSection(cfg); sect != "" {
		if z := sectStr(cfg[sect], "zonename"); z != "" {
			zonename = z
		}
	}
	ntpUpstreams := ""
	if ntp, ok := cfg["ntp"]; ok {
		ntpServer = sectStr(ntp, "enable_server") == "1"
		ntpUpstreams = strings.Join(sectList(ntp, "server"), " ")
	}
	deviceTime := ""
	if out, err := exec.CommandContext(r.Context(), "date",
		"+%d.%m.%Y %H:%M:%S %Z").Output(); err == nil {
		deviceTime = strings.TrimSpace(string(out))
	}
	zoneNames := make([]string, 0, len(tzZones))
	for _, z := range tzZones {
		zoneNames = append(zoneNames, z.Name)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"syslog":   syslog,
		"logstore": logstore,
		"mgmt_acl": map[string]any{
			"enabled": aclActive,
			"allow":   s.getSetting("mgmt_acl_allow", ""),
		},
		"time": map[string]any{
			"zonename":    zonename,
			"ntp_server":  ntpServer,
			"ntp_servers": ntpUpstreams,
			"device_time": deviceTime,
			"zones":       zoneNames,
		},
	})
}

func (s *server) handleSyslogSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Enabled *bool  `json:"enabled"`
		Host    string `json:"host"`
		Port    int    `json:"port"`
		Proto   string `json:"proto"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "nedostaje polje enabled")
		return
	}
	in.Host = strings.TrimSpace(in.Host)
	if *in.Enabled {
		if net.ParseIP(in.Host) == nil {
			writeErr(w, http.StatusBadRequest, "odredište mora biti IP adresa syslog poslužitelja")
			return
		}
		if in.Port == 0 {
			in.Port = 514
		}
		if in.Port < 1 || in.Port > 65535 {
			writeErr(w, http.StatusBadRequest, "neispravan port")
			return
		}
		if in.Proto == "" {
			in.Proto = "udp"
		}
		if in.Proto != "udp" && in.Proto != "tcp" {
			writeErr(w, http.StatusBadRequest, "proto mora biti udp ili tcp")
			return
		}
	}

	cfg, err := uciGetConfig(ctx, "system")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	sect := findSystemSection(cfg)
	if sect == "" {
		writeErr(w, http.StatusNotFound, "system sekcija ne postoji")
		return
	}
	backupName, err := s.backupConfig(systemConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}
	var b strings.Builder
	if *in.Enabled {
		fmt.Fprintf(&b, "set system.%s.log_ip=%s\n", sect, in.Host)
		fmt.Fprintf(&b, "set system.%s.log_port=%d\n", sect, in.Port)
		fmt.Fprintf(&b, "set system.%s.log_proto=%s\n", sect, in.Proto)
	} else {
		for _, opt := range []string{"log_ip", "log_port", "log_proto"} {
			if sectStr(cfg[sect], opt) != "" {
				fmt.Fprintf(&b, "delete system.%s.%s\n", sect, opt)
			}
		}
	}
	if b.Len() == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	b.WriteString("commit system\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := serviceReload(ctx, "log", "restart"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": *in.Enabled, "backup": backupName,
	})
}

/* ---------- vrijeme: vremenska zona + NTP poslužitelj ---------- */

// Kurirane zone s POSIX TZ zapisom (radi bez tzdata paketa).
var tzZones = []struct {
	Name  string `json:"name"`
	Posix string `json:"-"`
}{
	{"UTC", "UTC0"},
	{"Europe/Zagreb", "CET-1CEST,M3.5.0,M10.5.0/3"},
	{"Europe/Sarajevo", "CET-1CEST,M3.5.0,M10.5.0/3"},
	{"Europe/Belgrade", "CET-1CEST,M3.5.0,M10.5.0/3"},
	{"Europe/Ljubljana", "CET-1CEST,M3.5.0,M10.5.0/3"},
	{"Europe/Vienna", "CET-1CEST,M3.5.0,M10.5.0/3"},
	{"Europe/Berlin", "CET-1CEST,M3.5.0,M10.5.0/3"},
	{"Europe/Budapest", "CET-1CEST,M3.5.0,M10.5.0/3"},
	{"Europe/Rome", "CET-1CEST,M3.5.0,M10.5.0/3"},
	{"Europe/Prague", "CET-1CEST,M3.5.0,M10.5.0/3"},
	{"Europe/Warsaw", "CET-1CEST,M3.5.0,M10.5.0/3"},
	{"Europe/Paris", "CET-1CEST,M3.5.0,M10.5.0/3"},
	{"Europe/Zurich", "CET-1CEST,M3.5.0,M10.5.0/3"},
	{"Europe/London", "GMT0BST,M3.5.0/1,M10.5.0"},
	{"Europe/Lisbon", "WET0WEST,M3.5.0/1,M10.5.0"},
	{"Europe/Athens", "EET-2EEST,M3.5.0/3,M10.5.0/4"},
	{"Europe/Bucharest", "EET-2EEST,M3.5.0/3,M10.5.0/4"},
	{"Europe/Helsinki", "EET-2EEST,M3.5.0/3,M10.5.0/4"},
	{"Europe/Istanbul", "<+03>-3"},
	{"Europe/Moscow", "MSK-3"},
	{"America/New_York", "EST5EDT,M3.2.0,M11.1.0"},
	{"America/Chicago", "CST6CDT,M3.2.0,M11.1.0"},
	{"America/Los_Angeles", "PST8PDT,M3.2.0,M11.1.0"},
	{"Asia/Dubai", "<+04>-4"},
	{"Australia/Sydney", "AEST-10AEDT,M10.1.0,M4.1.0/3"},
}

func tzPosixFor(name string) string {
	for _, z := range tzZones {
		if z.Name == name {
			return z.Posix
		}
	}
	return ""
}

func (s *server) handleTimeSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Zonename   string  `json:"zonename"`
		NTPServer  *bool   `json:"ntp_server"`
		NTPServers *string `json:"ntp_servers"` // razmakom odvojeni; prazno = zadani pool
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.Zonename = strings.TrimSpace(in.Zonename)
	posix := tzPosixFor(in.Zonename)
	if posix == "" {
		writeErr(w, http.StatusBadRequest, "nepoznata vremenska zona")
		return
	}
	var upstreams []string
	if in.NTPServers != nil {
		for _, srv := range strings.Fields(*in.NTPServers) {
			srv = strings.ToLower(srv)
			if net.ParseIP(srv) == nil && !validDNSName(srv) {
				writeErr(w, http.StatusBadRequest, "neispravan NTP poslužitelj: "+srv)
				return
			}
			upstreams = append(upstreams, srv)
		}
	}
	cfg, err := uciGetConfig(ctx, "system")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	sect := findSystemSection(cfg)
	if sect == "" {
		writeErr(w, http.StatusNotFound, "system sekcija ne postoji")
		return
	}
	backupName, err := s.backupConfig(systemConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "set system.%s.zonename=%s\n", sect, uciQuote(in.Zonename))
	fmt.Fprintf(&b, "set system.%s.timezone=%s\n", sect, uciQuote(posix))
	if in.NTPServer != nil {
		if *in.NTPServer {
			b.WriteString("set system.ntp.enabled=1\n")
			b.WriteString("set system.ntp.enable_server=1\n")
		} else if _, ok := cfg["ntp"]; ok {
			b.WriteString("set system.ntp.enable_server=0\n")
		}
	}
	if in.NTPServers != nil {
		if ntp, ok := cfg["ntp"]; ok {
			if _, has := ntp["server"]; has {
				b.WriteString("delete system.ntp.server\n")
			}
		} else {
			b.WriteString("set system.ntp=timeserver\n")
		}
		if len(upstreams) == 0 {
			// prazno = vrati zadani OpenWrt pool
			for i := 0; i < 4; i++ {
				fmt.Fprintf(&b, "add_list system.ntp.server=%d.openwrt.pool.ntp.org\n", i)
			}
		}
		for _, srv := range upstreams {
			fmt.Fprintf(&b, "add_list system.ntp.server=%s\n", srv)
		}
	}
	b.WriteString("commit system\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// system reload postavlja /tmp/TZ; sysntpd i log preuzimaju novo stanje
	for _, svc := range [][2]string{{"system", "reload"}, {"sysntpd", "restart"},
		{"log", "restart"}} {
		if err := serviceReload(ctx, svc[0], svc[1]); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"zonename": in.Zonename, "backup": backupName,
	})
}

/* ---------- ograničenje upravljačkog pristupa (management ACL) ---------- */

// handleMgmtACLSet: pristup GUI-ju (8443) i SSH-u (22) dopušten samo s
// navedenih adresa/mreža; sve ostalo se odbacuje. Zbog rizika samo-
// zaključavanja promjena ide kroz safe mode (auto-rollback bez potvrde).
func (s *server) handleMgmtACLSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Enabled *bool  `json:"enabled"`
		Allow   string `json:"allow"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "nedostaje polje enabled")
		return
	}
	allow := strings.Fields(in.Allow)
	if *in.Enabled && len(allow) == 0 {
		writeErr(w, http.StatusBadRequest,
			"navedi bar jednu dopuštenu adresu ili mrežu (CIDR)")
		return
	}
	for _, a := range allow {
		if !validAddr(a) {
			writeErr(w, http.StatusBadRequest, "neispravna adresa: "+a)
			return
		}
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
	for name, sec := range cfg {
		if strings.HasPrefix(name, "sag_acl_") && sectStr(sec, ".type") == "rule" {
			fmt.Fprintf(&b, "delete firewall.%s\n", name)
		}
	}
	if *in.Enabled {
		for i, a := range allow {
			sn := fmt.Sprintf("sag_acl_ok%d", i)
			fmt.Fprintf(&b, "set firewall.%s=rule\n", sn)
			fmt.Fprintf(&b, "set firewall.%s.name=%s\n", sn, uciQuote("Saguaro-ACL dopusti "+a))
			fmt.Fprintf(&b, "set firewall.%s.src=*\n", sn)
			fmt.Fprintf(&b, "set firewall.%s.src_ip=%s\n", sn, a)
			fmt.Fprintf(&b, "set firewall.%s.proto=tcp\n", sn)
			fmt.Fprintf(&b, "set firewall.%s.dest_port=%s\n", sn, uciQuote("8443 22"))
			fmt.Fprintf(&b, "set firewall.%s.target=ACCEPT\n", sn)
		}
		b.WriteString("set firewall.sag_acl_block=rule\n")
		b.WriteString("set firewall.sag_acl_block.name=Saguaro-ACL-blokada\n")
		b.WriteString("set firewall.sag_acl_block.src=*\n")
		b.WriteString("set firewall.sag_acl_block.proto=tcp\n")
		fmt.Fprintf(&b, "set firewall.sag_acl_block.dest_port=%s\n", uciQuote("8443 22"))
		b.WriteString("set firewall.sag_acl_block.target=DROP\n")
	}
	if b.Len() == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
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
	s.setSetting("mgmt_acl_allow", strings.Join(allow, " "))
	s.setSetting("mgmt_acl_enabled", map[bool]string{true: "1", false: "0"}[*in.Enabled])

	// safe mode: bez potvrde u roku vraća se prethodni firewall
	if *in.Enabled {
		s.scheduleRollback("ograničenje upravljačkog pristupa",
			map[string]string{firewallConfig: backupName},
			[][2]string{{"firewall", "reload"}})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": *in.Enabled, "backup": backupName,
		"safe_mode": *in.Enabled,
	})
}

/* ---------- povijest mjerenja (za grafove) ---------- */

type metricSample struct {
	TS     int64   `json:"ts"`
	Load1  float64 `json:"load1"`
	MemPct float64 `json:"mem_pct"`
}

const metricsMax = 120 // 120 uzoraka × 30 s = zadnjih sat vremena

var (
	metricsMu   sync.Mutex
	metricsRing []metricSample
)

// collectMetrics uzorkuje stanje svakih 30 s; pokreće se iz main-a.
func collectMetrics() {
	for {
		var i ubusInfo
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := ubusCall(ctx, "system", "info", &i)
		cancel()
		if err == nil && i.Memory.Total > 0 {
			sm := metricSample{
				TS:     time.Now().Unix(),
				Load1:  float64(i.Load[0]) / 65536.0,
				MemPct: 100 * float64(i.Memory.Total-i.Memory.Available) / float64(i.Memory.Total),
			}
			metricsMu.Lock()
			metricsRing = append(metricsRing, sm)
			if len(metricsRing) > metricsMax {
				metricsRing = metricsRing[len(metricsRing)-metricsMax:]
			}
			metricsMu.Unlock()
		}
		time.Sleep(30 * time.Second)
	}
}

func (s *server) handleMetricsHistory(w http.ResponseWriter, r *http.Request) {
	metricsMu.Lock()
	out := make([]metricSample, len(metricsRing))
	copy(out, metricsRing)
	metricsMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"samples": out})
}

/* ---------- lozinka uređaja (root: SSH i LuCI) ---------- */

const shadowFile = "/etc/shadow"

// rootShadowLine vraća redak korisnika root iz /etc/shadow — po njemu se
// provjerava je li promjena lozinke stvarno prošla.
func rootShadowLine() (string, error) {
	b, err := os.ReadFile(shadowFile)
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "root:") {
			return l, nil
		}
	}
	return "", fmt.Errorf("korisnik root nije pronađen u %s", shadowFile)
}

// handleDevicePasswordSet mijenja lozinku uređaja (root) — istu koja se koristi
// za SSH i LuCI. Radi se preko busybox passwd naredbe; ona ispisuje upozorenja
// i kad uspije, pa se uspjeh provjerava usporedbom zapisa u /etc/shadow.
func (s *server) handleDevicePasswordSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		New     string `json:"new"`
		Confirm string `json:"confirm"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	switch {
	case len([]rune(in.New)) < 10:
		writeErr(w, http.StatusBadRequest,
			"lozinka uređaja mora imati bar 10 znakova")
		return
	case hasCtrl(in.New):
		writeErr(w, http.StatusBadRequest,
			"lozinka ne smije sadržavati prijelom retka ni tabulator")
		return
	case in.Confirm != "" && in.Confirm != in.New:
		writeErr(w, http.StatusBadRequest, "lozinke se ne podudaraju")
		return
	}

	before, err := rootShadowLine()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	backupName, err := s.backupConfig(shadowFile)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "passwd", "root")
	cmd.Stdin = strings.NewReader(in.New + "\n" + in.New + "\n")
	out, runErr := cmd.CombinedOutput()

	after, err := rootShadowLine()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if after == before {
		msg := strings.TrimSpace(string(out))
		if msg == "" && runErr != nil {
			msg = runErr.Error()
		}
		writeErr(w, http.StatusInternalServerError,
			"lozinka nije promijenjena: "+msg)
		return
	}
	addEvent(s, "warning", "Promijenjena je lozinka uređaja (root — SSH i LuCI)")
	writeJSON(w, http.StatusOK, map[string]any{
		"changed": true, "backup": backupName,
	})
}

/* ---------- trajni log na disk ---------- */

// Logovi zadano žive samo u malom spremniku u RAM-u (128 kB) i nestaju pri
// svakom restartu. Uređaj ima 220 GB diska, pa nema razloga da tako ostane:
// ovo uključuje pisanje u datoteku i dnevnu rotaciju kroz cron.
const logDir = "/opt/saguaro/log"
const logFile = logDir + "/system.log"
const logRotateMark = "# sag-logrotate"
const logRotateScript = "/opt/saguaro/etc/logrotate.sh"

type logStore struct {
	Enabled   bool  `json:"enabled"`
	BufferKB  int   `json:"buffer_kb"`
	KeepDays  int   `json:"keep_days"`
	SizeBytes int64 `json:"size_bytes"`
	Files     int   `json:"files"`
}

func (s *server) logStoreState(cfg map[string]uciSection) logStore {
	st := logStore{BufferKB: 128, KeepDays: 30}
	if sect := findSystemSection(cfg); sect != "" {
		st.Enabled = sectStr(cfg[sect], "log_file") == logFile
		if n, err := strconv.Atoi(sectStr(cfg[sect], "log_size")); err == nil && n > 0 {
			st.BufferKB = n
		}
	}
	if n, err := strconv.Atoi(s.getSetting("log_keep_days", "30")); err == nil && n > 0 {
		st.KeepDays = n
	}
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return st
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() {
			st.Files++
			st.SizeBytes += info.Size()
		}
	}
	return st
}

func (s *server) handleLogStoreSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Enabled  *bool `json:"enabled"`
		BufferKB int   `json:"buffer_kb"`
		KeepDays int   `json:"keep_days"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "nedostaje polje enabled")
		return
	}
	if in.BufferKB == 0 {
		in.BufferKB = 1024
	}
	if in.BufferKB < 64 || in.BufferKB > 16384 {
		writeErr(w, http.StatusBadRequest,
			"spremnik u memoriji mora biti između 64 i 16384 kB")
		return
	}
	if in.KeepDays == 0 {
		in.KeepDays = 30
	}
	if in.KeepDays < 1 || in.KeepDays > 3650 {
		writeErr(w, http.StatusBadRequest, "čuvanje logova mora biti 1–3650 dana")
		return
	}

	cfg, err := uciGetConfig(ctx, "system")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	sect := findSystemSection(cfg)
	if sect == "" {
		writeErr(w, http.StatusNotFound, "system sekcija ne postoji")
		return
	}
	backupName, err := s.backupConfig(systemConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}

	var b strings.Builder
	if *in.Enabled {
		if err := os.MkdirAll(logDir, 0o750); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		fmt.Fprintf(&b, "set system.%s.log_file=%s\n", sect, logFile)
		fmt.Fprintf(&b, "set system.%s.log_size=%d\n", sect, in.BufferKB)
	} else {
		if sectStr(cfg[sect], "log_file") != "" {
			fmt.Fprintf(&b, "delete system.%s.log_file\n", sect)
		}
		fmt.Fprintf(&b, "set system.%s.log_size=128\n", sect)
	}
	b.WriteString("commit system\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.setSetting("log_keep_days", strconv.Itoa(in.KeepDays)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.writeLogRotate(*in.Enabled, in.KeepDays); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := serviceReload(ctx, "log", "restart"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": *in.Enabled, "backup": backupName,
	})
}

// writeLogRotate postavlja (ili miče) noćnu rotaciju loga. Rotacija je
// preimenovanje uz datum + restart logd-a, jer logd piše u jednu datoteku i
// ne zna sam rotirati.
func (s *server) writeLogRotate(enabled bool, keepDays int) error {
	if !enabled {
		os.Remove(logRotateScript)
		return replaceCronLine(logRotateMark, "")
	}
	script := "#!/bin/sh\n" +
		"# Saguaro: noćna rotacija sistemskog loga\n" +
		"LOG=" + logFile + "\n" +
		// čišćenje starih ide prvo — inače bi se preskočilo onih dana kad
		// nema što rotirati (busybox find nema -delete, zato -exec rm)
		"find " + logDir + " -name 'system.log.*' -mtime +" +
		strconv.Itoa(keepDays) + " -exec rm -f {} \\; 2>/dev/null\n" +
		"[ -s \"$LOG\" ] || exit 0\n" +
		"mv \"$LOG\" \"$LOG.$(date +%Y-%m-%d)\"\n" +
		"/etc/init.d/log restart\n" +
		"gzip -f \"$LOG.$(date +%Y-%m-%d)\" 2>/dev/null\n" +
		"exit 0\n"
	if err := os.WriteFile(logRotateScript, []byte(script), 0o700); err != nil {
		return err
	}
	return replaceCronLine(logRotateMark,
		"5 0 * * * "+logRotateScript+" "+logRotateMark)
}

// handleLogFiles vraća popis spremljenih dnevnih logova (za preuzimanje).
func (s *server) handleLogFiles(w http.ResponseWriter, r *http.Request) {
	type f struct {
		Name  string `json:"name"`
		Size  int64  `json:"size"`
		MTime string `json:"mtime"`
	}
	out := []f{}
	entries, _ := os.ReadDir(logDir)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		out = append(out, f{e.Name(), info.Size(),
			info.ModTime().Format("2006-01-02 15:04")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"files": out})
}

// handleLogDownload servira spremljeni log. Ime se provjerava jednako strogo
// kao kod backupa — samo datoteke iz log direktorija, bez putanja.
func (s *server) handleLogDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		writeErr(w, http.StatusNotFound, "log ne postoji")
		return
	}
	p := filepath.Join(logDir, name)
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		writeErr(w, http.StatusNotFound, "log ne postoji")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, p)
}
