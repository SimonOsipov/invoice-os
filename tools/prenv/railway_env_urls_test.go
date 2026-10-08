// railway_env_urls_test.go pins railway-env.sh discover-urls (one aliased domains read of the five
// public services) and the dev-env.yml urls step that publishes its stdout.
package main

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type urlsService struct {
	label, idVar, key, id string
	custom, generated     []string
}

// Each domain is unique, so a swapped alias shows in the output. Gateway has two custom
// domains, and its generated one is not the first listed.
var urlsServices = []urlsService{
	{"gateway", "RAILWAY_SVC_GATEWAY_ID", "gateway_url", "svc-gateway-urls", []string{"api.ascomply.test", "api-2.ascomply.test"}, []string{"gateway-pr-7.up.railway.app"}},
	{"app", "RAILWAY_SVC_APP_ID", "app_url", "svc-app-urls", nil, []string{"app-pr-7.up.railway.app"}},
	{"landing", "RAILWAY_SVC_LANDING_ID", "landing_url", "svc-landing-urls", []string{"www.ascomply.test"}, []string{"landing-pr-7.up.railway.app"}},
	{"ops-console", "RAILWAY_SVC_OPS_CONSOLE_ID", "ops_console_url", "svc-ops-console-urls", nil, []string{"ops-console-pr-7.up.railway.app"}},
	{"support-console", "RAILWAY_SVC_SUPPORT_CONSOLE_ID", "support_console_url", "svc-support-console-urls", nil, []string{"support-console-pr-7.up.railway.app"}},
}

const urlsProjectToken = "tok-project-sentinel-not-real"

func domainList(domains []string) []map[string]any {
	out := []map[string]any{}
	for _, d := range domains {
		out = append(out, map[string]any{"domain": d, "targetPort": nil})
	}
	return out
}

func domainsJSON(t *testing.T, custom, generated []string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"customDomains": domainList(custom), "serviceDomains": domainList(generated)})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// newURLsShim plants every service's domains; override[label] replaces one service's raw JSON.
func newURLsShim(t *testing.T, override map[string]string) authShim {
	t.Helper()
	s := newAuthShim(t, nil, nil)
	for _, svc := range urlsServices {
		body, ok := override[svc.label]
		if !ok {
			body = domainsJSON(t, svc.custom, svc.generated)
		}
		writeFile(t, filepath.Join(s.dir, "domains-"+svc.id+".json"), body)
	}
	return s
}

// urlsExports sets the five service ids but not the Postgres one: discover-urls needs only the five.
func urlsExports(apiToken, projectToken bool, unset ...string) string {
	var b strings.Builder
	if apiToken {
		b.WriteString("export RAILWAY_API_TOKEN=" + forkToken + "\n")
	}
	if projectToken {
		b.WriteString("export RAILWAY_PROJECT_TOKEN=" + urlsProjectToken + "\n")
	}
	b.WriteString("export RAILWAY_PROJECT_ID=" + forkProjectID + "\nexport RAILWAY_DEV_ENVIRONMENT_ID=" + persistentEnvironmentID + "\n")
	for _, svc := range urlsServices {
		if !slices.Contains(unset, svc.idVar) {
			b.WriteString("export " + svc.idVar + "=" + svc.id + "\n")
		}
	}
	return b.String()
}

func runURLs(t *testing.T, s authShim, exports, env string) (stdout, stderr string, code int) {
	t.Helper()
	return s.run(t, exports, "discover-urls", env)
}

func wantURLLines() []string {
	var out []string
	for _, svc := range urlsServices {
		d := svc.generated[0]
		if len(svc.custom) > 0 {
			d = svc.custom[0]
		}
		out = append(out, svc.key+"=https://"+d)
	}
	return out
}

// outputLine returns stdout's line for key, failing when there is none.
func outputLine(t *testing.T, stdout, key string) string {
	t.Helper()
	for _, l := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(l, key+"=") {
			return l
		}
	}
	t.Fatalf("stdout has no %s= line; stdout = %q", key, stdout)
	return ""
}

// requireOneDiscoverCall fails unless the shim saw exactly one request, a discoverUrls.
func requireOneDiscoverCall(t *testing.T, s authShim) railwayCall {
	t.Helper()
	calls := s.calls(t)
	if got := operations(calls); !slices.Equal(got, []string{"discoverUrls"}) {
		t.Fatalf("Railway calls = %v, want exactly [discoverUrls]", got)
	}
	return calls[0]
}

// requireURLsRefused asserts the all-or-nothing contract: exit 1, empty stdout, and a stderr diagnostic.
func requireURLsRefused(t *testing.T, stdout, stderr string, code int) {
	t.Helper()
	if code != 1 {
		t.Errorf("exit = %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on a refusal", stdout)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Errorf("stderr is empty; diagnostics go to stderr")
	}
}

