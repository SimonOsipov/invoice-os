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
import { CreateUpload } from './CreateUpload'
import { CreateMapping } from './CreateMapping'
import { CreateForm } from './CreateForm'
import { CreateReport } from './CreateReport'
import type { PlatformCtx } from '../types'

// The wizard serves TWO paths with different step lists — the 2-step Enter/Review typed
// path and the 3-step Import/Map/Review import — so the header is resolved by wizardHeader
// (lib/importFlow.ts) rather than a flat Record<CreateStep, number>, which has no concept
// of which path the user is on. STAGE_OF moved there with it: one table, one owner, no
// second copy to drift. wizardHeader takes the step ALONE: the file arguments existed
// only to disambiguate 'upload' between the two paths, and with the document mock deleted
// (INVCR-01-01) 'upload' belongs unambiguously to the import path.
export function CreateFlow({ ctx }: { ctx: PlatformCtx }) {
  const { createStep } = ctx
  const { steps, stageIndex } = wizardHeader(createStep)

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

      {createStep === 'review' && <CreateReport ctx={ctx} />}
    </div>
  )
}
