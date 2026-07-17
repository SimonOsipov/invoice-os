// This file (validator.go) is M4-04-04: 03's client for 04's batch validate
// surface -- the fleet's first context-service -> context-service BUSINESS
// call ([03->04 transport]).
//
// (Outbound HTTP itself is not new: internal/platform/auth/verify.go:181
// fetches JWKS and internal/gateway/fleet.go:60,118 probe health, both with
// explicit-timeout clients. What is new is one context service asking another
// to do domain work -- Stage-1 F6 corrects the "fleet's first s2s HTTP caller"
// framing, and verify.go:74-78 is the cited precedent for the nil->default
// client idiom below.)
//
// THE ONE PROPERTY THIS FILE EXISTS TO HOLD: a failure is NEVER laundered
// into a verdict. An unreachable, faulted, or misconfigured 04 must surface
// as an error -- never as "no violations", which the gate (M4-04-06) would
// read as "every invoice is clean" and transition draft->validated. Two
// structures enforce it: the CLOSED status switch (only 200 decodes) and the
// TOTALITY check (the response must cover every sent ref before ByRef is
// built). Both are load-bearing; see their own comments.
package invoice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// headerS2SToken carries the shared peer secret to 04's S2SMiddleware.
// Declared here rather than imported: 03 must not import internal/validation
// ([wire-types-redeclared]). The gateway strips this header from every proxied
// request, so it can only ever travel inside the private network
// ([s2s-gateway-strip]).
const headerS2SToken = "X-S2S-Token"

// defaultValidateTimeout bounds one batch round trip when the caller injects
// no client of its own.
//
// Sized deliberately, and NOT copied from the two shipped precedents an
// implementer would reach for first: auth/verify.go's 10s (a JWKS fetch) and
// gateway/fleet.go's 3s (a health probe). Both bound a TINY request. This one
// bounds a single round trip carrying up to 500 invoices against a possibly
// cold 04 that then runs ~500 x 19 rule evaluations with ~1,000 CEL compiles.
// A 10s copy here would fail M4-04-08's gate intermittently, and the gate
// would get the blame.
//
// The reasoning is the reverse of a performance target: this timeout bounds a
// HANG; it does not enforce the <60s import budget -- M4-04-08's wall-clock
// assertion enforces that and fails on its own. So a timeout LONGER than the
// gate is harmless (it still converts an unbounded hang into a bounded error),
// while a timeout SHORTER than a cold round trip can only ever manufacture
// false failures. [Stage-1 F4]
const defaultValidateTimeout = 60 * time.Second

