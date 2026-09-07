// @vitest-environment jsdom
// vitest.config.ts stays `environment: 'node'` for every other suite.
//
// The popstate restore. Harness is App.routeNavigate.test.tsx's: the real <App/>, a
// session in a stubbed localStorage, ctx captured through a mocked Sidebar.

import { StrictMode } from 'react'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { EMPTY_BUCKET } from './lib/dashboard'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const JOB_A = 'c3d4e5f6-a7b8-4c3d-9e4f-5a6b7c8d9e0f'
const REVIEW_ID = 'a1b2c3d4-e5f6-47a8-89ab-cdef01234567'
const REVIEW_ID_2 = 'b2c3d4e5-f6a7-48b9-9abc-def012345678'
const REVIEW_ID_3 = 'c1c2c3c4-c5c6-47c7-89c8-c9cacbcccdce'
const AUDIT_INVOICE_ID = 'd1e2f3a4-b5c6-47d8-89ab-cdef01234567'
const INVOICE_ID = 'aaaaaaaa-0000-4000-8000-000000000001'

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

// Records every mount, including the job id it carried -- the Q6 oracle needs to see the
// screen did NOT render, not just that extractionJobId cleared.
const { extractionReviewMounts } = vi.hoisted(() => ({ extractionReviewMounts: [] as unknown[] }))
vi.mock('./components/ExtractionReview', () => ({
  ExtractionReview: (p: { jobId: unknown }) => {
    extractionReviewMounts.push(p.jobId)
    return null
  },
}))

// Same recorder for the detail screen (ROUTE-06-02): AC-1 asks that NO InvoiceDetail render
// for the previous company's invoice, which ctx.importedInvoiceId alone cannot show. The real
// screen also needs a live gateway fixture the roster harness below does not model.
const { invoiceDetailMounts } = vi.hoisted(() => ({ invoiceDetailMounts: [] as unknown[] }))
vi.mock('./components/InvoiceDetail', () => ({
  InvoiceDetail: (p: { ctx: { importedInvoiceId: unknown } }) => {
    invoiceDetailMounts.push(p.ctx.importedInvoiceId)
    return null
  },
}))

// AC-3's mid-wizard spec needs a real createStep: 'mapping' -- previewImport is the one
// network call that gates it, mocked here so reaching it needs no XHR/FakeXhr at all.
vi.mock('./lib/importApi', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./lib/importApi')>()
  return {
    ...actual,
    previewImport: vi.fn().mockResolvedValue({
      document_id: 'doc-mid-wizard-mapping',
      format: 'csv',
      delimiter: ',',
      encoding: 'utf-8',
      columns: ['invoice_number', 'total'],
      sample_rows: [['INV-1', '100']],
      rows_total: 1,
    }),
  }
})

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

// jsdom runs no real history stack for back()/forward() -- move the URL the way the
// browser would, then fire the event the browser fires.
async function popTo(path: string) {
  window.history.replaceState(null, '', path)
  await act(async () => {
    window.dispatchEvent(new PopStateEvent('popstate'))
  })
}

describe('AC-1: Back from a top-level view restores the previously visited view', () => {
  it('popstate_backFromATopLevelViewReturnsToThePreviousView', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.nav('audit')
    })

    await popTo('/invoices')
    expect(requireCtx().view, 'Back must restore invoices, not fall back to dashboard').toBe('invoices')
  })

  // Three steps, not two: a handler hard-wired to one view passes row 1 by accident and
  // fails rows 2 and 3 -- a two-item fixture cannot discriminate a skip from a stop.
  it('popstate_aThreeStepChainWalksBackInOrder', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.nav('audit')
    })
    await act(async () => {
      capturedCtx!.nav('settings')
    })

    await popTo('/audit')
    expect(requireCtx().view, 'first Back must land on audit').toBe('audit')

    await popTo('/invoices')
    expect(requireCtx().view, 'second Back must land on invoices').toBe('invoices')

    await popTo('/')
    expect(requireCtx().view, 'third Back must land on dashboard').toBe('dashboard')
  })
})

describe('AC-2: Forward re-applies the view it left', () => {
  it('popstate_forwardReAppliesTheViewItLeft', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.nav('audit')
    })

    await popTo('/invoices')
    // Floor: Forward is meaningless to assert unless Back actually moved the view away
    // from 'audit' first -- otherwise the two hops could cancel out on a stuck value.
    expect(requireCtx().view, 'Back must land on invoices before Forward can be meaningful').toBe('invoices')

    await popTo('/audit')
    expect(requireCtx().view, 'Forward must re-apply audit').toBe('audit')
  })
})

describe('AC-3: the handler performs no history write', () => {
  it('popstate_theHandlerWritesNoHistory', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.nav('audit')
    })

    // Move the URL the way a real Back press would BEFORE installing the spies -- this
    // harness's own move must not be mistaken for the handler's.
    window.history.replaceState(null, '', '/invoices')
    const pushSpy = vi.spyOn(window.history, 'pushState')
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    const lengthBefore = window.history.length
    // No fragment component -- subtask 05 removes the last writer that could have put one
    // on the url, so this comparison is pathname+search only from here on.
    const urlBeforeDispatch = window.location.pathname + window.location.search

    await act(async () => {
      window.dispatchEvent(new PopStateEvent('popstate'))
    })

    // AC-3's actual claim is "no duplicate history entry per Back press", not "zero
    // history-API calls" -- the review mirror is scoped to `view === 'create'` (ROUTE-03-03)
    // and this transition is audit -> invoices, so it fires nothing here; on a transition
    // that DID touch create it would still only rewrite the URL it already is (a no-op).
    // Assert the handler pushes nothing, adds no entry, and any replaceState observed is
    // that no-op rewrite -- a handler that wrote a *different* URL, or pushed, still fails.
    expect(pushSpy, 'the popstate handler must never call pushState').not.toHaveBeenCalled()
    expect(window.history.length, 'a popstate restore must add no history entry').toBe(lengthBefore)
    for (const call of replaceSpy.mock.calls) {
      const url = call[2]
      expect(url, 'any replaceState after a popstate must rewrite the current URL, not a different one').toBe(
        urlBeforeDispatch,
      )
    }
  })
})

describe('AC-4: exactly one listener is registered, and it is removed on unmount', () => {
  it('popstate_exactlyOneListenerIsRegisteredAndItIsRemovedOnUnmount', async () => {
    const addSpy = vi.spyOn(window, 'addEventListener')
    const removeSpy = vi.spyOn(window, 'removeEventListener')
    const { unmount } = await bootAt('/')

    const addCalls = addSpy.mock.calls.filter((c) => c[0] === 'popstate')
    // Floor before indexing: an empty match set must fail loudly, not read as "removed
    // cleanly" by never reaching the .toHaveLength(1) below.
    expect(addCalls.length, 'no addEventListener("popstate", ...) call was recorded at all').toBeGreaterThan(0)
    expect(addCalls, 'exactly one popstate listener must be registered').toHaveLength(1)
    const handler = addCalls[0]![1]

    unmount()

    const removeCalls = removeSpy.mock.calls.filter((c) => c[0] === 'popstate')
    expect(removeCalls.length, 'no removeEventListener("popstate", ...) call was recorded at all').toBeGreaterThan(0)
    expect(removeCalls, 'exactly one popstate listener must be removed').toHaveLength(1)
    expect(removeCalls[0]![1], 'the removed handler must be the same reference that was added').toBe(handler)
  })
})

