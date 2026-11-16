package invoice

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// seedMemberWith inserts a membership of role with an optional display name and email.
func seedMemberWith(t *testing.T, super *pgxpool.Pool, tenantID, userID, role string, displayName, email *string) {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO memberships (tenant_id, user_id, role, display_name, email, all_clients)
		 VALUES ($1, $2, $3, $4, $5, true) RETURNING id`, tenantID, userID, role, displayName, email,
	).Scan(&id); err != nil {
		t.Fatalf("seed membership(%s): %v", role, err)
	}
	t.Cleanup(func() { _, _ = super.Exec(context.Background(), `DELETE FROM memberships WHERE id = $1`, id) })
}

func historyAs(t *testing.T, fx namedInvoice, caller string) []StatusChange {
	t.Helper()
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: caller, Role: "authenticated", TenantID: fx.tenantID})
	got, err := fx.store.History(c, fx.invID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	return got
}

// TestHistory_ActorNameEmailLeak_NonAdminReadsUnnamedMemberNotAnotherMembersEmail: the
// history names a nameless actor by email for an admin (control) and "Unnamed member" for a
// non-admin, who still reads their own row's email.
func TestHistory_ActorNameEmailLeak_NonAdminReadsUnnamedMemberNotAnotherMembersEmail(t *testing.T) {
	super, app := dbTestPools(t)
	fx := seedNamedInvoice(t, super, app, "SECAUD-42-HIST", "Chinedu Okafor")

	other, self := uuid.NewString(), uuid.NewString()
	otherEmail, selfEmail := "halima@example.com", "me@example.com"
	seedMemberWith(t, super, fx.tenantID, other, "reviewer", nil, &otherEmail)
	seedMemberWith(t, super, fx.tenantID, self, "preparer", nil, &selfEmail)
	at := time.Now().UTC()
	seedHistoryRow(t, super, fx.tenantID, fx.invID, statusPtr(StatusDraft), StatusValidated, other, at)
	seedHistoryRow(t, super, fx.tenantID, fx.invID, statusPtr(StatusValidated), StatusDraft, self, at.Add(time.Second))

	if got := rowFor(t, historyAs(t, fx, fx.subject), other).ActorName; got != otherEmail {
		t.Errorf("admin reads %q, want the email %q (admins keep it)", got, otherEmail)
	}
	got := historyAs(t, fx, self)
	if name := rowFor(t, got, other).ActorName; name != "Unnamed member" || strings.Contains(name, "@") {
		t.Errorf("non-admin reads %q for another member, want %q", name, "Unnamed member")
	}
	if name := rowFor(t, got, self).ActorName; name != selfEmail {
		t.Errorf("non-admin reads %q for their own row, want %q", name, selfEmail)
	}
}
