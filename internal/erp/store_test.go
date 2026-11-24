package erp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// --- fixtures ---------------------------------------------------------------------------

// storePools returns the superuser (seed, BYPASSRLS) and app-role (Store) pools, or skips
// when the DSNs are unset, as the portfolio suite does.
func storePools(t *testing.T) (super, app *pgxpool.Pool) {
	t.Helper()
	appURL, superURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("erp store test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run make test-db PKGS=./internal/erp/...)")
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

func fakeStore(t *testing.T, app *pgxpool.Pool) *Store {
	t.Helper()
	return storeOver(t, app, nil, NewFake(testRedirect))
}

// storeOver wires the store as portfolio does: an insert-only queue client and, unless a test needs
// one, no secret store.
func storeOver(t *testing.T, app *pgxpool.Pool, secrets SecretStore, cs ...Connector) *Store {
	t.Helper()
	reg, err := NewRegistry(cs...)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	q, err := queue.New(app, queue.Config{})
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	return NewStore(app, reg, secrets, q)
}

func mustExec(t *testing.T, pool *pgxpool.Pool, q string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func mustScan(t *testing.T, pool *pgxpool.Pool, q string, args []any, dst ...any) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), q, args...).Scan(dst...); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
}

func seedTenant(t *testing.T, super *pgxpool.Pool, kind string) string {
	t.Helper()
	id := uuid.NewString()
	mustExec(t, super, "INSERT INTO tenants (id, name, kind) VALUES ($1, $2, $3)", id, "erp store "+kind, kind)
	t.Cleanup(func() { _, _ = super.Exec(context.Background(), "DELETE FROM tenants WHERE id = $1", id) })
	return id
}

// seedMember inserts a membership (and its client set) and returns a context for that caller.
func seedMember(t *testing.T, super *pgxpool.Pool, tenant, role, status string, allClients bool, clients ...string) context.Context {
	t.Helper()
	user := uuid.NewString()
	mustExec(t, super,
		"INSERT INTO memberships (tenant_id, user_id, role, status, all_clients) VALUES ($1, $2, $3, $4, $5)",
		tenant, user, role, status, allClients)
	for _, e := range clients {
		mustExec(t, super, "INSERT INTO membership_clients (tenant_id, user_id, entity_id) VALUES ($1, $2, $3)", tenant, user, e)
	}
	return auth.WithIdentity(context.Background(), auth.Identity{Subject: user, Role: "authenticated", TenantID: tenant})
}

func subjectOf(ctx context.Context) string {
	id, _ := auth.IdentityFromContext(ctx)
	return id.Subject
}

func seedClient(t *testing.T, super *pgxpool.Pool, tenant, name string) string {
	t.Helper()
	var id string
	mustScan(t, super, "INSERT INTO business_entities (tenant_id, name) VALUES ($1, $2) RETURNING id", []any{tenant, name}, &id)
	return id
}

// seedGrant inserts an api_key grant; secretRef "" leaves secret_ref NULL.
func seedGrant(t *testing.T, super *pgxpool.Pool, tenant, secretRef string) string {
	t.Helper()
	var id string
	mustScan(t, super,
		"INSERT INTO erp_grants (tenant_id, system, kind, secret_ref) VALUES ($1, 'fake', 'api_key', nullif($2, '')) RETURNING id",
		[]any{tenant, secretRef}, &id)
	return id
}

type connRow struct {
	id, tenant, entity, key, status, grant string // "" is NULL for key and grant, and a generated value for id
	createdAt                              *time.Time
}

func seedConnRow(t *testing.T, super *pgxpool.Pool, r connRow) string {
	t.Helper()
	var id string
	mustScan(t, super,
		"INSERT INTO erp_connections (id, tenant_id, entity_id, system, external_key, status, grant_id, created_at)"+
			" VALUES (coalesce(nullif($1, '')::uuid, gen_random_uuid()), $2, $3, 'fake', nullif($4, ''), $5, nullif($6, '')::uuid, coalesce($7::timestamptz, now()))"+
			" RETURNING id",
		[]any{r.id, r.tenant, r.entity, r.key, r.status, r.grant, r.createdAt}, &id)
	return id
}

// seedActiveConn inserts an active connection on its own new grant.
func seedActiveConn(t *testing.T, super *pgxpool.Pool, tenant, entity, key string) (connID, grantID string) {
	t.Helper()
	g := seedGrant(t, super, tenant, "")
	return seedConnRow(t, super, connRow{tenant: tenant, entity: entity, key: key, status: "active", grant: g}), g
}

func lockWorkspace(t *testing.T, super *pgxpool.Pool, tenant, by string) {
	t.Helper()
	mustExec(t, super,
		"UPDATE tenants SET deletion_scheduled_at = now(), deletion_due_at = now() + interval '30 days', deletion_scheduled_by = $2 WHERE id = $1",
		tenant, by)
}

func countOf(t *testing.T, super *pgxpool.Pool, q string, args ...any) int {
	t.Helper()
	var n int
	mustScan(t, super, q, args, &n)
	return n
}

func connCount(t *testing.T, super *pgxpool.Pool, tenant string) int {
	return countOf(t, super, "SELECT count(*) FROM erp_connections WHERE tenant_id = $1", tenant)
}

func grantCount(t *testing.T, super *pgxpool.Pool, tenant string) int {
	return countOf(t, super, "SELECT count(*) FROM erp_grants WHERE tenant_id = $1", tenant)
}

func eventCount(t *testing.T, super *pgxpool.Pool, tenant, event string) int {
	return countOf(t, super, "SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = $2", tenant, event)
}

func erpAuditCount(t *testing.T, super *pgxpool.Pool, tenant string) int {
	return countOf(t, super, "SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event LIKE 'erp.%'", tenant)
}

func orphanGrants(t *testing.T, super *pgxpool.Pool, tenant string) int {
	return countOf(t, super,
		"SELECT count(*) FROM erp_grants g WHERE g.tenant_id = $1 AND NOT EXISTS (SELECT 1 FROM erp_connections c WHERE c.grant_id = g.id)", tenant)
}

func grantExists(t *testing.T, super *pgxpool.Pool, id string) bool {
	return countOf(t, super, "SELECT count(*) FROM erp_grants WHERE id = $1", id) == 1
}

// erpState is every ERP row of the tenant (connections, grants, OAuth states), its queued ERP jobs and
// the ERP audit count, so "nothing written" also catches an update that leaves the row counts alone.
func erpState(t *testing.T, super *pgxpool.Pool, tenant string) string {
	t.Helper()
	var conns, grants, states string
	mustScan(t, super,
		"SELECT coalesce((SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id) FROM erp_connections c WHERE c.tenant_id = $1), '[]')::text,"+
			" coalesce((SELECT jsonb_agg(to_jsonb(g) ORDER BY g.id) FROM erp_grants g WHERE g.tenant_id = $1), '[]')::text,"+
			" coalesce((SELECT jsonb_agg(to_jsonb(s) ORDER BY s.id) FROM erp_oauth_states s WHERE s.tenant_id = $1), '[]')::text",
		[]any{tenant}, &conns, &grants, &states)
	jobs := countOf(t, super, "SELECT count(*) FROM river_job WHERE kind LIKE 'erp%' AND args->>'tenant_id' = $1", tenant)
	return fmt.Sprintf("connections=%s grants=%s states=%s jobs=%d audit=%d", conns, grants, states, jobs, erpAuditCount(t, super, tenant))
}

type connState struct {
	status     string
	key, grant *string
}

func (s connState) String() string {
	return fmt.Sprintf("{%s key=%s grant=%s}", s.status, deref(s.key), deref(s.grant))
}

func stateOf(t *testing.T, super *pgxpool.Pool, id string) connState {
	t.Helper()
	var s connState
	mustScan(t, super, "SELECT status, external_key, grant_id::text FROM erp_connections WHERE id = $1", []any{id}, &s.status, &s.key, &s.grant)
	return s
}

func deref(p *string) string {
	if p == nil {
		return "<NULL>"
	}
	return *p
}

func idsOf(cs []Connection) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

