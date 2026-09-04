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
	"net/http"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// rvdWireStructs is the wire contract this package's handlers and the SPA/e2e TypeScript
// mirrors are written against. Key sets are pinned, not counted: a renamed key is the defect.
// src is per row, so a wire struct living outside reader.go is scanned rather than silently
// uncovered.
var rvdWireStructs = []struct {
	src  string
	name string
	keys []string
}{
	{rdReaderSource, "ExtractionRegion", []string{"page", "x0", "y0", "x1", "y1"}},
	{rdReaderSource, "ExtractionPage", []string{"page", "width_px", "height_px"}},
	{rdReaderSource, "ExtractionCandidate", []string{"value", "region"}},
	{rdReaderSource, "ExtractionCorrected", []string{"method", "was", "where"}},
	{rdReaderSource, "ExtractionFieldState", []string{"name", "value", "region", "reason", "alternatives", "corrected"}},
	{rdReaderSource, "ExtractionDocument", []string{"filename", "content_type", "size_bytes", "stored_at"}},
	// EXTR-15-01 FK-8: failure_kind is pinned immediately after state — the two scalars a
	// reader consults together — because this list compares declaration ORDER, not a set.
	{rdReaderSource, "ExtractionDetail", []string{"id", "document_id", "state", "failure_kind", "document", "pages", "fields"}},
	{rdCorrectionSource, "CorrectionResponse", []string{"id", "field_name", "value", "method", "region", "invoice_id", "created_at"}},
}

// rvdSources is every file rvdWireStructs names, in first-seen order.
func rvdSources() []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range rvdWireStructs {
		if !seen[w.src] {
			seen[w.src] = true
			out = append(out, w.src)
		}
	}
	return out
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
func rvdSeedField(t *testing.T, ctx context.Context, tenantID, jobID, name string, value *string, box *rvdBox, rank int, reason *string, createdAt time.Time) {
	t.Helper()
	rvdSeedFieldID(t, ctx, tenantID, jobID, uuid.NewString(), name, value, box, rank, reason, createdAt)
}

// rvdOrderedIDs returns n uuids sharing one random prefix and differing only in their last four
// hex digits, so their bytewise order is the order returned. Random-prefixed because the dev DB
// keeps rows between runs and id is the primary key.
func rvdOrderedIDs(n int) []string {
	base := uuid.NewString()
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, fmt.Sprintf("%s%04x", base[:len(base)-4], i))
	}
	return out
}

// rvdSeedFieldID is rvdSeedField with the primary key named, so a test can make id order
// disagree with candidate_rank order (TestExtractionDetail_AlternativeOrderSurvivesACreatedAtTie).
func rvdSeedFieldID(t *testing.T, ctx context.Context, tenantID, jobID, id, name string, value *string, box *rvdBox, rank int, reason *string, createdAt time.Time) {
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
		     (id, tenant_id, extraction_job_id, field_name, value, page,
		      bbox_x0, bbox_y0, bbox_x1, bbox_y1, candidate_rank, reason_code, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		id, tenantID, jobID, name, value, page, x0, y0, x1, y1, rank, reason, createdAt,
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
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("A-0001"), nil, 0, nil, now)

	// A's own second document and second job: the counts below are per-document and per-job,
	// not per-tenant, and RLS cannot tell those apart.
	docA2 := rvdSeedDocumentMeta(t, ctx, tenantA, nil, nil, 2048, strings.Repeat("e", 64), now)
	jobA2 := rdSeedJob(t, ctx, tenantA, docA2, "succeeded", now, nil)
	for page := 1; page <= 4; page++ {
		rvdSeedPage(t, ctx, tenantA, docA2, page, 600, 800)
	}
	for _, name := range []string{"issue_date", "currency", "total_amount"} {
		rvdSeedField(t, ctx, tenantA, jobA2, name, rvdStr("A2"), nil, 0, nil, now)
	}

	// B's rows are what make A's counts mean something: without them "2 pages, 1 field" is
	// also what an empty database returns.
	rvdSeedPage(t, ctx, tenantB, docB, 1, 900, 1200)
	rvdSeedPage(t, ctx, tenantB, docB, 2, 900, 1200)
	rvdSeedPage(t, ctx, tenantB, docB, 3, 900, 1200)
	rvdSeedField(t, ctx, tenantB, jobB, "invoice_number", rvdStr("B-0001"), nil, 0, nil, now)
	rvdSeedField(t, ctx, tenantB, jobB, "supplier_tin", rvdStr("B-TIN"), nil, 0, nil, now)

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

// The detail carries one wire entry per field NAME, not per row: ranks 1..N nest under their
// rank-0 sibling, so three rows on invoice_number still yield one field, and no top-level
// reading ever carries a candidate_rank > 0 value. This is the guard against an ungrouped read.
func TestExtractionDetail_AlternativesDoNotBecomeTopLevelFields(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("DECIDED-0"), nil, 0, nil, now)
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("ALTERNATIVE-1"), nil, 1, nil, now)
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("ALTERNATIVE-2"), nil, 2, nil, now)
	rvdSeedField(t, ctx, tenantA, jobA, "supplier_tin", rvdStr("DECIDED-TIN"), nil, 0, nil, now.Add(time.Millisecond))

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

// EXTR-12-02 AC-1: reason_code reaches the wire verbatim. All four values the CHECK admits,
// because a reader that hardcoded one would pass a single-code fixture.
func TestExtractionDetail_CarriesTheStoredReasonCode(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	order := []string{"invoice_number", "supplier_tin", "total_amount", "issue_date", "currency"}
	want := map[string]string{
		"invoice_number": "unreadable",
		"supplier_tin":   "ambiguous",
		"total_amount":   "inconsistent",
		"issue_date":     "missing",
		"currency":       "", // NULL reason_code, the clean case
	}
	for i, name := range order {
		var reason *string
		if want[name] != "" {
			reason = rvdStr(want[name])
		}
		rvdSeedField(t, ctx, tenantA, jobA, name, rvdStr("v"), nil, 0, reason,
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
		if f.Reason != w {
			t.Errorf("field %q came back with reason %q, want the stored reason_code %q", f.Name, f.Reason, w)
		}
	}
}

// EXTR-12-02 AC-1, the NULL half: a clean field's reason crosses as "", never null. Proved on
// the marshalled bytes -- the Go value is "" either way if Reason ever becomes a *string.
func TestExtractionDetail_CleanFieldCarriesAnEmptyReason(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("A-0001"), nil, 0, nil, now)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	if len(got.Fields) != 1 {
		t.Fatalf("got %d field(s) %v, want exactly 1", len(got.Fields), rvdFieldNames(got.Fields))
	}
	if got.Fields[0].Reason != "" {
		t.Errorf("a NULL reason_code came back as %q, want the empty string", got.Fields[0].Reason)
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal the detail: %v", err)
	}
	if !strings.Contains(string(b), `"reason":""`) {
		t.Errorf("the detail marshals to\n  %s\nwithout \"reason\":\"\"", b)
	}
	if strings.Contains(string(b), `"reason":null`) {
		t.Errorf("the detail marshals to\n  %s\nwhich carries \"reason\":null; a clean field's reason is \"\"", b)
	}
}

