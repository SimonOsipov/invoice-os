package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	gaID         = "G-E409H76XYY"
	attempts     = 3
	maxBody      = 8 << 20
	sectionSuite = "prod_check"
	sectionFile  = ".github/workflows/nightly.yml"
	secretsUnset = "secrets PROD_PROBE_EMAIL and PROD_PROBE_PASSWORD are not set"
)

// allowedPosts is every non-GET the run may send.
var allowedPosts = map[string]bool{"/auth/sign-in": true, "/auth/exchange": true}

// workspaceReads are the reads the app makes on load, one per service.
var workspaceReads = []string{"/api/tenancy/v1/me", "/api/portfolio/v1/entities", "/api/invoice/v1/invoices", "/api/dashboard/v1/rollup"}

var (
	wantServices = []string{"gateway", "auth", "tenancy", "portfolio", "invoice", "validation", "dashboard", "submission", "notifications", "worker", "docling"}
	sites        = []string{"app.ascomply.com", "www.ascomply.com", "www.ascomply.com/library", "www.ascomply.com/blog", "ops.ascomply.com", "sup.ascomply.com"}
	sha40        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	scriptSrc    = regexp.MustCompile(`src="(/assets/[^"]*\.js)"`)
)

// targets holds every address and the retry pause, so a test points the tool at httptest.
type targets struct {
	api       string
	landing   string
	siteURL   func(site string) string
	retryWait time.Duration
}

type config struct {
	targets targets
	getenv  func(string) string
}

func prodConfig() config {
	return config{
		targets: targets{
			api:       "https://api.ascomply.com",
			landing:   "https://www.ascomply.com",
			siteURL:   func(s string) string { return "https://" + s + "/build.txt" },
			retryWait: 20 * time.Second,
		},
		getenv: os.Getenv,
	}
}

type stepResult struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Detail     string `json:"detail,omitempty"`
}

type failureEntry struct {
	Suite   string `json:"suite"`
	Test    string `json:"test"`
	File    string `json:"file"`
	Message string `json:"message"`
}

type sectionOut struct {
	OK       bool           `json:"ok"`
	Failures []failureEntry `json:"failures"`
	Steps    []stepResult   `json:"steps"`
}

type serviceHealth struct {
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Build    string   `json:"build"`
	Ready    *bool    `json:"ready"`
	NotReady []string `json:"not_ready"`
}

// guard is the one choke point for what the run may send: GET, and POST to allowedPosts.
type guard struct{ next http.RoundTripper }

func (g guard) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet && !(r.Method == http.MethodPost && allowedPosts[r.URL.Path]) {
		return nil, fmt.Errorf("prodcheck: %s %s is not an allowed call", r.Method, r.URL.Path)
	}
	return g.next.RoundTrip(r)
}

type checker struct {
	t      targets
	client *http.Client
	getenv func(string) string

	head, expected, authBuild string
	fleet                     []serviceHealth
	access                    string
	steps                     []stepResult
}

func newClient() *http.Client {
	return &http.Client{
		Timeout:       40 * time.Second,
		Transport:     guard{http.DefaultTransport},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWith(args, stdout, stderr, prodConfig())
}

func runWith(args []string, stdout, stderr io.Writer, cfg config) int {
	fs := flag.NewFlagSet("prodcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	head := fs.String("head", "", "main's head commit")
	expected := fs.String("expected", "", "newest first-parent commit of main that changes a file other than *.md")
	authBuild := fs.String("auth-build", "", "commit in the auth image tag")
	out := fs.String("out", "", "prod_check section file to write (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: prodcheck -out <file> [-head SHA] [-expected SHA] [-auth-build SHA]")
		return 2
	}
	c := &checker{t: cfg.targets, client: newClient(), getenv: cfg.getenv, head: *head, expected: *expected, authBuild: *authBuild}
	c.step("fleet is ready", c.fleetStep)
	c.step("services run main's head", c.buildsStep)
	c.step("sites serve main's head", c.sitesStep)
	c.step("landing carries the production GA4 id", c.gaStep)
	c.step("probe signs in", c.signInStep)
	c.step("probe has no registration metadata", c.registrationStep)
	c.step("probe loads its workspace", c.workspaceStep)

	sec := sectionOut{OK: true, Failures: []failureEntry{}, Steps: c.steps}
	for _, s := range c.steps {
		line := s.Status + " " + s.Name
		if s.Detail != "" {
			line += ": " + s.Detail
		}
		fmt.Fprintln(stdout, line)
		if s.Status == "failed" {
			sec.OK = false
			sec.Failures = append(sec.Failures, failureEntry{sectionSuite, s.Name, sectionFile, s.Detail})
		}
	}
	raw, err := json.MarshalIndent(sec, "", "  ")
	if err == nil {
		err = os.WriteFile(*out, append(raw, '\n'), 0o644)
	}
	if err != nil {
		fmt.Fprintf(stderr, "prodcheck: %v\n", err)
		return 1
	}
	return 0
}

// stepFn returns status and detail: "passed" or "skipped" with an optional note, or "failed" with the reason.
type stepFn func() (status, detail string)

func (c *checker) step(name string, fn stepFn) {
	start := time.Now()
	status, detail := fn()
	c.steps = append(c.steps, stepResult{name, status, time.Since(start).Milliseconds(), detail})
}

func passedOn(try int) string {
	if try > 1 {
		return fmt.Sprintf("passed on try %d", try)
	}
	return ""
}

// get reads url with up to three tries. A redirect or any non-200 is a failed read; code is 0 when nothing answered.
func (c *checker) get(u string, headers map[string]string) (code int, body []byte, try int) {
	for try = 1; try <= attempts; try++ {
		code, body = c.getOnce(u, headers)
		if code == http.StatusOK {
			return code, body, try
		}
		if try < attempts {
			time.Sleep(c.t.retryWait)
		}
	}
	return code, nil, attempts
}

func (c *checker) getOnce(u string, headers map[string]string) (int, []byte) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return 0, nil
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return 0, nil
	}
	return resp.StatusCode, body
}

