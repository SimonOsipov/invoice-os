package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

// Fleet-health probe bounds. A dead or slow backend must never hang the endpoint: each
// probe carries its own short timeout, and no more than maxProbeConcurrency run at once —
// so the whole roll-up is bounded regardless of how many upstreams are unreachable.
const (
	probeTimeout        = 3 * time.Second
	maxProbeConcurrency = 8
	// A /healthz body is a two-key object; the cap only stops a malfunctioning
	// upstream from streaming into the gateway.
	maxHealthzBody = 4 << 10
)

// Per-service and roll-up status literals (kept as constants so the payload contract — a
// future public status page reads it — is defined in one place).
const (
	statusUp      = "up"
	statusDown    = "down"
	fleetOK       = "ok"
	fleetDegraded = "degraded"
)

// ServiceHealth is one backend's line in the fleet-health payload: a stable name, an
// up/down status, and a reason only when down. Deliberately shaped to back a future
// public client-facing status page (that UI is out of scope here).
type ServiceHealth struct {
	Name   string `json:"name"`
	Status string `json:"status"`          // statusUp | statusDown
	Error  string `json:"error,omitempty"` // set only when Status == statusDown
	// Build is the commit the service reported on /healthz. Empty when the
	// probe failed or the service is older than platform.BuildSHA. Deliberately
	// NOT part of the up/down verdict: a fleet running the wrong commit is
	// healthy, just not the one under test, and conflating the two would make
	// /healthz/fleet lie in the other direction. The deploy gate compares it.
	Build string `json:"build,omitempty"`
}

// FleetHealth is the GET /healthz/fleet body: an overall roll-up plus per-service detail.
type FleetHealth struct {
	Status   string          `json:"status"` // fleetOK (all up) | fleetDegraded (any down)
	Services []ServiceHealth `json:"services"`
}

// FleetHealthHandler returns GET /healthz/fleet: a public, unauthenticated roll-up of
// every backend's /healthz. The seven context services are private-network-only, so only
// the gateway can reach them — this route is how CI (and a future status page) observes
// fleet health through the one public backend surface. The gateway reports itself up (it
// is answering this request); each upstream is probed at <base>/healthz. Overall 200 when
// all are up, 503 when any is down, with the culprit(s) named in the body. Fan-out is
// bounded (per-probe timeout + concurrency cap) so one dead backend cannot hang it.
//
// Registered on the platform mux OUTSIDE /api/ and outside the JWT verifier: it is an
// operational endpoint, not tenant data.
func FleetHealthHandler(upstreams map[string]*url.URL, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	client := &http.Client{Timeout: probeTimeout}

	// Stable order — the context services sorted by name — so the payload is deterministic
	// for tests and reads consistently on a status page. The gateway is prepended per-request.
	names := make([]string, 0, len(upstreams))
	for name := range upstreams {
		names = append(names, name)
	}
	sort.Strings(names)

	return func(w http.ResponseWriter, r *http.Request) {
		probed := make([]ServiceHealth, len(names))
		sem := make(chan struct{}, maxProbeConcurrency)
		var wg sync.WaitGroup
		for i, name := range names {
			wg.Add(1)
			go func(i int, name string, base *url.URL) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				probed[i] = probeService(r.Context(), client, name, base)
			}(i, name, upstreams[name])
		}
		wg.Wait()

		// The gateway is up by definition here; the context probes follow in name order.
		services := make([]ServiceHealth, 0, len(names)+1)
		services = append(services, ServiceHealth{Name: "gateway", Status: statusUp, Build: platform.BuildSHA})
		services = append(services, probed...)

		allUp := true
		for _, s := range services {
			if s.Status != statusUp {
				allUp = false
				break
			}
		}

		body := FleetHealth{Status: fleetDegraded, Services: services}
		code := http.StatusServiceUnavailable
		if allUp {
			body.Status = fleetOK
			code = http.StatusOK
		} else {
			log.WarnContext(r.Context(), "gateway fleet-health degraded", slog.Any("services", services))
		}
		writeJSON(w, code, body)
	}
}

// probeService issues GET <base>/healthz with a per-probe timeout and maps the outcome to
// up/down. Any transport error or non-2xx status is down, with the reason recorded so the
// body names why the service failed.
func probeService(ctx context.Context, client *http.Client, name string, base *url.URL) ServiceHealth {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	target := base.JoinPath("healthz").String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return ServiceHealth{Name: name, Status: statusDown, Error: err.Error()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return ServiceHealth{Name: name, Status: statusDown, Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ServiceHealth{Name: name, Status: statusDown, Error: fmt.Sprintf("healthz returned %d", resp.StatusCode)}
	}
	// A body that will not decode leaves Build empty rather than failing the
	// probe: 2xx already settled up/down, and the deploy gate reports a missing
	// build as a mismatch on its own terms.
	var payload struct {
		Build string `json:"build"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, maxHealthzBody)).Decode(&payload)
	return ServiceHealth{Name: name, Status: statusUp, Build: payload.Build}
}
