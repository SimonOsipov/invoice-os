//go:build purgeimpl

// Names notifications.UnconfirmedPurge, which does not exist yet. 07-06 deletes the build tag.

package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/notifications"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth/gotrueadmin"
)

// staleSnapshot is a lister that returns one account as an earlier read saw it.
type staleSnapshot struct{ user gotrueadmin.AdminUser }

func (s staleSnapshot) ListUsers(_ context.Context, page, _ int) ([]gotrueadmin.AdminUser, error) {
	if page > 1 {
		return nil, nil
	}
	return []gotrueadmin.AdminUser{s.user}, nil
}

func unconfirmedSnapshot(id, email string, age time.Duration) staleSnapshot {
	at := time.Now().Add(-age)
	return staleSnapshot{gotrueadmin.AdminUser{ID: id, Email: email, CreatedAt: at, ConfirmationSentAt: &at}}
}

// runPurge runs the worker task as invoice_app and returns its JSON log.
func runPurge(t *testing.T, lister notifications.PurgeLister) string {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), mailEnv(t, "DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var buf bytes.Buffer
	p := &notifications.UnconfirmedPurge{Pool: pool, Account: lister, Logger: slog.New(slog.NewJSONHandler(&buf, nil)), Now: time.Now}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce = %v, want nil", err)
	}
	return buf.String()
}

// verifyToken redeems a mailed link's token at GoTrue and returns the status.
func verifyToken(t *testing.T, base, kind, link string) int {
	t.Helper()
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	status, _ := postJSON(t, base+"/verify", map[string]string{"type": kind, "token_hash": parsed.Query().Get("token")})
	return status
}

func requireIdPAccount(t *testing.T, base, id string, want bool) {
	t.Helper()
	got, err := emailAs(t, adminClient(t, base), id)
	if err != nil || (got != "") != want {
		t.Fatalf("admin GET of the account = (%q, %v); want exists %v", got, err, want)
	}
}

// requireLogCount requires a log line that holds a number of at least 1 and no address or id.
func requireLogCount(t *testing.T, log string, secrets ...string) {
	t.Helper()
	counted := false
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		for k, v := range rec {
			if n, ok := v.(float64); ok && k != "time" && n >= 1 {
				counted = true
			}
		}
	}
	if !counted {
		t.Errorf("log %q holds no removed count", log)
	}
	for _, s := range secrets {
		if strings.Contains(log, s) {
			t.Errorf("log holds an address or user id")
		}
	}
}

func TestIdP_UnconfirmedPurge_RemovesAStaleUnconfirmedAccount(t *testing.T) {
	base := idpMailURL(t)
	grantAccountStateRead(t)
	u := signUpMailed(t, base, "purge-", "example.test")
	id := userID(t, u.email)
	backdate(t, id, past, past, 0)
	requireIdPAccount(t, base, id, true)

	log := runPurge(t, adminClient(t, base))

	requireIdPAccount(t, base, id, false)
	requireLogCount(t, log, u.email, id)
}

// Address rows the full erase would delete; the purge must leave every one.
func seedAddressRows(t *testing.T, email string) func() map[string]int {
	t.Helper()
	su := superConn(t)
	tenant := seedTenant(t, su)
	for _, q := range []string{
		`INSERT INTO readiness_checks (email, file_type, row_count) VALUES ($1, 'csv', 1)`,
		`INSERT INTO readiness_check_requests (email, token_hash, file_type, row_count, mail, expires_at)
			VALUES ($1, sha256(gen_random_uuid()::text::bytea), 'csv', 1, '{}', now() + interval '1 hour')`,
		`INSERT INTO blog_subscribers (email, category_ids, frequency, confirmed_at, version, resend_delivered_at) VALUES ($1, '{a}', 'weekly', now(), 1, now())`,
		`INSERT INTO blog_subscription_requests (email, category_ids, frequency, token_hash, expires_at)
			VALUES ($1, '{a}', 'weekly', sha256(gen_random_uuid()::text::bytea), now() + interval '1 hour')`,
		`SELECT public.mail_unsubscribe_token($1)`,
	} {
		exec(t, su, q, email)
	}
	exec(t, su, `INSERT INTO invitations (id, tenant_id, role, invitee_email, status, token_hash, expires_at, invited_by)
		VALUES ($1, $2, 'admin', $3, 'pending', sha256(gen_random_uuid()::text::bytea), now() + interval '7 days', $4)`,
		uuid.NewString(), tenant, email, uuid.NewString())
	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			`DELETE FROM readiness_checks WHERE email = $1`, `DELETE FROM readiness_check_requests WHERE email = $1`,
			`DELETE FROM blog_subscribers WHERE email = $1`, `DELETE FROM blog_subscription_requests WHERE email = $1`,
			`DELETE FROM mail_unsubscribes WHERE email = $1`, `DELETE FROM invitations WHERE invitee_email = $1`,
		} {
			_, _ = su.Exec(ctx, q, email)
		}
	})
	return func() map[string]int {
		counts := map[string]int{}
		for table, col := range map[string]string{"readiness_checks": "email", "readiness_check_requests": "email", "blog_subscribers": "email",
			"blog_subscription_requests": "email", "mail_unsubscribes": "email", "invitations": "invitee_email"} {
			counts[table] = countRows(t, `SELECT count(*) FROM `+table+` WHERE `+col+` = $1`, email)
		}
		return counts
	}
}

