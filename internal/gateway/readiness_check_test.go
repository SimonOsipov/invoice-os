package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

const (
	checkIP      = "203.0.113.7"
	checkRemote  = checkIP + ":4000"
	checkConsent = "I agree to the check terms."
)

type workerGot struct {
	fields   map[string]string
	file     []byte
	fileName string
}

type fakeWorker struct {
	srv       *httptest.Server
	mu        sync.Mutex
	n         int
	got       []workerGot
	status    int
	reply     string
	onRequest func(*http.Request)
}

func newFakeWorker(t *testing.T) *fakeWorker {
	t.Helper()
	w := &fakeWorker{status: http.StatusOK, reply: `{"checks_left":2}`}
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		w.n++
		hook := w.onRequest
		w.mu.Unlock()
		if hook != nil {
			hook(r)
		}
		g := workerGot{fields: map[string]string{}}
		if mr, err := r.MultipartReader(); err == nil {
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				b, _ := io.ReadAll(p)
				if p.FormName() == "file" {
					g.file, g.fileName = b, p.FileName()
				} else {
					g.fields[p.FormName()] = string(b)
				}
			}
		}
		w.mu.Lock()
		w.got = append(w.got, g)
		status, reply := w.status, w.reply
		w.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(status)
		_, _ = io.WriteString(rw, reply)
	}))
	t.Cleanup(w.srv.Close)
	return w
}

func (w *fakeWorker) calls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n
}

func (w *fakeWorker) answer(status int, reply string) {
	w.mu.Lock()
	w.status, w.reply = status, reply
	w.mu.Unlock()
}

type fakeCheckSink struct {
	mu    sync.Mutex
	calls [][2]string
	err   error
}

func (s *fakeCheckSink) ReadinessCheck(_ context.Context, email, consent string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, [2]string{email, consent})
	return s.err
}

func (s *fakeCheckSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

type checkEnv struct {
	rc   *readinessCheck
	th   *SignInThrottle
	sink *fakeCheckSink
	w    *fakeWorker
	logs *safeBuf
}

func newCheckEnv(t *testing.T, enforce bool) *checkEnv {
	t.Helper()
	e := &checkEnv{sink: &fakeCheckSink{}, w: newFakeWorker(t), logs: &safeBuf{}}
	e.th = NewSignInThrottle("readiness-check-ip", ReadinessCheckPerIP, ReadinessCheckMaxKeys, ReadinessCheckIPWindow, time.Now)
	u, _ := url.Parse(e.w.srv.URL)
	log := slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.rc = newReadinessCheck(u, http.DefaultClient, "gw-token", e.sink, e.th, enforce, platform.PostureLocal, log)
	return e
}

func ipCount(th *SignInThrottle, ip string) int {
	th.mu.Lock()
	defer th.mu.Unlock()
	return th.counts[signInKey(ip)].n
}

type mpPart struct {
	name, file string
	data       []byte
}

var unreadFile = make([]byte, 1<<20)

func okParts(file string, data []byte) []mpPart {
	return []mpPart{{"email", "", []byte("a@x.co")}, {"consent_text", "", []byte(checkConsent)}, {"file", file, data}}
}

// newCheckRequest streams parts through a pipe; consumed turns true only once the last file part was read in full.
func newCheckRequest(t *testing.T, path string, parts ...mpPart) (*http.Request, *atomic.Bool) {
	t.Helper()
	pr, pw := io.Pipe()
	t.Cleanup(func() { pr.Close() })
	mw := multipart.NewWriter(pw)
	consumed := &atomic.Bool{}
	go func() {
		for _, p := range parts {
			var w io.Writer
			var err error
			if p.file != "" {
				w, err = mw.CreateFormFile(p.name, p.file)
			} else {
				w, err = mw.CreateFormField(p.name)
			}
			if err == nil {
				_, err = w.Write(p.data)
			}
			if err != nil {
				return
			}
			if p.file != "" {
				consumed.Store(true)
			}
		}
		pw.CloseWithError(mw.Close())
	}()
	req, err := http.NewRequest(http.MethodPost, path, pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.RemoteAddr = checkRemote
	return req, consumed
}

func (e *checkEnv) run(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	e.rc.run(rec, req)
	return rec
}

func (e *checkEnv) runOK(t *testing.T, ip string) *httptest.ResponseRecorder {
	t.Helper()
	req, _ := newCheckRequest(t, "/readiness-check/run", okParts("inv.csv", []byte("a,b\n1,2\n"))...)
	req.RemoteAddr = ip + ":4000"
	return e.run(t, req)
}

func errOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var b struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	return b.Error
}

func wantRefused(t *testing.T, rec *httptest.ResponseRecorder, status int, msg string) {
	t.Helper()
	if rec.Code != status || (msg != "" && errOf(t, rec) != msg) {
		t.Fatalf("answer = %d %q, want %d %q", rec.Code, rec.Body.String(), status, msg)
	}
}

func TestRun_MissingConsentIsRefusedBeforeTheFile(t *testing.T) {
	e := newCheckEnv(t, true)
	req, consumed := newCheckRequest(t, "/readiness-check/run", mpPart{"email", "", []byte("a@x.co")}, mpPart{"file", "inv.csv", unreadFile})
	rec := e.run(t, req)
	wantRefused(t, rec, 400, checkConsentSentence)
	if consumed.Load() || ipCount(e.th, checkIP) != 0 || e.w.calls() != 0 {
		t.Errorf("file read %v, ip count %d, worker calls %d; want none", consumed.Load(), ipCount(e.th, checkIP), e.w.calls())
	}
}

func TestRun_InvalidEmailIsRefusedBeforeTheFile(t *testing.T) {
	e := newCheckEnv(t, true)
	req, consumed := newCheckRequest(t, "/readiness-check/run", mpPart{"email", "", []byte("nope")}, mpPart{"consent_text", "", []byte(checkConsent)}, mpPart{"file", "inv.csv", unreadFile})
	wantRefused(t, e.run(t, req), 400, "email is invalid")
	if consumed.Load() || ipCount(e.th, checkIP) != 0 {
		t.Errorf("file read %v, ip count %d; want none", consumed.Load(), ipCount(e.th, checkIP))
	}
}

func TestRun_ConsentOver500CharactersIsRefused(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want int
	}{{501, 400}, {500, 200}} {
		e := newCheckEnv(t, true)
		text := strings.Repeat("é", tc.n)
		req, _ := newCheckRequest(t, "/readiness-check/run", mpPart{"email", "", []byte("a@x.co")}, mpPart{"consent_text", "", []byte(text)}, mpPart{"file", "inv.csv", []byte("a\n1\n")})
		if rec := e.run(t, req); rec.Code != tc.want {
			t.Errorf("consent of %d characters = %d, want %d", tc.n, rec.Code, tc.want)
		}
	}
}

