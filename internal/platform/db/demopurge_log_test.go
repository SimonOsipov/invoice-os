// Suite for the line the purge reports itself with. The purge is non-fatal, so
// a green /healthz proves the gateway booted, not that the purge worked — this
// line and dev-env.yml's gate (purge_gate_test.go) are the only two things that
// can say otherwise.
//
// No database: logPurgeResult is a pure shaping function over PurgeResult.
package db_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	purgeCompleteMsg = "demo purge complete"
	purgeFailedMsg   = "db: provision: demo-tenant purge failed — continuing to seed"
)

// logRecord keeps every field as raw JSON so `{}` stays distinguishable from
// `null` — the nil-map trap slog.Any falls into.
type logRecord map[string]json.RawMessage

// decodeLogRecords parses the NDJSON a slog.JSONHandler wrote.
func decodeLogRecords(t *testing.T, buf *bytes.Buffer) []logRecord {
	t.Helper()
	var out []logRecord
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec logRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func recString(t *testing.T, rec logRecord, key string) string {
	t.Helper()
	raw, ok := rec[key]
	if !ok {
		t.Fatalf("log record carries no %q field: %v", key, rec)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("%q = %s, want a string: %v", key, raw, err)
	}
	return s
}

func recInt(t *testing.T, rec logRecord, key string) int64 {
	t.Helper()
	raw, ok := rec[key]
	if !ok {
		t.Fatalf("log record carries no %q field: %v", key, rec)
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatalf("%q = %s, want a number: %v", key, raw, err)
	}
	return n
}

// captureLogger returns a logger writing NDJSON into buf at every level.
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, nil))
}

// TestPurgeLogsPerTableCounts (AC-1): one INFO line names every non-zero table
// and its count. audit_log is reported in its OWN field because its count has a
// different denominator: every other table's number is excess over the curated
// seed baseline, audit_log's is all demo audit activity accumulated since the
// last purge. Summing them would be meaningless, so they never share a map.
func TestPurgeLogsPerTableCounts(t *testing.T) {
	const auditRows = 40
	res := db.PurgeResult{
		ByTable:  map[string]int64{"invoices": 7, "line_items": 12, "audit_log": auditRows},
		Rows:     59,
		Duration: 250 * time.Millisecond,
	}
	wantByTable := map[string]int64{"invoices": 7, "line_items": 12}
	if len(res.ByTable) == 0 || len(wantByTable) == 0 {
		t.Fatal("the fixture carries no per-table counts — every assertion below would be vacuous")
	}

	var buf bytes.Buffer
	db.LogPurgeResultForTest(captureLogger(&buf), res, nil)

	recs := decodeLogRecords(t, &buf)
	if len(recs) != 1 {
		t.Fatalf("logPurgeResult wrote %d record(s), want exactly 1 — one purge, one line", len(recs))
	}
	rec := recs[0]

	if got := recString(t, rec, "level"); got != "INFO" {
		t.Errorf("level = %q, want INFO", got)
	}
	if got := recString(t, rec, "msg"); got != purgeCompleteMsg {
		t.Errorf("msg = %q, want %q", got, purgeCompleteMsg)
	}
	if got, want := recInt(t, rec, "tenants"), int64(len(db.DemoTenants)); got != want {
		t.Errorf("tenants = %d, want %d (len(db.DemoTenants))", got, want)
	}
	if got := recInt(t, rec, "rows"); got != res.Rows {
		t.Errorf("rows = %d, want %d — the transaction's honest total, audit_log included", got, res.Rows)
	}
	if got := recInt(t, rec, "audit_log_rows"); got != auditRows {
		t.Errorf("audit_log_rows = %d, want %d", got, auditRows)
	}
	if _, ok := rec["duration_ms"]; !ok {
		t.Error("the success line carries no duration_ms")
	} else if got := recInt(t, rec, "duration_ms"); got != res.Duration.Milliseconds() {
		t.Errorf("duration_ms = %d, want %d", got, res.Duration.Milliseconds())
	}

	raw, ok := rec["by_table"]
	if !ok {
		t.Fatalf("log record carries no by_table field: %v", rec)
	}
	var byTable map[string]int64
	if err := json.Unmarshal(raw, &byTable); err != nil {
		t.Fatalf("by_table = %s, want a JSON object: %v", raw, err)
	}
	if _, leaked := byTable["audit_log"]; leaked {
		t.Errorf("by_table carries audit_log (%s) — the two counts have different denominators and must not share a map", raw)
	}
	if len(byTable) != len(wantByTable) {
		t.Errorf("by_table holds %d table(s) (%s), want %d", len(byTable), raw, len(wantByTable))
	}
	for table, want := range wantByTable {
		if got := byTable[table]; got != want {
			t.Errorf("by_table[%q] = %d, want %d", table, got, want)
		}
	}
}

