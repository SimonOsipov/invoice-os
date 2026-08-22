package archive

import (
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Request is a validated evidence-bundle query: entity + inclusive period.
type Request struct {
	EntityID string // validated uuid, raw caller string — NOT uuid.UUID
	From, To time.Time
}

// maxSlugBytes bounds bundleFilename's slug (AC-4).
const maxSlugBytes = 48

var slugRunPattern = regexp.MustCompile(`[^A-Za-z0-9]+`)

// parseRequest validates all three params as required (AC-2), diverging from
// internal/audit/handlers.go:70-72 where empty means "no filter" on purpose.
func parseRequest(query url.Values) (Request, string) {
	var r Request

	entityID := query.Get("entity_id")
	if entityID == "" {
		return Request{}, "entity_id is required"
	}
	if _, err := uuid.Parse(entityID); err != nil {
		return Request{}, "entity_id must be a well-formed uuid"
	}
	r.EntityID = entityID

	fromRaw := query.Get("from")
	if fromRaw == "" {
		return Request{}, "from is required"
	}
	from, err := time.Parse(time.RFC3339, fromRaw)
	if err != nil {
		return Request{}, "from must be an RFC3339 timestamp"
	}
	r.From = from

	toRaw := query.Get("to")
	if toRaw == "" {
		return Request{}, "to is required"
	}
	to, err := time.Parse(time.RFC3339, toRaw)
	if err != nil {
		return Request{}, "to must be an RFC3339 timestamp"
	}
	r.To = to

	if r.From.After(r.To) {
		return Request{}, "from must not be after to"
	}

	return r, ""
}

// bundleFilename slugs entityName, falling back to the entity uuid when nothing
// alphanumeric survives the slug (AC-4).
func bundleFilename(entityName string, r Request) string {
	slug := strings.Trim(slugRunPattern.ReplaceAllString(entityName, "-"), "-")
	if slug == "" {
		slug = r.EntityID
	} else if len(slug) > maxSlugBytes {
		slug = slug[:maxSlugBytes]
	}

	return "ASComply_evidence_" + slug + "_" + r.From.UTC().Format("20060102") +
		"_" + r.To.UTC().Format("20060102") + ".zip"
}
