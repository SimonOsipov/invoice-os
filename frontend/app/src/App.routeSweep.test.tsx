// @vitest-environment jsdom
// vitest.config.ts stays `environment: 'node'` for every other suite.
//
// The route sweep: one ledger, total over the imported ROUTE_PATHS table, plus the rows
// the ledger claims for itself. Harness copied from App.routePopstate.test.tsx (`bootAt`,
// `popTo`, the mocked Sidebar, the mount recorders) -- there is no shared helper module in
// this package, so copy-per-file with a comment naming the source is the idiom here.
//
// No two-entity roster on any row: nothing here calls switchClient, and `popTo` writes
// replaceState(null, ...), so ROUTE-06-02's identity clamp is a no-op throughout.
//
// The two bare-path limitations are documented elsewhere and not re-asserted here:
// boot_detailWithNoSelectionColdBootsToTheEmptyState and
// boot_extractionWithNoJobIdRendersNothing, both in App.routeBoot.test.tsx.

import { readFileSync } from 'node:fs'
import path from 'node:path'

import { act, cleanup, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { ALL_13 } from './App.atomAudit.data'
import { ROUTE_PATHS } from './lib/route'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx, View } from './types'

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const INVOICE_ID = 'aaaaaaaa-0000-4000-8000-000000000001'
const JOB_A = 'c3d4e5f6-a7b8-4c3d-9e4f-5a6b7c8d9e0f'

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

// Recorders, not just ctx: a Forward that restores the atom but rebuilds the screen with a
// blank id would pass a ctx-only assertion.
const { extractionReviewMounts } = vi.hoisted(() => ({ extractionReviewMounts: [] as unknown[] }))
vi.mock('./components/ExtractionReview', () => ({
  ExtractionReview: (p: { jobId: unknown }) => {
    extractionReviewMounts.push(p.jobId)
    return null
  },
}))

const { invoiceDetailMounts } = vi.hoisted(() => ({ invoiceDetailMounts: [] as unknown[] }))
vi.mock('./components/InvoiceDetail', () => ({
  InvoiceDetail: (p: { ctx: { importedInvoiceId: unknown } }) => {
    invoiceDetailMounts.push(p.ctx.importedInvoiceId)
    return null
  },
}))

