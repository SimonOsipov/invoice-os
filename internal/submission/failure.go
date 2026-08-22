package submission

// FailureKind is the reason a submission landed the invoice in status='failed'.
// A plain scalar, not a Result variant: it does not describe an attempt's
// outcome (Result's job) but WHY the queued->failed dead-letter edge fired, so
// it travels as an explicit argument -- MarkFailed's and recordFailureAudit's --
// instead of widening the sealed union.
type FailureKind string

const (
	FailurePayloadNotBuilt       FailureKind = "payload_not_built"
	FailureNeverAcknowledged     FailureKind = "never_acknowledged"
	FailureAcknowledgedNoVerdict FailureKind = "acknowledged_no_verdict"
)

// Valid mirrors invoices.failure_kind's CHECK IN-list exactly ([no-second-encoding-of-unknown]).
func (k FailureKind) Valid() bool {
	switch k {
	case FailurePayloadNotBuilt, FailureNeverAcknowledged, FailureAcknowledgedNoVerdict:
		return true
	default:
		return false
	}
}
