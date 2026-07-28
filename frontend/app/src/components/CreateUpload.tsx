// Create flow · step 1 — ONE live import surface (spreadsheet upload, server-backed,
// M4-08-04: previewImport -> mapping -> createImport) plus a sandbox-only preview of
// the mock document-parse path. Previously two stacked cards implied two real import
// methods; only the spreadsheet path ever touched a server — the document card is a
// local setInterval fixture over SAMPLE_FILES with zero network and no OCR/parse
// endpoint behind it, so it now renders only behind ctx.sandbox, explicitly labelled
// as a preview, and its dashed panel no longer claims to accept a dragged file (it
// never had a drop handler).
//
// The entity picker is gone too: entityId now mirrors ctx.active.entityId (the
// company already chosen via the workspace switcher) instead of a second, separate
// dropdown — that dropdown's list included archived entities the switcher
// deliberately hides, so it could file a LIVE import under a company the switcher
// itself would never let you select. A null active.entityId (in-house mode, or the
// loading/error/zero-entity window before the switcher resolves) renders an explicit
// blocked state rather than a permanently-disabled button with no explanation.
// Ported shell from Platform.dc.html ~L407-448.

import { useState } from 'react'

import { EmptyState, gatewayBase } from '@invoice-os/api-client'

import { SAMPLE_FILES } from '../data'
import { importGlyph, tickGlyph13 } from '../glyphs'
import { canReadColumns, hasImportableExtension } from '../lib/importFlow'
import type { PlatformCtx } from '../types'

export function CreateUpload({ ctx }: { ctx: PlatformCtx }) {
  const { active, mode, sandbox, uploadFile, entityId, importFile, importError } = ctx
  const selFile = SAMPLE_FILES.find((f) => f.id === uploadFile) || null
  const hasFile = !!selFile
  const [dragOver, setDragOver] = useState(false)

  // `base` still gates the "Read columns" button below (gateway-wide) — with no
  // gateway configured, entitiesAsync never fires, `active` falls back to
  // emptyClient() (entityId: null), and the blocked state below already covers that
  // case; this is the second, button-level guard for the same condition.
  const base = gatewayBase()

  const readReady = canReadColumns(entityId, importFile)
  const badExtension = importFile !== null && !hasImportableExtension(importFile.name)
  // active.entityId is string | null (types.ts) — null for in-house mode (no
  // business_entities rows by design) and for the loading/error/zero-entity fallback
  // (lib/clients.ts's emptyClient()) before the switcher resolves.
  const blocked = active.entityId === null

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
          {blocked ? (
            <EmptyState
              title="No linked entity"
              message={
                mode === 'inhouse'
                  ? 'In-house workspaces have no linked business entity — import is unavailable here.'
                  : "This workspace isn't linked to a business entity yet. Add one in Clients before importing."
              }
            />
          ) : (
            <>
              <input
                id="pf-import-file"
                className="pf-file"
                type="file"
                accept=".csv,.xlsx"
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
            </>
          )}

          <div style={{ display: 'flex', gap: 10, alignItems: 'center' }}>
            {!blocked && (
              <button
                onClick={ctx.readColumns}
                disabled={base == null || !readReady}
                className="v2-btn v2-btn-primary pf-btn"
                style={{ height: 42, padding: '0 18px', justifyContent: 'center', background: readReady ? 'var(--action)' : 'var(--bg-3)', color: readReady ? 'var(--text-on-dark)' : 'var(--fg-4)', cursor: readReady ? 'pointer' : 'not-allowed' }}
              >
                <span style={{ display: 'inline-flex' }}>{importGlyph}</span> Read columns
              </button>
            )}
            {/* Load-bearing: the only from-scratch creation path (manual entry) must
                render in BOTH live and sandbox, and independent of the entity guard
                above — approve() is already a no-op for a null entityId. */}
            <button onClick={ctx.skipUpload} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 38, padding: '0 16px' }}>
              Skip — enter manually
            </button>
          </div>
        </div>
      </div>

      {sandbox && (
        <>
          <div className="label">Or import a single document</div>

          <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
            <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
              <span className="card-title">Import a document · {active.short}</span>
              <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
                PREVIEW · SANDBOX
              </span>
            </div>
            <div style={{ padding: 20 }}>
              <div style={{ border: '1.5px dashed var(--line-3)', borderRadius: 'var(--radius-md)', padding: '30px 20px', display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center', background: 'var(--bg-1)', marginBottom: 22 }}>
                <span style={{ width: 48, height: 48, borderRadius: 'var(--radius-md)', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center', marginBottom: 14 }}>{importGlyph}</span>
                <div style={{ fontSize: 15, fontWeight: 600, marginBottom: 5 }}>Preview: parse a sample document</div>
                <p style={{ fontSize: 13, color: 'var(--fg-3)', margin: 0, maxWidth: 380, lineHeight: 1.55 }}>
                  The parser extracts buyer details, line items and totals, then pre-fills the invoice for validation. Sample files only — this preview can't read a real upload.
                </p>
              </div>
              <div className="label" style={{ marginBottom: 12 }}>
                Sample files
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
                {SAMPLE_FILES.map((f) => {
                  const sel = uploadFile === f.id
                  return (
                    <button
                      key={f.id}
                      onClick={() => ctx.selectFile(f.id)}
                      className="pf-upcard"
                      style={{ display: 'flex', alignItems: 'center', gap: 13, padding: '12px 14px', border: `1px solid ${sel ? 'var(--action)' : 'var(--line-2)'}`, background: sel ? 'var(--action-tint)' : 'var(--bg-2)', borderRadius: 'var(--radius-md)', width: '100%' }}
                    >
                      <span style={{ flex: 'none', width: 38, height: 38, borderRadius: 'var(--radius-md)', background: f.iconBg, color: f.iconColor, display: 'grid', placeItems: 'center', fontFamily: 'var(--font-mono)', fontSize: 10, fontWeight: 700, letterSpacing: '0.02em' }}>{f.ext}</span>
                      <div style={{ flex: 1, textAlign: 'left' }}>
                        <div style={{ fontSize: 13.5, fontWeight: 500, color: 'var(--fg-1)' }}>{f.name}</div>
                        <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 2 }}>
                          {f.meta}
                        </div>
                      </div>
                      <span style={{ flex: 'none', color: 'var(--action)', display: 'inline-flex' }}>{sel ? tickGlyph13 : ''}</span>
                    </button>
                  )
                })}
              </div>
              <button
                onClick={ctx.parseFile}
                disabled={!hasFile}
                className="v2-btn v2-btn-primary pf-btn"
                style={{ width: '100%', justifyContent: 'center', height: 42, marginTop: 18, background: hasFile ? 'var(--action)' : 'var(--bg-3)', color: hasFile ? 'var(--text-on-dark)' : 'var(--fg-4)', cursor: hasFile ? 'pointer' : 'not-allowed' }}
              >
                <span style={{ display: 'inline-flex' }}>{importGlyph}</span> Upload &amp; parse
              </button>
              <p style={{ fontSize: 11.5, color: 'var(--fg-3)', textAlign: 'center', margin: '12px 0 0', lineHeight: 1.5 }}>Parsed fields are editable before validation.</p>
            </div>
          </div>
        </>
      )}
    </div>
  )
}
