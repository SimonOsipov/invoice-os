# @invoice-os/e2e — deployed fleet E2E suites

Three Playwright suites verify the deployed dev fleet as post-deploy checks in
`.github/workflows/dev-env.yml` (M2-14): **smoke** (SPA render, console coverage, and the
cross-persona boundary matrix), **api** (a typed HTTP contract suite over the live
gateway — no browser) and **topology** (browser-driven, backend-verified assertions over
the live gateway). Each has its own config — `playwright.config.ts`,
`playwright.api.config.ts`, `playwright.topology.config.ts` — and its own reason to exist.

A fourth project, `test:unit`, is not a fleet suite at all: see [Unit tests](#unit-tests).

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

| Target          | Env var               | Needed by       |
| --------------- | --------------------- | --------------- |
| landing         | `LANDING_URL`         | smoke, topology |
| ops-console     | `OPS_CONSOLE_URL`     | smoke           |
| support-console | `SUPPORT_CONSOLE_URL` | smoke           |
| app             | `APP_URL`             | smoke, topology |
| gateway         | `GATEWAY_URL`         | api, topology   |

CI sets all five for the whole `e2e` job, so this table matters mainly when running a
suite by hand. Most are resolved at module scope and throw during collection; a few
(smoke's `APP_URL`) resolve lazily and throw on the first test that needs them.

## Smoke suite

`playwright.config.ts` → `testDir: './smoke'`, `fullyParallel: true`.

Covers the three SPAs the landing page hands off to — `landing`, `ops-console` and
`support-console`. It is no longer only a render check:

- **Render** (`smoke/apps.ts`, `smoke.spec.ts`): each app is opened through the real
  landing sign-in hand-off (`?persona=`, never a test-only backdoor) and asserts a
  signature element of its main view, failing on any console error or uncaught page error.
- **Behaviour on backend-less surfaces** (`landing-nav.spec.ts`, `ops-console.spec.ts`,
  `support-console.spec.ts`): the landing nav's scroll-spy, and functional navigation over
  what each console is *for*. Both consoles are mock data with no backend, so these
  assertions pin fixture behaviour rather than a contract — `docs/e2e-convention.md` says
  when that is allowed.
- **Boundary matrix** (`persona-boundaries.spec.ts`): every destination handed a persona
  it does not admit must bounce the visitor back to the landing page. This drives **all
  three destinations including the app**, which is why smoke needs `APP_URL` too. Every
  cell is refused synchronously before any fetch — no gateway contact, no database reads —
  so the suite stays safe under `fullyParallel: true` (`[boundaries-in-smoke]`).

```bash
pnpm --filter @invoice-os/e2e exec playwright install chromium   # first run only
LANDING_URL=... OPS_CONSOLE_URL=... SUPPORT_CONSOLE_URL=... APP_URL=... \
  pnpm --filter @invoice-os/e2e test:smoke    # `test` is the same command
```

## API suite (M3-14)

`playwright.api.config.ts` → `testDir: './api'`, `fullyParallel: false`, `workers: 1`.

A headless, typed HTTP contract suite over the same deployed gateway — **no browser at
all**, so the config declares no browser project and `playwright install` is not needed
for it. `api/client.ts` resolves `GATEWAY_URL` itself, mirroring `topology/targets.ts`,
which is why `baseURL` is intentionally unset.

**The serial setting is load-bearing, not a leftover default.** The kill-switch spec
mutates the **global `rules` table** and every spec shares one deployed database, so
parallel workers would race — a concurrent validate observing a mid-toggle rule, or
entity-namespace contention (Decision A8). For the same reason the suite is not read-only:
it self-heals rule state in `beforeAll` and restores it in `afterAll`, which is why CI
runs it against ephemeral PR environments only.

```bash
GATEWAY_URL=... pnpm --filter @invoice-os/e2e test:api
```

## Topology suite (M2-14)

`playwright.topology.config.ts` → `testDir: './topology'`, `fullyParallel: false`,
`workers: 1`.

The M2 exit criterion: it drives the **app** SPA and the **live gateway** together, not
just an SPA in isolation. In the unified dev env the app is always gateway-wired
(`VITE_GATEWAY_URL` set), so this suite owns the app's assertion — the persona sign-in
hand-off must render the backend-verified tenant identity, not the mock-only shell render
the smoke suite used to check. It also asserts cross-tenant isolation over the live edge,
and drives the app's persona-scoped surfaces, the import wizard, invoices, validation and
Workflows.

Serial for the same reason as the api suite: it shares the same non-reset deployed dev
database (`[topology-config-conforms-workers-1]`). Beyond `GATEWAY_URL` + `APP_URL` it
also needs `LANDING_URL` — `topology/auth.spec.ts` starts at the landing front door.

```bash
GATEWAY_URL=... APP_URL=... LANDING_URL=... pnpm --filter @invoice-os/e2e test:topology
```

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

## How CI runs them

`dev-env.yml`'s `e2e` job runs **smoke → api → topology**, in that order, on pull requests
only. **The ordering is load-bearing**: the api suite's `beforeAll` self-heal and
`afterAll` rule-restore must complete before topology's rule-dependent assertions run, and
the two share the global `rules` fixture.
