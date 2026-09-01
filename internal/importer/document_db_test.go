// document_db_test.go: DB-backed RED specs for Store.SettledExtraction (EXTR-06-01, task-761),
// authored against the stub in document.go before any real query exists -- Mode A. Reuses
// dbTestPools/seedTenant/seedDocument/memberSubject from store_test.go and
// service_source_rows_test.go (same package); adds its own seeding for extraction_jobs and
// extraction_field_results, which no existing helper touches.
//
// Spec-to-test map (Test Specs table, EXTR-06-01 / task-761):
//
//	SX-01 TestSettledExtraction_ReturnsNewestSucceededJobFieldsInCreatedAtOrder
//	SX-02 TestSettledExtraction_OnlyNonSucceededJobReturnsErrNotFound
//	SX-03 TestSettledExtraction_DeadLetteredJobNeverChosenEvenWhenNewest
//	SX-04 TestSettledExtraction_ExcludesCandidateRankAboveZero
//	SX-05 TestSettledExtraction_TiedCreatedAtResolvesStablyAcross20Calls
//	SX-06 TestRLS_SettledExtractionCrossTenantReadReturnsErrNotFound
//	SX-07 TestSettledExtraction_FieldsNeverNilOnErrorAndEmptySuccess
//	SX-08 TestSettledExtraction_NullFilenameSurfacesAsEmptyString
//
// SX-09 (the internal/extraction import fence) lives in document_deps_test.go.
package importer

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	sxExtractor        = "extr-06-01-fixture"
	sxExtractorVersion = "v1"
)

// sxPtr is a *string literal convenience -- no existing helper in this package does this.
func sxPtr(s string) *string { return &s }

// sxSeedDocument inserts one documents row with an explicit (possibly nil) filename.
// seedDocument (service_source_rows_test.go) always leaves filename NULL; SX-01 needs a
// non-NULL one to prove the coalesce path carries a real value, not just "".
func sxSeedDocument(t *testing.T, super *pgxpool.Pool, tenantID string, filename *string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes, filename)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		tenantID, "sx/"+tenantID+"/"+uuid.NewString(), strings.Repeat("a", 64), int64(11), filename,
	).Scan(&id); err != nil {
		t.Fatalf("seed documents: %v", err)
	}
	return id
}

// seedExtractionJob inserts one extraction_jobs row as the superuser (BYPASSRLS -- invoice_app
// holds no DELETE and this package's tests never need to clean it up individually; seedTenant's
// cascade handles that).
func seedExtractionJob(t *testing.T, super *pgxpool.Pool, tenantID, documentID, state string, createdAt time.Time) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO extraction_jobs (tenant_id, document_id, state, extractor, extractor_version, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		tenantID, documentID, state, sxExtractor, sxExtractorVersion, createdAt,
	).Scan(&id); err != nil {
		t.Fatalf("seed extraction_jobs (state %s): %v", state, err)
	}
	return id
}

// seedNExtractionJobsSameInstant inserts n succeeded jobs for documentID in ONE INSERT
// statement, so all n share the exact transaction now() for created_at (SX-05: the tie must
// come from the clock, not from separate Exec calls landing microseconds apart). n must be
// >= 2. A 2-job fixture (the original SX-05 shape) is too small: Postgres returns a stable
// pick for 2 tied rows even with no id tiebreak, so it never exercises `, j.id DESC` at all
// (task-762 SX-05-FIX).
func seedNExtractionJobsSameInstant(t *testing.T, super *pgxpool.Pool, tenantID, documentID string, n int) []string {
	t.Helper()
	if n < 2 {
		t.Fatalf("seedNExtractionJobsSameInstant: n = %d, want >= 2", n)
	}

	var sb strings.Builder
	sb.WriteString(`INSERT INTO extraction_jobs (tenant_id, document_id, state, extractor, extractor_version) VALUES `)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("($1, $2, 'succeeded', $3, $4)")
	}
	sb.WriteString(" RETURNING id")

	rows, err := super.Query(context.Background(), sb.String(), tenantID, documentID, sxExtractor, sxExtractorVersion)
	if err != nil {
		t.Fatalf("seed %d tied extraction_jobs: %v", n, err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan tied extraction_jobs id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read tied extraction_jobs ids: %v", err)
	}
	if len(ids) != n {
		t.Fatalf("seeded %d tied extraction_jobs, want %d", len(ids), n)
	}
	return ids
}

