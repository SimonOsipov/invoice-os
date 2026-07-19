# Add-a-Service Recipe (Railway)

**Audience:** whoever stamps the next backend service (M2-12: gateway + seven context
services; M7-01: opsconsole). Follow it verbatim — every value below is either fixed by
convention or derived mechanically from the service name. If you find yourself making a
judgment call, the recipe is broken: fix the recipe, then the service.

**Railway target — the base environment a new service is created in.** Since M4-23 each PR
also gets its own ephemeral environment, forked from `development` by `dev-env.yml`'s
`prepare-env` job, but a new service is never created *in* one of those directly: it is
created once, here, on `development`, and every subsequent PR fork inherits it. Measured
(M4-23-04): a fork inherits all 12 service instances with `watchPatterns: []`, the service
domains (auto-renamed) and the Postgres TCP proxy. It does **not** inherit a Postgres
*deployment* (started explicitly), a *volume*, or **sealed variables**.
`$ENV` throughout this recipe always means `development`'s id below:

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

> **Realized in M2-12 — how `SERVICE` is supplied (deviation from the original
> plan).** Railway config-as-code has **no `build.buildArgs`** field
> ([config-as-code/reference](https://docs.railway.com/config-as-code/reference)
> lists only `builder`, `dockerfilePath`, `buildCommand`, `watchPatterns`,
> `railpackVersion`), so the `buildArgs` block first specified for `railway.json`
> was silently ignored and `SERVICE` reached the build empty. The working mechanism
> is Railway's documented one: declare `ARG SERVICE` in the Dockerfile (done) and
> set `SERVICE=<svc>` as a **service variable** — Railway injects declared service
> variables as build args (§4). One consequence: the shared Dockerfile uses **no**
> BuildKit `--mount=type=cache`, because Railway requires each cache-mount id to
> embed the *building service's own id* (`id=s/<service-id>-<target>`) and bans env
> vars in the id — unexpressable in one Dockerfile shared by every service. Plain
> Docker layer caching is kept; the build stays portable (Railway + local).

## 2. The config block — `cmd/<svc>/railway.json`

One file per service, committed next to the binary's `main.go`. Copy verbatim,
substitute `<svc>`/`<ctx>`:

```json
{
  "$schema": "https://railway.com/railway.schema.json",
  "build": {
    "builder": "DOCKERFILE",
    "dockerfilePath": "Dockerfile"
  },
  "deploy": {
    "healthcheckPath": "/healthz",
    "restartPolicyType": "ON_FAILURE"
  }
}
```

No `build.watchPatterns` field: Railway ignores it entirely regardless of value (§3), and
per-PR ephemeral environments (M4-23) require every service to **always** rebuild — the
opposite of what a populated watch-path list would suggest. Leave it out; do not add one
"for documentation" — see §3.

Fixed by convention, not per-service choice:

- **`/healthz`** is the health endpoint for every backend service (the SPAs use
  `/health` via Caddy; that difference is deliberate and stays).
- **`ON_FAILURE`** restart policy everywhere.
- **Root Directory stays `/`** (unset) on the Railway service — the build needs the
  whole repo (root `go.mod`, shared `internal/`).
- **No `buildArgs`.** `SERVICE=<svc>` is set as a **service variable** instead (§4,
  and the §1 M2-12 note) — Railway config-as-code has no `buildArgs` field.

## 3. Watch-path convention: always empty (M3-16, mandatory since M4-23)

**Every service's Watch Paths must be empty. Never populate them.** This inverts the
recipe's original M1-06/M2-12 convention (a per-service path list, mirroring what changed
on `main`) after M3-16's cold-fleet-deploy incident, and per-PR ephemeral environments
(designed in M4-21, actually created from M4-23) make emptiness load-bearing rather than a
nice-to-have:

- **Why empty is now required.** Every deploy target is either a *fresh ephemeral PR
  environment* (`dev-env.yml`'s `prepare-env` job forks the whole service graph from
  `development` via `environmentCreate`) or a `workflow_dispatch` run against
  `development` — either way, `dev-env.yml`
  needs **every** service to actually build and deploy on `railway up`, regardless of
  whether that particular run's diff touches the service's own paths. A non-empty Watch
  Paths list makes `railway up` **skip** (no deployment created, `no changes detected in
  watch paths, build will skip`) whenever the diff misses it — which, after a torn-down or
  freshly-forked service, means that service never comes up at all, failing `health-gate`
  or `fleet-gate` before the E2E suites ever run (the M3-16 incident, `docs/deploy-model.md`
  "Cold-fleet recovery"). Emptying it makes `railway up` fall back to its documented
  default: always upload and build the working tree, for every service, on every run
  (Approach 3: always-rebuild).
- **`railway.json`'s `build.watchPatterns` field is inert regardless of value** — Railway
  never reads it (confirmed live; see §2) — so it is not part of this invariant at all; do
  not add it to a new service's config file.
- **The field that matters is the *service instance*'s `watchPatterns`**, set via
  `serviceInstanceUpdate` (runbook step 3) — **always to `[]`**, never to a path list.
  `railway.json` is applied when a **build starts**, but watch patterns are evaluated **at
  trigger time**, from the instance settings, *before* any config file is read — a
  freshly created service already defaults to instance `watchPatterns: []` (confirmed live,
  2026-07-05: instance fields also report `builder: RAILPACK` and `healthcheckPath: null`
  while deployments demonstrably build via Dockerfile and answer the healthcheck — the
  instance record and the effective config genuinely diverge on several fields, not just
  this one). **After creating the service, explicitly set (or confirm) instance
  `watchPatterns: []`** — do not leave it to chance, and never "helpfully" populate it with
  a path list.
