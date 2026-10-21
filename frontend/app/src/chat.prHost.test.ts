// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://app-pr-42.up.railway.app/" }
import { beforeEach, describe, expect, it, vi } from 'vitest'

import '../../library/src/testStorage'
import { CONSENT_STORAGE_KEY } from '../../landing/src/consent'
import { jarNames, resetJar, w } from '../../landing/src/hubspotChat.test.util'

beforeEach(() => {
  document.head.innerHTML = ''
  document.body.innerHTML = ''
  delete w._hsq
  localStorage.clear()
  resetJar()
  vi.resetModules()
})

describe('app chat on a PR host', () => {
  it('a PR app host loads no chat and pushes nothing after Accept', async () => {
    const { chooseAppConsent } = await import('./chat')
    chooseAppConsent('accept', 'ada@example.com')
    expect(document.head.querySelectorAll('script#hs-script-loader').length).toBe(0)
    expect(w._hsq).toBeUndefined()
    expect(localStorage.getItem(CONSENT_STORAGE_KEY)).toContain('"analytics":true')
    expect(document.cookie).not.toContain(CONSENT_STORAGE_KEY)
  })

  it('endAppChat on a PR host deletes host cookies', async () => {
    const { endAppChat } = await import('./chat')
    document.cookie = '__hstc=v; path=/'
    expect(jarNames()).toContain('__hstc')
    endAppChat()
    expect(jarNames()).not.toContain('__hstc')
  })

  it('Reject on a PR host deletes host cookies', async () => {
    const { chooseAppConsent } = await import('./chat')
    document.cookie = '__hstc=v; path=/'
    chooseAppConsent('reject', 'ada@example.com')
    expect(jarNames()).not.toContain('__hstc')
  })
})
