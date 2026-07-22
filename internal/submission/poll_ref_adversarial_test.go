// M5-02-05 (task-222): QA Mode B adversarial coverage for submission_jobs.poll_ref, beyond
// the four AC-derived Test Specs already GREEN in poll_ref_db_test.go. Same reuse rules as
// that file: package submission_test, requireExchangeDB / fx / exChain, no second TestMain,
// no new writer function, no testify.
//
// Five cases, one per bullet in the QA Mode B brief:
//
//  1. TestPollRef_LongRefRoundTrips        — a 10 KiB ref round-trips whole; no length bound
//     is a deliberate omission (the migration header's own argument), so this proves the
//     column does not silently truncate an authority-defined opaque token of unbounded size.
//  2. TestPollRef_NulByteRejected           — a NUL byte (0x00) is not valid Postgres `text`;
//     asserts the OBSERVED SQLSTATE rather than assuming one, and documents it as a boundary
//     an adapter must respect before ever reaching this column.
//  3. TestPollRef_MultiByteRoundTrips        — CJK, RTL (Arabic), and an emoji (with its
//     variation selector) round-trip byte-identically — encoding, not just length.
//  4. TestRLS_PollRefCrossTenantUpdateAffectsZeroRows — an UPDATE naming tenant A's job while
//     the GUC is scoped to tenant B is a silent zero-row no-op, never an error: the
//     tenant_isolation policy's USING clause doubles as the UPDATE's WITH CHECK/visibility
//     filter, so the row is invisible to the statement, not rejected by it.
//  5. TestPollRef_UpdateBumpsUpdatedAtTrigger — an UPDATE naming only poll_ref still bumps
//     updated_at, proving the new column participates in the table's existing BEFORE UPDATE
//     FOR EACH ROW trigger (submission_jobs_touch_updated_at) rather than sitting outside it.
package submission_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// TestPollRef_LongRefRoundTrips: no CHECK or length bound exists on poll_ref (the migration
// header argues this explicitly — an invented cap would constrain an authority-defined
// opaque string this repo does not control the format of). A 10 KiB ref must therefore
// round-trip whole, not silently truncate at some incidental buffer/TOAST boundary.
func TestPollRef_LongRefRoundTrips(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, _, jobID, cleanup := exChain(t, f)
	defer cleanup()

	want := strings.Repeat("poll-ref-chunk-0123456789/", 400) // > 10 KiB
	if len(want) < 10*1024 {
		t.Fatalf("fixture construction bug: built %d bytes, want >= 10 KiB", len(want))
	}

	if err := pollRefWrite(ctx, f, tenantID, jobID, &want); err != nil {
		if failPollRefUndefined(t, "UPDATE poll_ref (10 KiB ref)", err) {
			return
		}
		t.Fatalf("write a 10 KiB poll_ref: %v", err)
	}

	got, err := pollRefRead(ctx, f, tenantID, jobID)
	if err != nil {
		if failPollRefUndefined(t, "SELECT poll_ref (10 KiB ref)", err) {
			return
		}
		t.Fatalf("read back the 10 KiB poll_ref: %v", err)
	}
	if got == nil {
		t.Fatal("10 KiB poll_ref read back NULL, want the written value")
	}
	if len(*got) != len(want) {
		t.Fatalf("10 KiB poll_ref read back %d bytes, want %d — truncated somewhere between "+
			"the write and this separate read", len(*got), len(want))
	}
	if *got != want {
		t.Error("10 KiB poll_ref round-tripped the right length but different content — " +
			"corruption, not truncation")
	}
}

// TestPollRef_NulByteRejected: Postgres `text` cannot store an embedded NUL byte (0x00) —
// verified live against this worktree's DB (docker exec psql: `SELECT E'foo\x00bar'` raises
// `invalid byte sequence for encoding "UTF8"`, matching the same rationale already documented
// at internal/platform/db/app_exchange_rls_test.go:181-183 for app_exchange's body columns).
// This asserts the OBSERVED SQLSTATE through the actual driver/parameterized-query path
// (pgx), not the raw-literal path, since those are not guaranteed to fail identically. It is
// a boundary an AppAdapter must respect BEFORE ever handing a Ref to this column — no
// scrubbing happens here, and none should (out of scope for M5-02-05: no writer ships).
func TestPollRef_NulByteRejected(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, _, jobID, cleanup := exChain(t, f)
	defer cleanup()

	poisoned := "before\x00after"
	err := pollRefWrite(ctx, f, tenantID, jobID, &poisoned)
	if err == nil {
		t.Fatal("UPDATE poll_ref with an embedded NUL byte succeeded, want an error — " +
			"Postgres text cannot represent 0x00")
	}
	if failPollRefUndefined(t, "UPDATE poll_ref (embedded NUL byte)", err) {
		return
	}
	if code := exPgCode(err); code != "22021" {
		t.Errorf("UPDATE poll_ref with an embedded NUL byte: SQLSTATE = %q, want 22021 "+
			"(invalid_byte_sequence_for_encoding) — got: %v", code, err)
	}
}

