package accountmail

import (
	"html"
	"html/template"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var blogMail = BlogConfirmMail{
	Email: "ada@obi.test", Token: "abc_-", Topics: []string{"Tax policy", "E-invoicing"}, Frequency: "every_post",
}

func renderBlog(t *testing.T, m BlogConfirmMail) (subject, out string) {
	t.Helper()
	subject, out, err := RenderBlogConfirm(m)
	if err != nil {
		t.Fatalf("RenderBlogConfirm(%+v): %v", m, err)
	}
	return subject, out
}

// blogHrefs returns the unescaped hrefs of the confirm button and the fallback link.
func blogHrefs(t *testing.T, out string) []string {
	t.Helper()
	var got []string
	for _, m := range regexp.MustCompile(`<a href="([^"]*)"`).FindAllStringSubmatch(out, -1) {
		if strings.HasPrefix(html.UnescapeString(m[1]), BlogConfirmURL) {
			got = append(got, html.UnescapeString(m[1]))
		}
	}
	if len(got) != 2 {
		t.Fatalf("confirm links = %v, want the button and the fallback link", got)
	}
	return got
}

func TestBlogConfirmTemplate_ComposesTheLayout(t *testing.T) {
	src, err := Template("blog_confirm")
	if err != nil {
		t.Fatalf("Template(blog_confirm): %v", err)
	}
	if _, err := template.New("blog_confirm").Parse(string(src)); err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, out := renderBlog(t, blogMail)
	for _, want := range []string{
		"Button not working?",
		"Confirm your ASComply blog subscription",
		"Confirm your email to get new posts from the ASComply blog.",
		"Newsletter",
		"Confirm your subscription.",
		"New posts, on your schedule.",
		"Someone asked to get ASComply blog posts at ada@obi.test. Confirm to start. Nothing is sent until you do.",
		"Confirm subscription",
		"This link works once and expires in 48 hours.",
		"If you did not ask for this, ignore this email. You will not be subscribed.",
		"You received this email because this address was entered on the ASComply blog newsletter page.",
		">Email<", ">Topics<", ">How often<",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("body misses %q", want)
		}
	}
}

func TestRenderBlogConfirm_SubjectIsFixed(t *testing.T) {
	a, _ := renderBlog(t, blogMail)
	b, _ := renderBlog(t, BlogConfirmMail{Email: "x@y.test", Token: "t", Topics: []string{"Other"}, Frequency: "monthly"})
	if a != "Confirm your ASComply blog subscription" || a != b {
		t.Errorf("subjects = %q, %q; want the fixed subject", a, b)
	}
}

func TestRenderBlogConfirm_ButtonAndFallbackOpenTheConfirmPage(t *testing.T) {
	_, out := renderBlog(t, blogMail)
	for i, h := range blogHrefs(t, out) {
		if want := "https://blog.ascomply.com/subscribe/confirm?token=abc_-"; h != want {
			t.Errorf("link %d = %q, want %q", i, h, want)
		}
	}
}

func TestRenderBlogConfirm_TokenIsEscapedInBothLinks(t *testing.T) {
	m := blogMail
	m.Token = `a&b"<x`
	_, out := renderBlog(t, m)
	for i, h := range blogHrefs(t, out) {
		if want := BlogConfirmURL + "?token=" + url.QueryEscape(m.Token); h != want {
			t.Errorf("link %d = %q, want %q", i, h, want)
		}
	}
	if strings.Contains(out, `token=a&b`) || strings.Contains(out, `<x`) {
		t.Error("raw token characters reached the body")
	}
}

func TestRenderBlogConfirm_ListsTopicsFrequencyAndEmail(t *testing.T) {
	_, out := renderBlog(t, blogMail)
	for _, want := range []string{"Tax policy, E-invoicing", "Every post", "ada@obi.test"} {
		if !strings.Contains(out, want) {
			t.Errorf("body misses %q", want)
		}
	}
}

func TestRenderBlogConfirm_EscapesUserText(t *testing.T) {
	_, out := renderBlog(t, BlogConfirmMail{
		Email: `x"<script>@a.co`, Token: "t", Topics: []string{"<b>T</b>"}, Frequency: "weekly",
	})
	for _, bad := range []string{"<script>", "<b>"} {
		if strings.Contains(out, bad) {
			t.Errorf("body holds raw %q", bad)
		}
	}
}

func TestRenderBlogConfirm_States48Hours(t *testing.T) {
	_, out := renderBlog(t, blogMail)
	if !strings.Contains(out, "expires in 48 hours") {
		t.Error("body misses the 48-hour expiry")
	}
}

func TestRenderBlogConfirm_FrequencyLabels(t *testing.T) {
	for freq, label := range map[string]string{"weekly": "Weekly digest", "every_post": "Every post", "monthly": "Monthly"} {
		m := blogMail
		m.Frequency = freq
		if _, out := renderBlog(t, m); !strings.Contains(out, ">"+label+"<") {
			t.Errorf("frequency %q: body misses label %q", freq, label)
		}
	}
	for _, freq := range []string{"each", ""} {
		m := blogMail
		m.Frequency = freq
		if _, _, err := RenderBlogConfirm(m); err == nil {
			t.Errorf("frequency %q: no error", freq)
		}
	}
}
