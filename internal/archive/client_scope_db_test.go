package archive

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

	// Each route answers one 404 body for a client outside the set, an unknown id and another tenant's.
	var foreign string
	mustCommitFixture(t, super, func(tx pgx.Tx) string {
		tid := mustCreateTenant(t, tx, "archive-client-scope-foreign")
		foreign = mustCreateEntity(t, tx, tid, "Foreign", "")
		return tid
	})
	now := time.Now().UTC()
	routes := map[string]struct {
		h        http.HandlerFunc
		from, to string
	}{
		"bundle":   {DownloadHandler(s.Assemble, nil), now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339)},
		"preview":  {PreviewHandler(s.Preview, nil), now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339)},
		"register": {RegisterHandler(s.Register, nil), now.AddDate(0, 0, -1).Format(time.DateOnly), now.AddDate(0, 0, 1).Format(time.DateOnly)},
	}
	for name, rt := range routes {
		serve := func(entity string) *httptest.ResponseRecorder {
			q := url.Values{"entity_id": {entity}, "from": {rt.from}, "to": {rt.to}}
			w := httptest.NewRecorder()
			rt.h(w, httptest.NewRequest(http.MethodGet, "/?"+q.Encode(), nil).WithContext(ctx))
			return w
		}
		outside := serve(entB)
		if outside.Code != http.StatusNotFound {
			t.Errorf("%s for client B: %d %s, want 404", name, outside.Code, outside.Body.String())
			continue
		}
		for who, id := range map[string]string{"unknown": uuid.NewString(), "another tenant's": foreign} {
			if got := serve(id); got.Code != outside.Code || got.Body.String() != outside.Body.String() {
				t.Errorf("%s for %s id: %d %s, differs from outside-the-set %d %s", name, who, got.Code, got.Body.String(), outside.Code, outside.Body.String())
			}
		}
		if w := serve(entA); w.Code != http.StatusOK {
			t.Errorf("%s for client A control: %d, want 200", name, w.Code)
		}
	}
}
