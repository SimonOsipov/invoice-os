// @vitest-environment jsdom
// vitest.config.ts stays `environment: 'node'` for every other suite.
//
// ROUTE-01-06. Out of Scope fences App.tsx:317 and :524-530 (the review hash) to work
// exactly as they do today; this pins that with an oracle now that the router seam's two
// writers (navigate, the mount alignment) share the URL with the pre-existing review-hash
// mirror and persona strip. Harness is App.routePopstate.test.tsx's: the real <App/>, a
// session in a stubbed localStorage, ctx captured through a mocked Sidebar.

import { readFileSync } from 'node:fs'
import path from 'node:path'

import { act, cleanup, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { parseReviewHash, reviewHash } from './lib/reviewBatch'
import { parseRoute } from './lib/route'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const REVIEW_ID = 'a1b2c3d4-e5f6-47a8-89ab-cdef01234567'
const REVIEW_ID_2 = 'b2c3d4e5-f6a7-48b9-9abc-def012345678'

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

let capturedCtx: PlatformCtx | undefined
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
    return null
  },
}))

// Records the reviewBatchIds ReviewBatch was mounted/re-rendered with -- the direct proof
// that "the review step renders" and which batch it renders, not just ctx.createStep.
const { reviewBatchMounts } = vi.hoisted(() => ({ reviewBatchMounts: [] as string[][] }))
vi.mock('./components/ReviewBatch', () => ({
  ReviewBatch: (p: { ctx: PlatformCtx }) => {
    reviewBatchMounts.push(p.ctx.reviewBatchIds)
    return null
  },
}))

