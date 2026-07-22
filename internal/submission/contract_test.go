// M5-02-06 (task-220): the reusable, DB-free, network-free adapter contract suite --
// ContractT, the complete AllLaws id list, the Canonical corpus, and the real sixteen-law
// RunAdapterContract / CheckResult / CheckEvidence implementation (Stage 2, executor).
//
// HONEST FRAMING (do not relabel any test's Kind below without re-reading this): even now
// that RunAdapterContract is real, "the reference adapter passes with zero failures" only
// proves refAdapter is lawful -- it is NOT proof the suite actually REJECTS a non-conforming
// adapter. That proof is demonstrated in M5-02-07 alone, a separate FINAL subtask, on
// purpose -- do not fold it in here. Of the six specs in this file:
//   - TestAllLaws_IdsAreUniqueAndUsed was genuinely RED against the Stage 2.5 no-op stub (see
//     its own doc comment) and is now green against the real law checks below.
//   - TestCheckResult_ShortCircuitsOnNilResult was genuinely RED against the stub and is now
//     green against CheckResult's real default-arm short-circuit.
//   - TestContractSuite_UsesNarrowT is a regression guard, not a red-first spec: it passes
//     because RunAdapterContract never calls t.Run or Fatalf. Framed like
//     TestValidatorClient_DoesNotImportValidationPackage
//     (internal/invoice/validator_test.go:440-451) and TestSubmissionPackage_
//     DoesNotImportInvoicePackage (deps_test.go in this package): the property holds for any
//     RunAdapterContract that never reaches for t.Run/Fatalf, so this exists to lock that
//     property against a later draft, not to record a transition.
//   - TestCheckResult_AcceptsWellFormedVariants, TestCheckEvidence_AcceptsWellFormedEvidence
//     and TestContractSuite_RunsWithoutDatabase are confirmatory: green both against the old
//     stub (trivially) and against the real implementation (genuinely) -- expected
//     engineering progress, not proof the suite rejects bad adapters.
//
// Package submission_test (external), matching every other test file in this package
// (exchange_test.go, exchange_db_test.go, failure_modes_test.go, worker_smoke_test.go,
// registry_test.go, ...). TestMain already exists at failure_modes_test.go:57 -- one per test
// binary -- so this file defines none. No testify. No t.Skip anywhere: these tests are pure Go
// with no DB and no network, and internal/tools/rlsgate fails the CI queue job on any
// test-level skip (or on zero tests observed), so they must run unconditionally.
package submission_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"reflect"
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
// (a1, a2) so L02 (Name/Version identity across two fresh instances) and, by running the
// Canonical corpus's Transform calls one on each instance, L03/L04 can compare across two
// fresh instances too rather than just repeated calls on one.
//
// recover() lives HERE, at each adapter-method call boundary (the unexported call*
// helpers below) -- never inside CheckResult/CheckEvidence, never exposed through ContractT.
// Only this function (via those helpers) calls the adapter's methods, so only those call sites
// can recover from one of them panicking:
//   - Poll's call site recovers under L14 ONLY.
//   - Name/Version/Transform/Submit's call sites each recover under L15 ONLY.
//     L14 and L15 partition the panic surface; Poll is never also checked under L15, or an
//     adapter that panics on Poll would trip both and break M5-02-07's set-equality assertions.
//
// When a call*helper's recover fires, it records the panic under its own law and reports
// "panicked" to its caller; every downstream check that would otherwise read that call's
// (necessarily zero-value) result is skipped for that one call -- the same reasoning the
// L14/L15 partition rests on, generalised: a caught panic must not cascade into ALSO tripping
// an unrelated law from the zero value left behind, or M5-02-07's per-law
// recorded-set-equals-exactly-{law} assertions become unachievable.
func RunAdapterContract(t ContractT, newAdapter func() submission.Adapter) {
	t.Helper()

	const cancelledIdemKey = "contract-idem-cancelled-context"
	const emptyWireIdemKey = "contract-idem-empty-wire"

	a1 := newAdapter()
	a2 := newAdapter()
	ctx := context.Background()

	// L01/L02: Name()/Version() non-empty, stable across repeated calls on the same
	// instance, and stable across two fresh instances.
	name1, name1Panicked := callName(t, "instance-1", a1)
	name1Repeat, name1RepeatPanicked := callName(t, "instance-1 (repeat)", a1)
	version1, version1Panicked := callVersion(t, "instance-1", a1)
	version1Repeat, version1RepeatPanicked := callVersion(t, "instance-1 (repeat)", a1)
	name2, name2Panicked := callName(t, "instance-2", a2)
	version2, version2Panicked := callVersion(t, "instance-2", a2)

	if !name1Panicked && name1 == "" {
		t.Errorf("L01: instance-1: Name() must be non-empty")
	}
	if !version1Panicked && version1 == "" {
		t.Errorf("L01: instance-1: Version() must be non-empty")
	}
	if !name1Panicked && !name1RepeatPanicked && name1 != name1Repeat {
		t.Errorf("L02: Name() must be stable across repeated calls on the same instance: "+
			"got %q then %q", name1, name1Repeat)
	}
	if !version1Panicked && !version1RepeatPanicked && version1 != version1Repeat {
		t.Errorf("L02: Version() must be stable across repeated calls on the same instance: "+
			"got %q then %q", version1, version1Repeat)
	}
	if !name1Panicked && !name2Panicked && name1 != name2 {
		t.Errorf("L02: Name() must be stable across two fresh instances: got %q and %q", name1, name2)
	}
	if !version1Panicked && !version2Panicked && version1 != version2 {
		t.Errorf("L02: Version() must be stable across two fresh instances: got %q and %q", version1, version2)
	}

	// L03/L04/L05: drive the fixed Canonical corpus through Transform on TWO fresh
	// instances (a1, a2) with the same input, then feed instance-1's resulting Wire into
	// Submit for the per-value laws L06-L13.
	for _, tc := range canonicalCorpus {
		before := deepCopyCanonical(tc.c)
		wireA, errA, panickedA := callTransform(t, tc.name+" (instance-1)", a1, ctx, tc.c)
		wireB, errB, panickedB := callTransform(t, tc.name+" (instance-2)", a2, ctx, tc.c)
		after := deepCopyCanonical(tc.c)

		if !reflect.DeepEqual(before, after) {
			t.Errorf("L04: %s: Transform mutated its Canonical argument", tc.name)
		}

		if !panickedA && !panickedB {
			if (errA == nil) != (errB == nil) {
				t.Errorf("L03: %s: Transform(c) returned an error from one adapter instance "+
					"but not the other: instance-1 err=%v, instance-2 err=%v", tc.name, errA, errB)
			} else if errA == nil && !bytes.Equal(wireA, wireB) {
				t.Errorf("L03: %s: Transform(c) is not deterministic: two fresh adapter "+
					"instances produced different Wire bytes for the same input", tc.name)
			}
		}

		if !panickedA {
			if errA != nil {
				if len(wireA) != 0 {
					t.Errorf("L05: %s: Transform returned a non-empty Wire (%d byte(s)) "+
						"alongside a non-nil error", tc.name, len(wireA))
				}
			} else if len(wireA) == 0 {
				t.Errorf("L05: %s: Transform returned an empty Wire alongside a nil error", tc.name)
			}
		}

		idemKey := "contract-idem-" + tc.name
		result, evidence, submitPanicked := callSubmit(t, tc.name, a1, ctx, wireA, idemKey)
		if !submitPanicked {
			CheckResult(t, tc.name+" Submit", result)
			CheckEvidence(t, tc.name+" Submit", evidence, idemKey)
		}
	}

	// L15 (Submit): an empty Wire specifically -- not necessarily produced by any corpus
	// case above (a successful Transform of the zero Canonical still marshals to non-empty
	// JSON bytes, so "zero Canonical" and "empty Wire" are two genuinely different cases).
	emptyWireResult, emptyWireEvidence, emptyWirePanicked :=
		callSubmit(t, "empty-wire", a1, ctx, submission.Wire{}, emptyWireIdemKey)
	if !emptyWirePanicked {
		CheckResult(t, "empty-wire Submit", emptyWireResult)
		CheckEvidence(t, "empty-wire Submit", emptyWireEvidence, emptyWireIdemKey)
	}

	// L14: Poll with a Ref this run never issued through a Pending outcome must not panic
	// and must return a well-formed Result + Evidence -- L06-L13 apply through
	// CheckResult/CheckEvidence, same as any other Result/Evidence pair.
	unissuedRef := submission.Ref("contract-suite-never-issued-ref")
	unissuedResult, unissuedEvidence, unissuedPanicked := callPoll(t, "unissued-ref", a1, ctx, unissuedRef)
	if !unissuedPanicked {
		CheckResult(t, "unissued-ref Poll", unissuedResult)
		CheckEvidence(t, "unissued-ref Poll", unissuedEvidence, "")
	}

	// L16: an already-cancelled context makes Submit and Poll return Retryable with
	// Evidence.ReachedWire false, regardless of what either would otherwise have done.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	cancelledSubmitResult, cancelledSubmitEvidence, cancelledSubmitPanicked := callSubmit(
		t, "cancelled-context", a1, cancelledCtx,
		submission.Wire("contract-suite-cancelled-ctx-wire"), cancelledIdemKey)
	if !cancelledSubmitPanicked {
		if _, ok := cancelledSubmitResult.(submission.Retryable); !ok {
			t.Errorf("L16: cancelled-context: Submit must return Retryable when ctx is "+
				"already cancelled, got %T", cancelledSubmitResult)
		}
		if cancelledSubmitEvidence.ReachedWire {
			t.Errorf("L16: cancelled-context: Submit's Evidence.ReachedWire must be false " +
				"when ctx is already cancelled")
		}
		CheckResult(t, "cancelled-context Submit", cancelledSubmitResult)
		CheckEvidence(t, "cancelled-context Submit", cancelledSubmitEvidence, cancelledIdemKey)
	}

	cancelledPollResult, cancelledPollEvidence, cancelledPollPanicked := callPoll(
		t, "cancelled-context", a1, cancelledCtx, submission.Ref("contract-suite-cancelled-ctx-ref"))
	if !cancelledPollPanicked {
		if _, ok := cancelledPollResult.(submission.Retryable); !ok {
			t.Errorf("L16: cancelled-context: Poll must return Retryable when ctx is "+
				"already cancelled, got %T", cancelledPollResult)
		}
		if cancelledPollEvidence.ReachedWire {
			t.Errorf("L16: cancelled-context: Poll's Evidence.ReachedWire must be false " +
				"when ctx is already cancelled")
		}
		CheckResult(t, "cancelled-context Poll", cancelledPollResult)
		CheckEvidence(t, "cancelled-context Poll", cancelledPollEvidence, "")
	}
}

