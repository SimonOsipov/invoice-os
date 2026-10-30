package main

import (
	"bytes"
	"encoding/base64"
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
	email    = "probe@example.test"
	password = "probe-pass-value"
	authCode = "exchange-code-value"
	refresh  = "refresh-token-value"
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

	// the probe: signIn and exchange get the decoded JSON body, reads gets the path
	signIn   func(in map[string]string) (int, string)
	exchange func(in map[string]string) (int, string)
	reads    func(path string) (int, string)
	access   string
	posts    []map[string]string
	auth     map[string]string
}

func b64(v any) string {
	raw, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// jwtWith builds an unsigned token at runtime so no token literal reaches a commit.
func jwtWith(claims map[string]any) string {
	return b64(map[string]string{"alg": "none"}) + "." + b64(claims) + ".sig"
}

func probeEnv() map[string]string {
	return map[string]string{"PROD_GATEWAY_TOKEN": token, "PROD_PROBE_EMAIL": email, "PROD_PROBE_PASSWORD": password}
}

func (f *fakeProd) posted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		if !strings.HasPrefix(r, "GET ") {
			out = append(out, r)
		}
	}
	return out
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
		auth:    map[string]string{},
		access:  jwtWith(map[string]any{"user_metadata": map[string]any{"email_verified": true}}),
		landing: `<html><script type="module" crossorigin src="/assets/a.js"></script><script src="/assets/b.js"></script></html>`,
		bundles: map[string]string{"/assets/a.js": "nothing here", "/assets/b.js": "x=\"" + gaID + "\""},
	}
	f.fleet = func(int) (int, string) { return 200, fleetBody(nil) }
	f.site = func(string) (int, string, http.Header) { return 200, good + "\n", nil }
	f.signIn = func(in map[string]string) (int, string) {
		if in["email"] != email || in["password"] != password || len(in["state"]) != 43 {
			return 401, `{"error":"invalid email or password"}`
		}
		return 200, `{"code":"` + authCode + `"}`
	}
	f.exchange = func(in map[string]string) (int, string) {
		if in["code"] != authCode || len(f.posts) < 1 || in["state"] != f.posts[0]["state"] {
			return 400, `{"error":"invalid or expired code"}`
		}
		return 200, `{"access_token":"` + f.access + `","refresh_token":"` + refresh + `"}`
	}
	f.reads = func(path string) (int, string) {
		if path == "/api/tenancy/v1/me" {
			return 200, `{"tenant":{"id":"t-1","name":"Probe","kind":"own","deletion_due_at":null},"user":{"id":"u-1","role":"owner","display_name":null,"email":null}}`
		}
		return 200, `{}`
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeProd) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.headers[r.URL.Path] = r.Header.Get("X-Gateway-Token")
	f.auth[r.URL.Path] = r.Header.Get("Authorization")
	f.mu.Unlock()
	switch {
	case r.URL.Path == "/auth/sign-in" || r.URL.Path == "/auth/exchange":
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		call := f.signIn
		if r.URL.Path == "/auth/exchange" {
			call = f.exchange
		} else {
			f.posts = append(f.posts, in)
		}
		f.mu.Unlock()
		code, body := call(in)
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	case strings.HasPrefix(r.URL.Path, "/api/"):
		if r.Header.Get("Authorization") != "Bearer "+f.access {
			w.WriteHeader(401)
			return
		}
		code, body := f.reads(r.URL.Path)
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
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
		env = probeEnv()
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
		"worker absent":        {200, fleetBody(func(m map[string]map[string]any) { delete(m, "worker") }), "worker missing"},
		"invoice down":         {200, fleetBody(func(m map[string]map[string]any) { m["invoice"]["status"] = "down" }), "invoice down"},
		"empty services":       {200, `{"status":"ok","services":[]}`, "gateway missing"},
		"503 without services": {503, `{"status":"degraded","services":[]}`, "fleet answered 503"},
		"503 not json":         {503, "<html>bad gateway</html>", "fleet answered 503"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.fleet = func(int) (int, string) { return c.code, c.body }
			f.run(t, nil).want(t, 0, "failed", c.detail)
		})
	}
}

// degradedFleet is the 503 body the gateway sends when a service is down: status degraded,
// the down entry carries an error and no build.
func degradedFleet(down string) string {
	var doc map[string]any
	_ = json.Unmarshal([]byte(fleetBody(func(m map[string]map[string]any) {
		m[down] = map[string]any{"name": down, "status": "down", "error": "health returned 500"}
	})), &doc)
	doc["status"] = "degraded"
	raw, _ := json.Marshal(doc)
	return string(raw)
}

