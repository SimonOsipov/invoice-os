package jevmeasure

import (
	"encoding/json"
	"time"
)

// ProbabilityKind selects the comparator Report sweeps with (A51): a noul
// value flags low, a choice answer's confidence flags high.
type ProbabilityKind string

const (
	KindNoul             ProbabilityKind = "noul"
	KindChoiceConfidence ProbabilityKind = "choice_confidence"
)

// Outcome is one question's result: which check, which document, which
// field or option, whether it was labelled right, wrong or not asked, the
// probability Jev returned, how long the call took, and its usage. Tagged
// so $JEV_OUT/jev-outcomes.json (the merge ledger, MergeOutcomes) is legible.
type Outcome struct {
	Check           string          `json:"check"`
	DocumentID      string          `json:"document_id"`
	Field           string          `json:"field"`
	Label           string          `json:"label"` // "right" | "wrong" | "not-asked"
	ProbabilityKind ProbabilityKind `json:"probability_kind,omitempty"`
	Probability     *json.Number    `json:"probability,omitempty"`
	Elapsed         time.Duration   `json:"elapsed_ns"`
	Usage           Usage           `json:"usage"`
	Failed          bool            `json:"failed,omitempty"`
	// Reason is why this row carries no answer: an extraction.Reason string, a variant skip
	// reason, or a vendor error. Empty when Label == "right"/"wrong".
	Reason string `json:"reason,omitempty"`
	// Variant marks a row planted with a known-wrong value (CHECK-01-04 A28/A29): excluded from
	// the budget and from the production cost.
	Variant bool `json:"variant,omitempty"`
	// Answer is the option a `choice` question answered; empty for a `noul` row. Field carries
	// the true type on a choice row, Answer the answered one.
	Answer string `json:"answer,omitempty"`
	// CallID groups rows that share one Ask() call, so usageStats/latencyOverElapsed fold once
	// per call rather than once per row. Empty is its own call (every CHECK-01-02 fixture).
	CallID string `json:"call_id,omitempty"`
	// Reader names what produced this document's text (docling, pdfium, importer.Decode (csv));
	// empty renders no Provenance row (CHECK-01-02 fixtures are unaffected).
	Reader string `json:"reader,omitempty"`
}
