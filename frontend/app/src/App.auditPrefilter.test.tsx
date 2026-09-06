// @vitest-environment jsdom
// AUDIT-09-05 Mode A. The "Open in Audit ->" hand-off, observed on PlatformCtx.
//
// Harness is App.standIn.test.tsx's: the real <App/>, a session seeded into a stubbed
// localStorage, and ctx captured through a mocked Sidebar. VITE_GATEWAY_URL stays unstubbed,
// so gatewayBase() is null and nothing on any screen fetches.
//
// ROUTE-04-04: the atom has SCREEN lifetime -- nothing clears it behind /audit any more, and
// `navigate` off Audit is its only eraser. AC-1 still reads the RENDER LOG rather than a
// post-act() ctx read, because its claim is about the FIRST Audit render, not the settled one.

import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { StrictMode } from 'react'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { AuditPrefilter, PlatformCtx } from './types'

// The two PlatformCtx members this subtask adds. Declared here rather than in types.ts: making
// them REQUIRED there reds App.tsx's one real construction site, and this commit does not touch
// App.tsx. platformCtx_declaresTheAtomAndTheVerb is the fence that they land there; delete this
// alias once they have.
type PrefilterCtx = PlatformCtx & {
  auditPrefilter: AuditPrefilter | null
  openAuditForInvoice: (invoiceId: string, invoiceNumber: string | null) => void
}

type RenderEntry = { view: string; prefilter: AuditPrefilter | null | undefined }

// Sidebar is not memoized and takes a freshly built ctx object every render (App.tsx:1133),
// so it re-renders on every Workspace render -- which is what makes this a complete log.
let capturedCtx: PrefilterCtx | undefined
const renders: RenderEntry[] = []
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    const ctx = p.ctx as PrefilterCtx
    capturedCtx = ctx
    renders.push({ view: ctx.view, prefilter: ctx.auditPrefilter })
    return null
  },
}))

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }

const INVOICE_ID = 'aaaaaaaa-0000-4000-8000-000000000001'
const INVOICE_NUMBER = 'INV-1'

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
  window.history.replaceState(null, '', '/')
  vi.stubGlobal('localStorage', createMemoryStorage())
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

async function renderApp(opts: { strict?: boolean } = {}) {
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  vi.resetModules()
  const { default: App } = await import('./App')
  return render(opts.strict ? <StrictMode><App /></StrictMode> : <App />)
}

function requireCtx(): PrefilterCtx {
  expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()
  expect(
    typeof capturedCtx!.openAuditForInvoice,
    'PlatformCtx must expose openAuditForInvoice(invoiceId, invoiceNumber)',
  ).toBe('function')
  expect('auditPrefilter' in capturedCtx!, 'PlatformCtx must expose the auditPrefilter atom').toBe(true)
  return capturedCtx!
}

// null -> non-null and back, counted over the whole log. StrictMode doubles the render count,
// so "how many renders carried the atom" is not a contract; the transitions are.
function transitions(log: RenderEntry[]): { armed: number; cleared: number } {
  const on = log.map((r) => r.prefilter != null)
  let armed = 0
  let cleared = 0
  for (let i = 1; i < on.length; i++) {
    if (!on[i - 1] && on[i]) armed++
    if (on[i - 1] && !on[i]) cleared++
  }
  return { armed, cleared }
}