// everyStoreError lets wantErr prove an answer is that error and no other.
var everyStoreError = []error{
	ErrNotPermitted, ErrUnknownSystem, ErrNoSuchRecord, ErrAlreadyConnected,
	ErrAlreadyDisconnected, ErrKeylessPending, db.ErrNotActiveMember, db.ErrNoTenant, db.ErrWorkspaceLocked,
	ErrInvalidMapping, ErrInvalidLookup,
}

func wantErr(t *testing.T, label string, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Errorf("%s: err = %v, want %v", label, got, want)
		return
	}
	for _, other := range everyStoreError {
		if other != want && errors.Is(got, other) {
			t.Errorf("%s: err %v also matches %v", label, got, other)
		}
	}
}

// race starts every fn at once and returns their errors in order.
func race(fns ...func() error) []error {
	errs := make([]error, len(fns))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, f := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = f()
		}()
	}
	close(start)
	wg.Wait()
	return errs
}

// bounded keeps a deadlock from hanging the suite.
func bounded(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	t.Cleanup(cancel)
	return c
}

type storeCall struct {
	name string
	fn   func(ctx context.Context) error
}

// allCalls drives every method; entity and connection must be real so only the caller can refuse.
func allCalls(s *Store, entity, conn string) []storeCall {
	return []storeCall{
		{"Gate", func(ctx context.Context) error { return s.Gate(ctx) }},
		{"List", func(ctx context.Context) error { _, err := s.List(ctx, entity); return err }},
		{"Connect", func(ctx context.Context) error {
			_, _, _, err := s.Connect(ctx, entity, ConnectInput{System: "fake"})
			return err
		}},
		{"Disconnect", func(ctx context.Context) error { _, err := s.Disconnect(ctx, entity, conn); return err }},
	}
}

// --- connect issues a consent URL and a single-use state ---------------------------------

// consentState returns the state of a consent URL after checking it points at the fake's consent page.
func consentState(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || u.Host != testRedirect.Host || u.Path != "/erp/fake/consent" {
		t.Fatalf("consent URL = %q (%v), want the fake consent page on %s", raw, err, testRedirect.Host)
	}
	state := u.Query().Get("state")
	if state == "" {
		t.Fatalf("consent URL %q carries no state", raw)
	}
	return state
}

func hashOf(state string) []byte {
	h := sha256.Sum256([]byte(state))
	return h[:]
}

type stateRow struct {
	hash        []byte
	user, conn  string
	used        *time.Time
	secondsLeft float64
}

// statesOf lists the tenant's OAuth state rows; secondsLeft is measured by the database clock.
func statesOf(t *testing.T, super *pgxpool.Pool, tenant string) []stateRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		"SELECT state_hash, user_id::text, connection_id::text, used_at, extract(epoch FROM expires_at - now())::float8"+
			" FROM erp_oauth_states WHERE tenant_id = $1 ORDER BY created_at, id", tenant)
	if err != nil {
		t.Fatalf("read erp_oauth_states: %v", err)
	}
	defer rows.Close()
	var out []stateRow
	for rows.Next() {
		var r stateRow
		if err := rows.Scan(&r.hash, &r.user, &r.conn, &r.used, &r.secondsLeft); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read erp_oauth_states: %v", err)
	}
	return out
}

