import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { checkSavedState } from './browserSession'

const launch = vi.hoisted(() => vi.fn(async () => Promise.reject(new Error('the saved-state check booted a browser'))))
const me = vi.hoisted(() => vi.fn())
vi.mock('@playwright/test', () => ({ chromium: { launch } }))
vi.mock('../api/client', () => ({ me }))
vi.mock('../staffSession', () => ({ CONSOLE_SESSION_KEY: { ops: 'invoice-os.ops-session', support: 'invoice-os.support-session' } }))

const urls = { GATEWAY_URL: 'https://api.test', APP_URL: 'https://app.test', LANDING_URL: 'https://landing.test', OPS_CONSOLE_URL: 'https://ops.test', SUPPORT_CONSOLE_URL: 'https://support.test' }
const TENANT = '11111111-1111-1111-1111-111111111111'
const jwt = (claims: Record<string, unknown>) => `h.${Buffer.from(JSON.stringify(claims)).toString('base64url')}.s`
const now = () => Math.floor(Date.now() / 1000)
const inAnHour = () => now() + 3600
// A fresh one-hour app token: issued just now, so far from its renewal.
const live = (claims: Record<string, unknown> = {}) => jwt({ iat: now(), exp: inAnHour(), ...claims })
const cookie = (audience: string) => ({ name: `__Host-rt-${audience}`, domain: 'api.test', path: '/', httpOnly: true, secure: true })
const account = { email: 'a@example.com', password: 'p', tenantId: TENANT }

let dir: string
beforeEach(() => {
  dir = mkdtempSync(path.join(tmpdir(), 'ctl-state-'))
  launch.mockClear()
  me.mockReset()
})
afterEach(() => rmSync(dir, { recursive: true, force: true }))

function state(origin: string, key: string, record: unknown, cookies: object[] = []): string {
  const file = path.join(dir, 'state.json')
  const value = typeof record === 'string' ? record : JSON.stringify(record)
  writeFileSync(file, JSON.stringify({ cookies, origins: [{ origin, localStorage: [{ name: key, value }] }] }))
  return file
}
const appState = (token: string, cookies: object[] = [cookie('app')]) => state(urls.APP_URL, 'invoice-os.session', { token, handoff: true }, cookies)

describe('checkSavedState reads the saved access token and never boots the app', () => {
  it('a live app token with the expected tenant is reused, role from the API', async () => {
    const token = live()
    me.mockResolvedValue({ tenant: { id: TENANT }, user: { role: 'reviewer' } })

    const out = await checkSavedState('firm-reviewer', account, urls, appState(token))

    expect(out).toEqual({ ok: true, role: 'reviewer' })
    expect(me).toHaveBeenCalledWith(token)
    expect(launch, 'booting the app presents the saved refresh token to GoTrue').not.toHaveBeenCalled()
  })

  it('an expired token is stale without a request', async () => {
    const out = await checkSavedState('firm-admin', account, urls, appState(jwt({ iat: now() - 3590, exp: now() + 10 })))

    expect(out).toEqual({ ok: false })
    expect(me).not.toHaveBeenCalled()
    expect(launch).not.toHaveBeenCalled()
  })

  it('a refused API read, a tenant mismatch and an unreadable file are stale', async () => {
    me.mockRejectedValueOnce(new Error('401'))
    expect(await checkSavedState('firm-admin', account, urls, appState(live()))).toEqual({ ok: false })

    me.mockResolvedValueOnce({ tenant: { id: 'other' }, user: { role: 'admin' } })
    expect(await checkSavedState('firm-admin', account, urls, appState(live()))).toEqual({ ok: false })

    expect(await checkSavedState('firm-admin', account, urls, path.join(dir, 'missing.json'))).toEqual({ ok: false })
    me.mockResolvedValue({ tenant: { id: TENANT }, user: { role: 'admin' } })
    expect(await checkSavedState('firm-admin', account, urls, state('https://other.test', 'invoice-os.session', { token: live() }, [cookie('app')]))).toEqual({ ok: false })
    expect(await checkSavedState('firm-admin', account, urls, state(urls.APP_URL, 'invoice-os.session', 'not json', [cookie('app')]))).toEqual({ ok: false })
    expect(launch).not.toHaveBeenCalled()
  })

  it('a state without the audience cookie is stale', async () => {
    me.mockResolvedValue({ tenant: { id: TENANT }, user: { role: 'admin' } })
    expect(await checkSavedState('firm-admin', account, urls, appState(live(), []))).toEqual({ ok: false })
    expect(await checkSavedState('firm-admin', account, urls, appState(live(), [cookie('ops')]))).toEqual({ ok: false })
    expect(await checkSavedState('firm-admin', account, urls, appState(live(), [{ ...cookie('app'), domain: 'other.test' }]))).toEqual({ ok: false })
    expect(await checkSavedState('firm-admin', account, urls, appState(live(), [cookie('app')]))).toEqual({ ok: true, role: 'admin' })
  })

  it('a console state is never reused', async () => {
    const staff = live({ app_metadata: { staff: true } })
    const file = state(urls.OPS_CONSOLE_URL, 'invoice-os.ops-session', { v: 3, token: staff }, [cookie('ops')])
    expect(await checkSavedState('developer', account, urls, file)).toEqual({ ok: false })
    expect(launch, 'booting a console renews with the saved cookie').not.toHaveBeenCalled()
  })

  it('an app state whose token is due is stale', async () => {
    me.mockResolvedValue({ tenant: { id: TENANT }, user: { role: 'admin' } })
    expect(await checkSavedState('firm-admin', account, urls, appState(jwt({ iat: now() - 3000, exp: now() + 600 })))).toEqual({ ok: false })
    expect(await checkSavedState('firm-admin', account, urls, appState(jwt({ iat: now() - 300, exp: now() + 3300 })))).toEqual({ ok: true, role: 'admin' })
  })
})
