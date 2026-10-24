// fleetgate_test.go: EXTR-17-06. The fleet size is written in prose in a dozen
// places across workflows and CI scripts; each one goes stale silently
// the moment the fleet changes. This derives the size from dev-env.yml's
// deploy topology and fails on every site that disagrees.
//
// Test-only, no main.go: like internal/tools/dockerignoregate (the precedent
// for this shape) it needs no runtime arguments, so plain `go test ./...`
// reaches it with no ci.yml wiring.
package fleetgate

import (
	"io/fs"
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
//	grep -rnE '\b(16|17)\b' \
//	  .github/workflows scripts/ci internal/tools \
//	  | grep -iE 'service'
//
// Split across three lines on purpose: written as one line this comment
// carries both halves of the pattern and becomes a hit in its own scan.
var scanTrees = []string{".github/workflows", "scripts/ci", "internal/tools"}

// The two halves of that command, kept on separate lines so neither line is
// itself a hit.
var (
	countRe   = regexp.MustCompile(`\b(16|17)\b`)
	subjectRe = regexp.MustCompile(`(?i)service`)
)

// Population floors: the measured population (4 hits across 1 file), this gate's own source excluded.
const (
	minHits  = 4
	minFiles = 1
)

// allowEntry keys on a line substring, never a line number -- a line-keyed
// entry drifts on the next edit above it and turns into recurring red.
type allowEntry struct {
	File         string
	LineContains string
	Why          string
}

// TestFleetGate_AllowlistFaultsFireOnAPlantedTree drives the carve-out machinery.
var allowlist = []allowEntry{}

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

func allowed(list []allowEntry, h hit) bool {
	for _, e := range list {
		if h.File == e.File && strings.Contains(h.Text, e.LineContains) {
			return true
		}
	}
	return false
}

// allowlistFaults returns the entries that carve out no scanned line, the
// entries that carve out more than one, and whether list carves out half the
// hits or more. TestFleetGate_AllowlistFaultsFireOnAPlantedTree drives each.
func allowlistFaults(list []allowEntry, hits []hit) (stale, wide []string, hollow bool) {
	excluded := 0
	for _, h := range hits {
		if allowed(list, h) {
			excluded++
		}
	}
	for _, e := range list {
		var matched []string
		for _, h := range hits {
			if allowed([]allowEntry{e}, h) {
				matched = append(matched, h.File+":"+strconv.Itoa(h.Line))
			}
		}
		name := e.File + " / " + strconv.Quote(e.LineContains)
		switch {
		case len(matched) == 0:
			stale = append(stale, name+" ("+e.Why+")")
		case len(matched) > 1:
			wide = append(wide, name+" excludes "+strings.Join(matched, ", "))
		}
	}
	return stale, wide, excluded*2 >= len(hits)
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

// spaURLKey maps a deploy-spas name to its URL variable: library -> LIBRARY_URL.
func spaURLKey(spa string) string {
	return strings.ToUpper(strings.ReplaceAll(spa, "-", "_")) + "_URL"
}

// jobBounds returns the line range [start, end) of the job that holds line i.
func jobBounds(lines []string, i int) (int, int) {
	start := i
	for start > 0 && !jobHeadRe.MatchString(lines[start]) {
		start--
	}
	end := i + 1
	for end < len(lines) && !jobHeadRe.MatchString(lines[end]) {
		end++
	}
	return start, end
}

// trailingYAMLComment matches a `#` comment that follows code on a run line (no quote handling: the wait lines carry none).
var trailingYAMLComment = regexp.MustCompile(`\s+#.*$`)

// --- AC-2 ---

// backendCountRe finds "<n> backends" prose; countRe cannot, as it needs the word "service".
var backendCountRe = regexp.MustCompile(`(?i)\b([0-9]+|eight|nine|ten|eleven|twelve)\s+backends?\b`)

var countWords = map[string]int{"eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12}

// --- AC-3 ---

func TestFleetGate_FindsAPlantedControlNeedle(t *testing.T) {
	root := t.TempDir()
	tree := filepath.Join(root, "notes")
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
	write("quiet.md", "a count of 16 with no subject word\n"+
		"the subject word alone: "+subject+", no count\n")
	write("stale.md", "nothing on this line\n"+
		"all 16 of them, one per "+subject+"\n")

	hits, err := scanUnder(root, []string{"notes"})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("planted 1 control needle, scanner reported %d hit(s): %+v", len(hits), hits)
	}
	got := hits[0]
	if got.File != "notes/stale.md" || got.Line != 2 {
		t.Errorf("control needle reported at %s:%d, planted at notes/stale.md:2", got.File, got.Line)
	}
	if len(got.Counts) != 1 || got.Counts[0] != 16 {
		t.Errorf("control needle read as %v, planted as [16]", got.Counts)
	}
}

// The real allowlist may be empty, which leaves the two allowlist tests
// looping over nothing; this drives every fault branch on a planted tree.
func TestFleetGate_AllowlistFaultsFireOnAPlantedTree(t *testing.T) {
	root := t.TempDir()
	tree := filepath.Join(root, "notes")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Spliced, as in TestFleetGate_FindsAPlantedControlNeedle, so no line of
	// this file is itself a hit.
	const subject = "service"
	line := "all 16 of them, one per " + subject
	body := line + " CARVE\n" + line + " WIDE\n" + line + " WIDE\n" + line + "\n" + line + "\n" + line + "\n"
	if err := os.WriteFile(filepath.Join(tree, "a.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	hits, err := scanUnder(root, []string{"notes"})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(hits) != 6 {
		t.Fatalf("planted 6 hits, scanner reported %d: %+v", len(hits), hits)
	}

	carve := allowEntry{File: "notes/a.md", LineContains: "CARVE"}
	wideEntry := allowEntry{File: "notes/a.md", LineContains: "WIDE"}
	gone := allowEntry{File: "notes/a.md", LineContains: "GONE"}

	if !allowed([]allowEntry{carve}, hits[0]) {
		t.Errorf("the entry does not carve out its own line %q", hits[0].Text)
	}
	if allowed([]allowEntry{carve}, hits[3]) {
		t.Errorf("the entry carves out a line it does not name: %q", hits[3].Text)
	}
	if stale, wide, hollow := allowlistFaults([]allowEntry{carve}, hits); len(stale) != 0 || len(wide) != 0 || hollow {
		t.Errorf("a one-line entry over 6 hits reported stale=%v wide=%v hollow=%v, want none", stale, wide, hollow)
	}
	if stale, _, _ := allowlistFaults([]allowEntry{gone}, hits); len(stale) != 1 {
		t.Errorf("an entry matching no line reported stale=%v, want 1", stale)
	}
	if _, wide, _ := allowlistFaults([]allowEntry{wideEntry}, hits); len(wide) != 1 {
		t.Errorf("an entry matching 2 lines reported wide=%v, want 1", wide)
	}
	// Exactly half: the boundary the hollow check owns.
	if _, _, hollow := allowlistFaults([]allowEntry{carve, wideEntry}, hits); !hollow {
		t.Errorf("3 of 6 lines carved out, hollow not reported")
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
	jobs := findKey(devLines, 0, "jobs:")
	if jobs < 0 {
		t.Fatalf("%s: no `jobs:` key", devEnvRel)
	}
	changes := findKey(devLines, jobs, "changes:")
	if changes < 0 {
		t.Fatalf("%s: no `changes` job", devEnvRel)
	}
	filterAt := findKey(devLines, changes, "e2e:")
	if filterAt < 0 {
		t.Fatalf("%s: the `changes` job has no `e2e:` path filter", devEnvRel)
	}
	devPaths, devAt := yamlListAt(devLines, filterAt)
	if len(devPaths) == 0 {
		t.Fatalf("%s:%d: parsed an empty `e2e:` filter list", devEnvRel, filterAt+1)
	}
	if !assertMember(t, devEnvRel, devPaths, devAt, "sidecar/**") {
		t.Errorf("%s: `sidecar/**` absent from the `changes` job's e2e filter -- a Python-only PR fires no deploy gate", devEnvRel)
	}
	// Control: a member every version of this list has carried.
	if !contains(devPaths, "internal/**") {
		t.Errorf("%s: the `e2e:` filter parsed to %v, which does not contain `internal/**` -- the parse reached the wrong block", devEnvRel, devPaths)
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
