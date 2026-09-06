// goldens_test.go: the four wild_* arrangements -- production layouts reproduced as generated
// fixtures, their committed goldens, and the guards that keep them out of the corpus ratchets.
// No database.
//
// A wild_ layout is scored by expectByLayout and is NOT a corpus_ layout: it gains no
// corpusExpect, corpusLayouts, corpusTokenFloor or t1aGaps entry.
package endtoend

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const (
	wildTwoParty = "wild_two_party_bare_tin.pdf"
	wildRuled    = "wild_ruled_lines_totals.pdf"
	wildRCNaira  = "wild_rc_due_naira.pdf"
	wildStacked  = "wild_stacked_borderless.pdf"
)

// wildLayouts is hard-coded, never a directory walk: a walk cannot see a fixture that is
// missing, which is the failure Core AC 7 exists to catch.
var wildLayouts = []string{wildTwoParty, wildRuled, wildRCNaira, wildStacked}

// --- the pinned synthetic identifier table ------------------------------------------------
//
// Every literal below is freshly synthesized, never observed. The same values are declared
// beside the builders in ../fixtures_test.go; TestWildLayouts_UseOnlySynthesizedIdentifiers
// holds the two copies together.

var wildTINs = []string{
	"99999999-0801", "99999999-0802", // two_party
	"99999999-0901", "99999999-0902", // ruled_lines_totals
	"99999999-1001", "99999999-1002", // rc_due_naira
	"99999999-1101", "99999999-1102", // stacked_borderless
}

var wildInvNums = []string{"INV-2101", "INV-2102", "INV-2103", "INV-2104"}

const wildRCNumber = "RC-000142"

// wildHeaders is the decorated line-item header row. EXTR-24 recognises these; EXTR-21 only
// commits them, so none may enter liLexicon here.
var wildHeaders = []string{"S/N", "DESCRIPTION OF GOODS", "QTY", "RATE (N)", "Amount ₦"}

// wildLabels are the buyer-block heading and the two fragment-bearing labels. The apostrophe is
// U+2019: fxHelvetica declares no /Encoding, so pdfium reads byte 0x27 under StandardEncoding
// as quoteright.
var wildLabels = []string{"Invoice to", "Customer No.", "Buyer’s Signature"}

var wildCast = []string{"Adeyemi Trading Limited", "Honeywell Group"}

// wildRuledLastLineAmount is the ruled table's last row amount -- the positional candidate
// AC-13 requires the totals identity to reject.
const wildRuledLastLineAmount = "1000.00"

// --- shared helpers ------------------------------------------------------------------------

// wildGolden is a layout's committed docling golden.
func wildGolden(layout string) string {
	return strings.TrimSuffix(layout, ".pdf") + ".docling.json"
}

// wildReadFile reads a path relative to this package directory. An empty file must fail rather
// than read as a clean scan.
func wildReadFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	if len(raw) == 0 {
		t.Fatalf("%s is empty; every scan over it would find nothing and report a clean file", rel)
	}
	return string(raw)
}

// wildPages reads one committed fixture with the production PDFium reader. Zero tokens is a
// fatal: every assertion taken off the page would hold vacuously.
func wildPages(t *testing.T, layout string) []extraction.Page {
	t.Helper()
	eeRequireFixtures(t, []string{layout})

	var pages []extraction.Page
	onPage := func(p extraction.Page) error {
		p.ImagePNG = nil
		pages = append(pages, p)
		return nil
	}
	doc := extraction.Document{Bytes: eeFixtureBytes(t, layout), ContentType: eeContentType}
	if _, err := extraction.NewPDFiumReader().Read(t.Context(), doc, onPage); err != nil {
		t.Fatalf("read %s with the PDFium reader: %v", layout, err)
	}
	if wildTokenCount(pages) == 0 {
		t.Fatalf("%s read 0 token(s); every assertion over its text would hold vacuously", layout)
	}
	return pages
}

func wildTokenCount(pages []extraction.Page) int {
	n := 0
	for _, p := range pages {
		n += len(p.Tokens)
	}
	return n
}

func wildTokenTexts(pages []extraction.Page) []string {
	out := []string{}
	for _, p := range pages {
		for _, tok := range p.Tokens {
			out = append(out, tok.Text)
		}
	}
	return out
}

// wildTokenContaining returns the first token whose text holds want, and whether one exists.
// A trailing space is expected: a token whose baseline continues into another Tj gains one.
func wildTokenContaining(pages []extraction.Page, want string) (extraction.Token, bool) {
	for _, p := range pages {
		for _, tok := range p.Tokens {
			if strings.Contains(tok.Text, want) {
				return tok, true
			}
		}
	}
	return extraction.Token{}, false
}

