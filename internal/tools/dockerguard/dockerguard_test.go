package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	h1 = "1111111111111111111111111111111111111111111111111111111111111111"
	h2 = "2222222222222222222222222222222222222222222222222222222222222222"

	golang = "golang:1.26-alpine@sha256:" + h1
	distro = "gcr.io/distroless/static@sha256:" + h2
	node   = "node:22.23.5-alpine@sha256:" + h1
	caddy  = "caddy:2.11.7-alpine@sha256:" + h2
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// repo writes files (path to content), tracks all but those listed in untracked.
func repo(t *testing.T, files map[string]string, untracked ...string) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	for p, c := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	skip := map[string]bool{}
	for _, u := range untracked {
		skip[u] = true
	}
	for p := range files {
		if !skip[p] {
			git(t, dir, "add", p)
		}
	}
	return dir
}

func dependabot(dirs ...string) string {
	s := "version: 2\nupdates:\n  - package-ecosystem: npm\n    directory: /\n  - package-ecosystem: docker\n    directories:\n"
	for _, d := range dirs {
		s += "      - " + d + "\n"
	}
	return s
}

const depPath = ".github/dependabot.yml"

// one is a repo with a single root Dockerfile and a matching Dependabot entry.
func one(t *testing.T, dockerfile string) (int, string, string) {
	t.Helper()
	return do(repo(t, map[string]string{"Dockerfile": dockerfile, depPath: dependabot("/")}))
}

func do(dir string) (int, string, string) {
	var o, e bytes.Buffer
	code := run(dir, &o, &e)
	return code, o.String(), e.String()
}

func wantExit(t *testing.T, got, want int, stderr string) {
	t.Helper()
	if got != want {
		t.Fatalf("exit = %d, want %d\nstderr: %s", got, want, stderr)
	}
}

func wantHas(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Fatalf("missing %q in:\n%s", sub, s)
	}
}

func TestRun_RepoIsClean(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := do(strings.TrimSpace(string(out)))
	wantExit(t, code, 0, stderr)
	m := regexp.MustCompile(`(\d+) Dockerfiles, (\d+) external FROM lines, all pinned`).FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("unexpected stdout %q", stdout)
	}
	for _, s := range m[1:] {
		if n, _ := strconv.Atoi(s); n < 1 {
			t.Fatalf("population floor: %q", stdout)
		}
	}
}

func TestRun_AllPinnedPasses(t *testing.T) {
	t.Parallel()
	dir := repo(t, map[string]string{
		"Dockerfile": "FROM " + golang + " AS build\nFROM " + distro + "\nUSER nonroot:nonroot\n",
		"frontend/x/Dockerfile": "FROM " + node + " AS build\nFROM " + caddy + " AS serve\n" +
			"RUN setcap -r /usr/bin/caddy\nUSER nobody:nobody\n",
		depPath: dependabot("/", "/frontend/x"),
	})
	code, stdout, stderr := do(dir)
	wantExit(t, code, 0, stderr)
	wantHas(t, stdout, "2 Dockerfiles, 4 external FROM lines")
}

func TestRun_TagOnlyFromFailsNamingFileAndLine(t *testing.T) {
	t.Parallel()
	dir := repo(t, map[string]string{
		"frontend/x/Dockerfile": "# c\n\nFROM node:22-alpine AS build\n",
		depPath:                 dependabot("/frontend/x"),
	})
	code, _, stderr := do(dir)
	wantExit(t, code, 1, stderr)
	wantHas(t, stderr, "frontend/x/Dockerfile:3: FROM node:22-alpine carries no @sha256 digest")
}

func TestRun_PlatformFlagDoesNotHideTheImage(t *testing.T) {
	t.Parallel()
	code, _, stderr := one(t, "FROM --platform=$BUILDPLATFORM node:22-alpine AS build\n")
	wantExit(t, code, 1, stderr)
	wantHas(t, stderr, "node:22-alpine")
	code, _, stderr = one(t, "FROM --platform=$BUILDPLATFORM "+node+" AS build\n")
	wantExit(t, code, 0, stderr)
}

func TestRun_LowercaseAndContinuedFromAreRead(t *testing.T) {
	t.Parallel()
	code, _, stderr := one(t, "from node:22-alpine\n")
	wantExit(t, code, 1, stderr)
	wantHas(t, stderr, "Dockerfile:1: FROM node:22-alpine")
	code, _, stderr = one(t, "# c\nFROM \\\n  node:22-alpine AS build\n")
	wantExit(t, code, 1, stderr)
	wantHas(t, stderr, "Dockerfile:2: FROM node:22-alpine")
}

func TestRun_DigestMustBeSha256Of64Hex(t *testing.T) {
	t.Parallel()
	for _, img := range []string{
		"node@sha256:abc",
		"node@sha512:" + strings.Repeat("a", 128),
		"node:22@sha256:" + strings.Repeat("A", 64),
	} {
		code, _, stderr := one(t, "FROM "+img+"\n")
		wantExit(t, code, 1, stderr)
		wantHas(t, stderr, img)
	}
}

func TestRun_VariableImageFails(t *testing.T) {
	t.Parallel()
	code, _, stderr := one(t, "ARG BASE=node@sha256:"+h1+"\nFROM $BASE\n")
	wantExit(t, code, 1, stderr)
	wantHas(t, stderr, "through a variable ($BASE)")
}

func TestRun_InternalStageAndScratchPass(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := one(t, "FROM "+node+" AS deps\nFROM Deps AS test\nFROM scratch\nUSER 65532\n")
	wantExit(t, code, 0, stderr)
	wantHas(t, stdout, "1 external FROM lines")
}

