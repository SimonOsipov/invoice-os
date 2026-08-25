// The four non-sheet canvases of the source-document previewer, plus the small pure
// helpers the card, the modal header and the rail all read.
//
// The design's `Download original` / `Download original file` actions and every fragment
// of copy presupposing a download are cut with the button: nothing in this build can
// download a stored document, and this is an evidence surface.

import type { CSSProperties } from 'react'

import type { ApiError, AsyncStatus } from '@invoice-os/api-client'

import { refreshGlyph, warnTriGlyph } from '../glyphs'
import { actorLabel } from '../lib/actor'
import { fmtDate, fmtDateTime } from '../lib/format'
import {
  classifyDocument,
  formatBytes,
  type LoadStatus,
  type SourceDocumentRecord,
  type SourceDocumentResponse,
} from '../lib/sourceDocument'

/** The `useAsync` result the card and the modal share — one record fetch, two readers. */
export type SourceDocumentAsync = {
  status: AsyncStatus
  data: SourceDocumentResponse | null
  error: ApiError | null
  run: () => void
}

function extensionOf(filename: string | null): string {
  const dot = filename ? filename.lastIndexOf('.') : -1
  return dot < 0 ? '' : (filename as string).slice(dot + 1).toLowerCase()
}

/** Header/format label: the extension uppercased, else the declared type, else UNKNOWN. */
export function formatLabel(filename: string | null, declaredContentType: string | null): string {
  const ext = extensionOf(filename)
  if (ext) return ext.toUpperCase()
  const declared = (declaredContentType ?? '').split(';')[0].trim()
  return declared || 'UNKNOWN'
}

export interface FileTone {
  bg: string
  fg: string
}

// XLSX green / PDF red / JPG amber / unknown muted, off the same classifier the state
// resolver uses. Never `--accent-tint`: it is undefined in the rebuilt design system and
// resolves silently to nothing.
export function fileTypeTone(filename: string | null, declaredContentType: string | null): FileTone {
  switch (classifyDocument(filename, declaredContentType)) {
    case 'spreadsheet':
      return { bg: 'var(--status-green-bg)', fg: 'var(--status-green-text)' }
    case 'pdf':
      return { bg: 'var(--status-red-bg)', fg: 'var(--status-red-text)' }
    case 'image':
      return { bg: 'var(--status-amber-bg)', fg: 'var(--status-amber-text)' }
    case 'unrenderable':
      return { bg: 'var(--bg-3)', fg: 'var(--fg-3)' }
  }
}

// The real failure, not a fabricated reference: the error envelope is `{error: string}`
// and ApiError carries only kind/status/body, so no request id exists to print.
export function failureLine(error: ApiError | null): string {
  if (error?.kind === 'http' && error.status != null) return `HTTP ${error.status}`
  if (error?.kind === 'malformed') return 'MALFORMED RESPONSE'
  return 'NETWORK ERROR'
}

const CANVAS: CSSProperties = {
  height: '100%',
  overflow: 'auto',
  padding: '40px 44px',
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'flex-start',
  gap: 14,
}

const HEADING: CSSProperties = { fontSize: 17, fontWeight: 600, letterSpacing: '-0.01em', color: 'var(--fg-1)' }
const BODY: CSSProperties = { fontSize: 13, lineHeight: 1.65, color: 'var(--fg-2)', margin: 0, maxWidth: 620 }

export function NoSourceCanvas({
  invoiceNumber,
  createdAt,
  createdBy,
  createdByResolved,
}: {
  invoiceNumber: string
  createdAt: string | null
  createdBy: string | null
  /** The server's resolved pair for `createdBy` (actor.ts), when the caller's wire carries one. */
  createdByResolved?: { name: string; kind: string }
}) {
  // The clause names a PERSON or nobody: 'system' typed nothing in, and a raw uuid never
  // appears mid-prose (SourceDocumentStates.test.tsx, "names a person and nobody else").
  const creator = actorLabel(createdBy, createdByResolved)
  const by = creator.kind === 'person' ? ` by ${creator.text}` : ''
  return (
    <div data-testid="source-document-no-source" style={CANVAS}>
      <div style={HEADING}>There is no source document</div>
      <p style={BODY}>
        {invoiceNumber} was typed into ASComply{by} on {fmtDate(createdAt)}. No file was uploaded, so there is nothing
        to preview — the state strip is the record of how far this invoice has come.
      </p>
      <div style={{ marginTop: 6, padding: '14px 16px', border: '1px dashed var(--line-3)', borderRadius: 'var(--radius-md)', background: 'transparent', maxWidth: 620 }}>
        <div className="label" style={{ marginBottom: 6 }}>
          Why this is not an error
        </div>
        <p style={{ ...BODY, fontSize: 12.5, color: 'var(--fg-3)' }}>
          A source document can only arrive through an import run. Invoices entered by hand — a one-off, a correction, a
          customer whose system exports nothing — never gain one, and one can never be attached later.
        </p>
      </div>
    </div>
  )
}

