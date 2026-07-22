// M5-02-03 (task-218): the RED-first spec for internal/submission/exchange_bridge.go --
// Operation, the five Outcome constants, and the pure ExchangeFor builder. Authored against
// exchange_bridge.go's deliberate Stage 2.5 stub (ExchangeFor always returns a zero
// Exchange{}), so every behavioural test below fails on a real assertion, not a compile
// error -- the package already compiles because the stub's signature is the real one.
//
// Package `submission_test` (external), matching exchange_test.go, exchange_db_test.go,
// failure_modes_test.go and worker_smoke_test.go. No testify, no new TestMain (one already
// exists at failure_modes_test.go:57).
//
// This file does NOT redeclare exAdapter/exAdapterVersion (exchange_db_test.go:75-76,
// visible here as the same test package) -- the stub adapter below uses different values
// ("ref"/"v9", the test spec table's own literals) on purpose, so a passing
// TestExchangeFor_StampsAdapterIdentity proves ExchangeFor read a.Name()/a.Version() rather
// than echoing some constant the two files happen to share.
package submission_test

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/submission"
	"github.com/SimonOsipov/invoice-os/migrations"
)

// exBridgeAdapter is a minimal Adapter stub for ExchangeFor's specs. Name/Version return
// fixed, non-empty strings deliberately DIFFERENT from exAdapter/exAdapterVersion
// (exchange_db_test.go), so identity assertions below prove the value came from the Adapter,
// not from a shared package-level constant. Transform/Submit/Poll panic: ExchangeFor consumes
// an already-built Evidence, not a live Adapter attempt, so it must never call them.
type exBridgeAdapter struct {
	name, version string
}

func (a exBridgeAdapter) Name() string    { return a.name }
func (a exBridgeAdapter) Version() string { return a.version }

func (a exBridgeAdapter) Transform(ctx context.Context, c submission.Canonical) (submission.Wire, error) {
	panic("exBridgeAdapter.Transform: ExchangeFor must never call an Adapter method")
}

func (a exBridgeAdapter) Submit(ctx context.Context, w submission.Wire, idemKey string) (submission.Result, submission.Evidence) {
	panic("exBridgeAdapter.Submit: ExchangeFor must never call an Adapter method")
}

func (a exBridgeAdapter) Poll(ctx context.Context, ref submission.Ref) (submission.Result, submission.Evidence) {
	panic("exBridgeAdapter.Poll: ExchangeFor must never call an Adapter method")
}

// --- AC-1: Outcome is derived from Evidence.ReachedWire, and only ever one of two values ---

func TestExchangeFor_ReachedWireSelectsSent(t *testing.T) {
	a := exBridgeAdapter{name: "ref", version: "v9"}
	ev := submission.Evidence{ReachedWire: true}

	got := submission.ExchangeFor(a, submission.OpSubmit, 1, "job-1", "inv-1", ev)

	if got.Outcome != submission.OutcomeSent {
		t.Errorf("ExchangeFor(...).Outcome = %q, want %q (Evidence.ReachedWire=true)",
			got.Outcome, submission.OutcomeSent)
	}
}

func TestExchangeFor_NotReachedWireSelectsConnectionFailed(t *testing.T) {
	a := exBridgeAdapter{name: "ref", version: "v9"}
	ev := submission.Evidence{ReachedWire: false}

	got := submission.ExchangeFor(a, submission.OpSubmit, 1, "job-1", "inv-1", ev)

	if got.Outcome != submission.OutcomeConnectionFailed {
		t.Errorf("ExchangeFor(...).Outcome = %q, want %q (Evidence.ReachedWire=false)",
			got.Outcome, submission.OutcomeConnectionFailed)
	}
}

// --- AC-2: Adapter identity and Attempt come from the caller's own parameters ---

