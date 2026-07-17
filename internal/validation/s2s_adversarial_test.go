// M4-04-03 (task-109) -- Stage 4 (QA Verify, Mode B) adversarial coverage,
// added on top of the executor's green VB-03..06 without modifying any of
// them. Makes s2s.go's documented empty-configured-token invariant
// LOAD-BEARING: a runnable, discoverable assertion instead of prose only.
//
// crypto/subtle.ConstantTimeCompare([]byte(""), []byte("")) == 1 (verified
// directly, `go run` scratch), so S2SMiddleware("") admits ANY caller who
// simply sends no X-S2S-Token header at all -- the default state of every
// HTTP client that has not been told to set one. S2SMiddleware itself has
// no internal defense against being constructed with an empty token: the
// doc comment records that cmd/validation/main.go's mustEnv("S2S_TOKEN")
// (log.Fatalf's at boot on an unset var) is the ONLY thing standing between
// "empty token" and "open endpoint" today.
//
// This file does not change that design (a QA stage adds tests, not
// implementation) and does not claim the doc comment is wrong -- mustEnv's
// boot-time fail-fast is a real, verified control (cmd/validation/main.go),
// and defended-by-construction is a legitimate pattern already used
// elsewhere in this story ([s2s-identity]). What was missing is that the
// invariant lived ONLY in prose: nothing in the test suite would notice if
// it silently became false. These two tests pin the exact boundary of the
// risk (admits on a genuinely EMPTY presented token; still rejects any
// GUESSED non-empty one) so a future change to S2SMiddleware, or to
// mustEnv's call site, has a test in its way -- not just a comment.
package validation

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// doS2SWithConfiguredToken drives S2SMiddleware(configuredToken) directly
// (rather than s2s_test.go's doS2S, which hardcodes s2sTestToken as the
// CONFIGURED token) -- these tests specifically need to vary the configured
// side, which doS2S has no parameter for.
func doS2SWithConfiguredToken(t *testing.T, configuredToken, headerToken string) *httptest.ResponseRecorder {
	t.Helper()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r := httptest.NewRequest("POST", "/v1/validate/batch", strings.NewReader(`{"invoices":[]}`))
	if headerToken != "" {
		r.Header.Set("X-S2S-Token", headerToken)
	}
	rec := httptest.NewRecorder()
	S2SMiddleware(configuredToken)(next).ServeHTTP(rec, r)
	return rec
}

// TestS2S_EmptyConfiguredTokenAdmitsCallerWithNoTokenHeader pins the
// documented risk as an executable fact: S2SMiddleware("") (an empty
// CONFIGURED token) admits a caller that sends NO X-S2S-Token header at
// all -- r.Header.Get returns "" for an absent header, and
// ConstantTimeCompare("", "") == 1. Today this configuration is
// unreachable in production because cmd/validation/main.go sources the
// token via mustEnv("S2S_TOKEN"), which log.Fatalf's at boot rather than
// starting the server with an empty token (verified: cmd/validation/main.go
// calls mustEnv, and mustEnv log.Fatalf's when os.Getenv returns "").
//
// If S2SMiddleware's construction site is ever changed to source the token
// more permissively (e.g. mustEnv swapped for a bare os.Getenv, or a
// default-empty config flag), THIS is the behavior that opens up: every
// caller with no special header at all is treated as a trusted fleet peer.
// This test's continued existence -- and its PASS today -- is the marker:
// anyone hardening S2SMiddleware against this must come here and change
// what it asserts, rather than the risk silently drifting out of view.
func TestS2S_EmptyConfiguredTokenAdmitsCallerWithNoTokenHeader(t *testing.T) {
	rec := doS2SWithConfiguredToken(t, "" /* configured token */, "" /* no header presented */)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- S2SMiddleware(\"\") is documented (s2s.go) to admit a caller "+
			"presenting no token when the CONFIGURED token is itself empty (ConstantTimeCompare(\"\",\"\")==1); "+
			"if this now 401s, either S2SMiddleware gained internal empty-token defense (update this test to "+
			"assert 401 and delete the risk-tracking framing above) or something else changed -- either way "+
			"this test existing is what makes the invariant discoverable instead of assumed", rec.Code)
	}
}

// TestS2S_EmptyConfiguredTokenStillRejectsGuessedToken bounds the risk
// above: an empty CONFIGURED token does NOT admit every possible caller --
// only one presenting literally no token (or an explicitly empty one)
// slips through. Any caller presenting a real, non-empty guessed value
// still gets 401, because ConstantTimeCompare requires equal-length slices
// to return 1 and a non-empty guess never matches a zero-length configured
// token. This is what confines the empty-token gap to "reachable only if
// mustEnv's fail-fast is bypassed AND the caller sends nothing" rather than
// "any caller who tries" -- worth pinning separately so a regression that
// widens the hole (e.g. a comparison that treats "any non-matching token"
// as equivalent) would be caught here even if the no-header case above were
// somehow accidentally "fixed" into always-401.
func TestS2S_EmptyConfiguredTokenStillRejectsGuessedToken(t *testing.T) {
	rec := doS2SWithConfiguredToken(t, "" /* configured token */, "some-guessed-token")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 -- an empty CONFIGURED token must still reject a caller "+
			"presenting a real, non-empty guessed token (only a genuinely empty presented token "+
			"can match a genuinely empty configured one)", rec.Code)
	}
}
