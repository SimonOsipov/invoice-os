// reader_detail_adversarial_db_test.go: EXTR-11-01's QA pass over (*Reader).Detail — the
// commit-failure path, the child tables' own isolation, and the edges the AC suite leaves at
// their happy value. Shares reader_db_test.go's fixtures and tracer.
package extraction_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// rqaWireMirrorsSource is the SPA test whose extractor decides whether these structs have a
// mirror at all.
const rqaWireMirrorsSource = "../../frontend/app/src/lib/wireMirrors.test.ts"

// rqaKillOn terminates the backend as the named statement ends, so only COMMIT is left.
// TraceQueryEnd fires from rows.Close, after the result reader is drained.
func rqaKillOn(t *testing.T, ctx context.Context, needle string, killed *int) *rdQueryTracer {
	t.Helper()
	return &rdQueryTracer{onEnd: func(conn *pgx.Conn, sql string) {
		if !strings.Contains(sql, needle) {
			return
		}
		*killed++
		pid := conn.PgConn().PID()
		var gone bool
		if err := stRequire(t).super.QueryRow(ctx,
			`SELECT pg_terminate_backend($1, 5000)`, pid).Scan(&gone); err != nil {
			t.Errorf("terminate backend %d: %v", pid, err)
			return
		}
		if !gone {
			t.Errorf("backend %d survived pg_terminate_backend, so the commit below may succeed", pid)
		}
	}}
}

// rqaSQLState returns the SQLSTATE of err, or "" when it carries none.
func rqaSQLState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// rqaInsertPage writes one page-image row as the superuser and returns the error verbatim:
// these cases are about which INSERTs the schema refuses.
func rqaInsertPage(ctx context.Context, t *testing.T, tenantID, documentID string, page int) error {
	t.Helper()
	_, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO extraction_page_images
		     (tenant_id, document_id, page_number, width_px, height_px, storage_key)
		 VALUES ($1, $2, $3, 100, 100, $4)`,
		tenantID, documentID, page, rvdPageKey(tenantID, page))
	return err
}

// rqaInsertField writes one field-result row as the superuser and returns the error verbatim.
func rqaInsertField(ctx context.Context, t *testing.T, tenantID, jobID, name string) error {
	t.Helper()
	_, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO extraction_field_results
		     (tenant_id, extraction_job_id, field_name, value, candidate_rank)
		 VALUES ($1, $2, $3, $4, 0)`,
		tenantID, jobID, name, "v")
	return err
}

// AC 5's last path: the scan already filled the struct and only COMMIT is left. Detail assigns
// inside the closure, so discarding on the way out is the only thing that keeps uncommitted
// rows off the wire. The mirror of TestRLS_ExtractionReaderDiscardsRowsWhenTheCommitFails,
// which covers JobsForDocument only.
func TestRLS_ExtractionDetailDiscardsRowsWhenTheCommitFails(t *testing.T) {
	ctx := t.Context()
	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	rvdSeedPage(t, ctx, tenantA, docA, 1, 1275, 1651)
	rvdSeedPage(t, ctx, tenantA, docA, 2, 1275, 1651)
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("A-0001"),
		&rvdBox{Page: 1, X0: 0.1, Y0: 0.2, X1: 0.3, Y1: 0.4}, 0, nil, now)

	// Positive control on the untraced pool: the rows exist, so the empty answer below is not
	// an empty-fixture artefact.
	ctl, err := rdReader(t).Detail(ctxA, jobA)
	if err != nil || len(ctl.Pages) != 2 || len(ctl.Fields) != 1 {
		t.Fatalf("control read returned %d page(s), %d field(s) and %v, want 2, 1 and no error",
			len(ctl.Pages), len(ctl.Fields), err)
	}

	// The LAST of the three statements: killing on the first would exercise the query-failure
	// path, which returns emptyDetail from detailTx rather than from Detail.
	var killed int
	r := &extraction.Reader{Pool: rdTracedPool(t, rqaKillOn(t, ctx, "extraction_field_results", &killed))}
	got, err := r.Detail(ctxA, jobA)

	if killed != 1 {
		t.Fatalf("the tracer fired on %d extraction_field_results quer(ies), want 1; the commit-failure path was not reached", killed)
	}
	if err == nil {
		t.Fatal("the read reported no error although its transaction could not commit")
	}
	if got.ID != "" || got.DocumentID != "" || got.State != "" {
		t.Errorf("the commit-failure path carried id %q / document %q / state %q; a row read in a transaction that never committed must not reach the caller",
			got.ID, got.DocumentID, got.State)
	}
	if got.Document != (extraction.ExtractionDocument{}) {
		t.Errorf("the commit-failure path carried document %+v, want the zero value", got.Document)
	}
	if got.Pages == nil || got.Fields == nil {
		t.Errorf("the commit-failure path returned Pages=%v Fields=%v; a nil slice marshals to JSON null", got.Pages, got.Fields)
	}
	if len(got.Pages) != 0 || len(got.Fields) != 0 {
		t.Errorf("the commit-failure path returned %d page(s) %v and %d field(s) %v beside its error %v",
			len(got.Pages), rvdPageNumbers(got.Pages), len(got.Fields), rvdFieldNames(got.Fields), err)
	}
}

