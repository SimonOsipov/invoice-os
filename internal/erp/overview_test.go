package erp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// The wire shape of Design -> "API contracts", decoded from the marshalled Overview so these tests
// pin the JSON the screen reads, not the Go field names.
type docWire struct {
	ID                      string     `json:"id"`
	SourceDocID             string     `json:"source_doc_id"`
	SourceRevision          string     `json:"source_revision"`
	State                   string     `json:"state"`
	Failure                 *string    `json:"failure"`
	InvoiceID               *string    `json:"invoice_id"`
	InvoiceNumber           *string    `json:"invoice_number"`
	DiffersFromERP          bool       `json:"differs_from_erp"`
	ChangedAfterClearanceAt *time.Time `json:"changed_after_clearance_at"`
	DeletedInERPAt          *time.Time `json:"deleted_in_erp_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
}

type heldWire struct {
	docWire
	Reasons []string `json:"reasons"`
}

type writebackWire struct {
	ID            string  `json:"id"`
	InvoiceID     string  `json:"invoice_id"`
	InvoiceNumber *string `json:"invoice_number"`
	Outcome       string  `json:"outcome"`
	State         string  `json:"state"`
	Reason        *string `json:"reason"`
	Attempt       int     `json:"attempt"`
}

type overviewWire struct {
	Connection struct {
		ID       string `json:"id"`
		EntityID string `json:"entity_id"`
		System   string `json:"system"`
		Status   string `json:"status"`
	} `json:"connection"`
	Activity struct {
		Since     time.Time      `json:"since"`
		Counts    map[string]int `json:"counts"`
		Documents []docWire      `json:"documents"`
	} `json:"activity"`
	Writebacks []writebackWire `json:"writebacks"`
	Held       []heldWire      `json:"held"`
}

// readOverview answers the Overview as the JSON the route would send, and that JSON.
func readOverview(t *testing.T, s *Store, ctx context.Context, entityID, connectionID string) (overviewWire, []byte) {
	t.Helper()
	ov, err := s.Overview(ctx, entityID, connectionID)
	if err != nil {
		t.Fatalf("Overview(%s, %s): %v", entityID, connectionID, err)
	}
	raw, err := json.Marshal(ov)
	if err != nil {
		t.Fatalf("marshal Overview: %v", err)
	}
	var w overviewWire
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("decode Overview %s: %v", raw, err)
	}
	return w, raw
}

func docIDs(docs []docWire) []string {
	out := []string{}
	for _, d := range docs {
		out = append(out, d.SourceDocID)
	}
	return out
}

func heldIDs(held []heldWire) []string {
	out := []string{}
	for _, h := range held {
		out = append(out, h.SourceDocID)
	}
	return out
}

func keysOfRaw(m map[string]json.RawMessage) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

// ovTaken plants an invoice and a drafted or linked document on it; extra carries the other columns.
func ovTaken(e *ingestEnv, docID, state, number string, extra plantDoc) string {
	e.t.Helper()
	inv := e.plantInvoice(plantInv{number: number})
	extra.docID, extra.state, extra.invoiceID = docID, state, &inv
	e.plantDocument(extra)
	return inv
}

func ovMember(t *testing.T, e *ingestEnv, role string) context.Context {
	return seedMember(t, e.super, e.tenant, role, "active", true)
}

func TestOverview_ActivityCountsTheLastDayByState(t *testing.T) {
	e := newIngest(t)
	s := fakeStore(t, e.app)
	ctx := ovMember(t, e, "admin")

	e.plantDocument(plantDoc{docID: "d-pending", state: "pending", updatedAgo: 1 * time.Minute})
	ovTaken(e, "d-drafted", "drafted", "FK1-0001", plantDoc{updatedAgo: 2 * time.Minute})
	ovTaken(e, "d-linked", "linked", "FK1-0002", plantDoc{updatedAgo: 3 * time.Minute})
	e.plantDocument(plantDoc{docID: "d-held", state: "held", updatedAgo: 4 * time.Minute})
	e.plantDocument(plantDoc{docID: "d-failed", state: "failed", failure: sp("exhausted"), updatedAgo: 5 * time.Minute})
	e.plantDocument(plantDoc{docID: "d-deleted", state: "deleted", deleted: tp(time.Now()), updatedAgo: 6 * time.Minute})
	ovTaken(e, "d-old", "drafted", "FK1-0003", plantDoc{updatedAgo: 48 * time.Hour})

	before := time.Now()
	ov, _ := readOverview(t, s, ctx, e.entity, e.conn)
	after := time.Now()

	if lo, hi := before.Add(-24*time.Hour-time.Second), after.Add(-24*time.Hour+time.Second); ov.Activity.Since.Before(lo) || ov.Activity.Since.After(hi) {
		t.Errorf("activity.since = %s, want now - 24h (%s..%s)", ov.Activity.Since, lo, hi)
	}
	want := map[string]int{"pulled": 6, "pending": 1, "drafted": 1, "linked": 1, "held": 1, "failed": 1, "deleted": 1}
	if fmt.Sprint(ov.Activity.Counts) != fmt.Sprint(want) {
		t.Errorf("counts = %v, want %v (the 2-day-old row is not counted)", ov.Activity.Counts, want)
	}
	wantDocs := []string{"d-pending", "d-drafted", "d-linked", "d-held", "d-failed", "d-deleted", "d-old"}
	if got := docIDs(ov.Activity.Documents); !slices.Equal(got, wantDocs) {
		t.Errorf("documents = %v, want every row newest first: %v", got, wantDocs)
	}
}

func TestOverview_DocumentsAreTheFiftyLatest(t *testing.T) {
	e := newIngest(t)
	s := fakeStore(t, e.app)
	ctx := ovMember(t, e, "admin")

	var want []string
	for i := range 52 {
		id := fmt.Sprintf("d%02d", i)
		e.plantDocument(plantDoc{docID: id, state: "pending", updatedAgo: time.Duration(i) * time.Minute})
		if i < 50 {
			want = append(want, id)
		}
	}

	ov, _ := readOverview(t, s, ctx, e.entity, e.conn)
	if got := docIDs(ov.Activity.Documents); !slices.Equal(got, want) {
		t.Errorf("documents = %v, want the 50 newest in order (d50 and d51 absent)", got)
	}
	if ov.Activity.Counts["pulled"] != 52 || ov.Activity.Counts["pending"] != 52 {
		t.Errorf("counts = %v, want all 52 rows counted: the limit cuts the list, not the counts", ov.Activity.Counts)
	}
}

func TestOverview_DocumentCarriesItsInvoiceNumber(t *testing.T) {
	e := newIngest(t)
	s := fakeStore(t, e.app)
	ctx := ovMember(t, e, "admin")

	inv := ovTaken(e, "d-linked", "linked", "FK1-0007", plantDoc{updatedAgo: time.Minute})
	e.plantDocument(plantDoc{docID: "d-failed", state: "failed", failure: sp("refused"), updatedAgo: 2 * time.Minute})

	ov, raw := readOverview(t, s, ctx, e.entity, e.conn)
	if len(ov.Activity.Documents) != 2 {
		t.Fatalf("documents = %v, want the linked and the failed row", docIDs(ov.Activity.Documents))
	}
	linked, failed := ov.Activity.Documents[0], ov.Activity.Documents[1]
	if linked.SourceDocID != "d-linked" || linked.InvoiceID == nil || *linked.InvoiceID != inv ||
		linked.InvoiceNumber == nil || *linked.InvoiceNumber != "FK1-0007" || linked.Failure != nil || linked.State != "linked" {
		t.Errorf("linked row = %+v, want invoice %s numbered FK1-0007 and no failure", linked, inv)
	}
	if failed.SourceDocID != "d-failed" || failed.Failure == nil || *failed.Failure != "refused" || failed.State != "failed" {
		t.Errorf("failed row = %+v, want its failure refused", failed)
	}

	// The failed row's invoice keys are present and null, not absent.
	var shape struct {
		Activity struct {
			Documents []map[string]json.RawMessage `json:"documents"`
		} `json:"activity"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil || len(shape.Activity.Documents) != 2 {
		t.Fatalf("decode %s: %v", raw, err)
	}
	for _, k := range []string{"invoice_id", "invoice_number"} {
		if v, ok := shape.Activity.Documents[1][k]; !ok || string(v) != "null" {
			t.Errorf("failed row %s = %s (present %v), want null", k, v, ok)
		}
	}
	wantKeys := []string{"changed_after_clearance_at", "deleted_in_erp_at", "differs_from_erp", "failure", "id", "invoice_id", "invoice_number", "source_doc_id", "source_revision", "state", "updated_at"}
	if got := keysOfRaw(shape.Activity.Documents[0]); !slices.Equal(got, wantKeys) {
		t.Errorf("document keys = %v, want %v", got, wantKeys)
	}
}

