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
// treats as behavioural triggers. EXTR-04 owns the real field vocabulary; these names are
// illustrative.
var mockFixtures = []mockFixture{
	{
		name: "clean-invoice",
		body: "MOCK-FIXTURE clean-invoice v1\n",
		fields: []FieldResult{
			mockClean(Field{Name: "invoice_number", Value: mockValue("MOCK-INV-0001"), Region: &Region{Page: 1, X0: 0.62, Y0: 0.08, X1: 0.90, Y1: 0.13}, Reason: ReasonNone}),
			mockClean(Field{Name: "invoice_date", Value: mockValue("2026-01-01"), Region: &Region{Page: 1, X0: 0.62, Y0: 0.14, X1: 0.90, Y1: 0.19}, Reason: ReasonNone}),
			mockClean(Field{Name: "total_amount", Value: mockValue("1000.00"), Region: &Region{Page: 1, X0: 0.62, Y0: 0.70, X1: 0.90, Y1: 0.76}, Reason: ReasonNone}),
			mockClean(Field{Name: "supplier_tin", Value: mockValue("MOCK-TIN-SUPPLIER"), Region: &Region{Page: 1, X0: 0.10, Y0: 0.08, X1: 0.38, Y1: 0.13}, Reason: ReasonNone}),
			mockClean(Field{Name: "buyer_tin", Value: mockValue("MOCK-TIN-BUYER"), Region: &Region{Page: 1, X0: 0.10, Y0: 0.30, X1: 0.38, Y1: 0.35}, Reason: ReasonNone}),
		},
	},
	{
		name: "unreadable-scan",
		body: "MOCK-FIXTURE unreadable-scan v1\n",
		fields: []FieldResult{
			mockClean(Field{Name: "invoice_number", Reason: ReasonUnreadable}),
			mockClean(Field{Name: "invoice_date", Reason: ReasonUnreadable}),
			mockClean(Field{Name: "total_amount", Reason: ReasonUnreadable}),
			mockClean(Field{Name: "supplier_tin", Reason: ReasonUnreadable}),
			mockClean(Field{Name: "buyer_tin", Reason: ReasonUnreadable}),
		},
	},
}

// mockDefaultResult answers every unrecognised document: all five Reason values, both Region
// states and both Value states, so a downstream story gets every branch without a fixture file.
// buyer_tin carries a nil Value, not an empty string -- laws E08 and E10 together leave that
// the only legal shape for a ReasonMissing field.
var mockDefaultResult = []FieldResult{
	mockClean(Field{Name: "invoice_number", Value: mockValue("MOCK-INV-0001"), Region: &Region{Page: 1, X0: 0.62, Y0: 0.08, X1: 0.90, Y1: 0.13}, Reason: ReasonNone}),
	mockClean(Field{Name: "invoice_date", Value: mockValue("2026-01-01"), Region: &Region{Page: 1, X0: 0.62, Y0: 0.14, X1: 0.90, Y1: 0.19}, Reason: ReasonAmbiguous}),
	mockClean(Field{Name: "total_amount", Value: mockValue("1000.00"), Reason: ReasonInconsistent}),
	mockClean(Field{Name: "supplier_tin", Region: &Region{Page: 1, X0: 0.10, Y0: 0.08, X1: 0.38, Y1: 0.13}, Reason: ReasonUnreadable}),
	mockClean(Field{Name: "buyer_tin", Reason: ReasonMissing}),
}

// mockResults is the SHA-256 lookup Extract reads.
var mockResults = func() map[[32]byte][]FieldResult {
	m := make(map[[32]byte][]FieldResult, len(mockFixtures))
	for _, fx := range mockFixtures {
		m[sha256.Sum256([]byte(fx.body))] = fx.fields
	}
	return m
}()