// seedExtractionField inserts one extraction_field_results row as the superuser.
func seedExtractionField(t *testing.T, super *pgxpool.Pool, tenantID, jobID, name string, value, reason *string, rank int, createdAt time.Time) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO extraction_field_results
		     (tenant_id, extraction_job_id, field_name, value, reason_code, candidate_rank, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		tenantID, jobID, name, value, reason, rank, createdAt,
	).Scan(&id); err != nil {
		t.Fatalf("seed extraction_field_results (%s, rank %d): %v", name, rank, err)
	}
	return id
}

// seedFieldCorrection inserts one extraction_field_corrections row as the superuser -- the
// human layer SettledExtraction must not read.
func seedFieldCorrection(t *testing.T, super *pgxpool.Pool, tenantID, jobID, name, value, method string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO extraction_field_corrections
		     (tenant_id, extraction_job_id, field_name, value, method, actor)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		tenantID, jobID, name, value, method, "extr-12-05-fixture-actor",
	); err != nil {
		t.Fatalf("seed extraction_field_corrections (%s, %s): %v", name, method, err)
	}
}

// sxIdentity is the memberSubject caller scoped to tenantID -- store_test.go's seedTenant
// already gives memberSubject an active membership in every tenant it seeds.
func sxIdentity(ctx context.Context, tenantID string) context.Context {
	return auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
}

// SX-01: the newest succeeded job wins, its JobID and the document's Filename both come back,
// and Fields is ordered by created_at -- not alphabetically, not by insertion accident.
func TestSettledExtraction_ReturnsNewestSucceededJobFieldsInCreatedAtOrder(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "SX-01 tenant")
	filename := "invoice-scan.pdf"
	documentID := sxSeedDocument(t, super, tenantID, &filename)

	now := time.Now().UTC()
	older := seedExtractionJob(t, super, tenantID, documentID, "succeeded", now.Add(-1*time.Hour))
	newer := seedExtractionJob(t, super, tenantID, documentID, "succeeded", now)

	// The older job's field must never surface -- proves job selection, not just field filtering.
	seedExtractionField(t, super, tenantID, older, "invoice_number", sxPtr("OLD-1"), nil, 0, now.Add(-1*time.Hour))

	// Non-alphabetical insertion order (zulu, alpha, mike): a field_name sort would return
	// alpha/mike/zulu, so matching insertion order proves ORDER BY created_at governs.
	seedExtractionField(t, super, tenantID, newer, "zulu_field", sxPtr("Z"), nil, 0, now.Add(1*time.Second))
	seedExtractionField(t, super, tenantID, newer, "alpha_field", sxPtr("A"), nil, 0, now.Add(2*time.Second))
	seedExtractionField(t, super, tenantID, newer, "mike_field", sxPtr("M"), nil, 0, now.Add(3*time.Second))

	store := NewStore(app)
	ex, err := store.SettledExtraction(sxIdentity(ctx, tenantID), documentID)
	if err != nil {
		t.Fatalf("SettledExtraction: %v", err)
	}
	if ex.JobID != newer {
		t.Errorf("JobID = %q, want the newer job %q", ex.JobID, newer)
	}
	if ex.Filename != filename {
		t.Errorf("Filename = %q, want %q", ex.Filename, filename)
	}
	wantNames := []string{"zulu_field", "alpha_field", "mike_field"}
	if len(ex.Fields) != len(wantNames) {
		t.Fatalf("len(Fields) = %d, want %d (got %+v)", len(ex.Fields), len(wantNames), ex.Fields)
	}
	for i, name := range wantNames {
		if ex.Fields[i].Name != name {
			t.Errorf("Fields[%d].Name = %q, want %q (insertion/created_at order)", i, ex.Fields[i].Name, name)
		}
	}
}

