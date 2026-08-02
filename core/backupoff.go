package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Backup izvan uređaja: arhiva se šifrira i pošalje na vanjski poslužitelj
// preko SCP-a. Backup koji leži samo na uređaju nije backup — kvar diska ili
// krađa uređaja znače gubitak svega. A arhiva sadrži privatne ključeve VPN-a,
// token i lozinke, pa nešifrirana ne smije napustiti uređaj.

const offsiteKeyFile = "offsite_key" // privatni SSH ključ za slanje
const offsiteMark = "# sag-offsite"  // oznaka cron retka
const encMagic = "SAGENC1\n"         // zaglavlje šifrirane arhive
const encSaltLen = 16
const encIter = 600000 // OWASP preporuka za PBKDF2-SHA256 (2023+)

/* ---------- šifriranje arhive ---------- */

// encryptFile šifrira datoteku lozinkom (PBKDF2-SHA256 → AES-256-GCM).
// Zapis: SAGENC1 | sol (16 B) | nonce (12 B) | šifrirani sadržaj.
func encryptFile(src, dst, passphrase string) error {
	plain, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	salt := make([]byte, encSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, encIter, 32)
	if err != nil {
		return err
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	out := []byte(encMagic)
	out = append(out, salt...)
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plain, []byte(encMagic))
	return os.WriteFile(dst, out, 0o600)
}

// decryptFile je obrnuti smjer; koristi ga zastavica -decrypt-backup, da se
// arhiva može otvoriti i na drugom uređaju ili računalu.
func decryptFile(src, dst, passphrase string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	head := len(encMagic) + encSaltLen + 12
	if len(raw) < head || string(raw[:len(encMagic)]) != encMagic {
		return fmt.Errorf("%s nije Saguaro šifrirana arhiva", filepath.Base(src))
	}
	salt := raw[len(encMagic) : len(encMagic)+encSaltLen]
	nonce := raw[len(encMagic)+encSaltLen : head]
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, encIter, 32)
	if err != nil {
		return err
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return err
	}
	plain, err := gcm.Open(nil, nonce, raw[head:], []byte(encMagic))
	if err != nil {
		return fmt.Errorf("dešifriranje nije uspjelo — pogrešna lozinka " +
			"ili oštećena arhiva")
	}
	return os.WriteFile(dst, plain, 0o600)
}

/* ---------- SSH ključ za slanje ---------- */

func (s *server) offsiteKeyPath() string {
	return filepath.Join(s.etcDir, offsiteKeyFile)
}

// ensureOffsiteKey stvara ključ za prijavu na odredišni poslužitelj ako ga
// još nema, i vraća javni dio u obliku za authorized_keys.
func (s *server) ensureOffsiteKey() (string, error) {
	path := s.offsiteKeyPath()
	if _, err := os.Stat(path); err != nil {
		// dropbearov scp traži OpenSSH format ključa; generiramo ga alatom
		// koji je na uređaju, jer Go ne piše OpenSSH privatni format
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		os.Remove(path)
		os.Remove(path + ".pub")
		cmd := exec.CommandContext(ctx, "dropbearkey", "-t", "ed25519", "-f", path)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("izrada ključa: %v: %s", err, out)
		}
		os.Chmod(path, 0o600)
	}
	return s.offsitePubKey()
}

func (s *server) offsitePubKey() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "dropbearkey", "-y",
		"-f", s.offsiteKeyPath()).Output()
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "ssh-") {
			return strings.TrimSpace(l), nil
		}
	}
	return "", fmt.Errorf("javni ključ nije pronađen")
}

/* ---------- postavke ---------- */

func (s *server) handleOffsiteGet(w http.ResponseWriter, r *http.Request) {
	pub := ""
	if _, err := os.Stat(s.offsiteKeyPath()); err == nil {
		pub, _ = s.offsitePubKey()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":     s.getSetting("offsite_enabled", "0") == "1",
		"host":        s.getSetting("offsite_host", ""),
		"port":        s.getSetting("offsite_port", "22"),
		"user":        s.getSetting("offsite_user", ""),
		"path":        s.getSetting("offsite_path", ""),
		"encrypt":     s.getSetting("offsite_encrypt", "1") == "1",
		"has_pass":    s.getSetting("backup_pass", "") != "",
		"public_key":  pub,
		"last_ok":     s.getSetting("offsite_last_ok", ""),
		"last_error":  s.getSetting("offsite_last_error", ""),
		"key_missing": pub == "",
	})
}

