package blogweb

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// testdata/link_parts.json is also read by packages/api-client/src/linkParts.test.ts.
func TestLinkParts_MatchesSharedCaseTable(t *testing.T) {
	raw, err := os.ReadFile("testdata/link_parts.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name  string `json:"name"`
		In    string `json:"in"`
		Parts []struct {
			Text string `json:"text"`
			Href string `json:"href"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 30 {
		t.Fatalf("case table has %d cases", len(cases))
	}
	for _, c := range cases {
		want := []Part{}
		for _, p := range c.Parts {
			want = append(want, Part{Text: p.Text, Href: p.Href})
		}
		got := linkParts(c.In)
		if got == nil {
			got = []Part{}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: linkParts(%q) = %+v, want %+v", c.Name, c.In, got, want)
		}
	}
}
