// contract_red_test.go: the RED half of the twelve-law contract suite -- deliberately broken
// extractors, one per law, graded by the EXACT set of law ids RunExtractorContract records.
// Nothing in contract_test.go proves the suite REJECTS a bad extractor; this file is what does.
//
// STAGE 1 (this commit) ships the MACHINERY and an EMPTY redCases table. Stage 2 fills the
// table with fifteen rows and adds the per-law TestRedCase_* specs.
//
// HONEST FRAMING (do not relabel any spec below without re-reading this):
//   - Before this file existed the package compiled and all 32 of its tests passed. So the red
//     recorded here is a real assertion failure, not a build failure standing in for one --
//     that is what the empty table buys.
//   - TestAllLaws_EveryLawHasADemonstratedRed is the ONE genuinely red-first spec in this
//     commit. Against the empty table it fails on twelve assertions, one per orphaned law id.
//   - TestContractSuite_RejectsNonConformingExtractors is RED here on a VACUITY GUARD -- zero
//     rows for twelve laws -- and not on any law. It is not a law transition and must not be
//     sold as one. From Stage 2 on it is what drives every broken extractor.
//   - TestContractCorpusNeedsNoRestore is a DESIGN LOCK, never red-first. Its fingerprint half
//     passes VACUOUSLY here: the loop runs zero rows, so it compares two fresh corpora with no
//     extractor in between. Its no-Cleanup scan is live from this commit.
//
// THE FINGERPRINT HALF ONLY DISCRIMINATES IF THE E05 ROW'S WRITE IS NON-INVOLUTIVE. Measured on
// this story: against a memoised newCorpus -- the shared corpus this locks out -- an E05 row
// writing doc.Bytes[0] ^= 0xFF leaves this test GREEN, because the document laws then call
// Extract an even number of times per blob and the XOR undoes itself before the second
// fingerprint is taken. The same test against doc.Bytes[0]++ goes red naming all four
// byte-carrying cases. Stage 2 ships that row; if it is ever made involutive this test becomes
// decoration and nothing else will say so.
//
// It ranges redCases itself rather than depending on running after the table. "After the full
// red table runs" is not a property of a Go test: -run and -shuffle both change ordering, and a
// filtered CI run would make it vacuous. The one slow row is excluded because it costs a full
// cancelledExtractBudget. Stage 2 must keep that row's Extract clear of doc.Bytes, or the
// exclusion silently loses coverage: callExtractCancelled strands that goroutine for the
// binary's life, so a late write through the bytes it still holds would land inside some other
// test entirely.
//
// OVERLAP, stated rather than implied: TestCorpusIsFreshPerLaw (contract_test.go) subsumes the
// fingerprint half FOR CORPUS-ALIASING MUTATIONS, not entirely. It calls newCorpus twice back
// to back with no extractor run between, and compares only length and byte 0, never full
// content, so it structurally cannot see a write that manifests only after an Extract runs. The
// no-Cleanup scan overlaps nothing here: it is the direct lock against the precedent's
// restoreL04Corpus (internal/submission/contract_red_test.go:284) -- M5-02's defect proven
// absent rather than merely designed out.
//
// refExtractor is embedded BY VALUE. There is no *refExtractor: refExtractor is a struct{} with
// value receivers and newRefExtractor returns a value (contract_test.go:216-224). Re-probed on
// this subtask, all three shapes: a value embed works; newRefExtractor().(*refExtractor) panics
// on the interface conversion; and a zero *refExtractor panics on the first promoted call even
// though the type is zero-width, because Go emits the nil check regardless.
//
// Package extraction_test (external). Imports are stdlib plus internal/extraction only
// (deps_test.go scan B sees test imports). No test here skips and no database environment
// variable is named, in code or comment: internal/tools/rlsgate classifies a package as
// DB-gated when one file's raw bytes carry both, and this file needs neither.
package extraction_test

