package ai

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// -- helpers --

// noDialServer answers nothing useful and fails the test if it is ever hit:
// every test in this file expects the fake path to skip the network.
func noDialServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("fake test dialled %s, want no network call", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeModeClient builds a fake-mode client whose endpoint fails the test if
// ever dialled.
func fakeModeClient(t *testing.T) *Client {
	t.Helper()
	return newClient(config{key: "k", endpoint: noDialServer(t).URL, fake: true, budget: budget, now: time.Now, sleep: realSleep}, nil)
}

// fakeCallWithin bounds every Call in this file. A correctly wired fake
// never touches the network, so it returns in microseconds; the 2s window
// only matters today, before Stage 3 wires the branch into call(), when an
// unwired fake client falls through to the real retry loop.
func fakeCallWithin(t *testing.T, c *Client, req Request) (map[string]any, error) {
	t.Helper()
	return callWithin(t, c, t.Context(), req, 2*time.Second)
}

// answerMarker builds an AIFAKE-ANSWER-<base64url> marker for content.
func answerMarker(content string) string {
	return markerAnswerPrefix + base64.RawURLEncoding.EncodeToString([]byte(content))
}

var wantBlankTop = map[string]any{"total": nil, "vat": nil, "currency": nil, "n": nil}
var wantValidContent = map[string]any{"total": "1935.00", "vat": nil, "currency": "NGN", "n": json.Number("3")}

// assertFakeRefused runs req against a fresh fake client and requires that
// the fake path refuses it without a network call and without ErrUnavailable.
func assertFakeRefused(t *testing.T, req Request) {
	t.Helper()
	c := fakeModeClient(t)
	answer, err := fakeCallWithin(t, c, req)
	if err == nil {
		t.Fatal("err = nil, want non-nil")
	}
	if errors.Is(err, ErrUnavailable) {
		t.Errorf("err wraps ErrUnavailable, want a validation error")
	}
	if answer != nil {
		t.Errorf("answer = %#v, want nil", answer)
	}
}

// -- T06 --

func TestFake_NoMarkerAnswersBlank(t *testing.T) {
	t.Run("top_level_schema", func(t *testing.T) {
		c := fakeModeClient(t)
		req := baseReq()
		req.Text = "invoice 123"

		got, err := fakeCallWithin(t, c, req)
		if err != nil {
			t.Fatalf("Call() err = %v, want nil", err)
		}
		if !reflect.DeepEqual(got, wantBlankTop) {
			t.Errorf("Call() = %#v, want %#v", got, wantBlankTop)
		}
	})

	t.Run("nested_schema_blanks_only_the_top_level", func(t *testing.T) {
		c := fakeModeClient(t)
		req := baseReq()
		req.Text = "invoice 123"
		req.Schema = nestedSchema

		got, err := fakeCallWithin(t, c, req)
		if err != nil {
			t.Fatalf("Call() err = %v, want nil", err)
		}
		want := map[string]any{"buyer": nil}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Call() = %#v, want %#v", got, want)
		}
	})
}

// -- T07 --

func TestFake_PagesOnlyAnswersBlank(t *testing.T) {
	c := fakeModeClient(t)
	req := baseReq()
	req.Text = ""
	req.Pages = [][]byte{{1, 2, 3}}

	got, err := fakeCallWithin(t, c, req)
	if err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}
	if !reflect.DeepEqual(got, wantBlankTop) {
		t.Errorf("Call() = %#v, want %#v", got, wantBlankTop)
	}
}

// -- T08 --

func TestFake_AnswerMarkerReturnsTheGivenAnswer(t *testing.T) {
	c := fakeModeClient(t)
	req := baseReq()
	req.Text = "row 1\n" + answerMarker(validContent) + "\nrow 2"

	got, err := fakeCallWithin(t, c, req)
	if err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}
	if !reflect.DeepEqual(got, wantValidContent) {
		t.Errorf("Call() = %#v, want %#v", got, wantValidContent)
	}
}

// -- T09 --

func TestFake_UnavailableMarkerIsUnavailableAtOnce(t *testing.T) {
	c := fakeModeClient(t)
	req := baseReq()
	req.Text = "row 1\n" + markerUnavailable + "\nrow 2"

	start := time.Now()
	answer, err := callWithin(t, c, t.Context(), req, time.Second)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
	if answer != nil {
		t.Errorf("answer = %#v, want nil", answer)
	}
	if elapsed >= 100*time.Millisecond {
		t.Errorf("elapsed = %v, want < 100ms (the fake must not spend the budget)", elapsed)
	}
}

