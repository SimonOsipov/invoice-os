import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// LAND-01 — local-only landing showcase. No proxy/backend; all content is static.
export default defineConfig({
  plugins: [react()],
  // Favicons come from the design-tokens package so all four apps serve identical
  // bytes; this stands in for a local public/ dir, which none of them have.
  publicDir: fileURLToPath(new URL('../../packages/design-tokens/assets/favicon', import.meta.url)),
  server: {
    port: 5173,
  },
})
