import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { sentryVitePlugin } from '@sentry/vite-plugin'
import { sourcemapUploadOptions } from '../../packages/monitoring/src/upload'

export default defineConfig({
  plugins: [react(), sentryVitePlugin(sourcemapUploadOptions(fileURLToPath(new URL('.', import.meta.url))))],
  // Maps go to Sentry, never to browsers.
  build: { sourcemap: 'hidden' },
  // Favicons come from the design-tokens package, as in the other SPAs.
  publicDir: fileURLToPath(new URL('../../packages/design-tokens/assets/favicon', import.meta.url)),
  server: {
    port: 5177,
  },
})
