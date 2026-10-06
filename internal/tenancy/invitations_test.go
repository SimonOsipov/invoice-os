package tenancy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// invWorld is one tenant with one active admin, "Ada Obi" unless named otherwise.
type invWorld struct {
	super, app *pgxpool.Pool
	tenant     string
	admin      string
	store      *Store
}

func newInvWorld(t *testing.T, tenantName, adminName string) invWorld {
	t.Helper()
	super, app := dbTestPools(t)
	w := invWorld{super: super, app: app, store: NewStore(app)}
	w.tenant = seedTenant(t, super, tenantName)
	w.admin = w.addMember(t, "admin", "active", adminName)
	return w
}

// addMember seeds a membership; a name is stored as the display name, with a unique email.
func (w invWorld) addMember(t *testing.T, role, status, name string) string {
	t.Helper()
	id := uuid.NewString()
	var dn *string
	if name != "" {
		dn = &name
	}
	seedIdentityMembership(t, w.super, w.tenant, id, role, status, dn, strp(id+"@members.test"))
	return id
}

func (w invWorld) as(userID string) context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{Subject: userID, Role: "authenticated", TenantID: w.tenant})
}

func (w invWorld) adminCtx() context.Context { return w.as(w.admin) }

type invRow struct {
	ID, Email, Role, Status, SendStatus, InvitedBy string
	Hash                                           []byte
	Expires                                        time.Time
}

// invRows reads a tenant's invitations as superuser; where is an extra predicate over $2...
func invRows(t *testing.T, super *pgxpool.Pool, tenant, where string, args ...any) []invRow {
	t.Helper()
	q := `SELECT id::text, invitee_email, role, status, send_status, coalesce(invited_by::text, ''),
	             coalesce(token_hash, ''::bytea), coalesce(expires_at, 'epoch')
	        FROM invitations WHERE tenant_id = $1`
	if where != "" {
		q += " AND (" + where + ")"
	}
	rows, err := super.Query(context.Background(), q+" ORDER BY created_at, id", append([]any{tenant}, args...)...)
	if err != nil {
		t.Fatalf("read invitations: %v", err)
	}
	defer rows.Close()
	var out []invRow
	for rows.Next() {
		var r invRow
		if err := rows.Scan(&r.ID, &r.Email, &r.Role, &r.Status, &r.SendStatus, &r.InvitedBy, &r.Hash, &r.Expires); err != nil {
			t.Fatalf("scan invitation: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read invitations: %v", err)
	}
	return out
}

// pendingRow returns the one pending row for an address, failing when there is not exactly one.
func pendingRow(t *testing.T, super *pgxpool.Pool, tenant, email string) invRow {
	t.Helper()
	rows := invRows(t, super, tenant, "invitee_email = $2 AND status = 'pending'", email)
	if len(rows) != 1 {
		t.Fatalf("pending rows for %s = %d, want 1", email, len(rows))
	}
	return rows[0]
}

type seedInv struct {
	tenant, email, role, status, sendStatus string
	id, invitedBy                           string
	hash                                    []byte
	expires, createdAt                      time.Time
}

// seedInvitation inserts one invitations row as superuser; empty fields get valid defaults.
func seedInvitation(t *testing.T, super *pgxpool.Pool, s seedInv) invRow {
	t.Helper()
	if s.id == "" {
		s.id = uuid.NewString()
	}
	if s.role == "" {
		s.role = "preparer"
	}
	if s.status == "" {
		s.status = "pending"
	}
	if s.sendStatus == "" {
		s.sendStatus = "sent"
	}
	if s.invitedBy == "" {
		s.invitedBy = uuid.NewString()
	}
	if s.hash == nil {
		h := sha256.Sum256([]byte(uuid.NewString()))
		s.hash = h[:]
	}
	if s.expires.IsZero() {
		s.expires = time.Now().Add(7 * 24 * time.Hour)
	}
	if s.createdAt.IsZero() {
		s.createdAt = time.Now()
	}
	if _, err := super.Exec(context.Background(),
		`INSERT INTO invitations (id, tenant_id, role, invitee_email, status, send_status, invited_by, token_hash, expires_at, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		s.id, s.tenant, s.role, s.email, s.status, s.sendStatus, s.invitedBy, s.hash, s.expires, s.createdAt); err != nil {
		t.Fatalf("seed invitation %s: %v", s.email, err)
	}
	return invRow{ID: s.id, Email: s.email, Role: s.role, Status: s.status, SendStatus: s.sendStatus, InvitedBy: s.invitedBy, Hash: s.hash, Expires: s.expires}
}

// seedAuditEvents inserts n audit rows aged age (a Postgres interval) as superuser.
func seedAuditEvents(t *testing.T, super *pgxpool.Pool, tenant, event, payload, age string, n int) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO audit_log (tenant_id, actor, event, payload, created_at)
		 SELECT $1::uuid, 'seed', $2, $3::jsonb, now() - $4::interval FROM generate_series(1, $5::int)`,
		tenant, event, payload, age, n); err != nil {
		t.Fatalf("seed audit events: %v", err)
	}
}

const (
	sentPayload      = `{"invitation_id":"00000000-0000-0000-0000-000000000001","role":"preparer"}`
	resentCounted    = `{"invitation_id":"00000000-0000-0000-0000-000000000001","role":"preparer","counted":true}`
	resentNotCounted = `{"invitation_id":"00000000-0000-0000-0000-000000000001","role":"preparer","counted":false}`
)

// inviteAudit counts a tenant's invitation.% audit rows as superuser.
func inviteAudit(t *testing.T, super *pgxpool.Pool, tenant string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event LIKE 'invitation.%'`, tenant).Scan(&n); err != nil {
		t.Fatalf("count invitation audit rows: %v", err)
	}
	return n
}

func countInvitations(t *testing.T, super *pgxpool.Pool, tenant string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(), `SELECT count(*) FROM invitations WHERE tenant_id = $1`, tenant).Scan(&n); err != nil {
		t.Fatalf("count invitations: %v", err)
	}
	return n
}

// auditPayloads returns every payload of one event in a tenant, decoded.
func auditPayloads(t *testing.T, super *pgxpool.Pool, tenant, event string) []map[string]any {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT payload FROM audit_log WHERE tenant_id = $1 AND event = $2 ORDER BY id`, tenant, event)
	if err != nil {
		t.Fatalf("read audit payloads: %v", err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan payload: %v", err)
		}
		m := map[string]any{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode payload %s: %v", raw, err)
		}
		out = append(out, m)
	}
	return out
}

func sha(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func addrs(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%d@x.test", prefix, i+1)
	}
	return out
}

func mustIssue(t *testing.T, ctx context.Context, s *Store, emails []string, role string) []IssuedInvite {
	t.Helper()
	got, err := s.CreateInvitations(ctx, emails, role)
	if err != nil {
		t.Fatalf("CreateInvitations(%v): %v", emails, err)
	}
	if len(got) != len(emails) {
		t.Fatalf("CreateInvitations(%v) returned %d invites, want %d", emails, len(got), len(emails))
	}
	return got
}

func near(t *testing.T, what string, got, want time.Time, tol time.Duration) {
	t.Helper()
	if d := got.Sub(want); d < -tol || d > tol {
		t.Errorf("%s = %v, want within %v of %v", what, got, tol, want)
	}
}

func inviteExpiry() time.Time {
	return time.Now().Add(accountmail.InviteValidDays * 24 * time.Hour)
}

