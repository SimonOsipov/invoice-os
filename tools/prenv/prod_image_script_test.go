// prod_image_script_test.go runs scripts/ci/prod-image.sh against docker, gcloud and sleep PATH shims.
// prodBox is also the harness of deploy_prod_workflow_test.go, which runs workflow steps against the same shims.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const prodRegistry = "registry.test/proj/images"

// The docker shim logs one argv line per call to $D/docker.log. Files in $D steer it: inspect-rc (exit of
// `buildx inspect`, 1 when absent), create-fails (the first N `buildx create` calls fail), build-fails, build-fails-late (fails
// after writing the metadata), meta.json
// (written to the --metadata-file path; no file = no metadata).
const prodDockerShim = `printf '%s\n' "$*" >> "$D/docker.log"
[ "$1" = buildx ] || exit 0
case "$2" in
inspect) exit "$(cat "$D/inspect-rc" 2>/dev/null || echo 1)" ;;
create)
  n=$(cat "$D/create-n" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$D/create-n"
  [ "$n" -gt "$(cat "$D/create-fails" 2>/dev/null || echo 0)" ] || { echo "ERROR: create $n failed" >&2; exit 1; }
  exit 0 ;;
build)
  echo "#1 build noise on stdout"; echo "#1 build noise on stderr" >&2
  [ ! -e "$D/build-fails" ] || { echo "ERROR: build failed" >&2; exit 1; }
  prev=
  for a in "$@"; do
    [ "$prev" = --metadata-file ] && cp "$D/meta.json" "$a" 2>/dev/null
    prev=$a
  done
  [ ! -e "$D/build-fails-late" ] || { echo "ERROR: push failed after the metadata was written" >&2; exit 1; }
  exit 0 ;;
esac
`

// The gcloud shim logs argv to $D/gcloud.log. `run jobs update` prints CALL <argv> on stdout, so a test sees
// the order against the step's own echo. `run <kind> describe <name>` prints $D/run.<kind>.describe.<name>.json,
// failing its first fails.<same key> calls; `tags add` fails when tags-fails exists. `artifacts docker images list` prints $D/list.json; its first
// list-fails calls exit 1.
const prodGcloudShim = `printf '%s\n' "$*" >> "$D/gcloud.log"
case "$1 $2 $3" in
"artifacts docker images")
  n=$(cat "$D/list-n" 2>/dev/null || echo 0); echo $((n + 1)) > "$D/list-n"
  [ "$n" -ge "$(cat "$D/list-fails" 2>/dev/null || echo 0)" ] || { echo "ERROR: list failed" >&2; exit 1; }
  cat "$D/list.json" ;;
"run jobs update") echo "CALL $*" ;;
"run services describe" | "run revisions describe" | "run jobs describe")
  k="$1.$2.$3.$4"; n=$(cat "$D/n.$k" 2>/dev/null || echo 0); echo $((n + 1)) > "$D/n.$k"
  [ "$n" -ge "$(cat "$D/fails.$k" 2>/dev/null || echo 0)" ] || { echo "ERROR: describe failed" >&2; exit 1; }
  cat "$D/$k.json" ;;
"artifacts docker tags") [ ! -e "$D/tags-fails" ] || { echo "ERROR: tags add failed" >&2; exit 1; } ;;
esac
`

// prodScrubbed are the variables a test sets itself; the runner's own copies never leak in.
var prodScrubbed = []string{"D", "PATH", "REGISTRY", "PROJECT", "REGION", "SHA", "TAG", "GITHUB_SHA", "GITHUB_RUN_ID",
	"GITHUB_RUN_ATTEMPT", "RUNNER_TEMP", "IMAGE_NAME", "SERVICE", "SPA", "DOCKERFILE", "HUBSPOT_PORTAL_ID"}

type prodBox struct {
	t    *testing.T
	root string // $D: shim logs and switches
	bin  string
	ws   string // the working directory of every run
	env  map[string]string
}

type prodRun struct {
	stdout, stderr string
	code           int
}

func newProdBox(t *testing.T) *prodBox {
	t.Helper()
	root := t.TempDir()
	b := &prodBox{t: t, root: root, bin: filepath.Join(root, "bin"), ws: filepath.Join(root, "ws"),
		env: map[string]string{"D": root, "REGISTRY": prodRegistry}}
	for _, d := range []string{b.bin, b.ws} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeShim(t, filepath.Join(b.bin, "docker"), prodDockerShim)
	writeShim(t, filepath.Join(b.bin, "gcloud"), prodGcloudShim)
	writeSleepStub(t, b.bin)
	return b
}

func requireProdImageScript(t *testing.T) string {
	t.Helper()
	p := filepath.Join(repoRoot(t), "scripts", "ci", "prod-image.sh")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("scripts/ci/prod-image.sh missing: %v", err)
	}
	return p
}

// installScript copies the tree's prod-image.sh to scripts/ci/ in the workspace, where a deploy job has it checked out.
func (b *prodBox) installScript() {
	b.t.Helper()
	raw, err := os.ReadFile(requireProdImageScript(b.t))
	if err != nil {
		b.t.Fatal(err)
	}
	dst := filepath.Join(b.ws, "scripts", "ci", "prod-image.sh")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		b.t.Fatal(err)
	}
	writeFile(b.t, dst, string(raw))
}

func (b *prodBox) put(name, body string) {
	b.t.Helper()
	writeFile(b.t, filepath.Join(b.root, name), body)
}

func (b *prodBox) meta(digest, name string) {
	b.t.Helper()
	b.put("meta.json", fmt.Sprintf(`{"containerimage.digest":%q,"image.name":%q}`, digest, name))
}

// digestFile writes <ws>/digests/<image> with body, creating digests/.
func (b *prodBox) digestFile(image, body string) {
	b.t.Helper()
	dir := filepath.Join(b.ws, "digests")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.t.Fatal(err)
	}
	writeFile(b.t, filepath.Join(dir, image), body)
}

