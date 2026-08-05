package main

import (
	"testing"
)

// Preklapanje mreža je cijeli razlog zašto se veza ured–ured ruši u praksi:
// obje poslovnice imaju 192.168.1.0/24 i ništa ne radi. Ova provjera mora
// hvatati i djelomično preklapanje, ne samo istovjetne mreže.
func TestNetsOverlap(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"192.168.1.0/24", "192.168.1.0/24", true},   // ista mreža
		{"192.168.1.0/24", "192.168.1.128/25", true}, // druga je dio prve
		{"192.168.0.0/16", "192.168.50.0/24", true},  // prva sadrži drugu
		{"10.0.0.0/8", "10.7.0.0/24", true},
		{"192.168.1.0/24", "192.168.2.0/24", false},
		{"10.7.0.0/24", "192.168.50.0/24", false},
		{"172.16.0.0/12", "172.32.0.0/16", false}, // susjedno, ali izvan
	}
	for _, c := range cases {
		got := netsOverlap(mustNet(t, c.a), mustNet(t, c.b))
		if got != c.want {
			t.Errorf("%s vs %s: dobiveno %v, očekivano %v", c.a, c.b, got, c.want)
		}
		// preklapanje je simetrično — ako vrijedi u jednom smjeru, vrijedi i obratno
		if netsOverlap(mustNet(t, c.b), mustNet(t, c.a)) != got {
			t.Errorf("%s vs %s: rezultat ovisi o redoslijedu", c.a, c.b)
		}
	}
}

func TestParseSubnetList(t *testing.T) {
	// adresa uređaja se svodi na mrežnu adresu, inače bi ista mreža upisana
	// na dva načina prošla kao dvije različite
	nets, err := parseSubnetList("192.168.60.5/24, 10.20.0.0/16 ,  ")
	if err != nil {
		t.Fatal(err)
	}
	if got := netListString(nets); got != "192.168.60.0/24, 10.20.0.0/16" {
		t.Errorf("dobiveno %q", got)
	}

	// sav promet kroz tunel nije veza ured-ured nego preusmjeravanje interneta
	if _, err := parseSubnetList("0.0.0.0/0"); err == nil {
		t.Error("0.0.0.0/0 je prošao")
	}
	// prazan popis nema smisla — ne bi se znalo što ide kroz tunel
	if _, err := parseSubnetList("   "); err == nil {
		t.Error("prazan popis je prošao")
	}
	for _, bad := range []string{"192.168.1.0", "192.168.1.0/33", "necemreza/24",
		"2001:db8::/32"} {
		if _, err := parseSubnetList(bad); err == nil {
			t.Errorf("neispravan unos %q je prošao", bad)
		}
	}
}

func TestSplitEndpoint(t *testing.T) {
	cases := []struct {
		in   string
		host string
		port int
		bad  bool
	}{
		{"", "", 0, false},
		{"vpn.tvrtka.hr", "vpn.tvrtka.hr", 0, false},
		{"vpn.tvrtka.hr:51821", "vpn.tvrtka.hr", 51821, false},
		{"81.2.3.4", "81.2.3.4", 0, false},
		{"81.2.3.4:1234", "81.2.3.4", 1234, false},
		{"VPN.Tvrtka.HR", "vpn.tvrtka.hr", 0, false},
		{"vpn.tvrtka.hr:0", "", 0, true},
		{"vpn.tvrtka.hr:99999", "", 0, true},
		{"ne valja ovo", "", 0, true},
	}
	for _, c := range cases {
		h, p, err := splitEndpoint(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("%q je prošao, a ne bi smio", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if h != c.host || p != c.port {
			t.Errorf("%q: dobiveno %q/%d, očekivano %q/%d", c.in, h, p, c.host, c.port)
		}
	}
}

func TestUciList(t *testing.T) {
	// uci istu opciju vraća čas kao tekst, čas kao popis
	if got := uciList(uciSection{"addresses": "10.7.0.1/24"}, "addresses"); len(got) != 1 ||
		got[0] != "10.7.0.1/24" {
		t.Errorf("jedna vrijednost: %v", got)
	}
	got := uciList(uciSection{"addresses": []any{"10.7.0.1/24", "10.8.0.1/24"}}, "addresses")
	if len(got) != 2 || got[1] != "10.8.0.1/24" {
		t.Errorf("popis: %v", got)
	}
	if got := uciList(uciSection{}, "addresses"); got != nil {
		t.Errorf("nepostojeća opcija: %v", got)
	}
	if got := uciList(uciSection{"addresses": ""}, "addresses"); got != nil {
		t.Errorf("prazna opcija: %v", got)
	}
}
