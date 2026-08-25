// RED specs (AUDIT-09-01, Mode A) -- pin stripNodes before the executor fills in its
// body. invoiceStrip.ts is a stub that throws 'not implemented', so every case below
// fails on that throw: the correct red reason, not an import or compile error.
//
// The source of truth is .ralph/AUDIT-09-01-arch.md, which overrides the story's Test
// Specs table in eight places (its §7). Section references below point at that file.
//
// TIMEZONE: no TZ is pinned in this repo (vitest.config.ts is three lines; no playwright
// config sets one). Every timestamp here is OFFSET-LESS ('2026-07-01T14:32:07'), which
// ECMA-262 parses as LOCAL time, so fmtTime's local getHours()/getMinutes() round-trip it
// exactly in every timezone. A 'Z'-suffixed input has no timezone-stable HH:MM -- never
// assert one in this file.
import { describe, expect, it } from 'vitest'

import { APP_PERSONAS } from '../auth'
import { stripNodes, type StripNode, type StripState } from './invoiceStrip'
import type { ApprovalRun } from './approvals'
import type { InvoiceStatus, StatusChange } from './invoices'

const KEYS = ['draft', 'validated', 'approved', 'queued', 'accepted'] as const

const T_DRAFT = '2026-07-01T09:00:00' // 09:00
const T_VALIDATED = '2026-07-01T10:15:00' // 10:15
const T_QUEUED = '2026-07-01T11:30:00' // 11:30
const T_CLOSED = '2026-07-01T13:05:00' // 13:05
const T_TERMINAL = '2026-07-01T14:32:07' // 14:32

// A real APP_PERSONAS subject from the OTHER tenant. The leak regression below is
// vacuous unless this subject actually resolves to a name -- S-10 asserts that first.
const OTHER_TENANT_SUBJECT = APP_PERSONAS.inhouse.subject

function h(to: InvoiceStatus, changed_at: string, over: Partial<StatusChange> = {}): StatusChange {
  return {
    from_status: null,
    to_status: to,
    actor: APP_PERSONAS.firm.subject,
    actor_name: 'Ada Lovelace',
    actor_kind: 'person',
    changed_at,
    ...over,
  }
}

function mkRun(state: string, over: Partial<ApprovalRun> = {}): ApprovalRun {
  return {
    run_id: 'run-1',
    state,
    opened_at: T_VALIDATED,
    closed_at: null,
    closed_by: null,
    steps: [],
    decisions: [],
    ...over,
  }
}

// Every case goes through this, so §6.11's fixed key order and the always-five arity are
// asserted for free on every input in the file.
function strip(history: StatusChange[], run: ApprovalRun | null, status: InvoiceStatus): StripNode[] {
  const nodes = stripNodes(history, run, status)
  expect(nodes.map((n) => n.key)).toEqual([...KEYS])
  return nodes
}

const HISTORY_TO_QUEUED: StatusChange[] = [
  h('draft', T_DRAFT),
  h('validated', T_VALIDATED, { from_status: 'draft' }),
  h('queued', T_QUEUED, { from_status: 'validated' }),
]

// ---------------------------------------------------------------------------
// Shape and totality
// ---------------------------------------------------------------------------

