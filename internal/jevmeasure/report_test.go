// report_test.go: the renderer's tests, against outcomes built in-process --
// no vendor, no network. D1-D5 are the architect's fixtures; the jmRank*
// ones are QA's, added because no D1-D5 percentile rank was fractional
// enough to tell ceil from floor -- see TestReport_TheLatencyPercentileRoundsTheRankUp.
package jevmeasure

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func jmNum(s string) *json.Number {
	n := json.Number(s)
	return &n
}

// jmLineWithTokens returns the first report line containing every tok, so a
// row can be found by an anchor unlikely to collide with raw outcome data
// echoed elsewhere (D4's right values sit exactly on the swept thresholds).
func jmLineWithTokens(t *testing.T, md string, toks ...string) string {
	t.Helper()
	for _, line := range strings.Split(md, "\n") {
		all := true
		for _, tok := range toks {
			if !strings.Contains(line, tok) {
				all = false
				break
			}
		}
		if all {
			return line
		}
	}
	t.Fatalf("no report line contains all of %v", toks)
	return ""
}

// jmStripToken removes tok once, so a standalone-digit search on the
// remainder can't match inside tok itself (e.g. the leading 0 of "0.90").
func jmStripToken(s, tok string) string {
	return strings.Replace(s, tok, "", 1)
}

func jmHasNumber(s, n string) bool {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(n) + `\b`).MatchString(s)
}

// jmSection returns just the "## <check>" block, so a per-check assertion
// can't be satisfied by text another check's section printed.
func jmSection(t *testing.T, md, check string) string {
	t.Helper()
	head := "## " + check + "\n"
	i := strings.Index(md, head)
	if i == -1 {
		t.Fatalf("report has no %q section", check)
	}
	rest := md[i+len(head):]
	if j := strings.Index(rest, "\n## "); j != -1 {
		return rest[:j]
	}
	return rest
}

// D1 -- percentiles. 10 latencies, all successful.
func d1Outcomes() []Outcome {
	ms := []int{10, 12, 14, 16, 18, 20, 30, 40, 60, 500}
	out := make([]Outcome, len(ms))
	for i, m := range ms {
		out[i] = Outcome{
			Check: "value_check", DocumentID: fmt.Sprintf("d1-%d", i), Field: "amount",
			Label: "right", ProbabilityKind: KindNoul, Probability: jmNum("0.99"),
			Elapsed: time.Duration(m) * time.Millisecond,
		}
	}
	return out
}

// D2 -- the two latency series: 8 successes plus 2 failures whose elapsed
// time only shows up in the all-attempts series.
func d2Outcomes() []Outcome {
	successMs := []int{10, 12, 14, 16, 18, 20, 30, 40}
	failMs := []int{2500, 3000}
	var out []Outcome
	for i, m := range successMs {
		out = append(out, Outcome{
			Check: "value_check", DocumentID: fmt.Sprintf("d2-ok-%d", i), Field: "amount",
			Label: "right", ProbabilityKind: KindNoul, Probability: jmNum("0.99"),
			Elapsed: time.Duration(m) * time.Millisecond,
		})
	}
	for i, m := range failMs {
		out = append(out, Outcome{
			Check: "value_check", DocumentID: fmt.Sprintf("d2-fail-%d", i), Field: "amount",
			Label: "not-asked", Failed: true, Reason: "vendor error",
			ProbabilityKind: KindNoul, Elapsed: time.Duration(m) * time.Millisecond,
		})
	}
	return out
}

// D3 -- tokens and cost. 18 successful production calls with usage, 2 failed
// (no usage); the 7 excluded variant calls simply never enter this slice.
func d3Outcomes() []Outcome {
	input := []string{"100", "110", "120", "130", "140", "150", "160", "170", "180", "190", "200", "210", "220", "230", "240", "250", "1100", "1500"}
	var out []Outcome
	for i, v := range input {
		out = append(out, Outcome{
			Check: "value_check", DocumentID: fmt.Sprintf("d3-%d", i), Field: "amount",
			Label: "right", ProbabilityKind: KindNoul, Probability: jmNum("0.99"),
			Usage: Usage{InputTokens: jmNum(v), OutputTokens: jmNum("20")},
		})
	}
	for i := 0; i < 2; i++ {
		out = append(out, Outcome{
			Check: "value_check", DocumentID: fmt.Sprintf("d3-fail-%d", i), Field: "amount",
			Label: "not-asked", Failed: true, Reason: "vendor error",
			ProbabilityKind: KindNoul,
		})
	}
	return out
}

// D4 -- the threshold sweep. 9 outcomes (5 right, 4 wrong) over 3 documents;
// the right values sit exactly on each swept threshold.
func d4Outcomes() []Outcome {
	right := []string{"0.95", "0.90", "0.70", "0.50", "0.30"}
	wrong := []string{"0.10", "0.45", "0.65", "0.85"}
	docs := []string{"d1", "d2", "d3"}
	var out []Outcome
	for i, v := range right {
		out = append(out, Outcome{
			Check: "value_check", DocumentID: docs[i%3], Field: "amount",
			Label: "right", ProbabilityKind: KindNoul, Probability: jmNum(v),
		})
	}
	for i, v := range wrong {
		out = append(out, Outcome{
			Check: "value_check", DocumentID: docs[i%3], Field: "amount",
			Label: "wrong", ProbabilityKind: KindNoul, Probability: jmNum(v),
		})
	}
	return out
}

// D5 -- the degraded-run boundary: 20 production-shaped calls, `failed` of them failed.
func d5Outcomes(failed int) []Outcome {
	var out []Outcome
	for i := 0; i < 20; i++ {
		doc := fmt.Sprintf("d5-%d", i)
		if i < failed {
			out = append(out, Outcome{
				Check: "value_check", DocumentID: doc, Field: "amount",
				Label: "not-asked", Failed: true, Reason: "vendor error",
				ProbabilityKind: KindNoul,
			})
			continue
		}
		out = append(out, Outcome{
			Check: "value_check", DocumentID: doc, Field: "amount",
			Label: "right", ProbabilityKind: KindNoul, Probability: jmNum("0.99"),
		})
	}
	return out
}

func jmAskedOutcomes(n int) []Outcome {
	out := make([]Outcome, n)
	for i := range out {
		out[i] = Outcome{
			Check: "value_check", DocumentID: fmt.Sprintf("na-%d", i), Field: "amount",
			Label: "right", ProbabilityKind: KindNoul, Probability: jmNum("0.99"),
		}
	}
	return out
}