func TestExchangeFor_StampsAdapterIdentity(t *testing.T) {
	a := exBridgeAdapter{name: "ref", version: "v9"}
	ev := submission.Evidence{ReachedWire: true}

	got := submission.ExchangeFor(a, submission.OpSubmit, 1, "job-1", "inv-1", ev)

	if got.Adapter != "ref" {
		t.Errorf("ExchangeFor(...).Adapter = %q, want %q (from a.Name(), not a shared constant)",
			got.Adapter, "ref")
	}
	if got.AdapterVersion != "v9" {
		t.Errorf("ExchangeFor(...).AdapterVersion = %q, want %q (from a.Version())",
			got.AdapterVersion, "v9")
	}
}

func TestExchangeFor_AttemptIsOneBased(t *testing.T) {
	a := exBridgeAdapter{name: "ref", version: "v9"}
	ev := submission.Evidence{ReachedWire: true}

	got := submission.ExchangeFor(a, submission.OpSubmit, 1, "job-1", "inv-1", ev)

	if got.Attempt != 1 {
		t.Errorf("ExchangeFor(...).Attempt = %d, want 1 -- passed through untouched, never "+
			"decremented to match submission_jobs.attempts (0-based, unlike app_exchange.attempt)",
			got.Attempt)
	}
}

// --- AC-3 / AC-7: Evidence passes through faithfully, unscrubbed ---

// The non-allowlisted header (Authorization) is on BOTH header maps alongside an allowlisted
// one (Content-Type). Content-only assertions on an allowlisted header can't prove
// ScrubHeaders was never invoked -- a "safe" header survives either way -- so the header that
// ScrubHeaders WOULD drop is the one that proves it. Body pointer identity (`==` against the
// input `*string`), not content equality, is the proof SafeBody was never invoked: SafeBody
// always allocates a fresh string, so equal content alone would pass whether or not the
// bridge wrongly ran it.
func TestExchangeFor_PassesEvidenceThroughUnchanged(t *testing.T) {
	a := exBridgeAdapter{name: "ref", version: "v9"}

	reqBody := "request body bytes"
	respBody := "response body bytes"
	status := 429
	latency := 12

	reqHeaders := http.Header{}
	reqHeaders.Set("Content-Type", "application/json")
	reqHeaders.Set("Authorization", "Bearer super-secret")

	respHeaders := http.Header{}
	respHeaders.Set("Content-Type", "application/xml")
	respHeaders.Set("Authorization", "Bearer super-secret")

	ev := submission.Evidence{
		RequestHeaders:  reqHeaders,
		ResponseHeaders: respHeaders,
		RequestBody:     &reqBody,
		ResponseBody:    &respBody,
		HTTPStatus:      &status,
		LatencyMS:       &latency,
		ReachedWire:     true,
	}

	got := submission.ExchangeFor(a, submission.OpSubmit, 1, "job-1", "inv-1", ev)

	if v := got.RequestHeaders.Get("Authorization"); v != "Bearer super-secret" {
		t.Errorf("ExchangeFor(...).RequestHeaders[Authorization] = %q, want %q -- ExchangeFor "+
			"must not call ScrubHeaders (Decision [scrub-is-the-recorders-job]; RecordExchange "+
			"applies it at write time)", v, "Bearer super-secret")
	}
	if v := got.ResponseHeaders.Get("Authorization"); v != "Bearer super-secret" {
		t.Errorf("ExchangeFor(...).ResponseHeaders[Authorization] = %q, want %q -- same proof, "+
			"response side", v, "Bearer super-secret")
	}
	// Control: the allowlisted header must ALSO still be there -- a bridge that dropped every
	// header regardless of allowlist would otherwise still fail the Authorization checks above
	// for the wrong reason.
	if v := got.RequestHeaders.Get("Content-Type"); v != "application/json" {
		t.Errorf("ExchangeFor(...).RequestHeaders[Content-Type] = %q, want %q", v, "application/json")
	}
	if v := got.ResponseHeaders.Get("Content-Type"); v != "application/xml" {
		t.Errorf("ExchangeFor(...).ResponseHeaders[Content-Type] = %q, want %q", v, "application/xml")
	}

	if got.RequestBody != &reqBody {
		t.Error("ExchangeFor(...).RequestBody is not the SAME pointer as the input Evidence's " +
			"RequestBody -- SafeBody always allocates a fresh string, so pointer identity is " +
			"the content-independent proof SafeBody was never called")
	}
	if got.ResponseBody != &respBody {
		t.Error("ExchangeFor(...).ResponseBody is not the SAME pointer as the input Evidence's " +
			"ResponseBody -- same proof, response side")
	}

	if got.HTTPStatus == nil || *got.HTTPStatus != status {
		t.Errorf("ExchangeFor(...).HTTPStatus = %v, want %d", got.HTTPStatus, status)
	}
	if got.LatencyMS == nil || *got.LatencyMS != latency {
		t.Errorf("ExchangeFor(...).LatencyMS = %v, want %d", got.LatencyMS, latency)
	}
}

