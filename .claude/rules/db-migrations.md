---
paths:
  - "migrations/**"
  - "db/**"
  - "internal/platform/db/**"
  - "internal/demo*/**"
---
# Database and migrations

- Connect services as `invoice_app`. Run goose as `invoice_migrator`.
- Never point `DATABASE_URL` or `DATABASE_MIGRATION_URL` at the superuser DSN. A superuser bypasses RLS.
- `invoice_app` owns no object and runs no DDL.
- Grant to `invoice_app` in the migration that creates the object.
- Grant the minimum verbs per object.
- Never use `ALTER DEFAULT PRIVILEGES`.
- Give the append-only tables `audit_log` and `idempotency_keys` SELECT and INSERT only.
- Give `invoice_app` INSERT only on `staff_audit_log`. Staff read it only through `public.staff_read_staff_audit_log`.
- Give `invoice_app` INSERT, DELETE and `SELECT (taken_at)` on `health_samples`. Staff read it only through `public.staff_read_health_samples`.
- Give `invoice_app` SELECT and INSERT only on `ai_calls`. Staff read it only through `public.staff_read_ai_calls` and `public.staff_read_ai_call_totals`.
- Give `invoice_app` SELECT, INSERT and UPDATE on `rollup_snapshots`. The purge, the reset and the FK cascade remove its rows.
- Give `invoice_app` SELECT, INSERT, UPDATE and DELETE on `blog_categories`, `blog_posts` and `blog_post_covers`. They are global: no RLS.
- Give `invoice_app` SELECT, INSERT, UPDATE and DELETE on `blog_subscription_requests`, and SELECT, INSERT and UPDATE on `blog_subscribers`. Both are global: no RLS.
- Staff read authors only through `public.staff_read_blog_authors`.
- Treat `submission_job_state_counts` as callable by every `invoice_app` service. It returns per-state counts only.
- Copy the `tenants` template for a new tenant table. Use `ENABLE` and `FORCE ROW LEVEL SECURITY` and a `tenant_isolation` policy.
- Read the tenant as `nullif(current_setting('app.current_tenant', true), '')::uuid`. An unset tenant returns zero rows.
- Never write `SET LOCAL app.current_tenant` by hand. Use `WithinTenantTx` or `WithinRequestTenantTx`.
- Loop over tenants one at a time for cross-tenant work.
- Enumerate tenants only through `invoice_tenant_reader` or a `staff_read_` function.
- Install a trusted extension in a migration. Install an untrusted extension in `db/bootstrap.sql`.
- Grant `EXECUTE` on `public.invitation_by_token`, `public.pending_invites_for_email`, `public.accept_invitation_by_id` and `public.invitee_account_state` to `invoice_app` only.
- Grant `auth_hook_reader` SELECT on the `auth.users` columns `email`, `email_confirmed_at` and `is_sso_user` only.
- Never grant `auth_hook_reader` a table-level privilege on `auth.users`.
- Treat `invitee_account_state` as callable by every `invoice_app` service for any address.
- Treat `invoice_migrator` as able to read `email`, `email_confirmed_at` and `is_sso_user` of every `auth.users` row through `SET ROLE auth_hook_reader`.
- Treat `invoice_migrator` as able to read `submission_jobs.state` of every tenant through `SET ROLE auth_hook_reader`.
- Return no rows from `pending_invites_for_email` for a user id that holds a membership of any status.
- Refuse `accept_invitation_by_id` in this order: GUC mismatch `42501`, not valid `invitation_not_valid`, already a member `one_workspace_per_identity`.
- Name a new migration with a current timestamp.
- Check the order with `go run ./internal/tools/migrationorder -base origin/<the PR's base branch>`. CI passes the PR's base sha.
- Write a working Down in every migration. CI resets all migrations and applies them again.
- Never edit an applied migration.
- A preview recreates its `public` schema at boot when its applied versions and the migration files disagree (`db.RecreateStuckSchema`). Production never does.
- Create a role that you add to `db/bootstrap.sql` by hand on production. Production runs no boot-time bootstrap.
- Gate deploy-time provisioning with an allowlist of environment names, never a blocklist.
- Pass the raw `ENVIRONMENT` value to `BootstrapEnabled`.
- Gate `db.Reset` on `platform.DeployEnv()`, never on `ENVIRONMENT`.
- Gate the gateway's account-state boot grant on `platform.Posture(platform.DeployEnv())`. Start it only for the `local` and `preview` postures.
- Connect as `supabase_auth_admin` only from GoTrue and from `db.GrantAccountStateRead`.
- Connect as `invoice_gateway` only from the gateway's hand-off store and registrant outbox. Grant it `SELECT`, `INSERT` and `DELETE` on `gateway_handoff_codes` and `INSERT` on `registrant_handoffs`, nothing else.
- Create `invoice_gateway` on production by hand before setting `DATABASE_GATEWAY_URL`, then grant it the three verbs on `gateway_handoff_codes` and `INSERT` on `registrant_handoffs`. The migration grants only when the role already exists.
- Grant the account-state read on production by hand, as the superuser or `supabase_auth_admin`. The production gateway never runs it.

