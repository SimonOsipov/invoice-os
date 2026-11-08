package approval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// cancelOnSQL fails the one statement whose text contains match, by cancelling its context.
type cancelOnSQL struct{ match string }

func (c cancelOnSQL) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if !strings.Contains(d.SQL, c.match) {
		return ctx
	}
	dead, cancel := context.WithCancel(ctx)
	cancel()
	return dead
}

func (cancelOnSQL) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// failingAppPool is tracedAppPool with a tracer that fails the statements containing match.
func failingAppPool(t *testing.T, match string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	cfg.ConnConfig.Tracer = cancelOnSQL{match: match}
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect failing app pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

const signMarker = "-- unsignableReasonsTx"

// signWorld is a firm with clients A and B, role "sign-k", and a validated invoice on A
// with an open run pending on the role. users lists every approver candidate seeded.
type signWorld struct {
	t          *testing.T
	super, app *pgxpool.Pool
	f          approveFixture
	entB       string
	key        string
	users      []string
	allow      bool
	delegate   *string
}

func newSignWorld(t *testing.T, super, app *pgxpool.Pool, name string) *signWorld {
	t.Helper()
	const key = "sign-k"
	f := newApproveFixture(t, super, app, name, key)
	return &signWorld{t: t, super: super, app: app, f: f, key: key,
		entB: seedBusinessEntity(t, super, f.tenantID, name+" B")}
}

func (w *signWorld) add(s memberSpec) string {
	w.t.Helper()
	u := seedMemberOf(w.t, w.super, w.f.tenantID, s)
	w.users = append(w.users, u)
	return u
}

func (w *signWorld) inRole(s memberSpec) string {
	w.t.Helper()
	s.roleID = w.f.roleID
	return w.add(s)
}

func (w *signWorld) createdBy(actor string) {
	w.t.Helper()
	seedHistory(w.t, w.super, w.f.tenantID, w.f.invoiceID, nil, actor)
}

// delegation sets the pending step's delegation: allow with a nil user is "anyone".
func (w *signWorld) delegation(allow bool, user *string) {
	w.t.Helper()
	setStepDelegation(w.t, w.super, w.f.stepID, allow, user)
	w.allow, w.delegate = allow, user
}

func (w *signWorld) probe() SignerProbe {
	return SignerProbe{InvoiceID: w.f.invoiceID, RoleKey: ptr(w.key), AllowDelegation: w.allow, DelegateUserID: w.delegate}
}

func (w *signWorld) limitedTo(ents ...string) memberSpec {
	return memberSpec{role: "reviewer", limited: true, entityIDs: ents}
}

// readReasons runs unsignableReasonsTx as the app role in a tenant tx that sets no client scope.
func readReasons(t *testing.T, pool *pgxpool.Pool, tenantID string, probes []SignerProbe) []string {
	t.Helper()
	var got []string
	err := db.WithinTenantTx(context.Background(), pool, tenantID, func(tx pgx.Tx) error {
		var err error
		got, err = unsignableReasonsTx(context.Background(), tx, probes)
		return err
	})
	if err != nil {
		t.Fatalf("unsignableReasonsTx: %v", err)
	}
	return got
}

func wantReasons(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reasons = %q, want %q", got, want)
	}
}

var errRolledBack = errors.New("rolled back on purpose")

// approves reports whether u's approve succeeds through decideTx, rolled back.
func (w *signWorld) approves(u string) bool {
	w.t.Helper()
	caller := auth.Identity{Subject: u, Role: "authenticated", TenantID: w.f.tenantID}
	ctx := auth.WithIdentity(context.Background(), caller)
	err := db.WithinRequestTenantTx(ctx, w.app, func(tx pgx.Tx) error {
		if _, err := decideTx(ctx, tx, w.f.invoiceID, "approved", nil, caller, nil); err != nil {
			return err
		}
		return errRolledBack
	})
	switch {
	case errors.Is(err, errRolledBack):
		return true
	case errors.Is(err, db.ErrNotActiveMember), errors.Is(err, ErrNotPermitted), errors.Is(err, ErrRunNotFound),
		errors.Is(err, ErrNotRoleHolder), errors.Is(err, ErrCreatorRefused):
		return false
	default:
		w.t.Fatalf("approve as %s: unexpected error %v", u, err)
		return false
	}
}

