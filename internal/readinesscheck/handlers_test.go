package readinesscheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/importer"
	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
	"github.com/SimonOsipov/invoice-os/internal/publiccheck"
	"github.com/SimonOsipov/invoice-os/internal/validation"
)

const (
	visitor      = "pii@x.io"
	oneInvoice   = "Inv,Total,BTIN,Date\nA,1,12345678-0001,2026-10-01\n"
	threeInvoice = "Inv,Total,BTIN,Date\nA,1,12345678-0001,2026-10-01\nB,2,,2027-01-05\nC,3,12345678-0002,2027-01-06\n"
	csvMapping   = `{"invoice_number":"Inv","total":"Total","buyer_tin":"BTIN","issue_date":"Date"}`
	checkSubject = "Your ASComply readiness check results"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type events struct {
	mu   sync.Mutex
	list []string
}

func (e *events) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, s)
}

func (e *events) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.list)
}

type fakeStore struct {
	ev        *events
	count     int
	countErr  error
	insertErr error
	mu        sync.Mutex
	inserts   []Summary
}

func (s *fakeStore) Count(context.Context, string) (int, error) { return s.count, s.countErr }

func (s *fakeStore) Insert(_ context.Context, m Summary) error {
	s.ev.add("insert")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inserts = append(s.inserts, m)
	return s.insertErr
}

func (s *fakeStore) rows() []Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.inserts)
}

// recSender records the order of its sends against the store and delegates to a Capture.
type recSender struct {
	ev      *events
	err     error
	capture accountmail.Capture
}

func (s *recSender) Send(ctx context.Context, msgs []accountmail.Message) error {
	s.ev.add("send")
	if s.err != nil {
		return s.err
	}
	return s.capture.Send(ctx, msgs)
}

type fakeAI struct{ calls atomic.Int32 }

func (a *fakeAI) Enabled() bool { return true }
func (a *fakeAI) Call(context.Context, ai.Request) (map[string]any, error) {
	a.calls.Add(1)
	return map[string]any{}, nil
}

type fakeMapper struct {
	calls  atomic.Int32
	answer map[string]any
}

func (m *fakeMapper) Enabled() bool { return true }
func (m *fakeMapper) Call(context.Context, ai.Request) (map[string]any, error) {
	m.calls.Add(1)
	return m.answer, nil
}

type fakeText struct {
	calls atomic.Int32
	err   error
}

func (r *fakeText) Name() string    { return "stub" }
func (r *fakeText) Version() string { return "1" }
func (r *fakeText) Read(context.Context, extraction.Document, func(extraction.Page) error) (extraction.PageResult, error) {
	r.calls.Add(1)
	return extraction.PageResult{}, r.err
}

type rig struct {
	svc     *Service
	store   *fakeStore
	send    *recSender
	ev      *events
	logs    *lockedBuf
	mapper  *fakeMapper
	ai      *fakeAI
	text    *fakeText
	loads   atomic.Int32
	entered chan struct{}
	gate    chan struct{} // when set, LoadRules blocks until it closes or the run context ends
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{ev: &events{}, logs: &lockedBuf{}, ai: &fakeAI{}, text: &fakeText{err: errors.New("docling down")},
		entered: make(chan struct{}, 8)}
	r.store = &fakeStore{ev: r.ev}
	r.send = &recSender{ev: r.ev}
	r.mapper = &fakeMapper{answer: map[string]any{
		"invoice_number": "Inv", "supplier_name": "Seller", "supplier_tin": "Seller TIN",
		"total": "Total", "header_row": json.Number("1"),
	}}
	checker := &publiccheck.Checker{
		LoadRules: r.load,
		Engine:    validation.NewDefaultEngine(),
		Mapper:    r.mapper,
		Readers:   extraction.CheckReaders{Text: r.text, AI: r.ai},
		Now:       func() time.Time { return time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC) },
	}
	logger := slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r.svc = NewService(checker, r.store, r.send, logger)
	return r
}

