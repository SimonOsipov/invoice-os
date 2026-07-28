// UBL / XML modal — renders the generated UBL 2.1 document for the currently selected
// REAL invoice. Ported from Platform.dc.html ~L957-976; persona-handoff-fix step 3
// swaps it off the fabricated `active.invoices` overlay (a mock invoice cycled onto
// whichever company was selected, printed as if it were that company's own document)
// onto its own live fetch, mirroring LiveInvoiceDetail's exact gating idiom
// (InvoiceDetail.tsx:131-241): Loading/ErrorState/no-gateway/not-found ladder, then
// ublXml() (lib/xml.ts) over the real InvoiceDetailRecord. Backdrop click closes; inner
// click is stopped (mirrors the prototype's `stop` handler).
//
// NOTE (persona-handoff-fix step 3 finding): `ctx.openXml` currently has NO caller
// anywhere in the app — nothing opens this modal today (confirmed: no button/handler
// references it anywhere in src/). The single-invoice mock flow that used to reach it
// was already retired by M5-09-04, and `ctx.selectedId` is otherwise fully inert —
// InvoiceDetail.tsx's dispatcher only ever renders a live detail for
// `ctx.importedInvoiceId`, never for the mock `selectedId` kind. This fix resolves off
// `importedInvoiceId` (the one selection field that IS real) regardless, so the
// document is honest the day a "View XML" button gets wired back up, rather than
// leaving a quieter version of the original bug waiting for that day.

import type { ReactNode } from 'react'

import { EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { getInvoice, shouldFetchInvoices, type InvoiceDetailRecord } from '../lib/invoices'
import { ublXml } from '../lib/xml'
import { closeGlyph, docGlyph2, downloadGlyph } from '../glyphs'
import type { PlatformCtx } from '../types'

export function XmlModal({ ctx }: { ctx: PlatformCtx }) {
  const { importedInvoiceId } = ctx
  const base = gatewayBase()
  // Same `base ? … : …` narrowing as LiveInvoiceDetail (InvoiceDetail.tsx:135-138), plus
  // an `importedInvoiceId != null` guard — this modal opens over App.tsx's whole
  // Workspace, not nested under InvoiceDetail, so it has no already-selected invoice to
  // assume; `immediate` only fires once BOTH are true, keeping a no-selection window at
  // zero network same as the no-gateway one.
  const detail = useAsync<InvoiceDetailRecord>(
    () =>
      base && importedInvoiceId
        ? getInvoice(ctx.authedFetch, base, importedInvoiceId)
        : Promise.reject(new Error('no invoice selected')),
    { immediate: shouldFetchInvoices(base) && importedInvoiceId != null, deps: [importedInvoiceId] },
  )

  let content: ReactNode
  if (importedInvoiceId == null) {
    content = <EmptyState title="No document selected" message="Open an invoice and choose “View XML” to preview its UBL document." />
  } else if (base == null) {
    content = <EmptyState title="No gateway configured" message="Connect a gateway to load this document." />
  } else if (detail.status === 'loading') {
    content = <Loading label="Loading document…" />
  } else if (detail.status === 'error') {
    content = detail.error ? <ErrorState error={detail.error} onRetry={detail.run} /> : null
  } else if (detail.data == null) {
    content = <EmptyState title="Invoice not found" message="This invoice could not be loaded." />
  } else {
    content = (
      <pre className="mono" style={{ margin: 0, fontSize: 12, lineHeight: 1.6, color: 'var(--fg-1)', whiteSpace: 'pre', tabSize: 2 }}>
        {ublXml(detail.data)}
      </pre>
    )
  }

  return (
    <div onClick={ctx.closeXml} style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'oklch(20% .02 210 / 0.42)', backdropFilter: 'blur(2px)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 40, animation: 'popIn 140ms ease-out' }}>
      <div onClick={(e) => e.stopPropagation()} style={{ width: 760, maxWidth: '100%', maxHeight: '100%', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', boxShadow: '0 24px 60px -20px oklch(20% .02 210 / 0.4)', display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
        <div style={{ flex: 'none', padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <span style={{ color: 'var(--action)', display: 'inline-flex' }}>{docGlyph2}</span>
            <div>
              <div className="card-title">UBL 2.1 document</div>
              <div className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
                PEPPOL BIS 3.0 · {detail.data?.invoice_number ?? '—'}
              </div>
            </div>
          </div>
          <div style={{ display: 'flex', gap: 9 }}>
            <button className="v2-btn v2-btn-ghost pf-btn" style={{ height: 34, fontSize: 13 }}>
              <span style={{ display: 'inline-flex' }}>{downloadGlyph}</span> Download .xml
            </button>
            <button onClick={ctx.closeXml} className="pf-btn" style={{ width: 34, height: 34, borderRadius: 'var(--radius-md)', border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}>
              {closeGlyph}
            </button>
          </div>
        </div>
        <div style={{ flex: 1, overflow: 'auto', background: 'var(--bg-1)', padding: '18px 20px' }}>{content}</div>
      </div>
    </div>
  )
}
