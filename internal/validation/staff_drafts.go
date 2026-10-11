// Staff draft routes under /v1/staff/rule-versions/draft: open, add, edit, remove, publish.
// The platform admits only a rules-role caller. Each write runs a rule_draft_* function and
// its staff audit row in one WithinStaffTx; this file is the only Go caller of those functions.
package validation

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	maxDraftRuleBodyBytes = 32 << 10
	maxPublishBodyBytes   = 8 << 10
	maxTestBodyBytes      = 1 << 20
	maxDraftParamsBytes   = 16 << 10
	maxDraftKeyLen        = 64
	maxDraftTargetRunes   = 200
	maxDraftWhenRunes     = 2000
	maxDraftMessageRunes  = 500
	oneDraftIndex         = "rule_set_versions_one_draft"
	startDateMessage      = "start date must be today or later"
)

var (
	// ErrNoDraft: no unsealed version exists.
	ErrNoDraft = errors.New("validation: no draft")
	// ErrDraftExists: a draft is already open.
	ErrDraftExists = errors.New("validation: a draft already exists")
	// ErrRuleInDraft: an add named a key the draft already holds.
	ErrRuleInDraft = errors.New("validation: rule already in the draft")
	// ErrRuleNotInDraft: an edit or remove named a key the draft lacks.
	ErrRuleNotInDraft = errors.New("validation: no such rule in the draft")
	// ErrDraftInvalid wraps the text of a draft that cannot be published.
	ErrDraftInvalid = errors.New("draft invalid")
	// ErrStartDateInPast: the start date is before today (UTC).
	ErrStartDateInPast = errors.New("validation: " + startDateMessage)
)

//go:embed sample_invoice.json
var sampleInvoiceJSON []byte

var draftKeyPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// DraftOpened is the POST /v1/staff/rule-versions/draft body.
type DraftOpened struct {
	RuleSetVersionID uuid.UUID `json:"rule_set_version_id"`
	Version          int       `json:"version"`
	FromVersion      int       `json:"from_version"`
}

// DraftRuleResult is the add and remove body.
type DraftRuleResult struct {
	Key              string    `json:"key"`
	RuleSetVersion   int       `json:"rule_set_version"`
	RuleSetVersionID uuid.UUID `json:"rule_set_version_id"`
}

// DraftRuleEdited is the edit body.
type DraftRuleEdited struct {
	DraftRuleResult
	Changed bool `json:"changed"`
}

// DraftPublished is the publish body.
type DraftPublished struct {
	RuleSetVersionID uuid.UUID `json:"rule_set_version_id"`
	Version          int       `json:"version"`
	EffectiveFrom    string    `json:"effective_from"`
	RuleCount        int       `json:"rule_count"`
}

type draftRuleRequest struct {
	Type     string          `json:"type"`
	Target   string          `json:"target"`
	Params   json.RawMessage `json:"params"`
	Severity string          `json:"severity"`
	When     *string         `json:"when"`
	Message  string          `json:"message"`
	Enabled  *bool           `json:"enabled"`
}

type addDraftRuleRequest struct {
	Key string `json:"key"`
	draftRuleRequest
}

// validated is a rule body that passed validateDraftRule.
type validated struct {
	typ, target, severity, message string
	params                         string
	when                           *string
	enabled                        bool
}

func (v validated) auditRule() map[string]any {
	return map[string]any{
		"type": v.typ, "target": v.target, "params": json.RawMessage(v.params),
		"severity": v.severity, "when": v.when, "message": v.message, "enabled": v.enabled,
	}
}

