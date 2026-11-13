// deploy_prod_workflow_test.go runs the digest-deploy steps of deploy-prod.yml and dev-env.yml's probe step
// with bash and the prodBox shims (prod_image_script_test.go). The source scans at the end each name a failure
// that no local run can observe.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	takeScriptStep   = "Take prod-image.sh from this workflow's commit"
	pointJobsStep    = "Point the jobs at this commit"
	startRevsStep    = "Start the new revisions without traffic"
	probeStep        = "Push a probe image by digest"
	previewsRegistry = "africa-south1-docker.pkg.dev/ascomply-preview/previews"
)

// prodServices are the 18 services production deploys by digest.
var prodServices = []string{"gateway", "auth", "tenancy", "portfolio", "invoice", "validation", "dashboard", "submission",
	"notifications", "worker", "docling", "app", "landing", "library", "blog", "check", "ops-console", "support-console"}

func stepOf(t *testing.T, workflow, job, name string) workflowStep {
	t.Helper()
	for _, j := range workflowJobsOf(readWorkflow(t, workflow)) {
		if j.name != job {
			continue
		}
		for _, s := range j.steps() {
			if s.keys["name"] == name {
				return s
			}
		}
		t.Fatalf("%s: job %s has no step named %q", workflow, job, name)
	}
	t.Fatalf("%s has no job %s", workflow, job)
	return workflowStep{}
}

// buildBox is a runner that checked out OLD (shallow, as actions/checkout does) while the workflow runs at NEW,
// whose tree adds scripts/ci/prod-image.sh.
func buildBox(t *testing.T) (b *prodBox, oldSHA, newSHA string) {
	t.Helper()
	script, err := os.ReadFile(requireProdImageScript(t))
	if err != nil {
		t.Fatal(err)
	}
	b = newProdBox(t)
	seed, origin := filepath.Join(b.root, "seed"), filepath.Join(b.root, "origin.git")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	prodShaGit(t, seed, "init", "-b", "main")
	oldSHA = prodShaCommit(t, seed, "Dockerfile")
	if err := os.MkdirAll(filepath.Join(seed, "scripts", "ci"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(seed, "scripts", "ci", "prod-image.sh"), string(script))
	prodShaGit(t, seed, "add", "scripts/ci/prod-image.sh")
	prodShaGit(t, seed, "commit", "-m", "adds prod-image.sh")
	newSHA = prodShaGit(t, seed, "rev-parse", "HEAD")
	prodShaGit(t, b.root, "clone", "--bare", seed, origin)
	prodShaGit(t, origin, "config", "uploadpack.allowAnySHA1InWant", "true")
	prodShaGit(t, b.ws, "init", "-b", "work")
	prodShaGit(t, b.ws, "remote", "add", "origin", origin)
	prodShaGit(t, b.ws, "-c", "protocol.file.allow=always", "fetch", "--depth", "1", "origin", oldSHA)
	prodShaGit(t, b.ws, "checkout", "--detach", "FETCH_HEAD")
	runner := filepath.Join(b.root, "runner")
	if err := os.MkdirAll(runner, 0o755); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"GITHUB_SHA": newSHA, "RUNNER_TEMP": runner, "SPA": "false", "SERVICE": "gateway",
		"DOCKERFILE": "Dockerfile", "IMAGE_NAME": "gateway", "SHA": strings.Repeat("b", 40), "PROJECT": "proj-x", "REGION": "region-x",
		"HUBSPOT_PORTAL_ID": "1", "GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "protocol.file.allow", "GIT_CONFIG_VALUE_0": "always"} {
		b.env[k] = v
	}
	return b, oldSHA, newSHA
}

func TestDeployProd_BuildStepsRecordTheirOwnPushDigest(t *testing.T) {
	t.Parallel()
	take := stepOf(t, "deploy-prod.yml", "build", takeScriptStep)
	build := stepOf(t, "deploy-prod.yml", "build", "Build and push")
	b, oldSHA, newSHA := buildBox(t)
	digest := "sha256:" + hex64("a")
	b.meta(digest, prodRegistry+"/gateway")

	if r := b.step(take); r.code != 0 {
		t.Fatalf("%s: exit %d stderr %q", takeScriptStep, r.code, r.stderr)
	}
	got, err := os.ReadFile(filepath.Join(b.env["RUNNER_TEMP"], "prod-image.sh"))
	want, _ := os.ReadFile(requireProdImageScript(t))
	if err != nil || string(got) != string(want) {
		t.Errorf("$RUNNER_TEMP/prod-image.sh is not NEW's scripts/ci/prod-image.sh (read error %v)", err)
	}
	if head := prodShaGit(t, b.ws, "rev-parse", "HEAD"); head != oldSHA {
		t.Errorf("workspace HEAD = %s, want OLD %s (NEW is %s): the build context must stay the target commit", head, oldSHA, newSHA)
	}
	if _, ok := b.wsFile("scripts/ci/prod-image.sh"); ok {
		t.Error("the workspace gained scripts/ci/prod-image.sh; the step must not touch the build context")
	}

	if r := b.step(build); r.code != 0 {
		t.Fatalf("Build and push: exit %d stderr %q", r.code, r.stderr)
	}
	if got, ok := b.wsFile("digests/gateway"); !ok || got != digest+"\n" {
		t.Errorf("digests/gateway = %q (exists %v), want %q", got, ok, digest+"\n")
	}
	argv := b.dockerArgv()
	wantTail := "-f Dockerfile --build-arg BUILD_SHA=" + b.env["SHA"] + " --build-arg SERVICE=gateway ."
	if len(argv) == 0 || !strings.HasPrefix(argv[len(argv)-1], "buildx build ") || !strings.HasSuffix(argv[len(argv)-1], " "+wantTail) {
		t.Errorf("docker argv:\n%s\nwant the last call to be buildx build ... %s", strings.Join(argv, "\n"), wantTail)
	}
}

