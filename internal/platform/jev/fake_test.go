package jev

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// -- helpers --

// noSendServer fails the test on any request: fake mode sends nothing.
func noSendServer(t *testing.T) *testServer {
	t.Helper()
	return newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		t.Errorf("fake mode sent a request to %s, want none", r.URL.Path)
		reply(w, http.StatusInternalServerError, "")
	})
}

// fakeConfig is fake mode with no key. Its sleep fails the test: the fake never waits.
func fakeConfig(t *testing.T, ts *testServer) config {
	fc := newFakeClock()
	return config{endpoint: ts.endpoint(), fake: true, budget: budget, retryWait: retryWait, now: fc.now,
		sleep: func(context.Context, time.Duration) error {
			t.Error("fake mode called sleep, want no wait")
			return nil
		}}
}

func fakeClient(t *testing.T) (*Client, *testServer) {
	t.Helper()
	ts := noSendServer(t)
	return newClient(fakeConfig(t, ts), nil), ts
}

// askFake asks once and fails the test on any round trip or hit.
func askFake(t *testing.T, c *Client, ts *testServer, req Request) (Response, error) {
	t.Helper()
	var resp Response
	var err error
	noDials(t, func() { resp, err = askWithin(t, c, t.Context(), req, 2*time.Second) })
	if got := ts.hits.Load(); got != 0 {
		t.Errorf("hits = %d, want 0: fake mode sends nothing", got)
	}
	return resp, err
}

// fakeReq holds two nouls, a choice whose Default is its middle option, and two
// scores whose Defaults are the last level and a middle one.
func fakeReq(state string) Request {
	return Request{
		Purpose: PurposeDocumentType,
		State:   state,
		Questions: map[string]Question{
			"n1": {Type: TypeNoul, Instructions: "Is it an invoice?"},
			"n2": {Type: TypeNoul, Instructions: "Is the total 1,935.00?", True: "it is", False: "it is not"},
			"c": {Type: TypeChoice, Instructions: "Which document type?", Options: []Option{
				{"tax invoice", "A tax invoice"}, {"receipt", ""}, {"credit note", "A credit note"},
			}, Default: "receipt"},
			"s": {Type: TypeScore, Instructions: "How legible is it?", Options: []Option{
				{"l0", "illegible"}, {"l1", "partly legible"}, {"l2", "legible"},
			}, Default: "l2"},
			"s2": {Type: TypeScore, Instructions: "How complete is it?", Options: []Option{
				{"k0", "empty"}, {"k1", "sparse"}, {"k2", "most fields"}, {"k3", "every field"},
			}, Default: "k1"},
		},
	}
}

// defaultAnswers is fakeReq's no-marker answer set.
func defaultAnswers() map[string]Answer {
	return map[string]Answer{
		"n1": {Type: TypeNoul, Noul: 1},
		"n2": {Type: TypeNoul, Noul: 1},
		"c":  {Type: TypeChoice, Choice: "receipt", Confidence: 1},
		"s":  {Type: TypeScore, Score: 2, Confidence: 1},
		"s2": {Type: TypeScore, Score: 1, Confidence: 1},
	}
}

func choiceMarker(name string) string {
	return markerChoicePrefix + base64.RawURLEncoding.EncodeToString([]byte(name))
}

// wantAnswers asserts the answers before the error, so a red names the answers.
func wantAnswers(t *testing.T, resp Response, err error, want map[string]Answer) {
	t.Helper()
	if !reflect.DeepEqual(resp.Answers, want) {
		t.Errorf("answers = %+v, want %+v", resp.Answers, want)
	}
	if err != nil {
		t.Errorf("Ask() err = %v, want nil", err)
	}
}

func wantNoAnswers(t *testing.T, resp Response) {
	t.Helper()
	if len(resp.Answers) != 0 {
		t.Errorf("answers = %+v, want none", resp.Answers)
	}
}

// -- AC-3 --

