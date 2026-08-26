// handlers.go: GET /v1/audit-log (AUDIT-04-07). Reached through the gateway as
// /api/invoice/v1/audit-log — the gateway routes on the first segment under /api/ and
// forwards the subpath, so the mux pattern carries no prefix.
//
// writeJSON/writeError/statusForErr mirror internal/dashboard/handlers.go:22-44 verbatim;
// per-package duplicates are the convention here, not a shared library.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// maxFilterTextLen bounds q, matching internal/invoice's cap of the same name.
const maxFilterTextLen = 200

// maxFilterValues bounds the repeated event and actor params. Checked over USABLE
// (non-empty) values and before any further work, so a caller cannot buy unbounded
// parsing with empty ones.
const maxFilterValues = 50

// defaultLimit and maxLimit are §2's page rules: absent means 25, above 100 clamps, below
// 1 is a 400 rather than a clamp — a caller asking for 0 rows has made a mistake, and
// silently serving 25 hides it.
const (
	defaultLimit = 25
	maxLimit     = 100
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// statusForErr maps a store error to its status and body. db.ErrNoTenant is 401
// (fail-closed), db.ErrNotActiveMember is 403; anything else is a 500 with a generic
// body, so no internal ever reaches the response. Logging the 500 case is the caller's job — only it knows the operation.
func statusForErr(err error) (status int, msg string) {
	switch {
	case errors.Is(err, db.ErrNoTenant):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, db.ErrNotActiveMember):
		return http.StatusForbidden, db.NotActiveMemberMessage
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}

// ListHandler returns GET /v1/audit-log. Identity is checked FIRST, before any parameter
// is read, so an unauthenticated caller cannot learn which parameters exist by watching
// 400s (TestAuditListHandler_UnauthenticatedIs401BeforeParsing).
//
// Two rules run through every parameter below, carried from
// internal/invoice/handlers.go:572-582. EMPTY IS ABSENT, not a 400: `?event=` applies no
// filter. A MALFORMED value is always a 400 raised BEFORE the store is called — silently
// dropping a narrowing filter renders a wrong page with a plausible total instead of an
// honest error.
func ListHandler(list func(ctx context.Context, f Filter) (Response, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		f, msg := parseFilter(r.URL.Query())
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}

		out, err := list(r.Context(), f)
		if err != nil {
			status, body := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "audit: list", slog.Any("err", err))
			}
			writeError(w, status, body)
			return
		}

		writeJSON(w, http.StatusOK, out)
	}
}

// parseFilter turns the querystring into a Filter, or returns the message for a 400. It
// returns a message rather than an error because every failure here is a 400 with a
// caller-facing string; nothing it produces is ever a 500.
func parseFilter(query url.Values) (Filter, string) {
	f := Filter{Limit: defaultLimit}

	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return Filter{}, "limit must be an integer"
		}
		// Above the cap clamps, below 1 is refused: a caller asking for zero rows has made
		// a mistake, and quietly serving 25 hides it.
		if n < 1 {
			return Filter{}, "limit must be >= 1"
		}
		if n > maxLimit {
			n = maxLimit
		}
		f.Limit = n
	}

	if raw := query.Get("cursor"); raw != "" {
		c, err := DecodeCursor(raw)
		if err != nil {
			return Filter{}, "cursor is malformed"
		}
		f.Cursor = &c
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
			return Filter{}, tc.name + " must be an RFC3339 timestamp"
		}
		*tc.dst = when
	}

	// Repeated params: read every value (query.Get returns only the first) and skip ""
	// per value, so `?event=` alone still applies no filter. The cap is checked over
	// USABLE values only, so empty ones can never buy unbounded work.
	var msg string
	if f.Events, msg = repeatedValues(query["event"], "event"); msg != "" {
		return Filter{}, msg
	}
	if f.Actors, msg = repeatedValues(query["actor"], "actor"); msg != "" {
		return Filter{}, msg
	}

	f.ActorKind = query.Get("actor_kind")
	switch f.ActorKind {
	case "", "people", "system":
	default:
		return Filter{}, `actor_kind must be "people" or "system"`
	}
	// The two answer the same question two ways, so a request setting both has no single
	// meaning; refusing is honest where picking one silently is not.
	if len(f.Actors) > 0 && f.ActorKind != "" {
		return Filter{}, "actor and actor_kind cannot be combined"
	}

	switch raw := query.Get("company"); raw {
	case "":
		f.Company = AllCompanies()
	case companyWorkspace:
		f.Company = WorkspaceOnly()
	default:
		if _, err := uuid.Parse(raw); err != nil {
			return Filter{}, `company must be a well-formed uuid or the literal "workspace"`
		}
		f.Company = NamedCompany(raw)
	}

	if f.Q = query.Get("q"); len(f.Q) > maxFilterTextLen {
		return Filter{}, "q is too long"
	}

	if f.InvoiceID = query.Get("invoice_id"); f.InvoiceID != "" {
		if _, err := uuid.Parse(f.InvoiceID); err != nil {
			return Filter{}, "invoice_id must be a well-formed uuid"
		}
	}

	return f, ""
}

// companyWorkspace is the one non-uuid value the company param accepts (A-5).
const companyWorkspace = "workspace"

// repeatedValues collects a repeated param's non-empty values and enforces the cap.
func repeatedValues(raw []string, name string) ([]string, string) {
	var out []string
	for _, v := range raw {
		if v != "" {
			out = append(out, v)
		}
	}
	if len(out) > maxFilterValues {
		return nil, fmt.Sprintf("%s accepts at most %d values", name, maxFilterValues)
	}
	return out, ""
}
