package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// Statičke rute.
//
// Uređaj zna put do mreža koje su na njemu i do interneta. Sve ostalo —
// mreža iza drugog rutera, podmreža na drugoj lokaciji preko VPN-a, stari
// segment koji je ostao na zasebnom uređaju — treba ručno upisanu rutu:
// "za 192.168.100.0/24 idi na 192.168.50.1".
//
// Zapisuje se kao `config route` (IPv4) odnosno `config route6` u
// /etc/config/network, pod prefiksom sag_rt_ — Saguaro i ovdje dira samo
// vlastite zapise (D-011). Primjena ide kroz safe mode: ako kriva ruta
// prekine pristup uređaju, stara se konfiguracija sama vrati.

const routePrefix = "sag_rt_"

type StaticRoute struct {
	UUID    string `json:"uuid"`
	Name    string `json:"name"`
	Family  string `json:"family"` // ipv4 | ipv6
	Iface   string `json:"iface"`  // logičko sučelje (lan, wan, sag_vlan20…)
	Target  string `json:"target"` // odredišna mreža u CIDR obliku
	Gateway string `json:"gateway"`
	Metric  int    `json:"metric"`
	Enabled bool   `json:"enabled"`
	Notes   string `json:"notes"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

const routeCols = `uuid, name, family, iface, target, COALESCE(gateway,''),
	metric, enabled, COALESCE(notes,''), created_at, updated_at`

func scanRoute(row interface{ Scan(...any) error }) (StaticRoute, error) {
	var n StaticRoute
	err := row.Scan(&n.UUID, &n.Name, &n.Family, &n.Iface, &n.Target,
		&n.Gateway, &n.Metric, &n.Enabled, &n.Notes, &n.CreatedAt, &n.UpdatedAt)
	return n, err
}

type routeIn struct {
	StaticRoute
	Enabled *bool `json:"enabled"`
}

/* ---------- provjere ---------- */

// normalizeTarget prihvaća i golu adresu (192.168.5.7) i mrežu (10.0.0.0/8),
// a vraća uvijek CIDR oblik i mrežnu adresu — 10.0.0.5/8 je gotovo uvijek
// tipfeler, a u tablici bi izgledalo kao da radi.
func normalizeTarget(v, family string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", fmt.Errorf("upiši odredišnu mrežu")
	}
	if ip := net.ParseIP(v); ip != nil {
		if (ip.To4() != nil) != (family == "ipv4") {
			return "", fmt.Errorf("adresa %s nije %s", v, family)
		}
		if ip.To4() != nil {
			return ip.String() + "/32", nil
		}
		return ip.String() + "/128", nil
	}
	ip, ipnet, err := net.ParseCIDR(v)
	if err != nil {
		return "", fmt.Errorf("odredište mora biti mreža u obliku 192.168.100.0/24")
	}
	if (ip.To4() != nil) != (family == "ipv4") {
		return "", fmt.Errorf("mreža %s ne odgovara odabranoj vrsti adresa", v)
	}
	ones, bits := ipnet.Mask.Size()
	if ones == 0 {
		return "", fmt.Errorf(
			"ovo je zadana ruta (sav promet) — nju vodi internet veza, " +
				"a kod više veza modul Multi-WAN; statička ruta bi ih tiho zaobišla")
	}
	if bits == 0 {
		return "", fmt.Errorf("neispravna maska")
	}
	return ipnet.String(), nil
}

// validateRoute vraća true ako je zapis ispravan; inače sam pošalje odgovor.
// networks su lokalne mreže uređaja (sučelje -> CIDR) za provjeru gatewaya.
func validateRoute(w http.ResponseWriter, n *StaticRoute, ifaces map[string][]string) bool {
	n.Name = strings.TrimSpace(n.Name)
	n.Iface = strings.TrimSpace(n.Iface)
	n.Gateway = strings.TrimSpace(n.Gateway)
	if n.Family == "" {
		n.Family = "ipv4"
	}
	switch {
	case n.Name == "" || hasCtrl(n.Name):
		writeErr(w, http.StatusBadRequest, "upiši naziv rute")
		return false
	case n.Family != "ipv4" && n.Family != "ipv6":
		writeErr(w, http.StatusBadRequest, "vrsta adresa mora biti ipv4 ili ipv6")
		return false
	case n.Iface == "" || hasCtrl(n.Iface):
		writeErr(w, http.StatusBadRequest, "odaberi sučelje kroz koje ruta izlazi")
		return false
	case len(ifaces) > 0 && ifaces[n.Iface] == nil:
		writeErr(w, http.StatusBadRequest, "sučelje "+n.Iface+" ne postoji")
		return false
	case n.Metric < 0 || n.Metric > 65535:
		writeErr(w, http.StatusBadRequest, "metrika mora biti između 0 i 65535")
		return false
	}
	t, err := normalizeTarget(n.Target, n.Family)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return false
	}
	n.Target = t

	if n.Gateway == "" {
		// bez gatewaya ruta vrijedi samo ako je odredište izravno na tom
		// sučelju (on-link) — rijetko, ali legitimno
		return true
	}
	gw := net.ParseIP(n.Gateway)
	if gw == nil || (gw.To4() != nil) != (n.Family == "ipv4") {
		writeErr(w, http.StatusBadRequest,
			"gateway mora biti "+n.Family+" adresa")
		return false
	}
	// Gateway mora biti dosežan izravno preko odabranog sučelja. Jezgra takvu
	// rutu inače odbija ("invalid gateway"), a u sučelju bi izgledala kao da
	// je primijenjena — pa se odbija odmah, uz objašnjenje.
	subnets := ifaces[n.Iface]
	if len(subnets) == 0 {
		return true // sučelje još nema adresu (npr. veza nije podignuta)
	}
	for _, c := range subnets {
		_, ipnet, err := net.ParseCIDR(c)
		if err == nil && ipnet.Contains(gw) {
			return true
		}
	}
	writeErr(w, http.StatusBadRequest, fmt.Sprintf(
		"gateway %s nije u mreži sučelja %s (%s) — jezgra takvu rutu odbija; "+
			"provjeri jesi li odabrao pravo sučelje",
		n.Gateway, n.Iface, strings.Join(subnets, ", ")))
	return false
}

// routeIfaceSubnets vraća sva logička sučelja i njihove podmreže, obje vrste
// adresa zajedno — koristi se i za provjeru i za padajući izbornik.
func (s *server) routeIfaceSubnets(ctx context.Context) map[string][]string {
	out := map[string][]string{}
	var dump struct {
		Interface []struct {
			Interface   string `json:"interface"`
			IPv4Address []struct {
				Address string `json:"address"`
				Mask    int    `json:"mask"`
			} `json:"ipv4-address"`
			IPv6Address []struct {
				Address string `json:"address"`
				Mask    int    `json:"mask"`
			} `json:"ipv6-address"`
		} `json:"interface"`
	}
	if err := ubusCall(ctx, "network.interface", "dump", &dump); err != nil {
		return out
	}
	for _, i := range dump.Interface {
		if i.Interface == "loopback" {
			continue
		}
		subs := []string{}
		for _, a := range i.IPv4Address {
			if _, n, err := net.ParseCIDR(fmt.Sprintf("%s/%d", a.Address, a.Mask)); err == nil {
				subs = append(subs, n.String())
			}
		}
		for _, a := range i.IPv6Address {
			if _, n, err := net.ParseCIDR(fmt.Sprintf("%s/%d", a.Address, a.Mask)); err == nil {
				subs = append(subs, n.String())
			}
		}
		out[i.Interface] = subs
	}
	return out
}

/* ---------- stvarno stanje u jezgri ---------- */

// kernelRoute je jedan redak iz tablice usmjeravanja same jezgre. Čita se iz
// /proc/net/route i /proc/net/ipv6_route — to su zapisi jezgre, ne ispis
// nekog alata, pa nema parsiranja teksta koji se može promijeniti (D-007).
type kernelRoute struct {
	Family  string `json:"family"`
	Target  string `json:"target"`
	Gateway string `json:"gateway"`
	Device  string `json:"device"`
	Metric  int    `json:"metric"`
}

// leHexIPv4 pretvara adresu zapisanu obrnutim redoslijedom bajtova (kako je u
// /proc/net/route) u čitljiv oblik.
func leHexIPv4(s string) net.IP {
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return nil
	}
	return net.IPv4(byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

func maskBits(m net.IP) int {
	if m == nil || m.To4() == nil {
		return 0
	}
	ones, _ := net.IPMask(m.To4()).Size()
	return ones
}

func kernelRoutes() []kernelRoute {
	out := []kernelRoute{}
	if b, err := os.ReadFile("/proc/net/route"); err == nil {
		for i, ln := range strings.Split(string(b), "\n") {
			f := strings.Fields(ln)
			if i == 0 || len(f) < 11 {
				continue
			}
			dst, gw, msk := leHexIPv4(f[1]), leHexIPv4(f[2]), leHexIPv4(f[7])
			if dst == nil {
				continue
			}
			metric, _ := strconv.Atoi(f[6])
			g := ""
			if gw != nil && !gw.Equal(net.IPv4zero) {
				g = gw.String()
			}
			out = append(out, kernelRoute{
				Family:  "ipv4",
				Target:  fmt.Sprintf("%s/%d", dst.String(), maskBits(msk)),
				Gateway: g, Device: f[0], Metric: metric,
			})
		}
	}
	if b, err := os.ReadFile("/proc/net/ipv6_route"); err == nil {
		for _, ln := range strings.Split(string(b), "\n") {
			f := strings.Fields(ln)
			if len(f) < 10 {
				continue
			}
			dst := hexIPv6(f[0])
			gw := hexIPv6(f[4])
			if dst == nil {
				continue
			}
			plen, _ := strconv.ParseInt(f[1], 16, 32)
			metric, _ := strconv.ParseInt(f[5], 16, 64)
			g := ""
			if gw != nil && !gw.Equal(net.IPv6zero) {
				g = gw.String()
			}
			out = append(out, kernelRoute{
				Family:  "ipv6",
				Target:  fmt.Sprintf("%s/%d", dst.String(), plen),
				Gateway: g, Device: f[9], Metric: int(metric),
			})
		}
	}
	return out
}

func hexIPv6(s string) net.IP {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return nil
	}
	return net.IP(b)
}

/* ---------- API ---------- */

func (s *server) routeList() ([]StaticRoute, error) {
	rows, err := s.db.Query(`SELECT ` + routeCols + ` FROM nw_routes
		ORDER BY family, metric, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StaticRoute{}
	for rows.Next() {
		n, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *server) handleRouteList(w http.ResponseWriter, r *http.Request) {
	list, err := s.routeList()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"routes":  list,
		"ifaces":  s.routeIfaceSubnets(r.Context()),
		"kernel":  kernelRoutes(),
		"applied": s.routesApplied(r.Context(), list),
	})
}

