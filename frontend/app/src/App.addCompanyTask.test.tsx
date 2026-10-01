// @vitest-environment jsdom
// AUTH-10-03 QA: the App.tsx dashboard branch (AddCompanyTask vs DashboardActive) driven
// through the real <App/>. Harness mirrors App.routeBoot.test.tsx.
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { EMPTY_BUCKET } from './lib/dashboard'
import { DEEP_LINK_KEY, DEEP_LINK_SCHEMA_VERSION } from './lib/deepLink'
import type { Entity } from './lib/portfolio'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

const GATEWAY = 'https://gw.test'

const ENTITY: Entity = {
  id: 'e1',
  name: 'Lagos Freight',
  tin: '20184412-0001',
  registration: null,
  sector: null,
  address: null,
  status: 'active',
  created_at: '2026-01-01T00:00:00Z',
}

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

let entityReply: () => Promise<Entity[]>

function stubGateway() {
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      const body = url.includes('/api/portfolio/v1/entities')
        ? entityReply().then((entities) => ({ entities, pagination: { limit: 200, offset: 0, total: entities.length } }))
        : Promise.resolve({
            policies: [],
            members: [],
            roles: [],
            invoices: [],
            total: 0,
            pagination: { limit: 50, offset: 0, total: 0 },
            clients: [],
            totals: EMPTY_BUCKET,
            rejection_reasons: [],
          })
      return body.then((b) => ({ ok: true, status: 200, statusText: 'OK', json: () => Promise.resolve(b) }))
    }),
  )
}

async function boot(path: string, opts: { gateway: boolean; persona?: Session['persona'] }) {
  window.history.replaceState(null, '', path)
  const session: Session = { persona: opts.persona ?? APP_PERSONAS.firm, token: opts.gateway ? 'tok' : null, me: null, verified: true }
  localStorage.setItem(SESSION_KEY, serializeSession(session))
  if (opts.gateway) stubGateway()
  vi.resetModules()
  const { default: App } = await import('./App')
  await act(async () => {
    render(<App />)
  })
}

const task = () => screen.queryByTestId('add-company-task')

let sawTask = false
let observer: MutationObserver | null = null
function watchForTask() {
  sawTask = false
  observer = new MutationObserver(() => {
    if (document.querySelector('[data-testid="add-company-task"]')) sawTask = true
  })
  observer.observe(document.body, { childList: true, subtree: true })
}

beforeEach(() => {
  capturedCtx = undefined
  entityReply = () => Promise.resolve([])
  sessionStorage.clear()
  vi.stubGlobal('localStorage', createMemoryStorage())
  window.history.replaceState(null, '', '/')
})