func TestDeployProd_BuildStepFailsWhenThePushFails(t *testing.T) {
	t.Parallel()
	take := stepOf(t, "deploy-prod.yml", "build", takeScriptStep)
	build := stepOf(t, "deploy-prod.yml", "build", "Build and push")
	b, _, _ := buildBox(t)
	b.meta("sha256:"+hex64("a"), prodRegistry+"/gateway")
	b.put("build-fails", "1")
	if r := b.step(take); r.code != 0 {
		t.Fatalf("%s: exit %d stderr %q", takeScriptStep, r.code, r.stderr)
	}
	r := b.step(build)
	if r.code == 0 {
		t.Error("Build and push exited 0 although buildx build failed")
	}
	if !slices.ContainsFunc(b.dockerArgv(), func(l string) bool { return strings.HasPrefix(l, "buildx build ") }) {
		t.Errorf("buildx build was never reached (stderr %q); the failure under test is the push", r.stderr)
	}
	if got, ok := b.wsFile("digests/gateway"); ok {
		t.Errorf("digests/gateway = %q after a failed push", got)
	}
}

func migrateBox(t *testing.T) *prodBox {
	t.Helper()
	b := newProdBox(t)
	b.installScript()
	b.env["PROJECT"], b.env["REGION"], b.env["SHA"] = "proj-x", "region-x", strings.Repeat("b", 40)
	return b
}

func TestDeployProd_MigratePointsTheJobsAtThisRunsDigests(t *testing.T) {
	t.Parallel()
	step := stepOf(t, "deploy-prod.yml", "migrate", pointJobsStep)
	b := migrateBox(t)
	b.digestFile("migrate", "sha256:"+hex64("1")+"\n")
	b.digestFile("deploycheck", "sha256:"+hex64("2")+"\n")
	r := b.step(step)
	if r.code != 0 {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	migrateRef := prodRegistry + "/migrate@sha256:" + hex64("1")
	checkRef := prodRegistry + "/deploycheck@sha256:" + hex64("2")
	calls := b.lines("gcloud.log")
	if len(calls) != 3 {
		t.Fatalf("gcloud calls = %v, want 3 job updates", calls)
	}
	out := strings.Split(r.stdout, "\n")
	for _, c := range []struct{ job, ref string }{{"migrate", migrateRef}, {"migrate-bootstrap", migrateRef}, {"deploy-check", checkRef}} {
		prefix := "run jobs update " + c.job + " --image " + c.ref
		if !slices.ContainsFunc(calls, func(l string) bool { return l == prefix || strings.HasPrefix(l, prefix+" ") }) {
			t.Errorf("no gcloud call %q in %v", prefix, calls)
		}
		echo := slices.Index(out, "gcloud "+prefix)
		call := slices.IndexFunc(out, func(l string) bool { return strings.HasPrefix(l, "CALL "+prefix) })
		if echo < 0 || call < 0 || echo > call {
			t.Errorf("stdout lacks the echo %q before its call (echo at %d, call at %d):\n%s", "gcloud "+prefix, echo, call, r.stdout)
		}
	}
}

func TestDeployProd_MigrateMakesNoCallWithoutBothDigests(t *testing.T) {
	t.Parallel()
	step := stepOf(t, "deploy-prod.yml", "migrate", pointJobsStep)
	for _, tc := range []struct{ name, migrate, check, named string }{
		{"deploycheck digest missing", "sha256:" + hex64("1") + "\n", "\x00missing", "deploycheck"},
		{"migrate digest has 63 hex", "sha256:" + strings.Repeat("1", 63) + "\n", "sha256:" + hex64("2") + "\n", "migrate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := migrateBox(t)
			b.digestFile("migrate", tc.migrate)
			if tc.check != "\x00missing" {
				b.digestFile("deploycheck", tc.check)
			}
			r := b.step(step)
			if r.code == 0 || !strings.Contains(r.stderr, tc.named) {
				t.Errorf("exit %d stderr %q, want a failure naming %s", r.code, r.stderr, tc.named)
			}
			if calls := b.lines("gcloud.log"); len(calls) != 0 {
				t.Errorf("gcloud calls before the digests were checked: %v", calls)
			}
		})
	}
	// Control: both digests present, three calls.
	b := migrateBox(t)
	b.digestFile("migrate", "sha256:"+hex64("1")+"\n")
	b.digestFile("deploycheck", "sha256:"+hex64("2")+"\n")
	if r := b.step(step); r.code != 0 || len(b.lines("gcloud.log")) != 3 {
		t.Errorf("control: exit %d stderr %q gcloud calls %v", r.code, r.stderr, b.lines("gcloud.log"))
	}
}

