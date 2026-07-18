# Topology E2E — the M2 exit criterion (M2-14)

The end-to-end proof that the platform foundation works as one system against the **live
dev fleet**: a real browser signs in on the deployed app SPA and renders a
backend-resolved tenant identity, every backend's health is green, and cross-tenant
isolation holds over the full edge path. Realized by the post-deploy verification steps of
`.github/workflows/dev-env.yml` plus the gateway CORS + fleet-health route (M2-14.1 / .2),
the deployed-dev wiring (M2-14.3), and the Playwright topology suite (`e2e/topology/`,
M2-14.4).

## What it asserts

1. **Fleet /healthz gate** — the gateway's public `GET /healthz/fleet` roll-up reports all
   8 backends (gateway + 7 context services) green; the run fails naming any that are down.
   The context services are private-network-only, so this route is the only way CI sees
   their health through the one public backend surface.
2. **Live browser login** — a Playwright test drives the persona mock-login on the deployed
   app SPA and asserts the **verified** tenant identity renders: the sidebar marker
   `title="Tenant verified via /v1/me"`. That marker is the discriminator — the static firm
   fallback shows the same "OKAFOR & PARTNERS" label, so only the marker proves the round
   trip (mint → `GET /api/tenancy/v1/me`) resolved a backend identity.
3. **Cross-tenant isolation** — mints a tenant-A and a tenant-B token via the gateway's mock
   issuer and asserts `GET /api/tenancy/v1/me` returns exactly the caller's own tenant. Both
   rows exist in the seeded table, so RLS (JWT-verify → inject `X-Tenant-ID` → `SET LOCAL
   app.current_tenant` → policy) — not a missing row — is demonstrably the filter.

## How it runs

There is no standalone topology workflow. These assertions run automatically as the
post-deploy verification steps of `.github/workflows/dev-env.yml`, on every ready
(non-draft) PR — after the fleet is deployed to that PR's own ephemeral Railway
environment (M4-21) and its Postgres is bootstrapped + seeded fresh at gateway boot
(M4-21-04), alongside the smoke, api, and demo suites. `dev-env.yml` flow:

```
resolve-env ──> poll for this run's target environment (this PR's ephemeral fork, or
                `development` on workflow_dispatch) ──> assert Watch Paths empty
                (M3-16 invariant) ──> discover the 4 public URLs fresh
gateway     ──> gate on /healthz (schema migrated + DB seeded at boot)
            ──> deploy 7 context services + 3 SPAs (app is gateway-wired: VITE_GATEWAY_URL
                is a durable Railway reference variable, M4-21-05)
            ──> verify: smoke (landing + ops-console) + api (typed contract suite) +
                topology (fleet gate, browser login, isolation) + demo (Day-30 journey)
```

reset-seed (`db/reset.dev.sql` + `db/seed.dev.sql`, superuser) is **dispatch-path-only**
now (M4-21-06) — it runs only on a `workflow_dispatch` run against the persistent
`development` environment, never on a PR run, whose own Postgres is born empty and
self-seeds at gateway boot instead (see "The data-only reset" below). The PR's environment
then stays up while the PR is open — Railway deprovisions it automatically on close (see
[deploy-model.md](./deploy-model.md)); `development` is never deprovisioned. `dev-env.yml`
remains dispatchable by hand (`workflow_dispatch`) to re-run the deploy + verify flow
against `development` on demand.

## Prerequisites

Most of the workflow's inputs are now **discovered at runtime** rather than pre-applied —
see [deploy-model.md](./deploy-model.md) for the full per-PR-environment prerequisites list
(Railway PR Environments enabled, the account-scoped `RAILWAY_API_TOKEN`, the
empty-Watch-Paths invariant). What remains one-time / human-applied:

### Railway variables (durable reference variables, not per-run `--set`)

`app.VITE_GATEWAY_URL`, `gateway.GATEWAY_MOCK_ISSUER`, and `gateway.CORS_ALLOWED_ORIGINS`
are durable Railway **reference variables** on `development` (M4-21-05, task-129) — Railway
forks them into every PR environment along with the rest of `development`'s variable
topology, so the workflow no longer sets any of them per-run.

