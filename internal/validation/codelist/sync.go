package codelist

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrHeld marks a pull that the shrink guard refused; nrs_codes is unchanged.
var ErrHeld = errors.New("pull held")

// Change is what one sync did to a list. Entries is the published entry count.
// Held: nothing was applied and Removed is what the pull would have removed.
type Change struct {
	Added, Removed, Changed []string
	Entries                 int
	Held                    bool
}

// Syncer fetches the NRS lists and applies them to nrs_codes.
type Syncer struct {
	pool    *pgxpool.Pool
	baseURL string
	hc      *http.Client
	log     *slog.Logger
	lists   []List
}

// NewSyncer returns a Syncer for the registry Lists. A nil hc uses fetch's default client.
func NewSyncer(pool *pgxpool.Pool, baseURL string, hc *http.Client, log *slog.Logger) *Syncer {
	return &Syncer{pool: pool, baseURL: baseURL, hc: hc, log: log, lists: Lists}
}

// SyncList fetches one list, then applies it. A fetch error opens no transaction.
// When NRS answers empty and the list has no rows, the saved copy seeds it; the
// error is still ErrEmptyList, because NRS failed.
func (s *Syncer) SyncList(ctx context.Context, l List) (Change, error) {
	grouped, n, err := fetch(ctx, s.hc, s.baseURL, l)
	if errors.Is(err, ErrEmptyList) {
		return s.seedFromSaved(ctx, l, err)
	}
	if err != nil {
		return Change{}, err
	}
	return apply(ctx, s.pool, l, grouped, n, false)
}

// seedFromSaved applies the saved copy of l only to a list with no rows. It returns fetchErr
// unless the seed itself failed.
func (s *Syncer) seedFromSaved(ctx context.Context, l List, fetchErr error) (Change, error) {
	grouped, n, ok, err := savedCopy(l)
	if !ok {
		return Change{}, fetchErr
	}
	if err != nil {
		return Change{}, fmt.Errorf("codelist: saved copy of %s: %w", l.Name, err)
	}
	ch, err := apply(ctx, s.pool, l, grouped, n, true)
	if errors.Is(err, errNotEmpty) {
		return Change{}, fetchErr
	}
	if err != nil {
		return Change{}, err
	}
	s.log.Warn("codelist seeded from saved copy", "list", l.Name, "entries", ch.Entries)
	return ch, fetchErr
}

// SyncAll syncs every list; one failing list does not stop the others.
func (s *Syncer) SyncAll(ctx context.Context) error {
	var errs []error
	for _, l := range s.lists {
		ch, err := s.SyncList(ctx, l)
		if err != nil {
			s.log.Error("codelist sync failed", "list", l.Name, "err", err)
			errs = append(errs, err)
			continue
		}
		s.log.Info("codelist synced", "list", l.Name, "entries", ch.Entries,
			"added", len(ch.Added), "removed", len(ch.Removed), "changed", len(ch.Changed))
	}
	return errors.Join(errs...)
}
