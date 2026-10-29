package accountevents

import (
	"context"
	"regexp"
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
)

// AC 8: the gateway constants, the package list and the outbox CHECK name the same six events.
func TestEvents_TheThreeListsAgree(t *testing.T) {
	e := newEnv(t)

	gw := []string{
		gateway.EventSignedIn, gateway.EventSignInFailed, gateway.EventSignInBlocked,
		gateway.EventSignedOut, gateway.EventPasswordResetRequested, gateway.EventPasswordResetCompleted,
	}
	var def string
	err := e.admin.QueryRow(context.Background(), `SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'account_event_handoffs'::regclass AND contype = 'c' AND pg_get_constraintdef(oid) LIKE '%account.%'`).Scan(&def)
	if err != nil {
		t.Fatalf("read the outbox event CHECK: %v", err)
	}
	var check []string
	for _, m := range regexp.MustCompile(`'(account\.[a-z_]+)'`).FindAllStringSubmatch(def, -1) {
		check = append(check, m[1])
	}

	if len(Events) == 0 {
		t.Fatal("accountevents.Events is empty, want the six names")
	}
	if len(check) != 6 || len(gw) != 6 {
		t.Fatalf("CHECK names %d event(s), gateway %d, want 6 each; CHECK: %s", len(check), len(gw), def)
	}
	for name, got := range map[string][]string{"accountevents": Events, "gateway": gw, "outbox CHECK": check} {
		if want := sorted(slices.Clone(check)...); !slices.Equal(sorted(slices.Clone(got)...), want) {
			t.Errorf("%s names %v, want the CHECK's %v", name, sorted(slices.Clone(got)...), want)
		}
	}
}
