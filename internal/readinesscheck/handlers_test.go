package readinesscheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	holdToken    = "Tk-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" // 43 base64url characters
	consentS     = "Allow marketing communications: ASComply Africa may email me product news and offers."
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

type relCall struct {
	token  string
	ctxErr error
}

type confirmCall struct{ token, consent string }

type fakeStore struct {
	ev        *events
	count     int
	countErr  error
	holdErr   error
	mu        sync.Mutex
	inserts   []Summary
	helds     []Held
	releases  []relCall
	confirms  []confirmCall
	confirmFn func(ctx context.Context, token, consent string, send func(context.Context, Held, int) error) (Held, bool, error)
}

func (s *fakeStore) Count(context.Context, string) (int, error) { return s.count, s.countErr }

func (s *fakeStore) Insert(_ context.Context, m Summary) error {
	s.ev.add("insert")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inserts = append(s.inserts, m)
	return nil
}

func (s *fakeStore) Hold(_ context.Context, h Held) (string, error) {
	s.ev.add("hold")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.helds = append(s.helds, h)
	if s.holdErr != nil {
		return "", s.holdErr
	}
	return holdToken, nil
}

func (s *fakeStore) Release(ctx context.Context, token string) error {
	s.ev.add("release")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releases = append(s.releases, relCall{token, ctx.Err()})
	return nil
}

func (s *fakeStore) Confirm(ctx context.Context, token, consent string, send func(context.Context, Held, int) error) (Held, bool, error) {
	s.ev.add("confirm")
	s.mu.Lock()
	s.confirms = append(s.confirms, confirmCall{token, consent})
	fn := s.confirmFn
	s.mu.Unlock()
	if fn == nil {
		return Held{}, false, ErrLinkInvalid
	}
	return fn(ctx, token, consent, send)
}

func (s *fakeStore) PurgeExpired(context.Context) error { return nil }

func (s *fakeStore) rows() []Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.inserts)
}

func (s *fakeStore) holds() []Held {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.helds)
}

func (s *fakeStore) released() []relCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.releases)
}

func (s *fakeStore) confirmed() []confirmCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.confirms)
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

// runBody is the answer of the submit; it must be the same bytes for every address.
func runBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	wantStatus(t, rec, http.StatusOK, "")
	if out := decode(t, rec); len(out) != 1 || out["status"] != "confirm" {
		t.Fatalf("body = %v, want exactly {\"status\":\"confirm\"}", out)
	}
	return rec.Body.String()
}

func TestRun_AFreshAddressHoldsTheResultAndMailsTheLink(t *testing.T) {
	r := newRig(t)
	runBody(t, r.run(t, "  Visitor@X.io ", "csv", []byte(threeInvoice), "mapping", csvMapping))
	helds := r.store.holds()
	if len(helds) != 1 {
		t.Fatalf("holds = %d, want 1", len(helds))
	}
	h := helds[0]
	if h.Email != "visitor@x.io" || h.Summary.FileType != "csv" || h.Summary.RowCount != 3 ||
		!slices.Equal(h.Summary.FailedRuleIDs, []string{"buyer-tin-required"}) {
		t.Errorf("held = %+v, want visitor@x.io csv 3 rows [buyer-tin-required]", h)
	}
	if h.Mail.ChecksLeft != 0 || h.Mail.Checked == 0 || h.Mail.Checked != len(h.Mail.Invoices) {
		t.Errorf("held mail = checked %d, %d invoices, checks left %d; want the checked invoices and 0 (the press sets the checks left)",
			h.Mail.Checked, len(h.Mail.Invoices), h.Mail.ChecksLeft)
	}
	msgs := r.send.capture.Messages()
	if len(msgs) != 1 || msgs[0].To != "visitor@x.io" || msgs[0].Subject != accountmail.ReadinessConfirmSubject ||
		!strings.Contains(msgs[0].HTML, "?t="+holdToken) {
		t.Fatalf("captured = %+v, want one mail to visitor@x.io with subject %q and the held token in its link", msgs, accountmail.ReadinessConfirmSubject)
	}
	if ev := r.ev.all(); !slices.Equal(ev, []string{"hold", "send"}) {
		t.Errorf("events = %v, want hold then send and no insert", ev)
	}
}

