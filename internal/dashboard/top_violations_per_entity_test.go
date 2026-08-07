// Multi-entity coverage for Store.Rollup's top_violations: every existing
// top-violations test seeds exactly one entity, so summation and re-sort
// across entities are otherwise untested.
package dashboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

// clientFor returns the Client row for entityID, or fails if absent.
func clientFor(t *testing.T, got Rollup, entityID string) Client {
	t.Helper()
	for _, c := range got.Clients {
		if c.EntityID == entityID {
			return c
		}
	}
	t.Fatalf("no client row for entity %s (got %d clients)", entityID, len(got.Clients))
	return Client{}
}

// MTV-01: 3 entities with disjoint rule sets -- each client's own list must
// be exact and correctly ordered, and the root list must be their union.
func TestStoreRollup_TopViolationsPerEntityMTV01MultiEntitySplit(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MTV-01 tenant")
	e1 := seedEntity(t, super, tenantID, "MTV-01 entity 1")
	e2 := seedEntity(t, super, tenantID, "MTV-01 entity 2")
	e3 := seedEntity(t, super, tenantID, "MTV-01 entity 3")

	broken := func(rule string) string {
		return fmt.Sprintf(`[{"rule_key":%q,"severity":"error","message":"m"}]`, rule)
	}
	for i := 0; i < 3; i++ {
		seedInvoiceWithViolations(t, super, tenantID, e1, fmt.Sprintf("MTV01-e1-a-%d", i), "draft", broken("field-a"))
	}
	seedInvoiceWithViolations(t, super, tenantID, e1, "MTV01-e1-b", "draft", broken("field-b"))
	for i := 0; i < 2; i++ {
		seedInvoiceWithViolations(t, super, tenantID, e2, fmt.Sprintf("MTV01-e2-%d", i), "draft", broken("tax-a"))
	}
	seedInvoiceWithViolations(t, super, tenantID, e3, "MTV01-e3-a", "draft", broken("ident-a"))
	seedInvoiceWithViolations(t, super, tenantID, e3, "MTV01-e3-b", "draft", broken("ident-b"))

	got := rollupFor(t, app, tenantID)

	assertTopViolations(t, clientFor(t, got, e1).TopViolations, []RuleCount{
		{RuleKey: "field-a", Invoices: 3}, {RuleKey: "field-b", Invoices: 1},
	})
	assertTopViolations(t, clientFor(t, got, e2).TopViolations, []RuleCount{
		{RuleKey: "tax-a", Invoices: 2},
	})
	assertTopViolations(t, clientFor(t, got, e3).TopViolations, []RuleCount{
		{RuleKey: "ident-a", Invoices: 1}, {RuleKey: "ident-b", Invoices: 1},
	})

	want := []RuleCount{
		{RuleKey: "field-a", Invoices: 3},
		{RuleKey: "tax-a", Invoices: 2},
		{RuleKey: "field-b", Invoices: 1},
		{RuleKey: "ident-a", Invoices: 1},
		{RuleKey: "ident-b", Invoices: 1},
	}
	assertTopViolations(t, got.TopViolations, want)
	assertTopViolations(t, got.Totals.TopViolations, want)
}

// MTV-03/MTV-04: a rule firing in two of three entities must sum at the
// root while each client shows only its own share.
func TestStoreRollup_TopViolationsPerEntityMTV03RuleFiresInTwoEntitiesSumsAtRoot(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MTV-03 tenant")
	e1 := seedEntity(t, super, tenantID, "MTV-03 entity 1")
	e2 := seedEntity(t, super, tenantID, "MTV-03 entity 2")
	e3 := seedEntity(t, super, tenantID, "MTV-03 entity 3")

	cross := `[{"rule_key":"cross-rule","severity":"error","message":"m"}]`
	for i := 0; i < 2; i++ {
		seedInvoiceWithViolations(t, super, tenantID, e1, fmt.Sprintf("MTV03-e1-cross-%d", i), "draft", cross)
	}
	seedInvoiceWithViolations(t, super, tenantID, e1, "MTV03-e1-only", "draft",
		`[{"rule_key":"e1-only","severity":"error","message":"m"}]`)
	seedInvoiceWithViolations(t, super, tenantID, e2, "MTV03-e2-cross", "draft", cross)
	for i := 0; i < 4; i++ {
		seedInvoiceWithViolations(t, super, tenantID, e3, fmt.Sprintf("MTV03-e3-only-%d", i), "draft",
			`[{"rule_key":"e3-only","severity":"error","message":"m"}]`)
	}

	got := rollupFor(t, app, tenantID)

	assertTopViolations(t, clientFor(t, got, e1).TopViolations, []RuleCount{
		{RuleKey: "cross-rule", Invoices: 2}, {RuleKey: "e1-only", Invoices: 1},
	})
	assertTopViolations(t, clientFor(t, got, e2).TopViolations, []RuleCount{
		{RuleKey: "cross-rule", Invoices: 1},
	})
	assertTopViolations(t, clientFor(t, got, e3).TopViolations, []RuleCount{
		{RuleKey: "e3-only", Invoices: 4},
	})

	want := []RuleCount{
		{RuleKey: "e3-only", Invoices: 4},
		{RuleKey: "cross-rule", Invoices: 3},
		{RuleKey: "e1-only", Invoices: 1},
	}
	assertTopViolations(t, got.TopViolations, want)
	assertTopViolations(t, got.Totals.TopViolations, want)
}

