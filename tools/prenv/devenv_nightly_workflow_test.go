// devenv_nightly_workflow_test.go pins the nightly mode of .github/workflows/dev-env.yml.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	nlOn        = "needs.changes.outputs.nightly == 'true'"
	nlOff       = "needs.changes.outputs.nightly != 'true'"
	nlTarget    = "E2E_TARGET: ${{ needs.changes.outputs.nightly == 'true' && 'nightly' || '' }}"
	nlTopoIf    = "!cancelled() && contains(needs.changes.outputs.topology_after, needs.e2e.result) && " + authorCond
	nlTeardown  = "always() && " + nlOn + " && " + authorCond
	nlGroup     = "group: dev-preview-${{ github.event.pull_request.number || 'nightly' }}"
	nlPRVar     = "PR: ${{ github.event.pull_request.number || 'nightly' }}"
	nlFlakyStep = "Fail on a retry-pass in a spec this PR changed"
)

var regexpExpr = regexp.MustCompile(`\$\{\{\s*([A-Za-z0-9_.-]+)`)

func nlJSONFile(name string) string {
	return "PLAYWRIGHT_JSON_OUTPUT_FILE: ${{ " + nlOn + " && format('{0}/nightly-results/" + name + "', github.workspace) || '' }}"
}

// nlCode drops the comments of a run block, so a commented command does not count.
func nlCode(run string) string {
	return strings.Join(stripHashComments(strings.Split(run, "\n")), "\n")
}

func nlJobs(yml string) map[string]workflowJob {
	m := map[string]workflowJob{}
	for _, j := range workflowJobsOf(yml) {
		m[j.name] = j
	}
	return m
}

func nlStep(j workflowJob, name string) (workflowStep, bool) {
	for _, s := range j.steps() {
		if s.keys["name"] == name {
			return s, true
		}
	}
	return workflowStep{}, false
}

func nlStepByID(j workflowJob, id string) (workflowStep, bool) {
	for _, s := range j.steps() {
		if s.keys["id"] == id {
			return s, true
		}
	}
	return workflowStep{}, false
}

// nlRaw returns the trimmed lines of the step named name.
func nlRaw(j workflowJob, name string) []string {
	for _, s := range rawSteps(j) {
		if slices.Contains(s, "name: "+name) {
			return s
		}
	}
	return nil
}

// devEnvNightlyProblems names each nightly-mode clause of dev-env.yml that the text breaks.
func devEnvNightlyProblems(yml string) []string {
	var p []string
	bad := func(msg string) { p = append(p, msg) }
	jobs := nlJobs(yml)
	top := stripHashComments(strings.Split(yml, "\n"))

	if !slices.Contains(onBlockLines(strings.Split(yml, "\n")), "workflow_dispatch:") {
		bad("on: lacks workflow_dispatch")
	}
	if !slices.Contains(top, "  "+nlGroup) {
		bad("concurrency group is not " + nlGroup)
	}
	if !slices.Contains(top, "  "+nlPRVar) {
		bad("env.PR is not " + nlPRVar)
	}

	ch, ok := jobs["changes"]
	if !ok {
		return append(p, "no changes job")
	}
	outs := blockLines(ch.lines, 4, "outputs")
	for _, w := range []string{
		"e2e: ${{ github.event_name == 'workflow_dispatch' && 'true' || steps.lane.outputs.e2e }}",
		"nightly: ${{ steps.mode.outputs.nightly }}",
		"topology_after: ${{ steps.mode.outputs.topology_after }}",
		"shards: ${{ steps.shards.outputs.shards }}",
	} {
		if !slices.Contains(outs, w) {
			bad("changes outputs lack " + w)
		}
	}
	if m, ok := nlStepByID(ch, "mode"); !ok || m.env["NIGHTLY"] != "${{ github.event_name == 'workflow_dispatch' }}" {
		bad("changes mode step NIGHTLY is not the dispatch test")
	} else if strings.Count(m.keys["run"], "topology_after=success failure") != 1 {
		bad("mode step: only the nightly branch may write topology_after=success failure")
	}
	for _, id := range []string{"filter", "lane"} {
		if s, ok := nlStepByID(ch, id); !ok || s.keys["if"] != "github.event_name == 'pull_request'" {
			bad("changes step " + id + " is not pull_request only")
		}
	}
	if s, ok := nlStepByID(ch, "shards"); !ok || !strings.Contains(nlCode(s.keys["run"]), "node e2e/topology/shards.ts") {
		bad("changes lacks the shards step")
	}
	if va, _ := jobKey(jobs["commit-authors"], "outputs"); !strings.Contains(va, "dependabot_only: ${{ steps.verdict.outputs.dependabot_only }}") {
		bad("commit-authors output changed")
	}

	e2e, topo := jobs["e2e"], jobs["topology"]
	if _, ok := nlStepByID(e2e, "shards"); ok {
		bad("e2e keeps the shards step")
	}
	if strings.Contains(strings.Join(e2e.lines, "\n"), "shards:") {
		bad("e2e keeps a shards output")
	}
	if !slices.Equal(needsOf(topo), []string{"changes", "commit-authors", "e2e", "prepare-env", "deploy"}) {
		bad("topology needs are not changes, commit-authors, e2e, prepare-env, deploy")
	}
	if x, _ := foldedIf(topo); x != nlTopoIf {
		bad("topology if is not exactly " + nlTopoIf)
	}
	if !strings.Contains(strings.Join(topo.lines, "\n"), "shard: ${{ fromJSON(needs.changes.outputs.shards) }}") {
		bad("topology matrix does not read changes shards")
	}

	pe := jobs["prepare-env"]
	var order []string
	for _, s := range pe.steps() {
		switch s.keys["name"] {
		case "Delete a leftover nightly preview", "Prove no nightly preview is left", "Count the previews", "preview cap reached", "Create the PR's secrets (generated, never copied from production)":
			order = append(order, s.keys["name"])
			if !strings.HasPrefix(s.keys["name"], "Create") && !strings.HasPrefix(s.keys["if"], nlOn) {
				bad("prepare-env step " + s.keys["name"] + " is not nightly only")
			}
		}
	}
	if !slices.Equal(order, []string{"Delete a leftover nightly preview", "Prove no nightly preview is left", "Count the previews", "preview cap reached", "Create the PR's secrets (generated, never copied from production)"}) {
		bad("prepare-env cap steps are out of order: " + strings.Join(order, " | "))
	}
	if c, _ := nlStep(pe, "preview cap reached"); c.keys["if"] != nlOn+" && steps.count.outputs.full != 'false'" {
		bad("preview cap reached if does not read steps.count.outputs.full")
	}
	if l, _ := nlStep(pe, "Delete a leftover nightly preview"); !strings.Contains(nlCode(l.keys["run"]), `preview.sh teardown "$PR"`) {
		bad("prepare-env leftover step does not tear down the nightly")
	}
	if c, ok := nlStepByID(pe, "count"); !ok || c.keys["name"] != "Count the previews" || !strings.Contains(nlCode(c.keys["run"]), "preview.sh cap") {
		bad("prepare-env count step missing")
	}

	if w, ok := nlStepByID(jobs["ci-watch"], "wait"); !ok || w.keys["if"] != nlOff {
		bad("ci-watch wait step lacks the nightly clause")
	}
	for _, j := range []string{"e2e", "topology"} {
		if s, ok := nlStep(jobs[j], nlFlakyStep); !ok || !strings.HasSuffix(s.keys["if"], " && "+nlOff) {
			bad(j + " flaky gate lacks the nightly clause")
		}
	}
	if x, _ := jobKey(jobs["e2e-gate"], "if"); x != "always() && "+nlOff {
		bad("e2e-gate if is not always() && nightly off")
	}

	for _, j := range []string{"e2e", "topology"} {
		if !slices.Contains(blockLines(jobs[j].lines, 4, "env"), nlTarget) {
			bad(j + " env lacks E2E_TARGET")
		}
	}
	for step, f := range map[string]string{
		"Smoke (landing, library, blog and both consoles render)": "playwright-smoke.json",
		"API E2E (typed contract suite)":                          "playwright-api.json",
	} {
		if s, ok := nlStep(e2e, step); !ok || s.env["PLAYWRIGHT_JSON_OUTPUT_FILE"] != strings.TrimPrefix(nlJSONFile(f), "PLAYWRIGHT_JSON_OUTPUT_FILE: ") {
			bad("e2e " + step + " does not write " + f)
		}
	}
	if s, ok := nlStep(topo, "Topology (${{ matrix.shard }})"); !ok || !strings.Contains(s.env["PLAYWRIGHT_JSON_OUTPUT_FILE"], "playwright-topology-{1}.json', github.workspace, matrix.shard)") {
		bad("topology step does not write playwright-topology-<shard>.json")
	}
	for j, art := range map[string]string{"e2e": "results-e2e", "topology": "results-topology-${{ matrix.shard }}"} {
		raw := nlRaw(jobs[j], "Upload the nightly results")
		for _, w := range []string{"if: always() && " + nlOn, "uses: actions/upload-artifact@v7", "name: " + art, "path: nightly-results/", "if-no-files-found: ignore", "retention-days: 14"} {
			if !slices.Contains(raw, w) {
				bad(j + " results upload lacks " + w)
			}
		}
	}

	hg, _ := nlStep(jobs["health-gate"], "Wait for gateway /health 200 on the commit under test")
	for _, w := range []string{`"$mock" != on`, `"$issuers" != 2`} {
		if !strings.Contains(hg.keys["run"], w) {
			bad("health-gate lacks " + w)
		}
	}
	if strings.Contains(strings.Join(jobs["health-gate"].lines, "\n"), "nightly") {
		bad("health-gate names the nightly mode")
	}

	td, ok := jobs["nightly-teardown"]
	if !ok {
		return append(p, "no nightly-teardown job")
	}
	if x, _ := jobKey(td, "name"); x != "Delete the nightly preview" {
		bad("nightly-teardown name is " + x)
	}
	if !slices.Equal(needsOf(td), []string{"changes", "commit-authors", "prepare-env", "build", "deploy", "health-gate", "fleet-gate", "e2e", "topology", "contacts", "nightly-ai", "email"}) {
		bad("nightly-teardown needs are wrong")
	}
	if x, _ := foldedIf(td); x != nlTeardown {
		bad("nightly-teardown if is not exactly " + nlTeardown)
	}
	if x, _ := jobKey(td, "timeout-minutes"); x != "20" {
		bad("nightly-teardown timeout-minutes is not 20")
	}
	var runs []string
	for _, s := range td.steps() {
		if r := s.keys["run"]; r != "" {
			runs = append(runs, r)
		}
	}
	if len(runs) != 2 || runs[0] != `bash scripts/ci/preview.sh teardown "$PR"` ||
		!strings.Contains(runs[1], "preview.sh prs") || !strings.Contains(runs[1], "$GITHUB_STEP_SUMMARY") || !strings.Contains(runs[1], "::error::") {
		bad("nightly-teardown does not tear down, then print and check prs")
	}

	for name, j := range jobs {
		if strings.Contains(strings.Join(j.lines, "\n"), "scripts/ci/preview.sh") {
			if _, ok := jobKey(j, "timeout-minutes"); !ok {
				bad("job " + name + " calls preview.sh without timeout-minutes")
			}
		}
	}
	if x, _ := jobKey(e2e, "timeout-minutes"); x != nlE2ETimeout {
		bad("e2e timeout-minutes is not the nightly-only 40")
	}
	if g := unguardedOIDCJobs(yml); len(g) != 0 {
		bad("unguarded OIDC jobs: " + strings.Join(g, ","))
	}
	p = append(p, contactProblems(jobs)...)
	p = append(p, aiCheckProblems(jobs)...)
	return append(p, emailProblems(jobs)...)
}

