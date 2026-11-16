package customer

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// fillSrcSQL selects the invoice's fileable buyer. A field a member corrected
// (edited_fields) is never written; a NULL or blank invoice value never clears one.
const fillSrcSQL = `
WITH src AS (
	SELECT i.tenant_id, btrim(i.buyer_tin) AS tin, i.entity_id,
		nullif(btrim(i.buyer_name), '') AS name,
		CASE WHEN btrim(i.invoice_kind) IN ('B2B','B2G','B2C') THEN btrim(i.invoice_kind) END AS buyer_type,
		nullif(btrim(i.buyer_email), '') AS email,
		nullif(btrim(i.buyer_telephone), '') AS telephone,
		nullif(btrim(i.buyer_street), '') AS street,
		nullif(btrim(i.buyer_city), '') AS city,
		nullif(btrim(i.buyer_postal_zone), '') AS postal_zone,
		nullif(btrim(i.buyer_country), '') AS country,
		nullif(btrim(i.buyer_state), '') AS state,
		nullif(btrim(i.buyer_lga), '') AS lga
	FROM invoices i
	WHERE i.id = $1 AND btrim(i.buyer_tin) ~ '^([0-9]{8}-[0-9]{4}|RN-[0-9]+)$'
)`

// fillInsertSQL files a new TIN linked to the invoice's client, created_by NULL.
const fillInsertSQL = fillSrcSQL + `
INSERT INTO customers (tenant_id, tin, name, buyer_type, email, telephone, street, city, postal_zone, country, state, lga, created_by, client_ids)
SELECT tenant_id, tin, name, buyer_type, email, telephone, street, city, postal_zone, country, state, lga, NULL, ARRAY[entity_id] FROM src
ON CONFLICT (tenant_id, tin) DO NOTHING`

// fillUpdateSQL refreshes and links a record the caller reaches. It is a plain UPDATE:
// RLS skips a hidden row, where ON CONFLICT DO UPDATE would raise 42501.
const fillUpdateSQL = fillSrcSQL + `
UPDATE customers c SET
	name = CASE WHEN 'name' = ANY(c.edited_fields) THEN c.name ELSE COALESCE(src.name, c.name) END,
	buyer_type = CASE WHEN 'buyer_type' = ANY(c.edited_fields) THEN c.buyer_type ELSE COALESCE(src.buyer_type, c.buyer_type) END,
	email = CASE WHEN 'email' = ANY(c.edited_fields) THEN c.email ELSE COALESCE(src.email, c.email) END,
	telephone = CASE WHEN 'telephone' = ANY(c.edited_fields) THEN c.telephone ELSE COALESCE(src.telephone, c.telephone) END,
	street = CASE WHEN 'street' = ANY(c.edited_fields) THEN c.street ELSE COALESCE(src.street, c.street) END,
	city = CASE WHEN 'city' = ANY(c.edited_fields) THEN c.city ELSE COALESCE(src.city, c.city) END,
	postal_zone = CASE WHEN 'postal_zone' = ANY(c.edited_fields) THEN c.postal_zone ELSE COALESCE(src.postal_zone, c.postal_zone) END,
	country = CASE WHEN 'country' = ANY(c.edited_fields) THEN c.country ELSE COALESCE(src.country, c.country) END,
	state = CASE WHEN 'state' = ANY(c.edited_fields) THEN c.state ELSE COALESCE(src.state, c.state) END,
	lga = CASE WHEN 'lga' = ANY(c.edited_fields) THEN c.lga ELSE COALESCE(src.lga, c.lga) END,
	client_ids = CASE WHEN src.entity_id = ANY(c.client_ids) THEN c.client_ids ELSE c.client_ids || src.entity_id END
FROM src
WHERE c.tenant_id = src.tenant_id AND c.tin = src.tin`

// FillFromInvoiceTx files the invoice's buyer in the directory inside the caller's
// transaction. It writes no audit row; an unfileable buyer TIN is a no-op.
// It needs READ COMMITTED: the update's fresh snapshot sees a row a racing fill committed.
func FillFromInvoiceTx(ctx context.Context, tx pgx.Tx, invoiceID string) error {
	tag, err := tx.Exec(ctx, fillInsertSQL, invoiceID)
	if err != nil || tag.RowsAffected() == 1 {
		return err
	}
	// Conflict or no fileable TIN: the insert waited out any racing writer; link on a new snapshot.
	_, err = tx.Exec(ctx, fillUpdateSQL, invoiceID)
	return err
}
