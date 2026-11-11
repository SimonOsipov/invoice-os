package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/readinesscheck"
)

const (
	checkIP      = "203.0.113.7"
	checkRemote  = checkIP + ":4000"
	checkConsent = checkConsentText
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
	w := &fakeWorker{status: http.StatusOK, reply: `{"status":"confirm"}`}
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

type checkEnv struct {
	rc   *readinessCheck
	th   *SignInThrottle
	w    *fakeWorker
	logs *safeBuf
}

func newCheckEnv(t *testing.T, enforce bool) *checkEnv {
	t.Helper()
	e := &checkEnv{w: newFakeWorker(t), logs: &safeBuf{}}
	e.th = NewSignInThrottle("readiness-check-ip", ReadinessCheckPerIP, ReadinessCheckMaxKeys, ReadinessCheckIPWindow, time.Now)
	u, _ := url.Parse(e.w.srv.URL)
	log := slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.rc = newReadinessCheck(u, http.DefaultClient, "gw-token", e.th, enforce, platform.PostureLocal, log)
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
	req.RemoteAddr = net.JoinHostPort(ip, "4000")
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

func TestRun_OnlyTheCurrentConsentSentenceIsTaken(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       int
	}{
		{"current sentence", checkConsentText, 200},
		{"sentence of an earlier deploy", "Email me the results of this check. I agree that ASComply Africa keeps my email address and may contact me about the check.", 400},
		{"arbitrary string", "I agree to the check terms.", 400},
		{"current sentence with a trailing space", checkConsentText + " ", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newCheckEnv(t, true)
			req, consumed := newCheckRequest(t, "/readiness-check/run", mpPart{"email", "", []byte("a@x.co")}, mpPart{"consent_text", "", []byte(tc.text)}, mpPart{"file", "inv.csv", []byte("a\n1\n")})
			rec := e.run(t, req)
			if rec.Code != tc.want {
				t.Fatalf("answer = %d %s, want %d", rec.Code, rec.Body, tc.want)
			}
			if tc.want == 400 {
				wantRefused(t, rec, 400, checkConsentSentence)
				if consumed.Load() || ipCount(e.th, checkIP) != 0 || e.w.calls() != 0 {
					t.Errorf("file read %v, ip count %d, worker calls %d; want none", consumed.Load(), ipCount(e.th, checkIP), e.w.calls())
				}
			}
		})
	}
}

