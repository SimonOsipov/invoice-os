package notifications

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth/gotrueadmin"
)

type metaPut struct{ id, token string }

// fakeAccounts serves the users in pages of perPage and records each PUT; failID makes that PUT fail.
type fakeAccounts struct {
	users  []gotrueadmin.AdminUser
	pages  []int
	puts   []metaPut
	failID string
}

func (f *fakeAccounts) ListUsers(_ context.Context, page, perPage int) ([]gotrueadmin.AdminUser, error) {
	f.pages = append(f.pages, page)
	lo, hi := (page-1)*perPage, min(page*perPage, len(f.users))
	if lo >= len(f.users) {
		return nil, nil
	}
	return f.users[lo:hi], nil
}

func (f *fakeAccounts) SetUserMetadata(_ context.Context, id string, meta map[string]any) error {
	if id == f.failID {
		return errors.New("put failed")
	}
	tok, _ := meta["unsubscribe"].(string)
	f.puts = append(f.puts, metaPut{id, tok})
	for i := range f.users {
		if f.users[i].ID == id {
			f.users[i].Unsubscribe = tok
		}
	}
	return nil
}

func metaUser(t *testing.T, e env, unsub string) gotrueadmin.AdminUser {
	t.Helper()
	u := gotrueadmin.AdminUser{ID: uuid.NewString(), Email: fmt.Sprintf("um-%s@unsub.test", uuid.NewString()[:8]), Unsubscribe: unsub}
	t.Cleanup(func() {
		_, _ = e.admin.Exec(context.Background(), `DELETE FROM mail_unsubscribes WHERE email = $1`, u.Email)
	})
	return u
}

func dbToken(t *testing.T, e env, email string) string {
	t.Helper()
	var tok string
	if err := e.admin.QueryRow(context.Background(), `SELECT token::text FROM mail_unsubscribes WHERE email = $1`, email).Scan(&tok); err != nil {
		t.Fatalf("token row of %s: %v", email, err)
	}
	return tok
}

func TestUnsubscribeMetadata_WritesMissingAndWrongTokensOnly(t *testing.T) {
	e := newEnv(t)
	missing, stale, ok := metaUser(t, e, ""), metaUser(t, e, uuid.NewString()), metaUser(t, e, "")
	if _, err := e.admin.Exec(context.Background(), `SELECT public.mail_unsubscribe_token($1)`, ok.Email); err != nil {
		t.Fatal(err)
	}
	ok.Unsubscribe = dbToken(t, e, ok.Email)
	// 100 filler users without an email fill page 1, so the 3 users span 2 pages.
	users := make([]gotrueadmin.AdminUser, 0, 103)
	users = append(users, missing)
	for i := 0; i < 98; i++ {
		users = append(users, gotrueadmin.AdminUser{ID: uuid.NewString()})
	}
	users = append(users, stale, ok)
	acct := &fakeAccounts{users: users}
	um := &UnsubscribeMetadata{Pool: e.app, Account: acct}
	if err := um.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []metaPut{{missing.ID, dbToken(t, e, missing.Email)}, {stale.ID, dbToken(t, e, stale.Email)}}
	if fmt.Sprint(acct.puts) != fmt.Sprint(want) {
		t.Errorf("puts = %v, want %v", acct.puts, want)
	}
	if fmt.Sprint(acct.pages) != "[1 2]" {
		t.Errorf("pages read = %v, want [1 2]", acct.pages)
	}
}

func TestUnsubscribeMetadata_SkipsAUserWithoutAnEmail(t *testing.T) {
	e := newEnv(t)
	var before int
	if err := e.admin.QueryRow(context.Background(), `SELECT count(*) FROM mail_unsubscribes`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	acct := &fakeAccounts{users: []gotrueadmin.AdminUser{{ID: uuid.NewString()}}}
	if err := (&UnsubscribeMetadata{Pool: e.app, Account: acct}).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := e.admin.QueryRow(context.Background(), `SELECT count(*) FROM mail_unsubscribes`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if len(acct.puts) != 0 || after != before {
		t.Errorf("puts = %v, token rows %d -> %d; want none, unchanged", acct.puts, before, after)
	}
}

func TestUnsubscribeMetadata_AFailedPutRetriesWithoutRewriting(t *testing.T) {
	e := newEnv(t)
	u1, u2 := metaUser(t, e, ""), metaUser(t, e, "")
	acct := &fakeAccounts{users: []gotrueadmin.AdminUser{u1, u2}, failID: u2.ID}
	um := &UnsubscribeMetadata{Pool: e.app, Account: acct}
	if err := um.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce = nil, want the PUT error")
	}
	acct.failID = ""
	if err := um.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(acct.puts) != 2 || acct.puts[0].id != u1.ID || acct.puts[1].id != u2.ID {
		t.Errorf("puts = %v, want user 1 once then user 2 once", acct.puts)
	}
}