const (
	nlTokenVersionStep = "${{ steps.token_version.outputs.version }}"
	nlTokenVersionOut  = "${{ needs.prepare-env.outputs.hubspot_token_version }}"
	nlContactsIf       = "!cancelled() && " + nlOn + " && needs.deploy.result == 'success'"
)

// contactProblems names each clause of the contacts, start and contacts-cleanup wiring that the text breaks.
func contactProblems(jobs map[string]workflowJob) []string {
	var p []string
	bad := func(msg string) { p = append(p, msg) }

	pe := jobs["prepare-env"]
	if o := blockLines(pe.lines, 4, "outputs"); !slices.Contains(o, "hubspot_token_version: "+nlTokenVersionStep) {
		bad("prepare-env output hubspot_token_version does not read steps.token_version")
	}
	idx := func(j workflowJob, name string) int {
		for i, s := range j.steps() {
			if s.keys["name"] == name {
				return i
			}
		}
		return -1
	}
	const (
		pre   = "Check the contact secrets"
		ver   = "Read the HubSpot token version"
		start = "Start the contacts (guard the portal, delete leftovers)"
		up    = "Upload the contacts start result"
		sec   = "Create the PR's secrets (generated, never copied from production)"
	)
	if i := []int{idx(pe, pre), idx(pe, ver), idx(pe, start), idx(pe, up), idx(pe, sec)}; !slices.IsSorted(i) || i[0] < 0 {
		bad("prepare-env contact steps are out of order or missing: preflight, token_version, start, upload, secrets")
	}
	for _, n := range []string{pre, ver, start} {
		if s, ok := nlStep(pe, n); !ok || s.keys["if"] != nlOn {
			bad("prepare-env step " + n + " is not nightly only")
		}
	}
	if s, ok := nlStep(pe, pre); !ok || strings.TrimSpace(nlCode(s.keys["run"])) != `bash scripts/ci/preview.sh contact-preflight "$PR"` {
		bad("prepare-env preflight step does not run contact-preflight")
	}
	if s, ok := nlStepByID(pe, "token_version"); !ok || !strings.Contains(nlCode(s.keys["run"]), `preview.sh contact-token-version "$PR"`) || !strings.Contains(nlCode(s.keys["run"]), "$GITHUB_OUTPUT") {
		bad("prepare-env token_version step does not write contact-token-version to GITHUB_OUTPUT")
	}
	if s, ok := nlStep(pe, start); !ok || !strings.Contains(nlCode(s.keys["run"]), `preview.sh contact-token "$PR" "`+nlTokenVersionStep+`"`) {
		bad("prepare-env start reads the token without steps.token_version.outputs.version")
	}
	if st, ok := nlStep(pe, up); !ok || st.keys["if"] != "always() && "+nlOn || !slices.Contains(nlRaw(pe, up), "name: contacts-start") {
		bad("prepare-env contacts-start upload does not run if always() in nightly mode")
	}

	dep := jobs["deploy"]
	if !slices.Contains(needsOf(dep), "prepare-env") {
		bad("deploy does not need prepare-env")
	}
	if x, _ := foldedIf(dep); !strings.Contains(x, "needs.prepare-env.result == 'success'") {
		bad("deploy if does not require prepare-env success")
	}
	if s, ok := nlStep(dep, "Deploy the backends"); !ok || s.env["NIGHTLY_HUBSPOT_TOKEN_VERSION"] != nlTokenVersionOut {
		bad("deploy backends env NIGHTLY_HUBSPOT_TOKEN_VERSION is not " + nlTokenVersionOut)
	}

	co := jobs["contacts"]
	if x, _ := jobKey(co, "name"); x != "Real contact reaches HubSpot and Resend" {
		bad("contacts name is " + x)
	}
	if x, _ := jobKey(co, "timeout-minutes"); x != "15" {
		bad("contacts timeout-minutes is not 15")
	}
	if !slices.Equal(needsOf(co), []string{"changes", "prepare-env", "deploy", "e2e", "topology"}) {
		bad("contacts needs are wrong")
	}
	if x, _ := foldedIf(co); x != nlContactsIf {
		bad("contacts if is not exactly " + nlContactsIf)
	}
	if !slices.Contains(blockLines(co.lines, 4, "env"), "NIGHTLY_RUN_ID: ${{ github.run_id }}-${{ github.run_attempt }}") {
		bad("contacts NIGHTLY_RUN_ID is not run_id-run_attempt")
	}
	guard, gok := nlStep(co, "Refuse a re-run that skipped the deploy")
	check, cok := nlStep(co, "Check the contacts")
	if !gok || !cok || guard.index > check.index || guard.keys["id"] != "attempt" ||
		guard.env["DEPLOYED_ATTEMPT"] != "${{ needs.deploy.outputs.attempt }}" || guard.env["RUN_ATTEMPT"] != "${{ github.run_attempt }}" ||
		!strings.Contains(strings.Join(nlRaw(co, "Refuse a re-run that skipped the deploy"), "\n"), `[ "$DEPLOYED_ATTEMPT" != "$RUN_ATTEMPT" ]`) {
		bad("contacts lacks the re-run guard before the check")
	}
	if g := jobs["e2e-gate"]; slices.Contains(needsOf(g), "contacts") || slices.Contains(needsOf(g), "contacts-cleanup") {
		bad("e2e-gate needs a contacts job")
	}
	if !strings.Contains(nlCode(strings.Join(co.lines, "\n")), `preview.sh contact-token "$PR" "`+nlTokenVersionOut+`"`) {
		bad("contacts reads the token without the prepare-env version")
	}

	cl := jobs["contacts-cleanup"]
	if x, _ := jobKey(cl, "name"); x != "Delete the run's contacts" {
		bad("contacts-cleanup name is " + x)
	}
	if x, _ := jobKey(cl, "timeout-minutes"); x != "20" {
		bad("contacts-cleanup timeout-minutes is not 20")
	}
	if !slices.Equal(needsOf(cl), []string{"changes", "commit-authors", "prepare-env", "nightly-teardown"}) {
		bad("contacts-cleanup needs are wrong")
	}
	if x, _ := foldedIf(cl); x != nlTeardown {
		bad("contacts-cleanup if is not exactly " + nlTeardown)
	}
	if !slices.Equal(blockLines(cl.lines, 4, "permissions"), []string{"contents: read", "actions: read", "id-token: write"}) {
		bad("contacts-cleanup permissions are wrong")
	}
	dl := strings.Join(nlRaw(cl, "Download the contacts results"), "\n")
	if !strings.Contains(dl, "continue-on-error: true") {
		bad("contacts-cleanup download can fail the job before the delete")
	}
	for _, w := range []string{"uses: actions/download-artifact@v4", "pattern: contacts-*", "merge-multiple: true"} {
		if !strings.Contains(dl, w) {
			bad("contacts-cleanup download lacks " + w)
		}
	}
	if !strings.Contains(nlCode(strings.Join(cl.lines, "\n")), `preview.sh contact-token "$PR" "`+nlTokenVersionOut+`"`) {
		bad("contacts-cleanup reads the token without the prepare-env version")
	}
	if !strings.Contains(strings.Join(cl.lines, "\n"), "run_started_at") {
		bad("contacts-cleanup does not read run_started_at")
	}
	if strings.Count(strings.Join(cl.lines, "\n"), "::add-mask::") != 3 {
		bad("contacts-cleanup does not mask the token, key and domain")
	}
	if !slices.Contains(nlRaw(cl, "Upload the nightly results"), "name: results-hubspot") {
		bad("contacts-cleanup does not upload results-hubspot")
	}
	return p
}

