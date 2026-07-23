// mock_script.go: M5-03-02 (task-225). The reserved buyer-TIN block and its scripted outcome
// table, the deterministic IRN/CSID/QR synthesis, the rejection vocabulary, the four
// synthesized APP response bodies, and the pending-handle (Ref) codec.
//
// The TYPE SET, the CONSTANTS and the FUNCTION SIGNATURES are the Stage-1 architecture output
// and are final; mock_script_test.go's specs assert against the SYMBOLS here rather than against
// retyped literals, so a constant may not be inlined at its use site.
//
// The four synthesized response bodies are built as map[string]any and rendered through
// mockJSONBody (the precedent is internal/invoice/payload.go:86 and platform/health.go:54).
// That is deliberately NOT the [wire-payload] "structs only" rule next door in mock_wire.go:
// that rule buys byte-determinism for the WIRE, whose field order must be fixed by declaration.
// A map buys the same determinism here (encoding/json sorts map keys) and lets the 503 body omit
// `data` and `errors` outright rather than carrying two omitempty pointers to say nothing.
//
// PURITY, enforced by review not by the compiler: this file must never contain an adapter
// type, a context.Context, time.Now, math/rand, an HTTP status constant, a response header,
// MockConfig, Evidence, or a package-level MUTABLE var. `time` is imported for time.Parse ONLY
// -- a parser, not a clock. HTTP statuses, Content-Type/Retry-After headers and LatencyMS
// belong to M5-03-03; do not pull them forward.
//
// Decisions this file implements:
//   - [non-reserved-defaults-to-accept] mockTriggerFor returns mockTriggerAccept EXPLICITLY for
//     an unallocated input; it is never a free map-miss default, which a reader cannot see.
//   - [reserved-is-luhn-invalid] every ALLOCATED trigger TIN matches the shipped
//     `buyer-tin-format` rule ^[0-9]{8}-[0-9]{4}$ (so it can actually reach submission) AND is
//     Luhn-invalid (so tools/fixturegen, the repo's only TIN generator, provably cannot mint
//     one). 99999999-0008 is the UNIQUE Luhn-VALID member of the -000X range and is therefore
//     permanently unallocatable; -0009 already exists as an unrelated literal at
//     internal/invoice/payload_fingerprint_test.go:68. Both live in mockNeverAllocate.
//   - [irn-is-identity-keyed-not-content-keyed] the IRN reads exactly two envelope fields (ID
//     and IssueDate), so it is STABLE across a change to an amount, a line or a party. The
//     CSID and the QR payload are functions of the WHOLE wire and change with any byte.
//   - [their-field-our-path] the 422 body names the field in the APP's vocabulary
//     (mockRejectField); the Reason we hand upward names it in ours (mockRejectPath). The
//     asymmetry is the whole exercise.
//   - [base64-is-rawurl-everywhere] BOTH the CSID and the QR payload use base64.RawURLEncoding,
//     overriding the story's StdEncoding for the QR. RawURL is the repo's unanimous convention
//     (auth/jwks.go:104, auth/mockissuer.go:94-95); StdEncoding appears nowhere.
//   - [ref-enforces-its-own-invariants] decodeMockRef rejects a negative poll count and a blank
//     IRN, not merely malformed encoding -- M5-03-04 returns Accepted{IRN: ids.IRN} straight out
//     of the ref and L07 requires that IRN non-blank.
package submission

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// mockTrigger is a STRING type, not an int enum, so a failure message, a log line and
// docs/mock-app-adapter.md all print the same token.
type mockTrigger string

const (
	mockTriggerAccept      mockTrigger = "accept"
	mockTriggerReject      mockTrigger = "reject"
	mockTriggerPending     mockTrigger = "pending"
	mockTriggerUnavailable mockTrigger = "unavailable"
	mockTriggerSlow        mockTrigger = "slow"
	mockTriggerTimeout     mockTrigger = "timeout"
	mockTriggerConnection  mockTrigger = "connection"
)

