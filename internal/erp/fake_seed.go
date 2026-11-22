package erp

type seedInvoice struct {
	id, number, date, customerID string
	voided                       bool
}

type seedCustomer struct{ id, name, tin, email string }

// Every seeded customer sits in one state and LGA from the code-list fixtures.
const (
	fakeState = "NG-AB"
	fakeLGA   = "NG-AB-ANO"
)

type seedCompany struct {
	invoices  []seedInvoice
	customers []seedCustomer
}

// Invoice counts (3, 4, 6) differ so a query that drops its scope shows in a count;
// each exceeds the page size.
var fakeSeed = map[string]seedCompany{
	"fake-co-1": {
		invoices: []seedInvoice{
			{"fake-co-1-inv-1", "FK1-0001", "2026-01-05", "fake-co-1-cust-1", false},
			{"fake-co-1-inv-2", "FK1-0002", "2026-01-12", "fake-co-1-cust-2", false},
			{"fake-co-1-inv-3", "FK1-0003", "2026-01-19", "fake-co-1-cust-1", false},
		},
		customers: []seedCustomer{
			{"fake-co-1-cust-1", "Adewale Traders Ltd", "1234567897", "accounts@adewale.example"},
			{"fake-co-1-cust-2", "Bola Foods Ltd", "2345678904", "ap@bolafoods.example"},
		},
	},
	"fake-co-2": {
		invoices: []seedInvoice{
			{"fake-co-2-inv-1", "FK2-0001", "2026-02-02", "fake-co-2-cust-1", false},
			{"fake-co-2-inv-2", "FK2-0002", "2026-02-09", "fake-co-2-cust-2", false},
			{"fake-co-2-inv-3", "FK2-0003", "2026-02-16", "fake-co-2-cust-1", false},
			{"fake-co-2-inv-4", "FK2-0004", "2026-02-23", "fake-co-2-cust-2", false},
		},
		customers: []seedCustomer{
			{"fake-co-2-cust-1", "Chidi Logistics Ltd", "3456789019", "finance@chidi.example"},
			{"fake-co-2-cust-2", "Dayo Textiles Ltd", "4567890126", "pay@dayo.example"},
		},
	},
	"fake-co-3": {
		invoices: []seedInvoice{
			{"fake-co-3-inv-1", "FK3-0001", "2026-03-02", "fake-co-3-cust-1", false},
			{"fake-co-3-inv-2", "FK3-0002", "2026-03-09", "fake-co-3-cust-1", false},
			{"fake-co-3-inv-3", "FK3-0003", "2026-03-16", "fake-co-3-cust-1", false},
			{"fake-co-3-inv-4", "FK3-0004", "2026-03-23", "fake-co-3-cust-1", false},
			{"fake-co-3-inv-5", "FK3-0005", "2026-03-30", "fake-co-3-cust-1", false},
			{"fake-co-3-inv-6", "FK3-0006", "2026-04-06", "fake-co-3-cust-1", true},
		},
		customers: []seedCustomer{
			{"fake-co-3-cust-1", "Emeka Steel Ltd", "5678901231", "ar@emeka.example"},
		},
	},
}
