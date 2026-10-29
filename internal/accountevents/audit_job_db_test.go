package accountevents

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

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
