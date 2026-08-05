package main

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

func openDB(path string) (*sql.DB, error) {
	// dovrši odgođeni restore iz backupa (staged prije reboota u backup modulu)
	if st, err := os.Stat(path + ".restore"); err == nil && st.Mode().IsRegular() {
		os.Remove(path)
		os.Remove(path + "-wal")
		os.Remove(path + "-shm")
		if err := os.Rename(path+".restore", path); err != nil {
			return nil, fmt.Errorf("restore baze: %w", err)
		}
	}
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc/sqlite je najstabilniji s jednom konekcijom; promet je malen.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("shema: %w", err)
	}
	// migracije postojećih baza (IF NOT EXISTS ne dodaje nove stupce);
	// greška "duplicate column" znači da je stupac već tu
	db.Exec(`ALTER TABLE fw_forwards ADD COLUMN reflection INTEGER NOT NULL DEFAULT 1`)
	db.Exec(`ALTER TABLE fw_forwards ADD COLUMN src_dip TEXT`)
	db.Exec(`ALTER TABLE fw_rules ADD COLUMN start_time TEXT`)
	db.Exec(`ALTER TABLE fw_rules ADD COLUMN stop_time TEXT`)
	db.Exec(`ALTER TABLE fw_rules ADD COLUMN weekdays TEXT`)
	db.Exec(`ALTER TABLE users ADD COLUMN must_change_pw INTEGER NOT NULL DEFAULT 0`)
	// uloge (E1): postojeći jedini korisnik ostaje admin, što je i bio
	db.Exec(`ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'admin'`)
	db.Exec(`ALTER TABLE users ADD COLUMN full_name TEXT`)
	db.Exec(`ALTER TABLE users ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE users ADD COLUMN last_login TEXT`)
	// dvofaktorska prijava (E2)
	db.Exec(`ALTER TABLE users ADD COLUMN totp_secret TEXT`)
	db.Exec(`ALTER TABLE users ADD COLUMN totp_enabled INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE users ADD COLUMN totp_last INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE ovpn_clients ADD COLUMN pass_hash TEXT`)
	db.Exec(`ALTER TABLE rp_sites ADD COLUMN tls_mode TEXT NOT NULL DEFAULT 'passthrough'`)
	db.Exec(`ALTER TABLE rp_sites ADD COLUMN acme_staging INTEGER NOT NULL DEFAULT 0`)
	return db, nil
}
