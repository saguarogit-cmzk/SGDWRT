package main

import (
	"fmt"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// VLAN wizard: jedan korak stvara 802.1q uređaj + sučelje + DHCP pool +
// firewall zonu s forwardinzima. Sve sekcije nose sag prefiks (D-011):
//
//	network.sag_vlan<id>_dev (device), network.sag_vlan<id> (interface),
//	dhcp.sag_vlan<id> (pool), firewall.sag_zn_<id> (zona),
//	firewall.sag_fw_<id>_wan / _lan i sag_fw_lan_<id> (forwardinzi).
var reVlanSlug = regexp.MustCompile(`^[a-z][a-z0-9]{1,9}$`)
var reLease = regexp.MustCompile(`^[0-9]{1,4}[smhd]$`)

/* ---------- pregled ---------- */

type vlanOut struct {
	VID     int    `json:"vid"`
	Port    string `json:"port"`
	Name    string `json:"name"` // ime zone/mreže
	IPAddr  string `json:"ipaddr"`
	Netmask string `json:"netmask"`
	DHCP    bool   `json:"dhcp"`
	Start   string `json:"dhcp_start"`
	Limit   string `json:"dhcp_limit"`
	Access  string `json:"access"` // wan | wan_lan | isolated
	Up      bool   `json:"up"`
}

func (s *server) handleVlanList(w http.ResponseWriter, r *http.Request) {
	netCfg, err := uciGetConfig(r.Context(), "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	dhcpCfg, _ := uciGetConfig(r.Context(), "dhcp")
	fwCfg, _ := uciGetConfig(r.Context(), "firewall")

	var dump struct {
		Interface []struct {
			Interface string `json:"interface"`
			Up        bool   `json:"up"`
		} `json:"interface"`
	}
	ubusCall(r.Context(), "network.interface", "dump", &dump)
	up := map[string]bool{}
	for _, i := range dump.Interface {
		up[i.Interface] = i.Up
	}

	reIface := regexp.MustCompile(`^sag_vlan([0-9]+)$`)
	vlans := []vlanOut{}
	for name, sec := range netCfg {
		m := reIface.FindStringSubmatch(name)
		if m == nil || sectStr(sec, ".type") != "interface" {
			continue
		}
		vid, _ := strconv.Atoi(m[1])
		o := vlanOut{
			VID: vid, IPAddr: sectStr(sec, "ipaddr"),
			Netmask: sectStr(sec, "netmask"), Access: "isolated",
			Up: up[name],
		}
		if dev, ok := netCfg[name+"_dev"]; ok {
			o.Port = sectStr(dev, "ifname")
		}
		if pool, ok := dhcpCfg[name]; ok {
			o.DHCP = sectStr(pool, "ignore") != "1"
			o.Start = sectStr(pool, "start")
			o.Limit = sectStr(pool, "limit")
		}
		if zn, ok := fwCfg[fmt.Sprintf("sag_zn_%d", vid)]; ok {
			o.Name = sectStr(zn, "name")
		}
		_, hasWan := fwCfg[fmt.Sprintf("sag_fw_%d_wan", vid)]
		_, hasLan := fwCfg[fmt.Sprintf("sag_fw_%d_lan", vid)]
		if hasWan && hasLan {
			o.Access = "wan_lan"
		} else if hasWan {
			o.Access = "wan"
		}
		vlans = append(vlans, o)
	}
	sort.Slice(vlans, func(i, j int) bool { return vlans[i].VID < vlans[j].VID })
	writeJSON(w, http.StatusOK, map[string]any{"vlans": vlans})
}

/* ---------- wizard: stvaranje ---------- */

type vlanIn struct {
	VID       int    `json:"vid"`
	Port      string `json:"port"`
	Name      string `json:"name"`
	CIDR      string `json:"cidr"` // adresa uređaja u mreži, npr. 192.168.20.1/24
	DHCP      *bool  `json:"dhcp"`
	Start     int    `json:"dhcp_start"`
	Limit     int    `json:"dhcp_limit"`
	Leasetime string `json:"dhcp_leasetime"`
	Access    string `json:"access"`
}

func (s *server) handleVlanCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in vlanIn
	if !decodeBody(w, r, &in) {
		return
	}
	in.Port = strings.TrimSpace(in.Port)
	in.Name = strings.ToLower(strings.TrimSpace(in.Name))
	in.Access = strings.TrimSpace(in.Access)
	in.Leasetime = strings.TrimSpace(in.Leasetime)
	if in.VID < 2 || in.VID > 4094 {
		writeErr(w, http.StatusBadRequest, "VLAN ID mora biti 2-4094")
		return
	}
	if !reDevName.MatchString(in.Port) || strings.Contains(in.Port, ".") {
		writeErr(w, http.StatusBadRequest, "neispravan port (npr. eth0)")
		return
	}
	if !reVlanSlug.MatchString(in.Name) {
		writeErr(w, http.StatusBadRequest,
			"naziv mreže: 2-10 malih slova/znamenki, počinje slovom (npr. guest)")
		return
	}
	ip, ipnet, err := net.ParseCIDR(strings.TrimSpace(in.CIDR))
	if err != nil || ip.To4() == nil {
		writeErr(w, http.StatusBadRequest,
			"adresa mora biti IPv4 CIDR, npr. 192.168.20.1/24")
		return
	}
	if ip.Equal(ipnet.IP) {
		writeErr(w, http.StatusBadRequest,
			"upiši adresu uređaja u mreži (npr. .1), ne adresu mreže")
		return
	}
	if in.Access == "" {
		in.Access = "wan"
	}
	if in.Access != "wan" && in.Access != "wan_lan" && in.Access != "isolated" {
		writeErr(w, http.StatusBadRequest, "pristup mora biti wan, wan_lan ili isolated")
		return
	}
	if in.Start == 0 {
		in.Start = 100
	}
	if in.Limit == 0 {
		in.Limit = 150
	}
	if in.Start < 2 || in.Limit < 1 || in.Start+in.Limit > 65000 {
		writeErr(w, http.StatusBadRequest, "neispravan DHCP raspon")
		return
	}
	if in.Leasetime == "" {
		in.Leasetime = "12h"
	}
	if !reLease.MatchString(in.Leasetime) {
		writeErr(w, http.StatusBadRequest, "neispravan leasetime (npr. 12h)")
		return
	}

	iface := fmt.Sprintf("sag_vlan%d", in.VID)
	netCfg, err := uciGetConfig(ctx, "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if _, exists := netCfg[iface]; exists {
		writeErr(w, http.StatusConflict, fmt.Sprintf("VLAN %d već postoji", in.VID))
		return
	}
	// zauzetost imena zone i preklapanje podmreže
	fwCfg, err := uciGetConfig(ctx, "firewall")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	for _, sec := range fwCfg {
		if sectStr(sec, ".type") == "zone" && sectStr(sec, "name") == in.Name {
			writeErr(w, http.StatusConflict, "zona s imenom "+in.Name+" već postoji")
			return
		}
	}
	for name, sec := range netCfg {
		if sectStr(sec, ".type") != "interface" || sectStr(sec, "ipaddr") == "" {
			continue
		}
		oIP := net.ParseIP(sectStr(sec, "ipaddr"))
		if oIP != nil && ipnet.Contains(oIP) {
			writeErr(w, http.StatusConflict,
				"podmreža se preklapa sa sučeljem "+name+" ("+oIP.String()+")")
			return
		}
	}

	backups := []string{}
	for _, cfg := range []string{networkConfig, dhcpConfig, firewallConfig} {
		bn, err := s.backupConfig(cfg)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
			return
		}
		backups = append(backups, bn)
	}

	vdev := fmt.Sprintf("%s.%d", in.Port, in.VID)
	mask := net.IP(ipnet.Mask).String()

	var nb strings.Builder
	fmt.Fprintf(&nb, "set network.%s_dev=device\n", iface)
	fmt.Fprintf(&nb, "set network.%s_dev.type=8021q\n", iface)
	fmt.Fprintf(&nb, "set network.%s_dev.ifname=%s\n", iface, in.Port)
	fmt.Fprintf(&nb, "set network.%s_dev.vid=%d\n", iface, in.VID)
	fmt.Fprintf(&nb, "set network.%s_dev.name=%s\n", iface, vdev)
	fmt.Fprintf(&nb, "set network.%s=interface\n", iface)
	fmt.Fprintf(&nb, "set network.%s.proto=static\n", iface)
	fmt.Fprintf(&nb, "set network.%s.device=%s\n", iface, vdev)
	fmt.Fprintf(&nb, "set network.%s.ipaddr=%s\n", iface, ip.String())
	fmt.Fprintf(&nb, "set network.%s.netmask=%s\n", iface, mask)
	nb.WriteString("commit network\n")
	if err := uciBatch(ctx, nb.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	var db strings.Builder
	fmt.Fprintf(&db, "set dhcp.%s=dhcp\n", iface)
	fmt.Fprintf(&db, "set dhcp.%s.interface=%s\n", iface, iface)
	fmt.Fprintf(&db, "set dhcp.%s.start=%d\n", iface, in.Start)
	fmt.Fprintf(&db, "set dhcp.%s.limit=%d\n", iface, in.Limit)
	fmt.Fprintf(&db, "set dhcp.%s.leasetime=%s\n", iface, in.Leasetime)
	if in.DHCP != nil && !*in.DHCP {
		fmt.Fprintf(&db, "set dhcp.%s.ignore=1\n", iface)
	}
	db.WriteString("commit dhcp\n")
	if err := uciBatch(ctx, db.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	var fb strings.Builder
	zn := fmt.Sprintf("sag_zn_%d", in.VID)
	fmt.Fprintf(&fb, "set firewall.%s=zone\n", zn)
	fmt.Fprintf(&fb, "set firewall.%s.name=%s\n", zn, in.Name)
	fmt.Fprintf(&fb, "add_list firewall.%s.network=%s\n", zn, iface)
	fmt.Fprintf(&fb, "set firewall.%s.input=ACCEPT\n", zn)
	fmt.Fprintf(&fb, "set firewall.%s.output=ACCEPT\n", zn)
	fmt.Fprintf(&fb, "set firewall.%s.forward=REJECT\n", zn)
	if in.Access == "wan" || in.Access == "wan_lan" {
		fmt.Fprintf(&fb, "set firewall.sag_fw_%d_wan=forwarding\n", in.VID)
		fmt.Fprintf(&fb, "set firewall.sag_fw_%d_wan.src=%s\n", in.VID, in.Name)
		fmt.Fprintf(&fb, "set firewall.sag_fw_%d_wan.dest=wan\n", in.VID)
	}
	if in.Access == "wan_lan" {
		fmt.Fprintf(&fb, "set firewall.sag_fw_%d_lan=forwarding\n", in.VID)
		fmt.Fprintf(&fb, "set firewall.sag_fw_%d_lan.src=%s\n", in.VID, in.Name)
		fmt.Fprintf(&fb, "set firewall.sag_fw_%d_lan.dest=lan\n", in.VID)
		fmt.Fprintf(&fb, "set firewall.sag_fw_lan_%d=forwarding\n", in.VID)
		fmt.Fprintf(&fb, "set firewall.sag_fw_lan_%d.src=lan\n", in.VID)
		fmt.Fprintf(&fb, "set firewall.sag_fw_lan_%d.dest=%s\n", in.VID, in.Name)
	}
	fb.WriteString("commit firewall\n")
	if err := uciBatch(ctx, fb.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	for _, svc := range [][2]string{{"network", "reload"}, {"dnsmasq", "reload"},
		{"firewall", "reload"}} {
		if err := serviceReload(ctx, svc[0], svc[1]); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"created": iface, "device": vdev, "backups": backups,
	})
}

/* ---------- brisanje ---------- */

func (s *server) handleVlanDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vid, err := strconv.Atoi(r.PathValue("vid"))
	if err != nil || vid < 2 || vid > 4094 {
		writeErr(w, http.StatusBadRequest, "neispravan VLAN ID")
		return
	}
	iface := fmt.Sprintf("sag_vlan%d", vid)

	netCfg, err := uciGetConfig(ctx, "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if _, ok := netCfg[iface]; !ok {
		writeErr(w, http.StatusNotFound, "VLAN ne postoji")
		return
	}
	dhcpCfg, _ := uciGetConfig(ctx, "dhcp")
	fwCfg, _ := uciGetConfig(ctx, "firewall")

	backups := []string{}
	for _, cfg := range []string{networkConfig, dhcpConfig, firewallConfig} {
		bn, err := s.backupConfig(cfg)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
			return
		}
		backups = append(backups, bn)
	}

	var fb strings.Builder
	for _, sect := range []string{
		fmt.Sprintf("sag_zn_%d", vid),
		fmt.Sprintf("sag_fw_%d_wan", vid),
		fmt.Sprintf("sag_fw_%d_lan", vid),
		fmt.Sprintf("sag_fw_lan_%d", vid),
	} {
		if _, ok := fwCfg[sect]; ok {
			fmt.Fprintf(&fb, "delete firewall.%s\n", sect)
		}
	}
	if fb.Len() > 0 {
		fb.WriteString("commit firewall\n")
		if err := uciBatch(ctx, fb.String()); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if _, ok := dhcpCfg[iface]; ok {
		if err := uciBatch(ctx, fmt.Sprintf("delete dhcp.%s\ncommit dhcp\n", iface)); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	var nb strings.Builder
	fmt.Fprintf(&nb, "delete network.%s\n", iface)
	if _, ok := netCfg[iface+"_dev"]; ok {
		fmt.Fprintf(&nb, "delete network.%s_dev\n", iface)
	}
	nb.WriteString("commit network\n")
	if err := uciBatch(ctx, nb.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	for _, svc := range [][2]string{{"firewall", "reload"}, {"dnsmasq", "reload"},
		{"network", "reload"}} {
		if err := serviceReload(ctx, svc[0], svc[1]); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": iface, "backups": backups,
	})
}
