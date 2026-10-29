package audit_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/gateway"
)

// The reader's list, the gateway constants, and firmWideEvents name the same six events.
func TestAccountEvents_ListMatchesTheGateway(t *testing.T) {
	gw := []string{
		gateway.EventSignedIn, gateway.EventSignInFailed, gateway.EventSignInBlocked,
		gateway.EventSignedOut, gateway.EventPasswordResetRequested, gateway.EventPasswordResetCompleted,
	}
	var firm []string
	for _, e := range audit.FirmWideEventsForTest() {
		if strings.HasPrefix(e, "account.") {
			firm = append(firm, e)
		}
	}

	if len(audit.AccountEvents) == 0 {
		t.Fatal("audit.AccountEvents is empty, want the six names")
	}
	want := slices.Sorted(slices.Values(gw))
	if len(want) != 6 {
		t.Fatalf("the gateway names %d account events, want 6", len(want))
	}
	for name, got := range map[string][]string{
		"audit.AccountEvents":      audit.AccountEvents,
		"firmWideEvents (account)": firm,
	} {
		if g := slices.Sorted(slices.Values(got)); !slices.Equal(g, want) {
			t.Errorf("%s = %v, want the gateway's %v", name, g, want)
		}
	}
}
