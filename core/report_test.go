package main

import (
	"strings"
	"testing"
	"time"
)

// Izvještaj se šalje za mjesec koji je gotov. Računanje "prethodnog mjeseca"
// se lako pokvari na krajevima mjeseca i na prijelazu godine, a greška se
// primijeti tek za mjesec dana — zato ovdje.
func TestPrevMonth(t *testing.T) {
	cases := []struct{ now, want string }{
		{"2026-08-01", "2026-07"},
		{"2026-08-05", "2026-07"},
		{"2026-08-31", "2026-07"},
		{"2026-03-31", "2026-02"}, // veljača ima manje dana od ožujka
		{"2026-03-01", "2026-02"},
		{"2026-01-01", "2025-12"}, // prijelaz godine
		{"2026-01-31", "2025-12"},
		{"2024-03-01", "2024-02"}, // prijestupna godina
	}
	for _, c := range cases {
		now, err := time.Parse("2006-01-02", c.now)
		if err != nil {
			t.Fatal(err)
		}
		if got := prevMonth(now); got != c.want {
			t.Errorf("%s: dobiveno %s, očekivano %s", c.now, got, c.want)
		}
	}
}

func TestMonthLabel(t *testing.T) {
	for in, want := range map[string]string{
		"2026-01": "siječanj 2026.",
		"2026-07": "srpanj 2026.",
		"2026-12": "prosinac 2026.",
	} {
		if got := monthLabel(in); got != want {
			t.Errorf("%s: dobiveno %q, očekivano %q", in, got, want)
		}
	}
	// neispravan unos se ne smije srušiti, samo se vrati kakav je
	if got := monthLabel("bezveze"); got != "bezveze" {
		t.Errorf("neispravan mjesec: %q", got)
	}
}

func TestFmtMinutes(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "bez prekida"},
		{-5, "bez prekida"},
		{1, "1 min"},
		{59, "59 min"},
		{60, "1 h"},
		{61, "1 h 1 min"},
		{135, "2 h 15 min"},
		{1440, "24 h"},
	}
	for _, c := range cases {
		if got := fmtMinutes(c.in); got != c.want {
			t.Errorf("%d: dobiveno %q, očekivano %q", c.in, got, c.want)
		}
	}
}

func TestFmtBytesHR(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1024, "1 kB"},
		{1536 * 1024, "1.5 MB"},
		{3 * 1024 * 1024 * 1024, "3.0 GB"},
		{2 * 1024 * 1024 * 1024 * 1024, "2.00 TB"},
	}
	for _, c := range cases {
		if got := fmtBytesHR(c.in); got != c.want {
			t.Errorf("%d: dobiveno %q, očekivano %q", c.in, got, c.want)
		}
	}
}

// Poruke u izvještaj dolaze iz dnevnika, a u dnevnik ulaze imena uređaja i
// domena koja Saguaro nije izmislio. Ako se ne pobjegne od HTML-a, dovoljno je
// da netko nazove računalo "<img onerror=...>" pa da to završi u tuđem
// sandučiću kao kod.
func TestReportHTMLEscaping(t *testing.T) {
	d := reportData{
		Month: "2026-07", MonthLabel: "srpanj 2026.",
		Device: `<script>alert(1)</script>`,
		TopEvents: []reportEvent{
			{Message: `Nepoznat uređaj "<img src=x onerror=alert(1)>"`, Count: 3, Last: "2026-07-05 10:00"},
		},
		Monitors: []reportMonitor{{Name: "<b>server</b>", IP: "192.168.1.10", Pct: 99.9}},
		Warnings: []string{"<i>napomena</i>"},
	}
	out := renderReportHTML(d)
	for _, bad := range []string{"<script>", "<img src=x", "<b>server</b>", "<i>napomena</i>"} {
		if strings.Contains(out, bad) {
			t.Errorf("neočišćen HTML u izvještaju: %s", bad)
		}
	}
	for _, want := range []string{"&lt;script&gt;", "srpanj 2026.", "192.168.1.10"} {
		if !strings.Contains(out, want) {
			t.Errorf("izvještaj ne sadrži %q", want)
		}
	}
}

// Prazan mjesec ne smije srušiti sastavljanje ni tvrditi 0 % dostupnosti —
// treba reći da podataka nema.
func TestReportHTMLEmptyMonth(t *testing.T) {
	d := reportData{
		Month: "2026-07", MonthLabel: "srpanj 2026.", DaysTotal: 31,
		Warnings: []string{"Za taj mjesec uređaj nema zapisa"},
	}
	out := renderReportHTML(d)
	if !strings.Contains(out, "nema zapisa") {
		t.Error("izvještaj ne kaže da podataka nema")
	}
	if !strings.Contains(out, "Nijedan uređaj nije pod nadzorom") {
		t.Error("prazan popis nadzora nije objašnjen")
	}
}

