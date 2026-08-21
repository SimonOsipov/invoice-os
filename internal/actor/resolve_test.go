package actor_test

// resolve_test.go: AUDIT-02-02 (task-607) RED specs (QA Mode A) for actor.Resolve,
// against a real Postgres as invoice_app, per the RLS testing convention.
//
// Every DB-backed test here self-skips without DATABASE_URL +
// DATABASE_SUPERUSER_URL + DATABASE_MIGRATION_URL. Run locally with
// `DEV_DB_PORT=5433 make test-actor`; in CI the rls job's gate step fails the build
// on any skip (TestActor_CIRLSJobRunsThisPackage guards that the step exists).
//
// No testify. External test package: Resolve, Name, Kind and Label are the surface.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	"github.com/SimonOsipov/invoice-os/internal/actor"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- fixtures --------------------------------------------------------------

// db/seed.dev.sql's two persona tenants and the memberships they own. The rls CI
// job bootstraps and migrates but never runs the seed file, so TestMain applies it
// (same call and reason as internal/demopolicy/demopolicy_test.go:1431).
const (
	okaforTenantID    = "11111111-1111-1111-1111-111111111111"
	honeywellTenantID = "22222222-2222-2222-2222-222222222222"

	okaforAdmin        = "c0000000-0000-0000-0000-000000000001" // Chinedu Okafor
	okaforPreparer     = "c0000000-0000-0000-0000-000000000003" // Folake Adesina
	okaforBlankable    = "c0000000-0000-0000-0000-000000000005" // Chiamaka Nwosu, the D-31 fixture
	okaforSuspended    = "c0000000-0000-0000-0000-000000000007" // Halima Yusuf, suspended
	honeywellAdmin     = "c0000000-0000-0000-0000-000000000002" // Ngozi Balogun
	honeywellSuspended = "c0000000-0000-0000-0000-000000000012" // Adebayo Ogunlesi, suspended
)

// The free-text actors real writers store: internal/importer/backfill.go:18 and
// internal/invoice/actor.go:54.
var freeTextActors = []string{"backfill-source-rows", "revalidate-rule-set"}

