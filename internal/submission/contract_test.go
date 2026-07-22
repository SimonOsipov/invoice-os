// M5-02-06 (task-220), Stage 1: the reusable, DB-free, network-free adapter contract suite --
// ContractT, the complete AllLaws id list, the Canonical corpus, and RunAdapterContract /
// CheckResult / CheckEvidence with deliberate Stage 2.5 NO-OP stub bodies. The executor's
// Stage 2 commit replaces those three stub bodies with the real sixteen-law implementation;
// this file's own shape (interface, id list, corpus) is already the real, final shape.
//
// HONEST FRAMING (do not relabel any test's Kind below without re-reading this): because
// RunAdapterContract is authored in THIS SAME subtask, "the reference adapter passes with
// zero failures" is trivially true against a no-op suite -- a false green, not a demonstrated
// RED. The suite's teeth (proof it actually rejects a non-conforming adapter) are demonstrated
// in M5-02-07 alone, a separate FINAL subtask, on purpose -- do not fold that proof in here.
// Of the six specs in this file:
//   - TestAllLaws_IdsAreUniqueAndUsed is genuinely RED right now (see its own doc comment).
//   - TestCheckResult_ShortCircuitsOnNilResult is genuinely RED right now (see its own doc
//     comment).
//   - TestContractSuite_UsesNarrowT is a regression guard, not a red-first spec: it passes
//     from the moment it compiles, because a no-op RunAdapterContract trivially never calls
//     t.Run or Fatalf. Framed like TestValidatorClient_DoesNotImportValidationPackage
//     (internal/invoice/validator_test.go:440-451) and TestSubmissionPackage_
//     DoesNotImportInvoicePackage (deps_test.go in this package): the property already holds
//     for any RunAdapterContract that never reaches for t.Run/Fatalf, so this exists to lock
//     that property against a later draft, not to record a transition.
//   - TestCheckResult_AcceptsWellFormedVariants, TestCheckEvidence_AcceptsWellFormedEvidence
//     and TestContractSuite_RunsWithoutDatabase are confirmatory and trivially green against
//     the Stage 2.5 stub (a no-op records zero failures for ANY input) -- expected engineering
//     progress, not proof the suite works.
//
// Package submission_test (external), matching every other test file in this package
// (exchange_test.go, exchange_db_test.go, failure_modes_test.go, worker_smoke_test.go,
// registry_test.go, ...). TestMain already exists at failure_modes_test.go:57 -- one per test
// binary -- so this file defines none. No testify. No t.Skip anywhere: these tests are pure Go
// with no DB and no network, and internal/tools/rlsgate fails the CI queue job on any
// test-level skip (or on zero tests observed), so they must run unconditionally.
package submission_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// ContractT is the slice of *testing.T the suite needs. *testing.T satisfies it (confirmed on
// this toolchain, Go 1.26.4: func (c *common) Helper() and func (c *common) Errorf(format
// string, args ...any) are both on *testing.T's method set). M5-02-07's recorder substitutes a
// narrow double for it -- [suite-takes-a-narrow-t] -- which is only possible because
// RunAdapterContract, CheckResult and CheckEvidence never reach for t.Run (whose callback is
// typed *testing.T, a concrete type no interface satisfies) or t.Fatalf (which calls
// runtime.Goexit and would abort the whole test binary rather than just this one check).
type ContractT interface {
	Helper()
	Errorf(format string, args ...any)
}

var _ ContractT = (*testing.T)(nil)

// AllLaws is the complete law id list: exactly L01 through L16, in the parent story's table
// order, no extras and no omissions. M5-02-07's completeness test asserts every id here has a
// demonstrated RED; TestAllLaws_IdsAreUniqueAndUsed (below) asserts this list has no
// duplicates and matches exactly the set of ids RunAdapterContract/CheckResult/CheckEvidence's
// own Errorf call sites emit.
var AllLaws = []string{
	"L01", "L02", "L03", "L04", "L05", "L06", "L07", "L08",
	"L09", "L10", "L11", "L12", "L13", "L14", "L15", "L16",
}

