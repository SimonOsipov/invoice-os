package auth_test

import (
	"bytes"
	"html/template"
	"regexp"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

// accountMailSubject is idp-up.sh's GOTRUE_MAILER_SUBJECTS_CONFIRMATION for idp-mail.
const accountMailSubject = "Confirm your ASComply account"

var imgSrcRe = regexp.MustCompile(`<img\b[^>]*\bsrc="([^"]*)"`)

// brandedRegistrant registers with the landing form's answers and returns the one mail GoTrue sent.
func brandedRegistrant(t *testing.T) (idpUser, mailpitMessage) {
	t.Helper()
	gw, _ := startGateway(t, idpMailURL(t), 0, nil)
	u := registrant(t, gw, map[string]string{"display_name": "Ada Obi", "workspace_name": "Obi Partners", "kind": "in_house"})
	return u, mailFor(t, u.email)
}

func TestIdP_ConfirmationMailIsBranded(t *testing.T) {
	u, msg := brandedRegistrant(t)

	if msg.Subject != accountMailSubject {
		t.Errorf("mail subject = %q, want %q", msg.Subject, accountMailSubject)
	}
	if msg.HTML == "" {
		t.Fatal("the mail has no HTML body")
	}
	for _, want := range []string{"Ada Obi", "Obi Partners", "Admin", u.email, "Button not working?", "Add your company"} {
		if !strings.Contains(msg.HTML, want) {
			t.Errorf("mail HTML lacks %q", want)
		}
	}
}

func TestIdP_ConfirmationMailLogoIsAbsolutePublic(t *testing.T) {
	_, msg := brandedRegistrant(t)

	var srcs []string
	for _, m := range imgSrcRe.FindAllStringSubmatch(msg.HTML, -1) {
		srcs = append(srcs, m[1])
	}
	if len(srcs) == 0 {
		t.Fatalf("mail HTML holds no <img src>: %s", msg.HTML)
	}
	if !strings.HasPrefix(accountmail.LogoURL, "https://") {
		t.Fatalf("accountmail.LogoURL = %q, want an absolute https:// URL", accountmail.LogoURL)
	}
	for _, src := range srcs {
		if src != accountmail.LogoURL {
			t.Errorf("<img src> = %q, want accountmail.LogoURL %q", src, accountmail.LogoURL)
		}
	}
}

func TestIdP_BrandedConfirmLinkVerifiesThenSignInSucceeds(t *testing.T) {
	base := idpMailURL(t)
	u, msg := brandedRegistrant(t)
	if msg.Subject != accountMailSubject {
		t.Fatalf("mail subject = %q, want the branded %q", msg.Subject, accountMailSubject)
	}

	if got := follow(t, confirmationLink(t, u.email)); got != siteURL+"/?verified=1" {
		t.Fatalf("verify redirect = %q, want %s/?verified=1", got, siteURL)
	}
	accessToken(t, base, u)
}

func TestIdP_ConfirmationMailWithoutAnswersIsBranded(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	u := registrant(t, gw)
	msg := mailFor(t, u.email)

	if msg.Subject != accountMailSubject {
		t.Errorf("mail subject = %q, want %q", msg.Subject, accountMailSubject)
	}
	if want := "Hi, your ASComply account has been created. Confirm " + u.email + " to open your workspace."; !strings.Contains(msg.HTML, want) {
		t.Errorf("mail HTML lacks the no-answers intro %q", want)
	}
	if strings.Contains(msg.HTML, "Organisation") {
		t.Error("mail HTML holds an Organisation label without answers")
	}
	if got := follow(t, confirmationLink(t, u.email)); got != siteURL+"/?verified=1" {
		t.Fatalf("verify redirect = %q, want %s/?verified=1", got, siteURL)
	}
	accessToken(t, base, u)
}

func TestConfirmationLinkSelection(t *testing.T) {
	const link = "http://localhost:9995/auth/verify?token=t1&type=signup&redirect_to=http://localhost:3000"
	const other = "http://localhost:9995/auth/verify?token=t2&type=signup&redirect_to=http://localhost:3000"
	// GoTrue's default confirmation body (templatemailer.go defaultConfirmationMail).
	goTrueDefault := `<h2>Confirm your email address</h2>
<p>Follow the link below to confirm this email address and finish signing up.</p>
<p><a href="` + link + `">Confirm email address</a></p>`
	anchor := func(href, text string) string { return `<a href="` + href + `">` + text + `</a>` }

	// The branded mail as GoTrue renders it: attribute and text carry the URL HTML-escaped.
	src, err := accountmail.Template("confirmation")
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := template.New("confirmation").Parse(string(src))
	if err != nil {
		t.Fatal(err)
	}
	var branded bytes.Buffer
	if err := tpl.Execute(&branded, map[string]any{
		"ConfirmationURL": link, "Email": "ada@obi.test", "SiteURL": siteURL,
		"Data": map[string]any{"registration": map[string]any{"display_name": "Ada Obi", "workspace_name": "Obi Partners"}},
	}); err != nil {
		t.Fatalf("render the branded template: %v", err)
	}

	for _, c := range []struct {
		name, body string
		want       string // "" means the body is refused
	}{
		{"branded_body", branded.String(), link},
		{"button_label_is_not_known", anchor(link, "Verify now") + anchor(link, link), link},
		{"unrelated_anchors_are_ignored", anchor("https://www.ascomply.com/privacy", "Privacy") + anchor(link, "Go") + anchor(link, link), link},
		{"gotrue_default_has_no_fallback_anchor", goTrueDefault, ""},
		{"button_and_fallback_differ", anchor(link, "Confirm email address") + anchor(other, other), ""},
		{"two_different_fallback_urls", anchor(link, "Confirm email address") + anchor(link, link) + anchor(other, other), ""},
		{"fallback_without_a_button", anchor(link, link), ""},
		{"no_anchor", "<p>no link</p>", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := actionLink(c.body)
			if c.want == "" {
				if err == nil || err.Error() == "" {
					t.Errorf("actionLink = %q, nil error; want a refusal with a message", got)
				}
				return
			}
			if err != nil || got != c.want {
				t.Errorf("actionLink = %q, %v; want %q", got, err, c.want)
			}
		})
	}
}