// Root order must come from a real re-sort, not a concatenation of the
// per-entity lists in Clients order -- echo/mike/kilo/alpha/zulu are sized
// so naive concatenation and the correct sort disagree past the first entry.
func TestStoreRollup_TopViolationsPerEntityRootResortNotConcatenation(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "resort tenant")
	e1 := seedEntity(t, super, tenantID, "resort entity 1")
	e2 := seedEntity(t, super, tenantID, "resort entity 2")
	e3 := seedEntity(t, super, tenantID, "resort entity 3")

	broken := func(rule string) string {
		return fmt.Sprintf(`[{"rule_key":%q,"severity":"error","message":"m"}]`, rule)
	}
	for i := 0; i < 10; i++ {
		seedInvoiceWithViolations(t, super, tenantID, e1, fmt.Sprintf("RESORT-e1-echo-%d", i), "draft", broken("echo"))
	}
	seedInvoiceWithViolations(t, super, tenantID, e1, "RESORT-e1-alpha", "draft", broken("alpha"))
	seedInvoiceWithViolations(t, super, tenantID, e1, "RESORT-e1-zulu", "draft", broken("zulu"))
	for i := 0; i < 5; i++ {
		seedInvoiceWithViolations(t, super, tenantID, e2, fmt.Sprintf("RESORT-e2-mike-%d", i), "draft", broken("mike"))
	}
	for i := 0; i < 3; i++ {
		seedInvoiceWithViolations(t, super, tenantID, e3, fmt.Sprintf("RESORT-e3-kilo-%d", i), "draft", broken("kilo"))
	}

	got := rollupFor(t, app, tenantID)

	want := []RuleCount{
		{RuleKey: "echo", Invoices: 10},
		{RuleKey: "mike", Invoices: 5},
		{RuleKey: "kilo", Invoices: 3},
		{RuleKey: "alpha", Invoices: 1},
		{RuleKey: "zulu", Invoices: 1},
	}
	assertTopViolations(t, got.TopViolations, want)
	assertTopViolations(t, got.Totals.TopViolations, want)
}

// Root tie-break must apply AFTER summing across entities: alpha-rule (1 in
// e1 + 1 in e3) ties zulu-rule (2 in e2 alone) only once totalled, and
// neither entity's own list shows that tie.
func TestStoreRollup_TopViolationsPerEntityRootTieBreakAcrossEntities(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "cross-entity tie tenant")
	e1 := seedEntity(t, super, tenantID, "tie entity 1")
	e2 := seedEntity(t, super, tenantID, "tie entity 2")
	e3 := seedEntity(t, super, tenantID, "tie entity 3")

	alpha := `[{"rule_key":"alpha-rule","severity":"error","message":"m"}]`
	zulu := `[{"rule_key":"zulu-rule","severity":"error","message":"m"}]`
	seedInvoiceWithViolations(t, super, tenantID, e1, "TIE-e1", "draft", alpha)
	for i := 0; i < 2; i++ {
		seedInvoiceWithViolations(t, super, tenantID, e2, fmt.Sprintf("TIE-e2-%d", i), "draft", zulu)
	}
	seedInvoiceWithViolations(t, super, tenantID, e3, "TIE-e3", "draft", alpha)

	got := rollupFor(t, app, tenantID)

	assertTopViolations(t, clientFor(t, got, e1).TopViolations, []RuleCount{{RuleKey: "alpha-rule", Invoices: 1}})
	assertTopViolations(t, clientFor(t, got, e2).TopViolations, []RuleCount{{RuleKey: "zulu-rule", Invoices: 2}})
	assertTopViolations(t, clientFor(t, got, e3).TopViolations, []RuleCount{{RuleKey: "alpha-rule", Invoices: 1}})

	want := []RuleCount{{RuleKey: "alpha-rule", Invoices: 2}, {RuleKey: "zulu-rule", Invoices: 2}}
	assertTopViolations(t, got.TopViolations, want)
	assertTopViolations(t, got.Totals.TopViolations, want)
}

