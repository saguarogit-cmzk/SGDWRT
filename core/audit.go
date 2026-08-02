package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Trag promjena konfiguracije.
//
// Umjesto da svaki modul sam prijavljuje što je promijenio, Saguaro svake
// minute usporedi datoteke u /etc/config s prošlim stanjem. Time se hvataju i
// promjene napravljene *izvan* Saguara — kroz LuCI ili sa SSH-a — što je
// upravo ono što se inače ne vidi. OpenWrt nema više administratorskih računa
// (sve je root), pa se promjena izvan Saguara ne može pripisati osobi; može se
// samo pošteno označiti kao takva.

const uciDir = "/etc/config"
const auditInterval = 60 * time.Second
const auditKeep = 300     // koliko se zapisa čuva
const auditMaxDiff = 8000 // najveća duljina spremljene razlike
const auditAttribWin = 3 * time.Minute

// noteWrite pamti tko je zadnji pisao kroz Saguaro; po tome se promjena
// pripisuje korisniku ili se označava kao napravljena izvan sučelja.
func (s *server) noteWrite(user string) {
	s.writeMu.Lock()
	s.lastWriteUser = user
	s.lastWriteAt = time.Now()
	s.writeMu.Unlock()
}

func (s *server) writeSource() string {
	s.writeMu.RLock()
	user, at := s.lastWriteUser, s.lastWriteAt
	s.writeMu.RUnlock()
	if user != "" && time.Since(at) < auditAttribWin {
		return user
	}
	return "" // prazno = izvan Saguara
}

/* ---------- petlja ---------- */

func (s *server) auditLoop() {
	// prvi krug samo snima polazno stanje, da se pri prvom pokretanju ne
	// prijavi da je "sve promijenjeno"
	s.scanConfigs(true)
	for {
		time.Sleep(auditInterval)
		s.scanConfigs(false)
	}
}

func (s *server) scanConfigs(seedOnly bool) {
	entries, err := os.ReadDir(uciDir)
	if err != nil {
		return
	}
	names := []string{}
	for _, e := range entries {
		if !e.IsDir() && !strings.HasSuffix(e.Name(), ".apk-new") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	source := s.writeSource()
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(uciDir, name))
		if err != nil {
			continue
		}
		cur := string(b)
		sum := sha256.Sum256(b)
		hash := hex.EncodeToString(sum[:])

		var prevHash, prevBody string
		err = s.db.QueryRow(`SELECT hash, body FROM config_state WHERE name=?`,
			name).Scan(&prevHash, &prevBody)
		known := err == nil

		if known && prevHash == hash {
			continue
		}
		s.db.Exec(`INSERT INTO config_state (name, hash, body, updated_at)
			VALUES (?,?,?,datetime('now'))
			ON CONFLICT(name) DO UPDATE SET hash=excluded.hash,
			body=excluded.body, updated_at=datetime('now')`, name, hash, cur)

		if seedOnly || !known {
			continue // prvo viđenje datoteke nije promjena
		}
		diff := unifiedDiff(prevBody, cur)
		if diff == "" {
			continue
		}
		if len(diff) > auditMaxDiff {
			diff = diff[:auditMaxDiff] + "\n… (razlika je skraćena)"
		}
		added, removed := countDiff(diff)
		s.db.Exec(`INSERT INTO config_changes (name, source, added, removed, diff)
			VALUES (?,?,?,?,?)`, name, source, added, removed, diff)

		what := "korisnik " + source
		if source == "" {
			what = "izvan Saguara (LuCI ili SSH)"
		}
		s.alert("config", "info",
			"Promijenjena je konfiguracija "+name+" — "+what+
				" (+"+strconv.Itoa(added)+"/-"+strconv.Itoa(removed)+" redaka)")
	}
	s.db.Exec(`DELETE FROM config_changes WHERE id NOT IN
		(SELECT id FROM config_changes ORDER BY id DESC LIMIT ?)`, auditKeep)
}

/* ---------- razlika ---------- */

// unifiedDiff vraća razliku dvaju tekstova u obliku sličnom "diff -u", ali bez
// vanjskih alata. Datoteke su male (uci konfiguracije), pa je jednostavan
// algoritam najdužeg zajedničkog podniza sasvim dovoljan.
func unifiedDiff(oldText, newText string) string {
	a := strings.Split(strings.TrimRight(oldText, "\n"), "\n")
	b := strings.Split(strings.TrimRight(newText, "\n"), "\n")

	// tablica duljina najdužeg zajedničkog podniza
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var out strings.Builder
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out.WriteString("- " + a[i] + "\n")
			i++
		default:
			out.WriteString("+ " + b[j] + "\n")
			j++
		}
	}
	for ; i < n; i++ {
		out.WriteString("- " + a[i] + "\n")
	}
	for ; j < m; j++ {
		out.WriteString("+ " + b[j] + "\n")
	}
	return out.String()
}

func countDiff(diff string) (added, removed int) {
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "+ "):
			added++
		case strings.HasPrefix(l, "- "):
			removed++
		}
	}
	return
}

/* ---------- API ---------- */

func (s *server) handleAuditList(w http.ResponseWriter, r *http.Request) {
	type change struct {
		ID      int64  `json:"id"`
		TS      string `json:"ts"`
		Name    string `json:"name"`
		Source  string `json:"source"`
		Added   int    `json:"added"`
		Removed int    `json:"removed"`
	}
	out := []change{}
	rows, err := s.db.Query(`SELECT id, ts, name, COALESCE(source,''), added, removed
		FROM config_changes ORDER BY id DESC LIMIT 100`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var c change
			if rows.Scan(&c.ID, &c.TS, &c.Name, &c.Source, &c.Added,
				&c.Removed) == nil {
				out = append(out, c)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": out})
}

func (s *server) handleAuditDiff(w http.ResponseWriter, r *http.Request) {
	var diff, name, ts, source string
	err := s.db.QueryRow(`SELECT diff, name, ts, COALESCE(source,'')
		FROM config_changes WHERE id=?`, r.PathValue("id")).
		Scan(&diff, &name, &ts, &source)
	if err != nil {
		writeErr(w, http.StatusNotFound, "zapis ne postoji")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": name, "ts": ts, "source": source, "diff": diff,
	})
}

// handleAuditRun pokreće usporedbu odmah — da korisnik ne čeka sljedeći krug
// nakon što je nešto promijenio.
func (s *server) handleAuditRun(w http.ResponseWriter, r *http.Request) {
	s.scanConfigs(false)
	s.handleAuditList(w, r)
}
