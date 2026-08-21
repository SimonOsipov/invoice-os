package actor_test

// resolve_test.go: actor.Resolve against a real Postgres as invoice_app, per the
// RLS testing convention. Acceptance specs first, then the adversarial set.
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

	// Live positive control: the SAME subjects on a SCOPED tx from the same pool
	// do resolve. Without it every assertion above would stay green against a
	// database whose memberships table is empty.
	ctrl := mustResolve(t, scopedTx(t, traced, rec, okaforTenantID), subjects)
	assertLabel(t, ctrl, okaforAdmin, person("Chinedu Okafor"))
	assertLabel(t, ctrl, okaforSuspended, person("Halima Yusuf"))
	assertLabel(t, ctrl, honeywellAdmin, raw(honeywellAdmin))
}

// --- QA Mode B: adversarial coverage ---------------------------------------
// Added after the AC specs above went green. Each test below kills a mutation the
// AC set survived; the mutation is named in the comment that opens it.

// blankMembershipCols writes name/email onto a membership row and restores the
// originals at test end. It fails if the row already looks blank, so the caller's
// fall-through assertion cannot pass on a fixture that never changed.
//
// name/email are *string so a caller can write true SQL NULL and not only "":
// both columns are bare nullable text
// (migrations/20260808140706_memberships_status_and_identity.sql:7-8), and
// TestActorResolve_NullColumnsFallThroughToTheSubject needs that rung.
func blankMembershipCols(t *testing.T, super *pgxpool.Pool, userID string, name, email *string) {
	t.Helper()
	ctx := context.Background()

	var origName, origEmail *string
	if err := super.QueryRow(ctx,
		`SELECT display_name, email FROM memberships WHERE tenant_id = $1 AND user_id = $2`,
		okaforTenantID, userID).Scan(&origName, &origEmail); err != nil {
		t.Fatalf("read the fixture row %s (did db/seed.dev.sql run?): %v", userID, err)
	}
	if origName == nil || *origName == "" || origEmail == nil || *origEmail == "" {
		t.Fatalf("fixture row %s starts with display_name=%v email=%v; the change below would not be observable", userID, origName, origEmail)
	}
	restoreName, restoreEmail := *origName, *origEmail
	t.Cleanup(func() {
		if _, err := super.Exec(context.Background(),
			`UPDATE memberships SET display_name = $1, email = $2 WHERE tenant_id = $3 AND user_id = $4`,
			restoreName, restoreEmail, okaforTenantID, userID); err != nil {
			t.Errorf("restore the fixture row %s: %v", userID, err)
		}
	})

	if _, err := super.Exec(ctx,
		`UPDATE memberships SET display_name = $1, email = $2 WHERE tenant_id = $3 AND user_id = $4`,
		name, email, okaforTenantID, userID); err != nil {
		t.Fatalf("write the fixture columns: %v", err)
	}
	var took bool
	if err := super.QueryRow(ctx,
		`SELECT display_name IS NOT DISTINCT FROM $1 AND email IS NOT DISTINCT FROM $2
		   FROM memberships WHERE tenant_id = $3 AND user_id = $4`,
		name, email, okaforTenantID, userID).Scan(&took); err != nil {
		t.Fatalf("read back the fixture columns: %v", err)
	}
	if !took {
		t.Fatalf("fixture row %s did not take display_name=%s email=%s", userID, showCol(name), showCol(email))
	}
}

// showCol renders a nullable column for a failure message: %q on a *string prints
// an address, and NULL must be distinguishable from "".
func showCol(p *string) string {
	if p == nil {
		return "NULL"
	}
	return fmt.Sprintf("%q", *p)
}

