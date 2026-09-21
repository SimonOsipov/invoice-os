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

// markerPayloadClass is the base64url alphabet an ANSWER marker's payload is read with, shared
// by markerRe and scopedMarkerRe (TestFake_ScopedPayloadClassRoundTripsDashAndUnderscore).
const markerPayloadClass = `[A-Za-z0-9_-]+`

// markerRe finds the steering marker a test or a deployed spec plants in the
// input. Case-sensitive, compiled once at init.
var markerRe = regexp.MustCompile(`AIFAKE-(?:UNAVAILABLE|ANSWER-` + markerPayloadClass + `)`)

// scopedMarkerRe builds AIFAKE-<SCOPE>-... for one non-empty FakeScope. validateRequest already
// enforced scope against ^[A-Z]+$, so it is safe to inline into the pattern.
func scopedMarkerRe(scope string) *regexp.Regexp {
	return regexp.MustCompile(`AIFAKE-` + scope + `-(?:UNAVAILABLE|ANSWER-` + markerPayloadClass + `)`)
}

// markerUnavailableFor and markerAnswerPrefixFor are the unavailable marker and the answer
// prefix for one FakeScope; empty scope is today's unscoped spelling, byte for byte.
func markerUnavailableFor(scope string) string {
	if scope == "" {
		return markerUnavailable
	}
	return "AIFAKE-" + scope + "-UNAVAILABLE"
}

func markerAnswerPrefixFor(scope string) string {
	if scope == "" {
		return markerAnswerPrefix
	}
	return "AIFAKE-" + scope + "-ANSWER-"
}

// fakeMarker returns the first marker in Text, else the first in FakeHint. A non-empty
// FakeScope searches only that scope's own spelling and never falls back to the unscoped one --
// falling back is what let one marker steer two calls sharing the same Text.
func fakeMarker(req Request) string {
	re := markerRe
	if req.FakeScope != "" {
		re = scopedMarkerRe(req.FakeScope)
	}
	if m := re.FindString(req.Text); m != "" {
		return m
	}
	return re.FindString(req.FakeHint)
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
	answerPrefix := markerAnswerPrefixFor(req.FakeScope)
	switch {
	case m == "":
		res.answer = blankAnswer(schema)
	case m == markerUnavailableFor(req.FakeScope):
		res.err = fmt.Errorf("%w (fake)", ErrUnavailable)
	default: // answerPrefix
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(m, answerPrefix))
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
