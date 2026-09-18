// aireading.go: the AI's per-document reading and the checks that decide whether one of its
// values may change a header field. mergeAI (aimerge.go) calls checkAI; the extract worker
// calls askAI.
package extraction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
)

// aiUnavailableField is the one row a document gets when its AI call fails (AIR-04, Q9).
const aiUnavailableField = "document_ai_reading"

// AIReader is the slice of *ai.Client the worker uses; nil is off.
type AIReader interface {
	Enabled() bool
	Call(ctx context.Context, req ai.Request) (map[string]any, error)
}

// aiSystem and aiTextIntro are tools/aimodeltest/run.py's SYSTEM and TEXT_INTRO, byte for byte
// (TestAIPrompt_MatchesTheMeasuredHarness): the prompt is the one already measured against real
// models.
const aiSystem = `You extract the header fields of one invoice for a Nigerian e-invoicing system.

Return a JSON object with exactly these keys. Each value is a string, or null when the document does not print that field. Never guess, compute or infer a value that is not printed.
- invoice_number: the invoice's own number, exactly as printed. Not a purchase order, customer, account, RC or payment reference number.
- issue_date: the date the invoice was issued, as YYYY-MM-DD. Not a due date, delivery date or billing period.
- supplier_name: the seller issuing the invoice, exactly as printed.
- supplier_tin: the seller's Tax Identification Number, exactly as printed.
- buyer_name: the party being billed, exactly as printed.
- buyer_tin: the buyer's Tax Identification Number, exactly as printed.
- currency: the ISO 4217 code of the invoice currency. A printed ₦, N or Naira means NGN.
- subtotal: the amount before VAT, as digits with a decimal point and no currency symbol or thousands separators (1250000.00).
- vat: the VAT amount (not the rate), same number format.
- total: the total amount payable, same number format.`

const aiTextIntro = `The document text below was read by a PDF parser. Each line is one visual row on the page. y is the row's vertical position and x each text run's horizontal position, both from 0 to 1 measured from the top-left corner.`

const (
	aiPromptPageFmt = "--- page %d ---"
	aiPromptRowFmt  = "y=%.3f | %s"
	aiPromptRunFmt  = "x=%.2f %s"
)

// aiFieldSchema is the JSON schema askAI sends: HeaderFields, string-or-null, no line items.
var aiFieldSchema = aiSchemaFor(HeaderFields)

func aiSchemaFor(fields []string) json.RawMessage {
	props := make(map[string]any, len(fields))
	for _, f := range fields {
		props[f] = map[string]any{"type": []string{"string", "null"}}
	}
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             fields,
		"properties":           props,
	}
	b, _ := json.Marshal(schema) // fixed, marshalable shape: cannot fail
	return b
}

// aiPromptText is aiTextIntro plus one line per Docling token in reader order, the AIR-01
// measured shape (run.py doc_text).
func aiPromptText(pages []TokenPage) string {
	var lines []string
	for _, p := range pages {
		lines = append(lines, fmt.Sprintf(aiPromptPageFmt, p.Number))
		for _, tok := range p.Tokens {
			run := fmt.Sprintf(aiPromptRunFmt, tok.Region.X0, tok.Text)
			lines = append(lines, fmt.Sprintf(aiPromptRowFmt, tok.Region.Y0, run))
		}
	}
	return aiTextIntro + "\n\n" + strings.Join(lines, "\n")
}

// aiFailed: any Call error sends the document to manual entry (BQ1), except an ended caller
// context (shutdown or job timeout, not an AI answer) and ErrOff (AC-6).
func aiFailed(err error) bool {
	return err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded) &&
		!errors.Is(err, ai.ErrOff)
}

// askAI asks once. The bool reports a failed call (aiFailed); the answer is nil then.
func askAI(ctx context.Context, r AIReader, pages []TokenPage) (map[string]string, bool) {
	if r == nil || !r.Enabled() {
		return nil, false
	}
	ans, err := r.Call(ctx, ai.Request{
		Purpose:    ai.PurposeDocument,
		System:     aiSystem,
		Text:       aiPromptText(pages),
		SchemaName: "invoice_fields",
		Schema:     aiFieldSchema,
	})
	if err != nil {
		return nil, aiFailed(err)
	}
	var out map[string]string
	for _, f := range HeaderFields {
		s, ok := ans[f].(string)
		if !ok || strings.TrimSpace(s) == "" {
			continue
		}
		if out == nil {
			out = make(map[string]string)
		}
		out[f] = s
	}
	return out, false
}

// paymentLabelRE is check (c): a supplier_name or buyer_name value fails behind a payment-account
// label. Kept out of anchorLexicon so it never moves a stored layout fingerprint.
var paymentLabelRE = regexp.MustCompile(`(?i)\b(account\s*name|account\s*no|account\s*number|bank)\b`)

