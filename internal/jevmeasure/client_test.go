// client_test.go: the HTTP client's tests, driven by httptest. No vendor, no network.
package jevmeasure

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func jcMinimalQuestions() map[string]Question {
	return map[string]Question{"q1": {Type: "noul", Instructions: "is the amount correct?"}}
}

// AC-1. One POST, the key under AuthHeaderName with AuthHeaderPrefix, and the wire
// body carries model/state/questions -- the key appears nowhere else. The spelling
// of AuthHeaderName/AuthHeaderPrefix is read from the constants, never guessed:
// CHECK-02 owns that lookup against the vendor's API reference.
func TestClient_SendsOneBearerPost(t *testing.T) {
	const apiKey = "sk-test-JEVKEY42"
	const model = "jev-latest"
	const state = "invoice state text"

	var requests int
	var gotMethod, gotAuth, gotURL string
	var gotHeader http.Header
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		gotMethod = r.Method
		gotAuth = r.Header.Get(AuthHeaderName)
		gotURL = r.URL.String()
		gotHeader = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		io.WriteString(w, `{"answers":{},"usage":{}}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, model, apiKey)
	if _, _, err := c.Ask(context.Background(), state, jcMinimalQuestions()); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if requests != 1 {
		t.Fatalf("server saw %d request(s), want exactly 1", requests)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if want := AuthHeaderPrefix + apiKey; gotAuth != want {
		t.Errorf("%s header = %q, want %q", AuthHeaderName, gotAuth, want)
	}
	if strings.Contains(gotURL, apiKey) {
		t.Errorf("URL %q carries the key", gotURL)
	}
	if strings.Contains(string(gotBody), apiKey) {
		t.Errorf("body carries the key outside the header")
	}
	for name, vals := range gotHeader {
		if name == AuthHeaderName {
			continue
		}
		for _, v := range vals {
			if strings.Contains(v, apiKey) {
				t.Errorf("header %s carries the key: %q", name, v)
			}
		}
	}

	var parsed map[string]any
	if err := json.Unmarshal(gotBody, &parsed); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if parsed["model"] != model {
		t.Errorf("body model = %v, want %q", parsed["model"], model)
	}
	if parsed["state"] != state {
		t.Errorf("body state = %v, want %q", parsed["state"], state)
	}
	qs, ok := parsed["questions"].(map[string]any)
	if !ok || len(qs) != 1 {
		t.Errorf("body questions = %v, want a 1-entry object", parsed["questions"])
	}
}

// AC-1. json.Number must survive a value a float64 field would round.
func TestClient_ProbabilitiesDecodeExactly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"answers":{"q1":{"type":"noul","noul":0.93000000000000005}},"usage":{}}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "jev-latest", "k")
	resp, _, err := c.Ask(context.Background(), "state", jcMinimalQuestions())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	ans, ok := resp.Answers["q1"]
	if !ok {
		t.Fatalf("answers[q1] missing")
	}
	if ans.Noul == nil {
		t.Fatalf("Noul is nil")
	}
	if got := string(*ans.Noul); got != "0.93000000000000005" {
		t.Errorf("Noul = %s, want 0.93000000000000005 -- a float64 field would round this", got)
	}
}

// AC-1, new A49. A noul answer is {type, noul} -- two fields, no confidence. A
// choice answer in the same response is the control leg: it does carry one.
func TestClient_ANoulAnswerCarriesNoConfidence(t *testing.T) {
	body := `{"answers":{
		"q1":{"type":"noul","noul":0.42},
		"q2":{"type":"choice","choice":"tax_invoice","confidence":0.81,"probabilities":{"tax_invoice":0.81,"other":0.19}}
	},"usage":{}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "jev-latest", "k")
	resp, _, err := c.Ask(context.Background(), "state", jcMinimalQuestions())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	q1, ok := resp.Answers["q1"]
	if !ok {
		t.Fatalf("answers[q1] missing")
	}
	if q1.Noul == nil {
		t.Fatalf("q1.Noul is nil")
	}
	if q1.Confidence != nil {
		t.Errorf("q1.Confidence = %s, want nil -- a noul answer has no confidence field", string(*q1.Confidence))
	}

	q2, ok := resp.Answers["q2"]
	if !ok {
		t.Fatalf("answers[q2] missing")
	}
	if q2.Confidence == nil {
		t.Errorf("q2.Confidence is nil -- control leg: a choice answer does carry one")
	}
	if q2.Choice != "tax_invoice" {
		t.Errorf("q2.Choice = %q, want tax_invoice", q2.Choice)
	}
}

// AC-2. Absence check: the "500" leg is the floor -- an empty error would pass
// the SECRETBODY assertion vacuously otherwise.
func TestClient_AnErrorCarriesNoResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "SECRETBODY")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "jev-latest", "k")
	_, _, err := c.Ask(context.Background(), "state", jcMinimalQuestions())
	if err == nil {
		t.Fatalf("Ask: want an error on 500, got nil")
	}
	msg := err.Error()
	if strings.Contains(msg, "SECRETBODY") {
		t.Errorf("error %q carries the response body", msg)
	}
	if !strings.Contains(msg, "500") {
		t.Errorf("error %q never names the status code", msg)
	}
}

// AC-3/7, new. A failed call still reports its elapsed wall clock, since the
// all-attempts latency series depends on it.
func TestClient_AFailedCallStillReportsItsElapsedTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "jev-latest", "k")
	_, elapsed, err := c.Ask(context.Background(), "state", jcMinimalQuestions())
	if err == nil {
		t.Fatalf("Ask: want an error on 500, got nil")
	}
	if elapsed < 50*time.Millisecond {
		t.Errorf("elapsed = %v, want >= 50ms -- an impl returning 0 on the error path would fail this", elapsed)
	}
}
