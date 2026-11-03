import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    environment: 'node',
    env: { VITE_HUBSPOT_PORTAL_ID: '1234567' },
    setupFiles: ['./src/consentCookie.test.util.ts'],
  },
})