var urlsAliasField = regexp.MustCompile(`(\w+)\s*:\s*domains\(([^)]*)\)`)

func TestDiscoverURLs_OneRequestForFiveDomains(t *testing.T) {
	s := newURLsShim(t, nil)
	stdout, stderr, code := runURLs(t, s, urlsExports(true, false), forkEnvID)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	c := requireOneDiscoverCall(t, s)
	if c.Variables["p"] != forkProjectID || c.Variables["e"] != forkEnvID {
		t.Errorf("variables p, e = %v, %v; want %s, %s", c.Variables["p"], c.Variables["e"], forkProjectID, forkEnvID)
	}
	ids := map[string]string{}
	for k, v := range c.Variables {
		if id, _ := v.(string); k != "p" && k != "e" {
			ids[id] = k
		}
	}
	for _, svc := range urlsServices {
		if _, ok := ids[svc.id]; !ok {
			t.Errorf("no variable carries the %s service id %s; variables = %v", svc.label, svc.id, c.Variables)
		}
	}
	// The fake ignores the selection set, so a missing customDomains or serviceDomains is only visible here.
	for _, list := range []string{"customDomains", "serviceDomains"} {
		if !regexp.MustCompile(list + `\s*\{[^}]*\bdomain\b`).MatchString(c.Query) {
			t.Errorf("the query does not select %s { domain }:\n%s", list, c.Query)
		}
	}
	fields := urlsAliasField.FindAllStringSubmatch(c.Query, -1)
	if len(fields) != 5 {
		t.Fatalf("the query has %d aliased domains(...) fields, want 5:\n%s", len(fields), c.Query)
	}
	seenAlias, seenVar := map[string]bool{}, map[string]bool{}
	for _, f := range fields {
		args := f[2]
		m := regexp.MustCompile(`serviceId:\s*\$(\w+)`).FindStringSubmatch(args)
		if m == nil || !regexp.MustCompile(`projectId:\s*\$p\b`).MatchString(args) || !regexp.MustCompile(`environmentId:\s*\$e\b`).MatchString(args) {
			t.Errorf("alias %s does not read projectId: $p, environmentId: $e and a serviceId variable: %q", f[1], args)
			continue
		}
		if seenAlias[f[1]] || seenVar[m[1]] {
			t.Errorf("alias %s or variable $%s is used twice; GraphQL rejects duplicate response keys", f[1], m[1])
		}
		seenAlias[f[1]], seenVar[m[1]] = true, true
	}
	if len(seenVar) != 5 {
		t.Errorf("the 5 fields read %d distinct service variables, want 5", len(seenVar))
	}
}

func TestDiscoverURLs_PrefersTheCustomDomain(t *testing.T) {
	s := newURLsShim(t, nil)
	stdout, stderr, code := runURLs(t, s, urlsExports(true, false), forkEnvID)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	for key, want := range map[string]string{"gateway_url": "https://api.ascomply.test", "landing_url": "https://www.ascomply.test"} {
		if got := outputLine(t, stdout, key); got != key+"="+want {
			t.Errorf("%s = %q, want the first custom domain %q (the generated one is also listed)", key, got, want)
		}
	}
}

func TestDiscoverURLs_FallsBackToTheGeneratedDomain(t *testing.T) {
	s := newURLsShim(t, nil)
	stdout, stderr, code := runURLs(t, s, urlsExports(true, false), forkEnvID)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	for key, want := range map[string]string{"app_url": "https://app-pr-7.up.railway.app", "support_console_url": "https://support-console-pr-7.up.railway.app"} {
		if got := outputLine(t, stdout, key); got != key+"="+want {
			t.Errorf("%s = %q, want the generated domain %q (customDomains is [])", key, got, want)
		}
	}
}

func TestDiscoverURLs_NullCustomDomainsRefuses(t *testing.T) {
	generated := `{"domain":"landing-pr-7.up.railway.app","targetPort":null}`
	for _, c := range []struct{ name, landing string }{
		{"customDomains is null", `{"customDomains":null,"serviceDomains":[` + generated + `]}`},
		{"customDomains is absent", `{"serviceDomains":[` + generated + `]}`},
		{"the whole alias is null", `null`},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newURLsShim(t, map[string]string{"landing": c.landing})
			stdout, stderr, code := runURLs(t, s, urlsExports(true, false), forkEnvID)
			requireOneDiscoverCall(t, s)
			requireURLsRefused(t, stdout, stderr, code)
			if !strings.Contains(stderr, "customDomains") {
				t.Errorf("stderr does not carry select_domain's customDomains refusal: %q", stderr)
			}
			if strings.Contains(stdout, "landing-pr-7") {
				t.Errorf("the generated domain was used although customDomains is unreadable: %q", stdout)
			}
		})
	}
}