func (r *rig) load(ctx context.Context, dates []string, _ []validation.Rule) (map[string]validation.RuleSet, error) {
	r.loads.Add(1)
	select {
	case r.entered <- struct{}{}:
	default:
	}
	if r.gate != nil {
		select {
		case <-r.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	rule := validation.Rule{Key: "buyer-tin-required", Type: validation.TypeRequired, Target: "buyer.tin",
		Params: json.RawMessage(`{}`), Severity: "error", Message: "buyer tin is needed", Scope: "document", Enabled: true}
	out := map[string]validation.RuleSet{}
	for _, d := range dates {
		out[d] = validation.RuleSet{Version: 4, Rules: []validation.Rule{rule}}
	}
	return out, nil
}

// untouched asserts the run stopped before the engine and wrote nothing.
func (r *rig) untouched(t *testing.T) {
	t.Helper()
	if n, a, m, x := r.loads.Load(), r.ai.calls.Load(), r.mapper.calls.Load(), r.text.calls.Load(); n+a+m+x != 0 {
		t.Errorf("engine calls: rules %d, ai %d, mapper %d, text reader %d, want all 0", n, a, m, x)
	}
	r.noWrites(t)
}

func (r *rig) noWrites(t *testing.T) {
	t.Helper()
	if ev := r.ev.all(); len(ev) != 0 {
		t.Errorf("events = %v, want no send and no insert", ev)
	}
}

func form(t *testing.T, parts [][2]string, file []byte) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, p := range parts {
		if err := w.WriteField(p[0], p[1]); err != nil {
			t.Fatal(err)
		}
	}
	if file != nil {
		fw, err := w.CreateFormFile("file", "secret.csv")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(file)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, w.FormDataContentType()
}

func runForm(t *testing.T, email, format string, file []byte, kv ...string) (io.Reader, string) {
	t.Helper()
	parts := [][2]string{{"email", email}, {"format", format}}
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, [2]string{kv[i], kv[i+1]})
	}
	return form(t, parts, file)
}

func serve(h http.Handler, body io.Reader, ctype string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/", body)
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func (r *rig) run(t *testing.T, email, format string, file []byte, kv ...string) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := runForm(t, email, format, file, kv...)
	return serve(r.svc.RunHandler(), body, ct)
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q is not JSON: %v (status %d)", rec.Body.String(), err, rec.Code)
	}
	return out
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, status int, errWord string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, status, rec.Body.String())
	}
	if errWord == "" {
		return
	}
	if got := decode(t, rec)["error"]; got != errWord {
		t.Errorf("error = %v, want %q", got, errWord)
	}
}

func TestRun_FourthRunIsRefusedBeforeTheEngine(t *testing.T) {
	for _, count := range []int{3, 4} {
		r := newRig(t)
		r.store.count = count
		rec := r.run(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping)
		wantStatus(t, rec, http.StatusConflict, "sign_up")
		r.untouched(t)
	}
}

func TestRun_ThirdRunIsAllowed(t *testing.T) {
	r := newRig(t)
	r.store.count = 2
	rec := r.run(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping)
	wantStatus(t, rec, http.StatusOK, "")
	if got := decode(t, rec)["checks_left"]; got != float64(0) {
		t.Errorf("checks_left = %v, want 0", got)
	}
	if ev := r.ev.all(); !slices.Equal(ev, []string{"send", "insert"}) {
		t.Errorf("events = %v, want send then insert", ev)
	}
}