// AC-4. D4's own table: every row differs from every other; per-100 uses the
// 3 documents, not the 9 questions. The header leg pins AC-4's column order.
func TestReport_ThresholdSweepCountsBothErrorKinds(t *testing.T) {
	md, _, err := Render(d4Outcomes(), Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	header := "| threshold | right flagged | right flagged per 100 documents | wrong missed | wrong caught |"
	if !strings.Contains(body, header) {
		t.Errorf("sweep header is not AC-4's columns in AC-4's order; want %q", header)
	}

	rows := []struct {
		threshold, rightFlagged, per100, wrongCaught, wrongMissed string
	}{
		{"0.30", "1", "33.33", "1", "3"},
		{"0.50", "2", "66.67", "2", "2"},
		{"0.70", "3", "100.00", "3", "1"},
		{"0.90", "4", "133.33", "4", "0"},
	}
	for _, row := range rows {
		line := jmLineWithTokens(t, body, row.threshold, row.per100)
		scan := jmStripToken(line, row.threshold)
		for _, want := range []string{row.rightFlagged, row.per100, row.wrongCaught, row.wrongMissed} {
			if !jmHasNumber(scan, want) {
				t.Errorf("threshold %s row %q: want %s present", row.threshold, line, want)
			}
		}
	}
}

// AC-4. A right value at exactly 0.50 must flag (noul <= threshold, inclusive).
func TestReport_AThresholdBoundaryIsInclusive(t *testing.T) {
	outcomes := []Outcome{{
		Check: "value_check", DocumentID: "d1", Field: "amount",
		Label: "right", ProbabilityKind: KindNoul, Probability: jmNum("0.50"),
	}}
	md, _, err := Render(outcomes, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	if got := strings.Count(body, "noul <= threshold"); got != 1 {
		t.Errorf(`report shows "noul <= threshold" %d time(s), want exactly 1`, got)
	}

	notFlagged := jmLineWithTokens(t, body, "0.30", "0.00")
	if jmHasNumber(jmStripToken(notFlagged, "0.30"), "1") {
		t.Errorf("threshold 0.30 row %q: a right value of 0.50 must not flag (0.50 <= 0.30 is false)", notFlagged)
	}

	for _, thr := range []string{"0.50", "0.70", "0.90"} {
		line := jmLineWithTokens(t, body, thr, "100.00")
		if !jmHasNumber(jmStripToken(line, thr), "1") {
			t.Errorf("threshold %s row %q: a right value of exactly 0.50 must flag (boundary inclusive)", thr, line)
		}
	}
}

// AC-4, new A49. The value check and the document-type check sweep opposite
// directions; a single shared comparator would red one of these two legs.
func TestReport_TheTwoChecksSweepOppositeDirections(t *testing.T) {
	valueOut := []Outcome{{
		Check: "value_check", DocumentID: "d1", Field: "amount",
		Label: "right", ProbabilityKind: KindNoul, Probability: jmNum("0.20"),
	}}
	md1, _, err := Render(valueOut, Pricing{})
	if err != nil {
		t.Fatalf("Render (value): %v", err)
	}
	body1 := string(md1)
	if !strings.Contains(body1, "noul <= threshold") {
		t.Errorf("value check report never states its comparator literally")
	}
	line := jmLineWithTokens(t, body1, "0.30")
	if !jmHasNumber(jmStripToken(line, "0.30"), "1") {
		t.Errorf("threshold 0.30 row %q: a noul of 0.20 must flag (0.20 <= 0.30)", line)
	}

	typeOut := []Outcome{{
		Check: "document_type_check", DocumentID: "d1", Field: "receipt",
		Label: "right", ProbabilityKind: KindChoiceConfidence, Probability: jmNum("0.20"),
	}}
	md2, _, err := Render(typeOut, Pricing{})
	if err != nil {
		t.Fatalf("Render (type): %v", err)
	}
	body2 := string(md2)
	if !strings.Contains(body2, "confidence >= threshold") {
		t.Errorf("document-type report never states its comparator literally")
	}
	line2 := jmLineWithTokens(t, body2, "0.30")
	if jmHasNumber(jmStripToken(line2, "0.30"), "1") {
		t.Errorf("threshold 0.30 row %q: a confidence of 0.20 must not record (0.20 >= 0.30 is false)", line2)
	}
}

// AC-5. Vacuous as recorded (an empty report shows zero "%", trivially
// satisfying "every %% is preceded by a count"); the floor below fixes it.
func TestReport_StatesNBeforeAnyRate(t *testing.T) {
	var outcomes []Outcome
	for i := 0; i < 3; i++ {
		outcomes = append(outcomes, Outcome{
			Check: "value_check", DocumentID: fmt.Sprintf("na-%d", i), Field: "amount",
			Label: "not-asked",
		})
	}
	for i := 0; i < 7; i++ {
		outcomes = append(outcomes, Outcome{
			Check: "value_check", DocumentID: fmt.Sprintf("ok-%d", i), Field: "amount",
			Label: "right", ProbabilityKind: KindNoul, Probability: jmNum("0.99"),
		})
	}
	md, _, err := Render(outcomes, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	if pct := strings.Count(body, "%"); pct < 3 {
		t.Fatalf(`report shows %d "%%" occurrence(s), want at least 3 -- a report with none would pass "every %% has a count" vacuously`, pct)
	}
	digit := regexp.MustCompile(`[0-9]`)
	for _, line := range strings.Split(body, "\n") {
		idx := strings.Index(line, "%")
		for idx != -1 {
			if !digit.MatchString(line[:idx]) {
				t.Errorf("line %q: a %% at position %d has no count before it on the same line", line, idx)
			}
			rest := line[idx+1:]
			next := strings.Index(rest, "%")
			if next == -1 {
				break
			}
			idx += 1 + next
		}
	}
}

// AC-5. Vacuous as recorded (no quiet leg, so an always-warning impl passed);
// the 30-asked leg below fixes it.
func TestReport_ASmallRunSaysSoRatherThanQuotingARate(t *testing.T) {
	md29, _, err := Render(jmAskedOutcomes(29), Pricing{})
	if err != nil {
		t.Fatalf("Render (29): %v", err)
	}
	body29 := strings.ToLower(string(md29))
	if !strings.Contains(body29, "fewer than 30") {
		t.Errorf("29 asked questions must warn naming the small-N floor (fewer than 30)")
	}
	warning := jmLineWithTokens(t, body29, "fewer than 30")
	if !jmHasNumber(warning, "29") {
		t.Errorf("warning %q must name the actual asked count (29)", warning)
	}

	md30, _, err := Render(jmAskedOutcomes(30), Pricing{})
	if err != nil {
		t.Fatalf("Render (30): %v", err)
	}
	body30 := strings.ToLower(string(md30))
	if strings.Contains(body30, "fewer than 30") {
		t.Errorf("30 asked questions must not trip the small-N warning")
	}
}

// AC-6. D1's own table: nearest-rank p50/p90/max, differing from the wrong
// formulas that would otherwise pass (mean, avg-of-two median, linear-interp).
func TestReport_PercentilesAreNearestRankAndDifferFromEveryNeighbour(t *testing.T) {
	md, _, err := Render(d1Outcomes(), Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	for _, want := range []string{"18", "60", "500"} {
		if !jmHasNumber(body, want) {
			t.Errorf("report never shows %s (nearest-rank p50/p90/max)", want)
		}
	}
	// 10, the min, is skipped: it legitimately equals both a raw data point
	// and n, so its absence can't be asserted without a false failure.
	for _, wrong := range []string{"72", "19", "104"} {
		if jmHasNumber(body, wrong) {
			t.Errorf("report shows %s -- a wrong percentile formula, not nearest-rank", wrong)
		}
	}
}

// AC-6. The budget line names the failure-inclusive series in words; D2's
// all-attempts triple (18, 2500, 3000) differs from the successful-only one.
func TestJevReport_TheBudgetLineNamesTheFailureInclusiveSeries(t *testing.T) {
	md, _, err := Render(d2Outcomes(), Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	budget := jmLineWithTokens(t, body, "budget")
	if !strings.Contains(budget, "all attempts") {
		t.Errorf(`budget line %q never names the "all attempts" series in words`, budget)
	}
	for _, want := range []string{"18", "2500", "3000"} {
		if !jmHasNumber(body, want) {
			t.Errorf("all-attempts series never shows %s (p50/p90/max)", want)
		}
	}
}

// AC-6/7. A failed call's elapsed time is excluded from the successful-only
// series; D2's successful-only triple (16, 40, 40) differs from all-attempts.
func TestJevReport_ATimedOutCallContributesItsElapsedTime(t *testing.T) {
	md, _, err := Render(d2Outcomes(), Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	for _, want := range []string{"16", "40"} {
		if !jmHasNumber(body, want) {
			t.Errorf("successful-only series never shows %s (p50/p90=max)", want)
		}
	}
}

// AC-8. D5's real strict->10% boundary: 2/20 (10%) is quiet, 3/20 (15%) fires
// naming the counts.
func TestJevReport_ADegradedRunTripsAtMoreThanOneInTen(t *testing.T) {
	quiet, _, err := Render(d5Outcomes(2), Pricing{})
	if err != nil {
		t.Fatalf("Render (quiet): %v", err)
	}
	if strings.Contains(string(quiet), "2 of 20") || strings.Contains(string(quiet), "DEGRADED") {
		t.Errorf("2 failed of 20 attempted is exactly one in ten, not more -- must not trip the degraded warning")
	}

	loud, _, err := Render(d5Outcomes(3), Pricing{})
	if err != nil {
		t.Fatalf("Render (loud): %v", err)
	}
	if !strings.Contains(string(loud), "3 of 20") || !strings.Contains(string(loud), "DEGRADED") {
		t.Errorf("3 failed of 20 attempted must trip the degraded warning naming the counts")
	}
}

// AC-9. D3's corrected arithmetic: mean 300, p90 1100, cost per 1,000
// documents 0.72, discriminating against every wrong denominator/leak listed
// in the plan (1.595, 0.80, 1.1815, 0.0144, 0.54, 0.00072).
func TestReport_UsagePresentComputesMeanP90AndCostPerThousand(t *testing.T) {
	md, _, err := Render(d3Outcomes(), Pricing{2.00, 10.00})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	for _, want := range []string{"300", "1100", "0.72"} {
		if !jmHasNumber(body, want) {
			t.Errorf("report never shows %s (mean/p90/cost per 1,000 documents)", want)
		}
	}
	cost := jmLineWithTokens(t, body, "cost per 1,000 documents")
	low := strings.ToLower(cost)
	if !strings.Contains(low, "operator-supplied") && !strings.Contains(low, "operator supplied") {
		t.Errorf("cost line %q never labels the price as operator-supplied", cost)
	}
	for _, want := range []string{"$2.00", "$10.00"} {
		if !strings.Contains(cost, want) {
			t.Errorf("cost line %q must print the price it used (%s)", cost, want)
		}
	}
}

// AC-9. With usage present and a price supplied, neither absent-sentence may print.
func TestReport_UsagePresentNeverPrintsAnAbsentSentence(t *testing.T) {
	md, _, err := Render(d3Outcomes(), Pricing{2.00, 10.00})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)
	if strings.Contains(body, "not reported on") {
		t.Errorf("usage is present for every production call; nothing should read as absent")
	}
	if strings.Contains(body, "price not supplied") {
		t.Errorf("a price was supplied; the report must not say otherwise")
	}
}

// AC-9, new A50. No price -> no cost figure, but the token lines still print.
func TestReport_NoPriceMeansNoCostNumber(t *testing.T) {
	md, _, err := Render(d3Outcomes(), Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)
	if !strings.Contains(body, "price not supplied — cost not computed") {
		t.Errorf("report never reads %q with no price supplied", "price not supplied — cost not computed")
	}
	if !jmHasNumber(body, "300") {
		t.Errorf("token lines must still print without a price")
	}
	if strings.Contains(body, "$") {
		t.Errorf("no cost figure should print without a price")
	}
}

// AC-10, D-11. An absent input_tokens value names the field AND the count ("1 of 2 calls", not
// "some calls") and is excluded from the mean, not zeroed: (0+500)/2=250 would be the
// zero-defaulting bug; excluding the absent value gives 500.
func TestReport_AnAbsentUsageFieldIsNamedNotZeroed(t *testing.T) {
	outcomes := []Outcome{
		{Check: "value_check", DocumentID: "d1", Field: "amount", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.99"),
			Usage: Usage{InputTokens: nil, OutputTokens: jmNum("20")}},
		{Check: "value_check", DocumentID: "d2", Field: "amount", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.99"),
			Usage: Usage{InputTokens: jmNum("500"), OutputTokens: jmNum("20")}},
	}
	md, _, err := Render(outcomes, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	if !strings.Contains(body, "usage: input_tokens not reported on 1 of 2 calls") {
		t.Errorf("an absent input_tokens value must name the field and the count (1 of 2 calls); got body %q", body)
	}
	if !jmHasNumber(body, "500") {
		t.Errorf("mean input tokens must be 500 -- the one reported value, absence excluded")
	}
	if jmHasNumber(body, "250") {
		t.Errorf("mean input tokens shows 250 -- a zero-defaulting bug, not an excluded absence")
	}
}

// A30's eight document-type options, in A30's fixed order.
var jmA30Options = []string{
	"tax invoice", "receipt", "proforma", "quotation",
	"credit note", "delivery note", "statement", "purchase order",
}

// AC-11. Registry-driven: the report renders from WordingRegistry() itself,
// so a constant added without being wired into Render fails here rather than
// escaping unnoticed by a hand-written list.
func TestReport_QuotesEveryWordingRegistryEntryVerbatim(t *testing.T) {
	reg := WordingRegistry()
	if len(reg) < 15 {
		t.Fatalf("WordingRegistry() has %d entr(ies), want at least 15 (7 constants + 8 criteria)", len(reg))
	}

	outcomes := []Outcome{
		{Check: "value_check", DocumentID: "d1", Field: "amount", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.90")},
		{Check: "document_type_check", DocumentID: "d1", Field: "receipt", Label: "right",
			ProbabilityKind: KindChoiceConfidence, Probability: jmNum("0.90")},
	}
	md, _, err := Render(outcomes, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)
	for key, want := range reg {
		if !strings.Contains(body, want) {
			t.Errorf("wording %q is not quoted verbatim in the report", key)
		}
	}

	if len(DocumentTypeCriteria) != len(jmA30Options) {
		t.Fatalf("DocumentTypeCriteria has %d entr(ies), want %d", len(DocumentTypeCriteria), len(jmA30Options))
	}
	last := -1
	for _, opt := range jmA30Options {
		desc, ok := DocumentTypeCriteria[opt]
		if !ok {
			t.Fatalf("DocumentTypeCriteria has no entry for %q", opt)
		}
		idx := strings.Index(body, desc)
		if idx == -1 {
			t.Fatalf("criteria for %q is not quoted in the report", opt)
		}
		if idx <= last {
			t.Errorf("%q's criteria appears out of A30 order (index %d, want > %d)", opt, idx, last)
		}
		last = idx
	}
}

// AC-12, new A49. A noul answer has no confidence, so the value section's
// table must never mention it; the document-type render is the control leg
// that stops an empty/broken render from passing the first half vacuously.
func TestReport_TheValueSectionHasNoConfidenceColumn(t *testing.T) {
	valueOnly := []Outcome{{
		Check: "value_check", DocumentID: "d1", Field: "amount",
		Label: "right", ProbabilityKind: KindNoul, Probability: jmNum("0.90"),
	}}
	mdValue, _, err := Render(valueOnly, Pricing{})
	if err != nil {
		t.Fatalf("Render (value): %v", err)
	}
	if strings.Contains(strings.ToLower(string(mdValue)), "confidence") {
		t.Errorf("value-only report mentions confidence; a noul answer has none")
	}

	typeOnly := []Outcome{{
		Check: "document_type_check", DocumentID: "d1", Field: "receipt",
		Label: "right", ProbabilityKind: KindChoiceConfidence, Probability: jmNum("0.90"),
	}}
	mdType, _, err := Render(typeOnly, Pricing{})
	if err != nil {
		t.Fatalf("Render (type): %v", err)
	}
	if !strings.Contains(strings.ToLower(string(mdType)), "confidence") {
		t.Errorf("document-type report never mentions confidence -- control leg: an empty render would also pass the value-only check above")
	}
}

// jmRankLatencies -- the integer percentile path. 11 latencies, so q*n is
// fractional at both p50 (5.5) and p90 (9.9) and the floor neighbours (505,
// 909) appear in no other figure the report prints.
func jmRankLatencies() []Outcome {
	ms := []int{101, 202, 303, 404, 505, 606, 707, 808, 909, 1010, 1111}
	out := make([]Outcome, len(ms))
	for i, m := range ms {
		out[i] = Outcome{
			Check: "value_check", DocumentID: fmt.Sprintf("rank-%d", i), Field: "amount",
			Label: "right", ProbabilityKind: KindNoul, Probability: jmNum("0.99"),
			Elapsed: time.Duration(m) * time.Millisecond,
		}
	}
	return out
}

// jmRankTokens -- the float percentile path. 11 input-token counts; p90's
// rank is 9.9, so its floor neighbour (999) differs from its ceil one (1110).
func jmRankTokens() []Outcome {
	input := []string{"111", "222", "333", "444", "555", "666", "777", "888", "999", "1110", "1221"}
	out := make([]Outcome, len(input))
	for i, v := range input {
		out[i] = Outcome{
			Check: "value_check", DocumentID: fmt.Sprintf("tok-%d", i), Field: "amount",
			Label: "right", ProbabilityKind: KindNoul, Probability: jmNum("0.99"),
			Usage: Usage{InputTokens: jmNum(v), OutputTokens: jmNum("20")},
		}
	}
	return out
}

// AC-6. nearestRank rounds the rank UP. D1/D2 could not see this: three of
// their four q*n products are whole numbers, and the fourth's floor neighbour
// equals the max they already accept.
func TestReport_TheLatencyPercentileRoundsTheRankUp(t *testing.T) {
	md, _, err := Render(jmRankLatencies(), Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	for _, want := range []string{"606", "1010", "1111"} {
		if !jmHasNumber(body, want) {
			t.Errorf("latency series never shows %s (ceil-rank p50/p90/max over 11 values)", want)
		}
	}
	for _, wrong := range []string{"505", "909"} {
		if jmHasNumber(body, wrong) {
			t.Errorf("latency series shows %s -- the floor-rank neighbour, not nearest-rank ceil", wrong)
		}
	}
}

// AC-9. The same ceil-rank rule on the token path, which runs through a
// separate float64 helper and so needs its own dataset.
func TestReport_TheTokenPercentileRoundsTheRankUp(t *testing.T) {
	md, _, err := Render(jmRankTokens(), Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	tokens := jmLineWithTokens(t, body, "input tokens per call")
	if !jmHasNumber(tokens, "1110") {
		t.Errorf("token line %q: p90 over 11 values is the 10th (1110), the ceil of rank 9.9", tokens)
	}
	if jmHasNumber(tokens, "999") {
		t.Errorf("token line %q: 999 is the floor-rank neighbour, not nearest-rank ceil", tokens)
	}
	if !jmHasNumber(tokens, "666") {
		t.Errorf("token line %q: mean over 11 values is 666", tokens)
	}
}

// AC-4. The document-type comparator is inclusive at the boundary too; only
// the noul side of A51 was pinned before.
func TestReport_AConfidenceThresholdBoundaryIsInclusive(t *testing.T) {
	outcomes := []Outcome{{
		Check: "document_type_check", DocumentID: "d1", Field: "receipt",
		Label: "right", ProbabilityKind: KindChoiceConfidence, Probability: jmNum("0.50"),
	}}
	md, _, err := Render(outcomes, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	for _, thr := range []string{"0.30", "0.50"} {
		line := jmLineWithTokens(t, body, thr, "100.00")
		if !jmHasNumber(jmStripToken(line, thr), "1") {
			t.Errorf("threshold %s row %q: a confidence of exactly 0.50 must record (boundary inclusive)", thr, line)
		}
	}
	for _, thr := range []string{"0.70", "0.90"} {
		line := jmLineWithTokens(t, body, thr, "0.00")
		if jmHasNumber(jmStripToken(line, thr), "1") {
			t.Errorf("threshold %s row %q: a confidence of 0.50 must not record (0.50 >= %s is false)", thr, line, thr)
		}
	}
}

// AC-6/7. A question never asked burned no call, so its row must not enter
// either latency series -- including it would deflate every percentile.
func TestReport_AQuestionNeverAskedBurnsNoLatency(t *testing.T) {
	outcomes := []Outcome{{
		Check: "value_check", DocumentID: "d1", Field: "amount",
		Label: "right", ProbabilityKind: KindNoul, Probability: jmNum("0.99"),
		Elapsed: 700 * time.Millisecond,
	}}
	for i := 0; i < 3; i++ {
		outcomes = append(outcomes, Outcome{
			Check: "value_check", DocumentID: fmt.Sprintf("na-%d", i), Field: "amount",
			Label: "not-asked",
		})
	}
	md, _, err := Render(outcomes, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	for _, series := range []string{"all attempts", "successful only"} {
		line := jmLineWithTokens(t, body, series, "p50")
		if !strings.Contains(line, "p50 700") {
			t.Errorf("%s line %q: p50 must be 700 -- three never-asked rows must not add three zeros", series, line)
		}
	}
}

// AC-5. The N line names all three counts; nothing pinned the documents count
// or the not-asked count before.
func TestReport_TheNLineNamesDocumentsAskedAndNotAsked(t *testing.T) {
	var outcomes []Outcome
	for i := 0; i < 7; i++ {
		outcomes = append(outcomes, Outcome{
			Check: "value_check", DocumentID: fmt.Sprintf("ok-%d", i), Field: "amount",
			Label: "right", ProbabilityKind: KindNoul, Probability: jmNum("0.99"),
		})
	}
	for i := 0; i < 5; i++ {
		outcomes = append(outcomes, Outcome{
			Check: "value_check", DocumentID: fmt.Sprintf("na-%d", i), Field: "amount",
			Label: "not-asked",
		})
	}
	md, _, err := Render(outcomes, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	line := jmLineWithTokens(t, string(md), "documents:")
	for _, want := range []string{"documents: 12", "questions asked: 7", "questions not asked: 5"} {
		if !strings.Contains(line, want) {
			t.Errorf("N line %q must state %q", line, want)
		}
	}
}

// AC-5. Each check counts its own documents; a document answered by two
// checks is one document in each section, not two in either.
func TestReport_EachCheckCountsItsOwnDocuments(t *testing.T) {
	// "shared" answers twice in value_check, so a documents count that is
	// really a row count reads 3 here instead of 2.
	outcomes := []Outcome{
		{Check: "value_check", DocumentID: "shared", Field: "amount", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.99")},
		{Check: "value_check", DocumentID: "shared", Field: "buyer_tin", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.99")},
		{Check: "value_check", DocumentID: "value-only", Field: "amount", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.99")},
		{Check: "document_type_check", DocumentID: "shared", Field: "receipt", Label: "right",
			ProbabilityKind: KindChoiceConfidence, Probability: jmNum("0.99")},
	}
	md, _, err := Render(outcomes, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	valueLine := jmLineWithTokens(t, jmSection(t, body, "value_check"), "documents:")
	if !strings.Contains(valueLine, "documents: 2") {
		t.Errorf("value_check N line %q: two distinct document ids over three rows, want documents: 2", valueLine)
	}
	typeLine := jmLineWithTokens(t, jmSection(t, body, "document_type_check"), "documents:")
	if !strings.Contains(typeLine, "documents: 1") {
		t.Errorf("document_type_check N line %q: one document id, want documents: 1", typeLine)
	}
}

// AC-10. An absent output_tokens must be named too; only the input side was
// pinned, so dropping the output half of the absence test left no red.
func TestReport_AnAbsentOutputTokenCountIsNamedToo(t *testing.T) {
	outcomes := []Outcome{{
		Check: "value_check", DocumentID: "d1", Field: "amount", Label: "right",
		ProbabilityKind: KindNoul, Probability: jmNum("0.99"),
		Usage: Usage{InputTokens: jmNum("500"), OutputTokens: nil},
	}}
	md, _, err := Render(outcomes, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	if !strings.Contains(body, "usage: output_tokens not reported on 1 of 1 calls") {
		t.Errorf("an absent output_tokens value must name the field and the count (1 of 1 calls); got body %q", body)
	}
	if !jmHasNumber(body, "500") {
		t.Errorf("the reported input count must still print alongside the absence")
	}
}

// Render's second return value is the JSON twin a re-run is diffed against;
// nothing asserted it existed before.
func TestReport_TheJSONTwinCarriesTheSameCounts(t *testing.T) {
	outcomes := []Outcome{
		{Check: "value_check", DocumentID: "d1", Field: "amount", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.99")},
		{Check: "value_check", DocumentID: "d2", Field: "amount", Label: "not-asked"},
	}
	_, twin, err := Render(outcomes, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	var sections []struct {
		Check      string
		Documents  int
		Asked      int
		NotAsked   int
		Thresholds []struct {
			Threshold    float64
			RightFlagged int
		}
	}
	if err := json.Unmarshal(twin, &sections); err != nil {
		t.Fatalf("JSON twin does not parse: %v", err)
	}
	if len(sections) != 1 {
		t.Fatalf("JSON twin has %d section(s), want 1", len(sections))
	}
	got := sections[0]
	if got.Check != "value_check" || got.Documents != 2 || got.Asked != 1 || got.NotAsked != 1 {
		t.Errorf("JSON twin section = %+v, want value_check with 2 documents, 1 asked, 1 not asked", got)
	}
	if len(got.Thresholds) != len(thresholds) {
		t.Errorf("JSON twin carries %d threshold row(s), want %d", len(got.Thresholds), len(thresholds))
	}
}

// --- CHECK-01-04: D-4's ruling, the variant split, the per-call fold, and the reason tally ---

// C-2, D-4. A failed row is NOT-ASKED: it cannot enter a denominator of labelled answers, so
// the N line's three percentages must read against that ruling.
func TestJevReport_AFailedRowDoesNotEnterTheAskedCount(t *testing.T) {
	var outcomes []Outcome
	for i := 0; i < 4; i++ {
		outcomes = append(outcomes, Outcome{
			Check: "value_check", DocumentID: fmt.Sprintf("ok-%d", i), Field: "amount",
			Label: "right", ProbabilityKind: KindNoul, Probability: jmNum("0.99"),
		})
	}
	outcomes = append(outcomes, Outcome{
		Check: "value_check", DocumentID: "fail-0", Field: "amount",
		Label: "not-asked", Failed: true, Reason: "vendor error", ProbabilityKind: KindNoul,
	})
	md, _, err := Render(outcomes, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	line := jmLineWithTokens(t, string(md), "documents:")
	for _, want := range []string{"questions asked: 4 (80.00%)", "questions not asked: 1 (20.00%)", "questions failed: 1 (20.00%)"} {
		if !strings.Contains(line, want) {
			t.Errorf("N line %q must state %q", line, want)
		}
	}
	if strings.Contains(line, "asked: 5") {
		t.Errorf("N line %q reads asked: 5 -- a failed row must not enter the asked count", line)
	}
	// D-4: the ruling is stated in the rendered report, not only in code.
	const ruling = "a failed question counts as not asked"
	if !strings.Contains(string(md), ruling) {
		t.Errorf("report never states %q; a reader cannot tell which denominator the percentages use", ruling)
	}
}

// C-1. The variant marker (A55/AC-11) separates the planted-variant cost from the
// production-shaped one; dropping it collapses both into the all-calls figure.
func TestJevReport_TheVariantCostIsSeparateFromTheProductionCost(t *testing.T) {
	outcomes := d3Outcomes()
	for i := 0; i < 7; i++ {
		outcomes = append(outcomes, Outcome{
			Check: "value_check", DocumentID: fmt.Sprintf("d3-%d", i), Field: "variant_field",
			Label: "wrong", Variant: true, ProbabilityKind: KindNoul, Probability: jmNum("0.99"),
			Usage: Usage{InputTokens: jmNum("1000"), OutputTokens: jmNum("20")},
		})
	}
	md, _, err := Render(outcomes, Pricing{2.00, 10.00})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	prod := jmLineWithTokens(t, body, "cost per 1,000 documents", "production")
	if !jmHasNumber(prod, "0.72") {
		t.Errorf("production cost line %q must read 0.72, unmoved by the variant rows", prod)
	}
	all := jmLineWithTokens(t, body, "cost per 1,000 documents", "all calls")
	if !jmHasNumber(all, "1.49") {
		t.Errorf("all-calls cost line %q must read 1.49 (production + planted variants)", all)
	}
	if prod == all {
		t.Fatalf("production and all-calls cost lines are the same line; Variant produced no split")
	}
}

// C-1. A variant call burns wall clock but must never enter the budget series: its elapsed
// time is synthetic-corruption overhead, not a production-shaped attempt.
func TestJevReport_AVariantCallBurnsNoBudgetLatency(t *testing.T) {
	var outcomes []Outcome
	for i := 0; i < 5; i++ {
		outcomes = append(outcomes, Outcome{
			Check: "value_check", DocumentID: fmt.Sprintf("prod-%d", i), Field: "amount",
			Label: "right", ProbabilityKind: KindNoul, Probability: jmNum("0.99"),
			Elapsed: 100 * time.Millisecond,
		})
	}
	for i := 0; i < 3; i++ {
		outcomes = append(outcomes, Outcome{
			Check: "value_check", DocumentID: fmt.Sprintf("var-%d", i), Field: "amount",
			Label: "wrong", Variant: true, ProbabilityKind: KindNoul, Probability: jmNum("0.99"),
			Elapsed: 5000 * time.Millisecond,
		})
	}
	md, _, err := Render(outcomes, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	for _, series := range []string{"all attempts", "successful only"} {
		line := jmLineWithTokens(t, body, series, "p50")
		for _, want := range []string{"p50 100", "p90 100", "max 100"} {
			if !strings.Contains(line, want) {
				t.Errorf("%s line %q must read %q -- a variant row must not enter this series", series, line, want)
			}
		}
	}
	if jmHasNumber(body, "5000") {
		t.Errorf("report shows 5000 -- a variant call's elapsed time must burn no budget latency")
	}
}

// R-5. usageStats/latencyOverElapsed fold per outcome ROW today; several rows sharing one
// CallID must fold to one call, or the cost figure inflates by the questions-per-call factor.
func TestJevReport_RowsSharingOneCallCountItsUsageOnce(t *testing.T) {
	var outcomes []Outcome
	for i := 0; i < 6; i++ {
		outcomes = append(outcomes, Outcome{
			Check: "value_check", DocumentID: "d1", Field: fmt.Sprintf("f%d", i),
			Label: "right", ProbabilityKind: KindNoul, Probability: jmNum("0.99"),
			Usage: Usage{InputTokens: jmNum("600"), OutputTokens: jmNum("20")}, CallID: "doc1#production",
		})
	}
	// Empty CallID is its own call -- the fallback every CHECK-01-02 fixture (none of which
	// set CallID) relies on.
	outcomes = append(outcomes, Outcome{
		Check: "value_check", DocumentID: "d2", Field: "amount",
		Label: "right", ProbabilityKind: KindNoul, Probability: jmNum("0.99"),
		Usage: Usage{InputTokens: jmNum("600"), OutputTokens: jmNum("20")},
	})

	md, _, err := Render(outcomes, Pricing{1.00, 0})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	tokens := jmLineWithTokens(t, body, "input tokens per call")
	if !jmHasNumber(tokens, "600") {
		t.Errorf("token line %q: mean input tokens must be 600", tokens)
	}
	cost := jmLineWithTokens(t, body, "cost per 1,000 documents")
	if !jmHasNumber(cost, "0.60") {
		t.Errorf("cost line %q: want 0.60 -- 1,200 input tokens over 2 calls, not 4,200 over 7 rows", cost)
	}
	if jmHasNumber(body, "2.10") {
		t.Errorf("report shows 2.10 -- the unfolded cost over 7 rows, not the per-call fold")
	}
}

// AC-4. "never silently dropped" extends to the reason tally: every not-asked row's Reason
// must be named and counted, and the counts must sum to the printed not-asked figure.
func TestJevReport_TheNotAskedReasonsAreTallied(t *testing.T) {
	counts := map[string]int{
		"missing": 2, "ambiguous": 3, "no answer key": 1, "variant not plantable": 4,
	}
	var outcomes []Outcome
	i := 0
	for reason, n := range counts {
		for j := 0; j < n; j++ {
			outcomes = append(outcomes, Outcome{
				Check: "value_check", DocumentID: fmt.Sprintf("na-%d", i), Field: "amount",
				Label: "not-asked", Reason: reason,
			})
			i++
		}
	}
	md, _, err := Render(outcomes, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	line := jmLineWithTokens(t, body, "not asked, by reason:")
	sum := 0
	for reason, n := range counts {
		want := fmt.Sprintf("%s %d", reason, n)
		if !strings.Contains(line, want) {
			t.Errorf("reason line %q missing %q", line, want)
		}
		sum += n
	}
	notAsked := jmLineWithTokens(t, body, "questions not asked:")
	if !strings.Contains(notAsked, fmt.Sprintf("questions not asked: %d", sum)) {
		t.Errorf("N line %q: not-asked count must equal the reason tally's sum (%d)", notAsked, sum)
	}
}

// --- CHECK-01-07: three checks in one report, the merge ledger, the caveat registry ---

// Row 8/AC-6. Four sections (two mapping halves + value + document-type), each with its own N
// and its own 4-row threshold table; the coverage line names all four with no partial warning.
func TestJevReport_CoversAllThreeChecks(t *testing.T) {
	outcomes := []Outcome{
		{Check: "value_check", DocumentID: "d1", Field: "amount", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.90")},
		{Check: "document_type_check", DocumentID: "d1", Field: "receipt", Label: "right",
			ProbabilityKind: KindChoiceConfidence, Probability: jmNum("0.90")},
		{Check: "mapping_check_auto", DocumentID: "sw_zoho", Field: "total", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.90")},
		{Check: "mapping_check_ai", DocumentID: "sw_zoho", Field: "total", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.90")},
	}
	md, _, err := Render(outcomes, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	headings := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "## ") && line != "## Provenance" && line != "## Wording" {
			headings++
		}
	}
	if headings != 4 {
		t.Fatalf("report has %d check heading(s), want exactly 4", headings)
	}
	for _, check := range checkOrder {
		sec := jmSection(t, body, check)
		if !strings.Contains(sec, "documents: 1") {
			t.Errorf("%s section has no own documents: line", check)
		}
		rows := 0
		for _, line := range strings.Split(sec, "\n") {
			if strings.HasPrefix(line, "| 0.") {
				rows++
			}
		}
		if rows != 4 {
			t.Errorf("%s section has %d threshold row(s), want 4:\n%s", check, rows, sec)
		}
	}

	want := "checks covered by this artifact: " + strings.Join(checkOrder, ", ")
	if !strings.Contains(body, want) {
		t.Errorf("coverage line %q not found in order %v", want, checkOrder)
	}
	if strings.Contains(body, "has not been measured into this artifact yet") {
		t.Errorf("partial warning must be absent when all four checks are present")
	}
}

// Row 20/§4.4.4. Fewer than four checks: the coverage line names only what is present, and the
// partial warning fires -- a partial artifact must read as visibly partial.
func TestJevReport_APartialArtifactNamesAndWarnsAboutMissingChecks(t *testing.T) {
	outcomes := []Outcome{
		{Check: "value_check", DocumentID: "d1", Field: "amount", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.90")},
		{Check: "mapping_check_auto", DocumentID: "sw_zoho", Field: "total", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.90")},
	}
	md, _, err := Render(outcomes, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	if !strings.Contains(body, "checks covered by this artifact: value_check, mapping_check_auto") {
		t.Errorf("coverage line does not name exactly the two present checks; body:\n%s", body)
	}
	if !strings.Contains(body, "has not been measured into this artifact yet") {
		t.Errorf("a 2-of-4 artifact must warn that it is partial")
	}
}

// Row 9/AC-6. A probability-free section renders no threshold table, names the RIGHT missing
// word for its own comparator kind, and still renders its confusion table; the value-only
// control leg is what stops a single hard-coded message string reding
// TestReport_TheValueSectionHasNoConfidenceColumn; the third leg controls that a section WITH
// probabilities is unaffected.
func TestJevReport_ACheckWithNoProbabilityRendersNoThresholdTable(t *testing.T) {
	typeOut := []Outcome{
		{Check: "document_type_check", DocumentID: "d1", Field: "receipt", Answer: "receipt",
			Label: "right", ProbabilityKind: KindChoiceConfidence},
	}
	md, _, err := Render(typeOut, Pricing{})
	if err != nil {
		t.Fatalf("Render (type): %v", err)
	}
	sec := jmSection(t, string(md), "document_type_check")
	if !strings.Contains(sec, "no confidence returned by the vendor — no threshold table") {
		t.Errorf("section never states the no-confidence line:\n%s", sec)
	}
	for _, want := range []string{"| 0.30 |", "| 0.50 |", "| 0.70 |", "| 0.90 |", "comparator:"} {
		if strings.Contains(sec, want) {
			t.Errorf("probability-free section must not contain %q:\n%s", want, sec)
		}
	}
	if !strings.Contains(sec, "confusion table") {
		t.Errorf("section must still render the confusion table")
	}

	valueOut := []Outcome{
		{Check: "value_check", DocumentID: "d1", Field: "amount", Label: "right", ProbabilityKind: KindNoul},
	}
	md2, _, err := Render(valueOut, Pricing{})
	if err != nil {
		t.Fatalf("Render (value): %v", err)
	}
	body2 := string(md2)
	if !strings.Contains(body2, "no probability returned by the vendor — no threshold table") {
		t.Errorf("value-only probability-free section must state the no-probability line")
	}
	if strings.Contains(strings.ToLower(body2), "confidence") {
		t.Errorf("value-only report must never mention confidence")
	}

	md3, _, err := Render(d1Outcomes(), Pricing{})
	if err != nil {
		t.Fatalf("Render (control): %v", err)
	}
	body3 := string(md3)
	for _, want := range []string{"| 0.30 |", "| 0.50 |", "| 0.70 |", "| 0.90 |"} {
		if !strings.Contains(body3, want) {
			t.Errorf("a section with probabilities must still render %q", want)
		}
	}
}

// Row 10/AC-7. Reader is named per (check, document): the fixture pairs docling with two
// documents (one repeated across two fields, so dedup is exercised), pdfium with one, and the
// mapping reader with one; a document with no Reader produces no row.
func TestJevReport_NamesTheReaderPerDocument(t *testing.T) {
	outcomes := []Outcome{
		{Check: "value_check", DocumentID: "docling-1", Field: "amount", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.90"), Reader: "docling"},
		{Check: "value_check", DocumentID: "docling-1", Field: "total", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.90"), Reader: "docling"},
		{Check: "value_check", DocumentID: "docling-2", Field: "amount", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.90"), Reader: "docling"},
		{Check: "value_check", DocumentID: "pdfium-1", Field: "amount", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.90"), Reader: "pdfium"},
		{Check: "mapping_check_auto", DocumentID: "sw_zoho", Field: "total", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.90"), Reader: "importer.Decode (csv)"},
		{Check: "value_check", DocumentID: "no-reader-1", Field: "amount", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.90")},
	}
	md, _, err := Render(outcomes, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	prov := jmSection(t, string(md), "Provenance")

	for _, want := range []string{"docling", "pdfium", "importer.Decode (csv)", "docling-1", "docling-2", "pdfium-1", "sw_zoho"} {
		if !strings.Contains(prov, want) {
			t.Errorf("Provenance section missing %q:\n%s", want, prov)
		}
	}
	if strings.Contains(prov, "no-reader-1") {
		t.Errorf("a document with an empty Reader must produce no row")
	}
	rows := strings.Count(prov, "| value_check |") + strings.Count(prov, "| mapping_check_auto |")
	if rows != 4 {
		t.Errorf("Provenance carries %d reader row(s), want 4 (docling-1 dedup, docling-2, pdfium-1, sw_zoho):\n%s", rows, prov)
	}

	valueOnly := []Outcome{{Check: "value_check", DocumentID: "d1", Field: "amount", Label: "right",
		ProbabilityKind: KindNoul, Probability: jmNum("0.90"), Reader: "docling"}}
	mdVal, _, err := Render(valueOnly, Pricing{})
	if err != nil {
		t.Fatalf("Render (value-only): %v", err)
	}
	if strings.Contains(strings.ToLower(string(mdVal)), "confidence") {
		t.Errorf("a value-only render with readers set must still never mention confidence")
	}
}

// Row 20/D-1. MergeOutcomes is order-independent: whichever binary runs last, the merged
// ledger's check order is governed by checkOrder, not by arrival order.
func TestMergeOutcomes_IsOrderIndependentOfWhichBinaryRunsFirst(t *testing.T) {
	valueRows := []Outcome{{Check: "value_check", DocumentID: "v1", Field: "amount", Label: "right"}}
	mappingRows := []Outcome{{Check: "mapping_check_auto", DocumentID: "sw_zoho", Field: "total", Label: "right"}}

	ledgerA := MergeOutcomes(nil, valueRows)
	ledgerA = MergeOutcomes(ledgerA, mappingRows)

	ledgerB := MergeOutcomes(nil, mappingRows)
	ledgerB = MergeOutcomes(ledgerB, valueRows)

	if !reflect.DeepEqual(ledgerA, ledgerB) {
		t.Errorf("MergeOutcomes order depends on which binary ran first:\nA = %+v\nB = %+v", ledgerA, ledgerB)
	}
	if len(ledgerA) != 2 || ledgerA[0].Check != "value_check" || ledgerA[1].Check != "mapping_check_auto" {
		t.Errorf("merged ledger = %+v, want [value_check, mapping_check_auto] in checkOrder", ledgerA)
	}

	// Purity (D-1): MergeOutcomes must not reorder or overwrite either argument in place --
	// the ledger's prior rows are the other binary's, and a caller may still be holding them.
	priorIn := []Outcome{
		{Check: "mapping_check_ai", DocumentID: "m1", Field: "total", Label: "right"},
		{Check: "value_check", DocumentID: "v1", Field: "amount", Label: "right"},
	}
	freshIn := []Outcome{{Check: "mapping_check_auto", DocumentID: "a1", Field: "total", Label: "right"}}
	priorWant := append([]Outcome(nil), priorIn...)
	freshWant := append([]Outcome(nil), freshIn...)
	_ = MergeOutcomes(priorIn, freshIn)
	if !reflect.DeepEqual(priorIn, priorWant) {
		t.Errorf("MergeOutcomes mutated prior: got %+v, want %+v", priorIn, priorWant)
	}
	if !reflect.DeepEqual(freshIn, freshWant) {
		t.Errorf("MergeOutcomes mutated fresh: got %+v, want %+v", freshIn, freshWant)
	}
}

// Row 20. Re-running one check replaces its rows rather than appending a second copy; a check
// this merge never touches survives both runs untouched.
func TestMergeOutcomes_ReplacesRatherThanAppendsTheSameCheck(t *testing.T) {
	valueRows := []Outcome{{Check: "value_check", DocumentID: "v1", Field: "amount", Label: "right"}}
	first := []Outcome{{Check: "mapping_check_auto", DocumentID: "sw_zoho", Field: "total", Label: "right"}}
	second := []Outcome{
		{Check: "mapping_check_auto", DocumentID: "sw_zoho", Field: "total", Label: "right"},
		{Check: "mapping_check_auto", DocumentID: "mix_01", Field: "total", Label: "wrong"},
	}

	merged := MergeOutcomes(valueRows, first)
	merged = MergeOutcomes(merged, second)

	var mappingRows, keptValueRows int
	for _, o := range merged {
		switch o.Check {
		case "mapping_check_auto":
			mappingRows++
		case "value_check":
			keptValueRows++
		}
	}
	if mappingRows != 2 {
		t.Errorf("merged holds %d mapping_check_auto row(s), want 2 -- the second run's own count, not 1+2=3", mappingRows)
	}
	if keptValueRows != 1 {
		t.Errorf("merged holds %d value_check row(s), want 1 -- value_check must survive a mapping-only re-run untouched", keptValueRows)
	}
}

// Row 20. A stored-then-reloaded Outcome keeps its Probability, Elapsed, Usage and Reader; the
// Render leg is the consequence this guards -- a probability that did not survive the ledger
// would silently drop out of the threshold sweep on the next merge.
func TestOutcome_JSONRoundTripKeepsProbabilityElapsedUsageAndReader(t *testing.T) {
	o := Outcome{
		Check: "value_check", DocumentID: "d1", Field: "amount", Label: "right",
		ProbabilityKind: KindNoul, Probability: jmNum("0.375"),
		Elapsed: 1234 * time.Millisecond, Reader: "docling",
		Usage: Usage{InputTokens: jmNum("500"), OutputTokens: jmNum("20")},
	}
	b, err := json.Marshal([]Outcome{o})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back []Outcome
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back) != 1 {
		t.Fatalf("round trip produced %d outcome(s), want 1", len(back))
	}
	got := back[0]
	if got.Probability == nil || got.Probability.String() != "0.375" {
		t.Errorf("Probability did not round-trip: got %v, want 0.375", got.Probability)
	}
	if got.Elapsed != o.Elapsed {
		t.Errorf("Elapsed did not round-trip: got %v, want %v", got.Elapsed, o.Elapsed)
	}
	if got.Usage.InputTokens == nil || got.Usage.InputTokens.String() != "500" {
		t.Errorf("Usage.InputTokens did not round-trip: got %v, want 500", got.Usage.InputTokens)
	}
	if got.Reader != "docling" {
		t.Errorf("Reader did not round-trip: got %q, want docling", got.Reader)
	}

	md, _, err := Render(back, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(md), "comparator: noul <= threshold") {
		t.Errorf("a round-tripped Probability must still drive the threshold sweep")
	}
}

// Row 21. Every CaveatRegistry() entry renders when its gate is satisfied, and renders NOWHERE
// when it is not -- the negative leg is what stops a future caveat wording from reding
// TestReport_TheValueSectionHasNoConfidenceColumn. Placement and content legs follow.
func TestJevReport_EveryRequiredCaveatIsRendered(t *testing.T) {
	reg := CaveatRegistry()

	valueVariant := []Outcome{
		{Check: "value_check", DocumentID: "d1", Field: "amount", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.90")},
		{Check: "value_check", DocumentID: "d1", Field: "variant_x", Label: "wrong", Variant: true,
			ProbabilityKind: KindNoul, Probability: jmNum("0.90")},
	}
	valuePlain := []Outcome{
		{Check: "value_check", DocumentID: "d1", Field: "amount", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.90")},
	}
	doctypeAnswered := []Outcome{
		{Check: "document_type_check", DocumentID: "d1", Field: "receipt", Answer: "receipt",
			Label: "right", ProbabilityKind: KindChoiceConfidence, Probability: jmNum("0.90")},
	}
	doctypePlain := []Outcome{
		{Check: "document_type_check", DocumentID: "d1", Field: "receipt", Label: "not-asked", Reason: "no answer key"},
	}
	mappingAutoFewWrongs := []Outcome{
		{Check: "mapping_check_auto", DocumentID: "l1", Field: "total", Label: "right"},
		{Check: "mapping_check_auto", DocumentID: "l2", Field: "vat", Label: "wrong"},
		{Check: "mapping_check_auto", DocumentID: "l3", Field: "vat", Label: "wrong"},
		{Check: "mapping_check_auto", DocumentID: "l4", Field: "vat", Label: "wrong"},
	}
	var mappingAutoManyWrongs []Outcome
	for i := 0; i < 11; i++ {
		mappingAutoManyWrongs = append(mappingAutoManyWrongs, Outcome{
			Check: "mapping_check_auto", DocumentID: fmt.Sprintf("l%d", i), Field: "vat", Label: "wrong",
		})
	}
	mappingAI := []Outcome{{Check: "mapping_check_ai", DocumentID: "l1", Field: "total", Label: "right"}}
	bothMapping := append(append([]Outcome{}, mappingAutoFewWrongs...), mappingAI...)
	nonMapping := []Outcome{
		{Check: "value_check", DocumentID: "d1", Field: "amount", Label: "right",
			ProbabilityKind: KindNoul, Probability: jmNum("0.90")},
	}

	cases := []struct {
		id             string
		gated, ungated []Outcome
	}{
		{caveatDoctypeTitleAnnounced, doctypeAnswered, doctypePlain},
		{caveatValuePlantedWrongs, valueVariant, valuePlain},
		{caveatMappingDeclaredHeaderRow, mappingAutoFewWrongs, nonMapping},
		{caveatMappingAutoFewWrongs, mappingAutoFewWrongs, mappingAutoManyWrongs},
		{caveatMappingAcceptedList, mappingAutoFewWrongs, nonMapping},
		{caveatMappingTwoHalves, bothMapping, mappingAutoFewWrongs},
	}

	// Registry control: an entry added without a matching case here (plus wording.provisional,
	// handled separately below) reds this rather than escaping unnoticed.
	if len(reg) != len(cases)+1 {
		t.Fatalf("CaveatRegistry() has %d entr(ies), want %d (%d gated/ungated cases + wording.provisional)", len(reg), len(cases)+1, len(cases))
	}

	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			want, ok := reg[tc.id]
			if !ok {
				t.Fatalf("registry has no entry %q", tc.id)
			}

			mdGated, _, err := Render(tc.gated, Pricing{})
			if err != nil {
				t.Fatalf("Render (gated): %v", err)
			}
			if !strings.Contains(string(mdGated), want) {
				t.Errorf("gated render never quotes caveat %q verbatim", tc.id)
			}

			mdUngated, _, err := Render(tc.ungated, Pricing{})
			if err != nil {
				t.Fatalf("Render (ungated): %v", err)
			}
			if strings.Contains(string(mdUngated), want) {
				t.Errorf("ungated render quotes caveat %q; its gate is not gating", tc.id)
			}
		})
	}

	// wording.provisional: always, inside ## Wording, before the first ###.
	mdAny, _, err := Render(valuePlain, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	wordingCaveat := reg[caveatWordingProvisional]
	wordingSec := jmSection(t, string(mdAny), "Wording")
	idx := strings.Index(wordingSec, wordingCaveat)
	firstHeading := strings.Index(wordingSec, "### ")
	if idx == -1 {
		t.Fatalf("wording.provisional never renders inside ## Wording")
	}
	if firstHeading != -1 && idx > firstHeading {
		t.Errorf("wording.provisional must render before the first ### heading inside ## Wording")
	}
	if !strings.Contains(wordingCaveat, "Provisional") {
		t.Errorf("wording.provisional must contain %q", "Provisional")
	}
	if !strings.Contains(wordingCaveat, "the live run") {
		t.Errorf("wording.provisional must point at the live run")
	}

	// Placement leg: doctype.title_announced above the confusion table.
	mdDoctype, _, err := Render(doctypeAnswered, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	doctypeSec := jmSection(t, string(mdDoctype), "document_type_check")
	caveatIdx := strings.Index(doctypeSec, reg[caveatDoctypeTitleAnnounced])
	confusionIdx := strings.Index(doctypeSec, "confusion table")
	if caveatIdx == -1 || confusionIdx == -1 || caveatIdx > confusionIdx {
		t.Errorf("doctype.title_announced must render above the confusion table")
	}
	if !strings.Contains(reg[caveatDoctypeTitleAnnounced], "optimistic bound") {
		t.Errorf("doctype.title_announced must contain %q", "optimistic bound")
	}

	// Placement leg: value.planted_wrongs inside value_check.
	mdValue, _, err := Render(valueVariant, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	valueSec := jmSection(t, string(mdValue), "value_check")
	if !strings.Contains(valueSec, reg[caveatValuePlantedWrongs]) {
		t.Errorf("value.planted_wrongs must render inside the value_check section")
	}

	// Placement leg: mapping_check_auto carries its three caveats.
	mdAuto, _, err := Render(mappingAutoFewWrongs, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	autoSec := jmSection(t, string(mdAuto), "mapping_check_auto")
	for _, id := range []string{caveatMappingDeclaredHeaderRow, caveatMappingAutoFewWrongs, caveatMappingAcceptedList} {
		if !strings.Contains(autoSec, reg[id]) {
			t.Errorf("mapping_check_auto section must contain caveat %q", id)
		}
	}
	for _, needle := range []string{"56", "50", "title_01", "title_02", "title_05", "title_06"} {
		if !strings.Contains(reg[caveatMappingDeclaredHeaderRow], needle) {
			t.Errorf("mapping.declared_header_row must name %q", needle)
		}
	}

	// Placement leg: mapping_check_ai carries accepted_list and two_halves.
	mdBoth, _, err := Render(bothMapping, Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	aiSec := jmSection(t, string(mdBoth), "mapping_check_ai")
	for _, id := range []string{caveatMappingAcceptedList, caveatMappingTwoHalves} {
		if !strings.Contains(aiSec, reg[id]) {
			t.Errorf("mapping_check_ai section must contain caveat %q", id)
		}
	}
}