// TestMain establishes db/seed.dev.sql's personas when the DSNs are present, so
// this suite behaves identically locally and in the rls job. db.Seed re-anchors
// created_at and re-enables every rule, which is why the ci.yml step must stay
// ahead of only the TestSeed step (asserted by TestActor_CIRLSJobRunsThisPackage).
func TestMain(m *testing.M) {
	if dsnsPresent() {
		if err := db.Seed(context.Background(), os.Getenv("DATABASE_SUPERUSER_URL"), dbsql.FS); err != nil {
			fmt.Fprintf(os.Stderr, "actor suite: db.Seed (establish the persona memberships): %v\n", err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

func dsnsPresent() bool {
	return os.Getenv("DATABASE_URL") != "" &&
		os.Getenv("DATABASE_SUPERUSER_URL") != "" &&
		os.Getenv("DATABASE_MIGRATION_URL") != ""
}

// --- harness ---------------------------------------------------------------

// dbTestPools returns the superuser (seed + read-back) and app-role (Resolve)
// pools, or skips. All three DSNs are required even though this package reads only
// two, so a partial export skips the WHOLE package rather than half of it — the
// internal/submission silent-skip trap. Gate copied from
// internal/approval/workflow_roles_test.go:41-67.
func dbTestPools(t *testing.T) (super, app *pgxpool.Pool) {
	t.Helper()
	if !dsnsPresent() {
		t.Skip("actor db-integration test skipped: set DATABASE_URL, DATABASE_SUPERUSER_URL and DATABASE_MIGRATION_URL (or run `make test-actor`)")
	}
	ctx := context.Background()

	s, err := pgxpool.New(ctx, os.Getenv("DATABASE_SUPERUSER_URL"))
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("ping superuser (is the DB up and bootstrapped?): %v", err)
	}

	a, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	t.Cleanup(a.Close)

	return s, a
}

// stmt is one recorded statement. Ported from internal/approval's sqlRecorder
// (workflow_roles_test.go:279-301), extended to keep Args: AC-7 and A4 are
// assertions about the BOUND ARRAY's length, which the SQL text cannot show.
type stmt struct {
	sql  string
	args []any
}

type sqlRecorder struct {
	mu    sync.Mutex
	stmts []stmt
}

func (r *sqlRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	r.mu.Lock()
	r.stmts = append(r.stmts, stmt{sql: d.SQL, args: d.Args})
	r.mu.Unlock()
	return ctx
}

func (r *sqlRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (r *sqlRecorder) reset() {
	r.mu.Lock()
	r.stmts = nil
	r.mu.Unlock()
}

// mentioning filters to the statements containing substr, which keeps the count
// immune to the pool's own begin/commit/health-check traffic and to the
// set_config the tenant-scoped tx issues.
func (r *sqlRecorder) mentioning(substr string) []stmt {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []stmt
	for _, s := range r.stmts {
		if strings.Contains(s.sql, substr) {
			out = append(out, s)
		}
	}
	return out
}

func (r *sqlRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.stmts))
	for _, s := range r.stmts {
		out = append(out, s.sql)
	}
	return out
}

// tracedAppPool is a second app-role pool whose statements are recorded. Callers
// must already have gone through dbTestPools, which owns the skip gate.
func tracedAppPool(t *testing.T) (*pgxpool.Pool, *sqlRecorder) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	rec := &sqlRecorder{}
	cfg.ConnConfig.Tracer = rec
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect traced app pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p, rec
}

// scopedTx begins a tx and sets app.current_tenant exactly as
// db.WithinTenantTx (internal/platform/db/db.go:62) does — the posture Resolve's
// production caller is in. Rolled back at test end; the recorder is reset so the
// set_config never lands in an assertion.
func scopedTx(t *testing.T, pool *pgxpool.Pool, rec *sqlRecorder, tenantID string) pgx.Tx {
	t.Helper()
	tx := beginTx(t, pool)
	if _, err := tx.Exec(context.Background(),
		"SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
		t.Fatalf("set app.current_tenant=%s: %v", tenantID, err)
	}
	if rec != nil {
		rec.reset()
	}
	return tx
}

func beginTx(t *testing.T, pool *pgxpool.Pool) pgx.Tx {
	t.Helper()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

// membershipsStmt asserts exactly want statements touched memberships and returns
// them, so no caller indexes into an empty slice.
func membershipsStmts(t *testing.T, rec *sqlRecorder, want int) []stmt {
	t.Helper()
	got := rec.mentioning("memberships")
	if len(got) != want {
		t.Fatalf("Resolve issued %d memberships statement(s), want %d; all statements: %q", len(got), want, rec.all())
	}
	return got
}

// boundArrayLen returns the length of the one slice argument s bound — the size of
// the `= ANY($1::uuid[])` array, whatever concrete element type the implementation
// chose. []byte is excluded: that is a scalar, not the array.
func boundArrayLen(t *testing.T, s stmt) int {
	t.Helper()
	for _, a := range s.args {
		v := reflect.ValueOf(a)
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() != reflect.Uint8 {
			return v.Len()
		}
	}
	t.Fatalf("the memberships statement bound no slice argument, so `= ANY($1::uuid[])` was not used: sql=%q args=%v", s.sql, s.args)
	return 0
}

// uuidIn asks Postgres itself whether a spelling is a uuid, and what it normalises
// to. The parameter is cast text->uuid SERVER-side so this is uuid_in's verdict,
// not pgx's client-side encoder. Each probe is its own implicit transaction, so a
// rejection poisons nothing.
func uuidIn(t *testing.T, super *pgxpool.Pool, s string) (canonical string, accepted bool) {
	t.Helper()
	err := super.QueryRow(context.Background(), "SELECT (($1::text)::uuid)::text", s).Scan(&canonical)
	if err == nil {
		return canonical, true
	}
	if pgCode(err) == "22P02" {
		return "", false
	}
	t.Fatalf("uuid_in probe for %q failed with something other than 22P02: %v", s, err)
	return "", false
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func person(text string) actor.Label { return actor.Label{Text: text, Kind: actor.KindPerson} }
func raw(text string) actor.Label    { return actor.Label{Text: text, Kind: actor.KindRaw} }

// mustResolve fails on error rather than returning it: every case here is a
// success case, and a 22P02 would abort the caller's transaction.
func mustResolve(t *testing.T, tx pgx.Tx, subjects []string) map[string]actor.Label {
	t.Helper()
	got, err := actor.Resolve(context.Background(), tx, subjects)
	if err != nil {
		t.Fatalf("Resolve(%d subjects) = error %v (SQLSTATE %q)", len(subjects), err, pgCode(err))
	}
	if len(got) == 0 && len(subjects) > 0 {
		t.Fatalf("Resolve returned %d entries for %d subjects; every assertion below would read a zero-value Label", len(got), len(subjects))
	}
	return got
}

func assertLabel(t *testing.T, got map[string]actor.Label, subject string, want actor.Label) {
	t.Helper()
	have, ok := got[subject]
	if !ok {
		t.Errorf("subject %q is not a key in the result (AC-2: nothing is silently dropped)", subject)
		return
	}
	if have != want {
		t.Errorf("Resolve[%q] = %+v, want %+v", subject, have, want)
	}
}

// --- specs -----------------------------------------------------------------

// AC-4 shape 1 plus A3's other half: "system" is classified in Go and excluded
// from the bound array, while the uuid beside it still costs exactly one query.
func TestActorResolve_SystemIsClassifiedNotQueried(t *testing.T) {
	_, _ = dbTestPools(t)
	traced, rec := tracedAppPool(t)
	tx := scopedTx(t, traced, rec, okaforTenantID)

	got := mustResolve(t, tx, []string{"system", okaforAdmin})

	assertLabel(t, got, "system", actor.Label{Text: "System", Kind: actor.KindSystem})
	assertLabel(t, got, okaforAdmin, person("Chinedu Okafor"))

	s := membershipsStmts(t, rec, 1)[0]
	if n := boundArrayLen(t, s); n != 1 {
		t.Errorf("bound array holds %d element(s), want 1 — \"system\" must never enter it (AC-3)", n)
	}
}

// §3's 22P02 hazard: binding "backfill-source-rows" into a uuid[] raises
// `invalid input syntax for type uuid: "backfill-source-rows"` and ABORTS the
// caller's transaction, which in production is Store.History's. The gate must
// filter it before the bind, so the tx survives.
func TestActorResolve_FreeTextNeverReachesTheUUIDArray(t *testing.T) {
	_, _ = dbTestPools(t)
	traced, rec := tracedAppPool(t)
	tx := scopedTx(t, traced, rec, okaforTenantID)

	subjects := append(append([]string{}, freeTextActors...), okaforAdmin)
	got, err := actor.Resolve(context.Background(), tx, subjects)
	if err != nil {
		t.Fatalf("Resolve = error %v (SQLSTATE %q); 22P02 means the free-text actors reached the uuid[] bind", err, pgCode(err))
	}

	for _, ft := range freeTextActors {
		assertLabel(t, got, ft, raw(ft))
	}
	assertLabel(t, got, okaforAdmin, person("Chinedu Okafor"))

	s := membershipsStmts(t, rec, 1)[0]
	if n := boundArrayLen(t, s); n != 1 {
		t.Errorf("bound array holds %d element(s), want 1 — only the uuid may be bound", n)
	}

	// A 22P02 would have left the tx aborted (25P02 on the next statement). Prove
	// it is still usable, which no return value can show.
	var one int
	if err := tx.QueryRow(context.Background(), "SELECT 1").Scan(&one); err != nil {
		t.Fatalf("the caller's tx is unusable after Resolve (%v) — it was aborted mid-call", err)
	}
}

// The silent-wrong-answer guard. The Go gate must accept EXACTLY what uuid_in
// accepts: stricter, and a subject the audit trigger indexed renders as raw text
// instead of a name — a wrong answer, not an error. Postgres is the oracle and
// also supplies the canonical form, so this test cannot drift from §6's grammar.
func TestActorResolve_UUIDGateMatchesUUIDIn(t *testing.T) {
	super, _ := dbTestPools(t)

	// "system" is deliberately absent: Resolve classifies it as KindSystem, which
	// is a third verdict this accept/reject table cannot express.
	spellings := []string{
		okaforAdmin,                                // canonical
		strings.ToUpper(okaforAdmin),               // upper-case hex
		"{" + okaforAdmin + "}",                    // braced
		"{" + strings.ToUpper(okaforAdmin) + "}",   // braced + upper
		strings.ReplaceAll(okaforAdmin, "-", ""),   // 32 bare hex digits
		"c000-0000-0000-0000-0000-0000-0000-0001",  // a hyphen after every 4th digit
		"c0000000-00000000-0000-0000-000000000001", // 36 hex digits
		"{" + okaforAdmin,                          // unmatched open brace
		okaforAdmin + "}",                          // unmatched close brace
		"{}",                                       // A5: matched pair, nothing inside
		"{",                                        // A5: LIKE '{%}' cannot match one byte
		"}",
		"",
		" " + okaforAdmin,                       // leading space
		okaforAdmin + " ",                       // trailing space
		"c0000000-0000-0000-0000-00000000000",   // 31 digits
		"c0000000-0000-0000-0000-0000000000012", // 33 digits
		"g0000000-0000-0000-0000-000000000001",  // non-hex
		"a0000000-0000-4000-8000-000000000999",  // valid uuid, no membership row
		freeTextActors[0], freeTextActors[1],
	}
	if len(spellings) == 0 {
		t.Fatal("empty corpus — this test would pass vacuously")
	}

	var accepts, rejects int
	for _, spelling := range spellings {
		canonical, accepted := uuidIn(t, super, spelling)
		if accepted {
			accepts++
		} else {
			rejects++
		}

		t.Run(fmt.Sprintf("%q", spelling), func(t *testing.T) {
			traced, rec := tracedAppPool(t)
			tx := scopedTx(t, traced, rec, okaforTenantID)

			got := mustResolve(t, tx, []string{spelling})

			wantStmts := 0
			if accepted {
				wantStmts = 1
			}
			if n := len(rec.mentioning("memberships")); n != wantStmts {
				t.Errorf("uuid_in %s %q, but Resolve issued %d memberships statement(s), want %d — the Go gate disagrees with uuid_in",
					map[bool]string{true: "ACCEPTS", false: "REJECTS"}[accepted], spelling, n, wantStmts)
			}

			want := raw(spelling)
			if accepted && canonical == okaforAdmin {
				want = person("Chinedu Okafor")
			}
			assertLabel(t, got, spelling, want)
		})
	}

	if accepts == 0 || rejects == 0 {
		t.Fatalf("corpus produced %d accepts and %d rejects; both must be non-zero or the comparison is one-sided", accepts, rejects)
	}
}

// AC-6, the only layer that can observe an RLS qual. The seed shares no subject
// between the two tenants, so the negative uses Honeywell's admin against an
// Okafor-scoped tx — WITH a live positive control in the SAME call: a test that
// passed because the query returned nothing at all would prove nothing.
func TestActorResolve_CrossTenantSubjectIsUnresolvable(t *testing.T) {
	_, _ = dbTestPools(t)
	traced, rec := tracedAppPool(t)
	tx := scopedTx(t, traced, rec, okaforTenantID)

	got := mustResolve(t, tx, []string{honeywellAdmin, okaforAdmin})

	// Positive control first: this tx CAN resolve, so the absence below is RLS's
	// doing and not an empty result set.
	assertLabel(t, got, okaforAdmin, person("Chinedu Okafor"))
	assertLabel(t, got, honeywellAdmin, raw(honeywellAdmin))
	if got[honeywellAdmin].Text == "Ngozi Balogun" {
		t.Errorf("Resolve leaked Honeywell's %q into an Okafor-scoped call", "Ngozi Balogun")
	}

	// The refusal must be RLS, not a shortcut that skipped the bind.
	s := membershipsStmts(t, rec, 1)[0]
	if n := boundArrayLen(t, s); n != 2 {
		t.Errorf("bound array holds %d element(s), want 2 — the cross-tenant subject must be BOUND and refused by RLS, not filtered in Go", n)
	}
}

// D-9: Resolve reads no status column. Split per tenant (the two seeded suspended
// rows live in DIFFERENT tenants, db/seed.dev.sql:46 and :53), so neither half
// needs the RLS-bypassing superuser tx that AC-6 forbids.
func TestActorResolve_SuspendedMemberStillResolves(t *testing.T) {
	_, _ = dbTestPools(t)

	cases := []struct {
		tenantID  string
		subject   string
		want      string
		otherSubj string // the other tenant's suspended row: must NOT resolve here
	}{
		{okaforTenantID, okaforSuspended, "Halima Yusuf", honeywellSuspended},
		{honeywellTenantID, honeywellSuspended, "Adebayo Ogunlesi", okaforSuspended},
	}
	if len(cases) == 0 {
		t.Fatal("empty table — would pass vacuously")
	}

	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			traced, rec := tracedAppPool(t)
			tx := scopedTx(t, traced, rec, tc.tenantID)

			got := mustResolve(t, tx, []string{tc.subject, tc.otherSubj})

			assertLabel(t, got, tc.subject, person(tc.want))
			// Proves the tx is tenant-scoped, not a superuser tx that would
			// satisfy both halves at once.
			assertLabel(t, got, tc.otherSubj, raw(tc.otherSubj))
		})
	}
}

