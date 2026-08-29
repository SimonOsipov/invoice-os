// reader_db_test.go: the request-seam reader's suite (EXTR-07-01). It shares store_db_test.go's
// harness — stRequire is this package's one sanctioned skip site — and seeds every fixture as
// the superuser, because invoice_app holds no DELETE on extraction_jobs and no INSERT on
// tenants.
//
// Written before the implementation: reader.go ships as a stub whose JobsForDocument returns a
// not-implemented error, so every behavioural case below fails on its own assertion rather
// than on a compile error.
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
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	rdReaderSource = "reader.go"
	rdCapName      = "maxJobsPerDocument"

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
	id := uuid.NewString()
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
	tied := []string{
		rdSeedJob(t, ctx, tenantID, documentID, rdStates[0], base.Add(-time.Second), nil),
		rdSeedJob(t, ctx, tenantID, documentID, rdStates[0], base.Add(-time.Second), nil),
	}
	// uuid compares bytewise in Postgres, and the canonical lowercase-hex form sorts the same
	// way, so the larger string is the larger uuid.
	slices.Sort(tied)
	want := []string{newest, tied[1], tied[0]}

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
	const wantJob = `{"id":"","document_id":"","state":"","created_at":"0001-01-01T00:00:00Z","last_error":null}`
	if string(b) != wantJob {
		t.Errorf("a zero JobState marshals to\n  %s\nwant\n  %s", b, wantJob)
	}

	// Named separately so a drifted key set says which key moved, not just that the string
	// differs.
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal JobState: %v", err)
	}
	wantKeys := []string{"created_at", "document_id", "id", "last_error", "state"}
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
