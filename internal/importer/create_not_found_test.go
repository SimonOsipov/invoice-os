package importer

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

// invoice.Store.Create answers ErrNotFound when the client left the caller's set after EntityVisible.
func TestStatusForErr_InvoiceNotFoundIs404(t *testing.T) {
	for name, err := range map[string]error{
		"bare":    invoice.ErrNotFound,
		"wrapped": fmt.Errorf("create: %w", invoice.ErrNotFound),
	} {
		status, msg := statusForErr(err)
		if status != http.StatusNotFound || msg != "not found" {
			t.Errorf("%s: statusForErr = %d %q, want 404 \"not found\"", name, status, msg)
		}
	}
}

func TestDomainCreateErrorMessage_InvoiceNotFoundQuarantinesTheRow(t *testing.T) {
	msg, ok := domainCreateErrorMessage(fmt.Errorf("create: %w", invoice.ErrNotFound))
	if !ok || msg == "" {
		t.Errorf("domainCreateErrorMessage(invoice.ErrNotFound) = %q, %v, want a quarantine message", msg, ok)
	}
}