describe('AC-5: an unrecognised path restores dashboard', () => {
  it('popstate_anUnrecognisedPathRestoresDashboard', async () => {
    await bootAt('/audit')
    expect(requireCtx().view, 'sanity: booting at /audit must seed that view').toBe('audit')

    await popTo('/gone')
    expect(
      requireCtx().view,
      'a popstate onto an unrecognised path must fall back to dashboard, not leave audit stale',
    ).toBe('dashboard')
  })
})

describe('Core AC 4: the sessions first screen pushed nothing to go back to', () => {
  it('popstate_theSessionsFirstScreenPushedNothingToGoBackTo', async () => {
    const pushSpy = vi.spyOn(window.history, 'pushState')
    await bootAt('/')
    requireCtx()

    expect(
      pushSpy,
      'the boot render must not push a history entry -- the workspace\'s first entry is the landing hand-off\'s own',
    ).not.toHaveBeenCalled()
  })
})

describe('AC-6 (Q6 Back half): Back after a company switch cannot reach the company just left', () => {
  // A hardcoded popTo target can't test this: it never consults what switchClient actually
  // wrote to history, so the assertion holds regardless of whether the scrub ran (route-02-06
  // Stage 1 finding). Instead capture the real write: replaceState calls fired BEFORE
  // switchClient's own pushState land on the entry being left; anything after targets the
  // entry just pushed to, not the one Back returns to. If none fired before the push, the
  // scrub didn't run and the left-behind entry is still the pre-switch URL.
  it('popstate_backAfterACompanySwitchCannotReachTheCompanyJustLeft', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.openExtraction(JOB_A)
    })
    let ctx = requireCtx()
    const preSwitchUrl = window.location.pathname
    expect(preSwitchUrl, 'sanity: openExtraction must push /extraction/<jobId>').toBe(`/extraction/${JOB_A}`)
    expect(ctx.extractionJobId, 'sanity: the job id must be set').toBe(JOB_A)

    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    const pushSpy = vi.spyOn(window.history, 'pushState')
    await act(async () => {
      capturedCtx!.switchClient('other-entity-999')
    })
    ctx = requireCtx()
    expect(ctx.extractionJobId, 'sanity: switchClient (ROUTE-01-03) must already clear the job').toBeNull()

    const firstPushOrder = pushSpy.mock.invocationCallOrder[0]
    expect(firstPushOrder, 'sanity: switchClient must push the dashboard entry').toBeDefined()
    const preNavReplaces = replaceSpy.mock.calls.filter(
      (_, i) => replaceSpy.mock.invocationCallOrder[i]! < firstPushOrder!,
    )
    const leftBehindUrl =
      preNavReplaces.length > 0 ? (preNavReplaces[preNavReplaces.length - 1]![2] as string) : preSwitchUrl

    // The spy is push-only and openExtraction already recorded one mount above -- reset so
    // the assertion below measures only the window after Back, not that earlier mount.
    extractionReviewMounts.length = 0

    await popTo(leftBehindUrl)
    ctx = requireCtx()
    expect(ctx.view, 'Back must restore whatever the scrub actually left behind, not extraction').toBe('invoices')
    expect(ctx.extractionJobId, 'the cleared job must not come back on a popstate restore').toBeNull()
    expect(
      extractionReviewMounts,
      'no ExtractionReview may render for a null job id -- App.tsx\'s view===extraction && extractionJobId!=null gate',
    ).toHaveLength(0)
  })
})

// QA (route-02-06): the detail mirror of AC-6 above. AC-4 names this exact scenario, but
// the only pre-existing detail-side test (popstate_backToInvoiceAfterACompanySwitchRendersNoStaleSelection,
// below) hardcodes popTo('/invoice') -- the same vacuous pattern Stage 1 found for the
// extraction case: deleting the scrub still leaves it green, since it never consults what
// switchClient actually wrote. Same spy-and-capture fix as V-4, applied to the detail path.
describe('QA adversarial (route-02-06, AC-4): Back after a company switch from /invoices/<id> cannot reach the company just left', () => {
  it('popstate_backAfterACompanySwitchFromDetailCannotReachTheCompanyJustLeft', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.openImportedInvoice(INVOICE_ID)
    })
    let ctx = requireCtx()
    const preSwitchUrl = window.location.pathname
    expect(preSwitchUrl, 'sanity: openImportedInvoice must push /invoices/<id>').toBe(`/invoices/${INVOICE_ID}`)
    expect(ctx.importedInvoiceId, 'sanity: the selection must be armed').toBe(INVOICE_ID)

    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    const pushSpy = vi.spyOn(window.history, 'pushState')
    await act(async () => {
      capturedCtx!.switchClient('other-entity-444')
    })
    ctx = requireCtx()
    expect(ctx.importedInvoiceId, 'sanity: switchClient must already clear the selection').toBeNull()

    const firstPushOrder = pushSpy.mock.invocationCallOrder[0]
    expect(firstPushOrder, 'sanity: switchClient must push the dashboard entry').toBeDefined()
    const preNavReplaces = replaceSpy.mock.calls.filter(
      (_, i) => replaceSpy.mock.invocationCallOrder[i]! < firstPushOrder!,
    )
    const leftBehindUrl =
      preNavReplaces.length > 0 ? (preNavReplaces[preNavReplaces.length - 1]![2] as string) : preSwitchUrl

    await popTo(leftBehindUrl)
    ctx = requireCtx()
    expect(ctx.view, 'Back must restore whatever the scrub actually left behind, not detail').toBe('invoices')
    expect(ctx.importedInvoiceId, 'the cleared selection must not come back on a popstate restore').toBeNull()
  })
})

describe('N-5: Back onto the bare list clears a stale invoice id', () => {
  it('popstate_backFromDetailToInvoicesClearsTheImportedId', async () => {
    await bootAt(`/invoices/${INVOICE_ID}`)
    let ctx = requireCtx()
    expect(ctx.view, 'sanity: booting at /invoices/<id> must seed detail').toBe('detail')
    expect(ctx.importedInvoiceId, 'sanity: the boot id must seed the selection').toBe(INVOICE_ID)

    await popTo('/invoices')
    ctx = requireCtx()
    expect(ctx.view, 'Back must restore the invoices list').toBe('invoices')
    expect(ctx.importedInvoiceId, 'Back onto the bare list must clear the stale invoice id').toBeNull()
    expect(window.location.pathname, 'the URL must agree with the restored view').toBe('/invoices')
  })
})

describe('N-6: Back onto /extraction/<jobId> restores the job id together with the view', () => {
  it('popstate_backOntoExtractionRestoresTheJobId', async () => {
    await bootAt('/invoices')
    const ctx0 = requireCtx()
    expect(ctx0.view, 'sanity: booting at /invoices must seed the list').toBe('invoices')

    await popTo(`/extraction/${JOB_A}`)
    const ctx = requireCtx()
    expect(ctx.view, 'Back must restore the extraction view').toBe('extraction')
    expect(
      ctx.extractionJobId,
      'Back must restore the job id in the same commit as the view, not a render later',
    ).toBe(JOB_A)
    expect(window.location.pathname, 'the URL must agree with the restored view').toBe(`/extraction/${JOB_A}`)
  })
})

