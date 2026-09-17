// aitext_adversarial_internal_test.go: QA edge and negative coverage for the AIR-01-01 helpers.
package extraction

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAIText_InLineRuleFindsAMultiWordValueInsideALine(t *testing.T) {
	line := tok("Ref 7781 dated 04 Mar 2026 terms 30 days", 1, 0.10, 0.40, 0.60, 0.42)
	pages := onePage(1, line)

	if found, _ := aitRuleWholeToken("issue_date", "2026-03-04", pages); found {
		t.Fatal("precondition failed: rule A already finds the date inside the longer line")
	}
	found, box := aitRuleInLine("issue_date", "2026-03-04", pages)
	if !found {
		t.Fatal("rule B did not find a three-word date inside one line")
	}
	if box != line.Region {
		t.Errorf("box = %+v, want the line's region %+v", box, line.Region)
	}
}

func TestAIText_JoinedRowRuleJoinsATINSplitAcrossTwoCells(t *testing.T) {
	left := tok("99999999-", 1, 0.10, 0.300, 0.20, 0.312)
	right := tok("1202", 1, 0.21, 0.300, 0.25, 0.312)
	pages := onePage(1, left, right)

	if found, _ := aitRuleInLine("buyer_tin", "99999999-1202", pages); found {
		t.Fatal("precondition failed: rule B already joins two cells")
	}
	found, box := aitRuleJoinedRow("buyer_tin", "99999999-1202", pages)
	if !found {
		t.Fatal("rule C did not join a TIN split across two cells on one row")
	}
	if want := unionRegion(left.Region, right.Region); box != want {
		t.Errorf("box = %+v, want %+v", box, want)
	}
	if found, _ := aitRuleJoinedRow("buyer_tin", "99999999-1203", pages); found {
		t.Error("rule C accepted a TIN whose last group is not the printed one")
	}
}

func TestAIText_JoinedRowRuleDoesNotJoinAcrossPages(t *testing.T) {
	pages := []TokenPage{
		{Number: 1, Tokens: []Token{tok("Honeywell", 1, 0.10, 0.300, 0.19, 0.312)}},
		{Number: 2, Tokens: []Token{tok("Group", 2, 0.20, 0.300, 0.26, 0.312)}},
	}
	if found, _ := aitRuleJoinedRow("buyer_name", "Honeywell Group", pages); found {
		t.Error("rule C joined two cells at the same height on different pages")
	}
}

func TestAIText_RulesFindAValueOnTheSecondPage(t *testing.T) {
	golden := aitGoldenPages(t, "wild_scanned_no_number")
	if len(golden) == 0 || len(golden[0].Tokens) == 0 {
		t.Fatal("precondition failed: the golden replay produced no tokens")
	}
	second := TokenPage{Number: 2}
	for _, tk := range golden[0].Tokens {
		tk.Region.Page = 2
		second.Tokens = append(second.Tokens, tk)
	}
	first := TokenPage{Number: 1, Tokens: []Token{tok("TIN: 11111111-0001", 1, 0.1, 0.1, 0.4, 0.12)}}
	pages := []TokenPage{first, second}
	want := findToken(t, []TokenPage{second}, "99999999-1202")

	rules := map[string]func(string, string, []TokenPage) (bool, Region){
		"A": aitRuleWholeToken, "B": aitRuleInLine, "C": aitRuleJoinedRow,
	}
	for name, rule := range rules {
		found, box := rule("buyer_tin", "99999999-1202", pages)
		if !found {
			t.Errorf("rule %s did not find the value printed on page 2", name)
			continue
		}
		if box != want.Region || box.Page != 2 {
			t.Errorf("rule %s box = %+v, want the page-2 token region %+v", name, box, want.Region)
		}
	}
}

func TestAIText_EveryRuleFindsNothingOnAnEmptyRead(t *testing.T) {
	reads := map[string][]TokenPage{
		"nil pages":         nil,
		"a page, no tokens": {{Number: 1}},
	}
	rules := map[string]func(string, string, []TokenPage) (bool, Region){
		"A": aitRuleWholeToken, "B": aitRuleInLine, "C": aitRuleJoinedRow, "control": aitRuleSubstringControl,
	}
	for readName, pages := range reads {
		for ruleName, rule := range rules {
			if found, box := rule("total", "1935.00", pages); found || box != (Region{}) {
				t.Errorf("%s: rule %s = (%v, %+v), want (false, zero box)", readName, ruleName, found, box)
			}
		}
	}
}

