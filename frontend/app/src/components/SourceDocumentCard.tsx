// The invoice-detail right-rail entry point. Card recipe is
// the rail's universal one (--bg-2 / --line-1 / --radius-md) so the stack reads as one.
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

// Resolved by InvoiceDetail, never here. Three states, not two: a lookup that failed has
// established no more than an unfinished one, and neither may claim that no job exists.
export type ExtractionEntry = { jobId: string | null; loading: boolean; failed: boolean }

const NO_EXTRACTION = 'This document has no extraction to check.'
const LOOKUP_FAILED = 'We could not check this document for an extraction.'

export function SourceDocumentCard({ meta, onOpen, extraction, onOpenExtraction }: { meta: SourceDocumentAsync; onOpen: () => void; extraction: ExtractionEntry; onOpenExtraction: (jobId: string) => void }) {
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
        <div style={{ padding: '13px 14px', marginBottom: 12, border: '1px dashed var(--line-3)', borderRadius: 'var(--radius-md)', background: 'transparent' }}>
          <div style={{ fontSize: 12.5, fontWeight: 600, marginBottom: 4 }}>No source document</div>
          <div style={{ fontSize: 12, lineHeight: 1.5, color: 'var(--fg-2)' }}>
            This invoice was typed into ASComply. There is no uploaded file behind it.
          </div>
        </div>
        <button
          type="button"
          data-testid="why-no-source-document"
          onClick={onOpen}
          className="v2-btn v2-btn-ghost pf-btn"
          style={{ width: '100%', height: 34, fontSize: 13, justifyContent: 'center' }}
        >
          Why there is no file
        </button>
      </>
    )
  } else {
    const kind = classifyDocument(record.filename, record.declared_content_type)
    const tone = fileTypeTone(record.filename, record.declared_content_type)
    const hasJob = !extraction.loading && !extraction.failed && extraction.jobId !== null
    // One sentence per SETTLED arm. An unsettled lookup has established nothing, so it gets
    // the disabled control and no reason at all.
    const blockedReason = extraction.loading ? null : extraction.failed ? LOOKUP_FAILED : extraction.jobId === null ? NO_EXTRACTION : null
    body = (
      <>
        <div style={{ display: 'flex', gap: 11, marginBottom: 13 }}>
          <span style={{ flex: 'none', width: 38, height: 38, borderRadius: 'var(--radius-md)', background: tone.bg, color: tone.fg, display: 'grid', placeItems: 'center' }}>
            {docGlyph2}
          </span>
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ fontSize: 13, fontWeight: 600, wordBreak: 'break-all', lineHeight: 1.35 }}>
              {record.filename ?? 'Filename not recorded'}
            </div>
            <div className="mono" data-testid="source-document-card-meta" style={{ marginTop: 3, fontSize: 9.5, letterSpacing: '0.05em', color: 'var(--fg-3)' }}>
              {formatLabel(record.filename, record.declared_content_type)} · {formatBytes(record.size_bytes)}
            </div>
          </div>
        </div>
        <p data-testid="source-document-range" style={{ margin: '0 0 13px', fontSize: 12, lineHeight: 1.5, color: 'var(--fg-2)' }}>
          {describeSourceRows(response.source_rows, kind)}
        </p>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <button
            type="button"
            data-testid="view-source-document"
            onClick={onOpen}
            className="v2-btn v2-btn-ghost pf-btn"
            style={{ width: '100%', height: 34, fontSize: 13, justifyContent: 'center' }}
          >
            View source document
          </button>
          {/* Disabled-with-a-visible-reason, never hidden and never a `title=` — a title on a
              disabled button is invisible in Chromium (APPR-16). Inline background/border
              because `.v2-btn-ghost:hover` (v2/app-layer.css) carries no `!important` and is
              not guarded by `:not(:disabled)`. */}
          <button
            type="button"
            data-testid="open-extraction-review"
            onClick={() => extraction.jobId !== null && onOpenExtraction(extraction.jobId)}
            disabled={!hasJob}
            className="v2-btn v2-btn-ghost pf-btn"
            style={{
              width: '100%',
              height: 34,
              fontSize: 13,
              justifyContent: 'center',
              ...(hasJob ? null : { background: 'var(--bg-3)', borderColor: 'var(--line-1)', color: 'var(--fg-4)', cursor: 'not-allowed' }),
            }}
          >
            Check the extraction
          </button>
          {blockedReason !== null && (
            <p style={{ margin: 0, fontSize: 12.5, lineHeight: 1.55, color: 'var(--fg-3)' }}>{blockedReason}</p>
          )}
        </div>
        <div className="mono" style={{ marginTop: 12, paddingTop: 11, borderTop: '1px solid var(--line-1)', fontSize: 10.5, color: 'var(--fg-3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          SHA-256 {shortHash(record.content_hash)}
        </div>
      </>
    )
  }

  return (
    <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
      <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10 }}>
        <span className="card-title">Source document</span>
        <span className="mono" style={{ flex: 'none', fontSize: 9, fontWeight: 700, letterSpacing: '0.09em', color: 'var(--fg-3)' }}>
          READ ONLY
        </span>
      </div>
      <div data-testid="source-document-card" style={{ padding: '15px 18px 16px' }}>
        {body}
      </div>
    </div>
  )
}
