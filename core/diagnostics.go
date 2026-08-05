package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Dijagnostika: tko trenutno s kim razgovara (conntrack) i snimanje prometa
// u .pcap datoteku (tcpdump).
//
// Aktivne veze se čitaju izravno iz /proc/net/nf_conntrack — to je zapis same
// jezgre, pa ne treba nikakav dodatni paket. Snimanje prometa treba
// tcpdump-mini; instalira se na klik, kao i haproxy za obrnuti proxy.

/* ---------- aktivne veze ---------- */

type connEntry struct {
	Family   string `json:"family"` // ipv4 | ipv6
	Proto    string `json:"proto"`  // tcp | udp | icmp | ...
	State    string `json:"state"`  // ESTABLISHED, TIME_WAIT... (prazno za udp)
	Src      string `json:"src"`
	Dst      string `json:"dst"`
	SPort    int    `json:"sport"`
	DPort    int    `json:"dport"`
	OutBytes int64  `json:"out_bytes"` // izvor -> odredište
	InBytes  int64  `json:"in_bytes"`  // odgovor natrag
	Timeout  int    `json:"timeout_s"` // koliko još jezgra drži zapis
	SrcName  string `json:"src_name,omitempty"`
	// Unreplied: jezgra nije vidjela nijedan paket u povratnom smjeru. To je
	// normalno za tek započete veze, ali kad takvih ima puno, uređaj vidi samo
	// pola prometa — najčešće jer nije stvarni izlaz te mreže, nego stoji uz
	// postojeći router (nađeno na uređaju 05.08.2026.).
	Unreplied bool `json:"unreplied"`
}

// deviceSummary zbraja veze po lokalnom uređaju — „tko troši vezu".
type deviceSummary struct {
	IP       string `json:"ip"`
	Name     string `json:"name,omitempty"`
	Conns    int    `json:"conns"`
	OutBytes int64  `json:"out_bytes"`
	InBytes  int64  `json:"in_bytes"`
}

// parseConntrackLine čita jedan redak nf_conntrack zapisa. Format je stabilan
// (ključ=vrijednost), prvi src/dst par je originalni smjer, drugi odgovor.
func parseConntrackLine(line string) (connEntry, bool) {
	f := strings.Fields(line)
	if len(f) < 4 {
		return connEntry{}, false
	}
	e := connEntry{Family: f[0], Proto: f[2]}
	e.Timeout, _ = strconv.Atoi(f[4])
	first := true // prvi src= je originalni smjer, drugi je odgovor
	for _, tok := range f[4:] {
		k, v, ok := strings.Cut(tok, "=")
		if !ok {
			// Stanje TCP veze stoji kao goli token (ESTABLISHED, TIME_WAIT…).
			// Mora sadržavati slovo — i timeout je goli token, ali brojčani,
			// pa bi bez ove provjere "114" završio kao stanje (viđeno uživo).
			if tok == "[UNREPLIED]" {
				e.Unreplied = true
				continue
			}
			if e.Proto == "tcp" && e.State == "" && len(tok) > 2 &&
				strings.ToUpper(tok) == tok && !strings.HasPrefix(tok, "[") &&
				strings.IndexFunc(tok, func(r rune) bool {
					return r >= 'A' && r <= 'Z'
				}) >= 0 {
				e.State = tok
			}
			continue
		}
		switch k {
		case "src":
			if first {
				e.Src = v
			}
		case "dst":
			if first {
				e.Dst = v
			}
		case "sport":
			if first {
				e.SPort, _ = strconv.Atoi(v)
			}
		case "dport":
			if first {
				e.DPort, _ = strconv.Atoi(v)
			}
		case "bytes":
			n, _ := strconv.ParseInt(v, 10, 64)
			if first {
				e.OutBytes = n
				first = false // bytes= zatvara prvi smjer
			} else {
				e.InBytes = n
			}
		}
	}
	if e.Src == "" || e.Dst == "" {
		return connEntry{}, false
	}
	return e, true
}

// connMaxRows ograničava odgovor — sučelje s 10000 redaka ionako nitko ne
// čita, a sortirano po prometu bitno je pri vrhu.
const connMaxRows = 500

