-- INVED-01-01: widen invoice_app's grant on line_items to include DELETE.
--
-- 20260714105151_line_items.sql:76-80 granted SELECT/INSERT/UPDATE only, reasoning
-- "the fix loop (M4-05) edits rows in place — no DELETE (no hard-delete consumer
-- exists yet)". INVED-01 makes removing a line one of the three line edits the
-- invoice edit surface must support, so the consumer now exists and the grant is
-- widened here rather than by editing that shipped migration (Decision
-- [line-delete-grant]).
--
-- Grant only — NO policy change. line_items' tenant_isolation policy is FOR ALL with
-- no TO clause, and in Postgres a DELETE is filtered by a policy's USING clause
-- (DELETE has no WITH CHECK), so the existing predicate already constrains DELETE to
-- the current tenant: cross-tenant DELETE affects 0 rows and an unset
-- app.current_tenant GUC deletes nothing. Proven by the RLS cases added alongside
-- this migration.
--
-- Bare GRANT DELETE, not a restatement of the full matrix: GRANT is additive, and the
-- Down must revoke exactly what this migration added (docs/migrations.md §3).
-- Nothing is granted to invoice_tenant_reader — line_items has never been exposed to
-- the cross-tenant enumeration identity and this migration does not change that.
--
-- No StatementBegin/End: no function bodies here.

-- +goose Up
GRANT DELETE ON line_items TO invoice_app;

-- +goose Down
REVOKE DELETE ON line_items FROM invoice_app;
