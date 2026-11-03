---
paths:
  - ".github/workflows/**"
  - "scripts/ci/**"
  - "tools/prenv/**"
---
# CI

- Production deploys are in `ci-gcp.md`. PR previews are in `ci-preview.md`.
- Never trigger a workflow on `pull_request_target`, `workflow_run` or `workflow_call`. Never use `secrets: inherit` or a local `uses: ./` step. `TestNoWorkflowReachesSecretsThroughATrustedTrigger` pins it.
- Run E2E only on `pull_request` and on the nightly preview. The api suite writes data, so it never runs against production.
- Start the `topology` job on a PR only when the `e2e` job succeeded. A red `e2e` job skips topology and fails the gate. On the nightly, start topology when `e2e` has ended.
- Expect a superseded commit's preview to fail at the CI watch. `ci.yml` cancels the earlier run on the same ref.
- Give a PR the library lane when every changed path is under `frontend/library/` and none is in `SHARED_WITH_BLOG` in `scripts/ci/lane.sh`.
- Give a PR the blog lane when every changed path is under `frontend/blog/`. `frontend/blog/index.html`, `frontend/blog/Dockerfile` and `frontend/blog/Dockerfile.dockerignore` give the full lane: they are inputs of `cmd/blog`.
- Give any other PR the full lane. On the library or blog lane, CI runs the `Library` or `Blog` job in place of `Frontend`, and the preview runs no E2E.
- Keep the small lanes to `frontend/library/` and `frontend/blog/`. `TestLane_OnlyANamedPackageTakesTheSmallLane` fails when another package takes one.
- Create every PATH shim in a `tools/prenv` test with `writeShim`. A freshly written executable costs about 0.8 s on its first exec on macOS; `writeShim` avoids it.
- Start each `tools/prenv` test with `t.Parallel()`. Keep a test serial only when it calls `t.Setenv`, assigns a package-level variable, writes into the repo tree or asserts a real-clock window, and say why in a `// serial:` line.
- Run a bash script from a `tools/prenv` test through `runToFiles`, not a pipe. Under parallel load `/bin/bash` 3.2 fails an `echo` on a blocked pipe with EINTR.
- Feed `grep -q` and `head -n 1` a here-string, never a pipe, in a script under `pipefail`. The writer of the pipe gets SIGPIPE when the reader exits early, and the pipeline fails.
- Dispatch `ci.yml` only from `nightly.yml`. A dispatch is full mode: every job runs, and the roll-up is named `CI (nightly)`.
- Dispatch `dev-env.yml` only from `nightly.yml`. A dispatch deploys the `nightly` preview, runs E2E without the flaky gate and the `E2E gate`, and deletes the preview.
- Export `ARGOS_TOKEN` only in the `Send the screenshots to Argos` step of `nightly.yml`. `dev-env.yml` compares it with empty and exports nothing.
- Read the Workload Identity provider from `vars.GCP_WIF_PROVIDER` and the HubSpot portal id from `vars.HUBSPOT_PORTAL_ID`. Write neither value in a tracked file.
- Run `scripts/ci/require-vars.sh` with a job's variables after checkout and before the first Google auth step in each root GCP job. `dev-env-teardown.yml` inlines the check, because it checks out the PR head. `prodid_vars_workflow_test.go` pins the jobs.
- Run the `Production ids` job (`internal/tools/prodidgate`) on every PR. It fails a tree that holds the GCP prod project number or the HubSpot portal id. It allows no exception. Update its pinned digest when a value changes. `-selfcheck` fails until you do.
