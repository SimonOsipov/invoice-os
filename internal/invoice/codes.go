package invoice

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// CodeEntry is one hit of the NRS code-list search.
type CodeEntry struct {
	Code    string  `json:"code"`
	Label   string  `json:"label"`
	Percent *string `json:"percent"`
}

// codeSearchLists are the registry lists the search serves.
var codeSearchLists = []string{"states", "lgas", "hs-codes", "services-codes", "invoice-quantity-codes", "tax-categories"}

const (
	codeSearchDefaultLimit = 20
	codeSearchMaxLimit     = 50
	codeSearchMaxQuery     = 100
)

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// SearchCodes reads the global code lists inside the tenant seam, so only an
// active member reads. q is matched literally.
func (s *Store) SearchCodes(ctx context.Context, list, q string, limit int) ([]CodeEntry, error) {
	out := []CodeEntry{}
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
SELECT code,
       coalesce(nullif(entries->0->>'name',''), nullif(entries->0->>'value',''), entries->0->>'description', '') AS label,
       CASE WHEN list = 'tax-categories' AND entries->0->>'percent' ~ '^[0-9]+(\.[0-9]+)?$' THEN entries->0->>'percent' END
FROM nrs_codes
WHERE list = $1 AND ($2::text = '' OR code ILIKE $2::text || '%'
      OR coalesce(nullif(entries->0->>'name',''), nullif(entries->0->>'value',''), entries->0->>'description', '') ILIKE '%' || $2::text || '%')
ORDER BY (code = $3) DESC, (code ILIKE $2::text || '%') DESC, code
LIMIT $4`, list, likeEscaper.Replace(q), q, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e CodeEntry
			if err := rows.Scan(&e.Code, &e.Label, &e.Percent); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CodeSearchHandler serves GET /v1/code-lists/{list}?q=&limit=.
func CodeSearchHandler(search func(ctx context.Context, list, q string, limit int) ([]CodeEntry, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		list := r.PathValue("list")
		if !slices.Contains(codeSearchLists, list) {
			writeError(w, http.StatusNotFound, "unknown code list")
			return
		}
		query := r.URL.Query()
		q := strings.TrimSpace(query.Get("q"))
		if utf8.RuneCountInString(q) > codeSearchMaxQuery {
			writeError(w, http.StatusBadRequest, "q must be at most 100 characters")
			return
		}
		if !utf8.ValidString(q) || strings.ContainsRune(q, 0) {
			writeError(w, http.StatusBadRequest, "q must be valid text")
			return
		}
		limit := codeSearchDefaultLimit
		if raw := query.Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "limit must be an integer")
				return
			}
			limit = n
		}
		if limit < 1 {
			writeError(w, http.StatusBadRequest, "limit must be >= 1")
			return
		}
		limit = min(limit, codeSearchMaxLimit)

		codes, err := search(r.Context(), list, q, limit)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.Error("invoice: code search failed", "op", "code_search", "list", list, "err", err)
			}
			writeError(w, status, msg)
			return
		}
		if codes == nil {
			codes = []CodeEntry{}
		}
		writeJSON(w, http.StatusOK, map[string][]CodeEntry{"codes": codes})
	}
}
