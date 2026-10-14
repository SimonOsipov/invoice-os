package systemhealth

import (
	"context"
	"log/slog"
	"time"
)

// Sampler records one sample per slot; run it on codelist.NewWorker.
type Sampler struct {
	Store  *Store
	Fleet  FleetReader
	Now    func() time.Time
	Logger *slog.Logger
}

// Sample records the current slot. A fleet error records unknown; a store error is returned.
func (s *Sampler) Sample(ctx context.Context) error {
	slot := s.Now().UTC().Truncate(SampleInterval)
	status, down := FleetUnknown, (*int)(nil)
	if f, err := s.Fleet(ctx); err != nil {
		s.Logger.Warn("systemhealth: fleet read failed", "err", err)
	} else {
		n := ServicesDown(f)
		status, down = f.Status, &n
	}
	_, err := s.Store.Record(ctx, slot, status, down)
	return err
}
