// wait_spa_builds_test.go runs scripts/ci/wait-spa-builds.sh against a scripted curl:
// the shared SPA build wait of dev-env.yml's e2e and spa-build-gate jobs.
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	spaSHA = "5e975e718251c892c7cbfd3602bf6aa009f37ce5"
	spaOld = "0d1c2b3a4f5e60718293a4b5c6d7e8f901234567"
)

var spaURLs = []string{"https://landing.test", "https://app.test", "https://ops.test", "https://support.test"}

// spaShim is a curl on PATH that answers GET /health and GET /build.txt per host:
//
//	health-<host>[.seq]  the status code (default 200), one line per call from .seq first
//	build-<host>[.seq]   the body; with neither file, /build.txt is a 404
//
// req.log holds `<METHOD>\t<url>` per call and argv.log the tab-joined argv. The method is POST for
// any body flag, the -X value, HEAD for -I, else GET. Without --fail an error status exits 0.
type spaShim struct {
	dir, prelude string
}

func newSPAShim(t *testing.T) spaShim {
	t.Helper()
	dir := t.TempDir()
	stub := `#!/bin/sh
dir='` + dir + `'
{ for a in "$@"; do printf '%s\t' "$a"; done; printf '\n'; } >> "$dir/argv.log"
url="" method=GET w="" fail="" out="" explicit=""
while [ $# -gt 0 ]; do
  case "$1" in
    -X|--request) method="$2"; explicit=1; shift ;;
    -I|--head) [ -n "$explicit" ] || method=HEAD ;;
    -d|--data|--data-raw|--data-binary|--data-urlencode|--json|-F|--form|-T|--upload-file) [ -n "$explicit" ] || method=POST; shift ;;
    -w|--write-out) w="$2"; shift ;;
    -o|--output) out="$2"; shift ;;
    --max-time|-m|--connect-timeout|-H|--header|--retry|--retry-delay) shift ;;
    --fail|--fail-with-body) fail=1 ;;
    --*) ;;
    -[!-]*f*) fail=1 ;;
    http://*|https://*) url="$1" ;;
  esac
  shift
done
printf '%s\t%s\n' "$method" "$url" >> "$dir/req.log"
rest="${url#*://}"; host="${rest%%/*}"; path="/${rest#*/}"
next() {
  if [ -s "$dir/$1.seq" ]; then
    head -n 1 "$dir/$1.seq"; tail -n +2 "$dir/$1.seq" > "$dir/$1.seq.tmp"; mv "$dir/$1.seq.tmp" "$dir/$1.seq"
  elif [ -f "$dir/$1" ]; then cat "$dir/$1"; fi
}
case "$path" in
  /health)
    code=$(next "health-$host"); code="${code:-200}"; body=ok ;;
  /build.txt)
    if [ -s "$dir/build-$host.seq" ] || [ -f "$dir/build-$host" ]; then code=200; body=$(next "build-$host"); else code=404; body=; fi ;;
  *) code=404; body= ;;
esac
[ -z "$w" ] || printf '%s' "$code"
if [ "$code" -ge 400 ]; then
  [ -z "$fail" ] || { echo "curl: (22) The requested URL returned error: $code" >&2; exit 22; }
fi
[ -n "$out" ] || printf '%s\n' "$body"
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSleepStub(t, dir)
	s := spaShim{dir: dir, prelude: "export PATH='" + dir + "':\"$PATH\"\n"}
	for _, u := range spaURLs {
		s.serve(t, u, spaSHA)
	}
	return s
}

func spaHost(url string) string { return strings.TrimPrefix(url, "https://") }

// serve makes url's /build.txt answer body on every call.
func (s spaShim) serve(t *testing.T, url, body string) {
	t.Helper()
	writeFile(t, filepath.Join(s.dir, "build-"+spaHost(url)), body+"\n")
}

func (s spaShim) put(t *testing.T, name, body string) {
	t.Helper()
	writeFile(t, filepath.Join(s.dir, name), body)
}

// spaScript fails the test when the script is missing or not executable.
func spaScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "scripts", "ci", "wait-spa-builds.sh")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("scripts/ci/wait-spa-builds.sh does not exist: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("scripts/ci/wait-spa-builds.sh is not executable (mode %v)", info.Mode())
	}
	return path
}

func (s spaShim) run(t *testing.T, sha string, urls ...string) (stdout, stderr string, code int) {
	t.Helper()
	script := spaScript(t)
	return runBashScript(t, s.prelude+"bash '"+script+"' \"$@\"\n", append([]string{sha}, urls...)...)
}

func (s spaShim) sleeps(t *testing.T) []string {
	t.Helper()
	return railwayShim{dir: s.dir}.sleeps(t)
}

type spaRequest struct{ Method, URL string }

func (s spaShim) requests(t *testing.T) []spaRequest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.dir, "req.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []spaRequest
	for _, l := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		m, u, _ := strings.Cut(l, "\t")
		out = append(out, spaRequest{m, u})
	}
	return out
}

// hits counts the requests to url plus path.
func (s spaShim) hits(t *testing.T, url, path string) int {
	t.Helper()
	n := 0
	for _, r := range s.requests(t) {
		if r.URL == url+path {
			n++
		}
	}
	return n
}

func healthyLines(out, sha string) int {
	re := regexp.MustCompile(`(?m)^\s*healthy on ` + regexp.QuoteMeta(sha) + `\s*$`)
	return len(re.FindAllString(out, -1))
}

// The step's message in dev-env.yml before this subtask, with %s for url, sha and last seen.
func spaTimeoutError(url, sha, seen string) string {
	return "::error::" + url + " did not serve build " + sha + " within 600s (last seen: '" + seen + "'). " +
		"'none' means /build.txt is absent -- the image predates the stamped Dockerfile layer or the stamp step did not run; " +
		"any other value means the new image never replaced the old one. Running E2E here would drive the wrong frontend."
}

func TestWaitSPABuilds_AllServeTheExpectedBuild(t *testing.T) {
	s := newSPAShim(t)
	stdout, stderr, code := s.run(t, spaSHA, spaURLs...)

	if code != 0 {
		t.Errorf("exit %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if n := healthyLines(stdout, spaSHA); n != len(spaURLs) {
		t.Errorf("healthy-on lines = %d, want %d; stdout = %q", n, len(spaURLs), stdout)
	}
	for _, u := range spaURLs {
		if want := "Waiting for " + u + " on build " + spaSHA + " ..."; !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q", want)
		}
		for _, p := range []string{"/health", "/build.txt"} {
			if n := s.hits(t, u, p); n != 1 {
				t.Errorf("%s%s requests = %d, want 1", u, p, n)
			}
		}
	}
	if got := s.sleeps(t); len(got) != 0 {
		t.Errorf("sleeps = %v, want none when every SPA is ready at once", got)
	}
}

func TestWaitSPABuilds_WaitsForARollingDeploy(t *testing.T) {
	s := newSPAShim(t)
	app := spaURLs[1]
	s.put(t, "build-"+spaHost(app)+".seq", spaOld+"\n"+spaOld+"\n")
	stdout, stderr, code := s.run(t, spaSHA, spaURLs...)

	if code != 0 {
		t.Errorf("exit %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if n := s.hits(t, app, "/build.txt"); n != 3 {
		t.Errorf("app /build.txt requests = %d, want 3 (old, old, %s)", n, spaSHA[:7])
	}
	if got := strings.Join(s.sleeps(t), " "); got != "5 5" {
		t.Errorf("sleeps = %q, want %q: two 5 s waits for app, none for the others", got, "5 5")
	}
	if n := healthyLines(stdout, spaSHA); n != len(spaURLs) {
		t.Errorf("healthy-on lines = %d, want %d", n, len(spaURLs))
	}
}

func TestWaitSPABuilds_StaleBuildFailsNamingURLAndSeen(t *testing.T) {
	s := newSPAShim(t)
	ops := spaURLs[2]
	s.serve(t, ops, spaOld)
	stdout, stderr, code := s.run(t, spaSHA, spaURLs...)

	if code != 1 {
		t.Errorf("exit %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if n := s.hits(t, ops, "/build.txt"); n != 120 {
		t.Errorf("ops /build.txt requests = %d, want exactly 120", n)
	}
	sleeps := s.sleeps(t)
	total := 0
	for _, v := range sleeps {
		if v != "5" {
			t.Errorf("sleeps = %v, want every sleep to be 5", sleeps)
			break
		}
		total += 5
	}
	if total != 600 {
		t.Errorf("total sleep = %d s, want the 600 s window", total)
	}
	if want := spaTimeoutError(ops, spaSHA, spaOld); !strings.Contains(errorLines(stdout+stderr), want) {
		t.Errorf("error lines lack the timeout message\nwant: %q\ngot:  %q", want, errorLines(stdout+stderr))
	}
}

func TestWaitSPABuilds_NoHealthReportsNone(t *testing.T) {
	s := newSPAShim(t)
	landing := spaURLs[0]
	s.put(t, "health-"+spaHost(landing), "503\n")
	stdout, stderr, code := s.run(t, spaSHA, spaURLs...)

	if code != 1 {
		t.Errorf("exit %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if n := s.hits(t, landing, "/health"); n != 120 {
		t.Errorf("landing /health requests = %d, want exactly 120", n)
	}
	if n := s.hits(t, landing, "/build.txt"); n != 0 {
		t.Errorf("landing /build.txt requests = %d, want 0 while /health never answers 200", n)
	}
	if want := spaTimeoutError(landing, spaSHA, "none"); !strings.Contains(errorLines(stdout+stderr), want) {
		t.Errorf("error lines lack the timeout message\nwant: %q\ngot:  %q", want, errorLines(stdout+stderr))
	}
}

// A landing whose /build.txt is a 404 is the other way to see nothing.
func TestWaitSPABuilds_MissingBuildTxtReportsNone(t *testing.T) {
	s := newSPAShim(t)
	support := spaURLs[3]
	if err := os.Remove(filepath.Join(s.dir, "build-"+spaHost(support))); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := s.run(t, spaSHA, spaURLs...)

	if code != 1 {
		t.Errorf("exit %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if n := s.hits(t, support, "/build.txt"); n != 120 {
		t.Errorf("support /build.txt requests = %d, want exactly 120", n)
	}
	if want := spaTimeoutError(support, spaSHA, "none"); !strings.Contains(errorLines(stdout+stderr), want) {
		t.Errorf("error lines lack the timeout message\nwant: %q\ngot:  %q", want, errorLines(stdout+stderr))
	}
}

func TestWaitSPABuilds_SendsOnlyGETs(t *testing.T) {
	s := newSPAShim(t)
	// A passing run that also waits: app answers 503 on /health once, then serves an old build once.
	app := spaURLs[1]
	s.put(t, "health-"+spaHost(app)+".seq", "503\n200\n")
	s.put(t, "build-"+spaHost(app)+".seq", spaOld+"\n")
	stdout, stderr, code := s.run(t, spaSHA, spaURLs...)

	if code != 0 {
		t.Fatalf("exit %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	reqs := s.requests(t)
	if len(reqs) == 0 {
		t.Fatal("the curl stub logged no request")
	}
	var health, build int
	for _, r := range reqs {
		if r.Method != "GET" {
			t.Errorf("%s %s: want GET only", r.Method, r.URL)
		}
		health += btoi(strings.HasSuffix(r.URL, "/health"))
		build += btoi(strings.HasSuffix(r.URL, "/build.txt"))
	}
	if health == 0 || build == 0 {
		t.Errorf("control: /health requests = %d, /build.txt requests = %d, want both non-zero", health, build)
	}

	raw, err := os.ReadFile(filepath.Join(s.dir, "argv.log"))
	if err != nil {
		t.Fatal(err)
	}
	forbidden := regexp.MustCompile(`^(-d.*|--data.*|-X.*|--request.*|-I|--head|-F|--form|-T|--upload-file|--json)$`)
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		for _, tok := range strings.Split(strings.TrimSuffix(line, "\t"), "\t") {
			if forbidden.MatchString(tok) {
				t.Errorf("curl argv carries %q: %q", tok, line)
			}
		}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// No URL to wait for is a caller error, never a pass.
func TestWaitSPABuilds_NoURLsFails(t *testing.T) {
	s := newSPAShim(t)
	stdout, stderr, code := s.run(t, spaSHA)

	if code == 0 {
		t.Errorf("exit 0 with no URLs: a gate that checked nothing passed; stdout = %q, stderr = %q", stdout, stderr)
	}
	if !strings.Contains(errorLines(stdout+stderr), "usage") {
		t.Errorf("error lines carry no usage line: %q", stdout+stderr)
	}
}

// An empty expected sha would match an empty /build.txt: refused before any request.
func TestWaitSPABuilds_EmptyExpectedSHAFails(t *testing.T) {
	s := newSPAShim(t)
	for _, u := range spaURLs {
		s.serve(t, u, "")
	}
	stdout, stderr, code := s.run(t, "", spaURLs...)

	if code == 0 {
		t.Errorf("exit 0 with an empty expected sha; stdout = %q, stderr = %q", stdout, stderr)
	}
	if n := len(s.requests(t)); n != 0 {
		t.Errorf("requests = %d, want 0: nothing to compare against", n)
	}
}

// The comparison is exact: an abbreviated or extended sha is another build.
func TestWaitSPABuilds_OnlyTheExactBuildPasses(t *testing.T) {
	for _, c := range []struct{ name, served string }{
		{"abbreviated sha", spaSHA[:7]},
		{"sha with a suffix", spaSHA + "-dirty"},
		{"sha with a prefix", "v" + spaSHA},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newSPAShim(t)
			landing := spaURLs[0]
			s.serve(t, landing, c.served)
			stdout, stderr, code := s.run(t, spaSHA, spaURLs...)

			if code != 1 {
				t.Errorf("exit %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
			}
			if want := spaTimeoutError(landing, spaSHA, c.served); !strings.Contains(errorLines(stdout+stderr), want) {
				t.Errorf("error lines lack the timeout message naming %q: %q", c.served, errorLines(stdout+stderr))
			}
		})
	}
}

// The stamp file ends in a newline, and a CRLF or padded one must still match.
func TestWaitSPABuilds_TrimsWhitespaceAroundTheBuild(t *testing.T) {
	s := newSPAShim(t)
	s.put(t, "build-"+spaHost(spaURLs[0]), "  "+spaSHA+" \r\n")
	stdout, stderr, code := s.run(t, spaSHA, spaURLs...)

	if code != 0 {
		t.Errorf("exit %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if n := healthyLines(stdout, spaSHA); n != len(spaURLs) {
		t.Errorf("healthy-on lines = %d, want %d", n, len(spaURLs))
	}
}

// Each URL gets its own 120 ticks, and its own last-seen build.
func TestWaitSPABuilds_EachURLHasItsOwnWindowAndLastSeen(t *testing.T) {
	late := strings.Repeat(spaOld+"\n", 100) + spaSHA + "\n"
	s := newSPAShim(t)
	s.put(t, "build-"+spaHost(spaURLs[0])+".seq", late)
	s.put(t, "build-"+spaHost(spaURLs[1])+".seq", late)
	stdout, stderr, code := s.run(t, spaSHA, spaURLs...)
	if code != 0 {
		t.Fatalf("two URLs that each need 101 ticks: exit %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if n := s.hits(t, spaURLs[1], "/build.txt"); n != 101 {
		t.Errorf("app /build.txt requests = %d, want 101", n)
	}

	s = newSPAShim(t)
	ops := spaURLs[2]
	s.put(t, "health-"+spaHost(ops), "503\n")
	stdout, stderr, code = s.run(t, spaSHA, spaURLs...)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if want := spaTimeoutError(ops, spaSHA, "none"); !strings.Contains(errorLines(stdout+stderr), want) {
		t.Errorf("ops never answered /health after two URLs served %s; want last seen 'none': %q", spaSHA[:7], errorLines(stdout+stderr))
	}
}

// The first URL that fails ends the run: no later URL is asked.
func TestWaitSPABuilds_FirstFailureStopsTheRun(t *testing.T) {
	s := newSPAShim(t)
	s.serve(t, spaURLs[0], spaOld)
	_, _, code := s.run(t, spaSHA, spaURLs...)

	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if n := s.hits(t, spaURLs[0], "/health"); n != 120 {
		t.Errorf("landing /health requests = %d, want 120", n)
	}
	for _, u := range spaURLs[1:] {
		if n := s.hits(t, u, "/health"); n != 0 {
			t.Errorf("%s /health requests = %d, want 0 after landing failed", u, n)
		}
	}
}

// A trailing slash makes `//health`, which the edge does not answer: the run ends red naming the URL.
func TestWaitSPABuilds_TrailingSlashURLNeverPassesVacuously(t *testing.T) {
	s := newSPAShim(t)
	slashed := spaURLs[1] + "/"
	stdout, stderr, code := s.run(t, spaSHA, spaURLs[0], slashed)

	if code != 1 {
		t.Errorf("exit %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if e := errorLines(stdout + stderr); !strings.Contains(e, "::error::"+slashed+" did not serve build") {
		t.Errorf("error lines do not name the URL %q: %q", slashed, e)
	}
	if healthyLines(stdout, spaSHA) != 1 {
		t.Errorf("want exactly the first URL healthy; stdout = %q", stdout)
	}
}

// An empty prepare-env output reaches the script as "": refused before any request.
func TestWaitSPABuilds_EmptyURLFailsAtOnce(t *testing.T) {
	s := newSPAShim(t)
	stdout, stderr, code := s.run(t, spaSHA, spaURLs[0], "", spaURLs[2])

	if code == 0 {
		t.Errorf("exit 0 with an empty url; stdout = %q, stderr = %q", stdout, stderr)
	}
	if n := len(s.requests(t)); n != 0 {
		t.Errorf("requests = %d, want 0: the empty argument is refused before any request", n)
	}
	if e := errorLines(stdout + stderr); !strings.Contains(e, "argument 3") {
		t.Errorf("error lines do not name the empty argument's position (argument 3): %q", e)
	}
}
