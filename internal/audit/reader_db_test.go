// AUDIT-04-02: the page query against a real Postgres — keyset order and stability,
// has_more/next_cursor agreement, the four company states, and the wire shape rules
// (events never null, id a string, total a count under the filters).
//
// Helpers here use a page* prefix: reader_test.go already owns reader* in this package.
// Rows are seeded by raw INSERT with an explicit created_at, never by N audit.Record
// calls in one transaction — audit_log.created_at defaults to now(), which Postgres
// freezes for the whole transaction, so same-tx rows would tie and no ordering assertion
// below would mean anything. Same technique as audit_plan_test.go's corpus builder.
package audit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- fixture -------------------------------------------------------------------------

// pageFixture is a throwaway tenant that starts with no entities and no invoices, so a
// test can add exactly the ones it needs. audit_log rows have no FK and outlive it.
type pageFixture struct{ tenant string }

// pageSeedTenant commits a throwaway tenant. The tenants row goes in as the migrator
// (invoice_app holds SELECT only there).
func pageSeedTenant(t *testing.T, f *fixture) pageFixture {
	t.Helper()
	ctx := context.Background()
	p := pageFixture{tenant: uuid.NewString()}

	if err := db.WithinTenantTx(ctx, f.mig, p.tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, $2)`,
			p.tenant, "audit-page-"+p.tenant[:8])
		return e
	}); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	t.Cleanup(func() {
		_ = db.WithinTenantTx(context.Background(), f.mig, p.tenant, func(tx pgx.Tx) error {
			for _, sql := range []string{
				`DELETE FROM invoices WHERE tenant_id = $1`,
				`DELETE FROM business_entities WHERE tenant_id = $1`,
				`DELETE FROM tenants WHERE id = $1`,
			} {
				if _, e := tx.Exec(context.Background(), sql, p.tenant); e != nil {
					return e
				}
			}
			return nil
		})
	})
	return p
}

// pageSeedEntity commits one business_entities row with NO invoice and NO import_batch.
// That is what makes pageDeleteEntity legal: invoices.invoices_tenant_entity_fk is
// ON DELETE RESTRICT, so deleting an entity that has an invoice raises 23503.
func pageSeedEntity(t *testing.T, f *fixture, p pageFixture, name string) string {
	t.Helper()
	ctx := context.Background()
	id := uuid.NewString()
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, $3)`,
			id, p.tenant, name)
		return e
	}); err != nil {
		t.Fatalf("seed entity %s: %v", name, err)
	}
	return id
}

// pageSeedInvoice commits one invoice under entityID. An entity with an invoice can no
// longer be deleted (see pageSeedEntity).
func pageSeedInvoice(t *testing.T, f *fixture, p pageFixture, entityID string) string {
	t.Helper()
	ctx := context.Background()
	id := uuid.NewString()
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3, $4)`,
			id, p.tenant, entityID, "INV-"+id[:8])
		return e
	}); err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	return id
}

// pageDeleteEntity removes a company while its audit rows stay — the "entity since
// deleted" state of System Design §2, where entity_id survives but company_name is NULL.
func pageDeleteEntity(t *testing.T, f *fixture, p pageFixture, entityID string) {
	t.Helper()
	ctx := context.Background()
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM business_entities WHERE id = $1`, entityID)
		return e
	}); err != nil {
		t.Fatalf("delete entity: %v", err)
	}
}

// --- seeding -------------------------------------------------------------------------

// pageRow is one audit row to insert: an event name, a JSON payload, and how many seconds
// ago it happened.
type pageRow struct {
	event      string
	payload    string
	ageSeconds int
}

// pageSeries builds n rows of one event, oldest first, one second apart, the oldest
// oldestAge seconds ago.
func pageSeries(event string, n, oldestAge int) []pageRow {
	out := make([]pageRow, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, pageRow{event: event, payload: "{}", ageSeconds: oldestAge - i})
	}
	return out
}