beforeEach(() => {
  capturedCtx = undefined
  extractionReviewMounts.length = 0
  invoiceDetailMounts.length = 0
  window.history.replaceState(null, '', '/')
  vi.stubGlobal('localStorage', createMemoryStorage())
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

// vi.resetModules() + the dynamic import are load-bearing: VITE_DEMO_MODE is read at
// import time, so a statically imported App would freeze it at the first boot.
async function bootAt(pathname: string) {
  window.history.replaceState(null, '', pathname)
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  vi.resetModules()
  const { default: App } = await import('./App')
  return render(<App />)
}

function requireCtx(): PlatformCtx {
  expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()
  return capturedCtx!
}

// jsdom runs no real history stack for back()/forward() -- move the URL the way the
// browser would, then fire the event the browser fires. Copied from
// App.routePopstate.test.tsx:118.
async function popTo(pathname: string) {
  window.history.replaceState(null, '', pathname)
  await act(async () => {
    window.dispatchEvent(new PopStateEvent('popstate'))
  })
}

// ---------------------------------------------------------------------------
// The ledger
// ---------------------------------------------------------------------------

// A cell is either a named spec that proves it (here or elsewhere, both verified on disk)
// or an explicit refusal carrying its reason. There is no third state: a row cannot be
// quietly skipped.
type Cell = { test: string; file: string } | { na: string }

const SELF = 'src/App.routeSweep.test.tsx'
const BOOT = 'src/App.routeBoot.test.tsx'
const NAVIGATE = 'src/App.routeNavigate.test.tsx'
const POPSTATE = 'src/App.routePopstate.test.tsx'
const DEEPLINK = 'src/App.signedOutDeepLink.test.tsx'
const CODEC = 'src/lib/route.test.ts'

interface Row {
  coldBoot: Cell
  navUrl: Cell
  back: Cell
  forward: Cell
}

// Forward onto a plain top-level view has no oracle at this layer: popTo is replaceState +
// a synthetic PopStateEvent, so a Forward row would be a byte-identical copy of that view's
// Back row. It is a browser-layer claim (docs/e2e-convention.md), asserted deployed for two
// of the thirteen by 'deployed app: Forward re-applies the view Back left'.
const FORWARD_IS_BACK_AT_THIS_LAYER = {
  na: 'jsdom has no history stack; Forward onto a bare view is the same synthetic PopStateEvent as its Back row and adds no discriminating power. Browser layer owns it.',
}

const COLD_BOOT_LOOP: Cell = { test: 'boot_everyNonDefaultPathSeedsItsOwnView', file: BOOT }
const NAV_LOOP: Cell = { test: 'nav_everySidebarViewPushesItsOwnPath', file: NAVIGATE }
const BACK_HERE: Cell = { test: 'sweep_everyRoutePathSurvivesABackOntoIt', file: SELF }

const plainRow = (coldBoot: Cell = COLD_BOOT_LOOP): Row => ({
  coldBoot,
  navUrl: NAV_LOOP,
  back: BACK_HERE,
  forward: FORWARD_IS_BACK_AT_THIS_LAYER,
})

// The Back column is `here` on all thirteen deliberately. Measured on this head, the
// pre-existing Back coverage asserts AC-2's view+pathname pair almost nowhere: six views
// (approvals, rules, customers, reports, workflows, clients) had no popstate spec at all,
// and dashboard/create/detail/settings each assert one half.
const SWEEP: Record<View, Row> = {
  // The route-boot loop filters dashboard out; the signed-out deep-link suite covers it,
  // asserting view AND pathname on a bare-root boot.
  dashboard: plainRow({ test: 'restore_noStoredDestinationBootsToDashboardAsToday', file: DEEPLINK }),
  invoices: plainRow(),
  approvals: plainRow(),
  rules: plainRow(),
  customers: plainRow(),
  reports: plainRow(),
  workflows: plainRow(),
  clients: plainRow(),
  audit: plainRow(),
  settings: plainRow(),
  create: plainRow(),
  detail: plainRow(),
  extraction: plainRow(),
}

// The four parameterised forms. Each names the ROUTE_PATHS key it extends, so a form
// cannot outlive the base view it drills into.
const PARAMETERISED: { form: string; base: View; row: Row }[] = [
  {
    form: '/invoices/:id',
    base: 'detail',
    row: {
      coldBoot: { test: 'boot_detailPathSeedsTheIdOnTheFirstCommittedRender', file: BOOT },
      navUrl: { test: 'openImportedInvoice_pushesTheDetailPathAndKeepsTheSelection', file: NAVIGATE },
      back: { test: 'popstate_backOntoInvoiceDetailRestoresTheImportedId', file: POPSTATE },
      forward: { test: 'sweep_forwardRebuildsTheInvoiceDetailScreen', file: SELF },
    },
  },
  {
    form: '/extraction/:jobId',
    base: 'extraction',
    row: {
      coldBoot: { test: 'boot_extractionPathSeedsTheJobIdAndMountsTheReviewScreen', file: BOOT },
      navUrl: { test: 'openExtraction_pushesExtractionWithTheJobIdInTheSameRender', file: NAVIGATE },
      back: { test: 'popstate_backOntoExtractionRestoresTheJobId', file: POPSTATE },
      forward: { test: 'sweep_forwardRebuildsTheExtractionScreen', file: SELF },
    },
  },
  {
    form: '/imports/<ids>/review',
    base: 'create',
    row: {
      coldBoot: { test: 'boot_aReviewPathSeedsTheScreenAndSurvivesTheAlignment', file: BOOT },
      // The review path is never pushed by an in-app write: the mount alignment replaces
      // with it, which is what this citation pins.
      navUrl: { test: 'boot_theAlignmentCarriesTheReviewIdsItself', file: BOOT },
      back: { test: 'popstate_backOntoAReviewEntryRestoresThatBatchNotTheLiveOne', file: POPSTATE },
      forward: { test: 'popstate_forwardReAppliesTheReviewRoute', file: POPSTATE },
    },
  },
  {
    form: '/settings/:tab',
    base: 'settings',
    row: {
      coldBoot: { test: 'boot_settingsTabSeedsFromThePathSegment', file: BOOT },
      // A tab click replaces rather than pushes.
      navUrl: { test: 'settings_aTabClickReplacesAndAddsNoEntry', file: NAVIGATE },
      // popstate_backRestoresTheSettingsTab / popstate_forwardReAppliesTheTab assert the
      // tab atom only -- neither reads ctx.view or the pathname, so neither proves the
      // route restored. Asserted here instead.
      back: { test: 'sweep_theSettingsTabPathRestoresViewAndPathnameOnBackAndForward', file: SELF },
      forward: { test: 'sweep_theSettingsTabPathRestoresViewAndPathnameOnBackAndForward', file: SELF },
    },
  },
]

const COLUMNS: (keyof Row)[] = ['coldBoot', 'navUrl', 'back', 'forward']

// The other guards that iterate the same imported table. Named so a rename of any of them
// surfaces here rather than quietly halving the totality discipline.
const SIBLING_TOTALITY_GUARDS: Cell[] = [
  { test: 'routeTable_isTotalOverTheThirteenViews', file: CODEC },
  COLD_BOOT_LOOP,
  NAV_LOOP,
]

// Reads the cited file off disk and asserts the spec name is really in it. Idiom from
// guard_everyAppRenderingTestFileResetsTheJsdomUrl (App.routeNavigate.test.tsx:426).
// readFileSync, not grep: src/lib/route.test.ts holds a non-UTF-8 byte, so file(1) calls it
// `data` and plain grep skips it silently.
function verifyCell(label: string, cell: Cell, problems: string[]): boolean {
  if ('na' in cell) {
    if (cell.na.trim().length < 20) problems.push(`${label}: refused with no usable reason`)
    return false
  }
  const src = readFileSync(path.join(process.cwd(), cell.file), 'utf8')
  if (!src.includes(cell.test)) problems.push(`${label}: '${cell.test}' is not in ${cell.file}`)
  return true
}

describe('AC-4, AC-5: the ledger is total over the imported route table', () => {
  it('sweep_theLedgerIsTotalOverTheRouteTable', () => {
    const tableKeys = Object.keys(ROUTE_PATHS).sort()
    // Vacuity floor: a mis-resolved import gives {}, and every diff below then compares
    // nothing against nothing and passes.
    expect(tableKeys.length, 'ROUTE_PATHS did not resolve to the real thirteen-view table').toBeGreaterThan(12)

    const ledgerKeys = Object.keys(SWEEP).sort()
    const missing = tableKeys.filter((k) => !ledgerKeys.includes(k))
    const extra = ledgerKeys.filter((k) => !tableKeys.includes(k))
    expect({ missing, extra }, 'the ledger and ROUTE_PATHS must agree in both directions').toEqual({
      missing: [],
      extra: [],
    })

    // The fourth copy of the route table. App.atomAudit.data.ts retypes all thirteen paths
    // by hand; a fourteenth key would leave it silently stale, which is the exact drift
    // this sweep exists to catch.
    expect([...ALL_13].sort(), 'App.atomAudit.data.ts ALL_13 has drifted from ROUTE_PATHS').toEqual(
      Object.values(ROUTE_PATHS).sort(),
    )

    const problems: string[] = []
    let verified = 0
    for (const guard of SIBLING_TOTALITY_GUARDS) verifyCell('sibling guard', guard, problems)
    for (const [view, row] of Object.entries(SWEEP) as [View, Row][]) {
      for (const col of COLUMNS) {
        if (verifyCell(`${view}.${col}`, row[col], problems)) verified += 1
      }
    }
    expect(problems, 'every ledger cell must be a verified citation or a stated refusal').toEqual([])
    // Floor: thirteen rows x four columns, minus the thirteen refused Forward cells.
    expect(verified, 'the ledger verified far too few cells to be total').toBe(13 * COLUMNS.length - 13)
  })

  it('sweep_theParameterisedLedgerIsTotalOverTheFourForms', () => {
    expect(PARAMETERISED, 'the four parameterised forms').toHaveLength(4)
    expect(new Set(PARAMETERISED.map((p) => p.form)).size, 'the forms must be distinct').toBe(4)

    const problems: string[] = []
    let verified = 0
    for (const { form, base, row } of PARAMETERISED) {
      if (!(base in ROUTE_PATHS)) problems.push(`${form}: base view '${base}' is not a ROUTE_PATHS key`)
      for (const col of COLUMNS) {
        if (verifyCell(`${form}.${col}`, row[col], problems)) verified += 1
      }
    }
    expect(problems, 'every parameterised cell must be a verified citation').toEqual([])
    expect(verified, 'no parameterised cell may be refused').toBe(4 * COLUMNS.length)
  })
})

describe('AC-2: every route path survives a Back onto it, view and pathname together', () => {
  it('sweep_everyRoutePathSurvivesABackOntoIt', async () => {
    const views = Object.keys(ROUTE_PATHS) as View[]
    // Vacuity floor: an empty table makes both loops below no-ops.
    expect(views.length, 'ROUTE_PATHS did not resolve to the real thirteen-view table').toBeGreaterThan(12)

    await bootAt('/')
    for (const v of views) {
      await act(async () => {
        capturedCtx!.nav(v)
      })
    }
    // Anchor: without it the first Back below would land on the path already showing.
    await act(async () => {
      capturedCtx!.nav('dashboard')
    })

    // Collected, not thrown per row: a handler that breaks exactly one view must name that
    // view alone, which a bail-on-first-failure loop cannot show.
    const problems: string[] = []
    for (const v of [...views].reverse()) {
      const expected = ROUTE_PATHS[v]
      await popTo(expected)
      const ctx = requireCtx()
      if (ctx.view !== v) problems.push(`Back onto ${expected}: view is '${ctx.view}', expected '${v}'`)
      if (window.location.pathname !== expected) {
        problems.push(`Back onto ${expected}: pathname is '${window.location.pathname}'`)
      }
    }
    expect(problems, 'every route path must restore its own view AND its own pathname').toEqual([])
  })
})

describe('AC-3: Forward re-applies a parameterised route, screen and all', () => {
  it('sweep_forwardRebuildsTheInvoiceDetailScreen', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.openImportedInvoice(INVOICE_ID)
    })
    await act(async () => {
      capturedCtx!.nav('audit')
    })

    await popTo(`/invoices/${INVOICE_ID}`)
    await popTo('/')
    // Floor: Forward proves nothing unless the Back really left the detail screen first.
    expect(requireCtx().view, 'the Back hops must land back on the root').toBe('dashboard')
    expect(requireCtx().importedInvoiceId, 'the Back onto the root must have cleared the id').toBeNull()
    invoiceDetailMounts.length = 0

    await popTo(`/invoices/${INVOICE_ID}`)
    const ctx = requireCtx()
    expect(ctx.view, 'Forward must re-apply the detail view').toBe('detail')
    expect(ctx.importedInvoiceId, 'Forward must re-apply the id, not just the view').toBe(INVOICE_ID)
    expect(invoiceDetailMounts.length, 'the detail screen never rebuilt on the forward hop').toBeGreaterThan(0)
    expect(
      invoiceDetailMounts[invoiceDetailMounts.length - 1],
      'the rebuilt detail screen must carry the id, not a blank selection',
    ).toBe(INVOICE_ID)
    expect(window.location.pathname, 'the URL must be the forward entry').toBe(`/invoices/${INVOICE_ID}`)
  })

  it('sweep_forwardRebuildsTheExtractionScreen', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.openExtraction(JOB_A)
    })
    await act(async () => {
      capturedCtx!.nav('audit')
    })

    await popTo(`/extraction/${JOB_A}`)
    await popTo('/')
    expect(requireCtx().view, 'the Back hops must land back on the root').toBe('dashboard')
    expect(requireCtx().extractionJobId, 'the Back onto the root must have cleared the job id').toBeNull()
    extractionReviewMounts.length = 0

    await popTo(`/extraction/${JOB_A}`)
    const ctx = requireCtx()
    expect(ctx.view, 'Forward must re-apply the extraction view').toBe('extraction')
    expect(ctx.extractionJobId, 'Forward must re-apply the job id, not just the view').toBe(JOB_A)
    expect(extractionReviewMounts.length, 'the review screen never rebuilt on the forward hop').toBeGreaterThan(0)
    expect(
      extractionReviewMounts[extractionReviewMounts.length - 1],
      'the rebuilt review screen must carry the job id',
    ).toBe(JOB_A)
    expect(window.location.pathname, 'the URL must be the forward entry').toBe(`/extraction/${JOB_A}`)
  })
})

