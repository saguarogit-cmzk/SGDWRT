package main

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Mjesečni izvještaj (D3). Dvije stvari:
//
//  1. **uzorkovanje** — svake minute se zapiše kako uređaj stoji (radi li
//     internet, javljaju li se nadzirani uređaji, koliko je prošlo prometa).
//     Bez toga izvještaj ne bi imao odakle reći "internet je radio 99,8 %
//     vremena": dnevnik se rotira, a brojači prometa se pri restartu nuliraju.
//  2. **sastavljanje i slanje** — jednom mjesečno, za prethodni mjesec.
//
// Slanje je zadano isključeno, kao i sve ostale obavijesti (Goranovo pravilo:
// mailom ide samo ono što je izrijekom uključeno).

/* ---------- uzorkovanje ---------- */

// reportSample zapisuje jedno mjerenje. Zove se iz nadzorne petlje svake
// minute; radi u istoj transakciji misli kao i ostatak — tiho, bez rušenja
// petlje ako nešto ne uspije.
func (s *server) reportSample(ctx context.Context) {
	day := time.Now().Format("2006-01-02")
	s.db.Exec(`INSERT OR IGNORE INTO report_days (day) VALUES (?)`, day)

	// stanje sustava
	var info struct {
		Uptime int64    `json:"uptime"`
		Load   [3]int64 `json:"load"`
		Memory struct {
			Total     int64 `json:"total"`
			Available int64 `json:"available"`
		} `json:"memory"`
		Root struct {
			Total int64 `json:"total"`
			Free  int64 `json:"free"`
		} `json:"root"`
	}
	loadNow, memPct, diskPct := 0.0, 0, 0
	reboot := 0
	if ubusCall(ctx, "system", "info", &info) == nil {
		loadNow = float64(info.Load[0]) / 65536.0
		if info.Memory.Total > 0 {
			memPct = int((info.Memory.Total - info.Memory.Available) * 100 / info.Memory.Total)
		}
		if info.Root.Total > 0 {
			diskPct = int((info.Root.Total - info.Root.Free) * 100 / info.Root.Total)
		}
		// uptime koji je pao znači da se uređaj u međuvremenu pokrenuo iznova
		prev, _ := strconv.ParseInt(s.getSetting("report_last_uptime", "0"), 10, 64)
		if prev > 0 && info.Uptime < prev {
			reboot = 1
		}
		s.setSetting("report_last_uptime", strconv.FormatInt(info.Uptime, 10))
	}

	// radi li internet: dovoljno je da je bar jedna WAN veza gore
	wanOK := 0
	var dump ubusIfaceDump
	rx, tx := int64(0), int64(0)
	if ubusCall(ctx, "network.interface", "dump", &dump) == nil {
		var devs map[string]ubusDevice
		haveDevs := ubusCall(ctx, "network.device", "status", &devs) == nil
		for _, in := range dump.Interface {
			if !reWanName.MatchString(in.Name) {
				continue
			}
			if in.Up {
				wanOK = 1
			}
			if haveDevs && in.Device != "" {
				if d, ok := devs[in.Device]; ok {
					r, t := s.counterDelta(in.Device, d.Statistics.RxBytes, d.Statistics.TxBytes)
					rx += r
					tx += t
				}
			}
		}
	}

	s.db.Exec(`UPDATE report_days SET
		samples = samples + 1,
		wan_ok = wan_ok + ?,
		load_max = MAX(load_max, ?),
		mem_max = MAX(mem_max, ?),
		disk_max = MAX(disk_max, ?),
		rx_bytes = rx_bytes + ?,
		tx_bytes = tx_bytes + ?,
		reboots = reboots + ?
		WHERE day = ?`, wanOK, loadNow, memPct, diskPct, rx, tx, reboot, day)

	// dostupnost nadziranih uređaja — stanje je upravo osvježila nadzorna
	// petlja, pa se ovdje samo prepisuje, bez novih pingova
	rows, err := s.db.Query(`SELECT uuid, name, ip, COALESCE(last_ok,-1)
		FROM nw_monitors WHERE enabled = 1`)
	if err != nil {
		return
	}
	type m struct {
		uuid, name, ip string
		ok             int
	}
	ms := []m{}
	for rows.Next() {
		var x m
		if rows.Scan(&x.uuid, &x.name, &x.ip, &x.ok) == nil {
			ms = append(ms, x)
		}
	}
	rows.Close()
	for _, x := range ms {
		if x.ok < 0 {
			continue // još nije provjeren, ne broji se ni u jednu stranu
		}
		s.db.Exec(`INSERT INTO report_monitor_days (day, monitor_uuid, name, ip, samples, ok)
			VALUES (?,?,?,?,1,?)
			ON CONFLICT(day, monitor_uuid) DO UPDATE SET
				samples = samples + 1, ok = ok + ?, name = excluded.name, ip = excluded.ip`,
			day, x.uuid, x.name, x.ip, x.ok, x.ok)
	}
}

