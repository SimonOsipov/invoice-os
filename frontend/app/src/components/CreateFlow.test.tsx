// @vitest-environment jsdom
// RED specs (task-396, BUG-03-07 item 10, Mode A) — CreateFlow.tsx:72 renders a connector
// span after every step unconditionally, so the last step's connector dangles past it with
// nothing after it. Pins the post-fix contract (wrap in idx < steps.length - 1) before the
// executor makes the change. First test file for this component — no ctx-builder existed;
// createFlowCtx follows the repo's per-file local-helper convention (reportsCtx() in
// ReportsView.test.tsx, detailCtx() in InvoiceDetail.test.tsx).
import { cleanup, render } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import type { CreateStep, PlatformCtx } from '../types'
import { CreateFlow } from './CreateFlow'

// Handler surface enumerated by grepping `ctx\.` in CreateUpload.tsx and CreateForm.tsx —
// the two step components a 3-step ('upload') and a 1-step ('form') createStep actually
// render here. Neither mounts a useEffect or calls ctx.authedFetch, so no fetch mock is
// needed. `run.status: 'idle'` keeps runIsActive(run) false so the step router renders,
// not ImportProgress.
function createFlowCtx(createStep: CreateStep): PlatformCtx {
  const ctx = {
    createStep,
    run: { files: [], cursor: 0, status: 'idle' },
    closeCreate: () => {},
    mode: 'firm',
    active: { short: 'Lagos Freight', tin: '20184412-0001' },
    // CreateUpload
    pickedFiles: [],
    filesRefusal: null,
    importError: null,
    activeEntity: null,
    entitiesState: 'ready',
    entities: [],
    clients: [],
    addPickedFiles: () => {},
    removePickedFile: () => {},
    setSettingsTab: () => {},
    nav: () => {},
    readAllColumns: () => {},
    skipUpload: () => {},
    // CreateForm
    draft: { number: '', buyer: '', buyerTin: '', date: '', currency: 'NGN', items: [] },
    filing: false,
    filingError: null,
    updateDraft: () => {},
    updateItemDesc: () => {},
    updateItem: () => {},
    removeItem: () => {},
    addItem: () => {},
    fileDraft: () => {},
  }
  return ctx as unknown as PlatformCtx
}

// The connector span is unmarked (no class, no testid) — width: 36 / height: 1 is the
// literal pair CreateFlow.tsx:72 itself is pinned by (the plan's own grep target), and
// jsdom parses the inline style attribute into these, so this selector cannot collide with
// the step-number span (width: 22, height: 22) sitting next to it.
function connectorCount(container: HTMLElement): number {
  return Array.from(container.querySelectorAll('span')).filter(
    (s) => s.style.width === '36px' && s.style.height === '1px',
  ).length
}

describe('CreateFlow — step-strip connector spans (BUG-03-07 item 10)', () => {
  afterEach(() => cleanup())

  it('3-step import path (createStep upload): exactly 2 connectors', () => {
    const { container } = render(<CreateFlow ctx={createFlowCtx('upload')} />)
    expect(connectorCount(container)).toBe(2)
  })

  it('1-step typed path (createStep form): exactly 0 connectors', () => {
    const { container } = render(<CreateFlow ctx={createFlowCtx('form')} />)
    expect(connectorCount(container)).toBe(0)
  })
})
