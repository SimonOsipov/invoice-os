// The purge allowlist lives in internal/platform/db ([allowlist-in-platform-db]);
// this package keeps its own copy because db cannot import it back. Drift
// between the two is a red test rather than a refactor.
package demodocs

import (
	"reflect"
	"sort"
	"testing"

	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func TestPurgeAllowlistMatchesDemodocs(t *testing.T) {
	got := append([]string{}, DemoTenants...)
	want := append([]string{}, db.DemoTenants...)
	if len(got) == 0 || len(want) == 0 {
		t.Fatalf("one of the allowlists is empty (demodocs=%d, db=%d) — comparing them would prove nothing", len(got), len(want))
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("demodocs.DemoTenants and db.DemoTenants disagree — the seeder would write documents onto a tenant the purge never clears, or skip one it does\ndemodocs: %v\ndb:       %v", got, want)
	}
}
