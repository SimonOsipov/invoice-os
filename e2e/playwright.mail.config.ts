import { defineConfig } from '@playwright/test'

// Sends real mail and holds tokens: no retry (a retry sends more), nothing recorded. The result is the section file.
export default defineConfig({
  testDir: './mail',
  // Keeps the vitest files in ./mail out of the run.
  testMatch: '**/*.spec.ts',
  timeout: 120_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: [['list']],
  use: {
    headless: true,
    trace: 'off',
    video: 'off',
    screenshot: 'off',
  },
  projects: [{ name: 'mail' }],
})