// SX-02: a document whose only job is 'extracting' returns ErrNotFound, even though that job
// carries field rows.
func TestSettledExtraction_OnlyNonSucceededJobReturnsErrNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "SX-02 tenant")
	documentID := seedDocument(t, super, tenantID)

	job := seedExtractionJob(t, super, tenantID, documentID, "extracting", time.Now().UTC())
	seedExtractionField(t, super, tenantID, job, "invoice_number", sxPtr("INV-1"), nil, 0, time.Now().UTC())

	store := NewStore(app)
	if _, err := store.SettledExtraction(sxIdentity(ctx, tenantID), documentID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SettledExtraction(extracting-only document) err = %v, want ErrNotFound", err)
	}
}

// SX-03: a dead_lettered job newer than a succeeded one is skipped -- the older succeeded
// job's fields come back, not ErrNotFound and not the dead_lettered job's field.
func TestSettledExtraction_DeadLetteredJobNeverChosenEvenWhenNewest(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "SX-03 tenant")
	documentID := seedDocument(t, super, tenantID)

	now := time.Now().UTC()
	older := seedExtractionJob(t, super, tenantID, documentID, "succeeded", now.Add(-1*time.Hour))
	newer := seedExtractionJob(t, super, tenantID, documentID, "dead_lettered", now)

	seedExtractionField(t, super, tenantID, older, "invoice_number", sxPtr("OLDER-OK"), nil, 0, now.Add(-1*time.Hour))
	seedExtractionField(t, super, tenantID, newer, "invoice_number", sxPtr("SHOULD-NOT-SURFACE"), nil, 0, now)

	store := NewStore(app)
	ex, err := store.SettledExtraction(sxIdentity(ctx, tenantID), documentID)
	if err != nil {
		t.Fatalf("SettledExtraction: %v", err)
	}
	if ex.JobID != older {
		t.Fatalf("JobID = %q, want the older succeeded job %q (newer dead_lettered job must be skipped)", ex.JobID, older)
	}
	if len(ex.Fields) != 1 || ex.Fields[0].Value == nil || *ex.Fields[0].Value != "OLDER-OK" {
		t.Errorf("Fields = %+v, want exactly one field with value %q", ex.Fields, "OLDER-OK")
	}
}

// SX-04: candidate_rank >= 1 alternatives never surface, even sharing field_name with the
// rank-0 winner.
func TestSettledExtraction_ExcludesCandidateRankAboveZero(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "SX-04 tenant")
	documentID := seedDocument(t, super, tenantID)

	job := seedExtractionJob(t, super, tenantID, documentID, "succeeded", time.Now().UTC())
	seedExtractionField(t, super, tenantID, job, "buyer_name", sxPtr("Decided Name"), nil, 0, time.Now().UTC())
	seedExtractionField(t, super, tenantID, job, "buyer_name", sxPtr("Alt One"), nil, 1, time.Now().UTC())
	seedExtractionField(t, super, tenantID, job, "buyer_name", sxPtr("Alt Two"), nil, 2, time.Now().UTC())

	store := NewStore(app)
	ex, err := store.SettledExtraction(sxIdentity(ctx, tenantID), documentID)
	if err != nil {
		t.Fatalf("SettledExtraction: %v", err)
	}
	if len(ex.Fields) != 1 {
		t.Fatalf("len(Fields) = %d, want 1 (rank-0 only); got %+v", len(ex.Fields), ex.Fields)
	}
	if ex.Fields[0].Value == nil || *ex.Fields[0].Value != "Decided Name" {
		t.Errorf("Fields[0].Value = %v, want %q", ex.Fields[0].Value, "Decided Name")
	}
}

