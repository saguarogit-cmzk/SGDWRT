package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Data particija — zasebna particija za sve Saguaro podatke.
//
// Nadogradnja OpenWrt-a na x86 uređaju upisuje cijeli disk image, **zajedno s
// tablicom particija**. Sve što je na root particiji se time briše; do sada je
// Saguaro preživljavao preko keep liste, što je krhko (jedan zaboravljen put i
// nestanu backupi — dogodilo se).
//
// Ispravna podjela je ona koju koriste i uređaji s ozbiljnim firmwareom:
//
//	root particija (1 GB) — OpenWrt; prepisuje se pri svakoj nadogradnji
//	data particija (ostatak diska) — /opt/saguaro; nadogradnja je ne dira,
//	jer je disk image velik 1 GB i ne dopire do nje
//
// Poslije nadogradnje treba samo vratiti **zapis** o toj particiji u tablicu
// (nekoliko bajtova), a ne dirati podatke. To radi init skripta pri dizanju,
// i to tek nakon što provjeri da na zapisanom mjestu stvarno postoji ext4.

const dataPartMount = "/opt/saguaro"
const dataPartLabel = "saguaro-data"
const dataPartRecord = "/etc/saguaro-datapart.json"
const dataPartTmpMount = "/mnt/sag-data"

// dataPartMinBytes — ispod ovoga nema smisla dijeliti disk.
const dataPartMinBytes = 4 << 30

// partEntry je jedna particija na disku, onako kako je vidi jezgra.
type partEntry struct {
	Name    string `json:"name"`
	Num     int    `json:"num"`
	Start   int64  `json:"start"` // u sektorima od 512 B
	Sectors int64  `json:"sectors"`
	Bytes   int64  `json:"bytes"`
}

// partDeviceName sastavlja ime particije iz imena diska i broja. NVMe i eMMC
// diskovi (nvme0n1, mmcblk0) završavaju znamenkom i traže 'p' prije broja
// (nvme0n1p3), obični (sda) ne (sda3).
func partDeviceName(disk string, num int) string {
	if n := len(disk); n > 0 && disk[n-1] >= '0' && disk[n-1] <= '9' {
		return fmt.Sprintf("%sp%d", disk, num)
	}
	return fmt.Sprintf("%s%d", disk, num)
}

