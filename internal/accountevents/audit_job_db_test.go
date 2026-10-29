package accountevents

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func (e *env) work(a AuditArgs) error {
	w := &AuditWorker{Pool: e.app}
	return w.Work(context.Background(), &river.Job[AuditArgs]{Args: a})
}

type auditRow struct {
	Actor, Event string
	Payload      map[string]any
}

// rowsUnder reads the actor's audit rows under the tenant's own RLS scope, as the app does.
func (e *env) rowsUnder(t *testing.T, tenant, actor string) []auditRow {
	t.Helper()
	var out []auditRow
	err := db.WithinTenantTx(context.Background(), e.app, tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `SELECT actor, event, payload FROM audit_log WHERE actor = $1 ORDER BY created_at, id`, actor)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r auditRow
			var raw []byte
			if err := rows.Scan(&r.Actor, &r.Event, &raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &r.Payload); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("read audit_log under tenant %s: %v", tenant, err)
	}
	return out
}

func (e *env) auditEverywhere(t *testing.T, actor string) int {
	t.Helper()
	return e.mustCount(t, `SELECT count(*) FROM audit_log WHERE actor = $1`, actor)
}

// AC 5: one row in the job's tenant; the other tenant of the same member sees none.
func TestAuditJob_WritesOneRowInItsTenantOnly(t *testing.T) {
	e := newEnv(t)
	a, b, u := e.tenant(t), e.tenant(t), e.user()
	e.member(t, a, u, "active", e.email("aj"))
	e.member(t, b, u, "active", e.email("aj"))
	at := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)

	err := e.work(AuditArgs{TenantID: a, UserID: u, Event: evSignedIn, IP: "203.0.113.7", Device: "Chrome on macOS", OccurredAt: at})
	if err != nil {
		t.Fatalf("Work: %v", err)
	}

	inA := e.rowsUnder(t, a, u)
	if len(inA) != 1 {
		t.Fatalf("tenant A holds %d row(s) for the member, want 1", len(inA))
	}
	r := inA[0]
	if r.Actor != u || r.Event != evSignedIn {
		t.Errorf("row = actor %q event %q, want actor %q event %q", r.Actor, r.Event, u, evSignedIn)
	}
	keys := make([]string, 0, len(r.Payload))
	for k := range r.Payload {
		keys = append(keys, k)
	}
	if slices.Sort(keys); !slices.Equal(keys, []string{"device", "ip_address", "occurred_at"}) {
		t.Errorf("payload keys = %v, want exactly [device ip_address occurred_at]", keys)
	}
	if r.Payload["ip_address"] != "203.0.113.7" || r.Payload["device"] != "Chrome on macOS" || r.Payload["occurred_at"] != at {
		t.Errorf("payload = %v, want ip 203.0.113.7, device Chrome on macOS, occurred_at %s", r.Payload, at)
	}
	if got := e.rowsUnder(t, b, u); len(got) != 0 {
		t.Errorf("tenant B reads %d row(s) written for tenant A, want 0", len(got))
	}
	if n := e.auditEverywhere(t, u); n != 1 {
		t.Errorf("%d row(s) for the member across all tenants, want exactly 1", n)
	}
}

// AC 5: a job for a tenant that does not exist fails and writes nothing. audit_log has no FK to
// tenants and its RLS check passes for any bound tenant, so the worker must refuse the unknown tenant itself.
func TestAuditJob_RLSRefusesAForeignTenantWrite(t *testing.T) {
	e := newEnv(t)
	a, u := e.tenant(t), e.user()
	e.member(t, a, u, "active", e.email("ghost"))
	now := time.Now().UTC().Format(time.RFC3339)

	if err := e.work(AuditArgs{TenantID: a, UserID: u, Event: evSignedIn, IP: "203.0.113.7", Device: "Chrome on macOS", OccurredAt: now}); err != nil {
		t.Fatalf("control job for a real tenant: %v", err)
	}
	if n := e.auditEverywhere(t, u); n != 1 {
		t.Fatalf("control: %d row(s), want 1", n)
	}

	ghost := "6f1c2a3e-8b4d-4c5e-9a7f-0123456789ab"
	err := e.work(AuditArgs{TenantID: ghost, UserID: u, Event: evSignedIn, IP: "203.0.113.7", Device: "Chrome on macOS", OccurredAt: now})
	if err == nil {
		t.Error("job for a tenant that does not exist returned nil, want an error")
	}
	if n := e.auditEverywhere(t, u); n != 1 {
		t.Errorf("%d row(s) for the member across all tenants after the ghost job, want still 1", n)
	}
	if n := e.mustCount(t, `SELECT count(*) FROM audit_log WHERE tenant_id = $1`, ghost); n != 0 {
		t.Errorf("%d row(s) written for the unknown tenant, want 0", n)
	}
}

