package approval

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// RED STUB for APSC-03-01 Mode A: lets match_test.go compile. The executor replaces
// this file with the Design's matcher; every body below is deliberately inert.

type inForceVersion struct {
	VersionID, PolicyID  string
	Position             int
	DocumentTypes        []string
	EntityIDs            []string
	RoleIDs              []string
	BandLower, BandUpper *decimal.Decimal
	BandCurrency         *string
}

type matchDoc struct {
	DocumentType string
	EntityID     string
	Total        *decimal.Decimal
	Currency     *string
	CreatorRoles map[string]bool
}

const (
	scopeDocumentType = "document_type"
	scopeClient       = "client"
	scopeAmountBand   = "amount_band"
	scopeCreatorRole  = "creator_role"
)

func firstMatch(doc matchDoc, versions []inForceVersion) (v inForceVersion, matched []string, ok bool) {
	return inForceVersion{}, nil, false
}

func readInForceVersionsTx(ctx context.Context, tx pgx.Tx) ([]inForceVersion, error) {
	return nil, nil
}

func readCreatorRoleIDsTx(ctx context.Context, tx pgx.Tx, invoiceID string) (map[string]bool, error) {
	return nil, nil
}