// canonicalCase names one entry in the fixed corpus RunAdapterContract drives Transform
// (L03/L04/L05) and Submit with.
type canonicalCase struct {
	name string
	c    submission.Canonical
}

// canonicalCorpus is the fixed set of submission.Canonical values the suite exercises: a
// fully-populated invoice, a minimal one (invoice number only), one with no lines, one with
// all-nil money fields, one with multi-byte/very-long text, and the zero value.
//
// The "no lines" case's Lines field is NIL, not an empty-but-non-nil slice. SubmissionCanonical
// (internal/invoice/submission_canonical.go) builds Lines with `var lines []CanonicalLine` +
// append, so an invoice with zero line items -- whether the source LineItems was nil or a
// non-nil empty slice -- always yields a nil Canonical.Lines, never an empty one. That matters
// here because a nil []T marshals to JSON null, not [] -- the exact class of gate failure
// M4-16 shipped against (a Go []T without omitempty rendering null instead of []). The suite
// does not legislate an adapter's wire format ([wire-is-opaque-bytes]), so this is not a
// seventeenth law -- it is this corpus keeping the case and being honest about its shape, so
// whoever writes M5-03's mock or M6's real adapter meets the nil-vs-empty decision here, inside
// the contract suite, rather than in production against a live authority.
var canonicalCorpus = []canonicalCase{
	{name: "full", c: fullCanonical()},
	{name: "minimal", c: submission.Canonical{InvoiceNumber: "INV-MIN-0001"}},
	{name: "no-lines", c: noLinesCanonical()}, // Lines is nil here, see doc comment above
	{name: "all-nil-money", c: allNilMoneyCanonical()},
	{name: "multi-byte-long-text", c: multiByteLongTextCanonical()},
	{name: "zero", c: submission.Canonical{}},
}

func strPtr(s string) *string { return &s }

func fullCanonical() submission.Canonical {
	issue := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	return submission.Canonical{
		InvoiceID:     "inv-full-1",
		InvoiceNumber: "INV-FULL-0001",
		IssueDate:     &issue,
		Supplier:      submission.Party{TIN: strPtr("SUP-TIN-1"), Name: strPtr("Supplier Co")},
		Buyer:         submission.Party{TIN: strPtr("BUY-TIN-1"), Name: strPtr("Buyer Ltd")},
		Currency:      strPtr("NGN"),
		Subtotal:      strPtr("1000.00"),
		VAT:           strPtr("75.00"),
		Total:         strPtr("1075.00"),
		Lines: []submission.CanonicalLine{
			{
				LineID:      "line-1",
				LineNo:      1,
				Description: strPtr("Widget"),
				Quantity:    strPtr("2"),
				UnitPrice:   strPtr("500.00"),
				LineTotal:   strPtr("1000.00"),
				LineTax:     strPtr("75.00"),
			},
		},
	}
}

func noLinesCanonical() submission.Canonical {
	return submission.Canonical{
		InvoiceID:     "inv-no-lines-1",
		InvoiceNumber: "INV-NOLINES-0001",
		Currency:      strPtr("NGN"),
		Subtotal:      strPtr("0.00"),
		VAT:           strPtr("0.00"),
		Total:         strPtr("0.00"),
		// Lines is deliberately left as the zero value -- nil, not []submission.CanonicalLine{}.
	}
}

func allNilMoneyCanonical() submission.Canonical {
	return submission.Canonical{
		InvoiceID:     "inv-nil-money-1",
		InvoiceNumber: "INV-NILMONEY-0001",
		Supplier:      submission.Party{TIN: strPtr("SUP-TIN-2"), Name: strPtr("Supplier Co")},
		Buyer:         submission.Party{TIN: strPtr("BUY-TIN-2"), Name: strPtr("Buyer Ltd")},
		Currency:      strPtr("NGN"),
		// Subtotal/VAT/Total deliberately nil -- the "all-nil money fields" corpus case.
		Lines: []submission.CanonicalLine{
			{
				LineID:      "line-nil-1",
				LineNo:      1,
				Description: strPtr("Service with no priced fields yet"),
				// Quantity/UnitPrice/LineTotal/LineTax deliberately nil too.
			},
		},
	}
}

