// staff_read.go: the staff reads of ai_calls. Rows come only through staff_read_ functions in db.WithinStaffTx.
package aicalls

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	defaultLimit = 25
	maxLimit     = 100
	maxFilterLen = 64 // the purpose and outcome CHECK lengths
	dayLayout    = "2006-01-02"
)

// Filter is the staff read's filter; a zero value is absent. Failed applies to the list only, Cursor to the list and TotalsCursor to the totals.
type Filter struct {
	Limit                    int
	Cursor                   *Cursor
	TotalsCursor             *TotalsCursor
	Tenant, Purpose, Outcome string
	Failed                   bool
	From, To                 time.Time
}

type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// TotalsCursor is the last group of a totals page: Day is YYYY-MM-DD, Name the tenant name.
type TotalsCursor struct {
	Day    string `json:"day"`
	Name   string `json:"name"`
	Tenant string `json:"tenant"`
}

type Call struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenant_id"`
	TenantName   string    `json:"tenant_name"`
	CreatedAt    time.Time `json:"created_at"`
	Client       string    `json:"client"`
	Purpose      string    `json:"purpose"`
	Model        string    `json:"model"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	Cost         string    `json:"cost"`
	LatencyMS    int64     `json:"latency_ms"`
	Attempts     int       `json:"attempts"`
	Outcome      string    `json:"outcome"`
}

type Total struct {
	Day          string `json:"day"`
	TenantID     string `json:"tenant_id"`
	TenantName   string `json:"tenant_name"`
	Calls        int64  `json:"calls"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	Cost         string `json:"cost"`
	Failed       int64  `json:"failed"`
}

type PageInfo struct {
	Limit      int     `json:"limit"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}

type Page struct {
	Calls []Call   `json:"calls"`
	Page  PageInfo `json:"page"`
}

type TotalsPage struct {
	Totals []Total  `json:"totals"`
	Page   PageInfo `json:"page"`
}

// Store reads ai_calls for staff.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func EncodeCursor(c Cursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID))
}

func DecodeCursor(s string) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, err
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return Cursor{}, errors.New("aicalls: cursor has no separator")
	}
	when, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return Cursor{}, err
	}
	if _, err := uuid.Parse(id); err != nil {
		return Cursor{}, err
	}
	return Cursor{CreatedAt: when, ID: id}, nil
}

// EncodeTotalsCursor is JSON because a tenant name may hold any character.
func EncodeTotalsCursor(c TotalsCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func DecodeTotalsCursor(s string) (TotalsCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return TotalsCursor{}, err
	}
	var c TotalsCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return TotalsCursor{}, err
	}
	if _, err := time.Parse(dayLayout, c.Day); err != nil {
		return TotalsCursor{}, err
	}
	if _, err := uuid.Parse(c.Tenant); err != nil {
		return TotalsCursor{}, err
	}
	if !postgresText(c.Name) {
		return TotalsCursor{}, errors.New("aicalls: cursor name is not text")
	}
	return c, nil
}

// postgresText is false for invalid UTF-8 or a NUL rune: Postgres rejects both with 22021.
func postgresText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

// nilIfEmpty and nilIfZero give the SQL NULL of an absent filter.
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nilIfZero(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// clampLimit keeps the SQL limit positive: a negative p_limit raises 2201W.
func clampLimit(n int) int {
	if n < 1 {
		return defaultLimit
	}
	return min(n, maxLimit)
}

// StaffCalls pages ai_calls across tenants, newest first.
func (s *Store) StaffCalls(ctx context.Context, f Filter) (Page, error) {
	staff, err := db.StaffActor(ctx)
	if err != nil {
		return Page{}, err
	}
	f.Limit = clampLimit(f.Limit)
	var beforeAt, beforeID any
	if f.Cursor != nil {
		beforeAt, beforeID = f.Cursor.CreatedAt, f.Cursor.ID
	}
	calls := []Call{}
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `SELECT id::text, tenant_id::text, tenant_name, created_at, client, purpose, model,
			input_tokens, output_tokens, cost::text, latency_ms, attempts, outcome
			FROM public.staff_read_ai_calls($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			staff, nilIfEmpty(f.Tenant), nilIfEmpty(f.Purpose), nilIfEmpty(f.Outcome), f.Failed,
			nilIfZero(f.From), nilIfZero(f.To), beforeAt, beforeID, f.Limit+1)
		if err != nil {
			return db.StaffReadError("aicalls: staff calls", err)
		}
		defer r.Close()
		for r.Next() {
			var c Call
			if err := r.Scan(&c.ID, &c.TenantID, &c.TenantName, &c.CreatedAt, &c.Client, &c.Purpose, &c.Model,
				&c.InputTokens, &c.OutputTokens, &c.Cost, &c.LatencyMS, &c.Attempts, &c.Outcome); err != nil {
				return db.StaffReadError("aicalls: staff calls", err)
			}
			calls = append(calls, c)
		}
		return db.StaffReadError("aicalls: staff calls", r.Err())
	})
	if err != nil {
		return Page{}, err
	}
	page := Page{Calls: calls, Page: PageInfo{Limit: f.Limit}}
	if len(calls) > f.Limit {
		page.Calls = calls[:f.Limit]
		last := page.Calls[f.Limit-1]
		next := EncodeCursor(Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
		page.Page.HasMore, page.Page.NextCursor = true, &next
	}
	return page, nil
}

