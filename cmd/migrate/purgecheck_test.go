package main

import (
	"context"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type memStore struct{ keys map[string]bool }

func (m *memStore) Put(_ context.Context, key string, _ io.ReadSeeker, _ int64) error {
	m.keys[key] = true
	return nil
}

func (m *memStore) Get(context.Context, string, string) (document.Object, error) {
	return document.Object{}, nil
}

func (m *memStore) List(_ context.Context, prefix string) ([]string, error) {
	var out []string
	for k := range m.keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (m *memStore) DeleteMany(_ context.Context, keys []string) error {
	for _, k := range keys {
		delete(m.keys, k)
	}
	return nil
}

// TestPurgeCheckRun_PassesAgainstTheAppRole runs the check's SQL as invoice_app on the test database.
func TestPurgeCheckRun_PassesAgainstTheAppRole(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := &memStore{keys: map[string]bool{}}

	rows, objects, step, err := purgeCheckRun(ctx, pool, store)
	if err != nil {
		t.Fatalf("purgeCheckRun failed at %q: %v", step, err)
	}
	if rows != 1 || objects != 2 || len(store.keys) != 0 {
		t.Errorf("rows %d, objects %d, %d keys left; want 1, 2, 0", rows, objects, len(store.keys))
	}
}
