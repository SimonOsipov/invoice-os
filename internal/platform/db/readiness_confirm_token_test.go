package db_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/readinesscheck"
)

type noMerge struct{}

func (noMerge) ReadinessCheckTx(context.Context, pgx.Tx, string, string) error { return nil }

func rcSet(email, token string) (bool, error) {
	return db.SetReadinessConfirmToken(context.Background(), os.Getenv("DATABASE_MIGRATION_URL"), email, token)
}

func rcHold(t *testing.T, s *readinesscheck.Store, email string) string {
	t.Helper()
	tok, err := s.Hold(context.Background(), readinesscheck.Held{Email: email, Summary: readinesscheck.Summary{FileType: "csv"}})
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	return tok
}

func rcConfirm(s *readinesscheck.Store, token string) error {
	_, _, err := s.Confirm(context.Background(), token, "consent", func(context.Context, readinesscheck.Held, int) error { return nil })
	return err
}

func rcStore(t *testing.T) (*readinesscheck.Store, string) {
	t.Helper()
	rcrRequireTable(t)
	return &readinesscheck.Store{Pool: h.app, Contacts: noMerge{}}, rcrEmail(t)
}

func TestSetReadinessConfirmToken_ThePressAcceptsTheNewToken(t *testing.T) {
	s, email := rcStore(t)
	t1 := rcHold(t, s, email)
	t2 := newToken(t)
	if ok, err := rcSet(" "+email+" ", t2); err != nil || !ok {
		t.Fatalf("SetReadinessConfirmToken = (%v, %v), want (true, nil)", ok, err)
	}
	if err := rcConfirm(s, t1); !errors.Is(err, readinesscheck.ErrLinkInvalid) {
		t.Errorf("Confirm(old token) = %v, want ErrLinkInvalid", err)
	}
	if err := rcConfirm(s, t2); err != nil {
		t.Errorf("Confirm(new token) = %v, want nil", err)
	}
}

func TestSetReadinessConfirmToken_ChangesOnlyTheNewestRow(t *testing.T) {
	s, email := rcStore(t)
	t1 := rcHold(t, s, email)
	t2 := rcHold(t, s, email)
	t3 := newToken(t)
	if ok, err := rcSet(email, t3); err != nil || !ok {
		t.Fatalf("SetReadinessConfirmToken = (%v, %v), want (true, nil)", ok, err)
	}
	if err := rcConfirm(s, t2); !errors.Is(err, readinesscheck.ErrLinkInvalid) {
		t.Errorf("Confirm(T2) = %v, want ErrLinkInvalid: the newest row's old token must be gone", err)
	}
	for name, tok := range map[string]string{"T3": t3, "T1": t1} {
		if err := rcConfirm(s, tok); err != nil {
			t.Errorf("Confirm(%s) = %v, want nil", name, err)
		}
	}
}

func TestSetReadinessConfirmToken_NoLiveRowIsFalse(t *testing.T) {
	s, email := rcStore(t)
	if ok, err := rcSet(email, newToken(t)); err != nil || ok {
		t.Fatalf("no row: SetReadinessConfirmToken = (%v, %v), want (false, nil)", ok, err)
	}
	old := rcHold(t, s, email)
	if _, err := h.super.Exec(context.Background(),
		`UPDATE readiness_check_requests SET expires_at = now() - interval '1 minute' WHERE email = $1`, email); err != nil {
		t.Fatal(err)
	}
	if ok, err := rcSet(email, newToken(t)); err != nil || ok {
		t.Fatalf("expired row: SetReadinessConfirmToken = (%v, %v), want (false, nil)", ok, err)
	}
	var n int
	if err := h.super.QueryRow(context.Background(),
		`SELECT count(*) FROM readiness_check_requests WHERE email = $1 AND token_hash = sha256(convert_to($2, 'UTF8'))`, email, old).Scan(&n); err != nil || n != 1 {
		t.Errorf("the expired row's hash changed (count %d, err %v)", n, err)
	}
}
