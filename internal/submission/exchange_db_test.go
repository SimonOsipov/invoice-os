// M5-01-05 (task-215): the DB-BACKED half of the evidence write seam's spec — RecordExchange
// against a real Postgres. Authored BEFORE internal/submission/exchange.go exists, so the RED
// state of this file is a COMPILE ERROR ("undefined: submission.RecordExchange", "undefined:
// submission.Exchange"), the same red as exchange_test.go. The pure ScrubHeaders / SafeBody
// specs live there; this file only asserts what a round-trip through app_exchange can prove.
//
// Package `submission_test`, matching worker_smoke_test.go and failure_modes_test.go.
// TestMain already exists at failure_modes_test.go:57 — one per test binary — so this file
// defines none; it reuses that fixture's two pools through requireExchangeDB below.
//
// GATING. Every case here self-skips via requireExchangeDB when the suite is unconfigured,
// and ONLY then. That is the sole permitted skip: .github/workflows/ci.yml:374 runs the
// `queue` job through scripts/ci/rls-test-gate.sh over ./internal/submission/..., where a
// SKIP is a FAILURE — but that job sets both DSNs, so nothing here skips there. The default
// `go` job sets neither and has no gate, so the skip is an ordinary pass (AC #6).
//
// WHY BOTH DSNs, and why the migrator seeds. `make test-queue` (Makefile:119-122) sets
// DATABASE_URL and DATABASE_MIGRATION_URL but NOT DATABASE_SUPERUSER_URL — unlike
// `make test-rls`. So the superuser/BYPASSRLS seeding idiom used by
// internal/platform/db/app_exchange_rls_test.go is unavailable here. invoice_app cannot
// stand in: it holds SELECT ONLY on `tenants` and cannot create one. Fixtures are therefore
// seeded as invoice_migrator (the table owner) — but the owner is bound by FORCE ROW LEVEL
// SECURITY to the tenant PREDICATE, so every seed still runs inside db.WithinTenantTx. The
// full FK chain is tenants → business_entities → invoices → submission_jobs, and only then
// can app_exchange's 3-column FK (tenant_id, submission_job_id, invoice_id) be satisfied.
//
// Local run: `DEV_DB_PORT=5433 make test-queue` from this worktree (Makefile:32 defaults to
// 5432; this worktree's compose stack publishes 5433).
//
// TWO THINGS THAT SHAPE THESE ASSERTIONS, both established empirically against the live DB
// during the M5-01-05 Explore pass:
//
//  1. TENANCY IS NOT AUTOMATIC HERE, and the audit.Record analogy in the plan is wrong about
//     it. audit_log.tenant_id carries a column DEFAULT that reads the app.current_tenant
//     GUC; app_exchange.tenant_id has NO DEFAULT (migrations/20260722093218_app_exchange.sql
//     :79). An INSERT that omits the column leaves it NULL, the RLS WITH CHECK evaluates
//     NULL, and the write is refused 42501 — EVEN INSIDE A VALID TENANT CONTEXT. So an
//     audit-style INSERT naming no tenant_id yields a seam that can never write a row at
//     all. The implementation must name the column and fill it in SQL from the same GUC
//     expression every RLS policy in this repo uses — nullif over current_setting
//     ("app.current_tenant", true) cast to uuid — which keeps tenant_id OFF the Exchange
//     struct (no bypass affordance) while making the write work.
//
//     That is why four of the five cases below require a SUCCESSFUL write. If
//     TestRecordExchange_WithoutTenantContextRefused is the only one passing, the
//     implementation took the audit shortcut and the seam is inert. (PG evaluates the RLS
//     WITH CHECK before NOT NULL, so the shortcut surfaces as 42501, never 23502.)
//
//  2. http.Header MARSHALS ITS VALUES AS ARRAYS: {"Content-Type":["application/json"]}, not
//     {"Content-Type":"application/json"}. Assertions here therefore unmarshal the stored
//     jsonb back into an http.Header and compare VALUES, never `->>` scalars.
//
// One more schema trap the plan's struct notes get right and is worth restating at the point
// of use: app_exchange.attempt is 1-BASED (CHECK attempt >= 1), unlike submission_jobs.attempts
// (CHECK attempts >= 0). Copying a fresh job's attempts=0 across hits 23514.
package submission_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