// deepCopyCanonical returns a Canonical holding entirely independent copies of every pointer
// and slice c reaches -- used to snapshot a corpus case's input before/after a Transform call
// so L04 (no input mutation) can tell a genuine mutation apart from a shared backing
// array/pointer that both the snapshot and the live argument happen to point at. A nil Lines
// (the "no-lines" corpus case) stays nil, never becomes an empty-but-non-nil slice.
func deepCopyCanonical(c submission.Canonical) submission.Canonical {
	cp := c
	if c.IssueDate != nil {
		d := *c.IssueDate
		cp.IssueDate = &d
	}
	cp.Supplier = deepCopyParty(c.Supplier)
	cp.Buyer = deepCopyParty(c.Buyer)
	cp.Currency = deepCopyStringPtr(c.Currency)
	cp.Subtotal = deepCopyStringPtr(c.Subtotal)
	cp.VAT = deepCopyStringPtr(c.VAT)
	cp.Total = deepCopyStringPtr(c.Total)
	if c.Lines != nil {
		lines := make([]submission.CanonicalLine, len(c.Lines))
		for i, l := range c.Lines {
			lines[i] = submission.CanonicalLine{
				LineID:      l.LineID,
				LineNo:      l.LineNo,
				Description: deepCopyStringPtr(l.Description),
				Quantity:    deepCopyStringPtr(l.Quantity),
				UnitPrice:   deepCopyStringPtr(l.UnitPrice),
				LineTotal:   deepCopyStringPtr(l.LineTotal),
				LineTax:     deepCopyStringPtr(l.LineTax),
			}
		}
		cp.Lines = lines
	}
	return cp
}

