// fleetgate_test.go: EXTR-17-06. The fleet size is written in prose in a dozen
// places across workflows, docs and CI scripts; each one goes stale silently
// the moment the fleet changes. This derives the size from dev-env.yml's
// deploy topology and fails on every site that disagrees.
//
// Test-only, no main.go: like internal/tools/dockerignoregate (the precedent
// for this shape) it needs no runtime arguments, so plain `go test ./...`
// reaches it with no ci.yml wiring.
package fleetgate

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	devEnvRel = ".github/workflows/dev-env.yml"
	ciRel     = ".github/workflows/ci.yml"

	// This gate's own source is scanned for correctness but excluded from the
	// population floor -- its allowlist and comments would otherwise inflate
	// the floor until it stopped meaning anything.
	selfDir = "internal/tools/fleetgate/"
)

// The four trees the detection command walks:
//
//	grep -rnE '\b(13|14)\b' \
//	  .github/workflows docs scripts/ci internal/tools \
//	  | grep -iE 'service'
//
// Split across three lines on purpose: written as one line this comment
// carries both halves of the pattern and becomes a hit in its own scan.
var scanTrees = []string{".github/workflows", "docs", "scripts/ci", "internal/tools"}

// The two halves of that command, kept on separate lines so neither line is
// itself a hit.
var (
	countRe   = regexp.MustCompile(`\b(13|14)\b`)
	subjectRe = regexp.MustCompile(`(?i)service`)
)

// Population floors. Measured at plan time: 12 hits across 5 files, none of
// which this subtask deletes, so this leaves 2 hits of headroom.
const (
	minHits  = 10
	minFiles = 5
)

// allowEntry keys on a line substring, never a line number -- a line-keyed
// entry drifts on the next edit above it and turns into recurring red.
type allowEntry struct {
	File         string
	LineContains string
	Why          string
}

var allowlist = []allowEntry{
	{
		File:         "scripts/ci/railway-env.sh",
		LineContains: "APPR-14-03",
		Why:          "the digits belong to a story id, not a fleet count",
	},
}

