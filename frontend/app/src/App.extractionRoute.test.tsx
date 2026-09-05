// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// EXTR-11-08, Mode A. The route, `carryView`, and the `openExtraction` hand-off, observed on
// PlatformCtx. Harness is App.auditPrefilter.test.tsx's, which is itself App.standIn's: the
// real <App/>, a session in a stubbed localStorage, ctx captured through a mocked Sidebar.
//
// `tsc --noEmit` STAYS GREEN in this commit, deliberately. Every red below is a runtime
// assertion naming one missing thing, because a compile failure is one global red and not
// nine per-spec ones. The two casts that buy that -- `EXTRACTION` and `ExtractionCtx` -- are
// App.auditPrefilter.test.tsx:26-31's own idiom, and each is fenced by a source scan under
// `the PlatformCtx and View contract` below so the declaration cannot go missing silently.

import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { act, cleanup, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import type { Member } from './lib/members'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx, View } from './types'

// EXTR-11-08 adds 'extraction' to the View union (types.ts:172). Widening it here is not
// this commit's to do -- Header.tsx's `Record<View, string>` stops compiling the moment it
// lands -- so the member is cast in, and `view_theUnionGainsExtraction` is the fence.
const EXTRACTION = 'extraction' as View

// The two PlatformCtx members this subtask adds, same reason and same shape as
// App.auditPrefilter.test.tsx's PrefilterCtx: making them required in types.ts reds App.tsx's
// one real construction site, and this commit ships no source change.
type ExtractionCtx = PlatformCtx & {
  extractionJobId: string | null
  openExtraction: (jobId: string) => void
}

const JOB_ID = 'c3d4e5f6-a7b8-4c3d-9e4f-5a6b7c8d9e0f'
const OTHER_JOB_ID = 'a1b2c3d4-e5f6-4a1b-8c2d-3e4f5a6b7c8d'

type RenderEntry = { view: string; jobId: string | null | undefined }

// The route's own props, recorded off a stubbed ExtractionReview. App.tsx does not import
// this module yet, so the factory below simply never runs and `reviewProps` stays empty --
// which is exactly the red `route_extractionViewMountsTheReviewWithTheJobId` reports.
const { reviewProps } = vi.hoisted(() => ({ reviewProps: [] as { ctx: unknown; jobId: unknown }[] }))
vi.mock('./components/ExtractionReview', () => ({
  ExtractionReview: (p: { ctx: unknown; jobId: unknown }) => {
    reviewProps.push({ ctx: p.ctx, jobId: p.jobId })
    return null
  },
}))

// Sidebar is not memoized and takes a freshly built ctx object every render (App.tsx:1133),
// so it re-renders on every Workspace render -- which is what makes this a complete log.
let capturedCtx: ExtractionCtx | undefined
const renders: RenderEntry[] = []
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    const ctx = p.ctx as ExtractionCtx
    capturedCtx = ctx
    renders.push({ view: ctx.view, jobId: ctx.extractionJobId })
    return null
  },
}))

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }

const MEMBER: Member = {
  id: 'm-standin-001',
  name: 'Tunde Bello',
  initials: 'TB',
  email: 'tunde@example.ng',
  role: 'preparer',
  status: 'active',
  isYou: false,
}

// Same subject as the seat -- what `returnToSeat` is handed.
const SEAT_AS_MEMBER: Member = {
  id: SEAT_SESSION.persona.subject,
  name: SEAT_SESSION.persona.name,
  initials: SEAT_SESSION.persona.initials,
  email: null,
  role: 'admin',
  status: 'active',
  isYou: true,
}

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

