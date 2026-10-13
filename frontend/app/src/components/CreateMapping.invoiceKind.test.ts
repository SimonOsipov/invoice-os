// @vitest-environment jsdom
// The Map step's buyer-type select. Renders CreateMapping directly with a hand-built ctx
// (CreateMapping.badge.test.ts precedent).

import { createElement } from 'react'

import { cleanup, fireEvent, render } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { ImportPreview } from '../lib/importApi'
import { initMappingFromHeaders } from '../lib/mapping'
import type { MappingGroup } from '../lib/mappingGroups'
import type { PlatformCtx } from '../types'
import { CreateMapping } from './CreateMapping'

afterEach(() => cleanup())

const COLS = ['Invoice No', 'Kind']

function mkGroup(placed: Record<string, string | null>): MappingGroup {
  const preview: ImportPreview = {
    document_id: 'aaaaaaaa-0000-4000-8000-0000000000b2',
    format: 'csv',
    delimiter: ',',
    encoding: 'utf-8',
    columns: COLS,
    sample_rows: [['INV-1', 'B2B']],
    rows_total: 1,
  }
  return {
    id: 'g-kind',
    signature: JSON.stringify(COLS),
    fileIds: ['f1'],
    preview,
    headerRow: 1,
    mapping: { ...initMappingFromHeaders(COLS), ...placed },
    restored: null,
    suggested: null,
  }
}

function ctxFor(group: MappingGroup, over: Record<string, unknown> = {}): PlatformCtx {
  return {
    active: { short: 'Lagos Freight' },
    preview: group.preview,
    mapping: group.mapping,
    armedField: null,
    dragField: null,
    run: { files: [], cursor: 0, status: 'idle' },
    importError: null,
    entityId: 'aaaaaaaa-0000-4000-8000-000000000001',
    groups: [group],
    groupIndex: 0,
    pickedFiles: [{ id: 'f1', file: new File(['x'], 'a.csv'), documentId: null }],
    setDrag: () => {},
    endDrag: () => {},
    armField: () => {},
    resetGroupToAutomatic: () => {},
    splitOutFile: () => {},
    setInvoiceKind: () => {},
    dropOn: () => {},
    clickCol: () => {},
    unmap: () => {},
    backToImport: () => {},
    continueMapping: () => {},
    ...over,
  } as unknown as PlatformCtx
}

const select = (c: HTMLElement) => c.querySelector<HTMLSelectElement>('[data-testid="map-invoice-kind"]')

describe('CreateMapping buyer-type select', () => {
  it('ENGI07-MAP-01: the buyer-type select renders while invoice_kind is unplaced', () => {
    const { container } = render(createElement(CreateMapping, { ctx: ctxFor(mkGroup({ invoice_kind: null })) }))
    const el = select(container)
    expect(el, 'the select must render').not.toBeNull()
    expect(Array.from(el!.options).map((o) => [o.value, o.textContent])).toEqual([
      ['', 'Not set'],
      ['B2B', 'B2B · Business'],
      ['B2G', 'B2G · Government'],
      ['B2C', 'B2C · Consumer'],
    ])
    expect(el!.value).toBe('')
  })

  it('ENGI07-MAP-01b: the select shows the group choice and reports a change by group id', () => {
    const setInvoiceKind = vi.fn()
    const group = { ...mkGroup({ invoice_kind: null }), invoiceKind: 'B2G' as const }
    const { container } = render(createElement(CreateMapping, { ctx: ctxFor(group, { setInvoiceKind }) }))
    const el = select(container)!
    expect(el.value).toBe('B2G')
    fireEvent.change(el, { target: { value: 'B2C' } })
    expect(setInvoiceKind).toHaveBeenLastCalledWith('g-kind', 'B2C')
    fireEvent.change(el, { target: { value: '' } })
    expect(setInvoiceKind).toHaveBeenLastCalledWith('g-kind', undefined)
  })

  it('ENGI07-MAP-02: placing invoice_kind removes the select', () => {
    const { container } = render(createElement(CreateMapping, { ctx: ctxFor(mkGroup({ invoice_kind: 'Kind' })) }))
    expect(container.querySelector('[data-testid="map-column"]'), 'control: the screen rendered').not.toBeNull()
    expect(select(container)).toBeNull()
  })

  it('ENGI07-MAP-03: the select is disabled during a run, as the Back button', () => {
    const run = { files: [], cursor: 0, status: 'running' }
    const { container } = render(createElement(CreateMapping, { ctx: ctxFor(mkGroup({ invoice_kind: null }), { run }) }))
    const back = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('Back to import'))
    expect(back?.disabled, 'control: Back is disabled during a run').toBe(true)
    expect(select(container)!.disabled).toBe(true)
  })

  it('ENGI07-MAP-03b: the select is enabled when no run is active', () => {
    const { container } = render(createElement(CreateMapping, { ctx: ctxFor(mkGroup({ invoice_kind: null })) }))
    expect(select(container)!.disabled).toBe(false)
  })
})
