// @vitest-environment jsdom
// vitest.config.ts stays `environment: 'node'` for every other suite.
//
// navigate() and the eight setView( call sites. Harness is App.routeBoot.test.tsx's: the
// real <App/>, a session in a stubbed localStorage, ctx captured through a mocked Sidebar.

import { execSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import path from 'node:path'

import { act, cleanup, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { ROUTE_PATHS } from './lib/route'
import type { Member } from './lib/members'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { AuditPrefilter, PlatformCtx, View } from './types'

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const REVIEW_ID = 'a1b2c3d4-e5f6-47a8-89ab-cdef01234567'
const INVOICE_ID = 'aaaaaaaa-0000-4000-8000-000000000001'
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

type RenderEntry = { view: View; prefilter: AuditPrefilter | null; jobId: string | null }

let capturedCtx: PlatformCtx | undefined
const renders: RenderEntry[] = []
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
    renders.push({ view: p.ctx.view, prefilter: p.ctx.auditPrefilter, jobId: p.ctx.extractionJobId })
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
  const app = <App />
  return render(app)
}

// The control needle for guard_everyAppRenderingTestFileResetsTheJsdomUrl: renders with
// WHATEVER window.location already is, unlike bootAt above which always sets it first.
async function renderWithoutUrlReset() {
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  vi.resetModules()
  const { default: App } = await import('./App')
  const app = <App />
  return render(app)
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
    expect(window.location.pathname, 'openImportedInvoice must push /invoice').toBe('/invoice')
    expect(window.history.length, 'openImportedInvoice must add exactly one history entry').toBe(lengthBefore + 1)
    expect(ctx.importedInvoiceId, 'the selection atom must name the id it was handed').toBe(INVOICE_ID)
  })

  it('selectInvoice_pushesTheDetailPath', async () => {
    await bootAt('/')
    const lengthBefore = window.history.length
    await act(async () => {
      capturedCtx!.selectInvoice('INV-1')
    })
    const ctx = requireCtx()
    expect(window.location.pathname, 'selectInvoice must push /invoice').toBe('/invoice')
    expect(window.history.length, 'selectInvoice must add exactly one history entry').toBe(lengthBefore + 1)
    expect(ctx.selectedId, 'the mock selection atom must name the number it was handed').toBe('INV-1')
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
    await act(async () => {
      capturedCtx!.openExtraction(JOB_A)
    })
    expect(window.location.pathname, 'openExtraction must push /extraction').toBe('/extraction')

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
    await bootAt(`/create#review/${REVIEW_ID}`)
    let ctx = requireCtx()
    // Sanity: the review hash seeds reviewBatchIds -- this is the atom switchClient must
    // still clear, unrelated to any URL work this subtask does.
    expect(ctx.reviewBatchIds, 'sanity: the review hash must seed reviewBatchIds').toEqual([REVIEW_ID])

    await act(async () => {
      capturedCtx!.openRule('late-fee')
    })
    await act(async () => {
      capturedCtx!.openPolicy('policy-1')
    })
    await act(async () => {
      capturedCtx!.selectInvoice('INV-9')
    })
    ctx = requireCtx()
    expect(ctx.selectedId, 'sanity: selectInvoice must have armed selectedId').toBe('INV-9')

    const lengthBefore = window.history.length
    await act(async () => {
      capturedCtx!.switchClient('other-entity-002')
    })
    ctx = requireCtx()

    expect(window.location.pathname, 'switchClient must push the dashboard path').toBe('/')
    expect(window.history.length, 'switchClient must add exactly one history entry').toBe(lengthBefore + 1)
    expect(ctx.reviewBatchIds, 'reviewBatchIds must still be cleared').toEqual([])
    expect(ctx.selectedId, 'selectedId must still be cleared').toBeNull()
    expect(ctx.importedInvoiceId, 'importedInvoiceId must still be cleared').toBeNull()
    expect(ctx.createStep, 'createStep must still reset to form').toBe('form')
    expect(ctx.openRuleKey, 'openRuleKey must still be cleared').toBeNull()
    expect(ctx.editingPolicyId, 'editingPolicyId must still be cleared').toBeNull()
    // filingError cannot be armed in this harness (no gateway to reject against) -- this
    // pins that switchClient leaves it at its resting value, not a fresh dirtying.
    expect(ctx.filingError, 'filingError must stay null').toBeNull()
  })
})

