package erp

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
)

const (
	fakeSystem   = "fake"
	fakePageSize = 2
	fakeRevision = "1"
	jsonType     = "application/json"
)

// Fake is an in-memory connector over a fixed seed. It makes no network call.
type Fake struct{}

func NewFake() *Fake { return &Fake{} }

func (*Fake) System() string             { return fakeSystem }
func (*Fake) Capabilities() Capabilities { return Capabilities{} }

func (*Fake) Authorize(_ context.Context, req AuthorizeRequest) (AuthorizeResult, error) {
	if _, ok := fakeSeed[req.ExternalKey]; !ok {
		return AuthorizeResult{}, ErrUnknownScope
	}
	return AuthorizeResult{ExternalKey: req.ExternalKey, Grant: &GrantMaterial{Kind: GrantAPIKey}}, nil
}

func (*Fake) Validate(_ context.Context, _ Credentials, scope string) error {
	if _, ok := fakeSeed[scope]; !ok {
		return ErrUnknownScope
	}
	return nil
}

// ListChanged pages the company's invoices in seed order. The cursor is a 4-byte offset plus
// its scope, so another company's cursor is refused; the final cursor returns nothing and itself.
func (*Fake) ListChanged(_ context.Context, _ Credentials, scope string, cursor []byte) (Changes, error) {
	co, ok := fakeSeed[scope]
	if !ok {
		return Changes{}, ErrUnknownScope
	}
	var off int
	if len(cursor) != 0 {
		if len(cursor) < 4 || string(cursor[4:]) != scope {
			return Changes{}, ErrBadCursor
		}
		off = int(binary.BigEndian.Uint32(cursor))
		if off > len(co.invoices) {
			return Changes{}, ErrBadCursor
		}
	}
	end := min(off+fakePageSize, len(co.invoices))
	refs := make([]DocRef, 0, end-off)
	for _, inv := range co.invoices[off:end] {
		refs = append(refs, DocRef{SourceDocID: inv.id, SourceRevision: fakeRevision, Scope: scope, Deleted: inv.voided})
	}
	next := binary.BigEndian.AppendUint32(nil, uint32(end))
	next = append(next, scope...)
	return Changes{Refs: refs, Cursor: next, More: end < len(co.invoices)}, nil
}

type fakeLine struct {
	Description string `json:"description"`
	Quantity    string `json:"quantity"`
	UnitPrice   string `json:"unit_price"`
	VATRate     string `json:"vat_rate"`
}

func (*Fake) Fetch(_ context.Context, _ Credentials, ref DocRef) (Document, error) {
	for _, inv := range fakeSeed[ref.Scope].invoices {
		if inv.id != ref.SourceDocID {
			continue
		}
		body, err := json.Marshal(map[string]any{
			"number":      inv.number,
			"issue_date":  inv.date,
			"currency":    "NGN",
			"customer_id": inv.customerID,
			"lines":       []fakeLine{{"Consulting services", "1", "100000.00", "7.5"}},
		})
		if err != nil {
			return Document{}, err
		}
		return Document{
			Ref:         DocRef{SourceDocID: inv.id, SourceRevision: fakeRevision, Scope: ref.Scope},
			ContentType: jsonType,
			Body:        body,
		}, nil
	}
	return Document{}, ErrNotFound
}

func (*Fake) FetchParty(_ context.Context, _ Credentials, scope, partyID string) (Party, error) {
	for _, c := range fakeSeed[scope].customers {
		if c.id != partyID {
			continue
		}
		body, err := json.Marshal(map[string]string{"id": c.id, "name": c.name, "tin": c.tin, "email": c.email})
		if err != nil {
			return Party{}, err
		}
		return Party{ID: c.id, ContentType: jsonType, Body: body}, nil
	}
	return Party{}, ErrNotFound
}

func (*Fake) WriteBack(context.Context, Credentials, DocRef, WriteBack) (DocRef, error) {
	return DocRef{}, ErrUnsupported
}

func (*Fake) VerifyWebhook(context.Context, http.Header, []byte) ([]Hint, error) {
	return nil, ErrUnsupported
}

func (*Fake) EnsureSubscriptions(context.Context, Credentials, string) error { return ErrUnsupported }
