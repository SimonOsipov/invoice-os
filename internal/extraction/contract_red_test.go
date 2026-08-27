// contract_red_test.go: the RED half of the twelve-law contract suite -- twenty deliberately
// broken extractors, graded by the EXACT set of law ids RunExtractorContract records. Nothing
// in contract_test.go proves the suite REJECTS a bad extractor; this file is what does.
//
// HONEST FRAMING (do not relabel any spec below without re-reading this):
//   - TestAllLaws_EveryLawHasADemonstratedRed was the ONE genuinely red-first spec here.
//     Against the empty table an earlier commit shipped, it failed on twelve assertions, one
//     per orphaned law id. It is green now because the table carries all twelve.
//   - TestContractSuite_RejectsNonConformingExtractors was red in that commit on a VACUITY
//     GUARD -- zero rows for twelve laws -- and not on any law. That is not a law transition
//     and must not be sold as one. From here on it is what drives all twenty rows.
//   - The six TestRedCase_* specs are EXECUTABLE PROOF, green from their first run, and each
//     is REDUNDANT with a table row: same factory, same want, same grading helper. Measured --
//     no mutation moves a named spec without also moving its row. They buy a name in the test
//     output and nothing else, so do not count them twice.
//   - TestContractCorpusNeedsNoRestore is a DESIGN LOCK, never red-first.
//
// ONE ROW PER EMISSION SITE, measured rather than assumed. The runner holds 20 law-prefixed
// Errorf sites. Each was disabled in turn, as an if false && guard rather than a deletion:
// deleting a block orphans a local and fails to COMPILE, and a build failure greps identically
// to nothing having gone red. All 20 moved EXACTLY ONE row and nothing else, and
// TestAllLaws_IdsAreUniqueAndUsed stayed GREEN throughout: a static scan cannot tell a live law
// from dead code, which is the limit contract_test.go:24-29 states and this file closes.
//
// A LAW ID SET CANNOT SEE HALF A LAW GO MISSING. That is why E01, E02, E03, E11 and E12 carry a
// row per site rather than one row tripping a pair: before the split, disabling either half of
// a pair alone left the WHOLE REPOSITORY GREEN. It cannot see the WATCHDOG site at all --
// disable contract_test.go:505 and callExtractCancelled hands back (nil, nil), so the nil-error
// site records E12 in its place and the id set is identical either way. That one row is graded
// on the recorded TEXT as well, through redCase.wantMessage.
//
// THE FINGERPRINT HALF ONLY DISCRIMINATES IF THE E05 ROW'S WRITE IS NON-INVOLUTIVE, re-measured
// on this commit against a memoised newCorpus -- the shared corpus this locks out. The shipped
// doc.Bytes[0]++ goes RED naming all four byte-carrying cases; doc.Bytes[0] ^= 0xFF leaves it
// GREEN, because a shared blob takes eight live Extract calls and the XOR undoes itself before
// the second fingerprint. Make that write involutive and this test becomes decoration.
//
// It ranges redCases itself rather than depending on running after the table. "After the full
// red table runs" is not a property of a Go test: -run and -shuffle both change ordering, and a
// filtered CI run would make it vacuous. The one slow row is excluded because it costs a full
// cancelledExtractBudget, which is sound only while that row never writes through doc.Bytes:
// callExtractCancelled strands its goroutine past the fingerprint, and a deliberate late write
// there is caught by nothing, go test -race included. assertSlowRowReadsBytesOnlyForLength is
// that claim made executable, because a source scan is the only oracle there can be.
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
// value receivers and newRefExtractor returns a value (contract_test.go:216-224). Re-probed by
// COMPILING all three shapes on this subtask: a value embed works; newRefExtractor().(*refExtractor)
// panics on the interface conversion; and a zero *refExtractor satisfies the interface, returns
// fine from an OVERRIDDEN method, and panics on the first PROMOTED one even though the type is
// zero-width, because Go emits the nil check regardless. That last shape gets no compiler help.
//
// Package extraction_test (external). Imports are stdlib plus internal/extraction only
// (deps_test.go scan B sees test imports). No test here skips and no database environment
// variable is named, in code or comment: internal/tools/rlsgate classifies a package as
// DB-gated when one file's raw bytes carry both, and this file needs neither.
package extraction_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

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

	// wantMessage, when set, is a substring exactly one recorded message must carry. Set
	// equality grades law ids, and for the watchdog site that is not enough: with :505 disabled
	// the timeout path returns (nil, nil) and the nil-error site records E12 in its place, so
	// the id set is identical either way. Only the text tells the two apart -- measured.
	wantMessage string

	// slow marks the E12 watchdog row, which costs a full cancelledExtractBudget.
	// TestContractCorpusNeedsNoRestore skips it and assertSlowRowReadsBytesOnlyForLength keeps
	// that skip sound.
	slow bool
}

