package customer_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/customer"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	archivedEvent = "customers.customer.archived"
	restoredEvent = "customers.customer.restored"
)

func TestSetStatusHandler_ArchiveThenRestore(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer archive restore")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1})

	rec := do(t, h, ctxA, http.MethodPost, "/v1/customers/"+id+"/archive", "")
	wantStatus(t, rec, http.StatusOK)
	if got := decodeCustomer(t, rec.Body.Bytes()); got.ID != id || got.Status != "archived" {
		t.Errorf("archive body = id %s status %q, want %s archived", got.ID, got.Status, id)
	}
	if st := rowStatus(t, super, id); st != "archived" {
		t.Errorf("row status after archive = %q, want archived", st)
	}

	rec = do(t, h, ctxA, http.MethodPost, "/v1/customers/"+id+"/restore", "")
	wantStatus(t, rec, http.StatusOK)
	if got := decodeCustomer(t, rec.Body.Bytes()); got.ID != id || got.Status != "active" {
		t.Errorf("restore body = id %s status %q, want %s active", got.ID, got.Status, id)
	}
	if st := rowStatus(t, super, id); st != "active" {
		t.Errorf("row status after restore = %q, want active", st)
	}
}

func TestSetStatusHandler_RedundantMoveIs409AndWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer redundant")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1})

	rec := do(t, h, ctxA, http.MethodPost, "/v1/customers/"+id+"/restore", "")
	wantStatus(t, rec, http.StatusConflict)
	if got := errorBody(t, rec); got != "redundant transition" {
		t.Errorf("restore of an active record error = %q, want redundant transition", got)
	}
	if n := len(auditRows(t, super, tenantA, restoredEvent)); n != 0 {
		t.Errorf("restored audit rows = %d, want 0", n)
	}

	wantStatus(t, do(t, h, ctxA, http.MethodPost, "/v1/customers/"+id+"/archive", ""), http.StatusOK)
	before := rowJSON(t, super, id)
	rec = do(t, h, ctxA, http.MethodPost, "/v1/customers/"+id+"/archive", "")
	wantStatus(t, rec, http.StatusConflict)
	if got := errorBody(t, rec); got != "redundant transition" {
		t.Errorf("archive of an archived record error = %q, want redundant transition", got)
	}
	if n := len(auditRows(t, super, tenantA, archivedEvent)); n != 1 {
		t.Errorf("archived audit rows = %d, want 1", n)
	}
	if after := rowJSON(t, super, id); after != before {
		t.Errorf("row changed on a redundant move:\n before %s\n after  %s", before, after)
	}
}

func TestStoreSetStatus_RedundantCheckRunsUnderTheRowLock(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer archive race")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1})

	const n = 8
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = store.SetStatus(ctxA, id, customer.StatusArchived)
		}(i)
	}
	close(start)
	wg.Wait()

	ok, redundant := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, customer.ErrRedundantTransition):
			redundant++
		default:
			t.Errorf("unexpected error from a concurrent archive: %v", err)
		}
	}
	if ok != 1 || redundant != n-1 {
		t.Errorf("concurrent archives = %d ok, %d redundant, want 1 and %d", ok, redundant, n-1)
	}
	if rows := auditRows(t, super, tenantA, archivedEvent); len(rows) != 1 {
		t.Errorf("archived audit rows = %d, want 1", len(rows))
	}
}

func TestSetStatusHandler_UnknownAndMalformedIDAre404(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer status unknown id")
	seedCustomer(t, super, tenantA, seed{TIN: tin1})

	for _, bad := range []string{uuid.NewString(), "not-a-uuid"} {
		for _, action := range []string{"archive", "restore"} {
			rec := do(t, h, ctxA, http.MethodPost, "/v1/customers/"+bad+"/"+action, "")
			wantStatus(t, rec, http.StatusNotFound)
			if got := errorBody(t, rec); got != "not found" {
				t.Errorf("%s id %q error = %q, want not found", action, bad, got)
			}
		}
	}
	for _, ev := range []string{archivedEvent, restoredEvent} {
		if n := len(auditRows(t, super, tenantA, ev)); n != 0 {
			t.Errorf("%s audit rows after 404s = %d, want 0", ev, n)
		}
	}
}

func TestStoreList_ArchivedStaysInTheUnfilteredList(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer archived listed")
	a := seedCustomer(t, super, tenantA, seed{TIN: tin1})
	b := seedCustomer(t, super, tenantA, seed{TIN: tin2})

	if _, err := store.SetStatus(ctxA, a, customer.StatusArchived); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	got, total, err := store.List(ctxA, customer.ListFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{a, b}
	gotIDs := ids(got)
	sort.Strings(want)
	sort.Strings(gotIDs)
	if total != 2 || !reflect.DeepEqual(gotIDs, want) {
		t.Errorf("unfiltered List = ids %v total %d, want %v total 2", gotIDs, total, want)
	}
}

func TestSetStatus_PreparerCanArchiveAndRestore(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA := seedTenant(t, super, "customer preparer status")
	_, ctxP := seedMember(t, super, tenantA, "preparer", "active")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1})

	wantStatus(t, do(t, h, ctxP, http.MethodPost, "/v1/customers/"+id+"/archive", ""), http.StatusOK)
	wantStatus(t, do(t, h, ctxP, http.MethodPost, "/v1/customers/"+id+"/restore", ""), http.StatusOK)
}

