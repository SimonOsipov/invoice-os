// noninvoice_test.go: CHECK-01-03's seven synthetic non-invoices -- the document-type check's
// negative half. Each fixture pairs a generated PDF with a docling golden it replays from;
// AC-8 (jev_value_test.go, package endtoend) is the guard that keeps them out of every
// corpus_ ratchet the positive half already owns.
package extraction_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// Provenance also picks the golden's suffix and the reader that replays it: a docling golden is
// byte-compared by ci.yml's canary job, a pdfium golden is not.
const (
	niProvDocling = "docling"
	niProvPDFium  = "pdfium"
)

const (
	fxNonInvoiceReceipt       = "noninvoice_receipt.pdf"
	fxNonInvoiceProforma      = "noninvoice_proforma.pdf"
	fxNonInvoiceQuotation     = "noninvoice_quotation.pdf"
	fxNonInvoiceCreditNote    = "noninvoice_credit_note.pdf"
	fxNonInvoiceDeliveryNote  = "noninvoice_delivery_note.pdf"
	fxNonInvoiceStatement     = "noninvoice_statement.pdf"
	fxNonInvoicePurchaseOrder = "noninvoice_purchase_order.pdf"
)

// nonInvoiceFixture is one synthetic non-invoice: the type the document-type check must return,
// the generator that emits it, its committed bytes, and the golden its text replays from.
type nonInvoiceFixture struct {
	docType    string
	build      func() []byte
	pdf        string
	golden     string
	provenance string
}

var nonInvoiceFixtures = []nonInvoiceFixture{
	{"receipt", fxBuildNonInvoiceReceipt, fxNonInvoiceReceipt, "noninvoice_receipt.docling.json", niProvDocling},
	{"proforma", fxBuildNonInvoiceProforma, fxNonInvoiceProforma, "noninvoice_proforma.docling.json", niProvDocling},
	{"quotation", fxBuildNonInvoiceQuotation, fxNonInvoiceQuotation, "noninvoice_quotation.docling.json", niProvDocling},
	{"credit_note", fxBuildNonInvoiceCreditNote, fxNonInvoiceCreditNote, "noninvoice_credit_note.docling.json", niProvDocling},
	{"delivery_note", fxBuildNonInvoiceDeliveryNote, fxNonInvoiceDeliveryNote, "noninvoice_delivery_note.docling.json", niProvDocling},
	{"statement", fxBuildNonInvoiceStatement, fxNonInvoiceStatement, "noninvoice_statement.docling.json", niProvDocling},
	{"purchase_order", fxBuildNonInvoicePurchaseOrder, fxNonInvoicePurchaseOrder, "noninvoice_purchase_order.docling.json", niProvDocling},
}

// nonInvoiceTypes is hard-coded, never derived from nonInvoiceFixtures: derived, it could not
// see a missing type.
var nonInvoiceTypes = []string{
	"receipt", "proforma", "quotation", "credit_note", "delivery_note", "statement", "purchase_order",
}

// niTokenFloor is each fixture's golden token count as MEASURED after generation. Zero is
// unpinned and fails. filled at Stage 4 from the real read.
var niTokenFloor = map[string]int{
	fxNonInvoiceReceipt:       10,
	fxNonInvoiceProforma:      10,
	fxNonInvoiceQuotation:     9,
	fxNonInvoiceCreditNote:    9,
	fxNonInvoiceDeliveryNote:  11,
	fxNonInvoiceStatement:     10,
	fxNonInvoicePurchaseOrder: 9,
}

// niTINs are the seven TINs designed for this corpus (see the CHECK-01-03 architecture doc's
// TIN allocation table). A hit outside this set is in the free block but was never designed.
var niTINs = []string{
	"99999999-1401", "99999999-1402", "99999999-1403", "99999999-1404",
	"99999999-1405", "99999999-1406", "99999999-1407",
}

// niTitleFor is the title token AC-6 requires on each replayed golden, so a golden copied from
// a different fixture cannot satisfy a bare token count.
var niTitleFor = map[string]string{
	fxNonInvoiceReceipt:       "RECEIPT",
	fxNonInvoiceProforma:      "PROFORMA INVOICE",
	fxNonInvoiceQuotation:     "QUOTATION",
	fxNonInvoiceCreditNote:    "CREDIT NOTE",
	fxNonInvoiceDeliveryNote:  "DELIVERY NOTE",
	fxNonInvoiceStatement:     "STATEMENT OF ACCOUNT",
	fxNonInvoicePurchaseOrder: "PURCHASE ORDER",
}

// --- the tests --------------------------------------------------------------