describe('AC-4: /settings/:tab restores the route, not only the tab', () => {
  it('sweep_theSettingsTabPathRestoresViewAndPathnameOnBackAndForward', async () => {
    await bootAt('/settings/roles')
    expect(requireCtx().settingsTab, 'sanity: the boot must seed the roles tab').toBe('roles')

    await act(async () => {
      capturedCtx!.nav('audit')
    })
    expect(requireCtx().view, 'sanity: the nav away must leave settings').toBe('audit')

    await popTo('/settings/roles')
    let ctx = requireCtx()
    expect(ctx.view, 'Back onto /settings/<tab> must restore the settings view').toBe('settings')
    expect(ctx.settingsTab, 'Back must restore the tab alongside the view').toBe('roles')
    expect(window.location.pathname, 'Back must leave the tab segment in the URL').toBe('/settings/roles')

    await popTo('/audit')
    expect(requireCtx().view, 'floor: the second hop must actually leave settings again').toBe('audit')

    await popTo('/settings/roles')
    ctx = requireCtx()
    expect(ctx.view, 'Forward must re-apply the settings view').toBe('settings')
    expect(ctx.settingsTab, 'Forward must re-apply the tab').toBe('roles')
    expect(window.location.pathname, 'Forward must re-apply the tab segment').toBe('/settings/roles')
  })
})
