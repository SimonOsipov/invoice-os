---
paths:
  - ".github/workflows/dev-env.yml"
  - ".github/workflows/dev-env-teardown.yml"
  - ".github/workflows/dev-env-sweeper.yml"
  - "scripts/ci/preview.sh"
---
# PR previews on Google Cloud

- `dev-env.yml` deploys each ready PR to `ascomply-preview` as `<svc>-pr-<N>` on run.app. `scripts/ci/preview.sh` does every gcloud call.
- Authenticate as `deploy-preview`. Workload Identity admits it from `pull_request` runs and from `main`. It holds no role in `ascomply-prod`.
- Keep the shared preview resources in invoice-os-infra `preview.tf`. Create and delete the per-PR resources only with `preview.sh`.
- Label every per-PR resource `preview-pr=<N>`. The sweeper finds a preview by that label.
- Recreate `pr_<N>` on every deploy with the `preview-db` job. Cloud SQL refuses `session_replication_role`, so no reset runs in place.
- Run the migrate job before GoTrue. Run it again with `preview.sh grant` after GoTrue is up: GoTrue creates `auth.users` on its first boot.
- Generate each PR's tokens and GoTrue key in `preview.sh secrets`. Never copy a production value into preview.
- Give no preview service an OpenRouter key or a Sentry DSN. The `Preview invariants` job fails on either.
- Probe the gateway at `/health` on run.app. Cloud Run's front end answers `/healthz` itself; `/healthz/fleet` passes through.
- Print the library and blog addresses with their base paths from `preview.sh urls`. A preview has no load balancer.
- Send `X-Gateway-Token` to `/healthz/fleet`. `preview.sh token <N>` prints the PR's token.
- Read cloud credentials only after `pnpm install` in a job that installs dependencies.
- Re-run the whole workflow to retry an `E2E gate`, never "Re-run failed jobs". The database is recreated only when the `deploy` job runs.
- Teardown deletes the PR's services, migrate job, database, bucket and secrets on close. The nightly sweep deletes a preview older than 24 h whose PR reads closed.
- Keep a preview fleet at 30 database connections or fewer at peak (`preview.sh budget`) and open at most 3 previews: 3 x 30 fits the 95 usable slots of the `preview` instance's `max_connections` 100 (100 - 3 superuser-reserved - 2 `cloudsqladmin` = 95). Nothing enforces the cap; past it, any open preview's services can fail with 53300.
