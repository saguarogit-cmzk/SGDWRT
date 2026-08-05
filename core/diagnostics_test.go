package main

import "testing"

// Redci su stvarni zapisi s uređaja (IN100, OpenWrt 25.12.5) — parser se
// provjerava na onome što jezgra doista ispisuje, ne na izmišljenom formatu.
func TestParseConntrackLine(t *testing.T) {
	tcp := "ipv4     2 tcp      6 37 SYN_SENT src=192.168.50.199 dst=80.253.163.24 sport=61773 dport=443 packets=3 bytes=156 [UNREPLIED] src=80.253.163.24 dst=192.168.50.199 sport=443 dport=61773 packets=0 bytes=0 mark=16128 zone=0 use=2"
	e, ok := parseConntrackLine(tcp)
	if !ok {
		t.Fatal("tcp redak nije prošao")
	}
	if e.Family != "ipv4" || e.Proto != "tcp" || e.State != "SYN_SENT" {
		t.Errorf("family/proto/state = %s/%s/%s", e.Family, e.Proto, e.State)
	}
	if e.Src != "192.168.50.199" || e.Dst != "80.253.163.24" ||
		e.SPort != 61773 || e.DPort != 443 {
		t.Errorf("adrese: %s:%d -> %s:%d", e.Src, e.SPort, e.Dst, e.DPort)
	}
	// prvi bytes= je odlazni smjer, drugi je odgovor — ne smiju se zamijeniti
	if e.OutBytes != 156 || e.InBytes != 0 {
		t.Errorf("out/in = %d/%d", e.OutBytes, e.InBytes)
	}

	udp := "ipv4     2 udp      17 7 src=192.168.50.50 dst=255.255.255.255 sport=48597 dport=10001 packets=2 bytes=120 [UNREPLIED] src=255.255.255.255 dst=192.168.50.50 sport=10001 dport=48597 packets=0 bytes=0 mark=16128 zone=0 use=2"
	e, ok = parseConntrackLine(udp)
	if !ok {
		t.Fatal("udp redak nije prošao")
	}
	if e.Proto != "udp" || e.State != "" {
		t.Errorf("udp proto/state = %s/%q", e.Proto, e.State)
	}
	if e.OutBytes != 120 || e.DPort != 10001 {
		t.Errorf("udp out/dport = %d/%d", e.OutBytes, e.DPort)
	}

	// troznamenkasti timeout je uživo završavao kao "stanje" veze
	est := "ipv4     2 tcp      6 114 ESTABLISHED src=192.168.50.199 dst=192.168.50.222 sport=55513 dport=22 packets=100 bytes=13393458 src=192.168.50.222 dst=192.168.50.199 sport=22 dport=55513 packets=90 bytes=34374 [ASSURED] mark=0 zone=0 use=2"
	e, ok = parseConntrackLine(est)
	if !ok {
		t.Fatal("established redak nije prošao")
	}
	if e.State != "ESTABLISHED" {
		t.Errorf("stanje = %q (broj ne smije biti stanje)", e.State)
	}
	if e.OutBytes != 13393458 || e.InBytes != 34374 {
		t.Errorf("out/in = %d/%d", e.OutBytes, e.InBytes)
	}

	if _, ok := parseConntrackLine(""); ok {
		t.Error("prazan redak je prošao")
	}
	if _, ok := parseConntrackLine("ipv4 2 tcp"); ok {
		t.Error("krnji redak je prošao")
	}
}

func TestSafeCapName(t *testing.T) {
	good := []string{"snimka-20260805-120000.pcap"}
	bad := []string{"../etc/passwd", "snimka-..-x.pcap.gz", "token",
		"snimka-a/..b.pcap", "/opt/saguaro/etc/token"}
	for _, n := range good {
		if !safeCapName(n) {
			t.Errorf("odbijen ispravan naziv %q", n)
		}
	}
	for _, n := range bad {
		if safeCapName(n) {
			t.Errorf("prošao opasan naziv %q", n)
		}
	}
}