func TestRun_SendsOneMailThenWritesOneRow(t *testing.T) {
	r := newRig(t)
	rec := r.run(t, "  Visitor@X.io ", "csv", []byte(threeInvoice), "mapping", csvMapping)
	wantStatus(t, rec, http.StatusOK, "")
	if got := decode(t, rec)["checks_left"]; got != float64(2) {
		t.Errorf("checks_left = %v, want 2", got)
	}
	msgs := r.send.capture.Messages()
	if len(msgs) != 1 || msgs[0].To != "visitor@x.io" || msgs[0].Subject != checkSubject || msgs[0].HTML == "" {
		t.Fatalf("captured = %+v, want one mail to visitor@x.io with subject %q and a body", msgs, checkSubject)
	}
	rows := r.store.rows()
	if len(rows) != 1 {
		t.Fatalf("inserts = %d, want 1", len(rows))
	}
	got := rows[0]
	if strings.TrimSpace(strings.ToLower(got.Email)) != "visitor@x.io" || got.FileType != "csv" || got.RowCount != 3 ||
		!slices.Equal(got.FailedRuleIDs, []string{"buyer-tin-required"}) {
		t.Errorf("summary = %+v, want visitor@x.io csv 3 rows [buyer-tin-required]", got)
	}
	if ev := r.ev.all(); !slices.Equal(ev, []string{"send", "insert"}) {
		t.Errorf("events = %v, want send then insert", ev)
	}
}

func TestRun_ReadUnavailableIs503AndCountsNothing(t *testing.T) {
	r := newRig(t)
	rec := r.run(t, visitor, "pdf", []byte("%PDF-1.4"))
	wantStatus(t, rec, http.StatusServiceUnavailable, "failed")
	if r.text.calls.Load() != 1 {
		t.Errorf("text reader calls = %d, want 1 (the engine ran, so the answer is failed, not unavailable)", r.text.calls.Load())
	}
	r.noWrites(t)
}

func TestRun_SpentBudgetIs503(t *testing.T) {
	r := newRig(t)
	r.gate = make(chan struct{})
	r.svc.runBudget = 50 * time.Millisecond
	start := time.Now()
	rec := r.run(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping)
	wantStatus(t, rec, http.StatusServiceUnavailable, "failed")
	if r.loads.Load() != 1 {
		t.Errorf("rule loads = %d, want 1 (the engine started and was cut off)", r.loads.Load())
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("answered after %s, want the shortened budget to cut the run", d)
	}
	r.noWrites(t)
}

func TestRun_FailedSendIs503AndWritesNoRow(t *testing.T) {
	r := newRig(t)
	r.send.err = &accountmail.SendError{Status: 500}
	rec := r.run(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping)
	wantStatus(t, rec, http.StatusServiceUnavailable, "failed")
	if ev := r.ev.all(); !slices.Equal(ev, []string{"send"}) {
		t.Errorf("events = %v, want one send and no insert", ev)
	}
}

// holdTwo parks MaxConcurrent runs inside the engine and returns a func that releases them and returns their answers.
func holdTwo(t *testing.T, r *rig) func() []*httptest.ResponseRecorder {
	t.Helper()
	r.gate = make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(r.gate) }) }
	t.Cleanup(release)
	recs := make([]*httptest.ResponseRecorder, MaxConcurrent)
	var wg sync.WaitGroup
	for i := range recs {
		body, ct := runForm(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping)
		wg.Add(1)
		go func() {
			defer wg.Done()
			recs[i] = serve(r.svc.RunHandler(), body, ct)
		}()
	}
	for range recs {
		select {
		case <-r.entered:
		case <-time.After(3 * time.Second):
			release()
			wg.Wait()
			var codes []int
			for _, rec := range recs {
				codes = append(codes, rec.Code)
			}
			t.Fatalf("%d calls never reached the engine together; they answered %v", MaxConcurrent, codes)
		}
	}
	return func() []*httptest.ResponseRecorder {
		release()
		wg.Wait()
		return recs
	}
}

