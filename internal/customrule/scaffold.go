// QA scaffold for ENGI-14-04 (Mode A): exported surface only, every method fails.
// Execution deletes this file and writes customrule.go, store.go and handlers.go.
package customrule

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errNotImplemented = errors.New("customrule: not implemented")

var (
	ErrNotPermitted     = errors.New("customrule: only a workspace admin can change custom rules")
	ErrNotFound         = errors.New("customrule: custom rule not found")
	ErrCheckUnavailable = errors.New("customrule: custom rules cannot be checked right now")
)

// InvalidError is a 400: Msg is the answer body.
type InvalidError struct{ Msg string }

func (e InvalidError) Error() string { return e.Msg }

// Key is the violation key of a rule: "custom:" + ruleID.
func Key(ruleID string) string { return "" }

// RuleInput is the POST and PUT body.
type RuleInput struct {
	EntityID *string         `json:"entity_id"`
	Type     string          `json:"type"`
	Target   string          `json:"target"`
	Params   json.RawMessage `json:"params"`
	Severity string          `json:"severity"`
	When     *string         `json:"when"`
	Message  string          `json:"message"`
	Enabled  bool            `json:"enabled"`
}

// CheckInput is what the validation service's check route receives.
type CheckInput struct {
	Type     string          `json:"type"`
	Target   string          `json:"target"`
	Params   json.RawMessage `json:"params"`
	Severity string          `json:"severity"`
	When     *string         `json:"when"`
	Message  string          `json:"message"`
}

// Rule is the latest revision of a custom rule.
type Rule struct {
	ID         string          `json:"id"`
	Key        string          `json:"key"`
	Revision   int             `json:"revision"`
	RevisionID string          `json:"revision_id"`
	EntityID   *string         `json:"entity_id"`
	Type       string          `json:"type"`
	Target     string          `json:"target"`
	Params     json.RawMessage `json:"params"`
	Severity   string          `json:"severity"`
	When       *string         `json:"when"`
	Message    string          `json:"message"`
	Enabled    bool            `json:"enabled"`
	UpdatedAt  time.Time       `json:"updated_at"`
	UpdatedBy  string          `json:"updated_by"`
}

type Store struct {
	pool  *pgxpool.Pool
	check func(context.Context, CheckInput) error
}

func NewStore(pool *pgxpool.Pool, check func(context.Context, CheckInput) error) *Store {
	return &Store{pool: pool, check: check}
}

func (s *Store) List(ctx context.Context) ([]Rule, error) { return nil, errNotImplemented }

func (s *Store) Create(ctx context.Context, in RuleInput) (Rule, error) {
	return Rule{}, errNotImplemented
}

func (s *Store) Update(ctx context.Context, id string, in RuleInput) (Rule, error) {
	return Rule{}, errNotImplemented
}

func (s *Store) Delete(ctx context.Context, id string) error { return errNotImplemented }

// CurrentTx returns the latest revision of each enabled, not-deleted rule that is
// firm-wide or scoped to one of entityIDs.
func CurrentTx(ctx context.Context, tx pgx.Tx, entityIDs []string) ([]Rule, error) {
	return nil, errNotImplemented
}

// SaveLockKey is the text hashed into the tenant's save lock: 'custom_rules:' || tenant_id.
func SaveLockKey(tenantID string) string { return "" }

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, errNotImplemented.Error(), http.StatusInternalServerError)
}

func ListHandler(list func(ctx context.Context) ([]Rule, error), log *slog.Logger) http.HandlerFunc {
	return notImplemented
}

func CreateHandler(create func(ctx context.Context, in RuleInput) (Rule, error), log *slog.Logger) http.HandlerFunc {
	return notImplemented
}

func UpdateHandler(update func(ctx context.Context, id string, in RuleInput) (Rule, error), log *slog.Logger) http.HandlerFunc {
	return notImplemented
}

func DeleteHandler(del func(ctx context.Context, id string) error, log *slog.Logger) http.HandlerFunc {
	return notImplemented
}
