package main

import (
	"testing"
	"time"
)

// Službene ispitne vrijednosti iz RFC 6238 (Appendix B), SHA-1, tajna
// "12345678901234567890". Ako ovo prođe, algoritam je točan — a to je dio
// koji ne smije biti pogrešan ni za dlaku.
func TestTOTPRFC6238(t *testing.T) {
	secret := []byte("12345678901234567890")
	cases := []struct {
		unix int64
		want string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}
	for _, c := range cases {
		got := totpCode(secret, uint64(c.unix/totpPeriod), 8)
		if got != c.want {
			t.Errorf("t=%d: dobiveno %s, RFC kaže %s", c.unix, got, c.want)
		}
	}
}

func TestTOTPVerifyWindowAndReplay(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1111111109, 0)
	center := now.Unix() / totpPeriod

	// kod za trenutni korak prolazi
	code := totpCode(secret, uint64(center), totpDigits)
	step, ok := totpVerify(secret, code, now, 0)
	if !ok || step != center {
		t.Fatalf("trenutni kod nije prošao (step=%d, ok=%v)", step, ok)
	}

	// isti kod se NE smije upotrijebiti dvaput — presretnut kod bi inače
	// vrijedio do kraja svojih 30 sekundi
	if _, ok := totpVerify(secret, code, now, step); ok {
		t.Error("isti kod je prošao drugi put")
	}

	// jedan korak prije i poslije prolazi (sat na telefonu nije točan u sekundu)
	for _, d := range []int64{-1, 1} {
		c := totpCode(secret, uint64(center+d), totpDigits)
		if _, ok := totpVerify(secret, c, now, 0); !ok {
			t.Errorf("kod s pomakom %d nije prošao", d)
		}
	}
	// dva koraka izvan prozora ne prolaze
	for _, d := range []int64{-2, 2} {
		c := totpCode(secret, uint64(center+d), totpDigits)
		if _, ok := totpVerify(secret, c, now, 0); ok {
			t.Errorf("kod s pomakom %d je prošao, a ne bi smio", d)
		}
	}

	// smeće ne prolazi
	for _, bad := range []string{"", "123", "abcdef", "1234567", "000000"} {
		if _, ok := totpVerify(secret, bad, now, 0); ok && bad != code {
			t.Errorf("neispravan kod %q je prošao", bad)
		}
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, err := newRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != recoveryCodeCount {
		t.Fatalf("dobiveno %d kodova, očekivano %d", len(codes), recoveryCodeCount)
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if seen[c] {
			t.Errorf("ponovljen kod %q", c)
		}
		seen[c] = true
		if len(c) != 11 { // 5 + crtica + 5
			t.Errorf("neočekivan oblik koda %q", c)
		}
	}
	// sažetak ne smije ovisiti o crticama ni velikim slovima — korisnik ih
	// prepisuje rukom i tipka kako mu dođe
	a := recoveryHash("ab12c-de34f")
	b := recoveryHash("AB12CDE34F")
	if a != b {
		t.Error("sažetak se razlikuje ovisno o crtici i velikim slovima")
	}
	if recoveryHash("ab12c-de34f") == recoveryHash("ab12c-de34e") {
		t.Error("različiti kodovi daju isti sažetak")
	}
}

func TestChallengeTries(t *testing.T) {
	id, err := newChallenge("uuid-1", "marko.horvat", roleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	// krivo utipkan kod ne smije srušiti prijavu — inače korisnik zbog jedne
	// greške mora ponovno upisivati ime i lozinku
	for i := 1; i < totpChallengeTries; i++ {
		if c := claimChallenge(id); c == nil {
			t.Fatalf("međukorak je nestao već nakon %d. pokušaja", i)
		}
	}
	// zadnji dopušteni pokušaj se još provjerava...
	if c := claimChallenge(id); c == nil {
		t.Fatal("zadnji dopušteni pokušaj nije prošao do provjere koda")
	}
	// ...a nakon njega međukorak više ne postoji
	if c := claimChallenge(id); c != nil {
		t.Error("pogađanje se nastavlja i nakon iscrpljenih pokušaja")
	}

	// uspješna prijava troši međukorak odmah
	id2, _ := newChallenge("uuid-2", "ana.kovac", roleViewer)
	if claimChallenge(id2) == nil {
		t.Fatal("novi međukorak ne radi")
	}
	dropChallenge(id2)
	if claimChallenge(id2) != nil {
		t.Error("međukorak vrijedi i nakon uspješne prijave")
	}

	// izmišljeni izazov ne vrijedi
	if claimChallenge("nepostojeci") != nil {
		t.Error("izmišljeni međukorak je prošao")
	}
}

func TestOtpauthURI(t *testing.T) {
	u := otpauthURI("Saguaro Lab Zagreb", "marko.horvat", "JBSWY3DPEHPK3PXP")
	for _, want := range []string{
		"otpauth://totp/",
		"secret=JBSWY3DPEHPK3PXP",
		"digits=6",
		"period=30",
		"algorithm=SHA1",
	} {
		if !contains(u, want) {
			t.Errorf("URI ne sadrži %q: %s", want, u)
		}
	}
	// razmak u nazivu uređaja mora biti kodiran, inače aplikacija odbije URI
	if contains(u, "Saguaro Lab") {
		t.Errorf("razmak nije kodiran: %s", u)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
