package notifications

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth/gotrueadmin"
)

func TestUnconfirmedPurge_PicksOnlyStaleUnconfirmedAccounts(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	const day = 24 * time.Hour
	for _, c := range []struct {
		name string
		user gotrueadmin.AdminUser
		want bool
	}{
		{"confirmed, created 30 days ago", gotrueadmin.AdminUser{CreatedAt: *ago(30 * day), EmailConfirmedAt: ago(29 * day)}, false},
		{"unconfirmed, last mail 7 days less a minute", gotrueadmin.AdminUser{CreatedAt: *ago(30 * day), ConfirmationSentAt: ago(7*day - time.Minute)}, false},
		{"unconfirmed, last mail exactly 7 days", gotrueadmin.AdminUser{CreatedAt: *ago(7 * day)}, false},
		{"created 8 days ago, confirmation mail 1 hour ago", gotrueadmin.AdminUser{CreatedAt: *ago(8 * day), ConfirmationSentAt: ago(time.Hour)}, false},
		{"created 8 days ago, reset mail 1 hour ago", gotrueadmin.AdminUser{CreatedAt: *ago(8 * day), RecoverySentAt: ago(time.Hour)}, false},
		{"created 8 days ago, no mail stamps", gotrueadmin.AdminUser{CreatedAt: *ago(8 * day)}, true},
		{"last mail 7 days plus a minute", gotrueadmin.AdminUser{CreatedAt: *ago(30 * day), ConfirmationSentAt: ago(7*day + time.Minute)}, true},
		{"both stamps 8 days old", gotrueadmin.AdminUser{CreatedAt: *ago(9 * day), ConfirmationSentAt: ago(8 * day), RecoverySentAt: ago(8 * day)}, true},
		{"old stamps, a reset mail 1 hour ago", gotrueadmin.AdminUser{CreatedAt: *ago(9 * day), ConfirmationSentAt: ago(8 * day), RecoverySentAt: ago(time.Hour)}, false},
	} {
		if got := stale(c.user, now); got != c.want {
			t.Errorf("%s: stale = %v, want %v", c.name, got, c.want)
		}
	}
}

func purgeLog() (*bytes.Buffer, *slog.Logger) {
	var buf bytes.Buffer
	return &buf, slog.New(slog.NewJSONHandler(&buf, nil))
}

func TestUnconfirmedPurge_AListFailureEndsTheRun(t *testing.T) {
	listErr := errors.New("admin list failed")
	buf, logger := purgeLog()
	acct := &fakeAccounts{listErr: listErr, users: []gotrueadmin.AdminUser{{ID: uuid.NewString(), Email: "who@leak.test"}}}

	err := (&UnconfirmedPurge{Account: acct, Logger: logger, Now: time.Now}).RunOnce(context.Background())

	if !errors.Is(err, listErr) {
		t.Fatalf("RunOnce = %v, want the list error", err)
	}
	if fmt.Sprint(acct.pages) != "[1]" {
		t.Errorf("pages read = %v, want [1]", acct.pages)
	}
	if strings.Contains(buf.String(), "leak.test") || strings.Contains(buf.String(), acct.users[0].ID) {
		t.Errorf("log %q holds an address or a user id", buf)
	}
}

// A pool that cannot connect makes every purge call fail; the run still returns one error.
func TestUnconfirmedPurge_EveryPurgeFailingReturnsOneErrorWithoutAddresses(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	old := time.Now().Add(-8 * 24 * time.Hour)
	fresh := time.Now().Add(-time.Hour)
	mk := func(email string, created time.Time) gotrueadmin.AdminUser {
		return gotrueadmin.AdminUser{ID: uuid.NewString(), Email: email, CreatedAt: created}
	}
	users := []gotrueadmin.AdminUser{mk("a@leak.test", old), mk("b@leak.test", fresh), mk("c@leak.test", old), mk("d@leak.test", old)}
	buf, logger := purgeLog()

	runErr := (&UnconfirmedPurge{Pool: pool, Account: &fakeAccounts{users: users}, Logger: logger, Now: time.Now}).RunOnce(context.Background())

	if runErr == nil {
		t.Fatal("RunOnce = nil, want the purge error")
	}
	if _, ok := runErr.(interface{ Unwrap() []error }); ok {
		t.Errorf("RunOnce = %v, want one error, not a joined one", runErr)
	}
	for _, text := range []string{runErr.Error(), buf.String()} {
		if strings.Contains(text, "leak.test") {
			t.Errorf("error or log %q holds an address", text)
		}
		for _, u := range users {
			if strings.Contains(text, u.ID) {
				t.Errorf("error or log %q holds a user id", text)
			}
		}
	}
}

