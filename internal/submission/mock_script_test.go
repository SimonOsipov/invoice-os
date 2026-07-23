// mock_script_test.go: M5-03-02 (task-225) RED specs (QA Mode A) for the reserved buyer-TIN
// block, the scripted outcome table, the deterministic identifier synthesis, the rejection
// vocabulary, the four synthesized APP response bodies and the pending-handle codec.
//
// What this file proves, once green:
//
//   - that the seven allocated trigger TINs are the ONLY inputs that divert from accept, and
//     that everything else -- the empty string, a foreign TIN, an unallocated reserved suffix,
//     and a trigger with stray whitespace -- takes the accept path
//     ([non-reserved-defaults-to-accept]);
//   - that every allocated TIN can actually REACH submission (it matches the shipped
//     `buyer-tin-format` regex, read out of the live migration text rather than retyped) while
//     being mechanically unmintable by tools/fixturegen (it is Luhn-invalid)
//     ([reserved-is-luhn-invalid]);
//   - that the IRN, CSID and QR payload carry no clock, no randomness and no counter, and that
//     the IRN specifically is keyed on DOCUMENT IDENTITY rather than on content
//     ([irn-is-identity-keyed-not-content-keyed]);
//   - that the IRN is non-blank for every corpus shape, including the zero Canonical, because
//     L07 requires a non-blank Accepted.IRN;
//   - that the rejection speaks TWO vocabularies at once: their field name in the body they
//     "returned", our dotted path on the Reason we hand upward ([their-field-our-path]);
//   - and that the Ref codec enforces its own invariants rather than merely its encoding
//     ([ref-enforces-its-own-invariants]).
//
// PACKAGE submission (IN-PACKAGE), the same deliberate choice mock_wire_test.go documents at
// length: mockTriggerFor, mockIdentifiersFor, encodeMockRef, decodeMockRef, mockAllocations and
// the body builders are ALL unexported, and MockAdapter -- the exported seam -- does not exist
// until M5-03-03. Story decision [test-package-follows-the-symbol]. Do not "correct" this to
// package submission_test.
//
// RED STATE AT AUTHORING TIME: mock_script.go ships the complete type set, the real constant
// values and the final function signatures with deliberately no-op bodies, and mockAllocations
// is deliberately EMPTY -- so every failure below is an ASSERTION failure, not a compile error.
// Two things here are honestly NOT red-first and say so at their own site: the
// msLuhnCheckDigit positive control (it exercises this file's transcription, not production
// code) and the constant-level sanity checks that compare one symbol against another.
//
// AC-8's spec fails at authoring time because docs/mock-app-adapter.md does not exist yet.
// That is correct RED: the doc is the executor's deliverable.
//
// Helper prefix `ms` mirrors mock_wire_test.go's `mw`, so the two in-package files in this
// directory cannot collide. Standard library only -- no testify. No TestMain: exactly one
// exists (failure_modes_test.go:57) and both packages here build into ONE test binary. No
// t.Skip anywhere: internal/tools/rlsgate/rlsgate.go fails CI on any test-level skip, and
// nothing in this file needs a database, a network or a clock.
package submission

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/migrations"
)

// ---------------------------------------------------------------------------------------
// Shared helpers.
//
// The Canonical corpus itself is NOT rebuilt here: mock_wire_test.go's mwCorpus() and its
// mwFullCanonical/mwStrPtr build funcs are in this same package and are reused directly. Each
// is a BUILD FUNC, so every test below still gets a fresh, independent Canonical.
// ---------------------------------------------------------------------------------------

// msSeedMigration is the exact filename of the v1 rule-set seed. Read through migrations.FS --
// the house way this package already reads migration text (exchange_bridge_test.go:310,320,
// exchange_bridge_edge_test.go:66) -- rather than through filepath.Glob or a ../../ path
// ([tests-read-migrations-through-the-embed-fs]). The exact filename makes a migration RENAME
// a loud test failure rather than a silent glob miss.
const msSeedMigration = "20260711121327_seed_mbs_v1.sql"

func msSeedMigrationText(t *testing.T) string {
	t.Helper()
	b, err := migrations.FS.ReadFile(msSeedMigration)
	if err != nil {
		t.Fatalf("read %s from migrations.FS: %v (was the migration renamed? the exact filename is "+
			"deliberate -- update it here rather than reaching for a glob)", msSeedMigration, err)
	}
	return string(b)
}

// msBuyerTINPattern extracts the SHIPPED buyer-tin-format pattern from the seed migration text.
// It is never retyped here: a retyped regex would compare this file to a copy of itself and
// could never detect drift in the rule that actually gates submission.
func msBuyerTINPattern(t *testing.T) *regexp.Regexp {
	t.Helper()
	sql := msSeedMigrationText(t)

	var row string
	for _, line := range strings.Split(sql, "\n") {
		if strings.Contains(line, "'buyer-tin-format'") {
			row = line
			break
		}
	}
	if row == "" {
		t.Fatalf("no 'buyer-tin-format' row in %s -- the shipped rule that forces the reserved "+
			"block's shape is gone; the reserved block must be re-derived, not this test relaxed",
			msSeedMigration)
	}

	const open = `"pattern":"`
	i := strings.Index(row, open)
	if i < 0 {
		t.Fatalf("the 'buyer-tin-format' row in %s carries no %s params key:\n%s", msSeedMigration, open, row)
	}
	rest := row[i+len(open):]
	j := strings.Index(rest, `"}`)
	if j < 0 {
		t.Fatalf("the 'buyer-tin-format' pattern in %s is unterminated:\n%s", msSeedMigration, row)
	}
	return regexp.MustCompile(rest[:j])
}

// msRuleKeyRow matches one row of the seed migration's rules VALUES block, capturing its key.
// The rule_set_versions INSERT above it opens `VALUES (1, true, ...)`, which this cannot match.
var msRuleKeyRow = regexp.MustCompile(`(?m)^\s*\('([a-z0-9-]+)',`)

func msShippedRuleKeys(t *testing.T) []string {
	t.Helper()
	matches := msRuleKeyRow.FindAllStringSubmatch(msSeedMigrationText(t), -1)
	if len(matches) == 0 {
		t.Fatalf("extracted zero rule keys from %s -- the comparison below would pass vacuously",
			msSeedMigration)
	}
	keys := make([]string, 0, len(matches))
	for _, m := range matches {
		keys = append(keys, m[1])
	}
	return keys
}

