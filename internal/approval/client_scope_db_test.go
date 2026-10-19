package approval

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// csApproval is a firm tenant with clients A and B, an open run on a validated invoice of each,
// and a reviewer limited to A who holds the step's workflow role.
type csApproval struct {
	f          approveFixture
	entB       string
	invB, runB string
	limited    context.Context
}

func newCSApproval(t *testing.T, super, app *pgxpool.Pool, name string) csApproval {
	t.Helper()
	f := newApproveFixture(t, super, app, name, "cs-role")
	x := csApproval{f: f}
	x.entB = seedBusinessEntity(t, super, f.tenantID, name+" Client B")
	x.invB = seedInvoice(t, super, f.tenantID, x.entB, name+"-invoice-b")
	setInvoiceStatus(t, super, x.invB, "validated")
	res, err := arm(t, app, f.tenantID, x.invB, "fp-"+name+"-b", "fixture-arm")
	if err != nil {
		t.Fatalf("arm client B's invoice: %v", err)
	}
	x.runB = res.RunID

	user := uuid.NewString()
	seedMembership(t, super, f.tenantID, user, "reviewer", "active")
	if _, err := super.Exec(context.Background(),
		`UPDATE memberships SET all_clients = false WHERE tenant_id = $1 AND user_id = $2`, f.tenantID, user); err != nil {
		t.Fatalf("limit reviewer: %v", err)
	}
	if _, err := super.Exec(context.Background(),
		`INSERT INTO membership_clients (tenant_id, user_id, entity_id) VALUES ($1, $2, $3)`, f.tenantID, user, f.entityID); err != nil {
		t.Fatalf("seed membership_clients: %v", err)
	}
	staffWorkflowRole(t, super, f.tenantID, f.roleID, user, 0)
	x.limited = auth.WithIdentity(context.Background(), auth.Identity{Subject: user, Role: "authenticated", TenantID: f.tenantID})
	return x
}

func TestClientScope_ApprovalRunOfAnotherClientIsNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	x := newCSApproval(t, super, app, "CS approval run")
	store := NewStore(app, stubFingerprinter, nil)

	if _, err := store.ApprovalRun(x.limited, x.invB); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("ApprovalRun(client B's invoice) err = %v, want ErrRunNotFound", err)
	}
	run, err := store.ApprovalRun(x.limited, x.f.invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun(own invoice): %v", err)
	}
	if run.RunID != x.f.runID {
		t.Errorf("run id = %s, want %s", run.RunID, x.f.runID)
	}
}

func TestClientScope_DecideOnAnotherClientIsNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	x := newCSApproval(t, super, app, "CS approval decide")
	store := NewStore(app, stubFingerprinter, nil)

	if _, err := store.DecideSeam(x.limited, x.invB, "approved", ""); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("DecideSeam(client B's invoice) err = %v, want ErrRunNotFound", err)
	}
	if n := len(decisionsForRun(t, super, x.runB)); n != 0 {
		t.Errorf("decision rows for client B's run = %d, want 0", n)
	}
	if _, err := store.DecideSeam(x.limited, x.f.invoiceID, "approved", ""); err != nil {
		t.Errorf("DecideSeam(own invoice) control: %v", err)
	}
}

func TestClientScope_RunAndDecideRoutesAnswerOneNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	x := newCSApproval(t, super, app, "CS approval 404")
	foreign := newApproveFixture(t, super, app, "CS approval foreign", "cs-role-foreign")
	store := NewStore(app, stubFingerprinter, nil)
	routes := map[string]http.HandlerFunc{"GET approval": RunHandler(store.ApprovalRun, nil), "POST approvals": DecideHandler(store.DecideSeam, nil)}

	serve := func(h http.HandlerFunc, method, id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/", strings.NewReader(`{"decision":"approved"}`)).WithContext(x.limited)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	for name, h := range routes {
		method := strings.Fields(name)[0]
		outside := serve(h, method, x.invB)
		if outside.Code != http.StatusNotFound || !strings.Contains(outside.Body.String(), "no approval run for this invoice") {
			t.Errorf("%s on client B's invoice: %d %s, want 404 no approval run for this invoice", name, outside.Code, outside.Body.String())
			continue
		}
		for who, id := range map[string]string{"unknown": uuid.NewString(), "another tenant's": foreign.invoiceID} {
			if got := serve(h, method, id); got.Code != outside.Code || got.Body.String() != outside.Body.String() {
				t.Errorf("%s on %s id: %d %s, differs from outside-the-set %d %s", name, who, got.Code, got.Body.String(), outside.Code, outside.Body.String())
			}
		}
	}
	if n := len(decisionsForRun(t, super, x.runB)); n != 0 {
		t.Errorf("decision rows for client B's run = %d, want 0", n)
	}
	if w := serve(routes["GET approval"], "GET", x.f.invoiceID); w.Code != http.StatusOK {
		t.Errorf("GET approval on own invoice: %d %s, want 200", w.Code, w.Body.String())
	}
}

func TestClientScope_AdminAndAllClientsMemberStillReadEveryRun(t *testing.T) {
	super, app := dbTestPools(t)
	x := newCSApproval(t, super, app, "CS approval full")
	store := NewStore(app, stubFingerprinter, nil)
	for _, role := range []string{"admin", "preparer"} {
		ctx, _ := callerCtx(t, super, x.f.tenantID, role, "active")
		run, err := store.ApprovalRun(ctx, x.invB)
		if err != nil || run.RunID != x.runB {
			t.Errorf("%s ApprovalRun(client B's invoice) = %q, %v, want run %s", role, run.RunID, err, x.runB)
		}
	}
}
