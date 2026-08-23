// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.

import { readFileSync } from 'node:fs'
import { join } from 'node:path'

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { afterEach, describe, expect, it } from 'vitest'

import type { AuditEvent } from '../lib/audit'

import { AuditRow } from './AuditRow'

afterEach(cleanup)

function ev(over: Partial<AuditEvent> = {}): AuditEvent {
  return {
    id: '11111111-1111-1111-1111-111111111111',
    created_at: '2026-08-20T09:15:00Z',
    event: 'submission.accepted',
    actor: 'system',
    actor_name: 'System',
    actor_kind: 'system',
    entity_id: 'a0000000-0000-0000-0000-000000000001',
    company_name: 'Honeywell Group',
    company_scope: 'company',
    payload: {},
    ...over,
  }
}

// The parent owns `expandedId` (ReviewInvoicesTab.tsx's idiom). Single-open is a property
// of that ownership, not of the row -- so the test drives it through a parent, which is
// also the shape AUDIT-09 will mount.
function TwoRows() {
  const [openId, setOpenId] = useState<string | null>(null)
  const a = ev({ id: 'row-a', payload: { a_key: 'A' } })
  const b = ev({ id: 'row-b', payload: { b_key: 'B' } })
  return (
    <>
      {[a, b].map((e) => (
        <AuditRow key={e.id} event={e} expanded={openId === e.id} onToggle={() => setOpenId(openId === e.id ? null : e.id)} />
      ))}
    </>
  )
}

describe('AuditRow', () => {
  it('auditRow_singleOpenAtATime', () => {
    render(<TwoRows />)
    const [rowA, rowB] = screen.getAllByTestId('audit-row')
    fireEvent.click(rowA)
    expect(screen.getByText('A')).toBeTruthy()
    fireEvent.click(rowB)
    // Opening B closed A: the parent holds one id, so no second panel can survive.
    expect(screen.getByText('B')).toBeTruthy()
    expect(screen.queryByText('A')).toBeNull()
    expect(screen.getAllByTestId('audit-expansion')).toHaveLength(1)
    expect(rowA.getAttribute('aria-expanded')).toBe('false')
    expect(rowB.getAttribute('aria-expanded')).toBe('true')
  })

  it('auditRow_expansionRendersOnlyPayloadKeys', () => {
    // The design mock draws six fields for an accepted transmission. The payload the Go
    // writer actually stores has four. Rendering the payload's OWN keys is the contract --
    // a fixed field list would print two empty rows and read as missing data.
    const payload = { irn: 'NG-001', csid: 'CSID-9', status_code: '200', attempt: 2 }
    render(<AuditRow event={ev({ payload })} expanded onToggle={() => {}} />)
    const fields = screen.getAllByTestId('audit-payload-field')
    expect(fields).toHaveLength(4)
    expect(screen.getByText('NG-001')).toBeTruthy()
    expect(screen.queryByText(/duration/i)).toBeNull()
  })

  it('auditRow_invoiceAffordanceReadsBothKeys', () => {
    // The invoice key is inconsistent by design: `id` from internal/invoice/* and
    // approval/engine.go, `invoice_id` from approval/decision.go and
    // submission/verdict_audit.go. Reading only one silently drops half the log.
    const cases: Array<{ name: string; payload: Record<string, unknown>; want: string }> = [
      { name: 'id', payload: { id: 'inv-1' }, want: 'inv-1' },
      { name: 'invoice_id', payload: { invoice_id: 'inv-2' }, want: 'inv-2' },
    ]
    for (const c of cases) {
      const seen: string[] = []
      render(<AuditRow event={ev({ payload: c.payload })} expanded onToggle={() => {}} onFilterToInvoice={(id) => seen.push(id)} />)
      const link = screen.getByTestId('audit-invoice-affordance')
      fireEvent.click(link)
      expect(seen, `payload key ${c.name} must yield the affordance`).toEqual([c.want])
      cleanup()
    }
  })

  it('auditRow_isExtractable', () => {
    // AUDIT-09 mounts this row scoped to one invoice. An import from the Audit screen
    // would drag the whole screen -- and its fetch -- along with it.
    const src = readFileSync(join(__dirname, 'AuditRow.tsx'), 'utf8')
    const imports = src.match(/^import .*$/gm) ?? []
    // Control needle: prove the scan reads a real import list before trusting its silence.
    expect(imports.length, 'the import scan found nothing -- the regex, not the file, is wrong').toBeGreaterThanOrEqual(3)
    expect(imports.join('\n')).toContain('../lib/audit')
    expect(imports.join('\n')).not.toContain('AuditView')
    expect(src).not.toContain('PlatformCtx')
  })
})

describe('AuditRow evidence affordance', () => {
  it('auditRow_evidenceAffordanceIsInertNotFaked', () => {
    // AUDIT-08 owns the drawer. The story permits the button in a disabled state and
    // forbids faking the drawer, so the button carries a VISIBLE reason: a title= on a
    // disabled button never fires in Chromium.
    render(<AuditRow event={ev({ event: 'submission.accepted', payload: { id: 'inv-1', irn: 'NG-1' } })} expanded onToggle={() => {}} />)
    const btn = screen.getByTestId('audit-evidence-affordance')
    expect(btn).toHaveProperty('disabled', true)
    expect(screen.getByTestId('audit-evidence-blocked-reason').textContent).toBeTruthy()
  })

  it('auditRow_evidenceAffordanceOnlyWhereEvidenceExists', () => {
    // A policy edit has no transmission behind it; offering the link would claim a record
    // that does not exist.
    render(<AuditRow event={ev({ event: 'approval_policy.updated', payload: { id: 'pol-1' } })} expanded onToggle={() => {}} />)
    expect(screen.queryByTestId('audit-evidence-affordance')).toBeNull()
  })
})
