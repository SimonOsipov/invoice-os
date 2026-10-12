package customrule_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/customrule"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	entityMsg = "entity_id is not a client company of this workspace"
	capMsg    = "a workspace can hold at most 100 custom rules"
)

// --- fixtures ---------------------------------------------------------------

func dbTestPools(t *testing.T) (super, app *pgxpool.Pool) {
	t.Helper()
	appURL, superURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("customrule db test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL")
	}
	ctx := context.Background()
	s, err := pgxpool.New(ctx, superURL)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("ping superuser: %v", err)
	}
	a, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	t.Cleanup(a.Close)
	return s, a
}

func seedTenant(t *testing.T, super *pgxpool.Pool, label string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm')`, id, label); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id) })
	return id
}

func seedMember(t *testing.T, super *pgxpool.Pool, tenantID, role, status string) (string, context.Context) {
	t.Helper()
	subject := uuid.NewString()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, $3, $4)`,
		tenantID, subject, role, status); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	return subject, auth.WithIdentity(context.Background(),
		auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})
}

func seedEntity(t *testing.T, super *pgxpool.Pool, tenantID, name string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO business_entities (tenant_id, name) VALUES ($1, $2) RETURNING id`, tenantID, name).Scan(&id); err != nil {
		t.Fatalf("seed business_entities: %v", err)
	}
	return id
}

type workspace struct {
	id                                      string
	adminSubject                            string
	admin, preparer, reviewer, suspendedAdm context.Context
}

func seedWorkspace(t *testing.T, super *pgxpool.Pool, label string) workspace {
	t.Helper()
	id := seedTenant(t, super, label)
	w := workspace{id: id}
	w.adminSubject, w.admin = seedMember(t, super, id, "admin", "active")
	_, w.preparer = seedMember(t, super, id, "preparer", "active")
	_, w.reviewer = seedMember(t, super, id, "reviewer", "active")
	_, w.suspendedAdm = seedMember(t, super, id, "admin", "suspended")
	return w
}

// fakeCheck stands in for Validator.CheckCustomRule and counts its calls.
type fakeCheck struct {
	calls  atomic.Int32
	mu     sync.Mutex
	err    error
	inputs []customrule.CheckInput
}

func (f *fakeCheck) fn(_ context.Context, in customrule.CheckInput) error {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, in)
	return f.err
}

func (f *fakeCheck) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeCheck) last() customrule.CheckInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.inputs) == 0 {
		return customrule.CheckInput{}
	}
	return f.inputs[len(f.inputs)-1]
}

func input(entity *string, msg string) customrule.RuleInput {
	return customrule.RuleInput{EntityID: entity, Type: "required", Target: "buyer.name",
		Params: json.RawMessage(`{}`), Severity: "error", Message: msg, Enabled: true}
}

type seedRev struct {
	Tenant, RuleID string
	Revision       int
	Entity         *string
	Severity       string
	Message        string
	Enabled        bool
	Deleted        bool
}

func rev(tenant, ruleID string, n int) seedRev {
	return seedRev{Tenant: tenant, RuleID: ruleID, Revision: n, Severity: "error", Message: "m", Enabled: true}
}

func insertRev(t *testing.T, super *pgxpool.Pool, r seedRev) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO custom_rule_revisions
		     (tenant_id, rule_id, revision, entity_id, type, target, params, severity, message, enabled, deleted, created_by)
		 VALUES ($1, $2, $3, $4, 'required', 'buyer.name', '{}', $5, $6, $7, $8, 'seed') RETURNING id`,
		r.Tenant, r.RuleID, r.Revision, r.Entity, r.Severity, r.Message, r.Enabled, r.Deleted).Scan(&id); err != nil {
		t.Fatalf("seed custom_rule_revisions: %v", err)
	}
	return id
}

// seedRule inserts revision 1 of a new error+enabled rule and returns its rule and revision ids.
func seedRule(t *testing.T, super *pgxpool.Pool, tenantID string, entity *string, msg string) (ruleID, revID string) {
	t.Helper()
	ruleID = uuid.NewString()
	r := rev(tenantID, ruleID, 1)
	r.Entity, r.Message = entity, msg
	return ruleID, insertRev(t, super, r)
}

type revRow struct {
	ID, TenantID, RuleID string
	Revision             int
	EntityID             *string
	Type, Target, Params string
	Severity             string
	When                 *string
	Message              string
	Enabled, Deleted     bool
	CreatedBy            string
	RecheckedNull        bool
}

// revRows reads the revisions as the superuser; an empty ruleID reads the whole tenant.
func revRows(t *testing.T, super *pgxpool.Pool, tenantID, ruleID string) []revRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT id, tenant_id, rule_id, revision, entity_id, type, target, params::text, severity, "when",
		        message, enabled, deleted, created_by, rechecked_at IS NULL
		   FROM custom_rule_revisions
		  WHERE tenant_id = $1 AND ($2 = '' OR rule_id::text = $2)
		  ORDER BY rule_id, revision`, tenantID, ruleID)
	if err != nil {
		t.Fatalf("read custom_rule_revisions: %v", err)
	}
	defer rows.Close()
	var out []revRow
	for rows.Next() {
		var r revRow
		if err := rows.Scan(&r.ID, &r.TenantID, &r.RuleID, &r.Revision, &r.EntityID, &r.Type, &r.Target, &r.Params,
			&r.Severity, &r.When, &r.Message, &r.Enabled, &r.Deleted, &r.CreatedBy, &r.RecheckedNull); err != nil {
			t.Fatalf("scan custom_rule_revisions: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate custom_rule_revisions: %v", err)
	}
	return out
}

func revCount(t *testing.T, super *pgxpool.Pool, tenantID string) int {
	t.Helper()
	return len(revRows(t, super, tenantID, ""))
}

// currentCount counts rules whose latest revision is not deleted (disabled ones count).
func currentCount(t *testing.T, super *pgxpool.Pool, tenantID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM (SELECT DISTINCT ON (rule_id) deleted FROM custom_rule_revisions
		                        WHERE tenant_id = $1 ORDER BY rule_id, revision DESC) c WHERE NOT deleted`,
		tenantID).Scan(&n); err != nil {
		t.Fatalf("count current rules: %v", err)
	}
	return n
}

func snapshot(t *testing.T, super *pgxpool.Pool, tenantID string) string {
	t.Helper()
	var s string
	if err := super.QueryRow(context.Background(),
		`SELECT coalesce(string_agg(to_jsonb(r)::text, '|' ORDER BY rule_id, revision), '')
		   FROM custom_rule_revisions r WHERE tenant_id = $1`, tenantID).Scan(&s); err != nil {
		t.Fatalf("snapshot custom_rule_revisions: %v", err)
	}
	return s
}

type auditRow struct {
	Actor, Event string
	Payload      map[string]any
	EntityID     *string
}

func auditRows(t *testing.T, super *pgxpool.Pool, tenantID, event string) []auditRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT actor, event, payload, entity_id::text FROM audit_log WHERE tenant_id = $1 AND event = $2 ORDER BY id`,
		tenantID, event)
	if err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var a auditRow
		var raw []byte
		if err := rows.Scan(&a.Actor, &a.Event, &raw, &a.EntityID); err != nil {
			t.Fatalf("scan audit_log: %v", err)
		}
		if err := json.Unmarshal(raw, &a.Payload); err != nil {
			t.Fatalf("decode audit payload %s: %v", raw, err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate audit_log: %v", err)
	}
	return out
}

