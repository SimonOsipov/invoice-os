// M5-01-05 (task-215): the PURE half of the evidence write seam's spec — ScrubHeaders and
// SafeBody, no database, no network. Authored BEFORE internal/submission/exchange.go exists,
// so the RED state of this file is a COMPILE ERROR ("undefined: submission.ScrubHeaders",
// etc.), which is the correct and only possible red for a Go seam that has not been written.
// The DB-backed half (RecordExchange round-trips) is exchange_db_test.go.
//
// This file runs in the DEFAULT CI `go` job — no DATABASE_URL, no Postgres — so it contains
// ZERO t.Skip of any kind. That is not stylistic: .github/workflows/ci.yml:374 runs the
// `queue` job through scripts/ci/rls-test-gate.sh over ./internal/submission/..., and that
// gate treats a SKIP as a FAILURE. Any skip added here fails CI.
//
// Package `submission_test` (external), matching worker_smoke_test.go and
// failure_modes_test.go. TestMain already exists at failure_modes_test.go:57 — one per test
// binary — so this file defines none.
//
// Three traps this file is deliberately shaped to catch, all three found in the M5-01-05
// Explore pass and all three invisible to a test written from the plan's prose alone:
//
//  1. CANONICALISATION. textproto.CanonicalMIMEHeaderKey upper-cases only the first letter
//     and letters after a hyphen and LOWER-CASES the rest, so `X-RateLimit-Limit`
//     canonicalises to `X-Ratelimit-Limit`. An allowlist written as literal
//     map[string]bool{"X-RateLimit-Limit": true} compared against canonicalised keys NEVER
//     matches, and all three rate-limit headers are silently dropped — fail-closed, no
//     error, just lost evidence. TestScrubHeaders_KeepsAllowlistedHeaders feeds all three
//     and asserts they survive, so that bug is a hard failure rather than a quiet gap.
//     For the same reason every assertion here compares header names case-INSENSITIVELY
//     (exLookupFold): the test must measure survival, not spelling.
//
//  2. `\x00` IS VALID UTF-8. utf8.ValidString("\x00") is true, so strings.ToValidUTF8 does
//     NOT remove a NUL. Postgres `text` cannot hold one (22021). The NUL specs below are
//     therefore genuinely distinct from the invalid-UTF-8 specs: an implementation that
//     only calls ToValidUTF8 passes TestSafeBody_InvalidUTF8CoercedAndFlagged and FAILS
//     TestSafeBody_NulByteRemovedAndFlagged.
//
//  3. SHARED BACKING ARRAYS. Copying a header value with `out[k] = vs` rather than
//     `append([]string(nil), vs...)` produces a result whose slices alias the caller's.
//     TestScrubHeaders_DoesNotMutateInput writes through the RESULT and asserts the INPUT
//     is unchanged, which is the only way to observe that aliasing.
//
// ONE PLAN DEFECT, resolved here and reported: the task's Implementation Plan gives ONE
// signature, `ScrubHeaders(h http.Header) http.Header`, but TWO direction-specific
// allowlists (request: Content-Type, Content-Length, Accept, User-Agent, Idempotency-Key,
// X-Request-Id, X-Correlation-Id; response: those minus Accept/User-Agent/Idempotency-Key,
// plus Date, Retry-After, X-RateLimit-*). A single direction-less function cannot express
// two lists. The only reading consistent with the stated signature — and with AC #3's
// "applies ScrubHeaders to BOTH header maps" — is the UNION of the two, which is what
// exAllowlisted below encodes. Nothing is weakened by the union: not one of the twelve
// names is a credential, and Authorization / Cookie / Set-Cookie / X-Api-Key /
// Proxy-Authorization are on neither list and stay dropped.
package submission_test

