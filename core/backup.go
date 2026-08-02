package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Puni backup: OpenWrt konfiguracija (sysupgrade -b) + Saguaro baza + etc
// (token, TLS certifikat) u jednoj tar.gz arhivi u backup direktoriju.
const fullPrefix = "full-" // arhive izrađene na uređaju
const uploadPrefix = "up-" // arhive učitane kroz API
const fullKeep = 10        // rotacija punih arhiva
const restoreMaxBytes = 200 << 20

/* ---------- izrada ---------- */

// createFullBackup izradi punu arhivu; koristi je i Update modul prije nadogradnje.
func (s *server) createFullBackup(ctx context.Context) (string, int64, error) {
	ts := time.Now().Format("20060102-150405")
	tmp, err := os.MkdirTemp("", "sagbk-*")
	if err != nil {
		return "", 0, err
	}
	defer os.RemoveAll(tmp)

	// kanonski OpenWrt backup /etc datoteka
	etcTar := filepath.Join(tmp, "etc.tar.gz")
	if out, err := exec.CommandContext(ctx, "sysupgrade", "-b", etcTar).
		CombinedOutput(); err != nil {
		return "", 0, fmt.Errorf("sysupgrade -b: %v: %s", err, out)
	}

	// konzistentan snapshot žive SQLite baze
	dbSnap := filepath.Join(tmp, "saguaro.db")
	if _, err := s.db.Exec("VACUUM INTO '" +
		strings.ReplaceAll(dbSnap, "'", "''") + "'"); err != nil {
		return "", 0, fmt.Errorf("snapshot baze: %w", err)
	}

	host, _ := os.Hostname()
	manifest, _ := json.Marshal(map[string]any{
		"saguaro_backup":  true,
		"saguaro_version": version,
		"hostname":        host,
		"created_at":      ts,
	})

	name := fullPrefix + ts + ".tar.gz"
	outPath := filepath.Join(s.backupDir, name)
	if err := writeBackupArchive(outPath, manifest, etcTar, dbSnap, s.etcDir); err != nil {
		os.Remove(outPath)
		return "", 0, err
	}
	s.rotateBackups(fullPrefix, fullKeep)

	fi, err := os.Stat(outPath)
	if err != nil {
		return "", 0, err
	}
	return name, fi.Size(), nil
}

