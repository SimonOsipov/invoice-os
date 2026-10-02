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

// reconcileCall is one NAME=VALUE pair of a set_service_vars line in reconcile_url_variables.
// verified: the next statement is that label's auth_check on the pairs.
type reconcileCall struct {
	idVar, label, name, value string
	verified                  bool
}

// Bash variable names are case-sensitive, so every pattern here is too.
var (
	reconcileCallPattern = regexp.MustCompile(`^\s*set_service_vars\s+"\$env_id"\s+"\$(RAILWAY_SVC_\w+)"\s+(\S+)\s+""((?:\s+\S+)+)\s*$`)
	reconcilePairToken   = regexp.MustCompile(`"[^"]*"|\S+`)
	reconcilePairName    = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

// reconcileCalls parses every set_service_vars line in the comment-stripped body; an unparsable line is fatal.
func reconcileCalls(t *testing.T) []reconcileCall {
	t.Helper()
	var calls []reconcileCall
	var lines []string
	for _, l := range stripHashComments(strings.Split(reconcileURLVariablesBody(t), "\n")) {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "set_service_vars") {
			continue
		}
		m := reconcileCallPattern.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("reconcile_url_variables call does not parse as `set_service_vars \"$env_id\" \"$RAILWAY_SVC_*\" <label> \"\" NAME=VALUE...`: %q", strings.TrimSpace(line))
		}
		verified := i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == `auth_check `+m[2]+` "${SET_VARS_PAIRS[@]}" || exit 1`
		for _, tok := range reconcilePairToken.FindAllString(m[3], -1) {
			name, value, ok := strings.Cut(strings.Trim(tok, `"`), "=")
			if !ok || !reconcilePairName.MatchString(name) {
				t.Fatalf("set_service_vars argument %s is not NAME=VALUE: %q", tok, strings.TrimSpace(line))
			}
			calls = append(calls, reconcileCall{m[1], m[2], name, value, verified})
		}
	}
	// A floor: the 9 variables that predate the landing gateway variable.
	if len(calls) < 9 {
		t.Fatalf("parsed %d set_service_vars pairs in reconcile_url_variables, want >= 9 (extraction is broken)", len(calls))
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
	if app == nil || app.idVar != "RAILWAY_SVC_APP_ID" || app.value != "$gateway_url" || !app.verified {
		t.Fatalf("control: no verified `set_service_vars ... app ... VITE_GATEWAY_URL=$gateway_url` line; the pattern shape is stale (%+v)", app)
	}
	if landing == nil || landing.idVar != "RAILWAY_SVC_LANDING_ID" || landing.value != "$gateway_url" {
		t.Errorf("reconcile_url_variables has no `set_service_vars \"$env_id\" \"$RAILWAY_SVC_LANDING_ID\" landing \"\" ... VITE_GATEWAY_URL=$gateway_url` (AC-1)")
	} else if !landing.verified {
		t.Errorf("landing's set_service_vars line is not followed by `auth_check landing \"${SET_VARS_PAIRS[@]}\" || exit 1` (AC-1)")
	}
}

// Every label names its own service id.
func TestReconcileURLVariablesGatewayURLNotOnOtherServices(t *testing.T) {
	idForLabel := map[string]string{
		"gateway":         "RAILWAY_SVC_GATEWAY_ID",
		"app":             "RAILWAY_SVC_APP_ID",
		"landing":         "RAILWAY_SVC_LANDING_ID",
		"ops-console":     "RAILWAY_SVC_OPS_CONSOLE_ID",
		"support-console": "RAILWAY_SVC_SUPPORT_CONSOLE_ID",
	}

	gatewayLabels := map[string]bool{}
	landingLines := 0
	for _, c := range reconcileCalls(t) {
		want, ok := idForLabel[c.label]
		if !ok {
			t.Errorf("set_service_vars uses unknown label %q; add it to idForLabel deliberately", c.label)
		} else if c.idVar != want {
			t.Errorf("set_service_vars labelled %q writes $%s, want $%s", c.label, c.idVar, want)
		}
		if !c.verified {
			t.Errorf("%s.%s is written without the auth_check that follows its set_service_vars line", c.label, c.name)
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

	var got []string
	for l := range gatewayLabels {
		got = append(got, l)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "app,landing,ops-console,support-console" {
		t.Errorf("set_service_vars writes VITE_GATEWAY_URL on %v, want exactly [app landing ops-console support-console]", got)
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
	write := regexp.MustCompile(`(?m)^\s*set_service_vars "\$env_id" "\$auth_id" auth .*"\$\{auth_vars\[@\]\}"\s*$`)
	if !write.MatchString(forkJoined) {
		t.Errorf(`cmd_set_fork_auth no longer calls set_service_vars "$env_id" "$auth_id" auth ... "${auth_vars[@]}"`)
	}
	if call := `auth_check auth "${auth_vars[@]}"`; !strings.Contains(forkJoined, call) {
		t.Errorf("cmd_set_fork_auth no longer calls %s", call)
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
