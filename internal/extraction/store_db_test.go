// DB-backed suite for the extraction store: per-role pools, TestMain-driven, one env-gated
// skip site. scripts/ci/rls-test-gate.sh fails a step on any skip, so stRequire must stay the
// only t.Skip in this package.
//
// Local run against this worktree's compose DB:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5434/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5434/invoice_os?sslmode=disable" \
//	go test -p 1 -count=1 ./internal/extraction/...
package extraction_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	stExtractor        = "extr-08-store"
	stExtractorVersion = "v1"
	stStoreSource      = "store.go"
)

var (
	stH       *stHarness
	stErrNoDB = errors.New("extraction store suite not configured")
)

type stHarness struct {
	app   *pgxpool.Pool
	super *pgxpool.Pool
}

// TestMain reads the environment once, before any test runs: contract_test.go's
// TestContractSuite_RunsWithoutDatabase clears both DSNs with t.Setenv, and a helper that
// re-read os.Getenv per test would start skipping because of it.
func TestMain(m *testing.M) {
	ctx := context.Background()
	h, err := stSetup(ctx)
	if err != nil && !errors.Is(err, stErrNoDB) {
		fmt.Fprintf(os.Stderr, "extraction store suite setup: %v\n", err)
		os.Exit(1)
	}
	stH = h

	code := m.Run()

	if stH != nil {
		stH.app.Close()
		stH.super.Close()
	}
	os.Exit(code)
}

func stSetup(ctx context.Context) (*stHarness, error) {
	appURL := os.Getenv("DATABASE_URL")
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		return nil, stErrNoDB
	}

	h := &stHarness{}
	for _, c := range []struct {
		dst **pgxpool.Pool
		url string
		who string
	}{
		{&h.super, superURL, "superuser"},
		{&h.app, appURL, "app"},
	} {
		pool, err := pgxpool.New(ctx, c.url)
		if err != nil {
			return nil, fmt.Errorf("connect %s: %w", c.who, err)
		}
		*c.dst = pool
	}
	// A DSN that is set but unreachable or unmigrated is an error, not a skip.
	if err := h.super.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping superuser (is the DB up and migrated?): %w", err)
	}
	return h, nil
}

// stRequire is this package's ONE skip site.
func stRequire(t *testing.T) *stHarness {
	t.Helper()
	if stH == nil {
		t.Skip("extraction store suite skipped: set DATABASE_URL (invoice_app) and " +
			"DATABASE_SUPERUSER_URL (fixtures and cross-check reads)")
	}
	return stH
}

func stStore(t *testing.T) *extraction.Store {
	t.Helper()
	return &extraction.Store{Pool: stRequire(t).app}
}

// stTenant seeds one tenant and one document as the superuser: invoice_app holds only SELECT
// on tenants. Teardown is one DELETE FROM tenants; the cascade reaches documents and
// extraction_jobs despite the RESTRICT between them.
func stTenant(t *testing.T, ctx context.Context) (tenantID, documentID string) {
	t.Helper()
	h := stRequire(t)

	tenantID = uuid.NewString()
	documentID = uuid.NewString()

	if _, err := h.super.Exec(ctx,
		`INSERT INTO tenants (id, name) VALUES ($1, $2)`,
		tenantID, "extr-08 "+tenantID[:8]); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		if _, err := h.super.Exec(context.Background(),
			`DELETE FROM tenants WHERE id = $1`, tenantID); err != nil {
			t.Errorf("teardown tenant %s: %v", tenantID, err)
		}
	})

	if _, err := h.super.Exec(ctx,
		`INSERT INTO documents (id, tenant_id, storage_key, content_hash, size_bytes)
		 VALUES ($1, $2, $3, $4, $5)`,
		documentID, tenantID, "extr-08/"+documentID, strings.Repeat("a", 64), 1024); err != nil {
		t.Fatalf("seed document: %v", err)
	}
	return tenantID, documentID
}

func stPtr(s string) *string { return &s }

func stAssertJobState(t *testing.T, ctx context.Context, jobID, want string) {
	t.Helper()
	var got string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT state FROM extraction_jobs WHERE id = $1`, jobID).Scan(&got); err != nil {
		t.Fatalf("read job %s state: %v", jobID, err)
	}
	if got != want {
		t.Errorf("job %s is in state %q, want %q", jobID, got, want)
	}
}

func stAssertFieldResultCount(t *testing.T, ctx context.Context, jobID string, want int) {
	t.Helper()
	var got int
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT count(*) FROM extraction_field_results WHERE extraction_job_id = $1`,
		jobID).Scan(&got); err != nil {
		t.Fatalf("count field results for job %s: %v", jobID, err)
	}
	if got != want {
		t.Errorf("job %s has %d field result row(s), want %d", jobID, got, want)
	}
}

func stJobLastError(t *testing.T, ctx context.Context, jobID string) *string {
	t.Helper()
	var got *string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT last_error FROM extraction_jobs WHERE id = $1`, jobID).Scan(&got); err != nil {
		t.Fatalf("read last_error for job %s: %v", jobID, err)
	}
	return got
}

// stLayoutRow is EXTR-14-03's two written columns, plus updated_at: W-05's replay oracle reads
// all three before and after a replay to prove none of them moved.
type stLayoutRow struct {
	Fingerprint *string
	Anchors     *string
	UpdatedAt   time.Time
}

func stJobLayout(t *testing.T, ctx context.Context, jobID string) stLayoutRow {
	t.Helper()
	var r stLayoutRow
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT layout_fingerprint, layout_anchors::text, updated_at FROM extraction_jobs WHERE id = $1`,
		jobID).Scan(&r.Fingerprint, &r.Anchors, &r.UpdatedAt); err != nil {
		t.Fatalf("read layout columns for job %s: %v", jobID, err)
	}
	return r
}

// stAssertStoreNeverNamesUpdatedAt is the load-bearing half of
// TestExtractionStore_AdvanceMovesStateAndUpdatedAt: the temporal assertion there cannot
// tell the trigger firing from the writer naming the column.
func stAssertStoreNeverNamesUpdatedAt(t *testing.T) {
	t.Helper()
	f, fset := mxParse(t, stStoreSource)

	var lits int
	ast.Inspect(f, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		lits++
		if strings.Contains(strings.ToLower(bl.Value), "updated_at") {
			t.Errorf("%s: store.go names updated_at in SQL; the BEFORE UPDATE trigger owns that column",
				fset.Position(bl.Pos()))
		}
		return true
	})
	if lits == 0 {
		t.Fatal("store.go holds no string literals, so this scan examined nothing")
	}
}

func TestExtractionStore_EnsureJobIsIdempotentPerRiverJob(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	// A sibling on the same tenant and document, created first and alone: a lookup that
	// drops the river_job_id predicate resolves this row for the call below.
	sibling, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 900101)
	if err != nil {
		t.Fatalf("EnsureJob sibling: %v", err)
	}

	first, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 900102)
	if err != nil {
		t.Fatalf("EnsureJob first: %v", err)
	}
	if first.ID == sibling.ID {
		t.Fatalf("EnsureJob for river_job_id 900102 returned the sibling job (river_job_id 900101) %s; the lookup does not filter on river_job_id", sibling.ID)
	}
	if first.State != "queued" || first.Attempts != 0 {
		t.Errorf("a new job reports {state %q, attempts %d}, want {queued, 0}", first.State, first.Attempts)
	}

	// Job carries neither column, so the insert's own extractor/extractor_version binding is
	// only reachable from the row: both CHECKs admit any non-empty string, so a swap is legal.
	var extractor, version string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT extractor, extractor_version FROM extraction_jobs WHERE id = $1`, first.ID).
		Scan(&extractor, &version); err != nil {
		t.Fatalf("read job %s extractor columns: %v", first.ID, err)
	}
	if extractor != stExtractor || version != stExtractorVersion {
		t.Errorf("the new job stored {extractor %q, extractor_version %q}, want {%q, %q}",
			extractor, version, stExtractor, stExtractorVersion)
	}

	// Move the row so the second call has something other than the insert defaults to report.
	if err := s.Advance(ctx, tenantID, first.ID, "extracting", "", 1); err != nil {
		t.Fatalf("Advance to extracting: %v", err)
	}

	second, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 900102)
	if err != nil {
		t.Fatalf("EnsureJob second: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("the second EnsureJob returned job %s, want the existing %s (the sibling is %s)",
			second.ID, first.ID, sibling.ID)
	}
	if second.State != "extracting" || second.Attempts != 1 {
		t.Errorf("the second EnsureJob reports {state %q, attempts %d}, want the stored {extracting, 1}",
			second.State, second.Attempts)
	}

	var rows int
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT count(*) FROM extraction_jobs WHERE tenant_id = $1 AND river_job_id = $2`,
		tenantID, 900102).Scan(&rows); err != nil {
		t.Fatalf("count jobs for river_job_id 900102: %v", err)
	}
	if rows != 1 {
		t.Errorf("river_job_id 900102 has %d job rows, want 1", rows)
	}
}

