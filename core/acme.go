package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Certifikati za obrnuti proxy (Let's Encrypt).
//
// Kad uređaj sam vodi certifikat, on otvara TLS vezu umjesto internog servera
// — interni server tada može ostati na običnom HTTP-u, a certifikati se vode
// na jednom mjestu i sami se obnavljaju.
//
// Izdavanje ide preko paketa acme (acme.sh) i HTTP-01 provjere: Let's Encrypt
// dolazi na port 80 imena koje se traži. Taj promet vatrozid već preusmjerava
// na proxy, pa proxy putanju /.well-known/acme-challenge/ šalje malom
// poslužitelju unutar Saguara koji poslužuje samo tu mapu.

const acmeCertDir = "/etc/ssl/acme" // ovdje paket ostavlja certifikate
const acmeSecPrefix = "sag_"        // naši zapisi u toj konfiguraciji
const acmeChallengePort = 8081      // sluša samo na 127.0.0.1
const acmeChallengePath = "/.well-known/acme-challenge/"

// Mapu s odgovorima na provjeru određuje sam paket acme i od verzije 1.5
// opcija "webroot" je zastarjela — očekuje se da web poslužitelj poslužuje
// upravo ovu putanju. Zato je uzimamo takvu kakva jest.
const acmeWebroot = "/var/run/acme/challenge"

func (s *server) proxyCertDir() string { return filepath.Join(s.etcDir, "proxy", "certs") }

func acmeInstalled() bool {
	_, err := os.Stat("/usr/lib/acme/client/acme.sh")
	return err == nil
}

// startChallengeServer poslužuje mapu s odgovorima na provjeru. Sluša samo na
// petlji: izvana se do njega dolazi kroz proxy, koji propušta isključivo
// putanju provjere.
func (s *server) startChallengeServer() {
	dir := filepath.Join(acmeWebroot, ".well-known", "acme-challenge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	mux := http.NewServeMux()
	mux.Handle(acmeChallengePath, http.StripPrefix(acmeChallengePath,
		http.FileServer(http.Dir(dir))))
	srv := &http.Server{
		Addr:         fmt.Sprintf("127.0.0.1:%d", acmeChallengePort),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	go func() { _ = srv.ListenAndServe() }()
}

/* ---------- konfiguracija paketa acme ---------- */

// writeAcmeConfig upisuje po jedan cert zapis za svaku stranicu koja traži
// certifikat na uređaju. Diraju se samo zapisi s našim prefiksom.
func (s *server) writeAcmeConfig(ctx context.Context, sites []RPSite) error {
	cfg, err := uciGetConfig(ctx, "acme")
	if err != nil {
		return err
	}
	email := s.getSetting("acme_email", "")
	var b strings.Builder
	for name := range cfg {
		if strings.HasPrefix(name, acmeSecPrefix) {
			fmt.Fprintf(&b, "delete acme.%s\n", name)
		}
	}
	// zajednički zapis: e-mail računa (Let's Encrypt ga koristi za obavijesti
	// o isteku i o promjenama uvjeta)
	if email != "" {
		for name, sec := range cfg {
			if sectStr(sec, ".type") == "acme" {
				fmt.Fprintf(&b, "set acme.%s.account_email=%s\n", name, uciQuote(email))
			}
		}
	}
	n := 0
	for _, st := range sites {
		if !st.Enabled || st.TLSMode != "acme" {
			continue
		}
		sn := acmeSecPrefix + rpID(st.UUID)
		fmt.Fprintf(&b, "set acme.%s=cert\n", sn)
		fmt.Fprintf(&b, "set acme.%s.enabled=1\n", sn)
		fmt.Fprintf(&b, "add_list acme.%s.domains=%s\n", sn, uciQuote(st.Hostname))
		// opcija "webroot" je namjerno izostavljena — paket je proglasio
		// zastarjelom i sam poslužuje iz acmeWebroot putanje
		fmt.Fprintf(&b, "set acme.%s.validation_method=webroot\n", sn)
		fmt.Fprintf(&b, "set acme.%s.key_type=ec256\n", sn)
		staging := "0"
		if st.AcmeStaging {
			staging = "1"
		}
		fmt.Fprintf(&b, "set acme.%s.staging=%s\n", sn, staging)
		n++
	}
	if b.Len() == 0 {
		return nil
	}
	b.WriteString("commit acme\n")
	return uciBatch(ctx, b.String())
}

// linkCerts povezuje izdane certifikate u mapu koju čita proxy. Namjerno su
// to poveznice, a ne kopije: kad se certifikat obnovi, proxy pri ponovnom
// učitavanju odmah vidi novi sadržaj.
func (s *server) linkCerts(sites []RPSite) (int, error) {
	dir := s.proxyCertDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	// počisti stare poveznice — stranica je možda obrisana
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
	n := 0
	for _, st := range sites {
		if !st.Enabled || st.TLSMode != "acme" {
			continue
		}
		src := filepath.Join(acmeCertDir, st.Hostname+".combined.crt")
		if _, err := os.Stat(src); err != nil {
			continue // certifikat još nije izdan
		}
		if err := os.Symlink(src, filepath.Join(dir, st.Hostname+".pem")); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// certInfo čita rok valjanosti izdanog certifikata.
func certInfo(host string) map[string]any {
	out := map[string]any{"issued": false}
	b, err := os.ReadFile(filepath.Join(acmeCertDir, host+".crt"))
	if err != nil {
		return out
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return out
	}
	crt, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return out
	}
	out["issued"] = true
	out["not_after"] = crt.NotAfter.Format("2006-01-02")
	out["days_left"] = int(time.Until(crt.NotAfter).Hours() / 24)
	out["issuer"] = crt.Issuer.CommonName
	return out
}

/* ---------- API ---------- */

func (s *server) handleAcmeGet(w http.ResponseWriter, r *http.Request) {
	sites, err := s.rpSites()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	certs := map[string]any{}
	for _, st := range sites {
		if st.TLSMode == "acme" {
			certs[st.Hostname] = certInfo(st.Hostname)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installed": acmeInstalled(),
		"email":     s.getSetting("acme_email", ""),
		"certs":     certs,
		"webroot":   acmeWebroot,
	})
}

func (s *server) handleAcmeSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email string `json:"email"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.Email = strings.TrimSpace(in.Email)
	if in.Email != "" && (!strings.Contains(in.Email, "@") || hasCtrl(in.Email)) {
		writeErr(w, http.StatusBadRequest, "neispravna e-mail adresa")
		return
	}
	s.setSetting("acme_email", in.Email)
	writeJSON(w, http.StatusOK, map[string]any{"email": in.Email})
}

func (s *server) handleAcmeInstall(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !acmeInstalled() {
		_ = exec.CommandContext(ctx, "apk", "update").Run()
		out, err := exec.CommandContext(ctx, "apk", "add",
			"acme-acmesh", "acme-common").CombinedOutput()
		if err != nil {
			writeErr(w, http.StatusInternalServerError,
				"instalacija: "+err.Error()+": "+string(out))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"installed": acmeInstalled()})
}

// handleAcmeIssue traži certifikate za sve stranice koje ih trebaju.
// Izdavanje traje, pa se odgovor vraća s ispisom postupka.
func (s *server) handleAcmeIssue(w http.ResponseWriter, r *http.Request) {
	if !acmeInstalled() {
		writeErr(w, http.StatusConflict, "paket acme nije instaliran")
		return
	}
	if s.getSetting("acme_email", "") == "" {
		writeErr(w, http.StatusConflict,
			"prvo upiši e-mail adresu za Let's Encrypt račun")
		return
	}
	sites, err := s.rpSites()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	want := []string{}
	for _, st := range sites {
		if st.Enabled && st.TLSMode == "acme" {
			want = append(want, st.Hostname)
		}
	}
	if len(want) == 0 {
		writeErr(w, http.StatusConflict,
			"nijedna stranica ne traži certifikat na uređaju")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	if err := s.writeAcmeConfig(ctx, sites); err != nil {
		writeErr(w, http.StatusInternalServerError, "konfiguracija acme: "+err.Error())
		return
	}
	// "renew" pokreće izdavanje odmah (start samo uključi noćni posao), ali
	// radi u pozadini — zato se čeka na rezultat, s gornjom granicom
	if _, err := exec.CommandContext(ctx, "/etc/init.d/acme", "renew").
		CombinedOutput(); err != nil {
		writeErr(w, http.StatusInternalServerError, "pokretanje izdavanja: "+err.Error())
		return
	}
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		done := true
		for _, h := range want {
			if _, err := os.Stat(filepath.Join(acmeCertDir, h+".crt")); err != nil {
				done = false
			}
		}
		if done {
			break
		}
		if out, _ := exec.CommandContext(ctx, "pgrep", "-f", "acme.sh").Output(); len(out) == 0 {
			break // postupak je završio (uspješno ili ne)
		}
		time.Sleep(5 * time.Second)
	}
	// paket loguje kroz syslog, pa se ispis dohvaća odande
	log := ""
	lg, _ := exec.CommandContext(ctx, "logread", "-l", "80").Output()
	for _, ln := range strings.Split(string(lg), "\n") {
		if strings.Contains(ln, "acme") {
			log += ln + "\n"
		}
	}
	n, lerr := s.linkCerts(sites)
	if lerr != nil {
		writeErr(w, http.StatusInternalServerError, "povezivanje certifikata: "+lerr.Error())
		return
	}
	certs := map[string]any{}
	for _, h := range want {
		certs[h] = certInfo(h)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"requested": want,
		"linked":    n,
		"certs":     certs,
		"log":       strings.TrimSpace(log),
		"error":     err != nil,
	})
}
