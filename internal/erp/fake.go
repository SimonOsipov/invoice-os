package erp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/erp/erpweb"
)

const (
	fakeSystem   = "fake"
	fakePageSize = 2
	fakeRevision = "1"
	jsonType     = "application/json"
)

// Fake is an in-memory connector over a fixed seed. It makes no network call.
type Fake struct {
	redirect *url.URL
	mu       sync.Mutex
	writes   map[string]WriteBack // by scope and document id
}

// NewFake serves its consent page on the origin of redirect, the gateway's callback URL.
func NewFake(redirect *url.URL) *Fake {
	return &Fake{redirect: redirect, writes: map[string]WriteBack{}}
}

func (*Fake) System() string { return fakeSystem }
func (*Fake) Capabilities() Capabilities {
	return Capabilities{Webhooks: true, Note: true, CustomFieldSlots: 2}
}

// Authorize builds the fake consent page's URL.
func (f *Fake) Authorize(_ context.Context, req AuthorizeRequest) (AuthorizeResult, error) {
	if f.redirect == nil || req.State == "" {
		return AuthorizeResult{}, errors.New("erp: fake authorize needs a redirect and a state")
	}
	u := url.URL{Scheme: f.redirect.Scheme, Host: f.redirect.Host, Path: "/erp/fake/consent", RawQuery: url.Values{"state": {req.State}}.Encode()}
	return AuthorizeResult{ConsentURL: u.String()}, nil
}

// fakeToken is the grant's opaque secret: random tokens, and whether the vendor ends the grant at refresh.
type fakeToken struct {
	Access       string `json:"access"`
	Refresh      string `json:"refresh"`
	RefreshFails bool   `json:"refresh_fails,omitempty"`
}

const (
	fakeExpired       = erpweb.FakeExpired
	fakeRefreshFails  = erpweb.FakeRefreshFails
	fakeTokenLifetime = time.Hour
)

// fakeCodes are the nine consent codes; erpweb owns the list so the gateway page shows the same.
func fakeCodes() []string { return erpweb.FakeCodes() }

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func newFakeGrant(refreshFails, expired bool) (GrantMaterial, error) {
	secret, err := json.Marshal(fakeToken{Access: randomToken(), Refresh: randomToken(), RefreshFails: refreshFails})
	if err != nil {
		return GrantMaterial{}, err
	}
	exp := time.Now().Add(fakeTokenLifetime)
	if expired {
		exp = time.Now().Add(-time.Minute)
	}
	return GrantMaterial{Kind: GrantOAuthRefresh, Secret: secret, ExpiresAt: &exp}, nil
}

// Exchange accepts exactly the codes of fakeCodes.
func (*Fake) Exchange(_ context.Context, req ExchangeRequest) (ExchangeResult, error) {
	if !slices.Contains(fakeCodes(), req.Code) {
		return ExchangeResult{}, errors.New("erp: unknown fake code")
	}
	key, suffix, _ := strings.Cut(req.Code, "~")
	g, err := newFakeGrant(suffix == fakeRefreshFails, suffix != "")
	if err != nil {
		return ExchangeResult{}, err
	}
	return ExchangeResult{ExternalKey: key, Grant: g}, nil
}

// Refresh rotates both tokens and gives a normal hour; a ~refresh_fails grant is revoked.
func (*Fake) Refresh(_ context.Context, c Credentials) (GrantMaterial, error) {
	var t fakeToken
	if err := json.Unmarshal(c.Secret, &t); err != nil || t.Access == "" || t.Refresh == "" {
		return GrantMaterial{}, errors.New("erp: fake grant secret unreadable")
	}
	if t.RefreshFails {
		return GrantMaterial{}, ErrGrantRevoked
	}
	return newFakeGrant(false, false)
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

// WriteBack stores the write under its company and document and returns ref unchanged.
func (f *Fake) WriteBack(_ context.Context, _ Credentials, ref DocRef, wb WriteBack) (DocRef, error) {
	co, ok := fakeSeed[ref.Scope]
	if !ok {
		return DocRef{}, ErrUnknownScope
	}
	if !slices.ContainsFunc(co.invoices, func(i seedInvoice) bool { return i.id == ref.SourceDocID }) {
		return DocRef{}, ErrNotFound
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes[ref.Scope+"\x00"+ref.SourceDocID] = wb
	return ref, nil
}

// LastWriteBack is the latest write to the company's document, for tests and the deployed proof.
func (f *Fake) LastWriteBack(scope, docID string) (WriteBack, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	wb, ok := f.writes[scope+"\x00"+docID]
	return wb, ok
}

// EnsureSubscriptions has nothing to register; it only refuses an unseeded company.
func (*Fake) EnsureSubscriptions(_ context.Context, _ Credentials, scope string) error {
	if _, ok := fakeSeed[scope]; !ok {
		return ErrUnknownScope
	}
	return nil
}
