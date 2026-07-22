// M5-01-02 (task-212): tests for the fiscal-outcome columns on `invoices` plus the
// tenant-scoped uniqueness target the M5-01-03 composite FK needs. Written BEFORE the
// migration exists (RED against SQLSTATE 42703 undefined_column, except INV-FISC-07 —
// see below). The ALTER the Executor will add:
//
//	ALTER TABLE invoices
//	    ADD COLUMN irn               text  CHECK (irn        IS NULL OR char_length(irn)        > 0),
//	    ADD COLUMN csid              text  CHECK (csid       IS NULL OR char_length(csid)       > 0),
//	    ADD COLUMN qr_payload        text  CHECK (qr_payload IS NULL OR char_length(qr_payload) > 0),
//	    ADD COLUMN rejection_reasons jsonb NOT NULL DEFAULT '[]';
//	ALTER TABLE invoices
//	    ADD CONSTRAINT invoices_tenant_id_id_uq UNIQUE (tenant_id, id);
//
// Four things about this change shape the cases below:
//
//   - `rejection_reasons` elements are `{code, message, path}` — deliberately NOT
//     `rule_key` / `severity`. It parallels invoice.Violation (internal/invoice/validator.go:77-82,
//     json tags rule_key/severity/message/path) on `path` ALONE, the one field the
//     fix-and-resubmit loop mechanically consumes. `code` is the authority's own error
//     taxonomy and is kept verbatim rather than mapped onto our rule-key vocabulary;
//     `severity` is meaningless because an APP rejection is blocking by definition.
//     INV-FISC-05 therefore asserts key-for-key preservation, not merely that the array
//     round-trips.
//   - There is NO shape CHECK on `rejection_reasons`, mirroring `invoices.violations`
//     (migrations/20260714103137_invoices.sql:61), which carries none either. The shape
//     above is a convention the writer honours, so the only DB-level guarantees worth
//     asserting are NOT NULL, the '[]' default, and that jsonb does not mangle keys.
//   - The new columns get NO policy of their own: the M4-01 `tenant_isolation` policy is
//     row-scoped, so it already covers every column added later. INV-FISC-06 pins that —
//     it is the test that would go red if someone "helpfully" added a second policy or a
//     column-level grant during the ALTER.
//   - The uniqueness target MUST be `ADD CONSTRAINT ... UNIQUE`, not `CREATE UNIQUE INDEX`.
//     A bare unique index produces no pg_constraint row and so cannot be referenced by a
//     composite FK. This table already demonstrates the difference: its 3-column guard
//     `invoices_tenant_entity_number_uq` is an index-only unique, and `invoices` has zero
//     contype='u' rows today. Every pg_constraint query here filters contype explicitly —
//     PG18 also stores NOT NULL constraints there (contype='n'), of which this table has
//     seven, so an unfiltered lookup would assert almost nothing.
//
// Spec-to-test map (Test Specs table, task-212):
//
//	INV-FISC-01 TestRLS_InvoicesFiscalColumnDefaults
//	INV-FISC-02 TestRLS_InvoicesFiscalIdentifiersRoundTrip
//	INV-FISC-03 TestRLS_InvoicesFiscalEmptyIdentifierRejected
//	INV-FISC-04 TestRLS_InvoicesRejectionReasonsNotNull
//	INV-FISC-05 TestRLS_InvoicesRejectionReasonsShapePreserved
//	INV-FISC-06 TestRLS_InvoicesFiscalColumnsRespectTenantIsolation
//	INV-FISC-07 TestRLS_InvoicesTenantIdIdUniqueConstraintExists
//
// INV-FISC-01..06 go RED with 42703 undefined_column. INV-FISC-07 does NOT: seedInvoice
// (invoices_rls_test.go:83-92) names only (id, tenant_id, entity_id, invoice_number) and is
// fully column-agnostic, so nothing in that case ever touches a new column — it goes RED as
// a plain "constraint not found" assertion failure instead.
//
// Rows are seeded per-test (seedInvoice / seedBusinessEntity), never in the shared
// harness.seed(). `invoices` does cascade from `tenants`, so the harness teardown would
// eventually reap these rows — every case still deletes its own explicitly, and registers
// that cleanup BEFORE the assertion that could t.Fatalf, so the failure path leaks nothing
// into sibling cases.
//
// Named with the TestRLS_ prefix so the CI `rls` job's `-run TestRLS
// ./internal/platform/db/...` (.github/workflows/ci.yml) and `make test-rls` both pick these
// up with no workflow edit. Every case calls requireHarness(t), which SKIPS when the
// per-role DATABASE_* URLs are unset so a bare `go test ./...` stays green with no DB — note
// that under the CI gate (scripts/ci/rls-test-gate.sh) a SKIP is itself a failure, so no case
// here may add a t.Skip of its own.
//
// Run: `make test-rls`, or directly with the same four DSNs, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_MIGRATION_URL="postgres://invoice_migrator:migrator@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_READER_URL="postgres://invoice_tenant_reader:reader@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -run TestRLS_Invoices ./internal/platform/db/...
//
// (A worktree running the compose DB on an alternate host port must substitute it in all
// four DSNs — e.g. `DEV_DB_PORT=5433 make test-rls`, since Makefile:32 defaults to 5432.)
package db_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// failIfUndefinedColumn turns the pre-migration failure mode into an explicit,
// self-explaining message instead of a raw driver error, following the tenants_kind_test.go
// precedent (:63-66). Returns true when it fired, so callers that must not proceed can stop.
func failIfUndefinedColumn(t *testing.T, what string, err error) bool {
	t.Helper()
	if pgCode(err) == "42703" {
		t.Fatalf("%s: undefined_column (42703) — the invoices fiscal-outcome migration "+
			"(irn/csid/qr_payload/rejection_reasons) is not applied yet: %v", what, err)
		return true
	}
	return false
}

