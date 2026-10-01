import { readdirSync } from 'node:fs'
import { defineConfig, devices } from '@playwright/test'
import { partitionErrors, UNITS } from './topology/shards'

const specs = readdirSync(new URL('./topology', import.meta.url), { recursive: true })
  .map(String)
  .filter((f) => f.endsWith('.spec.ts'))
const partition = partitionErrors(specs)
if (partition.length > 0) throw new Error(`topology shard map: ${partition.join('; ')}`)

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
  // One worker per unit; CI runs one unit per job (topology/shards.ts). Without --project
  // every unit runs on this one worker.
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  // See the note in playwright.config.ts: a retry-pass reports green, so name it.
  reporter: process.env.CI
    ? [['list'], ['html', { open: 'never' }], ['./flakyReporter.ts', { label: 'topology' }]]
    : [['list'], ['./flakyReporter.ts', { label: 'topology' }]],
  use: {
    headless: true,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: UNITS.map((u) => ({
    name: u.name,
    testMatch: u.specs,
    use: { ...devices['Desktop Chrome'] },
  })),
})
