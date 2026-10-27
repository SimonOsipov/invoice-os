package accountmail

import (
	"html"
	"strings"
	"testing"
	"time"
)

var okaforCancelled = DeletionCancelledMail{Workspace: "Okafor & Co", Canceller: "Bola Ade", DueAt: dueAt}

func renderCancelled(t *testing.T, m DeletionCancelledMail) (subject, out string) {
	t.Helper()
	subject, out, err := RenderDeletionCancelled(m)
	if err != nil {
		t.Fatalf("RenderDeletionCancelled(%+v): %v", m, err)
	}
	return subject, out
}

func TestDeletionCancelledTemplate_ComposesTheLayout(t *testing.T) {
	checkComposes(t, "deletion_cancelled")
}

func TestRenderDeletionCancelled_NamesWorkspaceCancellerAndDate(t *testing.T) {
	_, out := renderCancelled(t, okaforCancelled)
	want := "Bola Ade cancelled the deletion of Okafor & Co. It will not be deleted on 24 November 2026 (UTC)."
	if got := introWith(t, out, "cancelled the deletion of"); got != want {
		t.Errorf("intro = %q, want %q", got, want)
	}
	checkRows(t, out, [][2]string{
		{"Organisation", "Okafor & Co"}, {"Cancelled by", "Bola Ade"}, {"Was due", "24 November 2026, 18:10 UTC"},
	})
}

func TestRenderDeletionCancelled_ButtonOpensTheApp(t *testing.T) {
	_, out := renderCancelled(t, okaforCancelled)
	var button, fallback []anchor
	for _, a := range anchors(out) {
		switch a.text {
		case "Open ASComply":
			button = append(button, a)
		case AppURL:
			fallback = append(fallback, a)
		}
		if strings.ContainsAny(a.href, "#?") {
			t.Errorf("anchor %+v holds a fragment or query", a)
		}
	}
	if len(button) != 1 || button[0].href != AppURL {
		t.Errorf("button anchors = %+v, want one with href %s", button, AppURL)
	}
	if len(fallback) != 1 || fallback[0].href != AppURL {
		t.Errorf("fallback anchors = %+v, want one whose href and text are %s", fallback, AppURL)
	}
}

func TestRenderDeletionCancelled_SubjectIsFixed(t *testing.T) {
	subject, out := renderCancelled(t, DeletionCancelledMail{Workspace: "Pay now", Canceller: "Bank", DueAt: dueAt})
	if subject != DeletionCancelledSubject || DeletionCancelledSubject != "Your ASComply workspace is no longer scheduled for deletion" {
		t.Errorf("subject = %q, const = %q", subject, DeletionCancelledSubject)
	}
	for _, leak := range []string{"Pay now", "Bank"} {
		if strings.Contains(subject, leak) {
			t.Errorf("subject %q holds %q", subject, leak)
		}
	}
	if got := titleRe.FindStringSubmatch(out); got == nil || got[1] != DeletionCancelledSubject {
		t.Errorf("title = %q, want %q", got, DeletionCancelledSubject)
	}
}

func TestRenderDeletionCancelled_DatesAreUTC(t *testing.T) {
	checkDatesAreUTC(t, func(d time.Time) string {
		m := okaforCancelled
		m.DueAt = d
		_, out := renderCancelled(t, m)
		return out
	})
}

func TestRenderDeletionCancelled_EmptyCancellerAndZeroDate(t *testing.T) {
	m := okaforCancelled
	m.Canceller = ""
	_, out := renderCancelled(t, m)
	if got := introWith(t, out, "cancelled the deletion of"); !strings.HasPrefix(got, "A workspace admin cancelled") {
		t.Errorf("intro = %q, want it to start \"A workspace admin cancelled\"", got)
	}
	if _, got := mustRow(t, out, "Cancelled by"); got != "A workspace admin" {
		t.Errorf("Cancelled by = %q, want \"A workspace admin\"", got)
	}

	m.DueAt = time.Time{}
	subject, out, err := RenderDeletionCancelled(m)
	if err == nil || subject != "" || out != "" {
		t.Errorf("zero date: got subject %q, %d bytes, err %v; want an error and nothing else", subject, len(out), err)
	}
}

func TestRenderDeletionCancelled_EscapesHostileInputAndWraps(t *testing.T) {
	m := DeletionCancelledMail{Workspace: "<script>x</script>", Canceller: `" onmouseover="x`, DueAt: dueAt}
	_, out := renderCancelled(t, m)
	for _, bad := range []string{"<script", `onmouseover="`} {
		if strings.Contains(out, bad) {
			t.Errorf("hostile input leaves %q live in the output", bad)
		}
	}
	for label, want := range map[string]string{"Organisation": m.Workspace, "Cancelled by": m.Canceller} {
		_, inner := mustRow(t, out, label)
		if strings.ContainsAny(inner, `<>"`) || html.UnescapeString(inner) != want {
			t.Errorf("%s cell = %q, want the escaped text of %q", label, inner, want)
		}
	}

	long, canceller := strings.Repeat("W", 200), strings.Repeat("C", 200)
	_, out = renderCancelled(t, DeletionCancelledMail{Workspace: long, Canceller: canceller, DueAt: dueAt})
	for label, v := range map[string]string{"Organisation": long, "Cancelled by": canceller} {
		if !strings.Contains(out, v) {
			t.Fatalf("mail does not hold the long %s value", label)
		}
		if s, _ := mustRow(t, out, label); !strings.Contains(s, "overflow-wrap:anywhere") {
			t.Errorf("%s value cell style %q lacks overflow-wrap:anywhere", label, s)
		}
	}
	checkLayoutRules(t, out)
}