func TestStore_ConnectCreatesAKeylessPendingRowAndAState(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1 := seedClient(t, super, tenant, "Client One")

	conn, consent, created, err := store.Connect(admin, e1, ConnectInput{System: "fake"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !created {
		t.Error("created = false for the first connect of a client")
	}
	if conn.Status != "pending" || conn.ExternalKey != nil || conn.System != "fake" || conn.EntityID != e1 {
		t.Errorf("Connect = %+v, want a keyless pending fake connection of %s", conn, e1)
	}
	if s := stateOf(t, super, conn.ID); s.status != "pending" || s.key != nil || s.grant != nil {
		t.Errorf("row = %+v, want pending with no key and no grant", s)
	}
	if n := grantCount(t, super, tenant); n != 0 {
		t.Errorf("%d grant rows after connect, want 0 (a grant comes from the callback)", n)
	}

	state := consentState(t, consent)
	prefix, hash, ok := ParseState(state)
	if !ok || prefix != tenant {
		t.Errorf("state tenant prefix = %q (well-formed %v), want %s", prefix, ok, tenant)
	}
	rows := statesOf(t, super, tenant)
	if len(rows) != 1 {
		t.Fatalf("%d state rows, want 1", len(rows))
	}
	r := rows[0]
	if !reflect.DeepEqual(r.hash, hashOf(state)) || !reflect.DeepEqual(r.hash, hash) {
		t.Error("the stored hash is not the sha256 of the consent URL's state")
	}
	if r.user != subjectOf(admin) || r.conn != conn.ID || r.used != nil {
		t.Errorf("state row = user %s connection %s used %v, want the admin %s, connection %s, unused", r.user, r.conn, r.used, subjectOf(admin), conn.ID)
	}
	if r.secondsLeft <= 570 || r.secondsLeft > 600 {
		t.Errorf("state expires in %.0f s, want about 600 (10 minutes)", r.secondsLeft)
	}
	if n := eventCount(t, super, tenant, "erp.connection.created"); n != 1 {
		t.Errorf("%d erp.connection.created events, want 1", n)
	}
	if n := eventCount(t, super, tenant, "erp.connection.authorised"); n != 0 {
		t.Errorf("%d erp.connection.authorised events at connect, want 0 (the callback authorises)", n)
	}
}

func TestStore_ConnectAgainReusesTheRowAndKeepsOnlyTheNewestState(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1, e2 := seedClient(t, super, tenant, "Client One"), seedClient(t, super, tenant, "Client Two")

	neighbour, neighbourURL, created, err := store.Connect(admin, e2, ConnectInput{System: "fake"})
	if err != nil || !created {
		t.Fatalf("Connect on the other client = %v, created %v", err, created)
	}
	first, firstURL, created, err := store.Connect(admin, e1, ConnectInput{System: "fake"})
	if err != nil || !created {
		t.Fatalf("first Connect = %v, created %v", err, created)
	}
	if first.ID == neighbour.ID {
		t.Fatal("two clients share a keyless pending row")
	}
	var last string
	seen := map[string]bool{consentState(t, firstURL): true}
	for i := range 2 {
		again, consent, created, err := store.Connect(admin, e1, ConnectInput{System: "fake"})
		if err != nil {
			t.Fatalf("Connect again #%d: %v", i+1, err)
		}
		if again.ID != first.ID || created {
			t.Errorf("Connect again #%d = %s created %v, want the same row %s, not created", i+1, again.ID, created, first.ID)
		}
		last = consentState(t, consent)
		if seen[last] {
			t.Errorf("Connect again #%d answered a state it had answered before", i+1)
		}
		seen[last] = true
	}

	var mine, theirs [][]byte
	for _, r := range statesOf(t, super, tenant) {
		if r.conn == first.ID {
			mine = append(mine, r.hash)
		} else {
			theirs = append(theirs, r.hash)
		}
	}
	if len(mine) != 1 || !reflect.DeepEqual(mine[0], hashOf(last)) {
		t.Errorf("the connection has %d state rows, want exactly one, the newest", len(mine))
	}
	if len(theirs) != 1 || !reflect.DeepEqual(theirs[0], hashOf(consentState(t, neighbourURL))) {
		t.Errorf("the other client's connection has %d state rows, want its own, untouched", len(theirs))
	}
	if n := connCount(t, super, tenant); n != 2 {
		t.Errorf("%d connection rows, want 2", n)
	}
	if n := eventCount(t, super, tenant, "erp.connection.created"); n != 2 {
		t.Errorf("%d erp.connection.created events, want 2 (one per client, none for a reuse)", n)
	}
}

func TestStore_ConcurrentConnectsShareOneKeylessRow(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admins := [2]context.Context{
		bounded(t, seedMember(t, super, tenant, "admin", "active", true)),
		bounded(t, seedMember(t, super, tenant, "admin", "active", true)),
	}
	for i := 0; i < 10; i++ {
		e := seedClient(t, super, tenant, fmt.Sprintf("Client %d", i))
		var conns [2]Connection
		var urls [2]string
		var created [2]bool
		errs := race(
			func() (err error) {
				conns[0], urls[0], created[0], err = store.Connect(admins[0], e, ConnectInput{System: "fake"})
				return
			},
			func() (err error) {
				conns[1], urls[1], created[1], err = store.Connect(admins[1], e, ConnectInput{System: "fake"})
				return
			},
		)
		for j, err := range errs {
			if err != nil {
				t.Fatalf("round %d: connect %d: %v", i, j, err)
			}
		}
		if conns[0].ID != conns[1].ID || created[0] == created[1] {
			t.Errorf("round %d: connections %s and %s, created %v, want one row and exactly one creator", i, conns[0].ID, conns[1].ID, created)
		}
		if n := countOf(t, super, "SELECT count(*) FROM erp_connections WHERE entity_id = $1", e); n != 1 {
			t.Errorf("round %d: %d connection rows, want 1", i, n)
		}
		var rows []stateRow
		for _, r := range statesOf(t, super, tenant) {
			if r.conn == conns[0].ID {
				rows = append(rows, r)
			}
		}
		if len(rows) != 1 {
			t.Fatalf("round %d: %d state rows, want 1", i, len(rows))
		}
		// Whichever connect ran last owns the row's one state, and that state names its own admin.
		winners := 0
		for j := range admins {
			if reflect.DeepEqual(rows[0].hash, hashOf(consentState(t, urls[j]))) {
				winners++
				if rows[0].user != subjectOf(admins[j]) {
					t.Errorf("round %d: the surviving state names %s, want its minter %s", i, rows[0].user, subjectOf(admins[j]))
				}
			}
		}
		if winners != 1 {
			t.Errorf("round %d: the surviving state matches %d of the two answered URLs, want exactly 1", i, winners)
		}
	}
	if n := eventCount(t, super, tenant, "erp.connection.created"); n != 10 {
		t.Errorf("%d erp.connection.created events, want 10 (one per client)", n)
	}
}

func TestStore_ConnectByIDReauthorises(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1 := seedClient(t, super, tenant, "Client One")
	g := seedGrant(t, super, tenant, "erp-grant-held")
	rows := []struct {
		name, status, key, grant string
	}{
		{"disconnected", "disconnected", "fake-co-1", ""},
		{"reconsent", "reconsent", "fake-co-2", g},
		{"keyed pending", "pending", "fake-co-3", ""},
		{"keyless pending", "pending", "", ""},
	}
	for _, r := range rows {
		id := seedConnRow(t, super, connRow{tenant: tenant, entity: e1, key: r.key, status: r.status, grant: r.grant})
		conn, consent, created, err := store.Connect(admin, e1, ConnectInput{System: "fake", ConnectionID: id})
		if err != nil {
			t.Fatalf("%s: Connect: %v", r.name, err)
		}
		if conn.ID != id || created || conn.Status != "pending" {
			t.Errorf("%s: Connect = %s %s created %v, want %s pending, not created", r.name, conn.ID, conn.Status, created, id)
		}
		st := stateOf(t, super, id)
		if st.status != "pending" || deref(st.key) != deref(strOrNil(r.key)) || deref(st.grant) != deref(strOrNil(r.grant)) {
			t.Errorf("%s: row = %+v, want pending with its key %q and grant %q kept", r.name, st, r.key, r.grant)
		}
		state := consentState(t, consent)
		var mine [][]byte
		for _, sr := range statesOf(t, super, tenant) {
			if sr.conn == id {
				mine = append(mine, sr.hash)
			}
		}
		if len(mine) != 1 || !reflect.DeepEqual(mine[0], hashOf(state)) {
			t.Errorf("%s: %d state rows for the connection, want exactly the answered state", r.name, len(mine))
		}
	}
	if !grantExists(t, super, g) {
		t.Error("the re-authorised row's grant was deleted before the callback replaced it")
	}
	if n := eventCount(t, super, tenant, "erp.connection.created"); n != 0 {
		t.Errorf("%d erp.connection.created events for re-authorised rows, want 0", n)
	}
	if n := countOf(t, super, "SELECT count(*) FROM river_job WHERE kind = 'erp_secret_delete' AND args->>'tenant_id' = $1", tenant); n != 0 {
		t.Errorf("%d delete jobs at connect, want 0 (the grant is kept)", n)
	}
}

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func TestStore_ConnectActiveByIDIsAlreadyConnected(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1 := seedClient(t, super, tenant, "Client One")
	active, g := seedActiveConn(t, super, tenant, e1, "fake-co-1")
	sibling := seedConnRow(t, super, connRow{tenant: tenant, entity: e1, key: "fake-co-2", status: "disconnected"})
	before := erpState(t, super, tenant)

	_, consent, created, err := store.Connect(admin, e1, ConnectInput{System: "fake", ConnectionID: active})
	wantErr(t, "Connect on an active row", err, ErrAlreadyConnected)
	if consent != "" || created {
		t.Errorf("consent URL %q, created %v on a refusal", consent, created)
	}
	if got := erpState(t, super, tenant); got != before {
		t.Errorf("a refused connect changed the rows, states or audit:\nbefore %s\nafter  %s", before, got)
	}
	if s := stateOf(t, super, active); s.status != "active" || deref(s.grant) != g {
		t.Errorf("active row = %+v, want active on %s", s, g)
	}
	// The same client connects a non-active row, so the 409 came from the status.
	if _, consent, _, err := store.Connect(admin, e1, ConnectInput{System: "fake", ConnectionID: sibling}); err != nil || consent == "" {
		t.Errorf("control: Connect on a disconnected sibling = %q, %v, want a consent URL", consent, err)
	}
}

func TestStore_ConnectByForeignIDIsNotFound(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant, other := seedTenant(t, super, "firm"), seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1, e2 := seedClient(t, super, tenant, "Client One"), seedClient(t, super, tenant, "Client Two")
	eOther := seedClient(t, super, other, "Foreign Client")
	mine := seedConnRow(t, super, connRow{tenant: tenant, entity: e1, key: "fake-co-1", status: "disconnected"})
	theirs := seedConnRow(t, super, connRow{tenant: tenant, entity: e2, key: "fake-co-1", status: "disconnected"})
	foreign := seedConnRow(t, super, connRow{tenant: other, entity: eOther, key: "fake-co-1", status: "disconnected"})
	var otherSystem string
	mustScan(t, super,
		"INSERT INTO erp_connections (tenant_id, entity_id, system, external_key, status) VALUES ($1, $2, 'other', 'k', 'disconnected') RETURNING id",
		[]any{tenant, e1}, &otherSystem)
	before := erpState(t, super, tenant)
	beforeOther := erpState(t, super, other)

	for name, id := range map[string]string{
		"another client's connection": theirs,
		"another system's connection": otherSystem,
		"another tenant's connection": foreign,
		"an unknown id":               uuid.NewString(),
		"a malformed id":              "abc",
	} {
		_, consent, created, err := store.Connect(admin, e1, ConnectInput{System: "fake", ConnectionID: id})
		wantErr(t, name, err, ErrNoSuchRecord)
		if consent != "" || created {
			t.Errorf("%s: consent URL %q, created %v on a refusal", name, consent, created)
		}
	}
	if got := erpState(t, super, tenant); got != before {
		t.Errorf("refused connects changed the rows, states or audit:\nbefore %s\nafter  %s", before, got)
	}
	if got := erpState(t, super, other); got != beforeOther {
		t.Errorf("refused connects changed the other tenant:\nbefore %s\nafter  %s", beforeOther, got)
	}
	if _, consent, _, err := store.Connect(admin, e1, ConnectInput{System: "fake", ConnectionID: mine}); err != nil || consent == "" {
		t.Errorf("control: Connect on the client's own row = %q, %v, want a consent URL", consent, err)
	}
}

// callRecorder counts every connector call; Fake does the work.
type callRecorder struct {
	*Fake
	mu    sync.Mutex
	calls map[string]int
	state string
}

func newCallRecorder() *callRecorder {
	return &callRecorder{Fake: NewFake(testRedirect), calls: map[string]int{}}
}

func (r *callRecorder) note(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[name]++
}

func (r *callRecorder) Authorize(ctx context.Context, req AuthorizeRequest) (AuthorizeResult, error) {
	r.note("Authorize")
	r.mu.Lock()
	r.state = req.State
	r.mu.Unlock()
	return r.Fake.Authorize(ctx, req)
}

func (r *callRecorder) Exchange(ctx context.Context, req ExchangeRequest) (ExchangeResult, error) {
	r.note("Exchange")
	return r.Fake.Exchange(ctx, req)
}

func (r *callRecorder) Refresh(ctx context.Context, c Credentials) (GrantMaterial, error) {
	r.note("Refresh")
	return r.Fake.Refresh(ctx, c)
}

func (r *callRecorder) Validate(ctx context.Context, c Credentials, scope string) error {
	r.note("Validate")
	return r.Fake.Validate(ctx, c, scope)
}

func (r *callRecorder) ListChanged(ctx context.Context, c Credentials, scope string, cursor []byte) (Changes, error) {
	r.note("ListChanged")
	return r.Fake.ListChanged(ctx, c, scope, cursor)
}

func (r *callRecorder) Fetch(ctx context.Context, c Credentials, ref DocRef) (Document, error) {
	r.note("Fetch")
	return r.Fake.Fetch(ctx, c, ref)
}

func (r *callRecorder) FetchParty(ctx context.Context, c Credentials, scope, partyID string) (Party, error) {
	r.note("FetchParty")
	return r.Fake.FetchParty(ctx, c, scope, partyID)
}

func (r *callRecorder) WriteBack(ctx context.Context, c Credentials, ref DocRef, wb WriteBack) (DocRef, error) {
	r.note("WriteBack")
	return r.Fake.WriteBack(ctx, c, ref, wb)
}

func (r *callRecorder) VerifyWebhook(ctx context.Context, h http.Header, body []byte) ([]Hint, error) {
	r.note("VerifyWebhook")
	return r.Fake.VerifyWebhook(ctx, h, body)
}

func (r *callRecorder) EnsureSubscriptions(ctx context.Context, c Credentials, scope string) error {
	r.note("EnsureSubscriptions")
	return r.Fake.EnsureSubscriptions(ctx, c, scope)
}

func TestStore_ConnectCallsOnlyAuthorize(t *testing.T) {
	super, app := storePools(t)
	rec := newCallRecorder()
	store := storeOver(t, app, nil, rec)
	tenant := seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1 := seedClient(t, super, tenant, "Client One")
	reconnectable := seedConnRow(t, super, connRow{tenant: tenant, entity: e1, key: "fake-co-1", status: "disconnected"})

	_, consent, _, err := store.Connect(admin, e1, ConnectInput{System: "fake"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if got := consentState(t, consent); rec.state != got {
		t.Errorf("Authorize received state %q, want the one in the consent URL %q", rec.state, got)
	}
	if prefix, _, ok := ParseState(rec.state); !ok || prefix != tenant {
		t.Errorf("Authorize received state %q, want one that binds the tenant %s", rec.state, tenant)
	}
	if _, _, _, err := store.Connect(admin, e1, ConnectInput{System: "fake", ConnectionID: reconnectable}); err != nil {
		t.Fatalf("Connect by id: %v", err)
	}
	if want := map[string]int{"Authorize": 2}; !reflect.DeepEqual(rec.calls, want) {
		t.Errorf("connector calls = %v, want %v (one Authorize per connect, nothing else)", rec.calls, want)
	}
}

// --- AC 1: the admin gate ---------------------------------------------------------------

func TestStore_NonAdminRefusedOnGateConnectAndDisconnect(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	for _, kind := range []string{"firm", "in_house"} {
		for _, role := range []string{"preparer", "reviewer"} {
			t.Run(kind+"/"+role, func(t *testing.T) {
				tenant := seedTenant(t, super, kind)
				admin := seedMember(t, super, tenant, "admin", "active", true)
				caller := seedMember(t, super, tenant, role, "active", true)
				e1 := seedClient(t, super, tenant, "Client One")
				c1, g1 := seedActiveConn(t, super, tenant, e1, "fake-co-1")
				before := erpState(t, super, tenant)

				wantErr(t, "Gate", store.Gate(caller), ErrNotPermitted)
				_, _, created, err := store.Connect(caller, e1, ConnectInput{System: "fake"})
				wantErr(t, "Connect", err, ErrNotPermitted)
				if created {
					t.Error("Connect created = true for a refused caller")
				}
				_, err = store.Disconnect(caller, e1, c1)
				wantErr(t, "Disconnect", err, ErrNotPermitted)

				if err := store.Gate(admin); err != nil {
					t.Errorf("control: an active admin's Gate = %v, want nil", err)
				}
				if got := erpState(t, super, tenant); got != before {
					t.Errorf("ERP rows or audit changed after refused calls:\nbefore %s\nafter  %s", before, got)
				}
				if s := stateOf(t, super, c1); s.status != "active" || deref(s.grant) != g1 {
					t.Errorf("connection after refused Disconnect = %+v, want active on %s", s, g1)
				}
			})
		}
	}
}

func TestStore_InactiveCallerRefusedBySeam(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1 := seedClient(t, super, tenant, "Client One")
	c1, _ := seedActiveConn(t, super, tenant, e1, "fake-co-1")
	callers := []struct {
		name string
		ctx  context.Context
	}{
		{"suspended admin", seedMember(t, super, tenant, "admin", "suspended", true)},
		{"suspended preparer", seedMember(t, super, tenant, "preparer", "suspended", true)},
		{"no membership", auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenant})},
	}
	before := erpState(t, super, tenant)
	for _, c := range callers {
		for _, call := range allCalls(store, e1, c1) {
			wantErr(t, c.name+" "+call.name, call.fn(c.ctx), db.ErrNotActiveMember)
		}
	}
	if err := store.Gate(admin); err != nil {
		t.Fatalf("control: an active admin's Gate = %v, want nil", err)
	}
	if got := erpState(t, super, tenant); got != before {
		t.Errorf("ERP rows or audit changed:\nbefore %s\nafter  %s", before, got)
	}
}

// The preparer reaches each input; only the gate clause can be what refuses it, because the
// admin control gets the downstream error for the same input. A row the admin could connect is
// left without a control, so a refused role is the only thing standing between it and a state row.
func TestStore_NonAdminRefusedWhateverTheInput(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant, other := seedTenant(t, super, "firm"), seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1, e2 := seedClient(t, super, tenant, "Client One"), seedClient(t, super, tenant, "Client Two")
	eOther := seedClient(t, super, other, "Foreign Client")
	c1, _ := seedActiveConn(t, super, tenant, e1, "fake-co-1")
	c2, _ := seedActiveConn(t, super, tenant, e2, "fake-co-1")
	cOther := seedConnRow(t, super, connRow{tenant: other, entity: eOther, key: "fake-co-1", status: "disconnected"})
	reconnectable := seedConnRow(t, super, connRow{tenant: tenant, entity: e1, key: "fake-co-3", status: "disconnected"})
	keyless := seedConnRow(t, super, connRow{tenant: tenant, entity: e1, status: "pending"})

	connects := []struct {
		name, entity string
		in           ConnectInput
		downstream   error // nil: the admin would succeed, so there is no control
	}{
		{"unknown system", e1, ConnectInput{System: "sage"}, ErrUnknownSystem},
		{"non-UUID client", "not-a-uuid", ConnectInput{System: "fake"}, ErrNoSuchRecord},
		{"unknown client", uuid.NewString(), ConnectInput{System: "fake"}, ErrNoSuchRecord},
		{"other tenant's client", eOther, ConnectInput{System: "fake"}, ErrNoSuchRecord},
		{"active connection", e1, ConnectInput{System: "fake", ConnectionID: c1}, ErrAlreadyConnected},
		{"another client's connection", e1, ConnectInput{System: "fake", ConnectionID: c2}, ErrNoSuchRecord},
		{"another tenant's connection", e1, ConnectInput{System: "fake", ConnectionID: cOther}, ErrNoSuchRecord},
		{"unknown connection", e1, ConnectInput{System: "fake", ConnectionID: uuid.NewString()}, ErrNoSuchRecord},
		{"non-UUID connection", e1, ConnectInput{System: "fake", ConnectionID: "abc"}, ErrNoSuchRecord},
		{"disconnected connection of the client", e1, ConnectInput{System: "fake", ConnectionID: reconnectable}, nil},
		{"keyless pending connection of the client", e1, ConnectInput{System: "fake", ConnectionID: keyless}, nil},
	}
	disconnects := []struct {
		name, entity, conn string
	}{
		{"non-UUID client", "not-a-uuid", c1},
		{"other tenant's client", eOther, c1},
		{"unknown connection", e1, uuid.NewString()},
		{"non-UUID connection", e1, "not-a-uuid"},
	}
	for _, role := range []string{"preparer", "reviewer"} {
		caller := seedMember(t, super, tenant, role, "active", true)
		before := erpState(t, super, tenant)
		for _, c := range connects {
			_, consent, created, err := store.Connect(caller, c.entity, c.in)
			wantErr(t, role+" Connect "+c.name, err, ErrNotPermitted)
			if consent != "" || created {
				t.Errorf("%s Connect %s: consent URL %q, created %v on a refusal", role, c.name, consent, created)
			}
			if c.downstream != nil {
				_, _, _, err = store.Connect(admin, c.entity, c.in)
				wantErr(t, "control: admin Connect "+c.name, err, c.downstream)
			}
		}
		for _, d := range disconnects {
			_, err := store.Disconnect(caller, d.entity, d.conn)
			wantErr(t, role+" Disconnect "+d.name, err, ErrNotPermitted)
			_, err = store.Disconnect(admin, d.entity, d.conn)
			wantErr(t, "control: admin Disconnect "+d.name, err, ErrNoSuchRecord)
		}
		if got := erpState(t, super, tenant); got != before {
			t.Errorf("%s: ERP rows, states, jobs or audit changed:\nbefore %s\nafter  %s", role, before, got)
		}
	}
	if n := countOf(t, super, "SELECT count(*) FROM erp_oauth_states WHERE tenant_id = $1", tenant); n != 0 {
		t.Errorf("%d OAuth state rows after only refused connects, want 0", n)
	}
	// The same two rows connect for the admin, so the refusals above were the role's.
	for _, in := range []ConnectInput{{System: "fake", ConnectionID: reconnectable}, {System: "fake", ConnectionID: keyless}} {
		if _, consent, _, err := store.Connect(admin, e1, in); err != nil || consent == "" {
			t.Errorf("control: admin Connect %+v = %q, %v, want a consent URL", in, consent, err)
		}
	}
}

// --- AC 2, 3: connect and its events ---------------------------------------------------

func TestStore_ConnectAndDisconnectRecordTheirEvents(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1 := seedClient(t, super, tenant, "Client One")

	c, _, _, err := store.Connect(admin, e1, ConnectInput{System: "fake"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := store.Disconnect(admin, e1, c.ID); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	// A reconnect re-authorises the same row and records nothing: authorised comes with the callback.
	if _, _, _, err := store.Connect(admin, e1, ConnectInput{System: "fake", ConnectionID: c.ID}); err != nil {
		t.Fatalf("reconnect: %v", err)
	}

	rows, err := super.Query(context.Background(),
		"SELECT event, actor, payload::text, entity_id::text FROM audit_log WHERE tenant_id = $1 AND event LIKE 'erp.connection.%' ORDER BY id", tenant)
	if err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	defer rows.Close()
	var events []string
	for rows.Next() {
		var event, actor, payload string
		var entity *string
		if err := rows.Scan(&event, &actor, &payload, &entity); err != nil {
			t.Fatalf("scan: %v", err)
		}
		events = append(events, event)
		var body map[string]any
		if err := json.Unmarshal([]byte(payload), &body); err != nil {
			t.Fatalf("payload %q: %v", payload, err)
		}
		keys := make([]string, 0, len(body))
		for k := range body {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if !reflect.DeepEqual(keys, []string{"connection_id", "system"}) {
			t.Errorf("%s payload keys = %v, want exactly [connection_id system]", event, keys)
		}
		if body["system"] != "fake" || body["connection_id"] != c.ID {
			t.Errorf("%s payload = %v, want system fake and connection_id %s", event, body, c.ID)
		}
		if actor != subjectOf(admin) {
			t.Errorf("%s actor = %q, want the caller %q", event, actor, subjectOf(admin))
		}
		if entity != nil {
			t.Errorf("%s entity_id = %s, want NULL (workspace-level)", event, *entity)
		}
	}
	want := []string{"erp.connection.created", "erp.connection.revoked"}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("events = %v, want %v in that order", events, want)
	}
}

// --- AC 4: bad input -------------------------------------------------------------------

func TestStore_ConnectRefusesBadInputAndWritesNothing(t *testing.T) {
	super, app := storePools(t)
	tenant, other := seedTenant(t, super, "firm"), seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1 := seedClient(t, super, tenant, "Client One")
	eOther := seedClient(t, super, other, "Foreign Client")
	empty, err := NewRegistry()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}

	cases := []struct {
		name   string
		store  *Store
		entity string
		system string
		want   error // names the clause that refuses: registry or the client lookup
	}{
		{"unknown system (registry lookup)", fakeStore(t, app), e1, "sage", ErrUnknownSystem},
		{"fake with an empty registry (registry lookup)", NewStore(app, empty, nil, nil), e1, "fake", ErrUnknownSystem},
		{"non-UUID client (client lookup)", fakeStore(t, app), "not-a-uuid", "fake", ErrNoSuchRecord},
		{"unknown client (client lookup)", fakeStore(t, app), uuid.NewString(), "fake", ErrNoSuchRecord},
		{"other tenant's client (client lookup, RLS)", fakeStore(t, app), eOther, "fake", ErrNoSuchRecord},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, created, err := c.store.Connect(admin, c.entity, ConnectInput{System: c.system})
			wantErr(t, "Connect", err, c.want)
			if created {
				t.Error("created = true on a refused connect")
			}
		})
	}
	for _, tn := range []string{tenant, other} {
		if c, g, a := connCount(t, super, tn), grantCount(t, super, tn), erpAuditCount(t, super, tn); c+g+a != 0 {
			t.Errorf("tenant %s wrote %d connections, %d grants, %d audit rows, want none", tn, c, g, a)
		}
	}
	if _, _, _, err := fakeStore(t, app).Connect(admin, e1, ConnectInput{System: "fake"}); err != nil {
		t.Errorf("control: the valid input is refused: %v", err)
	}
}

// secondSystem is a registry entry that is not the fake.
type secondSystem struct{ *Fake }

func (secondSystem) System() string { return "second" }

// --- AC 8, 9: disconnect ---------------------------------------------------------------

func TestStore_DisconnectReleasesAnUnsharedGrant(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1, e2 := seedClient(t, super, tenant, "Client One"), seedClient(t, super, tenant, "Client Two")
	_, otherGrant := seedActiveConn(t, super, tenant, e2, "fake-co-2")
	cID, g := seedActiveConn(t, super, tenant, e1, "fake-co-1")
	c := Connection{ID: cID}
	got, err := store.Disconnect(admin, e1, c.ID)
	if err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if got.ID != c.ID || got.Status != "disconnected" {
		t.Errorf("Disconnect = %+v, want %s disconnected", got, c.ID)
	}
	st := stateOf(t, super, c.ID)
	if st.status != "disconnected" || st.grant != nil {
		t.Errorf("row = %+v, want disconnected with grant_id NULL", st)
	}
	if grantExists(t, super, g) {
		t.Error("the connection's own grant still exists")
	}
	if !grantExists(t, super, otherGrant) {
		t.Error("another client's grant was deleted")
	}
}

func TestStore_DisconnectKeepsASharedGrant(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1, e2 := seedClient(t, super, tenant, "Client One"), seedClient(t, super, tenant, "Client Two")
	g := seedGrant(t, super, tenant, "")
	c1 := seedConnRow(t, super, connRow{tenant: tenant, entity: e1, key: "fake-co-1", status: "active", grant: g})
	c2 := seedConnRow(t, super, connRow{tenant: tenant, entity: e2, key: "fake-co-1", status: "active", grant: g})

	if _, err := store.Disconnect(admin, e1, c1); err != nil {
		t.Fatalf("Disconnect c1: %v", err)
	}
	if !grantExists(t, super, g) {
		t.Fatal("the shared grant was deleted while c2 still uses it")
	}
	if s := stateOf(t, super, c2); s.status != "active" || deref(s.grant) != g {
		t.Errorf("c2 = %+v, want active on %s", s, g)
	}
	if s := stateOf(t, super, c1); s.status != "disconnected" || s.grant != nil {
		t.Errorf("c1 = %+v, want disconnected with grant_id NULL", s)
	}
	if _, err := store.Disconnect(admin, e2, c2); err != nil {
		t.Fatalf("Disconnect c2: %v", err)
	}
	if grantExists(t, super, g) {
		t.Error("the grant survives its last connection")
	}
}

func TestStore_DisconnectLeavesTheClientsInvoicesAlone(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1, e2 := seedClient(t, super, tenant, "Client One"), seedClient(t, super, tenant, "Client Two")
	for i := 0; i < 2; i++ {
		mustExec(t, super, "INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3)", tenant, e1, fmt.Sprintf("E1-%d", i))
	}
	for i := 0; i < 3; i++ {
		mustExec(t, super, "INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3)", tenant, e2, fmt.Sprintf("E2-%d", i))
	}
	snapshot := func() string {
		var s string
		mustScan(t, super, "SELECT coalesce(jsonb_agg(to_jsonb(i) ORDER BY i.id), '[]')::text FROM invoices i WHERE i.tenant_id = $1", []any{tenant}, &s)
		return s
	}
	if n := countOf(t, super, "SELECT count(*) FROM invoices WHERE tenant_id = $1", tenant); n != 5 {
		t.Fatalf("seeded %d invoices, want 5", n)
	}
	cID, _ := seedActiveConn(t, super, tenant, e1, "fake-co-1")
	c := Connection{ID: cID}
	before := snapshot()

	if _, err := store.Disconnect(admin, e1, c.ID); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if after := snapshot(); after != before {
		t.Errorf("invoices changed on disconnect:\nbefore %s\nafter  %s", before, after)
	}
	if n := connCount(t, super, tenant); n != 1 {
		t.Errorf("connection rows = %d, want 1 (a disconnect keeps the row)", n)
	}
}

func TestStore_DisconnectPendingWithoutAGrant(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1 := seedClient(t, super, tenant, "Client One")
	unrelated := seedGrant(t, super, tenant, "")
	c := seedConnRow(t, super, connRow{tenant: tenant, entity: e1, status: "pending"})

	got, err := store.Disconnect(admin, e1, c)
	if err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if got.Status != "disconnected" {
		t.Errorf("status = %q, want disconnected", got.Status)
	}
	if s := stateOf(t, super, c); s.status != "disconnected" || s.grant != nil {
		t.Errorf("row = %+v, want disconnected with grant_id NULL", s)
	}
	if n := grantCount(t, super, tenant); n != 1 || !grantExists(t, super, unrelated) {
		t.Errorf("grants = %d (unrelated present %v), want the one unrelated grant untouched", n, grantExists(t, super, unrelated))
	}
	if n := eventCount(t, super, tenant, "erp.connection.revoked"); n != 1 {
		t.Errorf("revoked events = %d, want 1", n)
	}
}

func TestStore_DisconnectScopedToTheClient(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant, other := seedTenant(t, super, "firm"), seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	adminB := seedMember(t, super, other, "admin", "active", true)
	e1, e2 := seedClient(t, super, tenant, "Client One"), seedClient(t, super, tenant, "Client Two")
	eB := seedClient(t, super, other, "Foreign Client")
	c1, _ := seedActiveConn(t, super, tenant, e1, "fake-co-1")
	before := erpState(t, super, tenant)

	cases := []struct {
		name   string
		ctx    context.Context
		entity string
		conn   string
	}{
		{"another client of the tenant (connection lookup by id and entity_id)", admin, e2, c1},
		{"another tenant's admin, the owning client (client lookup, RLS)", adminB, e1, c1},
		{"another tenant's admin, their own client (connection lookup, RLS)", adminB, eB, c1},
		{"unknown connection id", admin, e1, uuid.NewString()},
		{"non-UUID connection id", admin, e1, "not-a-uuid"},
	}
	for _, c := range cases {
		_, err := store.Disconnect(c.ctx, c.entity, c.conn)
		wantErr(t, c.name, err, ErrNoSuchRecord)
	}
	if s := stateOf(t, super, c1); s.status != "active" {
		t.Errorf("c1 status = %q, want active", s.status)
	}
	if got := erpState(t, super, tenant); got != before {
		t.Errorf("ERP rows or audit changed:\nbefore %s\nafter  %s", before, got)
	}
	if _, err := store.Disconnect(admin, e1, c1); err != nil {
		t.Errorf("control: the owning admin cannot disconnect c1: %v", err)
	}
}

func TestStore_DisconnectTwiceIsAlreadyDisconnected(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1 := seedClient(t, super, tenant, "Client One")
	cID, _ := seedActiveConn(t, super, tenant, e1, "fake-co-1")
	c := Connection{ID: cID}
	if _, err := store.Disconnect(admin, e1, c.ID); err != nil {
		t.Fatalf("first Disconnect: %v", err)
	}
	_, err := store.Disconnect(admin, e1, c.ID)
	wantErr(t, "second Disconnect", err, ErrAlreadyDisconnected)
	if n := eventCount(t, super, tenant, "erp.connection.revoked"); n != 1 {
		t.Errorf("revoked events = %d, want 1", n)
	}
}

// --- AC 10, 15, 16, 17: races and the secret_ref tripwire -------------------------------

func TestStore_ConcurrentDisconnectsOfASharedGrantLeaveNoGrant(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admin := bounded(t, seedMember(t, super, tenant, "admin", "active", true))
	for i := 0; i < 10; i++ {
		e1, e2 := seedClient(t, super, tenant, fmt.Sprintf("A%d", i)), seedClient(t, super, tenant, fmt.Sprintf("B%d", i))
		g := seedGrant(t, super, tenant, "")
		c1 := seedConnRow(t, super, connRow{tenant: tenant, entity: e1, key: "fake-co-1", status: "active", grant: g})
		c2 := seedConnRow(t, super, connRow{tenant: tenant, entity: e2, key: "fake-co-1", status: "active", grant: g})

		errs := race(
			func() error { _, err := store.Disconnect(admin, e1, c1); return err },
			func() error { _, err := store.Disconnect(admin, e2, c2); return err },
		)
		for j, err := range errs {
			if err != nil {
				t.Errorf("round %d disconnect %d: %v (a 42501 means a row lock on erp_grants, a 40P01 a lock-order deadlock)", i, j, err)
			}
		}
		if grantExists(t, super, g) {
			t.Errorf("round %d: grant %s survives both disconnects", i, g)
		}
	}
	if n := grantCount(t, super, tenant); n != 0 {
		t.Errorf("grants left = %d, want 0", n)
	}
}

func TestStore_ConcurrentDisconnectsOfOneConnection(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admin := bounded(t, seedMember(t, super, tenant, "admin", "active", true))
	e1 := seedClient(t, super, tenant, "Client One")
	cID, _ := seedActiveConn(t, super, tenant, e1, "fake-co-1")
	c := Connection{ID: cID}
	in := ConnectInput{System: "fake", ConnectionID: cID}
	for i := 0; i < 10; i++ {
		errs := race(
			func() error { _, err := store.Disconnect(admin, e1, c.ID); return err },
			func() error { _, err := store.Disconnect(admin, e1, c.ID); return err },
		)
		nilN, dupN := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				nilN++
			case errors.Is(err, ErrAlreadyDisconnected):
				dupN++
			default:
				t.Errorf("round %d: unexpected %v", i, err)
			}
		}
		if nilN != 1 || dupN != 1 {
			t.Errorf("round %d: %d successes and %d ErrAlreadyDisconnected, want 1 and 1", i, nilN, dupN)
		}
		if n := eventCount(t, super, tenant, "erp.connection.revoked"); n != i+1 {
			t.Errorf("round %d: revoked events = %d, want %d (one per round)", i, n, i+1)
		}
		if n := grantCount(t, super, tenant); n != 0 {
			t.Errorf("round %d: grants = %d, want 0 after the disconnect", i, n)
		}
		if _, _, _, err := store.Connect(admin, e1, in); err != nil {
			t.Fatalf("round %d: reconnect: %v", i, err)
		}
	}
}

