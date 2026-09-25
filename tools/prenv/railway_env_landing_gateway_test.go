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

// reconcileCall is one upsert_variable / verify_variable line in reconcile_url_variables.
type reconcileCall struct {
	verb, idVar, label, name, value string
}

// Bash variable names are case-sensitive, so every pattern here is too.
var reconcileCallPattern = regexp.MustCompile(`^\s*(upsert_variable|verify_variable)\s+"\$env_id"\s+"\$(RAILWAY_SVC_\w+)"\s+(\S+)\s+(\w+)\s+(\S+)\s*$`)

// reconcileCalls parses every call in the comment-stripped body; an unparsable call line is fatal.
func reconcileCalls(t *testing.T) []reconcileCall {
	t.Helper()
	var calls []reconcileCall
	for _, line := range stripHashComments(strings.Split(reconcileURLVariablesBody(t), "\n")) {
		s := strings.TrimSpace(line)
		if !strings.HasPrefix(s, "upsert_variable") && !strings.HasPrefix(s, "verify_variable") {
			continue
		}
		m := reconcileCallPattern.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("reconcile_url_variables call does not parse as `<verb> \"$env_id\" \"$RAILWAY_SVC_*\" <label> <NAME> <value>`: %q", s)
		}
		calls = append(calls, reconcileCall{m[1], m[2], m[3], m[4], m[5]})
	}
	// 9 upserts + 9 verifies at HEAD before AUTH-05-05.
	if len(calls) < 18 {
		t.Fatalf("parsed %d upsert/verify calls in reconcile_url_variables, want >= 18 (extraction is broken)", len(calls))
	}
	return calls
}

func TestReconcileURLVariablesSetsAndVerifiesLandingGateway(t *testing.T) {
	body := strings.Join(stripHashComments(strings.Split(reconcileURLVariablesBody(t), "\n")), "\n")

	for _, verb := range []string{"upsert_variable", "verify_variable"} {
		// Control: the app's pair must still match the same shape.
		app := regexp.MustCompile(`(?m)^\s*` + verb + `\s+"\$env_id"\s+"\$RAILWAY_SVC_APP_ID"\s+app\s+VITE_GATEWAY_URL\s+"\$gateway_url"\s*$`)
		if !app.MatchString(body) {
			t.Fatalf("control: no `%s ... app VITE_GATEWAY_URL \"$gateway_url\"` line; the pattern shape is stale", verb)
		}
		landing := regexp.MustCompile(`(?m)^\s*` + verb + `\s+"\$env_id"\s+"\$RAILWAY_SVC_LANDING_ID"\s+landing\s+VITE_GATEWAY_URL\s+"\$gateway_url"\s*$`)
		if !landing.MatchString(body) {
			t.Errorf("reconcile_url_variables has no `%s \"$env_id\" \"$RAILWAY_SVC_LANDING_ID\" landing VITE_GATEWAY_URL \"$gateway_url\"` (AC-1)", verb)
		}
	}
}

// Every label names its own service id, and VITE_GATEWAY_URL goes to app and landing only.
func TestReconcileURLVariablesGatewayURLNotOnOtherServices(t *testing.T) {
	idForLabel := map[string]string{
		"gateway":         "RAILWAY_SVC_GATEWAY_ID",
		"app":             "RAILWAY_SVC_APP_ID",
		"landing":         "RAILWAY_SVC_LANDING_ID",
		"ops-console":     "RAILWAY_SVC_OPS_CONSOLE_ID",
		"support-console": "RAILWAY_SVC_SUPPORT_CONSOLE_ID",
	}

	gatewayLabels := map[string]map[string]bool{"upsert_variable": {}, "verify_variable": {}}
	landingLines := 0
	for _, c := range reconcileCalls(t) {
		want, ok := idForLabel[c.label]
		if !ok {
			t.Errorf("%s uses unknown label %q; add it to idForLabel deliberately", c.verb, c.label)
		} else if c.idVar != want {
			t.Errorf("%s labelled %q writes $%s, want $%s", c.verb, c.label, c.idVar, want)
		}
		if c.label == "landing" {
			landingLines++
		}
		if c.name == "VITE_GATEWAY_URL" {
			gatewayLabels[c.verb][c.label] = true
			if c.value != `"$gateway_url"` {
				t.Errorf("%s %s VITE_GATEWAY_URL carries %s, want \"$gateway_url\"", c.verb, c.label, c.value)
			}
		}
	}
	if landingLines == 0 {
		t.Fatalf("control: no landing-labelled call parsed")
	}

	for verb, labels := range gatewayLabels {
		var got []string
		for l := range labels {
			got = append(got, l)
		}
		sort.Strings(got)
		if strings.Join(got, ",") != "app,landing" {
			t.Errorf("%s writes VITE_GATEWAY_URL on %v, want exactly [app landing]", verb, got)
		}
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

func TestSetForkAuthAutoconfirms(t *testing.T) {
	const entry = "GOTRUE_MAILER_AUTOCONFIRM=true"
	const needle = "GOTRUE_MAILER_AUTOCONFIRM"

	fork := stripHashComments(shellFunctionBody(t, "cmd_set_fork_auth"))
	forkJoined := strings.Join(fork, "\n")
	forkVars := authVarsEntries(t, "cmd_set_fork_auth", fork)
	if len(forkVars) < 5 || !slices.Contains(forkVars, "GOTRUE_DISABLE_SIGNUP=false") {
		t.Fatalf("control: fork auth_vars %v lacks GOTRUE_DISABLE_SIGNUP=false or has < 5 entries", forkVars)
	}
	// GoTrue reads the env var name case-sensitively; the value is pinned to lowercase `true`.
	if !slices.Contains(forkVars, entry) {
		t.Errorf("cmd_set_fork_auth auth_vars %v lacks %q (AC-2)", forkVars, entry)
	}
	// Written and re-read through the array, so the entry cannot skip the read-back.
	for _, call := range []string{`auth_write "$env_id" "$auth_id" auth "${auth_vars[@]}"`, `auth_check auth "${auth_vars[@]}"`} {
		if !strings.Contains(forkJoined, call) {
			t.Errorf("cmd_set_fork_auth no longer calls %s", call)
		}
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
		t.Errorf("railway-env.sh names %s %d times in code, %d inside cmd_set_fork_auth; only the fork may write it", needle, all, inFork)
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
