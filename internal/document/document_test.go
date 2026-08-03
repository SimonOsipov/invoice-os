// document_test.go: the Store specs (Upsert/Get + their audit rows), authored
// before internal/document/document.go exists. DB-backed as invoice_app, so RLS
// is what scopes every assertion query — no manual `WHERE tenant_id` runs
// through the app pool.
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -p 1 -count=1 ./internal/document/...
package document_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- shared DB-test harness (per-package copy, per internal/importer/store_test.go) ---

// dbTestPools returns the superuser (seed/read-back) and app-role (Store) pools,
// or skips when the per-role DSNs are unset. The pair is DATABASE_URL +
// DATABASE_SUPERUSER_URL, not DATABASE_MIGRATION_URL.
func dbTestPools(t *testing.T) (super, app *pgxpool.Pool) {
	t.Helper()
	appURL := os.Getenv("DATABASE_URL")
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("document db-integration test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
	}
	ctx := context.Background()

	s, err := pgxpool.New(ctx, superURL)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("ping superuser (is the DB up and bootstrapped?): %v", err)
	}

	a, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	t.Cleanup(a.Close)

	return s, a
}

// seedTenantWithID inserts one throwaway tenants row under a caller-chosen id.
// The id has to be choosable so a test can let the documents FK fail first and
// only then make the tenant real.
func seedTenantWithID(t *testing.T, super *pgxpool.Pool, id, label string) string {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm')`, id, label,
	); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id)
	})
	return id
}

// seedTenant inserts one throwaway tenants row; the CASCADE takes its documents
// with it.
func seedTenant(t *testing.T, super *pgxpool.Pool, label string) string {
	t.Helper()
	return seedTenantWithID(t, super, uuid.NewString(), label)
}

func mustCount(t *testing.T, super *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return n
}

// auditCount counts audit_log rows for tenantID+event through the app pool, so
// FORCE RLS is what scopes the tenant — mirrors internal/invoice/store_test.go.
func auditCount(t *testing.T, pool *pgxpool.Pool, tenantID, event string) int {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE event = $1`, event).Scan(&n)
	}); err != nil {
		t.Fatalf("count audit_log: %v", err)
	}
	return n
}

func auditActor(t *testing.T, pool *pgxpool.Pool, tenantID, event string) string {
	t.Helper()
	ctx := context.Background()
	var actor string
	if err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT actor FROM audit_log WHERE event = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, event,
		).Scan(&actor)
	}); err != nil {
		t.Fatalf("read audit_log actor: %v", err)
	}
	return actor
}

// auditCitingCount counts tenantID's rows for event whose payload mentions id
// anywhere — key-name agnostic on purpose, so the payload shape stays free.
func auditCitingCount(t *testing.T, pool *pgxpool.Pool, tenantID, event, id string) int {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM audit_log WHERE event = $1 AND payload::text LIKE '%' || $2 || '%'`,
			event, id,
		).Scan(&n)
	}); err != nil {
		t.Fatalf("count audit_log citing %s: %v", id, err)
	}
	return n
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// hashHex is a stand-in for the server-side hash: 64 hex chars, which is what
// documents.content_hash's CHECK demands.
func hashHex(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func identity(ctx context.Context, tenantID, subject string) context.Context {
	return auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})
}

// docFixture builds one Upsert input. Tenant is NOT a field — Upsert reads it
// from the identity, so no caller can supply one.
func docFixture(tenantID, seed string, size int64) document.Document {
	h := hashHex(seed)
	name, ct := seed+".csv", "text/csv"
	return document.Document{
		StorageKey:          document.StorageKey(tenantID, h),
		ContentHash:         h,
		SizeBytes:           size,
		Filename:            &name,
		DeclaredContentType: &ct,
	}
}

// --- AC-4: Get resolves only RLS-visible rows ------------------------------

func TestStoreGet_CrossTenantIsNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := document.NewStore(app)

	tenantA := seedTenant(t, super, "doc get cross-tenant A")
	tenantB := seedTenant(t, super, "doc get cross-tenant B")
	cA := identity(ctx, tenantA, uuid.NewString())
	cB := identity(ctx, tenantB, uuid.NewString())

	stored, _, err := store.Upsert(cA, docFixture(tenantA, "cross-tenant", 11))
	if err != nil {
		t.Fatalf("Upsert as A: %v", err)
	}

	// Positive half: the owner resolves it, so the refusal below is a refusal
	// and not a Get that never resolves anything.
	own, err := store.Get(cA, stored.ID)
	if err != nil {
		t.Fatalf("Get as the owning tenant: %v", err)
	}
	if own.ID != stored.ID {
		t.Errorf("Get as owner returned id %q, want %q", own.ID, stored.ID)
	}

	got, err := store.Get(cB, stored.ID)
	if !errors.Is(err, document.ErrNotFound) {
		t.Fatalf("Get of tenant A's document as tenant B = %v, want ErrNotFound", err)
	}
	if got.ID != "" {
		t.Errorf("refused cross-tenant Get returned a populated Document (id %q), want the zero value", got.ID)
	}
}