// QA gap-fill: task-914's own AC-5 ("Back onto /invoices/<id> from elsewhere restores detail
// AND the id") has no row in the architect's Test Specs table (only N-5's reverse direction
// and N-6's extraction mirror do) -- this is the missing mirror of N-6 for the detail side.
describe('AC-5: Back onto /invoices/<id> from elsewhere restores detail and the id', () => {
  it('popstate_backOntoInvoiceDetailRestoresTheImportedId', async () => {
    await bootAt('/invoices')
    const ctx0 = requireCtx()
    expect(ctx0.view, 'sanity: booting at /invoices must seed the list').toBe('invoices')

    await popTo(`/invoices/${INVOICE_ID}`)
    const ctx = requireCtx()
    expect(ctx.view, 'Back must restore the detail view').toBe('detail')
    expect(
      ctx.importedInvoiceId,
      'Back must restore the invoice id in the same commit as the view, not a render later',
    ).toBe(INVOICE_ID)
    expect(window.location.pathname, 'the URL must agree with the restored view').toBe(`/invoices/${INVOICE_ID}`)
  })
})

describe('Adversarial: Back onto a view that takes no id clears whichever id was live', () => {
  it('popstate_backOntoAnIdlessViewClearsALiveExtractionJob', async () => {
    await bootAt(`/extraction/${JOB_A}`)
    let ctx = requireCtx()
    expect(ctx.extractionJobId, 'sanity: booting at /extraction/<id> must seed the job').toBe(JOB_A)

    await popTo('/settings')
    ctx = requireCtx()
    expect(ctx.view, 'Back onto an id-less view must still restore that view').toBe('settings')
    expect(ctx.extractionJobId, 'an id-less target must clear a live job id, not leave it stale').toBeNull()
    expect(ctx.importedInvoiceId, 'an id-less target must not carry an invoice id either').toBeNull()
  })
})

describe('Adversarial: rapid double-Back across two different drill-down ids', () => {
  it('popstate_rapidDoubleBackAcrossTwoIdsLandsOnTheSecondIdOnly', async () => {
    await bootAt('/invoices')

    // Two Back presses in the same flush, each landing on a DIFFERENT id-carrying path --
    // the final commit must carry ONLY the second id, with no bleed from the first.
    await act(async () => {
      window.history.replaceState(null, '', `/invoices/${INVOICE_ID}`)
      window.dispatchEvent(new PopStateEvent('popstate'))
      window.history.replaceState(null, '', `/extraction/${JOB_A}`)
      window.dispatchEvent(new PopStateEvent('popstate'))
    })

    const ctx = requireCtx()
    expect(ctx.view, 'the second hop must win, not stall on the first').toBe('extraction')
    expect(ctx.extractionJobId, 'the second hop\'s job id must land').toBe(JOB_A)
    expect(ctx.importedInvoiceId, 'the first hop\'s invoice id must not survive the second hop').toBeNull()
  })
})

describe('Adversarial: an id needing percent-encoding round-trips through push then Back', () => {
  it('popstate_anEncodedIdRoundTripsThroughPushThenBack', async () => {
    const RAW_ID = 'job a/b?c'
    await bootAt('/')
    await act(async () => {
      capturedCtx!.openExtraction(RAW_ID)
    })
    let ctx = requireCtx()
    expect(
      window.location.pathname,
      'the pushed URL must percent-encode the raw id, not embed it literally',
    ).toBe(`/extraction/${encodeURIComponent(RAW_ID)}`)
    expect(ctx.extractionJobId, 'the live atom keeps the raw, undecoded id').toBe(RAW_ID)

    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await popTo(`/extraction/${encodeURIComponent(RAW_ID)}`)
    ctx = requireCtx()
    expect(ctx.view, 'Back must restore the extraction view').toBe('extraction')
    expect(ctx.extractionJobId, 'Back must decode the id back to its original raw form').toBe(RAW_ID)
  })
})

// --- QA adversarial coverage below (route-01-04, Mode B) --------------------------

describe('Adversarial: rapid double-Back and Back-Forward-Back', () => {
  it('popstate_rapidDoubleBackLandsOnTheCorrectFinalView', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.nav('audit')
    })
    await act(async () => {
      capturedCtx!.nav('settings')
    })
    const lengthBefore = window.history.length

    // Two Back presses fired before React gets a chance to settle between them --
    // both events land in the same act() flush, the way two fast physical clicks would.
    await act(async () => {
      window.history.replaceState(null, '', '/audit')
      window.dispatchEvent(new PopStateEvent('popstate'))
      window.history.replaceState(null, '', '/invoices')
      window.dispatchEvent(new PopStateEvent('popstate'))
    })

    expect(requireCtx().view, 'a rapid double-Back must still land on the second entry, not stall on the first').toBe(
      'invoices',
    )
    expect(window.location.pathname, 'the URL must agree with the view after the double-Back').toBe('/invoices')
    expect(window.history.length, 'neither popstate may add a history entry').toBe(lengthBefore)
  })

  it('popstate_backForwardBackKeepsViewAndUrlInAgreement', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.nav('audit')
    })
    await act(async () => {
      capturedCtx!.nav('settings')
    })

    await popTo('/audit')
    expect(requireCtx().view, 'Back must land on audit').toBe('audit')
    expect(window.location.pathname, 'the URL must agree with the view after Back').toBe('/audit')

    await popTo('/settings')
    expect(requireCtx().view, 'Forward must re-apply settings').toBe('settings')
    expect(window.location.pathname, 'the URL must agree with the view after Forward').toBe('/settings')

    await popTo('/audit')
    expect(requireCtx().view, 'the second Back must land on audit again, not settings or dashboard').toBe('audit')
    expect(window.location.pathname, 'the URL must agree with the view after the second Back').toBe('/audit')
  })
})

describe('Adversarial: Back into a view whose data was cleared elsewhere', () => {
  it('popstate_backToInvoiceAfterACompanySwitchRendersNoStaleSelection', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.openImportedInvoice('dddddddd-1111-4111-8111-111111111111')
    })
    let ctx = requireCtx()
    expect(window.location.pathname, 'sanity: openImportedInvoice must push /invoices/<id>').toBe(
      '/invoices/dddddddd-1111-4111-8111-111111111111',
    )
    expect(ctx.importedInvoiceId, 'sanity: the selection must be armed').toBe('dddddddd-1111-4111-8111-111111111111')

    await act(async () => {
      capturedCtx!.switchClient('other-entity-777')
    })
    ctx = requireCtx()
    expect(ctx.importedInvoiceId, 'sanity: switchClient must already clear the selection').toBeNull()

    await popTo('/invoice')
    ctx = requireCtx()
    expect(ctx.view, 'Back must restore the detail view').toBe('detail')
    // Matches decision [route-01-limitations]: a cold /invoice has no selection and
    // InvoiceDetail renders its EmptyState -- a popstate-reached /invoice must be the
    // same, not the previous company's row.
    expect(ctx.importedInvoiceId, 'a popstate-restored /invoice must not resurrect an imported-invoice target either').toBeNull()
  })
})

