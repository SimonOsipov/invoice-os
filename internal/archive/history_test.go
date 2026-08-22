// history_test.go: RED specs for AUDIT-05-04 (Mode A) -- header/no-join guards,
// no DB needed.
package archive

import (
	"strings"
	"testing"
)

// AC-7: the CSV must carry actor_name/actor_kind and no separate raw-subject
// column (no "actor", "subject" or "actor_subject").
func TestHistoryHeader_CarriesNoRawSubjectColumn(t *testing.T) {
	if len(historyCSVHeader) == 0 {
		t.Fatal("historyCSVHeader is empty -- want the declared 8-column header (AC-7)")
	}
	var hasName, hasKind bool
	for _, col := range historyCSVHeader {
		switch col {
		case "actor_name":
			hasName = true
		case "actor_kind":
			hasKind = true
		case "actor", "subject", "actor_subject":
			t.Errorf("historyCSVHeader contains raw-subject column %q -- AC-7 forbids a column "+
				"holding the raw subject separately from actor_name", col)
		}
	}
	if !hasName {
		t.Error("historyCSVHeader has no actor_name column")
	}
	if !hasKind {
		t.Error("historyCSVHeader has no actor_kind column")
	}
}

// Mirrors AUDIT-05-05's TestExchangeSQL_ContainsNoJoinAgainstInvoices: guards the
// invoiceNumbers-not-JOIN decision (invoice_number comes from a separate lookup).
func TestHistorySQL_ContainsNoJoinAgainstInvoices(t *testing.T) {
	if selectHistorySQL == "" {
		t.Fatal("selectHistorySQL is empty -- want the declared query")
	}
	if strings.Contains(selectHistorySQL, "invoices") {
		t.Errorf("selectHistorySQL mentions %q -- invoice_number must come from invoiceNumbers, "+
			"never a JOIN against invoices:\n%s", "invoices", selectHistorySQL)
	}
}