func staleUsers(n int) []gotrueadmin.AdminUser {
	old := time.Now().Add(-8 * 24 * time.Hour)
	users := make([]gotrueadmin.AdminUser, n)
	for i := range users {
		users[i] = gotrueadmin.AdminUser{ID: uuid.NewString(), CreatedAt: old}
	}
	return users
}

// The lister drops a user once purged, as GoTrue does, so a run that pages by offset while deleting skips rows.
func TestUnconfirmedPurge_RemovesEveryStaleAccountAcrossPages(t *testing.T) {
	acct := &fakeAccounts{users: staleUsers(250)}
	var calls int
	purge := func(_ context.Context, id string) (bool, error) {
		calls++
		for i, u := range acct.users {
			if u.ID == id {
				acct.users = append(acct.users[:i:i], acct.users[i+1:]...)
				break
			}
		}
		return true, nil
	}

	err := (&UnconfirmedPurge{Account: acct, Purge: purge, Now: time.Now}).RunOnce(context.Background())

	if err != nil {
		t.Fatalf("RunOnce = %v", err)
	}
	if calls != 250 || len(acct.users) != 0 {
		t.Errorf("purge calls = %d, accounts left = %d, want 250 and 0", calls, len(acct.users))
	}
}

func TestUnconfirmedPurge_AFailingAccountDoesNotStopTheOthersAndOneErrorNamesTheCount(t *testing.T) {
	users := staleUsers(5)
	boom := errors.New("function missing")
	var calls int
	purge := func(_ context.Context, id string) (bool, error) {
		calls++
		if calls == 2 || calls == 4 {
			return false, boom
		}
		return true, nil
	}
	buf, logger := purgeLog()

	err := (&UnconfirmedPurge{Account: &fakeAccounts{users: users}, Purge: purge, Logger: logger, Now: time.Now}).RunOnce(context.Background())

	if !errors.Is(err, boom) || calls != 5 {
		t.Fatalf("RunOnce = %v after %d calls, want the purge error after all 5", err, calls)
	}
	if _, ok := err.(interface{ Unwrap() []error }); ok || !strings.Contains(err.Error(), "2 purges failed, removed 3") {
		t.Errorf("RunOnce = %q, want one error naming 2 failures and 3 removed", err)
	}
	for _, u := range users {
		if strings.Contains(err.Error(), u.ID) || strings.Contains(buf.String(), u.ID) {
			t.Errorf("error or log holds user id %s", u.ID)
		}
	}
	if !strings.Contains(buf.String(), `"accounts":3`) {
		t.Errorf("log %q lacks the 3 accounts removed", buf)
	}
}

func TestUnconfirmedPurge_AListFailureOnALaterPageKeepsTheCount(t *testing.T) {
	listErr := errors.New("page 2 failed")
	acct := &laterPageFails{fakeAccounts: fakeAccounts{users: staleUsers(100)}, err: listErr}
	buf, logger := purgeLog()

	err := (&UnconfirmedPurge{Account: acct, Purge: func(context.Context, string) (bool, error) { return true, nil }, Logger: logger, Now: time.Now}).RunOnce(context.Background())

	if !errors.Is(err, listErr) {
		t.Fatalf("RunOnce = %v, want the list error", err)
	}
	if !strings.Contains(buf.String(), `"accounts":100`) {
		t.Errorf("log %q lacks the 100 accounts removed before the list failure", buf)
	}
}

type laterPageFails struct {
	fakeAccounts
	err error
}

func (l *laterPageFails) ListUsers(ctx context.Context, page, perPage int) ([]gotrueadmin.AdminUser, error) {
	if page > 1 {
		return nil, l.err
	}
	return l.fakeAccounts.ListUsers(ctx, page, perPage)
}

func TestUnconfirmedPurge_PagesTheListAt100(t *testing.T) {
	users := make([]gotrueadmin.AdminUser, 101)
	for i := range users {
		now := time.Now()
		users[i] = gotrueadmin.AdminUser{ID: uuid.NewString(), CreatedAt: now, EmailConfirmedAt: &now}
	}
	acct := &fakeAccounts{users: users}

	if err := (&UnconfirmedPurge{Account: acct, Now: time.Now}).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce = %v, want nil: no account is a candidate, so no pool call", err)
	}
	if fmt.Sprint(acct.pages) != "[1 2]" {
		t.Errorf("pages read = %v, want [1 2]", acct.pages)
	}
}
