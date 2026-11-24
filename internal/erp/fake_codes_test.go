package erp

import (
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/erp/erpweb"
)

func TestFakeSeed_CoversTheConsentPageCompanies(t *testing.T) {
	var keys []string
	for k := range fakeSeed {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := slices.Sorted(slices.Values(erpweb.FakeCompanies)); !slices.Equal(keys, want) {
		t.Errorf("fakeSeed companies = %v, want the consent page's %v", keys, want)
	}
}
