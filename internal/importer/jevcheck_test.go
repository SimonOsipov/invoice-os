package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/jevmeasure"
	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

// The run-2 wording from CHECK-00 Jev Measurement Results, "Mapping check — 2026-09-24".
const (
	mcMeasuredInstructions = "State whether the following statement about this spreadsheet is true: the named column holds the named invoice field. The spreadsheet's first rows are shown; one row is one invoice line, and invoice-level values repeat on every line of the same invoice. Judge from the column's header and its values; a header may use any name, abbreviation or language for its field. The fields are: invoice_number: the invoice's own number, not an order, PO, customer, account or payment reference. issue_date: the date the invoice was issued, not a due, delivery or payment date. buyer_tin: the buyer's (customer's) Tax Identification Number, not the seller's own TIN or an RC number. buyer_name: the buyer's name, not a customer code, address, email or phone. currency: the currency code. subtotal: the invoice's amount before VAT, not a line amount. vat: the invoice's VAT amount, not a VAT rate or a line's tax. total: the invoice's total including VAT, not a line amount, amount paid, balance due or amount after withholding tax. line_description: the line's item or service description. line_quantity: the line's quantity. line_unit_price: the line's price per unit, not a line total."
	mcMeasuredTrue         = "The column holds the named field as defined above, whatever its header is called."
	mcMeasuredFalse        = "The column holds something other than the named field as defined above: another field, a rate or percentage where an amount is defined, a line amount where an invoice amount is defined, the seller's details where the buyer's are defined, or data that is not an invoice field."
)

// mcStub records every Ask and answers a fixed pair regardless of Enabled, so a skipped
// Enabled guard still shows in the call count.
type mcStub struct {
	enabled bool
	resp    jev.Response
	err     error
	calls   []jev.Request
}

func (s *mcStub) Enabled() bool { return s.enabled }

func (s *mcStub) Ask(ctx context.Context, req jev.Request) (jev.Response, error) {
	s.calls = append(s.calls, req)
	return s.resp, s.err
}

func mcNouls(nouls map[string]float64) jev.Response {
	a := make(map[string]jev.Answer, len(nouls))
	for id, n := range nouls {
		a[id] = jev.Answer{Type: jev.TypeNoul, Noul: n}
	}
	return jev.Response{Answers: a}
}

var mcWindow = [][]string{{"Invoice No", "VAT %", "Total"}, {"INV-1", "7.5", "1075"}}

func TestMappingCheck_TheQuestionIsTheMeasuredQuestion(t *testing.T) {
	for _, tc := range []struct{ field, header string }{{"vat", "VAT %"}, {"buyer_tin", "Our TIN"}} {
		got := mappingCheckQuestion(tc.field, tc.header)
		want := jpQuestion(tc.field, tc.header)
		if got.Type != jev.TypeNoul {
			t.Errorf("%s: Type = %q, want %q", tc.field, got.Type, jev.TypeNoul)
		}
		if got.Instructions != want.Instructions {
			t.Errorf("%s: Instructions differ from jpQuestion's\n got: %q\nwant: %q", tc.field, got.Instructions, want.Instructions)
		}
		if got.True != want.Criteria["true"] || got.False != want.Criteria["false"] {
			t.Errorf("%s: True/False = %q / %q, want jpQuestion's criteria", tc.field, got.True, got.False)
		}
		if got.Options != nil || got.Default != "" {
			t.Errorf("%s: Options = %v, Default = %q; want nil and empty", tc.field, got.Options, got.Default)
		}
	}
	// Control: the comparison above is against the harness constant, not two empty strings.
	q := mappingCheckQuestion("vat", "VAT %")
	if !strings.HasPrefix(q.Instructions, jevmeasure.MappingCheckInstructions) || !strings.Contains(q.Instructions, "Column header: VAT %.") {
		t.Errorf("Instructions %q lacks the harness prefix or the header suffix", q.Instructions)
	}
	if !strings.Contains(q.Instructions, "vat: the invoice's VAT amount, not a VAT rate or a line's tax.") {
		t.Errorf("Instructions lack the measured vat definition")
	}
	if q.True != mcMeasuredTrue {
		t.Errorf("True = %q, want the measured %q", q.True, mcMeasuredTrue)
	}
	// wording.go must hold the measured text itself, not only the product copy.
	if jevmeasure.MappingCheckInstructions != mcMeasuredInstructions {
		t.Errorf("jevmeasure.MappingCheckInstructions is not the measured run-2 text:\n%q", jevmeasure.MappingCheckInstructions)
	}
	if jevmeasure.MappingCheckCriteriaTrue != mcMeasuredTrue {
		t.Errorf("jevmeasure.MappingCheckCriteriaTrue is not the measured run-2 text:\n%q", jevmeasure.MappingCheckCriteriaTrue)
	}
	if jevmeasure.MappingCheckCriteriaFalse != mcMeasuredFalse {
		t.Errorf("jevmeasure.MappingCheckCriteriaFalse is not the measured run-2 text:\n%q", jevmeasure.MappingCheckCriteriaFalse)
	}
}