// The sentence is read from the landing source: a change on either side fails here.
func TestCheckConsentText_EqualsTheLandingSentence(t *testing.T) {
	src, err := os.ReadFile("../../frontend/landing/src/readinessConsent.ts")
	if err != nil {
		t.Fatalf("read the landing sentence: %v", err)
	}
	m := regexp.MustCompile(`(?m)^export const READINESS_CONSENT_TEXT =\s*'([^'\\\n]+)'`).FindSubmatch(src)
	if m == nil {
		t.Fatal("readinessConsent.ts: no exported single-quoted READINESS_CONSENT_TEXT literal")
	}
	if string(m[1]) != checkConsentText {
		t.Errorf("landing sentence %q, gateway checkConsentText %q", m[1], checkConsentText)
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

func TestRun_TwoAddressesInOneSlash64ShareTheDailyCount(t *testing.T) {
	e := newCheckEnv(t, true)
	for i := range ReadinessCheckPerIP {
		ip := []string{"2001:db8:1:2::1", "2001:db8:1:2:ffff::9"}[i%2]
		if rec := e.runOK(t, ip); rec.Code != 200 {
			t.Fatalf("run %d = %d %s, want 200", i+1, rec.Code, rec.Body)
		}
	}
	wantRefused(t, e.runOK(t, "2001:db8:1:2::abcd"), 429, checkIPSentence)
	if e.w.calls() != ReadinessCheckPerIP {
		t.Errorf("worker calls = %d, want %d", e.w.calls(), ReadinessCheckPerIP)
	}
	if n := ipCount(e.th, "2001:db8:1:2::/64"); n != ReadinessCheckPerIP {
		t.Errorf("/64 count = %d, want %d", n, ReadinessCheckPerIP)
	}
}

func TestRun_AnotherSlash64HasItsOwnCount(t *testing.T) {
	e := newCheckEnv(t, true)
	for range ReadinessCheckPerIP {
		e.runOK(t, "2001:db8:1:2::1")
	}
	if rec := e.runOK(t, "2001:db8:1:3::1"); rec.Code != 200 {
		t.Fatalf("other /64 run = %d %s, want 200", rec.Code, rec.Body)
	}
}

func TestRun_IPv4KeepsItsExactAddressKey(t *testing.T) {
	e := newCheckEnv(t, true)
	for range ReadinessCheckPerIP {
		e.runOK(t, checkIP)
	}
	if rec := e.runOK(t, "203.0.113.8"); rec.Code != 200 {
		t.Fatalf("neighbouring IPv4 run = %d %s, want 200", rec.Code, rec.Body)
	}
	wantRefused(t, e.runOK(t, checkIP), 429, checkIPSentence)
}

func TestRun_AMappedIPv6AddressCountsAsItsIPv4(t *testing.T) {
	e := newCheckEnv(t, true)
	for range ReadinessCheckPerIP {
		e.runOK(t, checkIP)
	}
	wantRefused(t, e.runOK(t, "::ffff:"+checkIP), 429, checkIPSentence)
}

func TestRun_AZonedIPv6AddressCountsPerSlash64WithoutItsZone(t *testing.T) {
	e := newCheckEnv(t, true)
	for range ReadinessCheckPerIP {
		e.runOK(t, "fe80::1%eth0")
	}
	wantRefused(t, e.runOK(t, "fe80::2%eth1"), 429, checkIPSentence)
}

func TestRun_RefundReturnsTheSlash64Count(t *testing.T) {
	e := newCheckEnv(t, true)
	e.w.answer(400, `{"error":"m"}`)
	e.runOK(t, "2001:db8:1:2::1")
	if n := ipCount(e.th, "2001:db8:1:2::/64"); n != 0 {
		t.Errorf("/64 count = %d, want 0: the engine did not start", n)
	}
}

func TestCheckIPKey(t *testing.T) {
	for in, want := range map[string]string{
		"2001:db8:1:2::1":                  "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff:ffff:ffff:ffff": "2001:db8:1:2::/64",
		"2001:db8:1:3::1":                  "2001:db8:1:3::/64",
		"203.0.113.7":                      "203.0.113.7",
		"":                                 unattributableIPKey,
		"fe80::1%eth0":                     "fe80::/64",
	} {
		if got := checkIPKey(in); got != want {
			t.Errorf("checkIPKey(%q) = %q, want %q", in, got, want)
		}
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
		{"400", 400, `{"error":"m"}`, false},
		{"413", 413, `{"error":"too large"}`, false},
		{"503 busy", 503, `{"error":"busy"}`, false},
		{"503 unavailable", 503, `{"error":"unavailable"}`, false},
		{"503 ceiling", 503, `{"error":"ceiling"}`, false},
		{"400 other word", 400, `{"error":"too long"}`, false},
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

// wantCeilingSentence is the agreed sentence; checkCeilingSentence must equal it.
const wantCeilingSentence = "The free check has reached its limit for today. Try again tomorrow."

func TestRun_WorkerCeilingIsTheCeilingSentence(t *testing.T) {
	if checkCeilingSentence != wantCeilingSentence {
		t.Fatalf("checkCeilingSentence = %q, want %q", checkCeilingSentence, wantCeilingSentence)
	}
	e := newCheckEnv(t, true)
	e.w.answer(503, `{"error":"ceiling"}`)
	wantRefused(t, e.runOK(t, checkIP), 503, wantCeilingSentence)
	if e.w.calls() != 1 {
		t.Errorf("worker calls = %d, want 1: the refusal must come from the worker", e.w.calls())
	}
	if n := ipCount(e.th, checkIP); n != 0 {
		t.Errorf("ip count = %d, want 0: the slot comes back", n)
	}
}

func TestRun_WorkerUnavailableStaysTheUnavailableSentence(t *testing.T) {
	for _, reply := range []string{`{"error":"unavailable"}`, `{"error":"failed"}`, `{"error":"busy"}`, ``} {
		t.Run(reply, func(t *testing.T) {
			e := newCheckEnv(t, true)
			e.w.answer(503, reply)
			wantRefused(t, e.runOK(t, checkIP), 503, checkUnavailableSentence)
		})
	}
}

// The word is the worker's own constant, so a change to the worker's sentence that the gateway does not follow fails here.
func TestRun_KeepsTheSlotForTheTooLongRefusal(t *testing.T) {
	const word = readinesscheck.DocumentTooLongSentence
	e := newCheckEnv(t, true)
	e.w.answer(400, `{"error":"`+word+`"}`)
	wantRefused(t, e.runOK(t, checkIP), 400, word)
	if n := ipCount(e.th, checkIP); n != 1 {
		t.Errorf("ip count = %d, want 1: Docling already read the file", n)
	}
}

func TestRun_KeepsTheSlotWhenTheEngineStarted(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		reply    string
		deadline bool
	}{
		{"200", 200, `{"status":"confirm"}`, false},
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

func TestRun_AnswersConfirm(t *testing.T) {
	e := newCheckEnv(t, true)
	req, _ := newCheckRequest(t, "/readiness-check/run", mpPart{"email", "", []byte("  A@x.co ")}, mpPart{"consent_text", "", []byte(checkConsent)}, mpPart{"file", "inv.csv", []byte("a\n1\n")})
	rec := e.run(t, req)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"status":"confirm"}` {
		t.Fatalf("answer = %d %s, want 200 {\"status\":\"confirm\"}", rec.Code, rec.Body)
	}
	if n := ipCount(e.th, checkIP); n != 1 {
		t.Errorf("ip count = %d, want 1", n)
	}
	if len(e.w.got) != 1 || e.w.got[0].fields["email"] != "A@x.co" {
		t.Errorf("worker got %+v, want one call with the trimmed email", e.w.got)
	}
}

// The visitor must not learn from the answer whether the address had used its checks.
func TestRun_EveryWorker200GetsTheSameAnswer(t *testing.T) {
	var first *httptest.ResponseRecorder
	for _, reply := range []string{`{"status":"confirm"}`, `{"checks_left":2}`, `not json`} {
		e := newCheckEnv(t, true)
		e.w.answer(200, reply)
		rec := e.runOK(t, checkIP)
		if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"status":"confirm"}` {
			t.Fatalf("worker %s: answer = %d %s, want 200 confirm", reply, rec.Code, rec.Body)
		}
		if first == nil {
			first = rec
			continue
		}
		if rec.Body.String() != first.Body.String() || rec.Header().Get("Content-Type") != first.Header().Get("Content-Type") {
			t.Errorf("worker %s: answer %q (%s) differs from %q (%s)", reply, rec.Body, rec.Header().Get("Content-Type"), first.Body, first.Header().Get("Content-Type"))
		}
	}
}

func TestRun_AWorker409IsUnavailable(t *testing.T) {
	e := newCheckEnv(t, true)
	e.w.answer(409, `{"error":"sign_up"}`)
	rec := e.runOK(t, checkIP)
	wantRefused(t, rec, 502, checkUnavailableSentence)
	if strings.Contains(strings.ToLower(rec.Body.String()), "free checks") || strings.Contains(strings.ToLower(rec.Body.String()), "sign up") {
		t.Errorf("answer %s names the used-up state", rec.Body)
	}
	if n := ipCount(e.th, checkIP); n != 1 {
		t.Errorf("ip count = %d, want 1: a stray 409 is not a not-started refusal", n)
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
		{"422", 422, `{"error":"unreadable"}`, false, 422, checkUnreadableSentence},
		{"400", 400, `{"error":"m"}`, false, 400, "m"},
		{"503", 503, `{"error":"failed"}`, false, 503, checkUnavailableSentence},
		{"409", 409, `{"error":"sign_up"}`, false, 502, checkUnavailableSentence},
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

func TestMapping_WorkerCeilingIsTheCeilingSentence(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		want        string
	}{
		{"ceiling", `{"error":"ceiling"}`, wantCeilingSentence},
		{"unavailable", `{"error":"unavailable"}`, checkUnavailableSentence},
		{"failed", `{"error":"failed"}`, checkUnavailableSentence},
		{"no word", ``, checkUnavailableSentence},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newCheckEnv(t, true)
			e.w.answer(503, tc.reply)
			req, _ := newCheckRequest(t, "/readiness-check/mapping", mpPart{"file", "inv.csv", []byte("a\n1\n")})
			rec := httptest.NewRecorder()
			e.rc.mapping(rec, req)
			wantRefused(t, rec, 503, tc.want)
			if n := ipCount(e.th, checkIP); n != 0 {
				t.Errorf("ip count = %d, want 0: /mapping holds no IP slot", n)
			}
		})
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
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":"confirm"}`)), Header: http.Header{}, Request: r}, nil
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

// rawUpload is a hand-written multipart request on a real connection, so a test can stall or trickle it.
type rawUpload struct {
	conn      net.Conn
	body      []byte
	fileStart int // offset of the first file byte
}

// uploadServer closes after the test's connections: Close waits for handlers, so a handler that never returns would hang the test.
func uploadServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func dialUpload(t *testing.T, srv *httptest.Server, path string, parts ...mpPart) *rawUpload {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		var w io.Writer
		if p.file != "" {
			w, _ = mw.CreateFormFile(p.name, p.file)
		} else {
			w, _ = mw.CreateFormField(p.name)
		}
		_, _ = w.Write(p.data)
	}
	_ = mw.Close()
	body := buf.Bytes()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	head := "POST " + path + " HTTP/1.1\r\nHost: check\r\nContent-Type: " + mw.FormDataContentType() +
		"\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n"
	if _, err := io.WriteString(conn, head); err != nil {
		t.Fatal(err)
	}
	start := bytes.Index(body, []byte(`name="file"`))
	start += bytes.Index(body[start:], []byte("\r\n\r\n")) + 4
	return &rawUpload{conn: conn, body: body, fileStart: start}
}

func (u *rawUpload) send(t *testing.T, b []byte) {
	t.Helper()
	if _, err := u.conn.Write(b); err != nil {
		t.Fatal(err)
	}
}

// answer reads the response, waiting at most 3 s.
func (u *rawUpload) answer() (status int, errText string, err error) {
	_ = u.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(u.conn), nil)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var in struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(b, &in)
	return resp.StatusCode, in.Error, nil
}

func (u *rawUpload) wantAnswer(t *testing.T, status int, errText string) {
	t.Helper()
	got, text, err := u.answer()
	if err != nil || got != status || (errText != "" && text != errText) {
		t.Fatalf("answer = %d %q (err %v), want %d %q", got, text, err, status, errText)
	}
}

func waitSlotsFree(t *testing.T, e *checkEnv) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(e.rc.slots) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d slots still held", len(e.rc.slots))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var slowFile = bytes.Repeat([]byte("a,b\n1,2\n"), 8192)

func TestRun_AStalledUploadIs408AndRefunded(t *testing.T) {
	e := newCheckEnv(t, true)
	e.runOK(t, "127.0.0.1")
	calls := e.w.calls()
	e.rc.uploadTimeout = 300 * time.Millisecond
	srv := uploadServer(t, http.HandlerFunc(e.rc.run))
	u := dialUpload(t, srv, "/readiness-check/run", okParts("inv.csv", slowFile)...)
	u.send(t, u.body[:u.fileStart+len(slowFile)/2])
	u.wantAnswer(t, 408, checkSlowUploadSentence)
	waitSlotsFree(t, e)
	if n := ipCount(e.th, "127.0.0.1"); n != 1 || e.w.calls() != calls {
		t.Errorf("ip count %d, new worker calls %d, want 1 (the earlier run only: one refund) and 0", n, e.w.calls()-calls)
	}
}

func TestMapping_AStalledUploadIs408(t *testing.T) {
	e := newCheckEnv(t, true)
	e.runOK(t, "127.0.0.1")
	calls := e.w.calls()
	e.rc.uploadTimeout = 300 * time.Millisecond
	srv := uploadServer(t, http.HandlerFunc(e.rc.mapping))
	u := dialUpload(t, srv, "/readiness-check/mapping", mpPart{"file", "inv.csv", slowFile})
	u.send(t, u.body[:u.fileStart+len(slowFile)/2])
	u.wantAnswer(t, 408, checkSlowUploadSentence)
	waitSlotsFree(t, e)
	if n := ipCount(e.th, "127.0.0.1"); n != 1 || e.w.calls() != calls {
		t.Errorf("ip count %d, new worker calls %d, want 1 (mapping neither counts nor refunds) and 0", n, e.w.calls()-calls)
	}
}

func TestRun_StalledUploadsFreeEverySlot(t *testing.T) {
	e := newCheckEnv(t, true)
	e.rc.uploadTimeout = 300 * time.Millisecond
	srv := uploadServer(t, http.HandlerFunc(e.rc.run))
	var ups []*rawUpload
	for range heldUploads {
		u := dialUpload(t, srv, "/readiness-check/run", okParts("inv.csv", slowFile)...)
		u.send(t, u.body[:u.fileStart+len(slowFile)/2])
		ups = append(ups, u)
	}
	for _, u := range ups {
		u.wantAnswer(t, 408, checkSlowUploadSentence)
	}
	waitSlotsFree(t, e)
	if rec := e.runOK(t, checkIP); rec.Code != 200 {
		t.Fatalf("next visitor = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

func TestRun_TheDeadlineStartsAtTheSlot(t *testing.T) {
	e := newCheckEnv(t, true)
	e.rc.uploadTimeout = 2 * time.Second
	srv := uploadServer(t, http.HandlerFunc(e.rc.run))
	u := dialUpload(t, srv, "/readiness-check/run", okParts("inv.csv", slowFile)...)
	consent := bytes.Index(u.body, []byte(`name="consent_text"`))
	file := bytes.Index(u.body, []byte(`name="file"`))
	u.send(t, u.body[:consent])
	time.Sleep(1500 * time.Millisecond)
	u.send(t, u.body[consent:file])
	time.Sleep(1500 * time.Millisecond)
	u.send(t, u.body[file:])
	u.wantAnswer(t, 200, "")
}

func TestRun_ASlowWorkerAfterAFastUploadStillAnswers200(t *testing.T) {
	e := newCheckEnv(t, true)
	e.rc.uploadTimeout = time.Second
	_, release := holdWorker(e)
	t.Cleanup(release)
	time.AfterFunc(1500*time.Millisecond, release)
	var ctxErr atomic.Value
	srv := uploadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.rc.run(w, r)
		ctxErr.Store(struct{ err error }{r.Context().Err()})
	}))
	u := dialUpload(t, srv, "/readiness-check/run", okParts("inv.csv", slowFile)...)
	u.send(t, u.body)
	u.wantAnswer(t, 200, "")
	if got, _ := ctxErr.Load().(struct{ err error }); got.err != nil {
		t.Errorf("request context after run = %v, want nil", got.err)
	}
}

func TestReadinessCheck_UploadTimeoutIs90Seconds(t *testing.T) {
	e := newCheckEnv(t, true)
	if e.rc.uploadTimeout != 90*time.Second {
		t.Errorf("uploadTimeout = %v, want 90s", e.rc.uploadTimeout)
	}
}

func TestRun_AnUploadStillTricklingAtTheLimitIs408(t *testing.T) {
	e := newCheckEnv(t, true)
	e.rc.uploadTimeout = 300 * time.Millisecond
	srv := uploadServer(t, http.HandlerFunc(e.rc.run))
	u := dialUpload(t, srv, "/readiness-check/run", okParts("inv.csv", make([]byte, 1<<20))...)
	u.send(t, u.body[:u.fileStart])
	type answer struct {
		status int
		text   string
		err    error
	}
	got := make(chan answer, 1)
	go func() {
		s, text, err := u.answer()
		got <- answer{s, text, err}
	}()
	chunk := make([]byte, 1024)
	for sent, stop := 0, time.After(5*time.Second); sent < len(u.body)-u.fileStart; sent += len(chunk) {
		select {
		case a := <-got:
			if a.err != nil || a.status != 408 || a.text != checkSlowUploadSentence {
				t.Fatalf("answer = %d %q (err %v), want 408", a.status, a.text, a.err)
			}
			waitSlotsFree(t, e)
			if n := ipCount(e.th, "127.0.0.1"); n != 0 || e.w.calls() != 0 {
				t.Errorf("ip count %d, worker calls %d, want 0 and 0", n, e.w.calls())
			}
			return
		case <-stop:
			t.Fatal("no answer within 5 s of trickling")
		case <-time.After(50 * time.Millisecond):
		}
		if _, err := u.conn.Write(chunk); err != nil {
			break
		}
	}
	a := <-got
	if a.err != nil || a.status != 408 || a.text != checkSlowUploadSentence {
		t.Fatalf("answer = %d %q (err %v), want 408", a.status, a.text, a.err)
	}
	waitSlotsFree(t, e)
	if n := ipCount(e.th, "127.0.0.1"); n != 0 || e.w.calls() != 0 {
		t.Errorf("ip count %d, worker calls %d, want 0 and 0", n, e.w.calls())
	}
}

func TestRun_ASlowUploadThatFinishesInTimeIs200(t *testing.T) {
	e := newCheckEnv(t, true)
	e.rc.uploadTimeout = 3 * time.Second
	srv := uploadServer(t, http.HandlerFunc(e.rc.run))
	u := dialUpload(t, srv, "/readiness-check/run", okParts("inv.csv", slowFile)...)
	piece := (len(u.body) - u.fileStart) / 5
	u.send(t, u.body[:u.fileStart])
	for i := range 5 {
		end := u.fileStart + (i+1)*piece
		if i == 4 {
			end = len(u.body)
		}
		u.send(t, u.body[u.fileStart+i*piece:end])
		time.Sleep(50 * time.Millisecond)
	}
	u.wantAnswer(t, 200, "")
	if n := ipCount(e.th, "127.0.0.1"); n != 1 || e.w.calls() != 1 {
		t.Errorf("ip count %d, worker calls %d, want 1 and 1", n, e.w.calls())
	}
}

// deadlineWriter records the read deadlines set through http.ResponseController.
type deadlineWriter struct {
	http.ResponseWriter
	set chan time.Time
}

func (d *deadlineWriter) SetReadDeadline(t time.Time) error {
	d.set <- t
	return nil
}

func filePart(t *testing.T, body io.Reader, boundary string) *multipart.Part {
	t.Helper()
	p, err := multipart.NewReader(body, boundary).NextPart()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadFile_AFiredDeadlineIsClearedOnSuccess(t *testing.T) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	dw := &deadlineWriter{ResponseWriter: httptest.NewRecorder(), set: make(chan time.Time, 4)}
	go func() {
		w, _ := mw.CreateFormFile("file", "inv.csv")
		_, _ = w.Write([]byte("a,b\n"))
		<-dw.set
		dw.set <- time.Now()
		_, _ = w.Write([]byte("1,2\n"))
		_ = mw.Close()
		_ = pw.Close()
	}()
	part := filePart(t, pr, mw.Boundary())
	data, ok := readFile(context.Background(), slog.New(slog.DiscardHandler), dw, part, time.Millisecond)
	if !ok || string(data) != "a,b\n1,2\n" {
		t.Fatalf("readFile = %q, %v, want the full file", data, ok)
	}
	var last time.Time
	for len(dw.set) > 0 {
		last = <-dw.set
	}
	if !last.IsZero() {
		t.Errorf("last read deadline = %v, want the zero time", last)
	}
}

// endOnWrite ends the upload once the deadline failure is logged, since an unsupported deadline cannot.
type endOnWrite struct {
	bytes.Buffer
	pw *io.PipeWriter
}

func (e *endOnWrite) Write(b []byte) (int, error) {
	n, err := e.Buffer.Write(b)
	_ = e.pw.CloseWithError(io.ErrUnexpectedEOF)
	return n, err
}

func TestReadFile_LogsWhenDeadlinesAreNotSupported(t *testing.T) {
	pr, pw := io.Pipe()
	logs := &endOnWrite{pw: pw}
	log := slog.New(slog.NewTextHandler(logs, nil))
	mw := multipart.NewWriter(pw)
	go func() {
		w, _ := mw.CreateFormFile("file", "secret-name.csv")
		_, _ = w.Write([]byte("a,b\n"))
	}()
	part := filePart(t, pr, mw.Boundary())
	rec := httptest.NewRecorder()
	if _, ok := readFile(context.Background(), log, rec, part, time.Millisecond); ok {
		t.Fatal("readFile succeeded on a stalled read")
	}
	out := logs.String()
	if strings.Count(out, "upload deadline not supported") != 1 || !strings.Contains(out, "level=ERROR") {
		t.Errorf("log = %q, want one error line", out)
	}
	if strings.Contains(out, "secret-name") {
		t.Errorf("log names the file: %q", out)
	}
}
