// @vitest-environment jsdom
// The in-force usage fetch in App.tsx: a publish re-reads it without touching the roles fetch,
// an older answer cannot overwrite a newer one, and zero roles stay 'empty'.
// Harness: App.membersMirror.test.tsx (real <App/>, stubbed gateway, ctx captured through Sidebar).
import { act, cleanup, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import type { Role, RolesInForce } from './lib/roles'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

const GATEWAY = 'https://gw.test'

let capturedCtx: PlatformCtx | undefined
// Every render of the shell, so "never loading" covers the in-between renders too.
let seen: Array<{ rolesState: PlatformCtx['rolesState']; rolesInForce: RolesInForce | null; roles: Role[] }> = []
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
    seen.push({ rolesState: p.ctx.rolesState, rolesInForce: p.ctx.rolesInForce, roles: p.ctx.roles })
    return null
  },
}))

function createMemoryStorage() {
  const store = new Map<string, string>()
  return {
    getItem: vi.fn((key: string) => (store.has(key) ? (store.get(key) as string) : null)),
    setItem: vi.fn((key: string, value: string) => {
      store.set(key, value)
    }),
    removeItem: vi.fn((key: string) => {
      store.delete(key)
    }),
    clear: vi.fn(() => {
      store.clear()
    }),
  }
}

const cfo: Role = { key: 'cfo', title: 'CFO', desc: 'D', members: [] }
const usage = (name: string): RolesInForce => ({ cfo: [{ id: 'p1', name, steps: 1, scope: false }] })

// One held answer per workflow-roles GET, in call order.
type Held = { resolve: (body: unknown) => void; reject: (err: Error) => void }
let roleGets: Held[] = []

function stubGateway() {
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: { method?: string }) => {
      const reply = (b: unknown) => ({ ok: true, status: 200, statusText: 'OK', json: () => Promise.resolve(b) })
      if (url.endsWith('/publish') && init?.method === 'POST') {
        return Promise.resolve(
          reply({ id: 'p1', name: 'P', scope: '', position: 0, scopes: {}, status: 'published', version: 1, sealed: true, steps: [], versions: [] }),
        )
      }
      if (url.includes('/workflow-roles')) {
        return new Promise((resolve, reject) => roleGets.push({ resolve: (b) => resolve(reply(b)), reject }))
      }
      return Promise.resolve(reply({ policies: [], roles: [], invoices: [], clients: [], entities: [], memberships: [], pagination: { limit: 50, offset: 0, total: 0 } }))
    }),
  )
}

async function boot(roles: Role[], inForce: RolesInForce) {
  window.history.replaceState(null, '', '/')
  const session: Session = { persona: APP_PERSONAS.firm, token: 'tok', me: null, verified: true }
  localStorage.setItem(SESSION_KEY, serializeSession(session))
  stubGateway()
  vi.resetModules()
  const { default: App } = await import('./App')
  await act(async () => {
    render(<App />)
  })
  // Boot issues the roles GET and the in-force GET; both read the same body.
  await waitFor(() => expect(roleGets.length).toBe(2))
  await act(async () => roleGets.forEach((g) => g.resolve({ workflow_roles: roles, in_force: inForce })))
}

const ctx = () => capturedCtx!
const answer = (call: number, body: unknown) => act(async () => roleGets[call].resolve(body))
const publish = () => act(async () => void (await ctx().publishPolicy('p1')))

beforeEach(() => {
  capturedCtx = undefined
  seen = []
  roleGets = []
  vi.stubGlobal('localStorage', createMemoryStorage())
  window.history.replaceState(null, '', '/')
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('App: the in-force usage', () => {
  it('a publish re-reads in-force usage without a roles loading state', async () => {
    await boot([cfo], usage('before'))
    expect(ctx().rolesInForce).toEqual(usage('before'))
    seen = []

    await publish()

    expect(roleGets.length, 'a publish did not re-read the usage').toBe(3)
    expect(ctx().rolesInForce, 'the count blanked while the re-run loaded').toEqual(usage('before'))
    await answer(2, { workflow_roles: [cfo], in_force: usage('after') })

    expect(ctx().rolesInForce).toEqual(usage('after'))
    expect(seen.length, 'nothing rendered after the publish, so the loop below is vacuous').toBeGreaterThan(0)
    for (const s of seen) {
      expect(s.rolesState, 'the builder would render <Loading /> mid-publish').toBe('ready')
      expect(s.roles, 'the roles mirror blanked mid-publish').toEqual([cfo])
    }
  })

  it('a policy delete re-reads in-force usage too', async () => {
    await boot([cfo], usage('before'))

    await act(async () => void (await ctx().deletePolicy('p1')))

    expect(roleGets.length, 'a policy delete did not re-read the usage').toBe(3)
    expect(ctx().rolesInForce, 'the count blanked while the re-run loaded').toEqual(usage('before'))
    await answer(2, { workflow_roles: [cfo], in_force: {} })
    expect(ctx().rolesInForce).toEqual({})
    expect(ctx().rolesState).toBe('ready')
  })

  it('a failed re-read leaves null, not the stale count', async () => {
    await boot([cfo], usage('before'))

    await publish()
    await act(async () => roleGets[2].reject(new Error('gateway down')))

    expect(ctx().rolesInForce).toBeNull()
    expect(ctx().rolesState).toBe('ready')
    expect(ctx().roles).toEqual([cfo])
  })

  it('an older in-force response cannot overwrite a newer one', async () => {
    await boot([cfo], usage('boot'))

    await publish()
    await publish()
    expect(roleGets.length).toBe(4)

    await answer(3, { workflow_roles: [cfo], in_force: usage('second') })
    await answer(2, { workflow_roles: [cfo], in_force: usage('first') })

    expect(ctx().rolesInForce).toEqual(usage('second'))
  })

  it('the previous count survives every render of a reload, and is replaced by the new answer', async () => {
    await boot([cfo], usage('before'))
    seen = []

    await publish()
    expect(seen.length, 'nothing rendered during the reload, so the loop is vacuous').toBeGreaterThan(0)
    for (const s of seen) expect(s.rolesInForce).toEqual(usage('before'))
    await answer(2, { workflow_roles: [cfo], in_force: usage('after') })

    expect(ctx().rolesInForce).toEqual(usage('after'))
  })

  it('a failed first read is null, and refetchRolesInForce then lands the count', async () => {
    window.history.replaceState(null, '', '/')
    const session: Session = { persona: APP_PERSONAS.firm, token: 'tok', me: null, verified: true }
    localStorage.setItem(SESSION_KEY, serializeSession(session))
    stubGateway()
    vi.resetModules()
    const { default: App } = await import('./App')
    await act(async () => {
      render(<App />)
    })
    await waitFor(() => expect(roleGets.length).toBe(2))
    await act(async () => {
      roleGets[0].resolve({ workflow_roles: [cfo] })
      roleGets[1].reject(new Error('gateway down'))
    })
    expect(ctx().rolesInForce).toBeNull()

    await act(async () => ctx().refetchRolesInForce())
    expect(roleGets.length).toBe(3)
    await answer(2, { workflow_roles: [cfo], in_force: usage('late') })

    expect(ctx().rolesInForce).toEqual(usage('late'))
    expect(ctx().rolesState).toBe('ready')
  })

  it('zero roles stay empty', async () => {
    await boot([], {})

    expect(ctx().rolesState).toBe('empty')
    expect(ctx().rolesInForce).toEqual({})
  })
})
