package tenancy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// uuid.Parse accepts these spellings of one subject; one identity must get one workspace (Q21).
func TestStoreProvisionWorkspace_SubjectSpellingsShareOneWorkspace(t *testing.T) {
	r := newRegistrant(t)
	store := NewStore(r.app)
	canonical := r.id.Subject
	if _, _, err := store.ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Canonical", DisplayName: "Ada"}); err != nil {
		t.Fatalf("canonical provision: %v", err)
	}
	for _, spelling := range []string{
		strings.ToUpper(canonical),
		"{" + canonical + "}",
		"urn:uuid:" + canonical,
		strings.ReplaceAll(canonical, "-", ""),
	} {
		t.Run(spelling, func(t *testing.T) {
			stray := uuid.NewSHA1(workspaceNamespace, []byte(spelling)).String()
			t.Cleanup(func() { _, _ = r.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, stray) })
			id := r.id
			id.Subject = spelling
			ctx := auth.WithTenantlessCaller(context.Background(), id)
			tenant, subject, err := store.ProvisionWorkspace(ctx, ProvisionInput{WorkspaceName: "Respelled", DisplayName: "Ada"})
			if !errors.Is(err, ErrAlreadyProvisioned) {
				t.Errorf("err = %v (tenant %s, user.id %q), want ErrAlreadyProvisioned: %q is %s", err, tenant.ID, subject, spelling, canonical)
			}
			var n int
			if err := r.super.QueryRow(context.Background(), `SELECT count(*) FROM memberships WHERE user_id = $1`, canonical).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Errorf("%d memberships for %s across tenants, want 1", n, canonical)
			}
		})
	}
}
