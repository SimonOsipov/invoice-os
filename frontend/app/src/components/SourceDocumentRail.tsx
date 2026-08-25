// The 316px evidence rail: content fingerprint, the document record, and the
// immutability note pinned to the bottom. Driven by the document record alone — never
// gated on the sheet or the bytes, so it is already true while the canvas is still
// loading.

import { useEffect, useRef, useState } from 'react'

import { copyGlyph, shieldGlyph } from '../glyphs'
import { actorLabel } from '../lib/actor'
import { fmtDateTime, fmtPlain } from '../lib/format'
import { formatBytes, type SourceDocumentRecord } from '../lib/sourceDocument'
import type { PlatformCtx } from '../types'

const COPIED_MS = 1800

const NOTE = 'Never rewritten. ASComply cannot replace, rename or annotate a source document'

function scopeOwner(ctx: Pick<PlatformCtx, 'mode' | 'active' | 'user'>): string {
  const name = ctx.mode === 'firm' ? ctx.active.name : (ctx.user.tenantName ?? ctx.active.name)
  return name || 'your organisation'
}

function Row({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div style={{ display: 'flex', gap: 10, alignItems: 'baseline' }}>
      <span className="label" style={{ flex: 'none', width: 112 }}>
        {label}
      </span>
      <span className={mono ? 'mono' : undefined} style={{ flex: 1, minWidth: 0, fontSize: 12.5, color: 'var(--fg-2)', wordBreak: 'break-all' }}>
        {value}
      </span>
    </div>
  )
}

export function SourceDocumentRail({
  ctx,
  record,
  invoiceNumber,
  sheetRowsTotal,
}: {
  ctx: Pick<PlatformCtx, 'mode' | 'active' | 'user'>
  record: SourceDocumentRecord | null
  invoiceNumber: string
  /** `null` until the sheet response lands — the row count is a file fact, not a stored one. */
  sheetRowsTotal: number | null
}) {
  const [copied, setCopied] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  useEffect(() => () => {
    if (timer.current != null) clearTimeout(timer.current)
  }, [])

  const frame = { display: 'flex', flexDirection: 'column' as const, height: '100%', minHeight: 0, background: 'var(--bg-2)' }
  const scroll = { flex: 1, overflow: 'auto', minHeight: 0, padding: '16px 18px', display: 'flex', flexDirection: 'column' as const, gap: 20 }

  if (record === null) {
    return (
      <div data-testid="source-document-rail" style={frame}>
        <div style={scroll}>
          <div style={{ padding: '14px 16px', border: '1px dashed var(--line-3)', borderRadius: 'var(--radius-md)', background: 'transparent', fontSize: 12.5, lineHeight: 1.6, color: 'var(--fg-3)' }}>
            No file, no size, no fingerprint. Manually entered invoices carry their state strip instead — the five
            stages {invoiceNumber} passes through, with each stage it reached showing who moved it and when.
          </div>
        </div>
      </div>
    )
  }

  const hashLines = record.content_hash.match(/.{1,16}/g) ?? []
  const uploader = actorLabel(record.uploaded_by)

  function copyHash() {
    // Optional-chained: a jsdom/insecure context has no clipboard, and a rejected write
    // must not surface as a console error (the e2e console gate is unfiltered).
    navigator.clipboard?.writeText(record?.content_hash ?? '').catch(() => {})
    setCopied(true)
    if (timer.current != null) clearTimeout(timer.current)
    timer.current = setTimeout(() => setCopied(false), COPIED_MS)
  }

  return (
    <div data-testid="source-document-rail" style={frame}>
      <div style={scroll}>
        <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
          <div style={{ padding: '10px 14px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10 }}>
            <span className="card-title">Content fingerprint · SHA-256</span>
            <button
              type="button"
              data-testid="copy-hash"
              onClick={copyHash}
              // A plain button, not `.pf-btn` — that class forces a pill radius with
              // `!important` (app-layer.css:192-201).
              style={{ flex: 'none', whiteSpace: 'nowrap', height: 26, padding: '0 9px', borderRadius: 'var(--radius-xs)', border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 11.5, fontWeight: 500, display: 'inline-flex', alignItems: 'center', gap: 5 }}
            >
              <span style={{ display: 'inline-flex' }}>{copyGlyph}</span> {copied ? 'Copied' : 'Copy'}
            </button>
          </div>
          <div style={{ padding: '12px 14px', background: 'var(--bg-1)' }}>
            {hashLines.map((line) => (
              <div key={line} className="mono" data-testid="hash-line" style={{ fontSize: 12, letterSpacing: '0.04em', lineHeight: 1.7, color: 'var(--fg-2)' }}>
                {line}
              </div>
            ))}
          </div>
          {/* Nothing recomputes SHA-256 in the browser — and a spreadsheet fetches decoded
              JSON rather than bytes — so the design's green MATCHES line would be a claim
              this build cannot make. Only the muted line ships. */}
          <div style={{ padding: '9px 14px', borderTop: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', gap: 7 }}>
            <span style={{ flex: 'none', width: 6, height: 6, borderRadius: 99, background: 'var(--fg-4)' }} />
            <span className="mono" style={{ fontSize: 10, letterSpacing: '0.06em', color: 'var(--fg-3)' }}>
              NOT VERIFIED THIS SESSION
            </span>
          </div>
        </div>
        <p style={{ margin: 0, fontSize: 11.5, lineHeight: 1.55, color: 'var(--fg-3)' }}>
          Recompute this hash on the original file and it will match, or the file is not the one we were given.
        </p>

        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          <div className="card-title">Document record</div>
          <Row label="Original filename" value={record.filename ?? 'Not recorded'} mono />
          <Row label="File size" value={formatBytes(record.size_bytes)} />
          <Row label="Uploaded" value={fmtDateTime(record.uploaded_at)} />
          <Row label="Uploaded by" value={uploader.text} mono={uploader.mono} />
          <Row label="Invoices created" value={`${fmtPlain(record.invoices_created)} from this one file`} />
          {/* `Rows in file` only once the sheet lands. `Pages`, `Dimensions` and `Rows read`
              are omitted rather than placeholdered — none is derivable in this build. */}
          {sheetRowsTotal != null && <Row label="Rows in file" value={fmtPlain(sheetRowsTotal)} />}
        </div>
      </div>

      <div style={{ flex: 'none', padding: '13px 18px', borderTop: '1px solid var(--line-1)', display: 'flex', gap: 10, background: 'var(--bg-1)' }}>
        <span style={{ flex: 'none', color: 'var(--action)', display: 'inline-flex' }}>{shieldGlyph}</span>
        <p style={{ margin: 0, fontSize: 11.5, lineHeight: 1.55, color: 'var(--fg-2)' }}>
          {NOTE} — for {scopeOwner(ctx)} and for us.
        </p>
      </div>
    </div>
  )
}
