package auth_test

import (
	"reflect"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// Red stub for ENGI-11-03: Identity.Staff/RulesRole do not exist yet.
// Executor: delete this file and add to idp_staff_integration_test.go:
//
//	func flagsOf(_ *testing.T, id auth.Identity) (bool, bool) { return id.Staff, id.RulesRole }

func flagsOf(t *testing.T, id auth.Identity) (staff, rules bool) {
	t.Helper()
	v := reflect.ValueOf(id)
	s, r := v.FieldByName("Staff"), v.FieldByName("RulesRole")
	if !s.IsValid() || !r.IsValid() {
		t.Fatal("Identity has no Staff/RulesRole field")
	}
	return s.Bool(), r.Bool()
}
