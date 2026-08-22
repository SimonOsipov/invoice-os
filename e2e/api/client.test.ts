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
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

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

  // QA adversarial: a fresh run (new run_id) starting at the same ord as the previous
  // run's last-decided step -- a cancel-and-re-arm -- must not be mistaken for a stall.
  it('the stalled-ord guard is keyed on (run_id, ord), not ord alone: a fresh run at the same ord is not a stall', async () => {
    const runA = run({ run_id: 'run-A', steps: [step(0, 'fin_mgr')] })
    const runBOpen = run({ run_id: 'run-B', steps: [step(0, 'fin_mgr')] })
    const runBClosed = run({ run_id: 'run-B', state: 'approved', closed_at: '2026-01-01T02:00:00Z', closed_by: 'fin_mgr' })
    const read = vi.fn().mockResolvedValue(runA)
    const decide = vi.fn().mockResolvedValueOnce(runBOpen).mockResolvedValueOnce(runBClosed)

    const result = await approveUntilClosed('inv-1', { fin_mgr: 'tok-fin_mgr' }, 6, { read, decide })

    expect(result).toEqual(runBClosed)
    expect(decide).toHaveBeenCalledTimes(2)
  })

  // QA adversarial: pins AC-6's role-key-and-subject wrap deployment-free (the other AC-6
  // row needs a live gateway). Token payload is real base64url JSON, matching decodeJwtSubject.
  it('AC-6: a decide failure is wrapped with the pending role key and the subject decoded from the token', async () => {
    const openRun = run({ steps: [step(0, 'fin_mgr')] })
    const read = vi.fn().mockResolvedValue(openRun)
    const decide = vi.fn().mockRejectedValue(new Error('403 forbidden'))
    const subject = 'c0000000-0000-0000-0000-000000000099'
    const token = `header.${Buffer.from(JSON.stringify({ sub: subject })).toString('base64url')}.sig`

    const err = await captureRejection(approveUntilClosed('inv-1', { fin_mgr: token }, 6, { read, decide }))

    expect(err.message).toBe(
      `approveUntilClosed: decide failed for role "fin_mgr" as subject ${subject} (invoice inv-1): 403 forbidden`,
    )
  })

  // A non-JWT token must not let decodeJwtSubject's own parse error replace the real decide
  // failure being reported here; the subject degrades to 'unavailable' instead.
  it('a decide failure with an undecodable token reports the subject as unavailable', async () => {
    const openRun = run({ steps: [step(0, 'fin_mgr')] })
    const read = vi.fn().mockResolvedValue(openRun)
    const decide = vi.fn().mockRejectedValue(new Error('403 forbidden'))

    const err = await captureRejection(approveUntilClosed('inv-1', { fin_mgr: 'not-a-jwt' }, 6, { read, decide }))

    expect(err.message).toBe(
      'approveUntilClosed: decide failed for role "fin_mgr" as subject unavailable (invoice inv-1): 403 forbidden',
    )
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

  // Empty tokens against the real transport would reach the initial read unauthenticated
  // and surface a bare 401; this must fail before that call, naming the real problem.
  // Scripted transports (AC-2/AC-3 above) don't touch the network on the token's account,
  // so this only guards the default pair.
  it('throws before any network call when tokens is empty and no transport is injected', async () => {
    calls.length = 0

    const err = await captureRejection(approveUntilClosed('inv-empty-tokens', {}))

    expect(err.message).toBe(
      'approveUntilClosed: tokens is empty; cannot authenticate the initial read (invoice inv-empty-tokens)',
    )
    expect(calls).toHaveLength(0)
  })
})

describe('firmApproverTokens', () => {
  // QA adversarial, runs FIRST to see a cold cache (the module-scope memo persists across
  // tests in this file). No await between the two calls -- a sequential pair can't tell
  // "memoise the promise" apart from "memoise the value", since by call 2 call 1 already
  // settled either way. Concurrency is the only oracle that distinguishes them.
  it('AC-7: two concurrent callers mint exactly two logins, not four', async () => {
    calls.length = 0

    const [first, second] = await Promise.all([firmApproverTokens(), firmApproverTokens()])

    const logins = calls.filter((c) => c.url.endsWith('/auth/login'))
    expect(logins).toHaveLength(2) // fin_mgr + compliance, minted once despite the race
    expect(second).toEqual(first)
  })

  // Runs second, against the warm cache the test above left: a further call must not re-mint.
  it('AC-7: a further call after the cache is warm mints no additional logins', async () => {
    calls.length = 0

    const third = await firmApproverTokens()

    const logins = calls.filter((c) => c.url.endsWith('/auth/login'))
    expect(logins).toHaveLength(0)
    expect(third).toEqual({
      fin_mgr: 'token-for-c0000000-0000-0000-0000-000000000004',
      compliance: 'token-for-c0000000-0000-0000-0000-000000000005',
    })
  })
})

// --- AUDIT-04-08 (task-627, AC #5): the audit reader's wire mirror, Go
// internal/audit/reader.go <-> e2e/api/client.ts. Two-way, not three (D-10): there is no SPA
// mirror, because the endpoint has no UI yet.
//
// The three helpers below are copied from frontend/app/src/lib/approvals.test.ts:867-902 --
// this package cannot import from frontend/app/src.

