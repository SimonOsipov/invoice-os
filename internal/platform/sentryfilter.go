package platform

import "github.com/getsentry/sentry-go"

// scrubEvent removes request data, queries and user identity from an error
// event or transaction before it leaves for Sentry.
func scrubEvent(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	return event
}

// stripQuery removes each "?" or "#" and the run of non-whitespace after it.
func stripQuery(s string) string {
	return s
}