func TestInvitations_AdminIssuesPendingInvites(t *testing.T) {
	w := newInvWorld(t, "Obi Partners", "Ada Obi")

	got := mustIssue(t, w.adminCtx(), w.store, []string{"b@x.test", "c@x.test"}, "preparer")

	for i, email := range []string{"b@x.test", "c@x.test"} {
		inv := got[i]
		if inv.Email != email || inv.Role != "preparer" {
			t.Errorf("result %d email/role = %q/%q, want %q/preparer", i, inv.Email, inv.Role, email)
		}
		if inv.Workspace != "Obi Partners" || inv.Inviter != "Ada Obi" {
			t.Errorf("result %d workspace/inviter = %q/%q, want Obi Partners/Ada Obi", i, inv.Workspace, inv.Inviter)
		}
		near(t, "result expiry", inv.ExpiresAt, inviteExpiry(), time.Minute)
		if !slices.Equal(inv.TokenHash, sha(inv.Token)) {
			t.Errorf("result %d TokenHash is not sha256(Token)", i)
		}

		row := pendingRow(t, w.super, w.tenant, email)
		if row.ID != inv.ID {
			t.Errorf("row id = %s, result id = %s", row.ID, inv.ID)
		}
		if !slices.Equal(row.Hash, sha(inv.Token)) {
			t.Errorf("stored token_hash for %s is not sha256(token)", email)
		}
		near(t, "stored expires_at", row.Expires, inviteExpiry(), time.Minute)
		if row.InvitedBy != w.admin || row.Role != "preparer" || row.SendStatus != "sending" {
			t.Errorf("row %s invited_by/role/send_status = %s/%s/%s, want %s/preparer/sending", email, row.InvitedBy, row.Role, row.SendStatus, w.admin)
		}
	}
	if n := len(invRows(t, w.super, w.tenant, "")); n != 2 {
		t.Errorf("invitations rows = %d, want 2", n)
	}
}

func TestInvitations_TokenIsRandomAndNeverStored(t *testing.T) {
	w := newInvWorld(t, "Token Tenant", "Ada Obi")
	ctx := w.adminCtx()

	issued := mustIssue(t, ctx, w.store, []string{"a@x.test", "b@x.test"}, "reviewer")
	issued = append(issued, mustIssue(t, ctx, w.store, append([]string{"c@x.test"}, addrs("m", 17)...), "reviewer")...)

	re := regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	seen := map[string]bool{}
	for _, inv := range issued {
		if !re.MatchString(inv.Token) {
			t.Errorf("token %q is not 43 base64url characters", inv.Token)
		}
		if seen[inv.Token] {
			t.Errorf("token %q was issued twice", inv.Token)
		}
		seen[inv.Token] = true
	}

	var rowsText, auditText string
	if err := w.super.QueryRow(context.Background(),
		`SELECT coalesce(string_agg(row_to_json(i)::text, ' '), '') FROM invitations i WHERE tenant_id = $1`, w.tenant).Scan(&rowsText); err != nil {
		t.Fatal(err)
	}
	if err := w.super.QueryRow(context.Background(),
		`SELECT coalesce(string_agg(payload::text || actor || event, ' '), '') FROM audit_log WHERE tenant_id = $1`, w.tenant).Scan(&auditText); err != nil {
		t.Fatal(err)
	}
	if rowsText == "" {
		t.Fatal("no invitation rows to search for the token")
	}
	for _, inv := range issued {
		if strings.Contains(rowsText, inv.Token) {
			t.Errorf("an invitations column holds the token text %q", inv.Token)
		}
		if strings.Contains(auditText, inv.Token) {
			t.Errorf("an audit row holds the token text %q", inv.Token)
		}
	}
}

func TestInvitations_NonAdminIsRefused(t *testing.T) {
	cases := []struct {
		name string
		role string
		stat string
		want error
	}{
		{"preparer", "preparer", "active", ErrInviteNotPermitted},
		{"reviewer", "reviewer", "active", ErrInviteNotPermitted},
		{"suspended admin", "admin", "suspended", db.ErrNotActiveMember},
		{"no membership", "", "", db.ErrNotActiveMember},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newInvWorld(t, "Refusal Tenant", "Ada Obi")
			caller := uuid.NewString()
			if c.role != "" {
				caller = w.addMember(t, c.role, c.stat, "Caller")
			}

			got, err := w.store.CreateInvitations(w.as(caller), []string{"b@x.test"}, "preparer")
			if !errors.Is(err, c.want) {
				t.Fatalf("CreateInvitations err = %v, want %v", err, c.want)
			}
			if len(got) != 0 {
				t.Errorf("refused call returned %d invites", len(got))
			}
			if n := countInvitations(t, w.super, w.tenant); n != 0 {
				t.Errorf("invitations rows after a refusal = %d, want 0", n)
			}
			if n := inviteAudit(t, w.super, w.tenant); n != 0 {
				t.Errorf("invitation audit rows after a refusal = %d, want 0", n)
			}

			// Control: the same tenant still lets the admin invite, so the zero counts above are not a dead store.
			mustIssue(t, w.adminCtx(), w.store, []string{"b@x.test"}, "preparer")
			if n := countInvitations(t, w.super, w.tenant); n != 1 {
				t.Errorf("invitations rows after the admin control = %d, want 1", n)
			}
		})
	}
}