// D-31 against REAL columns: Name's ladder treats a non-nil "" as absent, and the
// only way to prove Resolve carries that through is to write ” into the database
// and read it back. The seed has no such row, so this test makes and restores one.
func TestActorResolve_StoredEmptyStringFallsThrough(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	var origName, origEmail *string
	if err := super.QueryRow(ctx,
		`SELECT display_name, email FROM memberships WHERE tenant_id = $1 AND user_id = $2`,
		okaforTenantID, okaforBlankable).Scan(&origName, &origEmail); err != nil {
		t.Fatalf("read the fixture row (did db/seed.dev.sql run?): %v", err)
	}
	if origName == nil || *origName == "" || origEmail == nil || *origEmail == "" {
		t.Fatalf("fixture row %s starts with display_name=%v email=%v; the fall-through below would not be observable", okaforBlankable, origName, origEmail)
	}
	restoreName, restoreEmail := *origName, *origEmail
	t.Cleanup(func() {
		if _, err := super.Exec(context.Background(),
			`UPDATE memberships SET display_name = $1, email = $2 WHERE tenant_id = $3 AND user_id = $4`,
			restoreName, restoreEmail, okaforTenantID, okaforBlankable); err != nil {
			t.Errorf("restore the fixture row: %v", err)
		}
	})

	setCols := func(t *testing.T, name, email string) {
		t.Helper()
		if _, err := super.Exec(ctx,
			`UPDATE memberships SET display_name = $1, email = $2 WHERE tenant_id = $3 AND user_id = $4`,
			name, email, okaforTenantID, okaforBlankable); err != nil {
			t.Fatalf("blank the fixture columns: %v", err)
		}
		// The columns are nullable; prove '' landed rather than NULL, or the
		// assertion below would be testing the NULL path instead.
		var isEmpty bool
		if err := super.QueryRow(ctx,
			`SELECT display_name IS NOT DISTINCT FROM $1 AND email IS NOT DISTINCT FROM $2
			   FROM memberships WHERE tenant_id = $3 AND user_id = $4`,
			name, email, okaforTenantID, okaforBlankable).Scan(&isEmpty); err != nil {
			t.Fatalf("read back the blanked columns: %v", err)
		}
		if !isEmpty {
			t.Fatalf("fixture row did not take display_name=%q email=%q", name, email)
		}
	}

	t.Run("blank display_name falls through to email", func(t *testing.T) {
		setCols(t, "", restoreEmail)
		traced, rec := tracedAppPool(t)
		tx := scopedTx(t, traced, rec, okaforTenantID)

		got := mustResolve(t, tx, []string{okaforBlankable})
		assertLabel(t, got, okaforBlankable, person(restoreEmail))
	})

	t.Run("blank display_name and email fall through to the raw subject", func(t *testing.T) {
		setCols(t, "", "")
		traced, rec := tracedAppPool(t)
		tx := scopedTx(t, traced, rec, okaforTenantID)

		got := mustResolve(t, tx, []string{okaforBlankable})
		assertLabel(t, got, okaforBlankable, raw(okaforBlankable))
	})
}