func TestStore_ConnectRacingDisconnectOnOneRowSerialises(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admin := bounded(t, seedMember(t, super, tenant, "admin", "active", true))
	e1 := seedClient(t, super, tenant, "Client One")
	c := seedConnRow(t, super, connRow{tenant: tenant, entity: e1, status: "pending"})
	in := ConnectInput{System: "fake", ConnectionID: c}
	for i := 0; i < 20; i++ {
		errs := race(
			func() error { _, err := store.Disconnect(admin, e1, c); return err },
			func() error { _, _, _, err := store.Connect(admin, e1, in); return err },
		)
		for j, err := range errs {
			if err != nil {
				t.Errorf("round %d call %d: %v, want nil from either order", i, j, err)
			}
		}
		if s := stateOf(t, super, c); s.status != "pending" && s.status != "disconnected" {
			t.Errorf("round %d: status %q, want pending or disconnected", i, s.status)
		}
		if n := countOf(t, super, "SELECT count(*) FROM erp_oauth_states WHERE connection_id = $1", c); n > 1 {
			t.Errorf("round %d: %d state rows, want at most the newest", i, n)
		}
		if _, _, _, err := store.Connect(admin, e1, in); err != nil { // back to pending for the next round
			t.Fatalf("round %d: reset: %v", i, err)
		}
	}
}