**Assumed already present on the gateway (from M2-12/M2-13):** `AUTH_ISSUER`, and
`AUTH_JWKS_URL` pointing at the gateway's **own**
`<discovered gateway URL>/.well-known/jwks.json` so the mock round trip verifies — this,
too, is forked per-environment by Railway. If the round trip 401s on a PR environment, this
is the first thing to check.

### GitHub secrets

| Secret | Value |
|---|---|
| `RAILWAY_API_DEV_TOKEN` | The `development` project token — used by `workflow_dispatch` runs and by every run's Watch-Paths/URL/DSN discovery queries when the trigger is `workflow_dispatch`. |
| `RAILWAY_API_TOKEN` | The account-scoped token (task-131) every PR-triggered run authenticates with — a project token cannot reach an ephemeral PR environment (F6). |

No workflow reads a `DATABASE_SUPERUSER_URL_DEV` GitHub secret anymore (M4-21-10) — the
Postgres superuser DSN is discovered fresh per run via the Railway GraphQL API
(`variables(projectId, environmentId, serviceId)` → `DATABASE_PUBLIC_URL`), masked with
`::add-mask::` inside the job that uses it, never a job output. `make demo-reset`'s
same-named **local** environment variable is unrelated and unaffected.

## The data-only reset (`development` / `workflow_dispatch` only)

`db/reset.dev.sql` (`TRUNCATE tenants CASCADE`) then `db/seed.dev.sql` re-applies the
canonical fixtures — the isolation pair (`aaaa…`/`bbbb…`) plus the persona tenants (`1111…`
Okafor & Partners / `2222…` Honeywell Group) — against `development`'s Postgres. It is
**data-only**: schema and migration history are untouched and persist. Idempotent — safe to
re-run. Setup owns correctness, not teardown, so the fixtures are deterministic regardless
of prior state. A PR's own ephemeral environment never runs this step — its Postgres is
born empty and the gateway seeds it once, at boot (`internal/platform/db.Provision`,
M4-21-04), so there is nothing to reset.

## Cold-fleet recovery (M3-16)

The topology suite (and the smoke suite alongside it) only runs once `fleet-gate` and
`deploy-spas` are both green — so it depends on every service in the fleet actually coming
up on `dev-env.yml`'s `railway up` step, including services a given PR doesn't touch. Since
M4-21 every environment is a **fresh, cold, from-scratch 11-service build** (a new PR fork,
or a `workflow_dispatch` run against `development`), so this is the norm on every run, not
an edge case: each Railway service has a service-level **Watch Paths** filter that makes
`railway up` skip (no deployment created) when the diff misses the service's watched
paths — a skip would leave that service Offline and fail `health-gate` or `fleet-gate`
before the E2E suites ever ran. (`railway.json`'s `build.watchPatterns` field looks like it
should control this but Railway ignores it entirely — it is not wired to anything, which is
why an earlier attempt to fix this via `railway.json` had no effect.)

**Fix / invariant, now runtime-asserted:** service-level Watch Paths were cleared to empty,
out-of-band, on all 11 non-Postgres services. With Watch Paths empty, `railway up --ci
--service <svc>` always builds and deploys the working tree — for every service, on every
`dev-env.yml` run. `resolve-env` (M4-21-09) additionally **asserts** every non-Postgres
service instance in the target environment still reports empty Watch Paths and fails the
run, naming the offender(s), if not — a regression can no longer reach the deploy steps
silently. This is Railway-side config applied once, not something expressed in
`railway.json` — see [deploy-model.md](./deploy-model.md) "Cold-fleet recovery (M3-16)" for
the root cause and the full rationale (Approach 3: always-rebuild, chosen after live
experiments falsified scale-to-0 and diff-driven alternatives).

The gateway `health-gate` window was widened again under M4-21 (360s → 900s) — and
`fleet-gate` / the e2e SPA `/health` wait (200s → 600s) — since every environment is now a
cold 11-service build, not the exception a warm redeploy used to be (Decision
`[gate-windows-provisional]`).

## Related

- [deploy-model.md](./deploy-model.md) — the per-PR-environment deploy + verify flow this
  suite runs inside of, and Railway's automatic teardown on PR close.
- `e2e/README.md` — the smoke + topology suites, run commands, and target-URL conventions.
- `db/seed.dev.sql` — the canonical fixtures re-applied on every run.