// AC-2: the committed bytes match their generator, and every fixture has an fxCorpus entry so
// -update actually regenerates it.
func TestNonInvoice_FixturesMatchTheirGenerator(t *testing.T) {
	if len(nonInvoiceFixtures) != 7 {
		t.Fatalf("nonInvoiceFixtures names %d fixture(s), want 7", len(nonInvoiceFixtures))
	}

	fx := corpusFxNames()
	pdfs := make([]string, 0, len(nonInvoiceFixtures))
	for _, f := range nonInvoiceFixtures {
		pdfs = append(pdfs, f.pdf)
		t.Run(f.pdf, func(t *testing.T) {
			if !fx[f.pdf] {
				t.Errorf("%s has no fxCorpus entry; -update would never write it and a byte-compare would pin a hand-edited blob against a generator nothing regenerates", f.pdf)
			}

			want := f.build()
			fxAssertWellFormed(t, f.pdf, want)

			got := fxRead(t, f.pdf)
			if !bytes.Equal(got, want) {
				t.Errorf("committed %s does not match its generator: %d byte(s) on disk, %d regenerated", f.pdf, len(got), len(want))
			}
		})
	}

	if got := fxUngenerated(append(slices.Clone(pdfs), "noninvoice_planted.pdf")); !slices.Equal(got, []string{"noninvoice_planted.pdf"}) {
		t.Errorf("a planted noninvoice_planted.pdf reports %v, want exactly [noninvoice_planted.pdf]", got)
	}
}

// AC-3: the same builder run twice in one process is byte-identical.
func TestNonInvoice_GeneratorIsDeterministic(t *testing.T) {
	for _, f := range nonInvoiceFixtures {
		t.Run(f.pdf, func(t *testing.T) {
			first := f.build()
			fxAssertWellFormed(t, f.pdf, first)

			second := f.build()
			if !bytes.Equal(first, second) {
				t.Errorf("%s generated %d byte(s) then %d byte(s) in one process -- a corpus built on a timestamp or a map walk is worthless", f.pdf, len(first), len(second))
			}
		})
	}
}

// AC-4: mirrors TestCorpus_HasAllSixNamedLayouts for the seven non-invoice types.
func TestNonInvoice_TheTypeTableNamesExactlySevenTypes(t *testing.T) {
	const want = 7

	if len(nonInvoiceTypes) != want {
		t.Fatalf("nonInvoiceTypes names %d type(s), want %d -- the hard-coded set is the floor and cannot be trimmed to fit the table", len(nonInvoiceTypes), want)
	}
	named := make(map[string]bool, want)
	for _, n := range nonInvoiceTypes {
		named[n] = true
	}

	got := make(map[string]bool, want)
	for _, f := range nonInvoiceFixtures {
		got[f.docType] = true
	}
	if len(got) != want {
		t.Errorf("nonInvoiceFixtures carries %d distinct docType(s), want exactly %d -- a duplicated docType hides a missing one", len(got), want)
	}

	for _, n := range nonInvoiceTypes {
		if !got[n] {
			t.Errorf("nonInvoiceFixtures has no row for %s", n)
		}
	}
	for n := range got {
		if !named[n] {
			t.Errorf("nonInvoiceFixtures carries %s, which is not one of the seven named types", n)
		}
	}
}

// AC-5: every TIN-shaped token on the committed bytes is inside the free reserved block, is not
// one of the mock adapter's scripted or never-allocate values, and is one of the seven designed
// niTINs. Reads through pdfium regardless of provenance: this is a claim about the committed
// bytes, and it must hold whichever reader is used downstream.
func TestNonInvoice_UsesOnlyFreeReservedTINs(t *testing.T) {
	if len(niTINs) != 7 {
		t.Fatalf("niTINs names %d TIN(s), want 7", len(niTINs))
	}
	pinned := make(map[string]bool, len(niTINs))
	for _, tin := range niTINs {
		pinned[tin] = true
	}

	var all []string
	for _, f := range nonInvoiceFixtures {
		t.Run(f.pdf, func(t *testing.T) {
			pages, _ := ptRead(t, f.pdf)

			var hits []string
			for _, tok := range ptTokens(pages) {
				for _, hit := range corpusTIN.FindAllString(tok.Text, -1) {
					hits = append(hits, hit)
					if !strings.HasPrefix(hit, corpusFreeTINPrefix) {
						t.Errorf("%s carries TIN %q, outside the reserved %s block -- it could collide with a real taxpayer", f.pdf, hit, corpusFreeTINPrefix)
						continue
					}
					suffix := strings.TrimPrefix(hit, corpusFreeTINPrefix)
					for _, scripted := range corpusScriptedTINs {
						if suffix == scripted {
							t.Errorf("%s carries TIN %q, one of the mock adapter's scripted or never-allocate values (corpusScriptedTINs)", f.pdf, hit)
						}
					}
					if !pinned[hit] {
						t.Errorf("%s carries TIN %q, which is in the free block but is not one of the seven designed niTINs", f.pdf, hit)
					}
				}
			}
			if len(hits) == 0 {
				t.Fatalf("found 0 TIN-shaped token(s) on %s, want at least 1 -- this scan asserts an absence and found nothing to assert over", f.pdf)
			}
			all = append(all, hits...)
		})
	}

	if len(all) < 7 {
		t.Errorf("found %d TIN-shaped token(s) across the seven fixtures, want at least 7", len(all))
	}
}

