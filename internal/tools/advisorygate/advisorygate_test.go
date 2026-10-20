package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var today = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

const okLog = `Scanned /w/go.mod file and found 107 packages
Scanned /w/sidecar/docling/requirements.txt file and found 9 packages
Scanned /w/pnpm-lock.yaml file and found 226 packages
`

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func noRating(string) (string, error) { return "", nil }

func idSet(found []Finding) []string {
	var out []string
	for _, f := range found {
		out = append(out, strings.Join(f.IDs, "/"))
	}
	sort.Strings(out)
	return out
}

func countedIDs(t *testing.T, raw []byte, lookup func(string) (string, error)) []string {
	t.Helper()
	found, _, err := Counted(raw, lookup)
	if err != nil {
		t.Fatal(err)
	}
	return idSet(found)
}

type grp struct {
	ids, aliases []string
	max          string
	uncalled     bool
}

// synth builds an osv-scanner document with one package; recs maps record id to severity.
func synth(recs map[string]string, groups ...grp) []byte {
	var vulns, gs []map[string]any
	for id, sev := range recs {
		v := map[string]any{"id": id}
		if sev != "" {
			v["database_specific"] = map[string]any{"severity": sev}
		}
		vulns = append(vulns, v)
	}
	for _, g := range groups {
		m := map[string]any{"ids": g.ids, "aliases": g.aliases, "max_severity": g.max}
		if g.uncalled {
			m["experimental_analysis"] = map[string]any{"x": map[string]any{"called": false}}
		}
		gs = append(gs, m)
	}
	b, _ := json.Marshal(map[string]any{"results": []any{map[string]any{
		"source": map[string]any{"path": "go.mod"},
		"packages": []any{map[string]any{
			"package":         map[string]any{"name": "p", "version": "1"},
			"groups":          gs,
			"vulnerabilities": vulns,
		}},
	}}})
	return b
}

func lookupAnswering(sev string, asked *[]string) func(string) (string, error) {
	return func(id string) (string, error) {
		*asked = append(*asked, id)
		return sev, nil
	}
}

func TestCounted_PreDeps04FixtureNamesTheSixHighs(t *testing.T) {
	got := countedIDs(t, fixture(t, "osv-pre-deps04.json"), noRating)
	want := []string{
		"GO-2026-6348/GHSA-vp52-pcj8-j9qc", "GO-2026-6443/GHSA-2v4p-qf9q-27wj",
		"GHSA-28wg-ghj8-5hjv", "GHSA-2v37-7h3g-55p8", "GHSA-r28c-9q8g-f849", "GHSA-68fv-2mgg-jv7q",
	}
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	for _, g := range got {
		if strings.Contains(g, "qc2q") || strings.Contains(g, "fxqj") {
			t.Fatalf("moderate counted: %s", g)
		}
	}
}

func TestCounted_GHSAModerateBeatsHighCVSS(t *testing.T) {
	raw := synth(map[string]string{"GHSA-9jxx-vjrv-h2rq": "MODERATE"},
		grp{ids: []string{"PYSEC-2026-4192", "GHSA-9jxx-vjrv-h2rq"}, max: "7.8"})
	if got := countedIDs(t, raw, noRating); len(got) != 0 {
		t.Fatalf("counted %v", got)
	}
	aliased := synth(map[string]string{"GHSA-9jxx-vjrv-h2rq": "MODERATE"},
		grp{ids: []string{"PYSEC-2026-4192"}, aliases: []string{"GHSA-9jxx-vjrv-h2rq"}, max: "7.8"})
	if got := countedIDs(t, aliased, noRating); len(got) != 0 {
		t.Fatalf("alias rating ignored: counted %v", got)
	}
	head := fixture(t, "osv-head.json")
	if !bytes.Contains(head, []byte("PYSEC-2026-4192")) {
		t.Fatal("head fixture lost the docling group")
	}
}

func TestCounted_TorchPYSECOnlyHighsCount(t *testing.T) {
	got := strings.Join(countedIDs(t, fixture(t, "osv-torch-2.9.1.json"), noRating), " ")
	for _, want := range []string{"PYSEC-2026-139", "PYSEC-2026-2286"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s not counted in %s", want, got)
		}
	}
	for _, not := range []string{"qfhq", "rrmf"} {
		if strings.Contains(got, not) {
			t.Errorf("LOW %s counted", not)
		}
	}
}

