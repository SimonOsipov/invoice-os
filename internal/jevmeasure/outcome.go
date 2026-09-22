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
// probability Jev returned, how long the call took, and its usage.
type Outcome struct {
	Check           string
	DocumentID      string
	Field           string
	Label           string // "right" | "wrong" | "not-asked"
	ProbabilityKind ProbabilityKind
	Probability     *json.Number
	Elapsed         time.Duration
	Usage           Usage
	Failed          bool
	FailReason      string
}
