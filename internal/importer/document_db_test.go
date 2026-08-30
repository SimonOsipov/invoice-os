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

// seedTwoExtractionJobsSameInstant inserts two succeeded jobs for documentID in ONE INSERT
// statement, so both share the exact transaction now() for created_at (SX-05: the tie must
// come from the clock, not from two Exec calls landing microseconds apart).
func seedTwoExtractionJobsSameInstant(t *testing.T, super *pgxpool.Pool, tenantID, documentID string) (idA, idB string) {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`INSERT INTO extraction_jobs (tenant_id, document_id, state, extractor, extractor_version)
		 VALUES ($1, $2, 'succeeded', $3, $4), ($1, $2, 'succeeded', $3, $4)
		 RETURNING id`,
		tenantID, documentID, sxExtractor, sxExtractorVersion,
	)
	if err != nil {
		t.Fatalf("seed tied extraction_jobs: %v", err)
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
	if len(ids) != 2 {
		t.Fatalf("seeded %d tied extraction_jobs, want 2", len(ids))
	}
	return ids[0], ids[1]
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
func TestSettledExtraction_TiedCreatedAtResolvesStablyAcross20Calls(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "SX-05 tenant")
	documentID := seedDocument(t, super, tenantID)
	idA, idB := seedTwoExtractionJobsSameInstant(t, super, tenantID, documentID)
	valid := map[string]bool{idA: true, idB: true}

	store := NewStore(app)
	c := sxIdentity(ctx, tenantID)

	var first string
	for i := 0; i < 20; i++ {
		ex, err := store.SettledExtraction(c, documentID)
		if err != nil {
			t.Fatalf("call %d: SettledExtraction: %v", i, err)
		}
		if !valid[ex.JobID] {
			t.Fatalf("call %d: JobID = %q, want one of the two tied jobs %v", i, ex.JobID, []string{idA, idB})
		}
		if i == 0 {
			first = ex.JobID
			continue
		}
		if ex.JobID != first {
			t.Fatalf("call %d: JobID = %q, want the stable winner %q from call 0 (tie not broken deterministically)", i, ex.JobID, first)
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
