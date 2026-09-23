// ailines_key_internal_test.go: the line-item answer key -- aliLoadKey's refusal ladder, the
// key/dump coverage fence (AC-4..6), the key's own census (AC-7) and the provenance check
// (AC-8). No file is opened except at a path the caller supplies.
package extraction

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// aliKeyDoc is one document's key rows. Rows is never nil on a loaded key: a document with no
// rows list at all is refused, so an empty slice can only mean a confirmed zero-row control.
type aliKeyDoc struct {
	Confirmed bool
	Rows      []DocLine
	Note      string
}

type aliKey struct {
	Docs map[string]aliKeyDoc
}

// aliScoredZeroRow marks a confirmed document with no rows: a negative control, scored, not an
// unconfirmed one.
func (d aliKeyDoc) aliScoredZeroRow() bool { return d.Confirmed && len(d.Rows) == 0 }

// aliScoredDocs splits the key's documents by confirmation. Both lists sorted; an unconfirmed
// document contributes no cell anywhere downstream.
func aliScoredDocs(key aliKey) (scored, unconfirmed []string) {
	for file, doc := range key.Docs {
		if doc.Confirmed {
			scored = append(scored, file)
		} else {
			unconfirmed = append(unconfirmed, file)
		}
	}
	sort.Strings(scored)
	sort.Strings(unconfirmed)
	return scored, unconfirmed
}

// aliKeyDocJSON decodes one document entry. Rows is a pointer so an absent list stays
// distinguishable from an empty one, and roles stay raw map keys so an unknown one is still
// visible to name.
type aliKeyDocJSON struct {
	Confirmed bool                  `json:"confirmed"`
	Rows      *[]map[string]*string `json:"rows"`
	Note      string                `json:"note"`
}

// aliLoadKey refuses a key that cannot show it was confirmed, and a row carrying a role the
// package does not know. Mirrors aitLoadKey.
func aliLoadKey(path string) (aliKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return aliKey{}, fmt.Errorf("read line key %s: %w", path, err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return aliKey{}, fmt.Errorf("parse line key %s: %w", path, err)
	}

	confRaw, ok := top["confirmation"]
	if !ok {
		return aliKey{}, fmt.Errorf("line key %s carries no confirmation marker", path)
	}
	var conf aitConfirmationJSON
	if err := json.Unmarshal(confRaw, &conf); err != nil {
		return aliKey{}, fmt.Errorf("line key %s: parse confirmation: %w", path, err)
	}
	if strings.TrimSpace(conf.AnswerFile) == "" {
		return aliKey{}, fmt.Errorf("line key %s: confirmation carries no answer_file", path)
	}
	delete(top, "confirmation")

	docs := make(map[string]aliKeyDoc, len(top))
	for file, docRaw := range top {
		var d aliKeyDocJSON
		if err := json.Unmarshal(docRaw, &d); err != nil {
			return aliKey{}, fmt.Errorf("line key %s: parse %s: %w", path, file, err)
		}
		if d.Rows == nil {
			return aliKey{}, fmt.Errorf("line key %s: %s carries no rows list", path, file)
		}

		rows := make([]DocLine, 0, len(*d.Rows))
		for i, rowMap := range *d.Rows {
			line := DocLine{Index: i + 1}
			for role, v := range rowMap {
				if !containsString(LineRoles, role) {
					return aliKey{}, fmt.Errorf("line key %s: %s row %d carries role %q, which is not one of LineRoles", path, file, i+1, role)
				}
				switch role {
				case LineRoleDescription:
					line.Description = v
				case LineRoleQuantity:
					line.Quantity = v
				case LineRoleUnitPrice:
					line.UnitPrice = v
				case LineRoleLineTotal:
					line.LineTotal = v
				case LineRoleLineTax:
					line.LineTax = v
				}
			}
			rows = append(rows, line)
		}

		docs[file] = aliKeyDoc{
			Confirmed: d.Confirmed,
			Rows:      rows,
			Note:      d.Note,
		}
	}
	return aliKey{Docs: docs}, nil
}

// aliPublicStem returns a document's stem when its name marks it a committed corpus fixture --
// the only names a coverage failure may print. Cross-checked against docDump.Set by
// aliKeyCoverageProblems, so the prefix rule cannot drift away from the manifest.
func aliPublicStem(file string) (string, bool) {
	stem := aitStem(file)
	if strings.HasPrefix(stem, "corpus_") || strings.HasPrefix(stem, "wild_") {
		return stem, true
	}
	return "", false
}

