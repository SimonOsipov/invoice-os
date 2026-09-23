package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const stampMockIssuerScript = "scripts/ci/stamp-mock-issuer.sh"

func TestCommittedGatewayBuildTagsAreEmpty(t *testing.T) {
	b, err := os.ReadFile("build.tags")
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("cmd/gateway/build.tags is missing: the Dockerfile passes it to `go build -tags`, and %s stamps it on PR builds only; commit it empty", stampMockIssuerScript)
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != "" {
		t.Errorf("cmd/gateway/build.tags reads %q; committed non-empty, every push build compiles those tags into the production gateway", got)
	}
}

// fileState is a file's content, or nil when it does not exist.
func fileState(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte{}, b...)
}

// runStamp runs `sh scripts/ci/stamp-mock-issuer.sh` inside dir, on a copy of the
// script, so nothing can reach the real cmd/gateway/build.tags.
func runStamp(t *testing.T, dir string) (string, int) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", stampMockIssuerScript))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Nothing to copy: sh reports the missing script and the assertions fail on it.
	case err != nil:
		t.Fatal(err)
	default:
		dst := filepath.Join(dir, stampMockIssuerScript)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, src, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(t.Context(), "sh", stampMockIssuerScript)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}
	if err != nil {
		t.Fatalf("sh %s: %v", stampMockIssuerScript, err)
	}
	return string(out), 0
}

func TestStampMockIssuerWritesTheTag(t *testing.T) {
	realBefore := fileState(t, "build.tags")
	t.Cleanup(func() {
		if after := fileState(t, "build.tags"); !bytes.Equal(after, realBefore) {
			t.Errorf("the real cmd/gateway/build.tags changed from %q to %q; the stamp must run only on the temp copy", realBefore, after)
		}
	})

	t.Run("stamps_an_empty_file", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "cmd", "gateway", "build.tags")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		out, code := runStamp(t, dir)
		if code != 0 {
			t.Errorf("exit %d, want 0:\n%s", code, out)
		}
		if got := string(fileState(t, target)); got != "mockissuer" && got != "mockissuer\n" {
			t.Errorf("cmd/gateway/build.tags reads %q after the stamp, want exactly \"mockissuer\"", got)
		}
	})

	t.Run("missing_file_fails", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "cmd", "gateway", "build.tags")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		out, code := runStamp(t, dir)
		if code != 1 {
			t.Errorf("exit %d with cmd/gateway/build.tags absent, want 1:\n%s", code, out)
		}
		if !strings.Contains(out, "::error::") {
			t.Errorf("no ::error:: annotation with cmd/gateway/build.tags absent:\n%s", out)
		}
		if fileState(t, target) != nil {
			t.Errorf("the stamp created cmd/gateway/build.tags instead of refusing")
		}
	})
}

// A command starts a line or follows ; & or |. Exact case: paths and flags are case-sensitive.
var (
	canaryStampRE   = regexp.MustCompile(`(?:^|[;&|]\s*)(?:(?:ba)?sh\s+)?(?:\./)?scripts/ci/stamp-mock-issuer\.sh(?:[\s;&|]|$)`)
	canaryRestoreRE = regexp.MustCompile(`(?:^|[;&|]\s*)git checkout -- cmd/gateway/build\.tags(?:[\s;&|]|$)`)
	canaryScanRE    = regexp.MustCompile(`(?:^|[;&|]\s*)((?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)+)go test \./cmd/gateway/?(?:\s|$)`)
	goTestRunFlagRE = regexp.MustCompile(`(?:^|\s)--?(?:test\.)?run(?:[=\s]|$)`)
)

