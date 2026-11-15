package main

import (
	"slices"
	"strings"
	"testing"
)

// Two docling builds must list one pip freeze, and a red comparison must fail ci.
func TestCI_DoclingFreezeGatesTheRollUp(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "ci.yml")
	words := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune("[], ", r) })
	}

	fz, ok := ciJobOf(yml, "docling-freeze")
	if !ok {
		t.Fatal("ci.yml has no docling-freeze job")
	}
	fzNeeds, _ := jobKey(fz, "needs")
	for _, n := range []string{"docling-canary", "docling-test"} {
		if !slices.Contains(words(fzNeeds), n) {
			t.Errorf("docling-freeze needs lacks %s", n)
		}
	}
	if body := strings.Join(fz.lines, "\n"); !strings.Contains(body, "diff -u freeze/test-build.freeze freeze/canary-run.freeze") {
		t.Error("docling-freeze does not diff the two freeze files")
	}

	ci, ok := ciJobOf(yml, "ci")
	if !ok {
		t.Fatal("ci.yml has no ci job")
	}
	needs, _ := jobKey(ci, "needs")
	if !slices.Contains(words(needs), "docling-freeze") {
		t.Error("ci needs lacks docling-freeze")
	}
	body := strings.Join(ci.lines, "\n")
	for _, res := range []string{"failure", "cancelled"} {
		if !strings.Contains(body, `needs.docling-freeze.result }}" = "`+res+`"`) {
			t.Errorf("ci run block does not fail on a %s docling-freeze", res)
		}
	}
}
