# Add-a-Service Recipe (Railway)

**Audience:** whoever stamps the next backend service (M2-12: gateway + seven context
services; M7-01: opsconsole). Follow it verbatim — every value below is either fixed by
convention or derived mechanically from the service name. If you find yourself making a
judgment call, the recipe is broken: fix the recipe, then the service.

**Railway target (only one environment exists):**

| | ID |
|---|---|
| Project | `9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3` |
| Environment `development` | `6c864094-6a06-452f-8495-be77d8a94fe7` |

**Validation status.** Everything in this recipe was cross-checked on 2026-07-05 against
the live config of the three SPA services created in M1-06 (fetched via GraphQL, not from
memory), and every mutation name in the runbook exists in the current schema. The
compute-specific parts — the shared Go Dockerfile contract (§1), `/healthz`, and
private-networking-only ingress — are *specified* here but first *proven* by M2-12, the
first compute consumer. If M2-12 has to deviate, update this doc in the same PR.

---

## 0. Naming and placeholders

A backend service is one Go binary under `cmd/<svc>/`. Throughout this doc:

- `<svc>` — the service name, identical in: the `cmd/<svc>` directory, the Railway
  service name, and the private-networking hostname `<svc>.railway.internal`.
  Lowercase, no separators (matches existing `cmd/` dirs: `tenancy`, `portfolio`,
  `invoice`, `validation`, `submission`, `dashboard`, `notifications`, `opsconsole`;
  plus `gateway`).
- `<ctx>` — the service's domain context dir `internal/<ctx>`. For the seven context
  services `<ctx>` = `<svc>`. The gateway has no context dir — drop that line wherever
  it appears.

## 1. Shared Dockerfile contract (file lands in M2, contract fixed now)

All Go services build from **one** Dockerfile at the **repo root** (`Dockerfile`), with
the repo root as build context — same context convention as the SPA Dockerfiles. It is
parameterized by a single build arg:

```dockerfile
ARG SERVICE            # e.g. tenancy — builds ./cmd/${SERVICE}
```

Contract: multi-stage build; build stage compiles `./cmd/${SERVICE}` with the
repo-root `go.mod`/`go.sum`; the run stage contains only the binary and listens on
`$PORT`. The file itself is written in M2 alongside the first compilable Go code
(nothing can build or verify it today — `cmd/` dirs are placeholders). Do not write
per-service Dockerfiles.

## 2. The config block — `cmd/<svc>/railway.json`

One file per service, committed next to the binary's `main.go`. Copy verbatim,
substitute `<svc>`/`<ctx>`:

```json
{
  "$schema": "https://railway.com/railway.schema.json",
  "build": {
    "builder": "DOCKERFILE",
    "dockerfilePath": "Dockerfile",
    "buildArgs": { "SERVICE": "<svc>" },
    "watchPatterns": [
      "cmd/<svc>/**",
      "internal/<ctx>/**",
      "internal/platform/**",
      "internal/audit/**",
      "go.mod",
      "go.sum",
      "Dockerfile"
    ]
  },
  "deploy": {
    "healthcheckPath": "/healthz",
    "restartPolicyType": "ON_FAILURE"
  }
}
```

Fixed by convention, not per-service choice:

- **`/healthz`** is the health endpoint for every backend service (the SPAs use
  `/health` via Caddy; that difference is deliberate and stays).
- **`ON_FAILURE`** restart policy everywhere.
- **Root Directory stays `/`** (unset) on the Railway service — the build needs the
  whole repo (root `go.mod`, shared `internal/`).

## 3. Watch-path convention

The watch patterns answer one question: *which paths, when changed on `main`, mean this
service must rebuild?* For a Go service that is exactly:

| Pattern | Why |
|---|---|
| `cmd/<svc>/**` | the binary itself |
| `internal/<ctx>/**` | its domain context (omit for gateway) |
| `internal/platform/**` | shared db/config/river/jwt code — imported by every service |
| `internal/audit/**` | shared audit module (D7) — imported by every service |
| `go.mod`, `go.sum` | dependency changes |
| `Dockerfile` | the shared build definition |

