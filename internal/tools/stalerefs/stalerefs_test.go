package main

import (
	"strings"
	"testing"
)

// fakeGrep answers Scan's tree lookups from a fixture, so a known positive can
// be pinned without depending on git history or CI fetch depth.
func fakeGrep(tree map[string][]Hit) func(string) []Hit {
	return func(lit string) []Hit { return tree[lit] }
}

func defaultAllow() Allow {
	return Allow{Files: []string{"e2e/envCopyStrings.ts", "e2e/envCopy.test.ts"}}
}

func find(t *testing.T, got []Finding, lit string) Finding {
	t.Helper()
	for _, f := range got {
		if f.Literal == lit {
			return f
		}
	}
	t.Fatalf("no finding for %q; got %d findings: %v", lit, len(got), literalsOf(got))
	return Finding{}
}

func literalsOf(f []Finding) []string {
	out := make([]string, 0, len(f))
	for _, x := range f {
		out = append(out, x.Literal)
	}
	return out
}

// TestScan_DEMO01ToastRename is the first known positive, taken verbatim from
// commit bb728b1a. Cycle 2 of DEMO-01 reworded the support console's toast tag
// without grepping e2e/ for assertions on it; two smoke specs still asserted the
// old text, and the deploy gate failed (run 30712481388, one re-trigger).
//
// Three separate versions of this scan reported ZERO on this input while it was
// being written. This test exists so the fourth cannot.
func TestScan_DEMO01ToastRename(t *testing.T) {
	diff := `--- a/frontend/support-console/src/App.tsx
+++ b/frontend/support-console/src/App.tsx
@@ -41,1 +41,1 @@
-    showToast('Re-drive queued · ' + id, 'AUDIT LOGGED')
+    showToast('Re-drive queued · ' + id, 'AUDIT ON ACCREDITATION')
@@ -54,1 +54,1 @@
-    showToast('Cancelled · ' + id, 'AUDIT LOGGED', 'red')
+    showToast('Cancelled · ' + id, 'AUDIT ON ACCREDITATION', 'red')
--- a/e2e/envCopyStrings.ts
+++ b/e2e/envCopyStrings.ts
@@ -27,0 +28,1 @@
+  'AUDIT LOGGED',
`
	tree := map[string][]Hit{
		"AUDIT LOGGED": {
			{"e2e/envCopy.test.ts", 129, `      "showToast('Cancelled', 'AUDIT LOGGED')",`},
			{"e2e/envCopyStrings.ts", 28, `  'AUDIT LOGGED',`},
			{"e2e/smoke/support-console.spec.ts", 185, `  // where the ops console's does not, and asserting the message and the AUDIT LOGGED tag on`},
			{"e2e/smoke/support-console.spec.ts", 191, `  await expect(toast).toContainText('AUDIT LOGGED')`},
			{"e2e/smoke/support-console.spec.ts", 311, `  await expect(toast).toContainText('AUDIT LOGGED')`},
			{"frontend/app/src/envPosture.test.ts", 30, `  'AUDIT LOGGED',`},
		},
	}

	got := Scan(diff, fakeGrep(tree), defaultAllow())
	f := find(t, got, "AUDIT LOGGED")

	if len(f.Stale) != 2 {
		t.Fatalf("stale references = %d, want exactly the 2 real assertions; got %+v", len(f.Stale), f.Stale)
	}
	for i, want := range []int{191, 311} {
		if f.Stale[i].Path != "e2e/smoke/support-console.spec.ts" || f.Stale[i].Line != want {
			t.Errorf("stale[%d] = %s:%d, want e2e/smoke/support-console.spec.ts:%d",
				i, f.Stale[i].Path, f.Stale[i].Line, want)
		}
	}
	if len(f.GoneOn) != 1 || f.GoneOn[0] != "frontend/support-console/src/App.tsx" {
		t.Errorf("GoneOn = %v, want [frontend/support-console/src/App.tsx]", f.GoneOn)
	}
}

