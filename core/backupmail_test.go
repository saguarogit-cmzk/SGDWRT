package main

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Poruka s privitkom se ne može provjeriti na uređaju bez stvarnog poštanskog
// računa, pa se provjerava ovdje: sastavi se i raspakira natrag istim putem
// kojim to radi i program za poštu.

func TestBuildMailWithAttachment(t *testing.T) {
	payload := []byte("ovo je\x00binarni\xff sadržaj arhive")
	msg, err := buildMailWithAttachment("uredaj@primjer.hr",
		[]string{"goran@primjer.hr", "drugi@primjer.hr"},
		"Saguaro (Lab Zagreb) — sigurnosna kopija full-20260804.tar.gz",
		"Tijelo poruke.\nDrugi redak.\n",
		"full-20260804.tar.gz.enc", payload)
	if err != nil {
		t.Fatalf("sastavljanje: %v", err)
	}

	m, err := mail.ReadMessage(bytes.NewReader(msg))
	if err != nil {
		t.Fatalf("zaglavlje se ne da pročitati: %v", err)
	}
	if got := m.Header.Get("To"); got != "goran@primjer.hr, drugi@primjer.hr" {
		t.Errorf("primatelji = %q", got)
	}
	// naslov ima hrvatske znakove — mora biti kodiran, inače stiže izlomljen
	subj, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil || !strings.Contains(subj, "sigurnosna kopija") {
		t.Errorf("naslov = %q (%v)", m.Header.Get("Subject"), err)
	}

	mt, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/mixed" {
		t.Fatalf("vrsta poruke = %q (%v)", mt, err)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])

	body, err := mr.NextPart()
	if err != nil {
		t.Fatalf("prvi dio: %v", err)
	}
	txt, _ := io.ReadAll(body)
	if !strings.Contains(string(txt), "Tijelo poruke.") {
		t.Errorf("tijelo poruke nije na mjestu: %q", txt)
	}

	att, err := mr.NextPart()
	if err != nil {
		t.Fatalf("privitak: %v", err)
	}
	if got := att.FileName(); got != "full-20260804.tar.gz.enc" {
		t.Errorf("naziv privitka = %q", got)
	}
	if got := att.Header.Get("Content-Transfer-Encoding"); got != "base64" {
		t.Errorf("kodiranje privitka = %q", got)
	}
	// multipart sam ne dekodira base64 (samo quoted-printable), pa ručno
	got, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, att))
	if err != nil {
		t.Fatalf("čitanje privitka: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("privitak se ne poklapa: %q != %q", got, payload)
	}
	if _, err := mr.NextPart(); err != io.EOF {
		t.Errorf("poruka ima višak dijelova")
	}
}

// Arhiva mora izaći s uređaja šifrirana i vratiti se ista natrag; lozinka se
// pritom ne smije naći nigdje u poruci.
func TestBackupMailEncryptRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "arhiva.tar.gz")
	plain := bytes.Repeat([]byte("saguaro"), 3000)
	if err := os.WriteFile(src, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	const pass = "TajnaLozinkaArhive123"
	enc := filepath.Join(dir, "arhiva.enc")
	if err := encryptFile(src, enc, pass); err != nil {
		t.Fatalf("šifriranje: %v", err)
	}
	data, err := os.ReadFile(enc)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, plain[:64]) {
		t.Fatal("šifrirana datoteka sadrži izvorni sadržaj")
	}

	msg, err := buildMailWithAttachment("a@b.c", []string{"d@e.f"},
		"kopija", "Lozinka NIJE u ovoj poruci.\n", "arhiva.enc", data)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(msg, []byte(pass)) {
		t.Fatal("lozinka za dešifriranje se našla u poruci")
	}

	back := filepath.Join(dir, "vraceno.tar.gz")
	if err := decryptFile(enc, back, pass); err != nil {
		t.Fatalf("dešifriranje: %v", err)
	}
	got, err := os.ReadFile(back)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Error("vraćena arhiva nije jednaka izvornoj")
	}
	if decryptFile(enc, back, "krivaLozinka") == nil {
		t.Error("kriva lozinka je prošla")
	}
}