// counterDelta pretvara brojač sučelja (koji samo raste) u prirast od prošlog
// gledanja. Poslije restarta brojač kreće od nule, pa se manja vrijednost od
// zapamćene uzima kao cijeli prirast, a ne kao negativan broj.
func (s *server) counterDelta(dev string, rx, tx int64) (int64, int64) {
	key := "report_ctr_" + dev
	parts := strings.Fields(s.getSetting(key, ""))
	s.setSetting(key, fmt.Sprintf("%d %d", rx, tx))
	if len(parts) != 2 {
		// prvi put vidimo ovo sučelje: brojač već stoji na onome što je uređaj
		// prenio otkad je upaljen, a to nije promet ovog trenutka. Zapamti se
		// polazna vrijednost, a u izvještaj ne ide ništa — inače bi prvi dan
		// mjerenja pokazao sav promet od zadnjeg pokretanja uređaja.
		return 0, 0
	}
	lastRx, _ := strconv.ParseInt(parts[0], 10, 64)
	lastTx, _ := strconv.ParseInt(parts[1], 10, 64)
	dRx, dTx := rx-lastRx, tx-lastTx
	// manja vrijednost od zapamćene znači da je brojač krenuo ispočetka
	// (uređaj se pokrenuo iznova), pa je cijela nova vrijednost prirast
	if dRx < 0 {
		dRx = rx
	}
	if dTx < 0 {
		dTx = tx
	}
	return dRx, dTx
}

// reportCountEvent broji upozorenja u dnevni sažetak. Broji se ovdje, a ne
// naknadno iz dnevnika, jer se dnevnik rotira — do kraja mjeseca stariji
// zapisi više ne bi postojali i izvještaj bi tvrdio da je bilo mirnije nego
// što jest.
func (s *server) reportCountEvent(level string) {
	col := ""
	switch level {
	case "warning":
		col = "ev_warn"
	case "critical", "crit", "error":
		col = "ev_crit"
	default:
		return
	}
	day := time.Now().Format("2006-01-02")
	s.db.Exec(`INSERT OR IGNORE INTO report_days (day) VALUES (?)`, day)
	s.db.Exec(`UPDATE report_days SET `+col+` = `+col+` + 1 WHERE day = ?`, day)
}

// reportPrune briše sažetke starije od zadanog broja mjeseci.
func (s *server) reportPrune() {
	months, err := strconv.Atoi(s.getSetting("report_keep_months", "13"))
	if err != nil || months < 2 {
		months = 13
	}
	cut := time.Now().AddDate(0, -months, 0).Format("2006-01-02")
	s.db.Exec(`DELETE FROM report_days WHERE day < ?`, cut)
	s.db.Exec(`DELETE FROM report_monitor_days WHERE day < ?`, cut)
}

/* ---------- sastavljanje ---------- */

type reportMonitor struct {
	Name    string  `json:"name"`
	IP      string  `json:"ip"`
	Pct     float64 `json:"pct"`
	DownMin int     `json:"down_min"`
}

type reportDayRow struct {
	Day     string  `json:"day"`
	WanPct  float64 `json:"wan_pct"`
	RxBytes int64   `json:"rx_bytes"`
	TxBytes int64   `json:"tx_bytes"`
	EvWarn  int     `json:"ev_warn"`
	EvCrit  int     `json:"ev_crit"`
}

type reportEvent struct {
	Message string `json:"message"`
	Count   int    `json:"count"`
	Last    string `json:"last"`
}

type reportHost struct {
	IP      string `json:"ip"`
	Name    string `json:"name"`
	RxBytes int64  `json:"rx_bytes"`
	TxBytes int64  `json:"tx_bytes"`
}

type reportData struct {
	Month      string          `json:"month"`
	MonthLabel string          `json:"month_label"`
	Device     string          `json:"device"`
	Model      string          `json:"model"`
	Generated  string          `json:"generated"`
	DaysWith   int             `json:"days_with_data"`
	DaysTotal  int             `json:"days_total"`
	Samples    int             `json:"samples"`
	WanPct     float64         `json:"wan_pct"`
	WanDownMin int             `json:"wan_down_min"`
	Monitors   []reportMonitor `json:"monitors"`
	RxTotal    int64           `json:"rx_total"`
	TxTotal    int64           `json:"tx_total"`
	PeakDay    string          `json:"peak_day"`
	PeakBytes  int64           `json:"peak_bytes"`
	Days       []reportDayRow  `json:"days"`
	EvWarn     int             `json:"ev_warn"`
	EvCrit     int             `json:"ev_crit"`
	TopEvents  []reportEvent   `json:"top_events"`
	Reboots    int             `json:"reboots"`
	LoadMax    float64         `json:"load_max"`
	MemMax     int             `json:"mem_max"`
	DiskMax    int             `json:"disk_max"`
	Backups    int             `json:"backups"`
	LastBackup string          `json:"last_backup"`
	TopHosts   []reportHost    `json:"top_hosts"`
	HostsNote  string          `json:"hosts_note"`
	VPNPeers   int             `json:"vpn_peers"`
	VPNSites   int             `json:"vpn_sites"`
	Versions   string          `json:"versions"`
	Warnings   []string        `json:"warnings"`
}