const nlEmailIf = "!cancelled() && " + nlOn + " && " + authorCond +
	" && needs.deploy.result == 'success' && needs.health-gate.result == 'success' && needs.fleet-gate.result == 'success'"

// needsTeardown reports whether nightly-teardown is among a job's transitive needs: it starts after the mail phase.
func needsTeardown(jobs map[string]workflowJob, id string) bool {
	seen := map[string]bool{}
	var walk func(string) bool
	walk = func(id string) bool {
		for _, n := range needsOf(jobs[id]) {
			if n == "nightly-teardown" || (!seen[n] && walk(n)) {
				return true
			}
			seen[n] = true
		}
		return false
	}
	return walk(id)
}

// aiCheckProblems names each clause of the nightly-only nightly-ai job that the text breaks.
func aiCheckProblems(jobs map[string]workflowJob) []string {
	var p []string
	bad := func(msg string) { p = append(p, msg) }
	ai, ok := jobs["nightly-ai"]
	if !ok {
		return []string{"no nightly-ai job"}
	}
	if x, _ := jobKey(ai, "name"); x != "AI features and accuracy" {
		bad("nightly-ai name is " + x)
	}
	if x, _ := jobKey(ai, "timeout-minutes"); x != "30" {
		bad("nightly-ai timeout-minutes is not 30")
	}
	if x, _ := foldedIf(ai); x != nlEmailIf {
		bad("nightly-ai if is not exactly " + nlEmailIf)
	}
	if !slices.Equal(needsOf(ai), []string{"changes", "commit-authors", "prepare-env", "deploy", "health-gate", "fleet-gate", "e2e", "topology", "contacts"}) {
		bad("nightly-ai needs are wrong")
	}
	if !slices.Equal(blockLines(ai.lines, 4, "permissions"), []string{"contents: read"}) || strings.Contains(strings.Join(ai.lines, "\n"), "id-token") {
		bad("nightly-ai permissions are not contents: read alone")
	}
	env := strings.Join(blockLines(ai.lines, 4, "env"), "\n")
	for _, v := range []struct{ name, out string }{
		{"GATEWAY_URL", "gateway_url"}, {"APP_URL", "app_url"}, {"LANDING_URL", "landing_url"}, {"OPS_CONSOLE_URL", "ops_console_url"},
		{"SUPPORT_CONSOLE_URL", "support_console_url"}, {"LIBRARY_URL", "library_url"}, {"BLOG_URL", "blog_url"},
	} {
		if !strings.Contains(env, v.name+": ${{ needs.prepare-env.outputs."+v.out+" }}") {
			bad("nightly-ai env lacks " + v.name + " from prepare-env")
		}
	}
	guard, gok := nlStep(ai, "Refuse a re-run that skipped the deploy")
	check, cok := nlStep(ai, "Check the AI features and accuracy")
	if !gok || !cok || guard.index > check.index || guard.keys["id"] != "attempt" ||
		guard.env["DEPLOYED_ATTEMPT"] != "${{ needs.deploy.outputs.attempt }}" || guard.env["RUN_ATTEMPT"] != "${{ github.run_attempt }}" ||
		!strings.Contains(strings.Join(nlRaw(ai, "Refuse a re-run that skipped the deploy"), "\n"), `[ "$DEPLOYED_ATTEMPT" != "$RUN_ATTEMPT" ]`) {
		bad("nightly-ai lacks the re-run guard before the check")
	}
	if cok && check.keys["run"] != `pnpm --filter @invoice-os/e2e nightly-ai --out "$GITHUB_WORKSPACE/nightly-results/ai.json"` {
		bad("nightly-ai check step does not write nightly-results/ai.json")
	}
	if !slices.Contains(needsOf(jobs["email"]), "nightly-ai") {
		bad("email needs lack nightly-ai")
	}
	if !slices.Contains(needsOf(jobs["nightly-teardown"]), "nightly-ai") {
		bad("nightly-teardown needs lack nightly-ai")
	}
	if slices.Contains(needsOf(jobs["e2e-gate"]), "nightly-ai") {
		bad("e2e-gate needs nightly-ai")
	}
	raw := nlRaw(ai, "Upload the nightly results")
	for _, w := range []string{"if: always() && " + nlOn, "name: results-ai-check", "path: nightly-results/", "if-no-files-found: ignore", "retention-days: 14"} {
		if !slices.Contains(raw, w) {
			bad("nightly-ai results upload lacks " + w)
		}
	}
	return p
}

// emailProblems names each clause of the nightly-only email job that the text breaks.
func emailProblems(jobs map[string]workflowJob) []string {
	var p []string
	bad := func(msg string) { p = append(p, msg) }
	em, ok := jobs["email"]
	if !ok {
		return []string{"no email job"}
	}
	if x, _ := jobKey(em, "name"); x != "Real invite and reset mail" {
		bad("email name is " + x)
	}
	if x, _ := jobKey(em, "timeout-minutes"); x != "25" {
		bad("email timeout-minutes is not 25")
	}
	if x, _ := foldedIf(em); x != nlEmailIf {
		bad("email if is not exactly " + nlEmailIf)
	}
	if !slices.Equal(needsOf(em), []string{"changes", "commit-authors", "invariants", "prepare-env", "deploy", "health-gate", "fleet-gate", "e2e", "topology", "contacts", "nightly-ai"}) {
		bad("email needs are wrong")
	}
	for _, e := range blockLines(em.lines, 4, "env") {
		if strings.Contains(e, "${{ runner.") {
			bad("email job env reads the runner context: " + e)
		}
		for _, x := range regexpExpr.FindAllStringSubmatch(e, -1) {
			if !strings.HasPrefix(x[1], "github.") && !strings.HasPrefix(x[1], "needs.") {
				bad("email job env reads a context beyond github and needs: " + e)
			}
		}
	}
	if _, ok := jobs["e2e-gate"]; ok && slices.Contains(needsOf(jobs["e2e-gate"]), "email") {
		bad("e2e-gate needs email")
	}
	if !slices.Contains(needsOf(jobs["nightly-teardown"]), "email") {
		bad("nightly-teardown needs lack email")
	}

	seen := map[string]bool{}
	var walk func(string)
	walk = func(id string) {
		for _, n := range needsOf(jobs[id]) {
			if !seen[n] {
				seen[n] = true
				walk(n)
			}
		}
	}
	walk("email")
	for id, j := range jobs {
		if id == "email" || id == "nightly-teardown" || needsTeardown(jobs, id) {
			continue
		}
		if x, _ := foldedIf(j); !strings.Contains(x, nlOff) && !seen[id] {
			bad("job " + id + " can run beside the mail phase: not in email's needs")
		}
	}

	idx := func(match func(workflowStep) bool) int {
		for i, s := range em.steps() {
			if match(s) {
				return i
			}
		}
		return -1
	}
	run := func(sub string) int {
		return idx(func(s workflowStep) bool { return strings.Contains(nlCode(s.keys["run"]), sub) })
	}
	auth := idx(func(s workflowStep) bool { return strings.HasPrefix(s.keys["uses"], "google-github-actions/auth@") })
	pre := run("preview.sh mail-preflight")
	key := run("preview.sh mail-read-key")
	prep := run("test:mail prepare.spec.ts")
	on := run("preview.sh mail-on")
	spec := run("test:mail real-mail.spec.ts")
	chrome := run("playwright install")
	off := run("preview.sh mail-off")
	up := idx(func(s workflowStep) bool { return strings.HasPrefix(s.keys["uses"], "actions/upload-artifact@") })
	if !(0 <= chrome && chrome < auth) {
		bad("email installs Chromium after Google auth: the install would see the key")
	}
	if !(spec < off && off < up) {
		bad("email mail-off is not between the spec and the upload")
	}
	if st, ok := nlStep(em, "Turn real mail off"); !ok || strings.TrimSpace(nlCode(st.keys["if"])) != "always() && steps.mailon.outcome != 'skipped'" {
		bad("email mail-off step does not run if always() once mail-on ran")
	}
	if !(0 <= auth && auth < pre && pre < key && key < prep && prep < on && on < spec) {
		bad("email steps are out of order: auth, preflight, key read, prepare, mail-on, real-mail")
	}
	if st, ok := nlStep(em, "Read the Resend read key and receiving domain"); ok {
		r := nlCode(st.keys["run"])
		if strings.Count(r, "::add-mask::") != 2 || strings.Index(r, "::add-mask::$key") > strings.Index(r, "GITHUB_ENV") || strings.Index(r, "::add-mask::$domain") > strings.Index(r, "GITHUB_ENV") {
			bad("email masks the key and domain after writing GITHUB_ENV")
		}
	} else {
		bad("email lacks the key read step")
	}
	if st, ok := nlStep(em, "Real invite and reset mail"); !ok || st.keys["timeout-minutes"] != "16" {
		bad("email spec step timeout-minutes is not 16")
	}
	raw := nlRaw(em, "Upload the nightly results")
	for _, w := range []string{"if: always() && " + nlOn, "name: results-email", "path: nightly-results/", "retention-days: 14"} {
		if !slices.Contains(raw, w) {
			bad("email results upload lacks " + w)
		}
	}
	if strings.Contains(strings.Join(em.lines, "\n"), "playwright-report") {
		bad("email job names a playwright-report")
	}
	return p
}

