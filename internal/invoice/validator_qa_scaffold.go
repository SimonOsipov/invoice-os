// ===========================================================================
// QA MODE-A SCAFFOLD -- internal/invoice/validator_qa_scaffold.go
// ===========================================================================
// task-110 / M4-04-04 ("03: the validation client") is Test-first: yes.
// This subtask's real internal/invoice/validator.go does NOT exist yet --
// QA (RALPH Stage 2.5, Mode A) authors VC-01..14 (validator_test.go) RED,
// BEFORE implementation. A bare reference to an undeclared Validator/
// ValidateItem/ValidateResult/NewValidator/ErrUpstream/ErrNoActiveRuleSet
// would be a COMPILE error, not a valid red (per the story's own
// precedent-setting rule: a compile error tells you nothing about which
// assertion is wrong). This file exists ONLY to give validator_test.go
// something to compile and call, so every VC-* test fails on a REAL
// assertion instead.
//
// THE EXECUTOR MUST DELETE THIS ENTIRE FILE and write the real, correct
// internal/invoice/validator.go per task-110's plan + Stage-1 addendum.
// Do NOT "fix" this file in place -- replace it.
//
// Every behaviour below is DELIBERATELY WRONG, on purpose, mirroring the
// anti-patterns Stage-1's findings warn a naive implementation would ship:
//
//   F1 [CRITICAL] -- Violation / ValidateItem / batchResponseWire carry NO
//       json tags. Go matches JSON keys to untagged fields case-
//       insensitively but NOT across underscores: single-word wire fields
//       (ref, severity, message, path, violations) survive; multi-word
//       snake_case fields (rule_key, rule_set_version,
//       rule_set_version_id) silently zero out on decode, and on the
//       REQUEST side ValidateItem{Ref,Invoice} marshals as "Ref"/"Invoice"
//       (capitalized), not "ref"/"invoice". The real validator.go MUST
//       carry the exact tags 04 ships.
//   F2 [CRITICAL] -- Validate has NO status-code switch at all. Every HTTP
//       response, whatever its status (400/401/413/500/503/...), is
//       decoded as if it were 200. An error envelope like {"error":"..."}
//       has no fields matching the wire struct, so it silently decodes to
//       a ZERO-VALUE SUCCESS (nil error, empty ByRef) instead of ErrUpstream
//       -- laundering a failure into "every invoice is clean". The real
//       implementation needs a CLOSED switch: only 200 decodes, every other
//       status (named or not) returns ErrUpstream / ErrNoActiveRuleSet.
//   F3 -- ErrNoActiveRuleSet is declared (tests need the symbol) but never
//       actually returned by Validate -- nothing here distinguishes a 503
//       from any other status, because there IS no status handling.
//   F5 -- ByRef is built by blindly ranging over whatever the (mis-decoded)
//       results list contains. No check that the response is TOTAL over
//       the sent refs. A ref 04 omits from its response is simply absent
//       from the map -- indistinguishable from a clean verdict to any
//       caller that ranges over the refs it sent.
//   [s2s-identity] -- Validate also sends X-Tenant-ID (forbidden: 04's
//       batch surface has no tenant, so 03 must assert none).
//   [violations-write] -- a `"violations": null` ref is left as a nil Go
//       slice, never normalized to []Violation{} (a nil slice would encode
//       as SQL NULL into invoices.violations jsonb NOT NULL downstream).
//   [wire-types-redeclared] (AC#8) -- imports internal/validation on
//       purpose (see the import + unusedValidationRef below) so VC-14's
//       dependency guard has a real violation to catch. The real
//       validator.go must NOT import internal/validation at all.
//   Transport/decode errors are returned RAW (unwrapped) instead of being
//       mapped to ErrUpstream.
//   NewValidator falls back to http.DefaultClient on a nil hc (AC#5
//       forbids this -- DefaultClient has no timeout). No VC test in this
//       file drives the nil-hc path directly (VC-08 always injects an
//       explicit ms-timeout client), so this particular anti-pattern is
//       flagged here for the executor/QA Mode B rather than pinned by a
//       spec in this file.
//
// None of this is how the real validator.go should look; see task-110's
// plan and its Stage-1 addendum (F1-F6) for the correct, closed-switch
// design.
package invoice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	// ANTI-PATTERN (violates [wire-types-redeclared] / AC#8): the real
	// validator.go must NOT import internal/validation -- 03 redeclares its
	// own wire types instead, so it never needs 04's package to compile or
	// evaluate severity. This scaffold imports it anyway, on purpose, so
	// VC-14's `go list -deps` guard has a genuine hit to catch instead of
	// trivially passing before any implementation exists.
	"github.com/SimonOsipov/invoice-os/internal/validation"
)

// unusedValidationRef exists solely to keep the anti-pattern import above
// "used" (Go refuses to compile an unused import). Never called by tests or
// production code. Deleted along with this whole file.
var unusedValidationRef = validation.TypeRequired