// AC 6: empty detail is written as the words of [unknown-detail], not as blanks.
func TestAuditJob_UnknownDetailIsWrittenLiterally(t *testing.T) {
	e := newEnv(t)
	a, u := e.tenant(t), e.user()
	e.member(t, a, u, "active", e.email("unk"))

	err := e.work(AuditArgs{TenantID: a, UserID: u, Event: "account.sign_in_failed", IP: "", Device: "", OccurredAt: time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("Work: %v", err)
	}
	rows := e.rowsUnder(t, a, u)
	if len(rows) != 1 {
		t.Fatalf("%d row(s), want 1", len(rows))
	}
	if rows[0].Payload["ip_address"] != "Unknown IP" || rows[0].Payload["device"] != "Unknown device" {
		t.Errorf("payload = %v, want ip_address %q and device %q", rows[0].Payload, "Unknown IP", "Unknown device")
	}
}

// AC 7: three refusals in one second write three rows; nothing is deduplicated.
func TestAuditJob_EveryBlockedRefusalWritesARow(t *testing.T) {
	e := newEnv(t)
	a, u := e.tenant(t), e.user()
	e.member(t, a, u, "active", e.email("blk"))
	args := AuditArgs{TenantID: a, UserID: u, Event: "account.sign_in_blocked", IP: "203.0.113.7", Device: "Chrome on macOS", OccurredAt: time.Now().UTC().Format(time.RFC3339)}

	for i := 0; i < 3; i++ {
		if err := e.work(args); err != nil {
			t.Fatalf("Work #%d: %v", i+1, err)
		}
	}
	rows := e.rowsUnder(t, a, u)
	if len(rows) != 3 {
		t.Fatalf("%d row(s) after 3 blocked jobs, want 3", len(rows))
	}
	for _, r := range rows {
		if r.Event != "account.sign_in_blocked" {
			t.Errorf("row event = %q, want account.sign_in_blocked", r.Event)
		}
	}
}

// AC 5: an audit.Record failure comes back as the job's error, so River logs it and retries; no row is written.
func TestAuditJob_RecordFailureIsReturnedAndWritesNothing(t *testing.T) {
	e := newEnv(t)
	a, u := e.tenant(t), e.user()
	e.member(t, a, u, "active", e.email("rf"))
	now := time.Now().UTC().Format(time.RFC3339)

	if err := e.work(AuditArgs{TenantID: a, UserID: u, Event: evSignedIn, IP: "203.0.113.7", Device: "Chrome on macOS", OccurredAt: now}); err != nil {
		t.Fatalf("control job: %v", err)
	}
	for name, args := range map[string]AuditArgs{
		"empty event (audit_event_length)": {TenantID: a, UserID: u, Event: "", OccurredAt: now},
		"empty actor (audit_actor_length)": {TenantID: a, UserID: "", Event: evSignedIn, OccurredAt: now},
	} {
		err := e.work(args)
		var cancel *rivertype.JobCancelError
		if err == nil || errors.As(err, &cancel) {
			t.Errorf("%s: Work = %v, want the audit.Record error, retried by River (not cancelled)", name, err)
		}
	}
	if got := e.rowsUnder(t, a, u); len(got) != 1 {
		t.Errorf("%d row(s) for the member after two refused writes, want only the control's 1", len(got))
	}
	if n := e.mustCount(t, `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND actor = ''`, a); n != 0 {
		t.Errorf("%d row(s) with an empty actor, want 0", n)
	}
}

// AC 5: a tenant id that is not a UUID is refused before any statement; nothing is written anywhere.
func TestAuditJob_MalformedTenantWritesNothing(t *testing.T) {
	e := newEnv(t)
	a, u := e.tenant(t), e.user()
	e.member(t, a, u, "active", e.email("bad"))
	now := time.Now().UTC().Format(time.RFC3339)

	if err := e.work(AuditArgs{TenantID: a, UserID: u, Event: evSignedIn, OccurredAt: now}); err != nil {
		t.Fatalf("control job: %v", err)
	}
	for _, tid := range []string{"", "not-a-uuid", a + "x"} {
		if err := e.work(AuditArgs{TenantID: tid, UserID: u, Event: evSignedIn, OccurredAt: now}); err == nil {
			t.Errorf("tenant_id %q: Work returned nil, want an error", tid)
		}
	}
	if n := e.auditEverywhere(t, u); n != 1 {
		t.Errorf("%d row(s) for the member across all tenants, want only the control's 1", n)
	}
}

// AC 5: two members in two tenants; each job lands in its own tenant and neither tenant reads the other's row.
func TestAuditJob_TwoTenantsEachSeeOnlyTheirOwnRow(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(t), e.tenant(t)
	ua, ub := e.user(), e.user()
	e.member(t, a, ua, "active", e.email("ta"))
	e.member(t, b, ub, "active", e.email("tb"))
	now := time.Now().UTC().Format(time.RFC3339)

	for _, args := range []AuditArgs{
		{TenantID: a, UserID: ua, Event: evSignedIn, IP: "203.0.113.1", Device: "Chrome on macOS", OccurredAt: now},
		{TenantID: b, UserID: ub, Event: "account.signed_out", IP: "203.0.113.2", Device: "Safari on iOS", OccurredAt: now},
	} {
		if err := e.work(args); err != nil {
			t.Fatalf("Work: %v", err)
		}
	}
	for _, c := range []struct {
		tenant, own, other string
		ev                 string
	}{{a, ua, ub, evSignedIn}, {b, ub, ua, "account.signed_out"}} {
		own := e.rowsUnder(t, c.tenant, c.own)
		if len(own) != 1 || own[0].Event != c.ev {
			t.Errorf("tenant %s reads %+v for its own member, want one %s row", c.tenant, own, c.ev)
		}
		if other := e.rowsUnder(t, c.tenant, c.other); len(other) != 0 {
			t.Errorf("tenant %s reads %d row(s) of the other tenant's member, want 0", c.tenant, len(other))
		}
	}
}

// AC 5: a tenant that does not exist (a purged workspace) cancels the job; River must not retry it for days.
func TestAuditJob_UnknownTenantCancelsTheJob(t *testing.T) {
	e := newEnv(t)
	err := e.work(AuditArgs{TenantID: "6f1c2a3e-8b4d-4c5e-9a7f-0123456789ab", UserID: e.user(), Event: evSignedIn, OccurredAt: time.Now().UTC().Format(time.RFC3339)})
	var cancel *rivertype.JobCancelError
	if !errors.As(err, &cancel) {
		t.Errorf("Work = %v, want a river.JobCancel error", err)
	}
}

// A job that reads the tenant while purge_tenant has deleted it but not committed must not leave a row behind.
func TestAuditJob_PurgeInFlightLeavesNoRow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, u := e.tenant(t), e.user()

	purge, err := e.admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin purge: %v", err)
	}
	defer func() { _ = purge.Rollback(ctx) }()
	if _, err := purge.Exec(ctx, `SET LOCAL session_replication_role = 'replica'`); err != nil {
		t.Fatalf("replica role: %v", err)
	}
	if _, err := purge.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, a); err != nil {
		t.Fatalf("purge tenant: %v", err)
	}
	if _, err := purge.Exec(ctx, `DELETE FROM audit_log WHERE tenant_id = $1`, a); err != nil {
		t.Fatalf("purge audit: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- e.work(AuditArgs{TenantID: a, UserID: u, Event: evSignedIn, IP: e.tag + "-purge"})
	}()
	time.Sleep(500 * time.Millisecond)
	if err := purge.Commit(ctx); err != nil {
		t.Fatalf("commit purge: %v", err)
	}

	var jobErr error
	select {
	case jobErr = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("job did not return after the purge committed")
	}
	var cancel *rivertype.JobCancelError
	if !errors.As(jobErr, &cancel) {
		t.Fatalf("job returned %v, want a cancel", jobErr)
	}
	if n := e.mustCount(t, `SELECT count(*) FROM audit_log WHERE tenant_id = $1`, a); n != 0 {
		t.Fatalf("%d audit row(s) outlive the purged tenant, want 0", n)
	}
}
