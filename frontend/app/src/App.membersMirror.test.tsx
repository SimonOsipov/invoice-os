// @vitest-environment jsdom
// The members mirror in App.tsx: a refetch keeps the landed roster until new data lands.
// Harness mirrors App.addCompanyTask.test.tsx: the real <App/>, a stubbed gateway, ctx captured through a mocked Sidebar.
import { act, cleanup, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import type { MembershipWire } from './lib/members'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

const GATEWAY = 'https://gw.test'

let capturedCtx: PlatformCtx | undefined
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
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

const wire = (id: string, over: Partial<MembershipWire> = {}): MembershipWire => ({
  user_id: id,
  role: 'admin',
  status: 'active',
  display_name: `Member ${id}`,
  email: `${id}@x.ng`,
  ...over,
})

// One pending answer per memberships GET, in call order.
let membershipReplies: Array<(list: MembershipWire[]) => void> = []
let membershipCalls = 0
// The PUT .../clients answer: a row to return, or a status to refuse with.
let clientsPut: { calls: Array<{ url: string; body: unknown }>; answer: () => { status: number; body: unknown } } = { calls: [], answer: () => ({ status: 200, body: {} }) }

// The POST .../remove calls and answer, and the count of workflow-roles GETs.
let removePost: { calls: string[]; answer: () => { status: number; body: unknown } } = { calls: [], answer: () => ({ status: 200, body: {} }) }
let rolesGets = 0

function stubGateway() {
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: { body?: string }) => {
      const reply = (b: unknown) => ({ ok: true, status: 200, statusText: 'OK', json: () => Promise.resolve(b) })
      if (url.endsWith('/clients')) {
        clientsPut.calls.push({ url, body: init?.body ? JSON.parse(init.body) : undefined })
        const { status, body } = clientsPut.answer()
        return Promise.resolve({ ok: status < 400, status, statusText: '', json: () => Promise.resolve(body) })
      }
      if (url.endsWith('/remove')) {
        removePost.calls.push(url)
        const { status, body } = removePost.answer()
        return Promise.resolve({ ok: status < 400, status, statusText: '', json: () => Promise.resolve(body) })
      }
      if (url.includes('/workflow-roles')) rolesGets++
      if (url.includes('/api/tenancy/v1/memberships')) {
        membershipCalls++
        return new Promise((resolve) => membershipReplies.push((list) => resolve(reply({ memberships: list }))))
      }
      return Promise.resolve(reply({ policies: [], roles: [], invoices: [], clients: [], entities: [], pagination: { limit: 50, offset: 0, total: 0 } }))
    }),
  )
}

async function boot() {
  window.history.replaceState(null, '', '/')
  const session: Session = { persona: APP_PERSONAS.firm, token: 'tok', me: null, verified: true }
  localStorage.setItem(SESSION_KEY, serializeSession(session))
  stubGateway()
  vi.resetModules()
  const { default: App } = await import('./App')
  await act(async () => {
    render(<App />)
  })
}

const ctx = () => capturedCtx!
const answer = (call: number, list: MembershipWire[]) => act(async () => membershipReplies[call](list))

