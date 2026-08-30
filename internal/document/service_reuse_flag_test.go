// service_reuse_flag_test.go: Service.Store must hand its caller the reuse verdict Upsert
// already computes and service.go:66 currently discards.
//
// Reflection, not the compile-time var pin at service_test.go:423: while the signature is
// still (Document, error) a var pin would fail to BUILD, and a build failure is not a test
// result. Stage 3 widens the signature and updates that pin.
package document_test

import (
	"context"
	"io"
	"reflect"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/document"
)

func TestServiceStore_ReturnsTheReuseFlag(t *testing.T) {
	// Control: the flag exists one layer down. If Upsert stopped returning it, the assertion
	// below would be asking Service.Store for a value nothing computes.
	if n := reflect.TypeOf((*document.Store).Upsert).NumOut(); n != 3 {
		t.Fatalf("Store.Upsert returns %d value(s), want 3 (Document, bool, error); there is no reuse flag to propagate, so the assertion below means nothing", n)
	}

	got := reflect.TypeOf((*document.Service).Store)
	if n := got.NumIn(); n != 6 {
		t.Fatalf("Service.Store takes %d parameter(s), want 6 (receiver, ctx, filename, contentType, size, body); reflection is reading a different symbol than the one under test", n)
	}

	want := reflect.TypeOf(func(*document.Service, context.Context, string, string, int64, io.ReadSeeker) (document.Document, bool, error) {
		return document.Document{}, false, nil
	})
	if got != want {
		t.Errorf("Service.Store is %s, want %s -- POST /v1/documents cannot report reused without it", got, want)
	}
}
