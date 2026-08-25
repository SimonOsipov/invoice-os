// Projects an invoice onto the five-node state strip. The invariant that keeps this small:
// `status` and `run` decide every state and every label; `history` supplies only `at` and
// `actor` (invoiceStrip.test.ts S-2).

import { actorLabel, type ActorLabel } from './actor'
import { approvalRunStateView, type ApprovalRun } from './approvals'
import { fmtTime } from './format'
import type { InvoiceStatus, StatusChange } from './invoices'

export type StripState = 'done' | 'current' | 'failed' | 'unreached' | 'not-required'

export interface StripNode {
  key: 'draft' | 'validated' | 'approved' | 'queued' | 'accepted'
  label: string
  state: StripState
  // Non-null iff the node was entered AND a source recorded when.
  at: string | null
  actor: ActorLabel | null
  caption: string
}

// Node 3 is absent here: it follows the approval run, and no invoice status maps to it.
type SpineNode = 1 | 2 | 4 | 5

const NODE_OF_STATUS: Record<InvoiceStatus, SpineNode> = {
  draft: 1,
  validated: 2,
  queued: 4,
  submitted: 4,
  accepted: 5,
  rejected: 5,
  failed: 5,
}

const SOURCES: Record<SpineNode, InvoiceStatus[]> = {
  1: ['draft'],
  2: ['validated'],
  4: ['queued', 'submitted'],
  5: ['accepted', 'rejected', 'failed'],
}

// Node 3 never relabels; only node 5 does, from `status`.
const FIXED_LABEL: Record<1 | 2 | 3 | 4, string> = { 1: 'Draft', 2: 'Validated', 3: 'Approved', 4: 'Queued' }

const KEY_OF: Record<1 | 2 | 3 | 4 | 5, StripNode['key']> = {
  1: 'draft',
  2: 'validated',
  3: 'approved',
  4: 'queued',
  5: 'accepted',
}

// Last match by ARRAY POSITION, never a sort on changed_at: the server already ordered by
// (changed_at ASC, id ASC) and `id` is not on the wire, so two rows written in one
// transaction share a timestamp and keep their true order only here (S-17).
function latestInto(history: StatusChange[], want: InvoiceStatus[]): StatusChange | null {
  for (let i = history.length - 1; i >= 0; i--) {
    if (want.includes(history[i].to_status)) return history[i]
  }
  return null
}

// Gates on the RESOLVED kind, not the wire's actor_kind: an unnamed person degrades to
// `raw` and must print its subject whole (S-25).
function display(a: ActorLabel): string {
  if (a.kind !== 'person') return a.text
  return a.text.trim().split(/\s+/)[0]
}

function attribution(at: string | null, actor: ActorLabel | null): string | null {
  if (at === null) return null
  return actor === null ? fmtTime(at) : `${fmtTime(at)} · ${display(actor)}`
}

function spineNode(k: SpineNode, cursor: SpineNode, history: StatusChange[], status: InvoiceStatus): StripNode {
  const row = latestInto(history, SOURCES[k])
  const entered = k <= cursor
  const state: StripState =
    k < cursor
      ? 'done'
      : k > cursor
        ? 'unreached'
        : k === 5 && status === 'accepted'
          ? 'done'
          : k === 5 && (status === 'rejected' || status === 'failed')
            ? 'failed'
            : 'current'
  const at = entered && row !== null ? row.changed_at : null
  const actor = entered && row !== null ? actorLabel(row.actor, { name: row.actor_name, kind: row.actor_kind }) : null
  const label =
    k === 5
      ? status === 'rejected'
        ? 'Rejected by FIRS'
        : status === 'failed'
          ? 'Transmission failed'
          : 'Accepted by FIRS'
      : FIXED_LABEL[k]
  // A `current` node never renders its time, even with a populated row (S-20). Node 4
  // names the actual status, since queued and submitted share it.
  const caption =
    state === 'current'
      ? k === 4 && status === 'submitted'
        ? 'Submitted'
        : 'Waiting'
      : state === 'unreached'
        ? 'Not reached'
        : (attribution(at, actor) ?? '—')
  return { key: KEY_OF[k], label, state, at, actor, caption }
}

function approvalNode(run: ApprovalRun | null, cursor: SpineNode): StripNode {
  const bare = { key: KEY_OF[3], label: FIXED_LABEL[3], at: null, actor: null }
  if (run === null) {
    return cursor <= 2
      ? { ...bare, state: 'unreached', caption: 'Not reached' }
      : { ...bare, state: 'not-required', caption: 'Not required' }
  }
  switch (run.state) {
    case 'open':
      // The literal word, not approvalRunStateView('open').label ('In progress') -- the
      // Core AC pins 'Waiting' on the current state.
      return { ...bare, state: 'current', caption: 'Waiting' }
    case 'approved':
    case 'rejected': {
      // GET /v1/invoices/{id}/approval ships no resolved actor pair, so any subject other
      // than 'system' would fall through actorLabel to APP_PERSONAS and print the other
      // tenant's name (S-10). 'system' short-circuits above that table.
      // The closed_at guard keeps `at` and `actor` non-null together on every node (S-27).
      const actor = run.closed_at !== null && run.closed_by === 'system' ? actorLabel('system') : null
      return {
        ...bare,
        state: run.state === 'approved' ? 'done' : 'failed',
        at: run.closed_at,
        actor,
        caption: attribution(run.closed_at, actor) ?? '—',
      }
    }
    case 'cancelled':
      // closed_at is written COALESCE(closed_at, now()), so on a cancelled run it means
      // the approval time or the cancellation time. Not renderable either way (S-12).
      return { ...bare, state: 'not-required', caption: 'Approval voided' }
    default: {
      // ApprovalRun.state is `string` on the wire; a DB CHECK is the only enforcement. A
      // blank state has no label to echo, so it takes the generic `current` caption rather
      // than rendering an empty cell (S-33).
      const label = approvalRunStateView(run.state).label
      return { ...bare, state: 'current', caption: label.trim() === '' ? 'Waiting' : label }
    }
  }
}

export function stripNodes(history: StatusChange[], run: ApprovalRun | null, status: InvoiceStatus): StripNode[] {
  const cursor = NODE_OF_STATUS[status]
  return [
    spineNode(1, cursor, history, status),
    spineNode(2, cursor, history, status),
    approvalNode(run, cursor),
    spineNode(4, cursor, history, status),
    spineNode(5, cursor, history, status),
  ]
}