func deployBox(t *testing.T, omit string) (*prodBox, map[string]string) {
	t.Helper()
	b := newProdBox(t)
	b.installScript()
	b.env["SHA"], b.env["TAG"], b.env["PROJECT"], b.env["REGION"] = strings.Repeat("d", 40), "c-dddddddddddd", "proj-x", "region-x"
	refs := map[string]string{}
	for i, svc := range prodServices {
		d := fmt.Sprintf("sha256:%064x", i+1)
		refs[svc] = prodRegistry + "/" + svc + "@" + d
		if svc != omit {
			b.digestFile(svc, d+"\n")
		}
	}
	return b, refs
}

func TestDeployProd_DeployStartsEveryRevisionFromThisRunsDigest(t *testing.T) {
	t.Parallel()
	step := stepOf(t, "deploy-prod.yml", "deploy", startRevsStep)
	b, refs := deployBox(t, "")
	r := b.step(step)
	if r.code != 0 {
		t.Fatalf("exit %d stderr %q stdout %q", r.code, r.stderr, r.stdout)
	}
	calls := b.lines("gcloud.log")
	if len(calls) != len(prodServices) {
		t.Fatalf("%d gcloud calls, want %d:\n%s", len(calls), len(prodServices), strings.Join(calls, "\n"))
	}
	seen := map[string]bool{}
	for _, c := range calls {
		f := strings.Fields(c)
		if len(f) < 3 || f[0] != "run" || f[1] != "deploy" {
			t.Errorf("unexpected gcloud call %q", c)
			continue
		}
		svc := f[2]
		seen[svc] = true
		if flagValue(c, "--image") != refs[svc] || flagValue(c, "--update-labels") != "commit="+b.env["SHA"] ||
			flagValue(c, "--tag") != b.env["TAG"] || flagValue(c, "--project") != "proj-x" || flagValue(c, "--region") != "region-x" ||
			!slices.Contains(f, "--no-traffic") {
			t.Errorf("call for %s is off spec: %q (want --image %s --update-labels commit=%s --no-traffic --tag %s)", svc, c, refs[svc], b.env["SHA"], b.env["TAG"])
		}
	}
	for _, svc := range prodServices {
		if !seen[svc] {
			t.Errorf("no deploy call for %s", svc)
		}
	}
	echo := regexp.MustCompile(`(?m)^gcloud run deploy (\S+) --image (\S+)$`)
	echoed := map[string]string{}
	for _, m := range echo.FindAllStringSubmatch(r.stdout, -1) {
		echoed[m[1]] = m[2]
	}
	for _, svc := range prodServices {
		if echoed[svc] != refs[svc] {
			t.Errorf("stdout echo for %s = %q, want %q", svc, echoed[svc], refs[svc])
		}
	}
}

func TestDeployProd_DeployStartsNoRevisionWithoutEveryDigest(t *testing.T) {
	t.Parallel()
	step := stepOf(t, "deploy-prod.yml", "deploy", startRevsStep)
	b, _ := deployBox(t, "app")
	r := b.step(step)
	if r.code == 0 {
		t.Error("exit 0 with no digest for app")
	}
	if calls := b.lines("gcloud.log"); len(calls) != 0 {
		t.Errorf("gcloud calls without every digest: %v", calls)
	}
	if !regexp.MustCompile(`\bapp\b`).MatchString(errorLines(r.stderr)) {
		t.Errorf("stderr %q has no ::error:: line naming app", r.stderr)
	}
	// Control: with every digest the same step starts all 18.
	b, _ = deployBox(t, "")
	if r := b.step(step); r.code != 0 || len(b.lines("gcloud.log")) != len(prodServices) {
		t.Errorf("control: exit %d, %d gcloud calls", r.code, len(b.lines("gcloud.log")))
	}
}

// probeBox runs dev-env.yml's probe step in a workspace that holds the tree's prod-image.sh.
func probeBox(t *testing.T, listing string) (*prodBox, workflowStep) {
	t.Helper()
	step := stepOf(t, "dev-env.yml", "invariants", probeStep)
	if step.env["REGISTRY"] != previewsRegistry {
		t.Fatalf("probe step REGISTRY = %q, want %q", step.env["REGISTRY"], previewsRegistry)
	}
	b := newProdBox(t)
	b.installScript()
	b.env["REGISTRY"] = step.env["REGISTRY"]
	b.env["GITHUB_RUN_ID"], b.env["GITHUB_RUN_ATTEMPT"] = "42", "1"
	b.env["RUNNER_TEMP"] = filepath.Join(b.root, "runner")
	if err := os.MkdirAll(b.env["RUNNER_TEMP"], 0o755); err != nil {
		t.Fatal(err)
	}
	b.meta("sha256:"+hex64("e"), previewsRegistry+"/prod-image-probe")
	b.put("list.json", listing)
	return b, step
}