type hit struct {
	File   string
	Line   int
	Text   string
	Counts []int
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		t.Fatal("git reported an empty worktree root")
	}
	return root
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// scanUnder walks trees below root and returns every line carrying a fleet
// count. Exported behaviour is the detection command's, verbatim.
func scanUnder(root string, trees []string) ([]hit, error) {
	var hits []hit
	for _, tree := range trees {
		err := filepath.WalkDir(filepath.Join(root, tree), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			for i, line := range strings.Split(string(raw), "\n") {
				found := countRe.FindAllString(line, -1)
				if len(found) == 0 || !subjectRe.MatchString(line) {
					continue
				}
				counts := make([]int, 0, len(found))
				for _, m := range found {
					n, err := strconv.Atoi(m)
					if err != nil {
						return err
					}
					counts = append(counts, n)
				}
				hits = append(hits, hit{File: rel, Line: i + 1, Text: strings.TrimSpace(line), Counts: counts})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return hits, nil
}

func scanRepo(t *testing.T) []hit {
	t.Helper()
	hits, err := scanUnder(repoRoot(t), scanTrees)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return hits
}

func allowed(h hit) bool {
	for _, e := range allowlist {
		if h.File == e.File && strings.Contains(h.Text, e.LineContains) {
			return true
		}
	}
	return false
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// --- dev-env.yml topology parsing ---

var (
	expectedJSONRe = regexp.MustCompile(`expected_json='\[([^\]]*)\]'`)
	matrixListRe   = regexp.MustCompile(`^\s+service:\s*\[([^\]]*)\]\s*$`)
	quotedRe       = regexp.MustCompile(`"([^"]*)"`)
	jobHeadRe      = regexp.MustCompile(`^  [a-zA-Z]`)
)

// expectedJSONNames returns the names in dev-env.yml's expected_json literal.
func expectedJSONNames(t *testing.T, content string) []string {
	t.Helper()
	m := expectedJSONRe.FindStringSubmatch(content)
	if m == nil {
		t.Fatalf("%s: no expected_json='[...]' literal -- the gate has nothing to derive the fleet from", devEnvRel)
	}
	var out []string
	for _, q := range quotedRe.FindAllStringSubmatch(m[1], -1) {
		out = append(out, q[1])
	}
	return out
}

// matrixList returns the `service: [...]` matrix of the named job.
func matrixList(t *testing.T, content, job string) []string {
	t.Helper()
	lines := strings.Split(content, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimRight(l, " \t\r") == "  "+job+":" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s: no `%s:` job -- the topology this gate derives the fleet from is gone", devEnvRel, job)
	}
	for i := start + 1; i < len(lines); i++ {
		if jobHeadRe.MatchString(lines[i]) {
			break
		}
		if m := matrixListRe.FindStringSubmatch(lines[i]); m != nil {
			var out []string
			for _, f := range strings.Split(m[1], ",") {
				if f = strings.TrimSpace(f); f != "" {
					out = append(out, f)
				}
			}
			return out
		}
	}
	t.Fatalf("%s: job `%s` has no `service: [...]` matrix", devEnvRel, job)
	return nil
}

func hasJob(content, job string) bool {
	for _, l := range strings.Split(content, "\n") {
		if strings.TrimRight(l, " \t\r") == "  "+job+":" {
			return true
		}
	}
	return false
}

// --- AC-1 ---

func TestDevEnv_ExpectedJSONNamesEveryDeployedService(t *testing.T) {
	content := readFile(t, filepath.Join(repoRoot(t), devEnvRel))

	expected := expectedJSONNames(t, content)
	ctx := matrixList(t, content, "deploy-context")
	spas := matrixList(t, content, "deploy-spas")
	if !hasJob(content, "deploy-gateway") {
		t.Fatalf("%s: no `deploy-gateway:` job -- `gateway` can no longer be assumed deployed", devEnvRel)
	}
	// Empty-collection guard: every set comparison below is vacuously true on
	// an empty list, and that reads as "the whole fleet agrees".
	if len(expected) == 0 || len(ctx) == 0 || len(spas) == 0 {
		t.Fatalf("%s: parsed %d expected_json name(s), %d deploy-context, %d deploy-spas -- nothing to compare",
			devEnvRel, len(expected), len(ctx), len(spas))
	}

	deployed := append([]string{"gateway"}, ctx...)
	deployed = append(deployed, spas...)

	for _, name := range deployed {
		if !contains(expected, name) {
			t.Errorf("%s: `%s` is deployed but expected_json does not name it -- the Watch-Paths assertion never sees it", devEnvRel, name)
		}
	}
	for _, name := range expected {
		if !contains(deployed, name) {
			t.Errorf("%s: expected_json names `%s` but no job deploys it", devEnvRel, name)
		}
	}
	// The fleet size is derived, never typed twice.
	if len(expected) != 1+len(ctx)+len(spas) {
		t.Errorf("%s: len(expected_json)=%d but the topology deploys 1+%d+%d=%d",
			devEnvRel, len(expected), len(ctx), len(spas), 1+len(ctx)+len(spas))
	}

	if !contains(expected, "docling") {
		t.Errorf("%s: expected_json omits `docling` -- the sidecar can vanish from the environment and the fleet gate stays green", devEnvRel)
	}
	if !contains(ctx, "docling") {
		t.Errorf("%s: the deploy-context matrix omits `docling` -- nothing ships the sidecar to the environment under test", devEnvRel)
	}
	if contains(spas, "docling") {
		t.Errorf("%s: the deploy-spas matrix names `docling` -- it is a backend sidecar, not a static front end", devEnvRel)
	}
}

// --- AC-2 ---

func TestFleetGate_EveryCountSiteAgreesWithExpectedJSON(t *testing.T) {
	content := readFile(t, filepath.Join(repoRoot(t), devEnvRel))
	expected := expectedJSONNames(t, content)
	if len(expected) == 0 {
		t.Fatalf("%s: expected_json parsed to zero names -- there is no fleet size to arbitrate with", devEnvRel)
	}
	want := len(expected)

	// The fleet this gate arbitrates must be the one that includes the
	// sidecar. Otherwise every site below agrees on the wrong number and the
	// agreement proves nothing.
	if !contains(expected, "docling") {
		t.Errorf("%s: expected_json omits `docling`, so want=%d is the pre-sidecar fleet -- agreement with it is not evidence", devEnvRel, want)
	}

	hits := scanRepo(t)
	if len(hits) == 0 {
		t.Fatalf("the scan found no fleet-count site at all -- the detection pattern or the trees have drifted, and a clean run means nothing")
	}

	checked := 0
	for _, h := range hits {
		if allowed(h) {
			continue
		}
		checked++
		for _, got := range h.Counts {
			if got != want {
				t.Errorf("%s:%d: got %d want %d -- %s", h.File, h.Line, got, want, h.Text)
			}
		}
	}
	if checked == 0 {
		t.Fatalf("all %d hit(s) were allowlisted -- the allowlist has swallowed the population it was meant to carve one line out of", len(hits))
	}
}

// --- AC-3 ---

func TestFleetGate_ScanPopulationMeetsItsFloor(t *testing.T) {
	hits := scanRepo(t)

	files := map[string]bool{}
	n := 0
	for _, h := range hits {
		if strings.HasPrefix(h.File, selfDir) {
			continue
		}
		files[h.File] = true
		n++
	}

	if n < minHits {
		t.Errorf("scanned %d fleet-count line(s), floor %d -- a run reporting no disagreement here is indistinguishable from a run that scanned nothing", n, minHits)
	}
	if len(files) < minFiles {
		t.Errorf("fleet-count lines found in %d file(s), floor %d -- the walk has stopped reaching part of the tree", len(files), minFiles)
	}
	t.Logf("population: %d line(s) across %d file(s), this gate's own source excluded", n, len(files))
}

func TestFleetGate_FindsAPlantedControlNeedle(t *testing.T) {
	root := t.TempDir()
	tree := filepath.Join(root, "docs")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Two decoy lines that must not match, then the needle on line 2 of the
	// second file.
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(tree, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// `subject` is spliced in rather than written inline so that no line of
	// this file carries both a count and the subject word, which would make
	// the fixture a hit in the real scan.
	const subject = "service"
	write("quiet.md", "a count of 13 with no subject word\n"+
		"the subject word alone: "+subject+", no count\n")
	write("stale.md", "nothing on this line\n"+
		"all 13 of them, one per "+subject+"\n")

	hits, err := scanUnder(root, []string{"docs"})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("planted 1 control needle, scanner reported %d hit(s): %+v", len(hits), hits)
	}
	got := hits[0]
	if got.File != "docs/stale.md" || got.Line != 2 {
		t.Errorf("control needle reported at %s:%d, planted at docs/stale.md:2", got.File, got.Line)
	}
	if len(got.Counts) != 1 || got.Counts[0] != 13 {
		t.Errorf("control needle read as %v, planted as [13]", got.Counts)
	}
}

func TestFleetGate_AllowlistEntriesStillMatchSomething(t *testing.T) {
	if len(allowlist) == 0 {
		t.Fatal("the allowlist is empty -- either a real exclusion was dropped or this test can no longer tell a stale entry from a live one")
	}
	hits := scanRepo(t)
	if len(hits) == 0 {
		t.Fatal("the scan found nothing, so no allowlist entry can be resolved against it")
	}
	for _, e := range allowlist {
		found := false
		for _, h := range hits {
			if h.File == e.File && strings.Contains(h.Text, e.LineContains) {
				found = true
				t.Logf("allowlist: %s:%d %q (%s)", h.File, h.Line, e.LineContains, e.Why)
				break
			}
		}
		if !found {
			t.Errorf("allowlist entry %s / %q matches no scanned line -- it was excluded because %s, and that reason no longer applies", e.File, e.LineContains, e.Why)
		}
	}
}

// --- AC-4 ---

// stepHeadRe matches a step's `- name:` line inside a job's `steps:` list.
var stepHeadRe = regexp.MustCompile(`^\s+- name: (.*)$`)

// yamlSteps splits content into (name, body) pairs, one per `- name:` step.
func yamlSteps(content string) (names, bodies []string) {
	lines := strings.Split(content, "\n")
	cur := -1
	for _, l := range lines {
		if m := stepHeadRe.FindStringSubmatch(l); m != nil {
			names = append(names, m[1])
			bodies = append(bodies, "")
			cur = len(bodies) - 1
			continue
		}
		if cur >= 0 {
			bodies[cur] += l + "\n"
		}
	}
	return names, bodies
}

// TestDevEnv_DoclingBuildIsGated: docling has no public domain, so CI reaches it
// only through the gateway's /healthz/fleet roll-up. Without a step that names
// it, a sidecar serving the previous commit is invisible.
func TestDevEnv_DoclingBuildIsGated(t *testing.T) {
	content := readFile(t, filepath.Join(repoRoot(t), devEnvRel))

	names, bodies := yamlSteps(content)
	if len(names) == 0 {
		t.Fatalf("%s: parsed zero steps -- the scan reached nothing, so a missing gate is indistinguishable from a present one", devEnvRel)
	}

	idx := -1
	for i, n := range names {
		if strings.Contains(strings.ToLower(n), "docling") {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("%s: no step names `docling` among %d steps -- nothing blocks on the sidecar's build", devEnvRel, len(names))
	}
	t.Logf("gate step: %q", names[idx])
	body := bodies[idx]

	for _, want := range []string{
		"healthz/fleet",  // the only route to a service with no public domain
		"docling",        // the roll-up entry it selects
		".build",         // the field it compares
		"EXPECTED_BUILD", // the commit under test
		"github.sha",     // ...bound to the actual sha, not a literal
		"exit 1",         // and it fails the run rather than warning
	} {
		if !strings.Contains(body, want) {
			t.Errorf("%s: the `%s` step does not mention %q -- it cannot be blocking on the sidecar's build", devEnvRel, names[idx], want)
		}
	}
}

// --- AC-5 ---

var yamlItemRe = regexp.MustCompile(`^\s+-\s+'([^']*)'\s*$`)

// yamlListAt collects the quoted list items directly below lines[start],
// returning each item with its 1-based line number.
func yamlListAt(lines []string, start int) (items []string, at []int) {
	for i := start + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		m := yamlItemRe.FindStringSubmatch(lines[i])
		if m == nil {
			break
		}
		items = append(items, m[1])
		at = append(at, i+1)
	}
	return items, at
}

// findKey returns the index of the first line at or after from whose trimmed
// text is key.
func findKey(lines []string, from int, key string) int {
	for i := from; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == key {
			return i
		}
	}
	return -1
}

// TestWorkflows_SidecarPathsAreFiltered is the measurement that closes Core
// AC 6: both filters already carry sidecar/**, so this passes on first write
// and exists to keep it that way.
func TestWorkflows_SidecarPathsAreFiltered(t *testing.T) {
	root := repoRoot(t)

	devLines := strings.Split(readFile(t, filepath.Join(root, devEnvRel)), "\n")
	pr := findKey(devLines, 0, "pull_request:")
	if pr < 0 {
		t.Fatalf("%s: no `pull_request:` trigger", devEnvRel)
	}
	pathsAt := findKey(devLines, pr, "paths:")
	if pathsAt < 0 {
		t.Fatalf("%s: the `pull_request:` trigger has no `paths:` allowlist", devEnvRel)
	}
	devPaths, devAt := yamlListAt(devLines, pathsAt)
	if len(devPaths) == 0 {
		t.Fatalf("%s:%d: parsed an empty `paths:` list", devEnvRel, pathsAt+1)
	}
	if !assertMember(t, devEnvRel, devPaths, devAt, "sidecar/**") {
		t.Errorf("%s: `sidecar/**` absent from the pull_request paths allowlist -- a Python-only PR fires no deploy gate", devEnvRel)
	}
	// Control: a member every version of this list has carried.
	if !contains(devPaths, "internal/**") {
		t.Errorf("%s: the `paths:` list parsed to %v, which does not contain `internal/**` -- the parse reached the wrong block", devEnvRel, devPaths)
	}

	ciLines := strings.Split(readFile(t, filepath.Join(root, ciRel)), "\n")
	sc := findKey(ciLines, 0, "sidecar:")
	if sc < 0 {
		t.Fatalf("%s: no `sidecar:` path filter", ciRel)
	}
	ciPaths, ciAt := yamlListAt(ciLines, sc)
	if len(ciPaths) == 0 {
		t.Fatalf("%s:%d: the `sidecar:` filter parsed to an empty list", ciRel, sc+1)
	}
	if !assertMember(t, ciRel, ciPaths, ciAt, "sidecar/**") {
		t.Errorf("%s:%d: the `sidecar:` filter does not list `sidecar/**` -- it is %v", ciRel, sc+1, ciPaths)
	}
}

// assertMember reports whether want is in items and logs where it was found,
// so the passing run records the measured line rather than asserting a
// number that drifts.
func assertMember(t *testing.T, file string, items []string, at []int, want string) bool {
	t.Helper()
	for i, it := range items {
		if it == want {
			t.Logf("%s:%d lists %q", file, at[i], want)
			return true
		}
	}
	return false
}

// --- AC-2: the allowlist is a carve-out, not a hole ---

// TestFleetGate_AnAllowlistEntryExcludesExactlyOneLine: an entry matching many
// lines silently swallows real count sites, and every other test here stays
// green. Widening `APPR-14-03` to `service` survives the whole suite otherwise.
func TestFleetGate_AnAllowlistEntryExcludesExactlyOneLine(t *testing.T) {
	if len(allowlist) == 0 {
		t.Fatal("the allowlist is empty -- nothing to bound")
	}
	hits := scanRepo(t)
	if len(hits) == 0 {
		t.Fatal("the scan found nothing, so no allowlist entry can be bounded against it")
	}
	for _, e := range allowlist {
		var matched []string
		for _, h := range hits {
			if h.File == e.File && strings.Contains(h.Text, e.LineContains) {
				matched = append(matched, h.File+":"+strconv.Itoa(h.Line))
			}
		}
		if len(matched) != 1 {
			t.Errorf("allowlist entry %s / %q excludes %d line(s) %v, want exactly 1 -- an entry that matches more than the one line it names carves real count sites out of the scan",
				e.File, e.LineContains, len(matched), matched)
		}
	}
	// The excluded lines must stay a small minority of the population, so a
	// pile of narrow entries cannot hollow the scan out one line at a time.
	excluded := 0
	for _, h := range hits {
		if allowed(h) {
			excluded++
		}
	}
	if excluded*2 >= len(hits) {
		t.Errorf("%d of %d scanned line(s) are allowlisted -- the exclusions are no longer a carve-out", excluded, len(hits))
	}
}

// --- AC-4: the gate step's own shell, run against fixture roll-ups ---

var runBlockRe = regexp.MustCompile(`^(\s*)run: \|\s*$`)

// runScript extracts a step's `run: |` block, dedented.
func runScript(t *testing.T, body string) string {
	t.Helper()
	lines := strings.Split(body, "\n")
	start, indent := -1, ""
	for i, l := range lines {
		if m := runBlockRe.FindStringSubmatch(l); m != nil {
			start, indent = i, m[1]+"  "
			break
		}
	}
	if start < 0 {
		t.Fatalf("the step has no `run: |` block, so its shell cannot be executed:\n%s", body)
	}
	var out []string
	for _, l := range lines[start+1:] {
		if strings.TrimSpace(l) == "" {
			out = append(out, "")
			continue
		}
		if !strings.HasPrefix(l, indent) {
			break
		}
		out = append(out, strings.TrimPrefix(l, indent))
	}
	script := strings.Join(out, "\n")
	if strings.TrimSpace(script) == "" {
		t.Fatal("the `run: |` block dedented to nothing")
	}
	return script
}

// TestDevEnv_DoclingGateFailsOnAStaleOrAbsentSidecar runs the gate's real shell
// against four roll-up payloads. TestDevEnv_DoclingBuildIsGated only asserts
// substrings, so repointing the jq selector at `gateway` -- which greens the
// gate on the gateway's build while the sidecar is arbitrarily stale -- and
// treating `absent` as acceptable both survive it.
func TestDevEnv_DoclingGateFailsOnAStaleOrAbsentSidecar(t *testing.T) {
	for _, bin := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatalf("%s is not on PATH, so the gate's own shell cannot be executed here", bin)
		}
	}

	content := readFile(t, filepath.Join(repoRoot(t), devEnvRel))
	names, bodies := yamlSteps(content)
	idx := -1
	for i, n := range names {
		if strings.Contains(strings.ToLower(n), "docling") {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("%s: no step names `docling` among %d steps", devEnvRel, len(names))
	}
	script := runScript(t, bodies[idx])

	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const old = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	cases := []struct {
		name    string
		payload string
		wantOK  bool
		says    string
	}{
		{"serving the commit under test", `{"services":[{"name":"gateway","build":"` + sha + `"},{"name":"docling","build":"` + sha + `"}]}`, true, ""},
		{"stale sidecar, fresh gateway", `{"services":[{"name":"gateway","build":"` + sha + `"},{"name":"docling","build":"` + old + `"}]}`, false, old},
		{"sidecar reports no build", `{"services":[{"name":"gateway","build":"` + sha + `"},{"name":"docling","status":"up"}]}`, false, "none"},
		{"roll-up never names the sidecar", `{"services":[{"name":"gateway","build":"` + sha + `"}]}`, false, "absent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/healthz/fleet" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.payload))
			}))
			defer srv.Close()

			// Two substitutions, both for isolation only: the sleep stub keeps
			// the failing cases off a 25s poll, and the scratch path keeps
			// concurrent runs off one shared /tmp file.
			isolated := strings.ReplaceAll(script, "/tmp/docling.json", filepath.Join(t.TempDir(), "fleet.json"))
			cmd := exec.CommandContext(t.Context(), "bash", "-c", "sleep() { :; }\n"+isolated)
			cmd.Env = append(os.Environ(), "GATEWAY_URL="+srv.URL, "EXPECTED_BUILD="+sha)
			out, err := cmd.CombinedOutput()

			if tc.wantOK {
				if err != nil {
					t.Fatalf("the gate failed a sidecar already serving the commit under test: %v\n%s", err, out)
				}
				return
			}
			if err == nil {
				t.Fatalf("the gate passed on %s -- a stale or missing sidecar greens the deploy\n%s", tc.name, out)
			}
			if !strings.Contains(string(out), tc.says) {
				t.Errorf("the failure does not name %q, so the log does not say what the sidecar reported:\n%s", tc.says, out)
			}
		})
	}
}
