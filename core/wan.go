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

// WAN sučelja: "wan" (postojeće OpenWrt sučelje) + dodatna sag_wan2..sag_wan9.
// Dodatna sučelja se pri stvaranju dodaju u network listu wan zone (jedina
// izmjena tuđe sekcije — izravna posljedica korisničke radnje, uz backup).
var reWanName = regexp.MustCompile(`^(wan|sag_wan[2-9])$`)
var reDevName = regexp.MustCompile(`^eth[0-9]{1,2}(\.[0-9]{1,4})?$`)

/* ---------- pregled ---------- */

type wanOut struct {
	Name     string   `json:"name"`
	Proto    string   `json:"proto"`
	Device   string   `json:"device"`
	Metric   string   `json:"metric"`
	IPAddrs  []string `json:"ipaddrs"`
	Gateway  string   `json:"gateway"`
	DNS      []string `json:"dns"`
	Username string   `json:"username"`
	Up       bool     `json:"up"`
	RtIPv4   []string `json:"runtime_ipv4"`
	Managed  bool     `json:"managed_by_saguaro"`
}

func (s *server) handleWANList(w http.ResponseWriter, r *http.Request) {
	cfg, err := uciGetConfig(r.Context(), "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	// runtime stanje logičkih sučelja
	var dump struct {
		Interface []struct {
			Interface string `json:"interface"`
			Up        bool   `json:"up"`
			IPv4      []struct {
				Address string `json:"address"`
				Mask    int    `json:"mask"`
			} `json:"ipv4-address"`
		} `json:"interface"`
	}
	ubusCall(r.Context(), "network.interface", "dump", &dump)
	rt := map[string]*struct {
		up   bool
		ipv4 []string
	}{}
	for _, i := range dump.Interface {
		e := &struct {
			up   bool
			ipv4 []string
		}{up: i.Up}
		for _, a := range i.IPv4 {
			e.ipv4 = append(e.ipv4, fmt.Sprintf("%s/%d", a.Address, a.Mask))
		}
		rt[i.Interface] = e
	}

	wans := []wanOut{}
	for name, sec := range cfg {
		if sectStr(sec, ".type") != "interface" || !reWanName.MatchString(name) {
			continue
		}
		o := wanOut{
			Name: name, Proto: sectStr(sec, "proto"),
			Device: sectStr(sec, "device"), Metric: sectStr(sec, "metric"),
			IPAddrs: sectList(sec, "ipaddr"), Gateway: sectStr(sec, "gateway"),
			DNS: sectList(sec, "dns"), Username: sectStr(sec, "username"),
			Managed: strings.HasPrefix(name, sagPrefix),
		}
		if e, ok := rt[name]; ok {
			o.Up = e.up
			o.RtIPv4 = e.ipv4
		}
		wans = append(wans, o)
	}
	sort.Slice(wans, func(i, j int) bool { return wans[i].Name < wans[j].Name })

	// fizički portovi i tko ih koristi (za odabir u GUI-ju)
	used := map[string]string{}
	for name, sec := range cfg {
		if sectStr(sec, ".type") == "interface" {
			if d := sectStr(sec, "device"); d != "" {
				used[d] = name
			}
		}
		if sectStr(sec, ".type") == "device" && sectStr(sec, "type") == "bridge" {
			for _, p := range sectList(sec, "ports") {
				used[p] = sectStr(sec, "name")
			}
		}
	}
	var devs map[string]ubusDevice
	devices := []map[string]any{}
	if err := ubusCall(r.Context(), "network.device", "status", &devs); err == nil {
		names := make([]string, 0, len(devs))
		for n := range devs {
			if strings.HasPrefix(n, "eth") {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			devices = append(devices, map[string]any{
				"name": n, "carrier": devs[n].Carrier, "used_by": used[n],
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"wans": wans, "devices": devices})
}

/* ---------- konfiguracija ---------- */

type wanIn struct {
	Proto    string `json:"proto"` // dhcp | static | pppoe
	Device   string `json:"device"`
	IPAddrs  string `json:"ipaddrs"` // CIDR-ovi odvojeni razmakom/zarezom
	Gateway  string `json:"gateway"`
	DNS      string `json:"dns"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// wanMetric daje stabilan metric po sučelju (wan=10, sag_wanN=N*10)
// da više WAN-ova može imati default rutu istovremeno.
func wanMetric(name string) int {
	if name == "wan" {
		return 10
	}
	n, _ := strconv.Atoi(strings.TrimPrefix(name, "sag_wan"))
	return n * 10
}

func (s *server) handleWANSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := r.PathValue("name")
	if !reWanName.MatchString(name) {
		writeErr(w, http.StatusBadRequest,
			"ime mora biti wan ili sag_wan2..sag_wan9")
		return
	}
	var in wanIn
	if !decodeBody(w, r, &in) {
		return
	}
	in.Proto = strings.TrimSpace(in.Proto)
	in.Device = strings.TrimSpace(in.Device)
	if in.Proto != "dhcp" && in.Proto != "static" && in.Proto != "pppoe" {
		writeErr(w, http.StatusBadRequest, "proto mora biti dhcp, static ili pppoe")
		return
	}
	if !reDevName.MatchString(in.Device) {
		writeErr(w, http.StatusBadRequest, "neispravan uređaj (npr. eth1)")
		return
	}

	ipaddrs := []string{}
	for _, a := range strings.FieldsFunc(in.IPAddrs, func(r rune) bool {
		return r == ' ' || r == ',' || r == ';'
	}) {
		if ip, _, err := net.ParseCIDR(a); err != nil || ip.To4() == nil {
			writeErr(w, http.StatusBadRequest,
				"neispravna adresa "+a+" — očekujem IPv4 CIDR (npr. 203.0.113.2/29)")
			return
		}
		ipaddrs = append(ipaddrs, a)
	}
	gw := strings.TrimSpace(in.Gateway)
	if gw != "" && net.ParseIP(gw) == nil {
		writeErr(w, http.StatusBadRequest, "neispravan gateway")
		return
	}
	dns := []string{}
	for _, d := range strings.Fields(in.DNS) {
		if net.ParseIP(d) == nil {
			writeErr(w, http.StatusBadRequest, "neispravan DNS: "+d)
			return
		}
		dns = append(dns, d)
	}
	if in.Proto == "static" && len(ipaddrs) == 0 {
		writeErr(w, http.StatusBadRequest, "static WAN traži bar jednu adresu (CIDR)")
		return
	}
	if in.Proto == "pppoe" && (in.Username == "" || in.Password == "") {
		writeErr(w, http.StatusBadRequest, "pppoe traži korisničko ime i lozinku")
		return
	}

	backups := []string{}
	bn, err := s.backupConfig(networkConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}
	backups = append(backups, bn)

	cfg, err := uciGetConfig(ctx, "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	existing, isNew := cfg[name], false
	if existing == nil {
		existing, isNew = uciSection{}, true
	}

	var b strings.Builder
	fmt.Fprintf(&b, "set network.%s=interface\n", name)
	fmt.Fprintf(&b, "set network.%s.proto=%s\n", name, in.Proto)
	fmt.Fprintf(&b, "set network.%s.device=%s\n", name, in.Device)
	fmt.Fprintf(&b, "set network.%s.metric=%d\n", name, wanMetric(name))
	// očisti opcije prethodnog prototipa
	for _, opt := range []string{"ipaddr", "netmask", "gateway", "dns",
		"username", "password"} {
		if _, has := existing[opt]; has {
			fmt.Fprintf(&b, "delete network.%s.%s\n", name, opt)
		}
	}
	switch in.Proto {
	case "static":
		for _, a := range ipaddrs {
			fmt.Fprintf(&b, "add_list network.%s.ipaddr=%s\n", name, a)
		}
		if gw != "" {
			fmt.Fprintf(&b, "set network.%s.gateway=%s\n", name, gw)
		}
		for _, d := range dns {
			fmt.Fprintf(&b, "add_list network.%s.dns=%s\n", name, d)
		}
	case "pppoe":
		fmt.Fprintf(&b, "set network.%s.username=%s\n", name, uciQuote(in.Username))
		fmt.Fprintf(&b, "set network.%s.password=%s\n", name, uciQuote(in.Password))
	case "dhcp":
		if len(dns) > 0 {
			fmt.Fprintf(&b, "set network.%s.peerdns=0\n", name)
			for _, d := range dns {
				fmt.Fprintf(&b, "add_list network.%s.dns=%s\n", name, d)
			}
		} else if _, has := existing["peerdns"]; has {
			fmt.Fprintf(&b, "delete network.%s.peerdns\n", name)
		}
	}
	b.WriteString("commit network\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// novo sag_wan sučelje mora u wan zonu da dobije masq/mtu_fix i pravila
	if isNew {
		fb, err := s.backupConfig(firewallConfig)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
			return
		}
		backups = append(backups, fb)
		fw, err := uciGetConfig(ctx, "firewall")
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		for sect, sec := range fw {
			if sectStr(sec, ".type") != "zone" || sectStr(sec, "name") != "wan" {
				continue
			}
			hasIt := false
			for _, n := range sectList(sec, "network") {
				if n == name {
					hasIt = true
				}
			}
			if !hasIt {
				batch := fmt.Sprintf("add_list firewall.%s.network=%s\ncommit firewall\n",
					sect, name)
				if err := uciBatch(ctx, batch); err != nil {
					writeErr(w, http.StatusInternalServerError, err.Error())
					return
				}
				if err := serviceReload(ctx, "firewall", "reload"); err != nil {
					writeErr(w, http.StatusInternalServerError, err.Error())
					return
				}
			}
			break
		}
	}

	if err := serviceReload(ctx, "network", "reload"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"applied": name, "backups": backups,
	})
}

func (s *server) handleWANDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := r.PathValue("name")
	if !strings.HasPrefix(name, "sag_wan") || !reWanName.MatchString(name) {
		writeErr(w, http.StatusBadRequest,
			"briše se samo dodatni WAN (sag_wan*); glavni wan se uređuje")
		return
	}
	cfg, err := uciGetConfig(ctx, "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if _, ok := cfg[name]; !ok {
		writeErr(w, http.StatusNotFound, "sučelje ne postoji")
		return
	}
	nb, err := s.backupConfig(networkConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}
	fb, err := s.backupConfig(firewallConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}

	fw, err := uciGetConfig(ctx, "firewall")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var fbatch strings.Builder
	for sect, sec := range fw {
		if sectStr(sec, ".type") == "zone" && sectStr(sec, "name") == "wan" {
			for _, n := range sectList(sec, "network") {
				if n == name {
					fmt.Fprintf(&fbatch, "del_list firewall.%s.network=%s\n", sect, name)
				}
			}
		}
	}
	if fbatch.Len() > 0 {
		fbatch.WriteString("commit firewall\n")
		if err := uciBatch(ctx, fbatch.String()); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	batch := fmt.Sprintf("delete network.%s\ncommit network\n", name)
	if err := uciBatch(ctx, batch); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := serviceReload(ctx, "firewall", "reload"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := serviceReload(ctx, "network", "reload"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": name, "backups": []string{nb, fb},
	})
}
