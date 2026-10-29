---
paths:
  - ".github/workflows/**"
  - "scripts/ci/**"
  - "tools/prenv/**"
---
# CI

- Production deploys are in `ci-gcp.md`. PR previews are in `ci-preview.md`.
- Never trigger a workflow on `pull_request_target`, `workflow_run` or `workflow_call`. Never use `secrets: inherit` or a local `uses: ./` step. `TestNoWorkflowReachesSecretsThroughATrustedTrigger` pins it.
- Run E2E only on `pull_request`. The api suite writes data, so it never runs against production.
- On a PR, start the `topology` job only when the `e2e` job succeeded. A red `e2e` job skips topology and fails the gate. In a `workflow_dispatch` (nightly) run, topology also starts after a failed `e2e`.
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
