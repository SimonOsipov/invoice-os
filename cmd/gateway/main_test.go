// main_test.go: role-password rename fallback shim tests (M4-22-09/task-168).
// cmd/gateway/ had no test files before this one (main() itself isn't
// unit-testable -- it calls platform.Fatal and opens a real listener).
// Deliberately does NOT re-author TestBootstrapRejectsEmptyPasswords
// (internal/platform/db/bootstrap_test.go) or
// TestGatewayMainPassesRawEnvironmentToProvisioningGuard
// (internal/platform/db/provision_test.go, AC #6's named regression guard)
// -- both already cover what their names say.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"golang.org/x/net/html"
)

// TestGatewayMainPrefersUnprefixedPasswordVars: Test Spec #1. Static
// source-scan of main.go's RolePasswords literal. Deprecated var names are
// built via ToUpper(prefix) + suffix, never as a literal substring -- this
// file is itself grepped by TestRepoHasNoStrayInvoicePrefixedVars below
// (AC #4), and a literal deprecated "*PASSWORD" string here would trip it.
func TestGatewayMainPrefersUnprefixedPasswordVars(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read cmd/gateway/main.go: %v", err)
	}
	src := string(b)

	idx := strings.Index(src, "Passwords: db.RolePasswords{")
	if idx == -1 {
		t.Fatal(`cmd/gateway/main.go no longer builds Passwords via a "Passwords: db.RolePasswords{" literal -- this test's anchor moved`)
	}
	end := idx + 500
	if end > len(src) {
		end = len(src)
	}
	window := src[idx:end]

	deprecatedPrefix := strings.ToUpper("invoice_")

	for _, tc := range []struct {
		field     string
		newName   string
		oldSuffix string
	}{
		{"Migrator", `"MIGRATOR_PASSWORD"`, "MIGRATOR_PASSWORD"},
		{"App", `"APP_PASSWORD"`, "APP_PASSWORD"},
		{"Reader", `"READER_PASSWORD"`, "TENANT_READER_PASSWORD"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			oldName := `"` + deprecatedPrefix + tc.oldSuffix + `"`

			newIdx := strings.Index(window, tc.newName)
			oldIdx := strings.Index(window, oldName)

			if newIdx == -1 {
				t.Errorf("%s: preferred var %s is not read anywhere in the RolePasswords literal:\n%s", tc.field, tc.newName, window)
			}
			if oldIdx == -1 {
				t.Errorf("%s: deprecated fallback var %s is not present in the RolePasswords literal:\n%s", tc.field, oldName, window)
			}
			if newIdx != -1 && oldIdx != -1 && newIdx > oldIdx {
				t.Errorf("%s: preferred var %s must be read before deprecated fallback %s (new name wins) -- window:\n%s", tc.field, tc.newName, oldName, window)
			}

			// The deprecated name must never be the sole, unconditional
			// os.Getenv(...) argument feeding the field directly -- that would
			// mean the field is populated straight from the deprecated
			// variable with no resolution/fallback logic at all.
			bareOldRead := tc.field + ": os.Getenv(" + oldName + ")"
			if strings.Contains(window, bareOldRead) {
				t.Errorf("%s is populated by a bare os.Getenv(%s) with no resolution/fallback to the preferred name -- window:\n%s", tc.field, oldName, window)
			}
		})
	}
}

// TestGatewayMainWiresEachRoleToItsOwnVarPair: adversarial coverage.
// TestGatewayMainPrefersUnprefixedPasswordVars above only proves each
// var-name pair appears somewhere in the RolePasswords window, in order --
// it can't tell one field's resolveRolePassword call from another's.
// Mutation-verified: swapping Migrator/App's arguments left that test green
// while silently misconfiguring both roles' passwords on every boot. This
// test requires the exact literal call, field name included, per role.
func TestGatewayMainWiresEachRoleToItsOwnVarPair(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read cmd/gateway/main.go: %v", err)
	}
	src := string(b)

	// Built by concatenation, not as a literal, for the same self-grep reason
	// documented on TestGatewayMainPrefersUnprefixedPasswordVars above.
	deprecatedPrefix := strings.ToUpper("invoice_")

	for _, tc := range []struct {
		field     string
		newName   string
		oldSuffix string
	}{
		{"Migrator", "MIGRATOR_PASSWORD", "MIGRATOR_PASSWORD"},
		{"App", "APP_PASSWORD", "APP_PASSWORD"},
		{"Reader", "READER_PASSWORD", "TENANT_READER_PASSWORD"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			oldName := deprecatedPrefix + tc.oldSuffix
			// \s+ (not a literal single space) because gofmt column-aligns
			// these three struct-literal lines, padding the shorter field
			// names ("App:", "Reader:") with extra spaces to match
			// "Migrator:"'s width.
			pattern := regexp.QuoteMeta(tc.field+":") + `\s*` + regexp.QuoteMeta(`resolveRolePassword("`+tc.newName+`", "`+oldName+`", app.Logger),`)
			re := regexp.MustCompile(pattern)
			if !re.MatchString(src) {
				t.Errorf("cmd/gateway/main.go does not contain the exact wiring %q -- the %s field must resolve from its own (%s, %s) var pair, not a swapped or mismatched one", pattern, tc.field, tc.newName, oldName)
			}
		})
	}
}

// TestRepoHasNoStrayInvoicePrefixedVars: Test Spec #3 / AC #4. Walks every
// git-tracked file (git ls-files) and enforces the bounded blast radius:
// every deprecated-prefix "*PASSWORD" hit must live in cmd/gateway/main.go
// and nowhere else; every deprecated-prefix "*DATABASE_URL" hit must be
// zero. Those two DSN vars are Railway-console-only, no Go code reads them.
func TestRepoHasNoStrayInvoicePrefixedVars(t *testing.T) {
	rootOut, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	root := strings.TrimSpace(string(rootOut))

	filesOut, err := exec.Command("git", "-C", root, "ls-files").Output()
	if err != nil {
		t.Fatalf("git -C %s ls-files: %v", root, err)
	}
	files := strings.Split(strings.TrimSpace(string(filesOut)), "\n")

	// See TestGatewayMainPrefersUnprefixedPasswordVars's doc comment above:
	// built the same way, for the same self-grep reason.
	deprecatedPrefix := strings.ToUpper("invoice_")
	passwordPattern := regexp.MustCompile(deprecatedPrefix + `.*PASSWORD`)
	dsnPattern := regexp.MustCompile(deprecatedPrefix + `.*DATABASE_URL`)

	const wantPasswordFile = "cmd/gateway/main.go"
	var passwordHitsElsewhere []string
	var dsnHits []string

	for _, rel := range files {
		if rel == "" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			// Broken symlink, submodule gitlink, or similar: irrelevant to a
			// plain-text content grep, so skip rather than fail the suite.
			continue
		}
		text := string(content)
		if passwordPattern.MatchString(text) && rel != wantPasswordFile {
			passwordHitsElsewhere = append(passwordHitsElsewhere, rel)
		}
		if dsnPattern.MatchString(text) {
			dsnHits = append(dsnHits, rel)
		}
	}

	if len(passwordHitsElsewhere) > 0 {
		t.Errorf("deprecated *PASSWORD var referenced outside %s (want none): %v", wantPasswordFile, passwordHitsElsewhere)
	}
	if len(dsnHits) > 0 {
		t.Errorf("deprecated *DATABASE_URL var referenced, want zero hits repo-wide: %v", dsnHits)
	}
}

// TestRolePasswordResolutionPrecedence: Test Spec #4 (table-driven).
// Exercises resolveRolePassword with synthetic env var names, not the real
// MIGRATOR_PASSWORD/etc. triples: its logic is generic over whatever two
// names it's given, so a fixture pair proves resolution/precedence/warning
// behavior without coupling to production names (and sidesteps the same
// self-grep concern above, since fixture names contain neither "PASSWORD"
// nor the deprecated prefix). TestGatewayMainPrefersUnprefixedPasswordVars
// proves main.go calls this with the three real pairs, in order.
//
// Case 3 (both set) still warns even though the deprecated value goes
// unused -- an operator should still know to clean up the stale var.
func TestRolePasswordResolutionPrecedence(t *testing.T) {
	const (
		newVar = "GATEWAY_TEST_ROLE_PW_NEW"
		oldVar = "GATEWAY_TEST_ROLE_PW_OLD"
	)

	cases := []struct {
		name      string
		newVal    string
		oldVal    string
		wantValue string
		wantWarn  bool
	}{
		{
			name:      "new set, old unset",
			newVal:    "new-secret",
			oldVal:    "",
			wantValue: "new-secret",
			wantWarn:  false,
		},
		{
			name:      "new unset, old set",
			newVal:    "",
			oldVal:    "old-secret",
			wantValue: "old-secret",
			wantWarn:  true,
		},
		{
			name:      "both set: new wins, old ignored but still flagged for cleanup",
			newVal:    "new-secret",
			oldVal:    "old-secret",
			wantValue: "new-secret",
			wantWarn:  true,
		},
		{
			name:      "neither set: empty, no warning (fail-fast is validateRolePasswords' job, not this function's)",
			newVal:    "",
			oldVal:    "",
			wantValue: "",
			wantWarn:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(newVar, tc.newVal)
			t.Setenv(oldVar, tc.oldVal)

			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, nil))

			got := resolveRolePassword(newVar, oldVar, logger)
			if got != tc.wantValue {
				t.Errorf("resolveRolePassword(%q, %q, ...) = %q, want %q", newVar, oldVar, got, tc.wantValue)
			}

			logged := buf.String()
			if !tc.wantWarn {
				if logged != "" {
					t.Errorf("expected no warning logged, got: %s", logged)
				}
				return
			}

			if logged == "" {
				t.Fatalf("expected a deprecation warning to be logged naming %s and %s, got none", oldVar, newVar)
			}
			var entry map[string]any
			if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
				t.Fatalf("log line is not valid JSON: %v\nraw: %s", err, logged)
			}
			if level, _ := entry["level"].(string); level != "WARN" {
				t.Errorf("log level = %q, want WARN", level)
			}
			msg, _ := entry["msg"].(string)
			if !strings.Contains(msg, oldVar) {
				t.Errorf("warning message %q does not name the deprecated variable %s", msg, oldVar)
			}
			if !strings.Contains(msg, newVar) {
				t.Errorf("warning message %q does not name the replacement variable %s", msg, newVar)
			}
			if !strings.Contains(strings.ToLower(msg), "deprecated") {
				t.Errorf("warning message %q does not say the variable is deprecated, so an operator reading Railway logs would not know it needs cleanup", msg)
			}
		})
	}
}

