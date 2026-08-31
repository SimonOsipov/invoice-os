// Create flow · the in-flight import card. Replaces the determinate bar + "Working…"
// spinner that used to sit in CreateMapping's footer: the wrong shape for what this
// request actually is, and squeezed into a row where it read as a detail of the button
// beside it rather than the state of the whole screen.
//
// BULK-01-05 (task-308) turned this from a single-file card into a PER-FILE LIST: one
// row per file in the run, in run order (lib/importRun.ts's runFileRows), live while the
// run is active (CreateFlow's body-swap gate, runIsActive(run) — true for 'running' and
// the brief 'finished' tick before a route is chosen). The rewrite is additive to the
// card's own honesty rules, not a relaxation of them.
//
// It does NOT stay on screen once EVERY file has failed (AC #9, corrected by QA under
// this same task-308 after the first cut of this rewrite shipped it as a dead end):
// applyRoute (App.tsx) lands a `none` route on `run.status: 'failed'`
// (lib/importRun.ts's markRunFailed) rather than 'finished', and runIsActive treats
// 'failed' like 'idle' — so CreateFlow swaps back to the step router and CreateMapping
// renders again, showing the same per-file failures (runFailures) in its own footer
// instead of here.
//
// Every status traces to an observation: an upload phase the transport reports, an
// extraction_jobs.state off GET /v1/extractions (EXTR-07), a request in flight, or a settled
// count or reason. Two carve-outs, both deliberate. QUEUED is the absence of a report rather
// than a reading -- on the document path a row reads QUEUED while its bytes are still
// uploading. And the poll-budget refusal is the one status derived from elapsed time; it says
// extraction CONTINUES rather than advancing a stage. A percentage and a byte counter ARE
// observable (xhr.upload.onprogress -> uploadPercent) and still refused: bytes UPLOADED hit
// 100% while the server has not started, then sit there for the whole wait. A queue position
// is refused for a different reason -- nothing observes one.

import { documentRunRows } from '../lib/documentRun'
import { runFileRows } from '../lib/importRun'
import type { PlatformCtx } from '../types'

type InFlightKind = 'sending' | 'processing' | 'reading' | 'retrying'

// The four kinds that share the shimmer. Only `color` varies, and only for `retrying` (D-9):
// a retry is a warning, not the settled red failure below. The shimmer itself stays teal — it
// is the card's single in-flight glyph, not a severity signal.
const IN_FLIGHT_LABEL: Record<InFlightKind, { text: string; color: string }> = {
  sending: { text: 'SENDING FILE', color: 'var(--action)' },
  processing: { text: 'SERVER PROCESSING', color: 'var(--action)' },
  reading: { text: 'READING', color: 'var(--action)' },
  retrying: { text: 'RETRYING', color: 'var(--status-amber-text)' },
}

export function ImportProgress({ ctx }: { ctx: PlatformCtx }) {
  // `null` runKind falls to the spreadsheet builder deliberately: runFileRows can never return
  // 'reading'/'retrying' (EXTR-10-05 pins it), so a null run cannot leak document words.
  const rows = ctx.runKind === 'document' ? documentRunRows(ctx.run, ctx.documentStages) : runFileRows(ctx.run)
  if (rows.length === 0) return null

  return (
    <div data-testid="import-progress" style={{ maxWidth: 520, margin: '0 auto', background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
      <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--line-1)' }}>
        <span className="card-title">{rows.length === 1 ? 'Importing 1 file' : `Importing ${rows.length} files`}</span>
      </div>
      <div>
        {rows.map((row, i) => {
          // Total by exclusion, no cast: TS narrows the false branch to the four in-flight
          // kinds, so an eighth RunFileRow kind added later fails to compile here rather than
          // rendering blank.
          const inFlight = row.kind === 'queued' || row.kind === 'imported' || row.kind === 'failed' ? null : IN_FLIGHT_LABEL[row.kind]
          return (
            // Keyed by INDEX, not name: two picked files may share an identical filename
            // (BULK-01-03 QA coverage) and each is still its own row here, same reasoning
            // as CreateMapping's own column grid a few files up. Still index-keyed on the
            // document path: documentRunRows is 1:1 over run.files, which never reorders.
            <div
              key={i}
              data-testid="import-progress-row"
              style={{ padding: '12px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}
            >
              <span style={{ fontSize: 13, fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0 }}>{row.name}</span>
              {row.kind === 'queued' && (
                <span className="mono" style={{ flex: 'none', fontSize: 10.5, color: 'var(--fg-3)', letterSpacing: '0.06em' }}>
                  QUEUED
                </span>
              )}
              {inFlight !== null && (
                <span style={{ flex: 'none', display: 'flex', alignItems: 'center', gap: 8 }}>
                  {/* Indeterminate by construction: a repeating gradient twice the width of
                      its box, slid end to end forever. It encodes no position, so there is
                      no number it could be wrong about. --action / --action-tint (teal),
                      NOT --accent: --accent-tint does not exist in the rebuilt design
                      system, and an undefined custom property resolves to nothing with no
                      build error. The `shimmer` keyframe is global (styles/platform.css);
                      this row reuses it, same idiom as the single-file card it replaces. */}
                  <span
                    style={{
                      display: 'inline-block',
                      width: 28,
                      height: 6,
                      borderRadius: 99,
                      overflow: 'hidden',
                      background: 'repeating-linear-gradient(115deg, var(--action) 0 6px, var(--action-tint) 6px 13px)',
                      backgroundSize: '200% 100%',
                      animation: 'shimmer 1.15s linear infinite',
                    }}
                  />
                  <span className="mono" style={{ fontSize: 10.5, color: inFlight.color, letterSpacing: '0.06em' }}>
                    {inFlight.text}
                  </span>
                </span>
              )}
              {row.kind === 'imported' && (
                <span className="mono" style={{ flex: 'none', fontSize: 11, color: 'var(--status-green-text)', letterSpacing: '0.04em' }}>
                  {row.count} IMPORTED
                </span>
              )}
              {row.kind === 'failed' && (
                <span style={{ flex: 'none', maxWidth: 240, fontSize: 12, color: 'var(--status-red-text)', textAlign: 'right', lineHeight: 1.4 }}>{row.reason}</span>
              )}
            </div>
          )
        })}
      </div>
      <div style={{ padding: '14px 20px', display: 'flex', flexDirection: 'column', gap: 10 }}>
        <p style={{ fontSize: 12.5, color: 'var(--fg-2)', margin: 0, lineHeight: 1.55 }}>
          Remaining time is unknown — the server reads, groups, stores and validates each file in one request, and how long that takes depends on the file.
        </p>
        {/* Not a politeness. handlers.go runs the import on r.Context(), so a client
            disconnect CANCELS the request mid-flight — after CreateBatch and some rows
            have already committed. Leaving mid-run can leave every file CURRENTLY in
            flight half-written — one on the spreadsheet path, all of them on the
            document path — even though every file that already settled stays committed
            regardless ([partial-success-kept]). */}
        <p style={{ fontSize: 12.5, color: 'var(--status-amber-text)', margin: 0, lineHeight: 1.55 }}>
          Do not close this tab — each file imports on its own request, and closing it can leave the file in progress half-written.
        </p>
      </div>
    </div>
  )
}