// INV-FISC-01: a fresh invoice — inserted naming ONLY pre-existing columns, via the
// column-agnostic seedInvoice — reads back irn/csid/qr_payload NULL and rejection_reasons
// '[]'. This proves the three identifier columns are genuinely nullable (an authority has
// returned nothing yet for a draft invoice) and that the rejection_reasons DEFAULT is
// load-bearing rather than merely present in the DDL. Read as invoice_app inside a tenant
// tx on purpose: that is also the evidence for AC #6 — the table-level GRANT from
// 20260714103137_invoices.sql:95 already covers columns added later, so the migration adds
// no grant of its own.
func TestRLS_InvoicesFiscalColumnDefaults(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "INV-FISC-01 A Corp")
	defer cleanupEntityA()
	id, cleanupInvoice := seedInvoice(t, h.tenantA, entityA, "INV-FISC-01-A")
	defer cleanupInvoice()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var (
			irn              *string
			csid             *string
			qrPayload        *string
			rejectionReasons string
		)
		if e := tx.QueryRow(ctx,
			`SELECT irn, csid, qr_payload, rejection_reasons::text FROM invoices WHERE id = $1`, id,
		).Scan(&irn, &csid, &qrPayload, &rejectionReasons); e != nil {
			return e
		}
		if irn != nil {
			t.Errorf("irn on a fresh invoice = %q, want NULL (nothing returned by the authority yet)", *irn)
		}
		if csid != nil {
			t.Errorf("csid on a fresh invoice = %q, want NULL", *csid)
		}
		if qrPayload != nil {
			t.Errorf("qr_payload on a fresh invoice = %q, want NULL", *qrPayload)
		}
		if rejectionReasons != "[]" {
			t.Errorf("rejection_reasons default = %q, want %q (DEFAULT '[]' not load-bearing)",
				rejectionReasons, "[]")
		}
		return nil
	})
	if err != nil {
		if failIfUndefinedColumn(t, "SELECT the fiscal-outcome columns", err) {
			return
		}
		t.Fatalf("read back fiscal-outcome defaults: %v", err)
	}
}