func TestDiscoverURLs_PrintsExactlyFiveOutputLines(t *testing.T) {
	s := newURLsShim(t, nil)
	stdout, stderr, code := runURLs(t, s, urlsExports(true, false), forkEnvID)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if !slices.Equal(lines, wantURLLines()) {
		t.Errorf("stdout lines = %q, want exactly %q in this order", lines, wantURLLines())
	}
	for _, l := range lines {
		if !regexp.MustCompile(`^[a-z_]+_url=https://[A-Za-z0-9.-]+$`).MatchString(l) {
			t.Errorf("output line %q is not <key>_url=https://<host>", l)
		}
	}
}

func TestDiscoverURLs_NoDomainFailsNamingTheService(t *testing.T) {
	s := newURLsShim(t, map[string]string{"support-console": domainsJSON(t, nil, nil)})
	stdout, stderr, code := runURLs(t, s, urlsExports(true, false), forkEnvID)
	requireOneDiscoverCall(t, s)
	requireURLsRefused(t, stdout, stderr, code)
	want := "No domain found for support-console (service svc-support-console-urls) in environment " + forkEnvID +
		" — neither a custom domain nor a Railway-generated one. Every public service must have at least one."
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr does not carry today's message %q; stderr = %q", want, stderr)
	}
	if strings.Contains(stderr, "docs/") {
		t.Errorf("stderr points at a docs/ path; stderr = %q", stderr)
	}
}

func TestDiscoverURLs_GraphQLErrorEmptyStdout(t *testing.T) {
	const message = "sentinel-railway-message-7f3a"
	// beside plants errors next to a complete data; paths are errors[].path[0] values, one error each.
	beside := func(paths ...string) func(t *testing.T, s authShim) {
		return func(t *testing.T, s authShim) {
			var es []string
			for _, p := range paths {
				path := ""
				if p != "" {
					path = `"path":[` + p + `],`
				}
				es = append(es, `{"message":"`+message+`",`+path+`"extensions":{"code":"INTERNAL_SERVER_ERROR"}}`)
			}
			writeFile(t, filepath.Join(s.dir, "errors-discoverUrls.json"), "["+strings.Join(es, ",")+"]")
		}
	}
	for _, c := range []struct {
		name    string
		plant   func(t *testing.T, s authShim)
		named   []int // services (urlsServices index) the failure line must name
		wantEnv bool
	}{
		{"errors and no data", func(t *testing.T, s authShim) {
			writeFile(t, filepath.Join(s.dir, "faults-discoverUrls"), "gqlerr")
		}, nil, true},
		{"errors beside a complete data", beside(""), nil, true},
		{"a path names the service of the alias", beside(`"s2"`), []int{2}, false},
		{"two aliased errors name both services", beside(`"s0"`, `"s4"`), []int{0, 4}, false},
		{"an alias past the request names the environment", beside(`"s5"`), nil, true},
		{"a path that is no alias names the environment", beside(`"domains"`), nil, true},
		{"an in-range and an out-of-range alias name only the real service", beside(`"s1"`, `"s9"`), []int{1}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newURLsShim(t, nil)
			c.plant(t, s)
			stdout, stderr, code := runURLs(t, s, urlsExports(true, false), forkEnvID)
			requireOneDiscoverCall(t, s)
			requireURLsRefused(t, stdout, stderr, code)
			requireOneDiscoveryError(t, stderr)
			if c.wantEnv && !strings.Contains(stderr, forkEnvID) {
				t.Errorf("a failure with no alias path names the environment %s; stderr = %q", forkEnvID, stderr)
			}
			if len(c.named) == 0 && !c.wantEnv {
				t.Fatal("control: a case must name a service or the environment")
			}
			for i, svc := range urlsServices {
				if got, want := namesService(stderr, svc.label), slices.Contains(c.named, i); got != want {
					t.Errorf("stderr names %s = %v, want %v; stderr = %q", svc.label, got, want, stderr)
				}
			}
			// [read-error-naming]: Railway's message never reaches a failure line.
			for _, leaked := range []string{message, "Not Authorized"} {
				if strings.Contains(stdout+stderr, leaked) {
					t.Errorf("output carries Railway's message %q: %q", leaked, stdout+stderr)
				}
			}
		})
	}
}