func TestOverview_EmptyConnectionAnswersEmptySlices(t *testing.T) {
	e := newIngest(t)
	s := fakeStore(t, e.app)
	ctx := ovMember(t, e, "admin")

	ov, raw := readOverview(t, s, ctx, e.entity, e.conn)
	if ov.Connection.ID != e.conn || ov.Connection.EntityID != e.entity || ov.Connection.System != fakeSystem || ov.Connection.Status != "active" {
		t.Errorf("connection = %+v, want %s of %s, fake, active", ov.Connection, e.conn, e.entity)
	}
	var shape struct {
		Activity struct {
			Documents json.RawMessage `json:"documents"`
		} `json:"activity"`
		Writebacks json.RawMessage `json:"writebacks"`
		Held       json.RawMessage `json:"held"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	for name, got := range map[string]string{"documents": string(shape.Activity.Documents), "writebacks": string(shape.Writebacks), "held": string(shape.Held)} {
		if got != "[]" {
			t.Errorf("%s = %s, want [] (never null)", name, got)
		}
	}
	zero := map[string]int{"pulled": 0, "pending": 0, "drafted": 0, "linked": 0, "held": 0, "failed": 0, "deleted": 0}
	if fmt.Sprint(ov.Activity.Counts) != fmt.Sprint(zero) {
		t.Errorf("counts = %v, want seven zeros", ov.Activity.Counts)
	}
}

// ovWriteback plants one write-back attempt of the connection, created ago before now.
func ovWriteback(e *ingestEnv, conn, docID, invoiceID, outcome string, attempt int, state string, reason *string, ago time.Duration) string {
	e.t.Helper()
	var id string
	mustScan(e.t, e.super,
		`INSERT INTO erp_writebacks (tenant_id, entity_id, connection_id, source_document_id, invoice_id, result_version, outcome, attempt, state, reason, river_job_id, created_at)
		 VALUES ($1, (SELECT entity_id FROM erp_connections WHERE id = $2), $2, $3, $4, 1, $5, $6, $7, $8, 1, now() - $9::bigint * interval '1 second')
		 RETURNING id::text`,
		[]any{e.tenant, conn, docID, invoiceID, outcome, attempt, state, reason, int64(ago.Seconds())}, &id)
	return id
}

func TestOverview_WritebacksAreTheConnectionsLatestAttempts(t *testing.T) {
	e := newIngest(t)
	s := fakeStore(t, e.app)
	ctx := ovMember(t, e, "admin")
	c2 := e.addConnection(e.entity, "fake-co-2")

	invA := e.plantInvoice(plantInv{number: "FK1-0001"})
	invB := e.plantInvoice(plantInv{number: "FK1-0002"})
	invC := e.plantInvoice(plantInv{number: "FK1-0003"})
	docA := e.plantDocument(plantDoc{docID: "dA", state: "linked", invoiceID: &invA})
	docB := e.plantDocument(plantDoc{docID: "dB", state: "linked", invoiceID: &invB})
	docC := e.plantDocument(plantDoc{docID: "dC", state: "linked", invoiceID: &invC})
	docX := e.plantDocument(plantDoc{conn: c2, docID: "dX", state: "linked", invoiceID: &invA})

	// Planted out of order: the answer follows created_at, not insertion.
	skipped := ovWriteback(e, e.conn, docC, invC, "accepted", 1, "skipped", sp("disconnected"), time.Minute)
	ovWriteback(e, c2, docX, invA, "accepted", 1, "succeeded", nil, 0) // newest of all, another connection
	failed := ovWriteback(e, e.conn, docB, invB, "rejected", 2, "failed", sp("vendor"), 2*time.Minute)
	succeeded := ovWriteback(e, e.conn, docA, invA, "accepted", 1, "succeeded", nil, 3*time.Minute)

	ov, _ := readOverview(t, s, ctx, e.entity, e.conn)
	if len(ov.Writebacks) != 3 {
		t.Fatalf("writebacks = %+v, want this connection's 3 attempts (not the other connection's)", ov.Writebacks)
	}
	want := []struct {
		id, invoice, number, outcome, state string
		reason                              *string
		attempt                             int
	}{
		{skipped, invC, "FK1-0003", "accepted", "skipped", sp("disconnected"), 1},
		{failed, invB, "FK1-0002", "rejected", "failed", sp("vendor"), 2},
		{succeeded, invA, "FK1-0001", "accepted", "succeeded", nil, 1},
	}
	for i, w := range want {
		g := ov.Writebacks[i]
		reasonOK := (g.Reason == nil) == (w.reason == nil) && (w.reason == nil || *g.Reason == *w.reason)
		if g.ID != w.id || g.InvoiceID != w.invoice || g.InvoiceNumber == nil || *g.InvoiceNumber != w.number ||
			g.Outcome != w.outcome || g.State != w.state || !reasonOK || g.Attempt != w.attempt {
			t.Errorf("writebacks[%d] = %+v, want %+v", i, g, w)
		}
	}
}

func TestOverview_HeldListsEachFlagAndNothingElse(t *testing.T) {
	e := newIngest(t)
	s := fakeStore(t, e.app)
	ctx := ovMember(t, e, "admin")
	now := time.Now()
	min := time.Minute

	invDiffers := ovTaken(e, "d-differs", "linked", "FK1-0001", plantDoc{differs: true, updatedAgo: 1 * min})
	ovTaken(e, "d-changed", "linked", "FK1-0002", plantDoc{changed: tp(now.Add(-time.Hour)), updatedAgo: 2 * min})
	invDeleted := ovTaken(e, "d-deleted-inv", "linked", "FK1-0003", plantDoc{deleted: tp(now), updatedAgo: 3 * min})
	e.plantDocument(plantDoc{docID: "d-deleted-none", state: "deleted", deleted: tp(now), updatedAgo: 4 * min})
	e.plantDocument(plantDoc{docID: "d-held", state: "held", updatedAgo: 5 * min})
	e.plantDocument(plantDoc{docID: "d-failed", state: "failed", failure: sp("exhausted"), updatedAgo: 6 * min})
	ovTaken(e, "d-clean", "drafted", "FK1-0004", plantDoc{updatedAgo: 7 * min})

	ov, _ := readOverview(t, s, ctx, e.entity, e.conn)

	wantOrder := []string{"d-differs", "d-changed", "d-deleted-inv", "d-deleted-none", "d-held", "d-failed"}
	if got := heldIDs(ov.Held); !slices.Equal(got, wantOrder) {
		t.Fatalf("held = %v, want %v (the clean drafted row is not held)", got, wantOrder)
	}
	wantReasons := [][]string{{"differs_from_erp"}, {"changed_after_clearance"}, {"deleted_in_erp"}, {"deleted_in_erp"}, {"number_held"}, {"failed"}}
	for i, h := range ov.Held {
		if !slices.Equal(h.Reasons, wantReasons[i]) {
			t.Errorf("%s reasons = %v, want %v", h.SourceDocID, h.Reasons, wantReasons[i])
		}
	}
	if h := ov.Held[0]; h.InvoiceID == nil || *h.InvoiceID != invDiffers || h.InvoiceNumber == nil || *h.InvoiceNumber != "FK1-0001" || !h.DiffersFromERP {
		t.Errorf("differs row = %+v, want its invoice %s and the flag", h, invDiffers)
	}
	if h := ov.Held[2]; h.InvoiceID == nil || *h.InvoiceID != invDeleted || h.DeletedInERPAt == nil {
		t.Errorf("deleted-with-invoice row = %+v, want invoice_id %s kept and deleted_in_erp_at", h, invDeleted)
	}
	if h := ov.Held[3]; h.InvoiceID != nil {
		t.Errorf("deleted-without-invoice row = %+v, want no invoice_id", h)
	}
	if h := ov.Held[5]; h.Failure == nil || *h.Failure != "exhausted" {
		t.Errorf("failed row = %+v, want failure exhausted", h)
	}
	if got := docIDs(ov.Activity.Documents); len(got) != 7 || !slices.Contains(got, "d-clean") {
		t.Errorf("documents = %v, want all 7 rows including the clean one", got)
	}
}

func TestOverview_HeldReasonsKeepTheirOrder(t *testing.T) {
	e := newIngest(t)
	s := fakeStore(t, e.app)
	ctx := ovMember(t, e, "admin")
	now := time.Now()

	ovTaken(e, "d-both", "linked", "FK1-0001", plantDoc{differs: true, changed: tp(now), updatedAgo: 1 * time.Minute})
	// A failed row that differs and was deleted reads in the fixed order too.
	e.plantDocument(plantDoc{docID: "d-all", state: "failed", failure: sp("refused"), differs: true, deleted: tp(now), updatedAgo: 2 * time.Minute})

	ov, _ := readOverview(t, s, ctx, e.entity, e.conn)
	got := map[string][]string{}
	for _, h := range ov.Held {
		got[h.SourceDocID] = h.Reasons
	}
	if len(got) != 2 {
		t.Fatalf("held = %v, want the two flagged documents", heldIDs(ov.Held))
	}
	if want := []string{"differs_from_erp", "changed_after_clearance"}; !slices.Equal(got["d-both"], want) {
		t.Errorf("d-both reasons = %v, want %v", got["d-both"], want)
	}
	if want := []string{"differs_from_erp", "deleted_in_erp", "failed"}; !slices.Equal(got["d-all"], want) {
		t.Errorf("d-all reasons = %v, want %v", got["d-all"], want)
	}
}

func TestOverview_APreparerReads(t *testing.T) {
	e := newIngest(t)
	s := fakeStore(t, e.app)
	inv := ovTaken(e, "d1", "linked", "FK1-0001", plantDoc{changed: tp(time.Now())})

	for _, role := range []string{"admin", "preparer", "reviewer"} {
		ctx := ovMember(t, e, role)
		ov, _ := readOverview(t, s, ctx, e.entity, e.conn) // no ErrNotPermitted: it fatals on any error
		if ov.Connection.ID != e.conn || len(ov.Activity.Documents) != 1 || len(ov.Held) != 1 {
			t.Errorf("%s: Overview = %+v, want the connection with its 1 document, held", role, ov)
		}
		marks, err := s.Marks(ctx, e.entity, inv)
		if err != nil || len(marks) != 1 {
			t.Errorf("%s: Marks = %+v, %v, want the invoice's 1 mark", role, marks, err)
		}
	}
}

func TestOverview_ALockedWorkspaceReads(t *testing.T) {
	e := newIngest(t)
	s := fakeStore(t, e.app)
	ctx := ovMember(t, e, "admin")
	inv := ovTaken(e, "d1", "linked", "FK1-0001", plantDoc{})
	lockWorkspace(t, e.super, e.tenant, subjectOf(ctx))

	// The control: the lock refuses a write, so the reads below pass because they are read-only.
	if _, err := s.Disconnect(ctx, e.entity, e.conn); !errors.Is(err, db.ErrWorkspaceLocked) {
		t.Fatalf("Disconnect in a locked workspace = %v, want ErrWorkspaceLocked", err)
	}
	ov, _ := readOverview(t, s, ctx, e.entity, e.conn)
	if ov.Connection.ID != e.conn || len(ov.Activity.Documents) != 1 {
		t.Errorf("locked overview = %+v, want the connection and its document", ov)
	}
	if marks, err := s.Marks(ctx, e.entity, inv); err != nil || len(marks) != 1 {
		t.Errorf("locked Marks = %+v, %v, want 1 mark", marks, err)
	}
}

func TestOverview_AnotherClientsConnectionIsNotFound(t *testing.T) {
	e := newIngest(t)
	s := fakeStore(t, e.app)
	ctx := ovMember(t, e, "admin") // sees every client: only the SQL client check can refuse
	b := e.addClient("client B")

	// The control: the connection answers under its own client.
	if ov, _ := readOverview(t, s, ctx, e.entity, e.conn); ov.Connection.ID != e.conn {
		t.Fatalf("connection = %+v, want %s under its own client", ov.Connection, e.conn)
	}
	if _, err := s.Overview(ctx, b, e.conn); !errors.Is(err, ErrNoSuchRecord) {
		t.Errorf("Overview of client A's connection under client B = %v, want ErrNoSuchRecord", err)
	}
	if _, err := s.Overview(ctx, e.entity, uuid.NewString()); !errors.Is(err, ErrNoSuchRecord) {
		t.Errorf("Overview of an unknown connection = %v, want ErrNoSuchRecord", err)
	}
}

func TestOverview_ClientOutsideScopeIsNotFound(t *testing.T) {
	e := newIngest(t)
	s := fakeStore(t, e.app)
	b := e.addClient("client B")
	inv := e.plantInvoice(plantInv{number: numOne})

	scopedToA := seedMember(t, e.super, e.tenant, "preparer", "active", false, e.entity)
	if ov, _ := readOverview(t, s, scopedToA, e.entity, e.conn); ov.Connection.ID != e.conn {
		t.Fatalf("a member scoped to the client reads %+v, want its connection", ov.Connection)
	}
	scopedToB := seedMember(t, e.super, e.tenant, "preparer", "active", false, b)
	if _, err := s.Overview(scopedToB, e.entity, e.conn); !errors.Is(err, ErrNoSuchRecord) {
		t.Errorf("Overview for a client outside the scope = %v, want ErrNoSuchRecord", err)
	}
	if _, err := s.Marks(scopedToB, e.entity, inv); !errors.Is(err, ErrNoSuchRecord) {
		t.Errorf("Marks for a client outside the scope = %v, want ErrNoSuchRecord", err)
	}
}

func TestOverview_MalformedIDIsNotFound(t *testing.T) {
	e := newIngest(t)
	s := fakeStore(t, e.app)
	ctx := ovMember(t, e, "admin")
	inv := e.plantInvoice(plantInv{number: numOne})

	for name, err := range map[string]error{
		"overview, connection x": func() error { _, err := s.Overview(ctx, e.entity, "x"); return err }(),
		"overview, client x":     func() error { _, err := s.Overview(ctx, "x", e.conn); return err }(),
		"overview, empty":        func() error { _, err := s.Overview(ctx, e.entity, ""); return err }(),
		"marks, invoice x":       func() error { _, err := s.Marks(ctx, e.entity, "x"); return err }(),
		"marks, client x":        func() error { _, err := s.Marks(ctx, "x", inv); return err }(),
	} {
		if !errors.Is(err, ErrNoSuchRecord) {
			t.Errorf("%s = %v, want ErrNoSuchRecord and no query error", name, err)
		}
	}
}

func TestMarks_AnotherClientsInvoiceIsNotFound(t *testing.T) {
	e := newIngest(t)
	s := fakeStore(t, e.app)
	ctx := ovMember(t, e, "admin")
	b := e.addClient("client B")
	inv := e.plantInvoice(plantInv{number: numOne})

	if marks, err := s.Marks(ctx, e.entity, inv); err != nil || marks == nil {
		t.Fatalf("Marks under its own client = %+v, %v, want an empty answer", marks, err)
	}
	if _, err := s.Marks(ctx, b, inv); !errors.Is(err, ErrNoSuchRecord) {
		t.Errorf("Marks of client A's invoice under client B = %v, want ErrNoSuchRecord", err)
	}
	if _, err := s.Marks(ctx, e.entity, uuid.NewString()); !errors.Is(err, ErrNoSuchRecord) {
		t.Errorf("Marks of an unknown invoice = %v, want ErrNoSuchRecord", err)
	}
}

func TestOverview_AnotherTenantsRowsAreNotFound(t *testing.T) {
	e := newIngest(t)
	s := fakeStore(t, e.app)
	ctx := ovMember(t, e, "admin")
	other := newIngestOn(t, e.super, e.app, keyOne)
	otherInv := other.plantInvoice(plantInv{number: numOne})

	otherCtx := ovMember(t, other, "admin")
	if ov, _ := readOverview(t, s, otherCtx, other.entity, other.conn); ov.Connection.ID != other.conn {
		t.Fatalf("the other tenant's admin reads %+v, want its own connection", ov.Connection)
	}
	if _, err := s.Overview(ctx, e.entity, other.conn); !errors.Is(err, ErrNoSuchRecord) {
		t.Errorf("another tenant's connection under my client = %v, want ErrNoSuchRecord", err)
	}
	if _, err := s.Overview(ctx, other.entity, other.conn); !errors.Is(err, ErrNoSuchRecord) {
		t.Errorf("another tenant's client = %v, want ErrNoSuchRecord", err)
	}
	if _, err := s.Marks(ctx, e.entity, otherInv); !errors.Is(err, ErrNoSuchRecord) {
		t.Errorf("another tenant's invoice under my client = %v, want ErrNoSuchRecord", err)
	}
}

func TestMarks_ReturnsTheInvoicesMarks(t *testing.T) {
	e := newIngest(t)
	s := fakeStore(t, e.app)
	ctx := ovMember(t, e, "admin")
	inv := e.plantInvoice(plantInv{number: numOne, status: "accepted"})
	lone := e.plantInvoice(plantInv{number: "FK1-0002"})
	changed := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	e.plantDocument(plantDoc{docID: "d1", state: "linked", invoiceID: &inv, changed: &changed})

	marks, err := s.Marks(ctx, e.entity, inv)
	if err != nil || len(marks) != 1 {
		t.Fatalf("Marks = %+v, %v, want the one linked document", marks, err)
	}
	m := marks[0]
	if m.ConnectionID != e.conn || m.System != fakeSystem || m.SourceDocID != "d1" || m.State != "linked" ||
		m.ChangedAfterClearanceAt == nil || !m.ChangedAfterClearanceAt.Equal(changed) || m.DiffersFromERP || m.DeletedInERPAt != nil {
		t.Errorf("mark = %+v, want d1 changed at %s", m, changed)
	}

	none, err := s.Marks(ctx, e.entity, lone)
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("Marks of an unlinked invoice = %#v, %v, want an empty non-nil slice", none, err)
	}
}

func TestOverview_WritebacksAreTheFiftyLatest(t *testing.T) {
	e := newIngest(t)
	s := fakeStore(t, e.app)
	ctx := ovMember(t, e, "admin")
	inv := e.plantInvoice(plantInv{number: "FK1-0001"})
	doc := e.plantDocument(plantDoc{docID: "d1", state: "linked", invoiceID: &inv})

	var want []string
	for i := range 51 {
		id := ovWriteback(e, e.conn, doc, inv, "accepted", i+1, "succeeded", nil, time.Duration(i+1)*time.Minute)
		if i < 50 {
			want = append(want, id)
		}
	}
	ov, _ := readOverview(t, s, ctx, e.entity, e.conn)
	var got []string
	for _, w := range ov.Writebacks {
		got = append(got, w.ID)
	}
	if !slices.Equal(got, want) {
		t.Errorf("writebacks = %d rows, want the 50 newest in order (the oldest absent)", len(got))
	}
}

func TestOverview_HeldIsCappedAtOneHundred(t *testing.T) {
	e := newIngest(t)
	s := fakeStore(t, e.app)
	ctx := ovMember(t, e, "admin")

	var want []string
	for i := range 101 {
		id := fmt.Sprintf("h%03d", i)
		e.plantDocument(plantDoc{docID: id, state: "held", updatedAgo: time.Duration(i) * time.Second})
		if i < 100 {
			want = append(want, id)
		}
	}
	ov, _ := readOverview(t, s, ctx, e.entity, e.conn)
	if got := heldIDs(ov.Held); !slices.Equal(got, want) {
		t.Errorf("held = %d rows, want the 100 newest in order (h100 absent)", len(got))
	}
	if ov.Activity.Counts["held"] != 101 {
		t.Errorf("counts = %v, want all 101 held rows counted", ov.Activity.Counts)
	}
}

// Rows with one timestamp read in id order (documents, held) and id descending (write-backs).
func TestOverview_TiesBreakByID(t *testing.T) {
	e := newIngest(t)
	s := fakeStore(t, e.app)
	ctx := ovMember(t, e, "admin")
	inv := e.plantInvoice(plantInv{number: "FK1-0001"})

	docByID := map[string]string{}
	var ids []string
	for _, n := range []string{"a", "b", "c", "d"} {
		id := e.plantDocument(plantDoc{docID: "t-" + n, state: "failed", failure: sp("refused")})
		docByID[id] = "t-" + n
		ids = append(ids, id)
	}
	var wbs []string
	for i := range 4 {
		wbs = append(wbs, ovWriteback(e, e.conn, ids[0], inv, "accepted", i+1, "succeeded", nil, time.Minute))
	}
	mustExec(t, e.super, `UPDATE erp_source_documents SET updated_at = '2026-01-01T00:00:00Z' WHERE connection_id = $1`, e.conn)
	mustExec(t, e.super, `UPDATE erp_writebacks SET created_at = '2026-01-01T00:00:00Z' WHERE connection_id = $1`, e.conn)

	slices.Sort(ids)
	var wantDocs []string
	for _, id := range ids {
		wantDocs = append(wantDocs, docByID[id])
	}
	ov, _ := readOverview(t, s, ctx, e.entity, e.conn)
	if got := docIDs(ov.Activity.Documents); !slices.Equal(got, wantDocs) {
		t.Errorf("documents = %v, want id order %v", got, wantDocs)
	}
	if got := heldIDs(ov.Held); !slices.Equal(got, wantDocs) {
		t.Errorf("held = %v, want id order %v", got, wantDocs)
	}
	slices.Sort(wbs)
	slices.Reverse(wbs)
	var got []string
	for _, w := range ov.Writebacks {
		got = append(got, w.ID)
	}
	if !slices.Equal(got, wbs) {
		t.Errorf("writebacks = %v, want id descending %v", got, wbs)
	}
}
