// reader_detail_audit_db_test.go: AC 8 -- one screen open leaves exactly one document.read
// audit row naming the job's document, on the reader's own transaction, and a refused read
// leaves none. Two cases pin what "on the reader's own transaction" buys: the audit INSERT
// lands between that read's own begin and commit, and a recorder that refuses fails the read.
//
// The recorder is the test's own INSERT, not internal/audit.Record: deps_test.go fences this
// package off everything outside internal/platform/*, and an import of internal/audit here
// would fail that scan. It writes the same three columns Record writes, through the tx Detail
// hands it -- so a recorder called outside the transaction, or handed a nil one, writes nothing
// and the counts below stay at zero.
//
// Helpers use an rda* prefix.
package extraction_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// rdaReadEvent is the event name cmd/submission spells for production. It is spelled here too
// because the assertion needs a value to compare against; the rule that keeps it out of
// internal/extraction covers non-test source only (TestNewDocumentReadAuditor_SpellsTheEventInCmd).
const rdaReadEvent = "document.read"

// rdaCall is one recorder invocation, kept whole: the tx it was handed is as load-bearing as the
// document id, because a write on any other connection does not share the read's fate.
type rdaCall struct {
	subject    string
	documentID string
	tx         pgx.Tx
}

type rdaRecorder struct{ calls []rdaCall }

func (r *rdaRecorder) record(ctx context.Context, tx pgx.Tx, subject, documentID string) error {
	r.calls = append(r.calls, rdaCall{subject: subject, documentID: documentID, tx: tx})
	if tx == nil {
		return errors.New("audit: the recorder was handed no transaction, so the row cannot share the read's fate")
	}
	payload, err := json.Marshal(map[string]string{"id": documentID})
	if err != nil {
		return err
	}
	// The same INSERT internal/audit.Record issues: tenant_id comes off the tx's
	// app.current_tenant GUC by column default, and the RLS WITH CHECK refuses any other tenant.
	_, err = tx.Exec(ctx,
		`INSERT INTO audit_log (actor, event, payload) VALUES ($1, $2, $3)`,
		subject, rdaReadEvent, string(payload))
	return err
}

// rdaReader is rdReader plus the injected recorder.
func rdaReader(t *testing.T, rec *rdaRecorder) *extraction.Reader {
	t.Helper()
	return &extraction.Reader{Pool: stRequire(t).app, Audit: rec.record}
}

