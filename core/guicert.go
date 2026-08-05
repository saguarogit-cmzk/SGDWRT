package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Pravi certifikat za samo Saguaro sučelje (port 8443).
//
// Self-signed certifikat radi, ali svaki preglednik na njega urla i korisnik
// nauči klikati "prihvati rizik" — a to je navika koja jednog dana proguta i
// pravi napad. Ovdje se za sučelje traži Let's Encrypt certifikat, istom
// infrastrukturom koja već radi za obrnuti proxy (paket acme, HTTP-01).
//
// Certifikat se mijenja BEZ restarta servisa: poslužitelj certifikat uzima
// preko GetCertificate poziva iz spremišta koje se samo osvježi kad se
// datoteka promijeni. Noćna obnova (cron paketa acme) time prolazi
// neprimjetno. Ako ACME certifikata nema ili je neispravan, uvijek se pada
// natrag na self-signed — sučelje ne smije ostati bez TLS-a.

const guiCertFwPrefix = "sag_ac_"

/* ---------- spremište certifikata s vrućom zamjenom ---------- */

type certStore struct {
	mu       sync.RWMutex
	current  *tls.Certificate
	selfCert string // etc/cert.pem
	selfKey  string // etc/key.pem
	acmeHost func() string
	lastMod  time.Time
	usingAcme bool
}

func newCertStore(selfCert, selfKey string, acmeHost func() string) *certStore {
	cs := &certStore{selfCert: selfCert, selfKey: selfKey, acmeHost: acmeHost}
	cs.reload()
	return cs
}

// acmePath vraća putanju kombinirane datoteke (certifikat + ključ zajedno,
// kako je paket acme ostavlja) za trenutno postavljeno ime, ili prazno.
func (cs *certStore) acmePath() string {
	h := cs.acmeHost()
	if h == "" {
		return ""
	}
	return filepath.Join(acmeCertDir, h+".combined.crt")
}

// reload učita certifikat iznova: ACME ako postoji i valja, inače self-signed.
func (cs *certStore) reload() {
	var cert tls.Certificate
	var err error
	usingAcme := false
	var mod time.Time

	if p := cs.acmePath(); p != "" {
		if st, serr := os.Stat(p); serr == nil {
			// kombinirana datoteka sadrži i certifikat i ključ, pa se ista
			// predaje na oba mjesta — parser si iz nje uzme što ga zanima
			if b, rerr := os.ReadFile(p); rerr == nil {
				if c, perr := tls.X509KeyPair(b, b); perr == nil {
					cert, usingAcme, mod = c, true, st.ModTime()
				} else {
					log.Printf("certifikat sučelja: %s neispravan (%v) — ostaje self-signed", p, perr)
				}
			}
		}
	}
	if !usingAcme {
		cert, err = tls.LoadX509KeyPair(cs.selfCert, cs.selfKey)
		if err != nil {
			log.Printf("certifikat sučelja: self-signed nije čitljiv: %v", err)
			return
		}
		if st, serr := os.Stat(cs.selfCert); serr == nil {
			mod = st.ModTime()
		}
	}
	cs.mu.Lock()
	cs.current = &cert
	cs.usingAcme = usingAcme
	cs.lastMod = mod
	cs.mu.Unlock()
}

func (cs *certStore) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	if cs.current == nil {
		return nil, fmt.Errorf("certifikat nije učitan")
	}
	return cs.current, nil
}

// watch osvježava spremište kad se datoteka promijeni — noćna obnova
// certifikata tako prolazi bez restarta servisa.
func (cs *certStore) watch() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		p := cs.acmePath()
		cs.mu.RLock()
		using, last := cs.usingAcme, cs.lastMod
		cs.mu.RUnlock()
		switch {
		case p == "" && using:
			cs.reload() // ime maknuto — natrag na self-signed
		case p != "":
			if st, err := os.Stat(p); err == nil {
				if !using || st.ModTime().After(last) {
					cs.reload()
				}
			} else if using {
				cs.reload() // datoteka nestala
			}
		}
	}
}

// guiCerts postavlja main pri pokretanju; nil u -no-tls načinu.
var guiCerts *certStore

/* ---------- put za ACME provjeru kad proxy ne radi ---------- */

// guiCertFirewallSync drži port 80 prohodnim za Let's Encrypt provjeru.
// Ako obrnuti proxy radi, on već prima port 80 i prosljeđuje putanju provjere
// — tada naša pravila NE smiju postojati (dva preusmjerenja porta 80 bi se
// otimala i proxy bi gubio promet). Ako proxyja nema, upisuje se izravno
// preusmjerenje 80 → poslužitelj provjere.
func (s *server) guiCertFirewallSync(ctx context.Context) error {
	cfg, err := uciGetConfig(ctx, "firewall")
	if err != nil {
		return err
	}
	proxyActive := false
	for name := range cfg {
		if strings.HasPrefix(name, rpFwPrefix) {
			proxyActive = true
			break
		}
	}
	want := s.getSetting("gui_cert_host", "") != "" && !proxyActive

	have := false
	for name := range cfg {
		if strings.HasPrefix(name, guiCertFwPrefix) {
			have = true
			break
		}
	}
	if want == have {
		return nil
	}
	var b strings.Builder
	for name := range cfg {
		if strings.HasPrefix(name, guiCertFwPrefix) {
			fmt.Fprintf(&b, "delete firewall.%s\n", name)
		}
	}
	if want {
		sn := guiCertFwPrefix + "r80"
		fmt.Fprintf(&b, "set firewall.%s=redirect\n", sn)
		fmt.Fprintf(&b, "set firewall.%s.name=%s\n", sn,
			uciQuote("Let's Encrypt provjera (sučelje)"))
		fmt.Fprintf(&b, "set firewall.%s.src=wan\n", sn)
		fmt.Fprintf(&b, "set firewall.%s.proto=tcp\n", sn)
		fmt.Fprintf(&b, "set firewall.%s.src_dport=80\n", sn)
		fmt.Fprintf(&b, "set firewall.%s.dest_port=%d\n", sn, acmeChallengePort)
		fmt.Fprintf(&b, "set firewall.%s.target=DNAT\n", sn)
		an := guiCertFwPrefix + "a81"
		fmt.Fprintf(&b, "set firewall.%s=rule\n", an)
		fmt.Fprintf(&b, "set firewall.%s.name=%s\n", an,
			uciQuote("Let's Encrypt provjera ulaz"))
		fmt.Fprintf(&b, "set firewall.%s.src=wan\n", an)
		fmt.Fprintf(&b, "set firewall.%s.proto=tcp\n", an)
		fmt.Fprintf(&b, "set firewall.%s.dest_port=%d\n", an, acmeChallengePort)
		fmt.Fprintf(&b, "set firewall.%s.target=ACCEPT\n", an)
	}
	b.WriteString("commit firewall\n")
	if err := uciBatch(ctx, b.String()); err != nil {
		return err
	}
	return serviceReload(ctx, "firewall", "reload")
}