func TestExtractionStore_AdvanceMovesStateAndUpdatedAt(t *testing.T) {
	// Runs before the DB gate: it is the half that proves the writer did not do the trigger's
	// job, and it needs no database.
	stAssertStoreNeverNamesUpdatedAt(t)

	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 900201)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	if err := s.Advance(ctx, tenantID, job.ID, "failed", "boom", 2); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	var (
		state     string
		lastError *string
		attempts  int
		createdAt time.Time
		updatedAt time.Time
		moved     bool
	)
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT state, last_error, attempts, created_at, updated_at, updated_at > created_at
		   FROM extraction_jobs WHERE id = $1`, job.ID).
		Scan(&state, &lastError, &attempts, &createdAt, &updatedAt, &moved); err != nil {
		t.Fatalf("read job %s: %v", job.ID, err)
	}

	if state != "failed" {
		t.Errorf("state is %q, want failed", state)
	}
	if lastError == nil || *lastError != "boom" {
		t.Errorf("last_error is %v, want boom", lastError)
	}
	if attempts != 2 {
		t.Errorf("attempts is %d, want the 2 Advance was handed", attempts)
	}
	if !moved {
		t.Errorf("updated_at %s is not after created_at %s; the trigger did not fire",
			updatedAt.Format(time.RFC3339Nano), createdAt.Format(time.RFC3339Nano))
	}
}

func TestExtractionStore_WriteFieldResultsMapsReasonNoneToNull(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 900301)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	// The reason_code CHECK admits four words or NULL; a zero-length reason_code is 23514.
	if err := s.WriteFieldResults(ctx, tenantID, job.ID, []extraction.FieldResult{
		{Field: extraction.Field{Name: "invoice_number", Value: stPtr("INV-0001"), Reason: extraction.ReasonNone}},
		{Field: extraction.Field{Name: "buyer_tin", Reason: extraction.ReasonMissing}},
	}); err != nil {
		t.Fatalf("WriteFieldResults: %v", err)
	}

	read := func(field string) *string {
		t.Helper()
		var code *string
		if err := stRequire(t).super.QueryRow(ctx,
			`SELECT reason_code FROM extraction_field_results
			  WHERE extraction_job_id = $1 AND field_name = $2`, job.ID, field).Scan(&code); err != nil {
			t.Fatalf("read reason_code for %s: %v", field, err)
		}
		return code
	}

	if code := read("invoice_number"); code != nil {
		t.Errorf("ReasonNone stored reason_code %q, want SQL NULL", *code)
	}
	if code := read("buyer_tin"); code == nil || *code != "missing" {
		t.Errorf("ReasonMissing stored reason_code %v, want missing", code)
	}
}

func TestExtractionStore_WriteFieldResultsMapsNilRegionToNulls(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 900401)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	// The insert succeeding is _region_complete being satisfied: its first arm is the
	// all-NULL case. A zero page would be 23514 on page_check instead.
	if err := s.WriteFieldResults(ctx, tenantID, job.ID, []extraction.FieldResult{
		{Field: extraction.Field{Name: "total_amount", Value: stPtr("1000.00"), Reason: extraction.ReasonNone}},
	}); err != nil {
		t.Fatalf("WriteFieldResults with a nil Region: %v", err)
	}

	var (
		page           *int
		x0, y0, x1, y1 *float64
	)
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT page, bbox_x0, bbox_y0, bbox_x1, bbox_y1 FROM extraction_field_results
		  WHERE extraction_job_id = $1 AND field_name = $2`, job.ID, "total_amount").
		Scan(&page, &x0, &y0, &x1, &y1); err != nil {
		t.Fatalf("read region columns: %v", err)
	}

	if page != nil {
		t.Errorf("page is %d, want SQL NULL", *page)
	}
	for _, c := range []struct {
		col string
		got *float64
	}{{"bbox_x0", x0}, {"bbox_y0", y0}, {"bbox_x1", x1}, {"bbox_y1", y1}} {
		if c.got != nil {
			t.Errorf("%s is %v, want SQL NULL", c.col, *c.got)
		}
	}
}