func requireAddressRowsKept(t *testing.T, counts func() map[string]int, id, email string) {
	t.Helper()
	got := counts()
	if len(got) != 6 {
		t.Fatalf("control: %d address tables counted, want 6", len(got))
	}
	for table, n := range got {
		if n != 1 {
			t.Errorf("%s holds %d rows for the address after the purge, want 1", table, n)
		}
	}
	su := superConn(t)
	if n := countAuthRows(t, su, `SELECT count(*) FROM person_erasures WHERE user_id = $1::uuid`, id); n != 0 {
		t.Errorf("person_erasures holds %d rows for the account, want 0", n)
	}
	if n := countAuthRows(t, su, `SELECT count(*) FROM river_job WHERE kind IN ('person_erase', 'person_erase_by_id') AND args->>'user_id' = $1`, id); n != 0 {
		t.Errorf("%d person_erase jobs queued for the account, want 0", n)
	}
}

func TestIdP_UnconfirmedPurge_AnOwnerWhoConfirmsAfterTheListKeepsAccountAndData(t *testing.T) {
	base := idpMailURL(t)
	grantAccountStateRead(t)
	u := signUpMailed(t, base, "late-confirm-", "example.test")
	id := userID(t, u.email)
	counts := seedAddressRows(t, u.email)
	if status := verifyToken(t, base, "signup", confirmationLink(t, u.email)); status != http.StatusOK {
		t.Fatalf("POST /verify with the signup token: status %d, want 200", status)
	}
	backdate(t, id, 8*day, 8*day, 0)

	runPurge(t, unconfirmedSnapshot(id, u.email, 8*day))

	requireIdPAccount(t, base, id, true)
	requireConfirmed(t, u.email, true)
	if status, _ := signIn(t, base, u); status != http.StatusOK {
		t.Errorf("password grant after the run: status %d, want 200", status)
	}
	requireAddressRowsKept(t, counts, id, u.email)
}

func TestIdP_UnconfirmedPurge_ARegisterAtDaySixKeepsTheLiveLink(t *testing.T) {
	base := idpMailURL(t)
	grantAccountStateRead(t)
	u := signUpMailed(t, base, "day-six-", "example.test")
	id := userID(t, u.email)
	oldLink := confirmationLink(t, u.email)
	backdate(t, id, past+time.Hour, past+time.Hour, 0)
	u.register(t, base)
	var newLink string
	for _, l := range confirmationLinks(t, u.email, 2) {
		if l != oldLink {
			newLink = l
		}
	}
	if newLink == "" {
		t.Fatal("the second sign-up mailed no new link")
	}

	runPurge(t, adminClient(t, base))

	requireIdPAccount(t, base, id, true)
	if status := verifyToken(t, base, "signup", newLink); status != http.StatusOK {
		t.Errorf("POST /verify with the newest mail's token: status %d, want 200", status)
	}
}

func TestIdP_UnconfirmedPurge_ARecentResetMailKeepsTheAccount(t *testing.T) {
	base := idpMailURL(t)
	grantAccountStateRead(t)
	u := signUpMailed(t, base, "reset-", "example.test")
	id := userID(t, u.email)
	backdate(t, id, 8*day, 8*day, 0)
	if status, body := postJSON(t, base+"/recover", map[string]string{"email": u.email}); status != http.StatusOK {
		t.Fatalf("POST /recover: status %d, body %v; want 200", status, body)
	}
	link := recoveryLink(t, u.email, 2)

	runPurge(t, adminClient(t, base))

	requireIdPAccount(t, base, id, true)
	if status := verifyToken(t, base, "recovery", link); status != http.StatusOK {
		t.Errorf("POST /verify with the recovery token: status %d, want 200", status)
	}
	requireConfirmed(t, u.email, true)
}

func TestIdP_UnconfirmedPurge_LeavesTheAddressDataOfAStrangersRegistration(t *testing.T) {
	base := idpMailURL(t)
	grantAccountStateRead(t)
	u := signUpMailed(t, base, "stranger-", "victim.example")
	id := userID(t, u.email)
	counts := seedAddressRows(t, u.email)
	backdate(t, id, 8*day, 8*day, 0)

	runPurge(t, adminClient(t, base))

	requireIdPAccount(t, base, id, false)
	requireAddressRowsKept(t, counts, id, u.email)
}
