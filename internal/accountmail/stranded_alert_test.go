package accountmail

import (
	"html"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

var okaforStranded = StrandedAlertMail{
	Workspace: "Okafor & Co",
	Invoices:  []StrandedInvoice{{"INV-1", "Finance manager"}, {"INV-2", "CFO"}, {"INV-3", "Reviewer"}},
}

func renderStranded(t *testing.T, m StrandedAlertMail) (subject, out string) {
	t.Helper()
	subject, out, err := RenderStrandedAlert(m)
	if err != nil {
		t.Fatalf("RenderStrandedAlert(%+v): %v", m, err)
	}
	return subject, out
}

func TestStrandedAlertTemplate_ComposesTheLayout(t *testing.T) {
	checkComposes(t, "stranded_alert")
}

func TestRenderStrandedAlert_NamesWorkspaceRolesAndInvoices(t *testing.T) {
	_, out := renderStranded(t, okaforStranded)
	want := "An account in Okafor & Co was deleted. Nobody left in the workspace can approve the invoices below. Add someone who can approve them to each role, or they stay waiting."
	if got := introWith(t, out, "was deleted"); got != want {
		t.Errorf("intro = %q, want %q", got, want)
	}
	checkRows(t, out, [][2]string{
		{"Organisation", "Okafor & Co"}, {"Finance manager", "INV-1"}, {"CFO", "INV-2"}, {"Reviewer", "INV-3"},
	})

	// Two invoices on one role: checkRows reads the first row of a label, so list every row.
	same := okaforStranded
	same.Invoices = []StrandedInvoice{{"INV-1", "Finance manager"}, {"INV-2", "CFO"}, {"INV-3", "Finance manager"}}
	_, out = renderStranded(t, same)
	var got [][2]string
	for _, m := range regexp.MustCompile(`<tr><td width="120"[^>]*>([^<]*)</td><td[^>]*>([^<]*)</td></tr>`).FindAllStringSubmatch(out, -1) {
		got = append(got, [2]string{html.UnescapeString(m[1]), html.UnescapeString(m[2])})
	}
	wantRows := [][2]string{{"Organisation", "Okafor & Co"}, {"Finance manager", "INV-1"}, {"CFO", "INV-2"}, {"Finance manager", "INV-3"}}
	if !reflect.DeepEqual(got, wantRows) {
		t.Errorf("rows = %q, want %q", got, wantRows)
	}
}

func TestRenderStrandedAlert_MoreRowOnlyWhenMore(t *testing.T) {
	m := okaforStranded
	m.More = 5
	_, out := renderStranded(t, m)
	checkRows(t, out, [][2]string{
		{"Organisation", "Okafor & Co"}, {"Finance manager", "INV-1"}, {"CFO", "INV-2"}, {"Reviewer", "INV-3"}, {"And 5 more invoices", ""},
	})
	m.More = 1
	_, out = renderStranded(t, m)
	checkRows(t, out, [][2]string{
		{"Organisation", "Okafor & Co"}, {"Finance manager", "INV-1"}, {"CFO", "INV-2"}, {"Reviewer", "INV-3"}, {"And 1 more invoice", ""},
	})
	_, out = renderStranded(t, okaforStranded)
	if strings.Contains(out, "more invoices") {
		t.Error("More row rendered with More = 0")
	}
}

func TestRenderStrandedAlert_SubjectIsFixed(t *testing.T) {
	subject, _ := renderStranded(t, StrandedAlertMail{Workspace: "Pay now", Invoices: []StrandedInvoice{{"INV-9", "<b>Bank</b>"}}})
	if subject != StrandedAlertSubject {
		t.Errorf("subject = %q, want %q", subject, StrandedAlertSubject)
	}
}

func TestRenderStrandedAlert_EscapesHostileInput(t *testing.T) {
	_, out := renderStranded(t, StrandedAlertMail{
		Workspace: "<i>ws</i>", Invoices: []StrandedInvoice{{`"><b>`, "<script>x</script>"}},
	})
	for _, raw := range []string{"<script>x</script>", `"><b>`, "<i>ws</i>"} {
		if strings.Contains(out, raw) {
			t.Errorf("output holds raw %q", raw)
		}
	}
	for _, esc := range []string{"&lt;script&gt;x&lt;/script&gt;", "&#34;&gt;&lt;b&gt;", "&lt;i&gt;ws&lt;/i&gt;"} {
		if !strings.Contains(out, esc) {
			t.Errorf("output lacks escaped %q", esc)
		}
	}
}

func TestRenderStrandedAlert_ButtonOpensTheApp(t *testing.T) {
	_, out := renderStranded(t, okaforStranded)
	var button, fallback []anchor
	for _, a := range anchors(out) {
		switch a.text {
		case "Open ASComply":
			button = append(button, a)
		case AppURL:
			fallback = append(fallback, a)
		}
	}
	if len(button) != 1 || button[0].href != AppURL {
		t.Errorf("button anchors = %+v, want one with href %s", button, AppURL)
	}
	if len(fallback) != 1 || fallback[0].href != AppURL {
		t.Errorf("fallback anchors = %+v, want one whose href and text are %s", fallback, AppURL)
	}
}

func TestRenderStrandedAlert_NoInvoiceIsAnError(t *testing.T) {
	subject, out, err := RenderStrandedAlert(StrandedAlertMail{Workspace: "Okafor & Co", More: 3})
	if err == nil || out != "" || subject != "" {
		t.Errorf("got (%q, %q, %v), want an error and empty results", subject, out, err)
	}
}
