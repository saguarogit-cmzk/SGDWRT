package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Safe Mode (uzor: MikroTik): rizične mrežne promjene se automatski VRAĆAJU
// ako ih nitko ne potvrdi u roku — zaštita od samo-zaključavanja s udaljene
// lokacije. GUI potvrđuje sam pri prvom uspješnom dohvatu nakon promjene;
// ako je pristup izgubljen, potvrde nema i uređaj se vrati na staro.
const rollbackWindow = 120 * time.Second

type pendingRollback struct {
	reason   string
	deadline time.Time
	files    map[string]string // originalna putanja -> ime backupa u backupDir
	reloads  [][2]string       // servisi za reload nakon vraćanja
	timer    *time.Timer
}

var (
	rbMu      sync.Mutex
	rbPending *pendingRollback
)

// scheduleRollback registrira odgođeno vraćanje; nova registracija zamjenjuje
// staru (backupi starije promjene su i dalje u backup direktoriju).
func (s *server) scheduleRollback(reason string, files map[string]string, reloads [][2]string) {
	rbMu.Lock()
	defer rbMu.Unlock()
	if rbPending != nil {
		rbPending.timer.Stop()
	}
	p := &pendingRollback{
		reason: reason, deadline: time.Now().Add(rollbackWindow),
		files: files, reloads: reloads,
	}
	p.timer = time.AfterFunc(rollbackWindow, func() { s.executeRollback(p) })
	rbPending = p
	log.Printf("safe mode: promjena '%s' čeka potvrdu (%.0f s)", reason,
		rollbackWindow.Seconds())
}

func (s *server) executeRollback(p *pendingRollback) {
	rbMu.Lock()
	if rbPending != p {
		rbMu.Unlock()
		return
	}
	rbPending = nil
	rbMu.Unlock()

	log.Printf("safe mode: promjena '%s' NIJE potvrđena — vraćam prethodno stanje", p.reason)
	for orig, backupName := range p.files {
		b, err := os.ReadFile(filepath.Join(s.backupDir, backupName))
		if err != nil {
			log.Printf("safe mode: backup %s nečitljiv: %v", backupName, err)
			continue
		}
		if err := os.WriteFile(orig, b, 0o600); err != nil {
			log.Printf("safe mode: vraćanje %s: %v", orig, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, svc := range p.reloads {
		if err := serviceReload(ctx, svc[0], svc[1]); err != nil {
			log.Printf("safe mode: %s %s: %v", svc[0], svc[1], err)
		}
	}
	addEvent(s, "warning", "Safe mode: promjena '"+p.reason+
		"' nije potvrđena i automatski je vraćena")
}

func (s *server) handleRollbackStatus(w http.ResponseWriter, r *http.Request) {
	rbMu.Lock()
	defer rbMu.Unlock()
	if rbPending == nil {
		writeJSON(w, http.StatusOK, map[string]any{"pending": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pending":      true,
		"reason":       rbPending.reason,
		"seconds_left": int(time.Until(rbPending.deadline).Seconds()),
	})
}

func (s *server) handleRollbackConfirm(w http.ResponseWriter, r *http.Request) {
	rbMu.Lock()
	defer rbMu.Unlock()
	if rbPending == nil {
		writeJSON(w, http.StatusOK, map[string]any{"confirmed": false})
		return
	}
	rbPending.timer.Stop()
	log.Printf("safe mode: promjena '%s' potvrđena", rbPending.reason)
	reason := rbPending.reason
	rbPending = nil
	writeJSON(w, http.StatusOK, map[string]any{"confirmed": true, "reason": reason})
}
