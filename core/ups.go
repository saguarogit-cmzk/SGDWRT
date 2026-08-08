package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// UPS nadzor (D8): NUT (Network UPS Tools) preko OpenWrt paketa.
// Saguaro instalira pakete na klik, upiše sag_ konfiguraciju (driver, upsd na
// 127.0.0.1, upsmon kao master) i svakih 15 s čita stanje kroz `upsc`.
// Uredno gašenje pri praznoj bateriji radi sam upsmon — to mu je posao i radi
// ga i kad Saguaro servis ne bi radio. Saguaro povrh toga javlja događaje
// (nestanak struje, povratak, slaba baterija, gubitak veze s UPS-om) kroz
// postojeći sustav upozorenja.

const (
	nutServerConfig  = "/etc/config/nut_server"
	nutMonitorConfig = "/etc/config/nut_monitor"
	upsName          = "sag_ups" // ime NUT jedinice = ime uci sekcije
	upsPollEvery     = 15 * time.Second
)

// upsDrivers su ponuđeni driveri: usbhid-ups pokriva gotovo sve novije USB
// UPS-e (APC, Eaton, CyberPower...), nutdrv_qx starije i jeftinije (Mustek,
// Trust i slične s Megatec/Q1 protokolom).
var upsDrivers = map[string]string{
	"usbhid-ups": "nut-driver-usbhid-ups",
	"nutdrv_qx":  "nut-driver-nutdrv_qx",
}

var upsPackages = []string{
	"nut-server", "nut-upsmon", "nut-upsc",
	"nut-driver-usbhid-ups", "nut-driver-nutdrv_qx",
}

/* ---------- stanje iz upsc ---------- */

type upsState struct {
	mu      sync.RWMutex
	vars    map[string]string // zadnje očitanje upsc-a
	readAt  time.Time
	lastErr string
}

var upsCur upsState

// upscRead pročita sve varijable UPS-a. Greška znači da driver ne radi ili
// UPS nije spojen — i to je informacija, pa se sprema.
func upscRead(ctx context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "upsc", upsName+"@127.0.0.1").Output()
	if err != nil {
		return nil, err
	}
	vars := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		vars[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return vars, nil
}

func upsInstalled() bool {
	_, err := exec.LookPath("upsc")
	if err != nil {
		return false
	}
	_, err = exec.LookPath("upsd")
	return err == nil
}

// upsEnabled kaže je li Saguaro upisao i uključio svoju NUT konfiguraciju.
func upsEnabled(ctx context.Context) bool {
	return uciGet(ctx, "nut_server."+upsName+".driver") != "" &&
		uciGet(ctx, "nut_server."+upsName+".sag_enabled") == "1"
}

/* ---------- petlja: očitanje i događaji ---------- */

// upsLoop čita stanje UPS-a i javlja promjene. Prijelazi koje pratimo:
// OL→OB (nestanak struje), OB→OL (povratak), pojava LB (slaba baterija) i
// gubitak/povratak komunikacije s driverom.
func (s *server) upsLoop() {
	for {
		time.Sleep(upsPollEvery)
		ctx := context.Background()
		if !upsInstalled() || !upsEnabled(ctx) {
			continue
		}
		vars, err := upscRead(ctx)

		upsCur.mu.Lock()
		if err != nil {
			upsCur.lastErr = err.Error()
			upsCur.vars = nil
		} else {
			upsCur.lastErr = ""
			upsCur.vars = vars
			upsCur.readAt = time.Now()
		}
		upsCur.mu.Unlock()

		// komunikacija: "ok" ili "lost" — javlja se samo promjena
		comm := "ok"
		if err != nil {
			comm = "lost"
		}
		if changed, prev := s.alertValue("ups:comm", comm); changed {
			if comm == "lost" {
				s.alert("ups", "warning",
					"Veza s UPS-om je izgubljena — driver se ne javlja. "+
						"Provjeri USB kabel i je li UPS upaljen.")
			} else if prev == "lost" {
				s.alert("ups", "info", "Veza s UPS-om je ponovno uspostavljena.")
			}
		}
		if err != nil {
			continue
		}

		// ups.status: niz oznaka odvojenih razmakom, npr. "OB LB CHRG"
		status := vars["ups.status"]
		onBatt := hasUPSFlag(status, "OB")
		lowBatt := hasUPSFlag(status, "LB")

		power := "mreža"
		if onBatt {
			power = "baterija"
		}
		if changed, prev := s.alertValue("ups:power", power); changed {
			if power == "baterija" {
				s.alert("ups", "warning", "Nestala je struja — uređaj radi na bateriji UPS-a."+
					upsBattNote(vars))
			} else if prev == "baterija" {
				s.alert("ups", "info", "Struja se vratila — UPS je opet na mreži."+
					upsBattNote(vars))
			}
		}

		lb := "ne"
		if lowBatt {
			lb = "da"
		}
		if changed, _ := s.alertValue("ups:lowbatt", lb); changed && lowBatt {
			s.alert("ups", "error",
				"Baterija UPS-a je pri kraju — slijedi uredno gašenje uređaja "+
					"(upsmon). "+upsBattNote(vars))
		}
	}
}

