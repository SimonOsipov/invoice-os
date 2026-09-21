// ailines_internal_test.go: AIR-08's line-item scoring core -- role readings, the cell verdict
// and the order-preserving row alignment. Pure logic: no file, no env var, no network.
package extraction

import (
	"sort"
	"strings"
	"testing"
)

const (
	aliRight   = "right"
	aliWrong   = "wrong"
	aliMissing = "missing"
)

const (
	aliFound      = "found"
	aliMisaligned = "misaligned"
)

const (
	aliPassStrong     = 1
	aliPassAmount     = 2
	aliPassPositional = 3
)

func aliStr(s string) *string { return &s }

// aliEngineLineJSON is one line-item row's engine reading: only the roles the reader populated,
// keyed by role. An absent cell carries no key, the contract aitDumpEngineJSON already uses.
type aliEngineLineJSON struct {
	Index int                          `json:"index"`
	Roles map[string]aitDumpEngineJSON `json:"roles"`
}

// aliEngineLines groups Reconcile's line-item rows by index, ascending. A row whose every cell
// is absent produced no FieldResult at all, so it emits no entry and the indexes may gap.
func aliEngineLines(results []FieldResult) []aliEngineLineJSON {
	byIndex := map[int]map[string]aitDumpEngineJSON{}
	for _, r := range results {
		index, role, ok := ParseLineFieldName(r.Name)
		if !ok {
			continue // a header field, or the line_items block row: neither is a line cell
		}
		if byIndex[index] == nil {
			byIndex[index] = map[string]aitDumpEngineJSON{}
		}
		v := *r.Value // never nil: TestAliEngineLines_EveryEmittedLineRowCarriesAValue
		byIndex[index][role] = aitDumpEngineJSON{Value: &v, Reason: string(r.Reason)}
	}

	indexes := make([]int, 0, len(byIndex))
	for i := range byIndex {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)

	out := make([]aliEngineLineJSON, 0, len(indexes))
	for _, i := range indexes {
		out = append(out, aliEngineLineJSON{Index: i, Roles: byIndex[i]})
	}
	return out
}

// aliTrimTrailingZeros makes 700 and 700.00 one value; line_items.unit_price is numeric(14,2),
// so the difference is invisible downstream and scoring it would measure formatting.
func aliTrimTrailingZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	return strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
}

// aliRoleReadings returns the acceptable readings of one line-item cell. A numeric role whose
// raw the normaliser refuses has no reading, so a refused answer can never score right by loose
// text luck.
func aliRoleReadings(role, raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	switch role {
	case LineRoleDescription:
		return []string{aitLoose(raw)}
	case LineRoleQuantity:
		v, ok := liNormalizeQuantity(raw)
		if !ok {
			return nil
		}
		return []string{aliTrimTrailingZeros(v)}
	case LineRoleUnitPrice, LineRoleLineTotal, LineRoleLineTax:
		readings := normalizeAmount(raw)
		out := make([]string, len(readings))
		for i, r := range readings {
			out[i] = aliTrimTrailingZeros(r)
		}
		return out
	}
	return nil
}

// aliKeyHasValue separates value cells from correct nulls, so a flat right/total never
// overstates the evidence.
func aliKeyHasValue(key *string) bool {
	return key != nil && strings.TrimSpace(*key) != ""
}

func aliClassifyCell(role string, answer, key *string) string {
	answerBlank := answer == nil || strings.TrimSpace(*answer) == ""
	if !aliKeyHasValue(key) {
		if answerBlank {
			return aliRight
		}
		return aliWrong // a value invented against a null key cell
	}
	if answerBlank {
		return aliMissing
	}
	if hasCommonReading(aliRoleReadings(role, *answer), aliRoleReadings(role, *key)) {
		return aliRight
	}
	return aliWrong
}

// aliPairIdx is one matched pair, as 0-based positions into the caller's own answer/key slices.
type aliPairIdx struct{ Answer, Key int }

// aliMonotoneMatch returns a maximum-size matching strictly increasing on both sides.
// best[i][j] is filled backwards, then reconstructed by the standard LCS traceback: at each
// (i,j), take the pair when it is compatible and optimal, else advance whichever side's drop
// costs nothing.
func aliMonotoneMatch(answer, key []int, compatible func(a, k int) bool) []aliPairIdx {
	n, m := len(answer), len(key)
	best := make([][]int, n+1)
	for i := range best {
		best[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			take := 0
			if compatible(answer[i], key[j]) {
				take = 1 + best[i+1][j+1]
			}
			best[i][j] = aliMax3(best[i+1][j], best[i][j+1], take)
		}
	}

	pairs := make([]aliPairIdx, 0)
	i, j := 0, 0
	for i < n && j < m {
		if compatible(answer[i], key[j]) && best[i][j] == 1+best[i+1][j+1] {
			pairs = append(pairs, aliPairIdx{Answer: i, Key: j})
			i++
			j++
		} else if best[i+1][j] >= best[i][j+1] {
			i++
		} else {
			j++
		}
	}
	return pairs
}

func aliMax3(a, b, c int) int {
	m := a
	if b > m {
		m = b
	}
	if c > m {
		m = c
	}
	return m
}

// aliPair is one row match: 1-based positions in the caller's answer/key rows.
type aliPair struct {
	Answer  int
	Key     int
	Pass    int
	Outcome string
}

type aliAlignment struct {
	Pairs    []aliPair
	Invented []int // 1-based answer positions, ascending
	Dropped  []int // 1-based key positions, ascending
}