// -- T10 --

func TestFake_BadAnswerIsNotUnavailable(t *testing.T) {
	// Deliberately corrupted std-base64: printable JSON almost never contains
	// '+' or '/' (verified empirically), so a naive std-encoded example of
	// validContent would decode fine even after the '=' is stripped. These
	// three non-text bytes do contain a '/', so std truncates to "AAD",
	// which decodes to two garbage bytes -- a genuine, not accidental, error.
	stdBreaksRawURL := markerAnswerPrefix + base64.StdEncoding.EncodeToString([]byte{0x00, 0x00, 0xff})

	cases := map[string]struct {
		marker    string
		wantBlank bool
	}{
		// Stage 1 correction: not valid base64url at all, so the regexp
		// never matches, and this reads as no-marker (blank), not an error.
		"boundary_not_base64url_reads_as_blank": {"AIFAKE-ANSWER-!!!!", true},
		"not_json":                              {answerMarker("not json"), false},
		"json_array_not_object":                 {answerMarker(`[1]`), false},
		"missing_required_key":                  {answerMarker(`{"total":"1"}`), false},
		"enum_violation":                        {answerMarker(`{"total":"1","vat":null,"currency":"EUR","n":1}`), false},
		"std_encoding_breaks_urlsafe_decode":    {stdBreaksRawURL, false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := fakeModeClient(t)
			req := baseReq()
			req.Text = tc.marker

			answer, err := fakeCallWithin(t, c, req)

			if tc.wantBlank {
				if err != nil {
					t.Fatalf("Call() err = %v, want nil (non-matching marker reads as blank)", err)
				}
				if !reflect.DeepEqual(answer, wantBlankTop) {
					t.Errorf("Call() = %#v, want %#v", answer, wantBlankTop)
				}
				return
			}
			if err == nil {
				t.Fatal("err = nil, want non-nil")
			}
			if errors.Is(err, ErrUnavailable) {
				t.Errorf("err wraps ErrUnavailable, want a plain error")
			}
			if answer != nil {
				t.Errorf("answer = %#v, want nil", answer)
			}
			if !strings.Contains(err.Error(), "fake answer") {
				t.Errorf("err = %q, want it to contain %q", err.Error(), "fake answer")
			}
		})
	}
}

// -- T11 --

func TestFake_MarkerMatchIsExact(t *testing.T) {
	cases := map[string]string{
		"lowercase":            "aifake-unavailable",
		"underscore_not_dash":  "AIFAKE_UNAVAILABLE",
		"unknown_suffix":       "AIFAKE-OTHER",
		"answer_empty_payload": "AIFAKE-ANSWER-",
		"no_separator":         "AIFAKEUNAVAILABLE",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			c := fakeModeClient(t)
			req := baseReq()
			req.Text = text

			answer, err := fakeCallWithin(t, c, req)
			if err != nil {
				t.Fatalf("Call() err = %v, want nil", err)
			}
			if !reflect.DeepEqual(answer, wantBlankTop) {
				t.Errorf("Call() = %#v, want %#v", answer, wantBlankTop)
			}
		})
	}
}

// -- T12 --

func TestFake_FirstMarkerWins(t *testing.T) {
	t.Run("unavailable_first", func(t *testing.T) {
		c := fakeModeClient(t)
		req := baseReq()
		req.Text = markerUnavailable + " and later " + answerMarker(validContent)

		answer, err := fakeCallWithin(t, c, req)
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
		if answer != nil {
			t.Errorf("answer = %#v, want nil", answer)
		}
	})

	// Without this reverse row, a rule that always prefers "unavailable"
	// would pass the row above too.
	t.Run("answer_first", func(t *testing.T) {
		c := fakeModeClient(t)
		req := baseReq()
		req.Text = answerMarker(validContent) + " and later " + markerUnavailable

		answer, err := fakeCallWithin(t, c, req)
		if err != nil {
			t.Fatalf("Call() err = %v, want nil", err)
		}
		if !reflect.DeepEqual(answer, wantValidContent) {
			t.Errorf("Call() = %#v, want %#v", answer, wantValidContent)
		}
	})
}

