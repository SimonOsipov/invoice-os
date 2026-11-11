package aicalls

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/readinesscheck"
)

// stopTransport answers every request with a valid finish_reason "stop" completion and counts them.
type stopTransport struct{ n atomic.Int32 }

func (s *stopTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "openrouter.ai" {
		return nil, fmt.Errorf("unexpected host %q", r.URL.Host)
	}
	s.n.Add(1)
	body := `{"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.01}}`
	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header:  http.Header{"Content-Type": {"application/json"}},
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: r,
	}, nil
}

// No t.Parallel: it swaps http.DefaultTransport and commits a seed row.
func TestCeiling_ATenantAICallProceedsPastTheCheckCeiling(t *testing.T) {
	e := newEnv(t)
	t1 := e.tenants(t, 1)[0]
	ctx := context.Background()

	// The agreed $2.00; readinesscheck.AISpendCeilingUSD must equal it.
	const ceiling = "2.00"
	if readinesscheck.AISpendCeilingUSD != ceiling {
		t.Fatalf("AISpendCeilingUSD = %q, want %q", readinesscheck.AISpendCeilingUSD, ceiling)
	}
	id := uuid.NewString()
	t.Cleanup(func() { _, _ = e.super.Exec(context.Background(), `DELETE FROM ai_calls WHERE id = $1`, id) })
	if _, err := e.super.Exec(ctx,
		`INSERT INTO ai_calls (id, tenant_id, client, purpose, model, input_tokens, output_tokens, cost, latency_ms, attempts, outcome)
		 VALUES ($1, NULL, 'ai', 'public-check', 'test/model', 1, 1, 6.00, 1, 1, 'ok')`, id); err != nil {
		t.Fatalf("seed the NULL-tenant spend: %v", err)
	}
	store := &readinesscheck.Store{Pool: e.app}
	if reached, err := store.AISpendReached(ctx, ceiling); err != nil || !reached {
		t.Fatalf("AISpendReached(%s) = %v, %v at the seed, want true: the seed must be past the ceiling", ceiling, reached, err)
	}

	t.Setenv(ai.EnvFake, "")
	t.Setenv(ai.EnvKey, "test-key")
	logger, buf := newLogger()
	aiClient, err := ai.FromEnv(logger)
	if err != nil {
		t.Fatalf("ai.FromEnv: %v", err)
	}
	rec := NewRecorder(e.app, logger)
	aiClient.SetRecorder(rec.Record)

	rt := &stopTransport{}
	orig := http.DefaultTransport
	http.DefaultTransport = rt
	t.Cleanup(func() { http.DefaultTransport = orig })

	tenantCtx := auth.WithIdentity(ctx, auth.Identity{Subject: "u1", TenantID: t1})
	ans, err := aiClient.Call(tenantCtx, ai.Request{Purpose: ai.PurposeDocument, Text: "Invoice Number: INV-1", SchemaName: "ceiling", Schema: json.RawMessage(`{"type":"object"}`)})
	if err != nil {
		t.Fatalf("tenant ai Call past the check ceiling: %v", err)
	}
	if ans["ok"] != true {
		t.Errorf("answer = %v, want the stub's {ok: true}", ans)
	}
	if n := rt.n.Load(); n != 1 {
		t.Errorf("requests sent = %d, want 1", n)
	}
	rec.Wait()
	rows := e.rows(t, t1, "document")
	if len(rows) != 1 || rows[0].Outcome != "ok" {
		t.Fatalf("T1 rows = %+v, want one with outcome ok (log: %v)", rows, buf.lines(t))
	}

	// The same seed makes /run on a real Store refuse. A handler that goes on reaches the nil Checker and panics.
	var rb bytes.Buffer
	mw := multipart.NewWriter(&rb)
	for k, v := range map[string]string{"email": "ceiling-" + uuid.NewString() + "@x.io", "format": "csv", "mapping": `{"invoice_number":"Inv"}`} {
		_ = mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("file", "inv.csv")
	_, _ = fw.Write([]byte("Inv,Total\nA,1\n"))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/", &rb)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	h := readinesscheck.NewService(nil, store, nil, logger).RunHandler()
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Errorf("/run went past the ceiling to the checker (panic: %v), want 503 ceiling", p)
			}
		}()
		h.ServeHTTP(w, req)
	}()
	var out struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != http.StatusServiceUnavailable || out.Error != "ceiling" {
		t.Errorf("/run = %d %q, want 503 ceiling at the same seed", w.Code, w.Body.String())
	}
}
