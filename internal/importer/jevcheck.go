package importer

import (
	"context"

	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

// MappingChecker is the slice of *jev.Client this package uses; nil is off.
type MappingChecker interface {
	Enabled() bool
	Ask(ctx context.Context, req jev.Request) (jev.Response, error)
}

// Stub: the measured cut and wording land with the implementation.
const mappingCheckThreshold = 0

const (
	mappingCheckInstructions = ""
	mappingCheckTrue         = ""
	mappingCheckFalse        = ""
)

func mappingCheckQuestion(field, header string) jev.Question {
	return jev.Question{}
}

// checkPlacements returns the doubted fields, sorted; never nil. Any skip returns an empty slice.
func checkPlacements(ctx context.Context, c MappingChecker, window [][]string, placements map[string]string) []string {
	return []string{}
}