describe('AC-1: openAuditForInvoice writes the atom and navigates in one commit', () => {
  it('platformCtx_openAuditForInvoiceWritesAtomAndNav', async () => {
    await renderApp()
    const ctx = requireCtx()

    await act(async () => {
      ctx.openAuditForInvoice(INVOICE_ID, INVOICE_NUMBER)
    })

    // Vacuity floors: two of them, because either alone can be satisfied by half the feature.
    const auditRenders = renders.filter((r) => r.view === 'audit')
    expect(auditRenders.length, 'the handler never navigated to Audit').toBeGreaterThan(0)
    expect(renders.filter((r) => r.prefilter != null).length, 'the atom was never written').toBeGreaterThan(0)

    // AuditView seeds in the useState initializer of the render that mounts it, so the FIRST
    // committed render showing Audit is the only one that can still carry the atom.
    expect(auditRenders[0]!.prefilter, 'the first Audit render must already carry the atom').toEqual({
      invoiceId: INVOICE_ID,
      invoiceNumber: INVOICE_NUMBER,
    })

    // The other half of "neither can be stale": the atom is never live on another screen.
    const stale = renders.filter((r) => r.prefilter != null && r.view !== 'audit')
    expect(stale, `the atom was live on ${JSON.stringify(stale.map((r) => r.view))}`).toHaveLength(0)
  })

  it('platformCtx_openAuditForInvoiceAcceptsANullNumber', async () => {
    await renderApp()
    const ctx = requireCtx()

    await act(async () => {
      ctx.openAuditForInvoice(INVOICE_ID, null)
    })

    const auditRenders = renders.filter((r) => r.view === 'audit')
    expect(auditRenders.length, 'the handler never navigated to Audit').toBeGreaterThan(0)
    // A payload need not carry a number; the id is what the reader filters on.
    expect(auditRenders[0]!.prefilter).toEqual({ invoiceId: INVOICE_ID, invoiceNumber: null })
  })
})

describe('AC-4: the atom lives exactly as long as the /audit URL', () => {
  // Repointed from platformCtx_auditPrefilterIsClearedOnceAuditMounts. Only the MECHANISM
  // claim (a waitFor that the atom becomes null on arrival) is gone; the behavioural claim it
  // guarded -- a manual nav to Audit lands unfiltered -- is asserted verbatim below.
  it('platformCtx_aManualNavToAuditCarriesNoAtom', async () => {
    await renderApp()
    const ctx = requireCtx()

    await act(async () => {
      ctx.openAuditForInvoice(INVOICE_ID, INVOICE_NUMBER)
    })

    const mark = renders.length
    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.nav('audit')
    })

    const after = renders.slice(mark)
    expect(after.filter((r) => r.view === 'audit').length, 'vacuity floor: the manual nav never reached Audit').toBeGreaterThan(0)
    expect(after.filter((r) => r.view === 'invoices').length, 'vacuity floor: the nav away never happened').toBeGreaterThan(0)
    const armed = after.filter((r) => r.prefilter != null)
    expect(armed, 'a manual nav to Audit must land unfiltered').toHaveLength(0)
  })

  it('platformCtx_theAtomIsArmedOnceUnderStrictMode', async () => {
    // Control needle: prove StrictMode really double-invokes render bodies here, or every
    // assertion below is about a single mount wearing a StrictMode wrapper.
    let probeRenders = 0
    function Probe() {
      probeRenders++
      return null
    }
    render(
      <StrictMode>
        <Probe />
      </StrictMode>,
    )
    expect(probeRenders, 'StrictMode is not double-invoking renders here -- this spec proves nothing').toBe(2)
    cleanup()
    renders.length = 0

    await renderApp({ strict: true })
    const ctx = requireCtx()

    await act(async () => {
      ctx.openAuditForInvoice(INVOICE_ID, INVOICE_NUMBER)
    })

    // Armed exactly once and never cleared while /audit is the URL. A doubled write, or a
    // clearing effect that came back, moves one of these two counts.
    const { armed, cleared } = transitions(renders)
    expect(armed, 'the atom must be armed exactly once').toBe(1)
    expect(cleared, 'nothing may clear the atom while /audit is on screen').toBe(0)
    expect(renders[renders.length - 1]!.prefilter, 'the atom must still be armed on the last render').toEqual({
      invoiceId: INVOICE_ID,
      invoiceNumber: INVOICE_NUMBER,
    })
    expect(renders.filter((r) => r.prefilter != null && r.view !== 'audit'), 'no stale atom off the Audit screen').toHaveLength(0)
  })

  it('platformCtx_theAtomSurvivesARerenderOnAudit', async () => {
    await renderApp()
    const ctx = requireCtx()

    await act(async () => {
      ctx.openAuditForInvoice(INVOICE_ID, INVOICE_NUMBER)
    })

    // toggleSwitcher is the one ctx verb that commits state without navigating and without
    // writing the URL, so these are re-renders of the SAME /audit screen, not three arrivals.
    const mark = renders.length
    for (let i = 0; i < 3; i++) {
      await act(async () => {
        capturedCtx!.toggleSwitcher()
      })
    }

    const after = renders.slice(mark)
    // Floor: without real re-renders the last-render read below is the arrival render again.
    expect(after.length, 'vacuity floor: the three re-renders never committed').toBeGreaterThanOrEqual(3)
    expect(after.every((r) => r.view === 'audit'), 'the re-renders must not have navigated').toBe(true)
    expect(renders[renders.length - 1]!.prefilter, 'a re-render on /audit must not consume the atom').toEqual({
      invoiceId: INVOICE_ID,
      invoiceNumber: INVOICE_NUMBER,
    })
  })

  it('guard_theConsumeOnceEffectIsGone', () => {
    const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'App.tsx'), 'utf8')
    expect(src, 'the scan read the wrong file').toContain('function navigate(view: View')
    // Control needle: a surviving setAuditPrefilter call site proves the scan can match at all.
    expect(src, 'control: the scan must be able to find a setAuditPrefilter write').toContain(
      'setAuditPrefilter((prev)',
    )
    expect(src, 'the consume-once effect must be deleted, not disabled').not.toContain(
      "if (view === 'audit' && auditPrefilter != null)",
    )
  })
})

