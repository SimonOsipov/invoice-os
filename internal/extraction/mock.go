// mock.go: MockExtractor, the only Extractor EXTR-01 ships. Keyed on the SHA-256 of doc.Bytes:
// two recognised fixtures, one default result for everything else.
package extraction

import (
	"context"
	"crypto/sha256"
)

// Stamped into extraction_jobs.extractor / .extractor_version on every row, so a mock-produced
// result stays identifiable. Pinned by TestMockExtractor_PinsNameAndVersion.
const (
	mockExtractorName    = "mock"
	mockExtractorVersion = "v1"
)

// MockExtractor holds no field: no clock, no counter, no cache, no state of any kind.
type MockExtractor struct{}

// Pointer only; TestMockExtractor_OnlyThePointerSatisfiesExtractor rejects a value receiver.
var _ Extractor = (*MockExtractor)(nil)

func NewMockExtractor() *MockExtractor { return &MockExtractor{} }

func (m *MockExtractor) Name() string { return mockExtractorName }

func (m *MockExtractor) Version() string { return mockExtractorVersion }

// Extract returns the fixture's result for recognised bytes and mockDefaultResult otherwise.
// ctx.Err() is tested BEFORE the hash so a cancelled call never reads the contract corpus's
// 15 MiB case (law E12).
func (m *MockExtractor) Extract(ctx context.Context, doc Document) ([]FieldResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if fields, ok := mockResults[sha256.Sum256(doc.Bytes)]; ok {
		return cloneFields(fields), nil
	}
	return cloneFields(mockDefaultResult), nil
}

// MockFixture is one input MockExtractor recognises.
type MockFixture struct {
	Name  string
	Bytes []byte // a fresh copy; the caller owns it
}

// MockFixtures returns every recognised input, in declaration order.
// TestMockFixtures_HandsBackACopy locks the copy.
func MockFixtures() []MockFixture {
	out := make([]MockFixture, len(mockFixtures))
	for i, fx := range mockFixtures {
		out[i] = MockFixture{Name: fx.name, Bytes: []byte(fx.body)}
	}
	return out
}

// cloneFields deep-copies a result, alternatives included. Without it a caller mutating a
// returned Field would reach the table itself -- TestMockExtractor_ReturnsFreshMemoryPerCall.
func cloneFields(src []FieldResult) []FieldResult {
	out := make([]FieldResult, len(src))
	for i, fr := range src {
		out[i].Field = cloneField(fr.Field)
		out[i].Alternatives = make([]Field, len(fr.Alternatives))
		for j, alt := range fr.Alternatives {
			out[i].Alternatives[j] = cloneField(alt)
		}
	}
	return out
}

// cloneField copies one Field's Value and Region off the shared table.
func cloneField(f Field) Field {
	out := f
	if f.Value != nil {
		v := *f.Value
		out.Value = &v
	}
	if f.Region != nil {
		r := *f.Region
		out.Region = &r
	}
	return out
}

// mockValue is the only way this file builds a *string.
func mockValue(s string) *string { return &s }

// mockFixture pairs recognised bytes with the result they select. body is a string, not a
// []byte, so nothing a caller of MockFixtures does can reach the lookup key.
type mockFixture struct {
	name   string
	body   string
	fields []FieldResult
}

// mockClean wraps one decided reading with no alternatives -- the shape every fixture field
// takes. Alternatives is empty and non-nil: a nil []T without omitempty marshals to null.
func mockClean(f Field) FieldResult { return FieldResult{Field: f, Alternatives: []Field{}} }