func TestInvitations_ReinvitingAPendingAddressReissues(t *testing.T) {
	w := newInvWorld(t, "Reissue Tenant", "Ada Obi")
	adminB := w.addMember(t, "admin", "active", "Bola Eze")

	first := mustIssue(t, w.adminCtx(), w.store, []string{"b@x.test"}, "reviewer")[0]
	old := pendingRow(t, w.super, w.tenant, "b@x.test")
	// Push the expiry back so "later" is measurable inside one test run.
	if _, err := w.super.Exec(context.Background(),
		`UPDATE invitations SET expires_at = now() + interval '1 day', send_status = 'sent' WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}

	second := mustIssue(t, w.as(adminB), w.store, []string{"b@x.test"}, "admin")[0]

	if second.ID != first.ID {
		t.Errorf("reissue id = %s, want the same id %s", second.ID, first.ID)
	}
	if second.Inviter != "Bola Eze" || second.Role != "admin" {
		t.Errorf("reissue inviter/role = %q/%q, want Bola Eze/admin", second.Inviter, second.Role)
	}
	row := pendingRow(t, w.super, w.tenant, "b@x.test")
	if row.ID != first.ID {
		t.Errorf("pending row id = %s, want %s", row.ID, first.ID)
	}
	if slices.Equal(row.Hash, old.Hash) || !slices.Equal(row.Hash, sha(second.Token)) {
		t.Error("reissue did not store the new token's hash")
	}
	if n := len(invRows(t, w.super, w.tenant, "token_hash = $2", sha(first.Token))); n != 0 {
		t.Errorf("rows matching the first token's hash = %d, want 0", n)
	}
	if !row.Expires.After(time.Now().Add(24*time.Hour + time.Hour)) {
		t.Errorf("reissue expires_at = %v, want later than the old one (+1 day)", row.Expires)
	}
	near(t, "reissue expires_at", row.Expires, inviteExpiry(), time.Minute)
	if row.Role != "admin" || row.InvitedBy != adminB || row.SendStatus != "sending" {
		t.Errorf("reissued row role/invited_by/send_status = %s/%s/%s, want admin/%s/sending", row.Role, row.InvitedBy, row.SendStatus, adminB)
	}
	if n := countInvitations(t, w.super, w.tenant); n != 1 {
		t.Errorf("invitations rows = %d, want 1", n)
	}
	if n := len(auditPayloads(t, w.super, w.tenant, "invitation.sent")); n != 2 {
		t.Errorf("invitation.sent rows = %d, want 2 (one per issue)", n)
	}
}

// Normalising is the handler's job (RESEND-05-06); the store must match the stored lower-case address exactly.
func TestInvitations_AddressIsMatchedAfterNormalising(t *testing.T) {
	w := newInvWorld(t, "Normalise Tenant", "Ada Obi")
	mustIssue(t, w.adminCtx(), w.store, []string{"b@x.test"}, "reviewer")

	mustIssue(t, w.adminCtx(), w.store, []string{strings.ToLower(strings.TrimSpace(" B@X.test "))}, "reviewer")

	if n := len(invRows(t, w.super, w.tenant, "status = 'pending'")); n != 1 {
		t.Errorf("pending rows = %d, want 1", n)
	}
	if n := countInvitations(t, w.super, w.tenant); n != 1 {
		t.Errorf("invitations rows = %d, want 1", n)
	}
}

func TestInvitations_EachInviteIsAudited(t *testing.T) {
	w := newInvWorld(t, "Audit Tenant", "Ada Obi")
	emails := []string{"b@x.test", "c@x.test"}

	issued := mustIssue(t, w.adminCtx(), w.store, emails, "preparer")

	var actors []string
	rows, err := w.super.Query(context.Background(),
		`SELECT actor FROM audit_log WHERE tenant_id = $1 AND event = 'invitation.sent'`, w.tenant)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		actors = append(actors, a)
	}
	rows.Close()
	payloads := auditPayloads(t, w.super, w.tenant, "invitation.sent")
	if len(payloads) != 2 || len(actors) != 2 {
		t.Fatalf("invitation.sent rows = %d (actors %d), want 2", len(payloads), len(actors))
	}
	for _, a := range actors {
		if a != w.admin {
			t.Errorf("audit actor = %q, want the admin %q", a, w.admin)
		}
	}

	ids := map[string]bool{}
	for _, p := range payloads {
		keys := make([]string, 0, len(p))
		for k := range p {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, []string{"invitation_id", "role"}) {
			t.Errorf("payload keys = %v, want exactly [invitation_id role]", keys)
		}
		if p["role"] != "preparer" {
			t.Errorf("payload role = %v, want preparer", p["role"])
		}
		id, _ := p["invitation_id"].(string)
		ids[id] = true
		raw, _ := json.Marshal(p)
		for _, e := range emails {
			if strings.Contains(string(raw), e) {
				t.Errorf("payload %s carries the address %s", raw, e)
			}
		}
	}
	for _, inv := range issued {
		if !ids[inv.ID] {
			t.Errorf("no invitation.sent payload names invitation %s", inv.ID)
		}
	}
}

func TestInvitations_FailureRollsBackEveryAddress(t *testing.T) {
	cases := []struct {
		name   string
		emails []string
		role   string
	}{
		{"unknown role on every address", []string{"b@x.test", "c@x.test"}, "owner"},
		{"capitalised role", []string{"b@x.test"}, "Admin"},
		{"empty role", []string{"b@x.test"}, ""},
		// The first address inserts and audits before the second fails: only a rollback leaves zero rows.
		{"a later address fails after an earlier one was written", []string{"ok@x.test", ""}, "preparer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newInvWorld(t, "Rollback Tenant", "Ada Obi")

			got, err := w.store.CreateInvitations(w.adminCtx(), c.emails, c.role)
			if err == nil {
				t.Fatalf("CreateInvitations(%v, %q) = %d invites and no error, want an error", c.emails, c.role, len(got))
			}
			if errors.Is(err, ErrInviteNotPermitted) || errors.Is(err, ErrDailyInviteLimit) {
				t.Errorf("err = %v, want a database failure, not a policy refusal", err)
			}
			if n := countInvitations(t, w.super, w.tenant); n != 0 {
				t.Errorf("invitations rows after a failed call = %d, want 0", n)
			}
			if n := inviteAudit(t, w.super, w.tenant); n != 0 {
				t.Errorf("invitation audit rows after a failed call = %d, want 0", n)
			}
		})
	}
}

func TestInvitations_DailyLimit(t *testing.T) {
	w := newInvWorld(t, "Limit Tenant", "Ada Obi")
	other := seedTenant(t, w.super, "Other Limit Tenant")
	seedAuditEvents(t, w.super, w.tenant, "invitation.sent", sentPayload, "1 hour", 19)
	seedAuditEvents(t, w.super, w.tenant, "invitation.sent", sentPayload, "25 hours", 5)
	seedAuditEvents(t, w.super, other, "invitation.sent", sentPayload, "1 hour", 30)

	got, err := w.store.CreateInvitations(w.adminCtx(), []string{"b@x.test", "c@x.test"}, "preparer")
	if !errors.Is(err, ErrDailyInviteLimit) {
		t.Fatalf("19 counted + 2 addresses: err = %v, want ErrDailyInviteLimit", err)
	}
	if len(got) != 0 {
		t.Errorf("refused call returned %d invites", len(got))
	}
	if n := countInvitations(t, w.super, w.tenant); n != 0 {
		t.Errorf("invitations rows after the refusal = %d, want 0", n)
	}
	if n := inviteAudit(t, w.super, w.tenant); n != 24 {
		t.Errorf("audit rows after the refusal = %d, want the 24 seeded", n)
	}

	mustIssue(t, w.adminCtx(), w.store, []string{"d@x.test"}, "preparer")
	if _, err := w.store.CreateInvitations(w.adminCtx(), []string{"e@x.test"}, "preparer"); !errors.Is(err, ErrDailyInviteLimit) {
		t.Errorf("the 21st mail in the window: err = %v, want ErrDailyInviteLimit", err)
	}
}

func TestInvitations_DailyLimitCountsResends(t *testing.T) {
	w := newInvWorld(t, "Resend Limit Tenant", "Ada Obi")
	seedAuditEvents(t, w.super, w.tenant, "invitation.sent", sentPayload, "1 hour", 19)
	seedAuditEvents(t, w.super, w.tenant, "invitation.resent", resentCounted, "1 hour", 1)

	if _, err := w.store.CreateInvitations(w.adminCtx(), []string{"b@x.test"}, "preparer"); !errors.Is(err, ErrDailyInviteLimit) {
		t.Fatalf("19 sent + 1 counted resend: err = %v, want ErrDailyInviteLimit", err)
	}
	if n := countInvitations(t, w.super, w.tenant); n != 0 {
		t.Errorf("invitations rows after the refusal = %d, want 0", n)
	}
}

func TestInvitations_DailyLimitWindowEdges(t *testing.T) {
	cases := []struct {
		name    string
		seed    func(t *testing.T, w invWorld)
		refused bool
	}{
		{"19 at 1h and one at 23h59m", func(t *testing.T, w invWorld) {
			seedAuditEvents(t, w.super, w.tenant, "invitation.sent", sentPayload, "1 hour", 19)
			seedAuditEvents(t, w.super, w.tenant, "invitation.sent", sentPayload, "23 hours 59 minutes", 1)
		}, true},
		{"19 at 1h and one at 24h1m", func(t *testing.T, w invWorld) {
			seedAuditEvents(t, w.super, w.tenant, "invitation.sent", sentPayload, "1 hour", 19)
			seedAuditEvents(t, w.super, w.tenant, "invitation.sent", sentPayload, "24 hours 1 minute", 1)
		}, false},
		{"20 at 1h", func(t *testing.T, w invWorld) {
			seedAuditEvents(t, w.super, w.tenant, "invitation.sent", sentPayload, "1 hour", 20)
		}, true},
		{"20 resends marked counted false", func(t *testing.T, w invWorld) {
			seedAuditEvents(t, w.super, w.tenant, "invitation.resent", resentNotCounted, "1 hour", 20)
		}, false},
		{"a resend with no counted key counts", func(t *testing.T, w invWorld) {
			seedAuditEvents(t, w.super, w.tenant, "invitation.sent", sentPayload, "1 hour", 19)
			seedAuditEvents(t, w.super, w.tenant, "invitation.resent", `{"role":"preparer"}`, "1 hour", 1)
		}, true},
		{"10 sent and 10 counted resends", func(t *testing.T, w invWorld) {
			seedAuditEvents(t, w.super, w.tenant, "invitation.sent", sentPayload, "1 hour", 10)
			seedAuditEvents(t, w.super, w.tenant, "invitation.resent", resentCounted, "2 hours", 10)
		}, true},
		{"10 sent, 9 counted resends and 5 uncounted resends", func(t *testing.T, w invWorld) {
			seedAuditEvents(t, w.super, w.tenant, "invitation.sent", sentPayload, "1 hour", 10)
			seedAuditEvents(t, w.super, w.tenant, "invitation.resent", resentCounted, "2 hours", 9)
			seedAuditEvents(t, w.super, w.tenant, "invitation.resent", resentNotCounted, "2 hours", 5)
		}, false},
		{"20 other membership events do not count", func(t *testing.T, w invWorld) {
			seedAuditEvents(t, w.super, w.tenant, "membership.suspended", `{}`, "1 hour", 20)
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newInvWorld(t, "Edge Tenant", "Ada Obi")
			c.seed(t, w)

			got, err := w.store.CreateInvitations(w.adminCtx(), []string{"b@x.test"}, "preparer")
			if c.refused {
				if !errors.Is(err, ErrDailyInviteLimit) {
					t.Fatalf("err = %v, want ErrDailyInviteLimit", err)
				}
				if n := countInvitations(t, w.super, w.tenant); n != 0 {
					t.Errorf("invitations rows after a refusal = %d, want 0", n)
				}
				return
			}
			if err != nil || len(got) != 1 {
				t.Fatalf("got %d invites, err = %v, want one invite", len(got), err)
			}
		})
	}
}

// advisoryLocks counts advisory-lock requests on the tenant's invitation_send key that are (not) granted.
func advisoryLocks(t *testing.T, super *pgxpool.Pool, tenant string, granted bool) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM pg_locks
		  WHERE locktype = 'advisory' AND granted = $2
		    AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		    AND ((classid::bigint << 32) | objid::bigint) = hashtextextended('invitation_send:' || $1::text, 0)`,
		tenant, granted).Scan(&n); err != nil {
		t.Fatalf("read pg_locks: %v", err)
	}
	return n
}