describe('stripNodes: shape and totality', () => {
  it('S-1 (invoiceStrip_alwaysFiveNodes): every history x run x status combination yields five fully-populated nodes', () => {
    const histories: Array<[string, StatusChange[]]> = [
      ['empty', []],
      ['genesis', [h('draft', T_DRAFT)]],
      ['toQueued', HISTORY_TO_QUEUED],
    ]
    const runs: Array<[string, ApprovalRun | null]> = [
      ['null', null],
      ['open', mkRun('open')],
      ['approved', mkRun('approved', { closed_at: T_CLOSED, closed_by: OTHER_TENANT_SUBJECT })],
      ['rejected', mkRun('rejected', { closed_at: T_CLOSED, closed_by: OTHER_TENANT_SUBJECT })],
      ['cancelled', mkRun('cancelled', { closed_at: T_CLOSED })],
    ]
    const statuses = Object.keys(STATUS_TABLE) as InvoiceStatus[]
    const allowed: StripState[] = ['done', 'current', 'failed', 'unreached', 'not-required']

    let cases = 0
    for (const [hName, history] of histories) {
      for (const [rName, run] of runs) {
        for (const status of statuses) {
          const where = `${hName}/${rName}/${status}`
          const nodes = strip(history, run, status)
          expect(nodes, where).toHaveLength(5)
          for (const n of nodes) {
            // Non-vacuous with respect to node 3: a mapper that forgot it cannot satisfy
            // a non-empty label AND a non-empty caption on all five.
            expect(allowed, `${where} ${n.key}`).toContain(n.state)
            expect(n.label.length, `${where} ${n.key} label`).toBeGreaterThan(0)
            expect(n.caption.length, `${where} ${n.key} caption`).toBeGreaterThan(0)
            expect(n.at === null || typeof n.at === 'string', `${where} ${n.key} at`).toBe(true)
            if (n.actor !== null) expect(typeof n.actor.text, `${where} ${n.key} actor`).toBe('string')
          }
          cases += 1
        }
      }
    }
    expect(cases).toBe(105) // 3 histories x 5 runs x 7 statuses -- the sweep is not empty
  })
})

// ---------------------------------------------------------------------------
// Nodes 1/2/4/5: driven by `status` alone (arch §3e)
// ---------------------------------------------------------------------------

interface StatusRow {
  cursor: 1 | 2 | 4 | 5
  n1: StripState
  n2: StripState
  n4: StripState
  n5: StripState
  n5label: string
}

// A Record<InvoiceStatus, _> so TypeScript fails the build if a status is ever added and
// no row is written for it. Transcribed from arch §3e's table.
const STATUS_TABLE: Record<InvoiceStatus, StatusRow> = {
  draft: { cursor: 1, n1: 'current', n2: 'unreached', n4: 'unreached', n5: 'unreached', n5label: 'Accepted by FIRS' },
  validated: { cursor: 2, n1: 'done', n2: 'current', n4: 'unreached', n5: 'unreached', n5label: 'Accepted by FIRS' },
  queued: { cursor: 4, n1: 'done', n2: 'done', n4: 'current', n5: 'unreached', n5label: 'Accepted by FIRS' },
  submitted: { cursor: 4, n1: 'done', n2: 'done', n4: 'current', n5: 'unreached', n5label: 'Accepted by FIRS' },
  accepted: { cursor: 5, n1: 'done', n2: 'done', n4: 'done', n5: 'done', n5label: 'Accepted by FIRS' },
  rejected: { cursor: 5, n1: 'done', n2: 'done', n4: 'done', n5: 'failed', n5label: 'Rejected by FIRS' },
  failed: { cursor: 5, n1: 'done', n2: 'done', n4: 'done', n5: 'failed', n5label: 'Transmission failed' },
}

