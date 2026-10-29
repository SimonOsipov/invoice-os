package audit_test

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type recordSink struct{ records [][]string }

func (s *recordSink) Write(r []string) error {
	s.records = append(s.records, append([]string(nil), r...))
	return nil
}

// exportCSV runs WriteExportCSV in p's tenant transaction.
func exportCSV(t *testing.T, f *fixture, p pageFixture) (int, [][]string) {
	t.Helper()
	ctx := context.Background()
	sink := &recordSink{}
	var n int
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		var err error
		n, err = audit.WriteExportCSV(ctx, tx, sink)
		return err
	}); err != nil {
		t.Fatalf("WriteExportCSV: %v", err)
	}
	return n, sink.records
}

func exportCol(t *testing.T, name string) int {
	t.Helper()
	for i, c := range audit.ExportCSVHeader {
		if c == name {
			return i
		}
	}
	t.Fatalf("no column %q", name)
	return -1
}

func TestExportCSV_WritesEveryRowNewestFirstAcrossPages(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)

	// Rows 702-704 share one created_at and straddle the page boundary at 500
	// newest-first, so the keyset tiebreak on id is exercised.
	rows := pageSeries("invoice.updated", 1203, 2000)
	for _, i := range []int{703, 704} {
		rows[i].ageSeconds = rows[702].ageSeconds
	}
	ids := pageInsert(t, f, p, rows)

	type keyed struct {
		id  int64
		age int
	}
	want := make([]keyed, len(ids))
	for i := range ids {
		want[i] = keyed{ids[i], rows[i].ageSeconds}
	}
	sort.Slice(want, func(a, b int) bool {
		if want[a].age != want[b].age {
			return want[a].age < want[b].age
		}
		return want[a].id > want[b].id
	})

	n, recs := exportCSV(t, f, p)
	if n != 1203 || len(recs) != 1204 {
		t.Fatalf("n = %d, records = %d, want 1203 and 1204", n, len(recs))
	}
	if len(recs[0]) != len(audit.ExportCSVHeader) || recs[0][0] != "id" || recs[0][10] != "payload" {
		t.Fatalf("first record = %v, want the header", recs[0])
	}
	for i, w := range want {
		if got := recs[i+1][0]; got != strconv.FormatInt(w.id, 10) {
			t.Fatalf("record %d id = %s, want %d", i, got, w.id)
		}
	}
}

func TestExportCSV_ResolvesActorsCompaniesAndScopes(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)

	member := uuid.NewString()
	filtSeedMembership(t, f, p, member, "Ada Okonkwo")
	live := pageSeedEntity(t, f, p, "Live Co")
	gone := pageSeedEntity(t, f, p, "Gone Co")
	inv := pageSeedInvoice(t, f, p, live)

	filtInsert(t, f, p, []filtRow{
		{event: "portfolio.entity.created", actor: member, payload: `{"id":"` + live + `"}`, ageSeconds: 50},
		{event: "approval_policy.published", actor: "system", payload: `{"policy_id":"p1"}`, ageSeconds: 40},
		{event: "portfolio.entity.updated", actor: "system", payload: `{"id":"` + gone + `"}`, ageSeconds: 30},
		{event: "invoice.created", actor: "system", payload: `{"id":"` + inv + `","invoice_number":"N1"}`, ageSeconds: 20},
	})
	pageDeleteEntity(t, f, p, gone)

	_, recs := exportCSV(t, f, p)
	by := map[string][]string{}
	for _, r := range recs[1:] {
		by[r[exportCol(t, "event")]] = r
	}
	cases := []struct{ event, name, kind, entity, company, scope, invoice string }{
		{"portfolio.entity.created", "Ada Okonkwo", "person", live, "Live Co", "company", ""},
		{"approval_policy.published", "System", "system", "", "", "workspace", ""},
		{"portfolio.entity.updated", "System", "system", gone, "", "company", ""},
		{"invoice.created", "System", "system", live, "Live Co", "company", inv},
	}
	for _, c := range cases {
		r, ok := by[c.event]
		if !ok {
			t.Errorf("no record for %s", c.event)
			continue
		}
		got := []string{r[exportCol(t, "actor_name")], r[exportCol(t, "actor_kind")], r[exportCol(t, "entity_id")],
			r[exportCol(t, "company_name")], r[exportCol(t, "company_scope")], r[exportCol(t, "invoice_id")]}
		want := []string{c.name, c.kind, c.entity, c.company, c.scope, c.invoice}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s: cell %d = %q, want %q", c.event, i, got[i], want[i])
			}
		}
	}
}