func TestInvitations_ConcurrentCallsShareTheLimit(t *testing.T) {
	w := newInvWorld(t, "Concurrent Tenant", "Ada Obi")
	ctx := context.Background()

	conn, err := w.super.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('invitation_send:' || $1::text, 0))`, w.tenant); err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended('invitation_send:' || $1::text, 0))`, w.tenant); err != nil {
			t.Errorf("unlock: %v", err)
		}
		conn.Release()
	}
	t.Cleanup(release)
	if n := advisoryLocks(t, w.super, w.tenant, true); n != 1 {
		t.Fatalf("pg_locks filter sees %d granted locks on the key, want the test's own 1", n)
	}

	type result struct {
		issued []IssuedInvite
		err    error
	}
	chA, chB := make(chan result, 1), make(chan result, 1)
	call := func(ch chan result, prefix string) {
		got, err := w.store.CreateInvitations(w.adminCtx(), addrs(prefix, 15), "preparer")
		ch <- result{got, err}
	}
	go call(chA, "a")
	go call(chB, "b")

	deadline := time.Now().Add(5 * time.Second)
	for advisoryLocks(t, w.super, w.tenant, false) < 2 {
		select {
		case r := <-chA:
			chA <- r
			t.Fatalf("a call returned while the invitation_send lock was held (err %v)", r.err)
		case r := <-chB:
			chB <- r
			t.Fatalf("a call returned while the invitation_send lock was held (err %v)", r.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("pg_locks shows %d ungranted requests on the invitation_send key after 5 s, want 2", advisoryLocks(t, w.super, w.tenant, false))
		}
		time.Sleep(25 * time.Millisecond)
	}

	release()
	var rs []result
	for _, ch := range []chan result{chA, chB} {
		select {
		case r := <-ch:
			rs = append(rs, r)
		case <-time.After(15 * time.Second):
			t.Fatal("a call did not return after the lock was released")
		}
	}
	ok, limited := 0, 0
	for _, r := range rs {
		switch {
		case r.err == nil && len(r.issued) == 15:
			ok++
		case errors.Is(r.err, ErrDailyInviteLimit):
			limited++
		default:
			t.Errorf("unexpected result: %d invites, err %v", len(r.issued), r.err)
		}
	}
	if ok != 1 || limited != 1 {
		t.Errorf("successes/limit refusals = %d/%d, want 1/1", ok, limited)
	}
	if n := countInvitations(t, w.super, w.tenant); n != 15 {
		t.Errorf("invitations rows = %d, want 15", n)
	}
}