const (
	manifestType = "application/vnd.oci.image.manifest.v1+json"
	indexType    = "application/vnd.oci.image.index.v1+json"
)

func listRow(digest, tags, mediaType string) string {
	return fmt.Sprintf(`{"version":%q,"tags":%s,"metadata":{"mediaType":%q}}`, digest, tags, mediaType)
}

func TestDevEnv_InvariantsProbePushesByDigestToPreviews(t *testing.T) {
	t.Parallel()
	d := "sha256:" + hex64("e")
	other := listRow("sha256:"+hex64("f"), `["latest"]`, indexType)
	b, step := probeBox(t, "["+listRow(d, "[]", manifestType)+","+other+"]")
	r := b.step(step)
	if r.code != 0 {
		t.Fatalf("exit %d stderr %q stdout %q", r.code, r.stderr, r.stdout)
	}
	if got, err := os.ReadFile(filepath.Join(b.env["RUNNER_TEMP"], "probe", "run.txt")); err != nil || string(got) != "42-1\n" {
		t.Errorf("probe/run.txt = %q (%v), want %q", got, err, "42-1\n")
	}
	if !strings.Contains(r.stdout, "probe "+d+": one untagged image manifest") {
		t.Errorf("stdout lacks the success line for %s:\n%s", d, r.stdout)
	}
	out := "--output type=image,name=" + previewsRegistry + "/prod-image-probe,push-by-digest=true,push=true"
	if !slices.ContainsFunc(b.dockerArgv(), func(l string) bool { return strings.HasPrefix(l, "buildx build ") && strings.Contains(l, " "+out+" ") }) {
		t.Errorf("no buildx build with %s in:\n%s", out, strings.Join(b.dockerArgv(), "\n"))
	}
	for _, want := range []string{
		"artifacts docker images list " + previewsRegistry + "/prod-image-probe --include-tags --format json",
		"auth configure-docker africa-south1-docker.pkg.dev --quiet",
	} {
		if !slices.Contains(b.lines("gcloud.log"), want) {
			t.Errorf("gcloud log lacks %q: %v", want, b.lines("gcloud.log"))
		}
	}
}

func TestDevEnv_InvariantsProbeFailsWhenThePushDidNotLandUntagged(t *testing.T) {
	t.Parallel()
	d := "sha256:" + hex64("e")
	other := listRow("sha256:"+hex64("f"), `["latest"]`, indexType)
	for _, tc := range []struct{ name, listing string }{
		{"digest absent from the listing", "[" + other + "]"},
		{"digest absent, another untagged manifest listed", "[" + listRow("sha256:"+hex64("f"), "[]", manifestType) + "]"},
		{"empty listing", "[]"},
		{"digest carries a tag", "[" + listRow(d, `["x"]`, manifestType) + "," + other + "]"},
		{"digest is an image index", "[" + listRow(d, "[]", indexType) + "," + other + "]"},
		{"digest listed twice", "[" + listRow(d, "[]", manifestType) + "," + listRow(d, "[]", manifestType) + "]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, step := probeBox(t, tc.listing)
			r := b.step(step)
			if r.code == 0 || !strings.Contains(errorLines(r.stdout+"\n"+r.stderr), "previews") {
				t.Errorf("exit %d stdout %q stderr %q, want a failure with ::error:: naming previews", r.code, r.stdout, r.stderr)
			}
		})
	}

	// The listing itself fails three times: tried 3 times, 20 s apart, then the step fails.
	b, step := probeBox(t, "[]")
	b.put("list-fails", "99")
	r := b.step(step)
	if r.code == 0 || !strings.Contains(errorLines(r.stdout+"\n"+r.stderr), "previews") {
		t.Errorf("failing listing: exit %d stdout %q stderr %q, want a failure with ::error:: naming previews", r.code, r.stdout, r.stderr)
	}
	lists := 0
	for _, l := range b.lines("gcloud.log") {
		if strings.HasPrefix(l, "artifacts docker images list ") {
			lists++
		}
	}
	if lists != 3 {
		t.Errorf("listing tried %d times, want 3", lists)
	}
	if s := sleepsIn(t, b.bin); !slices.Equal(s, []string{"20", "20"}) {
		t.Errorf("sleeps = %v, want [20 20]", s)
	}

	// Control: the clean listing passes.
	b, step = probeBox(t, "["+listRow(d, "[]", manifestType)+"]")
	if r := b.step(step); r.code != 0 {
		t.Errorf("control: exit %d stderr %q", r.code, r.stderr)
	}
}

// ---- source scans: GitHub-only behaviour (see the plan's [workflow-scans]) ----

