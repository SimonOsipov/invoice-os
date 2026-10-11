// nrs_code_list_syncs: invoice_app may INSERT (the syncer) and, from this migration, SELECT (the staff
// sync-log read); it never UPDATEs or DELETEs. Every write rolls back.
package db_test

import (
	"context"
	"io/fs"
	"reflect"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	syncsReadGlob = "*_nrs_code_list_syncs_read.sql"
	syncsTable    = "public.nrs_code_list_syncs"
)

func syncsPrivilege(t *testing.T, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, role, priv, when string) bool {
	t.Helper()
	var ok bool
	if err := q.QueryRow(context.Background(), `SELECT has_table_privilege($1, $2, $3)`, role, syncsTable, priv).Scan(&ok); err != nil {
		t.Fatalf("has_table_privilege(%s, %s) %s: %v", role, priv, when, err)
	}
	return ok
}

func TestRLS_CodeListSyncsReadGrant(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	for _, c := range []struct {
		role, priv string
		want       bool
	}{
		{"invoice_app", "SELECT", true},
		{"invoice_app", "INSERT", true},
		{"invoice_app", "UPDATE", false},
		{"invoice_app", "DELETE", false},
		{"invoice_app", "TRUNCATE", false},
		{"invoice_app", "REFERENCES", false},
		{"invoice_app", "TRIGGER", false},
		{"invoice_tenant_reader", "SELECT", false},
		{"invoice_tenant_reader", "INSERT", false},
	} {
		if got := syncsPrivilege(t, h.super, c.role, c.priv, "after the migration"); got != c.want {
			t.Errorf("%s holds %s on nrs_code_list_syncs = %t, want %t", c.role, c.priv, got, c.want)
		}
	}

	// Column-level UPDATE would not show in the table-level check above.
	var anyUpdate bool
	if err := h.super.QueryRow(ctx, `SELECT has_any_column_privilege('invoice_app', $1, 'UPDATE')`, syncsTable).Scan(&anyUpdate); err != nil {
		t.Fatalf("has_any_column_privilege: %v", err)
	}
	if anyUpdate {
		t.Error("invoice_app holds a column UPDATE on nrs_code_list_syncs, want none")
	}

	// The whole non-owner ACL: this migration adds the SELECT and nothing else.
	rows, err := h.super.Query(ctx, `
SELECT g.grantee::regrole::text, g.privilege_type
  FROM pg_class c, LATERAL aclexplode(c.relacl) g
 WHERE c.oid = $1::regclass AND g.grantee <> c.relowner`, syncsTable)
	if err != nil {
		t.Fatalf("read the ACL: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var who, priv string
		if err := rows.Scan(&who, &priv); err != nil {
			t.Fatal(err)
		}
		got = append(got, who+":"+priv)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if want := []string{"invoice_app:INSERT", "invoice_app:SELECT"}; !reflect.DeepEqual(got, want) {
		t.Errorf("non-owner ACL of nrs_code_list_syncs = %v, want %v", got, want)
	}

	// Its sibling keeps the grants ENGI-03 gave it.
	for _, c := range []struct {
		priv string
		want bool
	}{{"SELECT", true}, {"INSERT", true}, {"DELETE", true}, {"UPDATE", false}} {
		var ok bool
		if err := h.super.QueryRow(ctx, `SELECT has_table_privilege('invoice_app', 'public.nrs_codes', $1)`, c.priv).Scan(&ok); err != nil {
			t.Fatalf("has_table_privilege nrs_codes %s: %v", c.priv, err)
		}
		if ok != c.want {
			t.Errorf("invoice_app holds %s on nrs_codes = %t, want %t", c.priv, ok, c.want)
		}
	}
}

func TestRLS_CodeListSyncsReadDownAndUp(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	matches, err := fs.Glob(migrations.FS, syncsReadGlob)
	if err != nil || len(matches) != 1 {
		t.Fatalf("glob %s in migrations.FS = %v (err %v), want exactly one file: the migration is not shipped", syncsReadGlob, matches, err)
	}
	up := auditEntitySectionOf(t, matches[0], "Up")
	down := shippedDownStatements(t, syncsReadGlob)
	if len(down) == 0 {
		t.Fatalf("Down of %s holds no statement", matches[0])
	}

	tx := revokeProbeTx(t)
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE invoice_migrator`); err != nil {
		t.Fatalf("SET LOCAL ROLE invoice_migrator: %v", err)
	}
	if !syncsPrivilege(t, tx, "invoice_app", "SELECT", "before the Down") {
		t.Fatal("before the Down: invoice_app holds no SELECT: is the migration applied?")
	}
	for _, s := range down {
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatalf("shipped Down statement %q: %v", s, err)
		}
	}
	if syncsPrivilege(t, tx, "invoice_app", "SELECT", "after the Down") {
		t.Error("after the Down: invoice_app still holds SELECT, want it revoked")
	}
	if !syncsPrivilege(t, tx, "invoice_app", "INSERT", "after the Down") {
		t.Error("after the Down: invoice_app lost INSERT, want the syncer's grant kept")
	}
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("shipped Up as the migrator: %v", err)
	}
	if !syncsPrivilege(t, tx, "invoice_app", "SELECT", "after the Up") {
		t.Error("after the Up: invoice_app holds no SELECT")
	}
	if syncsPrivilege(t, tx, "invoice_app", "UPDATE", "after the Up") || syncsPrivilege(t, tx, "invoice_app", "DELETE", "after the Up") {
		t.Error("after the Up: invoice_app holds UPDATE or DELETE, want neither")
	}
}
