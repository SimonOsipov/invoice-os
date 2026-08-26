// AUDIT-11-01 Mode A: the acceptance tests for "every invoice-scoped audit
// writer carries invoice_number", authored RED before any writer emits the
// key. Reuses the dbTestPools/seedTenant/seedEntity/seedInvoice/
// seedInvoiceAtStatus/seedInvoiceWithViolations/seedDraftWithBlockingViolation/
// seedResolvedFailed/seedMembership/seedRuleSetVersionID harness from
// store_test.go and friends (same package).
//
// Run: `DEV_DB_PORT=5442 make test-invoice` (go test -p 1).
package invoice

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// auditNumberKey is the ONE spelling, verbatim from invoices.invoice_number
// and from Invoice.InvoiceNumber's json tag (invoice.go:87).
const auditNumberKey = "invoice_number"

// auditNumberSiteCount floors auditNumberSites: internal/invoice holds exactly
// ten invoice-scoped audit.Record calls (nine in store.go, one in
// revalidate.go). A table that silently lost a row would satisfy every
// assertion inside the loop.
const auditNumberSiteCount = 10

// auditNumberSite is one audit.Record call site: how to drive it, and the
// payload key set it wrote before this story.
type auditNumberSite struct {
	label    string
	site     string // file:line at c9bac48
	event    string
	baseKeys []string
	// drive runs the real Store method and returns the invoices.id whose
	// audit row it just wrote.
	drive func(t *testing.T, super, app *pgxpool.Pool, tenantID, entityID string) string
}

// auditNumberIdentity returns a request context for tenantID plus the subject
// it carries (memberships-gated sites need to seed a row for that subject).
func auditNumberIdentity(tenantID string) (context.Context, string) {
	subject := uuid.NewString()
	return auth.WithIdentity(context.Background(), auth.Identity{
		Subject: subject, Role: "authenticated", TenantID: tenantID,
	}), subject
}