// TestScan_APPR15DeletedInviteModal is the second known positive, from commit
// 6f657b8c. InviteMembersModal.tsx was deleted while e2e/topology/roles.spec.ts
// still drove three of its test ids; the specs were repaired two commits later
// in b7db6c4.
//
// It doubles as the regression test for diff attribution: git prints
// `+++ /dev/null` for a deleted file, and keying the path off the `+++` side
// blamed these removals on whichever file the diff happened to list before.
func TestScan_APPR15DeletedInviteModal(t *testing.T) {
	diff := `--- a/frontend/app/src/App.tsx
+++ b/frontend/app/src/App.tsx
@@ -12,1 +12,0 @@
-import { InviteMembersModal } from './components/InviteMembersModal'
--- a/frontend/app/src/components/InviteMembersModal.tsx
+++ /dev/null
@@ -18,3 +0,0 @@
-    <div data-testid="invite-modal">
-      <p data-testid="invite-wfrole-helper">{helper}</p>
-      <button data-testid="invite-cancel" onClick={close}>Cancel</button>
`
	tree := map[string][]Hit{
		"invite-modal":         {{"e2e/topology/roles.spec.ts", 249, `  await expect(page.getByTestId('invite-modal')).toBeVisible()`}},
		"invite-wfrole-helper": {{"e2e/topology/roles.spec.ts", 252, `  await expect(page.getByTestId('invite-wfrole-helper')).toHaveText(MOCK_INVITE)`}},
		"invite-cancel":        {{"e2e/topology/roles.spec.ts", 253, `  await page.getByTestId('invite-cancel').click()`}},
	}

	got := Scan(diff, fakeGrep(tree), defaultAllow())
	if len(got) != 3 {
		t.Fatalf("findings = %v, want all three invite test ids", literalsOf(got))
	}
	for _, lit := range []string{"invite-modal", "invite-wfrole-helper", "invite-cancel"} {
		f := find(t, got, lit)
		if len(f.GoneOn) != 1 || f.GoneOn[0] != "frontend/app/src/components/InviteMembersModal.tsx" {
			t.Errorf("%q GoneOn = %v, want the DELETED file, not whatever preceded it in the diff", lit, f.GoneOn)
		}
	}
}

// TestScan_DeletedConsumerFileIsNeverReadAsAProducer pins the other half of the
// attribution bug. Deleting e2e/topology/roleFixtures.ts — a consumer — used to
// dump every literal in it onto the producer listed before it, which invented 14
// findings in one PR.
func TestScan_DeletedConsumerFileIsNeverReadAsAProducer(t *testing.T) {
	diff := `--- a/db/seed.dev.sql
+++ b/db/seed.dev.sql
@@ -10,1 +10,1 @@
-INSERT INTO x VALUES ('unrelated seed row');
+INSERT INTO x VALUES ('unrelated seed row v2');
--- a/e2e/topology/roleFixtures.ts
+++ /dev/null
@@ -155,2 +0,0 @@
-  { member: 'Chidi Anyanwu', text: 'Engagement Manager +1' },
-  usage: '2 approval steps · 2 policies',
`
	tree := map[string][]Hit{
		"Engagement Manager +1":         {{"e2e/topology/roles.spec.ts", 266, `  text: 'Engagement Manager +1',`}},
		"2 approval steps · 2 policies": {{"e2e/topology/roles.spec.ts", 120, `    usage: '2 approval steps · 2 policies',`}},
	}

	if got := Scan(diff, fakeGrep(tree), defaultAllow()); len(got) != 0 {
		t.Fatalf("findings = %v, want none: those literals were deleted from a CONSUMER, not a producer", literalsOf(got))
	}
}