describe('stripNodes: nodes 1/2/4/5 follow status, arch §3e', () => {
  it('S-2: each of the seven statuses lands its cursor, and state/label are identical with and without history', () => {
    const rows = Object.entries(STATUS_TABLE) as Array<[InvoiceStatus, StatusRow]>
    expect(rows).toHaveLength(7)
    expect(rows.length).toBeGreaterThan(0)

    for (const [status, row] of rows) {
      // Same table, two histories: the arch's central invariant is that history supplies
      // only `at`/`actor`, so state and label must not move between these two runs.
      const histories: Array<[string, StatusChange[]]> = [
        ['empty', []],
        ['toQueued', HISTORY_TO_QUEUED],
      ]
      for (const [hName, history] of histories) {
        const n = strip(history, null, status)
        const where = `${status}/${hName}`
        expect(n[0].state, `${where} n1`).toBe(row.n1)
        expect(n[1].state, `${where} n2`).toBe(row.n2)
        expect(n[3].state, `${where} n4`).toBe(row.n4)
        expect(n[4].state, `${where} n5`).toBe(row.n5)
        expect(n[0].label, `${where} n1`).toBe('Draft')
        expect(n[1].label, `${where} n2`).toBe('Validated')
        expect(n[2].label, `${where} n3`).toBe('Approved') // node 3 never relabels, §3b
        expect(n[3].label, `${where} n4`).toBe('Queued')
        expect(n[4].label, `${where} n5`).toBe(row.n5label)
      }
    }
  })

  it('S-3 (invoiceStrip_totalOverEveryStatus): progress over nodes 1/2/4/5 is monotone with at most one current', () => {
    // AMENDED from the story (arch §7 row 4): "exactly one node is current" is FALSE
    // across all five nodes. APPROVALS_ENFORCED is off in production, so an open run on a
    // queued invoice legitimately leaves node 3 AND node 4 current (see S-16). The
    // invariant is scoped to nodes {1,2,4,5}; node 3 is exempt by construction.
    const rank: Record<StripState, number> = {
      done: 0,
      current: 1,
      failed: 1,
      unreached: 2,
      'not-required': -1, // must never appear on 1/2/4/5
    }
    const statuses = Object.keys(STATUS_TABLE) as InvoiceStatus[]
    expect(statuses.length).toBeGreaterThan(0)

    for (const status of statuses) {
      const nodes = strip(HISTORY_TO_QUEUED, null, status)
      const spine = [nodes[0], nodes[1], nodes[3], nodes[4]]
      const states = spine.map((n) => n.state)

      expect(states, status).not.toContain('not-required')
      expect(states.filter((s) => s === 'current').length, `${status} currents`).toBeLessThanOrEqual(1)
      for (let i = 1; i < spine.length; i++) {
        // No unreached node sits behind a reached one.
        expect(rank[states[i]], `${status} ${spine[i].key} after ${spine[i - 1].key}`).toBeGreaterThanOrEqual(
          rank[states[i - 1]],
        )
      }
    }
  })

  it('S-4 (invoiceStrip_submittedSharesTheQueuedNode): submitted is current on node 4 and captions Submitted, not Waiting', () => {
    const nodes = strip(HISTORY_TO_QUEUED, null, 'submitted')
    expect(nodes[3].state).toBe('current')
    expect(nodes[3].caption).toBe('Submitted')
    expect(nodes[4].state).toBe('unreached')
    expect(nodes[4].caption).toBe('Not reached')

    // The sibling status shares the node but not the caption (arch §3d, P-10).
    expect(strip(HISTORY_TO_QUEUED, null, 'queued')[3].caption).toBe('Waiting')
  })

  it('S-5 (invoiceStrip_terminalRelabels): node 5 relabels and reddens on failed and on rejected', () => {
    const history = [...HISTORY_TO_QUEUED, h('failed', T_TERMINAL, { from_status: 'queued' })]
    const failed = strip(history, null, 'failed')
    expect(failed[4].label).toBe('Transmission failed')
    expect(failed[4].state).toBe('failed')
    expect(failed[4].at).toBe(T_TERMINAL)
    expect(failed[4].caption).toBe('14:32 · Ada')

    const rejected = strip(
      [...HISTORY_TO_QUEUED, h('rejected', T_TERMINAL, { from_status: 'queued' })],
      null,
      'rejected',
    )
    expect(rejected[4].label).toBe('Rejected by FIRS')
    expect(rejected[4].state).toBe('failed')

    const accepted = strip(
      [...HISTORY_TO_QUEUED, h('accepted', T_TERMINAL, { from_status: 'queued' })],
      null,
      'accepted',
    )
    expect(accepted[4].label).toBe('Accepted by FIRS')
    expect(accepted[4].state).toBe('done')
  })
})

// ---------------------------------------------------------------------------
// Node 3: driven by `run` alone (arch §3c)
// ---------------------------------------------------------------------------

