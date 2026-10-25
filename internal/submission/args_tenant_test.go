package submission_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// errorReporter tags Sentry events from the args' tenant_id key (queue.TenantScoped).
func TestSubmitAndPollArgs_TenantMatchesTheEncodedTenantID(t *testing.T) {
	u := uuid.NewString()
	for name, args := range map[string]queue.TenantScoped{
		"SubmitArgs": submission.SubmitArgs{TenantID: u},
		"PollArgs":   submission.PollArgs{TenantID: u},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(args)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := m["tenant_id"]; got != args.Tenant() || got != u {
				t.Errorf("encoded tenant_id = %v, Tenant() = %q, want %q", got, args.Tenant(), u)
			}
		})
	}
}
