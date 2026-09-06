// @vitest-environment jsdom
// vitest.config.ts stays `environment: 'node'` for every other suite.
//
// ROUTE-03-03. The review-hash mirror is now a review-PATH mirror, scoped to the `create`
// view only: it and `navigate` share the URL, and the mirror must stay inert off `create`
// so the two never contend. Harness is App.routePopstate.test.tsx's: the real <App/>, a
// session in a stubbed localStorage, ctx captured through a mocked Sidebar.

import { readFileSync } from 'node:fs'
import path from 'node:path'

import { act, cleanup, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { parseLocation } from './lib/route'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const REVIEW_ID = 'a1b2c3d4-e5f6-47a8-89ab-cdef01234567'
const REVIEW_ID_2 = 'b2c3d4e5-f6a7-48b9-9abc-def012345678'
const GATEWAY = 'https://gw.test'
const ENTITY_A = 'aaaaaaaa-0000-4000-8000-000000000001'
const DOCUMENT_ID = 'dddddddd-0000-4000-8000-00000000000d'
const BATCH_ID = 'bbbbbbbb-1111-4111-8111-111111111111'

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

// Only what previewImport/createImport's shared xhrJson transport (importApi.ts) touches
// for a happy-path multipart round trip -- App.handOff.test.tsx's FakeXhr, extended with a
// success reply (that file only ever drives onerror).
class FakeXhr {
  static instances: FakeXhr[] = []
  status = 0
  responseText = ''
  upload: { onprogress: (() => void) | null; onload: (() => void) | null } = { onprogress: null, onload: null }
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  ontimeout: (() => void) | null = null

  constructor() {
    FakeXhr.instances.push(this)
  }

  open(): void {}
  setRequestHeader(): void {}
  send(): void {}

  respond(status: number, body: unknown): void {
    this.status = status
    this.responseText = JSON.stringify(body)
    this.onload?.()
  }
}

function entityRow(id: string, name: string, tin: string) {
  return { id, name, tin, registration: null, sector: null, address: null, status: 'active', created_at: '2026-01-01T00:00:00Z' }
}

// One real entity so switchClient/openCreate has somewhere to resolve `activeEntity`;
// every other endpoint answers well enough not to crash a mounting Workspace
// (App.handOff.test.tsx's routeFetch, same fallback shape).
function routeFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      if (url.includes('/portfolio/v1/entities')) {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () =>
            Promise.resolve({
              entities: [entityRow(ENTITY_A, 'Mirror Co', '12345678-0001')],
              pagination: { limit: 200, offset: 0, total: 1 },
            }),
        })
      }
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () =>
          Promise.resolve({ entities: [], policies: [], members: [], roles: [], invoices: [], total: 0 }),
      })
    }),
  )
}

// bootAt plus the gateway/XHR wiring the run-driven mirror spec needs; every other spec in
// this file keeps using the plain bootAt above (no gateway, no network).
async function bootAtWithGateway(path: string) {
  routeFetch()
  vi.stubGlobal('XMLHttpRequest', FakeXhr)
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  return bootAt(path)
}

