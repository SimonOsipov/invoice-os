// @vitest-environment jsdom
// vitest.config.ts stays `environment: 'node'` for every other suite.
//
// navigate() and the eight setView( call sites. Harness is App.routeBoot.test.tsx's: the
// real <App/>, a session in a stubbed localStorage, ctx captured through a mocked Sidebar.

import { execSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import path from 'node:path'

import { act, cleanup, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { EMPTY_BUCKET } from './lib/dashboard'
import { MAX_RUN_FILES } from './lib/importRun'
import { ROUTE_PATHS } from './lib/route'
import type { Member } from './lib/members'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { AuditPrefilter, PlatformCtx, View } from './types'

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const REVIEW_ID = 'a1b2c3d4-e5f6-47a8-89ab-cdef01234567'
const INVOICE_ID = 'aaaaaaaa-0000-4000-8000-000000000001'
const OTHER_INVOICE_ID = 'bbbbbbbb-0000-4000-8000-000000000002'
const JOB_A = 'c3d4e5f6-a7b8-4c3d-9e4f-5a6b7c8d9e0f'

const MEMBER: Member = {
  id: 'm-nav-001',
  name: 'Tunde Bello',
  initials: 'TB',
  email: 'tunde@example.ng',
  role: 'preparer',
  status: 'active',
  isYou: false,
}

// Same subject as the seat -- becomePersona short-circuits into returnToSeat for this row
// (App.standIn.test.tsx's SEAT_AS_MEMBER, same shape).
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

type RenderEntry = {
  view: View
  prefilter: AuditPrefilter | null
  jobId: string | null
  importedInvoiceId: string | null
}

let capturedCtx: PlatformCtx | undefined
const renders: RenderEntry[] = []
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
    renders.push({
      view: p.ctx.view,
      prefilter: p.ctx.auditPrefilter,
      jobId: p.ctx.extractionJobId,
      importedInvoiceId: p.ctx.importedInvoiceId,
    })
    return null
  },
}))

// Stubbed so a cold /extraction boot (no gateway) never tries to fetch or paint a canvas.
// Whether it mounts with a live job id is ROUTE-01-04's popstate row, not this file's.
vi.mock('./components/ExtractionReview', () => ({
  ExtractionReview: () => null,
}))

beforeEach(() => {
  capturedCtx = undefined
  renders.length = 0
  // jsdom's environment is per FILE, not per test -- every test below sets its own path
  // through bootAt, but this is a defensive floor against ordering surprises.
  window.history.replaceState(null, '', '/')
  vi.stubGlobal('localStorage', createMemoryStorage())
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function bootAt(path: string, opts: { demoMode?: boolean } = {}) {
  window.history.replaceState(null, '', path)
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  if (opts.demoMode) vi.stubEnv('VITE_DEMO_MODE', 'true')
  vi.resetModules()
  const { default: App } = await import('./App')
  return render(<App />)
}

// The control needle for guard_everyAppRenderingTestFileResetsTheJsdomUrl: renders with
// WHATEVER window.location already is, unlike bootAt above which always sets it first.
async function renderWithoutUrlReset() {
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  vi.resetModules()
  const { default: App } = await import('./App')
  return render(<App />)
}

function requireCtx(): PlatformCtx {
  expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()
  return capturedCtx!
}

describe('AC-1: every setView( call site routes through navigate() and pushes', () => {
  it('nav_everySidebarViewPushesItsOwnPath', async () => {
    await bootAt('/')
    const views = Object.keys(ROUTE_PATHS) as View[]
    expect(views, 'the route table must have exactly 13 members').toHaveLength(13)

    for (const v of views) {
      const lengthBefore = window.history.length
      await act(async () => {
        capturedCtx!.nav(v)
      })
      const ctx = requireCtx()
      expect(window.location.pathname, `nav('${v}') should push '${ROUTE_PATHS[v]}'`).toBe(ROUTE_PATHS[v])
      expect(window.history.length, `nav('${v}') must add exactly one history entry`).toBe(lengthBefore + 1)
      expect(ctx.view, `nav('${v}') should set view to '${v}'`).toBe(v)
    }
  })

  it('openCreate_pushesCreate', async () => {
    await bootAt('/')
    const lengthBefore = window.history.length
    await act(async () => {
      capturedCtx!.openCreate()
    })
    const ctx = requireCtx()
    expect(window.location.pathname, 'openCreate must push /create').toBe('/create')
    expect(window.history.length, 'openCreate must add exactly one history entry').toBe(lengthBefore + 1)
    expect(ctx.createStep, 'the upload reset must still run').toBe('upload')
  })

  it('closeCreate_pushesInvoices', async () => {
    await bootAt('/create')
    const lengthBefore = window.history.length
    await act(async () => {
      capturedCtx!.closeCreate()
    })
    expect(window.location.pathname, 'closeCreate must push /invoices').toBe('/invoices')
    expect(window.history.length, 'closeCreate must add exactly one history entry').toBe(lengthBefore + 1)
  })

  it('openImportedInvoice_pushesTheDetailPathAndKeepsTheSelection', async () => {
    await bootAt('/')
    const lengthBefore = window.history.length
    await act(async () => {
      capturedCtx!.openImportedInvoice(INVOICE_ID)
    })
    const ctx = requireCtx()
    expect(window.location.pathname, 'openImportedInvoice must push /invoices/<id>').toBe(`/invoices/${INVOICE_ID}`)
    expect(window.history.length, 'openImportedInvoice must add exactly one history entry').toBe(lengthBefore + 1)
    expect(ctx.importedInvoiceId, 'the selection atom must name the id it was handed').toBe(INVOICE_ID)
  })

  // N-3: the same one-handler invariant openAuditForInvoice/openExtraction already pin
  // below, restated for the id navigate() now carries -- the FIRST render with
  // view === 'detail' must already have importedInvoiceId, not a render later.
  it('openImportedInvoice_theFirstDetailRenderAlreadyCarriesTheId', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.openImportedInvoice(INVOICE_ID)
    })
    const detailRenders = renders.filter((r) => r.view === 'detail')
    expect(detailRenders.length, 'the handler never navigated to detail').toBeGreaterThan(0)
    expect(
      detailRenders[0]!.importedInvoiceId,
      'the first render that saw view === detail did not carry the id',
    ).toBe(INVOICE_ID)
  })

  it('ctx exposes no selectInvoice key (task-919, ROUTE-02-04, D-4)', async () => {
    await bootAt('/')
    const ctx = requireCtx()
    expect('selectInvoice' in ctx).toBe(false)
  })

  it('openAuditForInvoice_pushesAuditWithThePrefilterStillSetInTheSameRender', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.openAuditForInvoice(INVOICE_ID, 'INV-1')
    })
    expect(window.location.pathname, 'openAuditForInvoice must push /audit').toBe('/audit')

    // Vacuity floors: either alone is satisfied by half the handler.
    const auditRenders = renders.filter((r) => r.view === 'audit')
    expect(auditRenders.length, 'the handler never navigated to Audit').toBeGreaterThan(0)
    expect(renders.filter((r) => r.prefilter != null).length, 'the atom was never written').toBeGreaterThan(0)

    // A rewire that split the atom write and the navigate() call into two dispatches
    // would leave the FIRST render on Audit without the prefilter -- App.tsx:1157-1159's
    // one-handler invariant, restated for the navigate() seam.
    expect(auditRenders[0]!.prefilter, 'the first Audit render must already carry the prefilter').toEqual({
      invoiceId: INVOICE_ID,
      invoiceNumber: 'INV-1',
    })
  })

  it('openExtraction_pushesExtractionWithTheJobIdInTheSameRender', async () => {
    await bootAt('/')
    const lengthBefore = window.history.length
    await act(async () => {
      capturedCtx!.openExtraction(JOB_A)
    })
    expect(window.location.pathname, 'openExtraction must push /extraction/<jobId>').toBe(`/extraction/${JOB_A}`)
    expect(window.history.length, 'openExtraction must add exactly one history entry').toBe(lengthBefore + 1)

    const extractionRenders = renders.filter((r) => r.view === 'extraction')
    expect(extractionRenders.length, 'the handler never navigated to the review screen').toBeGreaterThan(0)
    expect(renders.filter((r) => r.jobId != null).length, 'the job id was never written').toBeGreaterThan(0)

    // Same one-handler invariant as openAuditForInvoice above (App.tsx:1163-1165), restated
    // for the navigate() seam.
    expect(
      extractionRenders[0]!.jobId,
      'the first render that saw view === extraction did not carry the job id',
    ).toBe(JOB_A)
  })
})