// AC 3's wire proof: one transaction, three statements, nothing else.
// TestRLS_ExtractionReaderIssuesNoStatementBeyondBeginSelectCommit holds this for
// JobsForDocument and stops at its single SELECT, so it says nothing about the three helpers.
// A per-helper WithinRequestTenantTx would show up here as three begins.
func TestRLS_ExtractionDetailIssuesNoStatementBeyondBeginSelectCommit(t *testing.T) {
	ctx := t.Context()
	tr := &rdQueryTracer{}
	r := &extraction.Reader{Pool: rdTracedPool(t, tr)}

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	for page := 1; page <= 3; page++ {
		rvdSeedPage(t, ctx, tenantA, docA, page, 1275, 1651)
	}
	for i, name := range []string{"invoice_number", "supplier_tin"} {
		rvdSeedField(t, ctx, tenantA, jobA, name, rvdStr("v"), nil, 0, nil, now.Add(time.Duration(i)*time.Millisecond))
	}

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	// The rows make the count mean something: an N+1 over an empty page set issues the same
	// five statements.
	if len(got.Pages) != 3 || len(got.Fields) != 2 {
		t.Fatalf("got %d page(s) and %d field(s), want 3 and 2", len(got.Pages), len(got.Fields))
	}

	_, seen := tr.matching(rpTable)
	if len(seen) != 5 {
		t.Fatalf("one detail read issued %d traced statement(s), want 5 (begin, three SELECTs, commit); the pool saw %v",
			len(seen), seen)
	}
	if seen[0] != "begin" || seen[4] != "commit" {
		t.Errorf("the traced statements were %v, want one begin/commit pair around three SELECTs", seen)
	}
	for i, table := range []string{"extraction_jobs", "extraction_page_images", "extraction_field_results"} {
		if !strings.Contains(seen[i+1], table) {
			t.Errorf("statement %d was %q, want the read of %s", i+1, seen[i+1], table)
		}
	}
}

// The child queries name document_id and extraction_job_id and no tenant_id, so what stops
// them reaching another tenant's rows is the composite FK: a child row can only ever point at
// a parent of its OWN tenant. Weaken either FK to a bare single column and the reader's
// scoping becomes a genuine cross-tenant leak that no other case in this package notices.
func TestRLS_ExtractionDetailChildTablesRefuseACrossTenantRow(t *testing.T) {
	ctx := t.Context()
	_, tenantA, docA := rdTenant(t, ctx, "active")
	_, tenantB, docB := rdTenant(t, ctx, "active")

	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	jobB := rdSeedJob(t, ctx, tenantB, docB, "succeeded", now, nil)

	// Positive controls first: the same INSERTs succeed against B's own parents, so the two
	// refusals below are the composite FK and not a malformed statement.
	if err := rqaInsertPage(ctx, t, tenantB, docB, 1); err != nil {
		t.Fatalf("B seeding a page image on its own document %s: %v", docB, err)
	}
	if err := rqaInsertField(ctx, t, tenantB, jobB, "invoice_number"); err != nil {
		t.Fatalf("B seeding a field result on its own job %s: %v", jobB, err)
	}

	for _, c := range []struct {
		label string
		err   error
	}{
		{"a page image for tenant B pointing at tenant A's document", rqaInsertPage(ctx, t, tenantB, docA, 1)},
		{"a field result for tenant B pointing at tenant A's job", rqaInsertField(ctx, t, tenantB, jobA, "invoice_number")},
	} {
		if c.err == nil {
			t.Errorf("%s was accepted; the child tables' isolation rests on the composite FK refusing it", c.label)
			continue
		}
		if code := rqaSQLState(c.err); code != "23503" {
			t.Errorf("%s failed with SQLSTATE %q (%v), want 23503 foreign_key_violation", c.label, code, c.err)
		}
	}
}

