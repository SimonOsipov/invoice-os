package gateway

import "time"

const (
	revocationSlack       = time.Second
	revocationKeep        = 2 * SessionCheckTTL
	revocationReadTimeout = time.Second
)

// revocationWindow is how far back a sign-out row can sit and still invalidate a cache entry of this age.
func revocationWindow(age time.Duration) time.Duration { return 0 }
