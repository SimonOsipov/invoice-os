package platform

import (
	_ "embed"
	"strings"
)

// buildSHAFile is overwritten by CI before the image build, so the commit is
// compiled into the binary. During a rolling deploy /healthz answers 200 from the
// OLD revision, so only this stamp tells which commit the fleet is serving.
//
// Committed as "dev" so a local build, `go test ./...` and any non-CI path all
// work unchanged; only CI ever writes a real sha here.
//
//go:embed buildsha.txt
var buildSHAFile string

// BuildSHA is the commit this binary was built from, or "dev" outside CI.
// Reported by /healthz and aggregated per service by GET /healthz/fleet, which
// is what lets the deploy gate block until every service is genuinely serving
// the commit under test.
var BuildSHA = strings.TrimSpace(buildSHAFile)
