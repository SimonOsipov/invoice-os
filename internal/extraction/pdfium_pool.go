// pdfium_pool.go: the process-wide wazero pool. The only file that may import a go-pdfium
// backend (TestPDFium_UsesTheWebAssemblyBackendOnly).
package extraction

import (
	"context"
	"fmt"
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

// withPDFiumInstance borrows one pool instance for the duration of fn. Structural rather than
// remembered: at MaxTotal 2, one unreturned instance leaves the next extraction blocked inside
// GetInstanceWithContext with no error to show for it.
func withPDFiumInstance(ctx context.Context, fn func(pdfium.Pdfium) error) error {
	pool, err := pdfiumPoolFor(ctx)
	if err != nil {
		return err
	}

	inst, err := pool.GetInstanceWithContext(ctx)
	if err != nil {
		return fmt.Errorf("pdfium: get instance: %w", err)
	}
	defer inst.Close()

	return fn(inst)
}
