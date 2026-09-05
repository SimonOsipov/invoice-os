// reader_db_test.go: the request-seam reader's suite (EXTR-07-01). It shares store_db_test.go's
// harness — stRequire is this package's one sanctioned skip site — and seeds every fixture as
// the superuser, because invoice_app holds no DELETE on extraction_jobs and no INSERT on
// tenants.
package extraction_test

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	rdReaderSource     = "reader.go"
	rdCorrectionSource = "handlers_correction.go"
	rdCapName          = "maxJobsPerDocument"

	// A real uuid on purpose: WithinRequestTenantTxOpts (tenant.go:61-63) delegates past the
	// membership gate entirely when Subject does not parse as one, which would make
	// TestRLS_ExtractionReaderRefusesANonActiveMember pass without exercising the gate.
	rdMemberSubject = "e5b10007-0000-4000-8000-000000000001"
	rdMemberRole    = "preparer"
)

// rdStates is every value the state column may hold (migration 20260827084025 lines 19-21).
// Each scan over it asserts it non-empty first: an absence proved over an empty set is not a
// proof.
var rdStates = []string{"queued", "extracting", "succeeded", "failed", "dead_lettered"}

func rdReader(t *testing.T) *extraction.Reader {
	t.Helper()
	return &extraction.Reader{Pool: stRequire(t).app}
}

// rdSeedJob inserts one extraction_jobs row as the superuser (BYPASSRLS, so it needs neither
// tenant context nor an INSERT grant). created_at is named explicitly: the column default
// now() is the TRANSACTION timestamp, so co-seeded rows would share it and the ordering
// assertions could not fail. Cleanup rides stTenant's tenant DELETE via ON DELETE CASCADE.
func rdSeedJob(t *testing.T, ctx context.Context, tenantID, documentID, state string, createdAt time.Time, lastErr *string) string {
	t.Helper()
	return rdSeedJobWithID(t, ctx, uuid.NewString(), tenantID, documentID, state, createdAt, lastErr)
}

// rdSeedJobWithID is rdSeedJob with the caller choosing the id. The ordering case needs the
// tied rows inserted in the reverse of the order it expects back — see
// TestRLS_ExtractionReaderReturnsEveryJobNewestFirst.
func rdSeedJobWithID(t *testing.T, ctx context.Context, id, tenantID, documentID, state string, createdAt time.Time, lastErr *string) string {
	t.Helper()
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO extraction_jobs
		     (id, tenant_id, document_id, state, extractor, extractor_version, created_at, last_error)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, tenantID, documentID, state, stExtractor, stExtractorVersion, createdAt, lastErr,
	); err != nil {
		t.Fatalf("seed extraction job (state %s, created_at %s): %v", state, createdAt.Format(time.RFC3339Nano), err)
	}
	return id
}

// rdTenant is stTenant plus the caller's membership row and a ctx carrying the matching
// Identity. stTenant seeds no membership, and WithinRequestTenantTxOpts refuses a caller
// without an active one before the closure runs, so every happy-path case here needs this.
func rdTenant(t *testing.T, ctx context.Context, status string) (reqCtx context.Context, tenantID, documentID string) {
	t.Helper()
	tenantID, documentID = stTenant(t, ctx)
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, $3, $4)`,
		tenantID, rdMemberSubject, rdMemberRole, status,
	); err != nil {
		t.Fatalf("seed membership (status %s): %v", status, err)
	}
	return auth.WithIdentity(ctx, auth.Identity{
		Subject:  rdMemberSubject,
		Role:     "authenticated",
		TenantID: tenantID,
	}), tenantID, documentID
}

// rdSeedDocument adds a second document to a tenant stTenant already seeded, so a case can
// tell a per-document answer from a per-tenant one.
func rdSeedDocument(t *testing.T, ctx context.Context, tenantID string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO documents (id, tenant_id, storage_key, content_hash, size_bytes)
		 VALUES ($1, $2, $3, $4, $5)`,
		id, tenantID, "extr-07/"+id, strings.Repeat("b", 64), 1024); err != nil {
		t.Fatalf("seed a second document for tenant %s: %v", tenantID, err)
	}
	return id
}

