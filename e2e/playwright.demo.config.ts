import { defineConfig, devices } from '@playwright/test'

// M3-11 Day-30 wedge demo config (task-85). A fourth suite alongside the
// untouched smoke/api/topology configs. Like topology it drives the app SPA in
// a real browser, so it declares a chromium project (unlike the headless-only
// api config) — but like the api config it runs SERIAL (fullyParallel: false,
// workers: 1): the demo is ONE continuous journey that also kill-switches the
// shared, un-tenanted `vat-standard-rate` rule, so nothing may run beside it.
// There is no `webServer` — every step hits the deployed dev fleet (the seam
// api/client.ts + topology/targets.ts resolve GATEWAY_URL/APP_URL themselves),
// so `baseURL` is intentionally unset. Timeouts match topology's cold-fleet
// budget (the fleet can be starting from zero AND the beforeAll seeds ~25
// entities within it — see the story's Seeding-cost watch-item).
export default defineConfig({
  testDir: './demo',
  timeout: 60_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  workers: 1,
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
