package approval

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// inForceVersion is an active version of a live policy with its scopes. A nil or empty
// scope matches any document.
type inForceVersion struct {
	VersionID, PolicyID  string
	Position             int
	DocumentTypes        []string
	EntityIDs            []string
	RoleIDs              []string
	BandLower, BandUpper *decimal.Decimal // nil: unbounded
	BandCurrency         *string          // nil: no band
}

type matchDoc struct {
	DocumentType string
	EntityID     string
	Total        *decimal.Decimal // nil: NULL or unparseable
	Currency     *string
	CreatorRoles map[string]bool // live role ids of the creator
}

const (
	scopeDocumentType = "document_type"
	scopeClient       = "client"
	scopeAmountBand   = "amount_band"
	scopeCreatorRole  = "creator_role"
)

// firstMatch returns the first version whose every set scope matches doc, and the names of
// its set scopes in a fixed order; matched is non-nil and empty for a catch-all.
func firstMatch(doc matchDoc, versions []inForceVersion) (v inForceVersion, matched []string, ok bool) {
	for _, c := range versions {
		if m, hit := matchScopes(doc, c); hit {
			return c, m, true
		}
	}
	return inForceVersion{}, nil, false
}

func matchScopes(doc matchDoc, v inForceVersion) ([]string, bool) {
	matched := []string{}
	if len(v.DocumentTypes) > 0 {
		if !slices.Contains(v.DocumentTypes, doc.DocumentType) {
			return nil, false
		}
		matched = append(matched, scopeDocumentType)
	}
	if len(v.EntityIDs) > 0 {
		if !slices.Contains(v.EntityIDs, doc.EntityID) {
			return nil, false
		}
		matched = append(matched, scopeClient)
	}
	if v.BandCurrency != nil {
		if doc.Currency == nil || *doc.Currency != *v.BandCurrency || doc.Total == nil {
			return nil, false
		}
		if v.BandLower != nil && doc.Total.LessThan(*v.BandLower) {
			return nil, false
		}
		if v.BandUpper != nil && doc.Total.GreaterThan(*v.BandUpper) {
			return nil, false
		}
		matched = append(matched, scopeAmountBand)
	}
	if len(v.RoleIDs) > 0 {
		if !slices.ContainsFunc(v.RoleIDs, func(id string) bool { return doc.CreatorRoles[id] }) {
			return nil, false
		}
		matched = append(matched, scopeCreatorRole)
	}
	return matched, true
}

// readInForceVersionsTx returns the active versions of live policies in position order.
func readInForceVersionsTx(ctx context.Context, tx pgx.Tx) ([]inForceVersion, error) {
	rows, err := tx.Query(ctx, `SELECT v.id::text, v.policy_id::text, p.position,
		       v.scope_document_types, v.scope_entity_ids::text[], v.scope_workflow_role_ids::text[],
		       v.scope_amount_lower::text, v.scope_amount_upper::text, v.scope_amount_currency
		  FROM approval_policy_versions v
		  JOIN approval_policies p ON p.id = v.policy_id
		 WHERE v.is_active AND p.deleted_at IS NULL
		 ORDER BY p.position, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []inForceVersion
	for rows.Next() {
		var v inForceVersion
		var lower, upper *string
		if err := rows.Scan(&v.VersionID, &v.PolicyID, &v.Position, &v.DocumentTypes, &v.EntityIDs,
			&v.RoleIDs, &lower, &upper, &v.BandCurrency); err != nil {
			return nil, err
		}
		v.BandLower, v.BandUpper = parseBound(lower), parseBound(upper)
		out = append(out, v)
	}
	return out, rows.Err()
}

// parseBound reads numeric as text: numeric accepts NaN, which a decimal scan rejects.
func parseBound(s *string) *decimal.Decimal {
	if s == nil {
		return nil
	}
	d, err := decimal.NewFromString(*s)
	if err != nil {
		return nil
	}
	return &d
}

// readCreatorRoleIDsTx returns the live workflow roles of the invoice's creator. The text
// compare matches no row for a non-human actor instead of failing a uuid cast.
func readCreatorRoleIDsTx(ctx context.Context, tx pgx.Tx, invoiceID string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `SELECT wrm.workflow_role_id::text
		  FROM invoice_status_history h
		  JOIN workflow_role_members wrm ON wrm.user_id::text = h.actor
		  JOIN workflow_roles wr ON wr.id = wrm.workflow_role_id AND wr.deleted_at IS NULL
		 WHERE h.invoice_id = $1 AND h.from_status IS NULL`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
