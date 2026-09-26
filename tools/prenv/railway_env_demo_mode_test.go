// railway_env_demo_mode_test.go pins the CI contract for VITE_DEMO_MODE on the app
// service's ephemeral PR environments (DEMO-06-07, task-595). T1 and T5 are RED at
// HEAD; T2, T3 and T4 are GUARDs, each already green, that fence the change against a
// specific named regression (see each test's doc comment).
//
// verify_variable cannot be driven token-free (its own re-read is a live GraphQL
// round trip), so no test here fakes one. The only live oracle for "Railway actually holds it" is a
// green prepare-env run on a real PR; the only oracle for "Vite actually baked it
// into the bundle" is e2e/topology/demo-persona.spec.ts, which cannot pass with the
// flag unset because the trigger it looks for is tree-shaken out of a flag-off build.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// reconcileURLVariablesBody extracts reconcile_url_variables's body from
// scripts/ci/railway-env.sh: everything between its opening brace and the next line
// that is exactly `}` at column 0. Fatal, not a silent miss, when the function is
// absent.
func reconcileURLVariablesBody(t *testing.T) string {
	t.Helper()
	path := railwayEnvScript(t)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	start := regexp.MustCompile(`(?m)^reconcile_url_variables\(\)\s*\{`).FindStringIndex(string(content))
	if start == nil {
		t.Fatalf("reconcile_url_variables() not found in %s", path)
	}
	rest := string(content)[start[1]:]
	end := regexp.MustCompile(`(?m)^\}$`).FindStringIndex(rest)
	if end == nil {
		t.Fatalf("could not find the closing brace of reconcile_url_variables in %s", path)
	}
	return rest[:end[0]]
}

// T1 (AC-1) — RED at HEAD: VITE_DEMO_MODE appears nowhere in railway-env.sh
// (verified via git grep, Stage 2 correction S2-1). Passes once the app service is
// both upserted AND independently re-verified.
//
// KILLS: the upsert added with no matching verify (or vice versa); either call
// targeting a service other than $RAILWAY_SVC_APP_ID.
func TestReconcileURLVariablesSetsAndVerifiesDemoMode(t *testing.T) {
	body := reconcileURLVariablesBody(t)

	upsertPattern := regexp.MustCompile(`upsert_variable\s+"\$env_id"\s+"\$RAILWAY_SVC_APP_ID"\s+app\s+VITE_DEMO_MODE\s+"?true"?`)
	verifyPattern := regexp.MustCompile(`verify_variable\s+"\$env_id"\s+"\$RAILWAY_SVC_APP_ID"\s+app\s+VITE_DEMO_MODE\s+"?true"?`)

	if !upsertPattern.MatchString(body) {
		t.Errorf("reconcile_url_variables does not upsert VITE_DEMO_MODE=true on the app service ($RAILWAY_SVC_APP_ID)")
	}
	if !verifyPattern.MatchString(body) {
		t.Errorf("reconcile_url_variables does not independently re-verify VITE_DEMO_MODE=true on the app service — \"the mutation's own response is never the evidence\" (railway-env.sh:1203)")
	}
}

// T2 (AC-2) — GUARD, green at HEAD: 8 upsert_variable calls, closing line reads
// "All 8 URL variables confirmed by independent re-query." (8 == 8). Wording-agnostic
// regex so the honest post-rename wording ("environment variables") still passes —
// only the COUNT is this test's claim.
//
// KILLS: the upsert/verify pair added while the closing line's literal count is left
// unchanged (grep -c 'upsert_variable "\$env_id"' would show N, the log would still
// claim N-1).
func TestConfirmedVariableCountMatchesUpserts(t *testing.T) {
	body := reconcileURLVariablesBody(t)

	upsertCount := len(regexp.MustCompile(`(?m)^\s*upsert_variable\s`).FindAllString(body, -1))
	if upsertCount == 0 {
		t.Fatalf("found 0 upsert_variable calls in reconcile_url_variables — body extraction is broken (vacuity guard)")
	}

	countPattern := regexp.MustCompile(`All (\d+) [a-zA-Z ]*variables confirmed by independent re-query\.`)
	m := countPattern.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("reconcile_url_variables's closing log line does not match \"All N ... variables confirmed by independent re-query.\"")
	}
	loggedCount, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("could not parse logged count %q: %v", m[1], err)
	}

	if loggedCount != upsertCount {
		t.Errorf("closing log line claims %d variables confirmed, but the function calls upsert_variable %d times", loggedCount, upsertCount)
	}
}

