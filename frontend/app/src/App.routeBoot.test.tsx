// @vitest-environment jsdom
// vitest.config.ts stays `environment: 'node'` for every other suite.
//
// Boot seeding: the view a path implies before navigate()/popstate exist. Harness mirrors
// App.extractionRoute.test.tsx -- the real <App/>, a session in a stubbed localStorage, ctx
// captured through a mocked Sidebar.

import { readFileSync } from 'node:fs'
import path from 'node:path'

import { StrictMode } from 'react'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { DEEP_LINK_KEY, DEEP_LINK_SCHEMA_VERSION } from './lib/deepLink'
import { ROUTE_PATHS } from './lib/route'
import type { Member } from './lib/members'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx, View } from './types'

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const REVIEW_ID = 'a1b2c3d4-e5f6-47a8-89ab-cdef01234567'
const INVOICE_ID = 'b7c1d2e3-4f5a-4b6c-8d9e-0f1a2b3c4d5e'
const DETAIL_ID = 'd1e2a3b4-c5d6-47e8-89fa-bc0123456789'
const JOB_ID = 'f1e2a3b4-c5d6-47e8-89fa-bc0123456790'

const MEMBER: Member = {
  id: 'm-boot-001',
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

let capturedCtx: PlatformCtx | undefined
// Additive render log beside capturedCtx, which keeps only the LAST render. Sidebar is not
// memoized and takes a freshly built ctx object every render, so this log is complete.
const ctxRenders: PlatformCtx[] = []
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
    ctxRenders.push(p.ctx)
    return null
  },
}))