func TestCounted_CVSSBoundary(t *testing.T) {
	for max, want := range map[string]bool{"7.0": true, "6.9": false, "": false, "10.0": true} {
		raw := synth(nil, grp{ids: []string{"PYSEC-1"}, max: max})
		if got := len(countedIDs(t, raw, noRating)) == 1; got != want {
			t.Errorf("max_severity %q counted=%v want %v", max, got, want)
		}
	}
}

func TestCounted_CriticalAndCaseInsensitive(t *testing.T) {
	raw := synth(map[string]string{"GHSA-a": "CRITICAL", "GHSA-b": "high"},
		grp{ids: []string{"GHSA-a"}, max: "0"}, grp{ids: []string{"GHSA-b"}, max: "0"})
	if got := countedIDs(t, raw, noRating); len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	mixed := synth(map[string]string{"GHSA-m": "MODERATE", "GHSA-h": "HIGH"},
		grp{ids: []string{"PYSEC-1"}, aliases: []string{"GHSA-m", "GHSA-h"}, max: "0"})
	if got := countedIDs(t, mixed, noRating); len(got) != 1 {
		t.Fatalf("a HIGH rating after a MODERATE one must count, got %v", got)
	}
}

func TestCounted_UnratedGHSARecordFallsBackToCVSS(t *testing.T) {
	for max, want := range map[string]bool{"8.1": true, "5.0": false} {
		raw := synth(map[string]string{"GHSA-a": ""}, grp{ids: []string{"GHSA-a"}, max: max})
		if got := len(countedIDs(t, raw, noRating)) == 1; got != want {
			t.Errorf("max %s counted=%v want %v", max, got, want)
		}
	}
}

func TestCounted_GoOnlyNoSeverityDoesNotCount(t *testing.T) {
	found, _, err := Counted(fixture(t, "osv-head.json"), noRating)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		if strings.HasPrefix(f.IDs[0], "GO-") {
			t.Errorf("GO-only counted: %v", f)
		}
	}
}

func TestCounted_CalledFalseStillCounts(t *testing.T) {
	raw := synth(map[string]string{"GHSA-a": "HIGH"}, grp{ids: []string{"GHSA-a"}, uncalled: true})
	if got := countedIDs(t, raw, noRating); len(got) != 1 {
		t.Fatalf("got %v", got)
	}
}

func TestCounted_NonNumericMaxSeverityErrors(t *testing.T) {
	if _, _, err := Counted(synth(nil, grp{ids: []string{"PYSEC-1"}, max: "abc"}), noRating); err == nil {
		t.Fatal("want an error")
	}
}

func TestCounted_MalformedJSONErrors(t *testing.T) {
	if _, _, err := Counted([]byte(`{"results":`), noRating); err == nil {
		t.Fatal("want an error")
	}
}

func unrecorded(max string) []byte {
	return synth(nil, grp{ids: []string{"PYSEC-1"}, aliases: []string{"GHSA-aaaa-aaaa-aaaa"}, max: max})
}

func TestCounted_LookupHighCountsWithoutRecord(t *testing.T) {
	var asked []string
	got := countedIDs(t, unrecorded(""), lookupAnswering("HIGH", &asked))
	if len(got) != 1 || fmt.Sprint(asked) != "[GHSA-aaaa-aaaa-aaaa]" {
		t.Fatalf("got %v asked %v", got, asked)
	}
}

func TestCounted_LookupModerateBeatsCVSS(t *testing.T) {
	var asked []string
	if got := countedIDs(t, unrecorded("8.1"), lookupAnswering("MODERATE", &asked)); len(got) != 0 {
		t.Fatalf("counted %v", got)
	}
}

func TestCounted_LookupEmptyFallsToCVSS(t *testing.T) {
	for max, want := range map[string]bool{"7.5": true, "": false} {
		var asked []string
		if got := len(countedIDs(t, unrecorded(max), lookupAnswering("", &asked))) == 1; got != want {
			t.Errorf("max %q counted=%v want %v", max, got, want)
		}
	}
}

