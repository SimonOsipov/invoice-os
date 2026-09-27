package jev

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// retiredRoots are the source roots the guard walks; docs/ and frontend/ name the retired strings on purpose.
var retiredRoots = []string{"cmd", "internal", "scripts", "tools", ".github", "e2e"}

const (
	retiredHost   = "api.typesafe.ai"
	retiredKeyVar = "TYPESAFE_API_KEY"
)

// retiredRouteScan walks retiredRoots under root and returns the scanned files and the hits, slash-relative.
// Comments count: a comment naming the retired route is as stale as code. The host matches in any
// case (DNS is case-insensitive); the env var matches exact case, as the shell reads it.
func retiredRouteScan(t *testing.T, root string) (scanned, hits []string) {
	t.Helper()
	for _, top := range retiredRoots {
		dir := filepath.Join(root, top)
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			t.Fatalf("scan root %s is not a directory under %s: %v", top, root, err)
		}
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || strings.HasSuffix(d.Name(), "_test.go") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			scanned = append(scanned, rel)
			src := string(b)
			switch {
			case strings.Contains(strings.ToLower(src), retiredHost):
				hits = append(hits, rel+": names "+retiredHost)
			case strings.HasSuffix(rel, ".go") && strings.Contains(src, retiredKeyVar):
				hits = append(hits, rel+": names "+retiredKeyVar)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", top, err)
		}
	}
	return scanned, hits
}

func TestNoSourceNamesTheRetiredRoute(t *testing.T) {
	// The planted tree is built from retiredRoots, so only this pin sees a root dropped from it.
	if want := []string{"cmd", "internal", "scripts", "tools", ".github", "e2e"}; !slices.Equal(retiredRoots, want) {
		t.Errorf("retiredRoots = %v, want %v", retiredRoots, want)
	}
	root := jevRepoRoot(t)
	scanned, hits := retiredRouteScan(t, root)

	// Today's walk scans ~460 files; the floor catches a walk that lost a root.
	const floor = 300
	if len(scanned) < floor {
		t.Fatalf("scanned %d file(s), want at least %d -- a truncated walk would report clean", len(scanned), floor)
	}
	for _, top := range retiredRoots {
		if !slices.ContainsFunc(scanned, func(s string) bool { return strings.HasPrefix(s, top+"/") }) {
			t.Errorf("the walk scanned no file under %s/", top)
		}
	}

	const railwayEnv = "scripts/ci/railway-env.sh"
	for _, want := range []string{"internal/platform/jev/client.go", railwayEnv} {
		if !slices.Contains(scanned, want) {
			t.Errorf("the walk did not visit %s", want)
		}
	}
	// The key-name rule stops at .go files because set-ai-fake still blanks and audits the retired key.
	if !strings.Contains(readRepoFile(t, root, railwayEnv), retiredKeyVar) {
		t.Errorf("%s no longer names %s -- the .sh control below proves nothing; drop it with the retired-key blank", railwayEnv, retiredKeyVar)
	}
	for _, h := range hits {
		if strings.HasPrefix(h, railwayEnv+":") {
			t.Errorf("flagged %s, want the key-name rule limited to .go files: %s", railwayEnv, h)
		}
	}

	for _, h := range hits {
		t.Errorf("non-test source names the retired route: %s", h)
	}
}

func TestRetiredRouteScan_FindsAPlantedNeedle(t *testing.T) {
	root := t.TempDir()
	for _, top := range retiredRoots {
		if err := os.MkdirAll(filepath.Join(root, top), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	both := "curl https://API.TypeSafe.ai/v1/systemone\n" + retiredKeyVar + "=x\n"
	plant := map[string]string{
		"scripts/host.sh":               "curl https://API.TypeSafe.ai/v1/systemone\n",
		"internal/x/key.go":             "package x\n\nconst k = \"" + retiredKeyVar + "\"\n",
		"scripts/key.sh":                retiredKeyVar + "=\"\"\n",
		"internal/x/both_test.go":       both,
		"e2e/node_modules/pkg/index.js": both,
		"tools/clean.go":                "package tools\n",
		"cmd/lower_key.go":              "package cmd\n\n// typesafe_api_key is not the env var.\n",
	}
	for rel, body := range plant {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	scanned, hits := retiredRouteScan(t, root)
	if len(scanned) == 0 {
		t.Fatal("scanned zero files in the planted tree")
	}
	if !slices.Contains(scanned, "scripts/key.sh") {
		t.Errorf("the walk did not visit scripts/key.sh, so its absence from hits proves nothing")
	}
	for _, skipped := range []string{"internal/x/both_test.go", "e2e/node_modules/pkg/index.js"} {
		if slices.Contains(scanned, skipped) {
			t.Errorf("the walk scanned %s, want it skipped", skipped)
		}
	}

	var flagged []string
	for _, h := range hits {
		flagged = append(flagged, strings.SplitN(h, ":", 2)[0])
	}
	slices.Sort(flagged)
	want := []string{"internal/x/key.go", "scripts/host.sh"}
	if !slices.Equal(flagged, want) {
		t.Errorf("flagged %v, want exactly %v (hits: %q)", flagged, want, hits)
	}
}