func TestInvitations_ResendInvalidatesTheOldToken(t *testing.T) {
	w := newInvWorld(t, "Resend Tenant", "Ada Obi")
	adminB := w.addMember(t, "admin", "active", "Bola Eze")
	first := mustIssue(t, w.adminCtx(), w.store, []string{"b@x.test"}, "reviewer")[0]
	if _, err := w.super.Exec(context.Background(),
		`UPDATE invitations SET expires_at = now() + interval '1 day', send_status = 'sent' WHERE id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}

	second, err := w.store.ResendInvitation(w.as(adminB), first.ID)
	if err != nil {
		t.Fatalf("ResendInvitation: %v", err)
	}

	if second.ID != first.ID || second.Email != "b@x.test" || second.Role != "reviewer" {
		t.Errorf("resend result id/email/role = %s/%s/%s, want %s/b@x.test/reviewer", second.ID, second.Email, second.Role, first.ID)
	}
	if second.Workspace != "Resend Tenant" {
		t.Errorf("resend workspace = %q", second.Workspace)
	}
	if second.Token == "" || second.Token == first.Token || !slices.Equal(second.TokenHash, sha(second.Token)) {
		t.Error("resend did not mint a new token with a matching hash")
	}
	if n := len(invRows(t, w.super, w.tenant, "token_hash = $2", sha(first.Token))); n != 0 {
		t.Errorf("rows matching the old token's hash = %d, want 0", n)
	}
	rows := invRows(t, w.super, w.tenant, "token_hash = $2", sha(second.Token))
	if len(rows) != 1 {
		t.Fatalf("rows matching the new token's hash = %d, want 1", len(rows))
	}
	row := rows[0]
	if !row.Expires.After(time.Now().Add(25 * time.Hour)) {
		t.Errorf("expires_at = %v, want restarted (later than +1 day)", row.Expires)
	}
	near(t, "resend expires_at", row.Expires, inviteExpiry(), time.Minute)
	if row.SendStatus != "sending" || row.Role != "reviewer" || row.InvitedBy != w.admin {
		t.Errorf("row send_status/role/invited_by = %s/%s/%s, want sending/reviewer/%s (the original inviter)", row.SendStatus, row.Role, row.InvitedBy, w.admin)
	}
	if second.Inviter != "Ada Obi" {
		t.Errorf("resend inviter = %q, want the original inviter Ada Obi", second.Inviter)
	}
	resent := auditPayloads(t, w.super, w.tenant, "invitation.resent")
	if len(resent) != 1 {
		t.Fatalf("invitation.resent rows = %d, want 1", len(resent))
	}
	if resent[0]["counted"] != true || resent[0]["role"] != "reviewer" || resent[0]["invitation_id"] != first.ID {
		t.Errorf("resent payload = %v, want counted true, role reviewer, invitation_id %s", resent[0], first.ID)
	}
	var actor string
	if err := w.super.QueryRow(context.Background(),
		`SELECT actor FROM audit_log WHERE tenant_id = $1 AND event = 'invitation.resent'`, w.tenant).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if actor != adminB {
		t.Errorf("resend actor = %q, want the resending admin %q", actor, adminB)
	}
}

func TestInvitations_ResendCountsTowardTheLimit(t *testing.T) {
	w := newInvWorld(t, "Resend Count Tenant", "Ada Obi")
	target := seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: "b@x.test", invitedBy: w.admin, sendStatus: "sent"})
	seedAuditEvents(t, w.super, w.tenant, "invitation.sent", sentPayload, "1 hour", 19)

	if _, err := w.store.ResendInvitation(w.adminCtx(), target.ID); err != nil {
		t.Fatalf("resend as the 20th mail: %v", err)
	}
	if _, err := w.store.CreateInvitations(w.adminCtx(), []string{"c@x.test"}, "preparer"); !errors.Is(err, ErrDailyInviteLimit) {
		t.Errorf("invite after a counted resend filled the window: err = %v, want ErrDailyInviteLimit", err)
	}
}

func TestInvitations_ExpiredInviteCanBeResent(t *testing.T) {
	w := newInvWorld(t, "Expired Tenant", "Ada Obi")
	target := seedInvitation(t, w.super, seedInv{
		tenant: w.tenant, email: "b@x.test", invitedBy: w.admin, expires: time.Now().Add(-24 * time.Hour),
	})

	got, err := w.store.ResendInvitation(w.adminCtx(), target.ID)
	if err != nil {
		t.Fatalf("resend of an expired invite: %v", err)
	}
	near(t, "result expiry", got.ExpiresAt, inviteExpiry(), time.Minute)
	near(t, "stored expiry", pendingRow(t, w.super, w.tenant, "b@x.test").Expires, inviteExpiry(), time.Minute)
}

func TestInvitations_StuckSendingInviteCanBeResent(t *testing.T) {
	w := newInvWorld(t, "Stuck Tenant", "Ada Obi")
	first := mustIssue(t, w.adminCtx(), w.store, []string{"b@x.test"}, "preparer")[0]
	if st := pendingRow(t, w.super, w.tenant, "b@x.test").SendStatus; st != "sending" {
		t.Fatalf("issued row send_status = %q, want it left at sending", st)
	}

	second, err := w.store.ResendInvitation(w.adminCtx(), first.ID)
	if err != nil {
		t.Fatalf("resend of a sending invite: %v", err)
	}
	if slices.Equal(second.TokenHash, first.TokenHash) {
		t.Error("resend kept the old hash")
	}
	if !slices.Equal(pendingRow(t, w.super, w.tenant, "b@x.test").Hash, second.TokenHash) {
		t.Error("stored hash is not the resent token's")
	}
	resent := auditPayloads(t, w.super, w.tenant, "invitation.resent")
	if len(resent) != 1 || resent[0]["counted"] != true {
		t.Errorf("resent payloads = %v, want one with counted true", resent)
	}
}

func TestInvitations_ResendOfAFailedSendIsFree(t *testing.T) {
	t.Run("at the limit", func(t *testing.T) {
		w := newInvWorld(t, "Free Resend Tenant", "Ada Obi")
		failed := seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: "f@x.test", invitedBy: w.admin, sendStatus: "failed"})
		sent := seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: "s@x.test", invitedBy: w.admin, sendStatus: "sent"})
		seedAuditEvents(t, w.super, w.tenant, "invitation.sent", sentPayload, "1 hour", 20)

		got, err := w.store.ResendInvitation(w.adminCtx(), failed.ID)
		if err != nil {
			t.Fatalf("resend of a failed invite at 20 counted events: %v", err)
		}
		if got.ID != failed.ID || slices.Equal(got.TokenHash, failed.Hash) {
			t.Error("resend did not reissue the failed invite")
		}
		resent := auditPayloads(t, w.super, w.tenant, "invitation.resent")
		if len(resent) != 1 || resent[0]["counted"] != false {
			t.Fatalf("resent payloads = %v, want one with counted false", resent)
		}
		if _, err := w.store.CreateInvitations(w.adminCtx(), []string{"new@x.test"}, "preparer"); !errors.Is(err, ErrDailyInviteLimit) {
			t.Errorf("new address after the free resend: err = %v, want ErrDailyInviteLimit", err)
		}

		// Control: the same resend of a delivered invite is refused and changes nothing.
		if _, err := w.store.ResendInvitation(w.adminCtx(), sent.ID); !errors.Is(err, ErrDailyInviteLimit) {
			t.Fatalf("resend of a sent invite at the limit: err = %v, want ErrDailyInviteLimit", err)
		}
		row := pendingRow(t, w.super, w.tenant, "s@x.test")
		if !slices.Equal(row.Hash, sent.Hash) || row.SendStatus != "sent" {
			t.Error("a refused resend changed the invite")
		}
		if n := len(auditPayloads(t, w.super, w.tenant, "invitation.resent")); n != 1 {
			t.Errorf("invitation.resent rows after the refused resend = %d, want 1", n)
		}
	})

	t.Run("a free resend does not use the budget", func(t *testing.T) {
		w := newInvWorld(t, "Free Budget Tenant", "Ada Obi")
		failed := seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: "f@x.test", invitedBy: w.admin, sendStatus: "failed"})
		seedAuditEvents(t, w.super, w.tenant, "invitation.sent", sentPayload, "1 hour", 19)

		if got, err := w.store.ResendInvitation(w.adminCtx(), failed.ID); err != nil || got.ID != failed.ID {
			t.Fatalf("free resend: id %q, err %v, want a reissue of %s", got.ID, err, failed.ID)
		}
		if got, err := w.store.CreateInvitations(w.adminCtx(), []string{"new@x.test"}, "preparer"); err != nil || len(got) != 1 {
			t.Errorf("the 20th counted mail after a free resend: %d invites, err = %v, want success", len(got), err)
		}
	})
}

func TestInvitations_ResendNamesTheOriginalInviterOrFallsBack(t *testing.T) {
	cases := []struct {
		name   string
		member func(t *testing.T, w invWorld, id string)
		want   string
	}{
		{"display name", func(t *testing.T, w invWorld, id string) {
			seedIdentityMembership(t, w.super, w.tenant, id, "admin", "active", strp("Ada Obi"), strp("ada@obi.test"))
		}, "Ada Obi"},
		{"display name null falls back to email", func(t *testing.T, w invWorld, id string) {
			seedIdentityMembership(t, w.super, w.tenant, id, "admin", "active", nil, strp("ada@obi.test"))
		}, "ada@obi.test"},
		{"suspended inviter is still named", func(t *testing.T, w invWorld, id string) {
			seedIdentityMembership(t, w.super, w.tenant, id, "admin", "suspended", strp("Ada Obi"), strp("ada@obi.test"))
		}, "Ada Obi"},
		{"no membership row", func(t *testing.T, w invWorld, id string) {}, ""},
		{"both null", func(t *testing.T, w invWorld, id string) {
			seedIdentityMembership(t, w.super, w.tenant, id, "admin", "active", nil, nil)
		}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newInvWorld(t, "Inviter Tenant", "Caller Admin")
			inviter := uuid.NewString()
			c.member(t, w, inviter)
			target := seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: "b@x.test", invitedBy: inviter})

			got, err := w.store.ResendInvitation(w.adminCtx(), target.ID)
			if err != nil {
				t.Fatalf("ResendInvitation: %v", err)
			}
			if got.ID != target.ID || got.Token == "" {
				t.Fatalf("resend did not reissue %s: id %q, token %q", target.ID, got.ID, got.Token)
			}
			if got.Inviter != c.want {
				t.Errorf("Inviter = %q, want %q", got.Inviter, c.want)
			}
		})
	}
}

func TestInvitations_ReinviteAfterAcceptedOrRevokedCreatesANewRow(t *testing.T) {
	for _, status := range []string{"accepted", "revoked"} {
		t.Run(status, func(t *testing.T) {
			w := newInvWorld(t, "Reinvite Tenant", "Ada Obi")
			old := seedInvitation(t, w.super, seedInv{
				tenant: w.tenant, email: "b@x.test", status: status, invitedBy: w.admin, createdAt: time.Now().Add(-48 * time.Hour),
			})

			got := mustIssue(t, w.adminCtx(), w.store, []string{"b@x.test"}, "reviewer")[0]

			if got.ID == old.ID {
				t.Fatalf("new invite reused the %s row's id", status)
			}
			if pendingRow(t, w.super, w.tenant, "b@x.test").ID != got.ID {
				t.Error("the pending row is not the new invite")
			}
			rows := invRows(t, w.super, w.tenant, "id = $2", old.ID)
			if len(rows) != 1 {
				t.Fatalf("old row count = %d, want 1", len(rows))
			}
			if rows[0].Status != status || !slices.Equal(rows[0].Hash, old.Hash) || !rows[0].Expires.Equal(old.Expires.Truncate(time.Microsecond)) {
				t.Errorf("old %s row changed: status %q, hash equal %v, expires %v want %v",
					status, rows[0].Status, slices.Equal(rows[0].Hash, old.Hash), rows[0].Expires, old.Expires)
			}
		})
	}
}

func TestInvitations_ResendTargets(t *testing.T) {
	w := newInvWorld(t, "Target Tenant", "Ada Obi")
	other := newInvWorld(t, "Other Target Tenant", "Olu Bee")
	foreign := seedInvitation(t, other.super, seedInv{tenant: other.tenant, email: "f@x.test", invitedBy: other.admin})
	accepted := seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: "a@x.test", status: "accepted", invitedBy: w.admin})
	revoked := seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: "r@x.test", status: "revoked", invitedBy: w.admin})

	cases := []struct {
		name string
		id   string
		want error
	}{
		{"random uuid", uuid.NewString(), ErrInvitationNotFound},
		{"another tenant's invite", foreign.ID, ErrInvitationNotFound},
		{"accepted", accepted.ID, ErrInvitationNotPending},
		{"revoked", revoked.ID, ErrInvitationNotPending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := w.store.ResendInvitation(w.adminCtx(), c.id)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if got.Token != "" {
				t.Error("a refused resend returned a token")
			}
		})
	}
	if n := inviteAudit(t, w.super, w.tenant); n != 0 {
		t.Errorf("invitation audit rows after four refusals = %d, want 0", n)
	}
	if rows := invRows(t, other.super, other.tenant, "id = $2", foreign.ID); len(rows) != 1 || !slices.Equal(rows[0].Hash, foreign.Hash) {
		t.Error("another tenant's invite changed")
	}
}

func TestInvitations_ResendNonAdminRefusedForEveryTarget(t *testing.T) {
	w := newInvWorld(t, "Non-admin Resend Tenant", "Ada Obi")
	other := newInvWorld(t, "Other Non-admin Tenant", "Olu Bee")
	preparer := w.addMember(t, "preparer", "active", "Pat Preparer")
	targets := map[string]string{
		"random uuid":       uuid.NewString(),
		"another tenant":    seedInvitation(t, other.super, seedInv{tenant: other.tenant, email: "f@x.test"}).ID,
		"accepted":          seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: "a@x.test", status: "accepted"}).ID,
		"revoked":           seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: "r@x.test", status: "revoked"}).ID,
		"pending in tenant": seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: "p@x.test"}).ID,
	}
	snapshot := func() string {
		var s string
		for _, tenant := range []string{w.tenant, other.tenant} {
			for _, r := range invRows(t, w.super, tenant, "") {
				s += fmt.Sprintf("%s|%s|%x|%s|%s;", r.ID, r.Status, r.Hash, r.Expires.Format(time.RFC3339Nano), r.SendStatus)
			}
		}
		return s
	}
	before := snapshot()

	for name, id := range targets {
		t.Run(name, func(t *testing.T) {
			if _, err := w.store.ResendInvitation(w.as(preparer), id); !errors.Is(err, ErrInviteNotPermitted) {
				t.Errorf("err = %v, want ErrInviteNotPermitted", err)
			}
		})
	}
	if before == "" || snapshot() != before {
		t.Error("a refused non-admin resend changed an invitations row")
	}
	if n := inviteAudit(t, w.super, w.tenant); n != 0 {
		t.Errorf("invitation audit rows = %d, want 0", n)
	}
}

// seedLegacyPending inserts a pending row with no token, the shape the NOT VALID check leaves behind
// from before the migration. The check is dropped and restored inside one transaction.
func seedLegacyPending(t *testing.T, super *pgxpool.Pool, tenant, email string) {
	t.Helper()
	ctx := context.Background()
	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var def string
	if err := tx.QueryRow(ctx,
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'invitations_pending_has_token'`).Scan(&def); err != nil {
		t.Fatalf("read the pending check: %v", err)
	}
	for _, q := range []string{
		`ALTER TABLE invitations DROP CONSTRAINT invitations_pending_has_token`,
		fmt.Sprintf(`INSERT INTO invitations (tenant_id, role, invitee_email) VALUES ('%s', 'preparer', '%s')`, tenant, email),
		`ALTER TABLE invitations ADD CONSTRAINT invitations_pending_has_token ` + def,
	} {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatalf("legacy seed %q: %v", q, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestInvitations_ListShowsThisTenantsPendingInvites(t *testing.T) {
	w := newInvWorld(t, "List Tenant", "Ada Obi")
	other := newInvWorld(t, "Other List Tenant", "Olu Bee")
	preparer := w.addMember(t, "preparer", "active", "Pat Preparer")
	now := time.Now().Truncate(time.Second)
	// Inserted out of order; p3 and p2 tie on created_at, so only the id breaks the tie (p3 < p2).
	p1 := seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: "p1@x.test", role: "admin", sendStatus: "failed", invitedBy: w.admin,
		createdAt: now.Add(-3 * time.Hour), expires: now.Add(24 * time.Hour)})
	p2 := seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: "p2@x.test", role: "reviewer", sendStatus: "sent", invitedBy: w.admin,
		id: "bbbbbbbb-0000-4000-8000-000000000000", createdAt: now.Add(-1 * time.Hour), expires: now.Add(48 * time.Hour)})
	p3 := seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: "p3@x.test", role: "preparer", sendStatus: "sending", invitedBy: w.admin,
		id: "aaaaaaaa-0000-4000-8000-000000000000", createdAt: now.Add(-1 * time.Hour), expires: now.Add(72 * time.Hour)})
	seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: "gone@x.test", status: "revoked", invitedBy: w.admin})
	seedLegacyPending(t, w.super, w.tenant, "legacy@x.test")
	seedInvitation(t, other.super, seedInv{tenant: other.tenant, email: "other@x.test", invitedBy: other.admin})

	got, err := w.store.ListInvitations(w.adminCtx())
	if err != nil {
		t.Fatalf("ListInvitations: %v", err)
	}
	want := []invRow{p1, p3, p2}
	if len(got) != len(want) {
		var emails []string
		for _, g := range got {
			emails = append(emails, g.Email)
		}
		t.Fatalf("listed %d invitations %v, want 3 (p1, p3, p2)", len(got), emails)
	}
	for i, wr := range want {
		g := got[i]
		if g.ID != wr.ID || g.Email != wr.Email || g.Role != wr.Role || g.Status != "pending" ||
			g.InvitedBy != w.admin || g.Delivery != wr.SendStatus {
			t.Errorf("row %d = %+v, want id %s email %s role %s delivery %s invited_by %s", i, g, wr.ID, wr.Email, wr.Role, wr.SendStatus, w.admin)
		}
		near(t, fmt.Sprintf("row %d expires_at", i), g.ExpiresAt, wr.Expires, time.Second)
		if g.CreatedAt.IsZero() {
			t.Errorf("row %d created_at is zero", i)
		}
	}
	near(t, "p1 created_at", got[0].CreatedAt, now.Add(-3*time.Hour), time.Second)

	if _, err := w.store.ListInvitations(w.as(preparer)); !errors.Is(err, ErrInviteNotPermitted) {
		t.Errorf("preparer list err = %v, want ErrInviteNotPermitted", err)
	}
}