func TestDiscoverURLs_MissingServiceIDRefusesBeforeAnyCall(t *testing.T) {
	cases := []struct{ name, exports, idVar string }{}
	for _, svc := range urlsServices {
		cases = append(cases, struct{ name, exports, idVar string }{svc.label + " unset", urlsExports(true, false, svc.idVar), svc.idVar})
	}
	cases = append(cases, struct{ name, exports, idVar string }{"landing empty", urlsExports(true, false) + "export RAILWAY_SVC_LANDING_ID=\n", "RAILWAY_SVC_LANDING_ID"})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newURLsShim(t, nil)
			s.requireLogs(t)
			before := len(s.calls(t))
			stdout, stderr, code := runURLs(t, s, c.exports, forkEnvID)
			requireURLsRefused(t, stdout, stderr, code)
			if got := len(s.calls(t)); got != before {
				t.Errorf("%d Railway call(s) before the service id guard refused", got-before)
			}
			if !strings.Contains(stderr, c.idVar) {
				t.Errorf("stderr does not name the missing %s: %q", c.idVar, stderr)
			}
		})
	}
}

func TestDiscoverURLs_ProjectTokenUsesProjectAccessTokenHeader(t *testing.T) {
	for _, c := range []struct {
		name                 string
		api, project         bool
		wantHeader, noHeader string
	}{
		{"dispatch: only the project token", false, true, "Project-Access-Token: " + urlsProjectToken, "Authorization:"},
		{"both tokens: the account token wins", true, true, "Authorization: Bearer " + forkToken, "Project-Access-Token:"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newURLsShim(t, nil)
			stdout, stderr, code := runURLs(t, s, urlsExports(c.api, c.project), forkEnvID)
			if code != 0 {
				t.Fatalf("exit = %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
			}
			requireOneDiscoverCall(t, s)
			argv := s.argv(t)
			if !strings.Contains(argv, c.wantHeader) {
				t.Errorf("curl argv has no %q: %s", c.wantHeader, argv)
			}
			if strings.Contains(argv, c.noHeader) {
				t.Errorf("curl argv carries %q: %s", c.noHeader, argv)
			}
		})
	}
}

func TestDiscoverURLs_NoTokenMakesNoCall(t *testing.T) {
	s := newURLsShim(t, nil)
	s.requireLogs(t)
	before := len(s.calls(t))
	stdout, stderr, code := runURLs(t, s, urlsExports(false, false), forkEnvID)
	requireURLsRefused(t, stdout, stderr, code)
	if got := len(s.calls(t)); got != before {
		t.Errorf("%d Railway call(s) with no token", got-before)
	}
	if !strings.Contains(stderr, "RAILWAY_API_TOKEN") {
		t.Errorf("stderr does not name the missing token: %q", stderr)
	}
}

func TestDiscoverURLs_ReadsThePersistentEnvironment(t *testing.T) {
	s := newURLsShim(t, nil)
	stdout, stderr, code := runURLs(t, s, urlsExports(true, false), persistentEnvironmentID)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (the subcommand only reads); stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if e := requireOneDiscoverCall(t, s).Variables["e"]; e != persistentEnvironmentID {
		t.Errorf("the request's e = %v, want the persistent environment %s", e, persistentEnvironmentID)
	}
	if lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n"); !slices.Equal(lines, wantURLLines()) {
		t.Errorf("stdout lines = %q, want %q", lines, wantURLLines())
	}
}

// wrapData is a response body whose data is the given aliases, in key order or reversed.
func wrapData(d map[string]string, backwards bool) string {
	keys := slices.Sorted(maps.Keys(d))
	if backwards {
		slices.Reverse(keys)
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, `"`+k+`":`+d[k])
	}
	return `{"data":{` + strings.Join(parts, ",") + `}}`
}

// goodAliases is the five aliases s0..s4 in urlsServices order; override[i] replaces alias i.
func goodAliases(t *testing.T, override map[int]string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for i, svc := range urlsServices {
		body, ok := override[i]
		if !ok {
			body = domainsJSON(t, svc.custom, svc.generated)
		}
		out["s"+strconv.Itoa(i)] = body
	}
	return out
}

