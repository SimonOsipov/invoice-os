// GET /v1/staff/code-list-syncs: the newest sync of each NRS code list, or one list's recent syncs.
package validation

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

var codeListName = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// CodeListLatest is the newest sync of one list.
type CodeListLatest struct {
	List         string    `json:"list"`
	SyncedAt     time.Time `json:"synced_at"`
	EntryCount   int       `json:"entry_count"`
	AddedCount   int       `json:"added_count"`
	RemovedCount int       `json:"removed_count"`
	ChangedCount int       `json:"changed_count"`
}

// CodeListSync is one sync of a list with the codes it moved.
type CodeListSync struct {
	SyncedAt   time.Time `json:"synced_at"`
	EntryCount int       `json:"entry_count"`
	Added      []string  `json:"added"`
	Removed    []string  `json:"removed"`
	Changed    []string  `json:"changed"`
}

// CodeListSyncs is the response body: lists for the summary, list and syncs for one list's detail.
// Pointers keep an empty slice on the wire as [] while the other shape's fields stay absent.
type CodeListSyncs struct {
	Lists *[]CodeListLatest `json:"lists,omitempty"`
	List  *string           `json:"list,omitempty"`
	Syncs *[]CodeListSync   `json:"syncs,omitempty"`
}

const codeListDetailLimit = 30

// CodeListSyncs reads the sync log: a summary when list is nil, else the newest 30 syncs of that list.
func (s *Store) CodeListSyncs(ctx context.Context, list *string) (CodeListSyncs, error) {
	var out CodeListSyncs
	err := db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		if list == nil {
			rows, err := tx.Query(ctx, `
SELECT DISTINCT ON (list) list, synced_at, entry_count, cardinality(added), cardinality(removed), cardinality(changed)
  FROM nrs_code_list_syncs
 ORDER BY list, synced_at DESC, id DESC`)
			if err != nil {
				return fmt.Errorf("validation: read code-list syncs: %w", err)
			}
			defer rows.Close()
			lists := []CodeListLatest{}
			for rows.Next() {
				var l CodeListLatest
				if err := rows.Scan(&l.List, &l.SyncedAt, &l.EntryCount, &l.AddedCount, &l.RemovedCount, &l.ChangedCount); err != nil {
					return fmt.Errorf("validation: scan code-list sync: %w", err)
				}
				lists = append(lists, l)
			}
			out.Lists = &lists
			return rows.Err()
		}
		rows, err := tx.Query(ctx, `
SELECT synced_at, entry_count, added, removed, changed
  FROM nrs_code_list_syncs
 WHERE list = $1
 ORDER BY synced_at DESC, id DESC
 LIMIT $2`, *list, codeListDetailLimit)
		if err != nil {
			return fmt.Errorf("validation: read code-list syncs of %s: %w", *list, err)
		}
		defer rows.Close()
		syncs := []CodeListSync{}
		for rows.Next() {
			var c CodeListSync
			if err := rows.Scan(&c.SyncedAt, &c.EntryCount, &c.Added, &c.Removed, &c.Changed); err != nil {
				return fmt.Errorf("validation: scan code-list sync: %w", err)
			}
			syncs = append(syncs, c)
		}
		out.List, out.Syncs = list, &syncs
		return rows.Err()
	})
	if err != nil {
		return CodeListSyncs{}, err
	}
	return out, nil
}

// StaffCodeListSyncsHandler serves GET /v1/staff/code-list-syncs.
func StaffCodeListSyncsHandler(read func(ctx context.Context, list *string) (CodeListSyncs, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var list *string
		if q := r.URL.Query(); q.Has("list") {
			name := q.Get("list")
			if !codeListName.MatchString(name) {
				writeError(w, http.StatusBadRequest, "invalid list")
				return
			}
			list = &name
		}
		out, err := read(r.Context(), list)
		if err != nil {
			status, msg := staffRulesError(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "validation: staff code-list syncs", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}
