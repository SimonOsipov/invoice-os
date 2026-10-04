import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { sentryVitePlugin } from '@sentry/vite-plugin'
import { sourcemapUploadOptions } from '../../packages/monitoring/src/upload'

// Support Console showcase. No dev proxy; the staff gate calls the gateway by its absolute URL,
// and all content is static mock data (src/data.tsx) — the cross-tenant read path it implies is M7, not this build.
export default defineConfig({
  plugins: [react(), sentryVitePlugin(sourcemapUploadOptions(fileURLToPath(new URL('.', import.meta.url))))],
  // Maps go to Sentry, never to browsers.
  build: { sourcemap: 'hidden' },
  // Favicons come from the design-tokens package so all four apps serve identical
  // bytes; this stands in for a local public/ dir, which none of them have.
  publicDir: fileURLToPath(new URL('../../packages/design-tokens/assets/favicon', import.meta.url)),
  server: {
    port: 5176,
  },
})