// TestPurgeLogsEmptyByTableOnACleanBoot (AC-2): a converged boot logs
// by_table={} — the proof the previous boot converged. A nil map through
// slog.Any serializes as `null`, which reads as "not measured".
func TestPurgeLogsEmptyByTableOnACleanBoot(t *testing.T) {
	for _, c := range []struct {
		name    string
		byTable map[string]int64
	}{
		{"a converged boot", map[string]int64{}},
		{"the zero PurgeResult, whose map is nil", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			db.LogPurgeResultForTest(captureLogger(&buf), db.PurgeResult{ByTable: c.byTable}, nil)

			recs := decodeLogRecords(t, &buf)
			if len(recs) != 1 {
				t.Fatalf("logPurgeResult wrote %d record(s), want exactly 1", len(recs))
			}
			rec := recs[0]

			if got := recString(t, rec, "level"); got != "INFO" {
				t.Errorf("level = %q, want INFO — a converged purge is not a failure", got)
			}
			if got := recInt(t, rec, "rows"); got != 0 {
				t.Errorf("rows = %d, want 0", got)
			}
			if got := recInt(t, rec, "audit_log_rows"); got != 0 {
				t.Errorf("audit_log_rows = %d, want 0 — 0 there is a real observation, so it is always printed", got)
			}
			raw, ok := rec["by_table"]
			if !ok {
				t.Fatalf("log record carries no by_table field: %v", rec)
			}
			if string(raw) != "{}" {
				t.Errorf("by_table = %s, want {} — `null` reads as a measurement never taken", raw)
			}
		})
	}
}

// TestPurgeLogsErrorWithoutCounts (AC-3): the failure line carries the error and
// omits duration_ms. PurgeDemoTenants returns a zero PurgeResult on every error
// path, so a 0 duration there would be a measurement never taken.
func TestPurgeLogsErrorWithoutCounts(t *testing.T) {
	wantErr := errors.New("db: purge: invitations: lock timeout")

	var buf bytes.Buffer
	db.LogPurgeResultForTest(captureLogger(&buf), db.PurgeResult{}, wantErr)

	recs := decodeLogRecords(t, &buf)
	if len(recs) != 1 {
		t.Fatalf("logPurgeResult wrote %d record(s) on the error path, want exactly 1", len(recs))
	}
	rec := recs[0]

	if got := recString(t, rec, "level"); got != "ERROR" {
		t.Errorf("level = %q, want ERROR", got)
	}
	if got := recString(t, rec, "msg"); got != purgeFailedMsg {
		t.Errorf("msg = %q, want %q", got, purgeFailedMsg)
	}
	if got := recString(t, rec, "error"); got != wantErr.Error() {
		t.Errorf("error = %q, want %q", got, wantErr.Error())
	}
	if got := recInt(t, rec, "rows"); got != 0 {
		t.Errorf("rows = %d, want 0", got)
	}
	if got := recInt(t, rec, "audit_log_rows"); got != 0 {
		t.Errorf("audit_log_rows = %d, want 0", got)
	}
	if raw, ok := rec["by_table"]; !ok {
		t.Error("the error line carries no by_table field; want {}")
	} else if string(raw) != "{}" {
		t.Errorf("by_table = %s, want {}", raw)
	}
	if raw, ok := rec["duration_ms"]; ok {
		t.Errorf("the error line carries duration_ms = %s; the zero PurgeResult never measured one", raw)
	}
	for _, r := range recs {
		if lvl := recString(t, r, "level"); lvl == "INFO" {
			t.Errorf("a failed purge also emitted an INFO record (%v) — one purge, one line", r)
		}
	}
}

// TestPurgeLogSurvivesANilLogger (AC-3): Provision's nil-logger fallback moves
// into the helper rather than vanishing. A no-panic assertion alone would pass
// on a helper that dropped the line entirely, so the default logger is captured
// and must receive it.
func TestPurgeLogSurvivesANilLogger(t *testing.T) {
	var buf bytes.Buffer
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })
	slog.SetDefault(captureLogger(&buf))

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("logPurgeResult(nil, ...) panicked: %v", r)
			}
		}()
		db.LogPurgeResultForTest(nil, db.PurgeResult{}, errors.New("boom"))
	}()

	recs := decodeLogRecords(t, &buf)
	if len(recs) != 1 {
		t.Fatalf("a nil logger produced %d record(s) on slog.Default(), want exactly 1 — the fallback vanished rather than moved", len(recs))
	}
	if got := recString(t, recs[0], "msg"); got != purgeFailedMsg {
		t.Errorf("msg = %q, want %q", got, purgeFailedMsg)
	}
}