describe('Adversarial: the listener survives a company switch', () => {
  it('popstate_backStillWorksAfterACompanySwitch', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.nav('audit')
    })
    // switchClient does not remount Workspace -- the mount-only listener (deps []) must
    // still be the one live handler afterward.
    await act(async () => {
      capturedCtx!.switchClient('other-entity-321')
    })
    expect(requireCtx().view, 'sanity: switchClient lands on dashboard').toBe('dashboard')

    await popTo('/audit')
    expect(requireCtx().view, 'Back must still work after a company switch').toBe('audit')
  })
})

describe('Adversarial: StrictMode double-invocation', () => {
  it('popstate_underStrictModeExactlyOneListenerSurvives', async () => {
    const addSpy = vi.spyOn(window, 'addEventListener')
    const removeSpy = vi.spyOn(window, 'removeEventListener')

    window.history.replaceState(null, '', '/')
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    vi.resetModules()
    const { default: App } = await import('./App')
    render(
      <StrictMode>
        <App />
      </StrictMode>,
    )
    requireCtx()

    const addCalls = addSpy.mock.calls.filter((c) => c[0] === 'popstate')
    const removeCalls = removeSpy.mock.calls.filter((c) => c[0] === 'popstate')
    // Floor: StrictMode really double-invoked the effect here, or the count below is
    // meaningless -- a mount that never doubled would also show a net of 1.
    expect(addCalls.length, 'StrictMode must have invoked the mount effect at least twice').toBeGreaterThan(1)
    expect(
      addCalls.length - removeCalls.length,
      'exactly one live popstate listener must survive StrictMode\'s mount/unmount/remount, not two',
    ).toBe(1)

    // A doubled listener would call setView twice per Back -- invisible if this test only
    // checked the final view (setView('audit') twice is idempotent). The count above is
    // the structural proof; this just confirms the surviving listener still functions.
    await popTo('/audit')
    expect(requireCtx().view, 'the surviving listener must still restore the view').toBe('audit')
  })
})

describe('Adversarial: the listener does not re-register on view change', () => {
  it('popstate_theListenerDoesNotReRegisterOnViewChange', async () => {
    await bootAt('/')
    const addSpy = vi.spyOn(window, 'addEventListener')
    const removeSpy = vi.spyOn(window, 'removeEventListener')

    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.nav('audit')
    })

    // A non-empty dependency array on the popstate effect would tear it down and re-add
    // it on every view change -- churn that AC-4's own mount-time check cannot see,
    // because it never changes view before counting.
    expect(
      addSpy.mock.calls.filter((c) => c[0] === 'popstate'),
      'the popstate listener must be registered once at mount and never again while the view changes',
    ).toHaveLength(0)
    expect(
      removeSpy.mock.calls.filter((c) => c[0] === 'popstate'),
      'the popstate listener must not be torn down while the component stays mounted',
    ).toHaveLength(0)
  })
})

describe('Adversarial: the three URL writers on create with a live review path', () => {
  // Single continuous review session (the shape ROUTE-03-04 will pin): the mirror and the
  // popstate handler never disagree here, because reviewBatchIds and createStep are never
  // reset in between -- only the view hops away and back.
  it('popstate_multiHopBackIntoALiveReviewHashComposesCorrectly', async () => {
    const path = `/imports/${REVIEW_ID}/review`
    await bootAt(path)
    let ctx = requireCtx()
    expect(ctx.view, 'sanity: the review path boots straight into create').toBe('create')
    expect(ctx.reviewBatchIds, 'sanity: the review path must seed reviewBatchIds').toEqual([REVIEW_ID])

    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    expect(window.location.pathname, 'nav away must land on invoices').toBe('/invoices')

    await act(async () => {
      capturedCtx!.nav('audit')
    })

    await popTo('/invoices')
    ctx = requireCtx()
    expect(ctx.view, 'first Back must land on invoices').toBe('invoices')

    await popTo(path)
    ctx = requireCtx()
    // Writer order on this popstate: (1) the browser applies the restored path, (2) the
    // popstate handler re-derives view AND (this story's arm) createStep/reviewBatchIds
    // from that same path, (3) the mirror re-runs on the view change and recomputes from
    // those just-restored values -- idempotent with the URL the browser already set.
    // Passes for the real reason now, not by omission.
    expect(ctx.view, 'second Back must land back on create').toBe('create')
    expect(window.location.pathname, 'the path must be the review path after the composed writers settle').toBe(
      path,
    )
    expect(ctx.reviewBatchIds, 'reviewBatchIds must be exactly the one id throughout, never dropped or swapped').toEqual([
      REVIEW_ID,
    ])
  })

  // The cross-view sibling: `view` actually leaves 'create' in between, so the mirror DOES
  // re-run on the way back in and recomputes from whatever createStep/reviewBatchIds hold
  // live -- not from the URL the browser just restored. Retires decision
  // [create-step-not-restored]'s "not reproduced" note: reaching a second live batch needs
  // no CreateFlow, the same bootAt-onto-a-review-path shortcut
  // link_anExternallyHeldReviewLinkStillColdLoadsItsOwnBatch already uses.
  it('popstate_crossViewBackOntoAReviewEntryIsNotRelinkedByTheMirror', async () => {
    await bootAt(`/imports/${REVIEW_ID_2}/review`)
    expect(requireCtx().reviewBatchIds, 'sanity: live memory starts on the second batch').toEqual([REVIEW_ID_2])

    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    expect(requireCtx().view, 'sanity: nav must actually leave create').toBe('invoices')

    await popTo(`/imports/${REVIEW_ID}/review`)
    const ctx = requireCtx()
    // Proof of stimulus: view must actually be back on create for the mirror's recompute
    // to even be in play here.
    expect(ctx.view, 'the popstate must land back on create').toBe('create')
    expect(ctx.reviewBatchIds, "the restored entry's id must win, not the live second batch").toEqual([REVIEW_ID])
    expect(
      window.location.pathname,
      'the mirror must not overwrite the address bar the browser just restored',
    ).toBe(`/imports/${REVIEW_ID}/review`)
  })
})

describe("ROUTE-03-04 AC-1: Back onto a review entry restores that entry's ids, not the live ones", () => {
  it('popstate_backOntoAReviewEntryRestoresThatBatchNotTheLiveOne', async () => {
    await bootAt(`/imports/${REVIEW_ID_2}/review`)
    let ctx = requireCtx()
    expect(ctx.view, 'sanity: the review path boots straight into create').toBe('create')
    expect(ctx.reviewBatchIds, 'sanity: live memory starts on the second batch').toEqual([REVIEW_ID_2])

    await popTo(`/imports/${REVIEW_ID}/review`)
    ctx = requireCtx()
    // `view` alone reads 'create' with or without the fix -- it cannot discriminate the bug.
    expect(ctx.view, 'sanity: a same-view Back never leaves create').toBe('create')
    expect(ctx.reviewBatchIds, "Back must restore the RESTORED entry's ids, not the ones left in memory").toEqual([
      REVIEW_ID,
    ])
    expect(ctx.createStep, 'Back onto a review path must land on the review step').toBe('review')
    expect(window.location.pathname, 'the URL must be the entry the browser actually restored').toBe(
      `/imports/${REVIEW_ID}/review`,
    )
  })
})