func ruleEvents() []string {
	return []string{"custom_rule.created", "custom_rule.updated", "custom_rule.deleted"}
}

func auditTotal(t *testing.T, super *pgxpool.Pool, tenantID string) int {
	t.Helper()
	n := 0
	for _, ev := range ruleEvents() {
		n += len(auditRows(t, super, tenantID, ev))
	}
	return n
}

func jsonEq(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal([]byte(a), &x); err != nil {
		t.Fatalf("json %q: %v", a, err)
	}
	if err := json.Unmarshal([]byte(b), &y); err != nil {
		t.Fatalf("json %q: %v", b, err)
	}
	return reflect.DeepEqual(x, y)
}

func newMux(store *customrule.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/custom-rules", customrule.ListHandler(store.List, nil))
	mux.HandleFunc("POST /v1/custom-rules", customrule.CreateHandler(store.Create, nil))
	mux.HandleFunc("PUT /v1/custom-rules/{id}", customrule.UpdateHandler(store.Update, nil))
	mux.HandleFunc("DELETE /v1/custom-rules/{id}", customrule.DeleteHandler(store.Delete, nil))
	return mux
}

func do(t *testing.T, h http.Handler, ctx context.Context, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body == nil {
		rd = strings.NewReader("")
	} else {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rd = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, target, rd).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func errorBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var b map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("response is not the {\"error\"} envelope: %q (%v)", rec.Body.String(), err)
	}
	return b["error"]
}

func asInvalid(err error) (customrule.InvalidError, bool) {
	var ie customrule.InvalidError
	return ie, errors.As(err, &ie)
}

// refused reports a 403-class refusal; a suspended admin is stopped by the seam first.
func refused(err error, suspended bool) bool {
	return errors.Is(err, customrule.ErrNotPermitted) || (suspended && errors.Is(err, db.ErrNotActiveMember))
}

// --- AC 1 ---------------------------------------------------------------------

func TestCustomRules_AdminCreatesFirmAndClientRules(t *testing.T) {
	super, app := dbTestPools(t)
	ws := seedWorkspace(t, super, "customrule create")
	entity := seedEntity(t, super, ws.id, "Client A")
	fc := &fakeCheck{}
	store := customrule.NewStore(app, fc.fn)

	firm, err := store.Create(ws.admin, input(nil, "firm rule"))
	if err != nil {
		t.Fatalf("Create firm-wide rule: %v", err)
	}
	client, err := store.Create(ws.admin, input(&entity, "client rule"))
	if err != nil {
		t.Fatalf("Create client rule: %v", err)
	}

	for name, r := range map[string]customrule.Rule{"firm": firm, "client": client} {
		if _, err := uuid.Parse(r.ID); err != nil {
			t.Errorf("%s rule id %q is not a uuid", name, r.ID)
		}
		if r.Revision != 1 {
			t.Errorf("%s rule revision = %d, want 1", name, r.Revision)
		}
		if r.Key != "custom:"+r.ID || customrule.Key(r.ID) != "custom:"+r.ID {
			t.Errorf("%s rule key = %q, Key() = %q, want custom:%s", name, r.Key, customrule.Key(r.ID), r.ID)
		}
		rows := revRows(t, super, ws.id, r.ID)
		if len(rows) != 1 {
			t.Fatalf("%s rule stored revisions = %d, want 1 (in the caller's tenant %s)", name, len(rows), ws.id)
		}
		got := rows[0]
		if got.ID != r.RevisionID || got.TenantID != ws.id || got.Revision != 1 || got.CreatedBy != ws.adminSubject {
			t.Errorf("%s stored row = id %s tenant %s revision %d by %s, want id %s tenant %s revision 1 by %s",
				name, got.ID, got.TenantID, got.Revision, got.CreatedBy, r.RevisionID, ws.id, ws.adminSubject)
		}
		if got.Type != "required" || got.Target != "buyer.name" || got.Severity != "error" || !got.Enabled || got.Deleted {
			t.Errorf("%s stored row content = %+v", name, got)
		}
	}
	if rows := revRows(t, super, ws.id, firm.ID); len(rows) == 1 && rows[0].EntityID != nil {
		t.Errorf("firm rule stored entity_id = %v, want NULL", *rows[0].EntityID)
	}
	if rows := revRows(t, super, ws.id, client.ID); len(rows) == 1 && (rows[0].EntityID == nil || *rows[0].EntityID != entity) {
		t.Errorf("client rule stored entity_id = %v, want %s", rows[0].EntityID, entity)
	}
	if n := fc.calls.Load(); n != 2 {
		t.Errorf("check calls = %d, want 2 (one per create)", n)
	}
}

func TestCustomRules_ListShowsCurrentRulesToEveryMember(t *testing.T) {
	super, app := dbTestPools(t)
	ws := seedWorkspace(t, super, "customrule list")
	store := customrule.NewStore(app, (&fakeCheck{}).fn)

	r1, r2, gone := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{r1, r2, gone} {
		insertRev(t, super, rev(ws.id, id, 1))
	}
	edited := rev(ws.id, r1, 2)
	edited.Message = "first, edited"
	insertRev(t, super, edited)
	off := rev(ws.id, r2, 2)
	off.Enabled = false
	insertRev(t, super, off)
	del := rev(ws.id, gone, 2)
	del.Deleted = true
	insertRev(t, super, del)

	for name, ctx := range map[string]context.Context{"preparer": ws.preparer, "reviewer": ws.reviewer, "admin": ws.admin} {
		got, err := store.List(ctx)
		if err != nil {
			t.Errorf("List as %s: %v", name, err)
			continue
		}
		if len(got) != 2 {
			t.Errorf("List as %s = %d rules, want 2 (the deleted one is absent)", name, len(got))
			continue
		}
		if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].ID < got[j].ID }) {
			t.Errorf("List as %s is not ordered by id", name)
		}
		by := map[string]customrule.Rule{got[0].ID: got[0], got[1].ID: got[1]}
		if g := by[r1]; g.Revision != 2 || g.Message != "first, edited" || !g.Enabled {
			t.Errorf("List as %s: edited rule = revision %d message %q enabled %v, want revision 2 %q enabled",
				name, g.Revision, g.Message, g.Enabled, "first, edited")
		}
		if g := by[r2]; g.Revision != 2 || g.Enabled {
			t.Errorf("List as %s: disabled rule = revision %d enabled %v, want revision 2 enabled false", name, g.Revision, g.Enabled)
		}
	}
	if _, err := store.List(ws.suspendedAdm); !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("List as suspended admin err = %v, want db.ErrNotActiveMember", err)
	}
}