func TestRun_StageNameBeforeItsDeclarationIsAnImage(t *testing.T) {
	t.Parallel()
	code, _, stderr := one(t, "FROM deps\nFROM "+node+" AS deps\n")
	wantExit(t, code, 1, stderr)
	wantHas(t, stderr, "Dockerfile:1: FROM deps")
}

func TestRun_CommentedFromIsIgnored(t *testing.T) {
	t.Parallel()
	code, _, stderr := one(t, "# FROM node:22-alpine\nFROM "+node+"\n")
	wantExit(t, code, 0, stderr)
	code, _, stderr = one(t, "# FROM "+node+"\n")
	wantExit(t, code, 1, stderr)
	wantHas(t, stderr, "no FROM line")
}

func TestRun_LeadingSyntaxLineIsAComment(t *testing.T) {
	t.Parallel()
	code, _, stderr := one(t, "# syntax=docker/dockerfile:1\nFROM "+node+"\n")
	wantExit(t, code, 0, stderr)
}

func TestRun_DockerfileDirMissingFromDependabotFails(t *testing.T) {
	t.Parallel()
	dir := repo(t, map[string]string{
		"Dockerfile":              "FROM " + node + "\n",
		"frontend/new/Dockerfile": "FROM " + node + "\n",
		depPath:                   dependabot("/"),
	})
	code, _, stderr := do(dir)
	wantExit(t, code, 1, stderr)
	wantHas(t, stderr, "lacks /frontend/new")
}

func TestRun_DependabotDirWithoutDockerfileFails(t *testing.T) {
	t.Parallel()
	dir := repo(t, map[string]string{
		"Dockerfile": "FROM " + node + "\n",
		depPath:      dependabot("/", "/frontend/gone"),
	})
	code, _, stderr := do(dir)
	wantExit(t, code, 1, stderr)
	wantHas(t, stderr, "lists /frontend/gone")
}

func TestRun_DependabotSingleDirectoryKeyIsRead(t *testing.T) {
	t.Parallel()
	dir := repo(t, map[string]string{
		"Dockerfile": "FROM " + node + "\n",
		depPath:      "version: 2\nupdates:\n  - package-ecosystem: docker\n    directory: /\n",
	})
	code, _, stderr := do(dir)
	wantExit(t, code, 0, stderr)
}

func TestRun_NoDockerEntryFails(t *testing.T) {
	t.Parallel()
	dir := repo(t, map[string]string{
		"Dockerfile": "FROM " + node + "\n",
		depPath:      "version: 2\nupdates:\n  - package-ecosystem: npm\n    directory: /\n",
	})
	code, _, stderr := do(dir)
	wantExit(t, code, 1, stderr)
	wantHas(t, stderr, "no package-ecosystem: docker entry")
}

func TestRun_UnparsableDependabotIsExit2(t *testing.T) {
	t.Parallel()
	dir := repo(t, map[string]string{"Dockerfile": "FROM " + node + "\n", depPath: "updates: [\n"})
	code, _, stderr := do(dir)
	wantExit(t, code, 2, stderr)
}

func TestRun_FindsEveryDockerfileNameAndSkipsIgnoreFiles(t *testing.T) {
	t.Parallel()
	tag := "FROM node:22-alpine\n"
	dir := repo(t, map[string]string{
		"Dockerfile":                tag,
		"a/Dockerfile.dev":          tag,
		"b/app.Dockerfile":          tag,
		"c/Containerfile":           tag,
		"a/Dockerfile.dockerignore": tag,
		"d/Dockerfile":              tag,
		depPath:                     dependabot("/", "/a", "/b", "/c"),
	}, "d/Dockerfile")
	code, _, stderr := do(dir)
	wantExit(t, code, 1, stderr)
	if n := strings.Count(stderr, "carries no @sha256 digest"); n != 4 {
		t.Fatalf("FROM problems = %d, want 4\n%s", n, stderr)
	}
	for _, bad := range []string{"dockerignore", "d/Dockerfile", "lacks", "lists"} {
		if strings.Contains(stderr, bad) {
			t.Fatalf("unexpected %q in:\n%s", bad, stderr)
		}
	}
}

func TestRun_NoDockerfileIsExit2(t *testing.T) {
	t.Parallel()
	code, _, stderr := do(repo(t, map[string]string{depPath: dependabot("/")}))
	wantExit(t, code, 2, stderr)
}

func TestRun_NotAGitRepoIsExit2(t *testing.T) {
	t.Parallel()
	code, _, stderr := do(t.TempDir())
	wantExit(t, code, 2, stderr)
}

func TestRun_UnreadableTrackedDockerfileIsExit2(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	dir := repo(t, map[string]string{"Dockerfile": "FROM " + node + "\n", depPath: dependabot("/")})
	if err := os.Chmod(filepath.Join(dir, "Dockerfile"), 0); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := do(dir)
	wantExit(t, code, 2, stderr)
}

func TestRun_ReportsEveryProblem(t *testing.T) {
	t.Parallel()
	dir := repo(t, map[string]string{
		"a/Dockerfile": "FROM node:22-alpine\n",
		"b/Dockerfile": "ARG BASE=x\nFROM $BASE\n",
		depPath:        dependabot("/a", "/b"),
	})
	code, _, stderr := do(dir)
	wantExit(t, code, 1, stderr)
	wantHas(t, stderr, "a/Dockerfile:1: FROM node:22-alpine")
	wantHas(t, stderr, fmt.Sprintf("b/Dockerfile:%d: FROM names its image through a variable", 2))
}
