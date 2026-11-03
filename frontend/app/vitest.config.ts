import { defineConfig } from 'vitest/config'
// ceiling: Library slicer imported, move it into landing after CHAT-02 merges
import { cookieNoticeDefine } from '../library/src/cookieNoticeCss'

export default defineConfig({
  define: cookieNoticeDefine(),
  test: {
    environment: 'node',
    env: { VITE_HUBSPOT_PORTAL_ID: '1234567' },
    setupFiles: ['../landing/src/consentCookie.test.util.ts'],
  },
})