describe('AC-1, AC-4: switchClient still pushes and still clears every pre-existing atom', () => {
  it('switchClient_pushesDashboardAndStillClearsEveryAtom', async () => {
    await bootAt(`/imports/${REVIEW_ID}/review`)
    let ctx = requireCtx()
    // Sanity: the review path seeds reviewBatchIds -- this is the atom switchClient must
    // still clear, unrelated to any URL work this subtask does.
    expect(ctx.reviewBatchIds, 'sanity: the review path must seed reviewBatchIds').toEqual([REVIEW_ID])

    await act(async () => {
      capturedCtx!.openRule('late-fee')
    })
    await act(async () => {
      capturedCtx!.openPolicy('policy-1')
    })
    await act(async () => {
      capturedCtx!.openImportedInvoice('99999999-1111-4111-8111-111111111111')
    })
    ctx = requireCtx()
    expect(ctx.importedInvoiceId, 'sanity: openImportedInvoice must have armed importedInvoiceId').toBe(
      '99999999-1111-4111-8111-111111111111',
    )

    const lengthBefore = window.history.length
    // route-02-06, V-5: count pushState calls directly -- the scrub is a replaceState, and
    // a second push here would mean it regressed into a second navigation.
    const pushSpy = vi.spyOn(window.history, 'pushState')
    await act(async () => {
      capturedCtx!.switchClient('other-entity-002')
    })
    ctx = requireCtx()

    expect(window.location.pathname, 'switchClient must push the dashboard path').toBe('/')
    expect(window.history.length, 'switchClient must add exactly one history entry').toBe(lengthBefore + 1)
    expect(pushSpy.mock.calls, 'switchClient must call pushState exactly once, not once per atom or writer').toHaveLength(1)
    expect(ctx.reviewBatchIds, 'reviewBatchIds must still be cleared').toEqual([])
    expect(ctx.importedInvoiceId, 'importedInvoiceId must still be cleared').toBeNull()
    expect(ctx.createStep, 'createStep must still reset to form').toBe('form')
    expect(ctx.openRuleKey, 'openRuleKey must still be cleared').toBeNull()
    expect(ctx.editingPolicyId, 'editingPolicyId must still be cleared').toBeNull()
    // filingError cannot be armed in this harness (no gateway to reject against) -- this
    // pins that switchClient leaves it at its resting value, not a fresh dirtying.
    expect(ctx.filingError, 'filingError must stay null').toBeNull()
  })
})

// Core AC 7: the settled URL alone can't prove the batch never rode ANY intermediate
// write -- switchClient writes twice (the leaving-entry scrub, then the dashboard push).
// Scan every recorded argument, not just where the dust settles.
describe('Core AC 7: switchClient scrubs the batch from every URL it writes', () => {
  it('switchClient_leavingReviewScrubsTheBatchFromEveryUrlItWrites', async () => {
    await bootAt(`/imports/${REVIEW_ID}/review`)
    const ctx = requireCtx()
    expect(ctx.reviewBatchIds, 'sanity: booting a review path must seed reviewBatchIds').toEqual([REVIEW_ID])

    const pushSpy = vi.spyOn(window.history, 'pushState')
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await act(async () => {
      ctx.switchClient('other-entity-777')
    })

    const written = [...pushSpy.mock.calls, ...replaceSpy.mock.calls].map((c) => String(c[2]))
    expect(written.length, 'switchClient recorded no history write at all').toBeGreaterThan(0)
    expect(
      written.some((u) => u.includes('/imports/')),
      `switchClient must scrub the batch from every URL it writes, got: ${JSON.stringify(written)}`,
    ).toBe(false)
    expect(requireCtx().reviewBatchIds, 'reviewBatchIds must be cleared').toEqual([])
  })
})

describe('AC-2: no bare setView( call site survives outside navigate()', () => {
  it('guard_appTsxHasNoSetViewCallOutsideNavigate', () => {
    const src = readFileSync(path.join(process.cwd(), 'src/App.tsx'), 'utf8')
    const matches = src.match(/\bsetView\b/g) ?? []
    // The 3 that must remain: the `[view, setView]` useState destructure, navigate's own
    // `setView(view)` call, and the popstate handler's (ROUTE-01-04) -- the one other
    // caller allowed to bypass navigate(), since it must never push (AC-3). Every one of
    // the 8 pre-existing call sites becomes a `navigate(...)` call and so drops out.
    expect(
      matches.length,
      `App.tsx has ${matches.length} bare 'setView' references; only navigate() and the popstate handler may call it`,
    ).toBe(3)
  })
})

describe('AC-3: a pushed URL never carries a query string', () => {
  it('nav_aPushedUrlCarriesNoQueryString', async () => {
    await bootAt('/?foo=1')
    const pushSpy = vi.spyOn(window.history, 'pushState')
    await act(async () => {
      capturedCtx!.nav('audit')
    })
    const call = pushSpy.mock.calls.find((c) => typeof c[2] === 'string' && c[2].startsWith('/audit'))
    expect(call, 'no pushState call to /audit was recorded').toBeDefined()
    expect(call![2], 'a pushed URL must never carry a query string').toBe('/audit')
    expect(window.location.search, 'the live URL must carry no query string either').toBe('')
  })

  // QA found the test above cannot distinguish navigate() reading location.search from
  // navigate() not: the mount-alignment effect (App.tsx:513-515) unconditionally strips
  // search on EVERY mount, so search is already '' by the time nav() runs above --
  // `+ window.location.search` mutated into navigate()'s push SURVIVES that test. Re-inject
  // search AFTER mount, isolating navigate()'s own construction of the pushed string from
  // the spy's recorded argument (the mirror running afterwards is irrelevant here).
  it('nav_neverEchoesASearchStringThatAppearsAfterMount', async () => {
    await bootAt('/')
    window.history.replaceState(null, '', window.location.pathname + '?injected=1')
    const pushSpy = vi.spyOn(window.history, 'pushState')
    await act(async () => {
      capturedCtx!.nav('audit')
    })
    const call = pushSpy.mock.calls.find((c) => typeof c[2] === 'string' && c[2].startsWith('/audit'))
    expect(call, 'no pushState call to /audit was recorded').toBeDefined()
    expect(call![2], 'navigate() must never echo a live location.search into its own push').toBe('/audit')
  })
})

describe('AC-5: a DEMO-06 persona switch corrects the URL and adds no entry', () => {
  it('personaSwitch_replacesTheUrlWithTheCarriedViewAndAddsNoEntry', async () => {
    await bootAt('/extraction', { demoMode: true })
    let ctx = requireCtx()
    expect(ctx.view, 'sanity: booting at /extraction should seed that view').toBe('extraction')
    expect(typeof ctx.becomePersona, 'DEMO_MODE must expose becomePersona on ctx').toBe('function')

    const lengthBefore = window.history.length
    await act(async () => {
      await ctx.becomePersona!(MEMBER, 'extraction')
    })
    ctx = requireCtx()

    expect(window.location.pathname, 'a persona switch must land the URL on the carried view').toBe('/invoices')
    expect(window.history.length, 'a persona switch must add no history entry').toBe(lengthBefore)
    expect(ctx.view, 'the carried view must be invoices, not extraction').toBe('invoices')
  })

  // route-02-06, V-1: the detail-drill-down mirror of the extraction case above. Assertion
  // only -- the collapse and the id-null boot seeding both come from ROUTE-02-01..05.
  it('personaSwitch_fromInvoiceDetailLandsOnInvoicesWithNoImportedId', async () => {
    await bootAt(`/invoices/${INVOICE_ID}`, { demoMode: true })
    let ctx = requireCtx()
    expect(ctx.view, 'sanity: booting at /invoices/<id> should seed detail').toBe('detail')
    expect(ctx.importedInvoiceId, 'sanity: the boot id must seed the selection').toBe(INVOICE_ID)

    await act(async () => {
      await ctx.becomePersona!(MEMBER, 'detail')
    })
    ctx = requireCtx()

    expect(window.location.pathname, 'a persona switch must land the URL on invoices').toBe('/invoices')
    expect(ctx.view, 'the carried view must be invoices, not detail').toBe('invoices')
    expect(ctx.importedInvoiceId, 'the drill-down id must not survive the collapse').toBeNull()
  })
})