// No TIN here is in the reserved 99999999-000N block internal/submission/mock_script.go:76-91
// treats as behavioural triggers. Every name in mockFixtures is on HeaderFields
// (vocabulary.go), so documentCreateInput maps it to a column instead of dropping it. The
// default result below also carries line-item names, which are deliberately off HeaderFields.
var mockFixtures = []mockFixture{
	{
		name: "clean-invoice",
		body: "MOCK-FIXTURE clean-invoice v1\n",
		fields: []FieldResult{
			mockClean(Field{Name: "invoice_number", Value: mockValue("MOCK-INV-0001"), Region: &Region{Page: 1, X0: 0.62, Y0: 0.08, X1: 0.90, Y1: 0.13}, Reason: ReasonNone}),
			mockClean(Field{Name: "issue_date", Value: mockValue("2026-01-01"), Region: &Region{Page: 1, X0: 0.62, Y0: 0.14, X1: 0.90, Y1: 0.19}, Reason: ReasonNone}),
			mockClean(Field{Name: "total", Value: mockValue("1000.00"), Region: &Region{Page: 1, X0: 0.62, Y0: 0.70, X1: 0.90, Y1: 0.76}, Reason: ReasonNone}),
			mockClean(Field{Name: "supplier_tin", Value: mockValue("MOCK-TIN-SUPPLIER"), Region: &Region{Page: 1, X0: 0.10, Y0: 0.08, X1: 0.38, Y1: 0.13}, Reason: ReasonNone}),
			mockClean(Field{Name: "buyer_tin", Value: mockValue("MOCK-TIN-BUYER"), Region: &Region{Page: 1, X0: 0.10, Y0: 0.30, X1: 0.38, Y1: 0.35}, Reason: ReasonNone}),
		},
	},
	{
		name: "unreadable-scan",
		body: "MOCK-FIXTURE unreadable-scan v1\n",
		fields: []FieldResult{
			mockClean(Field{Name: "invoice_number", Reason: ReasonUnreadable}),
			mockClean(Field{Name: "issue_date", Reason: ReasonUnreadable}),
			mockClean(Field{Name: "total", Reason: ReasonUnreadable}),
			mockClean(Field{Name: "supplier_tin", Reason: ReasonUnreadable}),
			mockClean(Field{Name: "buyer_tin", Reason: ReasonUnreadable}),
		},
	},
}

// mockDefaultResult answers every unrecognised document: seven header readings covering all
// five Reason values, both Region states and both Value states, then the four-line block below
// -- so a downstream story gets every branch without a fixture file.
// buyer_tin carries a nil Value, not an empty string -- laws E08 and E10 together leave that
// the only legal shape for a ReasonMissing field. issue_date's three readings sit at three
// distinct boxes; TestMockExtractor_AlternativeRegionsDifferFromTheDecidedReading holds them
// apart, because a region-swap cannot be observed against a shared box.
// subtotal disagrees with the four line totals (2095.50) by design: the table-level
// reconciliation state needs a demo that shows it.
var mockDefaultResult = append([]FieldResult{
	mockClean(Field{Name: "invoice_number", Value: mockValue("MOCK-INV-0001"), Region: &Region{Page: 1, X0: 0.62, Y0: 0.08, X1: 0.90, Y1: 0.13}, Reason: ReasonNone}),
	{
		Field: Field{Name: "issue_date", Value: mockValue("2026-01-01"), Region: &Region{Page: 1, X0: 0.62, Y0: 0.14, X1: 0.90, Y1: 0.19}, Reason: ReasonAmbiguous},
		// An alternative carries no Reason of its own: FieldResult puts it on the decided reading.
		Alternatives: []Field{
			{Name: "issue_date", Value: mockValue("2026-01-10"), Region: &Region{Page: 1, X0: 0.62, Y0: 0.80, X1: 0.90, Y1: 0.85}},
			{Name: "issue_date", Value: mockValue("2026-10-01"), Region: &Region{Page: 1, X0: 0.10, Y0: 0.50, X1: 0.38, Y1: 0.55}},
		},
	},
	mockClean(Field{Name: "total", Value: mockValue("1000.00"), Reason: ReasonInconsistent}),
	mockClean(Field{Name: "subtotal", Value: mockValue("950.00"), Reason: ReasonInconsistent}),
	// Inconsistent, not unreadable, and with a value: a field needs a reading to disagree with.
	mockClean(Field{Name: "supplier_tin", Value: mockValue("MOCK-TIN-SUPPLIER-ALT"), Region: &Region{Page: 1, X0: 0.10, Y0: 0.08, X1: 0.38, Y1: 0.13}, Reason: ReasonInconsistent}),
	mockClean(Field{Name: "buyer_tin", Reason: ReasonMissing}),
	mockClean(Field{Name: "vat", Region: &Region{Page: 1, X0: 0.62, Y0: 0.64, X1: 0.90, Y1: 0.69}, Reason: ReasonUnreadable}),
}, mockDefaultLineBlock()...)