// wildExpectRow is one wild layout's expectByLayout row. A layout the table does not name is a
// layout the suite does not score, so it fatals here rather than being skipped over.
func wildExpectRow(t *testing.T, layout string) map[string][]string {
	t.Helper()
	for _, want := range expectByLayout {
		if want.file == layout {
			return want.fields
		}
	}
	t.Fatalf("expectByLayout carries no row for %s; the layout is committed but scored by nothing", layout)
	return nil
}

// wildOneValue is the single expected value for a cell, for the arithmetic below.
func wildOneValue(t *testing.T, layout, field string) string {
	t.Helper()
	vals := wildExpectRow(t, layout)[field]
	if len(vals) != 1 {
		t.Fatalf("expectByLayout[%s].%s holds %d value(s) %v, want exactly 1", layout, field, len(vals), vals)
	}
	return vals[0]
}

func wildAmount(t *testing.T, raw string) float64 {
	t.Helper()
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		t.Fatalf("parse %q as an amount: %v", raw, err)
	}
	return f
}

// --- AC-2: the goldens are what the sidecar returned ---------------------------------------

// AC-2. A golden is produced by a built container, never by `go test -update` and never by
// hand. Two clauses: the UseNumber round trip preserves Python's "792.0", which a plain decode
// flattens; and a stub image answers with docling_version "stub", which round-trips perfectly.
func TestWildGoldens_AreMachineGenerated(t *testing.T) {
	if len(wildLayouts) == 0 {
		t.Fatal("wildLayouts is empty; the loop below would assert nothing")
	}
	for _, layout := range wildLayouts {
		golden := wildGolden(layout)
		t.Run(golden, func(t *testing.T) {
			eeRequireFixtures(t, []string{golden})
			raw := eeFixtureBytes(t, golden)

			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.UseNumber()
			var doc any
			if err := dec.Decode(&doc); err != nil {
				t.Fatalf("decode %s: %v", golden, err)
			}
			round, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				t.Fatalf("re-serialise %s: %v", golden, err)
			}
			round = append(round, '\n') // the script's print() writes one; Go's marshal does not
			if !bytes.Equal(round, raw) {
				t.Errorf("%s is not what `docling-canary.sh golden` writes (%d bytes re-serialised, %d committed); regenerate it from a freshly built image rather than editing it",
					golden, len(round), len(raw))
			}

			obj, ok := doc.(map[string]any)
			if !ok {
				t.Fatalf("%s decodes to %T, want a JSON object", golden, doc)
			}
			version, _ := obj["docling_version"].(string)
			if version == "" || version == "stub" {
				t.Errorf("%s reports docling_version %q; it was generated by a stub image, not the pinned sidecar", golden, version)
			}
		})
	}
}

// wildGeometryTol bounds a golden's disagreement with pdfium's read of the same PDF. The
// corpus figure, cwGeometryTol (../corpus_wired_db_test.go:794).
const wildGeometryTol = 0.02

// AC-2. A round trip and a non-stub version notice neither a hand-edited coordinate nor a
// golden replayed from a different document. A golden that no longer describes its PDF is a
// replay of nothing.
func TestWildGoldens_DescribeTheirPDF(t *testing.T) {
	for _, layout := range wildLayouts {
		t.Run(layout, func(t *testing.T) {
			golden := eeGoldenPages(t, wildGolden(layout))
			pdfium := wildPages(t, layout)

			if len(golden) != len(pdfium) {
				t.Fatalf("%s describes %d page(s) and %s reads %d", wildGolden(layout), len(golden), layout, len(pdfium))
			}

			compared := 0
			for i := range golden {
				g, p := golden[i], pdfium[i]
				if g.WidthPt != p.WidthPt || g.HeightPt != p.HeightPt {
					t.Errorf("page %d is %vx%v pt in the golden and %vx%v pt in the PDF", i+1, g.WidthPt, g.HeightPt, p.WidthPt, p.HeightPt)
				}
				if len(g.Tokens) != len(p.Tokens) {
					t.Errorf("page %d carries %d golden token(s) and %d pdfium token(s); the golden was truncated or padded", i+1, len(g.Tokens), len(p.Tokens))
					continue
				}
				for j := range g.Tokens {
					gt, pt := g.Tokens[j], p.Tokens[j]
					compared++
					if strings.TrimSpace(gt.Text) != strings.TrimSpace(pt.Text) {
						t.Errorf("page %d token %d reads %q in the golden and %q in the PDF", i+1, j, gt.Text, pt.Text)
					}
					for _, d := range []struct {
						edge string
						off  float64
					}{
						{"x0", gt.Region.X0 - pt.Region.X0},
						{"x1", gt.Region.X1 - pt.Region.X1},
						{"y0", gt.Region.Y0 - pt.Region.Y0},
						{"y1", gt.Region.Y1 - pt.Region.Y1},
					} {
						if math.Abs(d.off) > wildGeometryTol {
							t.Errorf("page %d token %d (%q) has %s %v off pdfium's; the golden no longer describes this PDF", i+1, j, gt.Text, d.edge, d.off)
						}
					}
				}
			}
			if compared == 0 {
				t.Fatalf("%s compared no token; the geometry assertions above held over nothing", wildGolden(layout))
			}
		})
	}
}

