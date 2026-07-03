import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// LAND-01 — local-only landing showcase. No proxy/backend; all content is static.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
  },
})
