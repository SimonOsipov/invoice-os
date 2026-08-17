// task-498 (APPR-08-07, Mode B): the emission rule for listInvoices' query params.
//
// AC #9 had no enforcement at all. Deleting the awaiting_approval line from listInvoices
// leaves `tsc --noEmit` green (the interface field still typechecks) and the whole vitest
// suite green, because nothing calls listInvoices under vitest and the first Playwright
// caller lands in APPR-08-10. A dropped emit line would therefore reach main silently, and
// APPR-08-10 would look like it had broken the filter.
//
// The @invoice-os/api-client seam is mocked so this asserts the URL listInvoices BUILDS,
// with no network. topology/targets.ts calls resolveTarget at module scope, so the env
// vars must be set before client.ts is imported -- hence the dynamic import.
//
// task-570 (APPR-14-04, Mode A): RED specs for approveUntilClosed/firmApproverTokens,
// authored before either exists in client.ts. Deployment-free loop-guard coverage lives
// here (AC-2/3/4/5/7/8 + the no-pending-step guard); AC-1 and AC-6 need a live gateway and
// are declared in contract-invoice.spec.ts instead. The mock below is now URL-aware (see
// `calls`) so a default-transport call can be told apart from a listInvoices call without
// touching the four pre-existing listInvoices assertions.
import { beforeAll, describe, expect, it, vi } from 'vitest'
import type { ApprovalRun } from './client'

const requested: string[] = []
// calls: url + method, additive to `requested` -- lets AC-7/AC-8 tell a GET /approval
// apart from a POST /approvals or /auth/login without changing what `requested` holds.
const calls: { url: string; method: string; body?: unknown }[] = []

// CLOSED_RUN: what the URL-aware mock answers any GET .../approval with -- an
// already-closed run, so the AC-8 default-transport test can prove decide is never reached.
const CLOSED_RUN: ApprovalRun = {
  run_id: 'run-default-closed',
  state: 'approved',
  opened_at: '2026-01-01T00:00:00Z',
  closed_at: '2026-01-01T01:00:00Z',
  closed_by: 'c0000000-0000-0000-0000-000000000004',
  steps: [],
  decisions: [],
}

vi.mock('@invoice-os/api-client/client', () => ({
  apiFetch: (url: string, init?: { method?: string; body?: unknown }) => {
    requested.push(url)
    calls.push({ url, method: init?.method ?? 'GET', body: init?.body })
    if (url.endsWith('/auth/login')) {
      const subject = (init?.body as { subject?: string } | undefined)?.subject
      return Promise.resolve({ access_token: `token-for-${subject}` })
    }
    if (url.includes('/approval')) {
      return Promise.resolve(CLOSED_RUN)
    }
    return Promise.resolve({ invoices: [], pagination: {} })
  },
  ApiError: class ApiError extends Error {},
}))

// Local types for the two not-yet-exported functions -- NOT derived from
// `typeof import('./client')`, which would fail `tsc --noEmit` until client.ts actually
// exports them (that failure is the point of a RED test, but a compile error is the wrong
// kind: Mode A wants a RUNTIME "not a function", not a collection-time error).
type ApprovalTransport = {
  read: (...args: any[]) => Promise<ApprovalRun>
  decide: (...args: any[]) => Promise<ApprovalRun>
}
type ApproveUntilClosed = (
  invoiceId: string,
  tokens: Record<string, string>,
  max?: number,
  transport?: ApprovalTransport,
) => Promise<ApprovalRun>
type FirmApproverTokens = () => Promise<Record<string, string>>

let listInvoices: (typeof import('./client'))['listInvoices']
let approveUntilClosed: ApproveUntilClosed
let firmApproverTokens: FirmApproverTokens

