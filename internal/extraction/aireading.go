// aireading.go: the AI's per-document reading and the checks that decide whether one of its
// values may change a header field. Stubs only in this subtask; mergeAI (AIR-03-02) is the
// first caller of askAI and checkAI.
package extraction

import (
	"context"
	"encoding/json"
	"regexp"

	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
)

// AIReader is the slice of *ai.Client the worker uses; nil is off.
type AIReader interface {
	Enabled() bool
	Call(ctx context.Context, req ai.Request) (map[string]any, error)
}

const (
	aiSystem    = ""
	aiTextIntro = ""
)

const (
	aiPromptPageFmt = ""
	aiPromptRowFmt  = ""
	aiPromptRunFmt  = ""
)

// aiFieldSchema is the JSON schema askAI sends: HeaderFields, string-or-null, no line items.
var aiFieldSchema = aiSchemaFor(HeaderFields)

func aiSchemaFor(fields []string) json.RawMessage {
	return nil
}

func aiPromptText(pages []TokenPage) string {
	return ""
}

func askAI(ctx context.Context, r AIReader, pages []TokenPage) map[string]string {
	return nil
}

// paymentLabelRE is check (c): a supplier_name or buyer_name value fails behind a payment-account
// label. Kept out of anchorLexicon so it never moves a stored layout fingerprint.
var paymentLabelRE *regexp.Regexp

// aiInvoiceNoLabel is anchorLabelMatchers' "invoice_no" entry, looked up once for check (d).
var aiInvoiceNoLabel *regexp.Regexp

// aiReading is one AI value that passed every check: its normalised form and the page region it
// was found in.
type aiReading struct {
	Value  string
	Region *Region
}

func collapse(s string) string {
	return ""
}

func checkAI(field, raw string, pages []TokenPage) (aiReading, bool) {
	return aiReading{}, false
}