// approvers returns the seeded candidates whose approve succeeds through decideTx, each rolled back.
func (w *signWorld) approvers() []string {
	w.t.Helper()
	var out []string
	for _, u := range w.users {
		if w.approves(u) {
			out = append(out, u)
		}
	}
	return out
}

// signRow seeds one world and names the reason it must read.
type signRow struct {
	name string
	want string
	seed func(w *signWorld)
}

var signRows = []signRow{
	{"active reviewer", "", func(w *signWorld) { w.inRole(memberSpec{role: "reviewer"}) }},
	{"active admin", "", func(w *signWorld) { w.inRole(memberSpec{role: "admin"}) }},
	{"suspended reviewer", "no holder", func(w *signWorld) { w.inRole(memberSpec{role: "reviewer", status: "suspended"}) }},
	{"preparer", "no holder", func(w *signWorld) { w.inRole(memberSpec{role: "preparer"}) }},
	{"invited admin", "no holder", func(w *signWorld) { w.inRole(memberSpec{role: "admin", status: "invited"}) }},
	{"no members", "no holder", func(w *signWorld) {}},
	{"reviewer limited to B", "out of client reach", func(w *signWorld) { w.inRole(w.limitedTo(w.entB)) }},
	{"reviewer limited to A", "", func(w *signWorld) { w.inRole(w.limitedTo(w.f.entityID)) }},
	{"deleted role with an active reviewer", "deleted", func(w *signWorld) {
		w.inRole(memberSpec{role: "reviewer"})
		softDeleteWorkflowRole(w.t, w.super, w.f.roleID)
	}},
	{"limited-to-B reviewer beside a suspended all-clients one", "out of client reach", func(w *signWorld) {
		w.inRole(w.limitedTo(w.entB))
		w.inRole(memberSpec{role: "reviewer", status: "suspended"})
	}},
	{"admin with client rows for B only", "", func(w *signWorld) {
		w.inRole(memberSpec{role: "admin", limited: true, entityIDs: []string{w.entB}})
	}},
}

var signCreatorRows = []signRow{
	{"creator in role, another reviewer outside the role reaches A", "creator only", func(w *signWorld) {
		c := w.inRole(memberSpec{role: "reviewer"})
		w.createdBy(c)
		w.add(memberSpec{role: "reviewer"})
	}},
	{"creator alone among the approvers", "", func(w *signWorld) {
		w.createdBy(w.inRole(memberSpec{role: "reviewer"}))
	}},
	{"other approver limited to B", "", func(w *signWorld) {
		w.createdBy(w.inRole(memberSpec{role: "reviewer"}))
		w.add(w.limitedTo(w.entB))
	}},
	{"other approver suspended", "", func(w *signWorld) {
		w.createdBy(w.inRole(memberSpec{role: "reviewer"}))
		w.add(memberSpec{role: "reviewer", status: "suspended"})
	}},
	{"other member a preparer", "", func(w *signWorld) {
		w.createdBy(w.inRole(memberSpec{role: "reviewer"}))
		w.add(memberSpec{role: "preparer"})
	}},
	{"creator with a colleague in the role", "", func(w *signWorld) {
		w.createdBy(w.inRole(memberSpec{role: "reviewer"}))
		w.inRole(memberSpec{role: "reviewer"})
	}},
	{"creator plus a role member limited to B, an outsider reaches A", "creator only", func(w *signWorld) {
		w.createdBy(w.inRole(memberSpec{role: "reviewer"}))
		w.inRole(w.limitedTo(w.entB))
		w.add(memberSpec{role: "reviewer"})
	}},
	{"creator limited to B alone in the role, an outsider reaches A", "out of client reach", func(w *signWorld) {
		c := w.inRole(w.limitedTo(w.entB))
		w.createdBy(c)
		w.add(memberSpec{role: "reviewer"})
	}},
	{"system creation actor", "", func(w *signWorld) {
		w.inRole(memberSpec{role: "reviewer"})
		w.createdBy("system")
	}},
	{"system creation actor, an outsider reaches A", "", func(w *signWorld) {
		w.inRole(memberSpec{role: "reviewer"})
		w.createdBy("system")
		w.add(memberSpec{role: "reviewer"})
	}},
}

