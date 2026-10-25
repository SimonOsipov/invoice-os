---
paths:
  - ".github/workflows/deploy-prod.yml"
  - ".github/workflows/rollback-prod.yml"
  - "scripts/ci/gcp-traffic.sh"
  - "cmd/deploycheck/**"
---
# Production deploys on Google Cloud

- `deploy-prod.yml` deploys production on a `main` push and on `workflow_dispatch`. Both wait for green `CI` on the commit.
- Deploy only a commit on `main`. Workload Identity admits `deploy-prod` only from a run on `refs/heads/main`.
- Give no deploy job a GitHub `environment:`. It changes the OIDC subject, and Workload Identity refuses it.
- Build each image once per commit, tagged with the full sha, in `africa-south1-docker.pkg.dev/ascomply-preview/images`.
- Run the `migrate` job before any revision starts. Keep migrations expand-only: old and new revisions share the database.
- Start every new revision with `--no-traffic` and the tag `c-<sha12>`.
- Check the tagged revisions with the `deploy-check` job (`cmd/deploycheck`). Tag URLs answer only inside the production network.
- Read the library and the blog in the deploy's site check at `www.ascomply.com/library` and `www.ascomply.com/blog`.
- Move traffic with `gcp-traffic.sh promote`. It sends 100 % to latest and tags the previous revision `rollback`.
- After the move, poll `/healthz/fleet` through the load balancer. On failure, `gcp-traffic.sh restore` sends traffic back.
- Roll back with `rollback-prod.yml`. It moves every service to its `rollback` revision together.
- Keep the deploy read-only after migrate. It writes no data.
- Probe a service at `/health` on a run.app host. Cloud Run's front end answers `/healthz` itself there, and the container never sees it.