beforeEach(() => {
  capturedCtx = undefined
  // ctxRenders is reset HERE ONLY. The two mid-test `capturedCtx = undefined` resets
  // (boot_everyNonDefaultPathSeedsItsOwnView, signOut_thePathnameDoesNotSurvive...) sit in
  // specs that never read this log; the specs that do read it slice it instead of clearing.
  ctxRenders.length = 0
  sessionStorage.clear()
  // jsdom's environment is per FILE, not per test -- without this, one test's boot URL
  // seeds the next test's boot.
  window.history.replaceState(null, '', '/')
  vi.stubGlobal('localStorage', createMemoryStorage())
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

// persona defaults to SEAT_SESSION's (firm) -- ROUTE-04-06 AC-2 is the first spec in this
// file to pass APP_PERSONAS.inhouse; every existing call site is unaffected.
async function bootAt(path: string, opts: { demoMode?: boolean; strict?: boolean; persona?: Session['persona'] } = {}) {
  window.history.replaceState(null, '', path)
  const session: Session = { ...SEAT_SESSION, persona: opts.persona ?? SEAT_SESSION.persona }
  localStorage.setItem(SESSION_KEY, serializeSession(session))
  if (opts.demoMode) vi.stubEnv('VITE_DEMO_MODE', 'true')
  vi.resetModules()
  const { default: App } = await import('./App')
  return render(opts.strict ? (
    <StrictMode>
      <App />
    </StrictMode>
  ) : (
    <App />
  ))
}

function requireCtx(): PlatformCtx {
  expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()
  return capturedCtx!
}

describe('AC-1: a path seeds the view it names', () => {
  it('boot_aPathSeedsTheViewItNames', async () => {
    await bootAt('/audit')
    const ctx = requireCtx()
    expect(ctx.view, `booting at /audit should seed view 'audit', got '${ctx.view}'`).toBe('audit')
  })

  it('boot_everyNonDefaultPathSeedsItsOwnView', async () => {
    const nonDefault = (Object.entries(ROUTE_PATHS) as [View, string][]).filter(([view]) => view !== 'dashboard')
    // Floor: a loop over an empty array passes every assertion inside it.
    expect(nonDefault, 'the route table must have exactly 12 non-dashboard entries').toHaveLength(12)

    for (const [view, path] of nonDefault) {
      cleanup()
      capturedCtx = undefined
      await bootAt(path)
      const ctx = requireCtx()
      expect(ctx.view, `booting at ${path} should seed view '${view}', got '${ctx.view}'`).toBe(view)
    }
  })
})

describe('AC-2: an unknown path falls back to dashboard', () => {
  it('boot_anUnknownPathFallsBackToDashboardAndTheUrlIsCorrected', async () => {
    await bootAt('/nonsense')
    const ctx = requireCtx()
    expect(ctx.view, `an unknown path should fall back to dashboard, got '${ctx.view}'`).toBe('dashboard')
    expect(window.location.pathname, 'the corrected URL must be the bare root').toBe('/')
  })
})

describe('AC-3: initialView still beats the path', () => {
  it('boot_initialViewStillBeatsThePath', async () => {
    await bootAt('/audit', { demoMode: true })
    const ctx = requireCtx()
    expect(typeof ctx.becomePersona, 'DEMO_MODE must expose becomePersona on ctx').toBe('function')

    await act(async () => {
      await ctx.becomePersona!(MEMBER, 'approvals')
    })
    expect(
      capturedCtx!.view,
      `a DEMO-06 initialView carry must beat the path, got '${capturedCtx!.view}'`,
    ).toBe('approvals')
  })
})

describe('AC-4: the review hash still beats the path', () => {
  it('boot_theReviewHashStillBeatsThePath', async () => {
    await bootAt(`/audit#review/${REVIEW_ID}`)
    const ctx = requireCtx()
    expect(ctx.view, `a live review hash must still win over the path, got '${ctx.view}'`).toBe('create')
    expect(ctx.createStep, 'the review step must be active').toBe('review')
  })
})

describe('AC-5: the alignment preserves the hash', () => {
  it('boot_theAlignmentPreservesTheHash', async () => {
    const hash = `#review/${REVIEW_ID}`
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await bootAt(`/${hash}`)
    requireCtx()
    expect(window.location.pathname, 'the alignment must rewrite the path to /create').toBe('/create')
    expect(window.location.hash, 'the alignment must preserve the review hash verbatim').toBe(hash)

    // The final `window.location.hash` above is a WEAK oracle for the alignment's own
    // line: App.tsx's pre-existing, untouched review-hash mirror (the effect declared
    // right after the alignment, App.tsx:524-530) recomputes and re-writes the identical
    // hash on this same commit whenever createStep is 'review', independent of what the
    // alignment wrote. A `replaceState` call that drops the hash from the alignment would
    // still leave `window.location.hash` correct, repaired by that unrelated effect. The
    // alignment's OWN write -- its first recorded call after the test's own boot-setup
    // call -- must therefore be checked directly.
    const own = replaceSpy.mock.calls.find((call) => typeof call[2] === 'string' && call[2].startsWith('/create'))
    expect(own, 'no replaceState call to /create was recorded').toBeDefined()
    expect(own![2], "the alignment's own replaceState call must itself carry the hash").toBe(`/create${hash}`)
  })
})

describe('AC-6: the alignment writes no history entry, and is idempotent', () => {
  it('boot_mountAddsNoHistoryEntry', async () => {
    const pushSpy = vi.spyOn(window.history, 'pushState')
    const lengthBefore = window.history.length
    await bootAt('/nonsense')
    requireCtx()
    expect(pushSpy, 'mount must never call pushState').not.toHaveBeenCalled()
    expect(window.history.length, 'mount must add no history entry').toBe(lengthBefore)
  })

  it('boot_theAlignmentIsIdempotentUnderStrictModeDoubleInvocation', async () => {
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await bootAt('/audit', { strict: true })
    requireCtx()
    // Floor: a writer swapped to pushState leaves this spy with zero calls, and
    // .filter(...).toHaveLength(0) below would pass on that broken build for the wrong
    // reason. boot_mountAddsNoHistoryEntry pins that swap directly; this floor keeps this
    // test meaningful on its own.
    expect(replaceSpy.mock.calls.length, 'replaceState must have been called at least once during boot').toBeGreaterThan(0)
    const differing = replaceSpy.mock.calls.filter((call) => call[2] !== '/audit')
    expect(
      differing,
      `an already-aligned boot must never replaceState to a differing URL: ${JSON.stringify(differing)}`,
    ).toHaveLength(0)
  })

  // No AC test pins the alignment's own dependency array. Since ROUTE-01-03, `ctx.nav`
  // calls navigate(), which pushState's straight to the new path -- so the pre-existing
  // review-hash mirror (App.tsx:533-539, untouched, out of scope), keyed on `view`, now
  // legitimately replaceState's that same path back on every nav. That makes "did some
  // replaceState call name /invoices" unusable as a discriminator: the mirror produces
  // exactly that call on a correct build. Count calls instead -- the mirror contributes
  // exactly one; a widened mount alignment would contribute a second.
  it('boot_theAlignmentDoesNotReRunWhenViewChangesAfterMount', async () => {
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await bootAt('/audit')
    const ctx = requireCtx()
    const callsBeforeNav = replaceSpy.mock.calls.length

    await act(async () => {
      ctx.nav('invoices')
    })

    expect(
      replaceSpy.mock.calls.length - callsBeforeNav,
      'exactly one replaceState is expected after a view change (the review-hash mirror, App.tsx:533-539); a second would mean the mount-only alignment effect re-ran',
    ).toBe(1)
  })
})

describe('AC-7: signOut resets the pathname to /, and preserves the hash', () => {
  it('signOut_thePathnameDoesNotSurviveIntoTheNextSignIn', async () => {
    // ctx.nav() does not touch the URL until navigate()/pushState land (ROUTE-01-03) --
    // booting straight at /invoices is what dirties the pathname today, via the mount
    // alignment this subtask adds (AC-1).
    await bootAt('/invoices')
    let ctx = requireCtx()
    expect(ctx.view, 'sanity: booting at /invoices must seed that view').toBe('invoices')

    await act(async () => {
      ctx.signOut()
    })
    expect(window.location.pathname, 'signOut must reset the pathname to /').toBe('/')

    expect(screen.getByText('Choose an account'), 'the in-app picker must render after sign-out').toBeTruthy()
    const pickButton = screen.getByText(SEAT_SESSION.persona.name).closest('button')
    expect(pickButton, 'the firm persona button was not found in the picker').toBeTruthy()
    capturedCtx = undefined
    await act(async () => {
      fireEvent.click(pickButton as HTMLButtonElement)
    })

    ctx = requireCtx()
    expect(ctx.view, 'a fresh sign-in must never inherit the previous session\'s view').toBe('dashboard')
  })

  it('signOut_preservesTheHashAndEveryOtherReset', async () => {
    const hash = `#review/${REVIEW_ID}`
    await bootAt(`/invoices${hash}`)
    const ctx = requireCtx()
    // sanity: the review hash beats the path (AC-4), so this boots onto /create.
    expect(window.location.pathname, 'sanity: the review hash must beat the path').toBe('/create')

    await act(async () => {
      ctx.signOut()
    })

    expect(window.location.hash, 'signOut must preserve the hash verbatim').toBe(hash)
    expect(window.location.pathname, 'signOut must still reset the pathname').toBe('/')
    expect(localStorage.getItem(SESSION_KEY), 'the persisted session must be cleared').toBeNull()
    expect(screen.queryByTestId('persona-toast'), 'no toast must be mounted after sign-out').toBeNull()
  })
})

describe('AC-8: the pre-existing sign-out regression oracle is untouched', () => {
  it('standIn_theExistingSignOutOracleIsUnmodified', () => {
    const source = readFileSync(path.join(process.cwd(), 'src/App.standIn.test.tsx'), 'utf8')
    expect(
      source.includes(
        "a return that commits after sign-out must not carry its view into the next sign-in').toBe('dashboard')",
      ),
      'the oracle\'s message and its .toBe(\'dashboard\') assertion must both survive verbatim',
    ).toBe(true)
  })
})

describe('QA adversarial coverage', () => {
  it('boot_neverEchoesAnExistingQueryStringIntoTheAlignedUrl', async () => {
    await bootAt('/audit?foo=bar')
    requireCtx()
    expect(window.location.pathname, 'the view still seeds from the path').toBe('/audit')
    expect(window.location.search, 'the alignment must never echo an existing query string').toBe('')
  })

  it('boot_toleratesExactlyOneTrailingSlash', async () => {
    await bootAt('/audit/')
    const ctx = requireCtx()
    expect(ctx.view, `a single trailing slash should still seed 'audit', got '${ctx.view}'`).toBe('audit')
  })

  it('boot_detailWithNoSelectionColdBootsToTheEmptyState (documented limitation, ROUTE-02 closes it)', async () => {
    await bootAt('/invoice')
    requireCtx()
    expect(screen.getByText('No invoice selected'), 'a cold /invoice boot must render the honest EmptyState').toBeTruthy()
  })

  it('boot_extractionWithNoJobIdRendersNothing (documented limitation, ROUTE-02 closes it)', async () => {
    await bootAt('/extraction')
    requireCtx()
    expect(
      screen.queryByTestId('extraction-review'),
      'App.tsx\'s `extractionJobId != null` gate must render nothing for a cold /extraction boot',
    ).toBeNull()
  })

  it('boot_aWrongCasePathFallsBackToDashboard', async () => {
    await bootAt('/Audit')
    const ctx = requireCtx()
    expect(ctx.view, `a wrong-case path must not match, got '${ctx.view}'`).toBe('dashboard')
    expect(window.location.pathname, 'the corrected URL must be the bare root').toBe('/')
  })

  it('boot_theReviewHashWinsOverAMismatchedPathAndTheAlignmentWritesCreatePlusTheHash', async () => {
    const hash = `#review/${REVIEW_ID}`
    await bootAt(`/settings${hash}`)
    const ctx = requireCtx()
    expect(ctx.view, 'the review hash must win over a completely unrelated path').toBe('create')
    expect(window.location.pathname, 'the alignment must correct the path to /create').toBe('/create')
    expect(window.location.hash, 'the alignment must carry the hash along').toBe(hash)
  })
})

// ROUTE-04-02: the boot seeds the three owned params and the mount alignment re-emits them.
describe('ROUTE-04-02 AC-1: the settings tab seeds from the path segment', () => {
  it('boot_settingsTabSeedsFromThePathSegment', async () => {
    await bootAt('/settings/roles')
    const ctx = requireCtx()
    expect(ctx.view, `booting at /settings/roles should seed view 'settings', got '${ctx.view}'`).toBe('settings')
    expect(ctx.settingsTab, `the path segment must seed settingsTab, got '${ctx.settingsTab}'`).toBe('roles')
    expect(window.location.pathname, 'the alignment must keep the tab segment in the URL').toBe('/settings/roles')
  })
})

describe('ROUTE-04-02 AC-2: the invoice query seeds, and survives the alignment', () => {
  it('boot_theInvoiceQuerySeedsFromTheUrl', async () => {
    await bootAt('/invoices?q=acme')
    const ctx = requireCtx()
    expect(ctx.view, 'sanity: /invoices must still seed the invoices view').toBe('invoices')
    expect(ctx.invoiceQuery, `the owned q param must seed invoiceQuery, got '${ctx.invoiceQuery}'`).toBe('acme')
  })

  it('boot_theAlignedUrlKeepsTheOwnedQuery', async () => {
    await bootAt('/invoices?q=acme')
    requireCtx()
    expect(
      window.location.pathname + window.location.search,
      'the alignment must re-emit the owned q param rather than drop it',
    ).toBe('/invoices?q=acme')
  })
})

describe('ROUTE-04-02 AC-3: the audit invoice filter seeds from the url', () => {
  it('boot_theAuditInvoiceFilterSeedsFromTheUrl', async () => {
    await bootAt(`/audit?invoice=${INVOICE_ID}`)
    // The SETTLED ctx, not ctxRenders[0] (ROUTE-04-04 tightening): the atom now has screen
    // lifetime, so it must still be armed after the effect phase, not just during the mount
    // render. The log floor stays as a not-empty sanity on the harness.
    expect(ctxRenders.length, 'the render log is empty -- Sidebar never rendered').toBeGreaterThan(0)
    expect(ctxRenders[0].view, 'floor: the first logged render must be the mount render').toBe('audit')
    expect(
      requireCtx().auditPrefilter,
      'the owned invoice param must seed the whole prefilter atom, invoiceNumber included',
    ).toEqual({ invoiceId: INVOICE_ID, invoiceNumber: null })
    expect(
      window.location.pathname + window.location.search,
      'the alignment must re-emit the owned invoice param',
    ).toBe(`/audit?invoice=${INVOICE_ID}`)
  })
})

describe('ROUTE-04-02 AC-4: an unowned param is dropped, an owned one is not', () => {
  // Positive control for the shipped boot_neverEchoesAnExistingQueryStringIntoTheAlignedUrl:
  // that spec alone stays green on an alignment that emits no query string at all.
  it('boot_dropsTheUnownedParamWhileKeepingTheOwnedOne', async () => {
    await bootAt(`/audit?foo=bar&invoice=${INVOICE_ID}`)
    requireCtx()
    expect(window.location.pathname, 'the view still seeds from the path').toBe('/audit')
    expect(window.location.search, 'the owned param survives the alignment and the unowned one does not').toBe(
      `?invoice=${INVOICE_ID}`,
    )
  })
})

describe('ROUTE-04-02 AC-5: a restored destination ignores a live query string', () => {
  it('boot_aRestoredDestinationIgnoresALiveQueryString', async () => {
    sessionStorage.setItem(
      DEEP_LINK_KEY,
      JSON.stringify({ v: DEEP_LINK_SCHEMA_VERSION, path: '/audit', at: Date.now() }),
    )
    await bootAt(`/?invoice=${INVOICE_ID}`)
    const ctx = requireCtx()
    expect(ctx.view, 'the restored destination must still decide the view').toBe('audit')
    expect(ctxRenders.length, 'the render log is empty -- Sidebar never rendered').toBeGreaterThan(0)
    // The SETTLED ctx on both halves (ROUTE-04-04 tightening). The atom has screen lifetime
    // now, so "never attached" means never at all, not merely absent by the effect phase.
    expect(
      ctx.auditPrefilter,
      'a query string sitting on the bare root must never attach to a restored path',
    ).toBeNull()
    expect(window.location.search, 'the restored path carries no query').toBe('')

    // Control needle: the same param on a LIVE /audit URL must still seed. Without it this
    // spec stays green on an implementation that never reads the query at all. The log is
    // sliced rather than reset, so the beforeEach-only reset strategy above still holds.
    const beforeControl = ctxRenders.length
    cleanup()
    sessionStorage.clear()
    await bootAt(`/audit?invoice=${INVOICE_ID}`)
    expect(ctxRenders.slice(beforeControl).length, 'the control boot logged no render').toBeGreaterThan(0)
    expect(requireCtx().auditPrefilter, 'control: a live /audit query must still seed the atom').toEqual({
      invoiceId: INVOICE_ID,
      invoiceNumber: null,
    })
  })
})

describe('ROUTE-04-02 AC-6: the review-hash boot path is unchanged', () => {
  it('boot_theReviewHashPathIsUnchanged', async () => {
    const hash = `#review/${REVIEW_ID}`
    // `q` is unowned on the bare root, so an alignment that appended the live search would
    // carry it onto /create. A bare `/#review/...` boot cannot tell those two apart.
    await bootAt(`/?q=acme${hash}`)
    const ctx = requireCtx()
    expect(ctx.view, 'the review hash must still win the boot').toBe('create')
    expect(ctx.createStep, 'the review step must still be active').toBe('review')
    expect(window.location.pathname, 'the alignment must still correct the path to /create').toBe('/create')
    expect(window.location.hash, 'the alignment must still preserve the hash verbatim').toBe(hash)
    expect(window.location.search, 'create owns no param, so the aligned URL carries no query').toBe('')
  })
})

describe('ROUTE-04-02 AC-7: under StrictMode every boot write names one url', () => {
  it('boot_underStrictModeTheUrlConvergesOnOneWrite', async () => {
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await bootAt('/settings/roles', { strict: true })
    requireCtx()
    // A call COUNT is the wrong invariant: StrictMode double-invokes both the alignment and
    // the review-hash mirror, so a correct boot records four writes of its own here.
    // bootAt writes the URL as its first statement, so call 0 is the harness's own setup.
    const writes = replaceSpy.mock.calls.slice(1).map((call) => call[2])
    expect(writes.length, 'the boot recorded no write of its own').toBeGreaterThanOrEqual(2)
    expect([...new Set(writes)], 'every boot write must name the same aligned URL').toEqual(['/settings/roles'])
  })
})

describe('ROUTE-04-02 QA adversarial coverage', () => {
  it('boot_anEmptyOwnedParamIsOmittedRatherThanReEmitted', async () => {
    await bootAt('/invoices?q=')
    const ctx = requireCtx()
    expect(ctx.invoiceQuery, 'an empty q seeds the empty string, not undefined').toBe('')
    expect(
      window.location.pathname + window.location.search,
      'omit-the-default: an empty owned param must produce no query string, not a bare `?q=`',
    ).toBe('/invoices')
  })

  it('boot_theDefaultSettingsTabIsOmittedFromTheAlignedUrl', async () => {
    // Six e2e assertions are `$`-anchored on `/settings` exactly and start depending on
    // omit-the-default once ROUTE-04-03 repoints navigate at routeUrl.
    await bootAt('/settings/members')
    const ctx = requireCtx()
    expect(ctx.view, 'sanity: /settings/members must seed the settings view').toBe('settings')
    expect(ctx.settingsTab, 'the explicit default segment still seeds members').toBe('members')
    expect(window.location.pathname, 'the default tab must be omitted from the aligned path').toBe('/settings')
    expect(window.location.search, 'the tab is a path segment, never a query param').toBe('')
  })

  it('boot_aMalformedInvoiceIdNeverReachesTheAtomOrTheAlignedUrl', async () => {
    await bootAt('/audit?invoice=not-a-uuid')
    requireCtx()
    expect(ctxRenders.length, 'the render log is empty -- Sidebar never rendered').toBeGreaterThan(0)
    expect(ctxRenders[0].view, 'floor: the first logged render must be the mount render').toBe('audit')
    expect(
      ctxRenders[0].auditPrefilter,
      'the codec drops a malformed id, so the atom must never see it',
    ).toBeNull()
    expect(
      window.location.pathname + window.location.search,
      'a dropped id must not be re-emitted -- the aligned URL is bare /audit',
    ).toBe('/audit')
  })

  it('boot_theReviewHashBeatsAnOwnedParamAndTheAlignedUrlCarriesNeither', async () => {
    const hash = `#review/${REVIEW_ID}`
    await bootAt(`/invoices?q=acme${hash}`)
    const ctx = requireCtx()
    expect(ctx.view, 'the review hash must beat the path that owns the param').toBe('create')
    expect(ctx.createStep, 'the review step must be active').toBe('review')
    // The term still seeds from the boot path, so leaving review returns to a filtered list;
    // `create` owns no param, so it cannot appear in the URL while review is on screen.
    expect(ctx.invoiceQuery, 'the seed reads the boot path, which the hash only overrides for `view`').toBe('acme')
    expect(window.location.pathname, 'the alignment must correct the path to /create').toBe('/create')
    expect(window.location.hash, 'the hash must survive the alignment verbatim').toBe(hash)
    expect(window.location.search, "create owns nothing, so the term must not follow it into the URL").toBe('')
  })

  it('boot_underStrictModeEveryWriteDropsTheUnownedParamAndKeepsTheOwnedOne', async () => {
    // AC-7's own fixture (/settings/roles) carries no query, so it cannot see a write that
    // re-emits `location.search` -- only the alignment runs before the review-hash mirror
    // keeps the two agreeing here.
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await bootAt(`/audit?foo=bar&invoice=${INVOICE_ID}`, { strict: true })
    requireCtx()
    const writes = replaceSpy.mock.calls.slice(1).map((call) => call[2])
    expect(writes.length, 'the boot recorded no write of its own').toBeGreaterThanOrEqual(2)
    expect([...new Set(writes)], 'every boot write must name the same cleaned URL').toEqual([
      `/audit?invoice=${INVOICE_ID}`,
    ])
  })
})

// ROUTE-02-02: seedFromPath's id reaches ctx and survives the mount alignment.
describe('ROUTE-02-02: cold-boot seeding reaches both ids', () => {
  it('boot_detailPathSeedsTheIdOnTheFirstCommittedRender (B-1)', async () => {
    await bootAt(`/invoices/${DETAIL_ID}`)
    const ctx = requireCtx()
    expect(ctx.view, `booting at /invoices/${DETAIL_ID} should seed 'detail', got '${ctx.view}'`).toBe('detail')
    expect(ctx.importedInvoiceId, 'the id must reach ctx.importedInvoiceId at boot').toBe(DETAIL_ID)
  })

  it('boot_extractionPathSeedsTheJobIdAndMountsTheReviewScreen (B-2)', async () => {
    await bootAt(`/extraction/${JOB_ID}`)
    const ctx = requireCtx()
    expect(ctx.view, `booting at /extraction/${JOB_ID} should seed 'extraction', got '${ctx.view}'`).toBe('extraction')
    expect(ctx.extractionJobId, 'the id must reach ctx.extractionJobId at boot').toBe(JOB_ID)
    expect(screen.queryByTestId('extraction-review'), 'ExtractionReview must mount once the job id is seeded').toBeTruthy()
  })

  it('boot_theAlignmentDoesNotDropTheIdFromTheUrlAfterMount (B-3)', async () => {
    await bootAt(`/invoices/${DETAIL_ID}`)
    requireCtx()
    expect(
      window.location.pathname,
      'the alignment must not rewrite the id-carrying path to the bare /invoice',
    ).toBe(`/invoices/${DETAIL_ID}`)
  })

  it('boot_theAlignmentWritesTheIdCarryingUrlOnceAndNeverPushState (B-4)', async () => {
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    const pushSpy = vi.spyOn(window.history, 'pushState')
    await bootAt(`/invoices/${DETAIL_ID}`)
    requireCtx()
    expect(pushSpy, 'mount must never call pushState').not.toHaveBeenCalled()
    // calls[0] is bootAt's own boot-setup replaceState (the AC-5 harness trap), not the
    // app's. The app's writes start at calls[1]: the mount alignment, then the untouched
    // review-hash mirror (App.tsx ~536), which also replaceState's on this same commit.
    const appCalls = replaceSpy.mock.calls.slice(1)
    expect(appCalls[0]?.[2], "the alignment's own replaceState call must carry the id").toBe(
      `/invoices/${DETAIL_ID}`,
    )
    const droppingId = appCalls.filter((call) => call[2] === '/invoice')
    expect(droppingId, 'no app replaceState call may ever drop the id back to the bare /invoice path').toHaveLength(
      0,
    )
  })

  it('boot_theReviewHashBeatsThePathAndDropsThePathsId (B-5)', async () => {
    const hash = `#review/${REVIEW_ID}`
    await bootAt(`/invoices/${DETAIL_ID}${hash}`)
    const ctx = requireCtx()
    expect(ctx.view, 'a live review hash must still win over a path carrying an id').toBe('create')
    expect(ctx.importedInvoiceId, "the path's id must not survive when the review hash wins").toBeNull()
    expect(window.location.pathname, 'the alignment must land on /create').toBe('/create')
    expect(window.location.hash, 'the alignment must preserve the review hash').toBe(hash)
  })

  it('boot_initialViewBeatsThePathAndDropsItsId (B-6)', async () => {
    await bootAt(`/invoices/${DETAIL_ID}`, { demoMode: true })
    let ctx = requireCtx()
    expect(ctx.view, 'sanity: the first mount seeds detail from the path').toBe('detail')
    expect(typeof ctx.becomePersona, 'DEMO_MODE must expose becomePersona on ctx').toBe('function')

    await act(async () => {
      await ctx.becomePersona!(MEMBER, 'audit')
    })
    ctx = requireCtx()
    expect(ctx.view, `a DEMO-06 initialView carry must beat the path, got '${ctx.view}'`).toBe('audit')
    expect(ctx.importedInvoiceId, "the path's id must not survive when initialView wins").toBeNull()
    expect(window.location.pathname, 'the alignment must land on /audit').toBe('/audit')
  })

  it('boot_aMalformedIdSegmentFallsBackToDashboardWithNoUncaughtError (B-9)', async () => {
    const onError = vi.fn()
    window.addEventListener('error', onError)
    try {
      await bootAt('/invoices/%zz')
      const ctx = requireCtx()
      expect(ctx.view, `a malformed id segment must fall back to dashboard, got '${ctx.view}'`).toBe('dashboard')
    } finally {
      window.removeEventListener('error', onError)
    }
    expect(onError, 'booting a malformed id segment must throw no uncaught error').not.toHaveBeenCalled()
  })
})

// QA (ROUTE-02-02, task-912): adversarial coverage the B-1..B-9 specs above did not
// exercise. No existence check on the id is owed here -- seedFromPath is a pure path
// parse, decoupled from data. Whether an unknown/foreign id gets a fallback surface is
// downstream rendering's job (see decision note in task-912's QA findings).
describe('QA adversarial coverage (ROUTE-02-02)', () => {
  it('boot_aWellFormedButUnknownIdStillReachesCtxUnvalidated', async () => {
    const unknownId = '00000000-0000-0000-0000-000000000000'
    await bootAt(`/invoices/${unknownId}`)
    const ctx = requireCtx()
    expect(ctx.view, 'a well-formed id must still seed detail regardless of whether it names a real row').toBe(
      'detail',
    )
    expect(
      ctx.importedInvoiceId,
      'boot performs no existence check -- the id reaches ctx verbatim; a fallback for an unknown id is not this subtask\'s job',
    ).toBe(unknownId)
  })

  it('boot_aQueryStringAndAReviewHashTogetherStillDropTheIdAndNeverEchoSearch', async () => {
    const hash = `#review/${REVIEW_ID}`
    await bootAt(`/invoices/${DETAIL_ID}?foo=bar${hash}`)
    const ctx = requireCtx()
    expect(ctx.view, 'the review hash must still win with a query string also present').toBe('create')
    expect(ctx.importedInvoiceId, "the path's id must not survive when the hash wins").toBeNull()
    expect(window.location.search, 'the alignment must never echo the query string').toBe('')
    expect(window.location.hash, 'the alignment must still preserve the hash').toBe(hash)
  })

  it('boot_initialViewBeatsBothTheReviewHashAndThePathsIdOnRemount', async () => {
    const hash = `#review/${REVIEW_ID}`
    await bootAt(`/invoices/${DETAIL_ID}${hash}`, { demoMode: true })
    let ctx = requireCtx()
    expect(ctx.view, 'sanity: the review hash beats the path on the first mount').toBe('create')
    expect(typeof ctx.becomePersona, 'DEMO_MODE must expose becomePersona on ctx').toBe('function')

    await act(async () => {
      await ctx.becomePersona!(MEMBER, 'audit')
    })
    ctx = requireCtx()
    expect(ctx.view, 'initialView must beat both the hash and the path on the remount').toBe('audit')
    expect(ctx.importedInvoiceId, "the path's id must not survive when initialView wins").toBeNull()
    expect(window.location.pathname, 'the alignment must land on /audit').toBe('/audit')
  })

  it('boot_anIdLessInitialViewOtherThanAuditAlsoDropsAJobId', async () => {
    // B-6 only exercises 'audit'; this exercises a different id-less branch of bootHref's
    // ternary (routePath('settings', null)) so the fallback arm isn't proven by one view alone.
    await bootAt(`/extraction/${JOB_ID}`, { demoMode: true })
    let ctx = requireCtx()
    expect(ctx.view, 'sanity: the first mount seeds extraction from the path').toBe('extraction')

    await act(async () => {
      await ctx.becomePersona!(MEMBER, 'settings')
    })
    ctx = requireCtx()
    expect(ctx.view, 'initialView must beat the path').toBe('settings')
    expect(ctx.extractionJobId, "the path's job id must not survive when initialView wins").toBeNull()
    expect(window.location.pathname, 'the alignment must land on /settings').toBe('/settings')
  })

  it('boot_theIdSurvivesRepeatedStrictModeRemounts', async () => {
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await bootAt(`/invoices/${DETAIL_ID}`, { strict: true })
    const ctx = requireCtx()
    expect(ctx.importedInvoiceId, 'StrictMode double-invocation must not lose the id').toBe(DETAIL_ID)
    expect(
      window.location.pathname,
      'StrictMode double-invocation must not drop the id from the aligned URL',
    ).toBe(`/invoices/${DETAIL_ID}`)
    const differing = replaceSpy.mock.calls.filter(
      (call) => call[2] !== `/invoices/${DETAIL_ID}`,
    )
    expect(
      differing,
      `every replaceState call under StrictMode must agree on the id-carrying URL: ${JSON.stringify(differing)}`,
    ).toHaveLength(0)
  })
})

// ROUTE-04-06 AC-1/AC-2, task-937. `company` is a real SettingsTab (lib/route.ts), so
// parseLocation resolves it for ANY mode -- the mode-aware refusal has to happen here, at
// the settingsTab seed, since route.ts cannot see `mode`. AC-1 is the one RED spec this
// subtask owns; AC-2 is the control needle proving the refusal is mode-scoped, not blanket.
describe('ROUTE-04-06 AC-1: a firm-mode /settings/company falls back identically to an unknown tab', () => {
  // Filtered to the known settings-tab labels: MembersView unconditionally mounts
  // MemberRoleMatrix's own `.pf-tab` disclosure toggle ("What can each role do?"), which
  // would otherwise pollute a bare `.pf-tab` scan once the fallback lands on Members.
  const SETTINGS_TAB_LABELS = new Set([
    'Members',
    'Roles',
    'ERP connectors',
    'API & webhooks',
    'Signing & certificates',
    'Company',
  ])
  function settingsStripLabels(): string[] {
    return Array.from(document.querySelectorAll('.pf-tab'))
      .map((el) => el.textContent ?? '')
      .filter((t) => SETTINGS_TAB_LABELS.has(t))
  }

  async function resolvedSettingsBoot(path: string) {
    await bootAt(path)
    const ctx = requireCtx()
    return {
      view: ctx.view,
      settingsTab: ctx.settingsTab,
      alignedUrl: window.location.pathname,
      stripLabels: settingsStripLabels(),
    }
  }

  it('boot_firmSettingsCompanyFallsBackIdenticallyToAnUnknownTab', async () => {
    const company = await resolvedSettingsBoot('/settings/company')
    cleanup()
    capturedCtx = undefined
    const nonsense = await resolvedSettingsBoot('/settings/nonsense')

    expect(company, 'the firm fallback must equal the unknown-tab fallback exactly').toEqual(nonsense)
    // An equivalence alone is satisfiable by two identically-broken renders -- pin the
    // literal shape too.
    expect(company, 'and both must equal the literal expected shape, not just each other').toEqual({
      view: 'settings',
      settingsTab: 'members',
      alignedUrl: '/settings',
      stripLabels: ['Members', 'Roles', 'ERP connectors', 'API & webhooks', 'Signing & certificates'],
    })
  })
})

describe('ROUTE-04-06 AC-2: control needle -- an in-house workspace keeps the Company tab', () => {
  it('boot_inhouseSettingsCompanyRendersTheCompanyPanel', async () => {
    await bootAt('/settings/company', { persona: APP_PERSONAS.inhouse })
    const ctx = requireCtx()

    expect(ctx.settingsTab, 'in-house must not fall back -- company is a real tab for this mode').toBe('company')
    expect(window.location.pathname, 'the URL must keep the tab segment for in-house').toBe('/settings/company')
    // The panel itself, not just the state -- proves AC-1's fallback is a real refusal,
    // not a tab that never had content in the first place.
    expect(screen.getByText('Your company'), 'the Company panel must actually render').toBeTruthy()
  })
})