// SX-05: two jobs tied on created_at still resolve to exactly one job, and the same one,
// across 20 calls -- a created_at-only ORDER BY could answer differently call to call.
//
// SX-05-FIX (task-762): a bare stability check ("every call returns the same job") does NOT
// catch a missing `, j.id DESC` -- confirmed by mutation, at both 12 and 200 tied jobs:
// Postgres deterministically repeats the SAME arbitrary pick for identical, unmodified data
// on every call within a process, tiebreak or no tiebreak. The assertion that actually
// catches the mutation is CORRECTNESS: the winner must be the tied job with the highest id
// (what `id DESC` selects), not merely self-consistent. Mutation-confirmed: removing
// `, j.id DESC` made every call return the SAME wrong job (not the highest id) -- stable, but
// incorrect; restoring it made every call return the highest id, correctly.
func TestSettledExtraction_TiedCreatedAtResolvesStablyAcross20Calls(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "SX-05 tenant")
	documentID := seedDocument(t, super, tenantID)
	// 12, not 2: the original 2-job fixture happened to pass this same accidental-stability
	// trap too -- enlarging alone does not fix it; the correctness assertion below does.
	ids := seedNExtractionJobsSameInstant(t, super, tenantID, documentID, 12)
	valid := map[string]bool{}
	for _, id := range ids {
		valid[id] = true
	}
	sorted := append([]string{}, ids...)
	sort.Strings(sorted)
	wantWinner := sorted[len(sorted)-1] // `ORDER BY ..., id DESC` picks the highest id among ties

	store := NewStore(app)
	c := sxIdentity(ctx, tenantID)

	for i := 0; i < 20; i++ {
		ex, err := store.SettledExtraction(c, documentID)
		if err != nil {
			t.Fatalf("call %d: SettledExtraction: %v", i, err)
		}
		if !valid[ex.JobID] {
			t.Fatalf("call %d: JobID = %q, want one of the %d tied jobs %v", i, ex.JobID, len(ids), ids)
		}
		if ex.JobID != wantWinner {
			t.Fatalf("call %d: JobID = %q, want %q (the highest id among the tied jobs, i.e. `id DESC` actually governing the tiebreak)", i, ex.JobID, wantWinner)
		}
	}
}

// SX-06: tenant B reading tenant A's document gets ErrNotFound, never A's fields --
// tenant_isolation makes A's extraction_jobs row invisible to B's transaction.
func TestRLS_SettledExtractionCrossTenantReadReturnsErrNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "SX-06 tenant A")
	tenantB := seedTenant(t, super, "SX-06 tenant B")
	docA := seedDocument(t, super, tenantA)
	seedDocument(t, super, tenantB) // gives B its own tenant-scoped row; unused ID is fine

	jobA := seedExtractionJob(t, super, tenantA, docA, "succeeded", time.Now().UTC())
	seedExtractionField(t, super, tenantA, jobA, "invoice_number", sxPtr("A-ONLY"), nil, 0, time.Now().UTC())

	store := NewStore(app)

	// Positive control: A reading its own document succeeds -- makes the negative case below
	// mean something (a store that always errors would pass it vacuously).
	if _, err := store.SettledExtraction(sxIdentity(ctx, tenantA), docA); err != nil {
		t.Fatalf("A reading its own document: %v", err)
	}

	if _, err := store.SettledExtraction(sxIdentity(ctx, tenantB), docA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("B reading A's document: err = %v, want ErrNotFound", err)
	}
}

// SX-07: Fields is never nil -- neither on the ErrNotFound path nor on a success with zero
// field rows. A nil slice marshals to JSON null on the eventual wire response.
func TestSettledExtraction_FieldsNeverNilOnErrorAndEmptySuccess(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	t.Run("errNotFound", func(t *testing.T) {
		tenantID := seedTenant(t, super, "SX-07 tenant notfound")
		documentID := seedDocument(t, super, tenantID)

		ex, err := store.SettledExtraction(sxIdentity(ctx, tenantID), documentID)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if ex.Fields == nil {
			t.Error("Fields is nil on the ErrNotFound path, want a non-nil empty slice")
		}
	})

	t.Run("successNoFields", func(t *testing.T) {
		tenantID := seedTenant(t, super, "SX-07 tenant empty")
		documentID := seedDocument(t, super, tenantID)
		seedExtractionJob(t, super, tenantID, documentID, "succeeded", time.Now().UTC())

		ex, err := store.SettledExtraction(sxIdentity(ctx, tenantID), documentID)
		if err != nil {
			t.Fatalf("SettledExtraction: %v", err)
		}
		if ex.Fields == nil {
			t.Error("Fields is nil for a succeeded job with zero field rows, want a non-nil empty slice")
		}
		if len(ex.Fields) != 0 {
			t.Errorf("Fields = %+v, want empty", ex.Fields)
		}
	})
}

