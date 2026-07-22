-- +goose Up
-- M5-01-02: the places on an invoice to keep what the authority gives back, plus the
-- tenant-scoped uniqueness target the submission spine's composite FKs need.
--
-- Four additive columns, no rewrite of anything already here:
--
--   irn        — the Invoice Reference Number the authority mints on acceptance.
--   csid       — the Cryptographic Stamp ID (the tamper seal) returned alongside it.
--   qr_payload — the encoded blob the printable/PDF invoice renders as a QR code.
--   rejection_reasons — what the authority said when it refused the invoice.
--
-- The three identifiers are NULLABLE because they genuinely do not exist until an
-- authority has accepted the invoice — a draft, a validated-but-unsubmitted, or a
-- rejected invoice carries none of them. Each gets an `IS NULL OR char_length(x) > 0`
-- CHECK so that an adapter which writes '' where it meant "nothing" is rejected (23514)
-- instead of quietly creating a second encoding of "no IRN". Unnamed inline CHECKs,
-- matching the M4-era convention on this table (20260714103137_invoices.sql:47-53) —
-- they are dropped with their columns, so the Down needs no separate statement.
--
-- rejection_reasons is `jsonb NOT NULL DEFAULT '[]'`, verbatim the shape of the
-- neighbouring `violations` column (20260714103137_invoices.sql:61), and like it carries
-- NO shape CHECK. "No reasons" is the empty array, never NULL. Element shape is a
-- convention the writer honours, not a DB constraint:
--
--   [{"code": "APP-ERR-0417", "message": "Supplier TIN not registered", "path": "supplier_tin"}]
--
-- `code` / `message` / optional `path` — deliberately NOT `rule_key` / `severity`. This
-- parallels invoice.Violation (internal/invoice/validator.go:77-82) on `path` alone, the
-- one field the fix-and-resubmit loop mechanically consumes. `code` is the authority's own
-- error taxonomy, kept verbatim rather than mapped onto our rule-key vocabulary (that
-- mapping would be an unverified invention); `severity` is meaningless because an APP
-- rejection is blocking by definition.
--
-- invoices_tenant_id_id_uq exists so the M5-01-03 `submission_jobs -> invoices` composite
-- FK (and transitively M5-01-04's `app_exchange -> submission_jobs`) has something to
-- reference: id alone is this table's PK, and a composite FK requires a composite
-- unique/PK on the referenced side. Redundant for uniqueness purposes, required by
-- Postgres — the same trade 20260718104103_invoices_entity_composite_fk.sql:31-32 made on
-- business_entities. It MUST be ADD CONSTRAINT ... UNIQUE, not CREATE UNIQUE INDEX: a bare
-- unique index produces no pg_constraint row and cannot be named by an FK. This table
-- already shows the difference — its 3-column guard invoices_tenant_entity_number_uq is
-- index-only (20260714103137_invoices.sql:81-82), which is why invoices carries zero
-- contype='u' rows before this migration.
--
-- No new GRANT: invoices carries a table-level GRANT (20260714103137_invoices.sql:95)
-- which covers columns added later. (Contrast rules, which uses a column-level
-- UPDATE (enabled) grant — that pattern would have required a follow-up grant here.)
--
-- No policy either: the M4-01 `tenant_isolation` policy on invoices is row-scoped, so it
-- already governs every column added after it.
--
-- Deliberately NOT in scope, deferred to M5-05: the invoices.status CHECK (it already
-- permits all 7 states, 20260714103137_invoices.sql:47-49) and the transition edges that
-- would populate these columns.
--
-- No StatementBegin/End: no function bodies in this migration.
ALTER TABLE invoices
    ADD COLUMN irn               text  CHECK (irn        IS NULL OR char_length(irn)        > 0),
    ADD COLUMN csid              text  CHECK (csid       IS NULL OR char_length(csid)       > 0),
    ADD COLUMN qr_payload        text  CHECK (qr_payload IS NULL OR char_length(qr_payload) > 0),
    ADD COLUMN rejection_reasons jsonb NOT NULL DEFAULT '[]';

ALTER TABLE invoices
    ADD CONSTRAINT invoices_tenant_id_id_uq UNIQUE (tenant_id, id);

-- +goose Down
-- Mirror order: the constraint first, then the columns, so reset→up round-trips clean
-- (the CI reversibility gate, docs/migrations.md §6). Dropping a column takes its inline
-- CHECK with it.
ALTER TABLE invoices DROP CONSTRAINT invoices_tenant_id_id_uq;
ALTER TABLE invoices
    DROP COLUMN rejection_reasons,
    DROP COLUMN qr_payload,
    DROP COLUMN csid,
    DROP COLUMN irn;
