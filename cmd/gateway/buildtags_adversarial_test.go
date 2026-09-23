package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// dockerfileRuns returns each RUN's shell text as Docker hands it to /bin/sh -c:
// comment lines dropped, even inside a continuation, and continuations joined.
func dockerfileRuns(src string) []string {
	var runs []string
	var cur strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		body, cont := strings.CutSuffix(strings.TrimRight(line, " \t\r"), `\`)
		cur.WriteString(body)
		if cont {
			continue
		}
		instr := strings.TrimSpace(cur.String())
		cur.Reset()
		if kw, rest, ok := strings.Cut(instr, " "); ok && strings.EqualFold(kw, "RUN") {
			runs = append(runs, strings.TrimSpace(rest))
		}
	}
	return runs
}

// fakeGo records CGO_ENABLED and its argv, NUL-separated, so an empty or multi-line arg survives.
const fakeGo = "#!/bin/sh\nprintf '%s\\0' \"CGO_ENABLED=${CGO_ENABLED-unset}\" \"$@\" > \"$FAKE_GO_LOG\"\n"

type goBuildCall struct {
	cgo, out   string
	tags, pkgs []string
}

// parseGoBuild reads fakeGo's log the way `go build` reads its flags.
func parseGoBuild(log []byte) (goBuildCall, error) {
	args := strings.Split(strings.TrimSuffix(string(log), "\x00"), "\x00")
	var c goBuildCall
	if len(args) < 2 || args[1] != "build" {
		return c, fmt.Errorf("fake go ran as %q, want `go build …`", args)
	}
	c.cgo = strings.TrimPrefix(args[0], "CGO_ENABLED=")
	a := args[2:]
	for i := 0; i < len(a); i++ {
		switch x := a[i]; {
		case x == "-tags" || x == "-o":
			if i+1 == len(a) {
				return c, fmt.Errorf("%s has no value in %q", x, a)
			}
			i++
			if x == "-o" {
				c.out = a[i]
			} else {
				c.tags = strings.FieldsFunc(a[i], func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
			}
		case strings.HasPrefix(x, "-tags="):
			c.tags = strings.FieldsFunc(strings.TrimPrefix(x, "-tags="), func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
		case strings.HasPrefix(x, "-"):
		default:
			c.pkgs = append(c.pkgs, x)
		}
	}
	return c, nil
}

// Executes the Dockerfile's own build line with a fake go on PATH: the argv it
// receives is what the image build compiles.
func TestDockerfileBuildLineReadsTheServiceBuildTags(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	var builds []string
	for _, r := range dockerfileRuns(string(src)) {
		if strings.Contains(r, "go build") {
			builds = append(builds, r)
		}
	}
	if len(builds) != 1 {
		t.Fatalf("Dockerfile has %d RUN(s) invoking go build, want exactly 1: %q", len(builds), builds)
	}
	run := builds[0]

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(fakeGo), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name, service string
		gatewayTags   []byte // nil: the real stamp script writes it
		wantTags      []string
	}{
		{"gateway, committed empty file", "gateway", []byte{}, nil},
		{"gateway, whitespace-only file", "gateway", []byte("\n \n"), nil},
		{"gateway, stamped by the real script", "gateway", nil, []string{"mockissuer"}},
		{"tenancy, no build.tags while the gateway is stamped", "tenancy", nil, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "cmd", "gateway", "build.tags")
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, c.gatewayTags, 0o644); err != nil {
				t.Fatal(err)
			}
			if c.gatewayTags == nil {
				if out, code := runStamp(t, dir); code != 0 {
					t.Fatalf("stamp exit %d:\n%s", code, out)
				}
			}
			log := filepath.Join(t.TempDir(), "go.log")
			cmd := exec.CommandContext(t.Context(), "sh", "-c", run)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"SERVICE="+c.service, "FAKE_GO_LOG="+log, "CGO_ENABLED=1")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("sh -c <Dockerfile build RUN>: %v\n%s", err, out)
			}
			b, err := os.ReadFile(log)
			if err != nil {
				t.Fatalf("the build RUN never invoked go: %v", err)
			}
			got, err := parseGoBuild(b)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.tags, c.wantTags) {
				t.Errorf("go build -tags reads %q, want %q", got.tags, c.wantTags)
			}
			if want := []string{"./cmd/" + c.service}; !slices.Equal(got.pkgs, want) {
				t.Errorf("go build packages %q, want %q", got.pkgs, want)
			}
			if got.out != "/out/service" {
				t.Errorf("go build -o %q, want /out/service", got.out)
			}
			if got.cgo != "0" {
				t.Errorf("CGO_ENABLED=%s, want 0", got.cgo)
			}
		})
	}

	t.Run("no SERVICE", func(t *testing.T) {
		log := filepath.Join(t.TempDir(), "go.log")
		cmd := exec.CommandContext(t.Context(), "sh", "-c", run)
		cmd.Dir = t.TempDir()
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "SERVICE=", "FAKE_GO_LOG="+log)
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "SERVICE build arg is required") {
			t.Errorf("empty SERVICE: err %v, output %q; want a refusal naming the build arg", err, out)
		}
		if _, err := os.Stat(log); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("empty SERVICE still invoked go build")
		}
	})
}

// The committed file may be whitespace-only, so the stamp must replace it, not append.
func TestStampMockIssuerOverwritesAWhitespaceOnlyFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "cmd", "gateway", "build.tags")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("\n \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if out, code := runStamp(t, dir); code != 0 {
			t.Fatalf("exit %d, want 0:\n%s", code, out)
		}
	}
	if got := string(fileState(t, target)); got != "mockissuer" && got != "mockissuer\n" {
		t.Errorf("cmd/gateway/build.tags reads %q after two stamps over a whitespace-only file, want exactly \"mockissuer\"", got)
	}
}

const canaryJobIf = "needs.changes.outputs.go == 'true'"

var (
	dockerCreateRE = regexp.MustCompile(`docker create ([^\s)]+)`)
	dockerCopyRE   = regexp.MustCompile(`docker cp \S+:/service"? (\S+)`)
)

