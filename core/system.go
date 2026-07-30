package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
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
	writeJSON(w, http.StatusOK, map[string]any{"syslog": syslog})
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