// MTV-02: a client with invoices but no error-severity violation gets [],
// never null -- both in the Go value and the marshalled body.
func TestStoreRollup_TopViolationsPerEntityMTV02NoErrorsIsEmptyNotNull(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MTV-02 tenant")
	e1 := seedEntity(t, super, tenantID, "MTV-02 entity with violations")
	e2 := seedEntity(t, super, tenantID, "MTV-02 clean entity")

	seedInvoiceWithViolations(t, super, tenantID, e1, "MTV02-e1", "draft",
		`[{"rule_key":"x","severity":"error","message":"m"}]`)
	seedInvoiceWithViolations(t, super, tenantID, e2, "MTV02-e2-warning", "draft",
		`[{"rule_key":"y","severity":"warning","message":"m"}]`)
	seedInvoice(t, super, tenantID, e2, "MTV02-e2-clean")

	got := rollupFor(t, app, tenantID)

	c2 := clientFor(t, got, e2)
	if c2.TopViolations == nil {
		t.Fatal("clean client's TopViolations is nil, want a non-nil empty slice")
	}
	assertTopViolations(t, c2.TopViolations, []RuleCount{})

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !bytes.Contains(body, []byte(`"top_violations":[]`)) {
		t.Errorf("marshalled body = %s, want a client-level \"top_violations\":[]", body)
	}
	if bytes.Contains(body, []byte(`"top_violations":null`)) {
		t.Errorf("marshalled body = %s, want no \"top_violations\":null anywhere", body)
	}
}

// Store.Rollup must pre-initialise Totals.TopViolations to an empty slice
// for an empty tenant -- a zero-value Bucket would marshal it as null
// (the DASH-31 fixture regression this subtask's implementation triggers
// elsewhere; this pins the production-side half of that guarantee).
func TestStoreRollup_TopViolationsPerEntityEmptyTenantTotalsPreInitialized(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "empty tenant totals init tenant")

	got := rollupFor(t, app, tenantID)

	if got.TopViolations == nil {
		t.Error("root TopViolations is nil, want a non-nil empty slice")
	}
	if got.Totals.TopViolations == nil {
		t.Error("Totals.TopViolations is nil, want a non-nil empty slice")
	}

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if bytes.Contains(body, []byte(`"top_violations":null`)) {
		t.Errorf("marshalled body = %s, want no \"top_violations\":null anywhere", body)
	}
}

// MTV-05: the same rule appearing twice on one invoice counts once, both in
// its own entity's row and in the cross-entity root sum.
func TestStoreRollup_TopViolationsPerEntityMTV05SameRuleTwiceOnOneInvoiceCountsOnce(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MTV-05 tenant")
	e1 := seedEntity(t, super, tenantID, "MTV-05 entity 1")
	e2 := seedEntity(t, super, tenantID, "MTV-05 entity 2")

	seedInvoiceWithViolations(t, super, tenantID, e1, "MTV05-e1", "draft",
		`[{"rule_key":"dup-rule","severity":"error","message":"a"},`+
			`{"rule_key":"dup-rule","severity":"error","message":"b"}]`)
	seedInvoiceWithViolations(t, super, tenantID, e2, "MTV05-e2", "draft",
		`[{"rule_key":"dup-rule","severity":"error","message":"c"}]`)

	got := rollupFor(t, app, tenantID)

	assertTopViolations(t, clientFor(t, got, e1).TopViolations, []RuleCount{{RuleKey: "dup-rule", Invoices: 1}})
	assertTopViolations(t, clientFor(t, got, e2).TopViolations, []RuleCount{{RuleKey: "dup-rule", Invoices: 1}})

	want := []RuleCount{{RuleKey: "dup-rule", Invoices: 2}}
	assertTopViolations(t, got.TopViolations, want)
	assertTopViolations(t, got.Totals.TopViolations, want)
}

