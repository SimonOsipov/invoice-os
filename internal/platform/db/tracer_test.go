package db

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

const spanOp = "db.sql.query"

// traceQuery runs one traced query under parent: Start, then End with err.
func traceQuery(parent *sentry.Span, sql string, args []any, err error) {
	ctx := queryTracer{}.TraceQueryStart(parent.Context(), nil, pgx.TraceQueryStartData{SQL: sql, Args: args})
	queryTracer{}.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: err})
}

// oneTransactionWithSpans fails unless exactly one transaction was sent, and returns its spans.
func oneTransactionWithSpans(t *testing.T, rec *sentrytest.Recorder, want int) []*sentry.Span {
	t.Helper()
	txs := rec.Transactions()
	if len(txs) != 1 {
		t.Fatalf("sent %d transactions, want 1", len(txs))
	}
	if got := len(txs[0].Spans); got != want {
		t.Fatalf("transaction has %d spans, want %d", got, want)
	}
	return txs[0].Spans
}

func TestQueryTracer_ChildSpanUnderAParent(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "db-test")
	tx := sentry.StartTransaction(context.Background(), "GET /x")

	traceQuery(tx, "SELECT $1", []any{sentrytest.MarkerTIN}, nil)
	tx.Finish()

	spans := oneTransactionWithSpans(t, rec, 1)
	if spans[0].Op != spanOp {
		t.Errorf("span op = %q, want %q", spans[0].Op, spanOp)
	}
	if spans[0].Description != "SELECT $1" {
		t.Errorf("span description = %q, want the SQL text", spans[0].Description)
	}
	if got := fmt.Sprint(spans[0].Data["db.system"]); got != "postgresql" {
		t.Errorf("span data db.system = %q, want postgresql", got)
	}
	sentrytest.AssertNoLeak(t, rec.Transactions())
}

func TestQueryTracer_NoParentNoSpan(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "db-test")

	ctx := queryTracer{}.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	if sentry.SpanFromContext(ctx) != nil {
		t.Error("a query with no parent span put a span in the context")
	}
	queryTracer{}.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	if got := rec.Transactions(); len(got) != 0 {
		t.Fatalf("a query with no parent sent %d transactions, want none", len(got))
	}

	tx := sentry.StartTransaction(context.Background(), "GET /x")
	traceQuery(tx, "SELECT 1", nil, nil)
	tx.Finish()
	oneTransactionWithSpans(t, rec, 1)
}

func TestQueryTracer_EndNeverFinishesTheParent(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "db-test")
	tx := sentry.StartTransaction(context.Background(), "GET /x")

	// End on a context that never passed through Start finishes nothing.
	queryTracer{}.TraceQueryEnd(tx.Context(), nil, pgx.TraceQueryEndData{})
	if !tx.EndTime.IsZero() {
		t.Fatal("End without a Start finished the parent")
	}

	traceQuery(tx, "SELECT 1", nil, nil)
	if !tx.EndTime.IsZero() {
		t.Error("End finished the parent span")
	}
	if got := rec.Transactions(); len(got) != 0 {
		t.Fatalf("End sent %d transactions before the parent finished, want none", len(got))
	}

	tx.Finish()
	spans := oneTransactionWithSpans(t, rec, 1)
	if spans[0].EndTime.IsZero() {
		t.Error("End left its own span unfinished")
	}
}

func TestQueryTracer_ErrorSetsStatusWithoutText(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "db-test")
	tx := sentry.StartTransaction(context.Background(), "GET /x")

	traceQuery(tx, "SELECT 1", nil, nil)
	traceQuery(tx, "INSERT INTO t VALUES ($1)", nil,
		fmt.Errorf("Key (tin)=(%s) already exists", sentrytest.MarkerTIN))
	tx.Finish()

	spans := oneTransactionWithSpans(t, rec, 2)
	if spans[0].Status == sentry.SpanStatusInternalError {
		t.Error("a successful query set status internal_error")
	}
	if spans[1].Status != sentry.SpanStatusInternalError {
		t.Errorf("failed query status = %v, want internal_error", spans[1].Status)
	}
	sentrytest.AssertNoLeak(t, rec.Transactions())
}

func TestQueryTracer_InterleavedQueriesFinishTheirOwnSpans(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "db-test")
	tx := sentry.StartTransaction(context.Background(), "GET /x")

	ctxA := queryTracer{}.TraceQueryStart(tx.Context(), nil, pgx.TraceQueryStartData{SQL: "A"})
	ctxB := queryTracer{}.TraceQueryStart(tx.Context(), nil, pgx.TraceQueryStartData{SQL: "B"})
	queryTracer{}.TraceQueryEnd(ctxB, nil, pgx.TraceQueryEndData{Err: fmt.Errorf("boom")})
	queryTracer{}.TraceQueryEnd(ctxA, nil, pgx.TraceQueryEndData{})
	tx.Finish()

	spans := oneTransactionWithSpans(t, rec, 2)
	byDesc := map[string]*sentry.Span{}
	for _, s := range spans {
		byDesc[s.Description] = s
	}
	if byDesc["A"] == nil || byDesc["B"] == nil {
		t.Fatalf("spans = %v, want one each for A and B", byDesc)
	}
	if byDesc["A"].Status == sentry.SpanStatusInternalError {
		t.Error("query A took query B's error status")
	}
	if byDesc["B"].Status != sentry.SpanStatusInternalError {
		t.Errorf("query B status = %v, want internal_error", byDesc["B"].Status)
	}
}

func TestQueryTracer_ConcurrentQueriesUnderOneParent(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "db-test")
	tx := sentry.StartTransaction(context.Background(), "GET /x")

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			traceQuery(tx, "SELECT 1", nil, nil)
		}()
	}
	wg.Wait()
	if got := rec.Transactions(); len(got) != 0 {
		t.Fatalf("queries sent %d transactions before the parent finished, want none", len(got))
	}
	tx.Finish()

	spans := oneTransactionWithSpans(t, rec, n)
	ids := map[sentry.SpanID]bool{}
	for _, s := range spans {
		ids[s.SpanID] = true
		if s.EndTime.IsZero() {
			t.Fatal("a concurrent query left its span unfinished")
		}
	}
	if len(ids) != n {
		t.Errorf("%d distinct span ids, want %d", len(ids), n)
	}
}

func TestNewPool_InstallsTheQueryTracer(t *testing.T) {
	pool, err := NewPool(context.Background(), "postgres://u:p@127.0.0.1:1/x")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, ok := pool.Config().ConnConfig.Tracer.(queryTracer); !ok {
		t.Errorf("pool tracer = %T, want queryTracer", pool.Config().ConnConfig.Tracer)
	}
}

func TestNewPool_BadDSNErrorIsWrapped(t *testing.T) {
	pool, err := NewPool(context.Background(), "postgres://u:p@[::1")
	if pool != nil {
		pool.Close()
		t.Fatal("NewPool returned a pool for a bad DSN")
	}
	if err == nil || !strings.HasPrefix(err.Error(), "db: open pool: ") {
		t.Errorf("error = %v, want one that starts %q", err, "db: open pool: ")
	}
}
