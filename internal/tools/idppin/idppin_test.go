package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	pinnedTag   = "v2.197.0"
	plannedFrom = "FROM ghcr.io/supabase/auth:v2.197.0@sha256:1736a63078f5922b198c4cbe50f80ab9a2d3b54fe8b7b6cfb2e9dc5dbbc12c6b"
	plannedBody = plannedFrom + "\n\nENV GOTRUE_DB_DRIVER=postgres \\\n    GOTRUE_JWT_AUD=authenticated\n"
)

var committedDockerfile = filepath.Join("..", "..", "..", "sidecar", "auth", "Dockerfile")

var binPath string

// A built binary keeps the real exit codes; `go run` collapses every failure to 1.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "idppin-cli-test")
	if err != nil {
		panic(err)
	}
	binPath = filepath.Join(dir, "idppin")
	if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		panic("building idppin: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func runIdppin(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("failed to run idppin %v: %v", args, err)
		}
		exitCode = exitErr.ExitCode()
	}
	return outBuf.String(), errBuf.String(), exitCode
}

func writeDockerfile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "Dockerfile")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// baseDockerfile prefers the committed file so the refusals track its real shape.
func baseDockerfile() string {
	if raw, err := os.ReadFile(committedDockerfile); err == nil {
		return string(raw)
	}
	return plannedBody
}

func fromLine(t *testing.T, body string) string {
	t.Helper()
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(l)), "FROM ") {
			return strings.TrimSpace(l)
		}
	}
	t.Fatalf("base Dockerfile has no FROM line:\n%s", body)
	return ""
}

func TestParsePin(t *testing.T) {
	t.Run("committed sidecar/auth/Dockerfile", func(t *testing.T) {
		raw, err := os.ReadFile(committedDockerfile)
		if err != nil {
			t.Fatalf("read the committed Dockerfile: %v", err)
		}
		got, err := ParsePin(string(raw))
		if err != nil || got != pinnedTag {
			t.Errorf("ParsePin(committed) = %q, %v; want %q, nil", got, err, pinnedTag)
		}
	})

	t.Run("planned FROM line", func(t *testing.T) {
		got, err := ParsePin(plannedBody)
		if err != nil || got != pinnedTag {
			t.Errorf("ParsePin = %q, %v; want %q, nil", got, err, pinnedTag)
		}
	})

	base := baseDockerfile()
	from := fromLine(t, base)
	ref := strings.TrimSpace(from[len("FROM "):])
	noDigest, _, _ := strings.Cut(ref, "@")
	hubRef := strings.TrimPrefix(ref, "ghcr.io/")

	// Each refusal is the accepted base with one edit, so the base must parse first.
	refusals := []struct{ name, body string }{
		{"FROM without a digest", strings.Replace(base, from, "FROM "+noDigest, 1)},
		{"FROM on docker.io", strings.Replace(base, from, "FROM docker.io/"+hubRef, 1)},
		{"unqualified Docker Hub name", strings.Replace(base, from, "FROM "+hubRef, 1)},
		{"zero FROM lines", strings.Replace(base, from, "", 1)},
		{"two FROM lines", strings.Replace(base, from, from+"\n"+from, 1)},
	}
	for _, r := range refusals {
		t.Run(r.name, func(t *testing.T) {
			if r.body == base {
				t.Fatal("the edit did not change the base Dockerfile; the refusal would be vacuous")
			}
			if got, err := ParsePin(base); err != nil || got != pinnedTag {
				t.Fatalf("control: ParsePin(base) = %q, %v; want %q, nil", got, err, pinnedTag)
			}
			if got, err := ParsePin(r.body); err == nil {
				t.Errorf("ParsePin accepted it and returned %q; want an error\n%s", got, r.body)
			}
		})
	}
}

func TestTagPrintsThePinnedTag(t *testing.T) {
	stdout, stderr, code := runIdppin(t, "tag", writeDockerfile(t, plannedBody))
	if code != 0 || stdout != pinnedTag+"\n" {
		t.Errorf("idppin tag: exit %d stdout %q stderr %q; want exit 0 and %q", code, stdout, stderr, pinnedTag+"\n")
	}
}

func TestLatestCheckExitCodes(t *testing.T) {
	df := writeDockerfile(t, plannedBody)
	for _, c := range []struct {
		latest string
		want   int
	}{
		{"v2.197.0", 0},
		{"v2.198.0", 1},
	} {
		stdout, stderr, code := runIdppin(t, "latest-check", df, c.latest)
		if code != c.want {
			t.Errorf("latest-check pinned %s vs %s: exit %d, want %d; stdout=%q stderr=%q",
				pinnedTag, c.latest, code, c.want, stdout, stderr)
		}
	}
}

// Production signup stays closed until it is opened by hand (AUTH-03 D12).
func TestAuthImageKeepsSignupClosed(t *testing.T) {
	t.Run("parser control", func(t *testing.T) {
		for body, want := range map[string]string{
			"ENV GOTRUE_DISABLE_SIGNUP=true":                                    "true",
			"ENV A=1 \\\n    GOTRUE_DISABLE_SIGNUP=false \\\n    B=2":           "false",
			"env GOTRUE_DISABLE_SIGNUP=false":                                   "false",
			"# ENV GOTRUE_DISABLE_SIGNUP=true\nENV GOTRUE_DISABLE_SIGNUP=false": "false",
		} {
			if got := dockerfileEnv(t, body)["GOTRUE_DISABLE_SIGNUP"]; got != want {
				t.Errorf("dockerfileEnv(%q) GOTRUE_DISABLE_SIGNUP = %q, want %q", body, got, want)
			}
		}
		if _, ok := dockerfileEnv(t, "# ENV GOTRUE_DISABLE_SIGNUP=true\n")["GOTRUE_DISABLE_SIGNUP"]; ok {
			t.Error("a commented-out ENV line was parsed")
		}
	})

	raw, err := os.ReadFile(committedDockerfile)
	if err != nil {
		t.Fatalf("reading the committed %s: %v", committedDockerfile, err)
	}
	env := dockerfileEnv(t, string(raw))
	if len(env) == 0 {
		t.Fatal("control: the committed Dockerfile sets no ENV, so the parser read nothing")
	}
	got, ok := env["GOTRUE_DISABLE_SIGNUP"]
	if !ok {
		t.Fatalf("sidecar/auth/Dockerfile sets no GOTRUE_DISABLE_SIGNUP; ENV keys = %v", env)
	}
	if got != "true" {
		t.Errorf("sidecar/auth/Dockerfile GOTRUE_DISABLE_SIGNUP = %q, want \"true\"", got)
	}
}
