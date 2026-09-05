// worker_layout_tokens_db_test.go: EXTR-19-06's specs for extraction_jobs.layout_tokens -- the
// page-1 token text a boxless job retains as the input a learned rule is derived from. Package
// extraction_test, so it shares store_db_test.go's TestMain, per-role pools and one skip site.
package extraction_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// --- harness -------------------------------------------------------------------------

// wtWire builds a docling wire response carrying exactly these page-1 token texts, boxless --
// the shape a DOCX read returns. Token text travels verbatim through doclingTokens, so this is
// how a spec picks the bytes the gate then sees.
func wtWire(t *testing.T, texts []string) []byte {
	t.Helper()
	tokens := make([]any, 0, len(texts))
	for _, s := range texts {
		tokens = append(tokens, map[string]any{"text": s})
	}
	b, err := json.Marshal(map[string]any{
		"docling_version": "1.10.0",
		"reader":          "docling",
		"version":         "v1",
		"pages": []any{map[string]any{
			"number": 1, "width_pt": 0.0, "height_pt": 0.0,
			"tables": []any{}, "tokens": tokens,
		}},
	})
	if err != nil {
		t.Fatalf("build the docling wire response: %v", err)
	}
	return b
}

// wtBoxlessWorker is the DOCX arm every spec here drives: the real fixture's bytes under the
// DOCX content type, the render forbidden, and golden replayed as the text read.
func wtBoxlessWorker(t *testing.T, golden []byte) *extraction.ExtractWorker {
	t.Helper()
	ew := wkWorkerPages(t, wkOK(), wkDocxOpener(t), wkForbiddenPages(&wkForbiddenReader{}), &wkAuditRecorder{})
	ew.Text = wpDoclingReader(t, golden)
	ew.Rules = wpStoreRules(t).load
	return ew
}

// wtRun works one job and returns its extraction_jobs id.
func wtRun(t *testing.T, ctx context.Context, ew *extraction.ExtractWorker, tenantID, documentID string, riverJobID int64) string {
	t.Helper()
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work (river job %d) returned %v, want nil -- a refused token set must never fail the job", riverJobID, err)
	}
	return wkExtractionJobID(t, ctx, tenantID, riverJobID)
}

// wtTokens reads extraction_jobs.layout_tokens as text. Nil is SQL NULL: the job stored no
// page-1 token text.
func wtTokens(t *testing.T, ctx context.Context, jobID string) *string {
	t.Helper()
	var raw *string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT layout_tokens::text FROM extraction_jobs WHERE id = $1`, jobID).Scan(&raw); err != nil {
		t.Fatalf("read layout_tokens for job %s: %v", jobID, err)
	}
	return raw
}

// wtTokenCharLength is Postgres's own rendered length of the stored value -- the number the
// column's CHECK measures, which is not len(json.Marshal).
func wtTokenCharLength(t *testing.T, ctx context.Context, jobID string) int {
	t.Helper()
	var n *int
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT char_length(layout_tokens::text) FROM extraction_jobs WHERE id = $1`, jobID).Scan(&n); err != nil {
		t.Fatalf("read char_length(layout_tokens) for job %s: %v", jobID, err)
	}
	if n == nil {
		t.Fatalf("job %s carries layout_tokens NULL, so its rendered length cannot be measured", jobID)
	}
	return *n
}

func wtDecode(t *testing.T, what string, raw *string) []string {
	t.Helper()
	if raw == nil {
		t.Fatalf("%s: layout_tokens is SQL NULL -- the job stored no page-1 token text", what)
	}
	var got []string
	if err := json.Unmarshal([]byte(*raw), &got); err != nil {
		t.Fatalf("%s: decode the stored layout_tokens: %v", what, err)
	}
	return got
}

