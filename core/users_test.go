package main

import (
	"net/http"
	"testing"
)

// Ovlasti su jedino mjesto gdje greška znači da netko može više nego smije,
// pa se provjeravaju iscrpno — po ulozi, metodi i putanji.
func TestPermitted(t *testing.T) {
	cases := []struct {
		role, method, path string
		want               bool
	}{
		// administrator smije sve
		{roleAdmin, http.MethodGet, "/api/v1/system", true},
		{roleAdmin, http.MethodPost, "/api/v1/users", true},
		{roleAdmin, http.MethodPost, "/api/v1/openwrt/flash", true},
		{roleAdmin, http.MethodDelete, "/api/v1/firewall/rules/x", true},

		// operater radi svakodnevni posao…
		{roleOperator, http.MethodGet, "/api/v1/system", true},
		{roleOperator, http.MethodPost, "/api/v1/firewall/apply", true},
		{roleOperator, http.MethodPost, "/api/v1/backup/create", true},
		{roleOperator, http.MethodPut, "/api/v1/routes/x", true},
		// …ali ne dira korisnike, token, lozinku uređaja ni nepovratno
		{roleOperator, http.MethodGet, "/api/v1/users", false},
		{roleOperator, http.MethodPost, "/api/v1/users", false},
		{roleOperator, http.MethodGet, "/api/v1/settings/token", false},
		{roleOperator, http.MethodPost, "/api/v1/settings/token/regenerate", false},
		{roleOperator, http.MethodPost, "/api/v1/system/device-password", false},
		{roleOperator, http.MethodPost, "/api/v1/openwrt/flash", false},
		{roleOperator, http.MethodPost, "/api/v1/openwrt/datapart", false},
		{roleOperator, http.MethodPost, "/api/v1/backup/restore", false},

		// pregled smije samo čitati
		{roleViewer, http.MethodGet, "/api/v1/system", true},
		{roleViewer, http.MethodGet, "/api/v1/connections", true},
		{roleViewer, http.MethodPost, "/api/v1/firewall/apply", false},
		{roleViewer, http.MethodPut, "/api/v1/routes/x", false},
		{roleViewer, http.MethodDelete, "/api/v1/dhcp/leases/x", false},
		{roleViewer, http.MethodGet, "/api/v1/users", false},

		// vlastiti račun i odjava rade uvijek — inače bi se korisnik
		// zaključao van
		{roleViewer, http.MethodPost, "/api/v1/auth/logout", true},
		{roleViewer, http.MethodPost, "/api/v1/auth/password", true},
		{roleViewer, http.MethodGet, "/api/v1/auth/session", true},
		{roleOperator, http.MethodPost, "/api/v1/auth/logout-others", true},

		// nepoznata uloga ne dobiva ništa
		{"", http.MethodGet, "/api/v1/system", false},
		{"superuser", http.MethodGet, "/api/v1/system", false},
	}
	for _, c := range cases {
		got, why := permitted(c.role, c.method, c.path)
		if got != c.want {
			t.Errorf("permitted(%q, %s %s) = %v (%s), očekivano %v",
				c.role, c.method, c.path, got, why, c.want)
		}
	}
}

// Putanja korisnika mora biti zatvorena i za podputanje — /api/v1/users/x/password
// je jednako osjetljiv kao /api/v1/users.
func TestAdminOnlyPathPrefixes(t *testing.T) {
	for _, p := range []string{
		"/api/v1/users", "/api/v1/users/abc", "/api/v1/users/abc/password",
		"/api/v1/users/abc/sessions", "/api/v1/settings/token",
		"/api/v1/settings/token/regenerate",
	} {
		if !adminOnlyPath(p) {
			t.Errorf("%s bi trebao biti samo za administratora", p)
		}
	}
	for _, p := range []string{
		"/api/v1/system", "/api/v1/backup/create", "/api/v1/settings/time",
		"/api/v1/settings/guicert", "/api/v1/openwrt/status",
	} {
		if adminOnlyPath(p) {
			t.Errorf("%s ne bi trebao biti ograničen na administratora", p)
		}
	}
}

func TestValidUsername(t *testing.T) {
	good := []string{"ana", "marko.horvat", "teh-1", "op_2", "a1"}
	bad := []string{"A", "1ana", "ana!", "", "a", ".ana", "ana korisnik",
		"prevelikoprevelikoprevelikoprevelikoime"}
	for _, u := range good {
		if !reUsername.MatchString(u) {
			t.Errorf("odbijeno ispravno ime %q", u)
		}
	}
	for _, u := range bad {
		if reUsername.MatchString(u) {
			t.Errorf("prošlo neispravno ime %q", u)
		}
	}
}