interface RunRow {
  name: string
  run: ApprovalRun | null
  status: InvoiceStatus
  state: StripState
  at: string | null
  // §3c's actor column: 'system' resolves through actorLabel('system'), every other
  // closer resolves to null. Never a name -- see S-10.
  actorText: string | null
  caption: string
}

const RUN_TABLE: RunRow[] = [
  { name: 'null run, cursor 1', run: null, status: 'draft', state: 'unreached', at: null, actorText: null, caption: 'Not reached' },
  { name: 'null run, cursor 2', run: null, status: 'validated', state: 'unreached', at: null, actorText: null, caption: 'Not reached' },
  { name: 'null run, cursor 4', run: null, status: 'queued', state: 'not-required', at: null, actorText: null, caption: 'Not required' },
  { name: 'null run, cursor 5', run: null, status: 'accepted', state: 'not-required', at: null, actorText: null, caption: 'Not required' },
  { name: 'open', run: mkRun('open'), status: 'validated', state: 'current', at: null, actorText: null, caption: 'Waiting' },
  {
    name: 'approved by a person',
    run: mkRun('approved', { closed_at: T_CLOSED, closed_by: OTHER_TENANT_SUBJECT }),
    status: 'queued',
    state: 'done',
    at: T_CLOSED,
    actorText: null, // §3c: a non-'system' closer resolves to null, never a name
    caption: '13:05',
  },
  {
    name: 'rejected by a person',
    run: mkRun('rejected', { closed_at: T_CLOSED, closed_by: OTHER_TENANT_SUBJECT }),
    status: 'draft',
    state: 'failed',
    at: T_CLOSED,
    actorText: null,
    caption: '13:05',
  },
  {
    name: 'approved by the engine',
    run: mkRun('approved', { closed_at: T_CLOSED, closed_by: 'system' }),
    status: 'queued',
    state: 'done',
    at: T_CLOSED,
    actorText: 'System',
    caption: '13:05 · System',
  },
  {
    name: 'cancelled',
    run: mkRun('cancelled', { closed_at: T_CLOSED, closed_by: OTHER_TENANT_SUBJECT }),
    status: 'draft',
    state: 'not-required',
    at: null, // §3c: closed_at's meaning flips on a cancelled run, so it is not rendered
    actorText: null,
    caption: 'Approval voided',
  },
]

