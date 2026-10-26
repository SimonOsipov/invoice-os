package accountmail

import (
	"bytes"
	"html"
	"html/template"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

var (
	dueAt        = time.Date(2026, time.November, 24, 18, 10, 44, 0, time.UTC)
	okaforNotice = DeletionNoticeMail{Workspace: "Okafor & Co", Requester: "Ada Okafor", Token: "t0k", DueAt: dueAt}
	titleRe      = regexp.MustCompile(`<title>(.*?)</title>`)
	preheaderRe  = regexp.MustCompile(`mso-hide:all">(.*?)&#847;`)
)

func renderNotice(t *testing.T, m DeletionNoticeMail) (subject, out string) {
	t.Helper()
	subject, out, err := RenderDeletionNotice(m)
	if err != nil {
		t.Fatalf("RenderDeletionNotice(%+v): %v", m, err)
	}
	return subject, out
}

// introWith returns the layout's intro cell, the one holding phrase.
func introWith(t *testing.T, out, phrase string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<td\b[^>]*>\s*([^<]*` + regexp.QuoteMeta(phrase) + `[^<]*)</td>`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no intro cell holding %q in:\n%s", phrase, out)
	}
	return strings.TrimSpace(html.UnescapeString(m[1]))
}

// checkRows asserts the labelled rows appear in order with these values, and only the last drops its rule.
func checkRows(t *testing.T, out string, rows [][2]string) {
	t.Helper()
	prev := -1
	for i, r := range rows {
		vs, inner := mustRow(t, out, r[0])
		if html.UnescapeString(inner) != r[1] {
			t.Errorf("%s = %q, want %q", r[0], inner, r[1])
		}
		pos := strings.Index(out, ">"+r[0]+"<")
		if pos <= prev {
			t.Errorf("row %q is out of order", r[0])
		}
		prev = pos
		hasRule := strings.Contains(vs, "border-bottom") && strings.Contains(labelStyle(t, out, r[0]), "border-bottom")
		noRule := !strings.Contains(vs, "border-bottom") && !strings.Contains(labelStyle(t, out, r[0]), "border-bottom")
		if last := i == len(rows)-1; (last && !noRule) || (!last && !hasRule) {
			t.Errorf("%s row has the wrong border-bottom for its position", r[0])
		}
	}
}

func checkComposes(t *testing.T, name string) {
	t.Helper()
	src, err := Template(name)
	if err != nil {
		t.Fatalf("Template(%s): %v", name, err)
	}
	layout, err := files.ReadFile("layout.html")
	if err != nil {
		t.Fatal(err)
	}
	mail, err := files.ReadFile(name + ".html")
	if err != nil {
		t.Fatalf("%s.html is not embedded: %v", name, err)
	}
	if len(layout) == 0 || len(mail) == 0 {
		t.Fatalf("layout.html %d bytes, %s.html %d bytes; want both non-empty", len(layout), name, len(mail))
	}
	if !bytes.HasPrefix(src, layout) || !bytes.HasSuffix(src, mail) {
		t.Errorf("Template(%s) is not layout.html followed by %s.html", name, name)
	}
	if _, err := template.New(name).Parse(string(src)); err != nil {
		t.Errorf("html/template does not parse Template(%s): %v", name, err)
	}
}

func checkDatesAreUTC(t *testing.T, render func(time.Time) string) {
	t.Helper()
	utc := time.Date(2026, time.November, 24, 23, 30, 0, 0, time.UTC)
	wat := utc.In(time.FixedZone("WAT", 3600))
	if wat.Day() != 25 {
		t.Fatalf("fixture: WAT instant is on day %d, want 25", wat.Day())
	}
	for _, d := range []time.Time{utc, wat} {
		got := text(render(d))
		for _, want := range []string{"24 November 2026 (UTC)", "24 November 2026, 23:30 UTC"} {
			if !strings.Contains(got, want) {
				t.Errorf("due %v: mail does not hold %q", d, want)
			}
		}
		if strings.Contains(got, "25 November") {
			t.Errorf("due %v: mail holds the local date", d)
		}
	}
}

func TestDeletionNoticeTemplate_ComposesTheLayout(t *testing.T) { checkComposes(t, "deletion_notice") }

func TestRenderDeletionNotice_NamesWorkspaceRequesterAndDate(t *testing.T) {
	_, out := renderNotice(t, okaforNotice)
	want := "Ada Okafor asked to delete Okafor & Co. It will be deleted on 24 November 2026 (UTC)."
	if got := introWith(t, out, "asked to delete"); got != want {
		t.Errorf("intro = %q, want %q", got, want)
	}
	checkRows(t, out, [][2]string{
		{"Organisation", "Okafor & Co"}, {"Requested by", "Ada Okafor"}, {"Deleted on", "24 November 2026, 18:10 UTC"},
	})
}

func TestRenderDeletionNotice_ButtonAndFallbackOpenTheCancelLink(t *testing.T) {
	const want = "https://app.ascomply.com/#cancel-deletion=abc-_XYZ"
	m := okaforNotice
	m.Token = "abc-_XYZ"
	_, out := renderNotice(t, m)

	var button, fallback []anchor
	for _, a := range anchors(out) {
		switch a.text {
		case "Cancel the deletion":
			button = append(button, a)
		case want:
			fallback = append(fallback, a)
		}
		if strings.Contains(a.href, "?token=") || strings.Contains(a.href, "?cancel") {
			t.Errorf("anchor %+v puts the token in the query", a)
		}
	}
	if len(button) != 1 || button[0].href != want {
		t.Errorf("button anchors = %+v, want one with href %s", button, want)
	}
	if len(fallback) != 1 || fallback[0].href != want {
		t.Errorf("fallback anchors = %+v, want one whose href and text are %s", fallback, want)
	}
}

func TestRenderDeletionNotice_TokenIsEscapedInBothLinks(t *testing.T) {
	cases := []struct{ token, frag string }{
		{"a+b", "a%2Bb"}, {"a/b", "a%2Fb"}, {"a=b", "a%3Db"}, {"a b", "a+b"}, {"a#b", "a%23b"}, {"a&b", "a%26b"},
		{`a"b<c>`, "a%22b%3Cc%3E"}, {"a%b", "a%25b"}, {"abc-_XYZ.~", "abc-_XYZ.~"},
	}
	for _, c := range cases {
		m := okaforNotice
		m.Token = c.token
		_, out := renderNotice(t, m)
		want := AppURL + "#cancel-deletion=" + c.frag
		as := anchors(out)
		if len(as) != 3 {
			t.Fatalf("token %q: anchors = %+v, want 3", c.token, as)
		}
		if as[0].text != "Cancel the deletion" || as[0].href != want {
			t.Errorf("token %q: button = %+v, want href %s", c.token, as[0], want)
		}
		if as[1].href != want || as[1].text != want {
			t.Errorf("token %q: fallback = %+v, want href and text %s", c.token, as[1], want)
		}
		u, err := url.Parse(as[0].href)
		if err != nil {
			t.Fatalf("token %q: href does not parse: %v", c.token, err)
		}
		if u.RawQuery != "" || strings.Count(as[0].href, "#") != 1 {
			t.Errorf("token %q: href %s holds a query or a second #", c.token, as[0].href)
		}
		q, err := url.ParseQuery(u.EscapedFragment())
		if err != nil || q.Get("cancel-deletion") != c.token || len(q) != 1 {
			t.Errorf("token %q: fragment decodes to %v, %v", c.token, q, err)
		}
	}
}