// The reserved block: 99999999-#### , all 10 000 values, reserved permanently. The 8-digit
// prefix is forced by the shipped buyer-tin-format rule
// (migrations/20260711121327_seed_mbs_v1.sql:16) -- a trigger TIN failing that regex would be
// rejected by validation and never reach submission at all.
const (
	mockReservedPrefix = "99999999-"

	mockTINAccept      = "99999999-0001"
	mockTINReject      = "99999999-0002"
	mockTINPending     = "99999999-0003"
	mockTINUnavailable = "99999999-0004"
	mockTINSlow        = "99999999-0005"
	mockTINTimeout     = "99999999-0006"
	mockTINConnection  = "99999999-0007"
)

// mockNeverAllocate lists reserved values that must NEVER be given a trigger.
//
// A slice, not a const block, because the specs iterate it. It is package-level and technically
// mutable; nothing in this package writes to it and nothing may start.
var mockNeverAllocate = []string{
	"99999999-0008", // the one Luhn-VALID value in -000X: fixturegen CAN mint it
	"99999999-0009", // already a live literal at internal/invoice/payload_fingerprint_test.go:68
}

// mockAllocation is one row of the scripted outcome table.
type mockAllocation struct {
	TIN     string
	Trigger mockTrigger
}

// mockAllocations is an ordered SLICE, not a map: seven entries make a linear scan free,
// declaration order is stable for both the specs and the doc table, and a map would invite
// nondeterministic range order into a package whose entire point is determinism.
//
// The order here IS the order of the table in docs/mock-app-adapter.md. -0008 and -0009 are
// absent on purpose; see mockNeverAllocate.
var mockAllocations = []mockAllocation{
	{TIN: mockTINAccept, Trigger: mockTriggerAccept},
	{TIN: mockTINReject, Trigger: mockTriggerReject},
	{TIN: mockTINPending, Trigger: mockTriggerPending},
	{TIN: mockTINUnavailable, Trigger: mockTriggerUnavailable},
	{TIN: mockTINSlow, Trigger: mockTriggerSlow},
	{TIN: mockTINTimeout, Trigger: mockTriggerTimeout},
	{TIN: mockTINConnection, Trigger: mockTriggerConnection},
}

// The APP's own response codes. Foreign-looking on purpose: nothing about them resembles our
// kebab-case validation rule keys.
const (
	mockCodeAccepted    = "NGE-2000"
	mockCodePending     = "NGE-2020"
	mockCodeRejected    = "NGE-4102"
	mockCodeUnavailable = "NGE-5030"
)

const (
	// mockRejectPath is OURS -- the dotted path internal/invoice's MBSPayload emits and the
	// shipped buyer-tin-format rule resolves. It appears on Reason.Path, never in a body.
	mockRejectPath = "buyer.tin"
	// mockRejectField is THEIRS -- it appears ONLY inside the synthesized 422 body.
	mockRejectField = "customer.taxIdentifier"
)

const (
	// mockRefPrefix opens every Ref this adapter issues, so a foreign ref is rejected before
	// any decoding is attempted.
	mockRefPrefix = "mockapp-v1."
	// mockPollAfterSeconds is declared HERE, not in M5-03-05, because the 202 body hard-codes
	// it and M5-03-05's mockPollBackoff must be mockPollAfterSeconds * time.Second rather than
	// a second literal 5.
	mockPollAfterSeconds = 5
	// mockLatencyEnv is declared HERE so AC-8's doc spec asserts against the SYMBOL from day
	// one. M5-03-05 must NOT redeclare it -- a duplicate const is a compile error, which is
	// the forcing function.
	mockLatencyEnv = "APP_ADAPTER_MOCK_LATENCY"
)