const (
	exAdapter        = "firs-mbs"
	exAdapterVersion = "v1"
)

// requireExchangeDB returns the shared TestMain fixture (failure_modes_test.go:49-57) or
// skips. It gates on the SAME pair of env vars that fixture does — DATABASE_URL for the
// invoice_app pool the seam writes through, DATABASE_MIGRATION_URL for the invoice_migrator
// pool that seeds the FK chain — because the app role cannot seed a tenant. It is the ONLY
// skip site in this file, and it fires on nothing but absent configuration.
func requireExchangeDB(t *testing.T) *effectsFixture {
	t.Helper()
	if fx == nil {
		t.Skip("exchange suite skipped: set DATABASE_URL and DATABASE_MIGRATION_URL " +
			"(or run `make test-queue`)")
	}
	return fx
}

// exChain seeds tenants → business_entities → invoices → submission_jobs for one fresh
// tenant and returns the tenant id, the invoice id and the job id, plus a cleanup func.
//
// Seeded as the MIGRATOR (see the file header for why not superuser and why not the app
// role) and inside db.WithinTenantTx, because FORCE ROW LEVEL SECURITY binds even the table
// owner to the tenant predicate.
//
// Cleanup deletes DEEPEST FIRST — app_exchange, then submission_jobs, then invoices, then
// business_entities, then the tenant. Every one of those FKs is ON DELETE RESTRICT
// (app_exchange → submission_jobs, submission_jobs → invoices, invoices → business_entities),
// so any other order leaves rows behind. A single `DELETE FROM tenants` relying on the
// tenant_id CASCADEs is NOT safe here: PostgreSQL gives no ordering guarantee between the
// cascading child tables, and a business_entities row cascade-deleted before its invoices
// raises 23001 against the sibling RESTRICT.
func exChain(t *testing.T, f *effectsFixture) (tenantID, invoiceID, jobID string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	tenantID = uuid.NewString()
	entityID := uuid.NewString()
	invoiceID = uuid.NewString()
	jobID = uuid.NewString()

	err := db.WithinTenantTx(ctx, f.mig, tenantID, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx,
			`INSERT INTO tenants (id, name) VALUES ($1, $2)`,
			tenantID, "M5-01-05 exchange "+tenantID[:8]); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx,
			`INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, $3)`,
			entityID, tenantID, "Exchange Corp"); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx,
			`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number)
			 VALUES ($1, $2, $3, $4)`,
			invoiceID, tenantID, entityID, "EX-"+invoiceID[:8]); e != nil {
			return e
		}
		_, e := tx.Exec(ctx,
			`INSERT INTO submission_jobs (id, tenant_id, invoice_id, idempotency_key,
			                              adapter, adapter_version)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			jobID, tenantID, invoiceID, "exkey-"+jobID, exAdapter, exAdapterVersion)
		return e
	})
	if err != nil {
		t.Fatalf("seed exchange FK chain (tenant=%s): %v", tenantID, err)
	}

	return tenantID, invoiceID, jobID, func() {
		_ = db.WithinTenantTx(context.Background(), f.mig, tenantID, func(tx pgx.Tx) error {
			for _, q := range []string{
				`DELETE FROM app_exchange       WHERE tenant_id = $1`,
				`DELETE FROM submission_jobs    WHERE tenant_id = $1`,
				`DELETE FROM invoices           WHERE tenant_id = $1`,
				`DELETE FROM business_entities  WHERE tenant_id = $1`,
				`DELETE FROM tenants            WHERE id = $1`,
			} {
				if _, e := tx.Exec(context.Background(), q, tenantID); e != nil {
					return e
				}
			}
			return nil
		})
	}
}

// exExchange builds a minimally valid Exchange for a seeded job. attempt is 1, not 0 —
// app_exchange.attempt is 1-BASED (CHECK attempt >= 1) while submission_jobs.attempts is
// 0-based, and a fixture that copies the job's value hits 23514.
func exExchange(invoiceID, jobID string) submission.Exchange {
	return submission.Exchange{
		SubmissionJobID: jobID,
		InvoiceID:       invoiceID,
		Operation:       "submit",
		Outcome:         "sent",
		Attempt:         1,
		Adapter:         exAdapter,
		AdapterVersion:  exAdapterVersion,
	}
}

// exStored is one app_exchange row read back through the APP role under its own tenant
// context — i.e. through RLS, exactly as the SPA and the M5-07 export will read it. Header
// columns come back as raw jsonb text and are unmarshalled into http.Header so values (which
// jsonb stores as ARRAYS) can be compared directly.
type exStored struct {
	requestHeaders  http.Header
	responseHeaders http.Header
	requestBody     *string
	responseBody    *string
	truncated       bool
	encodingCoerced bool
	attempt         int
}

func exReadRow(t *testing.T, f *effectsFixture, tenantID, jobID string) exStored {
	t.Helper()
	ctx := context.Background()
	var reqH, respH []byte
	var got exStored
	err := db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT request_headers, response_headers, request_body, response_body,
			        truncated, encoding_coerced, attempt
			   FROM app_exchange
			  WHERE submission_job_id = $1`, jobID).
			Scan(&reqH, &respH, &got.requestBody, &got.responseBody,
				&got.truncated, &got.encodingCoerced, &got.attempt)
	})
	if err != nil {
		t.Fatalf("read back app_exchange row for job %s: %v", jobID, err)
	}
	if e := json.Unmarshal(reqH, &got.requestHeaders); e != nil {
		t.Fatalf("unmarshal stored request_headers %q: %v", reqH, e)
	}
	if e := json.Unmarshal(respH, &got.responseHeaders); e != nil {
		t.Fatalf("unmarshal stored response_headers %q: %v", respH, e)
	}
	return got
}

