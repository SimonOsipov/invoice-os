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
| `DATABASE_SUPERUSER_URL` | Postgres superuser | yes | `db/bootstrap.sql` | at boot, gated (see below) |

**The load-bearing rule:** never point the app or the migration step at Railway's
`${{Postgres.DATABASE_URL}}` (the superuser). A superuser has **BYPASSRLS** — every
tenant-isolation policy silently becomes a no-op and the failure is invisible until a
cross-tenant leak. `invoice_migrator` also owns every table, and a table's **owner**
bypasses RLS *unless the table is `FORCE`d* — which is exactly why the app connects as a
*separate* role (`invoice_app`), never as the migrator. (M2-07 proves the owner-bypass
case adversarially; M2-06 adds `FORCE ROW LEVEL SECURITY`.)

> **`DATABASE_SUPERUSER_URL` on the gateway itself (M4-21-04, Decision
> `[superuser-dsn-on-gateway]`).** Provisioning a fresh ephemeral PR-environment Postgres
> with no human in the loop means the gateway binary — not just a human running psql, and
> not just CI — now holds a superuser DSN as one of its own runtime environment variables.
> That is a deliberately accepted, narrowed tradeoff, not an oversight: `db.Provision`
> (`internal/platform/db/provision.go`) gates bootstrap/seed behind `BootstrapEnabled`'s
> ALLOWLIST (exactly `development` or a Railway PR-environment name — never a blocklist,
> never production, QA F1), and `Bootstrap`/`Seed` (`internal/platform/db/bootstrap.go`)
> each open and close their **own** dedicated superuser connection — the DSN is read once
> per gated step and **never retained** past the call that used it (QA F3); it is not
> stored, logged, or reachable from any request-serving code path
> (`TestSuperuserDSNNotRetainedForRequestPath` proves the request pool comes from
> `DATABASE_URL`, never the superuser DSN). This is why `db/bootstrap.sql` is no longer
> "once, at provisioning" the way it was before M4-21 — it now runs on every gated boot,
> idempotently.

### Role model (created by `db/bootstrap.sql`)

- `invoice_migrator` — `LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE`, granted
  `CREATE, USAGE ON SCHEMA public`. Owns every object a migration creates.
- `invoice_app` — same attributes, **no DDL, no ownership**. Gets table privileges only
  via explicit per-migration `GRANT`s (see §3).
- `invoice_tenant_reader` (added M2-06) — same attributes (`NOSUPERUSER NOBYPASSRLS`),
  granted `USAGE ON SCHEMA public` only. It is the **one cross-tenant enumeration
  identity**: a `FOR SELECT TO invoice_tenant_reader` policy on `tenants` lets it — and
  only it — read every tenant row, while `invoice_app` still sees only its current one.
  It has no runtime URL yet; that is provisioned when its first consumer (M5-06
  reconciliation) lands. See §8.
- Bootstrap also `REVOKE CREATE ON SCHEMA public FROM PUBLIC` (a no-op on PG15+, kept for
  PG13/14 + defense-in-depth).

`bootstrap.sql` is idempotent (DO-block role creation + `ALTER ROLE` re-assertion), run
as the superuser via psql. `make db-bootstrap` runs it with dev-default passwords; real
passwords live only in Railway.

> **Bootstrap drift on already-provisioned DBs (hit in M2-12).** Adding a role here
> (as M2-06 added `invoice_tenant_reader`) does **not** retroactively create it on a
> Postgres that was bootstrapped earlier — the dev Railway DB, bootstrapped at M2-01,
> was missing the reader when M2-12 first ran migrations against it, so
> `20260707122459_tenants_rls.sql` failed with `role "invoice_tenant_reader" does not
> exist`. Re-run `bootstrap.sql` against every live DB after adding a role. To create
> just the new role without rotating the existing roles' passwords (step 3 rotates all
> three — it would invalidate the live `DATABASE_APP_URL`/`DATABASE_MIGRATION_URL_SOURCE`
> vars), run only its
> `CREATE ROLE` + `ALTER ROLE … NOSUPERUSER NOBYPASSRLS` + schema `GRANT USAGE` lines.
>
> **Since M4-21-04** this specific failure mode is structurally mitigated for any
> environment where gated boot-time provisioning is enabled (see the
> `[superuser-dsn-on-gateway]` note above): `db.Provision` re-runs the idempotent
> `bootstrap.sql` on every gateway boot, so a role added to it is picked up automatically
> on that environment's next deploy rather than requiring a manual re-run. This history
> stands as the reason that behavior exists.

