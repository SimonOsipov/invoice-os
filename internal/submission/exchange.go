package submission

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/textproto"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// MaxBodyBytes is the per-body evidence cap (256 KiB). It is enforced HERE and not in the
// schema on purpose: an octet_length CHECK would hard-reject an over-size evidence write at
// exactly the moment something has already gone wrong (see the app_exchange migration
// header). The Go seam clips instead, and says so with app_exchange.truncated.
const MaxBodyBytes = 256 * 1024

// allowedHeaders is the write-time header allowlist, canonicalised at init.
//
// It is the UNION of the story's request list (Content-Type, Content-Length, Accept,
// User-Agent, Idempotency-Key, X-Request-Id, X-Correlation-Id) and response list (those
// minus the request-only three, plus Date, Retry-After and the three X-RateLimit-*). One
// direction-less ScrubHeaders cannot express two lists, and AC #3 applies it to BOTH header
// maps; the union is the only consistent reading. Nothing is weakened by it — not one of the
// twelve is a credential, and Authorization / Cookie / Set-Cookie / X-Api-Key /
// Proxy-Authorization are on neither list, so they stay dropped.
//
// An ALLOWLIST, not a blocklist (PM Decision [credential scrubbing]): app_exchange feeds the
// customer-downloadable M5-07 archive export, so a header nobody has thought to blocklist —
// one a future adapter invents — must be lost by default rather than leaked by default.
//
// Built by running every literal through textproto.CanonicalMIMEHeaderKey rather than by
// hand, because canonicalisation upper-cases only the first letter and letters after a hyphen
// and LOWER-CASES the rest: X-RateLimit-Limit canonicalises to X-Ratelimit-Limit. A raw-
// literal map compared against canonicalised keys would never match those three and would
// silently drop them — fail-closed, no error, just lost evidence.
var allowedHeaders = func() map[string]struct{} {
	names := []string{
		"Content-Type", // on BOTH lists: the only per-body content-type record we keep
		"Content-Length",
		"Accept",
		"User-Agent",
		"Idempotency-Key",
		"X-Request-Id",
		"X-Correlation-Id",
		"Date",
		"Retry-After",
		"X-RateLimit-Limit",
		"X-RateLimit-Remaining",
		"X-RateLimit-Reset",
	}
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		m[textproto.CanonicalMIMEHeaderKey(n)] = struct{}{}
	}
	return m
}()

// ScrubHeaders returns a NEW http.Header holding only the allowlisted names from h, matched
// case-insensitively. Everything else — including names nobody has heard of yet — is dropped.
// h is never mutated, neither the map nor the value slices it holds.
//
// Keys are canonicalised AS THE INPUT IS ITERATED rather than by looking up each allowlisted
// name with h.Get: an http.Header built as a map literal stores its keys verbatim (only
// Set/Add canonicalise), so a per-name Get would miss a raw "authorization" — leaving the
// credential in place — and would also flatten multi-value headers to their first value.
//
// The result is always non-nil, even for empty or nil input. That is load-bearing at the
// write: json.Marshal of a nil http.Header yields the JSON scalar null, which binds happily
// to a NOT NULL jsonb column and lands 'null'::jsonb — the same trap documented at
// internal/invoice/store.go:834-839.
func ScrubHeaders(h http.Header) http.Header {
	out := http.Header{}
	for k, vs := range h {
		ck := textproto.CanonicalMIMEHeaderKey(k)
		if _, ok := allowedHeaders[ck]; !ok {
			continue
		}
		// append to out[ck] (nil on first sight of the name) always allocates and COPIES,
		// so the result never shares a backing array with the caller's slice — the bug that
		// out[ck] = vs would introduce. It also merges two raw keys that canonicalise alike.
		out[ck] = append(out[ck], vs...)
	}
	return out
}

// SafeBody makes s storable as Postgres text and returns it with two INDEPENDENT flags.
//
//   - coerced is true when the bytes could not be stored losslessly: a NUL was removed or
//     invalid UTF-8 was rewritten. Both are needed, and NUL removal is genuinely separate
//     from the UTF-8 repair — utf8.ValidString("\x00") is TRUE, so strings.ToValidUTF8 leaves
//     a NUL in place and the INSERT then dies with 22021, the exact failure this seam exists
//     to prevent (PM Decision [evidence is text, not parsed]).
//   - clipped is true only when the body actually exceeded MaxBodyBytes. Exactly at the cap
//     nothing is removed and the flag stays false.
//
// A body can be either, both, or neither. Coercion runs FIRST and the cap is applied to the
// coerced string: coercion changes byte length, and doing it in this order also guarantees
// the stored prefix is NUL-free wherever the NUL sat.
//
// The cap walks back to the last complete rune boundary, DROPPING a straddling rune rather
// than repairing it — capping first and repairing after would yield a body ending in U+FFFD
// and would report coercion on an input that was perfectly valid UTF-8.
func SafeBody(s string) (body string, clipped, coerced bool) {
	body = s

	if strings.IndexByte(body, 0) >= 0 {
		body = strings.ReplaceAll(body, "\x00", "")
		coerced = true
	}
	if !utf8.ValidString(body) {
		body = strings.ToValidUTF8(body, "�")
		coerced = true
	}

	if len(body) > MaxBodyBytes {
		cut := MaxBodyBytes
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut--
		}
		body = body[:cut]
		clipped = true
	}

	return body, clipped, coerced
}