// --- AC-3: CI wiring ------------------------------------------------------------------------

const (
	wildCIFile      = "../../../.github/workflows/ci.yml"
	wildTestdataRef = "internal/extraction/testdata/"
	wildCanaryRef   = "scripts/ci/docling-canary.sh"

	// wildCIPathFloor is the changes-filter's committed literal path count. This scan asserts
	// an ABSENCE, so a ci.yml that stopped naming testdata at all must fail here first.
	wildCIPathFloor = 20
)

// AC-3. An unwired fixture skips docling-canary, and the roll-up job counts a skipped job as a
// pass. Two occurrences per path: the `sidecar:` changes-filter entry and the golden step.
func TestWildGoldens_EveryNewFixtureIsWiredIntoTheCanaryJob(t *testing.T) {
	yaml := wildReadFile(t, wildCIFile)

	// Control needles: a ci.yml this scan cannot read reports every path unwired for the
	// wrong reason, and one that names no testdata path at all reads clean on a shrunken list.
	if !strings.Contains(yaml, wildCanaryRef) {
		t.Fatalf("%s names no %s step; the occurrence counts below are not about the canary job", wildCIFile, wildCanaryRef)
	}
	if n := strings.Count(yaml, wildTestdataRef); n < wildCIPathFloor {
		t.Fatalf("%s names %d %s path(s), want at least %d; the wiring list shrank", wildCIFile, n, wildTestdataRef, wildCIPathFloor)
	}

	for _, layout := range wildLayouts {
		for _, name := range []string{layout, wildGolden(layout)} {
			path := wildTestdataRef + name
			if n := strings.Count(yaml, path); n < 2 {
				t.Errorf("%s names %s %d time(s), want at least 2 -- the changes-filter entry and the golden step", wildCIFile, path, n)
			}
		}
	}
}

// --- AC-4: the corpus ratchets stay closed ---------------------------------------------------

// wildRatchets are the four collections a wild_ layout may never enter, each with the number of
// corpus_ entries it carries today. The floor is the control needle: a scan that stopped
// finding the collection reads clean.
var wildRatchets = []struct {
	file, decl string
	minCorpus  int
}{
	{"../corpus_test.go", "var corpusLayouts = ", 6},
	{"../corpus_test.go", "var corpusExpect = ", 6},
	{"../corpus_adversarial_test.go", "var corpusTokenFloor = ", 6},
	{"../tier1_adversarial_test.go", "var t1aGaps = ", 1},
}

// wildTier1Pins are the Tier-1 numbers a fifth corpus layout would move. Regexes, not literals:
// the const block's alignment is gofmt's, not the pin's.
var wildTier1Pins = []*regexp.Regexp{
	regexp.MustCompile(`tier1RecallHits\s*=\s*43\b`),
	regexp.MustCompile(`tier1RecallPairs\s*=\s*44\b`),
	regexp.MustCompile(`tier1DecisionHits\s*=\s*43\b`),
	regexp.MustCompile(`tier1DecisionPairs\s*=\s*44\b`),
}

const wildAccuracyFile = "../accuracy_test.go"

// wildVarBody is decl's body, up to the first line that is a lone closing brace. The
// collections are unexported in package extraction_test and unreachable from here, so this is
// a source scan.
func wildVarBody(t *testing.T, src, decl, file string) string {
	t.Helper()
	i := strings.Index(src, decl)
	if i < 0 {
		t.Fatalf("%s declares no %q; this scan asserts an absence and would report clean on a collection it never found", file, decl)
	}
	rest := src[i:]
	j := strings.Index(rest, "\n}\n")
	if j < 0 {
		t.Fatalf("%s's %q has no closing brace at column 0; the body below would run to end of file", file, decl)
	}
	return rest[:j]
}

