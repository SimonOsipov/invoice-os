// key_test.go: AC-3's specs, authored before internal/document/key.go exists.
package document_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/document"
)

// Both segments are server-derived: a tenant uuid from the verified JWT and a
// sha256 hex of the received bytes.
const (
	keyTenantA = "11111111-1111-1111-1111-111111111111"
	keyTenantB = "22222222-2222-2222-2222-222222222222"
	keyHash    = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

func TestStorageKey_Shape(t *testing.T) {
	got := document.StorageKey(keyTenantA, keyHash)
	want := "tenants/" + keyTenantA + "/" + keyHash

	if got != want {
		t.Fatalf("StorageKey(%q, %q) = %q, want %q", keyTenantA, keyHash, got, want)
	}
	if strings.HasPrefix(got, "/") {
		t.Errorf("StorageKey(%q, %q) = %q, want no leading slash — S3 would create an empty first path segment",
			keyTenantA, keyHash, got)
	}
}

// [dedupe-per-tenant] at the object layer: byte-identical uploads from two
// tenants must land on two distinct keys.
func TestStorageKey_DiffersAcrossTenantsForSameHash(t *testing.T) {
	a := document.StorageKey(keyTenantA, keyHash)
	b := document.StorageKey(keyTenantB, keyHash)

	if a == b {
		t.Fatalf("StorageKey(%q, H) and StorageKey(%q, H) both = %q, want two distinct keys for the same hash",
			keyTenantA, keyTenantB, a)
	}
	// Distinctness alone would pass on a key that ignores the hash, so pin
	// that each key carries its own tenant and the shared hash.
	for _, tc := range []struct {
		tenant string
		key    string
		other  string
	}{
		{keyTenantA, a, keyTenantB},
		{keyTenantB, b, keyTenantA},
	} {
		if !strings.Contains(tc.key, tc.tenant) {
			t.Errorf("StorageKey(%q, H) = %q, want it to contain the tenant %q", tc.tenant, tc.key, tc.tenant)
		}
		if strings.Contains(tc.key, tc.other) {
			t.Errorf("StorageKey(%q, H) = %q, want it NOT to contain the other tenant %q", tc.tenant, tc.key, tc.other)
		}
		if !strings.Contains(tc.key, keyHash) {
			t.Errorf("StorageKey(%q, H) = %q, want it to contain the content hash %q", tc.tenant, tc.key, keyHash)
		}
	}
}

// Core AC 4 is satisfied structurally, not by validation: no caller-supplied
// string (filename, content-type, form field, path segment) can reach a key
// because StorageKey has nowhere to put one. Adding a third parameter — or a
// variadic — fails here.
func TestStorageKey_HasNoCallerDerivedSegment(t *testing.T) {
	ft := reflect.TypeOf(document.StorageKey)

	if ft.Kind() != reflect.Func {
		t.Fatalf("document.StorageKey is a %v, want a func", ft.Kind())
	}
	if ft.IsVariadic() {
		t.Errorf("StorageKey is variadic (%v) — a variadic tail is a caller-derived segment by another name", ft)
	}
	if got := ft.NumIn(); got != 2 {
		t.Fatalf("StorageKey takes %d parameters (%v), want exactly 2 (tenantID, contentHash) — a third "+
			"parameter is by construction caller-derived and would breach Core AC 4", got, ft)
	}
	for i := 0; i < ft.NumIn(); i++ {
		if k := ft.In(i).Kind(); k != reflect.String {
			t.Errorf("StorageKey parameter %d is a %v, want string", i, k)
		}
	}
	if got := ft.NumOut(); got != 1 || ft.Out(0).Kind() != reflect.String {
		t.Errorf("StorageKey returns %v, want exactly one string", ft)
	}
}