// Kills `subject == systemActor` -> strings.EqualFold. The contract is the exact
// byte string (§2 table, "case-sensitive"): "System" is a person who named
// themselves that, and must render as raw text, not as the system pseudo-actor.
// Kind is the only thing separating the two, since both carry Text "System".
func TestActorResolve_SystemMatchIsExactAndCaseSensitive(t *testing.T) {
	_, _ = dbTestPools(t)
	traced, rec := tracedAppPool(t)
	tx := scopedTx(t, traced, rec, okaforTenantID)

	nearMisses := []string{"System", "SYSTEM", "sYsTeM", " system", "system ", "systems", "system\n"}
	if len(nearMisses) == 0 {
		t.Fatal("empty near-miss corpus — this test would pass vacuously")
	}

	got := mustResolve(t, tx, append([]string{"system"}, nearMisses...))

	// Positive control: the exact literal still classifies, so a blanket
	// "everything is raw" implementation cannot satisfy this test.
	assertLabel(t, got, "system", actor.Label{Text: "System", Kind: actor.KindSystem})
	for _, s := range nearMisses {
		assertLabel(t, got, s, raw(s))
	}
	if n := len(rec.mentioning("memberships")); n != 0 {
		t.Errorf("Resolve issued %d memberships statement(s) for system and its near misses, want 0; statements: %q", n, rec.all())
	}
}

// Kills Name(displayName, email, subject) -> Name(..., userID). The last rung must
// echo the caller's subject byte for byte (actor.go's "never parsed, normalised or
// truncated"), NOT the canonical user_id Postgres scanned back. Only a spelling
// that differs from the stored text can tell the two apart, so the seeded
// canonical subjects every other test uses are blind to this.
func TestActorResolve_RawFallbackEchoesTheSubjectNotTheStoredID(t *testing.T) {
	super, _ := dbTestPools(t)
	braced := "{" + strings.ToUpper(okaforBlankable) + "}"

	t.Run("a resolvable row names a non-canonical spelling", func(t *testing.T) {
		traced, rec := tracedAppPool(t)
		tx := scopedTx(t, traced, rec, okaforTenantID)

		got := mustResolve(t, tx, []string{braced})
		assertLabel(t, got, braced, person("Chiamaka Nwosu"))
	})

	t.Run("an unnameable row echoes that same spelling verbatim", func(t *testing.T) {
		blankMembershipCols(t, super, okaforBlankable, ptr(""), ptr(""))
		traced, rec := tracedAppPool(t)
		tx := scopedTx(t, traced, rec, okaforTenantID)

		got := mustResolve(t, tx, []string{braced, okaforAdmin})

		// Live positive control: this tx does resolve, so the fall-through below
		// is the blanked columns and not an empty result set.
		assertLabel(t, got, okaforAdmin, person("Chinedu Okafor"))
		assertLabel(t, got, braced, raw(braced))
		if got[braced].Text == okaforBlankable {
			t.Errorf("raw fallback returned the stored user_id %q instead of the subject %q", okaforBlankable, braced)
		}
		s := membershipsStmts(t, rec, 1)[0]
		if n := boundArrayLen(t, s); n != 2 {
			t.Errorf("bound array holds %d element(s), want 2 — the braced spelling must be bound, not filtered", n)
		}
	})
}

// Kills Label{Text: subject} -> a truncated subject. audit_actor_length
// (migrations/20260708062657_audit_log.sql:60) admits actors up to 255 characters,
// and every other subject in this suite is under 40 — so nothing else here would
// notice a length cap. uuid_in is the oracle for the gate's verdict.
func TestActorResolve_NearUUIDAtTheActorLengthCeiling(t *testing.T) {
	super, _ := dbTestPools(t)

	// Right character class, right case, wrong length: 255 lowercase hex digits.
	longHex := strings.Repeat("abcd", 63) + "abc"
	// A matched brace pair at the ceiling: strips to 253 hex digits, still no uuid.
	bracedLong := "{" + strings.Repeat("abcd", 63) + "a}"

	subjects := []string{longHex, bracedLong}
	for _, s := range subjects {
		if len(s) != 255 {
			t.Fatalf("subject is %d bytes, want exactly the 255-character audit_actor_length ceiling", len(s))
		}
		if _, accepted := uuidIn(t, super, s); accepted {
			t.Fatalf("uuid_in ACCEPTS %d-byte subject %q — the corpus is wrong, not the gate", len(s), s)
		}
	}

	traced, rec := tracedAppPool(t)
	tx := scopedTx(t, traced, rec, okaforTenantID)

	got := mustResolve(t, tx, append(subjects, okaforAdmin))

	// Live positive control: the query still ran for the real uuid beside them.
	assertLabel(t, got, okaforAdmin, person("Chinedu Okafor"))
	for _, s := range subjects {
		assertLabel(t, got, s, raw(s))
		if len(got[s].Text) != 255 {
			t.Errorf("Resolve[255-byte subject].Text is %d bytes — the subject was truncated, not echoed", len(got[s].Text))
		}
	}
	s := membershipsStmts(t, rec, 1)[0]
	if n := boundArrayLen(t, s); n != 1 {
		t.Errorf("bound array holds %d element(s), want 1 — neither 255-byte near-uuid may be bound", n)
	}
}