// Every broken extractor below embeds refExtractor BY VALUE and overrides the least it can, so
// whatever the suite records is attributable to the override and not to the scaffolding.

const redValue = "RED-0001"

// errRedUnreadable is the lawful error for a document an extractor cannot read.
var errRedUnreadable = errors.New("red extractor: document unreadable")

// lawfulFields is the reference result: one named field, a non-empty value, a normalised
// region, a declared reason. Each row's defect is a single departure from this. A fresh Value
// pointer per call, matching refExtractor.
func lawfulFields() []extraction.Field {
	value := redValue
	return []extraction.Field{{
		Name:   "invoice_number",
		Value:  &value,
		Region: &extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.40, Y1: 0.20},
		Reason: extraction.ReasonNone,
	}}
}

// redFields carries every row whose defect is in the RESULT. It reads nothing out of doc and
// honours cancellation, so build is the only variable and no such row can trip E05 or E12.
type redFields struct {
	refExtractor
	build func() []extraction.Field
}

func (e redFields) Extract(ctx context.Context, _ extraction.Document) ([]extraction.Field, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return e.build(), nil
}

// E01 owns two sites and one extractor cannot isolate both, so it gets a row each. emptyName
// keeps Name() constant, reaching the emptiness site alone; driftingName keeps it non-empty,
// reaching the stability site alone. E03 stays clear for both: the runner compares each
// instance's FIRST call (contract_test.go:369), and both shapes agree there.
type emptyName struct{ refExtractor }

func newEmptyName() extraction.Extractor { return emptyName{} }

func (emptyName) Name() string { return "" }

type driftingName struct {
	refExtractor
	calls int
}

func newDriftingName() extraction.Extractor { return &driftingName{} }

func (e *driftingName) Name() string {
	e.calls++
	return fmt.Sprintf("drifted-%d", e.calls)
}

// E02's twins of the pair above, on Version().
type emptyVersion struct{ refExtractor }

func newEmptyVersion() extraction.Extractor { return emptyVersion{} }

func (emptyVersion) Version() string { return "" }

type driftingVersion struct {
	refExtractor
	calls int
}

func newDriftingVersion() extraction.Extractor { return &driftingVersion{} }

func (e *driftingVersion) Version() string {
	e.calls++
	return fmt.Sprintf("drifted-%d", e.calls)
}

// E03 splits the same way, one row per site: each type stamps ONE of Name and Version with a
// sequence number, so two instances disagree on that one and inherit the other. Each instance
// stays stable across its own calls, so E01 and E02 stay clear.
type instanceStampedName struct {
	refExtractor
	seq int
}

// The counter is PER FACTORY. Per instance it would trip E01 instead; process-wide it would
// leak across rows and across repeat runs of one row.
func newInstanceStampedName() func() extraction.Extractor {
	seq := 0
	return func() extraction.Extractor {
		seq++
		return instanceStampedName{seq: seq}
	}
}

func (e instanceStampedName) Name() string { return fmt.Sprintf("stamped-name-%d", e.seq) }

type instanceStampedVersion struct {
	refExtractor
	seq int
}

func newInstanceStampedVersion() func() extraction.Extractor {
	seq := 0
	return func() extraction.Extractor {
		seq++
		return instanceStampedVersion{seq: seq}
	}
}

func (e instanceStampedVersion) Version() string { return fmt.Sprintf("stamped-version-%d", e.seq) }

// E04, success half: a nil slice alongside a nil error -- the []T to JSON null hazard.
func newNilSliceOnSuccess() extraction.Extractor { return redFields{build: nilSliceOnSuccess} }

func nilSliceOnSuccess() []extraction.Field { return nil }

// fieldsWithError is E04's error half: a non-nil slice smuggled out alongside an error. It
// takes doc as a blank so it cannot touch doc.Bytes -- E05 does not skip a live-context error,
// so a row that both errored and mutated would record E04 and E05 and fail the exact-set
// binding. This row is what keeps the error arm alive: with it deleted the success-half spec
// stays green.
type fieldsWithError struct{ refExtractor }

