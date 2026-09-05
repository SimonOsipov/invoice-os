// @vitest-environment jsdom
// vitest.config.ts stays `environment: 'node'` for every other suite.
//
// The popstate restore. Harness is App.routeNavigate.test.tsx's: the real <App/>, a
// session in a stubbed localStorage, ctx captured through a mocked Sidebar.

import { StrictMode } from 'react'
import { act, cleanup, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const JOB_A = 'c3d4e5f6-a7b8-4c3d-9e4f-5a6b7c8d9e0f'
const REVIEW_ID = 'a1b2c3d4-e5f6-47a8-89ab-cdef01234567'
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

beforeEach(() => {
  capturedCtx = undefined
  extractionReviewMounts.length = 0
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
    const urlBeforeDispatch = window.location.pathname + window.location.search + window.location.hash

    await act(async () => {
      window.dispatchEvent(new PopStateEvent('popstate'))
    })

    // AC-3's actual claim is "no duplicate history entry per Back press", not "zero
    // history-API calls" -- the pre-existing review-hash mirror (App.tsx:540-546) also
    // fires on this view change and calls replaceState, but only to rewrite the URL it
    // already is (a no-op). Assert the handler pushes nothing, adds no entry, and any
    // replaceState observed is that no-op rewrite -- a handler that wrote a *different*
    // URL, or pushed, still fails.
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
  it('popstate_backAfterACompanySwitchCannotReachTheCompanyJustLeft', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.openExtraction(JOB_A)
    })
    let ctx = requireCtx()
    expect(window.location.pathname, 'sanity: openExtraction must push /extraction/<jobId>').toBe(
      `/extraction/${JOB_A}`,
    )
    expect(ctx.extractionJobId, 'sanity: the job id must be set').toBe(JOB_A)

    await act(async () => {
      capturedCtx!.switchClient('other-entity-999')
    })
    ctx = requireCtx()
    expect(ctx.extractionJobId, 'sanity: switchClient (ROUTE-01-03) must already clear the job').toBeNull()

    // The spy is push-only and openExtraction already recorded one mount above -- reset so
    // the assertion below measures only the window after Back, not that earlier mount.
    extractionReviewMounts.length = 0

    await popTo('/extraction')
    ctx = requireCtx()
    expect(ctx.view, 'Back must restore the extraction view').toBe('extraction')
    expect(ctx.extractionJobId, 'the cleared job must not come back on a popstate restore').toBeNull()
    expect(
      extractionReviewMounts,
      'no ExtractionReview may render for a null job id -- App.tsx\'s view===extraction && extractionJobId!=null gate',
    ).toHaveLength(0)
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

describe('Adversarial: the three URL writers on create with a live review hash', () => {
  // Single continuous review session (the shape ROUTE-01-06 will pin): the mirror
  // (App.tsx:540-546) and the popstate handler never disagree here, because reviewBatchIds
  // and createStep are never reset in between -- only the view hops away and back.
  it('popstate_multiHopBackIntoALiveReviewHashComposesCorrectly', async () => {
    await bootAt(`/create#review/${REVIEW_ID}`)
    let ctx = requireCtx()
    expect(ctx.view, 'sanity: the review hash boots straight into create').toBe('create')
    expect(ctx.reviewBatchIds, 'sanity: the review hash must seed reviewBatchIds').toEqual([REVIEW_ID])

    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    expect(window.location.hash, 'nav away must clear the hash (the pre-existing mirror, out of scope here)').toBe('')

    await act(async () => {
      capturedCtx!.nav('audit')
    })

    await popTo('/invoices')
    ctx = requireCtx()
    expect(ctx.view, 'first Back must land on invoices').toBe('invoices')

    await popTo(`/create#review/${REVIEW_ID}`)
    ctx = requireCtx()
    // Writer order on this popstate: (1) the browser applies pathname+hash before the
    // event fires, (2) the popstate handler calls setView('create') only, (3) the
    // pre-existing review mirror re-runs because `view` changed and recomputes the hash
    // from LIVE createStep/reviewBatchIds -- both still 'review'/[REVIEW_ID] because
    // nothing in this chain ever reset them, so the mirror's rewrite is idempotent with
    // what the browser already restored. Final URL: /create#review/<id>, matching both
    // the entry and the live state.
    expect(ctx.view, 'second Back must land back on create').toBe('create')
    expect(window.location.pathname, 'the pathname must be /create after the composed writers settle').toBe('/create')
    expect(window.location.hash, 'the mirror must not have clobbered the hash with a different id').toBe(
      `#review/${REVIEW_ID}`,
    )
    expect(ctx.reviewBatchIds, 'reviewBatchIds must be exactly the one id throughout, never dropped or swapped').toEqual([
      REVIEW_ID,
    ])
  })

  // NOT covered here or by ROUTE-01-06's planned specs: if a SECOND, distinct review batch
  // is opened after the first (Finish -> openCreate -> a new import to review), closeCreate
  // (App.tsx:632-634) does not reset createStep/reviewBatchIds, so live state moves on to
  // the second batch while the FIRST batch's history entry still reads
  // /create#review/<firstId>. A popstate that changes `view` back to 'create' re-triggers
  // the mirror (App.tsx:540-546), which rewrites that entry's hash from the LIVE (second)
  // batch id -- overwriting the address bar the browser just restored with the wrong
  // batch. This is decision [create-step-not-restored] (App.tsx, `.ralph/ROUTE-01-final.md`)
  // extended from "shows the wrong step" to "shows and links the wrong batch"; recorded
  // here for ROUTE-01-06 / ROUTE-03, not reproduced as a spec because forcing a second
  // real batch id requires driving the full CreateFlow pipeline, out of proportion for a
  // popstate-listener suite.
})
