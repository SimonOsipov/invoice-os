# Dev Deploy Model — per-PR ephemeral environments (M2-14, reworked M4-21)

How the full fleet — the gateway, the 7 context services, and the three frontend SPAs
(`landing`, `app`, `ops-console`) — is deployed to Railway. Adopted in M2-14 (one unified
fleet deploy, superseding the M1-08 split model); reworked in M4-21 so **each PR deploys to
its own ephemeral Railway environment** instead of every PR sharing one `development`
environment.

## The model

- **Open / update a non-draft PR** → CI deploys and verifies the **whole fleet** together,
  coherently, from the PR's code, into a **fresh ephemeral Railway environment forked from
  `development`** — never into `development` itself. (`.github/workflows/dev-env.yml`)
  > **Correction (M4-23).** This previously said the fork happens "via Railway's own PR
  > Environments feature (task-131)". That was **false**. Railway is not subscribed to
  > this repo's PR events, so its PR Environments feature never created anything here —
  > see [Railway PR Environments are OFF](#railway-pr-environments-are-off) below. M4-23
  > replaces it with self-provisioning from CI.
- **Close a PR (merged or abandoned)** → **nothing is deprovisioned.** There is no
  automatic teardown, and there is no repo-side teardown workflow
  (`dev-env-cleanup.yml` was **deleted**, M4-21-11 — see Decision
  `[cleanup-workflow-deleted]`; tearing down `development` on every PR close would
  contradict Decision `[dev-env-status]` below).
  > **Corrected (M4-23).** This previously claimed Railway automatically deprovisions
  > the PR's environment, Postgres included, on close. That was Railway's *documented
  > PR-Environments behavior* — a feature that is OFF and never applied to this project
  > (see [Railway PR Environments are OFF](#railway-pr-environments-are-off)). It was
  > never observed happening here: task-131 was supposed to confirm it by closing a
  > throwaway PR and never did. Environments therefore accumulate until something removes
  > them (cost + orphaned Postgres instances). **A teardown mechanism is still to be
  > built — M4-23-06.** It will not be a reinstated shared-env `dev-env-cleanup.yml`
  > sweep (per Decision `[cleanup-workflow-deleted]`, per-PR environments have no shared
  > target to sweep).
- **`workflow_dispatch`** → targets the **persistent `development` environment** directly
  (never an ephemeral PR environment) — the same fleet-deploy + verify flow, plus the
  reset-seed step (M4-21-06), still serialized against itself (`dev-preview-development`-
  style concurrency, keyed by `github.ref` for dispatch runs).
- **`development` itself** is a stable, always-up base + demo environment (Decision
  `[dev-env-status]`) — it is the fork base every PR environment is created from, and it is
  what `make demo-reset` / live demo calls point at. It is **not** torn down by any
  automated workflow.
- **Production** → nothing. The `production` environment stays dormant.

Each PR's ephemeral environment and its four public URLs (gateway, app, landing,
ops-console) are **discovered fresh at deploy time** — a Railway-generated domain's suffix
is opaque/random and cannot be constructed (F7) — never hardcoded, and never assumed stable
across PRs the way `development`'s own four URLs (still constant, still hardcoded as
`RAILWAY_SVC_*_ID` **service ids**, never as domain strings) are.

```
PR opened ──> dev-env.yml:
                resolve-env: poll for this PR's ephemeral environment (tools/prenv's
                             pure Match, M4-21-08) ──> assert Watch Paths empty (M3-16
                             invariant, now runtime-asserted) ──> discover the 4 URLs
                gateway ──> gate on /healthz (schema migrated + seeded at boot, M4-21-04)
                ──> 7 context services + 3 SPAs (app is gateway-wired)
                ──> verify: smoke (landing + ops-console) + api + topology (app login,
                    cross-tenant isolation, fleet /healthz/fleet gate) + demo
              ──> PR stays open: environment stays up
PR closed  ──> Railway auto-deprovisions the PR's ephemeral environment (no repo workflow)

workflow_dispatch ──> targets `development` directly (persistent, never deprovisioned)
                   ──> same deploy + verify flow, PLUS reset-seed (data-only, superuser)
```

### Why per-PR environments, not one shared env

The old M2-14 model reasoned "dev is single-branch, single-agent: only one PR is ever
meaningfully 'the' dev env at a time" — true when stories ran one at a time, but it meant
every PR queued behind `dev-env.yml`'s single shared concurrency lock, serializing all CI
deploys. M4-21 removes that constraint: each PR forks its own environment, so
`dev-env.yml`'s concurrency group is now keyed **per-PR**
(`dev-preview-${{ github.event.pull_request.number || github.ref }}`) — two different PRs'
groups never collide, so their deploys run **fully in parallel**. A `workflow_dispatch` run
has no PR number and falls back to `github.ref` (constant across dispatch runs), so
dispatch runs against `development` still serialize against each other — that matters more
now, since dispatch is the only path left that resets/seeds shared state.

A half-deployed environment (SPAs without backends, or backends without an app) still has
no value: every ready PR (re)deploys its own environment whole, exactly as before.

## Railway PR Environments are OFF

Railway's built-in **PR Environments** feature is **disabled** on this project
(`prDeploys = false`, `botPrEnvironments = false`). It was turned off by hand in the
Railway dashboard on **2026-07-18**, so no committed command ever observed the previous
`true` state — there is no CI-captured "before".

**Why it is off.** It never worked here and never could. Railway is not subscribed to
this repo's pull-request events: the fleet is deployed by `railway up` (a CLI push from
CI), and the project has **zero deployment triggers**, so no service is attached to
GitHub. With nothing subscribed, `prDeploys = true` produced exactly nothing. M4-23
established this: the flag was on, the base environment was set correctly, and the
feature was completely **inert**.

