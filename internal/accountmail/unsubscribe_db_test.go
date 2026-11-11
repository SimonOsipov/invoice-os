package accountmail

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// unsubPools returns the invoice_app and superuser pools, and a lower-case address that the test removes.
func unsubPools(t *testing.T) (app, admin *pgxpool.Pool, email string) {
	t.Helper()
	appURL, adminURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || adminURL == "" {
		t.Skip("accountmail db test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
	}
	var err error
	if app, err = pgxpool.New(t.Context(), appURL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	if admin, err = pgxpool.New(t.Context(), adminURL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	email = "pt-" + uuid.NewString() + "@unsub.test"
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DELETE FROM mail_unsubscribes WHERE email = $1`, email)
	})
	return app, admin, email
}

func TestPoolTokens_ReturnsTheTableToken(t *testing.T) {
	app, admin, email := unsubPools(t)
	got, err := PoolTokens(app)(t.Context(), strings.ToUpper(email))
	if err != nil {
		t.Fatal(err)
	}
	var want string
	if err := admin.QueryRow(t.Context(), `SELECT token::text FROM mail_unsubscribes WHERE email = $1`, email).Scan(&want); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("token = %q, want %q", got, want)
	}
}

func TestSender_StillSendsToAnUnsubscribedAddress(t *testing.T) {
	app, admin, email := unsubPools(t)
	if _, err := admin.Exec(t.Context(), `INSERT INTO mail_unsubscribes (email, unsubscribed_at) VALUES ($1, now())`, email); err != nil {
		t.Fatal(err)
	}
	c := &Capture{Tokens: PoolTokens(app)}
	if err := c.Send(t.Context(), []Message{{To: email, HTML: unsubPlaceholderHTML}}); err != nil {
		t.Fatal(err)
	}
	var tok string
	if err := admin.QueryRow(t.Context(), `SELECT token::text FROM mail_unsubscribes WHERE email = $1`, email).Scan(&tok); err != nil {
		t.Fatal(err)
	}
	got := c.Messages()
	if len(got) != 1 || !strings.Contains(got[0].HTML, "?t="+tok) {
		t.Errorf("captured = %+v, want one mail with token %s", got, tok)
	}
}