---

## 2. On-deploy mechanism — gateway-as-migrator, CI-ordered

Migrations are applied **at deploy time, in-network, as `invoice_migrator`, from a single
designated service: the gateway.** They run **never through a public proxy**, and no
separate migration job holds its own inbound DB access.

> **Since M4-22**, no CI job or code path anywhere in this repo discovers, holds, or
> connects through a public DB proxy for anything — `health-gate` (the gateway's own
> `/healthz`) replaces liveness probing, HTTP against the gateway replaces the E2E suites'
> old direct DB access, and the boot-time seed replaces the deleted reset/seed job (see
> §7 below and [topology-e2e.md](./topology-e2e.md)). The underlying Railway TCP proxy
> resource on `development`'s Postgres is deleted via a separate, human Railway-console
> step (M4-22 Escalation E2) — until that completes, the resource itself may still exist,
> unused by anything in this repo.

How the ordering works (wired at **M2-12**, when the gateway exists):

1. On a deploy, CI deploys the **gateway first** and **health-gates** it — the gateway
   runs `goose up` (against `DATABASE_MIGRATION_URL`) as part of coming up healthy, so the
   schema is fully migrated *before* it reports healthy.
2. Only after the gateway is healthy does CI deploy the **seven context services**. They
   boot against an already-migrated schema and never run migrations themselves.

This gives a **global ordering barrier** (schema-before-fleet) using only in-network
connections — the gateway is the one service that already needs privileged DB reach, so
it doubles as the migrator. No context service is granted the migrator URL.

> **Realized in M2-12.** The gateway image is distroless (the binary only — no shell, no
> goose binary), so the migrator step runs *inside the binary*. `migrations/embed.go`
> ships every `migrations/*.sql` via `go:embed`, and `cmd/gateway` calls
> `internal/platform/db.MigrateUp` (goose's Provider `Up` over the pgx v5 stdlib driver,
> against `DATABASE_MIGRATION_URL`) **synchronously in `main`, before `app.Run` opens the
> listener**. `/healthz` is always-200 liveness, so it can only answer *after* migration
> returns — that is the "migrated before healthy" guarantee, enforced by process order
> rather than a probe. A migration error is fatal (`log.Fatal`), so the deploy never goes
> healthy. The CI ordering lives in `.github/workflows/dev-env.yml` (the `preview-backend.yml`
> name above is retired — dev-env.yml superseded it at M2-14): deploy the
> gateway → poll its public `/healthz` until 200 (the health-gate, which also surfaces a
> failed migration) → deploy the seven context services. Because the gateway embeds the
> SQL, its `cmd/gateway/railway.json` watch patterns include `migrations/**` — the one
> service for which a migration change would rebuild the image if Railway's committed
> `watchPatterns` field were wired to anything (it isn't — add-a-service.md §3's gotcha;
> since M3-16 every service's *instance-level* Watch Paths are cleared to empty and
> `dev-env.yml`'s `prepare-env` job asserts that invariant at runtime, so triggering no
> longer depends on this field at all).

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
- **River's queue tables (M2-08)** are managed by the queue library; the app touches them
  only through River's API. Its migration grants `invoice_app` exactly the DML River's
  driver issues per table (fetch/enqueue/complete/clean on `river_job` + `USAGE` on
  `river_job_id_seq`; leader election on `river_leader`; upsert/pause on `river_queue`;
  cleanup-only `DELETE` on `river_notification`) — explicit and per-table, never a blanket
  `ALL`. The same migration's app-owned `idempotency_keys` (the outbox dedupe ledger) IS
  tenant data and copies the FORCE-RLS `tenants` template; being permanent/append-only it
  gets `SELECT`/`INSERT` only, like `audit_log`.

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

