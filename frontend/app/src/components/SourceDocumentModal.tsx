// The previewer shell: the widest member of the app's existing modal family (1340px,
// full height), its header, the canvas/rail body grid, and the state dispatch.
//
// Rendered inline by LiveInvoiceDetail, never portalled to document.body: `--bg-*` /
// `--fg-*` / `--status-*` are declared on `.asc-app` (app-layer.css:25-27), so markup
// outside that tree resolves none of them.
//
// The shell owns all three load channels because `sourceDocumentState` needs all three
// statuses at once. It also owns every object URL it creates for that URL's whole life:
// a canvas is handed a `blob:` string, never the handle, so only this file can revoke.

import { useEffect, useRef, type ReactNode } from 'react'

import { gatewayBase, useAsync, type ApiError } from '@invoice-os/api-client'

import { closeGlyph, docGlyph } from '../glyphs'
import { fmtPlain } from '../lib/format'
import {
  classifyDocument,
  fetchDocumentBytes,
  formatBytes,
  getDocumentSheet,
  sourceDocumentState,
  type DocumentBytes,
  type DocumentKind,
  type DocumentSheet,
} from '../lib/sourceDocument'
import { useDismiss } from '../lib/useDismiss'
import { SourceDocumentImage, SourceDocumentPdf } from './SourceDocumentPages'
import { SourceDocumentRail } from './SourceDocumentRail'
import { SourceDocumentSheet } from './SourceDocumentSheet'
import {
  FailedCanvas,
  formatLabel,
  fileTypeTone,
  LoadingCanvas,
  NoSourceCanvas,
  toLoadStatus,
  UnrenderableCanvas,
  type SourceDocumentAsync,
} from './SourceDocumentStates'
import type { PlatformCtx } from '../types'

const RAIL_WIDTH = 316

function countLabel(n: number, singular: string): string {
  return `${fmtPlain(n)} ${n === 1 ? singular : `${singular}S`}`
}

