package archive

import (
	"net/url"
	"regexp"
	"time"
)

// Request is a validated evidence-bundle query: entity + inclusive period.
type Request struct {
	EntityID string // validated uuid, raw caller string — NOT uuid.UUID
	From, To time.Time
}

// maxSlugBytes bounds bundleFilename's slug (AC-4).
const maxSlugBytes = 48

var slugRunPattern = regexp.MustCompile(`[^A-Za-z0-9]+`)

// parseRequest: Mode A scaffold (AUDIT-05-01) — validation lands in the implementation
// subtask; this stub only keeps the package compiling.
func parseRequest(query url.Values) (Request, string) {
	return Request{}, ""
}

// bundleFilename: Mode A scaffold, see parseRequest.
func bundleFilename(entityName string, r Request) string {
	return ""
}