// EXTR-12-02 AC-2: alternatives is [] on every field, never null. The oracle is the marshalled
// bytes: len(x) == 0 is true of nil and of []T{} alike, so it passes on the exact bug it must
// catch. The control below proves a nil slice really does emit null.
func TestExtractionDetail_AlternativesAreNeverNil(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	for i, name := range []string{"invoice_number", "supplier_tin"} {
		rvdSeedField(t, ctx, tenantA, jobA, name, rvdStr("v"), nil, 0, nil,
			now.Add(time.Duration(i)*time.Millisecond))
	}

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	if len(got.Fields) != 2 {
		t.Fatalf("got %d field(s) %v, want 2 -- the count below is vacuous over a short list",
			len(got.Fields), rvdFieldNames(got.Fields))
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal the detail: %v", err)
	}
	if n := strings.Count(string(b), `"alternatives":[]`); n != len(got.Fields) {
		t.Errorf("the detail marshals to\n  %s\nwith %d empty alternatives array(s), want one per field (%d)",
			b, n, len(got.Fields))
	}
	if strings.Contains(string(b), `"alternatives":null`) {
		t.Errorf("the detail marshals to\n  %s\nwhich carries \"alternatives\":null", b)
	}

	// Control: a nil []ExtractionCandidate really does marshal to null, so the scan above is
	// an assertion and not a tautology.
	var ctl struct {
		Alternatives []extraction.ExtractionCandidate `json:"alternatives"`
	}
	cb, err := json.Marshal(ctl)
	if err != nil {
		t.Fatalf("marshal the nil-slice control: %v", err)
	}
	if string(cb) != `{"alternatives":null}` {
		t.Fatalf("a nil []ExtractionCandidate marshals to %s, want {\"alternatives\":null}; the null scan above proves nothing", cb)
	}
}

// EXTR-12-02 AC-3: ranks 1..N nest under their rank-0 sibling, in rank order, and reach the
// wire nowhere else. The rank-1 row carries a box and the rank-2 row does not, so an
// alternative's region is read per row rather than copied from the decision.
func TestExtractionDetail_NestsAlternativeCandidatesUnderTheirDecidedField(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	decidedBox := &rvdBox{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.30, Y1: 0.40}
	altBox := &rvdBox{Page: 2, X0: 0.51, Y0: 0.52, X1: 0.53, Y1: 0.54}
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("DECIDED-0"), decidedBox, 0, rvdStr("ambiguous"), now)
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("ALTERNATIVE-1"), altBox, 1, nil, now)
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("ALTERNATIVE-2"), nil, 2, nil, now)
	rvdSeedField(t, ctx, tenantA, jobA, "supplier_tin", rvdStr("DECIDED-TIN"), nil, 0, nil, now.Add(time.Millisecond))

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	if len(got.Fields) != 2 {
		t.Fatalf("got %d field(s) %v, want exactly 2 -- alternatives nest, they do not add entries",
			len(got.Fields), rvdFieldNames(got.Fields))
	}

	byName := map[string]extraction.ExtractionFieldState{}
	for _, f := range got.Fields {
		byName[f.Name] = f
	}

	inv, ok := byName["invoice_number"]
	if !ok {
		t.Fatalf("no invoice_number in %v", rvdFieldNames(got.Fields))
	}
	if inv.Reason != "ambiguous" {
		t.Errorf("invoice_number came back with reason %q, want ambiguous", inv.Reason)
	}
	if len(inv.Alternatives) != 2 {
		t.Fatalf("invoice_number came back with %d alternative(s), want the rank-1 and rank-2 rows",
			len(inv.Alternatives))
	}
	for i, want := range []struct {
		value  string
		region *extraction.ExtractionRegion
	}{
		{"ALTERNATIVE-1", &extraction.ExtractionRegion{Page: 2, X0: 0.51, Y0: 0.52, X1: 0.53, Y1: 0.54}},
		{"ALTERNATIVE-2", nil},
	} {
		alt := inv.Alternatives[i]
		if alt.Value == nil || *alt.Value != want.value {
			t.Errorf("alternative at index %d came back with value %v, want %q in rank order", i, alt.Value, want.value)
			continue
		}
		switch {
		case want.region == nil && alt.Region != nil:
			t.Errorf("alternative %q came back with region %+v; its row stored no box", want.value, *alt.Region)
		case want.region != nil && alt.Region == nil:
			t.Errorf("alternative %q came back with a nil region, want %+v", want.value, *want.region)
		case want.region != nil && *alt.Region != *want.region:
			t.Errorf("alternative %q came back with region %+v, want %+v", want.value, *alt.Region, *want.region)
		}
	}

	tin, ok := byName["supplier_tin"]
	if !ok {
		t.Fatalf("no supplier_tin in %v", rvdFieldNames(got.Fields))
	}
	if len(tin.Alternatives) != 0 {
		t.Errorf("supplier_tin came back with %d alternative(s) %+v; its job wrote it only at rank 0",
			len(tin.Alternatives), tin.Alternatives)
	}
	for _, f := range got.Fields {
		if f.Value != nil && strings.HasPrefix(*f.Value, "ALTERNATIVE-") {
			t.Errorf("top-level field %q carries %q, which is a candidate_rank > 0 row", f.Name, *f.Value)
		}
	}
}

