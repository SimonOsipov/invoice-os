package queue

import (
	"context"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// jobTracing is a pass-through until job attempts become transactions.
type jobTracing struct {
	river.MiddlewareDefaults
}

var (
	_ rivertype.JobInsertMiddleware = &jobTracing{}
	_ rivertype.WorkerMiddleware    = &jobTracing{}
)

func (jobTracing) InsertMany(ctx context.Context, _ []*rivertype.JobInsertParams, doInner func(context.Context) ([]*rivertype.JobInsertResult, error)) ([]*rivertype.JobInsertResult, error) {
	return doInner(ctx)
}

func (jobTracing) Work(ctx context.Context, _ *rivertype.JobRow, doInner func(context.Context) error) error {
	return doInner(ctx)
}