func deepCopyParty(p submission.Party) submission.Party {
	return submission.Party{TIN: deepCopyStringPtr(p.TIN), Name: deepCopyStringPtr(p.Name)}
}

func deepCopyStringPtr(s *string) *string {
	if s == nil {
		return nil
	}
	v := *s
	return &v
}

// callName invokes a.Name(), recovering a panic under L15 (Name/Version/Transform/Submit must
// never panic on any input -- Poll's panic surface is L14 alone, see callPoll). label
// identifies which check this call feeds, for the failure message. panicked tells the caller
// to skip any further check that would read the (necessarily zero-value) name.
func callName(t ContractT, label string, a submission.Adapter) (name string, panicked bool) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("L15: %s: Name() panicked: %v", label, r)
			panicked = true
		}
	}()
	name = a.Name()
	return
}

// callVersion is callName's twin for Version() -- same L15 attribution, same panicked signal.
func callVersion(t ContractT, label string, a submission.Adapter) (version string, panicked bool) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("L15: %s: Version() panicked: %v", label, r)
			panicked = true
		}
	}()
	version = a.Version()
	return
}

// callTransform invokes a.Transform, recovering a panic under L15. panicked tells the caller
// to skip L03/L05 checks that would otherwise compare against a zero-value Wire/error that
// only exists because the real call never completed.
func callTransform(t ContractT, label string, a submission.Adapter, ctx context.Context, c submission.Canonical) (w submission.Wire, err error, panicked bool) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("L15: %s: Transform panicked: %v", label, r)
			panicked = true
		}
	}()
	w, err = a.Transform(ctx, c)
	return
}