// EXTR-12-02 AC-3, the shape half: an alternative is value and region only. Asserted on the
// marshalled keys rather than the Go type, because that is what the SPA and e2e mirrors read.
func TestExtractionDetail_AnAlternativeCarriesOnlyValueAndRegion(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("DECIDED-0"), nil, 0, rvdStr("ambiguous"), now)
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("ALTERNATIVE-1"),
		&rvdBox{Page: 1, X0: 0.1, Y0: 0.2, X1: 0.3, Y1: 0.4}, 1, nil, now)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal the detail: %v", err)
	}

	var wire struct {
		Fields []struct {
			Name         string                       `json:"name"`
			Alternatives []map[string]json.RawMessage `json:"alternatives"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal the detail %s: %v", b, err)
	}

	var scanned int
	for _, f := range wire.Fields {
		for i, alt := range f.Alternatives {
			keys := make([]string, 0, len(alt))
			for k := range alt {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if !reflect.DeepEqual(keys, []string{"region", "value"}) {
				t.Errorf("field %q alternative %d marshals keys %v, want exactly [region value] -- no name, no reason, no nesting",
					f.Name, i, keys)
			}
			scanned++
		}
	}
	if scanned == 0 {
		t.Fatalf("the detail marshals to\n  %s\nwith no alternative at all, so the key-set assertion examined nothing", b)
	}
}

// EXTR-12-02: every row of one job shares a created_at in production (writeFieldResultsTx runs
// in one transaction and now() is transaction-constant), so the tie is real. The ids here
// ascend in an order that disagrees with BOTH field_name and candidate_rank: an ORDER BY that
// falls back to id alone returns supplier_tin first and ALTERNATIVE-2 before ALTERNATIVE-1.
func TestExtractionDetail_AlternativeOrderSurvivesACreatedAtTie(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	ids := rvdOrderedIDs(4)
	rvdSeedFieldID(t, ctx, tenantA, jobA, ids[0], "supplier_tin", rvdStr("DECIDED-TIN"), nil, 0, nil, now)
	rvdSeedFieldID(t, ctx, tenantA, jobA, ids[1], "invoice_number", rvdStr("ALTERNATIVE-2"), nil, 2, nil, now)
	rvdSeedFieldID(t, ctx, tenantA, jobA, ids[2], "invoice_number", rvdStr("ALTERNATIVE-1"), nil, 1, nil, now)
	rvdSeedFieldID(t, ctx, tenantA, jobA, ids[3], "invoice_number", rvdStr("DECIDED-0"), nil, 0, rvdStr("ambiguous"), now)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	if names := rvdFieldNames(got.Fields); !reflect.DeepEqual(names, []string{"invoice_number", "supplier_tin"}) {
		t.Fatalf("fields came back in order %v, want [invoice_number supplier_tin] -- field_name breaks the created_at tie, not id", names)
	}

	alts := []string{}
	for _, a := range got.Fields[0].Alternatives {
		if a.Value == nil {
			alts = append(alts, "<nil>")
			continue
		}
		alts = append(alts, *a.Value)
	}
	if !reflect.DeepEqual(alts, []string{"ALTERNATIVE-1", "ALTERNATIVE-2"}) {
		t.Errorf("invoice_number's alternatives came back in order %v, want [ALTERNATIVE-1 ALTERNATIVE-2] -- candidate_rank breaks the tie, not id", alts)
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

	rvdSeedField(t, ctx, tenantA, jobA, "supplier_tin", rvdStr("12345678-0001"), nil, 0, nil, now)

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
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("MOCK-INV-0001"), &want, 0, nil, now)

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
	var (
		sqlLits                             int
		joins                               int
		sawPages, sawFields, sawCorrections bool
	)
	// Both sources the detail read issues SQL from: correction_store.go's INSERT names tenant_id
	// legitimately and holds no SELECT, so the filter below never reaches it.
	sources := []string{rdReaderSource, csStoreSource}
	for _, src := range sources {
		f, fset := mxParse(t, src)
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
			if strings.Contains(unq, "extraction_field_corrections") {
				sawCorrections = true
			}
			if strings.Contains(unq, "tenant_id") {
				t.Errorf("%s: a %s query names tenant_id; the tenant_isolation policy is the only predicate",
					fset.Position(bl.Pos()), src)
			}
			return true
		})
	}

	if sqlLits == 0 {
		t.Fatalf("%v hold no SQL string literal, so the tenant_id absence above proved nothing", sources)
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
	if !sawCorrections {
		t.Errorf("%s holds no SELECT naming extraction_field_corrections, so widening this scan to it proved nothing", csStoreSource)
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
	structs := map[string]map[string]*ast.StructType{}
	for _, name := range rvdSources() {
		f, _ := mxParse(t, name)
		structs[name] = map[string]*ast.StructType{}
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			if st, ok := ts.Type.(*ast.StructType); ok {
				structs[name][ts.Name.Name] = st
			}
			return true
		})
	}

	var scanned int
	for _, want := range rvdWireStructs {
		st, ok := structs[want.src][want.name]
		if !ok {
			t.Errorf("%s declares no struct %s", want.src, want.name)
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
	sources := map[string]string{}
	for _, name := range rvdSources() {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		sources[name] = string(raw)
	}
	for _, want := range rvdWireStructs {
		body := regexp.MustCompile(`type\s+` + want.name + `\s+struct\s*\{([^{}]*)\}`).FindStringSubmatch(sources[want.src])
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
		// Escapes, not literals: ci.yml's Format step greps every .go file for U+201C/U+201D,
		// so a literal needle here fails the build this test exists to protect.
		for _, bad := range []string{"''", "\u2018", "\u2019", "\u201c", "\u201d"} {
			if strings.Contains(trimmed, bad) {
				t.Errorf("%s:%d: comment carries %q; write \"\" instead", rdReaderSource, i+1, bad)
			}
		}
	}
	if comments == 0 {
		t.Fatalf("%s holds no // comment, so the scan above examined nothing", rdReaderSource)
	}
}

// --- EXTR-12-05: the human layer laid over the decided readings ---------------------------

// The actor every correction seeded below is written by: a raw GoTrue subject, the convention
// audit_log.actor follows.
const rvcActor = "3c9f2a10-7b4d-4e6a-9c21-0f5a8d3b6e47"

// rvcSeedCorrection writes one extraction_field_corrections row as the SUPERUSER, never through
// the write path the merge reads back — a fixture built by the code under test cannot fail.
// Each call is its own statement, so seq ascends in call order.
//
// anchor is nil for "no label": anchor_label carries no char_length CHECK, so an empty-string
// fixture would reach the wire as "where":"" and pass the case the nullable key exists to catch.
func rvcSeedCorrection(t *testing.T, ctx context.Context, tenantID, jobID, name, value, method string, box *rvdBox, anchor *string) {
	t.Helper()

	var (
		page           *int
		x0, y0, x1, y1 *float64
	)
	if box != nil {
		page, x0, y0, x1, y1 = &box.Page, &box.X0, &box.Y0, &box.X1, &box.Y1
	}
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO extraction_field_corrections
		     (tenant_id, extraction_job_id, field_name, value, method, page,
		      bbox_x0, bbox_y0, bbox_x1, bbox_y1, anchor_label, actor)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		tenantID, jobID, name, value, method, page, x0, y0, x1, y1, anchor, rvcActor,
	); err != nil {
		t.Fatalf("seed the %s correction %q on %s: %v", method, value, name, err)
	}
}

// rvcField returns one merged field by name.
func rvcField(t *testing.T, d extraction.ExtractionDetail, name string) extraction.ExtractionFieldState {
	t.Helper()
	for _, f := range d.Fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("the detail carries no field %q, only %v", name, rvdFieldNames(d.Fields))
	return extraction.ExtractionFieldState{}
}

// rvcValue renders a *string for a failure message without dereferencing a nil.
func rvcValue(v *string) string {
	if v == nil {
		return "<nil>"
	}
	return strconv.Quote(*v)
}

// rvcRegion renders a *ExtractionRegion the same way.
func rvcRegion(r *extraction.ExtractionRegion) string {
	if r == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%+v", *r)
}

// rvcCorrectedRaw returns the raw JSON of one field's "corrected" key, so an assertion reads
// the bytes the SPA parses rather than the Go value behind them. "" means the key is absent.
func rvcCorrectedRaw(t *testing.T, d extraction.ExtractionDetail, name string) string {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal the detail: %v", err)
	}
	var wire struct {
		Fields []struct {
			Name      string          `json:"name"`
			Corrected json.RawMessage `json:"corrected"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal the detail %s: %v", b, err)
	}
	for _, f := range wire.Fields {
		if f.Name == name {
			return string(f.Corrected)
		}
	}
	t.Fatalf("the marshalled detail %s carries no field %q", b, name)
	return ""
}