// wtPageOneTexts is the golden's own page-1 token texts, read a second time through a real
// DoclingReader. Page 1 by Number, the rule BoxlessFingerprint follows.
func wtPageOneTexts(t *testing.T, golden []byte) []string {
	t.Helper()
	_, pages, res := dcServeGolden(t, golden)
	if res.TextChars == 0 {
		t.Fatalf("the golden carries no text; the boxless block is gated on TextChars > 0 and every assertion below would be vacuous")
	}
	for _, p := range pages {
		if p.Number != 1 {
			continue
		}
		out := make([]string, 0, len(p.Tokens))
		for _, tok := range p.Tokens {
			out = append(out, tok.Text)
		}
		if len(out) == 0 {
			t.Fatalf("the golden's page 1 carries no token; the comparison below would hold over two empty lists")
		}
		return out
	}
	t.Fatalf("the golden carries no page numbered 1")
	return nil
}

// wtMeasure is the gate's own metric: len(json.Marshal) plus one byte per comma, because
// Postgres renders a jsonb array with ", " and Go does not.
func wtMeasure(t *testing.T, tokens []string) int {
	t.Helper()
	b, err := json.Marshal(tokens)
	if err != nil {
		t.Fatalf("marshal the fixture: %v", err)
	}
	return len(b) + max(0, len(tokens)-1)
}

// wtTokensMeasuring returns n plain-ASCII token texts measuring exactly want under wtMeasure.
// Computed by padding the last token, never hand-counted: a hand-counted fixture stops guarding
// the boundary the moment the cap or the encoding moves.
func wtTokensMeasuring(t *testing.T, n, want int) []string {
	t.Helper()
	if n < 2 {
		t.Fatalf("a %d-token fixture makes the N-1 separator correction invisible", n)
	}
	tokens := make([]string, n)
	for i := range tokens {
		tokens[i] = fmt.Sprintf("Line %04d: Total 300.00", i)
	}
	for range 3 {
		m := wtMeasure(t, tokens)
		switch {
		case m == want:
			return tokens
		case m > want:
			t.Fatalf("the padded fixture measures %d, want exactly %d -- one ASCII byte must add exactly one", m, want)
		}
		tokens[n-1] += strings.Repeat("x", want-m)
	}
	t.Fatalf("padding did not converge on %d", want)
	return nil
}

// --- AC-1 ------------------------------------------------------------------------------

// LT-1 (AC-1). Two arms: the shipped .docx, and a hand-authored response whose document order
// is NOT sorted order. invoice.docx's own three paragraphs happen to be alphabetical, so the
// golden alone cannot tell document order from a sort. Must-fail mutation: write the tokens
// sorted -- the second arm reds.
func TestRLS_ExtractWorkerStoresPageOneTokensForABoxlessJob(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	golden := dcReadNamedGolden(t, dxGolden)
	want := wtPageOneTexts(t, golden)
	// Fixture-drift floor: the stored value is compared against the golden's own page-1 read,
	// and that read is compared against the .docx's transcribed paragraphs.
	if !slices.Equal(want, dxParagraphs) {
		t.Fatalf("%s page-1 tokens = %q, want invoice.docx's own paragraphs %q -- the golden is stale", dxGolden, want, dxParagraphs)
	}

	jobID := wtRun(t, ctx, wtBoxlessWorker(t, golden), tenantID, documentID, 915206)
	stAssertJobState(t, ctx, jobID, "succeeded")

	// Same row, both columns: the identity and the input it was derived from share one fate.
	if row := stJobLayout(t, ctx, jobID); row.Fingerprint == nil {
		t.Errorf("the boxless job carries layout_fingerprint NULL; the tokens below would describe a layout nothing is keyed on")
	}

	got := wtDecode(t, "a boxless DOCX", wtTokens(t, ctx, jobID))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the boxless job stored layout_tokens %q, want page 1's own token texts in document order %q", got, want)
	}

	unsorted := []string{"Total: 300.00", "Invoice No: ASC-2026-0001", "Buyer: Honeywell Group"}
	if slices.IsSorted(unsorted) {
		t.Fatalf("the order fixture %q is already sorted, so the assertion below cannot discriminate a sorted write", unsorted)
	}
	orderJob := wtRun(t, ctx, wtBoxlessWorker(t, wtWire(t, unsorted)), tenantID, documentID, 915214)
	stAssertJobState(t, ctx, orderJob, "succeeded")
	if got := wtDecode(t, "the order arm", wtTokens(t, ctx, orderJob)); !reflect.DeepEqual(got, unsorted) {
		t.Errorf("the order arm stored layout_tokens %q, want document order %q", got, unsorted)
	}
}