// callSubmit invokes a.Submit, recovering a panic under L15. panicked tells the caller to skip
// CheckResult/CheckEvidence and any L16 assertion for this call, since a caught panic leaves
// only zero-value Result/Evidence that would otherwise spuriously trip L06 (via CheckResult)
// on top of the L15 violation already recorded here.
func callSubmit(t ContractT, label string, a submission.Adapter, ctx context.Context, w submission.Wire, idemKey string) (r submission.Result, ev submission.Evidence, panicked bool) {
	t.Helper()
	defer func() {
		if rec := recover(); rec != nil {
			t.Errorf("L15: %s: Submit panicked: %v", label, rec)
			panicked = true
		}
	}()
	r, ev = a.Submit(ctx, w, idemKey)
	return
}

// callPoll invokes a.Poll, recovering a panic under L14 ONLY -- Poll's panic surface is
// disjoint from L15 (see the parent story's "L14 and L15 partition the panic surface"
// disjointness rule): if Poll's panic were ALSO checked under L15, an adapter that panics on
// Poll would trip both laws and break M5-02-07's recorded-set-equals-exactly-{L14} assertion.
// panicked tells the caller to skip CheckResult/CheckEvidence and any L16 assertion for this
// call, for the same reason callSubmit's does.
func callPoll(t ContractT, label string, a submission.Adapter, ctx context.Context, ref submission.Ref) (r submission.Result, ev submission.Evidence, panicked bool) {
	t.Helper()
	defer func() {
		if rec := recover(); rec != nil {
			t.Errorf("L14: %s: Poll panicked: %v", label, rec)
			panicked = true
		}
	}()
	r, ev = a.Poll(ctx, ref)
	return
}

