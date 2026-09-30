package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

func TestNewJWKSClient_TracesAndTimesOut(t *testing.T) {
	sentrytest.Boot(t, "gateway")
	stub := sentrytest.NewHeaderStub(t, nil)
	c := newJWKSClient()
	if c.Timeout != 10*time.Second {
		t.Errorf("Timeout = %v, want 10s", c.Timeout)
	}
	get := func(ctx context.Context) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, stub.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		resp.Body.Close()
	}

	tx := sentry.StartTransaction(context.Background(), "GET /x")
	get(tx.Context())
	tx.Finish()
	traceID, _ := stub.Only(t, 1).Trace(t)
	if traceID != tx.TraceID.String() {
		t.Errorf("sentry-trace trace id = %s, want the span's %s", traceID, tx.TraceID.String())
	}

	get(context.Background())
	stub.Only(t, 2).AssertUntraced(t)
}

func TestNewJWKSClient_VerifierFetchesKeysUnderTheRequestSpan(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "gateway")
	issuer, err := auth.NewMockIssuer("https://issuer.test")
	if err != nil {
		t.Fatal(err)
	}
	stub := sentrytest.NewHeaderStub(t, issuer.JWKSHandler().ServeHTTP)
	v, err := auth.NewVerifier(auth.Config{Issuer: "https://issuer.test", JWKSURL: stub.URL, HTTPClient: newJWKSClient()})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := issuer.Mint(auth.MintOptions{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}

	tx := sentry.StartTransaction(context.Background(), "GET /api/x")
	if _, err := v.Verify(tx.Context(), tok); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	tx.Finish()

	traceID, parentID := stub.Only(t, 1).Trace(t)
	if traceID != tx.TraceID.String() {
		t.Errorf("JWKS fetch trace id = %s, want the request's %s", traceID, tx.TraceID.String())
	}
	txs := rec.Transactions()
	if len(txs) != 1 {
		t.Fatalf("sent %d transactions, want 1", len(txs))
	}
	spans := sentrytest.ClientSpans(txs[0])
	if len(spans) != 1 || spans[0].SpanID.String() != parentID {
		t.Errorf("http.client spans = %d, want one whose id is the fetch's parent %s", len(spans), parentID)
	}
}
