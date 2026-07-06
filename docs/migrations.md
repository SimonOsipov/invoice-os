# Database Migrations & Role Model (M2-01)

**Audience:** anyone writing a migration, wiring the on-deploy migration step (M2-12),
or provisioning a Postgres (M2-01 subtask 4). This doc is to migrations what
[add-a-service.md](./add-a-service.md) is to compute services: it *specifies* the
mechanism and conventions now so the first live consumer (**M2-12**, the gateway) can
wire them **without making a single new decision**. If M2-12 has to deviate, fix this
doc in the same PR.

The tool is **goose** (`github.com/pressly/goose/v3`), pinned via the `go.mod` `tool`
directive so `go tool goose` is byte-identical locally and in CI. It speaks to Postgres
through the **pgx v5 stdlib** driver (`GOOSE_DRIVER=postgres`). Migration files are
plain SQL, timestamped `YYYYMMDDHHMMSS_slug.sql`, single-file `-- +goose Up` /
`-- +goose Down`.

---

## 1. The three connection identities (never collapse them)

RLS is only enforceable if the roles it applies to are **non-superuser**, lack
**BYPASSRLS**, and do **not own** the tables. So there are three distinct identities,
each with its own connection string:

| Env var | Role | Superuser? | Used by | When |
|---|---|---|---|---|
| `DATABASE_URL` | `invoice_app` | no (NOBYPASSRLS) | every service binary | runtime queries |
| `DATABASE_MIGRATION_URL` | `invoice_migrator` | no (NOBYPASSRLS) | goose | the migration step only |
| `DATABASE_SUPERUSER_URL` | Postgres superuser | yes | `db/bootstrap.sql` | **once**, at provisioning |

**The load-bearing rule:** never point the app or the migration step at Railway's
`${{Postgres.DATABASE_URL}}` (the superuser). A superuser has **BYPASSRLS** — every
tenant-isolation policy silently becomes a no-op and the failure is invisible until a
cross-tenant leak. `invoice_migrator` also owns every table, and a table's **owner**
bypasses RLS *unless the table is `FORCE`d* — which is exactly why the app connects as a
*separate* role (`invoice_app`), never as the migrator. (M2-07 proves the owner-bypass
case adversarially; M2-06 adds `FORCE ROW LEVEL SECURITY`.)

### Role model (created by `db/bootstrap.sql`)

- `invoice_migrator` — `LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE`, granted
  `CREATE, USAGE ON SCHEMA public`. Owns every object a migration creates.
- `invoice_app` — same attributes, **no DDL, no ownership**. Gets table privileges only
  via explicit per-migration `GRANT`s (see §3).
- Bootstrap also `REVOKE CREATE ON SCHEMA public FROM PUBLIC` (a no-op on PG15+, kept for
  PG13/14 + defense-in-depth).

`bootstrap.sql` is idempotent (DO-block role creation + `ALTER ROLE` re-assertion), run
as the superuser via psql. `make db-bootstrap` runs it with dev-default passwords; real
passwords live only in Railway.

---

## 2. On-deploy mechanism — gateway-as-migrator, CI-ordered

Migrations are applied **at deploy time, in-network, as `invoice_migrator`, from a single
designated service: the gateway.** There is **no public DB proxy** and no separate
migration job with its own inbound DB access.

How the ordering works (wired at **M2-12**, when the gateway exists):

1. On a deploy, CI deploys the **gateway first** and **health-gates** it — the gateway
   runs `goose up` (against `DATABASE_MIGRATION_URL`) as part of coming up healthy, so the
   schema is fully migrated *before* it reports healthy.
2. Only after the gateway is healthy does CI deploy the **seven context services**. They
   boot against an already-migrated schema and never run migrations themselves.

This gives a **global ordering barrier** (schema-before-fleet) using only in-network
connections — the gateway is the one service that already needs privileged DB reach, so
it doubles as the migrator. No context service is granted the migrator URL.

> **Specify-now / prove-at-first-consumer:** the exact wiring (gateway `railway.json`
> deploy hook or entrypoint step, the CI job ordering) is authored in **M2-12** against
> this spec — same pattern add-a-service.md uses for the shared Go Dockerfile. Everything
> a wirer needs is here; nothing is left to re-decide.

**Dev vs CI, two different gates:**

- The **shared dev Postgres** (M2-01 subtask 4) is migrated forward-only/additive — the
  solo, strictly-sequential model tolerates no down-migrations against shared state.
