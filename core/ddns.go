package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

// Dinamički DNS (ddns-scripts): uređaj sam ažurira svoje javno DNS ime kad
// se javna adresa promijeni — VPN endpoint ostaje dostupan bez statičkog IP-a.
// Saguaro upravlja jednom sekcijom: ddns.sag_ddns.
const ddnsConfig = "/etc/config/ddns"

var ddnsProviders = []string{
	"duckdns.org", "no-ip.com", "dyn.com", "cloudflare.com-v4",
	"desec.io", "dynv6.com",
}

func (s *server) handleDdnsGet(w http.ResponseWriter, r *http.Request) {
	installed := false
	if _, err := os.Stat("/etc/init.d/ddns"); err == nil {
		installed = true
	}
	out := map[string]any{
		"installed": installed, "enabled": false,
		"providers": ddnsProviders,
	}
	cfg, err := uciGetConfig(r.Context(), "ddns")
	if err == nil {
		if sec, ok := cfg["sag_ddns"]; ok {
			out["enabled"] = sectStr(sec, "enabled") == "1"
			out["provider"] = sectStr(sec, "service_name")
			out["update_url"] = sectStr(sec, "update_url")
			out["domain"] = sectStr(sec, "lookup_host")
			out["username"] = sectStr(sec, "username")
		}
	}
	// zadnja registrirana adresa (datoteka koju ddns-scripts održava)
	if b, err := os.ReadFile("/var/run/ddns/sag_ddns.ip"); err == nil {
		out["registered_ip"] = strings.TrimSpace(string(b))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleDdnsSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Enabled   *bool  `json:"enabled"`
		Provider  string `json:"provider"`
		UpdateURL string `json:"update_url"`
		Domain    string `json:"domain"`
		Username  string `json:"username"`
		Password  string `json:"password"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "nedostaje polje enabled")
		return
	}
	in.Domain = strings.ToLower(strings.TrimSpace(in.Domain))
	in.Provider = strings.TrimSpace(in.Provider)
	in.UpdateURL = strings.TrimSpace(in.UpdateURL)
	if *in.Enabled {
		if !validDNSName(in.Domain) {
			writeErr(w, http.StatusBadRequest, "neispravno DNS ime (npr. moj.duckdns.org)")
			return
		}
		if in.Provider == "" && in.UpdateURL == "" {
			writeErr(w, http.StatusBadRequest, "odaberi pružatelja ili upiši update URL")
			return
		}
		ok := in.Provider == ""
		for _, p := range ddnsProviders {
			if p == in.Provider {
				ok = true
			}
		}
		if !ok {
			writeErr(w, http.StatusBadRequest, "nepoznat pružatelj")
			return
		}
	}

	backups := []string{}
	if _, err := os.Stat(ddnsConfig); err == nil {
		bn, err := s.backupConfig(ddnsConfig)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
			return
		}
		backups = append(backups, bn)
	}

	cfg, _ := uciGetConfig(ctx, "ddns")
	var b strings.Builder
	if _, ok := cfg["sag_ddns"]; ok {
		b.WriteString("delete ddns.sag_ddns\n")
	}
	if *in.Enabled {
		b.WriteString("set ddns.sag_ddns=service\n")
		b.WriteString("set ddns.sag_ddns.enabled=1\n")
		fmt.Fprintf(&b, "set ddns.sag_ddns.lookup_host=%s\n", in.Domain)
		fmt.Fprintf(&b, "set ddns.sag_ddns.domain=%s\n", in.Domain)
		if in.Provider != "" {
			fmt.Fprintf(&b, "set ddns.sag_ddns.service_name=%s\n", uciQuote(in.Provider))
		} else {
			fmt.Fprintf(&b, "set ddns.sag_ddns.update_url=%s\n", uciQuote(in.UpdateURL))
		}
		if in.Username != "" {
			fmt.Fprintf(&b, "set ddns.sag_ddns.username=%s\n", uciQuote(in.Username))
		}
		if in.Password != "" {
			fmt.Fprintf(&b, "set ddns.sag_ddns.password=%s\n", uciQuote(in.Password))
		}
		b.WriteString("set ddns.sag_ddns.interface=wan\n")
		b.WriteString("set ddns.sag_ddns.ip_source=network\n")
		b.WriteString("set ddns.sag_ddns.ip_network=wan\n")
		b.WriteString("set ddns.sag_ddns.use_https=1\n")
		b.WriteString("set ddns.sag_ddns.check_interval=10\n")
		b.WriteString("set ddns.sag_ddns.check_unit=minutes\n")
	}
	b.WriteString("commit ddns\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	action := "restart"
	if !*in.Enabled {
		action = "stop"
	}
	if err := serviceReload(ctx, "ddns", action); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": *in.Enabled, "backups": backups,
	})
}
