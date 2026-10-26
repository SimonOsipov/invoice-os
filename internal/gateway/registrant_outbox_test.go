package gateway

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GKE-05 AC 5: the gateway writes a registrant durably before answering, and invoice_gateway can
// only add rows: it reads and deletes none.
func TestRLS_GatewayHandoffRegistrantOutbox_WritesAndCannotReadBack(t *testing.T) {
	d := requireHandoffDB(t)
	ctx := context.Background()
	user := RegistrantContact{
		UserID: uuid.NewString(), Email: "outbox-" + uuid.NewString()[:8] + "@corp.example",
		DisplayName: "Ada Obi", WorkspaceName: "Obi & Co",
		Consent: &MarketingConsent{Text: "Allow marketing", At: "2026-10-22T10:00:00Z"},
	}
	t.Cleanup(func() {
		_, _ = d.super.Exec(context.Background(), `DELETE FROM registrant_handoffs WHERE user_id = $1`, user.UserID)
	})

	if err := NewRegistrantOutbox(d.a, nil).Registrant(ctx, user); err != nil {
		t.Fatalf("Registrant: %v", err)
	}
	var email, name, ws, text, at string
	if err := d.super.QueryRow(ctx, `SELECT email, display_name, workspace_name, consent_text, consent_at
		FROM registrant_handoffs WHERE user_id = $1`, user.UserID).Scan(&email, &name, &ws, &text, &at); err != nil {
		t.Fatalf("read the row back as superuser: %v", err)
	}
	if email != user.Email || name != user.DisplayName || ws != user.WorkspaceName || text != user.Consent.Text || at != user.Consent.At {
		t.Errorf("row = %q %q %q %q %q, want the contact as handed off", email, name, ws, text, at)
	}

	for _, stmt := range []string{
		`SELECT count(*) FROM registrant_handoffs`,
		`DELETE FROM registrant_handoffs WHERE user_id = '` + user.UserID + `'`,
	} {
		_, err := d.a.Exec(ctx, stmt)
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("invoice_gateway ran %q: err = %v, want permission denied", stmt, err)
		}
	}
}

// GKE-05 AC 5: with an outbox, the hand-off is one write before handOffRegistrant returns, never a
// goroutine after the response. An unreachable database fails that write at once: one WARN, no retry.
func TestHandOff_AnOutboxWritesBeforeReturning(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://invoice_gateway:x@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	sink := newRecSink(nil)
	log, logs := newCaptureLog()

	handOffRegistrant(context.Background(), log, "verify", NewRegistrantOutbox(pool, sink), coWant)

	if got := logs.all(); len(got) != 1 {
		t.Fatalf("logged %d line(s) by the time the hand-off returned, want the one failure WARN: %+v", len(got), got)
	}
	if n := len(sink.got()); n != 0 {
		t.Errorf("the HTTP sink got %d registrant call(s), want 0: the outbox replaces it", n)
	}
}