// --- EXTR-17-06: docling is probed, never routed ---

// setUpstreamEnv points every routed and probed service at addr, so
// loadUpstreams succeeds and the maps it returns can be compared.
func setUpstreamEnv(t *testing.T, addr string) {
	t.Helper()
	for _, svc := range append(append([]string{}, routedServices...), probedServices...) {
		t.Setenv(strings.ToUpper(svc)+"_URL", addr)
	}
}

// TestLoadUpstreamsSeparatesRoutedFromProbed: routing is a capability the
// gateway withholds by type. A probed upstream must never appear in the map
// that becomes proxy routes.
func TestLoadUpstreamsSeparatesRoutedFromProbed(t *testing.T) {
	if len(routedServices) == 0 || len(probedServices) == 0 {
		t.Fatalf("routedServices=%d probedServices=%d -- an empty list makes every assertion below vacuous", len(routedServices), len(probedServices))
	}
	setUpstreamEnv(t, "http://127.0.0.1:1")

	routed, probed, err := loadUpstreams()
	if err != nil {
		t.Fatalf("loadUpstreams: %v", err)
	}
	if len(routed) != len(routedServices) {
		t.Errorf("routed has %d entries, want %d", len(routed), len(routedServices))
	}
	if len(probed) != len(probedServices) {
		t.Errorf("probed has %d entries, want %d", len(probed), len(probedServices))
	}
	for _, svc := range probedServices {
		if _, ok := routed[svc]; ok {
			t.Errorf("%q is in the routed map -- it would get a public /api/%s/* proxy route", svc, svc)
		}
		if _, ok := probed[svc]; !ok {
			t.Errorf("%q is probed but loadUpstreams did not return it -- /healthz/fleet would never observe it", svc)
		}
	}
	for _, svc := range routedServices {
		if _, ok := routed[svc]; !ok {
			t.Errorf("%q is routed but loadUpstreams did not return it", svc)
		}
	}
	if !slices.Contains(probedServices, "docling") {
		t.Errorf("probedServices = %v, want it to carry `docling` -- the sidecar has no public domain, so the roll-up is CI's only view of it", probedServices)
	}
}

// TestLoadUpstreamsFailsLoudlyOnAMissingProbedURL: the accepted cost of probing
// through the gateway is that gateway boot now depends on DOCLING_URL. It must
// stay a named boot failure, not a silently skipped probe.
func TestLoadUpstreamsFailsLoudlyOnAMissingProbedURL(t *testing.T) {
	setUpstreamEnv(t, "http://127.0.0.1:1")
	t.Setenv("DOCLING_URL", "")

	_, _, err := loadUpstreams()
	if err == nil {
		t.Fatal("loadUpstreams succeeded with DOCLING_URL unset -- the gateway would come up reporting a fleet it cannot see")
	}
	if !strings.Contains(err.Error(), "DOCLING_URL") {
		t.Errorf("error %q does not name DOCLING_URL, so the boot log would not say which variable is missing", err)
	}
}

// TestGatewayHandlersPublishNoProxyRouteForAProbedService is the security half
// of this change, asserted on the one function that merges the two maps: a
// probed service is 404 under /api/, while a routed one reaches its proxy (502
// against a dead upstream proves it routed rather than 404'd).
func TestGatewayHandlersPublishNoProxyRouteForAProbedService(t *testing.T) {
	get, fleetNames := gatewayMux(t)

	// `auth` is named explicitly: its 404 is only meaningful once the roll-up below sees it.
	for _, svc := range append(slices.Clone(probedServices), "auth") {
		if got := get("/api/" + svc + "/x"); got != http.StatusNotFound {
			t.Errorf("GET /api/%s/x = %d, want 404 -- a probed sidecar is exposed as a public proxy route", svc, got)
		}
	}
	// Control: without this a mux that routes NOTHING would pass the loop above.
	if got := get("/api/" + routedServices[0] + "/x"); got != http.StatusBadGateway {
		t.Fatalf("GET /api/%s/x = %d, want 502 (routed to a dead upstream) -- the 404s above prove nothing if no service is routed at all", routedServices[0], got)
	}

	// The roll-up sees both lists: that is why probing through the gateway works.
	seen := fleetNames()
	for _, svc := range append(append([]string{"gateway", "auth"}, routedServices...), probedServices...) {
		if !seen[svc] {
			t.Errorf("/healthz/fleet omits %q -- the deploy gate cannot block on a service the roll-up never names", svc)
		}
	}
}

// nilURLSessions is a checker for tests whose tokens carry no session_id, so GoTrue is never asked.
func nilURLSessions() *gateway.SessionChecker {
	return gateway.NewSessionChecker(nil, nil, time.Now, slog.Default())
}

