package accountmail

import (
	"bytes"
	"html"
	"html/template"
	"regexp"
	"strings"
	"testing"
)

const (
	confirmURL = "https://api.example.test/auth/verify?token=t1&type=signup&redirect_to="
	adaEmail   = "ada@obi.test"
)

// jsonMap mirrors GoTrue's models.JSONMap, the typed map it passes as .Data.
type jsonMap map[string]interface{}

// reg is the shape GoTrue hands the template after JSON decoding (register.go data["registration"]).
func reg(kv ...string) jsonMap {
	m := map[string]interface{}{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return jsonMap{"registration": m}
}

// gotrueData is the map ConfirmationMail executes the template with (templatemailer.go).
func gotrueData(email string, data jsonMap, withDataKey bool) map[string]any {
	d := map[string]any{
		"SiteURL":         "https://www.ascomply.com",
		"ConfirmationURL": confirmURL,
		"Email":           email,
		"Token":           "123456",
		"TokenHash":       "hash",
		"RedirectTo":      "https://www.ascomply.com/",
	}
	if withDataKey {
		d["Data"] = data
	}
	return d
}

// renderSrc parses and executes src as GoTrue does (template.go: template.New(url).Parse).
func renderSrc(t *testing.T, src []byte, data map[string]any) string {
	t.Helper()
	tpl, err := template.New("https://api.ascomply.com/emails/confirmation.html").Parse(string(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return buf.String()
}

func confirmationSrc(t *testing.T) []byte {
	t.Helper()
	src, err := Template("confirmation")
	if err != nil {
		t.Fatalf("Template(confirmation): %v", err)
	}
	if len(src) == 0 {
		t.Fatal("Template(confirmation) returned no bytes")
	}
	return src
}

func renderConfirmation(t *testing.T, email string, data jsonMap) string {
	t.Helper()
	return renderSrc(t, confirmationSrc(t), gotrueData(email, data, true))
}

// text is what a reader sees: entities decoded.
func text(out string) string { return html.UnescapeString(out) }

var (
	anchorRe = regexp.MustCompile(`(?s)<a\b([^>]*)>(.*?)</a>`)
	hrefRe   = regexp.MustCompile(`\bhref="([^"]*)"`)
	imgRe    = regexp.MustCompile(`<img\b[^>]*\bsrc="([^"]*)"`)
	styleRe  = regexp.MustCompile(`\bstyle="([^"]*)"`)
	introRe  = regexp.MustCompile(`(?s)<td\b([^>]*)>\s*(Hi[,\s].*?)</td>`)
)

type anchor struct{ href, text string }

func anchors(out string) []anchor {
	var as []anchor
	for _, m := range anchorRe.FindAllStringSubmatch(out, -1) {
		h := hrefRe.FindStringSubmatch(m[1])
		if h == nil {
			continue
		}
		as = append(as, anchor{href: html.UnescapeString(h[1]), text: strings.TrimSpace(html.UnescapeString(m[2]))})
	}
	return as
}

// intro returns the intro cell's style and its inner text.
func intro(t *testing.T, out string) (style, inner string) {
	t.Helper()
	m := introRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no intro cell starting \"Hi\" in:\n%s", out)
	}
	s := styleRe.FindStringSubmatch(m[1])
	if s == nil {
		t.Fatalf("intro cell has no style: %s", m[1])
	}
	return s[1], strings.TrimSpace(html.UnescapeString(m[2]))
}

// row finds the details row whose label cell reads label and returns its value cell's style and raw inner HTML.
func row(out, label string) (style, inner string, ok bool) {
	re := regexp.MustCompile(`(?s)<td\b[^>]*>\s*` + regexp.QuoteMeta(label) + `\s*</td>\s*<td\b([^>]*)>(.*?)</td>`)
	m := re.FindStringSubmatch(out)
	if m == nil {
		return "", "", false
	}
	s := styleRe.FindStringSubmatch(m[1])
	if s == nil {
		return "", strings.TrimSpace(m[2]), true
	}
	return s[1], strings.TrimSpace(m[2]), true
}

func mustRow(t *testing.T, out, label string) (style, inner string) {
	t.Helper()
	style, inner, ok := row(out, label)
	if !ok {
		t.Fatalf("no details row labelled %q in:\n%s", label, out)
	}
	return style, inner
}

func TestTemplate_ParsesAndRendersAsGoTrueDoes(t *testing.T) {
	src := confirmationSrc(t)
	cases := []struct {
		name string
		data map[string]any
	}{
		{"answers", gotrueData(adaEmail, reg("display_name", "Ada Obi", "workspace_name", "Obi Partners"), true)},
		{"empty Data", gotrueData(adaEmail, jsonMap{}, true)},
		{"nil Data map", gotrueData(adaEmail, nil, true)},
		{"no Data key", gotrueData(adaEmail, nil, false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := renderSrc(t, src, tc.data)
			if !strings.Contains(text(out), adaEmail) {
				t.Fatalf("rendered mail does not show %s:\n%s", adaEmail, out)
			}
		})
	}
}

func TestTemplate_RendersWithAnswersMissingOneField(t *testing.T) {
	out := renderConfirmation(t, adaEmail, reg("workspace_name", "Obi Partners"))
	_, inner := mustRow(t, out, "Organisation")
	if inner != "Obi Partners" {
		t.Fatalf("Organisation value = %q, want %q", inner, "Obi Partners")
	}
	if _, got := intro(t, out); got != "Hi, your ASComply account for Obi Partners has been created. Confirm ada@obi.test to open your workspace." {
		t.Errorf("workspace-only intro = %q", got)
	}

	out = renderConfirmation(t, adaEmail, reg("display_name", "Ada Obi"))
	if _, got := intro(t, out); got != "Hi Ada Obi, your ASComply account has been created. Confirm ada@obi.test to open your workspace." {
		t.Errorf("name-only intro = %q", got)
	}
	if strings.Contains(text(out), "Organisation") {
		t.Error("Organisation row renders without a workspace name")
	}
}

func TestTemplate_ShowsTheRegistrationAnswers(t *testing.T) {
	out := renderConfirmation(t, adaEmail, reg("display_name", "Ada Obi", "workspace_name", "Obi Partners"))
	_, got := intro(t, out)
	want := "Hi Ada Obi, your ASComply account for Obi Partners has been created. Confirm ada@obi.test to open your workspace."
	if got != want {
		t.Errorf("intro = %q, want %q", got, want)
	}
	if _, v := mustRow(t, out, "Organisation"); v != "Obi Partners" {
		t.Errorf("Organisation = %q, want Obi Partners", v)
	}
	if _, v := mustRow(t, out, "Email"); v != adaEmail {
		t.Errorf("Email = %q, want %s", v, adaEmail)
	}
	if _, v := mustRow(t, out, "Role"); v != "Admin" {
		t.Errorf("Role = %q, want Admin", v)
	}
}

func TestTemplate_RendersWithoutRegistrationAnswers(t *testing.T) {
	src := confirmationSrc(t)
	want := "Hi, your ASComply account has been created. Confirm ada@obi.test to open your workspace."
	cases := []struct {
		name string
		data map[string]any
	}{
		{"empty Data", gotrueData(adaEmail, jsonMap{}, true)},
		{"nil Data map", gotrueData(adaEmail, nil, true)},
		{"no Data key", gotrueData(adaEmail, nil, false)},
		{"empty registration", gotrueData(adaEmail, jsonMap{"registration": map[string]interface{}{}}, true)},
		{"empty answers", gotrueData(adaEmail, reg("display_name", "", "workspace_name", ""), true)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := renderSrc(t, src, tc.data)
			if _, got := intro(t, out); got != want {
				t.Errorf("intro = %q, want %q", got, want)
			}
			if strings.Contains(text(out), "Organisation") {
				t.Error("Organisation row renders without registration answers")
			}
			if _, v := mustRow(t, out, "Email"); v != adaEmail {
				t.Errorf("Email = %q, want %s", v, adaEmail)
			}
		})
	}
}

