package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// reconcileCall is one NAME=VALUE pair of a pass_plan_add line in reconcile_url_variables.
type reconcileCall struct {
	idVar, label, name, value string
}

// Bash variable names are case-sensitive, so every pattern here is too.
var (
	reconcileCallPattern = regexp.MustCompile(`^\s*pass_plan_add\s+"\$(\w+)"\s+(\S+)\s+""((?:\s+\S+)+)\s*$`)
	reconcilePairToken   = regexp.MustCompile(`"[^"]*"|\S+`)
	reconcilePairName    = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	// <id>=$(service_id_by_name <settle> <service> ...): which service a variable's id names.
	serviceIDByName = regexp.MustCompile(`\b(\w+)=\$\(\s*service_id_by_name\s+\S+\s+"?([a-z-]+)"?\s`)
)

// reconcileCalls parses every pass_plan_add line in the comment-stripped body; an unparsable line is fatal.
func reconcileCalls(t *testing.T) []reconcileCall {
	t.Helper()
	var calls []reconcileCall
	var lines []string
	for _, l := range stripHashComments(strings.Split(reconcileURLVariablesBody(t), "\n")) {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	for _, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "pass_plan_add") {
			continue
		}
		m := reconcileCallPattern.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("reconcile_url_variables call does not parse as `pass_plan_add \"$<id>\" <label> \"\" NAME=VALUE...`: %q", strings.TrimSpace(line))
		}
		for _, tok := range reconcilePairToken.FindAllString(m[3], -1) {
			name, value, ok := strings.Cut(strings.Trim(tok, `"`), "=")
			if !ok || !reconcilePairName.MatchString(name) {
				t.Fatalf("pass_plan_add argument %s is not NAME=VALUE: %q", tok, strings.TrimSpace(line))
			}
			calls = append(calls, reconcileCall{m[1], m[2], name, value})
		}
	}
	// A floor: the 9 variables that predate the landing gateway variable.
	if len(calls) < 9 {
		t.Fatalf("parsed %d pass_plan_add pairs in reconcile_url_variables, want >= 9 (extraction is broken)", len(calls))
	}
	return calls
}

func TestReconcileURLVariablesSetsAndVerifiesLandingGateway(t *testing.T) {
	var app, landing *reconcileCall
	for _, c := range reconcileCalls(t) {
		if c.name != "VITE_GATEWAY_URL" {
			continue
		}
		switch c.label {
		case "app":
			app = &c
		case "landing":
			landing = &c
		}
	}
	// Control: the app's pair must still match the same shape.
	if app == nil || app.value != "$gateway_url" {
		t.Fatalf("control: no `pass_plan_add ... app ... VITE_GATEWAY_URL=$gateway_url` line; the pattern shape is stale (%+v)", app)
	}
	if landing == nil || landing.value != "$gateway_url" {
		t.Errorf("reconcile_url_variables has no `pass_plan_add \"$<id>\" landing \"\" ... VITE_GATEWAY_URL=$gateway_url` (AC-1)")
	}
}