func (a aliAlignment) Found() int { return aliCountOutcome(a.Pairs, aliFound) }

func (a aliAlignment) Misaligned() int { return aliCountOutcome(a.Pairs, aliMisaligned) }

func aliCountOutcome(pairs []aliPair, outcome string) int {
	n := 0
	for _, p := range pairs {
		if p.Outcome == outcome {
			n++
		}
	}
	return n
}

func aliCellsAgree(role string, a, k DocLine) bool {
	ac, kc := a.Cell(role), k.Cell(role)
	if ac == nil || kc == nil {
		return false
	}
	return hasCommonReading(aliRoleReadings(role, *ac), aliRoleReadings(role, *kc))
}

func aliStrongAnchor(a, k DocLine) bool {
	return aliCellsAgree(LineRoleDescription, a, k) && aliCellsAgree(LineRoleLineTotal, a, k)
}

func aliAmountAnchor(a, k DocLine) bool {
	return aliCellsAgree(LineRoleLineTotal, a, k)
}

// aliSeq returns [lo, lo+1, ..., hi], or nil when lo > hi.
func aliSeq(lo, hi int) []int {
	if lo > hi {
		return nil
	}
	out := make([]int, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		out = append(out, i)
	}
	return out
}

// aliGaps returns the answer/key position ranges between consecutive pairs, plus the head
// before the first pair and the tail after the last. Positions are 1-based, ascending.
func aliGaps(pairs []aliPair, nAnswer, nKey int) [][2][]int {
	gaps := make([][2][]int, 0, len(pairs)+1)
	prevA, prevK := 0, 0
	for _, p := range pairs {
		gaps = append(gaps, [2][]int{aliSeq(prevA+1, p.Answer-1), aliSeq(prevK+1, p.Key-1)})
		prevA, prevK = p.Answer, p.Key
	}
	gaps = append(gaps, [2][]int{aliSeq(prevA+1, nAnswer), aliSeq(prevK+1, nKey)})
	return gaps
}

// aliMatchRange runs the maximum monotone matching over one position range, mapping local match
// positions back to 1-based global rows.
func aliMatchRange(answer, key []DocLine, ansPos, keyPos []int, pass int, compatible func(a, k DocLine) bool) []aliPair {
	if len(ansPos) == 0 || len(keyPos) == 0 {
		return nil
	}
	localCompat := func(a, k int) bool { return compatible(answer[a-1], key[k-1]) }
	local := aliMonotoneMatch(ansPos, keyPos, localCompat)
	out := make([]aliPair, len(local))
	for i, lp := range local {
		out[i] = aliPair{Answer: ansPos[lp.Answer], Key: keyPos[lp.Key], Pass: pass}
	}
	return out
}

func aliSortPairs(pairs []aliPair) {
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Answer < pairs[j].Answer })
}

// aliAlign runs the three passes in order: strong anchor over the full range, amount anchor per
// gap left by pass 1, then positional fill per gap left by passes 1+2.
func aliAlign(answer, key []DocLine) aliAlignment {
	nAnswer, nKey := len(answer), len(key)

	pairs := aliMatchRange(answer, key, aliSeq(1, nAnswer), aliSeq(1, nKey), aliPassStrong, aliStrongAnchor)

	for _, gap := range aliGaps(pairs, nAnswer, nKey) {
		pairs = append(pairs, aliMatchRange(answer, key, gap[0], gap[1], aliPassAmount, aliAmountAnchor)...)
	}
	aliSortPairs(pairs)

	for _, gap := range aliGaps(pairs, nAnswer, nKey) {
		ga, gk := gap[0], gap[1]
		for t := 0; t < len(ga) && t < len(gk); t++ {
			pairs = append(pairs, aliPair{Answer: ga[t], Key: gk[t], Pass: aliPassPositional})
		}
	}
	aliSortPairs(pairs)

	for i := range pairs {
		if pairs[i].Pass == aliPassPositional && !aliCellsAgree(LineRoleDescription, answer[pairs[i].Answer-1], key[pairs[i].Key-1]) {
			pairs[i].Outcome = aliMisaligned
		} else {
			pairs[i].Outcome = aliFound
		}
	}

	return aliAlignment{
		Pairs:    pairs,
		Invented: aliLeftover(pairs, nAnswer, func(p aliPair) int { return p.Answer }),
		Dropped:  aliLeftover(pairs, nKey, func(p aliPair) int { return p.Key }),
	}
}

// aliLeftover returns the 1..n positions no pair covers, ascending.
func aliLeftover(pairs []aliPair, n int, pos func(aliPair) int) []int {
	used := make(map[int]bool, len(pairs))
	for _, p := range pairs {
		used[pos(p)] = true
	}
	out := make([]int, 0)
	for i := 1; i <= n; i++ {
		if !used[i] {
			out = append(out, i)
		}
	}
	return out
}

// aliCheckPartition asserts the two row-outcome invariants: every key row is found, misaligned
// or dropped, and every answer row is found, misaligned or invented.
func aliCheckPartition(t *testing.T, got aliAlignment, nAnswer, nKey int) {
	t.Helper()
	if sum := got.Found() + got.Misaligned() + len(got.Dropped); sum != nKey {
		t.Errorf("Found()+Misaligned()+len(Dropped) = %d, want %d (len(key))", sum, nKey)
	}
	if sum := got.Found() + got.Misaligned() + len(got.Invented); sum != nAnswer {
		t.Errorf("Found()+Misaligned()+len(Invented) = %d, want %d (len(answer))", sum, nAnswer)
	}
}