func TestDiscoverURLs_VerbatimResponses(t *testing.T) {
	gen := func(i int) []string { return urlsServices[i].generated }
	extra := goodAliases(t, nil)
	extra["s5"] = domainsJSON(t, []string{"stray.ascomply.test"}, nil)
	extra["s99"] = `null`
	extra["gateway"] = domainsJSON(t, []string{"named-alias.ascomply.test"}, nil)
	cases := []struct {
		name    string
		data    map[string]string
		reverse bool
		want    map[string]string // key -> host, applied over wantURLLines
	}{
		{"an alias the request never sent is ignored", extra, false, nil},
		{"aliases in reverse key order still map by alias", goodAliases(t, nil), true, nil},
		{"a duplicated custom domain takes the first", goodAliases(t, map[int]string{
			0: domainsJSON(t, []string{"dup.ascomply.test", "dup.ascomply.test"}, gen(0)),
		}), false, map[string]string{"gateway_url": "dup.ascomply.test"}},
		{"the same host custom and generated prints once", goodAliases(t, map[int]string{
			1: domainsJSON(t, []string{"app-pr-7.up.railway.app"}, gen(1)),
		}), false, nil},
		{"uppercase, punycode, trailing-dot and numeric hostnames pass unchanged", goodAliases(t, map[int]string{
			0: domainsJSON(t, []string{"API-Prod.Example.COM"}, gen(0)),
			1: domainsJSON(t, []string{"xn--bcher-kva.example.com."}, gen(1)),
			2: domainsJSON(t, nil, []string{"3m-9.up.railway.app"}),
		}), false, map[string]string{
			"gateway_url": "API-Prod.Example.COM", "app_url": "xn--bcher-kva.example.com.", "landing_url": "3m-9.up.railway.app",
		}},
		{"a generated list of two takes the first", goodAliases(t, map[int]string{
			3: domainsJSON(t, nil, []string{"ops-first.up.railway.app", "ops-second.up.railway.app"}),
		}), false, map[string]string{"ops_console_url": "ops-first.up.railway.app"}},
		{"a domain object with an extra field and a port", goodAliases(t, map[int]string{
			4: `{"customDomains":[],"serviceDomains":[{"domain":"support-x.up.railway.app","targetPort":8080,"id":"d-1","__typename":"ServiceDomain"}],"__typename":"AllDomains"}`,
		}), false, map[string]string{"support_console_url": "support-x.up.railway.app"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newURLsShim(t, nil)
			if len(c.data) < 5 {
				t.Fatalf("control: %d aliases planted, want at least 5", len(c.data))
			}
			writeFile(t, filepath.Join(s.dir, "discoverUrls.json"), wrapData(c.data, c.reverse))
			stdout, stderr, code := runURLs(t, s, urlsExports(true, false), forkEnvID)
			if code != 0 {
				t.Fatalf("exit = %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
			}
			requireOneDiscoverCall(t, s)
			want := wantURLLines()
			for i, svc := range urlsServices {
				if h, ok := c.want[svc.key]; ok {
					want[i] = svc.key + "=https://" + h
				}
			}
			if lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n"); !slices.Equal(lines, want) {
				t.Errorf("stdout lines = %q, want %q", lines, want)
			}
		})
	}
}

func TestDiscoverURLs_UnusableResponseRefuses(t *testing.T) {
	good := func(t *testing.T) map[string]string { return goodAliases(t, nil) }
	without := func(t *testing.T, k string) map[string]string {
		d := good(t)
		delete(d, k)
		return d
	}
	cases := []struct {
		name string
		body func(t *testing.T) string // the whole response; data cases go through plantDiscover
	}{
		{"a 200 that is not JSON", func(*testing.T) string { return "<html>bad gateway</html>" }},
		{"an empty 200", func(*testing.T) string { return "" }},
		{"no data and no errors", func(*testing.T) string { return `{}` }},
		{"data null", func(*testing.T) string { return `{"data":null}` }},
		{"data a string", func(*testing.T) string { return `{"data":"oops"}` }},
		{"a top-level array", func(*testing.T) string { return `[]` }},
		{"the first alias is missing", func(t *testing.T) string { return wrapData(without(t, "s0"), false) }},
		{"the last alias is missing", func(t *testing.T) string { return wrapData(without(t, "s4"), false) }},
		{"an alias is an empty object", func(t *testing.T) string {
			d := good(t)
			d["s3"] = `{}`
			return wrapData(d, false)
		}},
		{"an alias is a string", func(t *testing.T) string {
			d := good(t)
			d["s1"] = `"app-pr-7.up.railway.app"`
			return wrapData(d, false)
		}},
		{"customDomains is a string", func(t *testing.T) string {
			d := good(t)
			d["s2"] = `{"customDomains":"www.ascomply.test","serviceDomains":[]}`
			return wrapData(d, false)
		}},
		{"serviceDomains is null and customDomains empty", func(t *testing.T) string {
			d := good(t)
			d["s4"] = `{"customDomains":[],"serviceDomains":null}`
			return wrapData(d, false)
		}},
	}
	// A chosen domain with no host: refused, naming the service (urlsServices index 1, app).
	hostless := map[string]string{
		"an empty custom domain":                  `{"customDomains":[{"domain":""}],"serviceDomains":[]}`,
		"a custom domain with no domain field":    `{"customDomains":[{"targetPort":1}],"serviceDomains":[]}`,
		"a null custom domain":                    `{"customDomains":[{"domain":null}],"serviceDomains":[]}`,
		"an empty generated domain":               `{"customDomains":[],"serviceDomains":[{"domain":""}]}`,
		"a generated domain with no domain field": `{"customDomains":[],"serviceDomains":[{"targetPort":1}]}`,
		"a numeric domain":                        `{"customDomains":[{"domain":123}],"serviceDomains":[]}`,
		"a boolean domain":                        `{"customDomains":[{"domain":true}],"serviceDomains":[]}`,
		"a domain with an inner space":            `{"customDomains":[{"domain":"app .test"}],"serviceDomains":[]}`,
		"a newline between hostname characters":   `{"customDomains":[{"domain":"a.test\nb.test"}],"serviceDomains":[]}`,
		"a domain with an embedded newline":       `{"customDomains":[{"domain":"a.test\nevil_url=https://x"}],"serviceDomains":[]}`,
	}
	for _, name := range slices.Sorted(maps.Keys(hostless)) {
		app := hostless[name]
		cases = append(cases, struct {
			name string
			body func(t *testing.T) string
		}{name, func(t *testing.T) string {
			d := good(t)
			d["s1"] = app
			return wrapData(d, false)
		}})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newURLsShim(t, nil)
			writeFile(t, filepath.Join(s.dir, "discoverUrls.json"), c.body(t))
			stdout, stderr, code := runURLs(t, s, urlsExports(true, false), forkEnvID)
			requireOneDiscoverCall(t, s)
			requireURLsRefused(t, stdout, stderr, code)
			if _, ok := hostless[c.name]; ok && !namesService(stderr, urlsServices[1].label) {
				t.Errorf("stderr does not name %s, the service whose domain has no host: %q", urlsServices[1].label, stderr)
			}
		})
	}
}

