// railway_env_auth_test.go pins railway-env.sh set-fork-auth, set-fork-auth-site and
// set-production-auth against a stateful scripted Railway: guards, writes, re-reads and redaction.
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

const (
	authForkEnvID     = "env-fork-auth"
	authForkName      = "pr-7"
	authStaleEnvID    = "env-fork-stale"
	authForkGatewayID = "svc-gw-fork"
	authForkAuthID    = "svc-auth-fork"
	authProdAuthID    = "svc-auth-production"

	authInternalURL  = "http://auth.railway.internal:8080"
	authJWKSURL      = authInternalURL + "/.well-known/jwks.json"
	authMockIssuer   = "https://mock.fiscalbridge.dev"
	authLoopbackJWKS = "http://127.0.0.1:8080/.well-known/jwks.json"
	authProdIssuer   = "urn:ascomply:auth:production"
	authForkIssuer   = "urn:ascomply:auth:" + authForkName
	authProdSiteURL  = "https://www.ascomply.com"
	authDSNReference = "postgresql://supabase_auth_admin:${{gateway.AUTH_ADMIN_PASSWORD}}@${{Postgres.RAILWAY_PRIVATE_DOMAIN}}:5432/${{Postgres.PGDATABASE}}"

	// Planted source values: what a fork inherits. None may be written back or printed.
	authSourceJWTSecret = "5eed0000000000000000000000000000000000000000000000000000000000aa"
	authSourcePassword  = "5eed0000000000000000000000000000000000000000000000000000000000bb"
	authSourceResendKey = "re_x_planted_source_resend_key"

	// Production inputs for --post-merge.
	authProdPassword  = "0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9"
	authProdJWTSecret = "f9e8d7c6b5a4938271605f4e3d2c1b0af9e8d7c6b5a4938271605f4e3d2c1b0a"
	authProdResendKey = "re_planted_production_resend_key"

	forkAuthSelfTestRunCmd = "bash scripts/ci/railway-env.sh set-fork-auth --self-test"
)

var (
	hex64         = regexp.MustCompile(`^[0-9a-f]{64}$`)
	redactedLine  = regexp.MustCompile(`^\s*[a-z-]+\.[A-Z0-9_]+ = <redacted>$`)
	authPersisted = regexp.MustCompile(`(?i)persistent environment \(` + regexp.QuoteMeta(persistentEnvironmentID) + `\)`)
)

// authShim is a curl on PATH that answers like Railway and keeps state: an upsert
// changes what the next read returns, per service. read-<svc>.jq bends a read.
type authShim struct {
	railwayShim
	argvLog string
}