import (
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// exAllowlisted is the UNION of the plan's request and response allowlists (see the file
// header for why a union). Spelled in the plan's own casing on purpose, NOT in canonical
// form: every assertion here folds case, so these literals prove nothing about
// canonicalisation and cannot accidentally encode the bug they exist to catch.
var exAllowlisted = []string{
	"Content-Type",
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

// exCredentialHeaders are the names that must never survive a scrub. They are not merely
// "not allowlisted" — they are the reason the seam exists (PM Decision [credential
// scrubbing]: the M5-07 archive export is customer-downloadable).
var exCredentialHeaders = []string{
	"Authorization",
	"Cookie",
	"Set-Cookie",
	"X-Api-Key",
	"Proxy-Authorization",
}

// exLookupFold finds a header by CASE-INSENSITIVE name, scanning the raw map keys rather
// than going through http.Header.Get. This is load-bearing twice over:
//
//   - Get canonicalises its argument before looking up, so a result map holding a RAW
//     non-canonical key (e.g. "authorization", which a map literal preserves verbatim —
//     only Set/Add canonicalise) would be invisible to Get and a "dropped" assertion built
//     on Get would pass vacuously while the credential was still there.
//   - Conversely a KEEP assertion built on the plan's literal "X-RateLimit-Limit" would
//     miss a correctly-canonicalised "X-Ratelimit-Limit". Folding makes both directions
//     measure survival, never spelling.
func exLookupFold(h http.Header, name string) ([]string, bool) {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return nil, false
}

// exRepeat builds an n-BYTE ASCII string (bytes, not runes — every spec below is about
// byte length against MaxBodyBytes).
func exRepeat(n int) string { return strings.Repeat("a", n) }

// --- ScrubHeaders ---------------------------------------------------------------------

// Every credential-bearing name is gone after a scrub, checked case-insensitively so a
// result that kept a raw lowercase key still fails. Paired with an allowlisted header in
// the same input that MUST survive, so a ScrubHeaders that returned an empty map for
// everything could not pass.
func TestScrubHeaders_DropsCredentialHeaders(t *testing.T) {
	h := http.Header{}
	for _, name := range exCredentialHeaders {
		h.Set(name, "secret-value-for-"+name)
	}
	h.Set("Content-Type", "application/json")

	got := submission.ScrubHeaders(h)

	for _, name := range exCredentialHeaders {
		if v, ok := exLookupFold(got, name); ok {
			t.Errorf("ScrubHeaders kept credential header %q = %v, want it dropped", name, v)
		}
	}
	// The control: without this a blanket "return http.Header{}" would pass the loop above.
	if _, ok := exLookupFold(got, "Content-Type"); !ok {
		t.Errorf("ScrubHeaders dropped allowlisted Content-Type; got keys %v", exKeys(got))
	}
}

// Every allowlisted name survives WITH ITS VALUE. The three X-RateLimit-* entries are the
// point of this spec (file header, trap 1): they are the only allowlist members whose
// canonical form differs from their conventional spelling, so an allowlist built from raw
// literals and compared against canonicalised keys drops exactly these three and nothing
// else — a failure no other spec in this file would notice.
func TestScrubHeaders_KeepsAllowlistedHeaders(t *testing.T) {
	h := http.Header{}
	want := map[string]string{}
	for _, name := range exAllowlisted {
		v := "value-for-" + name
		want[name] = v
		h.Set(name, v)
	}

	got := submission.ScrubHeaders(h)

	for _, name := range exAllowlisted {
		vs, ok := exLookupFold(got, name)
		if !ok {
			t.Errorf("ScrubHeaders dropped allowlisted header %q; got keys %v", name, exKeys(got))
			continue
		}
		if len(vs) != 1 || vs[0] != want[name] {
			t.Errorf("ScrubHeaders %q = %v, want [%q]", name, vs, want[name])
		}
	}
}

// An invented header nobody has thought of yet is dropped. This is the allowlist's whole
// reason for being over a blocklist (PM Decision [credential scrubbing]): a future adapter
// that starts sending X-Future-Secret leaks nothing, because the default is DROP.
func TestScrubHeaders_UnknownHeaderDroppedFailClosed(t *testing.T) {
	h := http.Header{}
	h.Set("X-Future-Secret", "not-on-any-list")
	h.Set("Accept", "application/json")

	got := submission.ScrubHeaders(h)

	if v, ok := exLookupFold(got, "X-Future-Secret"); ok {
		t.Errorf("ScrubHeaders kept unknown header X-Future-Secret = %v, want it dropped "+
			"(allowlist, not blocklist)", v)
	}
	if _, ok := exLookupFold(got, "Accept"); !ok {
		t.Errorf("ScrubHeaders dropped allowlisted Accept; got keys %v", exKeys(got))
	}
}

// Matching is case-insensitive in BOTH directions. The input is built as a MAP LITERAL, not
// via Set/Add, because a literal stores the key VERBATIM — only Set/Add canonicalise. That
// is what makes this spec bite: an implementation that iterates the allowlist calling
// h.Get(name) reads only canonical keys, so it would neither drop the lowercase
// "authorization" (leaking it, since a scrub that never saw the key cannot remove it — and
// a copy-everything-then-delete implementation would keep it outright) nor keep the
// lowercase "content-type".
func TestScrubHeaders_IsCaseInsensitive(t *testing.T) {
	h := http.Header{
		"authorization": {"Bearer super-secret"},
		"content-type":  {"application/json"},
	}

	got := submission.ScrubHeaders(h)

	if v, ok := exLookupFold(got, "Authorization"); ok {
		t.Errorf("ScrubHeaders kept lowercase credential key \"authorization\" = %v, "+
			"want it dropped just like the canonical spelling", v)
	}
	vs, ok := exLookupFold(got, "Content-Type")
	if !ok {
		t.Fatalf("ScrubHeaders dropped lowercase allowlisted key \"content-type\"; "+
			"got keys %v", exKeys(got))
	}
	if len(vs) != 1 || vs[0] != "application/json" {
		t.Errorf("ScrubHeaders \"content-type\" = %v, want [\"application/json\"]", vs)
	}
}

// The caller's http.Header is untouched — as a MAP and as the SLICES it holds. The second
// half is the real bug: `out[k] = vs` produces a result whose value slices share a backing
// array with the input, so a later write through the result reaches into the caller's
// header. `append([]string(nil), vs...)` is the fix. Only a write-through observes it,
// which is why this spec mutates the RESULT and then re-reads the INPUT.
func TestScrubHeaders_DoesNotMutateInput(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer super-secret")
	h.Add("Accept", "application/json")
	h.Add("Accept", "text/csv")

	got := submission.ScrubHeaders(h)

	// (a) the input map still holds everything it started with.
	if v, ok := exLookupFold(h, "Authorization"); !ok || len(v) != 1 || v[0] != "Bearer super-secret" {
		t.Errorf("input Authorization after ScrubHeaders = %v (present=%v), "+
			"want [\"Bearer super-secret\"] — ScrubHeaders must not mutate its argument", v, ok)
	}
	if v, _ := exLookupFold(h, "Accept"); len(v) != 2 {
		t.Errorf("input Accept after ScrubHeaders = %v, want 2 values", v)
	}

	// (b) the result is a DIFFERENT map: writing a key into it must not appear in the input.
	got.Set("X-Sentinel", "written-into-result")
	if _, ok := exLookupFold(h, "X-Sentinel"); ok {
		t.Error("writing a new key into the ScrubHeaders result appeared in the caller's " +
			"header — the same map was returned, not a copy")
	}

	// (c) the result's VALUE SLICES do not alias the input's backing array.
	res, ok := exLookupFold(got, "Accept")
	if !ok || len(res) != 2 {
		t.Fatalf("ScrubHeaders Accept = %v (present=%v), want 2 values", res, ok)
	}
	res[0] = "TAMPERED"
	if v, _ := exLookupFold(h, "Accept"); len(v) == 0 || v[0] != "application/json" {
		t.Errorf("mutating the result's Accept[0] changed the input to %v — the value slice "+
			"was assigned directly instead of copied with append([]string(nil), vs...)", v)
	}
}

// --- SafeBody -------------------------------------------------------------------------

// The overwhelmingly common case: a small, clean body passes through byte-identically with
// both flags false. Guards against an implementation that flags unconditionally.
func TestSafeBody_UnderCapUnchanged(t *testing.T) {
	in := exRepeat(1024)

	body, clipped, coerced := submission.SafeBody(in)

	if body != in {
		t.Errorf("SafeBody changed an under-cap clean body: len(in)=%d len(out)=%d", len(in), len(body))
	}
	if clipped || coerced {
		t.Errorf("SafeBody flags = (clipped=%v, coerced=%v), want (false, false)", clipped, coerced)
	}
}

// Over the cap: clipped, and ONLY clipped. The exact-length assertion is safe because the
// input is pure ASCII — there is no rune boundary to walk back over — so the cap lands on
// MaxBodyBytes precisely. coerced MUST stay false: truncating a valid body is not coercion,
// and a shared internal boolean driving both return values dies here.
func TestSafeBody_OverCapClippedAndFlagged(t *testing.T) {
	in := exRepeat(300 * 1024)

	body, clipped, coerced := submission.SafeBody(in)

	if len(body) != submission.MaxBodyBytes {
		t.Errorf("len(SafeBody body) = %d, want exactly %d (pure ASCII: no rune boundary to "+
			"walk back over)", len(body), submission.MaxBodyBytes)
	}
	if !strings.HasPrefix(in, body) {
		t.Error("SafeBody body is not a prefix of the input — the cap must clip the tail, " +
			"not rewrite the retained bytes")
	}
	if !clipped {
		t.Error("clipped = false, want true for a 300 KiB body")
	}
	if coerced {
		t.Error("coerced = true, want false — the input is entirely valid UTF-8 with no NUL; " +
			"truncation is not coercion (the two flags are independent)")
	}
}

// The cap must not cut a multi-byte rune in half. The input places a 2-byte 'é' straddling
// byte offset MaxBodyBytes exactly, so a naive in[:MaxBodyBytes] keeps its first byte only.
//
// coerced MUST be false here, and that is the sharper half of the spec: an implementation
// that caps FIRST and then repairs with ToValidUTF8 also yields a valid string — but one
// ending in U+FFFD, with coerced=true, from an input that was perfectly valid UTF-8. The
// prefix assertion catches that substitution; the coerced assertion names it.
func TestSafeBody_DoesNotSplitUTF8Rune(t *testing.T) {
	// 'é' is 2 bytes and starts at index MaxBodyBytes-1, so it spans the boundary.
	in := exRepeat(submission.MaxBodyBytes-1) + "é" + exRepeat(1024)

	body, clipped, coerced := submission.SafeBody(in)

	if !utf8.ValidString(body) {
		t.Error("SafeBody body is not valid UTF-8 — the cap split a rune")
	}
	if len(body) != submission.MaxBodyBytes-1 {
		t.Errorf("len(SafeBody body) = %d, want %d (walk back to the last COMPLETE rune "+
			"boundary below MaxBodyBytes=%d)", len(body), submission.MaxBodyBytes-1,
			submission.MaxBodyBytes)
	}
	if !strings.HasPrefix(in, body) {
		t.Error("SafeBody body is not a prefix of the input — a straddling rune must be " +
			"DROPPED, not replaced with U+FFFD")
	}
	if !clipped {
		t.Error("clipped = false, want true")
	}
	if coerced {
		t.Error("coerced = true, want false — the input is valid UTF-8 throughout; the seam's " +
			"own truncation must not count as coercion")
	}
}

// A NUL is REMOVED and flagged. This spec is why coercion cannot be "just call
// ToValidUTF8": utf8.ValidString("\x00") is TRUE, so ToValidUTF8 leaves the NUL in place
// and the app_exchange INSERT then dies with 22021 — the exact failure this seam exists to
// prevent. clipped MUST stay false: the body is far under the cap.
func TestSafeBody_NulByteRemovedAndFlagged(t *testing.T) {
	in := "before\x00after"

	body, clipped, coerced := submission.SafeBody(in)

	if strings.ContainsRune(body, 0) {
		t.Errorf("SafeBody body still contains a NUL byte (%q) — Postgres text cannot store "+
			"one (22021); note utf8.ValidString(\"\\x00\") is true, so ToValidUTF8 alone is "+
			"not enough", body)
	}
	if body != "beforeafter" {
		t.Errorf("SafeBody body = %q, want %q (the NUL is removed; the surrounding bytes are "+
			"preserved verbatim)", body, "beforeafter")
	}
	if !coerced {
		t.Error("coerced = false, want true — a NUL was removed, which is a lossy rewrite")
	}
	if clipped {
		t.Error("clipped = true, want false — the body is 12 bytes, far under the cap")
	}
}

// Invalid UTF-8 is coerced to something storable and flagged. The lone 0xFF is not a valid
// UTF-8 sequence, so Postgres would reject it with 22021 exactly as it rejects a NUL. The
// surrounding text assertions stop a "return empty string" implementation passing.
func TestSafeBody_InvalidUTF8CoercedAndFlagged(t *testing.T) {
	in := "ok" + string([]byte{0xFF}) + "end"

	body, clipped, coerced := submission.SafeBody(in)

	if !utf8.ValidString(body) {
		t.Errorf("SafeBody body %q is not valid UTF-8 — Postgres text would reject it (22021)", body)
	}
	if strings.IndexByte(body, 0xFF) >= 0 {
		t.Errorf("SafeBody body %q still contains the raw 0xFF byte", body)
	}
	if !strings.Contains(body, "ok") || !strings.Contains(body, "end") {
		t.Errorf("SafeBody body = %q, want the valid surrounding text (\"ok\"…\"end\") "+
			"preserved — only the invalid byte may be rewritten", body)
	}
	if !coerced {
		t.Error("coerced = false, want true — invalid UTF-8 was rewritten")
	}
	if clipped {
		t.Error("clipped = true, want false — the body is 6 bytes, far under the cap")
	}
}

// The boundary itself: a body of EXACTLY MaxBodyBytes valid bytes is untouched and unflagged.
// This is the off-by-one guard — `len(s) >= MaxBodyBytes` instead of `>` reports a truncation
// that never happened, and Core AC-3's "visible flag when a body was truncated" then lies.
func TestSafeBody_ExactlyAtCapNotFlagged(t *testing.T) {
	in := exRepeat(submission.MaxBodyBytes)

	body, clipped, coerced := submission.SafeBody(in)

	if len(body) != submission.MaxBodyBytes || body != in {
		t.Errorf("len(SafeBody body) = %d, want %d unchanged", len(body), submission.MaxBodyBytes)
	}
	if clipped {
		t.Error("clipped = true at exactly MaxBodyBytes, want false — nothing was removed")
	}
	if coerced {
		t.Error("coerced = true, want false")
	}
}

// Both flags at once, independently. This is the case that kills a single internal boolean
// whose value is returned twice.
//
// The plan does not say WHERE the NUL sits, which matters: with the NUL beyond the cap, a
// cap-then-coerce implementation would legitimately drop it along with the tail and report
// coerced=false. The NUL is therefore placed INSIDE the first MaxBodyBytes (at index 100),
// so the expectation holds under EITHER order — coerce-then-cap (the recommended
// implementation) or cap-then-coerce. The spec must not silently pin an implementation
// choice it never stated.
func TestSafeBody_OverCapWithNulSetsBothFlags(t *testing.T) {
	in := exRepeat(100) + "\x00" + exRepeat(300*1024)

	body, clipped, coerced := submission.SafeBody(in)

	if !clipped {
		t.Errorf("clipped = false, want true — input is %d bytes, cap is %d",
			len(in), submission.MaxBodyBytes)
	}
	if !coerced {
		t.Error("coerced = false, want true — a NUL at index 100 sits well inside the " +
			"retained prefix under either coerce/cap order")
	}
	if strings.ContainsRune(body, 0) {
		t.Error("SafeBody body still contains a NUL byte")
	}
	if len(body) > submission.MaxBodyBytes {
		t.Errorf("len(SafeBody body) = %d, want <= %d", len(body), submission.MaxBodyBytes)
	}
	if !utf8.ValidString(body) {
		t.Error("SafeBody body is not valid UTF-8")
	}
}

// exKeys returns a header's raw keys for failure messages — raw, not canonicalised, so a
// failure caused by a non-canonical key is legible in the output instead of hidden by the
// formatting.
func exKeys(h http.Header) []string {
	out := make([]string, 0, len(h))
	for k := range h {
		out = append(out, k)
	}
	return out
}
