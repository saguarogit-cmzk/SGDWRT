package main

import (
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// Multi-WAN kroz mwan3: Saguaro generira CIJELU /etc/config/mwan3 (uz backup)
// — datoteka dolazi s paketom kao primjer i nema tuđih ručnih sekcija.
// Načini rada: failover (redoslijed po prioritetu) ili balanced (težine).
// Runtime stanje se čita kroz `ubus call mwan3 status` (D-007).
const mwan3Config = "/etc/config/mwan3"

var reRuleLabel = regexp.MustCompile(`^[a-z][a-z0-9]{0,7}$`)

type mwWan struct {
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Priority int    `json:"priority"` // failover: 1 = primarni
	Weight   int    `json:"weight"`   // balanced: veći = više prometa
	TrackIPs string `json:"track_ips"`
}

type mwRule struct {
	Label    string `json:"label"`
	SrcIP    string `json:"src_ip"`
	DestIP   string `json:"dest_ip"`
	DestPort string `json:"dest_port"`
	Proto    string `json:"proto"`
	UseWan   string `json:"use_wan"`
}

/* ---------- pregled ---------- */

func (s *server) handleMultiwanGet(w http.ResponseWriter, r *http.Request) {
	_, lookErr := exec.LookPath("mwan3")
	cfg, err := uciGetConfig(r.Context(), "mwan3")
	if err != nil {
		cfg = map[string]uciSection{}
	}
	netCfg, err := uciGetConfig(r.Context(), "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	managed := false
	if _, ok := cfg["sag_policy"]; ok {
		managed = true
	}

	wans := []mwWan{}
	for name, sec := range netCfg {
		if sectStr(sec, ".type") != "interface" || !reWanName.MatchString(name) {
			continue
		}
		o := mwWan{Name: name, Priority: len(wans) + 1, Weight: 1,
			TrackIPs: "1.1.1.1 8.8.8.8"}
		if isec, ok := cfg[name]; ok && managed {
			o.Enabled = sectStr(isec, "enabled") == "1"
			if t := sectList(isec, "track_ip"); len(t) > 0 {
				o.TrackIPs = strings.Join(t, " ")
			}
		}
		if m, ok := cfg["sag_m_"+name]; ok {
			fmt.Sscanf(sectStr(m, "metric"), "%d", &o.Priority)
			fmt.Sscanf(sectStr(m, "weight"), "%d", &o.Weight)
		}
		wans = append(wans, o)
	}
	sort.Slice(wans, func(i, j int) bool { return wans[i].Name < wans[j].Name })

	rules := []mwRule{}
	for name, sec := range cfg {
		if sectStr(sec, ".type") != "rule" || !strings.HasPrefix(name, "sag_r_") {
			continue
		}
		rules = append(rules, mwRule{
			Label:    strings.TrimPrefix(name, "sag_r_"),
			SrcIP:    sectStr(sec, "src_ip"),
			DestIP:   sectStr(sec, "dest_ip"),
			DestPort: sectStr(sec, "dest_port"),
			Proto:    sectStr(sec, "proto"),
			UseWan:   strings.TrimPrefix(sectStr(sec, "use_policy"), "sag_po_"),
		})
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Label < rules[j].Label })

	var status map[string]any
	ubusCall(r.Context(), "mwan3", "status", &status)

	writeJSON(w, http.StatusOK, map[string]any{
		"installed": lookErr == nil,
		"managed":   managed,
		"enabled":   s.getSetting("mw_enabled", "0") == "1",
		"mode":      s.getSetting("mw_mode", "failover"),
		"wans":      wans,
		"rules":     rules,
		"status":    status,
	})
}

/* ---------- konfiguracija ---------- */

type multiwanIn struct {
	Enabled *bool    `json:"enabled"`
	Mode    string   `json:"mode"`
	Wans    []mwWan  `json:"wans"`
	Rules   []mwRule `json:"rules"`
}

func (s *server) handleMultiwanSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in multiwanIn
	if !decodeBody(w, r, &in) {
		return
	}
	enabled := in.Enabled == nil || *in.Enabled
	if in.Mode == "" {
		in.Mode = "failover"
	}
	if in.Mode != "failover" && in.Mode != "balanced" {
		writeErr(w, http.StatusBadRequest, "mode mora biti failover ili balanced")
		return
	}

	netCfg, err := uciGetConfig(ctx, "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	names := map[string]bool{}
	activeCount := 0
	for i := range in.Wans {
		x := &in.Wans[i]
		if !reWanName.MatchString(x.Name) {
			writeErr(w, http.StatusBadRequest, "neispravno WAN ime: "+x.Name)
			return
		}
		if _, ok := netCfg[x.Name]; !ok {
			writeErr(w, http.StatusBadRequest, "WAN sučelje "+x.Name+" ne postoji")
			return
		}
		if names[x.Name] {
			writeErr(w, http.StatusBadRequest, "WAN "+x.Name+" naveden dvaput")
			return
		}
		names[x.Name] = true
		if x.Priority < 1 || x.Priority > 9 {
			writeErr(w, http.StatusBadRequest, "prioritet mora biti 1-9 ("+x.Name+")")
			return
		}
		if x.Weight < 1 || x.Weight > 10 {
			writeErr(w, http.StatusBadRequest, "udio mora biti 1-10 ("+x.Name+")")
			return
		}
		ips := strings.Fields(x.TrackIPs)
		if x.Enabled && len(ips) == 0 {
			writeErr(w, http.StatusBadRequest,
				"za nadzor veze "+x.Name+" treba bar jedna IP adresa")
			return
		}
		for _, ip := range ips {
			if net.ParseIP(ip) == nil {
				writeErr(w, http.StatusBadRequest, "neispravna nadzorna IP: "+ip)
				return
			}
		}
		if x.Enabled {
			activeCount++
		}
	}
	if enabled && activeCount == 0 {
		writeErr(w, http.StatusBadRequest, "uključi bar jedan WAN za multi-WAN")
		return
	}
	usedLabels := map[string]bool{}
	for i := range in.Rules {
		x := &in.Rules[i]
		x.Label = strings.ToLower(strings.TrimSpace(x.Label))
		if !reRuleLabel.MatchString(x.Label) {
			writeErr(w, http.StatusBadRequest,
				"oznaka pravila: 1-8 malih slova/znamenki ("+x.Label+")")
			return
		}
		if usedLabels[x.Label] {
			writeErr(w, http.StatusBadRequest, "oznaka pravila ponovljena: "+x.Label)
			return
		}
		usedLabels[x.Label] = true
		if !names[x.UseWan] {
			writeErr(w, http.StatusBadRequest,
				"pravilo "+x.Label+": WAN "+x.UseWan+" nije na popisu")
			return
		}
		if x.SrcIP != "" && !validAddr(x.SrcIP) {
			writeErr(w, http.StatusBadRequest, "pravilo "+x.Label+": neispravan izvor")
			return
		}
		if x.DestIP != "" && !validAddr(x.DestIP) {
			writeErr(w, http.StatusBadRequest, "pravilo "+x.Label+": neispravno odredište")
			return
		}
		if x.DestPort != "" && !validPortSpec(x.DestPort) {
			writeErr(w, http.StatusBadRequest, "pravilo "+x.Label+": neispravan port")
			return
		}
		if x.Proto != "" && x.Proto != "tcp" && x.Proto != "udp" {
			writeErr(w, http.StatusBadRequest, "pravilo "+x.Label+": proto tcp/udp ili prazno")
			return
		}
		if x.DestPort != "" && x.Proto == "" {
			writeErr(w, http.StatusBadRequest,
				"pravilo "+x.Label+": port traži odabran protokol")
			return
		}
	}

	backupName, err := s.backupConfig(mwan3Config)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}

	// generiraj cijelu konfiguraciju: obriši sve sekcije pa složi naše
	old, err := uciGetConfig(ctx, "mwan3")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var b strings.Builder
	for name := range old {
		fmt.Fprintf(&b, "delete mwan3.%s\n", name)
	}
	b.WriteString("set mwan3.globals=globals\n")
	b.WriteString("set mwan3.globals.mmx_mask=0x3F00\n")

	ruleWans := map[string]bool{}
	for _, x := range in.Rules {
		ruleWans[x.UseWan] = true
	}
	for _, x := range in.Wans {
		en := 0
		if enabled && x.Enabled {
			en = 1
		}
		fmt.Fprintf(&b, "set mwan3.%s=interface\n", x.Name)
		fmt.Fprintf(&b, "set mwan3.%s.enabled=%d\n", x.Name, en)
		for _, ip := range strings.Fields(x.TrackIPs) {
			fmt.Fprintf(&b, "add_list mwan3.%s.track_ip=%s\n", x.Name, ip)
		}
		fmt.Fprintf(&b, "set mwan3.%s.family=ipv4\n", x.Name)
		fmt.Fprintf(&b, "set mwan3.%s.track_method=ping\n", x.Name)
		fmt.Fprintf(&b, "set mwan3.%s.reliability=1\n", x.Name)
		fmt.Fprintf(&b, "set mwan3.%s.count=1\n", x.Name)
		fmt.Fprintf(&b, "set mwan3.%s.timeout=2\n", x.Name)
		fmt.Fprintf(&b, "set mwan3.%s.interval=5\n", x.Name)
		fmt.Fprintf(&b, "set mwan3.%s.down=3\n", x.Name)
		fmt.Fprintf(&b, "set mwan3.%s.up=3\n", x.Name)
		fmt.Fprintf(&b, "set mwan3.%s.initial_state=online\n", x.Name)

		metric, weight := x.Priority, 1
		if in.Mode == "balanced" {
			metric, weight = 1, x.Weight
		}
		fmt.Fprintf(&b, "set mwan3.sag_m_%s=member\n", x.Name)
		fmt.Fprintf(&b, "set mwan3.sag_m_%s.interface=%s\n", x.Name, x.Name)
		fmt.Fprintf(&b, "set mwan3.sag_m_%s.metric=%d\n", x.Name, metric)
		fmt.Fprintf(&b, "set mwan3.sag_m_%s.weight=%d\n", x.Name, weight)

		// zasebna politika po WAN-u za pravila usmjeravanja
		if ruleWans[x.Name] {
			fmt.Fprintf(&b, "set mwan3.sag_po_%s=policy\n", x.Name)
			fmt.Fprintf(&b, "add_list mwan3.sag_po_%s.use_member=sag_m_%s\n", x.Name, x.Name)
			fmt.Fprintf(&b, "set mwan3.sag_po_%s.last_resort=default\n", x.Name)
		}
	}

	b.WriteString("set mwan3.sag_policy=policy\n")
	for _, x := range in.Wans {
		if x.Enabled {
			fmt.Fprintf(&b, "add_list mwan3.sag_policy.use_member=sag_m_%s\n", x.Name)
		}
	}
	b.WriteString("set mwan3.sag_policy.last_resort=default\n")

	// PBR pravila prije zadanog pravila (redoslijed sekcija = redoslijed provjere)
	for _, x := range in.Rules {
		sn := "sag_r_" + x.Label
		fmt.Fprintf(&b, "set mwan3.%s=rule\n", sn)
		if x.SrcIP != "" {
			fmt.Fprintf(&b, "set mwan3.%s.src_ip=%s\n", sn, x.SrcIP)
		}
		if x.DestIP != "" {
			fmt.Fprintf(&b, "set mwan3.%s.dest_ip=%s\n", sn, x.DestIP)
		}
		if x.DestPort != "" {
			fmt.Fprintf(&b, "set mwan3.%s.dest_port=%s\n", sn, x.DestPort)
			fmt.Fprintf(&b, "set mwan3.%s.proto=%s\n", sn, x.Proto)
		} else if x.Proto != "" {
			fmt.Fprintf(&b, "set mwan3.%s.proto=%s\n", sn, x.Proto)
		}
		fmt.Fprintf(&b, "set mwan3.%s.sticky=0\n", sn)
		fmt.Fprintf(&b, "set mwan3.%s.family=ipv4\n", sn)
		fmt.Fprintf(&b, "set mwan3.%s.use_policy=sag_po_%s\n", sn, x.UseWan)
	}
	b.WriteString("set mwan3.sag_default=rule\n")
	b.WriteString("set mwan3.sag_default.dest_ip=0.0.0.0/0\n")
	b.WriteString("set mwan3.sag_default.sticky=0\n")
	b.WriteString("set mwan3.sag_default.family=ipv4\n")
	b.WriteString("set mwan3.sag_default.use_policy=sag_policy\n")
	b.WriteString("commit mwan3\n")

	if err := uciBatch(ctx, b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	for k, v := range map[string]string{
		"mw_enabled": map[bool]string{true: "1", false: "0"}[enabled],
		"mw_mode":    in.Mode,
	} {
		if err := s.setSetting(k, v); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	action := "restart"
	if !enabled {
		action = "stop"
	}
	if err := serviceReload(ctx, "mwan3", action); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"applied": true, "enabled": enabled, "mode": in.Mode,
		"backup": backupName,
	})
}