// AC-4. The wild layouts are scored by expectByLayout and by nothing in internal/extraction:
// a wild_ entry in any of the four ratchets moves a pinned Tier-1 number silently.
func TestWildLayouts_DoNotEnterTheCorpusRatchets(t *testing.T) {
	for _, r := range wildRatchets {
		body := wildVarBody(t, wildReadFile(t, r.file), r.decl, r.file)
		if n := strings.Count(body, "corpus_"); n < r.minCorpus {
			t.Fatalf("%s's %q names %d corpus_ entr(ies), want at least %d; the scan is not reading the collection", r.file, r.decl, n, r.minCorpus)
		}
		if strings.Contains(body, "wild_") {
			t.Errorf("%s's %q names a wild_ layout; a wild layout in a corpus ratchet moves the Tier-1 denominators", r.file, r.decl)
		}
	}

	accuracy := wildReadFile(t, wildAccuracyFile)
	for _, re := range wildTier1Pins {
		if !re.MatchString(accuracy) {
			t.Errorf("%s no longer carries %s; the Tier-1 corpus must still read 43/44 on both rates", wildAccuracyFile, re)
		}
	}
}

// --- AC-5: TINs ------------------------------------------------------------------------------

// wildTINRE is any TIN-shaped run, deliberately wider than the reserved block so a fixture that
// left it is found rather than filtered out.
var wildTINRE = regexp.MustCompile(`[0-9]{8}-[0-9]{4}`)

const wildFreeTINPrefix = "99999999-"

// wildScriptedTINs are the submission mock's scripted outcomes and its never-allocate pair.
var wildScriptedTINs = []string{"0001", "0002", "0003", "0004", "0005", "0006", "0007", "0008", "0009"}

// AC-5. Every TIN on a reproduction is synthesized from the free reserved block. A TIN carried
// over from a production document, or one that collides with a scripted submission outcome,
// fails here rather than shipping.
func TestWildLayouts_UseOnlyFreeReservedTINs(t *testing.T) {
	for _, layout := range wildLayouts {
		t.Run(layout, func(t *testing.T) {
			var hits []string
			for _, text := range wildTokenTexts(wildPages(t, layout)) {
				hits = append(hits, wildTINRE.FindAllString(text, -1)...)
			}
			if len(hits) == 0 {
				t.Fatalf("%s carries no TIN-shaped run; this scan asserts an absence and found nothing to assert over", layout)
			}
			for _, tin := range hits {
				if !strings.HasPrefix(tin, wildFreeTINPrefix) {
					t.Errorf("%s carries TIN %q, outside the free reserved block %s", layout, tin, wildFreeTINPrefix)
					continue
				}
				if suffix := strings.TrimPrefix(tin, wildFreeTINPrefix); slices.Contains(wildScriptedTINs, suffix) {
					t.Errorf("%s carries TIN %q; -0001..-0009 are the submission mock's scripted and never-allocate suffixes", layout, tin)
				}
				if !slices.Contains(wildTINs, tin) {
					t.Errorf("%s carries TIN %q, which the pinned synthetic table does not name", layout, tin)
				}
			}
		})
	}
}

// --- AC-6: the layouts are scored, and their bytes carry what the table claims ----------------

// AC-6. Every wild layout carries all 8 written fields, and every value the table names is
// produced by the field's own shape from the layout's own bytes. A row naming a value the bytes
// do not carry would be a miss extraction can never win.
func TestWildLayouts_EveryExpectedValueAppearsInItsFixture(t *testing.T) {
	for _, layout := range wildLayouts {
		t.Run(layout, func(t *testing.T) {
			fields := wildExpectRow(t, layout)
			for _, field := range writtenFields {
				vals, ok := fields[field]
				if !ok {
					t.Errorf("expectByLayout[%s] has no %s key; a missing key drops the cell from the denominator", layout, field)
					continue
				}
				if len(vals) == 0 {
					t.Errorf("expectByLayout[%s].%s is empty; every wild cell carries a value on its page, so an empty list here is an absent cell that was never designed", layout, field)
					continue
				}
				readings := eePageTokenReadings(t, layout, field)
				for _, want := range vals {
					if !slices.Contains(readings, want) {
						t.Errorf("expectByLayout[%s].%s wants %q, which %s's own bytes do not produce under its shape; readings were %v", layout, field, want, layout, readings)
					}
				}
			}
		})
	}
}

