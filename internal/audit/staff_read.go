// staff_read.go: the staff reads of the audit logs. Rows come only through staff_read_ functions in db.WithinStaffTx.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/actor"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// StaffFilter is the staff search's filter; a zero value is absent. Tenant is a uuid, Actor the stored value.
type StaffFilter struct {
	Limit    int
	Cursor   *Cursor
	Tenant   string
	Event    string
	Actor    string
	From, To time.Time
}

// StaffRow is one audit_log row of any tenant. Actor is the stored value; ActorName and ActorKind are its label.
type StaffRow struct {
	ID         string          `json:"id"`
	TenantID   string          `json:"tenant_id"`
	TenantName *string         `json:"tenant_name"`
	CreatedAt  time.Time       `json:"created_at"`
	Event      string          `json:"event"`
	Actor      string          `json:"actor"`
	ActorName  string          `json:"actor_name"`
	ActorKind  string          `json:"actor_kind"`
	Payload    json.RawMessage `json:"payload"`
	seq        int64
}

type StaffPage struct {
	Events []StaffRow `json:"events"`
	Page   PageInfo   `json:"page"`
}

// StaffChange is one staff_audit_log row; TenantID and RuleSetVersionID are null where the change has none.
type StaffChange struct {
	ID               string          `json:"id"`
	Actor            string          `json:"actor"`
	Event            string          `json:"event"`
	RuleSetVersionID *string         `json:"rule_set_version_id"`
	TenantID         *string         `json:"tenant_id"`
	TenantName       *string         `json:"tenant_name"`
	SubjectID        *string         `json:"subject_id"`
	Payload          json.RawMessage `json:"payload"`
	CreatedAt        time.Time       `json:"created_at"`
}

// StaffSearch pages the audit log across tenants, newest first. The caller's Limit must be >= 1.
func (s *Store) StaffSearch(ctx context.Context, f StaffFilter) (StaffPage, error) {
	staff, err := db.StaffActor(ctx)
	if err != nil {
		return StaffPage{}, err
	}
	var (
		tenant, event, subject, from, to, beforeAt, beforeID any
	)
	if f.Tenant != "" {
		tenant = f.Tenant
	}
	if f.Event != "" {
		event = f.Event
	}
	if f.Actor != "" {
		subject = f.Actor
	}
	if !f.From.IsZero() {
		from = f.From
	}
	if !f.To.IsZero() {
		to = f.To
	}
	if f.Cursor != nil {
		beforeAt, beforeID = f.Cursor.CreatedAt, f.Cursor.ID
	}

	rows := []StaffRow{}
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `SELECT id, tenant_id::text, tenant_name, created_at, event, actor, member_name, member_email, payload
			FROM public.staff_read_audit_log($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			staff, tenant, event, subject, from, to, beforeAt, beforeID, f.Limit+1)
		if err != nil {
			return db.StaffReadError("audit: staff search", err)
		}
		defer r.Close()
		for r.Next() {
			var (
				row                 StaffRow
				memberName, memberE *string
			)
			if err := r.Scan(&row.seq, &row.TenantID, &row.TenantName, &row.CreatedAt, &row.Event, &row.Actor,
				&memberName, &memberE, &row.Payload); err != nil {
				return db.StaffReadError("audit: staff search", err)
			}
			row.ID = strconv.FormatInt(row.seq, 10)
			label := actor.Label{Text: "System", Kind: actor.KindSystem}
			if row.Actor != "system" {
				label = actor.Name(memberName, memberE, row.Actor)
			}
			row.ActorName, row.ActorKind = label.Text, string(label.Kind)
			rows = append(rows, row)
		}
		return db.StaffReadError("audit: staff search", r.Err())
	})
	if err != nil {
		return StaffPage{}, err
	}

	page := StaffPage{Events: rows, Page: PageInfo{Limit: f.Limit}}
	if len(rows) > f.Limit {
		page.Events = rows[:f.Limit]
		last := page.Events[f.Limit-1]
		next := EncodeCursor(last.CreatedAt, last.seq)
		page.Page.HasMore, page.Page.NextCursor = true, &next
	}
	return page, nil
}

// StaffChanges lists every staff_audit_log row, newest first.
// ceiling: unpaged, as the function; page it above ~1k rows.
func (s *Store) StaffChanges(ctx context.Context) ([]StaffChange, error) {
	staff, err := db.StaffActor(ctx)
	if err != nil {
		return nil, err
	}
	out := []StaffChange{}
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT s.id::text, s.actor::text, s.event, s.rule_set_version_id::text, s.tenant_id::text,
			t.name, s.subject_id::text, s.payload, s.created_at
			FROM public.staff_read_staff_audit_log($1) s
			LEFT JOIN public.staff_read_tenants($1) t ON t.id = s.tenant_id
			ORDER BY s.created_at DESC, s.id DESC`, staff)
		if err != nil {
			return db.StaffReadError("audit: staff changes", err)
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[StaffChange])
		return db.StaffReadError("audit: staff changes", err)
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []StaffChange{}
	}
	return out, nil
}

// parseStaffFilter returns the filter, or the message of a 400. A malformed value fails before any read.
func parseStaffFilter(query url.Values) (StaffFilter, string) {
	f := StaffFilter{Limit: defaultLimit}
	if f.Tenant = query.Get("tenant"); f.Tenant != "" {
		if _, err := uuid.Parse(f.Tenant); err != nil {
			return StaffFilter{}, "invalid tenant"
		}
	}
	if f.Event = query.Get("event"); len(f.Event) > maxFilterTextLen {
		return StaffFilter{}, "event is too long"
	}
	if f.Actor = query.Get("actor"); len(f.Actor) > maxFilterTextLen {
		return StaffFilter{}, "actor is too long"
	}
	for _, tc := range []struct {
		name string
		dst  *time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		raw := query.Get(tc.name)
		if raw == "" {
			continue
		}
		when, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return StaffFilter{}, tc.name + " must be an RFC3339 timestamp"
		}
		*tc.dst = when
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.From.After(f.To) {
		return StaffFilter{}, "from must not be after to"
	}
	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return StaffFilter{}, "limit must be an integer"
		}
		if n < 1 {
			return StaffFilter{}, "limit must be >= 1"
		}
		f.Limit = min(n, maxLimit)
	}
	if raw := query.Get("cursor"); raw != "" {
		c, err := DecodeCursor(raw)
		if err != nil {
			return StaffFilter{}, "cursor is malformed"
		}
		f.Cursor = &c
	}
	return f, ""
}

// StaffSearchHandler repeats no staff check: auth.RequireStaff runs before it.
func StaffSearchHandler(search func(context.Context, StaffFilter) (StaffPage, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// URL.Query drops a malformed escape silently, which would widen the search.
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid query string")
			return
		}
		f, msg := parseStaffFilter(query)
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		page, err := search(r.Context(), f)
		if !writeStaffError(w, r, log, "audit: staff search", err) {
			return
		}
		if page.Events == nil {
			page.Events = []StaffRow{}
		}
		page.Page.Limit = f.Limit
		writeJSON(w, http.StatusOK, page)
	}
}

// StaffChangesHandler repeats no staff check: auth.RequireStaff runs before it.
func StaffChangesHandler(list func(context.Context) ([]StaffChange, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		changes, err := list(r.Context())
		if !writeStaffError(w, r, log, "audit: staff changes", err) {
			return
		}
		if changes == nil {
			changes = []StaffChange{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"changes": changes})
	}
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