- **Runtime-enforced, not just documented (M4-21-09):** `dev-env.yml`'s `prepare-env` job
  queries every non-Postgres service instance's Watch Paths in the target environment on
  every run and **fails the run, naming the offender(s)**, if any are non-empty (Decision
  `[watch-paths-asserted]`) — a service accidentally recreated with a populated Watch Paths
  list, or a config drift, can no longer reach the deploy steps silently.

## 4. `.env.example` and env-var conventions

### `.env.example`

> **Not adopted for backend services (M2-12 decision).** The seven context services and
> the gateway ship **no** `cmd/<svc>/.env.example`. Each service's variables live in one
> place — its Railway service variable set (see the M2-12 wiring below and the task
> record) — and the binaries fail fast on a missing required var, so a committed example
> file bought duplication, not safety. The subsection below is retained for the SPA prior
> art and any future service that opts back in; it is **not** a requirement for the Go
> binaries.

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
- **`SERVICE=<svc>` everywhere, set explicitly** (runbook step 4). This is the build
  arg that selects which binary the shared Dockerfile compiles (`./cmd/${SERVICE}`) —
  a **build-time** variable Railway injects into the Dockerfile's `ARG SERVICE`, not a
  runtime one (the binary reads its name from code, not env). Required: an unset/empty
  `SERVICE` fails the build. See the §1 M2-12 note for why it is a variable, not
  `railway.json` `buildArgs`.
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
   > ⚠️ **This step may attach a deployment trigger, which now reds every open PR.** It did
   > when verified on 2026-07-05 (`branch: main`, `checkSuites: false`); whether it still
   > does is unverified. `railway-invariants.yml` fails **any** PR while **any** deployment
   > trigger exists in **any** environment. **Query the service's deployment triggers now
   > and `deploymentTriggerDelete` any that exist** — before continuing, not at the end of
   > the recipe. Full rationale under step 6.
2. **Point it at the config file:**
   ```graphql
   mutation { serviceInstanceUpdate(serviceId: "$SVC", environmentId: "$ENV",
     input: { railwayConfigFile: "cmd/<svc>/railway.json" }) }
   ```
3. **Explicitly set the instance Watch Paths to empty** (see §3 — this is the M3-16
   invariant per-PR environments depend on; never populate this with a path list):
   ```graphql
   mutation { serviceInstanceUpdate(serviceId: "$SVC", environmentId: "$ENV",
     input: { watchPatterns: [] }) }
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

> ⚠️ **Deployment triggers are now forbidden — remove one before opening a PR.**
> `railway-invariants.yml` (M4-23-02) fails **any** PR while **any** deployment trigger
> exists in **any** environment: Railway must never deploy this project outside CI's
> ordering. **Mandatory step: after creating the service, query its deployment triggers and
> delete any that exist** (`deploymentTriggerDelete`), then confirm the invariants workflow
> is green.
>
> This recipe was verified 2026-07-05, when `serviceCreate` did attach a trigger
> (`branch: main`, `checkSuites: false`). M4-23 measured `deploymentTriggers = 0`
> project-wide today. Whether `serviceCreate` **still** creates one is **unverified** —
> check, do not assume either way.

## 6. Verification checklist — "correctly added" means all three

1. **Healthy:** deployment status `SUCCESS`; `/healthz` answers on the private network
   (for public services, `curl https://<domain>/healthz` — SPAs: `/health`).
2. **Instance Watch Paths are really empty** (this catches the §3 invariant — the same
   query `dev-env.yml`'s `prepare-env` job runs on every run, M4-21-09):
   ```graphql
   query { environment(id: "$ENV") { serviceInstances { edges { node {
     serviceName watchPatterns } } } } }
   ```
   The new service's `watchPatterns` **must be empty** (`[]`). Non-empty here is the M3-16
   failure mode, not success — see §3.
3. **Always-rebuild proof:** trigger a `dev-env.yml` run (any PR, or `workflow_dispatch`)
   whose diff does **not** touch this service's own code, and confirm it still builds and
   redeploys rather than being skipped. (Supersedes the old M1-06 task-6.3
   positive/negative *isolation* probes, which proved the opposite property — selective
   rebuild-on-touch — now deliberately retired.)

---

## Appendix: static SPA variant (prior art, already live)

The three frontend SPAs predate this recipe and are its reference implementation —
same runbook, different config block. Differences:

| | Compute (this recipe) | Static SPA (live) |
|---|---|---|
| Config file | `cmd/<svc>/railway.json` | `frontend/<app>/railway.json` |
| Dockerfile | shared root `Dockerfile` + `SERVICE` arg | per-app `frontend/<app>/Dockerfile` (pnpm build → Caddy) |
| Health | `/healthz` (in the binary) | `/health` (shared root `Caddyfile`) |
| Watch patterns | empty (§3) | empty (§3) — same invariant, no per-service-type difference |
| Public domain | gateway only | yes (all three) |

Live services (dev): `landing`, `app`, `ops-console` — see `frontend/*/railway.json`
and the root `Caddyfile` for the exact serving setup.