/* ---------- API ---------- */

func (s *server) guiCertState() map[string]any {
	host := s.getSetting("gui_cert_host", "")
	out := map[string]any{
		"host":      host,
		"staging":   s.getSetting("gui_cert_staging", "0") == "1",
		"installed": acmeInstalled(),
		"email":     s.getSetting("acme_email", ""),
	}
	if guiCerts != nil {
		guiCerts.mu.RLock()
		out["using_acme"] = guiCerts.usingAcme
		guiCerts.mu.RUnlock()
	}
	if host != "" {
		out["cert"] = certInfo(host)
	}
	return out
}

func (s *server) handleGuiCertGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.guiCertState())
}

func (s *server) handleGuiCertSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Host    string `json:"host"`
		Staging *bool  `json:"staging"`
		Email   string `json:"email"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.Host = strings.ToLower(strings.TrimSpace(in.Host))
	if in.Host != "" && !reHostname.MatchString(in.Host) {
		writeErr(w, http.StatusBadRequest,
			"ime mora biti puno DNS ime, npr. router.tvrtka.hr")
		return
	}
	if e := strings.TrimSpace(in.Email); e != "" {
		if !strings.Contains(e, "@") || hasCtrl(e) {
			writeErr(w, http.StatusBadRequest, "neispravna e-mail adresa")
			return
		}
		s.setSetting("acme_email", e)
	}
	s.setSetting("gui_cert_host", in.Host)
	if in.Staging != nil {
		s.setSetting("gui_cert_staging", boolSetting(*in.Staging))
	}
	if err := s.guiCertFirewallSync(r.Context()); err != nil {
		log.Printf("guicert firewall: %v", err)
	}
	// acme konfiguracija se odmah uskladi — inače bi nakon micanja imena
	// noćni cron zauvijek pokušavao izdati certifikat za staro ime
	if acmeInstalled() {
		if sites, err := s.rpSites(); err == nil {
			if err := s.writeAcmeConfig(r.Context(), sites); err != nil {
				log.Printf("guicert acme config: %v", err)
			}
		}
	}
	if guiCerts != nil {
		guiCerts.reload() // ime maknuto ili promijenjeno → odmah ispravan cert
	}
	writeJSON(w, http.StatusOK, s.guiCertState())
}

// handleGuiCertIssue traži certifikat za ime sučelja. Dijeli acme
// konfiguraciju s proxyjem — writeAcmeConfig upisuje i ovaj zapis.
func (s *server) handleGuiCertIssue(w http.ResponseWriter, r *http.Request) {
	host := s.getSetting("gui_cert_host", "")
	if host == "" {
		writeErr(w, http.StatusConflict, "prvo upiši DNS ime sučelja")
		return
	}
	if !acmeInstalled() {
		writeErr(w, http.StatusConflict, "paket acme nije instaliran — klikni Instaliraj")
		return
	}
	if s.getSetting("acme_email", "") == "" {
		writeErr(w, http.StatusConflict, "prvo upiši e-mail za Let's Encrypt račun")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	if err := s.guiCertFirewallSync(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "firewall: "+err.Error())
		return
	}
	sites, err := s.rpSites()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.writeAcmeConfig(ctx, sites); err != nil {
		writeErr(w, http.StatusInternalServerError, "konfiguracija acme: "+err.Error())
		return
	}
	if _, err := exec.CommandContext(ctx, "/etc/init.d/acme", "renew").
		CombinedOutput(); err != nil {
		writeErr(w, http.StatusInternalServerError, "pokretanje izdavanja: "+err.Error())
		return
	}
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(acmeCertDir, host+".combined.crt")); err == nil {
			break
		}
		if out, _ := exec.CommandContext(ctx, "pgrep", "-f", "acme.sh").Output(); len(out) == 0 {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if guiCerts != nil {
		guiCerts.reload()
	}
	// dnevnik paketa, da korisnik vidi ZAŠTO nije prošlo kad ne prođe
	lg, _ := exec.CommandContext(ctx, "logread", "-l", "80").Output()
	acmeLog := ""
	for _, ln := range strings.Split(string(lg), "\n") {
		if strings.Contains(ln, "acme") {
			acmeLog += ln + "\n"
		}
	}
	st := s.guiCertState()
	st["log"] = strings.TrimSpace(acmeLog)
	writeJSON(w, http.StatusOK, st)
}