// Violation, ValidateItem, ValidateResult are 03's own wire types
// ([wire-types-redeclared]) -- SCAFFOLD VERSION, deliberately untagged
// (F1). The real validator.go must carry the exact tags 04 ships:
// rule_key, severity, message, path(,omitempty), ref, invoice,
// rule_set_version, rule_set_version_id.
type Violation struct {
	RuleKey  string
	Severity string
	Message  string
	Path     string
}

type ValidateItem struct {
	Ref     string
	Invoice map[string]any
}

type ValidateResult struct {
	RuleSetVersion   int
	RuleSetVersionID string
	ByRef            map[string][]Violation
}

// batchResponseWire is 04's actual POST /v1/validate/batch response shape
// (a "results" LIST, not a map) -- SCAFFOLD VERSION, deliberately untagged
// (F1), reproducing exactly the partial-decode Stage-1 measured live:
// single-word fields survive, multi-word snake_case fields silently zero
// out.
type batchResponseWire struct {
	RuleSetVersion   int
	RuleSetVersionID string
	Results          []struct {
		Ref        string
		Violations []Violation
	}
}

// ErrUpstream / ErrNoActiveRuleSet -- SCAFFOLD VERSION. Per Stage-1 F3, the
// real implementation declares both in invoice.go's existing sentinel
// block (invoice.go:158-163, alongside ErrValidation/ErrNotFound/...),
// since this subtask (order 4) is their first consumer -- M4-04-05 (order
// 5) is narrowed to own only ErrNotDraft/ErrStaleValidation. Declared here
// only so VC-03..07/11/12/13 compile; Validate() below never actually
// returns either of them (see the F2 anti-pattern: there is no status
// switch at all), so every spec that expects one of these sentinels fails
// on a real assertion, not a missing symbol.
var (
	ErrUpstream        = errors.New("invoice: upstream validation error")
	ErrNoActiveRuleSet = errors.New("invoice: no active rule-set")
)

// Validator -- SCAFFOLD VERSION of the fleet's first service-to-service
// business HTTP client (task-110).
type Validator struct {
	baseURL  string
	s2sToken string
	hc       *http.Client
}

// NewValidator -- SCAFFOLD VERSION. ANTI-PATTERN: falls back to
// http.DefaultClient when hc is nil, which AC#5 explicitly forbids
// (DefaultClient carries no timeout at all -- an unbounded hang on the
// import path). The real implementation must default to
// &http.Client{Timeout: 60 * time.Second} (Stage-1 F4), never
// DefaultClient.
func NewValidator(baseURL, s2sToken string, hc *http.Client) *Validator {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Validator{baseURL: baseURL, s2sToken: s2sToken, hc: hc}
}

// Validate -- SCAFFOLD VERSION. See the file header for the full list of
// deliberate anti-patterns this implements.
func (v *Validator) Validate(ctx context.Context, items []ValidateItem) (ValidateResult, error) {
	reqBody := struct {
		Invoices []ValidateItem `json:"invoices"`
	}{Invoices: items}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return ValidateResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.baseURL+"/v1/validate/batch", bytes.NewReader(b))
	if err != nil {
		return ValidateResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-S2S-Token", v.s2sToken)
	// ANTI-PATTERN ([s2s-identity], AC#2): the real client must send NO
	// identity headers at all. This scaffold deliberately also sends
	// X-Tenant-ID so VC-02 fails on a genuine assertion rather than passing
	// by accident.
	req.Header.Set("X-Tenant-ID", "unused-scaffold-value")

	resp, err := v.hc.Do(req)
	if err != nil {
		// ANTI-PATTERN (F2 / AC#3): a transport failure (closed connection,
		// timeout, ...) must map to ErrUpstream. This scaffold returns the
		// raw error instead.
		return ValidateResult{}, err
	}
	defer resp.Body.Close()

	// ANTI-PATTERN (F2): NO status-code switch at all. Every response,
	// whatever its HTTP status, is decoded as though it were 200 --
	// 400/401/413/500/503 error envelopes silently decode into a
	// zero-value success (nil error, empty ByRef) instead of ErrUpstream /
	// ErrNoActiveRuleSet.
	var wire batchResponseWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		// ANTI-PATTERN: raw decode error, never wrapped as ErrUpstream.
		return ValidateResult{}, err
	}

	// ANTI-PATTERN (F5): builds ByRef by blindly ranging over whatever the
	// (possibly wrong, possibly incomplete) results list contains -- no
	// check that the response is total over the sent refs (every sent ref
	// present exactly once). A ref 04 omits is simply absent from the map.
	byRef := make(map[string][]Violation, len(wire.Results))
	for _, r := range wire.Results {
		// ANTI-PATTERN ([violations-write]): no nil -> []Violation{}
		// normalization -- a `"violations": null` ref stays a nil slice.
		byRef[r.Ref] = r.Violations
	}

	return ValidateResult{
		RuleSetVersion:   wire.RuleSetVersion,
		RuleSetVersionID: wire.RuleSetVersionID,
		ByRef:            byRef,
	}, nil
}
