package archive

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

func TestClientScope_BundlePreviewAndRegisterRefuseAnotherClient(t *testing.T) {
	super := dbSuperPool(t)
	app, _ := dbAppPoolTraced(t)

	var entA, entB string
	user := uuid.NewString()
	tenant := mustCommitFixture(t, super, func(tx pgx.Tx) string {
		tid := mustCreateTenant(t, tx, "archive-client-scope")
		entA = mustCreateEntity(t, tx, tid, "Client A", "")
		entB = mustCreateEntity(t, tx, tid, "Client B", "")
		mustCreateInvoice(t, tx, invoiceFixture{tenantID: tid, entityID: entA, invoiceNumber: "CS-A-1"})
		mustCreateInvoice(t, tx, invoiceFixture{tenantID: tid, entityID: entB, invoiceNumber: "CS-B-1"})
		for _, q := range []string{
			`INSERT INTO memberships (tenant_id, user_id, role, status, all_clients) VALUES ($1, $2, 'reviewer', 'active', false)`,
			`INSERT INTO membership_clients (tenant_id, user_id, entity_id) VALUES ($1, $2, '` + entA + `')`,
		} {
			if _, err := tx.Exec(context.Background(), q, tid, user); err != nil {
				t.Fatalf("seed limited reviewer: %v", err)
			}
		}
		return tid
	})

	s := NewStore(app)
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: user, Role: "authenticated", TenantID: tenant})
	req := func(entity string) Request {
		now := time.Now().UTC()
		return Request{EntityID: entity, From: now.Add(-time.Hour), To: now.Add(time.Hour)}
	}
	exported := func() int {
		var n int
		if err := super.QueryRow(context.Background(),
			`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'invoice_register.exported'`, tenant).Scan(&n); err != nil {
			t.Fatalf("count audit rows: %v", err)
		}
		return n
	}

	if err := s.Assemble(ctx, req(entB), io.Discard, nil); !errors.Is(err, ErrEntityNotFound) {
		t.Errorf("Assemble(client B) err = %v, want ErrEntityNotFound", err)
	}
	if _, err := s.Preview(ctx, req(entB)); !errors.Is(err, ErrEntityNotFound) {
		t.Errorf("Preview(client B) err = %v, want ErrEntityNotFound", err)
	}
	if err := s.Register(ctx, req(entB), io.Discard, nil); !errors.Is(err, ErrEntityNotFound) {
		t.Errorf("Register(client B) err = %v, want ErrEntityNotFound", err)
	}
	if n := exported(); n != 0 {
		t.Errorf("invoice_register.exported rows after the refusals = %d, want 0", n)
	}

	if err := s.Assemble(ctx, req(entA), io.Discard, nil); err != nil {
		t.Errorf("Assemble(client A) control: %v", err)
	}
	if _, err := s.Preview(ctx, req(entA)); err != nil {
		t.Errorf("Preview(client A) control: %v", err)
	}
	if err := s.Register(ctx, req(entA), io.Discard, nil); err != nil {
		t.Errorf("Register(client A) control: %v", err)
	}
	if n := exported(); n != 1 {
		t.Errorf("invoice_register.exported rows after the control = %d, want 1", n)
	}
}