func TestExtractionStore_WriteFieldResultsRoundTripsRegion(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 900501)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	want := extraction.Region{Page: 2, X0: 0.1, Y0: 0.2, X1: 0.3, Y1: 0.4}
	if err := s.WriteFieldResults(ctx, tenantID, job.ID, []extraction.FieldResult{
		{Field: extraction.Field{Name: "invoice_number", Value: stPtr("INV-0002"), Region: &want, Reason: extraction.ReasonNone}},
	}); err != nil {
		t.Fatalf("WriteFieldResults: %v", err)
	}

	// Raw columns first. A write-side column swap cancelled by a matching read-side swap
	// survives the struct round trip below; the four asymmetric values here do not let it.
	var (
		page           int
		x0, y0, x1, y1 float64
	)
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT page, bbox_x0, bbox_y0, bbox_x1, bbox_y1 FROM extraction_field_results
		  WHERE extraction_job_id = $1 AND field_name = $2`, job.ID, "invoice_number").
		Scan(&page, &x0, &y0, &x1, &y1); err != nil {
		t.Fatalf("read region columns: %v", err)
	}
	if page != want.Page {
		t.Errorf("page is %d, want %d", page, want.Page)
	}
	for _, c := range []struct {
		col       string
		got, want float64
	}{
		{"bbox_x0", x0, want.X0},
		{"bbox_y0", y0, want.Y0},
		{"bbox_x1", x1, want.X1},
		{"bbox_y1", y1, want.Y1},
	} {
		if c.got != c.want {
			t.Errorf("%s is %v, want %v", c.col, c.got, c.want)
		}
	}

	out, err := s.FieldResults(ctx, tenantID, job.ID)
	if err != nil {
		t.Fatalf("FieldResults: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("FieldResults returned %d rows, want 1", len(out))
	}
	if out[0].Region == nil {
		t.Fatal("FieldResults returned a nil Region for a row with five populated columns")
	}
	if *out[0].Region != want {
		t.Errorf("FieldResults returned %+v, want %+v", *out[0].Region, want)
	}
}

func TestRLS_ExtractionStoreCannotWriteAcrossTenants(t *testing.T) {
	ctx := t.Context()
	h := stRequire(t)
	s := stStore(t)

	tenantA, _ := stTenant(t, ctx)
	tenantB, documentB := stTenant(t, ctx)

	jobB, err := s.EnsureJob(ctx, tenantB, documentB, stExtractor, stExtractorVersion, 900601)
	if err != nil {
		t.Fatalf("EnsureJob for tenant B: %v", err)
	}

	// The store's own surface. Advance is refused by its rows-affected guard and
	// WriteFieldResults by the composite FK -- neither consults a policy.
	if err := s.Advance(ctx, tenantA, jobB.ID, "failed", "boom", 1); err == nil {
		t.Error("Advance scoped to tenant A accepted tenant B's job id and reported no error")
	}
	if err := s.WriteFieldResults(ctx, tenantA, jobB.ID, []extraction.FieldResult{
		{Field: extraction.Field{Name: "invoice_number", Value: stPtr("LEAK"), Reason: extraction.ReasonNone}},
	}); err == nil {
		t.Error("WriteFieldResults scoped to tenant A accepted tenant B's job id and reported no error")
	}
	stAssertJobState(t, ctx, jobB.ID, "queued")
	stAssertFieldResultCount(t, ctx, jobB.ID, 0)

	// The policy itself. Neither write below carries a tenant_id predicate the store could
	// refuse it with, so only row-level security can: with RLS disabled the UPDATE touches
	// one row and the INSERT succeeds, while every assertion above stays green.
	var affected int64
	if err := db.WithinTenantTx(ctx, h.app, tenantA, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE extraction_jobs SET state = 'failed' WHERE id = $1`, jobB.ID)
		affected = tag.RowsAffected()
		return err
	}); err != nil {
		t.Fatalf("unscoped cross-tenant UPDATE: %v", err)
	}
	if affected != 0 {
		t.Errorf("an UPDATE naming only tenant B's job id touched %d row(s) under tenant A", affected)
	}
	stAssertJobState(t, ctx, jobB.ID, "queued")

	// tenant_isolation carries USING with no WITH CHECK, so the USING clause doubles as one:
	// a row naming tenant B whose composite FK is perfectly satisfied is still refused.
	err = db.WithinTenantTx(ctx, h.app, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO extraction_field_results (tenant_id, extraction_job_id, field_name)
			 VALUES ($1, $2, $3)`, tenantB, jobB.ID, "planted")
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Errorf("planting a tenant-B field result under tenant A returned %v, want SQLSTATE 42501", err)
	}
	stAssertFieldResultCount(t, ctx, jobB.ID, 0)
}

func TestExtractionStore_ReadReturnsEmptySliceNotNil(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 900701)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	// append makes any non-empty read non-nil whatever the initialiser, so a job with zero
	// field results is the only shape that can catch a bare var declaration.
	out, err := s.FieldResults(ctx, tenantID, job.ID)
	if err != nil {
		t.Fatalf("FieldResults: %v", err)
	}
	if out == nil {
		t.Error("FieldResults returned a nil slice for a job with zero results; nil marshals to null, not to an empty array")
	}
	if len(out) != 0 {
		t.Errorf("FieldResults returned %d rows for a job with zero results", len(out))
	}

	// The error path is the other half of never-nil: db.WithinTenantTx refuses a non-UUID
	// tenant before it issues a statement, so the helper's initialiser never runs and the
	// exported wrapper's own is all that stands between a caller and a JSON null.
	out, err = s.FieldResults(ctx, "not-a-uuid", job.ID)
	if err == nil {
		t.Fatal("FieldResults accepted a non-UUID tenant and reported no error")
	}
	if out == nil {
		t.Errorf("FieldResults returned a nil slice alongside its error %v, want an empty slice", err)
	}
	if len(out) != 0 {
		t.Errorf("FieldResults returned %d rows alongside its error", len(out))
	}
}

func TestExtractionStore_UsesTenantTxNotRequestTx(t *testing.T) {
	f, fset := mxParse(t, stStoreSource)

	var tenantTx int
	ast.Inspect(f, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if strings.HasPrefix(id.Name, "WithinTenantTx") {
			tenantTx++
		}
		// Prefix, not exact name: WithinRequestTenantTxOpts is the twin an exact match misses.
		if strings.HasPrefix(id.Name, "WithinRequest") {
			t.Errorf("%s: store.go names %s; the extraction worker has no request identity",
				fset.Position(id.Pos()), id.Name)
		}
		return true
	})
	if tenantTx == 0 {
		t.Fatal("store.go names no WithinTenantTx at all, so the scan above passed vacuously")
	}
}

func TestExtractionStore_AdvanceClearsLastErrorToNull(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 900801)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	// The row must actually carry an error first, or the retry below proves nothing: a
	// last_error that was NULL all along reads NULL whatever Advance binds.
	if err := s.Advance(ctx, tenantID, job.ID, "failed", "boom", 1); err != nil {
		t.Fatalf("Advance to failed: %v", err)
	}
	if got := stJobLastError(t, ctx, job.ID); got == nil || *got != "boom" {
		t.Fatalf("after a failing advance last_error is %v, want boom", got)
	}

	// last_error is a bare text column with no CHECK, so binding lastErr unconditionally
	// stores the empty string as a silent sentinel every reader would have to know about.
	if err := s.Advance(ctx, tenantID, job.ID, "extracting", "", 2); err != nil {
		t.Fatalf("Advance to extracting: %v", err)
	}
	if got := stJobLastError(t, ctx, job.ID); got != nil {
		t.Errorf("an empty lastErr stored last_error %q, want SQL NULL", *got)
	}
	stAssertJobState(t, ctx, job.ID, "extracting")
}

func TestExtractionStore_FieldResultsRoundTripValueAndReason(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 900901)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	// created_at is now(), which is transaction-start time and so identical for every row one
	// WriteFieldResults writes: field_name is what orders a batch, and these are in that order.
	want := []extraction.FieldResult{
		{Field: extraction.Field{Name: "buyer_tin", Reason: extraction.ReasonMissing}},
		{Field: extraction.Field{Name: "invoice_number", Value: stPtr("INV-0003"), Reason: extraction.ReasonNone}},
		{Field: extraction.Field{Name: "total_amount", Value: stPtr("1000.00"), Reason: extraction.ReasonAmbiguous}},
	}
	if err := s.WriteFieldResults(ctx, tenantID, job.ID, want); err != nil {
		t.Fatalf("WriteFieldResults: %v", err)
	}

	out, err := s.FieldResults(ctx, tenantID, job.ID)
	if err != nil {
		t.Fatalf("FieldResults: %v", err)
	}
	if len(out) != len(want) {
		t.Fatalf("FieldResults returned %d rows, want %d", len(out), len(want))
	}

	for i, w := range want {
		got := out[i]
		if got.Name != w.Name {
			t.Errorf("row %d is %q, want %q; the read is ORDER BY created_at, field_name", i, got.Name, w.Name)
			continue
		}
		switch {
		case w.Value == nil && got.Value != nil:
			t.Errorf("%s came back with value %q, want nil", w.Name, *got.Value)
		case w.Value != nil && got.Value == nil:
			t.Errorf("%s came back with a nil value, want %q", w.Name, *w.Value)
		case w.Value != nil && *got.Value != *w.Value:
			t.Errorf("%s came back with value %q, want %q", w.Name, *got.Value, *w.Value)
		}
		if got.Reason != w.Reason {
			t.Errorf("%s came back with reason %q, want %q", w.Name, got.Reason, w.Reason)
		}
	}
}

func TestExtractionStore_FieldResultsAreScopedToOneJob(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	// Two jobs on ONE tenant: row-level security scopes the read to the tenant, so only the
	// query's own extraction_job_id predicate can keep the sibling job's rows out.
	first, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 901001)
	if err != nil {
		t.Fatalf("EnsureJob first: %v", err)
	}
	second, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 901002)
	if err != nil {
		t.Fatalf("EnsureJob second: %v", err)
	}

	if err := s.WriteFieldResults(ctx, tenantID, first.ID, []extraction.FieldResult{
		{Field: extraction.Field{Name: "invoice_number", Value: stPtr("INV-FIRST"), Reason: extraction.ReasonNone}},
	}); err != nil {
		t.Fatalf("WriteFieldResults for the first job: %v", err)
	}
	if err := s.WriteFieldResults(ctx, tenantID, second.ID, []extraction.FieldResult{
		{Field: extraction.Field{Name: "invoice_number", Value: stPtr("INV-SECOND"), Reason: extraction.ReasonNone}},
	}); err != nil {
		t.Fatalf("WriteFieldResults for the second job: %v", err)
	}

	out, err := s.FieldResults(ctx, tenantID, first.ID)
	if err != nil {
		t.Fatalf("FieldResults: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("FieldResults for job %s returned %d rows, want only its own 1", first.ID, len(out))
	}
	if out[0].Value == nil || *out[0].Value != "INV-FIRST" {
		t.Errorf("FieldResults for job %s returned value %v, want INV-FIRST", first.ID, out[0].Value)
	}
}

// TestExtractionStore_WritesTheTextLayerVerdict (CONFIRMATORY): the exact shape
// PDFiumExtractor emits for a scan -- no value, no box, reason 'unreadable'. No other spec
// writes that reason, and the all-NULL region arm of _region_complete is what admits it.
func TestExtractionStore_WritesTheTextLayerVerdict(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 901101)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	if err := s.WriteFieldResults(ctx, tenantID, job.ID, []extraction.FieldResult{
		{Field: extraction.Field{Name: "document_text_layer", Reason: extraction.ReasonUnreadable}},
	}); err != nil {
		t.Fatalf("WriteFieldResults for the text-layer verdict: %v", err)
	}

	var (
		value, reason  *string
		page           *int
		x0, y0, x1, y1 *float64
	)
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT value, reason_code, page, bbox_x0, bbox_y0, bbox_x1, bbox_y1
		   FROM extraction_field_results
		  WHERE extraction_job_id = $1 AND field_name = $2`, job.ID, "document_text_layer").
		Scan(&value, &reason, &page, &x0, &y0, &x1, &y1); err != nil {
		t.Fatalf("read the text-layer row: %v", err)
	}

	if reason == nil || *reason != "unreadable" {
		t.Errorf("reason_code is %v, want unreadable", reason)
	}
	if value != nil {
		t.Errorf("value is %q, want SQL NULL", *value)
	}
	if page != nil {
		t.Errorf("page is %d, want SQL NULL", *page)
	}
	for _, c := range []struct {
		col string
		got *float64
	}{{"bbox_x0", x0}, {"bbox_y0", y0}, {"bbox_x1", x1}, {"bbox_y1", y1}} {
		if c.got != nil {
			t.Errorf("%s is %v, want SQL NULL", c.col, *c.got)
		}
	}
}

