// The four non-sheet canvases of the source-document previewer, plus the small pure
// helpers the card, the modal header and the rail all read.
//
// The design's `Download original` / `Download original file` actions and every fragment
// of copy presupposing a download are cut with the button: nothing in this build can
// download a stored document, and this is an evidence surface.

import type { CSSProperties, ReactNode } from 'react'

import type { ApiError, AsyncStatus } from '@invoice-os/api-client'

import { docGlyph, refreshGlyph } from '../glyphs'
import { Icon } from '../icons'
import { actorLabel } from '../lib/actor'
import { fmtDate, fmtDateTime } from '../lib/format'
import {
  classifyDocument,
  formatBytes,
  type LoadStatus,
  type SourceDocumentRecord,
  type SourceDocumentResponse,
} from '../lib/sourceDocument'

// The prototype's own warn triangle at 15 (stroke 1.6 is the Icon default); `warnTriGlyph` is a different path at 16.
const triangle15 = <Icon paths={['M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0Z', 'M12 9v4', 'M12 17h.01']} size={15} />

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
  display: 'grid',
  placeItems: 'center',
  padding: 32,
}

const HEADING: CSSProperties = { fontSize: 19, fontWeight: 700, letterSpacing: '-0.02em', color: 'var(--fg-1)', margin: '0 0 9px' }
const BODY: CSSProperties = { fontSize: 13.5, lineHeight: 1.6, color: 'var(--fg-2)', margin: '0 0 8px' }
const BODY_LAST: CSSProperties = { ...BODY, margin: '0 0 20px' }

function Tile({ bg, fg, children }: { bg: string; fg: string; children: ReactNode }) {
  return (
    <span style={{ width: 44, height: 44, borderRadius: 'var(--radius-md)', background: bg, color: fg, display: 'grid', placeItems: 'center', marginBottom: 16 }}>
      {children}
    </span>
  )
}

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
      <div style={{ width: '100%', maxWidth: 540 }}>
        <Tile bg="var(--bg-3)" fg="var(--fg-3)">
          {docGlyph}
        </Tile>
        <div style={HEADING}>There is no source document</div>
        <p style={BODY_LAST}>
          {invoiceNumber} was typed into ASComply{by} on {fmtDate(createdAt)}. No file was uploaded, so there is nothing
          to preview — the state strip is the record of how far this invoice has come.
        </p>
        <div style={{ border: '1px dashed var(--line-3)', borderRadius: 'var(--radius-md)', padding: '16px 18px', background: 'var(--bg-2)' }}>
          <div className="label" style={{ marginBottom: 8 }}>
            Why this is not an error
          </div>
          <p style={{ margin: 0, fontSize: 12.5, lineHeight: 1.6, color: 'var(--fg-2)' }}>
            A source document can only arrive through an import run. Invoices entered by hand — a one-off, a correction, a
            customer whose system exports nothing — never gain one, and one can never be attached later.
          </p>
        </div>
      </div>
    </div>
  )
}

const SKELETON_ROW: CSSProperties = { height: 26, borderRadius: 'var(--radius-md)', background: 'var(--bg-3)' }