func TestBuildMailHTML(t *testing.T) {
	msg := string(buildMailHTML("a@b.hr", []string{"c@d.hr", "e@f.hr"},
		"Izvještaj — srpanj", "tekst", "<p>html</p>"))
	for _, want := range []string{
		"To: c@d.hr, e@f.hr",
		"Content-Type: multipart/alternative",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Type: text/html; charset=utf-8",
		"<p>html</p>",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("poruka ne sadrži %q", want)
		}
	}
	// zaglavlje s našim slovima mora biti kodirano, inače ga poslužitelji odbiju
	if strings.Contains(msg, "Subject: Izvještaj") {
		t.Error("naslov s dijakriticima nije kodiran")
	}
	// granica se mora zatvoriti, inače mail program prikaže smeće
	if !strings.Contains(msg, "--\r\n") {
		t.Error("multipart granica nije zatvorena")
	}
}

// Brojači sučelja samo rastu i pri restartu kreću ispočetka. Prirast se mora
// računati tako da prvi pogled ne upiše sav promet od paljenja uređaja, a
// restart ne upiše negativan broj.
func TestCounterDelta(t *testing.T) {
	db, err := openDB(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &server{db: db}

	// prvi put: samo zapamti polazište, ništa se ne broji
	if rx, tx := s.counterDelta("eth1", 900000000, 400000000); rx != 0 || tx != 0 {
		t.Errorf("prvi pogled je upisao promet: %d/%d", rx, tx)
	}
	// normalan prirast
	if rx, tx := s.counterDelta("eth1", 900001000, 400000500); rx != 1000 || tx != 500 {
		t.Errorf("prirast: dobiveno %d/%d, očekivano 1000/500", rx, tx)
	}
	// uređaj se pokrenuo iznova — brojač je manji nego prije
	if rx, tx := s.counterDelta("eth1", 2000, 700); rx != 2000 || tx != 700 {
		t.Errorf("nakon restarta: dobiveno %d/%d, očekivano 2000/700", rx, tx)
	}
	// svako sučelje ima svoj brojač
	if rx, _ := s.counterDelta("eth2", 5000, 5000); rx != 0 {
		t.Errorf("drugo sučelje nije krenulo od nule: %d", rx)
	}
	if rx, _ := s.counterDelta("eth2", 5500, 5000); rx != 500 {
		t.Errorf("drugo sučelje: %d", rx)
	}
}

// Izlaz naredbe `nlbw -c csv` je unatoč imenu razdvojen tabovima. Prvi zapis
// je pretpostavljao točkazarez, pa tablica potrošnje po uređaju nikad nije
// pokazala nijedan redak — uzorak dolje je stvarni izlaz s uređaja.
func TestParseNlbwCSV(t *testing.T) {
	tabs := "\"ip\"\t\"conns\"\t\"rx_bytes\"\t\"rx_pkts\"\t\"tx_bytes\"\t\"tx_pkts\"\n" +
		"\"192.168.205.50\"\t7771\t6736574\t13188\t996391\t12951\n" +
		"\"192.168.50.222\"\t3352\t281400\t3350\t281064\t3346\n"
	hosts := parseNlbwCSV(tabs, 10)
	if len(hosts) != 2 {
		t.Fatalf("dobiveno %d redaka, očekivano 2", len(hosts))
	}
	if hosts[0].IP != "192.168.205.50" || hosts[0].RxBytes != 6736574 ||
		hosts[0].TxBytes != 996391 || hosts[0].Conns != 7771 {
		t.Errorf("prvi redak krivo pročitan: %+v", hosts[0])
	}

	// ako neka inačica ipak vrati točkazarez, mora raditi i tako
	semis := "\"ip\";\"conns\";\"rx_bytes\";\"rx_pkts\";\"tx_bytes\";\"tx_pkts\"\n" +
		"\"10.0.0.5\";12;500;3;700;4\n"
	hosts = parseNlbwCSV(semis, 10)
	if len(hosts) != 1 || hosts[0].IP != "10.0.0.5" || hosts[0].TxBytes != 700 {
		t.Errorf("točkazarez: %+v", hosts)
	}

	// ograničenje broja redaka
	if got := parseNlbwCSV(tabs, 1); len(got) != 1 {
		t.Errorf("ograničenje ne radi: %d", len(got))
	}
	// prazan i krnji izlaz ne smiju srušiti ništa
	for _, bad := range []string{"", "\n", "\"ip\"\t\"conns\"\n"} {
		if got := parseNlbwCSV(bad, 10); len(got) != 0 {
			t.Errorf("iz %q dobiveno %d redaka", bad, len(got))
		}
	}
}
