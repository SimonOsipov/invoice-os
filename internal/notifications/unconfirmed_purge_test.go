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

// A pool that cannot connect makes every purge call fail, so the joined error counts the candidates reached.
func TestUnconfirmedPurge_AFailingAccountDoesNotStopTheOthers(t *testing.T) {
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

	joined, ok := runErr.(interface{ Unwrap() []error })
	if !ok || len(joined.Unwrap()) != 3 {
		t.Fatalf("RunOnce = %v, want a joined error with one entry for each of the 3 stale accounts", runErr)
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
