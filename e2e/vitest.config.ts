import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    environment: 'node',
    // This package's *.spec.ts files are Playwright suites (run via `playwright test`,
    // never vitest) — vitest's default include glob matches both *.test.ts and *.spec.ts,
    // which would make it try (and fail) to execute them. Unit tests here are *.test.ts
    // only, mirroring packages/api-client and frontend/app's convention.
    include: ['**/*.test.ts'],
  },
})
