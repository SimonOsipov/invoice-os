// Adversarial coverage MET-01..17 (metrics_test.go) don't provide:
// cross_tenant_integration_test.go RLS-checks Counts/NeedsAttention but never
// Bucket.Metrics.
package dashboard

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// TestRLS_DashboardRollupCrossTenantMetricsIsolated proves Bucket.Metrics
// doesn't leak cross-tenant -- checked on Totals and the promoted per-client
// field, both directions, mirroring DASH-14's shape for Counts.
func TestRLS_DashboardRollupCrossTenantMetricsIsolated(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "METR-01-02 QA tenant A")
	tenantB := seedTenant(t, super, "METR-01-02 QA tenant B")
	entityA := seedEntity(t, super, tenantA, "METR-01-02 QA A Corp")
	entityB := seedEntity(t, super, tenantB, "METR-01-02 QA B Corp")

	seedInvoiceAtStatus(t, super, tenantA, entityA, "METRQA-A1", "validated")
	for i := 0; i < 3; i++ {
		seedInvoiceAtStatus(t, super, tenantB, entityB, fmt.Sprintf("METRQA-B%d", i), "rejected")
	}

	wantA := map[string]Metric{
		MetricReadiness:            {Num: 1, Den: 1},
		MetricBarFieldCompleteness: {Num: 1, Den: 1},
		MetricBarTaxAccuracy:       {Num: 1, Den: 1},
		MetricBarIdentifiersFormat: {Num: 1, Den: 1},
		MetricBlockedByRules:       {Num: 0, Den: 1},
		MetricFailedInTransmission: {Num: 0, Den: 1},
		MetricNeverValidated:       {Num: 0, Den: 1},
		MetricVATTracked:           {Num: 0, Den: 1},
	}
	wantB := map[string]Metric{
		MetricReadiness:            {Num: 0, Den: 3},
		MetricBarFieldCompleteness: {Num: 3, Den: 3},
		MetricBarTaxAccuracy:       {Num: 3, Den: 3},
		MetricBarIdentifiersFormat: {Num: 3, Den: 3},
		MetricBlockedByRules:       {Num: 0, Den: 3},
		MetricFailedInTransmission: {Num: 3, Den: 3},
		MetricNeverValidated:       {Num: 0, Den: 3},
		MetricVATTracked:           {Num: 0, Den: 3},
	}

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})
	cB := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB})

	gotA, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup(as tenant A): %v", err)
	}
	if !reflect.DeepEqual(gotA.Totals.Metrics, wantA) {
		t.Errorf("tenant A's Totals.Metrics = %+v, want %+v (must not fold in B's rejected invoices)", gotA.Totals.Metrics, wantA)
	}
	if len(gotA.Clients) != 1 || !reflect.DeepEqual(gotA.Clients[0].Metrics, wantA) {
		t.Errorf("tenant A's Clients[0].Metrics = %+v, want %+v", gotA.Clients[0].Metrics, wantA)
	}

	gotB, err := store.Rollup(cB)
	if err != nil {
		t.Fatalf("Rollup(as tenant B): %v", err)
	}
	if !reflect.DeepEqual(gotB.Totals.Metrics, wantB) {
		t.Errorf("tenant B's Totals.Metrics = %+v, want %+v (must not fold in A's validated invoice)", gotB.Totals.Metrics, wantB)
	}
	if len(gotB.Clients) != 1 || !reflect.DeepEqual(gotB.Clients[0].Metrics, wantB) {
		t.Errorf("tenant B's Clients[0].Metrics = %+v, want %+v", gotB.Clients[0].Metrics, wantB)
	}
}
