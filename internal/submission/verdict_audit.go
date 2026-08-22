// verdict_audit.go: this package's only 08 audit writes -- one row per terminal submission
// outcome, inside the caller's job transaction (System Design §6). Verdicts go through
// recordVerdictAudit, failures through recordFailureAudit, both via recordSubmissionAudit.
package submission

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
)

// recordSubmissionAudit is the single construction site for the submission.<outcome> event name
// and the only audit.Record call in this package, so the vocabulary cannot grow a fourth value
// by accident. Pinned by TestSubmissionAudit_EventNameHasOneConstructionSite and
// TestSubmissionAudit_OutcomeVocabularyIsExactlyThree.
//
// tx is the caller's tenant-scoped transaction, so the audit row shares its fate exactly like
// every other write in the same queue.OncePerJob closure. actor is the literal "system" (not the
// tenant -- internal/audit.Record never takes one; tx's app.current_tenant GUC already scopes the
// row), matching internal/invoice's SystemActor(tenantID).Subject BY VALUE: importing that package
// would break TestSubmissionPackage_DoesNotImportInvoicePackage.
func recordSubmissionAudit(ctx context.Context, tx pgx.Tx, outcome string, payload map[string]any) error {
	return audit.Record(ctx, tx, "system", "submission."+outcome, payload)
}

// recordVerdictAudit writes the row for a terminal verdict. payload is a summary only -- never the
// full Accepted/Rejected wire payload ([audit-payloads]: app_exchange already holds those bodies).
//
// reference is included only when non-empty (the scripted IRN on Accepted); on Rejected the caller
// passes "" and the key is left ABSENT from the payload entirely, not written as an empty string
// ([audit-reference-is-the-irn]). Pinned by TestRecordVerdictAudit_AcceptedRejectedUnchanged.
func recordVerdictAudit(ctx context.Context, tx pgx.Tx, invoiceID, jobID, outcome, reference string) error {
	payload := map[string]any{
		"invoice_id":        invoiceID,
		"submission_job_id": jobID,
		"outcome":           outcome,
	}
	if reference != "" {
		payload["reference"] = reference
	}
	return recordSubmissionAudit(ctx, tx, outcome, payload)
}

// recordFailureAudit writes the row for a terminal failure -- every branch that leaves the
// invoice in status='failed' with no further attempt, whichever submission_jobs state it sets.
// The reason travels as the FailureKind enum, never submission_jobs.last_error: that column is
// adapter-shaped free text that can carry wire detail, which [audit-payloads] forbids and
// submission_jobs already holds. Pinned by TestRecordFailureAudit_PayloadIsExactlyFourKeys and
// TestRecordFailureAudit_CarriesNoWireDetail.
func recordFailureAudit(ctx context.Context, tx pgx.Tx, invoiceID, jobID string, kind FailureKind) error {
	return recordSubmissionAudit(ctx, tx, "failed", map[string]any{
		"invoice_id":        invoiceID,
		"submission_job_id": jobID,
		"outcome":           "failed",
		"failure_kind":      string(kind),
	})
}
