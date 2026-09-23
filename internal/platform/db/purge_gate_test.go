// The chain that keeps the demo purge from failing SILENTLY, mirroring
// reset_gate_test.go's.
//
// The purge is the one non-fatal step in Provision: it fails, Provision logs and
// carries on, the gateway boots, /healthz returns 200 and every fleet gate goes
// green. So a green deploy is not evidence a purge ran. db.DemoPurgeOutcome ->
// platform.DemoPurge -> /healthz `demo_purge` -> dev-env.yml's health-gate is
// what turns that into a red run, and it is four files with no compiler between
// them. None of these tests touches a database.
//
// Names are TestPurge* deliberately — ci.yml's -run alternation is what makes
// them run in CI at all (TestCIRunFiltersReachEveryTestInThePackage).
package db_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// missingFragments returns the wanted fragments src does not carry, in order.
func missingFragments(src string, want []string) []string {
	var out []string
	for _, w := range want {
		if !strings.Contains(src, w) {
			out = append(out, w)
		}
	}
	return out
}

// TestPurgeDecisionIsPublishedByTheGateway pins the publish step against
// main.go's own source: main() opens a listener, so it cannot be called here.
// Asserting on db.DemoPurgeOutcome specifically is the point — a value derived
// any other way is a second copy of what Provision already decided.
func TestPurgeDecisionIsPublishedByTheGateway(t *testing.T) {
	want := []string{"platform.DemoPurge =", "db.DemoPurgeOutcome"}

	b, err := os.ReadFile("../../../cmd/gateway/main.go")
	if err != nil {
		t.Fatalf("read cmd/gateway/main.go: %v", err)
	}
	if missing := missingFragments(string(b), want); len(missing) != 0 {
		t.Errorf("cmd/gateway/main.go carries none of %v — /healthz would carry no demo_purge field, so a swallowed purge failure stays invisible to every gate", missing)
	}

	t.Run("control needle", func(t *testing.T) {
		const publishes = "\tplatform.DemoPurge = string(db.DemoPurgeOutcome)\n"
		if got := missingFragments(publishes, want); len(got) != 0 {
			t.Fatalf("the scanner calls %v missing from a fixture that carries both — it cannot see what it looks for", got)
		}
		const publishesNothing = "\tplatform.DBReset = strconv.FormatBool(provisionCfg.ResetWillRun())\n"
		if got := missingFragments(publishesNothing, want); len(got) != len(want) {
			t.Fatalf("the scanner found only %d of %d fragment(s) missing from a fixture that carries neither — a clean report from it would prove nothing", len(got), len(want))
		}
	})
}

// TestPurgeFieldIsPublishedByNoOtherService: the other eight services never
// provision, so their /healthz bodies must stay byte-identical to before the
// field existed.
func TestPurgeFieldIsPublishedByNoOtherService(t *testing.T) {
	mains, err := filepath.Glob("../../../cmd/*/main.go")
	if err != nil {
		t.Fatalf("glob cmd/*/main.go: %v", err)
	}
	if len(mains) < 2 {
		t.Fatalf("found %d cmd/*/main.go file(s); the fleet has nine, so the scan below would be vacuous", len(mains))
	}

	var assigning []string
	for _, path := range mains {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(b), "platform.DemoPurge =") {
			assigning = append(assigning, filepath.Base(filepath.Dir(path)))
		}
	}

	if len(assigning) != 1 {
		t.Fatalf("%d of %d cmd/*/main.go files assign platform.DemoPurge (%v), want exactly 1", len(assigning), len(mains), assigning)
	}
	if assigning[0] != "gateway" {
		t.Errorf("platform.DemoPurge is assigned by cmd/%s, want cmd/gateway — the gateway is the only binary that provisions", assigning[0])
	}
}

// gateFragments are what the health-gate must carry to read the field and
// assert it in both directions. Spelled once, reused by the control needle below.
var gateFragments = []string{`.demo_purge // empty`, `want_purge=true`, `want_purge=false`, `[ "$purge" != "$want_purge" ]`}

var workflowJobKey = regexp.MustCompile(`^  [A-Za-z0-9_-]+:\s*$`)