func TestTemplate_ActionURLIsTheButtonAndTheFallbackLink(t *testing.T) {
	out := renderConfirmation(t, adaEmail, reg("display_name", "Ada Obi", "workspace_name", "Obi Partners"))
	var button, fallback []anchor
	for _, a := range anchors(out) {
		switch {
		case a.text == "Confirm email address":
			button = append(button, a)
		case a.text == confirmURL:
			fallback = append(fallback, a)
		}
	}
	if len(button) != 1 || button[0].href != confirmURL {
		t.Errorf("button anchors = %+v, want one with href %s", button, confirmURL)
	}
	if len(fallback) != 1 || fallback[0].href != confirmURL {
		t.Errorf("fallback anchors = %+v, want one whose href and text are %s", fallback, confirmURL)
	}
	if !strings.Contains(text(out), "Button not working?") {
		t.Error(`"Button not working?" box missing`)
	}

	const hostile = `https://api.example.test/v?a="><script>x</script>&b=<i>`
	d := gotrueData(adaEmail, jsonMap{}, true)
	d["ConfirmationURL"] = hostile
	bad := renderSrc(t, confirmationSrc(t), d)
	if strings.Contains(bad, "<script") || strings.Contains(bad, "<i>") {
		t.Error("a hostile ConfirmationURL injects markup")
	}
	if n := len(anchorRe.FindAllString(bad, -1)); n != 3 {
		t.Errorf("hostile URL changed the anchor count to %d, want 3 (button, fallback, Privacy)", n)
	}
}