// --- AC-2 ------------------------------------------------------------------------------

// LT-2 (AC-2). The over-cap fixture is one byte over under the gate's metric and UNDER the cap
// under a naive len(json.Marshal), so it is also the canary for the separator correction: a
// gate measuring only len(b) stores it and Postgres refuses at 262145 rendered chars, which
// fails the shared transaction and dead-letters a document whose extraction is correct.
// Must-fail mutation: truncate to the cap instead of refusing -- the NULL assertion reds.
func TestRLS_ABoxlessJobOverTheTokenCapStoresNoTokens(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	capBytes := extraction.MaxLayoutTokensJSONForTest
	over := wtTokensMeasuring(t, 500, capBytes+1)
	if b, err := json.Marshal(over); err != nil || len(b) > capBytes {
		t.Fatalf("the over-cap fixture marshals to %d byte(s) (err %v); it must sit UNDER the cap by len(json.Marshal) alone, or it does not discriminate the separator correction", len(b), err)
	}

	// Control arm first, so the NULL that follows is attributable to the size and not to a
	// column no job ever writes.
	small := wtTokensMeasuring(t, 500, capBytes/2)
	okJob := wtRun(t, ctx, wtBoxlessWorker(t, wtWire(t, small)), tenantID, documentID, 915207)
	stAssertJobState(t, ctx, okJob, "succeeded")
	if got := wtDecode(t, "a storable boxless job", wtTokens(t, ctx, okJob)); !slices.Equal(got, small) {
		t.Fatalf("the control arm stored %d token(s), want its own %d -- without a stored control the NULL below is also what an unimplemented column looks like", len(got), len(small))
	}

	overJob := wtRun(t, ctx, wtBoxlessWorker(t, wtWire(t, over)), tenantID, documentID, 915208)
	stAssertJobState(t, ctx, overJob, "succeeded")
	if got := stJobFailureKind(t, ctx, overJob); got != nil {
		t.Errorf("an over-cap boxless job carries failure_kind %q, want SQL NULL -- token content must not dead-letter a correct extraction", *got)
	}
	if row := stJobLayout(t, ctx, overJob); row.Fingerprint == nil {
		t.Errorf("an over-cap boxless job carries layout_fingerprint NULL; the identity must survive a refused token set")
	}
	if raw := wtTokens(t, ctx, overJob); raw != nil {
		t.Errorf("an over-cap boxless job stored layout_tokens %s, want SQL NULL -- the refusal path never truncates", wkStr(raw))
	}
}

// --- AC-3 ------------------------------------------------------------------------------

// LT-3 (AC-3). The regression test for the three-cap defect: under the old 512x512 product this
// dead-lettered at layout_not_written. Must-fail mutation: set the SQL CHECK one byte below the
// Go cap -- the job dead-letters. Must-stay-green mutation: change a token's text without
// changing its length.
func TestRLS_ABoxlessJobAtExactlyTheTokenCapStoresItsTokens(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	capBytes := extraction.MaxLayoutTokensJSONForTest
	fixture := wtTokensMeasuring(t, 500, capBytes)

	// The fixture proves its own exactness BEFORE any outcome is read: without this the test
	// passes whether or not it sits on the boundary, and the assertions below stop being a
	// canary at all.
	b, err := json.Marshal(fixture)
	if err != nil {
		t.Fatalf("marshal the cap fixture: %v", err)
	}
	if got := len(b) + len(fixture) - 1; got != capBytes {
		t.Fatalf("the cap fixture measures %d, want exactly %d", got, capBytes)
	}
	if len(b) >= capBytes {
		t.Fatalf("the cap fixture marshals to %d byte(s), which already reaches the cap; the N-1 separator correction is then invisible and this test cannot see the defect it exists for", len(b))
	}

	jobID := wtRun(t, ctx, wtBoxlessWorker(t, wtWire(t, fixture)), tenantID, documentID, 915209)
	stAssertJobState(t, ctx, jobID, "succeeded")
	if got := stJobFailureKind(t, ctx, jobID); got != nil {
		t.Fatalf("a boxless job saturating the cap carries failure_kind %q, want SQL NULL -- a valid document must not dead-letter at its own token size", *got)
	}
	if got := wtDecode(t, "a saturating boxless job", wtTokens(t, ctx, jobID)); !slices.Equal(got, fixture) {
		t.Errorf("the saturating job stored %d token(s), want its own %d", len(got), len(fixture))
	}
	// Postgres's own measure, at the boundary: this is the number the CHECK compares.
	if got := wtTokenCharLength(t, ctx, jobID); got != capBytes {
		t.Errorf("the stored value renders as %d char(s), want exactly %d -- the Go metric and char_length(jsonb::text) must saturate together", got, capBytes)
	}
}