func (b *prodBox) environ() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if _, mine := b.env[k]; mine || containsStr(prodScrubbed, k) {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range b.env {
		out = append(out, k+"="+v)
	}
	return append(out, "PATH="+b.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (b *prodBox) exec(argv ...string) prodRun {
	b.t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = b.ws
	cmd.Env = b.environ()
	stdout, stderr, err := runToFiles(b.t, cmd, false)
	r := prodRun{stdout: stdout, stderr: stderr}
	if ee, ok := err.(*exec.ExitError); ok {
		r.code = ee.ExitCode()
	} else if err != nil {
		b.t.Fatal(err)
	}
	return r
}

// script runs the tree's prod-image.sh in the workspace.
func (b *prodBox) script(args ...string) prodRun {
	b.t.Helper()
	return b.exec(append([]string{"bash", requireProdImageScript(b.t)}, args...)...)
}

// step runs a workflow step's run: text the way GitHub's default shell does (bash -e).
func (b *prodBox) step(s workflowStep) prodRun {
	b.t.Helper()
	run := s.keys["run"]
	if run == "" || strings.Contains(run, "${{") {
		b.t.Fatalf("step %q: run: is empty or holds a ${{ }} expression; the run tests need plain bash reading env:", s.keys["name"])
	}
	return b.exec("bash", "-e", "-c", run, "bash")
}

func (b *prodBox) lines(name string) []string {
	b.t.Helper()
	raw, err := os.ReadFile(filepath.Join(b.root, name))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		b.t.Fatal(err)
	}
	return nonBlankLines(string(raw))
}

func (b *prodBox) wsFile(rel string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(b.ws, rel))
	return string(raw), err == nil
}

var metaFlag = regexp.MustCompile(`--metadata-file \S+`)

// dockerArgv is the docker log with the temporary metadata path replaced by META.
func (b *prodBox) dockerArgv() []string {
	b.t.Helper()
	var out []string
	for _, l := range b.lines("docker.log") {
		out = append(out, metaFlag.ReplaceAllString(l, "--metadata-file META"))
	}
	return out
}

func hex64(c string) string { return strings.Repeat(c, 64) }

