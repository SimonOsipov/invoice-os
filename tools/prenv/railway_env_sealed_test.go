// railway_env_sealed_test.go pins railway-env.sh audit-sealed-variables: exactly three
// names, sealed on `auth`, are allowed; every other sealed variable fails the audit.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	sealedAuthID    = "svc-auth-source"
	sealedGatewayID = "svc-gw-source"
	sealedAdminID   = "svc-auth-admin-source"
	envScoped       = "environment-scoped"
)

var (
	sealedAllowlist = []string{"GOTRUE_JWT_KEYS", "GOTRUE_JWT_SECRET", "GOTRUE_SMTP_PASS"}
	offenderLine    = regexp.MustCompile(`^  (\S+) \(serviceId=([^)]*)\)$`)
	unresolvedRE    = regexp.MustCompile(`(?i)cannot resolve the allowlist`)
)

func instance(id, name string) string {
	return `{"node":{"serviceId":"` + id + `","serviceName":"` + name + `"}}`
}

func sourceInstances() []string {
	return []string{instance(sealedGatewayID, "gateway"), instance(sealedAuthID, "auth"), instance(sealedAdminID, "auth-admin")}
}

// sealedOn builds a sealed node with no value; svc "" is environment-scoped (null).
func sealedOn(name, svc string) string {
	if svc == "" {
		return `{"node":{"name":"` + name + `","isSealed":true,"serviceId":null}}`
	}
	return `{"node":{"name":"` + name + `","isSealed":true,"serviceId":"` + svc + `"}}`
}

func plainOn(name, svc string) string {
	return `{"node":{"name":"` + name + `","isSealed":false,"serviceId":"` + svc + `"}}`
}

// newSealedShim answers the audit read and a separate settle read with the same instances,
// so the tests hold whichever query resolves `auth`.
func newSealedShim(t *testing.T, instances []string, nodes ...string) authShim {
	t.Helper()
	edges := `{"edges":[` + strings.Join(instances, ",") + `]}`
	resp := `{"data":{"environment":{"id":"` + persistentEnvironmentID + `","name":"production","serviceInstances":` + edges +
		`,"variables":{"edges":[` + strings.Join(nodes, ",") + `]}}}}`
	return newAuthShim(t, map[string]string{
		"sealed": resp,
		"settle": `{"data":{"environment":{"serviceInstances":` + edges + `}}}`,
	}, nil)
}

func runAudit(t *testing.T, s authShim) (stdout, out string, code int) {
	t.Helper()
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "audit-sealed-variables")
	return stdout, stdout + stderr, code
}

// sealedOffenders returns name@serviceId for each line listed under the Offenders: error.
func sealedOffenders(out string) []string {
	var got []string
	in := false
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "::error::") && strings.Contains(l, "Offenders:") {
			in = true
			continue
		}
		m := offenderLine.FindStringSubmatch(l)
		if !in || m == nil {
			in = in && m != nil
			continue
		}
		got = append(got, m[1]+"@"+m[2])
	}
	slices.Sort(got)
	return got
}

// requireRefused fails unless the audit exits 1 with the do-not-fork clause and exactly want as offenders.
func requireRefused(t *testing.T, out string, code int, want ...string) {
	t.Helper()
	if len(want) == 0 {
		t.Fatal("requireRefused needs at least one expected offender")
	}
	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, out)
	}
	if e := errorLines(out); !strings.Contains(e, "Sealed variables do NOT fork") || !strings.Contains(e, "Offenders:") {
		t.Errorf("error lines %q lack the clause \"Sealed variables do NOT fork ... Offenders:\"", e)
	}
	slices.Sort(want)
	if got := sealedOffenders(out); !slices.Equal(got, want) {
		t.Errorf("offenders = %v, want exactly %v; output = %q", got, want, out)
	}
}

// requireAllowed fails unless the audit exits 0, raises no error, and stdout names each allowed variable.
func requireAllowed(t *testing.T, stdout, out string, code int, allowed ...string) {
	t.Helper()
	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, out)
	}
	if e := errorLines(out); e != "" {
		t.Errorf("error lines %q, want none", e)
	}
	for _, n := range allowed {
		if !strings.Contains(stdout, n) {
			t.Errorf("stdout does not name the allowed sealed variable %s; stdout = %q", n, stdout)
		}
	}
}

