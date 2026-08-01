// Create flow · the in-flight import card. Replaces the determinate bar + "Working…"
// spinner that used to sit in CreateMapping's footer: the wrong shape for what this
// request actually is, and squeezed into a row where it read as a detail of the button
// beside it rather than the state of the whole screen.
//
// BULK-01-05 (task-308) turned this from a single-file card into a PER-FILE LIST: one
// row per file in the run, in run order (lib/importRun.ts's runFileRows), live while a
// createImport call is actually in flight or just settled (CreateFlow's body-swap gate,
// runIsActive(run) — true for 'running' and the brief 'finished' tick before a route is
// chosen). The rewrite is additive to the card's own honesty rules, not a relaxation of
// them.
//
// It does NOT stay on screen once EVERY file has failed (AC #9, corrected by QA under
// this same task-308 after the first cut of this rewrite shipped it as a dead end):
// applyRoute (App.tsx) lands a `none` route on `run.status: 'failed'`
// (lib/importRun.ts's markRunFailed) rather than 'finished', and runIsActive treats
// 'failed' like 'idle' — so CreateFlow swaps back to the step router and CreateMapping
// renders again, showing the same per-file failures (runFailures) in its own footer
// instead of here.
//
// WHAT THIS CARD DELIBERATELY DOES NOT SHOW, ON ANY ROW, and why each one is a lie
// rather than a missing feature:
//
// 1. NO STAGE LIST — not ticked, not even static. Two independent reasons.
//    (a) Nothing can drive one. `POST /v1/imports` is synchronous with no job to poll:
//        the invoice service routes only POST /v1/imports and POST /v1/imports/preview
//        (no GET, no status endpoint), there is no http.Flusher / text/event-stream
//        anywhere in internal/importer, and no EventSource / ReadableStream anywhere in
//        this app. importApi.ts's own progress contract already states the consequence —
//        after `upload.onload` everything (server parse, decode, DB writes, rule
//        evaluation, response travel) is unobservable, "so any stage label there would
//        be invented".
//    (b) Even a static list would MISDESCRIBE the server. The real pipeline
//        (internal/importer/service.go) is: resolve mapping → group → classify/quarantine
//        → entity lookup → count → CreateBatch + Store.Create per READY group →
//        Gate.ValidateBatch + ApplyValidation → Finalize. It STORES BEFORE IT VALIDATES,
//        and the classify/quarantine step — the one that produces the entire
//        unreadable-rows channel the report is half made of — has no obvious name at all.
//        A plausible-sounding four-item list would teach the user a wrong model of the
//        one thing on this screen they might later have to reason about.
//
// 2. NO ROW COUNTER, on any row. UploadPhase carries BYTES, never rows (importApi.ts's
//    UploadPhase): `sending` has loaded/total bytes, `processing` has nothing. A queued/
//    sending/processing row therefore carries NOTHING beyond the filename (lib/
//    importRun.ts's RunFileRow) — an "N OF M ROWS" pair with an invented N is the single
//    most misleading thing any row here could render. An `imported` row's count is a
//    DIFFERENT fact at a DIFFERENT moment: the server's own ready-invoice count, read
//    back AFTER that one file has settled, never a guess made while it is still in
//    flight.
//
// 3. NO BYTE COUNTER either. It is genuinely available — but only sometimes
//    (lengthComputable can be false, and zero progress events is legal, IMPAPI-08), and a
//    mono `X OF Y` in this slot reads as rows-read whatever noun sits next to it. An
//    honest number in a slot that is read as a different number is not honest.
//
// 4. NO PERCENTAGE, per row or across the whole run. The only computable fraction is
//    bytes UPLOADED, which on a fast link hits 100% while the server has not started, and
//    then sits at 100% for the entire wait. A bar that fills and stops is worse than one
//    that never claims.
//
// 5. NO RULE-SET VERSION. It is decided by the server DURING the request each row
//    represents — structurally after this screen — and there is no rules endpoint to ask
//    beforehand. The review screen renders the real one per batch, read back off GET
//    /v1/imports/{id} (INVCR-01-09) rather than off any row here.
//
// What IS rendered, per row: the filename, one word for the transition the transport
// genuinely observes (queued / sending / server processing), and — only once that file
// has actually settled — either the server's own ready-invoice count or its failure
// reason verbatim. Zero `sending` events for a given file is legal (IMPAPI-08), so no row
// may assume a minimum number of phase events before it settles.

import { runFileRows } from '../lib/importRun'
import type { PlatformCtx } from '../types'

export function ImportProgress({ ctx }: { ctx: PlatformCtx }) {
  const rows = runFileRows(ctx.run)
  if (rows.length === 0) return null

  return (
    <div style={{ maxWidth: 520, margin: '0 auto', background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
      <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--line-1)' }}>
        <span className="card-title">{rows.length === 1 ? 'Importing 1 file' : `Importing ${rows.length} files`}</span>
      </div>
      <div>
        {rows.map((row, i) => (
          // Keyed by INDEX, not name: two picked files may share an identical filename
          // (BULK-01-03 QA coverage) and each is still its own row here, same reasoning
          // as CreateMapping's own column grid a few files up.
          <div
            key={i}
            style={{ padding: '12px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}
          >
            <span style={{ fontSize: 13, fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0 }}>{row.name}</span>
            {row.kind === 'queued' && (
              <span className="mono" style={{ flex: 'none', fontSize: 10.5, color: 'var(--fg-3)', letterSpacing: '0.06em' }}>
                QUEUED
              </span>
            )}
            {(row.kind === 'sending' || row.kind === 'processing') && (
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
                <span className="mono" style={{ fontSize: 10.5, color: 'var(--action)', letterSpacing: '0.06em' }}>
                  {row.kind === 'sending' ? 'SENDING FILE' : 'SERVER PROCESSING'}
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
        ))}
      </div>
      <div style={{ padding: '14px 20px', display: 'flex', flexDirection: 'column', gap: 10 }}>
        <p style={{ fontSize: 12.5, color: 'var(--fg-2)', margin: 0, lineHeight: 1.55 }}>
          Remaining time is unknown — the server reads, groups, stores and validates each file in one request, and how long that takes depends on the file.
        </p>
        {/* Not a politeness. handlers.go runs the import on r.Context(), so a client
            disconnect CANCELS the request mid-flight — after CreateBatch and some rows
            have already committed. Still true one file at a time in a run: leaving mid-
            run can leave the file CURRENTLY sending half-written, even though every file
            that already settled stays committed regardless ([partial-success-kept]). */}
        <p style={{ fontSize: 12.5, color: 'var(--status-amber-text)', margin: 0, lineHeight: 1.55 }}>
          Do not close this tab — each file imports on its own request, and closing it can leave the file in progress half-written.
        </p>
      </div>
    </div>
  )
}