func TestInvitations_DeliveryIsRecordedForTheCurrentToken(t *testing.T) {
	w := newInvWorld(t, "Delivery Tenant", "Ada Obi")
	first := mustIssue(t, w.adminCtx(), w.store, []string{"b@x.test"}, "preparer")[0]
	second, err := w.store.ResendInvitation(w.adminCtx(), first.ID)
	if err != nil {
		t.Fatalf("resend: %v", err)
	}
	status := func() string { return pendingRow(t, w.super, w.tenant, "b@x.test").SendStatus }

	if err := w.store.RecordInviteDelivery(w.adminCtx(), first.ID, first.TokenHash, false); err != nil {
		t.Fatalf("record for the old hash: %v", err)
	}
	if got := status(); got != "sending" {
		t.Fatalf("send_status after a stale outcome = %q, want sending", got)
	}
	if err := w.store.RecordInviteDelivery(w.adminCtx(), second.ID, second.TokenHash, true); err != nil {
		t.Fatalf("record sent: %v", err)
	}
	if got := status(); got != "sent" {
		t.Errorf("send_status after the current outcome = %q, want sent", got)
	}
	if err := w.store.RecordInviteDelivery(w.adminCtx(), second.ID, second.TokenHash, false); err != nil {
		t.Fatalf("record failed: %v", err)
	}
	if got := status(); got != "failed" {
		t.Errorf("send_status after a failed outcome = %q, want failed", got)
	}

	// Another tenant's admin holding the right id and hash changes nothing; the owner's call still does.
	other := newInvWorld(t, "Delivery Other Tenant", "Olu Bee")
	if err := other.store.RecordInviteDelivery(other.adminCtx(), second.ID, second.TokenHash, true); err != nil {
		t.Fatalf("cross-tenant record: %v", err)
	}
	if got := status(); got != "failed" {
		t.Errorf("send_status after another tenant's record = %q, want failed (unchanged)", got)
	}
	if err := w.store.RecordInviteDelivery(w.adminCtx(), second.ID, second.TokenHash, true); err != nil {
		t.Fatalf("owner record: %v", err)
	}
	if got := status(); got != "sent" {
		t.Errorf("send_status after the owner's record = %q, want sent (the cross-tenant call must not be a dead path)", got)
	}
}