// pageInsert writes rows with explicit created_at and returns their ids in argument
// order. The BEFORE INSERT entity resolver still fires, so entity_id is attributed
// exactly as it would be on the production path.
func pageInsert(t *testing.T, f *fixture, p pageFixture, rows []pageRow) []int64 {
	t.Helper()
	ctx := context.Background()
	ids := make([]int64, 0, len(rows))
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		for _, r := range rows {
			var id int64
			err := tx.QueryRow(ctx, `
				INSERT INTO audit_log (tenant_id, actor, event, payload, created_at)
				VALUES ($1, 'page-actor', $2, $3::jsonb, now() - ($4 * interval '1 second'))
				RETURNING id`, p.tenant, r.event, r.payload, r.ageSeconds).Scan(&id)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	}); err != nil {
		t.Fatalf("insert %d audit rows: %v", len(rows), err)
	}
	return ids
}

// pageQuery runs Query inside a tenant-scoped transaction as invoice_app, the only way
// the RLS policy admits any row.
func pageQuery(t *testing.T, f *fixture, p pageFixture, filter audit.Filter) audit.Response {
	t.Helper()
	ctx := context.Background()
	var out audit.Response
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		var err error
		out, err = audit.Query(ctx, tx, filter)
		return err
	}); err != nil {
		t.Fatalf("Query: %v", err)
	}
	return out
}

// pageIDs is the returned page's ids, parsed back to the bigints they came from.
func pageIDs(t *testing.T, r audit.Response) []int64 {
	t.Helper()
	out := make([]int64, 0, len(r.Events))
	for _, e := range r.Events {
		n, err := strconv.ParseInt(e.ID, 10, 64)
		if err != nil {
			t.Fatalf("event id %q does not parse as a bigint: %v", e.ID, err)
		}
		out = append(out, n)
	}
	return out
}

// pageReverse copies ids newest-first, given a slice seeded oldest-first.
func pageReverse(ids []int64) []int64 {
	out := make([]int64, len(ids))
	for i, v := range ids {
		out[len(ids)-1-i] = v
	}
	return out
}

// pageCursorOf decodes r's next_cursor, failing when it is absent.
func pageCursorOf(t *testing.T, r audit.Response) *audit.Cursor {
	t.Helper()
	if r.Page.NextCursor == nil {
		t.Fatalf("next_cursor is nil, want one (has_more = %v)", r.Page.HasMore)
	}
	c, err := audit.DecodeCursor(*r.Page.NextCursor)
	if err != nil {
		t.Fatalf("decode next_cursor %q: %v", *r.Page.NextCursor, err)
	}
	return &c
}

// pageByEvent indexes a page by event name, failing on a duplicate so a caller can rely
// on the lookup being total.
func pageByEvent(t *testing.T, r audit.Response) map[string]audit.Event {
	t.Helper()
	out := make(map[string]audit.Event, len(r.Events))
	for _, e := range r.Events {
		if _, dup := out[e.Event]; dup {
			t.Fatalf("event %q appears twice in the page", e.Event)
		}
		out[e.Event] = e
	}
	return out
}

// pageEqualIDs compares two id slices in order.
func pageEqualIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- AC #1: newest first, no cursor needed --------------------------------------------

func TestAuditRead_FirstPageIsNewestFirst(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	seeded := pageInsert(t, f, p, pageSeries("invoice.created", 10, 100))

	got := pageQuery(t, f, p, audit.Filter{Limit: 10})

	if len(got.Events) == 0 {
		t.Fatalf("page is empty; 10 rows were seeded for this tenant")
	}
	if len(got.Events) != 10 {
		t.Fatalf("page has %d rows, want 10", len(got.Events))
	}
	for i := 1; i < len(got.Events); i++ {
		if !got.Events[i].CreatedAt.Before(got.Events[i-1].CreatedAt) {
			t.Errorf("row %d created_at %v is not strictly older than row %d's %v — the page is not newest-first",
				i, got.Events[i].CreatedAt, i-1, got.Events[i-1].CreatedAt)
		}
	}
	if want := pageReverse(seeded); !pageEqualIDs(pageIDs(t, got), want) {
		t.Errorf("page ids = %v, want %v (the seeded rows, newest first)", pageIDs(t, got), want)
	}
}

