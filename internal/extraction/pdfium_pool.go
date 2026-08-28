// pdfium_pool.go: the process-wide wazero pool. The only file that may import a go-pdfium
// backend (TestPDFium_UsesTheWebAssemblyBackendOnly).
package extraction

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/webassembly"
)

// Matches MaxWorkers for QueueName in cmd/submission/main.go, so a third concurrent instance
// can never be needed (TestPDFiumMaxTotalMatchesTheQueueWorkerCount).
const pdfiumMaxTotal = 2

// Entries into the once-func, read by TestPDFiumPool_BuiltAtMostOnce.
var pdfiumBuilds atomic.Int64

// Lazy: Init compiles the embedded wasm, costing about a second and a ~254 MiB Sys plateau,
// which every submission replica would otherwise pay at boot for a queue nothing may feed.
var pdfiumPool = sync.OnceValues(func() (pdfium.Pool, error) {
	pdfiumBuilds.Add(1)
	return webassembly.Init(webassembly.Config{
		MinIdle:      0,
		MaxIdle:      1,
		MaxTotal:     pdfiumMaxTotal,
		ReuseWorkers: true,
	})
})

// pdfiumPoolFor builds the pool on first use. ctx.Err() is tested before the pool is touched so
// a cancelled call never pays the compile (law E12).
func pdfiumPoolFor(ctx context.Context) (pdfium.Pool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return pdfiumPool()
}