beforeEach(() => {
  capturedCtx = undefined
  membershipReplies = []
  membershipCalls = 0
  clientsPut = { calls: [], answer: () => ({ status: 200, body: {} }) }
  removePost = { calls: [], answer: () => ({ status: 200, body: {} }) }
  rolesGets = 0
  vi.stubGlobal('localStorage', createMemoryStorage())
  window.history.replaceState(null, '', '/')
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('App: the members mirror', () => {
  it('App: a members refetch keeps the landed roster while it loads, then takes the new list', async () => {
    await boot()
    await waitFor(() => expect(membershipCalls).toBe(1))
    expect(ctx().members, 'control: nothing landed yet').toEqual([])

    await answer(0, [wire('u1'), wire('u2')])
    expect(ctx().membersState).toBe('ready')
    expect(ctx().members.map((m) => m.id)).toEqual(['u1', 'u2'])

    await act(async () => ctx().refetchMembers())
    expect(membershipCalls).toBe(2)
    expect(ctx().membersState, 'the refetch is in flight').toBe('loading')
    expect(ctx().members.map((m) => m.id), 'a refetch never blanks the landed roster').toEqual(['u1', 'u2'])

    await answer(1, [wire('u1'), wire('u3')])
    expect(ctx().membersState).toBe('ready')
    expect(ctx().members.map((m) => m.id), 'the fetch stays authoritative').toEqual(['u1', 'u3'])
  })

  it('App: a refetch that answers an empty roster clears the mirror', async () => {
    await boot()
    await waitFor(() => expect(membershipCalls).toBe(1))
    await answer(0, [wire('u1')])
    expect(ctx().members).toHaveLength(1)

    await act(async () => ctx().refetchMembers())
    expect(ctx().members, 'kept while loading').toHaveLength(1)

    await answer(1, [])
    expect(ctx().membersState).toBe('empty')
    expect(ctx().members, 'an emptied tenant leaves no ghost rows').toEqual([])
  })

  it('App: setMemberClients puts the whole access and the mirror takes the server answer; a refusal leaves it', async () => {
    await boot()
    await waitFor(() => expect(membershipCalls).toBe(1))
    await answer(0, [wire('u1'), wire('u2', { role: 'preparer', client_access: ['e1'] })])
    expect(ctx().members.find((m) => m.id === 'u2')?.clientAccess).toEqual(['e1'])

    clientsPut.answer = () => ({ status: 200, body: wire('u2', { role: 'preparer', client_access: ['e1', 'e2'] }) })
    await act(async () => ctx().setMemberClients('u2', ['e1', 'e2']))
    expect(clientsPut.calls).toHaveLength(1)
    expect(clientsPut.calls[0].url).toBe(`${GATEWAY}/api/tenancy/v1/memberships/u2/clients`)
    expect(clientsPut.calls[0].body).toEqual({ client_access: ['e1', 'e2'] })
    expect(ctx().members.find((m) => m.id === 'u2')?.clientAccess).toEqual(['e1', 'e2'])
    expect(ctx().members.find((m) => m.id === 'u1')?.clientAccess, 'the other row is untouched').toBeNull()

    clientsPut.answer = () => ({ status: 403, body: { error: "only an admin can choose a member's clients" } })
    await act(async () => {
      await expect(ctx().setMemberClients('u2', 'all')).rejects.toBeTruthy()
    })
    expect(ctx().members.find((m) => m.id === 'u2')?.clientAccess, 'a refusal rolls nothing in').toEqual(['e1', 'e2'])
  })

  it('App: removeMember posts the remove, the mirror takes the removed row and roles refetch; a refusal leaves both', async () => {
    await boot()
    await waitFor(() => expect(membershipCalls).toBe(1))
    await answer(0, [wire('u1'), wire('u2', { role: 'preparer' })])
    await waitFor(() => expect(rolesGets).toBeGreaterThan(0))
    const before = rolesGets

    removePost.answer = () => ({ status: 409, body: { error: 'nope' } })
    await act(async () => {
      await expect(ctx().removeMember('u2')).rejects.toBeTruthy()
    })
    expect(ctx().members.find((m) => m.id === 'u2')?.status, 'a refusal rolls nothing in').toBe('active')
    expect(rolesGets, 'a refusal refetches no roles').toBe(before)

    removePost.answer = () => ({ status: 200, body: wire('u2', { role: 'preparer', status: 'removed', display_name: null, email: null }) })
    await act(async () => ctx().removeMember('u2'))
    expect(removePost.calls).toEqual([`${GATEWAY}/api/tenancy/v1/memberships/u2/remove`, `${GATEWAY}/api/tenancy/v1/memberships/u2/remove`])
    const gone = ctx().members.find((m) => m.id === 'u2')
    expect([gone?.status, gone?.name, gone?.email]).toEqual(['removed', 'Deleted member', null])
    expect(ctx().members.find((m) => m.id === 'u1')?.status, 'the other row is untouched').toBe('active')
    await waitFor(() => expect(rolesGets).toBe(before + 1))
  })
})
