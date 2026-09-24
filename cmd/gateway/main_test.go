// main_test.go: role-password rename fallback shim tests (M4-22-09/task-168).
// cmd/gateway/ had no test files before this one (main() itself isn't
// unit-testable -- it calls log.Fatalf and opens a real listener).
// Deliberately does NOT re-author TestBootstrapRejectsEmptyPasswords
// (internal/platform/db/bootstrap_test.go) or
// TestGatewayMainPassesRawEnvironmentToProvisioningGuard
// (internal/platform/db/provision_test.go, AC #6's named regression guard)
// -- both already cover what their names say.
package main

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
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
// and nowhere else (docs/migrations.md excepted, see below); every
// deprecated-prefix "*DATABASE_URL" hit must be zero, same exception --
// those two DSN vars are Railway-console/docs-only, no Go code reads them.
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
	// docs/migrations.md names the deprecated vars truthfully because Railway
	// still holds them (escalation E3 pending) -- not a stray code reference.
	const docsExceptionFile = "docs/migrations.md"
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
		if passwordPattern.MatchString(text) && rel != wantPasswordFile && rel != docsExceptionFile {
			passwordHitsElsewhere = append(passwordHitsElsewhere, rel)
		}
		if dsnPattern.MatchString(text) && rel != docsExceptionFile {
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

	apiHandler, fleetHandler := gatewayHandlers(verifier, routed, probed, nil, slog.Default())
	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandler)
	mux.HandleFunc("GET /healthz/fleet", fleetHandler)

	get := func(path string) int {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec.Code
	}

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
	for _, svc := range append(append([]string{"gateway", "auth"}, routedServices...), probedServices...) {
		if !seen[svc] {
			t.Errorf("/healthz/fleet omits %q -- the deploy gate cannot block on a service the roll-up never names", svc)
		}
	}
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
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "fatal" {
				fatals++
			}
		}
		return true
	})
	if fatals != 1 {
		t.Errorf("%s: the `%s != nil` guard calls fatal %d time(s), want 1 -- boot must stop, and it must stop at ERROR", path, errName, fatals)
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
				if _, ok := isCallTo(e, "", "fatal"); ok {
					fatals++
				}
			}
			return true
		})
		if parses != 1 || fatals != 1 {
			t.Errorf("mustParseIssuers calls auth.ParseTrustedIssuers %d time(s) and fatal %d time(s), want 1 and 1 -- a malformed AUTH_ADDITIONAL_ISSUERS must stop boot at ERROR", parses, fatals)
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

	// The verifier trusts exactly the set that is counted.
	fedToVerifier := false
	ast.Inspect(body, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if sel, ok := cl.Type.(*ast.SelectorExpr); !ok || sel.Sel.Name != "Config" {
			return true
		}
		for _, e := range cl.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			k, _ := kv.Key.(*ast.Ident)
			v, _ := kv.Value.(*ast.Ident)
			if k != nil && v != nil && k.Name == "Additional" && v.Name == setVar {
				fedToVerifier = true
			}
		}
		return true
	})
	if !fedToVerifier {
		t.Errorf("main's auth.Config has no `Additional: %s`", setVar)
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
				if len(c.Args) == 5 {
					arg = c.Args[3]
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
