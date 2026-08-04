package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Stanje diska i root particije.
//
// Zašto uopće postoji: na x86 uređajima nadogradnja OpenWrt-a upisuje cijelu
// sliku, zajedno s tablicom particija. Root particija se time vraća na
// veličinu koju slika nosi (zadano ~104 MB), bez obzira koliko je disk velik
// i kolika je bila prije. Sustav se onda napuni do 98 % i počne se ponašati
// nepredvidivo, a naknadno širenje particije na živom uređaju je opasno.
//
// Zato Saguaro veličinu root particije rješava **prije** nadogradnje: slika se
// od službenog servisa naručuje s traženom veličinom root particije
// (rootfs_size_mb), pa nakon dizanja nema što širiti. Ovdje je čitanje stanja
// i provjere koje na to paze.

// asuMaxRootfsMB je gornja granica koju službeni servis dopušta za
// rootfs_size_mb (vidi njihov openapi.json). Veće se ne može naručiti.
const asuMaxRootfsMB = 1024

// rootfsMinFreeMB je rezerva koja mora ostati slobodna nakon nadogradnje —
// ispod toga se nadogradnja ne pušta bez izričite potvrde.
const rootfsMinFreeMB = 64

// diskInfo opisuje disk s kojeg se uređaj diže.
type diskInfo struct {
	Disk       string `json:"disk"`        // sda
	DiskBytes  int64  `json:"disk_bytes"`  // cijeli disk
	Part       string `json:"part"`        // sda2
	PartNum    int    `json:"part_num"`    // 2
	PartBytes  int64  `json:"part_bytes"`  // root particija
	Parts      int    `json:"parts"`       // koliko particija disk ima
	FreeTail   int64  `json:"free_tail"`   // neiskorišteno iza zadnje particije
	Removable  bool   `json:"removable"`   //
	Sectorless bool   `json:"-"`           // interno: nije particija nego cijeli disk
	FSBytes    int64  `json:"fs_bytes"`    // datotečni sustav na /
	FSUsed     int64  `json:"fs_used"`     //
	FSFree     int64  `json:"fs_free"`     //
	Err        string `json:"error,omitempty"`
}

// rootMajMin čita glavni/sporedni broj uređaja na kojem je korijen sustava.
// Čita se iz mountinfo jer BusyBox nema ništa bolje, a /dev/root je simbolička
// veza koja ne govori o kojem se disku radi.
func rootMajMin() (string, error) {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", err
	}
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Fields(ln)
		if len(f) < 5 {
			continue
		}
		if f[4] == "/" {
			return f[2], nil
		}
	}
	return "", fmt.Errorf("korijenski datotečni sustav nije pronađen u mountinfo")
}

func sysfsInt(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

// readDiskInfo skuplja sve iz sysfs-a — bez vanjskih alata, pa radi i kad
// parted nije instaliran.
func readDiskInfo() diskInfo {
	var d diskInfo
	mm, err := rootMajMin()
	if err != nil {
		d.Err = err.Error()
		return d
	}
	target, err := filepath.EvalSymlinks("/sys/dev/block/" + mm)
	if err != nil {
		d.Err = "sysfs ne poznaje uređaj " + mm
		return d
	}
	name := filepath.Base(target)
	parent := filepath.Base(filepath.Dir(target))

	if _, err := os.Stat(filepath.Join(target, "partition")); err == nil {
		// korijen je na particiji
		d.Part = name
		d.PartNum = int(sysfsInt(filepath.Join(target, "partition")))
		d.PartBytes = sysfsInt(filepath.Join(target, "size")) * 512
		d.Disk = parent
	} else {
		// korijen je na cijelom disku (nema tablice particija)
		d.Disk = name
		d.Part = name
		d.Sectorless = true
	}
	base := "/sys/block/" + d.Disk
	d.DiskBytes = sysfsInt(base+"/size") * 512
	d.Removable = sysfsInt(base+"/removable") == 1
	if d.Sectorless {
		d.PartBytes = d.DiskBytes
	}

	// koliko je prostora iza zadnje particije — to je ono što stoji neiskorišteno
	if ents, err := os.ReadDir(base); err == nil {
		var end int64
		for _, e := range ents {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), d.Disk) {
				continue
			}
			p := filepath.Join(base, e.Name())
			if _, err := os.Stat(filepath.Join(p, "partition")); err != nil {
				continue
			}
			d.Parts++
			if e := (sysfsInt(p+"/start") + sysfsInt(p+"/size")) * 512; e > end {
				end = e
			}
		}
		if d.DiskBytes > end {
			d.FreeTail = d.DiskBytes - end
		}
	}
	return d
}

