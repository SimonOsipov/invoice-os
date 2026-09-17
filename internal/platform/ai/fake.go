// fake.go answers a Call without a network call, steered by a marker in the
// request. Stage 3 (AIR-02-02) wires fakeCall into client.go's call() and
// fills in the bodies below; today they are stubs so the package compiles
// ahead of the red tests in fake_test.go.
package ai

import (
	"errors"
	"regexp"
)

const (
	markerUnavailable  = "AIFAKE-UNAVAILABLE"
	markerAnswerPrefix = "AIFAKE-ANSWER-"
)

// markerRe finds the steering marker a test or a deployed spec plants in the
// input. Case-sensitive, compiled once at init.
var markerRe = regexp.MustCompile(`AIFAKE-(?:UNAVAILABLE|ANSWER-[A-Za-z0-9_-]+)`)

// fakeMarker returns the first marker in Text, else the first in FakeHint.
// Stub: Stage 3 fills this in.
func fakeMarker(req Request) string {
	return ""
}

// blankAnswer is every top-level property set to null (D3). Stub: Stage 3
// fills this in.
func blankAnswer(schema map[string]any) map[string]any {
	return nil
}

// fakeCall answers without a network call. Stub: Stage 3 fills this in.
func (c *Client) fakeCall(req Request, schema map[string]any) result {
	return result{err: errors.New("ai: not implemented")}
}
