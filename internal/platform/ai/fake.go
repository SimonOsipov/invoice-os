// fake.go answers a Call without a network call, steered by a marker in the
// request.
package ai

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
)

const (
	markerUnavailable  = "AIFAKE-UNAVAILABLE"
	markerAnswerPrefix = "AIFAKE-ANSWER-"
)

// markerRe finds the steering marker a test or a deployed spec plants in the
// input. Case-sensitive, compiled once at init.
var markerRe = regexp.MustCompile(`AIFAKE-(?:UNAVAILABLE|ANSWER-[A-Za-z0-9_-]+)`)

// fakeMarker returns the first marker in Text, else the first in FakeHint.
func fakeMarker(req Request) string {
	if m := markerRe.FindString(req.Text); m != "" {
		return m
	}
	return markerRe.FindString(req.FakeHint)
}

// blankAnswer is every top-level property set to null (D3).
func blankAnswer(schema map[string]any) map[string]any {
	props, _ := schema["properties"].(map[string]any)
	blank := make(map[string]any, len(props))
	for key := range props {
		blank[key] = nil
	}
	return blank
}

// fakeCall answers without a network call. Attempts is 1.
func (c *Client) fakeCall(req Request, schema map[string]any) result {
	res := result{outcome: "fake", attempts: 1}
	m := fakeMarker(req)
	switch {
	case m == "":
		res.answer = blankAnswer(schema)
	case m == markerUnavailable:
		res.err = fmt.Errorf("%w (fake)", ErrUnavailable)
	default: // markerAnswerPrefix
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(m, markerAnswerPrefix))
		if err == nil {
			var answer map[string]any
			if answer, err = decodeAnswer(string(raw)); err == nil {
				if err = checkAnswer(schema, answer); err == nil {
					res.answer = answer
				}
			}
		}
		if err != nil {
			res.err = fmt.Errorf("ai: fake answer: %v", err)
		}
	}
	return res
}