```sql
GRANT USAGE ON SCHEMA auth TO auth_hook_reader;
GRANT SELECT (email, email_confirmed_at, is_sso_user) ON auth.users TO auth_hook_reader;
CREATE POLICY invitee_account_state_read ON auth.users FOR SELECT TO auth_hook_reader USING (true);
```

- Run the three statements again when a GoTrue upgrade drops the policy `invitee_account_state_read`.
- Keep `db.DemoTenants` a Go literal. Never derive it from `db/seed.dev.sql`.
- Add every new `tenant_id` table to `purgeTables` or `purgeExcludedTables`. `TestPurgeTableListCoversEveryTenantOwnedTable` fails otherwise.
- Order `purgeTables` leaf-first so foreign keys stay enforced.
- Write the tenant predicate in the same string literal as each purge `DELETE`.
- Seed demo documents and approval policies in the invoice service (`internal/demodocs`, `internal/demopolicy`), not in `db/seed.dev.sql`.
- Treat demo `business_entities` and `invoices` ids as unstable. Each purge and seed cycle mints new ids.
- Key demo fixtures on a literal seeded field: invoice number, entity name or workflow role key.
- Keep one row at most in `public.house_company`. Insert the production row by hand; the seed never replaces a row.
- Grant `EXECUTE` on `public.is_house_member` only to a role that owns a staff SQL function. Call it in every staff SQL function.
- Read tenant rows for staff only through a `public.staff_read_<noun>(p_actor uuid, …)` function.
- Create each one as `staff_reader`: `SECURITY DEFINER`, `SET search_path = ''`, `REVOKE EXECUTE … FROM PUBLIC`, `GRANT EXECUTE … TO invoice_app`.
- Make `IF NOT public.is_house_member(p_actor)` raising `insufficient_privilege` its first statement.
- Give `staff_reader` `SELECT` on the columns a function reads. Never a table-level grant.
- Give `staff_reader` one `FOR SELECT TO staff_reader USING (true)` policy on each RLS table it reads. A table without RLS gets no policy.
- Add each table with a `staff_reader` policy to the seed map of `TestRLS_AppStaysIsolatedOnEveryStaffReaderTable`.
- Run the `staff_reader` statements of `db/bootstrap.sql` by hand on production before the first migration that names the role.
- Treat `invoice_migrator` as able to read every column granted to `staff_reader`, in every tenant, through `SET ROLE staff_reader`.
- Scope a client table by `app.entity_scope` as the `tenant_isolation` policy of `invoices` does. Unset or `''` means every client.
- Scope a child table by the visibility of its parent row, as the `tenant_isolation` policy of `line_items` does.