func TestRun_FileBeforeEmailIsRefusedUnread(t *testing.T) {
	e := newCheckEnv(t, true)
	req, consumed := newCheckRequest(t, "/readiness-check/run", mpPart{"file", "inv.csv", unreadFile},
		mpPart{"email", "", []byte("a@x.co")}, mpPart{"consent_text", "", []byte(checkConsent)})
	wantRefused(t, e.run(t, req), 400, "invalid request body")
	if consumed.Load() || ipCount(e.th, checkIP) != 0 || e.w.calls() != 0 {
		t.Errorf("file read %v, ip count %d, worker calls %d; want none", consumed.Load(), ipCount(e.th, checkIP), e.w.calls())
	}
}

func TestRun_MalformedMappingOrHeaderRowIs400(t *testing.T) {
	for _, extra := range []mpPart{{"header_row", "", []byte("2a")}, {"mapping", "", []byte("[1]")}, {"mapping", "", []byte(`{"a":1}`)}} {
		e := newCheckEnv(t, true)
		parts := append([]mpPart{{"email", "", []byte("a@x.co")}, {"consent_text", "", []byte(checkConsent)}, extra}, mpPart{"file", "inv.csv", unreadFile})
		req, consumed := newCheckRequest(t, "/readiness-check/run", parts...)
		wantRefused(t, e.run(t, req), 400, "invalid request body")
		if consumed.Load() || ipCount(e.th, checkIP) != 0 {
			t.Errorf("%s %q: file read %v, ip count %d; want none", extra.name, extra.data, consumed.Load(), ipCount(e.th, checkIP))
		}
	}
}

func TestRun_EleventhRunFromOneIPIs429(t *testing.T) {
	e := newCheckEnv(t, true)
	for i := range ReadinessCheckPerIP {
		if rec := e.runOK(t, checkIP); rec.Code != 200 {
			t.Fatalf("run %d = %d %s, want 200", i+1, rec.Code, rec.Body)
		}
	}
	wantRefused(t, e.runOK(t, checkIP), 429, checkIPSentence)
	if e.w.calls() != ReadinessCheckPerIP {
		t.Errorf("worker calls = %d, want %d", e.w.calls(), ReadinessCheckPerIP)
	}
}

