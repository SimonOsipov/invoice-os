// The 316px evidence rail: content fingerprint, the document record, and the
// immutability note pinned to the bottom. Driven by the document record alone — never
// gated on the sheet or the bytes, so it is already true while the canvas is still
// loading.

import { useEffect, useRef, useState } from 'react'

import { shieldGlyph } from '../glyphs'
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
    <div style={{ display: 'grid', gridTemplateColumns: '110px minmax(0, 1fr)', gap: 10 }}>
      <span style={{ fontSize: 12, color: 'var(--fg-3)' }}>{label}</span>
      <span className={mono ? 'mono' : undefined} style={{ fontSize: 12.5, color: 'var(--fg-1)', lineHeight: 1.45, wordBreak: 'break-word' }}>
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
  const scroll = { flex: 1, overflow: 'auto', minHeight: 0, display: 'flex', flexDirection: 'column' as const }

  if (record === null) {
    return (
      <div data-testid="source-document-rail" style={frame}>
        <div style={scroll}>
          <div style={{ padding: '18px 20px' }}>
            <div style={{ padding: 14, border: '1px dashed var(--line-3)', borderRadius: 'var(--radius-md)', background: 'var(--bg-1)' }}>
              <p style={{ margin: 0, fontSize: 12.5, lineHeight: 1.55, color: 'var(--fg-2)' }}>
                No file, no size, no fingerprint. Manually entered invoices carry their state strip instead — the five
                stages {invoiceNumber} passes through, with each stage it reached showing who moved it and when.
              </p>
            </div>
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
        <div style={{ padding: '18px 20px 22px' }}>
          <div style={{ border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
            <div style={{ padding: '8px 12px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
              <span className="label">Content fingerprint · SHA-256</span>
              <button
                type="button"
                data-testid="copy-hash"
                onClick={copyHash}
                className="v2-btn v2-btn-ghost pf-btn"
                style={{ flex: 'none', height: 24, padding: '0 9px', fontSize: 11.5 }}
              >
                {copied ? 'Copied' : 'Copy'}
              </button>
            </div>
            <div className="mono" style={{ padding: '10px 12px', fontSize: 11, lineHeight: 1.65, color: 'var(--fg-1)', wordBreak: 'break-all' }}>
              {hashLines.map((line) => (
                <div key={line} data-testid="hash-line">
                  {line}
                </div>
              ))}
            </div>
          </div>
          {/* Nothing recomputes SHA-256 in the browser — and a spreadsheet fetches decoded
              JSON rather than bytes — so the design's green MATCHES line would be a claim
              this build cannot make. Only the muted line ships. */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 10 }}>
            <span style={{ flex: 'none', width: 6, height: 6, borderRadius: '50%', background: 'var(--line-3)' }} />
            <span className="mono" style={{ fontSize: 9.5, fontWeight: 700, letterSpacing: '0.06em', color: 'var(--fg-3)' }}>
              NOT VERIFIED THIS SESSION
            </span>
          </div>
          <p style={{ margin: '12px 0 0', fontSize: 12, lineHeight: 1.55, color: 'var(--fg-3)' }}>
            Recompute this hash on the original file and it will match, or the file is not the one we were given.
          </p>
        </div>

        <div style={{ borderTop: '1px solid var(--line-1)', padding: '16px 20px 20px' }}>
          <div className="label" style={{ marginBottom: 12 }}>
            Document record
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            <Row label="Original filename" value={record.filename ?? 'Not recorded'} />
            <Row label="File size" value={formatBytes(record.size_bytes)} />
            <Row label="Uploaded" value={fmtDateTime(record.uploaded_at)} />
            <Row label="Uploaded by" value={uploader.text} mono={uploader.mono} />
            <Row label="Invoices created" value={`${fmtPlain(record.invoices_created)} from this one file`} />
            {/* `Rows in file` only once the sheet lands. `Pages`, `Dimensions` and `Rows read`
                are omitted rather than placeholdered — none is derivable in this build. */}
            {sheetRowsTotal != null && <Row label="Rows in file" value={fmtPlain(sheetRowsTotal)} />}
          </div>
        </div>

        <div style={{ borderTop: '1px solid var(--line-1)', padding: '16px 20px 24px', marginTop: 'auto', display: 'flex', alignItems: 'flex-start', gap: 9 }}>
          <span style={{ flex: 'none', color: 'var(--fg-3)', marginTop: 1, display: 'inline-flex' }}>{shieldGlyph}</span>
          <p style={{ margin: 0, fontSize: 12, lineHeight: 1.55, color: 'var(--fg-2)' }}>
            {NOTE} — for {scopeOwner(ctx)} and for us.
          </p>
        </div>
      </div>
    </div>
  )
}
