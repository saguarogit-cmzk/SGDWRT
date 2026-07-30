package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
)

/* ---------- modeli ---------- */

type Device struct {
	UUID       string `json:"uuid"`
	Hostname   string `json:"hostname"`
	Model      string `json:"model"`
	CPU        string `json:"cpu"`
	RAMMB      int64  `json:"ram_mb"`
	DiskGB     int64  `json:"disk_gb"`
	Serial     string `json:"serial"`
	Firmware   string `json:"firmware"`
	SaguaroVer string `json:"saguaro_version"`
	Location   string `json:"location"`
	Customer   string `json:"customer"`
	Notes      string `json:"notes"`
	IsSelf     bool   `json:"is_self"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

type Host struct {
	UUID      string `json:"uuid"`
	Hostname  string `json:"hostname"`
	MAC       string `json:"mac"`
	IPv4      string `json:"ipv4"`
	VLAN      int64  `json:"vlan"`
	Customer  string `json:"customer"`
	Notes     string `json:"notes"`
	Managed   bool   `json:"managed"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

const devCols = `uuid, hostname, COALESCE(model,''), COALESCE(cpu,''),
	COALESCE(ram_mb,0), COALESCE(disk_gb,0), COALESCE(serial,''),
	COALESCE(firmware,''), COALESCE(saguaro_ver,''), COALESCE(location,''),
	COALESCE(customer,''), COALESCE(notes,''), is_self, created_at, updated_at`

func scanDevice(row interface{ Scan(...any) error }) (Device, error) {
	var d Device
	err := row.Scan(&d.UUID, &d.Hostname, &d.Model, &d.CPU, &d.RAMMB, &d.DiskGB,
		&d.Serial, &d.Firmware, &d.SaguaroVer, &d.Location, &d.Customer,
		&d.Notes, &d.IsSelf, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}

const hostCols = `uuid, COALESCE(hostname,''), mac, COALESCE(ipv4,''),
	COALESCE(vlan,0), COALESCE(customer,''), COALESCE(notes,''), managed,
	created_at, updated_at`

func scanHost(row interface{ Scan(...any) error }) (Host, error) {
	var h Host
	err := row.Scan(&h.UUID, &h.Hostname, &h.MAC, &h.IPv4, &h.VLAN,
		&h.Customer, &h.Notes, &h.Managed, &h.CreatedAt, &h.UpdatedAt)
	return h, err
}

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

/* ---------- samoregistracija ---------- */

// ensureSelf pri startu upiše/osvježi zapis ovog uređaja u inventoryju.
func (s *server) ensureSelf(ctx context.Context) error {
	var uuid string
	err := s.db.QueryRow(`SELECT uuid FROM devices WHERE is_self = 1`).Scan(&uuid)
	if err == sql.ErrNoRows {
		uuid = newUUID()
		_, err = s.db.Exec(
			`INSERT INTO devices (uuid, hostname, is_self) VALUES (?, 'unknown', 1)`, uuid)
	}
	if err != nil {
		return err
	}

	var b ubusBoard
	var i ubusInfo
	if err := ubusCall(ctx, "system", "board", &b); err != nil {
		return err
	}
	if err := ubusCall(ctx, "system", "info", &i); err != nil {
		return err
	}
	model := b.Model
	if model == "" {
		model = b.System
	}
	_, err = s.db.Exec(`UPDATE devices SET hostname=?, model=?, cpu=?, ram_mb=?,
		disk_gb=?, serial=?, firmware=?, saguaro_ver=?, updated_at=datetime('now')
		WHERE uuid=?`,
		b.Hostname, model, b.System, i.Memory.Total/(1024*1024),
		i.Root.Total/(1024*1024), readDMISerial(), b.Release.Description,
		version, uuid)
	if err != nil {
		return err
	}

	var devs map[string]ubusDevice
	if err := ubusCall(ctx, "network.device", "status", &devs); err == nil {
		names := make([]string, 0, len(devs))
		for n := range devs {
			if strings.HasPrefix(n, "eth") {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`DELETE FROM device_interfaces WHERE device_uuid=?`, uuid); err != nil {
			return err
		}
		for _, n := range names {
			if _, err := tx.Exec(`INSERT INTO device_interfaces (device_uuid, name, mac)
				VALUES (?, ?, ?)`, uuid, n, devs[n].MAC); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	return nil
}

// readDMISerial best-effort čita serijski broj iz DMI sysfs-a (x86).
func readDMISerial() string {
	for _, p := range []string{
		"/sys/class/dmi/id/product_serial",
		"/sys/class/dmi/id/board_serial",
	} {
		if b, err := os.ReadFile(p); err == nil {
			s := strings.TrimSpace(string(b))
			if s != "" && !strings.EqualFold(s, "To be filled by O.E.M.") &&
				!strings.EqualFold(s, "Default string") && s != "None" {
				return s
			}
		}
	}
	return ""
}

/* ---------- pomoćne za handlere ---------- */

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "neispravan JSON: "+err.Error())
		return false
	}
	return true
}

// applyUpdate gradi i izvrši UPDATE iz whitelist polja.
func (s *server) applyUpdate(w http.ResponseWriter, table, uuid string,
	body map[string]any, allowed []string) bool {
	set := make([]string, 0, len(allowed))
	args := make([]any, 0, len(allowed))
	for _, f := range allowed {
		if v, ok := body[f]; ok {
			if b, isBool := v.(bool); isBool {
				if b {
					v = 1
				} else {
					v = 0
				}
			}
			set = append(set, f+"=?")
			args = append(args, v)
		}
	}
	if len(set) == 0 {
		writeErr(w, http.StatusBadRequest, "nema polja za izmjenu")
		return false
	}
	args = append(args, uuid)
	q := "UPDATE " + table + " SET " + strings.Join(set, ", ") +
		", updated_at=datetime('now') WHERE uuid=?"
	res, err := s.db.Exec(q, args...)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return false
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "zapis ne postoji")
		return false
	}
	return true
}

