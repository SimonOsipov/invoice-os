import { describe, expect, it, vi } from 'vitest'

import type { AuthedFetch } from './portfolio'
import {
  activitySummary,
  changedAfterClearance,
  documentOutcome,
  getErpLookups,
  getErpMapping,
  getErpOverview,
  getInvoiceErpMarks,
  heldReasonText,
  lookupChanges,
  mappingChanges,
  saveErpLookups,
  saveErpMapping,
  writebackResultText,
  writebackText,
  type DocumentMark,
  type FieldView,
  type Lookup,
} from './erpConnectionDetail'

const stub = (answer: unknown = {}) => vi.fn(async () => answer) as unknown as AuthedFetch & ReturnType<typeof vi.fn>
const field = (target: string, over: Partial<FieldView> = {}): FieldView => ({
  target,
  label: target,
  line: false,
  source_path: null,
  default_path: null,
  origin: 'none',
  ...over,
})
const lk = (kind: string, erp_key: string, over: Partial<Lookup> = {}): Lookup => ({
  kind,
  erp_key,
  tax_category: null,
  exemption_code: null,
  hsn_code: null,
  service_code: null,
  tin: null,
  state: null,
  lga: null,
  ...over,
})
const mark = (over: Partial<DocumentMark> = {}): DocumentMark => ({
  connection_id: 'c',
  system: 'fake',
  source_doc_id: 'd',
  state: 'linked',
  differs_from_erp: false,
  changed_after_clearance_at: null,
  deleted_in_erp_at: null,
  ...over,
})

describe('erp connection detail calls', () => {
  it('erpDetail_callsHitTheirRoutes', async () => {
    const f = stub()
    const root = 'http://h/api/portfolio/v1/entities/a%2Fb'
    await getErpOverview(f, 'http://h', 'a/b', 'a/b')
    await getErpMapping(f, 'http://h', 'a/b', 'fake')
    await saveErpMapping(f, 'http://h', 'a/b', 'fake', [{ target: 't', source_path: null }])
    await getErpLookups(f, 'http://h', 'a/b', 'fake', 'co 1')
    await saveErpLookups(f, 'http://h', 'a/b', 'fake', 'co 1', [lk('item', 'k')])
    await getInvoiceErpMarks(f, 'http://h', 'a/b', 'i/1')
    expect(f.mock.calls).toEqual([
      [`${root}/erp-connections/a%2Fb/overview`],
      [`${root}/erp-mappings/fake`],
      [`${root}/erp-mappings/fake`, { method: 'PUT', body: { fields: [{ target: 't', source_path: null }] } }],
      [`${root}/erp-lookups/fake?external_key=co+1`],
      [`${root}/erp-lookups/fake?external_key=co+1`, { method: 'PUT', body: { lookups: [lk('item', 'k')] } }],
      [`${root}/invoices/i%2F1/erp-marks`],
    ])
  })
})

describe('mappingChanges', () => {
  const fields = [
    field('a', { origin: 'client', source_path: 'x.a', default_path: 'd.a' }),
    field('b', { origin: 'default', source_path: 'd.b', default_path: 'd.b' }),
    field('c'),
  ]
  it('mappingChanges_namesOnlyChangedTargets', () => {
    expect(mappingChanges(fields, { a: 'x.a', b: 'd.b', c: 'new.c' })).toEqual([{ target: 'c', source_path: 'new.c' }])
  })
  it('mappingChanges_blankRevertsADefaultedRow', () => {
    expect(mappingChanges(fields, { b: '' })).toEqual([{ target: 'b', source_path: null }])
  })
  it('mappingChanges_blankOnAnUnmappedRowIsNoChange', () => {
    expect(mappingChanges(fields, { c: '  ' })).toEqual([])
  })
  it('mappingChanges_useDefaultIsNull', () => {
    expect(mappingChanges(fields, { a: null })).toEqual([{ target: 'a', source_path: null }])
  })
})

describe('lookupChanges', () => {
  it('lookupChanges_addedEditedRemoved', () => {
    const before = [lk('item', 'i1', { hsn_code: '1' }), lk('item', 'i2', { hsn_code: '2' }), lk('customer', 'c1', { tin: 'T' })]
    const after = [lk('item', 'i1', { hsn_code: '9' }), lk('customer', 'c1', { tin: 'T' }), lk('tax_code', 't1', { tax_category: 'VAT' })]
    expect(lookupChanges(before, after)).toEqual([
      lk('item', 'i1', { hsn_code: '9' }),
      lk('tax_code', 't1', { tax_category: 'VAT' }),
      lk('item', 'i2'),
    ])
  })
})

