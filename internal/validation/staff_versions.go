// GET /v1/staff/rule-versions: every rule-set version with its state. The platform admits only a rules-role caller.
package validation

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// StaffVersion is one rule-set version on the GET /v1/staff/rule-versions wire.
type StaffVersion struct {
	RuleSetVersionID uuid.UUID  `json:"rule_set_version_id"`
	Version          int        `json:"version"`
	State            string     `json:"state"`
	EffectiveFrom    *string    `json:"effective_from"`
	PublishedAt      *time.Time `json:"published_at"`
	OpenedAt         *time.Time `json:"opened_at"`
	RuleCount        int        `json:"rule_count"`
	Notes            string     `json:"notes"`
}

// VersionList is the GET /v1/staff/rule-versions body.
type VersionList struct {
	Today    string         `json:"today"`
	Versions []StaffVersion `json:"versions"`
}

// Versions lists every version, highest first. A draft's published_at column is its creation time,
// so it goes out as opened_at and the draft's published_at is null.
func (s *Store) Versions(ctx context.Context) (VersionList, error) {
	var out VersionList
	err := db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
WITH t AS (SELECT (now() AT TIME ZONE 'UTC')::date AS today, rule_set_version_for((now() AT TIME ZONE 'UTC')::date) AS in_force)
SELECT v.id, v.version,
       CASE WHEN NOT v.sealed THEN 'draft'
            WHEN v.id = t.in_force THEN 'in_force'
            WHEN v.effective_from IS NULL THEN 'retired'
            WHEN v.effective_from > t.today THEN 'scheduled'
            ELSE 'superseded' END,
       to_char(v.effective_from, 'YYYY-MM-DD'),
       CASE WHEN v.sealed THEN v.published_at END,
       CASE WHEN NOT v.sealed THEN v.published_at END,
       (SELECT count(*) FROM rules r WHERE r.rule_set_version_id = v.id)::int,
       coalesce(v.notes, ''),
       to_char(t.today, 'YYYY-MM-DD')
  FROM rule_set_versions v, t
 ORDER BY v.version DESC`)
		if err != nil {
			return fmt.Errorf("validation: read versions: %w", err)
		}
		defer rows.Close()
		out.Versions = []StaffVersion{}
		for rows.Next() {
			var v StaffVersion
			if err := rows.Scan(&v.RuleSetVersionID, &v.Version, &v.State, &v.EffectiveFrom, &v.PublishedAt, &v.OpenedAt, &v.RuleCount, &v.Notes, &out.Today); err != nil {
				return fmt.Errorf("validation: scan version: %w", err)
			}
			out.Versions = append(out.Versions, v)
		}
		return rows.Err()
	})
	if err != nil {
		return VersionList{}, err
	}
	return out, nil
}

// StaffVersionsHandler serves GET /v1/staff/rule-versions.
func StaffVersionsHandler(list func(ctx context.Context) (VersionList, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		versions, err := list(r.Context())
		if err != nil {
			status, msg := staffRulesError(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "validation: staff list versions", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, versions)
	}
}
