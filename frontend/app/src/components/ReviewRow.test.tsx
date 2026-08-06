// @vitest-environment jsdom
// task-413 (BUG-05-04) QA gap-fill: no component test rendered Row before this file --
// the buyer-tin testid/colour on this surface was verified only by lib unit tests and
// code inspection (mutation-verify: deleting data-testid="buyer-tin" from ReviewRow.tsx
// reddened nothing). Row is rendered with `expanded=false` throughout so the
// ExpandedFixPanel's own getInvoice fetch never engages -- out of scope here.
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { InvoiceRecord } from '../lib/invoices'
import type { PlatformCtx } from '../types'
import { Row } from './ReviewRow'

function row(over: Partial<InvoiceRecord> = {}): InvoiceRecord {
  return {
    id: 'inv-x',
    entity_id: 'ent-1',
    import_batch_id: null,
    invoice_number: 'INV-X',
    status: 'draft',
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
    ...over,
  }
}

function reviewRowCtx(): PlatformCtx {
  const ctx = {
    mode: 'firm',
    active: { entityId: 'ent-1' },
    user: { tenantName: 'Acme Co' },
    authedFetch: createAuthedFetch(() => 'tok', () => {}),
    openCreate: () => {},
    openImportedInvoice: () => {},
    invoiceQuery: '',
  }
  return ctx as unknown as PlatformCtx
}

function renderRow(over: Partial<InvoiceRecord> = {}) {
  render(
    <Row
      r={row(over)}
      batches={[]}
      checked={false}
      expanded={false}
      onToggleExpand={() => {}}
      onToggle={() => {}}
      ctx={reviewRowCtx()}
      base="https://gw"
      onChanged={() => {}}
    />,
  )
}

afterEach(() => {
  cleanup()
})

describe('ReviewRow buyer TIN signal (task-413, BUG-05-04, AC-4)', () => {
  it('AC-4: null, empty and whitespace-only buyer TIN all read TIN MISSING in red', () => {
    const cases: Array<{ label: string; buyer_tin: string | null }> = [
      { label: 'null', buyer_tin: null },
      { label: 'empty string', buyer_tin: '' },
      { label: 'whitespace-only', buyer_tin: '   ' },
    ]

    for (const { label, buyer_tin } of cases) {
      renderRow({ buyer_tin })

      const tin = screen.getByTestId('buyer-tin')
      expect(tin.textContent, label).toBe('TIN MISSING')
      expect(tin.style.color, label).toBe('var(--status-red-text)')

      cleanup()
    }
  })

  it('AC-4/AC-5: a present buyer TIN, malformed or well-formed, renders the value in grey', () => {
    const cases: Array<{ label: string; buyer_tin: string }> = [
      { label: 'malformed', buyer_tin: 'BADTIN' },
      { label: 'well-formed', buyer_tin: '87654321-0002' },
    ]

    for (const { label, buyer_tin } of cases) {
      renderRow({ buyer_tin })

      const tin = screen.getByTestId('buyer-tin')
      expect(tin.textContent, label).toBe(buyer_tin)
      expect(tin.style.color, label).toBe('var(--fg-3)')

      cleanup()
    }
  })
})