/* ---------- devices handleri ---------- */

func (s *server) handleIdentity(w http.ResponseWriter, r *http.Request) {
	d, err := scanDevice(s.db.QueryRow(
		`SELECT ` + devCols + ` FROM devices WHERE is_self = 1`))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *server) handleDeviceList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(
		`SELECT ` + devCols + ` FROM devices ORDER BY is_self DESC, hostname`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

func (s *server) handleDeviceGet(w http.ResponseWriter, r *http.Request) {
	d, err := scanDevice(s.db.QueryRow(
		`SELECT `+devCols+` FROM devices WHERE uuid=?`, r.PathValue("uuid")))
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "uređaj ne postoji")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *server) handleDeviceCreate(w http.ResponseWriter, r *http.Request) {
	var d Device
	if !decodeBody(w, r, &d) {
		return
	}
	if strings.TrimSpace(d.Hostname) == "" {
		writeErr(w, http.StatusBadRequest, "hostname je obavezan")
		return
	}
	d.UUID = newUUID()
	_, err := s.db.Exec(`INSERT INTO devices
		(uuid, hostname, model, cpu, ram_mb, disk_gb, serial, firmware,
		 location, customer, notes, is_self)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,0)`,
		d.UUID, d.Hostname, d.Model, d.CPU, d.RAMMB, d.DiskGB, d.Serial,
		d.Firmware, d.Location, d.Customer, d.Notes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.handleDeviceGetByUUID(w, d.UUID, http.StatusCreated)
}

func (s *server) handleDeviceGetByUUID(w http.ResponseWriter, uuid string, code int) {
	d, err := scanDevice(s.db.QueryRow(
		`SELECT `+devCols+` FROM devices WHERE uuid=?`, uuid))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, code, d)
}