export function LoadingCanvas({ sizeBytes }: { sizeBytes: number | null }) {
  return (
    <div data-testid="source-document-loading" style={{ ...CANVAS, alignItems: 'stretch', gap: 18, padding: 0 }}>
      <div style={{ flex: 'none', display: 'flex', alignItems: 'center', gap: 12, padding: '11px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-2)' }}>
        {sizeBytes != null && (
          <span className="mono" style={{ flex: 'none', whiteSpace: 'nowrap', fontSize: 10.5, letterSpacing: '0.06em', color: 'var(--action)' }}>
            READING {formatBytes(sizeBytes)} FROM DOCUMENT STORAGE
          </span>
        )}
        {/* Indeterminate by construction — it encodes no position, so there is no number
            it could be wrong about. The `shimmer` keyframe is global (styles/platform.css). */}
        <span
          style={{
            flex: 1,
            height: 6,
            borderRadius: 99,
            overflow: 'hidden',
            background: 'repeating-linear-gradient(115deg, var(--action) 0 6px, var(--action-tint) 6px 13px)',
            backgroundSize: '200% 100%',
            animation: 'shimmer 1.15s linear infinite',
          }}
        />
      </div>
      <div style={{ padding: '0 18px', display: 'flex', flexDirection: 'column', gap: 7 }}>
        {Array.from({ length: 9 }, (_, i) => (
          <span
            key={i}
            style={{
              height: 20,
              borderRadius: 'var(--radius-xs)',
              background: 'repeating-linear-gradient(115deg, var(--bg-3) 0 40px, var(--bg-0) 40px 80px)',
              backgroundSize: '200% 100%',
              animation: 'shimmer 1.6s linear infinite',
            }}
          />
        ))}
      </div>
      <p style={{ ...BODY, padding: '0 18px 24px' }}>
        The file record and its fingerprint are already known — only the bytes are still on their way. Nothing about this
        document can change while it loads.
      </p>
    </div>
  )
}

export function UnrenderableCanvas({ record }: { record: SourceDocumentRecord }) {
  const facts: Array<[string, string]> = [
    ['FORMAT', formatLabel(record.filename, record.declared_content_type)],
    ['READER', 'None in the browser'],
    ['IMPORTED', fmtDateTime(record.uploaded_at)],
    ['INTEGRITY', 'SHA-256 recorded at upload'],
  ]
  return (
    <div data-testid="source-document-unrenderable" style={CANVAS}>
      <span style={{ color: 'var(--status-amber-text)', display: 'inline-flex' }}>{warnTriGlyph}</span>
      <div style={HEADING}>This file is stored, but we cannot render it here</div>
      <p style={BODY}>
        ASComply has no reader that can display this format in the browser. The bytes are intact and the file is exactly
        as it was uploaded.
      </p>
      <p style={BODY}>
        We keep files we cannot read on purpose. A file that broke an import is the one an auditor asks about, so it is
        never discarded.
      </p>
      <div style={{ marginTop: 6, width: '100%', maxWidth: 620, border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
        <div style={{ padding: '10px 14px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-2)' }}>
          <span className="card-title">What we know about this file</span>
        </div>
        <div style={{ padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 9 }}>
          {facts.map(([label, value]) => (
            <div key={label} style={{ display: 'flex', gap: 12 }}>
              <span className="mono" style={{ flex: 'none', width: 96, fontSize: 10.5, letterSpacing: '0.06em', color: 'var(--fg-3)' }}>
                {label}
              </span>
              <span className="mono" style={{ fontSize: 12, color: 'var(--fg-2)', wordBreak: 'break-all' }}>
                {value}
              </span>
            </div>
          ))}
        </div>
      </div>
      <p style={{ ...BODY, fontSize: 12.5, color: 'var(--fg-3)' }}>
        A file that failed to import never produces an invoice at all — those files stay on their import run.
      </p>
    </div>
  )
}

export function FailedCanvas({ error, onRetry }: { error: ApiError | null; onRetry: () => void }) {
  return (
    <div data-testid="source-document-failed" style={CANVAS}>
      <span style={{ color: 'var(--status-red-text)', display: 'inline-flex' }}>{warnTriGlyph}</span>
      <div style={HEADING}>The document did not load</div>
      <p style={BODY}>
        Document storage did not return the file. The record below is intact — the filename, the size and the fingerprint
        all come from the ledger, not from the file itself.
      </p>
      <p style={BODY}>
        This does not mean the document is gone. Nothing in ASComply can delete a stored source document.
      </p>
      <span className="mono" data-testid="source-document-failure-line" style={{ fontSize: 11, letterSpacing: '0.06em', color: 'var(--status-red-text)' }}>
        {failureLine(error)}
      </span>
      {/* `Try again` and nothing beside it — the design's `Download original` goes with
          the button (Out of Scope). */}
      <button
        type="button"
        data-testid="source-document-retry"
        onClick={onRetry}
        className="v2-btn v2-btn-ghost pf-btn"
        style={{ marginTop: 6, height: 34, fontSize: 13, whiteSpace: 'nowrap', flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 7 }}
      >
        <span style={{ display: 'inline-flex' }}>{refreshGlyph}</span> Try again
      </button>
    </div>
  )
}

/** `AsyncStatus` has an `'empty'` state `LoadStatus` does not; the map must be total. */
export function toLoadStatus(status: AsyncStatus): LoadStatus {
  switch (status) {
    case 'idle':
      return 'idle'
    case 'loading':
      return 'loading'
    case 'error':
      return 'error'
    // The default `isEmpty` never marks an object empty, but a future predicate that did
    // would strand the modal at `loading` if this arm were missing.
    case 'empty':
    case 'ready':
      return 'ready'
  }
}