func TestFake_SendsNothingAndIsEnabled(t *testing.T) {
	c, ts := fakeClient(t)
	if !c.Enabled() {
		t.Error("Enabled() = false, want true in fake mode")
	}

	before := dials.Load()
	_, err := askWithin(t, c, t.Context(), fakeReq("s"), 2*time.Second)
	if got := dials.Load() - before; got != 0 {
		t.Errorf("round trips = %d, want 0: fake mode with no key sends nothing", got)
	}
	if got := ts.hits.Load(); got != 0 {
		t.Errorf("hits = %d, want 0", got)
	}
	if err != nil {
		t.Errorf("Ask() err = %v, want nil", err)
	}
}

func TestFake_ValidatesTheRequestLikeTheRealPath(t *testing.T) {
	if len(invalidRequests) == 0 {
		t.Fatal("invalidRequests is empty")
	}
	for _, tc := range invalidRequests {
		t.Run(tc.clause, func(t *testing.T) {
			ts := noSendServer(t)
			buf := &bytes.Buffer{}
			c := newClient(fakeConfig(t, ts), jsonLogger(buf))
			req := threeTypeReq()
			// A marker the fake would act on: only validating first yields plain refused.
			req.State = "s " + markerUnavailable
			tc.mutate(&req)

			resp, err := askFake(t, c, ts, req)
			wantSkipped(t, err, textRefused)
			wantNoAnswers(t, resp)
			line := oneLine(t, buf)
			wantStr(t, line, "outcome", "skipped_refused")
			wantNum(t, line, "attempts", 0)
		})
	}

	// The same request unmutated reaches the fake, so the rows above test validation alone.
	t.Run("control_valid_request", func(t *testing.T) {
		c, ts := fakeClient(t)
		req := threeTypeReq()
		req.State = "s " + markerUnavailable

		_, err := askFake(t, c, ts, req)
		wantSkipped(t, err, textUnavailable+" (fake)")
	})
}

// -- AC-4 --

func TestFake_DefaultAnswersNoDoubt(t *testing.T) {
	c, ts := fakeClient(t)

	resp, err := askFake(t, c, ts, fakeReq("invoice 123"))
	wantAnswers(t, resp, err, defaultAnswers())
	if resp.Usage != (Usage{}) {
		t.Errorf("usage = %+v, want zero", resp.Usage)
	}
}

// -- AC-5 --

func TestFake_DoubtMarkerDoubtsEveryNoulOnly(t *testing.T) {
	c, ts := fakeClient(t)
	want := defaultAnswers()
	want["n1"] = Answer{Type: TypeNoul, Noul: 0}
	want["n2"] = Answer{Type: TypeNoul, Noul: 0}

	resp, err := askFake(t, c, ts, fakeReq("row 1\n"+markerDoubt+"\nrow 2"))
	wantAnswers(t, resp, err, want)
}

// -- AC-6 --

func TestFake_ChoiceMarkerAnswersTheNamedOption(t *testing.T) {
	c, ts := fakeClient(t)
	want := defaultAnswers()
	want["c"] = Answer{Type: TypeChoice, Choice: "credit note", Confidence: 1}

	resp, err := askFake(t, c, ts, fakeReq("row 1\n"+choiceMarker("credit note")+"\nrow 2"))
	wantAnswers(t, resp, err, want)
}

func TestFake_ChoiceMarkerNamingAnAbsentOptionIsRefused(t *testing.T) {
	c, ts := fakeClient(t)

	resp, err := askFake(t, c, ts, fakeReq(choiceMarker("purchase order")))
	wantSkipped(t, err, textRefused+" (fake)")
	wantNoAnswers(t, resp)
}

func TestFake_ChoiceMarkerWithAnUndecodablePayloadIsRefused(t *testing.T) {
	if _, err := base64.RawURLEncoding.DecodeString("A"); err == nil {
		t.Fatal("fixture: payload \"A\" decodes, want an undecodable payload")
	}
	c, ts := fakeClient(t)

	resp, err := askFake(t, c, ts, fakeReq(markerChoicePrefix+"A"))
	wantSkipped(t, err, textRefused+" (fake)")
	wantNoAnswers(t, resp)
}