// --- AC 2 ---------------------------------------------------------------------

func TestCustomRules_NonAdminCannotWriteWhateverTheBody(t *testing.T) {
	super, app := dbTestPools(t)
	ws := seedWorkspace(t, super, "customrule non-admin")
	existing := uuid.NewString()
	insertRev(t, super, rev(ws.id, existing, 1))
	before := snapshot(t, super, ws.id)
	fc := &fakeCheck{}
	store := customrule.NewStore(app, fc.fn)

	badSeverity := input(nil, "x")
	badSeverity.Severity = "info"
	foreign := uuid.NewString()
	bodies := map[string]customrule.RuleInput{
		"valid":          input(nil, "x"),
		"empty":          {},
		"bad severity":   badSeverity,
		"unknown entity": input(&foreign, "x"),
	}
	callers := map[string]context.Context{"preparer": ws.preparer, "reviewer": ws.reviewer, "suspended admin": ws.suspendedAdm}
	for who, ctx := range callers {
		susp := who == "suspended admin"
		for name, body := range bodies {
			if _, err := store.Create(ctx, body); !refused(err, susp) {
				t.Errorf("%s Create (%s body) err = %v, want ErrNotPermitted", who, name, err)
			}
			if _, err := store.Update(ctx, existing, body); !refused(err, susp) {
				t.Errorf("%s Update (%s body) err = %v, want ErrNotPermitted", who, name, err)
			}
			if _, err := store.Update(ctx, uuid.NewString(), body); !refused(err, susp) {
				t.Errorf("%s Update of an unknown id (%s body) err = %v, want ErrNotPermitted, not a 404", who, name, err)
			}
		}
		if err := store.Delete(ctx, existing); !refused(err, susp) {
			t.Errorf("%s Delete err = %v, want ErrNotPermitted", who, err)
		}
		if err := store.Delete(ctx, uuid.NewString()); !refused(err, susp) {
			t.Errorf("%s Delete of an unknown id err = %v, want ErrNotPermitted, not a 404", who, err)
		}
	}
	for _, who := range []string{"preparer", "reviewer"} {
		if _, err := store.Create(callers[who], input(nil, "x")); !errors.Is(err, customrule.ErrNotPermitted) {
			t.Errorf("%s Create err = %v, want exactly ErrNotPermitted", who, err)
		}
	}

	if n := fc.calls.Load(); n != 0 {
		t.Errorf("check calls after refused writes = %d, want 0", n)
	}
	if after := snapshot(t, super, ws.id); after != before {
		t.Errorf("revision rows changed by refused writes:\nbefore %s\nafter  %s", before, after)
	}
	if n := auditTotal(t, super, ws.id); n != 0 {
		t.Errorf("audit rows after refused writes = %d, want 0", n)
	}

	// Positive controls: the admin writes and the preparer reads.
	if _, err := store.Create(ws.admin, input(nil, "ok")); err != nil {
		t.Errorf("control: admin Create: %v", err)
	}
	if got, err := store.List(ws.preparer); err != nil || len(got) != 2 {
		t.Errorf("control: preparer List = %d rules, err %v, want 2", len(got), err)
	}
	if n := fc.calls.Load(); n != 1 {
		t.Errorf("control: check calls = %d, want 1", n)
	}
}

// --- AC 3 ---------------------------------------------------------------------

