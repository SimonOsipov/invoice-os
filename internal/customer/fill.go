package customer

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// fillSQL upserts the invoice's buyer into the directory. A field a member corrected
// (edited_fields) is never written; a NULL or blank invoice value never clears one.
const fillSQL = `
INSERT INTO customers AS c (tenant_id, tin, name, buyer_type, email, telephone, street, city, postal_zone, country, state, lga)
SELECT i.tenant_id, btrim(i.buyer_tin),
	nullif(btrim(i.buyer_name), ''),
	CASE WHEN btrim(i.invoice_kind) IN ('B2B','B2G','B2C') THEN btrim(i.invoice_kind) END,
	nullif(btrim(i.buyer_email), ''),
	nullif(btrim(i.buyer_telephone), ''),
	nullif(btrim(i.buyer_street), ''),
	nullif(btrim(i.buyer_city), ''),
	nullif(btrim(i.buyer_postal_zone), ''),
	nullif(btrim(i.buyer_country), ''),
	nullif(btrim(i.buyer_state), ''),
	nullif(btrim(i.buyer_lga), '')
FROM invoices i
WHERE i.id = $1 AND btrim(i.buyer_tin) ~ '^([0-9]{8}-[0-9]{4}|RN-[0-9]+)$'
ON CONFLICT (tenant_id, tin) DO UPDATE SET
		name = CASE WHEN 'name' = ANY(c.edited_fields) THEN c.name ELSE COALESCE(EXCLUDED.name, c.name) END,
		buyer_type = CASE WHEN 'buyer_type' = ANY(c.edited_fields) THEN c.buyer_type ELSE COALESCE(EXCLUDED.buyer_type, c.buyer_type) END,
		email = CASE WHEN 'email' = ANY(c.edited_fields) THEN c.email ELSE COALESCE(EXCLUDED.email, c.email) END,
		telephone = CASE WHEN 'telephone' = ANY(c.edited_fields) THEN c.telephone ELSE COALESCE(EXCLUDED.telephone, c.telephone) END,
		street = CASE WHEN 'street' = ANY(c.edited_fields) THEN c.street ELSE COALESCE(EXCLUDED.street, c.street) END,
		city = CASE WHEN 'city' = ANY(c.edited_fields) THEN c.city ELSE COALESCE(EXCLUDED.city, c.city) END,
		postal_zone = CASE WHEN 'postal_zone' = ANY(c.edited_fields) THEN c.postal_zone ELSE COALESCE(EXCLUDED.postal_zone, c.postal_zone) END,
		country = CASE WHEN 'country' = ANY(c.edited_fields) THEN c.country ELSE COALESCE(EXCLUDED.country, c.country) END,
		state = CASE WHEN 'state' = ANY(c.edited_fields) THEN c.state ELSE COALESCE(EXCLUDED.state, c.state) END,
		lga = CASE WHEN 'lga' = ANY(c.edited_fields) THEN c.lga ELSE COALESCE(EXCLUDED.lga, c.lga) END`

// FillFromInvoiceTx files the invoice's buyer in the directory inside the caller's
// transaction. It writes no audit row; an unfileable buyer TIN is a no-op.
func FillFromInvoiceTx(ctx context.Context, tx pgx.Tx, invoiceID string) error {
	_, err := tx.Exec(ctx, fillSQL, invoiceID)
	return err
}
