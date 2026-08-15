// @vitest-environment jsdom
// RED specs (APPR-12-03, task-528, A03-2..A03-9, Mode A) -- pin the Approvals screen's
// wiring against the ApprovalsView.tsx stub (`return null`) before the executor fills it
// in. Every spec below fails today because the stub renders nothing -- that IS the
// correct red reason (assertion / not-found), never an import or compile error. Mirrors
// InvoicesList.test.tsx's own `mockFetchSequence`/`row()`/`listCtx()` idiom.

import { readFileSync } from 'node:fs'
import path from 'node:path'

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

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
    const cells = row!.children
    expect(cells[3]?.textContent, 'step column must fall back to the em dash, never "Step null"').toBe('—')
    expect(cells[4]?.textContent, 'role column must fall back to the em dash').toBe('—')
    expect(cells[5]?.textContent, 'due column must fall back to the em dash').toBe('—')
    expect(row!.querySelector('[data-testid="approval-unstaffed-warning"]'), 'no warning without a pending holder to warn about').toBeNull()
    expect(row!.querySelector('[data-testid="approval-overdue"]'), 'no overdue marker without a run to be overdue on').toBeNull()
  })
})