func TestRun_PreviewLogsTheIPLimitAndAdmits(t *testing.T) {
	e := newCheckEnv(t, false)
	for range ReadinessCheckPerIP {
		e.runOK(t, checkIP)
	}
	if rec := e.runOK(t, checkIP); rec.Code != 200 {
		t.Fatalf("11th run = %d, want 200 on a preview", rec.Code)
	}
	logs := e.logs.String()
	if n := strings.Count(logs, "limit=ip"); n != 1 || !strings.Contains(logs, "enforced=false") || !strings.Contains(logs, "level=WARN") {
		t.Errorf("want one WARN with limit=ip enforced=false, got %d in:\n%s", n, logs)
	}
	if strings.Contains(logs, checkIP) {
		t.Errorf("the log carries the IP:\n%s", logs)
	}
}

func TestRun_NoCapOnAllRuns(t *testing.T) {
	e := newCheckEnv(t, true)
	for i := range 12 {
		if rec := e.runOK(t, "203.0.113."+string(rune('1'+i%9))+string(rune('0'+i/9))); rec.Code != 200 {
			t.Fatalf("run %d = %d, want 200", i+1, rec.Code)
		}
	}
}

func TestRun_SharedCountReadFailureIs503(t *testing.T) {
	e := newCheckEnv(t, true)
	pool, err := pgxpool.New(context.Background(), "postgres://u@127.0.0.1:1/x")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	e.th.Shared(pool, slog.New(slog.DiscardHandler))
	wantRefused(t, e.runOK(t, checkIP), 503, checkUnavailableSentence)
	if e.w.calls() != 0 {
		t.Errorf("worker calls = %d, want 0", e.w.calls())
	}
}

func TestRun_RefundsTheSlotWhenTheEngineDidNotStart(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		reply  string
		down   bool
	}{
		{"409", 409, `{"error":"sign_up"}`, false},
		{"400", 400, `{"error":"m"}`, false},
		{"413", 413, `{"error":"too large"}`, false},
		{"503 busy", 503, `{"error":"busy"}`, false},
		{"503 unavailable", 503, `{"error":"unavailable"}`, false},
		{"connection refused", 0, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newCheckEnv(t, true)
			e.w.answer(tc.status, tc.reply)
			if tc.down {
				e.w.srv.Close()
			}
			e.runOK(t, checkIP)
			if n := ipCount(e.th, checkIP); n != 0 {
				t.Errorf("ip count = %d, want 0: the engine did not start", n)
			}
		})
	}
}

func TestRun_KeepsTheSlotWhenTheEngineStarted(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		reply    string
		deadline bool
	}{
		{"200", 200, `{"checks_left":1}`, false},
		{"422", 422, `{"error":"unreadable"}`, false},
		{"503 failed", 503, `{"error":"failed"}`, false},
		{"500", 500, `{"error":"internal error"}`, false},
		{"deadline", 0, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newCheckEnv(t, true)
			e.w.answer(tc.status, tc.reply)
			if tc.deadline {
				e.rc.runTimeout = 100 * time.Millisecond
				e.w.onRequest = waitForClientToGo
			}
			e.runOK(t, checkIP)
			if n := ipCount(e.th, checkIP); n != 1 {
				t.Errorf("ip count = %d, want 1: the engine started", n)
			}
		})
	}
}

func TestRun_SentHandsTheContactOffOnce(t *testing.T) {
	e := newCheckEnv(t, true)
	req, _ := newCheckRequest(t, "/readiness-check/run", mpPart{"email", "", []byte("  A@x.co ")}, mpPart{"consent_text", "", []byte(checkConsent)}, mpPart{"file", "inv.csv", []byte("a\n1\n")})
	rec := e.run(t, req)
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || got["status"] != "sent" || got["checks_left"] != float64(2) {
		t.Fatalf("answer = %d %s, want 200 sent with checks_left 2", rec.Code, rec.Body)
	}
	if len(e.sink.calls) != 1 || e.sink.calls[0] != [2]string{"A@x.co", checkConsent} {
		t.Errorf("sink calls = %v, want one with the trimmed email and the consent text", e.sink.calls)
	}
}

type holdSink struct {
	entered chan struct{}
	release chan struct{}
	ctxErr  chan error
	calls   atomic.Int32
}

func (s *holdSink) ReadinessCheck(ctx context.Context, _, _ string) error {
	s.calls.Add(1)
	s.entered <- struct{}{}
	select {
	case <-s.release:
	case <-ctx.Done():
	}
	s.ctxErr <- ctx.Err()
	return nil
}

