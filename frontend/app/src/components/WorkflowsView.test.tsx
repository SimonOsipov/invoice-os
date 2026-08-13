// @vitest-environment jsdom
//
// APPR-09-03 QA (task-507). The screen had NO test file before this: `Updated
// {policy.updated}` swapped to `policyStanding(policy)` at WorkflowsView.tsx:109 with no
// oracle at all — verified by mutation, a hardcoded 'Updated recently' literal (with the
// now-unused import deleted, so `noUnusedLocals` stays quiet) passed all 2026 app tests
// and a clean tsc.
//
// Deliberately SMALL. Subtask 04 owns this screen's four-surface ladder, its EmptyState,
// its testid wrapper and the INTRO copy, and adds nine more specs to this file; this one
// pins only the cell the live swap moved.

import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { Policy } from '../lib/workflows'
import type { PlatformCtx } from '../types'
import { WorkflowsView } from './WorkflowsView'

function policy(over: Partial<Policy> = {}): Policy {
  return {
    id: 'p1',
    name: 'Standard approval policy',
    scope: 'All invoices',
    status: 'draft',
    version: 1,
    activeVersion: null,
    nodes: [{ id: 'n1', type: 'approval', role: 'fin_mgr', sla: '24', delegate: false }],
    ...over,
  }
}

function listCtx(policies: Policy[]): PlatformCtx {
  return {
    mode: 'firm',
    active: { short: 'Lagos Freight' },
    policies,
    policiesState: 'ready',
    policiesError: null,
    refetchPolicies: vi.fn(),
    editingPolicyId: null,
    createPolicy: vi.fn(async () => {}),
    deletePolicy: vi.fn(async () => {}),
    openPolicy: vi.fn(),
  } as unknown as PlatformCtx
}

afterEach(cleanup)

describe('APPR-09-03 QA: each row states its VERSION standing, not an edit time', () => {
  it('renders the standing computed from version/activeVersion for every row', () => {
    const rows = [
      policy({ id: 'polA', name: 'In force', status: 'published', version: 3, activeVersion: 3 }),
      policy({ id: 'polB', name: 'Edited draft', status: 'draft', version: 4, activeVersion: 3 }),
      policy({ id: 'polC', name: 'Lost the slot', status: 'published', version: 2, activeVersion: null }),
      policy({ id: 'polD', name: 'Brand new', status: 'draft', version: 1, activeVersion: null }),
    ]
    render(<WorkflowsView ctx={listCtx(rows)} />)

    // Control: the list really rendered all four rows, so the standings below are read off
    // a populated screen rather than agreeing vacuously with an empty one.
    expect(screen.getByText('4 POLICIES')).toBeTruthy()
    for (const p of rows) expect(screen.getByText(p.name), `${p.id} did not render`).toBeTruthy()

    // Four DISTINCT strings, each a function of that row's own version pair — a literal
    // cannot satisfy all four, and nor can a constant read off the first row.
    expect(screen.getByText('v3 in force')).toBeTruthy()
    expect(screen.getByText('v3 in force · v4 draft')).toBeTruthy()
    expect(screen.getByText('Not in force')).toBeTruthy()
    expect(screen.getByText('Never published')).toBeTruthy()
  })

  it('no row claims an edit time — `Policy.updated` is gone from the type', () => {
    render(<WorkflowsView ctx={listCtx([policy({ status: 'published', version: 1, activeVersion: 1 })])} />)

    expect(screen.getByText('v1 in force'), 'the standing cell must still render').toBeTruthy()
    expect(screen.queryByText(/^Updated /), 'a row still renders an "Updated …" cell').toBeNull()
  })
})
