package erp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/invoicefields"
)

// Mapping is target -> source path.
type Mapping map[string]string

const (
	TargetCustomerID = "erp_customer_id" // header; customer lookup key and FetchParty id
	TargetTaxCode    = "erp_tax_code"    // line; tax-code lookup key
	TargetItemID     = "erp_item_id"     // line; item lookup key
)

type Target struct {
	Key, Label string
	Line       bool
	Type       invoicefields.Type
}

var ErrInvalidMapping = errors.New("erp: invalid mapping")

// mappingError reads "<target>: <reason>" and matches ErrInvalidMapping.
type mappingError struct{ msg string }

func (e *mappingError) Error() string        { return e.msg }
func (e *mappingError) Is(target error) bool { return target == ErrInvalidMapping }

func invalidMapping(key, reason string) error { return &mappingError{key + ": " + reason} }

const (
	reasonUnknown   = "unknown field"
	reasonPath      = "source path is not valid"
	reasonHeaderArr = "a header field reads no []"
	reasonLineArr   = "a line field reads one []"
	reasonOneArray  = "line fields must read one array"
	reasonParty     = "@party is for header fields"
	reasonRequired  = "must be mapped"
)

const partySegment = "@party"

// Targets lists the import keys in ImportKeys order, then the three ERP keys.
func Targets() []Target {
	var out []Target
	for _, lead := range []bool{true, false} {
		for _, f := range invoicefields.All {
			if f.Import && f.Lead == lead {
				out = append(out, Target{Key: f.ImportKey(), Label: f.Label, Line: f.Line, Type: f.Type})
			}
		}
	}
	return append(out,
		Target{TargetCustomerID, "ERP customer id", false, invoicefields.Text},
		Target{TargetTaxCode, "ERP tax code", true, invoicefields.Text},
		Target{TargetItemID, "ERP item id", true, invoicefields.Text},
	)
}

func targetOf(key string) (Target, bool) {
	for _, t := range Targets() {
		if t.Key == key {
			return t, true
		}
	}
	return Target{}, false
}

type seg struct {
	name string
	arr  bool
}

type srcPath struct {
	party bool
	segs  []seg // after @party when party
	arrAt int   // index of the [] segment; -1 when none
	arrs  int
}

var segName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func parsePath(p string) (srcPath, bool) {
	out := srcPath{arrAt: -1}
	if len(p) > 256 {
		return out, false
	}
	parts := strings.Split(p, ".")
	if len(parts) > 16 {
		return out, false
	}
	for i, s := range parts {
		name, arr := strings.CutSuffix(s, "[]")
		if i == 0 && name == partySegment && !arr && len(parts) > 1 {
			out.party = true
			continue
		}
		if !segName.MatchString(name) {
			return out, false
		}
		if arr {
			out.arrs++
			if out.arrAt < 0 {
				out.arrAt = len(out.segs)
			}
		}
		out.segs = append(out.segs, seg{name, arr})
	}
	return out, true
}

// arrayKey names the array a line path iterates.
func (p srcPath) arrayKey() string {
	names := make([]string, 0, p.arrAt+1)
	for _, s := range p.segs[:p.arrAt+1] {
		names = append(names, s.name)
	}
	return strings.Join(names, ".")
}

// checkTarget parses a target's path and returns the reason it is refused, or "".
func checkTarget(t Target, path string) (srcPath, string) {
	p, ok := parsePath(path)
	switch {
	case !ok:
		return p, reasonPath
	case p.party && t.Line, p.party && t.Key == TargetCustomerID:
		return p, reasonParty
	case !t.Line && p.arrs != 0:
		return p, reasonHeaderArr
	case t.Line && p.arrs != 1:
		return p, reasonLineArr
	}
	return p, ""
}

// ValidateMapping checks m against the path grammar: one line array, @party on header fields only.
func ValidateMapping(m Mapping) error {
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if _, ok := targetOf(k); !ok {
			return invalidMapping(k, reasonUnknown)
		}
	}
	type lineRead struct{ key, array string }
	var reads []lineRead
	counts := map[string]int{}
	for _, t := range Targets() {
		path, ok := m[t.Key]
		if !ok {
			continue
		}
		p, reason := checkTarget(t, path)
		if reason != "" {
			return invalidMapping(t.Key, reason)
		}
		if t.Line {
			reads = append(reads, lineRead{t.Key, p.arrayKey()})
			counts[p.arrayKey()]++
		}
	}
	// The most-read array is the reference; a tie goes to the first target read.
	ref := ""
	for _, r := range reads {
		if ref == "" || counts[r.array] > counts[ref] {
			ref = r.array
		}
	}
	for _, r := range reads {
		if r.array != ref {
			return invalidMapping(r.key, reasonOneArray)
		}
	}
	if _, ok := m["invoice_number"]; !ok {
		return invalidMapping("invoice_number", reasonRequired)
	}
	return nil
}