// healthGateJob returns src's health-gate job lines, indent kept; nil when absent.
func healthGateJob(src string) []string {
	lines := strings.Split(src, "\n")
	start := slices.Index(lines, "  health-gate:")
	if start < 0 {
		return nil
	}
	end := start + 1
	for end < len(lines) && !workflowJobKey.MatchString(lines[end]) {
		end++
	}
	return lines[start:end]
}

// devEnvHealthGate returns the comment-stripped health-gate job. A job that never
// reads .build is not the gate, and every scan below would examine nothing.
func devEnvHealthGate(t *testing.T) string {
	t.Helper()
	job := healthGateJob(devEnvExecutable(t))
	gate := strings.Join(job, "\n")
	if len(job) < 20 || !strings.Contains(gate, `.build // empty`) || !strings.Contains(gate, "healthz") {
		t.Fatalf("dev-env.yml's health-gate job is %d line(s) and never reads .build from /healthz; the checks below would examine nothing", len(job))
	}
	return gate
}

// TestPurgeIsAssertedByTheDevEnvGate pins the far end of the chain: publishing
// the field buys nothing unless a gate reads it.
func TestPurgeIsAssertedByTheDevEnvGate(t *testing.T) {
	gate := stmtsCode(devEnvHealthGate(t))

	if missing := missingFragments(gate, gateFragments); len(missing) != 0 {
		t.Errorf("dev-env.yml's health-gate carries none of %v — no run fails a purge outcome that is wrong for its target", missing)
	}

	t.Run("control needle", func(t *testing.T) {
		const asserts = `
            purge=$(printf '%s' "$body" | jq -r '.demo_purge // empty' 2>/dev/null || echo '')
          if [ "$IS_PR" = "true" ]; then
            want_purge=true;  want_mock=on
          else
            want_purge=false; want_mock=absent
          fi
          if [ "$purge" != "$want_purge" ]; then
            echo "::error::purge"
            exit 1
          fi
`
		if got := missingFragments(stmtsCode(asserts), gateFragments); len(got) != 0 {
			t.Fatalf("the scanner calls %v missing from a fixture that carries all of them — it cannot see what it looks for", got)
		}
		if got := missingFragments(stmtsCode(strings.ReplaceAll(asserts, "purge", "reset")), gateFragments); len(got) != len(gateFragments) {
			t.Fatalf("the scanner found only %d of %d fragment(s) missing from a fixture with the purge assertion renamed away — a clean report from it would prove nothing", len(got), len(gateFragments))
		}
		inComment := "          echo ok  # " + strings.Join(gateFragments, " ") + "\n"
		if got := missingFragments(stmtsCode(inComment), gateFragments); len(got) != len(gateFragments) {
			t.Fatalf("the scanner reads %d fragment(s) out of a trailing comment", len(gateFragments)-len(got))
		}
	})
}

// stmtsCode returns src's shell statements, comments dropped, one per line.
func stmtsCode(src string) string {
	var b strings.Builder
	for _, s := range shellStmts(src) {
		b.WriteString(s.text + "\n")
	}
	return b.String()
}

// shellStmt is one shell statement and the 0-based source line it starts on.
type shellStmt struct {
	text string
	line int
}