// gatewayMux serves gatewayHandlers over upstreams set by setUpstreamEnv to a dead
// address. get sends an authenticated GET; fleetNames decodes /healthz/fleet's names.
func gatewayMux(t *testing.T) (get func(path string) int, fleetNames func() map[string]bool) {
	t.Helper()
	issuer, err := auth.NewMockIssuer(mountTestIssuer)
	if err != nil {
		t.Fatalf("mock issuer: %v", err)
	}
	jwks := httptest.NewServer(issuer.JWKSHandler())
	t.Cleanup(jwks.Close)
	verifier, err := auth.NewVerifier(auth.Config{Issuer: mountTestIssuer, JWKSURL: jwks.URL})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	tok, err := issuer.Mint(auth.MintOptions{
		Subject:  "11111111-1111-1111-1111-111111111111",
		Role:     "authenticated",
		TenantID: "tenant-a",
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	setUpstreamEnv(t, "http://127.0.0.1:1")
	routed, probed, err := loadUpstreams()
	if err != nil {
		t.Fatalf("loadUpstreams: %v", err)
	}

	apiHandler, fleetHandler := gatewayHandlers(verifier, nilURLSessions(), routed, probed, nil, slog.Default(), "gw-test-token")
	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandler)
	mux.HandleFunc("GET /healthz/fleet", fleetHandler)

	get = func(path string) int {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec.Code
	}
	fleetNames = func() map[string]bool {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz/fleet", nil))
		var fleet struct {
			Services []struct {
				Name string `json:"name"`
			} `json:"services"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &fleet); err != nil {
			t.Fatalf("decode /healthz/fleet: %v (body %q)", err, rec.Body.String())
		}
		seen := map[string]bool{}
		for _, s := range fleet.Services {
			seen[s.Name] = true
		}
		if len(seen) == 0 {
			t.Fatalf("/healthz/fleet names no service (body %q)", rec.Body.String())
		}
		return seen
	}
	return get, fleetNames
}

const mountTestIssuer = "https://mock.ascomply.test"

// TestGatewayMainFatalsOnAnUpstreamError: loadUpstreams' loud failure is only
// loud if main acts on it. Discarding the error boots a gateway with nil
// upstream maps, which 404s every /api/ route instead of naming the missing
// variable, and TestLoadUpstreamsFailsLoudlyOnAMissingProbedURL cannot see it.
func TestGatewayMainFatalsOnAnUpstreamError(t *testing.T) {
	const path = "main.go"
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var body *ast.BlockStmt
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "main" && fn.Recv == nil {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatalf("%s declares no func main -- the scan below has nothing to read", path)
	}

	at := -1
	errName := ""
	for i, st := range body.List {
		as, ok := st.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			continue
		}
		if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "loadUpstreams" {
			continue
		}
		if len(as.Lhs) != 3 {
			t.Fatalf("%s: loadUpstreams is assigned to %d name(s), want 3", path, len(as.Lhs))
		}
		id, ok := as.Lhs[2].(*ast.Ident)
		if !ok || id.Name == "_" {
			t.Fatalf("%s: main discards loadUpstreams' error -- a missing <NAME>_URL boots a gateway with no upstreams instead of a named failure", path)
		}
		at, errName = i, id.Name
		break
	}
	if at < 0 {
		t.Fatalf("%s: main never calls loadUpstreams -- upstreams are wired somewhere this scan cannot see", path)
	}
	if at+1 >= len(body.List) {
		t.Fatalf("%s: nothing follows the loadUpstreams call, so its error is never checked", path)
	}

	guard, ok := body.List[at+1].(*ast.IfStmt)
	if !ok {
		t.Fatalf("%s: the statement after loadUpstreams is %T, not an `if %s != nil` guard", path, body.List[at+1], errName)
	}
	bin, ok := guard.Cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		t.Fatalf("%s: the guard after loadUpstreams tests %v, not `%s != nil`", path, guard.Cond, errName)
	}
	if id, ok := bin.X.(*ast.Ident); !ok || id.Name != errName {
		t.Fatalf("%s: the guard after loadUpstreams tests something other than %s", path, errName)
	}

	fatals := 0
	ast.Inspect(guard.Body, func(n ast.Node) bool {
		if e, ok := n.(ast.Expr); ok {
			if _, ok := isCallTo(e, "platform", "Fatal"); ok {
				fatals++
			}
		}
		return true
	})
	if fatals != 1 {
		t.Errorf("%s: the `%s != nil` guard calls platform.Fatal %d time(s), want 1 -- boot must stop, and it must stop at ERROR", path, errName, fatals)
	}
}

// TestGatewayMainPassesAuthAdminPassword accepts either wiring the plan allows:
// resolveRolePassword("AUTH_ADMIN_PASSWORD", "", app.Logger) or os.Getenv("AUTH_ADMIN_PASSWORD").
func TestGatewayMainPassesAuthAdminPassword(t *testing.T) {
	const path = "main.go"
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var lits []*ast.CompositeLit
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if sel, ok := cl.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "RolePasswords" {
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "db" {
				lits = append(lits, cl)
			}
		}
		return true
	})
	if len(lits) != 1 {
		t.Fatalf("%s: found %d db.RolePasswords literal(s), want exactly 1", path, len(lits))
	}

	fields := map[string]ast.Expr{}
	for _, e := range lits[0].Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			if id, ok := kv.Key.(*ast.Ident); ok {
				fields[id.Name] = kv.Value
			}
		}
	}
	if _, ok := fields["Reader"]; !ok {
		t.Fatalf("%s: the db.RolePasswords literal has no Reader field -- this scan found the wrong literal", path)
	}
	val, ok := fields["AuthAdmin"]
	if !ok {
		t.Fatalf("%s: the db.RolePasswords literal has no AuthAdmin field", path)
	}

	call, ok := val.(*ast.CallExpr)
	if !ok {
		t.Fatalf("%s: AuthAdmin is %T, want a call reading AUTH_ADMIN_PASSWORD", path, val)
	}
	strArg := func(i int) (string, bool) {
		if i >= len(call.Args) {
			return "", false
		}
		lit, ok := call.Args[i].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(lit.Value)
		return s, err == nil
	}

	switch fn := call.Fun.(type) {
	case *ast.Ident:
		if fn.Name != "resolveRolePassword" {
			t.Fatalf("%s: AuthAdmin calls %s, want resolveRolePassword or os.Getenv", path, fn.Name)
		}
		if name, ok := strArg(0); !ok || name != "AUTH_ADMIN_PASSWORD" {
			t.Errorf("%s: AuthAdmin's resolveRolePassword first argument = %q, want \"AUTH_ADMIN_PASSWORD\"", path, name)
		}
		// No deprecated name exists for this variable.
		if old, ok := strArg(1); !ok || old != "" {
			t.Errorf("%s: AuthAdmin's resolveRolePassword fallback = %q, want \"\"", path, old)
		}
	case *ast.SelectorExpr:
		pkg, ok := fn.X.(*ast.Ident)
		if !ok || pkg.Name != "os" || fn.Sel.Name != "Getenv" {
			t.Fatalf("%s: AuthAdmin calls a selector other than os.Getenv", path)
		}
		if name, ok := strArg(0); !ok || name != "AUTH_ADMIN_PASSWORD" {
			t.Errorf("%s: AuthAdmin reads os.Getenv(%q), want \"AUTH_ADMIN_PASSWORD\"", path, name)
		}
	default:
		t.Fatalf("%s: AuthAdmin calls %T, want resolveRolePassword or os.Getenv", path, call.Fun)
	}
}

// TestLoadUpstreamsRequiresAuthURL: `auth` is probed, never routed, and a gateway
// that cannot see it must not boot.
func TestLoadUpstreamsRequiresAuthURL(t *testing.T) {
	if !slices.Contains(probedServices, "auth") {
		t.Errorf("probedServices = %v, want it to carry `auth`", probedServices)
	}

	setUpstreamEnv(t, "http://127.0.0.1:1")
	t.Setenv("AUTH_URL", "")
	_, _, err := loadUpstreams()
	if err == nil {
		t.Error("loadUpstreams succeeded with AUTH_URL unset, want a named boot failure")
	} else if !strings.Contains(err.Error(), "AUTH_URL") {
		t.Errorf("error %q does not name AUTH_URL", err)
	}

	t.Setenv("AUTH_URL", "http://127.0.0.1:2")
	routed, probed, err := loadUpstreams()
	if err != nil {
		t.Fatalf("loadUpstreams with AUTH_URL set: %v", err)
	}
	if u, ok := probed["auth"]; !ok || u.String() != "http://127.0.0.1:2" {
		t.Errorf("probed[auth] = %v (present %v), want http://127.0.0.1:2", u, ok)
	}
	if _, ok := routed["auth"]; ok {
		t.Error("auth is in the routed map -- it would get a public /api/auth/* proxy route")
	}
}

// TestLoadUpstreamsRequiresReconciliationURL: `reconciliation` is probed, never routed,
// so the deploy gate sees it running; a gateway that cannot see it must not boot.
func TestLoadUpstreamsRequiresReconciliationURL(t *testing.T) {
	if !slices.Contains(probedServices, "reconciliation") {
		t.Errorf("probedServices = %v, want it to carry `reconciliation`", probedServices)
	}

	setUpstreamEnv(t, "http://127.0.0.1:1")
	t.Setenv("RECONCILIATION_URL", "")
	_, _, err := loadUpstreams()
	if err == nil {
		t.Error("loadUpstreams succeeded with RECONCILIATION_URL unset, want a named boot failure")
	} else if !strings.Contains(err.Error(), "RECONCILIATION_URL") {
		t.Errorf("error %q does not name RECONCILIATION_URL", err)
	}

	t.Setenv("RECONCILIATION_URL", "http://127.0.0.1:2")
	routed, probed, err := loadUpstreams()
	if err != nil {
		t.Fatalf("loadUpstreams with RECONCILIATION_URL set: %v", err)
	}
	if u, ok := probed["reconciliation"]; !ok || u.String() != "http://127.0.0.1:2" {
		t.Errorf("probed[reconciliation] = %v (present %v), want http://127.0.0.1:2", u, ok)
	}
	if _, ok := routed["reconciliation"]; ok {
		t.Error("reconciliation is in the routed map -- it would get a public /api/reconciliation/* proxy route")
	}

	t.Run("rollup_names_it", func(t *testing.T) {
		get, fleetNames := gatewayMux(t)
		seen := fleetNames()
		if !seen["gateway"] {
			t.Fatalf("control: /healthz/fleet omits the gateway itself; names = %v", seen)
		}
		if !seen["reconciliation"] {
			t.Errorf("/healthz/fleet omits \"reconciliation\"; names = %v", seen)
		}
		if got := get("/api/reconciliation/x"); got != http.StatusNotFound {
			t.Errorf("GET /api/reconciliation/x = %d, want 404 -- a probed service is exposed as a public proxy route", got)
		}
		// Control: a 404 proves nothing if the mux routes no service at all.
		if got := get("/api/tenancy/x"); got != http.StatusBadGateway {
			t.Errorf("GET /api/tenancy/x = %d, want 502 (routed to a dead upstream)", got)
		}
	})

	// A down reconciliation must degrade the roll-up, never be skipped or tolerated.
	t.Run("down_degrades_rollup", func(t *testing.T) {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		t.Cleanup(up.Close)
		var mu sync.Mutex
		reconCode, reconPaths := http.StatusOK, []string{}
		recon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			reconPaths = append(reconPaths, r.URL.Path)
			w.WriteHeader(reconCode)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		t.Cleanup(recon.Close)

		setUpstreamEnv(t, up.URL)
		t.Setenv("RECONCILIATION_URL", recon.URL)
		routed, probed, err := loadUpstreams()
		if err != nil {
			t.Fatalf("loadUpstreams: %v", err)
		}
		_, fleet := gatewayHandlers(nil, nilURLSessions(), routed, probed, nil, slog.Default(), "gw-test-token")
		rollup := func() (int, string, map[string]string) {
			rec := httptest.NewRecorder()
			fleet(rec, httptest.NewRequest(http.MethodGet, "/healthz/fleet", nil))
			var body struct {
				Status   string `json:"status"`
				Services []struct {
					Name, Status string
				} `json:"services"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode /healthz/fleet: %v (body %q)", err, rec.Body.String())
			}
			st := map[string]string{}
			for _, s := range body.Services {
				st[s.Name] = s.Status
			}
			return rec.Code, body.Status, st
		}

		// Positive control: every backend up, reconciliation probed at its platform /healthz.
		code, status, st := rollup()
		if code != http.StatusOK || status != "ok" || st["reconciliation"] != "up" {
			t.Fatalf("all up: code=%d status=%q reconciliation=%q, want 200 ok up; services=%v", code, status, st["reconciliation"], st)
		}
		mu.Lock()
		paths := slices.Clone(reconPaths)
		reconCode = http.StatusServiceUnavailable
		mu.Unlock()
		if !slices.Equal(paths, []string{"/healthz", "/readyz"}) {
			t.Errorf("reconciliation probed at %v, want exactly [/healthz /readyz]", paths)
		}

		code, status, st = rollup()
		if code != http.StatusServiceUnavailable || status != "degraded" {
			t.Errorf("reconciliation down: code=%d status=%q, want 503 degraded", code, status)
		}
		if st["reconciliation"] != "down" {
			t.Errorf("reconciliation = %q, want down; services=%v", st["reconciliation"], st)
		}
		for name, s := range st {
			if name != "reconciliation" && s != "up" {
				t.Errorf("%s = %q, want up -- only reconciliation is down", name, s)
			}
		}
	})
}

