// verdict_audit.go: the shared 08 audit write for SubmitWorker/PollWorker's terminal Accepted
// and Rejected branches (System Design §6). M5-05-04 (task-240) added this for SubmitWorker's
// two synchronous branches; PollWorker's own Accepted/Rejected wiring is M5-05-05 (task-241).
package submission

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
)

// recordVerdictAudit writes one submission.<outcome> audit_log row on tx -- the caller's
// tenant-scoped transaction, so the audit row shares tx's fate exactly like every other write
// in the same queue.OncePerJob closure. actor is the literal "system" (not the tenant --
// internal/audit.Record never takes one; tx's app.current_tenant GUC already scopes the row),
// independently matching internal/invoice's SystemActor(tenantID).Subject convention by value
// since importing that package would violate [mapper-lives-in-03]/deps_test.go.
//
// payload is a summary only -- invoice_id, submission_job_id, outcome -- never the full
// Accepted/Rejected wire payload ([audit-payloads]: app_exchange already holds those bodies).
// reference is included only when non-empty (the scripted IRN on Accepted); on Rejected the
// caller passes "" and the key is left ABSENT from the payload entirely, not written as an
// empty string ([audit-reference-is-the-irn]).
func recordVerdictAudit(ctx context.Context, tx pgx.Tx, invoiceID, jobID, outcome, reference string) error {
	payload := map[string]any{
		"invoice_id":        invoiceID,
		"submission_job_id": jobID,
		"outcome":           outcome,
	}
	if reference != "" {
		payload["reference"] = reference
	}
	return audit.Record(ctx, tx, "system", "submission."+outcome, payload)
}