export function LoadingCanvas({ sizeBytes }: { sizeBytes: number | null }) {
  return (
    <div data-testid="source-document-loading" style={{ height: '100%', display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
      <div style={{ flex: 'none', display: 'flex', alignItems: 'center', gap: 12, padding: '11px 16px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-2)' }}>
        {sizeBytes != null && (
          <span className="mono" style={{ flex: 'none', whiteSpace: 'nowrap', fontSize: 10, letterSpacing: '0.06em', color: 'var(--fg-3)' }}>
            READING {formatBytes(sizeBytes)} FROM DOCUMENT STORAGE
          </span>
        )}
        {/* A static track: the prototype renders no fill, and the pulsing rows carry progress. */}
        <span style={{ flex: 1, height: 4, borderRadius: 4, background: 'var(--bg-3)' }} />
      </div>
      <div style={{ flex: 1, minHeight: 0, overflow: 'hidden', padding: '14px 16px' }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 9 }}>
          {Array.from({ length: 8 }, (_, i) => (
            <span key={i} style={{ ...SKELETON_ROW, animation: 'pulse 1.5s linear infinite' }} />
          ))}
          {[1, 0.6, 0.35].map((opacity) => (
            <span key={opacity} style={{ ...SKELETON_ROW, opacity }} />
          ))}
        </div>
        <p style={{ fontSize: 12.5, color: 'var(--fg-3)', margin: '20px 0 0', lineHeight: 1.55, maxWidth: 460 }}>
          The file record and its fingerprint are already known — only the bytes are still on their way. Nothing about this
          document can change while it loads.
        </p>
      </div>
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
      <div style={{ width: '100%', maxWidth: 560 }}>
        <Tile bg="var(--status-amber-bg)" fg="var(--status-amber-text)">
          {triangle15}
        </Tile>
        <div style={HEADING}>This file is stored, but we cannot render it here</div>
        <p style={BODY}>
          ASComply has no reader that can display this format in the browser. The bytes are intact and the file is exactly
          as it was uploaded.
        </p>
        <p style={BODY_LAST}>
          We keep files we cannot read on purpose. A file that broke an import is the one an auditor asks about, so it is
          never discarded.
        </p>
        <div style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', overflow: 'hidden', marginBottom: 20 }}>
          <div style={{ padding: '10px 14px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)' }}>
            <span className="label">What we know about this file</span>
          </div>
          <div style={{ padding: '4px 0' }}>
            {facts.map(([label, value]) => (
              <div key={label} style={{ display: 'grid', gridTemplateColumns: '96px 1fr', gap: 12, padding: '8px 14px', alignItems: 'baseline' }}>
                <span className="mono" style={{ fontSize: 9.5, fontWeight: 700, letterSpacing: '0.07em', color: 'var(--fg-3)' }}>
                  {label}
                </span>
                <span style={{ fontSize: 12.5, color: 'var(--fg-2)', lineHeight: 1.45, wordBreak: 'break-all' }}>{value}</span>
              </div>
            ))}
          </div>
        </div>
        <p style={{ margin: 0, fontSize: 12, color: 'var(--fg-3)', lineHeight: 1.55 }}>
          A file that failed to import never produces an invoice at all — those files stay on their import run.
        </p>
      </div>
    </div>
  )
}

export function FailedCanvas({ error, onRetry }: { error: ApiError | null; onRetry: () => void }) {
  return (
    <div data-testid="source-document-failed" style={CANVAS}>
      <div style={{ width: '100%', maxWidth: 520 }}>
        <Tile bg="var(--status-red-bg)" fg="var(--status-red-text)">
          {triangle15}
        </Tile>
        <div style={HEADING}>The document did not load</div>
        <p style={BODY}>
          Document storage did not return the file. The record below is intact — the filename, the size and the fingerprint
          all come from the ledger, not from the file itself.
        </p>
        <p style={BODY_LAST}>
          This does not mean the document is gone. Nothing in ASComply can delete a stored source document.
        </p>
        <div
          className="mono"
          data-testid="source-document-failure-line"
          style={{ display: 'inline-flex', alignItems: 'center', gap: 8, background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', padding: '8px 11px', fontSize: 11, color: 'var(--fg-2)', marginBottom: 20 }}
        >
          {failureLine(error)}
        </div>
        {/* `Try again` and nothing beside it — the design's `Download original` goes with
            the button (Out of Scope). */}
        <div>
          <button
            type="button"
            data-testid="source-document-retry"
            onClick={onRetry}
            className="v2-btn v2-btn-ghost pf-btn"
            style={{ height: 36, whiteSpace: 'nowrap', flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 7 }}
          >
            <span style={{ display: 'inline-flex' }}>{refreshGlyph}</span> Try again
          </button>
        </div>
      </div>
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
