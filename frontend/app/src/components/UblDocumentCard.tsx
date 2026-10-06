// The rail's last card: names the UBL document from props and fetches it only on Download.

import { useRef, useState } from 'react'

import { ApiError } from '@invoice-os/api-client'

import { docGlyph2, downloadGlyph } from '../glyphs'
import { getInvoiceUbl } from '../lib/invoices'
import type { PlatformCtx } from '../types'
import { LOAD_FAILED, downloadUbl, ublFilename } from './XmlModal'

export function UblDocumentCard({
  ctx,
  base,
  invoiceId,
  invoiceNumber,
  canView,
  blockedReason,
  editing,
  onView,
}: {
  ctx: PlatformCtx
  base: string
  invoiceId: string
  invoiceNumber: string
  canView: boolean
  blockedReason: string | null
  editing: boolean
  onView: () => void
}) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // A ref, not state: state batches, so a fast double click would pass a state guard.
  const inFlight = useRef(false)

  async function download() {
    if (inFlight.current) return
    inFlight.current = true
    setPending(true)
    setError(null)
    try {
      const xml = await getInvoiceUbl(ctx.authedFetch, base, invoiceId)
      // An empty 200 is a failure, as in XmlModal.
      if (!xml) setError(LOAD_FAILED)
      else downloadUbl(xml, invoiceNumber)
    } catch (err) {
      // Only a 409 carries a user-facing sentence; every other wire message is a sentinel.
      setError(err instanceof ApiError && err.status === 409 ? err.message : LOAD_FAILED)
    } finally {
      inFlight.current = false
      setPending(false)
    }
  }

  return (
    <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
      <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10 }}>
        <span className="card-title">UBL 2.1 document</span>
        <span className="mono" style={{ flex: 'none', fontSize: 9, fontWeight: 700, letterSpacing: '0.09em', color: 'var(--fg-3)' }}>
          READ ONLY
        </span>
      </div>
      <div data-testid="ubl-document-card" style={{ padding: '15px 18px 16px' }}>
        <div className="mono" data-testid="ubl-card-filename" style={{ fontSize: 12, fontWeight: 600, color: 'var(--fg-1)', wordBreak: 'break-all' }}>
          {ublFilename(invoiceNumber)}
        </div>
        <div className="mono" data-testid="ubl-card-meta" style={{ margin: '3px 0 13px', fontSize: 10, letterSpacing: '0.05em', color: 'var(--fg-3)' }}>
          UBL 2.1 · PEPPOL BIS 3.0
        </div>
        {!canView && blockedReason != null && (
          <div data-testid="ubl-card-blocked" style={{ marginTop: 12, padding: '13px 14px', marginBottom: 12, border: '1px dashed var(--line-3)', borderRadius: 'var(--radius-md)', background: 'transparent' }}>
            <div style={{ fontSize: 12.5, lineHeight: 1.55, color: 'var(--fg-3)' }}>{blockedReason}</div>
          </div>
        )}
        {canView && !editing && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            <button
              type="button"
              data-testid="ubl-card-view"
              onClick={onView}
              className="v2-btn v2-btn-ghost pf-btn"
              style={{ width: '100%', height: 34, fontSize: 13, justifyContent: 'center' }}
            >
              {docGlyph2} View UBL/XML
            </button>
            {/* Inline disabled style: `.v2-btn-ghost:hover` is not guarded by `:not(:disabled)`. */}
            <button
              type="button"
              data-testid="ubl-card-download"
              onClick={() => void download()}
              disabled={pending}
              className="v2-btn v2-btn-ghost pf-btn"
              style={{
                width: '100%',
                height: 34,
                fontSize: 13,
                justifyContent: 'center',
                ...(pending ? { background: 'var(--bg-3)', borderColor: 'var(--line-1)', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
              }}
            >
              {downloadGlyph} Download .xml
            </button>
            {error !== null && (
              <p data-testid="ubl-card-download-error" style={{ margin: 0, fontSize: 12.5, lineHeight: 1.55, color: 'var(--fg-3)' }}>
                {error}
              </p>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
