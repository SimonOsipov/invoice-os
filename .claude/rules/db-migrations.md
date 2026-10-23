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
- Write `db/bootstrap.sql` so that Cloud SQL's `postgres` can run it: it is not a superuser. Set no SUPERUSER attribute; the guard refuses a superuser role.
- Run `db/restore-check.sql` on Cloud SQL as `restore_auditor`: apply `db/restore-auditor.sql` once, then `SET ROLE restore_auditor`.
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
- Write expand-only migrations. The old revisions run against the new schema until traffic moves.
- Drop or rename a column or table in a later deploy, after no running revision reads it.
- Run migrations only in `cmd/migrate`, the job that runs before each deploy. No service migrates at boot.
- goose holds its session advisory lock while it migrates. `TestMigrateUpFromEmbedded` fails otherwise.
- Never edit an applied migration.
- The migrate job recreates a preview's `public` schema when its applied versions and the migration files disagree (`db.RecreateStuckSchema`). Production never does.
- Run `migrate bootstrap` once on production after you add a role to `db/bootstrap.sql`. The deploy-time run of the job never bootstraps production.
- Give `DATABASE_SUPERUSER_URL` only to the `migrate bootstrap` execution on production.
- Gate deploy-time provisioning with an allowlist of environment names, never a blocklist.
- Pass the raw `ENVIRONMENT` value to `BootstrapEnabled`.
- Gate `db.Reset` on `platform.DeployEnv()`, never on `ENVIRONMENT`.
- The migrate job runs the account-state grant on every run, as `supabase_auth_admin`. It logs `not_ready` until GoTrue has created `auth.users`. The next run restores the policy `invitee_account_state_read` after a GoTrue upgrade drops it.
- Connect as `supabase_auth_admin` only from GoTrue and from `db.GrantAccountStateRead`.
- Connect as `invoice_gateway` only from the gateway's hand-off store and registrant outbox. Grant it `SELECT`, `INSERT` and `DELETE` on `gateway_handoff_codes` and `INSERT` on `registrant_handoffs`, nothing else.
- Create `invoice_gateway` with `migrate bootstrap` before the migrations that grant to it. Those migrations grant only when the role already exists.
- Keep `db.DemoTenants` a Go literal. Never derive it from `db/seed.dev.sql`.
- Add every new `tenant_id` table to `purgeTables` or `purgeExcludedTables`. `TestPurgeTableListCoversEveryTenantOwnedTable` fails otherwise.
- Order `purgeTables` leaf-first so foreign keys stay enforced.
- Write the tenant predicate in the same string literal as each purge `DELETE`.
- Seed demo documents, approval policies and customers in the migrate job (`internal/demodocs`, `internal/demopolicy`, `customer.SeedDemo`), not in `db/seed.dev.sql`.
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

## Connection budget

Cloud SQL 1 vCPU / 3.75 GB allows 100 connections (GKE-01 P5); 3 are reserved for superusers.

- `db.NewPool` caps a pool at `DefaultMaxConns` (4) when the DSN sets no `pool_max_conns`.
- Set `pool_max_conns` in every hosted DSN, as the table says.
- Cap each Cloud Run service's max instances at the table's value.
- Recompute the total before you add a service, a pool or an instance. Keep it at 80 or below.

| Holder | DSN | `pool_max_conns` | Max instances | Connections |
|---|---|---|---|---|
| gateway | `DATABASE_GATEWAY_URL` | 4 | 2 | 8 |
| tenancy | `DATABASE_URL` | 4 | 2 | 8 |
| portfolio | `DATABASE_URL` | 4 | 1 | 4 |
| invoice | `DATABASE_URL` | 4 | 2 | 8 |
| validation | `DATABASE_URL` | 4 | 1 | 4 |
| dashboard | `DATABASE_URL` | 4 | 1 | 4 |
| submission | `DATABASE_URL` | 4 | 2 | 8 |
| notifications | `DATABASE_URL` | 4 | 1 | 4 |
| worker | `DATABASE_URL` (River runs 14 jobs at once) | 16 | 1 | 16 |
| worker | `DATABASE_READER_URL` | 2 | 1 | 2 |
| worker | leader connection | – | 1 | 1 |
| GoTrue | `GOTRUE_DB_MAX_POOL_SIZE` | 10 | 1 | 10 |
| migrate job | goose, the lock and one provisioning connection | – | 1 | 3 |
| **Total** | | | | **80** |

ceiling: a rollout runs old and new revisions together; one new instance per service at a full pool adds up to 36. Revisit when `pg_stat_activity` passes 80 or a pool waits: a larger tier or a pooler.
