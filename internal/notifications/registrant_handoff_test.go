package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// GKE-05 AC 5: a gateway row becomes one River job and leaves the outbox in the same move, and the
// job records the contact. Retries are River's.
func TestRegistrantHandoff_MovesTheRowAndTheJobRecordsTheContact(t *testing.T) {
	e := newEnv(t)
	gwURL := os.Getenv("DATABASE_GATEWAY_URL")
	if gwURL == "" {
		t.Fatal("DATABASE_GATEWAY_URL is not set; the row must be written as invoice_gateway, as the gateway writes it")
	}
	ctx := context.Background()
	gw, err := pgxpool.New(ctx, gwURL)
	if err != nil {
		t.Fatalf("gateway pool: %v", err)
	}
	t.Cleanup(gw.Close)

	email := uniqueEmail(t, e, "handoff")
	userID := uuid.NewString()
	t.Cleanup(func() {
		_, _ = e.admin.Exec(context.Background(), `DELETE FROM river_job WHERE kind = 'registrant_intake' AND args->>'email' = $1`, email)
		_, _ = e.admin.Exec(context.Background(), `DELETE FROM registrant_handoffs WHERE email = $1`, email)
	})
	if _, err := gw.Exec(ctx, `INSERT INTO registrant_handoffs (user_id, email, display_name, workspace_name, consent_text, consent_at)
		VALUES ($1, $2, 'Ada Obi', 'Obi & Co', $3, '2026-10-22T10:00:00Z')`, userID, email, consentText); err != nil {
		t.Fatalf("insert as invoice_gateway: %v", err)
	}

	if err := MoveRegistrantHandoffs(e.app, e.store.river)(ctx); err != nil {
		t.Fatalf("move: %v", err)
	}
	var left int
	if err := e.admin.QueryRow(ctx, `SELECT count(*) FROM registrant_handoffs WHERE email = $1`, email).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d outbox row(s) left after the move, want 0", left)
	}
	var raw []byte
	var queue string
	if err := e.admin.QueryRow(ctx, `SELECT args, queue FROM river_job WHERE kind = 'registrant_intake' AND args->>'email' = $1`, email).Scan(&raw, &queue); err != nil {
		t.Fatalf("read the queued job: %v", err)
	}
	if queue != river.QueueDefault {
		t.Errorf("job queue = %q, want %q: the contacts queue has no fetcher in off mode", queue, river.QueueDefault)
	}
	var args RegistrantArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatal(err)
	}
	want := RegistrantArgs{UserID: userID, Email: email, DisplayName: "Ada Obi", WorkspaceName: "Obi & Co", ConsentText: consentText, ConsentAt: "2026-10-22T10:00:00Z"}
	if args != want {
		t.Errorf("job args = %+v, want %+v", args, want)
	}

	// A second move finds nothing: the row went with the first.
	if err := MoveRegistrantHandoffs(e.app, e.store.river)(ctx); err != nil {
		t.Fatalf("second move: %v", err)
	}
	var jobs int
	if err := e.admin.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'registrant_intake' AND args->>'email' = $1`, email).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Errorf("%d job(s) after two moves, want 1", jobs)
	}

	w := &RegistrantWorker{Store: e.store, Logger: slog.Default()}
	if err := w.Work(ctx, &river.Job[RegistrantArgs]{Args: args}); err != nil {
		t.Fatalf("Work: %v", err)
	}
	var gotUser, company string
	var consented bool
	if err := e.admin.QueryRow(ctx, `SELECT user_id::text, company, marketing_consented_at IS NOT NULL FROM contacts WHERE email = $1`, email).
		Scan(&gotUser, &company, &consented); err != nil {
		t.Fatalf("read the contact: %v", err)
	}
	if gotUser != userID || company != "Obi & Co" || !consented {
		t.Errorf("contact = user %q company %q consented %v, want %q, \"Obi & Co\", true", gotUser, company, consented, userID)
	}
}

// A job without an email or with a malformed user id is cancelled, not retried.
func TestRegistrantWorker_CancelsAnUnusableJob(t *testing.T) {
	w := &RegistrantWorker{Logger: slog.Default()}
	for _, a := range []RegistrantArgs{{Email: " "}, {Email: "a@corp.example", UserID: "not-a-uuid"}} {
		var cancel *river.JobCancelError
		err := w.Work(context.Background(), &river.Job[RegistrantArgs]{Args: a})
		if !errors.As(err, &cancel) {
			t.Errorf("Work(%+v) = %v, want a JobCancel", a, err)
		}
	}
}