func answered(code int) string {
	if code == 0 {
		return "did not answer"
	}
	return fmt.Sprintf("answered %d", code)
}

func (c *checker) fleetStep() (string, string) {
	token := c.getenv("PROD_GATEWAY_TOKEN")
	if token == "" {
		return "failed", "gateway token is not set"
	}
	var problem string
	for try := 1; try <= attempts; try++ {
		if problem = c.readFleet(token); problem == "" {
			return "passed", passedOn(try)
		}
		if try < attempts {
			time.Sleep(c.t.retryWait)
		}
	}
	return "failed", problem
}

// readFleet makes one try. It leaves c.fleet set only when the roll-up listed services.
func (c *checker) readFleet(token string) string {
	c.fleet = nil
	code, body := c.getOnce(c.t.api+"/healthz/fleet", map[string]string{"X-Gateway-Token": token})
	if code != http.StatusOK && code != http.StatusServiceUnavailable {
		return "fleet " + answered(code)
	}
	var doc struct {
		Services []serviceHealth `json:"services"`
	}
	// A 503 is the roll-up naming what is down; an unreadable one stays "answered 503".
	if err := json.Unmarshal(body, &doc); err != nil {
		if code == http.StatusServiceUnavailable {
			return "fleet " + answered(code)
		}
		return "fleet body is unreadable"
	}
	if code == http.StatusServiceUnavailable && len(doc.Services) == 0 {
		return "fleet " + answered(code)
	}
	if len(doc.Services) > 0 {
		c.fleet = doc.Services
	}
	var problems []string
	for _, name := range wantServices {
		var found []serviceHealth
		for _, s := range doc.Services {
			if s.Name == name {
				found = append(found, s)
			}
		}
		switch {
		case len(found) == 0:
			problems = append(problems, name+" missing")
		case len(found) > 1:
			problems = append(problems, name+" listed twice")
		case found[0].Status != "up":
			problems = append(problems, name+" down")
		case found[0].Ready != nil && !*found[0].Ready:
			problems = append(problems, name+" not ready: "+strings.Join(found[0].NotReady, ","))
		}
	}
	if len(problems) == 0 && code != http.StatusOK {
		return "fleet " + answered(code)
	}
	return strings.Join(problems, "; ")
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// shaOrNone keeps a reported value out of the section unless it is a commit id.
func shaOrNone(s string) string {
	if sha40.MatchString(s) {
		return s
	}
	return "none"
}

func (c *checker) behind(pairs []string) string {
	return fmt.Sprintf("production is not on main: behind main: %s (want %s)", strings.Join(pairs, ", "), short(c.expected))
}

func (c *checker) buildsStep() (string, string) {
	if c.expected == "" {
		return "failed", "main's head was not resolved"
	}
	if len(c.fleet) == 0 {
		return "skipped", "fleet not read"
	}
	var behind []string
	for _, name := range wantServices {
		build := "none"
		if name == "auth" {
			build = shaOrNone(c.authBuild)
		} else {
			for _, s := range c.fleet {
				if s.Name == name {
					build = shaOrNone(s.Build)
					break
				}
			}
		}
		if build != c.expected {
			behind = append(behind, name+"="+short(build))
		}
	}
	if len(behind) > 0 {
		return "failed", c.behind(behind)
	}
	if c.head != "" && c.head != c.expected {
		return "passed", fmt.Sprintf("main's head %s only changes *.md; deployed head %s", short(c.head), short(c.expected))
	}
	return "passed", ""
}

func (c *checker) sitesStep() (string, string) {
	if c.expected == "" {
		return "failed", "main's head was not resolved"
	}
	var behind []string
	worst := 1
	for _, site := range sites {
		code, body, try := c.get(c.t.siteURL(site), nil)
		worst = max(worst, try)
		seen := "none"
		if code == http.StatusOK {
			seen = shaOrNone(strings.TrimSpace(string(body)))
		}
		if seen != c.expected {
			behind = append(behind, site+"="+short(seen))
		}
	}
	if len(behind) > 0 {
		return "failed", c.behind(behind)
	}
	return "passed", passedOn(worst)
}

func (c *checker) gaStep() (string, string) {
	code, page, worst := c.get(c.t.landing+"/", nil)
	if code != http.StatusOK {
		return "failed", "landing " + answered(code)
	}
	base, err := url.Parse(c.t.landing + "/")
	if err != nil {
		return "failed", "landing address is invalid"
	}
	var srcs []string
	seen := map[string]bool{}
	for _, m := range scriptSrc.FindAllStringSubmatch(string(page), -1) {
		if !seen[m[1]] && len(srcs) < 10 {
			seen[m[1]] = true
			srcs = append(srcs, m[1])
		}
	}
	if len(srcs) == 0 {
		return "failed", "landing names no script"
	}
	for _, src := range srcs {
		ref, err := url.Parse(src)
		if err != nil {
			return "failed", "landing names a bad script address"
		}
		code, js, try := c.get(base.ResolveReference(ref).String(), nil)
		worst = max(worst, try)
		if code != http.StatusOK {
			return "failed", "bundle " + answered(code)
		}
		if strings.Contains(string(js), gaID) {
			return "passed", passedOn(worst)
		}
	}
	return "failed", "landing bundle lacks " + gaID
}

// post sends one JSON POST with no retry; code is 0 when nothing answered.
func (c *checker) post(path string, payload any) (int, []byte) {
	raw, _ := json.Marshal(payload) // string maps cannot fail
	req, err := http.NewRequest(http.MethodPost, c.t.api+path, bytes.NewReader(raw))
	if err != nil {
		return 0, nil
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return 0, nil
	}
	return resp.StatusCode, body
}

func (c *checker) signInStep() (string, string) {
	email, password := c.getenv("PROD_PROBE_EMAIL"), c.getenv("PROD_PROBE_PASSWORD")
	if email == "" || password == "" {
		return "failed", secretsUnset
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "failed", "state could not be minted"
	}
	state := base64.RawURLEncoding.EncodeToString(raw)

	// ceiling: no sign-out, one GoTrue session a night; add sign-out to allowedPosts if the ~365 rows a year matter
	code, body := c.post("/auth/sign-in", map[string]string{"email": email, "password": password, "state": state})
	if code != http.StatusOK {
		return "failed", "sign-in " + answered(code)
	}
	var in struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(body, &in) != nil || in.Code == "" {
		return "failed", "sign-in answered 200 with no code"
	}
	code, body = c.post("/auth/exchange", map[string]string{"code": in.Code, "state": state})
	if code != http.StatusOK {
		return "failed", "exchange " + answered(code)
	}
	var tok struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if json.Unmarshal(body, &tok) != nil || tok.Access == "" || tok.Refresh == "" {
		return "failed", "exchange answered 200 without both tokens"
	}
	c.access = tok.Access
	return "passed", ""
}

func (c *checker) registrationStep() (string, string) {
	if c.access == "" {
		return "skipped", "sign-in did not pass"
	}
	parts := strings.Split(c.access, ".")
	if len(parts) != 3 {
		return "failed", "token is not a JWT"
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "failed", "token is not a JWT"
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(payload, &claims) != nil {
		return "failed", "token is not a JWT"
	}
	raw, ok := claims["user_metadata"]
	var meta map[string]json.RawMessage
	if !ok || json.Unmarshal(raw, &meta) != nil {
		return "failed", "token has no user_metadata claim"
	}
	if reg, set := meta["registration"]; set && string(bytes.TrimSpace(reg)) != "null" {
		return "failed", "user_metadata.registration is set"
	}
	return "passed", ""
}

func (c *checker) workspaceStep() (string, string) {
	if c.access == "" {
		return "skipped", "sign-in did not pass"
	}
	bearer := map[string]string{"Authorization": "Bearer " + c.access}
	var problems []string
	worst := 1
	for _, path := range workspaceReads {
		code, body, try := c.get(c.t.api+path, bearer)
		worst = max(worst, try)
		if code != http.StatusOK {
			problems = append(problems, path+" "+answered(code))
			continue
		}
		if path == "/api/tenancy/v1/me" {
			var me struct {
				Tenant *struct {
					ID string `json:"id"`
				} `json:"tenant"`
			}
			if json.Unmarshal(body, &me) != nil || me.Tenant == nil || me.Tenant.ID == "" {
				problems = append(problems, "/me has no tenant")
			}
		}
	}
	if len(problems) > 0 {
		return "failed", strings.Join(problems, "; ")
	}
	return "passed", passedOn(worst)
}
