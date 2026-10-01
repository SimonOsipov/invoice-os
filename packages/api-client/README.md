# @invoice-os/api-client

Shared typed gateway client + inherited async-state UI for the ASComply
Africa frontends. Every wired surface gets ONE way to talk to the gateway: an
authenticated request path (Bearer auth header), a single consistent typed
error envelope (`network` / `http` / `malformed`), and — in later M3-06
subtasks — an inherited idle/loading/error/empty/ready hook plus baseline
Loading / Error / Empty components. Consumers are `frontend/app`,
`frontend/landing` and `e2e`.

Transports report countable failures (5xx, network, timeout, malformed body) through
`@invoice-os/monitoring/report`; 4xx and caller cancels are not reported.

Ships as raw TypeScript source (no build step), following the same delivery
model as `packages/design-tokens`: `exports: { ".": "./src/index.ts" }`,
resolved directly by consumers via `moduleResolution: bundler`.

## Router decision

M3-06 introduces NO router; `frontend/app` keeps its `useState` view-switching.
A router is added only later, when a surface needs deep-linkable/bookmarkable
URLs.

## Usage

```ts
import { apiFetch, gatewayBase, ApiError } from '@invoice-os/api-client'

const base = gatewayBase() // trimmed VITE_GATEWAY_URL, or null when unset
if (base) {
  try {
    const me = await apiFetch<Me>(`${base}/api/tenancy/v1/me`, { token })
  } catch (err) {
    if (err instanceof ApiError) {
      // err.kind: 'network' | 'http' | 'malformed'
    }
  }
}
```

## Scripts

- `pnpm --filter @invoice-os/api-client test` — Vitest specs
- `pnpm --filter @invoice-os/api-client typecheck` — `tsc --noEmit`
