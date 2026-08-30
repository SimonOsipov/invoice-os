// Create flow orchestrator — the wizard header + a step router keyed on `ctx.createStep`,
// serving two paths: manual entry (a single 'form' step that files straight to the server)
// and a server-backed spreadsheet import (upload → mapping → review). Ported from
// Platform.dc.html ~L389-596 + renderVals() (~L1521-1524).
//
// The manual path has no step after 'form': INVCR-01-03 replaced the mock
// validate-then-approve tail with one real POST, and a successful filing navigates away to
// the real invoice detail view rather than rendering any success step here. Nothing in this
// router may affirm a filing — there is no branch left that could.

import { wizardHeader } from '../lib/importFlow'
import { runIsActive } from '../lib/importRun'
import { CreateUpload } from './CreateUpload'
import { CreateMapping } from './CreateMapping'
import { CreateForm } from './CreateForm'
import { ReviewBatch } from './ReviewBatch'
import { ImportProgress } from './ImportProgress'
import type { PlatformCtx } from '../types'

// The wizard serves THREE paths with different step lists — the 1-step Enter typed path,
// the 3-step Import/Map/Review spreadsheet import and the 2-step Import/Review document
// run — so the header is resolved by wizardHeader (lib/importFlow.ts) rather than a flat
// Record<CreateStep, number>, which has no concept of which path the user is on. STAGE_OF
// moved there with it: one table, one owner, no second copy to drift.
//
// wizardHeader takes the run kind as a SECOND argument, because 'review' is shared by the
// import and document paths and lands at a different index on each. This call passes only
// the step, which resolves the two shipped paths correctly; EXTR-09-07 wires the run kind
// through ctx when the document path gains an entry point.
export function CreateFlow({ ctx }: { ctx: PlatformCtx }) {
  const { createStep, run } = ctx
  const { steps, stageIndex } = wizardHeader(createStep)

  // The in-flight run owns the whole body, not a corner of the mapping footer. The
  // footer's two buttons already disabled themselves while uploading, but the GRID never
  // did — and it could not usefully: startRun serializes each group's mapping into its
  // own request at the moment the run starts, so dragging a field onto a different
  // column mid-flight rearranges a screen whose contents have already been sent, with no
  // effect whatsoever on what the server is doing. Leaving that live behind a small
  // spinner is an offer the request cannot honour. The header strip stays — the user is
  // still on the Map step, and this IS what that step is doing.
  //
  // Gated on runIsActive(run) (BULK-01-05, lib/importRun.ts) — true for 'running' and
  // the brief 'finished' tick before applyRoute picks a route, false for 'idle' AND for
  // 'failed'. A `none` route (every file failed at the request level, AC #9) lands
  // `run.status` on 'failed' (App.tsx's applyRoute, via markRunFailed) rather than
  // resetting to idle, so runFailures keeps returning every failure — but this gate
  // treats 'failed' exactly like 'idle', so the body-swap falls back to the step router
  // below and CreateMapping renders again (its own footer shows the failure list),
  // instead of leaving this dead-end card (no buttons) on screen forever.
  const importing = runIsActive(run)

  return (
    <div style={{ padding: '24px 36px 56px' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 14, marginBottom: 22 }}>
        {/* "Cancel" implies undo, and on the review step there is nothing to undo: the
            invoices were persisted at import time (§10.10), the batch exists, and this
            button does the identical setView('invoices') that "Finish · go to invoices"
            does 400px below it. Naming it after where it goes is the only honest label
            on a surface where everything is already saved. The other three steps keep
            "Cancel", where abandoning genuinely discards work in progress. */}
        <button onClick={ctx.closeCreate} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 34, padding: '0 12px', fontSize: 13 }}>
          {createStep === 'review' ? '← Invoices' : '← Cancel'}
        </button>
        <div style={{ flex: 1, display: 'flex', alignItems: 'center', gap: 0 }}>
          {steps.map(([n, label], idx) => {
            const done = idx < stageIndex
            const a = idx === stageIndex
            return (
              <div key={n} style={{ display: 'flex', alignItems: 'center' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <span style={{ width: 22, height: 22, borderRadius: 99, display: 'grid', placeItems: 'center', fontFamily: 'var(--font-mono)', fontSize: 11, fontWeight: 600, background: a ? 'var(--action)' : done ? 'var(--action-tint)' : 'var(--bg-2)', color: a ? 'var(--text-on-dark)' : done ? 'var(--action)' : 'var(--fg-3)', border: `1px solid ${a || done ? 'var(--action)' : 'var(--line-2)'}` }}>{n}</span>
                  <span style={{ fontSize: 13, fontWeight: 500, color: a ? 'var(--fg-1)' : 'var(--fg-3)' }}>{label}</span>
                </div>
                {/* [connector-omitted-not-transparent]: no trailing separator; prototype's sepBg is dynamic and its source is unavailable */}
                {idx < steps.length - 1 && (
                  <span style={{ width: 36, height: 1, background: 'var(--line-2)', margin: '0 14px' }} />
                )}
              </div>
            )
          })}
        </div>
      </div>

      {importing ? (
        <ImportProgress ctx={ctx} />
      ) : (
        <>
          {createStep === 'upload' && <CreateUpload ctx={ctx} />}

          {createStep === 'mapping' && <CreateMapping ctx={ctx} />}

          {createStep === 'form' && <CreateForm ctx={ctx} />}

          {createStep === 'review' && <ReviewBatch ctx={ctx} />}
        </>
      )}
    </div>
  )
}
