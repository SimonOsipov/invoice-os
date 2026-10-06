package accountmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testKey  = "k_test"
	testAddr = "ada@obi.test"
)

type recordedReq struct {
	Method, Path string
	Header       http.Header
	Body         []byte
}

// recordingServer records every request and answers with the status and body.
func recordingServer(t *testing.T, status int, body string) (*httptest.Server, func() []recordedReq) {
	t.Helper()
	var mu sync.Mutex
	var got []recordedReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, recordedReq{r.Method, r.URL.Path, r.Header.Clone(), b})
		mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recordedReq {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedReq(nil), got...)
	}
}

func msgs(n int) []Message {
	out := make([]Message, n)
	for i := range out {
		out[i] = Message{To: fmt.Sprintf("u%d@obi.test", i+1), Subject: fmt.Sprintf("s%d", i+1), HTML: fmt.Sprintf("<p>m%d</p>", i+1)}
	}
	return out
}

func sendStatus(t *testing.T, status int) error {
	t.Helper()
	srv, _ := recordingServer(t, status, `{}`)
	return NewResend(srv.URL, testKey, nil).Send(t.Context(), msgs(1))
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestResend_SendsOneBatch(t *testing.T) {
	srv, seen := recordingServer(t, http.StatusOK, `{"data":[]}`)
	in := []Message{
		{To: "ada@obi.test", Subject: "You are invited to ASComply", HTML: "<p>one</p>"},
		{To: "tunde@obi.test", Subject: "You are invited to ASComply", HTML: "<p>two</p>"},
	}
	if err := NewResend(srv.URL, testKey, nil).Send(t.Context(), in); err != nil {
		t.Fatalf("Send = %v, want nil", err)
	}
	reqs := seen()
	if len(reqs) != 1 {
		t.Fatalf("server saw %d requests, want exactly 1 batch call", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodPost || r.Path != "/emails/batch" {
		t.Errorf("request = %s %s, want POST /emails/batch", r.Method, r.Path)
	}
	for h, want := range map[string]string{
		"Authorization":      "Bearer " + testKey,
		"Content-Type":       "application/json",
		"x-batch-validation": "strict",
	} {
		if got := r.Header.Get(h); got != want {
			t.Errorf("header %s = %q, want %q", h, got, want)
		}
	}
	var body []map[string]any
	if err := json.Unmarshal(r.Body, &body); err != nil {
		t.Fatalf("body is not a JSON array of objects: %v\n%s", err, r.Body)
	}
	want := []map[string]any{
		{"from": From, "to": []any{"ada@obi.test"}, "subject": "You are invited to ASComply", "html": "<p>one</p>"},
		{"from": From, "to": []any{"tunde@obi.test"}, "subject": "You are invited to ASComply", "html": "<p>two</p>"},
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body = %v, want %v", body, want)
	}
}

func TestResend_StatusErrors(t *testing.T) {
	for _, status := range []int{200, 201} {
		if err := sendStatus(t, status); err != nil {
			t.Errorf("status %d: err = %v, want nil", status, err)
		}
	}
	for _, status := range []int{422, 429, 500} {
		err := sendStatus(t, status)
		var se *SendError
		if !errors.As(err, &se) {
			t.Errorf("status %d: err = %v, want a *SendError", status, err)
			continue
		}
		if se.Status != status {
			t.Errorf("status %d: SendError.Status = %d", status, se.Status)
		}
		if want := fmt.Sprintf("accountmail: send failed: status %d", status); err.Error() != want {
			t.Errorf("status %d: text = %q, want %q", status, err.Error(), want)
		}
	}
}

// refusedURL is the URL of a server that has been closed: dialing it fails.
func refusedURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	return u
}

// hangingServer holds each request until the client gives up.
func hangingServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// shortTimeout stands in for a client timeout: NewResend fixes its own at 10 s.
func shortTimeout(d time.Duration) http.RoundTripper {
	return rtFunc(func(r *http.Request) (*http.Response, error) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		return http.DefaultTransport.RoundTrip(r.WithContext(ctx))
	})
}

func TestResend_NoResponseIsStatusZero(t *testing.T) {
	cases := map[string]error{
		"refused connection": NewResend(refusedURL(t), testKey, nil).Send(t.Context(), msgs(1)),
		"timeout":            NewResend(hangingServer(t).URL, testKey, shortTimeout(50*time.Millisecond)).Send(t.Context(), msgs(1)),
	}
	for name, err := range cases {
		var se *SendError
		if !errors.As(err, &se) {
			t.Errorf("%s: err = %v, want a *SendError", name, err)
			continue
		}
		if se.Status != 0 {
			t.Errorf("%s: SendError.Status = %d, want 0", name, se.Status)
		}
	}
}

func TestResend_RedirectIsNotFollowed(t *testing.T) {
	target, targetSeen := recordingServer(t, http.StatusOK, `{}`)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/emails/batch", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(first.Close)

	err := NewResend(first.URL, testKey, nil).Send(t.Context(), msgs(1))
	var se *SendError
	if !errors.As(err, &se) || se.Status != http.StatusTemporaryRedirect {
		t.Fatalf("err = %v, want *SendError{307}", err)
	}
	if n := len(targetSeen()); n != 0 {
		t.Errorf("the redirect target got %d requests, want 0: the key must not follow a Location", n)
	}
}

func TestResend_ErrorTextCarriesNoSecret(t *testing.T) {
	echo := `{"message":"invalid to ` + testAddr + ` for key k_secret"}`
	type failure struct {
		name, base string
		rt         http.RoundTripper
	}
	var fails []failure
	for _, status := range []int{422, 429, 500} {
		srv, _ := recordingServer(t, status, echo)
		fails = append(fails, failure{fmt.Sprintf("status %d", status), srv.URL, nil})
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+testAddr+"/x", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	fails = append(fails,
		failure{"redirect", redirect.URL, nil},
		failure{"refused connection", refusedURL(t), nil},
		failure{"timeout", hangingServer(t).URL, shortTimeout(50 * time.Millisecond)},
	)

	for _, f := range fails {
		err := NewResend(f.base, "k_secret", f.rt).Send(t.Context(), []Message{{To: testAddr, Subject: "s", HTML: "<p>h</p>"}})
		if err == nil {
			t.Errorf("%s: err = nil, want a failure", f.name)
			continue
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			t.Errorf("%s: the chain holds a *url.Error, whose text carries the URL: %v", f.name, err)
		}
		host := strings.TrimPrefix(f.base, "http://")
		for _, secret := range []string{testAddr, "k_secret", f.base, host, "/emails/batch"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: error text %q holds %q", f.name, err.Error(), secret)
			}
		}
	}
}

func TestResend_EmptyAndOversizeBatches(t *testing.T) {
	srv, seen := recordingServer(t, http.StatusOK, `{}`)
	c := NewResend(srv.URL, testKey, nil)

	for name, in := range map[string][]Message{"nil": nil, "empty": {}} {
		if err := c.Send(t.Context(), in); err != nil {
			t.Errorf("%s slice: err = %v, want nil", name, err)
		}
	}
	if n := len(seen()); n != 0 {
		t.Fatalf("an empty batch made %d requests, want 0", n)
	}

	if err := c.Send(t.Context(), msgs(101)); err == nil {
		t.Error("101 messages: err = nil, want an error")
	}
	if n := len(seen()); n != 0 {
		t.Fatalf("101 messages made %d requests, want 0", n)
	}

	// 100 is the limit: a `>= 100` check would pass the 101 case above.
	if err := c.Send(t.Context(), msgs(100)); err != nil {
		t.Errorf("100 messages: err = %v, want nil", err)
	}
	if n := len(seen()); n != 1 {
		t.Errorf("100 messages made %d requests, want 1", n)
	}
}

func TestResend_RequestCarriesATenSecondDeadline(t *testing.T) {
	var mu sync.Mutex
	var left []time.Duration
	var noDeadline int
	spy := rtFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		if d, ok := r.Context().Deadline(); ok {
			left = append(left, time.Until(d))
		} else {
			noDeadline++
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	})
	if err := NewResend("http://resend.test", testKey, spy).Send(t.Context(), msgs(1)); err != nil {
		t.Fatalf("Send = %v, want nil", err)
	}
	if noDeadline != 0 || len(left) != 1 {
		t.Fatalf("spy saw %d requests with a deadline and %d without, want 1 and 0", len(left), noDeadline)
	}
	if left[0] <= 8*time.Second || left[0] > 10*time.Second {
		t.Errorf("request carried %v of deadline, want more than 8s and at most 10s", left[0])
	}
}