beforeEach(() => {
  capturedCtx = undefined
  renders.length = 0
  reviewProps.length = 0
  window.history.replaceState(null, '', '/')
  vi.stubGlobal('localStorage', createMemoryStorage())
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

// `demoMode` gates becomePersona/returnToSeat onto the ctx (DEMO-06); the carryView row needs
// them, the route rows do not, and passing the flag either way costs nothing.
async function renderAppWithSeat(demoMode: boolean) {
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  if (demoMode) vi.stubEnv('VITE_DEMO_MODE', 'true')
  vi.resetModules()
  const { default: App } = await import('./App')
  return render(<App />)
}

function requireCtx(): ExtractionCtx {
  expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()
  expect(
    typeof capturedCtx!.openExtraction,
    'PlatformCtx must expose openExtraction(jobId) -- EXTR-11-08 AC-3',
  ).toBe('function')
  expect('extractionJobId' in capturedCtx!, 'PlatformCtx must expose the extractionJobId atom').toBe(true)
  return capturedCtx!
}

describe("AC-2: carryView collapses 'extraction' to 'invoices'", () => {
  it('carryView_extractionCollapsesToInvoices', async () => {
    await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    // Each call alternates the Workspace key (stand-in subject <-> seat subject), which is
    // what makes a new `initialView` observable at all -- `view` is a useState initializer
    // read once per mount (App.standIn.test.tsx:474-476).
    await act(async () => {
      await capturedCtx!.becomePersona!(MEMBER, EXTRACTION)
    })
    expect(
      capturedCtx!.view,
      `becomePersona must collapse extraction to invoices, got ${capturedCtx!.view}`,
    ).toBe('invoices')

    await act(async () => {
      await capturedCtx!.returnToSeat!(EXTRACTION, SEAT_AS_MEMBER)
    })
    expect(
      capturedCtx!.view,
      `returnToSeat must collapse extraction to invoices, got ${capturedCtx!.view}`,
    ).toBe('invoices')

    // The control needle, on the same two handlers and the same read: without it a
    // carryView that collapsed EVERY view would satisfy both assertions above.
    await act(async () => {
      await capturedCtx!.becomePersona!(MEMBER, 'approvals')
    })
    expect(capturedCtx!.view, 'a non-collapsed view must pass through unchanged').toBe('approvals')
  })

  // The sticky-state class `D-27` names, one screen over: a view that collapses while the id it
  // was carrying survives leaves the NEXT openExtraction racing a stale job.
  it('carryView_thePersonaSwitchDropsTheJobIdWithTheView', async () => {
    await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    await act(async () => {
      capturedCtx!.openExtraction(JOB_ID)
    })
    // Floor: the id really was set, so the null below is a reset and not an untouched atom.
    expect(capturedCtx!.extractionJobId, 'the id was never written').toBe(JOB_ID)

    await act(async () => {
      await capturedCtx!.becomePersona!(MEMBER, EXTRACTION)
    })
    expect(capturedCtx!.view, 'the view must collapse').toBe('invoices')
    expect(capturedCtx!.extractionJobId, 'the job id outlived the view it belonged to').toBeNull()
  })
})

describe('AC-3: openExtraction writes the job id and navigates in one commit', () => {
  it('platformCtx_openExtractionWritesJobIdAndNav', async () => {
    await renderAppWithSeat(false)
    const ctx = requireCtx()

    await act(async () => {
      ctx.openExtraction(JOB_ID)
    })

    // Vacuity floors: two of them, because either alone is satisfied by half the handler.
    const extractionRenders = renders.filter((r) => r.view === 'extraction')
    expect(extractionRenders.length, 'the handler never navigated to the review screen').toBeGreaterThan(0)
    expect(renders.filter((r) => r.jobId != null).length, 'the job id was never written').toBeGreaterThan(0)

    // THE claim. A handler that sets the view and the id in two dispatches leaves the first
    // render that sees the new view without the id -- the trap App.tsx:1142-1147 records
    // because it bit before, and the shape platformCtx_openAuditForInvoiceWritesAtomAndNav
    // holds for openAuditForInvoice.
    expect(
      extractionRenders[0]!.jobId,
      'the first render that saw view === extraction did not carry the job id',
    ).toBe(JOB_ID)
  })

  it('platformCtx_openExtractionReplacesTheIdOnASecondCall', async () => {
    await renderAppWithSeat(false)
    const ctx = requireCtx()

    await act(async () => {
      ctx.openExtraction(JOB_ID)
    })
    await act(async () => {
      capturedCtx!.openExtraction(OTHER_JOB_ID)
    })

    expect(capturedCtx!.extractionJobId, 'the second call must replace the first id').toBe(OTHER_JOB_ID)
    // Every render on the review screen after the second call carries the second id -- a
    // one-shot atom that cleared itself would strand the screen with no job.
    const tail = renders.slice(renders.findIndex((r) => r.jobId === OTHER_JOB_ID))
    expect(tail.length, 'the second id never reached a render').toBeGreaterThan(0)
    expect(
      tail.filter((r) => r.view === 'extraction' && r.jobId !== OTHER_JOB_ID),
      'a render on the review screen lost the job id',
    ).toHaveLength(0)
  })
})

describe('openExtraction under the two calls nothing else covers', () => {
  it('platformCtx_twoCallsInOneCommitLandOnTheSecondJob', async () => {
    // Both inside ONE act(), unlike platformCtx_openExtractionReplacesTheIdOnASecondCall: React
    // batches them into a single commit, so a handler that wrote the id through a stale closure
    // or an updater would land on the first.
    await renderAppWithSeat(false)
    const ctx = requireCtx()

    await act(async () => {
      ctx.openExtraction(JOB_ID)
      ctx.openExtraction(OTHER_JOB_ID)
    })

    expect(capturedCtx!.view, 'the batched pair must still navigate').toBe('extraction')
    expect(capturedCtx!.extractionJobId, 'the last call in the commit must win').toBe(OTHER_JOB_ID)
    // And no committed render on the review screen carried the loser.
    expect(
      renders.filter((r) => r.view === 'extraction' && r.jobId !== OTHER_JOB_ID),
      'a render on the review screen carried the superseded job id',
    ).toHaveLength(0)
    const last = reviewProps[reviewProps.length - 1]
    expect(last?.jobId, 'the screen was handed the superseded job id').toBe(OTHER_JOB_ID)
  })

  it('platformCtx_anEmptyJobIdStillMountsTheScreen', async () => {
    // `''` is not `null`, so the route's narrowing lets it through and the screen renders its
    // own ErrorState off the 404 -- deliberate. The alternative (narrowing on truthiness) paints
    // the crumb over an empty column, which says less. Only a malformed wire can produce it:
    // the card's onClick forwards `newestJob(...).id` and guards on `!== null`.
    await renderAppWithSeat(false)
    const ctx = requireCtx()

    await act(async () => {
      ctx.openExtraction('')
    })

    expect(capturedCtx!.view, 'the handler must still navigate').toBe('extraction')
    expect(capturedCtx!.extractionJobId, 'the empty string must be stored verbatim, not coerced to null').toBe('')
    expect(reviewProps.length, 'the screen must mount and show its own failure, not a blank column').toBeGreaterThan(0)
    expect(reviewProps[reviewProps.length - 1]!.jobId, 'the screen must be handed the id it was given').toBe('')
  })
})

describe('AC-1: the route', () => {
  it('route_extractionViewMountsTheReviewWithTheJobId', async () => {
    await renderAppWithSeat(false)
    const ctx = requireCtx()

    await act(async () => {
      ctx.openExtraction(JOB_ID)
    })

    expect(
      reviewProps.length,
      'App renders no ExtractionReview for view === extraction -- the route line is missing',
    ).toBeGreaterThan(0)
    const last = reviewProps[reviewProps.length - 1]!
    // `jobId: string` is not nullable while ctx.extractionJobId is `string | null`, so the
    // call site has to narrow (`P-41`). A route that passed the atom through unchecked shows
    // up here as a null on a screen that cannot fetch with one.
    expect(last.jobId, 'ExtractionReview was not handed the job id off the ctx atom').toBe(JOB_ID)
    expect(
      typeof (last.ctx as PlatformCtx | undefined)?.nav,
      'ExtractionReview was not handed the PlatformCtx',
    ).toBe('function')
  })

  it('route_noJobIdRendersNoReviewScreen', async () => {
    await renderAppWithSeat(false)
    const ctx = requireCtx()

    await act(async () => {
      ctx.nav(EXTRACTION)
    })

    expect(capturedCtx!.view, 'the nav never reached the extraction view').toBe('extraction')
    expect(
      reviewProps,
      'ExtractionReview mounted with no job id -- jobId is `string`, so the route must narrow',
    ).toHaveLength(0)

    // The control needle for the absence above, in this same spec and through this same
    // stub: with an id the very same view DOES mount the screen.
    await act(async () => {
      capturedCtx!.openExtraction(JOB_ID)
    })
    expect(reviewProps.length, 'control: the stub never mounts, so the absence proved nothing').toBeGreaterThan(0)
  })
})

describe('the PlatformCtx and View contract', () => {
  const TYPES_TS = join(dirname(fileURLToPath(import.meta.url)), 'types.ts')

  // 39 `as unknown as PlatformCtx` fakes mean tsc fences almost nothing about this type, and
  // this file's own ExtractionCtx cast is a fortieth. This scan is what holds the two members
  // required rather than optional. Idiom: platformCtx_declaresTheAtomAndTheVerb.
  it('platformCtx_declaresTheJobIdAndTheVerb', () => {
    const src = readFileSync(TYPES_TS, 'utf8')
    const start = src.indexOf('export type PlatformCtx = {')
    expect(start, 'the scan read the wrong file -- no PlatformCtx declaration').toBeGreaterThan(-1)
    const end = src.indexOf('\n}\n', start)
    expect(end, 'PlatformCtx has no closing brace at column 0').toBeGreaterThan(start)
    const body = src.slice(start, end)
    // Control needle: proves the slice captured the interior, not an empty span.
    expect(body, "the slice missed PlatformCtx's members").toContain('importedInvoiceId: string | null')

    expect(body, 'PlatformCtx must carry the extractionJobId atom').toContain('extractionJobId: string | null')
    expect(body, 'PlatformCtx must carry the openExtraction verb').toContain('openExtraction: (')
    expect(body, 'extractionJobId must be required, not optional').not.toContain('extractionJobId?:')
    expect(body, 'openExtraction must be required, not optional').not.toContain('openExtraction?:')
  })

  // `EXTRACTION` above casts the member in, so no runtime row in this file would notice the
  // union never widening. tsc would -- Header.tsx's `Record<View, string>` and App.tsx's
  // route line both read it -- but only once the implementation exists to read it from.
  it('view_theUnionGainsExtraction', () => {
    const src = readFileSync(TYPES_TS, 'utf8')
    const start = src.indexOf('export type View =')
    expect(start, 'no View declaration in types.ts').toBeGreaterThan(-1)
    const line = src.slice(start, src.indexOf('\n', start))
    // Control needle: proves the line read is the union and not a truncated span.
    expect(line, 'the slice missed the View union').toContain("'dashboard'")
    expect(line, "View must include 'extraction'").toContain("'extraction'")
  })
})