func TestStoreGet_NonexistentIsNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := document.NewStore(app)

	tenantID := seedTenant(t, super, "doc get nonexistent")
	c := identity(ctx, tenantID, uuid.NewString())

	stored, _, err := store.Upsert(c, docFixture(tenantID, "nonexistent-neighbour", 11))
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, err := store.Get(c, stored.ID); err != nil {
		t.Fatalf("Get of the tenant's own document: %v", err)
	}

	_, err = store.Get(c, uuid.NewString())
	if !errors.Is(err, document.ErrNotFound) {
		t.Fatalf("Get of a well-formed but nonexistent id = %v, want the same ErrNotFound a cross-tenant id yields", err)
	}
}

// --- AC-5: audit rows ride the domain transaction --------------------------

func TestStoreUpsert_WritesCreatedAuditInSameTx(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := document.NewStore(app)

	tenantID := seedTenant(t, super, "doc created audit")
	subject := uuid.NewString()
	c := identity(ctx, tenantID, subject)

	stored, created, err := store.Upsert(c, docFixture(tenantID, "created-audit", 11))
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !created {
		t.Error("Upsert of an unseen content hash reported created = false, want true")
	}
	if stored.ID == "" {
		t.Fatal("Upsert returned an empty id")
	}

	if n := auditCount(t, app, tenantID, "document.created"); n != 1 {
		t.Errorf("document.created audit rows = %d, want 1", n)
	}
	if n := auditCount(t, app, tenantID, "document.reused"); n != 0 {
		t.Errorf("document.reused audit rows after a first store = %d, want 0", n)
	}
	if got := auditActor(t, app, tenantID, "document.created"); got != subject {
		t.Errorf("document.created actor = %q, want the identity Subject %q", got, subject)
	}
}

func TestStoreUpsert_DedupeWritesReusedNotCreated(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := document.NewStore(app)

	tenantID := seedTenant(t, super, "doc reused audit")
	c := identity(ctx, tenantID, uuid.NewString())
	in := docFixture(tenantID, "reused-audit", 11)

	first, created, err := store.Upsert(c, in)
	if err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if !created {
		t.Fatal("first Upsert reported created = false, want true")
	}

	second, created, err := store.Upsert(c, in)
	if err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	if created {
		t.Error("second Upsert of identical content reported created = true, want false")
	}
	if second.ID != first.ID {
		t.Errorf("second Upsert id = %q, want the first row's %q", second.ID, first.ID)
	}

	if n := auditCount(t, app, tenantID, "document.created"); n != 1 {
		t.Errorf("document.created audit rows after a dedupe = %d, want 1 (no second create)", n)
	}
	if n := auditCount(t, app, tenantID, "document.reused"); n != 1 {
		t.Errorf("document.reused audit rows = %d, want 1", n)
	}
}

func TestStoreGet_WritesReadAudit(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := document.NewStore(app)

	tenantID := seedTenant(t, super, "doc read audit")
	subject := uuid.NewString()
	c := identity(ctx, tenantID, subject)

	stored, _, err := store.Upsert(c, docFixture(tenantID, "read-audit", 11))
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, err := store.Get(c, stored.ID); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if n := auditCount(t, app, tenantID, "document.read"); n != 1 {
		t.Errorf("document.read audit rows = %d, want 1", n)
	}
	if n := auditCitingCount(t, app, tenantID, "document.read", stored.ID); n != 1 {
		t.Errorf("document.read rows citing %s = %d, want 1", stored.ID, n)
	}
	if got := auditActor(t, app, tenantID, "document.read"); got != subject {
		t.Errorf("document.read actor = %q, want the identity Subject %q", got, subject)
	}
}