// SX-08: documents.filename IS NULL surfaces as "", never a scan error or panic. Also asserts
// JobID: a zero-value Filename ("") would otherwise match a NULL filename by accident without
// the read having found anything at all.
func TestSettledExtraction_NullFilenameSurfacesAsEmptyString(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "SX-08 tenant")
	documentID := seedDocument(t, super, tenantID) // leaves filename NULL
	job := seedExtractionJob(t, super, tenantID, documentID, "succeeded", time.Now().UTC())

	store := NewStore(app)
	ex, err := store.SettledExtraction(sxIdentity(ctx, tenantID), documentID)
	if err != nil {
		t.Fatalf("SettledExtraction: %v", err)
	}
	if ex.JobID != job {
		t.Fatalf("JobID = %q, want %q", ex.JobID, job)
	}
	if ex.Filename != "" {
		t.Errorf("Filename = %q, want \"\" for a NULL documents.filename", ex.Filename)
	}
}

// QA additions below (not in the Test Specs table) -- adversarial/edge coverage found during
// task-761 verification.

// QA-01: a newer succeeded job with FEWER fields than an older one still wins. Guards against a
// selection query that (accidentally) picks the job with the most field rows rather than the
// truly newest one.
func TestSettledExtraction_NewerJobWithFewerFieldsStillWins(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "QA-01 tenant")
	documentID := seedDocument(t, super, tenantID)

	now := time.Now().UTC()
	older := seedExtractionJob(t, super, tenantID, documentID, "succeeded", now.Add(-1*time.Hour))
	newer := seedExtractionJob(t, super, tenantID, documentID, "succeeded", now)

	seedExtractionField(t, super, tenantID, older, "field_a", sxPtr("A"), nil, 0, now.Add(-1*time.Hour))
	seedExtractionField(t, super, tenantID, older, "field_b", sxPtr("B"), nil, 0, now.Add(-1*time.Hour))
	seedExtractionField(t, super, tenantID, older, "field_c", sxPtr("C"), nil, 0, now.Add(-1*time.Hour))
	seedExtractionField(t, super, tenantID, newer, "field_only", sxPtr("ONLY"), nil, 0, now)

	store := NewStore(app)
	ex, err := store.SettledExtraction(sxIdentity(ctx, tenantID), documentID)
	if err != nil {
		t.Fatalf("SettledExtraction: %v", err)
	}
	if ex.JobID != newer {
		t.Fatalf("JobID = %q, want the newer job %q (fewer fields must not lose the pick)", ex.JobID, newer)
	}
	if len(ex.Fields) != 1 || ex.Fields[0].Name != "field_only" {
		t.Errorf("Fields = %+v, want exactly the newer job's one field", ex.Fields)
	}
}

// QA-02: a succeeded job whose field results are entirely candidate_rank >= 1 (no rank-0 winner
// was ever written) is schema-legal, per the architecture notes (WriteFieldResults normally
// commits a rank-0 row atomically with the succeeded transition, but nothing in this table
// enforces that). The read must still succeed -- the job exists and is succeeded -- and Fields
// must come back empty, not an error and not the rank-1 rows themselves.
func TestSettledExtraction_JobWithOnlyAboveZeroRanksReturnsEmptyFieldsNotError(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "QA-02 tenant")
	documentID := seedDocument(t, super, tenantID)
	job := seedExtractionJob(t, super, tenantID, documentID, "succeeded", time.Now().UTC())
	seedExtractionField(t, super, tenantID, job, "orphan_alt", sxPtr("ALT"), nil, 1, time.Now().UTC())

	store := NewStore(app)
	ex, err := store.SettledExtraction(sxIdentity(ctx, tenantID), documentID)
	if err != nil {
		t.Fatalf("SettledExtraction: %v, want success (the job itself is succeeded)", err)
	}
	if ex.JobID != job {
		t.Fatalf("JobID = %q, want %q", ex.JobID, job)
	}
	if ex.Fields == nil {
		t.Error("Fields is nil, want a non-nil empty slice")
	}
	if len(ex.Fields) != 0 {
		t.Errorf("Fields = %+v, want empty (no rank-0 row exists)", ex.Fields)
	}
}