func (s *server) handleDeviceUpdate(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	var body map[string]any
	if !decodeBody(w, r, &body) {
		return
	}
	var isSelf bool
	err := s.db.QueryRow(`SELECT is_self FROM devices WHERE uuid=?`, uuid).Scan(&isSelf)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "uređaj ne postoji")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// za ovaj uređaj hardverska polja puni samoregistracija, ne API
	allowed := []string{"hostname", "model", "cpu", "ram_mb", "disk_gb",
		"serial", "firmware", "location", "customer", "notes"}
	if isSelf {
		allowed = []string{"location", "customer", "notes"}
	}
	if s.applyUpdate(w, "devices", uuid, body, allowed) {
		s.handleDeviceGetByUUID(w, uuid, http.StatusOK)
	}
}

func (s *server) handleDeviceDelete(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	var isSelf bool
	err := s.db.QueryRow(`SELECT is_self FROM devices WHERE uuid=?`, uuid).Scan(&isSelf)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "uređaj ne postoji")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if isSelf {
		writeErr(w, http.StatusForbidden, "vlastiti uređaj se ne može obrisati")
		return
	}
	if _, err := s.db.Exec(`DELETE FROM devices WHERE uuid=?`, uuid); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": uuid})
}

/* ---------- hosts handleri ---------- */

func (s *server) handleHostList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT ` + hostCols + ` FROM hosts ORDER BY hostname, mac`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	out := []Host{}
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, h)
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": out})
}

func (s *server) handleHostCreate(w http.ResponseWriter, r *http.Request) {
	var h Host
	if !decodeBody(w, r, &h) {
		return
	}
	mac, err := net.ParseMAC(strings.TrimSpace(h.MAC))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "neispravna MAC adresa")
		return
	}
	if h.IPv4 != "" && net.ParseIP(h.IPv4) == nil {
		writeErr(w, http.StatusBadRequest, "neispravna IPv4 adresa")
		return
	}
	h.UUID = newUUID()
	managed := 0
	if h.Managed {
		managed = 1
	}
	_, err = s.db.Exec(`INSERT INTO hosts
		(uuid, hostname, mac, ipv4, vlan, customer, notes, managed)
		VALUES (?,?,?,?,?,?,?,?)`,
		h.UUID, h.Hostname, strings.ToLower(mac.String()), h.IPv4, h.VLAN,
		h.Customer, h.Notes, managed)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "host s tom MAC adresom već postoji")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	hh, _ := scanHost(s.db.QueryRow(`SELECT `+hostCols+` FROM hosts WHERE uuid=?`, h.UUID))
	writeJSON(w, http.StatusCreated, hh)
}

func (s *server) handleHostUpdate(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	var body map[string]any
	if !decodeBody(w, r, &body) {
		return
	}
	if v, ok := body["mac"].(string); ok {
		mac, err := net.ParseMAC(strings.TrimSpace(v))
		if err != nil {
			writeErr(w, http.StatusBadRequest, "neispravna MAC adresa")
			return
		}
		body["mac"] = strings.ToLower(mac.String())
	}
	if v, ok := body["ipv4"].(string); ok && v != "" && net.ParseIP(v) == nil {
		writeErr(w, http.StatusBadRequest, "neispravna IPv4 adresa")
		return
	}
	allowed := []string{"hostname", "mac", "ipv4", "vlan", "customer", "notes", "managed"}
	if s.applyUpdate(w, "hosts", uuid, body, allowed) {
		h, _ := scanHost(s.db.QueryRow(`SELECT `+hostCols+` FROM hosts WHERE uuid=?`, uuid))
		writeJSON(w, http.StatusOK, h)
	}
}

func (s *server) handleHostDelete(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.Exec(`DELETE FROM hosts WHERE uuid=?`, r.PathValue("uuid"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "host ne postoji")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": r.PathValue("uuid")})
}