func TestRun_HandOffSurvivesAClientCancel(t *testing.T) {
	e := newCheckEnv(t, true)
	hs := &holdSink{entered: make(chan struct{}, 2), release: make(chan struct{}), ctxErr: make(chan error, 2)}
	e.rc.sink = hs
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := newCheckRequest(t, "/readiness-check/run", okParts("inv.csv", []byte("a\n1\n"))...)
	done := make(chan struct{})
	go func() { e.run(t, req.WithContext(ctx)); close(done) }()
	select {
	case <-hs.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the sink was never called")
	}
	cancel()
	close(hs.release)
	if err := <-hs.ctxErr; err != nil {
		t.Errorf("the hand-off context ended with the request: %v", err)
	}
	<-done
	if hs.calls.Load() != 1 {
		t.Errorf("sink calls = %d, want 1", hs.calls.Load())
	}
}

func TestRun_FailedHandOffStillAnswers200(t *testing.T) {
	e := newCheckEnv(t, true)
	e.sink.err = sinkStatusError(500)
	if rec := e.runOK(t, checkIP); rec.Code != 200 {
		t.Fatalf("answer = %d, want 200", rec.Code)
	}
	logs := e.logs.String()
	if strings.Count(logs, "level=WARN") != 1 || !strings.Contains(logs, "status=500") || strings.Contains(logs, "a@x.co") {
		t.Errorf("want one WARN with status=500 and no address:\n%s", logs)
	}
}

func TestRun_RefusedRunHandsNothingOff(t *testing.T) {
	e := newCheckEnv(t, true)
	e.w.answer(409, `{"error":"sign_up"}`)
	e.runOK(t, checkIP)
	if e.sink.count() != 0 {
		t.Errorf("sink calls = %d, want 0", e.sink.count())
	}
}

func TestReadinessCheckSink_PostsToTheIntake(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotToken string
	var gotBody map[string]string
	status := http.StatusAccepted
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotPath, gotToken = r.URL.Path, r.Header.Get("X-Gateway-Token")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	sink := NewHTTPReadinessCheckSink(u, http.DefaultClient, "tok")
	if err := sink.ReadinessCheck(context.Background(), "a@x.co", "text"); err != nil {
		t.Fatalf("202 = %v, want nil", err)
	}
	mu.Lock()
	if gotPath != "/internal/contacts/readiness-checks" || gotToken != "tok" || gotBody["email"] != "a@x.co" || gotBody["marketing_consent_text"] != "text" || len(gotBody) != 2 {
		t.Errorf("posted %q token %q body %v", gotPath, gotToken, gotBody)
	}
	status = http.StatusBadRequest
	mu.Unlock()
	if err := sink.ReadinessCheck(context.Background(), "a@x.co", "text"); err == nil {
		t.Error("400 = nil, want an error")
	}
}

func TestRun_WorkerAnswersMapToVisitorStatuses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		reply    string
		deadline bool
		want     int
		msg      string
	}{
		{"409", 409, `{"error":"sign_up"}`, false, 409, checkSignUpSentence},
		{"422", 422, `{"error":"unreadable"}`, false, 422, checkUnreadableSentence},
		{"400", 400, `{"error":"m"}`, false, 400, "m"},
		{"503", 503, `{"error":"failed"}`, false, 503, checkUnavailableSentence},
		{"418", 418, ``, false, 502, checkUnavailableSentence},
		{"timeout", 0, ``, true, 502, checkUnavailableSentence},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newCheckEnv(t, true)
			e.w.answer(tc.status, tc.reply)
			if tc.deadline {
				e.rc.runTimeout = 100 * time.Millisecond
				e.w.onRequest = waitForClientToGo
			}
			wantRefused(t, e.runOK(t, checkIP), tc.want, tc.msg)
		})
	}
}

func TestRun_WorkerOKWithoutChecksLeftIs502(t *testing.T) {
	e := newCheckEnv(t, true)
	e.w.answer(200, `{}`)
	wantRefused(t, e.runOK(t, checkIP), 502, checkUnavailableSentence)
	if n := ipCount(e.th, checkIP); n != 1 || e.sink.count() != 1 {
		t.Errorf("ip count %d, sink calls %d, want 1 and 1", n, e.sink.count())
	}
}

func TestRun_OversizeFileIs413WithoutAWorkerCall(t *testing.T) {
	e := newCheckEnv(t, true)
	req, _ := newCheckRequest(t, "/readiness-check/run", okParts("inv.csv", make([]byte, 15<<20+1))...)
	wantRefused(t, e.run(t, req), 413, checkTooLargeSentence)
	if e.w.calls() != 0 || ipCount(e.th, checkIP) != 0 {
		t.Errorf("worker calls %d, ip count %d, want 0 and 0", e.w.calls(), ipCount(e.th, checkIP))
	}
}

func TestRun_UnknownExtensionIs400(t *testing.T) {
	e := newCheckEnv(t, true)
	req, _ := newCheckRequest(t, "/readiness-check/run", okParts("invoice.png", []byte("x"))...)
	wantRefused(t, e.run(t, req), 400, checkFormatSentence)
	if e.w.calls() != 0 {
		t.Error("the worker was called")
	}
}

