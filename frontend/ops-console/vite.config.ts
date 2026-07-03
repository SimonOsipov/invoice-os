import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// OPS-01 — local-only ops console showcase. No proxy/backend; all content is static.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5175,
  },
})