// shellCommands splits run text into logical lines, joining backslash continuations.
func shellCommands(run string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(run, "\n") {
		if strings.HasSuffix(line, `\`) {
			cur.WriteString(strings.TrimSuffix(line, `\`) + " ")
			continue
		}
		cur.WriteString(line)
		out = append(out, cur.String())
		cur.Reset()
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// canaryScanProblems reports each way the docker-canary run text fails to test
// cmd/gateway against both copied binaries with the committed build.tags restored.
func canaryScanProblems(run string) []string {
	cmds := shellCommands(run)
	type event struct {
		line, col int
		restore   bool
	}
	var events []event
	scanLine, scanCol := -1, -1
	for i, c := range cmds {
		for _, m := range canaryStampRE.FindAllStringIndex(c, -1) {
			events = append(events, event{i, m[0], false})
		}
		for _, m := range canaryRestoreRE.FindAllStringIndex(c, -1) {
			events = append(events, event{i, m[0], true})
		}
		if scanLine >= 0 {
			continue
		}
		if m := canaryScanRE.FindStringSubmatchIndex(c); m != nil {
			env := strings.Fields(c[m[2]:m[3]])
			if slices.ContainsFunc(env, func(a string) bool { return strings.HasPrefix(a, "GATEWAY_BINARY=") }) &&
				slices.ContainsFunc(env, func(a string) bool { return strings.HasPrefix(a, "GATEWAY_BINARY_MOCKISSUER=") }) {
				scanLine, scanCol = i, m[0]
			}
		}
	}
	var problems []string
	if scanLine < 0 {
		problems = append(problems, "no command runs `GATEWAY_BINARY=… GATEWAY_BINARY_MOCKISSUER=… go test ./cmd/gateway/`")
		scanLine = len(cmds)
	} else if goTestRunFlagRE.MatchString(cmds[scanLine]) {
		problems = append(problems, fmt.Sprintf("the cmd/gateway test line carries -run; every test must run, and ciRunFilters cannot read a ./cmd/ package: %q", cmds[scanLine]))
	}
	var before []event
	for _, e := range events {
		if e.line < scanLine || (e.line == scanLine && e.col < scanCol) {
			before = append(before, e)
		}
	}
	slices.SortFunc(before, func(a, b event) int {
		if a.line != b.line {
			return a.line - b.line
		}
		return a.col - b.col
	})
	lastStamp := -1
	for i, e := range before {
		if !e.restore {
			lastStamp = i
		}
	}
	if lastStamp < 0 {
		problems = append(problems, "no `stamp-mock-issuer.sh` before the cmd/gateway test line; nothing builds the PR-shaped image")
	}
	if !slices.ContainsFunc(before[lastStamp+1:], func(e event) bool { return e.restore }) {
		problems = append(problems, "no `git checkout -- cmd/gateway/build.tags` after the last stamp and before the cmd/gateway test line; the test would read the stamped file")
	}
	return problems
}

// jobNeeds returns a job block's needs: list, flow or block style.
func jobNeeds(block []string) []string {
	for i, line := range block {
		trimmed := strings.TrimSpace(line)
		if len(line)-len(strings.TrimLeft(line, " ")) != 4 || !strings.HasPrefix(trimmed, "needs:") {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(trimmed, "needs:"))
		var names []string
		if val == "" {
			for _, l := range block[i+1:] {
				item, ok := strings.CutPrefix(strings.TrimSpace(l), "- ")
				if !ok {
					break
				}
				names = append(names, strings.Trim(strings.TrimSpace(item), `"'`))
			}
			return names
		}
		for _, n := range strings.Split(strings.Trim(val, "[]"), ",") {
			names = append(names, strings.Trim(strings.TrimSpace(n), `"'`))
		}
		return names
	}
	return nil
}

// aggregateFailsOn reports whether run text holds an `if` on needs.<job>.result that
// tests for failure and reaches `exit 1` before its `fi`. An echo of the result is not one.
func aggregateFailsOn(run, job string) bool {
	lines := strings.Split(run, "\n")
	ref := "${{ needs." + job + ".result }}"
	for i, l := range lines {
		if !strings.HasPrefix(l, "if ") || !strings.Contains(l, ref) ||
			!(strings.Contains(l, `= "failure"`) || strings.Contains(l, `!= "success"`)) {
			continue
		}
		for _, next := range lines[i:] {
			if strings.Contains(next, "exit 1") {
				return true
			}
			if next == "fi" {
				break
			}
		}
	}
	return false
}