func TestAuditSealed_NoneSealedPasses(t *testing.T) {
	var nodes []string
	for i := 0; len(nodes) < 30; i++ {
		svc := []string{sealedAuthID, sealedGatewayID, sealedAdminID}[i%3]
		nodes = append(nodes, plainOn(fmt.Sprintf("VAR_%02d", i), svc))
	}
	// Allowlisted names unsealed, on auth and elsewhere, never count as sealed.
	nodes[0], nodes[1] = plainOn("GOTRUE_JWT_KEYS", sealedAuthID), plainOn("GOTRUE_JWT_SECRET", sealedGatewayID)
	s := newSealedShim(t, sourceInstances(), nodes...)
	stdout, out, code := runAudit(t, s)
	if code != 0 || errorLines(out) != "" {
		t.Errorf("exit %d, error lines %q; want exit 0 and none", code, errorLines(out))
	}
	if !strings.Contains(stdout, "clean") {
		t.Errorf("stdout %q has no clean line", stdout)
	}
	if len(s.calls(t)) == 0 {
		t.Error("the audit made no Railway call; the pass proves nothing")
	}
}

func TestAuditSealed_AllowlistOnAuthPasses(t *testing.T) {
	for _, set := range [][]string{sealedAllowlist[:1], sealedAllowlist[:2], sealedAllowlist} {
		t.Run(strings.Join(set, "+"), func(t *testing.T) {
			nodes := []string{plainOn("PORT", sealedAuthID), plainOn("ENVIRONMENT", sealedGatewayID)}
			for _, n := range set {
				nodes = append(nodes, sealedOn(n, sealedAuthID))
			}
			stdout, out, code := runAudit(t, newSealedShim(t, sourceInstances(), nodes...))
			requireAllowed(t, stdout, out, code, set...)
			// It lists the names it found sealed, not the allowlist itself.
			for _, n := range sealedAllowlist[len(set):] {
				if strings.Contains(stdout, n) {
					t.Errorf("stdout names %s, which is not sealed; stdout = %q", n, stdout)
				}
			}
		})
	}
}

func TestAuditSealed_FourthNameOnAuthFails(t *testing.T) {
	stdout, out, code := runAudit(t, newSealedShim(t, sourceInstances(),
		sealedOn("GOTRUE_JWT_KEYS", sealedAuthID), sealedOn("GOTRUE_SITE_URL", sealedAuthID)))
	requireRefused(t, out, code, "GOTRUE_SITE_URL@"+sealedAuthID)
	if strings.Contains(stdout, "clean") {
		t.Errorf("a refused audit printed a clean line: %q", stdout)
	}
}

func TestAuditSealed_AllowlistedNameOnOtherServiceFails(t *testing.T) {
	cases := []struct {
		name  string
		nodes []string
		want  []string
	}{
		{"on gateway", []string{sealedOn("GOTRUE_JWT_KEYS", sealedGatewayID)}, []string{"GOTRUE_JWT_KEYS@" + sealedGatewayID}},
		// A service whose name starts with "auth" is not auth.
		{"on auth-admin", []string{sealedOn("GOTRUE_SMTP_PASS", sealedAdminID)}, []string{"GOTRUE_SMTP_PASS@" + sealedAdminID}},
		{"mixed: the same name on auth is allowed, on gateway refused",
			[]string{sealedOn("GOTRUE_JWT_KEYS", sealedAuthID), sealedOn("GOTRUE_JWT_KEYS", sealedGatewayID)},
			[]string{"GOTRUE_JWT_KEYS@" + sealedGatewayID}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, out, code := runAudit(t, newSealedShim(t, sourceInstances(), c.nodes...))
			requireRefused(t, out, code, c.want...)
		})
	}
}

