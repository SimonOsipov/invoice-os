package systemhealth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

var healthKeys = []string{"checked_at", "fleet", "jobs", "queue_depth", "trend"}

type readCall struct {
	since time.Time
}

// handlerRig serves Handler over fakes and records the order of the two reads.
type handlerRig struct {
	calls []string
	reads []readCall
	logs  *bytes.Buffer
	h     http.HandlerFunc
}

func newHandlerRig(counts map[string]int64, samples []Sample, readErr error, fleet gateway.FleetHealth, fleetErr error, now time.Time) *handlerRig {
	rig := &handlerRig{logs: &bytes.Buffer{}}
	log := slog.New(slog.NewJSONHandler(rig.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rig.h = Handler(func(_ context.Context, since time.Time) (map[string]int64, []Sample, error) {
		rig.calls = append(rig.calls, "read")
		rig.reads = append(rig.reads, readCall{since})
		return counts, samples, readErr
	}, func(context.Context) (gateway.FleetHealth, error) {
		rig.calls = append(rig.calls, "fleet")
		return fleet, fleetErr
	}, func() time.Time { return now }, log)
	return rig
}

func (rig *handlerRig) serve() *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	rig.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/staff/health", nil))
	return rec
}

// logLevels returns the level of every record whose message contains substr ("" matches all).
func (rig *handlerRig) logLevels(t *testing.T, substr string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(rig.logs.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if msg, _ := rec["msg"].(string); strings.Contains(msg, substr) {
			out = append(out, rec["level"].(string))
		}
	}
	return out
}

func decodeRaw(t *testing.T, rec *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return body
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d %q, want %d", rec.Code, rec.Body.String(), status)
	}
}

func TestStaffHealthHandler_Contract(t *testing.T) {
	now := time.Date(2026, 10, 14, 10, 5, 0, 0, time.UTC)
	fleet := gateway.FleetHealth{Status: "ok", Services: []gateway.ServiceHealth{{Name: "gateway", Status: "up"}}}
	rig := newHandlerRig(map[string]int64{"queued": 1, "pending": 2, "dead_lettered": 3}, nil, nil, fleet, nil, now)

	rec := rig.serve()
	wantStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body := decodeRaw(t, rec)
	if got := sortedKeys(body); !slices.Equal(got, healthKeys) {
		t.Fatalf("keys = %v, want %v", got, healthKeys)
	}
	var jobs map[string]int64
	if err := json.Unmarshal(body["jobs"], &jobs); err != nil {
		t.Fatal(err)
	}
	wantStates := []string{"accepted", "cancelled", "dead_lettered", "failed", "pending", "queued", "rejected", "submitting"}
	if got := sortedKeys(jobs); !slices.Equal(got, wantStates) {
		t.Errorf("jobs keys = %v, want the eight states spelled out: %v", got, wantStates)
	}
	if jobs["cancelled"] != 0 || jobs["queued"] != 1 || jobs["pending"] != 2 || jobs["dead_lettered"] != 3 {
		t.Errorf("jobs = %v", jobs)
	}
	if got := string(body["queue_depth"]); got != "3" {
		t.Errorf("queue_depth = %s, want 3", got)
	}
	if got := string(body["trend"]); got != "[]" {
		t.Errorf("trend = %s, want [] for no samples", got)
	}
	var checked time.Time
	if err := json.Unmarshal(body["checked_at"], &checked); err != nil || !checked.Equal(now) {
		t.Errorf("checked_at = %s (%v), want %v", body["checked_at"], err, now)
	}
	if !slices.Equal(rig.calls, []string{"read", "fleet"}) {
		t.Errorf("calls = %v, want the database read first, then the fleet", rig.calls)
	}
	if len(rig.reads) != 1 || !rig.reads[0].since.Equal(now.Add(-TrendWindow)) {
		t.Errorf("read since = %v, want checked_at - 7 days = %v", rig.reads, now.Add(-TrendWindow))
	}
	if levels := rig.logLevels(t, ""); slices.Contains(levels, "WARN") || slices.Contains(levels, "ERROR") {
		t.Errorf("a healthy read logged %v, want no warning or error", levels)
	}
}

func TestStaffHealthHandler_Contract_ExtraStatesPassThroughAndCheckedAtIsUTC(t *testing.T) {
	zone := time.FixedZone("UTC+2", 2*3600)
	now := time.Date(2026, 10, 14, 12, 5, 0, 0, zone)
	rig := newHandlerRig(map[string]int64{"queued": 1, "weird": 4}, nil, nil, gateway.FleetHealth{Status: "ok", Services: []gateway.ServiceHealth{}}, nil, now)
	rec := rig.serve()
	wantStatus(t, rec, http.StatusOK)
	body := decodeRaw(t, rec)
	var jobs map[string]int64
	if err := json.Unmarshal(body["jobs"], &jobs); err != nil {
		t.Fatal(err)
	}
	if jobs["weird"] != 4 || len(jobs) != len(JobStates)+1 {
		t.Errorf("jobs = %v, want the eight states plus weird=4", jobs)
	}
	if got := string(body["checked_at"]); got != `"2026-10-14T10:05:00Z"` {
		t.Errorf("checked_at = %s, want the UTC instant", got)
	}
}

