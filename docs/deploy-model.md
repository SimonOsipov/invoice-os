# Dev Deploy Model — PR preview + scale-to-zero (M1-08)

How the three frontend SPAs (`landing`, `app`, `ops-console`) are deployed to the
Railway **development** environment. Adopted in M1-08; supersedes the M1-06
"auto-deploy every push to `main`" model.

## The model

- **Open / update a non-draft PR** → CI deploys the three SPAs to the shared dev
  environment from the PR's code, then runs the Playwright smoke suite against
  the dev URLs. (`.github/workflows/preview.yml`)
- **Close / merge a PR** → CI scales the three dev services to zero.
  (`.github/workflows/preview-cleanup.yml`)
- **Steady state of `main` with no open PR** → dev runs zero SPA compute. The
  stable dev URLs come up only while a PR is open and go back to zero on close.
- **Production** → nothing. The `production` environment stays dormant.

The dev environment and its URLs are never deleted — only the compute (the latest
deployment) is added on `railway up` and removed on `railway down`, so the URLs
stay stable across PRs.

```
PR opened ──> preview.yml ──> railway up  (3 services) ──> smoke (Playwright)
PR closed ──> preview-cleanup.yml ──> railway down (3 services, → 0 compute)
```

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

**Run this once, after `preview.yml` and `preview-cleanup.yml` are on `main`**
(so there is never a window with neither auto-deploy nor the preview workflows):

For each of the three services (`landing`, `app`, `ops-console`):

1. Railway dashboard → the service → **Settings**.
2. Under the GitHub trigger, click **Disable** ("stop deploying automatically on
   new commits"). See [Railway docs — Controlling GitHub Autodeploys](https://docs.railway.com/deployments/github-autodeploys#disable-automatic-deployments).

Verify: push a trivial no-op commit to `main` and confirm no service redeploys
(Deployments tab shows nothing new). Open a throwaway PR and confirm `preview.yml`
deploys + smokes, then close it and confirm `preview-cleanup.yml` scales all three
to zero.

### Rollback

To revert to the M1-06 always-on model: re-enable auto-deploy per service
(Settings → **Enable**), and disable/remove `preview.yml` + `preview-cleanup.yml`.

## Related

- [add-a-service.md](./add-a-service.md) — how each service was provisioned (the
  deployment trigger this cutover disables is created in §5).
- `e2e/README.md` — the smoke suite `preview.yml` runs.
