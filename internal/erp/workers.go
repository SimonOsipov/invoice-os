package erp

import "github.com/riverqueue/river"

// Workers is every ERP job worker; cmd/worker builds it once and hands it to the one bundle.
type Workers struct {
	Refresh      *RefreshWorker
	SecretDelete *SecretDeleteWorker
}

// AddTo registers the ERP kinds on the bundle queue.New reads.
func AddTo(bundle *river.Workers, w *Workers) {
	river.AddWorker(bundle, w.Refresh)
	river.AddWorker(bundle, w.SecretDelete)
}
