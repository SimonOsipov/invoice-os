// reader_detail_db_test.go: EXTR-11-01's suite for (*Reader).Detail and the five tagged wire
// structs. Shares store_db_test.go's harness and reader_db_test.go's rdTenant/rdSeedJob
// fixtures, and seeds every row as the superuser for the same reason they do.
package extraction_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// rvdWireStructs is the wire contract EXTR-11-02's handler and the SPA/e2e TypeScript mirrors
// are written against. Key sets are pinned, not counted: a renamed key is the defect.
var rvdWireStructs = []struct {
	name string
	keys []string
}{
	{"ExtractionRegion", []string{"page", "x0", "y0", "x1", "y1"}},
	{"ExtractionPage", []string{"page", "width_px", "height_px"}},
	{"ExtractionFieldState", []string{"name", "value", "region"}},
	{"ExtractionDocument", []string{"filename", "content_type", "size_bytes", "stored_at"}},
	{"ExtractionDetail", []string{"id", "document_id", "state", "document", "pages", "fields"}},
}

// rvdSnakeCase is the tag shape every other wire struct in this repo uses.
var rvdSnakeCase = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// rvdJSONTag matches one struct tag the way wireMirrors.test.ts:27 does.
var rvdJSONTag = regexp.MustCompile("`json:\"([^\"]+)\"`")

func rvdStr(s string) *string { return &s }

// rvdPageKey is the shape extraction_page_images_key_tenant_scoped admits: the CHECK compares
// bytes against 'tenants/' || tenant_id.
func rvdPageKey(tenantID string, page int) string {
	return fmt.Sprintf("tenants/%s/pages/%s/v1/p%04d.png", tenantID, strings.Repeat("c", 64), page)
}

func rvdSeedPage(t *testing.T, ctx context.Context, tenantID, documentID string, page, widthPx, heightPx int) {
	t.Helper()
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO extraction_page_images
		     (tenant_id, document_id, page_number, width_px, height_px, storage_key)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		tenantID, documentID, page, widthPx, heightPx, rvdPageKey(tenantID, page),
	); err != nil {
		t.Fatalf("seed page image %d for document %s: %v", page, documentID, err)
	}
}

// rvdBox is the five all-or-none box columns extraction_field_results_region_complete governs.
type rvdBox struct {
	Page           int
	X0, Y0, X1, Y1 float64
}

// rvdSeedField writes one extraction_field_results row. created_at is named explicitly: the
// column default now() is the TRANSACTION timestamp, so co-seeded rows would tie and the
// ordering the read path relies on could not be observed.
func rvdSeedField(t *testing.T, ctx context.Context, tenantID, jobID, name string, value *string, box *rvdBox, rank int, createdAt time.Time) {
	t.Helper()

	var (
		page           *int
		x0, y0, x1, y1 *float64
	)
	if box != nil {
		page, x0, y0, x1, y1 = &box.Page, &box.X0, &box.Y0, &box.X1, &box.Y1
	}

	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO extraction_field_results
		     (tenant_id, extraction_job_id, field_name, value, page,
		      bbox_x0, bbox_y0, bbox_x1, bbox_y1, candidate_rank, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		tenantID, jobID, name, value, page, x0, y0, x1, y1, rank, createdAt,
	); err != nil {
		t.Fatalf("seed field result %q rank %d for job %s: %v", name, rank, jobID, err)
	}
}

// rvdSeedDocumentMeta seeds a document with the four values the toolbar meta line renders.
// stTenant's own document leaves filename and declared_content_type NULL, which is the other
// half of TestExtractionDetail_CarriesTheDocumentMetadata.
func rvdSeedDocumentMeta(t *testing.T, ctx context.Context, tenantID string, filename, contentType *string, sizeBytes int64, hash string, createdAt time.Time) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO documents
		     (id, tenant_id, storage_key, content_hash, size_bytes, filename, declared_content_type, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, tenantID, "extr-11/"+id, hash, sizeBytes, filename, contentType, createdAt,
	); err != nil {
		t.Fatalf("seed document for tenant %s: %v", tenantID, err)
	}
	return id
}