// wildTokenFloor is the token count each wild layout read at when it was committed. A floor,
// not an equality. MEASURE these off the committed fixtures; 0 is unpinned and fails below.
var wildTokenFloor = map[string]int{
	wildTwoParty: 0,
	wildRuled:    0,
	wildRCNaira:  0,
	wildStacked:  0,
}

// wildMinTokenFloor is the smallest committed corpus floor (corpus_ambiguous_date.pdf, 6). A
// floor of 1 would be satisfied by a fixture that printed almost nothing.
const wildMinTokenFloor = 6

// AC-6. The floor table names exactly the wild layouts in both directions, and each floor is
// non-vacuous: an emptied fixture must fail rather than read clean.
func TestWildLayouts_HaveATokenFloor(t *testing.T) {
	for _, layout := range wildLayouts {
		floor, ok := wildTokenFloor[layout]
		if !ok {
			t.Errorf("wildTokenFloor names no floor for %s; an emptied fixture would read clean", layout)
			continue
		}
		if floor < wildMinTokenFloor {
			t.Errorf("wildTokenFloor[%s] = %d, want at least %d -- measure it off the committed fixture", layout, floor, wildMinTokenFloor)
		}
	}
	for name := range wildTokenFloor {
		if !slices.Contains(wildLayouts, name) {
			t.Errorf("wildTokenFloor names %s, which is not a wild layout", name)
		}
	}

	for _, layout := range wildLayouts {
		t.Run(layout, func(t *testing.T) {
			if got := wildTokenCount(wildPages(t, layout)); got < wildTokenFloor[layout] {
				t.Errorf("%s reads %d token(s), below its committed floor of %d", layout, got, wildTokenFloor[layout])
			}
		})
	}
}

// --- AC-7: no identifier carried over from a production document ------------------------------

// The two identifier shapes on these reproductions. Deliberately narrow: reInvNum
// (../shapes.go:27) accepts every ordinary English word, so running every word-run through
// ShapeInvoiceNumber would demand the pinned set contain the whole text of every fixture.
var (
	wildRCRE     = regexp.MustCompile(`^RC-[0-9]{6}$`)
	wildMintedRE = regexp.MustCompile(`^[A-Z]{2,4}-[0-9]{4,}$`)
)

const wildFixturesFile = "../fixtures_test.go"

// AC-7. Every identifier on a reproduction comes from the pinned synthetic table declared
// beside the builders, so one carried over from a production document fails rather than
// shipping. The invoice-number pattern is extraction's own, not a new one.
func TestWildLayouts_UseOnlySynthesizedIdentifiers(t *testing.T) {
	if len(wildInvNums) == 0 || len(wildHeaders) == 0 || len(wildLabels) == 0 || len(wildCast) == 0 {
		t.Fatal("the pinned synthetic table is empty; every membership check below would be vacuous")
	}

	// Each minted number satisfies extraction's own invoice-number pattern, reused rather than
	// reinvented here.
	for _, num := range wildInvNums {
		if got := extraction.ShapeInvoiceNumber.Normalize(num); !slices.Contains(got, num) {
			t.Errorf("ShapeInvoiceNumber.Normalize(%q) = %v; a minted number extraction cannot read is not a reproduction of anything", num, got)
		}
	}

	// The table is declared beside the builders, not only here: two copies that drift would
	// let a fixture be generated from an identifier this scan never sees.
	src := wildReadFile(t, wildFixturesFile)
	pinned := append([]string{wildRCNumber}, wildInvNums...)
	pinned = append(pinned, wildTINs...)
	pinned = append(pinned, wildCast...)
	for _, id := range pinned {
		if !strings.Contains(src, id) {
			t.Errorf("%s does not name %q; the pinned table here and the builders' have drifted apart", wildFixturesFile, id)
		}
	}

	var rcHits, mintedHits int
	for _, layout := range wildLayouts {
		t.Run(layout, func(t *testing.T) {
			for _, text := range wildTokenTexts(wildPages(t, layout)) {
				for _, word := range strings.Fields(text) {
					word = strings.TrimSuffix(word, ":")
					switch {
					case wildRCRE.MatchString(word):
						rcHits++
					case wildMintedRE.MatchString(word):
						mintedHits++
					default:
						continue
					}
					if !slices.Contains(pinned, word) {
						t.Errorf("%s carries identifier %q, which the pinned synthetic table does not name", layout, word)
					}
				}
			}
		})
	}

	// Floor: this scan asserts an absence, and a corpus carrying no identifier at all reads
	// exactly like one carrying only synthesized ones.
	if rcHits == 0 {
		t.Errorf("no wild layout carries an RC-shaped identifier; %s was never reached", wildRCNumber)
	}
	if mintedHits < len(wildInvNums) {
		t.Errorf("the wild layouts carry %d minted identifier(s), want at least %d -- one per layout", mintedHits, len(wildInvNums))
	}
}

