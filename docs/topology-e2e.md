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
(non-draft) PR — after the fleet is deployed and the dev DB is reset + seeded, alongside the
smoke suite. `dev-env.yml` flow:

```
gateway ──> gate on /healthz (schema migrated)
        ──> deploy 7 context services + 3 SPAs (app is gateway-wired: VITE_GATEWAY_URL set)
        ──> reset + seed the dev DB (data-only, superuser)
        ──> verify: smoke (landing + ops-console) + topology (fleet gate, browser login, isolation)
```

The whole coherent env then stays up (see [deploy-model.md](./deploy-model.md)) rather than
being torn down after the assertions run. `dev-env.yml` remains dispatchable by hand
(`workflow_dispatch`) to re-run the deploy + verify flow on demand.

## One-time prerequisites (human-applied)

The workflow is self-contained **except** for credentials and pre-existing gateway auth
config it deliberately does not clobber. Apply these once:

### Railway service variables

The workflow **sets these itself** at deploy time (`railway variables --set … --skip-deploys`),
so no manual step is needed — listed here for the record:

| Service | Variable | Value |
|---|---|---|
| `app` | `VITE_GATEWAY_URL` | `https://gateway-development-997b.up.railway.app` (Vite bakes it at build) |
| `gateway` | `GATEWAY_MOCK_ISSUER` | `true` |
| `gateway` | `CORS_ALLOWED_ORIGINS` | `https://app-development-3b4b.up.railway.app` |

**Assumed already present on the gateway (from M2-12/M2-13 — NOT set by this workflow):**
`AUTH_ISSUER`, and `AUTH_JWKS_URL` pointing at the gateway's **own**
`https://gateway-development-997b.up.railway.app/.well-known/jwks.json` so the mock round
trip verifies. If the round trip 401s, this is the first thing to check.

### GitHub secrets

| Secret | Value |
|---|---|
| `DATABASE_SUPERUSER_URL_DEV` | The dev Postgres **PUBLIC** superuser DSN (Railway → Postgres → Connect → Public Network). Required because GitHub runners can't reach `*.railway.internal`, and `tenants` is FORCE ROW LEVEL SECURITY so only the superuser (BYPASSRLS) can TRUNCATE + seed it. |
| `RAILWAY_API_DEV_TOKEN` | Already present — the same dev project token the rest of `dev-env.yml` uses. |

## The data-only reset

`db/reset.dev.sql` (`TRUNCATE tenants CASCADE`) then `db/seed.dev.sql` re-applies the
canonical fixtures — the isolation pair (`aaaa…`/`bbbb…`) plus the persona tenants (`1111…`
Okafor & Partners / `2222…` Honeywell Group). It is **data-only**: schema and migration
history are untouched and persist (the dev Postgres is always-on). Idempotent — safe to
re-run. Setup owns correctness, not teardown, so the fixtures are deterministic regardless
of prior state.

## Related

- [deploy-model.md](./deploy-model.md) — the unified deploy + verify flow this suite runs
  inside of, and the scale-to-zero teardown on PR close.
- `e2e/README.md` — the smoke + topology suites, run commands, and target-URL conventions.
- `db/seed.dev.sql` — the canonical fixtures re-applied on every run.
