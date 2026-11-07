package approval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

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

func (w *signWorld) probe() CreatorProbe {
	return CreatorProbe{InvoiceID: w.f.invoiceID, RoleKey: ptr(w.key)}
}

func (w *signWorld) limitedTo(ents ...string) memberSpec {
	return memberSpec{role: "reviewer", limited: true, entityIDs: ents}
}

// readReasons runs unsignableReasonsTx as the app role in a tenant tx that sets no client scope.
func readReasons(t *testing.T, pool *pgxpool.Pool, tenantID string, probes []CreatorProbe) []string {
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

// approvers returns the seeded candidates whose approve succeeds through decideTx, each rolled back.
func (w *signWorld) approvers() []string {
	w.t.Helper()
	var out []string
	for _, u := range w.users {
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
			out = append(out, u)
		case errors.Is(err, db.ErrNotActiveMember), errors.Is(err, ErrNotPermitted), errors.Is(err, ErrRunNotFound),
			errors.Is(err, ErrNotRoleHolder), errors.Is(err, ErrCreatorRefused):
		default:
			w.t.Fatalf("approve as %s: unexpected error %v", u, err)
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

// TestCanSign_AgreesWithTheApprovePath: each row's literal reason holds, and the reason is ""
// exactly when some seeded candidate's decideTx approve succeeds.
func TestCanSign_AgreesWithTheApprovePath(t *testing.T) {
	super, app := dbTestPools(t)
	for _, set := range [][]signRow{signRows, signCreatorRows} {
		for _, r := range set {
			t.Run(r.name, func(t *testing.T) {
				w := newSignWorld(t, super, app, "APSC-14 agree "+r.name)
				r.seed(w)

				wantReasons(t, readReasons(t, app, w.f.tenantID, []CreatorProbe{w.probe()}), r.want)

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

	probes := []CreatorProbe{
		{InvoiceID: w.f.invoiceID, RoleKey: nil},
		{InvoiceID: w.f.invoiceID, RoleKey: ptr("no-such-role")},
	}
	wantReasons(t, readReasons(t, app, w.f.tenantID, probes), "deleted", "deleted")
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
	wantReasons(t, readReasons(t, app, w.f.tenantID, []CreatorProbe{w.probe()}), "")
	if got := w.approvers(); len(got) != 1 {
		t.Errorf("in-house approvers through decideTx = %d, want 1", len(got))
	}

	setKind("firm")
	wantReasons(t, readReasons(t, app, w.f.tenantID, []CreatorProbe{w.probe()}), "out of client reach")
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
	probe := func(id string) CreatorProbe { return CreatorProbe{InvoiceID: id, RoleKey: ptr(w.key)} }

	// The out-of-reach invoice I2 sits at every position, so no fixed output order matches all three.
	for _, c := range []struct {
		ids  []string
		want []string
	}{
		{[]string{i2, i1, i3}, []string{"out of client reach", "", ""}},
		{[]string{i1, i3, i2}, []string{"", "", "out of client reach"}},
		{[]string{i3, i2, i1}, []string{"", "out of client reach", ""}},
	} {
		probes := make([]CreatorProbe, len(c.ids))
		for i, id := range c.ids {
			probes[i] = probe(id)
		}
		wantReasons(t, readReasons(t, app, w.f.tenantID, probes), c.want...)
	}

	// Past a handful of probes the planner may hash-aggregate and drop the input order on its own.
	const n = 30
	long := make([]CreatorProbe, n)
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

	roles := []struct {
		key, want string
		seed      func(roleID string)
	}{
		{"cs-staffed", "", func(id string) { seedMemberOf(t, super, tenant, memberSpec{role: "reviewer", roleID: id}) }},
		{"cs-empty", "no holder", func(string) {}},
		{"cs-away", "out of client reach", func(id string) {
			seedMemberOf(t, super, tenant, memberSpec{role: "reviewer", limited: true, entityIDs: []string{w.entB}, roleID: id})
		}},
		{"cs-deleted", "deleted", func(id string) {
			seedMemberOf(t, super, tenant, memberSpec{role: "reviewer", roleID: id})
			softDeleteWorkflowRole(t, super, id)
		}},
	}
	for _, r := range roles {
		r.seed(seedWorkflowRole(t, super, tenant, r.key, r.key))
	}

	const n = 30
	probes := make([]CreatorProbe, n)
	want := make([]string, n)
	for i := range probes {
		r := roles[i%len(roles)]
		probes[i] = CreatorProbe{InvoiceID: seedInvoice(t, super, tenant, w.f.entityID, fmt.Sprintf("APSC-14 one-%d", i)), RoleKey: ptr(r.key)}
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
					got, err = unsignableReasonsTx(ctx, tx, []CreatorProbe{w.probe()})
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

	wantReasons(t, readReasons(t, app, t1.f.tenantID, []CreatorProbe{t1.probe()}), "no holder")
	wantReasons(t, readReasons(t, app, t2.f.tenantID, []CreatorProbe{t2.probe()}), "")
	wantReasons(t, readReasons(t, app, t3.f.tenantID, []CreatorProbe{t3.probe()}), "")
	if got := t3.approvers(); len(got) != 1 {
		t.Errorf("tenant 3 approvers through decideTx = %d, want 1", len(got))
	}
}

func TestCanSign_ReadErrorIsReturned(t *testing.T) {
	super, app := dbTestPools(t)
	failing := failingAppPool(t, signMarker)
	w := newSignWorld(t, super, app, "APSC-14 read error")
	w.inRole(memberSpec{role: "reviewer"})

	var got []string
	var inner error
	_ = db.WithinTenantTx(context.Background(), failing, w.f.tenantID, func(tx pgx.Tx) error {
		got, inner = unsignableReasonsTx(context.Background(), tx, []CreatorProbe{w.probe()})
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