// validateDraftRule runs the static checks on a rule body before any SQL.
// ceiling: static checks only; a parameter fault in a branch neither publish probe reaches passes publish and fails loud at validation time. The kill switch is the rescue.
func validateDraftRule(key string, in draftRuleRequest) (validated, error) {
	var v validated
	if len(key) > maxDraftKeyLen || !draftKeyPattern.MatchString(key) {
		return v, fmt.Errorf("key must be kebab-case, 1 to %d characters", maxDraftKeyLen)
	}
	if _, ok := NewDefaultEngine().registry[RuleType(in.Type)]; !ok {
		return v, errors.New("unknown rule type")
	}
	switch in.Severity {
	case "error", "warning", "info":
	default:
		return v, errors.New("severity must be error, warning or info")
	}
	if utf8.RuneCountInString(in.Target) > maxDraftTargetRunes {
		return v, fmt.Errorf("target must be at most %d characters", maxDraftTargetRunes)
	}
	params := map[string]any{}
	v.params = "{}"
	if raw := strings.TrimSpace(string(in.Params)); raw != "" && raw != "null" {
		if len(raw) > maxDraftParamsBytes {
			return v, fmt.Errorf("params must be at most %d bytes", maxDraftParamsBytes)
		}
		if err := json.Unmarshal([]byte(raw), &params); err != nil || params == nil {
			return v, errors.New("params must be a JSON object")
		}
		v.params = raw
	}
	if in.When != nil {
		w := *in.When
		if strings.TrimSpace(w) == "" || utf8.RuneCountInString(w) > maxDraftWhenRunes {
			return v, fmt.Errorf("when must be 1 to %d characters", maxDraftWhenRunes)
		}
		if err := compileCEL(w); err != nil {
			return v, errors.New("when does not compile")
		}
		v.when = in.When
	}
	switch RuleType(in.Type) {
	case TypeCEL:
		expr, _ := params["expr"].(string)
		if strings.TrimSpace(expr) == "" {
			return v, errors.New("a cel rule needs params.expr")
		}
		if err := compileCEL(expr); err != nil {
			return v, errors.New("params.expr does not compile")
		}
	case TypeFormat:
		pattern, ok := params["pattern"].(string)
		if !ok {
			return v, errors.New("a format/regex rule needs params.pattern")
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return v, errors.New("params.pattern does not compile")
		}
	}
	v.message = strings.TrimSpace(in.Message)
	if n := utf8.RuneCountInString(v.message); n < 1 || n > maxDraftMessageRunes {
		return v, fmt.Errorf("message must be 1 to %d characters", maxDraftMessageRunes)
	}
	if in.Enabled == nil {
		return v, errors.New("enabled is required")
	}
	v.typ, v.target, v.severity, v.enabled = in.Type, in.Target, in.Severity, *in.Enabled
	return v, nil
}

func staffActorOf(ctx context.Context) (uuid.UUID, error) {
	id, ok := auth.StaffFromContext(ctx)
	if !ok {
		return uuid.Nil, db.ErrNotStaff
	}
	actor, err := uuid.Parse(id.Subject)
	if err != nil {
		return uuid.Nil, db.ErrNotStaff
	}
	return actor, nil
}

// draftSQLError maps a rule_draft_* SQLSTATE; other errors pass through wrapped.
func draftSQLError(op string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "42501":
			return db.ErrNotStaff
		case pgErr.Code == "23505" && pgErr.ConstraintName == oneDraftIndex:
			return ErrDraftExists
		case pgErr.Code == "22023":
			return ErrStartDateInPast
		case pgErr.Code == "23514":
			return fmt.Errorf("%w: the draft has no rules", ErrDraftInvalid)
		}
	}
	return fmt.Errorf("validation: %s: %w", op, err)
}

// OpenDraft copies the version in force into a new draft.
func (s *Store) OpenDraft(ctx context.Context) (DraftOpened, error) {
	actor, err := staffActorOf(ctx)
	if err != nil {
		return DraftOpened{}, err
	}
	var res DraftOpened
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`SELECT rule_set_version_id, version, from_version FROM public.rule_draft_open($1)`, actor,
		).Scan(&res.RuleSetVersionID, &res.Version, &res.FromVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoActiveRuleSet
		}
		if err != nil {
			return draftSQLError("open draft", err)
		}
		return recordStaff(ctx, tx, actor, res.RuleSetVersionID, "validation.rule_set.draft_opened",
			map[string]any{"version": res.Version, "from_version": res.FromVersion})
	})
	if err != nil {
		return DraftOpened{}, err
	}
	return res, nil
}

type putResult struct {
	id               uuid.UUID
	version          int
	existed, changed bool
}

func putDraftRule(ctx context.Context, tx pgx.Tx, actor uuid.UUID, key string, r validated, create bool) (putResult, error) {
	var p putResult
	err := tx.QueryRow(ctx,
		`SELECT rule_set_version_id, version, existed, changed FROM public.rule_draft_put_rule($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10)`,
		actor, key, r.typ, r.target, r.params, r.severity, r.when, r.message, r.enabled, create,
	).Scan(&p.id, &p.version, &p.existed, &p.changed)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNoDraft
	}
	if err != nil {
		return p, draftSQLError("put draft rule", err)
	}
	return p, nil
}