// AC-2: a dropped key renders as a blank cell, not as raw text — the failure mode
// nothing downstream can detect. 100 distinct subjects across all four shapes.
func TestActorResolve_EveryInputSubjectIsAKey(t *testing.T) {
	_, _ = dbTestPools(t)
	traced, rec := tracedAppPool(t)
	tx := scopedTx(t, traced, rec, okaforTenantID)

	subjects := []string{"system"}
	subjects = append(subjects, freeTextActors...)
	for i := 0; i < 7; i++ {
		subjects = append(subjects, fmt.Sprintf("free-text-actor-%d", i))
	}
	// The six Okafor memberships resolve; the seven Honeywell ones are visible to
	// nobody on this tx and must come back raw.
	for _, n := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13} {
		subjects = append(subjects, fmt.Sprintf("c0000000-0000-0000-0000-%012d", n))
	}
	for i := len(subjects); i < 100; i++ {
		subjects = append(subjects, fmt.Sprintf("a0000000-0000-4000-8000-%012d", i))
	}
	if len(subjects) != 100 {
		t.Fatalf("built %d subjects, want exactly 100", len(subjects))
	}
	seen := map[string]bool{}
	for _, s := range subjects {
		if seen[s] {
			t.Fatalf("subject %q appears twice; this test needs 100 DISTINCT subjects", s)
		}
		seen[s] = true
	}

	got := mustResolve(t, tx, subjects)

	if len(got) != 100 {
		t.Errorf("Resolve returned %d entries for 100 distinct subjects", len(got))
	}
	for _, s := range subjects {
		label, ok := got[s]
		if !ok {
			t.Errorf("subject %q was dropped from the result", s)
			continue
		}
		if label.Text == "" {
			t.Errorf("Resolve[%q].Text is empty — a blank cell, not a fallback", s)
		}
	}
	assertLabel(t, got, "system", actor.Label{Text: "System", Kind: actor.KindSystem})
	assertLabel(t, got, okaforPreparer, person("Folake Adesina"))
	assertLabel(t, got, honeywellAdmin, raw(honeywellAdmin))
}