// --- AC 11, 18: list -------------------------------------------------------------------

func TestStore_ListReturnsOnlyTheClientsConnections(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1, e2 := seedClient(t, super, tenant, "Client One"), seedClient(t, super, tenant, "Client Two")
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	at := func(d time.Duration) *time.Time { v := base.Add(d); return &v }
	active := func(entity, key, id string, created *time.Time) string {
		g := seedGrant(t, super, tenant, "")
		return seedConnRow(t, super, connRow{id: id, tenant: tenant, entity: entity, key: key, status: "active", grant: g, createdAt: created})
	}

	// e1: the later row has the smaller id and is inserted first, so created_at, id, and heap order all differ.
	a := seedConnRow(t, super, connRow{id: "00000000-0000-4000-8000-000000000001", tenant: tenant, entity: e1, key: "fake-co-1", status: "disconnected", createdAt: at(2 * time.Minute)})
	b := active(e1, "fake-co-2", "ffffffff-0000-4000-8000-000000000001", at(time.Minute))
	// e2: x2 and x1 tie on created_at, so id decides between them; x3 is later with the smallest id.
	x1 := active(e2, "fake-co-1", "aaaaaaaa-0000-4000-8000-000000000001", at(0))
	x3 := active(e2, "fake-co-3", "00000000-0000-4000-8000-000000000002", at(3*time.Minute))
	x2 := active(e2, "fake-co-2", "55555555-0000-4000-8000-000000000001", at(0))

	got1, err := store.List(admin, e1)
	if err != nil {
		t.Fatalf("List(e1): %v", err)
	}
	if want := []string{b, a}; !reflect.DeepEqual(idsOf(got1), want) {
		t.Errorf("List(e1) = %v, want %v (created_at, then id)", idsOf(got1), want)
	}
	for _, c := range got1 {
		if c.EntityID != e1 {
			t.Errorf("List(e1) returned %s of client %s", c.ID, c.EntityID)
		}
	}
	if len(got1) == 2 && (got1[0].Status != "active" || got1[1].Status != "disconnected") {
		t.Errorf("List(e1) statuses = %s, %s, want active then disconnected (every status is listed)", got1[0].Status, got1[1].Status)
	}
	got2, err := store.List(admin, e2)
	if err != nil {
		t.Fatalf("List(e2): %v", err)
	}
	if want := []string{x2, x1, x3}; !reflect.DeepEqual(idsOf(got2), want) {
		t.Errorf("List(e2) = %v, want %v (a created_at tie falls to id)", idsOf(got2), want)
	}
}

