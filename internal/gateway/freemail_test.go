package gateway

import (
	"slices"
	"strings"
	"testing"
)

// recordedDomains is the recorded minimum; freeMailDomains may grow past it.
var recordedDomains = []string{
	"gmail.com", "googlemail.com",
	"outlook.com", "hotmail.com", "live.com", "msn.com",
	"hotmail.co.uk", "hotmail.fr", "hotmail.de", "hotmail.es", "hotmail.it",
	"live.co.uk", "live.fr", "outlook.fr", "outlook.de", "outlook.es",
	"yahoo.com", "ymail.com", "rocketmail.com",
	"yahoo.co.uk", "yahoo.fr", "yahoo.de", "yahoo.es", "yahoo.it",
	"yahoo.ca", "yahoo.com.au", "yahoo.co.in",
	"icloud.com", "me.com", "mac.com",
	"aol.com", "aim.com",
	"proton.me", "protonmail.com", "pm.me", "protonmail.ch",
	"gmx.com", "gmx.net", "mail.com",
	"yandex.com",
	"zohomail.com",
}

func requireRecordedMinimum(t *testing.T) {
	t.Helper()
	if len(freeMailDomains) < len(recordedDomains) {
		t.Fatalf("len(freeMailDomains) = %d, want >= %d", len(freeMailDomains), len(recordedDomains))
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
		{"multi_label_suffix", []string{"user@yahoo.co.uk", "user@mail.yahoo.com.au"}, true},
		{"case", []string{"USER@GMAIL.COM", "user@GMail.Com"}, true},
		{"whitespace", []string{"  user@gmail.com \t\n"}, true},
		{"plus_tag", []string{"user+tag@gmail.com"}, true},
		{"lookalike_not_blocked", []string{"user@gmai1.com"}, false},
		{"dot_boundary", []string{"user@evilgmail.com", "user@gmail.com.corp.example"}, false},
		{"multi_label_near_miss", []string{"user@notyahoo.co.uk"}, false},
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
	for _, d := range recordedDomains {
		if !slices.Contains(freeMailDomains, d) {
			t.Errorf("freeMailDomains lacks %q", d)
		}
	}
}

func TestIsFreeMail_Adversarial(t *testing.T) {
	for _, c := range []struct {
		name   string
		inputs []string
		want   bool
	}{
		{"upper_trailing_dot", []string{"USER@GMAIL.COM.", "User@Mail.Gmail.Com.."}, true},
		{"subdomain_trailing_dots", []string{"user@mail.gmail.com...", "user@a.b.outlook.com."}, true},
		{"whitespace_and_trailing_dot", []string{" \tuser@gmail.com.\n", " user@gmail.com "}, true},
		{"whitespace_in_local_part", []string{"us er@gmail.com"}, true},
		{"listed_domain_as_local_part", []string{"gmail.com@corp.example", "x.gmail.com@corp.example"}, false},
		{"near_miss_trailing_dot", []string{"user@evilgmail.com.", "user@gmail.com.corp.example."}, false},
		{"dots_only_domain", []string{"user@.", "user@..."}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, in := range c.inputs {
				if got := isFreeMail(in); got != c.want {
					t.Errorf("isFreeMail(%q) = %v, want %v", in, got, c.want)
				}
			}
		})
	}
}

func TestIsFreeMail_EveryListedDomainUpperCaseTrailingDot(t *testing.T) {
	requireRecordedMinimum(t)
	for _, d := range freeMailDomains {
		for _, in := range []string{" X@" + strings.ToUpper(d) + ". ", "x@SUB." + strings.ToUpper(d) + ".."} {
			if !isFreeMail(in) {
				t.Errorf("isFreeMail(%q) = false, want true", in)
			}
		}
	}
}
