-- NRS invoice fields: nullable, no default, no CHECK. The table-level grants in
-- 20260714103137_invoices.sql and 20260714105151_line_items.sql cover the new columns.

-- +goose Up
ALTER TABLE invoices
    ADD COLUMN invoice_kind text,
    ADD COLUMN tax_currency_code text,
    ADD COLUMN payment_status text,
    ADD COLUMN due_date date,
    ADD COLUMN tax_point_date date,
    ADD COLUMN issue_time time,
    ADD COLUMN supplier_email text,
    ADD COLUMN supplier_telephone text,
    ADD COLUMN supplier_street text,
    ADD COLUMN supplier_city text,
    ADD COLUMN supplier_postal_zone text,
    ADD COLUMN supplier_country text,
    ADD COLUMN supplier_state text,
    ADD COLUMN supplier_lga text,
    ADD COLUMN buyer_email text,
    ADD COLUMN buyer_telephone text,
    ADD COLUMN buyer_street text,
    ADD COLUMN buyer_city text,
    ADD COLUMN buyer_postal_zone text,
    ADD COLUMN buyer_country text,
    ADD COLUMN buyer_state text,
    ADD COLUMN buyer_lga text;

ALTER TABLE line_items
    ADD COLUMN tax_category text,
    ADD COLUMN hsn_code text,
    ADD COLUMN isic_code text,
    ADD COLUMN product_category text,
    ADD COLUMN service_category text,
    ADD COLUMN sellers_item_identification text,
    ADD COLUMN price_unit text,
    ADD COLUMN tax_percent numeric(14,2),
    ADD COLUMN base_quantity numeric(14,3);

-- +goose Down
ALTER TABLE line_items
    DROP COLUMN tax_category,
    DROP COLUMN hsn_code,
    DROP COLUMN isic_code,
    DROP COLUMN product_category,
    DROP COLUMN service_category,
    DROP COLUMN sellers_item_identification,
    DROP COLUMN price_unit,
    DROP COLUMN tax_percent,
    DROP COLUMN base_quantity;

ALTER TABLE invoices
    DROP COLUMN invoice_kind,
    DROP COLUMN tax_currency_code,
    DROP COLUMN payment_status,
    DROP COLUMN due_date,
    DROP COLUMN tax_point_date,
    DROP COLUMN issue_time,
    DROP COLUMN supplier_email,
    DROP COLUMN supplier_telephone,
    DROP COLUMN supplier_street,
    DROP COLUMN supplier_city,
    DROP COLUMN supplier_postal_zone,
    DROP COLUMN supplier_country,
    DROP COLUMN supplier_state,
    DROP COLUMN supplier_lga,
    DROP COLUMN buyer_email,
    DROP COLUMN buyer_telephone,
    DROP COLUMN buyer_street,
    DROP COLUMN buyer_city,
    DROP COLUMN buyer_postal_zone,
    DROP COLUMN buyer_country,
    DROP COLUMN buyer_state,
    DROP COLUMN buyer_lga;