// --- EXTR-05-06: ranked field results and their alternatives --------------------------

// AC-1: a decided field with no alternatives writes exactly one row, at rank 0.
func TestRLS_WriteFieldResultsWritesRankZeroForTheDecidedField(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 902001)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	if err := s.WriteFieldResults(ctx, tenantID, job.ID, []extraction.FieldResult{
		{Field: extraction.Field{Name: "invoice_number", Value: stPtr("INV-RANK0"), Reason: extraction.ReasonNone}},
	}); err != nil {
		t.Fatalf("WriteFieldResults: %v", err)
	}

	var rank int
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT candidate_rank FROM extraction_field_results
		  WHERE extraction_job_id = $1 AND field_name = $2`, job.ID, "invoice_number").Scan(&rank); err != nil {
		t.Fatalf("read candidate_rank: %v", err)
	}
	if rank != 0 {
		t.Errorf("candidate_rank is %d, want 0 for a decided field with no alternatives", rank)
	}
}

// AC-1: one ambiguous FieldResult with 2 alternatives writes 3 rows, ranked 0,1,2 in slice
// order -- the decided reading first, then each alternative in the order Reconcile produced it.
func TestRLS_WriteFieldResultsRanksAlternativesInSliceOrder(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 902101)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	fr := extraction.FieldResult{
		Field: extraction.Field{Name: "issue_date", Value: stPtr("2026-03-12"), Reason: extraction.ReasonAmbiguous},
		Alternatives: []extraction.Field{
			{Name: "issue_date", Value: stPtr("2026-12-03")},
			{Name: "issue_date", Value: stPtr("2026-03-20")},
		},
	}
	if err := s.WriteFieldResults(ctx, tenantID, job.ID, []extraction.FieldResult{fr}); err != nil {
		t.Fatalf("WriteFieldResults: %v", err)
	}

	rows, err := stRequire(t).super.Query(ctx,
		`SELECT candidate_rank, value FROM extraction_field_results
		  WHERE extraction_job_id = $1 AND field_name = $2 ORDER BY candidate_rank`,
		job.ID, "issue_date")
	if err != nil {
		t.Fatalf("read ranked rows: %v", err)
	}
	defer rows.Close()

	type rankedRow struct {
		rank  int
		value *string
	}
	var got []rankedRow
	for rows.Next() {
		var r rankedRow
		if err := rows.Scan(&r.rank, &r.value); err != nil {
			t.Fatalf("scan ranked row: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read ranked rows: %v", err)
	}

	want := []string{"2026-03-12", "2026-12-03", "2026-03-20"}
	if len(got) != len(want) {
		t.Fatalf("wrote %d row(s) for one ambiguous field with 2 alternatives, want %d (ranks 0,1,2)", len(got), len(want))
	}
	for i, w := range want {
		if got[i].rank != i {
			t.Errorf("row %d has candidate_rank %d, want %d", i, got[i].rank, i)
			continue
		}
		if got[i].value == nil || *got[i].value != w {
			t.Errorf("rank %d value is %v, want %q", i, got[i].value, w)
		}
	}
}

// AC-2/3: a written ambiguous field round-trips as ONE FieldResult with its alternatives
// attached, not as separate rows -- value, region and reason all survive for both the decided
// reading and its alternative.
func TestRLS_FieldResultsRoundTripsAnAmbiguousField(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 902201)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	decidedRegion := extraction.Region{Page: 1, X0: 0.1, Y0: 0.1, X1: 0.2, Y1: 0.2}
	altRegion := extraction.Region{Page: 1, X0: 0.3, Y0: 0.3, X1: 0.4, Y1: 0.4}
	want := extraction.FieldResult{
		Field: extraction.Field{Name: "issue_date", Value: stPtr("2026-03-12"), Region: &decidedRegion, Reason: extraction.ReasonAmbiguous},
		Alternatives: []extraction.Field{
			{Name: "issue_date", Value: stPtr("2026-12-03"), Region: &altRegion},
		},
	}
	if err := s.WriteFieldResults(ctx, tenantID, job.ID, []extraction.FieldResult{want}); err != nil {
		t.Fatalf("WriteFieldResults: %v", err)
	}

	out, err := s.FieldResults(ctx, tenantID, job.ID)
	if err != nil {
		t.Fatalf("FieldResults: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("FieldResults returned %d result(s) for one ambiguous field, want 1 (grouped)", len(out))
	}
	got := out[0]
	if got.Reason != extraction.ReasonAmbiguous {
		t.Errorf("decided reading reason is %q, want ambiguous", got.Reason)
	}
	if got.Value == nil || *got.Value != *want.Value {
		t.Errorf("decided reading value is %v, want %q", got.Value, *want.Value)
	}
	if got.Region == nil || *got.Region != decidedRegion {
		t.Errorf("decided reading region is %v, want %+v", got.Region, decidedRegion)
	}
	if len(got.Alternatives) != 1 {
		t.Fatalf("FieldResults returned %d alternative(s), want 1", len(got.Alternatives))
	}
	alt := got.Alternatives[0]
	if alt.Value == nil || *alt.Value != *want.Alternatives[0].Value {
		t.Errorf("alternative value is %v, want %q", alt.Value, *want.Alternatives[0].Value)
	}
	if alt.Region == nil || *alt.Region != altRegion {
		t.Errorf("alternative region is %v, want %+v", alt.Region, altRegion)
	}
}

// AC-2: candidate_rank is the read's own tiebreak, independent of insertion order -- rows
// planted at ranks 2,0,1 must still read back grouped with the alternatives in rank order.
// All three rows are seeded in one transaction, sharing one created_at, exactly as one real
// WriteFieldResults call would (a real write is one transaction) -- three separate Execs
// would give each row its own created_at, a shape production never produces.
func TestRLS_FieldResultsOrdersByCandidateRank(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	h := stRequire(t)
	tenantID, documentID := stTenant(t, ctx)

	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 902301)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	tx, err := h.super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed tx: %v", err)
	}
	defer tx.Rollback(ctx)
	for _, row := range []struct {
		rank  int
		value string
	}{{2, "third"}, {0, "first"}, {1, "second"}} {
		if _, err := tx.Exec(ctx,
			`INSERT INTO extraction_field_results (tenant_id, extraction_job_id, field_name, value, candidate_rank)
			 VALUES ($1, $2, $3, $4, $5)`,
			tenantID, job.ID, "total_amount", row.value, row.rank); err != nil {
			t.Fatalf("seed rank %d: %v", row.rank, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed tx: %v", err)
	}

	out, err := s.FieldResults(ctx, tenantID, job.ID)
	if err != nil {
		t.Fatalf("FieldResults: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("FieldResults returned %d result(s) for one field written at three ranks, want 1 (grouped)", len(out))
	}
	got := out[0]
	if got.Value == nil || *got.Value != "first" {
		t.Errorf("decided reading (rank 0) value is %v, want %q", got.Value, "first")
	}
	if len(got.Alternatives) != 2 {
		t.Fatalf("FieldResults returned %d alternative(s), want 2 (ranks 1 and 2)", len(got.Alternatives))
	}
	wantAlts := []string{"second", "third"}
	for i, w := range wantAlts {
		if got.Alternatives[i].Value == nil || *got.Alternatives[i].Value != w {
			t.Errorf("alternative %d value is %v, want %q -- the read must order by candidate_rank, not insertion order", i, got.Alternatives[i].Value, w)
		}
	}
}

// AC-4: a job with no rows at all still returns a non-nil, zero-length slice.
func TestRLS_FieldResultsOnAnEmptyJobIsEmptyNotNil(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 902401)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	out, err := s.FieldResults(ctx, tenantID, job.ID)
	if err != nil {
		t.Fatalf("FieldResults: %v", err)
	}
	if out == nil {
		t.Fatal("FieldResults returned a nil slice for a job with zero results, want a non-nil empty slice")
	}
	if len(out) != 0 {
		t.Errorf("FieldResults returned %d result(s) for a job with zero rows, want 0", len(out))
	}
}

// AC-4: Alternatives marshals as "[]", never "null" -- a nil slice would read as absent data
// to any consumer, not as "no alternatives".
func TestRLS_FieldResultAlternativesMarshalAsArrayNotNull(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 902501)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}
	if err := s.WriteFieldResults(ctx, tenantID, job.ID, []extraction.FieldResult{
		{Field: extraction.Field{Name: "invoice_number", Value: stPtr("INV-JSON"), Reason: extraction.ReasonNone}},
	}); err != nil {
		t.Fatalf("WriteFieldResults: %v", err)
	}

	out, err := s.FieldResults(ctx, tenantID, job.ID)
	if err != nil {
		t.Fatalf("FieldResults: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("FieldResults returned %d result(s), want 1", len(out))
	}
	if out[0].Alternatives == nil {
		t.Fatal("FieldResults returned a nil Alternatives slice, want non-nil empty")
	}

	raw, err := json.Marshal(out[0])
	if err != nil {
		t.Fatalf("marshal FieldResult: %v", err)
	}
	if !strings.Contains(string(raw), `"alternatives":[]`) {
		t.Errorf("marshaled FieldResult = %s, want the literal substring \"alternatives\":[]", raw)
	}
}

// AC-5: ReasonNone and a nil Region still bind SQL NULL through the []FieldResult signature.
func TestRLS_ReasonNoneAndNilRegionBindNull(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 902601)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	if err := s.WriteFieldResults(ctx, tenantID, job.ID, []extraction.FieldResult{
		{Field: extraction.Field{Name: "supplier_tin", Value: stPtr("MOCK-TIN"), Reason: extraction.ReasonNone}},
	}); err != nil {
		t.Fatalf("WriteFieldResults: %v", err)
	}

	var (
		reason         *string
		page           *int
		x0, y0, x1, y1 *float64
	)
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT reason_code, page, bbox_x0, bbox_y0, bbox_x1, bbox_y1 FROM extraction_field_results
		  WHERE extraction_job_id = $1 AND field_name = $2`, job.ID, "supplier_tin").
		Scan(&reason, &page, &x0, &y0, &x1, &y1); err != nil {
		t.Fatalf("read the row: %v", err)
	}
	if reason != nil {
		t.Errorf("reason_code is %q, want SQL NULL for ReasonNone", *reason)
	}
	if page != nil {
		t.Errorf("page is %d, want SQL NULL for a nil Region", *page)
	}
	for _, c := range []struct {
		col string
		got *float64
	}{{"bbox_x0", x0}, {"bbox_y0", y0}, {"bbox_x1", x1}, {"bbox_y1", y1}} {
		if c.got != nil {
			t.Errorf("%s is %v, want SQL NULL", c.col, *c.got)
		}
	}
}