func multiByteLongTextCanonical() submission.Canonical {
	long := strings.Repeat("Πολύ μεγάλο κείμενο με πολλαπλά bytes 你好世界 🎉 ", 200)
	return submission.Canonical{
		InvoiceID:     "inv-mb-1",
		InvoiceNumber: "INV-Ελληνικά-你好-0001",
		Supplier:      submission.Party{TIN: strPtr("SUP-TIN-3"), Name: strPtr("Ελληνική Εταιρεία 你好")},
		Buyer:         submission.Party{TIN: strPtr("BUY-TIN-3"), Name: strPtr(long)},
		Currency:      strPtr("NGN"),
		Subtotal:      strPtr("1.00"),
		VAT:           strPtr("0.00"),
		Total:         strPtr("1.00"),
		Lines: []submission.CanonicalLine{
			{LineID: "line-mb-1", LineNo: 1, Description: strPtr(long)},
		},
	}
}

// RunAdapterContract runs every law in AllLaws against adapters produced by newAdapter. No
// database, no network, no subtests (no t.Run), no Fatalf. newAdapter is called MORE THAN ONCE
// so identity-stability (L02) and input-mutation (L04) laws can compare two fresh instances.
//
// STAGE 2.5 STUB (M5-02-06 bootstrap, task-220): deliberately does nothing -- neither calls
// newAdapter nor evaluates any law. The executor's Stage 2 commit replaces this body with the
// real sixteen-law run.
//
// TODO(Stage 2): recover() belongs HERE, at each adapter-method call boundary -- never inside
// CheckResult/CheckEvidence, never exposed through ContractT. Only this function calls the
// adapter's methods, so only its own call sites can recover from one of them panicking:
//   - Poll's call site recovers under L14 ONLY.
//   - Name/Version/Transform/Submit's call sites each recover under L15 ONLY.
//     L14 and L15 partition the panic surface; Poll must never also be checked under L15, or an
//     adapter that panics on Poll would trip both and break M5-02-07's set-equality assertions.
func RunAdapterContract(t ContractT, newAdapter func() submission.Adapter) {
	t.Helper()
	// Stage 2.5: no-op. See TODO above.
}

// CheckResult evaluates L06 through L10 against r, labeling every failure with ctxName. A nil
// Result, or a pointer variant (*Accepted etc. -- isResult() has a value receiver, so the four
// pointer types satisfy Result too, per the parent story's L06 aliasing-hazard note), fails L06
// and returns immediately without evaluating L07-L10.
//
// STAGE 2.5 STUB (M5-02-06 bootstrap, task-220): deliberately does nothing -- no Errorf call
// ever fires. TestCheckResult_ShortCircuitsOnNilResult (below) is RED against this stub for
// exactly that reason. The executor's Stage 2 commit replaces this body with the real type
// switch.
func CheckResult(t ContractT, ctxName string, r submission.Result) {
	t.Helper()
	// Stage 2.5: no-op. See doc comment above.
}

// CheckEvidence evaluates L11 through L13 against ev, labeling every failure with ctxName.
// idemKey is the idempotency key passed to the Submit call that produced ev; pass "" for
// evidence that did not originate from a Submit call (e.g. Poll's) -- L13 has nothing to
// compare there and is vacuously satisfied by an adapter that sets no Idempotency-Key header at
// all. That is intentional, not a gap: the law only constrains an adapter that DOES expose
// idempotency via this header, guarding against one that mints its own key and silently breaks
// dedupe.
//
// STAGE 2.5 STUB (M5-02-06 bootstrap, task-220): deliberately does nothing. The executor's
// Stage 2 commit replaces this body with the real L11/L12/L13 checks.
func CheckEvidence(t ContractT, ctxName string, ev submission.Evidence, idemKey string) {
	t.Helper()
	// Stage 2.5: no-op. See doc comment above.
}

// lawRecorder is a ContractT double that accumulates every Errorf message and never calls
// t.Run or Fatalf -- exactly the shape of substitute [suite-takes-a-narrow-t] exists to make
// possible, and the type TestContractSuite_UsesNarrowT below drives RunAdapterContract with.
type lawRecorder struct {
	messages []string
}

func (r *lawRecorder) Helper() {}