type stepChunk struct {
	name  string
	lines []string // trimmed
}

func stepChunks(j workflowJob) []stepChunk {
	var out []stepChunk
	inSteps := false
	for _, line := range j.lines {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		trimmed := strings.TrimSpace(line)
		if !inSteps {
			inSteps = indent == 4 && trimmed == "steps:"
			continue
		}
		if trimmed == "" {
			continue
		}
		if indent <= 4 {
			break
		}
		if indent == 6 && strings.HasPrefix(trimmed, "- ") {
			out = append(out, stepChunk{})
			trimmed = strings.TrimPrefix(trimmed, "- ")
		}
		c := &out[len(out)-1]
		if c.name == "" && strings.HasPrefix(trimmed, "name:") {
			c.name = strings.TrimSpace(strings.TrimPrefix(trimmed, "name:"))
		}
		c.lines = append(c.lines, trimmed)
	}
	return out
}

// imageFlagProblems: every --image sits in a step that a run test executes, and that step resolves it through prod-image.sh.
func imageFlagProblems(yml string) []string {
	executed := map[string]bool{"migrate/" + pointJobsStep: false, "deploy/" + startRevsStep: false}
	var p []string
	inSteps := 0
	for _, j := range workflowJobsOf(yml) {
		for _, c := range stepChunks(j) {
			key := j.name + "/" + c.name
			hasImage, resolves := false, false
			for _, l := range c.lines {
				if strings.Contains(l, "--image") {
					hasImage = true
					inSteps++
				}
				if strings.Contains(l, "prod-image.sh") {
					resolves = true
				}
			}
			if !hasImage {
				continue
			}
			if _, ok := executed[key]; !ok {
				p = append(p, key+": --image in a step no run test executes")
				continue
			}
			executed[key] = true
			if !resolves {
				p = append(p, key+": --image without prod-image.sh resolving the reference")
			}
		}
	}
	total := 0
	for _, l := range stripHashComments(strings.Split(yml, "\n")) {
		if strings.Contains(l, "--image") {
			total++
		}
	}
	if total != inSteps {
		p = append(p, fmt.Sprintf("%d --image lines in the file, %d inside a job's steps", total, inSteps))
	}
	for k, seen := range executed {
		if !seen {
			p = append(p, k+": executed step holds no --image")
		}
	}
	return p
}

func requireNoProblems(t *testing.T, what string, p []string) {
	t.Helper()
	if len(p) > 0 {
		t.Fatalf("%s:\n  %s", what, strings.Join(p, "\n  "))
	}
}

func requireProblemWith(t *testing.T, row string, p []string, want string) {
	t.Helper()
	if !slices.ContainsFunc(p, func(s string) bool { return strings.Contains(s, want) }) {
		t.Errorf("%s: problems %q do not mention %q", row, p, want)
	}
}

// editJobLine replaces (repl "" drops) the first line of job whose trimmed text is old, keeping its indent.
func editJobLine(t *testing.T, yml, job, old, repl string) string {
	t.Helper()
	lines := strings.Split(yml, "\n")
	in := false
	for i, l := range lines {
		if strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") && strings.HasSuffix(strings.TrimSpace(l), ":") {
			in = strings.TrimSpace(l) == job+":"
			continue
		}
		if in && strings.TrimSpace(l) == old {
			if repl == "" {
				return strings.Join(append(lines[:i:i], lines[i+1:]...), "\n")
			}
			lines[i] = l[:len(l)-len(strings.TrimLeft(l, " "))] + repl
			return strings.Join(lines, "\n")
		}
	}
	t.Fatalf("job %s has no line %q to edit", job, old)
	return ""
}

func TestDeployProd_EveryImageFlagIsInAnExecutedStep(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "deploy-prod.yml")
	requireNoProblems(t, "deploy-prod.yml", imageFlagProblems(yml))
	mut := mutateOnce(t, yml, "      - name: Run the deploy-check job\n",
		"      - name: Deploy by tag\n        run: gcloud run deploy x --image \"$REGISTRY/x:$SHA\"\n      - name: Run the deploy-check job\n")
	requireProblemWith(t, "a tag deploy added to check", imageFlagProblems(mut), "check/Deploy by tag")
}

var (
	uploadLines = []string{"uses: actions/upload-artifact@v7", "name: digest-${{ matrix.image }}", "path: digests/",
		"if-no-files-found: error", "overwrite: true", "retention-days: 7"}
	downloadLines = []string{"uses: actions/download-artifact@v4", "pattern: digest-*", "merge-multiple: true", "path: digests"}
)