// --- AC #2: the keyset boundary does not shift under concurrent inserts (Core AC 3) ----

func TestAuditRead_KeysetBoundaryIsStableAcrossInserts(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	// Seeded oldest-first: seeded[0] is the oldest, seeded[9] the newest.
	seeded := pageInsert(t, f, p, pageSeries("invoice.created", 10, 100))
	newestFirst := pageReverse(seeded)

	first := pageQuery(t, f, p, audit.Filter{Limit: 5})
	if want := newestFirst[:5]; !pageEqualIDs(pageIDs(t, first), want) {
		t.Fatalf("page 1 ids = %v, want %v", pageIDs(t, first), want)
	}
	cursor := pageCursorOf(t, first)

	// Three rows newer than everything on page 1 land between the two fetches.
	intruders := pageInsert(t, f, p, pageSeries("invoice.updated", 3, 10))
	intruding := map[int64]bool{}
	for _, id := range intruders {
		intruding[id] = true
	}

	second := pageQuery(t, f, p, audit.Filter{Limit: 5, Cursor: cursor})

	if want := newestFirst[5:]; !pageEqualIDs(pageIDs(t, second), want) {
		t.Fatalf("page 2 ids = %v, want %v (rows 6-10, unshifted)", pageIDs(t, second), want)
	}
	firstPage := map[int64]bool{}
	for _, id := range newestFirst[:5] {
		firstPage[id] = true
	}
	for _, id := range pageIDs(t, second) {
		if intruding[id] {
			t.Errorf("page 2 holds id %d, one of the 3 rows inserted after page 1 — the boundary shifted", id)
		}
		if firstPage[id] {
			t.Errorf("page 2 repeats id %d from page 1", id)
		}
	}
}

// --- AC #3: has_more and next_cursor agree, and the cursor is well-defined ------------

func TestAuditRead_HasMoreAndNextCursorAgree(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	seeded := pageInsert(t, f, p, pageSeries("invoice.created", 7, 100))
	newestFirst := pageReverse(seeded)

	first := pageQuery(t, f, p, audit.Filter{Limit: 5})
	if len(first.Events) != 5 {
		t.Fatalf("page 1 has %d rows, want 5", len(first.Events))
	}
	if !first.Page.HasMore {
		t.Errorf("page 1 has_more = false, want true — 2 rows remain")
	}
	if first.Page.NextCursor == nil {
		t.Fatalf("page 1 next_cursor is nil while has_more = %v", first.Page.HasMore)
	}
	if first.Page.Limit != 5 {
		t.Errorf("page 1 limit = %d, want 5 (the requested limit, not the over-fetch)", first.Page.Limit)
	}

	second := pageQuery(t, f, p, audit.Filter{Limit: 5, Cursor: pageCursorOf(t, first)})
	if len(second.Events) != 2 {
		t.Fatalf("page 2 has %d rows, want 2", len(second.Events))
	}
	if second.Page.HasMore {
		t.Errorf("page 2 has_more = true, want false — it is the last page")
	}
	if second.Page.NextCursor != nil {
		t.Errorf("page 2 next_cursor = %q, want nil when has_more is false", *second.Page.NextCursor)
	}
	if want := newestFirst[5:]; !pageEqualIDs(pageIDs(t, second), want) {
		t.Errorf("page 2 ids = %v, want %v", pageIDs(t, second), want)
	}
}