**First CI observation (2026-07-18), against the live Railway API:**

```
prDeploys=false botPrEnvironments=false focusedPrEnvironments=false baseEnvironmentId=null deploymentTriggers=0
```

Note `baseEnvironmentId=null` and `deploymentTriggers=0`. The project has **no fork base
configured at all** and **nothing subscribed to GitHub** — so even with `prDeploys=true`
the feature had neither a base to fork from nor an event to fire on. This is the direct
measurement behind the claim above; `dev-env.yml`'s old header comment asserting
"PR Environments ON, base = `development`" was wrong on both counts.

**`prDeploys` is not the load-bearing condition — `deploymentTriggers == 0` is.**
Asserting only that a suggestively-named flag reads `false` is what produced M4-23 in the
first place: a green check that proves nothing. A *deployment trigger* is the thing with
causal power — if one ever appears, Railway can deploy a service on its own, outside CI,
in its own order, breaking the gateway-first sequencing `dev-env.yml` depends on,
**regardless of any toggle**.

So CI asserts both, and fails loudly rather than repairing:

- `.github/workflows/railway-invariants.yml` — runs on **every** `pull_request`, with no
  draft gate and no `paths:` filter (unlike `dev-env.yml`, whose jobs are all draft-gated),
  so drift is caught from the first push of any branch.
- `scripts/ci/railway-env.sh assert-project-settings` — reads the project back over
  Railway's GraphQL API and exits non-zero unless it positively observes
  `prDeploys=false`, `botPrEnvironments=false`, and zero `deploymentTriggers` across every
  environment. A GraphQL error (including "Not Authorized") is never treated as "already
  off". On success it prints one greppable line:
  `Railway PR Environments OFF: prDeploys=false botPrEnvironments=false focusedPrEnvironments=… baseEnvironmentId=… deploymentTriggers=0`

It uses the non-deprecated nested `project.environments[].deploymentTriggers`;
`Project.deploymentTriggers` is `@deprecated` and would become a false positive blocking
every PR if Railway removes it.

**How to re-check, and how to repair:**

```bash
gh workflow run railway-invariants.yml                  # re-assert on demand
gh workflow run railway-invariants.yml -f disable=true  # re-disable, then re-assert
```

The repair path is manual-only — never on the `pull_request` path — and after mutating it
re-verifies with an **independent** read-back request rather than echoing
`projectUpdate`'s own response back at itself. This cannot be verified from a laptop:
`RAILWAY_API_TOKEN` (account-scoped) lives only as a repo secret, so CI is the only place
this is ever proven.

## Auth

Two Railway tokens, chosen by trigger:

- **`pull_request` runs** use the **account-scoped** secret `RAILWAY_API_TOKEN` (task-131,
  human-provisioned). A **project token cannot reach an ephemeral PR environment** — it is
  pinned to one environment (F6) — so every PR-triggered `railway`/GraphQL call passes
  `--environment`/`--project` (or the equivalent GraphQL argument) explicitly, since an
  account token has no implicit project/environment scope.
