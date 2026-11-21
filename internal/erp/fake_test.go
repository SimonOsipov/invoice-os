package erp

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sort"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/portfolio"
)

var (
	_   Connector = (*Fake)(nil)
	ctx           = context.Background()
	cr            = Credentials{}
)

func TestFake_ImplementsConnector(t *testing.T) {
	if got := NewFake().System(); got != "fake" {
		t.Fatalf("System = %q", got)
	}
}

func TestFake_ListChangedPagesThroughEachCompanyOnce(t *testing.T) {
	f := NewFake()
	totals := map[int]bool{}
	for scope, co := range fakeSeed {
		var got []string
		var cursor []byte
		for {
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
		if len(got) <= 2 || totals[len(got)] {
			t.Errorf("%s total %d is not distinct and above one page", scope, len(got))
		}
		totals[len(got)] = true
	}
}

func TestFake_ListChangedFinalCursorReturnsNothingNew(t *testing.T) {
	f := NewFake()
	var cursor []byte
	for {
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

func offset(n uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, n)
	return b
}

func TestFake_ListChangedRefusesABadCursor(t *testing.T) {
	f := NewFake()
	for name, c := range map[string][]byte{"short": {1, 2, 3}, "past the total": offset(4)} {
		if _, err := f.ListChanged(ctx, cr, "fake-co-1", c); !errors.Is(err, ErrBadCursor) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := f.ListChanged(ctx, cr, "fake-co-1", offset(2)); err != nil {
		t.Errorf("offset 2 on fake-co-1: %v", err)
	}
}

func TestFake_ListChangedUnknownScope(t *testing.T) {
	if _, err := NewFake().ListChanged(ctx, cr, "fake-co-9", nil); !errors.Is(err, ErrUnknownScope) {
		t.Fatal(err)
	}
}

func TestFake_FetchReturnsTheSeededInvoice(t *testing.T) {
	seed := fakeSeed["fake-co-2"].invoices[1]
	d, err := NewFake().Fetch(ctx, cr, DocRef{SourceDocID: seed.id, Scope: "fake-co-2"})
	if err != nil {
		t.Fatal(err)
	}
	var body struct{ Number string }
	if err := json.Unmarshal(d.Body, &body); err != nil || body.Number != seed.number {
		t.Fatalf("body %s: %v", d.Body, err)
	}
	if d.ContentType != "application/json" || d.Ref.SourceRevision != "1" {
		t.Errorf("doc = %+v", d)
	}
}

func TestFake_FetchOutsideItsScopeIsNotFound(t *testing.T) {
	f := NewFake()
	id := fakeSeed["fake-co-1"].invoices[0].id
	if _, err := f.Fetch(ctx, cr, DocRef{SourceDocID: id, Scope: "fake-co-2"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign scope: %v", err)
	}
	if _, err := f.Fetch(ctx, cr, DocRef{SourceDocID: "nope", Scope: "fake-co-1"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
}

func TestFake_FetchPartyReturnsTheSeededCustomer(t *testing.T) {
	f := NewFake()
	for scope, co := range fakeSeed {
		for _, c := range co.customers {
			p, err := f.FetchParty(ctx, cr, scope, c.id)
			if err != nil {
				t.Fatal(err)
			}
			var body struct{ ID, TIN string }
			if err := json.Unmarshal(p.Body, &body); err != nil || body.ID != c.id || body.TIN != c.tin {
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

func TestFake_AuthorizeSeededKeyReturnsAnAPIKeyGrant(t *testing.T) {
	r, err := NewFake().Authorize(ctx, AuthorizeRequest{ExternalKey: "fake-co-3"})
	if err != nil || r.ExternalKey != "fake-co-3" || r.Grant == nil ||
		r.Grant.Kind != "api_key" || len(r.Grant.Secret) != 0 || r.Grant.ExpiresAt != nil {
		t.Fatalf("result = %+v, %v", r, err)
	}
}

func TestFake_AuthorizeUnknownOrEmptyKeyRefused(t *testing.T) {
	for _, k := range []string{"", "fake-co-9"} {
		r, err := NewFake().Authorize(ctx, AuthorizeRequest{ExternalKey: k})
		if !errors.Is(err, ErrUnknownScope) || r.Grant != nil || r.ExternalKey != "" {
			t.Errorf("%q: %+v, %v", k, r, err)
		}
	}
}

func TestFake_ValidateKnownScopeOnly(t *testing.T) {
	f := NewFake()
	if err := f.Validate(ctx, cr, "fake-co-1"); err != nil {
		t.Error(err)
	}
	if err := f.Validate(ctx, cr, "fake-co-9"); !errors.Is(err, ErrUnknownScope) {
		t.Error(err)
	}
}

func TestFake_UnsupportedOperations(t *testing.T) {
	f := NewFake()
	if _, err := f.WriteBack(ctx, cr, DocRef{}, WriteBack{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("WriteBack: %v", err)
	}
	if _, err := f.VerifyWebhook(ctx, nil, nil); !errors.Is(err, ErrUnsupported) {
		t.Errorf("VerifyWebhook: %v", err)
	}
	if err := f.EnsureSubscriptions(ctx, cr, "fake-co-1"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("EnsureSubscriptions: %v", err)
	}
	if f.Capabilities() != (Capabilities{}) {
		t.Error("capabilities not zero")
	}
}