// StaffTotals pages the per-UTC-day, per-tenant totals; Failed and Cursor do not apply.
func (s *Store) StaffTotals(ctx context.Context, f Filter) (TotalsPage, error) {
	staff, err := db.StaffActor(ctx)
	if err != nil {
		return TotalsPage{}, err
	}
	f.Limit = clampLimit(f.Limit)
	var beforeDay, beforeName, beforeTenant any
	if f.TotalsCursor != nil {
		day, err := time.Parse(dayLayout, f.TotalsCursor.Day)
		if err != nil {
			return TotalsPage{}, fmt.Errorf("aicalls: staff totals: cursor day: %w", err)
		}
		beforeDay, beforeName, beforeTenant = day, f.TotalsCursor.Name, f.TotalsCursor.Tenant
	}
	totals := []Total{}
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `SELECT day::text, tenant_id::text, tenant_name, calls, input_tokens, output_tokens, cost::text, failed
			FROM public.staff_read_ai_call_totals($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			staff, nilIfEmpty(f.Tenant), nilIfEmpty(f.Purpose), nilIfEmpty(f.Outcome),
			nilIfZero(f.From), nilIfZero(f.To), beforeDay, beforeName, beforeTenant, f.Limit+1)
		if err != nil {
			return db.StaffReadError("aicalls: staff totals", err)
		}
		defer r.Close()
		for r.Next() {
			var t Total
			if err := r.Scan(&t.Day, &t.TenantID, &t.TenantName, &t.Calls, &t.InputTokens, &t.OutputTokens, &t.Cost, &t.Failed); err != nil {
				return db.StaffReadError("aicalls: staff totals", err)
			}
			totals = append(totals, t)
		}
		return db.StaffReadError("aicalls: staff totals", r.Err())
	})
	if err != nil {
		return TotalsPage{}, err
	}
	page := TotalsPage{Totals: totals, Page: PageInfo{Limit: f.Limit}}
	if len(totals) > f.Limit {
		page.Totals = totals[:f.Limit]
		last := page.Totals[f.Limit-1]
		next := EncodeTotalsCursor(TotalsCursor{Day: last.Day, Name: last.TenantName, Tenant: last.TenantID})
		page.Page.HasMore, page.Page.NextCursor = true, &next
	}
	return page, nil
}

// parseFilter returns the filter, or the message of a 400. A malformed value fails before any read.
func parseFilter(q url.Values, totals bool) (Filter, string) {
	f := Filter{Limit: defaultLimit}
	if f.Tenant = q.Get("tenant"); f.Tenant != "" {
		if _, err := uuid.Parse(f.Tenant); err != nil {
			return Filter{}, "invalid tenant"
		}
	}
	if f.Purpose = q.Get("purpose"); len(f.Purpose) > maxFilterLen {
		return Filter{}, "purpose is too long"
	}
	if !postgresText(f.Purpose) {
		return Filter{}, "purpose is not valid text"
	}
	if f.Outcome = q.Get("outcome"); len(f.Outcome) > maxFilterLen {
		return Filter{}, "outcome is too long"
	}
	if !postgresText(f.Outcome) {
		return Filter{}, "outcome is not valid text"
	}
	if raw := q.Get("failed"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return Filter{}, "failed must be true or false"
		}
		f.Failed = b
	}
	for _, tc := range []struct {
		name string
		dst  *time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		raw := q.Get(tc.name)
		if raw == "" {
			continue
		}
		when, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return Filter{}, tc.name + " must be an RFC3339 timestamp"
		}
		*tc.dst = when
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.From.After(f.To) {
		return Filter{}, "from must not be after to"
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return Filter{}, "limit must be an integer"
		}
		if n < 1 {
			return Filter{}, "limit must be >= 1"
		}
		f.Limit = min(n, maxLimit)
	}
	if raw := q.Get("cursor"); raw != "" {
		if totals {
			c, err := DecodeTotalsCursor(raw)
			if err != nil {
				return Filter{}, "cursor is malformed"
			}
			f.TotalsCursor = &c
		} else {
			c, err := DecodeCursor(raw)
			if err != nil {
				return Filter{}, "cursor is malformed"
			}
			f.Cursor = &c
		}
	}
	return f, ""
}

// StaffCallsHandler repeats no staff check: auth.RequireStaff runs before it.
func StaffCallsHandler(list func(context.Context, Filter) (Page, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		f, ok := readFilter(w, r, false)
		if !ok {
			return
		}
		page, err := list(r.Context(), f)
		if !writeStaffError(w, r, log, "aicalls: staff calls", err) {
			return
		}
		if page.Calls == nil {
			page.Calls = []Call{}
		}
		page.Page.Limit = f.Limit
		writeJSON(w, http.StatusOK, page)
	}
}

// StaffTotalsHandler repeats no staff check: auth.RequireStaff runs before it.
func StaffTotalsHandler(totals func(context.Context, Filter) (TotalsPage, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		f, ok := readFilter(w, r, true)
		if !ok {
			return
		}
		page, err := totals(r.Context(), f)
		if !writeStaffError(w, r, log, "aicalls: staff totals", err) {
			return
		}
		if page.Totals == nil {
			page.Totals = []Total{}
		}
		page.Page.Limit = f.Limit
		writeJSON(w, http.StatusOK, page)
	}
}

// readFilter answers a 400 itself and reports false. URL.Query drops a malformed escape silently, which would widen the read.
func readFilter(w http.ResponseWriter, r *http.Request, totals bool) (Filter, bool) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid query string")
		return Filter{}, false
	}
	f, msg := parseFilter(q, totals)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return Filter{}, false
	}
	return f, true
}

// writeStaffError answers a store error and reports false; it reports true for nil.
func writeStaffError(w http.ResponseWriter, r *http.Request, log *slog.Logger, op string, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, db.ErrNotStaff):
		writeError(w, http.StatusForbidden, "forbidden")
	default:
		log.ErrorContext(r.Context(), op, slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