// AddDraftRule inserts a rule into the draft. A key the draft holds is ErrRuleInDraft.
func (s *Store) AddDraftRule(ctx context.Context, key string, r validated) (DraftRuleResult, error) {
	actor, err := staffActorOf(ctx)
	if err != nil {
		return DraftRuleResult{}, err
	}
	var res DraftRuleResult
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := putDraftRule(ctx, tx, actor, key, r, true)
		if err != nil {
			return err
		}
		if p.existed {
			return ErrRuleInDraft
		}
		res = DraftRuleResult{Key: key, RuleSetVersion: p.version, RuleSetVersionID: p.id}
		return recordStaff(ctx, tx, actor, p.id, "validation.rule_set.rule_added",
			map[string]any{"version": p.version, "key": key, "rule": r.auditRule()})
	})
	if err != nil {
		return DraftRuleResult{}, err
	}
	return res, nil
}

// EditDraftRule rewrites a rule of the draft. An absent key is ErrRuleNotInDraft; no change writes no audit row.
// ceiling: two staff editing one rule at once is last-write-wins; each audit row carries the full rule. Revisit when a second rules editor works at the same time.
func (s *Store) EditDraftRule(ctx context.Context, key string, r validated) (DraftRuleEdited, error) {
	actor, err := staffActorOf(ctx)
	if err != nil {
		return DraftRuleEdited{}, err
	}
	var res DraftRuleEdited
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := putDraftRule(ctx, tx, actor, key, r, false)
		if err != nil {
			return err
		}
		if !p.existed {
			return ErrRuleNotInDraft
		}
		res = DraftRuleEdited{DraftRuleResult{Key: key, RuleSetVersion: p.version, RuleSetVersionID: p.id}, p.changed}
		if !p.changed {
			return nil
		}
		return recordStaff(ctx, tx, actor, p.id, "validation.rule_set.rule_changed",
			map[string]any{"version": p.version, "key": key, "rule": r.auditRule()})
	})
	if err != nil {
		return DraftRuleEdited{}, err
	}
	return res, nil
}

// RemoveDraftRule deletes a rule from the draft. An absent key is ErrRuleNotInDraft.
func (s *Store) RemoveDraftRule(ctx context.Context, key string) (DraftRuleResult, error) {
	actor, err := staffActorOf(ctx)
	if err != nil {
		return DraftRuleResult{}, err
	}
	var res DraftRuleResult
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		var removed bool
		err := tx.QueryRow(ctx,
			`SELECT rule_set_version_id, version, removed FROM public.rule_draft_remove_rule($1, $2)`, actor, key,
		).Scan(&res.RuleSetVersionID, &res.RuleSetVersion, &removed)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoDraft
		}
		if err != nil {
			return draftSQLError("remove draft rule", err)
		}
		if !removed {
			return ErrRuleNotInDraft
		}
		res.Key = key
		return recordStaff(ctx, tx, actor, res.RuleSetVersionID, "validation.rule_set.rule_removed",
			map[string]any{"version": res.RuleSetVersion, "key": key})
	})
	if err != nil {
		return DraftRuleResult{}, err
	}
	return res, nil
}

// draftIDTx reads the id of the unsealed version; none is ErrNoDraft.
func draftIDTx(ctx context.Context, tx pgx.Tx) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM rule_set_versions WHERE NOT sealed`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoDraft
	}
	if err != nil {
		return "", fmt.Errorf("validation: read draft: %w", err)
	}
	return id, nil
}

func draftFault(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrDraftInvalid, fmt.Sprintf(format, args...))
}

// PublishDraft seals the draft and dates it from. Under the draft lock it first evaluates
// the draft on an empty invoice and on the sample; a fault is ErrDraftInvalid.
func (s *Store) PublishDraft(ctx context.Context, eng *Engine, from time.Time) (DraftPublished, error) {
	actor, err := staffActorOf(ctx)
	if err != nil {
		return DraftPublished{}, err
	}
	var sample Payload
	if err := json.Unmarshal(sampleInvoiceJSON, &sample); err != nil {
		return DraftPublished{}, fmt.Errorf("validation: embedded sample invoice: %w", err)
	}
	date := from.Format("2006-01-02")
	res := DraftPublished{EffectiveFrom: date}
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('rule_set_versions:draft'))`); err != nil {
			return fmt.Errorf("validation: lock draft: %w", err)
		}
		draftID, err := draftIDTx(ctx, tx)
		if err != nil {
			return err
		}
		rs, err := loadRuleSetByIDTx(ctx, tx, draftID)
		switch {
		case errors.Is(err, ErrCodeListMissing):
			return draftFault("%s", err.Error())
		case errors.Is(err, ErrEmptyRuleSet):
			return draftFault("the draft has no rules")
		case err != nil:
			return fmt.Errorf("validation: load draft: %w", err)
		}
		for _, p := range []Payload{{"invoice": map[string]any{}}, sample} {
			if _, err := eng.Evaluate(p, rs); err != nil {
				return draftFault("the draft does not evaluate: %s", err.Error())
			}
		}
		err = tx.QueryRow(ctx,
			`SELECT rule_set_version_id, version, rule_count FROM public.rule_draft_publish($1, $2::date)`, actor, date,
		).Scan(&res.RuleSetVersionID, &res.Version, &res.RuleCount)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoDraft
		}
		if err != nil {
			return draftSQLError("publish draft", err)
		}
		return recordStaff(ctx, tx, actor, res.RuleSetVersionID, "validation.rule_set.published",
			map[string]any{"version": res.Version, "effective_from": date, "rule_count": res.RuleCount})
	})
	if err != nil {
		return DraftPublished{}, err
	}
	return res, nil
}