// rdAuditCount counts as the SUPERUSER. An app-pool count is RLS-filtered and would read the
// same whether or not a row was written.
func rdAuditCount(t *testing.T, ctx context.Context, tenantID string) int {
	t.Helper()
	var n int
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1`, tenantID).Scan(&n); err != nil {
		t.Fatalf("count audit_log for tenant %s: %v", tenantID, err)
	}
	return n
}

// rdDeclaredCap reads maxJobsPerDocument out of reader.go rather than trusting a number
// retyped in the test: the cap and the assertion must move together.
func rdDeclaredCap(t *testing.T) int {
	t.Helper()
	f, fset := mxParse(t, rdReaderSource)

	var (
		got   int
		found bool
	)
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range vs.Names {
			if name.Name != rdCapName || i >= len(vs.Values) {
				continue
			}
			bl, ok := vs.Values[i].(*ast.BasicLit)
			if !ok || bl.Kind != token.INT {
				t.Fatalf("%s: %s is not an integer literal", fset.Position(vs.Values[i].Pos()), rdCapName)
			}
			v, err := strconv.Atoi(bl.Value)
			if err != nil {
				t.Fatalf("%s: %s = %s is not an integer: %v", fset.Position(bl.Pos()), rdCapName, bl.Value, err)
			}
			got, found = v, true
		}
		return true
	})
	if !found {
		t.Fatalf("reader.go declares no %s constant", rdCapName)
	}
	return got
}

// rdJobByID indexes a response so an assertion can name the row it means.
func rdJobByID(jobs []extraction.JobState) map[string]extraction.JobState {
	out := make(map[string]extraction.JobState, len(jobs))
	for _, j := range jobs {
		out[j.ID] = j
	}
	return out
}

func rdIDs(jobs []extraction.JobState) []string {
	out := make([]string, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.ID)
	}
	return out
}

// AC 2 and AC 5, production-read-path half: the reader keys on document_id alone, so the
// cross-tenant refusal below is the tenant_isolation policy doing it, not a predicate. The
// positive case is load-bearing — a reader that returned nothing at all would pass without it.
func TestRLS_ExtractionJobsCrossTenantReadRefused(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	_, tenantB, docB := rdTenant(t, ctx, "active")

	now := time.Now().UTC()
	jobA := rdSeedJob(t, ctx, tenantA, docA, rdStates[0], now, nil)
	jobB := rdSeedJob(t, ctx, tenantB, docB, rdStates[0], now, nil)

	out, err := r.JobsForDocument(ctxA, docA)
	if err != nil {
		t.Fatalf("read A's own document: %v", err)
	}
	if got := rdIDs(out.Jobs); len(got) != 1 || got[0] != jobA {
		t.Fatalf("A reading its own document got jobs %v, want [%s]", got, jobA)
	}

	out, err = r.JobsForDocument(ctxA, docB)
	if err != nil {
		t.Fatalf("read B's document while scoped to A: %v", err)
	}
	if out.Jobs == nil {
		t.Error("the cross-tenant read returned a nil Jobs slice; a nil slice marshals to JSON null")
	}
	if len(out.Jobs) != 0 {
		t.Errorf("A reading B's document %s got jobs %v, want none (B seeded %s)", docB, rdIDs(out.Jobs), jobB)
	}
}

// AC 4, error path: no Identity means no read at all. The seeded row is what makes the
// emptiness mean something — an empty answer over an empty table proves nothing.
func TestRLS_ExtractionReaderFailsClosedWithNoIdentity(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	_, tenantID, documentID := rdTenant(t, ctx, "active")
	rdSeedJob(t, ctx, tenantID, documentID, rdStates[0], time.Now().UTC(), nil)

	// The bare ctx, not the one rdTenant returned: no auth.Identity was ever put in it.
	out, err := r.JobsForDocument(ctx, documentID)
	if !errors.Is(err, db.ErrNoTenant) {
		t.Errorf("read without an Identity returned %v, want %v", err, db.ErrNoTenant)
	}
	if out.Jobs == nil {
		t.Error("the error path returned a nil Jobs slice, want []JobState{}")
	}
	if len(out.Jobs) != 0 {
		t.Errorf("read without an Identity returned %d row(s): %v", len(out.Jobs), rdIDs(out.Jobs))
	}
}

// AC 4, error path: a suspended member is refused before the closure runs.
func TestRLS_ExtractionReaderRefusesANonActiveMember(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	ctxSuspended, tenantID, documentID := rdTenant(t, ctx, "suspended")
	rdSeedJob(t, ctx, tenantID, documentID, rdStates[0], time.Now().UTC(), nil)

	// The membership gate is skipped outright for a non-uuid Subject (tenant.go:61-63), and
	// this case would then pass while proving nothing. Pinned here so a later edit to
	// rdMemberSubject cannot silently reopen it.
	id, ok := auth.IdentityFromContext(ctxSuspended)
	if !ok {
		t.Fatal("rdTenant returned a ctx carrying no Identity")
	}
	if _, err := uuid.Parse(id.Subject); err != nil {
		t.Fatalf("Identity.Subject %q is not a uuid, so the membership gate never runs: %v", id.Subject, err)
	}

	out, err := r.JobsForDocument(ctxSuspended, documentID)
	if !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("read as a suspended member returned %v, want %v", err, db.ErrNotActiveMember)
	}
	if out.Jobs == nil {
		t.Error("the error path returned a nil Jobs slice, want []JobState{}")
	}
	if len(out.Jobs) != 0 {
		t.Errorf("read as a suspended member returned %d row(s): %v", len(out.Jobs), rdIDs(out.Jobs))
	}
}

// AC 2 pass-through: the reader hands back whatever the column holds. Go names none of the
// five values, so a state added to the CHECK later needs no code change here.
func TestRLS_ExtractionReaderServesEveryStateVerbatim(t *testing.T) {
	if len(rdStates) == 0 {
		t.Fatal("the state fixture list is empty, so this case would assert nothing")
	}

	ctx := t.Context()
	r := rdReader(t)
	ctxA, tenantID, documentID := rdTenant(t, ctx, "active")

	base := time.Now().UTC().Truncate(time.Microsecond)
	want := make(map[string]string, len(rdStates))
	for i, state := range rdStates {
		id := rdSeedJob(t, ctx, tenantID, documentID, state, base.Add(time.Duration(i)*time.Second), nil)
		want[id] = state
	}

	out, err := r.JobsForDocument(ctxA, documentID)
	if err != nil {
		t.Fatalf("JobsForDocument: %v", err)
	}
	if len(out.Jobs) != len(want) {
		t.Fatalf("got %d row(s) %v, want %d", len(out.Jobs), rdIDs(out.Jobs), len(want))
	}
	got := rdJobByID(out.Jobs)
	for id, state := range want {
		j, ok := got[id]
		if !ok {
			t.Errorf("job %s (state %s) is missing from the response", id, state)
			continue
		}
		if j.State != state {
			t.Errorf("job %s reads state %q, want %q", id, j.State, state)
		}
		if j.DocumentID != documentID {
			t.Errorf("job %s echoes document_id %q, want %q", id, j.DocumentID, documentID)
		}
	}
}

// AC 4, zero-row path: a document with no job and a document that does not exist are the same
// answer, and it is an empty array rather than a nil one.
func TestRLS_ExtractionReaderReturnsEmptyForAnUnknownDocument(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)
	ctxA, _, _ := rdTenant(t, ctx, "active")

	out, err := r.JobsForDocument(ctxA, uuid.NewString())
	if err != nil {
		t.Fatalf("read an unknown document id: %v", err)
	}
	if out.Jobs == nil {
		t.Error("an unknown document returned a nil Jobs slice, want []JobState{}")
	}
	if len(out.Jobs) != 0 {
		t.Errorf("an unknown document returned %d row(s): %v", len(out.Jobs), rdIDs(out.Jobs))
	}
}

// AC 8 ordering: newest created_at first, id DESC breaking a tie. Two rows share one
// created_at on purpose — without a tie the tiebreak clause is never exercised.
func TestRLS_ExtractionReaderReturnsEveryJobNewestFirst(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)
	ctxA, tenantID, documentID := rdTenant(t, ctx, "active")

	base := time.Now().UTC().Truncate(time.Microsecond)
	newest := rdSeedJob(t, ctx, tenantID, documentID, rdStates[0], base, nil)

	// The tied rows are seeded in ASCENDING id order, the reverse of the answer below, and
	// there are four of them. An untiebroken sort returns them in insertion order, so seeding
	// random ids caught a dropped `id DESC` only half the time; this ordering makes the
	// accidental agreement impossible rather than unlikely.
	const tiedRows = 4
	tied := make([]string, 0, tiedRows)
	for range tiedRows {
		tied = append(tied, uuid.NewString())
	}
	// uuid compares bytewise in Postgres, and the canonical lowercase-hex form sorts the same
	// way, so the larger string is the larger uuid.
	slices.Sort(tied)
	for _, id := range tied {
		rdSeedJobWithID(t, ctx, id, tenantID, documentID, rdStates[0], base.Add(-time.Second), nil)
	}

	want := []string{newest}
	for i := len(tied) - 1; i >= 0; i-- {
		want = append(want, tied[i])
	}

	out, err := r.JobsForDocument(ctxA, documentID)
	if err != nil {
		t.Fatalf("JobsForDocument: %v", err)
	}
	if got := rdIDs(out.Jobs); !slices.Equal(got, want) {
		t.Errorf("row order is %v, want %v (created_at DESC, then id DESC)", got, want)
	}
}

// AC 8 cap: a response a client polls every 2s must be bounded, and the rows it keeps must be
// the newest ones rather than an arbitrary 50.
func TestRLS_ExtractionReaderCapsTheRowCount(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	capacity := rdDeclaredCap(t)
	if capacity != 50 {
		t.Fatalf("%s = %d, want 50", rdCapName, capacity)
	}
	const seeded = 60
	if seeded <= capacity {
		t.Fatalf("the fixture seeds %d row(s) against a cap of %d, so the cap is never reached", seeded, capacity)
	}

	ctxA, tenantID, documentID := rdTenant(t, ctx, "active")
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-seeded * time.Second)
	ids := make([]string, 0, seeded)
	for i := range seeded {
		ids = append(ids, rdSeedJob(t, ctx, tenantID, documentID, rdStates[0], base.Add(time.Duration(i)*time.Second), nil))
	}
	// ids is oldest-first; the response is newest-first and holds only the last `capacity`.
	want := make([]string, 0, capacity)
	for i := len(ids) - 1; i >= len(ids)-capacity; i-- {
		want = append(want, ids[i])
	}

	out, err := r.JobsForDocument(ctxA, documentID)
	if err != nil {
		t.Fatalf("JobsForDocument: %v", err)
	}
	if len(out.Jobs) != capacity {
		t.Fatalf("got %d row(s) from %d seeded, want %d", len(out.Jobs), seeded, capacity)
	}
	if got := rdIDs(out.Jobs); !slices.Equal(got, want) {
		t.Errorf("the capped page is %v, want the newest %d: %v", got, capacity, want)
	}
}

// AC 6: last_error is a nullable column read as *string. An in-flight job must read nil, not
// the empty string, and a terminal one must read the stored text unchanged.
func TestRLS_ExtractionReaderCarriesLastErrorOnlyWhenSet(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)
	ctxA, tenantID, documentID := rdTenant(t, ctx, "active")

	const boom = "extraction: docling sidecar returned zero tokens"
	base := time.Now().UTC().Truncate(time.Microsecond)
	inFlight := rdSeedJob(t, ctx, tenantID, documentID, "extracting", base, nil)
	terminal := rdSeedJob(t, ctx, tenantID, documentID, "dead_lettered", base.Add(-time.Second), stPtr(boom))

	out, err := r.JobsForDocument(ctxA, documentID)
	if err != nil {
		t.Fatalf("JobsForDocument: %v", err)
	}
	got := rdJobByID(out.Jobs)
	if len(got) != 2 {
		t.Fatalf("got %d row(s) %v, want 2", len(got), rdIDs(out.Jobs))
	}
	if j := got[inFlight]; j.LastError != nil {
		t.Errorf("the in-flight job reads last_error %q, want nil", *j.LastError)
	}
	if j := got[terminal]; j.LastError == nil || *j.LastError != boom {
		t.Errorf("the terminal job reads last_error %v, want %q", j.LastError, boom)
	}
}

// Q14: reading progress is not an event. No extraction.started, no read-side audit row of any
// kind. Counted as the superuser — an app-pool count is RLS-filtered and would read the same
// whether or not a row was written.
func TestRLS_ExtractionReaderWritesNoAuditRow(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)
	ctxA, tenantID, documentID := rdTenant(t, ctx, "active")
	rdSeedJob(t, ctx, tenantID, documentID, rdStates[0], time.Now().UTC(), nil)

	before := rdAuditCount(t, ctx, tenantID)

	out, err := r.JobsForDocument(ctxA, documentID)
	// A read that failed writes nothing either, and would satisfy the count below for the
	// wrong reason.
	if err != nil {
		t.Fatalf("JobsForDocument: %v", err)
	}
	if len(out.Jobs) != 1 {
		t.Fatalf("got %d row(s) %v, want 1", len(out.Jobs), rdIDs(out.Jobs))
	}

	if after := rdAuditCount(t, ctx, tenantID); after != before {
		t.Errorf("audit_log for tenant %s went from %d row(s) to %d across one read", tenantID, before, after)
	}
}

// AC 1 seam: the mirror image of TestExtractionStore_UsesTenantTxNotRequestTx. The request
// form is what pulls the tenant from the verified Identity and runs the membership gate; the
// bare form takes a tenant id from an argument, which no browser caller may supply.
func TestExtractionReader_UsesRequestTxNotTenantTx(t *testing.T) {
	f, fset := mxParse(t, rdReaderSource)

	var requestTx int
	ast.Inspect(f, func(n ast.Node) bool {
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
			t.Errorf("%s: reader.go names %s; the request seam must not take a tenant id by argument",
				fset.Position(id.Pos()), id.Name)
		}
		return true
	})
	if requestTx == 0 {
		t.Fatal("reader.go names no WithinRequestTenantTx at all, so the scan above passed vacuously")
	}
}

// AC 7: reader.go names none of the five states. Not red against the stub — it is a guard, and
// it fires the moment an executor pins a state in Go or in SQL.
//
// Two floors, because either empty set would make the absence below meaningless: the fixture
// list must be non-empty, and reader.go must hold at least one string literal to examine.
func TestExtractionReader_NamesNoStateLiteral(t *testing.T) {
	if len(rdStates) == 0 {
		t.Fatal("the state fixture list is empty, so the scan below examined nothing")
	}

	f, fset := mxParse(t, rdReaderSource)

	var lits int
	ast.Inspect(f, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		lits++
		unq, err := strconv.Unquote(bl.Value)
		if err != nil {
			t.Fatalf("%s: cannot unquote %s: %v", fset.Position(bl.Pos()), bl.Value, err)
		}
		for _, state := range rdStates {
			if unq == state {
				t.Errorf("%s: reader.go names the state %q; the column value passes through untouched",
					fset.Position(bl.Pos()), state)
			}
			// The SQL-quoted form catches a state pinned inside a larger statement, which a
			// whole-literal comparison alone would miss.
			if strings.Contains(unq, "'"+state+"'") {
				t.Errorf("%s: reader.go pins the state %q inside a SQL literal", fset.Position(bl.Pos()), state)
			}
		}
		return true
	})
	if lits == 0 {
		t.Fatal("reader.go holds no string literals, so this scan examined nothing")
	}
}

// AC 5 and AC 6: the exact key set, in declaration order, with last_error present and null.
// Green against the stub, which declares the tags — it pins them against later drift.
func TestExtractionJobState_MarshalsTheExactKeySet(t *testing.T) {
	b, err := json.Marshal(extraction.JobState{})
	if err != nil {
		t.Fatalf("marshal JobState: %v", err)
	}
	// EXTR-15-01 FK-5: failure_kind carries no omitempty, so a job that never failed
	// serialises an explicit null rather than an absent key. Declared last, beside last_error.
	const wantJob = `{"id":"","document_id":"","state":"","created_at":"0001-01-01T00:00:00Z","last_error":null,"failure_kind":null}`
	if string(b) != wantJob {
		t.Errorf("a zero JobState marshals to\n  %s\nwant\n  %s", b, wantJob)
	}

	// Named separately so a drifted key set says which key moved, not just that the string
	// differs.
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal JobState: %v", err)
	}
	wantKeys := []string{"created_at", "document_id", "failure_kind", "id", "last_error", "state"}
	gotKeys := make([]string, 0, len(decoded))
	for k := range decoded {
		gotKeys = append(gotKeys, k)
	}
	slices.Sort(gotKeys)
	if !slices.Equal(gotKeys, wantKeys) {
		t.Errorf("JobState carries keys %v, want exactly %v", gotKeys, wantKeys)
	}

	// The envelope: the nil slice is the bug the never-nil rule exists to stop, and the value
	// the reader returns on every path is the one below it.
	b, err = json.Marshal(extraction.JobsResponse{})
	if err != nil {
		t.Fatalf("marshal a zero JobsResponse: %v", err)
	}
	if string(b) != `{"jobs":null}` {
		t.Errorf("a zero JobsResponse marshals to %s, want {\"jobs\":null} — the shape the reader must never return", b)
	}
	b, err = json.Marshal(extraction.JobsResponse{Jobs: []extraction.JobState{}})
	if err != nil {
		t.Fatalf("marshal an empty JobsResponse: %v", err)
	}
	if string(b) != `{"jobs":[]}` {
		t.Errorf("an empty-slice JobsResponse marshals to %s, want {\"jobs\":[]}", b)
	}
}

// rdTraceKey carries the SQL from TraceQueryStart to TraceQueryEnd, which is handed no SQL of
// its own.
type rdTraceKey struct{}

// rdQueryTracer records every query on its pool and can run a hook as one ends. The request
// seam's own set_config and membership select ride a pgx.Batch, which a plain QueryTracer never
// sees (docs/migrations.md), so only the reader's own SELECT reaches this.
type rdQueryTracer struct {
	mu    sync.Mutex
	sqls  []string
	onEnd func(conn *pgx.Conn, sql string)
}

func (tr *rdQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	tr.mu.Lock()
	tr.sqls = append(tr.sqls, d.SQL)
	tr.mu.Unlock()
	return context.WithValue(ctx, rdTraceKey{}, d.SQL)
}

func (tr *rdQueryTracer) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, _ pgx.TraceQueryEndData) {
	sql, _ := ctx.Value(rdTraceKey{}).(string)
	if tr.onEnd != nil {
		tr.onEnd(conn, sql)
	}
}

func (tr *rdQueryTracer) matching(substr string) (n int, seen []string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for _, sql := range tr.sqls {
		if strings.Contains(sql, substr) {
			n++
		}
	}
	return n, slices.Clone(tr.sqls)
}

// rdTracedPool is a second invoice_app pool carrying tr. The harness pool is shared by every
// case in this package, so a tracer installed on it would fire under all of them.
func rdTracedPool(t *testing.T, tr *rdQueryTracer) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(stRequire(t).app.Config().ConnString())
	if err != nil {
		t.Fatalf("parse the app DSN: %v", err)
	}
	cfg.ConnConfig.Tracer = tr
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open a traced app pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// AC 1, the "exactly one query" half: counted on the wire, not read off the source. A reader
// that fetched jobs and then looped for anything per row would still satisfy every other case.
func TestRLS_ExtractionReaderIssuesOneQueryPerRead(t *testing.T) {
	ctx := t.Context()
	tr := &rdQueryTracer{}
	r := &extraction.Reader{Pool: rdTracedPool(t, tr)}

	ctxA, tenantID, documentID := rdTenant(t, ctx, "active")
	base := time.Now().UTC().Truncate(time.Microsecond)
	const seeded = 3
	for i := range seeded {
		rdSeedJob(t, ctx, tenantID, documentID, rdStates[i%len(rdStates)], base.Add(time.Duration(i)*time.Second), nil)
	}

	out, err := r.JobsForDocument(ctxA, documentID)
	if err != nil {
		t.Fatalf("JobsForDocument: %v", err)
	}
	// The rows are what make the count below mean something: a read that returned nothing
	// would also issue one query.
	if len(out.Jobs) != seeded {
		t.Fatalf("got %d row(s) %v, want %d", len(out.Jobs), rdIDs(out.Jobs), seeded)
	}
	if n, seen := tr.matching("extraction_jobs"); n != 1 {
		t.Errorf("one read of %d row(s) issued %d queries naming extraction_jobs, want 1; the pool saw %v",
			len(out.Jobs), n, seen)
	}
}

// AC 4, the third error path: the transaction fails at COMMIT, after the scan already filled a
// result set. WithinRequestTenantTx runs fn and only then commits, so assigning inside the
// closure is not enough — the rows must be discarded on the way out.
func TestRLS_ExtractionReaderDiscardsRowsWhenTheCommitFails(t *testing.T) {
	ctx := t.Context()
	ctxA, tenantID, documentID := rdTenant(t, ctx, "active")
	rdSeedJob(t, ctx, tenantID, documentID, rdStates[0], time.Now().UTC(), nil)

	// Positive control on the untraced pool: the row is readable, so the empty answer below
	// cannot be an empty-table artefact.
	if out, err := rdReader(t).JobsForDocument(ctxA, documentID); err != nil || len(out.Jobs) != 1 {
		t.Fatalf("control read returned %d row(s) and %v, want 1 and no error", len(out.Jobs), err)
	}

	// TraceQueryEnd fires from rows.Close, after the result reader is drained: the scan has
	// succeeded and only COMMIT is left. The two-argument form waits for the backend to be
	// gone, so the commit cannot win a race against the signal.
	var killed int
	tr := &rdQueryTracer{onEnd: func(conn *pgx.Conn, sql string) {
		if !strings.Contains(sql, "extraction_jobs") {
			return
		}
		killed++
		pid := conn.PgConn().PID()
		var gone bool
		if err := stRequire(t).super.QueryRow(ctx,
			`SELECT pg_terminate_backend($1, 5000)`, pid).Scan(&gone); err != nil {
			t.Errorf("terminate backend %d: %v", pid, err)
			return
		}
		if !gone {
			t.Errorf("backend %d survived pg_terminate_backend, so the commit below may succeed", pid)
		}
	}}

	r := &extraction.Reader{Pool: rdTracedPool(t, tr)}
	out, err := r.JobsForDocument(ctxA, documentID)

	if killed != 1 {
		t.Fatalf("the tracer fired on %d extraction_jobs quer(ies), want 1; the commit-failure path was not reached", killed)
	}
	if err == nil {
		t.Fatal("the read reported no error although its transaction could not commit")
	}
	if out.Jobs == nil {
		t.Error("the commit-failure path returned a nil Jobs slice, want []JobState{}")
	}
	if len(out.Jobs) != 0 {
		t.Errorf("the commit-failure path returned %d row(s) %v beside its error %v; rows read in a transaction that never committed must not reach the caller",
			len(out.Jobs), rdIDs(out.Jobs), err)
	}
}

// AC 8 boundary: exactly the cap, and one over it, on two documents of ONE tenant. The second
// document also proves the cap and the WHERE are per document rather than per tenant.
func TestRLS_ExtractionReaderCapsAtTheBoundary(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)
	capacity := rdDeclaredCap(t)
	if capacity < 2 {
		t.Fatalf("%s = %d, too small for a boundary case", rdCapName, capacity)
	}

	ctxA, tenantID, atCap := rdTenant(t, ctx, "active")
	overCap := rdSeedDocument(t, ctx, tenantID)

	base := time.Now().UTC().Truncate(time.Microsecond)
	atCapIDs := make([]string, 0, capacity)
	for i := range capacity {
		atCapIDs = append(atCapIDs, rdSeedJob(t, ctx, tenantID, atCap, rdStates[0], base.Add(time.Duration(i)*time.Second), nil))
	}
	overCapIDs := make([]string, 0, capacity+1)
	for i := range capacity + 1 {
		overCapIDs = append(overCapIDs, rdSeedJob(t, ctx, tenantID, overCap, rdStates[0], base.Add(time.Duration(i)*time.Second), nil))
	}

	out, err := r.JobsForDocument(ctxA, atCap)
	if err != nil {
		t.Fatalf("read the document holding exactly %d job(s): %v", capacity, err)
	}
	if len(out.Jobs) != capacity {
		t.Errorf("a document holding exactly %d job(s) read back %d", capacity, len(out.Jobs))
	}
	if got, want := rdIDs(out.Jobs), slices.Clone(atCapIDs); !slices.Equal(got, rdReversed(want)) {
		t.Errorf("the exactly-at-cap page is %v, want every row newest first: %v", got, want)
	}

	out, err = r.JobsForDocument(ctxA, overCap)
	if err != nil {
		t.Fatalf("read the document holding %d job(s): %v", capacity+1, err)
	}
	if len(out.Jobs) != capacity {
		t.Fatalf("a document holding %d job(s) read back %d, want %d", capacity+1, len(out.Jobs), capacity)
	}
	// The oldest is the one dropped, and no row of the sibling document leaked in.
	if got := rdJobByID(out.Jobs); len(got) > 0 {
		if _, present := got[overCapIDs[0]]; present {
			t.Errorf("the oldest job %s survived the cap; the dropped row must be the oldest", overCapIDs[0])
		}
		for _, id := range atCapIDs {
			if _, leaked := got[id]; leaked {
				t.Errorf("job %s belongs to document %s but came back under %s", id, atCap, overCap)
			}
		}
	} else {
		t.Fatal("the over-cap read returned no rows to inspect")
	}
}

// rdReversed copies ids newest-first out of an oldest-first fixture.
func rdReversed(ids []string) []string {
	out := make([]string, 0, len(ids))
	for i := len(ids) - 1; i >= 0; i-- {
		out = append(out, ids[i])
	}
	return out
}

// AC 6: *string distinguishes a stored empty string from SQL NULL, and the wire must keep them
// apart too — a reader that coerced "" to nil would report an in-flight job as failed-with-no-
// message, or the reverse.
func TestRLS_ExtractionReaderDistinguishesAnEmptyLastErrorFromNull(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)
	ctxA, tenantID, documentID := rdTenant(t, ctx, "active")

	base := time.Now().UTC().Truncate(time.Microsecond)
	empty := rdSeedJob(t, ctx, tenantID, documentID, rdStates[0], base, stPtr(""))
	null := rdSeedJob(t, ctx, tenantID, documentID, rdStates[0], base.Add(-time.Second), nil)

	out, err := r.JobsForDocument(ctxA, documentID)
	if err != nil {
		t.Fatalf("JobsForDocument: %v", err)
	}
	got := rdJobByID(out.Jobs)
	if len(got) != 2 {
		t.Fatalf("got %d row(s) %v, want 2", len(got), rdIDs(out.Jobs))
	}

	switch j := got[empty]; {
	case j.LastError == nil:
		t.Error("a job whose last_error is the empty string read back nil, which is the shape of NULL")
	case *j.LastError != "":
		t.Errorf("a job whose last_error is the empty string read back %q", *j.LastError)
	}
	if j := got[null]; j.LastError != nil {
		t.Errorf("a job whose last_error is NULL read back %q", *j.LastError)
	}

	for id, want := range map[string]string{empty: `"last_error":""`, null: `"last_error":null`} {
		b, err := json.Marshal(got[id])
		if err != nil {
			t.Fatalf("marshal job %s: %v", id, err)
		}
		if !strings.Contains(string(b), want) {
			t.Errorf("job %s marshals to %s, want it to carry %s", id, b, want)
		}
	}
}

// AC 2, fail-closed: a TenantID that parses but names no tenant is refused by the membership
// gate, not answered with somebody else's rows.
func TestRLS_ExtractionReaderRefusesAnIdentityForAnUnknownTenant(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)

	// A real tenant with a real job, read under an Identity naming a tenant that does not
	// exist: the empty answer must be the refusal, not an empty table.
	_, tenantID, documentID := rdTenant(t, ctx, "active")
	seeded := rdSeedJob(t, ctx, tenantID, documentID, rdStates[0], time.Now().UTC(), nil)

	ctxUnknown := auth.WithIdentity(ctx, auth.Identity{
		Subject:  rdMemberSubject,
		Role:     "authenticated",
		TenantID: uuid.NewString(),
	})

	out, err := r.JobsForDocument(ctxUnknown, documentID)
	if !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("a read for an unknown tenant returned %v, want %v", err, db.ErrNotActiveMember)
	}
	if out.Jobs == nil {
		t.Error("the error path returned a nil Jobs slice, want []JobState{}")
	}
	if len(out.Jobs) != 0 {
		t.Errorf("a read for an unknown tenant returned %d row(s) %v (the tenant's own job is %s)",
			len(out.Jobs), rdIDs(out.Jobs), seeded)
	}
}

// AC 4, the fourth error path: the read itself fails. document_id is uuid, so a malformed id is
// rejected by Postgres; pgx surfaces that at rows.Err rather than from tx.Query.
func TestRLS_ExtractionReaderReturnsAnEmptyListWhenTheQueryFails(t *testing.T) {
	ctx := t.Context()
	r := rdReader(t)
	ctxA, tenantID, documentID := rdTenant(t, ctx, "active")
	seeded := rdSeedJob(t, ctx, tenantID, documentID, rdStates[0], time.Now().UTC(), nil)

	out, err := r.JobsForDocument(ctxA, "not-a-uuid")
	if err == nil {
		t.Fatalf("a malformed document id was accepted and answered with %v", rdIDs(out.Jobs))
	}
	// The wrap has to name the read. Swallowed, the same call still errors — the aborted
	// transaction cannot commit — and every other assertion here would hold.
	if want := "extraction: read jobs for document"; !strings.Contains(err.Error(), want) {
		t.Errorf("the query error surfaced as %q, want it wrapped with %q", err, want)
	}
	if out.Jobs == nil {
		t.Error("the query-error path returned a nil Jobs slice, want []JobState{}")
	}
	if len(out.Jobs) != 0 {
		t.Errorf("the query-error path returned %d row(s) %v (the tenant's own job is %s)",
			len(out.Jobs), rdIDs(out.Jobs), seeded)
	}
}

// AC 1, the "not tenant_id" half: a hand-written tenant predicate would make every cross-tenant
// case here pass whether or not the policy is doing the work. Nothing else in the suite fails
// when one is added.
func TestExtractionReader_QueryNamesDocumentIDNotTenantID(t *testing.T) {
	f, fset := mxParse(t, rdReaderSource)

	const table = "extraction_jobs"
	var sqlLits int
	ast.Inspect(f, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		unq, err := strconv.Unquote(bl.Value)
		if err != nil || !strings.Contains(unq, table) {
			return true
		}
		sqlLits++
		if !strings.Contains(unq, "document_id") {
			t.Errorf("%s: the %s query names no document_id", fset.Position(bl.Pos()), table)
		}
		if strings.Contains(unq, "tenant_id") {
			t.Errorf("%s: the %s query names tenant_id; the tenant_isolation policy supplies that predicate",
				fset.Position(bl.Pos()), table)
		}
		return true
	})
	if sqlLits == 0 {
		t.Fatalf("reader.go holds no string literal naming %s, so this scan examined nothing", table)
	}
}
