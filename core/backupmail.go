package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Slanje sigurnosne kopije e-mailom.
//
// Backup koji leži samo na uređaju nije backup. Slanje na vlastiti poslužitelj
// već postoji, ali traži poslužitelj — a najčešći slučaj je da ga nema.
// Sandučić e-pošte je tada jedina kopija izvan uređaja, i za arhivu od ~80 KB
// je sasvim primjeren.
//
// Arhiva sadrži privatne ključeve VPN-a, certifikate, API token i otiske
// lozinki. Zato se **uvijek** šalje šifrirana (isti postupak kao za slanje na
// poslužitelj: PBKDF2-SHA256 → AES-256-GCM), a lozinka za otvaranje nikad ne
// ide u toj poruci — inače bi šifriranje bilo ukras.

// mailBackupMaxBytes je granica veličine šifrirane arhive. Base64 privitak
// naraste za trećinu, a poslužitelji uglavnom odbijaju poruke preko 25 MB.
const mailBackupMaxBytes = 15 << 20

// Učestalost slanja. Arhiva se pravi svaku noć, ali ne treba svaku noć i u
// sandučić — inače se poruke gomilaju i prestanu se gledati. Razmaci su malo
// kraći od punog razdoblja jer backup ide uvijek u isti sat: da razlika od
// nekoliko minuta ne preskoči cijeli tjedan.
var mailFreqs = []struct {
	ID    string
	Label string
	Every time.Duration
}{
	{"always", "uz svaki backup", 0},
	{"daily", "jednom dnevno", 20 * time.Hour},
	{"weekly", "jednom tjedno", (6*24 + 20) * time.Hour},
	{"monthly", "jednom mjesečno", 27 * 24 * time.Hour},
}

func mailFreq(id string) (string, time.Duration, bool) {
	for _, f := range mailFreqs {
		if f.ID == id {
			return f.Label, f.Every, true
		}
	}
	return "", 0, false
}

// backupMailDue javlja smije li automatsko slanje proći. Ručno slanje gumbom
// ovo ne pita — ako čovjek klikne, poruka ide.
func (s *server) backupMailDue() (bool, string) {
	id := s.getSetting("backup_mail_freq", "weekly")
	label, every, ok := mailFreq(id)
	if !ok || every == 0 {
		return true, ""
	}
	last := s.getSetting("backup_mail_last_ok", "")
	if last == "" {
		return true, ""
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", last, time.Local)
	if err != nil {
		return true, ""
	}
	if time.Since(t) >= every {
		return true, ""
	}
	return false, label
}

// backupMailTargets vraća primatelje: zasebni popis ako je upisan, inače isti
// kao za obavijesti.
func (s *server) backupMailTargets() []string {
	to := strings.Fields(s.getSetting("backup_mail_to", ""))
	if len(to) == 0 {
		to = strings.Fields(s.getSetting("smtp_to", ""))
	}
	return to
}

// sendBackupMail šifrira arhivu i pošalje je kao privitak.
func (s *server) sendBackupMail(ctx context.Context, archive string) error {
	if s.getSetting("smtp_host", "") == "" {
		return fmt.Errorf("SMTP poslužitelj nije postavljen (Nadzor → E-mail)")
	}
	to := s.backupMailTargets()
	if len(to) == 0 {
		return fmt.Errorf("nije upisan nijedan primatelj")
	}
	pass := s.getSetting("backup_pass", "")
	if pass == "" {
		return fmt.Errorf("nedostaje lozinka za šifriranje arhive — " +
			"nešifrirana kopija ne izlazi s uređaja")
	}
	src := filepath.Join(s.backupDir, archive)
	st, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("arhiva %s ne postoji", archive)
	}
	enc := filepath.Join(os.TempDir(), archive+".enc")
	if err := encryptFile(src, enc, pass); err != nil {
		return err
	}
	defer os.Remove(enc)
	data, err := os.ReadFile(enc)
	if err != nil {
		return err
	}
	if len(data) > mailBackupMaxBytes {
		return fmt.Errorf(
			"arhiva je prevelika za e-mail (%d MB, granica %d MB) — "+
				"koristi slanje na poslužitelj ili preuzmi ručno",
			len(data)>>20, mailBackupMaxBytes>>20)
	}

	label := s.getSetting("device_label", "")
	subject := "Saguaro"
	if label != "" {
		subject += " (" + label + ")"
	}
	subject += " — sigurnosna kopija " + archive

	body := "" +
		"U privitku je sigurnosna kopija konfiguracije uređaja.\n\n" +
		"Arhiva: " + archive + "\n" +
		"Veličina prije šifriranja: " + fmt.Sprintf("%d KB", st.Size()>>10) + "\n" +
		"Napravljena: " + st.ModTime().Format("02.01.2006. 15:04") + "\n\n" +
		"Privitak je šifriran (AES-256-GCM). Lozinka za otvaranje NIJE u ovoj\n" +
		"poruci i nikad se ne šalje istim putem — nalazi se u sučelju uređaja,\n" +
		"Backup → Lozinka arhive.\n\n" +
		"Otvaranje kopije:\n" +
		"  saguaro-core -decrypt-backup " + archive + ".enc -backup-pass 'lozinka'\n\n" +
		"Poruku je poslao Saguaro sam; na nju se ne odgovara.\n"

	msg, err := buildMailWithAttachment(
		s.getSetting("smtp_from", "saguaro@localhost"), to,
		subject, body, archive+".enc", data)
	if err != nil {
		return err
	}
	return s.smtpDeliver(to, msg)
}

