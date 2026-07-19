# Dev Deploy Model — per-PR ephemeral environments (M2-14, reworked M4-21, M4-23)

How the full fleet — the gateway, the 7 context services, and the three frontend SPAs
(`landing`, `app`, `ops-console`) — is deployed to Railway. Adopted in M2-14 (one unified
fleet deploy, superseding the M1-08 split model); reworked in M4-21 to end the shared-
`development` model, and completed in **M4-23**, which is when per-PR environments started
actually existing: CI now creates, tears down and sweeps them itself.

## The model

- **Open / update a non-draft PR** → CI deploys and verifies the **whole fleet** together,
  coherently, from the PR's code, into a **fresh ephemeral Railway environment forked from
  `development`** — never into `development` itself. (`.github/workflows/dev-env.yml`)
  > The fork is issued by **CI**, not by Railway's PR Environments feature — that feature
  > never created anything here (Railway is not subscribed to this repo's PR events) and is
  > OFF, see [Railway PR Environments are OFF](#railway-pr-environments-are-off) below.
- **Close a PR (merged or abandoned)** → `.github/workflows/dev-env-teardown.yml`
  (**M4-23-05**) deletes that PR's **whole ephemeral environment** via `environmentDelete`,
  at environment granularity (Decision `[teardown-deletes-environment]`) — not the old
  11-service `railway down` matrix. It resolves the target by exact `pr-<N>` name among
  `isEphemeral: true` environments **only**, so it can never touch `development`, and it
  shares `dev-env.yml`'s per-PR concurrency group (`dev-preview-<N>`) with `queue: max`, so
  a teardown queued behind an in-flight deploy of that same PR waits rather than deleting
  the environment mid-deploy (Decision `[teardown-shares-deploy-lock]`).
  Teardown is **best-effort — the fast path, not the guarantee.** A `closed` event that
  does not fire leaves an orphan; the daily sweeper (M4-23-07) reconciles ephemeral
  environments against PR state and is the **authority**
  (Decision `[teardown-best-effort-sweeper-authoritative]`). "Best-effort" refers to the
  *trigger*, not to the delete: an environment that is present and could not be deleted
  fails the run loudly (exit 1), because a red teardown run is the only notification
  channel there is. An already-absent environment is **success** (exit 0) — nothing to
  delete is the desired end state, and the sweeper may simply have got there first.
  > Railway tears down nothing here — its PR Environments feature is OFF and never
  > applied to this project (see
  > [Railway PR Environments are OFF](#railway-pr-environments-are-off)). Teardown is
  > **M4-23-05**, below; M4-23-06 is `ShouldReap`, the sweeper's pure reap predicate.
- **`workflow_dispatch`** → targets the **persistent `development` environment** directly
  (never an ephemeral PR environment) — the same fleet-deploy + health-gate + fleet-gate
  flow, without the E2E suites (M4-22-07 dropped the reset/seed job and the
  dispatch-path E2E run). Its concurrency group is
  `dev-preview-${{ github.event.pull_request.number || github.ref }}`, so a dispatch run's
  group is `dev-preview-refs/heads/<branch>` — it serializes only against **other dispatch
  runs from the same ref**, not across refs.
- **`development` itself** is a stable, always-up base + demo environment (Decision
  `[dev-env-status]`) — purely the fork base every PR environment is created from (Decision
  `[development-role]`), and it is what live demo calls point at. It is **not** torn down
  by any automated workflow.
- **Production** → nothing. The `production` environment stays dormant.

Each PR's ephemeral environment and its four public URLs (gateway, app, landing,
ops-console) are **discovered fresh at deploy time** — a Railway-generated domain's suffix
is opaque/random and cannot be constructed (F7) — never hardcoded, and never assumed stable
across PRs the way `development`'s own four URLs (still constant, still hardcoded as
`RAILWAY_SVC_*_ID` **service ids**, never as domain strings) are.

```
PR opened ──> dev-env.yml:
                prepare-env: derive `pr-<N>` (prenv.Name) ──> environmentCreate, forked
                             from `development` (skipInitialDeploys, create-or-reuse)
                             ──> deploy Postgres + probe ──> assert Watch Paths empty
                             (M3-16 invariant, now runtime-asserted) ──> discover the
                             4 URLs
                gateway ──> gate on /healthz (schema migrated + seeded at boot, M4-21-04)
                ──> 7 context services + 3 SPAs (app is gateway-wired)
                ──> verify: smoke (landing + ops-console) + api + topology (app login,
                    cross-tenant isolation, fleet /healthz/fleet gate) + demo
              ──> PR stays open: environment stays up
PR closed  ──> dev-env-teardown.yml (M4-23-05): prenv name ──> look the name up among
               ephemeral environments ──> environmentDelete ──> confirm by re-query.
               Best-effort; the daily sweeper (M4-23-07) is the authority.

workflow_dispatch ──> targets `development` directly (persistent, never torn down)
                   ──> same deploy + health-gate + fleet-gate flow, no E2E (M4-22-07
                       dropped the reset/seed job and dispatch-path E2E run)
```

### Why per-PR environments, not one shared env

The old M2-14 model reasoned "dev is single-branch, single-agent: only one PR is ever
meaningfully 'the' dev env at a time" — true when stories ran one at a time, but it meant
every PR queued behind `dev-env.yml`'s single shared concurrency lock, serializing all CI
deploys. M4-21 designed the constraint away and M4-23 delivered it: each PR forks its own
environment, so `dev-env.yml`'s concurrency group is now keyed **per-PR**
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
GitHub. With nothing subscribed, `prDeploys = true` produced exactly nothing — the flag
was on and the feature was completely **inert**. (That the flag was on is a dashboard
observation by hand, not a CI measurement; see the "before" caveat above.)

**First CI observation (2026-07-18), against the live Railway API:**

```
prDeploys=false botPrEnvironments=false focusedPrEnvironments=false baseEnvironmentId=null deploymentTriggers=0
```

Note `baseEnvironmentId=null` and `deploymentTriggers=0`. The project has **no fork base
configured at all** and **nothing subscribed to GitHub** — so even with `prDeploys=true`
the feature had neither a base to fork from nor an event to fire on. This is the direct
measurement behind the claim above. `dev-env.yml`'s old header comment asserted
"PR Environments ON, base = `development`": the first half was true at the time and is now
false by design; the second half was **never** true — no base was ever configured. What the
comment implied — that this produced working PR environments — was false throughout.

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

**Done, and now CI-asserted.** M4-23 measured `deploymentTriggers = 0` across every
environment, and `railway-invariants.yml` re-asserts that on every PR — any trigger
reappearing fails the build. The procedure below is retained for the case where a service
is recreated and arrives with a trigger attached.

For each of the 11 services (gateway, the 7 context services, and `landing`, `app`,
`ops-console`) **on the `development` environment**:

1. Railway dashboard → the service → **Settings**.
2. Under the GitHub trigger, click **Disable** ("stop deploying automatically on
   new commits"). See [Railway docs — Controlling GitHub Autodeploys](https://docs.railway.com/deployments/github-autodeploys#disable-automatic-deployments).

Verify: push a trivial no-op commit to `main` and confirm no service redeploys
on `development` (Deployments tab shows nothing new). Open a throwaway PR and confirm
`dev-env.yml` deploys + verifies its own ephemeral environment, then close it and confirm
`dev-env-teardown.yml` ran and the `pr-<N>` environment is gone. Expect
`railway-invariants.yml` to be **red** on that throwaway PR until the last trigger is gone —
that is the invariant working, not a fault. If it is still there, the
daily sweeper reaps it within a day.

### Rollback

To revert to the M1-06 always-on single-environment model: re-enable auto-deploy per
service on `development` (Settings → **Enable**), and disable/remove `dev-env.yml`,
`dev-env-teardown.yml`, `dev-env-sweeper.yml` and `railway-invariants.yml` — the last of
these will otherwise fail every PR once the deployment triggers are back.

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
runtime assertion**: `dev-env.yml`'s `prepare-env` job (M4-21-09, renamed M4-23) queries every non-Postgres
service instance's Watch Paths in the target environment and **fails the run, naming the
offending service(s)**, if any are non-empty (Decision `[watch-paths-asserted]`) — a silent
regression can no longer reach the deploy steps undetected. If a service is ever deleted
and recreated, its Watch Paths must still be re-cleared. Postgres is excluded — it was
never in the deploy fleet.

This empty-Watch-Paths invariant remains Railway-side dashboard config, not codified
directly in `railway.json` (per the M3-16 decision above) — only *asserted* by CI now, not
*set* by it.

## Teardown (M4-23-05)

`.github/workflows/dev-env-teardown.yml` deletes a PR's ephemeral environment when the PR
closes. `scripts/ci/railway-env.sh delete-environment <name>` does the work.

**The trigger was selected by experiment, not by argument.** The open question was whether
`pull_request: types: [closed]` even *fires* for a workflow file that is not yet on the base
branch — if it did not, teardown would have needed `pull_request_target` and would have been
provable only after merge.

| | |
|---|---|
| Method | Throwaway PR **#68** (branch `probe/teardown-trigger`, based on the feature branch), carrying a trivial probe workflow present **only on that branch** and never on the base |
| Control | `action=opened` → run **29660556112** — fired |
| Decisive | `action=closed`, `merged=false` → run **29660575019** — fired, 10s after an **unmerged** close |
| Result | **`pull_request: types: [closed]` fires on unmerged close.** `pull_request_target` is not needed; there is no post-merge-only limitation |

The `opened` control was load-bearing, not ceremony: without it a non-fire on close would
have been uninterpretable — it could not distinguish "the `closed` type does not fire" from
"this file was never discovered at all". This **supersedes** the earlier PR #52 observation,
which was confounded: #52 touched no workflow file and the cleanup workflow was already on
`main` throughout, so it only ever showed which ref an *already-merged* workflow was read
from — never that a PR-branch-only workflow would be **discovered** on close.

One fire is sufficient to select the trigger. GitHub community#26657 alleges **inconsistent**
firing, not non-firing, and inconsistency is already owned by the sweeper — so this is not
treated as a guarantee (Decision `[teardown-best-effort-sweeper-authoritative]`).

Two details that are load-bearing rather than stylistic:

- **Checkout is pinned to `github.event.pull_request.base.sha`.** Mandatory, not tidiness:
  this remote carries 66 `refs/pull/*/head` and **zero** `refs/pull/*/merge`, i.e. the merge
  ref is deleted at the moment of close, so a default checkout would race that deletion.
  Teardown needs only trusted base-branch code (`railway-env.sh` and `tools/prenv`) anyway.
- **The delete is proved by an independent re-query, never by `environmentDelete`'s return
  value.** That mutation returns a bare Boolean — indistinguishable from a silent no-op, the
  same shape this repo already distrusts for `serviceInstanceRedeploy`. Absent after the
  mutation counts as success *even if the mutation reported an error*, which also correctly
  absorbs the sweeper racing teardown to the same delete.

There are two independent never-delete-`development` guards: the name is resolved among
`isEphemeral: true` environments only, and the resolved id is separately refused if it
equals `RAILWAY_DEV_ENVIRONMENT_ID`. The second catches what the first structurally cannot —
Railway ever mislabelling `development` as ephemeral, or a human renaming it to `pr-<N>`.
The subcommand takes a **name and never an id** for the same reason: an id argument would
let a caller pass `development`'s id straight through and bypass the ephemeral filter.

## Sweeper (M4-23-07) — the authority

`.github/workflows/dev-env-sweeper.yml` is what actually guarantees no environment leaks.
Teardown is a fast path that can miss (`pull_request: closed` is documented as firing
inconsistently, and merge conflicts suppress `pull_request` events entirely); the sweeper
reconciles regardless.

- **Schedule:** cron `'17 4 * * *'` (daily), plus `workflow_dispatch`. Not on the hour —
  GitHub oversubscribes on-the-hour crons and drops or delays them.
- **Reap predicate** (`prenv.ShouldReap`, M4-23-06): reaps **only** on positive evidence
  that the environment's PR is CLOSED or MERGED. **Never on age** — an age TTL can kill an
  environment someone is actively using. Never `development`. An environment whose PR state
  cannot be determined is left alone.
- **Concurrency:** single-flight against itself (`dev-env-sweeper`). It deliberately does
  *not* join teardown's `dev-preview-<N>` lock — one sweep spans many PRs and has no single
  N. The residual race (sweeping `pr-<N>` while `dev-env.yml` deploys it) is accepted: it
  requires PR N to be closed, in which case deleting is correct anyway.

**Known limitation:** `schedule:` runs only from the **default branch**, so the daily cron
cannot be exercised until this merges. Before then only `workflow_dispatch` is provable.

## What a fork actually inherits (M4-23-04, measured)

Measured 2026-07-18 against a real probe fork of `development`, created with the exact
payload `railway-env.sh` ships (`ephemeral: true, skipInitialDeploys: true`), inspected,
then deleted. These are observations, not inferences from Railway's docs — several
contradict what the docs imply.

| Thing | Carries into a fork? | Consequence for `prepare-env` |
|---|---|---|
| Service instances | Yes — all 12, immediately, `watchPatterns: []` on every one | No settle race. The M3-16 invariant holds in a fork. The settle poll is insurance only. |
| Public domains | Yes, auto-renamed `<svc>-pr-<N>.up.railway.app` | Domain reconcile is a **no-op**: one query per service, zero mutations. The create path is insurance. |
| `targetPort` on those domains | `null` — in the fork **and** in `development` | Null is the NORMAL state (Railway magic-port detection). CI must **not** fail on it, and must not invent `8080`. |
| Postgres deployment | **No** — `latestDeployment == NONE` | Real gap: nothing in this repo ever deployed Postgres (the `railway up` matrices are gateway + 7 contexts + 3 SPAs; Postgres is excluded above). `prepare-env` now deploys it explicitly via `serviceInstanceDeployV2`, then waits. |
| Postgres volume | **No** — `volumeInstances == []`, while `development` has 5000MB | **CI must CREATE it.** Without a volume Postgres deploys to `SUCCESS` but **never accepts a connection** (corrected 2026-07-19 — see below). `prepare-env` creates it with `volumeCreate`, copying the `mountPath` and `region` from `development`, confirms by re-query, and redeploys Postgres if a deployment already existed. The database is still **ephemeral by design** and born empty — the gateway bootstraps, migrates and seeds at boot. |
| TCP proxy + `DATABASE_PUBLIC_URL` | Yes, with its own distinct port; `DATABASE_URL` resolves too | Since M4-22-08, `prepare-env` no longer probes or observes the proxy at all. `health-gate`'s `/healthz` 200 is now the sole Postgres liveness proof (`docs/migrations.md` §2) — strictly stronger. The proxy resource itself is scheduled for deletion via Escalation E2; until then it may still exist, unused. |
| Sealed variables | **No** — they never fork | `prepare-env` fails loudly if `development` holds any, since they would otherwise go silently missing in every PR environment. |
| Leftover PR environments | None existed before the probe | Independent confirmation that Railway's PR Environments feature never created any here. |

### Correction, 2026-07-19 — "no volume is fine" was false

The row above previously said a volume-less forked Postgres *"still deploys, reaches
SUCCESS and accepts TCP connections"*, and CI was written to record the absent volume
rather than repair it. **The "accepts connections" half was wrong**, and it cost the
first real run of this workflow.

How the error was made: a raw TCP connect (`socket.create_connection()`) to the Postgres
**TCP proxy** succeeded, and "Postgres is listening" was inferred from it. The Postgres
wire protocol was never spoken. A proxy accepts TCP whether or not the service behind it
is healthy, so that observation could not distinguish the two cases and never supported
the claim.

What is now measured — `dev-env.yml` run `29664995923`, plus a discriminator probe run
against that same environment ~40 minutes after its Postgres had reported `SUCCESS`:

| Probe | Result |
|---|---|
| `pg_isready` via `DATABASE_PUBLIC_URL` | **no response** — 12 attempts in CI, 6 more 40 min later |
| raw TCP to the same proxy host:port | **open**, throughout |

So: **the proxy routes correctly and Postgres is not answering the protocol.** A
volume-less forked Postgres does not serve, and no amount of waiting changes that — the
failure is structural, not a startup race. Hence `ensure_postgres_volume`.

The general lesson, which outlives this row: **a TCP connect is never evidence that the
service behind the socket is healthy.** `pg_isready` was, which is why it — not a status
field and not a socket — served as the authoritative liveness gate in `prepare-env` at the
time. Since M4-22-08, `prepare-env` no longer probes Postgres at all; `health-gate`'s
`/healthz` 200 is the current authoritative liveness proof (see the fork-fidelity table
above, and `docs/migrations.md` §2).

**Postgres reports `CRASHED` transiently mid-startup, then settles to `SUCCESS`.** The
wait in `railway-env.sh` therefore has **no early exit on a bad status** — it polls until
`SUCCESS` or until its budget is exhausted. This is not defensive padding: during the
probe run a poll that broke on the first `CRASHED` produced a *false* story-blocking
finding for a deployment that read `SUCCESS` moments later. Any early-fail threshold is a
guess about how long `CRASHED` can persist, and every such guess reintroduces that false
fatal.

### `ENVIRONMENT` is decorative in a fork

`ENVIRONMENT` is inherited **verbatim**, so inside `pr-<N>` it resolves to the literal
string `development`, while `RAILWAY_ENVIRONMENT_NAME` is `pr-<N>`.

`provisionableEnvironment` (`internal/platform/db/bootstrap.go`) returns true for exactly
`development` or `^(?:.+-)?pr-[0-9]+$`. A fork passes on the **first** branch; a fork whose
`ENVIRONMENT` were "correctly" `pr-<N>` would pass on the **second**. Both pass — so in a
PR environment the fail-closed allowlist **distinguishes nothing**. It retains full value
on the paths it was written for: `production`, `staging`, and empty.

This is recorded, **not repaired**. Setting `ENVIRONMENT` per-fork would be a regression,
not a fix: the environment *name* is a CI convention this workflow is free to change
(`[env-name-is-convention]`), and coupling provisioning behaviour to it would make a
rename silently change whether a database bootstraps. No variable is set and no app code
is touched.

## Related

- [add-a-service.md](./add-a-service.md) — how each service was provisioned (the
  deployment trigger this cutover disables is created in §5); its Watch-path convention
  now matches the always-empty invariant above.
- [topology-e2e.md](./topology-e2e.md) — what the post-deploy verification asserts and why.
- `e2e/README.md` — the smoke + topology suites `dev-env.yml` runs.
