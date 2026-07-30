package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// OSPF (dinamičko usmjeravanje) kroz bird2. Saguaro generira cijelu
// /etc/bird.conf (datoteka nastaje instalacijom bird2 paketa i nema tuđih
// ručnih zapisa; backup se sprema prije svake izmjene). Odabir sučelja i
// postavke čuvaju se u settings tablici; OSPF promet (IP proto 89) otvara se
// samo na zonama odabranih sučelja (sag_ospf_* pravila).
const birdConf = "/etc/bird.conf"

type ospfIface struct {
	Name string `json:"name"` // logičko sučelje (lan, sag_vlan20, sag_wg0...)
	Stub bool   `json:"stub"` // stub: mreža se objavljuje, susjedi se ne traže
}

/* ---------- pomoćne ---------- */

// ospfResolveDevice mapira logičko sučelje u fizički uređaj (lan -> br-lan).
func ospfResolveDevice(cfg map[string]uciSection, name string) string {
	sec, ok := cfg[name]
	if !ok || sectStr(sec, ".type") != "interface" {
		return ""
	}
	if d := sectStr(sec, "device"); d != "" {
		return d
	}
	if sectStr(sec, "proto") == "wireguard" {
		return name // netifd stvara uređaj s imenom sekcije
	}
	return ""
}

// ospfZoneOf nalazi ime firewall zone koja sadrži dano logičko sučelje.
func ospfZoneOf(fwCfg map[string]uciSection, iface string) string {
	for _, sec := range fwCfg {
		if sectStr(sec, ".type") != "zone" {
			continue
		}
		for _, n := range sectList(sec, "network") {
			if n == iface {
				return sectStr(sec, "name")
			}
		}
	}
	return ""
}

/* ---------- pregled ---------- */

func (s *server) handleOspfGet(w http.ResponseWriter, r *http.Request) {
	_, lookErr := exec.LookPath("bird")

	netCfg, err := uciGetConfig(r.Context(), "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	// ponuđena sučelja: sve osim loopbacka i IPv6 wan-a
	avail := []map[string]string{}
	for name := range netCfg {
		if sectStr(netCfg[name], ".type") != "interface" ||
			name == "loopback" || name == "wan6" {
			continue
		}
		dev := ospfResolveDevice(netCfg, name)
		if dev == "" {
			continue
		}
		avail = append(avail, map[string]string{"name": name, "device": dev})
	}
	sort.Slice(avail, func(i, j int) bool { return avail[i]["name"] < avail[j]["name"] })

	ifaces := []ospfIface{}
	json.Unmarshal([]byte(s.getSetting("ospf_interfaces", "[]")), &ifaces)

	running := false
	var svc map[string]struct {
		Instances map[string]struct {
			Running bool `json:"running"`
		} `json:"instances"`
	}
	if err := ubusCallArg(r.Context(), "service", "list",
		`{"name":"bird"}`, &svc); err == nil {
		for _, x := range svc["bird"].Instances {
			if x.Running {
				running = true
			}
		}
	}

	// stanje protokola i susjeda — birdc izlaz se samo prikazuje (bez
	// parsiranja odluka iz teksta), bird nema ubus/JSON sučelje
	status := ""
	if running {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "birdc", "show", "protocols").
			Output(); err == nil {
			status = string(out)
		}
		if out, err := exec.CommandContext(ctx, "birdc", "show", "ospf",
			"neighbors", "sagospf").Output(); err == nil {
			status += "\n" + string(out)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"installed":            lookErr == nil,
		"enabled":              s.getSetting("ospf_enabled", "0") == "1",
		"router_id":            s.getSetting("ospf_router_id", ""),
		"area":                 s.getSetting("ospf_area", "0"),
		"interfaces":           ifaces,
		"available_interfaces": avail,
		"running":              running,
		"status_text":          status,
	})
}

/* ---------- konfiguracija ---------- */

