// @vitest-environment jsdom
// A scheduled-for-deletion workspace is replaced by the full-page notice.
// Harness of App.suspended.test.tsx: real <App/>, a stored session, ctx captured via a mocked
// Sidebar; makeAuthedFetch is mocked to route every call through `handler` and fire the seams.

import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
import { APP_PERSONAS, type Session } from './auth'
import { NOT_ACTIVE_MEMBER_MESSAGE, WORKSPACE_LOCKED_MESSAGE } from './lib/authedFetch'
import { SESSION_KEY, serializeSession } from './lib/session'
import { CANCEL_LINK_KEY, peekCancelLink } from './lib/workspaceDeletion'
import type { PlatformCtx } from './types'

const LOCK_403 = new ApiError('http', WORKSPACE_LOCKED_MESSAGE, 403, { error: WORKSPACE_LOCKED_MESSAGE })
const SUSPEND_403 = new ApiError('http', NOT_ACTIVE_MEMBER_MESSAGE, 403, { error: NOT_ACTIVE_MEMBER_MESSAGE })
const DUE = '2026-11-24T12:00:00Z'
const GATEWAY = 'https://gw.test'

type Opts = { method?: string; body?: unknown }
type Handler = (url: string, opts?: Opts) => Promise<unknown>
let handler: Handler
let calls: Array<{ url: string; method: string }>

vi.mock('./lib/authedFetch', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./lib/authedFetch')>()
  return {
    ...actual,
    makeAuthedFetch: (_s: Session, _u: () => void, onSuspended?: () => void, _f?: unknown, onLocked?: () => void) =>
      async (url: string, opts?: Opts) => {
        calls.push({ url, method: opts?.method ?? 'GET' })
        try {
          return await handler(url, opts)
        } catch (e) {
          if (actual.isSuspended(e)) onSuspended?.()
          else if (actual.isWorkspaceLocked(e)) onLocked?.()
          throw e
        }
      },
  }
})

let capturedCtx: PlatformCtx | undefined
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
    return <div data-testid="sidebar" />
  },
}))

const me = (dueAt: string | null, role = 'admin') => ({
  tenant: { id: 't1', name: 'Adaeze Ventures', kind: 'firm', deletion_due_at: dueAt },
  user: { id: 'u1', role, display_name: null, email: null },
})
const sessionWith = (dueAt: string | null): Session => ({
  persona: APP_PERSONAS.firm,
  token: 'tok',
  me: me(dueAt) as Session['me'],
  verified: true,
})

function createMemoryStorage() {
  const store = new Map<string, string>()
  return {
    getItem: vi.fn((k: string) => (store.has(k) ? (store.get(k) as string) : null)),
    setItem: vi.fn((k: string, v: string) => void store.set(k, v)),
    removeItem: vi.fn((k: string) => void store.delete(k)),
    clear: vi.fn(() => store.clear()),
  }
}

const isMe = (url: string) => url.endsWith('/api/tenancy/v1/me')
const isCancel = (url: string) => url.endsWith('/workspace/deletion/cancel')
const isLinkCancel = (url: string) => url.endsWith('/workspace/deletion/cancel-by-link')
const LINK = 'L'.repeat(43)
const LINK_403 = 'this cancel link is no longer valid'
const stored = () => JSON.parse(localStorage.getItem(SESSION_KEY) as string)