func auditNumberSites() []auditNumberSite {
	return []auditNumberSite{
		{
			label: "created", site: "store.go:269", event: "invoice.created",
			baseKeys: []string{"id"},
			drive: func(t *testing.T, super, app *pgxpool.Pool, tenantID, entityID string) string {
				c, _ := auditNumberIdentity(tenantID)
				inv, err := NewStore(app).Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "AN-CREATED-1"})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				return inv.ID
			},
		},
		{
			label: "updated_via_Update", site: "store.go:946", event: "invoice.updated",
			baseKeys: []string{"id", "fields"},
			drive: func(t *testing.T, super, app *pgxpool.Pool, tenantID, entityID string) string {
				c, _ := auditNumberIdentity(tenantID)
				id := seedInvoice(t, super, tenantID, entityID, "AN-UPDATE-1")
				buyer := "AN buyer"
				if _, err := NewStore(app).Update(c, id, UpdateInput{BuyerName: &buyer}); err != nil {
					t.Fatalf("Update: %v", err)
				}
				return id
			},
		},
		{
			label: "updated_via_Edit", site: "store.go:1371", event: "invoice.updated",
			baseKeys: []string{"id", "fields"},
			drive: func(t *testing.T, super, app *pgxpool.Pool, tenantID, entityID string) string {
				c, _ := auditNumberIdentity(tenantID)
				id := seedInvoice(t, super, tenantID, entityID, "AN-EDIT-1")
				buyer := "AN buyer"
				if _, err := NewStore(app).Edit(c, id, EditInput{UpdateInput: UpdateInput{BuyerName: &buyer}}); err != nil {
					t.Fatalf("Edit: %v", err)
				}
				return id
			},
		},
		{
			label: "transitioned", site: "store.go:1852", event: "invoice.transitioned",
			baseKeys: []string{"id", "from", "to"},
			drive: func(t *testing.T, super, app *pgxpool.Pool, tenantID, entityID string) string {
				c, _ := auditNumberIdentity(tenantID)
				id := seedInvoice(t, super, tenantID, entityID, "AN-TRANSITION-1")
				if _, err := NewStore(app).Transition(c, id, StatusValidated); err != nil {
					t.Fatalf("Transition(draft->validated): %v", err)
				}
				return id
			},
		},
		{
			label: "validated_via_ApplyValidation", site: "store.go:2030", event: "invoice.validated",
			baseKeys: []string{"id", "rule_set_version_id", "outcome", "violation_count"},
			drive: func(t *testing.T, super, app *pgxpool.Pool, tenantID, entityID string) string {
				c, _ := auditNumberIdentity(tenantID)
				store := NewStore(app)
				inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "AN-APPLYVALIDATION-1"})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if _, err := store.ApplyValidation(c, inv.ID, []Violation{}, seedRuleSetVersionID(t, super), contentFingerprint(inv, inv.LineItems)); err != nil {
					t.Fatalf("ApplyValidation: %v", err)
				}
				return inv.ID
			},
		},
		{
			label: "validated_via_revalidate", site: "revalidate.go:81", event: "invoice.validated",
			baseKeys: []string{"id", "rule_set_version_id", "outcome", "violation_count"},
			drive: func(t *testing.T, super, app *pgxpool.Pool, tenantID, entityID string) string {
				return auditNumberDriveRevalidate(t, super, app, tenantID, entityID, "AN-REVALIDATE-1")
			},
		},
		{
			label: "kept_as_is", site: "store.go:2105", event: "invoice.kept_as_is",
			baseKeys: []string{"id", "reason"},
			drive: func(t *testing.T, super, app *pgxpool.Pool, tenantID, entityID string) string {
				c, _ := auditNumberIdentity(tenantID)
				id := seedDraftWithBlockingViolation(t, super, tenantID, entityID, "AN-KEEP-1")
				if _, err := NewStore(app).KeepAsIs(c, id, "AN keeping"); err != nil {
					t.Fatalf("KeepAsIs: %v", err)
				}
				return id
			},
		},
		{
			label: "unkept_as_is", site: "store.go:2155", event: "invoice.unkept_as_is",
			baseKeys: []string{"id"},
			drive: func(t *testing.T, super, app *pgxpool.Pool, tenantID, entityID string) string {
				c, _ := auditNumberIdentity(tenantID)
				store := NewStore(app)
				id := seedDraftWithBlockingViolation(t, super, tenantID, entityID, "AN-UNKEEP-1")
				if _, err := store.KeepAsIs(c, id, "AN keeping"); err != nil {
					t.Fatalf("KeepAsIs (fixture): %v", err)
				}
				if _, err := store.UnkeepAsIs(c, id); err != nil {
					t.Fatalf("UnkeepAsIs: %v", err)
				}
				return id
			},
		},
		{
			label: "resolved_outside", site: "store.go:2210", event: "invoice.resolved_outside",
			baseKeys: []string{"id", "reason"},
			drive: func(t *testing.T, super, app *pgxpool.Pool, tenantID, entityID string) string {
				c, subject := auditNumberIdentity(tenantID)
				seedMembership(t, super, tenantID, subject, "admin")
				id := seedInvoiceAtStatus(t, super, tenantID, entityID, "AN-RESOLVE-1", StatusFailed)
				if _, err := NewStore(app).ResolveOutside(c, id, "AN resolved offline"); err != nil {
					t.Fatalf("ResolveOutside: %v", err)
				}
				return id
			},
		},
		{
			label: "unresolved_outside", site: "store.go:2267", event: "invoice.unresolved_outside",
			baseKeys: []string{"id"},
			drive: func(t *testing.T, super, app *pgxpool.Pool, tenantID, entityID string) string {
				c, subject := auditNumberIdentity(tenantID)
				seedMembership(t, super, tenantID, subject, "admin")
				id := seedResolvedFailed(t, super, tenantID, entityID, "AN-UNRESOLVE-1", subject, "AN resolved offline")
				if _, err := NewStore(app).UnresolveOutside(c, id); err != nil {
					t.Fatalf("UnresolveOutside: %v", err)
				}
				return id
			},
		},
	}
}