// signDelegateRows are the delegate rows D1-D13 of the agreement table. "anyone" is allow with no named user.
var signDelegateRows = []signRow{
	{"D1 role empty, named reviewer outside the role", "", func(w *signWorld) {
		d := w.add(memberSpec{role: "reviewer"})
		w.delegation(true, &d)
	}},
	{"D2 role empty, anyone, an admin", "", func(w *signWorld) {
		w.add(memberSpec{role: "admin"})
		w.delegation(true, nil)
	}},
	{"D3 role empty, named delegate suspended, an admin", "no holder", func(w *signWorld) {
		d := w.add(memberSpec{role: "reviewer", status: "suspended"})
		w.add(memberSpec{role: "admin"})
		w.delegation(true, &d)
	}},
	{"D4 role empty, named delegate a preparer", "no holder", func(w *signWorld) {
		d := w.add(memberSpec{role: "preparer"})
		w.delegation(true, &d)
	}},
	{"D5 delegation off, role empty, an admin", "no holder", func(w *signWorld) {
		w.add(memberSpec{role: "admin"})
		w.delegation(false, nil)
	}},
	{"D6 named delegate is the creator, another reviewer reaches A", "creator only", func(w *signWorld) {
		d := w.add(memberSpec{role: "reviewer"})
		w.createdBy(d)
		w.add(memberSpec{role: "reviewer"})
		w.delegation(true, &d)
	}},
	{"D7 named delegate is the creator and the only approver", "", func(w *signWorld) {
		d := w.add(memberSpec{role: "reviewer"})
		w.createdBy(d)
		w.delegation(true, &d)
	}},
	{"D8 named delegate limited to B", "out of client reach", func(w *signWorld) {
		d := w.add(w.limitedTo(w.entB))
		w.delegation(true, &d)
	}},
	{"D9 anyone, every approver limited to B, no admin", "out of client reach", func(w *signWorld) {
		w.add(w.limitedTo(w.entB))
		w.add(w.limitedTo(w.entB))
		w.delegation(true, nil)
	}},
	{"D10 role deleted, anyone, an admin", "", func(w *signWorld) {
		w.add(memberSpec{role: "admin"})
		softDeleteWorkflowRole(w.t, w.super, w.f.roleID)
		w.delegation(true, nil)
	}},
	{"D11 role deleted, named delegate suspended", "deleted", func(w *signWorld) {
		d := w.add(memberSpec{role: "reviewer", status: "suspended"})
		softDeleteWorkflowRole(w.t, w.super, w.f.roleID)
		w.delegation(true, &d)
	}},
	{"D12 role member limited to B, named delegate reaches A", "", func(w *signWorld) {
		w.inRole(w.limitedTo(w.entB))
		d := w.add(memberSpec{role: "reviewer"})
		w.delegation(true, &d)
	}},
	{"D13 role member is the creator, anyone, another reviewer reaches A", "", func(w *signWorld) {
		c := w.inRole(memberSpec{role: "reviewer"})
		w.createdBy(c)
		w.add(memberSpec{role: "reviewer"})
		w.delegation(true, nil)
	}},
}

