package codelist

// DefaultBaseURL is the public NRS resources API; a list is served at {DefaultBaseURL}/{List.Name}.
const DefaultBaseURL = "https://einvoice1.nrs.gov.ng/api/v1/invoice/resources"

// List is one NRS code list. CodeKey is the entry field that holds the code.
type List struct {
	Name    string
	CodeKey string
}

var Lists = []List{
	{Name: "currencies", CodeKey: "code"},
	{Name: "vat-exemptions", CodeKey: "harmonized_system_code"},
	{Name: "hs-codes", CodeKey: "hscode"},
	{Name: "services-codes", CodeKey: "code"},
	{Name: "countries", CodeKey: "alpha_2"},
	{Name: "states", CodeKey: "code"},
	{Name: "lgas", CodeKey: "code"},
	{Name: "invoice-quantity-codes", CodeKey: "code"},
	{Name: "invoice-types", CodeKey: "code"},
	{Name: "payment-means", CodeKey: "code"},
	{Name: "tax-categories", CodeKey: "code"},
}