> **Note on `internal/audit/**`:** the build plan's M2-12 line names only
> `cmd/<svc> + internal/<ctx> + internal/platform`. `internal/audit` is added here
> because it is a shared module of the same class as `internal/platform` (imported by
> all services per D7) — omitting it would leave stale deployments after audit-module
> changes. If M2 restructures audit, revisit this line.

Do **not** add `migrations/**` — schema migrations are not a service-image concern and
must not trigger fleet-wide rebuilds. Do **not** add another service's context dir; if
service A needs service B's types, that coupling is the problem, not the watch list.

### ⚠️ The instance-level gotcha (this is half the reason this doc exists)

`railway.json` is applied when a **build starts** — but watch patterns are evaluated
**at trigger time**, from the **service instance** settings, *before* any config file is
read. A freshly created service has instance `watchPatterns: []`, which Railway treats
as "rebuild on every push". Net effect: config-as-code alone silently breaks deploy
isolation — every push to `main` rebuilds every service.

**Therefore: after creating the service, always mirror the watch patterns onto the
service instance via `serviceInstanceUpdate` (runbook step 3).** This was hit and
confirmed in M1-06; the live check (2026-07-05) shows the same asymmetry the other way:
instance fields report `builder: RAILPACK` and `healthcheckPath: null` while the
deployments demonstrably build via Dockerfile and answer the healthcheck — i.e. the
instance record and the effective config genuinely diverge. Trust `railway.json` for
build/deploy behavior; trust (and maintain) the **instance** for trigger behavior.

## 4. `.env.example` and env-var conventions

### `.env.example`

One per service at **`cmd/<svc>/.env.example`**, committed (the root `.gitignore`
already carves out `!.env.example`; real `.env` files are ignored). Rule: **every
environment variable the binary reads appears here** with a dev-safe placeholder — a
new variable read in code without a matching `.env.example` line is a review-blocking
omission. Baseline template:

```bash
# cmd/<svc>/.env.example — every env var this binary reads, dev-safe values only.
# Local: copy to .env (gitignored). Railway: set real values as service variables.
PORT=8080
DATABASE_URL=postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable
# <service-specific vars below, one comment line each explaining what it does>
```

### Conventions

- **Names:** `UPPER_SNAKE_CASE`. No per-service prefix — each Railway service has its
  own variable set, so prefixes add nothing.
- **`PORT=8080` everywhere, set explicitly** (runbook step 4). Railway injects `$PORT`
  at runtime; we pin it so private-networking callers can rely on
  `http://<svc>.railway.internal:8080` without per-service discovery.
- **Bind to all interfaces** (`:PORT`, not `127.0.0.1:PORT`). Railway private
  networking is IPv6-based; loopback-bound listeners are unreachable.
- **Cross-service values via reference variables**, never hardcoded:
  `${{Postgres.DATABASE_URL}}` for the database,
  `${{<svc>.RAILWAY_PRIVATE_DOMAIN}}` if a service needs another's hostname.
- **Railway injects `RAILWAY_*` variables automatically** (`RAILWAY_PRIVATE_DOMAIN`,
  `RAILWAY_SERVICE_NAME`, `RAILWAY_ENVIRONMENT_NAME`, …). Never set these manually;
  don't list them in `.env.example`.
- **Secrets live only in Railway service variables.** Never in the repo, never in
  `railway.json`, never as a real value in `.env.example`.
- **Public exposure:** only the three SPAs and the gateway get a public domain
  (runbook step 6). Context services, opsconsole, and Postgres are private-network
  only — for a backend service, *skipping* step 6 is what keeps it private.

## 5. Provisioning runbook

Auth: the Railway MCP server is unauthenticated in this setup — use the CLI token
against the public GraphQL API (or do the same steps in the dashboard UI):

```bash
TOKEN=$(jq -r '.user.accessToken // .user.token' ~/.railway/config.json)
# POST https://backboard.railway.com/graphql/v2  with  Authorization: Bearer $TOKEN
```

