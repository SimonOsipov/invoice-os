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

// wifEmails maps each deployer in the table to the one service account it may use.
var wifEmails = map[string]string{
	"deploy-prod":    "deploy-prod@ascomply-prod.iam.gserviceaccount.com",
	"deploy-preview": "deploy-preview@ascomply-preview.iam.gserviceaccount.com",
	"deploy-nightly": "deploy-nightly@ascomply-preview.iam.gserviceaccount.com",
}

var (
	wifRowRe    = regexp.MustCompile("^\\| `(deploy-[a-z]+)` \\| `([^`]+)`")
	wifEmailRe  = regexp.MustCompile(`[a-z0-9-]+@[a-z0-9-]+\.iam\.gserviceaccount\.com`)
	wifSARe     = regexp.MustCompile(`(?m)^\s*service_account:\s*(.+?)\s*$`)
	wifEnvRefRe = regexp.MustCompile(`^\$\{\{\s*env\.(\w+)\s*\}\}$`)
	// wifQuotedRe and wifEventRe are the only pieces an env value may hold besides operators.
	wifQuotedRe = regexp.MustCompile(`'[^']*'`)
	wifEventRe  = regexp.MustCompile(`github\.event_name`)
	wifOpsRe    = regexp.MustCompile(`^[\s$&|=!(){}]*$`)
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

func wifCode(text string) string {
	return strings.Join(stripHashComments(strings.Split(text, "\n")), "\n")
}

// wifSAEmails returns the emails every service_account: value in code can take.
// A value that is not a literal email or an env ref to literal emails is a fault.
func wifSAEmails(file, code string) (emails, faults []string) {
	for _, m := range wifSARe.FindAllStringSubmatch(code, -1) {
		v := strings.Trim(m[1], "'\"")
		if wifEmailRe.MatchString(v) && wifEmailRe.FindString(v) == v {
			emails = append(emails, v)
			continue
		}
		var found []string
		if r := wifEnvRefRe.FindStringSubmatch(v); r != nil {
			envRe := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(r[1]) + `:\s*(.+)$`)
			for _, e := range envRe.FindAllStringSubmatch(code, -1) {
				// Anything but quoted literals, github.event_name and operators is dynamic.
				if !wifOpsRe.MatchString(wifEventRe.ReplaceAllString(wifEmailRe.ReplaceAllString(wifQuotedRe.ReplaceAllString(e[1], ""), ""), "")) {
					found = nil
					break
				}
				found = append(found, wifEmailRe.FindAllString(e[1], -1)...)
			}
		}
		if len(found) == 0 {
			faults = append(faults, fmt.Sprintf("%s: service_account %q is not a literal email or an env value of literal emails", file, m[1]))
		}
		emails = append(emails, found...)
	}
	return emails, faults
}