// requireOneDiscoveryError fails unless stderr holds exactly one ::error:: line and it reads as English.
func requireOneDiscoveryError(t *testing.T, stderr string) {
	t.Helper()
	var errs []string
	for _, l := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(l, "::error::") {
			errs = append(errs, l)
		}
	}
	if len(errs) != 1 {
		t.Errorf("stderr has %d ::error:: lines, want exactly 1: %q", len(errs), errs)
	}
	if strings.Contains(stderr, "Could not discovering") {
		t.Errorf("stderr reads \"Could not discovering\": %q", stderr)
	}
}

func TestDiscoverURLs_TransportFailuresKeepQueryRetryRules(t *testing.T) {
	const cmd = "discover-urls"
	cases := []struct {
		name    string
		faults  []string
		hdr     []string
		calls   int
		code    int
		logRows []string
		says    []string // patterns the one error line must match: the wording of TestRailwayAPI_RateLimitOverSixHundredFailsAtOnce
	}{
		{"a 503 then success is one counted success", []string{"503"}, nil, 2, 0, []string{cmd + "\t1\ttransient", cmd + "\t2\tok"}, nil},
		{"a timeout then success", []string{"timeout"}, nil, 2, 0, []string{cmd + "\t1\ttransient", cmd + "\t2\tok"}, nil},
		{"a 429 then success", []string{"429"}, []string{"retry-after: 1"}, 2, 0, []string{cmd + "\t1\tratelimit", cmd + "\t2\tok"}, nil},
		{"three 503s exhaust the budget", []string{"503", "503", "503"}, nil, 3, 1, []string{cmd + "\t1\ttransient", cmd + "\t2\ttransient", cmd + "\t3\ttransient"}, nil},
		{"a 401 is not retried", []string{"401"}, nil, 1, 1, []string{cmd + "\t1\tfatal"}, nil},
		{"a 400 is not retried", []string{"400"}, nil, 1, 1, []string{cmd + "\t1\tfatal"}, nil},
		{"a second 429 is not retried", []string{"429", "429"}, []string{"retry-after: 1"}, 2, 1, []string{cmd + "\t1\tratelimit", cmd + "\t2\tratelimit"}, nil},
		{"a second 429 names the wait", []string{"429", "429"}, []string{"retry-after: 30"}, 2, 1, []string{cmd + "\t1\tratelimit", cmd + "\t2\tratelimit"}, []string{`429`, `(?i)second`, `\b30\b`}},
		{"a 429 over 600 s names the wait", []string{"429"}, []string{"retry-after: 601"}, 1, 1, []string{cmd + "\t1\tratelimit"}, []string{`429`, `\b601\b`, `\b600\b`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newURLsShim(t, nil)
			setFaults(t, s, "discoverUrls", c.faults...)
			if len(c.hdr) > 0 {
				plantRateLimitHeaders(t, s, c.hdr...)
			}
			tmp := t.TempDir()
			stdout, stderr, code := runURLs(t, s, urlsExports(true, false)+runnerTempExport(tmp), forkEnvID)
			if got := opCount(t, s, "discoverUrls"); got != c.calls || len(s.calls(t)) != c.calls {
				t.Errorf("discoverUrls requests = %d of %d, want %d", got, len(s.calls(t)), c.calls)
			}
			if code != c.code {
				t.Errorf("exit = %d, want %d; stdout = %q, stderr = %q", code, c.code, stdout, stderr)
			}
			if c.code == 0 {
				if lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n"); !slices.Equal(lines, wantURLLines()) {
					t.Errorf("stdout lines = %q, want %q", lines, wantURLLines())
				}
			} else {
				requireURLsRefused(t, stdout, stderr, code)
				if !strings.Contains(stderr, forkEnvID) {
					t.Errorf("stderr does not name the environment %s: %q", forkEnvID, stderr)
				}
				requireOneDiscoveryError(t, stderr)
				for _, re := range c.says {
					if !regexp.MustCompile(re).MatchString(errorLines(stderr)) {
						t.Errorf("the error line does not match %s: %q", re, errorLines(stderr))
					}
				}
			}
			if rows := callLogRows(t, tmp); !slices.Equal(rows, c.logRows) {
				t.Errorf("call log = %q, want %q", rows, c.logRows)
			}
			for _, leaked := range []string{"Problem processing request", "Not Authorized"} {
				if strings.Contains(stdout+stderr, leaked) {
					t.Errorf("output carries Railway's message %q: %q", leaked, stdout+stderr)
				}
			}
		})
	}
}