describe('ROUTE-03-04 AC-2: Forward re-applies the review route symmetrically', () => {
  it('popstate_forwardReAppliesTheReviewRoute', async () => {
    await bootAt(`/imports/${REVIEW_ID_2}/review`)
    await popTo(`/imports/${REVIEW_ID}/review`)
    // Floor: Forward is meaningless to assert unless Back actually moved off the second
    // batch first.
    expect(requireCtx().reviewBatchIds, 'the Back above must actually land on the first batch').toEqual([REVIEW_ID])

    await popTo(`/imports/${REVIEW_ID_2}/review`)
    const ctx = requireCtx()
    expect(ctx.view, 'proof of stimulus: forward must still resolve to create').toBe('create')
    expect(ctx.reviewBatchIds, 'Forward must restore the LATER batch, not stay on the entry Back just left').toEqual([
      REVIEW_ID_2,
    ])
    expect(ctx.createStep, 'Forward onto a review path must land on the review step').toBe('review')
    expect(window.location.pathname, 'the URL must be the forward entry the browser restored').toBe(
      `/imports/${REVIEW_ID_2}/review`,
    )
  })
})

describe('ROUTE-03-04: every id in a run survives a Back press', () => {
  it('popstate_everyIdInARunSurvivesABackPress', async () => {
    const ids = [REVIEW_ID, REVIEW_ID_2, REVIEW_ID_3]
    await bootAt('/invoices')
    expect(requireCtx().reviewBatchIds, 'sanity: booting elsewhere seeds no batch ids').toEqual([])

    await popTo(`/imports/${ids.join(',')}/review`)
    const ctx = requireCtx()
    expect(ctx.view, 'proof of stimulus: the popstate must land on create').toBe('create')
    expect(ctx.reviewBatchIds, 'every id in the run must survive the Back, in order').toEqual(ids)
    expect(ctx.createStep, 'a multi-id review path must land on the review step too').toBe('review')
    expect(window.location.pathname, 'the URL must carry all three ids').toBe(`/imports/${ids.join(',')}/review`)
  })
})

// Shared by both AC-3 specs below: reaches createStep 'mapping' with one picked file and
// its column mapping resolved, via the mocked previewImport -- no XHR, no real gateway.
async function driveToMidWizardMapping() {
  // The entities/members/etc. fetches also fire once VITE_GATEWAY_URL is set -- a generic
  // safe stub, not the real network, same idiom as App.routeReviewHash.test.tsx's routeFetch.
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
  vi.stubGlobal(
    'fetch',
    vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        json: () =>
          Promise.resolve({
            entities: [],
            policies: [],
            members: [],
            roles: [],
            invoices: [],
            pagination: { limit: 0, offset: 0, total: 0 },
          }),
      }),
    ),
  )
  await bootAt('/')
  await act(async () => {
    capturedCtx!.openCreate()
  })
  const file = new File(['invoice_number,total\nINV-1,100'], 'invoices.csv', { type: 'text/csv' })
  await act(async () => {
    capturedCtx!.addPickedFiles([file])
  })
  await act(async () => {
    capturedCtx!.readAllColumns()
  })
  await waitFor(() => expect(requireCtx().createStep, 'setup must actually reach mapping').toBe('mapping'))
}

describe('ROUTE-03-04 AC-3: a popstate onto a non-review path never resets the wizard', () => {
  it('popstate_aNonReviewEntryNeverResetsTheWizard', async () => {
    await driveToMidWizardMapping()
    expect(requireCtx().reviewBatchIds, 'sanity: mid-wizard carries no batch ids').toEqual([])

    await popTo('/invoices')
    const ctx = requireCtx()
    // Proof of stimulus: the handler DID run -- a passing atoms-untouched assertion below
    // is not a no-op default.
    expect(ctx.view, 'the popstate must actually have been processed').toBe('invoices')
    expect(ctx.createStep, 'a non-review Back must never reset the wizard mid-flow').toBe('mapping')
    expect(ctx.reviewBatchIds, 'a non-review Back must not touch reviewBatchIds either').toEqual([])
  })
})

describe('ROUTE-03-04 AC-3: a popstate onto a BARE /create entry leaves the wizard exactly as it was', () => {
  it('popstate_aBareCreateEntryLeavesTheWizardExactlyAsItWas', async () => {
    await driveToMidWizardMapping()
    expect(requireCtx().reviewBatchIds, 'sanity: mid-wizard carries no batch ids').toEqual([])
    expect(requireCtx().pickedFiles, 'sanity: the picked file is there before the pop').toHaveLength(1)
    expect(requireCtx().mapping, 'sanity: the resolved mapping is there before the pop').not.toBeNull()

    await popTo('/create')
    // Proof of stimulus: the pop actually landed on the bare path -- not a no-op driver.
    expect(window.location.pathname, 'the popstate must actually have moved to /create').toBe('/create')

    const ctx = requireCtx()
    // Pins the arm's gate: ids present, not view === 'create' -- a bare /create also
    // parses to 'create' but with no ids, and must never jump the wizard to review.
    expect(ctx.createStep, 'a bare /create Back must not reset the wizard step').toBe('mapping')
    expect(ctx.reviewBatchIds, 'a bare /create Back must not touch reviewBatchIds').toEqual([])
    expect(ctx.pickedFiles, 'the picked file must survive a bare /create Back').toHaveLength(1)
    expect(ctx.mapping, 'the resolved mapping must survive a bare /create Back').not.toBeNull()
  })
})

describe('ROUTE-03-04 AC-4: a popstate onto a review path still writes no history entry', () => {
  it('popstate_theHandlerWritesNoHistoryEntry', async () => {
    await bootAt('/invoices')
    // Move the URL the way a Back press would, before installing the spies.
    window.history.replaceState(null, '', `/imports/${REVIEW_ID}/review`)
    const pushSpy = vi.spyOn(window.history, 'pushState')
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    const lengthBefore = window.history.length
    const urlBeforeDispatch = window.location.pathname + window.location.search

    await act(async () => {
      window.dispatchEvent(new PopStateEvent('popstate'))
    })

    const ctx = requireCtx()
    // Proof of stimulus: the arm must actually have restored the ids for this to be a
    // meaningful "no extra history" check.
    expect(ctx.reviewBatchIds, 'the arm must actually have run').toEqual([REVIEW_ID])
    expect(pushSpy, 'the popstate handler must never call pushState').not.toHaveBeenCalled()
    expect(window.history.length, 'a popstate restore onto a review path must add no history entry').toBe(
      lengthBefore,
    )
    for (const call of replaceSpy.mock.calls) {
      expect(call[2], 'any replaceState after this popstate must rewrite the current URL, not a different one').toBe(
        urlBeforeDispatch,
      )
    }
  })
})

// --- ROUTE-04-05: the handler restores the other three owned atoms, not just view -------

