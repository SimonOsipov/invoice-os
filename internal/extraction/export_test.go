// export_test.go: the DB-backed worker suite lives in package extraction_test, which cannot
// name the unexported args type. These constructors hand it a value of that type instead.
// Compiled only under go test, so the production surface is unchanged.
package extraction

import (
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// NewExtractArgsForTest builds the args EnqueueTx takes. The return type is river.JobArgs, so
// the caller never writes the concrete name.
func NewExtractArgsForTest(tenantID, documentID, key string) river.JobArgs {
	return extractArgs{TenantID: tenantID, DocumentID: documentID, IdempotencyKey: key}
}

// NewExtractJobForTest builds the job Work takes, for the specs that call Work directly
// rather than through a River client.
func NewExtractJobForTest(riverJobID int64, attempt, maxAttempts int, tenantID, documentID, key string) *river.Job[extractArgs] {
	return &river.Job[extractArgs]{
		JobRow: &rivertype.JobRow{ID: riverJobID, Attempt: attempt, MaxAttempts: maxAttempts},
		Args:   extractArgs{TenantID: tenantID, DocumentID: documentID, IdempotencyKey: key},
	}
}
