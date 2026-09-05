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

// Stubbed so a cold /extraction boot (no gateway) never tries to fetch or paint a canvas --
// only whether it MOUNTS at all is under test in switchClient_clearsTheExtractionJob....
const { reviewMounts } = vi.hoisted(() => ({ reviewMounts: [] as unknown[] }))
vi.mock('./components/ExtractionReview', () => ({
  ExtractionReview: (p: unknown) => {
    reviewMounts.push(p)
    return null
  },
}))

beforeEach(() => {
  capturedCtx = undefined
  renders.length = 0
  reviewMounts.length = 0
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
  it('switchClient_clearsTheExtractionJobSoBackCannotReachTheCompanyJustLeft', async () => {
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

    // Simulates pressing Back from the post-switch dashboard to the /extraction entry
    // openExtraction pushed. The popstate LISTENER is ROUTE-01-04's, not this subtask's --
    // until it lands, this row stays red on the view/pathname mismatch even once the
    // extractionJobId clear below is wired.
    window.history.replaceState(null, '', '/extraction')
    window.dispatchEvent(new PopStateEvent('popstate'))
    ctx = requireCtx()

    expect(ctx.view, 'Back must land on the extraction view').toBe('extraction')
    expect(ctx.extractionJobId, 'the job id must not survive the company switch').toBeNull()
    expect(reviewMounts, 'no ExtractionReview must render with a null job id').toHaveLength(0)

    // The fence: exactly one atom clears. auditPrefilter was never armed (it never
    // survives a single commit, decision [company-switch-staleness]) and createStep's
    // reset to 'form' is switchClient's OWN pre-existing behaviour, unrelated to this fix.
    expect(ctx.auditPrefilter, 'auditPrefilter must behave exactly as it does on main').toBeNull()
    expect(ctx.createStep, 'createStep must behave exactly as it does on main').toBe('form')
  })
})
