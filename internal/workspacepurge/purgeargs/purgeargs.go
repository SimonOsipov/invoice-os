// Package purgeargs holds the args of the workspace purge job. It imports River only, so tenancy links no S3 SDK.
package purgeargs

import "github.com/riverqueue/river"

// Args names the tenant to purge.
type Args struct {
	TenantID string `json:"tenant_id"`
}

// Stub for the red tests: DEL-09-03 sets the kind and MaxAttempts.
func (Args) Kind() string { return "" }

func (Args) InsertOpts() river.InsertOpts { return river.InsertOpts{} }