func digestArtifactProblems(yml string) []string {
	jobs := map[string]workflowJob{}
	for _, j := range workflowJobsOf(yml) {
		jobs[j.name] = j
	}
	var p []string
	need := func(job, marker string, lines []string, noRef bool) {
		got := nightlyStepLines(jobs[job], marker)
		if got == nil {
			p = append(p, job+": no step with "+marker)
			return
		}
		for _, l := range lines {
			if !slices.Contains(got, l) {
				p = append(p, job+": "+marker+" step lacks "+l)
			}
		}
		for _, l := range got {
			if noRef && strings.HasPrefix(l, "ref:") {
				p = append(p, job+": checkout is pinned: "+l)
			}
		}
	}
	need("build", "uses: actions/upload-artifact", uploadLines, false)
	for _, job := range []string{"migrate", "deploy"} {
		need(job, "uses: actions/checkout", []string{"uses: actions/checkout@v4"}, true)
		need(job, "uses: actions/download-artifact", downloadLines, false)
	}
	return p
}

func TestDeployProd_DigestArtifactsFlowFromBuildToDeployers(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "deploy-prod.yml")
	requireNoProblems(t, "deploy-prod.yml", digestArtifactProblems(yml))
	for _, row := range []struct{ job, drop string }{
		{"build", "name: digest-${{ matrix.image }}"}, {"build", "if-no-files-found: error"}, {"build", "overwrite: true"},
		{"migrate", "pattern: digest-*"}, {"migrate", "merge-multiple: true"}, {"migrate", "path: digests"},
		{"deploy", "pattern: digest-*"}, {"deploy", "merge-multiple: true"}, {"deploy", "path: digests"},
	} {
		requireProblemWith(t, "drop "+row.job+" "+row.drop, digestArtifactProblems(editJobLine(t, yml, row.job, row.drop, "")), row.job+": ")
	}
	pinned := editJobLine(t, yml, "migrate", "- uses: actions/checkout@v4",
		"- uses: actions/checkout@v4\n        with:\n          ref: ${{ needs.await-ci.outputs.sha }}")
	requireProblemWith(t, "pin migrate's checkout", digestArtifactProblems(pinned), "migrate: checkout is pinned")
}

func probeProblems(yml string) []string {
	var inv workflowJob
	for _, j := range workflowJobsOf(yml) {
		if j.name == "invariants" {
			inv = j
		}
	}
	steps := inv.steps()
	invariants, probe := -1, -1
	for i, s := range steps {
		if strings.Contains(s.keys["run"], "preview.sh invariants") {
			invariants = i
		}
		if s.keys["name"] == probeStep {
			probe = i
		}
	}
	var p []string
	switch {
	case invariants < 0:
		return []string{"invariants: no preview.sh invariants step"}
	case probe < 0:
		return []string{"invariants: no step " + probeStep}
	case probe < invariants:
		p = append(p, "the probe runs before preview.sh invariants")
	}
	if steps[probe].keys["if"] != "${{ !cancelled() }}" {
		p = append(p, "the probe's if: is "+steps[probe].keys["if"]+", not ${{ !cancelled() }}")
	}
	if !strings.HasSuffix(steps[probe].env["REGISTRY"], "/previews") {
		p = append(p, "the probe's REGISTRY is "+steps[probe].env["REGISTRY"]+", not a previews repository")
	}
	return p
}

// A red invariants step skips later steps unless they say !cancelled(); only a GitHub run shows it.
func TestDevEnv_InvariantsProbeRunsAfterARedInvariant(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "dev-env.yml")
	requireNoProblems(t, "dev-env.yml", probeProblems(yml))
	requireProblemWith(t, "drop the if:", probeProblems(editJobLine(t, yml, "invariants", "if: ${{ !cancelled() }}", "")), "if:")
	requireProblemWith(t, "REGISTRY on images", probeProblems(editJobLine(t, yml, "invariants",
		"REGISTRY: "+previewsRegistry, "REGISTRY: africa-south1-docker.pkg.dev/ascomply-preview/images")), "REGISTRY")
}

const pinStepName = "Keep the serving and rollback images within the cleanup limit"

// pinOrderProblems: build has the pin step, and it comes before Build and push.
func pinOrderProblems(yml string) []string {
	take, pin, push := -1, -1, -1
	for _, j := range workflowJobsOf(yml) {
		if j.name != "build" {
			continue
		}
		for i, s := range j.steps() {
			switch s.keys["name"] {
			case takeScriptStep:
				take = i
			case pinStepName:
				pin = i
			case "Build and push":
				push = i
			}
		}
	}
	switch {
	case pin < 0:
		return []string{"build: no step " + pinStepName}
	case push < 0:
		return []string{"build: no step Build and push"}
	case pin > push:
		return []string{"build: the pin step runs after Build and push"}
	case take < 0 || take > pin:
		return []string{"build: " + takeScriptStep + " does not come before the pin step"}
	}
	return nil
}