describe('AC-6: every existing <App /> test file resets the jsdom URL', () => {
  it('guard_everyAppRenderingTestFileResetsTheJsdomUrl', () => {
    // Matches the JSX tag itself, not one call-site idiom -- a render(<App />) split
    // across two lines (or App.routeBoot.test.tsx's ternary) still contains this needle,
    // so a file can't opt out of the count by reshaping its own render call. Scoped to
    // *.test.tsx so main.tsx (the real, non-jsdom entry point) is correctly excluded.
    const needle = '<App( |/|>)'
    const out = execSync(`grep -rlE "${needle}" --include="*.test.tsx" src`, { cwd: process.cwd(), encoding: 'utf8' })
    const files = out
      .trim()
      .split('\n')
      .filter(Boolean)
    // Floor: a broken walk (wrong cwd, a mangled grep pattern) returns zero files and
    // reads exactly like a repo with nothing left to fix.
    // TEST-02 merge adds App.frontDoor/App.handOff/App.offlineFallback.test.tsx, the 11th-13th.
    // ROUTE-05-02 adds App.signedOutDeepLink.test.tsx and ROUTE-02-05 adds
    // App.routeDrillDown.test.tsx, the 14th and 15th.
    expect(files, 'the walk must find exactly the fifteen App-rendering test files').toHaveLength(15)

    for (const f of files) {
      const src = readFileSync(path.join(process.cwd(), f), 'utf8')
      const start = src.indexOf('beforeEach(() => {')
      expect(start, `${f} has no beforeEach(() => { block`).toBeGreaterThan(-1)
      const end = src.indexOf('\n})', start)
      const body = src.slice(start, end)
      expect(body, `${f}'s beforeEach must reset the jsdom URL to '/'`).toContain(
        "history.replaceState(null, '', '/')",
      )
    }
  })

  // The control needle for the guard above: without a between-render reset, a pathname one
  // render leaves behind seeds the very next one's boot view.
  it('guard_aLeftoverPathnameWouldHaveSeededTheNextBoot', async () => {
    window.history.replaceState(null, '', '/audit')
    await renderWithoutUrlReset()
    const ctx = requireCtx()
    expect(
      ctx.view,
      'a leftover pathname must seed the next render\'s boot view -- the pollution this guard exists to prevent is real, not hypothetical',
    ).toBe('audit')
  })
})

describe('AC-7: switchClient clears the one atom Epic Q6 named, and nothing else', () => {
  // The Back half (replaceState + dispatch popstate, asserting ctx.view restores) is
  // ROUTE-01-04's -- popstate_backAfterACompanySwitchCannotReachTheCompanyJustLeft in
  // App.routePopstate.test.tsx. This half is the whole Q6 fix and needs nothing from it.
  it('switchClient_clearsTheExtractionJobAndLeavesTheOtherAtomsAlone', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.openExtraction(JOB_A)
    })
    let ctx = requireCtx()
    expect(window.location.pathname, 'sanity: openExtraction must have pushed /extraction/<jobId>').toBe(
      `/extraction/${JOB_A}`,
    )
    expect(ctx.extractionJobId, 'sanity: the job id was never written').toBe(JOB_A)

    await act(async () => {
      capturedCtx!.switchClient('other-entity-003')
    })
    ctx = requireCtx()

    expect(ctx.extractionJobId, 'the job id must not survive the company switch').toBeNull()

    // The fence: exactly one atom clears. auditPrefilter was never armed on this journey
    // (nothing on it reaches /audit, decision [company-switch-staleness]) and createStep's
    // reset to 'form' is switchClient's OWN pre-existing behaviour, unrelated to this fix.
    expect(ctx.auditPrefilter, 'auditPrefilter must behave exactly as it does on main').toBeNull()
    expect(ctx.createStep, 'createStep must behave exactly as it does on main').toBe('form')
  })

  // REPOINTED by ROUTE-04-03, not relaxed. The original invariant -- switchClient must not
  // grow a clearing line of its own, because over-clearing is as much a Q6 violation as
  // under-clearing -- is now asserted on the source below. Its behavioural half moved:
  // auditPrefilter has SCREEN LIFETIME, so switchClient's navigate('dashboard') clears it by
  // construction and the settled value is null, not the armed pair.
  it('switchClient_leavesTheClearingToNavigateAndNeverWritesTheAtomItself', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.openAuditForInvoice(INVOICE_ID, 'INV-1')
      capturedCtx!.switchClient('other-entity-005')
    })
    const ctx = requireCtx()
    expect(window.location.pathname, 'switchClient must still win the URL').toBe('/')
    expect(ctx.auditPrefilter, 'a navigation off /audit clears the atom -- screen lifetime').toBeNull()

    const src = readFileSync(path.join(process.cwd(), 'src/App.tsx'), 'utf8')
    const start = src.indexOf('function switchClient(')
    expect(start, 'switchClient not found -- App.tsx was restructured').toBeGreaterThan(-1)
    const body = src.slice(start, src.indexOf('\n  }\n', start))
    // Floor + sanity: an empty or mis-anchored slice makes the absence below vacuous.
    expect(body.length, "switchClient's extracted body is empty -- the anchor is broken").toBeGreaterThan(0)
    expect(body, 'sanity: the slice really is switchClient').toContain("navigate('dashboard')")
    expect(body, 'switchClient must not grow a setAuditPrefilter line of its own').not.toContain('setAuditPrefilter')
  })
})

describe('QA adversarial: navigate() no longer carries a fragment forward', () => {
  it('nav_thePushCallNoLongerCarriesAnyLiveFragment', async () => {
    // Subtask 05 deletes navigate()'s own fragment-append expression -- a fragment live
    // at call time no longer rides along; the pushed url is exactly routeUrl's output.
    await bootAt('/audit')
    window.history.replaceState(null, '', '/audit#frag')
    const pushSpy = vi.spyOn(window.history, 'pushState')
    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    const call = pushSpy.mock.calls.find((c) => typeof c[2] === 'string' && c[2].startsWith('/invoices'))
    expect(call, 'no pushState call to /invoices was recorded').toBeDefined()
    expect(call![2], 'navigate() must no longer carry a fragment forward').toBe('/invoices')
  })
})

describe('QA adversarial: returnToSeat with no stand-in is a true no-op', () => {
  // No identity change means no Workspace remount, so nothing should touch the URL or
  // history -- carrying 'create' to 'invoices' only matters for a freshly mounted Workspace.
  it('returnToSeat_withNoStandInLeavesTheUrlAndHistoryUntouched', async () => {
    await bootAt('/create', { demoMode: true })
    const lengthBefore = window.history.length
    await act(async () => {
      await capturedCtx!.returnToSeat!('create', SEAT_AS_MEMBER)
    })
    requireCtx()
    expect(window.location.pathname, 'no stand-in to return from means nothing to correct').toBe('/create')
    expect(window.history.length, 'returnToSeat must add no history entry').toBe(lengthBefore)
  })

  // Formerly it.fails(): the explicit write fired unconditionally while the remount that
  // moves ctx.view did not, desyncing the two. Deleting the write (App.tsx) closes the gap.
  it('returnToSeat_withNoStandInLeavesCtxViewAgreeingWithTheUrl', async () => {
    await bootAt('/create', { demoMode: true })
    await act(async () => {
      await capturedCtx!.returnToSeat!('create', SEAT_AS_MEMBER)
    })
    const ctx = requireCtx()
    expect(window.location.pathname).toBe('/create')
    expect(ctx.view, 'the screen must agree with the address bar').toBe('create')
  })
})

describe('QA adversarial: switchClient from /extraction, the combined path+atom outcome', () => {
  it('switchClient_fromExtractionPushesDashboardAndDropsTheJobInOneStep', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.openExtraction(JOB_A)
    })
    expect(window.location.pathname, 'sanity').toBe(`/extraction/${JOB_A}`)
    const lengthBefore = window.history.length

    await act(async () => {
      capturedCtx!.switchClient('other-entity-006')
    })
    const ctx = requireCtx()
    expect(window.location.pathname, 'switchClient from /extraction must land on the dashboard path').toBe('/')
    expect(window.history.length, 'switchClient must add exactly one history entry').toBe(lengthBefore + 1)
    expect(ctx.extractionJobId, 'the job must not survive the switch').toBeNull()
  })
})

describe('QA adversarial: rapid successive navigations', () => {
  it('nav_rapidSuccessiveNavigationsLandOnTheFinalViewWithEveryEntryCounted', async () => {
    await bootAt('/')
    const lengthBefore = window.history.length
    await act(async () => {
      capturedCtx!.nav('invoices')
      capturedCtx!.nav('audit')
      capturedCtx!.nav('settings')
    })
    const ctx = requireCtx()
    expect(window.location.pathname, 'the final push must win').toBe('/settings')
    expect(window.history.length, 'all three pushes must be counted, none coalesced').toBe(lengthBefore + 3)
    expect(ctx.view, 'ctx.view must track the final navigation').toBe('settings')
  })
})

describe('QA adversarial: a nav from a boot-seeded view, not a view reached by a click', () => {
  it('nav_fromABootSeededViewPushesCorrectly', async () => {
    // ROUTE-01-02's boot seed, not navigate() -- the view is live before any handler runs.
    await bootAt('/audit')
    const ctx0 = requireCtx()
    expect(ctx0.view, 'sanity: boot must seed audit directly').toBe('audit')
    const lengthBefore = window.history.length

    await act(async () => {
      capturedCtx!.nav('settings')
    })
    const ctx = requireCtx()
    expect(window.location.pathname, 'nav must push from a boot-seeded view exactly as from a clicked one').toBe(
      '/settings',
    )
    expect(window.history.length, 'nav must add exactly one entry').toBe(lengthBefore + 1)
    expect(ctx.view).toBe('settings')
  })
})