func TestRenderDeletionNotice_SubjectIsFixed(t *testing.T) {
	subject, out := renderNotice(t, DeletionNoticeMail{Workspace: "Pay now", Requester: "Bank", Token: "t0k", DueAt: dueAt})
	if subject != DeletionNoticeSubject {
		t.Errorf("subject = %q, want %q", subject, DeletionNoticeSubject)
	}
	if DeletionNoticeSubject != "Your ASComply workspace is scheduled for deletion" {
		t.Errorf("DeletionNoticeSubject = %q", DeletionNoticeSubject)
	}
	for _, leak := range []string{"Pay now", "Bank"} {
		if strings.Contains(subject, leak) {
			t.Errorf("subject %q holds %q", subject, leak)
		}
	}
	if got := titleRe.FindStringSubmatch(out); got == nil || got[1] != DeletionNoticeSubject {
		t.Errorf("title = %q, want %q", got, DeletionNoticeSubject)
	}
}

func TestRenderDeletionNotice_DatesAreUTC(t *testing.T) {
	checkDatesAreUTC(t, func(d time.Time) string {
		m := okaforNotice
		m.DueAt = d
		_, out := renderNotice(t, m)
		return out
	})
}

func TestRenderDeletionNotice_EmptyRequesterFallsBack(t *testing.T) {
	m := okaforNotice
	m.Requester = ""
	_, out := renderNotice(t, m)
	if got := introWith(t, out, "asked to delete"); !strings.HasPrefix(got, "A workspace admin asked to delete") {
		t.Errorf("intro = %q, want it to start \"A workspace admin asked to delete\"", got)
	}
	if _, got := mustRow(t, out, "Requested by"); got != "A workspace admin" {
		t.Errorf("Requested by = %q, want \"A workspace admin\"", got)
	}
}