func TestRun_ThirdConcurrentCallIsBusy(t *testing.T) {
	r := newRig(t)
	finish := holdTwo(t, r)
	rec := r.run(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping)
	wantStatus(t, rec, http.StatusServiceUnavailable, "busy")
	if r.loads.Load() != int32(MaxConcurrent) {
		t.Errorf("rule loads = %d, want %d: a busy call must not reach the engine", r.loads.Load(), MaxConcurrent)
	}
	for i, held := range finish() {
		if held.Code != http.StatusOK {
			t.Errorf("held call %d answered %d, want 200 once released", i, held.Code)
		}
	}
}

type failReader struct{ t *testing.T }

func (f failReader) Read([]byte) (int, error) {
	f.t.Error("the body of a busy call was read")
	return 0, io.EOF
}

func TestRun_BusyIsAnsweredBeforeTheBodyIsRead(t *testing.T) {
	r := newRig(t)
	finish := holdTwo(t, r)
	_, ct := runForm(t, visitor, "csv", []byte(oneInvoice))
	rec := serve(r.svc.RunHandler(), failReader{t}, ct)
	wantStatus(t, rec, http.StatusServiceUnavailable, "busy")
	finish()
}

func TestRun_CountErrorIs503BeforeTheEngine(t *testing.T) {
	r := newRig(t)
	r.store.countErr = errors.New("db down")
	rec := r.run(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping)
	wantStatus(t, rec, http.StatusServiceUnavailable, "unavailable")
	r.untouched(t)
	if logs := r.logs.String(); !strings.Contains(logs, "level=ERROR") || strings.Contains(logs, visitor) {
		t.Errorf("logs = %q, want an ERROR line without the address", logs)
	}
}

func TestRun_FailedInsertStillAnswers200(t *testing.T) {
	r := newRig(t)
	r.store.insertErr = errors.New("db down")
	rec := r.run(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping)
	wantStatus(t, rec, http.StatusOK, "")
	if n := len(r.send.capture.Messages()); n != 1 {
		t.Errorf("mails = %d, want 1: the visitor has the result", n)
	}
	logs := r.logs.String()
	if n := strings.Count(logs, "level=ERROR"); n != 1 || strings.Contains(logs, visitor) {
		t.Errorf("logs = %q, want exactly one ERROR line without the address", logs)
	}
}

func TestRun_ASlotIsFreedAfterEveryOutcome(t *testing.T) {
	r := newRig(t)
	r.store.countErr = errors.New("db down")
	for range MaxConcurrent + 1 {
		wantStatus(t, r.run(t, visitor, "csv", []byte(oneInvoice)), http.StatusServiceUnavailable, "unavailable")
	}
	r.store.countErr, r.store.count = nil, 3
	for range MaxConcurrent + 1 {
		wantStatus(t, r.run(t, visitor, "csv", []byte(oneInvoice)), http.StatusConflict, "sign_up")
	}
	r.store.count = 0
	wantStatus(t, r.run(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping), http.StatusOK, "")
}

func TestRun_EngineErrorsMapToStatuses(t *testing.T) {
	big := make([]byte, publiccheck.MaxFileBytes+1)
	copy(big, oneInvoice)
	noNumber := `{"total":"Total"}`
	_, wantErr := (&publiccheck.Checker{}).Check(context.Background(),
		publiccheck.Request{File: []byte(oneInvoice), Format: "csv", Mapping: map[string]string{"total": "Total"}})
	if !errors.Is(wantErr, importer.ErrValidation) {
		t.Fatalf("setup: a mapping without invoice_number gives %v, want importer.ErrValidation", wantErr)
	}
	importerText := strings.TrimPrefix(wantErr.Error(), importer.ErrValidation.Error()+": ")
	if importerText == "" {
		t.Fatal("setup: the importer text is empty")
	}

	for _, tc := range []struct {
		name, format string
		file         []byte
		kv           []string
		status       int
		errWord      string
	}{
		{"empty file", "csv", []byte{}, []string{"mapping", csvMapping}, http.StatusUnprocessableEntity, "unreadable"},
		{"png format", "png", []byte(oneInvoice), nil, http.StatusBadRequest, ""},
		{"15 MiB and a byte", "csv", big, []string{"mapping", csvMapping}, http.StatusRequestEntityTooLarge, ""},
		{"no invoice_number in the mapping", "csv", []byte(oneInvoice), []string{"mapping", noNumber}, http.StatusBadRequest, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			rec := r.run(t, visitor, tc.format, tc.file, tc.kv...)
			wantStatus(t, rec, tc.status, tc.errWord)
			if tc.name == "no invoice_number in the mapping" {
				if got, _ := decode(t, rec)["error"].(string); !strings.Contains(got, importerText) {
					t.Errorf("error = %q, want the importer text %q", got, importerText)
				}
			}
			r.untouched(t)
		})
	}
}