describe('QA adversarial: navigating to the current view still pushes (documented, not asserted as a bug)', () => {
  // No AC in this subtask asks navigate() to dedupe a same-view call. Pinning the CURRENT
  // behaviour so a future change is a deliberate decision, not a silent regression either
  // way. Two Backs to leave one screen is a real UX question -- reported to the user, not
  // fixed here (QA does not change production behaviour).
  it('nav_toTheCurrentViewStillPushesADuplicateEntry', async () => {
    await bootAt('/invoices')
    const lengthBefore = window.history.length
    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    expect(window.location.pathname).toBe('/invoices')
    expect(window.history.length, 'current behaviour: a same-view nav still adds an entry').toBe(lengthBefore + 1)
  })
})

// --- ROUTE-04-03: navigate(view, params) and the five URL-aware writers --------------
//
// The push/replace rule: navigate and searchInvoices PUSH; setInvoiceQuery, setSettingsTab
// and setAuditInvoiceFilter REPLACE; the popstate handler writes nothing on the unclamped
// path (App.routePopstate.test.tsx's popstate_theHandlerWritesNoHistory), and its one write
// -- the ROUTE-06-02 identity clamp -- also replaces (popstate_theClampReplacesAndNeverPushes).
// Every history assertion below counts entries or reads the settled URL rather than merely
// asking "did some replaceState write X" -- the review-hash mirror writes on these paths too
// and has confounded three specs before.

const recordedUrls = (spy: { mock: { calls: unknown[][] } }): string[] =>
  spy.mock.calls.map((c) => c[2]).filter((u): u is string => typeof u === 'string')

describe('AC-2: a committed search pushes one entry carrying the term', () => {
  it('search_aCommittedQueryPushesOneEntryCarryingTheTerm', async () => {
    await bootAt('/')
    const ctx0 = requireCtx()
    expect(typeof ctx0.searchInvoices, 'PlatformCtx must expose the searchInvoices verb').toBe('function')

    const lengthBefore = window.history.length
    const pushSpy = vi.spyOn(window.history, 'pushState')
    await act(async () => {
      capturedCtx!.searchInvoices('acme')
    })
    const ctx = requireCtx()

    const pushed = recordedUrls(pushSpy)
    expect(pushed.length, 'the push spy recorded nothing at all').toBeGreaterThan(0)
    expect(
      pushed.filter((u) => u.startsWith('/invoices')),
      'a committed search must push exactly once, carrying the term',
    ).toEqual(['/invoices?q=acme'])
    expect(window.location.pathname + window.location.search).toBe('/invoices?q=acme')
    expect(window.history.length, 'a committed search adds exactly one entry').toBe(lengthBefore + 1)
    expect(ctx.invoiceQuery, 'the term must reach state too, not only the URL').toBe('acme')
  })
})

describe('AC-3: clearing the search box replaces', () => {
  it('search_clearingReplacesAndAddsNoEntry', async () => {
    await bootAt('/invoices?q=acme')
    const ctx0 = requireCtx()
    // Sanity: ROUTE-04-02's boot seed. Without it this proves nothing about clearing.
    expect(ctx0.invoiceQuery, 'sanity: the boot seed must carry the term').toBe('acme')
    expect(window.location.search, 'sanity: the aligned URL must carry the term').toBe('?q=acme')

    const lengthBefore = window.history.length
    const pushSpy = vi.spyOn(window.history, 'pushState')
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await act(async () => {
      capturedCtx!.setInvoiceQuery('')
    })
    const ctx = requireCtx()

    expect(window.location.pathname + window.location.search, 'the URL must lose ?q=').toBe('/invoices')
    expect(window.history.length, 'clearing must add no history entry').toBe(lengthBefore)
    expect(pushSpy.mock.calls, 'clearing must never push').toHaveLength(0)
    const replaced = recordedUrls(replaceSpy)
    expect(replaced.length, 'the replace spy recorded nothing -- no URL write happened at all').toBeGreaterThan(0)
    expect(replaced, 'the clearing verb must write the query-less URL itself').toContain('/invoices')
    expect(ctx.invoiceQuery, 'state must clear too').toBe('')
  })
})

describe('AC-4: a settings-tab click replaces', () => {
  it('settings_aTabClickReplacesAndAddsNoEntry', async () => {
    await bootAt('/settings')
    expect(requireCtx().settingsTab, 'sanity: /settings opens on Members').toBe('members')

    const lengthBefore = window.history.length
    const pushSpy = vi.spyOn(window.history, 'pushState')
    await act(async () => {
      capturedCtx!.setSettingsTab('roles')
    })
    const ctx = requireCtx()

    expect(window.location.pathname, 'the tab must appear as a path segment').toBe('/settings/roles')
    expect(window.history.length, 'a tab click must add no history entry').toBe(lengthBefore)
    expect(pushSpy.mock.calls, 'a tab click must never push').toHaveLength(0)
    expect(ctx.settingsTab, 'state must move with the URL').toBe('roles')
  })

  // The default tab is OMITTED, so returning to Members must write bare /settings. Every
  // other writer spec drives 'roles', and the two /settings pins in App.routeBoot.test.tsx
  // cover the boot alignment, not this click -- so nothing else here sees the omit rule.
  it('settings_returningToMembersWritesTheBarePath', async () => {
    await bootAt('/settings/roles')
    expect(requireCtx().settingsTab, 'sanity: the boot must open on Roles').toBe('roles')

    const pushSpy = vi.spyOn(window.history, 'pushState')
    await act(async () => {
      capturedCtx!.setSettingsTab('members')
    })
    const ctx = requireCtx()

    expect(window.location.pathname, 'the default tab must never appear as a segment').toBe('/settings')
    expect(pushSpy.mock.calls, 'a tab click must never push').toHaveLength(0)
    expect(ctx.settingsTab, 'state must move with the URL').toBe('members')
  })
})

describe('AC-5: openAuditForInvoice pushes the filtered audit URL', () => {
  it('openAuditForInvoice_pushesTheFilteredAuditUrl', async () => {
    await bootAt('/invoice')
    const lengthBefore = window.history.length
    const pushSpy = vi.spyOn(window.history, 'pushState')
    await act(async () => {
      capturedCtx!.openAuditForInvoice(INVOICE_ID, 'INV-1')
    })

    const pushed = recordedUrls(pushSpy)
    expect(pushed.length, 'the push spy recorded nothing at all').toBeGreaterThan(0)
    expect(
      pushed.filter((u) => u.startsWith('/audit')),
      'the hand-off must push exactly one entry, carrying the invoice',
    ).toEqual([`/audit?invoice=${INVOICE_ID}`])
    expect(window.history.length, 'the hand-off adds exactly one entry').toBe(lengthBefore + 1)

    // The render log, not ctx: the claim is that the FIRST Audit render already carries both
    // fields, which a settled read cannot distinguish from a later write.
    const auditRenders = renders.filter((r) => r.view === 'audit')
    expect(auditRenders.length, 'the hand-off never navigated to Audit').toBeGreaterThan(0)
    expect(auditRenders[0]!.prefilter, 'the first Audit render must carry BOTH fields').toEqual({
      invoiceId: INVOICE_ID,
      invoiceNumber: 'INV-1',
    })
  })
})

describe('AC-6: an in-screen audit filter edit replaces', () => {
  it('auditFilter_anInScreenEditReplaces', async () => {
    await bootAt(`/audit?invoice=${INVOICE_ID}`)
    const ctx0 = requireCtx()
    expect(typeof ctx0.setAuditInvoiceFilter, 'PlatformCtx must expose the setAuditInvoiceFilter verb').toBe('function')
    expect(window.location.search, 'sanity: the boot alignment must keep the filter in the URL').toBe(
      `?invoice=${INVOICE_ID}`,
    )

    const lengthBefore = window.history.length
    const pushSpy = vi.spyOn(window.history, 'pushState')
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await act(async () => {
      capturedCtx!.setAuditInvoiceFilter(null, null)
    })

    expect(window.location.pathname + window.location.search, 'removing the pill must clear the param').toBe('/audit')
    expect(window.history.length, 'an in-screen edit must add no history entry').toBe(lengthBefore)
    expect(pushSpy.mock.calls, 'an in-screen edit must never push').toHaveLength(0)
    const replaced = recordedUrls(replaceSpy)
    expect(replaced.length, 'the replace spy recorded nothing -- no URL write happened at all').toBeGreaterThan(0)
    expect(replaced, 'the verb must write the bare audit URL itself').toContain('/audit')
  })

  // The positive half. Without it, a verb that only ever wrote `/audit` would pass above.
  it('auditFilter_settingAnIdReplacesTheUrlWithThatId', async () => {
    await bootAt('/audit')
    expect(typeof requireCtx().setAuditInvoiceFilter, 'PlatformCtx must expose the setAuditInvoiceFilter verb').toBe(
      'function',
    )
    expect(window.location.search, 'sanity: an unfiltered boot carries no param').toBe('')

    const lengthBefore = window.history.length
    const pushSpy = vi.spyOn(window.history, 'pushState')
    await act(async () => {
      capturedCtx!.setAuditInvoiceFilter(OTHER_INVOICE_ID, 'INV-OTHER')
    })

    expect(window.location.pathname + window.location.search).toBe(`/audit?invoice=${OTHER_INVOICE_ID}`)
    expect(window.history.length, 'an in-screen edit must add no history entry').toBe(lengthBefore)
    expect(pushSpy.mock.calls, 'an in-screen edit must never push').toHaveLength(0)
  })
})