// Exchange is one attempt's evidence, as the caller hands it over. It carries exactly the
// app_exchange columns this seam owns and no others.
//
// There is deliberately NO TenantID field: the tenant comes from the tx's app.current_tenant
// GUC, filled inside the INSERT, so no call site is offered a way to write across tenants.
// Also absent: id and occurred_at (column defaults), and truncated / encoding_coerced, which
// RecordExchange computes from SafeBody so a caller cannot mis-report them.
//
// Attempt is 1-BASED (app_exchange CHECK attempt >= 1), unlike submission_jobs.attempts,
// which is 0-based — copying a fresh job's attempts straight across hits 23514.
type Exchange struct {
	SubmissionJobID string // app_exchange.submission_job_id (uuid)
	InvoiceID       string // app_exchange.invoice_id (uuid); must match the job's
	Operation       string // "submit" | "poll"
	Outcome         string // "sent" | "blocked_rate_limit" | "skipped_already_cleared" | "transform_failed"
	Attempt         int    // 1-based
	Adapter         string // mirrors submission_jobs.adapter
	AdapterVersion  string // mirrors submission_jobs.adapter_version

	RequestHeaders  http.Header
	RequestBody     *string // nil when there was none (nothing reached the wire)
	ResponseHeaders http.Header
	ResponseBody    *string // nil when there was none (timeout, dropped connection)
	HTTPStatus      *int    // nil when no response was received
	LatencyMS       *int    // nil when not measured
}

// RecordExchange appends exactly one app_exchange row on tx — the caller's tenant-scoped
// transaction (db.WithinTenantTx) — so the evidence shares that transaction's fate, the same
// in-tx shape as audit.Record. It applies ScrubHeaders to both header maps and SafeBody to
// both bodies itself, so a call site cannot forget either.
//
// Unlike audit.Record it NAMES tenant_id in the INSERT, filling it from the tx's
// app.current_tenant GUC with the same expression every RLS policy in this repo uses.
// audit_log.tenant_id carries a column DEFAULT that reads the GUC; app_exchange.tenant_id has
// none, so an INSERT that omitted the column would leave it NULL and be refused 42501 by the
// RLS WITH CHECK EVEN INSIDE A VALID TENANT CONTEXT — a seam that could never write a row.
// Filling it in SQL keeps the tenant off Exchange while making the write work, and outside a
// tenant context the expression still resolves to NULL and the policy still refuses (AC #4;
// PostgreSQL evaluates the WITH CHECK before NOT NULL, so it surfaces as 42501, not 23502).
func RecordExchange(ctx context.Context, tx pgx.Tx, e Exchange) error {
	reqHeaders, err := json.Marshal(ScrubHeaders(e.RequestHeaders))
	if err != nil {
		return fmt.Errorf("submission: marshal request headers: %w", err)
	}
	respHeaders, err := json.Marshal(ScrubHeaders(e.ResponseHeaders))
	if err != nil {
		return fmt.Errorf("submission: marshal response headers: %w", err)
	}

	// Row-level flags are the OR across both bodies: the story rejected per-body flags
	// (Decision ["truncated" keeps the PM's meaning; coercion gets its own flag]).
	reqBody, reqClipped, reqCoerced := safeBodyPtr(e.RequestBody)
	respBody, respClipped, respCoerced := safeBodyPtr(e.ResponseBody)

	if _, err := tx.Exec(ctx,
		`INSERT INTO app_exchange (tenant_id, submission_job_id, invoice_id, operation, outcome,
		                           attempt, request_body, request_headers, response_body,
		                           response_headers, http_status, latency_ms, truncated,
		                           encoding_coerced, adapter, adapter_version)
		 VALUES (nullif(current_setting('app.current_tenant', true), '')::uuid,
		         $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		e.SubmissionJobID, e.InvoiceID, e.Operation, e.Outcome, e.Attempt,
		reqBody, string(reqHeaders), respBody, string(respHeaders),
		e.HTTPStatus, e.LatencyMS,
		reqClipped || respClipped, reqCoerced || respCoerced,
		e.Adapter, e.AdapterVersion,
	); err != nil {
		return fmt.Errorf("submission: record exchange for job %s: %w", e.SubmissionJobID, err)
	}
	return nil
}

// safeBodyPtr runs SafeBody over an optional body, preserving nil (SQL NULL) as nil — a body
// that was never captured is not the same evidence as an empty one.
func safeBodyPtr(s *string) (*string, bool, bool) {
	if s == nil {
		return nil, false, false
	}
	body, clipped, coerced := SafeBody(*s)
	return &body, clipped, coerced
}