// A nil Evidence body/status/latency pointer stays nil on the returned Exchange -- never
// coerced to a zero value or empty string.
func TestExchangeFor_NilBodiesStayNil(t *testing.T) {
	a := exBridgeAdapter{name: "ref", version: "v9"}
	ev := submission.Evidence{} // the zero Evidence: every pointer nil, ReachedWire false

	got := submission.ExchangeFor(a, submission.OpSubmit, 1, "job-1", "inv-1", ev)

	if got.RequestBody != nil {
		t.Errorf("ExchangeFor(...).RequestBody = %v, want nil -- a nil Evidence body must stay "+
			"nil, never coerced to a zero value or empty string", *got.RequestBody)
	}
	if got.ResponseBody != nil {
		t.Errorf("ExchangeFor(...).ResponseBody = %v, want nil", *got.ResponseBody)
	}
	if got.HTTPStatus != nil {
		t.Errorf("ExchangeFor(...).HTTPStatus = %v, want nil", *got.HTTPStatus)
	}
	if got.LatencyMS != nil {
		t.Errorf("ExchangeFor(...).LatencyMS = %v, want nil", *got.LatencyMS)
	}
}

// --- AC-4: the constants exactly match the live app_exchange CHECK vocabularies ---

// exExtractCheckValues finds the literal preamble (e.g. "CHECK (operation IN (") in sql and
// returns the single-quoted values inside the IN (...) list that immediately follows.
//
// Scoped to the literal preamble on purpose, not a loose "any quoted word in the file" match
// (outside the actual CHECK clauses, these words never appear single-quoted together in
// either migration's prose). The value list itself never contains a '(' character -- only
// quoted strings and commas -- so the FIRST ')' after the preamble is always the IN(...)
// list's own closing paren, never the enclosing CHECK(...)'s. A naive search for the next
// ");" substring would NOT work here: the operation CHECK sits inside a CREATE TABLE column
// list and closes with ")," (a comma, not a semicolon), so that search would run on past it
// into unrelated SQL.
func exExtractCheckValues(t *testing.T, sql, preamble string) []string {
	t.Helper()
	idx := strings.Index(sql, preamble)
	if idx < 0 {
		t.Fatalf("literal substring %q not found in migration text", preamble)
	}
	rest := sql[idx+len(preamble):]
	end := strings.IndexByte(rest, ')')
	if end < 0 {
		t.Fatalf("no closing ) found after %q", preamble)
	}
	clause := rest[:end]

	valRe := regexp.MustCompile(`'([^']*)'`)
	matches := valRe.FindAllStringSubmatch(clause, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// exGooseUpSection returns the substring of sql between its "-- +goose Up" and
// "-- +goose Down" markers. Used to exclude BOTH the Down section's narrowing CHECK (the
// 4-value reversal) and -- because it is a different file entirely -- the birth migration's
// own superseded outcome CHECK from the parse: only the Up section here is the LIVE schema.
func exGooseUpSection(t *testing.T, sql string) string {
	t.Helper()
	upIdx := strings.Index(sql, "-- +goose Up")
	if upIdx < 0 {
		t.Fatal(`no "-- +goose Up" marker found in migration text`)
	}
	downIdx := strings.Index(sql, "-- +goose Down")
	if downIdx < 0 || downIdx < upIdx {
		t.Fatal(`no "-- +goose Down" marker found after "-- +goose Up" in migration text`)
	}
	return sql[upIdx:downIdx]
}

// exAssertSameSet fails naming the difference in BOTH directions: Go constants the live SQL
// CHECK doesn't have, and SQL CHECK values no Go constant covers. Order-independent (sets).
func exAssertSameSet(t *testing.T, label string, gotConstants, wantFromSQL []string) {
	t.Helper()
	gotSet := make(map[string]bool, len(gotConstants))
	for _, g := range gotConstants {
		gotSet[g] = true
	}
	wantSet := make(map[string]bool, len(wantFromSQL))
	for _, w := range wantFromSQL {
		wantSet[w] = true
	}

	var constantNotInSQL, sqlNotInConstant []string
	for g := range gotSet {
		if !wantSet[g] {
			constantNotInSQL = append(constantNotInSQL, g)
		}
	}
	for w := range wantSet {
		if !gotSet[w] {
			sqlNotInConstant = append(sqlNotInConstant, w)
		}
	}
	sort.Strings(constantNotInSQL)
	sort.Strings(sqlNotInConstant)

	if len(constantNotInSQL) > 0 || len(sqlNotInConstant) > 0 {
		t.Errorf("%s: Go constants %v vs. live SQL CHECK values %v do not match as sets\n"+
			"  Go constant(s) with NO matching SQL CHECK value: %v\n"+
			"  SQL CHECK value(s) with NO matching Go constant:  %v",
			label, gotConstants, wantFromSQL, constantNotInSQL, sqlNotInConstant)
	}
}

// TestOutcomeConstants_MatchAppExchangeCheck is a REGRESSION GUARD, not a red-to-green spec
// for this subtask -- in the framing internal/invoice/validator_test.go:445-447 uses for
// TestValidatorClient_DoesNotImportValidationPackage. The Operation/Outcome constants it
// checks are DECLARATIONS, written correctly in exchange_bridge.go's Stage 2.5 stub commit, so
// this test passes from the moment it is written and stays green. Its job is to catch FUTURE
// drift -- a migration that adds/removes a CHECK value without a matching constant update, or
// vice versa -- not to drive today's implementation.
//
// The comparison is against the LIVE migration text, read through migrations.FS (the same
// embed.FS internal/platform/db/migrate_test.go already imports this way), not a hand-
// transcribed Go literal set -- a hardcoded expected-set would compare the constants to a
// copy of themselves and could never detect drift.
func TestOutcomeConstants_MatchAppExchangeCheck(t *testing.T) {
	// operation traces to the birth migration's ONE AND ONLY operation CHECK -- no later
	// migration ever touches this column.
	opSQL, err := migrations.FS.ReadFile("20260722093218_app_exchange.sql")
	if err != nil {
		t.Fatalf("read 20260722093218_app_exchange.sql from migrations.FS: %v", err)
	}
	wantOps := exExtractCheckValues(t, string(opSQL), "CHECK (operation IN (")

	// outcome traces to the WIDENING migration's +goose Up section -- the live 5-value CHECK.
	// "connection_failed" is added ONLY here; the birth migration's own outcome CHECK (4
	// values) is superseded, and this same file's +goose Down section narrows back to 4 --
	// both must be excluded, or the test asserts against a stale set instead of the live one.
	outSQL, err := migrations.FS.ReadFile("20260722114935_app_exchange_connection_failed.sql")
	if err != nil {
		t.Fatalf("read 20260722114935_app_exchange_connection_failed.sql from migrations.FS: %v", err)
	}
	upSection := exGooseUpSection(t, string(outSQL))
	wantOutcomes := exExtractCheckValues(t, upSection, "CHECK (outcome IN (")

	gotOps := []string{string(submission.OpSubmit), string(submission.OpPoll)}
	gotOutcomes := []string{
		submission.OutcomeSent,
		submission.OutcomeBlockedRateLimit,
		submission.OutcomeSkippedAlreadyCleared,
		submission.OutcomeTransformFailed,
		submission.OutcomeConnectionFailed,
	}

	exAssertSameSet(t, "operation", gotOps, wantOps)
	exAssertSameSet(t, "outcome", gotOutcomes, wantOutcomes)
}