// buildMailWithAttachment sastavlja MIME poruku s jednim privitkom.
func buildMailWithAttachment(from string, to []string, subject, body,
	filename string, data []byte) ([]byte, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	sep := "sag-" + hex.EncodeToString(raw)

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mimeHeader(subject))
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n", sep)

	fmt.Fprintf(&b, "--%s\r\n", sep)
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	b.WriteString("\r\n")

	fmt.Fprintf(&b, "--%s\r\n", sep)
	fmt.Fprintf(&b, "Content-Type: application/octet-stream; name=\"%s\"\r\n", filename)
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	fmt.Fprintf(&b, "Content-Disposition: attachment; filename=\"%s\"\r\n\r\n", filename)
	enc := base64.StdEncoding.EncodeToString(data)
	for i := 0; i < len(enc); i += 76 {
		j := i + 76
		if j > len(enc) {
			j = len(enc)
		}
		b.WriteString(enc[i:j])
		b.WriteString("\r\n")
	}
	fmt.Fprintf(&b, "--%s--\r\n", sep)
	return []byte(b.String()), nil
}

// mailBackupAfterCreate se zove nakon svake izrade backupa (i ručne i noćne).
// Ne smije srušiti izradu — arhiva na uređaju već postoji.
func (s *server) mailBackupAfterCreate(ctx context.Context, archive string) string {
	if s.getSetting("backup_mail_enabled", "0") != "1" {
		return "isključeno"
	}
	if due, label := s.backupMailDue(); !due {
		return "preskočeno (" + label + ")"
	}
	if err := s.sendBackupMail(ctx, archive); err != nil {
		s.setSetting("backup_mail_last_error", err.Error())
		s.alert("backup", "warning",
			"Backup je napravljen, ali slanje na e-mail nije uspjelo: "+err.Error())
		return "neuspjelo: " + err.Error()
	}
	s.setSetting("backup_mail_last_ok", time.Now().Format("2006-01-02 15:04:05"))
	s.setSetting("backup_mail_last_error", "")
	return "poslano"
}

/* ---------- API ---------- */

func (s *server) handleBackupMailGet(w http.ResponseWriter, r *http.Request) {
	freq := s.getSetting("backup_mail_freq", "weekly")
	label, _, ok := mailFreq(freq)
	if !ok {
		freq, label = "weekly", "jednom tjedno"
	}
	opts := []map[string]string{}
	for _, f := range mailFreqs {
		opts = append(opts, map[string]string{"id": f.ID, "label": f.Label})
	}
	due, _ := s.backupMailDue()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":     s.getSetting("backup_mail_enabled", "0") == "1",
		"to":          s.getSetting("backup_mail_to", ""),
		"targets":     s.backupMailTargets(),
		"freq":        freq,
		"freq_label":  label,
		"freqs":       opts,
		"due_now":     due,
		"smtp_ready":  s.getSetting("smtp_host", "") != "",
		"pass_set":    s.getSetting("backup_pass", "") != "",
		"last_ok":     s.getSetting("backup_mail_last_ok", ""),
		"last_error":  s.getSetting("backup_mail_last_error", ""),
		"max_mb":      mailBackupMaxBytes >> 20,
		"attach_note": "arhiva se uvijek šalje šifrirana; lozinka ne ide istom porukom",
	})
}

func (s *server) handleBackupMailSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled *bool  `json:"enabled"`
		To      string `json:"to"`
		Freq    string `json:"freq"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Freq != "" {
		if _, _, ok := mailFreq(in.Freq); !ok {
			writeErr(w, http.StatusBadRequest,
				"učestalost mora biti always, daily, weekly ili monthly")
			return
		}
		s.setSetting("backup_mail_freq", in.Freq)
	}
	if in.Enabled != nil {
		if *in.Enabled && s.getSetting("smtp_host", "") == "" {
			writeErr(w, http.StatusConflict,
				"prvo postavi SMTP poslužitelj (Nadzor → E-mail)")
			return
		}
		if *in.Enabled && s.getSetting("backup_pass", "") == "" {
			writeErr(w, http.StatusConflict,
				"prvo postavi lozinku arhive — nešifrirana kopija ne izlazi s uređaja")
			return
		}
		s.setSetting("backup_mail_enabled", boolSetting(*in.Enabled))
	}
	// prazan popis znači "isti primatelji kao za obavijesti"
	s.setSetting("backup_mail_to", strings.Join(strings.Fields(in.To), " "))
	s.handleBackupMailGet(w, r)
}

// handleBackupMailSend šalje odabranu arhivu odmah; bez naziva ide zadnja.
func (s *server) handleBackupMailSend(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	_ = decodeBody(w, r, &in)

	name := s.safeBackupName(in.Name)
	if name == "" {
		entries, err := os.ReadDir(s.backupDir)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, e := range entries {
			if isArchiveName(e.Name()) && e.Name() > name {
				name = e.Name()
			}
		}
	}
	if name == "" {
		writeErr(w, http.StatusBadRequest,
			"nema nijedne arhive — prvo napravi puni backup")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	if err := s.sendBackupMail(ctx, name); err != nil {
		s.setSetting("backup_mail_last_error", err.Error())
		writeErr(w, http.StatusBadGateway, "slanje nije uspjelo: "+err.Error())
		return
	}
	s.setSetting("backup_mail_last_ok", time.Now().Format("2006-01-02 15:04:05"))
	s.setSetting("backup_mail_last_error", "")
	writeJSON(w, http.StatusOK, map[string]any{
		"sent": name, "to": s.backupMailTargets(),
	})
}