func TestCounted_LookupSkippedWhenRecordPresent(t *testing.T) {
	raw := synth(map[string]string{"GHSA-aaaa-aaaa-aaaa": "LOW"},
		grp{ids: []string{"PYSEC-1"}, aliases: []string{"GHSA-aaaa-aaaa-aaaa"}, max: "9.0"})
	var asked []string
	if got := countedIDs(t, raw, lookupAnswering("HIGH", &asked)); len(got) != 0 || len(asked) != 0 {
		t.Fatalf("counted %v asked %v", got, asked)
	}
}

func TestCounted_LookupErrorIsError(t *testing.T) {
	_, _, err := Counted(unrecorded("9.0"), func(string) (string, error) { return "", errors.New("boom") })
	if err == nil {
		t.Fatal("want an error")
	}
}

func TestCounted_EpicHeadFixtureAsksOnlyForUnrecordedGHSAs(t *testing.T) {
	raw := fixture(t, "osv-head.json")
	var asked []string
	lookup := func(id string) (string, error) {
		asked = append(asked, id)
		if id == "GHSA-gcjh-h69q-9w9g" {
			return "MODERATE", nil
		}
		return "", nil
	}
	found, _, err := Counted(raw, lookup)
	if err != nil || len(found) != 0 {
		t.Fatalf("found %v err %v", found, err)
	}
	sort.Strings(asked)
	if fmt.Sprint(asked) != "[GHSA-259r-337f-4rfw GHSA-gcjh-h69q-9w9g GHSA-h42j-9hcc-3cwc]" {
		t.Fatalf("asked %v", asked)
	}
	var d osvDoc
	json.Unmarshal(raw, &d)
	for _, r := range d.Results {
		for _, p := range r.Packages {
			for _, v := range p.Vulnerabilities {
				for _, a := range asked {
					if v.ID == a {
						t.Errorf("asked for %s, which has a record", a)
					}
				}
			}
		}
	}
}

func TestCounted_EpicHeadFixtureIsZero(t *testing.T) {
	found, total, err := Counted(fixture(t, "osv-head.json"), noRating)
	if err != nil || len(found) != 0 || total == 0 {
		t.Fatalf("found %v total %d err %v", found, total, err)
	}
}

// stub is an OSV API double; ids without an answer get 404.
type stub struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen []string
}

type answer struct {
	status int
	sev    string
}

