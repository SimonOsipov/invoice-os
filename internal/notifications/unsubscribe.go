package notifications

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// STUB (FREECHK-09-05 Test-Spec): compile-only symbols so the red tests fail on their assertions.
// The executor replaces this file whole.

type UnsubscribeStore struct{ pool *pgxpool.Pool }

func NewUnsubscribeStore(pool *pgxpool.Pool) *UnsubscribeStore { return &UnsubscribeStore{pool: pool} }

func (s *UnsubscribeStore) Lookup(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func (s *UnsubscribeStore) Press(context.Context, string, ResendClient) (string, error) {
	return "", nil
}

func maskEmail(string) string { return "" }

func UnsubscribeLookupHandler(*UnsubscribeStore, *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotImplemented) }
}

func UnsubscribeHandler(*UnsubscribeStore, ResendClient, *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotImplemented) }
}

func (r *Resend) OptOutMarketing(context.Context, string) error { return nil }

func (FakeResend) OptOutMarketing(context.Context, string) error { return nil }
