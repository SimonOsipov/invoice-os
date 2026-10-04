package notifications

import (
	"context"
	"log/slog"
	"net/http"
)

// IntakeStore is what the intake and self-read handlers need; *Store satisfies it.
type IntakeStore interface {
	Registrant(ctx context.Context, in RegistrantIntake) error
	DemoRequest(ctx context.Context, in DemoIntake) error
	Me(ctx context.Context, email string) (Contact, error)
}

var _ IntakeStore = (*Store)(nil)

// Compile-only stubs (AUTH-17-04 red phase): every handler answers 501.
func notImplemented(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotImplemented) }

func RegistrantsHandler(IntakeStore, *slog.Logger) http.HandlerFunc { return notImplemented }

func DemoRequestsHandler(IntakeStore, *slog.Logger) http.HandlerFunc { return notImplemented }

func MeHandler(IntakeStore, *slog.Logger) http.HandlerFunc { return notImplemented }