func requireDevEnvProblem(t *testing.T, old, repl, want string) {
	t.Helper()
	got := devEnvNightlyProblems(mutateOnce(t, readWorkflow(t, "dev-env.yml"), old, repl))
	if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, want) }) {
		t.Errorf("problems %q lack one naming %q", got, want)
	}
}

func TestDevEnvNightly_RealFileHasNoProblems(t *testing.T) {
	t.Parallel()
	if got := devEnvNightlyProblems(readWorkflow(t, "dev-env.yml")); len(got) > 0 {
		t.Errorf("problems: %q", got)
	}
}

// The job if: and needs of dev-env.yml at 7b01a4bf.
var nlGolden = map[string][2]string{
	"ci-watch":    {"[changes, commit-authors]", "github.event.pull_request.draft == false && needs.changes.outputs.e2e == 'true' && " + authorCond},
	"invariants":  {"[commit-authors]", authorCond},
	"prepare-env": {"[changes, commit-authors]", "github.event.pull_request.draft == false && needs.changes.outputs.e2e == 'true' && " + authorCond},
	"build":       {"[changes, commit-authors]", "github.event.pull_request.draft == false && needs.changes.outputs.e2e == 'true' && " + authorCond},
	"deploy":      {"[prepare-env, build]", "!cancelled() && needs.prepare-env.result == 'success' && needs.build.result == 'success'"},
	"health-gate": {"[deploy, prepare-env]", "!cancelled() && needs.deploy.result == 'success'"},
	"fleet-gate":  {"[deploy, prepare-env]", "!cancelled() && needs.deploy.result == 'success'"},
	"e2e": {"[fleet-gate, health-gate, prepare-env, deploy, ci-watch]",
		"!cancelled() && needs.fleet-gate.result == 'success' && needs.health-gate.result == 'success' && needs.ci-watch.result == 'success'"},
}

func TestDevEnvNightly_PRExpressionsUnchanged(t *testing.T) {
	t.Parallel()
	jobs := nlJobs(readWorkflow(t, "dev-env.yml"))
	for name, g := range nlGolden {
		j := jobs[name]
		needs, _ := jobKey(j, "needs")
		if name == "e2e" {
			needs = strings.Replace(needs, "[changes, ", "[", 1) // the one added edge
		}
		if needs != g[0] {
			t.Errorf("%s needs = %q, want %q", name, needs, g[0])
		}
		if x, _ := foldedIf(j); x != g[1] {
			t.Errorf("%s if = %q, want %q", name, x, g[1])
		}
	}
	for _, n := range []string{"contacts", "contacts-cleanup"} {
		if slices.Contains(needsOf(jobs["e2e-gate"]), n) {
			t.Errorf("e2e-gate needs %s", n)
		}
	}
}

func TestDevEnvNightly_ModeStepRuns(t *testing.T) {
	t.Parallel()
	step, ok := nlStepByID(devEnvJob(t, "changes"), "mode")
	if !ok {
		t.Fatal("changes has no mode step")
	}
	for nightly, want := range map[string]string{
		"true":  "nightly=true\ntopology_after=success failure\n",
		"false": "nightly=false\ntopology_after=success\n",
	} {
		out := filepath.Join(t.TempDir(), "out")
		_, stderr, code := runBashScript(t, "NIGHTLY="+nightly+" GITHUB_OUTPUT="+out+"\nexport NIGHTLY GITHUB_OUTPUT\n"+step.keys["run"])
		got, _ := os.ReadFile(out)
		if code != 0 || string(got) != want {
			t.Errorf("NIGHTLY=%s: exit %d, output %q, want %q; %s", nightly, code, got, want, stderr)
		}
	}
}

func TestDevEnvNightly_TopologyWaitsForE2EToEnd(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "dev-env.yml")
	for _, c := range []struct{ old, repl, want string }{
		{`if: "` + nlTopoIf + `"`, `if: "!cancelled() && contains(needs.changes.outputs.topology_after, needs.e2e.result)"`, "topology if"},
		{"needs: [changes, commit-authors, e2e, prepare-env, deploy]", "needs: [changes, e2e, prepare-env, deploy]", "topology needs"},
		{"shard: ${{ fromJSON(needs.changes.outputs.shards) }}", "shard: ${{ fromJSON(needs.e2e.outputs.shards) }}", "topology matrix"},
		{"            echo \"topology_after=success\" >>\"$GITHUB_OUTPUT\"", "            echo \"topology_after=success failure\" >>\"$GITHUB_OUTPUT\"", "mode"},
	} {
		got := devEnvNightlyProblems(mutateOnce(t, yml, c.old, c.repl))
		if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, c.want) }) {
			t.Errorf("mutation of %q: problems %q lack %q", c.old, got, c.want)
		}
	}
	// Dropping the author conjunct leaves topology unguarded for Dependabot.
	dropped := mutateOnce(t, yml, " && "+authorCond+"\"\n    runs-on: ubuntu-latest\n    # ceiling", "\"\n    runs-on: ubuntu-latest\n    # ceiling")
	if got := unguardedOIDCJobs(dropped); !slices.Contains(got, "topology") {
		t.Errorf("unguarded OIDC jobs = %v, want topology", got)
	}
	// An e2e shard step brings the shard list back to e2e.
	withShard := mutateOnce(t, yml, "      - name: Install deps\n        run: pnpm install --frozen-lockfile --ignore-scripts",
		"      - id: shards\n        run: echo\n      - name: Install deps\n        run: pnpm install --frozen-lockfile --ignore-scripts")
	if got := devEnvNightlyProblems(withShard); !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, "e2e keeps the shards step") }) {
		t.Errorf("problems %q lack the e2e shard step", got)
	}
}

func TestDevEnvNightly_ModeComesFromTheDispatch(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ old, repl, want string }{
		{"  workflow_dispatch:\n", "", "workflow_dispatch"},
		{"github.event.pull_request.number || 'nightly' }}\n  cancel", "github.event.pull_request.number }}\n  cancel", "concurrency group"},
		{"  PR: ${{ github.event.pull_request.number || 'nightly' }}", "  PR: ${{ github.event.pull_request.number }}", "env.PR"},
		{"nightly: ${{ steps.mode.outputs.nightly }}", "nightly: ${{ steps.mode.outputs.x }}", "changes outputs"},
		{"e2e: ${{ github.event_name == 'workflow_dispatch' && 'true' || steps.lane.outputs.e2e }}", "e2e: ${{ steps.lane.outputs.e2e }}", "changes outputs"},
		{"NIGHTLY: ${{ github.event_name == 'workflow_dispatch' }}", "NIGHTLY: false", "mode step"},
		{"        id: filter\n        if: github.event_name == 'pull_request'\n", "        id: filter\n", "step filter"},
	} {
		requireDevEnvProblem(t, c.old, c.repl, c.want)
	}
}