// --- AC-8: the require list is complete against the committed tree ----------------------------

// wildRequireListPin is the layout count the two require-lists must name once the wild layouts
// land. Hard-coded, not derived from eeLayoutCount: a list and a denominator that shrink
// together pass every ratio they feed.
const wildRequireListPin = 10

// AC-8. A fixture the require-list does not name cannot fatal when absent, which is how the
// suite silently stops scoring a layout. The list can see a missing file; the tree walk sees a
// list that quietly shrank below what is committed.
func TestEndToEnd_TheRequireListIsCompleteAgainstTheCommittedTree(t *testing.T) {
	if len(requiredPDFs) != wildRequireListPin || len(requiredGoldens) != wildRequireListPin {
		t.Fatalf("requiredPDFs=%d requiredGoldens=%d, want %d each -- every committed corpus_ and wild_ layout must be named",
			len(requiredPDFs), len(requiredGoldens), wildRequireListPin)
	}

	entries, err := os.ReadDir(eeFxDir)
	if err != nil {
		t.Fatalf("read %s: %v", eeFxDir, err)
	}
	var pdfs, goldens []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !(strings.HasPrefix(name, "corpus_") || strings.HasPrefix(name, "wild_")) {
			continue
		}
		switch {
		case strings.HasSuffix(name, ".docling.json"):
			goldens = append(goldens, name)
		case strings.HasSuffix(name, ".pdf"):
			pdfs = append(pdfs, name)
		}
	}

	// Floor: without it a shrunken list plus a lowered pin passes vacuously.
	if len(pdfs) < wildRequireListPin || len(goldens) < wildRequireListPin {
		t.Fatalf("%s holds %d scored .pdf and %d scored .docling.json, want at least %d each -- the walk is reading the wrong directory or the corpus shrank",
			eeFxDir, len(pdfs), len(goldens), wildRequireListPin)
	}

	for _, name := range pdfs {
		if !slices.Contains(requiredPDFs, name) {
			t.Errorf("%s is committed but absent from requiredPDFs; the suite would silently stop scoring it", name)
		}
	}
	for _, name := range goldens {
		if !slices.Contains(requiredGoldens, name) {
			t.Errorf("%s is committed but absent from requiredGoldens; an absent golden could not fatal", name)
		}
	}
}

// --- AC-10: the decorated header row --------------------------------------------------------

// wildNairaBytes is U+20A6 in UTF-8. Asserted on the bytes: the /ToUnicode-less variant reads
// the same glyph back as U+00A4, which prints indistinguishably in a failure message.
var wildNairaBytes = []byte{0xe2, 0x82, 0xa6}

// AC-10. The five decorated column headers are on the page, read back out of the committed
// bytes rather than trusted from the builder. EXTR-24 is graded against these strings, so an
// arrangement feature no test asserts silently rots.
func TestWildLayouts_TheRuledTableCarriesItsDecoratedHeaders(t *testing.T) {
	pages := wildPages(t, wildRuled)
	texts := wildTokenTexts(pages)

	for _, header := range wildHeaders {
		if _, ok := wildTokenContaining(pages, header); !ok {
			t.Errorf("%s carries no token holding %q; tokens were %v", wildRuled, header, texts)
		}
	}

	tok, ok := wildTokenContaining(pages, "Amount")
	if !ok {
		t.Fatalf("%s carries no Amount header token at all", wildRuled)
	}
	if !bytes.Contains([]byte(tok.Text), wildNairaBytes) {
		t.Errorf("%s's Amount header reads %q (% x); the naira must be U+20A6, so the /ToUnicode builder variant is missing", wildRuled, tok.Text, tok.Text)
	}
}