func newFieldsWithError() extraction.Extractor { return fieldsWithError{} }

func (fieldsWithError) Extract(ctx context.Context, _ extraction.Document) ([]extraction.Field, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return lawfulFields(), errRedUnreadable
}

// bytesMutating writes through the caller's bytes. The write is NON-INVOLUTIVE on purpose --
// see this file's header; an XOR leaves TestContractCorpusNeedsNoRestore green against the
// shared corpus it exists to lock out. The length guard is required: two corpus cases carry
// zero bytes and doc.Bytes[0] would panic.
type bytesMutating struct{ refExtractor }

func newBytesMutating() extraction.Extractor { return bytesMutating{} }

func (bytesMutating) Extract(ctx context.Context, doc extraction.Document) ([]extraction.Field, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(doc.Bytes) > 0 {
		doc.Bytes[0]++
	}
	return lawfulFields(), nil
}

// E06: one field with an empty Name beside a lawful one. E07 skips empty names by design, so
// this stays a singleton.
func newEmptyFieldName() extraction.Extractor { return redFields{build: emptyFieldName} }

func emptyFieldName() []extraction.Field {
	return append(lawfulFields(), extraction.Field{Reason: extraction.ReasonNone})
}

// E07: two fields sharing a Name, each otherwise lawful.
func newDuplicateFieldNames() extraction.Extractor { return redFields{build: duplicateFieldNames} }

func duplicateFieldNames() []extraction.Field {
	second := lawfulFields()[0]
	value := "RED-0002"
	second.Value = &value
	return append(lawfulFields(), second)
}

// E08: a non-nil Value pointing at an empty string. Region is nil, so E11 skips the field.
func newEmptyValuePointer() extraction.Extractor { return redFields{build: emptyValuePointer} }

func emptyValuePointer() []extraction.Field {
	value := ""
	return []extraction.Field{{Name: "invoice_number", Value: &value, Reason: extraction.ReasonNone}}
}

// E09: a Reason outside the five contract_test.go writes out. Not ReasonMissing, so E10 stays
// clear.
func newUndeclaredReason() extraction.Extractor { return redFields{build: undeclaredReason} }

func undeclaredReason() []extraction.Field {
	fields := lawfulFields()
	fields[0].Reason = extraction.Reason("bogus")
	return fields
}

// E10: ReasonMissing carrying a Value. The value stays non-empty so E08 stays clear, and
// ReasonMissing is declared so E09 does.
func newMissingWithValue() extraction.Extractor { return redFields{build: missingWithValue} }

func missingWithValue() []extraction.Field {
	fields := lawfulFields()
	fields[0].Reason = extraction.ReasonMissing
	return fields
}

// E11's bounds are two independent sites, one per axis, so one row each: PDF points on the
// row's own axis and a normalised box on the other. Page 1 is lawful, so the page site stays
// clear for both.
func newAbsoluteRegionX() extraction.Extractor { return redFields{build: absoluteRegionX} }

func absoluteRegionX() []extraction.Field {
	fields := lawfulFields()
	fields[0].Region = &extraction.Region{Page: 1, X0: 72, Y0: 0.10, X1: 540, Y1: 0.20}
	return fields
}

func newAbsoluteRegionY() extraction.Extractor { return redFields{build: absoluteRegionY} }

func absoluteRegionY() []extraction.Field {
	fields := lawfulFields()
	fields[0].Region = &extraction.Region{Page: 1, X0: 0.10, Y0: 720, X1: 0.40, Y1: 750}
	return fields
}

// E11, page zero: the third site, and the only row in the table that reaches it. The box is
// normalised and non-inverted, so both conjunctions are satisfied and this fires once per
// corpus case rather than three times.
func newPageZeroRegion() extraction.Extractor { return redFields{build: pageZeroRegion} }

func pageZeroRegion() []extraction.Field {
	fields := lawfulFields()
	fields[0].Region = &extraction.Region{Page: 0, X0: 0.10, Y0: 0.10, X1: 0.40, Y1: 0.20}
	return fields
}

// E12's two return-value sites are checked independently, so a row each. Both are lawful on a
// live context, so neither disturbs a value law.
//
// nilErrorUnderCancellation drops the error and the fields: the nil slice is lawful, so only
// the error site fires.
type nilErrorUnderCancellation struct{ refExtractor }

