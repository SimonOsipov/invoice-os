import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    environment: 'node',
    // The App renders nothing without a landing URL; tests that need it unset stub it.
    env: { VITE_LANDING_URL: 'https://land.example' },
  },
})