// AC-1: three corrections on one field and the newest wins. Four distinct strings, so a merge
// picking the reading or either superseded correction fails on the value it returns.
func TestExtractionDetail_LatestCorrectionWins(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("READ-A"), nil, 0, nil, now)
	for _, v := range []string{"HUMAN-B", "HUMAN-C", "HUMAN-D"} {
		rvcSeedCorrection(t, ctx, tenantA, jobA, "total", v, "typed", nil, nil)
	}

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	total := rvcField(t, got, "total")
	if total.Value == nil || *total.Value != "HUMAN-D" {
		t.Errorf("total came back %s, want \"HUMAN-D\" — the newest correction, not the reading "+
			"READ-A and not the superseded HUMAN-B/HUMAN-C", rvcValue(total.Value))
	}
	if total.Corrected == nil || total.Corrected.Method != "typed" {
		t.Errorf("total's corrected block is %+v, want method typed — a settled field carries one",
			total.Corrected)
	}
}

// AC-1's undo half. The undone row carries UNDO-Z, a value neither the extractor nor any
// correction ever wrote: a merge that returned the latest row's value would surface it.
//
// TWO corrections sit under the undo, which is what separates the rule from its neighbour: one
// undo drops the whole stack rather than cancelling the row beneath it. With a single
// correction the two rules agree on READ-A and neither can be told from the other.
//
// vat is the load-bearing control. Without it every assertion on total is also what a merge
// that ignores corrections entirely returns, and the case would pass on a do-nothing read.
func TestExtractionDetail_UndoneIgnoresItsOwnValueAndRestoresTheReading(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("READ-A"), nil, 0, rvdStr("ambiguous"), now)
	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("ALT-1"), nil, 1, nil, now)
	rvcSeedCorrection(t, ctx, tenantA, jobA, "total", "HUMAN-B", "typed", nil, nil)
	rvcSeedCorrection(t, ctx, tenantA, jobA, "total", "ALT-1", "chosen", nil, nil)
	rvcSeedCorrection(t, ctx, tenantA, jobA, "total", "UNDO-Z", "undone", nil, nil)

	rvdSeedField(t, ctx, tenantA, jobA, "vat", rvdStr("READ-V"), nil, 0, nil, now.Add(time.Millisecond))
	rvcSeedCorrection(t, ctx, tenantA, jobA, "vat", "HUMAN-W", "typed", nil, nil)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}

	total := rvcField(t, got, "total")
	if total.Value == nil || *total.Value != "READ-A" {
		t.Errorf("the undone total came back %s, want \"READ-A\" — an undo resets the field to the "+
			"extractor's reading: its own value is ignored, and so is the whole stack beneath it, "+
			"never just the correction it sits on (which would give \"HUMAN-B\")", rvcValue(total.Value))
	}
	if raw := rvcCorrectedRaw(t, got, "total"); raw != "null" {
		t.Errorf("the undone total marshals corrected as %s, want null — an undone field is no "+
			"longer a corrected one", raw)
	}
	if total.Reason != "ambiguous" {
		t.Errorf("the undone total came back with reason %q, want \"ambiguous\" — the reset restores "+
			"the reading's own flag", total.Reason)
	}
	if len(total.Alternatives) != 1 {
		t.Errorf("the undone total came back with %d alternative(s), want 1 — the reset restores them too",
			len(total.Alternatives))
	}

	// The control: without a field the merge must actually change, every assertion above is
	// satisfied by a read that never looks at a correction at all.
	vat := rvcField(t, got, "vat")
	if vat.Value == nil || *vat.Value != "HUMAN-W" {
		t.Errorf("the corrected vat came back %s, want \"HUMAN-W\" — this control is what stops the "+
			"undo assertions above passing on a read that ignores corrections", rvcValue(vat.Value))
	}
}

