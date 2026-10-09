package importer

import (
	"context"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

// QA Mode-A stub (ENGI-18-02): compiles the red tests, implements nothing. The executor replaces
// this file; RecordRuleBreaks moves to store.go.

// RuleBreak is one rule that a document reading broke on one header field.
type RuleBreak struct{ Field, RuleKey, Message string }

// ruleBreakFields maps a violation path to the header field it judged.
var ruleBreakFields = map[string]string{}

// ruleBreakEvaluateTimeout is a var so a test can shorten it.
var ruleBreakEvaluateTimeout = 10 * time.Second

func ruleBreaks(ex SettledExtraction, vs []invoice.Violation) []RuleBreak { return nil }

func (s *Store) RecordRuleBreaks(ctx context.Context, jobID, ruleSetVersionID string, breaks []RuleBreak) error {
	return nil
}