// fillRootFS dopunjava podatke o datotečnom sustavu iz ubus-a (iste brojke
// koje pokazuje nadzor); vrijednosti su u kilobajtima.
func (s *server) fillRootFS(d *diskInfo) {
	var info struct {
		Root struct {
			Total int64 `json:"total"`
			Free  int64 `json:"free"`
			Used  int64 `json:"used"`
		} `json:"root"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ubusCall(ctx, "system", "info", &info); err != nil {
		return
	}
	d.FSBytes = info.Root.Total * 1024
	d.FSUsed = info.Root.Used * 1024
	d.FSFree = info.Root.Free * 1024
}

// diskState je ono što sučelje prikazuje: stanje + jasna poruka što učiniti.
type diskState struct {
	diskInfo
	State        string `json:"state"`         // ok | tijesno | premalo | nepoznato
	Note         string `json:"note"`          //
	RecommendMB  int    `json:"recommend_mb"`  // koliko naručiti pri nadogradnji
	ShrunkBefore int64  `json:"shrunk_before"` // veličina prije zadnje nadogradnje
	Shrunk       bool   `json:"shrunk"`        // nadogradnja ju je smanjila
}

// recommendRootfsMB bira veličinu root particije za sljedeću sliku: barem
// koliko je sada, s prostorom za rast, ali ne preko granice servisa i ne preko
// veličine samog diska.
func recommendRootfsMB(d diskInfo) int {
	cur := int(d.PartBytes >> 20)
	used := int(d.FSUsed >> 20)
	want := used * 3
	if want < cur {
		want = cur
	}
	if want < 512 {
		want = 512
	}
	if want > asuMaxRootfsMB {
		want = asuMaxRootfsMB
	}
	if disk := int(d.DiskBytes>>20) - 64; disk > 0 && want > disk {
		want = disk
	}
	return want
}

// diskBeforeFile pamti veličinu root particije prije nadogradnje, da se poslije
// dizanja može reći je li se smanjila. Direktorij je u keep listi, pa zapis
// preživi nadogradnju.
func (s *server) diskBeforeFile() string { return filepath.Join(s.etcDir, "disk-before.json") }

type diskBefore struct {
	PartBytes int64  `json:"part_bytes"`
	FSUsed    int64  `json:"fs_used"`
	Version   string `json:"version"`
	At        string `json:"at"`
}

func (s *server) readDiskBefore() (diskBefore, bool) {
	b, err := os.ReadFile(s.diskBeforeFile())
	if err != nil {
		return diskBefore{}, false
	}
	var db diskBefore
	if json.Unmarshal(b, &db) != nil || db.PartBytes == 0 {
		return diskBefore{}, false
	}
	return db, true
}

// diskState spaja sve u ocjenu stanja.
func (s *server) diskState() diskState {
	d := readDiskInfo()
	s.fillRootFS(&d)
	st := diskState{diskInfo: d, RecommendMB: recommendRootfsMB(d)}
	if d.Err != "" || d.FSBytes == 0 {
		st.State = "nepoznato"
		st.Note = "veličina korijenske particije se ne može očitati"
		return st
	}
	if db, ok := s.readDiskBefore(); ok {
		st.ShrunkBefore = db.PartBytes
		// 10 % tolerancije — slike nikad nisu na bajt jednake
		if d.PartBytes*10 < db.PartBytes*9 {
			st.Shrunk = true
		}
	}
	freeMB := int(d.FSFree >> 20)
	switch {
	case freeMB < rootfsMinFreeMB/2:
		st.State = "premalo"
		st.Note = fmt.Sprintf("na korijenskoj particiji je slobodno samo %d MB — "+
			"sustav može prestati raditi ispravno", freeMB)
	case freeMB < rootfsMinFreeMB:
		st.State = "tijesno"
		st.Note = fmt.Sprintf("na korijenskoj particiji je slobodno %d MB", freeMB)
	default:
		st.State = "ok"
		st.Note = fmt.Sprintf("slobodno %d MB od %d MB", freeMB, int(d.FSBytes>>20))
	}
	if st.Shrunk {
		st.Note += fmt.Sprintf("; nadogradnja je particiju smanjila s %d MB na %d MB",
			int(st.ShrunkBefore>>20), int(d.PartBytes>>20))
	}
	return st
}

// checkRootAfterUpgrade se izvodi pri pokretanju servisa. Ako je nadogradnja
// smanjila root particiju ili je ostalo premalo mjesta, javlja odmah — prije se
// to primjećivalo tek kad bi uređaj stao.
func (s *server) checkRootAfterUpgrade() {
	st := s.diskState()
	if st.State == "nepoznato" {
		return
	}
	switch {
	case st.Shrunk:
		s.alert("resources", "warn", fmt.Sprintf(
			"Nakon nadogradnje je korijenska particija manja nego prije: %d MB umjesto %d MB "+
				"(slobodno %d MB). Kod sljedeće nadogradnje u modulu Updates zatraži "+
				"veličinu root particije od %d MB.",
			int(st.PartBytes>>20), int(st.ShrunkBefore>>20), int(st.FSFree>>20), st.RecommendMB))
	case st.State == "premalo":
		s.alert("resources", "warn", "Korijenska particija je gotovo puna: "+st.Note)
	}
	// zapis se troši jednom — poruka se ne ponavlja svaki restart
	if st.Shrunk {
		_ = os.Remove(s.diskBeforeFile())
	}
}

func (s *server) handleDiskStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.diskState())
}
