// QA gap-fill (BUG-06-02, task-384): failure.go's Valid() shipped with no
// test of its own -- pure unit tests, package submission_test per this
// package's dominant convention (result_test.go, canonical_test.go).
package submission_test

import (
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

func TestFailureKind_ValidAcceptsExactlyTheThreeConstants(t *testing.T) {
	for _, k := range []submission.FailureKind{
		submission.FailurePayloadNotBuilt,
		submission.FailureNeverAcknowledged,
		submission.FailureAcknowledgedNoVerdict,
	} {
		if !k.Valid() {
			t.Errorf("%q.Valid() = false, want true", k)
		}
	}
}

func TestFailureKind_ValidRejectsBlank(t *testing.T) {
	if submission.FailureKind("").Valid() {
		t.Error(`FailureKind("").Valid() = true, want false`)
	}
}

func TestFailureKind_ValidRejectsUnknownValue(t *testing.T) {
	if submission.FailureKind("app_rejected").Valid() {
		t.Error(`FailureKind("app_rejected").Valid() = true, want false`)
	}
}

func TestFailureKind_ValidRejectsCaseVariant(t *testing.T) {
	if submission.FailureKind("PAYLOAD_NOT_BUILT").Valid() {
		t.Error(`FailureKind("PAYLOAD_NOT_BUILT").Valid() = true, want false`)
	}
}