// DraftTestSide is the draft's half of a test result; Error holds an evaluation fault.
type DraftTestSide struct {
	RuleSetVersion   int         `json:"rule_set_version"`
	RuleSetVersionID uuid.UUID   `json:"rule_set_version_id"`
	Violations       []Violation `json:"violations"`
	Error            *string     `json:"error"`
}

// InForceTestSide is the half for the version in force today.
type InForceTestSide struct {
	RuleSetVersion   int         `json:"rule_set_version"`
	RuleSetVersionID uuid.UUID   `json:"rule_set_version_id"`
	Violations       []Violation `json:"violations"`
}

// DraftTestResult is the POST /v1/staff/rule-versions/draft/test body.
type DraftTestResult struct {
	Draft   DraftTestSide   `json:"draft"`
	InForce InForceTestSide `json:"in_force"`
}

// TestDraft evaluates the invoice against the draft and the version in force today. It writes nothing.
// A draft that cannot load its code lists or evaluate fills Draft.Error; an in-force fault is an error.
func (s *Store) TestDraft(ctx context.Context, eng *Engine, invoice map[string]any) (DraftTestResult, error) {
	if _, err := staffActorOf(ctx); err != nil {
		return DraftTestResult{}, err
	}
	var res DraftTestResult
	err := db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		draftID, err := draftIDTx(ctx, tx)
		if err != nil {
			return err
		}
		draft, err := loadRuleSetByIDTx(ctx, tx, draftID)
		var draftErr error
		switch {
		case errors.Is(err, ErrEmptyRuleSet):
			return draftFault("the draft has no rules")
		case errors.Is(err, ErrCodeListMissing):
			draftErr = err
		case err != nil:
			return fmt.Errorf("validation: load draft: %w", err)
		}
		inForce, err := loadTodayTx(ctx, tx)
		if err != nil {
			return err
		}
		res.Draft = DraftTestSide{RuleSetVersion: draft.Version, Violations: []Violation{}}
		res.Draft.RuleSetVersionID, _ = uuid.Parse(draftID)
		if draftErr == nil {
			var r Result
			if r, draftErr = eng.Evaluate(Payload{"invoice": invoice}, draft); draftErr == nil {
				res.Draft.Violations = r.Violations
			}
		}
		if draftErr != nil {
			msg := draftErr.Error()
			res.Draft.Error = &msg
		}
		r, err := eng.Evaluate(Payload{"invoice": invoice}, inForce)
		if err != nil {
			return fmt.Errorf("validation: evaluate version in force: %w", err)
		}
		res.InForce = InForceTestSide{RuleSetVersion: inForce.Version, Violations: r.Violations}
		res.InForce.RuleSetVersionID, _ = uuid.Parse(inForce.ID)
		return nil
	})
	if err != nil {
		return DraftTestResult{}, err
	}
	return res, nil
}

// draftsError maps a draft error to a status and message; everything else falls to staffRulesError.
func draftsError(err error) (int, string) {
	switch {
	case errors.Is(err, ErrNoDraft):
		return http.StatusNotFound, "no draft"
	case errors.Is(err, ErrDraftExists):
		return http.StatusConflict, "a draft already exists"
	case errors.Is(err, ErrRuleInDraft):
		return http.StatusConflict, "rule already in the draft"
	case errors.Is(err, ErrRuleNotInDraft):
		return http.StatusNotFound, "no such rule in the draft"
	case errors.Is(err, ErrStartDateInPast):
		return http.StatusBadRequest, startDateMessage
	case errors.Is(err, ErrDraftInvalid):
		return http.StatusConflict, strings.TrimPrefix(err.Error(), ErrDraftInvalid.Error()+": ")
	}
	return staffRulesError(err)
}

