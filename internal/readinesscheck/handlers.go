package readinesscheck

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/publiccheck"
)

const (
	MaxRunsPerEmail = 3
	RunBudget       = 240 * time.Second
	MappingBudget   = 60 * time.Second
	MailTimeout     = 15 * time.Second
	MaxConcurrent   = 2
)

// CheckStore is what the handlers need of the summary store; *Store satisfies it.
type CheckStore interface {
	Count(ctx context.Context, email string) (int, error)
	Insert(ctx context.Context, m Summary) error
}

// Service runs one public check. runBudget overrides RunBudget in tests when non-zero.
type Service struct {
	Checker   *publiccheck.Checker
	Store     CheckStore
	Sender    accountmail.Sender
	Logger    *slog.Logger
	runBudget time.Duration
}

func NewService(checker *publiccheck.Checker, store CheckStore, sender accountmail.Sender, logger *slog.Logger) *Service {
	return &Service{Checker: checker, Store: store, Sender: sender, Logger: logger}
}

// The handlers below are red-test stubs: every one answers 501.
func notImplemented() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotImplemented) })
}

func (s *Service) RunHandler() http.Handler                              { return notImplemented() }
func (s *Service) MappingHandler() http.Handler                          { return notImplemented() }
func (s *Service) CapturedMailHandler(*accountmail.Capture) http.Handler { return notImplemented() }