// listPartitions čita tablicu iz sysfs-a (bez vanjskih alata).
func listPartitions(disk string) []partEntry {
	out := []partEntry{}
	base := "/sys/block/" + disk
	ents, err := os.ReadDir(base)
	if err != nil {
		return out
	}
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), disk) {
			continue
		}
		p := filepath.Join(base, e.Name())
		num := sysfsInt(filepath.Join(p, "partition"))
		if num == 0 {
			continue
		}
		sec := sysfsInt(filepath.Join(p, "size"))
		out = append(out, partEntry{
			Name: e.Name(), Num: int(num),
			Start: sysfsInt(filepath.Join(p, "start")),
			Sectors: sec, Bytes: sec * 512,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// dataRecord je zapis geometrije koji preživi nadogradnju (leži na root
// particiji, u keep listi). Bez njega se poslije nadogradnje ne bi znalo gdje
// data particija počinje.
type dataRecord struct {
	Disk    string `json:"disk"`
	Part    int    `json:"part"`
	Start   int64  `json:"start"`   // sektor
	Sectors int64  `json:"sectors"` // veličina u sektorima
	UUID    string `json:"uuid"`
	At      string `json:"at"`
}

type dataPartState struct {
	Disk        string      `json:"disk"`
	Parts       []partEntry `json:"parts"`
	FreeBytes   int64       `json:"free_bytes"`   // slobodno iza zadnje particije
	FreeStart   int64       `json:"free_start"`   // prvi slobodan sektor (poravnat)
	Exists      bool        `json:"exists"`       // data particija postoji
	Device      string      `json:"device"`       // /dev/sda3
	SizeBytes   int64       `json:"size_bytes"`   //
	UsedBytes   int64       `json:"used_bytes"`   //
	Mounted     bool        `json:"mounted"`      // montirana na /opt/saguaro
	Ready       bool        `json:"ready"`        // može se stvoriti odmah
	Blocker     string      `json:"blocker"`      // ako ne može — zašto
	RootPartMB  int         `json:"root_part_mb"` //
	NextStepsCr []string    `json:"steps"`        // što treba napraviti, redom
}

func (s *server) dataPartState() dataPartState {
	d := readDiskInfo()
	st := dataPartState{Disk: d.Disk, RootPartMB: int(d.PartBytes >> 20)}
	if d.Disk == "" {
		st.Blocker = "disk se ne može očitati"
		return st
	}
	st.Parts = listPartitions(d.Disk)

	// zapis o postojećoj data particiji
	rec, haveRec := readDataRecord()
	for _, p := range st.Parts {
		if haveRec && p.Num == rec.Part {
			st.Exists = true
			st.Device = "/dev/" + p.Name
			st.SizeBytes = p.Bytes
		}
	}
	if st.Exists {
		st.Mounted = mountedOn(dataPartMount)
		if st.Mounted {
			st.UsedBytes = dirUsage(dataPartMount)
		}
		if st.Mounted {
			st.Blocker = ""
		} else {
			st.Blocker = "data particija postoji, ali nije montirana na " + dataPartMount
		}
		return st
	}

	// slobodan prostor iza zadnje particije
	var end int64
	for _, p := range st.Parts {
		if e := p.Start + p.Sectors; e > end {
			end = e
		}
	}
	total := d.DiskBytes / 512
	// poravnanje na 1 MiB — inače SSD pati, a parted se buni
	free := end
	if r := free % 2048; r != 0 {
		free += 2048 - r
	}
	st.FreeStart = free
	if total > free {
		st.FreeBytes = (total - free) * 512
	}

	switch {
	case st.FreeBytes >= dataPartMinBytes:
		st.Ready = true
	case d.PartBytes > d.DiskBytes/2:
		st.Blocker = fmt.Sprintf(
			"root particija zauzima gotovo cijeli disk (%d MB) — nema slobodnog "+
				"prostora. Prvo nadogradi OpenWrt: nova slika postavlja root na "+
				"1024 MB i time oslobađa ostatak diska.", int(d.PartBytes>>20))
		st.NextStepsCr = []string{
			"Backup → napravi punu kopiju i pošalji je na e-mail",
			"Updates → 2. OpenWrt → naruči sliku, preuzmi i nadogradi",
			"nakon dizanja se ovdje pojavi gumb za stvaranje data particije",
		}
	default:
		st.Blocker = fmt.Sprintf(
			"slobodno je samo %d MB, a za data particiju treba barem %d GB",
			int(st.FreeBytes>>20), dataPartMinBytes>>30)
	}
	return st
}

func mountedOn(target string) bool {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Fields(ln)
		if len(f) >= 5 && f[4] == target {
			return true
		}
	}
	return false
}

// dirUsage vraća zauzeće direktorija (zbroj veličina datoteka).
func dirUsage(dir string) int64 {
	var total int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func readDataRecord() (dataRecord, bool) {
	b, err := os.ReadFile(dataPartRecord)
	if err != nil {
		return dataRecord{}, false
	}
	var r dataRecord
	if json.Unmarshal(b, &r) != nil || r.Disk == "" || r.Part == 0 {
		return dataRecord{}, false
	}
	return r, true
}

func (s *server) handleDataPartStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.dataPartState())
}

// newFSUUID stvara UUID za novi filesystem. Zadaje se pri mkfs-u jer na
// uređaju nema ni blkid ni dumpe2fs, pa se poslije ne bi imalo odakle pročitati.
func newFSUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]), nil
}

