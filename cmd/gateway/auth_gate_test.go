package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goJobRunsJWKSUnderRace reports whether one command line of one `go` job step
// runs the TestJWKS_ tests under -race; needles split across steps do not count.
func goJobRunsJWKSUnderRace(ciYAML string) bool {
	for _, step := range jobSteps(jobBlock(yamlCode(ciYAML), "go")) {
		for _, line := range strings.Split(runText(step), "\n") {
			if strings.Contains(line, "go test") && strings.Contains(line, "-race") &&
				strings.Contains(line, "TestJWKS_") && strings.Contains(line, "./internal/platform/auth/") {
				return true
			}
		}
	}
	return false
}

func TestCIGoJobRunsJWKSTestsUnderRace(t *testing.T) {
	const head = "on: push\njobs:\n  go:\n    runs-on: ubuntu-latest\n    steps:\n      - name: Test\n        run: go test ./...\n"
	const step = "      - name: JWKS under race\n        run: go test -race -count=1 -run 'TestJWKS_' ./internal/platform/auth/\n"
	const blockStep = "      - name: JWKS under race\n        run: |\n          go test -race -count=1 -run 'TestJWKS_' ./internal/platform/auth/\n"
	const noRace = "      - name: JWKS\n        run: go test -count=1 -run 'TestJWKS_' ./internal/platform/auth/\n"
	const split = "      - run: go test -race ./...\n      - run: go test -run 'TestJWKS_' ./internal/platform/auth/\n"
	const commented = "      # - run: go test -race -count=1 -run 'TestJWKS_' ./internal/platform/auth/\n"
	const tail = "  docker-canary:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo canary\n"

	for _, c := range []struct {
		name string
		yaml string
		want bool
	}{
		{"with the step", head + step + tail, true},
		{"with the step as a block scalar", head + blockStep + tail, true},
		{"without the step", head + tail, false},
		{"without -race", head + noRace + tail, false},
		{"needles split across steps", head + split + tail, false},
		{"step commented out", head + commented + tail, false},
		{"step in another job", head + tail + step, false},
	} {
		if got := goJobRunsJWKSUnderRace(c.yaml); got != c.want {
			t.Errorf("fixture %q: runs JWKS under race = %v, want %v", c.name, got, c.want)
		}
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// Control: the scan finds the real go job's steps before judging them.
	steps := jobSteps(jobBlock(yamlCode(string(raw)), "go"))
	if len(steps) == 0 {
		t.Fatal("found no steps in the ci.yml go job; the scan is broken")
	}
	if !goJobRunsJWKSUnderRace(string(raw)) {
		t.Errorf(".github/workflows/ci.yml: no `go` job step runs `go test -race -run 'TestJWKS_' ./internal/platform/auth/`")
	}
}
