package importer

import (
	"time"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

// RuleBreak is one rule that a document reading broke on one header field.
type RuleBreak struct{ Field, RuleKey, Message string }

// ruleBreakFields maps a violation path to the unlocked header field it judged.
var ruleBreakFields = map[string]string{
	"issue_date": "issue_date",
	"currency":   "currency",
	"subtotal":   "subtotal",
	"vat":        "vat",
	"total":      "total",
	"buyer.tin":  "buyer_tin",
	"buyer.name": "buyer_name",
}

// ceiling: Evaluate is best effort, cut off at 10 s; raise it if the gate p99 passes ~5 s.
var ruleBreakEvaluateTimeout = 10 * time.Second

// ruleBreaks keeps the violations that point at a header field the reading has a value for,
// one per (field, rule key).
func ruleBreaks(ex SettledExtraction, vs []invoice.Violation) []RuleBreak {
	valued := make(map[string]bool, len(ex.Fields))
	for _, f := range ex.Fields {
		if f.Value != nil {
			valued[f.Name] = true
		}
	}
	var out []RuleBreak
	seen := map[[2]string]bool{}
	for _, v := range vs {
		field, ok := ruleBreakFields[v.Path]
		if !ok || !valued[field] || seen[[2]string{field, v.RuleKey}] {
			continue
		}
		seen[[2]string{field, v.RuleKey}] = true
		out = append(out, RuleBreak{field, v.RuleKey, v.Message})
	}
	return out
}
