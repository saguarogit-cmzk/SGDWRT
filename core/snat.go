package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Izlazna javna adresa po mreži (policy SNAT).
//
// Kad uređaj na WAN sučelju ima više javnih adresa, zadano sav odlazni promet
// izlazi s prve. Ovdje se za pojedinu mrežu, podmrežu ili host bira druga —
// npr. "računovodstvo izlazi kao 203.0.113.11". Bitno je kad druga strana
// filtrira po izvorišnoj adresi (banke, državni servisi, ugled mail servera).
//
// Izvodi se kroz fw4 `config nat` sekcije (target SNAT, snat_ip), pod
// prefiksom sag_snat_ — kao i sve ostalo, Saguaro dira samo svoje zapise.
// Redoslijed je bitan: prvo pravilo koje odgovara paketu pobjeđuje.

const snatPrefix = "sag_snat_"

type SNATRule struct {
	UUID     string `json:"uuid"`
	Name     string `json:"name"`
	Pos      int    `json:"pos"`      // redoslijed primjene (manji broj = ranije)
	OutZone  string `json:"out_zone"` // izlazna zona (wan, wan2…)
	SrcIP    string `json:"src_ip"`   // mreža/host čiji promet mijenja adresu
	DestIP   string `json:"dest_ip"`  // prazno = bilo koje odredište
	DestPort string `json:"dest_port"`
	Proto    string `json:"proto"` // all | tcp | udp | tcp udp
	SnatIP   string `json:"snat_ip"`
	Enabled  bool   `json:"enabled"`
	Notes    string `json:"notes"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

const snatCols = `uuid, name, pos, out_zone, src_ip, COALESCE(dest_ip,''),
	COALESCE(dest_port,''), proto, snat_ip, enabled, COALESCE(notes,''),
	created_at, updated_at`

func scanSNAT(row interface{ Scan(...any) error }) (SNATRule, error) {
	var n SNATRule
	err := row.Scan(&n.UUID, &n.Name, &n.Pos, &n.OutZone, &n.SrcIP, &n.DestIP,
		&n.DestPort, &n.Proto, &n.SnatIP, &n.Enabled, &n.Notes,
		&n.CreatedAt, &n.UpdatedAt)
	return n, err
}

type snatIn struct {
	SNATRule
	Enabled *bool `json:"enabled"`
}

// ifaceDump je zajednički oblik za čitanje sučelja iz ubus-a.
type ifaceDump struct {
	Interface []struct {
		Interface   string `json:"interface"`
		Up          bool   `json:"up"`
		Device      string `json:"device"`
		IPv4Address []struct {
			Address string `json:"address"`
			Mask    int    `json:"mask"`
		} `json:"ipv4-address"`
	} `json:"interface"`
}

// wanIfaces vraća imena sučelja koja pripadaju zonama prema internetu.
// Pripadnost se čita iz firewall zona (masq ili ime wan*), a ne iz ruta —
// u laboratoriju i LAN zna imati zadanu rutu, pa bi po ruti ispalo da je WAN.
func (s *server) wanIfaces(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	cfg, err := uciGetConfig(ctx, "firewall")
	if err != nil {
		return out
	}
	for _, sec := range cfg {
		if sectStr(sec, ".type") != "zone" {
			continue
		}
		name := sectStr(sec, "name")
		if sectStr(sec, "masq") != "1" && !strings.HasPrefix(name, "wan") {
			continue
		}
		for _, n := range sectList(sec, "network") {
			out[n] = true
		}
	}
	return out
}

// wanIPv4Addrs čita adrese stvarno postavljene na sučeljima prema internetu.
// Služi za dvije stvari: popis u sučelju i provjeru da se ne upiše adresa
// koje na uređaju nema — to je najčešća greška kod ovakvih pravila, a
// posljedica je tiho odbačen promet.
func (s *server) wanIPv4Addrs(ctx context.Context) map[string][]string {
	out := map[string][]string{}
	wan := s.wanIfaces(ctx)
	var dump ifaceDump
	if err := ubusCall(ctx, "network.interface", "dump", &dump); err != nil {
		return out
	}
	for _, i := range dump.Interface {
		if !wan[i.Interface] {
			continue
		}
		for _, a := range i.IPv4Address {
			if a.Address != "" {
				out[i.Interface] = append(out[i.Interface], a.Address)
			}
		}
	}
	return out
}

func (s *server) snatKnownAddrs(ctx context.Context) map[string]bool {
	known := map[string]bool{}
	for _, addrs := range s.wanIPv4Addrs(ctx) {
		for _, a := range addrs {
			known[a] = true
		}
	}
	return known
}

func validateSNAT(w http.ResponseWriter, n *SNATRule, known map[string]bool) bool {
	n.Name = strings.TrimSpace(n.Name)
	n.OutZone = strings.TrimSpace(n.OutZone)
	n.SrcIP = strings.TrimSpace(n.SrcIP)
	n.DestIP = strings.TrimSpace(n.DestIP)
	n.DestPort = strings.TrimSpace(n.DestPort)
	n.SnatIP = strings.TrimSpace(n.SnatIP)
	n.Proto = strings.TrimSpace(n.Proto)
	if n.OutZone == "" {
		n.OutZone = "wan"
	}
	if n.Proto == "" {
		n.Proto = "all"
	}
	switch {
	case n.Name == "" || hasCtrl(n.Name):
		writeErr(w, http.StatusBadRequest, "naziv je obavezan i bez prijeloma retka")
	case !reZone.MatchString(n.OutZone):
		writeErr(w, http.StatusBadRequest, "neispravno ime izlazne zone")
	case n.SrcIP == "":
		writeErr(w, http.StatusBadRequest,
			"upiši mrežu ili adresu čiji promet mijenja izlaznu adresu")
	case !validIPOrCIDROrAlias(n.SrcIP):
		writeErr(w, http.StatusBadRequest, "izvor mora biti IP, CIDR ili @alias")
	case n.DestIP != "" && !validIPOrCIDROrAlias(n.DestIP):
		writeErr(w, http.StatusBadRequest, "odredište mora biti IP, CIDR ili @alias")
	case net.ParseIP(n.SnatIP) == nil || net.ParseIP(n.SnatIP).To4() == nil:
		writeErr(w, http.StatusBadRequest, "izlazna adresa mora biti IPv4 adresa")
	case len(known) > 0 && !known[n.SnatIP]:
		// tvrda greška, ne upozorenje: pravilo s nepostojećom adresom tiho
		// odbacuje promet te mreže, a to se teško poveže s uzrokom
		writeErr(w, http.StatusConflict, "adrese "+n.SnatIP+
			" nema ni na jednom WAN sučelju — dodaj je u postavkama WAN veze "+
			"ili odaberi jednu od postojećih")
	default:
		return true
	}
	return false
}

// validIPOrCIDROrAlias prihvaća IP, CIDR ili @alias oblik.
func validIPOrCIDROrAlias(v string) bool {
	if strings.HasPrefix(v, "@") {
		return len(v) > 1 && !hasCtrl(v)
	}
	if net.ParseIP(v) != nil {
		return true
	}
	_, _, err := net.ParseCIDR(v)
	return err == nil
}

func (s *server) snatList() ([]SNATRule, error) {
	rows, err := s.db.Query(`SELECT ` + snatCols + ` FROM fw_snat
		ORDER BY pos, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SNATRule{}
	for rows.Next() {
		n, err := scanSNAT(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *server) handleSNATList(w http.ResponseWriter, r *http.Request) {
	out, err := s.snatList()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"snat":     out,
		"wan_ips":  s.wanIPv4Addrs(r.Context()),
		"networks": s.localNetworks(r.Context()),
	})
}