// --- AC-4 ------------------------------------------------------------------------------

// LT-4 (AC-4). Must-fail mutation: drop the NUL conjunct -- the INSERT raises
// unsupported Unicode escape sequence, the shared transaction fails and the job dead-letters at
// layout_not_written.
func TestRLS_ABoxlessJobWithANulByteStoresNoTokens(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	nulFree := []string{"Invoice No: ASC-2026-0919", "Issue Date: 14 Aug 2026", "Total: 300.00"}
	withNul := slices.Clone(nulFree)
	withNul[2] = "Total:\x00 300.00"

	// Control arm: the same token set without the NUL stores. Without it the NULL below is
	// equally what a column no job ever writes looks like.
	okJob := wtRun(t, ctx, wtBoxlessWorker(t, wtWire(t, nulFree)), tenantID, documentID, 915210)
	stAssertJobState(t, ctx, okJob, "succeeded")
	if got := wtDecode(t, "a NUL-free boxless job", wtTokens(t, ctx, okJob)); !slices.Equal(got, nulFree) {
		t.Fatalf("the control arm stored %q, want its own %q", got, nulFree)
	}

	nulJob := wtRun(t, ctx, wtBoxlessWorker(t, wtWire(t, withNul)), tenantID, documentID, 915211)
	stAssertJobState(t, ctx, nulJob, "succeeded")
	if got := stJobFailureKind(t, ctx, nulJob); got != nil {
		t.Errorf("a NUL-bearing boxless job carries failure_kind %q, want SQL NULL", *got)
	}
	row := stJobLayout(t, ctx, nulJob)
	if row.Fingerprint == nil {
		t.Errorf("a NUL-bearing boxless job carries layout_fingerprint NULL; the identity does not travel through the token text and must survive")
	}
	if row.Anchors == nil {
		t.Errorf("a NUL-bearing boxless job carries layout_anchors NULL; the sibling write must not be taken down with the tokens")
	}
	if raw := wtTokens(t, ctx, nulJob); raw != nil {
		t.Errorf("a NUL-bearing boxless job stored layout_tokens %s, want SQL NULL", wkStr(raw))
	}
}