// Every label's plan line names the id of the service of that name.
func TestReconcileURLVariablesGatewayURLNotOnOtherServices(t *testing.T) {
	known := map[string]bool{"gateway": true, "app": true, "landing": true, "ops-console": true, "support-console": true}
	resolved := map[string]string{}
	for _, m := range serviceIDByName.FindAllStringSubmatch(strings.Join(stripHashComments(strings.Split(reconcileURLVariablesBody(t), "\n")), "\n"), -1) {
		resolved[m[1]] = m[2]
	}

	gatewayLabels := map[string]bool{}
	landingLines := 0
	for _, c := range reconcileCalls(t) {
		if !known[c.label] {
			t.Errorf("pass_plan_add uses unknown label %q; add it to known deliberately", c.label)
		} else if resolved[c.idVar] != c.label {
			t.Errorf("pass_plan_add labelled %q writes $%s, which resolves the service %q by name", c.label, c.idVar, resolved[c.idVar])
		}
		if c.label == "landing" {
			landingLines++
		}
		if c.name == "VITE_GATEWAY_URL" {
			gatewayLabels[c.label] = true
			if c.value != "$gateway_url" {
				t.Errorf("%s VITE_GATEWAY_URL carries %s, want $gateway_url", c.label, c.value)
			}
		}
	}
	if landingLines == 0 {
		t.Fatalf("control: no landing-labelled call parsed")
	}
	if len(resolved) < 5 {
		t.Fatalf("control: %d service ids resolved by name in reconcile_url_variables (%v), want one per service", len(resolved), resolved)
	}

	var got []string
	for l := range gatewayLabels {
		got = append(got, l)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "app,landing,ops-console,support-console" {
		t.Errorf("pass_plan_add writes VITE_GATEWAY_URL on %v, want exactly [app landing ops-console support-console]", got)
	}
}

// authVarsEntries returns the quoted entries of the `local auth_vars=(` array in a comment-stripped body.
func authVarsEntries(t *testing.T, fn string, body []string) []string {
	t.Helper()
	start := -1
	for i, line := range body {
		if strings.TrimSpace(line) == "local auth_vars=(" {
			if start >= 0 {
				t.Fatalf("%s declares auth_vars twice", fn)
			}
			start = i
		}
	}
	if start < 0 {
		t.Fatalf("%s has no `local auth_vars=(` line", fn)
	}
	var entries []string
	for _, line := range body[start+1:] {
		s := strings.TrimSpace(line)
		if s == ")" {
			return entries
		}
		if s == "" {
			continue
		}
		entries = append(entries, strings.Trim(s, `"`))
	}
	t.Fatalf("%s: auth_vars array is never closed", fn)
	return nil
}

// forkAuthVarsHolder names the one function, other than set-production-auth's, that declares
// `local auth_vars=(`: the fork's auth plan, wherever the pass keeps it.
func forkAuthVarsHolder(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(railwayEnvScript(t))
	if err != nil {
		t.Fatal(err)
	}
	def := regexp.MustCompile(`^([a-z_0-9]+)\(\) \{$`)
	var holders []string
	cur := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if m := def.FindStringSubmatch(line); m != nil {
			cur = m[1]
		}
		if strings.TrimSpace(line) == "local auth_vars=(" && cur != "cmd_set_production_auth" {
			holders = append(holders, cur)
		}
	}
	if len(holders) != 1 {
		t.Fatalf("functions declaring `local auth_vars=(` besides cmd_set_production_auth = %v, want exactly 1", holders)
	}
	return holders[0]
}

// Runtime oracles for the write and the read-back: TestSetForkAuth_AutoconfirmsOnAuthOnly and
// TestSetForkAuth_AutoconfirmReReadMismatchFails.
func TestSetForkAuthAutoconfirms(t *testing.T) {
	const entry = "GOTRUE_MAILER_AUTOCONFIRM=true"
	const needle = "GOTRUE_MAILER_AUTOCONFIRM"

	holder := forkAuthVarsHolder(t)
	fork := stripHashComments(shellFunctionBody(t, holder))
	forkJoined := strings.Join(fork, "\n")
	forkVars := authVarsEntries(t, holder, fork)
	if len(forkVars) < 5 || !slices.Contains(forkVars, "GOTRUE_DISABLE_SIGNUP=false") {
		t.Fatalf("control: fork auth_vars %v lacks GOTRUE_DISABLE_SIGNUP=false or has < 5 entries", forkVars)
	}
	// GoTrue reads the env var name case-sensitively; the value is pinned to lowercase `true`.
	if !slices.Contains(forkVars, entry) {
		t.Errorf("%s auth_vars %v lacks %q (AC-2)", holder, forkVars, entry)
	}

	prod := stripHashComments(shellFunctionBody(t, "cmd_set_production_auth"))
	prodVars := authVarsEntries(t, "cmd_set_production_auth", prod)
	if len(prodVars) < 5 || !slices.Contains(prodVars, "GOTRUE_JWT_ISSUER=$issuer") {
		t.Fatalf("control: production auth_vars %v lacks GOTRUE_JWT_ISSUER=$issuer or has < 5 entries", prodVars)
	}
	if strings.Contains(strings.Join(prod, "\n"), needle) {
		t.Errorf("cmd_set_production_auth mentions %s in code; production must keep the image default (false)", needle)
	}

	// Nothing else in the script writes it either.
	raw, err := os.ReadFile(railwayEnvScript(t))
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Count(strings.Join(stripHashComments(strings.Split(string(raw), "\n")), "\n"), needle)
	inFork := strings.Count(forkJoined, needle)
	if all != inFork {
		t.Errorf("railway-env.sh names %s %d times in code, %d inside %s; only the fork may write it", needle, all, inFork, holder)
	}

	// The image default is what keeps production closed.
	dockerfile := filepath.Join(repoRoot(t), "sidecar", "auth", "Dockerfile")
	img, err := os.ReadFile(dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^[^#]*\bGOTRUE_MAILER_AUTOCONFIRM=false\b`).Match(img) {
		t.Errorf("%s no longer defaults GOTRUE_MAILER_AUTOCONFIRM=false", dockerfile)
	}
}