// Boundary: a null serviceId is not auth's.
func TestAuditSealed_AllowlistedNameEnvironmentScopedFails(t *testing.T) {
	t.Run("alone", func(t *testing.T) {
		_, out, code := runAudit(t, newSealedShim(t, sourceInstances(), sealedOn("GOTRUE_JWT_SECRET", "")))
		requireRefused(t, out, code, "GOTRUE_JWT_SECRET@"+envScoped)
	})
	t.Run("mixed with the same name on auth", func(t *testing.T) {
		_, out, code := runAudit(t, newSealedShim(t, sourceInstances(),
			sealedOn("GOTRUE_JWT_SECRET", sealedAuthID), sealedOn("GOTRUE_JWT_SECRET", "")))
		requireRefused(t, out, code, "GOTRUE_JWT_SECRET@"+envScoped)
	})
}

// A near-miss sits beside a real allowlisted name, so a loose match and a
// blanket refusal both fail here.
func TestAuditSealed_NameMatchIsExact(t *testing.T) {
	for _, variant := range []string{
		"gotrue_jwt_keys", "Gotrue_Jwt_Secret", "GOTRUE_JWT_KEYS_OLD", "X_GOTRUE_JWT_KEYS",
		"GOTRUE_JWT_KEY", "GOTRUE_SMTP_PAS", "GOTRUE.JWT.KEYS", "GOTRUE_JWT_KEYSGOTRUE_JWT_SECRET",
	} {
		t.Run(variant, func(t *testing.T) {
			_, out, code := runAudit(t, newSealedShim(t, sourceInstances(),
				sealedOn(variant, sealedAuthID), sealedOn("GOTRUE_JWT_SECRET", sealedAuthID)))
			requireRefused(t, out, code, variant+"@"+sealedAuthID)
		})
	}
}

// The allowlist does not mask any other sealed variable in the same response.
func TestAuditSealed_MixedSetFailsOnTheOffendersAlone(t *testing.T) {
	nodes := []string{plainOn("PORT", sealedAuthID)}
	for _, n := range sealedAllowlist {
		nodes = append(nodes, sealedOn(n, sealedAuthID))
	}
	nodes = append(nodes, sealedOn("GEMINI_API_KEY", sealedGatewayID), sealedOn("SHARED_TOKEN", ""), sealedOn("GOTRUE_SITE_URL", sealedAuthID))
	_, out, code := runAudit(t, newSealedShim(t, sourceInstances(), nodes...))
	requireRefused(t, out, code, "GEMINI_API_KEY@"+sealedGatewayID, "SHARED_TOKEN@"+envScoped, "GOTRUE_SITE_URL@"+sealedAuthID)
}

// Sealed variables outside the allowlist fail exactly as before the allowlist existed.
func TestAuditSealed_OtherServicesRefusalUnchanged(t *testing.T) {
	_, out, code := runAudit(t, newSealedShim(t, sourceInstances(),
		plainOn("PORT", sealedAuthID), sealedOn("GEMINI_API_KEY", sealedGatewayID), sealedOn("JEV_API_KEY", "")))
	requireRefused(t, out, code, "GEMINI_API_KEY@"+sealedGatewayID, "JEV_API_KEY@"+envScoped)
	if !strings.Contains(errorLines(out), "2 sealed variable(s) found in the source environment "+persistentEnvironmentID) {
		t.Errorf("error lines %q lack the count clause \"2 sealed variable(s) found in the source environment\"", errorLines(out))
	}
}

func TestAuditSealed_MissingAuthServiceFailsClosed(t *testing.T) {
	noAuth := [][]string{
		{instance(sealedGatewayID, "gateway"), instance(sealedAdminID, "auth-admin")},
		{instance(sealedGatewayID, "gateway"), instance(sealedAuthID, "Auth")},
		{},
	}
	for i, instances := range noAuth {
		t.Run([]string{"auth absent", "only a case variant of auth", "no instances"}[i], func(t *testing.T) {
			stdout, out, code := runAudit(t, newSealedShim(t, instances, sealedOn("GOTRUE_JWT_KEYS", sealedAuthID)))
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			if !unresolvedRE.MatchString(errorLines(out)) {
				t.Errorf("error lines %q lack the clause %q", errorLines(out), unresolvedRE)
			}
			if strings.Contains(stdout, "clean") {
				t.Errorf("an unresolved allowlist printed a clean line: %q", stdout)
			}
		})
	}
	t.Run("no sealed variables still pass", func(t *testing.T) {
		stdout, out, code := runAudit(t, newSealedShim(t, noAuth[0], plainOn("GOTRUE_JWT_KEYS", sealedGatewayID)))
		requireAllowed(t, stdout, out, code)
	})
}