func TestInvitations_RegisteredElsewhereReadsTheSame(t *testing.T) {
	w := newInvWorld(t, "Enumeration Tenant", "Ada Obi")
	other := newInvWorld(t, "Elsewhere Tenant", "Olu Bee")
	seedIdentityMembership(t, other.super, other.tenant, uuid.NewString(), "admin", "active", strp("Taken Person"), strp("taken@x.test"))
	seedIdentityMembership(t, w.super, w.tenant, uuid.NewString(), "reviewer", "active", strp("Same Workspace"), strp("member@x.test"))

	got := mustIssue(t, w.adminCtx(), w.store, []string{"taken@x.test", "fresh@x.test", "member@x.test"}, "preparer")

	mask := func(i IssuedInvite) IssuedInvite {
		i.ID, i.Email, i.Token, i.TokenHash, i.ExpiresAt = "", "", "", nil, time.Time{}
		return i
	}
	for i := 1; i < len(got); i++ {
		if !reflect.DeepEqual(mask(got[0]), mask(got[i])) {
			t.Errorf("result %d differs from result 0 beyond id/email/token/expiry: %+v vs %+v", i, mask(got[i]), mask(got[0]))
		}
	}
	if got[0].Workspace != "Enumeration Tenant" || got[0].Inviter != "Ada Obi" {
		t.Errorf("result workspace/inviter = %q/%q", got[0].Workspace, got[0].Inviter)
	}
	for _, e := range []string{"taken@x.test", "fresh@x.test", "member@x.test"} {
		pendingRow(t, w.super, w.tenant, e)
	}
	if n := countInvitations(t, w.super, other.tenant); n != 0 {
		t.Errorf("the other tenant gained %d invitations rows", n)
	}
}

type tracedStmt struct {
	sql  string
	args []any
}

// sqlTrace records every statement a pool sends.
type sqlTrace struct {
	mu    sync.Mutex
	stmts []tracedStmt
}

func (s *sqlTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stmts = append(s.stmts, tracedStmt{d.SQL, slices.Clone(d.Args)})
	return ctx
}

func (s *sqlTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestInvitations_NoQueryReadsAccountsByInviteeAddress(t *testing.T) {
	w := newInvWorld(t, "Trace Tenant", "Ada Obi")
	other := newInvWorld(t, "Trace Elsewhere Tenant", "Olu Bee")
	seedIdentityMembership(t, other.super, other.tenant, uuid.NewString(), "admin", "active", strp("Taken Person"), strp("taken@x.test"))
	seedIdentityMembership(t, w.super, w.tenant, uuid.NewString(), "reviewer", "active", strp("Same Workspace"), strp("member@x.test"))

	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	tr := &sqlTrace{}
	cfg.ConnConfig.Tracer = tr
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := NewStore(pool)

	emails := []string{"taken@x.test", "member@x.test", "fresh@x.test"}
	issued := mustIssue(t, w.adminCtx(), store, emails, "preparer")
	if _, err := store.ResendInvitation(w.adminCtx(), issued[0].ID); err != nil {
		t.Fatalf("resend: %v", err)
	}
	if _, err := store.ListInvitations(w.adminCtx()); err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := store.RecordInviteDelivery(w.adminCtx(), issued[0].ID, issued[0].TokenHash, true); err != nil {
		t.Fatalf("record: %v", err)
	}

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.stmts) < 10 {
		t.Fatalf("traced %d statements, want at least 10 (the pool is not being observed)", len(tr.stmts))
	}
	sawAddress := 0
	for _, st := range tr.stmts {
		low := strings.ToLower(st.sql)
		if strings.Contains(low, "auth.") {
			t.Errorf("a statement touches the auth schema: %s", st.sql)
		}
		byAddress := false
		for _, a := range st.args {
			if s, ok := a.(string); ok && slices.Contains(emails, s) {
				byAddress = true
			}
		}
		if !byAddress {
			continue
		}
		sawAddress++
		if strings.Contains(low, "memberships") || !strings.Contains(low, "invitations") {
			t.Errorf("a statement keyed by an invitee address reads beyond the invitations table: %s", st.sql)
		}
	}
	if sawAddress < len(emails) {
		t.Errorf("statements carrying an invitee address = %d, want at least %d (the insert per address)", sawAddress, len(emails))
	}
}

func TestInvitations_AnotherTenantNeitherSeesNorSharesRows(t *testing.T) {
	a := newInvWorld(t, "Tenant A", "Ada Obi")
	b := newInvWorld(t, "Tenant B", "Olu Bee")
	aInv := mustIssue(t, a.adminCtx(), a.store, []string{"shared@x.test", "a-only@x.test"}, "reviewer")
	aRow := pendingRow(t, a.super, a.tenant, "shared@x.test")

	bOwn := mustIssue(t, b.adminCtx(), b.store, []string{"b-only@x.test"}, "preparer")[0]
	got, err := b.store.ListInvitations(b.adminCtx())
	if err != nil {
		t.Fatalf("B list: %v", err)
	}
	if len(got) != 1 || got[0].ID != bOwn.ID {
		t.Fatalf("B lists %d invitations %+v, want only its own %s", len(got), got, bOwn.ID)
	}

	if _, err := b.store.ResendInvitation(b.adminCtx(), aInv[0].ID); !errors.Is(err, ErrInvitationNotFound) {
		t.Errorf("B resend of A's invite: err = %v, want ErrInvitationNotFound", err)
	}

	// The same address invited from B is B's own row; A's row is untouched.
	bShared := mustIssue(t, b.adminCtx(), b.store, []string{"shared@x.test"}, "admin")[0]
	if bShared.ID == aInv[0].ID {
		t.Fatal("B's invite reused A's row id")
	}
	after := pendingRow(t, a.super, a.tenant, "shared@x.test")
	if after.ID != aRow.ID || !slices.Equal(after.Hash, aRow.Hash) || after.Role != "reviewer" || after.InvitedBy != a.admin || !after.Expires.Equal(aRow.Expires) {
		t.Errorf("A's row changed after B invited the same address: %+v -> %+v", aRow, after)
	}
	if n := inviteAudit(t, a.super, a.tenant); n != 2 {
		t.Errorf("A's invitation audit rows = %d, want 2 (B's invite must not land in A's log)", n)
	}
	if n := inviteAudit(t, b.super, b.tenant); n != 2 {
		t.Errorf("B's invitation audit rows = %d, want 2", n)
	}

	// A full window in A does not limit B.
	seedAuditEvents(t, a.super, a.tenant, "invitation.sent", sentPayload, "1 hour", 20)
	if _, err := a.store.CreateInvitations(a.adminCtx(), []string{"c@x.test"}, "preparer"); !errors.Is(err, ErrDailyInviteLimit) {
		t.Fatalf("A at the limit: err = %v, want ErrDailyInviteLimit", err)
	}
	mustIssue(t, b.adminCtx(), b.store, []string{"c@x.test"}, "preparer")
}

func TestInvitations_EveryKnownRoleIsStored(t *testing.T) {
	w := newInvWorld(t, "Role Tenant", "Ada Obi")
	for _, role := range []string{"admin", "preparer", "reviewer"} {
		email := role + "@x.test"
		got := mustIssue(t, w.adminCtx(), w.store, []string{email}, role)[0]
		if got.Role != role {
			t.Errorf("result role = %q, want %q", got.Role, role)
		}
		if row := pendingRow(t, w.super, w.tenant, email); row.Role != role {
			t.Errorf("stored role = %q, want %q", row.Role, role)
		}
	}
}

func TestInvitations_EmptyAddressListWritesNothing(t *testing.T) {
	w := newInvWorld(t, "Empty Tenant", "Ada Obi")
	preparer := w.addMember(t, "preparer", "active", "Pat Preparer")

	for name, emails := range map[string][]string{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			got, _ := w.store.CreateInvitations(w.adminCtx(), emails, "preparer")
			if len(got) != 0 {
				t.Errorf("returned %d invites for no addresses", len(got))
			}
			if n := countInvitations(t, w.super, w.tenant); n != 0 {
				t.Errorf("invitations rows = %d, want 0", n)
			}
			if n := inviteAudit(t, w.super, w.tenant); n != 0 {
				t.Errorf("invitation audit rows = %d, want 0", n)
			}
			if _, err := w.store.CreateInvitations(w.as(preparer), emails, "preparer"); !errors.Is(err, ErrInviteNotPermitted) {
				t.Errorf("preparer with no addresses: err = %v, want ErrInviteNotPermitted", err)
			}
		})
	}
	mustIssue(t, w.adminCtx(), w.store, []string{"b@x.test"}, "preparer")
}

