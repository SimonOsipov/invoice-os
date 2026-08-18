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

// DBReset is "true" or "false" once a process has run boot-time database
// provisioning, and empty on every process that does not — which is every
// service except the gateway. /healthz omits the field entirely while it is
// empty, so the other services' bodies are byte-identical to before it existed.
//
// The gateway sets it from db.ProvisionConfig.ResetWillRun — the same predicate
// db.Provision branched on, never a second copy. It is published because the
// PR-environment reset is armed by a hand-set Railway variable
// (GATEWAY_DB_RESET) that fails CLOSED AND SILENT: lose it when a service is
// recreated and the E2E suites go quietly back to running against inherited
// residue, with a green fleet and nothing to say otherwise. dev-env.yml's
// health-gate asserts this field on every PR run.
var DBReset string

// DemoPurge is "true", "false" or "error" once a process has run boot-time
// provisioning, and empty on every process that does not. It carries
// db.PurgeOutcome's value verbatim; the purge is deliberately non-fatal, so
// "error" is the only signal a swallowed failure ever gives.
var DemoPurge string

// healthzHandler is a liveness probe: 200 as long as the process is running.
func healthzHandler(w http.ResponseWriter, _ *http.Request) {
	body := map[string]string{"status": "ok", "build": BuildSHA}
	if DBReset != "" {
		body["db_reset"] = DBReset
	}
	writeJSON(w, http.StatusOK, body)
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
