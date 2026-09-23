// fake.go answers an Ask without a network call, steered by a marker in State.
package jev

const (
	markerDoubt        = "JEVFAKE-DOUBT"
	markerChoicePrefix = "JEVFAKE-CHOICE-"
	markerUnavailable  = "JEVFAKE-UNAVAILABLE"
	markerRefused      = "JEVFAKE-REFUSED"
)

// fakeMarkers lists the markers once, in table order; the matcher and the doc
// test read it.
var fakeMarkers []string
