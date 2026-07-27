import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Support Console showcase. No proxy/backend; all content is static mock data
// (src/data.tsx) — the cross-tenant read path it implies is M7, not this build.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5176,
  },
})
