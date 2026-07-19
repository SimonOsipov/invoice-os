// Create / validate flow orchestrator — the wizard header + a step router keyed on
// `ctx.createStep` (upload → parsing → mapping → form|review → validating → results).
// Ported from Platform.dc.html ~L389-596 + the wizard slice of renderVals()
// (~L1521-1524). `form` and `review` are two variants of the same Build stage:
// a tabular file resolving to many invoices reviews them; anything else edits one.

import { VAL_LABELS, PARSE_LABELS, SAMPLE_FILES } from '../data'
import { wizardHeader } from '../lib/importFlow'
import { CreateUpload } from './CreateUpload'
import { CreateMapping } from './CreateMapping'
import { CreateForm } from './CreateForm'
import { CreateReview } from './CreateReview'
import { CreateResults } from './CreateResults'
import { ScanlineSteps } from './ScanlineSteps'
import type { PlatformCtx } from '../types'

// The wizard now serves TWO paths with different step lists — the 5-step single-document
// wizard and the 3-step Import/Map/Report import — so the header is resolved by
// wizardHeader (lib/importFlow.ts) rather than a flat Record<CreateStep, number>, which
// has no concept of which path the user is on. STAGE_OF moved there with it: one table,
// one owner, no second copy to drift.
export function CreateFlow({ ctx }: { ctx: PlatformCtx }) {
  const { createStep, draft, uploadFile, importFile, valIdx, parseIdx } = ctx
  const { steps, stageIndex } = wizardHeader(createStep, uploadFile, importFile)
  const selFileName = uploadFile ? SAMPLE_FILES.find((f) => f.id === uploadFile)?.name || '' : ''
  const valCount = Math.min(valIdx, VAL_LABELS.length)

  return (
    <div style={{ padding: '24px 36px 56px', maxWidth: 1080, margin: '0 auto' }}>
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
                  <span style={{ width: 22, height: 22, borderRadius: 99, display: 'grid', placeItems: 'center', fontFamily: 'var(--font-mono)', fontSize: 11, fontWeight: 600, background: a ? 'var(--accent)' : done ? 'var(--accent-tint)' : 'var(--bg-2)', color: a ? '#fff' : done ? 'var(--accent)' : 'var(--fg-3)', border: `1px solid ${a || done ? 'var(--accent)' : 'var(--line-2)'}` }}>{n}</span>
                  <span style={{ fontSize: 13, fontWeight: 500, color: a ? 'var(--fg-1)' : 'var(--fg-3)' }}>{label}</span>
                </div>
                <span style={{ width: 36, height: 1, background: 'var(--line-2)', margin: '0 14px' }} />
              </div>
            )
          })}
        </div>
      </div>

      {createStep === 'upload' && <CreateUpload ctx={ctx} />}

      {createStep === 'parsing' && (
        <ScanlineSteps
          title={`Parsing ${selFileName}…`}
          subtitle="READING FILE · DETECTING COLUMNS"
          labels={PARSE_LABELS}
          idx={parseIdx}
          unitLabel="PARSED"
          transformMs={180}
          widthMs={170}
        />
      )}

      {createStep === 'mapping' && <CreateMapping ctx={ctx} />}

      {createStep === 'form' && <CreateForm ctx={ctx} />}

      {createStep === 'review' && <CreateReview ctx={ctx} />}

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
    </div>
  )
}
