package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
)

// ReadyCheck reports whether a dependency is ready to serve. A nil error means
// ready; a non-nil error marks the dependency (and thus the service) not ready.
type ReadyCheck func(ctx context.Context) error

// readiness holds the registered readiness checks surfaced by /readyz.
type readiness struct {
	mu     sync.RWMutex
	checks map[string]ReadyCheck
}

func (rd *readiness) add(name string, check ReadyCheck) {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	if rd.checks == nil {
		rd.checks = make(map[string]ReadyCheck)
	}
	rd.checks[name] = check
}

func (rd *readiness) snapshot() map[string]ReadyCheck {
	rd.mu.RLock()
	defer rd.mu.RUnlock()
	out := make(map[string]ReadyCheck, len(rd.checks))
	for k, v := range rd.checks {
		out[k] = v
	}
	return out
}

// healthzHandler is a liveness probe: 200 as long as the process is running.
func healthzHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyzHandler runs every registered readiness check. All pass → 200; any
// failure → 503 with the failing dependency names.
func (rd *readiness) readyzHandler(w http.ResponseWriter, r *http.Request) {
	failures := make(map[string]string)
	for name, check := range rd.snapshot() {
		if err := check(r.Context()); err != nil {
			failures[name] = err.Error()
		}
	}
	if len(failures) > 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":   "not ready",
			"failures": failures,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