describe('ROUTE-04-05 AC-1: Back also restores the committed invoice search term', () => {
  it('popstate_backRestoresTheInvoiceQuery', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.searchInvoices('acme')
    })
    expect(window.location.pathname + window.location.search, 'sanity: the term landed').toBe('/invoices?q=acme')

    // "nav('invoices') with no term": submitting an empty box commits '' and leaves a bare
    // /invoices entry -- a plain nav('invoices') is durable and cannot produce this (it
    // re-emits whatever term is already in state), so this is the real production action
    // that lands on invoices with no term. The earlier acme URL is what we restore below.
    await act(async () => {
      capturedCtx!.searchInvoices('')
    })
    expect(requireCtx().invoiceQuery, 'sanity: the term was actually cleared before the restore').toBe('')

    await popTo('/invoices?q=acme')
    expect(requireCtx().invoiceQuery, 'Back must restore the invoice search term from the URL').toBe('acme')
  })
})

describe('ROUTE-04-05 AC-2: Back also restores the audit invoice filter', () => {
  it('popstate_backRestoresTheAuditInvoiceFilter', async () => {
    await bootAt('/audit')
    expect(requireCtx().auditPrefilter, 'sanity: the filter starts cleared').toBeNull()

    await popTo(`/audit?invoice=${AUDIT_INVOICE_ID}`)
    expect(requireCtx().auditPrefilter, 'Back must restore the audit invoice filter from the URL').toEqual({
      invoiceId: AUDIT_INVOICE_ID,
      invoiceNumber: null,
    })
  })
})

describe('ROUTE-04-05 AC-3: Back and Forward also restore the settings tab', () => {
  it('popstate_backRestoresTheSettingsTab', async () => {
    await bootAt('/settings/roles')
    expect(requireCtx().settingsTab, 'sanity: booting at /settings/roles must seed that tab').toBe('roles')

    await popTo('/settings')
    expect(requireCtx().settingsTab, 'Back must restore the members tab').toBe('members')
  })

  // The reverse of the row above -- proves the restore is derived from the URL on every
  // dispatch, not a one-directional "reset to members" fallback that only looks correct here.
  it('popstate_forwardReAppliesTheTab', async () => {
    await bootAt('/settings')
    expect(requireCtx().settingsTab, 'sanity: booting at /settings must seed the members tab').toBe('members')

    await popTo('/settings/roles')
    expect(requireCtx().settingsTab, 'Forward must re-apply the roles tab').toBe('roles')
  })
})

describe('AC-4, extended: a filtered restore still writes no history entry', () => {
  // Green-by-design (M11, .claude/handoff.yaml). The shipped popstate_theHandlerWritesNoHistory
  // (:150) restores to an unfiltered URL, where a handler that wrongly routed an atom through
  // its URL-writing WRAPPER (setInvoiceQuery, setAuditInvoiceFilter) instead of the raw setter
  // would pass undetected: those wrappers close over view/settingsTab/invoiceQuery/auditPrefilter
  // from the render the mount-only effect (deps []) captured them in, so on an unfiltered restore
  // the stale URL they would rebuild happens to match. On a FILTERED restore it does not -- the
  // wrapper rebuilds from stale state, disagreeing with the real pre-dispatch URL -- and this
  // loop, not a static scan (routeWriterGuard's own evasion, subtask-03), is what catches it.
  it('popstate_writesNoHistoryEntry', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.nav('audit')
    })

    for (const filteredUrl of ['/invoices?q=acme', `/audit?invoice=${AUDIT_INVOICE_ID}`]) {
      window.history.replaceState(null, '', filteredUrl)
      const pushSpy = vi.spyOn(window.history, 'pushState')
      const replaceSpy = vi.spyOn(window.history, 'replaceState')
      const lengthBefore = window.history.length
      const urlBeforeDispatch = window.location.pathname + window.location.search

      await act(async () => {
        window.dispatchEvent(new PopStateEvent('popstate'))
      })

      expect(pushSpy, `${filteredUrl}: the popstate handler must never call pushState`).not.toHaveBeenCalled()
      expect(window.history.length, `${filteredUrl}: a popstate restore must add no history entry`).toBe(lengthBefore)
      for (const call of replaceSpy.mock.calls) {
        expect(
          call[2],
          `${filteredUrl}: any replaceState after a popstate must rewrite the current URL -- a wrapper-setter mistake rebuilds it from stale mount-time state instead`,
        ).toBe(urlBeforeDispatch)
      }
      pushSpy.mockRestore()
      replaceSpy.mockRestore()
    }
  })
})

// --- ROUTE-06-02: the company-identity clamp ----------------------------------------
//
// Everything above this line boots with NO gateway, so `clients` stays [] and
// `active.entityId` is null before AND after switchClient -- the clamp compares two entity
// ids and with both null it can never fire. Every spec below therefore needs a REAL
// two-entity roster; roster_theTwoEntityRosterActuallyMovesTheActiveCompany is the floor
// that proves it, and nothing else in this block is meaningful without it.
// Idiom: App.routeReviewHash.test.tsx:118-152, widened to two rows (App.handOff.test.tsx).

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
              entities: [entityRow(ENTITY_A, 'Clamp Co A', '12345678-0001'), entityRow(ENTITY_B, 'Clamp Co B', '12345678-0002')],
              pagination: { limit: 200, offset: 0, total: 2 },
            }),
        })
      }
      // One fallback wide enough that no screen a clamp journey passes through throws.
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () =>
          Promise.resolve({
            access_token: 'test-token',
            // `me` for signIn's /tenancy/v1/me leg -- App.tsx:1430 reads me.tenant.name.
            tenant: { id: 't-clamp', name: 'Clamp Tenant' },
            entities: [],
            policies: [],
            members: [],
            roles: [],
            invoices: [],
            clients: [],
            // The in-house dashboard reads rollup.totals directly (lib/dashboard.ts:242).
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
    expect(requireCtx().active.entityId, 'the roster never resolved -- every clamp spec below would be vacuous').toBe(
      ENTITY_A,
    ),
  )
  return rendered
}

// What the browser would restore for the entry currently on screen: its url and the
// company stamp the writer put on it. Never hardcoded -- a hardcoded stamp would pass even
// if no production writer ever stamped, which is exactly the vacuity AC-7 exists to stop.
function currentEntry(): { url: string; e: string | null } {
  return {
    url: window.location.pathname + window.location.search,
    e: (window.history.state as { e?: string | null } | null)?.e ?? null,
  }
}

// Sibling of popTo (:105) for a STAMPED entry. popTo itself is untouched -- its 35 call
// sites depend on the bare, state-less event. `{ e: null }` and a bare null state are the
// same "no company named" to the handler's `?? null` fold, so replaying a pre-fix entry
// through this helper is faithful.
async function popToStamped(path: string, e: string | null) {
  const state = { e }
  window.history.replaceState(state, '', path)
  await act(async () => {
    window.dispatchEvent(new PopStateEvent('popstate', { state }))
  })
}

describe('ROUTE-06-02 harness floor: the two-entity roster', () => {
  it('roster_theTwoEntityRosterActuallyMovesTheActiveCompany', async () => {
    await bootAtWithGateway('/')
    expect(requireCtx().active.entityId, 'before the switch the active company must be entity A').toBe(ENTITY_A)

    await act(async () => {
      capturedCtx!.switchClient(ENTITY_B)
    })
    expect(requireCtx().active.entityId, 'after the switch the active company must be entity B').toBe(ENTITY_B)
  })
})