func TestFake_ChoicePayloadClassRoundTripsDashAndUnderscore(t *testing.T) {
	const name = "ok? a>"
	if enc := base64.RawURLEncoding.EncodeToString([]byte(name)); !strings.Contains(enc, "-") || !strings.Contains(enc, "_") {
		t.Fatalf("fixture: %q encodes to %q, want both '-' and '_'", name, enc)
	}
	c, ts := fakeClient(t)
	req := fakeReq(choiceMarker(name) + " end")
	setQ(&req, "c", func(q *Question) {
		q.Options = []Option{{"tax invoice", ""}, {name, ""}, {"credit note", ""}}
		q.Default = "tax invoice"
	})
	want := defaultAnswers()
	want["c"] = Answer{Type: TypeChoice, Choice: name, Confidence: 1}

	resp, err := askFake(t, c, ts, req)
	wantAnswers(t, resp, err, want)
}

func TestFake_TrailingWordCharsCorruptTheChoicePayload(t *testing.T) {
	t.Run("trailing_word_chars", func(t *testing.T) {
		c, ts := fakeClient(t)

		resp, err := askFake(t, c, ts, fakeReq("scan-"+choiceMarker("credit note")+"_v2.pdf"))
		wantSkipped(t, err, textRefused+" (fake)")
		wantNoAnswers(t, resp)
	})

	t.Run("control_non_word_char", func(t *testing.T) {
		c, ts := fakeClient(t)
		want := defaultAnswers()
		want["c"] = Answer{Type: TypeChoice, Choice: "credit note", Confidence: 1}

		resp, err := askFake(t, c, ts, fakeReq("scan-"+choiceMarker("credit note")+".pdf"))
		wantAnswers(t, resp, err, want)
	})
}

func TestFake_ChoiceMarkerWithNoChoiceQuestionAnswersTheDefaults(t *testing.T) {
	noChoice := func(state string) Request {
		req := fakeReq(state)
		delete(req.Questions, "c")
		return req
	}

	t.Run("payload_decodes", func(t *testing.T) {
		c, ts := fakeClient(t)
		want := defaultAnswers()
		delete(want, "c")

		resp, err := askFake(t, c, ts, noChoice(choiceMarker("credit note")))
		wantAnswers(t, resp, err, want)
	})

	t.Run("payload_undecodable", func(t *testing.T) {
		c, ts := fakeClient(t)

		resp, err := askFake(t, c, ts, noChoice(markerChoicePrefix+"A"))
		wantSkipped(t, err, textRefused+" (fake)")
		wantNoAnswers(t, resp)
	})
}

// -- AC-7 --

func TestFake_UnavailableMarkerSkipsAtOnce(t *testing.T) {
	c, ts := fakeClient(t)

	resp, err := askFake(t, c, ts, fakeReq("row 1\n"+markerUnavailable+"\nrow 2"))
	wantSkipped(t, err, textUnavailable+" (fake)")
	wantNoAnswers(t, resp)
}

func TestFake_RefusedMarkerSkipsAtOnce(t *testing.T) {
	c, ts := fakeClient(t)

	resp, err := askFake(t, c, ts, fakeReq("row 1\n"+markerRefused+"\nrow 2"))
	wantSkipped(t, err, textRefused+" (fake)")
	wantNoAnswers(t, resp)
}

// -- AC-8 --

func TestFake_FirstMarkerWins(t *testing.T) {
	t.Run("unavailable_then_doubt", func(t *testing.T) {
		c, ts := fakeClient(t)

		resp, err := askFake(t, c, ts, fakeReq("a "+markerUnavailable+" b "+markerDoubt+" c"))
		wantSkipped(t, err, textUnavailable+" (fake)")
		wantNoAnswers(t, resp)
	})

	t.Run("doubt_then_unavailable", func(t *testing.T) {
		c, ts := fakeClient(t)
		want := defaultAnswers()
		want["n1"] = Answer{Type: TypeNoul, Noul: 0}
		want["n2"] = Answer{Type: TypeNoul, Noul: 0}

		resp, err := askFake(t, c, ts, fakeReq("a "+markerDoubt+" b "+markerUnavailable+" c"))
		wantAnswers(t, resp, err, want)
	})
}

