// UBL viewer: fetches the server's canonical UBL 2.1 document for one invoice and shows it
// verbatim. Shell ported from Platform.dc.html ~L957-976 — backdrop click closes, inner
// click is stopped.

import type { ReactNode } from 'react'

import { ApiError, EmptyState, ErrorState, Loading, useAsync } from '@invoice-os/api-client'

import { closeGlyph, docGlyph2, downloadGlyph } from '../glyphs'
import { getInvoiceUbl } from '../lib/invoices'
import { useDismiss } from '../lib/useDismiss'
import type { PlatformCtx } from '../types'

// Revisit if either becomes true: an adapter actually transmits UBL (the mock APP takes a
// canonical payload, not this document), or a read endpoint exposes app_exchange bodies.
const PROVENANCE =
  'Rendered from the stored invoice record when you opened this view. It is not a copy of what was transmitted to the access point.'

// ErrorState prints error.message, and the wire message on this route is an internal
// sentinel ('invoice: validation', 'internal server error') — so the generic arm re-wraps
// with fixed copy and drops `body`, which carries the same sentence.
const LOAD_FAILED = 'This document could not be loaded.'

function ublFilename(invoiceNumber: string): string {
  const safe = invoiceNumber.replace(/[^A-Za-z0-9._-]/g, '-')
  return `${safe || 'invoice'}.xml`
}

function downloadUbl(xml: string, invoiceNumber: string): void {
  const url = URL.createObjectURL(new Blob([xml], { type: 'application/xml' }))
  const a = document.createElement('a')
  a.href = url
  a.download = ublFilename(invoiceNumber)
  a.click()
  URL.revokeObjectURL(url)
}

export function XmlModal({
  ctx,
  base,
  invoiceId,
  invoiceNumber,
  onClose,
}: {
  ctx: PlatformCtx
  base: string
  invoiceId: string
  invoiceNumber: string
  onClose: () => void
}) {
  const ubl = useAsync<string>(() => getInvoiceUbl(ctx.authedFetch, base, invoiceId), { deps: [invoiceId] })

  useDismiss(true, onClose)

  // Truthiness, not `!= null`: an empty 200 body resolves 'ready' with data '' (resolveStatus's
  // isEmpty is Array-gated), which would ship a blank <pre> and a download of zero bytes.
  const xml = ubl.status === 'ready' && ubl.data ? ubl.data : null

  let content: ReactNode
  if (ubl.status === 'loading') {
    content = <Loading label="Loading document…" />
  } else if (ubl.error?.status === 409) {
    content = <EmptyState message={ubl.error.message} />
  } else if (ubl.error?.status === 404) {
    content = <EmptyState title="Invoice not found" message="This invoice could not be loaded." />
  } else if (xml != null) {
    content = (
      <pre data-testid="ubl-xml" className="mono" style={{ margin: 0, fontSize: 12, lineHeight: 1.6, color: 'var(--fg-1)', whiteSpace: 'pre', tabSize: 2 }}>
        {xml}
      </pre>
    )
  } else {
    content = (
      <ErrorState
        error={new ApiError(ubl.error?.kind ?? 'malformed', LOAD_FAILED, ubl.error?.status ?? null)}
        onRetry={ubl.run}
      />
    )
  }

  return (
    <div data-testid="ubl-modal" onClick={onClose} style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'oklch(20% .02 210 / 0.42)', backdropFilter: 'blur(2px)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 40, animation: 'popIn 140ms ease-out' }}>
      <div onClick={(e) => e.stopPropagation()} role="dialog" aria-modal="true" aria-label="UBL 2.1 document" style={{ width: 760, maxWidth: '100%', maxHeight: '100%', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', boxShadow: '0 24px 60px -20px oklch(20% .02 210 / 0.4)', display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
        <div style={{ flex: 'none', padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <span style={{ color: 'var(--action)', display: 'inline-flex' }}>{docGlyph2}</span>
            <div>
              <div className="card-title">UBL 2.1 document</div>
              <div className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
                PEPPOL BIS 3.0 · {invoiceNumber}
              </div>
            </div>
          </div>
          <div style={{ display: 'flex', gap: 9 }}>
            {xml != null && (
              <button type="button" data-testid="download-ubl" onClick={() => downloadUbl(xml, invoiceNumber)} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 34, fontSize: 13 }}>
                <span style={{ display: 'inline-flex' }}>{downloadGlyph}</span> Download .xml
              </button>
            )}
            <button data-testid="ubl-modal-close" onClick={onClose} className="pf-btn" style={{ width: 34, height: 34, borderRadius: 'var(--radius-md)', border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}>
              {closeGlyph}
            </button>
          </div>
        </div>
        <div data-testid="ubl-provenance" style={{ flex: 'none', padding: '10px 20px', borderBottom: '1px solid var(--line-1)', fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>
          {PROVENANCE}
        </div>
        <div style={{ flex: 1, overflow: 'auto', background: 'var(--bg-1)', padding: '18px 20px' }}>{content}</div>
      </div>
    </div>
  )
}