func TestStore_SystemsFollowsTheRegistry(t *testing.T) {
	one, err := NewRegistry(NewFake(testRedirect))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	two, err := NewRegistry(secondSystem{NewFake(testRedirect)}, NewFake(testRedirect))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	none, err := NewRegistry()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	for _, c := range []struct {
		name string
		reg  Registry
		want []string
	}{
		{"fake only", one, []string{"fake"}},
		{"two, sorted", two, []string{"fake", "second"}},
		{"empty is [] not null", none, []string{}},
	} {
		if got := NewStore(nil, c.reg, nil, nil).Systems(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Systems() = %#v, want %#v", c.name, got, c.want)
		}
	}
}

// Unequal counts (2 and 3) make a list that drops its client filter show in the ids.
func TestStore_EveryActiveMemberListsAClientTheySee(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	for _, kind := range []string{"firm", "in_house"} {
		t.Run(kind, func(t *testing.T) {
			tenant := seedTenant(t, super, kind)
			admin := seedMember(t, super, tenant, "admin", "active", true)
			e1, e2 := seedClient(t, super, tenant, "Client One"), seedClient(t, super, tenant, "Client Two")
			base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
			later, earlier := base.Add(time.Minute), base
			g := seedGrant(t, super, tenant, "")
			a := seedConnRow(t, super, connRow{tenant: tenant, entity: e1, key: "fake-co-1", status: "disconnected", createdAt: &later})
			b := seedConnRow(t, super, connRow{tenant: tenant, entity: e1, key: "fake-co-2", status: "active", grant: g, createdAt: &earlier})
			for _, k := range []string{"fake-co-1", "fake-co-2", "fake-co-3"} {
				seedConnRow(t, super, connRow{tenant: tenant, entity: e2, key: k, status: "disconnected"})
			}

			callers := map[string]context.Context{
				"preparer": seedMember(t, super, tenant, "preparer", "active", true),
				"reviewer": seedMember(t, super, tenant, "reviewer", "active", true),
			}
			if kind == "firm" {
				callers["preparer with a client set"] = seedMember(t, super, tenant, "preparer", "active", false, e1, e2)
				callers["reviewer limited to the client"] = seedMember(t, super, tenant, "reviewer", "active", false, e1)
			}
			ref, err := store.List(admin, e1)
			if err != nil {
				t.Fatalf("admin List: %v", err)
			}
			want := []string{b, a}
			if !reflect.DeepEqual(idsOf(ref), want) {
				t.Fatalf("admin List = %v, want %v", idsOf(ref), want)
			}
			auditBefore := erpAuditCount(t, super, tenant)
			for name, ctx := range callers {
				got, err := store.List(ctx, e1)
				if err != nil {
					t.Errorf("%s List: %v", name, err)
					continue
				}
				if !reflect.DeepEqual(idsOf(got), want) {
					t.Errorf("%s List = %v, want the admin's %v", name, idsOf(got), want)
				}
			}
			if n := erpAuditCount(t, super, tenant); n != auditBefore {
				t.Errorf("audit rows %d -> %d, a list must write none", auditBefore, n)
			}
		})
	}
}

