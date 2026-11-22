package erp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
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
type Fake struct{ redirect *url.URL }

// NewFake serves its consent page on the origin of redirect, the gateway's callback URL.
func NewFake(redirect *url.URL) *Fake { return &Fake{redirect: redirect} }

func (*Fake) System() string             { return fakeSystem }
func (*Fake) Capabilities() Capabilities { return Capabilities{} }

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

// fakeLine is the four fields a test varies; fakeInvoiceBody adds the rest.
type fakeLine struct {
	Description string `json:"description"`
	Quantity    string `json:"quantity"`
	UnitPrice   string `json:"unit_price"`
	VATRate     string `json:"vat_rate"`
}

// fakeBodyLine is one line of the fake's body: the varying fields, the computed amounts and the static codes.
type fakeBodyLine struct {
	fakeLine
	LineTotal       string `json:"line_total"`
	LineTax         string `json:"line_tax"`
	TaxCode         string `json:"tax_code"`
	ItemID          string `json:"item_id"`
	TaxCategory     string `json:"tax_category"`
	HSNCode         string `json:"hsn_code"`
	ISICCode        string `json:"isic_code"`
	PriceUnit       string `json:"price_unit"`
	ProductCategory string `json:"product_category"`
	ServiceCategory string `json:"service_category"`
	BaseQuantity    string `json:"base_quantity"`
}

// fakeInvoiceBody builds the fake's invoice body. Each line total and tax is rounded before
// it is summed, so the header amounts always equal the sum of the stored lines.
func fakeInvoiceBody(inv seedInvoice, lines []fakeLine) ([]byte, error) {
	date, err := time.Parse("2006-01-02", inv.date)
	if err != nil {
		return nil, err
	}
	hundred := big.NewRat(100, 1)
	subtotal, vat := new(big.Rat), new(big.Rat)
	body := make([]fakeBodyLine, 0, len(lines))
	for _, l := range lines {
		qty, ok1 := parseDec(l.Quantity)
		price, ok2 := parseDec(l.UnitPrice)
		rate, ok3 := parseDec(l.VATRate)
		if !ok1 || !ok2 || !ok3 {
			return nil, errors.New("erp: fake line has an unparseable amount")
		}
		total := round2(new(big.Rat).Mul(qty, price))
		tax := round2(new(big.Rat).Quo(new(big.Rat).Mul(total, rate), hundred))
		subtotal.Add(subtotal, total)
		vat.Add(vat, tax)
		body = append(body, fakeBodyLine{
			fakeLine: l, LineTotal: total.FloatString(2), LineTax: tax.FloatString(2),
			TaxCode: "VAT-STD", ItemID: "fake-item-consulting", TaxCategory: "STANDARD_GST",
			HSNCode: "0101.21", ISICCode: "0111", PriceUnit: "10",
			ProductCategory: "Services", ServiceCategory: "Consulting", BaseQuantity: "1",
		})
	}
	return json.Marshal(map[string]any{
		"number": inv.number, "issue_date": inv.date, "currency": "NGN", "customer_id": inv.customerID,
		"invoice_kind": "B2B", "tax_currency_code": "NGN", "due_date": date.AddDate(0, 0, 30).Format("2006-01-02"),
		"issue_time": "09:00:00", "tax_point_date": inv.date, "payment_status": "PENDING",
		"subtotal": subtotal.FloatString(2), "vat": vat.FloatString(2), "total": new(big.Rat).Add(subtotal, vat).FloatString(2),
		"lines": body,
	})
}

func (*Fake) Fetch(_ context.Context, _ Credentials, ref DocRef) (Document, error) {
	for _, inv := range fakeSeed[ref.Scope].invoices {
		if inv.id != ref.SourceDocID {
			continue
		}
		body, err := fakeInvoiceBody(inv, []fakeLine{{"Consulting services", "1", "100000.00", "7.5"}})
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

// DefaultMapping covers every target of Targets().
func (*Fake) DefaultMapping() Mapping {
	m := Mapping{
		"invoice_number": "number", "issue_date": "issue_date", "currency": "currency",
		"subtotal": "subtotal", "vat": "vat", "total": "total",
		"invoice_kind": "invoice_kind", "tax_currency_code": "tax_currency_code", "due_date": "due_date",
		"issue_time": "issue_time", "tax_point_date": "tax_point_date", "payment_status": "payment_status",
		"buyer_tin": "@party.tin", "buyer_name": "@party.name",
		TargetCustomerID: "customer_id", TargetTaxCode: "lines[].tax_code", TargetItemID: "lines[].item_id",
		"line_description": "lines[].description", "line_quantity": "lines[].quantity", "line_unit_price": "lines[].unit_price",
		"line_total": "lines[].line_total", "line_tax": "lines[].line_tax", "line_tax_category": "lines[].tax_category",
		"line_hsn_code": "lines[].hsn_code", "line_isic_code": "lines[].isic_code",
		"line_product_category": "lines[].product_category", "line_service_category": "lines[].service_category",
		"line_sellers_item_identification": "lines[].item_id", "line_price_unit": "lines[].price_unit",
		"line_tax_percent": "lines[].vat_rate", "line_base_quantity": "lines[].base_quantity",
	}
	for _, k := range []string{"email", "telephone", "street", "city", "postal_zone", "country", "state", "lga"} {
		m["buyer_"+k] = "@party." + k
	}
	return m
}

func (*Fake) FetchParty(_ context.Context, _ Credentials, scope, partyID string) (Party, error) {
	for _, c := range fakeSeed[scope].customers {
		if c.id != partyID {
			continue
		}
		body, err := json.Marshal(map[string]string{
			"id": c.id, "name": c.name, "tin": c.tin, "email": c.email,
			"telephone": "+2348012345678", "street": "12 Factory Road", "city": "Aba", "postal_zone": "450001",
			"country": "NG", "state": fakeState, "lga": fakeLGA,
		})
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