func TestRun_AUsedAddressGetsTheUsedMailAndTheSameAnswer(t *testing.T) {
	want := runBody(t, newRig(t).run(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping))
	for _, count := range []int{3, 4} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			r := newRig(t)
			r.store.count = count
			if got := runBody(t, r.run(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping)); got != want {
				t.Errorf("body = %q, want the count-0 body %q", got, want)
			}
			if n := r.loads.Load(); n != 1 {
				t.Errorf("rule loads = %d, want 1: the engine runs for a used address too", n)
			}
			msgs := r.send.capture.Messages()
			if len(msgs) != 1 || msgs[0].To != visitor || msgs[0].Subject != accountmail.ReadinessUsedSubject ||
				strings.Contains(msgs[0].HTML, accountmail.ReadinessConfirmURL) {
				t.Fatalf("captured = %+v, want one used-checks mail to %s with no confirm link", msgs, visitor)
			}
			if ev := r.ev.all(); !slices.Equal(ev, []string{"send"}) {
				t.Errorf("events = %v, want one send and nothing held", ev)
			}
		})
	}
}

func TestRun_AnEngineRefusalIsTheSameForAFreshAndAUsedAddress(t *testing.T) {
	for _, tc := range []struct {
		name, format string
		file         []byte
		status       int
		word         string
	}{
		{"unreadable file", "csv", []byte{}, http.StatusUnprocessableEntity, "unreadable"},
		{"unsupported format", "png", []byte(oneInvoice), http.StatusBadRequest, FormatRefusal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var bodies []string
			for _, count := range []int{0, 3} {
				r := newRig(t)
				r.store.count = count
				rec := r.run(t, visitor, tc.format, tc.file, "mapping", csvMapping)
				wantStatus(t, rec, tc.status, tc.word)
				bodies = append(bodies, rec.Body.String())
				r.noWrites(t)
			}
			if bodies[0] != bodies[1] {
				t.Errorf("bodies differ: %q for a fresh address, %q for a used one", bodies[0], bodies[1])
			}
		})
	}
}

func TestRun_ThirdRunIsAllowed(t *testing.T) {
	r := newRig(t)
	r.store.count = 2
	rec := r.run(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping)
	runBody(t, rec)
	if _, has := decode(t, rec)["checks_left"]; has {
		t.Error("the answer carries checks_left, which names the address's count")
	}
	if ev := r.ev.all(); !slices.Equal(ev, []string{"hold", "send"}) {
		t.Errorf("events = %v, want hold then send", ev)
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

func TestRun_AFailedConfirmMailIs503AndReleasesTheHold(t *testing.T) {
	r := newRig(t)
	r.send.err = &accountmail.SendError{Status: 500}
	wantStatus(t, r.run(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping), http.StatusServiceUnavailable, "failed")
	if ev := r.ev.all(); !slices.Equal(ev, []string{"hold", "send", "release"}) {
		t.Errorf("events = %v, want hold, send, release", ev)
	}
	if rel := r.store.released(); len(rel) != 1 || rel[0].token != holdToken {
		t.Errorf("releases = %+v, want one with the Hold token", rel)
	}
}

func TestRun_AFailedHoldIs503AndSendsNothing(t *testing.T) {
	r := newRig(t)
	r.store.holdErr = errors.New("db down")
	wantStatus(t, r.run(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping), http.StatusServiceUnavailable, "unavailable")
	if ev := r.ev.all(); !slices.Equal(ev, []string{"hold"}) {
		t.Errorf("events = %v, want the failed hold and no send", ev)
	}
	if logs := r.logs.String(); !strings.Contains(logs, "level=ERROR") || strings.Contains(logs, visitor) {
		t.Errorf("logs = %q, want an ERROR line without the address", logs)
	}
}

func TestRun_AFailedUsedMailIs503AndHoldsNothing(t *testing.T) {
	r := newRig(t)
	r.store.count = 3
	r.send.err = &accountmail.SendError{Status: 500}
	wantStatus(t, r.run(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping), http.StatusServiceUnavailable, "failed")
	if ev := r.ev.all(); !slices.Equal(ev, []string{"send"}) {
		t.Errorf("events = %v, want one send and no hold or release", ev)
	}
}

// cancelSender holds the send open until its context ends, as a slow vendor does.
type cancelSender struct {
	ev      *events
	started chan struct{}
}

func (s *cancelSender) Send(ctx context.Context, _ []accountmail.Message) error {
	s.ev.add("send")
	close(s.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestRun_ACancelledSendStillReleasesTheHold(t *testing.T) {
	r := newRig(t)
	cs := &cancelSender{ev: r.ev, started: make(chan struct{})}
	r.svc.Sender = cs
	r.svc.mailTimeout = 2 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, ct := runForm(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping)
	req := httptest.NewRequest(http.MethodPost, "/", body).WithContext(ctx)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		r.svc.RunHandler().ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-cs.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never reached the send")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not end after the client left")
	}
	wantStatus(t, rec, http.StatusServiceUnavailable, "failed")
	rel := r.store.released()
	if len(rel) != 1 || rel[0].token != holdToken {
		t.Fatalf("releases = %+v, want one with the Hold token", rel)
	}
	if rel[0].ctxErr != nil {
		t.Errorf("Release ran on a context that was already done (%v): a client that leaves would strand the held row", rel[0].ctxErr)
	}
}

// heldRuns is the documented limit of 2 concurrent checks; it is a literal so that raising MaxConcurrent fails the busy tests.
const heldRuns = 2

// holdTwo parks heldRuns runs inside the engine and returns a func that releases them and returns their answers.
func holdTwo(t *testing.T, r *rig) func() []*httptest.ResponseRecorder {
	t.Helper()
	r.gate = make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(r.gate) }) }
	t.Cleanup(release)
	recs := make([]*httptest.ResponseRecorder, heldRuns)
	var wg sync.WaitGroup
	for i := range recs {
		body, ct := runForm(t, fmt.Sprintf("held%d@x.io", i), "csv", []byte(oneInvoice), "mapping", csvMapping)
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
	if r.loads.Load() != heldRuns {
		t.Errorf("rule loads = %d, want %d: a busy call must not reach the engine", r.loads.Load(), heldRuns)
	}
	for i, held := range finish() {
		if held.Code != http.StatusOK {
			t.Errorf("held call %d answered %d, want 200 once released", i, held.Code)
		}
	}
}

