package tenancy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// The sentence restated on purpose: the wire oracle for approval's memberLastSignerMessage.
const lastSignerSentence = "this change would leave a workflow role an open approval run is waiting on with no active admin or reviewer — add another admin or reviewer to the role, or decide the run or edit its invoice to cancel it, first"

const approver = "approver"

func lsInsertID(t *testing.T, super *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return id
}

// seedWaitingRun seeds a validated invoice with an open run and one pending approval step on roleKey.
func seedWaitingRun(t *testing.T, super *pgxpool.Pool, tenant, roleKey string) (invoiceID, runID string) {
	t.Helper()
	sfx := uuid.NewString()[:8]
	entity := lsInsertID(t, super, `INSERT INTO business_entities (tenant_id, name) VALUES ($1, $2) RETURNING id`, tenant, "Run Corp "+sfx)
	policy := lsInsertID(t, super, `INSERT INTO approval_policies (tenant_id, name) VALUES ($1, $2) RETURNING id`, tenant, "Run policy "+sfx)
	version := lsInsertID(t, super, `INSERT INTO approval_policy_versions (tenant_id, policy_id, version) VALUES ($1, $2, 1) RETURNING id`, tenant, policy)
	invoiceID = lsInsertID(t, super, `INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3) RETURNING id`, tenant, entity, "INV-"+sfx)
	if _, err := super.Exec(context.Background(), `UPDATE invoices SET status = 'validated' WHERE id = $1`, invoiceID); err != nil {
		t.Fatalf("validate invoice: %v", err)
	}
	runID = lsInsertID(t, super,
		`INSERT INTO approval_runs (tenant_id, invoice_id, policy_version_id, content_fingerprint) VALUES ($1, $2, $3, $4) RETURNING id`,
		tenant, invoiceID, version, "fp-"+sfx)
	if _, err := super.Exec(context.Background(),
		`INSERT INTO approval_run_steps (tenant_id, run_id, ord, kind, state, workflow_role_key) VALUES ($1, $2, 0, 'approval', 'pending', $3)`,
		tenant, runID, roleKey); err != nil {
		t.Fatalf("seed approval step: %v", err)
	}
	return invoiceID, runID
}

func cancelRun(t *testing.T, super *pgxpool.Pool, runID string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`UPDATE approval_runs SET state = 'cancelled', closed_at = now(), closed_by = 'qa' WHERE id = $1`, runID); err != nil {
		t.Fatalf("cancel run: %v", err)
	}
}

func lsWorld(t *testing.T, super, app *pgxpool.Pool) invWorld {
	t.Helper()
	w := invWorld{super: super, app: app, store: NewStore(app), tenant: seedTenant(t, super, "APSC-13 last signer")}
	w.admin = w.addMember(t, "admin", "active", "Ada Obi")
	return w
}

// lsGranted seeds a member holding two clients and "All clients", so a remove would change more than the status.
func lsGranted(t *testing.T, w invWorld, role, status string) string {
	t.Helper()
	id := w.addMember(t, role, status, "Rita "+uuid.NewString()[:4])
	giveClients(t, w.super, w.tenant, id, seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant))
	giveAllClients(t, w.super, w.tenant, id)
	return id
}

