package approval

import (
	"net/http"
	"strings"
	"testing"
)

// The seven approval handlers that decode a body, each with its cap constant. The
// fail-closed seams fail the test if the store is reached.
func bodyCapRoutes() []struct {
	name  string
	cap   int
	serve func(t *testing.T, body string) (code int, msg string)
} {
	role := func(method, path string) func(*testing.T, string) (int, string) {
		return func(t *testing.T, body string) (int, string) {
			id := caller()
			rec := serveRole(t, failClosedSeam(t), nil, method, path, body, &id)
			return rec.Code, errorMessage(t, rec.Body.Bytes())
		}
	}
	policy := func(method, path string) func(*testing.T, string) (int, string) {
		return func(t *testing.T, body string) (int, string) {
			id := caller()
			rec := servePolicy(t, failClosedPolicySeam(t), nil, method, path, body, &id)
			return rec.Code, errorMessage(t, rec.Body.Bytes())
		}
	}
	decide := func(t *testing.T, body string) (int, string) {
		id := caller()
		rec := serveDecision(t, failClosedDecide(t), nil, "/v1/invoices/"+decisionHandlerTestID+"/approvals", body, &id)
		return rec.Code, errorMessage(t, rec.Body.Bytes())
	}
	return []struct {
		name  string
		cap   int
		serve func(t *testing.T, body string) (code int, msg string)
	}{
		{"create role", maxCreateBodyBytes, role("POST", "/v1/workflow-roles")},
		{"update role", maxCreateBodyBytes, role("PATCH", "/v1/workflow-roles/tax-reviewer")},
		{"set members", maxStaffBodyBytes, role("PUT", "/v1/workflow-roles/tax-reviewer/members")},
		{"decide", maxDecisionBodyBytes, decide},
		{"create policy", maxPolicyBodyBytes, policy("POST", "/v1/approval-policies")},
		{"put draft", maxPolicyBodyBytes, policy("PUT", "/v1/approval-policies/"+policyHandlerTestID+"/draft")},
		{"reorder", maxPolicyBodyBytes, policy("PUT", "/v1/approval-policies/order")},
	}
}

// A valid value followed by padding past the cap must still be refused: the decoder
// stops after one value, so only the drain catches the trailing bytes.
func TestApprovalHandlers_ValuePlusOverCapPaddingIs413(t *testing.T) {
	for _, c := range bodyCapRoutes() {
		t.Run(c.name, func(t *testing.T) {
			body := `{}` + strings.Repeat(" ", c.cap)
			code, msg := c.serve(t, body)
			if code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413", code)
			}
			if msg != "request body exceeds the size limit" {
				t.Errorf("error = %q, want the 413 message", msg)
			}
		})
	}
}

func TestApprovalHandlers_MalformedUnderTheCapIs400(t *testing.T) {
	for _, c := range bodyCapRoutes() {
		t.Run(c.name, func(t *testing.T) {
			code, msg := c.serve(t, `{"x":`)
			if code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", code)
			}
			if msg != "invalid request body" {
				t.Errorf("error = %q, want %q", msg, "invalid request body")
			}
		})
	}
}