func (s *server) handleOffsiteSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled    *bool  `json:"enabled"`
		Host       string `json:"host"`
		Port       int    `json:"port"`
		User       string `json:"user"`
		Path       string `json:"path"`
		Encrypt    *bool  `json:"encrypt"`
		Passphrase string `json:"passphrase"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "nedostaje polje enabled")
		return
	}
	in.Host = strings.TrimSpace(in.Host)
	in.User = strings.TrimSpace(in.User)
	in.Path = strings.TrimSpace(in.Path)
	if in.Port == 0 {
		in.Port = 22
	}
	encrypt := in.Encrypt == nil || *in.Encrypt

	if *in.Enabled {
		if in.Host == "" || (net.ParseIP(in.Host) == nil && !validDNSName(in.Host)) {
			writeErr(w, http.StatusBadRequest,
				"odredište mora biti IP adresa ili ime poslužitelja")
			return
		}
		if !reOffsiteUser.MatchString(in.User) {
			writeErr(w, http.StatusBadRequest,
				"korisničko ime na poslužitelju: slova, brojke, - _ . (do 32 znaka)")
			return
		}
		if in.Path == "" || !strings.HasPrefix(in.Path, "/") || hasCtrl(in.Path) {
			writeErr(w, http.StatusBadRequest,
				"putanja na poslužitelju mora biti apsolutna, npr. /backup/saguaro")
			return
		}
		if in.Port < 1 || in.Port > 65535 {
			writeErr(w, http.StatusBadRequest, "neispravan port")
			return
		}
		if encrypt {
			pass := in.Passphrase
			if pass == "" {
				pass = s.getSetting("backup_pass", "")
			}
			if len([]rune(pass)) < 12 {
				writeErr(w, http.StatusBadRequest,
					"lozinka za šifriranje arhive mora imati bar 12 znakova — "+
						"bez nje se arhiva ne može otvoriti, pa je zapiši na sigurno")
				return
			}
		}
	}
	if in.Passphrase != "" {
		if hasCtrl(in.Passphrase) {
			writeErr(w, http.StatusBadRequest,
				"lozinka ne smije sadržavati prijelom retka")
			return
		}
		if err := s.setSetting("backup_pass", in.Passphrase); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	for k, v := range map[string]string{
		"offsite_enabled": boolSetting(*in.Enabled),
		"offsite_host":    in.Host,
		"offsite_port":    strconv.Itoa(in.Port),
		"offsite_user":    in.User,
		"offsite_path":    in.Path,
		"offsite_encrypt": boolSetting(encrypt),
	} {
		if err := s.setSetting(k, v); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	pub := ""
	if *in.Enabled {
		var err error
		if pub, err = s.ensureOffsiteKey(); err != nil {
			writeErr(w, http.StatusInternalServerError,
				"izrada SSH ključa: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"saved": true, "public_key": pub,
	})
}

/* ---------- slanje ---------- */

// sendOffsite šifrira (ako je traženo) i pošalje arhivu na vanjski poslužitelj.
func (s *server) sendOffsite(ctx context.Context, archive string) error {
	if s.getSetting("offsite_enabled", "0") != "1" {
		return nil
	}
	host := s.getSetting("offsite_host", "")
	user := s.getSetting("offsite_user", "")
	path := s.getSetting("offsite_path", "")
	port := s.getSetting("offsite_port", "22")
	if host == "" || user == "" || path == "" {
		return fmt.Errorf("odredište nije potpuno postavljeno")
	}

	src := filepath.Join(s.backupDir, archive)
	send := src
	if s.getSetting("offsite_encrypt", "1") == "1" {
		pass := s.getSetting("backup_pass", "")
		if pass == "" {
			return fmt.Errorf("nedostaje lozinka za šifriranje arhive")
		}
		enc := filepath.Join(os.TempDir(), archive+".enc")
		if err := encryptFile(src, enc, pass); err != nil {
			return err
		}
		defer os.Remove(enc)
		send = enc
	}

	helper, err := s.writeSSHHelper()
	if err != nil {
		return err
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	dst := user + "@" + host + ":" + strings.TrimSuffix(path, "/") + "/"
	cmd := exec.CommandContext(c, "scp", "-B", "-S", helper, "-P", port, send, dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// writeSSHHelper stvara omotač koji scp koristi umjesto izravnog poziva
// dbclienta. Potreban je zato što scp ne prima -i ni -y, a nama trebaju oba:
// vlastiti ključ i prihvaćanje ključa poslužitelja pri prvom spajanju
// (nakon toga promjena ključa poslužitelja prekida vezu, kao i inače).
func (s *server) writeSSHHelper() (string, error) {
	p := filepath.Join(s.etcDir, "offsite-ssh.sh")
	script := "#!/bin/sh\n" +
		"# Saguaro: prijenos backupa na vanjski poslužitelj\n" +
		"exec /usr/bin/dbclient -y -i " + s.offsiteKeyPath() + " \"$@\"\n"
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil {
		return "", err
	}
	return p, nil
}

// handleOffsiteTest pošalje zadnju arhivu odmah — provjera prije nego se
// oslonimo na noćni raspored.
func (s *server) handleOffsiteTest(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.backupDir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	latest := ""
	for _, e := range entries {
		if isArchiveName(e.Name()) && e.Name() > latest {
			latest = e.Name()
		}
	}
	if latest == "" {
		writeErr(w, http.StatusBadRequest,
			"nema nijedne arhive — prvo napravi puni backup")
		return
	}
	if err := s.sendOffsite(r.Context(), latest); err != nil {
		s.setSetting("offsite_last_error", err.Error())
		s.alert("backup", "warning", "Slanje backupa izvan uređaja nije uspjelo: "+
			err.Error())
		writeErr(w, http.StatusBadGateway, "slanje nije uspjelo: "+err.Error())
		return
	}
	s.setSetting("offsite_last_ok", time.Now().Format("2006-01-02 15:04:05"))
	s.setSetting("offsite_last_error", "")
	writeJSON(w, http.StatusOK, map[string]any{"sent": latest})
}

/* ---------- dešifriranje s naredbenog retka ---------- */

// runDecrypt otvara šifriranu arhivu; poziva se iz main-a zastavicom
// -decrypt-backup. Lozinka se čita iz -backup-pass ili iz baze.
func runDecrypt(src, out, pass string) error {
	if out == "" {
		out = strings.TrimSuffix(src, ".enc")
		if out == src {
			out = src + ".tar.gz"
		}
	}
	if err := decryptFile(src, out, pass); err != nil {
		return err
	}
	fmt.Println("Arhiva je dešifrirana u:", out)
	return nil
}

// reOffsiteUser ograničava korisničko ime na poslužitelju — ime ide u
// naredbeni redak scp-a, pa ne smije sadržavati ništa neočekivano.
var reOffsiteUser = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,32}$`)
