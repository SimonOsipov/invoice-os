---
paths:
  - ".github/workflows/dev-env.yml"
  - ".github/workflows/dev-env-teardown.yml"
  - ".github/workflows/dev-env-sweeper.yml"
  - ".github/workflows/nightly.yml"
  - ".github/workflows/deploy-prod.yml"
  - ".github/workflows/rollback-prod.yml"
  - "scripts/ci/gcp-traffic.sh"
  - "scripts/ci/prod-image.sh"
  - "cmd/deploycheck/**"
---
# Production deploys on Google Cloud

- `deploy-prod.yml` deploys production on a `main` push and on `workflow_dispatch`. Both wait for green `CI` on the commit.
- Deploy only a commit on `main`. Workload Identity admits `deploy-prod` only from the files below on `refs/heads/main`.
- Give no deploy job a GitHub `environment:` until SECAUD-16 decides. An `environment:` changes the OIDC subject; the deployer bindings read the workflow file and ref, not the subject.
- From invoice-os-infra #43 on: only `deploy-prod` writes `images`, tags are immutable, and `images` keeps the 50 newest and deletes older untagged. Before #43: keep-recent 20, delete-old 30 d, tags mutable, `deploy-preview` still a writer.
- Every `deploy-prod` run builds each image and pushes it untagged, by digest, to `africa-south1-docker.pkg.dev/ascomply-preview/images`. See `registry.tf`, `deployers.tf` in invoice-os-infra.
- Deploy, migrate and deploy-check use the digest that the run's own push recorded (`prod-image.sh push`, `ref`), never a tag. A re-run or a dispatch rebuilds. Run `prod-image.sh` from the workflow's commit, never from the commit being deployed.
- From #43 on, `images` keeps the 50 newest versions of each package and deletes older untagged ones. Before each push, `prod-image.sh pin` tags `keep-<digest>` on a serving, rollback or job image at rank 40 or more (ten below the 50). A failed read or tag annotates and the build goes on; a failed tag at rank 49 or more stops the deploy (the build matrix is fail-fast and deploy needs every leg; `TestProdImagePinStopsTheLegWhenTheLastPinFails`). Change `keep_count` and `KEEP_VERSIONS` together. The nightly reads auth's commit from the `commit` label each deploy sets.
- Run the `migrate` job before any revision starts. Keep migrations expand-only: old and new revisions share the database.
- Start every new revision with `--no-traffic` and the tag `c-<sha12>`.
- Check the tagged revisions with the `deploy-check` job (`cmd/deploycheck`). Tag URLs answer only inside the production network.
- The `deploy-check` job reads the gateway `/health` with `GATEWAY_TOKEN`, mounted from the `gateway-token` secret.
- Read the library, the blog and the check in the deploy's site check at `www.ascomply.com/library`, `www.ascomply.com/blog` and `www.ascomply.com/check`.
- Move traffic with `gcp-traffic.sh promote`. It sends 100 % to the run's tagged revision and tags the previous revision `rollback`.
- A tagged revision with a revision-level minimum keeps running. The worker's rule is in `add-service.md`.
- After the move, poll `/healthz/fleet` through the load balancer. On failure, `gcp-traffic.sh restore` sends traffic back.
- Roll back with `rollback-prod.yml`. It moves every service to its `rollback` revision together.
- Keep the deploy read-only after migrate. It writes no data.
- Probe a service at `/health` on a run.app host. Cloud Run's front end answers `/healthz` itself there, and the container never sees it.

## Who may assume a deployer

| Deployer | Workflow file | Run |
|----------|---------------|-----|
| `deploy-prod` | `deploy-prod.yml` | `refs/heads/main` |
| `deploy-prod` | `nightly.yml` | `refs/heads/main` |
| `deploy-prod` | `rollback-prod.yml` | `refs/heads/main` |
| `deploy-preview` | `dev-env.yml` | `pull_request`, `refs/heads/main` |
| `deploy-preview` | `dev-env-teardown.yml` | `pull_request` |
| `deploy-preview` | `dev-env-sweeper.yml` | `refs/heads/main` |
| `deploy-nightly` | `dev-env.yml` | `refs/heads/main` |

- The list lives in invoice-os-infra `bootstrap/wif-provider.yaml`; a token from any other file is refused at the exchange. In invoice-os-infra only `terraform.yml` gets a credential.
- Rename or add a workflow that authenticates in three steps. (1) Add the new file name to `wif-provider.yaml`; the owner merges it and runs `bootstrap.sh`. (2) Merge the invoice-os change, which updates this table in the same PR. (3) Remove the old name from `wif-provider.yaml` and run the script again.
- `TestWIFTable_*` (`tools/prenv/wif_binding_workflow_test.go`) fails a PR when a workflow that calls `google-github-actions/auth` has no row, names a deployer its rows do not give it, or a row names a missing file.
