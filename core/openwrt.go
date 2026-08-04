package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Nadogradnja samog OpenWrt-a iz sučelja.
//
// Obična slika s downloads.openwrt.org sadrži samo zadane pakete, pa bi nakon
// nadogradnje nestali mwan3, banIP, OpenVPN, bird2 i ostalo. Zato se slika
// naručuje od službenog servisa za izgradnju (sysupgrade.openwrt.org, isti
// koji koristi alat owut) s popisom paketa ovog uređaja.
//
// Ovo je jedina radnja u sustavu koja se ne može poništiti na daljinu, pa ima
// tri kočnice: provjera SHA256 otiska, obavezan svjež backup i potvrda
// upisom imena uređaja.

const asuBase = "https://sysupgrade.openwrt.org"

// reePkg propušta samo uobičajene nazive paketa — popis se doinstalira
// naredbom, pa ne smije nositi ništa drugo
var reePkg = regexp.MustCompile(`^[a-zA-Z0-9._+-]{1,64}$`)

const owImagePath = "/tmp/saguaro-sysupgrade.img.gz"
const owMaxImage = 400 << 20 // 400 MB — x86 slike su ~50 MB, ovo je gornja brana

// owPackagesFile čuva popis paketa preko nadogradnje (direktorij je u keep
// listi), da se nakon dizanja može provjeriti je li sve preživjelo.
func (s *server) owPackagesFile() string { return filepath.Join(s.etcDir, "packages.list") }
func (s *server) owLogFile() string      { return filepath.Join(s.etcDir, "sysupgrade.log") }
func (s *server) owMetaFile() string     { return filepath.Join(s.etcDir, "sysupgrade.json") }

type owMeta struct {
	Version  string `json:"version"`
	Image    string `json:"image"`
	SHA256   string `json:"sha256"`
	Source   string `json:"source"` // asu | upload
	Size     int64  `json:"size"`
	At       string `json:"at"`
	RootfsMB int    `json:"rootfs_mb"` // 0 = nepoznato (slika s računala)
}

// worldPackages čita popis izričito instaliranih paketa (apk "world").
// Verzijska ograničenja se odbacuju — servisu se šalju samo nazivi.
func worldPackages() []string {
	b, err := os.ReadFile("/etc/apk/world")
	if err != nil {
		return nil
	}
	out := []string{}
	for _, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		for _, sep := range []string{"=", ">", "<", "~"} {
			if i := strings.Index(ln, sep); i > 0 {
				ln = ln[:i]
			}
		}
		out = append(out, ln)
	}
	return out
}

func bootMode() string {
	if _, err := os.Stat("/sys/firmware/efi"); err == nil {
		return "EFI"
	}
	return "BIOS"
}

// owRelease čita podatke o izdanju s uređaja (ubus system board).
type owRelease struct {
	Version  string
	Revision string
	Target   string // npr. x86/64
	RootFS   string // ext4 | squashfs
	Hostname string
}

func (s *server) owRelease(ctx context.Context) (owRelease, error) {
	var board struct {
		Hostname   string `json:"hostname"`
		RootfsType string `json:"rootfs_type"`
		Release    struct {
			Version  string `json:"version"`
			Revision string `json:"revision"`
			Target   string `json:"target"`
		} `json:"release"`
	}
	if err := ubusCall(ctx, "system", "board", &board); err != nil {
		return owRelease{}, err
	}
	return owRelease{
		Version:  board.Release.Version,
		Revision: board.Release.Revision,
		Target:   board.Release.Target,
		RootFS:   board.RootfsType,
		Hostname: board.Hostname,
	}, nil
}

// asuLatest vraća najnovije stabilno izdanje po granama.
func asuLatest(ctx context.Context) ([]string, error) {
	var out struct {
		Latest []string `json:"latest"`
	}
	if err := httpGetJSON(ctx, asuBase+"/api/v1/overview", &out); err != nil {
		return nil, err
	}
	return out.Latest, nil
}

func httpGetJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	cl := &http.Client{Timeout: 30 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

// upgradeCandidate javlja postoji li novije izdanje u istoj grani
// (25.12.4 -> 25.12.5). Prelazak na drugu granu je odluka koju korisnik
// donosi svjesno, pa se ne nudi automatski.
func upgradeCandidate(current string, latest []string) string {
	curMajor := branchOf(current)
	for _, l := range latest {
		if branchOf(l) == curMajor && l != current {
			return l
		}
	}
	return ""
}

func branchOf(v string) string {
	p := strings.Split(v, ".")
	if len(p) < 2 {
		return v
	}
	return p[0] + "." + p[1]
}

func (s *server) handleOpenWrtStatus(w http.ResponseWriter, r *http.Request) {
	rel, err := s.owRelease(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	pkgs := worldPackages()
	out := map[string]any{
		"version":   rel.Version,
		"revision":  rel.Revision,
		"target":    rel.Target,
		"rootfs":    rel.RootFS,
		"boot_mode": bootMode(),
		"hostname":  rel.Hostname,
		"packages":  len(pkgs),
	}
	// zadnja pripremljena slika (preživi ponovno otvaranje sučelja)
	if b, err := os.ReadFile(s.owMetaFile()); err == nil {
		var m owMeta
		if json.Unmarshal(b, &m) == nil {
			if st, err := os.Stat(owImagePath); err == nil && st.Size() == m.Size {
				out["staged"] = m
			}
		}
	}
	if lat, err := asuLatest(r.Context()); err == nil {
		out["latest"] = lat
		out["candidate"] = upgradeCandidate(rel.Version, lat)
	} else {
		out["latest_error"] = "nema pristupa servisu izdanja: " + err.Error()
	}
	// stanje diska — nadogradnja x86 slike prepisuje tablicu particija, pa je
	// veličina root particije dio odluke, ne naknadna briga
	ds := s.diskState()
	out["disk"] = ds
	// zadnji backup — bez njega se nadogradnja ne pušta
	if t, name := s.lastBackupInfo(); name != "" {
		out["last_backup"] = name
		out["last_backup_age_min"] = int(time.Since(t).Minutes())
	}
	writeJSON(w, http.StatusOK, out)
}

// lastBackupInfo vraća vrijeme i naziv zadnje pune arhive.
func (s *server) lastBackupInfo() (time.Time, string) {
	ents, err := os.ReadDir(s.backupDir)
	if err != nil {
		return time.Time{}, ""
	}
	var newest time.Time
	var name string
	for _, e := range ents {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "full-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest, name = info.ModTime(), e.Name()
		}
	}
	return newest, name
}

/* ---------- naručivanje slike od službenog servisa ---------- */

type asuImage struct {
	Name       string `json:"name"`
	SHA256     string `json:"sha256"`
	Type       string `json:"type"`
	Filesystem string `json:"filesystem"`
}

// Odgovor servisa: `status` je broj (isti kao HTTP kod), `detail` je stanje
// ("queued", "started", "done"), a `imagebuilder_status` korak gradnje.
type asuResponse struct {
	RequestHash  string     `json:"request_hash"`
	Status       int        `json:"status"`
	BinDir       string     `json:"bin_dir"`
	Images       []asuImage `json:"images"`
	Detail       string     `json:"detail"`
	BuilderState string     `json:"imagebuilder_status"`
	QueuePos     int        `json:"queue_position"`
}

// pickImage bira sliku koja odgovara ovom uređaju: kombinirana slika, isti
// datotečni sustav i ista vrsta pokretanja (EFI ili BIOS). Kriva slika je
// najbrži način da uređaj ostane bez sustava.
func pickImage(images []asuImage, rootfs, boot string) (asuImage, error) {
	want := "combined"
	if boot == "EFI" {
		want = "combined-efi"
	}
	for _, im := range images {
		if im.Type != want || im.Filesystem != rootfs {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(im.Name), ".img.gz") {
			continue
		}
		return im, nil
	}
	return asuImage{}, fmt.Errorf(
		"servis nije ponudio sliku za ovaj uređaj (%s, %s) — nadogradnja se ne nastavlja",
		rootfs, boot)
}

func (s *server) handleOpenWrtBuild(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version  string `json:"version"`
		RootfsMB int    `json:"rootfs_mb"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	rel, err := s.owRelease(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if in.Version == "" {
		lat, err := asuLatest(r.Context())
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		in.Version = upgradeCandidate(rel.Version, lat)
	}
	if in.Version == "" || in.Version == rel.Version {
		writeErr(w, http.StatusConflict, "nema novijeg izdanja u ovoj grani")
		return
	}
	pkgs := worldPackages()
	if len(pkgs) == 0 {
		writeErr(w, http.StatusConflict, "popis paketa uređaja nije čitljiv")
		return
	}
	// Veličina root particije se traži unaprijed. Bez toga slika nosi zadanih
	// ~104 MB i nadogradnja svaki put vrati particiju na tu veličinu — a
	// naknadno širenje na uređaju koji radi je opasno i zna ga oboriti.
	ds := s.diskState()
	rootfsMB := in.RootfsMB
	if rootfsMB == 0 {
		rootfsMB = ds.RecommendMB
	}
	if rootfsMB < 0 || rootfsMB > asuMaxRootfsMB {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf(
			"veličina root particije mora biti između 1 i %d MB (granica servisa za izgradnju)",
			asuMaxRootfsMB))
		return
	}
	if ds.FSUsed > 0 && int64(rootfsMB)<<20 < ds.FSUsed+(rootfsMinFreeMB<<20) {
		writeErr(w, http.StatusConflict, fmt.Sprintf(
			"tražena root particija (%d MB) je manja od onoga što je već zauzeto (%d MB) "+
				"uvećanog za rezervu — uređaj se poslije nadogradnje ne bi digao",
			rootfsMB, int(ds.FSUsed>>20)))
		return
	}
	breq := map[string]any{
		"target":   rel.Target,
		"profile":  "generic",
		"version":  in.Version,
		"packages": pkgs,
	}
	// parametar ima smisla samo tamo gdje slika nosi tablicu particija
	if rootfsMB > 0 && rel.RootFS != "" && ds.PartBytes > 0 {
		breq["rootfs_size_mb"] = rootfsMB
	}
	body, _ := json.Marshal(breq)
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost,
		asuBase+"/api/v1/build", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	cl := &http.Client{Timeout: 60 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "servis za izgradnju: "+err.Error())
		return
	}
	defer resp.Body.Close()
	var ar asuResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&ar); err != nil {
		writeErr(w, http.StatusBadGateway, "neočekivan odgovor servisa: "+err.Error())
		return
	}
	switch resp.StatusCode {
	case http.StatusOK:
		im, err := pickImage(ar.Images, rel.RootFS, bootMode())
		if err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"state":     "ready",
			"version":   in.Version,
			"image":     im.Name,
			"sha256":    im.SHA256,
			"rootfs_mb": rootfsMB,
			"url":       asuBase + "/store/" + ar.BinDir + "/" + im.Name,
		})
	case http.StatusAccepted, http.StatusCreated:
		st := ar.Detail
		if ar.BuilderState != "" {
			st = ar.Detail + " · " + ar.BuilderState
		}
		if ar.Detail == "queued" && ar.QueuePos > 0 {
			st = fmt.Sprintf("u redu, %d ispred", ar.QueuePos)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"state":     "building",
			"version":   in.Version,
			"status":    st,
			"rootfs_mb": rootfsMB,
			"hash":      ar.RequestHash,
		})
	default:
		msg := ar.Detail
		if msg == "" {
			msg = "HTTP " + resp.Status
		}
		writeErr(w, http.StatusBadGateway, "servis za izgradnju: "+msg)
	}
}

// handleOpenWrtFetch preuzima izgrađenu sliku na uređaj i provjerava otisak.
func (s *server) handleOpenWrtFetch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL      string `json:"url"`
		SHA256   string `json:"sha256"`
		Version  string `json:"version"`
		Image    string `json:"image"`
		RootfsMB int    `json:"rootfs_mb"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !strings.HasPrefix(in.URL, asuBase+"/") || in.SHA256 == "" {
		writeErr(w, http.StatusBadRequest,
			"slika se preuzima samo sa službenog servisa i uz otisak")
		return
	}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, in.URL, nil)
	cl := &http.Client{Timeout: 10 * time.Minute}
	resp, err := cl.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeErr(w, http.StatusBadGateway, "preuzimanje: HTTP "+resp.Status)
		return
	}
	f, err := os.OpenFile(owImagePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, owMaxImage))
	f.Close()
	if err != nil {
		os.Remove(owImagePath)
		writeErr(w, http.StatusBadGateway, "prekinuto preuzimanje: "+err.Error())
		return
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, in.SHA256) {
		os.Remove(owImagePath)
		writeErr(w, http.StatusConflict,
			"otisak preuzete slike ne odgovara ("+got+") — nadogradnja se ne nastavlja")
		return
	}
	m := owMeta{Version: in.Version, Image: in.Image, SHA256: got, Source: "asu",
		Size: n, At: time.Now().Format(time.RFC3339), RootfsMB: in.RootfsMB}
	b, _ := json.Marshal(m)
	_ = os.WriteFile(s.owMetaFile(), b, 0o600)
	writeJSON(w, http.StatusOK, map[string]any{"staged": true, "size_bytes": n, "sha256": got})
}

