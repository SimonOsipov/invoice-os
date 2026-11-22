package erp

import "github.com/riverqueue/river"

// Workers is every ERP job worker; cmd/worker builds it once and hands it to the one bundle.
type Workers struct {
	Pull   *PullWorker
	Ingest *IngestWorker
}

// AddTo registers each non-nil ERP kind on the bundle queue.New reads.
func AddTo(bundle *river.Workers, w *Workers) {
	if w.Pull != nil {
		river.AddWorker(bundle, w.Pull)
	}
	if w.Ingest != nil {
		river.AddWorker(bundle, w.Ingest)
	}
}