// TestCanSign_AgreesWithTheApprovePath: each row's literal reason holds, and the reason is ""
// exactly when some seeded candidate's decideTx approve succeeds.
func TestCanSign_AgreesWithTheApprovePath(t *testing.T) {
	super, app := dbTestPools(t)
	for _, set := range [][]signRow{signRows, signCreatorRows, signDelegateRows} {
		for _, r := range set {
			t.Run(r.name, func(t *testing.T) {
				w := newSignWorld(t, super, app, "APSC-14 agree "+r.name)
				r.seed(w)

				wantReasons(t, readReasons(t, app, w.f.tenantID, []SignerProbe{w.probe()}), r.want)

				got := w.approvers()
				if r.want == "" && len(got) == 0 {
					t.Errorf("reason is empty but no candidate of %d can approve through decideTx", len(w.users))
				}
				if r.want != "" && len(got) != 0 {
					t.Errorf("reason is %q but %d candidates approve through decideTx", r.want, len(got))
				}
			})
		}
	}
}

func TestCanSign_NullKeyReadsDeleted(t *testing.T) {
	super, app := dbTestPools(t)
	w := newSignWorld(t, super, app, "APSC-14 null key")
	w.inRole(memberSpec{role: "reviewer"})

	probes := []SignerProbe{
		{InvoiceID: w.f.invoiceID, RoleKey: nil},
		{InvoiceID: w.f.invoiceID, RoleKey: ptr("no-such-role")},
	}
	wantReasons(t, readReasons(t, app, w.f.tenantID, probes), "deleted", "deleted")
}

func TestCanSign_NullKeyWithAnEligibleDelegateIsSignable(t *testing.T) {
	super, app := dbTestPools(t)
	w := newSignWorld(t, super, app, "APSC-14 null key delegate")
	admin := w.add(memberSpec{role: "admin"})
	w.delegation(true, nil)

	on := SignerProbe{InvoiceID: w.f.invoiceID, RoleKey: nil, AllowDelegation: true}
	wantReasons(t, readReasons(t, app, w.f.tenantID, []SignerProbe{on}), "")
	if !w.approves(admin) {
		t.Error("the admin's approve through decideTx fails, but the probe reads signable")
	}

	off := SignerProbe{InvoiceID: w.f.invoiceID, RoleKey: nil}
	wantReasons(t, readReasons(t, app, w.f.tenantID, []SignerProbe{off}), "deleted")
}

func TestCanSign_InHouseIgnoresClientAccess(t *testing.T) {
	super, app := dbTestPools(t)
	w := newSignWorld(t, super, app, "APSC-14 in-house")
	w.inRole(memberSpec{role: "reviewer", limited: true}) // all_clients = false, no client rows
	setKind := func(kind string) {
		if _, err := super.Exec(context.Background(), `UPDATE tenants SET kind = $2 WHERE id = $1`, w.f.tenantID, kind); err != nil {
			t.Fatalf("set tenant kind %s: %v", kind, err)
		}
	}

	setKind("in_house")
	wantReasons(t, readReasons(t, app, w.f.tenantID, []SignerProbe{w.probe()}), "")
	if got := w.approvers(); len(got) != 1 {
		t.Errorf("in-house approvers through decideTx = %d, want 1", len(got))
	}

	setKind("firm")
	wantReasons(t, readReasons(t, app, w.f.tenantID, []SignerProbe{w.probe()}), "out of client reach")
	if got := w.approvers(); len(got) != 0 {
		t.Errorf("firm approvers through decideTx = %d, want 0", len(got))
	}
}

