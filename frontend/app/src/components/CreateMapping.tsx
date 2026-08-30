// Create flow · step 2 — Map canonical invoice fields onto spreadsheet columns.
// Ported from Platform.dc.html ~L471-540 (markup) + ~L1549-1589 (render values).
//
// One spreadsheet row is one invoice LINE ITEM; rows group into invoices by the
// column mapped to `invoice_number`. Recognised columns arrive pre-placed and
// badged AUTO — except the invoice number, which is never guessed.
//
// Every column, sample cell and file fact on this screen now comes from the SERVER's
// preview response (M4-08-04, Core AC2) — the browser never parses the file. The whole
// derivation lives in previewColumns (lib/importFlow.ts) so it has a node oracle under
// the no-jsdom constraint and this component stays a dumb renderer with one call site.
//
// The guard below therefore requires `preview` and `mapping`: this screen is reachable
// only from the import path, which sets both.

import { CANON } from '../data'
import { recognize } from '../lib/mapping'
import { previewColumns } from '../lib/importFlow'
import { coverageSentence } from '../lib/mappingGroups'
import { runFailures, runIsActive } from '../lib/importRun'
import { gripGlyph, shieldGlyph, tickGlyph13, xSmallGlyph } from '../glyphs'
import type { PlatformCtx } from '../types'

