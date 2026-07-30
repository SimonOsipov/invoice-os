// Create / validate flow orchestrator — the wizard header + a step router keyed on
// `ctx.createStep`, serving two paths: manual entry (form → validating → results) and a
// server-backed spreadsheet import (upload → mapping → report). Ported from
// Platform.dc.html ~L389-596 + renderVals() (~L1521-1524).

import { VAL_LABELS } from '../data'
import { wizardHeader } from '../lib/importFlow'
import { CreateUpload } from './CreateUpload'
import { CreateMapping } from './CreateMapping'
import { CreateForm } from './CreateForm'
import { CreateResults } from './CreateResults'
import { CreateReport } from './CreateReport'
import { ScanlineSteps } from './ScanlineSteps'
import type { PlatformCtx } from '../types'

// The wizard serves TWO paths with different step lists — the 5-step manual-entry wizard
// and the 3-step Import/Map/Report import — so the header is resolved by wizardHeader
// (lib/importFlow.ts) rather than a flat Record<CreateStep, number>, which has no concept
// of which path the user is on. STAGE_OF moved there with it: one table, one owner, no
// second copy to drift. wizardHeader takes the step ALONE: the file arguments existed
// only to disambiguate 'upload' between the two paths, and with the document mock deleted
// (INVCR-01-01) 'upload' belongs unambiguously to the import path.
export function CreateFlow({ ctx }: { ctx: PlatformCtx }) {
  const { createStep, draft, valIdx } = ctx
  const { steps, stageIndex } = wizardHeader(createStep)
  const valCount = Math.min(valIdx, VAL_LABELS.length)

  return (
    <div style={{ padding: '24px 36px 56px' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 14, marginBottom: 22 }}>
        <button onClick={ctx.closeCreate} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 34, padding: '0 12px', fontSize: 13 }}>
          ← Cancel
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
                <span style={{ width: 36, height: 1, background: 'var(--line-2)', margin: '0 14px' }} />
              </div>
            )
          })}
        </div>
      </div>

      {createStep === 'upload' && <CreateUpload ctx={ctx} />}

      {createStep === 'mapping' && <CreateMapping ctx={ctx} />}

      {createStep === 'form' && <CreateForm ctx={ctx} />}

      {createStep === 'validating' && (
        <ScanlineSteps
          title="Validating against MBS rules…"
          subtitle={`${draft.number} · ${valCount} / 16 CHECKS`}
          labels={VAL_LABELS}
          idx={valIdx}
          unitLabel="COMPLETE"
          transformMs={170}
          widthMs={150}
        />
      )}

      {createStep === 'results' && <CreateResults ctx={ctx} />}

      {createStep === 'report' && <CreateReport ctx={ctx} />}
    </div>
  )
}