describe('stripNodes: node 3 follows the approval run, arch §3c', () => {
  it('S-6: every run state maps node 3 exactly, and never relabels it', () => {
    expect(RUN_TABLE.length).toBeGreaterThan(0)
    expect(RUN_TABLE).toHaveLength(9) // 5 run states; null split by cursor, approved split by closer

    for (const row of RUN_TABLE) {
      const node = strip(HISTORY_TO_QUEUED, row.run, row.status)[2]
      expect(node.key, row.name).toBe('approved')
      expect(node.label, row.name).toBe('Approved')
      expect(node.state, row.name).toBe(row.state)
      expect(node.at, row.name).toBe(row.at)
      expect(node.actor?.text ?? null, row.name).toBe(row.actorText)
      expect(node.caption, row.name).toBe(row.caption)
    }
  })

  it('S-7 (invoiceStrip_noRunReadsNotRequiredPastValidated): a null run reads Not required past validated and Not reached before it', () => {
    const past = strip(HISTORY_TO_QUEUED, null, 'queued')[2]
    expect(past.state).toBe('not-required')
    expect(past.caption).toBe('Not required')

    const before = strip([h('draft', T_DRAFT)], null, 'draft')[2]
    expect(before.state).toBe('unreached')
    expect(before.caption).toBe('Not reached')
  })

  it('S-8 (invoiceStrip_firsRejectionNeverRedensApproved): a FIRS rejection reddens node 5 only', () => {
    const history = [...HISTORY_TO_QUEUED, h('rejected', T_TERMINAL, { from_status: 'queued' })]
    const run = mkRun('approved', { closed_at: T_CLOSED, closed_by: 'system' })
    const nodes = strip(history, run, 'rejected')

    expect(nodes[2].state).toBe('done')
    expect(nodes[2].label).toBe('Approved')
    expect(nodes[4].state).toBe('failed')
    expect(nodes[4].label).toBe('Rejected by FIRS')
  })

  it('S-9 (invoiceStrip_approverRejectionRedensApprovedOnly): an approver rejection reddens node 3 only', () => {
    const nodes = strip([h('draft', T_DRAFT)], mkRun('rejected', { closed_at: T_CLOSED }), 'draft')
    expect(nodes[2].state).toBe('failed')
    expect(nodes[0].state).toBe('current')
    expect(nodes[4].state).toBe('unreached')
    expect(nodes[4].label).toBe('Accepted by FIRS')

    // Both red at once is legal and honest when the two causes are unrelated (arch §3f).
    const both = strip(
      [...HISTORY_TO_QUEUED, h('rejected', T_TERMINAL, { from_status: 'queued' })],
      mkRun('rejected', { closed_at: T_CLOSED }),
      'rejected',
    )
    expect(both[2].state).toBe('failed')
    expect(both[4].state).toBe('failed')
  })

  it('S-10 (arch §6.3): node 3 never carries a person name -- the cross-tenant leak regression', () => {
    // Nothing else in this file stops an executor writing actorLabel(run.closed_by).
    // GET /v1/invoices/{id}/approval ships no resolved actor pair (read_model.go:53-62),
    // so the unresolved rung falls through to APP_PERSONAS -- which holds BOTH tenants
    // unscoped and would print the other tenant's admin here.
    const persona = APP_PERSONAS.inhouse
    expect(persona.subject, 'fixture guard').toBe(OTHER_TENANT_SUBJECT)
    expect(persona.name).toBe('Ngozi Balogun')
    expect(persona.org).toBe('Honeywell Group')

    const nodes = strip(
      HISTORY_TO_QUEUED,
      mkRun('approved', { closed_at: T_CLOSED, closed_by: OTHER_TENANT_SUBJECT }),
      'queued',
    )
    const node = nodes[2]
    expect(node.actor).toBeNull()
    expect(node.caption).toBe('13:05')
    expect(node.caption).not.toContain(persona.name)
    expect(node.caption).not.toContain(persona.org)
    expect(node.caption).not.toContain('Ngozi')
    expect(node.caption).not.toContain('Honeywell')
    // Nor the bare subject, which is what a raw actorLabel() fall-through would print
    // for any closer who is not in the persona table.
    expect(node.caption).not.toContain(OTHER_TENANT_SUBJECT)
  })

  it('S-11 (arch §6.4): an auto-approved run names System', () => {
    const nodes = strip(HISTORY_TO_QUEUED, mkRun('approved', { closed_at: T_CLOSED, closed_by: 'system' }), 'queued')
    const node = nodes[2]
    expect(node.state).toBe('done')
    expect(node.at).toBe(T_CLOSED)
    expect(node.actor).toEqual({ text: 'System', mono: false, kind: 'system' })
    expect(node.caption).toBe('13:05 · System')
  })

  it('S-12 (arch §6.5): a cancelled-after-approved run carries no timestamp', () => {
    const nodes = strip(
      HISTORY_TO_QUEUED,
      mkRun('cancelled', { closed_at: T_CLOSED, closed_by: 'system' }),
      'validated',
    )
    const node = nodes[2]
    expect(node.state).toBe('not-required')
    expect(node.at).toBeNull()
    expect(node.actor).toBeNull()
    expect(node.caption).toBe('Approval voided')
    expect(node.caption).not.toContain('13:05')
  })

  it('S-13: a cancelled run on a demoted draft still reads Approval voided, not Not reached', () => {
    // The production-dominant cancelled case: CancelLiveRunTx voids the run on every path
    // back to draft (engine.go:261-271). Node 3 follows the run, not the cursor (§3e).
    const nodes = strip([h('draft', T_DRAFT)], mkRun('cancelled', { closed_at: T_CLOSED }), 'draft')
    expect(nodes[2].state).toBe('not-required')
    expect(nodes[2].caption).toBe('Approval voided')
    expect(nodes[0].state).toBe('current')
  })

  it('S-14 (arch §6.9): an unknown run state degrades to current with its own raw label, and does not throw', () => {
    // Unreachable through the DB CHECK, but ApprovalRun.state is `string` on the wire
    // (approvals.ts:311), so the default branch is mandatory under strict.
    const nodes = strip(HISTORY_TO_QUEUED, mkRun('weird'), 'validated')
    expect(nodes[2].state).toBe('current')
    expect(nodes[2].caption).toBe('weird')
    expect(nodes[2].at).toBeNull()
    expect(nodes[2].actor).toBeNull()
  })

  it('S-15: an approved run with no close time captions the em-dash rather than a bogus time', () => {
    // Derived from arch §3d (`attribution ?? "—"`) plus ApprovalRun.closed_at being
    // `string | null`; not one of §6's enumerated cases.
    const nodes = strip(HISTORY_TO_QUEUED, mkRun('approved', { closed_at: null, closed_by: 'system' }), 'queued')
    expect(nodes[2].state).toBe('done')
    expect(nodes[2].at).toBeNull()
    expect(nodes[2].caption).toBe('—')
  })
})