func TestStore_ListOutsideTheCallersClientScopeIsNotFound(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant, other := seedTenant(t, super, "firm"), seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1, e2 := seedClient(t, super, tenant, "Client One"), seedClient(t, super, tenant, "Client Two")
	eOther := seedClient(t, super, other, "Foreign Client")
	c1, _ := seedActiveConn(t, super, tenant, e1, "fake-co-1")
	c2, _ := seedActiveConn(t, super, tenant, e2, "fake-co-1")
	limited := seedMember(t, super, tenant, "reviewer", "active", false, e1)
	unassigned := seedMember(t, super, tenant, "preparer", "active", false)

	// The admin sees e2, so only the client-scope clause can refuse the limited member.
	if got, err := store.List(admin, e2); err != nil || !reflect.DeepEqual(idsOf(got), []string{c2}) {
		t.Fatalf("control: admin List(e2) = %v, %v, want [%s]", idsOf(got), err, c2)
	}
	if got, err := store.List(limited, e1); err != nil || !reflect.DeepEqual(idsOf(got), []string{c1}) {
		t.Errorf("control: limited List(e1) = %v, %v, want only [%s]", idsOf(got), err, c1)
	}
	_, err := store.List(limited, e2)
	wantErr(t, "limited member, a client outside the set", err, ErrNoSuchRecord)
	_, err = store.List(unassigned, e1)
	wantErr(t, "member with no clients", err, ErrNoSuchRecord)
	_, err = store.List(limited, uuid.NewString())
	wantErr(t, "limited member, unknown client", err, ErrNoSuchRecord)
	_, err = store.List(limited, eOther)
	wantErr(t, "limited member, another tenant's client", err, ErrNoSuchRecord)
	_, err = store.List(limited, "not-a-uuid")
	wantErr(t, "limited member, non-UUID client", err, ErrNoSuchRecord)
}

