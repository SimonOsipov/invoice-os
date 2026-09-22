package jevmeasure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// thresholds is the fixed sweep (Core AC-6 / D6): four candidate doubt
// thresholds, never inferred from data.
var thresholds = []float64{0.30, 0.50, 0.70, 0.90}

// Pricing is an operator-supplied $ rate per million tokens (A50): the
// vendor's usage object carries no cost figure. The zero value means "no
// price supplied" -- Render must never print a cost derived from it.
type Pricing struct {
	InputPerMillion  float64
	OutputPerMillion float64
}

func (p Pricing) supplied() bool {
	return p.InputPerMillion != 0 || p.OutputPerMillion != 0
}

type thresholdRow struct {
	Threshold    float64
	RightFlagged int
	RightPer100  float64
	WrongMissed  int
	WrongCaught  int
}

type latencyStats struct {
	P50Ms int64
	P90Ms int64
	MaxMs int64
}

type checkSection struct {
	Check      string
	Kind       ProbabilityKind
	Documents  int
	Asked      int
	NotAsked   int
	Failed     int
	Attempted  int
	Degraded   bool
	SmallN     bool
	Thresholds []thresholdRow
	LatencyAll latencyStats
	LatencyOK  latencyStats

	TokensMean   *float64
	TokensP90    *float64
	InputAbsent  bool
	OutputAbsent bool
	CostPer1000  *float64
}

// Render sweeps outcomes into the markdown report the lead transcribes into
// the vault, plus a JSON twin for diffing a re-run. Outcomes are grouped by
// Check, in first-seen order.
func Render(outcomes []Outcome, pricing Pricing) ([]byte, []byte, error) {
	var order []string
	groups := map[string][]Outcome{}
	for _, o := range outcomes {
		if _, ok := groups[o.Check]; !ok {
			order = append(order, o.Check)
		}
		groups[o.Check] = append(groups[o.Check], o)
	}

	var md bytes.Buffer
	fmt.Fprintf(&md, "# Jev Measurement Report\n\n")

	sections := make([]checkSection, 0, len(order))
	for _, check := range order {
		sec := buildCheckSection(check, groups[check], pricing)
		sections = append(sections, sec)
		writeCheckSection(&md, sec, pricing)
	}

	writeWording(&md)

	reportJSON, err := json.MarshalIndent(sections, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("jevmeasure: encode report json: %v", err)
	}
	return md.Bytes(), reportJSON, nil
}

func buildCheckSection(check string, outs []Outcome, pricing Pricing) checkSection {
	sec := checkSection{Check: check}

	docs := map[string]bool{}
	for _, o := range outs {
		docs[o.DocumentID] = true
		if o.Label == "not-asked" {
			sec.NotAsked++
		} else {
			sec.Asked++
		}
		if o.Failed {
			sec.Failed++
		}
		if o.Failed || o.Label != "not-asked" {
			sec.Attempted++
		}
	}
	sec.Documents = len(docs)
	sec.Degraded = sec.Attempted > 0 && sec.Failed*10 > sec.Attempted
	sec.SmallN = sec.Asked < 30

	sec.Kind, sec.Thresholds = buildThresholds(outs, sec.Documents)
	sec.LatencyAll = latencyOverElapsed(outs, true)
	sec.LatencyOK = latencyOverElapsed(outs, false)

	mean, p90, inputAbsent, outputAbsent, cost := usageStats(outs, sec.Documents, pricing)
	sec.TokensMean, sec.TokensP90 = mean, p90
	sec.InputAbsent, sec.OutputAbsent = inputAbsent, outputAbsent
	sec.CostPer1000 = cost

	return sec
}

// flagged applies A51's comparator: a noul value flags low (P(true) is
// small), a choice answer's confidence flags high.
func flagged(kind ProbabilityKind, v, threshold float64) bool {
	if kind == KindChoiceConfidence {
		return v >= threshold
	}
	return v <= threshold
}

func comparatorCaption(kind ProbabilityKind) string {
	if kind == KindChoiceConfidence {
		return "confidence >= threshold"
	}
	return "noul <= threshold"
}

