---
paths:
  - ".github/workflows/deploy-prod.yml"
  - ".github/workflows/rollback-prod.yml"
  - "scripts/ci/gcp-traffic.sh"
  - "scripts/ci/prod-image.sh"
  - "cmd/deploycheck/**"
---
# Production deploys on Google Cloud

- `deploy-prod.yml` deploys production on a `main` push and on `workflow_dispatch`. Both wait for green `CI` on the commit.
- Deploy only a commit on `main`. Workload Identity admits `deploy-prod` only from a run on `refs/heads/main`.
- Give no deploy job a GitHub `environment:`. It changes the OIDC subject, and Workload Identity refuses it.
- Every `deploy-prod` run builds each image and pushes it untagged, by digest, to `africa-south1-docker.pkg.dev/ascomply-preview/images`. Only `deploy-prod` writes it, and its tags are immutable (invoice-os-infra `registry.tf`, `deployers.tf`).
- Deploy, migrate and deploy-check use the digest that the run's own push recorded (`prod-image.sh push`, `ref`), never a tag. A re-run or a dispatch rebuilds. Run `prod-image.sh` from the workflow's commit, never from the commit being deployed.
- `images` keeps the 50 newest versions of each package and deletes older untagged ones. Before each push, `prod-image.sh pin` tags `keep-<digest>` on a serving, rollback or job image at rank 40 or more (ten below the 50); it never stops a build, it annotates. Change `keep_count` and `KEEP_VERSIONS` together. The nightly reads auth's commit from the `commit` label each deploy sets.
- Run the `migrate` job before any revision starts. Keep migrations expand-only: old and new revisions share the database.
- Start every new revision with `--no-traffic` and the tag `c-<sha12>`.
- Check the tagged revisions with the `deploy-check` job (`cmd/deploycheck`). Tag URLs answer only inside the production network.
- Read the library, the blog and the check in the deploy's site check at `www.ascomply.com/library`, `www.ascomply.com/blog` and `www.ascomply.com/check`.
- Move traffic with `gcp-traffic.sh promote`. It sends 100 % to latest and tags the previous revision `rollback`.
- A tagged revision with a revision-level minimum keeps running. The worker's rule is in `add-service.md`.
- After the move, poll `/healthz/fleet` through the load balancer. On failure, `gcp-traffic.sh restore` sends traffic back.
- Roll back with `rollback-prod.yml`. It moves every service to its `rollback` revision together.
- Keep the deploy read-only after migrate. It writes no data.
- Probe a service at `/health` on a run.app host. Cloud Run's front end answers `/healthz` itself there, and the container never sees it.