beforeAll(async () => {
  process.env.GATEWAY_URL = 'https://gateway.test'
  process.env.APP_URL = 'https://app.test'
  const mod = (await import('./client')) as unknown as {
    listInvoices: (typeof import('./client'))['listInvoices']
    approveUntilClosed: ApproveUntilClosed
    firmApproverTokens: FirmApproverTokens
  }
  ;({ listInvoices, approveUntilClosed, firmApproverTokens } = mod)
})

// query returns the query string listInvoices issued for the given argument.
async function query(arg?: Parameters<typeof listInvoices>[1]): Promise<string> {
  requested.length = 0
  await listInvoices('token', arg)
  expect(requested).toHaveLength(1)
  return requested[0].slice(requested[0].indexOf('/api/'))
}

describe('listInvoices awaiting_approval', () => {
  it('emits nothing when the field is omitted', async () => {
    expect(await query()).toBe('/api/invoice/v1/invoices')
    expect(await query({ limit: 5 })).toBe('/api/invoice/v1/invoices?limit=5')
  })

  it('emits awaiting_approval=true', async () => {
    expect(await query({ awaiting_approval: true })).toBe('/api/invoice/v1/invoices?awaiting_approval=true')
  })

  // The `!== undefined` rule, not the SPA client's `=== true` rule: explicit false is a
  // real query string here. strconv.ParseBool accepts "false", and ListFilter's zero value
  // applies no predicate, so the server answers it exactly as it answers an absent param.
  it('emits awaiting_approval=false rather than dropping it', async () => {
    expect(await query({ awaiting_approval: false })).toBe('/api/invoice/v1/invoices?awaiting_approval=false')
  })

  it('composes with the other params', async () => {
    expect(await query({ limit: 2, offset: 4, q: 'acme', entity_id: 'e-1', awaiting_approval: true })).toBe(
      '/api/invoice/v1/invoices?limit=2&offset=4&q=acme&entity_id=e-1&awaiting_approval=true',
    )
  })
})

// captureRejection awaits a promise and returns its rejection -- so a test can assert the
// EXACT message rather than merely that something threw (a wrong-reason throw, e.g. a typo
// reading `.workflow_role_key` off undefined, must fail the assertion, not slip through).
async function captureRejection(p: Promise<unknown>): Promise<Error> {
  try {
    await p
  } catch (e) {
    return e as Error
  }
  throw new Error('expected the promise to reject, but it resolved')
}

// step()/run(): minimal ApprovalRun/ApprovalRunStep builders for the scripted transports
// below. Explicit params only (no Partial<T> spread) -- spreading a Partial onto a fully
// keyed literal makes every base field's inferred type `T | undefined`, which the
// ApprovalRunStep/ApprovalRun return annotations then reject.
function step(ord: number, workflowRoleKey: string): ApprovalRun['steps'][number] {
  return {
    ord,
    kind: 'approval',
    state: 'pending',
    workflow_role_key: workflowRoleKey,
    workflow_role_title: null,
    holder: null,
    sla_hours: null,
    due_at: null,
    overdue: false,
    satisfied_at: null,
    satisfied_by: null,
    notify_target: null,
    notify_channel: null,
  }
}

function run(opts: {
  run_id?: string
  state?: string
  closed_at?: string | null
  closed_by?: string | null
  steps?: ApprovalRun['steps']
} = {}): ApprovalRun {
  return {
    run_id: opts.run_id ?? 'run-1',
    state: opts.state ?? 'open',
    opened_at: '2026-01-01T00:00:00Z',
    closed_at: opts.closed_at ?? null,
    closed_by: opts.closed_by ?? null,
    steps: opts.steps ?? [],
    decisions: [],
  }
}