// aliKeyCoverageProblems reports every break between the key and the dumped documents, empty
// when the two account for each other. Messages carry counts and corpus stems only: a ghost is
// usually a user invoice.
func aliKeyCoverageProblems(key aliKey, dumps []docDump, notScored []notScoredEntry) []string {
	var problems []string

	if len(dumps) == 0 {
		return append(problems, "aliCheckKeyCoverage read 0 dump(s); both directions below would pass vacuously")
	}

	var contradicts int
	for _, d := range dumps {
		_, pub := aliPublicStem(d.File)
		if pub != (d.Set == "corpus") {
			contradicts++
		}
	}
	if contradicts > 0 {
		problems = append(problems, fmt.Sprintf("%d dump(s) whose set contradicts the corpus-stem naming rule; a ghost's name could leak", contradicts))
	}

	var missing int
	var missingStems []string
	for _, d := range dumps {
		if d.TextChars == 0 {
			continue
		}
		if _, ok := key.Docs[d.File]; !ok {
			missing++
			if stem, ok := aliPublicStem(d.File); ok {
				missingStems = append(missingStems, stem)
			}
		}
	}
	if missing > 0 {
		sort.Strings(missingStems)
		problems = append(problems, fmt.Sprintf("%d scored document(s) have no key row; the line scorer would drop them silently (corpus: %v)", missing, missingStems))
	}

	accounted := map[string]bool{}
	for _, d := range dumps {
		accounted[d.File] = true
	}
	for _, n := range notScored {
		accounted[n.File] = true
	}

	var ghostCount int
	var ghostStems []string
	for file := range key.Docs {
		if accounted[file] {
			continue
		}
		ghostCount++
		if stem, ok := aliPublicStem(file); ok {
			ghostStems = append(ghostStems, stem)
		}
	}
	if ghostCount > 0 {
		sort.Strings(ghostStems)
		problems = append(problems, fmt.Sprintf("%d key row(s) match no dumped, textless or not-scored document (corpus: %v)", ghostCount, ghostStems))
	}

	return problems
}

// aliCheckKeyCoverage fails before a cell is written. AIR-08-05's driver calls it before its
// scoring loop.
func aliCheckKeyCoverage(t *testing.T, key aliKey, dumps []docDump, notScored []notScoredEntry) {
	t.Helper()
	if problems := aliKeyCoverageProblems(key, dumps, notScored); len(problems) > 0 {
		t.Fatal(strings.Join(problems, "; "))
	}
	scored := 0
	for _, d := range dumps {
		if d.TextChars > 0 {
			scored++
		}
	}
	t.Logf("line key coverage checked: %d scored document(s), %d key document(s)", scored, len(key.Docs))
}

// aliKeyTotalsResult is the key's own census. Value cells and correct nulls are counted
// SEPARATELY: a flat right/total over every cell would count a null==null agreement as evidence.
type aliKeyTotalsResult struct {
	Docs, Rows, ZeroRowControls, Unconfirmed int
	ValueCells, NullCells                    int
	NullsByRole                              map[string]int
}

// aliKeyTotals counts every field except Docs and Unconfirmed over CONFIRMED documents only.
func aliKeyTotals(key aliKey) aliKeyTotalsResult {
	result := aliKeyTotalsResult{Docs: len(key.Docs), NullsByRole: make(map[string]int, len(LineRoles))}
	for _, role := range LineRoles {
		result.NullsByRole[role] = 0
	}

	for _, d := range key.Docs {
		if !d.Confirmed {
			result.Unconfirmed++
			continue
		}
		if len(d.Rows) == 0 {
			result.ZeroRowControls++
		}
		result.Rows += len(d.Rows)
		for _, line := range d.Rows {
			for _, role := range LineRoles {
				cell := line.Cell(role)
				if aliKeyHasValue(cell) {
					result.ValueCells++
				} else {
					result.NullCells++
					result.NullsByRole[role]++
				}
			}
		}
	}
	return result
}

// aliProvenanceProblem reports a missing or empty second-reader record beside a key, empty
// when one is present. It never reads the contents: the record carries personal names.
func aliProvenanceProblem(keyPath string) string {
	dir := filepath.Dir(keyPath)
	info, err := os.Stat(filepath.Join(dir, "key.provenance.md"))
	if err != nil {
		return fmt.Sprintf("no provenance record beside the key in %s", dir)
	}
	if info.Size() == 0 {
		return fmt.Sprintf("the provenance record beside the key in %s is empty", dir)
	}
	return ""
}

// aliRealKeyPath returns the real key's path, or "" after one log line. The gate returns; it
// never asks the runner to skip -- TestExtractionPackage_HasExactlyOneSkipSite owns the
// package's only skip site.
func aliRealKeyPath(t *testing.T) string {
	t.Helper()
	path := os.Getenv("AIMT_LINE_KEY")
	if path == "" {
		t.Log("AIMT_LINE_KEY unset: no read")
	}
	return path
}