// The four stale-entry specs. All RED until the clamp lands: today no production writer
// stamps, so currentEntry().e is null on every recorded entry, the replay names no company
// and the handler re-derives the previous company's selection straight off the URL.
describe('ROUTE-06-02 AC-1: an older entry from another company does not resume its selection', () => {
  it('popstate_anOlderDetailEntryFromAnotherCompanyDoesNotResumeItsInvoice', async () => {
    await bootAtWithGateway('/')
    await act(async () => {
      capturedCtx!.openImportedInvoice(INVOICE_ID)
    })
    const stale = currentEntry()
    expect(stale.url, 'sanity: openImportedInvoice must push /invoices/<id>').toBe(`/invoices/${INVOICE_ID}`)

    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.switchClient(ENTITY_B)
    })
    expect(requireCtx().active.entityId, 'floor: the switch must really move the active company').toBe(ENTITY_B)

    // openImportedInvoice already recorded one mount above -- reset so the count below
    // measures only the window after Back.
    invoiceDetailMounts.length = 0

    await popToStamped(stale.url, stale.e)
    const ctx = requireCtx()
    expect(ctx.importedInvoiceId, "Back onto entity A's invoice under entity B must not resume that selection").toBeNull()
    expect(invoiceDetailMounts, "no InvoiceDetail may render for the previous company's invoice").toHaveLength(0)
    expect(ctx.view, 'the view is carried, collapsed to the list the selection belonged to').toBe('invoices')
    expect(window.location.pathname, 'the URL must agree with the collapsed view').toBe('/invoices')
  })

  it('popstate_anOlderExtractionEntryFromAnotherCompanyDoesNotResumeItsJob', async () => {
    await bootAtWithGateway('/')
    await act(async () => {
      capturedCtx!.openExtraction(JOB_A)
    })
    const stale = currentEntry()
    expect(stale.url, 'sanity: openExtraction must push /extraction/<jobId>').toBe(`/extraction/${JOB_A}`)

    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.switchClient(ENTITY_B)
    })
    expect(requireCtx().active.entityId, 'floor: the switch must really move the active company').toBe(ENTITY_B)

    // openExtraction already recorded one mount above -- reset so the count below measures
    // only the window after Back.
    extractionReviewMounts.length = 0

    await popToStamped(stale.url, stale.e)
    const ctx = requireCtx()
    expect(ctx.extractionJobId, "Back onto entity A's job under entity B must not resume it").toBeNull()
    expect(extractionReviewMounts, 'no ExtractionReview may render for the previous company job').toHaveLength(0)
    expect(window.location.pathname, 'the URL must agree with the collapsed view').toBe('/invoices')
  })

  it('popstate_anOlderReviewEntryFromAnotherCompanyDoesNotResumeItsBatch', async () => {
    await bootAtWithGateway(`/imports/${REVIEW_ID}/review`)
    expect(requireCtx().reviewBatchIds, 'sanity: the review path must seed the batch').toEqual([REVIEW_ID])
    const stale = currentEntry()

    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.switchClient(ENTITY_B)
    })
    expect(requireCtx().active.entityId, 'floor: the switch must really move the active company').toBe(ENTITY_B)

    // switchClient (App.tsx:661) already cleared reviewBatchIds; the oracle here is that
    // popstate must not RE-ARM it from the restored path.
    await popToStamped(stale.url, stale.e)
    const ctx = requireCtx()
    expect(ctx.reviewBatchIds, "Back must not re-arm entity A's batch under entity B").toEqual([])
    expect(ctx.createStep, 'a clamped entry must not drop the wizard back into review').not.toBe('review')
    expect(window.location.pathname, 'the URL must agree with the collapsed view').toBe('/invoices')
  })

  // AC-2's fourth atom. The ctx.auditPrefilter assertion is the SOLE discriminator between
  // the top-of-handler clamp and a tail replaceState that enumerates the three ids: a tail
  // mutant cleans the URL but leaves this atom armed, because setAuditPrefilter has already
  // run against the live ?invoice=. Never trade it for the URL half.
  it('popstate_anOlderAuditEntryFromAnotherCompanyDoesNotResumeItsInvoiceFilter', async () => {
    await bootAtWithGateway('/')
    await act(async () => {
      capturedCtx!.openAuditForInvoice(AUDIT_INVOICE_ID, 'INV-2026-00001')
    })
    const stale = currentEntry()
    expect(stale.url, 'sanity: openAuditForInvoice must push /audit?invoice=<id>').toBe(
      `/audit?invoice=${AUDIT_INVOICE_ID}`,
    )

    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.switchClient(ENTITY_B)
    })
    expect(requireCtx().active.entityId, 'floor: the switch must really move the active company').toBe(ENTITY_B)

    await popToStamped(stale.url, stale.e)
    const ctx = requireCtx()
    expect(ctx.auditPrefilter, "Back must not re-arm entity A's invoice filter under entity B").toBeNull()
    expect(window.location.search, 'the clamped entry must drop the query, not just the state').toBe('')
    expect(window.location.pathname, 'audit carries through carryView -- only the filter is dropped').toBe('/audit')
  })
})

// CONTROL. Green BEFORE the fix and it must stay green after -- do not contort it into a
// red. It is what stops the clamp becoming a blanket disable (mutation 2 turns it red).
// Pre-fix it passes because nothing clamps at all; post-fix it passes because both sides
// name entity A. Its discriminating power arrives with the fix.
describe('ROUTE-06-02 AC-5 (control): an entry from the same company still restores its id', () => {
  it('popstate_anEntryFromTheSameCompanyStillRestoresItsId', async () => {
    await bootAtWithGateway('/')
    await act(async () => {
      capturedCtx!.openImportedInvoice(INVOICE_ID)
    })
    const entry = currentEntry()
    expect(entry.url, 'sanity: openImportedInvoice must push /invoices/<id>').toBe(`/invoices/${INVOICE_ID}`)

    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    // No switch: the company that minted the entry is still the active one.
    await popToStamped(entry.url, entry.e)
    const ctx = requireCtx()
    expect(ctx.importedInvoiceId, 'a same-company entry must still restore its selection').toBe(INVOICE_ID)
    expect(ctx.view, 'a same-company entry must still restore the detail view').toBe('detail')
    expect(invoiceDetailMounts, 'the detail screen must really render for its own company').toContain(INVOICE_ID)
    expect(window.location.pathname, 'a same-company entry must keep its own URL').toBe(`/invoices/${INVOICE_ID}`)
  })
})

// CONTROL. Green BEFORE the fix and it must stay green after. An entry that names no
// company is unknown, never stale: this is the shape all 35 popTo() call sites produce, so
// dropping the `?? null` fold (mutation 3) reddens this spec AND the 36-spec popTo
// population with it. The switch above the replay is load-bearing -- without it `here` is
// also null and the spec could not tell a fold from a crash.
describe('ROUTE-06-02 AC-6 (control): an entry with no stamp never clamps', () => {
  it('popstate_anUnstampedEntryDoesNotClamp', async () => {
    await bootAtWithGateway('/')
    await act(async () => {
      capturedCtx!.switchClient(ENTITY_B)
    })
    expect(requireCtx().active.entityId, 'floor: the active company must be known, or nothing is being folded').toBe(
      ENTITY_B,
    )

    // popTo writes a null state and fires a bare PopStateEvent -- an entry minted before
    // the stamp shipped.
    await popTo(`/invoices/${INVOICE_ID}`)
    const ctx = requireCtx()
    expect(ctx.importedInvoiceId, 'an unstamped entry names no company and must restore normally').toBe(INVOICE_ID)
    expect(ctx.view, 'an unstamped entry must still restore its own view').toBe('detail')
  })
})

