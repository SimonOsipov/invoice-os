// ailines_internal_test.go: AIR-08's line-item scoring core -- role readings and the cell
// verdict. The row alignment itself lives in ailinesmerge.go now, shared with production.
// Pure logic: no file, no env var, no network.
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