func TestAuditRead_CursorReplayedUnderADifferentFilterSetIsWellDefined(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	seeded := pageInsert(t, f, p, pageSeries("invoice.created", 9, 100))
	newestFirst := pageReverse(seeded)

	// Filter set A: limit 5. Limit is the only Filter field Query honours in this
	// subtask, so it is the only dimension a "different filter set" can vary here.
	pageA := pageQuery(t, f, p, audit.Filter{Limit: 5})
	cursor := pageCursorOf(t, pageA)

	// Filter set B: limit 3, same cursor. D-25: well-defined, just semantically odd.
	pageB := pageQuery(t, f, p, audit.Filter{Limit: 3, Cursor: cursor})

	if len(pageB.Events) == 0 {
		t.Fatalf("replayed page is empty; 4 rows are older than the cursor")
	}
	if want := newestFirst[5:8]; !pageEqualIDs(pageIDs(t, pageB), want) {
		t.Fatalf("replayed page ids = %v, want %v (B's rows strictly older than the cursor)",
			pageIDs(t, pageB), want)
	}
	seen := map[int64]bool{}
	for _, e := range pageB.Events {
		id, _ := strconv.ParseInt(e.ID, 10, 64)
		if seen[id] {
			t.Errorf("replayed page repeats id %d", id)
		}
		seen[id] = true
		if e.CreatedAt.After(cursor.CreatedAt) {
			t.Errorf("replayed page holds id %d at %v, newer than the cursor's %v",
				id, e.CreatedAt, cursor.CreatedAt)
		}
		if e.CreatedAt.Equal(cursor.CreatedAt) && id >= cursor.ID {
			t.Errorf("replayed page holds id %d at the cursor timestamp but not strictly below the cursor id %d",
				id, cursor.ID)
		}
	}
}

// TestAuditRead_HasMoreIsFalseWhenExactlyLimitRowsExist is the boundary the over-fetch
// trim must get right: available rows == limit exactly, so the over-fetch (limit+1)
// returns no surplus row. A `>=` trim condition (instead of `>`) reports has_more=true
// here even though nothing further exists.
func TestAuditRead_HasMoreIsFalseWhenExactlyLimitRowsExist(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	pageInsert(t, f, p, pageSeries("invoice.created", 5, 50))

	got := pageQuery(t, f, p, audit.Filter{Limit: 5})
	if len(got.Events) != 5 {
		t.Fatalf("page has %d rows, want 5", len(got.Events))
	}
	if got.Page.HasMore {
		t.Errorf("has_more = true, want false — exactly 5 rows exist and the page holds all of them")
	}
	if got.Page.NextCursor != nil {
		t.Errorf("next_cursor = %q, want nil when has_more is false", *got.Page.NextCursor)
	}
}

// TestAuditRead_KeysetTiebreaksOnIDWhenTimestampsCollide is the case the file header's
// own seeding rule (see top of file) otherwise never exercises: rows sharing one
// created_at. now() is frozen per transaction, so 3 rows inserted in one pageInsert call
// share an identical created_at; ids still increase in insertion order, so ORDER BY
// created_at DESC, id DESC breaks the tie deterministically. This is the only case that
// distinguishes ids[f.Limit-1] (the correct boundary row) from the untrimmed surplus row.
func TestAuditRead_KeysetTiebreaksOnIDWhenTimestampsCollide(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	tied := pageInsert(t, f, p, []pageRow{
		{event: "invoice.created", payload: "{}", ageSeconds: 10},
		{event: "invoice.updated", payload: "{}", ageSeconds: 10},
		{event: "invoice.submitted", payload: "{}", ageSeconds: 10},
	})
	older := pageInsert(t, f, p, []pageRow{
		{event: "invoice.rejected", payload: "{}", ageSeconds: 20},
	})

	first := pageQuery(t, f, p, audit.Filter{Limit: 2})
	if want := []int64{tied[2], tied[1]}; !pageEqualIDs(pageIDs(t, first), want) {
		t.Fatalf("page 1 ids = %v, want %v (id DESC breaks the created_at tie)", pageIDs(t, first), want)
	}
	if !first.Page.HasMore {
		t.Fatalf("has_more = false, want true — 2 more rows remain")
	}

	second := pageQuery(t, f, p, audit.Filter{Limit: 2, Cursor: pageCursorOf(t, first)})
	if want := []int64{tied[0], older[0]}; !pageEqualIDs(pageIDs(t, second), want) {
		t.Errorf("page 2 ids = %v, want %v — a cursor built from the untrimmed surplus row "+
			"silently skips tied[0] here", pageIDs(t, second), want)
	}
}

