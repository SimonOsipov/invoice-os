package accountmail

import (
	"bytes"
	"fmt"
	"html"
	"html/template"
	"regexp"
	"strings"
	"testing"
)

var obiInvite = InviteMail{
	Workspace: "Obi Partners", Inviter: "Ada Obi", Email: "tunde@obi.test", Role: "reviewer", Token: "t0k",
}

var inviteIntroRe = regexp.MustCompile(`(?s)<td\b[^>]*>\s*([^<]*\binvited you to join\b[^<]*)</td>`)

func renderInvite(t *testing.T, m InviteMail) (subject, out string) {
	t.Helper()
	subject, out, err := RenderInvite(m)
	if err != nil {
		t.Fatalf("RenderInvite(%+v): %v", m, err)
	}
	return subject, out
}

func inviteIntro(t *testing.T, out string) string {
	t.Helper()
	m := inviteIntroRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no intro cell holding \"invited you to join\" in:\n%s", out)
	}
	return strings.TrimSpace(html.UnescapeString(m[1]))
}

// labelStyle returns the style of the label cell reading label.
func labelStyle(t *testing.T, out, label string) string {
	t.Helper()
	re := regexp.MustCompile(`<td\b([^>]*)>\s*` + regexp.QuoteMeta(label) + `\s*</td>`)
	m := re.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no label cell %q in:\n%s", label, out)
	}
	s := styleRe.FindStringSubmatch(m[1])
	if s == nil {
		t.Fatalf("label cell %q has no style", label)
	}
	return s[1]
}

func TestInviteTemplate_ComposesTheLayout(t *testing.T) {
	src, err := Template("invite")
	if err != nil {
		t.Fatalf("Template(invite): %v", err)
	}
	layout, err := files.ReadFile("layout.html")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := files.ReadFile("invite.html")
	if err != nil {
		t.Fatalf("invite.html is not embedded: %v", err)
	}
	if len(layout) == 0 || len(inv) == 0 {
		t.Fatalf("layout.html %d bytes, invite.html %d bytes; want both non-empty", len(layout), len(inv))
	}
	if !bytes.HasPrefix(src, layout) {
		t.Error("Template(invite) does not begin with layout.html")
	}
	if !bytes.HasSuffix(src, inv) {
		t.Error("Template(invite) does not end with invite.html")
	}
	if _, err := template.New("invite").Parse(string(src)); err != nil {
		t.Errorf("html/template does not parse Template(invite): %v", err)
	}
}

func TestRenderInvite_NamesWorkspaceInviterAndRole(t *testing.T) {
	_, out := renderInvite(t, obiInvite)

	got := inviteIntro(t, out)
	if !strings.Contains(got, "Ada Obi invited you to join Obi Partners on ASComply as Reviewer.") {
		t.Errorf("intro = %q, want it to name inviter, workspace and role", got)
	}
	if !strings.Contains(got, "tunde@obi.test") {
		t.Errorf("intro = %q, want it to name the invited address", got)
	}

	rows := []struct{ label, value string }{
		{"Organisation", "Obi Partners"}, {"Invited by", "Ada Obi"}, {"Email", "tunde@obi.test"}, {"Role", "Reviewer"},
	}
	prev := -1
	for _, r := range rows {
		_, inner := mustRow(t, out, r.label)
		if inner != r.value {
			t.Errorf("%s = %q, want %q", r.label, inner, r.value)
		}
		pos := strings.Index(out, ">"+r.label+"<")
		if pos <= prev {
			t.Errorf("row %q is out of order (at %d, previous row at %d)", r.label, pos, prev)
		}
		prev = pos
	}

	// The _last styles drop the bottom rule; the other three rows keep it.
	for _, r := range rows[:3] {
		vs, _ := mustRow(t, out, r.label)
		if !strings.Contains(vs, "border-bottom") || !strings.Contains(labelStyle(t, out, r.label), "border-bottom") {
			t.Errorf("%s row lacks the border-bottom of the non-last styles", r.label)
		}
	}
	vs, _ := mustRow(t, out, "Role")
	if strings.Contains(vs, "border-bottom") || strings.Contains(labelStyle(t, out, "Role"), "border-bottom") {
		t.Error("Role row carries a border-bottom; it must use the _last styles")
	}
}

func TestRenderInvite_ButtonAndFallbackOpenTheAcceptPage(t *testing.T) {
	const want = "https://www.ascomply.com/invite#token=abc-_XYZ"
	m := obiInvite
	m.Token = "abc-_XYZ"
	_, out := renderInvite(t, m)

	var button, fallback []anchor
	all := anchors(out)
	if len(all) == 0 {
		t.Fatal("mail holds no anchors")
	}
	for _, a := range all {
		switch a.text {
		case "Accept invite":
			button = append(button, a)
		case want:
			fallback = append(fallback, a)
		}
		if strings.Contains(a.href, "?token=") {
			t.Errorf("anchor %+v puts the token in the query", a)
		}
	}
	if len(button) != 1 || button[0].href != want {
		t.Errorf("button anchors = %+v, want one with href %s", button, want)
	}
	if len(fallback) != 1 || fallback[0].href != want {
		t.Errorf("fallback anchors = %+v, want one whose href and text are %s", fallback, want)
	}
	if !strings.Contains(text(out), "Button not working?") {
		t.Error(`"Button not working?" box missing`)
	}
}