func TestModeFromEnv_PreviewAndKey(t *testing.T) {
	cases := []struct {
		name     string
		preview  bool
		env      map[string]string
		wantMode Mode
		wantKey  string
	}{
		{"preview, unset", true, nil, "capture", ""},
		{"preview, key set", true, map[string]string{"RESEND_SENDING_KEY": "k"}, "capture", ""},
		{"preview, whitespace", true, map[string]string{"RESEND_SENDING_KEY": "  "}, "capture", ""},
		{"not preview, key set", false, map[string]string{"RESEND_SENDING_KEY": "k"}, "real", "k"},
		{"not preview, whitespace is not trimmed", false, map[string]string{"RESEND_SENDING_KEY": "  "}, "real", "  "},
		{"not preview, unset", false, nil, "off", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// RESEND_API_KEY is the contacts key on another service; it must never pick `real`.
			env := map[string]string{"RESEND_API_KEY": "contacts-key"}
			for k, v := range tc.env {
				env[k] = v
			}
			mode, key := ModeFromEnv(func(k string) string { return env[k] }, tc.preview)
			if mode != tc.wantMode || key != tc.wantKey {
				t.Errorf("ModeFromEnv = (%q, %q), want (%q, %q)", mode, key, tc.wantMode, tc.wantKey)
			}
		})
	}
}