// wifAuthFiles lists the files that call google-github-actions/auth outside a comment.
func wifAuthFiles(texts map[string]string) []string {
	var files []string
	for file, text := range texts {
		if strings.Contains(wifCode(text), "google-github-actions/auth") {
			files = append(files, file)
		}
	}
	sort.Strings(files)
	return files
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
	known := map[string]string{}
	for name, email := range wifEmails {
		known[email] = name
	}
	var faults []string
	used := map[string]map[string]bool{}
	for _, file := range wifAuthFiles(texts) {
		if listed[file] == nil {
			faults = append(faults, fmt.Sprintf("%s calls google-github-actions/auth but has no ci-gcp.md table row%s", file, where))
			continue
		}
		emails, bad := wifSAEmails(file, wifCode(texts[file]))
		faults = append(faults, bad...)
		used[file] = map[string]bool{}
		for _, e := range emails {
			name, ok := known[e]
			if !ok {
				faults = append(faults, fmt.Sprintf("%s names %s, which is not a deployer account%s", file, e, where))
				continue
			}
			used[file][name] = true
			if !listed[file][name] {
				faults = append(faults, fmt.Sprintf("%s names %s, which the ci-gcp.md table does not give it%s", file, name, where))
			}
		}
	}
	for _, r := range rows {
		if _, ok := texts[r.file]; !ok {
			faults = append(faults, fmt.Sprintf("ci-gcp.md table names %s, which does not exist%s", r.file, where))
		} else if !used[r.file][r.deployer] {
			faults = append(faults, fmt.Sprintf("ci-gcp.md table gives %s to %s, which does not use it%s", r.deployer, r.file, where))
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

func TestWIFTable_MatchesTheWorkflows(t *testing.T) {
	t.Parallel()
	rows, texts := wifReal(t)
	// Floor: a scan that finds no auth step would pass for nothing.
	if got := wifAuthFiles(texts); len(got) < 6 {
		t.Fatalf("auth-using workflows = %q, want at least 6", got)
	}
	if got := wifTableFaults(rows, texts); len(got) != 0 {
		t.Fatalf("faults:\n%s", strings.Join(got, "\n"))
	}
}

func TestWIFTable_CIRunsGoOnATableEdit(t *testing.T) {
	t.Parallel()
	ci := readWorkflow(t, "ci.yml")
	i, j := strings.Index(ci, "\n            go:\n"), strings.Index(ci, "\n            migrations:\n")
	if i < 0 || j < i || !strings.Contains(ci[i:j], "- '.claude/rules/ci-gcp.md'") {
		t.Fatal("ci.yml go filter does not list .claude/rules/ci-gcp.md")
	}
}

func wifOne(file, body string) map[string]string {
	return map[string]string{file: "uses: google-github-actions/auth@v2\nwith:\n" + body}
}

func wifWant(t *testing.T, got []string, subs ...string) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("faults = %q, want one", got)
	}
	for _, s := range subs {
		if !strings.Contains(got[0], s) {
			t.Fatalf("fault %q lacks %q", got[0], s)
		}
	}
}

func TestWIFTable_FaultsNameAnUnlistedFile(t *testing.T) {
	t.Parallel()
	got := wifTableFaults(nil, wifOne("new.yml", "  service_account: deploy-prod@ascomply-prod.iam.gserviceaccount.com\n"))
	wifWant(t, got, "new.yml", "wif-provider.yaml")
}

func TestWIFTable_FaultsNameAnUnlistedDeployer(t *testing.T) {
	t.Parallel()
	rows := []wifRow{{"deploy-preview", "s.yml"}}
	got := wifTableFaults(rows, wifOne("s.yml", "  service_account: deploy-prod@ascomply-prod.iam.gserviceaccount.com\n"))
	// Also unused: the row's deploy-preview.
	if len(got) != 2 {
		t.Fatalf("faults = %q", got)
	}
	if !strings.Contains(got[0]+got[1], "names deploy-prod") || !strings.Contains(got[0]+got[1], "gives deploy-preview to s.yml") {
		t.Fatalf("faults = %q", got)
	}
}

func TestWIFTable_FaultsAWrongProjectOrNameVariant(t *testing.T) {
	t.Parallel()
	for _, sa := range []string{
		"deploy-preview@ascomply-prod.iam.gserviceaccount.com",
		"deploy-prod-v2@ascomply-prod.iam.gserviceaccount.com",
		"deploy-prod@other-9.iam.gserviceaccount.com",
	} {
		rows := []wifRow{{"deploy-prod", "s.yml"}}
		got := wifTableFaults(rows, wifOne("s.yml", "  service_account: "+sa+"\n"))
		if len(got) == 0 || !strings.Contains(strings.Join(got, "\n"), sa) {
			t.Errorf("%s: faults = %q", sa, got)
		}
	}
}

func TestWIFTable_FaultsANonLiteralServiceAccount(t *testing.T) {
	t.Parallel()
	rows := []wifRow{{"deploy-prod", "s.yml"}}
	for _, body := range []string{
		"  service_account: ${{ secrets.SA }}\n",
		"  service_account: ${{ env.MISSING }}\n",
		"  service_account: ${{ env.SA }}\nenv:\n  SA: ${{ vars.SA }}\n",
		"  service_account: ${{ env.SA }}\nenv:\n  SA: ${{ github.event_name == 'x' && 'a-b@p.iam.gserviceaccount.com' || vars.SA }}\n",
	} {
		got := wifTableFaults(rows, wifOne("s.yml", body))
		if !strings.Contains(strings.Join(got, "\n"), "not a literal") {
			t.Errorf("%q: faults = %q", body, got)
		}
	}
}

func TestWIFTable_ResolvesAnEnvValueWithTwoArms(t *testing.T) {
	t.Parallel()
	rows := []wifRow{{"deploy-preview", "s.yml"}, {"deploy-nightly", "s.yml"}}
	body := "  service_account: ${{ env.D }}\nenv:\n  D: ${{ github.event_name == 'x' && 'deploy-nightly@ascomply-preview.iam.gserviceaccount.com' || 'deploy-preview@ascomply-preview.iam.gserviceaccount.com' }}\n"
	if got := wifTableFaults(rows, wifOne("s.yml", body)); len(got) != 0 {
		t.Fatalf("faults = %q", got)
	}
	// An arm the table does not give the file is a fault.
	got := wifTableFaults(rows[:1], wifOne("s.yml", body))
	wifWant(t, got, "deploy-nightly", "does not give it")
}

func TestWIFTable_FaultsARowForAFileThatDoesNotUseTheDeployer(t *testing.T) {
	t.Parallel()
	rows := []wifRow{{"deploy-prod", "s.yml"}, {"deploy-preview", "s.yml"}}
	got := wifTableFaults(rows, wifOne("s.yml", "  service_account: deploy-prod@ascomply-prod.iam.gserviceaccount.com\n"))
	wifWant(t, got, "deploy-preview", "s.yml", "does not use it")
	got = wifTableFaults([]wifRow{{"deploy-prod", "n.yml"}}, map[string]string{"n.yml": "run: echo\n"})
	wifWant(t, got, "n.yml", "does not use it")
}

func TestWIFTable_FaultsARowNamingAMissingFile(t *testing.T) {
	t.Parallel()
	got := wifTableFaults([]wifRow{{"deploy-prod", "gone.yml"}}, map[string]string{})
	wifWant(t, got, "gone.yml", "does not exist")
}

func TestWIFTable_IgnoresACommentedAuthLine(t *testing.T) {
	t.Parallel()
	texts := map[string]string{"c.yml": "steps:\n  # uses: google-github-actions/auth@v2\n  - run: echo\n"}
	if got := wifTableFaults(nil, texts); len(got) != 0 {
		t.Fatalf("faults = %q", got)
	}
}