func sameLines(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

const (
	prodInspect = "buildx inspect prod-image"
	prodCreate  = "buildx create --name prod-image --driver docker-container --driver-opt image=mirror.gcr.io/moby/buildkit:buildx-stable-1 --bootstrap"
)

func TestProdImagePushRecordsTheDigestItPushed(t *testing.T) {
	t.Parallel()
	requireProdImageScript(t)
	digest := "sha256:" + hex64("a")

	b := newProdBox(t)
	b.meta(digest, prodRegistry+"/gateway")
	r := b.script("push", "digests", "gateway", "-f", "Dockerfile", "--build-arg", "X=1", ".")
	if r.code != 0 || r.stdout != digest+"\n" {
		t.Fatalf("push: exit %d stdout %q stderr %q, want exit 0 stdout %q", r.code, r.stdout, r.stderr, digest+"\n")
	}
	if got, ok := b.wsFile("digests/gateway"); !ok || got != digest+"\n" {
		t.Errorf("digests/gateway = %q (exists %v), want %q", got, ok, digest+"\n")
	}
	if ents, _ := os.ReadDir(filepath.Join(b.ws, "digests")); len(ents) != 1 {
		t.Errorf("digests/ holds %d entries, want only gateway (the temporary file is moved)", len(ents))
	}
	want := []string{prodInspect, prodCreate,
		"buildx build --builder prod-image --provenance=false --sbom=false --output type=image,name=" + prodRegistry +
			"/gateway,push-by-digest=true,push=true --metadata-file META -f Dockerfile --build-arg X=1 ."}
	argv := b.dockerArgv()
	if !sameLines(argv, want) {
		t.Errorf("docker argv:\n%s\nwant:\n%s", strings.Join(argv, "\n"), strings.Join(want, "\n"))
	}
	for _, l := range argv {
		if w := strings.Fields(l)[0]; w == "push" || w == "tag" || strings.Contains(l, prodRegistry+"/gateway:") || strings.Contains(l, " --tag ") {
			t.Errorf("docker call %q pushes or names a tag", l)
		}
	}
	if g := b.lines("gcloud.log"); len(g) != 0 {
		t.Errorf("push called gcloud: %v", g)
	}

	// An existing builder is reused.
	b2 := newProdBox(t)
	b2.put("inspect-rc", "0")
	b2.meta(digest, prodRegistry+"/gateway")
	r = b2.script("push", "digests", "gateway", ".")
	if r.code != 0 {
		t.Fatalf("push with the builder present: exit %d stderr %q", r.code, r.stderr)
	}
	for _, l := range b2.dockerArgv() {
		if strings.HasPrefix(l, "buildx create") {
			t.Errorf("created a builder that exists: %q", l)
		}
	}
	if len(b2.dockerArgv()) != 2 {
		t.Errorf("docker argv with the builder present: %v, want inspect and build only", b2.dockerArgv())
	}
}

func TestProdImagePushFailsWithoutItsDigest(t *testing.T) {
	t.Parallel()
	requireProdImageScript(t)
	good := "sha256:" + hex64("a")
	for _, tc := range []struct {
		name  string
		setup func(b *prodBox)
	}{
		{"builder cannot be created", func(b *prodBox) { b.put("create-fails", "99"); b.meta(good, prodRegistry+"/gateway") }},
		{"build fails", func(b *prodBox) { b.put("build-fails", "1"); b.meta(good, prodRegistry+"/gateway") }},
		{"build fails after writing its metadata", func(b *prodBox) { b.put("build-fails-late", "1"); b.meta(good, prodRegistry+"/gateway") }},
		{"no metadata written", func(b *prodBox) {}},
		{"metadata without a digest", func(b *prodBox) { b.put("meta.json", `{"image.name":"`+prodRegistry+`/gateway"}`) }},
		{"63 hex", func(b *prodBox) { b.meta("sha256:"+strings.Repeat("a", 63), prodRegistry+"/gateway") }},
		{"upper-case hex", func(b *prodBox) { b.meta("sha256:"+hex64("A"), prodRegistry+"/gateway") }},
		{"another image's name", func(b *prodBox) { b.meta(good, prodRegistry+"/auth") }},
		{"unparseable JSON", func(b *prodBox) { b.put("meta.json", "not json {") }},
		{"null digest", func(b *prodBox) {
			b.put("meta.json", `{"containerimage.digest":null,"image.name":"`+prodRegistry+`/gateway"}`)
		}},
		{"empty digest", func(b *prodBox) { b.meta("", prodRegistry+"/gateway") }},
		{"sha512 digest", func(b *prodBox) { b.meta("sha512:"+hex64("a"), prodRegistry+"/gateway") }},
		{"digest with a prefix", func(b *prodBox) { b.meta("x"+good, prodRegistry+"/gateway") }},
		{"digest with a second line", func(b *prodBox) { b.meta(good+"\nsha256:"+hex64("b"), prodRegistry+"/gateway") }},
		{"no image name", func(b *prodBox) { b.put("meta.json", `{"containerimage.digest":"`+good+`"}`) }},
		{"a tagged image name", func(b *prodBox) { b.meta(good, prodRegistry+"/gateway:latest") }},
		{"an image name that extends ours", func(b *prodBox) { b.meta(good, prodRegistry+"/gateway-x") }},
		{"another registry", func(b *prodBox) { b.meta(good, "other.test/proj/images/gateway") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newProdBox(t)
			tc.setup(b)
			r := b.script("push", "digests", "gateway", ".")
			if r.code == 0 || r.stdout != "" || errorLines(r.stderr) == "" {
				t.Errorf("exit %d stdout %q stderr %q, want a failure with empty stdout and ::error:: on stderr", r.code, r.stdout, r.stderr)
			}
			if got, ok := b.wsFile("digests/gateway"); ok {
				t.Errorf("recorded digests/gateway = %q after a failed push", got)
			}
		})
	}

	// Control: the same call with valid metadata records a digest, so the refusals above are not blanket.
	b := newProdBox(t)
	b.meta(good, prodRegistry+"/gateway")
	if r := b.script("push", "digests", "gateway", "."); r.code != 0 {
		t.Fatalf("control push: exit %d stderr %q", r.code, r.stderr)
	}
	if _, ok := b.wsFile("digests/gateway"); !ok {
		t.Error("control push recorded no digest")
	}
}

func TestProdImageRefusesABadImageNameOrMissingEnv(t *testing.T) {
	t.Parallel()
	requireProdImageScript(t)
	digest := "sha256:" + hex64("c")
	calls := func(image string) map[string][]string {
		return map[string][]string{
			"push": {"push", "digests", image, "."},
			"ref":  {"ref", "digests", image},
			"refs": {"refs", "digests", image},
		}
	}
	prepare := func() *prodBox {
		b := newProdBox(t)
		b.meta("sha256:"+hex64("a"), prodRegistry+"/gateway")
		b.digestFile("gateway", digest+"\n")
		return b
	}
	refused := func(t *testing.T, b *prodBox, r prodRun, what string) {
		t.Helper()
		if r.code == 0 || errorLines(r.stderr) == "" {
			t.Errorf("%s: exit %d stderr %q, want a refusal with ::error::", what, r.code, r.stderr)
		}
		if d, g := b.lines("docker.log"), b.lines("gcloud.log"); len(d)+len(g) != 0 {
			t.Errorf("%s: called docker %v or gcloud %v before refusing", what, d, g)
		}
	}
	for _, image := range []string{"../x", "Gateway", "", "gate way", "-x", "9x", "gate/way", "gateway\nx", "gateway\n", "gate_way", "../digests/gateway", "digests/gateway"} {
		for sub, args := range calls(image) {
			b := prepare()
			refused(t, b, b.script(args...), fmt.Sprintf("%s %q", sub, image))
		}
	}
	b := prepare()
	refused(t, b, b.script("refs", "digests", "gateway", "../x"), `refs gateway ../x`)
	for sub, args := range calls("gateway") {
		b := prepare()
		delete(b.env, "REGISTRY")
		refused(t, b, b.script(args...), sub+" without REGISTRY")
	}

	// Control: valid names with REGISTRY run.
	b = prepare()
	if r := b.script("ref", "digests", "gateway"); r.code != 0 {
		t.Errorf("control ref: exit %d stderr %q", r.code, r.stderr)
	}
	if r := b.script("push", "digests", "gateway", "."); r.code != 0 || len(b.lines("docker.log")) == 0 {
		t.Errorf("control push: exit %d stderr %q docker calls %v", r.code, r.stderr, b.lines("docker.log"))
	}
}

func TestProdImageRefPrintsTheDigestReference(t *testing.T) {
	t.Parallel()
	requireProdImageScript(t)
	b := newProdBox(t)
	digest := "sha256:" + hex64("c")
	b.digestFile("gateway", digest+"\n")
	r := b.script("ref", "digests", "gateway")
	want := prodRegistry + "/gateway@" + digest
	if r.code != 0 || strings.TrimSuffix(r.stdout, "\n") != want || strings.Contains(strings.TrimSuffix(r.stdout, "\n"), "\n") {
		t.Errorf("ref: exit %d stdout %q stderr %q, want %q", r.code, r.stdout, r.stderr, want)
	}
	if d, g := b.lines("docker.log"), b.lines("gcloud.log"); len(d)+len(g) != 0 {
		t.Errorf("ref called docker %v or gcloud %v; it reads the file only", d, g)
	}
}

func TestProdImageRefRefusesAMissingOrMalformedDigest(t *testing.T) {
	t.Parallel()
	requireProdImageScript(t)
	d := "sha256:" + hex64("c")
	for _, tc := range []struct{ name, body string }{
		{"missing", "\x00missing"},
		{"empty", ""},
		{"63 hex", "sha256:" + strings.Repeat("c", 63) + "\n"},
		{"upper-case hex", "sha256:" + hex64("C") + "\n"},
		{"two lines", d + "\n" + d + "\n"},
		{"a tag reference", prodRegistry + "/gateway:" + strings.Repeat("c", 40) + "\n"},
		{"no final newline", d},
		{"CRLF", d + "\r\n"},
		{"trailing space", d + " \n"},
		{"sha512 prefix", "sha512:" + hex64("c") + "\n"},
		{"72 bytes, one non-hex", "sha256:" + strings.Repeat("c", 63) + "g\n"},
		{"a blank first line", "\n" + d},
		{"a trailing NUL byte", d + "\n\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newProdBox(t)
			if tc.body != "\x00missing" {
				b.digestFile("gateway", tc.body)
			}
			r := b.script("ref", "digests", "gateway")
			if r.code == 0 || r.stdout != "" || !strings.Contains(r.stderr, "gateway") {
				t.Errorf("exit %d stdout %q stderr %q, want a failure with empty stdout and gateway named", r.code, r.stdout, r.stderr)
			}
			if tc.body == "\x00missing" && !strings.Contains(r.stderr, "no digest recorded") {
				t.Errorf("stderr %q does not say that no digest was recorded", r.stderr)
			}
		})
	}
	b := newProdBox(t)
	b.digestFile("gateway", d+"\n")
	if r := b.script("ref", "digests", "gateway"); r.code != 0 {
		t.Errorf("control ref: exit %d stderr %q", r.code, r.stderr)
	}
}

