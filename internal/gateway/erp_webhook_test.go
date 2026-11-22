package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type hookWorker struct {
	calls  atomic.Int32
	path   string
	token  string
	raw    []byte
	status int
}

func newHookWorker(t *testing.T, status int) (*hookWorker, *url.URL) {
	t.Helper()
	hw := &hookWorker{status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hw.calls.Add(1)
		hw.path, hw.token = r.URL.Path, r.Header.Get("X-Gateway-Token")
		hw.raw, _ = io.ReadAll(r.Body)
		w.WriteHeader(hw.status)
		_, _ = w.Write([]byte("worker-needle"))
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return hw, u
}

func hookHandler(u *url.URL, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return ERPWebhookHandler(u, &http.Client{Timeout: 5 * time.Second}, "gw-token", "fake", log)
}

func postHook(h http.Handler, body []byte, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/erp/webhooks/fake", bytes.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestERPWebhook_ForwardsTheExactBodyAndVendorHeaders(t *testing.T) {
	hw, u := newHookWorker(t, 200)
	body := []byte{'a', 0x00, 0xff, 'z'}
	rec := postHook(hookHandler(u, nil), body, map[string]string{
		"X-Fake-Signature": "sig", "Content-Type": "text/plain", "Cookie": "s=1",
		"X-Forwarded-For": "1.2.3.4", "Forwarded": "for=1.2.3.4", "X-Real-Ip": "1.2.3.4",
	})
	if rec.Code != 200 || hw.calls.Load() != 1 || hw.path != "/internal/erp/webhooks/fake" || hw.token != "gw-token" {
		t.Fatalf("code %d calls %d path %q token %q", rec.Code, hw.calls.Load(), hw.path, hw.token)
	}
	var env struct {
		Headers map[string][]string `json:"headers"`
		Body    []byte              `json:"body"`
	}
	if err := json.Unmarshal(hw.raw, &env); err != nil || !bytes.Equal(env.Body, body) {
		t.Fatalf("envelope body %v err %v", env.Body, err)
	}
	if env.Headers["X-Fake-Signature"][0] != "sig" || env.Headers["Content-Type"][0] != "text/plain" {
		t.Errorf("vendor headers missing: %v", env.Headers)
	}
	for _, h := range []string{"Cookie", "X-Forwarded-For", "Forwarded", "X-Real-Ip"} {
		if _, ok := env.Headers[h]; ok {
			t.Errorf("%s forwarded", h)
		}
	}
}

func TestERPWebhook_BodyAndHeaderCaps(t *testing.T) {
	hw, u := newHookWorker(t, 200)
	h := hookHandler(u, nil)
	if rec := postHook(h, make([]byte, 65536), nil); rec.Code != 200 || hw.calls.Load() != 1 {
		t.Fatalf("64 KiB: %d, calls %d", rec.Code, hw.calls.Load())
	}
	cases := []struct {
		name string
		body int
		hdr  map[string]string
		want int
	}{
		{"body over", 65537, nil, 413},
		{"raw header over", 1, map[string]string{"X-Big": strings.Repeat("a", 17<<10)}, 431},
		{"encoded header over", 1, map[string]string{"X-Big": strings.Repeat("<", 3<<10)}, 431},
	}
	for _, c := range cases {
		if rec := postHook(h, make([]byte, c.body), c.hdr); rec.Code != c.want {
			t.Errorf("%s = %d, want %d", c.name, rec.Code, c.want)
		}
	}
	if hw.calls.Load() != 1 {
		t.Errorf("worker calls %d, want 1 (only the 64 KiB body)", hw.calls.Load())
	}
}

func TestERPWebhook_MaxShapeEnvelopeFits(t *testing.T) {
	hw, u := newHookWorker(t, 200)
	// Each "<" encodes to 6 bytes; size the value so the encoded headers land just under 16 KiB.
	hdr := map[string]string{"X-A": strings.Repeat("<", 2700)}
	for {
		enc, _ := json.Marshal(http.Header{"X-A": {hdr["X-A"]}})
		if len(enc) <= erpWebhookMaxHeader-6 {
			break
		}
		hdr["X-A"] = hdr["X-A"][1:]
	}
	if rec := postHook(hookHandler(u, nil), make([]byte, 65536), hdr); rec.Code != 200 || hw.calls.Load() != 1 {
		t.Fatalf("code %d calls %d", rec.Code, hw.calls.Load())
	}
	if len(hw.raw) > 103790 {
		t.Errorf("envelope %d bytes, want <= 103790", len(hw.raw))
	}
}

func TestERPWebhook_MapsTheWorkerAnswer(t *testing.T) {
	for in, want := range map[int]int{200: 200, 400: 400, 401: 401, 404: 404, 413: 413, 500: 503, 502: 503, 418: 503} {
		_, u := newHookWorker(t, in)
		rec := postHook(hookHandler(u, nil), []byte("x"), nil)
		if rec.Code != want || strings.Contains(rec.Body.String(), "worker-needle") {
			t.Errorf("worker %d -> %d body %q, want %d without the worker body", in, rec.Code, rec.Body.String(), want)
		}
	}
}

func TestERPWebhook_UnreachableOrSlowWorkerIs503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	u, _ := url.Parse(srv.URL)
	srv.Close()
	if rec := postHook(hookHandler(u, nil), []byte("x"), nil); rec.Code != 503 {
		t.Errorf("closed worker = %d, want 503", rec.Code)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { time.Sleep(300 * time.Millisecond) }))
	t.Cleanup(slow.Close)
	u, _ = url.Parse(slow.URL)
	h := ERPWebhookHandler(u, &http.Client{Timeout: 50 * time.Millisecond}, "gw-token", "fake", slog.New(slog.DiscardHandler))
	if rec := postHook(h, []byte("x"), nil); rec.Code != 503 {
		t.Errorf("slow worker = %d, want 503", rec.Code)
	}
}

func TestERPWebhook_OnlyPost(t *testing.T) {
	hw, u := newHookWorker(t, 200)
	h := hookHandler(u, nil)
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodHead} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, "/erp/webhooks/fake", nil))
		if rec.Code != 405 || rec.Header().Get("Allow") != "POST" {
			t.Errorf("%s = %d Allow %q", m, rec.Code, rec.Header().Get("Allow"))
		}
	}
	if hw.calls.Load() != 0 {
		t.Errorf("worker calls %d, want 0", hw.calls.Load())
	}
}

func TestERPWebhook_LogsNoBodyHeaderOrAddress(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	_, ok := newHookWorker(t, 200)
	_, bad := newHookWorker(t, 500)
	for _, u := range []*url.URL{ok, bad} {
		for _, n := range []int{10, 65537} {
			body := append([]byte("body-needle"), make([]byte, n)...)
			req := httptest.NewRequest(http.MethodPost, "/erp/webhooks/fake", bytes.NewReader(body))
			req.Header.Set("X-Fake-Signature", "sig-needle")
			req.Header.Set("X-Forwarded-For", "9.9.9.9")
			req.RemoteAddr = "8.8.8.8:1234"
			hookHandler(u, log).ServeHTTP(httptest.NewRecorder(), req)
		}
	}
	for _, needle := range []string{"body-needle", "sig-needle", "9.9.9.9", "8.8.8.8"} {
		if strings.Contains(buf.String(), needle) {
			t.Errorf("log carries %q: %s", needle, buf.String())
		}
	}
}
