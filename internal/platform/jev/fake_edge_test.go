package jev

import (
	"testing"
)

// twoChoiceReq is fakeReq plus a second choice with a different Default.
func twoChoiceReq(state string, secondOptions []Option) Request {
	req := fakeReq(state)
	req.Questions["c2"] = Question{Type: TypeChoice, Instructions: "Which currency?", Options: secondOptions, Default: secondOptions[0].Name}
	return req
}

func TestFake_ChoiceMarkerSteersEveryChoiceQuestion(t *testing.T) {
	c, ts := fakeClient(t)
	req := twoChoiceReq(choiceMarker("credit note")+" end", []Option{{"NGN", ""}, {"credit note", ""}, {"USD", ""}})
	want := defaultAnswers()
	want["c"] = Answer{Type: TypeChoice, Choice: "credit note", Confidence: 1}
	want["c2"] = Answer{Type: TypeChoice, Choice: "credit note", Confidence: 1}

	resp, err := askFake(t, c, ts, req)
	wantAnswers(t, resp, err, want)
}

func TestFake_ChoiceMarkerRefusesWhenAnyChoiceLacksTheName(t *testing.T) {
	c, ts := fakeClient(t)
	req := twoChoiceReq(choiceMarker("credit note")+" end", []Option{{"NGN", ""}, {"USD", ""}})

	resp, err := askFake(t, c, ts, req)
	wantSkipped(t, err, textRefused+" (fake)")
	wantNoAnswers(t, resp)
}

// The payload class needs one character, so a bare prefix is no marker and a later one still steers.
func TestFake_ABareChoicePrefixIsNotAMarker(t *testing.T) {
	doubted := defaultAnswers()
	doubted["n1"] = Answer{Type: TypeNoul, Noul: 0}
	doubted["n2"] = Answer{Type: TypeNoul, Noul: 0}

	for name, tc := range map[string]struct {
		state string
		want  map[string]Answer
	}{
		"alone":            {"scan " + markerChoicePrefix + " end", defaultAnswers()},
		"at_the_end":       {"scan " + markerChoicePrefix, defaultAnswers()},
		"then_doubt":       {markerChoicePrefix + " " + markerDoubt, doubted},
		"then_punctuation": {markerChoicePrefix + ".pdf", defaultAnswers()},
	} {
		t.Run(name, func(t *testing.T) {
			c, ts := fakeClient(t)

			resp, err := askFake(t, c, ts, fakeReq(tc.state))
			wantAnswers(t, resp, err, tc.want)
		})
	}

	t.Run("then_unavailable", func(t *testing.T) {
		c, ts := fakeClient(t)

		resp, err := askFake(t, c, ts, fakeReq(markerChoicePrefix+" "+markerUnavailable))
		wantSkipped(t, err, textUnavailable+" (fake)")
		wantNoAnswers(t, resp)
	})
}

// Complements TestFake_MarkersAreCaseSensitiveAndReadFromStateOnly with the fields it does not cover.
func TestFake_MarkersOutsideStateDoNotSteer(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(r *Request)
		want   func() map[string]Answer
	}{
		{"only_in_false", func(r *Request) {
			setQ(r, "n2", func(q *Question) { q.False = markerDoubt })
		}, defaultAnswers},
		{"only_in_an_option_name", func(r *Request) {
			setQ(r, "c", func(q *Question) { q.Options[0].Name = markerRefused })
		}, defaultAnswers},
		{"only_in_a_score_level_description", func(r *Request) {
			setQ(r, "s", func(q *Question) { q.Options[0].Description = markerUnavailable })
		}, defaultAnswers},
		{"only_in_a_question_id", func(r *Request) {
			r.Questions[markerUnavailable] = r.Questions["n1"]
			delete(r.Questions, "n1")
		}, func() map[string]Answer {
			want := defaultAnswers()
			want[markerUnavailable] = want["n1"]
			delete(want, "n1")
			return want
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, ts := fakeClient(t)
			req := fakeReq("s")
			tc.mutate(&req)

			resp, err := askFake(t, c, ts, req)
			wantAnswers(t, resp, err, tc.want())
		})
	}
}