const AUDIT_GO_PATH = fileURLToPath(new URL('../../internal/audit/reader.go', import.meta.url))
const AUDIT_TS_PATH = fileURLToPath(new URL('./client.ts', import.meta.url))

// Struct-scoped, so a file-wide tag regex cannot fold one struct's keys into another's count.
function goStructKeys(source: string, structName: string): string[] {
  const body = new RegExp(`type\\s+${structName}\\s+struct\\s*\\{([^{}]*)\\}`).exec(source)?.[1] ?? ''
  const keys: string[] = []
  for (const m of body.matchAll(/`json:"([^"]+)"`/g)) {
    const key = m[1].split(',')[0]
    if (key !== '-') keys.push(key)
  }
  return keys
}

function tsInterfaceKeys(source: string, interfaceName: string): string[] {
  const body = new RegExp(`export interface\\s+${interfaceName}\\s*\\{([^{}]*)\\}`).exec(source)?.[1] ?? ''
  const keys: string[] = []
  for (const rawSeg of body.split(/[\n;]/)) {
    const seg = rawSeg.trim()
    if (!seg || seg.startsWith('//')) continue
    const m = /^([A-Za-z_][A-Za-z0-9_]*)\??\s*:/.exec(seg)
    if (m) keys.push(m[1])
  }
  return keys
}

function keySetDiff(a: string[], b: string[]): string[] {
  const setA = new Set(a)
  const setB = new Set(b)
  const diff = new Set<string>()
  for (const k of a) if (!setB.has(k)) diff.add(k)
  for (const k of b) if (!setA.has(k)) diff.add(k)
  return [...diff]
}

// Floors are the CURRENT key counts, so adding a field on one side alone trips the floor
// before the equality row runs. Facet's Go tag is `kind,omitempty`, which extracts as `kind` --
// the TS side must declare it.
const AUDIT_WIRE_STRUCTS = [
  { go: 'Event', ts: 'AuditEvent', floor: 10 },
  { go: 'PageInfo', ts: 'AuditPageInfo', floor: 3 },
  { go: 'Facet', ts: 'AuditFacet', floor: 4 },
  { go: 'Facets', ts: 'AuditFacets', floor: 3 },
  { go: 'Response', ts: 'AuditResponse', floor: 5 },
] as const

describe('wire mirror: Go internal/audit/reader.go <-> e2e/api/client.ts (AUDIT-04-08 AC #5)', () => {
  // Needle and floor run BEFORE equality: a broken regex yields [] on both sides and the
  // symmetric difference of two empty sets is empty, so equality alone passes vacuously.
  it('control needle: both source files were really read, and are the files we meant', () => {
    const goSource = readFileSync(AUDIT_GO_PATH, 'utf8')
    const tsSource = readFileSync(AUDIT_TS_PATH, 'utf8')

    expect(goSource.length).toBeGreaterThan(0)
    expect(goSource, 'lost anchor on internal/audit/reader.go').toContain('type Response struct')

    expect(tsSource.length).toBeGreaterThan(0)
    expect(tsSource, 'lost anchor on e2e/api/client.ts').toContain('getAuditLog')
  })

  it('population FLOOR: every struct and interface yielded at least its current key count', () => {
    const goSource = readFileSync(AUDIT_GO_PATH, 'utf8')
    const tsSource = readFileSync(AUDIT_TS_PATH, 'utf8')

    for (const { go, ts, floor } of AUDIT_WIRE_STRUCTS) {
      expect(goStructKeys(goSource, go).length, `Go ${go} must clear its floor of ${floor}`).toBeGreaterThanOrEqual(
        floor,
      )
      expect(
        tsInterfaceKeys(tsSource, ts).length,
        `client.ts ${ts} must clear its floor of ${floor}`,
      ).toBeGreaterThanOrEqual(floor)
    }
  })

  it('equality: every key set agrees across both sources (meaningful only because the floor row above ran)', () => {
    const goSource = readFileSync(AUDIT_GO_PATH, 'utf8')
    const tsSource = readFileSync(AUDIT_TS_PATH, 'utf8')

    for (const { go, ts } of AUDIT_WIRE_STRUCTS) {
      expect(
        keySetDiff(goStructKeys(goSource, go), tsInterfaceKeys(tsSource, ts)),
        `${ts}: Go ${go} vs client.ts`,
      ).toEqual([])
    }
  })

  it('planted-positive: the comparator can report a real mismatch, not merely agree', () => {
    // Synthetic, in memory: 'b' is on the Go side and deliberately absent from the TS side.
    const goFixture = 'type Fixture struct {\n\tA string `json:"a"`\n\tB string `json:"b"`\n}'
    const tsFixtureMissingB = 'export interface Fixture {\n  a: string\n}'

    expect(goStructKeys(goFixture, 'Fixture')).toEqual(['a', 'b'])
    expect(tsInterfaceKeys(tsFixtureMissingB, 'Fixture')).toEqual(['a'])
    expect(keySetDiff(goStructKeys(goFixture, 'Fixture'), tsInterfaceKeys(tsFixtureMissingB, 'Fixture'))).toEqual(['b'])
  })
})
