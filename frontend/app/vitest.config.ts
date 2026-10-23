import { defineConfig } from 'vitest/config'
// ceiling: Library slicer imported, move it into landing after CHAT-02 merges
import { cookieNoticeDefine } from '../library/src/cookieNoticeCss'

export default defineConfig({
  define: cookieNoticeDefine(),
  test: {
    environment: 'node',
    setupFiles: ['../landing/src/consentCookie.test.util.ts'],
  },
})
