package erp

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/portfolio"
)

var testRedirect = &url.URL{Scheme: "https", Host: "gw.example", Path: "/erp/callback"}

var (
	_   Connector = (*Fake)(nil)
	ctx           = context.Background()
	cr            = Credentials{}
)

func TestFake_RevokeIsANoOp(t *testing.T) {
	if err := NewFake(testRedirect).Revoke(context.Background(), Credentials{Kind: GrantOAuthRefresh, Secret: []byte("x")}); err != nil {
		t.Errorf("Revoke = %v, want nil", err)
	}
}

func TestFake_ImplementsConnector(t *testing.T) {
	if got := NewFake(testRedirect).System(); got != "fake" {
		t.Fatalf("System = %q", got)
	}
}

func TestFake_ListChangedPagesThroughEachCompanyOnce(t *testing.T) {
	f := testFake(t)
	wantCounts := map[string]int{"fake-co-1": 3, "fake-co-2": 4, "fake-co-3": 6}
	if len(fakeSeed) != len(wantCounts) {
		t.Fatalf("seed has %d companies, want %d", len(fakeSeed), len(wantCounts))
	}
	totals := map[int]bool{}
	for scope, co := range fakeSeed {
		var got []string
		var cursor []byte
		for pages := 0; ; pages++ {
			if pages > 10 {
				t.Fatalf("%s: the cursor never ends", scope)
			}
			ch, err := f.ListChanged(ctx, cr, scope, cursor)
			if err != nil {
				t.Fatal(err)
			}
			if len(ch.Refs) > 2 {
				t.Fatalf("%s page of %d", scope, len(ch.Refs))
			}
			for _, r := range ch.Refs {
				if r.Scope != scope {
					t.Errorf("ref scope %q in %s", r.Scope, scope)
				}
				got = append(got, r.SourceDocID)
			}
			cursor = ch.Cursor
			if !ch.More {
				break
			}
		}
		var want []string
		for _, inv := range co.invoices {
			want = append(want, inv.id)
		}
		sort.Strings(got)
		sort.Strings(want)
		if len(got) != len(want) {
			t.Fatalf("%s got %v want %v", scope, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s got %v want %v", scope, got, want)
			}
		}
		if len(got) != wantCounts[scope] {
			t.Errorf("%s lists %d refs, want %d", scope, len(got), wantCounts[scope])
		}
		if len(got) <= 2 || totals[len(got)] {
			t.Errorf("%s total %d is not distinct and above one page", scope, len(got))
		}
		totals[len(got)] = true
	}
}

func TestFake_ListChangedFlagsTheVoidedSeedDocument(t *testing.T) {
	f := testFake(t)
	for _, scope := range []string{"fake-co-1", "fake-co-2", "fake-co-3"} {
		var refs []DocRef
		var cursor []byte
		for pages := 0; ; pages++ {
			if pages > 10 {
				t.Fatalf("%s: the cursor never ends", scope)
			}
			ch, err := f.ListChanged(ctx, cr, scope, cursor)
			if err != nil {
				t.Fatal(err)
			}
			refs = append(refs, ch.Refs...)
			cursor = ch.Cursor
			if !ch.More {
				break
			}
		}
		if scope == "fake-co-3" && len(refs) != 6 {
			t.Fatalf("fake-co-3 lists %d refs, want 6", len(refs))
		}
		for _, r := range refs {
			if want := r.SourceDocID == "fake-co-3-inv-6"; r.Deleted != want {
				t.Errorf("%s Deleted = %v, want %v", r.SourceDocID, r.Deleted, want)
			}
		}
		if scope == "fake-co-3" && !slices.ContainsFunc(refs, func(r DocRef) bool { return r.SourceDocID == "fake-co-3-inv-6" && r.Deleted }) {
			t.Error("fake-co-3-inv-6 is not listed as deleted")
		}
	}
}

func TestFake_FetchStillServesTheVoidedDocument(t *testing.T) {
	d, err := testFake(t).Fetch(ctx, cr, DocRef{SourceDocID: "fake-co-3-inv-6", Scope: "fake-co-3"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	var body struct {
		Number     string
		IssueDate  string `json:"issue_date"`
		CustomerID string `json:"customer_id"`
	}
	if err := json.Unmarshal(d.Body, &body); err != nil || body.Number != "FK3-0006" || body.IssueDate != "2026-04-06" || body.CustomerID != "fake-co-3-cust-1" {
		t.Errorf("body %s: %v", d.Body, err)
	}
}

func TestFake_ListChangedFinalCursorReturnsNothingNew(t *testing.T) {
	f := NewFake(testRedirect)
	var cursor []byte
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("the cursor never ends")
		}
		ch, err := f.ListChanged(ctx, cr, "fake-co-2", cursor)
		if err != nil {
			t.Fatal(err)
		}
		cursor = ch.Cursor
		if !ch.More {
			break
		}
	}
	ch, err := f.ListChanged(ctx, cr, "fake-co-2", cursor)
	if err != nil || len(ch.Refs) != 0 || ch.More || !bytes.Equal(ch.Cursor, cursor) {
		t.Fatalf("final = %+v, %v", ch, err)
	}
}