func TestDiscoverURLs_NoTokenOrBodyInOutputOrArgv(t *testing.T) {
	for _, c := range []struct {
		name  string
		plant func(t *testing.T, s authShim)
		code  int
	}{
		{"success", func(*testing.T, authShim) {}, 0},
		{"a graphql error", func(t *testing.T, s authShim) { writeFile(t, filepath.Join(s.dir, "faults-discoverUrls"), "gqlerr") }, 1},
		{"a 401", func(t *testing.T, s authShim) { setFaults(t, s, "discoverUrls", "401") }, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newURLsShim(t, nil)
			c.plant(t, s)
			stdout, stderr, code := runURLs(t, s, urlsExports(true, true), forkEnvID)
			if code != c.code {
				t.Fatalf("control: exit = %d, want %d; stderr = %q", code, c.code, stderr)
			}
			argv := s.argv(t)
			if !strings.Contains(argv, "--data @-") || strings.Contains(argv, "--data {") || strings.Contains(argv, "discoverUrls") {
				t.Errorf("the request body reached curl's argv, or was not sent on stdin: %s", argv)
			}
			for label, n := range map[string]string{"the account token": forkToken, "the project token": urlsProjectToken} {
				if strings.Contains(stdout+stderr, n) {
					t.Errorf("stdout or stderr carries %s: %q", label, stdout+stderr)
				}
			}
		})
	}
}

func TestDiscoverURLs_UsageAndProjectGuards(t *testing.T) {
	t.Run("no environment id is a usage error with no call", func(t *testing.T) {
		s := newURLsShim(t, nil)
		s.requireLogs(t)
		before := len(s.calls(t))
		stdout, stderr, code := s.run(t, urlsExports(true, false), "discover-urls")
		if code != 2 || stdout != "" || !strings.Contains(stderr, "usage: railway-env.sh discover-urls <environment-id>") {
			t.Errorf("exit = %d, stdout = %q, stderr = %q; want 2, empty, the usage line", code, stdout, stderr)
		}
		if got := len(s.calls(t)); got != before {
			t.Errorf("%d Railway call(s) with no environment id", got-before)
		}
	})
	t.Run("an unset project id refuses before any call", func(t *testing.T) {
		s := newURLsShim(t, nil)
		s.requireLogs(t)
		before := len(s.calls(t))
		stdout, stderr, code := runURLs(t, s, strings.Replace(urlsExports(true, false), "export RAILWAY_PROJECT_ID="+forkProjectID+"\n", "unset RAILWAY_PROJECT_ID\n", 1), forkEnvID)
		requireURLsRefused(t, stdout, stderr, code)
		if got := len(s.calls(t)); got != before {
			t.Errorf("%d Railway call(s) with no project id", got-before)
		}
		if !strings.Contains(stderr, "RAILWAY_PROJECT_ID") {
			t.Errorf("stderr does not name RAILWAY_PROJECT_ID: %q", stderr)
		}
	})
}