func buildThresholds(outs []Outcome, docs int) (ProbabilityKind, []thresholdRow) {
	var kind ProbabilityKind
	var right, wrong []float64
	for _, o := range outs {
		if o.Probability == nil {
			continue
		}
		if o.ProbabilityKind != "" {
			kind = o.ProbabilityKind
		}
		v, err := o.Probability.Float64()
		if err != nil {
			continue
		}
		switch o.Label {
		case "right":
			right = append(right, v)
		case "wrong":
			wrong = append(wrong, v)
		}
	}

	rows := make([]thresholdRow, 0, len(thresholds))
	for _, t := range thresholds {
		var rightFlagged, wrongCaught int
		for _, v := range right {
			if flagged(kind, v, t) {
				rightFlagged++
			}
		}
		for _, v := range wrong {
			if flagged(kind, v, t) {
				wrongCaught++
			}
		}
		var per100 float64
		if docs > 0 {
			per100 = float64(rightFlagged) / float64(docs) * 100
		}
		rows = append(rows, thresholdRow{
			Threshold:    t,
			RightFlagged: rightFlagged,
			RightPer100:  per100,
			WrongMissed:  len(wrong) - wrongCaught,
			WrongCaught:  wrongCaught,
		})
	}
	return kind, rows
}

// latencyOverElapsed builds the all-attempts or successful-only series
// (A45). A row never asked (not-asked, not failed) burned no call and is
// excluded from both; a failed row burned wall clock and counts only in
// all-attempts.
func latencyOverElapsed(outs []Outcome, allAttempts bool) latencyStats {
	var vals []int64
	for _, o := range outs {
		neverAttempted := o.Label == "not-asked" && !o.Failed
		if neverAttempted {
			continue
		}
		if !allAttempts && o.Failed {
			continue
		}
		vals = append(vals, o.Elapsed.Milliseconds())
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	max := int64(0)
	if len(vals) > 0 {
		max = vals[len(vals)-1]
	}
	return latencyStats{
		P50Ms: nearestRank(vals, 0.50),
		P90Ms: nearestRank(vals, 0.90),
		MaxMs: max,
	}
}

// nearestRank returns sorted[ceil(q*n)-1] (1-indexed nearest-rank), the
// method report.go names once and every dataset in report_test.go pins
// against its wrong neighbours (mean, interpolation, average-of-two).
func nearestRank(sorted []int64, q float64) int64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	idx := int(math.Ceil(q * float64(n)))
	if idx < 1 {
		idx = 1
	}
	if idx > n {
		idx = n
	}
	return sorted[idx-1]
}

func nearestRankFloat(sorted []float64, q float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	idx := int(math.Ceil(q * float64(n)))
	if idx < 1 {
		idx = 1
	}
	if idx > n {
		idx = n
	}
	return sorted[idx-1]
}

// usageStats computes input-token mean/p90 and cost per 1,000 documents
// over successfully asked rows only. A nil token pointer is excluded from
// the mean, never zeroed (AC-10), and marks its field absent.
func usageStats(outs []Outcome, docs int, pricing Pricing) (mean, p90 *float64, inputAbsent, outputAbsent bool, costPer1000 *float64) {
	var inputVals []float64
	var inputSum, outputSum float64

	for _, o := range outs {
		if o.Failed || o.Label == "not-asked" {
			continue
		}
		if o.Usage.InputTokens == nil {
			inputAbsent = true
		} else if v, err := o.Usage.InputTokens.Float64(); err == nil {
			inputVals = append(inputVals, v)
			inputSum += v
		}
		if o.Usage.OutputTokens == nil {
			outputAbsent = true
		} else if v, err := o.Usage.OutputTokens.Float64(); err == nil {
			outputSum += v
		}
	}

	if len(inputVals) > 0 {
		sort.Float64s(inputVals)
		m := inputSum / float64(len(inputVals))
		mean = &m
		p := nearestRankFloat(inputVals, 0.90)
		p90 = &p
	}

	// ceiling: sums tokens per outcome ROW, not per call; a check that puts
	// several rows on one call would double-count until CHECK-01-04 dedupes.
	if pricing.supplied() && docs > 0 && (inputSum > 0 || outputSum > 0) {
		c := (inputSum*pricing.InputPerMillion + outputSum*pricing.OutputPerMillion) / 1_000_000 / float64(docs) * 1000
		costPer1000 = &c
	}
	return mean, p90, inputAbsent, outputAbsent, costPer1000
}

