// Create flow · step 1 — THE import surface: one spreadsheet upload, server-backed
// (M4-08-04: previewImport -> mapping -> createImport). It is the only card here, and
// every pixel of it talks to a real endpoint.
//
// It used to be joined by a second, sandbox-gated "import a document" card: a local
// setInterval fixture over two hardcoded sample filenames, with zero network and no
// OCR/parse endpoint behind it. INVCR-01-01 deleted it outright rather than leave a fake
// parse anchoring a five-step strip that will never ship in that shape ([one-flow],
// [prereq-delete-mock]). Real document ingestion is explicitly out of scope (§14) — do
// not reintroduce a client-side stand-in for it.
//
// The entity picker is gone too: entityId now mirrors ctx.active.entityId (the
// company already chosen via the workspace switcher) instead of a second, separate
// dropdown — that dropdown's list included archived entities the switcher
// deliberately hides, so it could file a LIVE import under a company the switcher
// itself would never let you select.
//
// That coupling had a side effect this file used to enshrine: a null active.entityId
// blanked the WHOLE upload surface behind a "No linked entity" empty state. In-house
// workspaces have a permanently-null entityId (inhouseClient() hardcodes it — there is
// no business_entities row, and no Clients screen to create one from), so an in-house
// accountant could not open the wizard's first step at all while the firm could. But
// nothing about reading a spreadsheet's columns needs an entity: the preview endpoint
// takes the file alone. The gate therefore now sits where the entity is genuinely
// required — the commit, on the Map step (CreateMapping's continueBtn + App.tsx's
// startImport). Both personas get the dropzone and Read columns; only filing differs.
// Ported shell from Platform.dc.html ~L407-448.

import { useState } from 'react'

import { gatewayBase } from '@invoice-os/api-client'

import { importGlyph } from '../glyphs'
import { canReadColumns, hasImportableExtension } from '../lib/importFlow'
import type { PlatformCtx } from '../types'

export function CreateUpload({ ctx }: { ctx: PlatformCtx }) {
  const { active, importFile, importError } = ctx
  const [dragOver, setDragOver] = useState(false)

  // `base` gates the "Read columns" button below (gateway-wide): with no gateway
  // configured there is nothing to POST the preview to.
  const base = gatewayBase()

  const readReady = canReadColumns(importFile)
  const badExtension = importFile !== null && !hasImportableExtension(importFile.name)

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
        <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <span className="card-title">Import invoices · {active.short}</span>
          <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
            TIN {active.tin}
          </span>
        </div>
        <div style={{ padding: 20, display: 'flex', flexDirection: 'column', gap: 18 }}>
          {/* aria-label is load-bearing, not decoration. input[type=file] has an
              implicit role of BUTTON, and its accessible name is computed from its
              label — which now wraps the whole dropzone, so once a file is chosen
              that name would swallow the zone's prose ("…click Read columns
              below…"). getByRole('button', {name: 'Read columns'}) substring-matches,
              so the input collided with the real Read-columns button and every
              import spec died on a strict-mode violation. An explicit aria-label
              wins over the label in the accname algorithm: it keeps the name short,
              stable and free of any other control's name, and stops a screen reader
              announcing a paragraph of prose as the button's name. */}
          <input
            id="pf-import-file"
            className="pf-file"
            type="file"
            accept=".csv,.xlsx"
            aria-label="Choose a spreadsheet to import"
            onChange={(e) => ctx.selectImportFile(e.target.files?.[0] ?? null)}
          />
          <label
            htmlFor="pf-import-file"
            onDragOver={(e) => {
              e.preventDefault()
              setDragOver(true)
            }}
            // dragleave bubbles up from the label's OWN children (icon, copy), so a
            // bare handler drops the highlight every time the pointer crosses one and
            // the zone flickers for the whole drag. Only clear once the pointer has
            // actually left the label's subtree — relatedTarget is null when it
            // leaves the window, and contains(null) is false, so that still clears.
            onDragLeave={(e) => {
              if (e.currentTarget.contains(e.relatedTarget as Node | null)) return
              setDragOver(false)
            }}
            onDrop={(e) => {
              e.preventDefault()
              setDragOver(false)
              ctx.selectImportFile(e.dataTransfer.files[0] ?? null)
            }}
            style={{
              display: 'flex',
              flexDirection: 'column',
              alignItems: 'center',
              textAlign: 'center',
              border: `1.5px dashed ${badExtension ? 'var(--status-red-border)' : dragOver ? 'var(--action)' : 'var(--line-3)'}`,
              borderRadius: 'var(--radius-md)',
              padding: '30px 20px',
              background: dragOver ? 'var(--action-tint)' : 'var(--bg-1)',
              cursor: 'pointer',
            }}
          >
            <span style={{ width: 48, height: 48, borderRadius: 'var(--radius-md)', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center', marginBottom: 14 }}>
              {importGlyph}
            </span>
            {badExtension ? (
              <p style={{ fontSize: 12, color: 'var(--status-red-text)', margin: 0, lineHeight: 1.5 }}>
                {importFile?.name} is not a spreadsheet — choose a .csv or .xlsx file.
              </p>
            ) : importFile ? (
              <>
                <div className="mono" style={{ fontSize: 14, fontWeight: 600, marginBottom: 5, color: 'var(--fg-1)' }}>
                  {importFile.name}
                </div>
                <p style={{ fontSize: 13, color: 'var(--fg-3)', margin: 0, maxWidth: 380, lineHeight: 1.55 }}>
                  Selected — click Read columns below, or drop a different file to replace it.
                </p>
              </>
            ) : (
              <>
                <div style={{ fontSize: 15, fontWeight: 600, marginBottom: 5 }}>{dragOver ? 'Drop to select' : 'Drag a spreadsheet here, or click to choose'}</div>
                <p style={{ fontSize: 13, color: 'var(--fg-3)', margin: 0, maxWidth: 380, lineHeight: 1.55 }}>
                  .csv or .xlsx, one row per invoice — the server reads your columns on the next step.
                </p>
              </>
            )}
          </label>

          {importError && (
            <p style={{ fontSize: 12.5, color: 'var(--status-red-text)', margin: 0, lineHeight: 1.5 }}>{importError.message}</p>
          )}

          <div style={{ display: 'flex', gap: 10, alignItems: 'center' }}>
            <button
              onClick={ctx.readColumns}
              disabled={base == null || !readReady}
              className="v2-btn v2-btn-primary pf-btn"
              style={{ height: 42, padding: '0 18px', justifyContent: 'center', background: readReady ? 'var(--action)' : 'var(--bg-3)', color: readReady ? 'var(--text-on-dark)' : 'var(--fg-4)', cursor: readReady ? 'pointer' : 'not-allowed' }}
            >
              <span style={{ display: 'inline-flex' }}>{importGlyph}</span> Read columns
            </button>
            {/* Load-bearing: the only from-scratch creation path (manual entry) must
                render in BOTH live and sandbox — and for both personas. */}
            <button onClick={ctx.skipUpload} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 38, padding: '0 16px' }}>
              Skip — enter manually
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
