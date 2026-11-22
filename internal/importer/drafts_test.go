package importer

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

type draftCreator struct {
	calls []string
	errs  map[int]error
}

func (c *draftCreator) Create(_ context.Context, in invoice.CreateInput) (invoice.Invoice, error) {
	i := len(c.calls)
	c.calls = append(c.calls, in.InvoiceNumber)
	if err := c.errs[i]; err != nil {
		return invoice.Invoice{}, err
	}
	return invoice.Invoice{ID: "id-" + in.InvoiceNumber, InvoiceNumber: in.InvoiceNumber}, nil
}

type draftValidator struct {
	calls [][]invoice.Invoice
	out   invoice.BatchOutcome
	err   error
}

func (v *draftValidator) ValidateBatch(_ context.Context, invs []invoice.Invoice) (invoice.BatchOutcome, error) {
	v.calls = append(v.calls, invs)
	return v.out, v.err
}

type finalizeCall struct {
	status string
	out    DraftOutcome
	// validatorCalls at the moment finalize ran.
	validated int
}

func draftIns(n int) []invoice.CreateInput {
	ins := make([]invoice.CreateInput, n)
	for i := range ins {
		ins[i].InvoiceNumber = "INV-" + strconv.Itoa(i)
	}
	return ins
}

func recordFinalize(v *draftValidator, calls *[]finalizeCall, ret error) func(context.Context, DraftOutcome, string) error {
	return func(_ context.Context, out DraftOutcome, status string) error {
		*calls = append(*calls, finalizeCall{status: status, out: out, validated: len(v.calls)})
		return ret
	}
}