### The helper (M2-06): `WithinTenantTx`

Application code never issues `SET LOCAL` by hand. The single sanctioned entry point is
`db.WithinTenantTx` in `internal/platform/db`:

```go
db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
    // every tenant-scoped query for this unit of work runs on tx
})
```

What it guarantees:

- **Sets the GUC inside a transaction** via `SELECT set_config('app.current_tenant', $1,
  true)` — the *function* form of `SET LOCAL`, because the bare `SET LOCAL` statement
  cannot bind a parameter. `is_local = true` scopes the setting to this transaction.
- **Transaction-scoped ⇒ no pooled-connection bleed.** When the tx commits or rolls back
  the setting evaporates, so the next borrower of that pooled connection starts clean.
  This is the invariant M2-07 attacks (pooled-connection reuse must not carry tenant
  context across transactions).
- **Fail-closed.** `tenantID` is parsed as a UUID *before* the tx opens; empty or malformed
  input returns `ErrNoTenant` and issues **no** statement — the helper can never run an
  unscoped query.
- **Explicit tenant, not context-derived.** The core helper takes the tenant as an
  argument so it serves both the HTTP path and the worker (§8). `WithinRequestTenantTx`
  is a thin wrapper that pulls the tenant from the request `auth.Identity` for handlers.

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

**Local** (`make help` lists these). `make dev-db` is the one-command entry point and
needs **no `.env`**; the lower-level targets read the §1 `DATABASE_*` URLs from `.env`
(copy `.env.example`) or the environment:

| Command | Does | As role |
|---|---|---|
| `make dev-db` | **one command** — `docker compose up` a local Postgres → bootstrap roles (in-container) → apply all migrations | — |
| `make dev-db-down` | stop/remove the local Postgres container (keeps the data volume) | — |
| `make dev-db-reset` | drop the data volume and rebuild the local DB from empty | — |
| `make db-bootstrap` | create/rotate the two roles (needs a host `psql`) | superuser |
| `make migrate-up` | apply all pending migrations | migrator |
| `make migrate-down` | roll back the latest migration | migrator |
| `make migrate-reset` | roll back **all** migrations | migrator |
| `make migrate-status` | show applied/pending state | migrator |
| `make migrate-create name=<slug>` | scaffold a timestamped SQL migration | — |

The local DB comes from `docker-compose.yml` — a `postgres:18` service on
`localhost:$DEV_DB_PORT` (default `5432`; override to run multiple worktrees'
stacks concurrently, e.g. `DEV_DB_PORT=5433 make dev-db`), database `invoice_os`;
its major matches the CI/Railway major (the
§6 rule below). `make dev-db` bootstraps the roles *inside* the container, so a host
`psql` client is **not** required for the local loop (only `make db-bootstrap` run on
its own needs one). The app and `go test` connect as `invoice_app`, so RLS behaves
locally exactly as in CI/prod. This compose file is **local-only** — CI spins up its
own ephemeral Postgres service container.