func TestCustomRules_EachWriteAuditsInTheSameTx(t *testing.T) {
	super, app := dbTestPools(t)
	ws := seedWorkspace(t, super, "customrule audit")
	entity := seedEntity(t, super, ws.id, "Client A")
	store := customrule.NewStore(app, (&fakeCheck{}).fn)

	created, err := store.Create(ws.admin, input(&entity, "m1"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Update(ws.admin, created.ID, input(&entity, "m2")); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := store.Delete(ws.admin, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	rows := revRows(t, super, ws.id, created.ID)
	if len(rows) != 3 {
		t.Fatalf("stored revisions = %d, want 3", len(rows))
	}
	for i, ev := range ruleEvents() {
		got := auditRows(t, super, ws.id, ev)
		if len(got) != 1 {
			t.Errorf("%s audit rows = %d, want 1", ev, len(got))
			continue
		}
		a, stored := got[0], rows[i]
		if a.Actor != ws.adminSubject {
			t.Errorf("%s actor = %q, want %q", ev, a.Actor, ws.adminSubject)
		}
		if a.Payload["rule_id"] != created.ID || a.Payload["revision_id"] != stored.ID ||
			a.Payload["revision"] != float64(i+1) || a.Payload["entity_id"] != entity {
			t.Errorf("%s payload = %v, want rule_id %s revision_id %s revision %d entity_id %s",
				ev, a.Payload, created.ID, stored.ID, i+1, entity)
		}
		if ev == "custom_rule.deleted" {
			continue
		}
		var params any
		_ = json.Unmarshal([]byte(stored.Params), &params)
		var when any
		if stored.When != nil {
			when = *stored.When
		}
		want := map[string]any{"type": stored.Type, "target": stored.Target, "params": params,
			"severity": stored.Severity, "when": when, "message": stored.Message, "enabled": stored.Enabled}
		if !reflect.DeepEqual(a.Payload["rule"], want) {
			t.Errorf("%s payload rule = %v, want the stored revision %v", ev, a.Payload["rule"], want)
		}
	}
	if !rows[2].Deleted {
		t.Errorf("revision 3 deleted = false, want true")
	}
}

func TestCustomRules_RevisionAndAuditShareOneTx(t *testing.T) {
	super, app := dbTestPools(t)
	ws := seedWorkspace(t, super, "customrule xmin")
	store := customrule.NewStore(app, (&fakeCheck{}).fn)

	created, err := store.Create(ws.admin, input(nil, "m1"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Update(ws.admin, created.ID, input(nil, "m2")); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := store.Delete(ws.admin, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	rows := revRows(t, super, ws.id, created.ID)
	if len(rows) != 3 {
		t.Fatalf("stored revisions = %d, want 3", len(rows))
	}
	for i, ev := range ruleEvents() {
		var revXmin, auditXmin string
		if err := super.QueryRow(context.Background(),
			`SELECT r.xmin::text, a.xmin::text
			   FROM custom_rule_revisions r JOIN audit_log a
			     ON a.tenant_id = r.tenant_id AND a.payload->>'revision_id' = r.id::text
			  WHERE r.tenant_id = $1 AND r.id = $2 AND a.event = $3`,
			ws.id, rows[i].ID, ev).Scan(&revXmin, &auditXmin); err != nil {
			t.Errorf("%s: xmin join (no row means the revision and its audit row do not both exist): %v", ev, err)
			continue
		}
		for label, x := range map[string]string{"revision": revXmin, "audit_log": auditXmin} {
			if x == "0" || x == "2" {
				t.Fatalf("%s.xmin = %s: a frozen xid makes this proof vacuous", label, x)
			}
		}
		if revXmin != auditXmin {
			t.Errorf("%s: revision xmin = %s, audit xmin = %s, want one transaction", ev, revXmin, auditXmin)
		}
	}
}

func TestCustomRules_AuditScopeFollowsTheRule(t *testing.T) {
	super, app := dbTestPools(t)
	ws := seedWorkspace(t, super, "customrule scope")
	entity := seedEntity(t, super, ws.id, "Client A")
	store := customrule.NewStore(app, (&fakeCheck{}).fn)

	client, err := store.Create(ws.admin, input(&entity, "client"))
	if err != nil {
		t.Fatalf("Create client rule: %v", err)
	}
	firm, err := store.Create(ws.admin, input(nil, "firm"))
	if err != nil {
		t.Fatalf("Create firm rule: %v", err)
	}
	if _, err := store.Update(ws.admin, firm.ID, input(nil, "firm 2")); err != nil {
		t.Fatalf("Update firm rule: %v", err)
	}
	if err := store.Delete(ws.admin, firm.ID); err != nil {
		t.Fatalf("Delete firm rule: %v", err)
	}

	created := auditRows(t, super, ws.id, "custom_rule.created")
	if len(created) != 2 {
		t.Fatalf("custom_rule.created rows = %d, want 2", len(created))
	}
	for _, a := range created {
		switch a.Payload["rule_id"] {
		case client.ID:
			if a.EntityID == nil || *a.EntityID != entity {
				t.Errorf("client rule audit entity_id = %v, want %s", a.EntityID, entity)
			}
			if s := audit.ScopeOf(a.Event, a.EntityID); s != audit.ScopeCompany {
				t.Errorf("client rule audit scope = %q, want company", s)
			}
		case firm.ID:
			if a.EntityID != nil {
				t.Errorf("firm rule audit entity_id = %s, want NULL", *a.EntityID)
			}
		default:
			t.Errorf("unexpected audit payload %v", a.Payload)
		}
	}
	for _, ev := range ruleEvents() {
		for _, a := range auditRows(t, super, ws.id, ev) {
			if a.Payload["rule_id"] != firm.ID {
				continue
			}
			if s := audit.ScopeOf(ev, a.EntityID); s != audit.ScopeWorkspace {
				t.Errorf("firm rule %s scope = %q, want workspace", ev, s)
			}
		}
	}
}

// --- AC 4 ---------------------------------------------------------------------

func TestCustomRules_UpdateAppendsAndKeepsTheOldRevision(t *testing.T) {
	super, app := dbTestPools(t)
	ws := seedWorkspace(t, super, "customrule update")
	store := customrule.NewStore(app, (&fakeCheck{}).fn)
	ruleID, rev1ID := seedRule(t, super, ws.id, nil, "old message")
	created := customrule.Rule{ID: ruleID, Key: customrule.Key(ruleID), RevisionID: rev1ID}
	var rev1Before string
	if err := super.QueryRow(context.Background(),
		`SELECT to_jsonb(r)::text FROM custom_rule_revisions r WHERE id = $1`, created.RevisionID).Scan(&rev1Before); err != nil {
		t.Fatalf("snapshot revision 1: %v", err)
	}

	updated, err := store.Update(ws.admin, created.ID, input(nil, "new message"))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.ID != created.ID || updated.Key != created.Key || updated.Revision != 2 ||
		updated.Message != "new message" || updated.RevisionID == created.RevisionID {
		t.Errorf("Update answer = id %s key %s revision %d message %q revision_id %s, want the same rule at revision 2 with a new revision id",
			updated.ID, updated.Key, updated.Revision, updated.Message, updated.RevisionID)
	}
	rows := revRows(t, super, ws.id, created.ID)
	if len(rows) != 2 || rows[1].Revision != 2 || rows[1].Message != "new message" || rows[1].ID != updated.RevisionID {
		t.Fatalf("stored revisions = %+v, want revisions 1 and 2 with revision 2 holding the new message", rows)
	}
	var rev1After string
	if err := super.QueryRow(context.Background(),
		`SELECT to_jsonb(r)::text FROM custom_rule_revisions r WHERE id = $1`, created.RevisionID).Scan(&rev1After); err != nil {
		t.Fatalf("re-read revision 1: %v", err)
	}
	if rev1After != rev1Before {
		t.Errorf("revision 1 changed:\nbefore %s\nafter  %s", rev1Before, rev1After)
	}
}

func TestCustomRules_ConcurrentUpdatesBothLand(t *testing.T) {
	super, app := dbTestPools(t)
	ws := seedWorkspace(t, super, "customrule concurrent update")
	store := customrule.NewStore(app, (&fakeCheck{}).fn)
	ruleID, _ := seedRule(t, super, ws.id, nil, "start")
	created := customrule.Rule{ID: ruleID}

	const writers = 8
	sent := map[string]bool{}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, writers)
	for i := 0; i < writers; i++ {
		msg := fmt.Sprintf("writer-%d", i)
		sent[msg] = true
		wg.Add(1)
		go func(i int, msg string) {
			defer wg.Done()
			<-start
			_, errs[i] = store.Update(ws.admin, created.ID, input(nil, msg))
		}(i, msg)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("writer %d: %v", i, err)
		}
	}
	rows := revRows(t, super, ws.id, created.ID)
	if len(rows) != writers+1 {
		t.Fatalf("stored revisions = %d, want %d", len(rows), writers+1)
	}
	got := map[string]bool{}
	for i, r := range rows {
		if r.Revision != i+1 {
			t.Errorf("revision at position %d = %d, want consecutive numbering", i, r.Revision)
		}
		if i > 0 {
			got[r.Message] = true
		}
	}
	if !reflect.DeepEqual(got, sent) {
		t.Errorf("messages stored = %v, want every writer's message exactly once (%v)", got, sent)
	}
	list, err := store.List(ws.admin)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %d rules, err %v, want 1", len(list), err)
	}
	if last := rows[len(rows)-1]; list[0].Revision != last.Revision || list[0].Message != last.Message {
		t.Errorf("current rule = revision %d %q, want the last revision %d %q (last write wins)",
			list[0].Revision, list[0].Message, last.Revision, last.Message)
	}
}

func TestCustomRules_DeleteHidesTheRule(t *testing.T) {
	super, app := dbTestPools(t)
	ws := seedWorkspace(t, super, "customrule delete")
	store := customrule.NewStore(app, (&fakeCheck{}).fn)
	aID, _ := seedRule(t, super, ws.id, nil, "a")
	bID, _ := seedRule(t, super, ws.id, nil, "b")
	a, b := customrule.Rule{ID: aID}, customrule.Rule{ID: bID}

	if err := store.Delete(ws.admin, a.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	rows := revRows(t, super, ws.id, a.ID)
	if len(rows) != 2 || rows[0].Deleted || !rows[1].Deleted || rows[1].Revision != 2 {
		t.Fatalf("stored revisions = %+v, want revision 1 live and revision 2 deleted", rows)
	}
	list, err := store.List(ws.preparer)
	if err != nil || len(list) != 1 || list[0].ID != b.ID {
		t.Errorf("List after delete = %v, err %v, want only rule b", list, err)
	}

	if _, err := store.Update(ws.admin, a.ID, input(nil, "again")); !errors.Is(err, customrule.ErrNotFound) {
		t.Errorf("Update of a deleted rule err = %v, want ErrNotFound", err)
	}
	if err := store.Delete(ws.admin, a.ID); !errors.Is(err, customrule.ErrNotFound) {
		t.Errorf("second Delete err = %v, want ErrNotFound", err)
	}
	if n := len(revRows(t, super, ws.id, a.ID)); n != 2 {
		t.Errorf("revisions after refused update and delete = %d, want 2", n)
	}
	if n := len(auditRows(t, super, ws.id, "custom_rule.deleted")); n != 1 {
		t.Errorf("custom_rule.deleted rows = %d, want 1", n)
	}
}

// --- AC 5 ---------------------------------------------------------------------

func TestCustomRules_ForeignOrUnknownEntityIsTheSame400(t *testing.T) {
	super, app := dbTestPools(t)
	a := seedWorkspace(t, super, "customrule entity A")
	b := seedTenant(t, super, "customrule entity B")
	foreign := seedEntity(t, super, b, "B's client")
	own := seedEntity(t, super, a.id, "A's client")
	store := customrule.NewStore(app, (&fakeCheck{}).fn)
	existingID, _ := seedRule(t, super, a.id, nil, "existing")
	existing := customrule.Rule{ID: existingID}
	before := snapshot(t, super, a.id)
	auditBefore := auditTotal(t, super, a.id)

	unknown := uuid.NewString()
	for name, id := range map[string]string{"another tenant's entity": foreign, "unknown entity": unknown} {
		id := id
		_, cerr := store.Create(a.admin, input(&id, "x"))
		_, uerr := store.Update(a.admin, existing.ID, input(&id, "x"))
		for op, err := range map[string]error{"Create": cerr, "Update": uerr} {
			ie, ok := asInvalid(err)
			if !ok || ie.Msg != entityMsg {
				t.Errorf("%s with %s err = %v, want InvalidError %q", op, name, err, entityMsg)
			}
		}
	}
	if after := snapshot(t, super, a.id); after != before {
		t.Errorf("revision rows changed by refused entity ids:\nbefore %s\nafter  %s", before, after)
	}
	if n := auditTotal(t, super, a.id); n != auditBefore {
		t.Errorf("audit rows = %d, want %d (a refused write audits nothing)", n, auditBefore)
	}
	if n := revCount(t, super, b); n != 0 {
		t.Errorf("tenant B revisions = %d, want 0", n)
	}
	if got, err := store.List(a.admin); err != nil || len(got) != 1 || got[0].Revision != 1 {
		t.Errorf("List = %v, err %v, want the existing rule still at revision 1", got, err)
	}
	if _, err := store.Create(a.admin, input(&own, "ok")); err != nil {
		t.Errorf("control: Create with the workspace's own entity: %v", err)
	}
}

func TestCustomRules_ForeignOrUnknownRuleIsTheSame404(t *testing.T) {
	super, app := dbTestPools(t)
	a := seedWorkspace(t, super, "customrule rule A")
	b := seedWorkspace(t, super, "customrule rule B")
	store := customrule.NewStore(app, (&fakeCheck{}).fn)
	theirsID, _ := seedRule(t, super, b.id, nil, "b's rule")
	theirs := customrule.Rule{ID: theirsID}
	before := snapshot(t, super, b.id)

	for name, id := range map[string]string{"another tenant's rule": theirs.ID, "unknown rule": uuid.NewString()} {
		if _, err := store.Update(a.admin, id, input(nil, "x")); !errors.Is(err, customrule.ErrNotFound) {
			t.Errorf("Update of %s err = %v, want ErrNotFound", name, err)
		}
		if err := store.Delete(a.admin, id); !errors.Is(err, customrule.ErrNotFound) {
			t.Errorf("Delete of %s err = %v, want ErrNotFound", name, err)
		}
	}
	if after := snapshot(t, super, b.id); after != before {
		t.Errorf("B's rows changed by A's refused writes:\nbefore %s\nafter  %s", before, after)
	}
	if n := revCount(t, super, a.id); n != 0 {
		t.Errorf("tenant A revisions = %d, want 0", n)
	}
	if n := len(auditRows(t, super, a.id, "custom_rule.updated")) + len(auditRows(t, super, a.id, "custom_rule.deleted")); n != 0 {
		t.Errorf("tenant A audit rows = %d, want 0", n)
	}
	if _, err := store.Update(b.admin, theirs.ID, input(nil, "own edit")); err != nil {
		t.Errorf("control: B's own Update: %v", err)
	}
}

func TestCustomRules_CrossTenantReadsAndWritesAreRefused(t *testing.T) {
	super, app := dbTestPools(t)
	a := seedWorkspace(t, super, "customrule rls A")
	b := seedWorkspace(t, super, "customrule rls B")
	entityA := seedEntity(t, super, a.id, "A's client")
	store := customrule.NewStore(app, (&fakeCheck{}).fn)
	raID, raRev := seedRule(t, super, a.id, &entityA, "a's rule")
	rbID, _ := seedRule(t, super, b.id, nil, "b's rule")
	ra, rb := customrule.Rule{ID: raID, RevisionID: raRev}, customrule.Rule{ID: rbID}

	for who, tc := range map[string]struct {
		ctx  context.Context
		want string
		not  string
	}{"B admin": {b.admin, rb.ID, ra.ID}, "B preparer": {b.preparer, rb.ID, ra.ID}, "A preparer": {a.preparer, ra.ID, rb.ID}} {
		got, err := store.List(tc.ctx)
		if err != nil || len(got) != 1 || got[0].ID != tc.want {
			t.Errorf("List as %s = %v, err %v, want only own tenant's rule %s", who, got, err, tc.want)
		}
	}
	if _, err := store.Create(b.admin, input(&entityA, "x")); err == nil {
		t.Errorf("B Create with A's entity succeeded, want InvalidError")
	}
	if rows := revRows(t, super, b.id, ""); len(rows) != 1 {
		t.Errorf("tenant B revisions = %d, want 1 (its own rule only)", len(rows))
	}
	if rows := revRows(t, super, a.id, ""); len(rows) != 1 || rows[0].ID != ra.RevisionID {
		t.Errorf("tenant A revisions = %+v, want A's single revision untouched", rows)
	}
}

// --- AC 6 ---------------------------------------------------------------------

func TestCustomRules_CheckRefusalIs400AndStoresNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ws := seedWorkspace(t, super, "customrule check refusal")
	fc := &fakeCheck{}
	store := customrule.NewStore(app, fc.fn)
	h := newMux(store)
	existing := uuid.NewString()
	insertRev(t, super, rev(ws.id, existing, 1))
	before := snapshot(t, super, ws.id)

	const reason = "params.expr must compile to a boolean expression"
	fc.setErr(customrule.InvalidError{Msg: reason})
	body := input(nil, "refused")

	rec := do(t, h, ws.admin, http.MethodPost, "/v1/custom-rules", body)
	if rec.Code != http.StatusBadRequest || errorBody(t, rec) != reason {
		t.Errorf("POST refused by the check = %d %q, want 400 %q", rec.Code, rec.Body.String(), reason)
	}
	rec = do(t, h, ws.admin, http.MethodPut, "/v1/custom-rules/"+existing, body)
	if rec.Code != http.StatusBadRequest || errorBody(t, rec) != reason {
		t.Errorf("PUT refused by the check = %d %q, want 400 %q", rec.Code, rec.Body.String(), reason)
	}
	if n := fc.calls.Load(); n != 2 {
		t.Errorf("check calls = %d, want 2 (the refusals came from the check)", n)
	}
	if after := snapshot(t, super, ws.id); after != before {
		t.Errorf("rows changed by check-refused saves:\nbefore %s\nafter  %s", before, after)
	}
	if n := auditTotal(t, super, ws.id); n != 0 {
		t.Errorf("audit rows after check-refused saves = %d, want 0", n)
	}

	fc.setErr(nil)
	rec = do(t, h, ws.admin, http.MethodPost, "/v1/custom-rules", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("control: POST with a passing check = %d %q, want 201", rec.Code, rec.Body.String())
	}
	if got := fc.last(); got.Type != body.Type || got.Target != body.Target || got.Severity != body.Severity ||
		got.Message != body.Message || got.When != nil || !jsonEq(t, string(got.Params), string(body.Params)) {
		t.Errorf("check received %+v, want the body's type, target, params, severity, when and message", got)
	}
}

func TestCustomRules_CheckOutageIs502AndStoresNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ws := seedWorkspace(t, super, "customrule check outage")
	fc := &fakeCheck{}
	store := customrule.NewStore(app, fc.fn)
	h := newMux(store)
	existing := uuid.NewString()
	insertRev(t, super, rev(ws.id, existing, 1))
	before := snapshot(t, super, ws.id)

	fc.setErr(fmt.Errorf("%w: validation service returned status 500", invoice.ErrUpstream))
	if _, err := store.Create(ws.admin, input(nil, "x")); !errors.Is(err, customrule.ErrCheckUnavailable) {
		t.Errorf("Create during a check outage err = %v, want ErrCheckUnavailable", err)
	}
	if _, err := store.Update(ws.admin, existing, input(nil, "x")); !errors.Is(err, customrule.ErrCheckUnavailable) {
		t.Errorf("Update during a check outage err = %v, want ErrCheckUnavailable", err)
	}
	const wantBody = "custom rules cannot be checked right now"
	for _, tc := range []struct{ method, target string }{
		{http.MethodPost, "/v1/custom-rules"},
		{http.MethodPut, "/v1/custom-rules/" + existing},
	} {
		rec := do(t, h, ws.admin, tc.method, tc.target, input(nil, "x"))
		if rec.Code != http.StatusBadGateway || errorBody(t, rec) != wantBody {
			t.Errorf("%s during a check outage = %d %q, want 502 %q", tc.method, rec.Code, rec.Body.String(), wantBody)
		}
	}
	if n := fc.calls.Load(); n != 4 {
		t.Errorf("check calls = %d, want 4", n)
	}
	if after := snapshot(t, super, ws.id); after != before {
		t.Errorf("rows changed during a check outage:\nbefore %s\nafter  %s", before, after)
	}
	if n := auditTotal(t, super, ws.id); n != 0 {
		t.Errorf("audit rows during a check outage = %d, want 0", n)
	}

	fc.setErr(nil)
	if _, err := store.Create(ws.admin, input(nil, "x")); err != nil {
		t.Errorf("control: Create once the check is back: %v", err)
	}
}

// --- AC 7 ---------------------------------------------------------------------

func TestCustomRules_PendingMarkFollowsBlockingRevisions(t *testing.T) {
	super, app := dbTestPools(t)
	ws := seedWorkspace(t, super, "customrule pending mark")
	store := customrule.NewStore(app, (&fakeCheck{}).fn)

	blocking, err := store.Create(ws.admin, input(nil, "blocks"))
	if err != nil {
		t.Fatalf("Create error rule: %v", err)
	}
	warn := input(nil, "warns")
	warn.Severity = "warning"
	warning, err := store.Create(ws.admin, warn)
	if err != nil {
		t.Fatalf("Create warning rule: %v", err)
	}
	off := input(nil, "off")
	off.Enabled = false
	if _, err := store.Create(ws.admin, off); err != nil {
		t.Fatalf("Create disabled error rule: %v", err)
	}
	if rows := revRows(t, super, ws.id, blocking.ID); len(rows) != 1 || !rows[0].RecheckedNull {
		t.Errorf("created error+enabled revision: rechecked_at NULL = %v, want true", rows)
	}
	if rows := revRows(t, super, ws.id, warning.ID); len(rows) != 1 || rows[0].RecheckedNull {
		t.Errorf("created warning revision: rechecked_at NULL = %v, want false", rows)
	}

	if _, err := store.Update(ws.admin, blocking.ID, off); err != nil {
		t.Fatalf("Update to disabled: %v", err)
	}
	if _, err := store.Update(ws.admin, blocking.ID, input(nil, "blocks again")); err != nil {
		t.Fatalf("Update back to error+enabled: %v", err)
	}
	if err := store.Delete(ws.admin, blocking.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	rows := revRows(t, super, ws.id, blocking.ID)
	if len(rows) != 4 {
		t.Fatalf("blocking rule revisions = %d, want 4", len(rows))
	}
	for i, wantNull := range []bool{true, false, true, false} {
		if rows[i].RecheckedNull != wantNull {
			t.Errorf("revision %d (enabled %v, deleted %v): rechecked_at NULL = %v, want %v",
				rows[i].Revision, rows[i].Enabled, rows[i].Deleted, rows[i].RecheckedNull, wantNull)
		}
	}

	all := revRows(t, super, ws.id, "")
	if len(all) != 6 {
		t.Fatalf("tenant revisions = %d, want 6", len(all))
	}
	for _, r := range all {
		want := r.Enabled && r.Severity == "error" && !r.Deleted
		if r.RecheckedNull != want {
			t.Errorf("rule %s revision %d (enabled %v, %s, deleted %v): rechecked_at NULL = %v, want %v",
				r.RuleID, r.Revision, r.Enabled, r.Severity, r.Deleted, r.RecheckedNull, want)
		}
	}
}

// --- AC 8: CurrentTx ----------------------------------------------------------

func currentTx(t *testing.T, app *pgxpool.Pool, ctx context.Context, entities []string) []customrule.Rule {
	t.Helper()
	var out []customrule.Rule
	err := db.WithinRequestTenantTx(ctx, app, func(tx pgx.Tx) error {
		var err error
		out, err = customrule.CurrentTx(ctx, tx, entities)
		return err
	})
	if err != nil {
		t.Fatalf("CurrentTx(%v): %v", entities, err)
	}
	return out
}

func messages(rules []customrule.Rule) []string {
	out := make([]string, len(rules))
	for i, r := range rules {
		out[i] = fmt.Sprintf("%s@%d", r.Message, r.Revision)
	}
	sort.Strings(out)
	return out
}

func TestCurrentTx_LatestRevisionInScope(t *testing.T) {
	super, app := dbTestPools(t)
	ws := seedWorkspace(t, super, "customrule currenttx scope")
	ea := seedEntity(t, super, ws.id, "A")
	eb := seedEntity(t, super, ws.id, "B")

	firm := uuid.NewString()
	insertRev(t, super, seedRev{Tenant: ws.id, RuleID: firm, Revision: 1, Severity: "error", Message: "firm", Enabled: true})
	firmRev2 := insertRev(t, super, seedRev{Tenant: ws.id, RuleID: firm, Revision: 2, Severity: "warning", Message: "firm", Enabled: true})
	// Same message at both revisions: only the revision number tells them apart.
	ruleA, ruleB := uuid.NewString(), uuid.NewString()
	revA := insertRev(t, super, seedRev{Tenant: ws.id, RuleID: ruleA, Revision: 1, Entity: &ea, Severity: "error", Message: "client-a", Enabled: true})
	insertRev(t, super, seedRev{Tenant: ws.id, RuleID: ruleB, Revision: 1, Entity: &eb, Severity: "error", Message: "client-b", Enabled: true})
	disabled := uuid.NewString()
	insertRev(t, super, seedRev{Tenant: ws.id, RuleID: disabled, Revision: 1, Severity: "error", Message: "disabled", Enabled: true})
	insertRev(t, super, seedRev{Tenant: ws.id, RuleID: disabled, Revision: 2, Severity: "error", Message: "disabled", Enabled: false})
	deleted := uuid.NewString()
	insertRev(t, super, seedRev{Tenant: ws.id, RuleID: deleted, Revision: 1, Severity: "error", Message: "deleted", Enabled: true})
	insertRev(t, super, seedRev{Tenant: ws.id, RuleID: deleted, Revision: 2, Severity: "error", Message: "deleted", Enabled: true, Deleted: true})

	got := currentTx(t, app, ws.preparer, []string{ea})
	if want := []string{"client-a@1", "firm@2"}; !reflect.DeepEqual(messages(got), want) {
		t.Fatalf("CurrentTx([A]) = %v, want %v", messages(got), want)
	}
	if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].ID < got[j].ID }) {
		t.Errorf("CurrentTx result is not ordered by rule id")
	}
	for _, r := range got {
		wantID := map[string]string{firm: firmRev2, ruleA: revA}[r.ID]
		if r.RevisionID != wantID || r.Key != "custom:"+r.ID {
			t.Errorf("rule %s: revision_id %s key %s, want revision_id %s key custom:%s", r.ID, r.RevisionID, r.Key, wantID, r.ID)
		}
	}
	if got := currentTx(t, app, ws.preparer, []string{ea, eb}); !reflect.DeepEqual(messages(got), []string{"client-a@1", "client-b@1", "firm@2"}) {
		t.Errorf("CurrentTx([A, B]) = %v, want both clients and the firm rule", messages(got))
	}
	if got := currentTx(t, app, ws.preparer, nil); !reflect.DeepEqual(messages(got), []string{"firm@2"}) {
		t.Errorf("CurrentTx(none) = %v, want only the firm-wide rule", messages(got))
	}
}