// rvdMethod returns the named method of reader.go, or fails: every source scan below is an
// absence proof, and an absence proved over a missing function proves nothing.
func rvdMethod(t *testing.T, f *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv != nil && fn.Name.Name == name && fn.Body != nil {
			return fn
		}
	}
	t.Fatalf("%s declares no method %s, so the scan below would examine nothing", rdReaderSource, name)
	return nil
}

func rvdFieldNames(fields []extraction.ExtractionFieldState) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.Name)
	}
	return out
}

func rvdPageNumbers(pages []extraction.ExtractionPage) []int {
	out := make([]int, 0, len(pages))
	for _, p := range pages {
		out = append(out, p.Page)
	}
	return out
}

// AC 8, refusal half, plus the collection scoping for the two child tables. The positive
// control is load-bearing: a Detail that refused every read would pass the refusal alone.
func TestRLS_ExtractionDetailCrossTenantReadRefused(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	_, tenantB, docB := rdTenant(t, ctx, "active")

	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	jobB := rdSeedJob(t, ctx, tenantB, docB, "succeeded", now, nil)

	rvdSeedPage(t, ctx, tenantA, docA, 1, 1275, 1651)
	rvdSeedPage(t, ctx, tenantA, docA, 2, 1275, 1651)
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("A-0001"), nil, 0, now)

	// A's own second document and second job: the counts below are per-document and per-job,
	// not per-tenant, and RLS cannot tell those apart.
	docA2 := rvdSeedDocumentMeta(t, ctx, tenantA, nil, nil, 2048, strings.Repeat("e", 64), now)
	jobA2 := rdSeedJob(t, ctx, tenantA, docA2, "succeeded", now, nil)
	for page := 1; page <= 4; page++ {
		rvdSeedPage(t, ctx, tenantA, docA2, page, 600, 800)
	}
	for _, name := range []string{"issue_date", "currency", "total_amount"} {
		rvdSeedField(t, ctx, tenantA, jobA2, name, rvdStr("A2"), nil, 0, now)
	}

	// B's rows are what make A's counts mean something: without them "2 pages, 1 field" is
	// also what an empty database returns.
	rvdSeedPage(t, ctx, tenantB, docB, 1, 900, 1200)
	rvdSeedPage(t, ctx, tenantB, docB, 2, 900, 1200)
	rvdSeedPage(t, ctx, tenantB, docB, 3, 900, 1200)
	rvdSeedField(t, ctx, tenantB, jobB, "invoice_number", rvdStr("B-0001"), nil, 0, now)
	rvdSeedField(t, ctx, tenantB, jobB, "supplier_tin", rvdStr("B-TIN"), nil, 0, now)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("A reading its own job %s: %v", jobA, err)
	}
	if got.ID != jobA || got.DocumentID != docA {
		t.Fatalf("A reading its own job got id %q / document %q, want %q / %q", got.ID, got.DocumentID, jobA, docA)
	}
	if got.State != "succeeded" {
		t.Errorf("state came back %q, want the column value succeeded verbatim", got.State)
	}
	if len(got.Pages) != 2 {
		t.Errorf("job %s carried %d page(s) %v, want 2 -- document %s holds 4 more and B's document %s holds 3",
			jobA, len(got.Pages), rvdPageNumbers(got.Pages), docA2, docB)
	}
	if len(got.Fields) != 1 {
		t.Errorf("job %s carried %d field(s) %v, want 1 -- job %s holds 3 more and B's job %s holds 2",
			jobA, len(got.Fields), rvdFieldNames(got.Fields), jobA2, jobB)
	}

	got, err = r.Detail(ctxA, jobB)
	if !errors.Is(err, extraction.ErrNotFound) {
		t.Errorf("A reading B's job %s returned %v, want %v", jobB, err, extraction.ErrNotFound)
	}
	if got.ID != "" || got.DocumentID != "" || got.State != "" {
		t.Errorf("the refused read carried id %q / document %q / state %q, want the zero values", got.ID, got.DocumentID, got.State)
	}
	if got.Pages == nil || got.Fields == nil {
		t.Errorf("the refused read returned Pages=%v Fields=%v; a nil slice marshals to JSON null", got.Pages, got.Fields)
	}
	if len(got.Pages) != 0 || len(got.Fields) != 0 {
		t.Errorf("the refused read carried %d page(s) %v and %d field(s) %v, want none",
			len(got.Pages), rvdPageNumbers(got.Pages), len(got.Fields), rvdFieldNames(got.Fields))
	}
}

