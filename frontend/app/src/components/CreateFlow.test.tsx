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
function createFlowCtx(createStep: CreateStep, runKind: 'spreadsheet' | 'document' | null = null): PlatformCtx {
  const ctx = {
    createStep,
    runKind,
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
    // ReviewBatch — the document path's step 2. Enumerated by grepping `ctx\.` in
    // ReviewBatch.tsx; an empty id list keeps its own fetch effect from firing.
    reviewBatchIds: [],
    authedFetch: async () => {
      throw new Error('no fetch expected — reviewBatchIds is empty')
    },
    openImportedInvoice: () => {},
    restartImport: () => {},
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

// RED specs (EXTR-09-07, task-774, Mode A / test-first) — the header must fork with the
// run. wizardHeader(step, runKind) shipped in EXTR-09-06 and is already right; CreateFlow
// still calls it with the step alone, so a document run renders the 3-step spreadsheet
// strip. Wiring ctx through is this subtask's job.
describe('CreateFlow — the header follows the run kind (EXTR-09-07, AC-2)', () => {
  afterEach(() => cleanup())

  function stepLabels(container: HTMLElement): string[] {
    // The step-number span is width/height 22 (connectorCount above pins the 36x1
    // connector against exactly this pair); its label is the sibling right after it.
    return Array.from(container.querySelectorAll('span'))
      .filter((s) => s.style.width === '22px' && s.style.height === '22px')
      .map((s) => (s.nextElementSibling as HTMLElement | null)?.textContent ?? '')
  }

  it('FORK-HDR-1: a document run on the upload step renders the 2-step Import · Review strip', () => {
    const { container } = render(<CreateFlow ctx={createFlowCtx('upload', 'document')} />)
    const labels = stepLabels(container)

    expect(labels.length).toBeGreaterThan(0)
    expect(labels).toEqual(['Import', 'Review'])
    expect(connectorCount(container)).toBe(1)
  })

  it('FORK-HDR-2 (AC-1 control): a spreadsheet run still renders the shipped 3-step strip', () => {
    const { container } = render(<CreateFlow ctx={createFlowCtx('upload', 'spreadsheet')} />)

    expect(stepLabels(container)).toEqual(['Import', 'Map', 'Review'])
    expect(connectorCount(container)).toBe(2)
  })

  it('FORK-HDR-3: the shared Review step lands at index 1 of 2 on the document path, not 2 of 3', () => {
    const { container } = render(<CreateFlow ctx={createFlowCtx('review', 'document')} />)

    expect(stepLabels(container)).toEqual(['Import', 'Review'])
  })
})
