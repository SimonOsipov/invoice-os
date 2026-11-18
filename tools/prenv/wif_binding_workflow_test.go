package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The table in ci-gcp.md mirrors the list in invoice-os-infra bootstrap/wif-provider.yaml.
// A workflow that disagrees with it fails here, not at the next deploy.

type wifRow struct{ deployer, file string }

var (
	wifRowRe      = regexp.MustCompile("^\\| `(deploy-[a-z]+)` \\| `([^`]+)`")
	wifDeployerRe = regexp.MustCompile(`(deploy-[a-z]+)@ascomply-(?:prod|preview)\.iam\.gserviceaccount\.com`)
)

func wifTableRows(md string) []wifRow {
	var rows []wifRow
	for _, line := range strings.Split(md, "\n") {
		if m := wifRowRe.FindStringSubmatch(line); m != nil {
			rows = append(rows, wifRow{m[1], m[2]})
		}
	}
	return rows
}

// wifTableFaults compares the table with the workflow texts, keyed by file name.
func wifTableFaults(rows []wifRow, texts map[string]string) []string {
	const where = "; the list lives in invoice-os-infra bootstrap/wif-provider.yaml"
	listed := map[string]map[string]bool{}
	for _, r := range rows {
		if listed[r.file] == nil {
			listed[r.file] = map[string]bool{}
		}
		listed[r.file][r.deployer] = true
	}
	var faults []string
	for file := range listed {
		if _, ok := texts[file]; !ok {
			faults = append(faults, fmt.Sprintf("ci-gcp.md table names %s, which does not exist%s", file, where))
		}
	}
	for file, text := range texts {
		code := strings.Join(stripHashComments(strings.Split(text, "\n")), "\n")
		if !strings.Contains(code, "google-github-actions/auth") {
			continue
		}
		if listed[file] == nil {
			faults = append(faults, fmt.Sprintf("%s calls google-github-actions/auth but has no ci-gcp.md table row%s", file, where))
			continue
		}
		for _, m := range wifDeployerRe.FindAllStringSubmatch(code, -1) {
			if !listed[file][m[1]] {
				faults = append(faults, fmt.Sprintf("%s names %s, which the ci-gcp.md table does not give it%s", file, m[1], where))
			}
		}
	}
	sort.Strings(faults)
	return faults
}

func wifReal(t *testing.T) ([]wifRow, map[string]string) {
	t.Helper()
	md, err := os.ReadFile(filepath.Join(repoRoot(t), ".claude", "rules", "ci-gcp.md"))
	if err != nil {
		t.Fatal(err)
	}
	rows := wifTableRows(string(md))
	if len(rows) == 0 {
		t.Fatal("no table rows in ci-gcp.md")
	}
	_, texts := workflowTexts(t)
	return rows, texts
}

func TestWIFTable_EveryAuthWorkflowIsListed(t *testing.T) {
	t.Parallel()
	rows, texts := wifReal(t)
	for _, f := range wifTableFaults(rows, texts) {
		if strings.Contains(f, "no ci-gcp.md table row") {
			t.Error(f)
		}
	}
}

func TestWIFTable_FaultsNameAnUnlistedFile(t *testing.T) {
	t.Parallel()
	texts := map[string]string{"new.yml": "steps:\n  - uses: google-github-actions/auth@v2\n"}
	got := wifTableFaults(nil, texts)
	if len(got) != 1 || !strings.Contains(got[0], "new.yml") || !strings.Contains(got[0], "wif-provider.yaml") {
		t.Fatalf("faults = %q", got)
	}
}

func TestWIFTable_EveryDeployerEmailIsListedForItsFile(t *testing.T) {
	t.Parallel()
	rows, texts := wifReal(t)
	for _, f := range wifTableFaults(rows, texts) {
		if strings.Contains(f, "does not give it") {
			t.Error(f)
		}
	}
}

func TestWIFTable_FaultsNameAnUnlistedDeployer(t *testing.T) {
	t.Parallel()
	rows := []wifRow{{"deploy-preview", "dev-env-sweeper.yml"}}
	texts := map[string]string{"dev-env-sweeper.yml": "uses: google-github-actions/auth@v2\nsa: deploy-prod@ascomply-prod.iam.gserviceaccount.com\n"}
	got := wifTableFaults(rows, texts)
	if len(got) != 1 || !strings.Contains(got[0], "dev-env-sweeper.yml") || !strings.Contains(got[0], "deploy-prod") {
		t.Fatalf("faults = %q", got)
	}
}

func TestWIFTable_EveryRowNamesARealFile(t *testing.T) {
	t.Parallel()
	rows, texts := wifReal(t)
	for _, f := range wifTableFaults(rows, texts) {
		if strings.Contains(f, "does not exist") {
			t.Error(f)
		}
	}
	got := wifTableFaults([]wifRow{{"deploy-prod", "gone.yml"}}, map[string]string{})
	if len(got) != 1 || !strings.Contains(got[0], "gone.yml") {
		t.Fatalf("faults = %q", got)
	}
}

func TestWIFTable_IgnoresACommentedAuthLine(t *testing.T) {
	t.Parallel()
	texts := map[string]string{"c.yml": "steps:\n  # uses: google-github-actions/auth@v2\n  - run: echo\n"}
	if got := wifTableFaults(nil, texts); len(got) != 0 {
		t.Fatalf("faults = %q", got)
	}
}