import (
	"crypto/sha256"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// redCase is one deliberately broken extractor and the exact law id set the suite must record.
type redCase struct {
	// lawID is the PRIMARY law this row exists to demonstrate. AllLaws is bound against these
	// and not against the want union, so a row cannot claim a law it merely trips in passing.
	lawID string

	// name distinguishes rows sharing a primary id and names the subtest.
	name string

	// want is the exact recorded set, graded in both directions. A row that trips an extra law
	// is a defect in the row, not a pass.
	want map[string]bool

	// newExtractor is a NAMED package-level factory, never an inline closure: the per-law specs
	// reference the same name, so each broken extractor has one definition rather than two that
	// drift (precedent: internal/submission/contract_red_test.go:634-638).
	newExtractor func() extraction.Extractor

	// slow marks the E12 watchdog row, which costs a full cancelledExtractBudget.
	// TestContractCorpusNeedsNoRestore skips it, which is only sound while that row's Extract
	// never indexes doc.Bytes -- see this file's header.
	slow bool
}

// redCases is EMPTY in Stage 1, which is what makes TestAllLaws_EveryLawHasADemonstratedRed a
// genuine red against a package that already compiles. Stage 2 fills it with fifteen rows:
// E01, E02, E03, E04 twice, E05, E06, E07, E08, E09, E10, E11 twice, E12 twice.
var redCases = []redCase{}

// lawSet builds a want value from a list of ids.
func lawSet(ids ...string) map[string]bool {
	s := make(map[string]bool, len(ids))
	for _, id := range ids {
		s[id] = true
	}
	return s
}

// recordedLawIDs drives one extractor through the whole suite and returns the ids it tripped.
func recordedLawIDs(newExtractor func() extraction.Extractor) map[string]bool {
	rec := &lawRecorder{}
	RunExtractorContract(rec, newExtractor)
	return rec.lawIDs()
}

// assertExactLawIDs grades got against want by set equality in BOTH directions -- containment
// would let a row pass by tripping some other law. Both loops iterate a sorted list, so the
// message order is stable across runs and diffable against a previous CI log.
func assertExactLawIDs(t *testing.T, label string, got, want map[string]bool) {
	t.Helper()
	for _, id := range sortedLawIDs(want) {
		if !got[id] {
			t.Errorf("%s: law %s was not recorded: recorded %v, want exactly %v",
				label, id, sortedLawIDs(got), sortedLawIDs(want))
		}
	}
	for _, id := range sortedLawIDs(got) {
		if !want[id] {
			t.Errorf("%s: law %s was recorded but not wanted: recorded %v, want exactly %v",
				label, id, sortedLawIDs(got), sortedLawIDs(want))
		}
	}
}

// corpusDigest is one corpus case hashed.
type corpusDigest struct {
	name   string
	size   int
	digest [32]byte
}

// corpusFingerprint hashes a freshly built corpus. SHA-256 rather than bytes.Equal over two
// live 15 MiB slices: cheaper in memory, and a digest is diffable in a failure message. A
// slice rather than a map keyed by name, so newCorpus's order is preserved -- the messages
// stay stable and a reordered corpus is caught too.
func corpusFingerprint() []corpusDigest {
	corpus := newCorpus()
	out := make([]corpusDigest, 0, len(corpus))
	for _, c := range corpus {
		out = append(out, corpusDigest{
			name:   c.name,
			size:   len(c.doc.Bytes),
			digest: sha256.Sum256(c.doc.Bytes),
		})
	}
	return out
}

// TestAllLaws_EveryLawHasADemonstratedRed (RED-FIRST -- the one genuine red in this commit):
// AllLaws equals, in both directions, the set of PRIMARY law ids redCases demonstrates. One
// Errorf per orphan, never one combined message that could print an empty and therefore
// misleadingly reassuring list (mirrors contract_test.go's own per-id loops).
//
// It reads the TABLE, not this file's source. A law id written in a comment cannot enter a
// []redCase literal, so the laundering the sibling static scans are exposed to does not apply.
func TestAllLaws_EveryLawHasADemonstratedRed(t *testing.T) {
	demonstrated := make(map[string]bool, len(redCases))
	for _, tc := range redCases {
		demonstrated[tc.lawID] = true
	}
	declared := lawSet(AllLaws...)

	for _, id := range AllLaws {
		if !demonstrated[id] {
			t.Errorf("law %s has no demonstrated RED: no redCases row carries it as its primary id", id)
		}
	}
	for _, id := range sortedLawIDs(demonstrated) {
		if !declared[id] {
			t.Errorf("redCases demonstrates %s but AllLaws does not list it", id)
		}
	}
}

// TestContractSuite_RejectsNonConformingExtractors (RED here on a VACUITY GUARD, not on a law
// -- see this file's header): every row records exactly its want set.
func TestContractSuite_RejectsNonConformingExtractors(t *testing.T) {
	if len(redCases) < len(AllLaws) {
		t.Fatalf("redCases holds %d row(s) for %d laws; the loop below cannot be complete",
			len(redCases), len(AllLaws))
	}

	for _, tc := range redCases {
		t.Run(tc.lawID+"/"+tc.name, func(t *testing.T) {
			// Coherence before grading. A row relabelled to a law it does not demonstrate
			// would otherwise fake the meta-test green; this closes that at the other end.
			if !tc.want[tc.lawID] {
				t.Fatalf("row %s/%s wants %v, which does not include its own primary id",
					tc.lawID, tc.name, sortedLawIDs(tc.want))
			}
			assertExactLawIDs(t, tc.lawID+"/"+tc.name, recordedLawIDs(tc.newExtractor), tc.want)
		})
	}
}

const redSuiteFile = "contract_red_test.go"

// TestContractCorpusNeedsNoRestore (DESIGN LOCK -- see this file's header, including why the
// fingerprint half needs a non-involutive E05 write and why it passes vacuously in this
// commit): running every non-slow row leaves a freshly built corpus byte-identical, by SHA-256
// per case, to one built before the loop; and no function in this file reaches for Cleanup.
func TestContractCorpusNeedsNoRestore(t *testing.T) {
	// The scan runs first so a vacuity Fatalf below can never suppress it.
	assertNoCleanupCalls(t)

	before := corpusFingerprint()
	var carrying int
	for _, d := range before {
		if d.size > 0 {
			carrying++
		}
	}
	if carrying < 3 {
		t.Fatalf("only %d corpus case(s) carry bytes; the comparison below would be near-vacuous", carrying)
	}

	for _, tc := range redCases {
		if tc.slow {
			continue
		}
		RunExtractorContract(&lawRecorder{}, tc.newExtractor)
	}

	after := corpusFingerprint()
	if len(after) != len(before) {
		t.Fatalf("the corpus held %d case(s) before the red table and %d after", len(before), len(after))
	}
	for i := range before {
		switch {
		case before[i].name != after[i].name:
			t.Errorf("corpus case %d is %q before the red table and %q after", i, before[i].name, after[i].name)
		case before[i].size != after[i].size:
			t.Errorf("corpus case %q is %d bytes before the red table and %d after",
				before[i].name, before[i].size, after[i].size)
		case before[i].digest != after[i].digest:
			t.Errorf("corpus case %q: SHA-256 %x before the red table and %x after; a red extractor's write outlived its own row, so the corpus is shared and a restore hook would be needed",
				before[i].name, before[i].digest, after[i].digest)
		}
	}
}

// assertNoCleanupCalls fails when any function in this file reaches for Cleanup. The precedent
// needed restoreL04Corpus (internal/submission/contract_red_test.go:284) because its corpus was
// a package-level var; here newCorpus is a per-law factory, so no row can leak into another and
// no hook is needed. Any Cleanup selector counts, not only a call, so a method value handed to
// a helper is caught too. Mode 0, so comments are not in the AST and this one cannot trip it.
func assertNoCleanupCalls(t *testing.T) {
	t.Helper()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, redSuiteFile, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", redSuiteFile, err)
	}

	var fns int
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		fns++
		ast.Inspect(fn, func(n ast.Node) bool {
			sel, isSel := n.(*ast.SelectorExpr)
			if !isSel || sel.Sel.Name != "Cleanup" {
				return true
			}
			t.Errorf("%s: %s reaches for Cleanup; the corpus is a per-law factory, so no red case needs a restore hook",
				fset.Position(sel.Pos()), fn.Name.Name)
			return true
		})
	}
	if fns == 0 {
		t.Fatalf("parsed 0 function declarations in %s; the scan above would pass vacuously", redSuiteFile)
	}
}