func (s *server) handleConnections(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile("/proc/net/nf_conntrack")
	if err != nil {
		writeErr(w, http.StatusInternalServerError,
			"conntrack tablica nije čitljiva: "+err.Error())
		return
	}
	// ime uređaja iz DHCP leasea, da tablica ne bude samo brojevi
	names := map[string]string{}
	for _, l := range parseLeases(leaseFile) {
		if l.Hostname != "" {
			names[l.IP] = l.Hostname
		}
	}
	conns := []connEntry{}
	perDev := map[string]*deviceSummary{}
	var totalOut, totalIn int64
	unreplied, totalConns := 0, 0
	for _, ln := range strings.Split(string(b), "\n") {
		e, ok := parseConntrackLine(ln)
		if !ok {
			continue
		}
		e.SrcName = names[e.Src]
		totalConns++
		if e.Unreplied {
			unreplied++
		}
		totalOut += e.OutBytes
		totalIn += e.InBytes
		d := perDev[e.Src]
		if d == nil {
			d = &deviceSummary{IP: e.Src, Name: names[e.Src]}
			perDev[e.Src] = d
		}
		d.Conns++
		d.OutBytes += e.OutBytes
		d.InBytes += e.InBytes
		conns = append(conns, e)
	}
	// najveći promet na vrh — to se traži kad se pita „što troši vezu"
	sort.Slice(conns, func(i, j int) bool {
		return conns[i].OutBytes+conns[i].InBytes > conns[j].OutBytes+conns[j].InBytes
	})
	truncated := false
	if len(conns) > connMaxRows {
		conns = conns[:connMaxRows]
		truncated = true
	}
	devs := make([]deviceSummary, 0, len(perDev))
	for _, d := range perDev {
		devs = append(devs, *d)
	}
	sort.Slice(devs, func(i, j int) bool {
		return devs[i].OutBytes+devs[i].InBytes > devs[j].OutBytes+devs[j].InBytes
	})
	if len(devs) > 50 {
		devs = devs[:50]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connections": conns,
		"devices":     devs,
		"total":       len(perDev),
		"truncated":   truncated,
		"total_out":   totalOut,
		"total_in":    totalIn,
		"unreplied":   unreplied,
		"conns_total": totalConns,
		// Više od 40 % veza bez ijednog paketa natrag nije normalno stanje
		// mreže — tada uređaj gotovo sigurno vidi samo jedan smjer prometa i
		// sve brojke ovdje treba čitati s tim na umu.
		"one_sided": totalConns >= 10 && unreplied*100/totalConns > 40,
	})
}

/* ---------- snimanje prometa ---------- */

// Snimke idu na data particiju — tamo ima mjesta, a ne troše root.
func (s *server) captureDir() string { return filepath.Join(filepath.Dir(s.etcDir), "log") }

const capMaxSeconds = 600      // najviše 10 minuta po snimci
const capMaxFileMB = 100       // tcpdump sam stane na ovoj veličini
const capSnapLen = "96"        // zaglavlja su dovoljna za analizu, sadržaj ne treba
const tcpdumpBin = "/usr/bin/tcpdump"

var capMu sync.Mutex
var capProc *exec.Cmd
var capFile string
var capStarted time.Time

func tcpdumpInstalled() bool {
	_, err := os.Stat(tcpdumpBin)
	return err == nil
}