// jobSteps splits a job block into its steps.
func jobSteps(block []string) [][]string {
	var steps [][]string
	in := false
	for _, l := range block {
		switch {
		case strings.HasPrefix(l, "      - "):
			steps = append(steps, []string{l})
			in = true
		case in && (strings.TrimSpace(l) == "" || strings.HasPrefix(l, "        ")):
			steps[len(steps)-1] = append(steps[len(steps)-1], l)
		default:
			in = false
		}
	}
	return steps
}

// stepKey returns a step's own key, on the dash line or indented under it.
func stepKey(step []string, key string) (string, bool) {
	for i, l := range step {
		rest, ok := "", false
		if i == 0 {
			rest, ok = strings.CutPrefix(l, "      - ")
		} else if strings.HasPrefix(l, "        ") && !strings.HasPrefix(l, "         ") {
			rest, ok = strings.TrimPrefix(l, "        "), true
		}
		if v, found := strings.CutPrefix(rest, key+":"); ok && found {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

// canaryImages returns the gateway image tags the job builds, and each
// `docker cp …:/service <dest>` destination mapped to the image it came from.
func canaryImages(steps [][]string) (built map[string]bool, copied map[string]string) {
	built, copied = map[string]bool{}, map[string]string{}
	for _, s := range steps {
		if uses, _ := stepKey(s, "uses"); strings.HasPrefix(uses, "docker/build-push-action") &&
			slices.ContainsFunc(s, func(l string) bool { return strings.TrimSpace(l) == "SERVICE=gateway" }) {
			for _, l := range s {
				if v, ok := strings.CutPrefix(strings.TrimSpace(l), "tags:"); ok {
					for _, tag := range strings.Split(v, ",") {
						built[strings.TrimSpace(tag)] = true
					}
				}
			}
		}
		image := ""
		for _, l := range strings.Split(runText(s), "\n") {
			if m := dockerCreateRE.FindStringSubmatch(l); m != nil {
				image = m[1]
			}
			if m := dockerCopyRE.FindStringSubmatch(l); m != nil && image != "" {
				copied[strings.Trim(m[1], `"'`)] = image
			}
		}
	}
	return built, copied
}

// canaryDecorativeProblems reports each way the docker-canary scan can go green
// without scanning the images: a swallowed exit, a gate that skips it, or a path
// that is not an image's /service.
func canaryDecorativeProblems(ciYAML string) []string {
	block := jobBlock(yamlCode(ciYAML), "docker-canary")
	if len(block) == 0 {
		return []string{"no docker-canary job"}
	}
	var problems []string
	jobIf := ""
	for _, l := range block {
		if v, ok := strings.CutPrefix(strings.TrimPrefix(strings.TrimSpace(l), "- "), "continue-on-error:"); ok && strings.TrimSpace(v) != "false" {
			problems = append(problems, fmt.Sprintf("docker-canary carries continue-on-error: %q; a failed scan would not fail the job", strings.TrimSpace(l)))
		}
		if v, ok := strings.CutPrefix(l, "    if:"); ok {
			jobIf = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(v), "${{"), "}}"))
		}
	}
	if jobIf != canaryJobIf {
		problems = append(problems, fmt.Sprintf("the docker-canary job if: reads %q, want %q; the aggregate counts a skipped canary as a pass", jobIf, canaryJobIf))
	}

	steps := jobSteps(block)
	built, copied := canaryImages(steps)
	scans := 0
	for _, s := range steps {
		var cmds []string
		for _, c := range shellCommands(runText(s)) {
			if strings.TrimSpace(c) != "" {
				cmds = append(cmds, strings.TrimSpace(c))
			}
		}
		for _, c := range cmds {
			m := canaryScanRE.FindStringSubmatchIndex(c)
			if m == nil {
				continue
			}
			env := map[string]string{}
			for _, a := range strings.Fields(c[m[2]:m[3]]) {
				k, v, _ := strings.Cut(a, "=")
				env[k] = strings.Trim(v, `"'`)
			}
			prod, okProd := env["GATEWAY_BINARY"]
			mock, okMock := env["GATEWAY_BINARY_MOCKISSUER"]
			if !okProd || !okMock {
				continue
			}
			scans++
			if v, ok := stepKey(s, "if"); ok {
				problems = append(problems, fmt.Sprintf("the scan step carries if: %s; it must run whenever the job runs", v))
			}
			if len(cmds) != 1 {
				problems = append(problems, fmt.Sprintf("the scan step runs %d commands; only the scan may run there, so nothing masks its exit", len(cmds)))
			}
			if m[0] != 0 || strings.ContainsAny(c, "|;&`") {
				problems = append(problems, fmt.Sprintf("the scan command carries a shell operator; its exit must be the step's: %q", c))
			}
			for _, b := range []struct{ name, path string }{{"GATEWAY_BINARY", prod}, {"GATEWAY_BINARY_MOCKISSUER", mock}} {
				image, ok := copied[b.path]
				switch {
				case !ok:
					problems = append(problems, fmt.Sprintf("%s=%s is not the /service of an image copied in this job", b.name, b.path))
				case !built[image]:
					problems = append(problems, fmt.Sprintf("%s=%s comes from %s, an image this job never builds with SERVICE=gateway", b.name, b.path, image))
				}
			}
			if prod == mock || (copied[prod] != "" && copied[prod] == copied[mock]) {
				problems = append(problems, fmt.Sprintf("GATEWAY_BINARY and GATEWAY_BINARY_MOCKISSUER scan the same binary (%s, %s)", prod, mock))
			}
		}
	}
	if scans == 0 {
		problems = append(problems, "no step runs `GATEWAY_BINARY=… GATEWAY_BINARY_MOCKISSUER=… go test ./cmd/gateway/`")
	}
	return problems
}