// localNetworks vraća lokalne mreže (naziv -> CIDR) za padajući izbornik u
// sučelju, da se podmreža ne prepisuje ručno.
func (s *server) localNetworks(ctx context.Context) map[string]string {
	out := map[string]string{}
	wan := s.wanIfaces(ctx)
	var dump ifaceDump
	if err := ubusCall(ctx, "network.interface", "dump", &dump); err != nil {
		return out
	}
	for _, i := range dump.Interface {
		if i.Interface == "loopback" || wan[i.Interface] || len(i.IPv4Address) == 0 {
			continue
		}
		a := i.IPv4Address[0]
		_, ipnet, err := net.ParseCIDR(fmt.Sprintf("%s/%d", a.Address, a.Mask))
		if err != nil {
			continue
		}
		out[i.Interface] = ipnet.String()
	}
	return out
}

func (s *server) handleSNATCreate(w http.ResponseWriter, r *http.Request) {
	var in snatIn
	if !decodeBody(w, r, &in) {
		return
	}
	n := &in.SNATRule
	if !validateSNAT(w, n, s.snatKnownAddrs(r.Context())) {
		return
	}
	// novo pravilo ide na kraj popisa
	var maxPos int
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(pos),0) FROM fw_snat`).Scan(&maxPos)
	n.UUID = newUUID()
	n.Pos = maxPos + 1
	_, err := s.db.Exec(`INSERT INTO fw_snat
		(uuid, name, pos, out_zone, src_ip, dest_ip, dest_port, proto, snat_ip,
		 enabled, notes)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		n.UUID, n.Name, n.Pos, n.OutZone, n.SrcIP, n.DestIP, n.DestPort,
		n.Proto, n.SnatIP, enabledIntOf(in.Enabled), n.Notes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	nn, _ := scanSNAT(s.db.QueryRow(`SELECT `+snatCols+` FROM fw_snat WHERE uuid=?`, n.UUID))
	writeJSON(w, http.StatusCreated, nn)
}