export function CreateMapping({ ctx }: { ctx: PlatformCtx }) {
  const { active, preview, mapping, armedField, dragField, run, importError, entityId, groups, groupIndex, pickedFiles } = ctx
  if (!preview || !mapping) return null

  // BULK-01-04: which group this screen is showing, and the fileId -> filename lookup
  // coverageSentence (and the split control below) render off. `activeGroup` is
  // guaranteed non-null here in practice — `preview`/`mapping` above are themselves
  // `groups[groupIndex]?.… ?? null` reads, so the guard just above already requires it —
  // but it is looked up again rather than threaded through ctx as a fourth value, since
  // `groups`/`groupIndex` are already the two sources of truth ctx exposes.
  const activeGroup = groups[groupIndex] ?? null
  const names: Record<string, string> = Object.fromEntries(pickedFiles.map((pf) => [pf.id, pf.file.name]))
  const sentence = activeGroup ? coverageSentence(activeGroup, names) : ''
  // A short, honest label for the header row below — the full statement of which files
  // this mapping covers lives in the coverage-sentence block, not here. Falls back to
  // the first picked file's own name (BULK-01-05 deleted the `importFile` shim this used
  // to read) only if activeGroup is somehow null (see above).
  const fallbackFileName = pickedFiles[0]?.file.name ?? ''
  const primaryFileName = activeGroup ? (names[activeGroup.fileIds[0]] ?? fallbackFileName) : fallbackFileName

  const dropHot = !!(armedField || dragField)
  const recognized = recognize(preview.columns)

  // column header -> the field currently placed on it
  const colField: Record<string, string> = {}
  Object.keys(mapping).forEach((f) => {
    const h = mapping[f]
    if (h) colField[h] = f
  })

  // col.mappable is false for exactly one case: a blank-named column. '' is the
  // reserved unplaced sentinel toImportMapping strips, so a field dropped there would
  // vanish from the payload and import as NULL with no feedback at all — such a column
  // therefore gets no drop/click handler below. A whitespace-only header is NOT blocked:
  // the server's resolveMapping matches it exactly, so blocking it would be a
  // stricter-than-server gate.
  const columns = previewColumns(preview, 3).map((col) => {
    const fk = colField[col.header] || null
    const isAuto = !!fk && recognized[fk] === col.header
    return {
      ...col,
      field: fk,
      isAuto,
      colBg: fk ? (isAuto ? 'var(--action-tint)' : 'var(--action-tint)') : 'var(--bg-2)',
      tagBg: isAuto ? 'var(--status-green-bg)' : 'var(--action-tint)',
      tagBorder: isAuto ? 'var(--status-green-border)' : 'var(--action)',
      tagColor: isAuto ? 'var(--status-green-text)' : 'var(--action)',
      dropBorder: dropHot && !fk ? 'var(--action)' : 'var(--line-2)',
      dropBg: dropHot && !fk ? 'var(--action-tint)' : 'transparent',
    }
  })

  const fileExt = (() => {
    const dot = primaryFileName.lastIndexOf('.')
    return dot > 0 ? primaryFileName.slice(dot + 1).toUpperCase() : 'FILE'
  })()

  // BULK-01-05: mirrors CreateFlow's own runIsActive(run) body-swap gate — this screen
  // is never mounted while a run is actively running/finishing (CreateFlow renders
  // ImportProgress instead), so this stays the same always-false-in-practice guard it
  // was before. It is ALSO false on the 'failed' landing (AC #9, task-308 QA
  // correction) — that is the whole point of that status existing: Continue/Back must
  // be usable again once the operator is back here reading the per-file failures below.
  const uploading = runIsActive(run)

  // AC #9 (BULK-01-05 QA correction, task-308): every file in the run failed at the
  // request level and applyRoute (App.tsx) landed back on this screen instead of
  // resetting `run` — lib/importRun.ts's runFailures still returns them because only
  // `run.status` changed (to 'failed'), never `files`. Empty on every other path this
  // screen renders on (a fresh mapping, or mid-run — which `uploading` above shows
  // never actually happens here either).
  const failures = runFailures(run)

  const paletteChips = CANON.filter((c) => !mapping[c.key]).map((c) => {
    const armed = armedField === c.key
    return {
      key: c.key,
      required: !!c.required,
      bg: armed ? 'var(--action)' : c.required ? 'var(--status-red-bg)' : 'var(--bg-2)',
      border: armed ? 'var(--action)' : c.required ? 'var(--status-red-border)' : 'var(--line-2)',
      color: armed ? 'var(--text-on-dark)' : c.required ? 'var(--status-red-text)' : 'var(--fg-1)',
    }
  })

  const leftToPlace = paletteChips.length
  const allPlaced = leftToPlace === 0
  const invNumMapped = !!mapping.invoice_number
  const optionalUnmapped = paletteChips.filter((c) => !c.required).length

  // Every fact here is the server's, including the nullable pair — delimiter/encoding
  // are JSON null for xlsx, and interpolating them raw would print the literal "null".
  const mapFacts = `DELIMITER ${preview.delimiter ?? '—'} · ${preview.encoding ?? '—'} · ${preview.rows_total} ROWS · ${preview.columns.length} COLS`
  // No invoice count: how many invoices these rows resolve to is the SERVER's verdict,
  // reported after the import, and computing it in the browser first is exactly the
  // duplicated-judgement this story removes (Core AC3). Rows are the honest unit here.
  // The commit gate of the SPREADSHEET path — and the one step of it that genuinely needs
  // an entity. (A document run commits on the upload step instead, and gates on the entity
  // there: CreateUpload.tsx's `readReady`.)
  // Upload and mapping do not (the preview endpoint takes the file alone), but the import
  // writes import_batches.entity_id and invoices.entity_id, both NOT NULL, so with no
  // linked entity there is nothing to file the rows against: startImport() returns early
  // and the click does nothing whatsoever. A workspace with no entity resolved yet —
  // either persona, task-304 AC-2/AC-3 — is exactly that case, and unlike the
  // unmapped-invoice-number case below the user cannot resolve it from THIS SCREEN (there
  // is no entity picker here, whatever route exists elsewhere) — hence a real `disabled`,
  // not just not-allowed styling, and copy that names the reason rather than a button
  // that looks armed and silently swallows the click.
  const canFile = entityId !== null
  // The armed branch is the footer half of INVCR-01-05's arm-on-click: clicking the
  // continue control with invoice_number unplaced no longer swallows the click, it arms
  // the field (App.tsx's continueMapping). The columns light up on their own — `dropHot`
  // above is driven by armedField — so this note is what tells the user what the lit
  // columns are FOR. It sits after the !canFile branch on purpose: with no entity there
  // is nothing to place a field towards, and the continue control is genuinely disabled
  // there, so this state is unreachable in that workspace anyway.
  const invNumArmed = armedField === 'invoice_number' && !invNumMapped
  const mapNote = !canFile
    ? { text: 'Columns read. Filing is unavailable in this workspace — it has no linked business entity to file the invoices against.', color: 'var(--status-muted-text)' }
    : invNumArmed
      ? { text: 'invoice_number is armed — click the column that holds it. Nothing continues until you place it by hand.', color: 'var(--action)' }
      : !invNumMapped
        ? { text: 'Drag invoice_number onto a column to continue — the invoice number is never guessed for you.', color: 'var(--status-red-text)' }
        : optionalUnmapped > 0
          ? { text: `${optionalUnmapped} optional field${optionalUnmapped === 1 ? '' : 's'} still unplaced — unmapped fields import as empty and are judged by the rule engine.`, color: 'var(--status-muted-text)' }
          : { text: 'All fields mapped.', color: 'var(--status-green-text)' }
  // BULK-01-04: on every group EXCEPT the last, clicking Continue only advances to the
  // next group's mapping screen (App.tsx's continueMapping) — it does not import
  // anything yet. Claiming `Import N rows` there would be a lie about what the click
  // does, the same class of dishonesty [shared-mapping-shown] forbids for silent
  // sharing. Gated on `groups`/`groupIndex` (BULK-01-04) rather than a single-group
  // literal so a single-group run (the common case) still reads exactly as shipped.
  const isLastGroup = groupIndex >= groups.length - 1
  const continueBtn = !canFile
    ? { bg: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed', label: 'Filing needs a linked entity' }
    : {
        bg: invNumMapped ? 'var(--action)' : 'var(--bg-3)',
        color: invNumMapped ? 'var(--text-on-dark)' : 'var(--fg-4)',
        cursor: invNumMapped ? 'pointer' : 'not-allowed',
        label: invNumMapped
          ? isLastGroup
            ? `Import ${preview.rows_total} rows`
            : 'Continue to next file'
          : 'Map invoice number to continue',
      }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
        <div style={{ padding: '14px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
          <span className="card-title">Map fields to columns · {active.short}</span>
          {allPlaced ? (
            <span className="mono" style={{ fontSize: 11, color: 'var(--status-green-text)' }}>ALL FIELDS PLACED</span>
          ) : (
            <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>{leftToPlace} TO PLACE</span>
          )}
        </div>
        <div style={{ padding: '14px 20px 18px' }}>
          <div style={{ display: 'flex', alignItems: 'flex-start', gap: 9, padding: '11px 12px', background: 'var(--bg-1)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-input)', marginBottom: 14 }}>
            <span style={{ flex: 'none', color: 'var(--fg-3)', marginTop: 1 }}>{shieldGlyph}</span>
            <p style={{ fontSize: 12, color: 'var(--fg-2)', margin: 0, lineHeight: 1.5 }}>
              Drag each field onto the column that holds its data — or click a field, then a column. One spreadsheet row is a single line item; rows group into invoices by the column mapped to{' '}
              <span className="mono" style={{ fontSize: 11 }}>invoice_number</span>. Supplier details come from {active.short}, not the file. Recognised columns are pre-placed and marked{' '}
              <span className="mono" style={{ fontSize: 10, color: 'var(--status-green-text)' }}>AUTO</span> — the invoice number is never guessed.
            </p>
          </div>
          {allPlaced ? (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, color: 'var(--status-green-text)' }}>
              <span style={{ display: 'inline-flex' }}>{tickGlyph13}</span> Every field is placed on a column.
            </div>
          ) : (
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 9 }}>
              {paletteChips.map((c) => (
                <button
                  key={c.key}
                  draggable
                  onDragStart={(e) => {
                    try {
                      e.dataTransfer.setData('text/plain', c.key)
                      e.dataTransfer.effectAllowed = 'move'
                    } catch {
                      /* dataTransfer unavailable — click-to-place still works */
                    }
                    ctx.setDrag(c.key)
                  }}
                  onDragEnd={() => ctx.endDrag()}
                  onClick={() => ctx.armField(c.key)}
                  className="pf-btn"
                  style={{ display: 'inline-flex', alignItems: 'center', gap: 7, cursor: 'grab', fontFamily: 'var(--font-mono)', fontSize: 11.5, fontWeight: 600, letterSpacing: '0.02em', textTransform: 'uppercase', padding: '8px 12px', borderRadius: 'var(--radius-input)', background: c.bg, border: `1px solid ${c.border}`, color: c.color }}
                >
                  <span style={{ display: 'inline-flex', opacity: 0.6 }}>{gripGlyph}</span>
                  {c.key}
                  {c.required && <span style={{ fontWeight: 700 }}>*</span>}
                </button>
              ))}
            </div>
          )}
        </div>
      </div>

      {/* Coverage sentence + group pager + split control (BULK-01-04, Core AC 3, decision
          [shared-mapping-shown]) — additive only, inserted between the palette card and
          the column-grid card; neither of those two cards nor anything inside them is
          touched. Renders UNCONDITIONALLY, including for a group of one
          ([coverage-sentence-is-unconditional]): showing it only when this group covers
          more than one file would make its absence read as "nothing is shared", which is
          exactly the silent share this decision forbids.

          <span className="mono">, never a new <div className="mono"> — e2e/topology/
          import-wizard.spec.ts's E2E-02 asserts `page.locator('main div.mono')` resolves
          to exactly ONE element, the column header cell below (:210 in this file). */}
      {activeGroup && (
        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: '12px 20px', display: 'flex', flexDirection: 'column', gap: 8 }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
            <p style={{ fontSize: 12.5, color: 'var(--fg-2)', margin: 0, lineHeight: 1.5 }}>{sentence}</p>
            {groups.length > 1 && (
              <span className="mono" style={{ flex: 'none', fontSize: 10.5, color: 'var(--fg-3)', letterSpacing: '0.03em', whiteSpace: 'nowrap' }}>
                GROUP {groupIndex + 1} OF {groups.length}
              </span>
            )}
          </div>
          {/* Split is a no-op on a single-file group (lib/mappingGroups.ts's splitOut),
              so the control renders only where it can do something. */}
          {activeGroup.fileIds.length > 1 && (
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
              {activeGroup.fileIds.map((fid) => (
                <button
                  key={fid}
                  type="button"
                  onClick={() => ctx.splitOutFile(fid)}
                  className="pf-btn"
                  style={{ fontSize: 11.5, fontWeight: 600, padding: '5px 10px', borderRadius: 'var(--radius-input)', background: 'var(--bg-1)', border: '1px solid var(--line-2)', color: 'var(--fg-2)', cursor: 'pointer' }}
                >
                  Map {names[fid] ?? fid} separately
                </button>
              ))}
            </div>
          )}
        </div>
      )}

      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
        <div style={{ padding: '12px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
          <span style={{ display: 'flex', alignItems: 'center', gap: 10, minWidth: 0 }}>
            <span style={{ flex: 'none', width: 30, height: 30, borderRadius: 'var(--radius-md)', background: 'var(--bg-3)', color: 'var(--fg-3)', display: 'grid', placeItems: 'center', fontFamily: 'var(--font-mono)', fontSize: 8.5, fontWeight: 700 }}>{fileExt}</span>
            {/* A short, representative label — the full statement of which files this
                mapping covers is the coverage sentence above, not this row. Reading off
                `primaryFileName` (the active group's own first covered file) rather than
                a bare `pickedFiles[0]` matters here: that fallback is always the FIRST
                picked file, so on any group past the first this row would otherwise keep
                naming a file that already finished mapping — a visible contradiction
                with the correct coverage sentence one line above it. */}
            <span style={{ fontSize: 13.5, fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {primaryFileName}
              {activeGroup && activeGroup.fileIds.length > 1 ? ` +${activeGroup.fileIds.length - 1} more` : ''}
            </span>
          </span>
          <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', letterSpacing: '0.03em', whiteSpace: 'nowrap' }}>{mapFacts}</span>
        </div>
        <div style={{ overflowX: 'auto' }}>
          <div style={{ display: 'flex', minWidth: 'min-content' }}>
            {columns.map((col, ci) => (
              <div
                // Keyed by INDEX, not header: the preview returns headers verbatim and
                // duplicates are preserved, so a header key would collide and
                // mis-associate drop targets between two columns of the same name.
                key={ci}
                onDrop={
                  col.mappable
                    ? (e) => {
                        e.preventDefault()
                        ctx.dropOn(col.header)
                      }
                    : undefined
                }
                onDragOver={col.mappable ? (e) => e.preventDefault() : undefined}
                onClick={col.mappable ? () => ctx.clickCol(col.header) : undefined}
                style={{ flex: 'none', width: 150, borderRight: '1px solid var(--line-1)', background: col.colBg, cursor: col.mappable ? 'pointer' : 'not-allowed', opacity: col.mappable ? 1 : 0.6 }}
              >
                <div style={{ height: 22, display: 'grid', placeItems: 'center', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)' }}>
                  <span className="mono" style={{ fontSize: 9.5, color: 'var(--fg-4)', fontWeight: 600 }}>{col.letter}</span>
                </div>
                <div style={{ padding: '8px 9px', borderBottom: '1px solid var(--line-1)', minHeight: 66 }}>
                  <div className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', marginBottom: 6 }}>{col.header}</div>
                  {!col.mappable ? (
                    <div style={{ display: 'grid', placeItems: 'center', height: 30, border: '1px dashed var(--line-2)', borderRadius: 'var(--radius-sm)' }}>
                      <span style={{ fontSize: 10, color: 'var(--fg-4)', textAlign: 'center', lineHeight: 1.3 }}>unnamed — not mappable</span>
                    </div>
                  ) : col.field ? (
                    <span
                      draggable
                      onDragStart={(e) => {
                        try {
                          e.dataTransfer.setData('text/plain', col.field as string)
                          e.dataTransfer.effectAllowed = 'move'
                        } catch {
                          /* dataTransfer unavailable — click-to-place still works */
                        }
                        ctx.setDrag(col.field as string)
                      }}
                      style={{ display: 'inline-flex', alignItems: 'center', gap: 5, maxWidth: '100%', cursor: 'grab', background: col.tagBg, border: `1px solid ${col.tagBorder}`, borderRadius: 'var(--radius-sm)', padding: '3px 6px' }}
                    >
                      <span className="mono" style={{ fontSize: 9, fontWeight: 700, color: col.tagColor, letterSpacing: '0.01em', textTransform: 'uppercase', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{col.field}</span>
                      {col.isAuto && (
                        <span className="mono" style={{ flex: 'none', fontSize: 7.5, fontWeight: 700, color: 'var(--status-green-text)', border: '1px solid var(--status-green-border)', borderRadius: 'var(--radius-sm)', padding: '0 3px' }}>AUTO</span>
                      )}
                      <span
                        onClick={(e) => {
                          e.stopPropagation()
                          ctx.unmap(col.header)
                        }}
                        style={{ flex: 'none', cursor: 'pointer', color: col.tagColor, display: 'inline-flex' }}
                      >
                        {xSmallGlyph}
                      </span>
                    </span>
                  ) : (
                    <div style={{ display: 'grid', placeItems: 'center', height: 30, border: `1px dashed ${col.dropBorder}`, borderRadius: 'var(--radius-sm)', background: col.dropBg }}>
                      <span style={{ fontSize: 10.5, color: 'var(--fg-4)' }}>drop field</span>
                    </div>
                  )}
                </div>
                {col.samples.map((v, i) => (
                  <div key={i} style={{ height: 30, display: 'flex', alignItems: 'center', padding: '0 9px', borderBottom: '1px solid var(--line-1)' }}>
                    <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{v}</span>
                  </div>
                ))}
              </div>
            ))}
          </div>
        </div>
      </div>

      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 16, background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: '14px 20px' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 20, minWidth: 0 }}>
          <span style={{ display: 'flex', alignItems: 'center', gap: 8, flex: 'none' }}>
            <span style={{ fontSize: 12.5, color: 'var(--fg-3)' }}>Rows to import</span>
            <span className="mono" style={{ fontSize: 15, fontWeight: 700, color: 'var(--action)' }}>{preview.rows_total}</span>
          </span>
          {/* AC #9: a run that just landed here with every file failed takes priority
              over importError/mapNote — those are both about THIS screen's live
              mapping state, not a run that already ran and failed. filename + the
              server's own message verbatim, one line per failed file (runFailures).
              <span className="mono">, never <div className="mono"> — e2e/topology/
              import-wizard.spec.ts's E2E-02 asserts `main div.mono` resolves to
              exactly the one column-header cell further down this file. */}
          {failures.length > 0 ? (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              {failures.map((f, i) => (
                <span key={i} style={{ fontSize: 12, color: 'var(--status-red-text)', lineHeight: 1.4 }}>
                  <span className="mono" style={{ fontSize: 11 }}>{f.name}</span>: {f.message}
                </span>
              ))}
            </div>
          ) : importError ? (
            <span style={{ fontSize: 12, color: 'var(--status-red-text)', lineHeight: 1.4 }}>{importError.message}</span>
          ) : (
            <span style={{ fontSize: 12, color: mapNote.color, lineHeight: 1.4 }}>{mapNote.text}</span>
          )}
        </div>
        <div style={{ display: 'flex', gap: 10, flex: 'none', alignItems: 'center' }}>
          {/* The in-flight indicator used to live here — a determinate byte bar with an
              indeterminate spinner fallback. INVCR-01-05 moved it out to ImportProgress,
              which body-swaps this whole screen (CreateFlow) rather than tucking the
              state of the request into the corner of a footer whose every control is
              disabled while it runs. The reasoning about WHAT such an indicator may
              honestly claim — no stage list, no row counter, no percentage — moved with
              it and is recorded at the top of that file. */}
          <button onClick={ctx.backToImport} disabled={uploading} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 42, padding: '0 16px' }}>
            ← Back to import
          </button>
          <button
            onClick={ctx.continueMapping}
            disabled={uploading || !canFile}
            className="v2-btn pf-btn"
            style={{ height: 42, padding: '0 18px', justifyContent: 'center', background: continueBtn.bg, color: continueBtn.color, cursor: continueBtn.cursor }}
          >
            {continueBtn.label}
          </button>
        </div>
      </div>
    </div>
  )
}