describe('AC-2: no bare setView( call site survives outside navigate()', () => {
  it('guard_appTsxHasNoSetViewCallOutsideNavigate', () => {
    const src = readFileSync(path.join(process.cwd(), 'src/App.tsx'), 'utf8')
    const matches = src.match(/\bsetView\b/g) ?? []
    // The 2 that must remain: the `[view, setView]` useState destructure, and navigate's
    // own `setView(view)` call. Every one of the 8 pre-existing call sites becomes a
    // `navigate(...)` call and so drops out of this count.
    expect(
      matches.length,
      `App.tsx has ${matches.length} bare 'setView' references; navigate() must be the only caller`,
    ).toBe(2)
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
})

describe('AC-6: every existing <App /> test file resets the jsdom URL', () => {
  it('guard_everyAppRenderingTestFileResetsTheJsdomUrl', () => {
    // Split so THIS file's own source text never contains the needle it greps for --
    // otherwise this test would find itself as a 6th match and never read exactly 5.
    const needle = ['render(<App', ' '].join('')
    const out = execSync(`grep -rln "${needle}" src`, { cwd: process.cwd(), encoding: 'utf8' })
    const files = out
      .trim()
      .split('\n')
      .filter(Boolean)
    // Floor: a broken walk (wrong cwd, a mangled grep pattern) returns zero files and
    // reads exactly like a repo with nothing left to fix.
    expect(files, 'the walk must find exactly the five pre-existing App-rendering test files').toHaveLength(5)

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
    expect(window.location.pathname, 'sanity: openExtraction must have pushed /extraction').toBe('/extraction')
    expect(ctx.extractionJobId, 'sanity: the job id was never written').toBe(JOB_A)

    await act(async () => {
      capturedCtx!.switchClient('other-entity-003')
    })
    ctx = requireCtx()

    expect(ctx.extractionJobId, 'the job id must not survive the company switch').toBeNull()

    // The fence: exactly one atom clears. auditPrefilter was never armed (it never
    // survives a single commit, decision [company-switch-staleness]) and createStep's
    // reset to 'form' is switchClient's OWN pre-existing behaviour, unrelated to this fix.
    expect(ctx.auditPrefilter, 'auditPrefilter must behave exactly as it does on main').toBeNull()
    expect(ctx.createStep, 'createStep must behave exactly as it does on main').toBe('form')
  })

  // Closes a gap QA found by mutation: auditPrefilter is never armed in the test above, so
  // a switchClient that ALSO cleared it would pass unnoticed -- over-clearing is as much a
  // Q6 violation as under-clearing. Both handlers fire inside ONE act() so React batches
  // them into a single commit with view === 'dashboard', never 'audit' -- the consume-once
  // effect (App.tsx:545-547) only fires when view === 'audit', so it cannot intervene and
  // mask a switchClient regression here.
  it('switchClient_leavesAuditPrefilterUntouchedEvenWhenArmedInTheSameCommit', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.openAuditForInvoice(INVOICE_ID, 'INV-1')
      capturedCtx!.switchClient('other-entity-005')
    })
    const ctx = requireCtx()
    expect(window.location.pathname, 'switchClient must still win the URL').toBe('/')
    expect(ctx.auditPrefilter, 'switchClient must not clear an atom armed in the same commit').toEqual({
      invoiceId: INVOICE_ID,
      invoiceNumber: 'INV-1',
    })
  })
})

describe('QA adversarial: navigate() carries the hash even though the review mirror clears it after', () => {
  it('nav_thePushCallCarriesWhateverHashWasLiveAtCallTime', async () => {
    // A real review hash, not an arbitrary fragment: an unrecognised fragment is stripped
    // by the PRE-EXISTING review mirror (App.tsx:533-539) on the very first mount, which
    // would confound this test before navigate() ever runs.
    await bootAt(`/create#review/${REVIEW_ID}`)
    const pushSpy = vi.spyOn(window.history, 'pushState')
    await act(async () => {
      capturedCtx!.nav('audit')
    })
    const call = pushSpy.mock.calls.find((c) => typeof c[2] === 'string' && c[2].startsWith('/audit'))
    expect(call, 'no pushState call to /audit was recorded').toBeDefined()
    // Read the SPY's recorded argument, not the settled DOM: the review mirror clears the
    // hash in the very next effect once view leaves 'create' (ROUTE-01-06's own AC), which
    // would mask a navigate() that dropped the hash on its own push.
    expect(
      call![2],
      "navigate() must carry forward whatever hash was live at call time, per decision [one-writer-rule]",
    ).toBe(`/audit#review/${REVIEW_ID}`)
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
    expect(window.location.pathname, 'sanity').toBe('/extraction')
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