func newAuthShim(t *testing.T, responses map[string]string, stores map[string]map[string]string) authShim {
	t.Helper()
	dir := t.TempDir()
	s := authShim{
		railwayShim: railwayShim{dir: dir, prelude: "export PATH='" + dir + "':\"$PATH\"\n", log: filepath.Join(dir, "calls.jsonl")},
		argvLog:     filepath.Join(dir, "argv.log"),
	}
	for op, body := range responses {
		writeFile(t, filepath.Join(dir, op+".json"), body)
	}
	for svc, vars := range stores {
		raw, err := json.Marshal(vars)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "store-"+svc+".json"), string(raw))
	}
	shim := `#!/bin/sh
dir='` + dir + `'
printf '%s\n' "$*" >> "$dir/argv.log"
data=""
while [ $# -gt 0 ]; do
  case "$1" in --data|--data-binary|--data-raw) data="$2"; shift ;; esac
  shift
done
case "$data" in
  @-) data=$(cat) ;;
  @*) data=$(cat "${data#@}") ;;
esac
printf '%s' "$data" | jq -c . >> "$dir/calls.jsonl"
q=$(printf '%s' "$data" | jq -r '.query')
case "$q" in
  *"variableUpsert("*)
    n=$(printf '%s' "$data" | jq -r '.variables.input.name')
    # upsert-<NAME>.fail fails the transport; upsert-<NAME>.json is the reply and nothing is stored.
    if [ -f "$dir/upsert-$n.fail" ]; then cat "$dir/upsert-$n.fail" >&2; exit 22; fi
    if [ -f "$dir/upsert-$n.json" ]; then cat "$dir/upsert-$n.json"; exit 0; fi
    s=$(printf '%s' "$data" | jq -r '.variables.input.serviceId')
    st="$dir/store-$s.json"; [ -f "$st" ] || echo '{}' > "$st"
    printf '%s' "$data" | jq -c --slurpfile st "$st" '$st[0] + {(.variables.input.name): .variables.input.value}' > "$st.tmp" && mv "$st.tmp" "$st"
    echo '{"data":{"variableUpsert":true}}' ;;
  *isSealed*)
    cat "$dir/sealed.json" ;;
  *"variables(projectId"*)
    s=$(printf '%s' "$data" | jq -r '.variables | (.s // .serviceId // empty)')
    st="$dir/store-$s.json"; [ -f "$st" ] || echo '{}' > "$st"
    if [ -f "$dir/read-$s.jq" ]; then v=$(jq -c -f "$dir/read-$s.jq" "$st"); else v=$(cat "$st"); fi
    printf '{"data":{"variables":%s}}' "$v" ;;
  *)
    op=$(printf '%s' "$data" | jq -r '.query | capture("^\\s*(query|mutation)\\s+(?<n>\\w+)").n')
    if [ -f "$dir/$op.json" ]; then cat "$dir/$op.json"; else echo '{"errors":[{"message":"unrouted"}]}'; fi ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	return s
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// bendRead makes every later read of svc pass through the jq filter.
func (s authShim) bendRead(t *testing.T, svc, filter string) {
	t.Helper()
	writeFile(t, filepath.Join(s.dir, "read-"+svc+".jq"), filter)
}

func (s authShim) run(t *testing.T, exports, sub string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runBashScript(t, s.prelude+exports+"bash '"+railwayEnvScript(t)+"' "+sub+" \"$@\"\n", args...)
}

type authUpsert struct{ Service, Name, Value string }

// upserts lists every variableUpsert the shim received, in order.
func (s authShim) upserts(t *testing.T) []authUpsert {
	t.Helper()
	var out []authUpsert
	for _, c := range s.calls(t) {
		if !strings.Contains(c.Query, "variableUpsert(") {
			continue
		}
		in, _ := c.Variables["input"].(map[string]any)
		svc, _ := in["serviceId"].(string)
		name, _ := in["name"].(string)
		value, _ := in["value"].(string)
		out = append(out, authUpsert{svc, name, value})
	}
	return out
}

func (s authShim) mutations(t *testing.T) []string {
	t.Helper()
	var ops []string
	for _, c := range s.calls(t) {
		if strings.HasPrefix(strings.TrimSpace(c.Query), "mutation") {
			ops = append(ops, operations([]railwayCall{c})...)
		}
	}
	return ops
}

// readAfter reports whether svc's variables were read after call index i.
func (s authShim) readAfter(t *testing.T, svc string, i int) bool {
	t.Helper()
	for _, c := range s.calls(t)[i+1:] {
		if strings.Contains(c.Query, "variables(projectId") && (c.Variables["s"] == svc || c.Variables["serviceId"] == svc) {
			return true
		}
	}
	return false
}

// lastCallIndex returns the index of the last upsert of svc.name, or -1.
func (s authShim) lastCallIndex(t *testing.T, svc, name string) int {
	t.Helper()
	at := -1
	for i, c := range s.calls(t) {
		if !strings.Contains(c.Query, "variableUpsert(") {
			continue
		}
		in, _ := c.Variables["input"].(map[string]any)
		if in["serviceId"] == svc && in["name"] == name {
			at = i
		}
	}
	return at
}

func (s authShim) argv(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(s.argvLog)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

func upsertsOf(ups []authUpsert, svc, name string) []authUpsert {
	var out []authUpsert
	for _, u := range ups {
		if u.Service == svc && u.Name == name {
			out = append(out, u)
		}
	}
	return out
}

// oneUpsert fails unless svc.name was upserted exactly once, and returns its value.
func oneUpsert(t *testing.T, ups []authUpsert, svc, name string) string {
	t.Helper()
	got := upsertsOf(ups, svc, name)
	if len(got) != 1 {
		t.Errorf("%s.%s upserted %d time(s), want exactly 1; upserts = %v", svc, name, len(got), names(ups))
		if len(got) == 0 {
			return ""
		}
	}
	return got[0].Value
}

func names(ups []authUpsert) []string {
	var out []string
	for _, u := range ups {
		out = append(out, u.Service+"."+u.Name)
	}
	return out
}

func freshJWK(t *testing.T) string {
	t.Helper()
	out, err := exec.Command(binPath, "jwk-es256").Output()
	if err != nil {
		t.Fatalf("prenv jwk-es256: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func jwkCheck(t *testing.T, value string) (string, bool) {
	t.Helper()
	cmd := exec.Command(binPath, "jwk-check")
	cmd.Stdin = strings.NewReader(value)
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

// jwkPrivateScalar returns the "d" member, which must never be printed.
func jwkPrivateScalar(t *testing.T, value string) string {
	t.Helper()
	var keys []map[string]any
	if err := json.Unmarshal([]byte(value), &keys); err != nil || len(keys) == 0 {
		t.Fatalf("not a JWK array: %v", err)
	}
	d, _ := keys[0]["d"].(string)
	if d == "" {
		t.Fatal("the JWK has no d member")
	}
	return d
}

func authEnvList(ephemeralStale bool) string {
	return fmt.Sprintf(`{"data":{"environments":{"edges":[`+
		`{"node":{"id":"%s","name":"production","isEphemeral":false}},`+
		`{"node":{"id":"%s","name":"%s","isEphemeral":true}},`+
		`{"node":{"id":"%s","name":"pr-8","isEphemeral":%t}}]}}}`,
		persistentEnvironmentID, authForkEnvID, authForkName, authStaleEnvID, ephemeralStale)
}

// forkAuthStores is what a fork inherits from production after U3b, before set-fork-auth runs.
func forkAuthStores(sourceJWK string) map[string]map[string]string {
	return map[string]map[string]string{
		authForkGatewayID: {
			"RAILWAY_ENVIRONMENT_NAME": authForkName,
			"AUTH_ISSUER":              authProdIssuer,
			"AUTH_JWKS_URL":            authJWKSURL,
			"AUTH_URL":                 authInternalURL,
			"AUTH_ADMIN_PASSWORD":      authSourcePassword,
			"DATABASE_MIGRATION_URL":   "postgresql://invoice_migrator:" + forkEnvSecret + "@h:5432/railway",
		},
		authForkAuthID: {
			"RAILWAY_ENVIRONMENT_NAME": authForkName,
			"PORT":                     "8080",
			"API_EXTERNAL_URL":         authInternalURL,
			"DATABASE_URL":             authDSNReference,
			"GOTRUE_SITE_URL":          authProdSiteURL,
			"GOTRUE_JWT_ISSUER":        authProdIssuer,
			"GOTRUE_JWT_KEYS":          sourceJWK,
			"GOTRUE_JWT_SECRET":        authSourceJWTSecret,
			"GOTRUE_SMTP_PASS":         authSourceResendKey,
		},
	}
}

func forkAuthRailway() map[string]string {
	return map[string]string{
		"envList": authEnvList(true),
		"settle": forkSettle(
			`{"node":{"serviceId":"`+authForkGatewayID+`","serviceName":"gateway"}}`,
			`{"node":{"serviceId":"`+authForkAuthID+`","serviceName":"auth"}}`,
		),
	}
}

func newForkAuthShim(t *testing.T, sourceJWK string) authShim {
	t.Helper()
	return newAuthShim(t, forkAuthRailway(), forkAuthStores(sourceJWK))
}

func forkAuthExports() string { return forkExports(true, true, true) }

// runForkAuthOK runs set-fork-auth against a fresh shim and fails unless it exits 0.
func runForkAuthOK(t *testing.T, sourceJWK string) (authShim, string) {
	t.Helper()
	s := newForkAuthShim(t, sourceJWK)
	stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth", authForkEnvID)
	out := stdout + stderr
	if code != 0 {
		t.Fatalf("set-fork-auth exit %d, want 0; output = %q", code, out)
	}
	return s, out
}

func TestSetForkAuth_RefusesPersistentAndNonEphemeral(t *testing.T) {
	t.Run("control: a pr fork is written", func(t *testing.T) {
		s, _ := runForkAuthOK(t, freshJWK(t))
		if len(s.mutations(t)) == 0 {
			t.Fatal("control: the fork run wrote nothing, so a zero-write refusal proves nothing")
		}
	})
	t.Run("the persistent id", func(t *testing.T) {
		s := newForkAuthShim(t, freshJWK(t))
		stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth", persistentEnvironmentID)
		out := stdout + stderr
		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, out)
		}
		if !authPersisted.MatchString(errorLines(out)) {
			t.Errorf("no ::error:: line refuses the persistent environment (%s) by id; output = %q", persistentEnvironmentID, out)
		}
		if calls := s.calls(t); len(calls) != 0 {
			t.Errorf("the persistent-id refusal called Railway %v; it must refuse before any network call", operations(calls))
		}
		s.requireLogs(t)
	})
	t.Run("a non-ephemeral id", func(t *testing.T) {
		resp := forkAuthRailway()
		resp["envList"] = authEnvList(false)
		s := newAuthShim(t, resp, forkAuthStores(freshJWK(t)))
		stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth", authStaleEnvID)
		out := stdout + stderr
		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, out)
		}
		if !strings.Contains(errorLines(out), "is NOT ephemeral") {
			t.Errorf("no ::error:: line says the environment is NOT ephemeral; output = %q", out)
		}
		if m := s.mutations(t); len(m) != 0 {
			t.Errorf("a non-ephemeral environment received mutations %v", m)
		}
		if ops := operations(s.calls(t)); !slices.Contains(ops, "envList") {
			t.Errorf("Railway calls = %v; the ephemeral check never listed environments", ops)
		}
	})
}

func TestSetForkAuth_WritesFreshKeysNeverTheSourceValue(t *testing.T) {
	k0 := freshJWK(t)
	if _, ok := jwkCheck(t, k0); !ok {
		t.Fatal("control: the planted source key does not pass jwk-check, so 'differs from K0' would be trivial")
	}
	s1, _ := runForkAuthOK(t, k0)
	s2, _ := runForkAuthOK(t, k0)
	ups1, ups2 := s1.upserts(t), s2.upserts(t)

	key1 := oneUpsert(t, ups1, authForkAuthID, "GOTRUE_JWT_KEYS")
	key2 := oneUpsert(t, ups2, authForkAuthID, "GOTRUE_JWT_KEYS")
	if key1 == "" {
		t.Fatal("no key was written")
	}
	if key1 == k0 {
		t.Error("the fork's GOTRUE_JWT_KEYS equals the source key; it must be freshly generated")
	}
	if key1 == key2 {
		t.Error("two runs wrote the same GOTRUE_JWT_KEYS; the key is not generated per run")
	}
	if out, ok := jwkCheck(t, key1); !ok {
		t.Errorf("the written key fails jwk-check: %s", out)
	}

	sec1 := oneUpsert(t, ups1, authForkAuthID, "GOTRUE_JWT_SECRET")
	sec2 := oneUpsert(t, ups2, authForkAuthID, "GOTRUE_JWT_SECRET")
	if !hex64.MatchString(sec1) {
		t.Errorf("GOTRUE_JWT_SECRET is not 64 lowercase hex characters (len %d)", len(sec1))
	}
	if sec1 == authSourceJWTSecret || sec1 == sec2 {
		t.Error("GOTRUE_JWT_SECRET is the source value or repeats across runs; it must be freshly generated")
	}
}

// Sealed production secrets are absent in a fork, not empty; the write must not depend on them.
func TestSetForkAuth_SealedSourceVariablesAbsent(t *testing.T) {
	stores := forkAuthStores("")
	for _, n := range []string{"GOTRUE_JWT_KEYS", "GOTRUE_JWT_SECRET", "GOTRUE_SMTP_PASS"} {
		delete(stores[authForkAuthID], n)
	}
	s := newAuthShim(t, forkAuthRailway(), stores)
	stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth", authForkEnvID)
	if code != 0 {
		t.Fatalf("exit %d with the sealed secrets absent, want 0; output = %q", code, stdout+stderr)
	}
	ups := s.upserts(t)
	if key := oneUpsert(t, ups, authForkAuthID, "GOTRUE_JWT_KEYS"); key != "" {
		if _, ok := jwkCheck(t, key); !ok {
			t.Error("the written key fails jwk-check")
		}
	}
	if !hex64.MatchString(oneUpsert(t, ups, authForkAuthID, "GOTRUE_JWT_SECRET")) {
		t.Error("GOTRUE_JWT_SECRET is not 64 lowercase hex characters")
	}
}

func TestSetForkAuthSite_WritesAndReReadsSiteURL(t *testing.T) {
	const site = "https://landing-pr-7.up.railway.app"

	t.Run("a valid https URL", func(t *testing.T) {
		s := newForkAuthShim(t, freshJWK(t))
		stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth-site", authForkEnvID, site)
		out := stdout + stderr
		if code != 0 {
			t.Fatalf("exit %d, want 0; output = %q", code, out)
		}
		ups := s.upserts(t)
		if len(ups) != 1 || ups[0] != (authUpsert{authForkAuthID, "GOTRUE_SITE_URL", site}) {
			t.Errorf("upserts = %v, want exactly auth.GOTRUE_SITE_URL=%s", ups, site)
		}
		if at := s.lastCallIndex(t, authForkAuthID, "GOTRUE_SITE_URL"); at < 0 || !s.readAfter(t, authForkAuthID, at) {
			t.Error("auth's variables were not re-read after the GOTRUE_SITE_URL write")
		}
	})

	t.Run("the re-read differs", func(t *testing.T) {
		s := newForkAuthShim(t, freshJWK(t))
		s.bendRead(t, authForkAuthID, `.GOTRUE_SITE_URL = "https://elsewhere.example"`)
		stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth-site", authForkEnvID, site)
		out := stdout + stderr
		if code != 1 || !strings.Contains(errorLines(out), "GOTRUE_SITE_URL") {
			t.Errorf("exit %d and error lines %q, want exit 1 naming GOTRUE_SITE_URL", code, errorLines(out))
		}
	})

	for _, c := range []struct {
		name, env, url string
		says           *regexp.Regexp
	}{
		{"an empty URL", authForkEnvID, "", regexp.MustCompile(`https://`)},
		{"an http URL", authForkEnvID, "http://x", regexp.MustCompile(`https://`)},
		{"the persistent id", persistentEnvironmentID, site, authPersisted},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newForkAuthShim(t, freshJWK(t))
			stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth-site", c.env, c.url)
			out := stdout + stderr
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			if !c.says.MatchString(errorLines(out)) || strings.Contains(out, "usage:") {
				t.Errorf("error lines %q do not carry %q (and must not be the usage line)", errorLines(out), c.says)
			}
			if m := s.mutations(t); len(m) != 0 {
				t.Errorf("the refusal wrote %v", m)
			}
		})
	}

	t.Run("set-fork-auth does not write the site URL", func(t *testing.T) {
		s, _ := runForkAuthOK(t, freshJWK(t))
		ups := s.upserts(t)
		if len(ups) == 0 {
			t.Fatal("control: set-fork-auth wrote nothing")
		}
		if got := upsertsOf(ups, authForkAuthID, "GOTRUE_SITE_URL"); len(got) != 0 {
			t.Errorf("set-fork-auth upserted GOTRUE_SITE_URL %d time(s); only set-fork-auth-site may", len(got))
		}
	})
}