const (
	// mockServiceID is the middle segment of the IRN: <docRef>-<serviceID>-<YYYYMMDD>, the
	// shape the real FIRS MBS IRN takes.
	mockServiceID = "FBMOCK01"
	// mockIRNDateLayout renders the IRN's date part; mockIssueDateLayout (mock_wire.go:43)
	// parses the envelope's IssueDate on the way in.
	mockIRNDateLayout = "20060102"
	// mockIRNNoDate is what an absent or unparseable IssueDate degrades to. The IRN stays
	// non-blank either way (L07).
	mockIRNNoDate = "00000000"
	// mockDocRefMaxLen truncates the sanitised document reference. Sanitising happens BEFORE
	// truncating, so the cut can never split a multi-byte rune.
	mockDocRefMaxLen = 24
	// mockDocRefFallbackPrefix opens the digest-derived docRef used when the document has no
	// usable invoice number at all.
	mockDocRefFallbackPrefix = "INV"
	// mockDocRefFallbackHexLen is how many hex characters of the wire digest the fallback
	// docRef carries.
	mockDocRefFallbackHexLen = 8
)

// ErrMockUnknownRef is the sentinel decodeMockRef wraps, so M5-03-05 can branch with errors.Is
// rather than on message text. This is the error the contract suite's L14 probe
// (Ref("contract-suite-never-issued-ref"), contract_test.go:300) must land on.
var ErrMockUnknownRef = errors.New("submission: mock poll ref was not issued by this adapter")

// mockIdentifiers is the synthesized identifier triple. Carried as a UNIT because all three
// travel together through Accepted, through the 200 body and through the Ref -- four positional
// same-typed string arguments are a transposition waiting to happen.
type mockIdentifiers struct {
	IRN       string
	CSID      string
	QRPayload string
}

// mockQR is the decoded shape of QRPayload: compact JSON, base64url-encoded.
type mockQR struct {
	IRN  string `json:"irn"`
	CSID string `json:"csid"`
	TIN  string `json:"tin"` // the SUPPLIER TIN -- the party the authority clears
	Amt  string `json:"amt"` // LegalMonetaryTotal.PayableAmount.Value, "" when absent
	Cur  string `json:"cur"` // DocumentCurrencyCode
}

// mockRefPayload is the decoded shape of a Ref: the remaining poll count plus the verdict the
// pending submission will converge on. [ref-carries-the-verdict] -- the caller must persist the
// Ref carried by each Pending.
type mockRefPayload struct {
	N    int    `json:"n"`
	IRN  string `json:"irn"`
	CSID string `json:"csid"`
	QR   string `json:"qr"`
}

// mockTriggerFor maps a buyer TIN onto its scripted outcome.
//
// EXACT string match, deliberately NOT normalised -- the opposite ruling from registry.go's
// IsProduction, which normalises to protect a fail-CLOSED boot gate. Normalising here would
// WIDEN the set of inputs that activate a scripted outcome, which is the wrong direction.
func mockTriggerFor(buyerTIN string) mockTrigger {
	for _, a := range mockAllocations {
		if a.TIN == buyerTIN {
			return a.Trigger
		}
	}
	// EXPLICIT, not a map-miss default: an unallocated TIN -- including "" and every reserved
	// suffix nobody has claimed -- takes the ordinary accept path
	// ([non-reserved-defaults-to-accept]).
	return mockTriggerAccept
}

// mockIdentifiersFor synthesizes the IRN, CSID and QR payload. Pure: no clock, no randomness,
// no counter. w supplies the digest the CSID and QR are keyed on; env supplies the document
// identity the IRN is keyed on ([irn-is-identity-keyed-not-content-keyed]).
func mockIdentifiersFor(w Wire, env mockEnvelope) mockIdentifiers {
	digest := sha256.Sum256(w)

	ids := mockIdentifiers{
		// Exactly two envelope fields, no digest: an amount, a line or a party may change
		// without moving the IRN. The real FIRS MBS IRN carries no content digest either.
		IRN:  mockDocRef(env.ID, digest) + "-" + mockServiceID + "-" + mockIRNDatePart(env.IssueDate),
		CSID: base64.RawURLEncoding.EncodeToString(digest[:]),
	}

	// The payable amount is nil-safe: an all-nil-money canonical carries no PayableAmount at
	// all, and "" is the honest rendering of absent -- never "0".
	amount := ""
	if payable := env.LegalMonetaryTotal.PayableAmount; payable != nil {
		amount = payable.Value
	}

	// The QR carries the CSID, so it moves with any byte of the wire even though the IRN does
	// not. Its tin is the SUPPLIER's: the buyer TIN is the trigger channel, and stamping a
	// reserved trigger value into every accepted invoice's payload would be exactly wrong.
	ids.QRPayload = base64.RawURLEncoding.EncodeToString([]byte(mockJSONBody(mockQR{
		IRN:  ids.IRN,
		CSID: ids.CSID,
		TIN:  mockSupplierTIN(env),
		Amt:  amount,
		Cur:  env.DocumentCurrencyCode,
	})))

	return ids
}