// TestScan_RetiringAStringIntoAForbiddenListStillReportsIt pins the per-file
// scoping of the added-set. DEMO-01 reworded the toast AND added the retired
// text to a forbidden-copy registry in the same commit; a global "was it
// re-added anywhere" test read that as "still produced" and suppressed the
// finding entirely.
func TestScan_RetiringAStringIntoAForbiddenListStillReportsIt(t *testing.T) {
	diff := `--- a/frontend/app/src/components/Banner.tsx
+++ b/frontend/app/src/components/Banner.tsx
@@ -8,1 +8,1 @@
-  return <Row sub="Sent to NRS" />
+  return <Row sub="Queued for NRS" />
--- a/frontend/app/src/envPosture.test.ts
+++ b/frontend/app/src/envPosture.test.ts
@@ -30,0 +31,1 @@
+  'Sent to NRS',
`
	tree := map[string][]Hit{
		"Sent to NRS": {
			{"frontend/app/src/envPosture.test.ts", 31, `  'Sent to NRS',`},
			{"e2e/topology/connectors.spec.ts", 77, `  await expect(page.getByTestId('status')).toHaveText('Sent to NRS')`},
		},
	}

	f := find(t, Scan(diff, fakeGrep(tree), defaultAllow()), "Sent to NRS")
	if len(f.Stale) != 1 || f.Stale[0].Line != 77 {
		t.Fatalf("stale = %+v, want the connectors spec assertion at line 77", f.Stale)
	}
}

// TestScan_SurvivingProducerMeansTheStringMoved is the dominant false-positive
// class: a mock promoted into db/seed.dev.sql, where the e2e assertion on it
// stays correct. Before this rule the scan reported 487 findings over 16 PRs.
func TestScan_SurvivingProducerMeansTheStringMoved(t *testing.T) {
	diff := `--- a/frontend/app/src/lib/roles.ts
+++ b/frontend/app/src/lib/roles.ts
@@ -4,1 +4,0 @@
-  { id: 'preparer', title: 'Invoice Preparer' },
`
	tree := map[string][]Hit{
		"Invoice Preparer": {
			{"db/seed.dev.sql", 88, `INSERT INTO workflow_roles (title) VALUES ('Invoice Preparer');`},
			{"e2e/topology/roles.spec.ts", 107, `    title: 'Invoice Preparer',`},
		},
	}

	if got := Scan(diff, fakeGrep(tree), defaultAllow()); len(got) != 0 {
		t.Fatalf("findings = %v, want none: db/seed.dev.sql still produces it", literalsOf(got))
	}
}

// TestScan_CommentsAndAbsenceAssertionsAreNotStaleReferences pins the two
// suppressions. A spec that asserts the copy is GONE, or a comment explaining
// that it went, does not depend on the string still being produced.
//
// This filter silently did nothing for two versions: `git grep` with a rev
// prefixes its output `rev:path:line:text`, and slicing the wrong field handed
// the matcher "191:  // ..." so the leading-comment pattern never matched.
func TestScan_CommentsAndAbsenceAssertionsAreNotStaleReferences(t *testing.T) {
	diff := `--- a/frontend/app/src/components/XmlModal.tsx
+++ b/frontend/app/src/components/XmlModal.tsx
@@ -30,1 +30,1 @@
-      <button>View XML</button>
+      <button>View UBL</button>
`
	tree := map[string][]Hit{
		"View XML": {
			{"e2e/topology/invoice-surfaces.spec.ts", 1902, `  await expect(page.getByTestId('ubl-modal')).not.toContainText('View XML')`},
			{"docs/e2e-convention.md", 44, `<!-- the old 'View XML' label was retired in BUG-04 -->`},
		},
	}

	if got := Scan(diff, fakeGrep(tree), defaultAllow()); len(got) != 0 {
		t.Fatalf("findings = %v, want none: both survivors record the removal", literalsOf(got))
	}
}