func TestRenderDeletionNotice_RefusesAnEmptyTokenOrDate(t *testing.T) {
	noToken, noDate := okaforNotice, okaforNotice
	noToken.Token, noDate.DueAt = "", time.Time{}
	for name, m := range map[string]DeletionNoticeMail{"empty token": noToken, "zero date": noDate} {
		subject, out, err := RenderDeletionNotice(m)
		if err == nil || subject != "" || out != "" {
			t.Errorf("%s: got subject %q, %d bytes, err %v; want an error and nothing else", name, subject, len(out), err)
		}
	}
}

func TestRenderDeletionNotice_EscapesHostileInput(t *testing.T) {
	m := DeletionNoticeMail{Workspace: "<script>x</script>", Requester: `" onmouseover="x`, Token: "t0k", DueAt: dueAt}
	_, out := renderNotice(t, m)
	for _, bad := range []string{"<script", `onmouseover="`} {
		if strings.Contains(out, bad) {
			t.Errorf("hostile input leaves %q live in the output", bad)
		}
	}
	for label, want := range map[string]string{"Organisation": m.Workspace, "Requested by": m.Requester} {
		_, inner := mustRow(t, out, label)
		if strings.ContainsAny(inner, `<>"`) || html.UnescapeString(inner) != want {
			t.Errorf("%s cell = %q, want the escaped text of %q", label, inner, want)
		}
	}
	as := anchors(out)
	const link = AppURL + "#cancel-deletion=t0k"
	if len(as) != 3 || as[0].href != link || as[1].href != link || as[2].href != "https://www.ascomply.com/privacy" {
		t.Errorf("hostile input moved an anchor: %+v", as)
	}
	pre := preheaderRe.FindStringSubmatch(out)
	if pre == nil || pre[1] != "An admin asked to delete your ASComply workspace. You can cancel it until the due date." {
		t.Errorf("preheader = %q, want the fixed copy", pre)
	}
}

func TestRenderDeletionNotice_LongValuesWrap(t *testing.T) {
	long, requester := strings.Repeat("W", 200), strings.Repeat("R", 200)
	_, out := renderNotice(t, DeletionNoticeMail{Workspace: long, Requester: requester, Token: "t0k", DueAt: dueAt})
	for _, v := range []string{long, requester} {
		if !strings.Contains(out, v) {
			t.Fatalf("mail does not hold the long value %.20s...", v)
		}
	}
	for _, label := range []string{"Organisation", "Requested by"} {
		s, _ := mustRow(t, out, label)
		for _, w := range []string{"word-break:break-word", "overflow-wrap:anywhere"} {
			if !strings.Contains(s, w) {
				t.Errorf("%s value cell style %q lacks %s", label, s, w)
			}
		}
	}
}

func checkLayoutRules(t *testing.T, out string) {
	t.Helper()
	imgs := imgRe.FindAllStringSubmatch(out, -1)
	if len(imgs) != 2 {
		t.Fatalf("mail holds %d <img>, want 2", len(imgs))
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

func TestRenderDeletionNotice_KeepsTheLayoutRules(t *testing.T) {
	_, out := renderNotice(t, okaforNotice)
	checkLayoutRules(t, out)
}