// shellStmts splits src at unquoted `;` and newlines, drops `#` comments, and
// splits a leading then/else/do off a command that shares its line.
func shellStmts(src string) []shellStmt {
	var out []shellStmt
	for i, line := range strings.Split(src, "\n") {
		var cur strings.Builder
		flush := func() {
			s := strings.TrimSpace(cur.String())
			cur.Reset()
			for s != "" {
				kw, rest, _ := strings.Cut(s, " ")
				rest = strings.TrimSpace(rest)
				if (kw != "then" && kw != "else" && kw != "do") || rest == "" {
					out = append(out, shellStmt{s, i})
					return
				}
				out = append(out, shellStmt{kw, i})
				s = rest
			}
		}
		var inS, inD bool
	scan:
		for j := 0; j < len(line); j++ {
			c := line[j]
			switch {
			case c == '\\' && !inS && j+1 < len(line):
				cur.WriteByte(c)
				j++
				c = line[j]
			case c == '\'' && !inD:
				inS = !inS
			case c == '"' && !inS:
				inD = !inD
			case inS || inD:
			case c == ';':
				flush()
				continue
			case c == '#' && (j == 0 || line[j-1] == ' ' || line[j-1] == '\t'):
				break scan
			}
			cur.WriteByte(c)
		}
		flush()
	}
	return out
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// topIfBlocks returns each if-block no other if-block encloses, opener to fi, in source order.
func topIfBlocks(stmts []shellStmt) [][]shellStmt {
	var blocks [][]shellStmt
	depth, start := 0, 0
	for j, s := range stmts {
		switch firstWord(s.text) {
		case "if":
			if depth == 0 {
				start = j
			}
			depth++
		case "fi":
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 {
				blocks = append(blocks, stmts[start:j+1])
			}
		}
	}
	return blocks
}

func stmtsAssign(b []shellStmt, name string) bool {
	return slices.ContainsFunc(b, func(s shellStmt) bool { return strings.HasPrefix(s.text, name+"=") })
}

// stmtsTest reports whether an if or elif condition in b reads "$v".
func stmtsTest(b []shellStmt, v string) bool {
	return slices.ContainsFunc(b, func(s shellStmt) bool {
		w := firstWord(s.text)
		return (w == "if" || w == "elif") && strings.Contains(s.text, `"$`+v+`"`)
	})
}

// gateBlocks returns the top-level if-blocks that assign want or test "$v": the
// whole gate for one field, in the order the step runs it.
func gateBlocks(src, v, want string) [][]shellStmt {
	var out [][]shellStmt
	for _, b := range topIfBlocks(shellStmts(src)) {
		if stmtsAssign(b, want) || stmtsTest(b, v) {
			out = append(out, b)
		}
	}
	return out
}

// gateScript returns gateBlocks as runnable shell, "" when there are none.
func gateScript(src, v, want string) string {
	lines := strings.Split(src, "\n")
	var parts []string
	for _, b := range gateBlocks(src, v, want) {
		for _, l := range lines[b[0].line : b[len(b)-1].line+1] {
			parts = append(parts, strings.TrimSpace(l))
		}
	}
	return strings.Join(parts, "\n")
}

// branchValues returns what the if-block b assigns to name at its own depth,
// split into its then-branch and its else-branch, and whether it has an elif.
func branchValues(b []shellStmt, name string) (then, els []string, elif bool) {
	depth, inElse := 0, false
	for _, s := range b {
		switch firstWord(s.text) {
		case "if":
			depth++
			continue
		case "fi":
			depth--
			continue
		case "elif":
			elif = elif || depth == 1
			inElse = inElse || depth == 1
			continue
		case "else":
			inElse = inElse || depth == 1
			continue
		}
		v, ok := strings.CutPrefix(s.text, name+"=")
		if depth != 1 || !ok {
			continue
		}
		if v = strings.Trim(v, `"'`); inElse {
			els = append(els, v)
		} else {
			then = append(then, v)
		}
	}
	return then, els, elif
}

// gateLayoutFaults returns every reason the purge gate in src is not the
// directional shape: want_purge=true in the IS_PR branch, false in its else, and
// one assertion after that branch at its indent. Empty means the layout is right.
func gateLayoutFaults(src string) []string {
	lines := strings.Split(src, "\n")
	indent := func(line int) int { return len(lines[line]) - len(strings.TrimLeft(lines[line], " ")) }

	var branch []shellStmt
	var checks [][]shellStmt
	for _, b := range gateBlocks(src, "purge", "want_purge") {
		if branch == nil && strings.HasPrefix(b[0].text, `if [ "$IS_PR" = "true" ]`) && stmtsAssign(b, "want_purge") {
			branch = b
		}
		if stmtsTest(b, "purge") {
			checks = append(checks, b)
		}
	}

	var faults []string
	if branch == nil {
		faults = append(faults, `no top-level if [ "$IS_PR" = "true" ] branch assigns want_purge`)
	} else {
		then, els, elif := branchValues(branch, "want_purge")
		if !slices.Equal(then, []string{"true"}) {
			faults = append(faults, fmt.Sprintf("the IS_PR branch assigns want_purge %v, want exactly [true]", then))
		}
		if !slices.Equal(els, []string{"false"}) {
			faults = append(faults, fmt.Sprintf("the else branch assigns want_purge %v, want exactly [false]", els))
		}
		if elif {
			faults = append(faults, "the IS_PR branch carries an elif, so its else is not the only persistent path")
		}
	}
	if len(checks) != 1 {
		return append(faults, fmt.Sprintf(`%d top-level if-blocks test "$purge", want exactly 1`, len(checks)))
	}
	check := checks[0]
	if !strings.HasPrefix(check[0].text, `if [ "$purge" != "$want_purge" ]`) {
		faults = append(faults, fmt.Sprintf(`the purge assertion is %q, not if [ "$purge" != "$want_purge" ]`, check[0].text))
	}
	if !slices.ContainsFunc(check, func(s shellStmt) bool { return s.text == "exit 1" }) {
		faults = append(faults, "the purge assertion never runs exit 1")
	}
	if branch != nil {
		if check[0].line <= branch[len(branch)-1].line {
			faults = append(faults, "the purge assertion sits before or inside the IS_PR branch, so want_purge is unset or the assertion runs on one path only")
		}
		if indent(check[0].line) != indent(branch[0].line) {
			faults = append(faults, fmt.Sprintf("the purge assertion is indented %d spaces against the IS_PR branch's %d", indent(check[0].line), indent(branch[0].line)))
		}
	}
	return faults
}

// TestPurgeGateAssertionCoversBothPaths: the expected outcome is set per target,
// and one assertion after the branch compares against it on every trigger.
// Nesting the assertion inside a branch disarms the other path and looks
// identical in review.
func TestPurgeGateAssertionCoversBothPaths(t *testing.T) {
	gate := devEnvHealthGate(t)

	for _, fault := range gateLayoutFaults(gate) {
		t.Errorf("dev-env.yml's health-gate: %s", fault)
	}

	t.Run("control needle", func(t *testing.T) {
		const reset = `
          if [ "$IS_PR" = "true" ]; then
            if [ "$reset" != "true" ]; then
              exit 1
            fi
          elif [ "$reset" = "true" ]; then
            exit 1
          fi
`
		const branch = `
          if [ "$IS_PR" = "true" ]; then
            want_purge=true;  want_mock=on
          else
            want_purge=false; want_mock=absent
          fi
`
		const check = `          if [ "$purge" != "$want_purge" ]; then
            echo "::error::purge"
            exit 1
          fi
`
		good := reset + branch + check
		if faults := gateLayoutFaults(good); len(faults) != 0 {
			t.Fatalf("the scanner reports %v against the directional fixture — it cannot recognise the shape it demands", faults)
		}
		oneLine := reset + `
          if [ "$IS_PR" = "true" ]; then want_purge=true; want_mock=on; else want_purge=false; want_mock=absent; fi
          if [ "$purge" != "$want_purge" ]; then echo "::error::purge"; exit 1; fi
`
		if faults := gateLayoutFaults(oneLine); len(faults) != 0 {
			t.Fatalf("the scanner reports %v against the same gate written on two lines", faults)
		}

		nested := reset + strings.Replace(branch, "            want_purge=true;  want_mock=on\n",
			"            want_purge=true;  want_mock=on\n  "+strings.ReplaceAll(check, "\n  ", "\n    "), 1)
		for _, bad := range []struct{ name, src string }{
			{"swapped values", reset + strings.NewReplacer("want_purge=true", "want_purge=false", "want_purge=false", "want_purge=true").Replace(branch) + check},
			{"assertion nested in the PR branch", nested},
			{"assertion before the branch", reset + check + branch},
			{"non-directional assertion kept beside it", good + `          if [ "$purge" != "true" ]; then
            exit 1
          fi
`},
			{"no else", reset + strings.Replace(branch, "          else\n            want_purge=false; want_mock=absent\n", "", 1) + check},
		} {
			if bad.src == good {
				t.Fatalf("fixture %q: the edit did not apply", bad.name)
			}
			if faults := gateLayoutFaults(bad.src); len(faults) == 0 {
				t.Errorf("the scanner reports the %q fixture clean — it cannot find a planted violation", bad.name)
			}
		}
	})
}

// runShellBlock runs block under bash -e, as a workflow `run:` step does, with
// only env set, and returns its exit status.
func runShellBlock(t *testing.T, block string, env ...string) int {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "bash", "-e", "-c", block)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "EXPECTED_BUILD=deadbeef"}, env...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("run gate block with %v: %v (output %q)", env, err, out)
	}
	return exitErr.ExitCode()
}

