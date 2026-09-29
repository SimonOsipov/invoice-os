package importer

import (
	"errors"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

// Markers match internal/platform's filter tests. Each is a bare CSV value, so
// only the site's %q puts it inside quotes.
const (
	redactMarkerTIN = "87654321-0009"
	redactMarkerAmt = "9999999.99"
	redactMarkerIRN = "INV-SECRET-2026"
)

// assertScrubbedForSentry: the raw message keeps marker and anchor (Railway
// logs and the import screen); the Sentry copy keeps the anchor and loses the marker.
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

// A one-column header row makes each fixture fail the same way whatever the map order.
func TestResolveMappingErrorsAreScrubbedForSentry(t *testing.T) {
	t.Run("unknown_key", func(t *testing.T) {
		_, err := resolveMapping(map[string]string{"invoice_number": "No", redactMarkerTIN: "x"}, []string{"No"})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("err = %v, want ErrValidation", err)
		}
		assertScrubbedForSentry(t, err, redactMarkerTIN, "is not a recognized canonical field")
	})

	t.Run("missing_header", func(t *testing.T) {
		_, err := resolveMapping(map[string]string{"invoice_number": redactMarkerIRN}, []string{"No"})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("err = %v, want ErrValidation", err)
		}
		assertScrubbedForSentry(t, err, redactMarkerIRN, "not found in header row")
	})
}

func TestParseIssueDateErrorIsScrubbedForSentry(t *testing.T) {
	_, err := parseIssueDate(redactMarkerAmt)
	assertScrubbedForSentry(t, err, redactMarkerAmt, "issue_date")
}