// AC-7's de-duplication half: 100 rows over 40 distinct actors bind 40 elements.
func TestActorResolve_DuplicateSubjectsBindOnce(t *testing.T) {
	_, _ = dbTestPools(t)
	traced, rec := tracedAppPool(t)
	tx := scopedTx(t, traced, rec, okaforTenantID)

	distinct := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		distinct = append(distinct, fmt.Sprintf("a0000000-0000-4000-8000-%012d", i))
	}
	subjects := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		subjects = append(subjects, distinct[i%40])
	}

	got := mustResolve(t, tx, subjects)

	if len(got) != 40 {
		t.Errorf("Resolve returned %d entries for 40 distinct actors", len(got))
	}
	s := membershipsStmts(t, rec, 1)[0]
	if n := boundArrayLen(t, s); n != 40 {
		t.Errorf("bound array holds %d element(s), want 40 — 100 rows over 40 actors must bind each actor once", n)
	}
}

// A3: AC-1 reads "at MOST one". A page whose actors are all `system` and free text
// filters to an empty array and must cost zero round trips — binding an empty
// uuid[] would be a wasted one.
func TestActorResolve_EmptyFilteredSetIssuesNoQuery(t *testing.T) {
	_, _ = dbTestPools(t)
	traced, rec := tracedAppPool(t)
	tx := scopedTx(t, traced, rec, okaforTenantID)

	subjects := append([]string{"system"}, freeTextActors...)
	subjects = append(subjects, "rule-set-publish")

	got := mustResolve(t, tx, subjects)

	if n := len(rec.mentioning("memberships")); n != 0 {
		t.Errorf("Resolve issued %d memberships statement(s) for an all-system/free-text page, want 0; statements: %q", n, rec.all())
	}
	assertLabel(t, got, "system", actor.Label{Text: "System", Kind: actor.KindSystem})
	for _, s := range subjects[1:] {
		assertLabel(t, got, s, raw(s))
	}
	if len(got) != len(subjects) {
		t.Errorf("Resolve returned %d entries for %d subjects — zero queries must not mean zero keys", len(got), len(subjects))
	}
}