func (s *server) handleCaptureStatus(w http.ResponseWriter, r *http.Request) {
	capMu.Lock()
	running := capProc != nil
	file := capFile
	started := capStarted
	capMu.Unlock()

	files := []map[string]any{}
	if ents, err := os.ReadDir(s.captureDir()); err == nil {
		for _, e := range ents {
			if !strings.HasPrefix(e.Name(), "snimka-") || !strings.HasSuffix(e.Name(), ".pcap") {
				continue
			}
			if info, err := e.Info(); err == nil {
				files = append(files, map[string]any{
					"name": e.Name(), "size_bytes": info.Size(),
					"modified_at": info.ModTime().Unix(),
				})
			}
		}
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i]["modified_at"].(int64) > files[j]["modified_at"].(int64)
	})
	out := map[string]any{
		"installed":   tcpdumpInstalled(),
		"running":     running,
		"files":       files,
		"max_seconds": capMaxSeconds,
		"max_file_mb": capMaxFileMB,
		"ifaces":      s.routeIfaceSubnets(r.Context()),
	}
	if running {
		out["file"] = filepath.Base(file)
		out["running_s"] = int(time.Since(started).Seconds())
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleCaptureInstall(w http.ResponseWriter, r *http.Request) {
	if !tcpdumpInstalled() {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		_ = exec.CommandContext(ctx, "apk", "update").Run()
		out, err := exec.CommandContext(ctx, "apk", "add", "tcpdump-mini").CombinedOutput()
		if err != nil {
			writeErr(w, http.StatusInternalServerError,
				"instalacija: "+err.Error()+": "+string(out))
			return
		}
	}
	addEvent(s, "info", "Instaliran alat za snimanje prometa (tcpdump-mini)")
	writeJSON(w, http.StatusOK, map[string]any{"installed": tcpdumpInstalled()})
}

// validCapFilter propušta tcpdump filter izraz. Filter ide kao zaseban
// argument (bez ljuske), pa je opasnost mala; brane se samo kontrolni znakovi
// i pretjerana duljina.
func validCapFilter(f string) bool {
	if len(f) > 200 || hasCtrl(f) {
		return false
	}
	return true
}

func (s *server) handleCaptureStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Iface   string `json:"iface"`
		Seconds int    `json:"seconds"`
		Filter  string `json:"filter"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !tcpdumpInstalled() {
		writeErr(w, http.StatusConflict, "tcpdump nije instaliran — prvo klikni Instaliraj")
		return
	}
	if in.Seconds <= 0 {
		in.Seconds = 60
	}
	if in.Seconds > capMaxSeconds {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("najdulje %d sekundi po snimci", capMaxSeconds))
		return
	}
	in.Iface = strings.TrimSpace(in.Iface)
	if in.Iface == "" {
		in.Iface = "any"
	}
	if in.Iface != "any" {
		if _, err := os.Stat("/sys/class/net/" + in.Iface); err != nil {
			writeErr(w, http.StatusBadRequest, "sučelje "+in.Iface+" ne postoji")
			return
		}
	}
	in.Filter = strings.TrimSpace(in.Filter)
	if in.Filter != "" && !validCapFilter(in.Filter) {
		writeErr(w, http.StatusBadRequest, "neispravan filter izraz")
		return
	}

	capMu.Lock()
	defer capMu.Unlock()
	if capProc != nil {
		writeErr(w, http.StatusConflict, "snimanje je već u tijeku")
		return
	}
	name := "snimka-" + time.Now().Format("20060102-150405") + ".pcap"
	path := filepath.Join(s.captureDir(), name)
	// Granice trajanja i veličine čuva naš nadzorni timer, ne tcpdump opcije:
	// -G/-C/-W se na mini gradnji ne ponašaju jednako svugdje, a timer uz to
	// pokriva i slučaj da tcpdump stane pisati a proces ostane visjeti.
	args := []string{"-i", in.Iface, "-s", capSnapLen, "-w", path}
	if in.Filter != "" {
		args = append(args, in.Filter)
	}
	cmd := exec.Command(tcpdumpBin, args...)
	if err := cmd.Start(); err != nil {
		writeErr(w, http.StatusInternalServerError, "pokretanje: "+err.Error())
		return
	}
	capProc = cmd
	capFile = path
	capStarted = time.Now()

	// snimanje se samo zaustavi nakon zadanog vremena ili kad datoteka
	// prijeđe granicu — zaboravljena snimka ne smije puniti disk danima
	go func(c *exec.Cmd, p string, limit time.Duration) {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for range t.C {
			capMu.Lock()
			if capProc != c {
				capMu.Unlock()
				return
			}
			over := time.Since(capStarted) >= limit
			if st, err := os.Stat(p); err == nil && st.Size() > capMaxFileMB<<20 {
				over = true
			}
			if over {
				_ = c.Process.Kill()
				_, _ = c.Process.Wait()
				capProc = nil
				capMu.Unlock()
				addEvent(s, "info", "Snimanje prometa završeno: "+filepath.Base(p))
				return
			}
			capMu.Unlock()
		}
	}(cmd, path, time.Duration(in.Seconds)*time.Second)

	addEvent(s, "info", fmt.Sprintf("Pokrenuto snimanje prometa (%s, %d s)", in.Iface, in.Seconds))
	writeJSON(w, http.StatusOK, map[string]any{"started": true, "file": name})
}

func (s *server) handleCaptureStop(w http.ResponseWriter, r *http.Request) {
	capMu.Lock()
	defer capMu.Unlock()
	if capProc == nil {
		writeJSON(w, http.StatusOK, map[string]any{"stopped": false})
		return
	}
	_ = capProc.Process.Kill()
	_, _ = capProc.Process.Wait()
	name := filepath.Base(capFile)
	capProc = nil
	addEvent(s, "info", "Snimanje prometa zaustavljeno: "+name)
	writeJSON(w, http.StatusOK, map[string]any{"stopped": true, "file": name})
}

// safeCapName dopušta samo nazive koje smo sami stvorili.
func safeCapName(name string) bool {
	return name == filepath.Base(name) && strings.HasPrefix(name, "snimka-") &&
		strings.HasSuffix(name, ".pcap") && !strings.Contains(name, "..")
}

func (s *server) handleCaptureDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !safeCapName(name) {
		writeErr(w, http.StatusNotFound, "snimka ne postoji")
		return
	}
	path := filepath.Join(s.captureDir(), name)
	if _, err := os.Stat(path); err != nil {
		writeErr(w, http.StatusNotFound, "snimka ne postoji")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.tcpdump.pcap")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, path)
}

func (s *server) handleCaptureDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !safeCapName(name) {
		writeErr(w, http.StatusNotFound, "snimka ne postoji")
		return
	}
	if err := os.Remove(filepath.Join(s.captureDir(), name)); err != nil {
		writeErr(w, http.StatusNotFound, "snimka ne postoji")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}