func hasUPSFlag(status, flag string) bool {
	for _, f := range strings.Fields(status) {
		if f == flag {
			return true
		}
	}
	return false
}

// upsBattNote sažme stanje baterije za tekst poruke, ako ga UPS daje.
func upsBattNote(vars map[string]string) string {
	var parts []string
	if v := vars["battery.charge"]; v != "" {
		parts = append(parts, "baterija "+v+" %")
	}
	if v := vars["battery.runtime"]; v != "" {
		if sec, err := strconv.Atoi(v); err == nil {
			parts = append(parts, "autonomija ~"+strconv.Itoa(sec/60)+" min")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

/* ---------- API ---------- */

func (s *server) handleUPSGet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	installed := upsInstalled()
	out := map[string]any{
		"installed": installed,
		"enabled":   false,
		"drivers":   []string{"usbhid-ups", "nutdrv_qx"},
	}
	if !installed {
		writeJSON(w, http.StatusOK, out)
		return
	}
	out["enabled"] = upsEnabled(ctx)
	out["driver"] = uciGet(ctx, "nut_server."+upsName+".driver")
	if v := uciGet(ctx, "nut_server.override_battery_charge_low.value"); v != "" {
		out["low_pct"] = v
	}

	// svježe očitanje na zahtjev — GUI ne čeka petlju
	if out["enabled"] == true {
		vars, err := upscRead(ctx)
		if err == nil {
			upsCur.mu.Lock()
			upsCur.vars, upsCur.readAt, upsCur.lastErr = vars, time.Now(), ""
			upsCur.mu.Unlock()
		}
	}
	upsCur.mu.RLock()
	if upsCur.vars != nil {
		v := upsCur.vars
		st := map[string]any{
			"status":  v["ups.status"],
			"read_at": upsCur.readAt.Unix(),
			"model":   strings.TrimSpace(v["ups.mfr"] + " " + v["ups.model"]),
		}
		for apiKey, nutKey := range map[string]string{
			"charge_pct": "battery.charge", "runtime_s": "battery.runtime",
			"load_pct": "ups.load", "input_v": "input.voltage",
			"battery_v": "battery.voltage",
		} {
			if raw := v[nutKey]; raw != "" {
				if f, err := strconv.ParseFloat(raw, 64); err == nil {
					st[apiKey] = f
				}
			}
		}
		out["ups"] = st
	} else if upsCur.lastErr != "" {
		out["error"] = "UPS se ne javlja (driver ne radi ili UPS nije spojen)"
	}
	upsCur.mu.RUnlock()
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleUPSInstall(w http.ResponseWriter, r *http.Request) {
	if !upsInstalled() {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		_ = exec.CommandContext(ctx, "apk", "update").Run()
		args := append([]string{"add"}, upsPackages...)
		if out, err := exec.CommandContext(ctx, "apk", args...).CombinedOutput(); err != nil {
			writeErr(w, http.StatusInternalServerError,
				"instalacija: "+err.Error()+": "+string(out))
			return
		}
	}
	addEvent(s, "info", "Instaliran NUT — nadzor neprekidnog napajanja (UPS)")
	writeJSON(w, http.StatusOK, map[string]any{"installed": upsInstalled()})
}

func (s *server) handleUPSSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Enabled *bool  `json:"enabled"`
		Driver  string `json:"driver"`
		LowPct  int    `json:"low_pct"` // 0 = prepusti UPS-u tvornički prag
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "nedostaje polje enabled")
		return
	}
	if !upsInstalled() {
		writeErr(w, http.StatusBadRequest, "prvo instaliraj NUT pakete")
		return
	}
	if in.Driver == "" {
		in.Driver = "usbhid-ups"
	}
	if _, ok := upsDrivers[in.Driver]; !ok {
		writeErr(w, http.StatusBadRequest, "nepoznat driver: "+in.Driver)
		return
	}
	if in.LowPct < 0 || in.LowPct > 90 {
		writeErr(w, http.StatusBadRequest, "prag baterije: 0–90 %")
		return
	}

	for _, p := range []string{nutServerConfig, nutMonitorConfig} {
		if _, err := s.backupConfig(p); err != nil {
			writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
			return
		}
	}

	// lozinka veže upsmon na upsd (samo 127.0.0.1); generira se jednom
	pw := uciGet(ctx, "nut_server.sag_user.password")
	if pw == "" {
		raw := make([]byte, 12)
		if _, err := rand.Read(raw); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		pw = hex.EncodeToString(raw)
	}

	en := "0"
	if *in.Enabled {
		en = "1"
	}
	var b strings.Builder
	// nut_server: driver + korisnik + slušanje samo lokalno
	b.WriteString("set nut_server." + upsName + "=driver\n")
	b.WriteString("set nut_server." + upsName + ".driver=" + in.Driver + "\n")
	b.WriteString("set nut_server." + upsName + ".port=auto\n")
	b.WriteString("set nut_server." + upsName + ".sag_enabled=" + en + "\n")
	// prag baterije: init skripta NUT-a čita listu override u driver sekciji
	// i vrijednost iz zasebne sekcije override_<varijabla> (s '_' umjesto '.')
	b.WriteString("delete nut_server." + upsName + ".override\n")
	b.WriteString("delete nut_server.override_battery_charge_low\n")
	if in.LowPct > 0 {
		b.WriteString("add_list nut_server." + upsName + ".override=battery_charge_low\n")
		b.WriteString("set nut_server.override_battery_charge_low=override\n")
		b.WriteString("set nut_server.override_battery_charge_low.value=" +
			strconv.Itoa(in.LowPct) + "\n")
	}
	b.WriteString("set nut_server.sag_user=user\n")
	b.WriteString("set nut_server.sag_user.username=saguaro\n")
	b.WriteString("set nut_server.sag_user.password=" + pw + "\n")
	b.WriteString("set nut_server.sag_user.upsmon=master\n")
	b.WriteString("set nut_server.sag_listen=listen_address\n")
	b.WriteString("set nut_server.sag_listen.address=127.0.0.1\n")
	b.WriteString("set nut_server.sag_listen.port=3493\n")
	b.WriteString("commit nut_server\n")
	// nut_monitor: upsmon kao master — on gasi uređaj kad je baterija prazna
	b.WriteString("set nut_monitor.upsmon=upsmon\n")
	b.WriteString("set nut_monitor.upsmon.minsupplies=1\n")
	b.WriteString("set nut_monitor.sag_master=master\n")
	b.WriteString("set nut_monitor.sag_master.upsname=" + upsName + "\n")
	b.WriteString("set nut_monitor.sag_master.hostname=127.0.0.1\n")
	b.WriteString("set nut_monitor.sag_master.powervalue=1\n")
	b.WriteString("set nut_monitor.sag_master.username=saguaro\n")
	b.WriteString("set nut_monitor.sag_master.password=" + pw + "\n")
	b.WriteString("commit nut_monitor\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	action := "restart"
	if !*in.Enabled {
		action = "stop"
	}
	if err := serviceReload(ctx, "nut-server", action); err != nil && *in.Enabled {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := serviceReload(ctx, "nut-monitor", action); err != nil && *in.Enabled {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if *in.Enabled {
		_ = serviceReload(ctx, "nut-server", "enable")
		_ = serviceReload(ctx, "nut-monitor", "enable")
		addEvent(s, "info", "UPS nadzor uključen (driver "+in.Driver+")")
	} else {
		_ = serviceReload(ctx, "nut-server", "disable")
		_ = serviceReload(ctx, "nut-monitor", "disable")
		addEvent(s, "info", "UPS nadzor isključen")
	}
	note := "driveru treba koja sekunda da nađe UPS nakon uključivanja"
	if !*in.Enabled {
		note = "UPS nadzor je isključen, NUT servisi su zaustavljeni"
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": *in.Enabled, "note": note})
}