// INV-FISC-02: the acceptance path. An invoice_app UPDATE writing all three identifiers at
// once affects exactly one row and reads them back byte-identical — no truncation, no
// trimming, no case folding. The values below are deliberately awkward: an IRN with the
// hyphenated FIRS shape, a CSID that is base64 with '+', '/' and '=' padding, and a QR
// payload long enough that a text->varchar(n) slip would truncate it.
func TestRLS_InvoicesFiscalIdentifiersRoundTrip(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	const (
		wantIRN  = "INV0001-94ND90NR-20260722"
		wantCSID = "aG9zdC1zaWduZWQ+Y3NpZC92YWx1ZQ=="
		wantQR   = "eyJpcm4iOiJJTlYwMDAxLTk0TkQ5ME5SLTIwMjYwNzIyIiwiaXNzdWVyIjoiRklSUyIsInNpZyI6ImJhc2U2NC1ibG9iIn0="
	)

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "INV-FISC-02 A Corp")
	defer cleanupEntityA()
	id, cleanupInvoice := seedInvoice(t, h.tenantA, entityA, "INV-FISC-02-A")
	defer cleanupInvoice()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx,
			`UPDATE invoices SET irn = $1, csid = $2, qr_payload = $3 WHERE id = $4`,
			wantIRN, wantCSID, wantQR, id)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 1 {
			t.Errorf("UPDATE of the fiscal identifiers affected %d rows, want 1", ct.RowsAffected())
		}
		return nil
	})
	if err != nil {
		if failIfUndefinedColumn(t, "UPDATE the fiscal identifiers", err) {
			return
		}
		t.Fatalf("write the fiscal identifiers as invoice_app: %v", err)
	}

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var irn, csid, qrPayload string
		if e := tx.QueryRow(ctx,
			`SELECT irn, csid, qr_payload FROM invoices WHERE id = $1`, id,
		).Scan(&irn, &csid, &qrPayload); e != nil {
			return e
		}
		for _, c := range []struct{ name, got, want string }{
			{"irn", irn, wantIRN},
			{"csid", csid, wantCSID},
			{"qr_payload", qrPayload, wantQR},
		} {
			if c.got != c.want {
				t.Errorf("%s read back = %q, want %q (byte-identical)", c.name, c.got, c.want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read back the fiscal identifiers: %v", err)
	}
}

// INV-FISC-03: an adapter that writes the empty string where it meant "nothing" is rejected
// by the IS NULL OR char_length(x) > 0 CHECK on each identifier, SQLSTATE 23514. The
// distinction matters downstream: NULL means "the authority returned no IRN", an empty
// string would mean "the authority returned an empty IRN", and only the first is a real state. Each attempt runs
// in its own tx (a rejected statement poisons the surrounding one), and the refusal is
// mutation-verified afterwards — the column is still NULL, so the empty string was not
// silently coerced instead of rejected.
func TestRLS_InvoicesFiscalEmptyIdentifierRejected(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "INV-FISC-03 A Corp")
	defer cleanupEntityA()
	id, cleanupInvoice := seedInvoice(t, h.tenantA, entityA, "INV-FISC-03-A")
	defer cleanupInvoice()

	for _, col := range []string{"irn", "csid", "qr_payload"} {
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			// #nosec G202 -- col is a literal from the loop above, never external input.
			_, e := tx.Exec(ctx, `UPDATE invoices SET `+col+` = '' WHERE id = $1`, id)
			return e
		})
		if err == nil {
			t.Errorf("UPDATE %s = '' succeeded, want CHECK violation (SQLSTATE 23514) — an empty "+
				"identifier is not the same state as no identifier", col)
			continue
		}
		if failIfUndefinedColumn(t, "UPDATE "+col+" = ''", err) {
			return
		}
		if code := pgCode(err); code != "23514" {
			t.Errorf("UPDATE %s = '': SQLSTATE = %q, want 23514 (check_violation): %v", col, code, err)
		}
	}

	// Mutation-verify: all three are still NULL. Asked as the superuser, which sees past
	// RLS entirely, so a row written under some other tenant context would still show up.
	var irn, csid, qrPayload *string
	if err := h.super.QueryRow(ctx,
		`SELECT irn, csid, qr_payload FROM invoices WHERE id = $1`, id,
	).Scan(&irn, &csid, &qrPayload); err != nil {
		t.Fatalf("mutation-verify the refused empty identifiers: %v", err)
	}
	for _, c := range []struct {
		name string
		got  *string
	}{{"irn", irn}, {"csid", csid}, {"qr_payload", qrPayload}} {
		if c.got != nil {
			t.Errorf("%s after the refused empty write = %q, want NULL (the '' was coerced, not rejected)",
				c.name, *c.got)
		}
	}
}