describe('approveUntilClosed', () => {
  it('AC-2: returns immediately, without deciding, on a run that is already closed', async () => {
    const closed = run({ state: 'approved', closed_at: '2026-01-01T01:00:00Z', closed_by: 'admin' })
    const read = vi.fn().mockResolvedValue(closed)
    const decide = vi.fn()

    const result = await approveUntilClosed('inv-1', {}, 6, { read, decide })

    expect(result).toEqual(closed)
    expect(decide).not.toHaveBeenCalled()
    expect(read).toHaveBeenCalledTimes(1)
  })

  it('AC-3: throws naming the pending role when tokens has no entry for it', async () => {
    const openRun = run({ steps: [step(1, 'compliance')] })
    const read = vi.fn().mockResolvedValue(openRun)
    const decide = vi.fn()

    const err = await captureRejection(approveUntilClosed('inv-1', {}, 6, { read, decide }))

    expect(err.message).toBe('approveUntilClosed: no token for pending role "compliance" (invoice inv-1)')
    expect(decide).not.toHaveBeenCalled()
  })

  // Stage-1 correction (c): an open run with no pending approval step must not fall
  // through to `tokens[undefined]` -- that would report a confusing missing-token error
  // for a role that doesn't exist.
  it('throws a stalled-run error when the run is open but has no pending approval step', async () => {
    const openRun = run({ steps: [] })
    const read = vi.fn().mockResolvedValue(openRun)
    const decide = vi.fn()

    const err = await captureRejection(approveUntilClosed('inv-1', { compliance: 'tok-compliance' }, 6, { read, decide }))

    expect(err.message).toBe('approveUntilClosed: run is open but has no pending approval step (invoice inv-1, run run-1)')
    expect(decide).not.toHaveBeenCalled()
  })

  it('AC-4: throws when the pending ord does not advance, rather than looping to max', async () => {
    const stalled = run({ steps: [step(0, 'fin_mgr')] })
    const read = vi.fn().mockResolvedValue(stalled)
    const decide = vi.fn().mockResolvedValue(stalled)

    const err = await captureRejection(approveUntilClosed('inv-1', { fin_mgr: 'tok-fin_mgr' }, 6, { read, decide }))

    expect(err.message).toBe('approveUntilClosed: pending ord did not advance (invoice inv-1, run run-1, ord 0, role fin_mgr)')
    expect(decide).toHaveBeenCalledTimes(1)
  })

  it('AC-5: throws on max exceeded, naming the invoice and the last pending role', async () => {
    // ord advances on every call (read or decide alike) so the stalled-ord guard never
    // fires -- this test is only about the max guard.
    let ord = -1
    const nextRun = () => {
      ord += 1
      return run({ steps: [step(ord, 'compliance')] })
    }
    const read = vi.fn(() => Promise.resolve(nextRun()))
    const decide = vi.fn(() => Promise.resolve(nextRun()))

    const err = await captureRejection(approveUntilClosed('inv-1', { compliance: 'tok-compliance' }, 3, { read, decide }))

    expect(err.message).toBe('approveUntilClosed: exceeded max decisions (3) for invoice inv-1; still pending role compliance')
    expect(decide).toHaveBeenCalledTimes(3)
  })

  it('AC-8: the transport defaults to the real getInvoiceApproval/decideInvoiceApproval pair', async () => {
    calls.length = 0

    const result = await approveUntilClosed('inv-default', { compliance: 'tok-compliance' })

    expect(result).toEqual(CLOSED_RUN)
    const reads = calls.filter((c) => c.method === 'GET' && c.url.includes('/approval'))
    const decides = calls.filter((c) => c.method === 'POST' && c.url.includes('/approval'))
    expect(reads).toHaveLength(1)
    expect(decides).toHaveLength(0)
  })
})

describe('firmApproverTokens', () => {
  it('AC-7: mints each holder token once per worker, even called twice', async () => {
    calls.length = 0

    const first = await firmApproverTokens()
    const second = await firmApproverTokens()

    const logins = calls.filter((c) => c.url.endsWith('/auth/login'))
    expect(logins).toHaveLength(2) // fin_mgr + compliance, minted once total across both calls
    expect(second).toEqual(first)
  })
})
