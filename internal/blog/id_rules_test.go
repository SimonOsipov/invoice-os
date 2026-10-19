package blog

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// testdata/id_rules.json is also read by frontend/support-console/src/blog.test.ts.
func TestIDRules_FixtureMatchesPostIDAndCleanBody(t *testing.T) {
	raw, err := os.ReadFile("testdata/id_rules.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		PostID []struct {
			Title string   `json:"title"`
			Taken []string `json:"taken"`
			Want  string   `json:"want"`
		} `json:"post_id"`
		CleanBody []struct {
			Body []Block `json:"body"`
			Want []Block `json:"want"`
		} `json:"clean_body"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.PostID) == 0 || len(fx.CleanBody) == 0 {
		t.Fatal("fixture is empty")
	}
	for _, r := range fx.PostID {
		if got := PostID(r.Title, func(id string) bool { return slices.Contains(r.Taken, id) }); got != r.Want {
			t.Errorf("PostID(%q, %v) = %q, want %q", r.Title, r.Taken, got, r.Want)
		}
	}
	for i, r := range fx.CleanBody {
		got, _ := json.Marshal(CleanBody(r.Body))
		want, _ := json.Marshal(r.Want)
		if string(got) != string(want) {
			t.Errorf("clean_body[%d]: got %s, want %s", i, got, want)
		}
	}
}