func newStub(t *testing.T, answers map[string]answer) *stub {
	t.Helper()
	s := &stub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.seen = append(s.seen, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		a, ok := answers[strings.TrimPrefix(r.URL.Path, "/v1/vulns/")]
		if !ok {
			a = answer{status: 404}
		}
		w.WriteHeader(a.status)
		if a.status == 200 {
			fmt.Fprintf(w, `{"database_specific":{"severity":%q}}`, a.sev)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// fakeScanner writes a bash scanner that records argv and pwd, copies outJSON to --output-file
// (when non-empty), prints log (default okLog) to stderr and exits exit.
func fakeScanner(t *testing.T, exit int, outJSON, log string) string {
	return fakeScannerOn(t, exit, outJSON, log, "2")
}

func fakeScannerOn(t *testing.T, exit int, outJSON, log, fd string) string {
	t.Helper()
	dir := t.TempDir()
	if log == "" {
		log = okLog
	}
	if err := os.WriteFile(filepath.Join(dir, "log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	if outJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "out.json"), []byte(outJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := fmt.Sprintf(`#!/bin/bash
d=$(dirname "$0")
printf '%%s\n' "$@" > "$d/argv"
pwd > "$d/pwd"
prev=""
for a in "$@"; do
  if [ "$prev" = "--output-file" ] && [ -f "$d/out.json" ]; then cp "$d/out.json" "$a"; fi
  prev=$a
done
cat "$d/log" >&%s
exit %d
`, fd, exit)
	path := filepath.Join(dir, "osv-scanner")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func gate(t *testing.T, osv, root string, api *stub) (code int, out, errOut string) {
	t.Helper()
	if api == nil {
		api = newStub(t, nil)
	}
	var o, e bytes.Buffer
	code = Run(osv, root, api.srv.URL, today, &o, &e)
	return code, o.String(), e.String()
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func TestReport_NamesIDPackageVersion(t *testing.T) {
	osv := fakeScanner(t, 1, string(fixture(t, "osv-pre-deps04.json")), "")
	_, out, _ := gate(t, osv, t.TempDir(), nil)
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "counted: ") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 6 {
		t.Fatalf("want 6 counted lines, got:\n%s", out)
	}
	want := []struct{ id, pkg, manifest string }{
		{"GHSA-vp52-pcj8-j9qc", "google.golang.org/grpc@1.82.1", "go.mod"},
		{"GHSA-2v4p-qf9q-27wj", "google.golang.org/grpc@1.82.1", "go.mod"},
		{"GHSA-28wg-ghj8-5hjv", "nanoid@3.3.15", "pnpm-lock.yaml"},
		{"GHSA-2v37-7h3g-55p8", "nanoid@3.3.15", "pnpm-lock.yaml"},
		{"GHSA-r28c-9q8g-f849", "postcss@8.5.16", "pnpm-lock.yaml"},
		{"GHSA-68fv-2mgg-jv7q", "source-map-js@1.2.1", "pnpm-lock.yaml"},
	}
	for _, w := range want {
		found := false
		for _, l := range lines {
			found = found || (strings.Contains(l, w.id) && strings.Contains(l, w.pkg) && strings.Contains(l, "("+w.manifest+")"))
		}
		if !found {
			t.Errorf("no line with %v in:\n%s", w, out)
		}
	}
}

func TestReport_SummaryLine(t *testing.T) {
	for fx, re := range map[string]string{
		"osv-head.json":       `^advisories: 0 high or critical findings of \d+ total$`,
		"osv-pre-deps04.json": `^advisories: 6 high or critical findings of \d+ total$`,
	} {
		osv := fakeScanner(t, 1, string(fixture(t, fx)), "")
		_, out, _ := gate(t, osv, t.TempDir(), nil)
		if last := lastLine(out); !regexp.MustCompile(re).MatchString(last) {
			t.Errorf("%s: last line %q", fx, last)
		}
	}
}

func TestRun_ScannerArgv(t *testing.T) {
	osv := fakeScanner(t, 1, string(fixture(t, "osv-pre-deps04.json")), "")
	root := t.TempDir()
	code, _, _ := gate(t, osv, root, nil)
	if code != 1 {
		t.Fatalf("code %d: the gate did not read the --output-file", code)
	}
	raw, _ := os.ReadFile(filepath.Join(filepath.Dir(osv), "argv"))
	argv := strings.Split(strings.TrimSpace(string(raw)), "\n")
	has := func(want ...string) {
		for i := range argv {
			if i+len(want) <= len(argv) && fmt.Sprint(argv[i:i+len(want)]) == fmt.Sprint(want) {
				return
			}
		}
		t.Errorf("argv lacks %v: %v", want, argv)
	}
	for _, f := range []string{"--no-resolve", "--all-vulns", "--no-call-analysis=go"} {
		has(f)
	}
	has("--format", "json")
	has("--output-file", argv[indexOf(argv, "--output-file")+1])
	has("-L", "go.mod")
	has("-L", "sidecar/docling/requirements.txt")
	has("-L", "pnpm-lock.yaml")
	for _, a := range argv {
		if strings.HasPrefix(a, "--config") {
			t.Errorf("argv has %s", a)
		}
	}
	pwd, _ := os.ReadFile(filepath.Join(filepath.Dir(osv), "pwd"))
	gotPwd, _ := filepath.EvalSymlinks(strings.TrimSpace(string(pwd)))
	wantPwd, _ := filepath.EvalSymlinks(root)
	if gotPwd != wantPwd {
		t.Errorf("scanner ran in %s, want %s", gotPwd, wantPwd)
	}
}

func indexOf(s []string, v string) int {
	for i := range s {
		if s[i] == v {
			return i
		}
	}
	return -1
}

func TestRun_ScannerExit127IsToolError(t *testing.T) {
	for _, exit := range []int{2, 127} {
		osv := fakeScanner(t, exit, `{"results":[]}`, "")
		code, _, errOut := gate(t, osv, t.TempDir(), nil)
		if code != 2 || !strings.Contains(errOut, fmt.Sprint(exit)) {
			t.Fatalf("exit %d: code %d stderr %q", exit, code, errOut)
		}
	}
}

func TestRun_ScannerWritesNoFileIsToolError(t *testing.T) {
	if code, _, _ := gate(t, fakeScanner(t, 0, "", ""), t.TempDir(), nil); code != 2 {
		t.Fatalf("code %d", code)
	}
}

func TestRun_Exit1WithModerateOnlyPasses(t *testing.T) {
	osv := fakeScanner(t, 1, string(fixture(t, "osv-head.json")), "")
	if code, out, _ := gate(t, osv, t.TempDir(), nil); code != 0 {
		t.Fatalf("code %d\n%s", code, out)
	}
}

func TestRun_Exit1WithHighFails(t *testing.T) {
	osv := fakeScanner(t, 1, string(fixture(t, "osv-pre-deps04.json")), "")
	if code, _, _ := gate(t, osv, t.TempDir(), nil); code != 1 {
		t.Fatalf("code %d", code)
	}
}

func TestRun_Exit0EmptyResultsPasses(t *testing.T) {
	code, out, _ := gate(t, fakeScanner(t, 0, `{"results":[]}`, ""), t.TempDir(), nil)
	if code != 0 || lastLine(out) != "advisories: 0 high or critical findings of 0 total" {
		t.Fatalf("code %d\n%s", code, out)
	}
}

func TestRun_PassesScannerLogThrough(t *testing.T) {
	osv := fakeScanner(t, 0, `{"results":[]}`, okLog+"Loaded filter from: x\n")
	if _, out, _ := gate(t, osv, t.TempDir(), nil); !strings.Contains(out, "Loaded filter from: x") {
		t.Fatalf("stdout lacks the scanner line:\n%s", out)
	}
}

func writeToml(t *testing.T, root, rel, body string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func entry(until string) string {
	return "[[IgnoredVulns]]\nid = \"GHSA-x\"\nreason = \"r\"\nignoreUntil = " + until + "\n"
}

func problemsOf(t *testing.T, body string) []string {
	t.Helper()
	root := t.TempDir()
	writeToml(t, root, "osv-scanner.toml", body)
	p, err := IgnoreProblems(root, today)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestIgnore_ThirtyDayBoundary(t *testing.T) {
	if p := problemsOf(t, entry("2026-11-19")); len(p) != 0 {
		t.Errorf("day 30: %v", p)
	}
	p := problemsOf(t, entry("2026-11-20"))
	if len(p) != 1 || !strings.Contains(p[0], "2026-11-20") {
		t.Errorf("day 31: %v", p)
	}
}

func TestIgnore_TimestampUsesUTCCalendarDate(t *testing.T) {
	for _, ok := range []string{"2026-11-19T12:00:00Z", "2026-11-19T23:59:59Z", "2026-11-20T01:00:00+05:00"} {
		if p := problemsOf(t, entry(ok)); len(p) != 0 {
			t.Errorf("%s: %v", ok, p)
		}
	}
	for _, bad := range []string{"2026-11-20T00:00:00Z", "2026-11-19T23:00:00-05:00"} {
		if p := problemsOf(t, entry(bad)); len(p) != 1 {
			t.Errorf("%s: %v", bad, p)
		}
	}
}

func TestIgnore_MissingFieldsFail(t *testing.T) {
	cases := map[string]string{
		"no reason":    "[[IgnoredVulns]]\nid = \"X\"\nignoreUntil = 2026-11-01\n",
		"empty reason": "[[IgnoredVulns]]\nid = \"X\"\nreason = \"\"\nignoreUntil = 2026-11-01\n",
		"no id":        "[[IgnoredVulns]]\nreason = \"r\"\nignoreUntil = 2026-11-01\n",
		"no until":     "[[IgnoredVulns]]\nid = \"X\"\nreason = \"r\"\n",
		"empty id":     "[[IgnoredVulns]]\nid = \"\"\nreason = \"r\"\nignoreUntil = 2026-11-01\n",
		"blank reason": "[[IgnoredVulns]]\nid = \"X\"\nreason = \"  \"\nignoreUntil = 2026-11-01\n",
	}
	for name, body := range cases {
		p := problemsOf(t, body)
		if len(p) != 1 || !strings.Contains(p[0], "osv-scanner.toml:1:") {
			t.Errorf("%s: %v", name, p)
		}
	}
}

func TestIgnore_ExpiredEntryPasses(t *testing.T) {
	if p := problemsOf(t, entry("2026-10-01")); len(p) != 0 {
		t.Fatalf("%v", p)
	}
}

func TestIgnore_RealRepoRootHasNoProblems(t *testing.T) {
	p, err := IgnoreProblems("../../..", time.Now().UTC())
	if err != nil || len(p) != 0 {
		t.Fatalf("problems %v err %v", p, err)
	}
}

func TestIgnore_UnsupportedFormsFail(t *testing.T) {
	head := "[[IgnoredVulns]]\nid = \"X\"\nreason = \"r\"\n"
	cases := map[string]string{
		"quoted date":      head + "ignoreUntil = \"2026-11-01\"\n",
		"multiline string": "[[IgnoredVulns]]\nid = \"X\"\nreason = \"\"\"r\"\"\"\nignoreUntil = 2026-11-01\n",
		"unknown key":      head + "ignoreUntil = 2026-11-01\nfoo = \"x\"\n",
		"other table":      entry("2026-11-01") + "[[PackageOverrides]]\nname = \"x\"\n",
		"overrides table":  "[[PackageOverrides]]\nid = \"X\"\nreason = \"r\"\nignoreUntil = 2026-11-01\n",
		"key before table": "id = \"X\"\n" + entry("2026-11-01"),
		"duplicate key":    head + "reason = \"again\"\nignoreUntil = 2026-11-01\n",
	}
	for name, body := range cases {
		if p := problemsOf(t, body); len(p) == 0 {
			t.Errorf("%s: no problem", name)
		}
	}
}

func TestIgnore_WalkFindsNestedAndSkipsNodeModules(t *testing.T) {
	root := t.TempDir()
	bad := "[[IgnoredVulns]]\nid = \"X\"\n"
	writeToml(t, root, "a/b/osv-scanner.toml", bad)
	writeToml(t, root, "node_modules/x/osv-scanner.toml", bad)
	writeToml(t, root, ".claude/w/osv-scanner.toml", bad)
	writeToml(t, root, ".git/x/osv-scanner.toml", bad)
	writeToml(t, root, ".ralph/x/osv-scanner.toml", bad)
	p, err := IgnoreProblems(root, today)
	if err != nil || len(p) == 0 {
		t.Fatalf("problems %v err %v", p, err)
	}
	for _, line := range p {
		if !strings.Contains(line, filepath.Join("a", "b", "osv-scanner.toml")) {
			t.Errorf("problem from a skipped path: %s", line)
		}
	}
}

func TestIgnore_RootInsideSkippedDirIsStillWalked(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".claude")
	writeToml(t, root, "osv-scanner.toml", "[[IgnoredVulns]]\nid = \"X\"\n")
	p, err := IgnoreProblems(root, today)
	if err != nil || len(p) == 0 {
		t.Fatalf("a root named .claude was skipped: problems %v err %v", p, err)
	}
}

func TestRun_BadIgnoreFailsWithCleanScan(t *testing.T) {
	root := t.TempDir()
	writeToml(t, root, "osv-scanner.toml", "[[IgnoredVulns]]\nid = \"X\"\n")
	if code, _, _ := gate(t, fakeScanner(t, 0, `{"results":[]}`, ""), root, nil); code != 1 {
		t.Fatalf("code %d", code)
	}
}

func TestRun_ManifestLineMissingIsToolError(t *testing.T) {
	log := "Scanned /w/go.mod file and found 1 package\nScanned /w/sidecar/docling/requirements.txt file and found 9 packages\n"
	code, _, errOut := gate(t, fakeScanner(t, 0, `{"results":[]}`, log), t.TempDir(), nil)
	if code != 2 || !strings.Contains(errOut, "pnpm-lock.yaml") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

func TestRun_ManifestZeroPackagesIsToolError(t *testing.T) {
	log := strings.Replace(okLog, "requirements.txt file and found 9 packages", "requirements.txt file and found 0 packages", 1)
	code, _, errOut := gate(t, fakeScanner(t, 0, `{"results":[]}`, log), t.TempDir(), nil)
	if code != 2 || !strings.Contains(errOut, "sidecar/docling/requirements.txt") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

func TestRun_ScannedLinesOnStdoutAreRead(t *testing.T) {
	osv := fakeScannerOn(t, 0, `{"results":[]}`, "", "1")
	if code, _, errOut := gate(t, osv, t.TempDir(), nil); code != 0 {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

func TestRun_LookupHighOverHTTP(t *testing.T) {
	api := newStub(t, map[string]answer{"GHSA-aaaa-aaaa-aaaa": {200, "HIGH"}})
	osv := fakeScanner(t, 1, string(unrecorded("")), "")
	if code, out, _ := gate(t, osv, t.TempDir(), api); code != 1 {
		t.Fatalf("code %d\n%s", code, out)
	}
	if fmt.Sprint(api.seen) != "[GET /v1/vulns/GHSA-aaaa-aaaa-aaaa]" {
		t.Fatalf("server saw %v", api.seen)
	}
}

func TestRun_Lookup404IsNoRating(t *testing.T) {
	for max, want := range map[string]int{"": 0, "8.1": 1} {
		osv := fakeScanner(t, 1, string(unrecorded(max)), "")
		if code, _, _ := gate(t, osv, t.TempDir(), nil); code != want {
			t.Errorf("max %q: code %d want %d", max, code, want)
		}
	}
}

func TestRun_LookupCachesPerRunAndReadsLowerCase(t *testing.T) {
	api := newStub(t, map[string]answer{"GHSA-aaaa-aaaa-aaaa": {200, "high"}})
	raw := synth(nil,
		grp{ids: []string{"PYSEC-1"}, aliases: []string{"GHSA-aaaa-aaaa-aaaa"}},
		grp{ids: []string{"PYSEC-2"}, aliases: []string{"GHSA-aaaa-aaaa-aaaa"}})
	code, out, _ := gate(t, fakeScanner(t, 1, string(raw), ""), t.TempDir(), api)
	if code != 1 || len(api.seen) != 1 || !strings.Contains(out, "findings of 2 total") {
		t.Fatalf("code %d seen %v\n%s", code, api.seen, out)
	}
}

func TestRun_LookupTransportErrorIsToolError(t *testing.T) {
	api := newStub(t, nil)
	api.srv.Close()
	code, _, errOut := gate(t, fakeScanner(t, 1, string(unrecorded("")), ""), t.TempDir(), api)
	if code != 2 || !strings.Contains(errOut, "GHSA-aaaa-aaaa-aaaa") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

func TestRun_Lookup500IsToolError(t *testing.T) {
	api := newStub(t, map[string]answer{"GHSA-aaaa-aaaa-aaaa": {500, ""}})
	osv := fakeScanner(t, 1, string(unrecorded("")), "")
	code, _, errOut := gate(t, osv, t.TempDir(), api)
	if code != 2 || !strings.Contains(errOut, "GHSA-aaaa-aaaa-aaaa") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

func TestRun_EpicHeadWithStubAPIIsClean(t *testing.T) {
	api := newStub(t, map[string]answer{"GHSA-gcjh-h69q-9w9g": {200, "MODERATE"}})
	osv := fakeScanner(t, 1, string(fixture(t, "osv-head.json")), "")
	if code, out, _ := gate(t, osv, t.TempDir(), api); code != 0 {
		t.Fatalf("code %d\n%s", code, out)
	}
}

func fastBackoff(t *testing.T) {
	old := lookupBackoff
	lookupBackoff = time.Millisecond
	t.Cleanup(func() { lookupBackoff = old })
}

// flaky answers the first n requests with status, then 200 HIGH.
func flaky(t *testing.T, n, status int) (*httptest.Server, *int) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits <= n {
			w.WriteHeader(status)
			return
		}
		fmt.Fprint(w, `{"database_specific":{"severity":"HIGH"}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestLookup_RetriesTransientStatus(t *testing.T) {
	fastBackoff(t)
	for _, st := range []int{429, 503} {
		srv, hits := flaky(t, 2, st)
		sev, err := newLookup(srv.URL)("GHSA-aaaa-aaaa-aaaa")
		if err != nil || sev != "HIGH" || *hits != 3 {
			t.Errorf("status %d: sev %q err %v hits %d", st, sev, err, *hits)
		}
	}
}

func TestLookup_FailsClosedAfterLastAttempt(t *testing.T) {
	fastBackoff(t)
	srv, hits := flaky(t, 99, 503)
	if _, err := newLookup(srv.URL)("GHSA-aaaa-aaaa-aaaa"); err == nil || *hits != 3 {
		t.Fatalf("err %v hits %d", err, *hits)
	}
}

func TestLookup_Permanent4xxIsNotRetried(t *testing.T) {
	fastBackoff(t)
	srv, hits := flaky(t, 99, 403)
	if _, err := newLookup(srv.URL)("GHSA-aaaa-aaaa-aaaa"); err == nil || *hits != 1 {
		t.Fatalf("err %v hits %d", err, *hits)
	}
}

func TestLookup_NotFoundIsNotRetried(t *testing.T) {
	fastBackoff(t)
	srv, hits := flaky(t, 99, 404)
	if sev, err := newLookup(srv.URL)("GHSA-aaaa-aaaa-aaaa"); err != nil || sev != "" || *hits != 1 {
		t.Fatalf("sev %q err %v hits %d", sev, err, *hits)
	}
}

func TestRun_AbsoluteManifestPathWithRelativeRootPrintsRelative(t *testing.T) {
	abs, err := filepath.Abs("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.Replace(string(fixture(t, "osv-pre-deps04.json")), `"path": "go.mod"`, `"path": "`+abs+`"`, -1)
	if raw == string(fixture(t, "osv-pre-deps04.json")) {
		t.Skip("fixture has no go.mod source path to rewrite")
	}
	_, out, _ := gate(t, fakeScanner(t, 1, raw, ""), ".", nil)
	if strings.Contains(out, abs) || !strings.Contains(out, "(go.mod)") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestCounted_NoLookupAfterHighRecord(t *testing.T) {
	raw := synth(map[string]string{"GHSA-aaaa-aaaa-aaaa": "HIGH"},
		grp{ids: []string{"GHSA-aaaa-aaaa-aaaa"}, aliases: []string{"GHSA-bbbb-bbbb-bbbb"}})
	var asked []string
	if got := countedIDs(t, raw, lookupAnswering("LOW", &asked)); len(got) != 1 || len(asked) != 0 {
		t.Fatalf("counted %v asked %v", got, asked)
	}
}

func TestCounted_NoLookupAfterHighLookup(t *testing.T) {
	raw := synth(nil, grp{ids: []string{"GHSA-aaaa-aaaa-aaaa"}, aliases: []string{"GHSA-bbbb-bbbb-bbbb"}})
	var asked []string
	if got := countedIDs(t, raw, lookupAnswering("CRITICAL", &asked)); len(got) != 1 || len(asked) != 1 {
		t.Fatalf("counted %v asked %v", got, asked)
	}
}

func TestIgnore_HeaderTrailingCommentIsAccepted(t *testing.T) {
	body := "[[IgnoredVulns]]  # docling\nid = \"X\"\nreason = \"r\"\nignoreUntil = 2026-11-01\n"
	if p := problemsOf(t, body); len(p) != 0 {
		t.Fatalf("%v", p)
	}
}

func init() { lookupBackoff = time.Millisecond }

func TestCounted_HighLookupAfterModerateLookupStillCounts(t *testing.T) {
	raw := synth(nil, grp{ids: []string{"PYSEC-1"}, aliases: []string{"GHSA-aaaa-aaaa-aaaa", "GHSA-bbbb-bbbb-bbbb"}, max: "0"})
	answers := map[string]string{"GHSA-aaaa-aaaa-aaaa": "MODERATE", "GHSA-bbbb-bbbb-bbbb": "HIGH"}
	lookup := func(id string) (string, error) { return answers[id], nil }
	if got := countedIDs(t, raw, lookup); len(got) != 1 {
		t.Fatalf("a HIGH lookup after a MODERATE one must count, got %v", got)
	}
}