// routesApplied javlja odgovara li zapisano stanje onome u konfiguraciji
// uređaja — da se vidi treba li stisnuti Primijeni.
func (s *server) routesApplied(ctx context.Context, list []StaticRoute) bool {
	cfg, err := uciGetConfig(ctx, "network")
	if err != nil {
		return false
	}
	have := 0
	for name := range cfg {
		if strings.HasPrefix(name, routePrefix) {
			have++
		}
	}
	want := 0
	for _, n := range list {
		if n.Enabled {
			want++
		}
	}
	return have == want
}

func (s *server) handleRouteCreate(w http.ResponseWriter, r *http.Request) {
	var in routeIn
	if !decodeBody(w, r, &in) {
		return
	}
	n := &in.StaticRoute
	if !validateRoute(w, n, s.routeIfaceSubnets(r.Context())) {
		return
	}
	n.UUID = newUUID()
	_, err := s.db.Exec(`INSERT INTO nw_routes
		(uuid, name, family, iface, target, gateway, metric, enabled, notes)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		n.UUID, n.Name, n.Family, n.Iface, n.Target, n.Gateway, n.Metric,
		enabledIntOf(in.Enabled), n.Notes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	nn, _ := scanRoute(s.db.QueryRow(`SELECT `+routeCols+` FROM nw_routes WHERE uuid=?`, n.UUID))
	writeJSON(w, http.StatusCreated, nn)
}

func (s *server) handleRouteUpdate(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	var in routeIn
	if !decodeBody(w, r, &in) {
		return
	}
	n := &in.StaticRoute
	if !validateRoute(w, n, s.routeIfaceSubnets(r.Context())) {
		return
	}
	res, err := s.db.Exec(`UPDATE nw_routes SET name=?, family=?, iface=?,
		target=?, gateway=?, metric=?, enabled=?, notes=?,
		updated_at=datetime('now') WHERE uuid=?`,
		n.Name, n.Family, n.Iface, n.Target, n.Gateway, n.Metric,
		enabledIntOf(in.Enabled), n.Notes, uuid)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if k, _ := res.RowsAffected(); k == 0 {
		writeErr(w, http.StatusNotFound, "ruta ne postoji")
		return
	}
	nn, _ := scanRoute(s.db.QueryRow(`SELECT `+routeCols+` FROM nw_routes WHERE uuid=?`, uuid))
	writeJSON(w, http.StatusOK, nn)
}

func (s *server) handleRouteDelete(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.Exec(`DELETE FROM nw_routes WHERE uuid=?`, r.PathValue("uuid"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if k, _ := res.RowsAffected(); k == 0 {
		writeErr(w, http.StatusNotFound, "ruta ne postoji")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

/* ---------- primjena ---------- */

func (s *server) handleRouteApply(w http.ResponseWriter, r *http.Request) {
	list, err := s.routeList()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	backupName, err := s.backupConfig(networkConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}
	cfg, err := uciGetConfig(r.Context(), "network")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	var b strings.Builder
	// stare sag_rt_ sekcije se brišu i ispisuju iznova — tako nema zaostalih
	for name := range cfg {
		if strings.HasPrefix(name, routePrefix) {
			fmt.Fprintf(&b, "delete network.%s\n", name)
		}
	}
	idx := 0
	for _, n := range list {
		if !n.Enabled {
			continue
		}
		idx++
		sec := fmt.Sprintf("%s%02d%s", routePrefix, idx,
			strings.ReplaceAll(n.UUID, "-", "")[:6])
		kind := "route"
		if n.Family == "ipv6" {
			kind = "route6"
		}
		fmt.Fprintf(&b, "set network.%s=%s\n", sec, kind)
		fmt.Fprintf(&b, "set network.%s.interface=%s\n", sec, uciQuote(n.Iface))
		fmt.Fprintf(&b, "set network.%s.target=%s\n", sec, n.Target)
		if n.Gateway != "" {
			fmt.Fprintf(&b, "set network.%s.gateway=%s\n", sec, n.Gateway)
		}
		if n.Metric > 0 {
			fmt.Fprintf(&b, "set network.%s.metric=%d\n", sec, n.Metric)
		}
	}
	b.WriteString("commit network\n")

	if err := uciBatch(r.Context(), b.String()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := serviceReload(r.Context(), "network", "reload"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// safe mode: kriva ruta zna presjeći pristup samom uređaju, pa se
	// konfiguracija sama vraća ako se nitko ne javi
	s.scheduleRollback("statičke rute",
		map[string]string{networkConfig: backupName},
		[][2]string{{"network", "reload"}})

	addEvent(s, "info", fmt.Sprintf("Primijenjene statičke rute (%d)", idx))
	writeJSON(w, http.StatusOK, map[string]any{
		"applied": idx, "backup": backupName, "safe_mode": true,
	})
}
