// Store.RulesInForce and Store.SwitchRule against the dev DB, plus the real handler bound to the real Store.
// `rules` is global: each test registers restoreRulesOnCleanup first. staff_audit_log is append-only,
// so rows are found by a fresh actor uuid per test and stay behind. No t.Parallel().
package validation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// staffCtx is the context a handler sees behind auth.RequireRulesRole for subject.
func staffCtx(t *testing.T, subject string) context.Context {
	t.Helper()
	var ctx context.Context
	h := auth.RequireRulesRole(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() }))
	req := httptest.NewRequest("PATCH", "/v1/staff/rules/x", nil).
		WithContext(auth.WithIdentity(context.Background(), auth.Identity{Subject: subject, Role: "authenticated", Staff: true, RulesRole: true}))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if ctx == nil {
		t.Fatal("RequireRulesRole refused the staff caller")
	}
	return ctx
}

type auditRow struct {
	Event   string
	Version string
	Payload map[string]any
}

// auditRowsOf reads the actor's staff_audit_log rows oldest first, as the owner.
func auditRowsOf(t *testing.T, super *pgxpool.Pool, actor string) []auditRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT event, rule_set_version_id::text, payload::text FROM staff_audit_log WHERE actor = $1 ORDER BY id`, actor)
	if err != nil {
		t.Fatalf("read staff_audit_log: %v", err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		var payload string
		if err := rows.Scan(&r.Event, &r.Version, &payload); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(payload), &r.Payload); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func versionInForce(t *testing.T, super *pgxpool.Pool) (id string, version int) {
	t.Helper()
	if err := super.QueryRow(context.Background(),
		`SELECT v.id::text, v.version FROM rule_set_versions v WHERE v.id = rule_set_version_for((now() AT TIME ZONE 'UTC')::date)`,
	).Scan(&id, &version); err != nil {
		t.Fatalf("read the version in force: %v", err)
	}
	return id, version
}

func TestStaffRules_ListReturnsTheVersionInForce(t *testing.T) {
	super, app := dbTestPools(t)
	got, err := NewStore(app).RulesInForce(staffCtx(t, uuid.NewString()))
	if err != nil {
		t.Fatalf("RulesInForce: %v", err)
	}
	wantID, wantVersion := versionInForce(t, super)
	if got.RuleSetVersionID.String() != wantID || got.Version != wantVersion {
		t.Errorf("version = %s v%d, want %s v%d", got.RuleSetVersionID, got.Version, wantID, wantVersion)
	}
	rows, err := super.Query(context.Background(),
		`SELECT key, type, target, severity, scope, message, enabled FROM rules WHERE rule_set_version_id = $1 ORDER BY key`, wantID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var want []StaffRule
	for rows.Next() {
		var r StaffRule
		if err := rows.Scan(&r.Key, &r.Type, &r.Target, &r.Severity, &r.Scope, &r.Message, &r.Enabled); err != nil {
			t.Fatal(err)
		}
		want = append(want, r)
	}
	if len(want) == 0 || len(got.Rules) != len(want) {
		t.Fatalf("got %d rules, want %d (>0)", len(got.Rules), len(want))
	}
	for i := range want {
		if got.Rules[i] != want[i] {
			t.Errorf("rule %d = %+v, want %+v", i, got.Rules[i], want[i])
		}
		if got.Rules[i].Scope != "document" {
			t.Errorf("rule %s scope = %q, want document", got.Rules[i].Key, got.Rules[i].Scope)
		}
	}
}

func TestStaffRules_SwitchOffAndOnChangesTheNextLoad(t *testing.T) {
	super, app := dbTestPools(t)
	restoreRulesOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)
	ctx, store := staffCtx(t, actor), NewStore(app)
	enabledIn := func() bool {
		rs, err := store.LoadForDates(ctx, []string{"2026-10-11"})
		if err != nil {
			t.Fatalf("LoadForDates: %v", err)
		}
		for _, r := range rs["2026-10-11"].Rules {
			if r.Key == functionKey {
				return r.Enabled
			}
		}
		t.Fatalf("%s missing from the loaded set", functionKey)
		return false
	}
	if !enabledIn() {
		t.Fatal("fixture: the rule starts disabled")
	}
	res, err := store.SwitchRule(ctx, functionKey, false, "r1")
	if err != nil || res.Key != functionKey || res.Enabled {
		t.Fatalf("SwitchRule off = %+v, %v", res, err)
	}
	if enabledIn() {
		t.Error("the next load still sees the rule enabled")
	}
	if _, err := store.SwitchRule(ctx, functionKey, true, "r2"); err != nil {
		t.Fatalf("SwitchRule on: %v", err)
	}
	if !enabledIn() {
		t.Error("the next load still sees the rule disabled")
	}
}

func TestStaffRules_SwitchWritesTheAuditRowInTheSameTx(t *testing.T) {
	super, app := dbTestPools(t)
	restoreRulesOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)
	res, err := NewStore(app).SwitchRule(staffCtx(t, actor), functionKey, false, "kill it")
	if err != nil {
		t.Fatalf("SwitchRule: %v", err)
	}
	verID, ver := versionInForce(t, super)
	rows := auditRowsOf(t, super, actor)
	if len(rows) != 1 {
		t.Fatalf("%d audit rows, want 1", len(rows))
	}
	r := rows[0]
	if r.Event != "validation.rule.disabled" || r.Version != verID || res.RuleSetVersionID.String() != verID || res.RuleSetVersion != ver {
		t.Errorf("row = %+v, result = %+v, want event validation.rule.disabled and version %s v%d", r, res, verID, ver)
	}
	if r.Payload["key"] != functionKey || r.Payload["enabled"] != false || r.Payload["version"] != float64(ver) || r.Payload["reason"] != "kill it" || len(r.Payload) != 4 {
		t.Errorf("payload = %v", r.Payload)
	}
}

func TestStaffRules_AuditFailureRollsTheFlipBack(t *testing.T) {
	super, app := dbTestPools(t)
	restoreRulesOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)
	orig := recordStaff
	t.Cleanup(func() { recordStaff = orig })
	boom := errors.New("audit down")
	recordStaff = func(context.Context, pgx.Tx, uuid.UUID, uuid.UUID, string, any) error { return boom }

	_, err := NewStore(app).SwitchRule(staffCtx(t, actor), functionKey, false, "r")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the audit error", err)
	}
	if !ruleEnabledActive(t, super, functionKey) {
		t.Error("rules.enabled changed although the audit write failed")
	}
	if n := len(auditRowsOf(t, super, actor)); n != 0 {
		t.Errorf("%d audit rows, want 0", n)
	}
}

// The binding PM condition: a flip through the real handler and the real Store leaves one audit row,
// and a failed audit write is a 500 with the rule unchanged.
func TestStaffRules_RouteFlipWritesTheAuditRow(t *testing.T) {
	super, app := dbTestPools(t)
	restoreRulesOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)
	mux := http.NewServeMux()
	mux.Handle("PATCH /v1/staff/rules/{key}", StaffSwitchRuleHandler(NewStore(app).SwitchRule, nil))
	patch := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("PATCH", "/v1/staff/rules/"+functionKey, strings.NewReader(body)).WithContext(staffCtx(t, actor)))
		return rec
	}

	rec := patch(`{"enabled":false,"reason":"via route"}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if ruleEnabledActive(t, super, functionKey) {
		t.Error("the route did not disable the rule")
	}
	rows := auditRowsOf(t, super, actor)
	if len(rows) != 1 || rows[0].Event != "validation.rule.disabled" || rows[0].Payload["reason"] != "via route" {
		t.Fatalf("audit rows = %+v, want one validation.rule.disabled with the reason", rows)
	}

	orig := recordStaff
	t.Cleanup(func() { recordStaff = orig })
	recordStaff = func(context.Context, pgx.Tx, uuid.UUID, uuid.UUID, string, any) error {
		return errors.New("audit down")
	}
	if rec := patch(`{"enabled":true,"reason":"again"}`); rec.Code != 500 {
		t.Fatalf("status with a failing audit = %d, want 500", rec.Code)
	}
	if ruleEnabledActive(t, super, functionKey) {
		t.Error("the flip survived a failed audit write")
	}
	if n := len(auditRowsOf(t, super, actor)); n != 1 {
		t.Errorf("%d audit rows, want still 1", n)
	}
}