// A refused read must not leak that the id exists into the trail.
func TestStoreGet_CrossTenantWritesNoAudit(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := document.NewStore(app)

	tenantA := seedTenant(t, super, "doc no-audit A")
	tenantB := seedTenant(t, super, "doc no-audit B")
	cA := identity(ctx, tenantA, uuid.NewString())
	cB := identity(ctx, tenantB, uuid.NewString())

	stored, _, err := store.Upsert(cA, docFixture(tenantA, "no-audit", 11))
	if err != nil {
		t.Fatalf("Upsert as A: %v", err)
	}

	if _, err := store.Get(cB, stored.ID); !errors.Is(err, document.ErrNotFound) {
		t.Fatalf("cross-tenant Get = %v, want ErrNotFound", err)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM audit_log WHERE tenant_id = $1`, tenantB); n != 0 {
		t.Errorf("audit_log rows under the refused tenant = %d, want 0", n)
	}
	if n := mustCount(t, super,
		`SELECT count(*) FROM audit_log WHERE event = 'document.read' AND payload::text LIKE '%' || $1 || '%'`,
		stored.ID,
	); n != 0 {
		t.Errorf("document.read rows citing the refused id = %d, want 0 (under any tenant)", n)
	}
}

// A 256-char Subject passes every documents CHECK and fails audit_log's
// audit_actor_length (23514), so the failure lands after the domain INSERT.
func TestStoreUpsert_AuditFailureRollsBackRow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := document.NewStore(app)

	tenantID := seedTenant(t, super, "doc audit rollback")
	c := identity(ctx, tenantID, strings.Repeat("a", 256))

	_, _, err := store.Upsert(c, docFixture(tenantID, "audit-rollback", 11))
	if err == nil {
		t.Fatal("Upsert with a 256-char actor succeeded, want the audit_log CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Errorf("Upsert with a 256-char actor: pgCode = %q, want 23514 (check_violation): %v", code, err)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM documents WHERE tenant_id = $1`, tenantID); n != 0 {
		t.Errorf("documents rows after a failed audit write = %d, want 0 (the domain write rolls back)", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM audit_log WHERE tenant_id = $1`, tenantID); n != 0 {
		t.Errorf("audit_log rows after a failed audit write = %d, want 0", n)
	}
}

// --- AC-6: a malformed uuid is a validation error, not a 500 ----------------

func TestStoreGet_MalformedUUIDIsValidationError(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := document.NewStore(app)

	tenantID := seedTenant(t, super, "doc malformed uuid")
	c := identity(ctx, tenantID, uuid.NewString())

	got, err := store.Get(c, "not-a-uuid")
	if !errors.Is(err, document.ErrValidation) {
		t.Fatalf("Get(%q) = %v, want ErrValidation", "not-a-uuid", err)
	}
	if got.ID != "" {
		t.Errorf("Get on a malformed id returned a populated Document (id %q), want the zero value", got.ID)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM audit_log WHERE tenant_id = $1`, tenantID); n != 0 {
		t.Errorf("audit_log rows after a malformed-id Get = %d, want 0", n)
	}
}

// --- AC-7: the rls CI job must run this package ----------------------------

// Without the step the whole package is untested in CI, and rls-test-gate.sh is
// the only thing that fails the build on a DB test that silently SKIPs.
func TestDocument_CIRLSJobRunsThisPackage(t *testing.T) {
	root, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	path := filepath.Join(strings.TrimSpace(string(root)), ".github", "workflows", "ci.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	lines := strings.Split(string(raw), "\n")
	var inJobs, inRLS bool
	var block []string
	for _, line := range lines {
		if strings.HasPrefix(line, "jobs:") {
			inJobs = true
			continue
		}
		if !inJobs {
			continue
		}
		// A new two-space key ends the rls job's block.
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") && strings.TrimSpace(line) != "" {
			inRLS = strings.TrimSpace(line) == "rls:"
			continue
		}
		if inRLS {
			block = append(block, line)
		}
	}
	if len(block) == 0 {
		t.Fatal("no `rls:` job found in .github/workflows/ci.yml — every assertion below would pass vacuously")
	}

	body := strings.Join(block, "\n")
	// The importer step is the model AC-7 names; finding it proves the block
	// extraction above actually captured the job's steps.
	if !strings.Contains(body, "./internal/importer/...") {
		t.Fatalf("the extracted rls job does not run ./internal/importer/... — the block scan is broken, so the "+
			"assertion below is vacuous:\n%s", body)
	}

	var found bool
	for _, line := range block {
		if strings.Contains(line, "scripts/ci/rls-test-gate.sh") && strings.Contains(line, "./internal/document/...") {
			found = true
			break
		}
	}
	if !found {
		t.Error("the rls job has no `scripts/ci/rls-test-gate.sh ... ./internal/document/...` step — the package's " +
			"DB-backed tests would either not run at all or SKIP into a green build")
	}
}