// mockDocRef sanitises a document reference for the IRN: ToUpper FIRST, then strip to
// [A-Z0-9-], then truncate to mockDocRefMaxLen. The order is load-bearing -- stripping first
// would delete every lowercase letter, turning "inv-001" into "-001". Empty AFTER sanitisation
// degrades to mockDocRefFallbackPrefix + the first mockDocRefFallbackHexLen UPPERCASE hex
// characters of digest, or the IRN would mix cases.
func mockDocRef(id string, digest [sha256.Size]byte) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(id) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	ref := b.String()

	if ref == "" {
		// %X over the first bytes of the digest: two hex characters per byte, upper-cased so the
		// composed IRN stays inside [A-Z0-9-].
		return fmt.Sprintf("%s%X", mockDocRefFallbackPrefix, digest[:mockDocRefFallbackHexLen/2])
	}
	if len(ref) > mockDocRefMaxLen {
		// Safe on bytes: everything that survived the loop is single-byte ASCII.
		ref = ref[:mockDocRefMaxLen]
	}
	return ref
}

// mockIRNDatePart renders an envelope IssueDate as YYYYMMDD. ANY error, including an empty
// input, degrades to mockIRNNoDate. time.Parse is a parser, not a clock.
func mockIRNDatePart(issueDate string) string {
	d, err := time.Parse(mockIssueDateLayout, issueDate)
	if err != nil {
		return mockIRNNoDate
	}
	return d.Format(mockIRNDateLayout)
}

// mockSupplierTIN reads the supplier TIN back out of a parsed envelope, for the QR payload.
// Total and nil-safe, the mirror of mockBuyerTIN (mock_wire.go:233).
func mockSupplierTIN(env mockEnvelope) string {
	scheme := env.AccountingSupplierParty.Party.PartyTaxScheme
	if scheme == nil {
		return ""
	}
	return scheme.CompanyID
}

// mockRejectionReasons returns the reason set the reject trigger hands upward.
//
// A FUNCTION returning a FRESH slice, never a package var: Rejected.Reasons crosses the adapter
// seam, and a shared backing array is the exact L04 failure mode contract_red_test.go:57-58
// documents.
func mockRejectionReasons() []Reason {
	// A composite literal, evaluated afresh on every call: the returned slice shares no backing
	// array with any other call's.
	return []Reason{{
		Code: mockCodeRejected,
		// The message is the APP's, so it reads like an authority verdict rather than one of our
		// validation strings -- but it is what the SPA shows the operator, so it must say what to
		// do about it.
		Message: "Customer tax identifier is not registered with the tax authority.",
		// OURS. The 422 body next door names the same field mockRejectField; translating between
		// the two vocabularies is the adapter's job and the whole point of the exercise.
		Path: mockRejectPath,
	}}
}

// mockAcceptedBody renders the synthesized 200 body.
func mockAcceptedBody(ids mockIdentifiers) string {
	return mockJSONBody(map[string]any{
		"status":  "ACCEPTED",
		"code":    mockCodeAccepted,
		"message": "Invoice cleared.",
		"data": map[string]any{
			"irn":  ids.IRN,
			"csid": ids.CSID,
			"qr":   ids.QRPayload,
		},
	})
}