// A4: de-duplicate on the NORMALISED uuid, key the result on the RAW subject.
// Matching a returned user_id back to inputs by string equality against the stored
// text would drop the braced spelling entirely.
func TestActorResolve_TwoSpellingsOfOneIDBindOnceAndKeyTwice(t *testing.T) {
	_, _ = dbTestPools(t)
	traced, rec := tracedAppPool(t)
	tx := scopedTx(t, traced, rec, okaforTenantID)

	braced := "{" + strings.ToUpper(okaforAdmin) + "}"
	got := mustResolve(t, tx, []string{braced, okaforAdmin})

	assertLabel(t, got, braced, person("Chinedu Okafor"))
	assertLabel(t, got, okaforAdmin, person("Chinedu Okafor"))
	if len(got) != 2 {
		t.Errorf("Resolve returned %d entries, want 2 — both raw spellings are keys", len(got))
	}

	s := membershipsStmts(t, rec, 1)[0]
	if n := boundArrayLen(t, s); n != 1 {
		t.Errorf("bound array holds %d element(s), want 1 — two spellings of one id bind once", n)
	}
}

// A7's negative control, and the only proof that the scope is RLS's alone. A tx
// with no app.current_tenant must resolve NOTHING — every seeded uuid comes back
// raw, with no error. If this ever returns a name, Resolve grew a scope of its own
// or the caller was handed a BYPASSRLS connection.
func TestActorResolve_UnscopedTxResolvesNothingRatherThanWrongly(t *testing.T) {
	_, _ = dbTestPools(t)
	traced, rec := tracedAppPool(t)
	tx := beginTx(t, traced) // deliberately no set_config
	rec.reset()

	// Prove the fixture: a GUC leaked from a pooled connection would make the
	// assertions below test a scoped tx by accident.
	var guc string
	if err := tx.QueryRow(context.Background(),
		"SELECT coalesce(current_setting('app.current_tenant', true), '')").Scan(&guc); err != nil {
		t.Fatalf("read app.current_tenant: %v", err)
	}
	if guc != "" {
		t.Fatalf("app.current_tenant is %q on a tx that never set it — this tx is scoped, so the test proves nothing", guc)
	}
	rec.reset()

	subjects := []string{okaforAdmin, okaforPreparer, okaforSuspended, honeywellAdmin, honeywellSuspended}
	got, err := actor.Resolve(context.Background(), tx, subjects)
	if err != nil {
		t.Fatalf("Resolve on an unscoped tx = error %v (SQLSTATE %q); it must resolve nothing, not fail", err, pgCode(err))
	}

	for _, s := range subjects {
		assertLabel(t, got, s, raw(s))
	}

	// The query DID run — RLS returned nothing. A zero-statement result here would
	// be a different mechanism passing for the right reason.
	s := membershipsStmts(t, rec, 1)[0]
	if n := boundArrayLen(t, s); n != len(subjects) {
		t.Errorf("bound array holds %d element(s), want %d", n, len(subjects))
	}
}
