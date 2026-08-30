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
// workspaces had a permanently-null entityId (no business_entities row at all, and no
// route to create one), so an in-house accountant could not open the wizard's first step
// at all while the firm could — task-304 fixed both halves (App.tsx resolves a real
// entity when one exists; Settings gained a Company panel, [entity-picker], to create the
// first one). But nothing about reading a spreadsheet's columns ever needed an entity in
// the first place: the preview endpoint takes the file alone. The gate therefore sits
// where the entity is genuinely required — the commit, on the Map step (CreateMapping's
// continueBtn + App.tsx's startImport). Both personas get the dropzone and Read columns;
// only filing differs, and only for a workspace with no resolved entity yet, either mode.
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
import { computeNoEntity, hasImportableExtension } from '../lib/importFlow'
import { canReadColumnsAll } from '../lib/importRun'
import type { PlatformCtx } from '../types'

export function CreateUpload({ ctx }: { ctx: PlatformCtx }) {
  const { active, pickedFiles, filesRefusal, importError, activeEntity, entitiesState, entities, clients, mode } = ctx
  const [dragOver, setDragOver] = useState(false)

  // `activeEntity`, not `active.entityId` — the same resolved-object predicate every
  // filing gate reads ([gate-on-the-resolved-entity]), so this panel and the Map step's
  // refusal can never disagree about whether an entity exists.
  //
  // task-304 (INVCR-01-19): the derivation itself moved to lib/importFlow.ts's
  // computeNoEntity (unchanged logic, this story only extracted it) so it is
  // node-testable under the no-jsdom constraint — this component stays unrenderable in
  // the vitest suite, but the predicate it reads is not, which is how AC-6 ("the panel
  // must not become dead code or in-house-only") stays provable once
  // [inhouse-can-start]'s browser test — this codebase's only e2e coverage of the panel —
  // stops being reachable with a genuinely-zero-entity persona (every fixture this suite
  // can sign in as now has at least one entity, db/seed.dev.sql). See computeNoEntity's
  // own doc comment for the two guards (fetch settled, roster caught up) and why a
  // FIRM workspace whose active entity has been archived out of the roster is still a
  // real, non-dead case this must keep firing for.
  const noEntity = computeNoEntity(activeEntity, entitiesState, entities.length, clients.length)

  // `base` gates the "Read columns" button below (gateway-wide): with no gateway
  // configured there is nothing to POST the preview to.
  const base = gatewayBase()

  const readReady = canReadColumnsAll(pickedFiles)
  // Aggregate over the whole selection, for the dropzone's border cue only — the
  // per-file note (rendered after the label, alongside the chosen-files list) is what
  // actually names which file is bad.
  const anyBadExtension = pickedFiles.some((pf) => !hasImportableExtension(pf.file.name))

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
            accept=".csv,.xlsx,.pdf,.png,.jpg,.jpeg,.webp,.docx"
            multiple
            aria-label="Choose files to import"
            onChange={(e) => ctx.addPickedFiles(Array.from(e.target.files ?? []))}
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
              ctx.addPickedFiles(Array.from(e.dataTransfer.files))
            }}
            style={{
              display: 'flex',
              flexDirection: 'column',
              alignItems: 'center',
              textAlign: 'center',
              border: `1.5px dashed ${anyBadExtension ? 'var(--status-red-border)' : dragOver ? 'var(--action)' : 'var(--line-3)'}`,
              borderRadius: 'var(--radius-md)',
              padding: '30px 20px',
              background: dragOver ? 'var(--action-tint)' : 'var(--bg-1)',
              cursor: 'pointer',
            }}
          >
            <span style={{ width: 48, height: 48, borderRadius: 'var(--radius-md)', background: 'var(--action-tint)', color: 'var(--action)', display: 'grid', placeItems: 'center', marginBottom: 14 }}>
              {importGlyph}
            </span>
            {pickedFiles.length > 0 ? (
              <>
                <div className="mono" style={{ fontSize: 14, fontWeight: 600, marginBottom: 5, color: 'var(--fg-1)' }}>
                  {pickedFiles.length} file{pickedFiles.length === 1 ? '' : 's'} selected
                </div>
                <p style={{ fontSize: 13, color: 'var(--fg-3)', margin: 0, maxWidth: 380, lineHeight: 1.55 }}>
                  Selected — click Read columns below, drop more files to add them, or remove one from the list below.
                </p>
              </>
            ) : (
              <>
                <div style={{ fontSize: 15, fontWeight: 600, marginBottom: 5 }}>{dragOver ? 'Drop to select' : 'Drag files here, or click to choose'}</div>
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
                  <span className="mono" style={{ fontSize: 11.5 }}>invoice_number</span> — one invoice or five hundred, the same way. Pick up to five files per run.
                </p>
              </>
            )}
          </label>

          {/* A <span>, and it lives OUTSIDE the label on purpose — see the placement
              note on the panel below. It states ACCEPTED_PICKED_TYPES (lib/importFlow.ts)
              in human terms, not a second source of truth; PICKER-2 pins it to the accept
              attribute. Neither is the gate: a DROPPED file never meets accept (onDrop
              above hands it straight to addPickedFiles), so classifyPickedFile is. */}
          <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-4)', letterSpacing: '0.06em' }}>
            ACCEPTED · CSV · XLSX · PDF · PNG · JPG · JPEG · WEBP · DOCX
          </span>

          {/* The chosen-files list, per-file remove control and per-file bad-extension
              note (BULK-01-03, Core AC 1). A bad-extension file is still listed here —
              addFiles never drops on extension, only on the five-file count cap — so the
              user can see and remove it rather than wonder why it silently vanished. */}
          {pickedFiles.length > 0 && (
            <ul style={{ display: 'flex', flexDirection: 'column', gap: 6, margin: 0, padding: 0, listStyle: 'none' }}>
              {pickedFiles.map((pf) => {
                const badExtension = !hasImportableExtension(pf.file.name)
                return (
                  <li
                    key={pf.id}
                    style={{
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      gap: 10,
                      padding: '8px 12px',
                      borderRadius: 'var(--radius-md)',
                      border: `1px solid ${badExtension ? 'var(--status-red-border)' : 'var(--line-1)'}`,
                      background: 'var(--bg-1)',
                    }}
                  >
                    <div style={{ minWidth: 0 }}>
                      <div className="mono" style={{ fontSize: 13, fontWeight: 600, color: 'var(--fg-1)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                        {pf.file.name}
                      </div>
                      {badExtension && (
                        <p style={{ fontSize: 11.5, color: 'var(--status-red-text)', margin: '2px 0 0', lineHeight: 1.4 }}>
                          Unsupported file type — choose one of the accepted types above.
                        </p>
                      )}
                    </div>
                    <button
                      type="button"
                      onClick={() => ctx.removePickedFile(pf.id)}
                      className="pf-btn"
                      style={{ flex: 'none', background: 'none', border: 0, padding: 0, fontFamily: 'var(--font-sans)', fontSize: 12, fontWeight: 600, color: 'var(--fg-3)', textDecoration: 'underline', cursor: 'pointer' }}
                    >
                      Remove
                    </button>
                  </li>
                )
              })}
            </ul>
          )}

          {/* Cap-refusal (BULK-01-03, Core AC 1) — capRefusal's text verbatim, sole copy
              owner in lib/importRun.ts. Never a silent truncation: whenever addPickedFiles
              drops any incoming file past MAX_RUN_FILES, this names the cap and the count. */}
          {filesRefusal && (
            <p style={{ fontSize: 12.5, color: 'var(--status-amber-text)', margin: 0, lineHeight: 1.5 }}>{filesRefusal}</p>
          )}

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
              </p>
              {/* task-304 (INVCR-01-19): unconditional now for both personas — firm's
                  destination is the Clients page (NAV_CLIENTS, ClientsView's own
                  EntityFormModal), in-house's is the Settings > Company panel
                  (SettingsView, AC-4, the SAME EntityFormModal). Neither used to be a
                  dead end; in-house's own in-house-only refusal sentence that used to sit
                  in the paragraph above is deleted outright rather than reworded — it is
                  simply false now that this button has a real destination for that
                  persona too.
                  Navigating away discards a file the user may have picked; nothing has
                  been uploaded at this point, so there is nothing to lose but the pick. */}
              <button
                onClick={() => {
                  if (mode === 'inhouse') ctx.setSettingsTab('company')
                  ctx.nav(mode === 'inhouse' ? 'settings' : 'clients')
                }}
                className="pf-btn"
                style={{ marginTop: 9, background: 'none', border: 0, padding: 0, fontFamily: 'var(--font-sans)', fontSize: 12.5, fontWeight: 600, color: 'var(--status-amber-text)', textDecoration: 'underline', cursor: 'pointer' }}
              >
                Link a business entity →
              </button>
            </div>
          )}

          {importError && (
            <p style={{ fontSize: 12.5, color: 'var(--status-red-text)', margin: 0, lineHeight: 1.5 }}>{importError.message}</p>
          )}

          <div style={{ display: 'flex', gap: 10, alignItems: 'center' }}>
            <button
              onClick={ctx.readAllColumns}
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