func TestCanSign_KeepsProbeOrderAcrossThreeInvoicesAndTwoClients(t *testing.T) {
	super, app := dbTestPools(t)
	w := newSignWorld(t, super, app, "APSC-14 order")
	w.inRole(w.limitedTo(w.f.entityID))
	i1 := w.f.invoiceID
	i2 := seedInvoice(t, super, w.f.tenantID, w.entB, "APSC-14 order I2")
	i3 := seedInvoice(t, super, w.f.tenantID, w.f.entityID, "APSC-14 order I3")
	probe := func(id string) SignerProbe { return SignerProbe{InvoiceID: id, RoleKey: ptr(w.key)} }

	// The out-of-reach invoice I2 sits at every position, so no fixed output order matches all three.
	for _, c := range []struct {
		ids  []string
		want []string
	}{
		{[]string{i2, i1, i3}, []string{"out of client reach", "", ""}},
		{[]string{i1, i3, i2}, []string{"", "", "out of client reach"}},
		{[]string{i3, i2, i1}, []string{"", "out of client reach", ""}},
	} {
		probes := make([]SignerProbe, len(c.ids))
		for i, id := range c.ids {
			probes[i] = probe(id)
		}
		wantReasons(t, readReasons(t, app, w.f.tenantID, probes), c.want...)
	}

	// Past a handful of probes the planner may hash-aggregate and drop the input order on its own.
	const n = 30
	long := make([]SignerProbe, n)
	wantLong := make([]string, n)
	for i := range long {
		id := []string{i2, i3, i1}[i%3]
		long[i] = probe(id)
		if id == i2 {
			wantLong[i] = "out of client reach"
		}
	}
	wantReasons(t, readReasons(t, app, w.f.tenantID, long), wantLong...)
}

func TestCanSign_OneStatementWhateverTheProbeCount(t *testing.T) {
	super, app := dbTestPools(t)
	tapp, rec := tracedAppPool(t)
	w := newSignWorld(t, super, app, "APSC-14 one statement")
	tenant := w.f.tenantID

	deleg := seedMemberOf(t, super, tenant, memberSpec{role: "reviewer"}) // in no role: a delegate only
	roles := []struct {
		key, want string
		seed      func(roleID string)
		delegate  *string
	}{
		{"cs-staffed", "", func(id string) { seedMemberOf(t, super, tenant, memberSpec{role: "reviewer", roleID: id}) }, nil},
		{"cs-empty", "no holder", func(string) {}, nil},
		{"cs-away", "out of client reach", func(id string) {
			seedMemberOf(t, super, tenant, memberSpec{role: "reviewer", limited: true, entityIDs: []string{w.entB}, roleID: id})
		}, nil},
		{"cs-deleted", "deleted", func(id string) {
			seedMemberOf(t, super, tenant, memberSpec{role: "reviewer", roleID: id})
			softDeleteWorkflowRole(t, super, id)
		}, nil},
		{"cs-deleg", "", func(string) {}, &deleg},
	}
	for _, r := range roles {
		r.seed(seedWorkflowRole(t, super, tenant, r.key, r.key))
	}

	const n = 30
	probes := make([]SignerProbe, n)
	want := make([]string, n)
	for i := range probes {
		r := roles[i%len(roles)]
		probes[i] = SignerProbe{InvoiceID: seedInvoice(t, super, tenant, w.f.entityID, fmt.Sprintf("APSC-14 one-%d", i)), RoleKey: ptr(r.key)}
		switch {
		case r.delegate != nil:
			probes[i].AllowDelegation, probes[i].DelegateUserID = true, r.delegate
		case i%2 == 1: // a named stranger changes no answer, so on and off alternate with the same reasons
			probes[i].AllowDelegation, probes[i].DelegateUserID = true, ptr(uuid.NewString())
		}
		want[i] = r.want
	}

	rec.reset()
	got := readReasons(t, tapp, tenant, probes)
	if m := rec.mentioning(signMarker); len(m) != 1 {
		t.Errorf("statements marked %q for %d probes = %d, want 1", signMarker, n, len(m))
	}
	if m := rec.mentioning("invoice_status_history"); len(m) != 1 {
		t.Errorf("statements reading invoice_status_history for %d probes = %d, want 1", n, len(m))
	}
	wantReasons(t, got, want...)

	rec.reset()
	empty := readReasons(t, tapp, tenant, nil)
	if m := rec.mentioning(signMarker); len(m) != 0 {
		t.Errorf("nil probes ran %d marked statements, want 0", len(m))
	}
	if !reflect.DeepEqual(empty, []string{}) {
		t.Errorf("nil probes returned %#v, want []string{}", empty)
	}
}