// Kills the A5 brace-strip length guard a second time, and pins the degenerate
// inputs no caller should produce. audit_actor_length forbids a stored "" outright,
// so this is defensive: Resolve must key it, not panic on it and not bind it.
func TestActorResolve_NilEmptyAndBlankSubjects(t *testing.T) {
	_, _ = dbTestPools(t)

	t.Run("nil slice", func(t *testing.T) {
		traced, rec := tracedAppPool(t)
		tx := scopedTx(t, traced, rec, okaforTenantID)

		got, err := actor.Resolve(context.Background(), tx, nil)
		if err != nil {
			t.Fatalf("Resolve(nil) = error %v", err)
		}
		if got == nil {
			t.Error("Resolve(nil) returned a nil map; callers index it without a nil check")
		}
		if len(got) != 0 {
			t.Errorf("Resolve(nil) returned %d entries, want 0", len(got))
		}
		if n := len(rec.mentioning("memberships")); n != 0 {
			t.Errorf("Resolve(nil) issued %d memberships statement(s), want 0", n)
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		traced, rec := tracedAppPool(t)
		tx := scopedTx(t, traced, rec, okaforTenantID)

		got, err := actor.Resolve(context.Background(), tx, []string{})
		if err != nil {
			t.Fatalf("Resolve([]) = error %v", err)
		}
		if len(got) != 0 {
			t.Errorf("Resolve([]) returned %d entries, want 0", len(got))
		}
		if n := len(rec.mentioning("memberships")); n != 0 {
			t.Errorf("Resolve([]) issued %d memberships statement(s), want 0", n)
		}
	})

	t.Run("an empty subject beside a real one", func(t *testing.T) {
		traced, rec := tracedAppPool(t)
		tx := scopedTx(t, traced, rec, okaforTenantID)

		got := mustResolve(t, tx, []string{"", okaforAdmin})

		// Live positive control: "" must not poison the bind for its neighbour.
		assertLabel(t, got, okaforAdmin, person("Chinedu Okafor"))
		assertLabel(t, got, "", actor.Label{Text: "", Kind: actor.KindRaw})
		s := membershipsStmts(t, rec, 1)[0]
		if n := boundArrayLen(t, s); n != 1 {
			t.Errorf("bound array holds %d element(s), want 1 — \"\" must never be bound", n)
		}
	})
}

// Pins CURRENT behaviour, deliberately: a whitespace-only display_name is not ""
// to Name's ladder, so it renders as a blank cell with Kind person. That is
// AUDIT-02-01's open finding and NOT this subtask's to change — D-31 settled the
// "" case only. If a later story trims, this test is the one that must be edited,
// which is the point of pinning it.
func TestActorResolve_WhitespaceOnlyDisplayNameIsNotTreatedAsBlank(t *testing.T) {
	super, _ := dbTestPools(t)
	blankMembershipCols(t, super, okaforBlankable, ptr("   "), ptr("c.nwosu@okafor.ng"))

	traced, rec := tracedAppPool(t)
	tx := scopedTx(t, traced, rec, okaforTenantID)

	got := mustResolve(t, tx, []string{okaforBlankable, okaforAdmin})

	// Live positive control: the query resolved a neighbour, so the label below
	// came from the row and not from a miss.
	assertLabel(t, got, okaforAdmin, person("Chinedu Okafor"))
	assertLabel(t, got, okaforBlankable, person("   "))
	if strings.TrimSpace(got[okaforBlankable].Text) != "" {
		t.Errorf("Resolve[%s].Text = %q; this test pins the UNTRIMMED behaviour — if trimming shipped, change this test deliberately",
			okaforBlankable, got[okaforBlankable].Text)
	}
}

// Kills the `if _, seen := subjectsOf[norm]; !seen` de-duplication on its own.
// TestActorResolve_DuplicateSubjectsBindOnce repeats one raw spelling, which the
// raw-subject short-circuit collapses before the normalised de-duplication is ever
// reached — so that test survives dropping it. AC-7's real page mixes spellings:
// 40 actors over 100 events, every raw subject distinct, still one query.
func TestActorResolve_FortyActorsInMixedSpellingsBindFortyInOneQuery(t *testing.T) {
	_, _ = dbTestPools(t)
	traced, rec := tracedAppPool(t)
	tx := scopedTx(t, traced, rec, okaforTenantID)

	actors := []string{okaforAdmin, okaforPreparer, okaforBlankable, okaforSuspended}
	for i := len(actors); i < 40; i++ {
		actors = append(actors, fmt.Sprintf("a0000000-0000-4000-8000-%012d", i))
	}
	spell := []func(string) string{
		func(s string) string { return s },
		strings.ToUpper,
		func(s string) string { return "{" + s + "}" },
	}

	var subjects []string
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		s := spell[i/40](actors[i%40])
		if seen[s] {
			t.Fatalf("spelling %q repeats; this test needs 100 DISTINCT raw subjects so the raw short-circuit cannot do the de-duplication", s)
		}
		seen[s] = true
		subjects = append(subjects, s)
	}
	if len(subjects) != 100 || len(actors) != 40 {
		t.Fatalf("built %d subjects over %d actors, want 100 over 40", len(subjects), len(actors))
	}

	got := mustResolve(t, tx, subjects)

	if len(got) != 100 {
		t.Errorf("Resolve returned %d entries for 100 distinct spellings, want 100", len(got))
	}
	s := membershipsStmts(t, rec, 1)[0]
	if n := boundArrayLen(t, s); n != 40 {
		t.Errorf("bound array holds %d element(s), want 40 — 100 spellings of 40 actors must bind each actor once", n)
	}
	// Live positive control across all three spellings of one seeded actor.
	for _, f := range spell {
		assertLabel(t, got, f(okaforAdmin), person("Chinedu Okafor"))
	}
	for _, sub := range subjects {
		if got[sub].Text == "" {
			t.Errorf("Resolve[%q].Text is empty — a blank cell, not a fallback", sub)
		}
	}
}