func TestSetForkAuth_IssuerAndAdditionalSetAgree(t *testing.T) {
	s, _ := runForkAuthOK(t, freshJWK(t))
	ups := s.upserts(t)

	if got := oneUpsert(t, ups, authForkAuthID, "GOTRUE_JWT_ISSUER"); got != authForkIssuer {
		t.Errorf("auth.GOTRUE_JWT_ISSUER = %q, want %q", got, authForkIssuer)
	}

	raw := oneUpsert(t, ups, authForkGatewayID, "AUTH_ADDITIONAL_ISSUERS")
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var set []struct {
		Issuer  string `json:"issuer"`
		JWKSURL string `json:"jwks_url"`
	}
	if err := dec.Decode(&set); err != nil {
		t.Fatalf("gateway.AUTH_ADDITIONAL_ISSUERS %q is not an array of {issuer, jwks_url}: %v", raw, err)
	}
	if len(set) != 1 {
		t.Fatalf("AUTH_ADDITIONAL_ISSUERS has %d element(s), want 1: %q", len(set), raw)
	}
	if set[0].Issuer != authForkIssuer {
		t.Errorf("AUTH_ADDITIONAL_ISSUERS[0].issuer = %q, want %q (the value written to GOTRUE_JWT_ISSUER)", set[0].Issuer, authForkIssuer)
	}
	if set[0].JWKSURL != authJWKSURL {
		t.Errorf("AUTH_ADDITIONAL_ISSUERS[0].jwks_url = %q, want %q", set[0].JWKSURL, authJWKSURL)
	}

	for name, want := range map[string]string{
		"AUTH_ISSUER":   authMockIssuer,
		"AUTH_JWKS_URL": authLoopbackJWKS,
		"AUTH_URL":      authInternalURL,
	} {
		if got := oneUpsert(t, ups, authForkGatewayID, name); got != want {
			t.Errorf("gateway.%s = %q, want %q", name, got, want)
		}
	}
	for name, want := range map[string]string{"PORT": "8080", "API_EXTERNAL_URL": authInternalURL} {
		if got := oneUpsert(t, ups, authForkAuthID, name); got != want {
			t.Errorf("auth.%s = %q, want %q", name, got, want)
		}
	}
}