func TestCurrentTx_MovedRuleLeavesItsOldClient(t *testing.T) {
	super, app := dbTestPools(t)
	ws := seedWorkspace(t, super, "customrule currenttx moved")
	ea := seedEntity(t, super, ws.id, "A")
	eb := seedEntity(t, super, ws.id, "B")
	rule := uuid.NewString()
	insertRev(t, super, seedRev{Tenant: ws.id, RuleID: rule, Revision: 1, Entity: &ea, Severity: "error", Message: "moves", Enabled: true})
	moved := insertRev(t, super, seedRev{Tenant: ws.id, RuleID: rule, Revision: 2, Entity: &eb, Severity: "error", Message: "moves", Enabled: true})

	if got := currentTx(t, app, ws.admin, []string{ea}); len(got) != 0 {
		t.Errorf("CurrentTx([A]) = %v, want none: the rule moved to B", messages(got))
	}
	got := currentTx(t, app, ws.admin, []string{eb})
	if len(got) != 1 || got[0].Revision != 2 || got[0].RevisionID != moved {
		t.Errorf("CurrentTx([B]) = %v, want the rule at revision 2", messages(got))
	}
	if got := currentTx(t, app, ws.admin, []string{ea, eb}); len(got) != 1 {
		t.Errorf("CurrentTx([A, B]) = %d rules, want the moved rule once", len(got))
	}
}