func TestDevEnvNightly_PrepareEnvStopsAtTheCap(t *testing.T) {
	t.Parallel()
	pe := devEnvJob(t, "prepare-env")
	for _, n := range []string{"Delete a leftover nightly preview", "Prove no nightly preview is left", "Count the previews", "preview cap reached"} {
		s, ok := nlStep(pe, n)
		if !ok || !strings.HasPrefix(s.keys["if"], nlOn) {
			t.Errorf("step %q is missing or not nightly only (if = %q)", n, s.keys["if"])
		}
	}
	yml := readWorkflow(t, "dev-env.yml")
	for _, c := range []struct{ old, repl, want string }{
		{"        if: " + nlOn + " && steps.count.outputs.full != 'false'\n", "        if: " + nlOn + "\n", "preview cap reached if"},
		{"      - name: preview cap reached\n", "      - name: preview cap reached early\n", "out of order"},
		{"      - name: Delete a leftover nightly preview\n        if: " + nlOn + "\n", "      - name: Delete a leftover nightly preview\n", "not nightly only"},
	} {
		got := devEnvNightlyProblems(mutateOnce(t, yml, c.old, c.repl))
		if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, c.want) }) {
			t.Errorf("mutation %q: problems %q lack %q", c.old, got, c.want)
		}
	}
	// Moving the cap step after the secrets step.
	capStep := "      - name: preview cap reached\n        if: " + nlOn + " && steps.count.outputs.full != 'false'\n        env:\n          OPEN: ${{ steps.count.outputs.open }}\n        run: |\n          echo \"::error::preview cap reached: $OPEN previews open (.claude/rules/ci-preview.md)\"\n          exit 1\n"
	secrets := "      - name: Create the PR's secrets (generated, never copied from production)\n        run: bash scripts/ci/preview.sh secrets \"$PR\"\n"
	moved := mutateOnce(t, mutateOnce(t, yml, capStep, ""), secrets, secrets+capStep)
	if got := devEnvNightlyProblems(moved); !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, "out of order") }) {
		t.Errorf("a cap step after the secrets step went unnoticed: %q", got)
	}
}

// nlE2ETimeout: the nightly gets 40 minutes; a PR keeps GitHub's 360-minute default (Q1).
const nlE2ETimeout = "${{ needs.changes.outputs.nightly == 'true' && 40 || 360 }}"

// nlCapFails as the cap output makes the stub's `cap` exit non-zero.
const nlCapFails = "!fail"

// nlRunStep runs the named step of a job with a stub preview.sh; the stub's prs and cap output are the arguments.
func nlRunStep(t *testing.T, job, step, prs, capOut string, env ...string) (string, int) {
	t.Helper()
	s, ok := nlStep(devEnvJob(t, job), step)
	if !ok {
		t.Fatalf("no step %q in %s", step, job)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts/ci"), 0o755); err != nil {
		t.Fatal(err)
	}
	capBody := "printf '%s' '" + capOut + "'"
	if capOut == nlCapFails {
		capBody = "echo 'gcloud blip' >&2; exit 3"
	}
	writeIn(t, filepath.Join(dir, "scripts/ci"), "preview.sh", "case \"$1\" in\nprs) printf '%s' '"+prs+"' ;;\ncap) "+capBody+" ;;\nesac\n")
	script := "cd '" + dir + "'\n" + strings.Join(env, "\n") + "\nexport GITHUB_OUTPUT='" + filepath.Join(dir, "out") + "' GITHUB_STEP_SUMMARY='" + filepath.Join(dir, "summary") + "'\n" + s.keys["run"]
	stdout, stderr, code := runBashScript(t, script)
	return stdout + stderr, code
}

func nlRunPreview(t *testing.T, step, prs, capOut string, env ...string) (string, int) {
	t.Helper()
	return nlRunStep(t, "prepare-env", step, prs, capOut, env...)
}

func TestDevEnvNightly_CapStepsRun(t *testing.T) {
	t.Parallel()
	const prove = "Prove no nightly preview is left"
	if out, code := nlRunPreview(t, prove, "nightly 2026\n7 2026\n", ""); code != 1 || !strings.Contains(out, "::error::") {
		t.Errorf("a nightly line: exit %d, out %q, want exit 1 with ::error::", code, out)
	}
	for _, prs := range []string{"7 2026\n", "", "nightly2 2026\n"} {
		if out, code := nlRunPreview(t, prove, prs, ""); code != 0 {
			t.Errorf("prs %q: exit %d, out %q, want 0", prs, code, out)
		}
	}
	if out, code := nlRunPreview(t, "preview cap reached", "", "", "OPEN=3"); code != 1 || !strings.Contains(out, "preview cap reached: 3") {
		t.Errorf("cap step: exit %d, out %q, want exit 1 with 'preview cap reached: 3'", code, out)
	}
	const gone = "Prove the nightly preview is gone"
	if out, code := nlRunStep(t, "nightly-teardown", gone, "nightly 2026\n7 2026\n", ""); code != 1 || !strings.Contains(out, "::error::") {
		t.Errorf("teardown with a nightly line left: exit %d, out %q, want exit 1 with ::error::", code, out)
	}
	for _, prs := range []string{"7 2026\n", "", "nightly2 2026\n"} {
		if out, code := nlRunStep(t, "nightly-teardown", gone, prs, ""); code != 0 {
			t.Errorf("teardown, prs %q: exit %d, out %q, want 0", prs, code, out)
		}
	}
	out, code := nlRunPreview(t, "Count the previews", "", "open=1\nfull=false\n")
	if code != 0 || !strings.Contains(out, "full=false") {
		t.Errorf("count step: exit %d, out %q", code, out)
	}
}

// A failing `cap` must fail the step (GitHub's default shell has no pipefail), and a missing
// `full` must not read as "not full".
func TestDevEnvNightly_FailedCapCountFailsTheStep(t *testing.T) {
	t.Parallel()
	if out, code := nlRunPreview(t, "Count the previews", "", nlCapFails); code == 0 {
		t.Errorf("count step exited 0 with a failing cap, out %q", out)
	}
	pe := devEnvJob(t, "prepare-env")
	if c, _ := nlStep(pe, "preview cap reached"); !strings.Contains(c.keys["if"], "steps.count.outputs.full != 'false'") {
		t.Errorf("cap guard if = %q, want it to fire unless full is 'false'", c.keys["if"])
	}
}

func TestDevEnvNightly_PRGatesSkipped(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ old, repl, want string }{
		{"        name: Wait for the CI check to succeed on this commit\n        if: " + nlOff + "\n", "        name: Wait for the CI check to succeed on this commit\n", "ci-watch wait"},
		{"    if: always() && " + nlOff + "\n    runs-on: ubuntu-latest\n    timeout-minutes: 5", "    if: always()\n    runs-on: ubuntu-latest\n    timeout-minutes: 5", "e2e-gate"},
		{"if: always() && steps.attempt.outcome == 'success' && " + nlOff + "\n", "if: always() && steps.attempt.outcome == 'success'\n", "e2e flaky gate"},
		{"steps.targets.outcome == 'success' && " + nlOff, "steps.targets.outcome == 'success'", "topology flaky gate"},
	} {
		requireDevEnvProblem(t, c.old, c.repl, c.want)
	}
}

func TestDevEnvNightly_ResultsUploaded(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ old, repl, want string }{
		{"      " + nlTarget + "\n    steps:\n      # \"Re-run", "    steps:\n      # \"Re-run", "e2e env lacks E2E_TARGET"},
		{nlJSONFile("playwright-smoke.json"), "PLAYWRIGHT_JSON_OUTPUT_FILE: ''", "Smoke"},
		{nlJSONFile("playwright-api.json"), "PLAYWRIGHT_JSON_OUTPUT_FILE: ''", "API E2E"},
		{"name: results-e2e", "name: results", "results upload lacks name: results-e2e"},
		{"name: results-topology-${{ matrix.shard }}", "name: results-topology", "topology results upload lacks"},
		{"        if: always() && " + nlOn + "\n        uses: actions/upload-artifact@v7\n        with:\n          name: results-e2e", "        if: always()\n        uses: actions/upload-artifact@v7\n        with:\n          name: results-e2e", "e2e results upload lacks if"},
		{"          name: results-e2e\n          path: nightly-results/\n          if-no-files-found: ignore\n          retention-days: 14", "          name: results-e2e\n          path: nightly-results/\n          if-no-files-found: ignore\n          retention-days: 7", "retention-days: 14"},
	} {
		got := devEnvNightlyProblems(mutateOnce(t, readWorkflow(t, "dev-env.yml"), c.old, c.repl))
		if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, c.want) }) {
			t.Errorf("mutation %q: problems %q lack %q", c.old, got, c.want)
		}
	}
}

func TestDevEnvNightly_HealthGateHasNoModeBranch(t *testing.T) {
	t.Parallel()
	hg := devEnvJob(t, "health-gate")
	s, _ := nlStep(hg, "Wait for gateway /health 200 on the commit under test")
	for _, w := range []string{`"$mock" != on`, `"$issuers" != 2`} {
		if !strings.Contains(s.keys["run"], w) {
			t.Errorf("health gate lacks %q", w)
		}
	}
	if strings.Contains(strings.Join(hg.lines, "\n"), "nightly") {
		t.Error("health-gate names the nightly mode")
	}
	requireDevEnvProblem(t, `if [ "$issuers" != 2 ]; then`, `if [ "$issuers" != 1 ]; then`, "health-gate lacks")
}