// AC-2: a settled field is no longer flagged. Asserted on the marshalled bytes, which is what
// the SPA branches on.
func TestExtractionDetail_CorrectedFieldIsNoLongerFlagged(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("READ-A"),
		&rvdBox{Page: 1, X0: 0.11, Y0: 0.12, X1: 0.13, Y1: 0.14}, 0, rvdStr("ambiguous"), now)
	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("ALT-1"),
		&rvdBox{Page: 1, X0: 0.21, Y0: 0.22, X1: 0.23, Y1: 0.24}, 1, nil, now)
	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("ALT-2"),
		&rvdBox{Page: 2, X0: 0.31, Y0: 0.32, X1: 0.33, Y1: 0.34}, 2, nil, now)
	rvcSeedCorrection(t, ctx, tenantA, jobA, "total", "ALT-2", "chosen", nil, nil)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	b, err := json.Marshal(rvcField(t, got, "total"))
	if err != nil {
		t.Fatalf("marshal the total field: %v", err)
	}
	for _, want := range []string{`"reason":""`, `"alternatives":[]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the settled total marshals to %s, which does not carry %s — a corrected field "+
				"must not render as flagged", b, want)
		}
	}
}

// AC-3: was is the value the correction superseded, not the current one and not the reading.
// Four distinct strings, so returning READ-A, ALT-X or HUMAN-C all fail.
func TestExtractionDetail_WasIsTheSupersededReading(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("READ-A"), nil, 0, rvdStr("ambiguous"), now)
	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("ALT-X"), nil, 1, nil, now)
	rvcSeedCorrection(t, ctx, tenantA, jobA, "total", "HUMAN-B", "typed", nil, nil)
	rvcSeedCorrection(t, ctx, tenantA, jobA, "total", "HUMAN-C", "typed", nil, nil)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	total := rvcField(t, got, "total")
	if total.Value == nil || *total.Value != "HUMAN-C" {
		t.Errorf("total came back %s, want \"HUMAN-C\"", rvcValue(total.Value))
	}
	if raw := rvcCorrectedRaw(t, got, "total"); raw != `{"method":"typed","was":"HUMAN-B","where":null}` {
		t.Errorf("total marshals corrected as %s, want {\"method\":\"typed\",\"was\":\"HUMAN-B\","+
			"\"where\":null} — was is the reading the newest correction superseded, which is the "+
			"correction beneath it", raw)
	}
}

// AC-3's other half: when the correction is a field's first there is no earlier correction, so
// was falls back to the decided reading — and that reading may itself carry no value at all,
// which is why was is nullable.
func TestExtractionDetail_WasIsTheReadingWhenTheCorrectionIsTheFirst(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("READ-A"), nil, 0, nil, now)
	rvcSeedCorrection(t, ctx, tenantA, jobA, "total", "HUMAN-B", "typed", nil, nil)

	// The mock's own shape: a missing field carries a nil Value (mock.go, laws E08/E10).
	rvdSeedField(t, ctx, tenantA, jobA, "buyer_tin", nil, nil, 0, rvdStr("missing"), now.Add(time.Millisecond))
	rvcSeedCorrection(t, ctx, tenantA, jobA, "buyer_tin", "HUMAN-T", "typed", nil, nil)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	if raw := rvcCorrectedRaw(t, got, "total"); raw != `{"method":"typed","was":"READ-A","where":null}` {
		t.Errorf("total marshals corrected as %s, want was \"READ-A\" — the first correction on a "+
			"field supersedes the extractor's reading", raw)
	}
	if raw := rvcCorrectedRaw(t, got, "buyer_tin"); raw != `{"method":"typed","was":null,"where":null}` {
		t.Errorf("buyer_tin marshals corrected as %s, want was null — the extractor read nothing "+
			"there, and \"\" would claim it read an empty value", raw)
	}
}

// AC-4: corrected is null, never an empty object, on a field no human has touched.
//
// The second job is the load-bearing control: "corrected is null everywhere" is also what a
// build where corrected is ALWAYS null returns, and that build is wrong.
func TestExtractionDetail_UncorrectedFieldHasNullCorrected(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("READ-A"), nil, 0, nil, now)
	rvdSeedField(t, ctx, tenantA, jobA, "vat", rvdStr("READ-V"), nil, 0, nil, now.Add(time.Millisecond))

	docB := rdSeedDocument(t, ctx, tenantA)
	jobB := rdSeedJob(t, ctx, tenantA, docB, "succeeded", now, nil)
	rvdSeedField(t, ctx, tenantA, jobB, "total", rvdStr("READ-B"), nil, 0, nil, now)
	rvcSeedCorrection(t, ctx, tenantA, jobB, "total", "HUMAN-B", "typed", nil, nil)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for the uncorrected job %s: %v", jobA, err)
	}
	if len(got.Fields) != 2 {
		t.Fatalf("the uncorrected job carried %d field(s) %v, want 2 — the assertions below need both",
			len(got.Fields), rvdFieldNames(got.Fields))
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal the uncorrected detail: %v", err)
	}
	if n := strings.Count(string(b), `"corrected":null`); n != 2 {
		t.Errorf("the uncorrected detail %s carries %d \"corrected\":null key(s), want 2 — one per field", b, n)
	}
	if strings.Contains(string(b), `"corrected":{`) {
		t.Errorf("the uncorrected detail %s carries a corrected object; a field no human touched "+
			"carries null, never an empty object", b)
	}

	// The control: a build that never emits a corrected object passes everything above.
	gotB, err := r.Detail(ctxA, jobB)
	if err != nil {
		t.Fatalf("Detail for the corrected job %s: %v", jobB, err)
	}
	bB, err := json.Marshal(gotB)
	if err != nil {
		t.Fatalf("marshal the corrected detail: %v", err)
	}
	if !strings.Contains(string(bB), `"corrected":{`) {
		t.Errorf("the corrected detail %s carries no corrected object at all, so the null assertions "+
			"above pass for the wrong reason", bB)
	}
}

// AC-5: a chosen correction settles the field FROM one of the alternatives, so the highlight
// must move to that alternative's own box. R0, R1 and R2 share no coordinate and R2 sits on
// page 2, so a merge that keeps R0, takes the first alternative, or hardcodes the page all fail.
//
// invoice_number is the decoy and is read FIRST: it is ambiguous too and one of its own
// alternatives spells "ALT-2" at a fourth box, so a merge that matches the correction's value
// against a neighbour's alternatives rather than the corrected field's own points at rDecoy.
func TestExtractionDetail_ChosenCorrectionTakesTheAlternativesRegion(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	rDecoy := rvdBox{Page: 3, X0: 0.71, Y0: 0.72, X1: 0.83, Y1: 0.84}
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("READ-N"),
		&rvdBox{Page: 1, X0: 0.01, Y0: 0.02, X1: 0.03, Y1: 0.04}, 0, rvdStr("ambiguous"), now)
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("ALT-2"), &rDecoy, 1, nil, now)

	later := now.Add(time.Millisecond)
	r2 := rvdBox{Page: 2, X0: 0.51, Y0: 0.52, X1: 0.63, Y1: 0.64}
	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("READ-A"),
		&rvdBox{Page: 1, X0: 0.11, Y0: 0.12, X1: 0.13, Y1: 0.14}, 0, rvdStr("ambiguous"), later)
	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("ALT-1"),
		&rvdBox{Page: 1, X0: 0.21, Y0: 0.22, X1: 0.33, Y1: 0.34}, 1, nil, later)
	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("ALT-2"), &r2, 2, nil, later)
	rvcSeedCorrection(t, ctx, tenantA, jobA, "total", "ALT-2", "chosen", nil, nil)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	if names := rvdFieldNames(got.Fields); !slices.Equal(names, []string{"invoice_number", "total"}) {
		t.Fatalf("the detail carries fields %v, want [invoice_number total] — total must not be the "+
			"first field, or a merge reading the wrong field's alternatives reads its own",
			names)
	}
	total := rvcField(t, got, "total")
	want := extraction.ExtractionRegion{Page: r2.Page, X0: r2.X0, Y0: r2.Y0, X1: r2.X1, Y1: r2.Y1}
	if total.Region == nil || *total.Region != want {
		t.Errorf("the chosen total highlights %s, want %+v — the field was settled from the second "+
			"alternative, so the canvas must point where THAT alternative was read, not where a "+
			"neighbouring field happens to hold the same string",
			rvcRegion(total.Region), want)
	}
	if total.Value == nil || *total.Value != "ALT-2" {
		t.Errorf("the chosen total came back %s, want \"ALT-2\"", rvcValue(total.Value))
	}

	// The decoy carries no correction, so the merge must leave it exactly as read.
	decoy := rvcField(t, got, "invoice_number")
	if raw := rvcCorrectedRaw(t, got, "invoice_number"); raw != "null" {
		t.Errorf("invoice_number marshals corrected as %s, want null — nothing was corrected there", raw)
	}
	if decoy.Reason != "ambiguous" || len(decoy.Alternatives) != 1 {
		t.Errorf("invoice_number came back with reason %q and %d alternative(s), want \"ambiguous\" and 1 "+
			"— a correction on total must not settle its neighbour", decoy.Reason, len(decoy.Alternatives))
	}
}

// AC-5's fallback: a chosen value matching no alternative keeps the reading's own region rather
// than emitting none. The value assertion is what reds — the region assertion alone is also
// what a merge that ignores corrections returns.
func TestExtractionDetail_ChosenValueMatchingNoAlternativeKeepsTheReadingsRegion(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	r0 := rvdBox{Page: 1, X0: 0.11, Y0: 0.12, X1: 0.13, Y1: 0.14}
	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("READ-A"), &r0, 0, rvdStr("ambiguous"), now)
	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("ALT-1"),
		&rvdBox{Page: 2, X0: 0.21, Y0: 0.22, X1: 0.33, Y1: 0.34}, 1, nil, now)
	rvcSeedCorrection(t, ctx, tenantA, jobA, "total", "HUMAN-NOMATCH", "chosen", nil, nil)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	total := rvcField(t, got, "total")
	if total.Value == nil || *total.Value != "HUMAN-NOMATCH" {
		t.Errorf("the chosen total came back %s, want \"HUMAN-NOMATCH\" — a chosen value matching no "+
			"alternative still settles the field", rvcValue(total.Value))
	}
	want := extraction.ExtractionRegion{Page: r0.Page, X0: r0.X0, Y0: r0.Y0, X1: r0.X1, Y1: r0.Y1}
	if total.Region == nil || *total.Region != want {
		t.Errorf("the chosen total highlights %s, want the reading's own %+v — with nothing to match, "+
			"the region is left alone rather than dropped", rvcRegion(total.Region), want)
	}
}

// AC-5's other half, and what subtask 08 reads back: a pointed correction carries its own
// stored box and the field must highlight there. Four distinct coordinates on page 2, so an
// x/y transposition and a hardcoded page each fail here rather than on a deployed screen.
func TestExtractionDetail_PointedCorrectionTakesItsOwnStoredBox(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	pointed := rvdBox{Page: 2, X0: 0.21, Y0: 0.32, X1: 0.43, Y1: 0.54}
	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("READ-A"),
		&rvdBox{Page: 1, X0: 0.11, Y0: 0.12, X1: 0.13, Y1: 0.14}, 0, nil, now)
	rvcSeedCorrection(t, ctx, tenantA, jobA, "total", "HUMAN-P", "pointed", &pointed, nil)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	total := rvcField(t, got, "total")
	want := extraction.ExtractionRegion{Page: pointed.Page, X0: pointed.X0, Y0: pointed.Y0, X1: pointed.X1, Y1: pointed.Y1}
	if total.Region == nil || *total.Region != want {
		t.Errorf("the pointed total highlights %s, want the stored box %+v — the human dragged that "+
			"box and the read is the only thing that gives it back", rvcRegion(total.Region), want)
	}
	if total.Value == nil || *total.Value != "HUMAN-P" {
		t.Errorf("the pointed total came back %s, want \"HUMAN-P\"", rvcValue(total.Value))
	}
}

// AC-6: a typed correction changed the value, not where it was read from. The value assertion
// is what reds — the region assertion alone passes on a read that ignores corrections.
func TestExtractionDetail_TypedCorrectionKeepsTheOriginalRegion(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	r0 := rvdBox{Page: 2, X0: 0.11, Y0: 0.22, X1: 0.33, Y1: 0.44}
	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("READ-A"), &r0, 0, nil, now)
	rvcSeedCorrection(t, ctx, tenantA, jobA, "total", "HUMAN-B", "typed", nil, nil)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	total := rvcField(t, got, "total")
	if total.Value == nil || *total.Value != "HUMAN-B" {
		t.Errorf("the typed total came back %s, want \"HUMAN-B\"", rvcValue(total.Value))
	}
	want := extraction.ExtractionRegion{Page: r0.Page, X0: r0.X0, Y0: r0.Y0, X1: r0.X1, Y1: r0.Y1}
	if total.Region == nil || *total.Region != want {
		t.Errorf("the typed total highlights %s, want the reading's own %+v — typing a value says "+
			"nothing about where it was read", rvcRegion(total.Region), want)
	}
}

// The wire key corrected.where carries the correction's anchor label, and is null — never "" —
// when the row has none. anchor_label has no char_length CHECK, so "" is a value the column
// admits and a merge that coerced nil to "" would render a dangling "Taken from ".
func TestExtractionDetail_WhereCarriesTheAnchorLabelAndIsNullWithoutOne(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	label := "Total due"
	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("READ-A"), nil, 0, nil, now)
	rvcSeedCorrection(t, ctx, tenantA, jobA, "total", "HUMAN-P", "pointed",
		&rvdBox{Page: 2, X0: 0.21, Y0: 0.32, X1: 0.43, Y1: 0.54}, &label)

	rvdSeedField(t, ctx, tenantA, jobA, "vat", rvdStr("READ-V"), nil, 0, nil, now.Add(time.Millisecond))
	rvcSeedCorrection(t, ctx, tenantA, jobA, "vat", "HUMAN-W", "typed", nil, nil)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	if raw := rvcCorrectedRaw(t, got, "total"); raw != `{"method":"pointed","was":"READ-A","where":"Total due"}` {
		t.Errorf("total marshals corrected as %s, want where \"Total due\" — the stored anchor label "+
			"is the one member of the block not derivable from the rest of the field", raw)
	}
	if raw := rvcCorrectedRaw(t, got, "vat"); raw != `{"method":"typed","was":"READ-V","where":null}` {
		t.Errorf("vat marshals corrected as %s, want where null — a correction with no anchor label "+
			"says so explicitly; \"\" would read as a label the row does not carry", raw)
	}
}

// A correction may name a field the extractor produced no reading for: refuseField admits
// buyer_name and currency, which mockDefaultResult never emits, so the row already reaches the
// invoice while the screen shows nothing. The entry is appended after the read fields.
func TestExtractionDetail_CorrectionForAFieldTheExtractorNeverReadIsCarried(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)

	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("READ-A"), nil, 0, nil, now)
	rvdSeedField(t, ctx, tenantA, jobA, "vat", rvdStr("READ-V"), nil, 0, nil, now.Add(time.Millisecond))
	// Two of them, seeded newest-name-first: [buyer_name currency] is neither the seeding order
	// nor a one-element list, so the appended run is ordered rather than incidental.
	rvcSeedCorrection(t, ctx, tenantA, jobA, "currency", "NGN", "typed", nil, nil)
	pointed := rvdBox{Page: 2, X0: 0.21, Y0: 0.32, X1: 0.43, Y1: 0.54}
	rvcSeedCorrection(t, ctx, tenantA, jobA, "buyer_name", "HUMAN-N", "pointed", &pointed, nil)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	names := rvdFieldNames(got.Fields)
	want := []string{"total", "vat", "buyer_name", "currency"}
	if !slices.Equal(names, want) {
		t.Fatalf("the detail carries fields %v, want %v — a correction on a field the extractor never "+
			"read must still reach the screen, appended after the read fields and in name order",
			names, want)
	}
	currency := rvcField(t, got, "currency")
	if currency.Value == nil || *currency.Value != "NGN" {
		t.Errorf("the synthesized currency came back %s, want \"NGN\"", rvcValue(currency.Value))
	}
	if currency.Region != nil {
		t.Errorf("the synthesized currency highlights %s, want none — a typed value says nothing "+
			"about where it was read", rvcRegion(currency.Region))
	}
	if raw := rvcCorrectedRaw(t, got, "currency"); raw != `{"method":"typed","was":null,"where":null}` {
		t.Errorf("the synthesized currency marshals corrected as %s, want was null — nothing preceded "+
			"the human's value", raw)
	}
	if currency.Reason != "" || len(currency.Alternatives) != 0 {
		t.Errorf("the synthesized currency came back with reason %q and %d alternative(s), want \"\" and 0",
			currency.Reason, len(currency.Alternatives))
	}

	// A pointed correction is the one way a never-read field gets a box, and subtask 08 is what
	// sends one. Four distinct coordinates on page 2, so a transposition and a hardcoded page fail.
	buyerName := rvcField(t, got, "buyer_name")
	wantBox := extraction.ExtractionRegion{Page: pointed.Page, X0: pointed.X0, Y0: pointed.Y0, X1: pointed.X1, Y1: pointed.Y1}
	if buyerName.Region == nil || *buyerName.Region != wantBox {
		t.Errorf("the synthesized buyer_name highlights %s, want the stored box %+v — a field the "+
			"extractor never read is exactly the field a human has to point at",
			rvcRegion(buyerName.Region), wantBox)
	}
	if buyerName.Value == nil || *buyerName.Value != "HUMAN-N" {
		t.Errorf("the synthesized buyer_name came back %s, want \"HUMAN-N\"", rvcValue(buyerName.Value))
	}
}

// The corrections read names no tenant_id, so tenant_isolation is the only thing scoping it.
// B holds a correction on the same field name, so A reading its own value means something.
func TestRLS_ExtractionDetailCorrectionsAreScopedByThePolicyAlone(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	_, tenantB, docB := rdTenant(t, ctx, "active")

	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	jobB := rdSeedJob(t, ctx, tenantB, docB, "succeeded", now, nil)

	rvdSeedField(t, ctx, tenantA, jobA, "total", rvdStr("A-READ"), nil, 0, nil, now)
	rvcSeedCorrection(t, ctx, tenantA, jobA, "total", "A-HUMAN", "typed", nil, nil)

	rvdSeedField(t, ctx, tenantB, jobB, "total", rvdStr("B-READ"), nil, 0, nil, now)
	for _, v := range []string{"B-HUMAN-1", "B-HUMAN-2"} {
		rvcSeedCorrection(t, ctx, tenantB, jobB, "total", v, "typed", nil, nil)
	}

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("A reading its own job %s: %v", jobA, err)
	}
	total := rvcField(t, got, "total")
	if total.Value == nil || *total.Value != "A-HUMAN" {
		t.Errorf("A's total came back %s, want \"A-HUMAN\" — its own correction, never B's",
			rvcValue(total.Value))
	}
	if raw := rvcCorrectedRaw(t, got, "total"); raw != `{"method":"typed","was":"A-READ","where":null}` {
		t.Errorf("A's total marshals corrected as %s, want was \"A-READ\" — B's two corrections must "+
			"not reach A's window at all", raw)
	}

	if _, err := r.Detail(ctxA, jobB); !errors.Is(err, extraction.ErrNotFound) {
		t.Errorf("A reading B's job %s returned %v, want %v", jobB, err, extraction.ErrNotFound)
	}
}

// EXTR-13-10: the real POST/GET path for TestExtractionMerge_LineCorrectionOverwritesACell's
// pure-function proof. A real line-items write is posted through the handler (lixDBServe,
// handlers_lineitems_db_test.go), then Detail is read on the same job. Today the per-cell
// readings pass straight through untouched, so the field still carries the seeded reading.
func TestRLS_LineItemsCorrectionRoundTripsThroughDetail(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	entityID := cxEntity(t, ctx, tenantID)
	cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR13-10-RT", "draft")

	now := time.Now().UTC().Truncate(time.Microsecond)
	for i, seed := range []struct{ role, value string }{
		{extraction.LineRoleDescription, "OLD-DESC"},
		{extraction.LineRoleQuantity, "9"},
		{extraction.LineRoleUnitPrice, "99.00"},
		{extraction.LineRoleLineTotal, "99.00"},
	} {
		rvdSeedField(t, ctx, tenantID, jobID, extraction.LineFieldName(1, seed.role), rvdStr(seed.value), nil, 0, nil,
			now.Add(time.Duration(i)*time.Millisecond))
	}

	// lixLinesBody(1) posts one line: description "line 0", quantity "1", unit_price "1.00",
	// line_total "1.00" -- every cell differs from the OLD-* readings seeded above.
	w := lixDBServe(t, reqCtx, jobID, lixLinesBody(1), lixApplier(lixApplyOpts{write: true}), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("POST line-items: %d (body=%q), want 201 -- the Detail assertions below would be vacuous", w.Code, w.Body.String())
	}

	got, err := r.Detail(reqCtx, jobID)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobID, err)
	}
	name := extraction.LineFieldName(1, extraction.LineRoleDescription)
	f := rvcField(t, got, name)
	if f.Value == nil || *f.Value != "line 0" {
		t.Errorf("%s = %s after the POST, want \"line 0\" -- the value that was just saved, not the OLD-DESC reading",
			name, rvcValue(f.Value))
	}
}

// ---------------------------------------------------------------------------
// EXTR-15-01 — failure_kind on ExtractionDetail
// ---------------------------------------------------------------------------

// rvdJSONKey returns one top-level raw JSON value, failing attributably when the key is
// absent. The assertions below go through the marshalled wire rather than the Go field so a
// missing field reds on an assertion instead of on a compile error.
func rvdJSONKey(t *testing.T, b []byte, key string) string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	raw, ok := m[key]
	if !ok {
		t.Fatalf("the wire body carries no %q key:\n  %s", key, b)
	}
	return string(raw)
}

// FK-8 (AC-7). No omitempty: a job that never failed serialises an explicit null, so the
// review screen can tell "no kind" from "key not sent".
func TestExtractionDetail_FailureKindMarshalsAsExplicitNull(t *testing.T) {
	b, err := json.Marshal(extraction.ExtractionDetail{})
	if err != nil {
		t.Fatalf("marshal a zero ExtractionDetail: %v", err)
	}
	if got := rvdJSONKey(t, b, "failure_kind"); got != "null" {
		t.Errorf("a zero ExtractionDetail marshals failure_kind as %s, want null", got)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal the detail: %v", err)
	}
	gotKeys := make([]string, 0, len(decoded))
	for k := range decoded {
		gotKeys = append(gotKeys, k)
	}
	slices.Sort(gotKeys)
	wantKeys := []string{"document", "document_id", "failure_kind", "fields", "id", "pages", "state"}
	if !slices.Equal(gotKeys, wantKeys) {
		t.Errorf("ExtractionDetail carries keys %v, want exactly %v", gotKeys, wantKeys)
	}
}

// FK-8 (AC-7/8). The two extraction DTOs answer the same question about the same job with the
// same value. Asserted against a literal first, then against each other: an equality alone is
// satisfied by two DTOs that are both wrong, and by two absent keys.
func TestExtractionDetail_FailureKindAgreesWithTheJobsList(t *testing.T) {
	ctx := t.Context()
	stRequireFailureKind(t, ctx)

	r := rdReader(t)
	reqCtx, tenantID, failedDoc := rdTenant(t, ctx, "active")
	cleanDoc := rdSeedDocument(t, ctx, tenantID)

	failedJob := rdSeedJob(t, ctx, tenantID, failedDoc, "dead_lettered", time.Now().UTC(), stPtr("boom"))
	stPlantFailureKind(t, ctx, failedJob, "text_not_read")
	cleanJob := rdSeedJob(t, ctx, tenantID, cleanDoc, "succeeded", time.Now().UTC(), nil)

	for _, tc := range []struct {
		name  string
		jobID string
		docID string
		want  string
	}{
		{"dead-lettered", failedJob, failedDoc, `"text_not_read"`},
		// The control: a job that settled cleanly carries null on both DTOs, so the equality
		// below cannot pass on two keys that are simply never populated.
		{"succeeded", cleanJob, cleanDoc, "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail, err := r.Detail(reqCtx, tc.jobID)
			if err != nil {
				t.Fatalf("Detail for job %s: %v", tc.jobID, err)
			}
			db, err := json.Marshal(detail)
			if err != nil {
				t.Fatalf("marshal the detail: %v", err)
			}
			detailKind := rvdJSONKey(t, db, "failure_kind")
			if detailKind != tc.want {
				t.Errorf("GET /v1/extractions/%s reports failure_kind %s, want %s", tc.jobID, detailKind, tc.want)
			}

			resp, err := r.JobsForDocument(reqCtx, tc.docID)
			if err != nil {
				t.Fatalf("JobsForDocument for %s: %v", tc.docID, err)
			}
			if len(resp.Jobs) != 1 {
				t.Fatalf("the document holds %d job(s), want 1; the comparison below would pick the wrong row", len(resp.Jobs))
			}
			jb, err := json.Marshal(resp.Jobs[0])
			if err != nil {
				t.Fatalf("marshal the job state: %v", err)
			}
			listKind := rvdJSONKey(t, jb, "failure_kind")
			if listKind != tc.want {
				t.Errorf("GET /v1/extractions reports failure_kind %s for job %s, want %s", listKind, tc.jobID, tc.want)
			}
			if listKind != detailKind {
				t.Errorf("the two DTOs disagree about job %s: the list says %s, the detail says %s", tc.jobID, listKind, detailKind)
			}
		})
	}
}