// AC-10. The decoration must be reachable at the reader too: EXTR-24 reads header cells off
// Page.Tables, which PDFiumReader leaves nil.
func TestWildGoldens_TheRuledTableGoldenCarriesTheHeaderRow(t *testing.T) {
	inspected := 0
	for _, layout := range wildLayouts {
		pages := eeGoldenPages(t, wildGolden(layout))
		inspected++

		var row0 []string
		for _, p := range pages {
			for _, tbl := range p.Tables {
				for _, cell := range tbl.Cells {
					if cell.Row == 0 {
						row0 = append(row0, cell.Text)
					}
				}
			}
		}
		if layout != wildRuled {
			continue
		}
		if len(row0) == 0 {
			t.Errorf("%s's golden carries no table header row; docling did not detect the 5-column ruled table, so EXTR-24 has no reader-side oracle", layout)
			continue
		}
		for _, header := range wildHeaders {
			if !slices.ContainsFunc(row0, func(s string) bool { return strings.Contains(s, header) }) {
				t.Errorf("%s's golden header row is %v; it does not carry %q", layout, row0, header)
			}
		}
	}
	// Control needle: a walk that replayed nothing would report every golden clean.
	if inspected != len(wildLayouts) {
		t.Errorf("inspected %d golden(s), want %d", inspected, len(wildLayouts))
	}
}

// --- AC-11: this story fixes nothing ----------------------------------------------------------

// wildLiLexiconKeys is liLexicon's shipped key set, measured at ../lineitems.go:127-141.
// Recognising a decorated header is EXTR-24's; EXTR-21 only commits the arrangement.
var wildLiLexiconKeys = []string{
	"description", "item", "details", "particulars",
	"qty", "quantity", "unit price", "rate", "price",
	"line total", "total", "amount",
}

const wildLexiconFile = "../lineitems.go"

var wildMapKeyRE = regexp.MustCompile(`"([^"]*)":`)

// AC-11. liLexicon is unedited and holds none of the five decorated headers, normalised or not.
func TestWildLayouts_TheHeaderLexiconIsNotEdited(t *testing.T) {
	body := wildVarBody(t, wildReadFile(t, wildLexiconFile), "var liLexicon = ", wildLexiconFile)

	var keys []string
	for _, m := range wildMapKeyRE.FindAllStringSubmatch(body, -1) {
		keys = append(keys, m[1])
	}
	// Control needle: an empty key set satisfies every absence clause below.
	if len(keys) == 0 {
		t.Fatalf("%s's liLexicon parsed to 0 key(s); the scan is not reading the map literal", wildLexiconFile)
	}

	slices.Sort(keys)
	want := slices.Clone(wildLiLexiconKeys)
	slices.Sort(want)
	if !slices.Equal(keys, want) {
		t.Errorf("liLexicon holds %v, want %v -- this story commits the arrangement and changes no extraction rule", keys, want)
	}
	for _, header := range wildHeaders {
		for _, form := range []string{header, strings.ToLower(header)} {
			// "qty" is already shipped; the equality above is what pins it unmoved. Only a
			// NEW key is this story overstepping.
			if slices.Contains(wildLiLexiconKeys, form) {
				continue
			}
			if slices.Contains(keys, form) {
				t.Errorf("liLexicon holds %q; recognising a decorated header is EXTR-24's, not this story's", form)
			}
		}
	}
}

// --- AC-12: the buyer heading and the label fragments ------------------------------------------

// AC-12. The buyer block's heading and the two fragment-bearing labels are on the page, and the
// buyer name sits between them -- the arrangement EXTR-22 is graded against. Y grows downward.
func TestWildLayouts_TheTwoPartyFixtureCarriesTheBuyerHeadingAndFragments(t *testing.T) {
	pages := wildPages(t, wildTwoParty)
	texts := wildTokenTexts(pages)

	for _, label := range wildLabels {
		if _, ok := wildTokenContaining(pages, label); !ok {
			t.Errorf("%s carries no token holding %q; tokens were %v", wildTwoParty, label, texts)
		}
	}

	above, okA := wildTokenContaining(pages, "Customer No.")
	name, okN := wildTokenContaining(pages, "Honeywell Group")
	below, okB := wildTokenContaining(pages, "Buyer’s Signature")
	if !okA || !okN || !okB {
		t.Fatalf("%s: Customer No.=%v buyer name=%v Buyer's Signature=%v; the ordering below cannot be asserted", wildTwoParty, okA, okN, okB)
	}
	if !(above.Region.Y0 < name.Region.Y0 && name.Region.Y0 < below.Region.Y0) {
		t.Errorf("%s prints Customer No. at y0 %v, the buyer name at %v and Buyer's Signature at %v; the fragments must sit above and below the name for either to compete with it",
			wildTwoParty, above.Region.Y0, name.Region.Y0, below.Region.Y0)
	}
}

