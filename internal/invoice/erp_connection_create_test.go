package invoice

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// seedERPConnection inserts an active connection on its own grant as the superuser.
func seedERPConnection(t *testing.T, super *pgxpool.Pool, tenantID, entityID string) string {
	t.Helper()
	ctx := context.Background()
	var grantID, id string
	if err := super.QueryRow(ctx,
		`INSERT INTO erp_grants (tenant_id, system, kind) VALUES ($1, 'fake', 'api_key') RETURNING id`, tenantID,
	).Scan(&grantID); err != nil {
		t.Fatalf("seed erp_grants: %v", err)
	}
	if err := super.QueryRow(ctx,
		`INSERT INTO erp_connections (tenant_id, entity_id, system, external_key, status, grant_id)
		 VALUES ($1, $2, 'fake', 'fake-co-1', 'active', $3) RETURNING id`, tenantID, entityID, grantID,
	).Scan(&id); err != nil {
		t.Fatalf("seed erp_connections: %v", err)
	}
	return id
}

func erpConnectionOf(t *testing.T, super *pgxpool.Pool, invoiceID string) *string {
	t.Helper()
	var got *string
	if err := super.QueryRow(context.Background(),
		`SELECT erp_connection_id::text FROM invoices WHERE id = $1`, invoiceID).Scan(&got); err != nil {
		t.Fatalf("read invoices.erp_connection_id: %v", err)
	}
	return got
}

// tenantRowCounts is every row a create writes for the tenant, so "nothing written" also
// catches a half-committed child row.
func tenantRowCounts(t *testing.T, super *pgxpool.Pool, tenantID string) [4]int {
	t.Helper()
	var n [4]int
	if err := super.QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM invoices WHERE tenant_id = $1),
		        (SELECT count(*) FROM line_items WHERE tenant_id = $1),
		        (SELECT count(*) FROM invoice_status_history WHERE tenant_id = $1),
		        (SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'invoice.created')`, tenantID,
	).Scan(&n[0], &n[1], &n[2], &n[3]); err != nil {
		t.Fatalf("count tenant rows: %v", err)
	}
	return n
}

func TestStoreCreate_PersistsTheERPConnection(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "erp connection tenant")
	entityID := seedEntity(t, super, tenantID, "erp connection entity")
	connID := seedERPConnection(t, super, tenantID, entityID)

	store := NewStore(app)
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "ERP-CONN-1", ERPConnectionID: &connID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := erpConnectionOf(t, super, inv.ID); got == nil || *got != connID {
		t.Errorf("invoices.erp_connection_id = %v, want %q", got, connID)
	}

	plain, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "ERP-CONN-2"})
	if err != nil {
		t.Fatalf("Create without a connection: %v", err)
	}
	if got := erpConnectionOf(t, super, plain.ID); got != nil {
		t.Errorf("invoices.erp_connection_id = %q for a create that named none, want NULL", *got)
	}
}

func TestStoreCreate_AnotherTenantsERPConnectionIsRefused(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := seedTenant(t, super, "erp connection tenant A")
	tenantB := seedTenant(t, super, "erp connection tenant B")
	entityA := seedEntity(t, super, tenantA, "erp connection entity A")
	entityB := seedEntity(t, super, tenantB, "erp connection entity B")
	connB := seedERPConnection(t, super, tenantB, entityB)

	store := NewStore(app)
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantA})

	// The control runs first: without it the zero deltas below also hold for a counter reading
	// the wrong tenant or table.
	before := tenantRowCounts(t, super, tenantA)
	lines := []LineItemInput{{Description: ptr("one line")}}
	if _, err := store.Create(c, CreateInput{EntityID: entityA, InvoiceNumber: "ERP-XT-CONTROL", LineItems: lines}); err != nil {
		t.Fatalf("control create: %v", err)
	}
	afterControl := tenantRowCounts(t, super, tenantA)
	want := [4]int{before[0] + 1, before[1] + 1, before[2] + 1, before[3] + 1}
	if afterControl != want {
		t.Fatalf("control create moved the counts %v -> %v, want %v: the counter is blind", before, afterControl, want)
	}

	_, err := store.Create(c, CreateInput{EntityID: entityA, InvoiceNumber: "ERP-XT-REFUSED", ERPConnectionID: &connB, LineItems: lines})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Create with another tenant's connection: err = %v, want ErrValidation", err)
	}
	if got := tenantRowCounts(t, super, tenantA); got != afterControl {
		t.Errorf("a refused create left rows: counts %v -> %v", afterControl, got)
	}

	_, err = store.Create(c, CreateInput{EntityID: entityA, InvoiceNumber: "ERP-XT-NOSUCH", ERPConnectionID: ptr(uuid.NewString())})
	if !errors.Is(err, ErrValidation) {
		t.Errorf("Create with a never-existed connection: err = %v, want ErrValidation", err)
	}
}

func ptr(s string) *string { return &s }
