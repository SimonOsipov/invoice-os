// M4-09-01 (task-182), QA Mode B adversarial coverage: the AC tests authored
// RED in Mode A (46b8da7) and driven GREEN in Stage 3 (6fe2793) prove the
// happy-path shape (a stamped invoice's version resolves; a never-validated
// one is nil). The three specs below close the gaps a happy-path suite alone
// would miss:
//
//   - TestStoreGet_RuleSetVersionResolvesByID: the Mode-A store test stamps
//     ONE row against whatever seedRuleSetVersionID's `LIMIT 1` happens to
//     return -- a subselect that ignored the join key entirely and just
//     returned "the only row it can see" would pass that test by accident.
//     This test stamps TWO invoices against two DISTINCT rule_set_versions
//     rows (the M3-04/M4-04-01 seed always publishes at least two) and
//     asserts each Get resolves its OWN row's version -- which only holds if
//     the subselect actually correlates on rule_set_version_id. Compared
//     against each row's version read back from the DB, not a hardcoded
//     literal (deliberately -- internal/validation's RS-V2-14 detection gate
//     flags hardcoded "assume the rule-set version is 1" literals repo-wide;
//     this test asserts nothing about WHICH version numbers exist, only that
//     Get resolves whichever one was actually stamped).
//   - TestGetHandler_RawJSONCarriesBothVersionKeys: a single raw-JSON
//     assertion that a validated invoice's GET body carries BOTH
//     rule_set_version_id (the embedded Invoice's own field) and
//     rule_set_version (getResponse's sibling) with their OWN, DIFFERING
//     values -- guards against the embedded-vs-sibling shadowing that Go's
//     JSON encoder would apply if the two ever collided on tag name (they
//     don't today: the domain field is json:"-"), silently dropping one key.
//   - TestGetHandler_RealStore_NeverValidatedEmitsExplicitNull: the Mode-A
//     null-marshalling test stubs Get and asserts the wrapper's own
//     no-omitempty behaviour in isolation. This test wires the REAL
//     Store.Get into the REAL GetHandler (mirrors cmd/invoice/main.go's own
//     wiring) against a freshly seeded draft row, so the explicit-null
//     contract is pinned end to end -- Store->wire, not just wrapper->wire.
package invoice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// ruleSetVersionRow is one rule_set_versions row's (id, version) pair, read
// back from the DB rather than assumed -- see this file's header doc for why
// this test never hardcodes a specific version number.
type ruleSetVersionRow struct {
	id      string
	version int
}

// TestStoreGet_RuleSetVersionResolvesByID (M4-09-01 adversarial): stamps two
// invoices against two DISTINCT rule_set_versions rows and asserts each Get
// resolves ITS OWN row's version -- not "the only/first row" (which is all
// TestStoreGet_PopulatesRuleSetVersion's single-row stamp can prove, since
// seedRuleSetVersionID's `LIMIT 1` always returns the same row regardless of
// which id was actually stamped onto the invoice). Each assertion compares
// against that row's OWN version, scanned back from the DB -- never a
// hardcoded literal (this file's header doc explains why).
func TestStoreGet_RuleSetVersionResolvesByID(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "M4-09-01-adv tenant")
	entityID := seedEntity(t, super, tenantID, "M4-09-01-adv entity")
	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows, err := super.Query(ctx, `SELECT id, version FROM rule_set_versions ORDER BY published_at LIMIT 2`)
	if err != nil {
		t.Fatalf("look up two rule_set_versions rows (is the M3-04/M4-04-01 seed applied?): %v", err)
	}
	var seeds []ruleSetVersionRow
	for rows.Next() {
		var r ruleSetVersionRow
		if err := rows.Scan(&r.id, &r.version); err != nil {
			rows.Close()
			t.Fatalf("scan rule_set_versions row: %v", err)
		}
		seeds = append(seeds, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rule_set_versions rows: %v", err)
	}
	if len(seeds) < 2 {
		t.Fatalf("this test needs two distinct rule_set_versions rows, found %d", len(seeds))
	}
	rowA, rowB := seeds[0], seeds[1]
	if rowA.id == rowB.id {
		t.Fatalf("test setup invalid: the two rows resolved to the same id %q", rowA.id)
	}

	invA := seedInvoice(t, super, tenantID, entityID, "M4-09-01-ADV-A")
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET rule_set_version_id = $1 WHERE id = $2`, rowA.id, invA,
	); err != nil {
		t.Fatalf("stamp invA: %v", err)
	}
	invB := seedInvoice(t, super, tenantID, entityID, "M4-09-01-ADV-B")
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET rule_set_version_id = $1 WHERE id = $2`, rowB.id, invB,
	); err != nil {
		t.Fatalf("stamp invB: %v", err)
	}

	gotA, err := store.Get(c, invA)
	if err != nil {
		t.Fatalf("Get(invA): %v", err)
	}
	if gotA.RuleSetVersion == nil || *gotA.RuleSetVersion != rowA.version {
		t.Errorf("Get(invA stamped with rowA's id).RuleSetVersion = %v, want %d (rowA's own version)",
			gotA.RuleSetVersion, rowA.version)
	}

	gotB, err := store.Get(c, invB)
	if err != nil {
		t.Fatalf("Get(invB): %v", err)
	}
	if gotB.RuleSetVersion == nil || *gotB.RuleSetVersion != rowB.version {
		t.Errorf("Get(invB stamped with rowB's id).RuleSetVersion = %v, want %d (rowB's own version)",
			gotB.RuleSetVersion, rowB.version)
	}
}

