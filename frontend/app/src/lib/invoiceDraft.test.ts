// RED specs (INVCR-01-02, task-278, Mode A) — pin draftToCreateRequest before the
// executor implements the body in Stage 3. Every spec below currently fails because the
// stub throws `new Error('not implemented')` before ever computing a result -- that IS
// the correct RED reason (assertion / not-implemented), not an import/compile error.
//
// Red-first honesty (task-278 plan): CREATE-1/CREATE-2 (lib/invoices.test.ts), DRAFT-1,
// DRAFT-2, DRAFT-7 are genuinely discriminating -- each fails against a plausible wrong
// implementation. DRAFT-3, DRAFT-5, DRAFT-6, DRAFT-8 are weak-but-real guards: they rule
// out one specific named mistake (`?? ''`, re-hyphenation, `Client.tin`) but any careful
// first implementation passes them. DRAFT-4 is a pure regression guard -- red only
// because the stub throws; no plausible implementation fails it.
//
// DRAFT-3 pins the C7 residual risk in place DELIBERATELY: it asserts the UNREPAIRED
// pass-through. If it ever fails, that is a decision about C7 (see the story description
// / QA Debate Log finding C7), not a bug to fix here.
import { describe, expect, it } from 'vitest'

import { draftToCreateRequest } from './invoiceDraft'
import type { Entity } from './portfolio'
import type { Draft } from '../types'

const baseDraft: Draft = {
  number: 'INV-2026-00482',
  buyer: 'Beta Ltd',
  buyerTin: '00000000002',
  buyerAddress: '12 Marina Road',
  date: '2026-06-16',
  currency: 'NGN',
  wht: false,
  docType: 'B2B',
  items: [{ desc: 'Logistics consulting — Q2', qty: 1, price: 2500000 }],
}

const baseEntity: Pick<Entity, 'id' | 'name' | 'tin'> = {
  id: 'e-1',
  name: 'Lagos Freight Ltd',
  tin: '12345678-0001',
}

describe('draftToCreateRequest: money', () => {
  it('DRAFT-1 money is an exact decimal string', () => {
    // 3 x 1000.005 discriminates the three candidate algorithms (task-278 GAP 2,
    // verified independently in node before authoring this spec):
    //   naive float (3*1000.005).toFixed(2)        -> '3000.01' (WRONG)
    //   exact BigInt, no rounding                    -> '3000.015' (WRONG)
    //   exact BigInt x half-up round to 2dp           -> '3000.02' (the only correct one)
    const draft: Draft = { ...baseDraft, items: [{ desc: 'x', qty: 3, price: 1000.005 }] }

    const result = draftToCreateRequest(draft, baseEntity)

    expect(result.subtotal).toBe('3000.02')
    expect(result.vat).toBe('225.00') // round2(3000.02 * 0.075) = round2(225.0015) = 225.00
    expect(result.total).toBe('3225.02') // subtotal + vat, both already 2dp -- exact
    expect(typeof result.subtotal).toBe('string')
    expect(typeof result.vat).toBe('string')
    expect(typeof result.total).toBe('string')
  })

  it('DRAFT-2 line_total is quantity x unit_price and line_tax is null', () => {
    const draft: Draft = {
      ...baseDraft,
      items: [
        { desc: 'a', qty: 2, price: 1500.25 },
        { desc: 'b', qty: 3, price: 99.99 },
      ],
    }

    const result = draftToCreateRequest(draft, baseEntity)

    expect(result.line_items).toHaveLength(2)
    expect(result.line_items[0].line_total).toBe('3000.50')
    expect(result.line_items[0].quantity).toBe('2')
    expect(result.line_items[0].unit_price).toBe('1500.25')
    expect(result.line_items[0].line_tax).toBeNull()
    expect(result.line_items[1].line_total).toBe('299.97')
    expect(result.line_items[1].quantity).toBe('3')
    expect(result.line_items[1].unit_price).toBe('99.99')
    expect(result.line_items[1].line_tax).toBeNull()
    expect(result.subtotal).toBe('3300.47')
  })
})

describe('draftToCreateRequest: supplier TIN (C7)', () => {
  it('DRAFT-3 a 12-bare-digit entity TIN is passed through unchanged', () => {
    const entity: Pick<Entity, 'id' | 'name' | 'tin'> = { ...baseEntity, tin: '100123450001' }

    const result = draftToCreateRequest(baseDraft, entity)

    // Deliberately NOT '10012345-0001' -- see the C7 note in the file header.
    expect(result.supplier_tin).toBe('100123450001')
  })

  it('DRAFT-5 a TIN-less entity maps to null', () => {
    const entity: Pick<Entity, 'id' | 'name' | 'tin'> = { ...baseEntity, tin: null }

    const result = draftToCreateRequest(baseDraft, entity)

    expect(result.supplier_tin).toBeNull()
    expect(result.supplier_name).toBe(baseEntity.name)
  })
})

describe('draftToCreateRequest: unpersistable / dropped fields', () => {
  it('DRAFT-4 no unpersistable key is ever emitted', () => {
    const draft: Draft = { ...baseDraft, buyerAddress: '12 Marina Road', wht: true, docType: 'B2G' }

    const result = draftToCreateRequest(draft, baseEntity)

    const keys = Object.keys(result)
    expect(keys).not.toContain('buyer_address')
    expect(keys).not.toContain('wht')
    expect(keys).not.toContain('doc_type')
    expect(keys).not.toContain('line_no')
    for (const line of result.line_items) {
      expect(Object.keys(line)).not.toContain('line_no')
      expect(Object.keys(line)).not.toContain('id')
    }
  })
})

describe('draftToCreateRequest: absent optionals', () => {
  it('DRAFT-6 empty optionals are null, not empty string', () => {
    const draft: Draft = { ...baseDraft, buyerTin: '', buyer: '' }

    const result = draftToCreateRequest(draft, baseEntity)

    expect(result.buyer_tin).toBeNull()
    expect(result.buyer_name).toBeNull()
  })
})

describe('draftToCreateRequest: issue_date (GAP 1)', () => {
  it('DRAFT-7 issue_date crosses the wire as RFC3339', () => {
    const draft: Draft = { ...baseDraft, date: '2026-06-16' }

    const result = draftToCreateRequest(draft, baseEntity)

    // A bare 'YYYY-MM-DD' 400s Go's *time.Time decode (`cannot parse "" as "T"`) --
    // verified independently against the live handler before authoring this spec.
    expect(result.issue_date).toBe('2026-06-16T00:00:00Z')

    const blankDateDraft: Draft = { ...baseDraft, date: '' }
    const blankResult = draftToCreateRequest(blankDateDraft, baseEntity)
    expect(blankResult.issue_date).toBeNull()
  })
})

describe('draftToCreateRequest: identity fields', () => {
  it('DRAFT-8 identity fields come from the entity, not the draft', () => {
    const entity: Pick<Entity, 'id' | 'name' | 'tin'> = { id: 'e-1', name: 'Lagos Freight Ltd', tin: '12345678-0001' }
    const draft: Draft = { ...baseDraft, number: 'INV-2026-00999' }

    const result = draftToCreateRequest(draft, entity)

    expect(result.entity_id).toBe('e-1')
    expect(result.supplier_name).toBe('Lagos Freight Ltd')
    expect(result.invoice_number).toBe(draft.number)
  })
})