// --- AC 13, 14 and state row 15: no identity, locked workspace -------------------------

func TestStore_NoIdentityIsErrNoTenant(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	e1 := seedClient(t, super, tenant, "Client One")
	c1, _ := seedActiveConn(t, super, tenant, e1, "fake-co-1")
	before := erpState(t, super, tenant)

	for _, call := range allCalls(store, e1, c1) {
		wantErr(t, call.name, call.fn(context.Background()), db.ErrNoTenant)
	}
	if got := erpState(t, super, tenant); got != before {
		t.Errorf("ERP rows or audit changed:\nbefore %s\nafter  %s", before, got)
	}
}

func TestStore_LockedWorkspaceRefusesWritesOnly(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1 := seedClient(t, super, tenant, "Client One")
	c1, g1 := seedActiveConn(t, super, tenant, e1, "fake-co-1")
	reconnectable := seedConnRow(t, super, connRow{tenant: tenant, entity: e1, key: "fake-co-2", status: "disconnected"})
	lockWorkspace(t, super, tenant, subjectOf(admin))
	before := erpState(t, super, tenant)

	for _, in := range []ConnectInput{{System: "fake"}, {System: "fake", ConnectionID: reconnectable}} {
		_, consent, created, err := store.Connect(admin, e1, in)
		wantErr(t, fmt.Sprintf("Connect %+v", in), err, db.ErrWorkspaceLocked)
		if created || consent != "" {
			t.Errorf("Connect %+v in a locked workspace: created %v, consent URL %q", in, created, consent)
		}
	}
	if n := countOf(t, super, "SELECT count(*) FROM erp_oauth_states WHERE tenant_id = $1", tenant); n != 0 {
		t.Errorf("%d OAuth state rows in a locked workspace, want 0", n)
	}
	_, err := store.Disconnect(admin, e1, c1)
	wantErr(t, "Disconnect", err, db.ErrWorkspaceLocked)
	if got := erpState(t, super, tenant); got != before {
		t.Errorf("ERP rows or audit changed in a locked workspace:\nbefore %s\nafter  %s", before, got)
	}
	if s := stateOf(t, super, c1); s.status != "active" || deref(s.grant) != g1 {
		t.Errorf("connection = %+v, want active on %s", s, g1)
	}
	if err := store.Gate(admin); err != nil {
		t.Errorf("Gate in a locked workspace = %v, want nil (read-only seam)", err)
	}
	got, err := store.List(admin, e1)
	gotIDs, wantIDs := idsOf(got), []string{c1, reconnectable}
	sort.Strings(gotIDs)
	sort.Strings(wantIDs)
	if err != nil || !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("List in a locked workspace = %v, %v, want %v", gotIDs, err, wantIDs)
	}
}

func TestStore_MemberListReadsThroughTheReadOnlySeam(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	preparer := seedMember(t, super, tenant, "preparer", "active", true)
	e1 := seedClient(t, super, tenant, "Client One")
	c1, _ := seedActiveConn(t, super, tenant, e1, "fake-co-1")
	lockWorkspace(t, super, tenant, subjectOf(admin))
	before := erpState(t, super, tenant)

	got, err := store.List(preparer, e1)
	if err != nil || !reflect.DeepEqual(idsOf(got), []string{c1}) {
		t.Errorf("preparer List in a locked workspace = %v, %v, want [%s]", idsOf(got), err, c1)
	}
	// The seam refuses the read-write transaction before requireAdmin runs.
	_, _, _, err = store.Connect(preparer, e1, ConnectInput{System: "fake"})
	wantErr(t, "control: preparer Connect", err, db.ErrWorkspaceLocked)
	if got := erpState(t, super, tenant); got != before {
		t.Errorf("ERP rows or audit changed:\nbefore %s\nafter  %s", before, got)
	}
}

// sqlTracer calls onStart before a statement is sent and onEnd once it has finished.
type sqlTracer struct {
	onStart func(sql string)
	onEnd   func(sql string)
}

type sqlKey struct{}

func (s sqlTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if s.onStart != nil {
		s.onStart(d.SQL)
	}
	return context.WithValue(ctx, sqlKey{}, d.SQL)
}

func (s sqlTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if s.onEnd != nil {
		sql, _ := ctx.Value(sqlKey{}).(string)
		s.onEnd(sql)
	}
}

// lossyConn fails its next Read once lost is set, after the server has had time to apply what was written.
type lossyConn struct {
	net.Conn
	lost *atomic.Bool
}

func (c lossyConn) Read(p []byte) (int, error) {
	if c.lost.CompareAndSwap(true, false) {
		time.Sleep(300 * time.Millisecond)
		return 0, io.ErrUnexpectedEOF
	}
	return c.Conn.Read(p)
}

// tracedAppPool is the app pool with a tracer and, when lost is set, a dialer whose connections can lose a reply.
func tracedAppPool(t *testing.T, tr pgx.QueryTracer, lost *atomic.Bool) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("erp store test skipped: DATABASE_URL unset")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	cfg.ConnConfig.Tracer = tr
	if lost != nil {
		cfg.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return lossyConn{Conn: c, lost: lost}, nil
		}
	}
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestStore_ReauthoriseWhileAnotherKeylessPendingExistsIsRefused(t *testing.T) {
	super, app := storePools(t)
	store := fakeStore(t, app)
	tenant := seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1 := seedClient(t, super, tenant, "Client One")

	a, _, _, err := store.Connect(admin, e1, ConnectInput{System: "fake"})
	if err != nil {
		t.Fatalf("connect A: %v", err)
	}
	if _, err := store.Disconnect(admin, e1, a.ID); err != nil {
		t.Fatalf("disconnect A: %v", err)
	}
	b, _, created, err := store.Connect(admin, e1, ConnectInput{System: "fake"})
	if err != nil || !created || b.ID == a.ID {
		t.Fatalf("connect B = %+v created %v err %v, want a new row", b, created, err)
	}
	before := erpState(t, super, tenant)

	_, _, _, err = store.Connect(admin, e1, ConnectInput{System: "fake", ConnectionID: a.ID})
	wantErr(t, "re-authorise A beside pending B", err, ErrKeylessPending)
	if after := erpState(t, super, tenant); after != before {
		t.Errorf("a refused re-authorise changed state:\nbefore %s\nafter  %s", before, after)
	}
}

func TestStore_ConnectRetriesWhenTheConflictingRowIsGoneBeforeTheSelect(t *testing.T) {
	super, _ := storePools(t)
	tenant := seedTenant(t, super, "firm")
	admin := seedMember(t, super, tenant, "admin", "active", true)
	e1 := seedClient(t, super, tenant, "Client One")
	p := seedConnRow(t, super, connRow{tenant: tenant, entity: e1, status: "pending"})
	g := seedGrant(t, super, tenant, "")
	var once sync.Once
	pool := tracedAppPool(t, sqlTracer{onEnd: func(sql string) {
		if !strings.Contains(sql, "ON CONFLICT") {
			return
		}
		// A callback activates the conflicting row between the insert and the select.
		once.Do(func() {
			mustExec(t, super, "UPDATE erp_connections SET status = 'active', external_key = 'k', grant_id = $2 WHERE id = $1", p, g)
		})
	}}, nil)
	store := storeOver(t, pool, nil, NewFake(testRedirect))

	conn, _, created, err := store.Connect(admin, e1, ConnectInput{System: "fake"})
	if err != nil {
		t.Fatalf("Connect = %v, want a new pending row", err)
	}
	if !created || conn.ID == p || conn.Status != "pending" {
		t.Errorf("Connect = %s %s created %v, want a new pending row beside active %s", conn.ID, conn.Status, created, p)
	}
}