var monthNames = []string{"siječanj", "veljača", "ožujak", "travanj", "svibanj",
	"lipanj", "srpanj", "kolovoz", "rujan", "listopad", "studeni", "prosinac"}

func monthLabel(month string) string {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return month
	}
	return fmt.Sprintf("%s %d.", monthNames[int(t.Month())-1], t.Year())
}

// prevMonth vraća oznaku prethodnog mjeseca (izvještaj se šalje za mjesec koji
// je gotov, ne za onaj u tijeku).
func prevMonth(now time.Time) string {
	return now.AddDate(0, 0, -now.Day()).Format("2006-01")
}

func (s *server) buildReport(ctx context.Context, month string) (reportData, error) {
	d := reportData{
		Month:      month,
		MonthLabel: monthLabel(month),
		Generated:  time.Now().Format("02.01.2006. 15:04"),
		Versions:   "Saguaro " + version,
	}
	first, err := time.Parse("2006-01", month)
	if err != nil {
		return d, fmt.Errorf("mjesec mora biti u obliku 2026-07")
	}
	d.DaysTotal = first.AddDate(0, 1, -1).Day()
	like := month + "-%"

	var b ubusBoard
	if ubusCall(ctx, "system", "board", &b) == nil {
		d.Device = b.Hostname
		d.Model = b.Model
		d.Versions = "OpenWrt " + b.Release.Version + " · Saguaro " + version
	}

	// dnevni sažeci
	rows, err := s.db.Query(`SELECT day, samples, wan_ok, load_max, mem_max,
		disk_max, rx_bytes, tx_bytes, reboots, ev_warn, ev_crit
		FROM report_days WHERE day LIKE ? ORDER BY day`, like)
	if err != nil {
		return d, err
	}
	totalSamples, totalWanOK := 0, 0
	for rows.Next() {
		var day string
		var samples, wanOK, memMax, diskMax, reboots, evW, evC int
		var loadMax float64
		var rx, tx int64
		if err := rows.Scan(&day, &samples, &wanOK, &loadMax, &memMax, &diskMax,
			&rx, &tx, &reboots, &evW, &evC); err != nil {
			continue
		}
		row := reportDayRow{Day: day, RxBytes: rx, TxBytes: tx, EvWarn: evW, EvCrit: evC}
		if samples > 0 {
			row.WanPct = float64(wanOK) * 100 / float64(samples)
		}
		d.Days = append(d.Days, row)
		totalSamples += samples
		totalWanOK += wanOK
		d.RxTotal += rx
		d.TxTotal += tx
		d.EvWarn += evW
		d.EvCrit += evC
		d.Reboots += reboots
		if loadMax > d.LoadMax {
			d.LoadMax = loadMax
		}
		if memMax > d.MemMax {
			d.MemMax = memMax
		}
		if diskMax > d.DiskMax {
			d.DiskMax = diskMax
		}
		if rx+tx > d.PeakBytes {
			d.PeakBytes = rx + tx
			d.PeakDay = day
		}
	}
	rows.Close()
	d.DaysWith = len(d.Days)
	d.Samples = totalSamples
	if totalSamples > 0 {
		d.WanPct = float64(totalWanOK) * 100 / float64(totalSamples)
		d.WanDownMin = totalSamples - totalWanOK // jedno mjerenje = jedna minuta
	}
	if d.DaysWith == 0 {
		d.Warnings = append(d.Warnings,
			"Za taj mjesec uređaj nema zapisa — izvještaj se počinje puniti od "+
				"trenutka kad je ova verzija Saguara postavljena.")
	} else if d.DaysWith < d.DaysTotal {
		d.Warnings = append(d.Warnings, fmt.Sprintf(
			"Uređaj ima zapise za %d od %d dana u mjesecu; postoci se računaju "+
				"samo iz izmjerenog vremena.", d.DaysWith, d.DaysTotal))
	}

	// dostupnost nadziranih uređaja
	mrows, err := s.db.Query(`SELECT name, ip, SUM(samples), SUM(ok)
		FROM report_monitor_days WHERE day LIKE ?
		GROUP BY monitor_uuid ORDER BY name`, like)
	if err == nil {
		for mrows.Next() {
			var name, ip string
			var samples, ok int
			if mrows.Scan(&name, &ip, &samples, &ok) != nil || samples == 0 {
				continue
			}
			d.Monitors = append(d.Monitors, reportMonitor{
				Name: name, IP: ip,
				Pct:     float64(ok) * 100 / float64(samples),
				DownMin: samples - ok,
			})
		}
		mrows.Close()
	}

	// najčešća upozorenja iz dnevnika (dnevnik se rotira, pa je ovo uzorak;
	// ukupni brojevi gore su točni jer se broje u trenutku nastanka)
	erows, err := s.db.Query(`SELECT message, COUNT(*) c, MAX(ts)
		FROM events WHERE ts LIKE ? AND level IN ('warning','critical')
		GROUP BY message ORDER BY c DESC LIMIT 8`, like)
	if err == nil {
		for erows.Next() {
			var e reportEvent
			if erows.Scan(&e.Message, &e.Count, &e.Last) == nil {
				d.TopEvents = append(d.TopEvents, e)
			}
		}
		erows.Close()
	}

	// backupi napravljeni u tom mjesecu (arhive na disku nose datum u imenu)
	if ents, err := os.ReadDir(s.backupDir); err == nil {
		stamp := strings.ReplaceAll(month, "-", "")
		last := ""
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			n := e.Name()
			if strings.Contains(n, stamp) {
				d.Backups++
			}
			if fi, err := e.Info(); err == nil && (last == "" || fi.ModTime().Format("2006-01-02 15:04") > last) {
				last = fi.ModTime().Format("2006-01-02 15:04")
			}
		}
		d.LastBackup = last
	}
	if d.Backups == 0 {
		d.Warnings = append(d.Warnings, "U tom mjesecu nije napravljena nijedna "+
			"sigurnosna kopija koja je još na uređaju.")
	}

	s.db.QueryRow(`SELECT COUNT(*) FROM wg_peers WHERE enabled=1`).Scan(&d.VPNPeers)
	s.db.QueryRow(`SELECT COUNT(*) FROM wg_sites WHERE enabled=1`).Scan(&d.VPNSites)

	d.TopHosts, d.HostsNote = s.trafficTop(ctx)
	return d, nil
}