func newNilErrorUnderCancellation() extraction.Extractor { return nilErrorUnderCancellation{} }

func (nilErrorUnderCancellation) Extract(ctx context.Context, _ extraction.Document) ([]extraction.Field, error) {
	if ctx.Err() != nil {
		return nil, nil
	}
	return lawfulFields(), nil
}

// fieldsUnderCancellation smuggles a result out beside the error: the error is lawful, so only
// the slice site fires.
type fieldsUnderCancellation struct{ refExtractor }

func newFieldsUnderCancellation() extraction.Extractor { return fieldsUnderCancellation{} }

func (fieldsUnderCancellation) Extract(ctx context.Context, _ extraction.Document) ([]extraction.Field, error) {
	if err := ctx.Err(); err != nil {
		return lawfulFields(), err
	}
	return lawfulFields(), nil
}

// cancellationBlocking reaches E12's third site, the watchdog, which nothing else in the
// repository exercises. Two conditions carry the whole row:
//
//	ctx.Err() != nil        -- a live-context wait would hang law E04 forever, because
//	                           callExtract runs Extract synchronously with no timer
//	len(doc.Bytes) > 1<<20  -- only the ceiling case is that large, so exactly one of the six
//	                           E12 probes pays the budget instead of all six
//
// It never indexes doc.Bytes: callExtractCancelled strands this goroutine for the binary's
// life still holding doc, so a write here would land inside some later test with nothing to
// attribute it.
type cancellationBlocking struct{ refExtractor }

func newCancellationBlocking() extraction.Extractor { return cancellationBlocking{} }

func (cancellationBlocking) Extract(ctx context.Context, doc extraction.Document) ([]extraction.Field, error) {
	if err := ctx.Err(); err != nil {
		if len(doc.Bytes) > 1<<20 {
			time.Sleep(cancelledExtractBudget + time.Second)
		}
		return nil, err
	}
	return lawfulFields(), nil
}

// redCases is twenty rows for twelve laws -- ONE PER EMISSION SITE, because a law id set
// cannot see half a law go missing. Six laws carry more than one row: no single broken
// extractor can reach both of E01's, E02's, E03's or E12's sites at once without tripping a
// second law, and E04 and E11 own arms a single result cannot occupy together. Nineteen rows
// cost about 13ms apiece; the slow one costs a full cancelledExtractBudget and is the only
// thing in the repository that goes red when the watchdog stops reporting.
var redCases = []redCase{
	{lawID: "E01", name: "name-always-empty", want: lawSet("E01"), newExtractor: newEmptyName},
	{lawID: "E01", name: "name-drifts-within-an-instance", want: lawSet("E01"), newExtractor: newDriftingName},
	{lawID: "E02", name: "version-always-empty", want: lawSet("E02"), newExtractor: newEmptyVersion},
	{lawID: "E02", name: "version-drifts-within-an-instance", want: lawSet("E02"), newExtractor: newDriftingVersion},
	{lawID: "E03", name: "name-stamped-per-instance", want: lawSet("E03"), newExtractor: newInstanceStampedName()},
	{lawID: "E03", name: "version-stamped-per-instance", want: lawSet("E03"), newExtractor: newInstanceStampedVersion()},
	{lawID: "E04", name: "nil-slice-on-success", want: lawSet("E04"), newExtractor: newNilSliceOnSuccess},
	{lawID: "E04", name: "fields-alongside-an-error", want: lawSet("E04"), newExtractor: newFieldsWithError},
	{lawID: "E05", name: "mutates-doc-bytes", want: lawSet("E05"), newExtractor: newBytesMutating},
	{lawID: "E06", name: "empty-field-name", want: lawSet("E06"), newExtractor: newEmptyFieldName},
	{lawID: "E07", name: "duplicate-field-names", want: lawSet("E07"), newExtractor: newDuplicateFieldNames},
	{lawID: "E08", name: "empty-value-pointer", want: lawSet("E08"), newExtractor: newEmptyValuePointer},
	{lawID: "E09", name: "undeclared-reason", want: lawSet("E09"), newExtractor: newUndeclaredReason},
	{lawID: "E10", name: "missing-with-a-value", want: lawSet("E10"), newExtractor: newMissingWithValue},
	{lawID: "E11", name: "absolute-x-coordinates", want: lawSet("E11"), newExtractor: newAbsoluteRegionX},
	{lawID: "E11", name: "absolute-y-coordinates", want: lawSet("E11"), newExtractor: newAbsoluteRegionY},
	{lawID: "E11", name: "page-zero", want: lawSet("E11"), newExtractor: newPageZeroRegion},
	{lawID: "E12", name: "nil-error-under-cancellation", want: lawSet("E12"), newExtractor: newNilErrorUnderCancellation},
	{lawID: "E12", name: "fields-under-cancellation", want: lawSet("E12"), newExtractor: newFieldsUnderCancellation},
	{
		lawID:        "E12",
		name:         "blocks-past-the-budget",
		want:         lawSet("E12"),
		newExtractor: newCancellationBlocking,
		wantMessage:  "did not return within",
		slow:         true,
	},
}

