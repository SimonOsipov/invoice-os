import { defineConfig } from '@playwright/test'

// M3-14 API E2E config (task-74). Separate from smoke/topology so all three
// suites run independently: smoke asserts each SPA renders, topology drives a
// browser round trip against the live gateway, and this one is a headless
// typed HTTP contract suite over the same deployed gateway — no browser at
// all. Like the topology config there is no `webServer` — every test hits a
// real deployed URL — and `baseURL` is intentionally unset; the seam
// (api/client.ts) resolves GATEWAY_URL itself, mirroring topology/targets.ts.
// Timeouts match topology's cold-fleet values (the fleet can be starting from
// zero). Unlike topology, this suite runs SERIAL (fullyParallel: false,
// workers: 1): the kill-switch spec (M3-14-03) mutates the GLOBAL `rules`
// table and every spec shares one deployed DB, so parallel workers would race
// (a concurrent validate observing a mid-toggle rule, or entity-namespace
// contention) — see the story's Decision A8.
export default defineConfig({
  testDir: './api',
  // Playwright's default testMatch also matches *.test.ts, which are this package's
  // vitest unit tests (see vitest.config.ts for the mirror-image exclusion). Collecting
  // one aborts the ENTIRE run — "0 tests in 0 files", reported as a suite failure —
  // so restrict Playwright to *.spec.ts.
  testMatch: '**/*.spec.ts',
  timeout: 60_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    headless: true,
  },
  projects: [{ name: 'api' }],
})
