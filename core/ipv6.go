package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// IPv6 kao jedan prekidač.
//
// Uređaj i OpenWrt sve dijelove IPv6 podrške već imaju, ali razbacane po tri
// konfiguracije (network, dhcp, firewall). Ovdje se pale i gase zajedno, na
// svim mrežama odjednom, da ne ostane pola upaljeno.
//
// Tri stanja:
//
//	isključen  — mreže ne objavljuju IPv6, uređaj ga ne traži od pružatelja
//	automatski — prefiks se traži od pružatelja (DHCPv6-PD) i dijeli mrežama
//	ručno      — koristi se vlastiti prefiks (ULA ili statički dobiven)
//
// Bitno: IPv6 nema NAT, pa svaki uređaj u mreži dobiva javnu adresu. Jedina
// zaštita je vatrozid, koji zadano odbija sve dolazno — objava servera preko
// IPv6 ide izričitim pravilom, nikad "usput".

const wan6Iface = "wan6"

type v6Network struct {
	Name      string   `json:"name"`
	DHCPSec   string   `json:"dhcp_section"`
	IP6Assign string   `json:"ip6assign"`
	RA        string   `json:"ra"`
	DHCPv6    string   `json:"dhcpv6"`
	Addresses []string `json:"addresses"`
}

// v6LocalNetworks vraća lokalne mreže koje smiju objavljivati IPv6:
// sve osim loopbacka, WAN-ova i VPN sučelja (tunel adresira sam VPN).
func (s *server) v6LocalNetworks(ctx context.Context, netCfg, dhcpCfg map[string]uciSection) []v6Network {
	wan := s.wanIfaces(ctx)
	out := []v6Network{}
	for name, sec := range netCfg {
		if sectStr(sec, ".type") != "interface" {
			continue
		}
		if name == "loopback" || wan[name] || strings.HasPrefix(name, "sag_wg") ||
			strings.HasPrefix(name, "sag_ovpn") || name == wan6Iface {
			continue
		}
		proto := sectStr(sec, "proto")
		if proto != "static" && proto != "dhcp" {
			continue
		}
		n := v6Network{Name: name, IP6Assign: sectStr(sec, "ip6assign")}
		for dn, ds := range dhcpCfg {
			if sectStr(ds, ".type") == "dhcp" && sectStr(ds, "interface") == name {
				n.DHCPSec = dn
				n.RA = sectStr(ds, "ra")
				n.DHCPv6 = sectStr(ds, "dhcpv6")
			}
		}
		out = append(out, n)
	}
	return out
}

// v6Addresses čita stvarne IPv6 adrese sučelja s uređaja.
func v6Addresses(ctx context.Context) (map[string][]string, string) {
	addrs := map[string][]string{}
	prefix := ""
	var dump struct {
		Interface []struct {
			Interface   string `json:"interface"`
			IPv6Address []struct {
				Address string `json:"address"`
				Mask    int    `json:"mask"`
			} `json:"ipv6-address"`
			IPv6Prefix []struct {
				Address string `json:"address"`
				Mask    int    `json:"mask"`
			} `json:"ipv6-prefix"`
			IPv6PrefixAssign []struct {
				Address string `json:"address"`
				Mask    int    `json:"mask"`
			} `json:"ipv6-prefix-assignment"`
		} `json:"interface"`
	}
	if err := ubusCall(ctx, "network.interface", "dump", &dump); err != nil {
		return addrs, prefix
	}
	for _, i := range dump.Interface {
		for _, a := range i.IPv6Address {
			addrs[i.Interface] = append(addrs[i.Interface],
				fmt.Sprintf("%s/%d", a.Address, a.Mask))
		}
		for _, a := range i.IPv6PrefixAssign {
			addrs[i.Interface] = append(addrs[i.Interface],
				fmt.Sprintf("%s/%d", a.Address, a.Mask))
		}
		for _, p := range i.IPv6Prefix {
			if i.Interface == wan6Iface {
				prefix = fmt.Sprintf("%s/%d", p.Address, p.Mask)
			}
		}
	}
	return addrs, prefix
}

// v6Mode izvodi stanje iz same konfiguracije, a ne iz zapamćene postavke —
// tako sučelje pokazuje ono što je stvarno na uređaju i kad je netko mijenjao
// izvan Saguara.
func v6Mode(netCfg map[string]uciSection, nets []v6Network) string {
	serving := false
	for _, n := range nets {
		if n.RA == "server" && n.IP6Assign != "" {
			serving = true
		}
	}
	if !serving {
		return "off"
	}
	w6, ok := netCfg[wan6Iface]
	if ok && sectStr(w6, "disabled") != "1" {
		return "auto"
	}
	return "manual"
}

func (s *server) handleIPv6Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	netCfg, err := uciGetConfig(ctx, "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	dhcpCfg, err := uciGetConfig(ctx, "dhcp")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	nets := s.v6LocalNetworks(ctx, netCfg, dhcpCfg)
	addrs, prefix := v6Addresses(ctx)
	for i := range nets {
		nets[i].Addresses = addrs[nets[i].Name]
	}
	ula := ""
	if g, ok := netCfg["globals"]; ok {
		ula = sectStr(g, "ula_prefix")
	}
	w6 := map[string]any{"configured": false}
	if sec, ok := netCfg[wan6Iface]; ok {
		w6 = map[string]any{
			"configured": true,
			"proto":      sectStr(sec, "proto"),
			"disabled":   sectStr(sec, "disabled") == "1",
			"addresses":  addrs[wan6Iface],
			"prefix":     prefix,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":       v6Mode(netCfg, nets),
		"ula_prefix": ula,
		"wan6":       w6,
		"networks":   nets,
	})
}

