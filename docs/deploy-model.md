# Dev Deploy Model — unified fleet deploy + verify (M2-14)

How the full fleet — the gateway, the 7 context services, and the three frontend SPAs
(`landing`, `app`, `ops-console`) — is deployed to the Railway **development** environment.
Adopted in M2-14; supersedes the M1-08 split model (separate `preview.yml` for the SPAs and
`preview-backend.yml` for the backends, each deploying only half the fleet).

## The model

- **Open / update a non-draft PR** → CI deploys and verifies the **whole fleet** together,
  coherently, from the PR's code, then leaves it up. (`.github/workflows/dev-env.yml`)
- **Close / merge a PR** → CI scales all 11 services to zero.
  (`.github/workflows/dev-env-cleanup.yml`)
- **Steady state of `main` with no open PR** → dev runs zero compute. The stable dev URLs
  come up only while a PR is open and go back to zero on close. Postgres is excluded — it
  stays always-on.
- **Production** → nothing. The `production` environment stays dormant.

The dev environment and its URLs are never deleted — only the compute (the latest
deployment) is added on `railway up` and removed on `railway down`, so the URLs stay stable
across PRs.

```
PR opened ──> dev-env.yml:
                gateway ──> gate on /healthz (schema migrated)
                ──> 7 context services + 3 SPAs (app is gateway-wired)
                ──> reset + seed dev DB (data-only, superuser)
                ──> verify: smoke (landing + ops-console) + topology (app login,
                    cross-tenant isolation, fleet /healthz/fleet gate)
              ──> env stays up
PR closed  ──> dev-env-cleanup.yml ──> railway down (11 services, → 0 compute; Postgres always-on)
```

### Why one unified env, not a split

Dev is single-branch, single-agent: only one PR is ever meaningfully "the" dev env at a
time, so there is no concurrency risk to hedge against with separate frontend/backend
deploys. A half-deployed dev env — SPAs live but backends stale, or vice versa — has no
value: the app can't do a real sign-in round trip without a gateway-wired backend, and the
topology exit criterion needs the whole fleet up together. The old split (deploy only the
SPAs on a frontend-only PR, only the backends on a backend-only PR) is retired; every ready
PR now deploys and verifies the whole fleet, regardless of which files it touches. This also
supersedes the roadmap's M3-13 ("deploy-then-E2E gate") — that gate is now built into
`dev-env.yml` itself.

## Auth

Both workflows authenticate with the GitHub secret **`RAILWAY_API_DEV_TOKEN`**,
a Railway **project token** scoped to the dev environment, consumed as
`RAILWAY_TOKEN`. A project token pins the project + environment, so
`railway up`/`railway down --service <svc>` need no `railway link` and no
project/workspace IDs. (See [Railway docs — CLI login](https://docs.railway.com/cli/login):
`RAILWAY_TOKEN` is project-scoped; `RAILWAY_API_TOKEN` is account-scoped.)

## One-time cutover: disable auto-deploy from `main`

M1-06 attached each service to `SimonOsipov/invoice-os` @ `main` with auto-deploy
on push (the deployment trigger created by `serviceCreate` — see
[add-a-service.md](./add-a-service.md) §5). That trigger **must be disabled** for
this model to hold: otherwise a merge pushes to `main`, Railway auto-deploys the
service, and the `railway down` teardown is immediately undone.

Disabling auto-deploy does **not** affect `railway up` / `railway down` — those
upload/remove deployments directly and keep working. Only push-triggered
auto-deploy stops.

**Run this once, after `dev-env.yml` and `dev-env-cleanup.yml` are on `main`**
(so there is never a window with neither auto-deploy nor the unified workflow):

For each of the 11 services (gateway, the 7 context services, and `landing`, `app`,
`ops-console`):

1. Railway dashboard → the service → **Settings**.
2. Under the GitHub trigger, click **Disable** ("stop deploying automatically on
   new commits"). See [Railway docs — Controlling GitHub Autodeploys](https://docs.railway.com/deployments/github-autodeploys#disable-automatic-deployments).

Verify: push a trivial no-op commit to `main` and confirm no service redeploys
(Deployments tab shows nothing new). Open a throwaway PR and confirm `dev-env.yml`
deploys + verifies the fleet, then close it and confirm `dev-env-cleanup.yml` scales all 11
services to zero.

### Rollback

To revert to the M1-06 always-on model: re-enable auto-deploy per service
(Settings → **Enable**), and disable/remove `dev-env.yml` + `dev-env-cleanup.yml`.

## Cold-fleet recovery (M3-16)

**Root cause.** Each Railway service has a *service-level* **Watch Paths**
setting (a monorepo build filter, configured in the dashboard) that suppresses
`railway up` whenever the uploaded snapshot doesn't touch that service's
watched paths — printing `no changes detected in watch paths, build will
skip` and creating no deployment. After `dev-env-cleanup.yml`'s `railway down`
removes a service's deployment on PR close, the *next* PR's `dev-env.yml`
`railway up --ci --service <svc>` for any service whose watch paths the PR
doesn't touch (e.g. a `.github/workflows/**`-only PR) skips instead of
building — the torn-down service never comes back, and since `dev-env.yml`
gates on the gateway's `/healthz` before deploying the rest of the fleet, one
such skip fails the whole run. This is distinct from `railway.json`'s
`build.watchPatterns` field, which Railway silently **ignores** — it never
appears in a deployment's property mapping regardless of value. That's why
M2-14's removal of `watchPatterns` from `railway.json` (`bae6c0f`) never
actually fixed this: the field it edited was never wired to anything.

**The fix (invariant, not a workflow change).** Service-level Watch Paths were
cleared to empty on all 11 non-Postgres services (gateway, the 7 context
services, and `landing`/`app`/`ops-console`) via the Railway API, out-of-band,
one time. With Watch Paths empty, `railway up` reverts to its documented
default — it always uploads and builds the working tree, for every service,
on every run (Approach 3: always-rebuild). This must hold as an **invariant**:
if a service is ever deleted and recreated, its Watch Paths must be
re-cleared, or the skip failure mode returns. Postgres is excluded — it was
never in the deploy fleet.

This empty-Watch-Paths invariant is a deliberately-documented one-time,
human-applied prerequisite — it is Railway-side dashboard config, not codified
anywhere in this repo (per the M3-16 decision above); an automated CI
invariant-check (e.g. a read-only GraphQL query asserting all 11 services
report empty watch paths) is possible FUTURE hardening, not implemented here.

**Teardown is unchanged.** `dev-env-cleanup.yml` still runs `railway down`
(removes the deployment, not a scale-to-0 pause) — see the file's header
comment. It is now recoverable **only because** of the empty-watch-paths
invariant above: the next PR's `dev-env.yml` rebuilds every service from
nothing rather than trying (and failing) to skip to a deployment that no
longer exists.

See the M3-16 story (`User Stories/M3/M3-16 Dev-Env Cold-Fleet Deploy Fix.md`)
for the live experiment log that falsified the alternative approaches
(scale-to-0 teardown, diff-driven redeploys).

## Related

- [add-a-service.md](./add-a-service.md) — how each service was provisioned (the
  deployment trigger this cutover disables is created in §5).
- [topology-e2e.md](./topology-e2e.md) — what the post-deploy verification asserts and why.
- `e2e/README.md` — the smoke + topology suites `dev-env.yml` runs.