beforeEach(() => {
  capturedCtx = undefined
  calls = []
  handler = async (url) => {
    if (isMe(url)) return me(DUE)
    throw new Error(`unexpected ${url}`)
  }
  window.history.replaceState(null, '', '/')
  sessionStorage.clear()
  vi.stubGlobal('localStorage', createMemoryStorage())
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function renderApp(session: Session) {
  localStorage.setItem(SESSION_KEY, serializeSession(session))
  vi.resetModules()
  const { default: App } = await import('./App')
  return render(<App />)
}

const notice = () => screen.queryByTestId('workspace-deletion-notice')

describe('deletion notice', () => {
  it('deletion notice: a locked stored session boots to the notice', async () => {
    await renderApp(sessionWith(DUE))
    await screen.findByTestId('workspace-deletion-notice')

    expect(capturedCtx, 'the workspace must not mount').toBeUndefined()
    expect(calls.map((c) => c.url)).toEqual([`${GATEWAY}/api/tenancy/v1/me`])
  })

  it('deletion notice: a mid-session lock 403 replaces the workspace', async () => {
    await renderApp(sessionWith(null))
    await screen.findByTestId('sidebar')
    expect(notice()).toBeNull()
    handler = async (url) => {
      if (isMe(url)) return me(DUE)
      throw LOCK_403
    }
    await act(async () => {
      await capturedCtx!.authedFetch('/x').catch(() => {})
    })

    await screen.findByTestId('workspace-deletion-notice')
    expect(screen.queryByTestId('sidebar')).toBeNull()
  })

  it.each([
    ['admin', true],
    ['preparer', false],
  ])('deletion notice: an admin sees Cancel deletion, a preparer does not (%s)', async (role, admin) => {
    handler = async (url) => {
      if (isMe(url)) return me(DUE, role)
      throw new Error(`unexpected ${url}`)
    }
    await renderApp(sessionWith(DUE))
    const n = await screen.findByTestId('workspace-deletion-notice')

    expect(n.textContent).toContain('This workspace is scheduled for deletion')
    await waitFor(() => expect(n.textContent).toContain('24 November 2026'))
    expect(n.textContent).toContain('Adaeze Ventures')
    expect(screen.queryByTestId('deletion-notice-cancel') !== null).toBe(admin)
    expect(screen.getByRole('button', { name: 'Sign out' })).toBeTruthy()
  })

  it('deletion notice: Cancel deletion unlocks and remounts the workspace', async () => {
    handler = async (url) => {
      if (isMe(url)) return me(DUE)
      if (isCancel(url)) return {}
      throw new Error(`unexpected ${url}`)
    }
    await renderApp(sessionWith(DUE))
    fireEvent.click(await screen.findByTestId('deletion-notice-cancel'))

    await screen.findByTestId('sidebar')
    expect(calls.some((c) => isCancel(c.url) && c.method === 'POST')).toBe(true)
    expect(notice()).toBeNull()
    expect(stored().me.tenant.deletion_due_at).toBeNull()
  })

  it('deletion notice: a failed cancel keeps the notice and shows why', async () => {
    const why = 'only an admin can cancel the deletion'
    handler = async (url) => {
      if (isMe(url)) return me(DUE)
      throw new ApiError('http', why, 403, { error: why })
    }
    await renderApp(sessionWith(DUE))
    fireEvent.click(await screen.findByTestId('deletion-notice-cancel'))

    await waitFor(() => expect(screen.getByTestId('deletion-notice-error').textContent).toBe(why))
    expect(notice()).not.toBeNull()
    expect(capturedCtx).toBeUndefined()
  })

  it('deletion notice: a stale stored lock unlocks when /v1/me says null', async () => {
    handler = async (url) => {
      if (isMe(url)) return me(null)
      throw new Error(`unexpected ${url}`)
    }
    await renderApp(sessionWith(DUE))

    await screen.findByTestId('sidebar')
    expect(notice()).toBeNull()
  })

  it('deletion notice: Sign out signs out', async () => {
    await renderApp(sessionWith(DUE))
    await screen.findByTestId('workspace-deletion-notice')
    vi.mocked(localStorage.removeItem).mockClear()
    fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))

    await waitFor(() => expect(localStorage.removeItem).toHaveBeenCalledWith(SESSION_KEY))
    expect(notice()).toBeNull()
  })

  it('deletion notice: the suspension latch still shows SuspendedNotice', async () => {
    await renderApp(sessionWith(null))
    await screen.findByTestId('sidebar')
    handler = async () => {
      throw SUSPEND_403
    }
    await act(async () => {
      await capturedCtx!.authedFetch('/x').catch(() => {})
    })

    await screen.findByTestId('suspended-notice')
    expect(notice()).toBeNull()
  })
  it('deletion notice: a failed /v1/me read after a latch shows Retry, and Retry loads the date', async () => {
    await renderApp(sessionWith(null))
    await screen.findByTestId('sidebar')
    let meOk = false
    handler = async (url) => {
      if (isMe(url)) {
        if (!meOk) throw new ApiError('network', 'Could not reach the server')
        return me(DUE)
      }
      throw LOCK_403
    }
    await act(async () => {
      await capturedCtx!.authedFetch('/x').catch(() => {})
    })

    const retry = await screen.findByRole('button', { name: 'Retry' })
    expect(notice()!.textContent).not.toContain('24 November 2026')
    meOk = true
    fireEvent.click(retry)
    await waitFor(() => expect(notice()!.textContent).toContain('24 November 2026'))
  })

  it('deletion notice: a suspended admin whose cancel is refused sees the sentence and no suspended notice', async () => {
    handler = async (url) => {
      if (isMe(url)) return me(DUE)
      throw SUSPEND_403
    }
    await renderApp(sessionWith(DUE))
    fireEvent.click(await screen.findByTestId('deletion-notice-cancel'))

    await waitFor(() => expect(screen.getByTestId('deletion-notice-error').textContent).toBe(NOT_ACTIVE_MEMBER_MESSAGE))
    expect(screen.queryByTestId('suspended-notice')).toBeNull()
    expect((screen.getByTestId('deletion-notice-cancel') as HTMLButtonElement).disabled).toBe(false)
  })

  it('deletion notice: a network failure on cancel shows its message and re-enables Cancel', async () => {
    handler = async (url) => {
      if (isMe(url)) return me(DUE)
      throw new ApiError('network', 'Could not reach the server')
    }
    await renderApp(sessionWith(DUE))
    fireEvent.click(await screen.findByTestId('deletion-notice-cancel'))

    await waitFor(() => expect(screen.getByTestId('deletion-notice-error').textContent).toBe('Could not reach the server'))
    expect(notice()).not.toBeNull()
    expect((screen.getByTestId('deletion-notice-cancel') as HTMLButtonElement).disabled).toBe(false)
  })
})