// handleOpenWrtUpload prima sliku s računala (za uređaje bez pristupa internetu
// ili kad se koristi vlastita gradnja).
func (s *server) handleOpenWrtUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, owMaxImage)
	head := make([]byte, 2)
	if _, err := io.ReadFull(r.Body, head); err != nil ||
		head[0] != 0x1f || head[1] != 0x8b {
		writeErr(w, http.StatusBadRequest, "slika mora biti .img.gz")
		return
	}
	f, err := os.OpenFile(owImagePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h),
		io.MultiReader(bytes.NewReader(head), r.Body))
	f.Close()
	if err != nil {
		os.Remove(owImagePath)
		writeErr(w, http.StatusBadRequest, "prekinut prijenos: "+err.Error())
		return
	}
	sum := hex.EncodeToString(h.Sum(nil))
	m := owMeta{Image: filepath.Base(r.Header.Get("X-Filename")), SHA256: sum,
		Source: "upload", Size: n, At: time.Now().Format(time.RFC3339)}
	b, _ := json.Marshal(m)
	_ = os.WriteFile(s.owMetaFile(), b, 0o600)
	writeJSON(w, http.StatusCreated, map[string]any{
		"staged": true, "size_bytes": n, "sha256": sum,
	})
}

/* ---------- sama nadogradnja ---------- */