// TestAuditRead_LimitMustBePositive: no AC covers this guard directly, but removing it
// panics (index out of range) once the over-fetch trims to an empty slice, rather than
// failing gracefully — confirmed by mutation during QA. The handler (AUDIT-04-07) is
// expected to clamp/validate limit before calling Query; this guard is Query's own
// last line of defense.
func TestAuditRead_LimitMustBePositive(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	ctx := context.Background()

	for _, limit := range []int{0, -1} {
		err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
			_, err := audit.Query(ctx, tx, audit.Filter{Limit: limit})
			return err
		})
		if err == nil {
			t.Errorf("limit %d: Query returned no error, want one", limit)
		}
	}
}

// --- AC #4/#5: the four company states -------------------------------------------------

func TestAuditRead_CompanyColumnDistinguishesAllFourStates(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)

	live := pageSeedEntity(t, f, p, "Live Co")
	gone := pageSeedEntity(t, f, p, "Gone Co")

	pageInsert(t, f, p, []pageRow{
		// portfolio.entity.* carries the entity id in the payload directly, so the
		// insert trigger attributes it without an invoice — see the resolver's
		// v_direct branch.
		{event: "portfolio.entity.created", payload: `{"id":"` + live + `"}`, ageSeconds: 40},
		{event: "portfolio.entity.updated", payload: `{"id":"` + gone + `"}`, ageSeconds: 30},
		{event: "approval_policy.published", payload: `{"policy_id":"p1"}`, ageSeconds: 20},
		{event: "document.created", payload: `{"id":"` + uuid.NewString() + `"}`, ageSeconds: 10},
	})
	pageDeleteEntity(t, f, p, gone)

	got := pageQuery(t, f, p, audit.Filter{Limit: 10})
	if len(got.Events) != 4 {
		t.Fatalf("page has %d rows, want 4", len(got.Events))
	}
	byEvent := pageByEvent(t, got)

	cases := []struct {
		event     string
		entityID  *string
		company   *string
		scope     audit.CompanyScope
		stateName string
	}{
		{"portfolio.entity.created", &live, readerPtr("Live Co"), audit.ScopeCompany, "a live company"},
		{"portfolio.entity.updated", &gone, nil, audit.ScopeCompany, "a company that no longer exists"},
		{"approval_policy.published", nil, nil, audit.ScopeWorkspace, "workspace-level"},
		{"document.created", nil, nil, audit.ScopeUnattributed, "unattributed"},
	}
	for _, c := range cases {
		e, ok := byEvent[c.event]
		if !ok {
			t.Errorf("%s: no row for event %q in the page", c.stateName, c.event)
			continue
		}
		if !pageSamePtr(e.EntityID, c.entityID) {
			t.Errorf("%s (%s): entity_id = %s, want %s",
				c.stateName, c.event, pageShow(e.EntityID), pageShow(c.entityID))
		}
		if !pageSamePtr(e.CompanyName, c.company) {
			t.Errorf("%s (%s): company_name = %s, want %s",
				c.stateName, c.event, pageShow(e.CompanyName), pageShow(c.company))
		}
		if e.CompanyScope != c.scope {
			t.Errorf("%s (%s): company_scope = %q, want %q", c.stateName, c.event, e.CompanyScope, c.scope)
		}
	}
}