// The read-order invariant: created_at is transaction-start time, identical for every row one
// WriteFieldResults call writes, so (field_name, candidate_rank) is what must resolve the read
// -- an application invariant, not a DB constraint (no unique index exists). Fields are
// written out of field_name order on purpose, so an ORDER BY that dropped field_name in favour
// of insertion order could not pass by accident.
func TestRLS_FieldResultsReadOrderPinsFieldNameThenCandidateRank(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 902701)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	write := []extraction.FieldResult{
		{Field: extraction.Field{Name: "total_amount", Value: stPtr("1000.00"), Reason: extraction.ReasonNone}},
		{Field: extraction.Field{Name: "buyer_tin", Value: stPtr("B-1"), Reason: extraction.ReasonNone}},
		{
			Field: extraction.Field{Name: "invoice_number", Value: stPtr("INV-A"), Reason: extraction.ReasonAmbiguous},
			Alternatives: []extraction.Field{
				{Name: "invoice_number", Value: stPtr("INV-B")},
				{Name: "invoice_number", Value: stPtr("INV-C")},
			},
		},
	}
	if err := s.WriteFieldResults(ctx, tenantID, job.ID, write); err != nil {
		t.Fatalf("WriteFieldResults: %v", err)
	}

	out, err := s.FieldResults(ctx, tenantID, job.ID)
	if err != nil {
		t.Fatalf("FieldResults: %v", err)
	}
	if len(out) != len(write) {
		t.Fatalf("FieldResults returned %d result(s), want %d (one per field, grouped)", len(out), len(write))
	}

	wantNames := []string{"buyer_tin", "invoice_number", "total_amount"}
	for i, name := range wantNames {
		if out[i].Name != name {
			t.Fatalf("result %d is %q, want %q -- the read must order by field_name when every row shares one created_at", i, out[i].Name, name)
		}
	}

	inv := out[1]
	if len(inv.Alternatives) != 2 {
		t.Fatalf("invoice_number carries %d alternative(s), want 2", len(inv.Alternatives))
	}
	wantAlts := []string{"INV-B", "INV-C"}
	for i, w := range wantAlts {
		if inv.Alternatives[i].Value == nil || *inv.Alternatives[i].Value != w {
			t.Errorf("invoice_number alternative %d is %v, want %q -- alternatives order by candidate_rank", i, inv.Alternatives[i].Value, w)
		}
	}
	if inv.Value == nil || *inv.Value != "INV-A" {
		t.Errorf("invoice_number decided value is %v, want %q", inv.Value, "INV-A")
	}
}

// The orphan invariant: a rank>0 row whose field_name has no rank-0 sibling is a shape
// Reconcile never produces, but nothing in the schema forbids it. FieldResults must refuse it
// rather than silently drop it (losing audit data) or promote it (fabricating a reading
// Reconcile never made).
func TestRLS_FieldResultsOrphanAlternativeIsAHardError(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	h := stRequire(t)
	tenantID, documentID := stTenant(t, ctx)

	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 902801)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	if _, err := h.super.Exec(ctx,
		`INSERT INTO extraction_field_results (tenant_id, extraction_job_id, field_name, value, candidate_rank)
		 VALUES ($1, $2, $3, $4, $5)`,
		tenantID, job.ID, "issue_date", "2026-12-03", 1); err != nil {
		t.Fatalf("seed the orphan alternative: %v", err)
	}

	out, err := s.FieldResults(ctx, tenantID, job.ID)
	if err == nil {
		t.Fatal("FieldResults accepted a rank>0 row with no rank-0 sibling and reported no error")
	}
	if len(out) != 0 {
		t.Errorf("FieldResults returned %d result(s) alongside its error, want an empty slice", len(out))
	}
}