**CI** (`.github/workflows/ci.yml`, `migrations` job — the independent hard gate): on a
fresh Postgres service container it runs `db/bootstrap.sql` as the superuser, `goose up` as
the migrator, asserts nothing is pending, then does a `reset → up` round-trip to prove every
migration is **reversible**. It bootstraps from empty every run (no reliance on prior state)
and is folded into the required `CI` gate, so a migration that errors, leaves pending state,
or fails the round-trip **blocks merge**. The CI Postgres **major version must match the
Railway-provisioned major** (both `18` today — Railway's `postgres-ssl` template
provisioned PG18) — bump them together. `reset` rolls migrations back in **reverse
application order** (goose's documented `Reset()` semantics) — the newest migration's Down
runs first. `20260717120000_rule_immutability_lock.sql` (M4-17) and `20260716185106_rule_set_v2.sql`
both depend on this: the lock's Down must drop its guards before either older migration's own
Down runs against `rule_set_versions`/`rules`.

A second DB-backed job, **`rls`**, bootstraps the roles the same way, applies the
migrations, and then runs the M2-07 adversarial isolation suite (§8) as the app, migrator,
and reader roles. It is also folded into the required `CI` gate. Locally, `make test-rls`
runs the same suite against the `make dev-db` Postgres.

---

## 7. Per-PR Postgres (M4-23) vs. the always-on `development` Postgres

Since M4-23, each PR gets its **own** ephemeral Railway environment — including its **own**
Postgres, born empty and bootstrapped + migrated + seeded fresh at gateway boot
(`internal/platform/db.Provision`, M4-21-04). When the PR closes, merged or not,
`dev-env-teardown.yml` deletes that whole environment — Postgres included — and the daily
`dev-env-sweeper.yml` reaps any the close event missed (M4-23). Losing that ephemeral DB's
state costs nothing; nothing else depends on it once the PR is gone.

The **`development` environment's own Postgres is different: it is stateful, persistent,
and never torn down** (Decision `[dev-env-status]`) — it is the fork base every PR
environment is created from, and the target of live demo calls.
Migrations against it are therefore forward-only/additive; the reversibility guarantee is
enforced against the *ephemeral CI* Postgres (§6) instead, never against `development`'s.

---

## 8. Cross-tenant access: the enumeration seam and the worker-role pattern (M2-06)

Per-tenant `SET LOCAL` (§4) is the rule for touching tenant data. Two legitimate
operations need to reach *more than one* tenant, and M2-06 fixes how — so M5 wires the
worker and reconciliation with **no new decision**.

### The worker-role pattern

The M5 submission worker processes jobs for many tenants from one long-lived process. It
is **not** special-cased against RLS:

- It connects as **`invoice_app`** — the same `NOBYPASSRLS` runtime role, the same
  `DATABASE_URL`. No dedicated worker role, no superuser.
- Every job payload carries its **`tenant_id`**, and the worker wraps each job's business
  logic in `WithinTenantTx(ctx, pool, job.TenantID, …)` — the *same* helper as the HTTP
  path, per-job `SET LOCAL`. `SET LOCAL` (not `SET SESSION`) is what keeps a pooled
  connection from carrying one job's tenant into the next.
- **River's own queue tables are infrastructure, not tenant data** — they have no
  `tenant_id` and no RLS; the worker operates them outside any tenant context. Only the
  job *handler's* tenant-scoped work runs inside `WithinTenantTx`.
- A **cross-tenant batch** (e.g. reconciliation over every tenant) is a **loop that sets
  one tenant context at a time** — never a single blanket read. It is isolation-preserving
  by construction.

**Realized in M2-08.** `internal/platform/queue` builds the River client and the
`EnqueueTx` transactional-outbox helper (a domain change, its `idempotency_keys` row, and
the River job commit or roll back together); `cmd/submission` runs the worker pool on the
platform-kit lifecycle hook (`platform.BackgroundWorker`), draining in the shutdown window.
Each `SubmitWorker`/`PollWorker` job wraps its tenant-scoped work in
`WithinTenantTx(job.Args.TenantID, …)`.
The happy-path proof is the `queue` CI job (`make test-queue` locally); the adversarial
exactly-once / re-drive suite is M2-09.

### The enumeration seam — the one place per-tenant `SET LOCAL` can't reach

That batch loop needs the **list** of tenants to iterate, and `SELECT … FROM tenants` as
`invoice_app` returns only the current tenant's row. Enumerating *all* tenants is the one
operation the per-tenant model structurally cannot serve. Because the escapes are
deliberately closed — no `BYPASSRLS` role exists, and under `FORCE` even a `SECURITY
DEFINER` function owned by the table owner is still subject to the policy — M2-06 solves it
with a **narrowly-scoped second policy**, not a bypass:

```sql
CREATE POLICY tenant_enumerate ON tenants
    FOR SELECT TO invoice_tenant_reader USING (true);
```

RLS combines permissive policies with **OR**, so for `invoice_tenant_reader` the predicate
is `(id = current_tenant) OR true` = every row; `invoice_app` is not in that policy's `TO`
list, so it still sees exactly one. `invoice_tenant_reader` stays `NOBYPASSRLS` — its reach
is exactly what this policy grants, nothing more. Its Railway connection URL is
provisioned when M5-06 (reconciliation) becomes its first consumer (same
store-on-`Postgres`-service pattern as the app/migrator URLs — see the Appendix).

### Testing

The isolation matrix is proven by the **M2-07 adversarial RLS suite**
(`internal/platform/db/rls_test.go` + `rls_harness_test.go`), which attacks the
guarantees as each role against a real Postgres and asserts every attack fails:

- cross-tenant `SELECT`/`INSERT`/`UPDATE` are refused (reads return zero rows; writes
  raise a `WITH CHECK` violation, SQLSTATE 42501),
- a missing `app.current_tenant` fails **closed** (zero rows, never a full read),
- the table **owner** (`invoice_migrator`) cannot bypass a `FORCE`'d table, and no role
  holds `BYPASSRLS`/`SUPERUSER`,
- a reused pooled connection cannot carry tenant context across transactions
  (the `set_config(..., is_local=true)` = `SET LOCAL` invariant), and
- the **enumeration role** (`invoice_tenant_reader`) sees all tenants while `invoice_app`
  sees only its current one.

Writes need an app-writable table, but `tenants` grants `invoice_app` SELECT only — so
the suite creates a **test-only** `tenant_id`-column fixture table (the shape every M3+
table copies) with write grants to the app role, at runtime as the migrator; it is not a
committed migration. It runs in CI (the **`rls`** job: bootstrap roles → migrate →
attack; folded into the required `CI` gate) and locally via `make test-rls` (after
`make dev-db`). The suite **skips itself** when the per-role `DATABASE_*` URLs are absent,
so the default `go` job and a bare `go test ./...` stay green without a database.

> The build plan floated *testcontainers*; we instead reuse the same
> Postgres-service-container + Makefile-bootstrap path as the `migrations` job — no new Go
> dependency, one canonical bootstrap (`db/bootstrap.sql`), CI-consistent.

---

## Appendix: Provisioning the dev Postgres (M2-01 subtask 4)

**Scope note:** this runbook provisions `development`'s own Postgres — the one
persistent, always-on service (§7). It is a one-time / re-provision runbook, not something
run per-PR: a PR's ephemeral Postgres comes from the `environmentCreate` fork that
`dev-env.yml`'s `prepare-env` job issues (the *service* is inherited from `development`;
the fork carries no deployment, so `prepare-env` deploys it explicitly), and is then
bootstrapped/migrated/seeded by the gateway at boot
(`db.Provision`, M4-21-04, `[superuser-dsn-on-gateway]` above) — no human runs the steps
below for it.