// msLuhnCheckDigit is transcribed CHARACTER-FOR-CHARACTER from tools/fixturegen/gen.go:169-182,
// which is the SOURCE OF TRUTH for this property: genBuyerTIN (gen.go:151-164) is the only TIN
// generator in the repo, it builds an 11-digit random payload, appends this check digit as the
// twelfth digit, and renders digits[:8] + "-" + digits[8:]. tools/fixturegen is `package main`
// and cannot be imported, so a transcription is the only option.
//
// THE TRAP this transcription exists to avoid: the doubling is keyed on position from the right
// of the 11-digit PAYLOAD (even posFromRight, i.e. even indices i counted from the left of an
// odd-length slice), which coincides with textbook Luhn only once the check digit is appended.
// An author who writes "double every second digit from the right of the 12-digit string" gets a
// DIFFERENT answer and would assert a guarantee that does not hold.
//
// The positive control in TestMockReservedTINs_AreLuhnInvalid pins this: the payload
// [9,9,9,9,9,9,9,9,0,0,0] must yield 8 (every 9 contributes 9 whether doubled or not, since
// 18-9 = 9; sum = 72; (10 - 72%10) % 10 = 8), which makes 99999999-0008 the UNIQUE Luhn-VALID
// member of the 99999999-000X range. Without that control this whole spec would still "pass" if
// the transcription were wrong in a way that made every TIN look invalid.
func msLuhnCheckDigit(payload []int) int {
	sum := 0
	for i, d := range payload {
		posFromRight := len(payload) - 1 - i
		if posFromRight%2 == 0 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return (10 - sum%10) % 10
}

// msTINDigits splits an NNNNNNNN-NNNN TIN into its 12 digits. It reports false for anything
// that is not exactly that shape, so a caller can tell "not a TIN" apart from "a TIN that
// fails Luhn".
func msTINDigits(tin string) ([]int, bool) {
	stripped := strings.ReplaceAll(tin, "-", "")
	if len(stripped) != 12 {
		return nil, false
	}
	digits := make([]int, 12)
	for i, r := range stripped {
		if r < '0' || r > '9' {
			return nil, false
		}
		digits[i] = int(r - '0')
	}
	return digits, true
}

// msLuhnValid reports whether tin's twelfth digit is the check digit gen.go would have computed
// over its first eleven -- i.e. whether tools/fixturegen could ever have minted it.
func msLuhnValid(t *testing.T, tin string) bool {
	t.Helper()
	digits, ok := msTINDigits(tin)
	if !ok {
		t.Fatalf("msLuhnValid(%q): not an NNNNNNNN-NNNN TIN, so the Luhn question is meaningless", tin)
	}
	return msLuhnCheckDigit(digits[:11]) == digits[11]
}

// msRequireAllocations guards every spec that iterates mockAllocations. An `all()` over an
// EMPTY table passes vacuously, which is exactly the shape mockAllocations has in the stub --
// so without this guard several of the specs below would go green before a line of behaviour
// existed.
func msRequireAllocations(t *testing.T) {
	t.Helper()
	const want = 7
	if len(mockAllocations) != want {
		t.Fatalf("mockAllocations has %d entries, want %d (99999999-0001 .. -0007) -- every "+
			"per-allocation assertion below would otherwise pass vacuously", len(mockAllocations), want)
	}
}

// msIdentifiers builds the wire for c, parses it back, and synthesizes the identifier triple --
// the exact path Submit takes, since Submit is handed the Wire and never the Canonical.
func msIdentifiers(t *testing.T, c Canonical) (Wire, mockIdentifiers) {
	t.Helper()
	w, err := mockWireFrom(c)
	if err != nil {
		t.Fatalf("mockWireFrom returned an unexpected error: %v", err)
	}
	env, err := parseMockEnvelope(w)
	if err != nil {
		t.Fatalf("parseMockEnvelope rejected bytes this package just produced: %v", err)
	}
	return w, mockIdentifiersFor(w, env)
}

// msRequireNonBlank is the positive control every determinism assertion needs: three empty
// strings compare equal to three other empty strings, so "identical wire yields identical
// values" is satisfied trivially by an implementation that synthesizes nothing at all.
func msRequireNonBlank(t *testing.T, what string, ids mockIdentifiers) {
	t.Helper()
	for _, f := range []struct{ name, value string }{
		{"IRN", ids.IRN},
		{"CSID", ids.CSID},
		{"QRPayload", ids.QRPayload},
	} {
		if strings.TrimSpace(f.value) == "" {
			t.Fatalf("%s: %s is blank (%q) -- every equality assertion in this spec would pass "+
				"vacuously against an implementation that synthesizes nothing", what, f.name, f.value)
		}
	}
}

func msRawURLOnly(t *testing.T, what, s string) {
	t.Helper()
	for _, bad := range []string{"+", "/", "="} {
		if strings.Contains(s, bad) {
			t.Errorf("%s = %q contains %q -- [base64-is-rawurl-everywhere]: both the CSID and the QR "+
				"payload use base64.RawURLEncoding, whose alphabet excludes + / and =; StdEncoding "+
				"appears nowhere in this repo and its output needs escaping the moment M5-09 puts it "+
				"in a URL", what, s, bad)
		}
	}
}

// ---------------------------------------------------------------------------------------
// AC-1: the allocation table. [non-reserved-defaults-to-accept]
// ---------------------------------------------------------------------------------------

// TestMockTriggerFor_AllocationTable drives one row per allocation, and separately pins the
// TABLE itself -- its length, its order and its contents. The two halves catch different bugs:
// mockTriggerFor could be a correct-looking switch statement that has drifted from the table
// docs/mock-app-adapter.md is generated against, or the table could be right while the lookup
// silently normalises or short-circuits.
func TestMockTriggerFor_AllocationTable(t *testing.T) {
	want := []mockAllocation{
		{TIN: mockTINAccept, Trigger: mockTriggerAccept},
		{TIN: mockTINReject, Trigger: mockTriggerReject},
		{TIN: mockTINPending, Trigger: mockTriggerPending},
		{TIN: mockTINUnavailable, Trigger: mockTriggerUnavailable},
		{TIN: mockTINSlow, Trigger: mockTriggerSlow},
		{TIN: mockTINTimeout, Trigger: mockTriggerTimeout},
		{TIN: mockTINConnection, Trigger: mockTriggerConnection},
	}

	t.Run("lookup", func(t *testing.T) {
		for _, tc := range want {
			t.Run(tc.TIN, func(t *testing.T) {
				if got := mockTriggerFor(tc.TIN); got != tc.Trigger {
					t.Errorf("mockTriggerFor(%q) = %q, want %q", tc.TIN, got, tc.Trigger)
				}
			})
		}
	})

	t.Run("table-matches-the-documented-order", func(t *testing.T) {
		msRequireAllocations(t)
		for i, w := range want {
			if got := mockAllocations[i]; got != w {
				t.Errorf("mockAllocations[%d] = %+v, want %+v -- declaration order is the order "+
					"docs/mock-app-adapter.md's table is asserted against", i, got, w)
			}
		}
	})

	t.Run("no-duplicate-tin-or-trigger", func(t *testing.T) {
		msRequireAllocations(t)
		seenTIN := map[string]bool{}
		seenTrigger := map[mockTrigger]bool{}
		for _, a := range mockAllocations {
			if seenTIN[a.TIN] {
				t.Errorf("mockAllocations allocates %q twice -- a linear scan would silently honour "+
					"only the first", a.TIN)
			}
			if seenTrigger[a.Trigger] {
				t.Errorf("trigger %q is allocated to more than one TIN -- each of the seven scripted "+
					"outcomes has exactly one entry point", a.Trigger)
			}
			seenTIN[a.TIN] = true
			seenTrigger[a.Trigger] = true
		}
	})
}

// TestMockTriggerFor_DefaultsToAccept covers the whole negative space: nothing outside the seven
// allocations may divert. [non-reserved-defaults-to-accept] requires this to be an EXPLICIT
// return, not a free map-miss default -- a property a reader cannot see, but one this spec pins
// behaviourally either way.
func TestMockTriggerFor_DefaultsToAccept(t *testing.T) {
	cases := []struct{ name, tin string }{
		{"empty-string", ""},
		{"foreign-tin-shape", "BUY-TIN-1"},
		{"different-8-digit-prefix", "01234567-0001"},
		{"reserved-but-never-allocatable", "99999999-0008"},
		{"reserved-but-unallocated-literal", "99999999-0009"},
		{"reserved-but-unallocated-high", "99999999-4242"},
		{"reserved-boundary-zero", "99999999-0000"},
		{"reserved-boundary-top", "99999999-9999"},
		{"reserved-prefix-alone", mockReservedPrefix},
		{"suffix-only", "-0002"},
		{"trigger-digits-without-the-reserved-prefix", "12345678-0002"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mockTriggerFor(tc.tin); got != mockTriggerAccept {
				t.Errorf("mockTriggerFor(%q) = %q, want %q -- only the seven allocated TINs divert",
					tc.tin, got, mockTriggerAccept)
			}
		})
	}
}

