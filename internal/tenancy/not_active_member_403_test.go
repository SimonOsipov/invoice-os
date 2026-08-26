// AUDIT-10-03 sites 2, 3 and 4 of 14. tenancy carries three refusal-mapping
// sites: the named statusForErr, and the two inline switches inside MeHandler's
// and MembershipsHandler's returned literals. A name-shaped scan sees only the
// first, which is why TestHandlerMappingEveryRefusalSiteNamesNotActiveMember
// carries a P2-only floor.
//
// GET /v1/me is the D-5 exemption: Store.Me calls the ungated seam, so its arm
// is unreachable through the real store. TestMe_SuspendedMemberStillGets200 is
// the oracle that keeps it so. The arm is still required — the injected loader
// below reaches it, and an exemption expressed as an allowlist entry in the scan
// would rot where a dead arm cannot.
package tenancy

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func TestTenancyStatusForErr_NotActiveMemberIs403(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"bare", db.ErrNotActiveMember},
		{"wrapped", fmt.Errorf("tenancy: set membership status: %w", db.ErrNotActiveMember)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, msg := statusForErr(tc.err)
			if status != http.StatusForbidden {
				t.Errorf("status = %d, want 403", status)
			}
			if msg != db.NotActiveMemberMessage {
				t.Errorf("msg = %q, want db.NotActiveMemberMessage %q", msg, db.NotActiveMemberMessage)
			}
		})
	}
}

func TestMeHandler_NotActiveMemberIs403(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	load := MeLoader(func(context.Context) (Tenant, string, error) {
		return Tenant{}, "", db.ErrNotActiveMember
	})
	rec, body := doMe(t, load, &id)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Error != db.NotActiveMemberMessage {
		t.Errorf("error = %q, want db.NotActiveMemberMessage %q", body.Error, db.NotActiveMemberMessage)
	}
}

func TestMembershipsHandler_NotActiveMemberIs403(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	load := MembershipsLoader(func(context.Context) ([]Membership, error) {
		return nil, db.ErrNotActiveMember
	})
	rec, body := doMemberships(t, load, &id)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Error != db.NotActiveMemberMessage {
		t.Errorf("error = %q, want db.NotActiveMemberMessage %q", body.Error, db.NotActiveMemberMessage)
	}
}