func TestRun_MalformedMappingIs400(t *testing.T) {
	for _, tc := range []struct{ name, key, val string }{
		{"mapping not json", "mapping", "not json"},
		{"header_row not digits", "header_row", "2a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			kv := []string{"mapping", csvMapping, tc.key, tc.val}
			rec := r.run(t, visitor, "csv", []byte(oneInvoice), kv...)
			wantStatus(t, rec, http.StatusBadRequest, "invalid request body")
			r.untouched(t)
		})
	}
}

func mapForm(t *testing.T, format string, file []byte) (io.Reader, string) {
	return form(t, [][2]string{{"format", format}}, file)
}

func TestMapping_ReturnsTheSuggestion(t *testing.T) {
	r := newRig(t)
	body, ct := mapForm(t, "csv", []byte("Inv,Seller,Seller TIN,Total\nINV-1,Acme Ltd,12345678-0001,100.00\n"))
	rec := serve(r.svc.MappingHandler(), body, ct)
	wantStatus(t, rec, http.StatusOK, "")
	out := decode(t, rec)
	for _, k := range []string{"columns", "sample_rows", "header_row", "rows_total", "mapping", "source"} {
		if _, ok := out[k]; !ok {
			t.Errorf("response lacks key %q: %v", k, out)
		}
	}
	if len(out) != 6 {
		t.Errorf("response has %d keys, want exactly the 6 snake_case ones: %v", len(out), out)
	}
	if out["source"] != "ai" || out["header_row"] != float64(1) || out["rows_total"] != float64(1) {
		t.Errorf("source %v header_row %v rows_total %v, want ai 1 1", out["source"], out["header_row"], out["rows_total"])
	}
	if m, _ := out["mapping"].(map[string]any); m["supplier_name"] != "Seller" {
		t.Errorf("mapping = %v, want supplier_name Seller", out["mapping"])
	}
	if r.mapper.calls.Load() != 1 {
		t.Errorf("mapper calls = %d, want 1", r.mapper.calls.Load())
	}
}

func TestMapping_RefusesADocument(t *testing.T) {
	r := newRig(t)
	body, ct := mapForm(t, "pdf", []byte("%PDF-1.4"))
	wantStatus(t, serve(r.svc.MappingHandler(), body, ct), http.StatusBadRequest, "")
	r.untouched(t)
}

