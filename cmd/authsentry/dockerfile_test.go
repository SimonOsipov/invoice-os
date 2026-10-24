package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestAuthImage_RunsGoTrueUnderTheWrapper(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "sidecar", "auth", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	var final []string
	for _, l := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(strings.ToUpper(l), "FROM ") {
			final = nil
		}
		final = append(final, l)
	}
	if !strings.Contains(final[0], "/supabase/auth:") {
		t.Fatalf("final stage starts %q, want the supabase/auth FROM", final[0])
	}
	for _, want := range []string{
		`COPY --from=wrap /out/authsentry /usr/local/bin/authsentry`,
		`ENTRYPOINT ["/usr/local/bin/authsentry"]`,
		`CMD ["auth"]`,
	} {
		found := false
		for _, l := range final {
			found = found || l == want
		}
		if !found {
			t.Errorf("final stage lacks %q", want)
		}
	}
}

func TestAuthImage_FinalStageKeepsGoTrueUser(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "sidecar", "auth", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	var final []string
	for _, l := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(strings.ToUpper(l), "FROM ") {
			final = nil
		}
		final = append(final, l)
	}
	if len(final) < 3 {
		t.Fatalf("final stage has %d lines; the scan read nothing", len(final))
	}
	for _, l := range final {
		if strings.HasPrefix(strings.ToUpper(l), "USER ") {
			t.Errorf("final stage sets %q; it must keep the upstream user", l)
		}
	}
}

// The wrap stage builds the binary the final stage copies, from files the context keeps.
func TestAuthImage_WrapStageBuildsFromTheKeptContext(t *testing.T) {
	dir := filepath.Join("..", "..", "sidecar", "auth")
	raw, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	df := string(raw)
	if !regexp.MustCompile(`(?mi)^FROM \S+ AS wrap\s*$`).MatchString(df) {
		t.Error("no stage named wrap")
	}
	if !strings.Contains(df, "-o /out/authsentry ./cmd/authsentry") {
		t.Error("wrap stage does not build ./cmd/authsentry to /out/authsentry")
	}

	ign, err := os.ReadFile(filepath.Join(dir, "Dockerfile.dockerignore"))
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, l := range strings.Split(string(ign), "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "!") {
			kept = append(kept, strings.TrimPrefix(l, "!"))
		}
	}
	out, err := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.Dir}}{{end}}", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	n := 0
	for _, d := range strings.Fields(string(out)) {
		rel, err := filepath.Rel(root, d)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		n++
		ok := false
		for _, k := range kept {
			ok = ok || rel == k || strings.HasPrefix(rel, k+"/")
			if ok && !strings.Contains(df, "COPY "+k+" ") && k != "go.mod" && k != "go.sum" {
				t.Errorf("dockerignore keeps %s but the wrap stage does not COPY it", k)
			}
		}
		if !ok {
			t.Errorf("dockerignore drops %s, which cmd/authsentry imports", rel)
		}
	}
	if n < 3 {
		t.Fatalf("found %d in-module packages; the scan read nothing", n)
	}
}