func TestDockerCanaryScanIsNotDecorative(t *testing.T) {
	const jobHead = "on: push\njobs:\n  docker-canary:\n    name: Docker image (canary)\n    needs: changes\n"
	const jobIf = "    if: needs.changes.outputs.go == 'true'\n"
	const steps = "    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n" +
		"      - name: Build the gateway image\n        uses: docker/build-push-action@v6\n        with:\n          build-args: |\n            SERVICE=gateway\n          load: true\n          tags: gateway:canary\n" +
		"      - name: Copy the production-shaped binary\n        run: |\n          id=$(docker create gateway:canary)\n          docker cp \"$id:/service\" /tmp/gateway-prod\n" +
		"      - name: Stamp\n        run: sh scripts/ci/stamp-mock-issuer.sh\n" +
		"      - name: Build the PR-shaped gateway image\n        uses: docker/build-push-action@v6\n        with:\n          build-args: |\n            SERVICE=gateway\n          load: true\n          tags: gateway:canary-mockissuer\n" +
		"      - name: Copy the PR-shaped binary\n        run: |\n          id=$(docker create gateway:canary-mockissuer)\n          docker cp \"$id:/service\" /tmp/gateway-mockissuer\n" +
		"      - name: Restore\n        run: git checkout -- cmd/gateway/build.tags\n"
	const scanName = "      - name: Scan both gateway binaries\n"
	const scanCmd = "GATEWAY_BINARY=/tmp/gateway-prod GATEWAY_BINARY_MOCKISSUER=/tmp/gateway-mockissuer go test ./cmd/gateway/ -count=1 -v"
	const tail = "  ci:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo ok\n"
	scan := scanName + "        run: " + scanCmd + "\n"
	good := jobHead + jobIf + steps + scan + tail

	for _, c := range []struct{ name, yaml, want string }{
		{"the planned job", good, ""},
		{"the scan as a continued block scalar", strings.Replace(good, scan, scanName+"        run: |\n          GATEWAY_BINARY=/tmp/gateway-prod \\\n          GATEWAY_BINARY_MOCKISSUER=/tmp/gateway-mockissuer \\\n          go test ./cmd/gateway/ -count=1 -v\n", 1), ""},
		{"|| true after the scan", strings.Replace(good, scanCmd, scanCmd+" || true", 1), "shell operator"},
		{"; true after the scan", strings.Replace(good, scanCmd, scanCmd+"; true", 1), "shell operator"},
		{"the scan piped to tee", strings.Replace(good, scanCmd, scanCmd+" | tee scan.txt", 1), "shell operator"},
		{"a short-circuit before the scan", strings.Replace(good, scanCmd, "true || "+scanCmd, 1), "shell operator"},
		{"set +e and a later command", strings.Replace(good, scan, scanName+"        run: |\n          set +e\n          "+scanCmd+"\n          echo done\n", 1), "runs 3 commands"},
		{"continue-on-error on the scan step", strings.Replace(good, scanName, scanName+"        continue-on-error: true\n", 1), "continue-on-error"},
		{"continue-on-error on the job", strings.Replace(good, jobIf, jobIf+"    continue-on-error: true\n", 1), "continue-on-error"},
		{"if: false on the scan step", strings.Replace(good, scanName, scanName+"        if: false\n", 1), "the scan step carries if:"},
		{"if: on the scan step's dash line", strings.Replace(good, scanName, "      - if: ${{ false }}\n        name: Scan both gateway binaries\n", 1), "the scan step carries if:"},
		{"the job gated off", strings.Replace(good, jobIf, "    if: false\n", 1), "job if:"},
		{"the job gate narrowed", strings.Replace(good, jobIf, "    if: needs.changes.outputs.go == 'true' && false\n", 1), "job if:"},
		{"the job gate removed", strings.Replace(good, jobIf, "", 1), "job if:"},
		{"GATEWAY_BINARY from a local build", strings.Replace(good, "GATEWAY_BINARY=/tmp/gateway-prod", "GATEWAY_BINARY=/tmp/gw-local", 1), "GATEWAY_BINARY=/tmp/gw-local is not the /service"},
		{"GATEWAY_BINARY_MOCKISSUER from a local build", strings.Replace(good, "GATEWAY_BINARY_MOCKISSUER=/tmp/gateway-mockissuer", "GATEWAY_BINARY_MOCKISSUER=/tmp/gw-local", 1), "GATEWAY_BINARY_MOCKISSUER=/tmp/gw-local is not the /service"},
		{"both variables on one binary", strings.Replace(good, "GATEWAY_BINARY_MOCKISSUER=/tmp/gateway-mockissuer", "GATEWAY_BINARY_MOCKISSUER=/tmp/gateway-prod", 1), "the same binary"},
		{"a copy from an image the job never builds", strings.Replace(good, "docker create gateway:canary-mockissuer", "docker create ghcr.io/example/gateway:latest", 1), "never builds"},
		{"no scan step", strings.Replace(good, scan, "", 1), "no step runs"},
	} {
		if c.name != "the planned job" && c.yaml == good {
			t.Fatalf("fixture %q: the replacement did not apply", c.name)
		}
		got := canaryDecorativeProblems(c.yaml)
		switch {
		case c.want == "" && len(got) != 0:
			t.Errorf("fixture %q: want no problem, got %v", c.name, got)
		case c.want != "" && !slices.ContainsFunc(got, func(p string) bool { return strings.Contains(p, c.want) }):
			t.Errorf("fixture %q: no problem mentions %q, got %v", c.name, c.want, got)
		}
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// Controls: the binding checks read two built images and two copies on the real job.
	built, copied := canaryImages(jobSteps(jobBlock(yamlCode(string(raw)), "docker-canary")))
	if len(built) < 2 || len(copied) < 2 {
		t.Fatalf("read %d built gateway image(s) %v and %d copy(ies) %v from docker-canary; the image scan is broken", len(built), built, len(copied), copied)
	}
	for _, p := range canaryDecorativeProblems(string(raw)) {
		t.Errorf(".github/workflows/ci.yml: %s", p)
	}
}