// T3 (AC-3) — GUARD, green at HEAD: the persistent-environment refusal
// (railway-env.sh:1227-1230) must survive byte-identical. Matched as one exact
// four-line block, not as scattered substrings, so a reflow that keeps the words but
// drops the exit still fails.
//
// KILLS: an executor restructuring the function around the new VITE_DEMO_MODE calls
// and dropping (or reordering past) this guard — which would re-open rewriting
// production's URL variables from a PR-environment run.
func TestReconcileURLVariablesRefusesThePersistentEnvironment(t *testing.T) {
	body := reconcileURLVariablesBody(t)

	const refusalBlock = `if [ "$env_id" = "$RAILWAY_DEV_ENVIRONMENT_ID" ]; then
    echo "::error::Refusing to rewrite URL variables in the persistent development environment ($env_id)."
    exit 1
  fi`

	if !strings.Contains(body, refusalBlock) {
		t.Errorf("reconcile_url_variables's persistent-environment refusal is not byte-identical to HEAD (AC-3); want the exact block:\n%s", refusalBlock)
	}
}

// T4 (AC-1) — GUARD, green before and after this change (Stage 2 correction S2-2:
// VITE_DEMO_MODE's own ARG/ENV pair already ships at Dockerfile:25-26, so this test
// closes no gap here — it is a standing fence for a future 4th VITE_ variable).
//
// KILLS the one silent gap in the whole chain: Railway holds the variable,
// verify_variable passes, the prepare-env log reads "app.VITE_DEMO_MODE = true", and
// the bundle is STILL flag-off because no build arg carried it into `vite build`.
func TestEveryAppViteVariableHasADockerfileArg(t *testing.T) {
	body := reconcileURLVariablesBody(t)

	namePattern := regexp.MustCompile(`upsert_variable\s+"\$env_id"\s+"\$RAILWAY_SVC_APP_ID"\s+app\s+(VITE_\w+)`)
	matches := namePattern.FindAllStringSubmatch(body, -1)
	if len(matches) < 2 {
		t.Fatalf("found %d VITE_* upserts against the app service, want >= 2 (vacuity guard — extraction may be broken)", len(matches))
	}

	names := make(map[string]bool)
	for _, m := range matches {
		names[m[1]] = true
	}

	dockerfilePath := filepath.Join(repoRoot(t), "frontend", "app", "Dockerfile")
	raw, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("reading %s: %v", dockerfilePath, err)
	}
	dockerfile := string(raw)

	for name := range names {
		argLine := "ARG " + name
		envLine := "ENV " + name + "=$" + name
		if !strings.Contains(dockerfile, argLine) {
			t.Errorf("%s is upserted onto the app service but %s declares no %q — the value cannot reach vite build", name, dockerfilePath, argLine)
		}
		if !strings.Contains(dockerfile, envLine) {
			t.Errorf("%s is upserted onto the app service but %s never promotes it with %q", name, dockerfilePath, envLine)
		}
	}
}

// T5 (AC-4) — RED at HEAD: scripts/** is in no ci.yml paths filter (F2). Mirrors
// dev-env.yml, which already lists scripts/ci/**. Without this, T1-T4 above are
// inert on exactly the edit they guard: a scripts-only commit matches no filter, the
// Go job is skipped, and `go test ./...` never runs.
func TestCIYmlGoFilterReachesScriptsCI(t *testing.T) {
	path := filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	content := string(raw)

	goFilterPattern := regexp.MustCompile(`(?s)\n\s*go:\s*\n(.*?)\n\s*migrations:`)
	m := goFilterPattern.FindStringSubmatch(content)
	if m == nil {
		t.Fatalf("could not locate the go: paths filter block in %s", path)
	}
	if !strings.Contains(m[1], "scripts/ci/**") {
		t.Errorf("ci.yml's go: paths filter does not include 'scripts/ci/**' — a commit touching only scripts/ci/railway-env.sh matches no filter, so the Go job (and every guard in this file) is skipped on exactly the edit it guards")
	}
}