func TestDevEnvNightly_TeardownAlwaysRunsInNightlyModeOnly(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "dev-env.yml")
	for _, c := range []struct{ old, repl, want string }{
		{"nightly-ai, email]\n    if: " + nlTeardown + "\n", "nightly-ai, email]\n    if: success() && " + nlOn + " && " + authorCond + "\n", "nightly-teardown if"},
		{"nightly-ai, email]\n    if: " + nlTeardown + "\n", "nightly-ai, email]\n    if: always() && " + authorCond + "\n", "nightly-teardown if"},
		{"    timeout-minutes: 20\n    permissions:\n      contents: read\n      id-token: write", "    timeout-minutes: 30\n    permissions:\n      contents: read\n      id-token: write", "timeout-minutes is not 20"},
		{"needs: [changes, commit-authors, prepare-env, build, deploy, health-gate, fleet-gate, e2e, topology, contacts, nightly-ai, email]", "needs: [changes, commit-authors, prepare-env, build, deploy]", "nightly-teardown needs"},
		{"        run: bash scripts/ci/preview.sh teardown \"$PR\"\n      - name: Prove the nightly preview is gone", "        run: echo skip\n      - name: Prove the nightly preview is gone", "does not tear down"},
	} {
		got := devEnvNightlyProblems(mutateOnce(t, yml, c.old, c.repl))
		if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, c.want) }) {
			t.Errorf("mutation %q: problems %q lack %q", c.old, got, c.want)
		}
	}
}

func TestDevEnvNightly_EveryPreviewJobHasATimeout(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "dev-env.yml")
	n := 0
	for name, j := range nlJobs(yml) {
		if !strings.Contains(strings.Join(j.lines, "\n"), "scripts/ci/preview.sh") {
			continue
		}
		n++
		if _, ok := jobKey(j, "timeout-minutes"); !ok {
			t.Errorf("job %s calls preview.sh without timeout-minutes", name)
		}
	}
	if v, _ := jobKey(nlJobs(yml)["e2e"], "timeout-minutes"); v != nlE2ETimeout {
		t.Errorf("e2e timeout-minutes = %q, want %q (40 nightly, the 360 default on a PR)", v, nlE2ETimeout)
	}
	if n < 8 {
		t.Errorf("control: only %d jobs call preview.sh", n)
	}
	requireDevEnvProblem(t, "    runs-on: ubuntu-latest\n    timeout-minutes: 15\n    permissions:\n      contents: read\n      id-token: write\n    outputs:\n      gateway_url", "    runs-on: ubuntu-latest\n    permissions:\n      contents: read\n      id-token: write\n    outputs:\n      gateway_url", "job prepare-env calls")
}