func writeDraftError(w http.ResponseWriter, r *http.Request, log *slog.Logger, op string, err error) {
	status, msg := draftsError(err)
	if status == http.StatusInternalServerError {
		log.ErrorContext(r.Context(), "validation: staff "+op, slog.Any("err", err))
	}
	writeError(w, status, msg)
}

// decodeDraftBody reads one JSON value of at most limit bytes into v; it answers 413 or 400 itself.
func decodeDraftBody(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		if err = dec.Decode(&struct{}{}); errors.Is(err, io.EOF) {
			err = nil
		} else if err == nil {
			err = errors.New("trailing data")
		}
	}
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// StaffOpenDraftHandler serves POST /v1/staff/rule-versions/draft.
func StaffOpenDraftHandler(open func(ctx context.Context) (DraftOpened, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		res, err := open(r.Context())
		if err != nil {
			writeDraftError(w, r, log, "open draft", err)
			return
		}
		writeJSON(w, http.StatusCreated, res)
	}
}

// StaffAddDraftRuleHandler serves POST /v1/staff/rule-versions/draft/rules.
func StaffAddDraftRuleHandler(add func(ctx context.Context, key string, r validated) (DraftRuleResult, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req addDraftRuleRequest
		if !decodeDraftBody(w, r, maxDraftRuleBodyBytes, &req) {
			return
		}
		rule, err := validateDraftRule(req.Key, req.draftRuleRequest)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		res, err := add(r.Context(), req.Key, rule)
		if err != nil {
			writeDraftError(w, r, log, "add draft rule", err)
			return
		}
		writeJSON(w, http.StatusCreated, res)
	}
}

// StaffEditDraftRuleHandler serves PUT /v1/staff/rule-versions/draft/rules/{key}.
func StaffEditDraftRuleHandler(edit func(ctx context.Context, key string, r validated) (DraftRuleEdited, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req draftRuleRequest
		if !decodeDraftBody(w, r, maxDraftRuleBodyBytes, &req) {
			return
		}
		key := r.PathValue("key")
		rule, err := validateDraftRule(key, req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		res, err := edit(r.Context(), key, rule)
		if err != nil {
			writeDraftError(w, r, log, "edit draft rule", err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

// StaffRemoveDraftRuleHandler serves DELETE /v1/staff/rule-versions/draft/rules/{key}.
func StaffRemoveDraftRuleHandler(remove func(ctx context.Context, key string) (DraftRuleResult, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		res, err := remove(r.Context(), r.PathValue("key"))
		if err != nil {
			writeDraftError(w, r, log, "remove draft rule", err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

type publishRequest struct {
	EffectiveFrom string `json:"effective_from"`
}

// StaffPublishDraftHandler serves POST /v1/staff/rule-versions/draft/publish.
func StaffPublishDraftHandler(publish func(ctx context.Context, from time.Time) (DraftPublished, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req publishRequest
		if !decodeDraftBody(w, r, maxPublishBodyBytes, &req) {
			return
		}
		from, err := time.Parse("2006-01-02", req.EffectiveFrom)
		if err != nil {
			writeError(w, http.StatusBadRequest, "effective_from must be a date, YYYY-MM-DD")
			return
		}
		if today := time.Now().UTC().Truncate(24 * time.Hour); from.Before(today) {
			writeError(w, http.StatusBadRequest, startDateMessage)
			return
		}
		res, err := publish(r.Context(), from)
		if err != nil {
			writeDraftError(w, r, log, "publish draft", err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

type testRequest struct {
	Invoice json.RawMessage `json:"invoice"`
}

// StaffTestDraftHandler serves POST /v1/staff/rule-versions/draft/test.
func StaffTestDraftHandler(test func(ctx context.Context, invoice map[string]any) (DraftTestResult, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req testRequest
		if !decodeDraftBody(w, r, maxTestBodyBytes, &req) {
			return
		}
		var invoice map[string]any
		if raw := strings.TrimSpace(string(req.Invoice)); !strings.HasPrefix(raw, "{") || json.Unmarshal(req.Invoice, &invoice) != nil {
			writeError(w, http.StatusBadRequest, "invoice must be an object")
			return
		}
		res, err := test(r.Context(), invoice)
		if err != nil {
			writeDraftError(w, r, log, "test draft", err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}