describe('ROUTE-04-04 AC-8: another owned-param write must not drop the invoice', () => {
  // setSettingsTab and setInvoiceQuery both rebuild the WHOLE url from state, so they read
  // the atom for the invoice member. While the atom was consume-once it read null one commit
  // after boot, and each of them silently dropped ?invoice= from a screen still rendering
  // filtered. The boot URL survives on main only because the mount alignment is declared
  // above the consume-once effect -- it is the NEXT write that loses the param.
  it('auditFilter_anotherOwnedParamWriteMustNotDropTheInvoice', async () => {
    await bootAt(`/audit?invoice=${INVOICE_ID}`)
    requireCtx()
    expect(
      window.location.pathname + window.location.search,
      'floor: the mount alignment must keep the invoice on the URL',
    ).toBe(`/audit?invoice=${INVOICE_ID}`)

    await act(async () => {
      capturedCtx!.setSettingsTab('roles')
    })
    expect(
      window.location.pathname + window.location.search,
      'a settings-tab write on /audit must not drop the invoice param',
    ).toBe(`/audit?invoice=${INVOICE_ID}`)
    expect(requireCtx().auditPrefilter, 'and must not have consumed the atom either').toEqual({
      invoiceId: INVOICE_ID,
      invoiceNumber: null,
    })

    // The second owned-param writer, same claim: the two share only the routeUrl call, so one
    // of them can be fixed while the other still drops it.
    await act(async () => {
      capturedCtx!.setInvoiceQuery('')
    })
    expect(
      window.location.pathname + window.location.search,
      'a query-clear on /audit must not drop the invoice param either',
    ).toBe(`/audit?invoice=${INVOICE_ID}`)
    expect(requireCtx().auditPrefilter, 'and must not have consumed the atom either').toEqual({
      invoiceId: INVOICE_ID,
      invoiceNumber: null,
    })
  })
})

describe("ROUTE-04-03 blocking fact: navigate's setAuditPrefilter updater must be FUNCTIONAL", () => {
  // openAuditForInvoice knows the invoiceNumber; navigate knows only the id. A plain-object
  // write inside navigate clobbers the number in the same batch, and ROUTE-04-04's AC-6 --
  // the Audit pill reading "Invoice INV-1" after a hand-off -- would ship broken. These two
  // rows are a pair: the first fails a plain-object write, the second fails a `(prev) => prev`
  // that keeps a stale number forever.
  it('navigate_keepsTheInvoiceNumberWhenTheParamNamesTheSameInvoice', async () => {
    await bootAt('/invoices')
    const ctx0 = requireCtx()
    expect(typeof ctx0.setAuditInvoiceFilter, 'PlatformCtx must expose the setAuditInvoiceFilter verb').toBe('function')

    // Arm off-screen so the navigation below is a genuine second commit, not one batch.
    // setAuditInvoiceFilter can arm off /audit; the URL then carries no param, because only
    // audit owns `invoice`. Recorded as a latent divergence, owned by ROUTE-04-05.
    await act(async () => {
      capturedCtx!.setAuditInvoiceFilter(INVOICE_ID, 'INV-1')
    })
    expect(requireCtx().auditPrefilter, 'sanity: the atom must be armed with BOTH fields').toEqual({
      invoiceId: INVOICE_ID,
      invoiceNumber: 'INV-1',
    })

    const mark = renders.length
    const pushSpy = vi.spyOn(window.history, 'pushState')
    await act(async () => {
      capturedCtx!.nav('audit', { auditInvoice: INVOICE_ID })
    })

    // nav must widen too: every component-facing call goes through it.
    const pushed = recordedUrls(pushSpy)
    expect(pushed.length, 'the push spy recorded nothing at all').toBeGreaterThan(0)
    expect(
      pushed.filter((u) => u.startsWith('/audit')),
      'nav must forward its params to navigate',
    ).toEqual([`/audit?invoice=${INVOICE_ID}`])

    const auditRenders = renders.slice(mark).filter((r) => r.view === 'audit')
    expect(auditRenders.length, 'the nav never reached Audit').toBeGreaterThan(0)
    expect(
      auditRenders[0]!.prefilter,
      'a plain-object write in navigate clobbers the number the hand-off supplied',
    ).toEqual({ invoiceId: INVOICE_ID, invoiceNumber: 'INV-1' })
  })

  it('navigate_dropsAStaleInvoiceNumberWhenTheParamNamesADifferentInvoice', async () => {
    await bootAt('/invoices')
    expect(typeof requireCtx().setAuditInvoiceFilter, 'PlatformCtx must expose the setAuditInvoiceFilter verb').toBe(
      'function',
    )
    await act(async () => {
      capturedCtx!.setAuditInvoiceFilter(INVOICE_ID, 'INV-1')
    })
    expect(requireCtx().auditPrefilter, 'sanity: the atom must be armed with BOTH fields').toEqual({
      invoiceId: INVOICE_ID,
      invoiceNumber: 'INV-1',
    })

    const mark = renders.length
    await act(async () => {
      capturedCtx!.nav('audit', { auditInvoice: OTHER_INVOICE_ID })
    })

    const auditRenders = renders.slice(mark).filter((r) => r.view === 'audit')
    expect(auditRenders.length, 'the nav never reached Audit').toBeGreaterThan(0)
    expect(
      auditRenders[0]!.prefilter,
      'a number belongs to ONE id -- carrying INV-1 onto another invoice is a wrong label',
    ).toEqual({ invoiceId: OTHER_INVOICE_ID, invoiceNumber: null })
    expect(window.location.pathname + window.location.search).toBe(`/audit?invoice=${OTHER_INVOICE_ID}`)
  })
})

describe('ROUTE-04-03: settingsTab and q are DURABLE, auditInvoice has screen lifetime', () => {
  it('nav_aSettingsTabParamWinsOverTheCurrentState', async () => {
    await bootAt('/workflows')
    expect(requireCtx().settingsTab, 'sanity: nothing has moved the tab yet').toBe('members')

    const lengthBefore = window.history.length
    await act(async () => {
      capturedCtx!.nav('settings', { settingsTab: 'roles' })
    })
    const ctx = requireCtx()

    expect(window.location.pathname, 'the destination and its owned param arrive together').toBe('/settings/roles')
    expect(window.history.length, 'a navigation adds exactly one entry').toBe(lengthBefore + 1)
    expect(ctx.settingsTab, 'state must agree with the address bar').toBe('roles')
  })

  it('nav_theCommittedQueryIsDurableAcrossANavigationAway', async () => {
    await bootAt('/')
    expect(typeof requireCtx().searchInvoices, 'PlatformCtx must expose the searchInvoices verb').toBe('function')
    await act(async () => {
      capturedCtx!.searchInvoices('acme')
    })
    expect(window.location.pathname + window.location.search, 'sanity: the search landed').toBe('/invoices?q=acme')

    await act(async () => {
      capturedCtx!.nav('audit')
    })
    expect(window.location.pathname + window.location.search, 'audit owns no q, so it emits none').toBe('/audit')

    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    const ctx = requireCtx()
    expect(window.location.pathname + window.location.search, 'q is durable: coming back re-emits it').toBe(
      '/invoices?q=acme',
    )
    expect(ctx.invoiceQuery, 'the term never left state either').toBe('acme')
  })
})

// AC-8. Call-site-only, never a bare token: a comment naming a setter is legitimate, and a
// guard that cannot tell a call from a mention is over-broad. ROUTE-04-04 has taken the
// auditPrefilter count from 4 to 3 by deleting the consume-once effect; ROUTE-04-05 takes it
// back to 4 with the popstate restore -- each bump must be a deliberate edit.
const countCalls = (src: string, name: string) => (src.match(new RegExp(`\\b${name}\\(`, 'g')) ?? []).length

