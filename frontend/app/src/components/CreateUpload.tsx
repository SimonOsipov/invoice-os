// Create flow · step 1 — two independent entry points, deliberately separated:
//
//   1. IMPORT A SPREADSHEET (M4-08-04, the real path) — pick a portfolio entity, pick a
//      real .csv/.xlsx off disk, and let the SERVER read the columns. Multi-invoice.
//   2. IMPORT A SINGLE DOCUMENT (the shipped showcase path) — the sample PDF/JPG picker
//      feeding the local parse animation. Untouched here.
//
// They are stacked as two separate cards rather than merged into one because they do
// genuinely different things — one uploads a real file to a real endpoint under a real
// entity, the other replays a fixture — and a single blended panel would invite picking
// a sample file while an entity is selected and expecting a real import. The 5-step vs
// 3-step wizard header (wizardHeader) already reflects the split; this mirrors it.
// Ported shell from Platform.dc.html ~L407-448.

import { EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { SAMPLE_FILES } from '../data'
import { importGlyph, tickGlyph13 } from '../glyphs'
import { canReadColumns, hasImportableExtension } from '../lib/importFlow'
import { clientsViewState, listEntities, shouldFetchEntities, type Entity } from '../lib/portfolio'
import type { PlatformCtx } from '../types'

export function CreateUpload({ ctx }: { ctx: PlatformCtx }) {
  const { active, uploadFile, entityId, importFile, importError } = ctx
  const selFile = SAMPLE_FILES.find((f) => f.id === uploadFile) || null
  const hasFile = !!selFile

  // Entity LIST is local (it is this component's own fetch); the SELECTION lives on ctx,
  // because createImport fires from CreateMapping after this component has unmounted.
  // Same useAsync/listEntities idiom as ClientsView.tsx:38-42, including the no-gateway
  // short-circuit that keeps a gateway-less build at zero network.
  const base = gatewayBase()
  const list = useAsync<Entity[]>(
    () => (base ? listEntities(ctx.authedFetch, base) : Promise.reject(new Error('no gateway configured'))),
    { immediate: shouldFetchEntities(base) },
  )
  const entityState = clientsViewState(base, list)
  // `list.data ?? []` — asyncReducer nulls `data` on the 'empty' branch (async-state.ts:51).
  const entities = list.data ?? []

  // No active/archived filter: the server accepts any non-empty entity_id it can see
  // under RLS (importer/handlers.go:169-172), so filtering here would be a
  // stricter-than-server gate. Status is shown instead, so the choice is informed.
  const readReady = canReadColumns(entityId, importFile)
  const badExtension = importFile !== null && !hasImportableExtension(importFile.name)

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
        <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <span style={{ fontSize: 15, fontWeight: 600 }}>Import a spreadsheet</span>
          <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
            CSV · XLSX · MANY INVOICES
          </span>
        </div>
        <div style={{ padding: 20, display: 'flex', flexDirection: 'column', gap: 18 }}>
          <div>
            <div className="label" style={{ marginBottom: 8 }}>
              Bill under entity
            </div>
            {entityState === 'loading' && <Loading label="Loading entities…" />}
            {entityState === 'error' && list.error && <ErrorState error={list.error} onRetry={list.run} />}
            {entityState === 'empty' && <EmptyState title="No entities yet" message="Add a business entity in Clients before importing." />}
            {entityState === 'idle' && (
              <p style={{ fontSize: 12.5, color: 'var(--fg-3)', margin: 0, lineHeight: 1.55 }}>No gateway configured — importing is unavailable in this build.</p>
            )}
            {entityState === 'ready' && (
              <>
                <select
                  value={entityId ?? ''}
                  onChange={(e) => ctx.selectEntity(e.target.value || null)}
                  style={{ width: '100%', height: 40, padding: '0 10px', background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 6, color: 'var(--fg-1)', fontSize: 13.5, fontFamily: 'var(--font-sans)' }}
                >
                  <option value="">Select an entity…</option>
                  {entities.map((e) => (
                    <option key={e.id} value={e.id}>
                      {e.name}
                      {e.status === 'archived' ? ' · archived' : ''}
                    </option>
                  ))}
                </select>
                <p style={{ fontSize: 11.5, color: 'var(--fg-3)', margin: '7px 0 0', lineHeight: 1.5 }}>
                  Invoices are filed under this entity's TIN. It is never guessed from the workspace you are viewing.
                </p>
              </>
            )}
          </div>

          <div>
            <div className="label" style={{ marginBottom: 8 }}>
              Spreadsheet file
            </div>
            <input
              type="file"
              accept=".csv,.xlsx"
              onChange={(e) => ctx.selectImportFile(e.target.files?.[0] ?? null)}
              style={{ width: '100%', fontSize: 13, color: 'var(--fg-2)' }}
            />
            {importFile && !badExtension && (
              <p className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', margin: '8px 0 0' }}>
                {importFile.name}
              </p>
            )}
            {badExtension && (
              <p style={{ fontSize: 12, color: 'var(--status-red-text)', margin: '8px 0 0', lineHeight: 1.5 }}>
                {importFile?.name} is not a spreadsheet — choose a .csv or .xlsx file.
              </p>
            )}
          </div>

          {importError && (
            <p style={{ fontSize: 12.5, color: 'var(--status-red-text)', margin: 0, lineHeight: 1.5 }}>{importError.message}</p>
          )}

          <button
            onClick={ctx.readColumns}
            disabled={base == null || !readReady}
            className="v2-btn v2-btn-primary pf-btn"
            style={{ alignSelf: 'flex-start', height: 42, padding: '0 18px', justifyContent: 'center', background: readReady ? 'var(--accent)' : 'var(--bg-3)', color: readReady ? '#fff' : 'var(--fg-4)', cursor: readReady ? 'pointer' : 'not-allowed' }}
          >
            <span style={{ display: 'inline-flex' }}>{importGlyph}</span> Read columns
          </button>
        </div>
      </div>

      <div className="label">Or import a single document</div>

      <div className="pf-create-grid" style={{ display: 'grid', gridTemplateColumns: '1fr 320px', gap: 16, alignItems: 'start' }}>
        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
          <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
            <span style={{ fontSize: 15, fontWeight: 600 }}>Import a document · {active.short}</span>
            <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
              PDF · IMAGE
            </span>
          </div>
          <div style={{ padding: 20 }}>
            <div style={{ border: '1.5px dashed var(--line-3)', borderRadius: 10, padding: '30px 20px', display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center', background: 'var(--bg-1)', marginBottom: 22 }}>
              <span style={{ width: 48, height: 48, borderRadius: 10, background: 'var(--accent-tint)', color: 'var(--accent)', display: 'grid', placeItems: 'center', marginBottom: 14 }}>{importGlyph}</span>
              <div style={{ fontSize: 15, fontWeight: 600, marginBottom: 5 }}>Drag a file here, or pick a sample below</div>
              <p style={{ fontSize: 13, color: 'var(--fg-3)', margin: 0, maxWidth: 380, lineHeight: 1.55 }}>
                The parser extracts buyer details, line items and totals, then pre-fills the invoice for validation.
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
                    style={{ display: 'flex', alignItems: 'center', gap: 13, padding: '12px 14px', border: `1px solid ${sel ? 'var(--accent)' : 'var(--line-2)'}`, background: sel ? 'var(--accent-tint)' : 'var(--bg-2)', borderRadius: 8, width: '100%' }}
                  >
                    <span style={{ flex: 'none', width: 38, height: 38, borderRadius: 8, background: f.iconBg, color: f.iconColor, display: 'grid', placeItems: 'center', fontFamily: 'var(--font-mono)', fontSize: 10, fontWeight: 700, letterSpacing: '0.02em' }}>{f.ext}</span>
                    <div style={{ flex: 1, textAlign: 'left' }}>
                      <div style={{ fontSize: 13.5, fontWeight: 500, color: 'var(--fg-1)' }}>{f.name}</div>
                      <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 2 }}>
                        {f.meta}
                      </div>
                    </div>
                    <span style={{ flex: 'none', color: 'var(--accent)', display: 'inline-flex' }}>{sel ? tickGlyph13 : ''}</span>
                  </button>
                )
              })}
            </div>
          </div>
        </div>
        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, padding: 20, position: 'sticky', top: 0 }}>
          <div className="label" style={{ marginBottom: 14 }}>
            Selected file
          </div>
          {hasFile && selFile && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 11, padding: 12, border: '1px solid var(--line-1)', borderRadius: 8, background: 'var(--bg-1)', marginBottom: 18 }}>
              <span style={{ flex: 'none', width: 34, height: 34, borderRadius: 7, background: selFile.iconBg, color: selFile.iconColor, display: 'grid', placeItems: 'center', fontFamily: 'var(--font-mono)', fontSize: 9, fontWeight: 700 }}>{selFile.ext}</span>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontSize: 13, fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{selFile.name}</div>
                <div className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', marginTop: 2 }}>
                  {selFile.meta}
                </div>
              </div>
            </div>
          )}
          {!hasFile && (
            <p style={{ fontSize: 12.5, color: 'var(--fg-3)', margin: '0 0 18px', lineHeight: 1.55 }}>Pick a file to parse, or skip and build the invoice manually.</p>
          )}
          <button
            onClick={ctx.parseFile}
            className="v2-btn v2-btn-primary pf-btn"
            style={{ width: '100%', justifyContent: 'center', height: 42, background: hasFile ? 'var(--accent)' : 'var(--bg-3)', color: hasFile ? '#fff' : 'var(--fg-4)', cursor: hasFile ? 'pointer' : 'not-allowed' }}
          >
            <span style={{ display: 'inline-flex' }}>{importGlyph}</span> Upload &amp; parse
          </button>
          <button onClick={ctx.skipUpload} className="v2-btn v2-btn-ghost pf-btn" style={{ width: '100%', justifyContent: 'center', height: 38, marginTop: 10 }}>
            Skip — enter manually
          </button>
          <p style={{ fontSize: 11.5, color: 'var(--fg-3)', textAlign: 'center', margin: '12px 0 0', lineHeight: 1.5 }}>Parsed fields are editable before validation.</p>
        </div>
      </div>
    </div>
  )
}
