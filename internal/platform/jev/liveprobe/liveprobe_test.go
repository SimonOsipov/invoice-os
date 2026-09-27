// Package liveprobe holds the gated pre-merge probe: one real Jev call per
// question type through OpenRouter.
package liveprobe

import (
	"log/slog"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

// lpLine is one parsed "jev call" log line plus the question type asked.
type lpLine struct {
	Type      jev.QuestionType `json:"-"`
	Purpose   string           `json:"purpose"`
	Model     string           `json:"model"`
	Outcome   string           `json:"outcome"`
	Attempts  int              `json:"attempts"`
	Cost      float64          `json:"cost"`
	LatencyMS int64            `json:"latency_ms"`
}

// STUB: returns no requests.
func lpRequests() []jev.Request { return nil }

// STUB: always open, no reason.
func lpGate() (open bool, reason string) { return true, "" }

// STUB: asks nothing.
func lpRun(t *testing.T, build func(*slog.Logger) (*jev.Client, error)) []lpLine {
	t.Helper()
	return nil
}

// STUB: parses nothing.
func lpZDRVersions(body []byte) (map[string]bool, error) { return map[string]bool{}, nil }

// STUB: passes every line and reports nothing. report is the
// `probe <type>: …` line the key holder pastes into the PR.
func lpVerdict(line lpLine, zdr map[string]bool) (report string, err error) { return "", nil }

// STUB: the executor writes the gated probe.
func TestJevLiveProbe(t *testing.T) {}