// TestScan_ALongerSiblingTokenDoesNotMaskTheRename pins the one bug the fixture
// tests structurally could not catch, because they hand Scan its grep results.
// Renaming the test id `detail-submit-confirm` while `detail-submit-confirm-prompt`
// survives used to read as "a producer still emits it", and the gate passed a
// live break. Found only by running the whole gate against a real rename.
func TestScan_ALongerSiblingTokenDoesNotMaskTheRename(t *testing.T) {
	diff := `--- a/frontend/app/src/components/InvoiceDetail.tsx
+++ b/frontend/app/src/components/InvoiceDetail.tsx
@@ -671,1 +671,1 @@
-                    <button data-testid="detail-submit-confirm" onClick={go}>
+                    <button data-testid="detail-submit-go" onClick={go}>
`
	tree := map[string][]Hit{
		"detail-submit-confirm": {
			// `git grep -F` returns this: the literal is a PREFIX of a live sibling.
			{"frontend/app/src/components/InvoiceDetail.tsx", 717, `      <div data-testid="detail-submit-confirm-prompt" style={{ fontSize: 11.5 }}>`},
			{"e2e/topology/invoice-surfaces.spec.ts", 1335, `  const prompt = page.getByTestId('detail-submit-confirm-prompt')`},
			{"e2e/topology/invoice-surfaces.spec.ts", 1349, `  await page.getByTestId('detail-submit-confirm').click()`},
		},
	}

	f := find(t, Scan(diff, fakeGrep(tree), defaultAllow()), "detail-submit-confirm")
	if len(f.Stale) != 1 || f.Stale[0].Line != 1349 {
		t.Fatalf("stale = %+v, want only the exact-token reference at line 1349", f.Stale)
	}
}

func TestWholeMatch(t *testing.T) {
	if WholeMatch(`getByTestId('detail-submit-confirm-prompt')`, "detail-submit-confirm") {
		t.Error("matched inside a longer identifier")
	}
	if !WholeMatch(`getByTestId('detail-submit-confirm')`, "detail-submit-confirm") {
		t.Error("failed to match a complete token")
	}
	// A prose literal extended by punctuation is still a real reference.
	if !WholeMatch(`sub="Sent to NRS · IRN assigned"`, "Sent to NRS") {
		t.Error("punctuation should not count as an identifier boundary")
	}
}

func TestRole(t *testing.T) {
	// An e2e spec matches the unit-test pattern too. Consumer must win: it is
	// the single most important consumer in the tree.
	for path, want := range map[string]string{
		"e2e/smoke/support-console.spec.ts":    "consumer",
		"e2e/topology/roleFixtures.ts":         "consumer",
		"docs/deploy-model.md":                 "consumer",
		".github/workflows/dev-env.yml":        "consumer",
		"frontend/app/src/lib/members.ts":      "producer",
		"internal/invoice/handlers.go":         "producer",
		"db/seed.dev.sql":                      "producer",
		"internal/invoice/handlers_test.go":    "ignore",
		"frontend/app/src/lib/members.test.ts": "ignore",
	} {
		if got := Role(path); got != want {
			t.Errorf("Role(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestDistinctive(t *testing.T) {
	keep := []string{"AUDIT LOGGED", "invite-wfrole-helper", "SEED_FIRM_POLICIES",
		"/v1/invoices/submit", "The tax authority rejected this invoice"}
	drop := []string{"16px 20px", "100%", ".mono", "0 24px 60px -20px oklch(20% .02 210 / 0.4)",
		") return ", "'all' | number[]", "https://example.com/x", "singleword"}

	for _, s := range keep {
		if !Distinctive(s) {
			t.Errorf("Distinctive(%q) = false, want true", s)
		}
	}
	for _, s := range drop {
		if Distinctive(s) {
			t.Errorf("Distinctive(%q) = true, want false", s)
		}
	}
}

func TestLiterals(t *testing.T) {
	got := Literals(`showToast('Re-drive queued', "AUDIT LOGGED", ` + "`ok`" + `)`)
	joined := strings.Join(got, "|")
	if !strings.Contains(joined, "AUDIT LOGGED") || !strings.Contains(joined, "Re-drive queued") {
		t.Fatalf("Literals = %v, want both quoted strings", got)
	}
	// `ok` is 2 chars, below the 6-char floor.
	if strings.Contains(joined, "ok") {
		t.Errorf("Literals = %v, want the 2-char literal dropped", got)
	}
}