func TestAuditSealed_NullEnvironmentFails(t *testing.T) {
	s := newAuthShim(t, map[string]string{
		"sealed": `{"data":{"environment":null}}`,
		"settle": `{"data":{"environment":null}}`,
	}, nil)
	_, out, code := runAudit(t, s)
	if code != 1 || !strings.Contains(errorLines(out), "This is NOT evidence that no sealed variables exist") {
		t.Errorf("exit %d, error lines %q; want exit 1 with \"This is NOT evidence that no sealed variables exist\"", code, errorLines(out))
	}
}

// sealedAuditQuery returns SEALED_AUDIT_QUERY's single-quoted body from the script.
func sealedAuditQuery(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(railwayEnvScript(t))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)\nSEALED_AUDIT_QUERY='([^']*)'`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("SEALED_AUDIT_QUERY='...' not found in railway-env.sh")
	}
	return m[1]
}

func TestAuditSealed_QuerySelectsNoValue(t *testing.T) {
	q := sealedAuditQuery(t)
	for _, f := range []string{"name", "isSealed", "serviceId"} {
		if !regexp.MustCompile(`\b` + f + `\b`).MatchString(q) {
			t.Errorf("SEALED_AUDIT_QUERY selects no %s; the extraction is wrong or the audit is blind: %q", f, q)
		}
	}
	if regexp.MustCompile(`(?i)\bvalues?\b`).MatchString(q) {
		t.Errorf("SEALED_AUDIT_QUERY selects a value field; a sealed value is never read: %q", q)
	}
}

var (
	sealedTrueRE = regexp.MustCompile(`"isSealed"\s*:\s*true`)
	nodeOpenRE   = regexp.MustCompile(`\{"node":\{`)
)

// Every shim fixture that seals a variable gives it no value key.
func TestSealedFixturesCarryNoValue(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	hits := map[string]int{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		for _, loc := range sealedTrueRE.FindAllStringIndex(src, -1) {
			opens := nodeOpenRE.FindAllStringIndex(src[:loc[0]], -1)
			end := strings.Index(src[loc[1]:], "}}")
			if len(opens) == 0 || end < 0 || loc[0]-opens[len(opens)-1][0] > 200 {
				t.Errorf("%s: a sealed fixture at byte %d is not inside a {\"node\":{...}} literal the scan can bound", f, loc[0])
				continue
			}
			node := src[opens[len(opens)-1][0] : loc[1]+end+2]
			if strings.Contains(node, `"value"`) {
				t.Errorf("%s: sealed fixture carries a value: %s", f, node)
			}
			hits[f]++
		}
	}
	for _, f := range []string{"railway_env_sealed_test.go", "railway_env_auth_test.go"} {
		if hits[f] == 0 {
			t.Errorf("%s: no sealed fixture found; the scan is blind", f)
		}
	}
}

// docs/identity-provider.md claims the audit allows exactly three names on auth; the audit must agree.
func TestAuditSealed_DocAllowlistMatchesTheAudit(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "identity-provider.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	const head = "**The audit allows exactly these three on `auth`.**"
	i := strings.Index(doc, head)
	if i < 0 {
		t.Fatalf("docs/identity-provider.md lacks %q", head)
	}
	para := doc[i:]
	if j := strings.Index(para, "\n\n"); j >= 0 {
		para = para[:j]
	}
	var named []string
	for _, m := range regexp.MustCompile("`(GOTRUE_[A-Z_]+)`").FindAllStringSubmatch(para, -1) {
		named = append(named, m[1])
	}
	slices.Sort(named)
	if !slices.Equal(named, sealedAllowlist) {
		t.Fatalf("the doc's audit paragraph names %v, want %v", named, sealedAllowlist)
	}
	var nodes []string
	for _, n := range named {
		nodes = append(nodes, sealedOn(n, sealedAuthID))
	}
	stdout, out, code := runAudit(t, newSealedShim(t, sourceInstances(), nodes...))
	requireAllowed(t, stdout, out, code, named...)
}