func TestAuditRead_CompanyColumnDistinguishesWorkspaceFromDeletedCompany(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)

	live := pageSeedEntity(t, f, p, "Still Here Ltd")
	gone := pageSeedEntity(t, f, p, "Wound Up Ltd")

	pageInsert(t, f, p, []pageRow{
		{event: "workflow_role.created", payload: `{"role_key":"preparer"}`, ageSeconds: 30},
		{event: "portfolio.entity.created", payload: `{"id":"` + live + `"}`, ageSeconds: 20},
		{event: "portfolio.entity.updated", payload: `{"id":"` + gone + `"}`, ageSeconds: 10},
	})
	pageDeleteEntity(t, f, p, gone)

	got := pageQuery(t, f, p, audit.Filter{Limit: 10})
	if len(got.Events) != 3 {
		t.Fatalf("page has %d rows, want 3", len(got.Events))
	}
	byEvent := pageByEvent(t, got)

	e, ok := byEvent["workflow_role.created"]
	if !ok {
		t.Fatalf("workflow_role.created is missing from the page")
	}
	if e.EntityID != nil || e.CompanyName != nil {
		t.Errorf("workspace-level row: (entity_id, company_name) = (%s, %s), want (null, null)",
			pageShow(e.EntityID), pageShow(e.CompanyName))
	}
	if e.CompanyScope != audit.ScopeWorkspace {
		t.Errorf("workspace-level row: company_scope = %q, want %q", e.CompanyScope, audit.ScopeWorkspace)
	}

	e, ok = byEvent["portfolio.entity.created"]
	if !ok {
		t.Fatalf("portfolio.entity.created is missing from the page")
	}
	if e.EntityID == nil || *e.EntityID != live || e.CompanyName == nil || *e.CompanyName != "Still Here Ltd" {
		t.Errorf("live-company row: (entity_id, company_name) = (%s, %s), want (%s, Still Here Ltd)",
			pageShow(e.EntityID), pageShow(e.CompanyName), live)
	}
	if e.CompanyScope != audit.ScopeCompany {
		t.Errorf("live-company row: company_scope = %q, want %q", e.CompanyScope, audit.ScopeCompany)
	}

	// The distinguishing pair: entity_id survives the company, company_name does not.
	e, ok = byEvent["portfolio.entity.updated"]
	if !ok {
		t.Fatalf("portfolio.entity.updated is missing from the page")
	}
	if e.EntityID == nil || *e.EntityID != gone || e.CompanyName != nil {
		t.Errorf("deleted-company row: (entity_id, company_name) = (%s, %s), want (%s, null) — "+
			"a deleted company must stay distinguishable from a workspace-level row",
			pageShow(e.EntityID), pageShow(e.CompanyName), gone)
	}
	if e.CompanyScope != audit.ScopeCompany {
		t.Errorf("deleted-company row: company_scope = %q, want %q", e.CompanyScope, audit.ScopeCompany)
	}
}

// TestAuditRead_DocumentEventIsNeverRenderedAsWorkspace is the D-23 control needle. The
// three document.* payloads carry a REAL invoice id, so an implementation that dispatched
// on payload keys instead of event names would attribute them to a company and this would
// catch it; classifying them as "workspace" instead of "unattributed" is the other
// failure it fences.
func TestAuditRead_DocumentEventIsNeverRenderedAsWorkspace(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	entity := pageSeedEntity(t, f, p, "Doc Co")
	invoice := pageSeedInvoice(t, f, p, entity)

	// Written through the production entry point so the real insert trigger runs.
	for _, event := range readerDocumentEvents {
		if err := db.WithinTenantTx(context.Background(), f.app, p.tenant, func(tx pgx.Tx) error {
			return audit.Record(context.Background(), tx, "page-actor", event,
				map[string]any{"id": invoice})
		}); err != nil {
			t.Fatalf("record %s: %v", event, err)
		}
	}

	got := pageQuery(t, f, p, audit.Filter{Limit: 10})
	byEvent := pageByEvent(t, got)
	for _, event := range readerDocumentEvents {
		e, ok := byEvent[event]
		if !ok {
			t.Errorf("%s is missing from the page", event)
			continue
		}
		if e.CompanyScope == audit.ScopeWorkspace {
			t.Errorf("%s: company_scope = %q — a document event is never firm-wide", event, e.CompanyScope)
		}
		if e.CompanyScope != audit.ScopeUnattributed {
			t.Errorf("%s: company_scope = %q, want %q", event, e.CompanyScope, audit.ScopeUnattributed)
		}
		if e.EntityID != nil {
			t.Errorf("%s: entity_id = %s, want null — the payload id is a document id, not an invoice id",
				event, pageShow(e.EntityID))
		}
	}
}

// --- AC #6: events is [], never null ---------------------------------------------------