// AC 3: one request transaction covers all three statements. The mirror image of
// TestExtractionStore_UsesTenantTxNotRequestTx; the bare form takes a tenant id by argument,
// which no browser caller may supply.
func TestRLS_ExtractionDetailUsesRequestTxNotTenantTx(t *testing.T) {
	f, fset := mxParse(t, rdReaderSource)
	fn := rvdMethod(t, f, "Detail")

	var requestTx int
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		// Prefix, not exact name: WithinRequestTenantTxOpts is the twin an exact match misses.
		if strings.HasPrefix(id.Name, "WithinRequestTenantTx") {
			requestTx++
			return true
		}
		if strings.HasPrefix(id.Name, "WithinTenantTx") {
			t.Errorf("%s: Detail names %s; the request seam takes its tenant from the verified Identity in ctx",
				fset.Position(id.Pos()), id.Name)
		}
		return true
	})
	// Doubles as this scan's control needle: zero hits and the absence above proved nothing.
	if requestTx != 1 {
		t.Errorf("Detail names WithinRequestTenantTx %d time(s), want exactly 1 -- all three statements share one transaction", requestTx)
	}
}

// AC 5: the empty case. A nil slice marshals to JSON null and every consumer loops over these.
func TestExtractionDetail_PagesAndFieldsAreNeverNil(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	jobA := rdSeedJob(t, ctx, tenantA, docA, "queued", time.Now().UTC(), nil)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s with no page and no field rows: %v", jobA, err)
	}
	if got.Pages == nil {
		t.Error("Pages came back nil, want []ExtractionPage{}")
	}
	if got.Fields == nil {
		t.Error("Fields came back nil, want []ExtractionFieldState{}")
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal the detail: %v", err)
	}
	for _, want := range []string{`"pages":[]`, `"fields":[]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the detail marshals to\n  %s\nwhich does not carry %s", b, want)
		}
	}

	// The control: the zero value carries the very shape the reader must never return, so the
	// two Contains checks above can tell [] from null.
	zero, err := json.Marshal(extraction.ExtractionDetail{})
	if err != nil {
		t.Fatalf("marshal a zero ExtractionDetail: %v", err)
	}
	for _, bad := range []string{`"pages":null`, `"fields":null`} {
		if !strings.Contains(string(zero), bad) {
			t.Errorf("a zero ExtractionDetail marshals to\n  %s\nwhich does not carry %s, so the assertions above cannot tell [] from null", zero, bad)
		}
	}
}

// AC 2 read-path half, page inventory: rows arrive in page order regardless of insert order.
func TestExtractionDetail_PagesOrderedByPageNumber(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", time.Now().UTC(), nil)

	// Inserted 3, 1, 2 on purpose: physical order must not decide the answer.
	rvdSeedPage(t, ctx, tenantA, docA, 3, 1275, 1653)
	rvdSeedPage(t, ctx, tenantA, docA, 1, 1275, 1651)
	rvdSeedPage(t, ctx, tenantA, docA, 2, 1275, 1652)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	if len(got.Pages) != 3 {
		t.Fatalf("got %d page(s) %v, want 3 -- every assertion below is vacuous over a short list", len(got.Pages), rvdPageNumbers(got.Pages))
	}
	if nums := rvdPageNumbers(got.Pages); !reflect.DeepEqual(nums, []int{1, 2, 3}) {
		t.Errorf("pages came back in order %v, want [1 2 3]", nums)
	}
	// The stored grid, not a recomputed one: pdfium.go:154-159 records why it must never be
	// derived. Heights differ per page so a swapped row is visible.
	for i, want := range []extraction.ExtractionPage{
		{Page: 1, WidthPx: 1275, HeightPx: 1651},
		{Page: 2, WidthPx: 1275, HeightPx: 1652},
		{Page: 3, WidthPx: 1275, HeightPx: 1653},
	} {
		if got.Pages[i] != want {
			t.Errorf("page at index %d = %+v, want %+v", i, got.Pages[i], want)
		}
	}
}

// AC 6: candidate_rank = 0 is the decided reading; 1..N are alternatives and are not on this
// wire.
func TestExtractionDetail_ExcludesAlternativeCandidates(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("DECIDED-0"), nil, 0, now)
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("ALTERNATIVE-1"), nil, 1, now)
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("ALTERNATIVE-2"), nil, 2, now)
	rvdSeedField(t, ctx, tenantA, jobA, "supplier_tin", rvdStr("DECIDED-TIN"), nil, 0, now.Add(time.Millisecond))

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	if len(got.Fields) != 2 {
		t.Fatalf("got %d field(s) %v, want exactly 2 rank-0 readings", len(got.Fields), rvdFieldNames(got.Fields))
	}
	for _, f := range got.Fields {
		if f.Value == nil {
			t.Fatalf("field %q came back with a nil value; both fixtures wrote one", f.Name)
		}
		if strings.HasPrefix(*f.Value, "ALTERNATIVE-") {
			t.Errorf("field %q carries %q, which is a candidate_rank > 0 row", f.Name, *f.Value)
		}
	}
	byName := map[string]string{}
	for _, f := range got.Fields {
		byName[f.Name] = *f.Value
	}
	if byName["invoice_number"] != "DECIDED-0" {
		t.Errorf("invoice_number came back %q, want the rank-0 value DECIDED-0", byName["invoice_number"])
	}
	if byName["supplier_tin"] != "DECIDED-TIN" {
		t.Errorf("supplier_tin came back %q, want DECIDED-TIN", byName["supplier_tin"])
	}
}

// AC 7: the region is reconstructed only when page IS NOT NULL, mirroring fieldResultsTx's
// all-or-none rule. A value with no box is a normal reading, not an error.
func TestExtractionDetail_NullPageYieldsNullRegion(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	rvdSeedField(t, ctx, tenantA, jobA, "supplier_tin", rvdStr("12345678-0001"), nil, 0, now)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	if len(got.Fields) != 1 {
		t.Fatalf("got %d field(s) %v, want 1", len(got.Fields), rvdFieldNames(got.Fields))
	}
	f := got.Fields[0]
	if f.Region != nil {
		t.Errorf("a row with all five box columns NULL came back with region %+v, want nil", *f.Region)
	}
	if f.Value == nil || *f.Value != "12345678-0001" {
		t.Errorf("value came back %v, want 12345678-0001 -- a missing box must not discard the reading", f.Value)
	}

	b, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal the field: %v", err)
	}
	if !strings.Contains(string(b), `"region":null`) {
		t.Errorf("the field marshals to %s, want a null region", b)
	}
}

// AC 2 read-path half, boxes: normalised [0,1] with a TOP-LEFT origin, exactly as written.
// A Y-flip is the defect this catches (pdfium.go:181-193 is where the flip is applied once).
func TestExtractionDetail_RegionRoundTripsNormalised(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	want := rvdBox{Page: 2, X0: 0.62, Y0: 0.08, X1: 0.90, Y1: 0.13}
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("MOCK-INV-0001"), &want, 0, now)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	if len(got.Fields) != 1 {
		t.Fatalf("got %d field(s) %v, want 1", len(got.Fields), rvdFieldNames(got.Fields))
	}
	region := got.Fields[0].Region
	if region == nil {
		t.Fatal("a row with all five box columns set came back with a nil region")
	}
	if *region != (extraction.ExtractionRegion{Page: want.Page, X0: want.X0, Y0: want.Y0, X1: want.X1, Y1: want.Y1}) {
		t.Errorf("region came back %+v, want %+v", *region, want)
	}
	if region.Y0 >= region.Y1 {
		t.Errorf("region has y0 %v >= y1 %v; the origin is top-left, so a flip inverted the box", region.Y0, region.Y1)
	}
}

// AC 2: the four values the document toolbar renders, from the joined documents row. Both
// arms matter -- a NULL filename must reach the wire as null, not as an empty string.
func TestExtractionDetail_CarriesTheDocumentMetadata(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	// stTenant's own document leaves filename and declared_content_type NULL.
	ctxA, tenantA, bareDoc := rdTenant(t, ctx, "active")
	stored := time.Date(2026, 8, 30, 10, 42, 7, 0, time.UTC)
	fullDoc := rvdSeedDocumentMeta(t, ctx, tenantA,
		rvdStr("sahel-freight-0418.pdf"), rvdStr("application/pdf"), 151552, strings.Repeat("d", 64), stored)

	fullJob := rdSeedJob(t, ctx, tenantA, fullDoc, "succeeded", time.Now().UTC(), nil)
	bareJob := rdSeedJob(t, ctx, tenantA, bareDoc, "succeeded", time.Now().UTC(), nil)

	got, err := r.Detail(ctxA, fullJob)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", fullJob, err)
	}
	if got.Document.Filename == nil || *got.Document.Filename != "sahel-freight-0418.pdf" {
		t.Errorf("filename came back %v, want sahel-freight-0418.pdf", got.Document.Filename)
	}
	if got.Document.ContentType == nil || *got.Document.ContentType != "application/pdf" {
		t.Errorf("content_type came back %v, want application/pdf", got.Document.ContentType)
	}
	if got.Document.SizeBytes != 151552 {
		t.Errorf("size_bytes came back %d, want 151552", got.Document.SizeBytes)
	}
	at, err := time.Parse(time.RFC3339, got.Document.StoredAt)
	if err != nil {
		t.Errorf("stored_at %q does not parse as RFC3339: %v", got.Document.StoredAt, err)
	} else if !at.Equal(stored) {
		t.Errorf("stored_at came back %s, want %s", at.UTC().Format(time.RFC3339Nano), stored.Format(time.RFC3339Nano))
	}

	got, err = r.Detail(ctxA, bareJob)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", bareJob, err)
	}
	if got.Document.Filename != nil {
		t.Errorf("a NULL filename came back as %q, want nil", *got.Document.Filename)
	}
	if got.Document.ContentType != nil {
		t.Errorf("a NULL declared_content_type came back as %q, want nil", *got.Document.ContentType)
	}
	b, err := json.Marshal(got.Document)
	if err != nil {
		t.Fatalf("marshal the document: %v", err)
	}
	for _, wantKey := range []string{`"filename":null`, `"content_type":null`} {
		if !strings.Contains(string(b), wantKey) {
			t.Errorf("the document marshals to %s, which does not carry %s", b, wantKey)
		}
	}
}

// AC 4: no query names tenant_id. A hand-written predicate would make
// TestRLS_ExtractionDetailCrossTenantReadRefused pass whether or not tenant_isolation is
// doing the work, and nothing else in the suite fails when one is added.
//
// Three positive needles before the absence: a scan that finds nothing reads exactly like a
// clean file.
func TestRLS_ExtractionDetailDocumentJoinNamesNoTenantId(t *testing.T) {
	f, fset := mxParse(t, rdReaderSource)

	var (
		sqlLits             int
		joins               int
		sawPages, sawFields bool
	)
	ast.Inspect(f, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		unq, err := strconv.Unquote(bl.Value)
		if err != nil || !strings.Contains(unq, "SELECT") {
			return true
		}
		sqlLits++

		if strings.Contains(unq, "extraction_jobs") && strings.Contains(unq, "documents") {
			joins++
			if !strings.Contains(unq, "JOIN") {
				t.Errorf("%s: the job query reads documents without a JOIN; the meta line must come from one statement",
					fset.Position(bl.Pos()))
			}
		}
		if strings.Contains(unq, "extraction_page_images") {
			sawPages = true
		}
		if strings.Contains(unq, "extraction_field_results") {
			sawFields = true
		}
		if strings.Contains(unq, "tenant_id") {
			t.Errorf("%s: a reader.go query names tenant_id; the tenant_isolation policy is the only predicate",
				fset.Position(bl.Pos()))
		}
		return true
	})

	if sqlLits == 0 {
		t.Fatalf("%s holds no SQL string literal, so the tenant_id absence above proved nothing", rdReaderSource)
	}
	if joins == 0 {
		t.Errorf("%s holds no query naming both extraction_jobs and documents; the toolbar meta line has no source", rdReaderSource)
	}
	if !sawPages {
		t.Errorf("%s holds no query naming extraction_page_images", rdReaderSource)
	}
	if !sawFields {
		t.Errorf("%s holds no query naming extraction_field_results", rdReaderSource)
	}
}

// AC 8: a refused read must not confirm that an id exists, so an absent job and another
// tenant's job are one answer.
func TestExtractionDetail_AbsentJobAndForeignJobAreIndistinguishable(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	_, tenantB, docB := rdTenant(t, ctx, "active")

	now := time.Now().UTC()
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	jobB := rdSeedJob(t, ctx, tenantB, docB, "succeeded", now, nil)
	absent := uuid.NewString()

	// Control: A can read something, so the two refusals below are refusals and not a seam
	// that fails for everyone.
	if _, err := r.Detail(ctxA, jobA); err != nil {
		t.Fatalf("A reading its own job %s: %v", jobA, err)
	}

	gotAbsent, errAbsent := r.Detail(ctxA, absent)
	gotForeign, errForeign := r.Detail(ctxA, jobB)

	if !errors.Is(errAbsent, extraction.ErrNotFound) {
		t.Errorf("an absent job id returned %v, want %v", errAbsent, extraction.ErrNotFound)
	}
	if !errors.Is(errForeign, extraction.ErrNotFound) {
		t.Errorf("another tenant's job id returned %v, want %v", errForeign, extraction.ErrNotFound)
	}
	if errAbsent != nil && errForeign != nil {
		// The caller's own id may appear; anything else that differs is a distinguisher.
		a := strings.ReplaceAll(errAbsent.Error(), absent, "<id>")
		b := strings.ReplaceAll(errForeign.Error(), jobB, "<id>")
		if a != b {
			t.Errorf("the two refusals read differently:\n  absent:  %s\n  foreign: %s", a, b)
		}
	}
	if !reflect.DeepEqual(gotAbsent, gotForeign) {
		t.Errorf("the two refusals returned different values:\n  absent:  %+v\n  foreign: %+v", gotAbsent, gotForeign)
	}
	// AC 5 covers the error paths too.
	if gotAbsent.Pages == nil || gotAbsent.Fields == nil {
		t.Errorf("the absent-job path returned Pages=%v Fields=%v; a nil slice marshals to JSON null", gotAbsent.Pages, gotAbsent.Fields)
	}
	if len(gotAbsent.Pages) != 0 || len(gotAbsent.Fields) != 0 {
		t.Errorf("the absent-job path carried %d page(s) and %d field(s), want none", len(gotAbsent.Pages), len(gotAbsent.Fields))
	}
}

// AC 8, unchanged-behaviour half: the shipped GET /v1/extractions collection route keeps
// answering an unmatched document_id with an empty list, so adding the sentinel changes no
// status code there.
func TestExtractionJobsForDocument_NeverReturnsErrNotFound(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	_, _, docB := rdTenant(t, ctx, "active")
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", time.Now().UTC(), nil)

	// Control: the seam answers at all.
	out, err := r.JobsForDocument(ctxA, docA)
	if err != nil {
		t.Fatalf("A reading its own document %s: %v", docA, err)
	}
	if len(out.Jobs) != 1 || out.Jobs[0].ID != jobA {
		t.Fatalf("A reading its own document got %v, want [%s]", rdIDs(out.Jobs), jobA)
	}

	for _, c := range []struct {
		label      string
		documentID string
	}{
		{"an id no document carries", uuid.NewString()},
		{"another tenant's document", docB},
	} {
		out, err := r.JobsForDocument(ctxA, c.documentID)
		if err != nil {
			t.Errorf("%s returned %v, want a nil error", c.label, err)
		}
		if errors.Is(err, extraction.ErrNotFound) {
			t.Errorf("%s returned ErrNotFound; the collection route must keep answering 200", c.label)
		}
		b, mErr := json.Marshal(out)
		if mErr != nil {
			t.Fatalf("marshal the jobs response: %v", mErr)
		}
		if string(b) != `{"jobs":[]}` {
			t.Errorf("%s marshals to %s, want {\"jobs\":[]}", c.label, b)
		}
	}
}

// AC 1: the wire contract. Two oracles, because either alone passes on a defect the other
// catches -- the AST sees tags a brace-bearing body would hide from wireMirrors.test.ts, and
// the regex is the extractor that actually runs in CI (wireMirrors.test.ts:24-32).
func TestExtractionWireStructs_CarryJsonTags(t *testing.T) {
	f, _ := mxParse(t, rdReaderSource)

	structs := map[string]*ast.StructType{}
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		if st, ok := ts.Type.(*ast.StructType); ok {
			structs[ts.Name.Name] = st
		}
		return true
	})

	var scanned int
	for _, want := range rvdWireStructs {
		st, ok := structs[want.name]
		if !ok {
			t.Errorf("%s declares no struct %s", rdReaderSource, want.name)
			continue
		}
		if st.Fields == nil || len(st.Fields.List) == 0 {
			t.Errorf("%s has no fields, so every tag assertion over it is vacuous", want.name)
			continue
		}
		for _, field := range st.Fields.List {
			if len(field.Names) == 0 {
				t.Errorf("%s carries an embedded field; the wire structs are flat so goStructKeys can read them", want.name)
				continue
			}
			if field.Tag == nil {
				t.Errorf("%s.%s carries no struct tag", want.name, field.Names[0].Name)
				continue
			}
			tag, err := strconv.Unquote(field.Tag.Value)
			if err != nil {
				t.Errorf("%s.%s: cannot unquote the tag %s: %v", want.name, field.Names[0].Name, field.Tag.Value, err)
				continue
			}
			key, _, _ := strings.Cut(reflect.StructTag(tag).Get("json"), ",")
			if !rvdSnakeCase.MatchString(key) {
				t.Errorf("%s.%s has json tag %q, want a snake_case key", want.name, field.Names[0].Name, key)
			}
			scanned++
		}
	}
	if scanned == 0 {
		t.Fatal("the AST scan examined no struct field at all")
	}

	// The second oracle: goStructKeys, verbatim. Its body group is [^{}]*, so a struct body
	// that is not brace-free yields no match and every downstream mirror silently compares
	// [] to [].
	src, err := os.ReadFile(rdReaderSource)
	if err != nil {
		t.Fatalf("read %s: %v", rdReaderSource, err)
	}
	for _, want := range rvdWireStructs {
		body := regexp.MustCompile(`type\s+` + want.name + `\s+struct\s*\{([^{}]*)\}`).FindStringSubmatch(string(src))
		if body == nil {
			t.Errorf("wireMirrors.test.ts's goStructKeys extracts nothing for %s: the struct must exist and its body must be brace-free", want.name)
			continue
		}
		keys := []string{}
		for _, m := range rvdJSONTag.FindAllStringSubmatch(body[1], -1) {
			key, _, _ := strings.Cut(m[1], ",")
			if key != "-" {
				keys = append(keys, key)
			}
		}
		if !reflect.DeepEqual(keys, want.keys) {
			t.Errorf("%s puts %v on the wire, want exactly %v in that order", want.name, keys, want.keys)
		}
	}
}

// AC 9: gofmt rewrites a pair of straight single quotes in a comment into curly quotes, and
// then gofmt -l is clean, so the CI format gate never fires on one. (This comment cannot
// spell the pair it hunts for -- gofmt would rewrite it here too.)
func TestExtractionReader_DocCommentsUseStraightQuotes(t *testing.T) {
	src, err := os.ReadFile(rdReaderSource)
	if err != nil {
		t.Fatalf("read %s: %v", rdReaderSource, err)
	}

	var comments int
	for i, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "//") {
			continue
		}
		comments++
		for _, bad := range []string{"''", "‘", "’", "“", "”"} {
			if strings.Contains(trimmed, bad) {
				t.Errorf("%s:%d: comment carries %q; write \"\" instead", rdReaderSource, i+1, bad)
			}
		}
	}
	if comments == 0 {
		t.Fatalf("%s holds no // comment, so the scan above examined nothing", rdReaderSource)
	}
}