describe('lookupChanges edges', () => {
  it('lookupChanges_blankAddedIsNotSent', () => {
    expect(lookupChanges([], [lk('item', 'new')])).toEqual([])
  })
})

describe('label helpers', () => {
  it('writebackResultText_everyOutcome', () => {
    expect(writebackResultText('accepted')).toBe('ACCEPTED')
    expect(writebackResultText('rejected')).toBe('REJECTED')
    expect(writebackResultText('failed')).toBe('FAILED')
    expect(writebackResultText('mystery')).toBe('mystery')
  })

  it('heldReasonText_everyReasonAndFailure', () => {
    expect(heldReasonText('differs_from_erp', null)).toBe('Differs from the ERP')
    expect(heldReasonText('changed_after_clearance', null)).toBe('Changed in the ERP after NRS accepted it')
    expect(heldReasonText('deleted_in_erp', null)).toBe('Deleted or voided in the ERP')
    expect(heldReasonText('number_held', null)).toBe('Its number is held by another ERP document')
    const failures: [string, string][] = [
      ['unmappable', 'the mapping could not read it'],
      ['refused', 'the invoice was refused'],
      ['not_found', 'the ERP no longer has it'],
      ['no_mapping', 'this ERP has no mapping'],
      ['exhausted', 'it kept failing'],
    ]
    for (const [code, text] of failures) expect(heldReasonText('failed', code)).toBe(`Failed: ${text}`)
    expect(heldReasonText('failed', 'mystery')).toBe('Failed: mystery')
    expect(heldReasonText('mystery', null)).toBe('mystery')
  })

  it('writebackText_everyStateAndReason', () => {
    expect(writebackText({ state: 'succeeded', reason: null })).toBe('Written back')
    const failed: [string, string][] = [
      ['not_found', 'the ERP document was not found'],
      ['unsupported', 'the ERP does not accept it'],
      ['revision_conflict', 'the ERP document changed first'],
      ['rate_limited', 'the ERP asked to slow down'],
      ['vendor', 'the ERP answered with an error'],
    ]
    for (const [r, t] of failed) expect(writebackText({ state: 'failed', reason: r })).toBe(`Not written: ${t}`)
    const skipped: [string, string][] = [
      ['disconnected', 'the connection was disconnected'],
      ['nothing_writable', 'nothing this ERP can store'],
      ['superseded', 'a newer result replaced it'],
      ['deleted_in_erp', 'deleted in the ERP'],
    ]
    for (const [r, t] of skipped) expect(writebackText({ state: 'skipped', reason: r })).toBe(`Skipped: ${t}`)
    expect(writebackText({ state: 'failed', reason: 'mystery' })).toBe('Not written: mystery')
  })

  it('documentOutcome_everyState', () => {
    const want = { drafted: 'NEW DRAFT', linked: 'LINKED', held: 'HELD', failed: 'FAILED', deleted: 'DELETED IN ERP', pending: 'IN PROGRESS' }
    for (const [state, text] of Object.entries(want)) expect(documentOutcome({ state })).toBe(text)
    expect(documentOutcome({ state: 'mystery' })).toBe('mystery')
  })

  it('activitySummary_zeroAndCounts', () => {
    const zero = { pulled: 0, pending: 0, drafted: 0, linked: 0, held: 0, failed: 0, deleted: 0 }
    expect(activitySummary(zero)).toBe('Nothing pulled in the last 24 hours.')
    expect(activitySummary({ pulled: 6, pending: 1, drafted: 1, linked: 1, held: 1, failed: 1, deleted: 1 })).toBe(
      '6 pulled · 1 new drafts · 1 linked · 1 held · 1 failed · 1 deleted in the ERP · 1 in progress',
    )
    // Distinct counts: with every part 1, a swapped part reads the same.
    expect(activitySummary({ pulled: 27, pending: 2, drafted: 3, linked: 5, held: 7, failed: 11, deleted: 13 })).toBe(
      '27 pulled · 3 new drafts · 5 linked · 7 held · 11 failed · 13 deleted in the ERP · 2 in progress',
    )
  })
})

describe('marks', () => {
  it('changedAfterClearance_picksTheMark', () => {
    const changed = mark({ source_doc_id: 'x', changed_after_clearance_at: '2026-01-01T00:00:00Z' })
    expect(changedAfterClearance([])).toBeNull()
    expect(changedAfterClearance([mark({ differs_from_erp: true }), changed])).toBe(changed)
  })

  it('getInvoiceErpMarks_toleratesABodyWithoutMarks', async () => {
    expect(await getInvoiceErpMarks(stub({ id: 'x' }), 'http://h', 'e', 'i')).toEqual([])
    expect(await getInvoiceErpMarks(stub({ marks: null }), 'http://h', 'e', 'i')).toEqual([])
  })
})