// parseMain returns main.go's func main body; the AST scans below read main's wiring.
func parseMain(t *testing.T) (*ast.File, *ast.BlockStmt) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "main" && fn.Recv == nil {
			return f, fn.Body
		}
	}
	t.Fatal("main.go declares no func main")
	return nil, nil
}

func isCallTo(e ast.Expr, pkg, name string) (*ast.CallExpr, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return call, pkg == "" && fn.Name == name
	case *ast.SelectorExpr:
		x, ok := fn.X.(*ast.Ident)
		return call, ok && x.Name == pkg && fn.Sel.Name == name
	}
	return call, false
}

func isStringLit(e ast.Expr, want string) bool {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(lit.Value)
	return err == nil && s == want
}

// TestGatewayAuthIssuersCountsPrimaryPlusAdditional pins main's wiring: the
// additional set comes from AUTH_ADDITIONAL_ISSUERS through a helper that is
// fatal on a parse error, feeds the verifier, and publishes 1 + its length
// before app.Run. main needs Postgres, so it is scanned, not run.
func TestGatewayAuthIssuersCountsPrimaryPlusAdditional(t *testing.T) {
	f, body := parseMain(t)

	var helper *ast.FuncDecl
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "mustParseIssuers" && fn.Recv == nil {
			helper = fn
		}
	}
	if helper == nil {
		t.Errorf("main.go declares no mustParseIssuers")
	} else {
		parses, fatals := 0, 0
		ast.Inspect(helper.Body, func(n ast.Node) bool {
			if e, ok := n.(ast.Expr); ok {
				if _, ok := isCallTo(e, "auth", "ParseTrustedIssuers"); ok {
					parses++
				}
				if _, ok := isCallTo(e, "platform", "Fatal"); ok {
					fatals++
				}
			}
			return true
		})
		if parses != 1 || fatals != 1 {
			t.Errorf("mustParseIssuers calls auth.ParseTrustedIssuers %d time(s) and platform.Fatal %d time(s), want 1 and 1 -- a malformed AUTH_ADDITIONAL_ISSUERS must stop boot at ERROR", parses, fatals)
		}
	}

	// setVar is the local that holds mustParseIssuers(os.Getenv("AUTH_ADDITIONAL_ISSUERS")).
	setVar, setAt := "", -1
	for i, st := range body.List {
		as, ok := st.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			continue
		}
		call, ok := isCallTo(as.Rhs[0], "", "mustParseIssuers")
		if !ok || len(call.Args) != 1 {
			continue
		}
		env, ok := isCallTo(call.Args[0], "os", "Getenv")
		if !ok || len(env.Args) != 1 || !isStringLit(env.Args[0], "AUTH_ADDITIONAL_ISSUERS") {
			continue
		}
		if id, ok := as.Lhs[0].(*ast.Ident); ok {
			setVar, setAt = id.Name, i
		}
	}
	if setVar == "" {
		t.Fatal(`main never assigns mustParseIssuers(os.Getenv("AUTH_ADDITIONAL_ISSUERS")) to a local`)
	}

	// The verifier trusts exactly the set that is counted, and fetches keys through the traced client.
	fedToVerifier, tracedFetch := false, false
	ast.Inspect(body, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if sel, ok := cl.Type.(*ast.SelectorExpr); !ok || sel.Sel.Name != "Config" {
			return true
		}
		fed, traced := false, false
		for _, e := range cl.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			k, _ := kv.Key.(*ast.Ident)
			v, _ := kv.Value.(*ast.Ident)
			if k != nil && v != nil && k.Name == "Additional" && v.Name == setVar {
				fed = true
			}
			if call, ok := isCallTo(kv.Value, "", "newJWKSClient"); k != nil && ok && k.Name == "HTTPClient" && len(call.Args) == 0 {
				traced = true
			}
		}
		fedToVerifier = fedToVerifier || fed
		tracedFetch = tracedFetch || (fed && traced)
		return true
	})
	if !fedToVerifier {
		t.Errorf("main's auth.Config has no `Additional: %s`", setVar)
	}
	if !tracedFetch {
		t.Error("main's verifier auth.Config has no `HTTPClient: newJWKSClient()` -- the JWKS fetch would carry no trace")
	}

	isOnePlusLen := func(e ast.Expr) bool {
		bin, ok := e.(*ast.BinaryExpr)
		if !ok || bin.Op != token.ADD {
			return false
		}
		isOne := func(x ast.Expr) bool { l, ok := x.(*ast.BasicLit); return ok && l.Value == "1" }
		isLen := func(x ast.Expr) bool {
			c, ok := isCallTo(x, "", "len")
			if !ok || len(c.Args) != 1 {
				return false
			}
			id, ok := c.Args[0].(*ast.Ident)
			return ok && id.Name == setVar
		}
		return (isOne(bin.X) && isLen(bin.Y)) || (isLen(bin.X) && isOne(bin.Y))
	}
	publishAt, runAt := -1, -1
	for i, st := range body.List {
		if as, ok := st.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
			if sel, ok := as.Lhs[0].(*ast.SelectorExpr); ok && sel.Sel.Name == "AuthIssuers" {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "platform" {
					if c, ok := isCallTo(as.Rhs[0], "strconv", "Itoa"); ok && len(c.Args) == 1 && isOnePlusLen(c.Args[0]) {
						publishAt = i
					}
				}
			}
		}
		if ifs, ok := st.(*ast.IfStmt); ok && runAt < 0 {
			if as, ok := ifs.Init.(*ast.AssignStmt); ok && len(as.Rhs) == 1 {
				if c, ok := as.Rhs[0].(*ast.CallExpr); ok {
					if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Run" {
						runAt = i
					}
				}
			}
		}
	}
	if runAt < 0 {
		t.Fatal("main has no `if err := app.Run(...)` statement -- the ordering check below has no anchor")
	}
	if publishAt < 0 {
		t.Errorf("main never sets platform.AuthIssuers = strconv.Itoa(1 + len(%s))", setVar)
	} else if publishAt < setAt || publishAt > runAt {
		t.Errorf("platform.AuthIssuers is set at statement %d, want it after the parse (%d) and before app.Run (%d)", publishAt, setAt, runAt)
	}
}

// A malformed AUTH_ADDITIONAL_ISSUERS must stop boot before Provision bootstraps, resets or seeds.
func TestGatewayMainParsesIssuersBeforeProvision(t *testing.T) {
	_, body := parseMain(t)

	parseAt, provisionAt := -1, -1
	for i, st := range body.List {
		ast.Inspect(st, func(n ast.Node) bool {
			e, ok := n.(ast.Expr)
			if !ok {
				return true
			}
			if _, ok := isCallTo(e, "", "mustParseIssuers"); ok && parseAt < 0 {
				parseAt = i
			}
			if _, ok := isCallTo(e, "db", "Provision"); ok && provisionAt < 0 {
				provisionAt = i
			}
			return true
		})
	}
	if parseAt < 0 || provisionAt < 0 {
		t.Fatalf("main calls mustParseIssuers at statement %d and db.Provision at %d, want both", parseAt, provisionAt)
	}
	if parseAt >= provisionAt {
		t.Errorf("mustParseIssuers runs at statement %d, want it before db.Provision (%d)", parseAt, provisionAt)
	}
}