// CheckResult evaluates L06 through L10 against r, labeling every failure with ctxName. A nil
// Result, or a pointer variant (*Accepted etc. -- isResult() has a value receiver, so the four
// pointer types satisfy Result too, per the parent story's L06 aliasing-hazard note), fails L06
// and returns immediately without evaluating L07-L10: the switch below only has cases for the
// four VALUE variants, so a pointer variant (or nil, or any other type) falls to the default
// arm exactly as intended -- no `case *Accepted:` is added, since that would defeat the point.
func CheckResult(t ContractT, ctxName string, r submission.Result) {
	t.Helper()

	switch v := r.(type) {
	case submission.Accepted:
		if strings.TrimSpace(v.IRN) == "" {
			t.Errorf("L07: %s: Accepted.IRN must be non-empty and not blank, got %q", ctxName, v.IRN)
		}
		if v.CSID != "" && strings.TrimSpace(v.CSID) == "" {
			t.Errorf("L07: %s: Accepted.CSID, when non-empty, must not be blank, got %q", ctxName, v.CSID)
		}
		if v.QRPayload != "" && strings.TrimSpace(v.QRPayload) == "" {
			t.Errorf("L07: %s: Accepted.QRPayload, when non-empty, must not be blank, got %q", ctxName, v.QRPayload)
		}

	case submission.Rejected:
		if len(v.Reasons) == 0 {
			t.Errorf("L08: %s: Rejected.Reasons must be non-empty", ctxName)
		}
		for i, reason := range v.Reasons {
			if reason.Code == "" {
				t.Errorf("L08: %s: Rejected.Reasons[%d].Code must be non-empty", ctxName, i)
			}
			if reason.Message == "" {
				t.Errorf("L08: %s: Rejected.Reasons[%d].Message must be non-empty", ctxName, i)
			}
		}

	case submission.Pending:
		if v.Ref == "" {
			t.Errorf("L09: %s: Pending.Ref must be non-empty", ctxName)
		}
		if v.PollAfter.IsZero() {
			t.Errorf("L09: %s: Pending.PollAfter must be non-zero", ctxName)
		}

	case submission.Retryable:
		if v.Err == nil {
			t.Errorf("L10: %s: Retryable.Err must be non-nil", ctxName)
		}

	default:
		t.Errorf("L06: %s: Result must be non-nil and type-switch into exactly one of the "+
			"four Accepted/Rejected/Pending/Retryable VALUE variants, got %T", ctxName, r)
		return
	}
}

// CheckEvidence evaluates L11 through L13 against ev, labeling every failure with ctxName.
// idemKey is the idempotency key passed to the Submit call that produced ev; pass "" for
// evidence that did not originate from a Submit call (e.g. Poll's) -- L13 has nothing to
// compare there and is vacuously satisfied by an adapter that sets no Idempotency-Key header at
// all. That is intentional, not a gap: the law only constrains an adapter that DOES expose
// idempotency via this header, guarding against one that mints its own key and silently breaks
// dedupe.
func CheckEvidence(t ContractT, ctxName string, ev submission.Evidence, idemKey string) {
	t.Helper()

	// L11: !ReachedWire implies no HTTPStatus, no ResponseBody, no ResponseHeaders -- the
	// contrapositive (HTTPStatus != nil implies ReachedWire) is the same statement, so one
	// direction of checks covers both halves of the law.
	if !ev.ReachedWire {
		if ev.HTTPStatus != nil {
			t.Errorf("L11: %s: Evidence.HTTPStatus must be nil when ReachedWire is false, got %d", ctxName, *ev.HTTPStatus)
		}
		if ev.ResponseBody != nil {
			t.Errorf("L11: %s: Evidence.ResponseBody must be nil when ReachedWire is false, got %q", ctxName, *ev.ResponseBody)
		}
		if len(ev.ResponseHeaders) != 0 {
			t.Errorf("L11: %s: Evidence.ResponseHeaders must be empty when ReachedWire is false, got %v", ctxName, ev.ResponseHeaders)
		}
	}

	// L12.
	if ev.LatencyMS != nil && *ev.LatencyMS < 0 {
		t.Errorf("L12: %s: Evidence.LatencyMS must be >= 0 when set, got %d", ctxName, *ev.LatencyMS)
	}

	// L13: conditional on the header actually being present (see doc comment above).
	if v, present := idempotencyKeyValue(ev.RequestHeaders); present && v != idemKey {
		t.Errorf("L13: %s: Evidence.RequestHeaders carries Idempotency-Key %q, want %q "+
			"(the idemKey passed to Submit)", ctxName, v, idemKey)
	}
}

// idempotencyKeyValue returns the value h carries for the Idempotency-Key header, matched by
// CANONICALISING EACH STORED KEY as it is iterated -- mirrors ScrubHeaders' own reasoning
// (exchange.go) that a per-name h.Get would miss a header whose map key was never
// canonicalised (an http.Header built as a map literal, rather than via Set/Add, stores its
// keys verbatim). present is false when no such header is there at all, which is what makes
// L13 vacuous for an adapter that never sets this header.
func idempotencyKeyValue(h http.Header) (value string, present bool) {
	for k, vs := range h {
		if textproto.CanonicalMIMEHeaderKey(k) == "Idempotency-Key" {
			if len(vs) > 0 {
				return vs[0], true
			}
			return "", true
		}
	}
	return "", false
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
