package main

import (
	"net"
	"testing"
)

func mustNet(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatalf("neispravan CIDR %s: %v", cidr, err)
	}
	return n
}

func TestNextFreeTunnelIP(t *testing.T) {
	n := mustNet(t, "10.7.0.0/24")

	if got := nextFreeTunnelIP(n, map[string]bool{}); got != "10.7.0.2" {
		t.Errorf("prazna mreža: %q, očekivano 10.7.0.2", got)
	}
	used := map[string]bool{"10.7.0.2": true, "10.7.0.3": true, "10.7.0.5": true}
	if got := nextFreeTunnelIP(n, used); got != "10.7.0.4" {
		t.Errorf("rupa u nizu: %q, očekivano 10.7.0.4", got)
	}
	// popunjena mreža nema što ponuditi
	full := map[string]bool{}
	for i := 2; i <= 254; i++ {
		full[uintToIPv4(0x0A070000+uint32(i))] = true
	}
	if got := nextFreeTunnelIP(n, full); got != "" {
		t.Errorf("popunjena mreža: %q, očekivano prazno", got)
	}
	if got := nextFreeTunnelIP(nil, nil); got != "" {
		t.Errorf("bez mreže: %q, očekivano prazno", got)
	}
	// /30 nema mjesta za klijenta uz rezervirane adrese
	if got := nextFreeTunnelIP(mustNet(t, "10.7.0.0/30"), nil); got != "" {
		t.Errorf("/30: %q, očekivano prazno", got)
	}
}

func TestTunnelIPReserved(t *testing.T) {
	n := mustNet(t, "10.7.0.0/24")
	cases := map[string]bool{
		"10.7.0.0":   true,  // mrežna adresa
		"10.7.0.1":   true,  // uređaj u tunelu
		"10.7.0.255": true,  // broadcast
		"10.7.0.2":   false, // prvi klijent
		"10.7.0.254": false, // zadnji upotrebljiv
	}
	for ip, want := range cases {
		if got := tunnelIPReserved(n, net.ParseIP(ip)); got != want {
			t.Errorf("%s: %v, očekivano %v", ip, got, want)
		}
	}
	// bez poznate mreže se ne odbija ništa
	if tunnelIPReserved(nil, net.ParseIP("10.7.0.1")) {
		t.Error("bez mreže ne smije javiti rezerviranost")
	}
}
