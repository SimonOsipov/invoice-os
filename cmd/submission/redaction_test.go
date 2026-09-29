package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform"
)

// Matches internal/platform's filter tests; a bare extracted value, so only
// the site's %q puts it inside quotes.
const redactMarkerTIN = "87654321-0009"

func TestInvoiceEditForIssueDateErrorIsScrubbedForSentry(t *testing.T) {
	v := redactMarkerTIN
	_, err := invoiceEditFor("issue_date", &v)
	if err == nil {
		t.Fatal("got no error; the fixture no longer fails, so nothing is proven")
	}
	if !errors.Is(err, extraction.ErrValueRefused) {
		t.Errorf("err = %v, want ErrValueRefused", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, redactMarkerTIN) {
		t.Errorf("error %q does not carry marker %q; the fixture proves nothing", msg, redactMarkerTIN)
	}
	scrubbed := platform.ScrubText(msg)
	if !strings.Contains(scrubbed, "issue_date") {
		t.Errorf("ScrubText(%q) = %q, want it to keep issue_date", msg, scrubbed)
	}
	if strings.Contains(scrubbed, redactMarkerTIN) {
		t.Errorf("ScrubText(%q) = %q still carries marker %q", msg, scrubbed, redactMarkerTIN)
	}
}
