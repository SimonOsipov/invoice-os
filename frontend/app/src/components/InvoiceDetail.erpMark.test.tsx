// @vitest-environment jsdom
// The "changed in the ERP" banner on the invoice view (ERPF-08-06).
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import { fmtDateTime } from '../lib/format'
import type { PlatformCtx } from '../types'
import { InvoiceDetail } from './InvoiceDetail'

const AT = '2026-07-02T10:00:00Z'
const mark = (over: Record<string, unknown> = {}) => ({
  connection_id: 'c1',
  system: 'fake',
  source_doc_id: 'd1',
  state: 'linked',
  differs_from_erp: false,
  changed_after_clearance_at: null,
  deleted_in_erp_at: null,
  ...over,
})
const record = { id: 'i1', entity_id: 'e1', invoice_number: 'INV-ERP-1', status: 'validated', issue_date: '2026-07-01T00:00:00Z', lines: [], violations: [], rejection_reasons: [] }

function ctx(mode: 'firm' | 'inhouse' = 'firm'): PlatformCtx {
  return { mode, active: { entityId: 'e1' }, user: { tenantName: 'Acme' }, authedFetch: createAuthedFetch(() => 'tok', vi.fn()), importedInvoiceId: 'i1', nav: () => {} } as unknown as PlatformCtx
}

function mockFetch(marks: { ok: boolean; status: number; body: unknown }) {
  const f = vi.fn((url: string) => {
    if (url.endsWith('/erp-marks')) return Promise.resolve({ ok: marks.ok, status: marks.status, json: () => Promise.resolve(marks.body) })
    if (url.endsWith('/custom-rules/names')) return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ names: {} }) })
    if (url.endsWith('/history')) return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve([]) })
    if (url.endsWith('/approval')) return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({ error: 'none' }) })
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(record) })
  })
  vi.stubGlobal('fetch', f)
  return f
}
const ok = (body: unknown) => ({ ok: true, status: 200, body })

beforeEach(() => vi.stubEnv('VITE_GATEWAY_URL', 'https://gw'))
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('InvoiceDetail ERP changed mark', () => {
  it('invoiceDetail_showsTheChangedInErpMark', async () => {
    mockFetch(ok({ marks: [mark({ changed_after_clearance_at: AT })] }))
    render(<InvoiceDetail ctx={ctx()} />)
    const banner = await screen.findByTestId('invoice-erp-changed-mark')
    expect(banner.textContent).toContain('Changed in the ERP after NRS accepted it')
    expect(banner.textContent).toContain(`Fake ERP · ${fmtDateTime(AT)}`)
  })

  it('invoiceDetail_noMarkNoBanner', async () => {
    for (const marks of [[], [mark({ differs_from_erp: true })]]) {
      const f = mockFetch(ok({ marks }))
      render(<InvoiceDetail ctx={ctx()} />)
      await screen.findByText('INV-ERP-1')
      await waitFor(() => expect(f.mock.calls.some(([u]) => String(u).endsWith('/erp-marks'))).toBe(true))
      await new Promise((r) => setTimeout(r, 20))
      expect(screen.queryByTestId('invoice-erp-changed-mark')).toBeNull()
      cleanup()
    }
  })

  it('invoiceDetail_aFailedMarksReadIsSilent', async () => {
    const f = mockFetch({ ok: false, status: 500, body: { error: 'boom' } })
    render(<InvoiceDetail ctx={ctx()} />)
    await screen.findByText('INV-ERP-1')
    await waitFor(() => expect(f.mock.calls.some(([u]) => String(u).endsWith('/erp-marks'))).toBe(true))
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.queryByTestId('invoice-erp-changed-mark')).toBeNull()
    expect(screen.queryByText(/something went wrong|try again/i)).toBeNull()
    expect(screen.getByText('INV-ERP-1')).toBeTruthy()
  })

  it('invoiceDetail_marksReadNamesClientAndInvoice', async () => {
    const f = mockFetch(ok({ marks: [] }))
    render(<InvoiceDetail ctx={ctx()} />)
    await waitFor(() => expect(f.mock.calls.some(([u]) => String(u).endsWith('/entities/e1/invoices/i1/erp-marks'))).toBe(true))
  })

  it('invoiceDetail_aMarksBodyWithoutMarksIsSilent', async () => {
    mockFetch(ok(record))
    render(<InvoiceDetail ctx={ctx()} />)
    await screen.findByText('INV-ERP-1')
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.queryByTestId('invoice-erp-changed-mark')).toBeNull()
    expect(screen.queryByText(/something went wrong|try again/i)).toBeNull()
  })

  it('invoiceDetail_inHouseSendsNoMarksRead', async () => {
    const f = mockFetch(ok({ marks: [mark({ changed_after_clearance_at: AT })] }))
    render(<InvoiceDetail ctx={ctx('inhouse')} />)
    await screen.findByText('INV-ERP-1')
    await new Promise((r) => setTimeout(r, 20))
    expect(f.mock.calls.some(([u]) => String(u).endsWith('/erp-marks'))).toBe(false)
  })
})