// INV-FISC-04: rejection_reasons is NOT NULL, so an explicit NULL is refused with 23502.
// "No reasons" is the empty array, never NULL — a nullable column would force every reader
// (and the M5-09 SPA) to distinguish two encodings of the same fact. This mirrors
// invoices.violations, which is NOT NULL DEFAULT '[]' for the same reason.
func TestRLS_InvoicesRejectionReasonsNotNull(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "INV-FISC-04 A Corp")
	defer cleanupEntityA()
	id, cleanupInvoice := seedInvoice(t, h.tenantA, entityA, "INV-FISC-04-A")
	defer cleanupInvoice()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE invoices SET rejection_reasons = NULL WHERE id = $1`, id)
		return e
	})
	if err == nil {
		t.Fatal("UPDATE rejection_reasons = NULL succeeded, want not_null_violation (SQLSTATE 23502) — " +
			"\"no reasons\" must be the empty array, not NULL")
	}
	if failIfUndefinedColumn(t, "UPDATE rejection_reasons = NULL", err) {
		return
	}
	if code := pgCode(err); code != "23502" {
		t.Fatalf("UPDATE rejection_reasons = NULL: SQLSTATE = %q, want 23502 (not_null_violation): %v", code, err)
	}

	// Mutation-verify: still the '[]' default, not silently emptied to NULL.
	var reasons string
	if e := h.super.QueryRow(ctx,
		`SELECT rejection_reasons::text FROM invoices WHERE id = $1`, id,
	).Scan(&reasons); e != nil {
		t.Fatalf("mutation-verify rejection_reasons after the refused NULL: %v", e)
	}
	if reasons != "[]" {
		t.Errorf("rejection_reasons after the refused NULL = %q, want %q", reasons, "[]")
	}
}

// INV-FISC-05: a rejection payload round-trips key-for-key. The column carries no shape
// CHECK (mirroring violations), so what this pins is that jsonb storage preserves the
// authority's own vocabulary exactly: `code` is NOT rewritten to `rule_key`, no `severity`
// is invented, `path` survives, and no element gains or loses a key. The two elements
// differ in `path` presence, because `path` is the one field the fix-and-resubmit loop
// mechanically consumes and an authority may omit it for a whole-document error — dropping
// a key-less element, or backfilling `path: ""`, are both regressions this catches.
func TestRLS_InvoicesRejectionReasonsShapePreserved(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	const payload = `[{"code":"APP-ERR-0417","message":"Supplier TIN not registered","path":"supplier_tin"},` +
		`{"code":"APP-ERR-0902","message":"Invoice rejected by the authority"}]`

	want := []map[string]string{
		{"code": "APP-ERR-0417", "message": "Supplier TIN not registered", "path": "supplier_tin"},
		{"code": "APP-ERR-0902", "message": "Invoice rejected by the authority"},
	}

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "INV-FISC-05 A Corp")
	defer cleanupEntityA()
	id, cleanupInvoice := seedInvoice(t, h.tenantA, entityA, "INV-FISC-05-A")
	defer cleanupInvoice()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE invoices SET rejection_reasons = $1::jsonb WHERE id = $2`, payload, id)
		return e
	})
	if err != nil {
		if failIfUndefinedColumn(t, "UPDATE rejection_reasons", err) {
			return
		}
		t.Fatalf("write rejection_reasons as invoice_app: %v", err)
	}

	var raw string
	if e := h.super.QueryRow(ctx,
		`SELECT rejection_reasons::text FROM invoices WHERE id = $1`, id,
	).Scan(&raw); e != nil {
		t.Fatalf("read back rejection_reasons: %v", e)
	}

	var got []map[string]any
	if e := json.Unmarshal([]byte(raw), &got); e != nil {
		t.Fatalf("rejection_reasons read back as %q, which is not a JSON array of objects: %v", raw, e)
	}
	// Guard against a vacuous pass: an empty (or truncated) array would satisfy every
	// per-element assertion below without asserting anything at all.
	if len(got) != len(want) {
		t.Fatalf("rejection_reasons round-tripped %d elements, want %d (raw: %s)", len(got), len(want), raw)
	}

	for i, wantElem := range want {
		gotElem := got[i]
		if len(gotElem) != len(wantElem) {
			t.Errorf("element %d has %d keys %v, want %d keys %v — a key was added or dropped",
				i, len(gotElem), keysOf(gotElem), len(wantElem), keysOf2(wantElem))
		}
		for k, v := range wantElem {
			gv, ok := gotElem[k]
			if !ok {
				t.Errorf("element %d is missing key %q (raw: %s)", i, k, raw)
				continue
			}
			if gs, isStr := gv.(string); !isStr || gs != v {
				t.Errorf("element %d key %q = %#v, want %q", i, k, gv, v)
			}
		}
		// The two keys the PM decision deliberately did NOT adopt. Their appearance would
		// mean someone mapped the authority's error taxonomy onto our internal rule-key
		// vocabulary — the exact translation that was rejected as unverifiable.
		for _, forbidden := range []string{"rule_key", "severity"} {
			if _, present := gotElem[forbidden]; present {
				t.Errorf("element %d gained key %q — rejection_reasons is {code,message,path}, "+
					"deliberately NOT invoice.Violation's shape (raw: %s)", i, forbidden, raw)
			}
		}
	}
}