func TestRun_ForwardsTheFieldsAndAnIgnoredClientCancel(t *testing.T) {
	e := newCheckEnv(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.w.onRequest = func(*http.Request) { cancel() }
	body := []byte("PK-binary\x00bytes")
	parts := []mpPart{{"email", "", []byte("a@x.co")}, {"consent_text", "", []byte(checkConsent)},
		{"header_row", "", []byte("3")}, {"mapping", "", []byte(`{"invoice_number":"No"}`)}, {"file", "secret.xlsx", body}}
	req, _ := newCheckRequest(t, "/readiness-check/run", parts...)
	if rec := e.run(t, req.WithContext(ctx)); rec.Code != 200 {
		t.Fatalf("answer = %d %s, want 200", rec.Code, rec.Body)
	}
	g := e.w.got[0]
	want := map[string]string{"email": "a@x.co", "format": "xlsx", "header_row": "3", "mapping": `{"invoice_number":"No"}`}
	if len(g.fields) != len(want) {
		t.Errorf("fields = %v, want %v", g.fields, want)
	}
	for k, v := range want {
		if g.fields[k] != v {
			t.Errorf("field %s = %q, want %q", k, g.fields[k], v)
		}
	}
	if !bytes.Equal(g.file, body) || strings.Contains(g.fileName, "secret") {
		t.Errorf("file %q named %q, want the bytes under a name without the visitor's", g.file, g.fileName)
	}
}

func TestMapping_ForwardsAndRelaysTheSuggestion(t *testing.T) {
	e := newCheckEnv(t, true)
	const reply = `{"source":"heuristic","header_row":1,"columns":["a"],"sample_rows":[["1"]],"rows_total":1,"mapping":{}}`
	e.w.answer(200, reply)
	req, _ := newCheckRequest(t, "/readiness-check/mapping", mpPart{"file", "inv.csv", []byte("a\n1\n")})
	rec := httptest.NewRecorder()
	e.rc.mapping(rec, req)
	g := e.w.got[0]
	if rec.Code != 200 || rec.Body.String() != reply || g.fields["format"] != "csv" || string(g.file) != "a\n1\n" {
		t.Fatalf("answer %d %q; worker got %v %q", rec.Code, rec.Body, g.fields, g.file)
	}
	req, _ = newCheckRequest(t, "/readiness-check/mapping", mpPart{"file", "inv.pdf", []byte("x")})
	rec = httptest.NewRecorder()
	e.rc.mapping(rec, req)
	wantRefused(t, rec, 400, checkSheetSentence)
	if e.w.calls() != 1 {
		t.Errorf("worker calls = %d, want 1", e.w.calls())
	}
}

func TestReadinessCheck_LogsNoPersonalData(t *testing.T) {
	const addr, file = "pii@x.io", "secret.csv"
	e := newCheckEnv(t, true)
	post := func(parts ...mpPart) {
		req, _ := newCheckRequest(t, "/readiness-check/run", parts...)
		e.run(t, req)
	}
	full := []mpPart{{"email", "", []byte(addr)}, {"consent_text", "", []byte(checkConsent)}, {"file", file, []byte("a\n1\n")}}
	post(full...)
	e.sink.err = sinkStatusError(500)
	post(full...)
	e.w.answer(200, `{}`)
	post(full...)
	e.w.answer(418, ``)
	post(full...)
	e.w.srv.Close()
	post(full...)
	post(mpPart{"email", "", []byte("nope")}, full[1], full[2])
	post(full[0], full[1], mpPart{"file", "secret.png", []byte("x")})
	for range ReadinessCheckPerIP {
		post(full...)
	}
	req, _ := newCheckRequest(t, "/readiness-check/mapping", mpPart{"file", file, []byte("a\n")})
	e.rc.mapping(httptest.NewRecorder(), req)
	logs := e.logs.String()
	if logs == "" {
		t.Fatal("no log lines: the scan went blind")
	}
	for _, s := range []string{addr, checkIP, file, "secret"} {
		if strings.Contains(logs, s) {
			t.Errorf("the log carries %q:\n%s", s, logs)
		}
	}
}

func TestReadinessCheck_NeverParsesAMultipartForm(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "readiness_check.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	banned := map[string]bool{"FormFile": true, "FormValue": true, "PostFormValue": true, "ParseMultipartForm": true}
	reads := 0
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if banned[sel.Sel.Name] {
				t.Errorf("readiness_check.go uses %s: the file would spool to disk", sel.Sel.Name)
			}
			if sel.Sel.Name == "MultipartReader" {
				reads++
			}
		}
		return true
	})
	if reads == 0 {
		t.Error("control: no MultipartReader call; the scan went blind")
	}
}