func TestFleet_503NamesTheDownServiceAndBuildsStillReport(t *testing.T) {
	f := newFake(t)
	f.fleet = func(int) (int, string) { return 503, degradedFleet("invoice") }
	r := f.run(t, nil)
	r.want(t, 0, "failed", "invoice down")
	if strings.Contains(r.step(t, 0).Detail, "answered 503") {
		t.Errorf("detail %q does not name the service", r.step(t, 0).Detail)
	}
	r.want(t, 1, "failed", "invoice=none")
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

func TestGA_BadScriptAddressFails(t *testing.T) {
	f := newFake(t)
	f.landing = `<html><script src="/assets/%zz.js"></script></html>`
	r := f.run(t, nil)
	r.want(t, 3, "failed", "landing names a bad script address")
	r.want(t, 4, "passed", "")
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
	want := []string{"fleet is ready", "services run main's head", "sites serve main's head", "landing carries the production GA4 id", "probe signs in", "probe has no registration metadata", "probe loads its workspace"}
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

func TestGuard_RefusesEveryOtherWrite(t *testing.T) {
	f := newFake(t)
	c := newClient()
	cases := []struct{ method, path string }{
		{"POST", "/auth/sign-out"},
		{"POST", "/auth/register"},
		{"PUT", "/auth/sign-in"},
		{"PATCH", "/auth/sign-in"},
		{"DELETE", "/auth/sign-in"},
		{"PUT", "/auth/exchange"},
		{"PATCH", "/auth/exchange"},
		{"DELETE", "/auth/exchange"},
		{"POST", "/api/tenancy/v1/me"},
	}
	for _, tc := range cases {
		req, _ := http.NewRequest(tc.method, f.srv.URL+tc.path, nil)
		if _, err := c.Do(req); err == nil {
			t.Errorf("%s %s was sent", tc.method, tc.path)
		}
	}
	if len(f.requests) != 0 {
		t.Errorf("server saw %v", f.requests)
	}
}

func TestAllowedCalls_WholeRunListsEveryCall(t *testing.T) {
	f := newFake(t)
	r := f.run(t, nil)
	if !r.sec.OK {
		t.Fatalf("run is red: %s", r.stdout)
	}
	want := map[string]int{
		"GET /healthz/fleet": 1, "GET /": 1, "GET /assets/a.js": 1, "GET /assets/b.js": 1,
		"POST /auth/sign-in": 1, "POST /auth/exchange": 1,
		"GET /api/tenancy/v1/me": 1, "GET /api/portfolio/v1/entities": 1, "GET /api/invoice/v1/invoices": 1, "GET /api/dashboard/v1/rollup": 1,
	}
	for _, site := range sites {
		want["GET /site/"+site+"/build.txt"]++
	}
	got := map[string]int{}
	for _, q := range f.requests {
		got[q]++
	}
	if len(got) != len(want) {
		t.Errorf("calls %v, want %v", got, want)
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("%s: %d calls, want %d", k, got[k], n)
		}
	}
	if len(f.posted()) != 2 {
		t.Errorf("non-GET calls %v", f.posted())
	}
}

func TestProbe_SignInThenExchangeBindsTheState(t *testing.T) {
	f := newFake(t)
	var seen []string
	f.exchange = func(in map[string]string) (int, string) {
		seen = append(seen, in["state"], in["code"])
		return 200, `{"access_token":"` + f.access + `","refresh_token":"` + refresh + `"}`
	}
	r := f.run(t, nil)
	r.want(t, 4, "passed", "")
	if len(f.posts) != 1 || len(seen) != 2 {
		t.Fatalf("posts %v seen %v", f.posts, seen)
	}
	state := f.posts[0]["state"]
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(state) {
		t.Errorf("state %q", state)
	}
	if seen[0] != state || seen[1] != authCode {
		t.Errorf("exchange sent state %q code %q", seen[0], seen[1])
	}
	if f.posts[0]["email"] != email || f.posts[0]["password"] != password {
		t.Error("sign-in lacks the credentials")
	}
}

func TestProbe_RefusalNamesTheStatusOnce(t *testing.T) {
	cases := map[string]struct {
		signCode, exCode int
		want             string
	}{
		"sign-in 401":  {401, 0, "sign-in answered 401"},
		"sign-in 429":  {429, 0, "sign-in answered 429"},
		"sign-in 503":  {503, 0, "sign-in answered 503"},
		"exchange 400": {200, 400, "exchange answered 400"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.signIn = func(map[string]string) (int, string) {
				if c.signCode != 200 {
					return c.signCode, `{"error":"x"}`
				}
				return 200, `{"code":"` + authCode + `"}`
			}
			f.exchange = func(map[string]string) (int, string) { return c.exCode, `{"error":"x"}` }
			r := f.run(t, nil)
			r.want(t, 4, "failed", c.want)
			r.want(t, 5, "skipped", "")
			r.want(t, 6, "skipped", "")
			wantPosts := 1
			if c.signCode == 200 {
				wantPosts = 2
			}
			if n := len(f.posted()); n != wantPosts {
				t.Errorf("POSTs %v", f.posted())
			}
			if r.code != 0 || r.sec.OK {
				t.Errorf("code %d ok %v", r.code, r.sec.OK)
			}
		})
	}
}

