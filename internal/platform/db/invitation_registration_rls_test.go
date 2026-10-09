package db_test

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const pendingForEmailSig = "public.invitation_pending_for_email(text)"

// uniqueAddress keeps the any-tenant lookup from matching rows other tests leave behind.
func uniqueAddress(local string) string {
	return local + "-" + uuid.NewString()[:8] + "@firm.test"
}

func registeredHash(t *testing.T, id string) []byte {
	t.Helper()
	var got []byte
	if err := h.super.QueryRow(context.Background(), `SELECT registered_token_hash FROM invitations WHERE id = $1`, id).Scan(&got); err != nil {
		t.Fatalf("read registered_token_hash: %v", err)
	}
	return got
}

// pendingFor calls the lookup as invoice_app; an empty guc leaves app.current_tenant unset.
func pendingFor(t *testing.T, guc, email string) bool {
	t.Helper()
	var got bool
	err := inAppTx(context.Background(), guc, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT public.invitation_pending_for_email($1::text)`, email).Scan(&got)
	})
	if err != nil {
		t.Fatalf("invitation_pending_for_email(%q) as invoice_app (GUC %q): %v", email, guc, err)
	}
	return got
}

func TestRLS_RegisteredTokenHashIsNullableAndThirtyTwoBytes(t *testing.T) {
	requireHarness(t)
	tenant := newNamedTenant(t, "Registration Firm")
	id := seedAcceptInvite(t, tenant, "reviewer", uniqueAddress("tunde"), newToken(t))

	if got := registeredHash(t, id); got != nil {
		t.Fatalf("fresh invite registered_token_hash = %x, want NULL", got)
	}

	want := hashOf(newToken(t))
	if len(want) != 32 {
		t.Fatalf("fixture hash is %d bytes, want 32", len(want))
	}
	if _, err := h.super.Exec(context.Background(), `UPDATE invitations SET registered_token_hash = $2 WHERE id = $1`, id, want); err != nil {
		t.Fatalf("set a 32-byte registered_token_hash: %v", err)
	}
	if got := registeredHash(t, id); !bytes.Equal(got, want) {
		t.Fatalf("registered_token_hash = %x, want %x", got, want)
	}

	_, err := h.super.Exec(context.Background(), `UPDATE invitations SET registered_token_hash = $2 WHERE id = $1`, id, want[:31])
	assertAcceptRefusal(t, "31-byte registered_token_hash", err, "23514", "invitations_registered_token_hash_len")
	if got := registeredHash(t, id); !bytes.Equal(got, want) {
		t.Errorf("registered_token_hash after the refused update = %x, want the 32-byte value %x", got, want)
	}
}

func TestRLS_InvitationPendingForEmailIsADefinerOwnedByTheHookReader(t *testing.T) {
	requireHarness(t)

	s, ok := readProc(t, pendingForEmailSig)
	if !ok {
		t.Fatalf("function %s does not exist", pendingForEmailSig)
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
	assertExecuteOnlyForApp(t, pendingForEmailSig, s, "auth_hook_reader")
}

func TestRLS_InvitationPendingForEmailSeesEveryTenant(t *testing.T) {
	requireHarness(t)
	one := newNamedTenant(t, "Firm One")
	two := newNamedTenant(t, "Firm Two")
	a, b := uniqueAddress("a"), uniqueAddress("b")
	seedAcceptInvite(t, one, "reviewer", a, newToken(t))
	seedAcceptInvite(t, two, "reviewer", b, newToken(t))

	for _, c := range []struct{ name, guc string }{
		{"guc unset", ""},
		{"guc nil uuid", uuid.Nil.String()},
		{"guc tenant one", one},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, addr := range []string{a, b} {
				if !pendingFor(t, c.guc, addr) {
					t.Errorf("pending_for_email(%q) = false, want true", addr)
				}
			}
		})
	}
}

func TestRLS_InvitationPendingForEmailIgnoresSpentExpiredAndOtherAddresses(t *testing.T) {
	requireHarness(t)
	tenant := newNamedTenant(t, "Spent Firm")
	c, d, e, f := uniqueAddress("c"), uniqueAddress("d"), uniqueAddress("e"), uniqueAddress("f")
	idC := seedAcceptInvite(t, tenant, "reviewer", c, newToken(t))
	idD := seedAcceptInvite(t, tenant, "reviewer", d, newToken(t))
	idE := seedAcceptInvite(t, tenant, "reviewer", e, newToken(t))
	seedAcceptInvite(t, tenant, "reviewer", f, newToken(t))
	setInviteState(t, idC, `status = 'accepted'`)
	setInviteState(t, idD, `status = 'revoked'`)
	setInviteState(t, idE, `expires_at = now() - interval '1 second'`)

	if !pendingFor(t, "", f) {
		t.Fatalf("control: pending invite of %q = false, want true", f)
	}
	for _, tc := range []struct{ name, addr string }{
		{"accepted", c},
		{"revoked", d},
		{"expired", e},
		{"another address", uniqueAddress("g")},
	} {
		if pendingFor(t, "", tc.addr) {
			t.Errorf("%s: pending_for_email(%q) = true, want false", tc.name, tc.addr)
		}
	}
}

func TestRLS_InvitationPendingForEmailFoldsCaseAndSpaces(t *testing.T) {
	requireHarness(t)
	tenant := newNamedTenant(t, "Fold Firm")
	local := "pat-" + uuid.NewString()[:8]
	seedAcceptInvite(t, tenant, "reviewer", local+"@firm.test", newToken(t))

	if !pendingFor(t, "", local+"@firm.test") {
		t.Fatalf("control: exact address = false, want true")
	}
	if !pendingFor(t, "", "  "+local+"@Firm.Test ") {
		t.Errorf("padded, mixed-case address = false, want true")
	}
	if pendingFor(t, "", "  "+local+"x@Firm.Test ") {
		t.Errorf("a different address after folding = true, want false")
	}
}

func TestRLS_RegisteredTokenHashUpdateIsTenantScoped(t *testing.T) {
	requireHarness(t)
	one := newNamedTenant(t, "Scope Firm One")
	two := newNamedTenant(t, "Scope Firm Two")
	idOne := seedAcceptInvite(t, one, "reviewer", uniqueAddress("p"), newToken(t))
	idTwo := seedAcceptInvite(t, two, "reviewer", uniqueAddress("q"), newToken(t))
	hash := hashOf(newToken(t))

	var affected int64
	err := inAppTx(context.Background(), one, func(tx pgx.Tx) error {
		tag, err := tx.Exec(context.Background(),
			`UPDATE invitations SET registered_token_hash = $1 WHERE id::text = ANY($2::text[])`, hash, []string{idOne, idTwo})
		affected = tag.RowsAffected()
		return err
	})
	if err != nil {
		t.Fatalf("update registered_token_hash as invoice_app (GUC tenant one): %v", err)
	}
	if affected != 1 {
		t.Errorf("rows affected = %d, want 1 (tenant one only)", affected)
	}
	if got := registeredHash(t, idOne); !bytes.Equal(got, hash) {
		t.Errorf("tenant one registered_token_hash = %x, want %x", got, hash)
	}
	if got := registeredHash(t, idTwo); got != nil {
		t.Errorf("tenant two registered_token_hash = %x, want NULL", got)
	}
}
