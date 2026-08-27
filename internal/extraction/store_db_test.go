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
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"os"
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
	if err := s.WriteFieldResults(ctx, tenantID, job.ID, []extraction.Field{
		{Name: "invoice_number", Value: stPtr("INV-0001"), Reason: extraction.ReasonNone},
		{Name: "buyer_tin", Reason: extraction.ReasonMissing},
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
	if err := s.WriteFieldResults(ctx, tenantID, job.ID, []extraction.Field{
		{Name: "total_amount", Value: stPtr("1000.00"), Reason: extraction.ReasonNone},
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
	if err := s.WriteFieldResults(ctx, tenantID, job.ID, []extraction.Field{
		{Name: "invoice_number", Value: stPtr("INV-0002"), Region: &want, Reason: extraction.ReasonNone},
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
	if err := s.WriteFieldResults(ctx, tenantA, jobB.ID, []extraction.Field{
		{Name: "invoice_number", Value: stPtr("LEAK"), Reason: extraction.ReasonNone},
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