// TestGetHandler_RawJSONCarriesBothVersionKeys (M4-09-01 adversarial): a
// single raw-JSON assertion that a validated invoice's GET body carries BOTH
// rule_set_version_id (the embedded domain Invoice's own field) and
// rule_set_version (getResponse's additive sibling) with their OWN,
// DIFFERING values -- decoded off the raw bytes, not the pre-typed
// invoiceBody helper, so a future field rename that collided the two tags
// would be caught by a decode mismatch rather than silently satisfied by
// whichever key Go's encoder happened to keep.
func TestGetHandler_RawJSONCarriesBothVersionKeys(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	versionID := uuid.NewString()
	version := 7
	want := Invoice{
		ID:               invoiceID,
		Status:           StatusValidated,
		RuleSetVersionID: &versionID,
		RuleSetVersion:   &version,
	}
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return want, nil
	}
	rec, _ := doInvoiceGet(t, get, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	var raw struct {
		RuleSetVersionID *string `json:"rule_set_version_id"`
		RuleSetVersion   *int    `json:"rule_set_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw body %q: %v", rec.Body.String(), err)
	}

	if raw.RuleSetVersionID == nil || *raw.RuleSetVersionID != versionID {
		t.Errorf("raw rule_set_version_id = %v, want %q (body=%s)", raw.RuleSetVersionID, versionID, rec.Body.String())
	}
	if raw.RuleSetVersion == nil || *raw.RuleSetVersion != version {
		t.Errorf("raw rule_set_version = %v, want %d (body=%s)", raw.RuleSetVersion, version, rec.Body.String())
	}
}

// TestGetHandler_RealStore_NeverValidatedEmitsExplicitNull (M4-09-01
// adversarial): wires the REAL Store.Get into the REAL GetHandler (the same
// method-value wiring cmd/invoice/main.go:54 uses in production) against a
// freshly seeded draft row (rule_set_version_id IS NULL, never validated).
// Pins the no-omitempty contract END TO END -- DB read through to wire byte
// -- rather than only at the wrapper level (TestGetHandler_
// RuleSetVersionMarshalsNull stubs Get and proves the wrapper alone; this
// proves Store.Get's nil really reaches the wire as an explicit null through
// the full stack).
func TestGetHandler_RealStore_NeverValidatedEmitsExplicitNull(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "M4-09-01-adv-e2e tenant")
	entityID := seedEntity(t, super, tenantID, "M4-09-01-adv-e2e entity")
	store := NewStore(app)

	invoiceID := seedInvoice(t, super, tenantID, entityID, "M4-09-01-ADV-E2E")

	identity := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}
	r := httptest.NewRequest("GET", "/v1/invoices/"+invoiceID, nil)
	r.SetPathValue("id", invoiceID)
	r = r.WithContext(auth.WithIdentity(ctx, identity))
	rec := httptest.NewRecorder()

	GetHandler(store.Get, nil).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"rule_set_version":null`) {
		t.Errorf("body = %s, want the literal \"rule_set_version\":null end to end from a real, "+
			"freshly seeded never-validated row (not a stubbed Get)", body)
	}
}