func (s *server) handleDataPartCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Confirm string `json:"confirm"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	rel, err := s.owRelease(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if strings.TrimSpace(in.Confirm) != rel.Hostname {
		writeErr(w, http.StatusBadRequest, "za potvrdu upiši ime uređaja: "+rel.Hostname)
		return
	}
	st := s.dataPartState()
	if st.Exists {
		writeErr(w, http.StatusConflict, "data particija već postoji ("+st.Device+")")
		return
	}
	if !st.Ready {
		writeErr(w, http.StatusConflict, st.Blocker)
		return
	}
	// svjež backup je uvjet — zahvat dira tablicu particija
	backupName, _, err := s.createFullBackup(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup prije zahvata: "+err.Error())
		return
	}

	num := 1
	for _, p := range st.Parts {
		if p.Num >= num {
			num = p.Num + 1
		}
	}
	if num > 4 {
		writeErr(w, http.StatusConflict,
			"disk već ima četiri primarne particije — nema mjesta za novu")
		return
	}
	disk := "/dev/" + st.Disk
	dev := "/dev/" + partDeviceName(st.Disk, num)
	endSector := (readDiskInfo().DiskBytes / 512) - 1

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	// 1. zapis u tablicu particija
	out, err := exec.CommandContext(ctx, "parted", "-s", disk, "mkpart", "primary",
		"ext4", fmt.Sprintf("%ds", st.FreeStart), fmt.Sprintf("%ds", endSector)).CombinedOutput()
	if err != nil {
		writeErr(w, http.StatusInternalServerError,
			"stvaranje particije: "+err.Error()+": "+strings.TrimSpace(string(out)))
		return
	}
	_ = exec.CommandContext(ctx, "partx", "-a", disk).Run()
	// jezgra treba trenutak da uređaj postane vidljiv
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(dev); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, err := os.Stat(dev); err != nil {
		writeErr(w, http.StatusInternalServerError,
			"particija je stvorena, ali jezgra ne vidi "+dev)
		return
	}

	// 2. datotečni sustav; UUID se zadaje jer ga poslije nema odakle pročitati
	uuid, err := newFSUUID()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out, err = exec.CommandContext(ctx, "mkfs.ext4", "-q", "-F",
		"-L", dataPartLabel, "-U", uuid, "-m", "0", dev).CombinedOutput()
	if err != nil {
		writeErr(w, http.StatusInternalServerError,
			"mkfs: "+err.Error()+": "+strings.TrimSpace(string(out)))
		return
	}

	// 3. zapis u fstab (montira se pri svakom dizanju, prije Saguaro servisa)
	sec := "sag_data"
	script := fmt.Sprintf(""+
		"set fstab.%s=mount\n"+
		"set fstab.%s.uuid=%s\n"+
		"set fstab.%s.target=%s\n"+
		"set fstab.%s.options=rw,noatime\n"+
		"set fstab.%s.enabled=1\n"+
		"commit fstab\n", sec, sec, uuid, sec, dataPartMount, sec, sec)
	if err := uciBatch(ctx, script); err != nil {
		writeErr(w, http.StatusInternalServerError, "fstab: "+err.Error())
		return
	}

	// 4. zapis geometrije za oporavak nakon nadogradnje
	parts := listPartitions(st.Disk)
	var mine partEntry
	for _, p := range parts {
		if p.Num == num {
			mine = p
		}
	}
	rb, _ := json.Marshal(dataRecord{Disk: st.Disk, Part: num, Start: mine.Start,
		Sectors: mine.Sectors, UUID: uuid, At: time.Now().Format(time.RFC3339)})
	if err := os.WriteFile(dataPartRecord, rb, 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, "zapis geometrije: "+err.Error())
		return
	}
	_ = ensureKeepList(s.etcDir, s.dataDir)

	// 5. selidba i restart — odvojeno od HTTP zahtjeva, jer se servis gasi
	mv := fmt.Sprintf(`(
sleep 2
logger -t saguaro "selidba na data particiju: pocetak"
mkdir -p %[1]s
mount %[2]s %[1]s || { logger -t saguaro "selidba: mount pao"; exit 1; }
cp -a %[3]s/. %[1]s/ || { logger -t saguaro "selidba: kopiranje palo"; umount %[1]s; exit 1; }
sync
/etc/init.d/saguaro-core stop
umount %[1]s
mount %[2]s %[3]s || { logger -t saguaro "selidba: zavrsni mount pao"; /etc/init.d/saguaro-core start; exit 1; }
logger -t saguaro "selidba na data particiju: gotovo"
/etc/init.d/saguaro-core start
) >/dev/null 2>&1 &`, dataPartTmpMount, dev, dataPartMount)
	if err := exec.Command("/bin/sh", "-c", mv).Run(); err != nil {
		writeErr(w, http.StatusInternalServerError, "pokretanje selidbe: "+err.Error())
		return
	}

	addEvent(s, "warning", "Stvorena data particija "+dev+
		" — Saguaro podaci se sele na nju")
	writeJSON(w, http.StatusOK, map[string]any{
		"device": dev, "uuid": uuid, "size_bytes": mine.Bytes,
		"backup": backupName,
		"note": "Particija je napravljena, podaci se sele. Servis se ponovno " +
			"pokreće — sučelje se javi za desetak sekundi.",
	})
}
