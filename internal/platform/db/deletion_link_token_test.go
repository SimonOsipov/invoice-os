package db_test

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func setLinkToken(t *testing.T, tenant, user, token string) (bool, error) {
	t.Helper()
	return db.SetDeletionLinkToken(context.Background(), os.Getenv("DATABASE_MIGRATION_URL"),
		uuid.MustParse(tenant), uuid.MustParse(user), token)
}

// seedLiveLink schedules the tenant and gives user a link expiring at the due date.
func seedLiveLink(t *testing.T, tenant, user string) {
	t.Helper()
	wdSchedule(t, tenant, "now()")
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO workspace_deletion_links (tenant_id, recipient_user_id, token_hash, expires_at)
		 SELECT id, $2, $3, deletion_due_at FROM tenants WHERE id = $1`, tenant, user, dlHash()); err != nil {
		t.Fatalf("seed link: %v", err)
	}
}

func linkHashOf(t *testing.T, tenant, user string) []byte {
	t.Helper()
	var got []byte
	if err := h.super.QueryRow(context.Background(),
		`SELECT token_hash FROM workspace_deletion_links WHERE tenant_id = $1 AND recipient_user_id = $2`, tenant, user).Scan(&got); err != nil {
		t.Fatalf("read token_hash: %v", err)
	}
	return got
}

func TestRLS_SetDeletionLinkTokenReplacesTheLiveHash(t *testing.T) {
	dlRequire(t, dlLinks)
	tenant, admin := dlTenant(t), uuid.NewString()
	seedLiveLink(t, tenant, admin)
	tok := newToken(t)

	ok, err := setLinkToken(t, tenant, admin, tok)
	if err != nil || !ok {
		t.Fatalf("SetDeletionLinkToken = (%v, %v), want (true, nil)", ok, err)
	}
	if !bytes.Equal(linkHashOf(t, tenant, admin), hashOf(tok)) {
		t.Error("token_hash is not sha256 of the token")
	}
}

func TestRLS_SetDeletionLinkTokenFindsNoLinkWhereNoneIsLive(t *testing.T) {
	dlRequire(t, dlLinks)
	tenant, admin := dlTenant(t), uuid.NewString()
	seedLiveLink(t, tenant, admin)
	before := linkHashOf(t, tenant, admin)

	// The scheduler holds no link row at all: its id is a user with no row.
	cleared, clearedAdmin := dlTenant(t), uuid.NewString()
	seedLiveLink(t, cleared, clearedAdmin)
	wdClear(t, cleared)
	clearedBefore := linkHashOf(t, cleared, clearedAdmin)

	for _, c := range []struct{ name, tenant, user string }{
		{"the scheduler", tenant, uuid.NewString()},
		{"a random user", tenant, uuid.NewString()},
		{"the admin under another tenant id", dlTenant(t), admin},
		{"the admin after the schedule is cleared", cleared, clearedAdmin},
	} {
		if ok, err := setLinkToken(t, c.tenant, c.user, newToken(t)); err != nil || ok {
			t.Errorf("%s: SetDeletionLinkToken = (%v, %v), want (false, nil)", c.name, ok, err)
		}
	}
	if !bytes.Equal(linkHashOf(t, tenant, admin), before) {
		t.Error("a hash changed for a call that found no live link")
	}
	if !bytes.Equal(linkHashOf(t, cleared, clearedAdmin), clearedBefore) {
		t.Error("the cleared deletion's hash changed")
	}
}