func TestAuditRead_EmptyPageMarshalsToEmptyArrayNotNull(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f) // no rows seeded

	got := pageQuery(t, f, p, audit.Filter{Limit: 20})
	if len(got.Events) != 0 {
		t.Fatalf("page has %d rows, want 0 for a tenant with no audit rows", len(got.Events))
	}
	if got.Events == nil {
		t.Errorf("Events is a nil slice; the store coerces it to make([]Event, 0, n) so it can never marshal as null")
	}

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		t.Fatalf("unmarshal response body: %v", err)
	}
	if string(probe["events"]) != "[]" {
		t.Errorf(`"events" = %s, want []`, probe["events"])
	}
	// Facets' three slices carry the same never-nil invariant (D-9), coerced here even
	// though their contents arrive in AUDIT-04-05.
	var facets map[string]json.RawMessage
	if err := json.Unmarshal(probe["facets"], &facets); err != nil {
		t.Fatalf("unmarshal facets: %v", err)
	}
	for _, key := range []string{"event", "actor", "company"} {
		if string(facets[key]) != "[]" {
			t.Errorf(`"facets.%s" = %s, want []`, key, facets[key])
		}
	}
}

// --- AC #7: id is a decimal string ------------------------------------------------------

func TestAuditRead_IDIsAStringOnTheWire(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	seeded := pageInsert(t, f, p, pageSeries("invoice.created", 1, 10))

	got := pageQuery(t, f, p, audit.Filter{Limit: 10})
	if len(got.Events) != 1 {
		t.Fatalf("page has %d rows, want 1", len(got.Events))
	}

	body, err := json.Marshal(got.Events[0])
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	raw := string(probe["id"])
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		t.Fatalf(`"id" = %s, want a JSON string — a bigint past 2^53 loses precision as a JSON number`, raw)
	}
	var asString string
	if err := json.Unmarshal(probe["id"], &asString); err != nil {
		t.Fatalf("id is not a JSON string: %v", err)
	}
	n, err := strconv.ParseInt(asString, 10, 64)
	if err != nil {
		t.Fatalf("id %q does not parse as a decimal bigint: %v", asString, err)
	}
	if n != seeded[0] {
		t.Errorf("id = %d, want %d (the seeded row)", n, seeded[0])
	}
}

// --- AC #8: total is a count under the filters ------------------------------------------

func TestAuditRead_TotalMatchesAnIndependentCount(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	pageInsert(t, f, p, pageSeries("invoice.created", 40, 200))

	got := pageQuery(t, f, p, audit.Filter{Limit: 10})
	if len(got.Events) != 10 {
		t.Fatalf("page has %d rows, want 10", len(got.Events))
	}
	if got.Total != 40 {
		t.Errorf("total = %d, want 40 (every matching row, not the page)", got.Total)
	}

	// The oracle is the hardcoded 40 above, not this: the statement below is the same
	// SQL text Query runs, in the same RLS-scoped transaction, so it cannot diverge
	// without 40 diverging too. It is kept because the Test Specs table asks for it, and
	// it does still catch Total being read off the wrong column or left at zero.
	var direct int
	if err := db.WithinTenantTx(context.Background(), f.app, p.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM audit_log`).Scan(&direct)
	}); err != nil {
		t.Fatalf("count audit_log directly: %v", err)
	}
	if got.Total != direct {
		t.Errorf("total = %d, direct count(*) = %d", got.Total, direct)
	}

	// A cursor is a position, not a filter: total must not shrink as the caller pages.
	second := pageQuery(t, f, p, audit.Filter{Limit: 10, Cursor: pageCursorOf(t, got)})
	if second.Total != got.Total {
		t.Errorf("page 2 total = %d, page 1 total = %d — the cursor is being counted as a filter",
			second.Total, got.Total)
	}
}

// --- small helpers ---------------------------------------------------------------------

// pageSamePtr compares two optional strings by value.
func pageSamePtr(got, want *string) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}

// pageShow renders an optional string for a failure message.
func pageShow(v *string) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprintf("%q", *v)
}
