package importer

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// dtSeedPoorScan seeds the one-row document_text_layer set the worker writes for a textless scan.
func dtSeedPoorScan(t *testing.T, super *pgxpool.Pool, tenantID, documentID string) {
	t.Helper()
	job := seedExtractionJob(t, super, tenantID, documentID, "succeeded", time.Now().UTC())
	seedExtractionField(t, super, tenantID, job, "document_text_layer", nil, sxPtr("unreadable"), 0, time.Now().UTC())
}

func dtSetVerdict(t *testing.T, super *pgxpool.Pool, documentID string) {
	t.Helper()
	ct, err := super.Exec(context.Background(),
		`UPDATE extraction_jobs SET document_type = 'receipt' WHERE document_id = $1`, documentID)
	if err != nil || ct.RowsAffected() != 1 {
		t.Fatalf("set the verdict on document %s: rows %d, err %v", documentID, ct.RowsAffected(), err)
	}
	var got *string
	if err := super.QueryRow(context.Background(),
		`SELECT document_type FROM extraction_jobs WHERE document_id = $1`, documentID).Scan(&got); err != nil {
		t.Fatalf("read the verdict back: %v", err)
	}
	if got == nil || *got != "receipt" {
		t.Fatalf("the verdict row holds %v, want receipt -- the comparison below would hold over nothing", got)
	}
}

// dtNormalise blanks what differs between two tenants' runs of one reading.
func dtNormalise(r BatchResult) BatchResult {
	r.ID = ""
	for i := range r.Errors {
		r.Errors[i].InvoiceID = ""
	}
	for i := range r.InvoiceViolations {
		r.InvoiceViolations[i].InvoiceID = ""
	}
	return r
}

func TestServiceImportDocument_ADocumentTypeVerdictChangesNoOutcome(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	svc := newTestService(app)

	for _, tc := range []struct {
		name        string
		seed        func(t *testing.T, tenantID, documentID string)
		wantReady   int
		wantMessage string
	}{
		{"clean", func(t *testing.T, tenantID, documentID string) {
			docSeedExtraction(t, super, tenantID, documentID, docCleanValues("DT-1"))
		}, 1, ""},
		{"no number", func(t *testing.T, tenantID, documentID string) {
			docSeedExtraction(t, super, tenantID, documentID, docNoNumberValues())
		}, 0, noInvoiceNumberMessage},
		{"poor scan", func(t *testing.T, tenantID, documentID string) {
			dtSeedPoorScan(t, super, tenantID, documentID)
		}, 0, poorScanMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type run struct {
				tenantID, documentID string
				res                  BatchResult
			}
			var runs [2]run
			for i, label := range []string{"verdict", "plain"} {
				tenantID := seedTenant(t, super, "document type "+label+" "+tc.name)
				entityID := seedEntity(t, super, tenantID, "document type "+label)
				documentID := docSeedDocument(t, super, tenantID)
				tc.seed(t, tenantID, documentID)
				if label == "verdict" {
					dtSetVerdict(t, super, documentID)
				}
				res, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID)
				if err != nil {
					t.Fatalf("%s: ImportDocument: %v", label, err)
				}
				runs[i] = run{tenantID, documentID, res}
			}
			withV, without := runs[0].res, runs[1].res

			if without.ReadyInvoices != tc.wantReady {
				t.Fatalf("the plain run filed %d invoice(s), want %d: %+v", without.ReadyInvoices, tc.wantReady, without)
			}
			if tc.wantMessage != "" && (len(without.Errors) != 1 || without.Errors[0].Message != tc.wantMessage) {
				t.Fatalf("the plain run's errors = %+v, want one carrying %q", without.Errors, tc.wantMessage)
			}
			if a, b := dtNormalise(withV), dtNormalise(without); !reflect.DeepEqual(a, b) {
				t.Errorf("BatchResult with a verdict differs from without:\n with:    %+v\n without: %+v", a, b)
			}

			if tc.name != "no number" {
				return
			}
			var readings [2]*CarriedReading
			for i, r := range runs {
				reading, err := svc.CarriedReading(sxIdentity(ctx, r.tenantID), r.documentID)
				if err != nil || reading == nil {
					t.Fatalf("CarriedReading for run %d: reading=%v err=%v, want a reading", i, reading, err)
				}
				cp := *reading
				cp.DocumentID, cp.ExtractionJobID = "", ""
				readings[i] = &cp
			}
			if !reflect.DeepEqual(readings[0], readings[1]) {
				t.Errorf("CarriedReading with a verdict differs from without:\n with:    %+v\n without: %+v", *readings[0], *readings[1])
			}
		})
	}
}

// SettledExtraction is the importer's only read of extraction_jobs; the verdict must not reach it.
func TestSettledExtraction_ReadsNoDocumentType(t *testing.T) {
	src, err := os.ReadFile("document.go")
	if err != nil {
		t.Fatalf("read document.go: %v", err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "document.go", src, 0)
	if err != nil {
		t.Fatalf("parse document.go: %v", err)
	}
	// String literals only: a comment naming document_type is not a read.
	var lits []string
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "SettledExtraction" || fd.Recv == nil || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING {
				lits = append(lits, bl.Value)
			}
			return true
		})
	}
	if len(lits) == 0 {
		t.Fatal("found no string literal in a (*Store).SettledExtraction body in document.go")
	}
	body := strings.Join(lits, "\n")
	for _, want := range []string{"coalesce(d.filename, '')", "FROM extraction_field_results"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the located body lacks %q; the scan below is not reading SettledExtraction's queries", want)
		}
	}
	if strings.Contains(body, "document_type") {
		t.Error("SettledExtraction reads document_type; a verdict would reach the importer")
	}
}
