import { defineConfig, devices } from '@playwright/test'

// M2-14 topology E2E config (task-23.4). Separate from the smoke config so the two
// suites run independently: smoke asserts each SPA renders; topology drives the live
// gateway round trip + cross-tenant isolation against the deployed dev fleet. Like the
// smoke config there is no `webServer` — every test hits a real deployed URL (see
// topology/targets.ts), and URLs come from env vars with live-dev defaults, so `baseURL`
// is intentionally unset. Timeouts are a touch longer than smoke's: the topology run
// brings the fleet up from zero, so a cold-started backend can be slow on first contact.
export default defineConfig({
  testDir: './topology',
  // Playwright suites are *.spec.ts; *.test.ts are vitest (see playwright.api.config.ts).
  testMatch: '**/*.spec.ts',
  timeout: 60_000,
  expect: { timeout: 15_000 },
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    headless: true,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