func TestAIText_AConfirmedEmptyKeyFieldIsScored(t *testing.T) {
	keyPath := writeJSONFile(t, "key.json", map[string]any{
		"confirmation": map[string]any{"source": "RALPH 0.6d", "answer_file": "key.answer.md", "recorded": "2026-09-17"},
		"doc.pdf": map[string]any{
			"set": "corpus", "source": "drafted",
			"fields": map[string]any{
				"vat": map[string]any{"values": []string{}, "confirmed": true},
			},
		},
	})
	key, err := aitLoadKey(keyPath)
	if err != nil {
		t.Fatalf("aitLoadKey: %v", err)
	}
	answers := []docAnswer{{File: "doc.pdf", Run: 1, Fields: map[string]string{}}}
	result := aitScoreConfirmed(key, []docDump{{File: "doc.pdf", TextChars: 100}}, answers, nil)
	if !hasCellForField(result.Cells, "doc.pdf", "vat") {
		t.Errorf("cells = %v, want a cell for the confirmed empty vat entry", result.Cells)
	}
	if fieldRefListed(result.Unconfirmed, "doc.pdf", "vat") {
		t.Error("a confirmed empty entry was listed as unconfirmed")
	}

	values := key.Docs["doc.pdf"].Fields["vat"].Values
	if got := aitClassify("vat", nil, values); got != "right" {
		t.Errorf("blank answer on a confirmed empty key = %q, want right", got)
	}
	printed := "67.50"
	if got := aitClassify("vat", &printed, values); got != "wrong" {
		t.Errorf("a value on a confirmed empty key = %q, want wrong", got)
	}
}

func TestAIText_AnErroredRecordWithFieldsNeverScores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "answers.jsonl")
	body := `{"file":"doc.pdf","run":1,"fields":{"total":"1935.00"}}` + "\n" +
		`{"file":"doc.pdf","run":2,"error":"timeout","fields":{"total":"1935.00"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write answers.jsonl: %v", err)
	}
	good, errCount, err := aitLoadAnswers(path)
	if err != nil {
		t.Fatalf("aitLoadAnswers: %v", err)
	}
	if len(good) != 1 || good[0].Run != 1 {
		t.Errorf("good = %+v, want only run 1", good)
	}
	if errCount != 1 {
		t.Errorf("errors = %d, want 1: a record with error set is errored even when it carries fields", errCount)
	}
}

func TestAIText_PercentilesSortACopyOfUnsortedInput(t *testing.T) {
	vals := []float64{10, 3, 7, 1, 9, 2, 8, 4, 6, 5}
	p50, p90, ok := aitPercentile(vals)
	if !ok || p50 != 6 || p90 != 10 {
		t.Errorf("aitPercentile(unsorted 1..10) = (%v, %v, %v), want (6, 10, true)", p50, p90, ok)
	}
	if vals[0] != 10 || vals[1] != 3 {
		t.Errorf("aitPercentile reordered its input: %v", vals)
	}
}

func TestAIText_PicksTheJoinedRowRuleWhenItAcceptsNothingExtra(t *testing.T) {
	stats := []ruleStat{
		{Name: "A", Found: 10, Accepted: 1, Eligible: true},
		{Name: "B", Found: 12, Accepted: 1, Eligible: true},
		{Name: "C", Found: 14, Accepted: 1, Eligible: true},
	}
	if got := aitPickRule(stats); got != "C" {
		t.Errorf("aitPickRule = %q, want C", got)
	}
	noneEligible := []ruleStat{{Name: "A", Found: 10, Accepted: 1}, {Name: "B", Found: 12, Accepted: 1}}
	if got := aitPickRule(noneEligible); got != "" {
		t.Errorf("aitPickRule(no eligible rule) = %q, want none", got)
	}
}
