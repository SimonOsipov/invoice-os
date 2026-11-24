import { afterEach, describe, expect, it, vi } from 'vitest'

import type { Fetch } from '../mail/resendClient'
import { consentUrlProblems, intuitLoginEnv, intuitSignInCode, pickIntuitCodeMail, waitForIntuitCode } from './intuitLogin'

const GW = 'https://gw.example.test'
const NAMES = ['INTUIT_LOGIN_EMAIL', 'INTUIT_LOGIN_PASSWORD', 'RESEND_READ_KEY']

afterEach(() => {
  vi.unstubAllEnvs()
  vi.useRealTimers()
})

describe('intuitLoginEnv', () => {
  it('names each missing variable and no set value', () => {
    for (const missing of NAMES) {
      for (const n of NAMES) vi.stubEnv(n, n === missing ? '' : `value-of-${n}`)
      let msg = ''
      try {
        intuitLoginEnv()
      } catch (e) {
        msg = (e as Error).message
      }
      expect(msg).toBe(`environment: not set: ${missing}`)
    }
  })
})

const good = () => {
  const u = new URL('https://appcenter.intuit.com/connect/oauth2')
  u.search = new URLSearchParams({
    client_id: 'cid',
    redirect_uri: `${GW}/erp/callback`,
    response_type: 'code',
    scope: 'com.intuit.quickbooks.accounting',
    state: 'st',
  }).toString()
  return u
}

describe('consentUrlProblems', () => {
  it('accepts the Intuit URL', () => {
    expect(consentUrlProblems(good().toString(), GW)).toEqual([])
  })

  it('names each fault', () => {
    const edit = (f: (u: URL) => void) => {
      const u = good()
      f(u)
      return u.toString()
    }
    const rows: [string, string, string][] = [
      ['host', edit((u) => (u.host = 'example.com')), 'host'],
      ['path', edit((u) => (u.pathname = '/connect')), 'path'],
      ['response_type', edit((u) => u.searchParams.set('response_type', 'token')), 'response_type'],
      ['scope', edit((u) => u.searchParams.delete('scope')), 'scope'],
      ['redirect', edit((u) => u.searchParams.set('redirect_uri', 'https://other.example.test/erp/callback')), 'redirect_uri'],
      ['state', edit((u) => u.searchParams.set('state', '')), 'state'],
      ['client_id', edit((u) => u.searchParams.set('client_id', '')), 'client_id'],
    ]
    for (const [label, url, part] of rows) {
      const got = consentUrlProblems(url, GW)
      expect(got, label).toHaveLength(1)
      expect(got[0], label).toContain(part)
    }
  })
})

describe('intuitSignInCode', () => {
  it('reads the first standalone six digits', () => {
    expect(intuitSignInCode('Your Intuit code is 482913.')).toBe('482913')
    expect(intuitSignInCode('no code here')).toBeNull()
    expect(intuitSignInCode('1234567')).toBeNull()
    expect(intuitSignInCode('ref 12 code 482913')).toBe('482913')
    expect(intuitSignInCode(null)).toBeNull()
    // An HTML colour is not a code.
    expect(intuitSignInCode('<td style="color:#333333">code 482913</td>')).toBe('482913')
  })
})

const TO = 'Login@Nightly.example.test'
const SINCE = new Date('2026-11-24T10:00:00Z')
const at = (s: string) => `2026-11-24 ${s}.000000+00`
const mail = (id: string, created: string, to = TO) => ({ id, to: [to], subject: 'x', created_at: at(created) })

describe('pickIntuitCodeMail', () => {
  it('takes only the newest mail for the address at or after since', () => {
    expect(pickIntuitCodeMail([mail('a', '09:59:59')], TO, SINCE)).toBeNull()
    expect(pickIntuitCodeMail([mail('a', '10:00:01')], TO, SINCE)?.id).toBe('a')
    expect(pickIntuitCodeMail([mail('a', '10:00:01'), mail('b', '10:00:09')], TO, SINCE)?.id).toBe('b')
    expect(pickIntuitCodeMail([mail('a', '10:00:01', 'other@x.test')], TO, SINCE)).toBeNull()
    expect(pickIntuitCodeMail([mail('a', '10:00:01', TO.toLowerCase())], TO.toUpperCase(), SINCE)?.id).toBe('a')
  })
})

const json = (b: unknown) => new Response(JSON.stringify(b))

// Serves the list at /emails/receiving and each body at /emails/receiving/<id>.
function stub(list: () => ReturnType<typeof mail>[], bodies: Record<string, string>): Fetch {
  return (async (url: string) => {
    const m = /\/emails\/receiving\/(.+)$/.exec(url)
    if (m) return json({ ...mail(m[1], '10:00:05'), text: bodies[m[1]] ?? null, html: null })
    return json({ data: list(), has_more: false })
  }) as unknown as Fetch
}

describe('waitForIntuitCode', () => {
  it('reads the newest post-since code', async () => {
    const f = stub(() => [mail('new', '10:00:30'), mail('old', '09:59:00')], { new: 'code 222222', old: 'code 111111' })
    expect(await waitForIntuitCode('k', TO, SINCE, 60_000, f)).toBe('222222')
  })

  it('skips a mail with no code and takes a later one', async () => {
    let list = [mail('m1', '10:00:10')]
    const f = stub(() => list, { m1: 'welcome', m2: 'your code is 333333' })
    const p = waitForIntuitCode('k', TO, SINCE, 60_000, f)
    list = [mail('m2', '10:00:20'), mail('m1', '10:00:10')]
    expect(await p).toBe('333333')
  })

  it('times out with an error that holds no address', async () => {
    vi.useFakeTimers()
    const f = stub(() => [], {})
    const p = waitForIntuitCode('k', TO, SINCE, 10_000, f).catch((e: Error) => e)
    await vi.advanceTimersByTimeAsync(20_000)
    const err = (await p) as Error
    expect(err.message).toMatch(/no Intuit sign-in code/)
    expect(err.message.toLowerCase()).not.toContain('nightly.example.test')
  })
})