func TestCanSign_SameAnswerUnderALimitedCaller(t *testing.T) {
	super, app := dbTestPools(t)
	rows := append([]signRow{}, signRows...)
	// The two readers are approvers too, so a lone creator now has another approver reaching A.
	rows = append(rows, signRow{"creator alone in the role, readers reach A", "creator only", func(w *signWorld) {
		w.createdBy(w.inRole(memberSpec{role: "reviewer"}))
	}})
	// The readers add an admin and a reviewer reaching A: D7's lone creator now has company, D9 has a reach.
	readers := map[string]string{
		"D7 named delegate is the creator and the only approver": "creator only",
		"D9 anyone, every approver limited to B, no admin":       "",
	}
	for _, r := range signDelegateRows {
		if want, ok := readers[r.name]; ok {
			r.want = want
		}
		rows = append(rows, r)
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			w := newSignWorld(t, super, app, "APSC-14 caller "+r.name)
			r.seed(w)
			admin := w.add(memberSpec{role: "admin"})
			limited := w.add(w.limitedTo(w.f.entityID))

			read := func(subject string) []string {
				ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: subject, Role: "authenticated", TenantID: w.f.tenantID})
				var got []string
				err := db.WithinRequestTenantTx(ctx, app, func(tx pgx.Tx) error {
					var err error
					got, err = unsignableReasonsTx(ctx, tx, []SignerProbe{w.probe()})
					return err
				})
				if err != nil {
					t.Fatalf("unsignableReasonsTx as %s: %v", subject, err)
				}
				return got
			}
			wantReasons(t, read(admin), r.want)
			wantReasons(t, read(limited), r.want)
			if signs := len(w.approvers()) > 0; signs != (r.want == "") {
				t.Errorf("reason %q but decideTx approve succeeds for some candidate = %t", r.want, signs)
			}
		})
	}
}

func TestCanSign_AnotherTenantNeverCounts(t *testing.T) {
	super, app := dbTestPools(t)

	t1 := newSignWorld(t, super, app, "APSC-14 tenant 1")
	t1.add(memberSpec{role: "admin"}) // an approver reaching everything, not in the role

	t2 := newSignWorld(t, super, app, "APSC-14 tenant 2") // same role key as tenant 1
	t2.inRole(memberSpec{role: "reviewer"})
	t2.add(memberSpec{role: "admin"})

	t3 := newSignWorld(t, super, app, "APSC-14 tenant 3")
	t3.createdBy(t3.inRole(memberSpec{role: "reviewer"})) // the only approver of its workspace

	wantReasons(t, readReasons(t, app, t1.f.tenantID, []SignerProbe{t1.probe()}), "no holder")
	wantReasons(t, readReasons(t, app, t2.f.tenantID, []SignerProbe{t2.probe()}), "")
	wantReasons(t, readReasons(t, app, t3.f.tenantID, []SignerProbe{t3.probe()}), "")
	if got := t3.approvers(); len(got) != 1 {
		t.Errorf("tenant 3 approvers through decideTx = %d, want 1", len(got))
	}
}

