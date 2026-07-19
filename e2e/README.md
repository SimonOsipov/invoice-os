# @invoice-os/e2e — deployed fleet E2E suites

Two Playwright suites that verify the deployed dev fleet as post-deploy checks in
`.github/workflows/dev-env.yml` (M2-14): **smoke** (pure SPA render checks) and
**topology** (backend-verified assertions over the live gateway).

There is **no local web server** — tests always run against a real deployed URL.

## Smoke suite

Covers the two pure SPAs, `landing` and `ops-console`: each test opens a deployed URL,
asserts a signature element of the app's main view is rendered, and fails on any console
error or uncaught page error during load. Neither app makes a backend round trip, so this
suite needs only the SPA deployments up.

```bash
LANDING_URL=... OPS_CONSOLE_URL=... pnpm --filter @invoice-os/e2e test          # required — see below
LANDING_URL=... OPS_CONSOLE_URL=... pnpm --filter @invoice-os/e2e test:smoke    # same as above, explicit
pnpm --filter @invoice-os/e2e exec playwright install chromium   # first run only
```

### Target URLs

Each app's URL is a **required** env var — there is no hardcoded default. Every PR now
deploys to its own ephemeral Railway environment with an unpredictable domain suffix
(M4-23), so a missing var throws naming itself rather than silently falling back to the
shared `development` fleet (Decision `[fail-loud-targets]`):

| App         | Env var            |
| ----------- | ------------------ |
| landing     | `LANDING_URL`      |
| ops-console | `OPS_CONSOLE_URL`  |

```bash
LANDING_URL=https://landing-pr-123.up.railway.app pnpm --filter @invoice-os/e2e test
```

## Topology suite (M2-14)

The M2 exit criterion: it drives the **app** SPA and the **live gateway** together, not
just an SPA in isolation. In the unified dev env the app is always gateway-wired
(`VITE_GATEWAY_URL` set), so this suite owns the app's assertion — the persona mock-login
must render the backend-verified tenant identity (not the mock-only shell render the old
smoke suite used to check). It also asserts cross-tenant isolation over the live edge and
gates on all 8 backends' health. It runs against `GATEWAY_URL` + `APP_URL` (same
required-env-var convention as the smoke suite, no hardcoded default) and, like the smoke
suite, is run as post-deploy verification in `dev-env.yml` (deploy fleet → boot-time
provision → smoke + topology). See `docs/topology-e2e.md`.

| Target  | Env var       |
| ------- | ------------- |
| gateway | `GATEWAY_URL` |
| app     | `APP_URL`     |

```bash
pnpm --filter @invoice-os/e2e test:topology   # needs the gateway-wired dev deploy up
```


<!-- M4-23 parallelism probe t4 — throwaway -->