// AC-6: every committed golden replays through the real reader its provenance names, past a
// measured per-fixture token floor, and carries the fixture's title somewhere in its tokens.
func TestNonInvoice_EveryGoldenReplaysWithTokens(t *testing.T) {
	if len(niTokenFloor) != 7 {
		t.Fatalf("niTokenFloor holds %d row(s), want 7 -- filled at Stage 4 from the real read", len(niTokenFloor))
	}

	for _, f := range nonInvoiceFixtures {
		t.Run(f.pdf, func(t *testing.T) {
			var n int
			var texts []string

			switch f.provenance {
			case niProvDocling:
				raw := dcReadNamedGolden(t, f.golden)
				_, tokens, _ := dcServeGolden(t, raw)
				for _, p := range tokens {
					n += len(p.Tokens)
					for _, tok := range p.Tokens {
						texts = append(texts, tok.Text)
					}
				}
			case niProvPDFium:
				want, err := os.ReadFile(filepath.Join(fxDir, f.golden))
				if err != nil {
					t.Fatalf("read golden %s: %v", f.golden, err)
				}
				pages, _ := ptRead(t, f.pdf)
				got := ptMarshal(t, ptTokens(pages))
				if !bytes.Equal(got, want) {
					t.Errorf("tokens for %s do not match %s", f.pdf, f.golden)
				}
				var pinned []extraction.Token
				if err := json.Unmarshal(want, &pinned); err != nil {
					t.Fatalf("unmarshal golden %s: %v", f.golden, err)
				}
				n = len(pinned)
				for _, tok := range pinned {
					texts = append(texts, tok.Text)
				}
			default:
				t.Fatalf("%s carries unknown provenance %q", f.pdf, f.provenance)
			}

			floor, ok := niTokenFloor[f.pdf]
			if !ok || floor == 0 {
				t.Errorf("niTokenFloor names no non-zero floor for %s; an emptied golden would read clean", f.pdf)
			}
			if n < floor {
				t.Errorf("%s replayed %d token(s), want at least %d", f.pdf, n, floor)
			}

			want := niTitleFor[f.pdf]
			var sawTitle bool
			for _, text := range texts {
				if strings.Contains(text, want) {
					sawTitle = true
					break
				}
			}
			if !sawTitle {
				t.Errorf("no token replayed from %s contains %q; a golden copied from a different document would satisfy a bare count", f.golden, want)
			}
		})
	}
}

// AC-7: a fixture's declared provenance is one of the two known values, its golden's suffix
// matches that provenance, and the golden is actually on disk. The suffix clause is what stops
// a pdfium-produced file named .docling.json from sailing into CI's canary byte-compare.
func TestNonInvoice_ProvenanceNamesAFileThatExists(t *testing.T) {
	if len(nonInvoiceFixtures) != 7 {
		t.Fatalf("nonInvoiceFixtures names %d fixture(s), want 7", len(nonInvoiceFixtures))
	}

	for _, f := range nonInvoiceFixtures {
		t.Run(f.pdf, func(t *testing.T) {
			switch f.provenance {
			case niProvDocling:
				if !strings.HasSuffix(f.golden, ".docling.json") {
					t.Errorf("%s declares provenance %q but golden %q does not end .docling.json", f.pdf, f.provenance, f.golden)
				}
			case niProvPDFium:
				if !strings.HasSuffix(f.golden, ".golden.json") {
					t.Errorf("%s declares provenance %q but golden %q does not end .golden.json", f.pdf, f.provenance, f.golden)
				}
			default:
				t.Errorf("%s declares provenance %q, want %q or %q", f.pdf, f.provenance, niProvDocling, niProvPDFium)
			}

			if _, err := os.Stat(filepath.Join(fxDir, f.golden)); err != nil {
				t.Errorf("golden %s for %s is declared but not on disk: %v", f.golden, f.pdf, err)
			}
		})
	}

	if _, err := os.Stat(filepath.Join(fxDir, "noninvoice_nosuchgolden.docling.json")); err == nil {
		t.Error("os.Stat on a golden that does not exist returned no error; the existence check above could never report an absence")
	}
}
