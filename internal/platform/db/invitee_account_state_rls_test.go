package db_test

import (
	"context"
	"reflect"
	"testing"
)

const accountStateSig = "public.invitee_account_state(text)"

// The function runs where auth.users does not exist, so this checks its shape and ACL only.
func TestRLS_InviteeAccountStateIsOwnedAndGrantedNarrowly(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	s, ok := readProc(t, accountStateSig)
	if !ok {
		t.Fatalf("function %s does not exist", accountStateSig)
	}
	if s.owner != "auth_hook_reader" {
		t.Errorf("owner = %q, want auth_hook_reader", s.owner)
	}
	if !s.secdef {
		t.Errorf("prosecdef = false, want SECURITY DEFINER")
	}
	if !reflect.DeepEqual(s.config, []string{`search_path=""`}) {
		t.Errorf("proconfig = %q, want [search_path=\"\"]", s.config)
	}
	if s.volatile != "s" {
		t.Errorf("provolatile = %q, want s (STABLE)", s.volatile)
	}
	var result string
	if err := h.super.QueryRow(ctx, `SELECT pg_get_function_result(to_regprocedure($1))`, accountStateSig).Scan(&result); err != nil || result != "text" {
		t.Errorf("function result = %q, err %v; want text", result, err)
	}
	assertExecuteOnlyForApp(t, accountStateSig, s, "auth_hook_reader")

	var state string
	err := h.reader.QueryRow(ctx, `SELECT public.invitee_account_state($1::text)`, "who@example.test").Scan(&state)
	if code := pgCode(err); code != "42501" {
		t.Errorf("invoice_tenant_reader call = %q, err %v (SQLSTATE %q); want 42501", state, err, code)
	}
}
