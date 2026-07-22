// M5-02-01 (task-217), Stage 2.5: like TestValidatorClient_DoesNotImportValidationPackage
// (internal/invoice/validator_test.go:440-450), this is a baseline/regression guard, not a
// strict red-to-green spec -- the property it checks already holds for any implementation
// that declares Canonical as this subtask's canonical.go does (invoice content only, no
// tenant id, no status, no violations). It exists to lock the declared shape against later
// drift, not to demonstrate a red-to-green transition, and it passes from this subtask's
// first commit onward.
//
// Package submission_test (external), matching every other test file in this package.
// TestMain already exists at failure_modes_test.go:57 -- one per test binary -- so this file
// defines none.
package submission_test

import (
	"reflect"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// TestCanonical_CarriesNoTenantOrStatus (AC-4, regression guard): Canonical has no TenantID,
// Status, Violations or RuleSetVersionID field -- it is 05's invoice-content projection, not
// a tenant- or validation-scoped record [canonical-is-invoice-content].
func TestCanonical_CarriesNoTenantOrStatus(t *testing.T) {
	typ := reflect.TypeOf(submission.Canonical{})
	forbidden := map[string]bool{
		"TenantID":         true,
		"Status":           true,
		"Violations":       true,
		"RuleSetVersionID": true,
	}
	for i := 0; i < typ.NumField(); i++ {
		if name := typ.Field(i).Name; forbidden[name] {
			t.Errorf("Canonical has a %q field, want none (Core AC-4: canonical is invoice content only)", name)
		}
	}
}