func TestRun_SecondRunForOneAddressInFlightIsBusy(t *testing.T) {
	r := newRig(t)
	r.gate = make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(r.gate) }) }
	t.Cleanup(release)
	body, ct := runForm(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- serve(r.svc.RunHandler(), body, ct) }()
	select {
	case <-r.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first run never reached the engine")
	}
	rec := r.run(t, " "+strings.ToUpper(visitor)+" ", "csv", []byte(oneInvoice), "mapping", csvMapping)
	wantStatus(t, rec, http.StatusServiceUnavailable, "busy")
	release()
	if first := <-done; first.Code != http.StatusOK {
		t.Errorf("first run answered %d, want 200", first.Code)
	}
	if n := r.loads.Load(); n != 1 {
		t.Errorf("rule loads = %d, want 1: the busy run must not reach the engine", n)
	}
}

func TestRun_ADifferentAddressIsNotBlockedByARunInFlight(t *testing.T) {
	r := newRig(t)
	r.gate = make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(r.gate) }) }
	t.Cleanup(release)
	body, ct := runForm(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- serve(r.svc.RunHandler(), body, ct) }()
	select {
	case <-r.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first run never reached the engine")
	}
	body2, ct2 := runForm(t, "other@x.io", "csv", []byte(oneInvoice), "mapping", csvMapping)
	second := make(chan *httptest.ResponseRecorder, 1)
	go func() { second <- serve(r.svc.RunHandler(), body2, ct2) }()
	select {
	case <-r.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("a run for another address was blocked")
	}
	release()
	for _, ch := range []chan *httptest.ResponseRecorder{done, second} {
		if rec := <-ch; rec.Code != http.StatusOK {
			t.Errorf("answered %d, want 200", rec.Code)
		}
	}
}