func TestFake_MarkersAreCaseSensitiveAndReadFromStateOnly(t *testing.T) {
	cases := []struct {
		name   string
		state  string
		mutate func(r *Request)
	}{
		{"lowercase_in_state", "scan " + strings.ToLower(markerDoubt), nil},
		{"only_in_instructions", "s", func(r *Request) {
			setQ(r, "n1", func(q *Question) { q.Instructions = "Is it " + markerDoubt + "?" })
		}},
		{"only_in_an_option_description", "s", func(r *Request) {
			setQ(r, "c", func(q *Question) { q.Options[0].Description = "A " + markerDoubt })
		}},
		{"only_in_true", "s", func(r *Request) {
			setQ(r, "n2", func(q *Question) { q.True = markerDoubt })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, ts := fakeClient(t)
			req := fakeReq(tc.state)
			if tc.mutate != nil {
				tc.mutate(&req)
			}

			resp, err := askFake(t, c, ts, req)
			wantAnswers(t, resp, err, defaultAnswers())
		})
	}

	// The marker in State does steer, so the rows above cannot pass on a fake that ignores markers.
	t.Run("control_in_state", func(t *testing.T) {
		c, ts := fakeClient(t)

		resp, err := askFake(t, c, ts, fakeReq("scan "+markerDoubt))
		if got := resp.Answers["n1"]; got != (Answer{Type: TypeNoul, Noul: 0}) {
			t.Errorf("answers[n1] = %+v, want noul 0", got)
		}
		if err != nil {
			t.Errorf("Ask() err = %v, want nil", err)
		}
	})
}

// The word-char rows fail a matcher anchored on \b; the filename row alone cannot.
func TestFake_MarkerNeedsNoWordBoundary(t *testing.T) {
	for name, state := range map[string]string{
		"filename_shaped":     "scan-" + markerDoubt + ".pdf",
		"leading_word_chars":  "scan" + markerDoubt,
		"trailing_word_chars": markerDoubt + "X",
	} {
		t.Run(name, func(t *testing.T) {
			c, ts := fakeClient(t)
			want := defaultAnswers()
			want["n1"] = Answer{Type: TypeNoul, Noul: 0}
			want["n2"] = Answer{Type: TypeNoul, Noul: 0}

			resp, err := askFake(t, c, ts, fakeReq(state))
			wantAnswers(t, resp, err, want)
		})
	}
}

func TestFake_AnAIRMarkerDoesNotSteer(t *testing.T) {
	c, ts := fakeClient(t)

	resp, err := askFake(t, c, ts, fakeReq("AIFAKE-UNAVAILABLE AIFAKE-ANSWER-e30"))
	wantAnswers(t, resp, err, defaultAnswers())
}

// -- AC-9 --

func TestFakeMarkers_AreTheFourSpellingsInOrder(t *testing.T) {
	want := []string{"JEVFAKE-DOUBT", "JEVFAKE-CHOICE-", "JEVFAKE-UNAVAILABLE", "JEVFAKE-REFUSED"}
	if !reflect.DeepEqual(fakeMarkers, want) {
		t.Errorf("fakeMarkers = %q, want %q", fakeMarkers, want)
	}
}

func TestFake_EveryTableMarkerSteersADistinctOutcome(t *testing.T) {
	if len(fakeMarkers) != 4 {
		t.Fatalf("len(fakeMarkers) = %d, want 4", len(fakeMarkers))
	}
	type outcome struct {
		answers map[string]Answer
		err     string
	}
	ask := func(state string) outcome {
		c, ts := fakeClient(t)
		resp, err := askFake(t, c, ts, fakeReq(state))
		return outcome{resp.Answers, errText(err)}
	}

	none := ask("doc 1")
	got := make([]outcome, len(fakeMarkers))
	for i, m := range fakeMarkers {
		state := "doc " + m
		if strings.HasSuffix(m, "-") {
			state += base64.RawURLEncoding.EncodeToString([]byte("credit note"))
		}
		got[i] = ask(state + " end")
		if reflect.DeepEqual(got[i], none) {
			t.Errorf("marker %q: outcome %+v equals the no-marker outcome", m, got[i])
		}
		for j := range i {
			if reflect.DeepEqual(got[i], got[j]) {
				t.Errorf("markers %q and %q steer the same outcome %+v", fakeMarkers[j], m, got[i])
			}
		}
	}
}