All six steps below were executed exactly this way in M1-06 and their resulting state
re-verified live on 2026-07-05; every mutation name exists in the current schema.
`$PROJECT` / `$ENV` are the IDs from the top of this doc; `$SVC` is the service ID
returned by step 1.

1. **Create the service** attached to the repo:
   ```graphql
   mutation { serviceCreate(input: {
     projectId: "$PROJECT", name: "<svc>",
     source: { repo: "SimonOsipov/invoice-os" }, branch: "main"
   }) { id } }
   ```
2. **Point it at the config file:**
   ```graphql
   mutation { serviceInstanceUpdate(serviceId: "$SVC", environmentId: "$ENV",
     input: { railwayConfigFile: "cmd/<svc>/railway.json" }) }
   ```
3. **Mirror the watch patterns onto the instance** (see §3 gotcha — never skip):
   ```graphql
   mutation { serviceInstanceUpdate(serviceId: "$SVC", environmentId: "$ENV",
     input: { watchPatterns: ["cmd/<svc>/**", "internal/<ctx>/**",
       "internal/platform/**", "internal/audit/**",
       "go.mod", "go.sum", "Dockerfile"] }) }
   ```
4. **Pin the port:**
   ```graphql
   mutation { variableUpsert(input: {
     projectId: "$PROJECT", environmentId: "$ENV", serviceId: "$SVC",
     name: "PORT", value: "8080" }) }
   ```
   Plus service-specific variables (e.g. `DATABASE_URL` = `${{Postgres.DATABASE_URL}}`)
   — one `variableUpsert` each.
5. **First deploy** from current `main`:
   ```graphql
   mutation { serviceInstanceDeployV2(serviceId: "$SVC", environmentId: "$ENV",
     commitSha: "<main HEAD sha>") }
   ```
6. **Public domain — SPAs and gateway ONLY.** Backend context services skip this step;
   that is the entire private-networking story:
   ```graphql
   mutation { serviceDomainCreate(input: {
     environmentId: "$ENV", serviceId: "$SVC", targetPort: 8080 }) { domain } }
   ```

Auto-deploy on push to `main` comes with step 1 (the deployment trigger is created with
the service; verified live: `branch: main`, `checkSuites: false`). Note `checkSuites:
false` means Railway deploys on push **without waiting for GitHub CI** — current
behavior, kept as-is; `deploymentTriggerUpdate` is the knob if we later gate deploys
on green CI.

## 6. Verification checklist — "correctly added" means all four

1. **Healthy:** deployment status `SUCCESS`; `/healthz` answers on the private network
   (for public services, `curl https://<domain>/healthz` — SPAs: `/health`).
2. **Instance watch patterns really set** (this catches the §3 gotcha):
   ```graphql
   query { environment(id: "$ENV") { serviceInstances { edges { node {
     serviceName watchPatterns } } } } }
   ```
   The new service's `watchPatterns` must be non-empty and match §3.
3. **Positive isolation probe:** push a trivial commit to `main` touching only
   `cmd/<svc>/` → only this service redeploys.
4. **Negative isolation probe:** a push touching a *different* service's paths does
   **not** redeploy this one. (Steps 3+4 are the M1-06 task-6.3 method; in practice one
   probe commit checks both directions across the fleet at once.)

---

## Appendix: static SPA variant (prior art, already live)

The three frontend SPAs predate this recipe and are its reference implementation —
same runbook, different config block. Differences:

| | Compute (this recipe) | Static SPA (live) |
|---|---|---|
| Config file | `cmd/<svc>/railway.json` | `frontend/<app>/railway.json` |
| Dockerfile | shared root `Dockerfile` + `SERVICE` arg | per-app `frontend/<app>/Dockerfile` (pnpm build → Caddy) |
| Health | `/healthz` (in the binary) | `/health` (shared root `Caddyfile`) |
| Watch patterns | §3 | `frontend/<app>/**`, `packages/design-tokens/**`, `pnpm-lock.yaml` |
| Public domain | gateway only | yes (all three) |

Live services (dev): `landing`, `app`, `ops-console` — see `frontend/*/railway.json`
and the root `Caddyfile` for the exact serving setup.