// A second WriteFieldResults call landing a second rank-0 row for a field_name already
// decided in this job must not cross-attach alternatives: each write's own alternative stays
// with that write's decided value (A keeps X, B keeps Y), because created_at leads the read
// order and separates the two writes' rows before field_name/candidate_rank group within one.
func TestRLS_FieldResultsTwoWritesToSameJobKeepAlternativesWithTheirOwnWrite(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 902901)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	if err := s.WriteFieldResults(ctx, tenantID, job.ID, []extraction.FieldResult{
		{
			Field:        extraction.Field{Name: "invoice_number", Value: stPtr("A"), Reason: extraction.ReasonAmbiguous},
			Alternatives: []extraction.Field{{Name: "invoice_number", Value: stPtr("X")}},
		},
	}); err != nil {
		t.Fatalf("first WriteFieldResults: %v", err)
	}
	if err := s.WriteFieldResults(ctx, tenantID, job.ID, []extraction.FieldResult{
		{
			Field:        extraction.Field{Name: "invoice_number", Value: stPtr("B"), Reason: extraction.ReasonAmbiguous},
			Alternatives: []extraction.Field{{Name: "invoice_number", Value: stPtr("Y")}},
		},
	}); err != nil {
		t.Fatalf("second WriteFieldResults: %v", err)
	}

	out, err := s.FieldResults(ctx, tenantID, job.ID)
	if err != nil {
		t.Fatalf("FieldResults: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d FieldResult(s) for two rank-0 writes of the same field_name, want 2 (kept as separate groups)", len(out))
	}
	first, second := out[0], out[1]
	if first.Value == nil || *first.Value != "A" || len(first.Alternatives) != 1 || first.Alternatives[0].Value == nil || *first.Alternatives[0].Value != "X" {
		t.Errorf("first group = value %v, alternatives %v; want A with its own alternative X", first.Value, first.Alternatives)
	}
	if second.Value == nil || *second.Value != "B" || len(second.Alternatives) != 1 || second.Alternatives[0].Value == nil || *second.Alternatives[0].Value != "Y" {
		t.Errorf("second group = value %v, alternatives %v; want B with its own alternative Y", second.Value, second.Alternatives)
	}
}

// stPageKey is the shape extraction_page_images_key_tenant_scoped admits. uuid::text
// renders lowercase and the CHECK compares bytes, so the tenant segment is never
// case-transformed.
func stPageKey(tenantID, hash string, page int) string {
	return fmt.Sprintf("tenants/%s/pages/%s/v1/p%04d.png", tenantID, hash, page)
}

// stDocument seeds one extra document under an existing tenant. stTenant's own document is
// the only other one; a second is what proves the whole-set replace is document-scoped.
func stDocument(t *testing.T, ctx context.Context, tenantID string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO documents (id, tenant_id, storage_key, content_hash, size_bytes)
		 VALUES ($1, $2, $3, $4, $5)`,
		id, tenantID, "extr-08/"+id, strings.Repeat("b", 64), 2048); err != nil {
		t.Fatalf("seed second document: %v", err)
	}
	return id
}

// stPageRows returns a document's stored page images in page order, as the superuser.
func stPageRows(t *testing.T, ctx context.Context, documentID string) []extraction.PageImage {
	t.Helper()
	rows, err := stRequire(t).super.Query(ctx,
		`SELECT page_number, width_px, height_px, storage_key FROM extraction_page_images
		  WHERE document_id = $1 ORDER BY page_number`, documentID)
	if err != nil {
		t.Fatalf("read page images for document %s: %v", documentID, err)
	}
	defer rows.Close()

	out := []extraction.PageImage{}
	for rows.Next() {
		var p extraction.PageImage
		if err := rows.Scan(&p.Page, &p.WidthPx, &p.HeightPx, &p.StorageKey); err != nil {
			t.Fatalf("scan page image for document %s: %v", documentID, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read page images for document %s: %v", documentID, err)
	}
	return out
}

// D-20. A retry re-renders and replaces; it never appends. The second document is the
// scope oracle: a DELETE that drops the document_id predicate empties it too, and the
// count for the first document alone cannot see that.
func TestExtractionStore_WritePageImagesReplacesTheWholeSet(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)
	otherDoc := stDocument(t, ctx, tenantID)

	first := []extraction.PageImage{
		{Page: 1, WidthPx: 1275, HeightPx: 1651, StorageKey: stPageKey(tenantID, "aaa", 1)},
		{Page: 2, WidthPx: 1275, HeightPx: 1651, StorageKey: stPageKey(tenantID, "aaa", 2)},
		{Page: 3, WidthPx: 1275, HeightPx: 1651, StorageKey: stPageKey(tenantID, "aaa", 3)},
	}
	if err := s.WritePageImages(ctx, tenantID, documentID, first); err != nil {
		t.Fatalf("WritePageImages (3 pages): %v", err)
	}
	if got := stPageRows(t, ctx, documentID); len(got) != 3 {
		t.Fatalf("the document holds %d page rows after the first write, want 3", len(got))
	}

	other := []extraction.PageImage{
		{Page: 1, WidthPx: 1275, HeightPx: 1651, StorageKey: stPageKey(tenantID, "bbb", 1)},
		{Page: 2, WidthPx: 1275, HeightPx: 1651, StorageKey: stPageKey(tenantID, "bbb", 2)},
	}
	if err := s.WritePageImages(ctx, tenantID, otherDoc, other); err != nil {
		t.Fatalf("WritePageImages for the second document: %v", err)
	}

	// A shorter re-render. Without the DELETE this is 23505 on page 1; with a DELETE that
	// forgot document_id the second document below is empty.
	replacement := []extraction.PageImage{
		{Page: 1, WidthPx: 1275, HeightPx: 1651, StorageKey: stPageKey(tenantID, "ccc", 1)},
	}
	if err := s.WritePageImages(ctx, tenantID, documentID, replacement); err != nil {
		t.Fatalf("WritePageImages (replacing 3 pages with 1): %v", err)
	}

	got := stPageRows(t, ctx, documentID)
	if len(got) != 1 {
		t.Fatalf("the document holds %d page rows after the replace, want 1 — the write appended", len(got))
	}
	if got[0] != replacement[0] {
		t.Errorf("the surviving row is %+v, want %+v", got[0], replacement[0])
	}
	if n := len(stPageRows(t, ctx, otherDoc)); n != 2 {
		t.Errorf("the second document holds %d page rows, want 2 — the replace was not document-scoped", n)
	}
}

// D-20: N attempts over one document converge on the page count, never a multiple of it.
func TestExtractionStore_WritePageImagesIsIdempotentAcrossRetries(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	pages := []extraction.PageImage{
		{Page: 1, WidthPx: 1275, HeightPx: 1651, StorageKey: stPageKey(tenantID, "ddd", 1)},
		{Page: 2, WidthPx: 1275, HeightPx: 1651, StorageKey: stPageKey(tenantID, "ddd", 2)},
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if err := s.WritePageImages(ctx, tenantID, documentID, pages); err != nil {
			t.Fatalf("WritePageImages attempt %d: %v", attempt, err)
		}
		got := stPageRows(t, ctx, documentID)
		if len(got) != len(pages) {
			t.Fatalf("after attempt %d the document holds %d page rows, want %d", attempt, len(got), len(pages))
		}
	}
}

// The render's own dimensions, per page. Width and height differ and page 2 differs from
// page 1, so a column swap or a per-page value read off the wrong row cannot survive.
func TestExtractionStore_WritePageImagesStoresDimensionsAndKey(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	want := []extraction.PageImage{
		{Page: 1, WidthPx: 1275, HeightPx: 1651, StorageKey: stPageKey(tenantID, "eee", 1)},
		{Page: 2, WidthPx: 1754, HeightPx: 1240, StorageKey: stPageKey(tenantID, "eee", 2)},
	}
	if err := s.WritePageImages(ctx, tenantID, documentID, want); err != nil {
		t.Fatalf("WritePageImages: %v", err)
	}

	got := stPageRows(t, ctx, documentID)
	if len(got) != len(want) {
		t.Fatalf("the document holds %d page rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("page %d stored %+v, want %+v", want[i].Page, got[i], want[i])
		}
	}
}

// An empty set is the replace with nothing to insert. Nothing branches on it, so the DELETE
// still runs and the inventory reads empty rather than stale.
func TestExtractionStore_WritePageImagesEmptySetClearsTheInventory(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	if err := s.WritePageImages(ctx, tenantID, documentID, []extraction.PageImage{
		{Page: 1, WidthPx: 1275, HeightPx: 1651, StorageKey: stPageKey(tenantID, "fff", 1)},
		{Page: 2, WidthPx: 1275, HeightPx: 1651, StorageKey: stPageKey(tenantID, "fff", 2)},
	}); err != nil {
		t.Fatalf("WritePageImages (2 pages): %v", err)
	}

	if err := s.WritePageImages(ctx, tenantID, documentID, nil); err != nil {
		t.Fatalf("WritePageImages (empty set): %v", err)
	}
	if got := stPageRows(t, ctx, documentID); len(got) != 0 {
		t.Errorf("the document holds %d page rows after an empty write, want 0", len(got))
	}
}

// extraction_page_images_key_tenant_scoped, reached through the store: a key outside the
// tenant's own prefix is refused even though the row's tenant_id is the caller's own.
func TestExtractionStore_WritePageImagesRefusesAKeyOutsideTheTenantPrefix(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)
	otherTenant := uuid.NewString()

	if err := s.WritePageImages(ctx, tenantID, documentID, []extraction.PageImage{
		{Page: 1, WidthPx: 1275, HeightPx: 1651, StorageKey: stPageKey(otherTenant, "ggg", 1)},
	}); err == nil {
		t.Error("WritePageImages accepted a storage_key under another tenant's prefix and reported no error")
	}
	if got := stPageRows(t, ctx, documentID); len(got) != 0 {
		t.Errorf("the document holds %d page rows after the refused key, want 0", len(got))
	}
}

// The DELETE leg matters as much as the INSERT: a whole-set replace scoped to the wrong
// tenant would empty that tenant's inventory before failing on the insert.
func TestRLS_ExtractionStoreCannotWritePageImagesAcrossTenants(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)

	tenantA, _ := stTenant(t, ctx)
	tenantB, documentB := stTenant(t, ctx)

	seeded := []extraction.PageImage{
		{Page: 1, WidthPx: 1275, HeightPx: 1651, StorageKey: stPageKey(tenantB, "hhh", 1)},
		{Page: 2, WidthPx: 1275, HeightPx: 1651, StorageKey: stPageKey(tenantB, "hhh", 2)},
	}
	if err := s.WritePageImages(ctx, tenantB, documentB, seeded); err != nil {
		t.Fatalf("WritePageImages for tenant B: %v", err)
	}

	if err := s.WritePageImages(ctx, tenantA, documentB, []extraction.PageImage{
		{Page: 1, WidthPx: 1275, HeightPx: 1651, StorageKey: stPageKey(tenantA, "iii", 1)},
	}); err == nil {
		t.Error("WritePageImages scoped to tenant A accepted tenant B's document id and reported no error")
	}

	got := stPageRows(t, ctx, documentB)
	if len(got) != len(seeded) {
		t.Fatalf("tenant B holds %d page rows after tenant A's refused write, want %d", len(got), len(seeded))
	}
	for i := range seeded {
		if got[i] != seeded[i] {
			t.Errorf("tenant B's page %d is %+v, want the seeded %+v", seeded[i].Page, got[i], seeded[i])
		}
	}
}

// ---------------------------------------------------------------------------
// EXTR-15-01 — extraction_jobs.failure_kind
// ---------------------------------------------------------------------------

const stFailureKindColumn = "failure_kind"

// stRequireFailureKind leads every case below: a pre-migration schema fails here attributably
// instead of on a raw 42703 out of a later statement.
func stRequireFailureKind(t *testing.T, ctx context.Context) {
	t.Helper()
	var present bool
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT count(*) = 1 FROM information_schema.columns
		   WHERE table_schema = 'public' AND table_name = 'extraction_jobs' AND column_name = $1`,
		stFailureKindColumn).Scan(&present); err != nil {
		t.Fatalf("check extraction_jobs.%s presence: %v", stFailureKindColumn, err)
	}
	if !present {
		t.Fatalf("extraction_jobs.%s does not exist yet", stFailureKindColumn)
	}
}

