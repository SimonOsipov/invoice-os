package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var localLabelsRe = regexp.MustCompile(`local labels=\(([^)]*)\)`)

// localLabels returns the words of the `local labels=(...)` array in a function body.
func localLabels(body string) []string {
	m := localLabelsRe.FindStringSubmatch(body)
	if m == nil {
		return nil
	}
	return strings.Fields(m[1])
}

// A SPA missing from one of these lists ships without its domain, id check or settle read.
func TestSPAListsMatchTheDeployedFleet(t *testing.T) {
	devEnv, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "dev-env.yml"))
	if err != nil {
		t.Fatal(err)
	}
	spas := sentryMatrixList(t, string(devEnv), "deploy-spas")
	if len(spas) == 0 {
		t.Fatal("deploy-spas parsed to zero names -- nothing to compare")
	}
	body := func(fn string) string {
		return strings.Join(stripHashComments(shellFunctionBody(t, fn)), "\n")
	}
	idVar := func(spa string) string {
		return "RAILWAY_SVC_" + strings.ToUpper(strings.ReplaceAll(spa, "-", "_")) + "_ID"
	}

	// Functions that name each SPA by label as well as by id variable.
	labelled := []string{"cmd_discover_urls", "cmd_verify_spa_domains", "reconcile_domains"}
	idOnly := []string{"require_fork_ids", "settle_fork"}

	for _, spa := range spas {
		for _, fn := range labelled {
			b := body(fn)
			if !regexp.MustCompile(`(^|[^A-Za-z0-9-])` + regexp.QuoteMeta(spa) + `([^A-Za-z0-9-]|$)`).MatchString(b) {
				t.Errorf("%s does not name the `%s` SPA", fn, spa)
			}
			if !strings.Contains(b, idVar(spa)) {
				t.Errorf("%s does not use %s", fn, idVar(spa))
			}
		}
		for _, fn := range idOnly {
			if !strings.Contains(body(fn), idVar(spa)) {
				t.Errorf("%s does not use %s", fn, idVar(spa))
			}
		}
	}

	if labels := localLabels(body("cmd_discover_urls")); !slices.Contains(labels, "landing") {
		t.Errorf("control: discover-urls labels %v do not hold `landing`", labels)
	}
	for _, fn := range append(slices.Clone(labelled), idOnly...) {
		if !strings.Contains(body(fn), idVar("landing")) {
			t.Errorf("control: %s does not use %s", fn, idVar("landing"))
		}
	}
	if labels := localLabels(body("cmd_discover_urls")); !slices.Contains(labels, "library") {
		t.Errorf("discover-urls labels %v do not hold `library`", labels)
	}
}
