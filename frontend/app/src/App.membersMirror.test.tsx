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

function stubGateway() {
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      const reply = (b: unknown) => ({ ok: true, status: 200, statusText: 'OK', json: () => Promise.resolve(b) })
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
})
