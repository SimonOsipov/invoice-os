package archive

import (
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

// Markers match internal/platform's filter tests. Each sits bare in the raw
// cell, so only the site's %q puts it inside quotes.
const (
	redactMarkerTIN  = "87654321-0009"
	redactMarkerCred = "cred-SECRET-4411"
)

// assertScrubbedForSentry: the raw message keeps marker and anchor (Railway
// logs); the Sentry copy keeps the anchor and loses the marker.
func assertScrubbedForSentry(t *testing.T, err error, marker, anchor string) {
	t.Helper()
	if err == nil {
		t.Fatal("got no error; the fixture no longer fails, so nothing is proven")
	}
	msg := err.Error()
	if !strings.Contains(msg, marker) {
		t.Errorf("error %q does not carry marker %q; the fixture proves nothing", msg, marker)
	}
	scrubbed := platform.ScrubText(msg)
	if !strings.Contains(scrubbed, anchor) {
		t.Errorf("ScrubText(%q) = %q, want it to keep %q", msg, scrubbed, anchor)
	}
	if strings.Contains(scrubbed, marker) {
		t.Errorf("ScrubText(%q) = %q still carries marker %q", msg, scrubbed, marker)
	}
}

func TestCompactJSONErrorIsScrubbedForSentry(t *testing.T) {
	_, err := compactJSON(`[{"reason": ` + redactMarkerTIN + `}]`)
	assertScrubbedForSentry(t, err, redactMarkerTIN, "archive: compact json")
}

func TestRescrubHeadersErrorIsScrubbedForSentry(t *testing.T) {
	_, err := rescrubHeaders(`{"Authorization": Bearer-` + redactMarkerCred + `}`)
	assertScrubbedForSentry(t, err, redactMarkerCred, "unmarshal headers")
}