// waitForClientToGo drains the body first: the server notices a closed connection only after that.
func waitForClientToGo(r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	<-r.Context().Done()
}

func holdWorker(e *checkEnv) (entered chan struct{}, release func()) {
	entered = make(chan struct{}, 8)
	gate := make(chan struct{})
	var once sync.Once
	e.w.onRequest = func(*http.Request) { entered <- struct{}{}; <-gate }
	return entered, func() { once.Do(func() { close(gate) }) }
}

// heldUploads is the documented limit of 4 uploads in flight; a literal, so that raising maxCheckUploads fails the busy tests.
const heldUploads = 4

// holdFour starts four runs and waits until all are inside the worker.
func holdFour(t *testing.T, e *checkEnv) (finish func() []int) {
	t.Helper()
	entered, release := holdWorker(e)
	t.Cleanup(release)
	codes := make(chan int, heldUploads)
	for range heldUploads {
		go func() { codes <- e.runOK(t, checkIP).Code }()
	}
	for range heldUploads {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("four runs never reached the worker")
		}
	}
	return func() []int {
		release()
		var out []int
		for range heldUploads {
			out = append(out, <-codes)
		}
		return out
	}
}

func TestRun_FifthConcurrentUploadIsBusy(t *testing.T) {
	e := newCheckEnv(t, true)
	finish := holdFour(t, e)
	req, consumed := newCheckRequest(t, "/readiness-check/run", okParts("inv.csv", unreadFile)...)
	wantRefused(t, e.run(t, req), 503, checkUnavailableSentence)
	if consumed.Load() || ipCount(e.th, checkIP) != heldUploads {
		t.Errorf("file read %v, ip count %d, want unread and %d", consumed.Load(), ipCount(e.th, checkIP), heldUploads)
	}
	for _, c := range finish() {
		if c != 200 {
			t.Errorf("held run = %d, want 200", c)
		}
	}
}

func TestMapping_SharesTheUploadCap(t *testing.T) {
	e := newCheckEnv(t, true)
	finish := holdFour(t, e)
	before := e.w.calls()
	req, _ := newCheckRequest(t, "/readiness-check/mapping", mpPart{"file", "inv.csv", []byte("a\n")})
	rec := httptest.NewRecorder()
	e.rc.mapping(rec, req)
	wantRefused(t, rec, 503, checkUnavailableSentence)
	if e.w.calls() != before {
		t.Error("the mapping call reached the worker")
	}
	finish()
}

func liveHeap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// Not parallel: the heap reading counts the whole process.
func TestRun_ForwardHoldsOneCopyOfTheFile(t *testing.T) {
	const size = 14 << 20 // above what the loopback socket buffers absorb, so the gateway still holds its copy at the hook
	e := newCheckEnv(t, true)
	data := bytes.Repeat([]byte("a,b\n1,2\n"), size/8)
	baseline := int64(liveHeap())
	var delta int64
	var cl int64
	var te []string
	e.w.onRequest = func(r *http.Request) {
		cl, te = r.ContentLength, r.TransferEncoding
		delta = int64(liveHeap()) - baseline
	}
	req, _ := newCheckRequest(t, "/readiness-check/run", okParts("big.csv", data)...)
	rec := e.run(t, req)
	runtime.KeepAlive(data) // else the baseline's copy is freed mid-run and hides the gateway's
	if rec.Code != 200 {
		t.Fatalf("answer = %d %s, want 200", rec.Code, rec.Body)
	}
	if cl != -1 || len(te) != 1 || te[0] != "chunked" {
		t.Errorf("ContentLength %d TransferEncoding %v, want -1 and [chunked]", cl, te)
	}
	if limit := int64(size + size*3/5); delta >= limit {
		t.Errorf("heap grew %d bytes before the worker read the body, want under %d: the forward holds more than one copy", delta, limit)
	}
	if got := len(e.w.got[0].file); got != size {
		t.Errorf("worker read %d file bytes, want %d", got, size)
	}
}

// A caller with no trustworthy address counts under one shared key, so it is still limited.
func TestRun_UnattributableClientIsStillLimited(t *testing.T) {
	e := newCheckEnv(t, true)
	for i := range ReadinessCheckPerIP {
		if rec := e.runOK(t, "not-an-ip"); rec.Code != 200 {
			t.Fatalf("run %d = %d %s, want 200", i+1, rec.Code, rec.Body)
		}
	}
	wantRefused(t, e.runOK(t, "not-an-ip"), 429, checkIPSentence)
	if e.w.calls() != ReadinessCheckPerIP {
		t.Errorf("worker calls = %d, want %d", e.w.calls(), ReadinessCheckPerIP)
	}
}