func TestCapture_RecordsWithoutNetwork(t *testing.T) {
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("capture made a network call: %s %s", r.Method, r.URL)
		return nil, errors.New("must not be called")
	})
	s := NewSender("capture", "", rt)
	c, ok := s.(*Capture)
	if !ok {
		t.Fatalf("NewSender(capture) = %T, want *Capture", s)
	}
	all := msgs(101)
	for i, m := range all {
		if err := s.Send(t.Context(), []Message{m}); err != nil {
			t.Fatalf("send %d: err = %v, want nil", i+1, err)
		}
	}
	got := c.Messages()
	if len(got) != 100 {
		t.Fatalf("Messages() holds %d, want the last 100", len(got))
	}
	if got[0] != all[1] || got[99] != all[100] {
		t.Errorf("first = %+v, last = %+v, want message 2 and message 101", got[0], got[99])
	}
}

func TestOff_IsNotConfigured(t *testing.T) {
	err := Off{}.Send(t.Context(), msgs(1))
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Off.Send = %v, want ErrNotConfigured", err)
	}
}

func TestNewSender_PicksTheImplementation(t *testing.T) {
	cases := []struct {
		mode Mode
		want any
	}{
		{"real", (*Resend)(nil)},
		{"capture", (*Capture)(nil)},
		{"off", Off{}},
	}
	for _, tc := range cases {
		got := NewSender(tc.mode, "k", nil)
		if reflect.TypeOf(got) != reflect.TypeOf(tc.want) {
			t.Errorf("NewSender(%q) = %T, want %T", tc.mode, got, tc.want)
		}
	}
}
