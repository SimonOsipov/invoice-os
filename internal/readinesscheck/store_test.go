package readinesscheck

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

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
