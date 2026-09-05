// Specs for stripNodes. S-1..S-26 were written RED against a throwing stub (Mode A);
// S-27..S-31 are the Mode B adversarial pass. Every spec below is proven killable by a
// source mutation -- see the QA report on task-672.
//
// The source of truth is .ralph/AUDIT-09-01-arch.md, which overrides the story's Test
// Specs table in eight places (its §7). Section references below point at that file.
//
// TIMEZONE: no TZ is pinned in this repo (vitest.config.ts is three lines; no playwright
// config sets one). Every timestamp here is OFFSET-LESS ('2026-07-01T14:32:07'), which
// ECMA-262 parses as LOCAL time, so fmtTime's local getHours()/getMinutes() round-trip it
// exactly in every timezone. A 'Z'-suffixed input has no timezone-stable HH:MM -- never
// assert one in this file.
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { basename, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
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

// A transmission failure, then a re-draft and a revalidation. S-2's claim that state and
// label never move with history is VACUOUS for node 5 without this: HISTORY_TO_QUEUED has
// no terminal row at all, so a node 5 that wrongly read history would still agree with one
// that read status. Here the LAST terminal row is red while the cursor has moved on.
const HISTORY_AFTER_FAILURE_LOOP: StatusChange[] = [
  h('draft', '2026-07-01T09:00:00'),
  h('validated', '2026-07-01T10:15:00', { from_status: 'draft' }),
  h('queued', '2026-07-01T11:30:00', { from_status: 'validated' }),
  h('submitted', '2026-07-01T12:00:00', { from_status: 'queued' }),
  h('failed', '2026-07-01T12:30:00', { from_status: 'submitted' }),
  h('draft', '2026-07-01T13:00:00', { from_status: 'failed' }),
  h('validated', '2026-07-01T13:30:00', { from_status: 'draft' }),
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
        ['afterFailureLoop', HISTORY_AFTER_FAILURE_LOOP],
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

  it('S-4 (invoiceStrip_submittedSharesTheQueuedNode): submitted is current on node 4 and captions the queued row, exactly as queued does', () => {
    const nodes = strip(HISTORY_TO_QUEUED, null, 'submitted')
    expect(nodes[3].state).toBe('current')
    const submittedCaption = nodes[3].caption
    expect(submittedCaption).toBe('11:30 · Ada')
    expect(nodes[4].state).toBe('unreached')
    expect(nodes[4].caption).toBe('Not reached')

    // The sibling status shares the node AND the caption. The equality, not the two
    // literals, is what proves no arm here reads `status`.
    const queuedCaption = strip(HISTORY_TO_QUEUED, null, 'queued')[3].caption
    expect(queuedCaption).toBe('11:30 · Ada')
    expect(submittedCaption).toBe(queuedCaption)
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
    // `current` at the end of this journey, and still renders the row it holds.
    const nodes = strip(LOOP, mkRun('cancelled', { closed_at: '2026-07-01T10:30:00' }), 'validated')

    expect(nodes[0].state).toBe('done')
    expect(nodes[0].at).toBe('2026-07-01T11:00:00') // row 5, NOT the 09:00 genesis
    expect(nodes[0].actor?.text).toBe('Bola Adeyemi')
    expect(nodes[0].caption).toBe('11:00 · Bola')

    expect(nodes[1].state).toBe('current')
    expect(nodes[1].at).toBe('2026-07-01T11:45:00') // row 6, NOT the 09:30 first validation
    expect(nodes[1].actor?.text).toBe('Chidi Nwosu')
    expect(nodes[1].caption).toBe('11:45 · Chidi') // a current node renders its own row

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

  it('S-20 (retargeted): a current node renders the populated history row it holds', () => {
    const nodes = strip([h('draft', T_DRAFT)], mkRun('rejected', { closed_at: T_CLOSED }), 'draft')
    expect(nodes[0].state).toBe('current')
    expect(nodes[0].at).toBe(T_DRAFT) // populated...
    expect(nodes[0].actor?.text).toBe('Ada Lovelace')
    expect(nodes[0].caption).toBe('09:00 · Ada') // ...and rendered
    expect(nodes[0].caption).toContain('09:00')
    expect(nodes[0].caption).toContain('Ada')
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
// A reached node captions its attribution, whatever its state
// ---------------------------------------------------------------------------

describe('stripNodes: a reached node captions its attribution', () => {
  it('S-35: the node under the cursor captions its own time and actor, on every status', () => {
    // Cursor -> node index: draft 0, validated 1, queued and submitted 3.
    const cases: Array<[InvoiceStatus, number, string]> = [
      ['draft', 0, '09:00 · Ada'],
      ['validated', 1, '10:15 · Ada'],
      ['queued', 3, '11:30 · Ada'],
    ]
    expect(cases.length).toBeGreaterThan(0)

    for (const [status, idx, caption] of cases) {
      const nodes = strip(HISTORY_TO_QUEUED, null, status)
      expect(nodes[idx].state, status).toBe('current')
      expect(nodes[idx].at, status).not.toBeNull()
      expect(nodes[idx].caption, status).toBe(caption)
    }

    // Control: the `done` node beside the current one takes the SAME shape, so the
    // assertions above cannot be passing on a caption only `done` nodes ever receive.
    const atValidated = strip(HISTORY_TO_QUEUED, null, 'validated')
    expect(atValidated[0].state).toBe('done')
    expect(atValidated[0].caption).toBe('09:00 · Ada')

    // submitted shares node 4 with queued, and now shares its caption too.
    expect(strip(HISTORY_TO_QUEUED, null, 'submitted')[3].caption).toBe('11:30 · Ada')
  })

  it('S-36: with no row to show, the current node captions the em-dash', () => {
    const nodes = strip([], null, 'draft')
    expect(nodes[0].state).toBe('current')
    expect(nodes[0].at).toBeNull()
    expect(nodes[0].actor).toBeNull()
    expect(nodes[0].caption).toBe('—')

    // Control: the same cursor WITH a row captions the row, so the em-dash above is the
    // missing row and not a mapper that captions '—' on every current node.
    const withRow = strip([h('draft', T_DRAFT)], null, 'draft')
    expect(withRow[0].state).toBe('current')
    expect(withRow[0].caption).toBe('09:00 · Ada')
    expect(withRow[0].caption).not.toBe('—')
  })

  it('S-37: current and done stay distinct by state alone, once their captions agree', () => {
    // Both rows carry the same time and actor, so the captions are identical and `state`
    // is the only thing left telling the two nodes apart.
    const nodes = strip([h('draft', T_DRAFT), h('validated', T_DRAFT, { from_status: 'draft' })], null, 'validated')
    expect(nodes[0].state).toBe('done')
    expect(nodes[1].state).toBe('current')
    expect(nodes[0].state).not.toBe(nodes[1].state)
    expect(nodes[0].caption).toBe('09:00 · Ada')
    expect(nodes[1].caption).toBe(nodes[0].caption)

    // No sixth state was added to carry the difference the caption stopped carrying. The
    // Record fails the typecheck if StripState gains or loses a member.
    const STATES: Record<StripState, true> = {
      done: true,
      current: true,
      failed: true,
      unreached: true,
      'not-required': true,
    }
    expect(Object.keys(STATES)).toHaveLength(5)
    expect(Object.keys(STATES)).toContain(nodes[1].state)
  })

  it('S-38: node 5 and node 3 are outside the change', () => {
    for (const status of ['draft', 'validated', 'queued', 'submitted'] as const) {
      const node5 = strip(HISTORY_TO_QUEUED, null, status)[4]
      expect(node5.state, status).toBe('unreached')
      expect(node5.caption, status).toBe('Not reached')
    }

    // approvalNode owns its own captions; only the spine's `current` arm moves.
    expect(strip(HISTORY_TO_QUEUED, mkRun('open'), 'validated')[2].caption).toBe('Waiting')
    expect(strip(HISTORY_TO_QUEUED, null, 'queued')[2].caption).toBe('Not required')
    expect(strip(HISTORY_TO_QUEUED, mkRun('cancelled', { closed_at: T_CLOSED }), 'draft')[2].caption).toBe(
      'Approval voided',
    )

    // Anti-vacuity: 'Not reached' above proves nothing if every node captions it. Node 1
    // sits at or behind every cursor, so it is reached on every run x status shape.
    expect(ALL_RUNS).toHaveLength(11)
    expect(ALL_STATUSES).toHaveLength(7)
    let swept = 0
    for (const [rName, run] of ALL_RUNS) {
      for (const status of ALL_STATUSES) {
        const node1 = strip(HISTORY_TO_QUEUED, run, status)[0]
        expect(node1.state, `${rName}/${status}`).not.toBe('unreached')
        expect(node1.caption, `${rName}/${status}`).not.toBe('Not reached')
        swept += 1
      }
    }
    expect(swept).toBe(ALL_RUNS.length * ALL_STATUSES.length)
    expect(swept).toBe(77)
  })

  it('S-39: the two em-dash routes stay distinguishable on a current node', () => {
    // '—' means no row; '— · Ada' means a row whose changed_at will not parse. Both reach
    // the caption through the same cell, and only `current` is new here -- S-31 pins the
    // `done` node.
    const noRow = strip([], null, 'draft')[0]
    expect(noRow.state).toBe('current')
    expect(noRow.at).toBeNull()
    expect(noRow.caption).toBe('—')

    const badDate = strip([h('draft', 'not-a-date')], null, 'draft')[0]
    expect(badDate.state).toBe('current')
    expect(badDate.at).toBe('not-a-date') // reached: the string is present, just unrenderable
    expect(badDate.actor?.text).toBe('Ada Lovelace')
    expect(badDate.caption).toBe('— · Ada')
    expect(badDate.caption).not.toContain('NaN')
    expect(badDate.caption).not.toContain('Invalid')

    expect(badDate.caption).not.toBe(noRow.caption)
  })

  it('S-42: a current node captions exactly what the SAME node captions once done', () => {
    // The Core AC's "same shape a done node uses" as an equality, not a second literal:
    // one history, two statuses, the node's caption must not move when the cursor passes.
    const cases: Array<[number, InvoiceStatus, InvoiceStatus]> = [
      [0, 'draft', 'validated'],
      [1, 'validated', 'queued'],
      [3, 'queued', 'accepted'],
    ]
    expect(cases.length).toBeGreaterThan(0)

    for (const [idx, whileCurrent, whileDone] of cases) {
      const where = `node${idx} ${whileCurrent}->${whileDone}`
      const cur = strip(HISTORY_TO_QUEUED, null, whileCurrent)[idx]
      const done = strip(HISTORY_TO_QUEUED, null, whileDone)[idx]
      expect(cur.state, where).toBe('current')
      expect(done.state, where).toBe('done')
      expect(cur.caption, where).toBe(done.caption)
      // Anti-vacuity: an equality over two placeholders would pass too.
      expect(cur.caption, where).not.toBe('—')
      expect(cur.caption, where).not.toBe('Not reached')
      expect(cur.caption, where).toMatch(/^\d\d:\d\d · \S/)
    }
  })

  it('S-43: on the spine `at` and `actor` are null together, so no caption dangles its separator', () => {
    // S-27 sweeps actor => at over all five nodes and records that the converse is FALSE on
    // node 3. Scoped to the spine the converse holds, and it is what keeps `HH:MM · ` off
    // the screen now that a current node renders.
    const histories: Array<[string, StatusChange[]]> = [
      ['empty', []],
      ['toQueued', HISTORY_TO_QUEUED],
      ['afterFailureLoop', HISTORY_AFTER_FAILURE_LOOP],
      ['badDate', [h('draft', 'not-a-date'), h('validated', T_VALIDATED, { from_status: 'draft' })]],
    ]
    expect(histories.length).toBeGreaterThan(0)

    let checked = 0
    let bothSet = 0
    let bothNull = 0
    for (const [hName, history] of histories) {
      for (const [rName, run] of ALL_RUNS) {
        for (const status of ALL_STATUSES) {
          for (const idx of [0, 1, 3, 4]) {
            const n = strip(history, run, status)[idx]
            const where = `${hName}/${rName}/${status} ${n.key}`
            expect(n.at === null, where).toBe(n.actor === null)
            if (n.at === null) bothNull += 1
            else bothSet += 1
            expect(n.caption.endsWith(' · '), where).toBe(false)
            expect(n.caption.trim(), where).not.toBe('')
            checked += 1
          }
        }
      }
    }
    expect(checked).toBe(histories.length * ALL_RUNS.length * ALL_STATUSES.length * 4)
    expect(checked).toBe(1232)
    // Anti-vacuity: both arms of the biconditional are actually reached.
    expect(bothSet).toBeGreaterThan(0)
    expect(bothNull).toBeGreaterThan(0)
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

// ---------------------------------------------------------------------------
// QA Mode B (task-672): the shipped invariants, malformed input, and the scope fence.
// S-1..S-26 above are untouched.
// ---------------------------------------------------------------------------

const ALL_STATUSES = Object.keys(STATUS_TABLE) as InvoiceStatus[]

// Every run shape a consumer can be handed, including the two the arch calls type-legal
// but unenumerated (approved/rejected with no close time) and an unknown state.
const ALL_RUNS: Array<[string, ApprovalRun | null]> = [
  ['null', null],
  ['open', mkRun('open')],
  ['approved+time+system', mkRun('approved', { closed_at: T_CLOSED, closed_by: 'system' })],
  ['approved+time+person', mkRun('approved', { closed_at: T_CLOSED, closed_by: OTHER_TENANT_SUBJECT })],
  ['approved+notime+system', mkRun('approved', { closed_at: null, closed_by: 'system' })],
  ['approved+notime+null', mkRun('approved', { closed_at: null, closed_by: null })],
  ['rejected+time+system', mkRun('rejected', { closed_at: T_CLOSED, closed_by: 'system' })],
  ['rejected+notime+system', mkRun('rejected', { closed_at: null, closed_by: 'system' })],
  ['cancelled+time+system', mkRun('cancelled', { closed_at: T_CLOSED, closed_by: 'system' })],
  ['weird', mkRun('weird')],
  ['emptyState', mkRun('')],
]

describe('stripNodes: the invariants subtask 02 relies on (arch §12)', () => {
  const SWEEP_HISTORIES: Array<[string, StatusChange[]]> = [
    ['empty', []],
    ['toQueued', HISTORY_TO_QUEUED],
    ['afterFailureLoop', HISTORY_AFTER_FAILURE_LOOP],
  ]

  it('S-27 (arch §12 C-2): a node never carries an actor without a time', () => {
    // The renderer draws the actor chip off `actor` and the time off `at`, so an actor with
    // no time is a chip floating next to an em-dash. The regression that motivated C-2 lived
    // on ONE run shape (approved with closed_at null), which is why this sweeps.
    //
    // ONE-DIRECTIONAL, and arch §12 C-2's "populated together or not at all" over-claims it.
    // The converse is false BY DESIGN: a human-approved run gives node 3 `at` with a null
    // `actor`, because resolving closed_by would re-open the cross-tenant leak (S-10, S-29).
    // A renderer must handle a time with no actor; it never sees an actor with no time.
    expect(SWEEP_HISTORIES.length * ALL_RUNS.length * ALL_STATUSES.length).toBeGreaterThan(0)

    let checked = 0
    let withActor = 0
    let timeOnly = 0
    for (const [hName, history] of SWEEP_HISTORIES) {
      for (const [rName, run] of ALL_RUNS) {
        for (const status of ALL_STATUSES) {
          for (const n of strip(history, run, status)) {
            if (n.actor !== null) {
              expect(n.at, `${hName}/${rName}/${status} ${n.key}`).not.toBeNull()
              withActor += 1
            } else if (n.at !== null) {
              timeOnly += 1
            }
            checked += 1
          }
        }
      }
    }
    expect(checked).toBe(1155) // 3 x 11 x 7 x 5 -- the sweep is not empty
    // Anti-vacuity: the implication is trivially true on a node with no actor at all, so the
    // sweep must reach both arms.
    expect(withActor).toBeGreaterThan(0)
    expect(timeOnly).toBeGreaterThan(0) // the by-design case the over-claimed wording denies
  })

  it('S-28 (arch §12 C-2): an approved run with no close time carries no actor either', () => {
    // The regression: §3c gave node 3 its actor with no closed_at condition, so this input
    // produced caption '—' next to a populated System chip.
    const node = strip(HISTORY_TO_QUEUED, mkRun('approved', { closed_at: null, closed_by: 'system' }), 'queued')[2]
    expect(node.state).toBe('done')
    expect(node.at).toBeNull()
    expect(node.actor).toBeNull()
    expect(node.caption).toBe('—')

    // Control: the same run WITH a close time does populate both, so the assertion above
    // is not passing because node 3 never carries an actor at all.
    const withTime = strip(HISTORY_TO_QUEUED, mkRun('approved', { closed_at: T_CLOSED, closed_by: 'system' }), 'queued')[2]
    expect(withTime.at).toBe(T_CLOSED)
    expect(withTime.actor?.text).toBe('System')
  })

  it('S-29 (arch §6.3, swept): node 3 never names its closer, whoever closed it', () => {
    // S-10 pins the one persona subject. This sweeps every closer shape the wire can carry
    // -- including null, which actorLabel(null) answers with 'Not recorded', not null.
    const closers = [
      OTHER_TENANT_SUBJECT,
      APP_PERSONAS.firm.subject,
      APP_PERSONAS.inhouse.email,
      'c0000000000000000000000000000009',
      '',
      null,
    ]
    expect(closers.length).toBeGreaterThan(0)
    const forbidden = Object.values(APP_PERSONAS).flatMap((p) => [p.name, p.org, p.email, p.subject, p.initials])
    expect(forbidden.length).toBeGreaterThan(0)

    for (const state of ['approved', 'rejected'] as const) {
      for (const closed_by of closers) {
        const where = `${state}/${closed_by}`
        const node = strip(HISTORY_TO_QUEUED, mkRun(state, { closed_at: T_CLOSED, closed_by }), 'queued')[2]
        expect(node.actor, where).toBeNull()
        expect(node.caption, where).toBe('13:05')
        for (const needle of forbidden) expect(node.caption, `${where} ${needle}`).not.toContain(needle)
        expect(node.caption, where).not.toContain('Not recorded')
      }
    }

    // Control needle: the scan above can still find an actor -- 'system' produces one.
    const auto = strip(HISTORY_TO_QUEUED, mkRun('approved', { closed_at: T_CLOSED, closed_by: 'system' }), 'queued')[2]
    expect(auto.actor?.text).toBe('System')
    expect(auto.caption).toBe('13:05 · System')
  })
})

describe('stripNodes: totality over malformed input', () => {
  // The mapper is specified TOTAL. These shapes are all reachable from a wire the server
  // widened to `string` (ApprovalRun.state, StatusChange.actor_kind) or from a partial
  // response; a throw here blanks the whole detail page.
  const MALFORMED: Array<[string, StatusChange[], ApprovalRun | null]> = [
    ['changed_at is not a date', [h('draft', 'not-a-date'), h('validated', 'not-a-date')], null],
    ['changed_at is empty', [h('draft', ''), h('validated', '')], null],
    [
      'to_status outside InvoiceStatus',
      [h('draft', T_DRAFT), { ...h('draft', T_VALIDATED), to_status: 'approved' as InvoiceStatus }],
      null,
    ],
    ['a draft -> draft self-transition', [h('draft', T_DRAFT, { from_status: 'draft' })], null],
    ['run with no steps or decisions', [h('draft', T_DRAFT)], { ...mkRun('open'), steps: undefined, decisions: undefined } as unknown as ApprovalRun],
    ['run state is empty string', [h('draft', T_DRAFT)], mkRun('')],
    ['actor_kind is a nonsense string', [h('draft', T_DRAFT, { actor_kind: 'wat' })], null],
    ['actor_name is whitespace only', [h('draft', T_DRAFT, { actor_name: '   ' })], null],
  ]

  it('S-30: no malformed input throws, and every case still yields five captioned nodes', () => {
    expect(MALFORMED.length).toBeGreaterThan(0)
    let cases = 0
    for (const [name, history, run] of MALFORMED) {
      for (const status of ALL_STATUSES) {
        const where = `${name}/${status}`
        const nodes = strip(history, run, status)
        expect(nodes, where).toHaveLength(5)
        for (const n of nodes) {
          expect(typeof n.caption, `${where} ${n.key}`).toBe('string')
          if (n.actor !== null) expect(n.at, `${where} ${n.key} pairing`).not.toBeNull()
          expect(n.caption.length, `${where} ${n.key}`).toBeGreaterThan(0)
        }
        cases += 1
      }
    }
    expect(cases).toBe(MALFORMED.length * 7)
  })

  it('S-31: an unparseable changed_at degrades to the em-dash rather than a bogus clock', () => {
    // fmtTime guards NaN, so the attribution keeps the actor and drops the time. The node
    // stays reached -- `at` is a non-null string, it is just not renderable.
    const nodes = strip([h('draft', 'not-a-date')], null, 'validated')
    expect(nodes[0].state).toBe('done')
    expect(nodes[0].at).toBe('not-a-date')
    expect(nodes[0].caption).toBe('— · Ada')
    expect(nodes[0].caption).not.toContain('NaN')
    expect(nodes[0].caption).not.toContain('Invalid')
  })

  it('S-32: a to_status outside InvoiceStatus is ignored, never mapped onto a node', () => {
    // 'approved' is an approval-run state, not an invoice status (arch §3a). A history row
    // carrying it must not become node 3's source, nor displace node 2's row.
    const history: StatusChange[] = [
      h('draft', T_DRAFT),
      h('validated', T_VALIDATED, { from_status: 'draft', actor_name: 'Real Validator' }),
      { ...h('draft', T_TERMINAL), to_status: 'approved' as InvoiceStatus, actor_name: 'Phantom Row' },
    ]
    const nodes = strip(history, null, 'validated')
    expect(nodes[1].at).toBe(T_VALIDATED)
    expect(nodes[1].actor?.text).toBe('Real Validator')
    expect(nodes[2].at).toBeNull()
    expect(nodes[2].actor).toBeNull()
    for (const n of nodes) expect(n.caption).not.toContain('Phantom')
    // Control: the same helper DOES surface an actor name when the row is well-formed.
    expect(strip(history, null, 'draft')[0].actor?.text).toBe('Ada Lovelace')
  })

  it('S-40: no spine node ever captions "Waiting", on any history x run x status', () => {
    // The removed exception captioned a `current` spine node 'Waiting'. Node 3 (index 2) is
    // approvalNode's and still owns the word, so it is excluded -- and the counters below
    // prove the sweep reaches it anyway, so the prohibition discriminates.
    const SWEEP: Array<[string, StatusChange[]]> = [
      ['empty', []],
      ['toQueued', HISTORY_TO_QUEUED],
      ['afterFailureLoop', HISTORY_AFTER_FAILURE_LOOP],
      ...MALFORMED.map(([name, history]) => [name, history] as [string, StatusChange[]]),
    ]

    let cases = 0
    let node3Waiting = 0
    let currentWithAttribution = 0
    for (const [hName, history] of SWEEP) {
      for (const [rName, run] of ALL_RUNS) {
        for (const status of ALL_STATUSES) {
          const where = `${hName}/${rName}/${status}`
          const nodes = strip(history, run, status)
          // Node 0 sits at or behind every cursor, so the sweep is never all-unreached.
          expect(nodes[0].state, where).not.toBe('unreached')
          for (const idx of [0, 1, 3, 4]) {
            const n = nodes[idx]
            expect(n.caption, `${where} ${n.key}`).not.toContain('Waiting')
            if (n.state === 'current' && /^\d\d:\d\d/.test(n.caption)) currentWithAttribution += 1
          }
          if (nodes[2].caption === 'Waiting') node3Waiting += 1
          cases += 1
        }
      }
    }

    expect(cases).toBe(SWEEP.length * ALL_RUNS.length * ALL_STATUSES.length)
    expect(cases).toBeGreaterThanOrEqual(847) // floor: 11 histories x 11 runs x 7 statuses
    // Anti-vacuity: node 3 does say the word (so the scan is live), and a `current` spine
    // node does render an attribution (so the sweep reaches the state this story changed).
    expect(node3Waiting).toBeGreaterThan(0)
    expect(currentWithAttribution).toBeGreaterThan(0)
  })
})

describe('stripNodes: the default branch captions a blank run state', () => {
  it('S-33: an EMPTY run state falls back to the generic current caption, never a blank cell', () => {
    // approvalRunStateView('') returns its argument verbatim (approvals.ts:411), so the
    // default branch would otherwise caption the node with '' and the strip would draw a
    // blank cell (StatusStrip.test.tsx 'no node ever renders a visually empty caption').
    // Unreachable through the run_state CHECK, but so is 'weird' -- and the default branch
    // exists because the wire widens state to `string`.
    const node = strip(HISTORY_TO_QUEUED, mkRun(''), 'validated')[2]
    expect(node.state).toBe('current')
    expect(node.caption).toBe('Waiting')
    // Whitespace is as invisible as the empty string, so it takes the same fallback.
    expect(strip(HISTORY_TO_QUEUED, mkRun('   '), 'validated')[2].caption).toBe('Waiting')
    // Control: a non-empty unknown state is still echoed verbatim, so the fallback above is
    // about a blank label specifically, not about the default branch losing its state.
    expect(strip(HISTORY_TO_QUEUED, mkRun('weird'), 'validated')[2].caption).toBe('weird')
  })
})

describe('stripNodes: the scope fence (AC-7)', () => {
  // AC-7 has no runtime surface, so it is asserted mechanically. Idiom copied from
  // src/sourceDocumentScope.test.ts.
  const SRC_DIR = join(dirname(fileURLToPath(import.meta.url)), '..')

  function walk(dir: string): string[] {
    return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
      const full = join(dir, e.name)
      if (e.isDirectory()) return e.name === 'node_modules' ? [] : walk(full)
      return /\.tsx?$/.test(e.name) ? [full] : []
    })
  }

  function stripComments(src: string): string {
    return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/.*$/gm, '')
  }

  it('S-34: invoiceStrip has no barrel and no importer outside the strip and its mount', () => {
    expect(existsSync(join(SRC_DIR, 'lib', 'index.ts'))).toBe(false)

    const files = walk(SRC_DIR)
    expect(files.length).toBeGreaterThan(50) // vacuity floor: the walk actually found the tree

    // The module-specifier form only, so this file's own `it(...)` titles -- which contain
    // the bare word invoiceStrip -- cannot self-match.
    const importers = files
      .filter((f) => /(?:from|import\()\s*['"][^'"]*\/invoiceStrip['"]/.test(stripComments(readFileSync(f, 'utf8'))))
      .map((f) => basename(f))
      .sort()

    expect(importers).toContain('invoiceStrip.test.ts') // vacuity floor: the needle matches
    // InvoiceDetail.tsx is the ONE mount: it calls stripNodes and hands the result to
    // StatusStrip, which is a pure renderer and imports the types only.
    const ALLOWED = ['InvoiceDetail.tsx', 'StatusStrip.test.tsx', 'StatusStrip.tsx', 'invoiceStrip.test.ts']
    expect(importers.filter((f) => !ALLOWED.includes(f))).toEqual([])
  })

  it('S-41: spineNode carries no "Waiting" literal and no per-status caption branch', () => {
    // Scoped to spineNode's body deliberately: approvalNode owns the word legitimately, so
    // a whole-file grep would be satisfied by the wrong site and report a false pass.
    const count = (s: string, needle: string) => s.split(needle).length - 1

    // Brace-balanced body of a top-level function. '' when the name is gone -- the CONTAINS
    // floors below turn that into a loud failure instead of a silent zero.
    const fnBody = (s: string, name: string): string => {
      const start = s.indexOf(`function ${name}(`)
      const open = start < 0 ? -1 : s.indexOf('{', s.indexOf(')', start))
      if (open < 0) return ''
      let depth = 0
      for (let i = open; i < s.length; i++) {
        if (s[i] === '{') depth += 1
        else if (s[i] === '}' && --depth === 0) return s.slice(open + 1, i)
      }
      return ''
    }

    const bare = stripComments(readFileSync(join(SRC_DIR, 'lib', 'invoiceStrip.ts'), 'utf8'))
    const spine = fnBody(bare, 'spineNode')
    const approval = fnBody(bare, 'approvalNode')
    // The slice is real AND bounded: it holds what only spineNode has, and none of what
    // only approvalNode has.
    expect(spine).toContain("'Accepted by FIRS'")
    expect(spine).toContain('latestInto(history')
    expect(spine).not.toContain("'Approval voided'")
    expect(approval).toContain("'Approval voided'")

    // Planted hit: the same needle finds approvalNode's own literals, so a zero on the
    // spine is an absence and not a dead search.
    const approvalWaiting = count(approval, "'Waiting'")
    expect(approvalWaiting).toBeGreaterThanOrEqual(2)
    // Every 'Waiting' in the file is one of approvalNode's. Counted, not pinned: node 3 is
    // free to grow another one.
    expect(count(bare, "'Waiting'")).toBe(approvalWaiting)
    expect(count(spine, "'Waiting'")).toBe(0)

    // The caption is one expression with no per-status arm. The state and label ternaries
    // above it keep their own `status ===` and sit outside the slice.
    const capStart = spine.indexOf('const caption')
    const capEnd = spine.indexOf('return', capStart)
    expect(capStart).toBeGreaterThanOrEqual(0)
    expect(capEnd).toBeGreaterThan(capStart)
    const caption = spine.slice(capStart, capEnd)
    expect(caption).toContain('attribution(at, actor)')
    expect(caption).not.toContain('KEY_OF') // bounded: it stops before the return
    expect(caption).not.toMatch(/status\s*===/)
    expect(caption).not.toContain("'Waiting'")
    // Control: the same regex DOES match the state ternary the slice excludes.
    expect(spine.slice(0, capStart)).toMatch(/status\s*===/)

    // Scoped, not over-broad: one more 'Waiting' planted in approvalNode leaves this green.
    const planted = bare.replace(approval, () => `${approval}  const x = 'Waiting'\n`)
    expect(count(fnBody(planted, 'approvalNode'), "'Waiting'")).toBe(approvalWaiting + 1)
    expect(count(fnBody(planted, 'spineNode'), "'Waiting'")).toBe(0)
    expect(fnBody(planted, 'spineNode')).toBe(spine)

    // The comment strip is load-bearing: a comment inside spineNode that quotes the word is
    // not a literal, and must not turn the guard red.
    const mentioned = bare.replace(spine, () => `${spine}  // the old arm said 'Waiting'\n`)
    expect(count(fnBody(mentioned, 'spineNode'), "'Waiting'")).toBe(1) // raw: the mention counts
    expect(count(fnBody(stripComments(mentioned), 'spineNode'), "'Waiting'")).toBe(0)
  })
})
