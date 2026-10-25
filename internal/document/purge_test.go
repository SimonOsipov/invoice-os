package document_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const purgeTenantB = "22222222-2222-2222-2222-222222222222"

func seed(t *testing.T, store document.ObjectStore, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if err := store.Put(context.Background(), k, bytes.NewReader([]byte("x")), 1); err != nil {
			t.Fatalf("seed Put(%s): %v", k, err)
		}
	}
}

func TestDeleteTenantObjects_RemovesDocumentAndPageKeysOnly(t *testing.T) {
	mem, store := newMemS3(t)
	keyB := document.StorageKey(purgeTenantB, keyHash)
	near := "tenants/" + keyTenantA + "x/k"
	seed(t, store, document.StorageKey(keyTenantA, keyHash), extraction.PageKey(keyTenantA, keyHash, 1), keyB, near)

	n, err := document.DeleteTenantObjects(context.Background(), store, keyTenantA)
	if err != nil || n != 2 {
		t.Fatalf("DeleteTenantObjects() = %d, %v, want 2, nil", n, err)
	}
	if got := mem.keysUnder("tenants/"); len(got) != 2 || !contains(got, keyB) || !contains(got, near) {
		t.Errorf("remaining keys = %v, want %s and %s", got, keyB, near)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestDeleteTenantObjects_DeletesInBatchesOfAtMostOneThousand(t *testing.T) {
	mem, store := newMemS3(t)
	for i := 0; i < 2501; i++ {
		mem.objects[fmt.Sprintf("tenants/%s/k%05d", keyTenantA, i)] = nil
	}
	n, err := document.DeleteTenantObjects(context.Background(), store, keyTenantA)
	if err != nil || n != 2501 {
		t.Fatalf("DeleteTenantObjects() = %d, %v, want 2501, nil", n, err)
	}
	if len(mem.deleteKeys) != 3 {
		t.Fatalf("DeleteObjects requests = %v, want 3", mem.deleteKeys)
	}
	for _, c := range mem.deleteKeys {
		if c > 1000 {
			t.Errorf("a DeleteObjects request held %d keys, want at most 1000", c)
		}
	}
	if left := mem.keysUnder("tenants/" + keyTenantA + "/"); len(left) != 0 {
		t.Errorf("%d keys remain under the prefix", len(left))
	}
}

func TestDeleteTenantObjects_CanonicalisesTheTenantID(t *testing.T) {
	// Hex letters, so ToUpper changes the text.
	const lower = "abcdefab-cdef-abcd-efab-cdefabcdefab"
	mem, store := newMemS3(t)
	seed(t, store, document.StorageKey(lower, keyHash))

	n, err := document.DeleteTenantObjects(context.Background(), store, strings.ToUpper(lower))
	if err != nil || n != 1 {
		t.Fatalf("DeleteTenantObjects(upper) = %d, %v, want 1, nil", n, err)
	}
	if got := mem.keysUnder("tenants/"); len(got) != 0 {
		t.Errorf("keys remain: %v", got)
	}
}

func TestDeleteTenantObjects_RefusesAMalformedTenantID(t *testing.T) {
	for _, id := range []string{"", "not-a-uuid", "../"} {
		mem, store := newMemS3(t)
		if _, err := document.DeleteTenantObjects(context.Background(), store, id); err == nil {
			t.Errorf("DeleteTenantObjects(%q) returned nil, want an error", id)
		}
		if mem.requests() != 0 {
			t.Errorf("DeleteTenantObjects(%q) sent %d requests, want 0", id, mem.requests())
		}
	}
}

func TestDeleteTenantObjects_NoObjectsIsZero(t *testing.T) {
	mem, store := newMemS3(t)
	n, err := document.DeleteTenantObjects(context.Background(), store, keyTenantA)
	if err != nil || n != 0 {
		t.Fatalf("DeleteTenantObjects() = %d, %v, want 0, nil", n, err)
	}
	if mem.lists != 1 || mem.deletes != 0 {
		t.Errorf("sent %d List and %d DeleteObjects, want 1 and 0", mem.lists, mem.deletes)
	}
}

func TestDeleteTenantObjects_StopsOnAPerKeyDeleteError(t *testing.T) {
	mem, store := newMemS3(t)
	for i := 0; i < 1500; i++ {
		mem.objects[fmt.Sprintf("tenants/%s/k%05d", keyTenantA, i)] = nil
	}
	bad := fmt.Sprintf("tenants/%s/k%05d", keyTenantA, 1200)
	mem.failKey = bad

	n, err := document.DeleteTenantObjects(context.Background(), store, keyTenantA)
	if err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("error = %v, want it to name %s", err, bad)
	}
	if n != 1000 {
		t.Errorf("count = %d, want 1000", n)
	}
}

func TestDeleteTenantObjects_StopsWhenTheStoreKeepsADeletedKey(t *testing.T) {
	mem, store := newMemS3(t)
	seed(t, store, document.StorageKey(keyTenantA, "h1"), document.StorageKey(keyTenantA, "h2"), document.StorageKey(keyTenantA, "h3"))
	mem.dropDelete = true

	_, err := document.DeleteTenantObjects(context.Background(), store, keyTenantA)
	if err == nil || !strings.Contains(err.Error(), "still lists") {
		t.Fatalf("error = %v, want it to say %q", err, "still lists")
	}
	if mem.lists > 2 {
		t.Errorf("sent %d List requests, want at most 2", mem.lists)
	}
}

func TestDeleteTenantObjects_DeletesAKeyReputAfterAnOlderBatch(t *testing.T) {
	mem, store := newMemS3(t)
	for i := 0; i < 2500; i++ {
		mem.objects[fmt.Sprintf("tenants/%s/k%05d", keyTenantA, i)] = nil
	}
	mem.reputKey, mem.reputAfter = fmt.Sprintf("tenants/%s/k%05d", keyTenantA, 5), 2

	n, err := document.DeleteTenantObjects(context.Background(), store, keyTenantA)
	if err != nil || n != 2501 {
		t.Fatalf("DeleteTenantObjects() = %d, %v, want 2501, nil", n, err)
	}
	if got := mem.keysUnder("tenants/"); len(got) != 0 {
		t.Errorf("purge left %v", got)
	}
}

func TestDeleteTenantObjects_AcceptsEveryUUIDSpellingAndDeletesTheCanonicalPrefix(t *testing.T) {
	const lower = "abcdefab-cdef-abcd-efab-cdefabcdefab"
	for _, id := range []string{"AbCdEfAb-CdEf-aBcD-eFaB-cDeFaBcDeFaB", "abcdefabcdefabcdefabcdefabcdefab", "{" + lower + "}", "urn:uuid:" + lower} {
		mem, store := newMemS3(t)
		seed(t, store, document.StorageKey(lower, keyHash), extraction.PageKey(lower, keyHash, 1))
		n, err := document.DeleteTenantObjects(context.Background(), store, id)
		if err != nil || n != 2 {
			t.Errorf("DeleteTenantObjects(%q) = %d, %v, want 2, nil", id, n, err)
		}
		if got := mem.keysUnder("tenants/"); len(got) != 0 {
			t.Errorf("DeleteTenantObjects(%q) left %v", id, got)
		}
	}
}

func TestDeleteTenantObjects_StoreThatDropsDeletesOfAFullPageReportsTheBatchCount(t *testing.T) {
	mem, store := newMemS3(t)
	for i := 0; i < 1500; i++ {
		mem.objects[fmt.Sprintf("tenants/%s/k%05d", keyTenantA, i)] = nil
	}
	mem.dropDelete = true

	n, err := document.DeleteTenantObjects(context.Background(), store, keyTenantA)
	if err == nil || !strings.Contains(err.Error(), "still lists") {
		t.Fatalf("error = %v, want it to say %q", err, "still lists")
	}
	if n != 1000 || mem.lists != 2 || len(mem.deleteKeys) != 1 {
		t.Errorf("count=%d lists=%d deletes=%v, want 1000, 2, one request", n, mem.lists, mem.deleteKeys)
	}
}
