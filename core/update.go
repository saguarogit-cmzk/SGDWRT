package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Update modul: nadogradnja saguaro-core + web iz release paketa.
// Dva izvora: GitHub Releases (kad postoje) ili ručno učitan paket kroz GUI.
// Prije svake nadogradnje radi se puni backup; binary se mijenja rename-om
// (stari inode ostaje živ procesu koji radi), pa restart servisa.
const githubRepo = "saguarogit-cmzk/SGSWRT"
const updateMaxBytes = 100 << 20

func (s *server) stagedUpdatePath() string {
	return filepath.Join(s.dataDir, "update-staged.tar.gz")
}

/* ---------- status ---------- */

type ghRelease struct {
	TagName     string `json:"tag_name"`
	PublishedAt string `json:"published_at"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

func fetchLatestRelease() (*ghRelease, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET",
		"https://api.github.com/repos/"+githubRepo+"/releases/latest", nil)
	req.Header.Set("User-Agent", "saguaro-core/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, nil // još nema objavljenih izdanja
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API: HTTP %d", resp.StatusCode)
	}
	var rel ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func (s *server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"current": version}

	if fi, err := os.Stat(s.stagedUpdatePath()); err == nil {
		out["staged"] = map[string]any{
			"size_bytes": fi.Size(), "uploaded_at": fi.ModTime().Unix(),
		}
	}
	rel, err := fetchLatestRelease()
	if err != nil {
		out["github_error"] = err.Error()
	} else if rel == nil {
		out["latest"] = nil
	} else {
		latest := map[string]any{
			"tag": rel.TagName, "published_at": rel.PublishedAt,
		}
		for _, a := range rel.Assets {
			if strings.Contains(a.Name, "linux-amd64") {
				latest["asset"] = a.Name
				latest["size_bytes"] = a.Size
				break
			}
		}
		out["latest"] = latest
	}
	writeJSON(w, http.StatusOK, out)
}

/* ---------- učitavanje paketa ---------- */

func (s *server) handleUpdateUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, updateMaxBytes)
	head := make([]byte, 2)
	if _, err := io.ReadFull(r.Body, head); err != nil ||
		head[0] != 0x1f || head[1] != 0x8b {
		writeErr(w, http.StatusBadRequest, "datoteka nije gzip arhiva")
		return
	}
	f, err := os.OpenFile(s.stagedUpdatePath(),
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	n, err := io.Copy(f, io.MultiReader(strings.NewReader(string(head)), r.Body))
	f.Close()
	if err != nil {
		os.Remove(s.stagedUpdatePath())
		writeErr(w, http.StatusBadRequest, "prekinut prijenos: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"staged": true, "size_bytes": n})
}

/* ---------- primjena ---------- */

// extractUpdate raspakira paket uz allowlist (saguaro-core + web/*).
func extractUpdate(archive, dst string) error {
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
		ok := name == "saguaro-core" ||
			(strings.HasPrefix(name, "web/") && !strings.Contains(name, ".."))
		if !ok || hdr.Typeflag != tar.TypeReg {
			continue
		}
		total += hdr.Size
		if total > updateMaxBytes {
			return fmt.Errorf("paket prevelik")
		}
		p := filepath.Join(dst, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
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

func copyFile(src, dst string, mode os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, mode)
}

func (s *server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Source string `json:"source"` // staged | github
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Source != "staged" && in.Source != "github" {
		writeErr(w, http.StatusBadRequest, "source mora biti staged ili github")
		return
	}

	if in.Source == "github" {
		rel, err := fetchLatestRelease()
		if err != nil {
			writeErr(w, http.StatusBadGateway, "GitHub: "+err.Error())
			return
		}
		if rel == nil {
			writeErr(w, http.StatusNotFound, "još nema objavljenih izdanja")
			return
		}
		url := ""
		for _, a := range rel.Assets {
			if strings.Contains(a.Name, "linux-amd64") {
				url = a.URL
				break
			}
		}
		if url == "" {
			writeErr(w, http.StatusNotFound, "izdanje nema linux-amd64 paket")
			return
		}
		client := &http.Client{Timeout: 3 * time.Minute}
		resp, err := client.Get(url)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "preuzimanje: "+err.Error())
			return
		}
		defer resp.Body.Close()
		f, err := os.OpenFile(s.stagedUpdatePath(),
			os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		_, err = io.Copy(f, io.LimitReader(resp.Body, updateMaxBytes))
		f.Close()
		if err != nil {
			writeErr(w, http.StatusBadGateway, "preuzimanje: "+err.Error())
			return
		}
	}

	if _, err := os.Stat(s.stagedUpdatePath()); err != nil {
		writeErr(w, http.StatusNotFound, "nema učitanog paketa za nadogradnju")
		return
	}

	tmp, err := os.MkdirTemp("", "sagup-*")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.RemoveAll(tmp)
	if err := extractUpdate(s.stagedUpdatePath(), tmp); err != nil {
		writeErr(w, http.StatusBadRequest, "paket neispravan: "+err.Error())
		return
	}
	newBin := filepath.Join(tmp, "saguaro-core")
	magic := make([]byte, 4)
	if f, err := os.Open(newBin); err != nil {
		writeErr(w, http.StatusBadRequest, "paket ne sadrži saguaro-core")
		return
	} else {
		io.ReadFull(f, magic)
		f.Close()
	}
	if string(magic) != "\x7fELF" {
		writeErr(w, http.StatusBadRequest, "saguaro-core u paketu nije linux binarna datoteka")
		return
	}

	// backup prije nadogradnje — zadnja stavka R-plana
	backupName, _, err := s.createFullBackup(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup prije nadogradnje: "+err.Error())
		return
	}

	exe, err := os.Executable()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := copyFile(newBin, exe+".new", 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.Rename(exe+".new", exe); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// web datoteke preko postojećih
	webSrc := filepath.Join(tmp, "web")
	if entries, err := os.ReadDir(webSrc); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if err := copyFile(filepath.Join(webSrc, e.Name()),
				filepath.Join(s.webDir, e.Name()), 0o644); err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	os.Remove(s.stagedUpdatePath())

	writeJSON(w, http.StatusOK, map[string]any{
		"applied": true, "backup": backupName, "restart_in": 2,
	})
	go func() {
		time.Sleep(2 * time.Second)
		if err := exec.Command("/etc/init.d/saguaro-core", "restart").Run(); err != nil {
			log.Printf("restart nakon nadogradnje: %v", err)
		}
	}()
}
