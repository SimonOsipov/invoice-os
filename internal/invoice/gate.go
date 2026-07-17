// M4-04-06 (task-113): the validate GATE's orchestration layer -- the piece
// that turns "an invoice", "a rule set over in 04", and "a store that can
// atomically stamp a verdict" into the single operation POST
// /v1/invoices/{id}/validate exposes ([gate-orchestration]).
//
// Gate mirrors internal/importer.Service's shape: a struct over the concrete
// stores/clients it drives, constructed once at boot in cmd/invoice/main.go.
// It owns the ORDER of the three steps and nothing else -- the payload mapping
// is payload.go's, the wire contract is validator.go's, the atomicity is
// Store.ApplyValidation's. It adds no rules of its own.
//
// The order is the design, and it is fixed:
//
//	Store.Get(id)                      tx #1, RLS-scoped, HYDRATES line items
//	MBSPayload + contentFingerprint    pure
//	Validator.Validate                 the HTTP call to 04, NO tx open
//	Store.ApplyValidation              tx #2: FOR UPDATE, status re-check,
//	                                   fingerprint re-check, write, transition,
//	                                   audit -- all or nothing
//
// Step 3 sits deliberately BETWEEN the two transactions ([toctou-staleness]):
// holding a row lock across a network call to another service would pin a pool
// connection under unbounded remote latency -- 500x over on an import. The cost
// of that choice is that the invoice can change under the evaluation, which is
// exactly what step 4's fingerprint re-check exists to catch.
//
// GET, NEVER LIST. Store.Get hydrates LineItems; Store.List leaves them nil by
// design ([D7], store.go:209-213). MBSPayload is pure and CANNOT tell a nil
// LineItems from a genuinely line-less invoice -- it would omit line_items and
// make line-items-required violate a PERFECTLY VALID invoice, a confident wrong
// verdict on every invoice of a 500-row import. So every invoice reaching
// Evaluate must be Get-sourced (the real path) or CreateInput-built (dry-run,
// M4-04-07's [payload-mapper]) -- never List-sourced. This is why ValidateBatch
// takes already-hydrated []Invoice rather than a []string of ids it would be
// tempted to fetch in bulk.
package invoice

import (
	"context"
	"fmt"
)

// Gate orchestrates the validate gate over the store and the 04 client. The
// caller owns the store's pool lifecycle and the validator's http.Client,
// exactly as importer.NewService's caller owns both stores'.
type Gate struct {
	store     *Store
	validator *Validator
}

// NewGate wraps the two dependencies the orchestration drives.
func NewGate(store *Store, v *Validator) *Gate {
	return &Gate{store: store, validator: v}
}

// EvalItem is one invoice submitted to Evaluate. Ref is caller-chosen and
// caller-opaque -- 04 echoes it back untouched and never interprets it. The
// real path sends the invoice id; the dry-run path sends the invoice_number,
// since no id exists yet (M4-04-07).
//
// Invoice must be Get-sourced or CreateInput-built. See this file's header for
// why a List-sourced one is a correctness bug the mapper cannot detect.
type EvalItem struct {
	Ref     string
	Invoice Invoice
}

// EvalResult is Evaluate's outcome: the single rule-set the WHOLE batch was
// evaluated against (one load means one version -- stamped once, not per item),
// plus every sent ref's violations.
//
// ByRef is TOTAL over the sent refs, inherited from Validator.Validate's own
// totality guarantee: it refuses any response that does not cover them. That
// matters because an absent map key returns nil, which reads to a caller as "no
// violations" -- i.e. as a clean verdict on an invoice 04 never actually
// judged.
type EvalResult struct {
	RuleSetVersion   int
	RuleSetVersionID string
	ByRef            map[string][]Violation
}

// Evaluate maps every item through MBSPayload and submits them to 04 in
// EXACTLY ONE round trip, whatever the batch size ([batch-of-one]).
//
// One call, not one per item, is load-bearing: it is what keeps a 500-invoice
// import to a single HTTP request rather than 500, and it is what makes the
// single-invoice gate below reuse this same batch contract instead of needing a
// second endpoint and a second client.
//
// It WRITES NOTHING -- that is what lets M4-04-07's dry-run reuse it verbatim.
//
// An empty batch short-circuits: no items means no violations, and it needs no
// network call to know that. This is not merely an optimization -- 04 answers a
// 400 to an empty batch, which validator.go correctly maps to ErrUpstream, so
// without this guard a caller with nothing to evaluate would get an OUTAGE
// error. That is reachable: an import whose every row is quarantined creates
// zero invoices and would hand ValidateBatch an empty slice. NOTE for callers:
// the short-circuit returns a zero-value RuleSetVersion/RuleSetVersionID --
// nothing was evaluated, so there is no version to report. Do not stamp a run
// with version 0.
func (g *Gate) Evaluate(ctx context.Context, items []EvalItem) (EvalResult, error) {
	if len(items) == 0 {
		return EvalResult{ByRef: map[string][]Violation{}}, nil
	}

	vitems := make([]ValidateItem, len(items))
	for i, it := range items {
		vitems[i] = ValidateItem{Ref: it.Ref, Invoice: MBSPayload(it.Invoice)}
	}

	res, err := g.validator.Validate(ctx, vitems)
	if err != nil {
		// ErrUpstream / ErrNoActiveRuleSet propagate WRAPPED AND INTACT, all the
		// way to statusForErr's 502/503. Never swallowed into an empty result:
		// an outage is not a verdict, and "we could not reach the rules" must
		// never render as "this invoice is clean".
		return EvalResult{}, err
	}

	return EvalResult{
		RuleSetVersion:   res.RuleSetVersion,
		RuleSetVersionID: res.RuleSetVersionID,
		ByRef:            res.ByRef,
	}, nil
}