export function SourceDocumentModal({
  ctx,
  meta,
  invoiceNumber,
  invoiceCreatedAt,
  createdBy,
  createdByResolved,
  onClose,
}: {
  ctx: PlatformCtx
  meta: SourceDocumentAsync
  invoiceNumber: string
  invoiceCreatedAt: string | null
  /** The invoice's first history actor — already fetched by the detail, so zero new network. */
  createdBy: string | null
  /** That row's server-resolved actor pair, which outranks the client persona table. */
  createdByResolved?: { name: string; kind: string }
  /** Must be STABLE — it is a `useDismiss` dependency (useDismiss.ts:39-55). */
  onClose: () => void
}) {
  const base = gatewayBase()
  const record = meta.data?.document ?? null
  const documentId = record?.id ?? null
  const kind: DocumentKind | null = record ? classifyDocument(record.filename, record.declared_content_type) : null

  // One channel per kind, and nothing at all for an unrenderable file — `sourceDocumentState`
  // short-circuits before consulting a channel for that kind.
  const sheet = useAsync<DocumentSheet>(
    () =>
      base && documentId
        ? getDocumentSheet(ctx.authedFetch, base, documentId)
        : Promise.reject(new Error('no source document')),
    { immediate: base != null && documentId != null && kind === 'spreadsheet', deps: [documentId, kind] },
  )
  // `useAsync`'s runId discards a superseded DISPATCH, but `fetchDocumentBytes` created the
  // object URL before the promise resolved (lib/sourceDocument.ts:98) — so the handle itself
  // is already on the floor. Registering every handle and releasing everything that is not
  // the rendered one is total: `release()` is idempotent, so resolution order cannot matter.
  const created = useRef<DocumentBytes[]>([])
  const disposed = useRef(false)

  const bytes = useAsync<DocumentBytes>(
    async () => {
      if (!(base && documentId && kind)) throw new Error('no source document')
      const h = await fetchDocumentBytes(ctx.getToken, base, documentId, kind, record?.filename ?? null)
      if (disposed.current) h.release()
      else created.current.push(h)
      return h
    },
    { immediate: base != null && documentId != null && (kind === 'pdf' || kind === 'image'), deps: [documentId, kind] },
  )

  // Empty at mount, so StrictMode's doubled cleanup releases nothing and the second setup
  // clears `disposed` again.
  useEffect(() => {
    disposed.current = false
    return () => {
      disposed.current = true
      for (const h of created.current) h.release()
      created.current = []
    }
  }, [])

  const handle = bytes.data
  useEffect(() => {
    for (const h of created.current) if (h !== handle) h.release()
    created.current = handle ? [handle] : []
  }, [handle])

  useDismiss(true, onClose)

  const sheetData = kind === 'spreadsheet' ? sheet.data : null
  const state = sourceDocumentState(
    { status: toLoadStatus(meta.status), value: meta.data },
    toLoadStatus(sheet.status),
    toLoadStatus(bytes.status),
  )

  // Retry re-runs whichever channel actually failed, meta first.
  const failed: { error: ApiError | null; run: () => void } =
    meta.status === 'error' ? meta : kind === 'spreadsheet' ? sheet : bytes

  let canvas: ReactNode = null
  if (state === 'no-source') {
    canvas = (
      <NoSourceCanvas
        invoiceNumber={invoiceNumber}
        createdAt={invoiceCreatedAt}
        createdBy={createdBy}
        createdByResolved={createdByResolved}
      />
    )
  } else if (state === 'loading') {
    canvas = <LoadingCanvas sizeBytes={record?.size_bytes ?? null} />
  } else if (state === 'failed') {
    canvas = <FailedCanvas error={failed.error} onRetry={failed.run} />
  } else if (state === 'unrenderable' && record) {
    canvas = <UnrenderableCanvas record={record} />
  } else if (state === 'spreadsheet' && sheetData) {
    // `key` is the only guard against component-state staleness: scope, scrollTop and the
    // auto-scroll effect would otherwise survive a document change.
    canvas = (
      <SourceDocumentSheet
        key={documentId ?? ''}
        sheet={sheetData}
        sourceRows={meta.data?.source_rows ?? null}
        otherInvoiceRows={record?.other_invoice_rows ?? []}
      />
    )
  } else if (state === 'pdf' && handle) {
    canvas = <SourceDocumentPdf key={documentId ?? ''} url={handle.url} />
  } else if (state === 'image' && handle) {
    // Keyed for the same reason the sheet is: the image canvas holds zoom state that must
    // not survive a document change.
    canvas = <SourceDocumentImage key={documentId ?? ''} url={handle.url} filename={record?.filename ?? null} />
  }

  const tone = record ? fileTypeTone(record.filename, record.declared_content_type) : { bg: 'var(--bg-3)', fg: 'var(--fg-3)' }
  const metaParts = record ? [formatLabel(record.filename, record.declared_content_type), formatBytes(record.size_bytes)] : []
  if (record && sheetData) {
    metaParts.push(countLabel(sheetData.rows_total, 'ROW'), countLabel(sheetData.columns.length, 'COLUMN'))
  }

  return (
    <div
      onClick={onClose}
      style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'oklch(20% .02 210 / 0.42)', backdropFilter: 'blur(2px)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 40, animation: 'popIn 140ms ease-out' }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label="Source document"
        data-testid="source-document-modal"
        style={{ width: '100%', maxWidth: 1340, height: '100%', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', boxShadow: '0 24px 60px -20px oklch(20% .02 210 / 0.4)', display: 'flex', flexDirection: 'column', overflow: 'hidden' }}
      >
        <div style={{ flex: 'none', padding: '14px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', gap: 14 }}>
          <span style={{ flex: 'none', width: 40, height: 40, borderRadius: 'var(--radius-sm)', background: tone.bg, color: tone.fg, display: 'grid', placeItems: 'center' }}>
            {docGlyph}
          </span>
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 9 }}>
              <span style={{ fontSize: 14.5, fontWeight: 600, color: 'var(--fg-1)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {record ? (record.filename ?? 'Filename not recorded') : 'Source document'}
              </span>
              {record && (
                <span className="mono" style={{ flex: 'none', whiteSpace: 'nowrap', fontSize: 9.5, fontWeight: 600, letterSpacing: '0.06em', borderRadius: 999, padding: '2px 8px', background: 'var(--action-tint)', color: 'var(--action)' }}>
                  IMMUTABLE RECORD
                </span>
              )}
            </div>
            <div className="mono" data-testid="source-document-meta" style={{ marginTop: 3, fontSize: 10.5, letterSpacing: '0.05em', color: 'var(--fg-3)' }}>
              {record ? metaParts.join(' · ') : 'NO FILE'}
            </div>
          </div>
          {/* No ghost `Download original` and no `DOWNLOAD STARTED` flash — both go with
              the download action (Out of Scope). Close is the only header control. */}
          <button
            type="button"
            onClick={onClose}
            className="pf-btn"
            aria-label="Close"
            data-testid="source-modal-close"
            style={{ flex: 'none', width: 34, height: 34, border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}
          >
            {closeGlyph}
          </button>
        </div>

        {/* THREE load-bearing declarations, not one. `minmax(0, 1fr)` constrains the ROW;
            an unconstrained flex item still takes a content-based automatic minimum, so
            without `flex: 1` + `minHeight: 0` the grid grows to the virtualised sheet's
            full height, the inner viewport measures that, and the window renders every
            row. SourceDocumentModal.test.tsx spec 1 is the regression guard. */}
        <div
          data-testid="source-document-modal-body"
          style={{ display: 'grid', gridTemplateColumns: `minmax(0, 1fr) ${RAIL_WIDTH}px`, gridTemplateRows: 'minmax(0, 1fr)', flex: 1, minHeight: 0, overflow: 'hidden' }}
        >
          {/* The canvas cell needs its own `minWidth`/`minHeight: 0` for the same reason
              the grid does — a grid item's automatic minimum is content-based. DOC-02-06
              and DOC-02-07 render here off `sheetData` and `handle`, both already held
              above: neither canvas fetches for itself. */}
          <div data-testid="source-document-canvas" style={{ background: 'var(--bg-1)', borderRight: '1px solid var(--line-1)', minWidth: 0, minHeight: 0, overflow: 'hidden' }}>
            {canvas}
          </div>
          <SourceDocumentRail
            ctx={ctx}
            record={record}
            invoiceNumber={invoiceNumber}
            sheetRowsTotal={sheetData?.rows_total ?? null}
          />
        </div>
      </div>
    </div>
  )
}
