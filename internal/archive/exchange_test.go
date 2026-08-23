// exchange_test.go: RED specs for AUDIT-05-05 (Mode A) -- header/no-join guards, no
// DB needed. Mirrors history_test.go/request_test.go's static-check shape.
package archive

import (
	"strings"
	"testing"
)

func countColumn(header []string, name string) int {
	n := 0
	for _, col := range header {
		if col == name {
			n++
		}
	}
	return n
}

// AC-5 / D-10: poll_ref appears in submissions.csv and in NO other CSV.
func TestBundleHeaders_PollRefAppearsInExactlyOneCSVColumn(t *testing.T) {
	if got := countColumn(submissionsCSVHeader, "poll_ref"); got != 1 {
		t.Errorf("submissionsCSVHeader carries poll_ref %d times, want exactly 1", got)
	}
	others := map[string][]string{
		"exchangeCSVHeader": exchangeCSVHeader,
		"invoicesCSVHeader": invoicesCSVHeader,
		"historyCSVHeader":  historyCSVHeader,
	}
	for name, h := range others {
		if got := countColumn(h, "poll_ref"); got != 0 {
			t.Errorf("%s carries poll_ref (count=%d), want 0 -- poll_ref belongs only in submissions.csv (D-10)", name, got)
		}
	}
}

// Mirrors TestHistorySQL_ContainsNoJoinAgainstInvoices: invoice_number comes from
// invoiceNumbers, never a JOIN against invoices.
func TestExchangeSQL_ContainsNoJoinAgainstInvoices(t *testing.T) {
	if selectExchangeSQL == "" {
		t.Fatal("selectExchangeSQL is empty -- want the declared query")
	}
	if strings.Contains(selectExchangeSQL, "invoices") {
		t.Errorf("selectExchangeSQL mentions %q -- invoice_number must come from invoiceNumbers, never a JOIN:\n%s",
			"invoices", selectExchangeSQL)
	}
}