func offset(n uint32, scope string) []byte {
	return append(binary.BigEndian.AppendUint32(nil, n), scope...)
}

func TestFake_ListChangedRefusesABadCursor(t *testing.T) {
	f := NewFake(testRedirect)
	for name, c := range map[string][]byte{
		"short":             {1, 2, 3},
		"past the total":    offset(4, "fake-co-1"),
		"no scope":          offset(2, ""),
		"foreign, in range": offset(2, "fake-co-3"),
	} {
		if _, err := f.ListChanged(ctx, cr, "fake-co-1", c); !errors.Is(err, ErrBadCursor) {
			t.Errorf("%s: %v", name, err)
		}
	}
	foreign, err := f.ListChanged(ctx, cr, "fake-co-3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ListChanged(ctx, cr, "fake-co-1", foreign.Cursor); !errors.Is(err, ErrBadCursor) {
		t.Errorf("issued foreign cursor: %v", err)
	}
	if _, err := f.ListChanged(ctx, cr, "fake-co-1", offset(2, "fake-co-1")); err != nil {
		t.Errorf("offset 2 on fake-co-1: %v", err)
	}
}

func TestFake_ListChangedUnknownScope(t *testing.T) {
	if _, err := NewFake(testRedirect).ListChanged(ctx, cr, "fake-co-9", nil); !errors.Is(err, ErrUnknownScope) {
		t.Fatal(err)
	}
}

func TestFake_FetchReturnsTheSeededInvoice(t *testing.T) {
	seed := fakeSeed["fake-co-2"].invoices[1]
	d, err := NewFake(testRedirect).Fetch(ctx, cr, DocRef{SourceDocID: seed.id, Scope: "fake-co-2"})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Number     string
		IssueDate  string `json:"issue_date"`
		Currency   string
		CustomerID string `json:"customer_id"`
		Lines      []struct{ Description string }
	}
	if err := json.Unmarshal(d.Body, &body); err != nil || body.Number != seed.number {
		t.Fatalf("body %s: %v", d.Body, err)
	}
	if body.IssueDate != seed.date || body.Currency != "NGN" || body.CustomerID != seed.customerID || len(body.Lines) == 0 {
		t.Errorf("body %s, want date %s, NGN, customer %s and a line", d.Body, seed.date, seed.customerID)
	}
	if d.ContentType != "application/json" || d.Ref.SourceRevision != "1" {
		t.Errorf("doc = %+v", d)
	}
}

