// client_scope_db_test.go: a member limited to client A reads no extraction of a document
// that only client B's rows cite. Helper prefix: cse.
package extraction_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// cseFixture: X is cited by A's invoice, Y by B's import batch and B's invoice, Z by nothing.
type cseFixture struct {
	tenant                     string
	docX, docY, docZ           string
	jobX, jobY, jobZ           string
	invX, invY                 string
	admin, limited, unassigned context.Context
	rec                        *rdaRecorder
	reader                     *extraction.Reader
}

func cseMember(t *testing.T, ctx context.Context, tenant, role string, clients ...string) context.Context {
	t.Helper()
	user := uuid.NewString()
	super := stRequire(t).super
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role, status, all_clients) VALUES ($1, $2, $3, 'active', false)`,
		tenant, user, role); err != nil {
		t.Fatalf("seed %s: %v", role, err)
	}
	for _, e := range clients {
		if _, err := super.Exec(ctx,
			`INSERT INTO membership_clients (tenant_id, user_id, entity_id) VALUES ($1, $2, $3)`, tenant, user, e); err != nil {
			t.Fatalf("seed membership_clients: %v", err)
		}
	}
	return auth.WithIdentity(ctx, auth.Identity{Subject: user, Role: "authenticated", TenantID: tenant})
}

func newCSEFixture(t *testing.T) cseFixture {
	t.Helper()
	ctx := t.Context()
	f := cseFixture{rec: &rdaRecorder{}}
	f.tenant, f.docX = stTenant(t, ctx)
	rdaPurge(t, f.tenant)
	now := time.Now().UTC().Truncate(time.Microsecond)
	f.docY = rvdSeedDocumentMeta(t, ctx, f.tenant, nil, nil, 1024, strings.Repeat("b", 64), now)
	f.docZ = rvdSeedDocumentMeta(t, ctx, f.tenant, nil, nil, 1024, strings.Repeat("c", 64), now)
	entA, entB := cxEntity(t, ctx, f.tenant), cxEntity(t, ctx, f.tenant)
	f.invX = cxInvoice(t, ctx, f.tenant, entA, f.docX, "CSE-X", "draft")
	f.invY = cxInvoice(t, ctx, f.tenant, entB, f.docY, "CSE-Y", "draft")
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO import_batches (tenant_id, entity_id, document_id) VALUES ($1, $2, $3)`,
		f.tenant, entB, f.docY); err != nil {
		t.Fatalf("seed import batch: %v", err)
	}
	for doc, job := range map[string]*string{f.docX: &f.jobX, f.docY: &f.jobY, f.docZ: &f.jobZ} {
		*job = rdSeedJob(t, ctx, f.tenant, doc, "succeeded", now, nil)
		rvdSeedField(t, ctx, f.tenant, *job, "total", rvdStr("100.00"), nil, 0, nil, now)
		rvdSeedPage(t, ctx, f.tenant, doc, 1, 100, 100)
	}
	f.admin = cseMember(t, ctx, f.tenant, "admin")
	f.limited = cseMember(t, ctx, f.tenant, "reviewer", entA)
	f.unassigned = cseMember(t, ctx, f.tenant, "preparer")
	f.reader = &extraction.Reader{Pool: stRequire(t).app, Audit: f.rec.record}
	return f
}

func TestClientScope_ExtractionJobsOfAHiddenDocumentAreAbsent(t *testing.T) {
	f := newCSEFixture(t)
	for _, c := range []struct {
		name string
		doc  string
		want int
	}{{"X", f.docX, 1}, {"Y", f.docY, 0}, {"Z", f.docZ, 1}} {
		got, err := f.reader.JobsForDocument(f.limited, c.doc)
		if err != nil || got.Jobs == nil || len(got.Jobs) != c.want {
			t.Errorf("limited JobsForDocument(%s) = %+v, err %v, want %d job(s), non-nil", c.name, got.Jobs, err, c.want)
		}
	}
}

