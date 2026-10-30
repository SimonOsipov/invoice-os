package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

const (
	good     = "79fcf34d511bcb8dc2671a54d85b7348d6cb1f50"
	other    = "060dc803aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headSHA  = "1111111111111111111111111111111111111111"
	token    = "gw-token-value"
	internal = "http://notifications.internal.svc:8080/readyz"
)

// fakeProd is a recording server for the API, the landing and the six sites.
type fakeProd struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []string
	headers  map[string]string

	fleet    func(n int) (int, string)
	fleetN   int
	site     func(site string) (int, string, http.Header)
	landing  string
	bundles  map[string]string
	bundleSt map[string]int
	landingC int
}

func fleetBody(mut func(map[string]map[string]any)) string {
	svcs := map[string]map[string]any{}
	for _, n := range wantServices {
		s := map[string]any{"name": n, "status": "up", "build": good, "ready": true}
		if n == "auth" {
			delete(s, "build")
			delete(s, "ready")
		}
		if n == "docling" {
			delete(s, "ready")
		}
		svcs[n] = s
	}
	if mut != nil {
		mut(svcs)
	}
	list := []any{}
	for _, n := range wantServices {
		if s, ok := svcs[n]; ok {
			list = append(list, s)
		}
	}
	raw, _ := json.Marshal(map[string]any{"status": "ok", "services": list})
	return string(raw)
}

func newFake(t *testing.T) *fakeProd {
	f := &fakeProd{
		headers: map[string]string{},
		landing: `<html><script type="module" crossorigin src="/assets/a.js"></script><script src="/assets/b.js"></script></html>`,
		bundles: map[string]string{"/assets/a.js": "nothing here", "/assets/b.js": "x=\"" + gaID + "\""},
	}
	f.fleet = func(int) (int, string) { return 200, fleetBody(nil) }
	f.site = func(string) (int, string, http.Header) { return 200, good + "\n", nil }
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeProd) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.headers[r.URL.Path] = r.Header.Get("X-Gateway-Token")
	f.mu.Unlock()
	switch {
	case r.URL.Path == "/healthz/fleet":
		f.mu.Lock()
		f.fleetN++
		n := f.fleetN
		f.mu.Unlock()
		code, body := f.fleet(n)
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	case strings.HasPrefix(r.URL.Path, "/site/"):
		site := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/site/"), "/build.txt")
		code, body, h := f.site(site)
		for k, v := range h {
			w.Header()[k] = v
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	case r.URL.Path == "/":
		if f.landingC != 0 {
			w.WriteHeader(f.landingC)
			return
		}
		_, _ = w.Write([]byte(f.landing))
	case strings.HasPrefix(r.URL.Path, "/assets/"):
		if c := f.bundleSt[r.URL.Path]; c != 0 {
			w.WriteHeader(c)
			return
		}
		_, _ = w.Write([]byte(f.bundles[r.URL.Path]))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeProd) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r == "GET "+path {
			n++
		}
	}
	return n
}

type result struct {
	code           int
	stdout, stderr string
	raw            string
	sec            sectionOut
}

func (r result) step(t *testing.T, i int) stepResult {
	t.Helper()
	if i >= len(r.sec.Steps) {
		t.Fatalf("no step %d in %+v", i, r.sec.Steps)
	}
	return r.sec.Steps[i]
}

func (r result) want(t *testing.T, i int, status, detail string) {
	t.Helper()
	s := r.step(t, i)
	if s.Status != status {
		t.Errorf("step %d %q: status %q (%q), want %q", i, s.Name, s.Status, s.Detail, status)
	}
	if !strings.Contains(s.Detail, detail) {
		t.Errorf("step %d %q: detail %q lacks %q", i, s.Name, s.Detail, detail)
	}
}