func TestRun_TheAddressIsFreedAfterARun(t *testing.T) {
	r := newRig(t)
	for range 2 {
		wantStatus(t, r.run(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping), http.StatusOK, "")
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

func TestRun_ASlotIsFreedAfterEveryOutcome(t *testing.T) {
	r := newRig(t)
	r.store.countErr = errors.New("db down")
	for range MaxConcurrent + 1 {
		wantStatus(t, r.run(t, visitor, "csv", []byte(oneInvoice)), http.StatusServiceUnavailable, "unavailable")
	}
	r.store.countErr, r.store.count = nil, 3
	for range MaxConcurrent + 1 {
		wantStatus(t, r.run(t, visitor, "csv", []byte(oneInvoice), "mapping", csvMapping), http.StatusOK, "")
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
		{"pdf with PNG bytes", "pdf", []byte("\x89PNG\r\n\x1a\n0000"), nil, http.StatusBadRequest, FormatRefusal},
		{"xlsx that is not a zip", "xlsx", []byte("not a zip"), nil, http.StatusBadRequest, FormatRefusal},
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

func TestMapping_RefusesAnXlsxThatIsNotAZip(t *testing.T) {
	r := newRig(t)
	body, ct := mapForm(t, "xlsx", []byte("not a zip"))
	wantStatus(t, serve(r.svc.MappingHandler(), body, ct), http.StatusBadRequest, FormatRefusal)
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
		{"used address", func(r *rig) { r.store.count = 3 }, []string{"mapping", csvMapping}, csv, "csv"},
		{"hold error", func(r *rig) { r.store.holdErr = errors.New("db down") }, []string{"mapping", csvMapping}, csv, "csv"},
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
		t.Error("no branch logged an ERROR line; the count and hold errors must log one")
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
	helds := r.store.holds()
	if len(helds) != 1 || helds[0].Summary.FileType != "xlsx" || helds[0].Summary.RowCount != 3 {
		t.Errorf("held = %+v, want one xlsx hold of 3 rows", helds)
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

// confirmJSON is the body the gateway posts at the press.
func confirmJSON(token, consent string) string {
	b, _ := json.Marshal(map[string]string{"token": token, "consent_text": consent})
	return string(b)
}

func (r *rig) confirmCtx(ctx context.Context, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.svc.ConfirmHandler().ServeHTTP(rec, req)
	return rec
}

func (r *rig) confirm(body string) *httptest.ResponseRecorder {
	return r.confirmCtx(context.Background(), body)
}

// heldMail is a held result as the store returns it; its mail carries the zero checks left that Hold stores.
func heldMail() Held {
	return Held{Email: visitor, Summary: Summary{Email: visitor, FileType: "csv", RowCount: 1},
		Mail: accountmail.ReadinessCheckMail{Checked: 1, Total: 1, Invoices: []accountmail.ReadinessCheckInvoice{
			{Number: "INV-1", Passes: true, Buyer: []accountmail.MailField{{Label: "Name", Value: "ZZBUYER"}}}}}}
}

// sendsAt is a Confirm that consumes a link and calls send with the recorded count.
func sendsAt(count int, fresh bool) func(context.Context, string, string, func(context.Context, Held, int) error) (Held, bool, error) {
	return func(ctx context.Context, _, _ string, send func(context.Context, Held, int) error) (Held, bool, error) {
		h := heldMail()
		return h, fresh, send(ctx, h, count)
	}
}

func TestConfirm_SendsTheResultsWithTheChecksLeftAtThePress(t *testing.T) {
	for count, want := range map[int]string{0: "You have 2 free checks left.", 1: "You have 1 free check left.", 2: "You have no free checks left."} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			r := newRig(t)
			r.store.confirmFn = sendsAt(count, true)
			rec := r.confirm(confirmJSON(holdToken, consentS))
			wantStatus(t, rec, http.StatusOK, "")
			if out := decode(t, rec); len(out) != 1 || out["outcome"] != "sent" {
				t.Errorf("body = %v, want exactly {\"outcome\":\"sent\"}", out)
			}
			msgs := r.send.capture.Messages()
			if len(msgs) != 1 || msgs[0].To != visitor || msgs[0].Subject != checkSubject || !strings.Contains(msgs[0].HTML, want) {
				t.Fatalf("captured = %+v, want one results mail to %s that says %q", msgs, visitor, want)
			}
			if got := r.store.confirmed(); len(got) != 1 || got[0].token != holdToken {
				t.Errorf("Confirm calls = %+v, want one with the posted token", got)
			}
		})
	}
}

func TestConfirm_PassesTheGatewaySentenceAndRefusesABadOne(t *testing.T) {
	for _, tc := range []struct{ name, consent string }{{"a sentence", consentS}, {"500 characters", strings.Repeat("a", 500)}, {"500 two-byte characters", strings.Repeat("é", 500)}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.store.confirmFn = sendsAt(0, true)
			wantStatus(t, r.confirm(confirmJSON(holdToken, tc.consent)), http.StatusOK, "")
			if got := r.store.confirmed(); len(got) != 1 || got[0].consent != tc.consent {
				t.Errorf("Confirm calls = %+v, want one with the posted sentence", got)
			}
		})
	}
	for _, tc := range []struct{ name, body string }{
		{"missing", `{"token":"` + holdToken + `"}`},
		{"blank", confirmJSON(holdToken, "  \t ")},
		{"501 characters", confirmJSON(holdToken, strings.Repeat("a", 501))},
		{"501 two-byte characters", confirmJSON(holdToken, strings.Repeat("é", 501))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.store.confirmFn = sendsAt(0, true)
			wantStatus(t, r.confirm(tc.body), http.StatusBadRequest, "bad request")
			if got := r.store.confirmed(); len(got) != 0 {
				t.Errorf("Confirm calls = %+v, want none for a bad sentence", got)
			}
			r.noWrites(t)
		})
	}
}