func TestSetForkAuth_ReReadMismatchFails(t *testing.T) {
	cases := []struct {
		name, svc, variable, filter string
	}{
		{"AUTH_URL differs", authForkGatewayID, "AUTH_URL", `.AUTH_URL = "http://wrong.internal:8080"`},
		{"AUTH_URL absent", authForkGatewayID, "AUTH_URL", `del(.AUTH_URL)`},
		{"AUTH_URL empty", authForkGatewayID, "AUTH_URL", `.AUTH_URL = ""`},
		{"AUTH_ISSUER differs", authForkGatewayID, "AUTH_ISSUER", `.AUTH_ISSUER = "` + authProdIssuer + `"`},
		{"AUTH_JWKS_URL differs", authForkGatewayID, "AUTH_JWKS_URL", `.AUTH_JWKS_URL = "` + authJWKSURL + `"`},
		{"AUTH_ADDITIONAL_ISSUERS differs", authForkGatewayID, "AUTH_ADDITIONAL_ISSUERS", `.AUTH_ADDITIONAL_ISSUERS = "[]"`},
		{"GOTRUE_JWT_ISSUER differs", authForkAuthID, "GOTRUE_JWT_ISSUER", `.GOTRUE_JWT_ISSUER = "` + authProdIssuer + `"`},
		{"DATABASE_URL differs", authForkAuthID, "DATABASE_URL", `.DATABASE_URL = "postgresql://other"`},
		{"API_EXTERNAL_URL absent", authForkAuthID, "API_EXTERNAL_URL", `del(.API_EXTERNAL_URL)`},
		{"PORT differs", authForkAuthID, "PORT", `.PORT = "9999"`},
		{"GOTRUE_SMTP_HOST not blank", authForkAuthID, "GOTRUE_SMTP_HOST", `.GOTRUE_SMTP_HOST = "smtp.resend.com"`},
		{"GOTRUE_SMTP_PASS not blank", authForkAuthID, "GOTRUE_SMTP_PASS", `.GOTRUE_SMTP_PASS = "re_reappeared"`},
		{"secret AUTH_ADMIN_PASSWORD absent", authForkGatewayID, "AUTH_ADMIN_PASSWORD", `del(.AUTH_ADMIN_PASSWORD)`},
		{"secret AUTH_ADMIN_PASSWORD empty", authForkGatewayID, "AUTH_ADMIN_PASSWORD", `.AUTH_ADMIN_PASSWORD = ""`},
		{"secret GOTRUE_JWT_SECRET absent", authForkAuthID, "GOTRUE_JWT_SECRET", `del(.GOTRUE_JWT_SECRET)`},
		{"secret GOTRUE_JWT_KEYS not one signing key", authForkAuthID, "GOTRUE_JWT_KEYS", `.GOTRUE_JWT_KEYS = "[]"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newForkAuthShim(t, freshJWK(t))
			s.bendRead(t, c.svc, c.filter)
			stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth", authForkEnvID)
			out := stdout + stderr
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			if !strings.Contains(errorLines(out), c.variable) {
				t.Errorf("no ::error:: line names %s; error lines = %q", c.variable, errorLines(out))
			}
			if len(s.upserts(t)) == 0 {
				t.Error("nothing was written, so the failure is not a re-read failure")
			}
		})
	}
}

func TestSetForkAuth_WritesFreshAdminPasswordAndDSNReference(t *testing.T) {
	s, _ := runForkAuthOK(t, freshJWK(t))
	ups := s.upserts(t)
	pw := oneUpsert(t, ups, authForkGatewayID, "AUTH_ADMIN_PASSWORD")
	if !hex64.MatchString(pw) {
		t.Errorf("gateway.AUTH_ADMIN_PASSWORD is not 64 lowercase hex characters (len %d)", len(pw))
	}
	if pw == authSourcePassword {
		t.Error("gateway.AUTH_ADMIN_PASSWORD equals the source value; it must be freshly generated")
	}
	if got := oneUpsert(t, ups, authForkAuthID, "DATABASE_URL"); got != authDSNReference {
		t.Errorf("auth.DATABASE_URL = %q, want the reference %q", got, authDSNReference)
	}
}

func TestSetForkAuth_BlanksForkSMTP(t *testing.T) {
	s, out := runForkAuthOK(t, freshJWK(t))
	ups := s.upserts(t)
	for _, name := range []string{"GOTRUE_SMTP_HOST", "GOTRUE_SMTP_PASS"} {
		got := upsertsOf(ups, authForkAuthID, name)
		if len(got) != 1 || got[0].Value != "" {
			t.Errorf("auth.%s upserts = %v, want exactly one, set to \"\"", name, got)
		}
	}
	if strings.Contains(out, authSourceResendKey) {
		t.Error("the output carries the source Resend key")
	}
}

// secretScan fails when any needle appears in the output or the curl argv log.
func secretScan(t *testing.T, s authShim, out string, needles map[string]string) {
	t.Helper()
	if len(needles) == 0 {
		t.Fatal("no secret needles to scan for")
	}
	t.Run("stdout and stderr", func(t *testing.T) {
		for label, n := range needles {
			if n == "" {
				t.Errorf("needle %s is empty, so the scan proves nothing", label)
				continue
			}
			if strings.Contains(out, n) {
				t.Errorf("the output carries %s", label)
			}
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "<redacted>") && !redactedLine.MatchString(line) {
				t.Errorf("a redacted line carries more than `label.NAME = <redacted>`: %q", line)
			}
		}
	})
	t.Run("curl argv", func(t *testing.T) {
		argv := s.argv(t)
		if !strings.Contains(argv, "backboard.railway.com") {
			t.Fatalf("control: the argv log holds no Railway call, so a clean scan proves nothing")
		}
		for label, n := range needles {
			if n != "" && strings.Contains(argv, n) {
				t.Errorf("curl's argv carries %s; a secret must reach Railway on stdin, never argv (visible in ps)", label)
			}
		}
	})
}

func requireRedacted(t *testing.T, out string, labels ...string) {
	t.Helper()
	for _, l := range labels {
		if !regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(l) + ` = <redacted>$`).MatchString(out) {
			t.Errorf("no `%s = <redacted>` line; output = %q", l, out)
		}
	}
}

func TestAuthWritesNeverPrintSecrets(t *testing.T) {
	k0 := freshJWK(t)
	planted := map[string]string{
		"the source key's private scalar": jwkPrivateScalar(t, k0),
		"the source JWT secret":           authSourceJWTSecret,
		"the source admin password":       authSourcePassword,
		"the source Resend key":           authSourceResendKey,
	}

	t.Run("set-fork-auth success", func(t *testing.T) {
		s, out := runForkAuthOK(t, k0)
		ups := s.upserts(t)
		key := oneUpsert(t, ups, authForkAuthID, "GOTRUE_JWT_KEYS")
		needles := map[string]string{
			"the generated JWT secret":     oneUpsert(t, ups, authForkAuthID, "GOTRUE_JWT_SECRET"),
			"the generated admin password": oneUpsert(t, ups, authForkGatewayID, "AUTH_ADMIN_PASSWORD"),
			"the generated key":            key,
		}
		if key != "" {
			needles["the generated key's private scalar"] = jwkPrivateScalar(t, key)
		}
		for k, v := range planted {
			needles[k] = v
		}
		requireRedacted(t, out, "auth.GOTRUE_JWT_KEYS", "auth.GOTRUE_JWT_SECRET", "gateway.AUTH_ADMIN_PASSWORD")
		secretScan(t, s, out, needles)
	})

	t.Run("set-fork-auth failure", func(t *testing.T) {
		const mangled = "PLANTED-MANGLED-JWK-VALUE"
		s := newForkAuthShim(t, k0)
		s.bendRead(t, authForkAuthID, `.GOTRUE_JWT_KEYS = "`+mangled+`"`)
		stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth", authForkEnvID)
		out := stdout + stderr
		if code != 1 {
			t.Fatalf("exit %d, want 1 on a mangled key re-read; output = %q", code, out)
		}
		ups := s.upserts(t)
		needles := map[string]string{
			"the mangled key":              mangled,
			"the generated JWT secret":     oneUpsert(t, ups, authForkAuthID, "GOTRUE_JWT_SECRET"),
			"the generated admin password": oneUpsert(t, ups, authForkGatewayID, "AUTH_ADMIN_PASSWORD"),
		}
		for k, v := range planted {
			needles[k] = v
		}
		secretScan(t, s, out, needles)
	})

	prodJWK := freshJWK(t)
	prodNeedles := map[string]string{
		"the production key":                  prodJWK,
		"the production key's private scalar": jwkPrivateScalar(t, prodJWK),
		"the production JWT secret":           authProdJWTSecret,
		"the production admin password":       authProdPassword,
		"the production Resend key":           authProdResendKey,
	}

	t.Run("set-production-auth --post-merge success", func(t *testing.T) {
		s := newProdAuthShim(t, prodSealed())
		stdout, stderr, code := s.run(t, prodAuthExports(authProdPassword, prodJWK, authProdJWTSecret, authProdResendKey), "set-production-auth", "--post-merge", persistentEnvironmentID)
		out := stdout + stderr
		if code != 0 {
			t.Fatalf("exit %d, want 0; output = %q", code, out)
		}
		requireRedacted(t, out, "auth.GOTRUE_JWT_KEYS", "auth.GOTRUE_JWT_SECRET", "auth.GOTRUE_SMTP_PASS", "gateway.AUTH_ADMIN_PASSWORD")
		secretScan(t, s, out, prodNeedles)
	})

	t.Run("set-production-auth --post-merge failures", func(t *testing.T) {
		const badPW = "zz-planted-non-hex-admin-password"
		for _, c := range []struct {
			name, pw string
			sealed   string
			bend     string
		}{
			{"a sealed target", authProdPassword, prodSealed("GOTRUE_JWT_SECRET"), ""},
			{"a non-hex password", badPW, prodSealed(), ""},
			{"a secret absent on re-read", authProdPassword, prodSealed(), `del(.GOTRUE_JWT_SECRET)`},
		} {
			t.Run(c.name, func(t *testing.T) {
				s := newProdAuthShim(t, c.sealed)
				if c.bend != "" {
					s.bendRead(t, authProdAuthID, c.bend)
				}
				stdout, stderr, code := s.run(t, prodAuthExports(c.pw, prodJWK, authProdJWTSecret, authProdResendKey), "set-production-auth", "--post-merge", persistentEnvironmentID)
				out := stdout + stderr
				if code != 1 {
					t.Fatalf("exit %d, want 1; output = %q", code, out)
				}
				needles := map[string]string{"the non-hex password": badPW}
				for k, v := range prodNeedles {
					needles[k] = v
				}
				if c.name == "a non-hex password" {
					// Refused before any network call, so there is no argv to scan.
					for label, n := range needles {
						if strings.Contains(out, n) {
							t.Errorf("the output carries %s", label)
						}
					}
					return
				}
				secretScan(t, s, out, needles)
			})
		}
	})
}

// prodSealed answers the sealed-variable read; each named variable is sealed on auth.
func prodSealed(sealed ...string) string {
	edges := []string{`{"node":{"name":"ENVIRONMENT","isSealed":false,"serviceId":"` + productionGatewayID + `"}}`}
	for _, n := range sealed {
		edges = append(edges, `{"node":{"name":"`+n+`","isSealed":true,"serviceId":"`+authProdAuthID+`"}}`)
	}
	return `{"data":{"environment":{"id":"` + persistentEnvironmentID + `","name":"production","variables":{"edges":[` + strings.Join(edges, ",") + `]}}}}`
}

func newProdAuthShim(t *testing.T, sealed string) authShim {
	t.Helper()
	return newAuthShim(t, map[string]string{
		"settle": forkSettle(
			`{"node":{"serviceId":"`+productionGatewayID+`","serviceName":"gateway"}}`,
			`{"node":{"serviceId":"`+authProdAuthID+`","serviceName":"auth"}}`,
		),
		"sealedAudit": sealed,
		"sealed":      sealed,
	}, map[string]map[string]string{
		productionGatewayID: {"RAILWAY_ENVIRONMENT_NAME": "production", "AUTH_ISSUER": authMockIssuer, "AUTH_JWKS_URL": authLoopbackJWKS},
		authProdAuthID:      {"RAILWAY_ENVIRONMENT_NAME": "production"},
	})
}

// prodAuthExports sets each non-empty secret input, and the token, project and persistent id.
func prodAuthExports(pw, keys, secret, resend string) string {
	var b strings.Builder
	b.WriteString(forkExports(true, true, true))
	for name, v := range map[string]string{"AUTH_ADMIN_PASSWORD": pw, "AUTH_JWT_KEYS": keys, "AUTH_JWT_SECRET": secret, "RESEND_API_KEY": resend} {
		if v != "" {
			b.WriteString("export " + name + "='" + v + "'\n")
		}
	}
	return b.String()
}

func TestSetProductionAuth_Guards(t *testing.T) {
	jwk := freshJWK(t)
	valid := func() map[string]string {
		return map[string]string{"pw": authProdPassword, "keys": jwk, "secret": authProdJWTSecret}
	}
	cases := []struct {
		name   string
		args   []string
		inputs func(map[string]string)
		says   *regexp.Regexp
		leak   string
	}{
		{"a fork id, pre-merge", []string{"--pre-merge", authForkEnvID}, nil, onlyThePersistentEnvironment, ""},
		{"a fork id, post-merge", []string{"--post-merge", authForkEnvID}, nil, onlyThePersistentEnvironment, ""},
		{"no phase flag", []string{persistentEnvironmentID}, nil, regexp.MustCompile(`--pre-merge.*--post-merge|--post-merge.*--pre-merge`), ""},
		{"AUTH_JWT_KEYS missing", []string{"--post-merge", persistentEnvironmentID}, func(m map[string]string) { m["keys"] = "" }, regexp.MustCompile(`AUTH_JWT_KEYS`), ""},
		{"AUTH_JWT_SECRET missing", []string{"--post-merge", persistentEnvironmentID}, func(m map[string]string) { m["secret"] = "" }, regexp.MustCompile(`AUTH_JWT_SECRET`), ""},
		{"AUTH_ADMIN_PASSWORD missing", []string{"--post-merge", persistentEnvironmentID}, func(m map[string]string) { m["pw"] = "" }, regexp.MustCompile(`AUTH_ADMIN_PASSWORD`), ""},
		{"AUTH_ADMIN_PASSWORD not hex", []string{"--post-merge", persistentEnvironmentID}, func(m map[string]string) { m["pw"] = "a:b@c" }, regexp.MustCompile(`AUTH_ADMIN_PASSWORD.*hex|hex.*AUTH_ADMIN_PASSWORD`), "a:b@c"},
		{"AUTH_ADMIN_PASSWORD upper-case hex", []string{"--post-merge", persistentEnvironmentID}, func(m map[string]string) { m["pw"] = strings.ToUpper(authProdPassword) }, regexp.MustCompile(`AUTH_ADMIN_PASSWORD.*hex|hex.*AUTH_ADMIN_PASSWORD`), strings.ToUpper(authProdPassword)},
		{"AUTH_ADMIN_PASSWORD 63 hex", []string{"--post-merge", persistentEnvironmentID}, func(m map[string]string) { m["pw"] = authProdPassword[:63] }, regexp.MustCompile(`AUTH_ADMIN_PASSWORD.*hex|hex.*AUTH_ADMIN_PASSWORD`), authProdPassword[:63]},
	}
	t.Run("control: valid post-merge inputs reach Railway", func(t *testing.T) {
		s := newProdAuthShim(t, prodSealed())
		in := valid()
		if _, _, code := s.run(t, prodAuthExports(in["pw"], in["keys"], in["secret"], ""), "set-production-auth", "--post-merge", persistentEnvironmentID); code != 0 || len(s.calls(t)) == 0 {
			t.Fatalf("control: valid inputs exit %d after %d call(s), want exit 0 after at least one", code, len(s.calls(t)))
		}
	})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := valid()
			if c.inputs != nil {
				c.inputs(in)
			}
			for _, token := range []bool{true, false} {
				s := newProdAuthShim(t, prodSealed())
				exports := strings.Replace(prodAuthExports(in["pw"], in["keys"], in["secret"], ""), forkExports(true, true, true), forkExports(token, true, true), 1)
				args := append([]string{"set-production-auth"}, c.args...)
				stdout, stderr, code := runBashScript(t, s.prelude+exports+"bash '"+railwayEnvScript(t)+"' \"$@\"\n", args...)
				out := stdout + stderr
				if code != 1 {
					t.Errorf("token=%t: exit %d, want 1; output = %q", token, code, out)
				}
				if !c.says.MatchString(errorLines(out)) {
					t.Errorf("token=%t: error lines %q do not carry %q", token, errorLines(out), c.says)
				}
				if strings.Contains(out, "RAILWAY_API_TOKEN is not set") {
					t.Errorf("token=%t: require_env ran before the guard; output = %q", token, out)
				}
				if calls := s.calls(t); len(calls) != 0 {
					t.Errorf("token=%t: the refusal called Railway %v", token, operations(calls))
				}
				if c.leak != "" && strings.Contains(out, c.leak) {
					t.Errorf("token=%t: the refusal printed the refused password", token)
				}
				s.requireLogs(t)
			}
		})
	}
}

func TestSetProductionAuth_PreMergeWritesOnlyAuthURL(t *testing.T) {
	s := newProdAuthShim(t, prodSealed())
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "set-production-auth", "--pre-merge", persistentEnvironmentID)
	out := stdout + stderr
	if code != 0 {
		t.Fatalf("exit %d, want 0 (no secret input is needed before merge); output = %q", code, out)
	}
	ups := s.upserts(t)
	if want := (authUpsert{productionGatewayID, "AUTH_URL", authInternalURL}); len(ups) != 1 || ups[0] != want {
		t.Errorf("upserts = %v, want exactly %v", ups, want)
	}
	if m := s.mutations(t); len(m) != 1 {
		t.Errorf("mutations = %v, want exactly one", m)
	}
	if at := s.lastCallIndex(t, productionGatewayID, "AUTH_URL"); at < 0 || !s.readAfter(t, productionGatewayID, at) {
		t.Error("the gateway's variables were not re-read after the AUTH_URL write")
	}

	t.Run("a re-read mismatch fails", func(t *testing.T) {
		s := newProdAuthShim(t, prodSealed())
		s.bendRead(t, productionGatewayID, `del(.AUTH_URL)`)
		stdout, stderr, code := s.run(t, forkExports(true, true, true), "set-production-auth", "--pre-merge", persistentEnvironmentID)
		if code != 1 || !strings.Contains(errorLines(stdout+stderr), "AUTH_URL") {
			t.Errorf("exit %d and error lines %q, want exit 1 naming AUTH_URL", code, errorLines(stdout+stderr))
		}
	})
}

func TestSetProductionAuth_PostMergeRefusesSealedTarget(t *testing.T) {
	jwk := freshJWK(t)
	for _, name := range []string{"GOTRUE_JWT_KEYS", "GOTRUE_JWT_SECRET", "GOTRUE_SMTP_PASS"} {
		t.Run(name, func(t *testing.T) {
			s := newProdAuthShim(t, prodSealed(name))
			stdout, stderr, code := s.run(t, prodAuthExports(authProdPassword, jwk, authProdJWTSecret, authProdResendKey), "set-production-auth", "--post-merge", persistentEnvironmentID)
			out := stdout + stderr
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			if !regexp.MustCompile(regexp.QuoteMeta(name) + `.*(?i)sealed|(?i)sealed.*` + regexp.QuoteMeta(name)).MatchString(errorLines(out)) {
				t.Errorf("no ::error:: line names %s as sealed; error lines = %q", name, errorLines(out))
			}
			if m := s.mutations(t); len(m) != 0 {
				t.Errorf("a sealed target was refused after mutations %v; the refusal must precede every write", m)
			}
			if len(s.calls(t)) == 0 {
				t.Error("no Railway call was made, so the sealed flag was never read")
			}
		})
	}
}

// prodU3b is the post-merge write set; a secret's value is its input.
func prodU3b(jwk string) map[string]string {
	return map[string]string{
		"auth.PORT":                   "8080",
		"auth.DATABASE_URL":           authDSNReference,
		"auth.API_EXTERNAL_URL":       authInternalURL,
		"auth.GOTRUE_SITE_URL":        authProdSiteURL,
		"auth.GOTRUE_JWT_ISSUER":      authProdIssuer,
		"auth.GOTRUE_JWT_KEYS":        jwk,
		"auth.GOTRUE_JWT_SECRET":      authProdJWTSecret,
		"gateway.AUTH_ADMIN_PASSWORD": authProdPassword,
		"gateway.AUTH_ISSUER":         authProdIssuer,
		"gateway.AUTH_JWKS_URL":       authJWKSURL,
	}
}

func prodLabel(svc string) string {
	switch svc {
	case productionGatewayID:
		return "gateway"
	case authProdAuthID:
		return "auth"
	}
	return svc
}

func TestSetProductionAuth_PostMergeWritesAndReReadsTheSet(t *testing.T) {
	jwk := freshJWK(t)
	exports := prodAuthExports(authProdPassword, jwk, authProdJWTSecret, "")

	s := newProdAuthShim(t, prodSealed())
	stdout, stderr, code := s.run(t, exports, "set-production-auth", "--post-merge", persistentEnvironmentID)
	out := stdout + stderr
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, out)
	}
	got := map[string]string{}
	for _, u := range s.upserts(t) {
		k := prodLabel(u.Service) + "." + u.Name
		if _, dup := got[k]; dup {
			t.Errorf("%s upserted more than once", k)
		}
		got[k] = u.Value
	}
	want := prodU3b(jwk)
	for k, v := range want {
		if g, ok := got[k]; !ok {
			t.Errorf("%s was not written", k)
		} else if g != v {
			t.Errorf("%s = %q, want %q", k, g, v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s was written; it is not in the post-merge set", k)
		}
	}
	if _, ok := got["gateway.AUTH_ADDITIONAL_ISSUERS"]; ok {
		t.Error("gateway.AUTH_ADDITIONAL_ISSUERS was written; production keeps it unset")
	}

	// Each name is re-read: bending its read-back alone fails the run, naming it.
	for k := range want {
		label, name, _ := strings.Cut(k, ".")
		svc := productionGatewayID
		if label == "auth" {
			svc = authProdAuthID
		}
		filter := fmt.Sprintf(`.%s = "bent-by-the-test"`, name)
		if name == "AUTH_ADMIN_PASSWORD" || name == "GOTRUE_JWT_SECRET" {
			filter = fmt.Sprintf(`del(.%s)`, name)
		}
		t.Run("re-reads "+k, func(t *testing.T) {
			t.Parallel()
			s := newProdAuthShim(t, prodSealed())
			s.bendRead(t, svc, filter)
			stdout, stderr, code := s.run(t, exports, "set-production-auth", "--post-merge", persistentEnvironmentID)
			if code != 1 || !strings.Contains(errorLines(stdout+stderr), name) {
				t.Errorf("with %s bent on re-read: exit %d and error lines %q, want exit 1 naming it", k, code, errorLines(stdout+stderr))
			}
		})
	}
}

func TestSetProductionAuth_ResendOptional(t *testing.T) {
	jwk := freshJWK(t)
	t.Run("without RESEND_API_KEY", func(t *testing.T) {
		s := newProdAuthShim(t, prodSealed())
		stdout, stderr, code := s.run(t, prodAuthExports(authProdPassword, jwk, authProdJWTSecret, ""), "set-production-auth", "--post-merge", persistentEnvironmentID)
		if code != 0 {
			t.Fatalf("exit %d, want 0; output = %q", code, stdout+stderr)
		}
		ups := s.upserts(t)
		if len(ups) == 0 {
			t.Fatal("control: nothing was written")
		}
		if got := upsertsOf(ups, authProdAuthID, "GOTRUE_SMTP_PASS"); len(got) != 0 {
			t.Errorf("GOTRUE_SMTP_PASS was upserted %d time(s) with no Resend key", len(got))
		}
	})
	t.Run("with RESEND_API_KEY", func(t *testing.T) {
		s := newProdAuthShim(t, prodSealed())
		stdout, stderr, code := s.run(t, prodAuthExports(authProdPassword, jwk, authProdJWTSecret, authProdResendKey), "set-production-auth", "--post-merge", persistentEnvironmentID)
		out := stdout + stderr
		if code != 0 {
			t.Fatalf("exit %d, want 0; output = %q", code, out)
		}
		if got := oneUpsert(t, s.upserts(t), authProdAuthID, "GOTRUE_SMTP_PASS"); got != authProdResendKey {
			t.Error("auth.GOTRUE_SMTP_PASS is not the Resend key")
		}
		requireRedacted(t, out, "auth.GOTRUE_SMTP_PASS")
		if strings.Contains(out, authProdResendKey) {
			t.Error("the output carries the Resend key")
		}
	})
}

func TestSetForkAuthSelfTest(t *testing.T) {
	shim := newCurlShim(t)
	stdout, stderr, code := runBashScript(t, shim.prelude+"bash '"+railwayEnvScript(t)+"' set-fork-auth --self-test\n")
	out := stdout + stderr
	if code != 0 {
		t.Fatalf("--self-test exit %d, want 0; output = %q", code, out)
	}
	if strings.Contains(out, "FAILED") || strings.Contains(out, "::error::") {
		t.Errorf("--self-test passed but printed a failure; output = %q", out)
	}
	if calls := shim.calls(t); calls != "" {
		t.Errorf("--self-test called the network: %q", calls)
	}
	shim.requireOnPath(t)

	t.Run("a changed issuer helper fails it", func(t *testing.T) {
		raw, err := os.ReadFile(railwayEnvScript(t))
		if err != nil {
			t.Fatal(err)
		}
		// The helper builds the issuer from the literal prefix and an expansion; the expected literals do not.
		builder := regexp.MustCompile(`urn:ascomply:auth:([$%])`)
		if n := len(builder.FindAllIndex(raw, -1)); n == 0 {
			t.Fatal("no code builds urn:ascomply:auth:<expansion>, so there is no issuer helper to change")
		}
		mutated := builder.ReplaceAll(raw, []byte("urn:ascomply:auth-changed:$1"))
		path := filepath.Join(t.TempDir(), "railway-env.sh")
		if err := os.WriteFile(path, mutated, 0o755); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code := runBashScript(t, shim.prelude+"bash '"+path+"' set-fork-auth --self-test\n")
		out := stdout + stderr
		if code != 1 {
			t.Errorf("--self-test with a changed issuer exits %d, want 1; output = %q", code, out)
		}
		if !regexp.MustCompile(`(?i)issuer`).MatchString(errorLines(out)) {
			t.Errorf("no ::error:: line names the issuer; error lines = %q", errorLines(out))
		}
	})
}

func TestRailwayInvariantsRunsForkAuthSelfTest(t *testing.T) {
	inv := readWorkflow(t, "railway-invariants.yml")
	var control, jobs []string
	for _, job := range workflowJobsOf(inv) {
		for _, s := range job.steps() {
			if slices.Contains(invocations(s.keys["run"], "set-fork-environment"), forkSelfTestRunCmd) {
				control = append(control, job.name)
			}
			for _, cmd := range invocations(s.keys["run"], "set-fork-auth") {
				if cmd != forkAuthSelfTestRunCmd {
					t.Errorf("job %s runs %q; the invariants workflow runs only the self-test", job.name, cmd)
					continue
				}
				jobs = append(jobs, job.name)
			}
		}
	}
	if !slices.Equal(control, []string{"fork-environment-self-test"}) {
		t.Fatalf("control: the parser finds the set-fork-environment self-test in jobs %v; the scan is broken", control)
	}
	if !slices.Equal(jobs, []string{"fork-auth-self-test"}) {
		t.Errorf("%q runs in jobs %v, want exactly [fork-auth-self-test]", forkAuthSelfTestRunCmd, jobs)
	}
}