// The contacts step of fleet-gate, trimmed per line.
var nlContactsGate = []string{
	`name: Gate on notifications' contacts delivery mode`,
	`run: |`,
	`set -euo pipefail`,
	`want=fake`,
	`[ "$PR" != nightly ] || want=real`,
	`mode=""`,
	`for _ in $(seq 1 5); do`,
	`if curl -sS --connect-timeout 5 --max-time 40 -H "X-Gateway-Token: $GATEWAY_TOKEN" -o /tmp/contacts-fleet.json "$GATEWAY_URL/healthz/fleet" \`,
	`&& jq -e '.services | type == "array"' /tmp/contacts-fleet.json >/dev/null 2>&1; then`,
	`mode=$(jq -r '[.services[] | select(.name == "notifications") | .contacts // "none"][0] // "absent"' /tmp/contacts-fleet.json)`,
	`if [ "$mode" = "$want" ]; then break; fi`,
	`fi`,
	`sleep 5`,
	`done`,
	`if [ "$mode" != "$want" ]; then`,
	`echo "::error::notifications reports contacts='${mode:-unreachable}', want '$want'. On a PR, anything but fake means the preview could write to HubSpot or Resend: check DEPLOY_ENV is pr-<N>."`,
	`exit 1`,
	`fi`,
	`echo "notifications reports contacts=$want."`,
}

func TestDevEnvNightly_ContactsGatePinned(t *testing.T) {
	t.Parallel()
	got := nlRaw(devEnvJob(t, "fleet-gate"), "Gate on notifications' contacts delivery mode")
	if !slices.Equal(got, nlContactsGate) {
		t.Errorf("contacts gate changed:\n%s", strings.Join(got, "\n"))
	}
}

// Runs the step's script with a shim curl that serves a fleet roll-up reporting contacts=$mode.
func TestDevEnvContactsGate_WantsTheModeOfTheRun(t *testing.T) {
	t.Parallel()
	raw := nlRaw(devEnvJob(t, "fleet-gate"), "Gate on notifications' contacts delivery mode")
	i := slices.Index(raw, "run: |")
	if i < 0 {
		t.Fatal("contacts gate has no run block")
	}
	script := strings.Join(raw[i+1:], "\n")
	for _, c := range []struct {
		pr, mode string
		pass     bool
	}{
		{"nightly", "real", true}, {"nightly", "fake", false}, {"7", "fake", true}, {"7", "real", false},
	} {
		c := c
		t.Run(c.pr+"/"+c.mode, func(t *testing.T) {
			t.Parallel()
			shims := t.TempDir()
			writeShim(t, filepath.Join(shims, "curl"), `while [ $# -gt 0 ]; do [ "$1" = -o ] && out=$2; shift; done
printf '{"services":[{"name":"notifications","contacts":"%s"}]}' "$MODE" >"$out"
`)
			writeShim(t, filepath.Join(shims, "sleep"), "exit 0\n")
			// The step writes a fixed /tmp path; give each case its own.
			cmd := exec.Command("bash", "-c", strings.ReplaceAll(script, "/tmp/contacts-fleet.json", filepath.Join(shims, "fleet.json")))
			cmd.Env = append(os.Environ(), "PR="+c.pr, "MODE="+c.mode, "GATEWAY_URL=x", "GATEWAY_TOKEN=x", "PATH="+shims+string(os.PathListSeparator)+os.Getenv("PATH"))
			_, _, err := runToFiles(t, cmd, false)
			if got := err == nil; got != c.pass {
				t.Errorf("PR %s with contacts=%s: pass = %v, want %v (%v)", c.pr, c.mode, got, c.pass, err)
			}
		})
	}
}

func requireEmailMutations(t *testing.T, rows []struct{ old, repl, want string }) {
	t.Helper()
	yml := readWorkflow(t, "dev-env.yml")
	for _, c := range rows {
		got := devEnvNightlyProblems(mutateOnce(t, yml, c.old, c.repl))
		if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, c.want) }) {
			t.Errorf("mutation %q: problems %q lack %q", c.old, got, c.want)
		}
	}
}

func TestDevEnvNightly_EmailJobRunsInNightlyModeOnly(t *testing.T) {
	t.Parallel()
	requireEmailMutations(t, []struct{ old, repl, want string }{
		{"contacts, nightly-ai]\n    if: >-\n      !cancelled() &&\n      needs.changes.outputs.nightly == 'true' &&\n", "contacts, nightly-ai]\n    if: >-\n      !cancelled() &&\n", "email if"},
		{"    name: Real invite and reset mail\n", "    name: Mail\n", "email name"},
		{"    timeout-minutes: 25\n    permissions:\n      contents: read\n      actions: read", "    timeout-minutes: 40\n    permissions:\n      contents: read\n      actions: read", "email timeout-minutes"},
		{"        timeout-minutes: 16\n", "        timeout-minutes: 30\n", "timeout-minutes is not 16"},
		{"        if: always() && " + nlOn + "\n        uses: actions/upload-artifact@v7\n        with:\n          name: results-email", "        if: always()\n        uses: actions/upload-artifact@v7\n        with:\n          name: results-email", "email results upload lacks"},
	})
}

func TestDevEnvNightly_EmailJobEnvUsesAllowedContexts(t *testing.T) {
	t.Parallel()
	requireEmailMutations(t, []struct{ old, repl, want string }{
		{"      MAIL_SECTION_FILE: ${{ github.workspace }}/nightly-results/email.json", "      MAIL_SECTION_FILE: ${{ runner.temp }}/email.json", "runner context"},
	})
}

func TestDevEnvNightly_MailOnWaitsForE2E(t *testing.T) {
	t.Parallel()
	n := "needs: [changes, commit-authors, invariants, prepare-env, deploy, health-gate, fleet-gate, e2e, topology, contacts, nightly-ai]"
	requireEmailMutations(t, []struct{ old, repl, want string }{
		{n, strings.Replace(n, "e2e, ", "", 1), "email needs"},
		{n, strings.Replace(n, ", topology", "", 1), "email needs"},
		{n, strings.Replace(n, ", contacts", "", 1), "email needs"},
		{n, strings.Replace(n, ", nightly-ai", "", 1), "email needs"},
		{"fleet-gate, e2e, topology, contacts, nightly-ai, email]", "fleet-gate, e2e, topology, contacts, email]", "nightly-teardown needs"},
		{"fleet-gate, e2e, topology, contacts, nightly-ai, email]", "fleet-gate, e2e, topology, nightly-ai, email]", "nightly-teardown needs"},
	})
}

func TestDevEnvNightly_GuardRunsBeforeAnyDeploy(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "dev-env.yml")
	pf := "      - name: Check the contact secrets\n        if: " + nlOn + "\n        run: bash scripts/ci/preview.sh contact-preflight \"$PR\"\n"
	tv := "      - id: token_version\n        name: Read the HubSpot token version\n        if: " + nlOn + "\n"
	for _, c := range []struct{ old, repl, want string }{
		{pf + tv, tv + pf, "out of order"},
		{"      - name: Check the contact secrets\n        if: " + nlOn + "\n", "      - name: Check the contact secrets\n", "Check the contact secrets is not nightly only"},
		{"      - id: token_version\n        name: Read the HubSpot token version\n        if: " + nlOn + "\n", "      - id: token_version\n        name: Read the HubSpot token version\n", "Read the HubSpot token version is not nightly only"},
		{"    needs: [prepare-env, build]\n", "    needs: [build]\n", "deploy does not need prepare-env"},
		{"!cancelled() && needs.prepare-env.result == 'success' && needs.build.result == 'success'", "!cancelled() && needs.build.result == 'success'", "deploy if does not require"},
		{"        if: always() && " + nlOn + "\n        uses: actions/upload-artifact@v7\n        with:\n          name: contacts-start", "        if: " + nlOn + "\n        uses: actions/upload-artifact@v7\n        with:\n          name: contacts-start", "contacts-start upload"},
	} {
		got := devEnvNightlyProblems(mutateOnce(t, yml, c.old, c.repl))
		if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, c.want) }) {
			t.Errorf("mutation %q: problems %q lack %q", c.old, got, c.want)
		}
	}
}

func TestDevEnvNightly_GuardAndServicesUseOneTokenVersion(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "dev-env.yml")
	read := `preview.sh contact-token "$PR" "` + nlTokenVersionStep + `"`
	for _, c := range []struct{ old, repl, want string }{
		{read, `preview.sh contact-token "$PR"`, "start reads the token without"},
		{"hubspot_token_version: " + nlTokenVersionStep, "hubspot_token_version: ${{ steps.urls.outputs.gateway_url }}", "output hubspot_token_version"},
		{"NIGHTLY_HUBSPOT_TOKEN_VERSION: " + nlTokenVersionOut, "NIGHTLY_HUBSPOT_TOKEN_VERSION: latest", "NIGHTLY_HUBSPOT_TOKEN_VERSION is not"},
	} {
		got := devEnvNightlyProblems(mutateOnce(t, yml, c.old, c.repl))
		if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, c.want) }) {
			t.Errorf("mutation %q: problems %q lack %q", c.old, got, c.want)
		}
	}
	read2 := `preview.sh contact-token "$PR" "` + nlTokenVersionOut + `"`
	first := strings.Index(yml, read2)
	second := first + len(read2) + strings.Index(yml[first+len(read2):], read2)
	for name, at := range map[string]int{"contacts": first, "contacts-cleanup": second} {
		got := contactProblems(nlJobs(yml[:at] + `preview.sh contact-token "$PR"` + yml[at+len(read2):]))
		if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, name+" reads the token without") }) {
			t.Errorf("%s: problems %q", name, got)
		}
	}
}

func TestDevEnvNightly_ContactsJobRunsWhenE2EFails(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "dev-env.yml")
	for _, c := range []struct{ old, repl, want string }{
		{`if: "` + nlContactsIf + `"`, `if: "!cancelled() && needs.deploy.result == 'success'"`, "contacts if"},
		{`if: "` + nlContactsIf + `"`, `if: "!cancelled() && ` + nlOn + ` && needs.e2e.result == 'success'"`, "contacts if"},
		{"needs: [changes, prepare-env, deploy, e2e, topology]", "needs: [changes, prepare-env, deploy, e2e]", "contacts needs"},
		{"NIGHTLY_RUN_ID: ${{ github.run_id }}-${{ github.run_attempt }}", "NIGHTLY_RUN_ID: ${{ github.run_id }}", "NIGHTLY_RUN_ID"},
		{"    name: Real contact reaches HubSpot and Resend\n", "    name: Contacts\n", "contacts name"},
	} {
		got := devEnvNightlyProblems(mutateOnce(t, yml, c.old, c.repl))
		if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, c.want) }) {
			t.Errorf("mutation %q: problems %q lack %q", c.old, got, c.want)
		}
	}
}

func TestDevEnvNightly_ContactsGuardsAndDownloadSurviveFailure(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "dev-env.yml")
	for _, c := range []struct{ old, repl, want string }{
		{"        continue-on-error: true\n        uses: actions/download-artifact@v4\n        with:\n          pattern: contacts-*", "        uses: actions/download-artifact@v4\n        with:\n          pattern: contacts-*", "download can fail"},
		{"          DEPLOYED_ATTEMPT: ${{ needs.deploy.outputs.attempt }}\n          RUN_ATTEMPT: ${{ github.run_attempt }}\n        run: |\n          if [ \"$DEPLOYED_ATTEMPT\" != \"$RUN_ATTEMPT\" ]; then\n            echo \"::error::Re-run the whole workflow, not failed jobs — the fleet", "          DEPLOYED_ATTEMPT: ${{ needs.deploy.outputs.attempt }}\n          RUN_ATTEMPT: ${{ github.run_attempt }}\n        run: |\n          if [ \"$DEPLOYED_ATTEMPT\" = \"$RUN_ATTEMPT\" ]; then\n            echo \"::error::Re-run the whole workflow, not failed jobs — the fleet", "re-run guard"},
		{"      - name: Refuse a re-run that skipped the deploy\n        id: attempt\n        env:\n          DEPLOYED_ATTEMPT: ${{ needs.deploy.outputs.attempt }}\n          RUN_ATTEMPT: ${{ github.run_attempt }}\n        run: |\n          if [ \"$DEPLOYED_ATTEMPT\" != \"$RUN_ATTEMPT\" ]; then\n            echo \"::error::Re-run the whole workflow, not failed jobs — the fleet", "      - name: Refuse a re-run\n        id: attempt\n        env:\n          DEPLOYED_ATTEMPT: ${{ needs.deploy.outputs.attempt }}\n          RUN_ATTEMPT: ${{ github.run_attempt }}\n        run: |\n          if [ \"$DEPLOYED_ATTEMPT\" != \"$RUN_ATTEMPT\" ]; then\n            echo \"::error::Re-run the whole workflow, not failed jobs — the fleet", "re-run guard"},
	} {
		got := devEnvNightlyProblems(mutateOnce(t, yml, c.old, c.repl))
		if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, c.want) }) {
			t.Errorf("mutation %q: problems %q lack %q", c.old, got, c.want)
		}
	}
}

func TestDevEnvNightly_CleanupRunsAfterTeardown(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "dev-env.yml")
	cn := "needs: [changes, commit-authors, prepare-env, nightly-teardown]"
	cif := "    if: always() && " + nlOn + " && " + authorCond + "\n    runs-on: ubuntu-latest\n    timeout-minutes: 20\n    permissions:\n      contents: read\n      actions: read"
	for _, c := range []struct{ old, repl, want string }{
		{cn, "needs: [changes, commit-authors, prepare-env, e2e]", "contacts-cleanup needs"},
		{cif, strings.Replace(cif, "always() && "+nlOn+" && ", "always() && ", 1), "contacts-cleanup if"},
		{cif, strings.Replace(cif, "always()", "success()", 1), "contacts-cleanup if"},
		{"pattern: contacts-*\n          merge-multiple: true", "pattern: contacts-*", "merge-multiple"},
		{"    name: Delete the run's contacts\n", "    name: Cleanup\n", "contacts-cleanup name"},
	} {
		got := devEnvNightlyProblems(mutateOnce(t, yml, c.old, c.repl))
		if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, c.want) }) {
			t.Errorf("mutation %q: problems %q lack %q", c.old, got, c.want)
		}
	}
}

func TestDevEnvNightly_CleanupFollowsTheMailPhase(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "dev-env.yml")
	if got := emailProblems(nlJobs(yml)); len(got) > 0 {
		t.Fatalf("real file: %q", got)
	}
	got := emailProblems(nlJobs(mutateOnce(t, yml, "needs: [changes, commit-authors, prepare-env, nightly-teardown]", "needs: [changes, commit-authors, prepare-env]")))
	if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, "job contacts-cleanup can run beside the mail phase") }) {
		t.Errorf("problems %q", got)
	}
}

func TestDevEnvNightly_EmailSkipsARedFleet(t *testing.T) {
	t.Parallel()
	requireEmailMutations(t, []struct{ old, repl, want string }{
		{" &&\n      needs.health-gate.result == 'success' &&\n      needs.fleet-gate.result == 'success'\n    runs-on: ubuntu-latest\n    timeout-minutes: 25", "\n    runs-on: ubuntu-latest\n    timeout-minutes: 25", "email if"},
		{"      needs.deploy.result == 'success' &&\n      needs.health-gate.result == 'success' &&\n      needs.fleet-gate.result == 'success'\n    runs-on: ubuntu-latest\n    timeout-minutes: 25", "      needs.deploy.result == 'success' &&\n      needs.health-gate.result == 'success'\n    runs-on: ubuntu-latest\n    timeout-minutes: 25", "email if"},
	})
}

func TestDevEnvNightly_NothingRunsBesideTheMailPhase(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "dev-env.yml")
	add := func(job string) string {
		return mutateOnce(t, yml, "  # Real invite and reset mail, after", job+"\n  # Real invite and reset mail, after")
	}
	for _, needs := range []string{"needs: [deploy]\n", "needs: [changes]\n", ""} {
		got := devEnvNightlyProblems(add("  extra:\n    runs-on: ubuntu-latest\n    " + needs + "    steps:\n      - run: echo\n"))
		if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, "job extra can run beside the mail phase") }) {
			t.Errorf("job with %q: problems %q", needs, got)
		}
	}
	// A job that email needs is clean.
	clean := mutateOnce(t, add("  extra:\n    runs-on: ubuntu-latest\n    needs: [deploy]\n    steps:\n      - run: echo\n"),
		"fleet-gate, e2e, topology, contacts, nightly-ai]\n    if: >-\n      !cancelled() &&\n      needs.changes.outputs.nightly == 'true' &&", "fleet-gate, e2e, topology, contacts, nightly-ai, extra]\n    if: >-\n      !cancelled() &&\n      needs.changes.outputs.nightly == 'true' &&")
	if got := emailProblems(nlJobs(clean)); slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, "beside the mail phase") }) {
		t.Errorf("a job in email's needs is a problem: %q", got)
	}
}

func TestDevEnvNightly_EmailStepOrder(t *testing.T) {
	t.Parallel()
	cmd := map[string]string{
		"pf":   "        run: bash scripts/ci/preview.sh mail-preflight \"$PR\"\n",
		"on":   "        run: bash scripts/ci/preview.sh mail-on \"$PR\"\n",
		"prep": "        run: pnpm --filter @invoice-os/e2e test:mail prepare.spec.ts\n",
		"spec": "        run: pnpm --filter @invoice-os/e2e test:mail real-mail.spec.ts\n",
	}
	yml := readWorkflow(t, "dev-env.yml")
	for _, pair := range [][2]string{{"pf", "prep"}, {"prep", "on"}, {"on", "spec"}} {
		a, b := cmd[pair[0]], cmd[pair[1]]
		sw := mutateOnce(t, mutateOnce(t, yml, a, "@@A@@"), b, a)
		sw = strings.Replace(sw, "@@A@@", b, 1)
		if got := emailProblems(nlJobs(sw)); !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, "out of order") }) {
			t.Errorf("swap %v: problems %q", pair, got)
		}
	}
	auth := "      - uses: google-github-actions/auth@v3\n        with:\n          workload_identity_provider: ${{ env.WIF_PROVIDER }}\n          service_account: ${{ env.DEPLOYER }}\n"
	pf := "      - name: Check the nightly mail secrets\n        run: bash scripts/ci/preview.sh mail-preflight \"$PR\"\n"
	if got := emailProblems(nlJobs(mutateOnce(t, yml, auth+pf, pf+auth))); !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, "out of order") }) {
		t.Errorf("swap auth and preflight: problems %q", got)
	}
	chrome := "      - name: Install Chromium\n        run: pnpm --filter @invoice-os/e2e exec playwright install --with-deps chromium\n"
	specStep := "      - name: Real invite and reset mail\n        timeout-minutes: 16\n" + cmd["spec"]
	off := "      - name: Turn real mail off\n        if: always() && steps.mailon.outcome != 'skipped'\n        run: bash scripts/ci/preview.sh mail-off \"$PR\"\n"
	requireEmailMutations(t, []struct{ old, repl, want string }{
		{"          echo \"::add-mask::$domain\"\n          {\n            echo \"RESEND_READ_KEY", "          {\n            echo \"RESEND_READ_KEY", "masks the key and domain"},
		{chrome + auth + pf, auth + chrome + pf, "installs Chromium after Google auth"},
		{off, "", "mail-off is not between"},
		{specStep + off, off + specStep, "mail-off is not between"},
		{"      - name: Turn real mail off\n        if: always() && steps.mailon.outcome != 'skipped'\n", "      - name: Turn real mail off\n", "mail-off step does not run if always()"},
		{"        if: always() && steps.mailon.outcome != 'skipped'\n", "        if: always()\n", "mail-off step does not run if always()"},
	})
}

// requireAIMutations applies each row to the first match inside the nightly-ai job.
func requireAIMutations(t *testing.T, rows []struct{ old, repl, want string }) {
	t.Helper()
	yml := readWorkflow(t, "dev-env.yml")
	at := strings.Index(yml, "\n  nightly-ai:\n")
	if at < 0 {
		t.Fatal("no nightly-ai job")
	}
	for _, c := range rows {
		j := strings.Index(yml[at:], c.old)
		if j < 0 {
			t.Fatalf("nightly-ai lacks %q", c.old)
		}
		j += at
		got := devEnvNightlyProblems(yml[:j] + c.repl + yml[j+len(c.old):])
		if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, c.want) }) {
			t.Errorf("mutation %q: problems %q lack %q", c.old, got, c.want)
		}
	}
}

const nlAINeeds = "needs: [changes, commit-authors, prepare-env, deploy, health-gate, fleet-gate, e2e, topology, contacts]"

func TestDevEnvNightly_AICheckJobIsNightlyOnly(t *testing.T) {
	t.Parallel()
	rows := []struct{ old, repl, want string }{
		{"name: AI features and accuracy", "name: AI", "nightly-ai name"},
		{"timeout-minutes: 30", "timeout-minutes: 40", "nightly-ai timeout-minutes"},
		{"      contents: read\n", "      contents: read\n      id-token: write\n", "nightly-ai permissions"},
		{nlAINeeds, strings.Replace(nlAINeeds, ", topology", "", 1), "nightly-ai needs"},
		{nlAINeeds, strings.Replace(nlAINeeds, ", contacts", "", 1), "nightly-ai needs"},
		{"      needs.changes.outputs.nightly == 'true' &&\n", "", "nightly-ai if"},
		{"        if: always() && " + nlOn + "\n", "        if: always()\n", "nightly-ai results upload lacks"},
		{"name: results-ai-check", "name: results-ai", "name: results-ai-check"},
	}
	for _, v := range []string{"GATEWAY_URL", "APP_URL", "LANDING_URL", "OPS_CONSOLE_URL", "SUPPORT_CONSOLE_URL", "LIBRARY_URL", "BLOG_URL"} {
		rows = append(rows, struct{ old, repl, want string }{"      " + v + ": ", "      " + v + "_: ", "nightly-ai env lacks " + v})
	}
	requireAIMutations(t, rows)
}

func TestDevEnvNightly_AICheckRunsBeforeTheMailPhase(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "dev-env.yml")
	for _, c := range []struct{ old, repl, want string }{
		{"topology, contacts, nightly-ai]\n    if: >-", "topology, contacts]\n    if: >-", "email needs"},
		{nlAINeeds, strings.Replace(nlAINeeds, ", contacts", "", 1), "nightly-ai needs"},
	} {
		got := devEnvNightlyProblems(mutateOnce(t, yml, c.old, c.repl))
		if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, c.want) }) {
			t.Errorf("mutation %q: problems %q lack %q", c.old, got, c.want)
		}
	}
}

func TestDevEnvNightly_TeardownWaitsForTheAICheck(t *testing.T) {
	t.Parallel()
	got := devEnvNightlyProblems(mutateOnce(t, readWorkflow(t, "dev-env.yml"), "contacts, nightly-ai, email]", "contacts, email]"))
	if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, "nightly-teardown needs") }) {
		t.Errorf("problems %q", got)
	}
}

func TestDevEnvNightly_AICheckRunsAfterARedTopology(t *testing.T) {
	t.Parallel()
	jobs := nlJobs(readWorkflow(t, "dev-env.yml"))
	x, _ := foldedIf(jobs["nightly-ai"])
	for _, r := range []string{"needs.e2e.result", "needs.topology.result", "needs.contacts.result"} {
		if strings.Contains(x, r) {
			t.Errorf("nightly-ai if names %s", r)
		}
	}
	for _, r := range []string{"needs.deploy.result", "needs.health-gate.result", "needs.fleet-gate.result"} {
		if !strings.Contains(x, r) {
			t.Errorf("nightly-ai if lacks %s", r)
		}
	}
	requireAIMutations(t, []struct{ old, repl, want string }{
		{"      needs.deploy.result == 'success' &&\n", "      needs.deploy.result == 'success' &&\n      needs.e2e.result == 'success' &&\n", "nightly-ai if"},
		{"      needs.fleet-gate.result == 'success'\n", "      needs.fleet-gate.result == 'success' &&\n      needs.contacts.result == 'success'\n", "nightly-ai if"},
		{" &&\n      needs.fleet-gate.result == 'success'\n", "\n", "nightly-ai if"},
	})
}

func TestDevEnvNightly_AICheckRefusesARerun(t *testing.T) {
	t.Parallel()
	requireAIMutations(t, []struct{ old, repl, want string }{
		{`[ "$DEPLOYED_ATTEMPT" != "$RUN_ATTEMPT" ]`, `[ "$DEPLOYED_ATTEMPT" = "" ]`, "nightly-ai lacks the re-run guard"},
		{"DEPLOYED_ATTEMPT: ${{ needs.deploy.outputs.attempt }}", "DEPLOYED_ATTEMPT: ${{ needs.build.outputs.attempt }}", "nightly-ai lacks the re-run guard"},
		{"      - name: Refuse a re-run that skipped the deploy\n", "      - name: Skip the guard\n", "nightly-ai lacks the re-run guard"},
	})
}