describe('the PlatformCtx contract', () => {
  it('platformCtx_declaresTheAtomAndTheVerb', () => {
    // 39 `as unknown as PlatformCtx` fakes mean tsc fences almost nothing about this type.
    // This scan is what holds the two members required rather than optional.
    const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'types.ts'), 'utf8')
    const start = src.indexOf('export type PlatformCtx = {')
    expect(start, 'the scan read the wrong file -- no PlatformCtx declaration').toBeGreaterThan(-1)
    const end = src.indexOf('\n}\n', start)
    expect(end, 'PlatformCtx has no closing brace at column 0').toBeGreaterThan(start)
    const body = src.slice(start, end)
    // Control needle: prove the slice captured the interior, not an empty span.
    expect(body, 'the slice missed PlatformCtx\'s members').toContain('importedInvoiceId: string | null')

    expect(body, 'PlatformCtx must carry the auditPrefilter atom').toContain('auditPrefilter: AuditPrefilter | null')
    expect(body, 'PlatformCtx must carry the openAuditForInvoice verb').toContain('openAuditForInvoice: (')
    expect(body, 'auditPrefilter must be required, not optional').not.toContain('auditPrefilter?:')
    expect(body, 'openAuditForInvoice must be required, not optional').not.toContain('openAuditForInvoice?:')
  })
})

// ROUTE-04-04 QA. Every shipped row arms the atom ONCE. Now that it survives the arrival, a
// second hand-off lands on a screen that is already armed -- a state the consume-once effect
// made unreachable, and which nothing covered.
describe('ROUTE-04-04 QA: a second hand-off onto an already-armed Audit', () => {
  const SECOND_ID = 'bbbbbbbb-0000-4000-8000-000000000002'
  const SECOND_NUMBER = 'INV-2'

  it('platformCtx_aSecondHandOffCarriesItsOwnInvoiceAndNumber', async () => {
    await renderApp()
    const ctx = requireCtx()
    const pushSpy = vi.spyOn(window.history, 'pushState')

    await act(async () => {
      ctx.openAuditForInvoice(INVOICE_ID, INVOICE_NUMBER)
    })
    const mark = renders.length
    await act(async () => {
      capturedCtx!.openAuditForInvoice(SECOND_ID, SECOND_NUMBER)
    })

    const after = renders.slice(mark)
    expect(after.length, 'vacuity floor: the second hand-off never committed a render').toBeGreaterThan(0)
    expect(renders[renders.length - 1]!.prefilter, 'the atom must carry the SECOND invoice, number included').toEqual({
      invoiceId: SECOND_ID,
      invoiceNumber: SECOND_NUMBER,
    })
    // The hazard: navigate's updater keeps prev.invoiceNumber when the param names the same
    // invoice. A second hand-off names a DIFFERENT one, so a kept number is the first's, and
    // the Audit pill would read `Invoice INV-1` over invoice INV-2's events.
    const mixed = after.filter((r) => r.prefilter != null && r.prefilter.invoiceNumber === INVOICE_NUMBER)
    expect(mixed, `a render paired the new invoice with the old number: ${JSON.stringify(mixed)}`).toHaveLength(0)

    // Each hand-off is a destination of its own, so each is its own history entry.
    const pushed = pushSpy.mock.calls.map((c) => String(c[2]))
    expect(pushed.filter((u) => u.startsWith('/audit')), 'two hand-offs, two entries, in order').toEqual([
      `/audit?invoice=${INVOICE_ID}`,
      `/audit?invoice=${SECOND_ID}`,
    ])
    expect(window.location.pathname + window.location.search, 'and the URL ends on the second').toBe(
      `/audit?invoice=${SECOND_ID}`,
    )
  })
})