// exCountRows counts app_exchange rows for a job, read through the app role under tenant
// context (the table is FORCE RLS, so an unscoped count would see nothing).
func exCountRows(t *testing.T, f *effectsFixture, tenantID, jobID string) int {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM app_exchange WHERE submission_job_id = $1`, jobID).Scan(&n)
	}); err != nil {
		t.Fatalf("count app_exchange rows for job %s: %v", jobID, err)
	}
	return n
}

// exPgCode extracts the SQLSTATE from err, or "" when err does not WRAP a *pgconn.PgError.
// The "wrap" part is load-bearing: RecordExchange must return its INSERT error with %w (the
// audit.go:50 idiom) or errors.As cannot reach the driver error and the 42501 assertion
// below fails no matter how correct the SQL is.
func exPgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// --- RecordExchange --------------------------------------------------------------------

// The credential never reaches the row. Authorization and X-Api-Key are sent in, only the
// allowlisted names come back — asserted against the ACTUAL stored jsonb, not against
// RecordExchange's return value. Both header maps are checked because AC #3 requires the
// scrub on BOTH, and an implementation that scrubs the request and stores response headers
// raw would otherwise ship a Set-Cookie into the M5-07 export.
func TestRecordExchange_StoresScrubbedHeaders(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, jobID, cleanup := exChain(t, f)
	defer cleanup()

	e := exExchange(invoiceID, jobID)
	e.RequestHeaders = http.Header{}
	e.RequestHeaders.Set("Authorization", "Bearer super-secret")
	e.RequestHeaders.Set("X-Api-Key", "sk-live-should-never-be-stored")
	e.RequestHeaders.Set("Content-Type", "application/json")
	e.ResponseHeaders = http.Header{}
	e.ResponseHeaders.Set("Set-Cookie", "session=super-secret")
	e.ResponseHeaders.Set("Content-Type", "application/xml")

	if err := db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
		return submission.RecordExchange(ctx, tx, e)
	}); err != nil {
		t.Fatalf("RecordExchange in a valid tenant context: %v (app_exchange.tenant_id has "+
			"NO default — the INSERT must name the column and fill it from the GUC)", err)
	}

	got := exReadRow(t, f, tenantID, jobID)

	for name, h := range map[string]http.Header{
		"request_headers":  got.requestHeaders,
		"response_headers": got.responseHeaders,
	} {
		for _, cred := range []string{"Authorization", "X-Api-Key", "Set-Cookie"} {
			if v, ok := exLookupFold(h, cred); ok {
				t.Errorf("stored %s contains credential %q = %v — evidence is exported to "+
					"customers (M5-07); it must be scrubbed at WRITE time", name, cred, v)
			}
		}
	}
	if v, ok := exLookupFold(got.requestHeaders, "Content-Type"); !ok ||
		len(v) != 1 || v[0] != "application/json" {
		t.Errorf("stored request_headers Content-Type = %v (present=%v), want "+
			"[\"application/json\"] — jsonb stores http.Header values as ARRAYS", v, ok)
	}
	if v, ok := exLookupFold(got.responseHeaders, "Content-Type"); !ok ||
		len(v) != 1 || v[0] != "application/xml" {
		t.Errorf("stored response_headers Content-Type = %v (present=%v), want "+
			"[\"application/xml\"]", v, ok)
	}
	// The 1-based attempt survived the write intact — the CHECK is attempt >= 1.
	if got.attempt != 1 {
		t.Errorf("stored attempt = %d, want 1", got.attempt)
	}
}

// A 300 KiB body is stored clipped at the cap with truncated=true — and encoding_coerced
// FALSE, because the body is clean ASCII. The two flag assertions together are what prove
// the row's flags are independent rather than one boolean written twice (the schema comment
// on both columns says exactly this).
func TestRecordExchange_FlagsTruncatedBody(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, jobID, cleanup := exChain(t, f)
	defer cleanup()

	big := strings.Repeat("a", 300*1024)
	e := exExchange(invoiceID, jobID)
	e.RequestBody = &big

	if err := db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
		return submission.RecordExchange(ctx, tx, e)
	}); err != nil {
		t.Fatalf("RecordExchange with a 300 KiB body: %v", err)
	}

	got := exReadRow(t, f, tenantID, jobID)
	if got.requestBody == nil {
		t.Fatal("stored request_body is NULL, want the capped prefix")
	}
	if len(*got.requestBody) != submission.MaxBodyBytes {
		t.Errorf("stored request_body length = %d bytes, want exactly %d (pure ASCII: no "+
			"rune boundary to walk back over)", len(*got.requestBody), submission.MaxBodyBytes)
	}
	if !strings.HasPrefix(big, *got.requestBody) {
		t.Error("stored request_body is not a prefix of the submitted body — evidence must " +
			"be preserved verbatim up to the cap")
	}
	if !got.truncated {
		t.Error("stored truncated = false, want true — Core AC-3 requires a VISIBLE flag " +
			"when a body was truncated")
	}
	if got.encodingCoerced {
		t.Error("stored encoding_coerced = true, want false — the body is clean ASCII; " +
			"truncation is not coercion")
	}
}

// The write SURVIVES a NUL. Without the coercion step this INSERT dies with 22021 ("null
// character not permitted") — the precise failure PM Decision [evidence is text, not parsed]
// says must not happen, since the body arrives at the moment something has already gone
// wrong. Note this cannot be satisfied by strings.ToValidUTF8 alone: "\x00" IS valid UTF-8.
func TestRecordExchange_NulByteBodyDoesNotFailTheWrite(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, jobID, cleanup := exChain(t, f)
	defer cleanup()

	dirty := "response\x00body"
	e := exExchange(invoiceID, jobID)
	e.ResponseBody = &dirty

	if err := db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
		return submission.RecordExchange(ctx, tx, e)
	}); err != nil {
		if exPgCode(err) == "22021" {
			t.Fatalf("RecordExchange failed with 22021 — the NUL reached Postgres unscrubbed; "+
				"utf8.ValidString(\"\\x00\") is true, so ToValidUTF8 does not remove it: %v", err)
		}
		t.Fatalf("RecordExchange with a NUL-bearing body: %v", err)
	}

	got := exReadRow(t, f, tenantID, jobID)
	if got.responseBody == nil {
		t.Fatal("stored response_body is NULL, want the NUL-stripped text")
	}
	if strings.ContainsRune(*got.responseBody, 0) {
		t.Error("stored response_body still contains a NUL byte")
	}
	if *got.responseBody != "responsebody" {
		t.Errorf("stored response_body = %q, want %q", *got.responseBody, "responsebody")
	}
	if !got.encodingCoerced {
		t.Error("stored encoding_coerced = false, want true — a NUL was dropped, so the " +
			"stored evidence is complete-but-ALTERED and must say so")
	}
	if got.truncated {
		t.Error("stored truncated = true, want false — the body is 13 bytes, far under the cap")
	}
}

// AC #4: on a tx with no app.current_tenant, the write is REFUSED, not silently written
// unscoped. The tx is begun straight off the pool — deliberately NOT through
// db.WithinTenantTx, which is the only thing that sets the GUC.
//
// 42501 is what surfaces because the RLS WITH CHECK is evaluated before NOT NULL: the
// tenant_id expression resolves to NULL, the predicate is NULL, and the policy refuses.
// A 23502 here would mean the WITH CHECK never ran.
func TestRecordExchange_WithoutTenantContextRefused(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, jobID, cleanup := exChain(t, f)
	defer cleanup()

	tx, err := f.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin bare tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	err = submission.RecordExchange(ctx, tx, exExchange(invoiceID, jobID))
	if err == nil {
		t.Fatal("RecordExchange succeeded with NO tenant context — it must never write unscoped")
	}
	if code := exPgCode(err); code != "42501" {
		t.Fatalf("RecordExchange SQLSTATE = %q, want 42501 insufficient_privilege "+
			"(wrap the INSERT error with %%w, per audit.go:50, or errors.As cannot reach it): %v",
			code, err)
	}

	// The refusal left nothing behind. Read as the tenant, since the row — had one been
	// written unscoped — would carry a NULL tenant_id and be invisible; a non-zero count
	// would mean it was written to this tenant, which is the worse outcome.
	_ = tx.Rollback(ctx)
	if n := exCountRows(t, f, tenantID, jobID); n != 0 {
		t.Errorf("app_exchange rows after the refused write = %d, want 0", n)
	}
}

// Exactly ONE row per call. app_exchange is the evidence log for a submission ATTEMPT
// (Core AC-2: a record of attempts, not of responses) — a seam that wrote two rows would
// double-count every attempt in the ops console and the customer export, and one that wrote
// zero while returning nil would lose the attempt entirely. The zero case is the one a
// "return nil on success" assertion cannot see, which is why this counts.
func TestRecordExchange_WritesExactlyOneRow(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, jobID, cleanup := exChain(t, f)
	defer cleanup()

	if n := exCountRows(t, f, tenantID, jobID); n != 0 {
		t.Fatalf("app_exchange rows before RecordExchange = %d, want 0 (fresh fixture)", n)
	}

	if err := db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
		return submission.RecordExchange(ctx, tx, exExchange(invoiceID, jobID))
	}); err != nil {
		t.Fatalf("RecordExchange: %v", err)
	}

	if n := exCountRows(t, f, tenantID, jobID); n != 1 {
		t.Errorf("app_exchange rows after ONE RecordExchange = %d, want exactly 1", n)
	}
}