func (r *lawRecorder) Errorf(format string, args ...any) {
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
}

// lawIDs returns the set of law ids (the "L07" in "L07: ...") the recorder observed, parsed
// from the prefix of each message up to its first ": ".
func (r *lawRecorder) lawIDs() map[string]bool {
	ids := make(map[string]bool, len(r.messages))
	for _, m := range r.messages {
		if i := strings.Index(m, ":"); i > 0 {
			ids[m[:i]] = true
		}
	}
	return ids
}

// lawIDsInSuiteSource statically scans contract_test.go's own text for law ids appearing at an
// Errorf call site -- i.e. a call whose format string starts with a law id (like L07,
// literally two digits) immediately followed by a colon. This is a
// source-text check, not a runtime one: discovering "which ids CAN the suite emit" by running
// RunAdapterContract would require deliberately non-conforming adapters to actually trip each
// law, and manufacturing those here is explicitly M5-02-07's job alone (see this file's header
// and that subtask's scope note) -- a regex over this file's own bytes needs neither.
func lawIDsInSuiteSource(t *testing.T) map[string]bool {
	t.Helper()
	root := repoRootForDepsTest(t)
	path := filepath.Join(root, "internal", "submission", "contract_test.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	re := regexp.MustCompile(`Errorf\("(L\d{2}):`)
	ids := make(map[string]bool)
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		ids[m[1]] = true
	}
	return ids
}

// TestAllLaws_IdsAreUniqueAndUsed (AC-4, RED-FIRST): AllLaws must list no duplicate id, and
// must match EXACTLY the set of law ids the suite's own Errorf call sites emit -- discovered by
// scanning contract_test.go's source text (see lawIDsInSuiteSource), not by running the suite
// against adapters, since provoking every law to actually fire would require deliberately
// non-conforming adapters that are out of scope for this subtask (M5-02-07's job alone).
//
// Against this subtask's Stage 2.5 no-op stub -- whose RunAdapterContract/CheckResult/
// CheckEvidence bodies contain no Errorf call at all -- the emitted set is empty while AllLaws
// lists sixteen ids, so this fails on a real assertion (not a compile error) right now. It goes
// green once the executor's Stage 2 commit fills in the real law checks.
func TestAllLaws_IdsAreUniqueAndUsed(t *testing.T) {
	seen := make(map[string]bool, len(AllLaws))
	for _, id := range AllLaws {
		if seen[id] {
			t.Errorf("AllLaws contains duplicate id %q", id)
		}
		seen[id] = true
	}

	used := lawIDsInSuiteSource(t)

	for _, id := range AllLaws {
		if !used[id] {
			t.Errorf("AllLaws lists %s but no Errorf call site in contract_test.go emits it", id)
		}
	}
	for id := range used {
		if !seen[id] {
			t.Errorf("contract_test.go emits %s from an Errorf call site but AllLaws does not list it", id)
		}
	}
}

// TestCheckResult_AcceptsWellFormedVariants (AC-3, confirmatory): one well-formed value of each
// Result variant, run through CheckResult with a recorder, records zero failures. Trivially
// true against the Stage 2.5 no-op stub (see this file's header) -- expected progress, not
// proof CheckResult's real checks are correct.
func TestCheckResult_AcceptsWellFormedVariants(t *testing.T) {
	future := time.Now().Add(time.Hour)
	cases := []struct {
		name string
		r    submission.Result
	}{
		{"Accepted", submission.Accepted{IRN: "IRN-1"}},
		{"Rejected", submission.Rejected{Reasons: []submission.Reason{{Code: "E1", Message: "bad"}}}},
		{"Pending", submission.Pending{Ref: "ref-1", PollAfter: future}},
		{"Retryable", submission.Retryable{Err: errors.New("boom")}},
	}
	for _, tc := range cases {
		rec := &lawRecorder{}
		CheckResult(rec, tc.name, tc.r)
		if len(rec.messages) != 0 {
			t.Errorf("CheckResult(%s) recorded %d failure(s), want 0: %v", tc.name, len(rec.messages), rec.messages)
		}
	}
}

// TestCheckResult_ShortCircuitsOnNilResult (AC-3, RED-FIRST): a nil Result, run through
// CheckResult, records EXACTLY {L06} -- none of L07-L10 (disjointness rule 2: L06 short-
// circuits and must not fall through to evaluate the per-variant laws against a value that
// isn't one). Against the Stage 2.5 no-op stub, CheckResult never calls Errorf at all, so the
// recorded set is {} rather than {L06} -- a real assertion failure, not a compile error.
func TestCheckResult_ShortCircuitsOnNilResult(t *testing.T) {
	rec := &lawRecorder{}
	CheckResult(rec, "nil-result", nil)

	ids := rec.lawIDs()
	if !ids["L06"] {
		t.Fatalf("CheckResult(nil) recorded law ids %v, want exactly {L06}", ids)
	}
	if len(ids) != 1 {
		t.Fatalf("CheckResult(nil) recorded law ids %v, want exactly {L06} (L07-L10 must not be "+
			"evaluated once L06 has already failed)", ids)
	}
}

// TestCheckEvidence_AcceptsWellFormedEvidence (AC-3, confirmatory): well-formed evidence (a
// reached-wire attempt with a status/latency) and separately the zero Evidence both record zero
// failures. Trivially true against the Stage 2.5 no-op stub (see this file's header).
func TestCheckEvidence_AcceptsWellFormedEvidence(t *testing.T) {
	status := 200
	latency := 5
	ev := submission.Evidence{
		ReachedWire: true,
		HTTPStatus:  &status,
		LatencyMS:   &latency,
	}
	rec := &lawRecorder{}
	CheckEvidence(rec, "well-formed", ev, "idem-1")
	if len(rec.messages) != 0 {
		t.Errorf("CheckEvidence(well-formed) recorded %d failure(s), want 0: %v", len(rec.messages), rec.messages)
	}

	rec2 := &lawRecorder{}
	CheckEvidence(rec2, "zero-value", submission.Evidence{}, "idem-1")
	if len(rec2.messages) != 0 {
		t.Errorf("CheckEvidence(zero Evidence) recorded %d failure(s), want 0: %v", len(rec2.messages), rec2.messages)
	}
}

// TestContractSuite_UsesNarrowT (AC-6, REGRESSION GUARD -- not red-first, see this file's
// header): a recorder implementing only Helper and Errorf (nothing else) is handed to
// RunAdapterContract. Like TestValidatorClient_DoesNotImportValidationPackage
// (internal/invoice/validator_test.go:440-451) and TestSubmissionPackage_
// DoesNotImportInvoicePackage (deps_test.go, this package), the property this checks already
// holds for ANY RunAdapterContract that never reaches for t.Run or Fatalf -- including this
// subtask's own Stage 2.5 no-op stub, which trivially never calls either. It passes from the
// moment it compiles, not as a demonstrated red-to-green transition; it exists to lock the
// property against a later draft that reaches for a subtest or a Fatalf -- lawRecorder has no
// Run or Fatalf method, so such a draft would fail to COMPILE, not merely fail an assertion.
func TestContractSuite_UsesNarrowT(t *testing.T) {
	rec := &lawRecorder{}
	RunAdapterContract(rec, newRef)
	// This line existing and running IS the assertion: RunAdapterContract's parameter type is
	// ContractT, and rec satisfies it with exactly {Helper, Errorf} -- nothing more was needed
	// to type-check, and nothing panicked.
}

// TestContractSuite_RunsWithoutDatabase (AC-2, confirmatory): with DATABASE_URL and
// DATABASE_MIGRATION_URL unset for the process, RunAdapterContract runs to completion and
// records zero failures -- unlike failure_modes_test.go's DB-backed suite in this same package,
// which self-skips when those are unset, this suite never skips (it touches neither).
func TestContractSuite_RunsWithoutDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DATABASE_MIGRATION_URL", "")

	rec := &lawRecorder{}
	RunAdapterContract(rec, newRef)
	if len(rec.messages) != 0 {
		t.Errorf("RunAdapterContract with DATABASE_URL/DATABASE_MIGRATION_URL unset recorded %d "+
			"failure(s), want 0: %v", len(rec.messages), rec.messages)
	}
}
