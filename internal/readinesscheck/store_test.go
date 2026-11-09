package readinesscheck

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("readinesscheck db test skipped: set DATABASE_URL (or run `make test-rls`)")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// addr returns a unique lower-case address and removes its rows at cleanup.
func addr(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	a := fmt.Sprintf("rc-%s@x.io", uuid.NewString()[:8])
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM readiness_checks WHERE email = $1`, a) })
	return a
}

func TestStore_CountFoldsTheAddress(t *testing.T) {
	pool := testPool(t)
	a, b := addr(t, pool), addr(t, pool)
	s := &Store{Pool: pool}
	ctx := context.Background()
	for _, e := range []string{a, a, b} {
		if err := s.Insert(ctx, Summary{Email: e, FileType: "csv"}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.Count(ctx, " "+a[:1]+"C"+a[2:]+" ")
	if err != nil || n != 2 {
		t.Fatalf("Count = %d, %v; want 2", n, err)
	}
}

func TestStore_CountOfANewAddressIsZero(t *testing.T) {
	pool := testPool(t)
	n, err := (&Store{Pool: pool}).Count(context.Background(), addr(t, pool))
	if err != nil || n != 0 {
		t.Fatalf("Count = %d, %v; want 0", n, err)
	}
}

func TestStore_InsertWritesTheSummary(t *testing.T) {
	pool := testPool(t)
	a := addr(t, pool)
	before := time.Now().Add(-time.Minute)
	if err := (&Store{Pool: pool}).Insert(context.Background(), Summary{Email: " " + a + " ", FileType: "csv", RowCount: 7, FailedRuleIDs: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	var email, ft string
	var rc int
	var ids []string
	var at time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT email, file_type, row_count, failed_rule_ids, created_at FROM readiness_checks WHERE email = $1`, a).
		Scan(&email, &ft, &rc, &ids, &at); err != nil {
		t.Fatal(err)
	}
	if email != a || ft != "csv" || rc != 7 || !slices.Equal(ids, []string{"a", "b"}) {
		t.Fatalf("row = %q %q %d %v", email, ft, rc, ids)
	}
	if at.Before(before) || at.After(time.Now().Add(time.Minute)) {
		t.Fatalf("created_at = %v", at)
	}
}

func TestStore_InsertOfADocumentHasRowCountZero(t *testing.T) {
	pool := testPool(t)
	a := addr(t, pool)
	if err := (&Store{Pool: pool}).Insert(context.Background(), Summary{Email: a, FileType: "pdf"}); err != nil {
		t.Fatal(err)
	}
	var rc int
	var ids []string
	if err := pool.QueryRow(context.Background(), `SELECT row_count, failed_rule_ids FROM readiness_checks WHERE email = $1`, a).Scan(&rc, &ids); err != nil {
		t.Fatal(err)
	}
	if rc != 0 || len(ids) != 0 {
		t.Fatalf("row_count = %d, ids = %v", rc, ids)
	}
}

// Skips without DB env, like internal/publiccheck.
func TestFieldLabel_EveryGlobalRuleTargetHasALabel(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("readinesscheck db test skipped: set DATABASE_URL (or run `make test-rls`)")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	rows, err := pool.Query(ctx, `SELECT DISTINCT target FROM rules WHERE target <> ''`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			t.Fatal(err)
		}
		n++
		if fieldLabel(target, "") == target {
			t.Errorf("rule target %q has no field label", target)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no rule targets: the migrated database has no global rules")
	}
}
