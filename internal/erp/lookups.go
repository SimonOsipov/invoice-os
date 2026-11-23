package erp

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

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

// ApplyLookups writes each found value over the mapped one. ceiling: exemption_code is not applied;
// no invoice field holds it yet.
func ApplyLookups(m *Mapped, ls []Lookup) {
	by := map[[2]string]Lookup{}
	for _, l := range ls {
		by[[2]string{l.Kind, l.ERPKey}] = l
	}
	set := func(dst **string, v *string) {
		if v != nil {
			s := *v
			*dst = &s
		}
	}
	if k := strings.TrimSpace(m.CustomerID); k != "" {
		if l, ok := by[[2]string{"customer", k}]; ok {
			set(&m.In.BuyerTIN, l.TIN)
			set(&m.In.BuyerState, l.State)
			set(&m.In.BuyerLGA, l.LGA)
		}
	}
	for i := range m.In.LineItems {
		line := &m.In.LineItems[i]
		if k := strings.TrimSpace(at(m.TaxCodes, i)); k != "" {
			if l, ok := by[[2]string{"tax_code", k}]; ok {
				set(&line.TaxCategory, l.TaxCategory)
			}
		}
		if k := strings.TrimSpace(at(m.Items, i)); k != "" {
			if l, ok := by[[2]string{"item", k}]; ok {
				set(&line.HSNCode, l.HSNCode)
				set(&line.ISICCode, l.ServiceCode)
			}
		}
	}
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}

// readLookups reads the client's lookups of one system and ERP company for the keys m carries.
func readLookups(ctx context.Context, tx pgx.Tx, entityID, system, externalKey string, m Mapped) ([]Lookup, error) {
	keys := func(in []string) []string {
		out := []string{}
		for _, s := range in {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	rows, err := tx.Query(ctx,
		`SELECT kind, erp_key, tax_category, exemption_code, hsn_code, service_code, tin, state, lga
		 FROM erp_lookups WHERE entity_id = $1 AND system = $2 AND external_key = $3
		   AND ((kind = 'customer' AND erp_key = $4) OR (kind = 'tax_code' AND erp_key = ANY($5)) OR (kind = 'item' AND erp_key = ANY($6)))`,
		entityID, system, externalKey, strings.TrimSpace(m.CustomerID), keys(m.TaxCodes), keys(m.Items))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Lookup
	for rows.Next() {
		var l Lookup
		if err := rows.Scan(&l.Kind, &l.ERPKey, &l.TaxCategory, &l.ExemptionCode, &l.HSNCode, &l.ServiceCode, &l.TIN, &l.State, &l.LGA); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