func TestStaffHealthHandler_TrendFromSamples(t *testing.T) {
	now := time.Date(2026, 10, 14, 10, 5, 0, 0, time.UTC)
	zero, three := 0, 3
	at := func(i int) time.Time { return now.Add(-time.Duration(3-i) * SampleInterval) }
	// Distinct weights per state and a third sample, so a swapped field or a re-sort cannot pass.
	samples := []Sample{
		{TakenAt: at(0), FleetStatus: "unknown", ServicesDown: nil, JobCounts: map[string]int64{"queued": 1}},
		{TakenAt: at(1), FleetStatus: "ok", ServicesDown: &zero, JobCounts: map[string]int64{"pending": 2, "dead_lettered": 1}},
		{TakenAt: at(2), FleetStatus: "degraded", ServicesDown: &three, JobCounts: map[string]int64{"queued": 5, "submitting": 1, "pending": 1, "dead_lettered": 4, "accepted": 90}},
	}
	rig := newHandlerRig(map[string]int64{}, samples, nil, gateway.FleetHealth{Status: "ok", Services: []gateway.ServiceHealth{}}, nil, now)
	rec := rig.serve()
	wantStatus(t, rec, http.StatusOK)

	var trend []map[string]json.RawMessage
	if err := json.Unmarshal(decodeRaw(t, rec)["trend"], &trend); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		down        string
		depth, dead string
	}{{"null", "1", "0"}, {"0", "2", "1"}, {"3", "7", "4"}}
	if len(trend) != len(want) {
		t.Fatalf("trend has %d points, want %d", len(trend), len(want))
	}
	for i, w := range want {
		pt := trend[i]
		if got := sortedKeys(pt); !slices.Equal(got, []string{"dead_lettered", "queue_depth", "services_down", "taken_at"}) {
			t.Errorf("point %d keys = %v", i, got)
		}
		var takenAt time.Time
		if err := json.Unmarshal(pt["taken_at"], &takenAt); err != nil || !takenAt.Equal(at(i)) {
			t.Errorf("point %d taken_at = %s (%v), want %v", i, pt["taken_at"], err, at(i))
		}
		if string(pt["services_down"]) != w.down || string(pt["queue_depth"]) != w.depth || string(pt["dead_lettered"]) != w.dead {
			t.Errorf("point %d = down %s depth %s dead %s, want %s %s %s", i, pt["services_down"], pt["queue_depth"], pt["dead_lettered"], w.down, w.depth, w.dead)
		}
	}
}

func TestStaffHealthHandler_FleetErrorIsUnknown(t *testing.T) {
	now := time.Date(2026, 10, 14, 10, 5, 0, 0, time.UTC)
	rig := newHandlerRig(map[string]int64{"queued": 2, "dead_lettered": 1}, nil, nil, gateway.FleetHealth{}, errors.New("gateway down"), now)
	rec := rig.serve()
	wantStatus(t, rec, http.StatusOK)
	body := decodeRaw(t, rec)
	if got := string(body["fleet"]); got != `{"status":"unknown","services":[]}` {
		t.Errorf("fleet = %s, want the unknown roll-up with an empty services array", got)
	}
	var jobs map[string]int64
	if err := json.Unmarshal(body["jobs"], &jobs); err != nil || jobs["queued"] != 2 || jobs["dead_lettered"] != 1 {
		t.Errorf("jobs = %v (%v): the database parts must survive a fleet failure", jobs, err)
	}
	if string(body["queue_depth"]) != "2" || string(body["trend"]) != "[]" {
		t.Errorf("queue_depth = %s, trend = %s", body["queue_depth"], body["trend"])
	}
	levels := rig.logLevels(t, "")
	if !slices.Contains(levels, "WARN") || slices.Contains(levels, "ERROR") {
		t.Errorf("logged levels %v, want a warning and no error", levels)
	}
}

func TestStaffHealthHandler_ErrorsMapTo403And500(t *testing.T) {
	now := time.Date(2026, 10, 14, 10, 5, 0, 0, time.UTC)
	fleet := gateway.FleetHealth{Status: "ok", Services: []gateway.ServiceHealth{}}

	rig := newHandlerRig(nil, nil, db.ErrNotStaff, fleet, nil, now)
	rec := rig.serve()
	if rec.Code != http.StatusForbidden || strings.TrimSpace(rec.Body.String()) != `{"error":"forbidden"}` {
		t.Errorf("ErrNotStaff = %d %q, want 403 {\"error\":\"forbidden\"}", rec.Code, rec.Body.String())
	}
	if levels := rig.logLevels(t, ""); slices.Contains(levels, "ERROR") {
		t.Errorf("a refusal logged %v, want no error", levels)
	}

	rig = newHandlerRig(nil, nil, fmt.Errorf("staff health: %w", errors.New("connection reset by boom")), fleet, nil, now)
	rec = rig.serve()
	if rec.Code != http.StatusInternalServerError || strings.TrimSpace(rec.Body.String()) != `{"error":"internal server error"}` {
		t.Errorf("other error = %d %q, want 500 {\"error\":\"internal server error\"}", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("the body leaks the error: %q", rec.Body.String())
	}
	if levels := rig.logLevels(t, "systemhealth: staff health"); !reflect.DeepEqual(levels, []string{"ERROR"}) {
		t.Errorf("records named %q = %v, want one ERROR; log: %s", "systemhealth: staff health", levels, rig.logs.String())
	}
}
