package gateway

import (
	"slices"
	"strings"
	"testing"
)

// d3Domains is the minimum list Decision D3 records; freeMailDomains may grow past it.
var d3Domains = []string{
	"gmail.com", "googlemail.com",
	"outlook.com", "hotmail.com", "live.com", "msn.com",
	"yahoo.com", "ymail.com", "rocketmail.com",
	"icloud.com", "me.com", "mac.com",
	"aol.com",
	"proton.me", "protonmail.com",
	"gmx.com", "gmx.net", "mail.com",
	"yandex.com",
	"zohomail.com",
}

func requireRecordedMinimum(t *testing.T) {
	t.Helper()
	if len(freeMailDomains) < len(d3Domains) {
		t.Fatalf("len(freeMailDomains) = %d, want >= %d (D3)", len(freeMailDomains), len(d3Domains))
	}
}

func TestIsFreeMail(t *testing.T) {
	// Each false row guards against over-blocking; its paired true row keeps it from passing vacuously.
	for _, c := range []struct {
		name   string
		inputs []string
		want   bool
	}{
		{"listed", []string{"user@gmail.com", "user@yahoo.com"}, true},
		{"subdomain", []string{"user@mail.gmail.com", "user@a.b.outlook.com"}, true},
		{"case", []string{"USER@GMAIL.COM", "user@GMail.Com"}, true},
		{"whitespace", []string{"  user@gmail.com \t\n"}, true},
		{"plus_tag", []string{"user+tag@gmail.com"}, true},
		{"lookalike_not_blocked", []string{"user@gmai1.com"}, false},
		{"dot_boundary", []string{"user@evilgmail.com", "user@gmail.com.corp.example"}, false},
		{"business", []string{"user@corp.example"}, false},
		{"empty_domain", []string{"user@", "", "usergmail.com", "   "}, false},
		{"trailing_dot", []string{"user@gmail.com.", "user@gmail.com.."}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, in := range c.inputs {
				if got := isFreeMail(in); got != c.want {
					t.Errorf("isFreeMail(%q) = %v, want %v", in, got, c.want)
				}
			}
		})
	}

	t.Run("last_at", func(t *testing.T) {
		for _, c := range []struct {
			name string
			in   string
			want bool
		}{
			{"quoted_local_gmail", `"a@gmail.com"@corp.example`, false},
			{"final_domain_gmail", "user@corp.example@gmail.com", true},
		} {
			t.Run(c.name, func(t *testing.T) {
				if got := isFreeMail(c.in); got != c.want {
					t.Errorf("isFreeMail(%q) = %v, want %v", c.in, got, c.want)
				}
			})
		}
	})
}

func TestIsFreeMail_EveryListedDomainAndSubdomain(t *testing.T) {
	requireRecordedMinimum(t)
	for _, d := range freeMailDomains {
		for _, in := range []string{"x@" + d, "x@sub." + d} {
			if !isFreeMail(in) {
				t.Errorf("isFreeMail(%q) = false, want true", in)
			}
		}
	}
}

func TestFreeMailDomains_WellFormed(t *testing.T) {
	requireRecordedMinimum(t)
	if !slices.Contains(freeMailDomains, "gmail.com") {
		t.Fatal(`freeMailDomains lacks "gmail.com" (control)`)
	}
	seen := map[string]bool{}
	for _, d := range freeMailDomains {
		if d != strings.ToLower(d) {
			t.Errorf("%q is not lower-case", d)
		}
		if d != strings.TrimSpace(d) {
			t.Errorf("%q has surrounding whitespace", d)
		}
		if strings.Contains(d, "@") {
			t.Errorf("%q contains @", d)
		}
		if strings.HasPrefix(d, ".") || strings.HasSuffix(d, ".") {
			t.Errorf("%q has a leading or trailing dot", d)
		}
		if !strings.Contains(d, ".") {
			t.Errorf("%q has no dot", d)
		}
		if seen[d] {
			t.Errorf("%q appears more than once", d)
		}
		seen[d] = true
	}
}

func TestFreeMailDomains_HoldsRecordedMinimum(t *testing.T) {
	for _, d := range d3Domains {
		if !slices.Contains(freeMailDomains, d) {
			t.Errorf("freeMailDomains lacks %q (D3)", d)
		}
	}
}