func TestClientScope_ExtractionDetailAndPageOfAHiddenDocumentAreNotFound(t *testing.T) {
	f := newCSEFixture(t)
	ctx := t.Context()

	if _, err := f.reader.Detail(f.limited, f.jobY); !errors.Is(err, extraction.ErrNotFound) {
		t.Errorf("limited Detail(Y's job) err = %v, want ErrNotFound", err)
	}
	if _, err := f.reader.PageImageKey(f.limited, f.jobY, 1); !errors.Is(err, extraction.ErrNotFound) {
		t.Errorf("limited PageImageKey(Y's job, 1) err = %v, want ErrNotFound", err)
	}
	if n := rdaReadRows(t, ctx, f.tenant, f.docY); n != 0 {
		t.Errorf("document.read rows for Y = %d, want 0", n)
	}
	d, err := f.reader.Detail(f.limited, f.jobX)
	if err != nil || len(d.Fields) != 1 || len(d.Pages) != 1 {
		t.Errorf("limited Detail(X's job) = %d field(s), %d page(s), err %v, want 1, 1, nil", len(d.Fields), len(d.Pages), err)
	}
	if _, err := f.reader.PageImageKey(f.limited, f.jobX, 1); err != nil {
		t.Errorf("limited PageImageKey(X's job, 1): %v", err)
	}
	if n := rdaReadRows(t, ctx, f.tenant, f.docX); n != 1 {
		t.Errorf("document.read rows for X = %d, want 1", n)
	}
	if _, err := f.reader.Detail(f.limited, f.jobZ); err != nil {
		t.Errorf("limited Detail(Z's job): %v", err)
	}
	if _, err := f.reader.Detail(f.unassigned, f.jobY); !errors.Is(err, extraction.ErrNotFound) {
		t.Errorf("unassigned Detail(Y's job) err = %v, want ErrNotFound", err)
	}
}

func TestClientScope_ExtractionWritesOnAHiddenJobAre404AndWriteNothing(t *testing.T) {
	f := newCSEFixture(t)
	ctx := t.Context()
	linesBefore := lixLineRows(t, ctx, f.invY)

	unknown := cxServe(t, f.limited, uuid.NewString(), "total", cxBody(cxTotalAfter), cxApplier(true, nil), cxAuditor(nil))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("control: correction on an unknown job = %d %s, want 404", unknown.Code, unknown.Body.String())
	}
	got := cxServe(t, f.limited, f.jobY, "total", cxBody(cxTotalAfter), cxApplier(true, nil), cxAuditor(nil))
	if got.Code != http.StatusNotFound || got.Body.String() != unknown.Body.String() {
		t.Errorf("correction on Y's job = %d %s, want the unknown-job answer %d %s", got.Code, got.Body.String(), unknown.Code, unknown.Body.String())
	}
	lines := lixDBServe(t, f.limited, f.jobY, lixLinesBody(2), lixApplier(lixApplyOpts{write: true}), cxAuditor(nil))
	if lines.Code != http.StatusNotFound {
		t.Errorf("line-items on Y's job = %d %s, want 404", lines.Code, lines.Body.String())
	}
	if n := cxCorrectionRows(t, ctx, f.jobY); n != 0 {
		t.Errorf("correction rows on Y's job = %d, want 0", n)
	}
	if got := cxTotal(t, ctx, f.invY); got != cxTotalBefore {
		t.Errorf("Y's invoice total = %s, want unchanged %s", got, cxTotalBefore)
	}
	if after := lixLineRows(t, ctx, f.invY); len(after) != len(linesBefore) {
		t.Errorf("Y's invoice lines = %v, want unchanged %v", after, linesBefore)
	}
	if own := cxServe(t, f.limited, f.jobX, "total", cxBody(cxTotalAfter), cxApplier(true, nil), cxAuditor(nil)); own.Code != http.StatusCreated {
		t.Errorf("control: correction on X's job = %d %s, want 201", own.Code, own.Body.String())
	}
}

func TestClientScope_ExtractionAdminReadsEveryDocument(t *testing.T) {
	f := newCSEFixture(t)
	for _, c := range []struct{ name, doc, job string }{{"X", f.docX, f.jobX}, {"Y", f.docY, f.jobY}, {"Z", f.docZ, f.jobZ}} {
		if got, err := f.reader.JobsForDocument(f.admin, c.doc); err != nil || len(got.Jobs) != 1 {
			t.Errorf("admin JobsForDocument(%s) = %d job(s), err %v, want 1", c.name, len(got.Jobs), err)
		}
		if d, err := f.reader.Detail(f.admin, c.job); err != nil || len(d.Fields) != 1 || len(d.Pages) != 1 {
			t.Errorf("admin Detail(%s) = %d field(s), %d page(s), err %v, want 1, 1, nil", c.name, len(d.Fields), len(d.Pages), err)
		}
		if _, err := f.reader.PageImageKey(f.admin, c.job, 1); err != nil {
			t.Errorf("admin PageImageKey(%s): %v", c.name, err)
		}
	}
}
