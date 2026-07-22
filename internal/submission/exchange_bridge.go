// M5-02-03 (task-218): the bridge between an adapter's Evidence (M5-02-01, adapter.go /
// result.go) and M5-01's Exchange (exchange.go). Neither file above is touched by this one
// (AC #6) -- this adds the Operation vocabulary, the five Outcome constants, and the pure
// ExchangeFor builder that satisfies Core AC-7 ("every attempt yields evidence the caller can
// persist... without the adapter itself touching the database") with code instead of prose,
// per Decision [caller-bridge-in-02].
package submission

// Operation names the direction of one attempt against the APP: a fresh submission or a
// resumption of a deferred verdict. Mirrors app_exchange.operation's CHECK
// (migrations/20260722093218_app_exchange.sql).
type Operation string

const (
	OpSubmit Operation = "submit"
	OpPoll   Operation = "poll"
)

// The five app_exchange.outcome values, matching the live CHECK as widened by
// migrations/20260722114935_app_exchange_connection_failed.sql. ExchangeFor only ever
// produces OutcomeSent or OutcomeConnectionFailed, derived from Evidence.ReachedWire -- the
// other three are M5-04 caller-side overwrites, out of scope here
// ([caller-bridge-in-02]).
const (
	OutcomeSent                  = "sent"
	OutcomeBlockedRateLimit      = "blocked_rate_limit"
	OutcomeSkippedAlreadyCleared = "skipped_already_cleared"
	OutcomeTransformFailed       = "transform_failed"
	OutcomeConnectionFailed      = "connection_failed"
)

// ExchangeFor builds the M5-01 Exchange for one adapter attempt: a's identity, the caller's
// own op/attempt/jobID/invoiceID, and the Evidence the adapter observed for this attempt.
//
// STAGE 2.5 STUB (task-218 / M5-02-03, Mode A RED bootstrap): deliberately returns a zero
// Exchange{} regardless of input, so the package compiles and every behavioural spec in
// exchange_bridge_test.go fails on a real assertion instead of a compile error. The real body
// -- deriving Outcome from ev.ReachedWire and passing every other field through unscrubbed --
// lands in this same subtask's Stage 2.5 implementation pass (Mode B).
func ExchangeFor(a Adapter, op Operation, attempt int, jobID, invoiceID string, ev Evidence) Exchange {
	return Exchange{}
}
