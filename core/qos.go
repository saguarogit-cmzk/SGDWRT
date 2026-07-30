package main

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
)

// QoS kroz SQM (cake): ograničenje brzine po WAN sučelju uklanja bufferbloat
// i drži VoIP/sastanke glatkima kad netko zauzme vezu. Saguaro upravlja samo
// vlastitim sag_q_* sekcijama u /etc/config/sqm.
const sqmConfig = "/etc/config/sqm"

type qosQueue struct {
	Iface    string `json:"iface"`    // logičko sučelje (wan, sag_wan2, lan)
	Device   string `json:"device"`   // fizički uređaj (eth1...)
	Download int    `json:"download"` // kbit/s prema korisnicima (0 = bez limita)
	Upload   int    `json:"upload"`   // kbit/s prema internetu
	Enabled  bool   `json:"enabled"`
}

func (s *server) handleQosGet(w http.ResponseWriter, r *http.Request) {
	installed := false
	if _, err := os.Stat("/etc/init.d/sqm"); err == nil {
		installed = true
	}
	netCfg, err := uciGetConfig(r.Context(), "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	sqmCfg, _ := uciGetConfig(r.Context(), "sqm")

	queues := []qosQueue{}
	for name := range netCfg {
		if !reUplinkName.MatchString(name) ||
			sectStr(netCfg[name], ".type") != "interface" {
			continue
		}
		dev := ospfResolveDevice(netCfg, name)
		if dev == "" {
			continue
		}
		q := qosQueue{Iface: name, Device: dev}
		if sec, ok := sqmCfg["sag_q_"+name]; ok {
			q.Enabled = sectStr(sec, "enabled") == "1"
			fmt.Sscanf(sectStr(sec, "download"), "%d", &q.Download)
			fmt.Sscanf(sectStr(sec, "upload"), "%d", &q.Upload)
		}
		queues = append(queues, q)
	}
	sort.Slice(queues, func(i, j int) bool { return queues[i].Iface < queues[j].Iface })
	writeJSON(w, http.StatusOK, map[string]any{
		"installed": installed, "queues": queues,
	})
}

func (s *server) handleQosSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Queues []qosQueue `json:"queues"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	netCfg, err := uciGetConfig(ctx, "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	for _, q := range in.Queues {
		if !reUplinkName.MatchString(q.Iface) {
			writeErr(w, http.StatusBadRequest, "neispravno sučelje: "+q.Iface)
			return
		}
		if ospfResolveDevice(netCfg, q.Iface) == "" {
			writeErr(w, http.StatusBadRequest, "sučelje "+q.Iface+" nema uređaj")
			return
		}
		if q.Enabled && q.Download <= 0 && q.Upload <= 0 {
			writeErr(w, http.StatusBadRequest,
				q.Iface+": upiši brzinu preuzimanja i/ili slanja (kbit/s)")
			return
		}
		if q.Download < 0 || q.Upload < 0 || q.Download > 10000000 || q.Upload > 10000000 {
			writeErr(w, http.StatusBadRequest, q.Iface+": brzina izvan raspona")
			return
		}
	}

	backupName, err := s.backupConfig(sqmConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}
	sqmCfg, err := uciGetConfig(ctx, "sqm")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var b strings.Builder
	for name := range sqmCfg {
		if strings.HasPrefix(name, "sag_q_") {
			fmt.Fprintf(&b, "delete sqm.%s\n", name)
		}
	}
	anyEnabled := false
	for _, q := range in.Queues {
		if !q.Enabled {
			continue
		}
		anyEnabled = true
		sn := "sag_q_" + q.Iface
		dev := ospfResolveDevice(netCfg, q.Iface)
		fmt.Fprintf(&b, "set sqm.%s=queue\n", sn)
		fmt.Fprintf(&b, "set sqm.%s.enabled=1\n", sn)
		fmt.Fprintf(&b, "set sqm.%s.interface=%s\n", sn, dev)
		fmt.Fprintf(&b, "set sqm.%s.download=%d\n", sn, q.Download)
		fmt.Fprintf(&b, "set sqm.%s.upload=%d\n", sn, q.Upload)
		fmt.Fprintf(&b, "set sqm.%s.qdisc=cake\n", sn)
		fmt.Fprintf(&b, "set sqm.%s.script=piece_of_cake.qos\n", sn)
		fmt.Fprintf(&b, "set sqm.%s.linklayer=ethernet\n", sn)
		fmt.Fprintf(&b, "set sqm.%s.overhead=22\n", sn)
		fmt.Fprintf(&b, "set sqm.%s.debug_logging=0\n", sn)
		fmt.Fprintf(&b, "set sqm.%s.verbosity=5\n", sn)
	}
	b.WriteString("commit sqm\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	action := "restart"
	if !anyEnabled {
		action = "stop"
	}
	if err := serviceReload(ctx, "sqm", action); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if anyEnabled {
		serviceReload(ctx, "sqm", "enable")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"applied": true, "backup": backupName,
	})
}