func (s *server) handleOspfSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Enabled  *bool       `json:"enabled"`
		RouterID string      `json:"router_id"`
		Area     string      `json:"area"`
		Ifaces   []ospfIface `json:"interfaces"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "nedostaje polje enabled")
		return
	}
	enabled := *in.Enabled

	netCfg, err := uciGetConfig(ctx, "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	in.RouterID = strings.TrimSpace(in.RouterID)
	if in.RouterID == "" {
		if lan, ok := netCfg["lan"]; ok {
			in.RouterID = sectStr(lan, "ipaddr")
		}
	}
	if ip := net.ParseIP(in.RouterID); ip == nil || ip.To4() == nil {
		writeErr(w, http.StatusBadRequest, "router ID mora biti IPv4 adresa")
		return
	}
	in.Area = strings.TrimSpace(in.Area)
	if in.Area == "" {
		in.Area = "0"
	}
	var areaN uint32
	if _, err := fmt.Sscanf(in.Area, "%d", &areaN); err != nil {
		writeErr(w, http.StatusBadRequest, "area mora biti broj (npr. 0)")
		return
	}
	devs := map[string]string{}
	for _, i := range in.Ifaces {
		d := ospfResolveDevice(netCfg, i.Name)
		if d == "" {
			writeErr(w, http.StatusBadRequest, "nepoznato sučelje: "+i.Name)
			return
		}
		devs[i.Name] = d
	}
	if enabled && len(in.Ifaces) == 0 {
		writeErr(w, http.StatusBadRequest, "odaberi bar jedno sučelje za OSPF")
		return
	}

	backups := []string{}
	if _, err := os.Stat(birdConf); err == nil {
		bn, err := s.backupConfig(birdConf)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
			return
		}
		backups = append(backups, bn)
	}

	var b strings.Builder
	b.WriteString("# Saguaro OSPF konfiguracija — generirano, ne uređivati ručno\n")
	b.WriteString("log syslog all;\n")
	fmt.Fprintf(&b, "router id %s;\n\n", in.RouterID)
	b.WriteString("protocol device { }\n")
	b.WriteString("protocol direct { ipv4; }\n")
	b.WriteString("protocol kernel {\n")
	b.WriteString("  ipv4 { import none; export where source = RTS_OSPF; };\n")
	b.WriteString("  learn;\n}\n\n")
	if enabled {
		b.WriteString("protocol ospf v2 sagospf {\n")
		b.WriteString("  ipv4 { import all; export where source = RTS_DEVICE; };\n")
		fmt.Fprintf(&b, "  area %d {\n", areaN)
		for _, i := range in.Ifaces {
			if i.Stub {
				fmt.Fprintf(&b, "    interface \"%s\" { stub; };\n", devs[i.Name])
			} else {
				fmt.Fprintf(&b, "    interface \"%s\" { };\n", devs[i.Name])
			}
		}
		b.WriteString("  };\n}\n")
	}
	if err := os.WriteFile(birdConf, []byte(b.String()), 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// firewall: dopusti OSPF (proto 89) samo na zonama odabranih sučelja
	fwBackup, err := s.backupConfig(firewallConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}
	backups = append(backups, fwBackup)
	fwCfg, err := uciGetConfig(ctx, "firewall")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var fb strings.Builder
	for name, sec := range fwCfg {
		if strings.HasPrefix(name, "sag_ospf_") && sectStr(sec, ".type") == "rule" {
			fmt.Fprintf(&fb, "delete firewall.%s\n", name)
		}
	}
	if enabled {
		zones := map[string]bool{}
		for _, i := range in.Ifaces {
			if z := ospfZoneOf(fwCfg, i.Name); z != "" {
				zones[z] = true
			}
		}
		for z := range zones {
			sn := "sag_ospf_" + z
			fmt.Fprintf(&fb, "set firewall.%s=rule\n", sn)
			fmt.Fprintf(&fb, "set firewall.%s.name=%s\n", sn, uciQuote("Saguaro-OSPF "+z))
			fmt.Fprintf(&fb, "set firewall.%s.src=%s\n", sn, z)
			fmt.Fprintf(&fb, "set firewall.%s.proto=ospf\n", sn)
			fmt.Fprintf(&fb, "set firewall.%s.target=ACCEPT\n", sn)
		}
	}
	if fb.Len() > 0 {
		fb.WriteString("commit firewall\n")
		if err := uciBatch(ctx, fb.String()); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := serviceReload(ctx, "firewall", "reload"); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	ifJSON, _ := json.Marshal(in.Ifaces)
	for k, v := range map[string]string{
		"ospf_enabled":    map[bool]string{true: "1", false: "0"}[enabled],
		"ospf_router_id":  in.RouterID,
		"ospf_area":       in.Area,
		"ospf_interfaces": string(ifJSON),
	} {
		if err := s.setSetting(k, v); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	if enabled {
		if err := serviceReload(ctx, "bird", "enable"); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := serviceReload(ctx, "bird", "restart"); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		serviceReload(ctx, "bird", "stop")
		serviceReload(ctx, "bird", "disable")
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": enabled, "router_id": in.RouterID, "backups": backups,
	})
}
