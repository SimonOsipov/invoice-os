package systemhealth

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// ConnectionBudget is the Cloud SQL total in .claude/rules/db-migrations.md "Connection budget".
const ConnectionBudget = 80

const (
	MetricsTTL          = 60 * time.Second
	MetricsFetchTimeout = 15 * time.Second
)

// PanelData is one served chart; Series is [] when Available is false.
type PanelData struct {
	Key       string   `json:"key"`
	Unit      string   `json:"unit"`
	Available bool     `json:"available"`
	Series    []Series `json:"series"`
}

type LimitsSection struct {
	Available bool            `json:"available"`
	Services  []ServiceLimits `json:"services"`
}

type DeploysSection struct {
	Available bool     `json:"available"`
	Revisions []Deploy `json:"revisions"`
}

// Metrics is the staff metrics body.
type Metrics struct {
	Range            string         `json:"range"`
	Service          string         `json:"service"`
	Start            time.Time      `json:"start"`
	End              time.Time      `json:"end"`
	StepSeconds      int            `json:"step_seconds"`
	ConnectionBudget int            `json:"connection_budget"`
	Panels           []PanelData    `json:"panels"`
	Limits           LimitsSection  `json:"limits"`
	Deploys          DeploysSection `json:"deploys"`
}

var errFetchAborted = errors.New("systemhealth: metrics fetch aborted")

type metricsKey struct{ rangeName, service string }

type metricsEntry struct {
	body      Metrics
	fetchedAt time.Time
}

// metricsCall is one shared fetch.
type metricsCall struct {
	done chan struct{}
	body Metrics
	err  error // errFetchAborted when the leader did not finish
}

// MetricsSource serves Metrics from a 60 s cache with one shared fetch per key.
type MetricsSource struct {
	g   *Google
	now func() time.Time
	log *slog.Logger

	mu       sync.Mutex
	entries  map[metricsKey]metricsEntry // ceiling: len(Ranges) x (len(MetricServices)+1) keys, never evicted
	inflight map[metricsKey]*metricsCall
}

// NewMetricsSource makes no Google call.
func NewMetricsSource(g *Google, now func() time.Time, log *slog.Logger) *MetricsSource {
	return &MetricsSource{g: g, now: now, log: log, entries: map[metricsKey]metricsEntry{}, inflight: map[metricsKey]*metricsCall{}}
}

// Get answers from a cache entry younger than MetricsTTL, else joins or starts the key's one fetch.
// Its only error is ctx's, when the caller leaves before the fetch ends.
func (s *MetricsSource) Get(ctx context.Context, rangeName, service string) (Metrics, error) {
	key := metricsKey{rangeName, service}
	s.mu.Lock()
	start := s.now()
	if e, ok := s.entries[key]; ok && start.Sub(e.fetchedAt) < MetricsTTL {
		s.mu.Unlock()
		return e.body, nil
	}
	call, shared := s.inflight[key]
	if !shared {
		call = &metricsCall{done: make(chan struct{})}
		s.inflight[key] = call
	}
	s.mu.Unlock()

	if shared {
		select {
		case <-call.done:
			return call.body, call.err
		case <-ctx.Done():
			return Metrics{}, ctx.Err()
		}
	}
	s.run(ctx, key, call, start)
	// The leader finishes the fetch for the others, then reports its own departure.
	if err := ctx.Err(); err != nil {
		return Metrics{}, err
	}
	return call.body, nil
}

// run performs the shared fetch. The cleanup is deferred so a panic still releases the waiters.
func (s *MetricsSource) run(ctx context.Context, key metricsKey, call *metricsCall, start time.Time) {
	completed := false
	defer func() {
		s.mu.Lock()
		delete(s.inflight, key)
		if completed {
			s.entries[key] = metricsEntry{body: call.body, fetchedAt: start}
		} else {
			call.err = errFetchAborted
		}
		s.mu.Unlock()
		close(call.done)
	}()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), MetricsFetchTimeout)
	defer cancel()
	call.body = s.fetch(ctx, key, start)
	completed = true
}

// fetch asks Google for every section; a failed call marks only its own section unavailable.
func (s *MetricsSource) fetch(ctx context.Context, key metricsKey, now time.Time) Metrics {
	r := Ranges[key.rangeName]
	from, to := Window(r, now)
	m := Metrics{
		Range:            key.rangeName,
		Service:          key.service,
		Start:            from.UTC(),
		End:              to.UTC(),
		StepSeconds:      int(r.Step.Seconds()),
		ConnectionBudget: ConnectionBudget,
		Panels:           make([]PanelData, len(Panels)),
		Limits:           LimitsSection{Services: []ServiceLimits{}},
		Deploys:          DeploysSection{Revisions: []Deploy{}},
	}
	for i, p := range Panels {
		m.Panels[i] = PanelData{Key: p.Key, Unit: p.Unit, Series: []Series{}}
	}

	project, region, err := s.g.Meta.Location(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "system health: metadata", slog.String("error", err.Error()))
		return m
	}
	token, err := s.g.Meta.Token(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "system health: metadata", slog.String("error", err.Error()))
		return m
	}

	var wg sync.WaitGroup
	for i, p := range Panels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			series, err := s.g.queryRange(ctx, token, project, p.Query(r, key.service), from, to, r.Step)
			if err != nil {
				s.log.WarnContext(ctx, "system health: panel", slog.String("panel", p.Key), slog.String("error", err.Error()))
				return
			}
			m.Panels[i].Available, m.Panels[i].Series = true, series
		}()
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		l, err := s.g.limits(ctx, token, project, region, key.service)
		if err != nil {
			s.log.WarnContext(ctx, "system health: panel", slog.String("panel", "limits"), slog.String("error", err.Error()))
			return
		}
		m.Limits = LimitsSection{Available: true, Services: nonNil(l)}
	}()
	go func() {
		defer wg.Done()
		d, err := s.g.deploys(ctx, token, project, region, key.service, from, now)
		if err != nil {
			s.log.WarnContext(ctx, "system health: panel", slog.String("panel", "deploys"), slog.String("error", err.Error()))
			return
		}
		m.Deploys = DeploysSection{Available: true, Revisions: nonNil(d)}
	}()
	wg.Wait()
	return m
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
