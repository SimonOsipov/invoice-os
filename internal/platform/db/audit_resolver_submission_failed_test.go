// Adversarial cover for the submission.failed attribution rule: the payload shapes the
// resolver must refuse, and the tenant boundary it must not cross.
package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// auditSFTx begins an invoice_app transaction with one tenant's context set. invoice_app
// is the NOBYPASSRLS runtime identity, so the resolver's invoice lookup runs under RLS.
// The transaction rolls back, so no audit row survives.
func auditSFTx(t *testing.T, ctx context.Context, tenant string) pgx.Tx {
	t.Helper()
	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin invoice_app transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenant); err != nil {
		t.Fatalf("set tenant context: %v", err)
	}
	return tx
}

// auditSFResolve calls the resolver directly and returns nil for SQL NULL.
func auditSFResolve(t *testing.T, ctx context.Context, tx pgx.Tx, event, payload string) *string {
	t.Helper()
	var got *string
	if err := tx.QueryRow(ctx,
		`SELECT audit_log_entity_for($1, $2::jsonb)::text`, event, payload).Scan(&got); err != nil {
		t.Fatalf("resolve %s with payload %s: %v", event, payload, err)
	}
	return got
}

// The resolver runs inside a BEFORE INSERT trigger, so it has to be total: a payload it
// cannot attribute must come back NULL. A raise here would reject the audit write itself.
func TestRLS_AuditResolverSubmissionFailedIsTotalOverBadPayloads(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	f := seedAuditEntityFixture(t)
	tx := auditSFTx(t, ctx, f.tenant)

	cases := []struct {
		what    string
		payload string
		want    *string
	}{
		// uuid_in's grammar, not canonical form: callers echo the raw URL segment.
		{"canonical id", auditPayloadJSON("invoice_id", f.invoice), &f.entity},
		{"uppercase id", auditPayloadJSON("invoice_id", strings.ToUpper(f.invoice)), &f.entity},
		{"brace-wrapped id", auditPayloadJSON("invoice_id", "{"+f.invoice+"}"), &f.entity},
		{"hyphenless id", auditPayloadJSON("invoice_id", strings.ReplaceAll(f.invoice, "-", "")), &f.entity},
		// Dispatch is on the event name, so rule B reads invoice_id and nothing else.
		{"bare id spelling", auditPayloadJSON("id", f.invoice), nil},
		{"key absent", `{}`, nil},
		{"json null", `{"invoice_id": null}`, nil},
		{"empty string", auditPayloadJSON("invoice_id", ""), nil},
		{"non-uuid text", auditPayloadJSON("invoice_id", "not-a-uuid"), nil},
		{"number", `{"invoice_id": 42}`, nil},
		{"31 hex digits", auditPayloadJSON("invoice_id", strings.Repeat("a", 31)), nil},
		{"33 hex digits", auditPayloadJSON("invoice_id", strings.Repeat("a", 33)), nil},
		{"unbalanced brace", auditPayloadJSON("invoice_id", "{"+f.invoice), nil},
		{"well-formed, no such invoice", auditPayloadJSON("invoice_id", uuid.NewString()), nil},
		{"payload is an array", `[]`, nil},
		{"payload is a string", `"submission failed"`, nil},
	}

	for _, tc := range cases {
		got := auditSFResolve(t, ctx, tx, "submission.failed", tc.payload)
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("%s: resolved %s, want NULL", tc.what, *got)
		case tc.want != nil && got == nil:
			t.Errorf("%s: resolved NULL, want %s", tc.what, *tc.want)
		case tc.want != nil && got != nil && *got != *tc.want:
			t.Errorf("%s: resolved %s, want %s", tc.what, *got, *tc.want)
		}
	}
}

// entity_id attributes the row to a company, so the resolver must not reach another
// tenant's invoice. RLS is the whole guard: the resolver has no tenant predicate.
func TestRLS_AuditResolverSubmissionFailedCannotCrossTenants(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	a := seedAuditEntityFixture(t)
	b := seedAuditEntityFixture(t)
	foreign := auditPayloadJSON("invoice_id", b.invoice)

	txA := auditSFTx(t, ctx, a.tenant)
	if got := auditSFResolve(t, ctx, txA, "submission.failed", foreign); got != nil {
		t.Errorf("A resolved B's invoice to %s, want NULL", *got)
	}
	// Positive control: a resolver that always returns NULL would pass the line above.
	switch got := auditSFResolve(t, ctx, txA, "submission.failed", auditPayloadJSON("invoice_id", a.invoice)); {
	case got == nil:
		t.Errorf("A resolved its own invoice to NULL, want %s", a.entity)
	case *got != a.entity:
		t.Errorf("A resolved its own invoice to %s, want %s", *got, a.entity)
	}

	// B resolving the very argument A saw nothing for proves the NULL was isolation and
	// not a dead lookup.
	txB := auditSFTx(t, ctx, b.tenant)
	switch got := auditSFResolve(t, ctx, txB, "submission.failed", foreign); {
	case got == nil:
		t.Errorf("B resolved its own invoice to NULL, want %s", b.entity)
	case *got != b.entity:
		t.Errorf("B resolved its own invoice to %s, want %s", *got, b.entity)
	}
}

// The written row, not just the function: a submission.failed row naming another tenant's
// invoice lands with a NULL entity_id, and the write still succeeds.
func TestRLS_AuditSubmissionFailedRowCannotBeAttributedToAnotherTenant(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	a := seedAuditEntityFixture(t)
	b := seedAuditEntityFixture(t)

	tx := auditSFTx(t, ctx, a.tenant)
	insert := func(actor, payload string) {
		t.Helper()
		if _, err := tx.Exec(ctx,
			`INSERT INTO audit_log (actor, event, payload) VALUES ($1, 'submission.failed', $2::jsonb)`,
			actor, payload); err != nil {
			t.Fatalf("insert %s row: %v", actor, err)
		}
	}
	insert("foreign", auditPayloadJSON("invoice_id", b.invoice))
	insert("own", auditPayloadJSON("invoice_id", a.invoice))

	got := map[string]*string{}
	rows, err := tx.Query(ctx,
		`SELECT actor, entity_id::text FROM audit_log WHERE event = 'submission.failed' AND actor IN ('foreign', 'own')`)
	if err != nil {
		t.Fatalf("read back the rows: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var actor string
		var entity *string
		if err := rows.Scan(&actor, &entity); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[actor] = entity
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("read back %d rows, want 2 — the inserts did not land", len(got))
	}
	if e := got["foreign"]; e != nil {
		t.Errorf("the row naming B's invoice was attributed to %s, want NULL", *e)
	}
	switch e := got["own"]; {
	case e == nil:
		t.Errorf("the row naming A's own invoice was attributed to NULL, want %s", a.entity)
	case *e != a.entity:
		t.Errorf("the row naming A's own invoice was attributed to %s, want %s", *e, a.entity)
	}
}
