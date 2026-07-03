import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// PLAT-01 — local-only platform app showcase. No proxy/backend; all content is static/hardcoded.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5174,
  },
})