// Merge returns m with each target of over replacing its entry; neither input changes.
func (m Mapping) Merge(over Mapping) Mapping {
	out := maps.Clone(m)
	if out == nil {
		out = Mapping{}
	}
	maps.Copy(out, over)
	return out
}

// Fingerprint is the SHA-256 hex of the mapping's JSON (map keys sort), so equal mappings match.
func Fingerprint(m Mapping) string {
	b, _ := json.Marshal(m) // a map of strings cannot fail to marshal
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Mapped is one document after the mapping, before lookups.
type Mapped struct {
	In         invoice.CreateInput
	CustomerID string   // "" when unmapped or empty
	TaxCodes   []string // per line
	Items      []string // per line
}

func decodeBody(b []byte) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	return v, true
}

// walk follows object keys; anything else on the way is absent.
func walk(v any, segs []seg) any {
	for _, s := range segs {
		o, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = o[s.name]
	}
	return v
}

// leaf reads a scalar: absent and null are nil, a string is trimmed, a number keeps its literal text.
func leaf(key string, v any) (*string, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		if s := strings.TrimSpace(x); s != "" {
			return &s, nil
		}
		return nil, nil
	case json.Number:
		s := x.String()
		return &s, nil
	}
	return nil, unmappable(key)
}

var decimalText = regexp.MustCompile(`^[+-]?([0-9]+(\.[0-9]*)?|\.[0-9]+)$`)

// typed checks s against the target's type; only a date leaves a parsed value.
func typed(t Target, s *string) (*time.Time, error) {
	if s == nil {
		return nil, nil
	}
	switch t.Type {
	case invoicefields.Date:
		d, err := time.Parse("2006-01-02", *s)
		if err != nil {
			return nil, unmappable(t.Key)
		}
		return &d, nil
	case invoicefields.Money, invoicefields.Quantity, invoicefields.Percent:
		if !decimalText.MatchString(*s) {
			return nil, unmappable(t.Key)
		}
	}
	return nil, nil
}

// readTarget resolves one header target on the body or the party.
func readTarget(t Target, p srcPath, body, party any) (*string, *time.Time, error) {
	root := body
	if p.party {
		root = party
	}
	s, err := leaf(t.Key, walk(root, p.segs))
	if err != nil {
		return nil, nil, err
	}
	d, err := typed(t, s)
	return s, d, err
}

// CustomerIDOf is the trimmed erp_customer_id value of doc, or "" when unmapped or empty.
func CustomerIDOf(m Mapping, doc Document) (string, error) {
	path, ok := m[TargetCustomerID]
	if !ok {
		return "", nil
	}
	t, _ := targetOf(TargetCustomerID)
	p, reason := checkTarget(t, path)
	if reason != "" {
		return "", invalidMapping(t.Key, reason)
	}
	body, ok := decodeBody(doc.Body)
	if !ok {
		return "", unmappable("body")
	}
	s, _, err := readTarget(t, p, body, nil)
	if err != nil || s == nil {
		return "", err
	}
	return *s, nil
}

type headSetter func(in *invoice.CreateInput, s *string, d *time.Time)

var headSetters = map[string]headSetter{
	"invoice_number":    func(in *invoice.CreateInput, s *string, _ *time.Time) { in.InvoiceNumber = *s },
	"issue_date":        func(in *invoice.CreateInput, _ *string, d *time.Time) { in.IssueDate = d },
	"buyer_tin":         func(in *invoice.CreateInput, s *string, _ *time.Time) { in.BuyerTIN = s },
	"buyer_name":        func(in *invoice.CreateInput, s *string, _ *time.Time) { in.BuyerName = s },
	"currency":          func(in *invoice.CreateInput, s *string, _ *time.Time) { in.Currency = s },
	"subtotal":          func(in *invoice.CreateInput, s *string, _ *time.Time) { in.Subtotal = s },
	"vat":               func(in *invoice.CreateInput, s *string, _ *time.Time) { in.VAT = s },
	"total":             func(in *invoice.CreateInput, s *string, _ *time.Time) { in.Total = s },
	"invoice_kind":      func(in *invoice.CreateInput, s *string, _ *time.Time) { in.InvoiceKind = s },
	"tax_currency_code": func(in *invoice.CreateInput, s *string, _ *time.Time) { in.TaxCurrencyCode = s },
	"due_date":          func(in *invoice.CreateInput, _ *string, d *time.Time) { in.DueDate = d },
	"issue_time":        func(in *invoice.CreateInput, s *string, _ *time.Time) { in.IssueTime = s },
	"tax_point_date":    func(in *invoice.CreateInput, _ *string, d *time.Time) { in.TaxPointDate = d },
	"payment_status":    func(in *invoice.CreateInput, s *string, _ *time.Time) { in.PaymentStatus = s },
	"buyer_email":       func(in *invoice.CreateInput, s *string, _ *time.Time) { in.BuyerEmail = s },
	"buyer_telephone":   func(in *invoice.CreateInput, s *string, _ *time.Time) { in.BuyerTelephone = s },
	"buyer_street":      func(in *invoice.CreateInput, s *string, _ *time.Time) { in.BuyerStreet = s },
	"buyer_city":        func(in *invoice.CreateInput, s *string, _ *time.Time) { in.BuyerCity = s },
	"buyer_postal_zone": func(in *invoice.CreateInput, s *string, _ *time.Time) { in.BuyerPostalZone = s },
	"buyer_country":     func(in *invoice.CreateInput, s *string, _ *time.Time) { in.BuyerCountry = s },
	"buyer_state":       func(in *invoice.CreateInput, s *string, _ *time.Time) { in.BuyerState = s },
	"buyer_lga":         func(in *invoice.CreateInput, s *string, _ *time.Time) { in.BuyerLGA = s },
}

