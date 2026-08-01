import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// PLAT-01 — local-only platform app showcase. No proxy/backend; all content is static/hardcoded.
export default defineConfig({
  plugins: [react()],
  // Favicons come from the design-tokens package so all four apps serve identical
  // bytes; this stands in for a local public/ dir, which none of them have.
  publicDir: fileURLToPath(new URL('../../packages/design-tokens/assets/favicon', import.meta.url)),
  server: {
    port: 5174,
  },
})