// -- T13 --

func TestFake_SystemPromptIsNotScanned(t *testing.T) {
	c := fakeModeClient(t)
	req := baseReq()
	req.System = markerUnavailable
	req.Text = "invoice 123"

	answer, err := fakeCallWithin(t, c, req)
	if err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}
	if !reflect.DeepEqual(answer, wantBlankTop) {
		t.Errorf("Call() = %#v, want %#v", answer, wantBlankTop)
	}
}

// -- T14 --

func TestFake_ValidatesTheRequest(t *testing.T) {
	disallowedKeyword := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["total","vat","currency","n"],"properties":{"total":{"type":["string","null"],"pattern":"^N"},"vat":{"type":["string","null"]},"currency":{"type":"string","enum":["NGN","USD"]},"n":{"type":"integer"}}}`)

	cases := map[string]func() Request{
		// Real-path mirror: TestCall_InvalidRequestSendsNothing "bad_purpose".
		"bad_purpose": func() Request {
			r := baseReq()
			r.Purpose = "x"
			return r
		},
		// Real-path mirror: TestCall_FakeHintAloneIsNotInput. Proves the
		// fake cannot be steered into answering a request the real path
		// would refuse.
		"hint_alone_is_not_input": func() Request {
			r := baseReq()
			r.Text = ""
			r.Pages = nil
			r.FakeHint = markerUnavailable
			return r
		},
		// Real-path mirror: TestCall_InvalidRequestSendsNothing "no_schema_name".
		"no_schema_name": func() Request {
			r := baseReq()
			r.SchemaName = ""
			return r
		},
		// Real-path mirror: TestCall_InvalidRequestSendsNothing "non_object_schema".
		"non_object_schema": func() Request {
			r := baseReq()
			r.Schema = json.RawMessage(`{"type":"array"}`)
			return r
		},
		// Real-path mirror: TestCall_InvalidRequestSendsNothing "disallowed_keyword".
		"disallowed_keyword": func() Request {
			r := baseReq()
			r.Schema = disallowedKeyword
			return r
		},
	}

	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			assertFakeRefused(t, build())
		})
	}
}

// -- T15 --

func TestFake_HintSteersAnImagesOnlyCall(t *testing.T) {
	t.Run("unavailable", func(t *testing.T) {
		c := fakeModeClient(t)
		req := baseReq()
		req.Text = ""
		req.Pages = [][]byte{{1}}
		req.FakeHint = markerUnavailable

		answer, err := fakeCallWithin(t, c, req)
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
		if answer != nil {
			t.Errorf("answer = %#v, want nil", answer)
		}
	})

	// A filename-shaped hint deliberately: that is the channel AIR-05 will fill.
	t.Run("answer_inside_a_filename_shaped_hint", func(t *testing.T) {
		c := fakeModeClient(t)
		req := baseReq()
		req.Text = ""
		req.Pages = [][]byte{{1}}
		req.FakeHint = "scan-" + answerMarker(validContent) + ".pdf"

		answer, err := fakeCallWithin(t, c, req)
		if err != nil {
			t.Fatalf("Call() err = %v, want nil", err)
		}
		if !reflect.DeepEqual(answer, wantValidContent) {
			t.Errorf("Call() = %#v, want %#v", answer, wantValidContent)
		}
	})
}

// -- T16 --

func TestFake_TextMarkerWinsOverHint(t *testing.T) {
	t.Run("text_unavailable_hint_answer", func(t *testing.T) {
		c := fakeModeClient(t)
		req := baseReq()
		req.Text = markerUnavailable
		req.FakeHint = answerMarker(validContent)

		answer, err := fakeCallWithin(t, c, req)
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
		if answer != nil {
			t.Errorf("answer = %#v, want nil", answer)
		}
	})

	// Reverse row: pins the precedence rather than a coincidence.
	t.Run("text_answer_hint_unavailable", func(t *testing.T) {
		c := fakeModeClient(t)
		req := baseReq()
		req.Text = answerMarker(validContent)
		req.FakeHint = markerUnavailable

		answer, err := fakeCallWithin(t, c, req)
		if err != nil {
			t.Fatalf("Call() err = %v, want nil", err)
		}
		if !reflect.DeepEqual(answer, wantValidContent) {
			t.Errorf("Call() = %#v, want %#v", answer, wantValidContent)
		}
	})
}
