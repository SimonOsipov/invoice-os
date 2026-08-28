// pdfium_pool_internal_test.go: AC-7's two runtime pins. Internal, because the pool accessor and
// its build counter are unexported. The source scans live in pdfium_pool_test.go.
//
// TestPDFiumPool_BuiltAtMostOnce compiles the embedded wasm for real: about 1 s and a ~254 MiB
// Sys plateau, once, held for the rest of this test binary.
package extraction

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/klippa-app/go-pdfium"
)

// TestPDFiumPool_NotBuiltOnACancelledContext runs before TestPDFiumPool_BuiltAtMostOnce: the
// counter clause is only meaningful while the pool is still unbuilt, and it fatals rather than
// pass vacuously if some later test builds it first.
func TestPDFiumPool_NotBuiltOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	before := pdfiumBuilds.Load()
	pool, err := pdfiumPoolFor(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("pdfiumPoolFor on a cancelled context returned err %v, want context.Canceled: ctx.Err() is checked before the pool is touched, so a cancelled call never pays the compile", err)
	}
	if pool != nil {
		t.Errorf("pdfiumPoolFor on a cancelled context returned a pool; the guard runs after the build, not before it")
	}

	switch after := pdfiumBuilds.Load(); {
	case before != 0:
		t.Fatalf("the pool was already built (%d build(s)) before this test ran; the counter can no longer tell a guarded call from an already-fired once, so this clause must run first", before)
	case after != before:
		t.Errorf("the once-func ran %d time(s) on a cancelled call; it must not be entered at all", after-before)
	}
}

// TestPDFiumPool_BuiltAtMostOnce: one pool per process, under concurrent first calls.
func TestPDFiumPool_BuiltAtMostOnce(t *testing.T) {
	const callers = 32

	pools := make([]pdfium.Pool, callers)
	errs := make([]error, callers)
	start := make(chan struct{})

	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			pools[i], errs[i] = pdfiumPoolFor(context.Background())
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: pdfiumPoolFor: %v", i, err)
		}
	}
	if pools[0] == nil {
		t.Fatalf("caller 0 got a nil pool with no error; the identity check below would compare nothing")
	}
	for i, p := range pools {
		if p != pools[0] {
			t.Errorf("caller %d got a different pool from caller 0; the pool is not process-wide", i)
		}
	}
	if got := pdfiumBuilds.Load(); got != 1 {
		t.Errorf("the once-func ran %d time(s) across %d concurrent first calls, want 1: each build compiles the embedded wasm and adds its own ~254 MiB plateau", got, callers)
	}
}