// QA-03: a rank-0 field with value IS NULL and reason_code set (the unreadable/ambiguous/
// inconsistent/missing case) surfaces both Value=nil and the reason code, not a scan error.
// EXTR-06-02's mapper needs to tell this apart from a clean field.
func TestSettledExtraction_NullValueWithReasonCodeSurfaces(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "QA-03 tenant")
	documentID := seedDocument(t, super, tenantID)
	job := seedExtractionJob(t, super, tenantID, documentID, "succeeded", time.Now().UTC())
	seedExtractionField(t, super, tenantID, job, "tax_id", nil, sxPtr("unreadable"), 0, time.Now().UTC())

	store := NewStore(app)
	ex, err := store.SettledExtraction(sxIdentity(ctx, tenantID), documentID)
	if err != nil {
		t.Fatalf("SettledExtraction: %v", err)
	}
	if len(ex.Fields) != 1 {
		t.Fatalf("len(Fields) = %d, want 1", len(ex.Fields))
	}
	if ex.Fields[0].Value != nil {
		t.Errorf("Value = %v, want nil", ex.Fields[0].Value)
	}
	if ex.Fields[0].Reason == nil || *ex.Fields[0].Reason != "unreadable" {
		t.Errorf("Reason = %v, want \"unreadable\"", ex.Fields[0].Reason)
	}
}

// QA-04: a document that was never inserted at all and a document that exists but has never
// had an extraction job both return ErrNotFound, via the same zero-rows path -- the two cases
// are indistinguishable to the caller. This is intended (task-761: "Zero succeeded jobs =>
// ErrNotFound"), not a gap; this test pins that intent so a future change that starts
// distinguishing them is a deliberate decision, not an accident.
func TestSettledExtraction_NoDocumentAndDocumentWithNoJobBothReturnErrNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	t.Run("documentNeverExisted", func(t *testing.T) {
		tenantID := seedTenant(t, super, "QA-04 tenant a")
		ex, err := store.SettledExtraction(sxIdentity(ctx, tenantID), uuid.NewString())
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if ex.Fields == nil {
			t.Error("Fields is nil, want a non-nil empty slice")
		}
	})

	t.Run("documentExistsNoJob", func(t *testing.T) {
		tenantID := seedTenant(t, super, "QA-04 tenant b")
		documentID := seedDocument(t, super, tenantID)
		ex, err := store.SettledExtraction(sxIdentity(ctx, tenantID), documentID)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if ex.Fields == nil {
			t.Error("Fields is nil, want a non-nil empty slice")
		}
	})
}

// AC-7's behavioural half, and CONFIRMATORY rather than red-first: this subtask leaves
// SettledExtraction alone. A source parse cannot see a merge done inside documentCreateInput,
// so the value is read back through the real query with a correction sitting on the row.
func TestSettledExtraction_IgnoresCorrectionsAndReadsRankZero(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "EXTR-12-05 tenant")
	documentID := seedDocument(t, super, tenantID)

	job := seedExtractionJob(t, super, tenantID, documentID, "succeeded", time.Now().UTC())
	seedExtractionField(t, super, tenantID, job, "total", sxPtr("READ-A"), nil, 0, time.Now().UTC())
	seedFieldCorrection(t, super, tenantID, job, "total", "HUMAN-B", "typed")

	store := NewStore(app)
	ex, err := store.SettledExtraction(sxIdentity(ctx, tenantID), documentID)
	if err != nil {
		t.Fatalf("SettledExtraction: %v", err)
	}
	if len(ex.Fields) != 1 {
		t.Fatalf("len(Fields) = %d, want 1; got %+v", len(ex.Fields), ex.Fields)
	}
	if ex.Fields[0].Value == nil {
		t.Fatalf("Fields[0].Value came back nil, want %q", "READ-A")
	}
	if *ex.Fields[0].Value != "READ-A" {
		t.Errorf("Fields[0].Value = %q, want %q -- the correction already reached the invoice when it "+
			"was written, so reading it here would file the same value twice",
			*ex.Fields[0].Value, "READ-A")
	}
}