// validMapping is a JSON object of exactly n bytes.
func validMapping(n int) []byte {
	return append(append([]byte(`{"a":"`), bytes.Repeat([]byte("b"), n-len(`{"a":""}`))...), `"}`...)
}

func TestRun_BadPartsAreRefusedBeforeTheFile(t *testing.T) {
	email, consent := mpPart{"email", "", []byte("a@x.co")}, mpPart{"consent_text", "", []byte(checkConsent)}
	for _, tc := range []struct {
		name  string
		parts []mpPart
	}{
		{"repeated email", []mpPart{email, email, consent}},
		{"unknown part", []mpPart{email, consent, {"x", "", []byte("1")}}},
		{"email over its cap", []mpPart{{"email", "", bytes.Repeat([]byte("a"), 1025)}, consent}},
		{"consent over its cap", []mpPart{email, {"consent_text", "", bytes.Repeat([]byte("a"), 4097)}}},
		{"mapping over its cap", []mpPart{email, consent, {"mapping", "", validMapping(16<<10 + 1)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newCheckEnv(t, true)
			req, consumed := newCheckRequest(t, "/readiness-check/run", append(tc.parts, mpPart{"file", "inv.csv", unreadFile})...)
			wantRefused(t, e.run(t, req), 400, "invalid request body")
			if consumed.Load() || ipCount(e.th, checkIP) != 0 || e.w.calls() != 0 {
				t.Errorf("file read %v, ip count %d, worker calls %d; want none", consumed.Load(), ipCount(e.th, checkIP), e.w.calls())
			}
		})
	}
	t.Run("no file part", func(t *testing.T) {
		e := newCheckEnv(t, true)
		req, _ := newCheckRequest(t, "/readiness-check/run", email, consent)
		wantRefused(t, e.run(t, req), 400, "invalid request body")
		if e.w.calls() != 0 {
			t.Error("the worker was called")
		}
	})
}

func TestRun_AFileOfExactlyTheCapIsAccepted(t *testing.T) {
	e := newCheckEnv(t, true)
	req, _ := newCheckRequest(t, "/readiness-check/run", okParts("inv.csv", make([]byte, MaxCheckFileBytes))...)
	if rec := e.run(t, req); rec.Code != 200 {
		t.Fatalf("answer = %d %s, want 200 for a file of exactly 15 MiB", rec.Code, rec.Body)
	}
	if len(e.w.got) != 1 || len(e.w.got[0].file) != MaxCheckFileBytes {
		t.Errorf("worker got %d calls, want 1 with the whole file", len(e.w.got))
	}
}

func TestRun_AMappingOfExactlyTheCapIsAccepted(t *testing.T) {
	e := newCheckEnv(t, true)
	mapping := append([]byte(`{"a":"`), bytes.Repeat([]byte("b"), 16<<10-len(`{"a":""}`))...)
	mapping = append(mapping, `"}`...)
	if len(mapping) != 16<<10 {
		t.Fatalf("mapping is %d bytes, want 16 KiB", len(mapping))
	}
	parts := append([]mpPart{{"mapping", "", mapping}}, okParts("inv.csv", []byte("a,b\n1,2\n"))...)
	req, _ := newCheckRequest(t, "/readiness-check/run", parts...)
	if rec := e.run(t, req); rec.Code != 200 {
		t.Fatalf("answer = %d %s, want 200 for a mapping of exactly 16 KiB", rec.Code, rec.Body)
	}
}

func TestMapping_RefusesATextPartAndAnOversizeFile(t *testing.T) {
	for _, tc := range []struct {
		name   string
		parts  []mpPart
		status int
		msg    string
	}{
		{"a text part before the file", []mpPart{{"email", "", []byte("a@x.co")}, {"file", "inv.csv", []byte("a\n1\n")}}, 400, "invalid request body"},
		{"a file over 15 MiB", []mpPart{{"file", "inv.csv", make([]byte, 15<<20+1)}}, 413, checkTooLargeSentence},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newCheckEnv(t, true)
			req, _ := newCheckRequest(t, "/readiness-check/mapping", tc.parts...)
			rec := httptest.NewRecorder()
			e.rc.mapping(rec, req)
			wantRefused(t, rec, tc.status, tc.msg)
			if e.w.calls() != 0 {
				t.Error("the worker was called")
			}
		})
	}
}