func TestMappingCheck_TheThresholdIsTheMeasuredCut(t *testing.T) {
	if mappingCheckThreshold != 0.10 {
		t.Errorf("mappingCheckThreshold = %v, want 0.10", float64(mappingCheckThreshold))
	}
}

func TestMappingCheck_TheBoundaryIsInclusive(t *testing.T) {
	for _, tc := range []struct {
		noul float64
		want []string
	}{
		{0.10, []string{"vat"}},
		{0, []string{"vat"}},
		{math.Nextafter(0.10, 1), []string{}},
		{0.5, []string{}},
		{1, []string{}},
		{math.NaN(), []string{}},
	} {
		s := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"vat": tc.noul})}
		got := checkPlacements(context.Background(), s, mcWindow, map[string]string{"vat": "VAT %"})
		if !slices.Equal(got, tc.want) {
			t.Errorf("noul %v: got %v, want %v", tc.noul, got, tc.want)
		}
	}
}

func TestMappingCheck_OneRequestAsksOneNoulPerPlacement(t *testing.T) {
	var b strings.Builder
	b.WriteString("Invoice No,VAT %,Notes\n")
	for i := 1; i <= 11; i++ {
		fmt.Fprintf(&b, "INV-%d,75,row %d\n", i, i)
	}
	header, rows, _, err := Decode(strings.NewReader(b.String()), "csv")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	window := suggestWindow(header, rows)
	placements := map[string]string{"invoice_number": "Invoice No", "vat": "VAT %"}
	s := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 1, "vat": 1})}

	checkPlacements(context.Background(), s, window, placements)

	if len(s.calls) != 1 {
		t.Fatalf("Ask called %d times, want 1", len(s.calls))
	}
	req := s.calls[0]
	if req.Purpose != jev.PurposeMappingCheck {
		t.Errorf("Purpose = %q, want %q", req.Purpose, jev.PurposeMappingCheck)
	}
	if req.State != mappingPromptText(window) {
		t.Errorf("State is not mappingPromptText(window):\n%s", req.State)
	}
	if !strings.Contains(req.State, "Row 10:") || strings.Contains(req.State, "Row 11:") {
		t.Errorf("State should end at Row 10:\n%s", req.State)
	}
	ids := make([]string, 0, len(req.Questions))
	for id := range req.Questions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"invoice_number", "vat"}) {
		t.Fatalf("question ids = %v, want [invoice_number vat]", ids)
	}
	for field, header := range placements {
		if !reflect.DeepEqual(req.Questions[field], mappingCheckQuestion(field, header)) {
			t.Errorf("question %s = %+v, want mappingCheckQuestion(%q, %q)", field, req.Questions[field], field, header)
		}
	}
}

func TestMappingCheck_ReturnsTheDoubtedFieldsSorted(t *testing.T) {
	placements := map[string]string{"vat": "VAT %", "invoice_number": "Invoice No", "total": "Total", "currency": "Ccy", "buyer_tin": "TIN"}
	s := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"vat": 0.05, "invoice_number": 0.9, "total": 0.02, "currency": 0, "buyer_tin": 0.1})}
	// Map order is random; repeat so an unsorted result cannot pass by chance.
	for range 20 {
		if got := checkPlacements(context.Background(), s, mcWindow, placements); !slices.Equal(got, []string{"buyer_tin", "currency", "total", "vat"}) {
			t.Fatalf("got %v, want [buyer_tin currency total vat]", got)
		}
	}

	placements = map[string]string{"vat": "VAT %", "invoice_number": "Invoice No", "total": "Total"}
	s = &mcStub{enabled: true, resp: mcNouls(map[string]float64{"vat": 1, "invoice_number": 1, "total": 1})}
	got := checkPlacements(context.Background(), s, mcWindow, placements)
	if got == nil || len(got) != 0 {
		t.Fatalf("no doubt: got %#v, want non-nil empty", got)
	}
	if b, _ := json.Marshal(got); string(b) != "[]" {
		t.Errorf("no doubt marshals as %s, want []", b)
	}
}

