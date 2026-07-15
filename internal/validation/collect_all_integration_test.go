// M3-10-02 (Core AC 2): DB-backed proof that Engine.Evaluate collects EVERY
// applicable violation across the migration-seeded v1 rule set in one pass
// -- never fail-fast -- and stamps the result with the active version it
// evaluated against. Chains the same DB-load -> engine-evaluate pattern
// seed_test.go's loadV1/hasViolation/violationKeys helpers were built for
// (see that file's header), but exercises breadth across SIX simultaneously
// broken fields in one payload rather than one demo fixture's two.
//
// manyViolationsPayload() is defined here as a clean package-level func
// specifically so M3-10-05's golden suite can reuse it verbatim -- do not
// inline it into a single test.
//
// Run (same env gate as the rest of the package):
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/validation/...
package validation

import (
	"reflect"
	"testing"
)

// manyViolationsPayload returns a validInvoicePayload() mutated to break a
// spread of independent rules at once (invoice-number-required,
// issue-date-required, supplier-name-required, currency-allowed,
// supplier-tin-format, subtotal-non-negative, vat-standard-rate,
// no-duplicate-line-items, line-items-sum-subtotal -- nine rule keys total
// once vat-standard-rate's and line-items-sum-subtotal's fallout from the
// negative subtotal is included), while leaving every other seeded v1 rule
// passing:
//   - invoice_number and issue_date are deleted outright (presence rules fire).
//   - supplier.name is deleted, but supplier.tin stays present-but-malformed,
//     so supplier-tin-required passes while supplier-tin-format fires.
//   - currency is set to a disallowed value, but stays present, so
//     currency-required passes while currency-allowed fires.
//   - subtotal goes negative, but stays present, so subtotal-required passes
//     while subtotal-non-negative fires; the same negative subtotal also
//     drags vat-standard-rate's expected base far from the (now bogus) vat
//     value, so that fires too, AND leaves the two positive line amounts
//     (10*100 + 1*5 = 1005) unable to reconcile to it, so
//     line-items-sum-subtotal (line_sum) fires as well.
//   - vat itself stays present and non-negative, so vat-required and
//     vat-non-negative both pass.
//   - total and line_items are left untouched (total-*, line-items-required
//     all pass), except line_items gains a second entry sharing id "1" with
//     the first, so no-duplicate-line-items (cel) fires.
//
// This is the shared fixture reused verbatim by M3-10-05's golden suite --
// keep its shape here, do not fork a copy there.
func manyViolationsPayload() Payload {
	p := validInvoicePayload()
	inv := invoiceOf(p)

	delete(inv, "invoice_number")
	delete(inv, "issue_date")
	delete(inv["supplier"].(map[string]any), "name")
	inv["currency"] = "USD"
	inv["supplier"].(map[string]any)["tin"] = "BADTIN"
	inv["subtotal"] = -5.0
	inv["vat"] = 999.0

	items := inv["line_items"].([]any)
	dup := map[string]any{
		"id":          "1",
		"description": "Widget (dup)",
		"quantity":    1.0,
		"unit_price":  5.0,
		"line_total":  5.0,
	}
	inv["line_items"] = append(items, dup)

	return p
}

// TestCollectAll_ManyViolationsBreadth (Core AC 2): a payload with nine
// independently-broken rules returns EXACTLY those nine violation keys,
// sorted, each carrying a non-empty rule key/severity/message -- proving the
// engine collects every applicable violation in one pass rather than
// stopping at the first. A second control payload with a single defect
// returns exactly one violation, guarding against an over-broad
// "everything fires regardless of input" bug that would make the breadth
// assertion above vacuously true.
func TestCollectAll_ManyViolationsBreadth(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadV1(t, app)
	engine := NewDefaultEngine()

	t.Run("many_violations", func(t *testing.T) {
		result, err := engine.Evaluate(manyViolationsPayload(), rs)
		if err != nil {
			t.Fatalf("Evaluate(manyViolationsPayload): %v", err)
		}

		if result.RuleSetVersion != 1 {
			t.Errorf("RuleSetVersion = %d, want 1", result.RuleSetVersion)
		}

		wantKeys := []string{
			"currency-allowed",
			"invoice-number-required",
			"issue-date-required",
			"line-items-sum-subtotal",
			"no-duplicate-line-items",
			"subtotal-non-negative",
			"supplier-name-required",
			"supplier-tin-format",
			"vat-standard-rate",
		}
		if got := violationKeys(result); !reflect.DeepEqual(got, wantKeys) {
			t.Errorf("violation keys = %v, want %v (collect-ALL across nine independently-broken rules, sorted)", got, wantKeys)
		}

		for _, v := range result.Violations {
			if v.RuleKey == "" {
				t.Errorf("violation %+v has empty RuleKey", v)
			}
			if v.Severity == "" {
				t.Errorf("violation %+v has empty Severity", v)
			}
			if v.Message == "" {
				t.Errorf("violation %+v has empty Message", v)
			}
		}

		// Spot-check three of the nine triples (one required, one enum, one
		// cel rule type) against the migration-seeded content itself
		// (migrations/20260711121327_seed_mbs_v1.sql) rather than only
		// asserting non-empty above -- proves each triple carries the real
		// seeded rule/severity/message, not just placeholder strings, and
		// would catch a triple that came back non-empty but wrong (e.g. a
		// swapped or truncated message).
		wantContent := map[string]struct {
			severity Severity
			message  string
		}{
			"currency-allowed":        {"error", "Currency must be NGN."},
			"invoice-number-required": {"error", "Invoice number is required."},
			"no-duplicate-line-items": {"error", "Invoice contains duplicate line items (a line id appears more than once)."},
		}
		for _, v := range result.Violations {
			want, ok := wantContent[v.RuleKey]
			if !ok {
				continue
			}
			if v.Severity != want.severity {
				t.Errorf("%s: Severity = %q, want %q", v.RuleKey, v.Severity, want.severity)
			}
			if v.Message != want.message {
				t.Errorf("%s: Message = %q, want %q", v.RuleKey, v.Message, want.message)
			}
		}
	})

	t.Run("single_defect_control", func(t *testing.T) {
		p := validInvoicePayload()
		invoiceOf(p)["currency"] = "USD"

		result, err := engine.Evaluate(p, rs)
		if err != nil {
			t.Fatalf("Evaluate(single defect payload): %v", err)
		}

		wantKeys := []string{"currency-allowed"}
		if got := violationKeys(result); !reflect.DeepEqual(got, wantKeys) {
			t.Errorf("violation keys = %v, want %v (a single broken rule must fire alone, not cascade into unrelated rules)", got, wantKeys)
		}
	})

	t.Run("clean_payload_zero_violations", func(t *testing.T) {
		// Guards the other end of the breadth assertion: a payload that
		// breaks none of the nine rules above returns no violations at
		// all, AND the result is still stamped with the active version --
		// the version stamp is not something that only appears when there
		// happens to be a violation to attach it to.
		result, err := engine.Evaluate(validInvoicePayload(), rs)
		if err != nil {
			t.Fatalf("Evaluate(validInvoicePayload): %v", err)
		}

		if result.RuleSetVersion != 1 {
			t.Errorf("RuleSetVersion = %d, want 1 (stamped even on a clean pass)", result.RuleSetVersion)
		}
		if result.Violations == nil {
			t.Error("Violations = nil, want [] (never nil, even when empty -- Result doc)")
		}
		if len(result.Violations) != 0 {
			t.Errorf("Violations = %v, want none -- validInvoicePayload() breaks no seeded v1 rule", result.Violations)
		}
	})
}