func (f *fakeProd) run(t *testing.T, env map[string]string, args ...string) result {
	t.Helper()
	out := filepath.Join(t.TempDir(), "prod_check.json")
	if env == nil {
		env = map[string]string{"PROD_GATEWAY_TOKEN": token}
	}
	cfg := config{
		targets: targets{
			api:       f.srv.URL,
			landing:   f.srv.URL,
			siteURL:   func(s string) string { return f.srv.URL + "/site/" + s + "/build.txt" },
			retryWait: 0,
		},
		getenv: func(k string) string { return env[k] },
	}
	if !contains(args, "-expected") {
		args = append(args, "-expected", good, "-head", good, "-auth-build", good)
	}
	var so, se bytes.Buffer
	r := result{code: runWith(append(args, "-out", out), &so, &se, cfg), stdout: so.String(), stderr: se.String()}
	if raw, err := os.ReadFile(out); err == nil {
		r.raw = string(raw)
		if err := json.Unmarshal(raw, &r.sec); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func contains(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}

func assertAbsent(t *testing.T, r result, needles ...string) {
	t.Helper()
	for _, n := range needles {
		for name, s := range map[string]string{"section": r.raw, "stdout": r.stdout, "stderr": r.stderr} {
			if strings.Contains(s, n) {
				t.Errorf("%s holds %q", name, n)
			}
		}
	}
}

func TestFleet_RealShapeIsReady(t *testing.T) {
	f := newFake(t)
	r := f.run(t, nil)
	r.want(t, 0, "passed", "")
	if f.headers["/healthz/fleet"] != token {
		t.Errorf("fleet saw token %q", f.headers["/healthz/fleet"])
	}
	if r.code != 0 || !r.sec.OK {
		t.Errorf("code %d ok %v: %s", r.code, r.sec.OK, r.stdout)
	}
}

func TestFleet_NotReadyBehindA200Fails(t *testing.T) {
	f := newFake(t)
	f.fleet = func(int) (int, string) {
		return 200, fleetBody(func(m map[string]map[string]any) {
			m["tenancy"]["ready"] = false
			m["tenancy"]["not_ready"] = []string{"database"}
		})
	}
	r := f.run(t, nil)
	r.want(t, 0, "failed", "tenancy not ready: database")
	if f.count("/healthz/fleet") != 3 {
		t.Errorf("fleet requests: %d", f.count("/healthz/fleet"))
	}
}

func TestFleet_ServiceMissingOrDownFails(t *testing.T) {
	cases := map[string]struct {
		code         int
		body, detail string
	}{
		"worker absent":  {200, fleetBody(func(m map[string]map[string]any) { delete(m, "worker") }), "worker missing"},
		"invoice down":   {200, fleetBody(func(m map[string]map[string]any) { m["invoice"]["status"] = "down" }), "invoice down"},
		"empty services": {200, `{"status":"ok","services":[]}`, "gateway missing"},
		"503":            {503, fleetBody(nil), "fleet answered 503"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.fleet = func(int) (int, string) { return c.code, c.body }
			f.run(t, nil).want(t, 0, "failed", c.detail)
		})
	}
}

func TestFleet_UnreadableBodyFails(t *testing.T) {
	cases := map[string]struct{ body, detail string }{
		"empty":         {"", "fleet body is unreadable"},
		"not json":      {"<html>secret page</html>", "fleet body is unreadable"},
		"services null": {`{"services":null}`, "gateway missing"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.fleet = func(int) (int, string) { return 200, c.body }
			r := f.run(t, nil)
			r.want(t, 0, "failed", c.detail)
			r.want(t, 1, "skipped", "fleet not read")
			assertAbsent(t, r, "secret page")
			if f.count("/healthz/fleet") != 3 {
				t.Errorf("fleet requests: %d", f.count("/healthz/fleet"))
			}
		})
	}
}

func TestFleet_DuplicateNameFails(t *testing.T) {
	f := newFake(t)
	f.fleet = func(int) (int, string) {
		var doc map[string]any
		_ = json.Unmarshal([]byte(fleetBody(nil)), &doc)
		doc["services"] = append(doc["services"].([]any), map[string]any{"name": "invoice", "status": "down"})
		raw, _ := json.Marshal(doc)
		return 200, string(raw)
	}
	f.run(t, nil).want(t, 0, "failed", "invoice listed twice")
}

func TestFleet_NoTokenFails(t *testing.T) {
	f := newFake(t)
	r := f.run(t, map[string]string{})
	r.want(t, 0, "failed", "gateway token is not set")
	r.want(t, 1, "skipped", "")
	if f.count("/healthz/fleet") != 0 {
		t.Error("fleet was requested without a token")
	}
}

func TestFleet_ErrorTextStaysOut(t *testing.T) {
	f := newFake(t)
	f.fleet = func(int) (int, string) {
		return 200, fleetBody(func(m map[string]map[string]any) {
			m["notifications"]["status"] = "down"
			m["notifications"]["error"] = "Get " + internal + ": refused"
		})
	}
	r := f.run(t, nil)
	r.want(t, 0, "failed", "notifications down")
	assertAbsent(t, r, internal, "notifications.internal", token)
}

func TestBuilds_AllOnExpectedPasses(t *testing.T) {
	f := newFake(t)
	f.run(t, nil).want(t, 1, "passed", "")
}

func TestBuilds_MismatchNamesEachService(t *testing.T) {
	f := newFake(t)
	f.fleet = func(int) (int, string) {
		return 200, fleetBody(func(m map[string]map[string]any) {
			m["invoice"]["build"] = other
			delete(m["docling"], "build")
		})
	}
	r := f.run(t, nil, "-expected", good, "-head", good, "-auth-build", "")
	r.want(t, 1, "failed", "production is not on main: behind main: ")
	d := r.step(t, 1).Detail
	for _, w := range []string{"invoice=060dc803aaaa", "docling=none", "auth=none", "(want 79fcf34d511b)"} {
		if !strings.Contains(d, w) {
			t.Errorf("detail %q lacks %q", d, w)
		}
	}
	if strings.Contains(d, "tenancy") {
		t.Errorf("detail names a service on main: %q", d)
	}
}

