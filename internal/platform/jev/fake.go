// fake.go answers an Ask without a network call, steered by a marker in State.
package jev

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const (
	markerDoubt        = "JEVFAKE-DOUBT"
	markerChoicePrefix = "JEVFAKE-CHOICE-"
	markerUnavailable  = "JEVFAKE-UNAVAILABLE"
	markerRefused      = "JEVFAKE-REFUSED"
)

// fakeMarkers lists the markers once, in table order; the matcher and the doc
// test read it.
var fakeMarkers = []string{markerDoubt, markerChoicePrefix, markerUnavailable, markerRefused}

// markerRe finds the leftmost marker in State. The choice payload is base64url,
// so a name with spaces survives as one token.
var markerRe = func() *regexp.Regexp {
	alts := make([]string, len(fakeMarkers))
	for i, m := range fakeMarkers {
		alts[i] = regexp.QuoteMeta(m)
		if m == markerChoicePrefix {
			alts[i] += `[A-Za-z0-9_-]+`
		}
	}
	return regexp.MustCompile(strings.Join(alts, "|"))
}()

var (
	errRefusedFake     = fmt.Errorf("%w (fake)", errRefused)
	errUnavailableFake = fmt.Errorf("%w (fake)", errUnavailable)
)

// fakeCall answers every question with no doubt unless a marker steers it.
// Attempts is 1 and usage is zero.
func fakeCall(req Request) result {
	res := result{outcome: "fake", attempts: 1}
	m := markerRe.FindString(req.State)
	doubt := m == markerDoubt
	steered := strings.HasPrefix(m, markerChoicePrefix)
	var choice string
	switch {
	case m == markerUnavailable:
		return res.fail(errUnavailableFake, "fake")
	case m == markerRefused:
		return res.fail(errRefusedFake, "fake")
	case steered:
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(m, markerChoicePrefix))
		if err != nil {
			return res.fail(errRefusedFake, "fake")
		}
		choice = string(raw)
	}

	answers := make(map[string]Answer, len(req.Questions))
	for id, q := range req.Questions {
		switch q.Type {
		case TypeNoul:
			a := Answer{Type: TypeNoul, Noul: 1}
			if doubt {
				a.Noul = 0
			}
			answers[id] = a
		case TypeChoice:
			name := q.Default
			if steered {
				if !slices.ContainsFunc(q.Options, func(o Option) bool { return o.Name == choice }) {
					return res.fail(errRefusedFake, "fake")
				}
				name = choice
			}
			answers[id] = Answer{Type: TypeChoice, Choice: name, Confidence: 1}
		case TypeScore:
			level := slices.IndexFunc(q.Options, func(o Option) bool { return o.Name == q.Default })
			answers[id] = Answer{Type: TypeScore, Score: float64(level), Confidence: 1}
		}
	}
	res.resp = Response{Answers: answers}
	return res
}