describe('AC-8: every owned-param write goes through a URL writer', () => {
  const appSrc = () => readFileSync(path.join(process.cwd(), 'src/App.tsx'), 'utf8')

  it('guard_everyAuditPrefilterWriteGoesThroughAUrlWriter', () => {
    const src = appSrc()
    expect(src, 'the scan read the wrong file').toContain('function navigate(view: View')
    expect(
      countCalls(src, 'setAuditPrefilter'),
      "App.tsx may call setAuditPrefilter from exactly four sites: navigate's functional updater, openAuditForInvoice, setAuditInvoiceFilter and the popstate restore. The first three write the URL in the same block; the popstate restore is the one exception -- it reads the URL instead",
    ).toBe(4)
    // The useState destructure is `setAuditPrefilter]` -- a `]` sits between the name and
    // the paren, so it is correctly not a call site.
    expect(src, 'sanity: the destructure the count must NOT see').toContain('setAuditPrefilter] = useState')
  })

  it('guard_theAuditPrefilterCountIgnoresAMereMention', () => {
    // Must-stay-green mutation, pure and in memory -- this test never writes a file. It
    // fails if countCalls above is rewritten to the bare-token form.
    const src = appSrc()
    const planted = src + '\n// unlike setAuditPrefilter, this effect writes no URL\n'
    expect(countCalls(planted, 'setAuditPrefilter'), 'a comment naming the setter must not move the count').toBe(
      countCalls(src, 'setAuditPrefilter'),
    )
    // Negative control: without it, the assertion above proves nothing about the regex CHOICE.
    const bareToken = (t: string) => (t.match(/\bsetAuditPrefilter\b/g) ?? []).length
    expect(bareToken(src), 'sanity: the bare-token form sees something to begin with').toBeGreaterThan(0)
    expect(bareToken(planted), 'the bare-token form DOES move on that comment -- which is why it is wrong').toBe(
      bareToken(src) + 1,
    )
  })

  it('guard_everyInvoiceQueryWriteGoesThroughAUrlWriter', () => {
    const src = appSrc()
    expect(src, 'the scan read the wrong file').toContain('function navigate(view: View')
    expect(
      countCalls(src, 'setInvoiceQuery'),
      'App.tsx may call setInvoiceQuery from exactly one site: the wrapper declaration itself. The raw state setter is setInvoiceQuery_',
    ).toBe(1)
    expect(src, 'sanity: the raw setter the count must NOT see').toContain('setInvoiceQuery_')
  })

  it('guard_theInvoiceQueryCountIgnoresAMereMention', () => {
    const src = appSrc()
    const planted = src + '\n// setInvoiceQuery is the clearing verb; searchInvoices is the committing one\n'
    expect(countCalls(planted, 'setInvoiceQuery'), 'a comment naming the setter must not move the count').toBe(
      countCalls(src, 'setInvoiceQuery'),
    )
    const bareToken = (t: string) => (t.match(/\bsetInvoiceQuery\b/g) ?? []).length
    expect(bareToken(src), 'sanity: the bare-token form sees something to begin with').toBeGreaterThan(0)
    expect(bareToken(planted), 'the bare-token form DOES move on that comment -- which is why it is wrong').toBe(
      bareToken(src) + 1,
    )
  })
})

// QA adversarial (ROUTE-04-03). Every row below is a mutation that survived the Mode A specs.

describe('QA adversarial: settingsTab durability, the half of the asymmetry no spec pinned', () => {
  // navigate resolves `params?.settingsTab ?? settingsTab`. Rewriting that fallback to a
  // screen-lifetime resolution survived the whole suite:
  // nav_aSettingsTabParamWinsOverTheCurrentState always passes a param, so it never reads the
  // fallback. This row reads only the fallback.
  it('nav_theSettingsTabIsDurableAcrossANavigationAway', async () => {
    await bootAt('/settings')
    await act(async () => {
      capturedCtx!.setSettingsTab('roles')
    })
    expect(window.location.pathname, 'sanity: the tab is on the URL before we leave').toBe('/settings/roles')

    await act(async () => {
      capturedCtx!.nav('workflows')
    })
    expect(window.location.pathname, 'sanity: we actually left').toBe('/workflows')

    await act(async () => {
      capturedCtx!.nav('settings')
    })
    const ctx = requireCtx()
    expect(window.location.pathname, 'durable: coming back with no param re-emits the tab').toBe('/settings/roles')
    expect(ctx.settingsTab, 'state never lost it either').toBe('roles')
  })
})

describe('QA adversarial: leaving Audit clears the filter atom on an ordinary nav', () => {
  // Deleting navigate's clear-on-leave branch red only
  // switchClient_leavesTheClearingToNavigateAndNeverWritesTheAtomItself. A plain sidebar nav
  // away from an armed filter had no oracle.
  it('nav_leavingAuditClearsTheFilterAtomAndEmitsNoParam', async () => {
    await bootAt('/invoices')
    // Armed off-screen: nothing on /invoices clears the atom, so the pair survives here and
    // the nav below is the only candidate eraser.
    await act(async () => {
      capturedCtx!.setAuditInvoiceFilter(INVOICE_ID, 'INV-1')
    })
    expect(requireCtx().auditPrefilter, 'sanity: the atom is armed').toEqual({
      invoiceId: INVOICE_ID,
      invoiceNumber: 'INV-1',
    })

    const pushSpy = vi.spyOn(window.history, 'pushState')
    await act(async () => {
      capturedCtx!.nav('workflows')
    })

    expect(requireCtx().auditPrefilter, 'a filter outlives nothing but its control').toBeNull()
    expect(recordedUrls(pushSpy), 'the pushed URL carries no invoice param').toEqual(['/workflows'])
  })
})

describe('QA adversarial: an empty committed search', () => {
  // Submitting an empty box is a navigation to the unfiltered list, not a clear, so it
  // pushes -- unlike setInvoiceQuery(''). The '' must not survive into the URL as `?q=`.
  it('search_anEmptyCommittedSearchPushesAndEmitsNoQuery', async () => {
    await bootAt('/invoices?q=acme')
    expect(requireCtx().invoiceQuery, 'sanity: the boot seed carries the term').toBe('acme')

    const lengthBefore = window.history.length
    const pushSpy = vi.spyOn(window.history, 'pushState')
    await act(async () => {
      capturedCtx!.searchInvoices('')
    })
    const ctx = requireCtx()

    expect(recordedUrls(pushSpy), 'an empty term must not serialise as ?q=').toEqual(['/invoices'])
    expect(window.location.pathname + window.location.search).toBe('/invoices')
    expect(window.history.length, 'a committed search is a navigation whatever the term').toBe(lengthBefore + 1)
    expect(ctx.invoiceQuery, "'' overrides the durable term rather than falling back to it").toBe('')
  })
})

describe('QA adversarial: two committed searches in a row', () => {
  it('search_twoConsecutiveSearchesLeaveTwoDistinctEntries', async () => {
    await bootAt('/')
    const lengthBefore = window.history.length
    const pushSpy = vi.spyOn(window.history, 'pushState')
    await act(async () => {
      capturedCtx!.searchInvoices('acme')
    })
    await act(async () => {
      capturedCtx!.searchInvoices('zenith')
    })

    // Two entries, not one: a refined search must not replace the term it refined, or Back
    // would skip it.
    expect(recordedUrls(pushSpy)).toEqual(['/invoices?q=acme', '/invoices?q=zenith'])
    expect(window.history.length).toBe(lengthBefore + 2)
    expect(window.location.pathname + window.location.search).toBe('/invoices?q=zenith')
  })
})

describe('QA adversarial: a tab click made off the Settings screen', () => {
  // setSettingsTab replaces routeUrl for the CURRENT view. It must correct the URL it is on,
  // never invent /settings/<tab> for a screen the user is not looking at.
  it('settings_aTabClickOffTheSettingsScreenNeverWritesASettingsUrl', async () => {
    await bootAt('/audit')
    const lengthBefore = window.history.length
    const pushSpy = vi.spyOn(window.history, 'pushState')
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await act(async () => {
      capturedCtx!.setSettingsTab('roles')
    })
    const ctx = requireCtx()

    expect(window.location.pathname + window.location.search, 'the address bar is still Audit').toBe('/audit')
    expect(recordedUrls(replaceSpy), 'no write may name a screen the user is not on').not.toContain('/settings/roles')
    expect(pushSpy.mock.calls, 'a tab click never pushes').toHaveLength(0)
    expect(window.history.length).toBe(lengthBefore)
    expect(ctx.settingsTab, 'the tab still lands in state, ready for the next nav to Settings').toBe('roles')
  })
})

