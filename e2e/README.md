# @invoice-os/e2e — deployed fleet E2E suites

Three Playwright suites verify the deployed dev fleet as post-deploy checks in
`.github/workflows/dev-env.yml` (M2-14): **smoke** (SPA render, console coverage, and the
cross-persona boundary matrix), **api** (a typed HTTP contract suite over the live
gateway — no browser) and **topology** (browser-driven, backend-verified assertions over
the live gateway). Each has its own config — `playwright.config.ts`,
`playwright.api.config.ts`, `playwright.topology.config.ts` — and its own reason to exist.

A fourth project, `test:unit`, is not a fleet suite at all: see [Unit tests](#unit-tests).
A fifth, `test:hooks`, tests the `.claude/hooks` git-history guard in the `Claude hooks` CI job.

There is **no local web server** — the Playwright suites always run against a real
deployed URL.

**This file is how to run these suites and what each one needs.** What may be asserted and
why — organize by capability, keep the browser layer thin, functional only, one browser
serial, the two persona coverage grades — lives in **`docs/e2e-convention.md`**, which is
canonical. Read that one before adding a spec; read this one before running the suites.
When the two disagree, the convention doc wins and this file is the one to correct.

## Target URLs

Each target's URL is a **required** env var — there is no hardcoded default. Every PR now
deploys to its own ephemeral Railway environment with an unpredictable domain suffix
(M4-23), so a missing var throws naming itself rather than silently falling back to the
shared `development` fleet (Decision `[fail-loud-targets]`, `targets.ts`):

| Target          | Env var               | Needed by            |
| --------------- | --------------------- | -------------------- |
| landing         | `LANDING_URL`         | smoke, topology      |
| ops-console     | `OPS_CONSOLE_URL`     | smoke                |
| support-console | `SUPPORT_CONSOLE_URL` | smoke                |
| app             | `APP_URL`             | smoke, topology      |
| gateway         | `GATEWAY_URL`         | smoke, api, topology |

CI sets all five for the whole `e2e` job, so this table matters mainly when running a
suite by hand. Most are resolved at module scope and throw during collection; a few
resolve lazily and throw on the first test that needs them.

## Smoke suite

`playwright.config.ts` → `testDir: './smoke'`, `fullyParallel: true`.

Covers the three SPAs the landing page hands off to — `landing`, `ops-console` and
`support-console`. It is no longer only a render check:

- **Render** (`smoke/apps.ts`, `smoke.spec.ts`): landing is opened bare and each console on a
  seeded real staff session (`staffSession.ts`, which needs `GATEWAY_URL`). Each asserts a
  signature element of its main view, failing on any console error or uncaught page error.
- **Behaviour on backend-less surfaces** (`landing-nav.spec.ts`, `ops-console.spec.ts`,
  `support-console.spec.ts`): the landing nav's scroll-spy, and functional navigation over
  what each console is *for*. Both consoles' data is mock with no backend, so these
  assertions pin fixture behaviour rather than a contract — `docs/e2e-convention.md` says
  when that is allowed.
- **Boundary matrix** (`persona-boundaries.spec.ts`): every destination visited with
  a `persona` query parameter and no session must bounce the visitor back to the landing page. This drives **all
  three destinations including the app**, which is why smoke needs `APP_URL` too. Every
  cell is refused before any gateway contact — no database reads —
  so the suite stays safe under `fullyParallel: true` (`[boundaries-in-smoke]`).

```bash
pnpm --filter @invoice-os/e2e exec playwright install chromium   # first run only
LANDING_URL=... OPS_CONSOLE_URL=... SUPPORT_CONSOLE_URL=... APP_URL=... GATEWAY_URL=... \
  pnpm --filter @invoice-os/e2e test:smoke    # `test` is the same command
```

## API suite (M3-14)

`playwright.api.config.ts` → `testDir: './api'`, `fullyParallel: false`, `workers: 1`.

A headless, typed HTTP contract suite over the same deployed gateway — **no browser at
all**, so the config declares no browser project and `playwright install` is not needed
for it. `api/client.ts` resolves `GATEWAY_URL` itself, mirroring `topology/targets.ts`,
which is why `baseURL` is intentionally unset.

**The serial setting is load-bearing, not a leftover default.** Every spec shares one
deployed database, so parallel workers would race — entity-namespace contention
(Decision A8). The suite is not read-only, which is why CI runs it against ephemeral PR
environments only.

```bash
GATEWAY_URL=... pnpm --filter @invoice-os/e2e test:api
```

## Topology suite (M2-14)

`playwright.topology.config.ts` → `testDir: './topology'`, `fullyParallel: false`,
`workers: 1`, one Playwright project per unit in `topology/shards.ts`: `serial-lane`,
`import-wizard`, `import-wizard-2` and `invoice-surfaces`. CI runs each unit on its own runner in parallel;
`--project=<unit>` runs one. A spec file not assigned to exactly one unit fails the run at
config load.

The M2 exit criterion: it drives the **app** SPA and the **live gateway** together, not
just an SPA in isolation. In the unified dev env the app is always gateway-wired
(`VITE_GATEWAY_URL` set), so this suite owns the app's assertion — the real sign-in
hand-off must render the backend-verified tenant identity, not the mock-only shell render
the smoke suite used to check. It also asserts cross-tenant isolation over the live edge,
and drives the app's persona-scoped surfaces, the import wizard, invoices and Workflows.

Each unit is serial on one worker, for the same reason as the api suite: the specs of a
unit share the same non-reset deployed dev database (`[topology-config-conforms-workers-1]`).
The two shard units sign in on their own seeded tenants (`signInAs` with a `tenantId`), so
they do not contend with the lane. Beyond `GATEWAY_URL` + `APP_URL` it
also needs `LANDING_URL` — `topology/auth.spec.ts` starts at the landing front door.

```bash
GATEWAY_URL=... APP_URL=... LANDING_URL=... pnpm --filter @invoice-os/e2e test:topology
```

Without `--project` the command runs every unit, all on its one worker, one after another.
Add `--project=serial-lane` (or another unit name) after `test:topology` to run one, as CI does.

**There is no fleet-health gate in this suite.** The only fleet-health assertion in the
package is `api/perf.spec.ts`'s PERF-06: `GET /healthz/fleet` returns 200 with
`status: "ok"` and every entry in `services` reporting `up`. It iterates whatever the
roll-up returns and deliberately asserts **no service count**, so it cannot rot as the
fleet grows. Gating the *deploy* on the backends being green is `dev-env.yml`'s own
`fleet-gate` job, which runs before any suite. See `docs/topology-e2e.md`.

## Unit tests

`test:unit` is **not** a fleet suite. It runs this package's own seam logic under vitest in
`node` (`vitest.config.ts`), needs no deployment, and is wired into `ci.yml` rather than
`dev-env.yml`.

The filename split is what keeps the two runners apart: Playwright collects `*.spec.ts`
only, vitest includes `*.test.ts` only. Every config states this, because collecting the
other kind aborts the entire run.

**The trap that creates.** `test:unit` runs with **no deploy URLs set**, and both
`topology/targets.ts` and `smoke/apps.ts` resolve their targets at *module scope* — they
throw on import when a var is missing. So anything a `*.test.ts` imports, `personas.ts`
above all, must never transitively import either of them. `personas.ts` imports only
`targets.ts` (which exports `resolveTarget` and resolves nothing itself) and calls it
lazily, inside function bodies, for exactly this reason.

```bash
pnpm --filter @invoice-os/e2e test:unit
```

`test:hooks` runs `gitHistoryGuard.test.ts` (`vitest.hooks.config.ts`); `test:unit` excludes it. CI runs it in the `Claude hooks` job, only when `.claude/hooks/**` or either file changes:

```bash
pnpm --filter @invoice-os/e2e test:hooks
```

## Agent control CLI (`ctl`)

`ctl` is for agents, not CI. It replaces hand-driven browser sessions on a PR environment with three commands that print one JSON object each: stdout and exit 0, or stderr and exit 1 (failure) or 2 (usage). It needs Node >= 22.18, which runs the TypeScript directly (`node --import ./ctl/tsResolve.mjs ./ctl/main.ts`), so `e2e/ctl` uses erasable syntax only (`erasableSyntaxOnly`, checked by `typecheck`).

```bash
ctl() { pnpm -s --filter @invoice-os/e2e ctl "$@"; }
pwc() { pnpm -s --filter @invoice-os/e2e exec playwright-cli "$@"; }

ctl env pr-348                                  # { env, environmentId, urls, dark }
ctl login firm --env pr-348 --role reviewer     # sign in, save a storage state, print "next"
ctl measure '[data-testid="evidence-bundle-drawer"]' --props width,padding-left --viewport 1440
```

- **`env <pr-N|production>`** resolves the five service URLs from Railway and lists dark domains in `dark` (exit 1). It reads `~/.railway/config.json`, whose token expires after about an hour: run `railway whoami`, else `railway login`.
- **`login <firm|inhouse|developer|support> --env <pr-N> [--role admin|preparer|reviewer]`** creates the 8 demo accounts once per environment (admin, preparer and reviewer on tenants 1111 and 2222, plus a developer and a support staff account), signs in and saves a storage state. `--role` applies to `firm` and `inhouse` only. A repeat call reuses the account and the state (`reused: true`, `gatewayWrites: 0`). `--env production` exits 1: production has no demo account, so use read-only probes there.
- **`measure <selector> --props <p1,p2> [--viewport <width>]`** prints the box and computed styles of every match in the open `playwright-cli` session, after animations settle. Write a custom property as `--props=--x`. A selector with no match exits 1. `--viewport` leaves the open page at that width.
- **Session:** `--session`, else `$PLAYWRIGHT_CLI_SESSION`, else `default`.
- **Store:** `<worktree>/.ralph/ctl/<env>/`, gitignored. `accounts.json` (mode 0600) holds the passwords; `<persona>-<role>.json` (`developer.json`, `support.json` for staff) are the storage states. Output never prints a password. Delete the directory to re-create the accounts.
- **Browser:** `login` signs in in its own browser and never drives your `playwright-cli` session. It prints the `next` commands. Run `pwc list --json` first, and `open` only when the session is absent (`open` restarts an open one), then `state-load` the saved file, then `goto` the URL. `playwright-cli` blocks `file:` URLs. A console renews its session on every load, so `state-load` a developer or support state into one `playwright-cli` session only. To open another console session, delete that persona's `<key>.json` and run `ctl login` again. App personas renew only at 80 % of token life.
- **Read-only:** apart from `login`'s account creation and grants, click nothing that writes tenant data.

```bash
pwc list --json
pwc -s=s1 open
pwc -s=s1 state-load "$PWD/.ralph/ctl/pr-348/firm-reviewer.json"
pwc -s=s1 goto https://app-pr-348.up.railway.app
ctl measure 'h1' --props font-size --session s1
```

## How CI runs them

`dev-env.yml`'s `e2e` job runs **smoke → api**, in that order, on pull requests only. The
`topology` job runs after it (`needs: e2e`), one matrix leg per unit, in parallel.
**The api → topology ordering is load-bearing**: the two share one deployed database.