// Kills a chunked bind loop at resolve.go:75-96 —
// `for i := 0; i < len(bind); i += 100 { tx.Query(ctx, ..., bind[i:min(i+100, len(bind))]) }`
// — which every other test in this suite survives, because it still issues one
// statement at N=40 and only splits at N=500. The pinned number is a statement
// count within ONE Resolve call, not a per-process budget.
//
// AC-7's own scale (100 events over 40 actors, mixed spellings) is already pinned
// by TestActorResolve_FortyActorsInMixedSpellingsBindFortyInOneQuery, and the
// Store-layer bound by TestHistory_IssuesOneResolveQueryForManyRows
// (internal/invoice/history_actor_test.go:358). Neither is duplicated here: this
// test adds only the rung that proves the bound is CONSTANT in N.
func TestActorResolve_QueryCountIsConstantInN(t *testing.T) {
	_, _ = dbTestPools(t)

	sizes := []int{1, 40, 500}
	if len(sizes) == 0 {
		t.Fatal("empty table — this test would pass vacuously")
	}

	for _, n := range sizes {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			// okaforAdmin leads every rung as the live positive control: without a
			// subject that actually resolves, the count below would stay green
			// against a Resolve that names nothing.
			subjects := []string{okaforAdmin}
			for i := 1; i < n; i++ {
				subjects = append(subjects, fmt.Sprintf("a0000000-0000-4000-8000-%012d", i))
			}
			if len(subjects) != n {
				t.Fatalf("built %d subjects, want exactly %d", len(subjects), n)
			}

			traced, rec := tracedAppPool(t)
			tx := scopedTx(t, traced, rec, okaforTenantID)

			got := mustResolve(t, tx, subjects)

			if len(got) != n {
				t.Errorf("Resolve returned %d entries for %d subjects, want %d — a key was dropped at scale", len(got), n, n)
			}
			assertLabel(t, got, okaforAdmin, person("Chinedu Okafor"))

			s := membershipsStmts(t, rec, 1)[0]
			if bound := boundArrayLen(t, s); bound != n {
				t.Errorf("bound array holds %d element(s), want %d — every subject must ride the ONE statement", bound, n)
			}
		})
	}
}

