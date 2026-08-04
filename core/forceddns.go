package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Prisilni DNS.
//
// Blokada domena (adblock) i lokalna imena vrijede samo dok uređaji koriste
// DNS ovog uređaja. Dovoljno je da netko na svom računalu upiše 8.8.8.8 i
// filtar više ne postoji. Ovime se sav DNS promet iz odabranih mreža
// **preusmjeri natrag na uređaj**, bez obzira što je postavljeno na računalu.
//
// Tri sloja, jer se DNS danas zaobilazi na tri načina:
//  1. obični DNS (port 53) — preusmjerava se na uređaj
//  2. DNS preko TLS-a, DoT (port 853) — odbija se
//  3. DNS preko HTTPS-a, DoH (port 443 prema poznatim javnim poslužiteljima)
//     — odbija se po adresi
//
// Treći sloj je jedini koji nije potpun i to je pošteno reći: DoH radi na
// istom portu kao i običan web, pa se može blokirati samo poznatim javnim
// poslužiteljima. Preglednik koji koristi vlastiti DoH preko CDN-a ovime se ne
// zaustavlja — za to bi trebalo pregledavati sadržaj prometa.

const fdnsPrefix = "sag_fdns_"
const fdnsSetName = "sag_fdns_skip"

// fdnsDoHResolvers su adrese poznatih javnih DNS poslužitelja koji nude DoH.
// Popis je namjerno kratak i drži se velikih, stabilnih anycast adresa —
// dugačak popis bi se raspao za par mjeseci i davao lažan osjećaj pokrivenosti.
var fdnsDoHResolvers = []string{
	"8.8.8.8", "8.8.4.4", // Google
	"1.1.1.1", "1.0.0.1", "1.1.1.2", "1.0.0.2", "1.1.1.3", "1.0.0.3", // Cloudflare
	"9.9.9.9", "149.112.112.112", "9.9.9.10", "149.112.112.10", // Quad9
	"94.140.14.14", "94.140.15.15", "94.140.14.15", "94.140.15.16", // AdGuard
	"208.67.222.222", "208.67.220.220", // OpenDNS
	"45.90.28.0/24", "45.90.30.0/24", // NextDNS
	"194.242.2.2", // Mullvad
	"76.76.2.0/24", // ControlD
}

type forcedDNS struct {
	Enabled  bool     `json:"enabled"`
	Zones    []string `json:"zones"`     // mreže na koje se primjenjuje
	BlockDoT bool     `json:"block_dot"` // port 853
	BlockDoH bool     `json:"block_doh"` // 443 prema poznatim poslužiteljima
	Except   []string `json:"except"`    // adrese koje smiju koristiti svoj DNS
}

func (s *server) forcedDNSGet() forcedDNS {
	return forcedDNS{
		Enabled:  s.getSetting("fdns_enabled", "0") == "1",
		Zones:    strings.Fields(s.getSetting("fdns_zones", "")),
		BlockDoT: s.getSetting("fdns_dot", "1") == "1",
		BlockDoH: s.getSetting("fdns_doh", "1") == "1",
		Except:   strings.Fields(s.getSetting("fdns_except", "")),
	}
}

// fdnsLocalZones vraća zone na koje prisilni DNS ima smisla — sve osim onih
// koje gledaju prema internetu. Preusmjeravanje DNS-a s WAN strane bi značilo
// da uređaj odgovara na tuđe upite s interneta.
func (s *server) fdnsLocalZones(ctx context.Context) []string {
	out := []string{}
	cfg, err := uciGetConfig(ctx, "firewall")
	if err != nil {
		return out
	}
	for _, sec := range cfg {
		if sectStr(sec, ".type") != "zone" {
			continue
		}
		name := sectStr(sec, "name")
		if name == "" || name == "wan" || strings.HasPrefix(name, "wan") {
			continue
		}
		if sectStr(sec, "masq") == "1" {
			continue // zona koja maskira izlaz je izlazna, ne lokalna
		}
		out = append(out, name)
	}
	return out
}

func (s *server) handleForcedDNSGet(w http.ResponseWriter, r *http.Request) {
	cur := s.forcedDNSGet()
	writeJSON(w, http.StatusOK, map[string]any{
		"forced":     cur,
		"zones":      s.fdnsLocalZones(r.Context()),
		"doh_count":  len(fdnsDoHResolvers),
		"doh_list":   fdnsDoHResolvers,
		"applied":    s.fdnsApplied(r.Context(), cur),
		"doh_note":   "DoH se može blokirati samo poznatim javnim poslužiteljima",
		"set_family": "iznimke su IPv4 adrese ili mreže",
	})
}

// fdnsApplied javlja odgovara li stanje u konfiguraciji uređaja onome što je
// spremljeno — da sučelje zna treba li primjena.
func (s *server) fdnsApplied(ctx context.Context, cur forcedDNS) bool {
	cfg, err := uciGetConfig(ctx, "firewall")
	if err != nil {
		return false
	}
	have := 0
	for name, sec := range cfg {
		if strings.HasPrefix(name, fdnsPrefix) && sectStr(sec, ".type") == "redirect" {
			have++
		}
	}
	want := 0
	if cur.Enabled {
		want = len(cur.Zones)
	}
	return have == want
}

