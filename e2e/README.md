# @invoice-os/e2e — deployed SPA smoke suite

Playwright smoke tests (M1-08) for the three deployed SPAs. Each test opens a
deployed URL, asserts a signature element of the app's main mock view is
rendered, and fails on any console error or uncaught page error during load.

There is **no local web server** — tests always run against a real deployed URL.

## Run

```bash
pnpm --filter @invoice-os/e2e test          # against the live dev URLs (defaults)
pnpm --filter @invoice-os/e2e exec playwright install chromium   # first run only
```

## Target URLs

Each app's URL defaults to its live dev deployment and is overridable via an env
var, so the same suite runs against a PR preview or any other deploy:

| App         | Env var            | Default (dev)                                    |
| ----------- | ------------------ | ------------------------------------------------ |
| landing     | `LANDING_URL`      | https://landing-development-92a2.up.railway.app  |
| app         | `APP_URL`          | https://app-development-3b4b.up.railway.app      |
| ops-console | `OPS_CONSOLE_URL`  | https://ops-console-development.up.railway.app   |

```bash
APP_URL=https://app-pr-123.up.railway.app pnpm --filter @invoice-os/e2e test
```

## Topology suite (M2-14)

A second suite (`topology/`) is the M2 exit criterion: it drives the **live gateway**, not
just the SPAs. On a gateway-wired app build it proves the browser round trip renders the
backend-verified tenant identity, and it checks cross-tenant isolation over the live edge.
It runs against `GATEWAY_URL` + `APP_URL` (same env-var/live-dev-default convention) and is
orchestrated by `.github/workflows/topology-e2e.yml` (bring-up → reset+seed → assert →
scale-to-zero). See `docs/topology-e2e.md`.

```bash
pnpm --filter @invoice-os/e2e test:topology   # needs the gateway-wired dev deploy up
```