func TestBuilds_ExpectedMissing(t *testing.T) {
	f := newFake(t)
	r := f.run(t, nil, "-expected", "")
	r.want(t, 1, "failed", "main's head was not resolved")
	r.want(t, 2, "failed", "main's head was not resolved")
}

func TestBuilds_HeadAheadOfExpectedPasses(t *testing.T) {
	f := newFake(t)
	r := f.run(t, nil, "-expected", good, "-head", headSHA, "-auth-build", good)
	r.want(t, 1, "passed", "111111111111")
	r.want(t, 1, "passed", "79fcf34d511b")
}

func TestSites_AllServeExpected(t *testing.T) {
	f := newFake(t)
	f.run(t, nil).want(t, 2, "passed", "")
}

func TestSites_HTMLFallbackReadsAsNone(t *testing.T) {
	f := newFake(t)
	f.site = func(s string) (int, string, http.Header) {
		if s == "ops.ascomply.com" {
			return 200, "<html>an app shell</html>", nil
		}
		return 200, good, nil
	}
	r := f.run(t, nil)
	r.want(t, 2, "failed", "ops.ascomply.com=none")
	assertAbsent(t, r, "app shell")
}

func TestSites_MismatchNamesTheSite(t *testing.T) {
	f := newFake(t)
	f.site = func(s string) (int, string, http.Header) {
		if s == "www.ascomply.com/library" {
			return 200, other, nil
		}
		return 200, good, nil
	}
	r := f.run(t, nil)
	r.want(t, 2, "failed", "production is not on main: behind main: www.ascomply.com/library=060dc803aaaa (want 79fcf34d511b)")
	if strings.Contains(r.step(t, 2).Detail, "www.ascomply.com/blog") {
		t.Error("detail names a site on main")
	}
}

func TestGA_FoundInABundle(t *testing.T) {
	f := newFake(t)
	f.run(t, nil).want(t, 3, "passed", "")
}

func TestGA_OtherIdFails(t *testing.T) {
	f := newFake(t)
	f.bundles["/assets/b.js"] = "G-AAAAAAAAAA"
	f.run(t, nil).want(t, 3, "failed", "landing bundle lacks "+gaID)
}

func TestGA_NoScriptFails(t *testing.T) {
	f := newFake(t)
	f.landing = "<html>no scripts</html>"
	f.run(t, nil).want(t, 3, "failed", "landing names no script")
}

func TestGA_LandingOrBundleUnreadableFails(t *testing.T) {
	cases := map[string]struct {
		set    func(f *fakeProd)
		detail string
	}{
		"landing 500": {func(f *fakeProd) { f.landingC = 500 }, "landing answered 500"},
		"landing 302": {func(f *fakeProd) { f.landingC = 302 }, "landing answered 302"},
		"bundle 404":  {func(f *fakeProd) { f.bundleSt = map[string]int{"/assets/a.js": 404} }, "bundle answered 404"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			c.set(f)
			r := f.run(t, nil)
			r.want(t, 3, "failed", c.detail)
			assertAbsent(t, r, "nothing here", "<script")
		})
	}
}