func TestCurrentTx_NeverReadsAnotherTenant(t *testing.T) {
	super, app := dbTestPools(t)
	a := seedWorkspace(t, super, "customrule currenttx A")
	b := seedWorkspace(t, super, "customrule currenttx B")
	eb := seedEntity(t, super, b.id, "B's client")
	insertRev(t, super, seedRev{Tenant: b.id, RuleID: uuid.NewString(), Revision: 1, Severity: "error", Message: "b firm", Enabled: true})
	insertRev(t, super, seedRev{Tenant: b.id, RuleID: uuid.NewString(), Revision: 1, Entity: &eb, Severity: "error", Message: "b client", Enabled: true})

	if got := currentTx(t, app, b.admin, []string{eb}); len(got) != 2 {
		t.Fatalf("control: CurrentTx as B = %v, want B's two rules", messages(got))
	}
	if got := currentTx(t, app, a.admin, []string{eb}); len(got) != 0 {
		t.Errorf("CurrentTx as A with B's entity id = %v, want none", messages(got))
	}
	if got := currentTx(t, app, a.admin, nil); len(got) != 0 {
		t.Errorf("CurrentTx as A with no entities = %v, want none", messages(got))
	}
}

// --- AC 6 (D24): the 100-rule cap ----------------------------------------------