func TestDeployProd_BuildPinsBeforeItPushes(t *testing.T) {
	t.Parallel()
	pin := stepOf(t, "deploy-prod.yml", "build", pinStepName)
	b := pinBox(t, 0)
	runner := filepath.Join(b.root, "runner")
	if err := os.MkdirAll(runner, 0o755); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(requireProdImageScript(t))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(runner, "prod-image.sh"), string(script))
	b.env["RUNNER_TEMP"], b.env["IMAGE_NAME"] = runner, "gateway"
	if r := b.step(pin); r.code != 0 {
		t.Fatalf("%s: exit %d stderr %q", pinStepName, r.code, r.stderr)
	}
	if got := b.gcloudCalls("run services describe gateway "); len(got) != 1 {
		t.Errorf("gcloud log %q: want one run services describe gateway", b.lines("gcloud.log"))
	}

	yml := readWorkflow(t, "deploy-prod.yml")
	requireNoProblems(t, "deploy-prod.yml", pinOrderProblems(yml))
	block := "      - name: " + pinStepName + "\n        run: bash \"$RUNNER_TEMP/prod-image.sh\" pin \"$IMAGE_NAME\"\n"
	moved := mutateOnce(t, mutateOnce(t, yml, block, ""),
		"      - uses: actions/upload-artifact@v7\n        with:\n          name: digest-${{ matrix.image }}\n",
		block+"      - uses: actions/upload-artifact@v7\n        with:\n          name: digest-${{ matrix.image }}\n")
	requireProblemWith(t, "pin moved after the push", pinOrderProblems(moved), "after Build and push")

	// The pin step reads $RUNNER_TEMP/prod-image.sh, so the step that writes it comes first.
	takeBlock := "      - name: " + takeScriptStep + "\n        run: |\n          git fetch --no-tags --depth 1 origin \"$GITHUB_SHA\"\n" +
		"          git show \"$GITHUB_SHA:scripts/ci/prod-image.sh\" >\"$RUNNER_TEMP/prod-image.sh\"\n"
	late := mutateOnce(t, mutateOnce(t, yml, takeBlock, ""), "      - name: "+pinStepName+"\n", takeBlock+"      - name: "+pinStepName+"\n")
	requireNoProblems(t, "take moved to just before the pin (still in order)", pinOrderProblems(late))
	after := mutateOnce(t, mutateOnce(t, yml, takeBlock, ""), "      - uses: actions/upload-artifact@v7\n        with:\n          name: digest-${{ matrix.image }}\n", takeBlock+"      - uses: actions/upload-artifact@v7\n        with:\n          name: digest-${{ matrix.image }}\n")
	requireProblemWith(t, "take moved to the end of build", pinOrderProblems(after), "does not come before the pin step")
}

// buildLegProblems: every build leg builds, and no line of the job skips a build, reads a tag or pushes one.
func buildLegProblems(yml string) []string {
	var p []string
	lines := 0
	for _, j := range workflowJobsOf(yml) {
		if j.name != "build" {
			continue
		}
		for _, l := range stripHashComments(j.lines) {
			t := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "- ")))
			if t == "" {
				continue
			}
			lines++
			for _, bad := range []string{"docker push", "docker build", "docker tag", "gcloud artifacts", "steps.exists", "docker buildx imagetools"} {
				if strings.Contains(t, bad) {
					p = append(p, "build: "+strings.TrimSpace(l)+" ("+bad+")")
				}
			}
			if strings.HasPrefix(t, "if:") {
				p = append(p, "build: a step is conditional: "+strings.TrimSpace(l))
			}
		}
	}
	if lines < 40 {
		p = append(p, fmt.Sprintf("build: scanned %d lines, want the whole job", lines))
	}
	return p
}

// A re-added `exists` step or a tag push cannot be observed by a run test, which only runs the steps that exist.
func TestDeployProd_BuildLegsNeverSkipOrReadOrPushATag(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "deploy-prod.yml")
	requireNoProblems(t, "deploy-prod.yml", buildLegProblems(yml))
	add := func(step string) string {
		return mutateOnce(t, yml, "      - uses: actions/upload-artifact@v7\n        with:\n          name: digest-${{ matrix.image }}\n", step+"      - uses: actions/upload-artifact@v7\n        with:\n          name: digest-${{ matrix.image }}\n")
	}
	requireProblemWith(t, "an exists step", buildLegProblems(add("      - id: exists\n        run: gcloud artifacts docker images describe \"$X\"\n")), "gcloud artifacts")
	requireProblemWith(t, "a docker push", buildLegProblems(add("      - run: docker push \"$X\"\n")), "docker push")
	requireProblemWith(t, "a skipped Build and push", buildLegProblems(
		mutateOnce(t, yml, "      - name: Build and push\n", "      - name: Build and push\n        if: steps.exists.outputs.built != 'true'\n")), "conditional")
}

// buildMatrixImages are the images of build's matrix, in file order.
func buildMatrixImages(yml string) []string {
	re := regexp.MustCompile(`^- \{ image: ([a-z-]+),`)
	var out []string
	for _, j := range workflowJobsOf(yml) {
		if j.name != "build" {
			continue
		}
		for _, l := range stripHashComments(j.lines) {
			if m := re.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
				out = append(out, m[1])
			}
		}
	}
	return out
}