func TestExportCSV_PayloadIsCompactJSON(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	pageInsert(t, f, p, []pageRow{{event: "approval_policy.published", payload: `{"a": [1, 2], "b": "x,\"y\"\nz"}`}})

	_, recs := exportCSV(t, f, p)
	if got, want := recs[1][exportCol(t, "payload")], `{"a":[1,2],"b":"x,\"y\"\nz"}`; got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

func TestRLS_ExportCSVHoldsOnlyTheBoundTenant(t *testing.T) {
	f := requireFixture(t)
	a, b := pageSeedTenant(t, f), pageSeedTenant(t, f)
	aIDs := pageInsert(t, f, a, pageSeries("invoice.updated", 3, 30))
	pageInsert(t, f, b, pageSeries("invoice.updated", 3, 30))

	n, recs := exportCSV(t, f, a)
	if n != 3 || len(recs) != 4 {
		t.Fatalf("n = %d, records = %d, want 3 and 4", n, len(recs))
	}
	own := map[string]bool{}
	for _, id := range aIDs {
		own[strconv.FormatInt(id, 10)] = true
	}
	for _, r := range recs[1:] {
		if !own[r[0]] {
			t.Errorf("id %s is not tenant A's", r[0])
		}
	}
}

func TestExportCSV_AnEmptyLogWritesTheHeaderOnly(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)

	n, recs := exportCSV(t, f, p)
	if n != 0 || len(recs) != 1 {
		t.Fatalf("n = %d, records = %d, want 0 and 1", n, len(recs))
	}
	for i, c := range audit.ExportCSVHeader {
		if recs[0][i] != c {
			t.Fatalf("header cell %d = %q, want %q", i, recs[0][i], c)
		}
	}
}

func TestExportCSV_CreatedAtIsUTCRFC3339NanoAndEventAndActorAreVerbatim(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	ids := pageInsert(t, f, p, []pageRow{{event: "approval_policy.published", payload: "{}", ageSeconds: 90}})

	var want time.Time
	if err := db.WithinTenantTx(context.Background(), f.app, p.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT created_at FROM audit_log WHERE id = $1`, ids[0]).Scan(&want)
	}); err != nil {
		t.Fatal(err)
	}
	_, recs := exportCSV(t, f, p)
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2", len(recs))
	}
	r := recs[1]
	if got := r[exportCol(t, "created_at")]; got != want.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("created_at = %q, want %q", got, want.UTC().Format(time.RFC3339Nano))
	}
	if r[exportCol(t, "event")] != "approval_policy.published" || r[exportCol(t, "actor")] != "page-actor" {
		t.Fatalf("event/actor cells = %q / %q", r[exportCol(t, "event")], r[exportCol(t, "actor")])
	}
	if r[exportCol(t, "id")] != strconv.FormatInt(ids[0], 10) {
		t.Fatalf("id cell = %q", r[exportCol(t, "id")])
	}
}

// The workspace export is admin-only and unfiltered: an account row is written with its payload and as workspace-level.
func TestExportCSV_WritesAccountRowsWithPayload(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	member := uuid.NewString()
	filtInsert(t, f, p, []filtRow{{
		event: "account.signed_in", actor: member, ageSeconds: 10,
		payload: `{"ip_address":"198.51.100.7","device":"Chrome on macOS","occurred_at":"2026-10-29T08:00:00Z"}`,
	}})

	n, recs := exportCSV(t, f, p)

	if n != 1 || len(recs) != 2 {
		t.Fatalf("n = %d, records = %d, want 1 and 2", n, len(recs))
	}
	row := recs[1]
	var payload map[string]string
	if err := json.Unmarshal([]byte(row[exportCol(t, "payload")]), &payload); err != nil {
		t.Fatalf("payload cell %q is not JSON: %v", row[exportCol(t, "payload")], err)
	}
	for _, k := range []string{"ip_address", "device", "occurred_at"} {
		if payload[k] == "" {
			t.Errorf("payload %v lacks %s", payload, k)
		}
	}
	if got := row[exportCol(t, "actor")]; got != member {
		t.Errorf("actor = %q, want %q", got, member)
	}
	if got := row[exportCol(t, "company_scope")]; got != "workspace" {
		t.Errorf("company_scope = %q, want workspace", got)
	}
}
