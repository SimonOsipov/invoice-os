package aicalls

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
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
