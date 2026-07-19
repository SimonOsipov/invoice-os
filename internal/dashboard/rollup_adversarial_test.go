// M4-07-01 (task-155): QA adversarial coverage ON TOP OF DASH-01..15
// (store_test.go / cross_tenant_integration_test.go), written during the
// Mode B (post-implementation) verify pass. The 15 shipped specs prove the
// happy paths and the documented needs-attention/ordering rules; this file
// closes gaps they don't touch: malformed/odd `violations` shapes, severity
// case-sensitivity, the "non-draft carrying an error violation" invariant
// (unreachable via the real write path but force-seedable), deterministic
// tie-break at the entity_id level, and ordering correctness at a larger
// fanout. Reuses the dbTestPools/seedTenant/seedEntity/seedInvoice/
// seedInvoiceWithViolations harness from store_test.go (same package).
package dashboard

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// TestStoreRollup_MalformedViolationsNeverErrorsOrFalselyFlags: Postgres
// jsonb `@>` containment between mismatched top-level container types (or a
// non-matching structure) evaluates to false, never errors (Stage 1
// Architecture Validation, M4-07-01 story, edge case #1) -- this pins that
// claim against the real DB for every odd-but-well-formed shape the column
// CAN hold (invoices.violations is NOT NULL so malformed/non-JSON is
// impossible at the column level; these are all valid jsonb, just not the
// `[{"severity":"error",...}]` shape the predicate expects).
func TestStoreRollup_MalformedViolationsNeverErrorsOrFalselyFlags(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	cases := []struct {
		name       string
		violations string
	}{
		{"object-not-array", `{}`},
		{"array-of-scalars", `[1,2,3]`},
		{"element-missing-severity-key", `[{"rule_key":"x","message":"y"}]`},
		{"empty-array", `[]`},
		{"nested-wrapped-shape", `{"violations":[{"severity":"error"}]}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tenantID := seedTenant(t, super, "malformed-violations "+tc.name)
			entityID := seedEntity(t, super, tenantID, "entity "+tc.name)
			seedInvoiceWithViolations(t, super, tenantID, entityID, "MALFORMED-"+tc.name, "draft", tc.violations)

			cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})
			got, err := store.Rollup(cA)
			if err != nil {
				t.Fatalf("Rollup with violations=%s: %v", tc.violations, err)
			}
			if len(got.Clients) != 1 {
				t.Fatalf("Clients = %d rows, want 1", len(got.Clients))
			}
			row := got.Clients[0]
			if row.Counts.Draft != 1 {
				t.Errorf("Counts.Draft = %d, want 1", row.Counts.Draft)
			}
			if row.NeedsAttention != 0 {
				t.Errorf("violations=%s: NeedsAttention = %d, want 0 (malformed/non-matching shape must never count as needs-attention)", tc.violations, row.NeedsAttention)
			}
		})
	}
}

// TestStoreRollup_UppercaseSeverityDoesNotMatchPredicate: the real
// validation engine can never emit "ERROR" -- rules.severity is DB
// CHECK-constrained to the lowercase set ('error','warning','info')
// (migrations/20260711051711_rule_set_versions.sql:26) and every stored
// Violation's Severity is copied verbatim from Rule.Severity with no Go-side
// normalization anywhere in the write path (internal/validation/
// evaluators.go's violation() helper; no strings.ToLower/ToUpper in
// internal/validation or internal/invoice). This test force-writes past that
// guarantee directly into invoices.violations (which has no content-level
// CHECK of its own) to pin the SQL predicate's actual case-sensitive
// behavior as documented, known behavior rather than an assumption: if a
// future write path ever bypassed the rules table's CHECK, an
// uppercase-severity violation would silently NOT count as needs-attention.
func TestStoreRollup_UppercaseSeverityDoesNotMatchPredicate(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-adversarial uppercase severity")
	entityID := seedEntity(t, super, tenantID, "uppercase severity entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "UPPER-1", "draft",
		`[{"rule_key":"x","severity":"ERROR","message":"y"}]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 1 {
		t.Fatalf("Clients = %d rows, want 1", len(got.Clients))
	}
	if row := got.Clients[0]; row.NeedsAttention != 0 {
		t.Errorf("NeedsAttention = %d, want 0 (uppercase \"ERROR\" must not match the lowercase severity:\"error\" predicate)", row.NeedsAttention)
	}
}

// TestStoreRollup_NonDraftWithErrorViolationDoesNotCountUnlessRejectedOrFailed:
// per the story's invariant, a non-draft invoice can never carry a
// severity:"error" violation via the real ApplyValidation path (only
// 'draft' invoices are ever validated pre-transition). This force-seeds that
// otherwise-unreachable state directly -- bypassing Store.Rollup's own write
// path entirely, same force-write idiom as seedInvoiceAtStatus/
// seedInvoiceWithViolations -- to pin the DOCUMENTED behavior (the predicate
// only inspects `violations` when status='draft'; rejected/failed count
// unconditionally, every other status never counts regardless of
// violations) as a test rather than leaving it to prose in store.go's
// comment.
func TestStoreRollup_NonDraftWithErrorViolationDoesNotCountUnlessRejectedOrFailed(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-adversarial non-draft error violation")
	entityID := seedEntity(t, super, tenantID, "non-draft error violation entity")
	broken := `[{"rule_key":"x","severity":"error","message":"y"}]`
	seedInvoiceWithViolations(t, super, tenantID, entityID, "NONDRAFT-1", "validated", broken)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 1 {
		t.Fatalf("Clients = %d rows, want 1", len(got.Clients))
	}
	row := got.Clients[0]
	if row.Counts.Validated != 1 {
		t.Errorf("Counts.Validated = %d, want 1", row.Counts.Validated)
	}
	if row.NeedsAttention != 0 {
		t.Errorf("NeedsAttention = %d, want 0 (a validated invoice's violations must never be inspected by the predicate, even a forced error-severity one)", row.NeedsAttention)
	}
}

// TestStoreRollup_IdenticalNamesTieBreakByEntityIDAscending: three entities
// sharing the exact same name, all at needs_attention=0, exhausts BOTH the
// primary (needs_attention DESC) and secondary (entity_name ASC) sort keys
// -- ordering can only fall through to the tertiary entity_id ASC tie-break.
// DASH-10 only proves the entity_name ASC tie-break exists; this proves the
// final tie-break is deterministic too, not left to Postgres's unspecified
// GROUP BY row order.
func TestStoreRollup_IdenticalNamesTieBreakByEntityIDAscending(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-adversarial identical names")
	const sameName = "Identical Corp"
	e1 := seedEntity(t, super, tenantID, sameName)
	e2 := seedEntity(t, super, tenantID, sameName)
	e3 := seedEntity(t, super, tenantID, sameName)
	seedInvoice(t, super, tenantID, e1, "TIE-1")
	seedInvoice(t, super, tenantID, e2, "TIE-2")
	seedInvoice(t, super, tenantID, e3, "TIE-3")

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 3 {
		t.Fatalf("Clients = %d rows, want 3", len(got.Clients))
	}

	wantOrder := []string{e1, e2, e3}
	sort.Strings(wantOrder)
	gotOrder := []string{got.Clients[0].EntityID, got.Clients[1].EntityID, got.Clients[2].EntityID}
	for i, wantID := range wantOrder {
		if gotOrder[i] != wantID {
			t.Fatalf("Clients order (by id) = %v, want %v ascending (entity_id tie-break at equal name and needs_attention)", gotOrder, wantOrder)
		}
	}
}

// TestStoreRollup_LargeFanoutOrderingStaysCorrect: 25 entities across 5
// distinct needs_attention levels (5 entities tied within each level, so
// both the primary DESC key and the name-ASC tie-break are exercised at
// scale, not just the 2-3 row cases DASH-09/10 use).
func TestStoreRollup_LargeFanoutOrderingStaysCorrect(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-adversarial large fanout")

	const buckets = 5   // distinct needs_attention levels: 4,3,2,1,0
	const perBucket = 5 // entities per level -> exercises the name-ASC tie-break too
	broken := `[{"rule_key":"x","severity":"error","message":"y"}]`

	type seededEntity struct {
		id   string
		name string
	}
	var wantOrder []seededEntity // filled in DESC-bucket, ASC-name (seed) order

	for b := buckets - 1; b >= 0; b-- { // seed highest-need bucket first
		for j := 0; j < perBucket; j++ {
			name := fmt.Sprintf("Fanout-%d-%02d", b, j) // zero-padded j sorts lexicographically as seeded
			id := seedEntity(t, super, tenantID, name)
			for k := 0; k < b; k++ {
				seedInvoiceWithViolations(t, super, tenantID, id, fmt.Sprintf("FANOUT-%d-%02d-%02d", b, j, k), "draft", broken)
			}
			if b == 0 {
				// Bucket 0 has zero broken drafts -- still needs an invoice
				// to appear at all (AC-7's INNER JOIN excludes invoice-less
				// entities).
				seedInvoice(t, super, tenantID, id, fmt.Sprintf("FANOUT-%d-%02d-clean", b, j))
			}
			wantOrder = append(wantOrder, seededEntity{id: id, name: name})
		}
	}

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	total := buckets * perBucket
	if len(got.Clients) != total {
		t.Fatalf("Clients = %d rows, want %d", len(got.Clients), total)
	}
	for i, want := range wantOrder {
		if got.Clients[i].EntityID != want.id {
			gotIDs := make([]string, total)
			for j, c := range got.Clients {
				gotIDs[j] = c.EntityID
			}
			t.Fatalf("Clients[%d] = {id:%s name:%s}, want {id:%s name:%s} (needs_attention DESC, name ASC ordering broken at 25-entity scale; full got order = %v)",
				i, got.Clients[i].EntityID, got.Clients[i].EntityName, want.id, want.name, gotIDs)
		}
	}
}
