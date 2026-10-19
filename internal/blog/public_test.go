package blog

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

func TestLagosDate_TwentyThreeUTCIsTheNextDay(t *testing.T) {
	before := time.Date(2026, 10, 17, 22, 59, 59, 0, time.UTC)
	at := time.Date(2026, 10, 17, 23, 0, 0, 0, time.UTC)
	if got := LagosDate(before); got != "2026-10-17" {
		t.Errorf("LagosDate(22:59:59Z) = %q, want 2026-10-17", got)
	}
	if got := LagosDate(at); got != "2026-10-18" {
		t.Errorf("LagosDate(23:00:00Z) = %q, want 2026-10-18", got)
	}
	for _, c := range []struct {
		name string
		in   time.Time
		want string
	}{
		{"new year", time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC), "2027-01-01"},
		{"leap day", time.Date(2028, 2, 28, 23, 0, 0, 0, time.UTC), "2028-02-29"},
		{"northern summer has no offset change", time.Date(2026, 7, 1, 22, 59, 59, 0, time.UTC), "2026-07-01"},
	} {
		if got := LagosDate(c.in); got != c.want {
			t.Errorf("%s: LagosDate(%s) = %q, want %s", c.name, c.in.Format(time.RFC3339), got, c.want)
		}
	}
}

func TestLagosDate_IgnoresTheInputZone(t *testing.T) {
	in := time.Date(2026, 10, 18, 0, 30, 0, 0, time.FixedZone("UTC+3", 3*3600))
	if got := LagosDate(in); got != "2026-10-17" {
		t.Errorf("LagosDate(00:30 UTC+3 = 21:30Z) = %q, want 2026-10-17", got)
	}
}

func TestCoverPath_IsThePublicPrefixIdAndVersion(t *testing.T) {
	if got, want := CoverPath("p", 1760000000123456), "/blog/covers/p/1760000000123456"; got != want {
		t.Errorf("CoverPath = %q, want %q", got, want)
	}
}

func TestPublicPost_WireCarriesAuthorNameAndNoAuthorID(t *testing.T) {
	raw, err := json.Marshal(PublicPost{Body: []Block{{Type: "p", Text: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{"author_name", "body", "category_id", "cover", "featured", "id", "publish_date", "summary", "title"}
	if !slices.Equal(keys, want) {
		t.Errorf("wire keys = %v, want %v", keys, want)
	}
	if string(m["cover"]) != "null" {
		t.Errorf(`cover with no image = %s, want null`, m["cover"])
	}
}