func TestProbe_200WithoutTheCredentialFails(t *testing.T) {
	ok := func(map[string]string) (int, string) { return 200, `{"code":"` + authCode + `"}` }
	cases := map[string]struct {
		signIn, exchange func(map[string]string) (int, string)
		want             string
	}{
		"no code":      {func(map[string]string) (int, string) { return 200, `{}` }, nil, "sign-in answered 200 with no code"},
		"not json":     {func(map[string]string) (int, string) { return 200, `<html>x</html>` }, nil, "sign-in answered 200 with no code"},
		"no access":    {ok, func(map[string]string) (int, string) { return 200, `{"refresh_token":"r"}` }, "exchange answered 200 without both tokens"},
		"no refresh":   {ok, func(map[string]string) (int, string) { return 200, `{"access_token":"a"}` }, "exchange answered 200 without both tokens"},
		"empty tokens": {ok, func(map[string]string) (int, string) { return 200, `{"access_token":"","refresh_token":""}` }, "exchange answered 200 without both tokens"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.signIn = c.signIn
			if c.exchange != nil {
				f.exchange = c.exchange
			}
			r := f.run(t, nil)
			r.want(t, 4, "failed", c.want)
			r.want(t, 5, "skipped", "")
			r.want(t, 6, "skipped", "")
		})
	}
}

// withClaims makes the exchange hand out a token with these claims.
func withClaims(f *fakeProd, claims map[string]any) { f.access = jwtWith(claims) }

func TestProbe_NoRegistrationPasses(t *testing.T) {
	cases := map[string]map[string]any{
		"verified only":     {"user_metadata": map[string]any{"email_verified": true}},
		"empty metadata":    {"user_metadata": map[string]any{}},
		"null metadata":     {"user_metadata": nil},
		"registration null": {"user_metadata": map[string]any{"registration": nil}},
	}
	for name, claims := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			withClaims(f, claims)
			r := f.run(t, nil)
			r.want(t, 5, "passed", "")
			r.want(t, 6, "passed", "")
		})
	}
}

func TestProbe_RegistrationSetFails(t *testing.T) {
	cases := map[string]any{"object": map[string]any{"company": "x"}, "empty object": map[string]any{}, "string": "yes"}
	for name, reg := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			withClaims(f, map[string]any{"user_metadata": map[string]any{"registration": reg}})
			r := f.run(t, nil)
			r.want(t, 5, "failed", "user_metadata.registration is set")
			r.want(t, 6, "passed", "")
			if r.sec.OK {
				t.Error("run is green")
			}
		})
	}
}

func TestProbe_ClaimAbsentFails(t *testing.T) {
	f := newFake(t)
	withClaims(f, map[string]any{"sub": "u-1"})
	r := f.run(t, nil)
	r.want(t, 5, "failed", "token has no user_metadata claim")
	r.want(t, 6, "passed", "")
}

func TestProbe_MalformedTokenFails(t *testing.T) {
	cases := map[string]string{
		"not a jwt":       "plain-opaque-value",
		"two segments":    "aGVhZGVy." + b64(map[string]any{"user_metadata": map[string]any{}}),
		"padded payload":  "aGVhZGVy." + base64.URLEncoding.EncodeToString([]byte(`{"user_metadata":{}}`)) + ".sig",
		"payload no json": "aGVhZGVy." + base64.RawURLEncoding.EncodeToString([]byte("not json")) + ".sig",
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.access = tok
			r := f.run(t, nil)
			r.want(t, 4, "passed", "")
			r.want(t, 5, "failed", "token is not a JWT")
			r.want(t, 6, "passed", "")
		})
	}
}