// TestMockTriggerFor_IsExactMatchNotNormalised pins the DELIBERATE opposite ruling from
// registry.go's IsProduction. IsProduction normalises (trim + lowercase) because it guards a
// fail-CLOSED boot gate, where accepting more spellings is safer. Here normalising would WIDEN
// the set of inputs that activate a scripted outcome inside a running submission pipeline,
// which is the wrong direction: a stray trailing newline in a CSV import must NOT arm the
// reject trigger.
func TestMockTriggerFor_IsExactMatchNotNormalised(t *testing.T) {
	for _, tin := range []string{
		" " + mockTINReject,
		mockTINReject + " ",
		mockTINReject + "\n",
		"\t" + mockTINReject,
		mockTINReject + "\u00a0", // a NON-BREAKING space: strings.TrimSpace leaves it in place
		mockTINReject + ".",
		"9999999-0002",   // one digit short in the prefix
		"999999999-0002", // one digit long
	} {
		t.Run(fmt.Sprintf("%q", tin), func(t *testing.T) {
			if got := mockTriggerFor(tin); got != mockTriggerAccept {
				t.Errorf("mockTriggerFor(%q) = %q, want %q -- the match is EXACT and deliberately "+
					"un-normalised; anything else widens the set of inputs that arm a scripted outcome",
					tin, got, mockTriggerAccept)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------
// AC-2: the reserved block's two structural invariants. [reserved-is-luhn-invalid]
// ---------------------------------------------------------------------------------------

// TestMockReservedTINs_MatchTheShippedFormatRule is the "can it even reach submission?" half.
// The pattern is READ OUT of the live seed migration, never retyped: buyer-tin-format is an
// ERROR-severity rule, so a trigger TIN that failed it would be rejected during validation and
// the mock would never see it. That constraint is what forces the 8-digit 99999999- prefix.
func TestMockReservedTINs_MatchTheShippedFormatRule(t *testing.T) {
	re := msBuyerTINPattern(t)

	// Control: the pattern really is the discriminating one, not something that matches
	// everything. Without this, an extraction bug that yielded `.*` would make the rest pass.
	if re.MatchString("BUY-TIN-1") {
		t.Fatalf("the pattern extracted from %s (%s) matches a non-TIN string -- extraction is "+
			"broken and every assertion below would pass for the wrong reason", msSeedMigration, re)
	}

	msRequireAllocations(t)
	for _, a := range mockAllocations {
		t.Run(a.TIN, func(t *testing.T) {
			if !re.MatchString(a.TIN) {
				t.Errorf("allocated trigger %q does not match the shipped buyer-tin-format pattern %s "+
					"-- validation would reject it before submission ever ran, making the trigger "+
					"unreachable", a.TIN, re)
			}
			if !strings.HasPrefix(a.TIN, mockReservedPrefix) {
				t.Errorf("allocated trigger %q is outside the reserved block %q", a.TIN, mockReservedPrefix)
			}
		})
	}

	for _, tin := range mockNeverAllocate {
		t.Run("never-allocate/"+tin, func(t *testing.T) {
			if !re.MatchString(tin) {
				t.Errorf("never-allocate value %q does not match %s -- the never-allocate list must "+
					"describe values that are genuinely inside the reserved block", tin, re)
			}
		})
	}
}

// TestMockReservedTINs_AreLuhnInvalid is the "can fixturegen ever collide with it?" half, and it
// is the spec most likely to assert a FALSE guarantee if the Luhn transcription drifts. Hence
// the positive control, which runs FIRST and is honestly not red-first: it exercises
// msLuhnCheckDigit (this file's transcription of tools/fixturegen/gen.go:169-182), not any
// production code, and is green from the moment it is written. Its job is to make the negative
// assertions below MEAN something -- without it, a transcription that returned the wrong digit
// for everything would make every TIN "Luhn-invalid" and this spec would pass while guaranteeing
// nothing at all.
func TestMockReservedTINs_AreLuhnInvalid(t *testing.T) {
	t.Run("positive-control/transcribed-luhn-agrees-with-fixturegen", func(t *testing.T) {
		payload := []int{9, 9, 9, 9, 9, 9, 9, 9, 0, 0, 0}
		const want = 8
		if got := msLuhnCheckDigit(payload); got != want {
			t.Fatalf("msLuhnCheckDigit(%v) = %d, want %d.\n\nThis is the transcription check, not a "+
				"product bug: every 9 contributes 9 whether doubled or not (18-9 = 9), so the sum is "+
				"72 and (10 - 72%%10) %% 10 = 8. Getting anything else means this file's copy of "+
				"tools/fixturegen/gen.go:169-182 is wrong -- most likely by doubling from the right of "+
				"the 12-digit STRING instead of the 11-digit PAYLOAD. Fix the transcription; do NOT "+
				"adjust the expected value.", payload, got, want)
		}

		if !msLuhnValid(t, "99999999-0008") {
			t.Fatalf("99999999-0008 must be Luhn-VALID -- it is the unique valid member of the " +
				"99999999-000X range, which is precisely why it can never be allocated a trigger")
		}
		for _, tin := range []string{
			"99999999-0000", "99999999-0001", "99999999-0002", "99999999-0003",
			"99999999-0004", "99999999-0005", "99999999-0006", "99999999-0007",
			"99999999-0009",
		} {
			if msLuhnValid(t, tin) {
				t.Errorf("%q is Luhn-VALID, but 99999999-0008 is supposed to be the only valid member "+
					"of 99999999-000X -- the transcription or the arithmetic is wrong", tin)
			}
		}
	})

	t.Run("every-allocated-trigger-is-luhn-invalid", func(t *testing.T) {
		msRequireAllocations(t)
		for _, a := range mockAllocations {
			t.Run(a.TIN, func(t *testing.T) {
				if msLuhnValid(t, a.TIN) {
					t.Errorf("allocated trigger %q is Luhn-VALID, so tools/fixturegen's genBuyerTIN "+
						"could mint it -- M5-13's dataset would then arm a scripted outcome by "+
						"accident. [reserved-is-luhn-invalid] requires every allocation to be "+
						"mechanically unmintable", a.TIN)
				}
			})
		}
	})
}

// TestMockReservedTINs_NeverAllocateIsNotAllocated pins the two permanent exclusions by name,
// because each is excluded for a DIFFERENT reason and neither is derivable from the other:
// -0008 is Luhn-valid (fixturegen can mint it), -0009 is already a live literal at
// internal/invoice/payload_fingerprint_test.go:68.
func TestMockReservedTINs_NeverAllocateIsNotAllocated(t *testing.T) {
	want := []string{"99999999-0008", "99999999-0009"}
	if len(mockNeverAllocate) != len(want) {
		t.Fatalf("mockNeverAllocate = %v, want %v", mockNeverAllocate, want)
	}
	for i, w := range want {
		if mockNeverAllocate[i] != w {
			t.Errorf("mockNeverAllocate[%d] = %q, want %q", i, mockNeverAllocate[i], w)
		}
	}

	for _, tin := range mockNeverAllocate {
		t.Run(tin, func(t *testing.T) {
			for _, a := range mockAllocations {
				if a.TIN == tin {
					t.Errorf("%q is in mockNeverAllocate but is allocated trigger %q", tin, a.Trigger)
				}
			}
			// The behavioural half: an un-allocatable value must actually behave as accept.
			// The loop above passes vacuously while mockAllocations is empty; this does not.
			if got := mockTriggerFor(tin); got != mockTriggerAccept {
				t.Errorf("mockTriggerFor(%q) = %q, want %q -- a never-allocate value must take the "+
					"ordinary accept path", tin, got, mockTriggerAccept)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------
// AC-3: determinism, and the identity-vs-content split.
// [irn-is-identity-keyed-not-content-keyed]
// ---------------------------------------------------------------------------------------

// TestMockIdentifiers_AreDeterministic has FOUR parts, and part (c) is the corrected form of the
// story's original AC. The original read "a one-byte change changes all three", which the IRN
// STRUCTURALLY cannot satisfy: [irn-shape] is docRef(env.ID) + "-" + mockServiceID + "-" +
// datePart(env.IssueDate), reading exactly two envelope fields, so a changed amount leaves it
// identical. The shape is right -- the real FIRS MBS IRN is invoice-number + service-id +
// issue-date and carries no content digest -- so the AC was wrong, not the design. Asserting the
// original form would fail against a CORRECT implementation and push an author toward putting a
// digest in the IRN, destroying exactly the credibility [irn-shape] exists to buy.
func TestMockIdentifiers_AreDeterministic(t *testing.T) {
	t.Run("a/same-wire-twice-and-from-a-fresh-parse", func(t *testing.T) {
		c := mwFullCanonical()
		w, err := mockWireFrom(c)
		if err != nil {
			t.Fatalf("mockWireFrom returned an unexpected error: %v", err)
		}
		env1, err := parseMockEnvelope(w)
		if err != nil {
			t.Fatalf("parseMockEnvelope: %v", err)
		}

		first := mockIdentifiersFor(w, env1)
		msRequireNonBlank(t, "first synthesis", first)

		second := mockIdentifiersFor(w, env1)
		if second != first {
			t.Errorf("two calls on the same (wire, env) disagreed\nfirst:  %+v\nsecond: %+v", first, second)
		}

		env2, err := parseMockEnvelope(w) // a FRESH parse of the same bytes
		if err != nil {
			t.Fatalf("parseMockEnvelope (second): %v", err)
		}
		third := mockIdentifiersFor(w, env2)
		if third != first {
			t.Errorf("a freshly parsed envelope over the SAME bytes produced different identifiers -- "+
				"the synthesis must depend on the bytes and the parsed fields, not on the envelope "+
				"value's identity\nfirst: %+v\nthird: %+v", first, third)
		}
	})

	t.Run("b/one-hundred-iterations-agree", func(t *testing.T) {
		// 100 iterations kill a time.Now-, math/rand- or counter-based implementation: any of the
		// three would drift within the loop, and none is visible in a single-call assertion.
		_, want := msIdentifiers(t, mwFullCanonical())
		msRequireNonBlank(t, "iteration 0", want)

		for i := 1; i < 100; i++ {
			_, got := msIdentifiers(t, mwFullCanonical()) // fresh Canonical each time -- L03
			if got != want {
				t.Fatalf("iteration %d disagreed with iteration 0 -- the synthesis carries a clock, a "+
					"rand or a counter\nwant: %+v\ngot:  %+v", i, want, got)
			}
		}
	})

	t.Run("c/an-amount-change-moves-csid-and-qr-but-not-the-irn", func(t *testing.T) {
		base := mwFullCanonical()
		_, before := msIdentifiers(t, base)
		msRequireNonBlank(t, "before the amount edit", before)

		edited := mwFullCanonical()
		if edited.Subtotal == nil || *edited.Subtotal != "1000.00" {
			t.Fatalf("corpus precondition broken: the full canonical's Subtotal must be \"1000.00\", got %v",
				edited.Subtotal)
		}
		edited.Subtotal = mwStrPtr("1000.01") // a ONE-BYTE change, deep in the money block
		_, after := msIdentifiers(t, edited)

		if after.CSID == before.CSID {
			t.Errorf("CSID is unchanged after a one-byte edit to Subtotal (%q) -- the CSID is a "+
				"function of the WHOLE wire", before.CSID)
		}
		if after.QRPayload == before.QRPayload {
			t.Errorf("QRPayload is unchanged after a one-byte edit to Subtotal -- it carries the CSID " +
				"and the payable amount, so it is a function of the whole wire too")
		}
		if after.IRN != before.IRN {
			t.Errorf("IRN changed after an AMOUNT edit: %q -> %q.\n\n"+
				"[irn-is-identity-keyed-not-content-keyed]: the IRN reads exactly env.ID and "+
				"env.IssueDate, so it is deliberately STABLE across changes to amounts, lines and "+
				"parties. Do NOT 'fix' this by adding a wire digest to the IRN -- the real FIRS MBS "+
				"IRN carries none, and the resemblance is the whole point of the shape.",
				before.IRN, after.IRN)
		}
	})

	t.Run("d/an-identity-change-moves-the-irn", func(t *testing.T) {
		base := mwFullCanonical()
		_, before := msIdentifiers(t, base)
		msRequireNonBlank(t, "before the identity edit", before)

		numberChanged := mwFullCanonical()
		numberChanged.InvoiceNumber = "INV-FULL-0002"
		_, afterNumber := msIdentifiers(t, numberChanged)
		if afterNumber.IRN == before.IRN {
			t.Errorf("IRN %q is unchanged after the invoice NUMBER changed -- the IRN is keyed on "+
				"document identity and the number is half of it", before.IRN)
		}

		dateChanged := mwFullCanonical()
		newDate := time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC)
		dateChanged.IssueDate = &newDate
		_, afterDate := msIdentifiers(t, dateChanged)
		if afterDate.IRN == before.IRN {
			t.Errorf("IRN %q is unchanged after the ISSUE DATE changed -- the date is the other half "+
				"of document identity", before.IRN)
		}
	})
}

// TestMockIdentifiers_CSIDAndQRUseRawURLBase64 pins the ENCODING and the QR's decoded shape
// without re-implementing the synthesis: asserting `CSID == base64(sha256(w))` in the test would
// just compare the implementation to a copy of itself. Instead it asserts the observable
// properties -- 43 unpadded characters that decode to exactly a SHA-256 digest, and a QR payload
// that decodes to the five documented keys with values traceable to the envelope.
//
// [base64-is-rawurl-everywhere] is a deliberate deviation from the story text, which pinned
// StdEncoding for the QR while pinning RawURL for the CSID in the same breath. This spec is
// where that deviation is nailed down.
func TestMockIdentifiers_CSIDAndQRUseRawURLBase64(t *testing.T) {
	c := mwFullCanonical()
	w, ids := msIdentifiers(t, c)
	msRequireNonBlank(t, "full canonical", ids)

	t.Run("csid-is-an-unpadded-base64url-sha256", func(t *testing.T) {
		msRawURLOnly(t, "CSID", ids.CSID)

		wantLen := base64.RawURLEncoding.EncodedLen(sha256.Size) // 43
		if len(ids.CSID) != wantLen {
			t.Errorf("len(CSID) = %d (%q), want %d -- an unpadded base64url SHA-256 digest",
				len(ids.CSID), ids.CSID, wantLen)
		}
		raw, err := base64.RawURLEncoding.DecodeString(ids.CSID)
		if err != nil {
			t.Fatalf("CSID %q does not decode under base64.RawURLEncoding: %v", ids.CSID, err)
		}
		if len(raw) != sha256.Size {
			t.Errorf("CSID decodes to %d bytes, want %d (a SHA-256 digest)", len(raw), sha256.Size)
		}
		if len(w) == 0 {
			t.Fatalf("the wire is empty, so the digest below would be of nothing")
		}
	})

	t.Run("qr-decodes-to-the-five-documented-keys", func(t *testing.T) {
		msRawURLOnly(t, "QRPayload", ids.QRPayload)

		raw, err := base64.RawURLEncoding.DecodeString(ids.QRPayload)
		if err != nil {
			t.Fatalf("QRPayload %q does not decode under base64.RawURLEncoding: %v", ids.QRPayload, err)
		}
		var qr mockQR
		if err := json.Unmarshal(raw, &qr); err != nil {
			t.Fatalf("the decoded QR payload is not the documented JSON object: %v\npayload: %s", err, raw)
		}

		for _, f := range []struct{ name, got, want string }{
			{"irn", qr.IRN, ids.IRN},
			{"csid", qr.CSID, ids.CSID},
			{"tin", qr.TIN, "SUP-TIN-1"}, // the SUPPLIER TIN -- the party the authority clears
			{"amt", qr.Amt, "1075.00"},   // LegalMonetaryTotal.PayableAmount.Value, verbatim
			{"cur", qr.Cur, "NGN"},
		} {
			if f.got != f.want {
				t.Errorf("QR %s = %q, want %q", f.name, f.got, f.want)
			}
		}
	})

	t.Run("qr-carries-the-supplier-tin-not-the-buyer-tin", func(t *testing.T) {
		// The negative half. Both parties carry a TIN in the full corpus case, so a spec that only
		// asserted "tin is non-empty" would pass against an implementation reading the wrong party.
		env, err := parseMockEnvelope(w)
		if err != nil {
			t.Fatalf("parseMockEnvelope: %v", err)
		}
		if got := mockSupplierTIN(env); got != "SUP-TIN-1" {
			t.Errorf("mockSupplierTIN(env) = %q, want %q -- the mirror of mockBuyerTIN, reading the "+
				"SUPPLIER block", got, "SUP-TIN-1")
		}
		if got := mockSupplierTIN(mockEnvelope{}); got != "" {
			t.Errorf("mockSupplierTIN(mockEnvelope{}) = %q, want \"\" -- it must be total and nil-safe", got)
		}

		raw, err := base64.RawURLEncoding.DecodeString(ids.QRPayload)
		if err != nil {
			t.Fatalf("QRPayload does not decode: %v", err)
		}
		if strings.Contains(string(raw), "BUY-TIN-1") {
			t.Errorf("the QR payload carries the BUYER TIN -- it must carry the supplier's\npayload: %s", raw)
		}
	})
}

// ---------------------------------------------------------------------------------------
// AC-4: the IRN is non-blank everywhere, and has the documented shape.
// ---------------------------------------------------------------------------------------

// TestMockIRN_NonBlankForEveryCorpusShape drives every shape mock_wire_test.go's corpus knows,
// including the ZERO Canonical (no invoice number, no issue date at all) and the multi-byte one
// whose invoice number sanitises down to almost nothing. L07 requires Accepted.IRN non-blank,
// and M5-03-03 returns this value straight into an Accepted -- so a blank here is a contract
// violation, not a cosmetic one.
func TestMockIRN_NonBlankForEveryCorpusShape(t *testing.T) {
	cases := mwCorpus()
	cases = append(cases,
		mwCase{name: "invoice-number-only", build: func() Canonical {
			return Canonical{InvoiceNumber: "INV-ONLY-0001"}
		}},
		mwCase{name: "punctuation-only-invoice-number", build: func() Canonical {
			return Canonical{InvoiceNumber: "!!!"}
		}},
		mwCase{name: "whitespace-only-invoice-number", build: func() Canonical {
			return Canonical{InvoiceNumber: "   "}
		}},
	)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ids := msIdentifiers(t, tc.build())
			if strings.TrimSpace(ids.IRN) == "" {
				t.Errorf("IRN is blank (%q) for the %q shape -- L07 requires a non-blank "+
					"Accepted.IRN and M5-03-03 hands this value straight through", ids.IRN, tc.name)
			}
		})
	}
}

// TestMockIRN_ShapeIsDocRefServiceIDDate pins the composed shape end to end, against the SYMBOLS
// rather than retyped literals, on the two poles: a fully populated document and the zero
// Canonical, whose IRN must degrade to the digest fallback rather than to "" or "--".
func TestMockIRN_ShapeIsDocRefServiceIDDate(t *testing.T) {
	t.Run("populated-document", func(t *testing.T) {
		_, ids := msIdentifiers(t, mwFullCanonical()) // ID "INV-FULL-0001", IssueDate 2026-01-15
		want := "INV-FULL-0001-" + mockServiceID + "-20260115"
		if ids.IRN != want {
			t.Errorf("IRN = %q, want %q", ids.IRN, want)
		}
	})

	t.Run("zero-canonical-degrades-to-the-digest-fallback", func(t *testing.T) {
		_, ids := msIdentifiers(t, Canonical{})
		want := regexp.MustCompile(fmt.Sprintf(`^%s[0-9A-F]{%d}-%s-%s$`,
			mockDocRefFallbackPrefix, mockDocRefFallbackHexLen, mockServiceID, mockIRNNoDate))
		if !want.MatchString(ids.IRN) {
			t.Errorf("IRN = %q, want it to match %s -- a document with no invoice number and no "+
				"issue date must still mint a well-formed, non-blank IRN, and the hex must be "+
				"UPPERCASE or the IRN mixes cases", ids.IRN, want)
		}
	})

	t.Run("the-irn-alphabet-is-uppercase-ascii", func(t *testing.T) {
		alphabet := regexp.MustCompile(`^[A-Z0-9-]+$`)
		for _, tc := range mwCorpus() {
			t.Run(tc.name, func(t *testing.T) {
				_, ids := msIdentifiers(t, tc.build())
				if !alphabet.MatchString(ids.IRN) {
					t.Errorf("IRN = %q contains characters outside [A-Z0-9-] -- the docRef is upper-cased "+
						"and stripped before it is truncated, so no lowercase letter and no multi-byte "+
						"rune can survive into it", ids.IRN)
				}
			})
		}
	})
}

// TestMockIRN_DocRefUppercasesBeforeStrippingAndTruncates pins the ORDER of the three docRef
// steps, which is load-bearing and invisible in the composed IRN for most inputs. Stripping
// before upper-casing deletes every lowercase letter, turning "inv-001" into "-001"; truncating
// before sanitising can cut a multi-byte rune in half.
func TestMockIRN_DocRefUppercasesBeforeStrippingAndTruncates(t *testing.T) {
	digest := sha256.Sum256([]byte("mock-script-docref-fixture"))
	fallback := regexp.MustCompile(fmt.Sprintf(`^%s[0-9A-F]{%d}$`,
		mockDocRefFallbackPrefix, mockDocRefFallbackHexLen))

	t.Run("exact", func(t *testing.T) {
		for _, tc := range []struct{ name, id, want string }{
			{"already-canonical", "INV-0001", "INV-0001"},
			{"lowercase-is-upper-cased-not-deleted", "inv-001", "INV-001"},
			{"mixed-case", "Inv-Full-0001", "INV-FULL-0001"},
			{"punctuation-and-spaces-are-stripped", "inv/001 #2", "INV0012"},
			{"underscores-are-stripped", "INV_0001", "INV0001"},
			{"hyphens-only-does-not-trigger-the-fallback", "---", "---"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := mockDocRef(tc.id, digest); got != tc.want {
					t.Errorf("mockDocRef(%q) = %q, want %q", tc.id, got, tc.want)
				}
			})
		}
	})

	t.Run("falls-back-only-when-sanitising-leaves-nothing", func(t *testing.T) {
		for _, id := range []string{"", "   ", "!!!", "你好世界", "Ελληνικά"} {
			t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
				got := mockDocRef(id, digest)
				if !fallback.MatchString(got) {
					t.Errorf("mockDocRef(%q) = %q, want it to match %s -- nothing survives sanitisation, "+
						"so the digest fallback must fire and must be UPPERCASE hex", id, got, fallback)
				}
			})
		}
	})

	t.Run("truncates-to-the-documented-length", func(t *testing.T) {
		long := strings.Repeat("AB-9", 20) // 80 chars, all already in the alphabet
		got := mockDocRef(long, digest)
		if len(got) != mockDocRefMaxLen {
			t.Errorf("mockDocRef(<80 chars>) = %q (len %d), want len %d", got, len(got), mockDocRefMaxLen)
		}
		if want := long[:mockDocRefMaxLen]; got != want {
			t.Errorf("mockDocRef truncated to %q, want the FIRST %d sanitised characters (%q)",
				got, mockDocRefMaxLen, want)
		}
	})

	t.Run("sanitises-before-truncating-so-no-rune-is-split", func(t *testing.T) {
		// The multi-byte characters sit inside the first 24 BYTES but outside the first 24
		// surviving characters. Truncating first would cut one of them in half and leave invalid
		// UTF-8 in a value bound for invoices.irn.
		id := "你好世界-" + strings.Repeat("Z", 40)
		got := mockDocRef(id, digest)
		if len(got) != mockDocRefMaxLen {
			t.Errorf("mockDocRef = %q (len %d), want len %d", got, len(got), mockDocRefMaxLen)
		}
		if !regexp.MustCompile(`^[A-Z0-9-]+$`).MatchString(got) {
			t.Errorf("mockDocRef = %q -- sanitising must happen BEFORE truncating, so the result can "+
				"only ever contain [A-Z0-9-] and can never split a multi-byte rune", got)
		}
	})
}

// TestMockIRN_DatePartFallsBackToZeros pins that the date part is PARSED, never invented. Any
// error -- an empty string, a wrong layout, an out-of-range field -- degrades to mockIRNNoDate.
// time.Parse is a parser, not a clock: nothing here may reach for time.Now.
func TestMockIRN_DatePartFallsBackToZeros(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"canonical-date", "2026-01-15", "20260115"},
		{"leap-day", "2024-02-29", "20240229"},
		{"new-years-eve", "2025-12-31", "20251231"},
		{"empty", "", mockIRNNoDate},
		{"not-a-date", "not-a-date", mockIRNNoDate},
		{"month-out-of-range", "2026-13-45", mockIRNNoDate},
		{"non-leap-29-feb", "2025-02-29", mockIRNNoDate},
		{"rfc3339-is-the-wrong-layout", "2026-01-15T00:00:00Z", mockIRNNoDate},
		{"slashes", "15/01/2026", mockIRNNoDate},
		{"already-compact", "20260115", mockIRNNoDate},
		{"trailing-space", "2026-01-15 ", mockIRNNoDate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mockIRNDatePart(tc.in); got != tc.want {
				t.Errorf("mockIRNDatePart(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------
// AC-5: the rejection vocabulary.
// ---------------------------------------------------------------------------------------

// TestMockRejection_ReasonVocabulary pins the shape of what crosses the seam: exactly one
// Reason, a foreign-looking code, a non-empty message, and OUR dotted path -- the target the
// shipped buyer-tin-format rule resolves, so M5-09 can point the SPA at a field that genuinely
// exists and editing it makes the resubmission accept.
func TestMockRejection_ReasonVocabulary(t *testing.T) {
	reasons := mockRejectionReasons()
	if len(reasons) != 1 {
		t.Fatalf("mockRejectionReasons() returned %d reasons, want exactly 1 (L08 also requires "+
			"a non-empty set): %+v", len(reasons), reasons)
	}
	r := reasons[0]

	if r.Code == "" {
		t.Errorf("Reason.Code is empty")
	}
	if !strings.HasPrefix(r.Code, "NGE-") {
		t.Errorf("Reason.Code = %q, want the APP's own NGE- vocabulary -- a code that looked like "+
			"one of ours would make the SPA unable to tell an authority verdict from a local "+
			"validation failure", r.Code)
	}
	if r.Code != mockCodeRejected {
		t.Errorf("Reason.Code = %q, want %q -- the same code the 422 body carries", r.Code, mockCodeRejected)
	}
	if strings.TrimSpace(r.Message) == "" {
		t.Errorf("Reason.Message is blank -- it is what the SPA shows the operator")
	}
	if r.Path != mockRejectPath {
		t.Errorf("Reason.Path = %q, want %q -- OUR dotted path, the one MBSPayload emits and the "+
			"shipped buyer-tin-format rule resolves", r.Path, mockRejectPath)
	}
}

// TestMockRejection_CodeIsNotOneOfOurRuleKeys compares the reason code against every rule key in
// the LIVE seed migration text. The point of the mock is that an APP verdict is visibly foreign;
// a code that collided with one of our kebab-case rule keys would blur the line M5-09's UI has
// to draw.
func TestMockRejection_CodeIsNotOneOfOurRuleKeys(t *testing.T) {
	reasons := mockRejectionReasons()
	if len(reasons) == 0 {
		t.Fatalf("mockRejectionReasons() returned no reasons -- the comparison below would pass vacuously")
	}

	keys := msShippedRuleKeys(t)
	if len(keys) < 10 {
		t.Fatalf("extracted only %d rule keys from %s (%v) -- the v1 seed has 17; extraction is "+
			"broken and the comparison would be near-vacuous", len(keys), msSeedMigration, keys)
	}
	// Control: the extraction really did find the rule this whole reserved block exists because of.
	found := false
	for _, k := range keys {
		if k == "buyer-tin-format" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the extracted rule keys do not include buyer-tin-format: %v", keys)
	}

	kebab := regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)+$`)
	for _, r := range reasons {
		for _, k := range keys {
			if r.Code == k {
				t.Errorf("Reason.Code %q collides with the shipped rule key %q -- an APP verdict must "+
					"never be mistakable for one of our own validation failures", r.Code, k)
			}
		}
		if kebab.MatchString(r.Code) {
			t.Errorf("Reason.Code %q is shaped like one of our kebab-case rule keys; it must look "+
				"foreign", r.Code)
		}
	}
}

// TestMockRejection_ReasonsAreNotShared is L04 at this layer. Rejected.Reasons crosses the
// adapter seam and the caller may sort, truncate or annotate it; a package-level slice returned
// by reference would let one attempt's mutation reach every later one. contract_red_test.go:57-58
// documents exactly this failure mode from the other side -- an adapter mutating shared state
// through an aliased pointer.
func TestMockRejection_ReasonsAreNotShared(t *testing.T) {
	first := mockRejectionReasons()
	if len(first) == 0 {
		t.Fatalf("mockRejectionReasons() returned no reasons -- there is nothing to mutate and this " +
			"spec would pass vacuously")
	}
	original := first[0].Code

	first[0].Code = "MUTATED-BY-THE-CALLER"

	second := mockRejectionReasons()
	if len(second) != len(first) {
		t.Fatalf("mockRejectionReasons() returned %d reasons on the second call and %d on the first",
			len(second), len(first))
	}
	if second[0].Code != original {
		t.Errorf("mutating the FIRST result's Reason.Code changed the SECOND result: got %q, want %q "+
			"-- mockRejectionReasons must return a FRESH slice every call, never a package-level var "+
			"handed out by reference (L04)", second[0].Code, original)
	}
}

// ---------------------------------------------------------------------------------------
// AC-6: the synthesized response bodies. [their-field-our-path]
// ---------------------------------------------------------------------------------------

type msRejectedBody struct {
	Status  string `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Errors  []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Field   string `json:"field"`
	} `json:"errors"`
}

// TestMockBodies_RejectionUsesTheirFieldName is the asymmetry AC-6 exists for, asserted from
// both sides at once: the BODY the APP "returned" names the field in the APP's vocabulary and
// must contain no trace of ours, while the Reason we hand upward names it in ours. If the two
// ever converged, the mock would stop exercising the mapping M5-09 depends on.
func TestMockBodies_RejectionUsesTheirFieldName(t *testing.T) {
	// Constant-level sanity, honestly not red-first: it compares two symbols and is green from
	// the moment the constants exist. It exists so that a later "harmonisation" of the two
	// vocabularies fails loudly here rather than silently hollowing out this whole spec.
	if mockRejectField == mockRejectPath {
		t.Fatalf("mockRejectField and mockRejectPath are both %q -- the point of the mock is that the "+
			"APP's vocabulary and ours DIFFER", mockRejectField)
	}

	raw := mockRejectedBody()
	if strings.TrimSpace(raw) == "" {
		t.Fatalf("mockRejectedBody() returned an empty body -- every assertion below would be vacuous")
	}
	if strings.HasSuffix(raw, "\n") {
		t.Errorf("the body ends in a newline -- it is archived verbatim as app_exchange.response_body; " +
			"marshal it, never Encoder.Encode")
	}

	var body msRejectedBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("the 422 body is not parseable JSON: %v\nbody: %s", err, raw)
	}

	if body.Status != "REJECTED" {
		t.Errorf("body.status = %q, want %q", body.Status, "REJECTED")
	}
	if body.Code != mockCodeRejected {
		t.Errorf("body.code = %q, want %q", body.Code, mockCodeRejected)
	}
	if len(body.Errors) != 1 {
		t.Fatalf("body.errors has %d entries, want exactly 1: %+v", len(body.Errors), body.Errors)
	}
	if body.Errors[0].Field != mockRejectField {
		t.Errorf("body.errors[0].field = %q, want %q -- the APP names the field in ITS OWN vocabulary",
			body.Errors[0].Field, mockRejectField)
	}
	if body.Errors[0].Code != mockCodeRejected {
		t.Errorf("body.errors[0].code = %q, want %q", body.Errors[0].Code, mockCodeRejected)
	}
	if strings.TrimSpace(body.Errors[0].Message) == "" {
		t.Errorf("body.errors[0].message is blank")
	}

	if strings.Contains(raw, mockRejectPath) {
		t.Errorf("the 422 body contains OUR dotted path %q. The APP has never heard of it -- the "+
			"mapping from their field name to our path is the adapter's job, not the wire's.\nbody: %s",
			mockRejectPath, raw)
	}

	t.Run("the-reason-we-hand-up-uses-our-path-not-theirs", func(t *testing.T) {
		reasons := mockRejectionReasons()
		if len(reasons) == 0 {
			t.Fatalf("mockRejectionReasons() returned no reasons")
		}
		if reasons[0].Path != mockRejectPath {
			t.Errorf("Reason.Path = %q, want %q", reasons[0].Path, mockRejectPath)
		}
		if reasons[0].Path == mockRejectField {
			t.Errorf("Reason.Path is the APP's field name %q -- the adapter must translate it into "+
				"our dotted path, not pass it through", mockRejectField)
		}
	})
}

// TestMockBodies_AreWellFormedAndCarryTheirCodes covers all four builders. Every body is
// archived verbatim into app_exchange.response_body, so "well formed and compact" is a storage
// property as much as a cosmetic one.
func TestMockBodies_AreWellFormedAndCarryTheirCodes(t *testing.T) {
	ids := mockIdentifiers{IRN: "INV-0001-FBMOCK01-20260115", CSID: "csid-fixture", QRPayload: "qr-fixture"}
	ref := Ref(mockRefPrefix + "ref-fixture")

	for _, tc := range []struct {
		name       string
		body       string
		wantCode   string
		wantStatus string
		wantParts  []string
	}{
		{
			name: "200-accepted", body: mockAcceptedBody(ids),
			wantCode: mockCodeAccepted, wantStatus: "ACCEPTED",
			wantParts: []string{ids.IRN, ids.CSID, ids.QRPayload},
		},
		{
			name: "422-rejected", body: mockRejectedBody(),
			wantCode: mockCodeRejected, wantStatus: "REJECTED",
			wantParts: []string{mockRejectField},
		},
		{
			name: "202-pending", body: mockPendingBody(ref),
			wantCode: mockCodePending, wantStatus: "PENDING",
			wantParts: []string{string(ref), fmt.Sprintf("%d", mockPollAfterSeconds)},
		},
		{
			name: "503-unavailable", body: mockUnavailableBody(),
			wantCode: mockCodeUnavailable, wantStatus: "ERROR",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strings.TrimSpace(tc.body) == "" {
				t.Fatalf("the body is empty -- every assertion below would be vacuous")
			}
			if strings.HasSuffix(tc.body, "\n") {
				t.Errorf("the body ends in a newline; it is archived verbatim")
			}

			var decoded map[string]any
			if err := json.Unmarshal([]byte(tc.body), &decoded); err != nil {
				t.Fatalf("the body is not a parseable JSON object: %v\nbody: %s", err, tc.body)
			}
			if got, _ := decoded["code"].(string); got != tc.wantCode {
				t.Errorf("code = %q, want %q", got, tc.wantCode)
			}
			if got, _ := decoded["status"].(string); got != tc.wantStatus {
				t.Errorf("status = %q, want %q", got, tc.wantStatus)
			}
			if msg, _ := decoded["message"].(string); strings.TrimSpace(msg) == "" {
				t.Errorf("message is blank -- every synthesized body carries one")
			}
			for _, part := range tc.wantParts {
				if !strings.Contains(tc.body, part) {
					t.Errorf("the body does not carry %q\nbody: %s", part, tc.body)
				}
			}
		})
	}

	t.Run("503-carries-no-data-block", func(t *testing.T) {
		body := mockUnavailableBody()
		if strings.TrimSpace(body) == "" {
			t.Fatalf("mockUnavailableBody() returned an empty body")
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			t.Fatalf("the 503 body is not parseable JSON: %v\nbody: %s", err, body)
		}
		if _, ok := decoded["data"]; ok {
			t.Errorf("the 503 body carries a data block -- the authority decided nothing, so there "+
				"is nothing to carry\nbody: %s", body)
		}
		if _, ok := decoded["errors"]; ok {
			t.Errorf("the 503 body carries an errors block -- a 503 is a transport verdict, not a "+
				"validation one ([errors-never-verdicts])\nbody: %s", body)
		}
	})

	t.Run("202-pollAfterSeconds-tracks-the-constant", func(t *testing.T) {
		body := mockPendingBody(ref)
		if strings.TrimSpace(body) == "" {
			t.Fatalf("mockPendingBody() returned an empty body")
		}
		var decoded struct {
			Data struct {
				Reference        string `json:"reference"`
				PollAfterSeconds int    `json:"pollAfterSeconds"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			t.Fatalf("the 202 body is not parseable JSON: %v\nbody: %s", err, body)
		}
		if decoded.Data.Reference != string(ref) {
			t.Errorf("data.reference = %q, want %q -- the caller must persist exactly this handle",
				decoded.Data.Reference, string(ref))
		}
		if decoded.Data.PollAfterSeconds != mockPollAfterSeconds {
			t.Errorf("data.pollAfterSeconds = %d, want the constant mockPollAfterSeconds (%d) -- "+
				"M5-03-05's mockPollBackoff is derived from the same constant, and a second literal "+
				"here would let the two drift apart silently",
				decoded.Data.PollAfterSeconds, mockPollAfterSeconds)
		}
	})
}

// ---------------------------------------------------------------------------------------
// AC-7: the pending-handle codec. [ref-enforces-its-own-invariants]
// ---------------------------------------------------------------------------------------

// TestMockRef_RoundTrips pins the codec's happy path plus its determinism. The Ref is persisted
// to submission_jobs.poll_ref and handed back to Poll possibly days later on a different
// replica, so "encode twice, get the same bytes" is a durability property, not a nicety.
func TestMockRef_RoundTrips(t *testing.T) {
	ids := mockIdentifiers{
		IRN:       "INV-FULL-0001-" + mockServiceID + "-20260115",
		CSID:      "F5tPQ0lXQmxhaC1jc2lkLWZpeHR1cmUtdmFsdWU",
		QRPayload: "eyJpcm4iOiJJTlYtMDAwMSJ9",
	}

	for _, n := range []int{0, 1, 2, 7, 1000} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			ref := encodeMockRef(n, ids)
			if ref == "" {
				t.Fatalf("encodeMockRef(%d, %+v) returned an empty Ref -- L09 requires a non-empty "+
					"Pending.Ref", n, ids)
			}
			if !strings.HasPrefix(string(ref), mockRefPrefix) {
				t.Fatalf("Ref %q does not carry the %q prefix -- the prefix is what lets decodeMockRef "+
					"reject a foreign ref before attempting to decode it", ref, mockRefPrefix)
			}

			payload := strings.TrimPrefix(string(ref), mockRefPrefix)
			msRawURLOnly(t, "the encoded ref payload", payload)
			if _, err := base64.RawURLEncoding.DecodeString(payload); err != nil {
				t.Errorf("the ref payload %q does not decode under base64.RawURLEncoding: %v", payload, err)
			}

			gotN, gotIDs, err := decodeMockRef(ref)
			if err != nil {
				t.Fatalf("decodeMockRef(%q) returned an error for a ref it just minted: %v", ref, err)
			}
			if gotN != n {
				t.Errorf("decoded n = %d, want %d", gotN, n)
			}
			if gotIDs != ids {
				t.Errorf("decoded identifiers = %+v, want %+v", gotIDs, ids)
			}

			if again := encodeMockRef(n, ids); again != ref {
				t.Errorf("encoding the same (n, ids) twice produced different Refs:\n1: %q\n2: %q", ref, again)
			}
		})
	}

	t.Run("a-different-poll-count-yields-a-different-ref", func(t *testing.T) {
		if encodeMockRef(2, ids) == encodeMockRef(1, ids) {
			t.Errorf("encodeMockRef(2, ids) == encodeMockRef(1, ids) -- the countdown is carried IN " +
				"the ref, so the two must differ or Poll can never converge")
		}
	})
}

// TestMockRef_RejectsMalformed covers all FOUR rejection classes, and every case must wrap the
// sentinel so M5-03-05 can branch with errors.Is rather than on message text.
//
// "contract-suite-never-issued-ref" is the literal RunAdapterContract drives L14 with
// (contract_test.go:300) -- it is in this table verbatim so a codec change can never quietly
// break the contract suite.
//
// The last two cases are the INVARIANT class, and they are not cosmetic:
// mockapp-v1.<base64url of {"n":0}> is trivially constructible by hand, and M5-03-04's
// convergence branch returns Accepted{IRN: ids.IRN} straight out of the ref -- so a ref carrying
// a blank IRN would mint an Accepted that violates L07. Enforcing at the codec boundary once
// beats enforcing at every consumer ([ref-enforces-its-own-invariants]).
func TestMockRef_RejectsMalformed(t *testing.T) {
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

	valid := `{"n":1,"irn":"INV-0001-FBMOCK01-20260115","csid":"c","qr":"q"}`

	for _, tc := range []struct {
		name, class string
		ref         Ref
	}{
		{"empty", "prefix", Ref("")},
		{"contract-suite-never-issued-ref", "prefix", Ref("contract-suite-never-issued-ref")},
		{"wrong-prefix-entirely", "prefix", Ref("someotherapp-v1." + b64(valid))},
		{"prefix-without-the-dot", "prefix", Ref("mockapp-v1" + b64(valid))},
		{"next-version-prefix", "prefix", Ref("mockapp-v2." + b64(valid))},
		{"prefix-only-no-payload", "json", Ref(mockRefPrefix)},
		{"bad-base64", "base64", Ref(mockRefPrefix + "!!!")},
		{"std-base64-with-padding", "base64", Ref(mockRefPrefix + base64.StdEncoding.EncodeToString([]byte(valid)))},
		{"bad-json", "json", Ref(mockRefPrefix + b64("{"))},
		{"truncated-json", "json", Ref(mockRefPrefix + b64(valid[:len(valid)-10]))},
		{"json-array-not-object", "json", Ref(mockRefPrefix + b64(`[1,2,3]`))},
		{"negative-poll-count", "invariant", Ref(mockRefPrefix + b64(`{"n":-1,"irn":"INV-0001","csid":"c","qr":"q"}`))},
		{"blank-irn", "invariant", Ref(mockRefPrefix + b64(`{"n":0}`))},
		{"whitespace-only-irn", "invariant", Ref(mockRefPrefix + b64(`{"n":0,"irn":"   ","csid":"c","qr":"q"}`))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := decodeMockRef(tc.ref)
			if err == nil {
				t.Fatalf("decodeMockRef(%q) returned a nil error, want a non-nil one (%s class)",
					tc.ref, tc.class)
			}
			if !errors.Is(err, ErrMockUnknownRef) {
				t.Errorf("decodeMockRef(%q) error = %v, want it to wrap ErrMockUnknownRef so M5-03-05 "+
					"can branch with errors.Is rather than on message text", tc.ref, err)
			}
		})
	}

	t.Run("a-std-base64-payload-is-not-silently-accepted", func(t *testing.T) {
		// Guard for the case where the valid JSON happens to encode without padding under
		// StdEncoding: if the two encodings agree byte-for-byte the table row above proves
		// nothing, and the assertion needs to be known-vacuous rather than silently so.
		if base64.StdEncoding.EncodeToString([]byte(valid)) == base64.RawURLEncoding.EncodeToString([]byte(valid)) {
			t.Fatalf("the StdEncoding and RawURLEncoding forms of the fixture payload are identical, " +
				"so the std-base64-with-padding row above cannot discriminate; change the fixture")
		}
	})
}

// ---------------------------------------------------------------------------------------
// AC-8: the operator-facing doc.
// ---------------------------------------------------------------------------------------

// TestMockAdapterDoc_DocumentsEveryAllocation reads docs/mock-app-adapter.md from disk. `go test`
// sets the working directory to the package directory, so ../../docs resolves to the repo's docs
// tree from internal/submission.
//
// NOTE: reading a docs/ file from a Go test is a NEW pattern in this repo -- docs/ is not
// embedded and there is no precedent to copy ([tests-read-migrations-through-the-embed-fs]
// covers migrations only, and migrations.FS cannot reach outside migrations/). A relative read is
// the least-bad option here. repoRootForDepsTest (deps_test.go:46) is deliberately NOT used: it
// lives in package submission_test and is unreachable from this in-package file.
//
// Every assertion is against a SYMBOL, never a retyped literal: the doc's job is to describe the
// table this package actually ships, so a doc that drifted from the code would be the defect.
func TestMockAdapterDoc_DocumentsEveryAllocation(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "mock-app-adapter.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v -- AC-8 requires this doc; an operator who hits a scripted outcome in "+
			"a dev environment has nowhere else to look up what 99999999-0004 means", path, err)
	}
	doc := string(b)
	if strings.TrimSpace(doc) == "" {
		t.Fatalf("%s is empty", path)
	}

	msRequireAllocations(t)
	for _, a := range mockAllocations {
		if !strings.Contains(doc, a.TIN) {
			t.Errorf("%s does not mention the allocated TIN %s", path, a.TIN)
		}
		if !strings.Contains(doc, string(a.Trigger)) {
			t.Errorf("%s does not mention the trigger name %q (allocated to %s)", path, a.Trigger, a.TIN)
		}
	}

	if !strings.Contains(doc, mockReservedPrefix) {
		t.Errorf("%s does not state the reserved block prefix %q", path, mockReservedPrefix)
	}
	for _, tin := range mockNeverAllocate {
		if !strings.Contains(doc, tin) {
			t.Errorf("%s does not list the never-allocate value %s -- a future story that allocated "+
				"it would collide with fixturegen or with a live test literal", path, tin)
		}
	}
	if !strings.Contains(doc, mockLatencyEnv) {
		t.Errorf("%s does not document the env knob %s", path, mockLatencyEnv)
	}
	if !strings.Contains(doc, "APP_ADAPTER") {
		t.Errorf("%s does not mention APP_ADAPTER, the selector that turns the mock on at all", path)
	}
	if !strings.Contains(doc, "M5-13") {
		t.Errorf("%s does not warn M5-13 (and any anonymizer) never to mint a value inside the "+
			"reserved block -- that warning is the whole reason the block is reserved", path)
	}

	t.Run("names-which-trigger-is-the-permanently-failing-one", func(t *testing.T) {
		// The Core AC says "permanently-failing" but no trigger carries that name: the
		// unavailable allocation IS it (Retryable + 503 on every attempt, never converging).
		// Without this mapping written down, an operator hunts for a trigger that does not exist.
		if !strings.Contains(doc, "permanently") {
			t.Errorf("%s never uses the word \"permanently\" -- it must map the Core AC's "+
				"\"permanently-failing\" wording onto the %q trigger (%s), which never converges",
				path, mockTriggerUnavailable, mockTINUnavailable)
		}
	})
}