- The **independent hard gate** is CI applying every migration to a *fresh, ephemeral*
  Postgres with **zero Railway dependency** (see §6 / `.github/workflows/ci.yml`
  `migrations` job). That is what actually blocks a broken or irreversible migration from
  merging — not the dev DB.

---

## 3. Grants are explicit, per-migration (least privilege)

`db/bootstrap.sql` grants `invoice_app` **nothing**. Every privilege the app role needs on
a new object is granted **in the same migration that creates the object**:

```sql
-- +goose Up
CREATE TABLE tenants (...);              -- owned by invoice_migrator
GRANT SELECT, INSERT, UPDATE ON tenants TO invoice_app;   -- explicit, minimal
```

**Do not** use blanket `ALTER DEFAULT PRIVILEGES … GRANT ALL … TO invoice_app`. The point
is that different objects need *different* privileges, and a blanket grant erases that
distinction:

- **`audit_log` (M2-10) is append-only** — `invoice_app` gets `INSERT` only, never
  `UPDATE`/`DELETE`. A blanket grant would silently make the audit trail mutable.
- **River's internal tables (M2-08)** are managed by the queue library; the app touches
  them only through River's API, not via ambient table grants.

Least privilege is a per-object decision, so it lives in the per-object migration.

---

## 4. The `app.current_tenant` GUC contract (M2-06 consumes this)

Tenant scoping rides on a **custom GUC**, not a column default or a session role. The
contract migrations and policies must honor:

- **Read:** `current_setting('app.current_tenant', true)` — the `true` = *missing_ok*, so
  an **unset** GUC returns `NULL` instead of raising `unrecognized configuration parameter`.
- **Unset → NULL → zero rows.** An RLS policy compares the row's tenant to the GUC; when
  the GUC is `NULL` (no tenant set), the predicate is false for every row → the connection
  sees **nothing**. Fail-closed by construction: forgetting to set the tenant leaks no data.
