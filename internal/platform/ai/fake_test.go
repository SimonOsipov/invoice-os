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

// fakeCallWithin bounds every Call in this file and fails the test if the call
// dialled anything. A fake-mode Call answers in microseconds, so the 2s window
// only fires when a regression falls through to the real retry loop, and the
// dial counter catches a dial that never reaches noDialServer's handler.
func fakeCallWithin(t *testing.T, c *Client, req Request) (map[string]any, error) {
	t.Helper()
	var answer map[string]any
	var err error
	noDials(t, func() {
		answer, err = callWithin(t, c, t.Context(), req, 2*time.Second)
	})
	return answer, err
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

	var answer map[string]any
	var err error
	start := time.Now()
	noDials(t, func() {
		answer, err = callWithin(t, c, t.Context(), req, time.Second)
	})
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
		// Not base64url at all, so the regexp never matches: this reads as
		// no marker (blank), not as a bad answer.
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

// -- QA adversarial coverage --

const otherContent = `{"total":"7.00","vat":null,"currency":"USD","n":1}`

var wantOtherContent = map[string]any{"total": "7.00", "vat": nil, "currency": "USD", "n": json.Number("1")}

// offClient is a client with no key and fake off, whose endpoint fails the
// test if dialled.
func offClient(t *testing.T) *Client {
	t.Helper()
	return newClient(config{endpoint: noDialServer(t).URL, budget: budget, now: time.Now, sleep: realSleep}, nil)
}

// TestFake_MarkerSplitAcrossTextAndHintDoesNotMatch: each field is scanned on
// its own, so two halves never join into a marker.
func TestFake_MarkerSplitAcrossTextAndHintDoesNotMatch(t *testing.T) {
	cases := map[string]struct{ text, hint string }{
		"unavailable_halves": {"AIFAKE-UNAVAIL", "ABLE"},
		"answer_halves":      {"AIFAKE-ANSW", "ER-" + base64.RawURLEncoding.EncodeToString([]byte(validContent))},
		"prefix_then_rest":   {"AIFAKE-", "UNAVAILABLE"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := fakeModeClient(t)
			req := baseReq()
			req.Text = tc.text
			req.FakeHint = tc.hint

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

// TestFake_MarkerNeedsNoWordBoundary pins the grammar's documented
// consequence: the regexp has no boundary, so a marker embedded in a longer
// token still steers the call.
func TestFake_MarkerNeedsNoWordBoundary(t *testing.T) {
	cases := map[string]string{
		"leading_word_chars":  "scanAIFAKE-UNAVAILABLE",
		"trailing_word_chars": "AIFAKE-UNAVAILABLEX",
		"filename_shaped":     "scan-AIFAKE-UNAVAILABLE.pdf",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			c := fakeModeClient(t)
			req := baseReq()
			req.Text = text

			answer, err := fakeCallWithin(t, c, req)
			if !errors.Is(err, ErrUnavailable) {
				t.Errorf("err = %v, want ErrUnavailable", err)
			}
			if answer != nil {
				t.Errorf("answer = %#v, want nil", answer)
			}
		})
	}
}

// TestFake_TrailingWordCharsCorruptTheAnswerPayload is the other side of the
// missing boundary: word characters after an answer marker are swallowed into
// its payload, which then decodes to trailing bytes and fails.
func TestFake_TrailingWordCharsCorruptTheAnswerPayload(t *testing.T) {
	c := fakeModeClient(t)
	req := baseReq()
	req.Text = answerMarker(validContent) + "TRAILING"

	answer, err := fakeCallWithin(t, c, req)
	if err == nil {
		t.Fatal("err = nil, want non-nil")
	}
	if errors.Is(err, ErrUnavailable) {
		t.Error("err wraps ErrUnavailable, want a plain error")
	}
	if answer != nil {
		t.Errorf("answer = %#v, want nil", answer)
	}
	if !strings.Contains(err.Error(), "fake answer") {
		t.Errorf("err = %q, want it to contain %q", err.Error(), "fake answer")
	}
}

// TestFake_TwoMarkersOfTheSameKind: the answer rows carry distinct payloads,
// so a rule that took the last marker instead of the first would red.
func TestFake_TwoMarkersOfTheSameKind(t *testing.T) {
	t.Run("two_unavailable", func(t *testing.T) {
		c := fakeModeClient(t)
		req := baseReq()
		req.Text = markerUnavailable + " then " + markerUnavailable

		answer, err := fakeCallWithin(t, c, req)
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
		if answer != nil {
			t.Errorf("answer = %#v, want nil", answer)
		}
	})

	pairs := map[string]struct {
		text string
		want map[string]any
	}{
		"valid_then_other": {answerMarker(validContent) + " then " + answerMarker(otherContent), wantValidContent},
		"other_then_valid": {answerMarker(otherContent) + " then " + answerMarker(validContent), wantOtherContent},
	}
	for name, tc := range pairs {
		t.Run(name, func(t *testing.T) {
			c := fakeModeClient(t)
			req := baseReq()
			req.Text = tc.text

			answer, err := fakeCallWithin(t, c, req)
			if err != nil {
				t.Fatalf("Call() err = %v, want nil", err)
			}
			if !reflect.DeepEqual(answer, tc.want) {
				t.Errorf("Call() = %#v, want %#v", answer, tc.want)
			}
		})
	}
}

// TestFake_AnswerWithAnExtraKeyIsRefused: the fake runs the same checkAnswer
// as the real path, so additionalProperties:false still bites.
func TestFake_AnswerWithAnExtraKeyIsRefused(t *testing.T) {
	c := fakeModeClient(t)
	req := baseReq()
	req.Text = answerMarker(`{"total":"1","vat":null,"currency":"NGN","n":1,"extra":true}`)

	answer, err := fakeCallWithin(t, c, req)
	if err == nil {
		t.Fatal("err = nil, want non-nil")
	}
	if errors.Is(err, ErrUnavailable) {
		t.Error("err wraps ErrUnavailable, want a plain error")
	}
	if answer != nil {
		t.Errorf("answer = %#v, want nil", answer)
	}
	if !strings.Contains(err.Error(), "fake answer") {
		t.Errorf("err = %q, want it to contain %q", err.Error(), "fake answer")
	}
}

// TestFake_MarkerIsTheWholeText: a request whose entire text is a marker is
// still valid input, so it steers rather than refuses.
func TestFake_MarkerIsTheWholeText(t *testing.T) {
	t.Run("unavailable_only", func(t *testing.T) {
		c := fakeModeClient(t)
		req := baseReq()
		req.Text = markerUnavailable

		answer, err := fakeCallWithin(t, c, req)
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
		if answer != nil {
			t.Errorf("answer = %#v, want nil", answer)
		}
	})

	t.Run("answer_only", func(t *testing.T) {
		c := fakeModeClient(t)
		req := baseReq()
		req.Text = answerMarker(validContent)

		answer, err := fakeCallWithin(t, c, req)
		if err != nil {
			t.Fatalf("Call() err = %v, want nil", err)
		}
		if !reflect.DeepEqual(answer, wantValidContent) {
			t.Errorf("Call() = %#v, want %#v", answer, wantValidContent)
		}
	})
}

// TestFake_SchemaWithNoPropertiesAnswersAnEmptyObject: a schema that declares
// no fields has no fields to blank, so the answer is an empty object -- not
// nil, which would read as "no answer".
func TestFake_SchemaWithNoPropertiesAnswersAnEmptyObject(t *testing.T) {
	cases := map[string]json.RawMessage{
		"properties_absent": json.RawMessage(`{"type":"object","additionalProperties":false}`),
		"properties_empty":  json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
	}
	for name, schema := range cases {
		t.Run(name, func(t *testing.T) {
			c := fakeModeClient(t)
			req := baseReq()
			req.Schema = schema

			answer, err := fakeCallWithin(t, c, req)
			if err != nil {
				t.Fatalf("Call() err = %v, want nil", err)
			}
			if answer == nil {
				t.Fatal("answer = nil, want an empty object")
			}
			if len(answer) != 0 {
				t.Errorf("answer = %#v, want an empty object", answer)
			}
		})
	}
}

// TestFake_BlankIsNotCheckedAgainstTheSchema pins D3's deliberate asymmetry:
// the blank default is returned even when a non-nullable required property
// makes it fail the very check a decoded answer must pass.
func TestFake_BlankIsNotCheckedAgainstTheSchema(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["currency"],"properties":{"currency":{"type":"string"}}}`)

	c := fakeModeClient(t)
	req := baseReq()
	req.Schema = raw

	answer, err := fakeCallWithin(t, c, req)
	if err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}
	want := map[string]any{"currency": nil}
	if !reflect.DeepEqual(answer, want) {
		t.Fatalf("Call() = %#v, want %#v", answer, want)
	}

	// The same answer through checkAnswer is an error: that is what the blank
	// path skips, and a caller running its own check must expect it.
	schema, err := checkSchema(raw)
	if err != nil {
		t.Fatalf("checkSchema() err = %v, want nil", err)
	}
	if err := checkAnswer(schema, answer); err == nil {
		t.Error("checkAnswer(blank) err = nil, want non-nil: this schema's blank is not schema-valid")
	}
}

// TestBlankAnswer_HandlesASchemaWithoutUsableProperties covers the defensive
// type assertion. checkSchema refuses these shapes, so Call cannot deliver one
// (see the sibling test below); blankAnswer must not panic regardless.
func TestBlankAnswer_HandlesASchemaWithoutUsableProperties(t *testing.T) {
	cases := map[string]map[string]any{
		"nil_schema":             nil,
		"no_properties_key":      {"type": "object"},
		"properties_is_a_string": {"properties": "nope"},
		"properties_is_an_array": {"properties": []any{"a"}},
	}
	for name, schema := range cases {
		t.Run(name, func(t *testing.T) {
			got := blankAnswer(schema)
			if got == nil {
				t.Fatal("blankAnswer() = nil, want an empty object")
			}
			if len(got) != 0 {
				t.Errorf("blankAnswer() = %#v, want an empty object", got)
			}
		})
	}

	t.Run("two_properties_are_both_blanked", func(t *testing.T) {
		got := blankAnswer(map[string]any{"properties": map[string]any{
			"a": map[string]any{"type": "string"},
			"b": map[string]any{"type": "integer"},
		}})
		want := map[string]any{"a": nil, "b": nil}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("blankAnswer() = %#v, want %#v", got, want)
		}
	})
}

// TestFake_RefusesASchemaWhosePropertiesIsNotAnObject: the shape blankAnswer
// defends against never reaches it through Call.
func TestFake_RefusesASchemaWhosePropertiesIsNotAnObject(t *testing.T) {
	req := baseReq()
	req.Schema = json.RawMessage(`{"type":"object","properties":"nope"}`)
	assertFakeRefused(t, req)
}

// TestCall_OffAndFakeSetTheOutcomeAndAttempts reads the internal result:
// AIR-02-03 logs outcome and attempts, and nothing else asserts them.
func TestCall_OffAndFakeSetTheOutcomeAndAttempts(t *testing.T) {
	cases := map[string]struct {
		client       func(*testing.T) *Client
		req          func() Request
		wantOutcome  string
		wantAttempts int
	}{
		"off": {offClient, baseReq, "off", 0},
		"fake_blank": {fakeModeClient, func() Request {
			r := baseReq()
			r.Text = "invoice 123"
			return r
		}, "fake", 1},
		"fake_unavailable": {fakeModeClient, func() Request {
			r := baseReq()
			r.Text = markerUnavailable
			return r
		}, "fake", 1},
		"fake_bad_answer": {fakeModeClient, func() Request {
			r := baseReq()
			r.Text = answerMarker("not json")
			return r
		}, "fake", 1},
		"fake_refused_request": {fakeModeClient, func() Request {
			r := baseReq()
			r.Purpose = "x"
			return r
		}, "refused", 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := tc.client(t)
			var res result
			noDials(t, func() {
				res = c.call(t.Context(), tc.req())
			})
			if res.outcome != tc.wantOutcome {
				t.Errorf("outcome = %q, want %q", res.outcome, tc.wantOutcome)
			}
			if res.attempts != tc.wantAttempts {
				t.Errorf("attempts = %d, want %d", res.attempts, tc.wantAttempts)
			}
			if res.usage != (usage{}) {
				t.Errorf("usage = %#v, want the zero usage", res.usage)
			}
		})
	}
}

// TestCall_OffAnswersBeforeItReadsTheRequest: the off guard runs ahead of
// validation, so an off client answers ErrOff whether or not the request
// would have survived validation.
func TestCall_OffAnswersBeforeItReadsTheRequest(t *testing.T) {
	cases := map[string]func() Request{
		"valid_request": baseReq,
		"empty_request": func() Request { return Request{} },
		"bad_purpose": func() Request {
			r := baseReq()
			r.Purpose = "x"
			return r
		},
		"marker_in_text": func() Request {
			r := baseReq()
			r.Text = markerUnavailable
			return r
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			c := offClient(t)
			var answer map[string]any
			var err error
			noDials(t, func() {
				answer, err = callWithin(t, c, t.Context(), build(), 2*time.Second)
			})
			if !errors.Is(err, ErrOff) {
				t.Errorf("err = %v, want ErrOff", err)
			}
			if errors.Is(err, ErrUnavailable) {
				t.Error("err wraps ErrUnavailable, want ErrOff only")
			}
			if answer != nil {
				t.Errorf("answer = %#v, want nil", answer)
			}
		})
	}
}