func stJobFailureKind(t *testing.T, ctx context.Context, jobID string) *string {
	t.Helper()
	var got *string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT failure_kind FROM extraction_jobs WHERE id = $1`, jobID).Scan(&got); err != nil {
		t.Fatalf("read failure_kind for job %s: %v", jobID, err)
	}
	return got
}

// stPlantFailureKind writes a kind straight onto the row as the superuser. Advance carries no
// kind argument, so the clear-on-clean-advance case has no other way to arrange its
// precondition.
func stPlantFailureKind(t *testing.T, ctx context.Context, jobID, kind string) {
	t.Helper()
	ct, err := stRequire(t).super.Exec(ctx,
		`UPDATE extraction_jobs SET failure_kind = $2 WHERE id = $1`, jobID, kind)
	if err != nil {
		t.Fatalf("plant failure_kind %q on job %s: %v", kind, jobID, err)
	}
	if ct.RowsAffected() != 1 {
		t.Fatalf("planting failure_kind on job %s touched %d row(s), want 1", jobID, ct.RowsAffected())
	}
}

// stFailureKindConstsFromSource re-parses audit.go. This file is package extraction_test and
// cannot reach audit_internal_test.go's failureKindConsts; a hand-typed slice here would make
// TestExtractionJobs_FailureKindCheckMirrorsTheConsts compare the CHECK against itself.
func stFailureKindConstsFromSource(t *testing.T) []string {
	t.Helper()

	f, fset := mxParse(t, "audit.go")
	var out []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		var carried string
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			switch {
			case vs.Type != nil:
				carried = ""
				if id, ok := vs.Type.(*ast.Ident); ok {
					carried = id.Name
				}
			case len(vs.Values) > 0:
				carried = ""
			}
			if carried != "FailureKind" {
				continue
			}
			for i := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				bl, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					t.Fatalf("%s: FailureKind const %s has no string literal value",
						fset.Position(vs.Pos()), vs.Names[i].Name)
				}
				lit, err := strconv.Unquote(bl.Value)
				if err != nil {
					t.Fatalf("unquote %s = %s: %v", vs.Names[i].Name, bl.Value, err)
				}
				out = append(out, lit)
			}
		}
	}
	// The floor: a walk that read nothing would make every set comparison below vacuous.
	if len(out) < 5 {
		t.Fatalf("audit.go declares %d FailureKind const(s) (%v), want at least 5", len(out), out)
	}
	slices.Sort(out)
	return out
}

// FK-1 (AC-1). The CHECK admits NULL and exactly the five kinds FailureKind.Valid() accepts.
// ” is in the refused set because "" is what an empty kind must bind to SQL NULL, so a row
// carrying it could only come from a writer that lost that binding; payload_not_built is in it
// because internal/submission ships a different FailureKind under the same wire key.
func TestExtractionJobs_FailureKindCheckAdmitsTheFiveKindsAndNothingElse(t *testing.T) {
	ctx := t.Context()
	stRequireFailureKind(t, ctx)

	h := stRequire(t)
	tenantID, documentID := stTenant(t, ctx)

	insert := func(kind any) error {
		_, err := h.super.Exec(ctx,
			`INSERT INTO extraction_jobs (tenant_id, document_id, extractor, extractor_version, failure_kind)
			 VALUES ($1, $2, $3, $4, $5)`,
			tenantID, documentID, stExtractor, stExtractorVersion, kind)
		return err
	}

	accepted := stFailureKindConstsFromSource(t)
	for _, kind := range accepted {
		if err := insert(kind); err != nil {
			t.Errorf("the CHECK refused %q, which audit.go declares as a FailureKind: %v", kind, err)
		}
	}

	// NULL is the shape every pre-migration row and every success carries.
	if err := insert(nil); err != nil {
		t.Errorf("the CHECK refused a NULL failure_kind: %v", err)
	}

	for _, kind := range []string{"", "nonsense", "Document_Unavailable", "payload_not_built"} {
		err := insert(kind)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Errorf("inserting failure_kind %q returned %v, want SQLSTATE 23514", kind, err)
		}
	}

	// Non-vacuity: the accepted loop proves nothing over an empty set, and a CHECK that
	// silently dropped the value would leave fewer rows than probes.
	if len(accepted) < 5 {
		t.Fatalf("probed %d accepted value(s), want at least 5", len(accepted))
	}
	var rows int
	if err := h.super.QueryRow(ctx,
		`SELECT count(DISTINCT coalesce(failure_kind, '<null>')) FROM extraction_jobs WHERE tenant_id = $1`,
		tenantID).Scan(&rows); err != nil {
		t.Fatalf("count distinct stored kinds: %v", err)
	}
	if want := len(accepted) + 1; rows != want {
		t.Errorf("the tenant's jobs hold %d distinct failure_kind value(s), want %d — one per accepted kind plus NULL", rows, want)
	}
}

// FK-2 (AC-1, five not four). The CHECK's IN-list is set-equal to the FailureKind consts read
// out of audit.go source, so a sixth kind added to the vocabulary reds here rather than in
// production. reflect cannot enumerate a Go const block; source is the only oracle.
func TestExtractionJobs_FailureKindCheckMirrorsTheConsts(t *testing.T) {
	ctx := t.Context()
	want := stFailureKindConstsFromSource(t)
	stRequireFailureKind(t, ctx)

	rows, err := stRequire(t).super.Query(ctx,
		`SELECT pg_get_constraintdef(c.oid)
		   FROM pg_constraint c
		   JOIN pg_class t ON t.oid = c.conrelid
		   JOIN pg_namespace n ON n.oid = t.relnamespace
		  WHERE n.nspname = 'public' AND t.relname = 'extraction_jobs'
		    AND c.contype = 'c' AND pg_get_constraintdef(c.oid) LIKE '%failure_kind%'`)
	if err != nil {
		t.Fatalf("read the failure_kind CHECK on extraction_jobs: %v", err)
	}
	var defs []string
	for rows.Next() {
		var def string
		if err := rows.Scan(&def); err != nil {
			rows.Close()
			t.Fatalf("scan constraint definition: %v", err)
		}
		defs = append(defs, def)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("read the failure_kind CHECK on extraction_jobs: %v", err)
	}
	if len(defs) != 1 {
		t.Fatalf("extraction_jobs carries %d CHECK constraint(s) naming failure_kind (%v), want exactly 1",
			len(defs), defs)
	}

	seen := map[string]bool{}
	var got []string
	for _, m := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(defs[0], -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		got = append(got, m[1])
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("the CHECK admits %v, want exactly audit.go's %v\n  %s", got, want, defs[0])
	}
}

// FK-4 (AC-3). Every advance clears a prior kind, the discipline last_error already follows
// (TestExtractionStore_AdvanceClearsLastErrorToNull). The write half — a failing advance
// records its stage — is TestExtractWorker_FailureKindPerStage.
func TestExtractionStore_AdvanceClearsFailureKindToNull(t *testing.T) {
	ctx := t.Context()
	stRequireFailureKind(t, ctx)

	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 901501)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}

	// The row must carry a kind first, or the advance below proves nothing: a column that was
	// NULL all along reads NULL whatever Advance binds.
	stPlantFailureKind(t, ctx, job.ID, "pages_not_rendered")
	if got := stJobFailureKind(t, ctx, job.ID); got == nil || *got != "pages_not_rendered" {
		t.Fatalf("after planting, failure_kind is %v, want pages_not_rendered", got)
	}

	if err := s.Advance(ctx, tenantID, job.ID, "succeeded", "", 3); err != nil {
		t.Fatalf("Advance to succeeded: %v", err)
	}
	if got := stJobFailureKind(t, ctx, job.ID); got != nil {
		t.Errorf("a clean advance to succeeded left failure_kind %q, want SQL NULL", *got)
	}
	stAssertJobState(t, ctx, job.ID, "succeeded")
}

// ---------------------------------------------------------------------------
// EXTR-15-01 QA — adversarial coverage for failure_kind
// ---------------------------------------------------------------------------

// The new column rides tenant_isolation (FOR ALL, USING only) rather than a policy of its own,
// so the coverage is assumed, not stated. This proves it: tenant A can neither read nor
// overwrite tenant B's kind, and the tenant-B control rules out a query that reaches nothing.
func TestRLS_ExtractionJobFailureKindIsNotReadableOrWritableAcrossTenants(t *testing.T) {
	ctx := t.Context()
	stRequireFailureKind(t, ctx)

	h := stRequire(t)
	s := stStore(t)
	tenantA, _ := stTenant(t, ctx)
	tenantB, documentB := stTenant(t, ctx)

	jobB, err := s.EnsureJob(ctx, tenantB, documentB, stExtractor, stExtractorVersion, 901502)
	if err != nil {
		t.Fatalf("EnsureJob for tenant B: %v", err)
	}
	stPlantFailureKind(t, ctx, jobB.ID, "text_not_read")

	readAs := func(tenantID string) []string {
		t.Helper()
		var out []string
		if err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
			rows, qErr := tx.Query(ctx, `SELECT failure_kind FROM extraction_jobs WHERE id = $1`, jobB.ID)
			if qErr != nil {
				return qErr
			}
			defer rows.Close()
			for rows.Next() {
				var k *string
				if sErr := rows.Scan(&k); sErr != nil {
					return sErr
				}
				out = append(out, fmt.Sprintf("%v", k != nil && *k == "text_not_read"))
			}
			return rows.Err()
		}); err != nil {
			t.Fatalf("read failure_kind as %s: %v", tenantID, err)
		}
		return out
	}

	// The control first: an absence proved by a query that returns nothing for anyone is no
	// proof at all.
	if got := readAs(tenantB); len(got) != 1 || got[0] != "true" {
		t.Fatalf("tenant B reads its own failure_kind as %v, want [true]", got)
	}
	if got := readAs(tenantA); len(got) != 0 {
		t.Errorf("tenant A read %d row(s) of tenant B's failure_kind (%v), want none", len(got), got)
	}

	var affected int64
	if err := db.WithinTenantTx(ctx, h.app, tenantA, func(tx pgx.Tx) error {
		tag, xErr := tx.Exec(ctx,
			`UPDATE extraction_jobs SET failure_kind = 'extract_failed' WHERE id = $1`, jobB.ID)
		affected = tag.RowsAffected()
		return xErr
	}); err != nil {
		t.Fatalf("cross-tenant failure_kind UPDATE: %v", err)
	}
	if affected != 0 {
		t.Errorf("an UPDATE of failure_kind naming only tenant B's job touched %d row(s) under tenant A", affected)
	}
	if got := stJobFailureKind(t, ctx, jobB.ID); got == nil || *got != "text_not_read" {
		t.Errorf("tenant B's failure_kind is %v after tenant A's write, want text_not_read", got)
	}
}

// The empty kind is refused by the CHECK and unreachable through the binding: both halves,
// because either alone is satisfied by a writer that never emits ” for the wrong reason.
func TestExtractionJobs_FailureKindEmptyStringIsRefusedAndUnreachable(t *testing.T) {
	ctx := t.Context()
	stRequireFailureKind(t, ctx)

	h := stRequire(t)
	s := stStore(t)
	tenantID, documentID := stTenant(t, ctx)

	_, err := h.super.Exec(ctx,
		`INSERT INTO extraction_jobs (tenant_id, document_id, extractor, extractor_version, failure_kind)
		 VALUES ($1, $2, $3, $4, '')`,
		tenantID, documentID, stExtractor, stExtractorVersion)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Errorf("inserting an empty failure_kind returned %v, want SQLSTATE 23514", err)
	}

	// The unreachable half. An advance carrying the empty kind must bind SQL NULL: were it
	// binding the literal '', the CHECK above would turn every clean advance into an error.
	job, err := s.EnsureJob(ctx, tenantID, documentID, stExtractor, stExtractorVersion, 901503)
	if err != nil {
		t.Fatalf("EnsureJob: %v", err)
	}
	if err := s.Advance(ctx, tenantID, job.ID, "extracting", "", 1); err != nil {
		t.Fatalf("an advance carrying the empty kind was refused: %v", err)
	}
	if got := stJobFailureKind(t, ctx, job.ID); got != nil {
		t.Errorf("an advance carrying the empty kind stored %q, want SQL NULL", *got)
	}
	var empties int
	if err := h.super.QueryRow(ctx,
		`SELECT count(*) FROM extraction_jobs WHERE failure_kind = ''`).Scan(&empties); err != nil {
		t.Fatalf("count empty-string kinds: %v", err)
	}
	if empties != 0 {
		t.Errorf("%d extraction_jobs row(s) carry failure_kind = '', want 0", empties)
	}
}