type ipv6In struct {
	Mode   string `json:"mode"`   // off | auto | manual
	Prefix string `json:"prefix"` // samo za ručni način (npr. fd12:3456:789a::/48)
}

func (s *server) handleIPv6Set(w http.ResponseWriter, r *http.Request) {
	var in ipv6In
	if !decodeBody(w, r, &in) {
		return
	}
	in.Mode = strings.TrimSpace(in.Mode)
	in.Prefix = strings.TrimSpace(in.Prefix)
	switch in.Mode {
	case "off", "auto", "manual":
	default:
		writeErr(w, http.StatusBadRequest, "način mora biti off, auto ili manual")
		return
	}
	if in.Mode == "manual" {
		ip, ipnet, err := net.ParseCIDR(in.Prefix)
		if err != nil || ip.To4() != nil {
			writeErr(w, http.StatusBadRequest,
				"prefiks mora biti IPv6 mreža, npr. fd12:3456:789a::/48")
			return
		}
		ones, _ := ipnet.Mask.Size()
		if ones > 64 {
			writeErr(w, http.StatusBadRequest,
				"prefiks mora biti /64 ili širi — svaka mreža treba svoj /64")
			return
		}
	}

	ctx := r.Context()
	netCfg, err := uciGetConfig(ctx, "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	dhcpCfg, err := uciGetConfig(ctx, "dhcp")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	nets := s.v6LocalNetworks(ctx, netCfg, dhcpCfg)
	if len(nets) == 0 {
		writeErr(w, http.StatusConflict, "nema lokalnih mreža za IPv6")
		return
	}

	backupNet, err := s.backupConfig("/etc/config/network")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}
	if _, err := s.backupConfig("/etc/config/dhcp"); err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}

	var b strings.Builder
	on := in.Mode != "off"
	for _, n := range nets {
		if on {
			if n.IP6Assign == "" {
				fmt.Fprintf(&b, "set network.%s.ip6assign=64\n", n.Name)
			}
		} else {
			if n.IP6Assign != "" {
				fmt.Fprintf(&b, "delete network.%s.ip6assign\n", n.Name)
			}
		}
		if n.DHCPSec == "" {
			continue // mreža bez DHCP sekcije (npr. bez poola) — RA nema gdje
		}
		if on {
			fmt.Fprintf(&b, "set dhcp.%s.ra=server\n", n.DHCPSec)
			fmt.Fprintf(&b, "set dhcp.%s.dhcpv6=server\n", n.DHCPSec)
			fmt.Fprintf(&b, "set dhcp.%s.ra_slaac=1\n", n.DHCPSec)
			fmt.Fprintf(&b, "delete dhcp.%s.ra_flags\n", n.DHCPSec)
			fmt.Fprintf(&b, "add_list dhcp.%s.ra_flags=managed-config\n", n.DHCPSec)
			fmt.Fprintf(&b, "add_list dhcp.%s.ra_flags=other-config\n", n.DHCPSec)
		} else {
			fmt.Fprintf(&b, "set dhcp.%s.ra=disabled\n", n.DHCPSec)
			fmt.Fprintf(&b, "set dhcp.%s.dhcpv6=disabled\n", n.DHCPSec)
		}
	}

	// WAN strana: prefiks od pružatelja traži se samo u automatskom načinu
	if in.Mode == "auto" {
		wanDev := ""
		if wsec, ok := netCfg["wan"]; ok {
			wanDev = sectStr(wsec, "device")
		}
		if _, ok := netCfg[wan6Iface]; !ok {
			fmt.Fprintf(&b, "set network.%s=interface\n", wan6Iface)
		}
		fmt.Fprintf(&b, "set network.%s.proto=dhcpv6\n", wan6Iface)
		if wanDev != "" {
			fmt.Fprintf(&b, "set network.%s.device=%s\n", wan6Iface, uciQuote(wanDev))
		}
		fmt.Fprintf(&b, "set network.%s.reqaddress=try\n", wan6Iface)
		fmt.Fprintf(&b, "set network.%s.reqprefix=auto\n", wan6Iface)
		if sec, ok := netCfg[wan6Iface]; ok && sectStr(sec, "disabled") != "" {
			fmt.Fprintf(&b, "delete network.%s.disabled\n", wan6Iface)
		}
	} else if sec, ok := netCfg[wan6Iface]; ok {
		if sectStr(sec, "disabled") != "1" {
			fmt.Fprintf(&b, "set network.%s.disabled=1\n", wan6Iface)
		}
	}
	if in.Mode == "manual" {
		fmt.Fprintf(&b, "set network.globals.ula_prefix=%s\n", uciQuote(in.Prefix))
	}
	b.WriteString("commit network\ncommit dhcp\n")

	if err := uciBatch(ctx, b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := serviceReload(ctx, "network", "reload"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// odhcpd objavljuje prefikse; bez ponovnog pokretanja ostaje na starom
	_ = serviceReload(ctx, "odhcpd", "restart")

	writeJSON(w, http.StatusOK, map[string]any{
		"mode":     in.Mode,
		"networks": len(nets),
		"backup":   backupNet,
	})
}