// Kills resolve.go:85 `var displayName, email *string` -> non-pointer string: pgx
// cannot scan NULL into it ("cannot scan NULL into *string"), so Resolve returns
// an error. Nothing else here reaches that mutant, because every other DB-backed
// fall-through in this suite writes "" and never SQL NULL.
//
// It also kills actor.go:36 `if displayName != nil && *displayName != ""` -> `if
// *displayName != ""` (nil deref), but does not do so alone: TestActorName_
// FallsBackToEmail already panics on that one at the pure layer.
//
// Both columns are bare nullable text with no NOT NULL and no CHECK
// (migrations/20260808140706_memberships_status_and_identity.sql:7-8), so a row
// with both NULL is a legal state APPR-15's PATCH surface can produce. The row
// EXISTS, so Resolve overwrites its raw placeholder with Name(...) — which makes
// this AC-5's worst-shaped no-fabrication case and not the departed member of
// TestActorResolve_RawFallbackEchoesTheSubjectNotTheStoredID.
//
// memberships cross-tenant isolation is proven at
// internal/platform/db/memberships_rls_test.go:607 and e2e/api/isolation.spec.ts
// (AC2); neither is duplicated here.
func TestActorResolve_NullColumnsFallThroughToTheSubject(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	blankMembershipCols(t, super, okaforBlankable, nil, nil)

	// The columns must be NULL and not "", or this test silently re-runs D-31's
	// TestActorResolve_StoredEmptyStringFallsThrough; and the row must be PRESENT,
	// or the fall-through below is the departed-member path instead.
	var present int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM memberships
		  WHERE tenant_id = $1 AND user_id = $2
		    AND display_name IS NULL AND email IS NULL`,
		okaforTenantID, okaforBlankable).Scan(&present); err != nil {
		t.Fatalf("read back the NULLed columns: %v", err)
	}
	if present != 1 {
		t.Fatalf("%d membership row(s) for %s have display_name IS NULL AND email IS NULL, want exactly 1", present, okaforBlankable)
	}

	traced, rec := tracedAppPool(t)
	tx := scopedTx(t, traced, rec, okaforTenantID)

	got := mustResolve(t, tx, []string{okaforBlankable, okaforAdmin})

	// Live positive control in the SAME call: this tx does resolve names, so the
	// fall-through is the NULL columns and not an empty result set.
	assertLabel(t, got, okaforAdmin, person("Chinedu Okafor"))
	assertLabel(t, got, okaforBlankable, raw(okaforBlankable))
	if got[okaforBlankable].Text == "" {
		t.Errorf("Resolve[%s].Text is empty — a NULL row falls through to the subject, never to a blank cell (AC-5)", okaforBlankable)
	}

	// The NULL row must be BOUND and returned, not skipped: a Resolve that never
	// queried it would reach the same raw label by the wrong mechanism.
	s := membershipsStmts(t, rec, 1)[0]
	if n := boundArrayLen(t, s); n != 2 {
		t.Errorf("bound array holds %d element(s), want 2 — the NULL row must be bound", n)
	}
}
