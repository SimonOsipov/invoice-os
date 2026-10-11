package invoice

import "testing"

func fullContact() SupplierContact {
	return SupplierContact{
		Email: strPtr("co@x.ng"), Telephone: strPtr("+2348000000001"), Street: strPtr("1 Marina"),
		City: strPtr("Ikeja"), PostalZone: strPtr("100001"), Country: strPtr("NG"),
		State: strPtr("NG-LA"), LGA: strPtr("NG-LA-001"),
	}
}

func TestDefaultSupplierContact_FillsOnlyNilFields(t *testing.T) {
	in := CreateInput{SupplierEmail: strPtr("a@x.ng")}
	DefaultSupplierContact(&in, fullContact())
	for name, g := range map[string][2]*string{
		"Email": {in.SupplierEmail, strPtr("a@x.ng")}, "Telephone": {in.SupplierTelephone, strPtr("+2348000000001")},
		"Street": {in.SupplierStreet, strPtr("1 Marina")}, "City": {in.SupplierCity, strPtr("Ikeja")},
		"PostalZone": {in.SupplierPostalZone, strPtr("100001")}, "Country": {in.SupplierCountry, strPtr("NG")},
		"State": {in.SupplierState, strPtr("NG-LA")}, "LGA": {in.SupplierLGA, strPtr("NG-LA-001")},
	} {
		if g[0] == nil || *g[0] != *g[1] {
			t.Errorf("Supplier%s = %v, want %q", name, g[0], *g[1])
		}
	}
}

func TestDefaultSupplierContact_SentEmptyStringWins(t *testing.T) {
	in := CreateInput{SupplierCity: strPtr("")}
	DefaultSupplierContact(&in, fullContact())
	if in.SupplierCity == nil || *in.SupplierCity != "" {
		t.Fatalf("SupplierCity = %v, want the sent empty string", in.SupplierCity)
	}
}

func TestDefaultSupplierContact_BlankCompanyValueIsNoDefault(t *testing.T) {
	in := CreateInput{}
	DefaultSupplierContact(&in, SupplierContact{State: strPtr("  ")})
	if in.SupplierState != nil || in.SupplierLGA != nil {
		t.Fatalf("State/LGA = %v/%v, want nil/nil", in.SupplierState, in.SupplierLGA)
	}
}

func TestDefaultSupplierContact_TrimsTheCompanyValue(t *testing.T) {
	in := CreateInput{}
	DefaultSupplierContact(&in, SupplierContact{City: strPtr("  Ikeja ")})
	if in.SupplierCity == nil || *in.SupplierCity != "Ikeja" {
		t.Fatalf("SupplierCity = %v, want Ikeja", in.SupplierCity)
	}
}