func pct(n, total int) string {
	if total == 0 {
		return "0.00%"
	}
	return fmt.Sprintf("%.2f%%", float64(n)/float64(total)*100)
}

func writeCheckSection(w *bytes.Buffer, sec checkSection, pricing Pricing) {
	fmt.Fprintf(w, "## %s\n\n", sec.Check)

	total := sec.Asked + sec.NotAsked
	fmt.Fprintf(w, "documents: %d; questions asked: %d (%s); questions not asked: %d (%s); questions failed: %d (%s)\n\n",
		sec.Documents,
		sec.Asked, pct(sec.Asked, total),
		sec.NotAsked, pct(sec.NotAsked, total),
		sec.Failed, pct(sec.Failed, sec.Attempted),
	)

	if sec.SmallN {
		fmt.Fprintf(w, "This check asked fewer than 30 questions (%d) -- treat every rate above as indicative only, not a stable estimate.\n\n", sec.Asked)
	}
	if sec.Degraded {
		fmt.Fprintf(w, "DEGRADED: %d of %d production-shaped calls attempted failed (more than one in ten) -- %s's numbers below are unreliable.\n\n", sec.Failed, sec.Attempted, sec.Check)
	}

	fmt.Fprintf(w, "comparator: %s\n\n", comparatorCaption(sec.Kind))
	fmt.Fprintf(w, "| threshold | right flagged | right flagged per 100 documents | wrong missed | wrong caught |\n")
	fmt.Fprintf(w, "|---|---|---|---|---|\n")
	for _, row := range sec.Thresholds {
		fmt.Fprintf(w, "| %.2f | %d | %.2f | %d | %d |\n", row.Threshold, row.RightFlagged, row.RightPer100, row.WrongMissed, row.WrongCaught)
	}
	fmt.Fprintf(w, "\n")

	fmt.Fprintf(w, "call latency, all attempts (ms): p50 %d, p90 %d, max %d\n", sec.LatencyAll.P50Ms, sec.LatencyAll.P90Ms, sec.LatencyAll.MaxMs)
	fmt.Fprintf(w, "call latency, successful only (ms): p50 %d, p90 %d, max %d\n", sec.LatencyOK.P50Ms, sec.LatencyOK.P90Ms, sec.LatencyOK.MaxMs)
	fmt.Fprintf(w, "budget: read from the call latency, all attempts series above, never the successful-only one.\n\n")

	writeUsage(w, sec, pricing)
}

func writeUsage(w *bytes.Buffer, sec checkSection, pricing Pricing) {
	if sec.TokensMean != nil {
		fmt.Fprintf(w, "input tokens per call: mean %.0f, p90 %.0f\n", *sec.TokensMean, *sec.TokensP90)
	}
	if sec.InputAbsent || sec.OutputAbsent {
		fmt.Fprintf(w, "usage: not reported by the vendor for some calls\n")
	}
	if sec.CostPer1000 != nil {
		fmt.Fprintf(w, "cost per 1,000 documents: $%.2f (price used: $%.2f / 1M input tokens, $%.2f / 1M output tokens, operator-supplied)\n\n",
			*sec.CostPer1000, pricing.InputPerMillion, pricing.OutputPerMillion)
	} else {
		fmt.Fprintf(w, "cost per 1,000 documents: price not supplied — cost not computed\n\n")
	}
}

func writeWording(w *bytes.Buffer) {
	fmt.Fprintf(w, "## Wording\n\n")
	fmt.Fprintf(w, "### Value check\n\n%s\n\ntrue: %s\n\nfalse: %s\n\n",
		ValueCheckInstructions, ValueCheckCriteriaTrue, ValueCheckCriteriaFalse)
	fmt.Fprintf(w, "### Mapping check\n\n%s\n\ntrue: %s\n\nfalse: %s\n\n",
		MappingCheckInstructions, MappingCheckCriteriaTrue, MappingCheckCriteriaFalse)
	fmt.Fprintf(w, "### Document type check\n\n%s\n\n", DocumentTypeInstructions)
	for _, opt := range documentTypeOrder {
		fmt.Fprintf(w, "- **%s**: %s\n", opt, DocumentTypeCriteria[opt])
	}
	fmt.Fprintf(w, "\n")
}
