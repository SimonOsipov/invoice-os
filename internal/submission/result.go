package submission

import (
	"net/http"
	"time"
)

// Result is one attempt's outcome. Sealed: isResult() is unexported, so the four variants
// below are the complete set and no package outside internal/submission can add a fifth
// ([sealed-result-union]).
type Result interface{ isResult() }

// Accepted — the authority read the invoice and cleared it.
type Accepted struct {
	IRN       string // invoices.irn — REQUIRED and non-blank (law L07, enforced in M5-02-06)
	CSID      string // invoices.csid       — "" means the authority returned none → SQL NULL
	QRPayload string // invoices.qr_payload — "" means the authority returned none → SQL NULL
}

// Rejected — the authority read the invoice and refused it. NEVER a transport or HTTP
// failure ([errors-never-verdicts]).
type Rejected struct {
	Reasons []Reason // invoices.rejection_reasons; non-empty (law L08, enforced in M5-02-06)
}

// Reason is one element of invoices.rejection_reasons ([reason-shape]).
type Reason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
}

// Pending — the authority accepted the submission but deferred the verdict.
type Pending struct {
	PollAfter time.Time // not zero (law L09); the SCHEDULE that honours it is M5-04's
	Ref       Ref       // not empty (law L09); persisted to submission_jobs.poll_ref
}

// Retryable — nothing about the invoice was decided. Transport failure, timeout, cancelled
// context, 5xx, 401, an unparseable body: all Retryable, none Rejected.
type Retryable struct {
	Err error // non-nil (law L10)
}

func (Accepted) isResult()  {}
func (Rejected) isResult()  {}
func (Pending) isResult()   {}
func (Retryable) isResult() {}

// KindOf names a Result variant for logging and for the contract suite's messages.
// Returns "" for a nil Result.
//
// STAGE 2.5 STUB: this body deliberately returns "" unconditionally, ignoring r. It exists so
// the package compiles and TestKindOf can demonstrate a real assertion-level RED (rather than
// a compile error) before the executor implements the real type switch.
func KindOf(r Result) string {
	return ""
}

// Evidence is what the adapter observed during one attempt. It is FAITHFUL — the adapter
// does not scrub it; RecordExchange applies ScrubHeaders and SafeBody itself
// ([scrub-is-the-recorders-job]).
//
// The zero Evidence is the honest record of an attempt that never left the process:
// ReachedWire false, every pointer nil.
type Evidence struct {
	RequestHeaders  http.Header
	ResponseHeaders http.Header
	RequestBody     *string // nil when nothing was sent
	ResponseBody    *string // nil when nothing came back
	HTTPStatus      *int    // nil when no response was received
	LatencyMS       *int    // nil when not measured; >= 0 when set (app_exchange CHECK)

	// ReachedWire reports whether the bytes LEFT OUR PROCESS. It is the caller's sole input
	// for choosing between app_exchange.outcome 'sent' and 'connection_failed' — the
	// distinction exchange.go says the evidence log exists to preserve.
	ReachedWire bool
}
