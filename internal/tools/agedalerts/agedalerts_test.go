package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

const week = 7 * 24 * time.Hour

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func numbers(as []Alert) []int {
	out := []int{}
	for _, a := range as {
		out = append(out, a.Number)
	}
	return out
}

func mustSelect(t *testing.T, raw []byte, at time.Time) []Alert {
	t.Helper()
	as, err := Select(raw, at, week)
	if err != nil {
		t.Fatal(err)
	}
	return as
}

func alertJSON(number int, state, created string) string {
	return fmt.Sprintf(`{"number":%d,"state":%q,"created_at":%q}`, number, state, created)
}

func TestSelect_SevenDayCut(t *testing.T) {
	got := numbers(mustSelect(t, fixture(t), now))
	if want := []int{3, 4, 8, 9}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSelect_ExactlySevenDaysDoesNotCount(t *testing.T) {
	raw := fixture(t)
	if got := numbers(mustSelect(t, raw, now)); contains(got, 2) {
		t.Fatalf("#2 counted at exactly 7 days: %v", got)
	}
	if got := numbers(mustSelect(t, raw, now.Add(time.Second))); !contains(got, 2) {
		t.Fatalf("#2 missing one second later: %v", got)
	}
	if got := numbers(mustSelect(t, raw, now.Add(-time.Second))); contains(got, 3) {
		t.Fatalf("#3 counted one second early: %v", got)
	}
}

func contains(xs []int, n int) bool {
	for _, x := range xs {
		if x == n {
			return true
		}
	}
	return false
}

func TestSelect_OnlyOpenCounts(t *testing.T) {
	old := "2026-09-28T12:00:00Z"
	raw := []byte("[" + strings.Join([]string{
		alertJSON(5, "fixed", old), alertJSON(6, "dismissed", old),
		alertJSON(7, "auto_dismissed", old), alertJSON(10, "other", old), alertJSON(11, "open", old),
	}, ",") + "]")
	if got := numbers(mustSelect(t, raw, now)); !reflect.DeepEqual(got, []int{11}) {
		t.Fatalf("got %v, want only the open #11", got)
	}
}

func TestSelect_SeverityDoesNotMatter(t *testing.T) {
	got := mustSelect(t, fixture(t), now)
	sev := map[int]string{}
	for _, a := range got {
		sev[a.Number] = a.Severity
	}
	if sev[8] != "low" || sev[9] != "critical" {
		t.Fatalf("severities %v", sev)
	}
}

func TestSelect_SortedByNumber(t *testing.T) {
	got := numbers(mustSelect(t, fixture(t), now))
	if want := []int{3, 4, 8, 9}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSelect_EmptyArray(t *testing.T) {
	got, err := Select([]byte("[]"), now, week)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestSelect_BadInputIsAnError(t *testing.T) {
	cases := []struct{ name, in, wantNum string }{
		{"object", `{"message":"Not Found"}`, ""},
		{"not json", `not json`, ""},
		{"missing created_at", `[{"number":42,"state":"open"}]`, "42"},
		{"bad created_at", `[{"number":43,"state":"open","created_at":"yesterday"}]`, "43"},
	}
	for _, c := range cases {
		_, err := Select([]byte(c.in), now, week)
		if err == nil {
			t.Errorf("%s: no error", c.name)
			continue
		}
		if c.wantNum != "" && !strings.Contains(err.Error(), "#"+c.wantNum) {
			t.Errorf("%s: error %q lacks the alert number", c.name, err)
		}
	}
}

func TestSelect_AgeDaysRoundsDown(t *testing.T) {
	age := map[int]int{}
	for _, a := range mustSelect(t, fixture(t), now) {
		age[a.Number] = a.AgeDays
	}
	if want := map[int]int{3: 7, 4: 8, 9: 8, 8: 30}; !reflect.DeepEqual(age, want) {
		t.Fatalf("got %v, want %v", age, want)
	}
}

func TestSelect_NullFieldsDoNotFail(t *testing.T) {
	const c = `"number":11,"state":"open","created_at":"2026-10-20T12:00:00Z","html_url":"u"`
	cases := []struct {
		name, in, pkg, ghsa string
	}{
		{"dependency", `{` + c + `,"dependency":null,"security_advisory":{"ghsa_id":"G","severity":"low"}}`, "", "G"},
		{"package", `{` + c + `,"dependency":{"package":null},"security_advisory":{"ghsa_id":"G","severity":"low"}}`, "", "G"},
		{"advisory", `{` + c + `,"dependency":{"package":{"name":"p"}},"security_advisory":null}`, "p", ""},
		{"severity", `{` + c + `,"dependency":{"package":{"name":"p"}},"security_advisory":{"ghsa_id":"G","severity":null}}`, "p", "G"},
	}
	for _, tc := range cases {
		got, err := Select([]byte("["+tc.in+"]"), now, week)
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: got %v, %v", tc.name, got, err)
		}
		a := got[0]
		wantSev := "unknown"
		if tc.name != "severity" && tc.name != "advisory" {
			wantSev = "low"
		}
		if a.Package != tc.pkg || a.GHSA != tc.ghsa || a.Severity != wantSev {
			t.Errorf("%s: got %+v", tc.name, a)
		}
		if tc.name == "advisory" || tc.name == "severity" {
			if !strings.Contains(Body(got, now, 7), "unknown") {
				t.Errorf("%s: body lacks unknown", tc.name)
			}
		}
	}
}

func TestBody_ListsEachAlert(t *testing.T) {
	var picked []Alert
	for _, a := range mustSelect(t, fixture(t), now) {
		if a.Number == 3 || a.Number == 4 {
			picked = append(picked, a)
		}
	}
	body := Body(picked, now, 7)
	for _, want := range []string{
		"#3", "pkg-three", "GHSA-aaaa-aaaa-0003", "medium", "dependabot/3", "| 7 |",
		"#4", "pkg-four", "GHSA-aaaa-aaaa-0004", "dependabot/4", "| 8 |",
		"open more than 7 days", "2026-10-28T12:00:00Z", "closes itself when no open alert is older than 7 days",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}

func TestBody_RowStartsWithAlertNumber(t *testing.T) {
	var picked []Alert
	for _, a := range mustSelect(t, fixture(t), now) {
		if a.Number == 3 || a.Number == 4 {
			picked = append(picked, a)
		}
	}
	lines := strings.Split(Body(picked, now, 7), "\n")
	for _, prefix := range []string{"| #3 | ", "| #4 | "} {
		found := false
		for _, l := range lines {
			found = found || strings.HasPrefix(l, prefix)
		}
		if !found {
			t.Errorf("no row starts with %q", prefix)
		}
	}
}

func runCLI(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func fixturePath() string { return filepath.Join("testdata", "alerts.json") }

const nowArg = "2026-10-28T12:00:00Z"

func TestRun_WritesBodyAndCount(t *testing.T) {
	body := filepath.Join(t.TempDir(), "aged.md")
	code, out, _ := runCLI("-alerts", fixturePath(), "-body", body, "-now", nowArg)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if last := lines[len(lines)-1]; last != "aged alerts: 4" {
		t.Fatalf("last line %q", last)
	}
	if b, err := os.ReadFile(body); err != nil || len(b) == 0 {
		t.Fatalf("body %q, %v", b, err)
	}
}

func TestRun_NothingCountedWritesEmptyBody(t *testing.T) {
	dir := t.TempDir()
	alerts := filepath.Join(dir, "a.json")
	in := "[" + alertJSON(1, "open", "2026-10-22T12:00:00Z") + "," + alertJSON(2, "fixed", "2026-09-28T12:00:00Z") + "]"
	if err := os.WriteFile(alerts, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	body := filepath.Join(dir, "aged.md")
	if err := os.WriteFile(body, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCLI("-alerts", alerts, "-body", body, "-now", nowArg)
	if code != 0 || !strings.Contains(out, "aged alerts: 0") {
		t.Fatalf("exit %d, out %q", code, out)
	}
	st, err := os.Stat(body)
	if err != nil || st.Size() != 0 {
		t.Fatalf("body stat %v, %v", st, err)
	}
}

func TestRun_DaysFlagMovesTheCut(t *testing.T) {
	body := filepath.Join(t.TempDir(), "aged.md")
	code, out, _ := runCLI("-alerts", fixturePath(), "-body", body, "-now", nowArg, "-days", "9")
	if code != 0 || !strings.Contains(out, "aged alerts: 1") {
		t.Fatalf("exit %d, out %q", code, out)
	}
}

func TestRun_BadCallExits2(t *testing.T) {
	body := filepath.Join(t.TempDir(), "aged.md")
	cases := map[string][]string{
		"no alerts":    {"-body", body},
		"missing file": {"-alerts", filepath.Join(t.TempDir(), "nope.json"), "-body", body},
		"no body":      {"-alerts", fixturePath()},
		"bad now":      {"-alerts", fixturePath(), "-body", body, "-now", "nope"},
		"bad days":     {"-alerts", fixturePath(), "-body", body, "-days", "0"},
	}
	for name, args := range cases {
		code, _, errOut := runCLI(args...)
		if code != 2 || errOut == "" {
			t.Errorf("%s: exit %d, stderr %q", name, code, errOut)
		}
	}
}