// mockLineColumns and mockLineRows are the line grid's bands on page 1: four columns by four
// rows, clear of every header box the default result carries. The nearest one below the grid
// is issue_date's third reading at 0.50-0.55, not the lowest: its second sits lower still, at
// 0.80-0.85.
var mockLineColumns = map[string][2]float64{
	LineRoleDescription: {0.10, 0.44},
	LineRoleQuantity:    {0.46, 0.54},
	LineRoleUnitPrice:   {0.56, 0.72},
	LineRoleLineTotal:   {0.74, 0.90},
}

var mockLineRows = [][2]float64{{0.30, 0.34}, {0.35, 0.39}, {0.40, 0.44}, {0.45, 0.49}}

// mockLineRegions boxes one line's cells. A role left out gets no box, matching an absent
// value: LineItemResults emits no row for it, so a box would be dead data.
func mockLineRegions(index int, roles ...string) map[string]*Region {
	out := make(map[string]*Region, len(roles))
	for _, role := range roles {
		x, y := mockLineColumns[role], mockLineRows[index-1]
		out[role] = &Region{Page: 1, X0: x[0], Y0: y[0], X1: x[1], Y1: y[1]}
	}
	return out
}

// mockDefaultLines is the demo grid: one clean line, one whose total disagrees with qty x
// price, one missing a cell, one clean. Line 2's description is long on purpose -- it is what
// exercises the grid column's width.
var mockDefaultLines = []DocLine{
	{
		Index: 1, Description: mockValue("Widget"), Quantity: mockValue("2"),
		UnitPrice: mockValue("500.00"), LineTotal: mockValue("1000.00"),
		Regions: mockLineRegions(1, LineRoles...),
	},
	{
		Index: 2, Description: mockValue("Assembly, calibration and on-site commissioning of the line-item rig"), Quantity: mockValue("3"),
		UnitPrice: mockValue("250.00"), LineTotal: mockValue("900.00"),
		Regions: mockLineRegions(2, LineRoles...),
	},
	{
		Index: 3, Description: mockValue("Delivery"),
		UnitPrice: mockValue("120.00"), LineTotal: mockValue("120.00"),
		Regions: mockLineRegions(3, LineRoleDescription, LineRoleUnitPrice, LineRoleLineTotal),
	},
	{
		Index: 4, Description: mockValue("Installation"), Quantity: mockValue("1"),
		UnitPrice: mockValue("75.50"), LineTotal: mockValue("75.50"),
		Regions: mockLineRegions(4, LineRoles...),
	},
}

// mockDefaultLineBlock is the block row plus one row per populated cell, in LineItemResults'
// own emit order. Reconcile never runs on the mock's output -- the worker calls Extract
// directly -- so line 2's arithmetic disagreement (3 x 250.00 = 750.00, not 900.00) is stamped
// here rather than derived.
func mockDefaultLineBlock() []FieldResult {
	out := []FieldResult{mockClean(Field{Name: "line_items", Reason: ReasonNone})}
	for _, fr := range LineItemResults(mockDefaultLines) {
		if fr.Name == LineFieldName(2, LineRoleLineTotal) {
			fr.Reason = ReasonInconsistent
		}
		out = append(out, fr)
	}
	return out
}

// mockResults is the SHA-256 lookup Extract reads.
var mockResults = func() map[[32]byte][]FieldResult {
	m := make(map[[32]byte][]FieldResult, len(mockFixtures))
	for _, fx := range mockFixtures {
		m[sha256.Sum256([]byte(fx.body))] = fx.fields
	}
	return m
}()
