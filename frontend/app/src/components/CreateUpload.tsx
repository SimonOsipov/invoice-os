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
//
// INVCR-01-05 tells the entity-less user that EARLIER, in the amber panel below — and
// the panel is INFORMATIONAL, never blocking. It is added to the card; it replaces
// nothing and disables nothing. Making it a gate would re-create the very regression
// the paragraph above records ([inhouse-can-start]) and would falsify importFlow.ts's
// stated contract ("Preview gate = file only; commit gate = entity"). Nothing upstream
// of `Read columns` may ever acquire an entity check — the dropzone, the file input and
// the button stay live for both personas, whatever this panel says.
// Ported shell from Platform.dc.html ~L407-448.

import { useState } from 'react'

import { gatewayBase } from '@invoice-os/api-client'

import { importGlyph } from '../glyphs'
import { canReadColumns, hasImportableExtension } from '../lib/importFlow'
import type { PlatformCtx } from '../types'

export function CreateUpload({ ctx }: { ctx: PlatformCtx }) {
  const { active, importFile, importError, activeEntity, entitiesState, entities, clients, mode } = ctx
  const [dragOver, setDragOver] = useState(false)

  // `activeEntity`, not `active.entityId` — the same resolved-object predicate every
  // filing gate reads ([gate-on-the-resolved-entity]), so this panel and the Map step's
  // refusal can never disagree about whether an entity exists.
  //
  // The two guards below both exist for ONE reason: this panel is loud and amber, so a
  // single frame of it on a firm user who does have an entity is a visible lie. A
  // disabled button can afford to be wrong for a frame; this cannot.
  //
  // 1. The FETCH must have answered. activeEntity is also null while entities are in
  //    flight ('idle' | 'loading') and when the fetch failed ('error'); only
  //    'ready' | 'empty' mean the answer is definitive. 'idle' additionally covers the
  //    no-gateway build, which has no answer to give at all.
  //
  // 2. The ROSTER must have caught up with it. App.tsx derives `clients` from the fetched
  //    entities through a useEffect, so it lands one render LATE: on the render where the
  //    fetch resolves, entitiesState is already 'ready' while `clients` is still [],
  //    which collapses `active` to the emptyClient() placeholder and `activeEntity` to
  //    null. Guard 1 alone therefore still admits exactly one frame of amber — reading
  //    "No client has none", the placeholder's own name — and useEffect runs after paint,
  //    so that frame can genuinely reach the screen. buildClients is a 1:1 map, so a
  //    non-empty entity list with an empty roster is only ever that in-between render and
  //    this can never stick.
  //
  // Deliberately NOT `entities.length === 0`, which would have been the easy version of
  // guard 2: it would also hide the panel in a real steady state — a firm workspace whose
  // own entity has been archived out of the roster while other entities remain — where
  // there genuinely is nothing to file against. These guards remove a frame; they must
  // not remove a case.
  const entityAnswerSettled = entitiesState === 'ready' || entitiesState === 'empty'
  const rosterCatchingUp = entities.length > 0 && clients.length === 0
  const noEntity = activeEntity === null && entityAnswerSettled && !rosterCatchingUp

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
                {/* The string here used to read "one row per invoice", which is simply
                    FALSE: one row is one LINE ITEM, and rows group into invoices by the
                    column mapped to invoice_number — exactly what the next step says
                    (CreateMapping.tsx's own prose) and what the server does. A file of
                    five rows can be one invoice or five. Stating the grain here is the
                    whole point of this copy: it is the fact that decides whether the
                    user's spreadsheet is shaped right at all, and learning it a step
                    later is learning it too late. */}
                <p style={{ fontSize: 13, color: 'var(--fg-3)', margin: 0, maxWidth: 420, lineHeight: 1.55 }}>
                  The parser extracts buyer details, line items and totals. One row is one line item; rows group into invoices by the column you map to{' '}
                  <span className="mono" style={{ fontSize: 11.5 }}>invoice_number</span> — one invoice or five hundred, the same way.
                </p>
              </>
            )}
          </label>

          {/* A <span>, and it lives OUTSIDE the label on purpose — see the placement
              note on the panel below. The file input's own accept="" is the real gate;
              this is the human-readable statement of it, not a second source of truth. */}
          <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-4)', letterSpacing: '0.06em' }}>
            ACCEPTED · CSV · XLSX
          </span>

          {/* ⚠️ PLACEMENT IS LOAD-BEARING: everything here renders AFTER </label>, never
              between the <input class="pf-file"> and it. app-layer.css's dropzone focus
              ring is `.asc-app .pf-file:focus-visible + label` — an ADJACENT-sibling
              selector — so a single element inserted between the two silently kills the
              keyboard focus ring on the only control this step has. No test covers that;
              the failure is invisible except to a keyboard user.

              The panel itself is informational (see the header note): it states the fact
              early instead of letting the user map every column first and meet the
              refusal at the commit. It disables nothing — `Read columns` below is
              deliberately still live, because reading columns genuinely does not need an
              entity. */}
          {noEntity && (
            <div style={{ padding: '12px 14px', borderRadius: 'var(--radius-md)', background: 'var(--status-amber-bg)', border: '1px solid var(--status-amber-border)', color: 'var(--status-amber-text)' }}>
              <div style={{ fontSize: 13, fontWeight: 600, marginBottom: 5 }}>No linked business entity</div>
              <p style={{ fontSize: 12.5, margin: 0, lineHeight: 1.55 }}>
                An import is filed on behalf of a registered entity. {active.short} has none, so there is nothing to file against. Reading a file&rsquo;s columns still works — the refusal lands on the last step, where the invoices would be written.
                {mode === 'inhouse' && ' There is no way to link one from an in-house workspace yet, so imports cannot be filed here.'}
              </p>
              {/* Firm only. NAV_CLIENTS is in the firm-only sidebar group and
                  EntityFormModal mounts from ClientsView alone, so an in-house user
                  offered this link would be handed a control that goes nowhere they can
                  act — exactly the dead end this panel exists to replace. Clients, NOT
                  Settings: SettingsView has no entity form at all.
                  Navigating away discards a file the user may have picked; nothing has
                  been uploaded at this point, so there is nothing to lose but the pick. */}
              {mode === 'firm' && (
                <button
                  onClick={() => ctx.nav('clients')}
                  className="pf-btn"
                  style={{ marginTop: 9, background: 'none', border: 0, padding: 0, fontFamily: 'var(--font-sans)', fontSize: 12.5, fontWeight: 600, color: 'var(--status-amber-text)', textDecoration: 'underline', cursor: 'pointer' }}
                >
                  Link a business entity →
                </button>
              )}
            </div>
          )}

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

          {/* Only alongside the panel, and only to close the obvious escape hatch: a
              reader who has just been told imports cannot be filed will reach for the
              button directly above. Manual entry carries the SAME requirement —
              fileDraftGate refuses on a null resolved entity and CreateForm's primary
              renders disabled — so implying otherwise would send them one screen further
              to meet an identical wall. The button stays enabled: the form is worth
              reaching, and it names its own reason when it gets there. */}
          {noEntity && (
            <p style={{ fontSize: 12, color: 'var(--fg-3)', margin: 0, lineHeight: 1.5 }}>
              Manual entry has the same requirement — an invoice is filed against a registered entity too.
            </p>
          )}
        </div>
      </div>
    </div>
  )
}