// runGateBlock runs block with IS_PR and $purge set, and returns its exit status.
func runGateBlock(t *testing.T, block, isPR, purge string) int {
	t.Helper()
	return runShellBlock(t, block, "IS_PR="+isPR, "purge="+purge)
}

// gateOutcomes are the four values /healthz can hand the gate, on each target.
// A PR fork provisions at boot, so only "true" passes there. Production reads
// ENVIRONMENT=production, which db.provisionableEnvironment refuses, so only
// "false" passes there.
var gateOutcomes = []struct {
	isPR, purge string
	wantExit    int
	cause       string
}{
	{"true", "true", 0, "a PR fork whose purge ran"},
	{"true", "false", 1, "a fork whose ENVIRONMENT is off the allowlist, or GATEWAY_DB_BOOTSTRAP was lost"},
	{"true", "error", 1, "the purge failed and Provision swallowed it to keep the fleet up"},
	{"true", "", 1, "the gateway predates the field, or main.go's assignment was deleted"},
	{"false", "false", 0, "production, whose ENVIRONMENT turns boot provisioning off"},
	{"false", "true", 1, "a purge ran against live data: production's gateway ENVIRONMENT is no longer production"},
	{"false", "error", 1, "a purge was attempted against live data and failed"},
	{"false", "", 1, "the gateway predates the field, or main.go's assignment was deleted"},
}