**Status: DONE (2026-07-06).** The dev `Postgres` service exists in the `development`
environment (project `9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3`, service
`98723af0-50ca-42a4-a56a-3e0438b9ce8a`), image `postgres-ssl:18`, Online. Both roles are
bootstrapped and the baseline skeleton migration is applied (`goose_db_version` owned by
`invoice_migrator`). The steps below are the reusable runbook (re-provision / prod);
real passwords live **only** in Railway.

1. **Add Postgres** to the dev environment: `railway add -d postgres` (dashboard **New →
   Database → PostgreSQL** works too). Railway's `postgres-ssl` template currently
   provisions **PG18** — keep the CI Postgres major matched to it (§6). The service
   exposes `DATABASE_URL` (superuser) plus its own public-proxy variant, and `PG*` vars —
   step 2 below never touches either public form; default database name is `railway`,
   private host `postgres.railway.internal:5432`.

2. **Bootstrap the three roles**: choose strong passwords, then set them on the
   **gateway** service as `MIGRATOR_PASSWORD` / `APP_PASSWORD` / `READER_PASSWORD`,
   alongside `DATABASE_SUPERUSER_URL=${{Postgres.DATABASE_URL}}` and
   `GATEWAY_DB_BOOTSTRAP=true`. No psql, no public proxy, no manual SQL against Postgres
   at all — on its next boot the gateway bootstraps the roles itself, in-network
   (`internal/platform/db.Provision` → `Bootstrap`, gated by `BootstrapEnabled`'s
   allowlist — `[superuser-dsn-on-gateway]`, §1). This has been the real mechanism since
   M4-21-03/04; the psql-through-the-public-proxy sequence this section used to describe
   is obsolete, and the public proxy plays no role in it.

   > **Gateway-side role password variable names (M4-22-09).** The gateway prefers the
   > unprefixed `MIGRATOR_PASSWORD` / `APP_PASSWORD` / `READER_PASSWORD` variables
   > (matching `Makefile`/CI), falling back to their deprecated, legacy-prefixed
   > `INVOICE_*_PASSWORD` equivalents (see `cmd/gateway/main.go`'s `resolveRolePassword`)
   > and logging a warning when it does. `development`'s live `gateway` service still
   > needs the new names added (M4-22 Escalation E1, carrying the existing values across
   > — never regenerated) before the fallback stops firing there. Once every Railway
   > environment is confirmed on the new names (Escalations E1/E3/E4), the fallback
   > becomes dead code and should be deleted along with the deprecated Railway variables.

3. **Store the app + migrator URLs**, built from the **private** host — never the public
   proxy, per §2:
   ```
   invoice_app     → postgresql://invoice_app:<APP_PW>@postgres.railway.internal:5432/railway
   invoice_migrator→ postgresql://invoice_migrator:<MIGRATOR_PW>@postgres.railway.internal:5432/railway
   ```
   Railway variables are **service-scoped** (no CLI shared-variable path), and there is no
   DB consumer yet (the gateway is M2-12), so these are stored **on the `Postgres` service**
   under non-colliding names — `DATABASE_APP_URL` and `DATABASE_MIGRATION_URL_SOURCE`
   (plain `DATABASE_URL` on that service is Railway's superuser URL — do not overwrite it):
   ```bash
   printf '%s' "<migrator-url>" | railway variables set DATABASE_MIGRATION_URL_SOURCE --stdin -s Postgres --skip-deploys
   printf '%s' "<app-url>"      | railway variables set DATABASE_APP_URL              --stdin -s Postgres --skip-deploys
   ```
   **M2-12 wires the consumers** by reference: each service sets
   `DATABASE_URL=${{Postgres.DATABASE_APP_URL}}` and the gateway's migration step
   sets `DATABASE_MIGRATION_URL=${{Postgres.DATABASE_MIGRATION_URL_SOURCE}}`. **Never** hand
   any service `${{Postgres.DATABASE_URL}}` (superuser — disables RLS).

   > **As of this merge**, `development`'s live Postgres service still exposes these
   > under the deprecated `INVOICE_APP_DATABASE_URL` / `INVOICE_MIGRATOR_DATABASE_URL`
   > names (M4-22 Escalation E3, pending) — unlike the passwords, no code fallback exists
   > for these two names, since nothing in the repo reads them directly
   > (Railway-reference-only). This runbook shows the target names any fresh
   > provisioning should use.

4. **Stays always-on:** `development`, Postgres included, is never torn down by CI (§7).
   The teardown workflows act only on ephemeral `pr-<N>` environments and refuse
   `development` outright.

## Related

- [add-a-service.md](./add-a-service.md) — provisioning compute services; §4 covers the
  per-service `cmd/<svc>/.env.example` and env-var conventions for service binaries.
- [deploy-model.md](./deploy-model.md) — the per-PR ephemeral-environment model (create →
  deploy → verify → teardown → sweep) the dev Postgres is exempt from.
- `db/bootstrap.sql`, root `Makefile`, `migrations/` — the harness this doc specifies.