func TestHandlers_LogNoPersonalData(t *testing.T) {
	const buyer = "ZZBUYER"
	csv := []byte("Inv,Total,BTIN,Date," + buyer + "\nA,1,,2026-10-01,Beta\n")
	needles := []string{visitor, buyer, "secret.csv"}
	var errorLines int
	for _, tc := range []struct {
		name  string
		setup func(*rig)
		kv    []string
		file  []byte
		fmt   string
	}{
		{"count error", func(r *rig) { r.store.countErr = errors.New("db down") }, []string{"mapping", csvMapping}, csv, "csv"},
		{"sign up", func(r *rig) { r.store.count = 3 }, []string{"mapping", csvMapping}, csv, "csv"},
		{"insert error", func(r *rig) { r.store.insertErr = errors.New("db down") }, []string{"mapping", csvMapping}, csv, "csv"},
		{"send error", func(r *rig) { r.send.err = &accountmail.SendError{Status: 500} }, []string{"mapping", csvMapping}, csv, "csv"},
		{"read unavailable", nil, nil, []byte("%PDF-1.4"), "pdf"},
		{"mapping names a column the file lacks", nil, []string{"mapping", `{"invoice_number":"Inv","buyer_name":"` + buyer + `X"}`}, csv, "csv"},
		{"malformed mapping", nil, []string{"mapping", buyer}, csv, "csv"},
		{"unreadable", nil, []string{"mapping", csvMapping}, []byte{}, "csv"},
		{"spent budget", func(r *rig) { r.gate = make(chan struct{}); r.svc.runBudget = 20 * time.Millisecond }, []string{"mapping", csvMapping}, csv, "csv"},
		{"success", nil, []string{"mapping", csvMapping}, csv, "csv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			if tc.setup != nil {
				tc.setup(r)
			}
			rec := r.run(t, visitor, tc.fmt, tc.file, tc.kv...)
			if rec.Code == http.StatusNotImplemented {
				t.Fatalf("answered 501: the handler is not there, so a clean log proves nothing")
			}
			logs := r.logs.String()
			errorLines += strings.Count(logs, "level=ERROR")
			for _, n := range needles {
				if strings.Contains(logs, n) {
					t.Errorf("log carries %q: %s", n, logs)
				}
			}
		})
	}
	t.Run("mapping route", func(t *testing.T) {
		r := newRig(t)
		body, ct := mapForm(t, "csv", csv)
		rec := serve(r.svc.MappingHandler(), body, ct)
		if rec.Code == http.StatusNotImplemented {
			t.Fatalf("answered 501: the handler is not there, so a clean log proves nothing")
		}
		for _, n := range needles {
			if strings.Contains(r.logs.String(), n) {
				t.Errorf("log carries %q: %s", n, r.logs.String())
			}
		}
	})
	if errorLines == 0 {
		t.Error("no branch logged an ERROR line; the count and insert errors must log one")
	}
}

// A temp file would hold the visitor's file; runtime cannot see a spill below 32 MiB, so this reads the source.
func TestHandlers_NeverParseAMultipartForm(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "handlers.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	called := map[string]int{}
	ast.Inspect(f, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				called[sel.Sel.Name]++
			}
		}
		return true
	})
	if called["MultipartReader"] == 0 {
		t.Fatal("handlers.go never calls MultipartReader, so the check on the banned calls proves nothing")
	}
	for _, banned := range []string{"FormFile", "FormValue", "PostFormValue", "ParseMultipartForm", "ParseForm"} {
		if called[banned] != 0 {
			t.Errorf("handlers.go calls %s: it can spool the file to a temp file", banned)
		}
	}
}

