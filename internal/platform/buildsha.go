package platform

import (
	_ "embed"
	"strings"
)

// buildSHAFile is overwritten by CI immediately before `railway up`, so the
// commit travels inside the uploaded tarball and ends up compiled into the
// binary. `railway up` returns after the BUILD, not after the new container is
// serving, and /healthz answers 200 from the OLD container throughout a rolling
// deploy -- so "the fleet is healthy" never meant "the fleet is running this
// commit", and the E2E suite could and did verify the previous one.
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