var lineSetters = map[string]func(l *invoice.LineItemInput, s *string){
	"line_description":                 func(l *invoice.LineItemInput, s *string) { l.Description = s },
	"line_quantity":                    func(l *invoice.LineItemInput, s *string) { l.Quantity = s },
	"line_unit_price":                  func(l *invoice.LineItemInput, s *string) { l.UnitPrice = s },
	"line_total":                       func(l *invoice.LineItemInput, s *string) { l.LineTotal = s },
	"line_tax":                         func(l *invoice.LineItemInput, s *string) { l.LineTax = s },
	"line_tax_category":                func(l *invoice.LineItemInput, s *string) { l.TaxCategory = s },
	"line_hsn_code":                    func(l *invoice.LineItemInput, s *string) { l.HSNCode = s },
	"line_isic_code":                   func(l *invoice.LineItemInput, s *string) { l.ISICCode = s },
	"line_product_category":            func(l *invoice.LineItemInput, s *string) { l.ProductCategory = s },
	"line_service_category":            func(l *invoice.LineItemInput, s *string) { l.ServiceCategory = s },
	"line_sellers_item_identification": func(l *invoice.LineItemInput, s *string) { l.SellersItemIdentification = s },
	"line_price_unit":                  func(l *invoice.LineItemInput, s *string) { l.PriceUnit = s },
	"line_tax_percent":                 func(l *invoice.LineItemInput, s *string) { l.TaxPercent = s },
	"line_base_quantity":               func(l *invoice.LineItemInput, s *string) { l.BaseQuantity = s },
}

type linePlan struct {
	t Target
	p srcPath
}

// Apply maps doc and its party through m. A missing key reads as nil; a value that cannot be
// read as its target's type is ErrUnmappable, naming the target and never the value.
func Apply(m Mapping, doc Document, party *Party) (Mapped, error) {
	var out Mapped
	body, ok := decodeBody(doc.Body)
	if !ok {
		return out, unmappable("body")
	}
	var partyBody any
	if party != nil {
		if partyBody, ok = decodeBody(party.Body); !ok {
			return out, unmappable("party")
		}
	}
	var lines []linePlan
	for _, t := range Targets() {
		path, ok := m[t.Key]
		if !ok {
			continue
		}
		p, reason := checkTarget(t, path)
		if reason != "" {
			return out, invalidMapping(t.Key, reason)
		}
		if t.Line {
			lines = append(lines, linePlan{t, p})
			continue
		}
		s, d, err := readTarget(t, p, body, partyBody)
		if err != nil {
			return out, err
		}
		switch {
		case s == nil && t.Key == "invoice_number":
			return out, unmappable(t.Key)
		case s == nil:
		case t.Key == TargetCustomerID:
			out.CustomerID = *s
		default:
			headSetters[t.Key](&out.In, s, d)
		}
	}
	if len(lines) == 0 {
		return out, nil
	}
	arr := func(lp linePlan) []any {
		a, _ := walk(body, lp.p.segs[:lp.p.arrAt+1]).([]any)
		return a
	}
	n := len(arr(lines[0]))
	out.In.LineItems = make([]invoice.LineItemInput, n)
	out.TaxCodes, out.Items = make([]string, n), make([]string, n)
	for _, lp := range lines {
		els := arr(lp)
		for i, el := range els[:min(n, len(els))] {
			s, err := leaf(lp.t.Key, walk(el, lp.p.segs[lp.p.arrAt+1:]))
			if err == nil {
				_, err = typed(lp.t, s)
			}
			if err != nil {
				return out, err
			}
			if s == nil {
				continue
			}
			switch lp.t.Key {
			case TargetTaxCode:
				out.TaxCodes[i] = *s
			case TargetItemID:
				out.Items[i] = *s
			default:
				lineSetters[lp.t.Key](&out.In.LineItems[i], s)
			}
		}
	}
	return out, nil
}
