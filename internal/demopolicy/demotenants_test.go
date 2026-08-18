// This package arms only the two persona tenants, so its allowlist must stay a
// strict subset of the purge allowlist in internal/platform/db
// ([allowlist-in-platform-db]).
package demopolicy

import (
	"testing"

	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func TestPurgeAllowlistContainsThePersonaTenants(t *testing.T) {
	if len(DemoTenants) == 0 {
		t.Fatal("demopolicy.DemoTenants is empty — a subset assertion over it holds vacuously")
	}
	if len(DemoTenants) >= len(db.DemoTenants) {
		t.Fatalf("demopolicy.DemoTenants holds %d tenants and db.DemoTenants %d — the persona list must be a STRICT subset, so a widening here cannot silently become the purge allowlist", len(DemoTenants), len(db.DemoTenants))
	}
	allowed := map[string]bool{}
	for _, id := range db.DemoTenants {
		allowed[id] = true
	}
	for _, id := range DemoTenants {
		if !allowed[id] {
			t.Errorf("demopolicy seeds tenant %s, which db.DemoTenants does not list — an approval policy would be published onto a tenant the purge never clears", id)
		}
	}
}