// ---------------------------------------------------------------------------
// History supplies `at` and `actor` only (arch §4)
// ---------------------------------------------------------------------------

describe('stripNodes: history supplies at and actor only, arch §4', () => {
  // The 6-transition journey from e2e/topology/invoice-surfaces.spec.ts:1040-1083,
  // ending at status 'validated'.
  const LOOP: StatusChange[] = [
    h('draft', '2026-07-01T09:00:00'),
    h('validated', '2026-07-01T09:30:00', { from_status: 'draft' }),
    h('queued', '2026-07-01T10:00:00', { from_status: 'validated' }),
    h('rejected', '2026-07-01T10:30:00', { from_status: 'queued' }),
    h('draft', '2026-07-01T11:00:00', { from_status: 'rejected', actor_name: 'Bola Adeyemi' }),
    h('validated', '2026-07-01T11:45:00', { from_status: 'draft', actor_name: 'Chidi Nwosu' }),
  ]

  it('S-16 (invoiceStrip_loopKeepsFiveNodesAndLatestActor): the loop keeps five nodes and takes the latest row per node', () => {
    // AMENDED from the story (arch §7 row 5): assert the `at`/`actor` FIELDS. Node 2 is
    // `current` at the end of this journey, so its caption is 'Waiting', not the time.
    const nodes = strip(LOOP, mkRun('cancelled', { closed_at: '2026-07-01T10:30:00' }), 'validated')

    expect(nodes[0].state).toBe('done')
    expect(nodes[0].at).toBe('2026-07-01T11:00:00') // row 5, NOT the 09:00 genesis
    expect(nodes[0].actor?.text).toBe('Bola Adeyemi')
    expect(nodes[0].caption).toBe('11:00 · Bola')

    expect(nodes[1].state).toBe('current')
    expect(nodes[1].at).toBe('2026-07-01T11:45:00') // row 6, NOT the 09:30 first validation
    expect(nodes[1].actor?.text).toBe('Chidi Nwosu')
    expect(nodes[1].caption).toBe('Waiting') // a current node never renders its time

    // Node 4 saw a queueing at 10:00 and node 5 a rejection at 10:30, but the cursor is
    // back at 2, so both are demoted with no residue (arch §6.8).
    expect(nodes[3].state).toBe('unreached')
    expect(nodes[3].at).toBeNull()
    expect(nodes[3].actor).toBeNull()
    expect(nodes[3].caption).toBe('Not reached')

    expect(nodes[4].state).toBe('unreached')
    expect(nodes[4].label).toBe('Accepted by FIRS') // no red left over from the rejection
    expect(nodes[4].at).toBeNull()
    expect(nodes[4].actor).toBeNull()
  })

  it('S-17 (arch §6.1): history is consumed in array order and never re-sorted', () => {
    // The server already ordered by (changed_at ASC, id ASC) and `id` is not on the wire
    // (store.go:102), so two rows written in one transaction share a changed_at and a
    // client-side sort is strictly less correct. Feed a descending array: the LAST
    // element must win even though it is the older timestamp.
    const descending: StatusChange[] = [
      h('draft', '2026-07-01T12:00:00', { actor_name: 'Later Row' }),
      h('draft', '2026-07-01T08:00:00', { actor_name: 'Last Element' }),
    ]
    const nodes = strip(descending, null, 'validated')
    expect(nodes[0].at).toBe('2026-07-01T08:00:00')
    expect(nodes[0].actor?.text).toBe('Last Element')
    expect(nodes[0].caption).toBe('08:00 · Last')
  })

  it('S-18: nodes 1, 2 and 4 all take the LATEST matching row, not the first', () => {
    // AMENDED from the story's System Design table (arch §7 row 2 / §4): "first
    // transition in" is wrong for node 4 on a re-queue.
    const history: StatusChange[] = [
      h('draft', '2026-07-01T08:00:00', { actor_name: 'First Draft' }),
      h('validated', '2026-07-01T08:30:00', { actor_name: 'First Validate' }),
      h('queued', '2026-07-01T09:00:00', { actor_name: 'First Queue' }),
      h('draft', '2026-07-01T09:30:00', { actor_name: 'Second Draft' }),
      h('validated', '2026-07-01T10:00:00', { actor_name: 'Second Validate' }),
      h('queued', '2026-07-01T10:30:00', { actor_name: 'Second Queue' }),
      h('accepted', '2026-07-01T11:00:00', { actor_name: 'Only Accept' }),
    ]
    const nodes = strip(history, null, 'accepted')
    expect(nodes[0].actor?.text).toBe('Second Draft')
    expect(nodes[1].actor?.text).toBe('Second Validate')
    expect(nodes[3].actor?.text).toBe('Second Queue')
    expect(nodes[4].actor?.text).toBe('Only Accept')
    expect(nodes[0].at).toBe('2026-07-01T09:30:00')
    expect(nodes[3].at).toBe('2026-07-01T10:30:00')
  })

  it('S-19: node 4 pools queued and submitted, node 5 pools accepted, rejected and failed', () => {
    const submitted = strip(
      [...HISTORY_TO_QUEUED, h('submitted', T_TERMINAL, { from_status: 'queued' })],
      null,
      'accepted',
    )
    expect(submitted[3].at).toBe(T_TERMINAL) // the submitted row, not the earlier queued one

    for (const terminal of ['accepted', 'rejected', 'failed'] as const) {
      const nodes = strip([...HISTORY_TO_QUEUED, h(terminal, T_TERMINAL, { from_status: 'queued' })], null, terminal)
      expect(nodes[4].at, terminal).toBe(T_TERMINAL)
      expect(nodes[4].actor?.text, terminal).toBe('Ada Lovelace')
    }
  })

  it('S-20 (arch §6.10): a current node captions Waiting even when its history row is populated', () => {
    const nodes = strip([h('draft', T_DRAFT)], mkRun('rejected', { closed_at: T_CLOSED }), 'draft')
    expect(nodes[0].state).toBe('current')
    expect(nodes[0].at).toBe(T_DRAFT) // populated...
    expect(nodes[0].actor?.text).toBe('Ada Lovelace')
    expect(nodes[0].caption).toBe('Waiting') // ...but not rendered
    expect(nodes[0].caption).not.toContain('09:00')
    expect(nodes[0].caption).not.toContain('Ada')
  })

  it('S-21: an empty history leaves every reached node on the em-dash, with no throw', () => {
    // Unreachable in production (store.go:558-560 404s on zero rows) but the mapper is
    // total, and the SPA renders before /history resolves.
    const nodes = strip([], null, 'accepted')
    expect(nodes[0].state).toBe('done')
    expect(nodes[0].at).toBeNull()
    expect(nodes[0].actor).toBeNull()
    expect(nodes[0].caption).toBe('—')
    expect(nodes[4].state).toBe('done')
    expect(nodes[4].caption).toBe('—')
    expect(nodes[2].caption).toBe('Not required')
  })
})

