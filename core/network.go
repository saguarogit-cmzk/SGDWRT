package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

const networkConfig = "/etc/config/network"

/* ---------- čitanje lan konfiguracije ---------- */

func (s *server) handleNetworkLanGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := uciGetConfig(r.Context(), "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	lan, ok := cfg["lan"]
	if !ok {
		writeErr(w, http.StatusNotFound, "lan sučelje ne postoji u konfiguraciji")
		return
	}
	dns := []string{}
	switch v := lan["dns"].(type) {
	case string:
		dns = append(dns, v)
	case []any:
		for _, d := range v {
			if str, ok := d.(string); ok {
				dns = append(dns, str)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"proto":   sectStr(lan, "proto"),
		"ipaddr":  sectStr(lan, "ipaddr"),
		"netmask": sectStr(lan, "netmask"),
		"gateway": sectStr(lan, "gateway"),
		"dns":     strings.Join(dns, " "),
	})
}

/* ---------- promjena lan adrese ---------- */

type lanUpdate struct {
	IPAddr  string `json:"ipaddr"`
	Netmask string `json:"netmask"`
	Gateway string `json:"gateway"`
	DNS     string `json:"dns"`
}

func (s *server) handleNetworkLanSet(w http.ResponseWriter, r *http.Request) {
	var u lanUpdate
	if !decodeBody(w, r, &u) {
		return
	}

	ip := net.ParseIP(strings.TrimSpace(u.IPAddr))
	if ip == nil || ip.To4() == nil {
		writeErr(w, http.StatusBadRequest, "neispravna IP adresa")
		return
	}
	if u.Netmask == "" {
		u.Netmask = "255.255.255.0"
	}
	maskIP := net.ParseIP(strings.TrimSpace(u.Netmask))
	if maskIP == nil || maskIP.To4() == nil {
		writeErr(w, http.StatusBadRequest, "neispravna mrežna maska")
		return
	}
	mask := net.IPMask(maskIP.To4())
	if ones, bits := mask.Size(); ones == 0 && bits == 0 {
		writeErr(w, http.StatusBadRequest, "neispravna mrežna maska")
		return
	}
	subnet := net.IPNet{IP: ip.Mask(mask), Mask: mask}

	gw := strings.TrimSpace(u.Gateway)
	if gw != "" {
		gwIP := net.ParseIP(gw)
		if gwIP == nil || gwIP.To4() == nil {
			writeErr(w, http.StatusBadRequest, "neispravan gateway")
			return
		}
		if !subnet.Contains(gwIP) {
			writeErr(w, http.StatusBadRequest,
				"gateway "+gw+" nije u podmreži "+subnet.String())
			return
		}
	}

	dnsList := []string{}
	for _, d := range strings.FieldsFunc(u.DNS, func(r rune) bool {
		return r == ' ' || r == ',' || r == ';'
	}) {
		if net.ParseIP(d) == nil {
			writeErr(w, http.StatusBadRequest, "neispravan DNS: "+d)
			return
		}
		dnsList = append(dnsList, d)
	}

	// backup prije izmjene (D-011)
	backupName, err := s.backupConfig(networkConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}

	// postojeće stanje — da delete linije ne padnu na nepostojećim opcijama
	cfg, err := uciGetConfig(r.Context(), "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	lan := cfg["lan"]

	var b strings.Builder
	fmt.Fprintf(&b, "set network.lan.proto=static\n")
	fmt.Fprintf(&b, "set network.lan.ipaddr=%s\n", ip.String())
	fmt.Fprintf(&b, "set network.lan.netmask=%s\n", maskIP.To4().String())
	if gw != "" {
		fmt.Fprintf(&b, "set network.lan.gateway=%s\n", gw)
	} else if sectStr(lan, "gateway") != "" {
		b.WriteString("delete network.lan.gateway\n")
	}
	if _, has := lan["dns"]; has {
		b.WriteString("delete network.lan.dns\n")
	}
	for _, d := range dnsList {
		fmt.Fprintf(&b, "add_list network.lan.dns=%s\n", d)
	}
	b.WriteString("commit network\n")

	if err := uciBatch(r.Context(), b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// odgovor mora otići PRIJE reloada mreže — veza na staroj adresi pada
	writeJSON(w, http.StatusOK, map[string]any{
		"applied":   true,
		"new_url":   "https://" + ip.String() + ":8443/",
		"reload_in": 3,
		"backup":    backupName,
	})
	go func() {
		time.Sleep(3 * time.Second)
		if err := serviceReload(context.Background(), "network", "reload"); err != nil {
			log.Printf("network reload: %v", err)
		}
	}()
}
