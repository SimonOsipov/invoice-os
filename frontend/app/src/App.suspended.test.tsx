// @vitest-environment jsdom
// AUDIT-10-07 AC-4/AC-6 — the suspended state replaces the workspace.
//
// Harness is App.auditPrefilter.test.tsx's: the real <App/>, a session seeded into a stubbed
// localStorage, ctx captured through a mocked Sidebar. VITE_GATEWAY_URL stays unstubbed, so
// gatewayBase() is null and nothing on any screen fetches on its own. The seam refusal is
// delivered by calling ctx.authedFetch, whose module is mocked to do exactly what the real
// seam does against a 403 carrying the envelope — through the REAL isSuspended predicate.
// Everything after that callback is App's own code, which is what these specs are for.
//
// The predicate itself, the callback's once-ness and the rethrow are node specs in
// lib/authedFetch.test.ts (S1-S8); this file does not re-prove them.

import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
import { APP_PERSONAS, type Session } from './auth'
import { NOT_ACTIVE_MEMBER_MESSAGE } from './lib/authedFetch'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

// The wire refusal, built as apiFetch builds it: message from the envelope, body the parsed
// envelope itself.
const SEAM_REFUSAL = new ApiError('http', NOT_ACTIVE_MEMBER_MESSAGE, 403, { error: NOT_ACTIVE_MEMBER_MESSAGE })

// The third argument App hands the factory. undefined here means App never wired it.
let capturedOnSuspended: (() => void) | undefined

vi.mock('./lib/authedFetch', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./lib/authedFetch')>()
  return {
    ...actual,
    makeAuthedFetch: (_session: Session, _onSignOut: () => void, onSuspended?: () => void) => {
      capturedOnSuspended = onSuspended
      return async () => {
        if (actual.isSuspended(SEAM_REFUSAL)) onSuspended?.()
        throw SEAM_REFUSAL
      }
    },
  }
})

let capturedCtx: PlatformCtx | undefined
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
    return null
  },
}))

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: 'tok', me: null, verified: true }

// Node v25's native localStorage collides with jsdom's (App.standIn.test.tsx:74-75).
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

// join(), not `new URL(dynamic, import.meta.url)`: vite rewrites the latter into an asset
// glob over the repo root and the run dies on a non-asset it finds there.
const SRC_DIR = dirname(fileURLToPath(import.meta.url))

beforeEach(() => {
  capturedCtx = undefined
  capturedOnSuspended = undefined
  vi.stubGlobal('localStorage', createMemoryStorage())
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function renderApp() {
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  vi.resetModules()
  const { default: App } = await import('./App')
  return render(<App />)
}

function requireCtx(): PlatformCtx {
  expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()
  return capturedCtx!
}

// The workspace's own chrome, from two independent places: a testid App renders directly and
// the shell class every screen sits inside. One alone could survive a half-done replacement.
function workspaceIsRendered(): boolean {
  return screen.queryByTestId('env-banner') !== null || document.querySelector('.pf-shell') !== null
}

describe('AC-4: the suspended card replaces the workspace', () => {
  it('appSuspended_theWorkspaceIsReplacedByTheCard', async () => {
    await renderApp()
    const ctx = requireCtx()

    expect(workspaceIsRendered(), 'the workspace must be up BEFORE the refusal').toBe(true)
    expect(screen.queryByTestId('suspended-notice'), 'the card must not render unprompted').toBeNull()

    await act(async () => {
      await ctx.authedFetch('/x').catch(() => {})
    })

    const card = screen.queryByTestId('suspended-notice')
    expect(card, 'the seam refusal did not reach the render').not.toBeNull()
    expect(workspaceIsRendered(), 'no partial workspace: the shell must be gone, not merely covered').toBe(false)
  })

  it('appSuspended_theCardNamesTheReasonAndWhoToAsk', async () => {
    await renderApp()
    const ctx = requireCtx()
    await act(async () => {
      await ctx.authedFetch('/x').catch(() => {})
    })

    const text = screen.getByTestId('suspended-notice').textContent ?? ''
    expect(text, 'the card must say the membership is not active').toMatch(/membership in this workspace is not active/i)
    expect(text, 'the card must say who can fix it').toMatch(/workspace admin/i)
  })

  it('appSuspended_theCardOffersNoRetry', async () => {
    await renderApp()
    const ctx = requireCtx()
    await act(async () => {
      await ctx.authedFetch('/x').catch(() => {})
    })

    // No retry loop (design): a control that re-fires the refused call would hammer a gate
    // whose answer cannot change without an admin.
    expect(screen.getByTestId('suspended-notice').querySelectorAll('button')).toHaveLength(0)
  })

  it('appSuspended_theCardSurvivesAFurtherRefusal', async () => {
    // Idempotent: a second refused call must not tear the card down or loop the render.
    await renderApp()
    const ctx = requireCtx()
    await act(async () => {
      await ctx.authedFetch('/x').catch(() => {})
      await ctx.authedFetch('/y').catch(() => {})
    })

    expect(screen.queryByTestId('suspended-notice')).not.toBeNull()
  })
})

describe('AC-6: nothing on the suspended path writes a console error', () => {
  it('appSuspended_noConsoleErrorOnThatPath', async () => {
    // The topology suite fails a run on ANY console error (e2e/topology/consoleGate.ts), so a
    // logged rejection here would be a red browser spec on a screen this file cannot reach.
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})

    await renderApp()
    const ctx = requireCtx()
    await act(async () => {
      await ctx.authedFetch('/x').catch(() => {})
    })

    expect(screen.queryByTestId('suspended-notice')).not.toBeNull()
    expect(spy.mock.calls.map((c) => String(c[0])), 'console errors on the suspended path').toEqual([])
  })
})

describe('AC-2: both construction sites are wired', () => {
  it('appSuspended_appPassesOnSuspendedToMakeAuthedFetch', async () => {
    await renderApp()
    expect(typeof capturedOnSuspended, 'App.tsx never passed onSuspended to makeAuthedFetch').toBe('function')
  })

  it('appSuspended_makeImportAuthIsWiredFromTheSamePair', () => {
    // makeImportAuth's transport is the multipart XHR, which no render here exercises, and the
    // parameter is optional so a missed site typechecks. A source scan is the only guard that
    // the two factories stay in step (importApi.ts:43's own claim).
    const src = readFileSync(join(SRC_DIR, 'App.tsx'), 'utf8')

    for (const factory of ['makeAuthedFetch', 'makeImportAuth']) {
      const call = new RegExp(`${factory}\\(([^)]*)\\)`).exec(src)?.[1]
      expect(call, `App.tsx no longer calls ${factory}`).toBeDefined()
      expect(
        call!.split(',').length,
        `${factory} must be called with (session, onSignOut, onSuspended) — got ${call}`,
      ).toBe(3)
    }
  })
})