// ---------------------------------------------------------------------------
// Actor rendering (arch §3d, display())
// ---------------------------------------------------------------------------

describe('stripNodes: actor rendering, arch §3d', () => {
  const withActor = (over: Partial<StatusChange>): StripNode[] =>
    strip([h('draft', T_TERMINAL, over), h('validated', T_TERMINAL, over)], null, 'queued')

  it('S-22 (invoiceStrip_rawSubjectIsNotFirstNamed): a raw subject renders byte-for-byte and mono', () => {
    // AMENDED from the story (arch §7 row 6): actor_name is set to the SUBJECT, because
    // that is the branch production takes (resolve.go:60 -> actor.ts:26). Note this
    // subject is deliberately hyphen-free, so it matches no APP_PERSONAS entry.
    const subject = 'c0000000000000000000000000000002'
    const nodes = withActor({ actor: subject, actor_name: subject, actor_kind: 'raw' })
    expect(nodes[0].actor).toEqual({ text: subject, mono: true, kind: 'raw' })
    expect(nodes[0].caption).toBe(`14:32 · ${subject}`)
    expect(nodes[0].caption).toContain(subject)
  })

  it('S-23: a multi-word raw label is not first-named -- display() gates on kind, not on whitespace', () => {
    const nodes = withActor({ actor: 'backfill', actor_name: 'nightly backfill job', actor_kind: 'raw' })
    expect(nodes[0].actor?.kind).toBe('raw')
    expect(nodes[0].caption).toBe('14:32 · nightly backfill job')
    expect(nodes[0].caption).not.toBe('14:32 · nightly')
  })

  it('S-24 (invoiceStrip_systemIsNotFirstNamed): a system actor renders its full label, unsplit and not mono', () => {
    const nodes = withActor({ actor: 'system', actor_name: 'System', actor_kind: 'system' })
    expect(nodes[0].actor).toEqual({ text: 'System', mono: false, kind: 'system' })
    expect(nodes[0].caption).toBe('14:32 · System')

    const worker = withActor({ actor: 'system', actor_name: 'Submission worker', actor_kind: 'system' })
    expect(worker[0].caption).toBe('14:32 · Submission worker')
  })

  it('S-25 (arch §6.6): an empty actor_name degrades to the raw subject even when actor_kind says person', () => {
    // The case that breaks if the mapper reads row.actor_kind instead of the RESOLVED
    // ActorLabel.kind (arch §0.3 / §7 row 3): a first-name reduction here would print
    // 'c0000000-0000-0000-0000-000000000001' split on nothing, or worse, guess a name.
    const subject = APP_PERSONAS.firm.subject
    const nodes = withActor({ actor: subject, actor_name: '', actor_kind: 'person' })
    expect(nodes[0].actor).toEqual({ text: subject, mono: true, kind: 'raw' })
    expect(nodes[0].caption).toBe(`14:32 · ${subject}`)
    expect(nodes[0].caption).not.toContain('Chinedu')
  })

  it('S-26 (arch §6.7): first-name reduction handles multi-token, single-token, leading-space and email names', () => {
    expect(withActor({ actor_name: 'Ada Lovelace' })[0].caption).toBe('14:32 · Ada')
    expect(withActor({ actor_name: 'Ada' })[0].caption).toBe('14:32 · Ada')
    expect(withActor({ actor_name: ' Ada Lovelace' })[0].caption).toBe('14:32 · Ada')
    // internal/actor/actor.go:38-40 returns the email rung with KindPerson; it has no
    // whitespace, so it survives whole.
    expect(withActor({ actor_name: 'n.balogun@honeywell.ng' })[0].caption).toBe('14:32 · n.balogun@honeywell.ng')
  })
})
