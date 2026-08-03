// The invoice-detail right-rail entry point, directly above `Status history`. Card recipe
// copied from that card (InvoiceDetail.tsx:649-653) so the rail reads as one stack.
//
// It carries no row or column count: the card never fetches the sheet, neither count is
// stored, and a guessed one on an evidence surface is worse than an absent one.

import { ErrorState, Loading } from '@invoice-os/api-client'

import { classifyDocument, describeSourceRows, formatBytes } from '../lib/sourceDocument'
import { fileTypeTone, formatLabel, type SourceDocumentAsync } from './SourceDocumentStates'
import { docGlyph2 } from '../glyphs'

function shortHash(hash: string): string {
  return hash.length <= 17 ? hash : `${hash.slice(0, 8)}…${hash.slice(-8)}`
}

export function SourceDocumentCard({ meta, onOpen }: { meta: SourceDocumentAsync; onOpen: () => void }) {
  const response = meta.data
  const record = response?.document ?? null

  let body
  if (meta.status === 'error') {
    body = meta.error ? <ErrorState error={meta.error} onRetry={meta.run} /> : null
  } else if (response == null) {
    body = <Loading label="Loading document record…" />
  } else if (record == null) {
    body = (
      <>
        <div style={{ padding: '14px 16px', border: '1px dashed var(--line-3)', borderRadius: 'var(--radius-md)', background: 'transparent' }}>
          <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--fg-2)' }}>No source document</div>
          <div style={{ marginTop: 4, fontSize: 12.5, lineHeight: 1.55, color: 'var(--fg-3)' }}>
            This invoice was typed into ASComply. There is no uploaded file behind it.
          </div>
        </div>
        <button
          type="button"
          data-testid="why-no-source-document"
          onClick={onOpen}
          className="v2-btn v2-btn-ghost pf-btn"
          style={{ marginTop: 12, width: '100%', height: 34, fontSize: 13 }}
        >
          Why there is no file
        </button>
      </>
    )
  } else {
    const kind = classifyDocument(record.filename, record.declared_content_type)
    const tone = fileTypeTone(record.filename, record.declared_content_type)
    body = (
      <>
        <div style={{ display: 'flex', gap: 11 }}>
          <span style={{ flex: 'none', width: 38, height: 38, borderRadius: 'var(--radius-sm)', background: tone.bg, color: tone.fg, display: 'grid', placeItems: 'center' }}>
            {docGlyph2}
          </span>
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ fontSize: 12.5, fontWeight: 600, color: 'var(--fg-1)', wordBreak: 'break-all', lineHeight: 1.4 }}>
              {record.filename ?? 'Filename not recorded'}
            </div>
            <div className="mono" data-testid="source-document-card-meta" style={{ marginTop: 3, fontSize: 10.5, letterSpacing: '0.05em', color: 'var(--fg-3)' }}>
              {formatLabel(record.filename, record.declared_content_type)} · {formatBytes(record.size_bytes)}
            </div>
          </div>
        </div>
        <p data-testid="source-document-range" style={{ margin: '12px 0 0', fontSize: 12.5, lineHeight: 1.55, color: 'var(--fg-2)' }}>
          {describeSourceRows(response.source_rows, kind)}
        </p>
        <button
          type="button"
          data-testid="view-source-document"
          onClick={onOpen}
          className="v2-btn v2-btn-ghost pf-btn"
          style={{ marginTop: 12, width: '100%', height: 34, fontSize: 13 }}
        >
          View source document
        </button>
        <div className="mono" style={{ marginTop: 14, paddingTop: 12, borderTop: '1px solid var(--line-1)', fontSize: 10.5, letterSpacing: '0.04em', color: 'var(--fg-3)', wordBreak: 'break-all' }}>
          SHA-256 {shortHash(record.content_hash)}
        </div>
      </>
    )
  }

  return (
    <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
      <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10 }}>
        <span className="card-title">Source document</span>
        <span className="mono" style={{ flex: 'none', fontSize: 9.5, letterSpacing: '0.06em', color: 'var(--fg-3)' }}>
          READ ONLY
        </span>
      </div>
      <div data-testid="source-document-card" style={{ padding: '16px 18px' }}>
        {body}
      </div>
    </div>
  )
}
