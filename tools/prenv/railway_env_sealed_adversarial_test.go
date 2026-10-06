package main

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The count in the refusal is the offenders', not every sealed variable's.
func TestAuditSealed_OffenderCountExcludesTheAllowlisted(t *testing.T) {
	nodes := []string{plainOn("PORT", sealedAuthID)}
	for _, n := range sealedAllowlist {
		nodes = append(nodes, sealedOn(n, sealedAuthID))
	}
	nodes = append(nodes, sealedOn("GEMINI_API_KEY", sealedGatewayID))
	_, out, code := runAudit(t, newSealedShim(t, sourceInstances(), nodes...))
	requireRefused(t, out, code, "GEMINI_API_KEY@"+sealedGatewayID)
	const want = "::error::1 sealed variable(s) found in the source environment " + persistentEnvironmentID
	if !strings.Contains(errorLines(out), want) {
		t.Errorf("error lines %q lack %q", errorLines(out), want)
	}
}

// Two instances named auth resolve no id; the audit refuses rather than picks one.
func TestAuditSealed_DuplicateAuthServiceFailsClosed(t *testing.T) {
	instances := append(sourceInstances(), instance("svc-auth-twin", "auth"))
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
}

// The clean line counts the sealed variables it allowed out of the total.
func TestAuditSealed_CleanLineCountsTheAllowed(t *testing.T) {
	nodes := []string{plainOn("PORT", sealedAuthID), plainOn("ENVIRONMENT", sealedGatewayID),
		sealedOn("GOTRUE_JWT_KEYS", sealedAuthID), sealedOn("GOTRUE_SMTP_PASS", sealedAuthID)}
	stdout, out, code := runAudit(t, newSealedShim(t, sourceInstances(), nodes...))
	requireAllowed(t, stdout, out, code, "GOTRUE_JWT_KEYS", "GOTRUE_SMTP_PASS")
	if !strings.Contains(stdout, "2 of 4 variables") {
		t.Errorf("stdout %q lacks \"2 of 4 variables\"", stdout)
	}
}

// The shim answers any query shape, so only the query text shows auth can be resolved.
func TestAuditSealed_QuerySelectsTheServiceInstances(t *testing.T) {
	q := sealedAuditQuery(t)
	re := regexp.MustCompile(`serviceInstances\s*\{\s*edges\s*\{\s*node\s*\{\s*serviceId\s+serviceName\s*\}`)
	if !re.MatchString(q) {
		t.Errorf("SEALED_AUDIT_QUERY does not select serviceInstances { edges { node { serviceId serviceName } } }: %q", q)
	}
}

// With any variable sealed, every service the allowlist names must resolve to exactly one
// instance, whichever sealed variable it is.
func TestAuditSealed_MissingTenancyServiceFailsClosed(t *testing.T) {
	base := []string{instance(sealedGatewayID, "gateway"), instance(sealedAuthID, "auth"), instance(sealedAdminID, "auth-admin")}
	withTenancy := func(extra ...string) []string { return append(slices.Clone(base), extra...) }
	cases := []struct {
		name      string
		instances []string
		sealed    string
	}{
		{"tenancy absent", base, sealedOn("RESEND_SENDING_KEY", sealedTenancyID)},
		{"tenancy twice", withTenancy(instance(sealedTenancyID, "tenancy"), instance("svc-tenancy-twin", "tenancy")), sealedOn("RESEND_SENDING_KEY", sealedTenancyID)},
		{"only a case variant", withTenancy(instance(sealedTenancyID, "Tenancy")), sealedOn("RESEND_SENDING_KEY", sealedTenancyID)},
		{"only tenancy-admin", withTenancy(instance(sealedTenancyID, "tenancy-admin")), sealedOn("RESEND_SENDING_KEY", sealedTenancyID)},
		{"tenancy absent, only an auth name sealed", base, sealedOn("GOTRUE_JWT_KEYS", sealedAuthID)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, out, code := runAudit(t, newSealedShim(t, c.instances, c.sealed))
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			if e := errorLines(out); !unresolvedRE.MatchString(e) || !strings.Contains(e, "without the `tenancy` service") {
				t.Errorf("error lines %q lack the clause \"cannot resolve the allowlist without the `tenancy` service\"", e)
			}
			if strings.Contains(stdout, "clean") {
				t.Errorf("an unresolved allowlist printed a clean line: %q", stdout)
			}
		})
	}
	t.Run("auth is named first when both are missing", func(t *testing.T) {
		stdout, out, code := runAudit(t, newSealedShim(t, []string{instance(sealedGatewayID, "gateway")}, sealedOn("RESEND_SENDING_KEY", sealedTenancyID)))
		e := errorLines(out)
		if code != 1 || !strings.Contains(e, "without the `auth` service") || strings.Contains(e, "`tenancy`") {
			t.Errorf("exit %d, error lines %q; want exit 1 naming only the `auth` service", code, e)
		}
		if strings.Contains(stdout, "clean") {
			t.Errorf("an unresolved allowlist printed a clean line: %q", stdout)
		}
	})
	t.Run("no sealed variables still pass", func(t *testing.T) {
		stdout, out, code := runAudit(t, newSealedShim(t, base, plainOn("RESEND_SENDING_KEY", sealedGatewayID)))
		requireAllowed(t, stdout, out, code)
	})
}