describe('AC-1: the two existing history writers are unchanged', () => {
  it('guard_theTwoExistingHistoryWritersAreUnchanged', () => {
    const src = readFileSync(path.join(process.cwd(), 'src/App.tsx'), 'utf8')
    const reviewMirrorWrite = "routeUrl('create', { reviewBatchIds: ids })"
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

describe('AC-4: entering review from a run rewrites the entry, never pushes', () => {
  // Drives applyRoute indirectly (it is not on ctx): ctx.continueMapping() -> startRun()
  // -> applyRoute, the actual transition AC-4 names ("entering review FROM A RUN").
  // restartImport tests LEAVING review within create instead, a different journey.
  it('mirror_enteringReviewRewritesTheEntryItDoesNotPushOne', async () => {
    FakeXhr.instances = []
    await bootAtWithGateway('/')

    act(() => {
      requireCtx().openCreate()
    })
    await waitFor(() =>
      expect(requireCtx().activeEntity?.id, 'activeEntity never resolved from the entity list').toBe(ENTITY_A),
    )

    const file = new File(['invoice_number,total\nINV-1,100'], 'invoices.csv', { type: 'text/csv' })
    act(() => {
      requireCtx().addPickedFiles([file])
    })

    act(() => {
      requireCtx().readAllColumns()
    })
    expect(FakeXhr.instances, 'control: the preview never reached the upload transport').toHaveLength(1)
    act(() => {
      FakeXhr.instances[0]!.respond(200, {
        document_id: DOCUMENT_ID,
        format: 'csv',
        delimiter: ',',
        encoding: 'utf-8',
        columns: ['invoice_number', 'total'],
        sample_rows: [['INV-1', '100']],
        rows_total: 1,
      })
    })
    await waitFor(() => expect(requireCtx().createStep, 'preview never landed on the mapping step').toBe('mapping'))

    // invoice_number is never auto-mapped (ALIAS excludes it) -- arm and click it by hand,
    // same as the Map step's own click handler.
    act(() => {
      requireCtx().armField('invoice_number')
    })
    act(() => {
      requireCtx().clickCol('invoice_number')
    })
    expect(
      requireCtx().groups[0]?.mapping.invoice_number,
      'sanity: the click must map invoice_number',
    ).toBe('invoice_number')

    const lengthBefore = window.history.length
    act(() => {
      requireCtx().continueMapping()
    })
    expect(FakeXhr.instances, 'control: the run never reached the createImport transport').toHaveLength(2)
    act(() => {
      // ready_invoices: 0 keeps routeAfterImport off the 'single' branch (BULK-05-8's
      // run-size gate is for a DIFFERENT case), so a one-file run still lands on review.
      FakeXhr.instances[1]!.respond(200, {
        id: BATCH_ID,
        status: 'completed',
        format: 'csv',
        delimiter: ',',
        encoding: 'utf-8',
        rows_total: 1,
        rows_valid: 1,
        rows_invalid: 0,
        ready_invoices: 0,
        quarantined_invoices: 1,
        errors: [],
        rule_set_version: null,
        invoices_clean: 0,
        invoices_with_violations: 0,
        invoice_violations: [],
      })
    })

    await waitFor(() => expect(requireCtx().createStep, 'the run never landed on review').toBe('review'))
    expect(requireCtx().reviewBatchIds, 'entering review must seed the batch id the run created').toEqual([BATCH_ID])
    expect(window.location.pathname, 'entering review must rewrite the URL to the review path').toBe(
      `/imports/${BATCH_ID}/review`,
    )
    expect(
      window.history.length,
      'entering review from a run must rewrite the CURRENT entry, never push a new one',
    ).toBe(lengthBefore)
  })
})

describe('AC-2: navigating off review clears the path in exactly one entry', () => {
  it('mirror_navigatingOffReviewClearsThePathInOneEntry', async () => {
    await bootAt(`/imports/${REVIEW_ID}/review`)
    const ctx = requireCtx()
    expect(ctx.reviewBatchIds, 'sanity: booting the review path must actually seed the batch').toEqual([REVIEW_ID])
    const lengthBefore = window.history.length

    await act(async () => {
      ctx.nav('invoices')
    })

    expect(window.location.pathname, 'the final pathname must be /invoices').toBe('/invoices')
    expect(
      window.history.length,
      'exactly one new entry -- the mirrors rewrite of the SAME entry must not add a second',
    ).toBe(lengthBefore + 1)
  })
})

describe('AC-3: the mirror cannot strand the old pathname', () => {
  it('mirror_theMirrorCannotStrandTheOldPathname', async () => {
    await bootAt(`/imports/${REVIEW_ID}/review`)
    const ctx = requireCtx()
    expect(ctx.reviewBatchIds, 'sanity: booting the review path must actually seed the batch').toEqual([REVIEW_ID])

    await act(async () => {
      ctx.nav('audit')
    })

    expect(
      window.location.pathname,
      'a mirror that ran BEFORE the push would leave /create behind instead of moving to /audit',
    ).toBe('/audit')
  })

  // The early return's primary falsifier: force a reviewBatchIds change WHILE view stays
  // off create (restartImport clears the ids without touching view), and prove the mirror
  // does nothing at all -- not even a same-URL no-op.
  it('mirror_theMirrorIsInertOffTheCreateView', async () => {
    await bootAt(`/imports/${REVIEW_ID}/review`)
    let ctx = requireCtx()
    expect(ctx.reviewBatchIds, 'sanity: booting the review path must actually seed the batch').toEqual([REVIEW_ID])

    await act(async () => {
      ctx.nav('audit')
    })
    expect(window.location.pathname, 'sanity: nav must land on /audit first').toBe('/audit')
    const fullUrlBefore = window.location.pathname + window.location.search

    await act(async () => {
      requireCtx().restartImport()
    })
    ctx = requireCtx()
    expect(ctx.reviewBatchIds, 'sanity: restartImport must actually change reviewBatchIds').toEqual([])

    expect(
      window.location.pathname + window.location.search,
      'the mirror must write nothing for a reviewBatchIds change off the create view',
    ).toBe(fullUrlBefore)
  })
})

describe('AC-3 (omitted branch): leaving review within create falls back to the bare create path', () => {
  it('mirror_leavingReviewWithinCreateFallsBackToTheBareCreatePath', async () => {
    await bootAt(`/imports/${REVIEW_ID}/review`)
    const ctx = requireCtx()
    expect(ctx.reviewBatchIds, 'sanity: booting the review path must actually seed the batch').toEqual([REVIEW_ID])

    await act(async () => {
      ctx.restartImport()
    })

    expect(window.location.pathname, 'leaving review within create must fall back to the bare create path').toBe(
      '/create',
    )
  })
})

describe('AC-4: a review path re-derives the identical screen on reload', () => {
  it('link_aReviewPathReDerivesTheIdenticalScreenOnReload', async () => {
    const path = `/imports/${REVIEW_ID}/review`
    await bootAt(path)
    requireCtx()

    expect(window.location.pathname, 'the URL must be byte-identical to the boot URL').toBe(path)

    // Re-parse the FULL url from scratch, decoupled from ctx/App's own state -- this is
    // the invariant import-wizard.spec.ts's reload assertion guards on the deployed build:
    // a reload must re-derive the identical screen.
    const at = parseLocation(window.location.pathname, window.location.search)
    expect(at.view, 'the pathname alone must parse back to create').toBe('create')
    expect(at.reviewBatchIds, 'the path must re-parse to the same batch id').toEqual([REVIEW_ID])
  })
})

describe('AC-3: Back from Invoices returns to the review screen', () => {
  it('compose_backFromInvoicesReturnsToTheReviewScreen', async () => {
    await bootAt(`/imports/${REVIEW_ID}/review`)
    let ctx = requireCtx()
    expect(reviewBatchMounts.length, 'sanity: booting straight into review must render ReviewBatch').toBeGreaterThan(0)

    await act(async () => {
      ctx.nav('invoices')
    })
    reviewBatchMounts.length = 0

    window.history.replaceState(null, '', `/imports/${REVIEW_ID}/review`)
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
    // Session A: ends with reviewBatchIds === [id2] and the CURRENT entry rewritten to the
    // /imports/<id2>/review path -- the end state decision [second-batch-relinks-an-older-entry]
    // describes. Booting straight into it is an equivalent, cheaper way to reach that state
    // than driving the real closeCreate-then-reimport sequence, which that decision itself
    // calls out of proportion for a unit suite.
    const sessionA = await bootAt(`/imports/${REVIEW_ID_2}/review`)
    const ctxA = requireCtx()
    expect(ctxA.reviewBatchIds, 'sanity: session A must be sitting on batch 2').toEqual([REVIEW_ID_2])
    expect(window.location.pathname, "sanity: session A's own entry must read batch 2").toBe(
      `/imports/${REVIEW_ID_2}/review`,
    )
    sessionA.unmount()

    // A SEPARATE app instance, at a link a user copied or bookmarked earlier -- unaffected
    // by anything session A did, because the mirror rewrites ONE history entry in ONE tab
    // and cannot reach a link held anywhere else.
    capturedCtx = undefined
    reviewBatchMounts.length = 0
    await bootAt(`/imports/${REVIEW_ID}/review`)
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
