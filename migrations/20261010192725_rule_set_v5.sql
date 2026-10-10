-- +goose Up
-- Publish rule-set v5 (NRS content field set), dated 2027-01-01. v1-v4 are untouched.
-- Order is forced: v5 unsealed, its rules (rules_content_lock allows child INSERTs only under an
-- unsealed parent), then ONE UPDATE that seals and dates it. No rule_set_version_rechecks row:
-- the re-check writes it on 2027-01-01.
-- 67 rules: 15 v4 rules SELECT-copied, 5 fixed, 47 new. Each row's comment names its NRS v1.1
-- card, field and doc page (/docs/system-integrator/invoice-schema/<page>).
--
-- Known gaps, not enforced (NRS v1.1 cards):
--   * note (1.9) and payment_status (1.8): "Limit: 3 Characters" against longer documented values.
--   * accounting_cost (1.13): "Limit: 2 Characters" against the example 2000.
--   * HSNCode NNNN.NN, 7 chars (1.41.1) against the 8-digit vat-exemptions codes (3 with trailing spaces).
--   * REDUCED_VAT is listed at 7.5%; STAMP_DUTY 1% and WITHHOLDING_TAX are not rate-checked (D4).
--   * G2B invoice kind: changelog only, not in the field table (D8).
--   * Unenforced limits: street 255, city 100, postal zone 20, TIN 13, telephone "+" prefix (D15).
--   * NRS's TaxTotal example is inconsistent (TaxAmount 56.07 against a 60 subtotal, 1.32).
--   * The supplier sub-page says "buyer"; its example omits State and LGA, which it marks mandatory.

INSERT INTO rule_set_versions (version, sealed, notes)
VALUES (5, false, 'MBS global rule-set v5 (ENGI-09: NRS content field set)');

