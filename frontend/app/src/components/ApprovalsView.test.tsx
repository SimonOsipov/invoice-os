// @vitest-environment jsdom
// RED specs (APPR-12-03, task-528, A03-2..A03-9, Mode A) -- pin the Approvals screen's
// wiring against the ApprovalsView.tsx stub (`return null`) before the executor fills it
// in. Every spec below fails today because the stub renders nothing -- that IS the
// correct red reason (assertion / not-found), never an import or compile error. Mirrors
// InvoicesList.test.tsx's own `mockFetchSequence`/`row()`/`listCtx()` idiom.

import { readFileSync } from 'node:fs'
import path from 'node:path'

import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APPROVALS_COPY } from '../lib/approvals'
import { createAuthedFetch } from '../lib/authedFetch'
import { fmt } from '../lib/format'
import type { InvoiceListResponse, InvoiceRecord } from '../lib/invoices'
import type { PlatformCtx } from '../types'
import { ApprovalsView } from './ApprovalsView'

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

// Queues one response per call, in order -- page 1 must differ from page 2 / a switch's
// refetch (InvoicesList.test.tsx:24-32 precedent).
function mockFetchSequence(responses: MockResponse[]) {
  const fetchMock = vi.fn()
  for (const r of responses) fetchMock.mockResolvedValueOnce(r)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function listResponse(invoices: InvoiceRecord[], pagination: { limit: number; offset: number; total: number }): MockResponse {
  const body: InvoiceListResponse = { invoices, pagination }
  return { ok: true, status: 200, json: () => Promise.resolve(body) }
}

function errorResponse(status: number, message: string): MockResponse {
  return { ok: false, status, json: () => Promise.resolve({ error: message }) }
}

function urlParams(url: string): URLSearchParams {
  return new URL(url).searchParams
}

// A row shaped for the approvals queue -- approvable by default, one open step.
function approvalRow(over: Partial<InvoiceRecord> = {}): InvoiceRecord {
  return {
    id: 'inv-x',
    entity_id: 'ent-1',
    import_batch_id: null,
    invoice_number: 'INV-X',
    status: 'validated',
    issue_date: '2026-07-01T00:00:00Z',
    supplier_tin: '00000000001',
    supplier_name: 'Acme Ltd',
    buyer_tin: '00000000002',
    buyer_name: 'Beta Ltd',
    currency: 'NGN',
    subtotal: '1000.00',
    vat: '75.00',
    total: '1075.00',
    violations: [],
    rule_set_version_id: null,
    created_at: '2026-07-01T00:00:00Z',
    irn: null,
    csid: null,
    qr_payload: null,
    rejection_reasons: [],
    kept_as_is_at: null,
    kept_as_is_by: null,
    kept_as_is_reason: null,
    failure_kind: null,
    rule_set_version: null,
    can_approve: true,
    approve_blocked_reason: null,
    can_submit: false,
    submit_blocked_reason: null,
    approval: {
      run_state: 'open',
      pending_ord: 0,
      pending_role_title: 'Reviewer',
      pending_holder_warn: false,
      due_at: null,
      overdue: false,
    },
    ...over,
  }
}

// ApprovalsView reads exactly ctx.authedFetch/mode/active.entityId/user.tenantName
// (Implementation Notes, CTX section) -- narrowing idiom matches InvoicesList.test.tsx's
// listCtx(). `entityId: undefined` (default) leaves it unset so in-house callers can omit
// it without a stray `null` in the fixture.
function approvalsCtx(over: { mode?: 'firm' | 'inhouse'; entityId?: string | null } = {}): PlatformCtx {
  const ctx = {
    mode: over.mode ?? 'firm',
    active: { entityId: over.entityId === undefined ? 'ent-1' : over.entityId },
    user: { tenantName: 'Acme Co' },
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
  }
  return ctx as unknown as PlatformCtx
}

// A generic POST /approvals response -- the run/decision shape approvals.test.ts's own
// okResponse() uses, byte-identical.
function approveOkResponse(): MockResponse {
  return { ok: true, status: 200, json: () => Promise.resolve({ run_id: 'r1', state: 'open', steps: [], decisions: [] }) }
}

// Differentiates GET list calls from POST .../approvals calls -- the two request shapes
// this screen makes once bulk-approve exists. `listSteps` is consumed in call order, one
// entry per GET (an index past the array's end repeats the LAST entry, so an unplanned
// extra GET does not throw); an entry of 'PENDING' holds that GET unresolved until the
// test calls `resolveList` for that call's index. `approvalImpl` decides each POST's
// outcome per invoice id, defaulting to a uniform 200.
function mockBulkFetch(
  listSteps: (MockResponse | 'PENDING')[],
  approvalImpl: (id: string) => MockResponse | Promise<MockResponse> = () => approveOkResponse(),
) {
  let listCallIndex = 0
  const listResolvers = new Map<number, (r: MockResponse) => void>()
  const fetchMock = vi.fn((url: string, _init?: RequestInit) => {
    if (/\/approvals$/.test(url)) {
      const m = /\/invoices\/([^/]+)\/approvals$/.exec(url)
      return Promise.resolve(approvalImpl(m ? m[1] : ''))
    }
    const idx = listCallIndex++
    const step = listSteps[Math.min(idx, listSteps.length - 1)]
    if (step === 'PENDING') {
      return new Promise<MockResponse>((resolve) => listResolvers.set(idx, resolve))
    }
    return Promise.resolve(step)
  })
  vi.stubGlobal('fetch', fetchMock)
  return {
    fetchMock,
    resolveList: (idx: number, r: MockResponse) => {
      const resolve = listResolvers.get(idx)
      if (!resolve) throw new Error(`no pending list call at index ${idx}`)
      resolve(r)
    },
    approvalCalls: () => (fetchMock.mock.calls as [string, RequestInit][]).filter(([u]) => /\/approvals$/.test(u)),
  }
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('A03-2: the queue renders what the fetch returned (rows map to wire fields)', () => {
  it('renders invoice number, buyer, amount, the 1-based pending step, the role, and the overdue flag', async () => {
    const total = '154230.00'
    const rows = [
      approvalRow({
        id: 'inv-1',
        invoice_number: 'INV-2001',
        buyer_name: 'Beta Traders Ltd',
        total,
        approval: { run_state: 'open', pending_ord: 2, pending_role_title: 'Finance Lead', pending_holder_warn: false, due_at: '2020-01-01T00:00:00Z', overdue: true },
      }),
    ]
    mockFetchSequence([listResponse(rows, { limit: 50, offset: 0, total: 1 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)

    // Loading rung (AC #7) -- the shared Loading surface before the fetch settles.
    expect(document.querySelector('.apic-loading-spin'), 'the shared Loading surface must render before the fetch settles').toBeTruthy()

    expect(await screen.findByText('INV-2001')).toBeTruthy()
    expect(screen.getByText('Beta Traders Ltd')).toBeTruthy()
    expect(screen.getByText(fmt(Number(total)))).toBeTruthy()
    expect(screen.getByText('Finance Lead')).toBeTruthy()
    expect(screen.getByText('Step 3'), 'pending_ord 2 must render 1-based (G4)').toBeTruthy()
    expect(screen.getByTestId('approval-overdue'), 'the overdue flag must render off the wire\'s own overdue field').toBeTruthy()
  })
})

describe('A03-3: empty vs mid-set-empty are different rungs', () => {
  it('empty is defined by pagination.total === 0, never invoices.length', async () => {
    mockFetchSequence([listResponse([], { limit: 50, offset: 0, total: 0 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)

    const empty = await screen.findByTestId('approvals-empty')
    expect(empty).toBeTruthy()
    expect(screen.queryByTestId('approvals-empty-page')).toBeNull()
    expect(screen.queryByTestId('approvals-pager')).toBeNull()
  })

  it('a mid-set empty page (total>0, this page []) still renders the Pager', async () => {
    const page1 = Array.from({ length: 50 }, (_, i) => approvalRow({ id: `inv-${i}`, invoice_number: `INV-${i}` }))
    mockFetchSequence([
      listResponse(page1, { limit: 50, offset: 0, total: 60 }),
      listResponse([], { limit: 50, offset: 50, total: 60 }),
    ])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByTestId('approvals-pager')

    fireEvent.click(screen.getByRole('button', { name: 'Next →' }))

    const pageEmpty = await screen.findByTestId('approvals-empty-page')
    expect(pageEmpty).toBeTruthy()
    expect(screen.queryByTestId('approvals-empty'), 'a mid-set empty page must never render the zero-total empty state').toBeNull()
    expect(screen.getByTestId('approvals-pager'), 'the pager must stay mounted so the user can page back').toBeTruthy()
  })
})

describe('A03-4: the query sends awaiting_approval=true plus entity_id', () => {
  it('firm mode: both params are sent', async () => {
    const fetchMock = mockFetchSequence([listResponse([], { limit: 50, offset: 0, total: 0 })])

    render(<ApprovalsView ctx={approvalsCtx({ mode: 'firm', entityId: 'ent-7' })} />)

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    const [url] = fetchMock.mock.calls[0] as [string]
    expect(urlParams(url).get('awaiting_approval')).toBe('true')
    expect(urlParams(url).get('entity_id')).toBe('ent-7')
  })

  it('in-house mode: entity_id is omitted, awaiting_approval=true still sent', async () => {
    const fetchMock = mockFetchSequence([listResponse([], { limit: 50, offset: 0, total: 0 })])

    render(<ApprovalsView ctx={approvalsCtx({ mode: 'inhouse', entityId: null })} />)

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    const [url] = fetchMock.mock.calls[0] as [string]
    expect(urlParams(url).get('awaiting_approval')).toBe('true')
    expect(urlParams(url).get('entity_id')).toBeNull()
  })
})

describe('A03-5: the Pager reads the response\'s own limit, never a client constant', () => {
  it('SHOWING/PAGE labels are computed off the echoed pagination, not a hardcoded page size', async () => {
    // limit=7 is deliberately not a plausible client page-size constant (50) -- a
    // hardcoded APPROVALS_PAGE_SIZE would render the wrong labels here.
    const rows = [approvalRow({ id: 'inv-1', invoice_number: 'INV-1' })]
    mockFetchSequence([listResponse(rows, { limit: 7, offset: 0, total: 22 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)

    const pager = await screen.findByTestId('approvals-pager')
    expect(pager.textContent).toContain('SHOWING 1–7 OF 22')
    expect(pager.textContent).toContain('PAGE 1 / 4')
  })
})

describe('A03-6: the unstaffed seat is surfaced (pending_holder_warn)', () => {
  it('a warned row shows the marker, a fully-staffed row does not', async () => {
    const warned = approvalRow({
      id: 'inv-warn',
      invoice_number: 'INV-WARN',
      approval: { run_state: 'open', pending_ord: 0, pending_role_title: 'Compliance Officer', pending_holder_warn: true, due_at: null, overdue: false },
    })
    const clear = approvalRow({
      id: 'inv-clear',
      invoice_number: 'INV-CLEAR',
      approval: { run_state: 'open', pending_ord: 0, pending_role_title: 'Compliance Officer', pending_holder_warn: false, due_at: null, overdue: false },
    })
    mockFetchSequence([listResponse([warned, clear], { limit: 50, offset: 0, total: 2 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-WARN')

    const warnedRow = screen.getByText('INV-WARN').closest('[data-testid="approval-row"]')
    const clearRow = screen.getByText('INV-CLEAR').closest('[data-testid="approval-row"]')
    expect(warnedRow, 'the row wrapper testid is missing').not.toBeNull()
    expect(clearRow, 'the row wrapper testid is missing').not.toBeNull()
    expect(warnedRow!.querySelector('[data-testid="approval-unstaffed-warning"]'), 'the unstaffed-seat warning must render on the flagged row').not.toBeNull()
    expect(clearRow!.querySelector('[data-testid="approval-unstaffed-warning"]'), 'a fully-staffed row must not show the warning').toBeNull()
  })
})

describe('A03-7: the error rung retries', () => {
  it('a failed fetch renders ErrorState, and Retry re-issues the same fetch', async () => {
    const fetchMock = mockFetchSequence([errorResponse(503, 'gateway is down'), listResponse([], { limit: 50, offset: 0, total: 0 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)

    expect(await screen.findByText('gateway is down')).toBeTruthy()
    expect(screen.getByText('Something went wrong')).toBeTruthy()
    expect(fetchMock).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByText('Retry'))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
  })
})

// A03-8 (NEW, Plan gap G1). gateByActiveEntity's own doc comment
// (lib/invoices.ts:1293-1306) documents the one-commit passive-effect window this fixes;
// jsdom's act() flushes straight through that single frame (same limitation
// InvoicesList.test.tsx:312-333 already documents), so it cannot be observed directly
// here. Instead this proves the STRUCTURAL invariant the fix actually applies on every
// render: a row whose entity_id doesn't match ctx.active.entityId is filtered, whether it
// arrives via the transient frame or (as simulated here) a settled response that still
// carries a stale-entity row -- gateByActiveEntity's own filter can't distinguish the two,
// so exercising it this way is an equally valid, and more deterministic, oracle for the
// same line of code.
describe('A03-8: an entity switch never renders a row from the PREVIOUS entity', () => {
  it('firm mode: a row tagged with the OLD entity_id never renders, even inside a settled post-switch response', async () => {
    const entA = [approvalRow({ id: 'a1', invoice_number: 'INV-ENT-A', entity_id: 'ent-1' })]
    // The second fetch's payload still carries the stale entity-A row mixed in with the
    // genuine entity-B row -- exactly the shape a server-side scoping regression or the
    // pre-refetch commit frame would produce.
    const staleMixedIn = [
      approvalRow({ id: 'a1', invoice_number: 'INV-ENT-A', entity_id: 'ent-1' }),
      approvalRow({ id: 'b1', invoice_number: 'INV-ENT-B', entity_id: 'ent-2' }),
    ]
    const fetchMock = mockFetchSequence([
      listResponse(entA, { limit: 50, offset: 0, total: 1 }),
      listResponse(staleMixedIn, { limit: 50, offset: 0, total: 2 }),
    ])

    const { rerender } = render(<ApprovalsView ctx={approvalsCtx({ entityId: 'ent-1' })} />)
    await screen.findByText('INV-ENT-A')

    rerender(<ApprovalsView ctx={approvalsCtx({ entityId: 'ent-2' })} />)

    await screen.findByText('INV-ENT-B')
    expect(screen.queryByText('INV-ENT-A'), 'a row tagged with the OLD entity must never render, even inside a settled response for the new entity').toBeNull()
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('in-house mode is exempt from entity gating and still renders its rows normally', async () => {
    const rows = [approvalRow({ id: 'x1', invoice_number: 'INV-INHOUSE', entity_id: 'whole-tenant' })]
    mockFetchSequence([listResponse(rows, { limit: 50, offset: 0, total: 1 })])

    render(<ApprovalsView ctx={approvalsCtx({ mode: 'inhouse', entityId: null })} />)

    // Proves the fix isn't "hide every row" -- gateByActiveEntity's isInhouse branch
    // returns rows unfiltered.
    expect(await screen.findByText('INV-INHOUSE')).toBeTruthy()
  })
})

// A03-9 (NEW, Plan gap G3). App.tsx cannot be rendered in jsdom (Workspace needs a live
// session + entities fetch), and every A03-2..A03-8 spec above renders ApprovalsView
// DIRECTLY, so none of them touch App.tsx's own mount -- a forgotten render branch would
// paint a blank screen with no tsc error and nothing else catches it until the e2e layer,
// two subtasks later. Source-scan, matching policiesWiring.test.ts's App.tsx-scan idiom
// (`APP.includes(...)`, boolean assertions so a failure doesn't dump 40KB of source).
// process.cwd(), not fileURLToPath(import.meta.url) -- this file is
// @vitest-environment jsdom (see the A03-1 comment in Header.test.tsx for why).
function readSrc(relPath: string): string {
  return readFileSync(path.join(process.cwd(), relPath), 'utf8')
}

describe('A03-9: App.tsx mounts ApprovalsView on view === "approvals"', () => {
  it('non-vacuity control: the scan can tell a present render branch from an absent one', () => {
    const appSrc = readSrc('src/App.tsx')

    expect(appSrc.length, 'the scan must actually read App.tsx').toBeGreaterThan(40_000)
    expect(appSrc.includes(`{view === 'invoices' && <InvoicesList ctx={ctx} />}`), 'known-true anchor: a sibling branch that already exists').toBe(true)
    expect(appSrc.includes(`{view === 'definitely-not-a-real-view' && <Nope ctx={ctx} />}`), 'known-false anchor: a fabricated branch must not appear').toBe(false)
  })

  it('renders ApprovalsView beside its eleven siblings', () => {
    const appSrc = readSrc('src/App.tsx')

    expect(appSrc.includes(`{view === 'approvals' && <ApprovalsView ctx={ctx} />}`), 'App.tsx has no approvals render branch -- the screen is unreachable even once nav lands').toBe(true)
  })
})

// QA adversarial (task-528 review, G4's own text): store.go:691-694's queue predicate is
// `NOT EXISTS(approved run)`, vacuously true for an invoice validated before any policy
// was published -- `approval` is null on that row, not merely `pending_ord`. G4's lib
// specs (approvals.test.ts) pin approvalRowView's null handling directly; this proves the
// FULL render path (component + lib together) survives the same row without crashing and
// without inventing a step/role/due value for it.
describe('adversarial: a row with no run at all (approval: null)', () => {
  it('renders the em dash for step, role and due -- no warning, no overdue marker, no crash', async () => {
    const noRun = approvalRow({ id: 'inv-norun', invoice_number: 'INV-NORUN', approval: null })
    mockFetchSequence([listResponse([noRun], { limit: 50, offset: 0, total: 1 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-NORUN')

    const row = screen.getByText('INV-NORUN').closest('[data-testid="approval-row"]')
    expect(row, 'the row wrapper must still render').not.toBeNull()
    // Re-indexed by one for APPR-12-04's leading 24px checkbox track (G-04-I): cells[0]
    // is now the select checkbox, so step/role/due sit at 4/5/6. Left at 3/4/5 the step
    // assertion reads the AMOUNT and fails, while role/due would pass by coincidence
    // (both em dashes on this row).
    const cells = row!.children
    expect(cells[4]?.textContent, 'step column must fall back to the em dash, never "Step null"').toBe('—')
    expect(cells[5]?.textContent, 'role column must fall back to the em dash').toBe('—')
    expect(cells[6]?.textContent, 'due column must fall back to the em dash').toBe('—')
    expect(row!.querySelector('[data-testid="approval-unstaffed-warning"]'), 'no warning without a pending holder to warn about').toBeNull()
    expect(row!.querySelector('[data-testid="approval-overdue"]'), 'no overdue marker without a run to be overdue on').toBeNull()
  })
})

// --- task-529 (APPR-12-04, Mode A) -- RED specs for bulk approve: arm/confirm/cancel,
// the double-click guard, per-item results, the refetch-survival + badge-source rules,
// the vacuous-every guard, the four-layer disabled reason, and the prune effect settling.
// None of this DOM exists yet (ApprovalsView.tsx has no checkbox, no bar, no results
// panel), so every spec below fails on a missing element / testing-library "unable to
// find" error -- the correct red reason for Mode A, never an import/compile error.
// Selectors below (data-testid="approval-select-all" etc.) are this spec's own contract:
// Mode B's executor implements against them. --- G-04-B/G-04-H are source scans placed at
// the end of this file, mirroring A03-9's by-path idiom above (readSrc, process.cwd()).

describe('A04-1: arming sends nothing', () => {
  it('selecting rows and clicking the arm button issues no POST -- only confirm can', async () => {
    const rows = [approvalRow({ id: 'inv-a', invoice_number: 'INV-A' }), approvalRow({ id: 'inv-b', invoice_number: 'INV-B' })]
    const { fetchMock, approvalCalls } = mockBulkFetch([listResponse(rows, { limit: 50, offset: 0, total: 2 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByTestId('approval-select-all'))
    fireEvent.click(screen.getByTestId('approvals-bulk-submit')) // arm

    expect(screen.getByTestId('approvals-bulk-confirm'), 'arming must show the confirm stage').toBeTruthy()
    expect(approvalCalls(), 'arming alone must send zero approve requests').toHaveLength(0)
    expect(fetchMock).toHaveBeenCalledTimes(1) // only the initial GET
  })
})

describe('A04-2: confirm sends the eligible set, and ONLY the eligible set', () => {
  it('a mixed page sends POSTs for the approvable ids alone, never the blocked one', async () => {
    const a = approvalRow({ id: 'inv-a', invoice_number: 'INV-A', can_approve: true })
    const b = approvalRow({ id: 'inv-b', invoice_number: 'INV-B', can_approve: true })
    const blocked = approvalRow({
      id: 'inv-blocked',
      invoice_number: 'INV-BLOCKED',
      can_approve: false,
      approve_blocked_reason: 'Only a validated invoice can be approved or rejected.',
      approval: null,
    })
    const { approvalCalls } = mockBulkFetch([
      listResponse([a, b, blocked], { limit: 50, offset: 0, total: 3 }),
      listResponse([a, b, blocked], { limit: 50, offset: 0, total: 3 }), // the post-confirm refetch
    ])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByTestId('approval-select-all')) // select-all only picks approvable rows
    fireEvent.click(screen.getByTestId('approvals-bulk-submit'))
    fireEvent.click(screen.getByTestId('approvals-bulk-confirm'))

    await waitFor(() => expect(approvalCalls()).toHaveLength(2))
    const ids = approvalCalls()
      .map(([url]) => /\/invoices\/([^/]+)\/approvals$/.exec(url))
      .map((m) => (m ? m[1] : null))
    expect(ids.sort()).toEqual(['inv-a', 'inv-b'])
  })
})

describe('A04-3: a double-click on confirm sends exactly one fan-out (the ref wins the race disabled loses)', () => {
  it('two synchronous clicks on confirm never produce two fan-outs', async () => {
    const a = approvalRow({ id: 'inv-a', invoice_number: 'INV-A' })
    const b = approvalRow({ id: 'inv-b', invoice_number: 'INV-B' })
    const { approvalCalls } = mockBulkFetch([
      listResponse([a, b], { limit: 50, offset: 0, total: 2 }),
      listResponse([a, b], { limit: 50, offset: 0, total: 2 }),
    ])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByTestId('approval-select-all'))
    fireEvent.click(screen.getByTestId('approvals-bulk-submit'))
    const confirmBtn = screen.getByTestId('approvals-bulk-confirm')
    fireEvent.click(confirmBtn) // confirm #1
    fireEvent.click(confirmBtn) // confirm #2, SAME tick -- `disabled` has not re-rendered yet

    await screen.findByTestId('approvals-results')
    expect(approvalCalls(), 'a double-click must fan out over the 2 eligible ids exactly once, not twice').toHaveLength(2)
  })
})

describe('A04-4: cancel semantics', () => {
  it('A04-4a: cancel from armed returns to idle', async () => {
    const a = approvalRow({ id: 'inv-a', invoice_number: 'INV-A' })
    mockBulkFetch([listResponse([a], { limit: 50, offset: 0, total: 1 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByTestId('approval-select-all'))
    fireEvent.click(screen.getByTestId('approvals-bulk-submit'))
    expect(screen.getByTestId('approvals-bulk-confirm')).toBeTruthy()

    fireEvent.click(screen.getByTestId('approvals-bulk-cancel'))

    expect(screen.queryByTestId('approvals-bulk-confirm'), 'cancel from armed must return to idle').toBeNull()
    expect(screen.getByTestId('approvals-bulk-submit'), 'idle shows the arm button again').toBeTruthy()
  })

  it('A04-4b: cancel while submitting does nothing -- the in-flight request is not cancellable', async () => {
    const a = approvalRow({ id: 'inv-a', invoice_number: 'INV-A' })
    const resolvers: Array<(r: MockResponse) => void> = []
    const fetchMock = vi.fn((url: string) => {
      if (/\/approvals$/.test(url)) return new Promise<MockResponse>((resolve) => resolvers.push(resolve))
      return Promise.resolve(listResponse([a], { limit: 50, offset: 0, total: 1 }))
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByTestId('approval-select-all'))
    fireEvent.click(screen.getByTestId('approvals-bulk-submit'))
    fireEvent.click(screen.getByTestId('approvals-bulk-confirm')) // now submitting, POST held pending

    const cancelBtn = screen.getByTestId('approvals-bulk-cancel') as HTMLButtonElement
    expect(cancelBtn.disabled, 'cancel must carry the real disabled attribute while submitting').toBe(true)

    fireEvent.click(cancelBtn)

    expect(screen.getByTestId('approvals-bulk-confirm'), 'a no-op cancel must not un-arm a request already in flight').toBeTruthy()

    resolvers[0](approveOkResponse())
    await screen.findByTestId('approvals-results')
  })
})

describe('A04-5: per-item truth -- one result row per selected invoice, each showing its own outcome', () => {
  it('a mixed pass/fail confirm renders one row per selected id with its own label and message', async () => {
    const a = approvalRow({ id: 'inv-a', invoice_number: 'INV-A' })
    const b = approvalRow({ id: 'inv-b', invoice_number: 'INV-B' })
    const failMsg = 'this approval run is already closed'
    const { approvalCalls } = mockBulkFetch(
      [listResponse([a, b], { limit: 50, offset: 0, total: 2 }), listResponse([a, b], { limit: 50, offset: 0, total: 2 })],
      (id) => (id === 'inv-b' ? { ok: false, status: 409, json: () => Promise.resolve({ error: failMsg }) } : approveOkResponse()),
    )

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByTestId('approval-select-all'))
    fireEvent.click(screen.getByTestId('approvals-bulk-submit'))
    fireEvent.click(screen.getByTestId('approvals-bulk-confirm'))

    await waitFor(() => expect(approvalCalls()).toHaveLength(2))
    const panel = await screen.findByTestId('approvals-results')

    expect(within(panel).getByText('INV-A')).toBeTruthy()
    expect(within(panel).getByText('INV-B')).toBeTruthy()
    expect(within(panel).getByText(failMsg), "the failed row's own message must ride through byte-identically").toBeTruthy()

    const rowA = within(panel).getByText('INV-A').closest('[data-testid="approval-result-row"]')
    const rowB = within(panel).getByText('INV-B').closest('[data-testid="approval-result-row"]')
    expect(rowA, 'each result needs its own row wrapper').not.toBeNull()
    expect(rowB).not.toBeNull()
    expect(within(rowA as HTMLElement).queryByText(failMsg), "the OK row must not carry the failed row's message").toBeNull()
  })
})

describe('A04-6: the results panel survives the refetch (G-04-D)', () => {
  it('the panel stays mounted while list.run() is in flight (data:null), not nested under the ready rung', async () => {
    const a = approvalRow({ id: 'inv-a', invoice_number: 'INV-A' })
    const { fetchMock, approvalCalls, resolveList } = mockBulkFetch([listResponse([a], { limit: 50, offset: 0, total: 1 }), 'PENDING'])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByTestId('approval-select-all'))
    fireEvent.click(screen.getByTestId('approvals-bulk-submit'))
    fireEvent.click(screen.getByTestId('approvals-bulk-confirm'))

    await waitFor(() => expect(approvalCalls()).toHaveLength(1))
    const panel = await screen.findByTestId('approvals-results')

    // The refetch is now in flight (list.run() dispatched 'start', data:null) -- the
    // panel must still be in the DOM, proving it is a SIBLING of the `state === 'ready'`
    // rung rather than nested inside it (G-04-D).
    expect(screen.getByTestId('approvals-results'), 'the results panel must survive the refetch, not unmount with the stale ready subtree').toBe(panel)

    resolveList(1, listResponse([], { limit: 50, offset: 0, total: 0 }))
    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThanOrEqual(3))
    expect(screen.getByTestId('approvals-results'), 'the panel must still be present once the refetch settles').toBeTruthy()
  })
})

describe('A04-7: row badges after settle come from the refetch only', () => {
  it("the refetch's own fields render, never anything derived from the approve response", async () => {
    const before = approvalRow({
      id: 'inv-a',
      invoice_number: 'INV-A',
      approval: { run_state: 'open', pending_ord: 0, pending_role_title: 'Reviewer', pending_holder_warn: false, due_at: null, overdue: false },
    })
    const after = approvalRow({
      id: 'inv-a',
      invoice_number: 'INV-A',
      approval: { run_state: 'open', pending_ord: 1, pending_role_title: 'Finance Lead', pending_holder_warn: false, due_at: null, overdue: false },
    })
    const { approvalCalls } = mockBulkFetch([listResponse([before], { limit: 50, offset: 0, total: 1 }), listResponse([after], { limit: 50, offset: 0, total: 1 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('Step 1')

    fireEvent.click(screen.getByTestId('approval-select-all'))
    fireEvent.click(screen.getByTestId('approvals-bulk-submit'))
    fireEvent.click(screen.getByTestId('approvals-bulk-confirm'))

    await waitFor(() => expect(approvalCalls()).toHaveLength(1))
    await screen.findByText('Finance Lead') // the refetch's own field

    expect(screen.getByText('Step 2'), "stepLabel must come from the refetch's pending_ord (1 -> Step 2), not the stale pre-approve value").toBeTruthy()
    expect(screen.queryByText('Step 1'), 'the pre-approve step must not linger').toBeNull()
    expect(screen.queryByText('Reviewer'), 'the pre-approve role must not linger').toBeNull()
  })
})

describe('A04-8: select-all never reports "all" on a page with zero approvable rows (vacuous-every guard, component wiring)', () => {
  it('a page where every row is blocked renders select-all unchecked and non-indeterminate', async () => {
    const blocked1 = approvalRow({
      id: 'inv-1',
      invoice_number: 'INV-1',
      can_approve: false,
      approve_blocked_reason: 'Only a validated invoice can be approved or rejected.',
      approval: null,
    })
    const blocked2 = approvalRow({
      id: 'inv-2',
      invoice_number: 'INV-2',
      can_approve: false,
      approve_blocked_reason: 'Only a validated invoice can be approved or rejected.',
      approval: null,
    })
    mockBulkFetch([listResponse([blocked1, blocked2], { limit: 50, offset: 0, total: 2 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-1')

    const selectAll = screen.getByTestId('approval-select-all') as HTMLInputElement
    expect(selectAll.checked, 'zero approvable rows must never read as "all" selected').toBe(false)
    expect(selectAll.indeterminate, 'zero approvable rows is "none", not "some"').toBe(false)
  })
})

describe("A04-9: a disabled row states the SERVER's own why through its reason icon (G-04-C)", () => {
  it('the real disabled attribute, the checkbox title, and the icon/checkbox aria-describedby resolving to the tooltip', async () => {
    const reason = 'Only a validated invoice can be approved or rejected.'
    const blocked = approvalRow({ id: 'inv-blocked', invoice_number: 'INV-BLOCKED', can_approve: false, approve_blocked_reason: reason, approval: null })
    mockBulkFetch([listResponse([blocked], { limit: 50, offset: 0, total: 1 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-BLOCKED')

    const row = screen.getByText('INV-BLOCKED').closest('[data-testid="approval-row"]') as HTMLElement
    const checkbox = within(row).getByTestId('approval-select-row') as HTMLInputElement

    // Layer 1: the real disabled attribute -- a keyboard user cannot reach it.
    expect(checkbox.disabled).toBe(true)
    checkbox.focus()
    expect(document.activeElement, 'a disabled control must be genuinely out of the tab order').not.toBe(checkbox)

    // Layer 2: the checkbox keeps its own title, byte-identical to the reason.
    expect(checkbox.title, "the checkbox's title must still carry the server's sentence").toBe(reason)

    // Layer 4: the icon and the checkbox both describe the SAME tooltip node, by a
    // per-row unique id -- the icon's accessible name is the static copy key.
    const icon = within(row).getByTestId('approval-blocked-icon')
    const iconLabel = (APPROVALS_COPY as unknown as Record<string, string | undefined>).blockedReasonLabel
    expect(iconLabel, 'APPROVALS_COPY.blockedReasonLabel is not exported yet').toBeDefined()
    expect(icon.getAttribute('aria-label')).toBe(iconLabel)

    const describedbyId = icon.getAttribute('aria-describedby')
    expect(describedbyId).not.toBeNull()
    expect(describedbyId).toBe('approve-blocked-reason-inv-blocked')
    expect(checkbox.getAttribute('aria-describedby'), 'the checkbox must point at the same tooltip id as the icon').toBe(describedbyId)

    const tip = document.getElementById(describedbyId as string)
    expect(tip?.getAttribute('role')).toBe('tooltip')
    expect(tip?.textContent, 'aria-describedby must resolve to the exact reason text').toBe(reason)
  })

  it('two blocked rows on the same page get distinct per-row reason ids, each pointing at its own text', async () => {
    const reasonA = 'Only a validated invoice can be approved or rejected.'
    const reasonB = "Only an approver staffed to this step's workflow role can approve or reject it — ask whoever holds that role."
    const a = approvalRow({ id: 'inv-a', invoice_number: 'INV-A', can_approve: false, approve_blocked_reason: reasonA, approval: null })
    const b = approvalRow({ id: 'inv-b', invoice_number: 'INV-B', can_approve: false, approve_blocked_reason: reasonB, approval: null })
    mockBulkFetch([listResponse([a, b], { limit: 50, offset: 0, total: 2 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-A')

    const icons = screen.getAllByTestId('approval-blocked-icon')
    expect(icons).toHaveLength(2)
    const iconIds = icons.map((i) => i.getAttribute('aria-describedby'))
    expect(iconIds[0]).not.toBeNull()
    expect(iconIds[1]).not.toBeNull()
    expect(iconIds[0]).not.toBe(iconIds[1])
    expect(document.getElementById(iconIds[0] as string)?.textContent).toBe(reasonA)
    expect(document.getElementById(iconIds[1] as string)?.textContent).toBe(reasonB)

    // AC-5: the checkbox keeps naming the same per-row id as its own icon.
    const checkboxes = screen.getAllByTestId('approval-select-row') as HTMLInputElement[]
    const checkboxIds = checkboxes.map((c) => c.getAttribute('aria-describedby'))
    expect(checkboxIds).toEqual(iconIds)
  })
})

describe('B17-C1: a blocked row prints no sentence line and stays one grid line', () => {
  it('renders no approval-blocked-reason node, and the row keeps the same child count as the head', async () => {
    const reason = 'Only a validated invoice can be approved or rejected.'
    const blocked = approvalRow({ id: 'inv-blocked', invoice_number: 'INV-BLOCKED', can_approve: false, approve_blocked_reason: reason, approval: null })
    mockBulkFetch([listResponse([blocked], { limit: 50, offset: 0, total: 1 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-BLOCKED')

    expect(screen.queryByTestId('approval-blocked-reason'), 'the sentence line must be gone').toBeNull()

    const head = document.querySelector('.pf-list-head')
    const row = screen.getByTestId('approval-row')
    expect(head, 'lost anchor on the list head').not.toBeNull()
    expect(row.children.length, 'a blocked row must sit on one grid line, at the head width').toBe(head!.children.length)
  })
})

describe('B17-C2: the icon follows the invoice number on a blocked row only', () => {
  it('the blocked row cell holds the number then the icon wrapper; the approvable row has neither icon nor tip', async () => {
    const ok = approvalRow({ id: 'inv-ok', invoice_number: 'INV-OK', can_approve: true })
    const blocked = approvalRow({ id: 'inv-blocked', invoice_number: 'INV-BLOCKED', can_approve: false, approve_blocked_reason: 'Only a validated invoice can be approved or rejected.', approval: null })
    mockBulkFetch([listResponse([ok, blocked], { limit: 50, offset: 0, total: 2 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-OK')

    const blockedRow = screen.getByText('INV-BLOCKED').closest('[data-testid="approval-row"]') as HTMLElement
    const blockedCell = blockedRow.children[1] as HTMLElement
    const icon = within(blockedCell).getByTestId('approval-blocked-icon')

    expect(blockedCell.children.length, 'the invoice # cell must hold exactly the number span and the icon wrapper').toBe(2)
    const numberSpan = blockedCell.children[0] as HTMLElement
    expect(numberSpan.textContent).toBe('INV-BLOCKED')
    expect(numberSpan.getAttribute('style'), 'the number span must truncate with an ellipsis').toContain('text-overflow: ellipsis')
    expect(blockedCell.children[1].contains(icon), 'the icon must sit inside the wrapper that follows the number').toBe(true)

    const okRow = screen.getByText('INV-OK').closest('[data-testid="approval-row"]') as HTMLElement
    expect(okRow.querySelector('[data-testid="approval-blocked-icon"]'), 'an approvable row must show no icon').toBeNull()
    const okNumberSpan = (okRow.children[1] as HTMLElement).children[0] as HTMLElement
    expect(okNumberSpan.getAttribute('style'), 'every row truncates its number, not only blocked ones').toContain('text-overflow: ellipsis')
  })
})

describe('B17-C2b: the reason icon authors no copy of its own (AC-8)', () => {
  it("the icon carries no text and the tip's text is exactly the reason", async () => {
    const reason = 'Only a validated invoice can be approved or rejected.'
    const blocked = approvalRow({ id: 'inv-blocked', invoice_number: 'INV-BLOCKED', can_approve: false, approve_blocked_reason: reason, approval: null })
    mockBulkFetch([listResponse([blocked], { limit: 50, offset: 0, total: 1 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-BLOCKED')

    const icon = screen.getByTestId('approval-blocked-icon')
    expect(icon.textContent, 'the icon renders a glyph, never its own text').toBe('')
    const tip = screen.getByTestId('approval-blocked-tip')
    expect(tip.textContent).toBe(reason)
    // T5: BlockedReason as a whole authors no text beyond the reason.
    expect(icon.parentElement!.textContent, 'BlockedReason must render no text of its own outside the reason').toBe(reason)
  })
})

describe('B17-C3: hover reveals the server sentence byte-identically', () => {
  it('mouseEnter shows the tip; mouseLeave hides it again', async () => {
    const reason = 'Only a validated invoice can be approved or rejected.'
    const blocked = approvalRow({ id: 'inv-blocked', invoice_number: 'INV-BLOCKED', can_approve: false, approve_blocked_reason: reason, approval: null })
    mockBulkFetch([listResponse([blocked], { limit: 50, offset: 0, total: 1 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-BLOCKED')

    const icon = screen.getByTestId('approval-blocked-icon')
    const tip = screen.getByTestId('approval-blocked-tip')
    // T1: the tip must start hidden -- an initially-open tip would otherwise pass below.
    expect(tip.getAttribute('style'), 'the tip must be hidden before any interaction').toContain('display: none')

    fireEvent.mouseEnter(icon.parentElement as HTMLElement)
    expect(tip.getAttribute('style'), 'hover must reveal the tip').not.toContain('display: none')
    expect(tip.textContent).toBe(reason)
    // T1: a position must already be stored -- an effect that never measures would leave
    // the tip permanently visibility:hidden.
    expect(tip.getAttribute('style'), 'a position must be stored once the tip is shown').not.toContain('visibility: hidden')

    fireEvent.mouseLeave(icon.parentElement as HTMLElement)
    expect(tip.getAttribute('style'), 'leaving the wrapper must hide the tip again').toContain('display: none')
  })
})

describe('B17-C3b: keyboard focus reveals it and Escape or blur hides it', () => {
  it('focus shows the tip; Escape or blur hides it; a hover-opened tip also closes on a window Escape', async () => {
    const reason = 'Only a validated invoice can be approved or rejected.'
    const blocked = approvalRow({ id: 'inv-blocked', invoice_number: 'INV-BLOCKED', can_approve: false, approve_blocked_reason: reason, approval: null })
    mockBulkFetch([listResponse([blocked], { limit: 50, offset: 0, total: 1 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-BLOCKED')

    const icon = screen.getByTestId('approval-blocked-icon')
    const tip = screen.getByTestId('approval-blocked-tip')
    const wrapper = icon.parentElement as HTMLElement

    fireEvent.focus(icon)
    expect(tip.getAttribute('style'), 'focus must reveal the tip').not.toContain('display: none')

    fireEvent.keyDown(icon, { key: 'Escape' })
    expect(tip.getAttribute('style'), 'Escape on the icon must hide it').toContain('display: none')

    fireEvent.focus(icon)
    expect(tip.getAttribute('style')).not.toContain('display: none')
    fireEvent.blur(icon)
    expect(tip.getAttribute('style'), 'blur must hide it').toContain('display: none')

    // T2: a hover-opened tip has no focus on the button, so Escape must reach it through
    // the window (useDismiss, D1) -- not only a button onKeyDown.
    fireEvent.mouseEnter(wrapper)
    expect(tip.getAttribute('style')).not.toContain('display: none')
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(tip.getAttribute('style'), 'a hover-opened tip must also close on a window Escape').toContain('display: none')
  })
})

describe('B17-C3c: a scroll closes an open tip', () => {
  it('scroll and resize both close an open tip', async () => {
    const reason = 'Only a validated invoice can be approved or rejected.'
    const blocked = approvalRow({ id: 'inv-blocked', invoice_number: 'INV-BLOCKED', can_approve: false, approve_blocked_reason: reason, approval: null })
    mockBulkFetch([listResponse([blocked], { limit: 50, offset: 0, total: 1 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-BLOCKED')

    const icon = screen.getByTestId('approval-blocked-icon')
    const tip = screen.getByTestId('approval-blocked-tip')

    fireEvent.focus(icon)
    expect(tip.getAttribute('style')).not.toContain('display: none')
    fireEvent.scroll(window)
    expect(tip.getAttribute('style'), 'a scroll must hide an open tip').toContain('display: none')

    // T3: AC-3 also names resize -- no earlier spec drove it.
    fireEvent.focus(icon)
    expect(tip.getAttribute('style')).not.toContain('display: none')
    fireEvent(window, new Event('resize'))
    expect(tip.getAttribute('style'), 'a resize must hide an open tip').toContain('display: none')
  })
})

describe('B17-C6: the tip escapes the list overflow', () => {
  it('the tip is position: fixed while approvals-list itself still clips', async () => {
    const reason = 'Only a validated invoice can be approved or rejected.'
    const blocked = approvalRow({ id: 'inv-blocked', invoice_number: 'INV-BLOCKED', can_approve: false, approve_blocked_reason: reason, approval: null })
    mockBulkFetch([listResponse([blocked], { limit: 50, offset: 0, total: 1 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-BLOCKED')

    // control: the list still clips -- this is why the tip must escape it.
    const list = screen.getByTestId('approvals-list')
    expect(list.getAttribute('style'), 'control: approvals-list must still declare overflow: hidden').toContain('overflow: hidden')

    const icon = screen.getByTestId('approval-blocked-icon')
    fireEvent.mouseEnter(icon.parentElement as HTMLElement)
    const tip = screen.getByTestId('approval-blocked-tip')
    expect(tip.getAttribute('style'), 'the open tip must declare position: fixed so the list clip cannot cut it').toContain('position: fixed')
  })
})

describe('A04-10: the prune effect settles (does not loop) when the row set actually changes', () => {
  it('paging to a disjoint set of rows disarms without hard-throwing "Maximum update depth exceeded"', async () => {
    const page1 = [approvalRow({ id: 'inv-1', invoice_number: 'INV-1' }), approvalRow({ id: 'inv-2', invoice_number: 'INV-2' })]
    const page2 = [approvalRow({ id: 'inv-3', invoice_number: 'INV-3' })]
    mockBulkFetch([listResponse(page1, { limit: 2, offset: 0, total: 3 }), listResponse(page2, { limit: 2, offset: 2, total: 3 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-1')

    fireEvent.click(screen.getByTestId('approval-select-all'))
    fireEvent.click(screen.getByTestId('approvals-bulk-submit'))
    expect(screen.getByTestId('approvals-bulk-confirm')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Next →' }))

    // If the updater failed to return the SAME `sel` instance on a no-op, or the prune
    // effect otherwise re-fired every render, this await would surface React 19's
    // "Maximum update depth exceeded" here instead of resolving.
    await screen.findByText('INV-3')

    expect(screen.queryByTestId('approvals-bulk-bar'), 'a page whose rows share nothing with the old selection must disarm').toBeNull()
  })
})

describe('A04-13: the bar shows LIVE progress during the fan-out, driven by onProgress (G-04-A)', () => {
  it('the progress indicator reflects each settled item before the fan-out finishes', async () => {
    const a = approvalRow({ id: 'inv-a', invoice_number: 'INV-A' })
    const b = approvalRow({ id: 'inv-b', invoice_number: 'INV-B' })
    const resolvers: Array<(r: MockResponse) => void> = []
    const fetchMock = vi.fn((url: string) => {
      if (/\/approvals$/.test(url)) return new Promise<MockResponse>((resolve) => resolvers.push(resolve))
      return Promise.resolve(listResponse([a, b], { limit: 50, offset: 0, total: 2 }))
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByTestId('approval-select-all'))
    fireEvent.click(screen.getByTestId('approvals-bulk-submit'))
    fireEvent.click(screen.getByTestId('approvals-bulk-confirm'))

    // Concurrency exactly 1 -- the first request ('inv-a') is now pending and 'inv-b' has
    // not been issued yet, so onProgress has fired zero times.
    await waitFor(() => expect(resolvers).toHaveLength(1))

    resolvers[0](approveOkResponse()) // settles inv-a -> onProgress(result, 0) fires, done=1 of 2

    await waitFor(() => {
      const progress = screen.getByTestId('approvals-progress')
      expect(progress.textContent).toContain('1')
      expect(progress.textContent).toContain('2')
    })

    resolvers[1](approveOkResponse())
    await screen.findByTestId('approvals-results')
  })
})

describe('A04-14: a FAILED id is not still selected once the refetch lands (G-04-E)', () => {
  it('selection is cleared explicitly in the handler, even for an id that survives (still approvable) in the refetch', async () => {
    const a = approvalRow({ id: 'inv-a', invoice_number: 'INV-A' })
    const b = approvalRow({ id: 'inv-b', invoice_number: 'INV-B' })
    const failMsg = 'this approval run is already closed'
    const { approvalCalls } = mockBulkFetch(
      [
        listResponse([a, b], { limit: 50, offset: 0, total: 2 }),
        // inv-a FAILED and remains in the refetch, still approvable --
        // pruneApprovalSelection alone would KEEP it selected since it is present and
        // still can_approve:true; only an explicit clear-on-settle removes it (G-04-E).
        listResponse([a], { limit: 50, offset: 0, total: 1 }),
      ],
      (id) => (id === 'inv-a' ? { ok: false, status: 409, json: () => Promise.resolve({ error: failMsg }) } : approveOkResponse()),
    )

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByTestId('approval-select-all'))
    fireEvent.click(screen.getByTestId('approvals-bulk-submit'))
    fireEvent.click(screen.getByTestId('approvals-bulk-confirm'))

    await waitFor(() => expect(approvalCalls()).toHaveLength(2))
    await screen.findByTestId('approvals-results')

    // Scoped to the queue rows, not `screen`: A04-5 requires the results panel to carry
    // each invoice number as its own exact-text node, and this refetch deliberately keeps
    // INV-A on the page -- so an unscoped getByText matches BOTH and throws. Query
    // disambiguation only; the assertion below is unchanged.
    const row = screen.getAllByTestId('approval-row').find((r) => within(r).queryByText('INV-A')) as HTMLElement
    const checkbox = within(row).getByTestId('approval-select-row') as HTMLInputElement
    expect(checkbox.checked, 'a FAILED id must not still be selected once the refetch lands, even though it is still present and approvable').toBe(false)
  })
})

// --- QA adversarial (Stage 4, Mode B) -- added on top of A04-1..A04-14 above, which are
// left untouched. Each test below closed a mutation that survived every A04 spec as
// shipped: A04-2's own POST-id assertion is protected by bar.eligible/pruneApprovalSelection
// at CONFIRM time regardless of what toggleAll puts into `selected`, so it never actually
// exercises AC-1's "select-all... gate on isApprovableRow" as a SELECTION-STATE property;
// and A04-3's two separate `fireEvent.click` calls each get their own synchronous React
// flush in this harness (confirmed via instrumentation: `confirmBtn.disabled` is already
// `true`, and a jsdom click dispatch on a disabled control never invokes its listener,
// before the second `fireEvent.click` call executes), so removing `approveInFlight`
// entirely left A04-3 green -- it was never reaching the ref at all. ---

describe('QA adversarial: select-all on a mixed page must not mark a non-approvable row as selected (AC-1)', () => {
  it("a blocked row's own checkbox stays unchecked after select-all, even though its id never reaches the wire either way", async () => {
    const a = approvalRow({ id: 'inv-a', invoice_number: 'INV-A', can_approve: true })
    const blocked = approvalRow({
      id: 'inv-blocked',
      invoice_number: 'INV-BLOCKED',
      can_approve: false,
      approve_blocked_reason: 'Only a validated invoice can be approved or rejected.',
      approval: null,
    })
    mockBulkFetch([listResponse([a, blocked], { limit: 50, offset: 0, total: 2 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByTestId('approval-select-all'))

    const blockedRow = screen.getByText('INV-BLOCKED').closest('[data-testid="approval-row"]') as HTMLElement
    const blockedCheckbox = within(blockedRow).getByTestId('approval-select-row') as HTMLInputElement
    expect(blockedCheckbox.checked, 'select-all must never mark a non-approvable row as selected, not just keep its id off the wire').toBe(false)

    const okRow = screen.getByText('INV-A').closest('[data-testid="approval-row"]') as HTMLElement
    const okCheckbox = within(okRow).getByTestId('approval-select-row') as HTMLInputElement
    expect(okCheckbox.checked, 'the approvable row must still be selected').toBe(true)
  })
})

describe('QA adversarial: the TRUE same-tick double click (both clicks inside ONE outer act())', () => {
  it('two clicks batched into the SAME React commit still fan out exactly once', async () => {
    const a = approvalRow({ id: 'inv-a', invoice_number: 'INV-A' })
    const b = approvalRow({ id: 'inv-b', invoice_number: 'INV-B' })
    const { approvalCalls } = mockBulkFetch([
      listResponse([a, b], { limit: 50, offset: 0, total: 2 }),
      listResponse([a, b], { limit: 50, offset: 0, total: 2 }),
    ])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByTestId('approval-select-all'))
    fireEvent.click(screen.getByTestId('approvals-bulk-submit'))
    const confirmBtn = screen.getByTestId('approvals-bulk-confirm')

    // A04-3's two SEPARATE fireEvent.click calls each get their own synchronous flush, so
    // `disabled` alone already wins there. Nesting both dispatches inside ONE outer act()
    // suppresses the intermediate flush (RTL's own per-fireEvent act() nests and collapses
    // into the outer one), so both onClick handlers run against the SAME pre-render
    // `phase` closure before either commits -- the actual race approveInFlight exists to
    // survive, confirmed by reproducing it against a ref-removed mutation (4 calls, not 2)
    // before writing this assertion.
    act(() => {
      fireEvent.click(confirmBtn)
      fireEvent.click(confirmBtn)
    })

    await screen.findByTestId('approvals-results')
    expect(approvalCalls(), 'two clicks landing in the SAME commit must still fan out exactly once, not twice').toHaveLength(2)
  })
})

describe('QA adversarial: progress reads "0 of N" before any item has settled', () => {
  it('the initial progress frame, set synchronously by confirmApprove itself, names the total before onProgress ever fires', async () => {
    const a = approvalRow({ id: 'inv-a', invoice_number: 'INV-A' })
    const b = approvalRow({ id: 'inv-b', invoice_number: 'INV-B' })
    const resolvers: Array<(r: MockResponse) => void> = []
    const fetchMock = vi.fn((url: string) => {
      if (/\/approvals$/.test(url)) return new Promise<MockResponse>((resolve) => resolvers.push(resolve))
      return Promise.resolve(listResponse([a, b], { limit: 50, offset: 0, total: 2 }))
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByTestId('approval-select-all'))
    fireEvent.click(screen.getByTestId('approvals-bulk-submit'))
    fireEvent.click(screen.getByTestId('approvals-bulk-confirm'))

    // The first request is now pending and onProgress has fired zero times -- this is the
    // 0-of-N frame A04-13 starts one settle past.
    await waitFor(() => expect(resolvers).toHaveLength(1))
    const progress = screen.getByTestId('approvals-progress')
    expect(progress.textContent).toContain('0')
    expect(progress.textContent).toContain('2')

    resolvers[0](approveOkResponse())
    await waitFor(() => expect(resolvers).toHaveLength(2))
    resolvers[1](approveOkResponse())
    await screen.findByTestId('approvals-results')
  })
})

describe('QA adversarial: a fan-out where EVERY item fails still renders one result row per invoice', () => {
  it('the panel is not suppressed or collapsed to a single message when nothing succeeded', async () => {
    const a = approvalRow({ id: 'inv-a', invoice_number: 'INV-A' })
    const b = approvalRow({ id: 'inv-b', invoice_number: 'INV-B' })
    const failMsg = 'this approval run is already closed'
    const { approvalCalls } = mockBulkFetch(
      [listResponse([a, b], { limit: 50, offset: 0, total: 2 }), listResponse([a, b], { limit: 50, offset: 0, total: 2 })],
      () => ({ ok: false, status: 409, json: () => Promise.resolve({ error: failMsg }) }),
    )

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByTestId('approval-select-all'))
    fireEvent.click(screen.getByTestId('approvals-bulk-submit'))
    fireEvent.click(screen.getByTestId('approvals-bulk-confirm'))

    await waitFor(() => expect(approvalCalls()).toHaveLength(2))
    const panel = await screen.findByTestId('approvals-results')
    const resultRows = within(panel).getAllByTestId('approval-result-row')

    expect(resultRows, 'an all-failed fan-out must still render one row per invoice, not a collapsed summary').toHaveLength(2)
    expect(within(panel).getByText('INV-A')).toBeTruthy()
    expect(within(panel).getByText('INV-B')).toBeTruthy()
    expect(within(panel).getAllByText(failMsg)).toHaveLength(2)
  })
})

describe("QA adversarial: a blocked row's reason survives characters that would need escaping if rendered as markup", () => {
  it('renders the sentence as literal text via textContent, not interpreted HTML', async () => {
    const reason = 'Blocked: role is "Compliance & Risk" <owner> — see policy.'
    const blocked = approvalRow({ id: 'inv-blocked', invoice_number: 'INV-BLOCKED', can_approve: false, approve_blocked_reason: reason, approval: null })
    mockBulkFetch([listResponse([blocked], { limit: 50, offset: 0, total: 1 })])

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-BLOCKED')

    const tip = screen.getByTestId('approval-blocked-tip')
    expect(tip.textContent, 'the server sentence must render byte-identically, including quotes/angle-brackets/em-dash').toBe(reason)
    // React text children are never parsed as markup, but this pins that no consumer
    // downgrades to dangerouslySetInnerHTML later: an <owner> substring must not become a
    // real (empty) child element.
    expect(tip.querySelector('owner'), 'the "<owner>" substring must render as literal text, never as a child element').toBeNull()
  })
})

// --- Source scans (by-path, mirroring A03-9 above / BULK-15's shipped idiom in
// reviewBatch.test.ts) -- both hit a wall a render-driven spec cannot cross. ---

describe('ApprovalsView.tsx source: the prune effect preserves selected\'s IDENTITY when nothing changed (A04-12, G-04-B)', () => {
  // `rows` is ALREADY useMemo'd (ApprovalsView.tsx:68-71), so a render-driven spec cannot
  // go RED against an identity-breaking updater (`return next` unconditionally): the memo
  // alone already stops `rows` from changing identity on a no-op render, so the updater's
  // own identity bail never gets exercised by rendering. By-path source scan instead,
  // BULK-15's shipped idiom (reviewBatch.test.ts:1653-1676). Both halves are required:
  // the ternary IS the identity contract, and useMemo is the separate half that keeps
  // `rows` itself from re-triggering the effect on every render -- either alone is
  // insufficient (InvoicesList.tsx:136-148 records needing both).
  function scanPruneEffectShape(source: string): { hasTernary: boolean; hasMemo: boolean } {
    return {
      hasTernary: /next\.length === sel\.length\s*\?\s*sel\s*:\s*next/.test(source),
      hasMemo: /const rows = useMemo\(/.test(source),
    }
  }

  it('non-vacuity control: the scan tells the correct shape apart from a deliberately identity-breaking one', () => {
    const good = `
      useEffect(() => {
        setSelected((sel) => {
          const next = pruneApprovalSelection(sel, rows)
          return next.length === sel.length ? sel : next
        })
      }, [rows])
      const rows = useMemo(() => gateByActiveEntity(list.data), [list.data])
    `
    const bad = `
      useEffect(() => {
        setSelected((sel) => {
          const next = pruneApprovalSelection(sel, rows)
          return next
        })
      }, [rows])
      const rows = gateByActiveEntity(list.data)
    `
    expect(scanPruneEffectShape(good)).toEqual({ hasTernary: true, hasMemo: true })
    expect(scanPruneEffectShape(bad)).toEqual({ hasTernary: false, hasMemo: false })
  })

  it('A04-12: the prune effect does not exist in ApprovalsView.tsx yet', () => {
    const source = readSrc('src/components/ApprovalsView.tsx')
    const shape = scanPruneEffectShape(source)

    expect(shape.hasTernary, 'the prune effect must return the SAME sel instance when nothing changed').toBe(true)
    expect(shape.hasMemo, "rows must stay useMemo'd -- dropping it is what actually reintroduces the loop").toBe(true)
  })
})

describe('ApprovalsView.tsx source: the confirm handler guards on base == null before any network call (G-04-H)', () => {
  it('the guard sits before approveInvoices( is ever called', () => {
    // base cannot go null while the confirm button is reachable through the DOM: base is
    // recomputed fresh every render (gatewayBase() off import.meta.env, not state), and
    // invoicesViewState forces state:'idle' whenever base is null -- so the instant base
    // flips null on any re-render, the whole `state === 'ready'` subtree the confirm
    // button lives in unmounts before a click could ever reach the handler. A04-12 hits
    // the identical wall; source scan instead, mirroring InvoicesList.tsx:283's
    // submitSelection guard.
    const source = readSrc('src/components/ApprovalsView.tsx')

    const approveCallIdx = source.indexOf('approveInvoices(')
    expect(approveCallIdx, 'approveInvoices( must be called somewhere for this ordering check to be meaningful').toBeGreaterThan(-1)

    const guardIdx = source.search(/if \(base == null\) return\b/)
    expect(guardIdx, 'the confirm handler must guard on base == null before calling approveInvoices').toBeGreaterThan(-1)
    expect(guardIdx).toBeLessThan(approveCallIdx)
  })
})

// --- APPR-16-04 (task-536, Mode A) -- RED specs for the unmount-abort wiring (AC-5) and
// the in-flight pager freeze (AC-6/AC-7), on the approvals surface only. AC-8 (switcher/
// nav stay live, D-31) is covered on the register surface, InvoicesList.test.tsx's
// A16-4h -- both surfaces share the same Pager, so pinning it once there is sufficient.
describe('A16-4: unmount aborts the fan-out at a row boundary, and the pager freezes for the whole in-flight window (APPR-16-04)', () => {
  it('A16-4e: unmounting ApprovalsView mid-fan-out aborts at the next row boundary -- no further POST', async () => {
    const a = approvalRow({ id: 'inv-a', invoice_number: 'INV-A' })
    const b = approvalRow({ id: 'inv-b', invoice_number: 'INV-B' })
    const c = approvalRow({ id: 'inv-c', invoice_number: 'INV-C' })
    const resolvers = new Map<string, (r: MockResponse) => void>()
    // row b is held in flight -- unmount lands while it is still unsettled.
    const { fetchMock, approvalCalls } = mockBulkFetch(
      [listResponse([a, b, c], { limit: 50, offset: 0, total: 3 })],
      (id) => (id === 'inv-b' ? new Promise<MockResponse>((resolve) => resolvers.set(id, resolve)) : approveOkResponse()),
    )

    const { unmount } = render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByTestId('approval-select-all'))
    fireEvent.click(screen.getByTestId('approvals-bulk-submit'))
    fireEvent.click(screen.getByTestId('approvals-bulk-confirm'))

    await waitFor(() => expect(approvalCalls()).toHaveLength(2)) // a settled, b's POST issued and pending
    expect(resolvers.has('inv-b')).toBe(true)

    unmount()
    resolvers.get('inv-b')!(approveOkResponse())
    // No DOM survives the unmount to assert against -- flush enough microtask hops for
    // the loop to resume and (if unguarded) fire row c's request.
    await act(async () => {
      for (let i = 0; i < 20; i++) await Promise.resolve()
    })

    expect(
      approvalCalls(),
      "row b's own response must still be recorded (abort never cancels an in-flight request), but row c must never be requested after unmount",
    ).toHaveLength(2)
    expect(fetchMock.mock.calls.filter(([u]) => /\/invoices\/inv-c\/approvals$/.test(u as string))).toHaveLength(0)
  })

  it('A16-4f: the approvals pager is disabled for the whole in-flight window, and re-enabled once settled', async () => {
    const a = approvalRow({ id: 'inv-a', invoice_number: 'INV-A' })
    const b = approvalRow({ id: 'inv-b', invoice_number: 'INV-B' })
    const resolvers: Array<(r: MockResponse) => void> = []
    const fetchMock = vi.fn((url: string) => {
      if (/\/approvals$/.test(url)) return new Promise<MockResponse>((resolve) => resolvers.push(resolve))
      // limit:1/offset:1/total:3 -- both Prev and Next start enabled absent `busy`, so
      // the freeze under test is the OR clause, not an edge-of-set disable.
      return Promise.resolve(listResponse([a, b], { limit: 1, offset: 1, total: 3 }))
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<ApprovalsView ctx={approvalsCtx()} />)
    await screen.findByText('INV-A')

    const pager = () => screen.getByTestId('approvals-pager')
    const prevBtn = () => within(pager()).getByText('← Previous').closest('button') as HTMLButtonElement
    const nextBtn = () => within(pager()).getByText('Next →').closest('button') as HTMLButtonElement
    expect(prevBtn().disabled, 'both buttons must start enabled -- proves the freeze, not an edge-of-set disable, is under test').toBe(false)
    expect(nextBtn().disabled).toBe(false)
    // Not yet frozen -- title never fires on a disabled element in Chromium, so an absent
    // visible node here would mean the reason is unreachable the moment it matters.
    expect(within(pager()).queryByTestId('pager-blocked-reason'), 'no reason node before the freeze').toBeNull()

    fireEvent.click(screen.getByTestId('approval-select-all'))
    fireEvent.click(screen.getByTestId('approvals-bulk-submit'))
    fireEvent.click(screen.getByTestId('approvals-bulk-confirm')) // now submitting, POST held pending

    // `loading` is false here (list.data is still the settled page) -- only `phase ===
    // 'submitting'` can be freezing the pager in this window.
    expect(prevBtn().disabled, 'the pager must freeze for the whole in-flight window').toBe(true)
    expect(nextBtn().disabled).toBe(true)
    // AC-7/D-25: a disabled control must state WHY, not go silently dead -- InvoicesList's
    // sibling surface pins this (A16-4h); the approvals pager owed the same assertion.
    expect(prevBtn().title, 'the frozen pager must carry a non-empty reason, not a bare disabled attribute').toBeTruthy()
    expect(nextBtn().title).toBeTruthy()
    // The reason must reach the user as VISIBLE text, not just the inert title.
    expect(within(pager()).getByTestId('pager-blocked-reason').textContent, 'the visible reason must be APPROVALS_COPY.pagerReason').toBe(APPROVALS_COPY.pagerReason)
    expect(screen.getByText(APPROVALS_COPY.pagerReason), 'queryable by text, not just by attribute').toBeTruthy()

    resolvers[0](approveOkResponse()) // row a settles -- row b's POST issues next
    await waitFor(() => expect(resolvers.length).toBe(2))

    // The inter-row point: row a settled, row b now in flight. Only `phase ===
    // 'submitting'` can still be freezing the pager here -- proves the freeze holds
    // ACROSS the row boundary, not just for a single request.
    expect(prevBtn().disabled, 'the pager must stay frozen between rows, not flicker enabled at the row boundary').toBe(true)
    expect(nextBtn().disabled).toBe(true)

    resolvers[1](approveOkResponse())
    await screen.findByTestId('approvals-results')
    await waitFor(() => expect(nextBtn().disabled, 'the pager must re-enable once settled').toBe(false))
    expect(prevBtn().disabled).toBe(false)
  })
})