// AC-8, extended. The shipped pair counts `setAuditPrefilter(` and `setInvoiceQuery(`, but
// `setInvoiceQuery` names the URL-AWARE WRAPPER, so its count is 1 -- the declaration itself.
// The raw useState setters are `setInvoiceQuery_` and `setSettingsTab_`, and a bare
// `setInvoiceQuery_('')` planted anywhere in App.tsx left the whole suite green. ROUTE-04-05's
// popstate restore bumps both counts to 3, deliberately -- it calls the raw setters directly
// (the wrapper `setInvoiceQuery` does its own replaceState, which AC-4 forbids the handler
// from doing).
describe('QA adversarial AC-8: the raw state setters are called only from the URL-aware verbs', () => {
  const appSrc = () => readFileSync(path.join(process.cwd(), 'src/App.tsx'), 'utf8')

  it('guard_theRawQueryAndTabSettersHaveExactlyThreeCallSitesEach', () => {
    const src = appSrc()
    expect(src, 'the scan read the wrong file').toContain('function navigate(view: View')
    expect(
      countCalls(src, 'setInvoiceQuery_'),
      'App.tsx may call setInvoiceQuery_ from exactly three sites: navigate, setInvoiceQuery and the popstate restore',
    ).toBe(3)
    expect(
      countCalls(src, 'setSettingsTab_'),
      'App.tsx may call setSettingsTab_ from exactly three sites: navigate, setSettingsTab and the popstate restore',
    ).toBe(3)
    // The destructures are `setInvoiceQuery_]` and `setSettingsTab_]` -- correctly not call
    // sites, and present, so the counts above run over a real population.
    expect(src, 'sanity: the destructure the count must NOT see').toContain('setInvoiceQuery_] = useState')
    expect(src, 'sanity: the destructure the count must NOT see').toContain('setSettingsTab_] = useState')
  })

  it('guard_theRawSetterCountsIgnoreAMereMention', () => {
    const src = appSrc()
    const planted = src + '\n// setInvoiceQuery_ and setSettingsTab_ are the raw atoms\n'
    for (const name of ['setInvoiceQuery_', 'setSettingsTab_']) {
      expect(countCalls(planted, name), `a comment naming ${name} must not move the count`).toBe(countCalls(src, name))
      const bareToken = (t: string) => (t.match(new RegExp(`\\b${name}\\b`, 'g')) ?? []).length
      expect(bareToken(src), `sanity: the bare-token form sees ${name} to begin with`).toBeGreaterThan(0)
      expect(bareToken(planted), 'the bare-token form DOES move on that comment -- which is why it is wrong').toBe(
        bareToken(src) + 1,
      )
    }
  })
})

// AC-7, the runtime half. lib/routeWriterGuard.test.ts forbids the literal `location.search`
// in these bodies, but a scan is evaded by spelling it `const loc = window.location; loc.search`
// -- and an echoing setInvoiceQuery written that way passed all 4218 specs. Same shape as
// nav_neverEchoesASearchStringThatAppearsAfterMount, which covers navigate and nothing else:
// inject an UNOWNED param AFTER mount (the boot alignment strips one at boot, so a boot-time
// `&injected=1` proves nothing) and pin the recorded argument exactly.
const INJECTED = 'injected=1'

describe('QA adversarial AC-7: the replace-class writers re-serialise, they never echo', () => {
  it('setInvoiceQuery_neverEchoesAQueryStringInjectedAfterMount', async () => {
    await bootAt('/invoices?q=acme')
    window.history.replaceState(null, '', `/invoices?q=acme&${INJECTED}`)

    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await act(async () => {
      capturedCtx!.setInvoiceQuery('')
    })

    const replaced = recordedUrls(replaceSpy)
    expect(replaced, 'no URL write happened at all').not.toHaveLength(0)
    expect(replaced, 'the clearing verb rebuilds the URL from state').toContain('/invoices')
    expect(replaced.filter((u) => u.includes(INJECTED)), 'an unowned param was echoed back').toEqual([])
  })

  it('settings_theTabWriteNeverEchoesAQueryStringInjectedAfterMount', async () => {
    await bootAt('/settings')
    window.history.replaceState(null, '', `/settings?${INJECTED}`)

    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await act(async () => {
      capturedCtx!.setSettingsTab('roles')
    })

    const replaced = recordedUrls(replaceSpy)
    expect(replaced).toContain('/settings/roles')
    expect(replaced.filter((u) => u.includes(INJECTED)), 'an unowned param was echoed back').toEqual([])
    expect(window.location.search, 'the live URL lost it too').toBe('')
  })

  it('auditFilter_theInScreenWriteNeverEchoesAQueryStringInjectedAfterMount', async () => {
    await bootAt(`/audit?invoice=${INVOICE_ID}`)
    window.history.replaceState(null, '', `/audit?invoice=${INVOICE_ID}&${INJECTED}`)

    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await act(async () => {
      capturedCtx!.setAuditInvoiceFilter(OTHER_INVOICE_ID, null)
    })

    const replaced = recordedUrls(replaceSpy)
    // The exact argument, not a prefix: an echo that merely swapped the id would keep
    // `&injected=1` trailing it.
    expect(replaced, 'the verb rebuilds the whole URL from the id it was handed').toContain(
      `/audit?invoice=${OTHER_INVOICE_ID}`,
    )
    expect(replaced.filter((u) => u.includes(INJECTED)), 'an unowned param was echoed back').toEqual([])
  })

  it('search_theCommittedSearchNeverEchoesAQueryStringInjectedAfterMount', async () => {
    await bootAt('/invoices?q=acme')
    window.history.replaceState(null, '', `/invoices?q=acme&${INJECTED}`)

    const pushSpy = vi.spyOn(window.history, 'pushState')
    await act(async () => {
      capturedCtx!.searchInvoices('zenith')
    })

    // navigate's own never-echo spec pushes with NO params; this is the params path.
    expect(recordedUrls(pushSpy)).toEqual(['/invoices?q=zenith'])
  })
})

// QA Mode B adversarial (task-919, ROUTE-02-04): D-4 only proved `selectInvoice` is gone
// from the REAL ctx. AC-2 also claims `selectedId` is gone, but nothing asserted that
// against the real Workspace-built ctx (only against local test-double stubs, which
// trivially lack a field never listed in their own literal). Assert it here instead.
describe('QA adversarial: ctx.selectedId is gone from the real ctx, not just test stubs (task-919)', () => {
  it('ctx exposes no selectedId key', async () => {
    await bootAt('/')
    const ctx = requireCtx()
    expect('selectedId' in ctx).toBe(false)
  })
})

// QA adversarial (route-02-06): subtask 05 deletes the scrub's fragment echo -- a live
// fragment on the entry being left no longer rides along; the scrub writes the plain
// scrubbed path only.
describe('QA adversarial (route-02-06): switchClient scrub no longer carries a live fragment on the entry being left', () => {
  it('switchClient_scrubDropsAnyLiveFragmentOnTheEntryBeingLeft', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.openExtraction(JOB_A)
    })
    window.history.replaceState(null, '', window.location.pathname + '#stale-fragment')

    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await act(async () => {
      capturedCtx!.switchClient('other-entity-777')
    })

    expect(replaceSpy.mock.calls[0]?.[2], 'the scrub is the FIRST replaceState this switch performs').toBe(
      '/invoices',
    )
  })
})

// QA adversarial (route-02-06): the scrub fires unconditionally in switchClient, even when
// `view` carries no id at all -- carryView('invoices') is a no-op collapse, and the scrub
// must not crash or write something nonsensical for that case.
describe('QA adversarial (route-02-06): switchClient from an id-less view still runs a sane scrub', () => {
  it('switchClient_fromAnIdLessViewWritesTheSameSanePathBeforePushingDashboard', async () => {
    await bootAt('/invoices')
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await act(async () => {
      capturedCtx!.switchClient('other-entity-555')
    })
    expect(
      replaceSpy.mock.calls[0]?.[2],
      'a view with no drill-down id to scrub still runs the collapse; carryView(invoices) is a no-op',
    ).toBe('/invoices')
    expect(window.location.pathname, 'switchClient must still land on the dashboard path').toBe('/')
  })
})

// QA adversarial (route-02-06): a second switchClient call reads `view` from the render
// committed after the first call (not a stale closure from before it), and each call's
// scrub only touches the entry IT is leaving, not the first switch's already-scrubbed one.
describe('QA adversarial (route-02-06): two switchClient calls in a row each scrub their own leaving entry', () => {
  it('switchClient_calledTwiceInARowAddsExactlyTwoEntries', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.openExtraction(JOB_A)
    })
    const lengthBefore = window.history.length

    await act(async () => {
      capturedCtx!.switchClient('entity-A')
    })
    await act(async () => {
      capturedCtx!.switchClient('entity-B')
    })

    const ctx = requireCtx()
    expect(window.history.length, 'two switches must add exactly two entries, not more or fewer').toBe(
      lengthBefore + 2,
    )
    expect(window.location.pathname, 'the second switch must still land on the dashboard path').toBe('/')
    expect(ctx.extractionJobId, 'the job id must still be cleared after two switches').toBeNull()
  })
})