// lawSet builds a want value from a list of ids.
func lawSet(ids ...string) map[string]bool {
	s := make(map[string]bool, len(ids))
	for _, id := range ids {
		s[id] = true
	}
	return s
}

// recordedLaws drives one extractor through the whole suite and returns the recorder: lawIDs
// is what the table grades on, messages what the watchdog row needs beyond it.
func recordedLaws(newExtractor func() extraction.Extractor) *lawRecorder {
	rec := &lawRecorder{}
	RunExtractorContract(rec, newExtractor)
	return rec
}

// recordedLawIDs is the id-set half, which is all the named specs below need.
func recordedLawIDs(newExtractor func() extraction.Extractor) map[string]bool {
	return recordedLaws(newExtractor).lawIDs()
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

// assertOneMessageContains grades a row on the recorded TEXT, for a site set equality cannot
// see. Exactly one, not at least one: a defect that fires on a single corpus case is a
// different defect from one that fires on every case.
func assertOneMessageContains(t *testing.T, label string, messages []string, want string) {
	t.Helper()

	var n int
	for _, m := range messages {
		if strings.Contains(m, want) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%s: %d recorded message(s) carry %q, want exactly 1: recorded %v", label, n, want, messages)
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
			rec := recordedLaws(tc.newExtractor)
			assertExactLawIDs(t, tc.lawID+"/"+tc.name, rec.lawIDs(), tc.want)
			if tc.wantMessage != "" {
				assertOneMessageContains(t, tc.lawID+"/"+tc.name, rec.messages, tc.wantMessage)
			}
		})
	}
}

// The six TestRedCase_* specs below are EXECUTABLE PROOF, not red-first -- see this file's
// header, including which law mutation moves each one. They name the property in the test
// output, which the table's generated subtest names do not.
//
// The blocking E12 row deliberately gets no named spec: it costs a full cancelledExtractBudget,
// and running it twice takes the package from about 6s to about 11s and the repository wall
// time off its plateau. It is graded once, by the table.

// TestRedCase_E04NilSliceIsRejected: the []T to JSON null guard made executable.
func TestRedCase_E04NilSliceIsRejected(t *testing.T) {
	assertExactLawIDs(t, "E04/nil-slice-on-success", recordedLawIDs(newNilSliceOnSuccess), lawSet("E04"))
}

// TestRedCase_E04FieldsAlongsideAnErrorAreRejected covers E04's OTHER half. Without it the
// error arm can be deleted with the nil-slice spec above still green and the repository still
// green -- measured, and the reason E04 carries two rows.
func TestRedCase_E04FieldsAlongsideAnErrorAreRejected(t *testing.T) {
	assertExactLawIDs(t, "E04/fields-alongside-an-error", recordedLawIDs(newFieldsWithError), lawSet("E04"))
}

// TestRedCase_E05MutatingExtractIsRejected: an in-place write through the caller's bytes is
// recorded, and recorded as E05 alone.
func TestRedCase_E05MutatingExtractIsRejected(t *testing.T) {
	assertExactLawIDs(t, "E05/mutates-doc-bytes", recordedLawIDs(newBytesMutating), lawSet("E05"))
}

// TestRedCase_E11AbsoluteCoordinatesAreRejected pairs with the
// extraction_field_results_bbox_normalised CHECK, so PDF points in a Region are caught at both
// layers.
func TestRedCase_E11AbsoluteCoordinatesAreRejected(t *testing.T) {
	assertExactLawIDs(t, "E11/absolute-x-coordinates", recordedLawIDs(newAbsoluteRegionX), lawSet("E11"))
}