// AUDIT-09-05 QA. The two suites above stop at the seam: this file watched ctx and never the
// screen, and AuditView.test.tsx renders AuditView with a hand-built ctx object. Nothing
// asserted that App.tsx's own mount hands the LIVE atom to AuditView at all.
// Mutation-verified: `<AuditView ctx={{...ctx, auditPrefilter: null}} />` and a `key` that
// remounts AuditView on an atom change each left the whole app suite green.
describe('the App -> AuditView seam', () => {
  const GATEWAY = 'https://gw.test'

  // Everything the workspace fetches on the way to the Audit screen. Only the audit-log arm
  // is under test; the rest just has to be shaped well enough not to crash a render.
  function routeFetch(calls: string[]) {
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) => {
        calls.push(url)
        if (url.includes('/audit-log')) {
          return Promise.resolve({
            ok: true,
            status: 200,
            json: () =>
              Promise.resolve({
                events: [],
                page: { limit: 25, has_more: false, next_cursor: null },
                total: 0,
                log_is_empty: false,
                facets: { event: [], actor: [], company: [] },
              }),
          })
        }
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({ entities: [], policies: [], members: [], roles: [], invoices: [], total: 0 }),
        })
      }),
    )
  }

  async function renderAppWithGateway(calls: string[]) {
    routeFetch(calls)
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
    vi.resetModules()
    const { default: App } = await import('./App')
    return render(<App />)
  }

  // The screen's own main request: limit=25 is AUDIT_PAGE_INITIAL's, and the lifetime probe
  // carries limit=1. Keyed on the limit, never on the presence of a date window -- a
  // prefiltered mount sends no `from` on the main request either.
  const mainAuditCalls = (calls: string[]) =>
    calls.filter((u) => u.includes('/audit-log') && new URL(u).searchParams.get('limit') === '25')

  it('platformCtx_theAuditScreenActuallyLandsFiltered', async () => {
    const calls: string[] = []
    await renderAppWithGateway(calls)
    const ctx = requireCtx()

    await act(async () => {
      ctx.openAuditForInvoice(INVOICE_ID, INVOICE_NUMBER)
    })
    await waitFor(() => expect(mainAuditCalls(calls).length, 'the Audit screen never fetched').toBeGreaterThan(0))

    // EVERY main request, not just the first: a remount would issue a second one carrying the
    // default filter, and asserting only the first would miss it.
    for (const url of mainAuditCalls(calls)) {
      const p = new URL(url).searchParams
      expect(p.get('invoice_id'), `the Audit screen was not filtered by the hand-off: ${url}`).toBe(INVOICE_ID)
      expect(p.has('from'), `the Audit screen regained the 30-day window: ${url}`).toBe(false)
    }
    await waitFor(() =>
      expect(screen.queryByTestId('audit-pill-invoice'), 'the invoice pill must be on screen').not.toBeNull(),
    )
    expect(screen.getByTestId('audit-pill-invoice').textContent).toContain(INVOICE_NUMBER)
  })

  it('platformCtx_aManualNavToAuditLandsUnfiltered', async () => {
    // Control on the same locators and the same wire: without the hand-off the very same
    // screen windows by 30 days and sends no invoice.
    const calls: string[] = []
    await renderAppWithGateway(calls)
    const ctx = requireCtx()

    await act(async () => {
      ctx.nav('audit')
    })
    await waitFor(() => expect(mainAuditCalls(calls).length, 'the Audit screen never fetched').toBeGreaterThan(0))

    for (const url of mainAuditCalls(calls)) {
      const p = new URL(url).searchParams
      expect(p.has('invoice_id'), `a manual nav must send no invoice: ${url}`).toBe(false)
      expect(p.has('from'), `a manual nav must window by 30 days: ${url}`).toBe(true)
    }
    // Positive control on the same row: the pills row rendered and carries the 30-day pill,
    // so the absence below is the missing invoice filter, not a screen that never mounted.
    await waitFor(() =>
      expect(screen.queryByTestId('audit-pill-range'), 'control: the pills row rendered').not.toBeNull(),
    )
    expect(screen.queryByTestId('audit-pill-invoice'), 'and must show no invoice pill').toBeNull()
  })

  // AC-8. The atom has screen lifetime now, so every owned-param write rebuilds the URL from a
  // still-armed atom and ?invoice= survives. Restoring the deleted clear-on-mount effect reds
  // this row: that effect nulled the atom at mount, and the NEXT owned-param write then
  // rebuilt the URL with auditInvoice: null while the screen kept rendering filtered. The
  // RENDERED half -- the wire half is auditFilter_anotherOwnedParamWriteMustNotDropTheInvoice.
  it('platformCtx_theUrlKeepsMatchingWhatTheAuditScreenRenders', async () => {
    const calls: string[] = []
    window.history.replaceState(null, '', `/audit?invoice=${INVOICE_ID}`)
    await renderAppWithGateway(calls)
    requireCtx()

    await waitFor(() =>
      expect(screen.queryByTestId('audit-pill-invoice'), 'floor: the boot must land the screen filtered').not.toBeNull(),
    )
    expect(
      window.location.pathname + window.location.search,
      'floor: the mount alignment must keep the invoice on the URL',
    ).toBe(`/audit?invoice=${INVOICE_ID}`)

    await act(async () => {
      capturedCtx!.setSettingsTab('roles')
    })

    expect(
      window.location.pathname + window.location.search,
      'a settings-tab write must not drop the invoice the screen is still showing',
    ).toBe(`/audit?invoice=${INVOICE_ID}`)
    expect(screen.queryByTestId('audit-pill-invoice'), 'and the screen is still showing it').not.toBeNull()
    // Every main request, so a remount that silently widened the log cannot hide here.
    const main = mainAuditCalls(calls)
    expect(main.length, 'the Audit screen never fetched').toBeGreaterThan(0)
    for (const url of main) {
      expect(new URL(url).searchParams.get('invoice_id'), `a main request lost the filter: ${url}`).toBe(INVOICE_ID)
    }
  })

  // ROUTE-04-06 AC-5, task-937. route.ts's INVOICE_ID regex already drops a malformed
  // `?invoice=` before it ever reaches the atom (lib/route.test.ts) -- this is the wire-level
  // regression guard proving that drop actually keeps the id off the Audit screen's own request.
  it('platformCtx_aMalformedInvoiceQueryParamNeverReachesTheWire', async () => {
    const calls: string[] = []
    window.history.replaceState(null, '', '/audit?invoice=not-a-uuid')
    await renderAppWithGateway(calls)
    requireCtx()

    await waitFor(() => expect(mainAuditCalls(calls).length, 'floor: the Audit screen never fetched').toBeGreaterThan(0))
    for (const url of mainAuditCalls(calls)) {
      const p = new URL(url).searchParams
      expect(p.has('invoice_id'), `a malformed invoice param must never reach the wire: ${url}`).toBe(false)
      expect(p.has('from'), `an unfiltered mount must still window by 30 days: ${url}`).toBe(true)
    }
    expect(screen.queryByTestId('audit-pill-invoice'), 'and the screen renders unfiltered').toBeNull()
  })
})