// LT-5 (AC-4, sink probe). layout_anchors is the OTHER page-1 sink, and it is written from the
// same token set on the PDF arm too. Green by design: every anchorLexicon pattern is literal
// words, \s*, \.? and [\s-]*, none of which match a NUL, so no matched substring can carry one.
// The value is the alarm -- a lexicon edit that admitted a NUL into a match would make the real
// INSERT below fail, and that failure reaches the PDF arm as a dead-letter.
func TestRLS_LayoutAnchorsSurviveANulBearingToken(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	_, tokens, res := dcServeGolden(t, wtWire(t, []string{"Total:\x00 300.00", "Buyer:\x00 Honeywell Group"}))
	if res.TextChars == 0 {
		t.Fatalf("the hand-authored wire response carries no text")
	}
	obs := extraction.AnchorObservations(tokens)
	if len(obs) == 0 {
		t.Fatalf("a NUL-bearing token matched no anchor pattern; the INSERT below would carry an empty list and prove nothing")
	}
	for _, o := range obs {
		if strings.ContainsRune(o.Text, 0) {
			t.Errorf("anchor %q carries a NUL in its matched text %q; jsonb_in refuses that escape, so layout_anchors can no longer store this document", o.Label, o.Text)
		}
	}

	anchors, err := extraction.MarshalAnchorObservations(obs)
	if err != nil {
		t.Fatalf("MarshalAnchorObservations over a NUL-bearing token set: %v", err)
	}
	jobID := uuid.NewString()
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version, layout_anchors)
		 VALUES ($1, $2, $3, $4, $5, $6::jsonb)`,
		jobID, tenantID, documentID, wkExtractorName, wkExtractorVersion, string(anchors)); err != nil {
		t.Fatalf("storing layout_anchors derived from a NUL-bearing token set failed: %v -- this is a pre-existing defect on the PDF arm (worker.go's AnchorObservations call), not EXTR-19-06's to fix", err)
	}
}

// --- AC-5 ------------------------------------------------------------------------------

// wtCase is one corpus entry: a name that survives a category relabel, and the token set.
type wtCase struct {
	name   string
	tokens []string
}

// wtByteSpaceCorpus is every byte 0x00-0xFF embedded as "a<b>b", plus thirteen cases named
// individually so no category label can silently omit the decisive one.
func wtByteSpaceCorpus() []wtCase {
	out := make([]wtCase, 0, 269)
	for b := range 256 {
		out = append(out, wtCase{
			name:   fmt.Sprintf("byte 0x%02X", b),
			tokens: []string{"a" + string([]byte{byte(b)}) + "b"},
		})
	}
	return append(out,
		wtCase{"NUL", []string{"Total:\x00 300.00"}},
		wtCase{"lone high surrogate (WTF-8 ED A0 80)", []string{"a" + string([]byte{0xED, 0xA0, 0x80}) + "b"}},
		wtCase{"lone low surrogate (ED B0 80)", []string{"a" + string([]byte{0xED, 0xB0, 0x80}) + "b"}},
		wtCase{"invalid UTF-8 continuation (0xFF)", []string{"a" + string([]byte{0xFF}) + "b"}},
		wtCase{"truncated 3-byte sequence", []string{"a" + string([]byte{0xE2, 0x82}) + "b"}},
		wtCase{"U+2028", []string{"a\u2028b"}},
		wtCase{"U+2029", []string{"a\u2029b"}},
		wtCase{"astral U+1F600", []string{"a\U0001F600b"}},
		// Six literal characters, not a NUL: a document whose text really reads that escape
		// marshals to a doubled backslash and Postgres takes it, so a gate scanning the OUTPUT
		// for the escape would false-refuse a storable document.
		wtCase{`text that literally reads \u0000`, []string{`Total: \u0000`}},
		wtCase{"only NUL", []string{"\x00"}},
		wtCase{"empty string", []string{""}},
		wtCase{"nil slice", nil},
		wtCase{"empty slice", []string{}},
	)
}

// wtInsertTokens attempts the real INSERT and reports whether Postgres took it.
func wtInsertTokens(t *testing.T, ctx context.Context, tenantID, documentID, raw string) bool {
	t.Helper()
	_, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version, layout_tokens)
		 VALUES ($1, $2, $3, $4, $5, $6::jsonb)`,
		uuid.NewString(), tenantID, documentID, wkExtractorName, wkExtractorVersion, raw)
	return err == nil
}