// A gateway that boots without its token signs nothing; the read must stop boot before Provision.
func TestGatewayMainReadsGatewayTokenBeforeProvision(t *testing.T) {
	f, body := parseMain(t)

	readAt, provisionAt, reads := -1, -1, 0
	tokenVar := ""
	for i, st := range body.List {
		ast.Inspect(st, func(n ast.Node) bool {
			e, ok := n.(ast.Expr)
			if !ok {
				return true
			}
			if c, ok := isCallTo(e, "", "mustEnv"); ok && len(c.Args) == 1 && isStringLit(c.Args[0], "GATEWAY_TOKEN") {
				reads++
				if readAt < 0 {
					readAt = i
				}
			}
			if _, ok := isCallTo(e, "db", "Provision"); ok && provisionAt < 0 {
				provisionAt = i
			}
			return true
		})
		if as, ok := st.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
			if c, ok := isCallTo(as.Rhs[0], "", "mustEnv"); ok && len(c.Args) == 1 && isStringLit(c.Args[0], "GATEWAY_TOKEN") {
				if id, ok := as.Lhs[0].(*ast.Ident); ok {
					tokenVar = id.Name
				}
			}
		}
	}
	if reads != 1 {
		t.Fatalf("main reads mustEnv(\"GATEWAY_TOKEN\") %d time(s), want once", reads)
	}
	if provisionAt < 0 {
		t.Fatal("main has no db.Provision call -- the ordering check has no anchor")
	}
	if readAt >= provisionAt {
		t.Errorf("GATEWAY_TOKEN is read at statement %d, want it before db.Provision (%d)", readAt, provisionAt)
	}
	if tokenVar == "" {
		t.Fatal("main does not assign mustEnv(\"GATEWAY_TOKEN\") to a variable")
	}

	var last ast.Expr
	ast.Inspect(body, func(n ast.Node) bool {
		if e, ok := n.(ast.Expr); ok {
			if c, ok := isCallTo(e, "", "gatewayHandlers"); ok && len(c.Args) > 0 {
				last = c.Args[len(c.Args)-1]
			}
		}
		return true
	})
	if id, ok := last.(*ast.Ident); !ok || id.Name != tokenVar {
		t.Errorf("gatewayHandlers' last argument is %v, want the GATEWAY_TOKEN variable %q", last, tokenVar)
	}

	guards := 0
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "RequireGateway" {
			guards++
		}
		return true
	})
	if guards != 0 {
		t.Errorf("main.go calls RequireGateway %d time(s), want none: the gateway signs, it does not guard", guards)
	}
}

// TestGatewayMainProbesAuthAtItsJWKSPath: FleetHealthHandler's per-service path is
// inert unless main passes it. The fleet tests cannot see this call.
func TestGatewayMainProbesAuthAtItsJWKSPath(t *testing.T) {
	_, body := parseMain(t)

	var arg ast.Expr
	calls := 0
	ast.Inspect(body, func(n ast.Node) bool {
		if e, ok := n.(ast.Expr); ok {
			if c, ok := isCallTo(e, "", "gatewayHandlers"); ok {
				calls++
				if len(c.Args) == 7 {
					arg = c.Args[4]
				}
			}
		}
		return true
	})
	if calls != 1 || arg == nil {
		t.Fatalf("main calls gatewayHandlers %d time(s) with a healthPaths argument %v, want once", calls, arg)
	}

	// Accept the literal inline, or a local assigned from one.
	lit, _ := arg.(*ast.CompositeLit)
	if id, ok := arg.(*ast.Ident); ok && id.Name != "nil" {
		ast.Inspect(body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
				return true
			}
			if l, ok := as.Lhs[0].(*ast.Ident); ok && l.Name == id.Name {
				if cl, ok := as.Rhs[0].(*ast.CompositeLit); ok {
					lit = cl
				}
			}
			return true
		})
	}
	if lit == nil {
		t.Fatalf("gatewayHandlers' healthPaths argument is %T, want a map literal (or a local assigned from one)", arg)
	}
	if len(lit.Elts) == 0 {
		t.Fatal("the healthPaths literal is empty")
	}
	found := false
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if isStringLit(kv.Key, "auth") {
			found = true
			if !isStringLit(kv.Value, ".well-known/jwks.json") {
				t.Errorf("healthPaths[auth] is not \".well-known/jwks.json\"")
			}
		} else {
			t.Errorf("healthPaths carries a key other than auth; every other service stays on /healthz")
		}
	}
	if !found {
		t.Error("healthPaths has no `auth` entry")
	}
}