describe('ROUTE-06-02 AC-4/AC-12: the clamp replaces and never pushes', () => {
  it('popstate_theClampReplacesAndNeverPushes', async () => {
    await bootAtWithGateway('/')
    await act(async () => {
      capturedCtx!.openImportedInvoice(INVOICE_ID)
    })
    const stale = currentEntry()
    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.switchClient(ENTITY_B)
    })

    // Move the URL the way Back would BEFORE installing the spies (:172's discipline) --
    // this harness's own move must not be counted as the handler's.
    const state = { e: stale.e }
    window.history.replaceState(state, '', stale.url)
    const pushSpy = vi.spyOn(window.history, 'pushState')
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    const lengthBefore = window.history.length

    await act(async () => {
      window.dispatchEvent(new PopStateEvent('popstate', { state }))
    })

    expect(pushSpy, 'the clamp must never push a history entry').not.toHaveBeenCalled()
    expect(window.history.length, 'a clamped restore must add no history entry').toBe(lengthBefore)
    // Floor before indexing: zero recorded writes means the clamp never fired, which must
    // fail loudly rather than pass a vacuous loop over an empty call list.
    expect(replaceSpy.mock.calls, 'the clamp must rewrite the restored entry exactly once').toHaveLength(1)
    expect(replaceSpy.mock.calls[0]![2], 'the clamp must rewrite to the collapsed path').toBe('/invoices')
  })
})

describe('ROUTE-06-02 AC-7: the boot entry is backfilled once the entities resolve', () => {
  it('boot_theStampBackfillFillsTheBootEntryOnceTheEntitiesResolve', async () => {
    // Spy installed before the render: the mount alignment runs before the fetch resolves
    // and stamps null, so only the backfill can write a stamp naming entity A.
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await bootAtWithGateway(`/invoices/${INVOICE_ID}`)

    await waitFor(() =>
      expect(currentEntry().e, 'the boot entry must carry the resolved company, not the null it minted with').toBe(
        ENTITY_A,
      ),
    )
    expect(window.location.pathname, 'the backfill must not move the entry it stamps').toBe(`/invoices/${INVOICE_ID}`)
    const backfills = replaceSpy.mock.calls.filter((c) => (c[0] as { e?: string | null } | null)?.e === ENTITY_A)
    expect(backfills, 'the backfill is gated on a null stamp: it fills once, it does not re-run').toHaveLength(1)
  })

  it('popstate_aColdBootDeepLinkEntryClampsAfterASwitch', async () => {
    await bootAtWithGateway(`/invoices/${INVOICE_ID}`)
    expect(requireCtx().importedInvoiceId, 'sanity: the deep link must seed the selection').toBe(INVOICE_ID)
    const stale = currentEntry()

    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.switchClient(ENTITY_B)
    })

    await popToStamped(stale.url, stale.e)
    const ctx = requireCtx()
    expect(
      ctx.importedInvoiceId,
      'an unbackfilled boot entry is permanently unclampable -- this is the entry a cold deep link lands on',
    ).toBeNull()
    expect(window.location.pathname, 'the URL must agree with the collapsed view').toBe('/invoices')
  })
})

// Pins a CONSEQUENCE of the stamp, drives no code of its own: browser history is one stack
// across identities in the same tab, so an entry buried by the previous session resurfaces
// under the next one. Same repro shape as App.routeBoot.test.tsx:797.
describe('ROUTE-06-02 AC-1, cross-session: a buried entry from the previous session does not resume its invoice', () => {
  it('signOut_aBuriedEntryFromThePreviousSessionDoesNotResumeItsInvoice', async () => {
    await bootAtWithGateway('/')
    await act(async () => {
      capturedCtx!.switchClient(ENTITY_B)
    })
    await act(async () => {
      capturedCtx!.openImportedInvoice(INVOICE_ID)
    })
    const stale = currentEntry()
    expect(stale.url, 'sanity: the firm session must push /invoices/<id> under entity B').toBe(
      `/invoices/${INVOICE_ID}`,
    )

    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.signOut()
    })
    expect(screen.getByText('Choose an account'), 'the in-app picker must render after sign-out').toBeTruthy()

    capturedCtx = undefined
    const inhouseButton = screen.getByText(APP_PERSONAS.inhouse.name).closest('button')
    expect(inhouseButton, 'the in-house persona button was not found in the picker').toBeTruthy()
    await act(async () => {
      fireEvent.click(inhouseButton as HTMLButtonElement)
    })
    await waitFor(() =>
      expect(requireCtx().active.entityId, 'the next session must resolve its own active company').toBe(ENTITY_A),
    )

    await popToStamped(stale.url, stale.e)
    const ctx = requireCtx()
    expect(ctx.importedInvoiceId, "a previous session's buried selection must not resume here").toBeNull()
    expect(window.location.pathname, 'the URL must agree with the collapsed view').toBe('/invoices')
  })
})

// QA adversarial (ROUTE-06-02). The `?? null` fold claims to collapse THREE no-stamp
// shapes; popstate_anUnstampedEntryDoesNotClamp pins only the first (a bare null state).
// Both specs below run on the resolved roster with `here` non-null, so the fold is the only
// thing standing between them and a clamp -- dropping `?? null` reddens all three.
describe('ROUTE-06-02 QA: the other two shapes the no-stamp fold collapses', () => {
  // A state object that carries no `e` at all -- what any OTHER writer's state looks like
  // to this handler.
  it('popstate_anEntryWhoseStateCarriesNoCompanyKeyDoesNotClamp', async () => {
    await bootAtWithGateway('/')
    await act(async () => {
      capturedCtx!.switchClient(ENTITY_B)
    })
    expect(requireCtx().active.entityId, 'floor: the active company must be known, or nothing is being folded').toBe(
      ENTITY_B,
    )

    const state = { scroll: 0 }
    window.history.replaceState(state, '', `/invoices/${INVOICE_ID}`)
    await act(async () => {
      window.dispatchEvent(new PopStateEvent('popstate', { state }))
    })
    const ctx = requireCtx()
    expect(ctx.importedInvoiceId, 'a state object with no `e` names no company and must restore normally').toBe(
      INVOICE_ID,
    )
    expect(ctx.view, 'it must still restore its own view').toBe('detail')
  })

  // `{ e: null }` -- the shape the mount alignment mints before the portfolio resolves, and
  // the one a cold boot really carries until the backfill fills it.
  it('popstate_anEntryStampedNullDoesNotClamp', async () => {
    await bootAtWithGateway('/')
    await act(async () => {
      capturedCtx!.switchClient(ENTITY_B)
    })
    expect(requireCtx().active.entityId, 'floor: the active company must be known').toBe(ENTITY_B)

    await popToStamped(`/invoices/${INVOICE_ID}`, null)
    const ctx = requireCtx()
    expect(ctx.importedInvoiceId, 'an explicitly null stamp names no company and must restore normally').toBe(
      INVOICE_ID,
    )
    expect(ctx.view, 'it must still restore its own view').toBe('detail')
  })
})