// trafficTop čita potrošnju po uređaju iz nlbwmon-a. Ta se brojka NE zbraja u
// naše dnevne sažetke jer nlbwmon vodi vlastito razdoblje; zato uz nju uvijek
// ide napomena na što se odnosi, da se ne čita kao mjesečni zbroj.
func (s *server) trafficTop(ctx context.Context) ([]reportHost, string) {
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, "nlbw", "-c", "csv", "-g", "ip", "-o", "-rx_bytes").Output()
	if err != nil {
		return nil, "Potrošnja po uređaju nije dostupna (nlbwmon ne radi)."
	}
	names := map[string]string{}
	if rows, err := s.db.Query(`SELECT ip, COALESCE(name,'') FROM hosts WHERE ip <> ''`); err == nil {
		for rows.Next() {
			var ip, n string
			if rows.Scan(&ip, &n) == nil && n != "" {
				names[ip] = n
			}
		}
		rows.Close()
	}
	hosts := []reportHost{}
	for _, h := range parseNlbwCSV(string(out), 10) {
		hosts = append(hosts, reportHost{IP: h.IP, Name: names[h.IP],
			RxBytes: h.RxBytes, TxBytes: h.TxBytes})
	}
	sort.SliceStable(hosts, func(i, j int) bool {
		return hosts[i].RxBytes+hosts[i].TxBytes > hosts[j].RxBytes+hosts[j].TxBytes
	})
	note := "Brojke po uređaju vodi nlbwmon u vlastitom razdoblju i pri " +
		"ponovnom pokretanju uređaja kreću ispočetka, pa se ne moraju poklapati " +
		"s mjesečnim zbrojem na WAN-u."
	if dir := nlbwDBDir(); dir != "" && inRAM(dir) {
		note += " Baza mu stoji u " + dir + ", a to je radna memorija — " +
			"podaci se gube pri svakom ponovnom pokretanju uređaja."
	}
	return hosts, note
}

// inRAM kaže leži li putanja u radnoj memoriji. Na OpenWrt-u je /var samo
// poveznica na /tmp, pa provjera po zapisanom imenu nije dovoljna — put se
// mora razriješiti do kraja.
func inRAM(dir string) bool {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		real = dir
	}
	return strings.HasPrefix(real, "/tmp") || strings.HasPrefix(dir, "/tmp") ||
		strings.HasPrefix(dir, "/var/")
}