func lsStaff(t *testing.T, super *pgxpool.Pool, tenant, roleID, user string, ord int) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO workflow_role_members (tenant_id, workflow_role_id, user_id, ord) VALUES ($1, $2, $3, $4)`,
		tenant, roleID, user, ord); err != nil {
		t.Fatalf("staff role: %v", err)
	}
}

// lsState is the members' rows and client counts, the tenant's role rows and its audit count, as one string.
func (w invWorld) lsState(t *testing.T, users ...string) string {
	t.Helper()
	var b strings.Builder
	for _, u := range users {
		fmt.Fprintf(&b, "%+v\n", imageOf(t, w.super, w.tenant, u))
	}
	rows, err := w.super.Query(context.Background(),
		`SELECT r.key, m.user_id::text, m.ord FROM workflow_role_members m
		   JOIN workflow_roles r ON r.id = m.workflow_role_id
		  WHERE m.tenant_id = $1 ORDER BY r.key, m.ord`, w.tenant)
	if err != nil {
		t.Fatalf("read role rows: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, user string
		var ord int
		if err := rows.Scan(&key, &user, &ord); err != nil {
			t.Fatalf("scan role row: %v", err)
		}
		fmt.Fprintf(&b, "%s/%s/%d\n", key, user, ord)
	}
	fmt.Fprintf(&b, "audit=%d", w.auditTotal(t))
	return b.String()
}

func (w invWorld) signerCount(t *testing.T, key string) int {
	t.Helper()
	var n int
	if err := w.super.QueryRow(context.Background(),
		`SELECT count(*) FROM workflow_role_members m
		   JOIN workflow_roles r ON r.id = m.workflow_role_id
		   JOIN memberships ms ON ms.tenant_id = m.tenant_id AND ms.user_id = m.user_id
		  WHERE m.tenant_id = $1 AND r.key = $2 AND ms.status = 'active' AND ms.role IN ('admin', 'reviewer')`,
		w.tenant, key).Scan(&n); err != nil {
		t.Fatalf("count signers: %v", err)
	}
	return n
}

func (w invWorld) lsRemove(caller, target string) error {
	_, err := w.store.RemoveMember(w.as(caller), target)
	return err
}

func (w invWorld) lsSetStatus(caller, target, status string) error {
	_, err := w.store.SetMembershipStatus(w.as(caller), target, status)
	return err
}

func (w invWorld) lsSetRole(caller, target, role string) error {
	_, err := w.store.SetMemberRole(w.as(caller), target, role)
	return err
}

type lsChange struct {
	name  string
	event string
	do    func(w invWorld, caller, target string) error
}

var lsThreeChanges = []lsChange{
	{"remove", "membership.removed", func(w invWorld, c, u string) error { return w.lsRemove(c, u) }},
	{"suspend", "membership.suspended", func(w invWorld, c, u string) error { return w.lsSetStatus(c, u, "suspended") }},
	{"demote to preparer", roleChangedEvent, func(w invWorld, c, u string) error { return w.lsSetRole(c, u, "preparer") }},
}

type lsBody struct {
	Error string
	Items []map[string]string
	Total int
}

// wantRefusalBody asserts the 409 wire shape and returns the parsed body.
func wantRefusalBody(t *testing.T, rec *httptest.ResponseRecorder) lsBody {
	t.Helper()
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, body %s, want 409", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode body %s: %v", rec.Body.String(), err)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if got := strings.Join(keys, ","); got != "error,invoices,invoices_total,policies" {
		t.Fatalf("body keys = %s, want error,invoices,invoices_total,policies", got)
	}
	var b lsBody
	if err := json.Unmarshal(raw["error"], &b.Error); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if b.Error != lastSignerSentence {
		t.Errorf("error = %q, want the member sentence", b.Error)
	}
	if string(raw["policies"]) != "[]" {
		t.Errorf("policies = %s, want []", raw["policies"])
	}
	if err := json.Unmarshal(raw["invoices"], &b.Items); err != nil {
		t.Fatalf("decode invoices: %v", err)
	}
	if err := json.Unmarshal(raw["invoices_total"], &b.Total); err != nil {
		t.Fatalf("decode invoices_total: %v", err)
	}
	for _, it := range b.Items {
		ks := make([]string, 0, len(it))
		for k := range it {
			ks = append(ks, k)
		}
		slices.Sort(ks)
		if got := strings.Join(ks, ","); got != "id,invoice_number,workflow_role_key,workflow_role_title" {
			t.Errorf("item keys = %s, want id,invoice_number,workflow_role_key,workflow_role_title", got)
		}
	}
	return b
}

// wantLastSigner asserts a store error is the member refusal: it wraps ErrRoleInUse and writes the member body.
func wantLastSigner(t *testing.T, err error) lsBody {
	t.Helper()
	if !errors.Is(err, approval.ErrRoleInUse) {
		t.Fatalf("err = %v, want approval.ErrRoleInUse", err)
	}
	rec := httptest.NewRecorder()
	approval.WriteRoleInUse(rec, err)
	return wantRefusalBody(t, rec)
}

func lsMux(store *Store, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /v1/memberships/{user_id}", SetMembershipStatusHandler(store.SetMembershipStatus, log))
	mux.HandleFunc("POST /v1/memberships/{user_id}/remove", RemoveMemberHandler(store.RemoveMember, log))
	mux.HandleFunc("PATCH /v1/memberships/{user_id}/role", SetMemberRoleHandler(store.SetMemberRole, log))
	return mux
}

func (w invWorld) lsHTTP(h http.Handler, caller, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{Subject: caller, Role: "authenticated", TenantID: w.tenant}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func lsRace(a, b func() error) (errA, errB error) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(2)
	go func() { defer wg.Done(); <-start; errA = a() }()
	go func() { defer wg.Done(); <-start; errB = b() }()
	close(start)
	wg.Wait()
	return errA, errB
}

func TestLastSigner_RemoveSuspendAndDemoteAreRefusedWhileARunWaits(t *testing.T) {
	super, app := dbTestPools(t)
	for _, c := range lsThreeChanges {
		t.Run(c.name, func(t *testing.T) {
			w := lsWorld(t, super, app)
			r := lsGranted(t, w, "reviewer", "active")
			seedRoleStaffedBy(t, super, w.tenant, approver, r)
			invoice, _ := seedWaitingRun(t, super, w.tenant, approver)
			before := w.lsState(t, r)
			if img := imageOf(t, super, w.tenant, r); img.Clients != 2 || img.Roles != 1 || !img.AllClients {
				t.Fatalf("fixture: %+v, want 2 clients, 1 role, all_clients", img)
			}

			body := wantLastSigner(t, c.do(w, w.admin, r))

			if len(body.Items) != 1 || body.Items[0]["id"] != invoice || body.Items[0]["workflow_role_key"] != approver || body.Total != 1 {
				t.Errorf("items = %v total %d, want invoice %s on %s", body.Items, body.Total, invoice, approver)
			}
			if after := w.lsState(t, r); after != before {
				t.Errorf("a refusal wrote:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

func TestLastSigner_SelfChangeOfTheLastSignerIsRefused(t *testing.T) {
	super, app := dbTestPools(t)
	for _, c := range lsThreeChanges[1:] {
		t.Run(c.name, func(t *testing.T) {
			w := lsWorld(t, super, app)
			b := w.addMember(t, "admin", "active", "Zed Okoro")
			seedRoleStaffedBy(t, super, w.tenant, approver, w.admin)
			seedWaitingRun(t, super, w.tenant, approver)
			before := w.lsState(t, w.admin, b)

			wantLastSigner(t, c.do(w, w.admin, w.admin))

			if after := w.lsState(t, w.admin, b); after != before {
				t.Errorf("a refused self-change wrote:\nbefore %s\nafter  %s", before, after)
			}
			if n := w.auditCountOf(t, c.event); n != 0 {
				t.Errorf("%s rows = %d, want 0", c.event, n)
			}
		})
	}
}

func TestLastSigner_AnAdminWhoIsTheLastSignerIsRefused(t *testing.T) {
	super, app := dbTestPools(t)
	for _, c := range lsThreeChanges[1:] {
		t.Run(c.name, func(t *testing.T) {
			w := lsWorld(t, super, app)
			b := w.addMember(t, "admin", "active", "Zed Okoro")
			seedRoleStaffedBy(t, super, w.tenant, approver, b)
			seedWaitingRun(t, super, w.tenant, approver)
			before := w.lsState(t, w.admin, b)

			wantLastSigner(t, c.do(w, w.admin, b))

			if after := w.lsState(t, w.admin, b); after != before {
				t.Errorf("a refusal wrote:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

func TestLastSigner_HandlersAnswerTheRoleInUseBody(t *testing.T) {
	super, app := dbTestPools(t)
	calls := []struct{ name, method, path, body string }{
		{"remove", "POST", "/v1/memberships/%s/remove", ""},
		{"suspend", "PATCH", "/v1/memberships/%s", `{"status":"suspended"}`},
		{"demote", "PATCH", "/v1/memberships/%s/role", `{"role":"preparer"}`},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			w := lsWorld(t, super, app)
			r := w.addMember(t, "reviewer", "active", "Rita Eze")
			seedRoleStaffedBy(t, super, w.tenant, approver, r)
			invoice, _ := seedWaitingRun(t, super, w.tenant, approver)
			var number string
			if err := super.QueryRow(context.Background(), `SELECT invoice_number FROM invoices WHERE id = $1`, invoice).Scan(&number); err != nil {
				t.Fatalf("read invoice number: %v", err)
			}
			var logs bytes.Buffer
			h := lsMux(w.store, slog.New(slog.NewTextHandler(&logs, nil)))

			rec := w.lsHTTP(h, w.admin, c.method, fmt.Sprintf(c.path, r), c.body)

			body := wantRefusalBody(t, rec)
			if len(body.Items) != 1 || body.Total != 1 {
				t.Fatalf("items = %v total %d, want one item", body.Items, body.Total)
			}
			it := body.Items[0]
			if it["id"] != invoice || it["invoice_number"] != number || it["workflow_role_key"] != approver || it["workflow_role_title"] != approver {
				t.Errorf("item = %v, want invoice %s %s on %s", it, invoice, number, approver)
			}
			if strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("a refusal logged an error: %s", logs.String())
			}
		})
	}
}

func TestLastSigner_AnotherSignerLeftLetsTheChangeThrough(t *testing.T) {
	super, app := dbTestPools(t)
	for _, c := range lsThreeChanges {
		t.Run(c.name, func(t *testing.T) {
			w := lsWorld(t, super, app)
			r := w.addMember(t, "reviewer", "active", "Rita Eze")
			s := w.addMember(t, "reviewer", "active", "Sam Eze")
			seedRoleStaffedBy(t, super, w.tenant, approver, r, s)
			seedWaitingRun(t, super, w.tenant, approver)

			if err := c.do(w, w.admin, r); err != nil {
				t.Fatalf("%s with another signer left: %v, want nil", c.name, err)
			}
			if n := w.auditCountOf(t, c.event); n != 1 {
				t.Errorf("%s rows = %d, want 1", c.event, n)
			}
		})
	}
}

func TestLastSigner_ChangesThatKeepSigningGoThrough(t *testing.T) {
	super, app := dbTestPools(t)
	t.Run("demote admin to reviewer", func(t *testing.T) {
		w := lsWorld(t, super, app)
		r := w.addMember(t, "admin", "active", "Rita Eze")
		seedRoleStaffedBy(t, super, w.tenant, approver, r)
		seedWaitingRun(t, super, w.tenant, approver)
		if err := w.lsSetRole(w.admin, r, "reviewer"); err != nil {
			t.Fatalf("demote admin to reviewer: %v, want nil", err)
		}
		if n := w.auditCountOf(t, roleChangedEvent); n != 1 {
			t.Errorf("%s rows = %d, want 1", roleChangedEvent, n)
		}
	})
	t.Run("promote reviewer to admin", func(t *testing.T) {
		w := lsWorld(t, super, app)
		r := w.addMember(t, "reviewer", "active", "Rita Eze")
		seedRoleStaffedBy(t, super, w.tenant, approver, r)
		seedWaitingRun(t, super, w.tenant, approver)
		if err := w.lsSetRole(w.admin, r, "admin"); err != nil {
			t.Fatalf("promote reviewer to admin: %v, want nil", err)
		}
		if n := w.auditCountOf(t, roleChangedEvent); n != 1 {
			t.Errorf("%s rows = %d, want 1", roleChangedEvent, n)
		}
	})
}

// The role stays stranded through every step, so a reactivation or promotion that called the check would be refused.
func TestLastSigner_ReactivationAndPromotionGoThrough(t *testing.T) {
	super, app := dbTestPools(t)
	steps := []struct {
		name string
		do   func(w invWorld, p, s string) error
	}{
		{"reactivate the preparer", func(w invWorld, p, _ string) error { return w.lsSetStatus(w.admin, p, "active") }},
		{"promote the suspended preparer to reviewer", func(w invWorld, p, _ string) error { return w.lsSetRole(w.admin, p, "reviewer") }},
		{"reactivate the reviewer", func(w invWorld, _, s string) error { return w.lsSetStatus(w.admin, s, "active") }},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			w := lsWorld(t, super, app)
			p := w.addMember(t, "preparer", "suspended", "Pia Preparer")
			s := w.addMember(t, "reviewer", "suspended", "Sam Eze")
			seedRoleStaffedBy(t, super, w.tenant, approver, p, s)
			seedWaitingRun(t, super, w.tenant, approver)
			if n := w.signerCount(t, approver); n != 0 {
				t.Fatalf("fixture: signers = %d, want 0 (a stranded role)", n)
			}

			if err := st.do(w, p, s); err != nil {
				t.Fatalf("%s: %v, want nil", st.name, err)
			}
		})
	}
}

func TestLastSigner_NoWaitingRunGoesThrough(t *testing.T) {
	super, app := dbTestPools(t)
	ends := []struct {
		name string
		end  func(t *testing.T, w invWorld, invoice, run string)
	}{
		{"run cancelled", func(t *testing.T, _ invWorld, _, run string) { cancelRun(t, super, run) }},
		{"invoice queued", func(t *testing.T, _ invWorld, invoice, _ string) {
			if _, err := super.Exec(context.Background(), `UPDATE invoices SET status = 'queued' WHERE id = $1`, invoice); err != nil {
				t.Fatalf("queue invoice: %v", err)
			}
		}},
	}
	for _, e := range ends {
		for _, c := range lsThreeChanges {
			t.Run(e.name+"/"+c.name, func(t *testing.T) {
				w := lsWorld(t, super, app)
				r := w.addMember(t, "reviewer", "active", "Rita Eze")
				seedRoleStaffedBy(t, super, w.tenant, approver, r)
				invoice, run := seedWaitingRun(t, super, w.tenant, approver)
				e.end(t, w, invoice, run)

				if err := c.do(w, w.admin, r); err != nil {
					t.Fatalf("%s after the %s: %v, want nil", c.name, e.name, err)
				}
			})
		}
	}
}

func TestLastSigner_AMemberOfAStrandedRoleIsRefused(t *testing.T) {
	super, app := dbTestPools(t)
	w := lsWorld(t, super, app)
	p := w.addMember(t, "preparer", "active", "Pia Preparer")
	i := w.addMember(t, "admin", "invited", "Ida Invited")
	roleP := seedRoleStaffedBy(t, super, w.tenant, "role-p", p)
	roleI := seedRoleStaffedBy(t, super, w.tenant, "role-i", i)
	seedWaitingRun(t, super, w.tenant, "role-p")
	seedWaitingRun(t, super, w.tenant, "role-i")
	before := w.lsState(t, p, i)

	refused := []struct {
		name string
		do   func() error
	}{
		{"remove P", func() error { return w.lsRemove(w.admin, p) }},
		{"suspend P", func() error { return w.lsSetStatus(w.admin, p, "suspended") }},
		{"remove I", func() error { return w.lsRemove(w.admin, i) }},
	}
	for _, c := range refused {
		wantLastSigner(t, c.do())
	}
	if after := w.lsState(t, p, i); after != before {
		t.Fatalf("refusals wrote:\nbefore %s\nafter  %s", before, after)
	}

	lsStaff(t, super, w.tenant, roleP, w.addMember(t, "reviewer", "active", "Sam Eze"), 1)
	lsStaff(t, super, w.tenant, roleI, w.addMember(t, "reviewer", "active", "Sue Eze"), 1)
	for _, c := range []struct {
		name string
		do   func() error
	}{
		{"suspend P", func() error { return w.lsSetStatus(w.admin, p, "suspended") }},
		{"remove P", func() error { return w.lsRemove(w.admin, p) }},
		{"remove I", func() error { return w.lsRemove(w.admin, i) }},
	} {
		if err := c.do(); err != nil {
			t.Errorf("%s once the role has a signer: %v, want nil", c.name, err)
		}
	}
}

func TestLastSigner_ListsOnlyTheRolesLeftWithNoSigner(t *testing.T) {
	super, app := dbTestPools(t)
	w := lsWorld(t, super, app)
	r := w.addMember(t, "reviewer", "active", "Rita Eze")
	s := w.addMember(t, "reviewer", "active", "Sam Eze")
	seedRoleStaffedBy(t, super, w.tenant, "role-x", r, s)
	seedRoleStaffedBy(t, super, w.tenant, "role-y", r)
	seedWaitingRun(t, super, w.tenant, "role-x")
	seedWaitingRun(t, super, w.tenant, "role-y")
	h := lsMux(w.store, nil)
	remove := func() lsBody {
		t.Helper()
		return wantRefusalBody(t, w.lsHTTP(h, w.admin, "POST", "/v1/memberships/"+r+"/remove", ""))
	}
	keys := func(b lsBody) []string {
		var ks []string
		for _, it := range b.Items {
			ks = append(ks, it["workflow_role_key"])
		}
		return ks
	}

	if got := keys(remove()); !slices.Equal(got, []string{"role-y"}) {
		t.Errorf("roles named = %v, want [role-y] (role-x keeps S)", got)
	}

	if _, err := super.Exec(context.Background(), `DELETE FROM workflow_role_members WHERE user_id = $1`, s); err != nil {
		t.Fatalf("unstaff S: %v", err)
	}
	b := remove()
	if got := keys(b); !slices.Equal(got, []string{"role-x", "role-y"}) || b.Total != 2 {
		t.Errorf("roles named = %v total %d, want [role-x role-y] total 2", got, b.Total)
	}
}

func TestLastSigner_HandlerBodyCapsAtTwenty(t *testing.T) {
	super, app := dbTestPools(t)
	w := lsWorld(t, super, app)
	r := w.addMember(t, "reviewer", "active", "Rita Eze")
	seedRoleStaffedBy(t, super, w.tenant, approver, r)
	for range 25 {
		seedWaitingRun(t, super, w.tenant, approver)
	}

	rec := w.lsHTTP(lsMux(w.store, nil), w.admin, "PATCH", "/v1/memberships/"+r, `{"status":"suspended"}`)

	b := wantRefusalBody(t, rec)
	if len(b.Items) != 20 || b.Total != 25 {
		t.Errorf("items = %d, total = %d, want 20 and 25", len(b.Items), b.Total)
	}
}

func lsWaitForAdvisoryWaiter(t *testing.T, super *pgxpool.Pool, done <-chan error) {
	t.Helper()
	for range 500 {
		select {
		case err := <-done:
			t.Fatalf("the writer returned (%v) without waiting on the tenant lock", err)
		default:
		}
		var n int
		if err := super.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity
			  WHERE wait_event_type = 'Lock' AND wait_event = 'advisory' AND datname = current_database()`).Scan(&n); err != nil {
			t.Fatalf("read waiters: %v", err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no session is waiting on the tenant advisory lock")
}

func TestLastSigner_WritersTakeTheTenantLockAfterTheAdminCheck(t *testing.T) {
	super, app := dbTestPools(t)
	writers := []struct {
		name       string
		targetRole string
		refusal    error
		do         func(w invWorld, caller, target string) error
	}{
		{"remove", "preparer", ErrRemoveNotPermitted, func(w invWorld, c, u string) error { return w.lsRemove(c, u) }},
		{"suspend", "preparer", ErrNotPermitted, func(w invWorld, c, u string) error { return w.lsSetStatus(c, u, "suspended") }},
		{"demote", "reviewer", ErrRoleChangeNotPermitted, func(w invWorld, c, u string) error { return w.lsSetRole(c, u, "preparer") }},
	}
	for _, wr := range writers {
		t.Run(wr.name, func(t *testing.T) {
			ctx := context.Background()
			w := lsWorld(t, super, app)
			target := w.addMember(t, wr.targetRole, "active", "Tara Target")
			caller := w.addMember(t, "preparer", "active", "Pia Preparer")
			hold, err := super.Begin(ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer hold.Rollback(ctx) //nolint:errcheck
			if err := approval.LockTenantPublishTx(ctx, hold, w.tenant); err != nil {
				t.Fatalf("hold the tenant lock: %v", err)
			}

			refused := make(chan error, 1)
			go func() { refused <- wr.do(w, caller, target) }()
			select {
			case err := <-refused:
				if !errors.Is(err, wr.refusal) {
					t.Errorf("preparer caller: %v, want %v", err, wr.refusal)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("a non-admin waited on the tenant lock, want its 403 first")
			}

			done := make(chan error, 1)
			go func() { done <- wr.do(w, w.admin, target) }()
			lsWaitForAdvisoryWaiter(t, super, done)
			row, err := super.Begin(ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer row.Rollback(ctx) //nolint:errcheck
			if _, err := row.Exec(ctx,
				`SELECT 1 FROM memberships WHERE tenant_id = $1 AND user_id = $2 FOR UPDATE NOWAIT`, w.tenant, target); err != nil {
				t.Errorf("the waiting writer already holds the target row: %v, want the tenant lock first", err)
			}
			row.Rollback(ctx) //nolint:errcheck

			if err := hold.Commit(ctx); err != nil {
				t.Fatalf("release the tenant lock: %v", err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("admin %s after the lock was released: %v, want nil", wr.name, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the admin call did not return after the lock was released")
			}
		})
	}
}

func TestLastSigner_EarlierRefusalsComeFirst(t *testing.T) {
	super, app := dbTestPools(t)
	w := lsWorld(t, super, app)
	suspended := w.addMember(t, "reviewer", "suspended", "Sue Suspended")
	invited := w.addMember(t, "reviewer", "invited", "Ida Invited")
	removed := w.removedRow(t, "Rex Removed")
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")
	seedRoleStaffedBy(t, super, w.tenant, approver, w.admin, suspended)
	seedRoleStaffedBy(t, super, w.tenant, "role-i", invited)
	seedRoleStaffedBy(t, super, w.tenant, "role-p", prep)
	for _, k := range []string{approver, "role-i", "role-p"} {
		seedWaitingRun(t, super, w.tenant, k)
	}
	unknown := uuid.NewString()

	for name, err := range map[string]error{
		"unknown/remove":  w.lsRemove(w.admin, unknown),
		"unknown/suspend": w.lsSetStatus(w.admin, unknown, "suspended"),
		"unknown/demote":  w.lsSetRole(w.admin, unknown, "preparer"),
	} {
		if !errors.Is(err, ErrMembershipNotFound) {
			t.Errorf("%s: %v, want ErrMembershipNotFound", name, err)
		}
	}
	for name, err := range map[string]error{
		"invited/suspend": w.lsSetStatus(w.admin, invited, "suspended"),
		"invited/demote":  w.lsSetRole(w.admin, invited, "preparer"),
	} {
		if !errors.Is(err, ErrInvitedNotTransitionable) {
			t.Errorf("%s: %v, want ErrInvitedNotTransitionable", name, err)
		}
	}
	for name, err := range map[string]error{
		"removed/suspend": w.lsSetStatus(w.admin, removed, "suspended"),
		"removed/demote":  w.lsSetRole(w.admin, removed, "reviewer"),
	} {
		if !errors.Is(err, ErrRemovedNotTransitionable) {
			t.Errorf("%s: %v, want ErrRemovedNotTransitionable", name, err)
		}
	}
	for _, c := range lsThreeChanges {
		if err := c.do(w, w.admin, w.admin); !errors.Is(err, ErrLastActiveAdmin) {
			t.Errorf("%s of the sole admin who is the only signer: %v, want ErrLastActiveAdmin", c.name, err)
		}
	}

	before := w.auditTotal(t)
	for name, err := range map[string]error{
		"suspended/suspend again": w.lsSetStatus(w.admin, suspended, "suspended"),
		"removed/remove again":    w.lsRemove(w.admin, removed),
		"preparer/demote again":   w.lsSetRole(w.admin, prep, "preparer"),
	} {
		if err != nil {
			t.Errorf("%s: %v, want a 200 no-op", name, err)
		}
	}
	if after := w.auditTotal(t); after != before {
		t.Errorf("no-op changes wrote %d audit rows, want 0", after-before)
	}
}

func TestLastSigner_ConcurrentChangesToTheLastTwoSignersLeaveOne(t *testing.T) {
	super, app := dbTestPools(t)
	rows := []struct {
		name string
		a, b func(w invWorld, a, b string) error
	}{
		{"suspend A and remove B",
			func(w invWorld, a, _ string) error { return w.lsSetStatus(w.admin, a, "suspended") },
			func(w invWorld, _, b string) error { return w.lsRemove(w.admin, b) }},
		{"demote A and suspend B",
			func(w invWorld, a, _ string) error { return w.lsSetRole(w.admin, a, "preparer") },
			func(w invWorld, _, b string) error { return w.lsSetStatus(w.admin, b, "suspended") }},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			for round := range 20 {
				w := lsWorld(t, super, app)
				a := w.addMember(t, "reviewer", "active", "Abe Eze")
				b := w.addMember(t, "reviewer", "active", "Bea Eze")
				seedRoleStaffedBy(t, super, w.tenant, approver, a, b)
				seedWaitingRun(t, super, w.tenant, approver)

				errA, errB := lsRace(func() error { return row.a(w, a, b) }, func() error { return row.b(w, a, b) })

				nils, refused := 0, 0
				for _, err := range []error{errA, errB} {
					switch {
					case err == nil:
						nils++
					case errors.Is(err, approval.ErrRoleInUse):
						refused++
					default:
						t.Fatalf("round %d: unexpected error %v", round, err)
					}
				}
				if nils != 1 || refused != 1 {
					t.Fatalf("round %d: results %v and %v, want one nil and one ErrRoleInUse", round, errA, errB)
				}
				if n := w.signerCount(t, approver); n != 1 {
					t.Fatalf("round %d: signers left = %d, want 1", round, n)
				}
			}
		})
	}
}

// The sequential step is deterministic; the race only varies which side runs first.
func TestLastSigner_RacingAStaffingPutLeavesASigner(t *testing.T) {
	super, app := dbTestPools(t)
	roles := approval.NewStore(app, nil, nil)

	w := lsWorld(t, super, app)
	a := w.addMember(t, "reviewer", "active", "Abe Eze")
	b := w.addMember(t, "reviewer", "active", "Bea Eze")
	seedRoleStaffedBy(t, super, w.tenant, approver, a, b)
	seedWaitingRun(t, super, w.tenant, approver)
	if _, err := roles.SetRoleMembers(w.adminCtx(), approver, []string{a}); err != nil {
		t.Fatalf("narrow the role to A: %v", err)
	}
	wantLastSigner(t, w.lsSetStatus(w.admin, a, "suspended"))

	for round := range 20 {
		w := lsWorld(t, super, app)
		a := w.addMember(t, "reviewer", "active", "Abe Eze")
		b := w.addMember(t, "reviewer", "active", "Bea Eze")
		seedRoleStaffedBy(t, super, w.tenant, approver, a, b)
		seedWaitingRun(t, super, w.tenant, approver)

		errA, errB := lsRace(
			func() error { return w.lsSetStatus(w.admin, a, "suspended") },
			func() error { _, err := roles.SetRoleMembers(w.adminCtx(), approver, []string{a}); return err },
		)

		if (errA == nil) == (errB == nil) {
			t.Fatalf("round %d: suspend = %v, put = %v, want exactly one success", round, errA, errB)
		}
		if loser := errors.Join(errA, errB); !errors.Is(loser, approval.ErrRoleInUse) {
			t.Fatalf("round %d: loser = %v, want ErrRoleInUse", round, loser)
		}
		if n := w.signerCount(t, approver); n != 1 {
			t.Fatalf("round %d: signers left = %d, want 1", round, n)
		}
	}
}