// TestPollRef_MultiByteRoundTrips: CJK, Arabic (RTL, itself composed of combining
// presentation forms), and an emoji with its variation selector — three different multi-byte
// UTF-8 shapes in one ref — must all round-trip byte-for-byte across a transaction boundary.
// A naive byte-buffer bug (off-by-one truncation, a codepage assumption, stripping a
// "control-looking" byte inside a multi-byte sequence) would corrupt at least one of these
// without necessarily changing the string's rune COUNT, so length alone would not catch it —
// this compares full byte equality.
func TestPollRef_MultiByteRoundTrips(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, _, jobID, cleanup := exChain(t, f)
	defer cleanup()

	want := "受信確認番号-٠١٢٣٤٥٦٧٨٩-تأكيد-📄️-done"

	if err := pollRefWrite(ctx, f, tenantID, jobID, &want); err != nil {
		if failPollRefUndefined(t, "UPDATE poll_ref (multi-byte/RTL/emoji ref)", err) {
			return
		}
		t.Fatalf("write a multi-byte poll_ref: %v", err)
	}

	got, err := pollRefRead(ctx, f, tenantID, jobID)
	if err != nil {
		if failPollRefUndefined(t, "SELECT poll_ref (multi-byte/RTL/emoji ref)", err) {
			return
		}
		t.Fatalf("read back the multi-byte poll_ref: %v", err)
	}
	if got == nil {
		t.Fatal("multi-byte poll_ref read back NULL, want the written value")
	}
	if *got != want {
		t.Errorf("multi-byte poll_ref read back = %q (%d bytes), want %q (%d bytes) — "+
			"byte-for-byte mismatch across a transaction boundary",
			*got, len(*got), want, len(want))
	}
}

// TestRLS_PollRefCrossTenantUpdateAffectsZeroRows: an UPDATE naming tenant A's job id while
// the connection's GUC is scoped to tenant B is a SILENT zero-row no-op — the row is not
// visible to the statement at all under tenant_isolation's USING clause, so this is neither
// a permission error nor a write that somehow lands. Asserts the row count explicitly
// (CommandTag.RowsAffected()), following the exact idiom already used for the same shape at
// internal/platform/db/submission_jobs_rls_test.go's TestRLS_SubmissionJobsUpdatedAtBumpedByTrigger
// (ct.RowsAffected() on the UPDATE's own CommandTag). Then confirms, back under tenant A's own
// context, that poll_ref was NOT changed by the no-op attempt.
func TestRLS_PollRefCrossTenantUpdateAffectsZeroRows(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()

	tenantA, _, jobA, cleanupA := exChain(t, f)
	defer cleanupA()
	tenantB, _, _, cleanupB := exChain(t, f)
	defer cleanupB()

	attempted := "SHOULD-NEVER-LAND"
	err := db.WithinTenantTx(ctx, f.app, tenantB, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE submission_jobs SET poll_ref = $1 WHERE id = $2`, attempted, jobA)
		if e != nil {
			return e
		}
		if n := ct.RowsAffected(); n != 0 {
			t.Errorf("UPDATE poll_ref on tenant A's job under tenant B's context affected %d "+
				"rows, want 0 — tenant_isolation should make the row invisible to this "+
				"statement entirely", n)
		}
		return nil
	})
	if err != nil {
		if failPollRefUndefined(t, "UPDATE poll_ref (cross-tenant, expect zero-row no-op)", err) {
			return
		}
		t.Fatalf("cross-tenant UPDATE attempt errored (want a silent zero-row no-op, not an error): %v", err)
	}

	// Confirm the no-op attempt truly changed nothing, reading back under tenant A's OWN
	// context (not B's, which would just repeat the zero-row RLS filter).
	got, err := pollRefRead(ctx, f, tenantA, jobA)
	if err != nil {
		if failPollRefUndefined(t, "SELECT poll_ref (tenant A, after the cross-tenant no-op)", err) {
			return
		}
		t.Fatalf("read tenant A's poll_ref after the cross-tenant no-op attempt: %v", err)
	}
	if got != nil {
		t.Errorf("tenant A's poll_ref after a cross-tenant no-op UPDATE = %q, want NULL "+
			"(unchanged) — the no-op must not have partially applied", *got)
	}
}

// TestPollRef_UpdateBumpsUpdatedAtTrigger: submission_jobs_set_updated_at is a BEFORE UPDATE
// FOR EACH ROW trigger with no column list — it fires on ANY UPDATE to the row, poll_ref
// included. An UPDATE naming only poll_ref (not updated_at itself, and not any other column)
// must still bump updated_at, proving the new column participates in the table's existing
// machinery rather than being invisible to it. Mirrors the idiom at
// internal/platform/db/submission_jobs_rls_test.go's TestRLS_SubmissionJobsUpdatedAtBumpedByTrigger
// (deliberately not naming updated_at; a separate transaction's now() is guaranteed later
// than the seeding transaction's, so no sleep is needed).
func TestPollRef_UpdateBumpsUpdatedAtTrigger(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, _, jobID, cleanup := exChain(t, f)
	defer cleanup()

	var before time.Time
	if err := db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT updated_at FROM submission_jobs WHERE id = $1`, jobID).Scan(&before)
	}); err != nil {
		if failPollRefUndefined(t, "SELECT updated_at (before)", err) {
			return
		}
		t.Fatalf("read updated_at before the poll_ref UPDATE: %v", err)
	}

	ref := "trigger-participation-probe"
	if err := pollRefWrite(ctx, f, tenantID, jobID, &ref); err != nil {
		if failPollRefUndefined(t, "UPDATE poll_ref (trigger participation)", err) {
			return
		}
		t.Fatalf("UPDATE poll_ref (not naming updated_at): %v", err)
	}

	var after time.Time
	if err := db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT updated_at FROM submission_jobs WHERE id = $1`, jobID).Scan(&after)
	}); err != nil {
		if failPollRefUndefined(t, "SELECT updated_at (after)", err) {
			return
		}
		t.Fatalf("read updated_at after the poll_ref UPDATE: %v", err)
	}

	if !after.After(before) {
		t.Errorf("updated_at after an UPDATE that only names poll_ref = %s, want strictly "+
			"later than %s — poll_ref would be sitting outside the table's BEFORE UPDATE "+
			"trigger", after, before)
	}
}