// MTV-06: tenant B's rule counts must never appear in any of tenant A's
// client rows, checked across every client A has, not just the busy one.
func TestStoreRollup_TopViolationsPerEntityMTV06CrossTenantNeverLeaksIntoClientRows(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := seedTenant(t, super, "MTV-06 tenant A")
	tenantB := seedTenant(t, super, "MTV-06 tenant B")
	a1 := seedEntity(t, super, tenantA, "MTV-06 A entity 1")
	a2 := seedEntity(t, super, tenantA, "MTV-06 A entity 2 (clean)")
	b1 := seedEntity(t, super, tenantB, "MTV-06 B entity")

	seedInvoiceWithViolations(t, super, tenantA, a1, "MTV06-a1", "draft",
		`[{"rule_key":"a-rule","severity":"error","message":"m"}]`)
	seedInvoice(t, super, tenantA, a2, "MTV06-a2-clean")
	for i := 0; i < 5; i++ {
		seedInvoiceWithViolations(t, super, tenantB, b1, fmt.Sprintf("MTV06-b1-%d", i), "draft",
			`[{"rule_key":"leak-rule","severity":"error","message":"m"}]`)
	}

	gotA := rollupFor(t, app, tenantA)
	assertTopViolations(t, clientFor(t, gotA, a1).TopViolations, []RuleCount{{RuleKey: "a-rule", Invoices: 1}})
	assertTopViolations(t, clientFor(t, gotA, a2).TopViolations, []RuleCount{})
	for _, c := range gotA.Clients {
		for _, rc := range c.TopViolations {
			if rc.RuleKey == "leak-rule" {
				t.Errorf("tenant A client %s TopViolations contains B's leak-rule: %+v", c.EntityID, c.TopViolations)
			}
		}
	}
	assertTopViolations(t, gotA.TopViolations, []RuleCount{{RuleKey: "a-rule", Invoices: 1}})

	gotB := rollupFor(t, app, tenantB)
	assertTopViolations(t, clientFor(t, gotB, b1).TopViolations, []RuleCount{{RuleKey: "leak-rule", Invoices: 5}})
}

// MTV-07: NULL, empty-string, and missing rule_key elements produce no
// phantom entry in any client row, including an entity that only ever saw
// malformed elements.
func TestStoreRollup_TopViolationsPerEntityMTV07MalformedRuleKeyNoPhantomEntry(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MTV-07 tenant")
	e1 := seedEntity(t, super, tenantID, "MTV-07 entity 1")
	e2 := seedEntity(t, super, tenantID, "MTV-07 entity 2 (malformed only)")

	seedInvoiceWithViolations(t, super, tenantID, e1, "MTV07-e1", "draft",
		`[{"rule_key":"real-rule","severity":"error","message":"ok"},`+
			`{"rule_key":null,"severity":"error","message":"m"},`+
			`{"rule_key":"","severity":"error","message":"m"},`+
			`{"severity":"error","message":"m"}]`)
	seedInvoiceWithViolations(t, super, tenantID, e2, "MTV07-e2", "draft",
		`[{"rule_key":null,"severity":"error","message":"m"}]`)

	got := rollupFor(t, app, tenantID)

	assertTopViolations(t, clientFor(t, got, e1).TopViolations, []RuleCount{{RuleKey: "real-rule", Invoices: 1}})
	assertTopViolations(t, clientFor(t, got, e2).TopViolations, []RuleCount{})
	for _, c := range got.Clients {
		for _, rc := range c.TopViolations {
			if rc.RuleKey == "" {
				t.Errorf("client %s TopViolations contains an empty rule_key entry: %+v", c.EntityID, c.TopViolations)
			}
		}
	}

	want := []RuleCount{{RuleKey: "real-rule", Invoices: 1}}
	assertTopViolations(t, got.TopViolations, want)
	assertTopViolations(t, got.Totals.TopViolations, want)
}

// A non-array violations value (object or scalar) must be skipped, not
// raise -- and must not contaminate a different entity's row.
func TestStoreRollup_TopViolationsPerEntityNonArrayViolationsSkippedNotFatal(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "non-array per-entity tenant")
	e1 := seedEntity(t, super, tenantID, "non-array entity 1")
	e2 := seedEntity(t, super, tenantID, "non-array entity 2")

	seedInvoiceWithViolations(t, super, tenantID, e1, "NONARR-e1-object", "draft", `{}`)
	seedInvoiceWithViolations(t, super, tenantID, e1, "NONARR-e1-real", "draft",
		`[{"rule_key":"field-required","severity":"error","message":"m"}]`)
	seedInvoiceWithViolations(t, super, tenantID, e2, "NONARR-e2-scalar", "draft", `"not an array"`)

	got := rollupFor(t, app, tenantID)

	assertTopViolations(t, clientFor(t, got, e1).TopViolations, []RuleCount{{RuleKey: "field-required", Invoices: 1}})
	assertTopViolations(t, clientFor(t, got, e2).TopViolations, []RuleCount{})

	want := []RuleCount{{RuleKey: "field-required", Invoices: 1}}
	assertTopViolations(t, got.TopViolations, want)
	assertTopViolations(t, got.Totals.TopViolations, want)
}