func TestCapturedMail_ReturnsOnlyThatAddressOldestFirst(t *testing.T) {
	var c accountmail.Capture
	_ = c.Send(context.Background(), []accountmail.Message{
		{To: "a@x.io", Subject: "s1", HTML: "h1"}, {To: "b@x.io", Subject: "other", HTML: "ho"}, {To: "a@x.io", Subject: "s2", HTML: "h2"},
	})
	r := newRig(t)
	h := r.svc.CapturedMailHandler(&c)

	rec := serve(h, strings.NewReader(`{"to":"a@x.io"}`), "application/json")
	wantStatus(t, rec, http.StatusOK, "")
	var got struct {
		Messages []struct{ Subject, HTML string } `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || got.Messages[0].Subject != "s1" || got.Messages[1].Subject != "s2" || got.Messages[1].HTML != "h2" {
		t.Errorf("messages = %+v, want s1 then s2 for a@x.io only", got.Messages)
	}

	rec = serve(h, strings.NewReader(`{"to":"nobody@x.io"}`), "application/json")
	wantStatus(t, rec, http.StatusOK, "")
	if !strings.Contains(rec.Body.String(), `"messages":[]`) {
		t.Errorf("body = %s, want an empty messages array, not null", rec.Body.String())
	}

	wantStatus(t, serve(h, strings.NewReader(`not json`), "application/json"), http.StatusBadRequest, "")
}

func xlsxOf(t *testing.T, csv string) []byte {
	t.Helper()
	f := excelize.NewFile()
	for r, line := range strings.Split(strings.TrimSpace(csv), "\n") {
		for c, cell := range strings.Split(line, ",") {
			ref, _ := excelize.CoordinatesToCellName(c+1, r+1)
			if err := f.SetCellValue("Sheet1", ref, cell); err != nil {
				t.Fatal(err)
			}
		}
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRun_AnXlsxRunIsRecordedAsXlsx(t *testing.T) {
	r := newRig(t)
	rec := r.run(t, visitor, "xlsx", xlsxOf(t, threeInvoice), "mapping", csvMapping)
	wantStatus(t, rec, http.StatusOK, "")
	rows := r.store.rows()
	if len(rows) != 1 || rows[0].FileType != "xlsx" || rows[0].RowCount != 3 {
		t.Errorf("summary = %+v, want one xlsx row of 3 rows", rows)
	}
}

func TestRun_UnexpectedEngineErrorIs500WithoutPayload(t *testing.T) {
	r := newRig(t)
	r.svc.Checker.LoadRules = func(context.Context, []string, []validation.Rule) (map[string]validation.RuleSet, error) {
		return nil, errors.New("rules table gone: " + visitor)
	}
	rec := r.run(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping)
	wantStatus(t, rec, http.StatusInternalServerError, "internal error")
	r.noWrites(t)
	if logs := r.logs.String(); !strings.Contains(logs, "level=ERROR") || strings.Contains(logs, visitor) {
		t.Errorf("logs = %q, want an ERROR line without the error text", logs)
	}
}

func TestRun_TheMailStatesTheChecksLeftTheAnswerReports(t *testing.T) {
	for count, want := range map[int]string{0: "2 free checks left", 1: "1 free check left", 2: "no free checks left"} {
		r := newRig(t)
		r.store.count = count
		wantStatus(t, r.run(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping), http.StatusOK, "")
		msgs := r.send.capture.Messages()
		if len(msgs) != 1 || !strings.Contains(msgs[0].HTML, want) {
			t.Errorf("count %d: mail lacks %q", count, want)
		}
	}
}

func TestRun_RefusesAMissingEmailFileOrOversizePart(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parts [][2]string
		file  []byte
	}{
		{"no email", [][2]string{{"format", "csv"}}, []byte(oneInvoice)},
		{"blank email", [][2]string{{"email", "  "}, {"format", "csv"}}, []byte(oneInvoice)},
		{"no file part", [][2]string{{"email", visitor}, {"format", "csv"}}, nil},
		{"a part over 1 MiB", [][2]string{{"email", visitor}, {"format", "csv"}, {"mapping", strings.Repeat("a", 1<<20+1)}}, []byte(oneInvoice)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			body, ct := form(t, tc.parts, tc.file)
			wantStatus(t, serve(r.svc.RunHandler(), body, ct), http.StatusBadRequest, "invalid request body")
			r.untouched(t)
		})
	}
}

func TestMapping_ShareTheTwoSlotsWithRuns(t *testing.T) {
	r := newRig(t)
	finish := holdTwo(t, r)
	body, ct := mapForm(t, "csv", []byte("Inv,Total\nA,1\n"))
	wantStatus(t, serve(r.svc.MappingHandler(), body, ct), http.StatusServiceUnavailable, "busy")
	if r.mapper.calls.Load() != 0 {
		t.Errorf("mapper calls = %d, want 0: a busy mapping call must not reach the AI", r.mapper.calls.Load())
	}
	finish()
}