// --- AC-13: the totals discriminate -------------------------------------------------------------

// wildTotalsTol is the kobo the printed identity must hold to.
const wildTotalsTol = 0.01

// AC-13. subtotal + vat equals the printed total and does NOT equal the competing last-row line
// amount. A fixture where both candidates balance cannot tell a corroborated pick from a
// positional one, and EXTR-23's tie-break would be graded against an oracle that proves nothing.
func TestWildLayouts_TheRuledTableTotalsDiscriminate(t *testing.T) {
	subtotal := wildAmount(t, wildOneValue(t, wildRuled, "subtotal"))
	vat := wildAmount(t, wildOneValue(t, wildRuled, "vat"))
	total := wildAmount(t, wildOneValue(t, wildRuled, "total"))
	line := wildAmount(t, wildRuledLastLineAmount)

	if math.Abs(subtotal+vat-total) > wildTotalsTol {
		t.Errorf("%s prints %v + %v = %v, not the printed total %v; the corroborating identity does not hold", wildRuled, subtotal, vat, subtotal+vat, total)
	}
	if math.Abs(subtotal+vat-line) <= wildTotalsTol {
		t.Errorf("%s's last line amount %v also satisfies subtotal + vat; the fixture cannot discriminate a corroborated total from a positional one", wildRuled, line)
	}

	// The competing candidate must be on the page, or there is nothing for the totals to be
	// picked over.
	if readings := eePageTokenReadings(t, wildRuled, "total"); !slices.Contains(readings, wildRuledLastLineAmount) {
		t.Errorf("%s does not print the competing line amount %s; readings were %v", wildRuled, wildRuledLastLineAmount, readings)
	}
}

// --- AC-14: the naira's two roles stay apart -----------------------------------------------------

const wildCurrencyLabel = "Currency"

// AC-14. On wild_ruled_lines_totals the naira is decoration glued to a column header and the
// currency is sourced from an explicit label; on wild_rc_due_naira it is the currency marker and
// there is no label at all. EXTR-24's Out of Scope forbids conflating the two.
func TestWildLayouts_TheNairaRolesStaySeparate(t *testing.T) {
	ruled := wildPages(t, wildRuled)

	label, ok := wildTokenContaining(ruled, wildCurrencyLabel)
	if !ok {
		t.Fatalf("%s carries no %s label; its currency would have to be sourced from the Amount header's decoration", wildRuled, wildCurrencyLabel)
	}
	var fromLabel []string
	for _, gram := range eeNGrams(label.Text) {
		fromLabel = append(fromLabel, extraction.ShapeCurrency.Normalize(gram)...)
	}
	if !slices.Contains(fromLabel, "NGN") {
		t.Errorf("%s's %q token yields %v under ShapeCurrency; NGN must come from the label", wildRuled, label.Text, fromLabel)
	}
	if got := wildExpectRow(t, wildRuled)["currency"]; !slices.Equal(got, []string{"NGN"}) {
		t.Errorf("expectByLayout[%s].currency = %v, want [NGN] sourced from the label", wildRuled, got)
	}

	// The header's naira is decoration: the whole token is not a currency reading.
	header, ok := wildTokenContaining(ruled, "Amount")
	if !ok {
		t.Fatalf("%s carries no Amount header token", wildRuled)
	}
	if got := extraction.ShapeCurrency.Normalize(header.Text); len(got) != 0 {
		t.Errorf("%s's header token %q reads as currency %v; the naira there is decoration, not a marker", wildRuled, header.Text, got)
	}

	rc := wildPages(t, wildRCNaira)
	if tok, ok := wildTokenContaining(rc, wildCurrencyLabel); ok {
		t.Errorf("%s carries a %s label (%q); its currency must be reachable from the naira marker alone", wildRCNaira, wildCurrencyLabel, tok.Text)
	}
	marked := 0
	for _, text := range wildTokenTexts(rc) {
		if bytes.Contains([]byte(text), wildNairaBytes) {
			marked++
		}
	}
	if marked == 0 {
		t.Errorf("%s carries no U+20A6 token; the currency-marker role is not reproduced", wildRCNaira)
	}
	if readings := eePageTokenReadings(t, wildRCNaira, "currency"); !slices.Contains(readings, "NGN") {
		t.Errorf("%s does not reach NGN under ShapeCurrency; the miss would be an absent cell rather than a real one, readings were %v", wildRCNaira, readings)
	}
}