func TestTemplate_EscapesTheAnswers(t *testing.T) {
	out := renderConfirmation(t, adaEmail, reg("display_name", "<script>x</script>", "workspace_name", "A & B"))
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Error("display name is not escaped as &lt;script&gt;")
	}
	if !strings.Contains(out, "A &amp; B") {
		t.Error("workspace name is not escaped as A &amp; B")
	}
	if strings.Contains(out, "<script") {
		t.Error("output holds a live <script> tag")
	}
	if _, v := mustRow(t, out, "Organisation"); v != "A &amp; B" {
		t.Errorf("Organisation row = %q, want the escaped name", v)
	}

	const payload = `"><img src=x onerror=alert(1)>`
	out = renderConfirmation(t, payload+"@obi.test", reg("display_name", payload, "workspace_name", payload))
	if n := len(imgRe.FindAllString(out, -1)); n != 2 {
		t.Errorf("payload injected an element: %d <img>, want 2", n)
	}
	if strings.Contains(out, "<img src=x") || strings.Contains(out, "onerror=alert(1)>") {
		t.Error("payload survives unescaped in the mail")
	}
	if _, got := intro(t, out); !strings.Contains(got, payload) {
		t.Errorf("intro does not show the payload as text: %q", got)
	}
}

func TestTemplate_StepTwoIsAddYourCompany(t *testing.T) {
	out := text(renderConfirmation(t, adaEmail, jsonMap{}))
	for _, want := range []string{
		"What happens next", "Confirm your email", "Upload an invoice",
		"Add your company", "Enter your company's name and TIN.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mail does not hold %q", want)
		}
	}
	for _, bad := range []string{"Connect your accounting system", "Odoo"} {
		if strings.Contains(out, bad) {
			t.Errorf("mail still holds %q", bad)
		}
	}
}

func TestTemplate_CarriesTheQ12Corrections(t *testing.T) {
	raw := renderConfirmation(t, adaEmail, reg("display_name", "Ada Obi", "workspace_name", "Obi Partners"))
	out := text(raw)
	for _, bad := range []string{"ascomply.africa", "[Registered address]", "Help centre", ">Security<", ">Registered<", "{{"} {
		if strings.Contains(raw, bad) || strings.Contains(out, bad) {
			t.Errorf("mail holds %q", bad)
		}
	}
	for _, want := range []string{"ASComply Africa · Lagos, Nigeria", "This link expires in 24 hours."} {
		if !strings.Contains(out, want) {
			t.Errorf("mail does not hold %q", want)
		}
	}
	var others []anchor
	for _, a := range anchors(raw) {
		if a.text != "Confirm email address" && a.text != confirmURL {
			others = append(others, a)
		}
	}
	if len(others) != 1 || others[0].href != "https://www.ascomply.com/privacy" || others[0].text != "Privacy" {
		t.Errorf("footer anchors = %+v, want only Privacy -> https://www.ascomply.com/privacy", others)
	}
}

func TestTemplate_LogoIsTheAbsolutePublicURL(t *testing.T) {
	if !strings.HasPrefix(LogoURL, "https://") {
		t.Fatalf("LogoURL = %q, want an absolute https URL", LogoURL)
	}
	out := renderConfirmation(t, adaEmail, jsonMap{})
	imgs := imgRe.FindAllStringSubmatch(out, -1)
	if len(imgs) != 2 {
		t.Fatalf("mail holds %d <img>, want 2 (header and footer)", len(imgs))
	}
	for _, m := range imgs {
		if html.UnescapeString(m[1]) != LogoURL {
			t.Errorf("img src = %q, want %q", m[1], LogoURL)
		}
	}
}

