// saguaro-core — API servis Saguaro platforme na OpenWrt uređaju.
// Čita stanje sustava isključivo preko ubus-a (D-007) i servira REST API v1.
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const version = "0.1.0"

type server struct {
	token   string
	webDir  string
	started time.Time
}

func main() {
	listen := flag.String("listen", ":8443", "adresa:port za slušanje")
	etcDir := flag.String("etc", "/opt/saguaro/etc", "direktorij konfiguracije (token, certifikat)")
	webDir := flag.String("web", "/opt/saguaro/web", "direktorij statičkog weba")
	noTLS := flag.Bool("no-tls", false, "posluži bez TLS-a (samo za razvoj)")
	flag.Parse()

	if err := os.MkdirAll(*etcDir, 0o755); err != nil {
		log.Fatalf("etc direktorij: %v", err)
	}
	token, err := ensureToken(filepath.Join(*etcDir, "token"))
	if err != nil {
		log.Fatalf("token: %v", err)
	}

	s := &server{token: token, webDir: *webDir, started: time.Now()}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.Handle("GET /api/v1/system", s.auth(s.handleSystem))
	mux.Handle("GET /api/v1/system/status", s.auth(s.handleStatus))
	mux.Handle("GET /api/v1/storage", s.auth(s.handleStorage))
	mux.Handle("GET /api/v1/interfaces", s.auth(s.handleInterfaces))
	mux.HandleFunc("/", s.handleRoot)

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	if *noTLS {
		log.Printf("saguaro-core %s sluša na %s (BEZ TLS-a)", version, *listen)
		err = srv.ListenAndServe()
	} else {
		certFile, keyFile, cerr := ensureCert(*etcDir)
		if cerr != nil {
			log.Fatalf("certifikat: %v", cerr)
		}
		log.Printf("saguaro-core %s sluša na %s (TLS)", version, *listen)
		err = srv.ListenAndServeTLS(certFile, keyFile)
	}
	if err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// ensureToken učita API token ili ga generira pri prvom startu.
func ensureToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		t := strings.TrimSpace(string(b))
		if t != "" {
			return t, nil
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	t := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(t+"\n"), 0o600); err != nil {
		return "", err
	}
	log.Printf("generiran novi API token u %s", path)
	return t, nil
}

func (s *server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	})
}

func (s *server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if fi, err := os.Stat(filepath.Join(s.webDir, "index.html")); err == nil && !fi.IsDir() {
		http.FileServer(http.Dir(s.webDir)).ServeHTTP(w, r)
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><title>Saguaro</title>
<h1>Saguaro Infrastructure</h1><p>saguaro-core %s — API v1:</p>
<ul><li>GET /api/v1/health</li><li>GET /api/v1/system</li>
<li>GET /api/v1/system/status</li><li>GET /api/v1/storage</li>
<li>GET /api/v1/interfaces</li></ul>
<p>Svi endpointi osim /health traže <code>Authorization: Bearer &lt;token&gt;</code>.</p>`, version)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