func TestStaffRules_ReEnableWritesAnEnabledAuditRow(t *testing.T) {
	super, app := dbTestPools(t)
	restoreRulesOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)
	ctx, store := staffCtx(t, actor), NewStore(app)
	if _, err := store.SwitchRule(ctx, functionKey, false, "r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SwitchRule(ctx, functionKey, true, "r2"); err != nil {
		t.Fatal(err)
	}
	rows := auditRowsOf(t, super, actor)
	if len(rows) != 2 || rows[0].Event != "validation.rule.disabled" || rows[1].Event != "validation.rule.enabled" {
		t.Fatalf("rows = %+v, want disabled then enabled", rows)
	}
	if rows[1].Payload["enabled"] != true || rows[1].Payload["reason"] != "r2" {
		t.Errorf("second payload = %v", rows[1].Payload)
	}
}

func TestStaffRules_ConcurrentSwitchesGiveOneSuccessAndOne409(t *testing.T) {
	super, app := dbTestPools(t)
	restoreRulesOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)
	ctx, store := staffCtx(t, actor), NewStore(app)
	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = store.SwitchRule(ctx, functionKey, false, "r")
		}()
	}
	close(start)
	wg.Wait()
	var ok, conflict int
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrRuleAlreadyInState):
			conflict++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Errorf("successes = %d, conflicts = %d, want 1 and 1", ok, conflict)
	}
	if n := len(auditRowsOf(t, super, actor)); n != 1 {
		t.Errorf("%d audit rows, want 1", n)
	}
}