func TestCreateDrafts_CreatesValidatesThenFinalizesCompleted(t *testing.T) {
	c, v := &draftCreator{}, &draftValidator{}
	var fin []finalizeCall
	out, err := CreateDrafts(context.Background(), c, v, draftIns(3), recordFinalize(v, &fin, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.calls; len(got) != 3 || got[0] != "INV-0" || got[1] != "INV-1" || got[2] != "INV-2" {
		t.Fatalf("creates = %v", got)
	}
	if len(v.calls) != 1 || len(v.calls[0]) != 3 {
		t.Fatalf("validate calls = %v", v.calls)
	}
	for i, inv := range v.calls[0] {
		if inv.InvoiceNumber != "INV-"+strconv.Itoa(i) {
			t.Fatalf("validated order: %d is %s", i, inv.InvoiceNumber)
		}
	}
	if len(fin) != 1 || fin[0].status != "completed" || fin[0].validated != 1 {
		t.Fatalf("finalize = %+v", fin)
	}
	if len(out.Results) != 3 {
		t.Fatalf("results = %d", len(out.Results))
	}
	for i, r := range out.Results {
		if r.Invoice == nil {
			t.Fatalf("result %d has no invoice", i)
		}
	}
}

func TestCreateDrafts_FinalizeErrorIsReturned(t *testing.T) {
	errX := errors.New("x")
	v := &draftValidator{}
	var fin []finalizeCall
	_, err := CreateDrafts(context.Background(), &draftCreator{}, v, draftIns(1), recordFinalize(v, &fin, errX))
	if !errors.Is(err, errX) {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateDrafts_OutcomeCarriesVersionViolationsAndUnvalidated(t *testing.T) {
	viol := []invoice.Violation{{}}
	v := &draftValidator{out: invoice.BatchOutcome{
		RuleSetVersion: 7, Clean: 1, WithViolations: 1,
		ByID:        map[string][]invoice.Violation{"id-INV-0": viol},
		Unvalidated: []string{"id-INV-2"},
	}}
	var fin []finalizeCall
	out, err := CreateDrafts(context.Background(), &draftCreator{}, v, draftIns(3), recordFinalize(v, &fin, nil))
	if err != nil {
		t.Fatal(err)
	}
	if out.RuleSetVersion == nil || *out.RuleSetVersion != 7 {
		t.Fatalf("version = %v", out.RuleSetVersion)
	}
	if len(out.Results[0].Violations) != 1 || out.Results[0].Unvalidated {
		t.Fatalf("result 0 = %+v", out.Results[0])
	}
	if !out.Results[2].Unvalidated || out.Results[1].Unvalidated {
		t.Fatalf("unvalidated flags wrong: %+v", out.Results)
	}
	if out.InvoicesClean != 1 || out.InvoicesWithViolations != 1 {
		t.Fatalf("counts = %d/%d", out.InvoicesClean, out.InvoicesWithViolations)
	}
	if fin[0].out.RuleSetVersion == nil {
		t.Fatal("finalize did not see the validated outcome")
	}
}

func TestCreateDrafts_DomainRefusalIsRecordedAndTheRestContinue(t *testing.T) {
	c := &draftCreator{errs: map[int]error{1: invoice.ErrDuplicateNumber}}
	v := &draftValidator{}
	var fin []finalizeCall
	out, err := CreateDrafts(context.Background(), c, v, draftIns(3), recordFinalize(v, &fin, nil))
	if err != nil {
		t.Fatal(err)
	}
	r := out.Results[1]
	if !errors.Is(r.Err, invoice.ErrDuplicateNumber) || r.Message != "invoice number already imported" || r.Invoice != nil {
		t.Fatalf("result 1 = %+v", r)
	}
	if out.Results[0].Invoice == nil || out.Results[2].Invoice == nil {
		t.Fatal("inputs 0 and 2 should be created")
	}
	if len(v.calls) != 1 || len(v.calls[0]) != 2 || v.calls[0][0].InvoiceNumber != "INV-0" || v.calls[0][1].InvoiceNumber != "INV-2" {
		t.Fatalf("validator got %v", v.calls)
	}
}

func TestCreateDrafts_OperationalCreateErrorFinalizesFailedOnce(t *testing.T) {
	boom := errors.New("db down")
	c := &draftCreator{errs: map[int]error{1: boom}}
	v := &draftValidator{}
	var fin []finalizeCall
	_, err := CreateDrafts(context.Background(), c, v, draftIns(3), recordFinalize(v, &fin, nil))
	if err != boom {
		t.Fatalf("err = %v", err)
	}
	if len(fin) != 1 || fin[0].status != "failed" || fin[0].out.Results[0].Invoice == nil {
		t.Fatalf("finalize = %+v", fin)
	}
	if len(c.calls) != 2 {
		t.Fatalf("creates after the fault: %v", c.calls)
	}
	if len(v.calls) != 0 {
		t.Fatal("validator must not run")
	}
}

func TestCreateDrafts_ValidatorErrorFinalizesFailedOnce(t *testing.T) {
	errV := errors.New("gate down")
	v := &draftValidator{err: errV}
	var fin []finalizeCall
	_, err := CreateDrafts(context.Background(), &draftCreator{}, v, draftIns(2), recordFinalize(v, &fin, nil))
	if err != errV {
		t.Fatalf("err = %v", err)
	}
	if len(fin) != 1 || fin[0].status != "failed" {
		t.Fatalf("finalize = %+v", fin)
	}
}

func TestCreateDrafts_NothingCreatedSkipsTheValidator(t *testing.T) {
	c := &draftCreator{errs: map[int]error{0: invoice.ErrDuplicateNumber, 1: invoice.ErrValidation}}
	v := &draftValidator{}
	var fin []finalizeCall
	out, err := CreateDrafts(context.Background(), c, v, draftIns(2), recordFinalize(v, &fin, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.calls) != 0 || out.RuleSetVersion != nil {
		t.Fatalf("validator calls %d, version %v", len(v.calls), out.RuleSetVersion)
	}
	if len(fin) != 1 || fin[0].status != "completed" {
		t.Fatalf("finalize = %+v", fin)
	}
}

func TestImport_CreatesValidatesAndFinalizesOnlyThroughCreateDrafts(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "service.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "Import" && fd.Recv != nil {
			fn = fd
		}
	}
	if fn == nil {
		t.Fatal("Service.Import not found")
	}
	createDrafts, direct := 0, 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if fun.Name == "CreateDrafts" {
				createDrafts++
			}
		case *ast.SelectorExpr:
			if x, ok := fun.X.(*ast.SelectorExpr); ok {
				if (x.Sel.Name == "inv" && fun.Sel.Name == "Create") || (x.Sel.Name == "gate" && fun.Sel.Name == "ValidateBatch") {
					direct++
				}
			}
		}
		return true
	})
	if createDrafts != 1 || direct != 0 {
		t.Fatalf("CreateDrafts calls = %d, direct create/validate calls = %d", createDrafts, direct)
	}
}
