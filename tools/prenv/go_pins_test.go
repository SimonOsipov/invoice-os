// go_pins_test.go pins go.mod's go version and the Dockerfile golang tag to one full x.y.z.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	goDirectiveRE = regexp.MustCompile(`(?m)^go\s+(\S+)\s*$`)
	toolchainRE   = regexp.MustCompile(`(?m)^toolchain\s`)
	golangFromRE  = regexp.MustCompile(`(?mi)^FROM\s+(?:\S*/)?golang:([^\s@]+)`)
	fullVersionRE = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
)

func goPinProblems(goMod, dockerfile string) []string {
	var problems []string
	goVer, tag := "", ""
	if m := goDirectiveRE.FindStringSubmatch(goMod); m == nil {
		problems = append(problems, "go.mod has no go directive")
	} else if goVer = m[1]; !fullVersionRE.MatchString(goVer) {
		problems = append(problems, fmt.Sprintf("go.mod go %q is not a full x.y.z version", goVer))
	}
	if toolchainRE.MatchString(goMod) {
		problems = append(problems, "go.mod has a toolchain line")
	}
	if m := golangFromRE.FindStringSubmatch(dockerfile); m == nil {
		problems = append(problems, "Dockerfile has no golang FROM")
	} else {
		tag = strings.TrimSuffix(m[1], "-alpine")
		if !fullVersionRE.MatchString(tag) {
			problems = append(problems, fmt.Sprintf("Dockerfile golang tag %q is not a full x.y.z version", tag))
		}
	}
	if fullVersionRE.MatchString(goVer) && fullVersionRE.MatchString(tag) && goVer != tag {
		problems = append(problems, fmt.Sprintf("go.mod go %s and Dockerfile tag %s differ", goVer, tag))
	}
	return problems
}

func wantGoPinProblem(t *testing.T, goMod, dockerfile, substr string) {
	t.Helper()
	problems := goPinProblems(goMod, dockerfile)
	for _, p := range problems {
		if strings.Contains(p, substr) {
			return
		}
	}
	t.Errorf("goPinProblems = %q, want one containing %q", problems, substr)
}

const (
	pinGoMod      = "module x\n\ngo 1.26.9\n"
	pinDockerfile = "FROM mirror.gcr.io/library/golang:1.26.9-alpine AS build\n"
)

func TestGoPins_RepoAgree(t *testing.T) {
	root := repoRoot(t)
	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if p := goPinProblems(string(goMod), string(dockerfile)); len(p) != 0 {
		t.Errorf("go pin problems: %q", p)
	}
	v := goDirectiveRE.FindStringSubmatch(string(goMod))
	f := golangFromRE.FindStringSubmatch(string(dockerfile))
	if v == nil || f == nil || v[1] == "" || strings.TrimSuffix(f[1], "-alpine") != v[1] {
		t.Errorf("control: parsed go version %q and Dockerfile tag %q, want equal and non-empty", v, f)
	}
}

func TestGoPins_MismatchedTagFails(t *testing.T) {
	d := strings.Replace(pinDockerfile, "1.26.9", "1.26.8", 1)
	got := goPinProblems(pinGoMod, d)
	if len(got) != 1 || !strings.Contains(got[0], "differ") {
		t.Errorf("goPinProblems = %q, want one problem containing differ", got)
	}
}

func TestGoPins_FloatingTagFails(t *testing.T) {
	wantGoPinProblem(t, pinGoMod, "FROM golang:1.26-alpine AS build\n", "not a full x.y.z version")
}

func TestGoPins_GoDirectiveWithoutPatchFails(t *testing.T) {
	wantGoPinProblem(t, "module x\n\ngo 1.26\n", pinDockerfile, "not a full x.y.z version")
}

func TestGoPins_ToolchainLineFails(t *testing.T) {
	wantGoPinProblem(t, pinGoMod+"\ntoolchain go1.26.9\n", pinDockerfile, "toolchain line")
}

func TestGoPins_NoGolangFromFails(t *testing.T) {
	wantGoPinProblem(t, pinGoMod, "FROM alpine\n", "no golang FROM")
}

func TestGoPins_NoGoDirectiveFails(t *testing.T) {
	wantGoPinProblem(t, "module x\n", pinDockerfile, "no go directive")
}

func TestGoPins_CommentedFromIgnored(t *testing.T) {
	d := "# FROM golang:1.20-alpine\n" + pinDockerfile
	if p := goPinProblems(pinGoMod, d); len(p) != 0 {
		t.Errorf("goPinProblems = %q, want none", p)
	}
}

func TestGoPins_LowercaseFromParsed(t *testing.T) {
	wantGoPinProblem(t, pinGoMod, "from golang:1.26.8-alpine\n", "differ")
}