// deploy and migrate read digests/<image> for each of these; a missing leg blocks every deploy.
func TestDeployProd_EveryDeployedImageHasExactlyOneBuildLeg(t *testing.T) {
	t.Parallel()
	got := buildMatrixImages(readWorkflow(t, "deploy-prod.yml"))
	want := append(slices.Clone(prodServices), "migrate", "deploycheck")
	slices.Sort(got)
	slices.Sort(want)
	if len(got) == 0 || !slices.Equal(got, want) {
		t.Errorf("build matrix images:\n%v\nwant the 18 services, migrate and deploycheck:\n%v", got, want)
	}
}

const labelStep = "Check the commit label the nightly reads"

// labelBox serves auth's describe JSON, shaped like the real gateway service, with template labels labels.
func labelBox(t *testing.T, labels string) *prodBox {
	t.Helper()
	b := newProdBox(t)
	svc := mutateOnce(t, pinFixture(t, "gateway-service.json"),
		`"run.googleapis.com/startupProbeType": "Default"`, labels)
	b.put("run.services.describe.auth.json", svc)
	b.env["SHA"], b.env["PROJECT"], b.env["REGION"] = strings.Repeat("d", 40), "proj-x", "region-x"
	return b
}

const startProbe = `"run.googleapis.com/startupProbeType": "Default"`

func TestDeployProd_LabelCheckPassesOnTheDeployedCommit(t *testing.T) {
	t.Parallel()
	b := labelBox(t, startProbe+`, "commit": "`+strings.Repeat("d", 40)+`"`)
	r := b.step(stepOf(t, "deploy-prod.yml", "deploy", labelStep))
	if r.code != 0 || !strings.Contains(r.stdout, b.env["SHA"]) || strings.Contains(r.stdout+r.stderr, "::error::") {
		t.Errorf("exit %d stdout %q stderr %q, want exit 0, the sha named and no ::error::", r.code, r.stdout, r.stderr)
	}
}

func TestDeployProd_LabelCheckAnnotatesAMissingLabelWithoutFailing(t *testing.T) {
	t.Parallel()
	step := stepOf(t, "deploy-prod.yml", "deploy", labelStep)
	for _, tc := range []struct {
		name, labels string
		fails        int
	}{
		{"no commit label", startProbe, 0},
		{"another sha", startProbe + `, "commit": "` + strings.Repeat("e", 40) + `"`, 0},
		{"the read fails", startProbe, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := labelBox(t, tc.labels)
			if tc.fails > 0 {
				b.put("fails.run.services.describe.auth", "3")
			}
			r := b.step(step)
			lines := regexp.MustCompile(`(?m)^::error::auth's template carries commit label .*the nightly will report auth=none\.$`).FindAllString(r.stderr, -1)
			if r.code != 0 || len(lines) != 1 {
				t.Errorf("exit %d stderr %q, want exit 0 and one ::error:: line", r.code, r.stderr)
			}
			if tc.fails > 0 && !sameLines(b.sleeps(), []string{"20", "20"}) {
				t.Errorf("sleeps = %v, want [20 20]", b.sleeps())
			}
		})
	}
}

// labelStepProblems: the label check is in deploy, right after the start step, and may not fail the run silently or loudly.
func labelStepProblems(yml string) []string {
	for _, j := range workflowJobsOf(yml) {
		steps := j.steps()
		for i, s := range steps {
			if s.keys["name"] != labelStep {
				continue
			}
			switch {
			case j.name != "deploy":
				return []string{"the label check is in job " + j.name + ", not deploy"}
			case i == 0 || steps[i-1].keys["name"] != startRevsStep:
				return []string{"the label check does not follow " + startRevsStep}
			case s.keys["continue-on-error"] != "":
				return []string{"the label check has continue-on-error"}
			}
			return nil
		}
	}
	return []string{"no step " + labelStep}
}

func TestDeployProd_LabelCheckRunsInDeployAfterTheRevisionsStart(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "deploy-prod.yml")
	requireNoProblems(t, "deploy-prod.yml", labelStepProblems(yml))
	needs := regexp.MustCompile(`(?m)^    needs: \[[^\]]*\bdeploy\b`)
	if !needs.MatchString(yml) {
		t.Error("no job needs deploy")
	}
	start := strings.Index(yml, "      - name: "+labelStep)
	end := strings.Index(yml, "\n  # The new revisions are reachable")
	block := yml[start:end]
	moved := strings.Replace(yml, block, "", 1)
	before := mutateOnce(t, moved, "      - name: "+startRevsStep, block+"      - name: "+startRevsStep)
	requireProblemWith(t, "moved before the start step", labelStepProblems(before), "does not follow")
	toPromote := mutateOnce(t, moved, "      - name: Send 100 % to the new revisions", block+"      - name: Send 100 % to the new revisions")
	requireProblemWith(t, "moved to promote", labelStepProblems(toPromote), "not deploy")
	soft := mutateOnce(t, yml, "      - name: "+labelStep+"\n", "      - name: "+labelStep+"\n        continue-on-error: true\n")
	requireProblemWith(t, "continue-on-error added", labelStepProblems(soft), "continue-on-error")
}