func TestInvitations_TwentyAddressesFitAnEmptyWindow(t *testing.T) {
	w := newInvWorld(t, "Twenty Tenant", "Ada Obi")

	if _, err := w.store.CreateInvitations(w.adminCtx(), addrs("over", 21), "preparer"); !errors.Is(err, ErrDailyInviteLimit) {
		t.Fatalf("21 addresses on an empty window: err = %v, want ErrDailyInviteLimit", err)
	}
	if n := countInvitations(t, w.super, w.tenant); n != 0 {
		t.Fatalf("invitations rows after the refusal = %d, want 0", n)
	}
	mustIssue(t, w.adminCtx(), w.store, addrs("fit", 20), "preparer")
	if n := countInvitations(t, w.super, w.tenant); n != 20 {
		t.Errorf("invitations rows = %d, want 20", n)
	}
	if _, err := w.store.CreateInvitations(w.adminCtx(), []string{"one-more@x.test"}, "preparer"); !errors.Is(err, ErrDailyInviteLimit) {
		t.Errorf("21st mail: err = %v, want ErrDailyInviteLimit", err)
	}
}

// The handler dedupes (D9); a duplicate that reaches the store leaves one row and one live token.
func TestInvitations_DuplicateAddressInOneCallLeavesOneLiveToken(t *testing.T) {
	w := newInvWorld(t, "Duplicate Tenant", "Ada Obi")

	got := mustIssue(t, w.adminCtx(), w.store, []string{"d@x.test", "d@x.test"}, "preparer")

	if got[0].ID != got[1].ID {
		t.Errorf("duplicate results carry ids %s and %s, want one row", got[0].ID, got[1].ID)
	}
	row := pendingRow(t, w.super, w.tenant, "d@x.test")
	live := 0
	for _, inv := range got {
		if slices.Equal(row.Hash, sha(inv.Token)) {
			live++
		}
	}
	if live != 1 {
		t.Errorf("live tokens among the two returned = %d, want exactly 1", live)
	}
	if n := countInvitations(t, w.super, w.tenant); n != 1 {
		t.Errorf("invitations rows = %d, want 1", n)
	}
	if n := len(auditPayloads(t, w.super, w.tenant, "invitation.sent")); n != 2 {
		t.Errorf("invitation.sent rows = %d, want 2 (each mail counts)", n)
	}
}

func TestInvitations_CreateNamesTheCallerOrFallsBack(t *testing.T) {
	cases := []struct {
		name          string
		display, mail *string
		want          string
	}{
		{"display name", strp("Ada Obi"), strp("ada@obi.test"), "Ada Obi"},
		{"display name null falls back to email", nil, strp("ada@obi.test"), "ada@obi.test"},
		{"both null", nil, nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newInvWorld(t, "Caller Name Tenant", "Seed Admin")
			caller := uuid.NewString()
			seedIdentityMembership(t, w.super, w.tenant, caller, "admin", "active", c.display, c.mail)

			got := mustIssue(t, w.as(caller), w.store, []string{"b@x.test"}, "preparer")[0]
			if got.Inviter != c.want {
				t.Errorf("Inviter = %q, want %q", got.Inviter, c.want)
			}
		})
	}
}

func TestInvitations_InvitingALegacyTokenlessAddressReissuesIt(t *testing.T) {
	w := newInvWorld(t, "Legacy Reissue Tenant", "Ada Obi")
	seedLegacyPending(t, w.super, w.tenant, "legacy@x.test")
	var legacyID string
	if err := w.super.QueryRow(context.Background(),
		`SELECT id::text FROM invitations WHERE tenant_id = $1 AND invitee_email = 'legacy@x.test'`, w.tenant).Scan(&legacyID); err != nil {
		t.Fatal(err)
	}

	got := mustIssue(t, w.adminCtx(), w.store, []string{"legacy@x.test"}, "reviewer")[0]

	if got.ID != legacyID {
		t.Errorf("reissue id = %s, want the legacy row %s", got.ID, legacyID)
	}
	row := pendingRow(t, w.super, w.tenant, "legacy@x.test")
	if !slices.Equal(row.Hash, sha(got.Token)) || row.InvitedBy != w.admin || row.Expires.IsZero() {
		t.Errorf("legacy row not completed: %+v", row)
	}
}

// A tokenless legacy row is never listed, so a resend of its id is not a path the UI offers.
func TestInvitations_ResendOfALegacyTokenlessInviteChangesNothing(t *testing.T) {
	w := newInvWorld(t, "Legacy Resend Tenant", "Ada Obi")
	seedLegacyPending(t, w.super, w.tenant, "legacy@x.test")
	var id string
	if err := w.super.QueryRow(context.Background(),
		`SELECT id::text FROM invitations WHERE tenant_id = $1`, w.tenant).Scan(&id); err != nil {
		t.Fatal(err)
	}

	got, err := w.store.ResendInvitation(w.adminCtx(), id)

	if err == nil {
		t.Fatalf("resend of a tokenless pending row succeeded: %+v", got)
	}
	if got.Token != "" {
		t.Error("a failed resend returned a token")
	}
	if rows := invRows(t, w.super, w.tenant, ""); len(rows) != 1 || len(rows[0].Hash) != 0 {
		t.Errorf("legacy row changed: %+v", rows)
	}
	if n := inviteAudit(t, w.super, w.tenant); n != 0 {
		t.Errorf("invitation audit rows = %d, want 0", n)
	}
}

// holdInviteLock takes the tenant's invitation_send advisory lock on a superuser connection.
func holdInviteLock(t *testing.T, w invWorld) (release func()) {
	t.Helper()
	ctx := context.Background()
	conn, err := w.super.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('invitation_send:' || $1::text, 0))`, w.tenant); err != nil {
		t.Fatal(err)
	}
	released := false
	release = func() {
		if released {
			return
		}
		released = true
		if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended('invitation_send:' || $1::text, 0))`, w.tenant); err != nil {
			t.Errorf("unlock: %v", err)
		}
		conn.Release()
	}
	t.Cleanup(release)
	return release
}

func TestInvitations_ConcurrentResendsShareTheLimit(t *testing.T) {
	w := newInvWorld(t, "Concurrent Resend Tenant", "Ada Obi")
	a := seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: "a@x.test", invitedBy: w.admin, sendStatus: "sent"})
	b := seedInvitation(t, w.super, seedInv{tenant: w.tenant, email: "b@x.test", invitedBy: w.admin, sendStatus: "sent"})
	seedAuditEvents(t, w.super, w.tenant, "invitation.sent", sentPayload, "1 hour", 19)
	release := holdInviteLock(t, w)

	chA, chB := make(chan error, 1), make(chan error, 1)
	go func() { _, err := w.store.ResendInvitation(w.adminCtx(), a.ID); chA <- err }()
	go func() { _, err := w.store.ResendInvitation(w.adminCtx(), b.ID); chB <- err }()

	deadline := time.Now().Add(5 * time.Second)
	for advisoryLocks(t, w.super, w.tenant, false) < 2 {
		select {
		case err := <-chA:
			t.Fatalf("a resend returned while the invitation_send lock was held (err %v)", err)
		case err := <-chB:
			t.Fatalf("a resend returned while the invitation_send lock was held (err %v)", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("pg_locks shows %d ungranted requests on the invitation_send key after 5 s, want 2", advisoryLocks(t, w.super, w.tenant, false))
		}
		time.Sleep(25 * time.Millisecond)
	}
	release()

	ok, limited := 0, 0
	for _, ch := range []chan error{chA, chB} {
		select {
		case err := <-ch:
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrDailyInviteLimit):
				limited++
			default:
				t.Errorf("unexpected resend error: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("a resend did not return after the lock was released")
		}
	}
	if ok != 1 || limited != 1 {
		t.Errorf("successes/limit refusals = %d/%d, want 1/1", ok, limited)
	}
	if n := len(auditPayloads(t, w.super, w.tenant, "invitation.resent")); n != 1 {
		t.Errorf("invitation.resent rows = %d, want 1", n)
	}
}
