import { defineConfig, devices } from '@playwright/test'

// Smoke suite for the three deployed SPAs. There is no local web server — every
// test hits a real deployed URL (see smoke/apps.ts), so this config has no
// `webServer` block. URLs are resolved per-app from env vars with live-dev
// defaults, which is why `baseURL` is intentionally left unset.
export default defineConfig({
  testDir: './smoke',
  // Playwright suites are *.spec.ts; *.test.ts are vitest (see playwright.api.config.ts).
  testMatch: '**/*.spec.ts',
  timeout: 30_000,
  expect: { timeout: 10_000 },
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
