// ailinesmerge.go: the decision layer between Reconcile's line rows and the AI's line-item
// answer (Core AC 6). mergeAILines runs after Reconcile, over its []FieldResult, never before it
// over []DocLine -- merging earlier would feed AI rows into reconcileLines' line-sum and
// subtotal cross-check, reopening the Q6 arithmetic fence this story leaves alone.
//
// The row alignment (aliAlign and its dependents) is AIR-08-01's own measured three-pass rule,
// moved here from its harness test file unexported so production and the harness share one
// implementation; the harness's own specs keep calling it unchanged.
package extraction

import (
	"sort"
	"strings"
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

// aliTrimTrailingZeros makes 700 and 700.00 one value; line_items.unit_price is numeric(14,2),
// so the difference is invisible downstream and comparing it as different would measure
// formatting, not content.
func aliTrimTrailingZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	return strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
}

// aliRoleReadings returns the acceptable readings of one line-item cell. A numeric role whose
// raw the normaliser refuses has no reading, so a refused answer can never agree by loose text
// luck.
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

// hasCommonReading reports whether a and b share any reading.
func hasCommonReading(a, b []string) bool {
	for _, r := range a {
		for _, s := range b {
			if r == s {
				return true
			}
		}
	}
	return false
}

func aitLoose(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

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

// Cell mirrors DocLine.Cell so both sides of the alignment share one lookup shape.
func (l AILine) Cell(role string) *string {
	switch role {
	case LineRoleDescription:
		return l.Description
	case LineRoleQuantity:
		return l.Quantity
	case LineRoleUnitPrice:
		return l.UnitPrice
	case LineRoleLineTotal:
		return l.LineTotal
	case LineRoleLineTax:
		return l.LineTax
	}
	return nil
}

// aliEngineDocLine projects one engine index's cells into a DocLine for alignment. Region and
// reason play no part in aliCellsAgree, so only the values are carried.
func aliEngineDocLine(index int, cells map[string]FieldResult) DocLine {
	line := DocLine{Index: index}
	for role, r := range cells {
		v := *r.Value // never nil: LineItemResults and reconcileLines never emit a valueless row
		switch role {
		case LineRoleDescription:
			line.Description = &v
		case LineRoleQuantity:
			line.Quantity = &v
		case LineRoleUnitPrice:
			line.UnitPrice = &v
		case LineRoleLineTotal:
			line.LineTotal = &v
		case LineRoleLineTax:
			line.LineTax = &v
		}
	}
	return line
}

func aliAIDocLine(index int, l AILine) DocLine {
	return DocLine{
		Index:       index,
		Description: l.Description,
		Quantity:    l.Quantity,
		UnitPrice:   l.UnitPrice,
		LineTotal:   l.LineTotal,
		LineTax:     l.LineTax,
	}
}

// aliLineUnit is one row of aliPrintedOrder's output: a position into engineLines/engineIndexes
// (keyPos), a position into aiLines/ai (answerPos), or both. 0 means absent on that side.
type aliLineUnit struct {
	keyPos    int
	answerPos int
}

// aliUnitsForGap orders one gap's unmatched rows: the engine's own dropped rows (AC-7) before
// the AI's invented ones (AC-6) -- the engine's order is the backbone the AI's extra rows are
// spliced into.
func aliUnitsForGap(gap [2][]int) []aliLineUnit {
	var out []aliLineUnit
	for _, k := range gap[1] {
		out = append(out, aliLineUnit{keyPos: k})
	}
	for _, a := range gap[0] {
		out = append(out, aliLineUnit{answerPos: a})
	}
	return out
}

// aliPrintedOrder walks aliAlign's pairs and the gaps between (and before/after) them, so every
// row -- paired, dropped or invented -- comes out in aligned printed order (AC-8).
func aliPrintedOrder(a aliAlignment, nAnswer, nKey int) []aliLineUnit {
	gaps := aliGaps(a.Pairs, nAnswer, nKey)
	var out []aliLineUnit
	for i, p := range a.Pairs {
		out = append(out, aliUnitsForGap(gaps[i])...)
		out = append(out, aliLineUnit{keyPos: p.Key, answerPos: p.Answer})
	}
	out = append(out, aliUnitsForGap(gaps[len(gaps)-1])...)
	return out
}

// aliWriteValue is the canonical stored form of an AI-only cell: the same normalisers the
// engine's own rows are written through (liNormalizeDescription, liNormalizeQuantity,
// normalizeAmount) -- never aliRoleReadings' loosened comparison form, which would lower-case a
// description.
func aliWriteValue(role, raw string) (string, bool) {
	switch role {
	case LineRoleDescription:
		return liNormalizeDescription(raw)
	case LineRoleQuantity:
		return liNormalizeQuantity(raw)
	case LineRoleUnitPrice, LineRoleLineTotal, LineRoleLineTax:
		readings := normalizeAmount(raw)
		if len(readings) == 0 {
			return "", false
		}
		return readings[0], true
	}
	return "", false
}

// aliLinePresent is the page check a hallucinated AI-only line value must pass before it is
// written (AC-13/14): the value counts as present only when some run of words on one token's
// own line reads, under aliRoleReadings' own normalisation, as one of raw's own readings.
// Mirrors aiOccurrences' token-line walk; unlike checkAI there is no anchorLexicon label to
// corroborate a line role against.
// ceiling: a run never crosses a token boundary, so a description split across two visual lines
// (one token each) cannot be found whole; revisit if that proves common in the corpus.
func aliLinePresent(role, raw string, pages []TokenPage) bool {
	want := aliRoleReadings(role, raw)
	if len(want) == 0 {
		return false
	}
	for _, p := range pages {
		for _, t := range p.Tokens {
			words := strings.Fields(t.Text)
			starts, ends := aiWordOffsets(words)
			line := strings.Join(words, " ")
			for wi := range words {
				for wj := wi + 1; wj <= len(words); wj++ {
					run := line[starts[wi]:ends[wj-1]]
					if hasCommonReading(aliRoleReadings(role, run), want) {
						return true
					}
				}
			}
		}
	}
	return false
}

// aliMergeCell decides one role cell of a merged line row. engine/engineOK is the row's
// existing engine result for role (Value is never nil when engineOK); aiRaw is the AI's raw
// cell, nil when it answered nothing here. Returns nil when the cell has no evidence from
// either side, or an AI-only value is refused.
func aliMergeCell(role string, engine FieldResult, engineOK bool, aiRaw *string, pages []TokenPage) *FieldResult {
	aiHas := aiRaw != nil && strings.TrimSpace(*aiRaw) != ""

	if engineOK {
		row := engine
		switch {
		case !aiHas:
			return &row // AC-7: no AI evidence, the engine's row stands
		case hasCommonReading(aliRoleReadings(role, *engine.Value), aliRoleReadings(role, *aiRaw)):
			return &row // AC-3: agree, engine's row survives untouched
		case len(aliRoleReadings(role, *aiRaw)) == 0:
			return &row // the AI's cell is not a reading of this role at all: not evidence
		case !aliLinePresent(role, *aiRaw, pages):
			// AC-13: an unchecked value never flips a good engine cell to ambiguous,
			// matching mergeAI's own `if !checked { continue }`.
			return &row
		}
		v := *aiRaw
		row.Reason = ReasonAmbiguous
		row.Alternatives = []Field{{Name: engine.Name, Value: &v, Region: nil, Reason: ReasonNone}}
		return &row // AC-5: disagree, engine's value and region stand at rank 0
	}

	if !aiHas || len(aliRoleReadings(role, *aiRaw)) == 0 {
		return nil
	}
	if !aliLinePresent(role, *aiRaw, pages) {
		return nil // AC-13/14: not on the page, refused
	}
	written, ok := aliWriteValue(role, *aiRaw)
	if !ok {
		return nil
	}
	return &FieldResult{Field: Field{Value: &written, Reason: ReasonNone}, Alternatives: []Field{}} // AC-4/6: unmarked
}

// mergeAILines runs after Reconcile, over its own output rows: it decides every
// line_items[N].<role> row from rows' engine cells and the AI's line-item answer. Every
// non-line row -- the header fields, the line_items block, any marker row -- is returned
// byte-identical, in its original relative position (AC-2). ai == nil or empty returns rows
// completely unchanged (AC-9).
func mergeAILines(rows []FieldResult, ai []AILine, pages []TokenPage) []FieldResult {
	if len(ai) == 0 {
		return rows
	}

	nonLine := make([]FieldResult, 0, len(rows))
	engineByIndex := map[int]map[string]FieldResult{}
	var engineIndexes []int
	insertAt := -1

	for _, r := range rows {
		idx, role, ok := ParseLineFieldName(r.Name)
		if !ok {
			nonLine = append(nonLine, r)
			continue
		}
		if insertAt == -1 {
			insertAt = len(nonLine)
		}
		if engineByIndex[idx] == nil {
			engineByIndex[idx] = map[string]FieldResult{}
			engineIndexes = append(engineIndexes, idx)
		}
		engineByIndex[idx][role] = r
	}
	if insertAt == -1 {
		insertAt = len(nonLine)
	}
	liSortInts(engineIndexes)

	engineLines := make([]DocLine, len(engineIndexes))
	for i, idx := range engineIndexes {
		engineLines[i] = aliEngineDocLine(idx, engineByIndex[idx])
	}
	aiLines := make([]DocLine, len(ai))
	for i, l := range ai {
		aiLines[i] = aliAIDocLine(i+1, l)
	}

	alignment := aliAlign(aiLines, engineLines)
	units := aliPrintedOrder(alignment, len(aiLines), len(engineLines))

	type namedCell struct {
		role string
		row  FieldResult
	}
	var mergedRows [][]namedCell
	for _, u := range units {
		var engineCells map[string]FieldResult
		if u.keyPos != 0 {
			engineCells = engineByIndex[engineIndexes[u.keyPos-1]]
		}
		var aiLine AILine
		if u.answerPos != 0 {
			aiLine = ai[u.answerPos-1]
		}

		var cells []namedCell
		for _, role := range LineRoles {
			engineRow, engineOK := engineCells[role]
			var aiRaw *string
			if u.answerPos != 0 {
				aiRaw = aiLine.Cell(role)
			}
			if cell := aliMergeCell(role, engineRow, engineOK, aiRaw, pages); cell != nil {
				cells = append(cells, namedCell{role: role, row: *cell})
			}
		}
		if len(cells) > 0 {
			mergedRows = append(mergedRows, cells)
		}
	}

	lineRows := make([]FieldResult, 0, len(mergedRows)*len(LineRoles))
	for i, cells := range mergedRows {
		for _, c := range cells {
			name := LineFieldName(i+1, c.role)
			c.row.Name = name
			for j := range c.row.Alternatives {
				c.row.Alternatives[j].Name = name
			}
			lineRows = append(lineRows, c.row)
		}
	}

	out := make([]FieldResult, 0, len(nonLine)+len(lineRows))
	out = append(out, nonLine[:insertAt]...)
	out = append(out, lineRows...)
	out = append(out, nonLine[insertAt:]...)
	return out
}