// dockerCanaryProblems reads comment-stripped ci.yml: the docker-canary job's run
// text, then the ci aggregate's needs: and shell block.
func dockerCanaryProblems(ciYAML string) []string {
	lines := yamlCode(ciYAML)
	var problems []string
	if canary := jobBlock(lines, "docker-canary"); len(canary) == 0 {
		problems = append(problems, "no docker-canary job")
	} else {
		problems = append(problems, canaryScanProblems(runText(canary))...)
	}
	ci := jobBlock(lines, "ci")
	if len(ci) == 0 {
		return append(problems, "no ci job")
	}
	if !slices.Contains(jobNeeds(ci), "docker-canary") {
		problems = append(problems, "the ci job's needs: does not list docker-canary")
	}
	if !aggregateFailsOn(runText(ci), "docker-canary") {
		problems = append(problems, "the ci job's shell block never fails on needs.docker-canary.result")
	}
	return problems
}

func TestDockerCanaryScansBothGatewayImages(t *testing.T) {
	const stamp = "      - name: Stamp the mock issuer\n        run: sh scripts/ci/stamp-mock-issuer.sh\n"
	const restore = "      - name: Restore the committed build.tags\n        run: git checkout -- cmd/gateway/build.tags\n"
	const scan = "      - name: Scan both gateway binaries\n        run: GATEWAY_BINARY=/tmp/gateway-prod GATEWAY_BINARY_MOCKISSUER=/tmp/gateway-mockissuer go test ./cmd/gateway/ -count=1 -v\n"
	const head = "on: push\njobs:\n" +
		"  go:\n    runs-on: ubuntu-latest\n    steps:\n      - run: go test ./...\n" +
		"  docker-canary:\n    needs: changes\n    runs-on: ubuntu-latest\n    steps:\n" +
		"      - uses: actions/checkout@v4\n" +
		"      - name: Build the gateway image\n        uses: docker/build-push-action@v6\n        with:\n          build-args: |\n            SERVICE=gateway\n          load: true\n          tags: gateway:canary\n" +
		"      - name: Copy the production-shaped binary\n        run: |\n          id=$(docker create gateway:canary)\n          docker cp \"$id:/service\" /tmp/gateway-prod\n"
	const prBuild = "      - name: Build the PR-shaped gateway image\n        uses: docker/build-push-action@v6\n        with:\n          build-args: |\n            SERVICE=gateway\n          load: true\n          tags: gateway:canary-mockissuer\n"
	const setupGo = "      - uses: actions/setup-go@v5\n        with:\n          go-version-file: go.mod\n"
	const ciNeeds = "    needs: [changes, go, docker-canary]\n"
	const ciAssert = "          if [ \"${{ needs.docker-canary.result }}\" = \"failure\" ] || [ \"${{ needs.docker-canary.result }}\" = \"cancelled\" ]; then\n            echo \"::error::Docker canary build failed\"; exit 1\n          fi\n"
	const ciHead = "  ci:\n" + ciNeeds + "    if: always()\n    runs-on: ubuntu-latest\n    steps:\n      - name: Require all triggered jobs to pass\n        run: |\n" +
		"          echo \"docker-canary: ${{ needs.docker-canary.result }}\"\n" +
		"          if [ \"${{ needs.go.result }}\" = \"failure\" ]; then\n            echo \"::error::Go job failed\"; exit 1\n          fi\n"
	good := head + stamp + prBuild + restore + setupGo + scan + ciHead + ciAssert

	commentedScan := "      # - name: Scan both gateway binaries\n      #   run: GATEWAY_BINARY=/tmp/gateway-prod GATEWAY_BINARY_MOCKISSUER=/tmp/gateway-mockissuer go test ./cmd/gateway/ -count=1 -v\n"
	continued := "      - name: Scan both gateway binaries\n        run: |\n          GATEWAY_BINARY=/tmp/gateway-prod \\\n          GATEWAY_BINARY_MOCKISSUER=/tmp/gateway-mockissuer \\\n          go test ./cmd/gateway/ -count=1 -v%s\n"
	decoyGo := "  go:\n    runs-on: ubuntu-latest\n    steps:\n      - run: go test ./...\n" + stamp + restore + scan

	for _, c := range []struct {
		name string
		yaml string
		want int
	}{
		{"the planned job", good, 0},
		{"the scan as a continued block scalar", strings.Replace(good, scan, fmt.Sprintf(continued, ""), 1), 0},
		{"the test line deleted", strings.Replace(good, scan, "", 1), 1},
		{"the restore after the test", strings.Replace(strings.Replace(good, restore, "", 1), scan, scan+restore, 1), 1},
		{"no restore", strings.Replace(good, restore, "", 1), 1},
		{"the restore before the stamp", strings.Replace(strings.Replace(good, restore, "", 1), stamp, restore+stamp, 1), 1},
		{"no stamp", strings.Replace(good, stamp, "", 1), 1},
		{"the test line commented out", strings.Replace(good, scan, commentedScan, 1), 1},
		{"-run on the test line", strings.Replace(good, "go test ./cmd/gateway/ -count=1", "go test ./cmd/gateway/ -run 'Gateway' -count=1", 1), 1},
		{"-run on a continuation line", strings.Replace(good, scan, fmt.Sprintf(continued, " \\\n            -run=Gateway"), 1), 1},
		{"GATEWAY_BINARY_MOCKISSUER missing", strings.Replace(good, "GATEWAY_BINARY_MOCKISSUER=/tmp/gateway-mockissuer ", "", 1), 1},
		{"GATEWAY_BINARY missing", strings.Replace(good, "GATEWAY_BINARY=/tmp/gateway-prod ", "", 1), 1},
		{"the steps only in another job", strings.Replace(strings.Replace(strings.Replace(strings.Replace(good, stamp, "", 1), restore, "", 1), scan, "", 1), "  go:\n    runs-on: ubuntu-latest\n    steps:\n      - run: go test ./...\n", decoyGo, 1), 3},
		{"ci needs: without docker-canary", strings.Replace(good, ciNeeds, "    needs: [changes, go]\n", 1), 1},
		{"ci shell block only echoes docker-canary", strings.Replace(good, ciAssert, "", 1), 1},
	} {
		if c.name != "the planned job" && c.yaml == good {
			t.Fatalf("fixture %q: the replacement did not apply", c.name)
		}
		if got := dockerCanaryProblems(c.yaml); len(got) != c.want {
			t.Errorf("fixture %q: %d problem(s) %v, want %d", c.name, len(got), got, c.want)
		}
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	lines := yamlCode(string(raw))
	// Controls: the scan reads the real docker-canary job and the real aggregate.
	canary := jobBlock(lines, "docker-canary")
	if len(canary) < 10 || !strings.Contains(strings.Join(canary, "\n"), "SERVICE=gateway") {
		t.Fatalf("read %d docker-canary line(s) without `SERVICE=gateway`; the block scan is broken:\n%s", len(canary), strings.Join(canary, "\n"))
	}
	ci := jobBlock(lines, "ci")
	if !slices.Contains(jobNeeds(ci), "go") || !aggregateFailsOn(runText(ci), "go") {
		t.Fatalf("the ci job's needs: %v or shell block does not assert `go`; the aggregate scan is broken", jobNeeds(ci))
	}
	for _, p := range dockerCanaryProblems(string(raw)) {
		t.Errorf(".github/workflows/ci.yml: %s", p)
	}
}
