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

// contactFields pairs each CreateInput contact field with its SupplierContact twin.
func contactFields(in *CreateInput, c *SupplierContact) []struct {
	name string
	dst  **string
	src  **string
} {
	return []struct {
		name string
		dst  **string
		src  **string
	}{
		{"Email", &in.SupplierEmail, &c.Email}, {"Telephone", &in.SupplierTelephone, &c.Telephone},
		{"Street", &in.SupplierStreet, &c.Street}, {"City", &in.SupplierCity, &c.City},
		{"PostalZone", &in.SupplierPostalZone, &c.PostalZone}, {"Country", &in.SupplierCountry, &c.Country},
		{"State", &in.SupplierState, &c.State}, {"LGA", &in.SupplierLGA, &c.LGA},
	}
}

func TestDefaultSupplierContact_EachFieldDefaultsIndependently(t *testing.T) {
	n := len(contactFields(&CreateInput{}, &SupplierContact{}))
	if n != 8 {
		t.Fatalf("contact fields = %d, want 8", n)
	}
	for i := 0; i < n; i++ {
		in, c := CreateInput{}, SupplierContact{}
		fs := contactFields(&in, &c)
		*fs[i].src = strPtr("v-" + fs[i].name)
		DefaultSupplierContact(&in, c)
		for j, f := range contactFields(&in, &c) {
			if j == i {
				if *f.dst == nil || **f.dst != "v-"+f.name {
					t.Errorf("%s alone: got %v, want its own value", f.name, *f.dst)
				}
			} else if *f.dst != nil {
				t.Errorf("%s set while only %s had a company value", f.name, fs[i].name)
			}
		}
	}
}

func TestDefaultSupplierContact_EachSentValueWinsPerField(t *testing.T) {
	for i := 0; i < 8; i++ {
		for _, sent := range []string{"", "caller"} {
			in, c := CreateInput{}, fullContact()
			fs := contactFields(&in, &c)
			*fs[i].dst = strPtr(sent)
			DefaultSupplierContact(&in, c)
			if got := *fs[i].dst; got == nil || *got != sent {
				t.Errorf("%s sent %q: got %v", fs[i].name, sent, got)
			}
		}
	}
}

func TestDefaultSupplierContact_BlankOrNilCompanyValueLeavesEachFieldNil(t *testing.T) {
	for i := 0; i < 8; i++ {
		for _, blank := range []*string{nil, strPtr(""), strPtr(" \t\n ")} {
			in, c := CreateInput{}, SupplierContact{}
			fs := contactFields(&in, &c)
			*fs[i].src = blank
			DefaultSupplierContact(&in, c)
			if *fs[i].dst != nil {
				t.Errorf("%s with company value %v: got %q, want nil", fs[i].name, blank, **fs[i].dst)
			}
		}
	}
}
