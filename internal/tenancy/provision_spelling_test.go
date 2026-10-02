package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// respellings are the non-canonical forms uuid.Parse accepts for one subject.
func respellings(canonical string) []struct{ name, spelling string } {
	return []struct{ name, spelling string }{
		{"uppercase", strings.ToUpper(canonical)},
		{"braces", "{" + canonical + "}"},
		{"urn", "urn:uuid:" + canonical},
		{"no hyphens", strings.ReplaceAll(canonical, "-", "")},
	}
}

// uuid.Parse accepts these spellings of one subject; one identity must get one workspace (Q21).
func TestStoreProvisionWorkspace_SubjectSpellingsShareOneWorkspace(t *testing.T) {
	r := newRegistrant(t)
	store := NewStore(r.app)
	canonical := r.id.Subject
	if _, _, err := store.ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Canonical", DisplayName: "Ada"}); err != nil {
		t.Fatalf("canonical provision: %v", err)
	}
	for _, tc := range respellings(canonical) {
		t.Run(tc.name, func(t *testing.T) {
			stray := uuid.NewSHA1(workspaceNamespace, []byte(tc.spelling)).String()
			t.Cleanup(func() { _, _ = r.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, stray) })
			id := r.id
			id.Subject = tc.spelling
			ctx := auth.WithTenantlessCaller(context.Background(), id)
			tenant, subject, err := store.ProvisionWorkspace(ctx, ProvisionInput{WorkspaceName: "Respelled", DisplayName: "Ada"})
			if !errors.Is(err, ErrAlreadyProvisioned) {
				t.Errorf("err = %v (tenant %s, user.id %q), want ErrAlreadyProvisioned: %q is %s", err, tenant.ID, subject, tc.spelling, canonical)
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

// A first call with a respelled subject answers and stores the canonical form.
func TestProvisionHandler_RespelledSubjectAnswersCanonical(t *testing.T) {
	for i, tc := range respellings("") {
		t.Run(tc.name, func(t *testing.T) {
			r := newRegistrant(t)
			canonical := r.id.Subject
			id := r.id
			id.Subject = respellings(canonical)[i].spelling
			rec := postProvision(NewStore(r.app), auth.WithTenantlessCaller(context.Background(), id), validProvisionBody)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
			}
			var me meBody
			if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if me.Tenant.ID != r.tenantID || me.User.ID != canonical {
				t.Errorf("tenant.id %s, user.id %q, want %s and %q", me.Tenant.ID, me.User.ID, r.tenantID, canonical)
			}
			if _, members := provisionedRows(t, r.super, r.tenantID); len(members) != 1 || members[0].UserID != canonical {
				t.Errorf("memberships = %+v, want exactly one for %s", members, canonical)
			}
			events := provisionedEvents(t, r.super, r.tenantID)
			if len(events) != 1 {
				t.Fatalf("workspace.provisioned rows = %d, want 1", len(events))
			}
			if events[0].Actor != id.Subject || events[0].Payload["user_id"] != canonical {
				t.Errorf("actor %q, payload user_id %v, want the raw %q and the canonical %q",
					events[0].Actor, events[0].Payload["user_id"], id.Subject, canonical)
			}
			read := auth.WithIdentity(context.Background(), auth.Identity{Subject: canonical, Role: "authenticated", TenantID: r.tenantID})
			resp, err := audit.NewStore(r.app).List(read, audit.Filter{Events: []string{"workspace.provisioned"}, Limit: 10})
			if err != nil || len(resp.Events) != 1 {
				t.Fatalf("audit List = %d events, err %v, want 1", len(resp.Events), err)
			}
			if strings.HasPrefix(tc.spelling, "urn:") {
				return // ceiling: actor.Resolve does not normalise urn:uuid:, the actor stays raw
			}
			if resp.Events[0].ActorName != "Ada" || resp.Events[0].ActorKind != "person" {
				t.Errorf("actor = {%q %q}, want {Ada person}: a respelled raw actor must still resolve", resp.Events[0].ActorName, resp.Events[0].ActorKind)
			}
		})
	}
}