func seedCurrentRules(t *testing.T, super *pgxpool.Pool, tenantID string, n int) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO custom_rule_revisions (tenant_id, rule_id, revision, type, target, params, severity, message, enabled, created_by)
		 SELECT $1, gen_random_uuid(), 1, 'required', 'buyer.name', '{}', 'error', 'seed', g <> 1, 'seed'
		   FROM generate_series(1, $2::int) g`, tenantID, n); err != nil {
		t.Fatalf("seed %d rules: %v", n, err)
	}
}

func TestCustomRules_HundredAndFirstCreateIs400(t *testing.T) {
	super, app := dbTestPools(t)
	ws := seedWorkspace(t, super, "customrule cap")
	seedCurrentRules(t, super, ws.id, 100)
	if n := currentCount(t, super, ws.id); n != 100 {
		t.Fatalf("seeded current rules = %d, want 100", n)
	}
	fc := &fakeCheck{}
	store := customrule.NewStore(app, fc.fn)

	_, err := store.Create(ws.admin, input(nil, "101st"))
	if ie, ok := asInvalid(err); !ok || ie.Msg != capMsg {
		t.Fatalf("101st Create err = %v, want InvalidError %q", err, capMsg)
	}
	if n := fc.calls.Load(); n != 0 {
		t.Errorf("check calls on the refused 101st = %d, want 0", n)
	}
	if n := revCount(t, super, ws.id); n != 100 {
		t.Errorf("revisions after the refused create = %d, want 100", n)
	}
	if n := auditTotal(t, super, ws.id); n != 0 {
		t.Errorf("audit rows after the refused create = %d, want 0", n)
	}

	list, err := store.List(ws.admin)
	if err != nil || len(list) != 100 {
		t.Fatalf("List = %d rules, err %v, want 100", len(list), err)
	}
	if err := store.Delete(ws.admin, list[0].ID); err != nil {
		t.Fatalf("Delete one: %v", err)
	}
	if _, err := store.Create(ws.admin, input(nil, "fits now")); err != nil {
		t.Errorf("Create after a delete freed a place: %v", err)
	}
	if n := currentCount(t, super, ws.id); n != 100 {
		t.Errorf("current rules at the end = %d, want 100", n)
	}
}

func TestCustomRules_ConcurrentCreatesRespectTheCap(t *testing.T) {
	super, app := dbTestPools(t)
	ws := seedWorkspace(t, super, "customrule cap race")
	seedCurrentRules(t, super, ws.id, 99)
	store := customrule.NewStore(app, (&fakeCheck{}).fn)

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = store.Create(ws.admin, input(nil, fmt.Sprintf("racer-%d", i)))
		}(i)
	}
	close(start)
	wg.Wait()

	ok, refusedCap := 0, 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if ie, isInvalid := asInvalid(err); isInvalid && ie.Msg == capMsg {
			refusedCap++
		} else {
			t.Errorf("racing Create err = %v, want nil or the cap InvalidError", err)
		}
	}
	if ok != 1 || refusedCap != 1 {
		t.Errorf("racing creates: %d succeeded, %d refused by the cap, want 1 and 1", ok, refusedCap)
	}
	if n := currentCount(t, super, ws.id); n != 100 {
		t.Errorf("current rules = %d, want 100", n)
	}
}

// --- D27: the tenant save lock ---------------------------------------------------

func TestSaveLockKey_IsTheTenantAdvisoryKey(t *testing.T) {
	super, _ := dbTestPools(t)
	ws := seedWorkspace(t, super, "customrule lock key")
	ctx := context.Background()
	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('custom_rules:' || $1::text))`, ws.id); err != nil {
		t.Fatalf("take the design's lock: %v", err)
	}
	var free bool
	if err := super.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtext($1))`, customrule.SaveLockKey(ws.id)).Scan(&free); err != nil {
		t.Fatalf("try lock: %v", err)
	}
	if free {
		t.Errorf("SaveLockKey(%s) = %q hashes to a free lock, want the key of hashtext('custom_rules:' || tenant_id)",
			ws.id, customrule.SaveLockKey(ws.id))
	}
}

func TestCustomRules_EveryWriteWaitsForTheTenantSaveLock(t *testing.T) {
	super, app := dbTestPools(t)
	ws := seedWorkspace(t, super, "customrule lock wait")
	existing := uuid.NewString()
	insertRev(t, super, rev(ws.id, existing, 1))
	store := customrule.NewStore(app, (&fakeCheck{}).fn)

	ops := map[string]func() error{
		"Create": func() error { _, err := store.Create(ws.admin, input(nil, "x")); return err },
		"Update": func() error { _, err := store.Update(ws.admin, existing, input(nil, "x")); return err },
		"Delete": func() error { return store.Delete(ws.admin, existing) },
	}
	for _, name := range []string{"Create", "Update", "Delete"} {
		op := ops[name]
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			tx, err := super.Begin(ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('custom_rules:' || $1::text))`, ws.id); err != nil {
				t.Fatalf("take the save lock: %v", err)
			}
			done := make(chan error, 1)
			go func() { done <- op() }()
			select {
			case err := <-done:
				t.Fatalf("%s finished (err %v) while another session held the tenant save lock", name, err)
			case <-time.After(500 * time.Millisecond):
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatalf("release the lock: %v", err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("%s after the lock was released: %v", name, err)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%s did not finish after the lock was released", name)
			}
		})
	}
}
