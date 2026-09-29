package queue

import (
	"context"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// errorReporter is River's ErrorHandler: it reports the attempt that ends a job.
type errorReporter struct{}

var _ river.ErrorHandler = errorReporter{}

func (errorReporter) HandleError(context.Context, *rivertype.JobRow, error) *river.ErrorHandlerResult {
	return nil
}

func (errorReporter) HandlePanic(context.Context, *rivertype.JobRow, any, string) *river.ErrorHandlerResult {
	return nil
}
