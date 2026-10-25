package queue

import (
	"context"

	"github.com/riverqueue/river/rivertype"
)

// ReportFinalError drives the production final-attempt reporter for a job of kind with encoded args.
func ReportFinalError(kind string, args []byte, err error) {
	errorReporter{}.HandleError(context.Background(), &rivertype.JobRow{Kind: kind, Attempt: 8, MaxAttempts: 8, EncodedArgs: args}, err)
}