func TestProdImageRefsIsAllOrNothing(t *testing.T) {
	t.Parallel()
	requireProdImageScript(t)
	b := newProdBox(t)
	b.digestFile("gateway", "sha256:"+hex64("1")+"\n")
	b.digestFile("auth", "sha256:"+hex64("2")+"\n")
	r := b.script("refs", "digests", "gateway", "auth", "app")
	if r.code == 0 || r.stdout != "" || !regexp.MustCompile(`\bapp\b`).MatchString(r.stderr) {
		t.Errorf("refs with app missing: exit %d stdout %q stderr %q, want a failure, empty stdout, app named", r.code, r.stdout, r.stderr)
	}

	b.digestFile("app", "sha256:"+hex64("3")+"\n")
	r = b.script("refs", "digests", "gateway", "auth", "app")
	var want string
	for _, e := range []struct{ svc, d string }{{"gateway", "1"}, {"auth", "2"}, {"app", "3"}} {
		want += fmt.Sprintf("%s %s/%s@sha256:%s\n", e.svc, prodRegistry, e.svc, hex64(e.d))
	}
	if r.code != 0 || r.stdout != want {
		t.Errorf("refs: exit %d stdout %q stderr %q, want %q", r.code, r.stdout, r.stderr, want)
	}
}

func TestProdImagePushRetriesTheBuilderBootstrap(t *testing.T) {
	t.Parallel()
	requireProdImageScript(t)
	digest := "sha256:" + hex64("a")

	b := newProdBox(t)
	b.put("create-fails", "1")
	b.meta(digest, prodRegistry+"/gateway")
	r := b.script("push", "digests", "gateway", ".")
	if r.code != 0 || r.stdout != digest+"\n" {
		t.Fatalf("push after one failed create: exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}
	argv := b.dockerArgv()
	if len(argv) < 5 || !sameLines(argv[:4], []string{prodInspect, prodCreate, "buildx rm prod-image", prodCreate}) || !strings.HasPrefix(argv[4], "buildx build ") {
		t.Errorf("docker argv:\n%s\nwant inspect, create, rm, create, build", strings.Join(argv, "\n"))
	}
	if s := sleepsIn(t, b.bin); !sameLines(s, []string{"20"}) {
		t.Errorf("sleeps = %v, want [20]", s)
	}
	if _, ok := b.wsFile("digests/gateway"); !ok {
		t.Error("no digest recorded after the retry succeeded")
	}

	b = newProdBox(t)
	b.put("create-fails", "3")
	b.meta(digest, prodRegistry+"/gateway")
	r = b.script("push", "digests", "gateway", ".")
	if r.code == 0 || r.stdout != "" || errorLines(r.stderr) == "" {
		t.Errorf("push with a builder that never starts: exit %d stdout %q stderr %q, want ::error:: and a failure", r.code, r.stdout, r.stderr)
	}
	want := []string{prodInspect, prodCreate, "buildx rm prod-image", prodCreate, "buildx rm prod-image", prodCreate}
	if got := b.dockerArgv(); !sameLines(got, want) {
		t.Errorf("docker argv:\n%s\nwant three creates, a rm before each retry, and no build", strings.Join(got, "\n"))
	}
	if s := sleepsIn(t, b.bin); !sameLines(s, []string{"20", "20"}) {
		t.Errorf("sleeps = %v, want [20 20]", s)
	}
	if got, ok := b.wsFile("digests/gateway"); ok {
		t.Errorf("recorded digests/gateway = %q after the builder failed", got)
	}
}

const (
	pinRegistry = "africa-south1-docker.pkg.dev/ascomply-preview/images"
	pinServing  = "sha256:7299e2ef3e04adb98148340fd26274086754c64433132284b6868fd6068d6179"
	pinRollback = "sha256:badb4dd2b82735e97c055975259c271988394829a8e71f2facac0ea843239f0c"
	svcKey      = "run.services.describe.gateway"
	revServing  = "run.revisions.describe.gateway-00383-biy"
	revRollback = "run.revisions.describe.gateway-00380-mac"
)

func pinFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "prod-image", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// pinListing is the real gateway listing with synth untagged rows prepended, all newer than every real row;
// edit may change the rows (before the prepend) as maps.
func pinListing(t *testing.T, synth int, edit func(rows []map[string]any)) string {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(pinFixture(t, "gateway-images-list.json")), &rows); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(rows)
	}
	var out []map[string]any
	for i := 0; i < synth; i++ {
		ts := fmt.Sprintf("2026-12-01T00:00:%02d.000000Z", i)
		out = append(out, map[string]any{"version": fmt.Sprintf("sha256:%064x", i+1), "tags": []string{}, "createTime": ts, "updateTime": ts})
	}
	raw, err := json.Marshal(append(out, rows...))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func rowOf(t *testing.T, rows []map[string]any, digest string) map[string]any {
	t.Helper()
	for _, r := range rows {
		if r["version"] == digest {
			return r
		}
	}
	t.Fatalf("fixture has no row %s", digest)
	return nil
}

// pinBox serves the real gateway captures and a listing with synth newer rows.
func pinBox(t *testing.T, synth int) *prodBox {
	t.Helper()
	b := newProdBox(t)
	b.env["REGISTRY"], b.env["PROJECT"], b.env["REGION"] = pinRegistry, "ascomply-prod", "africa-south1"
	b.put(svcKey+".json", pinFixture(t, "gateway-service.json"))
	b.put(revServing+".json", pinFixture(t, "revision-gateway-00383-biy.json"))
	b.put(revRollback+".json", pinFixture(t, "revision-gateway-00380-mac.json"))
	b.put("list.json", pinListing(t, synth, nil))
	return b
}

func (b *prodBox) gcloudCalls(prefix string) []string {
	var out []string
	for _, l := range b.lines("gcloud.log") {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

// sleeps are the arguments of the sleep calls; the stub logs into bin/.
func (b *prodBox) sleeps() []string {
	raw, _ := os.ReadFile(filepath.Join(b.bin, "sleep.log"))
	return nonBlankLines(string(raw))
}

func tagCmd(digest string) string {
	return "artifacts docker tags add " + pinRegistry + "/gateway@" + digest + " " + pinRegistry + "/gateway:keep-" + strings.TrimPrefix(digest, "sha256:")
}

func requireTagged(t *testing.T, b *prodBox, want ...string) {
	t.Helper()
	got := b.gcloudCalls("artifacts docker tags")
	if len(got) != len(want) {
		t.Fatalf("tags add calls = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tags add call %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestProdImagePinLeavesTodaysProductionAlone(t *testing.T) {
	t.Parallel()
	b := pinBox(t, 0)
	r := b.script("pin", "gateway")
	if r.code != 0 {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	want := []string{
		"run services describe gateway --project ascomply-prod --region africa-south1 --format json",
		"run revisions describe gateway-00380-mac --project ascomply-prod --region africa-south1 --format json",
		"run revisions describe gateway-00383-biy --project ascomply-prod --region africa-south1 --format json",
		"artifacts docker images list " + pinRegistry + "/gateway --include-tags --format json",
	}
	got := b.lines("gcloud.log")
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("gcloud calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestProdImagePinTagsAProtectedImageAtTheMargin(t *testing.T) {
	t.Parallel()
	b := pinBox(t, 35)
	r := b.script("pin", "gateway")
	if r.code != 0 {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	requireTagged(t, b, tagCmd(pinRollback))
	if !strings.Contains(r.stdout, "kept gateway@"+pinRollback+": rank 40, pinned at 40 of 50") {
		t.Errorf("stdout %q has no kept line for the rollback digest at rank 40", r.stdout)
	}
}

func TestProdImagePinKeepsTheMarginBoundary(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		synth int
		want  []string
	}{{34, nil}, {36, []string{tagCmd(pinRollback)}}} {
		b := pinBox(t, c.synth)
		if r := b.script("pin", "gateway"); r.code != 0 {
			t.Fatalf("%d synthetic rows: exit %d stderr %q", c.synth, r.code, r.stderr)
		}
		requireTagged(t, b, c.want...)
	}
}

func TestProdImagePinCountsAVersionWhoseUpdateTimeMoved(t *testing.T) {
	t.Parallel()
	b := pinBox(t, 34)
	b.put("list.json", pinListing(t, 34, func(rows []map[string]any) {
		cut := rowOf(t, rows, pinRollback)["createTime"].(string)
		for _, r := range rows {
			if tags, _ := r["tags"].([]any); len(tags) > 0 && r["createTime"].(string) < cut {
				r["updateTime"] = "2027-01-01T00:00:00.000000Z"
				return
			}
		}
		t.Fatal("no tagged row older than the rollback digest")
	}))
	if r := b.script("pin", "gateway"); r.code != 0 {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	requireTagged(t, b, tagCmd(pinRollback))
}

func TestProdImagePinRanksByTheLargerOfBothOrders(t *testing.T) {
	t.Parallel()
	// The rollback digest is at rank 39 by createTime and, with its updateTime moved ahead of every row, at rank 1 by updateTime.
	// One more newer row makes it rank 40 by createTime: the larger order decides.
	for _, c := range []struct {
		synth int
		want  []string
	}{{34, nil}, {35, []string{tagCmd(pinRollback)}}} {
		b := pinBox(t, c.synth)
		b.put("list.json", pinListing(t, c.synth, func(rows []map[string]any) {
			rowOf(t, rows, pinRollback)["updateTime"] = "2027-01-01T00:00:00.000000Z"
		}))
		if r := b.script("pin", "gateway"); r.code != 0 {
			t.Fatalf("%d synthetic rows: exit %d stderr %q", c.synth, r.code, r.stderr)
		}
		requireTagged(t, b, c.want...)
	}
}

func TestProdImagePinReadsTheJobsForMigrateAndDeploycheck(t *testing.T) {
	t.Parallel()
	const m = pinRollback
	b := pinBox(t, 35) // the digest m is at rank 40
	job := strings.Replace(pinFixture(t, "migrate-job.json"), "migrate:9ad5fad6fe39dd9859a095811ce529a875e3f7e0", "migrate@"+m, 1)
	b.put("run.jobs.describe.migrate.json", job)
	b.put("run.jobs.describe.migrate-bootstrap.json", job)
	b.put("run.jobs.describe.deploy-check.json", job)
	if r := b.script("pin", "migrate"); r.code != 0 {
		t.Fatalf("migrate: exit %d stderr %q", r.code, r.stderr)
	}
	if got := b.gcloudCalls("run "); len(got) != 2 || !strings.HasPrefix(got[0], "run jobs describe migrate ") || !strings.HasPrefix(got[1], "run jobs describe migrate-bootstrap ") {
		t.Errorf("migrate: run calls = %q, want jobs describe migrate and migrate-bootstrap", got)
	}
	requireTagged(t, b, strings.ReplaceAll(tagCmd(m), "/gateway", "/migrate"))

	d := pinBox(t, 35)
	d.put("run.jobs.describe.deploy-check.json", job)
	if r := d.script("pin", "deploycheck"); r.code != 0 {
		t.Fatalf("deploycheck: exit %d stderr %q", r.code, r.stderr)
	}
	if got := d.gcloudCalls("run "); len(got) != 1 || !strings.HasPrefix(got[0], "run jobs describe deploy-check ") {
		t.Errorf("deploycheck: run calls = %q, want jobs describe deploy-check only", got)
	}

	u := pinBox(t, 35)
	for _, j := range []string{"migrate", "migrate-bootstrap"} {
		u.put("run.jobs.describe."+j+".json", pinFixture(t, "migrate-job.json"))
	}
	if r := u.script("pin", "migrate"); r.code != 0 {
		t.Fatalf("tag reference: exit %d stderr %q", r.code, r.stderr)
	}
	requireTagged(t, u)
}

func TestProdImagePinSkipsTaggedAndForeignReferences(t *testing.T) {
	t.Parallel()
	b := pinBox(t, 40) // the rollback digest is at rank 45
	b.put("list.json", pinListing(t, 40, func(rows []map[string]any) {
		rowOf(t, rows, pinRollback)["tags"] = []string{"5b49b0e1c0ffee"}
	}))
	b.put(svcKey+".json", strings.Replace(pinFixture(t, "gateway-service.json"),
		pinRegistry+"/gateway:9ad5fad6fe39dd9859a095811ce529a875e3f7e0", "us-docker.pkg.dev/cloudrun/container/hello", 1))
	b.put(revServing+".json", strings.ReplaceAll(pinFixture(t, "revision-gateway-00383-biy.json"),
		"gateway@"+pinServing, "gateway:"+strings.Repeat("a", 40)))
	r := b.script("pin", "gateway")
	if r.code != 0 {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	requireTagged(t, b)
}

func TestProdImagePinRetriesAFailedRead(t *testing.T) {
	t.Parallel()
	for _, key := range []string{svcKey, revRollback, "list"} {
		b := pinBox(t, 35)
		b.put("fails."+key, "1")
		if key == "list" {
			b.put("list-fails", "1")
		}
		if r := b.script("pin", "gateway"); r.code != 0 {
			t.Fatalf("%s: exit %d stderr %q", key, r.code, r.stderr)
		}
		requireTagged(t, b, tagCmd(pinRollback))
		prefix := strings.Join(strings.SplitN(key, ".", 4), " ")
		if key == "list" {
			prefix = "artifacts docker images list"
		}
		if n := len(b.gcloudCalls(prefix)); n != 2 {
			t.Errorf("%s: read %d times, want 2", key, n)
		}
		if got := b.sleeps(); len(got) != 1 || got[0] != "20" {
			t.Errorf("%s: sleep log %q, want one 20", key, got)
		}
	}
}

func TestProdImagePinGoesOnWhenAReadKeepsFailing(t *testing.T) {
	t.Parallel()
	for _, key := range []string{svcKey, revServing, "list"} {
		b := pinBox(t, 35)
		b.put("fails."+key, "99")
		prefix := strings.Join(strings.SplitN(key, ".", 4), " ")
		if key == "list" {
			b.put("list-fails", "99")
			prefix = "artifacts docker images list"
		}
		r := b.script("pin", "gateway")
		if r.code != 0 {
			t.Fatalf("%s: exit %d stderr %q", key, r.code, r.stderr)
		}
		if n := len(b.gcloudCalls(prefix)); n != 3 {
			t.Errorf("%s: read %d times, want 3", key, n)
		}
		if got := b.sleeps(); len(got) != 2 {
			t.Errorf("%s: sleep log %q, want two", key, got)
		}
		if n := strings.Count(r.stderr, "::error::pin gateway:"); n != 1 || !strings.Contains(r.stderr, prefix) || !strings.Contains(r.stderr, "the build goes on") {
			t.Errorf("%s: stderr %q, want one ::error::pin gateway: naming %q and 'the build goes on'", key, r.stderr, prefix)
		}
		requireTagged(t, b)
	}
}

// A read that exits 0 but prints no usable JSON fails open like a read that keeps failing.
func TestProdImagePinGoesOnWhenAReadPrintsNoJSON(t *testing.T) {
	t.Parallel()
	reads := []struct{ name, key, prefix string }{
		{"service", svcKey, "run services describe"},
		{"revision", revServing, "run revisions describe"},
		{"listing", "list", "artifacts docker images list"},
	}
	outputs := map[string]string{
		"not json":       "not json\n",
		"null":           "null\n",
		"warning, brace": "WARNING: x\n{",
		"warning, JSON":  "WARNING: x\n[]\n",
		"number":         "7\n",
	}
	for _, rd := range reads {
		for name, out := range outputs {
			if rd.key != "list" && name == "warning, JSON" {
				continue
			}
			b := pinBox(t, 35)
			b.put(rd.key+".json", out)
			r := b.script("pin", "gateway")
			if r.code != 0 {
				t.Errorf("%s/%s: exit %d stderr %q", rd.name, name, r.code, r.stderr)
				continue
			}
			if n := strings.Count(r.stderr, "::error::pin gateway:"); n != 1 || !strings.Contains(r.stderr, rd.prefix) || !strings.Contains(r.stderr, "the build goes on") {
				t.Errorf("%s/%s: stderr %q, want one ::error::pin gateway: naming %q and 'the build goes on'", rd.name, name, r.stderr, rd.prefix)
			}
			requireTagged(t, b)
		}
	}
}

func TestProdImagePinGoesOnWhenTaggingFails(t *testing.T) {
	t.Parallel()
	b := pinBox(t, 35)
	b.put("tags-fails", "1")
	r := b.script("pin", "gateway")
	if r.code != 0 {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	requireTagged(t, b, tagCmd(pinRollback))
	for _, w := range []string{"::error::pin gateway:", pinRollback, "rank 40", "gcloud " + tagCmd(pinRollback)} {
		if !strings.Contains(r.stderr, w) {
			t.Errorf("stderr %q lacks %q", r.stderr, w)
		}
	}
	if strings.Contains(r.stdout, "kept ") {
		t.Errorf("stdout %q has a kept line", r.stdout)
	}
}

func TestProdImagePinStopsTheLegWhenTheLastPinFails(t *testing.T) {
	t.Parallel()
	b := pinBox(t, 44) // rank 49
	b.put("tags-fails", "1")
	r := b.script("pin", "gateway")
	if r.code != 1 {
		t.Fatalf("rank 49: exit %d stderr %q, want 1", r.code, r.stderr)
	}
	// the serving digest, at rank 47, fails open first; the rollback digest at 49 stops the leg
	requireTagged(t, b, tagCmd(pinServing), tagCmd(pinRollback))
	for _, w := range []string{"::error::pin gateway:", pinRollback, "rank 49", "would be deleted by this push; the leg stops"} {
		if !strings.Contains(r.stderr, w) {
			t.Errorf("stderr %q lacks %q", r.stderr, w)
		}
	}

	f := pinBox(t, 43) // rank 48
	f.put("tags-fails", "1")
	r = f.script("pin", "gateway")
	if r.code != 0 || !strings.Contains(r.stderr, "rank 48 failed; the build goes on") {
		t.Errorf("rank 48: exit %d stderr %q, want 0 and the fail-open error", r.code, r.stderr)
	}
}

func TestProdImagePinWarnsWhenAProtectedImageIsGone(t *testing.T) {
	t.Parallel()
	b := pinBox(t, 35)
	var rows []map[string]any // drop the row from the JSON, as the listing no longer has it
	if err := json.Unmarshal([]byte(pinListing(t, 35, nil)), &rows); err != nil {
		t.Fatal(err)
	}
	kept := rows[:0]
	for _, r := range rows {
		if r["version"] != pinRollback {
			kept = append(kept, r)
		}
	}
	raw, _ := json.Marshal(kept)
	b.put("list.json", string(raw))
	r := b.script("pin", "gateway")
	if r.code != 0 {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "::warning::gateway@"+pinRollback+" is not in the registry") {
		t.Errorf("stderr %q has no warning naming the rollback digest", r.stderr)
	}
	for _, l := range nonBlankLines(r.stderr) {
		if !strings.HasPrefix(l, "::warning::") {
			t.Errorf("stderr line %q is not a warning; a missing digest ends in the warning alone", l)
		}
	}
	requireTagged(t, b)
}

func TestProdImagePinRefusesBadInput(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, unset, image string }{
		{"bad image", "", "../x"}, {"no REGISTRY", "REGISTRY", "gateway"}, {"no PROJECT", "PROJECT", "gateway"}, {"no REGION", "REGION", "gateway"},
	} {
		b := pinBox(t, 0)
		delete(b.env, c.unset)
		r := b.script("pin", c.image)
		if r.code != 1 || !strings.Contains(r.stderr, "::error::") {
			t.Errorf("%s: exit %d stderr %q, want 1 and ::error::", c.name, r.code, r.stderr)
		}
		if got := b.lines("gcloud.log"); len(got) != 0 {
			t.Errorf("%s: gcloud calls %q, want none", c.name, got)
		}
	}
}

// editJSON decodes raw, lets edit change it, and encodes it again.
func editJSON(t *testing.T, raw string, edit func(m map[string]any)) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	edit(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// addTraffic appends a traffic entry to both spec.traffic and status.traffic of a service describe.
func addTraffic(t *testing.T, raw string, entry map[string]any) string {
	t.Helper()
	return editJSON(t, raw, func(m map[string]any) {
		for _, part := range []string{"spec", "status"} {
			p := m[part].(map[string]any)
			p["traffic"] = append(p["traffic"].([]any), entry)
		}
	})
}

func TestProdImagePinStopsAtEveryRankFromTheLastStopUp(t *testing.T) {
	t.Parallel()
	// rank is the first digest at 49 or more: the serving digest sits two ranks below the rollback one, and fails open below 49.
	for _, c := range []struct{ synth, rank int }{{44, 49}, {45, 50}, {55, 58}} {
		b := pinBox(t, c.synth)
		b.put("tags-fails", "1")
		r := b.script("pin", "gateway")
		if r.code != 1 || !strings.Contains(r.stderr, fmt.Sprintf("rank %d failed; it would be deleted by this push; the leg stops", c.rank)) {
			t.Errorf("rank %d: exit %d stderr %q, want exit 1 and the leg-stops error", c.rank, r.code, r.stderr)
		}
	}
	// A pin that works at those ranks never stops the leg.
	b := pinBox(t, 55)
	r := b.script("pin", "gateway")
	if r.code != 0 || strings.Contains(r.stderr, "::error::") {
		t.Errorf("rank 60 with tags add working: exit %d stderr %q, want exit 0 and no error", r.code, r.stderr)
	}
	requireTagged(t, b, tagCmd(pinServing), tagCmd(pinRollback))
	if !strings.Contains(r.stdout, "kept gateway@"+pinRollback+": rank 60, pinned at 40 of 50") {
		t.Errorf("stdout %q has no kept line for rank 60", r.stdout)
	}
}

func TestProdImagePinTagsEachProtectedDigestOnce(t *testing.T) {
	t.Parallel()
	b := pinBox(t, 35) // the rollback digest is at rank 40, the serving one at 38
	// The template, the rollback revision and the rollback traffic entry all name the rollback digest.
	b.put(svcKey+".json", strings.Replace(pinFixture(t, "gateway-service.json"),
		pinRegistry+"/gateway:9ad5fad6fe39dd9859a095811ce529a875e3f7e0", pinRegistry+"/gateway@"+pinRollback, 1))
	if r := b.script("pin", "gateway"); r.code != 0 {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	requireTagged(t, b, tagCmd(pinRollback))
}

func TestProdImagePinTagsOnlyThisPackagesDigests(t *testing.T) {
	t.Parallel()
	// The rollback revision's image is the rollback digest, which sits in gateway's listing at rank 41, but the reference is
	// in another package, in a package whose name starts with gateway, in another registry, or is not a digest.
	own := pinRegistry + "/gateway@" + pinRollback
	for name, ref := range map[string]string{
		"another package":      pinRegistry + "/auth@" + pinRollback,
		"a longer package":     pinRegistry + "/gateway-evil@" + pinRollback,
		"another registry":     "other.test/proj/images/gateway@" + pinRollback,
		"a malformed digest":   pinRegistry + "/gateway@sha256:xyz",
		"a tag named like one": pinRegistry + "/gateway:" + strings.TrimPrefix(pinRollback, "sha256:"),
	} {
		b := pinBox(t, 36)
		b.put(revRollback+".json", strings.ReplaceAll(pinFixture(t, "revision-gateway-00380-mac.json"), own, ref))
		r := b.script("pin", "gateway")
		if r.code != 0 {
			t.Fatalf("%s: exit %d stderr %q", name, r.code, r.stderr)
		}
		requireTagged(t, b)
		if strings.Contains(r.stderr, "::warning::") {
			t.Errorf("%s: stderr %q warns about a reference that is not this package's digest", name, r.stderr)
		}
	}
	// Control: the same revision naming gateway's digest tags it.
	b := pinBox(t, 36)
	if r := b.script("pin", "gateway"); r.code != 0 {
		t.Fatalf("control: exit %d stderr %q", r.code, r.stderr)
	}
	requireTagged(t, b, tagCmd(pinRollback))
}

func TestProdImagePinProtectsTheTemplateImage(t *testing.T) {
	t.Parallel()
	// The service's template names a digest no revision names; it is untagged and past the margin in the real listing.
	var rows []map[string]any
	if err := json.Unmarshal([]byte(pinListing(t, 35, nil)), &rows); err != nil {
		t.Fatal(err)
	}
	var old string
	for _, r := range rows {
		if tags, _ := r["tags"].([]any); len(tags) == 0 && r["version"] != pinServing && r["version"] != pinRollback &&
			r["createTime"].(string) < rowOf(t, rows, pinRollback)["createTime"].(string) {
			old = r["version"].(string)
			break
		}
	}
	if old == "" {
		t.Fatal("no untagged real row older than the rollback digest")
	}
	b := pinBox(t, 35) // rollback at rank 40, serving at 38
	b.put(svcKey+".json", strings.Replace(pinFixture(t, "gateway-service.json"),
		pinRegistry+"/gateway:9ad5fad6fe39dd9859a095811ce529a875e3f7e0", pinRegistry+"/gateway@"+old, 1))
	if r := b.script("pin", "gateway"); r.code != 0 {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	want := []string{tagCmd(pinRollback), tagCmd(old)}
	slices.Sort(want)
	requireTagged(t, b, want...)
}

func TestProdImagePinReadsEveryRevisionWithTrafficAndNoOther(t *testing.T) {
	t.Parallel()
	const split, idle = "gateway-00382-xyz", "gateway-00384-new"
	oldest := fmt.Sprintf("sha256:%064x", 1) // the oldest of 40 synthetic rows: rank 40
	b := pinBox(t, 40)
	svc := addTraffic(t, pinFixture(t, "gateway-service.json"), map[string]any{"revisionName": split, "percent": 20})
	svc = addTraffic(t, svc, map[string]any{"revisionName": idle, "percent": 0, "tag": "c-aaaaaaaaaaaa"})
	b.put(svcKey+".json", svc)
	b.put("run.revisions.describe."+split+".json", strings.ReplaceAll(pinFixture(t, "revision-gateway-00383-biy.json"), pinServing, oldest))
	b.put("run.revisions.describe."+idle+".json", strings.ReplaceAll(pinFixture(t, "revision-gateway-00383-biy.json"), pinServing, fmt.Sprintf("sha256:%064x", 2)))
	if r := b.script("pin", "gateway"); r.code != 0 {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	if got := b.gcloudCalls("run revisions describe " + idle); len(got) != 0 {
		t.Errorf("read the 0 %% revision: %q", got)
	}
	if got := b.gcloudCalls("run revisions describe " + split); len(got) != 1 {
		t.Errorf("revision with 20 %% read %d times, want once", len(got))
	}
	// Serving (rank 43), rollback (45) and the split revision's digest (40); the idle one's digest (rank 39) is not protected.
	requireTagged(t, b, tagCmd(oldest), tagCmd(pinServing), tagCmd(pinRollback))
}

func TestProdImagePinCountsATieAsAhead(t *testing.T) {
	t.Parallel()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(pinListing(t, 34, nil)), &rows); err != nil { // the rollback digest is at rank 39
		t.Fatal(err)
	}
	roll := rowOf(t, rows, pinRollback)
	rows = append(rows, map[string]any{"version": "sha256:" + hex64("9"), "tags": []string{}, "createTime": roll["createTime"], "updateTime": roll["updateTime"]})
	raw, _ := json.Marshal(rows)
	b := pinBox(t, 34)
	b.put("list.json", string(raw))
	if r := b.script("pin", "gateway"); r.code != 0 {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	requireTagged(t, b, tagCmd(pinRollback))
}

func TestProdImagePinWarnsOnAnEmptyListing(t *testing.T) {
	t.Parallel()
	b := pinBox(t, 0)
	b.put("list.json", "[]")
	r := b.script("pin", "gateway")
	if r.code != 0 || strings.Count(r.stderr, "is not in the registry") != 2 {
		t.Errorf("exit %d stderr %q, want exit 0 and a warning for each of the two protected digests", r.code, r.stderr)
	}
	requireTagged(t, b)
}

func TestProdImagePinNeedsNoWriteOutsideTagsAdd(t *testing.T) {
	t.Parallel()
	// Every gcloud call of the first-day state and the margin state is a read or the one tags add.
	for _, synth := range []int{0, 35, 55} {
		b := pinBox(t, synth)
		if r := b.script("pin", "gateway"); r.code != 0 {
			t.Fatalf("%d synthetic rows: exit %d stderr %q", synth, r.code, r.stderr)
		}
		calls := b.lines("gcloud.log")
		if len(calls) == 0 {
			t.Fatalf("%d synthetic rows: no gcloud calls", synth)
		}
		for _, c := range calls {
			if !strings.HasPrefix(c, "run services describe ") && !strings.HasPrefix(c, "run revisions describe ") &&
				!strings.HasPrefix(c, "artifacts docker images list ") && !strings.HasPrefix(c, "artifacts docker tags add ") {
				t.Errorf("%d synthetic rows: pin made the call %q", synth, c)
			}
		}
	}
}
