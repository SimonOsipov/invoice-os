package auth

import (
	"reflect"
	"testing"
)

// Red stub for ENGI-11-03: Identity.Staff/RulesRole and MintOptions.Staff/RulesRole do not exist yet.
// Executor: delete this file and add to staff_claims_test.go:
//
//	func flagsOf(_ *testing.T, id Identity) (bool, bool) { return id.Staff, id.RulesRole }
//	func staffOpts(_ *testing.T, o MintOptions, staff, rules bool) MintOptions {
//		o.Staff, o.RulesRole = staff, rules
//		return o
//	}

func flagsOf(t *testing.T, id Identity) (staff, rules bool) {
	t.Helper()
	v := reflect.ValueOf(id)
	s, r := v.FieldByName("Staff"), v.FieldByName("RulesRole")
	if !s.IsValid() || !r.IsValid() {
		t.Fatal("Identity has no Staff/RulesRole field")
	}
	return s.Bool(), r.Bool()
}

func staffOpts(t *testing.T, o MintOptions, staff, rules bool) MintOptions {
	t.Helper()
	v := reflect.ValueOf(&o).Elem()
	s, r := v.FieldByName("Staff"), v.FieldByName("RulesRole")
	if !s.IsValid() || !r.IsValid() {
		t.Fatal("MintOptions has no Staff/RulesRole field")
	}
	s.SetBool(staff)
	r.SetBool(rules)
	return o
}
