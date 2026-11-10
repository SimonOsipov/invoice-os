package aicalls

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
	"github.com/SimonOsipov/invoice-os/internal/platform/airecord"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

func TestRecorder_ARealFakeClientCallLandsOneRowUnderItsTenant(t *testing.T) {
	e := newEnv(t)
	ids := e.tenants(t, 2)
	t1, t2 := ids[0], ids[1]
	t.Setenv(ai.EnvFake, "true")
	t.Setenv(jev.EnvFake, "true")
	t.Setenv(ai.EnvKey, "")
	t.Setenv(jev.EnvKey, "")
	logger, buf := newLogger()
	aiClient, err := ai.FromEnv(logger)
	if err != nil {
		t.Fatalf("ai.FromEnv: %v", err)
	}
	jevClient, err := jev.FromEnv(logger)
	if err != nil {
		t.Fatalf("jev.FromEnv: %v", err)
	}
	rec := NewRecorder(e.app, logger)
	aiClient.SetRecorder(rec.Record)
	jevClient.SetRecorder(rec.Record)

	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: "u1", TenantID: t1})
	if _, err := aiClient.Call(ctx, ai.Request{Purpose: ai.PurposeDocument, Text: "Invoice Number: INV-1", SchemaName: "chain", Schema: json.RawMessage(`{"type":"object"}`)}); err != nil {
		t.Fatalf("ai Call: %v", err)
	}
	if _, err := jevClient.Ask(ctx, jev.Request{Purpose: jev.PurposeMappingCheck, State: "Row 1: a", Questions: map[string]jev.Question{"invoice_number": {Type: jev.TypeNoul, Instructions: "Does the value fit?"}}}); err != nil {
		t.Fatalf("jev Ask: %v", err)
	}

	want := map[string]string{"document": "ai", "mapping_check": "jev"}
	for purpose, client := range want {
		var got []aiRow
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if got = e.rows(t, t1, purpose); len(got) > 0 {
				break
			}
		}
		if len(got) != 1 {
			t.Fatalf("T1 sees %d %s rows within 2s, want 1 (log: %v)", len(got), purpose, buf.lines(t))
		}
		if r := got[0]; r.Client != client || r.Outcome != "fake" || r.Attempts != 1 || r.TenantID != t1 {
			t.Errorf("%s row = %+v, want client %s, outcome fake, attempts 1, tenant %s", purpose, r, client, t1)
		}
		if n := len(e.rows(t, t2, purpose)); n != 0 {
			t.Errorf("T2 sees %d %s rows, want 0", n, purpose)
		}
	}
}

func TestRecorder_APublicCheckFakeClientCallShowsApartInStaffTotals(t *testing.T) {
	e := newEnv(t)
	tenant := e.namedTenant(t, "Chain tenant")
	t.Setenv(ai.EnvFake, "true")
	t.Setenv(ai.EnvKey, "")
	logger, buf := newLogger()
	aiClient, err := ai.FromEnv(logger)
	if err != nil {
		t.Fatalf("ai.FromEnv: %v", err)
	}
	rec := NewRecorder(e.app, logger)
	aiClient.SetRecorder(rec.Record)
	req := ai.Request{Purpose: ai.PurposeDocument, Text: "Invoice Number: INV-1", SchemaName: "chain", Schema: json.RawMessage(`{"type":"object"}`)}
	start := e.dbNow(t)

	tenantCtx := auth.WithIdentity(context.Background(), auth.Identity{Subject: "u1", TenantID: tenant})
	if _, err := aiClient.Call(tenantCtx, req); err != nil {
		t.Fatalf("tenant ai Call: %v", err)
	}
	if _, err := aiClient.Call(airecord.WithPublicCheck(context.Background()), req); err != nil {
		t.Fatalf("public ai Call: %v", err)
	}
	rec.Wait()

	// Move the rows to a far day so the totals read sees no other row; the NULL row is deleted by id.
	d := farDay()
	at := d.Add(12 * time.Hour)
	rs, err := e.super.Query(context.Background(),
		`UPDATE ai_calls SET created_at = $1 WHERE tenant_id IS NULL AND created_at >= $2 RETURNING id::text`, at, start)
	if err != nil {
		t.Fatalf("move the tenantless rows: %v", err)
	}
	var nullIDs []string
	for rs.Next() {
		var id string
		if err := rs.Scan(&id); err != nil {
			t.Fatal(err)
		}
		nullIDs = append(nullIDs, id)
	}
	rs.Close()
	t.Cleanup(func() {
		for _, id := range nullIDs {
			_, _ = e.super.Exec(context.Background(), `DELETE FROM ai_calls WHERE id = $1`, id)
		}
	})
	if len(nullIDs) != 1 {
		t.Fatalf("%d tenantless rows after the calls, want 1 (log: %v)", len(nullIDs), buf.lines(t))
	}
	if _, err := e.super.Exec(context.Background(), `UPDATE ai_calls SET created_at = $1 WHERE tenant_id = $2`, at, tenant); err != nil {
		t.Fatalf("move the tenant row: %v", err)
	}

	p, err := NewStore(e.app).StaffTotals(e.staff(t), Filter{From: d, To: d.Add(24*time.Hour - time.Second), Limit: 25})
	if err != nil {
		t.Fatalf("StaffTotals: %v", err)
	}
	var tenantRow, publicRow *Total
	for i, r := range p.Totals {
		switch r.TenantID {
		case tenant:
			tenantRow = &p.Totals[i]
		case "00000000-0000-0000-0000-000000000000":
			publicRow = &p.Totals[i]
		}
	}
	if tenantRow == nil || tenantRow.Calls != 1 || tenantRow.TenantName != "Chain tenant" {
		t.Errorf("tenant row = %+v, want 1 call named Chain tenant (all: %+v)", tenantRow, p.Totals)
	}
	if publicRow == nil || publicRow.Calls != 1 || publicRow.TenantName != "Readiness check" {
		t.Errorf("public row = %+v, want 1 call named Readiness check (all: %+v)", publicRow, p.Totals)
	}
	if len(p.Totals) != 2 {
		t.Errorf("%d totals groups, want 2 apart: %+v", len(p.Totals), p.Totals)
	}
}
