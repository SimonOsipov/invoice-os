// Package purgeargs holds the args of the workspace purge job. It imports River only, so tenancy links no S3 SDK.
package purgeargs

import "github.com/riverqueue/river"

// Args names the tenant to purge.
type Args struct {
	TenantID string `json:"tenant_id"`
}

func (Args) Kind() string { return "workspace_purge" }

// ceiling: one attempt; the hourly workspace-purge ticker retries a started purge.
func (Args) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 1} }