func TestRenderInvite_StatesSevenDays(t *testing.T) {
	_, out := renderInvite(t, obiInvite)
	got := text(out)
	want := fmt.Sprintf("This invite expires in %d days.", InviteValidDays)
	if want != "This invite expires in 7 days." {
		t.Fatalf("InviteValidDays = %d, want 7", InviteValidDays)
	}
	if !strings.Contains(got, want) {
		t.Errorf("mail does not hold %q", want)
	}
	for _, bad := range []string{"24 hours", "What happens next"} {
		if strings.Contains(got, bad) {
			t.Errorf("mail holds %q", bad)
		}
	}
}

func TestRenderInvite_SubjectIsFixed(t *testing.T) {
	subject, _ := renderInvite(t, InviteMail{
		Workspace: "Pay now", Inviter: "Bank", Email: "tunde@obi.test", Role: "admin", Token: "t0k",
	})
	if subject != InviteSubject {
		t.Errorf("subject = %q, want %q", subject, InviteSubject)
	}
	if InviteSubject != "You are invited to ASComply" {
		t.Errorf("InviteSubject = %q, want the D13 subject", InviteSubject)
	}
	for _, leak := range []string{"Pay now", "Bank"} {
		if strings.Contains(subject, leak) {
			t.Errorf("subject %q holds %q", subject, leak)
		}
	}
}

func TestRenderInvite_EscapesWorkspaceAndInviter(t *testing.T) {
	m := obiInvite
	m.Workspace, m.Inviter = "<script>x</script>", "A & B"
	_, out := renderInvite(t, m)
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Error("workspace is not escaped as &lt;script&gt;")
	}
	if !strings.Contains(out, "A &amp; B") {
		t.Error("inviter is not escaped as A &amp; B")
	}
	if strings.Contains(out, "<script") {
		t.Error("output holds a live <script> tag")
	}
}

func TestRenderInvite_KeepsTheLayoutRules(t *testing.T) {
	_, out := renderInvite(t, obiInvite)
	imgs := imgRe.FindAllStringSubmatch(out, -1)
	if len(imgs) != 2 {
		t.Fatalf("mail holds %d <img>, want 2 (header and footer)", len(imgs))
	}
	for _, m := range imgs {
		if html.UnescapeString(m[1]) != LogoURL {
			t.Errorf("img src = %q, want %q", m[1], LogoURL)
		}
	}
	if !strings.HasPrefix(out, "<!DOCTYPE html>") {
		t.Errorf("output starts %q, want <!DOCTYPE html>", out[:min(len(out), 40)])
	}
	for _, bad := range []string{"<!--", "{{", "ascomply.africa"} {
		if strings.Contains(out, bad) {
			t.Errorf("mail holds %q", bad)
		}
	}
}

func TestRenderInvite_RoleLabels(t *testing.T) {
	for role, label := range map[string]string{"admin": "Admin", "preparer": "Preparer", "reviewer": "Reviewer"} {
		t.Run(role, func(t *testing.T) {
			m := obiInvite
			m.Role = role
			_, out := renderInvite(t, m)
			if _, got := mustRow(t, out, "Role"); got != label {
				t.Errorf("Role row = %q, want %q", got, label)
			}
		})
	}
	m := obiInvite
	m.Role = "owner"
	subject, out, err := RenderInvite(m)
	if err == nil {
		t.Error(`RenderInvite accepts the role "owner"`)
	}
	if out != "" {
		t.Errorf("RenderInvite returned %d bytes of html with its error, want none", len(out))
	}
	if err != nil && subject != "" {
		t.Errorf("RenderInvite returned subject %q with its error, want none", subject)
	}
}

func TestRenderInvite_EmptyInviterFallsBack(t *testing.T) {
	m := obiInvite
	m.Inviter = ""
	_, out := renderInvite(t, m)
	if got := inviteIntro(t, out); !strings.HasPrefix(got, "A workspace admin invited you") {
		t.Errorf("intro = %q, want it to start \"A workspace admin invited you\"", got)
	}
	if _, got := mustRow(t, out, "Invited by"); got != "A workspace admin" {
		t.Errorf("Invited by = %q, want \"A workspace admin\"", got)
	}
}

func TestRenderInvite_LongValuesWrap(t *testing.T) {
	long := strings.Repeat("W", 200)
	inviter := strings.Repeat("I", 200)
	email := strings.Repeat("e", 111) + "@obi.test" // 120 characters
	_, out := renderInvite(t, InviteMail{Workspace: long, Inviter: inviter, Email: email, Role: "admin", Token: "t0k"})
	for _, v := range []string{long, inviter, email} {
		if !strings.Contains(out, v) {
			t.Fatalf("mail does not hold the long value %.20s...", v)
		}
	}
	for _, label := range []string{"Organisation", "Invited by", "Email"} {
		s, _ := mustRow(t, out, label)
		for _, w := range []string{"word-break:break-word", "overflow-wrap:anywhere"} {
			if !strings.Contains(s, w) {
				t.Errorf("%s value cell style %q lacks %s", label, s, w)
			}
		}
	}
}