// TestPurgeGateFailsOnEveryOutcomeOffItsDirection executes the committed gate.
// A gate that cannot go red is not a gate, and reading the YAML back cannot
// tell the two apart.
func TestPurgeGateFailsOnEveryOutcomeOffItsDirection(t *testing.T) {
	// First, so the runner is proved able to observe both outcomes even on the
	// runs where the real block is still missing.
	t.Run("control needle", func(t *testing.T) {
		const expect = "if [ \"$IS_PR\" = \"true\" ]; then\nwant_purge=true\nelse\nwant_purge=false\nfi\n"
		const equivalent = expect + "if [ \"$purge\" != \"$want_purge\" ]; then\necho fail\nexit 1\nfi"
		for _, c := range gateOutcomes {
			if got := runGateBlock(t, equivalent, c.isPR, c.purge); got != c.wantExit {
				t.Fatalf("the runner reports exit %d for IS_PR=%s purge=%q against a hand-written equivalent block, want %d — it cannot observe the outcome it asserts", got, c.isPR, c.purge, c.wantExit)
			}
		}
		const inverted = expect + "if [ \"$purge\" = \"$want_purge\" ]; then\nexit 1\nfi"
		for _, c := range gateOutcomes {
			if got := runGateBlock(t, inverted, c.isPR, c.purge); got == c.wantExit {
				t.Fatalf("the runner reports exit %d for IS_PR=%s purge=%q against an INVERTED block too — it is not reading the block it was given", got, c.isPR, c.purge)
			}
		}
	})

	block := gateScript(devEnvHealthGate(t), "purge", "want_purge")
	if block == "" {
		t.Fatalf(`dev-env.yml's health-gate carries no if-block on "$purge", so no outcome can fail a run`)
	}

	for _, c := range gateOutcomes {
		if got := runGateBlock(t, block, c.isPR, c.purge); got != c.wantExit {
			t.Errorf("IS_PR=%s demo_purge=%q exits %d, want %d — %s", c.isPR, c.purge, got, c.wantExit, c.cause)
		}
	}
}