// auditNumberDriveRevalidate drives DemoteRevalidatedTx, revalidate.go:81's
// writer. RevalidateActive itself needs a live Gate over HTTP; the tx-scoped
// method is the same audit.Record call.
func auditNumberDriveRevalidate(t *testing.T, super, app *pgxpool.Pool, tenantID, entityID, number string) string {
	t.Helper()
	ctx := context.Background()
	store := NewStore(app)
	id := seedInvoiceWithViolations(t, super, tenantID, entityID, number, string(StatusValidated), "[]")
	versionID := seedRuleSetVersionID(t, super)
	vs := []Violation{{RuleKey: "vat-standard-rate", Severity: "error", Message: "bad rate"}}
	if err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.DemoteRevalidatedTx(ctx, tx, id, tenantID, vs, versionID)
		return err
	}); err != nil {
		t.Fatalf("DemoteRevalidatedTx: %v", err)
	}
	return id
}

// auditNumberRow is one audit_log row plus both payload-derived columns.
type auditNumberRow struct {
	rows      int
	payload   json.RawMessage
	number    *string // payload->>'invoice_number'; nil means the key is absent
	invoiceID *string
	entityID  *string
}

// readAuditNumberRow returns the newest audit_log row for tenantID+event and
// how many rows that event has. ->> yields NULL for an absent key and "" for a
// present empty string, so number distinguishes the two.
func readAuditNumberRow(t *testing.T, app *pgxpool.Pool, tenantID, event string) auditNumberRow {
	t.Helper()
	ctx := context.Background()
	var r auditNumberRow
	if err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE event = $1`, event).Scan(&r.rows); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT payload, payload->>'`+auditNumberKey+`', invoice_id::text, entity_id::text
			   FROM audit_log WHERE event = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, event,
		).Scan(&r.payload, &r.number, &r.invoiceID, &r.entityID)
	}); err != nil {
		t.Fatalf("read audit_log row for %q: %v", event, err)
	}
	return r
}

// mustInvoiceNumber reads invoices.invoice_number back out of the table, so no
// assertion below compares the payload against a literal the test itself wrote.
func mustInvoiceNumber(t *testing.T, super *pgxpool.Pool, id string) string {
	t.Helper()
	var n string
	if err := super.QueryRow(context.Background(),
		`SELECT invoice_number FROM invoices WHERE id = $1`, id,
	).Scan(&n); err != nil {
		t.Fatalf("read invoices.invoice_number for %s: %v", id, err)
	}
	return n
}

func mustInvoiceEntityID(t *testing.T, super *pgxpool.Pool, id string) string {
	t.Helper()
	var e string
	if err := super.QueryRow(context.Background(),
		`SELECT entity_id::text FROM invoices WHERE id = $1`, id,
	).Scan(&e); err != nil {
		t.Fatalf("read invoices.entity_id for %s: %v", id, err)
	}
	return e
}

// auditNumberKeys returns payload's top-level keys, sorted.
func auditNumberKeys(t *testing.T, payload json.RawMessage) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatalf("unmarshal audit payload %s: %v", payload, err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// assertExactlyOneRow fails when the drive wrote no audit row at all -- several
// of these methods no-op silently when their guard is unmet, and every
// assertion after that would be reading some other test's row or none.
func assertExactlyOneRow(t *testing.T, row auditNumberRow, event, site string) {
	t.Helper()
	if row.rows != 1 {
		t.Fatalf("%s (%s): the tenant holds %d %s audit rows, want exactly 1 -- the fixture did not drive the writer", event, site, row.rows, event)
	}
}

// AC-1: every one of the ten invoice-scoped writers puts the invoice's own
// number in its payload, equal to invoices.invoice_number read back.
func TestAuditNumber_EveryInvoicePackageEventCarriesTheNumber(t *testing.T) {
	super, app := dbTestPools(t)

	sites := auditNumberSites()
	if len(sites) != auditNumberSiteCount {
		t.Fatalf("auditNumberSites holds %d rows, want %d -- a short table passes every assertion below vacuously", len(sites), auditNumberSiteCount)
	}

	for _, s := range sites {
		t.Run(s.label, func(t *testing.T) {
			tenantID := seedTenant(t, super, "AN "+s.label+" tenant")
			entityID := seedEntity(t, super, tenantID, "AN "+s.label+" entity")

			invID := s.drive(t, super, app, tenantID, entityID)
			want := mustInvoiceNumber(t, super, invID)
			if want == "" {
				t.Fatalf("fixture invoice %s carries a blank invoice_number; the comparison below would be vacuous", invID)
			}

			row := readAuditNumberRow(t, app, tenantID, s.event)
			assertExactlyOneRow(t, row, s.event, s.site)
			if row.number == nil {
				t.Fatalf("%s (%s): payload->>'%s' is SQL NULL (key absent); payload = %s, want %q", s.event, s.site, auditNumberKey, row.payload, want)
			}
			if *row.number != want {
				t.Errorf("%s (%s): payload->>'%s' = %q, want %q (invoices.invoice_number)", s.event, s.site, auditNumberKey, *row.number, want)
			}
		})
	}
}

// AC-1: revalidate.go:81 is a SEPARATE invoice.validated writer from
// store.go:2030 -- fixing one leaves the other silent. The actor assertion is
// what proves this row came from the sweep and not from ApplyValidation.
func TestAuditNumber_RevalidateWriterCarriesTheNumber(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "AN revalidate-writer tenant")
	entityID := seedEntity(t, super, tenantID, "AN revalidate-writer entity")

	invID := auditNumberDriveRevalidate(t, super, app, tenantID, entityID, "AN-REVALIDATE-WRITER")
	want := mustInvoiceNumber(t, super, invID)

	if got := auditActor(t, app, tenantID, "invoice.validated"); got != RevalidateActor(tenantID).Subject {
		t.Fatalf("invoice.validated actor = %q, want %q -- this row is not the sweep's, so the assertion below would test the wrong writer", got, RevalidateActor(tenantID).Subject)
	}

	row := readAuditNumberRow(t, app, tenantID, "invoice.validated")
	assertExactlyOneRow(t, row, "invoice.validated", "revalidate.go:81")
	if row.number == nil {
		t.Fatalf("invoice.validated (revalidate.go:81): payload->>'%s' is SQL NULL (key absent); payload = %s, want %q", auditNumberKey, row.payload, want)
	}
	if *row.number != want {
		t.Errorf("invoice.validated (revalidate.go:81): payload->>'%s' = %q, want %q", auditNumberKey, *row.number, want)
	}
}

// AC-1: transitionTx has SEVEN production callers -- store.go:1724
// (Transition), store.go:2009 (ApplyValidation's promote), store.go:1390
// (Edit's demotion), store.go:434 (DemoteApprovalRejectedTx), revalidate.go:71,
// batch_submit.go:258 and actor.go:142 (markTerminalTx). One edit at
// store.go:1852 covers all seven by construction, because the number comes from
// transitionTx's own UPDATE ... RETURNING and never from a caller argument.
// This is therefore a refactor guard, not a partial-fix guard: it goes red the
// day someone moves the number to a caller-supplied parameter and updates only
// the caller they were looking at.
func TestAuditNumber_TransitionedCarriesTheNumberOnEveryEdge(t *testing.T) {
	super, app := dbTestPools(t)

	edges := []struct {
		label string
		via   string
		drive func(t *testing.T, super, app *pgxpool.Pool, tenantID, entityID string) string
	}{
		{
			label: "Transition", via: "store.go:1724",
			drive: func(t *testing.T, super, app *pgxpool.Pool, tenantID, entityID string) string {
				c, _ := auditNumberIdentity(tenantID)
				id := seedInvoice(t, super, tenantID, entityID, "AN-EDGE-TRANSITION")
				if _, err := NewStore(app).Transition(c, id, StatusValidated); err != nil {
					t.Fatalf("Transition(draft->validated): %v", err)
				}
				return id
			},
		},
		{
			label: "ApplyValidation_promote", via: "store.go:2009",
			drive: func(t *testing.T, super, app *pgxpool.Pool, tenantID, entityID string) string {
				c, _ := auditNumberIdentity(tenantID)
				store := NewStore(app)
				inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "AN-EDGE-APPLYVALIDATION"})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if _, err := store.ApplyValidation(c, inv.ID, []Violation{}, seedRuleSetVersionID(t, super), contentFingerprint(inv, inv.LineItems)); err != nil {
					t.Fatalf("ApplyValidation: %v", err)
				}
				return inv.ID
			},
		},
		{
			label: "Edit_demotion", via: "store.go:1390",
			drive: func(t *testing.T, super, app *pgxpool.Pool, tenantID, entityID string) string {
				c, _ := auditNumberIdentity(tenantID)
				id := seedInvoiceAtStatus(t, super, tenantID, entityID, "AN-EDGE-EDIT", StatusValidated)
				buyer := "AN buyer"
				if _, err := NewStore(app).Edit(c, id, EditInput{UpdateInput: UpdateInput{BuyerName: &buyer}}); err != nil {
					t.Fatalf("Edit(validated -> demote to draft): %v", err)
				}
				return id
			},
		},
		{
			label: "DemoteRevalidatedTx", via: "revalidate.go:71",
			drive: func(t *testing.T, super, app *pgxpool.Pool, tenantID, entityID string) string {
				return auditNumberDriveRevalidate(t, super, app, tenantID, entityID, "AN-EDGE-REVALIDATE")
			},
		},
	}
	if len(edges) != 4 {
		t.Fatalf("edge table holds %d rows, want 4", len(edges))
	}

	for _, e := range edges {
		t.Run(e.label, func(t *testing.T) {
			tenantID := seedTenant(t, super, "AN edge "+e.label+" tenant")
			entityID := seedEntity(t, super, tenantID, "AN edge "+e.label+" entity")

			invID := e.drive(t, super, app, tenantID, entityID)
			want := mustInvoiceNumber(t, super, invID)

			row := readAuditNumberRow(t, app, tenantID, "invoice.transitioned")
			assertExactlyOneRow(t, row, "invoice.transitioned", e.via)
			if row.number == nil {
				t.Fatalf("invoice.transitioned via %s (%s): payload->>'%s' is SQL NULL (key absent); payload = %s, want %q", e.label, e.via, auditNumberKey, row.payload, want)
			}
			if *row.number != want {
				t.Errorf("invoice.transitioned via %s (%s): payload->>'%s' = %q, want %q", e.label, e.via, auditNumberKey, *row.number, want)
			}
		})
	}
}

// AC-2: the number reaches the payload verbatim. invoice_number carries no
// CHECK, so a fixture with LIKE metacharacters, a space and non-ASCII text is
// constructible -- and a writer that trimmed, escaped, normalised or
// lower-cased would sail through a plain-ASCII fixture.
func TestAuditNumber_NumberIsVerbatimNeverDerived(t *testing.T) {
	super, app := dbTestPools(t)

	const gnarly = "AN/2026 100%_ Ωμέγα"

	tenantID := seedTenant(t, super, "AN verbatim tenant")
	entityID := seedEntity(t, super, tenantID, "AN verbatim entity")
	c, _ := auditNumberIdentity(tenantID)

	inv, err := NewStore(app).Create(c, CreateInput{EntityID: entityID, InvoiceNumber: gnarly})
	if err != nil {
		t.Fatalf("Create with %q: %v", gnarly, err)
	}
	if stored := mustInvoiceNumber(t, super, inv.ID); stored != gnarly {
		t.Fatalf("invoices.invoice_number = %q, want %q -- the column itself already mangled it, so the payload assertion would be meaningless", stored, gnarly)
	}

	row := readAuditNumberRow(t, app, tenantID, "invoice.created")
	assertExactlyOneRow(t, row, "invoice.created", "store.go:269")
	if row.number == nil {
		t.Fatalf("invoice.created: payload->>'%s' is SQL NULL (key absent); payload = %s, want %q", auditNumberKey, row.payload, gnarly)
	}
	if *row.number != gnarly {
		t.Errorf("invoice.created: payload->>'%s' = %q, want %q byte-identically", auditNumberKey, *row.number, gnarly)
	}
}

// AC-3: the payload is WIDENED, never rewritten. Every pre-change key survives
// and exactly one is added -- a writer that replaced "id" with the number would
// NULL audit_log.invoice_id and audit_log.entity_id for every future row and
// still search fine.
func TestAuditNumber_InvoicePayloadKeysAreOnlyWidened(t *testing.T) {
	super, app := dbTestPools(t)

	sites := auditNumberSites()
	if len(sites) != auditNumberSiteCount {
		t.Fatalf("auditNumberSites holds %d rows, want %d -- a short table passes every assertion below vacuously", len(sites), auditNumberSiteCount)
	}

	for _, s := range sites {
		t.Run(s.label, func(t *testing.T) {
			if len(s.baseKeys) == 0 {
				t.Fatalf("%s (%s): baseKeys is empty; set equality against an empty want proves nothing", s.event, s.site)
			}
			tenantID := seedTenant(t, super, "AN keys "+s.label+" tenant")
			entityID := seedEntity(t, super, tenantID, "AN keys "+s.label+" entity")

			invID := s.drive(t, super, app, tenantID, entityID)
			row := readAuditNumberRow(t, app, tenantID, s.event)
			assertExactlyOneRow(t, row, s.event, s.site)

			want := append(append([]string{}, s.baseKeys...), auditNumberKey)
			sort.Strings(want)
			got := auditNumberKeys(t, row.payload)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s (%s): payload keys = [%s], want [%s] (every pre-change key, plus exactly %q); payload = %s",
					s.event, s.site, strings.Join(got, ","), strings.Join(want, ","), auditNumberKey, row.payload)
			}

			// The load-bearing survivor: the generated column and the entity
			// trigger both address "id" and nothing else.
			var decoded struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(row.payload, &decoded); err != nil {
				t.Fatalf("unmarshal payload %s: %v", row.payload, err)
			}
			if decoded.ID != invID {
				t.Errorf("%s (%s): payload id = %q, want %q (unchanged)", s.event, s.site, decoded.ID, invID)
			}
		})
	}
}

// AC-4: BOTH payload-derived columns still fill once the sibling key is there.
// audit_log.invoice_id is a STORED generated column and audit_log.entity_id is
// filled by the audit_log_entity_on_insert trigger; both read payload->>'id'
// and neither iterates the key set. An invoice_id-only assertion would not see
// entity_id regress.
func TestAuditNumber_ScopedColumnsStillFillWithTheNewKey(t *testing.T) {
	super, app := dbTestPools(t)

	sites := auditNumberSites()
	if len(sites) != auditNumberSiteCount {
		t.Fatalf("auditNumberSites holds %d rows, want %d -- a short table passes every assertion below vacuously", len(sites), auditNumberSiteCount)
	}

	for _, s := range sites {
		t.Run(s.label, func(t *testing.T) {
			tenantID := seedTenant(t, super, "AN scoped "+s.label+" tenant")
			entityID := seedEntity(t, super, tenantID, "AN scoped "+s.label+" entity")

			invID := s.drive(t, super, app, tenantID, entityID)
			row := readAuditNumberRow(t, app, tenantID, s.event)
			assertExactlyOneRow(t, row, s.event, s.site)

			// "with the new key present" is half the claim: without this the
			// test would stay green on a payload this story never touched.
			if row.number == nil {
				t.Fatalf("%s (%s): payload->>'%s' is SQL NULL (key absent), so this test is not yet asserting what its name says; payload = %s", s.event, s.site, auditNumberKey, row.payload)
			}

			if row.invoiceID == nil {
				t.Errorf("%s (%s): audit_log.invoice_id is NULL with %q present; payload = %s", s.event, s.site, auditNumberKey, row.payload)
			} else if *row.invoiceID != invID {
				t.Errorf("%s (%s): audit_log.invoice_id = %q, want %q", s.event, s.site, *row.invoiceID, invID)
			}

			wantEntity := mustInvoiceEntityID(t, super, invID)
			if row.entityID == nil {
				t.Errorf("%s (%s): audit_log.entity_id is NULL with %q present, which reads as a firm-wide claim; payload = %s", s.event, s.site, auditNumberKey, row.payload)
			} else if *row.entityID != wantEntity {
				t.Errorf("%s (%s): audit_log.entity_id = %q, want %q (the invoice's entity)", s.event, s.site, *row.entityID, wantEntity)
			}
		})
	}
}

// CF-5: invoice_number is NOT NULL with no CHECK, and the empty-number guard is
// Go-level (store.go:159), so the schema does not promise a non-blank number.
// The honest claim is that the payload MIRRORS the column: non-blank wherever
// the column is non-blank, and blank -- but present -- where it is not.
func TestAuditNumber_PayloadMirrorsTheColumnIncludingBlank(t *testing.T) {
	super, app := dbTestPools(t)

	t.Run("non_blank_column_yields_non_blank_payload", func(t *testing.T) {
		tenantID := seedTenant(t, super, "AN mirror non-blank tenant")
		entityID := seedEntity(t, super, tenantID, "AN mirror non-blank entity")
		c, _ := auditNumberIdentity(tenantID)

		inv, err := NewStore(app).Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "AN-MIRROR-1"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		want := mustInvoiceNumber(t, super, inv.ID)
		if want == "" {
			t.Fatalf("fixture column is blank; this subtest asserts the non-blank half")
		}

		row := readAuditNumberRow(t, app, tenantID, "invoice.created")
		assertExactlyOneRow(t, row, "invoice.created", "store.go:269")
		if row.number == nil || *row.number == "" {
			t.Fatalf("invoice.created: payload->>'%s' = %v, want the non-blank %q; payload = %s", auditNumberKey, row.number, want, row.payload)
		}
		if *row.number != want {
			t.Errorf("invoice.created: payload->>'%s' = %q, want %q", auditNumberKey, *row.number, want)
		}
	})

	t.Run("blank_column_yields_present_blank_payload", func(t *testing.T) {
		tenantID := seedTenant(t, super, "AN mirror blank tenant")
		entityID := seedEntity(t, super, tenantID, "AN mirror blank entity")
		c, _ := auditNumberIdentity(tenantID)

		// Store.Create's guard refuses '' , so the blank row is seeded by raw
		// SQL -- the only way it is reachable at all.
		id := seedInvoice(t, super, tenantID, entityID, "")
		if got := mustInvoiceNumber(t, super, id); got != "" {
			t.Fatalf("fixture invoices.invoice_number = %q, want blank", got)
		}

		buyer := "AN buyer"
		if _, err := NewStore(app).Update(c, id, UpdateInput{BuyerName: &buyer}); err != nil {
			t.Fatalf("Update: %v", err)
		}

		row := readAuditNumberRow(t, app, tenantID, "invoice.updated")
		assertExactlyOneRow(t, row, "invoice.updated", "store.go:946")
		if row.number == nil {
			t.Fatalf("invoice.updated: payload->>'%s' is SQL NULL (key absent), want the key present carrying the column's blank value; payload = %s", auditNumberKey, row.payload)
		}
		if *row.number != "" {
			t.Errorf("invoice.updated: payload->>'%s' = %q, want %q -- the payload must mirror the column, never invent a number", auditNumberKey, *row.number, "")
		}
	})
}