beforeEach(() => {
  capturedCtx = undefined
  reviewBatchMounts.length = 0
  window.history.replaceState(null, '', '/')
  vi.stubGlobal('localStorage', createMemoryStorage())
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function bootAt(path: string) {
  window.history.replaceState(null, '', path)
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  vi.resetModules()
  const { default: App } = await import('./App')
  return render(<App />)
}

function requireCtx(): PlatformCtx {
  expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()
  return capturedCtx!
}

describe('AC-1: the two existing history writers are unchanged', () => {
  it('guard_theTwoExistingHistoryWritersAreUnchanged', () => {
    const src = readFileSync(path.join(process.cwd(), 'src/App.tsx'), 'utf8')
    const reviewMirrorWrite = "window.location.pathname + window.location.search + (h ?? '')"
    const personaStripWrite = 'window.location.pathname + window.location.hash'

    const reviewIdx = src.indexOf(reviewMirrorWrite)
    const personaIdx = src.indexOf(personaStripWrite)
    // Floor: both writers must actually be located, at two DISTINCT positions -- a scan
    // that finds neither (or the same text twice) must not read as "both unchanged".
    expect(reviewIdx, 'the review-hash mirror line was not found verbatim -- it may have changed').toBeGreaterThan(-1)
    expect(personaIdx, 'the persona-strip mirror line was not found verbatim -- it may have changed').toBeGreaterThan(-1)
    expect(reviewIdx, 'the two writers must not resolve to the same location').not.toBe(personaIdx)
  })
})

describe('AC-2: navigating off review clears the hash in exactly one entry', () => {
  it('compose_navigatingOffReviewClearsTheHashInOneEntry', async () => {
    await bootAt(`/create#review/${REVIEW_ID}`)
    const ctx = requireCtx()
    const lengthBefore = window.history.length

    await act(async () => {
      ctx.nav('invoices')
    })

    expect(window.location.pathname, 'the final pathname must be /invoices').toBe('/invoices')
    expect(window.location.hash, 'the hash must be cleared').toBe('')
    expect(
      window.history.length,
      'exactly one new entry -- the mirrors rewrite of the SAME entry must not add a second',
    ).toBe(lengthBefore + 1)
  })
})

describe('AC-3: the mirror cannot strand the old pathname', () => {
  it('compose_theMirrorCannotStrandTheOldPathname', async () => {
    await bootAt(`/create#review/${REVIEW_ID}`)
    const ctx = requireCtx()

    await act(async () => {
      ctx.nav('audit')
    })

    expect(
      window.location.pathname,
      'a mirror that ran BEFORE the push would leave /create behind instead of moving to /audit',
    ).toBe('/audit')
  })
})

describe('AC-4: a review deep link ends with both the path and the hash correct', () => {
  it('compose_aReviewDeepLinkEndsWithBothThePathAndTheHash', async () => {
    await bootAt(`/#review/${REVIEW_ID}`)
    requireCtx()

    expect(window.location.pathname, 'the mount alignment must correct the pathname to /create').toBe('/create')
    expect(window.location.hash, 'the hash must survive the alignment verbatim').toBe(`#review/${REVIEW_ID}`)

    // Re-parse the FULL url from scratch, decoupled from ctx/App's own state -- this is
    // the invariant import-wizard.spec.ts:1369 guards on the deployed build: a reload must
    // re-derive the identical screen.
    expect(parseRoute(window.location.pathname), 'the pathname alone must parse back to create').toBe('create')
    const ids = parseReviewHash(window.location.hash)
    expect(ids, 'the hash must re-parse to the same batch id').toEqual([REVIEW_ID])
    expect(
      reviewHash('create', 'review', ids!),
      'reviewHash only round-trips to the SAME string when step is review -- proving step survives too, not just view',
    ).toBe(window.location.hash)
  })
})

describe('AC-3: Back from Invoices returns to the review screen', () => {
  it('compose_backFromInvoicesReturnsToTheReviewScreen', async () => {
    await bootAt(`/create#review/${REVIEW_ID}`)
    let ctx = requireCtx()
    expect(reviewBatchMounts.length, 'sanity: booting straight into review must render ReviewBatch').toBeGreaterThan(0)

    await act(async () => {
      ctx.nav('invoices')
    })
    reviewBatchMounts.length = 0

    window.history.replaceState(null, '', `/create#review/${REVIEW_ID}`)
    await act(async () => {
      window.dispatchEvent(new PopStateEvent('popstate'))
    })

    ctx = requireCtx()
    expect(ctx.view, 'Back must restore the create view').toBe('create')
    expect(
      reviewBatchMounts.length,
      'the review step must render again after Back, not stay on the Invoices screen',
    ).toBeGreaterThan(0)
    expect(reviewBatchMounts.at(-1), 'the re-rendered review step must still show the SAME batch').toEqual([REVIEW_ID])
  })
})

describe('AC-5: an externally-held review link is not damaged by anything this session does', () => {
  it('link_anExternallyHeldReviewLinkStillColdLoadsItsOwnBatch', async () => {
    // Session A: ends with reviewBatchIds === [id2] and the CURRENT entry's hash rewritten
    // to #review/<id2> -- the end state decision [second-batch-relinks-an-older-entry]
    // describes. Booting straight into it is an equivalent, cheaper way to reach that state
    // than driving the real closeCreate-then-reimport sequence, which that decision itself
    // calls out of proportion for a unit suite.
    const sessionA = await bootAt(`/create#review/${REVIEW_ID_2}`)
    const ctxA = requireCtx()
    expect(ctxA.reviewBatchIds, 'sanity: session A must be sitting on batch 2').toEqual([REVIEW_ID_2])
    expect(window.location.hash, "sanity: session A's own entry must read batch 2").toBe(`#review/${REVIEW_ID_2}`)
    sessionA.unmount()

    // A SEPARATE app instance, at a link a user copied or bookmarked earlier -- unaffected
    // by anything session A did, because the mirror rewrites ONE history entry in ONE tab
    // and cannot reach a link held anywhere else.
    capturedCtx = undefined
    reviewBatchMounts.length = 0
    await bootAt(`/create#review/${REVIEW_ID}`)
    const ctxB = requireCtx()

    expect(
      ctxB.reviewBatchIds,
      "the externally-held link must cold-load its OWN batch, never session A's",
    ).toEqual([REVIEW_ID])
    expect(
      reviewBatchMounts.some((ids) => ids.length === 1 && ids[0] === REVIEW_ID),
      'ReviewBatch must have rendered with batch 1',
    ).toBe(true)
    expect(
      reviewBatchMounts.some((ids) => ids.includes(REVIEW_ID_2)),
      "batch 2 must never appear in the cold-loaded link's render",
    ).toBe(false)
  })
})
