// Create flow · the in-flight import card. Replaces the determinate bar + "Working…"
// spinner that used to sit in CreateMapping's footer: the wrong shape for what this
// request actually is, and squeezed into a row where it read as a detail of the button
// beside it rather than the state of the whole screen.
//
// WHAT THIS CARD DELIBERATELY DOES NOT SHOW, and why each one is a lie rather than a
// missing feature:
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
// 2. NO ROW COUNTER. UploadPhase carries BYTES, never rows (importApi.ts's UploadPhase):
//    `sending` has loaded/total bytes, `processing` has nothing. The denominator is known
//    (preview.rows_total, the server's own count) but the NUMERATOR has no source
//    anywhere on either side of the wire. An "N OF M ROWS" pair with an invented N is the
//    single most misleading thing this card could render.
//
// 3. NO BYTE COUNTER either. It is genuinely available — but only sometimes
//    (lengthComputable can be false, and zero progress events is legal, IMPAPI-08), and a
//    mono `X OF Y` in this slot reads as rows-read whatever noun sits next to it. An
//    honest number in a slot that is read as a different number is not honest.
//
// 4. NO PERCENTAGE. Same reason: the only computable fraction is bytes UPLOADED, which
//    on a fast link hits 100% while the server has not started, and then sits at 100%
//    for the entire wait. A bar that fills and stops is worse than one that never claims.
//
// 5. NO RULE-SET VERSION. It arrives in the 201 body (ImportReport.rule_set_version) —
//    structurally AFTER this screen — and there is no rules endpoint to ask beforehand.
//    The only build-time value in the app is a mock literal that already disagrees with
//    the real active version. The review screen renders the real one from the report.
//
// What IS rendered: the server's own preview facts stated as WHAT IS BEING IMPORTED and
// never as progress, one phase word for the single transition the transport genuinely
// observes (last byte handed to the socket), and an indeterminate barber-pole that
// claims nothing about how far along anything is.

import type { PlatformCtx } from '../types'

export function ImportProgress({ ctx }: { ctx: PlatformCtx }) {
  const { preview, importFile, uploadPhase } = ctx
  // Reachable only from the import path, which sets both — same guard shape as
  // CreateMapping, which this card body-swaps for while the request is in flight.
  if (!preview || !importFile) return null

  return (
    <div style={{ maxWidth: 520, margin: '0 auto', background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
      <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
        <span className="card-title" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          Importing {importFile.name}
        </span>
        <span className="mono" style={{ flex: 'none', fontSize: 10.5, color: 'var(--action)', letterSpacing: '0.06em' }}>
          {uploadPhase.kind === 'sending' ? 'SENDING FILE' : 'SERVER PROCESSING'}
        </span>
      </div>
      <div style={{ padding: 20, display: 'flex', flexDirection: 'column', gap: 14 }}>
        {/* Indeterminate by construction: a repeating gradient twice the width of its
            box, slid end to end forever. It encodes no position, so there is no number
            it could be wrong about.

            --action / --action-tint (teal), NOT --accent: --accent is the amber
            confirmation/focus colour, and --accent-tint DOES NOT EXIST — it was dropped
            in the design-system rebuild, and an undefined custom property resolves to
            nothing with no build error, no runtime error and no failing test. Using it
            here would have shipped an invisible bar that every check passed on.

            The `shimmer` keyframe is global (styles/platform.css) and was already
            defined; this is its first consumer. Global keyframe + inline `animation` is
            this app's existing idiom (the spinner it replaces did the same) — no new
            @keyframes, no scoped <style>. */}
        <span
          style={{
            display: 'block',
            height: 6,
            borderRadius: 99,
            overflow: 'hidden',
            background: 'repeating-linear-gradient(115deg, var(--action) 0 12px, var(--action-tint) 12px 26px)',
            backgroundSize: '200% 100%',
            animation: 'shimmer 1.15s linear infinite',
          }}
        />
        {/* The server's own count, from the preview it already returned — stated as the
            size of the job, never as how much of it is done. */}
        <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
          {preview.rows_total} ROWS · {preview.columns.length} COLS
        </span>
        <p style={{ fontSize: 12.5, color: 'var(--fg-2)', margin: 0, lineHeight: 1.55 }}>
          Remaining time is unknown — the server reads, groups, stores and validates the whole file in one request, and how long that takes depends on the file.
        </p>
        {/* Not a politeness. handlers.go runs the import on r.Context(), so a client
            disconnect CANCELS it mid-flight — after CreateBatch and some rows have
            already committed. The story's original copy said the opposite ("leaving this
            page does not cancel the import"), which would have invited exactly the action
            that corrupts the batch. */}
        <p style={{ fontSize: 12.5, color: 'var(--status-amber-text)', margin: 0, lineHeight: 1.55 }}>
          Do not close this tab — the import runs on this request, and closing it can leave the batch half-written.
        </p>
      </div>
    </div>
  )
}
