import { defineConfig } from 'vitest/config'
import { cookieNoticeDefine } from './src/cookieNoticeCss'

export default defineConfig({
  define: cookieNoticeDefine(),
  test: {
    environment: 'node',
    setupFiles: ['../landing/src/consentCookie.test.util.ts'],
  },
})