// aiInvoiceNoLabel is anchorLabelMatchers' "invoice_no" entry, looked up once for check (d).
var aiInvoiceNoLabel = anchorMatcherRE("invoice_no")

func anchorMatcherRE(id string) *regexp.Regexp {
	for _, m := range anchorLabelMatchers {
		if m.ID == id {
			return m.RE
		}
	}
	return nil
}

// aiReading is one AI value that passed every check: its normalised form and the page region it
// was found in.
type aiReading struct {
	Value  string
	Region *Region
}

func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// aiOccurrence is one place on the page an AI value's form was found: the (labelView'd) text in
// front of it, and the region of the token line it came from.
type aiOccurrence struct {
	front  string
	region *Region
}

// checkAI runs (a)-(d) against one AI-answered field. A value passing every check is checked;
// its region is the page check's first qualifying occurrence.
func checkAI(field, raw string, pages []TokenPage) (aiReading, bool) {
	shape, ok := tier1Shape(field)
	if !ok {
		return aiReading{}, false
	}

	want := shape.Normalize(raw)
	var value string
	needD := false
	switch {
	case len(want) == 1:
		value = want[0]
	case len(want) == 0 && field == "invoice_number":
		t := strings.TrimSpace(raw)
		if !allASCIIDigits(t) || !reInvNum.MatchString(t) {
			return aiReading{}, false
		}
		value = t
		needD = true
	default:
		return aiReading{}, false // includes the ambiguous two-reading date
	}

	occurrences := aiOccurrences(shape, raw, want, pages)
	if len(occurrences) == 0 {
		return aiReading{}, false // (b)
	}

	if field == "supplier_name" || field == "buyer_name" {
		for _, occ := range occurrences {
			if paymentLabelRE.MatchString(occ.front) {
				return aiReading{}, false // (c)
			}
		}
	}

	if needD {
		for _, occ := range occurrences {
			if aiInvoiceNoLabel.MatchString(occ.front) {
				return aiReading{Value: value, Region: occ.region}, true
			}
		}
		return aiReading{}, false // (d)
	}

	return aiReading{Value: value, Region: occurrences[0].region}, true
}

// aiOccurrences walks every run of consecutive words inside each token's own line (never joining
// across tokens) and keeps one occurrence per run/form that reads as raw.
// ceiling: O(tokens x words^2 x len(anchorLexicon)) per field; revisit if a real document's page
// count makes this slow.
func aiOccurrences(shape Shape, raw string, want []string, pages []TokenPage) []aiOccurrence {
	var out []aiOccurrence
	for _, p := range pages {
		for i, tok := range p.Tokens {
			words := strings.Fields(tok.Text)
			line := strings.Join(words, " ")
			starts, ends := aiWordOffsets(words)
			for wi := range words {
				for wj := wi + 1; wj <= len(words); wj++ {
					run := line[starts[wi]:ends[wj-1]]
					for _, form := range aiRunForms(run) {
						if !aiFormHits(shape, form, raw, want) {
							continue
						}
						at := ends[wj-1] - len(form)
						front := strings.TrimSpace(line[:at])
						if front == "" && i > 0 {
							front = p.Tokens[i-1].Text
						}
						out = append(out, aiOccurrence{
							front:  labelView(front),
							region: usableRegion(tok.Region),
						})
					}
				}
			}
		}
	}
	return out
}

// aiWordOffsets is each word's byte start/end within strings.Join(words, " ") -- the "line" C4
// measures offsets into, not the token's raw text (TestAICheck_ComparesWithWhitespaceCollapsed).
func aiWordOffsets(words []string) (starts, ends []int) {
	starts = make([]int, len(words))
	ends = make([]int, len(words))
	pos := 0
	for i, w := range words {
		starts[i] = pos
		pos += len(w)
		ends[i] = pos
		pos++ // the single joining space
	}
	return starts, ends
}

// aiRunForms is a run's own text plus any anchor-lexicon label remainder inside it --
// tokenCarries' two raw forms (learn.go), reproduced per run so the matched form keeps a byte
// offset tokenCarries' bool return would lose.
func aiRunForms(run string) []string {
	forms := []string{run}
	for _, m := range anchorLabelMatchers {
		if loc := m.RE.FindStringIndex(run); loc != nil {
			forms = append(forms, sameTokenValue(run, loc))
		}
	}
	return forms
}

// aiFormHits is rule B: whitespace-collapsed equality with raw, or a shape reading form the same
// as one of raw's own readings. The equality branch also carries exception (d), where want is
// empty.
func aiFormHits(shape Shape, form, raw string, want []string) bool {
	if collapse(form) == collapse(raw) {
		return true
	}
	for _, w := range want {
		if readsAs(shape, form, w) {
			return true
		}
	}
	return false
}

func allASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
