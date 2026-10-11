// Landing side of the confirm bounce; the gateway's exact Location is pinned by TestVerify_ClickWithStateRedirectsWithACode.
import { afterEach, describe, expect, it, vi } from 'vitest'

const APP = 'https://app.t'
const CODE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'

// The module acts when it evaluates, so each load gets its own fake page.
async function load(url: string, env: string | null = APP) {
  vi.resetModules()
  if (env) vi.stubEnv('VITE_APP_URL', env)
  else vi.stubEnv('VITE_APP_URL', '')
  const at = new URL(url, 'https://www.ascomply.com')
  const calls: string[] = []
  vi.stubGlobal('location', { pathname: at.pathname, search: at.search, hash: at.hash, replace: vi.fn((u: string) => calls.push(`replace ${u}`)) })
  vi.stubGlobal('history', { replaceState: vi.fn((_s: unknown, _t: string, u: string) => calls.push(`strip ${u}`)) })
  const mod = await import('./verifyLink')
  return { ...mod, calls }
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

const loc = (search: string, hash = '') => ({ pathname: '/', search, hash })

describe('verifyForward', () => {
  it('verifyForward_confirmGoesToTheAppWithTheToken', async () => {
    const { verifyForward } = await load('/')
    const r = verifyForward(loc('?confirm=1&keep=1', '#token=abc_-1'), APP)
    expect(r).toEqual({ strip: '/?keep=1', target: 'https://app.t?auth=verify#token=abc_-1', failed: false })
    expect(verifyForward(loc('?confirm=1', '#token=abc_-1'), APP).strip).toBe('/')
  })

  it('verifyForward_inviteConfirmGoesToTheAppWithTheToken', async () => {
    const { verifyForward } = await load('/')
    const r = verifyForward(loc('?confirm=invite', '#token=abc_-1'), APP)
    expect(r).toEqual({ strip: '/', target: 'https://app.t?auth=verify-invite#token=abc_-1', failed: false })
  })

  it('verifyForward_plainConfirmIsUnchanged', async () => {
    const { verifyForward } = await load('/')
    expect(verifyForward(loc('?confirm=1', '#token=T'), APP).target).toBe('https://app.t?auth=verify#token=T')
  })

  it('verifyForward_otherConfirmValuesAreFailed', async () => {
    const { verifyForward } = await load('/')
    for (const s of ['?confirm=2', '?confirm=Invite', '?confirm=', '?confirm=invite&confirm=1', '?confirm=invite&confirm=invite']) {
      const r = verifyForward(loc(s, '#token=abc'), APP)
      expect(r, s).toEqual({ strip: '/?verify=failed', target: null, failed: true })
    }
  })

  it('verifyForward_inviteWithBadTokenOrNoAppIsFailed', async () => {
    const { verifyForward } = await load('/')
    for (const h of ['#token=a b', `#token=${'a'.repeat(257)}`, '#token=abc&token=def']) {
      expect(verifyForward(loc('?confirm=invite', h), APP), h).toEqual({ strip: '/?verify=failed', target: null, failed: true })
    }
    expect(verifyForward(loc('?confirm=invite', '#token=abc'), null)).toEqual({ strip: '/?verify=failed', target: null, failed: true })
  })

  it('verifyForward_handoffGoesToTheApp', async () => {
    const { verifyForward } = await load('/')
    expect(verifyForward(loc(`?verified=1&handoff=${CODE}`), APP)).toEqual({ strip: '/?verified=1', target: `https://app.t?handoff=${CODE}`, failed: false })
  })

  it('verifyForward_badTokenOrNoAppIsFailed', async () => {
    const { verifyForward } = await load('/')
    const bad: [string, string][] = [
      ['?confirm=1', '#token=a b'],
      ['?confirm=1', `#token=${'a'.repeat(257)}`],
      ['?confirm=1', '#token=abc&token=def'],
      ['?confirm=1&confirm=1', '#token=abc'],
      ['?confirm=1', ''],
      ['?confirm=1', '#token='],
    ]
    for (const [s, h] of bad) {
      expect(verifyForward(loc(s, h), APP), s + h).toEqual({ strip: '/?verify=failed', target: null, failed: true })
    }
    expect(verifyForward(loc('?confirm=1', '#token=abc'), null)).toEqual({ strip: '/?verify=failed', target: null, failed: true })
    // Control: the same link with a good token and an app goes through.
    expect(verifyForward(loc('?confirm=1', '#token=abc'), APP).target).not.toBeNull()
  })

  it('verifyForward_badCodeKeepsTheVerifiedNotice', async () => {
    const { verifyForward } = await load('/')
    for (const s of [`?verified=1&handoff=${CODE.slice(1)}`, `?verified=1&handoff=${CODE}&handoff=${CODE}`]) {
      expect(verifyForward(loc(s), APP), s).toEqual({ strip: '/?verified=1', target: null, failed: false })
    }
    expect(verifyForward(loc(`?verified=1&handoff=${CODE}`), null)).toEqual({ strip: '/?verified=1', target: null, failed: false })
    for (const [s, strip] of [[`?handoff=${CODE}`, '/'], [`?verified=0&handoff=${CODE}`, '/?verified=0']]) {
      expect(verifyForward(loc(s), APP), s).toEqual({ strip, target: null, failed: false })
    }
  })

  it('leaves every other URL alone', async () => {
    const { verifyForward } = await load('/')
    expect(verifyForward(loc('?a=1', '#faq'), APP)).toEqual({ strip: '/?a=1#faq', target: null, failed: false })
  })
})

describe('verifyLink module', () => {
  it('verifyLink_stripsBeforeItNavigates', async () => {
    const { forwarding, calls } = await load('/?confirm=1#token=abc')
    expect(forwarding).toBe(true)
    expect(calls).toEqual(['strip /', 'replace https://app.t?auth=verify#token=abc'])
  })

  it('does not forward without an app URL and shows the failed notice', async () => {
    const { forwarding, calls } = await load('/?confirm=1#token=abc', null)
    expect(forwarding).toBe(false)
    expect(calls).toEqual(['strip /?verify=failed'])
  })
})