func (s *server) handleOpenWrtFlash(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Confirm string `json:"confirm"`
		// AcceptRootfs je svjestan pristanak na sliku za koju se ne zna
		// veličina root particije ili je manja od sadašnje
		AcceptRootfs bool `json:"accept_rootfs"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	ctx := r.Context()
	rel, err := s.owRelease(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if strings.TrimSpace(in.Confirm) != rel.Hostname {
		writeErr(w, http.StatusBadRequest,
			"za potvrdu upiši ime uređaja: "+rel.Hostname)
		return
	}
	if _, err := os.Stat(owImagePath); err != nil {
		writeErr(w, http.StatusConflict, "nema pripremljene slike")
		return
	}
	// otisak se provjerava ponovno, neposredno prije upisa
	mb, err := os.ReadFile(s.owMetaFile())
	if err != nil {
		writeErr(w, http.StatusConflict, "nema podataka o pripremljenoj slici")
		return
	}
	var m owMeta
	if err := json.Unmarshal(mb, &m); err != nil || m.SHA256 == "" {
		writeErr(w, http.StatusConflict, "podaci o slici nisu čitljivi")
		return
	}
	f, err := os.Open(owImagePath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h := sha256.New()
	_, _ = io.Copy(h, f)
	f.Close()
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, m.SHA256) {
		writeErr(w, http.StatusConflict, "otisak slike se promijenio — nadogradnja se prekida")
		return
	}
	// Provjera root particije — jedina stvar koju nadogradnja x86 slike tiho
	// pokvari. Slika nosi svoju tablicu particija; ako je root u njoj manji
	// nego što treba, uređaj se poslije dizanja napuni do vrha.
	ds := s.diskState()
	switch {
	case m.RootfsMB == 0 && ds.PartBytes > 0 && !in.AcceptRootfs:
		writeErr(w, http.StatusConflict,
			"za ovu sliku se ne zna koliku root particiju nosi (slika je učitana s računala). "+
				"Nadogradnja može vratiti root particiju na zadanih ~104 MB. "+
				"Potvrdi kvačicom da nastavljaš svjesno.")
		return
	case m.RootfsMB > 0 && int64(m.RootfsMB)<<20 < ds.FSUsed+(rootfsMinFreeMB<<20) && !in.AcceptRootfs:
		writeErr(w, http.StatusConflict, fmt.Sprintf(
			"slika nosi root particiju od %d MB, a već je zauzeto %d MB — "+
				"pripremi sliku s većom root particijom (preporuka %d MB)",
			m.RootfsMB, int(ds.FSUsed>>20), ds.RecommendMB))
		return
	}
	// stanje prije nadogradnje se pamti da se poslije dizanja može reći je li
	// se particija smanjila (etc direktorij preživi nadogradnju)
	if ds.PartBytes > 0 {
		db, _ := json.Marshal(diskBefore{PartBytes: ds.PartBytes, FSUsed: ds.FSUsed,
			Version: rel.Version, At: time.Now().Format(time.RFC3339)})
		_ = os.WriteFile(s.diskBeforeFile(), db, 0o600)
	}

	// svjež puni backup je uvjet, ne preporuka
	name, _, err := s.createFullBackup(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup prije nadogradnje: "+err.Error())
		return
	}
	// popis paketa preživi nadogradnju (etc je u keep listi)
	_ = os.WriteFile(s.owPackagesFile(),
		[]byte(strings.Join(worldPackages(), "\n")+"\n"), 0o644)
	s.alert("reboot", "warn", "Nadogradnja OpenWrt-a na "+m.Version+
		" — uređaj se ponovno pokreće (backup "+name+")")

	// sysupgrade ubija procese, uključujući ovaj — zato ide odvojeno od HTTP
	// zahtjeva, s odgodom, da preglednik prvo dobije odgovor. Vanjski shell
	// odmah završi, a pozadinski posao preuzme init.
	script := fmt.Sprintf("(sleep 3; /sbin/sysupgrade -v %s >>%s 2>&1) &",
		owImagePath, s.owLogFile())
	if err := exec.Command("/bin/sh", "-c", script).Run(); err != nil {
		writeErr(w, http.StatusInternalServerError, "pokretanje nadogradnje: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"started": true,
		"version": m.Version,
		"backup":  name,
		"note": "Uređaj se ponovno pokreće. Sučelje se javi samo kad se digne — " +
			"obično za 1–3 minute.",
	})
}

// handleOpenWrtPackages uspoređuje popis paketa s onim prije nadogradnje.
func (s *server) handleOpenWrtPackages(w http.ResponseWriter, r *http.Request) {
	saved, err := os.ReadFile(s.owPackagesFile())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"checked": false})
		return
	}
	have := map[string]bool{}
	for _, p := range worldPackages() {
		have[p] = true
	}
	missing := []string{}
	for _, ln := range strings.Split(string(saved), "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" && !have[ln] {
			missing = append(missing, ln)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"checked": true, "missing": missing, "before": strings.Count(string(saved), "\n"),
	})
}

// handleOpenWrtPackagesRestore doinstalira pakete koji su nestali nadogradnjom.
func (s *server) handleOpenWrtPackagesRestore(w http.ResponseWriter, r *http.Request) {
	saved, err := os.ReadFile(s.owPackagesFile())
	if err != nil {
		writeErr(w, http.StatusConflict, "nema spremljenog popisa paketa")
		return
	}
	have := map[string]bool{}
	for _, p := range worldPackages() {
		have[p] = true
	}
	missing := []string{}
	for _, ln := range strings.Split(string(saved), "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" && !have[ln] && reePkg.MatchString(ln) {
			missing = append(missing, ln)
		}
	}
	if len(missing) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"installed": []string{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	_ = exec.CommandContext(ctx, "apk", "update").Run()
	args := append([]string{"add"}, missing...)
	out, err := exec.CommandContext(ctx, "apk", args...).CombinedOutput()
	if err != nil {
		writeErr(w, http.StatusInternalServerError,
			"doinstalacija: "+err.Error()+": "+string(out))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"installed": missing})
}
