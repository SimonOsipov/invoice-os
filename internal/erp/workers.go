package erp

import "github.com/riverqueue/river"

// Workers is every ERP job worker; cmd/worker builds it once and hands it to the one bundle.
type Workers struct {
	Refresh      *RefreshWorker
	SecretDelete *SecretDeleteWorker
	Pull         *PullWorker
	Ingest       *IngestWorker
}

// AddTo registers the ERP kinds on the bundle queue.New reads. A nil worker is skipped.
func AddTo(bundle *river.Workers, w *Workers) {
	if w.Refresh != nil {
		river.AddWorker(bundle, w.Refresh)
	}
	if w.SecretDelete != nil {
		river.AddWorker(bundle, w.SecretDelete)
	}
	if w.Pull != nil {
		river.AddWorker(bundle, w.Pull)
	}
	if w.Ingest != nil {
		river.AddWorker(bundle, w.Ingest)
	}
}