func (s *server) handleSNATUpdate(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	var in snatIn
	if !decodeBody(w, r, &in) {
		return
	}
	n := &in.SNATRule
	if !validateSNAT(w, n, s.snatKnownAddrs(r.Context())) {
		return
	}
	res, err := s.db.Exec(`UPDATE fw_snat SET name=?, out_zone=?, src_ip=?,
		dest_ip=?, dest_port=?, proto=?, snat_ip=?, enabled=?, notes=?,
		updated_at=datetime('now') WHERE uuid=?`,
		n.Name, n.OutZone, n.SrcIP, n.DestIP, n.DestPort, n.Proto, n.SnatIP,
		enabledIntOf(in.Enabled), n.Notes, uuid)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if k, _ := res.RowsAffected(); k == 0 {
		writeErr(w, http.StatusNotFound, "pravilo ne postoji")
		return
	}
	nn, _ := scanSNAT(s.db.QueryRow(`SELECT `+snatCols+` FROM fw_snat WHERE uuid=?`, uuid))
	writeJSON(w, http.StatusOK, nn)
}

func (s *server) handleSNATDelete(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.Exec(`DELETE FROM fw_snat WHERE uuid=?`, r.PathValue("uuid"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if k, _ := res.RowsAffected(); k == 0 {
		writeErr(w, http.StatusNotFound, "pravilo ne postoji")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// handleSNATMove mijenja redoslijed pravila zamjenom sa susjednim. Redoslijed
// je bitan jer paket obrađuje prvo pravilo koje mu odgovara.
func (s *server) handleSNATMove(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	var in struct {
		Dir string `json:"dir"` // up | down
	}
	if !decodeBody(w, r, &in) {
		return
	}
	list, err := s.snatList()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	idx := -1
	for i, n := range list {
		if n.UUID == uuid {
			idx = i
		}
	}
	if idx < 0 {
		writeErr(w, http.StatusNotFound, "pravilo ne postoji")
		return
	}
	other := idx - 1
	if in.Dir == "down" {
		other = idx + 1
	}
	if other < 0 || other >= len(list) {
		writeJSON(w, http.StatusOK, map[string]any{"moved": false})
		return
	}
	// redoslijed se svaki put ispiše iznova (1..n) — tako ostaje uredan i kad
	// su pozicije iz starijih zapisa razmaknute ili jednake
	list[idx], list[other] = list[other], list[idx]
	for i, n := range list {
		if _, err := s.db.Exec(`UPDATE fw_snat SET pos=? WHERE uuid=?`, i+1, n.UUID); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"moved": true})
}

// writeSNATSections ispisuje uci naredbe za sva uključena pravila.
// Poziva se iz primjene firewalla, unutar iste transakcije i backupa.
// Napomena: fw4 u nat sekciji ne prihvaća listu adresa (`src_ip must not be a
// list`), pa se pravilo s aliasom od više adresa razlomi na po jednu sekciju
// za svaku kombinaciju izvora i odredišta. Redni broj u nazivu sekcije čuva
// redoslijed, jer fw4 nat sekcije obrađuje onim redom kojim su zapisane.
func (s *server) writeSNATSections(b *strings.Builder, list []SNATRule) error {
	idx := 0
	for _, n := range list {
		if !n.Enabled {
			continue
		}
		srcs, err := s.resolveAlias(n.SrcIP)
		if err != nil {
			return fmt.Errorf("izlazna adresa %s: %v", n.Name, err)
		}
		dsts := []string{""}
		if n.DestIP != "" {
			dsts, err = s.resolveAlias(n.DestIP)
			if err != nil {
				return fmt.Errorf("izlazna adresa %s: %v", n.Name, err)
			}
		}
		short := strings.ReplaceAll(n.UUID, "-", "")[:6]
		for _, src := range srcs {
			for _, dst := range dsts {
				idx++
				sn := fmt.Sprintf("%s%02d%s", snatPrefix, idx, short)
				fmt.Fprintf(b, "set firewall.%s=nat\n", sn)
				fmt.Fprintf(b, "set firewall.%s.target=SNAT\n", sn)
				fmt.Fprintf(b, "set firewall.%s.name=%s\n", sn, uciQuote(n.Name))
				fmt.Fprintf(b, "set firewall.%s.src=%s\n", sn, n.OutZone)
				fmt.Fprintf(b, "set firewall.%s.src_ip=%s\n", sn, src)
				if dst != "" {
					fmt.Fprintf(b, "set firewall.%s.dest_ip=%s\n", sn, dst)
				}
				if n.DestPort != "" {
					fmt.Fprintf(b, "set firewall.%s.dest_port=%s\n", sn, uciQuote(n.DestPort))
				}
				fmt.Fprintf(b, "set firewall.%s.proto=%s\n", sn, uciQuote(n.Proto))
				fmt.Fprintf(b, "set firewall.%s.snat_ip=%s\n", sn, n.SnatIP)
			}
		}
	}
	return nil
}