// mockRejectedBody renders the synthesized 422 body. It names the field in the APP's OWN
// vocabulary (mockRejectField) and must never contain our dotted path.
func mockRejectedBody() string {
	return mockJSONBody(map[string]any{
		"status":  "REJECTED",
		"code":    mockCodeRejected,
		"message": "Invoice was not cleared.",
		"errors": []map[string]any{{
			"code":    mockCodeRejected,
			"message": "Customer tax identifier is not registered with the tax authority.",
			"field":   mockRejectField,
		}},
	})
}

// mockPendingBody renders the synthesized 202 body, carrying the Ref the caller must persist.
func mockPendingBody(ref Ref) string {
	return mockJSONBody(map[string]any{
		"status":  "PENDING",
		"code":    mockCodePending,
		"message": "Invoice queued for clearance.",
		"data": map[string]any{
			"reference": string(ref),
			// The same constant M5-03-05's mockPollBackoff is derived from, never a second
			// literal 5.
			"pollAfterSeconds": mockPollAfterSeconds,
		},
	})
}

// mockUnavailableBody renders the synthesized 503 body. No data block: the authority decided
// nothing.
func mockUnavailableBody() string {
	// No "errors" key either: a 503 is a transport verdict, not a validation one
	// ([errors-never-verdicts]).
	return mockJSONBody(map[string]any{
		"status":  "ERROR",
		"code":    mockCodeUnavailable,
		"message": "Clearance service is temporarily unavailable.",
	})
}

// mockJSONBody marshals v compactly and returns "" on the unreachable marshal error rather than
// panicking -- a panic trips L15/L14, a contract violation; an empty body is a cosmetic one.
//
// json.Marshal, never Encoder.Encode: every body here is archived verbatim as
// app_exchange.response_body and Encode's trailing newline would ride along.
func mockJSONBody(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// encodeMockRef mints the opaque pending handle: mockRefPrefix + base64url(compact JSON).
func encodeMockRef(n int, ids mockIdentifiers) Ref {
	return Ref(mockRefPrefix + base64.RawURLEncoding.EncodeToString([]byte(mockJSONBody(mockRefPayload{
		N:    n,
		IRN:  ids.IRN,
		CSID: ids.CSID,
		QR:   ids.QRPayload,
	}))))
}

// decodeMockRef reverses encodeMockRef. It errors, wrapping ErrMockUnknownRef, for FOUR
// classes: a wrong or missing prefix (including "" and the contract suite's
// "contract-suite-never-issued-ref"); bad base64; bad JSON (where a truncated ref lands); and
// an INVARIANT violation -- a negative poll count or a blank IRN
// ([ref-enforces-its-own-invariants]).
func decodeMockRef(ref Ref) (int, mockIdentifiers, error) {
	// CutPrefix, not TrimPrefix: only CutPrefix distinguishes "the prefix was absent" from "the
	// prefix was there and the remainder is empty", and those two land in different classes.
	encoded, ok := strings.CutPrefix(string(ref), mockRefPrefix)
	if !ok {
		return 0, mockIdentifiers{}, fmt.Errorf("%w: ref does not carry the %q prefix", ErrMockUnknownRef, mockRefPrefix)
	}

	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return 0, mockIdentifiers{}, fmt.Errorf("%w: %w", ErrMockUnknownRef, err)
	}

	var payload mockRefPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0, mockIdentifiers{}, fmt.Errorf("%w: %w", ErrMockUnknownRef, err)
	}

	// The INVARIANT class. A ref is trivially constructible by hand, and M5-03-04's convergence
	// branch returns Accepted{IRN: ids.IRN} straight out of one -- so a blank IRN here would
	// mint an Accepted that violates L07. Enforcing once at the codec boundary beats enforcing
	// at every consumer.
	if payload.N < 0 {
		return 0, mockIdentifiers{}, fmt.Errorf("%w: poll count %d is negative", ErrMockUnknownRef, payload.N)
	}
	if strings.TrimSpace(payload.IRN) == "" {
		return 0, mockIdentifiers{}, fmt.Errorf("%w: ref carries a blank IRN", ErrMockUnknownRef)
	}

	return payload.N, mockIdentifiers{
		IRN:       payload.IRN,
		CSID:      payload.CSID,
		QRPayload: payload.QR,
	}, nil
}