// keysOf / keysOf2 exist only to make the "key added or dropped" failure message name the
// keys instead of just counting them.
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func keysOf2(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// INV-FISC-06: the new columns need no policy of their own. The M4-01 `tenant_isolation`
// policy on invoices is row-scoped, so a tenant-A tx trying to write tenant B's irn matches
// zero rows and raises no error — B's row is simply invisible, exactly as INV-RLS-03 proves
// for `status`. The positive half (A writing A's own row) is asserted first: without it, a
// migration that broke every write would satisfy the zero-rows half vacuously.
func TestRLS_InvoicesFiscalColumnsRespectTenantIsolation(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "INV-FISC-06 A Corp")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "INV-FISC-06 B Corp")
	defer cleanupEntityB()
	idA, cleanupA := seedInvoice(t, h.tenantA, entityA, "INV-FISC-06-A")
	defer cleanupA()
	idB, cleanupB := seedInvoice(t, h.tenantB, entityB, "INV-FISC-06-B")
	defer cleanupB()

	// Positive half: A writing A's own irn affects exactly one row.
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE invoices SET irn = 'INV-FISC-06-OWN' WHERE id = $1`, idA)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 1 {
			t.Errorf("own-tenant irn UPDATE affected %d rows, want 1", ct.RowsAffected())
		}
		return nil
	})
	if err != nil {
		if failIfUndefinedColumn(t, "own-tenant UPDATE of irn", err) {
			return
		}
		t.Fatalf("own-tenant UPDATE of irn: %v", err)
	}

	// Negative half: the same context writing B's irn matches nothing and raises nothing.
	// Not an error — a cross-tenant UPDATE is filtered, not refused (the refusal path,
	// 42501, is for a WITH CHECK violation, which INV-RLS-07 covers).
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE invoices SET irn = 'INV-FISC-06-ROGUE' WHERE tenant_id = $1`, h.tenantB)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 0 {
			t.Errorf("cross-tenant irn UPDATE affected %d rows, want 0", ct.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("cross-tenant UPDATE of irn (expected 0 rows, no error): %v", err)
	}

	// Mutation-verify as the superuser, which sees past RLS: B's irn is untouched.
	var irnB *string
	if e := h.super.QueryRow(ctx, `SELECT irn FROM invoices WHERE id = $1`, idB).Scan(&irnB); e != nil {
		t.Fatalf("mutation-verify tenant B's irn: %v", e)
	}
	if irnB != nil {
		t.Errorf("tenant B's irn after A's cross-tenant UPDATE = %q, want NULL", *irnB)
	}
}