// Validate runs the gate on ONE stored invoice -- the operation POST
// /v1/invoices/{id}/validate exposes, and the only route by which an invoice
// reaches validated ([gate-endpoint], [validated-is-earned]).
//
// It is re-callable at any time and re-calling it IS re-validation (Core AC
// #6): fix the invoice via Store.Update, call this again, and a now-clean draft
// promotes.
//
// A blocking violation is a normal, nil-error return -- "this invoice has
// errors" is a legitimate OUTCOME of the gate, not a failure of it. The caller
// reads the returned Invoice's Status/Violations to tell the two apart. Errors
// mean no verdict was reached at all.
//
// The draft pre-check is ADVISORY ONLY. Store.ApplyValidation's in-tx re-check
// under FOR UPDATE is the authoritative one; this one exists solely to save the
// 04 round trip on an invoice the write would refuse anyway (GAPI-11), and it
// is inherently racy -- the invoice can change status between here and there.
// That is fine precisely because it is not the check anything relies on.
func (g *Gate) Validate(ctx context.Context, id string) (Invoice, error) {
	// Get, not List: this is the ONLY call that hydrates line items. See the
	// file header.
	inv, err := g.store.Get(ctx, id)
	if err != nil {
		// ErrNotFound (unknown OR cross-tenant, RLS-scoped) -> 404 and
		// ErrValidation (a malformed non-uuid id, 22P02) -> 400 both propagate
		// through the EXISTING statusForErr cases, and 04 is never called.
		return Invoice{}, err
	}
	if inv.Status != StatusDraft {
		return Invoice{}, fmt.Errorf("%w: invoice is %s, the gate is draft-only", ErrNotDraft, inv.Status)
	}

	// Taken BEFORE the network call, against the same Invoice value the payload
	// is built from -- so it describes exactly the content 04 judged.
	// ApplyValidation compares it to the LOCKED row and refuses the write if the
	// invoice changed underneath ([toctou-staleness]).
	fingerprint := contentFingerprint(inv)

	// A batch of one: the same wire contract, client, and endpoint the importer
	// uses ([batch-of-one]). No second endpoint to keep in sync.
	res, err := g.Evaluate(ctx, []EvalItem{{Ref: inv.ID, Invoice: inv}})
	if err != nil {
		return Invoice{}, err
	}

	// ByRef is total over the sent refs (Validator.Validate enforces it), so
	// this key is present by construction -- a nil here would be an absent
	// verdict masquerading as a clean one.
	return g.store.ApplyValidation(ctx, inv.ID, res.ByRef[inv.ID], res.RuleSetVersionID, fingerprint)
}

// BatchOutcome is ValidateBatch's report: the one rule-set the whole batch was
// evaluated against, the clean/blocked split, and every invoice's violations
// keyed by id.
//
// Clean and WithViolations are computed HERE, by the same hasBlockingViolation
// predicate that decided the promotions, rather than left for the caller to
// re-derive. The predicate is unexported and package-local; a caller in another
// package (the importer) could only guess at it, and the obvious guess --
// len(violations) == 0 -- is WRONG: a warning-only invoice has violations and
// still promotes. Counting here is what keeps "clean" and "promoted to
// validated" the same set by construction.
type BatchOutcome struct {
	RuleSetVersion   int
	RuleSetVersionID string
	Clean            int
	WithViolations   int
	ByID             map[string][]Violation
}

// ValidateBatch runs the gate over a batch of already-created invoices in ONE
// 04 round trip -- the importer's path ([import-validates]).
//
// invs must be Get-sourced or Store.Create-returned Invoices, both of which
// carry hydrated LineItems (store.go:133-148). Store.Create already returns
// them hydrated, so the importer re-reads NOTHING for the whole batch. Passing
// List-sourced invoices here is a correctness bug -- see this file's header.
//
// The evaluation is one call; the writes are one transaction EACH (each
// invoice's verdict is independently atomic, via the same ApplyValidation the
// single-invoice path uses). That is DB round trips, not HTTP ones, and
// collapsing them is a measured-only remedy left to M4-04-08's perf work rather
// than pre-optimized here.
//
// A write failure aborts and returns raw: a DB fault mid-batch is an
// operational failure, and the caller (the importer) classifies it as one --
// never laundered into per-row reporting.
func (g *Gate) ValidateBatch(ctx context.Context, invs []Invoice) (BatchOutcome, error) {
	items := make([]EvalItem, len(invs))
	fingerprints := make(map[string]string, len(invs))
	for i, inv := range invs {
		items[i] = EvalItem{Ref: inv.ID, Invoice: inv}
		fingerprints[inv.ID] = contentFingerprint(inv)
	}

	res, err := g.Evaluate(ctx, items)
	if err != nil {
		// An unreachable 04 is an OUTAGE, not "every invoice is clean". Nothing
		// is written.
		return BatchOutcome{}, err
	}

	out := BatchOutcome{
		RuleSetVersion:   res.RuleSetVersion,
		RuleSetVersionID: res.RuleSetVersionID,
		ByID:             make(map[string][]Violation, len(invs)),
	}
	for _, inv := range invs {
		vs := res.ByRef[inv.ID]
		if _, err := g.store.ApplyValidation(ctx, inv.ID, vs, res.RuleSetVersionID, fingerprints[inv.ID]); err != nil {
			return BatchOutcome{}, fmt.Errorf("apply validation to invoice %s: %w", inv.ID, err)
		}
		out.ByID[inv.ID] = vs
		if hasBlockingViolation(vs) {
			out.WithViolations++
		} else {
			out.Clean++
		}
	}
	return out, nil
}