// Violation is one failed rule as 04 reports it.
//
// 03 declares its OWN copy rather than importing internal/validation
// ([wire-types-redeclared], AC#8) -- mirroring the shipped precedent of
// writeJSON/writeError being copied per package rather than imported across
// one (internal/importer/handlers.go:220-222). Redeclaring the TYPE does not
// mean redeclaring the FORMAT: the json tags below are the contract, they are
// the only load-bearing part of this struct, and they match 04's shipped
// validation.Violation (rule.go:89-94) field for field.
//
// The tags are not cosmetic. Go matches JSON keys to fields case-insensitively
// but NOT across underscores, so an untagged copy decodes Severity, Message
// and Path correctly while silently zeroing RuleKey -- blocking still works, so
// the bug hides, while every stored violation loses its rule key. M4-04-05
// re-marshals this struct into invoices.violations, which the API serves raw,
// so these tags are also what keep the public API emitting rule_key rather
// than PascalCase RuleKey. [Stage-1 F1]
//
// Severity is a plain string, not a named type: 03 only reads it, and the gate
// that interprets it (M4-04-06) is the right owner of any constant.
type Violation struct {
	RuleKey  string `json:"rule_key"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Path     string `json:"path,omitempty"`
}

// ValidateItem is one invoice submitted for evaluation. Ref is caller-opaque
// and echoed back untouched -- 04 never interprets it (03 sends the invoice id
// on the real path, the invoice_number on dry-run where no id exists yet).
//
// Invoice must be a payload built from a Get-sourced invoice (or a
// CreateInput), never a List-sourced one: Store.List leaves LineItems nil
// (store.go:209-213), and MBSPayload cannot tell a nil LineItems from a
// genuinely line-less invoice -- it would omit line_items and make
// line-items-required fire on a perfectly valid invoice. This client takes the
// payload already mapped, so it cannot detect that upstream mistake.
type ValidateItem struct {
	Ref     string         `json:"ref"`
	Invoice map[string]any `json:"invoice"`
}

// ValidateResult is one batch's outcome: the rule-set the whole batch was
// evaluated against (stamped ONCE per batch -- one load means one version),
// plus every sent ref's violations.
//
// This is an in-process return type, not a wire type -- it carries no json
// tags because it never crosses the wire (validateBatchResponse below does).
// ByRef is TOTAL over the sent refs by construction: Validate refuses any
// response that does not cover them, so a missing key is unrepresentable
// rather than merely untested. That matters because an absent map key returns
// nil, which reads to a caller as "no violations" -- i.e. as a clean verdict on
// an invoice 04 never actually judged. [Stage-1 F5]
type ValidateResult struct {
	RuleSetVersion   int
	RuleSetVersionID string
	ByRef            map[string][]Violation
}

// validateBatchRequest / validateBatchItemResult / validateBatchResponse are
// 04's shipped wire shapes (internal/validation/handlers.go's batchRequest /
// batchItemResult / batchResponse), redeclared here with identical tags. The
// response's results is a LIST in request order; ByRef's map is built from it
// only after the totality check in Validate.
type validateBatchRequest struct {
	Invoices []ValidateItem `json:"invoices"`
}

type validateBatchItemResult struct {
	Ref        string      `json:"ref"`
	Violations []Violation `json:"violations"`
}

type validateBatchResponse struct {
	RuleSetVersion   int                       `json:"rule_set_version"`
	RuleSetVersionID string                    `json:"rule_set_version_id"`
	Results          []validateBatchItemResult `json:"results"`
}

// Validator calls 04's batch validate endpoint. Wired in M4-04-06 via
// invoice.NewValidator(mustEnv("VALIDATION_URL"), mustEnv("S2S_TOKEN"), nil).
type Validator struct {
	baseURL  string
	s2sToken string
	hc       *http.Client
}

// NewValidator returns a Validator posting to baseURL with s2sToken.
//
// A nil hc yields a client with an EXPLICIT timeout -- never
// http.DefaultClient, which has none: an unbounded hang on the import path is
// a 500-invoice outage. Same nil->default idiom as auth/verify.go:74-78, a
// different value (see defaultValidateTimeout). The injectable hc exists so
// tests can bound the call in milliseconds instead of sleeping out the
// production default. [AC#5, Stage-1 F4]
func NewValidator(baseURL, s2sToken string, hc *http.Client) *Validator {
	if hc == nil {
		hc = &http.Client{Timeout: defaultValidateTimeout}
	}
	return &Validator{baseURL: baseURL, s2sToken: s2sToken, hc: hc}
}

// Validate submits items to 04 in one round trip and returns each ref's
// violations, stamped with the single rule-set they were all evaluated
// against.
//
// Errors, never verdicts: a transport failure, a timeout, any non-200 status,
// an unparseable body, or a response that does not cover every sent ref all
// return an error. None of them ever returns an empty violation set.
//
//   - 503             -> ErrNoActiveRuleSet (distinguishable, so M4-04-06 can
//     answer 503 rather than 502: 04 has no published
//     rule-set, which is not 03's fault)
//   - everything else -> ErrUpstream (incl. 401: a misconfigured token is an
//     OUTAGE, not a judgment that the invoice is clean)
//
// Sends X-S2S-Token and nothing else identifying: no X-Tenant-ID, no X-User-*.
// 04's batch surface reads no tenant, so 03 asserts none ([s2s-identity]).
func (v *Validator) Validate(ctx context.Context, items []ValidateItem) (ValidateResult, error) {
	body, err := json.Marshal(validateBatchRequest{Invoices: items})
	if err != nil {
		return ValidateResult{}, fmt.Errorf("%w: marshal batch request: %v", ErrUpstream, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.baseURL+"/v1/validate/batch", bytes.NewReader(body))
	if err != nil {
		return ValidateResult{}, fmt.Errorf("%w: build batch request: %v", ErrUpstream, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerS2SToken, v.s2sToken)

	resp, err := v.hc.Do(req)
	if err != nil {
		// Transport: connection refused, closed connection, timeout, context
		// cancellation. Never a verdict on the invoices. [AC#3]
		return ValidateResult{}, fmt.Errorf("%w: post batch validate: %v", ErrUpstream, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	// THE STATUS SWITCH IS CLOSED, and that is the point. Only 200 reaches the
	// decode below; every other status returns an error WITHOUT the body ever
	// being unmarshalled into the result type.
	//
	// Structural rather than enumerated, on purpose. 04's error envelope
	// ({"error":"..."}) is valid JSON, and encoding/json ignores unknown keys,
	// so decoding a 400 or 413 body into validateBatchResponse SUCCEEDS --
	// yielding a zero-value success with an empty results list. The caller then
	// sees no violations for any ref and transitions every invoice
	// draft->validated. 04 ships exactly such a 400 (empty batch, or over its
	// 5,000-item cap) and 413 (body over 16 MiB), and the 400 is reachable in
	// production: the importer enforces no row ceiling at all, so a real CSV
	// import can exceed 5,000 invoices. A default->ErrUpstream also makes any
	// status 04 grows tomorrow (429, 409, ...) safe by default instead of
	// silently clean. [Stage-1 F2, AC#3]
	switch resp.StatusCode {
	case http.StatusOK:
		// fall through to decode
	case http.StatusServiceUnavailable:
		return ValidateResult{}, fmt.Errorf("%w: validation service has no active rule-set", ErrNoActiveRuleSet)
	default:
		return ValidateResult{}, fmt.Errorf("%w: validation service returned status %d", ErrUpstream, resp.StatusCode)
	}

	var wire validateBatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		// A 200 whose body does not parse is the last path that could launder
		// into a zero-value "clean" success. [AC#3]
		return ValidateResult{}, fmt.Errorf("%w: decode batch response: %v", ErrUpstream, err)
	}

	byRef := make(map[string][]Violation, len(wire.Results))
	for _, r := range wire.Results {
		// nil -> []Violation{}: a nil Go slice encodes as SQL NULL, and
		// invoices.violations is jsonb NOT NULL -- M4-04-05's write would raise
		// 23502 ([violations-write]).
		if r.Violations == nil {
			r.Violations = []Violation{}
		}
		byRef[r.Ref] = r.Violations
	}

	// TOTALITY: the response must cover every sent ref before ByRef is built.
	// Without this, a 200 that omits refs (truncation, a future partial-response
	// bug) leaves those keys absent -- and an absent key is indistinguishable
	// from a clean verdict to a caller ranging over the refs it sent. The same
	// failure shape as Store.List's nil LineItems: absence rendering as a
	// confident verdict.
	//
	// Two checks suffice for a bijection over distinct sent refs: equal counts,
	// then every sent ref present. A duplicated or unknown ref in the response
	// collapses or inflates the map and so trips the count check -- no separate
	// duplicate scan is needed. [Stage-1 F5]
	if len(byRef) != len(items) {
		return ValidateResult{}, fmt.Errorf("%w: batch response covers %d refs, want %d (results must be total over the sent refs)",
			ErrUpstream, len(byRef), len(items))
	}
	for _, it := range items {
		if _, ok := byRef[it.Ref]; !ok {
			return ValidateResult{}, fmt.Errorf("%w: batch response omits ref %q (results must be total over the sent refs)",
				ErrUpstream, it.Ref)
		}
	}

	return ValidateResult{
		RuleSetVersion:   wire.RuleSetVersion,
		RuleSetVersionID: wire.RuleSetVersionID,
		ByRef:            byRef,
	}, nil
}