func TestFake_FetchOutsideItsScopeIsNotFound(t *testing.T) {
	f := NewFake(testRedirect)
	id := fakeSeed["fake-co-1"].invoices[0].id
	if _, err := f.Fetch(ctx, cr, DocRef{SourceDocID: id, Scope: "fake-co-2"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign scope: %v", err)
	}
	if _, err := f.Fetch(ctx, cr, DocRef{SourceDocID: "nope", Scope: "fake-co-1"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
}

func TestFake_FetchPartyReturnsTheSeededCustomer(t *testing.T) {
	f := NewFake(testRedirect)
	for scope, co := range fakeSeed {
		for _, c := range co.customers {
			p, err := f.FetchParty(ctx, cr, scope, c.id)
			if err != nil {
				t.Fatal(err)
			}
			var body struct{ ID, Name, TIN, Email string }
			if err := json.Unmarshal(p.Body, &body); err != nil || body.ID != c.id || body.TIN != c.tin || body.Name != c.name || body.Email != c.email {
				t.Errorf("%s body %s: %v", c.id, p.Body, err)
			}
		}
	}
	if _, err := f.FetchParty(ctx, cr, "fake-co-1", fakeSeed["fake-co-2"].customers[0].id); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign customer: %v", err)
	}
}

func TestFake_SeededCustomerTINsAreValid(t *testing.T) {
	for _, co := range fakeSeed {
		for _, c := range co.customers {
			if _, err := portfolio.ValidateTIN(c.tin); err != nil {
				t.Errorf("%s: %v", c.id, err)
			}
		}
	}
}

func TestFake_AuthorizeBuildsTheConsentURL(t *testing.T) {
	state := "a b&c=d/é"
	r, err := NewFake(testRedirect).Authorize(ctx, AuthorizeRequest{State: state})
	want := "https://gw.example/erp/fake/consent?state=" + url.QueryEscape(state)
	if err != nil || r.ConsentURL != want {
		t.Fatalf("result = %+v, %v; want %s", r, err, want)
	}
	if _, err := NewFake(nil).Authorize(ctx, AuthorizeRequest{State: "s"}); err == nil {
		t.Error("no redirect accepted")
	}
	if _, err := NewFake(testRedirect).Authorize(ctx, AuthorizeRequest{}); err == nil {
		t.Error("empty state accepted")
	}
}

func TestFake_ExchangeIgnoresTheRealm(t *testing.T) {
	f := NewFake(testRedirect)
	a, errA := f.Exchange(ctx, ExchangeRequest{Code: "fake-co-1"})
	b, errB := f.Exchange(ctx, ExchangeRequest{Code: "fake-co-1", RealmID: "x"})
	if errA != nil || errB != nil || a.ExternalKey != b.ExternalKey || a.Grant.Kind != b.Grant.Kind {
		t.Errorf("results differ with the realm: %+v %v / %+v %v", a, errA, b, errB)
	}
}

func TestFake_ExchangeCodes(t *testing.T) {
	f := NewFake(testRedirect)
	codes := fakeCodes()
	if len(codes) != 9 {
		t.Fatalf("%d codes, want 9", len(codes))
	}
	for _, code := range codes {
		key, suffix, _ := strings.Cut(code, "~")
		r, err := f.Exchange(ctx, ExchangeRequest{Code: code})
		if err != nil || r.ExternalKey != key || r.Grant.Kind != GrantOAuthRefresh || len(r.Grant.Secret) == 0 || r.Grant.ExpiresAt == nil {
			t.Errorf("%s: %+v, %v", code, r, err)
			continue
		}
		past := r.Grant.ExpiresAt.Before(time.Now())
		if past != (suffix != "") {
			t.Errorf("%s: expiry %v, past = %v", code, r.Grant.ExpiresAt, past)
		}
		if !past && time.Until(*r.Grant.ExpiresAt) < 59*time.Minute {
			t.Errorf("%s: expiry %v is not about an hour away", code, r.Grant.ExpiresAt)
		}
	}
	for _, bad := range []string{"fake-co-9", "", "fake-co-1~x", "fake-co-1~", "~expired", "FAKE-CO-1"} {
		if _, err := f.Exchange(ctx, ExchangeRequest{Code: bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	one, _ := f.Exchange(ctx, ExchangeRequest{Code: "fake-co-1"})
	two, _ := f.Exchange(ctx, ExchangeRequest{Code: "fake-co-1"})
	if bytes.Equal(one.Grant.Secret, two.Grant.Secret) {
		t.Error("two exchanges returned the same secret")
	}
}

func TestFake_RefreshRotatesAndCanBeRevoked(t *testing.T) {
	f := NewFake(testRedirect)
	ex, _ := f.Exchange(ctx, ExchangeRequest{Code: "fake-co-1~expired"})
	g, err := f.Refresh(ctx, Credentials{Kind: ex.Grant.Kind, Secret: ex.Grant.Secret})
	if err != nil || bytes.Equal(g.Secret, ex.Grant.Secret) || g.Kind != GrantOAuthRefresh || g.ExpiresAt == nil ||
		time.Until(*g.ExpiresAt) < 59*time.Minute {
		t.Fatalf("refresh = %+v, %v", g, err)
	}
	var before, after fakeToken
	_ = json.Unmarshal(ex.Grant.Secret, &before)
	_ = json.Unmarshal(g.Secret, &after)
	if before.Access == after.Access || before.Refresh == after.Refresh {
		t.Error("a token was not rotated")
	}
	bad, _ := f.Exchange(ctx, ExchangeRequest{Code: "fake-co-2~refresh_fails"})
	for range 2 {
		if _, err := f.Refresh(ctx, Credentials{Secret: bad.Grant.Secret}); !errors.Is(err, ErrGrantRevoked) {
			t.Errorf("revoked refresh: %v", err)
		}
	}
	for _, s := range [][]byte{[]byte("garbage"), nil, []byte("{}")} {
		if _, err := f.Refresh(ctx, Credentials{Secret: s}); err == nil || errors.Is(err, ErrGrantRevoked) {
			t.Errorf("%q: %v", s, err)
		}
	}
}

func TestFake_ValidateKnownScopeOnly(t *testing.T) {
	f := NewFake(testRedirect)
	if err := f.Validate(ctx, cr, "fake-co-1"); err != nil {
		t.Error(err)
	}
	if err := f.Validate(ctx, cr, "fake-co-9"); !errors.Is(err, ErrUnknownScope) {
		t.Error(err)
	}
}

func TestFake_WriteBackStoresPerDocument(t *testing.T) {
	f := NewFake(testRedirect)
	r1 := DocRef{SourceDocID: "fake-co-1-inv-1", SourceRevision: "1", Scope: "fake-co-1"}
	r2 := DocRef{SourceDocID: "fake-co-2-inv-1", SourceRevision: "1", Scope: "fake-co-2"}
	if _, ok := f.LastWriteBack("fake-co-1", r1.SourceDocID); ok {
		t.Fatal("an unwritten document reads as written")
	}
	for ref, irn := range map[*DocRef]string{&r1: "IRN-1", &r2: "IRN-2"} {
		got, err := f.WriteBack(ctx, cr, *ref, WriteBack{IRN: irn, Fields: []string{"accepted"}, Note: "n " + irn})
		if err != nil || got != *ref {
			t.Fatalf("WriteBack %s = %+v, %v; want the ref unchanged", irn, got, err)
		}
	}
	for _, c := range []struct {
		ref DocRef
		irn string
	}{{r1, "IRN-1"}, {r2, "IRN-2"}} {
		wb, ok := f.LastWriteBack(c.ref.Scope, c.ref.SourceDocID)
		if !ok || wb.IRN != c.irn || wb.Note != "n "+c.irn || len(wb.Fields) != 1 {
			t.Errorf("LastWriteBack(%s) = %+v, %v", c.ref.Scope, wb, ok)
		}
	}
	if _, ok := f.LastWriteBack("fake-co-1", "fake-co-1-inv-2"); ok {
		t.Error("another document of the company reads as written")
	}
}

func TestFake_WriteBackRefusesUnknownScopeAndDocument(t *testing.T) {
	f := NewFake(testRedirect)
	if _, err := f.WriteBack(ctx, cr, DocRef{SourceDocID: "fake-co-1-inv-1", Scope: "nope"}, WriteBack{IRN: "x"}); !errors.Is(err, ErrUnknownScope) {
		t.Errorf("unknown scope: %v", err)
	}
	if _, err := f.WriteBack(ctx, cr, DocRef{SourceDocID: "fake-co-2-inv-1", Scope: "fake-co-1"}, WriteBack{IRN: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("another company's document: %v", err)
	}
	if _, err := f.WriteBack(ctx, cr, DocRef{SourceDocID: "missing", Scope: "fake-co-1"}, WriteBack{IRN: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown document: %v", err)
	}
	for _, c := range [][2]string{{"fake-co-1", "fake-co-2-inv-1"}, {"fake-co-1", "missing"}, {"nope", "fake-co-1-inv-1"}} {
		if _, ok := f.LastWriteBack(c[0], c[1]); ok {
			t.Errorf("a refused write was stored under %v", c)
		}
	}
}

func TestFakeDefaultMapping_CoversEveryTargetAndResolves(t *testing.T) {
	f := testFake(t)
	m := f.DefaultMapping()
	if err := ValidateMapping(m); err != nil {
		t.Fatalf("ValidateMapping: %v", err)
	}
	doc, err := f.Fetch(ctx, cr, DocRef{SourceDocID: "fake-co-1-inv-1", Scope: "fake-co-1"})
	if err != nil {
		t.Fatal(err)
	}
	party, err := f.FetchParty(ctx, cr, "fake-co-1", "fake-co-1-cust-1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := decodeBody(doc.Body)
	pbody, _ := decodeBody(party.Body)
	if len(m) != len(Targets()) {
		t.Errorf("default has %d paths for %d targets", len(m), len(Targets()))
	}
	for _, tg := range Targets() {
		path, ok := m[tg.Key]
		if !ok {
			t.Errorf("no path for %s", tg.Key)
			continue
		}
		p, _ := parsePath(path)
		var v any
		switch {
		case p.party:
			v = walk(pbody, p.segs)
		case tg.Line:
			els, _ := walk(body, p.segs[:p.arrAt+1]).([]any)
			if len(els) == 0 {
				t.Errorf("%s: no line", tg.Key)
				continue
			}
			v = walk(els[0], p.segs[p.arrAt+1:])
		default:
			v = walk(body, p.segs)
		}
		if s, err := leaf(tg.Key, v); err != nil || s == nil {
			t.Errorf("%s (%s) resolves to nothing", tg.Key, path)
		}
	}
	if _, err := Apply(m, doc, &party); err != nil {
		t.Errorf("Apply: %v", err)
	}
}

func TestFakeInvoiceBody_RoundsEachLineHalfAwayFromZero(t *testing.T) {
	inv := seedInvoice{id: "d", number: "R-1", date: "2026-05-01", customerID: "c"}
	for _, c := range []struct {
		name      string
		lines     []fakeLine
		wantLines [][2]string // line total, line tax
		wantHdr   [3]string   // subtotal, vat, total
	}{
		{"ties round away from zero",
			[]fakeLine{{"a", "3", "33.335", "7.5"}, {"b", "1", "0.125", "0"}, {"c", "2", "10.00", "7.5"}, {"d", "1", "-0.125", "0"}},
			[][2]string{{"100.01", "7.50"}, {"0.13", "0.00"}, {"20.00", "1.50"}, {"-0.13", "0.00"}},
			[3]string{"120.01", "9.00", "129.01"}},
		// Two 0.004 lines sum to 0.00, not the unrounded 0.01; two 0.4% taxes on 1.00 sum to 0.00, not 0.01.
		{"the header sums the rounded lines",
			[]fakeLine{{"a", "1", "0.004", "0"}, {"b", "1", "0.004", "0"}, {"c", "1", "1.00", "0.4"}, {"d", "1", "1.00", "0.4"}},
			[][2]string{{"0.00", "0.00"}, {"0.00", "0.00"}, {"1.00", "0.00"}, {"1.00", "0.00"}},
			[3]string{"2.00", "0.00", "2.00"}},
		// 0.005 rounds to a 0.01 total, and 50% of that is 0.005, which rounds to 0.01; on the unrounded 0.005 it would be 0.00.
		{"the tax is taken on the rounded line total",
			[]fakeLine{{"e", "1", "0.005", "50"}},
			[][2]string{{"0.01", "0.01"}},
			[3]string{"0.01", "0.01", "0.02"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			body, err := fakeInvoiceBody(inv, c.lines)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Subtotal string `json:"subtotal"`
				VAT      string `json:"vat"`
				Total    string `json:"total"`
				Lines    []struct {
					LineTotal string `json:"line_total"`
					LineTax   string `json:"line_tax"`
				} `json:"lines"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Lines) != len(c.wantLines) || len(c.wantLines) == 0 {
				t.Fatalf("lines = %d, want %d", len(got.Lines), len(c.wantLines))
			}
			for i, w := range c.wantLines {
				if got.Lines[i].LineTotal != w[0] || got.Lines[i].LineTax != w[1] {
					t.Errorf("line %d = %s/%s, want %s/%s", i, got.Lines[i].LineTotal, got.Lines[i].LineTax, w[0], w[1])
				}
			}
			if h := [3]string{got.Subtotal, got.VAT, got.Total}; h != c.wantHdr {
				t.Errorf("header = %v, want %v", h, c.wantHdr)
			}
		})
	}
}

func TestFakeInvoiceBody_RefusesAnUnparseableAmount(t *testing.T) {
	inv := seedInvoice{id: "d", number: "R-1", date: "2026-05-01", customerID: "c"}
	if _, err := fakeInvoiceBody(inv, []fakeLine{{"a", "1,5", "1", "0"}}); err == nil {
		t.Fatal("want an error")
	}
}

func TestFake_FetchCarriesTheSeededAmounts(t *testing.T) {
	f := testFake(t)
	doc, err := f.Fetch(ctx, cr, DocRef{SourceDocID: "fake-co-1-inv-1", Scope: "fake-co-1"})
	if err != nil {
		t.Fatal(err)
	}
	party, err := f.FetchParty(ctx, cr, "fake-co-1", "fake-co-1-cust-1")
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := Apply(f.DefaultMapping(), doc, &party)
	if err != nil {
		t.Fatal(err)
	}
	in := mapped.In
	exact(t, "Subtotal", in.Subtotal, "100000.00")
	exact(t, "VAT", in.VAT, "7500.00")
	exact(t, "Total", in.Total, "107500.00")
	if len(in.LineItems) != 1 {
		t.Fatalf("lines = %d", len(in.LineItems))
	}
	exact(t, "LineTotal", in.LineItems[0].LineTotal, "100000.00")
	exact(t, "LineTax", in.LineItems[0].LineTax, "7500.00")
	exact(t, "BuyerTIN", in.BuyerTIN, "1234567897")
	if mapped.CustomerID != "fake-co-1-cust-1" || mapped.TaxCodes[0] == "" || mapped.Items[0] == "" {
		t.Errorf("lookup keys = %q %v %v", mapped.CustomerID, mapped.TaxCodes, mapped.Items)
	}
}