// A token is 43 characters of A-Z a-z 0-9 - _ : each class must pass the shape check.
func TestConfirm_TakesEveryBase64urlCharacter(t *testing.T) {
	for _, c := range []string{"A", "z", "0", "-", "_"} {
		t.Run(c, func(t *testing.T) {
			r := newRig(t)
			r.store.confirmFn = sendsAt(0, true)
			token := strings.Repeat(c, tokenLength)
			wantStatus(t, r.confirm(confirmJSON(token, consentS)), http.StatusOK, "")
			if got := r.store.confirmed(); len(got) != 1 || got[0].token != token {
				t.Errorf("Confirm calls = %+v, want one with the token %q", got, token)
			}
		})
	}
}

func TestConfirm_ThreeRecordedChecksGetTheUsedMail(t *testing.T) {
	r := newRig(t)
	r.store.confirmFn = sendsAt(3, false)
	rec := r.confirm(confirmJSON(holdToken, consentS))
	wantStatus(t, rec, http.StatusOK, "")
	if out := decode(t, rec); len(out) != 1 || out["outcome"] != "used" {
		t.Errorf("body = %v, want exactly {\"outcome\":\"used\"}", out)
	}
	msgs := r.send.capture.Messages()
	if len(msgs) != 1 || msgs[0].To != visitor || msgs[0].Subject != accountmail.ReadinessUsedSubject {
		t.Fatalf("captured = %+v, want one used-checks mail to %s", msgs, visitor)
	}
}

func TestConfirm_AnUnknownTokenIs410(t *testing.T) {
	r := newRig(t)
	r.store.confirmFn = func(context.Context, string, string, func(context.Context, Held, int) error) (Held, bool, error) {
		return Held{}, false, fmt.Errorf("confirm: %w", ErrLinkInvalid)
	}
	wantStatus(t, r.confirm(confirmJSON(holdToken, consentS)), http.StatusGone, "gone")
	if n := len(r.send.capture.Messages()); n != 0 {
		t.Errorf("mails = %d, want none", n)
	}
	if got := r.store.confirmed(); len(got) != 1 {
		t.Errorf("Confirm calls = %d, want the one that found no link", len(got))
	}
}

func TestConfirm_ABadBodyIs410WithoutAStoreCall(t *testing.T) {
	tooBig := `{"token":"` + holdToken + `","consent_text":"` + consentS + `","pad":"` + strings.Repeat("a", 2048) + `"}`
	for _, tc := range []struct{ name, body string }{
		{"over 2 KiB", tooBig},
		{"not JSON", "not json"},
		{"empty", ""},
		{"a JSON array", "[]"},
		{"no token", `{"consent_text":"` + consentS + `"}`},
		{"42 characters", confirmJSON(holdToken[:42], consentS)},
		{"44 characters", confirmJSON(holdToken+"x", consentS)},
		{"a character outside base64url", confirmJSON(holdToken[:42]+"+", consentS)},
		{"padding", confirmJSON(holdToken[:42]+"=", consentS)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.store.confirmFn = sendsAt(0, true)
			wantStatus(t, r.confirm(tc.body), http.StatusGone, "gone")
			if got := r.store.confirmed(); len(got) != 0 {
				t.Errorf("Confirm calls = %+v, want none", got)
			}
			r.noWrites(t)
		})
	}
}

func TestConfirm_AFailedMailIs503(t *testing.T) {
	r := newRig(t)
	r.send.err = &accountmail.SendError{Status: 500}
	r.store.confirmFn = sendsAt(0, true)
	wantStatus(t, r.confirm(confirmJSON(holdToken, consentS)), http.StatusServiceUnavailable, "failed")
}

