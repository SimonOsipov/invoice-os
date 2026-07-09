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

## Related

- [add-a-service.md](./add-a-service.md) — how each service was provisioned (the
  deployment trigger this cutover disables is created in §5).
- [topology-e2e.md](./topology-e2e.md) — what the post-deploy verification asserts and why.
- `e2e/README.md` — the smoke + topology suites `dev-env.yml` runs.
