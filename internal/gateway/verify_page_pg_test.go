package gateway

// CI runs this in the RLS job, where DATABASE_GATEWAY_URL is set and the database has no GoTrue schema.

import (
	"net/http"
	"strings"
	"testing"
)

// The RLS database has no auth.confirm_link_address: the real lookup must fail closed, not render an unnamed page.
func TestPoolLinkAddress_AMissingFunctionIsTheFailedNotice(t *testing.T) {
	d := requireHandoffDB(t)
	log, buf := captureLog()
	h := vpHandlerWith(t, PoolLinkAddress(d.a), vpLimit(5), true, log)

	rec := vpDo(t, h, http.MethodGet, vpQuery(vpToken))

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != vpFailedLoc {
		t.Fatalf("answer = %d Location %q, want 303 %s", rec.Code, rec.Header().Get("Location"), vpFailedLoc)
	}
	if strings.Contains(rec.Body.String(), "<form") {
		t.Error("the failed notice renders the form")
	}
	lines := recordsNamed(t, buf, "verify-page: link address lookup failed")
	if len(lines) != 1 || warnCount(t, buf) != 1 {
		t.Fatalf("%d lookup-failed lines and %d WARNs, want 1 and 1: %s", len(lines), warnCount(t, buf), buf.String())
	}
	if c := lines[0]["class"]; c != "3F000" && c != "42883" && c != "42501" {
		t.Errorf("class = %v, want one of 3F000, 42883, 42501", c)
	}
	if strings.Contains(buf.String(), vpToken) {
		t.Errorf("the log holds the token: %s", buf.String())
	}
}