func TestConfirm_ACommitFailureIs503AndLogsAnError(t *testing.T) {
	r := newRig(t)
	r.store.confirmFn = func(ctx context.Context, _, _ string, send func(context.Context, Held, int) error) (Held, bool, error) {
		if err := send(ctx, heldMail(), 0); err != nil {
			return Held{}, false, err
		}
		return Held{}, false, errors.New("commit failed")
	}
	wantStatus(t, r.confirm(confirmJSON(holdToken, consentS)), http.StatusServiceUnavailable, "failed")
	if n := len(r.send.capture.Messages()); n != 1 {
		t.Errorf("mails = %d, want the one that went before the commit failed", n)
	}
	logs := r.logs.String()
	if n := strings.Count(logs, "level=ERROR"); n != 1 || strings.Contains(logs, visitor) || strings.Contains(logs, holdToken) {
		t.Errorf("logs = %q, want exactly one ERROR line without the address or the token", logs)
	}
}

func TestConfirm_AClientCancelDoesNotReachTheStore(t *testing.T) {
	r := newRig(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var left time.Duration
	var hasDeadline bool
	var ctxErr error
	r.store.confirmFn = func(ctx context.Context, _, _ string, _ func(context.Context, Held, int) error) (Held, bool, error) {
		d, ok := ctx.Deadline()
		left, hasDeadline = time.Until(d), ok
		close(entered)
		<-release
		ctxErr = ctx.Err()
		return heldMail(), true, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var rec *httptest.ResponseRecorder
	done := make(chan struct{})
	go func() {
		rec = r.confirmCtx(ctx, confirmJSON(holdToken, consentS))
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Confirm was never called")
	}
	cancel()
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler did not return")
	}
	if ctxErr != nil {
		t.Errorf("Confirm's context ended with %v after the client left: the press would roll back", ctxErr)
	}
	if !hasDeadline || left <= ConfirmTimeout-3*time.Second || left > ConfirmTimeout {
		t.Errorf("Confirm deadline: set %v, %v away; want ConfirmTimeout (%v) away", hasDeadline, left, ConfirmTimeout)
	}
	// The press sends its mail inside the transaction, so its budget must outlast the mail's.
	if left <= MailTimeout {
		t.Errorf("Confirm deadline %v away is not past the %v mail budget: a slow send would roll the press back", left, MailTimeout)
	}
	wantStatus(t, rec, http.StatusOK, "")
}

func TestConfirmAndRun_LogNoAddressOrToken(t *testing.T) {
	const buyer = "ZZBUYER"
	csv := []byte("Inv,Total,BTIN,Date," + buyer + "\nA,1,,2026-10-01,Beta\n")
	needles := []string{visitor, holdToken, buyer, "secret.csv"}
	run := func(r *rig) *httptest.ResponseRecorder { return r.run(t, visitor, "csv", csv, "mapping", csvMapping) }
	confirm := func(body string) func(*rig) *httptest.ResponseRecorder {
		return func(r *rig) *httptest.ResponseRecorder { return r.confirm(body) }
	}
	good := confirmJSON(holdToken, consentS)
	failSend := func(r *rig) { r.send.err = &accountmail.SendError{Status: 500} }
	for _, tc := range []struct {
		name   string
		setup  func(*rig)
		call   func(*rig) *httptest.ResponseRecorder
		status int
	}{
		{"fresh", nil, run, 200},
		{"used", func(r *rig) { r.store.count = 3 }, run, 200},
		{"hold error", func(r *rig) { r.store.holdErr = errors.New("db down") }, run, 503},
		{"confirmation mail error", failSend, run, 503},
		{"used mail error", func(r *rig) { r.store.count = 3; failSend(r) }, run, 503},
		{"press sent", func(r *rig) { r.store.confirmFn = sendsAt(1, true) }, confirm(good), 200},
		{"press used", func(r *rig) { r.store.confirmFn = sendsAt(3, false) }, confirm(good), 200},
		{"press unknown token", nil, confirm(good), 410},
		{"press bad body", nil, confirm("not json"), 410},
		{"press bad sentence", nil, confirm(confirmJSON(holdToken, "")), 400},
		{"press mail error", func(r *rig) { failSend(r); r.store.confirmFn = sendsAt(0, true) }, confirm(good), 503},
		{"press commit error", func(r *rig) {
			r.store.confirmFn = func(ctx context.Context, _, _ string, send func(context.Context, Held, int) error) (Held, bool, error) {
				_ = send(ctx, heldMail(), 0)
				return Held{}, false, errors.New("commit failed")
			}
		}, confirm(good), 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			if tc.setup != nil {
				tc.setup(r)
			}
			if rec := tc.call(r); rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: the branch did not run, so a clean log proves nothing", rec.Code, tc.status)
			}
			logs := r.logs.String()
			for _, n := range needles {
				if strings.Contains(logs, n) {
					t.Errorf("log carries %q: %s", n, logs)
				}
			}
		})
	}
}