func TestStaffRules_UnknownKeyIs404AndNoAuditRow(t *testing.T) {
	super, app := dbTestPools(t)
	restoreRulesOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)
	_, err := NewStore(app).SwitchRule(staffCtx(t, actor), "no-such-rule", false, "r")
	if !errors.Is(err, ErrRuleNotInForce) {
		t.Fatalf("err = %v, want ErrRuleNotInForce", err)
	}
	if status, _ := staffRulesError(err); status != 404 {
		t.Errorf("status = %d, want 404", status)
	}
	if n := len(auditRowsOf(t, super, actor)); n != 0 {
		t.Errorf("%d audit rows, want 0", n)
	}
}

func TestStaffRules_SameStateIs409AndNoAuditRow(t *testing.T) {
	super, app := dbTestPools(t)
	restoreRulesOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)
	_, err := NewStore(app).SwitchRule(staffCtx(t, actor), functionKey, true, "r")
	if !errors.Is(err, ErrRuleAlreadyInState) {
		t.Fatalf("err = %v, want ErrRuleAlreadyInState", err)
	}
	if !ruleEnabledActive(t, super, functionKey) {
		t.Error("rules.enabled changed")
	}
	if n := len(auditRowsOf(t, super, actor)); n != 0 {
		t.Errorf("%d audit rows, want 0", n)
	}
}

func TestStaffRules_NonUUIDSubjectIs403(t *testing.T) {
	super, app := dbTestPools(t)
	restoreRulesOnCleanup(t, super)
	count := func() (n int) {
		if err := super.QueryRow(context.Background(), `SELECT count(*) FROM staff_audit_log`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	_, err := NewStore(app).SwitchRule(staffCtx(t, "not-a-uuid"), functionKey, false, "r")
	if !errors.Is(err, db.ErrNotStaff) {
		t.Fatalf("err = %v, want db.ErrNotStaff", err)
	}
	if !ruleEnabledActive(t, super, functionKey) {
		t.Error("rules.enabled changed")
	}
	if after := count(); after != before {
		t.Errorf("staff_audit_log grew from %d to %d rows", before, after)
	}
}

func TestStaffRules_ActorWithoutTheRulesRoleIs403(t *testing.T) {
	super, app := dbTestPools(t)
	restoreRulesOnCleanup(t, super)
	actor := uuid.NewString()
	if _, err := super.Exec(context.Background(), `INSERT INTO staff_members (user_id, rules_role) VALUES ($1, false)`, actor); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = super.Exec(context.Background(), `DELETE FROM staff_members WHERE user_id = $1`, actor) })

	_, err := NewStore(app).SwitchRule(staffCtx(t, actor), functionKey, false, "r")
	if !errors.Is(err, db.ErrNotStaff) {
		t.Fatalf("err = %v, want db.ErrNotStaff (42501 mapped)", err)
	}
	if !ruleEnabledActive(t, super, functionKey) {
		t.Error("rules.enabled changed")
	}
	if n := len(auditRowsOf(t, super, actor)); n != 0 {
		t.Errorf("%d audit rows, want 0", n)
	}
}
