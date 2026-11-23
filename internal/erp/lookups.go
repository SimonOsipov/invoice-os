package erp

// Lookup is one ERP key's NRS values; kind is tax_code, item or customer.
type Lookup struct {
	Kind          string  `json:"kind"`
	ERPKey        string  `json:"erp_key"`
	TaxCategory   *string `json:"tax_category"`
	ExemptionCode *string `json:"exemption_code"`
	HSNCode       *string `json:"hsn_code"`
	ServiceCode   *string `json:"service_code"`
	TIN           *string `json:"tin"`
	State         *string `json:"state"`
	LGA           *string `json:"lga"`
}