// LT-6 (AC-5). Gate <=> Postgres in both directions. The two refusal sets are pinned by name so
// an all-refusing gate and a sweep that inserted nothing both red instead of reporting a clean
// agreement. Must-fail mutation (over-strict): also refuse 0x01. Must-fail (under-strict):
// accept NUL -- the gate's own bytes are then offered to Postgres and refused.
func TestLayoutTokensGate_AgreesWithPostgresOverTheWholeByteSpace(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	cases := wtByteSpaceCorpus()
	if len(cases) != 269 {
		t.Fatalf("the corpus holds %d case(s), want exactly 269 -- a shrunken corpus reports agreement it never measured", len(cases))
	}

	var gateRefused, pgRefused, disagreed []string
	for _, c := range cases {
		// The raw encoding is what Postgres would receive with the gate removed. A marshal
		// error is itself a refusal: there is nothing to send.
		rawOK := false
		if b, err := json.Marshal(c.tokens); err == nil {
			rawOK = wtInsertTokens(t, ctx, tenantID, documentID, string(b))
		}
		if !rawOK {
			pgRefused = append(pgRefused, c.name)
		}

		gateB, gateOK := extraction.LayoutTokensStorableForTest(c.tokens)
		if !gateOK {
			gateRefused = append(gateRefused, c.name)
			if rawOK {
				disagreed = append(disagreed, c.name+" (gate refuses, Postgres accepts)")
			}
			continue
		}
		if !wtInsertTokens(t, ctx, tenantID, documentID, string(gateB)) {
			disagreed = append(disagreed, c.name+" (gate accepts, Postgres refuses its own bytes)")
		}
	}

	if len(disagreed) != 0 {
		t.Errorf("the gate and Postgres disagree on %d of %d case(s): %v", len(disagreed), len(cases), disagreed)
	}

	// A raw nil slice marshals to `null`, which jsonb_typeof refuses; the gate normalises it to
	// [] and so is NOT in the gate refusal set.
	wantPG := []string{"NUL", "byte 0x00", "nil slice", "only NUL"}
	wantGate := []string{"NUL", "byte 0x00", "only NUL"}
	wtAssertRefusalSet(t, "Postgres", len(cases), pgRefused, wantPG)
	wtAssertRefusalSet(t, "the gate", len(cases), gateRefused, wantGate)
}

// wtAssertRefusalSet reports the two set differences rather than dumping 269 names: an
// all-refusing gate and a sweep that inserted nothing must both be readable.
func wtAssertRefusalSet(t *testing.T, who string, total int, got, want []string) {
	t.Helper()
	slices.Sort(got)
	slices.Sort(want)
	if slices.Equal(got, want) {
		return
	}
	extra := wtHead(wtDifference(got, want))
	missing := wtHead(wtDifference(want, got))
	t.Errorf("%s refused %d of %d case(s), want exactly %d %v\n  refused but must not be: %v\n  must be refused but was not: %v",
		who, len(got), total, len(want), want, extra, missing)
}

func wtDifference(a, b []string) []string {
	var out []string
	for _, s := range a {
		if !slices.Contains(b, s) {
			out = append(out, s)
		}
	}
	return out
}

func wtHead(s []string) string {
	if len(s) <= 8 {
		return fmt.Sprintf("%v", s)
	}
	return fmt.Sprintf("%v and %d more", s[:8], len(s)-8)
}

// --- AC-6 ------------------------------------------------------------------------------

// LT-7 (AC-6). Must-fail mutation: flip the !RendersPageImages conjunct at worker.go's boxless
// branch -- a PDF then takes both arms, stores tokens here, and overwrites its v1: fingerprint
// with a b1: one.
func TestRLS_APdfJobNeverStoresLayoutTokens(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	pdfEW := wpWorker(t, wkOK(), wpCorpusOpener(t),
		wpDoclingReader(t, dcReadNamedGolden(t, dcCorpusGoldenName)), wpStoreRules(t).load, &wkAuditRecorder{})
	pdfJob := wtRun(t, ctx, pdfEW, tenantID, documentID, 915212)
	stAssertJobState(t, ctx, pdfJob, "succeeded")

	pdfRow := stJobLayout(t, ctx, pdfJob)
	if pdfRow.Fingerprint == nil || !strings.HasPrefix(*pdfRow.Fingerprint, extraction.FingerprintVersion+":") {
		t.Fatalf("the PDF arm stored layout_fingerprint %s, want the %q namespace -- it did not take the geometric branch, so the NULL below proves nothing",
			wkStr(pdfRow.Fingerprint), extraction.FingerprintVersion)
	}
	if raw := wtTokens(t, ctx, pdfJob); raw != nil {
		t.Errorf("a PDF job stored layout_tokens %s, want SQL NULL", wkStr(raw))
	}

	// Control, same tenant and same column: NULL is not the column's universal state.
	docxJob := wtRun(t, ctx, wtBoxlessWorker(t, dcReadNamedGolden(t, dxGolden)), tenantID, documentID, 915213)
	stAssertJobState(t, ctx, docxJob, "succeeded")
	if raw := wtTokens(t, ctx, docxJob); raw == nil {
		t.Errorf("the DOCX control stored layout_tokens NULL; without a non-NULL value in this column the PDF's NULL above is also what an unwritten column looks like")
	}
}