// fakeAuth answers 200 on every path with a body that satisfies both /signup and
// /verify, and records "METHOD path" per call.
func fakeAuth(t *testing.T) (*url.URL, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"7f3c2a1e-0b7d-4f51-9a0e-5d1c2b3a4e5f","access_token":"at","refresh_token":"rt"}`))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse fake url: %v", err)
	}
	return u, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(calls)
	}
}

func serveRegistration(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
	return rec
}

// serveForm posts body as an urlencoded form, as the confirm page's button does.
func serveForm(h http.Handler, target, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	return rec
}

// pageForm returns the one form of a confirm page: its action resolved against pageURL, and its named inputs.
func pageForm(t *testing.T, pageURL, body string) (string, url.Values) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse page: %v", err)
	}
	attr := func(n *html.Node, key string) string {
		for _, a := range n.Attr {
			if a.Key == key {
				return a.Val
			}
		}
		return ""
	}
	var forms []*html.Node
	var find func(*html.Node)
	find = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "form" {
			forms = append(forms, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(doc)
	if len(forms) != 1 {
		t.Fatalf("page has %d forms, want exactly one:\n%s", len(forms), body)
	}
	if m := attr(forms[0], "method"); !strings.EqualFold(m, "post") {
		t.Fatalf("form method = %q, want post", m)
	}
	values := url.Values{}
	var collect func(*html.Node)
	collect = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "input" && attr(n, "name") != "" {
			values.Add(attr(n, "name"), attr(n, "value"))
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			collect(c)
		}
	}
	collect(forms[0])
	page, err := url.Parse(pageURL)
	if err != nil {
		t.Fatal(err)
	}
	action, err := url.Parse(attr(forms[0], "action"))
	if err != nil {
		t.Fatal(err)
	}
	return page.ResolveReference(action).String(), values
}

func TestRegistrationHandlers_WiresRegisterAndVerify(t *testing.T) {
	authURL, calls := fakeAuth(t)
	site, _ := url.Parse("https://site.example")
	const floor = 100 * time.Millisecond
	reg := registrationHandlers(authURL, site, floor, slog.New(slog.DiscardHandler), nil)

	start := time.Now()
	rec := serveRegistration(reg.Register, http.MethodPost, "/auth/register", `{"email":"new@corp.example","password":"Corr3ct-Horse"}`)
	if elapsed := time.Since(start); elapsed < floor {
		t.Errorf("Register answered after %v, want no earlier than the %v minimum", elapsed, floor)
	}
	if rec.Code != http.StatusAccepted {
		t.Errorf("Register = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	rec = serveForm(handoffVerify(authURL, site, nil), "/auth/verify", "token=T&type=signup")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "https://site.example/?verified=1" {
		t.Errorf("Verify = %d Location %q, want 303 https://site.example/?verified=1", rec.Code, rec.Header().Get("Location"))
	}
	if got, want := calls(), []string{"POST /signup", "POST /verify"}; !slices.Equal(got, want) {
		t.Errorf("GoTrue saw %v, want %v", got, want)
	}
}

// AUTH_SITE_URL or AUTH_URL unset: every GoTrue route refuses without calling GoTrue.
func TestRegistrationHandlers_NotConfigured503(t *testing.T) {
	site, _ := url.Parse("https://site.example")
	for _, unset := range []string{"AUTH_SITE_URL", "AUTH_URL"} {
		t.Run(unset, func(t *testing.T) {
			authURL, calls := fakeAuth(t)
			var reg registration
			if unset == "AUTH_SITE_URL" {
				reg = registrationHandlers(authURL, nil, 0, slog.New(slog.DiscardHandler), nil)
			} else {
				reg = registrationHandlers(nil, site, 0, slog.New(slog.DiscardHandler), nil)
			}

			answers := map[string]*httptest.ResponseRecorder{
				"Register": serveRegistration(reg.Register, http.MethodPost, "/auth/register", `{"email":"new@corp.example","password":"Corr3ct-Horse"}`),

				"ResendVerification":   serveRegistration(reg.ResendVerification, http.MethodPost, "/auth/resend-verification", `{"email":"new@corp.example"}`),
				"RequestPasswordReset": serveRegistration(reg.RequestPasswordReset, http.MethodPost, "/auth/request-password-reset", `{"email":"new@corp.example"}`),
			}
			// handoffHandlers builds Verify and needs a GoTrue URL to build the rest, so only AUTH_SITE_URL unset reaches it.
			if unset == "AUTH_SITE_URL" {
				answers["Verify"] = serveForm(handoffVerify(authURL, nil, nil), "/auth/verify", "token=T&type=signup")
			}
			for name, rec := range answers {
				if rec.Code != http.StatusServiceUnavailable {
					t.Errorf("%s = %d, want 503: %s", name, rec.Code, rec.Body.String())
					continue
				}
				var body map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body) != 1 || body["error"] != "registration is not configured" {
					t.Errorf("%s body = %s, want {\"error\":\"registration is not configured\"}", name, rec.Body.String())
				}
			}
			if got := calls(); len(got) != 0 {
				t.Errorf("GoTrue saw %v, want no calls", got)
			}
		})
	}
}

// invitationPreviewerStub answers every token with one live invite and counts the calls.
func invitationPreviewerStub(address string) (gateway.InvitationPreviewer, func() int) {
	var mu sync.Mutex
	n := 0
	return func(context.Context, string) (gateway.InvitationPreview, error) {
			mu.Lock()
			defer mu.Unlock()
			n++
			return gateway.InvitationPreview{Workspace: "Obi Partners", Role: "reviewer", Email: address}, nil
		}, func() int {
			mu.Lock()
			defer mu.Unlock()
			return n
		}
}

// AUTH_URL, AUTH_SITE_URL or the register throttle missing: invitee registration refuses, and the preview still answers.
func TestInvitationHandlers_NotConfigured503(t *testing.T) {
	site, _ := url.Parse("https://site.example")
	perIP := func() *gateway.SignInThrottle {
		return gateway.NewSignInThrottle("register", gateway.RegisterPerIP, gateway.RegisterMaxKeys, gateway.RegisterWindow, time.Now)
	}
	const registerBody = `{"token":"Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9T","password":"Corr3ct-Horse"}`

	// Control: configured, the same call signs the invited address up, so the 503s below are the unset input.
	t.Run("configured", func(t *testing.T) {
		authURL, calls := fakeAuth(t)
		preview, previewed := invitationPreviewerStub("tunde@obi.test")
		_, register := invitationHandlers(authURL, site, 0, perIP(), preview, slog.New(slog.DiscardHandler))

		rec := serveRegistration(register, http.MethodPost, "/auth/invitation/register", registerBody)

		if rec.Code != http.StatusAccepted {
			t.Fatalf("register = %d, want 202: %s", rec.Code, rec.Body.String())
		}
		if got := calls(); !slices.Equal(got, []string{"POST /signup"}) || previewed() != 1 {
			t.Errorf("GoTrue saw %v and the previewer %d call(s), want one signup and one preview", got, previewed())
		}
	})

	for _, unset := range []string{"AUTH_URL", "AUTH_SITE_URL", "register throttle"} {
		t.Run(unset, func(t *testing.T) {
			authURL, calls := fakeAuth(t)
			preview, previewed := invitationPreviewerStub("tunde@obi.test")
			log := slog.New(slog.DiscardHandler)
			var invitation, register http.Handler
			switch unset {
			case "AUTH_URL":
				invitation, register = invitationHandlers(nil, site, 0, perIP(), preview, log)
			case "AUTH_SITE_URL":
				invitation, register = invitationHandlers(authURL, nil, 0, perIP(), preview, log)
			default:
				invitation, register = invitationHandlers(authURL, site, 0, nil, preview, log)
			}

			rec := serveRegistration(register, http.MethodPost, "/auth/invitation/register", registerBody)

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("register = %d, want 503: %s", rec.Code, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body) != 1 || body["error"] != "registration is not configured" {
				t.Errorf("register body = %s, want {\"error\":\"registration is not configured\"}", rec.Body.String())
			}
			if got := calls(); len(got) != 0 || previewed() != 0 {
				t.Errorf("GoTrue saw %v and the previewer %d call(s), want none", got, previewed())
			}

			rec = serveRegistration(invitation, http.MethodPost, "/auth/invitation", `{"token":"Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9T"}`)
			if rec.Code != http.StatusOK || previewed() != 1 {
				t.Errorf("preview = %d with %d previewer call(s), want 200 and 1: %s", rec.Code, previewed(), rec.Body.String())
			}
			var preview200 map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &preview200); err != nil || preview200["email"] != "tunde@obi.test" {
				t.Errorf("preview body = %s, want the invite's workspace, role and email", rec.Body.String())
			}
		})
	}
}

// Invitee registration enforces its per-client limit exactly where /auth/register does: not on a PR preview.
func TestInvitationHandlers_EnforcementFollowsThePosture(t *testing.T) {
	for _, c := range []struct {
		name, env string
		wantCalls int
	}{
		{"pr-7", "pr-7", 11},
		{"production", "production", gateway.RegisterPerIP},
		{"development", "development", gateway.RegisterPerIP},
		{"empty", "", gateway.RegisterPerIP},
		{"upper-case PR-7", "PR-7", gateway.RegisterPerIP},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("RAILWAY_ENVIRONMENT_NAME", c.env)
			authURL, calls := fakeAuth(t)
			site, _ := url.Parse("https://site.example")
			log := slog.New(slog.DiscardHandler)
			reg := registrationHandlers(authURL, site, 0, log, nil)
			preview, _ := invitationPreviewerStub("tunde@obi.test")
			_, register := invitationHandlers(authURL, site, 0, reg.RegisterPerIP, preview, log)

			for range 11 {
				req := httptest.NewRequest(http.MethodPost, "/auth/invitation/register", strings.NewReader(`{"token":"Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9Tt9T","password":"Corr3ct-Horse"}`))
				req.RemoteAddr = "203.0.113.7:4000"
				rec := httptest.NewRecorder()
				register.ServeHTTP(rec, req)
				if rec.Code != http.StatusAccepted {
					t.Fatalf("invitee register = %d, want 202: %s", rec.Code, rec.Body.String())
				}
			}
			if got := countCalls(calls(), "POST /signup"); got != c.wantCalls {
				t.Errorf("GoTrue /signup calls = %d, want %d", got, c.wantCalls)
			}
		})
	}
}

// The throttle invitee registration shares is the one /auth/register spends.
func TestRegistrationHandlers_ExposesTheRegisterThrottle(t *testing.T) {
	const remote = "203.0.113.7:4000"
	authURL, _ := fakeAuth(t)
	site, _ := url.Parse("https://site.example")
	log := slog.New(slog.DiscardHandler)

	reg := registrationHandlers(authURL, site, 0, log, nil)
	if reg.RegisterPerIP == nil {
		t.Fatal("RegisterPerIP is nil with AUTH_URL and AUTH_SITE_URL configured")
	}
	if !reg.RegisterPerIP.Reserve("198.51.100.1") {
		t.Fatal("a fresh throttle refused its first reservation")
	}
	for _, email := range distinctAddresses(gateway.RegisterPerIP) {
		if rec := registerFrom(reg.Register, email, remote); rec.Code != http.StatusAccepted {
			t.Fatalf("register = %d, want 202: %s", rec.Code, rec.Body.String())
		}
	}
	if reg.RegisterPerIP.Reserve("203.0.113.7") {
		t.Errorf("RegisterPerIP still has budget for a client that spent all %d register attempts: it is not the throttle Register uses", gateway.RegisterPerIP)
	}

	if got := registrationHandlers(nil, site, 0, log, nil).RegisterPerIP; got != nil {
		t.Error("RegisterPerIP is non-nil with AUTH_URL unset")
	}
	if got := registrationHandlers(authURL, nil, 0, log, nil).RegisterPerIP; got != nil {
		t.Error("RegisterPerIP is non-nil with AUTH_SITE_URL unset")
	}
}

// resendFrom posts one resend for email as the client behind remote.
func resendFrom(h http.Handler, email, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/auth/resend-verification", strings.NewReader(`{"email":"`+email+`"}`))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// registerFrom posts one register for email as the client behind remote.
func registerFrom(h http.Handler, email, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(`{"email":"`+email+`","password":"Corr3ct-Horse"}`))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// countCalls counts the recorded GoTrue calls equal to want.
func countCalls(calls []string, want string) int {
	n := 0
	for _, c := range calls {
		if c == want {
			n++
		}
	}
	return n
}

// resetFrom posts one reset request for email as the client behind remote.
func resetFrom(h http.Handler, email, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/auth/request-password-reset", strings.NewReader(`{"email":"`+email+`"}`))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The reset page and its form need AUTH_SITE_URL, the form also AUTH_URL: unset, they answer 503 and call nothing.
func TestResetPasswordHandler_NotConfigured503(t *testing.T) {
	site, _ := url.Parse("https://site.example")
	log := slog.New(slog.DiscardHandler)
	signIn := gateway.NewSignInThrottle("sign-in", gateway.SignInMaxFailures, gateway.SignInMaxKeys, gateway.SignInWindow, time.Now)
	for _, unset := range []string{"AUTH_SITE_URL", "AUTH_URL"} {
		t.Run(unset, func(t *testing.T) {
			authURL, calls := fakeAuth(t)
			sessions := gateway.NewSessionChecker(nil, nil, time.Now, log)
			got := map[string]*httptest.ResponseRecorder{}
			if unset == "AUTH_SITE_URL" {
				got["page"] = serveRegistration(gateway.ResetPasswordPageHandler(nil), http.MethodGet, "/auth/reset-password?token=t&type=recovery", "")
				got["form"] = serveForm(resetPasswordHandler(authURL, nil, sessions, signIn, log), "/auth/reset-password", "token=t&type=recovery&password=new-password-1")
			} else {
				got["form"] = serveForm(resetPasswordHandler(nil, site, sessions, signIn, log), "/auth/reset-password", "token=t&type=recovery&password=new-password-1")
			}
			for name, rec := range got {
				if rec.Code != http.StatusServiceUnavailable {
					t.Errorf("%s = %d, want 503: %s", name, rec.Code, rec.Body.String())
					continue
				}
				var body map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body) != 1 || body["error"] != "registration is not configured" {
					t.Errorf("%s body = %s, want {\"error\":\"registration is not configured\"}", name, rec.Body.String())
				}
			}
			if c := calls(); len(c) != 0 {
				t.Errorf("GoTrue saw %v, want no calls", c)
			}
		})
	}
}

// Resend and reset spend one budget: 3 per address and 10 per client (D4). Separate throttles would double both.
func TestRegistrationHandlers_ResetSharesTheResendLimits(t *testing.T) {
	const remote = "203.0.113.7:4000"
	mailCalls := func(calls []string) int {
		return countCalls(calls, "POST /resend") + countCalls(calls, "POST /recover")
	}

	t.Run("per address", func(t *testing.T) {
		authURL, calls := fakeAuth(t)
		site, _ := url.Parse("https://site.example")
		reg := registrationHandlers(authURL, site, 0, slog.New(slog.DiscardHandler), nil)
		for range 2 {
			resendFrom(reg.ResendVerification, "ada@corp.example", remote)
			resetFrom(reg.RequestPasswordReset, "ada@corp.example", remote)
		}
		if got := mailCalls(calls()); got != 3 {
			t.Errorf("GoTrue /resend + /recover calls for one address = %d, want 3", got)
		}
	})
	t.Run("per client", func(t *testing.T) {
		authURL, calls := fakeAuth(t)
		site, _ := url.Parse("https://site.example")
		reg := registrationHandlers(authURL, site, 0, slog.New(slog.DiscardHandler), nil)
		for i, email := range distinctAddresses(11) {
			if i%2 == 0 {
				resendFrom(reg.ResendVerification, email, remote)
			} else {
				resetFrom(reg.RequestPasswordReset, email, remote)
			}
		}
		if got := mailCalls(calls()); got != 10 {
			t.Errorf("GoTrue /resend + /recover calls from one client = %d, want 10", got)
		}
	})
}

// On a PR preview the per-address limit of the reset request logs but does not refuse.
func TestRegistrationHandlers_ResetPreviewOnlyLogsPerAddress(t *testing.T) {
	for _, c := range []struct {
		env          string
		wantCalls    int
		wantEnforced bool
	}{{"pr-7", 4, false}, {"production", 3, true}} {
		t.Run(c.env, func(t *testing.T) {
			t.Setenv("RAILWAY_ENVIRONMENT_NAME", c.env)
			authURL, calls := fakeAuth(t)
			site, _ := url.Parse("https://site.example")
			var logs bytes.Buffer
			reg := registrationHandlers(authURL, site, 0, slog.New(slog.NewJSONHandler(&logs, nil)), nil)

			for range 4 {
				resetFrom(reg.RequestPasswordReset, "ada@corp.example", "203.0.113.7:4000")
			}
			if got := countCalls(calls(), "POST /recover"); got != c.wantCalls {
				t.Errorf("GoTrue /recover calls = %d, want %d", got, c.wantCalls)
			}
			var limited int
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var rec map[string]any
				if json.Unmarshal([]byte(line), &rec) != nil || rec["msg"] != "reset-request: limit reached" {
					continue
				}
				limited++
				if rec["enforced"] != c.wantEnforced || rec["limit"] != "address" {
					t.Errorf("limit line = %s, want limit address enforced=%v", line, c.wantEnforced)
				}
			}
			if limited != 1 {
				t.Errorf("%d limit lines, want exactly 1 for the fourth request", limited)
			}
		})
	}
}

func TestRegistrationHandlers_ResendWaitsAndLimits(t *testing.T) {
	const remote = "203.0.113.7:4000"
	for _, c := range []struct {
		name      string
		floor     time.Duration
		addresses []string
		wantCalls int
	}{
		{"waits", 300 * time.Millisecond, []string{"ada@corp.example"}, 1},
		{"three", 0, []string{"ada@corp.example", "ada@corp.example", "ada@corp.example"}, 3},
		{"four", 0, []string{"ada@corp.example", "ada@corp.example", "ada@corp.example", "ada@corp.example"}, 3},
		{"per key", 0, distinctAddresses(11), 10},
	} {
		t.Run(c.name, func(t *testing.T) {
			authURL, calls := fakeAuth(t)
			site, _ := url.Parse("https://site.example")
			reg := registrationHandlers(authURL, site, c.floor, slog.New(slog.DiscardHandler), nil)

			start := time.Now()
			for _, email := range c.addresses {
				rec := resendFrom(reg.ResendVerification, email, remote)
				if rec.Code != http.StatusAccepted {
					t.Errorf("resend = %d, want 202 whether or not it was sent: %s", rec.Code, rec.Body.String())
				}
			}
			if elapsed := time.Since(start); elapsed < time.Duration(len(c.addresses))*c.floor {
				t.Errorf("%d resends took %v, want no less than %v each", len(c.addresses), elapsed, c.floor)
			}
			if got := countCalls(calls(), "POST /resend"); got != c.wantCalls {
				t.Errorf("GoTrue /resend calls = %d, want %d", got, c.wantCalls)
			}
		})
	}
}

func distinctAddresses(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "user" + strconv.Itoa(i) + "@corp.example"
	}
	return out
}

// Register's per-key bucket is its own: not removed, not shared with resend, not resend's per-address one.
func TestRegistrationHandlers_RegisterHasItsOwnLimit(t *testing.T) {
	const remote = "203.0.113.7:4000"
	authURL, calls := fakeAuth(t)
	site, _ := url.Parse("https://site.example")
	reg := registrationHandlers(authURL, site, 0, slog.New(slog.DiscardHandler), nil)

	for _, email := range distinctAddresses(11) {
		if rec := registerFrom(reg.Register, email, remote); rec.Code != http.StatusAccepted {
			t.Errorf("register = %d, want 202 over the limit too: %s", rec.Code, rec.Body.String())
		}
	}
	if got := countCalls(calls(), "POST /signup"); got != 10 {
		t.Errorf("GoTrue /signup calls = %d, want 10", got)
	}

	if rec := resendFrom(reg.ResendVerification, "ada@corp.example", remote); rec.Code != http.StatusAccepted {
		t.Errorf("resend = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if got := countCalls(calls(), "POST /resend"); got != 1 {
		t.Errorf("GoTrue /resend calls after a spent register bucket = %d, want 1", got)
	}
}

// A PR fork counts and logs each limited route's limits but does not refuse; every other posture refuses the eleventh.
func TestRegistrationHandlers_PreviewOnlyLogs(t *testing.T) {
	routes := []struct {
		name, path, limitMsg string
		send                 func(h http.Handler, email, remote string) *httptest.ResponseRecorder
		handler              func(reg registration) http.Handler
	}{
		{"register", "POST /signup", "registration: limit reached", registerFrom, func(r registration) http.Handler { return r.Register }},
		{"resend", "POST /resend", "resend-verification: limit reached", resendFrom, func(r registration) http.Handler { return r.ResendVerification }},
		{"reset", "POST /recover", "reset-request: limit reached", resetFrom, func(r registration) http.Handler { return r.RequestPasswordReset }},
	}
	for _, c := range []struct {
		name, env    string
		wantCalls    int
		wantEnforced bool
	}{
		{"pr-7", "pr-7", 11, false},
		{"production", "production", 10, true},
		{"development", "development", 10, true},
		{"empty", "", 10, true},
		{"bare pr-", "pr-", 10, true},
		{"lookalike prod-7", "prod-7", 10, true},
		{"upper-case PR-7", "PR-7", 10, true},
		{"trailing text pr-7x", "pr-7x", 10, true},
	} {
		for _, rt := range routes {
			t.Run(c.name+"/"+rt.name, func(t *testing.T) {
				t.Setenv("RAILWAY_ENVIRONMENT_NAME", c.env)
				authURL, calls := fakeAuth(t)
				site, _ := url.Parse("https://site.example")
				var logs bytes.Buffer
				reg := registrationHandlers(authURL, site, 0, slog.New(slog.NewJSONHandler(&logs, nil)), nil)

				for _, email := range distinctAddresses(11) {
					rt.send(rt.handler(reg), email, "203.0.113.7:4000")
				}
				if got := countCalls(calls(), rt.path); got != c.wantCalls {
					t.Errorf("GoTrue %s calls = %d, want %d", rt.path, got, c.wantCalls)
				}
				var limited int
				for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
					var rec map[string]any
					if json.Unmarshal([]byte(line), &rec) != nil || rec["msg"] != rt.limitMsg {
						continue
					}
					limited++
					if rec["enforced"] != c.wantEnforced {
						t.Errorf("limit line enforced = %v, want %v: %s", rec["enforced"], c.wantEnforced, line)
					}
				}
				if limited != 1 {
					t.Errorf("%d limit lines, want exactly 1 for the eleventh request", limited)
				}
			})
		}
	}
}

// A full map warns under the name of its own throttle: register's, resend's per-address and resend's per-key.
func TestRegistrationHandlers_FullMapWarningNamesTheMap(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	authURL, _ := fakeAuth(t)
	site, _ := url.Parse("https://site.example")
	reg := registrationHandlers(authURL, site, 0, slog.New(slog.DiscardHandler), nil)

	remote := func(i int) string { return fmt.Sprintf("10.%d.%d.%d:4000", i>>16&255, i>>8&255, i&255) }
	for i := range gateway.RegisterMaxKeys {
		registerFrom(reg.Register, "user"+strconv.Itoa(i)+"@corp.example", remote(i))
	}
	registerFrom(reg.Register, "one-more@corp.example", remote(gateway.RegisterMaxKeys))
	for i := range gateway.ResendMaxKeys {
		resendFrom(reg.ResendVerification, "user"+strconv.Itoa(i)+"@corp.example", remote(i))
	}
	// A known client with a new address fills the per-address map; a new client fills the per-key map.
	resendFrom(reg.ResendVerification, "one-more@corp.example", remote(0))
	resendFrom(reg.ResendVerification, "another@corp.example", remote(gateway.ResendMaxKeys))

	for _, name := range []string{"register", "resend-address", "resend-ip"} {
		if want := name + " throttle full; refusing new addresses"; strings.Count(logs.String(), want) != 1 {
			t.Errorf("WARN %q appears %d times, want once: %s", want, strings.Count(logs.String(), want), logs.String())
		}
	}
}

// allowHeaderSet parses Access-Control-Allow-Headers into a lowercase token set.
func allowHeaderSet(h http.Header) map[string]bool {
	set := map[string]bool{}
	for _, tok := range strings.Split(h.Get("Access-Control-Allow-Headers"), ",") {
		if tok = strings.ToLower(strings.TrimSpace(tok)); tok != "" {
			set[tok] = true
		}
	}
	return set
}

// The /api/ mount composes CORS outside the verifier as main does, so a bearer-less
// preflight carrying the trace headers is answered 204, not 401.
func TestApiMountPreflightGrantsTraceHeaders(t *testing.T) {
	const origin = "https://app.ascomply.test"
	issuer, err := auth.NewMockIssuer(mountTestIssuer)
	if err != nil {
		t.Fatalf("mock issuer: %v", err)
	}
	jwks := httptest.NewServer(issuer.JWKSHandler())
	t.Cleanup(jwks.Close)
	verifier, err := auth.NewVerifier(auth.Config{Issuer: mountTestIssuer, JWKSURL: jwks.URL})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	setUpstreamEnv(t, "http://127.0.0.1:1")
	routed, probed, err := loadUpstreams()
	if err != nil {
		t.Fatalf("loadUpstreams: %v", err)
	}
	apiHandler, _ := gatewayHandlers(verifier, nilURLSessions(), routed, probed, nil, slog.Default(), "gw-test-token")
	mux := http.NewServeMux()
	mux.Handle("/api/", gateway.CORS([]string{origin})(apiHandler))

	r := httptest.NewRequest(http.MethodOptions, "/api/tenancy/v1/me", nil)
	r.Header.Set("Origin", origin)
	r.Header.Set("Access-Control-Request-Method", http.MethodGet)
	r.Header.Set("Access-Control-Request-Headers", "authorization, content-type, sentry-trace, baggage")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("bearer-less preflight = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	got := allowHeaderSet(rec.Header())
	want := map[string]bool{"authorization": true, "content-type": true, "sentry-trace": true, "baggage": true}
	if len(got) != len(want) {
		t.Errorf("granted token set = %v, want exactly %v", got, want)
	}
	for tok := range want {
		if !got[tok] {
			t.Errorf("granted token set = %v, missing %q", got, tok)
		}
	}
	if got["traceparent"] {
		t.Errorf("granted token set = %v, must not grant traceparent", got)
	}

	// Control: the verifier is on the path, so a bearer-less GET is 401.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tenancy/v1/me", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("control: GET /api/tenancy/v1/me with no bearer = %d, want 401", rec.Code)
	}

	// Trace headers buy no bypass: the traced GET is still 401, and the 401 carries the grant.
	r = httptest.NewRequest(http.MethodGet, "/api/tenancy/v1/me", nil)
	r.Header.Set("Origin", origin)
	r.Header.Set("sentry-trace", "0123456789abcdef0123456789abcdef-0123456789abcdef-1")
	r.Header.Set("baggage", "sentry-environment=production")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("traced GET with no bearer = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Errorf("traced 401 Access-Control-Allow-Origin = %q, want %q", got, origin)
	}

	// A disallowed origin's bearer-less preflight is answered 204 with no grant.
	r = httptest.NewRequest(http.MethodOptions, "/api/tenancy/v1/me", nil)
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("Access-Control-Request-Method", http.MethodGet)
	r.Header.Set("Access-Control-Request-Headers", "authorization, sentry-trace, baggage, traceparent")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusNoContent {
		t.Errorf("disallowed-origin preflight = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "" {
		t.Errorf("disallowed-origin preflight Access-Control-Allow-Headers = %q, want none", got)
	}
}

// TestGatewayMainStartsTheAccountStateGrantAfterProvision pins main's wiring of the boot-grant gate:
// main needs Postgres, so it is scanned, not run. The gate itself is tested in account_state_grant_test.go.
func TestGatewayMainStartsTheAccountStateGrantAfterProvision(t *testing.T) {
	f, body := parseMain(t)

	selector := func(e ast.Expr) string {
		var parts []string
		for {
			switch x := e.(type) {
			case *ast.SelectorExpr:
				parts = append([]string{x.Sel.Name}, parts...)
				e = x.X
			case *ast.Ident:
				return strings.Join(append([]string{x.Name}, parts...), ".")
			default:
				return ""
			}
		}
	}
	stmtHolding := func(match func(*ast.CallExpr) bool) (idx, n int) {
		idx = -1
		for i, s := range body.List {
			ast.Inspect(s, func(nd ast.Node) bool {
				if c, ok := nd.(*ast.CallExpr); ok && match(c) {
					idx = i
					n++
				}
				return true
			})
		}
		return idx, n
	}

	starts := 0
	ast.Inspect(f, func(nd ast.Node) bool {
		if c, ok := nd.(*ast.CallExpr); ok {
			if _, is := isCallTo(c, "", "startAccountStateGrant"); is {
				starts++
			}
		}
		return true
	})
	if starts != 1 {
		t.Fatalf("main.go calls startAccountStateGrant %d times, want exactly 1", starts)
	}

	var start *ast.CallExpr
	startIdx := -1
	for i, s := range body.List {
		if es, ok := s.(*ast.ExprStmt); ok {
			if c, is := isCallTo(es.X, "", "startAccountStateGrant"); is {
				start, startIdx = c, i
			}
		}
	}
	if start == nil {
		t.Fatal("startAccountStateGrant is not a statement of its own directly in func main (not inside an if, go or func literal)")
	}

	provIdx, provN := stmtHolding(func(c *ast.CallExpr) bool { _, is := isCallTo(c, "db", "Provision"); return is })
	runIdx, runN := stmtHolding(func(c *ast.CallExpr) bool { _, is := isCallTo(c, "app", "Run"); return is })
	if provN != 1 || runN != 1 {
		t.Fatalf("main calls db.Provision %d times and app.Run %d times, want 1 each", provN, runN)
	}
	if !(provIdx < startIdx && startIdx < runIdx) {
		t.Errorf("statement order: db.Provision at %d, startAccountStateGrant at %d, app.Run at %d; want Provision < start < Run", provIdx, startIdx, runIdx)
	}

	if len(start.Args) != 5 {
		t.Fatalf("startAccountStateGrant has %d arguments, want 5", len(start.Args))
	}
	if c, is := isCallTo(start.Args[0], "os", "Getenv"); !is || len(c.Args) != 1 || !isStringLit(c.Args[0], "RAILWAY_ENVIRONMENT_NAME") {
		t.Errorf("argument 1 is not os.Getenv(\"RAILWAY_ENVIRONMENT_NAME\")")
	}
	if got := selector(start.Args[1]); got != "provisionCfg.MigrationDSN" {
		t.Errorf("argument 2 = %q, want provisionCfg.MigrationDSN", got)
	}
	if got := selector(start.Args[2]); got != "provisionCfg.Passwords.AuthAdmin" {
		t.Errorf("argument 3 = %q, want provisionCfg.Passwords.AuthAdmin", got)
	}
	if got := selector(start.Args[3]); got != "app.Logger" {
		t.Errorf("argument 4 = %q, want app.Logger", got)
	}
	lit, ok := start.Args[4].(*ast.FuncLit)
	if !ok || len(lit.Body.List) != 1 {
		t.Fatalf("argument 5 is not a func literal holding exactly one statement")
	}
	gs, ok := lit.Body.List[0].(*ast.GoStmt)
	if !ok {
		t.Fatalf("argument 5's statement is %T, want a go statement", lit.Body.List[0])
	}
	if _, is := isCallTo(gs.Call, "", "grantAccountStateRead"); !is || len(gs.Call.Args) < 3 {
		t.Fatalf("argument 5's go statement does not call grantAccountStateRead with at least 3 arguments")
	}
	if got := selector(gs.Call.Args[2]); got != "db.GrantAccountStateRead" {
		t.Errorf("grantAccountStateRead's third argument = %q, want db.GrantAccountStateRead", got)
	}

	reads := 0
	ast.Inspect(f, func(nd ast.Node) bool {
		if c, ok := nd.(*ast.CallExpr); ok {
			if _, is := isCallTo(c, "os", "Getenv"); is && len(c.Args) == 1 && isStringLit(c.Args[0], "AUTH_ADMIN_PASSWORD") {
				reads++
			}
		}
		return true
	})
	if reads != 1 {
		t.Errorf("main.go reads os.Getenv(\"AUTH_ADMIN_PASSWORD\") %d times, want 1 (the grant takes provisionCfg.Passwords.AuthAdmin)", reads)
	}
}