func (s *server) handleForcedDNSSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled  *bool    `json:"enabled"`
		Zones    []string `json:"zones"`
		BlockDoT *bool    `json:"block_dot"`
		BlockDoH *bool    `json:"block_doh"`
		Except   []string `json:"except"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	local := map[string]bool{}
	for _, z := range s.fdnsLocalZones(r.Context()) {
		local[z] = true
	}
	zones := []string{}
	for _, z := range in.Zones {
		z = strings.TrimSpace(z)
		if z == "" {
			continue
		}
		if len(local) > 0 && !local[z] {
			writeErr(w, http.StatusBadRequest, "zona "+z+
				" nije lokalna mreža — prisilni DNS se ne postavlja prema internetu")
			return
		}
		zones = append(zones, z)
	}
	if in.Enabled != nil && *in.Enabled && len(zones) == 0 {
		writeErr(w, http.StatusBadRequest,
			"odaberi barem jednu mrežu na koju se prisilni DNS primjenjuje")
		return
	}
	except := []string{}
	for _, e := range in.Except {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		ip := net.ParseIP(e)
		if ip == nil {
			if _, _, err := net.ParseCIDR(e); err != nil {
				writeErr(w, http.StatusBadRequest,
					"iznimka "+e+" nije IP adresa ni mreža")
				return
			}
		} else if ip.To4() == nil {
			writeErr(w, http.StatusBadRequest,
				"iznimke su za sada samo IPv4 adrese ("+e+")")
			return
		}
		except = append(except, e)
	}

	if in.Enabled != nil {
		s.setSetting("fdns_enabled", boolSetting(*in.Enabled))
	}
	if in.BlockDoT != nil {
		s.setSetting("fdns_dot", boolSetting(*in.BlockDoT))
	}
	if in.BlockDoH != nil {
		s.setSetting("fdns_doh", boolSetting(*in.BlockDoH))
	}
	s.setSetting("fdns_zones", strings.Join(zones, " "))
	s.setSetting("fdns_except", strings.Join(except, " "))
	s.handleForcedDNSGet(w, r)
}

// writeForcedDNSSections ispisuje uci naredbe. Poziva se iz primjene
// firewalla, unutar iste transakcije i backupa — kao i SNAT.
//
// Iznimke se rješavaju imenovanim skupom adresa (`config ipset`) jer fw4 u
// redirect sekciji ne prima listu u `src_ip` (ista granica kao kod SNAT-a);
// preko skupa nastane `ip saddr != @sag_fdns_skip`, što radi za bilo koji
// broj iznimki.
func (s *server) writeForcedDNSSections(b *strings.Builder) {
	cur := s.forcedDNSGet()
	if !cur.Enabled || len(cur.Zones) == 0 {
		return
	}
	skip := ""
	if len(cur.Except) > 0 {
		sec := fdnsPrefix + "skip"
		fmt.Fprintf(b, "set firewall.%s=ipset\n", sec)
		fmt.Fprintf(b, "set firewall.%s.name=%s\n", sec, fdnsSetName)
		fmt.Fprintf(b, "set firewall.%s.match=src_ip\n", sec)
		for _, e := range cur.Except {
			fmt.Fprintf(b, "add_list firewall.%s.entry=%s\n", sec, e)
		}
		skip = "!" + fdnsSetName
	}

	for i, z := range cur.Zones {
		// 1. obični DNS natrag na uređaj
		sn := fmt.Sprintf("%sr%02d", fdnsPrefix, i+1)
		fmt.Fprintf(b, "set firewall.%s=redirect\n", sn)
		fmt.Fprintf(b, "set firewall.%s.name=%s\n", sn,
			uciQuote("Prisilni DNS ("+z+")"))
		fmt.Fprintf(b, "set firewall.%s.src=%s\n", sn, z)
		fmt.Fprintf(b, "set firewall.%s.src_dport=53\n", sn)
		fmt.Fprintf(b, "set firewall.%s.proto=%s\n", sn, uciQuote("tcp udp"))
		fmt.Fprintf(b, "set firewall.%s.target=DNAT\n", sn)
		if skip != "" {
			fmt.Fprintf(b, "set firewall.%s.ipset=%s\n", sn, skip)
		}

		// 2. DNS preko TLS-a
		if cur.BlockDoT {
			tn := fmt.Sprintf("%st%02d", fdnsPrefix, i+1)
			fmt.Fprintf(b, "set firewall.%s=rule\n", tn)
			fmt.Fprintf(b, "set firewall.%s.name=%s\n", tn,
				uciQuote("Blokada DoT ("+z+")"))
			fmt.Fprintf(b, "set firewall.%s.src=%s\n", tn, z)
			fmt.Fprintf(b, "set firewall.%s.dest=wan\n", tn)
			fmt.Fprintf(b, "set firewall.%s.proto=tcp\n", tn)
			fmt.Fprintf(b, "set firewall.%s.dest_port=853\n", tn)
			fmt.Fprintf(b, "set firewall.%s.target=REJECT\n", tn)
			if skip != "" {
				fmt.Fprintf(b, "set firewall.%s.ipset=%s\n", tn, skip)
			}
		}

		// 3. DNS preko HTTPS-a, po adresama poznatih poslužitelja
		if cur.BlockDoH {
			hn := fmt.Sprintf("%sh%02d", fdnsPrefix, i+1)
			fmt.Fprintf(b, "set firewall.%s=rule\n", hn)
			fmt.Fprintf(b, "set firewall.%s.name=%s\n", hn,
				uciQuote("Blokada DoH ("+z+")"))
			fmt.Fprintf(b, "set firewall.%s.src=%s\n", hn, z)
			fmt.Fprintf(b, "set firewall.%s.dest=wan\n", hn)
			fmt.Fprintf(b, "set firewall.%s.proto=tcp\n", hn)
			fmt.Fprintf(b, "set firewall.%s.dest_port=443\n", hn)
			for _, a := range fdnsDoHResolvers {
				fmt.Fprintf(b, "add_list firewall.%s.dest_ip=%s\n", hn, a)
			}
			fmt.Fprintf(b, "set firewall.%s.target=REJECT\n", hn)
			if skip != "" {
				fmt.Fprintf(b, "set firewall.%s.ipset=%s\n", hn, skip)
			}
		}
	}
}
