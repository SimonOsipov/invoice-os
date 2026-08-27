// mock.go: STAGE 2.5 PROBE STUB, deliberately wrong. A constant mock over one shared backing
// slice: it satisfies Extractor and passes all twelve laws while doing none of what AC #2-#5
// ask, which is what makes the red set in mock_test.go honest rather than a build failure.
// EXTR-01-06 Stage 3 replaces this whole file.
package extraction

import "context"

type MockExtractor struct{}

// Asserted on the POINTER type alone: a value and a pointer both satisfying Extractor is the
// aliasing hazard internal/submission/mock_adapter.go:150-153 documents.
var _ Extractor = (*MockExtractor)(nil)

func NewMockExtractor() *MockExtractor { return &MockExtractor{} }

func (m *MockExtractor) Name() string    { return "mock" }
func (m *MockExtractor) Version() string { return "v1" }

func stubPtr(s string) *string { return &s }

var stubFields = []Field{{
	Name:   "invoice_number",
	Value:  stubPtr("MOCK-INV-0001"),
	Region: &Region{Page: 1, X0: 0.10, Y0: 0.08, X1: 0.42, Y1: 0.13},
	Reason: ReasonNone,
}}

// Extract hands every caller the same slice, so the stub is red on freshness by construction.
func (m *MockExtractor) Extract(ctx context.Context, doc Document) ([]Field, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return stubFields, nil
}

// MockFixture is one input MockExtractor recognises.
type MockFixture struct {
	Name  string
	Bytes []byte
}

// MockFixtures returns every recognised input. The stub recognises none.
func MockFixtures() []MockFixture { return nil }