func TestMappingCheck_AnySkipDoubtsNothing(t *testing.T) {
	one := map[string]string{"vat": "VAT %"}
	two := map[string]string{"vat": "VAT %", "total": "Total"}
	doubtAll := mcNouls(map[string]float64{"vat": 0, "total": 0})

	t.Run("enabled", func(t *testing.T) {
		s := &mcStub{enabled: true, resp: doubtAll}
		got := checkPlacements(context.Background(), s, mcWindow, one)
		if len(s.calls) != 1 || !slices.Equal(got, []string{"vat"}) {
			t.Errorf("control: %d Ask, got %v; want 1 Ask and [vat]", len(s.calls), got)
		}
	})
	t.Run("nil", func(t *testing.T) {
		got := checkPlacements(context.Background(), nil, mcWindow, one)
		if got == nil || len(got) != 0 {
			t.Errorf("got %#v, want non-nil empty", got)
		}
	})
	for _, tc := range []struct {
		name       string
		enabled    bool
		window     [][]string
		placements map[string]string
	}{
		{"off", false, mcWindow, one},
		{"no placements", true, mcWindow, map[string]string{}},
		{"empty window", true, nil, one},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &mcStub{enabled: tc.enabled, resp: doubtAll}
			got := checkPlacements(context.Background(), s, tc.window, tc.placements)
			if len(s.calls) != 0 || got == nil || len(got) != 0 {
				t.Errorf("%d Ask, got %#v; want 0 Ask and non-nil empty", len(s.calls), got)
			}
		})
	}
	for _, tc := range []struct {
		name string
		resp jev.Response
		err  error
	}{
		{"refused", doubtAll, fmt.Errorf("%w: refused", jev.ErrCheckSkipped)},
		{"unavailable", doubtAll, fmt.Errorf("%w: unavailable", jev.ErrCheckSkipped)},
		{"deadline", doubtAll, fmt.Errorf("jev: %w", context.DeadlineExceeded)},
		{"plain error", doubtAll, errors.New("boom")},
		{"missing id", mcNouls(map[string]float64{"vat": 0}), nil},
		{"choice answer", jev.Response{Answers: map[string]jev.Answer{
			"vat":   {Type: jev.TypeNoul, Noul: 0},
			"total": {Type: jev.TypeChoice, Choice: "x", Confidence: 1},
		}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &mcStub{enabled: true, resp: tc.resp, err: tc.err}
			got := checkPlacements(context.Background(), s, mcWindow, two)
			if len(s.calls) != 1 || got == nil || len(got) != 0 {
				t.Errorf("%d Ask, got %#v; want 1 Ask and non-nil empty", len(s.calls), got)
			}
		})
	}
}

func TestMappingCheck_TheFakeDoubtsEveryPlacementOnlyUnderItsMarker(t *testing.T) {
	t.Setenv(jev.EnvFake, "true")
	t.Setenv(jev.EnvKey, "")
	c, err := jev.FromEnv(nil)
	if err != nil {
		t.Fatalf("jev.FromEnv: %v", err)
	}
	placements := map[string]string{"invoice_number": "Invoice No", "vat": "VAT %"}
	for _, tc := range []struct {
		notes string
		want  []string
	}{
		{"Notes", []string{}},
		{"Notes JEVFAKE-DOUBT", []string{"invoice_number", "vat"}},
		{"Notes JEVFAKE-UNAVAILABLE", []string{}},
		{"Notes JEVFAKE-REFUSED", []string{}},
	} {
		window := [][]string{{"Invoice No", "VAT %", tc.notes}, {"INV-1", "75", "x"}}
		got := checkPlacements(context.Background(), c, window, placements)
		if got == nil || !slices.Equal(got, tc.want) {
			t.Errorf("header %q: got %#v, want %v", tc.notes, got, tc.want)
		}
	}
}

func TestMappingCheck_TheThresholdCarriesACeilingLine(t *testing.T) {
	src, err := os.ReadFile("jevcheck.go")
	if err != nil {
		t.Fatalf("read jevcheck.go: %v", err)
	}
	lines := strings.Split(string(src), "\n")
	i := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "const mappingCheckThreshold") })
	if i < 1 {
		t.Fatalf("const mappingCheckThreshold line not found (index %d)", i)
	}
	above := lines[i-1]
	if !strings.HasPrefix(above, "// ceiling:") || !strings.Contains(above, "re-measure on real spreadsheets") {
		t.Errorf("line above the threshold = %q, want a // ceiling: line naming re-measure on real spreadsheets", above)
	}
}

func TestImporterPackage_ImportsTheJevClientNotTheHarness(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./internal/importer")
	cmd.Dir = sxDepsRepoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps ./internal/importer: %v\n%s", err, out)
	}
	deps := strings.Split(strings.TrimSpace(string(out)), "\n")
	if !slices.Contains(deps, "context") {
		t.Fatalf("control: go list -deps never named context; the scan is broken")
	}
	if !slices.Contains(deps, "github.com/SimonOsipov/invoice-os/internal/platform/jev") {
		t.Errorf("internal/importer does not import internal/platform/jev")
	}
	if slices.Contains(deps, "github.com/SimonOsipov/invoice-os/internal/jevmeasure") {
		t.Errorf("internal/importer imports internal/jevmeasure; product code must copy the wording")
	}
}
