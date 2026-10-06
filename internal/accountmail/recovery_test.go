package accountmail

import (
	"regexp"
	"strings"
	"testing"
)

const recoverURL = "https://api.example.test/auth/reset-password?token=t1&type=recovery&redirect_to="

func recoverySrc(t *testing.T) []byte {
	t.Helper()
	src, err := Template("recovery")
	if err != nil {
		t.Fatalf("Template(recovery): %v", err)
	}
	if len(src) == 0 {
		t.Fatal("Template(recovery) returned no bytes")
	}
	return src
}

// renderRecovery executes the template with GoTrue's data, the recovery link in place of the signup one.
func renderRecovery(t *testing.T, email string, data jsonMap, withDataKey bool) string {
	t.Helper()
	d := gotrueData(email, data, withDataKey)
	d["ConfirmationURL"] = recoverURL
	return renderSrc(t, recoverySrc(t), d)
}

func TestRecoveryTemplate_ParsesAndRendersAsGoTrueDoes(t *testing.T) {
	src := recoverySrc(t)
	cases := []struct {
		name string
		data map[string]any
	}{
		{"answers", gotrueData(adaEmail, reg("display_name", "Ada Obi", "workspace_name", "Obi Partners"), true)},
		{"nil Data map", gotrueData(adaEmail, nil, true)},
		{"no Data key", gotrueData(adaEmail, nil, false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := renderSrc(t, src, tc.data)
			if !strings.HasPrefix(out, "<!DOCTYPE html>") {
				t.Errorf("output starts %q, want <!DOCTYPE html>", out[:min(len(out), 40)])
			}
		})
	}
}

func TestRecoveryTemplate_CarriesItsCopy(t *testing.T) {
	got := text(renderRecovery(t, adaEmail, jsonMap{}, true))
	for _, want := range []string{
		"Reset your ASComply password", "Password reset", "Reset your password.", "Choose a new password",
		"This link expires in 24 hours.", "If you did not ask for this, ignore this email.",
		"You received this email because a password reset was requested for this address.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("mail does not hold %q", want)
		}
	}
	if strings.Contains(got, "What happens next") {
		t.Error(`mail holds the "What happens next" block`)
	}
}

func TestRecoveryTemplate_ActionURLIsTheButtonAndTheFallbackLink(t *testing.T) {
	out := renderRecovery(t, adaEmail, jsonMap{}, true)
	var linked []anchor
	for _, a := range anchors(out) {
		if a.href == recoverURL {
			linked = append(linked, a)
		}
	}
	if len(linked) != 2 {
		t.Fatalf("anchors with href %s = %+v, want the button and the fallback", recoverURL, linked)
	}
	var fallback, button int
	for _, a := range linked {
		switch a.text {
		case recoverURL:
			fallback++
		case "Choose a new password":
			button++
		}
	}
	if fallback != 1 || button != 1 {
		t.Errorf("anchors = %+v, want one button labelled %q and one fallback whose text is the href", linked, "Choose a new password")
	}
}

func TestRecoveryTemplate_ShowsNoRegistrationName(t *testing.T) {
	const email = "a&b@obi.test"
	const intro = "A password reset was requested for the ASComply account a&b@obi.test"
	answers := reg("display_name", "Zelda Quill", "workspace_name", "Quill Holdings")
	for _, tc := range []struct {
		name string
		data jsonMap
		keep bool
	}{{"with Data", answers, true}, {"without Data", nil, false}} {
		t.Run(tc.name, func(t *testing.T) {
			out := renderRecovery(t, email, tc.data, tc.keep)
			if !strings.Contains(text(out), intro) {
				t.Errorf("mail does not hold %q", intro)
			}
			for _, leak := range []string{"Zelda", "Quill"} {
				if strings.Contains(out, leak) {
					t.Errorf("mail holds the registration answer %q", leak)
				}
			}
			if n := strings.Count(out, "a&amp;b@obi.test"); n < 2 {
				t.Errorf("escaped address appears %d times, want in the intro and the Email row", n)
			}
			if strings.Contains(out, "a&b@obi.test") {
				t.Error("raw output holds the unescaped address")
			}
			if _, v := mustRow(t, out, "Email"); v != "a&amp;b@obi.test" {
				t.Errorf("Email row = %q, want the escaped address", v)
			}
		})
	}
}

func TestRecoveryTemplate_KeepsTheLayoutRules(t *testing.T) {
	out := renderRecovery(t, strings.Repeat("e", 111)+"@obi.test", jsonMap{}, true)
	imgs := imgRe.FindAllStringSubmatch(out, -1)
	if len(imgs) != 2 {
		t.Fatalf("mail holds %d <img>, want 2 (header and footer)", len(imgs))
	}
	for _, m := range imgs {
		if text(m[1]) != LogoURL {
			t.Errorf("img src = %q, want %q", m[1], LogoURL)
		}
	}
	var found bool
	for _, m := range regexp.MustCompile(`<table\b[^>]*>`).FindAllString(out, -1) {
		if strings.Contains(m, `width="600"`) {
			found = true
			if s := styleRe.FindStringSubmatch(m); s == nil || !strings.Contains(s[1], "max-width:600px") {
				t.Errorf("container table %s lacks max-width:600px in its style", m)
			}
		}
	}
	if !found {
		t.Error(`no table carries width="600"`)
	}
	style, _ := mustRow(t, out, "Email")
	if !strings.Contains(style, "overflow-wrap:anywhere") {
		t.Errorf("Email value cell style %q lacks overflow-wrap:anywhere", style)
	}
}