- **`workflow_dispatch` runs** keep using `RAILWAY_API_DEV_TOKEN`, a **project token**
  scoped to `development`, consumed as `RAILWAY_TOKEN` — unchanged from before M4-21. A
  project token pins the project + environment, so `railway up`/`railway down` need no
  `railway link` and no project/workspace IDs for this path.

(See [Railway docs — CLI login](https://docs.railway.com/cli/login): `RAILWAY_TOKEN` is
project-scoped; `RAILWAY_API_TOKEN` is account-scoped.)

## One-time cutover: disable auto-deploy from `main`

M1-06 attached each service to `SimonOsipov/invoice-os` @ `main` with auto-deploy
on push (the deployment trigger created by `serviceCreate` — see
[add-a-service.md](./add-a-service.md) §5). That trigger **must be disabled** on
`development`'s services for this model to hold: otherwise a merge pushes to `main`,
Railway auto-deploys the service on `development` outside CI's control, alongside whatever
`dev-env.yml` is doing.

Disabling auto-deploy does **not** affect `railway up` — that uploads a deployment
directly and keeps working. Only push-triggered auto-deploy stops.

**Run this once, after `dev-env.yml` is on `main`:**

For each of the 11 services (gateway, the 7 context services, and `landing`, `app`,
`ops-console`) **on the `development` environment**:

1. Railway dashboard → the service → **Settings**.
2. Under the GitHub trigger, click **Disable** ("stop deploying automatically on
   new commits"). See [Railway docs — Controlling GitHub Autodeploys](https://docs.railway.com/deployments/github-autodeploys#disable-automatic-deployments).

Verify: push a trivial no-op commit to `main` and confirm no service redeploys
on `development` (Deployments tab shows nothing new). Open a throwaway PR and confirm
`dev-env.yml` deploys + verifies its own ephemeral environment, then close it and confirm
Railway deprovisions that environment automatically.

### Rollback

To revert to the M1-06 always-on single-environment model: re-enable auto-deploy per
service on `development` (Settings → **Enable**), and disable/remove `dev-env.yml`.

## Cold-fleet recovery (M3-16)

**Root cause.** Each Railway service has a *service-level* **Watch Paths**
setting (a monorepo build filter, configured in the dashboard) that suppresses
`railway up` whenever the uploaded snapshot doesn't touch that service's
watched paths — printing `no changes detected in watch paths, build will
skip` and creating no deployment. Since every environment (a fresh PR fork, or a
`workflow_dispatch` run against `development`) is now potentially a cold, from-scratch
11-service build, a service whose Watch Paths aren't empty would silently skip and never
come up — and since `dev-env.yml` gates on the gateway's `/healthz` before deploying the
rest of the fleet, one such skip fails the whole run. This is distinct from
`railway.json`'s `build.watchPatterns` field, which Railway silently **ignores** — it never
appears in a deployment's property mapping regardless of value. That's why
M2-14's removal of `watchPatterns` from `railway.json` (`bae6c0f`) never
actually fixed this: the field it edited was never wired to anything.

**The fix (invariant, now runtime-asserted, not just documented).** Service-level Watch
Paths were cleared to empty on all 11 non-Postgres services (gateway, the 7 context
services, and `landing`/`app`/`ops-console`) via the Railway API, out-of-band, one time.
With Watch Paths empty, `railway up` reverts to its documented default — it always uploads
and builds the working tree, for every service, on every run (Approach 3: always-rebuild).
Per-PR environments promote this from a documented human invariant to a **mandatory
runtime assertion**: `dev-env.yml`'s `resolve-env` job (M4-21-09) queries every non-Postgres
service instance's Watch Paths in the target environment and **fails the run, naming the
offending service(s)**, if any are non-empty (Decision `[watch-paths-asserted]`) — a silent
regression can no longer reach the deploy steps undetected. If a service is ever deleted
and recreated, its Watch Paths must still be re-cleared. Postgres is excluded — it was
never in the deploy fleet.

This empty-Watch-Paths invariant remains Railway-side dashboard config, not codified
directly in `railway.json` (per the M3-16 decision above) — only *asserted* by CI now, not
*set* by it.

## Related

- [add-a-service.md](./add-a-service.md) — how each service was provisioned (the
  deployment trigger this cutover disables is created in §5); its Watch-path convention
  now matches the always-empty invariant above.
- [topology-e2e.md](./topology-e2e.md) — what the post-deploy verification asserts and why.
- `e2e/README.md` — the smoke + topology suites `dev-env.yml` runs.
