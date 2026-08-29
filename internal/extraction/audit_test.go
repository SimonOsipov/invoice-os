// audit_test.go: the audit seam seen from outside the package, which is where subtask 02's
// adapter sits. audit_internal_test.go pins the shape; this pins the reachable surface.
package extraction_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/jackc/pgx/v5"
)

// auditKinds is the vocabulary under test. audit_internal_test.go owns the assertion that
// these are the only four; here they are the seed for the near-miss set below.
var auditKinds = []extraction.FailureKind{
	extraction.FailureDocumentUnavailable,
	extraction.FailurePagesNotRendered,
	extraction.FailurePageRowsNotWritten,
	extraction.FailureExtractFailed,
}

// auditNearMisses derives the spellings a hand-written caller or a hand-edited SQL filter
// produces. Every one must be refused: audit_log is append-only, so a row written under a
// near-miss is permanently unreadable by the four-value vocabulary.
func auditNearMisses(v string) []string {
	return []string{
		strings.ToUpper(v),
		strings.ToUpper(v[:1]) + v[1:],
		" " + v,
		v + " ",
		v + "\n",
		strings.ReplaceAll(v, "_", "-"),
		strings.ReplaceAll(v, "_", ""),
		strings.ReplaceAll(v, "_", "."),
		v[:len(v)-1],
		v + "_",
	}
}

func TestFailureKind_ValidRejectsNearMissesOfEveryValue(t *testing.T) {
	if len(auditKinds) != 4 {
		t.Fatalf("seeded %d kind(s), want 4; the derivation below would under-cover", len(auditKinds))
	}

	// Positive half: without it, Valid() returning false for everything passes the rejections.
	for _, k := range auditKinds {
		if !k.Valid() {
			t.Errorf("FailureKind(%q).Valid() = false, want true", k)
		}
	}

	accepted := map[string]bool{}
	for _, k := range auditKinds {
		accepted[string(k)] = true
	}

	checked := 0
	for _, k := range auditKinds {
		for _, near := range auditNearMisses(string(k)) {
			// A derivation that lands back on a real value proves nothing.
			if accepted[near] {
				t.Errorf("near-miss derivation of %q produced the accepted value %q; the case below is vacuous", k, near)
				continue
			}
			checked++
			if extraction.FailureKind(near).Valid() {
				t.Errorf("FailureKind(%q).Valid() = true, want false; it is a near-miss of %q", near, k)
			}
		}
	}
	if checked < 4*len(auditNearMisses("a_b")) {
		t.Errorf("checked %d near-miss(es), want %d; a shrunken set reports clean over spellings nobody tried", checked, 4*len(auditNearMisses("a_b")))
	}
}

func TestExtractionAudit_ZeroValueCarriesNoValidFailureKind(t *testing.T) {
	var zero extraction.ExtractionAudit

	// The zero value reads as a failure (Succeeded false) whose kind is unset, so an adapter
	// gating its failure branch on Valid() refuses it. This is the claim audit.go's Valid()
	// doc comment makes; nothing else tests it.
	if zero.Succeeded {
		t.Errorf("the zero ExtractionAudit reports Succeeded = true")
	}
	if zero.FailureKind.Valid() {
		t.Errorf("the zero ExtractionAudit's FailureKind %q is Valid(); a half-filled failure would pass the adapter's gate", zero.FailureKind)
	}

	// Not vacuous: a filled failure passes the same gate.
	filled := extraction.ExtractionAudit{FailureKind: extraction.FailureExtractFailed}
	if !filled.FailureKind.Valid() {
		t.Errorf("a filled failure's FailureKind %q is not Valid(); the gate refuses everything", filled.FailureKind)
	}
}

func TestRecordExtractionAudit_IsSatisfiableFromOutsideThePackage(t *testing.T) {
	// Every field set by name from another package: the adapter in cmd/submission builds this
	// struct, so an unexported or renamed field breaks here at compile time.
	failure := extraction.ExtractionAudit{
		Succeeded:        false,
		DocumentID:       "11111111-1111-1111-1111-111111111111",
		ExtractionJobID:  "22222222-2222-2222-2222-222222222222",
		Extractor:        "docling",
		ExtractorVersion: "v1",
		FieldCount:       0,
		FlaggedCount:     0,
		State:            "dead_lettered",
		FailureKind:      extraction.FailurePageRowsNotWritten,
	}
	success := failure
	success.Succeeded = true
	success.State = "succeeded"
	success.FieldCount = 7
	success.FlaggedCount = 2
	success.FailureKind = ""

	var seen []extraction.ExtractionAudit
	sentinel := errors.New("recorder refused")
	var rec extraction.RecordExtractionAudit = func(_ context.Context, tx pgx.Tx, ev extraction.ExtractionAudit) error {
		if tx != nil {
			t.Errorf("recorder saw a non-nil tx; this test passes nil")
		}
		seen = append(seen, ev)
		if ev.Succeeded {
			return nil
		}
		return sentinel
	}

	if err := rec(t.Context(), nil, success); err != nil {
		t.Errorf("recorder returned %v on the success payload, want nil", err)
	}
	// The seam propagates the recorder's error: the audit write shares the worker's
	// transaction, so a swallowed error would commit the job without its row.
	if err := rec(t.Context(), nil, failure); !errors.Is(err, sentinel) {
		t.Errorf("recorder returned %v on the failure payload, want %v", err, sentinel)
	}

	if len(seen) != 2 {
		t.Fatalf("recorder saw %d payload(s), want 2", len(seen))
	}
	if seen[0] != success {
		t.Errorf("recorder saw %+v, want %+v", seen[0], success)
	}
	if seen[1] != failure {
		t.Errorf("recorder saw %+v, want %+v", seen[1], failure)
	}
}