func TestRun_ConsentWithNULAndAnEmailWithASpaceAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name, email, consent, want string
	}{
		{"NUL in the consent", "a@x.co", "I agree\x00", checkConsentSentence},
		{"blank consent", "a@x.co", "   ", checkConsentSentence},
		{"space inside the email", "a b@x.co", checkConsent, "email is invalid"},
		{"two at signs", "a@b@x.co", checkConsent, "email is invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newCheckEnv(t, true)
			req, consumed := newCheckRequest(t, "/readiness-check/run", mpPart{"email", "", []byte(tc.email)},
				mpPart{"consent_text", "", []byte(tc.consent)}, mpPart{"file", "inv.csv", unreadFile})
			wantRefused(t, e.run(t, req), 400, tc.want)
			if consumed.Load() || ipCount(e.th, checkIP) != 0 || e.w.calls() != 0 {
				t.Errorf("file read %v, ip count %d, worker calls %d; want none", consumed.Load(), ipCount(e.th, checkIP), e.w.calls())
			}
		})
	}
}

func TestRun_WorkerOKWithAnInvalidChecksLeftIs502(t *testing.T) {
	for _, reply := range []string{`{"checks_left":3}`, `{"checks_left":-1}`, `{"checks_left":"2"}`, `not json`} {
		e := newCheckEnv(t, true)
		e.w.answer(200, reply)
		wantRefused(t, e.runOK(t, checkIP), 502, checkUnavailableSentence)
		if e.sink.count() != 1 {
			t.Errorf("reply %s: sink calls = %d, want 1: the mail went", reply, e.sink.count())
		}
	}
}

func TestRun_FormatComesFromTheLowercasedExtension(t *testing.T) {
	for file, want := range map[string]string{"INV.CSV": "csv", "a.b.Xlsx": "xlsx", "scan.PDF": "pdf", "note.docx": "docx"} {
		e := newCheckEnv(t, true)
		req, _ := newCheckRequest(t, "/readiness-check/run", okParts(file, []byte("a\n1\n"))...)
		if rec := e.run(t, req); rec.Code != 200 {
			t.Fatalf("%s = %d %s, want 200", file, rec.Code, rec.Body)
		}
		if len(e.w.got) != 1 || e.w.got[0].fields["format"] != want {
			t.Errorf("%s: worker got %+v, want format %s", file, e.w.got, want)
		}
	}
}

func TestMapping_AcceptsAnXlsxAndRefusesADocx(t *testing.T) {
	e := newCheckEnv(t, true)
	e.w.answer(200, `{"columns":[]}`)
	for file, want := range map[string]int{"inv.xlsx": 200, "inv.XLSX": 200, "inv.docx": 400} {
		req, _ := newCheckRequest(t, "/readiness-check/mapping", mpPart{"file", file, []byte("x")})
		rec := httptest.NewRecorder()
		e.rc.mapping(rec, req)
		if rec.Code != want {
			t.Errorf("%s = %d, want %d", file, rec.Code, want)
		}
	}
	if e.w.calls() != 2 || e.w.got[0].fields["format"] != "xlsx" {
		t.Errorf("worker calls = %d (first format %q), want 2 xlsx calls", e.w.calls(), e.w.got[0].fields["format"])
	}
}

type checkRoundTrip func(*http.Request) (*http.Response, error)

func (f checkRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The real socket buffers can absorb the whole body before the worker looks, so this one stops the send:
// nothing reads the body until the heap is read, and the gateway still holds what it will send.
func TestRun_ForwardHoldsOneCopyOfTheFileUntilTheSend(t *testing.T) {
	const size = 8 << 20
	e := newCheckEnv(t, true)
	data := bytes.Repeat([]byte("a,b\n1,2\n"), size/8)
	baseline := int64(liveHeap())
	var delta, cl int64
	e.rc.client = &http.Client{Transport: checkRoundTrip(func(r *http.Request) (*http.Response, error) {
		cl = r.ContentLength
		delta = int64(liveHeap()) - baseline
		n, _ := io.Copy(io.Discard, r.Body)
		if n < size {
			t.Errorf("the forward carried %d bytes, want the %d of the file and more", n, size)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"checks_left":2}`)), Header: http.Header{}, Request: r}, nil
	})}
	req, _ := newCheckRequest(t, "/readiness-check/run", okParts("big.csv", data)...)
	rec := e.run(t, req)
	runtime.KeepAlive(data)
	if rec.Code != 200 {
		t.Fatalf("answer = %d %s, want 200", rec.Code, rec.Body)
	}
	if cl != -1 {
		t.Errorf("ContentLength = %d, want -1: the body is streamed", cl)
	}
	if limit := int64(size + size*3/5); delta < size/2 || delta >= limit {
		t.Errorf("heap grew %d bytes before the send, want from %d to %d: one copy of the file, no more", delta, size/2, limit)
	}
}