func TestProbe_WorkspaceReadsCarryTheBearer(t *testing.T) {
	f := newFake(t)
	r := f.run(t, nil)
	r.want(t, 6, "passed", "")
	for _, p := range workspaceReads {
		if f.auth[p] != "Bearer "+f.access {
			t.Errorf("%s saw %q", p, f.auth[p])
		}
	}
	for p, a := range f.auth {
		if a != "" && !strings.HasPrefix(p, "/api/") {
			t.Errorf("the bearer reached %s", p)
		}
	}
}

func TestProbe_WorkspaceFailureNamesThePath(t *testing.T) {
	f := newFake(t)
	f.reads = func(path string) (int, string) {
		switch path {
		case "/api/dashboard/v1/rollup":
			return 500, `{"error":"boom"}`
		case "/api/tenancy/v1/me":
			return 200, `{"tenant":{"id":""}}`
		}
		return 200, `{}`
	}
	r := f.run(t, nil)
	r.want(t, 6, "failed", "/api/dashboard/v1/rollup answered 500")
	r.want(t, 6, "failed", "/me has no tenant")
	if f.count("/api/dashboard/v1/rollup") != 3 {
		t.Errorf("rollup tries: %d", f.count("/api/dashboard/v1/rollup"))
	}
}

func TestProbe_MeWithoutTenantFails(t *testing.T) {
	cases := map[string]string{"not json": `<html>`, "no tenant key": `{"user":{}}`, "tenant null": `{"tenant":null}`}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.reads = func(string) (int, string) { return 200, body }
			f.run(t, nil).want(t, 6, "failed", "/me has no tenant")
		})
	}
}

func TestProbe_SecretsAbsent(t *testing.T) {
	cases := map[string]map[string]string{
		"both unset":    {"PROD_GATEWAY_TOKEN": token},
		"only email":    {"PROD_GATEWAY_TOKEN": token, "PROD_PROBE_EMAIL": email},
		"only password": {"PROD_GATEWAY_TOKEN": token, "PROD_PROBE_PASSWORD": password},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			r := f.run(t, env)
			r.want(t, 4, "failed", "secrets PROD_PROBE_EMAIL and PROD_PROBE_PASSWORD are not set")
			r.want(t, 5, "skipped", "")
			r.want(t, 6, "skipped", "")
			if r.code != 0 || r.sec.OK || len(r.sec.Steps) != 7 {
				t.Errorf("code %d ok %v steps %d", r.code, r.sec.OK, len(r.sec.Steps))
			}
			for i := 0; i < 4; i++ {
				r.want(t, i, "passed", "")
			}
			if len(f.posted()) != 0 {
				t.Errorf("sent %v", f.posted())
			}
			assertAbsent(t, r, email, password)
		})
	}
}

func TestNoSecretInAnyOutput(t *testing.T) {
	echo := "echo " + email + " " + password + " " + token + " " + authCode + " " + refresh
	secrets := []string{email, password, token, authCode, refresh}

	green := newFake(t)
	r := green.run(t, nil)
	assertAbsent(t, r, append(secrets, green.access)...)

	// every call fails with a body that echoes the secrets
	f := newFake(t)
	f.fleet = func(int) (int, string) { return 500, echo }
	f.site = func(string) (int, string, http.Header) { return 500, echo, nil }
	f.landing = echo
	f.signIn = func(map[string]string) (int, string) { return 500, echo }
	r = f.run(t, nil)
	assertAbsent(t, r, append(secrets, f.access)...)

	// sign-in and exchange succeed, the reads fail with echoes
	g := newFake(t)
	g.reads = func(string) (int, string) { return 500, echo + " " + g.access }
	r = g.run(t, nil)
	assertAbsent(t, r, append(secrets, g.access)...)

	// exchange refuses with an echo body
	x := newFake(t)
	x.exchange = func(map[string]string) (int, string) { return 400, echo + " " + x.access }
	r = x.run(t, nil)
	assertAbsent(t, r, append(secrets, x.access)...)
	r.want(t, 4, "failed", "exchange answered 400")

	// the tokens are bad: nothing of them reaches the section
	h := newFake(t)
	h.access = "opaque-" + authCode
	h.reads = func(string) (int, string) { return 200, `{"tenant":{"id":"` + token + `"}}` }
	r = h.run(t, nil)
	assertAbsent(t, r, append(secrets, h.access)...)
}