func TestStoreSetStatus_WritesOneAuditRowPerMove(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, subject, ctxA := seedTenantWithAdmin(t, super, "customer status audit")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1})

	if _, err := store.SetStatus(ctxA, id, customer.StatusArchived); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, err := store.SetStatus(ctxA, id, customer.StatusActive); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, tc := range []struct{ event, from, to string }{
		{archivedEvent, "active", "archived"},
		{restoredEvent, "archived", "active"},
	} {
		rows := auditRows(t, super, tenantA, tc.event)
		if len(rows) != 1 {
			t.Fatalf("%s rows = %d, want 1", tc.event, len(rows))
		}
		if rows[0].Actor != subject {
			t.Errorf("%s actor = %q, want %q", tc.event, rows[0].Actor, subject)
		}
		want := map[string]any{"id": id, "from": tc.from, "to": tc.to}
		if !reflect.DeepEqual(rows[0].Payload, want) {
			t.Errorf("%s payload = %s, want exactly %v", tc.event, rows[0].Raw, want)
		}
	}
}

func TestStoreSetStatus_AuditFailureRollsBackTheMove(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, _ := seedTenantWithAdmin(t, super, "customer status atomic")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1})

	// A 256-char non-UUID subject skips the membership lookup and fails audit_log's actor CHECK after the UPDATE.
	badCtx := auth.WithIdentity(context.Background(),
		auth.Identity{Subject: strings.Repeat("a", 256), Role: "authenticated", TenantID: tenantA})
	if _, err := store.SetStatus(badCtx, id, customer.StatusArchived); err == nil {
		t.Fatal("SetStatus with an audit-failing actor succeeded, want an error")
	}
	if st := rowStatus(t, super, id); st != "active" {
		t.Errorf("status after failed audit = %q, want active (rolled back)", st)
	}
	if n := len(auditRows(t, super, tenantA, archivedEvent)); n != 0 {
		t.Errorf("archived audit rows after failed audit = %d, want 0", n)
	}

	archived := seedCustomer(t, super, tenantA, seed{TIN: tin2, Status: "archived"})
	if _, err := store.SetStatus(badCtx, archived, customer.StatusActive); err == nil {
		t.Fatal("restore with an audit-failing actor succeeded, want an error")
	}
	if st := rowStatus(t, super, archived); st != "archived" {
		t.Errorf("status after failed restore audit = %q, want archived (rolled back)", st)
	}
	if n := len(auditRows(t, super, tenantA, restoredEvent)); n != 0 {
		t.Errorf("restored audit rows after failed audit = %d, want 0", n)
	}
}

func TestStoreSetStatus_ArchiveRestoreArchiveWritesThreeRowsInOrder(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer status order")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1})

	for _, target := range []string{customer.StatusArchived, customer.StatusActive, customer.StatusArchived} {
		if _, err := store.SetStatus(ctxA, id, target); err != nil {
			t.Fatalf("SetStatus %s: %v", target, err)
		}
	}
	rows, err := super.Query(context.Background(),
		`SELECT event FROM audit_log WHERE tenant_id = $1 AND event IN ($2, $3) ORDER BY id`, tenantA, archivedEvent, restoredEvent)
	if err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var ev string
		if err := rows.Scan(&ev); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, ev)
	}
	if want := []string{archivedEvent, restoredEvent, archivedEvent}; !reflect.DeepEqual(got, want) {
		t.Errorf("audit events in order = %v, want %v", got, want)
	}
}

func TestStoreSetStatus_ConcurrentPatchAndArchiveBothLand(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer patch vs archive")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1})

	var wg sync.WaitGroup
	start := make(chan struct{})
	var archiveErr, patchErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, archiveErr = store.SetStatus(ctxA, id, customer.StatusArchived)
	}()
	go func() {
		defer wg.Done()
		<-start
		email := "race@x.ng"
		_, patchErr = store.Update(ctxA, id, customer.UpdateInput{Email: &email})
	}()
	close(start)
	wg.Wait()

	if archiveErr != nil || patchErr != nil {
		t.Fatalf("archive err = %v, patch err = %v, want both nil", archiveErr, patchErr)
	}
	if st := rowStatus(t, super, id); st != "archived" {
		t.Errorf("status = %q, want archived", st)
	}
	var email *string
	if err := super.QueryRow(context.Background(), `SELECT email FROM customers WHERE id = $1`, id).Scan(&email); err != nil {
		t.Fatalf("read email: %v", err)
	}
	if email == nil || *email != "race@x.ng" {
		t.Errorf("email = %v, want race@x.ng (the PATCH survived the archive)", email)
	}
	if n := len(auditRows(t, super, tenantA, archivedEvent)); n != 1 {
		t.Errorf("archived rows = %d, want 1", n)
	}
	if n := len(auditRows(t, super, tenantA, updatedEvent)); n != 1 {
		t.Errorf("updated rows = %d, want 1", n)
	}
}