func TestLayout_ContainerKeepsSixHundredPixelsWithoutMSO(t *testing.T) {
	out := renderConfirmation(t, adaEmail, jsonMap{})
	if !strings.HasPrefix(out, "<!DOCTYPE html>") {
		t.Errorf("output starts %q, want <!DOCTYPE html>", out[:min(len(out), 40)])
	}
	if strings.Contains(out, "<!--") {
		t.Error("output holds an HTML comment")
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
}

func TestLayout_LongAnswersWrap(t *testing.T) {
	long := strings.Repeat("W", 200)
	email := strings.Repeat("e", 111) + "@obi.test" // 120 characters
	out := renderConfirmation(t, email, reg("display_name", long, "workspace_name", long))
	if !strings.Contains(out, long) || !strings.Contains(out, email) {
		t.Fatal("long answers or address are missing from the mail")
	}
	wrap := []string{"word-break:break-word", "overflow-wrap:anywhere"}
	check := func(what, style string) {
		for _, w := range wrap {
			if !strings.Contains(style, w) {
				t.Errorf("%s style %q lacks %s", what, style, w)
			}
		}
	}
	is, _ := intro(t, out)
	check("intro cell", is)
	for _, label := range []string{"Organisation", "Email", "Role"} {
		s, _ := mustRow(t, out, label)
		check(label+" value cell", s)
	}
}

// The stub defines every slot except next; the shell must come from layout.html alone.
func TestLayout_ServesAnyMailThatDefinesItsSlots(t *testing.T) {
	layout, err := files.ReadFile("layout.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(layout) == 0 {
		t.Fatal("layout.html is empty")
	}
	const slots = `{{define "title"}}Stub title{{end}}
{{define "preheader"}}Stub preheader{{end}}
{{define "eyebrow"}}Stub eyebrow{{end}}
{{define "heading"}}Stub heading{{end}}
{{define "heading_accent"}}Stub accent{{end}}
{{define "intro"}}Stub intro for {{.Email}}{{end}}
{{define "action_url"}}{{ .ConfirmationURL }}{{end}}
{{define "action_label"}}Stub action{{end}}
{{define "expiry"}}Stub expiry{{end}}
{{define "details"}}<tr><td>Stub label</td><td>Stub value</td></tr>{{end}}
{{define "ignore_note"}}Stub ignore{{end}}
{{define "reason"}}Stub reason{{end}}
`
	const call = `{{template "layout" .}}`
	data := gotrueData(adaEmail, jsonMap{}, true)

	out := text(renderSrc(t, []byte(string(layout)+slots+call), data))
	for _, want := range []string{
		LogoURL, "#082f31", "#f5bc88", "#dce7e4", "Button not working?", "ASComply Africa · Lagos, Nigeria",
		"Stub title", "Stub preheader", "Stub eyebrow", "Stub heading", "Stub accent", "Stub intro for " + adaEmail,
		"Stub action", "Stub expiry", "Stub label", "Stub value", "Stub ignore", "Stub reason",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("shell does not hold %q", want)
		}
	}
	if strings.Contains(out, "What happens next") {
		t.Error("shell renders the next section though the mail does not define it")
	}
	for _, leak := range []string{
		"Confirm email address", "You are registered", "Registration", "24 hours", "If you did not register",
		"Organisation", "Admin", "Add your company", "You received this email because",
	} {
		if strings.Contains(out, leak) {
			t.Errorf("layout hard-codes the confirmation copy %q", leak)
		}
	}
	if n := strings.Count(out, confirmURL); n != 3 {
		t.Errorf("the action_url slot renders %d times, want 3 (button href, fallback href, fallback text)", n)
	}

	withNext := text(renderSrc(t, []byte(string(layout)+slots+`{{define "next"}}Stub next steps{{end}}`+call), data))
	if !strings.Contains(withNext, "Stub next steps") {
		t.Error("a mail that defines next does not get it rendered")
	}
}

func TestTemplate_UnknownNameIsAnError(t *testing.T) {
	if src, err := Template("confirmation"); err != nil || len(src) == 0 {
		t.Fatalf("Template(confirmation) = %d bytes, %v; want bytes and no error", len(src), err)
	}
	for _, name := range []string{"nope", "", "../layout", "layout", "confirmation.html"} {
		src, err := Template(name)
		if err == nil {
			t.Errorf("Template(%q) returned no error", name)
		}
		if src != nil {
			t.Errorf("Template(%q) returned %d bytes, want nil", name, len(src))
		}
	}
}

func TestTemplate_ReturnsACopyAndAWholeDocument(t *testing.T) {
	a := confirmationSrc(t)
	want := string(a)
	for i := range a {
		a[i] = 'X'
	}
	if got := string(confirmationSrc(t)); got != want {
		t.Error("a second Template call sees the first caller's mutation")
	}
	out := renderConfirmation(t, adaEmail, jsonMap{})
	if n := strings.Count(out, "<html"); n != 1 {
		t.Errorf("output holds %d <html>, want 1", n)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "</html>") {
		t.Error("output does not end with </html>")
	}
}