// nlbwDBDir čita gdje nlbwmon drži bazu — bitno jer radna memorija znači da
// podaci ne preživljavaju ponovno pokretanje.
func nlbwDBDir() string {
	cfg, err := uciGetConfig(context.Background(), "nlbwmon")
	if err != nil {
		return ""
	}
	for _, sec := range cfg {
		if v := sectStr(sec, "database_directory"); v != "" {
			return v
		}
	}
	return ""
}

/* ---------- prikaz ---------- */

func fmtBytesHR(n int64) string {
	switch {
	case n >= 1<<40:
		return fmt.Sprintf("%.2f TB", float64(n)/float64(int64(1)<<40))
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f kB", float64(n)/float64(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// fmtMinutes pretvara minute u nešto čitljivo ("2 h 15 min").
func fmtMinutes(m int) string {
	if m <= 0 {
		return "bez prekida"
	}
	if m < 60 {
		return fmt.Sprintf("%d min", m)
	}
	h := m / 60
	r := m % 60
	if r == 0 {
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d h %d min", h, r)
}

func esc(s string) string { return html.EscapeString(s) }

// renderReportHTML sastavlja poruku. Stilovi su upisani u same elemente jer
// mail programi izbacuju <style> blok — bez toga bi izvještaj stigao kao gola
// hrpa teksta.
func renderReportHTML(d reportData) string {
	const (
		wrap  = `font:14px/1.5 -apple-system,Segoe UI,Roboto,Arial,sans-serif;color:#333f48;max-width:760px`
		h2    = `font-size:15px;margin:22px 0 8px;padding-bottom:5px;border-bottom:2px solid #4a7c2f;color:#2b4a1c`
		tbl   = `border-collapse:collapse;width:100%;font-size:13px`
		th    = `text-align:left;padding:5px 8px;background:#f2f5ee;border-bottom:1px solid #d8e0d0;font-weight:600`
		td    = `padding:5px 8px;border-bottom:1px solid #eceff2`
		tdNum = td + `;text-align:right;white-space:nowrap`
		big   = `font-size:22px;font-weight:700;color:#2b4a1c`
		muted = `color:#7b8b96;font-size:12px`
	)
	var b strings.Builder
	fmt.Fprintf(&b, `<div style="%s">`, wrap)
	fmt.Fprintf(&b, `<h1 style="font-size:19px;margin:0 0 2px">Mjesečni izvještaj — %s</h1>`,
		esc(d.MonthLabel))
	fmt.Fprintf(&b, `<div style="%s">%s%s · sastavljeno %s · %s</div>`, muted,
		esc(d.Device), map[bool]string{true: " (" + esc(d.Model) + ")", false: ""}[d.Model != ""],
		esc(d.Generated), esc(d.Versions))

	for _, w := range d.Warnings {
		fmt.Fprintf(&b, `<p style="margin:12px 0;padding:9px 11px;background:#fbf3e2;`+
			`border-left:3px solid #c07d10;font-size:13px">%s</p>`, esc(w))
	}

	// ukratko
	fmt.Fprintf(&b, `<div style="%s">Ukratko</div>`, h2)
	fmt.Fprintf(&b, `<table style="%s"><tr>`, tbl)
	cell := func(label, value string) {
		fmt.Fprintf(&b, `<td style="padding:10px 12px;border:1px solid #e3e8ec;width:25%%">`+
			`<div style="%s">%s</div><div style="%s">%s</div></td>`,
			big, esc(value), muted, esc(label))
	}
	cell("internet dostupan", fmt.Sprintf("%.2f %%", d.WanPct))
	cell("bez interneta", fmtMinutes(d.WanDownMin))
	cell("promet ukupno", fmtBytesHR(d.RxTotal+d.TxTotal))
	cell("upozorenja", strconv.Itoa(d.EvWarn+d.EvCrit))
	b.WriteString(`</tr></table>`)

	// dostupnost
	fmt.Fprintf(&b, `<div style="%s">Dostupnost</div>`, h2)
	fmt.Fprintf(&b, `<table style="%s"><tr><th style="%s">Što se pratilo</th>`+
		`<th style="%s">Adresa</th><th style="%s">Dostupno</th><th style="%s">Nedostupno</th></tr>`,
		tbl, th, th, th, th)
	fmt.Fprintf(&b, `<tr><td style="%s"><b>Internet (WAN)</b></td><td style="%s">—</td>`+
		`<td style="%s">%.2f %%</td><td style="%s">%s</td></tr>`,
		td, td, tdNum, d.WanPct, tdNum, fmtMinutes(d.WanDownMin))
	for _, m := range d.Monitors {
		fmt.Fprintf(&b, `<tr><td style="%s">%s</td><td style="%s">%s</td>`+
			`<td style="%s">%.2f %%</td><td style="%s">%s</td></tr>`,
			td, esc(m.Name), td, esc(m.IP), tdNum, m.Pct, tdNum, fmtMinutes(m.DownMin))
	}
	if len(d.Monitors) == 0 {
		fmt.Fprintf(&b, `<tr><td colspan="4" style="%s">Nijedan uređaj nije pod nadzorom `+
			`(Status → Monitoring).</td></tr>`, td+";color:#7b8b96")
	}
	b.WriteString(`</table>`)

	// promet
	fmt.Fprintf(&b, `<div style="%s">Promet</div>`, h2)
	fmt.Fprintf(&b, `<p style="margin:0 0 8px">Preuzeto <b>%s</b>, poslano <b>%s</b>.`,
		fmtBytesHR(d.RxTotal), fmtBytesHR(d.TxTotal))
	if d.PeakDay != "" {
		fmt.Fprintf(&b, ` Najjači dan bio je <b>%s</b> (%s).`,
			esc(d.PeakDay), fmtBytesHR(d.PeakBytes))
	}
	b.WriteString(`</p>`)
	if len(d.TopHosts) > 0 {
		fmt.Fprintf(&b, `<table style="%s"><tr><th style="%s">Uređaj</th>`+
			`<th style="%s">Preuzeto</th><th style="%s">Poslano</th></tr>`, tbl, th, th, th)
		for _, h := range d.TopHosts {
			name := h.IP
			if h.Name != "" {
				name = h.Name + " (" + h.IP + ")"
			}
			fmt.Fprintf(&b, `<tr><td style="%s">%s</td><td style="%s">%s</td>`+
				`<td style="%s">%s</td></tr>`, td, esc(name),
				tdNum, fmtBytesHR(h.RxBytes), tdNum, fmtBytesHR(h.TxBytes))
		}
		b.WriteString(`</table>`)
	}
	if d.HostsNote != "" {
		fmt.Fprintf(&b, `<p style="%s;margin:6px 0 0">%s</p>`, muted, esc(d.HostsNote))
	}

	// događaji
	fmt.Fprintf(&b, `<div style="%s">Upozorenja</div>`, h2)
	fmt.Fprintf(&b, `<p style="margin:0 0 8px">Ozbiljnih: <b>%d</b> · upozorenja: <b>%d</b> · `+
		`ponovnih pokretanja uređaja: <b>%d</b></p>`, d.EvCrit, d.EvWarn, d.Reboots)
	if len(d.TopEvents) > 0 {
		// brojevi gore počinju teći od dana kad je uređaj dobio ovu verziju,
		// a dnevnik seže dalje unatrag — bez ove rečenice izgleda kao greška
		listed := 0
		for _, e := range d.TopEvents {
			listed += e.Count
		}
		if listed > d.EvWarn+d.EvCrit {
			fmt.Fprintf(&b, `<p style="%s;margin:0 0 8px">U dnevniku ima i starijih `+
				`zapisa nego što ih je uređaj stigao prebrojati — brojanje teče od `+
				`dana kad je izvještaj uključen.</p>`, muted)
		}
		fmt.Fprintf(&b, `<table style="%s"><tr><th style="%s">Poruka</th>`+
			`<th style="%s">Puta</th><th style="%s">Zadnji put</th></tr>`, tbl, th, th, th)
		for _, e := range d.TopEvents {
			fmt.Fprintf(&b, `<tr><td style="%s">%s</td><td style="%s">%d</td>`+
				`<td style="%s">%s</td></tr>`, td, esc(e.Message), tdNum, e.Count,
				tdNum, esc(e.Last))
		}
		b.WriteString(`</table>`)
		fmt.Fprintf(&b, `<p style="%s;margin:6px 0 0">Popis je iz dnevnika koji uređaj `+
			`još čuva; ukupni brojevi iznad broje se u trenutku nastanka i točni su.</p>`, muted)
	}

	// uređaj
	fmt.Fprintf(&b, `<div style="%s">Uređaj</div>`, h2)
	fmt.Fprintf(&b, `<table style="%s">`, tbl)
	row := func(k, v string) {
		fmt.Fprintf(&b, `<tr><td style="%s;width:45%%">%s</td><td style="%s">%s</td></tr>`,
			td, esc(k), td, esc(v))
	}
	row("Najveće opterećenje procesora", fmt.Sprintf("%.2f", d.LoadMax))
	row("Najveća zauzetost memorije", fmt.Sprintf("%d %%", d.MemMax))
	row("Najveća zauzetost root particije", fmt.Sprintf("%d %%", d.DiskMax))
	row("Sigurnosne kopije u mjesecu", strconv.Itoa(d.Backups))
	if d.LastBackup != "" {
		row("Zadnja kopija na uređaju", d.LastBackup)
	}
	row("VPN korisnici / veze s poslovnicama",
		fmt.Sprintf("%d / %d", d.VPNPeers, d.VPNSites))
	row("Dana s mjerenjima", fmt.Sprintf("%d od %d", d.DaysWith, d.DaysTotal))
	b.WriteString(`</table>`)

	// po danima
	if len(d.Days) > 0 {
		fmt.Fprintf(&b, `<div style="%s">Po danima</div>`, h2)
		fmt.Fprintf(&b, `<table style="%s"><tr><th style="%s">Dan</th>`+
			`<th style="%s">Internet</th><th style="%s">Preuzeto</th>`+
			`<th style="%s">Poslano</th><th style="%s">Upozorenja</th></tr>`,
			tbl, th, th, th, th, th)
		for _, r := range d.Days {
			warn := ""
			if r.EvWarn+r.EvCrit > 0 {
				warn = strconv.Itoa(r.EvWarn + r.EvCrit)
			}
			style := tdNum
			if r.WanPct < 99.9 {
				style = tdNum + ";color:#c0392b;font-weight:600"
			}
			fmt.Fprintf(&b, `<tr><td style="%s">%s</td><td style="%s">%.1f %%</td>`+
				`<td style="%s">%s</td><td style="%s">%s</td><td style="%s">%s</td></tr>`,
				td, esc(r.Day), style, r.WanPct, tdNum, fmtBytesHR(r.RxBytes),
				tdNum, fmtBytesHR(r.TxBytes), tdNum, warn)
		}
		b.WriteString(`</table>`)
	}

	fmt.Fprintf(&b, `<p style="%s;margin-top:22px;border-top:1px solid #e3e8ec;padding-top:10px">`+
		`Izvještaj je sastavio uređaj sam, iz vlastitih mjerenja (jedno mjerenje `+
		`svake minute). Slanje se isključuje u Saguaru pod System → Reports.</p>`, muted)
	b.WriteString(`</div>`)
	return b.String()
}

// renderReportText je pričuvna, tekstualna inačica za mail programe koji ne
// prikazuju HTML.
func renderReportText(d reportData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Mjesečni izvještaj — %s\n%s\n\n", d.MonthLabel, d.Device)
	fmt.Fprintf(&b, "Internet dostupan: %.2f %% (bez interneta %s)\n",
		d.WanPct, fmtMinutes(d.WanDownMin))
	fmt.Fprintf(&b, "Promet: preuzeto %s, poslano %s\n",
		fmtBytesHR(d.RxTotal), fmtBytesHR(d.TxTotal))
	fmt.Fprintf(&b, "Upozorenja: %d ozbiljnih, %d upozorenja, %d pokretanja uređaja\n\n",
		d.EvCrit, d.EvWarn, d.Reboots)
	for _, m := range d.Monitors {
		fmt.Fprintf(&b, "  %s (%s): %.2f %%, nedostupno %s\n",
			m.Name, m.IP, m.Pct, fmtMinutes(m.DownMin))
	}
	for _, w := range d.Warnings {
		fmt.Fprintf(&b, "\nNapomena: %s\n", w)
	}
	b.WriteString("\nCijeli izvještaj je u HTML dijelu ove poruke.\n")
	return b.String()
}

/* ---------- slanje ---------- */

func (s *server) sendReport(ctx context.Context, month string) error {
	if s.getSetting("smtp_host", "") == "" || s.getSetting("smtp_to", "") == "" {
		return fmt.Errorf("SMTP postavke nisu popunjene")
	}
	d, err := s.buildReport(ctx, month)
	if err != nil {
		return err
	}
	from := s.getSetting("smtp_from", "saguaro@localhost")
	to := strings.Fields(s.getSetting("smtp_to", ""))
	name := d.Device
	if name == "" {
		name = "Saguaro"
	}
	msg := buildMailHTML(from, to, name+" — mjesečni izvještaj ("+d.MonthLabel+")",
		renderReportText(d), renderReportHTML(d))
	return s.smtpDeliver(to, msg)
}

// buildMailHTML slaže poruku s tekstualnim i HTML dijelom (multipart/alternative):
// mail program uzme onaj koji zna prikazati.
func buildMailHTML(from string, to []string, subject, text, htmlBody string) []byte {
	sep := "sag-rep-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mimeHeader(subject))
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", sep)
	fmt.Fprintf(&b, "--%s\r\n", sep)
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(strings.ReplaceAll(text, "\n", "\r\n"))
	fmt.Fprintf(&b, "\r\n--%s\r\n", sep)
	b.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
	b.WriteString(strings.ReplaceAll(htmlBody, "\n", "\r\n"))
	fmt.Fprintf(&b, "\r\n--%s--\r\n", sep)
	return []byte(b.String())
}

// reportDue se zove jednom dnevno iz nadzorne petlje. Šalje izvještaj za
// prethodni mjesec, i to najviše jednom — zapamti se koji je mjesec poslan.
func (s *server) reportDue(ctx context.Context) {
	s.reportPrune()
	if s.getSetting("report_enabled", "0") != "1" {
		return
	}
	day, err := strconv.Atoi(s.getSetting("report_day", "1"))
	if err != nil || day < 1 || day > 28 {
		day = 1
	}
	now := time.Now()
	if now.Day() < day {
		return
	}
	month := prevMonth(now)
	if s.getSetting("report_sent", "") == month {
		return
	}
	if err := s.sendReport(ctx, month); err != nil {
		s.alert("backup", "warning", "Mjesečni izvještaj nije poslan: "+err.Error())
		return
	}
	s.setSetting("report_sent", month)
	addEvent(s, "info", "Poslan mjesečni izvještaj za "+monthLabel(month))
}

/* ---------- API ---------- */

func (s *server) handleReportStatus(w http.ResponseWriter, r *http.Request) {
	months := []string{}
	rows, err := s.db.Query(`SELECT DISTINCT substr(day,1,7) m FROM report_days
		ORDER BY m DESC LIMIT 24`)
	if err == nil {
		for rows.Next() {
			var m string
			if rows.Scan(&m) == nil {
				months = append(months, m)
			}
		}
		rows.Close()
	}
	var days int
	s.db.QueryRow(`SELECT COUNT(*) FROM report_days`).Scan(&days)
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":     s.getSetting("report_enabled", "0") == "1",
		"day":         s.getSetting("report_day", "1"),
		"keep_months": s.getSetting("report_keep_months", "13"),
		"last_sent":   s.getSetting("report_sent", ""),
		"smtp_ready": s.getSetting("smtp_host", "") != "" &&
			s.getSetting("smtp_to", "") != "",
		"months":         months,
		"days_collected": days,
		"prev_month":     prevMonth(time.Now()),
	})
}

func (s *server) handleReportSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled    *bool `json:"enabled"`
		Day        int   `json:"day"`
		KeepMonths int   `json:"keep_months"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Day == 0 {
		in.Day = 1
	}
	if in.Day < 1 || in.Day > 28 {
		writeErr(w, http.StatusBadRequest,
			"dan slanja mora biti između 1 i 28 (da postoji u svakom mjesecu)")
		return
	}
	if in.KeepMonths == 0 {
		in.KeepMonths = 13
	}
	if in.KeepMonths < 2 || in.KeepMonths > 60 {
		writeErr(w, http.StatusBadRequest, "čuvanje mora biti između 2 i 60 mjeseci")
		return
	}
	if in.Enabled != nil && *in.Enabled &&
		(s.getSetting("smtp_host", "") == "" || s.getSetting("smtp_to", "") == "") {
		writeErr(w, http.StatusBadRequest,
			"prvo popuni SMTP postavke (Status → Alerts), inače nema kamo poslati")
		return
	}
	s.setSetting("report_enabled", boolSetting(in.Enabled != nil && *in.Enabled))
	s.setSetting("report_day", strconv.Itoa(in.Day))
	s.setSetting("report_keep_months", strconv.Itoa(in.KeepMonths))
	addEvent(s, "info", "Promijenjene postavke mjesečnog izvještaja")
	writeJSON(w, http.StatusOK, map[string]bool{"saved": true})
}

func reportMonthParam(r *http.Request) string {
	m := strings.TrimSpace(r.URL.Query().Get("month"))
	if m == "" {
		m = prevMonth(time.Now())
	}
	return m
}

func (s *server) handleReportView(w http.ResponseWriter, r *http.Request) {
	d, err := s.buildReport(r.Context(), reportMonthParam(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.URL.Query().Get("format") == "html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition",
			`inline; filename="izvjestaj-`+d.Month+`.html"`)
		w.Write([]byte("<!doctype html><meta charset=\"utf-8\"><title>Izvještaj " +
			esc(d.MonthLabel) + "</title><body style=\"margin:24px\">" +
			renderReportHTML(d) + "</body>"))
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *server) handleReportSend(w http.ResponseWriter, r *http.Request) {
	month := reportMonthParam(r)
	if err := s.sendReport(r.Context(), month); err != nil {
		writeErr(w, http.StatusBadGateway, "slanje nije uspjelo: "+err.Error())
		return
	}
	addEvent(s, "info", "Ručno poslan mjesečni izvještaj za "+monthLabel(month))
	writeJSON(w, http.StatusOK, map[string]any{"sent": true, "month": month})
}