// rqaOtherDocHash is a content_hash for a SECOND document under a tenant stTenant has already
// given one. stTenant seeds every first document with strings.Repeat("a", 64) and
// documents_tenant_content_hash_uq is (tenant_id, content_hash), so a hash spelled from one hex
// digit of the tenant id collides on one uuid in sixteen
// (TestRLS_ExtractionFixtureSeedsASecondDocumentForAnALeadingTenant).
func rqaOtherDocHash(tenantID string) string {
	return strings.ReplaceAll(tenantID, "-", "") + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// The a-leading tenant seeded deliberately rather than waited for.
func TestRLS_ExtractionFixtureSeedsASecondDocumentForAnALeadingTenant(t *testing.T) {
	ctx := t.Context()
	h := stRequire(t)

	tenantID := "a" + uuid.NewString()[1:]
	if _, err := uuid.Parse(tenantID); err != nil || !strings.HasPrefix(tenantID, "a") {
		t.Fatalf("the fixture tenant id %q is not an a-leading uuid (%v), so this case reproduces nothing", tenantID, err)
	}
	if _, err := h.super.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, $2)`,
		tenantID, "extr-11 "+tenantID[:8]); err != nil {
		t.Fatalf("seed the a-leading tenant: %v", err)
	}
	t.Cleanup(func() {
		if _, err := h.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID); err != nil {
			t.Errorf("teardown tenant %s: %v", tenantID, err)
		}
	})

	// stTenant's own first document, spelled the way it spells it. This is the row the old
	// derivation collided with.
	first := uuid.NewString()
	stTenantHash := strings.Repeat("a", 64)
	if _, err := h.super.Exec(ctx,
		`INSERT INTO documents (id, tenant_id, storage_key, content_hash, size_bytes)
		 VALUES ($1, $2, $3, $4, $5)`,
		first, tenantID, "extr-11/"+first, stTenantHash, 1024); err != nil {
		t.Fatalf("seed the first document the way stTenant does: %v", err)
	}

	hash := rqaOtherDocHash(tenantID)
	if hash == stTenantHash {
		t.Fatalf("the derived hash IS stTenant's own %q; the seed below is the collision, not a test of it", stTenantHash)
	}
	second := rvdSeedDocumentMeta(t, ctx, tenantID, nil, nil, 2048, hash, time.Now().UTC())

	// Both rows are really there: an insert that silently did nothing raises no error either.
	var got []string
	rows, err := h.super.Query(ctx,
		`SELECT content_hash FROM documents WHERE tenant_id = $1 ORDER BY content_hash`, tenantID)
	if err != nil {
		t.Fatalf("read back tenant %s documents: %v", tenantID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var ch string
		if err := rows.Scan(&ch); err != nil {
			t.Fatalf("scan content_hash: %v", err)
		}
		got = append(got, ch)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tenant %s documents: %v", tenantID, err)
	}
	if len(got) != 2 {
		t.Fatalf("tenant %s holds %d document(s) %v, want 2 (%s and %s)", tenantID, len(got), got, first, second)
	}
	if got[0] == got[1] {
		t.Errorf("both documents carry content_hash %q; documents_tenant_content_hash_uq should have refused that", got[0])
	}
}

// The reader-level half of the same rule, in BOTH directions: the AC suite builds tenant B's
// rows but never uses B's identity, so a refusal that happened to depend on A's data shape
// would pass it. Each tenant holds a second document as well, so the page and field counts
// are per document and per job rather than per tenant.
func TestRLS_ExtractionDetailCrossTenantRefusalHoldsBothDirections(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	ctxB, tenantB, docB := rdTenant(t, ctx, "active")

	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	jobB := rdSeedJob(t, ctx, tenantB, docB, "succeeded", now, nil)

	rvdSeedPage(t, ctx, tenantA, docA, 1, 1275, 1651)
	rvdSeedPage(t, ctx, tenantA, docA, 2, 1275, 1651)
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("A-0001"), nil, 0, nil, now)
	rvdSeedField(t, ctx, tenantA, jobA, "supplier_tin", rvdStr("A-TIN"), nil, 0, nil, now.Add(time.Millisecond))

	rvdSeedPage(t, ctx, tenantB, docB, 1, 900, 1200)
	rvdSeedField(t, ctx, tenantB, jobB, "invoice_number", rvdStr("B-0001"), nil, 0, nil, now)

	// Each tenant's own second document and job: without these the counts above are also what
	// a per-tenant answer returns.
	for _, o := range []struct {
		tenant string
		pages  int
		fields []string
	}{
		{tenantA, 5, []string{"issue_date", "currency", "total_amount"}},
		{tenantB, 4, []string{"issue_date", "currency"}},
	} {
		other := rvdSeedDocumentMeta(t, ctx, o.tenant, nil, nil, 2048, rqaOtherDocHash(o.tenant), now)
		otherJob := rdSeedJob(t, ctx, o.tenant, other, "succeeded", now, nil)
		for page := 1; page <= o.pages; page++ {
			rvdSeedPage(t, ctx, o.tenant, other, page, 600, 800)
		}
		for i, name := range o.fields {
			rvdSeedField(t, ctx, o.tenant, otherJob, name, rvdStr("x"), nil, 0, nil, now.Add(time.Duration(i)*time.Millisecond))
		}
	}

	for _, c := range []struct {
		label      string
		ctx        context.Context
		job        string
		wantPages  int
		wantFields int
	}{
		{"A reading its own job", ctxA, jobA, 2, 2},
		{"B reading its own job", ctxB, jobB, 1, 1},
	} {
		got, err := r.Detail(c.ctx, c.job)
		if err != nil {
			t.Fatalf("%s (%s): %v", c.label, c.job, err)
		}
		if got.ID != c.job {
			t.Fatalf("%s came back as job %q, want %q", c.label, got.ID, c.job)
		}
		if len(got.Pages) != c.wantPages {
			t.Errorf("%s carried %d page(s) %v, want %d -- the same tenant holds more on its other document",
				c.label, len(got.Pages), rvdPageNumbers(got.Pages), c.wantPages)
		}
		if len(got.Fields) != c.wantFields {
			t.Errorf("%s carried %d field(s) %v, want %d -- the same tenant holds more on its other job",
				c.label, len(got.Fields), rvdFieldNames(got.Fields), c.wantFields)
		}
	}

	for _, c := range []struct {
		label string
		ctx   context.Context
		job   string
	}{
		{"A reading B's job", ctxA, jobB},
		{"B reading A's job", ctxB, jobA},
	} {
		got, err := r.Detail(c.ctx, c.job)
		if !errors.Is(err, extraction.ErrNotFound) {
			t.Errorf("%s (%s) returned %v, want %v", c.label, c.job, err, extraction.ErrNotFound)
		}
		if got.ID != "" || got.DocumentID != "" || got.State != "" || got.Document != (extraction.ExtractionDocument{}) {
			t.Errorf("%s carried %+v, want the zero values", c.label, got)
		}
		if got.Pages == nil || got.Fields == nil {
			t.Errorf("%s returned Pages=%v Fields=%v; a nil slice marshals to JSON null", c.label, got.Pages, got.Fields)
		}
		if len(got.Pages) != 0 || len(got.Fields) != 0 {
			t.Errorf("%s carried %d page(s) %v and %d field(s) %v, want none",
				c.label, len(got.Pages), rvdPageNumbers(got.Pages), len(got.Fields), rvdFieldNames(got.Fields))
		}
	}
}

// AC 7's other edge: extraction_field_results_bbox_normalised compares with <=, so a
// zero-area box is a legal stored region. page IS NOT NULL is the whole rule -- area is not
// part of it, and a reader that dropped an empty box would lose a real anchor.
func TestRLS_ExtractionDetailKeepsADegenerateRegion(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	want := map[string]extraction.ExtractionRegion{
		"zero_area":   {Page: 1, X0: 0, Y0: 0, X1: 0, Y1: 0},
		"zero_height": {Page: 1, X0: 0.25, Y0: 0.5, X1: 0.75, Y1: 0.5},
		"whole_page":  {Page: 2, X0: 0, Y0: 0, X1: 1, Y1: 1},
	}
	for i, name := range []string{"zero_area", "zero_height", "whole_page"} {
		w := want[name]
		rvdSeedField(t, ctx, tenantA, jobA, name, rvdStr("v"),
			&rvdBox{Page: w.Page, X0: w.X0, Y0: w.Y0, X1: w.X1, Y1: w.Y1}, 0, nil,
			now.Add(time.Duration(i)*time.Millisecond))
	}

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	if len(got.Fields) != len(want) {
		t.Fatalf("got %d field(s) %v, want %d -- every assertion below is vacuous over a short list",
			len(got.Fields), rvdFieldNames(got.Fields), len(want))
	}
	for _, f := range got.Fields {
		w, ok := want[f.Name]
		if !ok {
			t.Errorf("unexpected field %q", f.Name)
			continue
		}
		if f.Region == nil {
			t.Errorf("field %q came back with a nil region; page IS NOT NULL is the whole rule, area is not part of it", f.Name)
			continue
		}
		if *f.Region != w {
			t.Errorf("field %q came back with region %+v, want %+v", f.Name, *f.Region, w)
		}
	}
}

// EXTR-12-02, the leak-proof half, deliberately rewritten: alternatives are ON this wire now,
// so the old absence scan asserted the opposite of what ships. What it proved survives -- the
// decided reading still has a NULL value and NO box while its alternatives carry both, so a
// reader that confused rank 1 for rank 0 still changes WHAT reaches the wire, not how much --
// and the absence scan moves to where the alternatives may not appear: any other field, and
// any top-level position.
func TestRLS_ExtractionDetailAlternativesNestOnlyUnderTheirOwnField(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	altBox := &rvdBox{Page: 3, X0: 0.11, Y0: 0.22, X1: 0.33, Y1: 0.44}
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", nil, nil, 0, rvdStr("ambiguous"), now)
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("ALT-RANK-1"), altBox, 1, nil, now)
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("ALT-RANK-2"), altBox, 2, nil, now)
	// The neighbour is what makes "under their own field" testable: with one field, "nowhere
	// else" and "nowhere" are the same assertion.
	rvdSeedField(t, ctx, tenantA, jobA, "supplier_tin", rvdStr("DECIDED-TIN"), nil, 0, nil, now.Add(time.Millisecond))

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	if len(got.Fields) != 2 {
		t.Fatalf("got %d field(s) %v, want exactly the two rank-0 readings", len(got.Fields), rvdFieldNames(got.Fields))
	}

	f := got.Fields[0]
	if f.Name != "invoice_number" {
		t.Fatalf("the first reading came back as %q, want invoice_number", f.Name)
	}
	if f.Value != nil {
		t.Errorf("value came back %q; rank 0 stored NULL, so this is an alternative's value", *f.Value)
	}
	if f.Region != nil {
		t.Errorf("region came back %+v; rank 0 stored no box, so this is an alternative's region", *f.Region)
	}

	wantRegion := extraction.ExtractionRegion{Page: 3, X0: 0.11, Y0: 0.22, X1: 0.33, Y1: 0.44}
	if len(f.Alternatives) != 2 {
		t.Fatalf("invoice_number came back with %d alternative(s), want the rank-1 and rank-2 rows nested under it",
			len(f.Alternatives))
	}
	for i, wantValue := range []string{"ALT-RANK-1", "ALT-RANK-2"} {
		alt := f.Alternatives[i]
		if alt.Value == nil || *alt.Value != wantValue {
			t.Errorf("alternative at index %d came back with value %v, want %q in rank order", i, alt.Value, wantValue)
		}
		if alt.Region == nil || *alt.Region != wantRegion {
			t.Errorf("alternative at index %d came back with region %v, want %+v", i, alt.Region, wantRegion)
		}
	}
	if n := len(got.Fields[1].Alternatives); n != 0 {
		t.Errorf("supplier_tin came back with %d alternative(s) %+v; its rows are rank 0 only",
			n, got.Fields[1].Alternatives)
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal the detail: %v", err)
	}
	// Each needle appears exactly as often as its own rows: twice for the shared box, once per
	// alternative value. More than that is a copy under some other key.
	for needle, want := range map[string]int{
		"ALT-RANK-1": 1, "ALT-RANK-2": 1, "DECIDED-TIN": 1,
		`"x0":0.11`: 2, `"y0":0.22`: 2, `"x1":0.33`: 2, `"y1":0.44`: 2,
	} {
		if n := strings.Count(string(b), needle); n != want {
			t.Errorf("the detail marshals to\n  %s\nwith %s appearing %d time(s), want %d", b, needle, n, want)
		}
	}

	// Blank every alternatives slice and the needles must vanish entirely: that is what makes
	// "only inside their own field's alternatives" an assertion rather than a count.
	stripped := extraction.ExtractionDetail{
		ID: got.ID, DocumentID: got.DocumentID, State: got.State,
		Document: got.Document, Pages: got.Pages,
		Fields: make([]extraction.ExtractionFieldState, 0, len(got.Fields)),
	}
	for _, sf := range got.Fields {
		sf.Alternatives = []extraction.ExtractionCandidate{}
		stripped.Fields = append(stripped.Fields, sf)
	}
	sb, err := json.Marshal(stripped)
	if err != nil {
		t.Fatalf("marshal the stripped detail: %v", err)
	}
	for _, needle := range []string{"ALT-RANK-1", "ALT-RANK-2", "0.11", "0.22", "0.33", "0.44"} {
		if strings.Contains(string(sb), needle) {
			t.Errorf("with every alternatives slice emptied the detail still marshals to\n  %s\nwhich carries %s -- a candidate_rank > 0 row reached the wire somewhere else",
				sb, needle)
		}
	}
	// Control: the stripped form is still the same document, so the six absences mean something.
	for _, needle := range []string{`"invoice_number"`, "DECIDED-TIN"} {
		if !strings.Contains(string(sb), needle) {
			t.Errorf("the stripped detail marshals to\n  %s\nwithout %s, so the needle scan above proved nothing", sb, needle)
		}
	}
}

// EXTR-12-02 AC-4: a rank>0 row whose field_name has no rank-0 sibling is dropped, never
// promoted to a field. supplier_tin is the positive control -- without it "invoice_number is
// absent" is also what a job with no rows at all returns.
func TestExtractionDetail_OrphanAlternativeIsDropped(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	rvdSeedField(t, ctx, tenantA, jobA, "supplier_tin", rvdStr("DECIDED-TIN"), nil, 0, nil, now)
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("ORPHAN-1"),
		&rvdBox{Page: 2, X0: 0.61, Y0: 0.62, X1: 0.63, Y1: 0.64}, 1, nil, now.Add(time.Millisecond))
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("ORPHAN-2"), nil, 2, nil, now.Add(2*time.Millisecond))

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	if len(got.Fields) != 1 {
		t.Fatalf("got %d field(s) %v, want only supplier_tin -- an orphan alternative is not a field",
			len(got.Fields), rvdFieldNames(got.Fields))
	}
	if got.Fields[0].Name != "supplier_tin" {
		t.Fatalf("the one reading came back as %q, want supplier_tin", got.Fields[0].Name)
	}
	if n := len(got.Fields[0].Alternatives); n != 0 {
		t.Errorf("supplier_tin came back with %d alternative(s) %+v; the orphans belong to another field name",
			n, got.Fields[0].Alternatives)
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal the detail: %v", err)
	}
	for _, needle := range []string{"invoice_number", "ORPHAN-1", "ORPHAN-2", "0.61", "0.62", "0.63", "0.64"} {
		if strings.Contains(string(b), needle) {
			t.Errorf("the detail marshals to\n  %s\nwhich carries %s from an orphan candidate_rank > 0 row", b, needle)
		}
	}
	// Control: the marshalled form is searchable, so the absences above mean something.
	if !strings.Contains(string(b), "DECIDED-TIN") {
		t.Errorf("the detail marshals to\n  %s\nwithout the control value, so the needle scan above proved nothing", b)
	}
}

// EXTR-12-02 AC-5: another tenant's alternatives are as unreachable as its decided readings.
// The positive control is load-bearing -- a reader that returned no alternative to anyone would
// pass the refusal alone, which is exactly the shape a cross-tenant test tends to fail at.
func TestRLS_ExtractionDetailCrossTenantAlternativesAreInvisible(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, _, _ := rdTenant(t, ctx, "active")
	ctxB, tenantB, docB := rdTenant(t, ctx, "active")

	now := time.Now().UTC().Truncate(time.Microsecond)
	jobB := rdSeedJob(t, ctx, tenantB, docB, "succeeded", now, nil)

	altBox := &rvdBox{Page: 1, X0: 0.71, Y0: 0.72, X1: 0.73, Y1: 0.74}
	rvdSeedField(t, ctx, tenantB, jobB, "invoice_number", rvdStr("B-DECIDED"), nil, 0, rvdStr("ambiguous"), now)
	rvdSeedField(t, ctx, tenantB, jobB, "invoice_number", rvdStr("B-ALT-1"), altBox, 1, nil, now)
	rvdSeedField(t, ctx, tenantB, jobB, "invoice_number", rvdStr("B-ALT-2"), altBox, 2, nil, now)

	// Control: B's own alternatives are readable, so A's empty answer is isolation and not an
	// empty fixture.
	ownGot, err := r.Detail(ctxB, jobB)
	if err != nil {
		t.Fatalf("B reading its own job %s: %v", jobB, err)
	}
	if len(ownGot.Fields) != 1 || len(ownGot.Fields[0].Alternatives) != 2 {
		t.Fatalf("B's own read gave %d field(s) with %v alternative(s); the fixture must hold alternatives or the refusal below proves nothing",
			len(ownGot.Fields), rvdAlternativeCounts(ownGot.Fields))
	}

	gotForeign, errForeign := r.Detail(ctxA, jobB)
	gotAbsent, errAbsent := r.Detail(ctxA, uuid.NewString())
	if !errors.Is(errForeign, extraction.ErrNotFound) {
		t.Fatalf("A reading B's job returned %v, want ErrNotFound", errForeign)
	}
	if errAbsent.Error() != errForeign.Error() {
		t.Errorf("the two refusals read differently:\n  absent:  %s\n  foreign: %s", errAbsent, errForeign)
	}
	if !reflect.DeepEqual(gotForeign, gotAbsent) {
		t.Errorf("A got %+v for B's job and %+v for an absent one; the two answers must be one value", gotForeign, gotAbsent)
	}

	b, err := json.Marshal(gotForeign)
	if err != nil {
		t.Fatalf("marshal the refused detail: %v", err)
	}
	for _, needle := range []string{"B-DECIDED", "B-ALT-1", "B-ALT-2", "0.71", "0.72", "0.73", "0.74"} {
		if strings.Contains(string(b), needle) {
			t.Errorf("A's refused read marshals to\n  %s\nwhich carries %s from tenant B", b, needle)
		}
	}
}

// rvdAlternativeCounts reports the per-field alternative counts, so a fixture failure names
// what it got instead of a bare number.
func rvdAlternativeCounts(fields []extraction.ExtractionFieldState) []int {
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		out = append(out, len(f.Alternatives))
	}
	return out
}

// page_number is an int column, so ORDER BY is numeric. The AC suite stops at three pages,
// where lexicographic and numeric order agree; a two-digit page is where they part.
func TestRLS_ExtractionDetailOrdersPagesNumericallyNotLexicographically(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", time.Now().UTC(), nil)

	// Inserted out of order, and 10/11 sort before 2 as text.
	for _, page := range []int{11, 2, 10, 1, 3} {
		rvdSeedPage(t, ctx, tenantA, docA, page, 600, 800+page)
	}

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	if len(got.Pages) != 5 {
		t.Fatalf("got %d page(s) %v, want 5", len(got.Pages), rvdPageNumbers(got.Pages))
	}
	if nums := rvdPageNumbers(got.Pages); !reflect.DeepEqual(nums, []int{1, 2, 3, 10, 11}) {
		t.Errorf("pages came back in order %v, want [1 2 3 10 11]; [1 10 11 2 3] is a text sort", nums)
	}
	// Heights differ per page, so a row paired with the wrong number is visible too.
	for _, p := range got.Pages {
		if p.HeightPx != 800+p.Page {
			t.Errorf("page %d carried height %d, want %d -- the row is paired with the wrong page number",
				p.Page, p.HeightPx, 800+p.Page)
		}
	}
}

// StoredAt is text on the wire, formatted in Go. pgx scans timestamptz into time.Local, so
// without the UTC normalisation the same row renders +03:00 on a developer's machine and Z in
// CI. TestExtractionDetail_CarriesTheDocumentMetadata compares instants and cannot see that.
func TestRLS_ExtractionDetailStoredAtIsUTCRegardlessOfProcessZone(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, _ := rdTenant(t, ctx, "active")
	wat := time.FixedZone("WAT", 1*3600)

	cases := []struct {
		label  string
		stored time.Time
		want   string
	}{
		{"whole second in +01:00", time.Date(2026, 8, 30, 11, 42, 7, 0, wat), "2026-08-30T10:42:07Z"},
		{"microseconds", time.Date(2026, 8, 30, 11, 42, 7, 123456000, wat), "2026-08-30T10:42:07.123456Z"},
	}
	jobs := make([]string, len(cases))
	for i, c := range cases {
		doc := rvdSeedDocumentMeta(t, ctx, tenantA, nil, nil, 1024, strings.Repeat("f", 63)+string(rune('0'+i)), c.stored)
		jobs[i] = rdSeedJob(t, ctx, tenantA, doc, "succeeded", time.Now().UTC(), nil)
	}

	// A process zone that is not UTC and not the host's, restored on the way out. Every test
	// in this package runs sequentially, so no other case observes it.
	saved := time.Local
	t.Cleanup(func() { time.Local = saved })
	time.Local = time.FixedZone("QA-0530", -(5*3600 + 1800))

	for i, c := range cases {
		got, err := r.Detail(ctxA, jobs[i])
		if err != nil {
			t.Fatalf("Detail for job %s (%s): %v", jobs[i], c.label, err)
		}
		if got.Document.StoredAt != c.want {
			t.Errorf("%s: stored_at came back %q, want %q", c.label, got.Document.StoredAt, c.want)
		}
		at, err := time.Parse(time.RFC3339, got.Document.StoredAt)
		if err != nil {
			t.Errorf("%s: stored_at %q does not parse as RFC3339: %v", c.label, got.Document.StoredAt, err)
		} else if !at.Equal(c.stored) {
			t.Errorf("%s: stored_at is %s, want the instant %s", c.label, at.UTC(), c.stored.UTC())
		}

		// Same row, same string: the value is a property of the row and not of the read.
		again, err := r.Detail(ctxA, jobs[i])
		if err != nil {
			t.Fatalf("second Detail for job %s: %v", jobs[i], err)
		}
		if again.Document.StoredAt != got.Document.StoredAt {
			t.Errorf("%s: two reads of one row gave %q and %q", c.label, got.Document.StoredAt, again.Document.StoredAt)
		}
	}
}

// AC 8 to the letter: "both return exactly it". The AC suite normalises the caller's own id
// out of the two messages before comparing, which admits a wrapped form whose text differs
// between the two answers. Nothing today needs that latitude, so the two refusals are pinned
// as identical values -- same string, same unwrap depth, same marshalled body.
func TestRLS_ExtractionDetailRefusalsCarryNoDistinguisher(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	_, tenantB, docB := rdTenant(t, ctx, "active")

	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	jobB := rdSeedJob(t, ctx, tenantB, docB, "succeeded", now, nil)
	rvdSeedPage(t, ctx, tenantB, docB, 1, 900, 1200)
	rvdSeedField(t, ctx, tenantB, jobB, "invoice_number", rvdStr("B-0001"), nil, 0, nil, now)

	// Control: A can read something, so the refusals below are refusals.
	if _, err := r.Detail(ctxA, jobA); err != nil {
		t.Fatalf("A reading its own job %s: %v", jobA, err)
	}

	gotAbsent, errAbsent := r.Detail(ctxA, uuid.NewString())
	gotForeign, errForeign := r.Detail(ctxA, jobB)
	if errAbsent == nil || errForeign == nil {
		t.Fatalf("absent returned %v and foreign returned %v; both must refuse", errAbsent, errForeign)
	}

	if errAbsent.Error() != errForeign.Error() {
		t.Errorf("the two refusals read differently:\n  absent:  %s\n  foreign: %s", errAbsent, errForeign)
	}
	// Unwrap depth is its own channel: two errors can print alike and still differ in shape.
	var depthAbsent, depthForeign int
	for e := errAbsent; e != nil; e = errors.Unwrap(e) {
		depthAbsent++
	}
	for e := errForeign; e != nil; e = errors.Unwrap(e) {
		depthForeign++
	}
	if depthAbsent != depthForeign {
		t.Errorf("the absent refusal unwraps %d deep and the foreign one %d; the shape tells them apart",
			depthAbsent, depthForeign)
	}

	absentJSON, err := json.Marshal(gotAbsent)
	if err != nil {
		t.Fatalf("marshal the absent-job detail: %v", err)
	}
	foreignJSON, err := json.Marshal(gotForeign)
	if err != nil {
		t.Fatalf("marshal the foreign-job detail: %v", err)
	}
	if string(absentJSON) != string(foreignJSON) {
		t.Errorf("the two refusals marshal differently:\n  absent:  %s\n  foreign: %s", absentJSON, foreignJSON)
	}
	// Control: the marshalled body is the shape EXTR-11-02 would send, not an empty string.
	if !strings.Contains(string(absentJSON), `"pages":[]`) {
		t.Errorf("the refused detail marshals to %s, which is not the wire shape the assertions above compare", absentJSON)
	}
}

// TestExtractionWireStructs_CarryJsonTags hand-copies goStructKeys out of the SPA suite. A
// copy is only an oracle while it matches the original, and the original lives in a file no
// Go test compiles.
func TestExtractionWireStructs_MirrorTheShippedExtractor(t *testing.T) {
	src, err := os.ReadFile(rqaWireMirrorsSource)
	if err != nil {
		t.Fatalf("read %s: %v", rqaWireMirrorsSource, err)
	}
	if !strings.Contains(string(src), "function goStructKeys(") {
		t.Fatalf("%s declares no goStructKeys; the copy in TestExtractionWireStructs_CarryJsonTags now mirrors nothing",
			rqaWireMirrorsSource)
	}
	for _, needle := range []string{
		`type\\s+${structName}\\s+struct\\s*\\{([^{}]*)\\}`,
		"/`json:\"([^\"]+)\"`/g",
	} {
		if !strings.Contains(string(src), needle) {
			t.Errorf("%s no longer holds %s; TestExtractionWireStructs_CarryJsonTags asserts against a stale copy of it",
				rqaWireMirrorsSource, needle)
		}
	}
	// The Go copy is read off the compiled regex rather than retyped, so the two move together.
	if got := rvdJSONTag.String(); !strings.Contains(string(src), got) {
		t.Errorf("rvdJSONTag is %s, which %s does not contain", got, rqaWireMirrorsSource)
	}
}
