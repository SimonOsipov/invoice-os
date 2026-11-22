package erp

import "github.com/riverqueue/river"

// RefreshArgs renews one grant's access token. The tenant key is tenant_id: purge_tenant deletes a
// purged tenant's jobs by it. Version is the grant version the job claims.
type RefreshArgs struct {
	TenantID string `json:"tenant_id"`
	GrantID  string `json:"grant_id"`
	Version  int64  `json:"version"`
}

func (RefreshArgs) Kind() string     { return "erp_token_refresh" }
func (a RefreshArgs) Tenant() string { return a.TenantID }

// InsertOpts: eight attempts, the budget of the submission job.
func (RefreshArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 8} }