// ROUTE-06-02 AC-7. A source scan, not a behavioural one: an unstamped Workspace writer
// mints an entry naming no company, and an entry naming no company can never be clamped.
// Nine sites, counted after the fix: the seven that ship today plus the boot-entry stamp
// backfill and the popstate clamp's own replaceState. Both new writers live inside
// Workspace, so they are inside this slice by construction.
// Out of the slice on purpose: signOut and the persona strip both live in App.
describe('ROUTE-06-02 AC-7: every Workspace history write carries the company stamp', () => {
  it('guard_everyWorkspaceHistoryWriteCarriesTheStamp', () => {
    const src = readFileSync(path.join(process.cwd(), 'src/App.tsx'), 'utf8')
    const startIdx = src.indexOf('function Workspace({ session,')
    const endIdx = src.indexOf('export default function App()')
    // Anchors first: a mis-anchored slice must fail naming the anchor, never fail the
    // count below for the wrong reason.
    expect(startIdx, 'the `function Workspace({ session,` anchor moved -- re-anchor, do not relax').toBeGreaterThan(-1)
    expect(endIdx, 'the `export default function App()` anchor moved -- re-anchor, do not relax').toBeGreaterThan(-1)
    expect(startIdx, 'the slice anchors are inverted').toBeLessThan(endIdx)
    const slice = src.slice(startIdx, endIdx)

    const sites = slice.match(/window\.history\.(?:push|replace)State\(/g) ?? []
    expect(sites, 'nine Workspace history writes are expected -- fewer means a mis-anchored slice or a lost writer').toHaveLength(9)
    const unstamped = slice.match(/window\.history\.(?:push|replace)State\(\s*null\s*,/g) ?? []
    expect(unstamped, 'zero Workspace history writes may still pass a literal null first argument').toEqual([])
  })
})

// --- ROUTE-06-03: import state does not survive a company switch --------------------
//
// Same vacuity as the ROUTE-06-02 block above: this file boots with no gateway, so
// active.entityId is null before AND after switchClient, and every spec below would
// compare null to null. Ported idiom: App.routePopstate.test.tsx:972-1075.

const GATEWAY = 'https://gateway.test'
const ENTITY_A = 'e1111111-1111-4111-8111-111111111111'
const ENTITY_B = 'e2222222-2222-4222-8222-222222222222'

function entityRow(id: string, name: string, tin: string) {
  return { id, name, tin, registration: null, sector: null, address: null, status: 'active', created_at: '2026-01-01T00:00:00Z' }
}

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
              entities: [entityRow(ENTITY_A, 'Reset Co A', '12345678-0001'), entityRow(ENTITY_B, 'Reset Co B', '12345678-0002')],
              pagination: { limit: 200, offset: 0, total: 2 },
            }),
        })
      }
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () =>
          Promise.resolve({
            access_token: 'test-token',
            tenant: { id: 't-reset', name: 'Reset Tenant' },
            entities: [],
            policies: [],
            members: [],
            roles: [],
            invoices: [],
            clients: [],
            totals: EMPTY_BUCKET,
            events: [],
            facets: { events: [], actors: [], companies: [] },
            page: { limit: 50, has_more: false, next_cursor: null },
            log_is_empty: true,
            rejection_reasons: [],
            total: 0,
            pagination: { limit: 50, offset: 0, total: 0 },
          }),
      })
    }),
  )
}

async function bootAtWithGateway(path: string) {
  routeFetch()
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  const rendered = await bootAt(path)
  await waitFor(() =>
    expect(requireCtx().active.entityId, 'the roster never resolved -- every spec below would be vacuous').toBe(
      ENTITY_A,
    ),
  )
  return rendered
}

describe('ROUTE-06-03 harness floor: the two-entity roster', () => {
  it('roster_theTwoEntityRosterActuallyMovesTheActiveCompany', async () => {
    await bootAtWithGateway('/')
    expect(requireCtx().active.entityId, 'before the switch the active company must be entity A').toBe(ENTITY_A)

    await act(async () => {
      capturedCtx!.switchClient(ENTITY_B)
    })
    expect(requireCtx().active.entityId, 'after the switch the active company must be entity B').toBe(ENTITY_B)
  })
})

describe('ROUTE-06-03 AC-1: switchClient clears every import atom', () => {
  it('switchClient_clearsEveryImportAtom', async () => {
    await bootAtWithGateway('/')
    await act(async () => {
      capturedCtx!.openCreate()
    })
    const overCap = Array.from(
      { length: MAX_RUN_FILES + 1 },
      (_, i) => new File(['invoice_number,total\nINV-1,100'], `invoice-${i}.csv`, { type: 'text/csv' }),
    )
    await act(async () => {
      capturedCtx!.addPickedFiles(overCap)
    })
    let ctx = requireCtx()
    // Genuinely armed, not already empty: an over-cap set trips addFiles' refusal.
    expect(ctx.pickedFiles.length, 'sanity: the run must have picked files before the switch').toBeGreaterThan(0)
    expect(ctx.filesRefusal, 'sanity: the over-cap set must have tripped a refusal').not.toBeNull()

    await act(async () => {
      capturedCtx!.switchClient(ENTITY_B)
    })
    ctx = requireCtx()
    expect(ctx.pickedFiles, 'pickedFiles must not survive a company switch').toEqual([])
    expect(ctx.filesRefusal, 'filesRefusal must not survive a company switch').toBeNull()

    // groups/groupIndex/run/importError cannot be armed in this harness -- no preview or
    // run gateway exists to arm them. Resting-value pins only (file's own idiom, :288-290),
    // not coverage that switchClient clears them.
    expect(ctx.groups, 'resting-value pin, not coverage: never armed in this harness').toEqual([])
    expect(ctx.groupIndex, 'resting-value pin, not coverage: never armed in this harness').toBe(0)
    expect(ctx.run, 'resting-value pin, not coverage: never armed in this harness').toEqual({
      files: [],
      cursor: 0,
      status: 'idle',
    })
    expect(ctx.importError, 'resting-value pin, not coverage: never armed in this harness').toBeNull()
  })
})

describe('ROUTE-06-03 AC-2: switchClient seeds the import target with the incoming company', () => {
  it('switchClient_seedsTheImportTargetWithTheINCOMINGCompany', async () => {
    await bootAtWithGateway('/')
    await act(async () => {
      capturedCtx!.openCreate()
    })
    expect(requireCtx().entityId, 'sanity: openCreate seeds entityId from the active company').toBe(ENTITY_A)

    await act(async () => {
      capturedCtx!.switchClient(ENTITY_B)
    })
    expect(requireCtx().entityId, 'entityId after switchClient(B) must be B -- never A, never null').toBe(ENTITY_B)
  })
})

// GREEN before the fix -- the control that a widened resetImport's default parameter
// leaves openCreate's existing bare call exactly as it behaves today.
describe('ROUTE-06-03 AC-3 control: openCreate still seeds from active with no argument', () => {
  it('openCreate_stillSeedsFromActiveWithNoArgument', async () => {
    await bootAtWithGateway('/')
    await act(async () => {
      capturedCtx!.openCreate()
    })
    expect(requireCtx().entityId, 'openCreate with no argument must still seed from active.entityId').toBe(ENTITY_A)
  })
})

// GREEN before the fix -- the re-seed effect (App.tsx:529-533) is untouched by this
// subtask. A deferred entities fetch reproduces the real race (App.tsx:502-528): the
// snapshot lands null while the entity is still pending, then the effect fills it once
// active resolves.
describe('ROUTE-06-03 AC-4 control: the null-to-resolved reseed effect is untouched', () => {
  it('reseed_theNullToResolvedEffectStillFillsAStaleNullSnapshot', async () => {
    let resolveEntities: () => void = () => {}
    const entitiesGate = new Promise<void>((resolve) => {
      resolveEntities = resolve
    })
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) => {
        if (url.includes('/portfolio/v1/entities')) {
          return entitiesGate.then(() => ({
            ok: true,
            status: 200,
            json: () =>
              Promise.resolve({
                entities: [entityRow(ENTITY_A, 'Reset Co A', '12345678-0001')],
                pagination: { limit: 200, offset: 0, total: 1 },
              }),
          }))
        }
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () =>
            Promise.resolve({
              access_token: 'test-token',
              tenant: { id: 't-reset', name: 'Reset Tenant' },
              entities: [],
              policies: [],
              members: [],
              roles: [],
              invoices: [],
              clients: [],
              totals: EMPTY_BUCKET,
              events: [],
              facets: { events: [], actors: [], companies: [] },
              page: { limit: 50, has_more: false, next_cursor: null },
              log_is_empty: true,
              rejection_reasons: [],
              total: 0,
              pagination: { limit: 50, offset: 0, total: 0 },
            }),
        })
      }),
    )
    vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
    await bootAt('/')

    await act(async () => {
      capturedCtx!.openCreate()
    })
    const ctx = requireCtx()
    expect(ctx.createStep, 'sanity: openCreate opens on the upload step').toBe('upload')
    expect(ctx.entityId, 'sanity: the snapshot raced the pending entities fetch and landed null').toBeNull()

    await act(async () => {
      resolveEntities()
    })
    await waitFor(() =>
      expect(requireCtx().entityId, 'the re-seed effect must fill the stale null once active resolves').toBe(
        ENTITY_A,
      ),
    )
  })
})