- **Set per transaction:** the app issues `SET LOCAL app.current_tenant = '<uuid>'` at the
  start of each tenant-scoped transaction (the `SET LOCAL` helper is M2-06's deliverable).
  `SET LOCAL` scopes it to the transaction, so a pooled connection never bleeds one tenant's
  GUC into the next transaction.

No DDL is needed to "declare" the GUC — a custom (dotted) GUC name is settable at runtime
with no configuration. That is why the M2-01 skeleton migration is a no-op.

---

## 5. Extensions: trusted → migration, untrusted → bootstrap

Where an extension gets installed depends on whether Postgres marks it **trusted**:

- **Trusted** (e.g. `pgcrypto`, `citext`) — a non-superuser with `CREATE` on the schema may
  `CREATE EXTENSION`. So install these **in a migration** (`invoice_migrator` can do it).
- **Untrusted** (needs superuser) — install in **`db/bootstrap.sql`** (runs as superuser),
  never in a migration, because the migrator role cannot and the migration would fail in CI
  and on deploy.

Rule of thumb: if `CREATE EXTENSION <x>` works as `invoice_migrator` against a fresh CI
Postgres, it belongs in a migration; if it needs the superuser, it belongs in bootstrap.
(Note: `gen_random_uuid()` is **core** on PG13+ — no extension required at all.)

---

## 6. Local & CI usage

**Local** (`make help` lists these; set the §1 `DATABASE_*` URLs in `.env` — gitignored — or your environment first):

| Command | Does | As role |
|---|---|---|
| `make db-bootstrap` | create/rotate the two roles (needs `psql`) | superuser |
| `make migrate-up` | apply all pending migrations | migrator |
| `make migrate-down` | roll back the latest migration | migrator |
| `make migrate-reset` | roll back **all** migrations | migrator |
| `make migrate-status` | show applied/pending state | migrator |
| `make migrate-create name=<slug>` | scaffold a timestamped SQL migration | — |

**CI** (`.github/workflows/ci.yml`, `migrations` job — the independent hard gate): on a
fresh Postgres service container it runs `db/bootstrap.sql` as the superuser, `goose up` as
the migrator, asserts nothing is pending, then does a `reset → up` round-trip to prove every
migration is **reversible**. It bootstraps from empty every run (no reliance on prior state)
and is folded into the required `CI` gate, so a migration that errors, leaves pending state,
or fails the round-trip **blocks merge**. The CI Postgres **major version must match the
Railway-provisioned major** (both `18` today — Railway's `postgres-ssl` template
provisioned PG18) — bump them together.

---

## 7. Scale-to-zero: the dev Postgres is always-on

The dev SPAs scale to zero on PR close ([deploy-model.md](./deploy-model.md)). The dev
**Postgres does not** — it is **stateful and shared across PRs**, so it is deliberately
**excluded from the `preview-cleanup.yml` teardown matrix** (which lists only
`landing, app, ops-console`). A database that scaled to zero on every PR close would lose
migration state and break every open PR. Dev migrations are therefore forward-only/additive;
the reversibility guarantee is enforced against the *ephemeral CI* Postgres (§6), not the
shared dev one.

---

## Appendix: Provisioning the dev Postgres (M2-01 subtask 4)

**Status: DONE (2026-07-06).** The dev `Postgres` service exists in the `development`
environment (project `9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3`, service
`98723af0-50ca-42a4-a56a-3e0438b9ce8a`), image `postgres-ssl:18`, Online. Both roles are
bootstrapped and the baseline skeleton migration is applied (`goose_db_version` owned by
`invoice_migrator`). The steps below are the reusable runbook (re-provision / prod);
real passwords live **only** in Railway.

1. **Add Postgres** to the dev environment: `railway add -d postgres` (dashboard **New →
   Database → PostgreSQL** works too). Railway's `postgres-ssl` template currently
   provisions **PG18** — keep the CI Postgres major matched to it (§6). The service
   exposes `DATABASE_URL`/`DATABASE_PUBLIC_URL` (both **superuser**) and `PG*` vars;
   default database name is `railway`, private host `postgres.railway.internal:5432`.

2. **Bootstrap the two roles** once, as the superuser (idempotent — re-running rotates the
   passwords). Pick strong passwords, then from a machine with the repo (the public proxy
   URL is reachable off-network; `psql` via Docker if you don't have it locally):
   ```bash
   docker run --rm -v "$PWD/db:/db:ro" postgres:18 \
     psql "<Postgres.DATABASE_PUBLIC_URL>" -v ON_ERROR_STOP=1 \
       -v migrator_password="<MIGRATOR_PW>" -v app_password="<APP_PW>" -f /db/bootstrap.sql
   ```

3. **Store the app + migrator URLs**, built from the **private** host — never the public
   proxy, per §2:
   ```
   invoice_app     → postgresql://invoice_app:<APP_PW>@postgres.railway.internal:5432/railway
   invoice_migrator→ postgresql://invoice_migrator:<MIGRATOR_PW>@postgres.railway.internal:5432/railway
   ```
   Railway variables are **service-scoped** (no CLI shared-variable path), and there is no
   DB consumer yet (the gateway is M2-12), so these are stored **on the `Postgres` service**
   under non-colliding names — `INVOICE_APP_DATABASE_URL` and `INVOICE_MIGRATOR_DATABASE_URL`
   (plain `DATABASE_URL` on that service is Railway's superuser URL — do not overwrite it):
   ```bash
   printf '%s' "<migrator-url>" | railway variables set INVOICE_MIGRATOR_DATABASE_URL --stdin -s Postgres --skip-deploys
   printf '%s' "<app-url>"      | railway variables set INVOICE_APP_DATABASE_URL      --stdin -s Postgres --skip-deploys
   ```
   **M2-12 wires the consumers** by reference: each service sets
   `DATABASE_URL=${{Postgres.INVOICE_APP_DATABASE_URL}}` and the gateway's migration step
   sets `DATABASE_MIGRATION_URL=${{Postgres.INVOICE_MIGRATOR_DATABASE_URL}}`. **Never** hand
   any service `${{Postgres.DATABASE_URL}}` (superuser — disables RLS).

4. **Stays always-on:** the `Postgres` service is **not** in `preview-cleanup.yml`'s
   teardown matrix (§7) — that matrix is guarded with a comment and lists SPAs only.

## Related

- [add-a-service.md](./add-a-service.md) — provisioning compute services; §4 covers the
  per-service `cmd/<svc>/.env.example` and env-var conventions for service binaries.
- [deploy-model.md](./deploy-model.md) — the PR-preview + scale-to-zero model the dev
  Postgres is exempt from.
- `db/bootstrap.sql`, root `Makefile`, `migrations/` — the harness this doc specifies.