// ghSource reads a file of the repository from this package's directory.
func ghSource(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestProductionFactsMatchDeployProd(t *testing.T) {
	wf := ghSource(t, ".github/workflows/deploy-prod.yml")
	if m := regexp.MustCompile(`VITE_GA_MEASUREMENT_ID=(\S+)`).FindStringSubmatch(wf); m == nil || m[1] != gaID {
		t.Errorf("gaID %q, deploy-prod.yml %v", gaID, m)
	}
	m := regexp.MustCompile(`for site in ([^;]+); do`).FindStringSubmatch(wf)
	if m == nil || strings.Join(sites, " ") != m[1] {
		t.Errorf("sites %v, deploy-prod.yml %v", sites, m)
	}
	pm := regexp.MustCompile(`printf '%s\\n' (gateway[^\\]*)\\\n\s+(\S.*) \|`).FindStringSubmatch(wf)
	if pm == nil {
		t.Fatal("printf list not found in deploy-prod.yml")
	}
	if got := strings.Join(wantServices, " "); got != strings.TrimSpace(pm[1]) {
		t.Errorf("wantServices %q, deploy-prod.yml %q", got, pm[1])
	}
}

func TestSection_ShapeAndOrder(t *testing.T) {
	f := newFake(t)
	f.site = func(s string) (int, string, http.Header) {
		if s == "app.ascomply.com" {
			return 200, other, nil
		}
		return 200, good, nil
	}
	r := f.run(t, nil)
	if r.code != 0 || r.sec.OK {
		t.Fatalf("code %d ok %v", r.code, r.sec.OK)
	}
	var names []string
	for _, s := range r.sec.Steps {
		names = append(names, s.Name)
	}
	want := []string{"fleet is ready", "services run main's head", "sites serve main's head", "landing carries the production GA4 id"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Errorf("steps %v", names)
	}
	var doc struct {
		Failures []map[string]string `json:"failures"`
		Steps    []map[string]any    `json:"steps"`
	}
	if err := json.Unmarshal([]byte(r.raw), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Failures) != 1 {
		t.Fatalf("failures %v", doc.Failures)
	}
	f0 := doc.Failures[0]
	if f0["suite"] != "prod_check" || f0["test"] != "sites serve main's head" || f0["file"] != ".github/workflows/nightly.yml" || !strings.Contains(f0["message"], "app.ascomply.com") || len(f0) != 4 {
		t.Errorf("failure %v", f0)
	}
	for _, s := range doc.Steps {
		if _, ok := s["duration_ms"]; !ok {
			t.Errorf("step %v lacks duration_ms", s)
		}
	}
}

func TestSection_NightlysummaryReadsIt(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	suites := write("suites.txt", "go=Go\n")
	jobs := write("jobs.json", `[{"name":"Go","conclusion":"success","steps":[]}]`)
	summarize := func(sec string) map[string]any {
		out := filepath.Join(dir, "summary-"+filepath.Base(sec))
		cmd := exec.Command("go", "run", "./internal/tools/nightlysummary", "-suites", suites, "-jobs", jobs,
			"-results", filepath.Join(dir, "results"), "-out", out, "-section", "prod_check="+sec,
			"-now", "2026-10-29T02:00:00Z", "-started", "2026-10-29T01:17:00Z", "-run-id", "1", "-sha", "abc")
		cmd.Dir = filepath.Join("..", "..", "..")
		_ = cmd.Run()
		raw, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "section has no steps") {
			t.Errorf("summary rejects the section: %s", raw)
		}
		return doc
	}
	green := newFake(t).run(t, nil)
	greenPath := write("green.json", green.raw)
	if v := summarize(greenPath)["verdict"]; v != "green" {
		t.Errorf("green section gave %v", v)
	}
	redF := newFake(t)
	redF.site = func(string) (int, string, http.Header) { return 200, other, nil }
	red := redF.run(t, nil)
	if v := summarize(write("red.json", red.raw))["verdict"]; v != "red" {
		t.Errorf("red section gave %v", v)
	}
}

func TestRun_NoOutIsUsage(t *testing.T) {
	var so, se bytes.Buffer
	if code := runWith(nil, &so, &se, config{getenv: func(string) string { return "" }}); code != 2 {
		t.Errorf("code %d", code)
	}
}

func TestRetry_FleetPassesOnTheThirdTry(t *testing.T) {
	f := newFake(t)
	f.fleet = func(n int) (int, string) {
		switch n {
		case 1:
			return 503, "{}"
		case 2:
			return 200, fleetBody(func(m map[string]map[string]any) { m["invoice"]["ready"] = false })
		}
		return 200, fleetBody(nil)
	}
	r := f.run(t, nil)
	r.want(t, 0, "passed", "passed on try 3")
	if f.count("/healthz/fleet") != 3 {
		t.Errorf("requests %d", f.count("/healthz/fleet"))
	}
}

func TestRetry_StopsAtThree(t *testing.T) {
	f := newFake(t)
	f.fleet = func(int) (int, string) { return 503, "" }
	f.run(t, nil).want(t, 0, "failed", "fleet answered 503")
	if f.count("/healthz/fleet") != 3 {
		t.Errorf("requests %d", f.count("/healthz/fleet"))
	}
}

func TestRedirectIsAFailedRead(t *testing.T) {
	f := newFake(t)
	f.site = func(s string) (int, string, http.Header) {
		if s == "sup.ascomply.com" {
			return 302, "", http.Header{"Location": {"/landed"}}
		}
		return 200, good, nil
	}
	r := f.run(t, nil)
	r.want(t, 2, "failed", "sup.ascomply.com=none")
	if f.count("/landed") != 0 {
		t.Error("the redirect target was requested")
	}
}

func TestGuard_RefusesNonGet(t *testing.T) {
	f := newFake(t)
	c := newClient()
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/auth/sign-out", nil)
	if _, err := c.Do(req); err == nil {
		t.Error("a POST was sent")
	}
	if len(f.requests) != 0 {
		t.Errorf("server saw %v", f.requests)
	}
}