func (s *server) handleBackupCreate(w http.ResponseWriter, r *http.Request) {
	name, size, err := s.createFullBackup(r.Context())
	if err != nil {
		s.alert("backup", "warning", "Izrada backupa nije uspjela: "+err.Error())
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// slanje izvan uređaja ne smije srušiti izradu backupa — arhiva na
	// uređaju već postoji, pa se neuspjeh samo javi
	offsite := "isključeno"
	if s.getSetting("offsite_enabled", "0") == "1" {
		if err := s.sendOffsite(r.Context(), name); err != nil {
			s.setSetting("offsite_last_error", err.Error())
			s.alert("backup", "warning",
				"Backup je napravljen, ali slanje izvan uređaja nije uspjelo: "+
					err.Error())
			offsite = "neuspjelo: " + err.Error()
		} else {
			s.setSetting("offsite_last_ok", time.Now().Format("2006-01-02 15:04:05"))
			s.setSetting("offsite_last_error", "")
			offsite = "poslano"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"archive": name, "size_bytes": size, "offsite": offsite,
	})
}

/* ---------- raspored automatskog backupa (cron) ---------- */

const cronFile = "/etc/crontabs/root"
const cronMarker = "# sag-backup"

func (s *server) backupCronScript() string {
	return filepath.Join(s.etcDir, "backup-cron.sh")
}

func readBackupSchedule() (enabled bool, freq string) {
	b, err := os.ReadFile(cronFile)
	if err != nil {
		return false, "daily"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.Contains(line, cronMarker) {
			continue
		}
		if strings.HasPrefix(line, "0 3 * * 0") {
			return true, "weekly"
		}
		return true, "daily"
	}
	return false, "daily"
}

func (s *server) handleBackupScheduleGet(w http.ResponseWriter, r *http.Request) {
	enabled, freq := readBackupSchedule()
	writeJSON(w, http.StatusOK, map[string]any{"enabled": enabled, "freq": freq})
}

func (s *server) handleBackupScheduleSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled *bool  `json:"enabled"`
		Freq    string `json:"freq"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "nedostaje polje enabled")
		return
	}
	if in.Freq == "" {
		in.Freq = "daily"
	}
	if in.Freq != "daily" && in.Freq != "weekly" {
		writeErr(w, http.StatusBadRequest, "freq mora biti daily ili weekly")
		return
	}

	// pomoćna skripta zove vlastiti API (server je već autoritet za izradu)
	script := "#!/bin/sh\n" +
		"# Saguaro raspoređeni backup — poziva lokalni API\n" +
		"curl -sk -H \"Authorization: Bearer $(cat " +
		filepath.Join(s.etcDir, "token") + ")\" -X POST -d '{}' " +
		"https://127.0.0.1:8443/api/v1/backup/create >/dev/null 2>&1\n"
	if err := os.WriteFile(s.backupCronScript(), []byte(script), 0o700); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	old, _ := os.ReadFile(cronFile)
	var lines []string
	for _, l := range strings.Split(string(old), "\n") {
		if l != "" && !strings.Contains(l, cronMarker) {
			lines = append(lines, l)
		}
	}
	if *in.Enabled {
		spec := "0 3 * * *"
		if in.Freq == "weekly" {
			spec = "0 3 * * 0"
		}
		lines = append(lines, spec+" "+s.backupCronScript()+" "+cronMarker)
	}
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	if err := os.WriteFile(cronFile, []byte(content), 0o600); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := serviceReload(r.Context(), "cron", "restart"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": *in.Enabled, "freq": in.Freq,
	})
}

func writeBackupArchive(outPath string, manifest []byte,
	etcTar, dbSnap, saguaroEtc string) error {
	f, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	addBytes := func(name string, b []byte) error {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o600, Size: int64(len(b)),
			ModTime: time.Now(),
		}); err != nil {
			return err
		}
		_, err := tw.Write(b)
		return err
	}
	addFile := func(name, src string) error {
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		return addBytes(name, b)
	}

	if err := addBytes("manifest.json", manifest); err != nil {
		return err
	}
	if err := addFile("etc.tar.gz", etcTar); err != nil {
		return err
	}
	if err := addFile("saguaro.db", dbSnap); err != nil {
		return err
	}
	entries, err := os.ReadDir(saguaroEtc)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := addFile("saguaro-etc/"+e.Name(),
			filepath.Join(saguaroEtc, e.Name())); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// rotateBackups čuva zadnjih keep datoteka s danim prefiksom.
func (s *server) rotateBackups(prefix string, keep int) {
	entries, err := os.ReadDir(s.backupDir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		os.Remove(filepath.Join(s.backupDir, names[0]))
		names = names[1:]
	}
}

/* ---------- popis / preuzimanje / brisanje ---------- */

type backupInfo struct {
	Name       string `json:"name"`
	SizeBytes  int64  `json:"size_bytes"`
	ModifiedAt int64  `json:"modified_at"`
}

func isArchiveName(name string) bool {
	return strings.HasPrefix(name, fullPrefix) || strings.HasPrefix(name, uploadPrefix)
}

func (s *server) handleBackupList(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.backupDir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	archives := []backupInfo{}
	configs := []backupInfo{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		b := backupInfo{Name: e.Name(), SizeBytes: fi.Size(),
			ModifiedAt: fi.ModTime().Unix()}
		if isArchiveName(e.Name()) {
			archives = append(archives, b)
		} else {
			configs = append(configs, b)
		}
	}
	sort.Slice(archives, func(i, j int) bool {
		return archives[i].ModifiedAt > archives[j].ModifiedAt
	})
	sort.Slice(configs, func(i, j int) bool {
		return configs[i].ModifiedAt > configs[j].ModifiedAt
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"archives": archives, "config_backups": configs,
	})
}

// safeBackupName vraća ime datoteke u backup direktoriju ili "" ako je neispravno.
func (s *server) safeBackupName(name string) string {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return ""
	}
	fi, err := os.Stat(filepath.Join(s.backupDir, name))
	if err != nil || fi.IsDir() {
		return ""
	}
	return name
}

func (s *server) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	name := s.safeBackupName(r.PathValue("name"))
	if name == "" {
		writeErr(w, http.StatusNotFound, "backup ne postoji")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, filepath.Join(s.backupDir, name))
}

func (s *server) handleBackupDelete(w http.ResponseWriter, r *http.Request) {
	name := s.safeBackupName(r.PathValue("name"))
	if name == "" || !isArchiveName(name) {
		// automatski backupi konfiguracija rotiraju se sami i ne brišu se ručno
		writeErr(w, http.StatusNotFound, "arhiva ne postoji")
		return
	}
	if err := os.Remove(filepath.Join(s.backupDir, name)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": name})
}

/* ---------- učitavanje ---------- */

func (s *server) handleBackupUpload(w http.ResponseWriter, r *http.Request) {
	base := filepath.Base(r.URL.Query().Get("name"))
	var clean strings.Builder
	for _, c := range base {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9', c == '.', c == '-', c == '_':
			clean.WriteRune(c)
		}
	}
	base = strings.TrimSuffix(clean.String(), ".tar.gz")
	base = strings.TrimSuffix(base, ".tgz")
	if base == "" || base == "." {
		base = "arhiva"
	}
	name := uploadPrefix + time.Now().Format("20060102-150405") + "-" + base + ".tar.gz"

	r.Body = http.MaxBytesReader(w, r.Body, restoreMaxBytes)
	head := make([]byte, 2)
	if _, err := io.ReadFull(r.Body, head); err != nil ||
		head[0] != 0x1f || head[1] != 0x8b {
		writeErr(w, http.StatusBadRequest, "datoteka nije gzip arhiva")
		return
	}
	f, err := os.OpenFile(filepath.Join(s.backupDir, name),
		os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	n, err := io.Copy(f, io.MultiReader(strings.NewReader(string(head)), r.Body))
	f.Close()
	if err != nil {
		os.Remove(filepath.Join(s.backupDir, name))
		writeErr(w, http.StatusBadRequest, "prekinut prijenos: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"archive": name, "size_bytes": n,
	})
}

/* ---------- vraćanje ---------- */

func (s *server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	name := s.safeBackupName(in.Name)
	if name == "" || !isArchiveName(name) {
		writeErr(w, http.StatusNotFound, "arhiva ne postoji")
		return
	}

	tmp, err := os.MkdirTemp("", "sagrs-*")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.RemoveAll(tmp)

	if err := extractBackup(filepath.Join(s.backupDir, name), tmp); err != nil {
		writeErr(w, http.StatusBadRequest, "arhiva neispravna: "+err.Error())
		return
	}

	mb, err := os.ReadFile(filepath.Join(tmp, "manifest.json"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "arhiva nema manifest — nije Saguaro backup")
		return
	}
	var man struct {
		SaguaroBackup bool `json:"saguaro_backup"`
	}
	if json.Unmarshal(mb, &man) != nil || !man.SaguaroBackup {
		writeErr(w, http.StatusBadRequest, "arhiva nije Saguaro backup")
		return
	}

	// 1) baza — staged; preuzima je openDB pri sljedećem startu (nakon reboota)
	if b, err := os.ReadFile(filepath.Join(tmp, "saguaro.db")); err == nil {
		if err := os.WriteFile(filepath.Join(s.dataDir, "saguaro.db.restore"),
			b, 0o600); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	// 2) token i certifikat
	if entries, err := os.ReadDir(filepath.Join(tmp, "saguaro-etc")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			b, err := os.ReadFile(filepath.Join(tmp, "saguaro-etc", e.Name()))
			if err != nil {
				continue
			}
			os.WriteFile(filepath.Join(s.etcDir, e.Name()), b, 0o600)
		}
	}
	// 3) OpenWrt konfiguracija — kanonski restore, vrijedi tek nakon reboota
	etcTar := filepath.Join(tmp, "etc.tar.gz")
	if _, err := os.Stat(etcTar); err == nil {
		if out, err := exec.CommandContext(r.Context(), "sysupgrade", "-r", etcTar).
			CombinedOutput(); err != nil {
			writeErr(w, http.StatusInternalServerError,
				fmt.Sprintf("sysupgrade -r: %v: %s", err, out))
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"restored":  name,
		"reboot_in": 5,
	})
	go func() {
		time.Sleep(5 * time.Second)
		// reboot izravno — ubus system reboot ne vraća JSON pa ubusCall ne prolazi
		if err := exec.Command("reboot").Run(); err != nil {
			log.Printf("reboot: %v", err)
		}
	}()
}

// extractBackup raspakira arhivu uz allowlist imena i limit veličine.
func extractBackup(archive, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		ok := name == "manifest.json" || name == "etc.tar.gz" ||
			name == "saguaro.db" ||
			(strings.HasPrefix(name, "saguaro-etc/") &&
				!strings.Contains(strings.TrimPrefix(name, "saguaro-etc/"), "/"))
		if !ok || hdr.Typeflag != tar.TypeReg {
			continue
		}
		total += hdr.Size
		if total > restoreMaxBytes {
			return fmt.Errorf("arhiva prevelika")
		}
		p := filepath.Join(dst, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(tr, hdr.Size))
		out.Close()
		if err != nil {
			return err
		}
	}
}

// replaceCronLine zamjenjuje (ili miče) redak u crontabu prepoznat po oznaci.
// Tuđi se retci ne diraju — svaki Saguaro zadatak ima vlastitu oznaku.
func replaceCronLine(marker, line string) error {
	old, _ := os.ReadFile(cronFile)
	var lines []string
	for _, l := range strings.Split(string(old), "\n") {
		if l != "" && !strings.Contains(l, marker) {
			lines = append(lines, l)
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	if err := os.WriteFile(cronFile, []byte(content), 0o600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return serviceReload(ctx, "cron", "restart")
}

// keepListFile govori OpenWrt-ovom sysupgradeu koje Saguaro datoteke mora
// sačuvati. Bez toga bi se pri nadogradnji firmwarea (ili vraćanju backupa)
// izgubile postavke izvan /etc/config: očvršćivanje jezgre, token, certifikati
// i baza. Backup arhive i logovi se namjerno NE navode — bile bi kružne
// odnosno nepotrebno velike.
const keepListFile = "/lib/upgrade/keep.d/saguaro"

// zadani direktoriji; sluze i za provjeru radi li servis nad stvarnom
// instalacijom ili je rijec o testnoj instanci
const defaultEtcDir = "/opt/saguaro/etc"
const defaultDataDir = "/opt/saguaro/data"

func ensureKeepList(etcDir, dataDir string) error {
	body := "# Saguaro — datoteke koje moraju preživjeti nadogradnju firmwarea\n" +
		sysctlFile + "\n" +
		etcDir + "\n" +
		dataDir + "\n"
	if old, err := os.ReadFile(keepListFile); err == nil && string(old) == body {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(keepListFile), 0o755); err != nil {
		return err
	}
	return os.WriteFile(keepListFile, []byte(body), 0o644)
}