// TestRedCase_E11PageZeroIsRejected: pages are 1-based. This is the only row that reaches
// E11's page site, so deleting that site moves this and nothing else.
func TestRedCase_E11PageZeroIsRejected(t *testing.T) {
	assertExactLawIDs(t, "E11/page-zero", recordedLawIDs(newPageZeroRegion), lawSet("E11"))
}

// TestRedCase_E12IgnoringCancellationIsRejected: an extractor that returns a result for an
// already-cancelled context is recorded. The other two E12 sites are graded by the table.
func TestRedCase_E12IgnoringCancellationIsRejected(t *testing.T) {
	assertExactLawIDs(t, "E12/fields-under-cancellation", recordedLawIDs(newFieldsUnderCancellation), lawSet("E12"))
}

const redSuiteFile = "contract_red_test.go"

// fset positions both source scans below. Mode 0, so comments are out of the AST and neither
// scan can be tripped by one.
var fset = token.NewFileSet()

func parseRedSuite(t *testing.T) *ast.File {
	t.Helper()

	f, err := parser.ParseFile(fset, redSuiteFile, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", redSuiteFile, err)
	}
	return f
}

// TestContractCorpusNeedsNoRestore (DESIGN LOCK -- see this file's header, including why the
// fingerprint half needs a non-involutive E05 write and why it passes vacuously in this
// commit): running every non-slow row leaves a freshly built corpus byte-identical, by SHA-256
// per case, to one built before the loop; and no function in this file reaches for Cleanup.
func TestContractCorpusNeedsNoRestore(t *testing.T) {
	// The scans run first so a vacuity Fatalf below can never suppress them.
	assertNoCleanupCalls(t)
	assertSlowRowReadsBytesOnlyForLength(t)

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

const slowRowType = "cancellationBlocking"

// assertSlowRowReadsBytesOnlyForLength fails when the slow row's Extract touches doc.Bytes for
// anything but len. Skipping that row above is only sound while it never writes: its goroutine
// is stranded past the fingerprint, so a late write lands where nothing can attribute it --
// measured on this commit, a deliberate one is caught by nothing, go test -race included. A
// source scan is the only oracle there can be.
func assertSlowRowReadsBytesOnlyForLength(t *testing.T) {
	t.Helper()

	fn := extractMethodOn(t, slowRowType)

	lengths := map[ast.Node]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, isCall := n.(*ast.CallExpr)
		if !isCall || len(call.Args) != 1 {
			return true
		}
		if id, isID := call.Fun.(*ast.Ident); isID && id.Name == "len" {
			lengths[call.Args[0]] = true
		}
		return true
	})

	var uses int
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		sel, isSel := n.(*ast.SelectorExpr)
		if !isSel || sel.Sel.Name != "Bytes" {
			return true
		}
		uses++
		if !lengths[n] {
			t.Errorf("%s: %s.Extract reaches doc.Bytes outside len(); its goroutine outlives the corpus fingerprint, so a write here is unattributable",
				fset.Position(sel.Pos()), slowRowType)
		}
		return true
	})
	if uses == 0 {
		t.Fatalf("%s.Extract never mentions doc.Bytes; it no longer keys on size and the scan above passes vacuously", slowRowType)
	}
}

// extractMethodOn returns the Extract method declared on recvType in this file.
func extractMethodOn(t *testing.T, recvType string) *ast.FuncDecl {
	t.Helper()

	for _, decl := range parseRedSuite(t).Decls {
		fn, isFn := decl.(*ast.FuncDecl)
		if !isFn || fn.Name.Name != "Extract" || fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		typ := fn.Recv.List[0].Type
		if star, isStar := typ.(*ast.StarExpr); isStar {
			typ = star.X
		}
		if id, isID := typ.(*ast.Ident); isID && id.Name == recvType {
			return fn
		}
	}
	t.Fatalf("no Extract method on %s in %s", recvType, redSuiteFile)
	return nil
}

// assertNoCleanupCalls fails when any function in this file reaches for Cleanup. The precedent
// needed restoreL04Corpus (internal/submission/contract_red_test.go:284) because its corpus was
// a package-level var; here newCorpus is a per-law factory, so no row can leak into another and
// no hook is needed. Any Cleanup selector counts, not only a call, so a method value handed to
// a helper is caught too.
func assertNoCleanupCalls(t *testing.T) {
	t.Helper()

	var fns int
	for _, decl := range parseRedSuite(t).Decls {
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