// INV-FISC-07: invoices carries a UNIQUE CONSTRAINT named invoices_tenant_id_id_uq over
// exactly (tenant_id, id), in that order. This is the whole point of the subtask for
// M5-01-03 and M5-01-04: a composite FK can only reference a pg_constraint-backed unique,
// so a bare CREATE UNIQUE INDEX — which is how this table's existing 3-column guard
// invoices_tenant_entity_number_uq is declared — would look identical in \d and still be
// unusable as an FK target. contype='u' is filtered explicitly, both to exclude that
// index-only unique and because PG18 records this table's seven NOT NULL constraints in
// pg_constraint as contype='n'.
//
// This case does NOT go RED with 42703 like its six siblings: seedInvoice is column-agnostic
// and nothing here reads a new column, so pre-migration it fails as a plain assertion
// ("constraint not found").
func TestRLS_InvoicesTenantIdIdUniqueConstraintExists(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	n, err := scanCount(ctx, h.super,
		`SELECT count(*) FROM pg_constraint
		  WHERE conrelid = 'public.invoices'::regclass
		    AND contype = 'u' AND conname = 'invoices_tenant_id_id_uq'`)
	if err != nil {
		t.Fatalf("query pg_constraint for invoices_tenant_id_id_uq: %v", err)
	}
	if n != 1 {
		t.Fatalf("UNIQUE constraints on invoices named invoices_tenant_id_id_uq = %d, want 1 — "+
			"constraint not found; the migration is not applied yet, or it declared a bare "+
			"CREATE UNIQUE INDEX (no pg_constraint row, unusable as a composite-FK target)", n)
	}

	rows, err := h.super.Query(ctx,
		`SELECT a.attname
		   FROM pg_constraint c
		   CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
		   JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		  WHERE c.conrelid = 'public.invoices'::regclass
		    AND c.contype = 'u' AND c.conname = 'invoices_tenant_id_id_uq'
		  ORDER BY k.ord`)
	if err != nil {
		t.Fatalf("query the constraint's columns: %v", err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var col string
		if e := rows.Scan(&col); e != nil {
			t.Fatalf("scan constraint column: %v", e)
		}
		cols = append(cols, col)
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate constraint columns: %v", e)
	}

	want := []string{"tenant_id", "id"}
	if len(cols) != len(want) {
		t.Fatalf("invoices_tenant_id_id_uq columns = %v, want %v", cols, want)
	}
	for i := range want {
		if cols[i] != want[i] {
			t.Errorf("invoices_tenant_id_id_uq column %d = %q, want %q (order is load-bearing: "+
				"the referencing composite FK must name (tenant_id, invoice_id) in this order)",
				i+1, cols[i], want[i])
		}
	}
}