// readReasonsAsSuper runs unsignableReasonsTx without RLS, so only the statement's own tenant pins keep tenants apart.
func readReasonsAsSuper(t *testing.T, super *pgxpool.Pool, probes []SignerProbe) []string {
	t.Helper()
	tx, err := super.BeginTx(context.Background(), pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	got, err := unsignableReasonsTx(context.Background(), tx, probes)
	if err != nil {
		t.Fatalf("unsignableReasonsTx as superuser: %v", err)
	}
	return got
}

func TestCanSign_AnotherTenantNeverCountsForAnAnyoneDelegate(t *testing.T) {
	super, app := dbTestPools(t)

	t1 := newSignWorld(t, super, app, "APSC-14 anyone tenant 1")
	t1.add(memberSpec{role: "reviewer", status: "suspended"}) // its only approver
	t1.delegation(true, nil)

	t2 := newSignWorld(t, super, app, "APSC-14 anyone tenant 2")
	t2.add(memberSpec{role: "admin"})
	t2.delegation(true, nil)

	// Positive control: an "anyone" step with its own active admin reads signable on the same pool.
	t3 := newSignWorld(t, super, app, "APSC-14 anyone tenant 3")
	t3.add(memberSpec{role: "admin"})
	t3.delegation(true, nil)

	wantReasons(t, readReasonsAsSuper(t, super, []SignerProbe{t3.probe()}), "")
	wantReasons(t, readReasonsAsSuper(t, super, []SignerProbe{t1.probe()}), "no holder")
}

func TestCanSign_ReadErrorIsReturned(t *testing.T) {
	super, app := dbTestPools(t)
	failing := failingAppPool(t, signMarker)
	w := newSignWorld(t, super, app, "APSC-14 read error")
	w.inRole(memberSpec{role: "reviewer"})

	var got []string
	var inner error
	_ = db.WithinTenantTx(context.Background(), failing, w.f.tenantID, func(tx pgx.Tx) error {
		got, inner = unsignableReasonsTx(context.Background(), tx, []SignerProbe{w.probe()})
		return inner
	})
	if inner == nil {
		t.Error("a failed signer read returned a nil error")
	}
	if got != nil {
		t.Errorf("a failed signer read returned reasons %q, want nil", got)
	}
}

func TestHolderReason_MapsEachLadderReason(t *testing.T) {
	for reason, want := range map[string]string{
		"": "", "deleted": HolderReasonUnstaffed, "no holder": HolderReasonUnstaffed,
		"out of client reach": HolderReasonNobodyCanApprove, "creator only": HolderReasonNobodyCanApprove,
	} {
		got := holderReason(reason)
		switch {
		case want == "" && got != nil:
			t.Errorf("holderReason(%q) = %q, want nil", reason, *got)
		case want != "" && (got == nil || *got != want):
			t.Errorf("holderReason(%q) = %v, want %q", reason, got, want)
		}
	}
}

// TestCanSign_SubjectSignsMatchesTheApprovePath: subject_signs is true exactly when that user's decideTx approve succeeds.
func TestCanSign_SubjectSignsMatchesTheApprovePath(t *testing.T) {
	super, app := dbTestPools(t)
	tapp, rec := tracedAppPool(t)
	cases := []struct {
		prefix  string
		signers int
	}{{"D1 ", 1}, {"D6 ", 0}, {"D7 ", 1}, {"D8 ", 0}, {"D12 ", 1}}
	for _, c := range cases {
		var row signRow
		for _, r := range signDelegateRows {
			if strings.HasPrefix(r.name, c.prefix) {
				row = r
			}
		}
		if row.seed == nil {
			t.Fatalf("no delegate row starts with %q", c.prefix)
		}
		t.Run(row.name, func(t *testing.T) {
			w := newSignWorld(t, super, app, "APSC-14 subject "+row.name)
			row.seed(w)

			signing := 0
			for _, u := range w.users {
				rec.reset()
				var signs []bool
				err := db.WithinTenantTx(context.Background(), tapp, w.f.tenantID, func(tx pgx.Tx) error {
					var err error
					_, signs, err = signerStandingTx(context.Background(), tx, []SignerProbe{w.probe()}, u)
					return err
				})
				if err != nil {
					t.Fatalf("signerStandingTx as subject %s: %v", u, err)
				}
				if m := rec.mentioning(signMarker); len(m) != 1 {
					t.Errorf("statements marked %q for one call = %d, want 1", signMarker, len(m))
				}
				want := w.approves(u)
				if len(signs) != 1 || signs[0] != want {
					t.Errorf("subject %s: subject_signs = %v, decideTx approve succeeds = %t", u, signs, want)
				}
				if want {
					signing++
				}
			}
			if signing != c.signers {
				t.Errorf("candidates that approve through decideTx = %d, want %d", signing, c.signers)
			}
		})
	}
}