describe('cancel link', () => {
  const posts = () => calls.filter((c) => c.method === 'POST')
  const cancelsAnswer = (url: string) => {
    if (isMe(url)) return me(DUE)
    if (isCancel(url) || isLinkCancel(url)) return {}
    throw new Error(`unexpected ${url}`)
  }

  it('cancel link: a signed-in admin opens the link and Cancel deletion sends it', async () => {
    const bodies: unknown[] = []
    handler = async (url, opts) => {
      if (isLinkCancel(url)) bodies.push(opts?.body)
      return cancelsAnswer(url)
    }
    window.history.replaceState(null, '', `/#cancel-deletion=${LINK}`)
    await renderApp(sessionWith(DUE))
    fireEvent.click(await screen.findByTestId('deletion-notice-cancel'))

    await screen.findByTestId('sidebar')
    expect(window.location.hash).toBe('')
    expect(posts().map((c) => c.url)).toEqual([`${GATEWAY}/api/tenancy/v1/workspace/deletion/cancel-by-link`])
    expect(bodies).toEqual([{ token: LINK }])
    expect(peekCancelLink()).toBeNull()
  })

  it('cancel link: the token survives the sign-in bounce', async () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    const real = Object.getOwnPropertyDescriptor(window, 'location')!
    const hrefWrites: string[] = []
    Object.defineProperty(window, 'location', {
      configurable: true,
      writable: true,
      value: {
        get href() {
          return hrefWrites.length ? hrefWrites[hrefWrites.length - 1] : 'http://localhost/'
        },
        set href(v: string) {
          hrefWrites.push(v)
        },
        pathname: '/',
        hash: `#cancel-deletion=${LINK}`,
        hostname: 'localhost',
        origin: 'http://localhost',
        search: '',
      },
    })
    try {
      vi.resetModules()
      const { default: App } = await import('./App')
      render(<App />)
      await waitFor(() => expect(hrefWrites.length).toBeGreaterThan(0))
      expect(hrefWrites[0].startsWith('https://landing.example/')).toBe(true)
      expect(peekCancelLink()).toBe(LINK)
    } finally {
      Object.defineProperty(window, 'location', real)
    }
    cleanup()
    handler = async (url, opts) => {
      if (isLinkCancel(url)) expect(opts?.body).toEqual({ token: LINK })
      return cancelsAnswer(url)
    }
    await renderApp(sessionWith(DUE))
    fireEvent.click(await screen.findByTestId('deletion-notice-cancel'))
    await screen.findByTestId('sidebar')
    expect(posts().map((c) => c.url)).toEqual([`${GATEWAY}/api/tenancy/v1/workspace/deletion/cancel-by-link`])
  })

  it('cancel link: a refused link falls through to the plain route in one click', async () => {
    handler = async (url) => {
      if (isLinkCancel(url)) throw new ApiError('http', LINK_403, 403, { error: LINK_403 })
      return cancelsAnswer(url)
    }
    window.history.replaceState(null, '', `/#cancel-deletion=${LINK}`)
    await renderApp(sessionWith(DUE))
    fireEvent.click(await screen.findByTestId('deletion-notice-cancel'))

    await screen.findByTestId('sidebar')
    expect(peekCancelLink()).toBeNull()
    expect(posts().map((c) => c.url.split('/deletion')[1])).toEqual(['/cancel-by-link', '/cancel'])
  })

  it('cancel link: a 403 that is not the link refusal shows the error and sends no second request', async () => {
    const NOT_PERMITTED = 'only an admin can cancel the deletion'
    handler = async (url) => {
      if (isLinkCancel(url)) throw new ApiError('http', NOT_PERMITTED, 403, { error: NOT_PERMITTED })
      return cancelsAnswer(url)
    }
    window.history.replaceState(null, '', `/#cancel-deletion=${LINK}`)
    await renderApp(sessionWith(DUE))
    fireEvent.click(await screen.findByTestId('deletion-notice-cancel'))

    await waitFor(() => expect(screen.getByTestId('deletion-notice-error').textContent).toBe(NOT_PERMITTED))
    expect(notice()).not.toBeNull()
    expect(posts().map((c) => c.url.split('/deletion')[1])).toEqual(['/cancel-by-link'])
  })

  it('cancel link: a stored session from before the lock still sends the link', async () => {
    let locked = false
    handler = async (url) => {
      if (isLinkCancel(url) || isCancel(url)) return cancelsAnswer(url)
      if (isMe(url)) return me(DUE)
      locked = true
      throw LOCK_403
    }
    window.history.replaceState(null, '', `/#cancel-deletion=${LINK}`)
    await renderApp(sessionWith(null))
    await screen.findByTestId('sidebar')
    await act(async () => {
      await capturedCtx!.authedFetch('/x').catch(() => {})
    })
    expect(locked).toBe(true)
    fireEvent.click(await screen.findByTestId('deletion-notice-cancel'))

    await screen.findByTestId('sidebar')
    expect(posts().map((c) => c.url.split('/deletion')[1])).toEqual(['/cancel-by-link'])
  })

  it('cancel link: a link in an unlocked workspace is never sent', async () => {
    handler = async () => ({})
    window.history.replaceState(null, '', `/#cancel-deletion=${LINK}`)
    await renderApp(sessionWith(null))

    await screen.findByTestId('sidebar')
    expect(calls.filter((c) => isCancel(c.url) || isLinkCancel(c.url))).toEqual([])
  })

  it('cancel link: an expired hold falls back to the plain cancel', async () => {
    handler = async (url) => cancelsAnswer(url)
    sessionStorage.setItem(CANCEL_LINK_KEY, JSON.stringify({ v: 1, t: LINK, at: Date.now() - 10 * 60 * 1000 - 1 }))
    await renderApp(sessionWith(DUE))
    fireEvent.click(await screen.findByTestId('deletion-notice-cancel'))

    await screen.findByTestId('sidebar')
    expect(posts().map((c) => c.url.split('/deletion')[1])).toEqual(['/cancel'])
  })

  it('cancel link: the token leaves the address bar even when the visit is signed out', async () => {
    handler = async () => ({})
    window.history.replaceState(null, '', `/#cancel-deletion=${LINK}`)
    vi.resetModules()
    const { default: App } = await import('./App')
    render(<App />)

    await waitFor(() => expect(window.location.hash).toBe(''))
    expect(peekCancelLink()).toBe(LINK)
  })

  it('cancel link: a link is dropped when the notice finds the workspace already unlocked', async () => {
    handler = async (url) => (isMe(url) ? me(null) : {})
    window.history.replaceState(null, '', `/#cancel-deletion=${LINK}`)
    await renderApp(sessionWith(DUE))

    await screen.findByTestId('sidebar')
    expect(peekCancelLink()).toBeNull()
  })
})
