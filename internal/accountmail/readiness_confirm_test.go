package accountmail

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

const readinessToken = "a_b-0123456789abcdefghijklmnopqrstuvwxyzAB"

func TestRenderReadinessConfirm_LinksTheTokenTwice(t *testing.T) {
	subject, out, err := RenderReadinessConfirm(ReadinessConfirmMail{Email: "ada@obi.test", Token: readinessToken, FileType: "pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if subject != ReadinessConfirmSubject {
		t.Errorf("subject = %q", subject)
	}
	want := ReadinessConfirmURL + "?t=" + url.QueryEscape(readinessToken)
	var got []string
	for _, m := range regexp.MustCompile(`<a href="([^"]*)"`).FindAllStringSubmatch(out, -1) {
		if h := html.UnescapeString(m[1]); strings.HasPrefix(h, ReadinessConfirmURL) {
			got = append(got, h)
		}
	}
	if len(got) != 2 || got[0] != want || got[1] != want {
		t.Errorf("confirm links = %v, want the button and the fallback link both %q", got, want)
	}
}

func TestRenderReadinessConfirm_EscapesTheAddressAndStatesTheExpiry(t *testing.T) {
	_, out, err := RenderReadinessConfirm(ReadinessConfirmMail{Email: "<x>@a.io", Token: "tok", FileType: "pdf"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"&lt;x&gt;@a.io", "PDF", "expires in 24 hours"} {
		if !strings.Contains(out, want) {
			t.Errorf("mail lacks %q", want)
		}
	}
	if strings.Contains(out, "<x>") {
		t.Error("mail holds the raw address")
	}
}

func TestRenderReadinessUsed_SaysThreeChecksAndLinksSignUp(t *testing.T) {
	subject, out, err := RenderReadinessUsed(ReadinessUsedMail{Email: "ada@obi.test"})
	if err != nil {
		t.Fatal(err)
	}
	if subject != ReadinessUsedSubject {
		t.Errorf("subject = %q", subject)
	}
	if !strings.Contains(out, "has used its 3 free checks") || !strings.Contains(out, `href="`+ReadinessCheckSignUpURL+`"`) {
		t.Error("mail lacks the 3-checks line or the sign-up link")
	}
	if strings.Contains(out, ReadinessConfirmURL) {
		t.Error("used mail holds a confirm link")
	}
}

func TestReadinessMails_CarryTheUnsubscribePlaceholder(t *testing.T) {
	_, confirm, err := RenderReadinessConfirm(ReadinessConfirmMail{Email: "ada@obi.test", Token: "tok", FileType: "csv"})
	if err != nil {
		t.Fatal(err)
	}
	_, used, err := RenderReadinessUsed(ReadinessUsedMail{Email: "ada@obi.test"})
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"confirm": confirm, "used": used} {
		if n := strings.Count(out, "?t=ASCOMPLY_UNSUBSCRIBE_TOKEN"); n != 1 {
			t.Errorf("%s: placeholder count = %d, want 1", name, n)
		}
	}
}

func TestRenderReadinessConfirm_RefusesAnEmptyTokenOrUnknownType(t *testing.T) {
	for _, m := range []ReadinessConfirmMail{
		{Email: "a@b.test", Token: "", FileType: "pdf"},
		{Email: "a@b.test", Token: "tok", FileType: "txt"},
	} {
		if _, out, err := RenderReadinessConfirm(m); err == nil || out != "" {
			t.Errorf("%+v: err = %v, html len %d; want an error and no HTML", m, err, len(out))
		}
	}
}

func TestTemplate_KnowsTheTwoNewMails(t *testing.T) {
	for _, name := range []string{"readiness_confirm", "readiness_used"} {
		checkComposes(t, name)
		src, err := Template(name)
		if err != nil || !strings.Contains(string(src), `{{define "layout"}}`) || !strings.Contains(string(src), `define "heading"}}`) {
			t.Errorf("Template(%s): err %v, layout/blocks missing", name, err)
		}
	}
}
