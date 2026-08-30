// service_reuse_flag_test.go: Service.Store's SIGNATURE carries Upsert's reuse verdict.
//
// The signature only. An inverted flag has the same type and passes here; the value is pinned
// by service_reuse_value_test.go.
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