-- Carried unchanged; enabled forced true as in v4 (a kill-switch flip is not published content).
-- supplier-tin-required: NRS v1.1 card 1.23 AccountingSupplierParty, field 1.22.2 tin; /accounting-supplier-party
-- supplier-name-required: NRS v1.1 card 1.23 AccountingSupplierParty, field 1.22.1 PartyName; /accounting-supplier-party
-- invoice-number-required: NRS v1.1 irn (InvNo part of InvNo-ServiceID-YYYYMMDD); card number unconfirmed
-- issue-date-required: NRS v1.1 card 1.3 IssueDate
-- currency-required: NRS v1.1 card 1.11 DocumentCurrencyCode
-- subtotal-required: NRS v1.1 card 1.33 LegalMonetaryTotal, LineExtensionAmount and TaxExclusiveAmount (D17)
-- subtotal-non-negative: NRS v1.1 card 1.33 LegalMonetaryTotal, LineExtensionAmount and TaxExclusiveAmount (D17)
-- vat-required: NRS v1.1 card 1.32 TaxTotal, field 1.3.1 TaxAmount (/tax-total) (D17)
-- vat-non-negative: NRS v1.1 card 1.32 TaxTotal, field 1.3.1 TaxAmount (/tax-total) (D17)
-- total-required: NRS v1.1 card 1.33 LegalMonetaryTotal, TaxInclusiveAmount and PayableAmount (D17)
-- total-non-negative: NRS v1.1 card 1.33 LegalMonetaryTotal, TaxInclusiveAmount and PayableAmount (D17)
-- line-items-required: NRS v1.1 card 1.34 InvoiceLine, at least one line
-- line-items-sum-subtotal: NRS v1.1 card 1.34 InvoiceLine, field 1.41.10 LineExtensionAmount, summed to card 1.33 LineExtensionAmount; /invoice-line
-- line-cost-non-negative: NRS v1.1 card 1.34 InvoiceLine, field 1.41.12 Price PriceAmount; /invoice-line
-- no-duplicate-line-items: NRS v1.1 card 1.34 InvoiceLine, one entry per line; no NRS field, MBS rule
INSERT INTO rules
    (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
SELECT v5.id, r.key, r.type, r.target, r.params, r.severity, r."when", r.message, r.scope, true
FROM rules r
JOIN rule_set_versions v4 ON v4.id = r.rule_set_version_id AND v4.version = 4
CROSS JOIN rule_set_versions v5
WHERE v5.version = 5
  AND r.key IN ('supplier-tin-required', 'supplier-name-required', 'invoice-number-required', 'issue-date-required', 'currency-required', 'subtotal-required', 'subtotal-non-negative', 'vat-required', 'vat-non-negative', 'total-required', 'total-non-negative', 'line-items-required', 'line-items-sum-subtotal', 'line-cost-non-negative', 'no-duplicate-line-items');

-- Fixed: same keys as v4, new content (D3).
INSERT INTO rules
    (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
SELECT v5.id, r.key, r.type, r.target, r.params, 'error', r."when", r.message, 'document', true
FROM rule_set_versions v5
CROSS JOIN (VALUES
    -- NRS v1.1 card 1.35 AccountingCustomerParty, field 1.22.2 tin; /accounting-supplier-party
    ('buyer-tin-required', 'required', 'buyer.tin', '{}'::jsonb, $w$has(invoice.invoice_kind) && invoice.invoice_kind in ['B2B', 'B2G']$w$::text, 'Buyer TIN is required for a B2B or B2G invoice.'),
    -- NRS v1.1 card 1.23 AccountingSupplierParty example RN-847789, field 1.22.2 tin; changelog Oct 2025
    ('supplier-tin-format', 'format/regex', 'supplier.tin', '{"pattern":"^(?:[0-9]{8}-[0-9]{4}|RN-[0-9]+)$"}'::jsonb, NULL, 'Supplier TIN must be NNNNNNNN-NNNN or an RN- incorporation number.'),
    -- NRS v1.1 card 1.35 AccountingCustomerParty, field 1.22.2 tin; changelog Oct 2025
    ('buyer-tin-format', 'format/regex', 'buyer.tin', '{"pattern":"^(?:[0-9]{8}-[0-9]{4}|RN-[0-9]+)$"}'::jsonb, NULL, 'Buyer TIN, when present, must be NNNNNNNN-NNNN or an RN- incorporation number.'),
    -- NRS v1.1 card 1.11 DocumentCurrencyCode; currencies list
    ('currency-allowed', 'enum', 'currency', '{"list":"currencies"}'::jsonb, NULL, 'Currency must be a code on the NRS currency list.'),
    -- NRS v1.1 card 1.32 TaxTotal, field 1.3.2 TaxSubtotal (/tax-total); tax-categories list rates (D4)
    ('vat-standard-rate', 'tax_math', 'tax_subtotals', '{"items":"tax_subtotals","base":"taxable_amount","expected":"tax_amount","rate_by":"tax_category","rates":{"STANDARD_VAT":0.075,"ZERO_VAT":0,"EXEMPTED":0},"tolerance":0.005}'::jsonb, NULL, 'Tax must equal the category rate of the taxable amount (STANDARD_VAT 7.5%, ZERO_VAT 0%, EXEMPTED 0%).')
) AS r(key, type, target, params, "when", message)
WHERE v5.version = 5;

-- New: header.
INSERT INTO rules
    (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
SELECT v5.id, r.key, r.type, r.target, r.params, 'error', r."when", r.message, 'document', true
FROM rule_set_versions v5
CROSS JOIN (VALUES
    -- NRS v1.1 card 1.7 InvoiceKind
    ('invoice-kind-required', 'required', 'invoice_kind', '{}'::jsonb, NULL, 'Invoice kind (B2B, B2G or B2C) is required.'),
    -- NRS v1.1 card 1.7 InvoiceKind; changelog Dec 2025
    ('invoice-kind-allowed', 'enum', 'invoice_kind', '{"values":["B2B","B2G","B2C"]}'::jsonb, NULL, 'Invoice kind must be B2B, B2G or B2C.'),
    -- NRS v1.1 card 1.12 TaxCurrencyCode
    ('tax-currency-required', 'required', 'tax_currency_code', '{}'::jsonb, NULL, 'Tax currency is required.'),
    -- NRS v1.1 card 1.12 TaxCurrencyCode; currencies list
    ('tax-currency-allowed', 'enum', 'tax_currency_code', '{"list":"currencies"}'::jsonb, NULL, 'Tax currency must be a code on the NRS currency list.'),
    -- NRS v1.1 card 1.32 TaxTotal, field 1.3.1 TaxAmount (/tax-total)
    ('vat-equals-tax-subtotals', 'line_sum', 'vat', '{"items":"tax_subtotals","amount":"tax_amount","expected":"vat","tolerance":0.005}'::jsonb, NULL, 'The VAT total must equal the sum of the tax categories.'),
    -- NRS v1.1 card 1.32 TaxTotal; v4 vat-standard-rate kept where no line is categorised (D33)
    ('vat-standard-rate-uncategorised', 'tax_math', 'vat', '{"base":"subtotal","rate":0.075,"expected":"vat","tolerance":0.005}'::jsonb, $w$!has(invoice.tax_subtotals)$w$::text, 'With no tax category on any line, VAT must be 7.5% of the subtotal.')
) AS r(key, type, target, params, "when", message)
WHERE v5.version = 5;

-- New: supplier.
INSERT INTO rules
    (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
SELECT v5.id, r.key, r.type, r.target, r.params, 'error', r."when", r.message, 'document', true
FROM rule_set_versions v5
CROSS JOIN (VALUES
    -- NRS v1.1 card 1.23 AccountingSupplierParty, field 1.22.3 Email; /accounting-supplier-party
    ('supplier-email-required', 'required', 'supplier.email', '{}'::jsonb, NULL, 'Supplier email is required.'),
    -- NRS v1.1 card 1.23 AccountingSupplierParty, field 1.22.7 StreetName; /accounting-supplier-party
    ('supplier-street-required', 'required', 'supplier.street', '{}'::jsonb, NULL, 'Supplier street is required.'),
    -- NRS v1.1 card 1.23 AccountingSupplierParty, field 1.22.8 CityName; /accounting-supplier-party
    ('supplier-city-required', 'required', 'supplier.city', '{}'::jsonb, NULL, 'Supplier city is required.'),
    -- NRS v1.1 card 1.23 AccountingSupplierParty, field 1.22.9 PostalZone; /accounting-supplier-party
    ('supplier-postal-zone-required', 'required', 'supplier.postal_zone', '{}'::jsonb, NULL, 'Supplier postal zone is required.'),
    -- NRS v1.1 card 1.23 AccountingSupplierParty, field 1.22.10 LGA; /accounting-supplier-party
    ('supplier-lga-required', 'required', 'supplier.lga', '{}'::jsonb, NULL, 'Supplier LGA is required.'),
    -- NRS v1.1 card 1.23 AccountingSupplierParty, field 1.22.11 State; /accounting-supplier-party
    ('supplier-state-required', 'required', 'supplier.state', '{}'::jsonb, NULL, 'Supplier state is required.'),
    -- NRS v1.1 card 1.23 AccountingSupplierParty, field 1.22.12 Country; /accounting-supplier-party
    ('supplier-country-required', 'required', 'supplier.country', '{}'::jsonb, NULL, 'Supplier country is required.'),
    -- NRS v1.1 card 1.23 AccountingSupplierParty, field 1.22.10 LGA; lgas list
    ('supplier-lga-allowed', 'enum', 'supplier.lga', '{"list":"lgas"}'::jsonb, NULL, 'Supplier LGA must be a code on the NRS lgas list.'),
    -- NRS v1.1 card 1.23 AccountingSupplierParty, field 1.22.11 State; states list
    ('supplier-state-allowed', 'enum', 'supplier.state', '{"list":"states"}'::jsonb, NULL, 'Supplier state must be a code on the NRS states list.'),
    -- NRS v1.1 card 1.23 AccountingSupplierParty, field 1.22.12 Country; countries list
    ('supplier-country-allowed', 'enum', 'supplier.country', '{"list":"countries"}'::jsonb, NULL, 'Supplier country must be a code on the NRS countries list.'),
    -- NRS v1.1 card 1.23 AccountingSupplierParty, field 1.22.1 PartyName, max 255; /accounting-supplier-party
    ('supplier-name-length', 'format/regex', 'supplier.name', '{"pattern":"(?s)^.{0,255}$"}'::jsonb, NULL, 'Supplier name must be at most 255 characters.'),
    -- NRS v1.1 card 1.23 AccountingSupplierParty, field 1.22.3 Email, max 100; /accounting-supplier-party
    ('supplier-email-length', 'format/regex', 'supplier.email', '{"pattern":"(?s)^.{0,100}$"}'::jsonb, NULL, 'Supplier email must be at most 100 characters.')
) AS r(key, type, target, params, "when", message)
WHERE v5.version = 5;

-- New: buyer. Presence rules apply to B2B and B2G only (D7).
INSERT INTO rules
    (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
SELECT v5.id, r.key, r.type, r.target, r.params, 'error', r."when", r.message, 'document', true
FROM rule_set_versions v5
CROSS JOIN (VALUES
    -- NRS v1.1 card 1.35 AccountingCustomerParty, field 1.22.1 PartyName; /accounting-supplier-party
    ('buyer-name-required', 'required', 'buyer.name', '{}'::jsonb, $w$has(invoice.invoice_kind) && invoice.invoice_kind in ['B2B', 'B2G']$w$::text, 'Buyer name is required for a B2B or B2G invoice.'),
    -- NRS v1.1 card 1.35 AccountingCustomerParty, field 1.22.3 Email; /accounting-supplier-party
    ('buyer-email-required', 'required', 'buyer.email', '{}'::jsonb, $w$has(invoice.invoice_kind) && invoice.invoice_kind in ['B2B', 'B2G']$w$::text, 'Buyer email is required for a B2B or B2G invoice.'),
    -- NRS v1.1 card 1.35 AccountingCustomerParty, field 1.22.7 StreetName; /accounting-supplier-party
    ('buyer-street-required', 'required', 'buyer.street', '{}'::jsonb, $w$has(invoice.invoice_kind) && invoice.invoice_kind in ['B2B', 'B2G']$w$::text, 'Buyer street is required for a B2B or B2G invoice.'),
    -- NRS v1.1 card 1.35 AccountingCustomerParty, field 1.22.8 CityName; /accounting-supplier-party
    ('buyer-city-required', 'required', 'buyer.city', '{}'::jsonb, $w$has(invoice.invoice_kind) && invoice.invoice_kind in ['B2B', 'B2G']$w$::text, 'Buyer city is required for a B2B or B2G invoice.'),
    -- NRS v1.1 card 1.35 AccountingCustomerParty, field 1.22.9 PostalZone; /accounting-supplier-party
    ('buyer-postal-zone-required', 'required', 'buyer.postal_zone', '{}'::jsonb, $w$has(invoice.invoice_kind) && invoice.invoice_kind in ['B2B', 'B2G']$w$::text, 'Buyer postal zone is required for a B2B or B2G invoice.'),
    -- NRS v1.1 card 1.35 AccountingCustomerParty, field 1.22.10 LGA; /accounting-supplier-party
    ('buyer-lga-required', 'required', 'buyer.lga', '{}'::jsonb, $w$has(invoice.invoice_kind) && invoice.invoice_kind in ['B2B', 'B2G']$w$::text, 'Buyer LGA is required for a B2B or B2G invoice.'),
    -- NRS v1.1 card 1.35 AccountingCustomerParty, field 1.22.11 State; /accounting-supplier-party
    ('buyer-state-required', 'required', 'buyer.state', '{}'::jsonb, $w$has(invoice.invoice_kind) && invoice.invoice_kind in ['B2B', 'B2G']$w$::text, 'Buyer state is required for a B2B or B2G invoice.'),
    -- NRS v1.1 card 1.35 AccountingCustomerParty, field 1.22.12 Country; /accounting-supplier-party
    ('buyer-country-required', 'required', 'buyer.country', '{}'::jsonb, $w$has(invoice.invoice_kind) && invoice.invoice_kind in ['B2B', 'B2G']$w$::text, 'Buyer country is required for a B2B or B2G invoice.'),
    -- NRS v1.1 card 1.35 AccountingCustomerParty, field 1.22.10 LGA; lgas list
    ('buyer-lga-allowed', 'enum', 'buyer.lga', '{"list":"lgas"}'::jsonb, NULL, 'Buyer LGA must be a code on the NRS lgas list.'),
    -- NRS v1.1 card 1.35 AccountingCustomerParty, field 1.22.11 State; states list
    ('buyer-state-allowed', 'enum', 'buyer.state', '{"list":"states"}'::jsonb, NULL, 'Buyer state must be a code on the NRS states list.'),
    -- NRS v1.1 card 1.35 AccountingCustomerParty, field 1.22.12 Country; countries list
    ('buyer-country-allowed', 'enum', 'buyer.country', '{"list":"countries"}'::jsonb, NULL, 'Buyer country must be a code on the NRS countries list.'),
    -- NRS v1.1 card 1.35 AccountingCustomerParty, field 1.22.1 PartyName, max 255; /accounting-supplier-party
    ('buyer-name-length', 'format/regex', 'buyer.name', '{"pattern":"(?s)^.{0,255}$"}'::jsonb, NULL, 'Buyer name must be at most 255 characters.'),
    -- NRS v1.1 card 1.35 AccountingCustomerParty, field 1.22.3 Email, max 100; /accounting-supplier-party
    ('buyer-email-length', 'format/regex', 'buyer.email', '{"pattern":"(?s)^.{0,100}$"}'::jsonb, NULL, 'Buyer email must be at most 100 characters.')
) AS r(key, type, target, params, "when", message)
WHERE v5.version = 5;

-- New: lines.
INSERT INTO rules
    (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
SELECT v5.id, r.key, r.type, r.target, r.params, 'error', r."when", r.message, 'document', true
FROM rule_set_versions v5
CROSS JOIN (VALUES
    -- NRS v1.1 card 1.34 InvoiceLine, field 1.41.9 InvoicedQuantity; /invoice-line
    ('line-quantity-required', 'cel', 'line_items', jsonb_build_object('expr', $e$!has(invoice.line_items) || invoice.line_items.all(x, has(x.quantity) && type(x.quantity) == double)$e$::text), NULL, 'Each line needs a quantity.'),
    -- NRS v1.1 card 1.34 InvoiceLine, field 1.41.10 LineExtensionAmount; /invoice-line
    ('line-amount-required', 'cel', 'line_items', jsonb_build_object('expr', $e$!has(invoice.line_items) || invoice.line_items.all(x, has(x.line_total) && type(x.line_total) == double)$e$::text), NULL, 'Each line needs a line amount.'),
    -- NRS v1.1 card 1.34 InvoiceLine, field 1.41.11 Item Name, Description (the line description, D14); /invoice-line
    ('line-description-required', 'cel', 'line_items', jsonb_build_object('expr', $e$!has(invoice.line_items) || invoice.line_items.all(x, has(x.description) && x.description.matches(r'[^[:space:]\x{85}\p{Z}]'))$e$::text), NULL, 'Each line needs an item description.'),
    -- NRS v1.1 card 1.34 InvoiceLine, field 1.41.11 Item SellersItemIdentification; /invoice-line
    ('line-item-id-required', 'cel', 'line_items', jsonb_build_object('expr', $e$!has(invoice.line_items) || invoice.line_items.all(x, has(x.sellers_item_identification) && x.sellers_item_identification.matches(r'[^[:space:]\x{85}\p{Z}]'))$e$::text), NULL, 'Each line needs the seller''s item ID.'),
    -- NRS v1.1 card 1.34 InvoiceLine, field 1.41.12 Price PriceAmount; /invoice-line
    ('line-price-required', 'cel', 'line_items', jsonb_build_object('expr', $e$!has(invoice.line_items) || invoice.line_items.all(x, has(x.unit_price) && type(x.unit_price) == double)$e$::text), NULL, 'Each line needs a unit price.'),
    -- NRS v1.1 card 1.34 InvoiceLine, field 1.41.12 Price BaseQuantity; /invoice-line
    ('line-base-quantity-required', 'cel', 'line_items', jsonb_build_object('expr', $e$!has(invoice.line_items) || invoice.line_items.all(x, has(x.base_quantity) && type(x.base_quantity) == double)$e$::text), NULL, 'Each line needs a base quantity.'),
    -- NRS v1.1 card 1.34 InvoiceLine, field 1.41.12 Price PriceUnit; /invoice-line
    ('line-price-unit-required', 'cel', 'line_items', jsonb_build_object('expr', $e$!has(invoice.line_items) || invoice.line_items.all(x, has(x.price_unit) && x.price_unit.matches(r'[^[:space:]\x{85}\p{Z}]'))$e$::text), NULL, 'Each line needs a unit of measure.'),
    -- NRS v1.1 card 1.34 InvoiceLine, field 1.41.12 Price PriceUnit; invoice-quantity-codes list; /invoice-line
    ('line-price-unit-allowed', 'enum', 'price_unit', '{"list":"invoice-quantity-codes","items":"line_items"}'::jsonb, NULL, 'Unit of measure must be a code on the NRS unit list.'),
    -- NRS v1.1 card 1.34 InvoiceLine, fields 1.41.1-1.41.4 HSNCode+ProductCategory or ISICCode+ServiceCategory; /invoice-line
    ('line-classification-required', 'cel', 'line_items', jsonb_build_object('expr', $e$!has(invoice.line_items) || invoice.line_items.all(x, (has(x.hsn_code) && x.hsn_code.matches(r'[^[:space:]\x{85}\p{Z}]') && has(x.product_category) && x.product_category.matches(r'[^[:space:]\x{85}\p{Z}]')) || (has(x.isic_code) && x.isic_code.matches(r'[^[:space:]\x{85}\p{Z}]') && has(x.service_category) && x.service_category.matches(r'[^[:space:]\x{85}\p{Z}]')))$e$::text), NULL, 'Each line needs an HS code and product category, or an ISIC code and service category.'),
    -- NRS v1.1 card 1.34 InvoiceLine, field 1.41.1 HSNCode; hs-codes list; /invoice-line
    ('line-hs-code-allowed', 'enum', 'hsn_code', '{"list":"hs-codes","items":"line_items"}'::jsonb, NULL, 'HS code must be a code on the NRS HS list.'),
    -- NRS v1.1 card 1.34 InvoiceLine, field 1.41.3 ISICCode; services-codes list; /invoice-line
    ('line-service-code-allowed', 'enum', 'isic_code', '{"list":"services-codes","items":"line_items"}'::jsonb, NULL, 'ISIC code must be a code on the NRS service list.'),
    -- NRS v1.1 card 1.32 TaxTotal, field 1.3.2 TaxCategory ID, derived from the line (D12); /tax-total
    ('line-tax-category-required', 'cel', 'line_items', jsonb_build_object('expr', $e$!has(invoice.line_items) || invoice.line_items.all(x, has(x.tax_category) && x.tax_category.matches(r'[^[:space:]\x{85}\p{Z}]'))$e$::text), NULL, 'Each line needs a tax category.'),
    -- NRS v1.1 card 1.32 TaxTotal, field 1.3.2 TaxCategory ID; tax-categories list; /tax-total
    ('line-tax-category-allowed', 'enum', 'tax_category', '{"list":"tax-categories","items":"line_items"}'::jsonb, NULL, 'Tax category must be a code on the NRS tax-category list.'),
    -- NRS v1.1 card 1.32 TaxTotal, field 1.3.2 TaxCategory Percent, derived from the line (D12); /tax-total
    ('line-tax-percent-required', 'cel', 'line_items', jsonb_build_object('expr', $e$!has(invoice.line_items) || invoice.line_items.all(x, has(x.tax_percent) && type(x.tax_percent) == double)$e$::text), NULL, 'Each line needs a tax percent.'),
    -- NRS v1.1 card 1.32 TaxTotal, field 1.3.1 TaxAmount, derived from the line (D12); /tax-total
    ('line-tax-required', 'cel', 'line_items', jsonb_build_object('expr', $e$!has(invoice.line_items) || invoice.line_items.all(x, has(x.line_tax) && type(x.line_tax) == double)$e$::text), NULL, 'Each line needs a tax amount.'),
    -- NRS v1.1 card 1.32 TaxTotal, field 1.3.2 TaxCategory Percent; tax-categories list rates (D6); /tax-total
    ('tax-percent-matches-category', 'cel', 'line_items', jsonb_build_object('expr', $e$!has(invoice.line_items) || invoice.line_items.all(x, !has(x.tax_category) || !(x.tax_category in ['STANDARD_VAT', 'ZERO_VAT', 'EXEMPTED']) || !has(x.tax_percent) || (type(x.tax_percent) == double && x.tax_percent == {'STANDARD_VAT': 7.5, 'ZERO_VAT': 0.0, 'EXEMPTED': 0.0}[x.tax_category]))$e$::text), NULL, 'Tax percent must match the category: STANDARD_VAT 7.5, ZERO_VAT 0, EXEMPTED 0.')
) AS r(key, type, target, params, "when", message)
WHERE v5.version = 5;

UPDATE rule_set_versions SET sealed = true, effective_from = '2027-01-01' WHERE version = 5;

-- +goose Down
-- Both owner-proof guards are DISABLEd, not dropped (as v4's Down). v5's rules cascade.
-- Once an invoice stamps v5 the DELETE raises 23503; CI's reversibility gate runs on an empty database.

ALTER TABLE rules              DISABLE TRIGGER rules_content_lock;
ALTER TABLE rule_set_versions  DISABLE TRIGGER rule_set_versions_seal_guard;

DELETE FROM rule_set_versions WHERE version = 5;

ALTER TABLE rule_set_versions  ENABLE TRIGGER rule_set_versions_seal_guard;
ALTER TABLE rules              ENABLE TRIGGER rules_content_lock;