// Sibling of TestEveryAppViteVariableHasADockerfileArg for the landing service.
func TestEveryLandingViteVariableHasADockerfileArg(t *testing.T) {
	body := strings.Join(stripHashComments(strings.Split(reconcileURLVariablesBody(t), "\n")), "\n")

	// Bash names are case-sensitive, so the pattern is too.
	namePattern := regexp.MustCompile(`(?m)^\s*upsert_variable\s+"\$env_id"\s+"\$RAILWAY_SVC_LANDING_ID"\s+landing\s+(VITE_\w+)\s`)
	names := make(map[string]bool)
	for _, m := range namePattern.FindAllStringSubmatch(body, -1) {
		names[m[1]] = true
	}
	// APP, OPS, SUPPORT and GATEWAY.
	if len(names) < 4 {
		t.Fatalf("found %d distinct VITE_* upserts on the landing service (%v), want >= 4", len(names), names)
	}

	dockerfilePath := filepath.Join(repoRoot(t), "frontend", "landing", "Dockerfile")
	raw, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("reading %s: %v", dockerfilePath, err)
	}
	// Dockerfile comments are whole lines starting with #.
	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		if s := strings.TrimSpace(l); s != "" && !strings.HasPrefix(s, "#") {
			lines = append(lines, s)
		}
	}
	build := slices.Index(lines, "RUN pnpm --filter @invoice-os/landing build")
	if build < 0 {
		t.Fatalf("control: %s has no `RUN pnpm --filter @invoice-os/landing build` line", dockerfilePath)
	}

	// Exact lines before the build step; a prefix match would let VITE_APP_URL cover VITE_APP_URL_X.
	for name := range names {
		for _, want := range []string{"ARG " + name, "ENV " + name + "=$" + name} {
			i := slices.Index(lines, want)
			if i < 0 {
				t.Errorf("%s is upserted onto landing but %s has no %q line", name, dockerfilePath, want)
			} else if i > build {
				t.Errorf("%s: %q comes after the landing build step", dockerfilePath, want)
			}
		}
	}
}

func TestLandingDependsOnAPIClient(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "frontend", "landing", "package.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	if pkg.Dependencies["@invoice-os/design-tokens"] != "workspace:*" {
		t.Fatalf("control: %s dependencies lack @invoice-os/design-tokens workspace:*; got %v", path, pkg.Dependencies)
	}
	if got := pkg.Dependencies["@invoice-os/api-client"]; got != "workspace:*" {
		t.Errorf("%s dependencies[@invoice-os/api-client] = %q, want \"workspace:*\" (AC-4)", path, got)
	}

	// The Dockerfile installs with --frozen-lockfile, so the lockfile importer must agree.
	lock, err := os.ReadFile(filepath.Join(root, "pnpm-lock.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?ms)^  frontend/landing:\n(.*?)(?:^  \S|\z)`).FindSubmatch(lock)
	if m == nil {
		t.Fatalf("control: pnpm-lock.yaml has no frontend/landing importer")
	}
	importer := string(m[1])
	entry := func(name string) string {
		return "      '@invoice-os/" + name + "':\n        specifier: workspace:*\n        version: link:../../packages/" + name + "\n"
	}
	if !strings.Contains(importer, entry("design-tokens")) {
		t.Fatalf("control: the frontend/landing lockfile importer lacks the design-tokens link entry; the shape is stale")
	}
	if !strings.Contains(importer, entry("api-client")) {
		t.Errorf("pnpm-lock.yaml's frontend/landing importer lacks @invoice-os/api-client -> link:../../packages/api-client; `pnpm install --frozen-lockfile` would fail")
	}
}