// jobOutputs returns a job's outputs: map, key to expression.
func jobOutputs(j workflowJob) map[string]string {
	out := map[string]string{}
	in := false
	for _, line := range j.lines {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
		case indent == 4 && trimmed == "outputs:":
			in = true
		case indent <= 4:
			in = false
		case in && indent == 6:
			if k, v, ok := strings.Cut(trimmed, ":"); ok {
				out[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	}
	return out
}

func TestDevEnvYmlURLsStepWritesTheFiveOutputs(t *testing.T) {
	yml := readWorkflow(t, "dev-env.yml")
	var prep *workflowJob
	for _, j := range workflowJobsOf(yml) {
		if j.name == "prepare-env" {
			prep = &j
		}
	}
	if prep == nil {
		t.Fatal("control: workflowJobsOf finds no prepare-env job in dev-env.yml")
	}
	steps := prep.steps()
	hasResolve := false
	var urls []workflowStep
	for _, s := range steps {
		hasResolve = hasResolve || s.keys["id"] == "resolve"
		if s.keys["id"] == "urls" {
			urls = append(urls, s)
		}
	}
	if len(steps) < 10 || !hasResolve {
		t.Fatalf("control: prepare-env parsed to %d steps (resolve found: %v), want >= 10 and a resolve step", len(steps), hasResolve)
	}
	if len(urls) != 1 {
		t.Fatalf("prepare-env has %d steps with id: urls, want 1", len(urls))
	}
	s := urls[0]

	if v, ok := s.keys["if"]; ok {
		t.Errorf("the urls step has if: %q; push and dispatch read the persistent environment's URLs", v)
	}
	if v, ok := s.keys["continue-on-error"]; ok {
		t.Errorf("the urls step has continue-on-error: %q", v)
	}
	wantEnv := map[string]string{
		"RAILWAY_API_TOKEN":     "${{ github.event_name != 'workflow_dispatch' && secrets.RAILWAY_API_TOKEN || '' }}",
		"RAILWAY_PROJECT_TOKEN": "${{ github.event_name == 'workflow_dispatch' && secrets.RAILWAY_API_DEV_TOKEN || '' }}",
		"ENV_ID":                "${{ steps.resolve.outputs.environment_id }}",
	}
	if !reflect.DeepEqual(s.env, wantEnv) {
		t.Errorf("the urls step's env = %v, want %v", s.env, wantEnv)
	}

	run := s.keys["run"]
	if run == "" {
		t.Fatal("the urls step has no run block")
	}
	call := regexp.MustCompile(`(?m)^(\w+)=\$\(bash scripts/ci/railway-env\.sh discover-urls "\$ENV_ID"\)$`).FindStringSubmatch(run)
	if call == nil {
		t.Errorf("the urls run does not assign out=$(bash scripts/ci/railway-env.sh discover-urls \"$ENV_ID\"):\n%s", run)
	} else {
		// Quoted: an unquoted expansion folds the 5 lines into one.
		name := regexp.QuoteMeta(call[1])
		appended := regexp.MustCompile(`(?m)^(echo|printf)\b.*"\$\{?` + name + `\}?".*>> "\$GITHUB_OUTPUT"$`)
		loc := appended.FindStringIndex(run)
		if loc == nil {
			t.Errorf(`the urls run does not append "$%s" to "$GITHUB_OUTPUT":`+"\n%s", call[1], run)
		} else if at := strings.Index(run, "discover-urls"); at < 0 || at > loc[0] {
			t.Errorf("the urls run appends %s to $GITHUB_OUTPUT before it is assigned:\n%s", call[1], run)
		}
	}
	if n := strings.Count(run, "discover-urls"); n != 1 {
		t.Errorf("the urls run names discover-urls %d times, want 1", n)
	}
	for _, gone := range []string{"fetch_domain", "railway-env.sh query", "select-domain", "domains(projectId", "|| true"} {
		if strings.Contains(run, gone) {
			t.Errorf("the urls run still carries %q:\n%s", gone, run)
		}
	}

	outputs := jobOutputs(*prep)
	for _, svc := range urlsServices {
		if want := "${{ steps.urls.outputs." + svc.key + " }}"; outputs[svc.key] != want {
			t.Errorf("prepare-env outputs.%s = %q, want %q", svc.key, outputs[svc.key], want)
		}
		if n := strings.Count(yml, "needs.prepare-env.outputs."+svc.key); n < 1 {
			t.Errorf("control: no later job reads needs.prepare-env.outputs.%s", svc.key)
		}
	}
}