afterEach(() => {
  observer?.disconnect()
  observer = null
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('AUTH-10-03: the dashboard branch in App', () => {
  it('a workspace whose roster is still loading shows Loading at /, then the task once the roster settles empty', async () => {
    let release!: (e: Entity[]) => void
    entityReply = () => new Promise((r) => (release = r))
    watchForTask()
    await boot('/', { gateway: true })
    expect(screen.getByText('Loading your workspace…')).toBeTruthy()
    expect(task()).toBeNull()
    expect(sawTask, 'the task must not flash while the roster loads').toBe(false)

    await act(async () => release([]))
    await waitFor(() => expect(task()).not.toBeNull())
    expect(screen.getByRole('heading', { level: 1, name: 'Add your first client' })).toBeTruthy()
    expect(screen.queryByText('COMPLIANCE OVERVIEW')).toBeNull()
    expect(window.location.pathname).toBe('/')
  })

  it('a workspace with a company renders the dashboard and the task never flashes', async () => {
    entityReply = () => Promise.resolve([ENTITY])
    watchForTask()
    await boot('/', { gateway: true })
    await waitFor(() => expect(screen.getByText('COMPLIANCE OVERVIEW')).toBeTruthy())
    expect(task()).toBeNull()
    expect(sawTask, 'a workspace that has a company must never be shown the add-company task').toBe(false)
    expect(screen.queryByRole('heading', { level: 1, name: 'Add your first client' })).toBeNull()
  })

  it('the task disappears once a refetch resolves a company', async () => {
    await boot('/', { gateway: true })
    await waitFor(() => expect(task()).not.toBeNull())

    entityReply = () => Promise.resolve([ENTITY])
    await act(async () => capturedCtx!.refetchEntities())
    await waitFor(() => expect(screen.getByText('COMPLIANCE OVERVIEW')).toBeTruthy())
    expect(task()).toBeNull()
  })

  it('landing on the task writes no history entry and leaves the URL at /', async () => {
    const push = vi.spyOn(window.history, 'pushState')
    const replace = vi.spyOn(window.history, 'replaceState')
    await boot('/', { gateway: true })
    await waitFor(() => expect(task()).not.toBeNull())
    expect(push).not.toHaveBeenCalled()
    expect(replace.mock.calls.length, 'the mount alignment is the one replaceState').toBeGreaterThan(0)
    for (const call of replace.mock.calls) expect(new URL(String(call[2]), 'http://x').pathname).toBe('/')
    expect(window.location.pathname).toBe('/')
  })

  it('a deep link keeps its destination and the task never renders there', async () => {
    watchForTask()
    await boot('/audit', { gateway: true })
    await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: 'Audit log' })).toBeTruthy())
    await act(async () => {
      await new Promise((r) => setTimeout(r, 30))
    })
    expect(window.location.pathname).toBe('/audit')
    expect(sawTask, 'the task is the landing screen only').toBe(false)
  })

  it('a restored destination lands on its link, not the task', async () => {
    sessionStorage.setItem(DEEP_LINK_KEY, JSON.stringify({ v: DEEP_LINK_SCHEMA_VERSION, path: '/audit', query: '', at: Date.now() }))
    watchForTask()
    await boot('/', { gateway: true })
    await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: 'Audit log' })).toBeTruthy())
    await act(async () => {
      await new Promise((r) => setTimeout(r, 30))
    })
    expect(window.location.pathname).toBe('/audit')
    expect(sawTask).toBe(false)
  })

  it('the task renders on the dashboard only: leaving it hides the task, coming back shows it', async () => {
    await boot('/', { gateway: true })
    await waitFor(() => expect(task()).not.toBeNull())

    await act(async () => capturedCtx!.nav('invoices'))
    expect(capturedCtx!.view).toBe('invoices')
    expect(task()).toBeNull()

    await act(async () => capturedCtx!.nav('dashboard'))
    await waitFor(() => expect(task()).not.toBeNull())
  })

  it('with no gateway the task shows in each mode, its button disabled', async () => {
    await boot('/', { gateway: false, persona: APP_PERSONAS.inhouse })
    expect(capturedCtx!.mode).toBe('inhouse')
    const button = within(task()!).getByRole('button', { name: 'Add company' }) as HTMLButtonElement
    expect(button.disabled).toBe(true)
    cleanup()

    await boot('/', { gateway: false, persona: APP_PERSONAS.firm })
    expect(capturedCtx!.mode).toBe('firm')
    expect((within(task()!).getByRole('button', { name: 'Add client' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it("the task's no-gateway button matches the Settings Company tab's Add company button", async () => {
    await boot('/', { gateway: false, persona: APP_PERSONAS.inhouse })
    const onTask = within(task()!).getByRole('button', { name: 'Add company' }) as HTMLButtonElement
    expect(onTask.disabled).toBe(true)

    await act(async () => capturedCtx!.nav('settings'))
    await act(async () => capturedCtx!.setSettingsTab('company'))
    const onSettings = screen.getByRole('button', { name: 'Add company' }) as HTMLButtonElement
    expect(task()).toBeNull()
    expect(onSettings.disabled).toBe(onTask.disabled)
    expect(onSettings.className).toBe(onTask.className)
    expect(onSettings.innerHTML).toBe(onTask.innerHTML)
  })
})