// rdaReadRows counts, as the SUPERUSER, the document.read rows one tenant holds naming one
// document. An app-pool count is RLS-filtered and would read the same whether or not a row was
// written.
func rdaReadRows(t *testing.T, ctx context.Context, tenantID, documentID string) int {
	t.Helper()
	var n int
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT count(*) FROM audit_log
		  WHERE tenant_id = $1 AND event = $2 AND payload->>'id' = $3`,
		tenantID, rdaReadEvent, documentID).Scan(&n); err != nil {
		t.Fatalf("count %s rows for tenant %s document %s: %v", rdaReadEvent, tenantID, documentID, err)
	}
	return n
}

// rdaPurge drops the audit rows these cases write. audit_log carries no foreign key to tenants,
// so stTenant's teardown leaves them standing under dead ids -- and that debris is not inert:
// internal/platform/db's TestRLS_AuditReadTenantQualIsAnIndexCondOnEveryNewIndex asserts a
// planner index CHOICE, which moves with audit_log's global row count. worker_db_test.go's
// wkPurgeAuditLog is the same teardown for the same reason.
func rdaPurge(t *testing.T, tenantIDs ...string) {
	t.Helper()
	if len(tenantIDs) == 0 {
		t.Fatal("rdaPurge was handed no tenant, so it drops nothing and the rows it exists to remove stay")
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, id := range tenantIDs {
			wkPurgeAuditLog(t, ctx, id)
		}
	})
}

// AC 8, positive half. Exactly one row, not one-or-more: a recorder called once per statement
// would leave three and read as success to a > 0 assertion.
func TestRLS_ExtractionDetailWritesOneDocumentReadAuditRow(t *testing.T) {
	ctx := t.Context()
	rec := &rdaRecorder{}
	r := rdaReader(t, rec)

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	rdaPurge(t, tenantA)
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	rvdSeedPage(t, ctx, tenantA, docA, 1, 1275, 1651)
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("A-0001"), nil, 0, now)

	// The before-counts are what make "one" a delta rather than a coincidence.
	if before := rdaReadRows(t, ctx, tenantA, docA); before != 0 {
		t.Fatalf("tenant %s already holds %d %s row(s) for document %s before any read", tenantA, before, rdaReadEvent, docA)
	}
	beforeAll := rdAuditCount(t, ctx, tenantA)

	got, err := r.Detail(ctxA, jobA)
	// A read that failed writes nothing either, and would satisfy a zero-delta assertion for
	// the wrong reason.
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	if got.ID != jobA || got.DocumentID != docA {
		t.Fatalf("Detail returned id %q / document %q, want %q / %q", got.ID, got.DocumentID, jobA, docA)
	}

	if len(rec.calls) != 1 {
		t.Fatalf("the recorder ran %d time(s) across one detail read, want exactly 1 -- F-2's answer is one row per screen open", len(rec.calls))
	}
	call := rec.calls[0]
	if call.documentID != docA {
		t.Errorf("the recorder was handed document %q, want the job's own %q", call.documentID, docA)
	}
	if call.subject != rdMemberSubject {
		t.Errorf("the recorder was handed subject %q, want the verified caller %q", call.subject, rdMemberSubject)
	}
	if call.tx == nil {
		t.Error("the recorder was handed a nil transaction, so the audit row cannot share the read's fate")
	}

	if n := rdaReadRows(t, ctx, tenantA, docA); n != 1 {
		t.Errorf("tenant %s holds %d %s row(s) naming document %s after one read, want exactly 1", tenantA, n, rdaReadEvent, docA)
	}
	if after := rdAuditCount(t, ctx, tenantA); after != beforeAll+1 {
		t.Errorf("audit_log for tenant %s went from %d row(s) to %d across one read, want exactly one more", tenantA, beforeAll, after)
	}
}

// AC 8, negative half, with its own positive control: a Detail that audited nothing at all would
// pass the two refusals alone. Both refusal shapes are covered -- absent and another tenant's --
// because they are one answer on the wire and could be two paths in the code.
func TestRLS_ExtractionDetailRefusalWritesNoAuditRow(t *testing.T) {
	ctx := t.Context()

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	_, tenantB, docB := rdTenant(t, ctx, "active")
	rdaPurge(t, tenantA, tenantB)
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	jobB := rdSeedJob(t, ctx, tenantB, docB, "succeeded", now, nil)

	// Control: the recorder IS wired and CAN write on this reader.
	control := &rdaRecorder{}
	if _, err := rdaReader(t, control).Detail(ctxA, jobA); err != nil {
		t.Fatalf("control: A reading its own job %s: %v", jobA, err)
	}
	if len(control.calls) != 1 {
		t.Fatalf("control: the recorder ran %d time(s) on a successful read, want 1 -- the refusals below would prove nothing", len(control.calls))
	}
	if n := rdaReadRows(t, ctx, tenantA, docA); n != 1 {
		t.Fatalf("control: tenant %s holds %d %s row(s) for document %s, want 1", tenantA, n, rdaReadEvent, docA)
	}

	beforeA := rdAuditCount(t, ctx, tenantA)
	beforeB := rdAuditCount(t, ctx, tenantB)

	cases := []struct {
		name  string
		jobID string
	}{
		{"another tenant's job", jobB},
		{"a job that does not exist", uuid.NewString()},
	}
	if len(cases) == 0 {
		t.Fatal("the case table is empty, so this test examined nothing")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &rdaRecorder{}
			_, err := rdaReader(t, rec).Detail(ctxA, tc.jobID)
			if !errors.Is(err, extraction.ErrNotFound) {
				t.Fatalf("A reading %s returned %v, want %v", tc.jobID, err, extraction.ErrNotFound)
			}
			if len(rec.calls) != 0 {
				t.Errorf("the recorder ran %d time(s) on a refused read, want 0: %+v", len(rec.calls), rec.calls)
			}
			if n := rdaReadRows(t, ctx, tenantB, docB); n != 0 {
				t.Errorf("tenant %s holds %d %s row(s) naming document %s after a refused read, want 0", tenantB, n, rdaReadEvent, docB)
			}
		})
	}

	if afterA := rdAuditCount(t, ctx, tenantA); afterA != beforeA {
		t.Errorf("audit_log for tenant %s went from %d row(s) to %d across two refused reads", tenantA, beforeA, afterA)
	}
	if afterB := rdAuditCount(t, ctx, tenantB); afterB != beforeB {
		t.Errorf("audit_log for tenant %s went from %d row(s) to %d across two refused reads", tenantB, beforeB, afterB)
	}
}

// rdaFailingRecorder refuses and writes nothing.
type rdaFailingRecorder struct {
	calls int
	err   error
}

func (r *rdaFailingRecorder) record(context.Context, pgx.Tx, string, string) error {
	r.calls++
	return r.err
}

// AC 8's fate half. The row rides the read's own transaction, so a recorder that fails fails the
// READ: a swallowed audit error would serve the document with no trail behind it, and every count
// above would still read clean because their recorder succeeds.
func TestRLS_ExtractionDetailAuditFailureFailsTheRead(t *testing.T) {
	ctx := t.Context()

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	rdaPurge(t, tenantA)
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	rvdSeedPage(t, ctx, tenantA, docA, 1, 1275, 1651)
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("A-0001"), nil, 0, now)

	// Control: this fixture reads and audits when the recorder succeeds, so the refusal below is
	// the recorder's doing rather than an empty fixture or a job A cannot see.
	if got, err := rdaReader(t, &rdaRecorder{}).Detail(ctxA, jobA); err != nil || got.ID != jobA {
		t.Fatalf("control: Detail returned id %q and %v, want %q and no error", got.ID, err, jobA)
	}
	if n := rdaReadRows(t, ctx, tenantA, docA); n != 1 {
		t.Fatalf("control: tenant %s holds %d %s row(s) for document %s, want 1", tenantA, n, rdaReadEvent, docA)
	}
	before := rdAuditCount(t, ctx, tenantA)

	refused := errors.New("audit: the trail is unavailable")
	rec := &rdaFailingRecorder{err: refused}
	got, err := (&extraction.Reader{Pool: stRequire(t).app, Audit: rec.record}).Detail(ctxA, jobA)

	if rec.calls != 1 {
		t.Fatalf("the recorder ran %d time(s), want 1 -- a read that never audited proves nothing about what a failed audit does", rec.calls)
	}
	if !errors.Is(err, refused) {
		t.Fatalf("Detail returned %v, want the recorder's own error -- an audit whose failure the caller never learns of is a trail that can silently stop", err)
	}
	if got.ID != "" || got.DocumentID != "" || got.State != "" {
		t.Errorf("the failed read carried id %q / document %q / state %q; a transaction that rolled back must not reach the caller", got.ID, got.DocumentID, got.State)
	}
	if got.Pages == nil || got.Fields == nil {
		t.Errorf("the failed read returned Pages=%v Fields=%v; a nil slice marshals to JSON null", got.Pages, got.Fields)
	}
	if after := rdAuditCount(t, ctx, tenantA); after != before {
		t.Errorf("audit_log for tenant %s went from %d row(s) to %d across a read whose audit failed, want no change", tenantA, before, after)
	}
}

// AC 8's "on the reader's own transaction", read off the wire rather than off the recorder's
// argument: a recorder handed a real but DIFFERENT transaction satisfies every tx assertion
// above. One begin, three SELECTs, the audit INSERT, one commit, in that order.
func TestRLS_ExtractionDetailAuditsInsideTheReadsOwnTransaction(t *testing.T) {
	ctx := t.Context()
	tr := &rdQueryTracer{}
	rec := &rdaRecorder{}
	r := &extraction.Reader{Pool: rdTracedPool(t, tr), Audit: rec.record}

	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	rdaPurge(t, tenantA)
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	rvdSeedPage(t, ctx, tenantA, docA, 1, 1275, 1651)
	rvdSeedField(t, ctx, tenantA, jobA, "invoice_number", rvdStr("A-0001"), nil, 0, now)

	got, err := r.Detail(ctxA, jobA)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", jobA, err)
	}
	// The rows make the count mean something: a read over an empty document issues the same
	// statements whether or not it looked at anything.
	if len(got.Pages) != 1 || len(got.Fields) != 1 {
		t.Fatalf("got %d page(s) and %d field(s), want 1 and 1", len(got.Pages), len(got.Fields))
	}
	if len(rec.calls) != 1 {
		t.Fatalf("the recorder ran %d time(s), want 1", len(rec.calls))
	}

	_, seen := tr.matching(rpTable)
	if len(seen) != 6 {
		t.Fatalf("one audited detail read issued %d traced statement(s), want 6 (begin, three SELECTs, the audit INSERT, commit); the pool saw %v", len(seen), seen)
	}
	if seen[0] != "begin" || seen[5] != "commit" {
		t.Errorf("the traced statements were %v, want one begin/commit pair around four statements", seen)
	}
	if !strings.Contains(seen[4], "audit_log") {
		t.Errorf("statement 5 was %q, want the audit INSERT -- a row written on any other connection does not share the read's fate", seen[4])
	}
}
