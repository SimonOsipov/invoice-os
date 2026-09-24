package main

import (
	"regexp"
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
