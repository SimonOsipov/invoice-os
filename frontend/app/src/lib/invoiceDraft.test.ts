// C7 note: DRAFT-3 asserts the unrepaired TIN pass-through on purpose; a failure there is a product decision, not a bug.
import { describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import {
  applyBuyerRecord,
  buyerRequirement,
  draftLineTaxes,
  draftToCreateRequest,
  draftTotals,
  fileDraftGate,
  fileDraftInvoice,
  fileSuppliedNumber,
  withSupplierDefaults,
  type FileDraftDeps,
  type FileSuppliedNumberDeps,
} from './invoiceDraft'
import type { SupplyNumberRequest } from './importApi'
import type { Entity } from './portfolio'
import type { Customer } from './buyerDirectory'
import { DRAFT_NRS_BLANK, draftLine, emptyClient, startingDraft } from './clients'
import type { Draft, DraftLine } from '../types'

// buyerAddress/wht/docType left `Draft` in INVCR-01-03 (task-279) — the create flow was
// de-intersected from the mock verdict engine's `Validatable`, and none of the three has a
// column or a wire field. They are no longer expressible here at all.
const baseDraft: Draft = {
  number: 'INV-2026-00482',
  buyer: 'Beta Ltd',
  buyerTin: '00000000002',
  date: '2026-06-16',
  currency: 'NGN',
  ...DRAFT_NRS_BLANK,
  items: [draftLine({ desc: 'Logistics consulting — Q2', qty: 1, price: 2500000 })],
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
    const draft: Draft = { ...baseDraft, items: [draftLine({ desc: 'x', qty: 3, price: 1000.005 })] }

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
        draftLine({ desc: 'a', qty: 2, price: 1500.25 }),
        draftLine({ desc: 'b', qty: 3, price: 99.99 }),
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

  it('DRAFT-9 [verbatim-typed-derived-rounded]: quantity/unit_price beyond 2dp cross verbatim, unlike the rounded derived fields', () => {
    // qty is numeric(14,3) and unit_price numeric(14,2), but the TYPED wire values are
    // not rounded to their column scale the way subtotal/vat/total/line_total are --
    // round2() must never touch item.qty/item.price. Both values below carry 3dp so a
    // round2-mutated implementation is forced to visibly diverge: half-up round2 would
    // turn quantity '2.567' into '2.57' and unit_price '1000.005' into '1000.01'.
    const draft: Draft = {
      ...baseDraft,
      items: [draftLine({ desc: 'x', qty: 2.567, price: 1000.005 })],
    }

    const result = draftToCreateRequest(draft, baseEntity)

    expect(result.line_items[0].quantity).toBe('2.567')
    expect(result.line_items[0].unit_price).toBe('1000.005')
    // The derived line_total, by contrast, IS rounded to 2dp: exact 2.567 * 1000.005 =
    // 2567.012835 -> half-up round2 -> '2567.01'.
    expect(result.line_items[0].line_total).toBe('2567.01')
  })

  it('DRAFT-11 subtotal is round2 of the EXACT sum, not the sum of already-rounded line_totals', () => {
    // task-278 GAP-2 refinement: lineSumEval (internal/validation/evaluators_math.go:
    // 225-241) sums unit_price * quantity per line with NO intermediate rounding and
    // NEVER reads line_total -- so subtotal must match round2(Sigma exact products), not
    // Sigma(round2(each product)). quantity is numeric(14,3) and the UI input has no
    // `step`, so a fractional quantity is reachable. Two lines of 2.5 x 1500.25 diverge
    // the two candidate algorithms:
    //   exact line = 3750.625; exact sum = 7501.250 -> round2 -> '7501.25' (CORRECT --
    //     matches the server's own unrounded arithmetic)
    //   rounded line_total = round2(3750.625) = '3750.63'; summed = '7501.26' (WRONG --
    //     drifts 0.01 from the server's computation, > the 0.005 line-items-sum-subtotal
    //     tolerance, firing a false violation on a correctly-declared invoice)
    // Neither DRAFT-1 nor DRAFT-2 discriminates these -- both algorithms agree there.
    const draft: Draft = {
      ...baseDraft,
      items: [
        draftLine({ desc: 'a', qty: 2.5, price: 1500.25 }),
        draftLine({ desc: 'b', qty: 2.5, price: 1500.25 }),
      ],
    }

    const result = draftToCreateRequest(draft, baseEntity)

    expect(result.line_items[0].line_total).toBe('3750.63')
    expect(result.line_items[1].line_total).toBe('3750.63')
    expect(result.subtotal).toBe('7501.25') // NOT '7501.26'
  })

  it('DRAFT-12 an unparseable qty/price crosses as its raw String(), nulls that line_total, and nulls every declared total', () => {
    // QA adversarial coverage (task-278 Stage 4): the mapper's own header comment
    // (invoiceDraft.ts:41-47) and "UNSPECCED edge cases" note in the plan CLAIM this
    // behaviour but no prior spec ASSERTED it -- confirmed by grep, zero hits for "NaN"
    // in this file before this spec. NaN is reachable at the type level (LineItem.qty/
    // price: number, and NaN is a valid `number` at runtime) even though today's
    // <input type="number"> makes it hard to reach through the UI.
    const draft: Draft = {
      ...baseDraft,
      items: [
        draftLine({ desc: 'valid line', qty: 2, price: 100 }),
        draftLine({ desc: 'corrupt line', qty: NaN, price: 50 }),
      ],
    }

    const result = draftToCreateRequest(draft, baseEntity)

    // String(NaN) === 'NaN' crosses verbatim -- parseScaled refuses it, it is never
    // silently coerced to '0' or dropped.
    expect(result.line_items[1].quantity).toBe('NaN')
    expect(result.line_items[1].unit_price).toBe('50')
    // The corrupt line's OWN line_total is null...
    expect(result.line_items[1].line_total).toBeNull()
    // ...but critically, the OTHER, perfectly valid line is untouched per-line: its own
    // line_total is still computed normally. Only the DECLARED totals (subtotal/vat/
    // total), which depend on every line, go null.
    expect(result.line_items[0].line_total).toBe('200.00')
    expect(result.subtotal).toBeNull()
    expect(result.vat).toBeNull()
    expect(result.total).toBeNull()
  })
})

describe('draftToCreateRequest: VAT base', () => {
  it('VAT is taken from the ROUNDED subtotal: exact 0.195 files vat 0.02', () => {
    // 0.20 x 0.075 = 0.015 -> 0.02; the exact 0.195 x 0.075 = 0.014625 -> 0.01.
    const draft: Draft = { ...baseDraft, items: [draftLine({ desc: 'x', qty: 0.5, price: 0.39 })] }

    const result = draftToCreateRequest(draft, baseEntity)

    expect(result.subtotal).toBe('0.20')
    expect(result.vat).toBe('0.02')
    expect(result.total).toBe('0.22')
  })

  it('VAT from rounded subtotal: a subtotal already at 2dp is unaffected', () => {
    const draft: Draft = { ...baseDraft, items: [draftLine({ desc: 'x', qty: 1, price: 100.0 })] }

    const result = draftToCreateRequest(draft, baseEntity)

    expect(result.subtotal).toBe('100.00')
    expect(result.vat).toBe('7.50')
    expect(result.total).toBe('107.50')
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
    // This spec used to override buyerAddress/wht/docType on the input draft to prove the
    // mapper dropped them. INVCR-01-03 removed all three from `Draft`, so those overrides
    // are now COMPILE errors rather than runtime cases — strictly stronger than asserting
    // the mapper ignores them. The output-key assertions below are unchanged and remain the
    // guard: they fail for any mapper that emits these keys from any source, and DRAFT-10
    // separately pins the whole emitted key set exactly.
    const result = draftToCreateRequest(baseDraft, baseEntity)

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

describe('draftToCreateRequest: wire key order', () => {
  it('DRAFT-10 the emitted object matches createRequest\'s own field order byte-for-byte', () => {
    // apiFetch JSON.stringifies the returned object verbatim, so object-literal
    // insertion order IS what crosses the network -- the mapper's own header comment
    // (invoiceDraft.ts:111-113) states the return literal is deliberately ordered to
    // match createRequest's declaration order (handlers.go's `type createRequest`). No other DRAFT-*
    // spec pins this: they all read named properties, which is order-independent.
    const draft: Draft = {
      ...baseDraft,
      items: [draftLine({ desc: 'a', qty: 2, price: 1500.25 })],
    }

    const result = draftToCreateRequest(draft, baseEntity)

    expect(Object.keys(result)).toEqual([
      'entity_id',
      'invoice_number',
      'issue_date',
      'supplier_tin',
      'supplier_name',
      'buyer_tin',
      'buyer_name',
      'currency',
      'subtotal',
      'vat',
      'total',
      'line_items',
      'invoice_kind',
      'tax_currency_code',
      'due_date',
      'issue_time',
      'tax_point_date',
      'payment_status',
      'supplier_email',
      'supplier_telephone',
      'supplier_street',
      'supplier_city',
      'supplier_postal_zone',
      'supplier_country',
      'supplier_state',
      'supplier_lga',
      'buyer_email',
      'buyer_telephone',
      'buyer_street',
      'buyer_city',
      'buyer_postal_zone',
      'buyer_country',
      'buyer_state',
      'buyer_lga',
    ])
    expect(Object.keys(result.line_items[0])).toEqual([
      'description',
      'quantity',
      'unit_price',
      'line_total',
      'line_tax',
      'tax_category',
      'hsn_code',
      'isic_code',
      'product_category',
      'service_category',
      'sellers_item_identification',
      'price_unit',
      'tax_percent',
      'base_quantity',
    ])
  })
})

// RED specs (INVCR-01-03, task-279, Mode A) -- pin fileDraftInvoice/fileDraftGate before
// the executor implements their bodies in Stage 3 (lib/invoiceDraft.ts's stubs both
// throw `new Error('not implemented')`). Every spec below fails on that throw today --
// the correct RED reason (assertion / not-implemented), not an import/compile error.
//
// Red-first honesty (task-279 plan §10): SUBMIT-1/2/3 are genuinely discriminating --
// each falsifies a specific plausible-wrong implementation (SUBMIT-1: affirming before
// create() settles; SUBMIT-2: navigating in a `finally`, swallowing the error, or
// letting the rejection escape the returned promise; SUBMIT-3: a state-flag guard, or a
// re-entrancy check placed after the first `await`). GATE-1 is weak-but-real: any
// careful first implementation passes it; its one real judgement is the
// entity-before-number precedence.
describe('fileDraftInvoice: ordering (SUBMIT-1)', () => {
  it('SUBMIT-1 error/pending land BEFORE create() settles; created/pending:false land AFTER', async () => {
    const log: string[] = []
    const { promise, resolve } = deferred<{ id: string }>()
    const deps: FileDraftDeps = {
      create: () => promise,
      inFlight: { current: false },
      onPending: (p) => log.push(`pending:${p}`),
      onError: (e) => log.push(`error:${e === null ? 'null' : e.message}`),
      onCreated: (id) => log.push(`created:${id}`),
    }

    const call = fileDraftInvoice(baseDraft, baseEntity, deps)
    // RED-phase hygiene only: today's throwing stub rejects `call` synchronously,
    // before the mid-flight assertion below even runs, so without a handler attached
    // here Node reports an unhandledRejection warning once this test fails and exits.
    // `await call` further down still observes the real rejection/resolution --
    // attaching `.catch` here does not swallow it. Once Stage 3 lands, `call` is
    // genuinely pending at this point (create() has not settled), so this is a no-op.
    void call.catch(() => {})

    // Mid-flight: create() has been invoked but its promise is still open. This is the
    // proof, not the end-state assertion below -- an implementation that affirms
    // optimistically (calls onCreated, or advances past 'pending:true') before awaiting
    // create() would already show that divergence right here, even though its FINAL
    // log contents would be identical to a correct implementation's.
    expect(log).toEqual(['error:null', 'pending:true'])

    resolve({ id: 'u-1' })
    await call

    expect(log).toEqual(['error:null', 'pending:true', 'created:u-1', 'pending:false'])
  })
})

describe('fileDraftInvoice: error path (SUBMIT-2)', () => {
  it('SUBMIT-2 a rejected create() reports via onError with the real status/message, never calls onCreated, and the returned promise still resolves', async () => {
    const err = new ApiError('http', 'duplicate invoice number', 409)
    const create = vi.fn(() => Promise.reject(err))
    const onPending = vi.fn()
    const onError = vi.fn()
    const onCreated = vi.fn()
    const inFlight = { current: false }

    await expect(
      fileDraftInvoice(baseDraft, baseEntity, { create, inFlight, onPending, onError, onCreated }),
    ).resolves.toBeUndefined()

    expect(onCreated).not.toHaveBeenCalled()
    expect(onError).toHaveBeenCalledWith(expect.objectContaining({ status: 409, message: 'duplicate invoice number' }))
    expect(onPending).toHaveBeenLastCalledWith(false)
    expect(inFlight.current).toBe(false)
  })
})

describe('fileDraftInvoice: re-entrancy (SUBMIT-3)', () => {
  it('SUBMIT-3 a shared inFlight ref collapses two synchronous calls into one create(), and clears once settled so a third call fires again', async () => {
    const inFlight = { current: false }
    const first = deferred<{ id: string }>()
    const create = vi.fn(() => first.promise)
    const deps: FileDraftDeps = { create, inFlight, onPending: vi.fn(), onError: vi.fn(), onCreated: vi.fn() }

    const call1 = fileDraftInvoice(baseDraft, baseEntity, deps)
    const call2 = fileDraftInvoice(baseDraft, baseEntity, deps)
    // RED-phase hygiene only -- see the identical note in SUBMIT-1 above.
    void call1.catch(() => {})
    void call2.catch(() => {})

    // Both calls fired synchronously before either could await -- a state guard
    // checked only after the first `await` would let both through.
    expect(create).toHaveBeenCalledTimes(1)

    first.resolve({ id: 'u-1' })
    await call1
    await call2

    expect(create).toHaveBeenCalledTimes(1)
    expect(inFlight.current).toBe(false)

    // A third call, after the first has fully settled, DOES fire -- proving the flag
    // clears rather than latching permanently.
    const second = deferred<{ id: string }>()
    create.mockImplementationOnce(() => second.promise)
    const call3 = fileDraftInvoice(baseDraft, baseEntity, deps)
    void call3.catch(() => {})

    expect(create).toHaveBeenCalledTimes(2)
    second.resolve({ id: 'u-2' })
    await call3
  })
})

describe('fileDraftGate (GATE-1)', () => {
  it('GATE-1 gates on the resolved entity first, the invoice number second', () => {
    expect(fileDraftGate(baseDraft, null)).toEqual({ canFile: false, reason: 'Filing needs a linked entity' })
    expect(fileDraftGate({ ...baseDraft, number: '' }, baseEntity)).toEqual({
      canFile: false,
      reason: 'Invoice number is required',
    })
    // Both fail at once -> entity wins: it is unresolvable in-app (no picker on this
    // screen), while a blank number is resolvable (the field is editable) -- so entity
    // is checked first.
    expect(fileDraftGate({ ...baseDraft, number: '' }, null)).toEqual({
      canFile: false,
      reason: 'Filing needs a linked entity',
    })
    expect(fileDraftGate(baseDraft, baseEntity)).toEqual({ canFile: true })
  })
})

// QA (Stage 4, task-279): GATE-1 is architect-flagged "weak-but-real" -- it exercises
// every branch's OUTCOME but not every branch's TRIGGER independently, and does not pin
// [no-trim-at-either-layer] for the gate itself (only draftToCreateRequest's DRAFT-6
// does, for a different function). These four close that gap without re-testing GATE-1's
// own assertions.
describe('fileDraftGate: adversarial / boundary (QA, task-279)', () => {
  it('QA-GATE-1 a whitespace-only invoice number is NOT gated -- only the exact empty string is (mirrors [no-trim-at-either-layer])', () => {
    // draft.number === '' is the ONLY blank check fileDraftGate makes (invoiceDraft.ts:
    // 237) -- neither layer trims, so a lone space is a (weird but) non-empty string and
    // must pass. A `.trim() === ''` implementation would wrongly gate this.
    expect(fileDraftGate({ ...baseDraft, number: ' ' }, baseEntity)).toEqual({ canFile: true })
  })

  it('QA-GATE-2 entity identity alone gates -- a TIN-less or empty-name entity still counts as resolved', () => {
    // The gate's entity branch is `entity === null`, nothing deeper: it is not the
    // draftToCreateRequest mapper, and has no reason to inspect entity.tin/name. A
    // TIN-less entity (legal -- DRAFT-5 pins the mapper's own null-TIN handling) must
    // still be treated as "resolved" here.
    const tinlessEntity: Pick<Entity, 'id' | 'name' | 'tin'> = { id: 'e-2', name: '', tin: null }
    expect(fileDraftGate(baseDraft, tinlessEntity)).toEqual({ canFile: true })
  })

  it('QA-GATE-3 the two reason strings are exact -- no trailing punctuation, no interpolation', () => {
    // Byte-exact pin, independent of GATE-1's toEqual checks: CreateForm renders
    // gate.reason directly as the button's label (task-279 plan §8.5), and the entity
    // reason must stay byte-identical to CreateMapping's own refusal copy
    // ([file-is-the-verb]) -- a stray space or period here would silently fork the two
    // wordings apart.
    const entityResult = fileDraftGate(baseDraft, null)
    const numberResult = fileDraftGate({ ...baseDraft, number: '' }, baseEntity)
    expect(entityResult.canFile === false && entityResult.reason).toBe('Filing needs a linked entity')
    expect(numberResult.canFile === false && numberResult.reason).toBe('Invoice number is required')
  })

  it('QA-GATE-4 canFile:true never carries a reason key at all', () => {
    // The discriminated union's positive branch is `{ canFile: true }` with no other
    // members (invoiceDraft.ts:238) -- a caller narrowing on `canFile` must not find a
    // stray `reason` left over from a careless spread.
    const result = fileDraftGate(baseDraft, baseEntity)
    expect(Object.keys(result)).toEqual(['canFile'])
  })
})

// QA (Stage 4, task-279): fileDraftInvoice edge cases the 7 red specs (SUBMIT-1/2/3,
// GATE-1) don't reach -- all three drive a REAL synchronous/malformed failure through
// the function rather than a well-behaved mock, which is exactly the class of input the
// red specs (authored against a throwing stub, before any implementation existed) could
// not anticipate.
describe('fileDraftInvoice: edge cases beyond the red specs (QA, task-279)', () => {
  it('QA-SUBMIT-1 create() resolving without an `id` still reaches onCreated -- fileDraftInvoice does not validate the shape at runtime', () => {
    // `create`'s type is `Promise<{id:string}>`, but nothing inside fileDraftInvoice
    // re-checks that contract at runtime -- it trusts the caller. A misbehaving `create`
    // (a malformed server response the api-client layer failed to catch) resolving with
    // no `id` at all must not throw or hang; it silently reaches onCreated with
    // `undefined`. Documented as ACTUAL behaviour, not a claim it is correct -- App.tsx's
    // real `onCreated` is `openImportedInvoice`, which would then navigate to
    // `/detail` for invoiceId `undefined`, a downstream concern outside this function.
    const onCreated = vi.fn()
    const onError = vi.fn()
    const onPending = vi.fn()
    const create = vi.fn(() => Promise.resolve({} as { id: string }))
    const inFlight = { current: false }

    return fileDraftInvoice(baseDraft, baseEntity, { create, inFlight, onPending, onError, onCreated }).then(() => {
      expect(onCreated).toHaveBeenCalledWith(undefined)
      // onError(null) still fires once, unconditionally, at the top of the body-order
      // contract (the "clear any previous error" observation) -- this is NOT a new
      // error being reported for the missing `id`, so pin it explicitly rather than
      // asserting onError was never called at all.
      expect(onError).toHaveBeenCalledTimes(1)
      expect(onError).toHaveBeenCalledWith(null)
      expect(inFlight.current).toBe(false)
    })
  })

  it('QA-SUBMIT-2 a synchronous throw from draftToCreateRequest lands on onError, never escapes, and the returned promise still resolves', () => {
    // draftToCreateRequest is called INSIDE fileDraftInvoice's try block, synchronously,
    // before `create` is ever invoked (invoiceDraft.ts:203) -- so a malformed draft that
    // makes the mapper throw (rather than reject a promise) must be caught by the SAME
    // try/catch as a rejected create(), not escape as an unhandled synchronous
    // exception. `items` typed as an array but forced to `null` here (a shape no
    // TypeScript caller can produce, but a corrupted/hand-built Draft at runtime could)
    // makes `draft.items.map(...)` throw a real TypeError -- not a fake/injected one.
    const malformedDraft = { ...baseDraft, items: null } as unknown as Draft
    const create = vi.fn()
    const onCreated = vi.fn()
    const onError = vi.fn()
    const onPending = vi.fn()
    const inFlight = { current: false }

    return expect(
      fileDraftInvoice(malformedDraft, baseEntity, { create, inFlight, onPending, onError, onCreated }),
    )
      .resolves.toBeUndefined()
      .then(() => {
        expect(create).not.toHaveBeenCalled()
        expect(onCreated).not.toHaveBeenCalled()
        // Two onError calls: the unconditional onError(null) clear at the top of the
        // body-order contract, then the actual reported error from the catch clause --
        // the synchronous throw did NOT skip the clear or double up on it.
        expect(onError).toHaveBeenCalledTimes(2)
        expect(onError).toHaveBeenNthCalledWith(1, null)
        const [reported] = onError.mock.calls[1]
        expect(reported).not.toBeNull()
        expect(reported.message).toMatch(/map is not a function|Cannot read propert/)
        expect(onPending).toHaveBeenLastCalledWith(false)
        expect(inFlight.current).toBe(false)
      })
  })

  it('QA-SUBMIT-3 onPending(false) still fires via `finally` when onCreated itself throws synchronously', () => {
    // NOTE: this also demonstrates a real (not merely hypothetical) sharp edge, worth
    // recording for the team rather than silently passing over: onCreated() is called
    // INSIDE the same try block as `create()` (invoiceDraft.ts:204), so a throw from
    // onCreated is caught by the SAME catch clause as a network failure and gets
    // reported to onError as if the FILING itself had failed -- even though the POST
    // genuinely succeeded and the invoice now exists server-side. In production
    // onCreated is the bare `openImportedInvoice` reference (App.tsx), which does not
    // throw under normal navigation, so this is not reachable today -- but the ordering
    // is asserted here because `finally` firing regardless is the one guarantee this
    // test can make, and the mislabeling is worth flagging rather than assuming away.
    const boom = new Error('onCreated blew up')
    const onCreated = vi.fn(() => {
      throw boom
    })
    const onError = vi.fn()
    const onPending = vi.fn()
    const create = vi.fn(() => Promise.resolve({ id: 'u-1' }))
    const inFlight = { current: false }

    return fileDraftInvoice(baseDraft, baseEntity, { create, inFlight, onPending, onError, onCreated }).then(() => {
      // The one guarantee under test: finally always runs.
      expect(onPending).toHaveBeenLastCalledWith(false)
      expect(inFlight.current).toBe(false)
      // Documented actual behaviour: onCreated's own throw is caught upstream and
      // reported as a filing error, despite `create()` having already resolved.
      expect(onError).toHaveBeenCalledWith(expect.objectContaining({ message: 'onCreated blew up' }))
    })
  })
})

// Local helper -- no shared 'deferred promise' convention exists yet elsewhere in this
// repo's tests (grepped before adding this). `resolve` is captured out of the Promise
// executor so a test can control exactly WHEN create() settles, independent of when it
// is called -- the mechanism SUBMIT-1's mid-flight assertion and SUBMIT-3's
// call-count-before-settling assertion both depend on.
function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

// EXTR-15-06, task-832 -- SD-8 / AC-8.
//
// The recorded document id is draftToCreateRequest's THIRD, optional parameter, so every
// shipped call site and every DRAFT-* fixture above keeps compiling.
//
// `Draft` was deliberately narrowed (types.ts:79-81) to fields that have a column and a wire
// key; a document id is recorded by the hand-off, not typed by the operator, so it does not
// belong on that type. If EXTR-15-07 records it on `Draft` instead, this spec is what has to
// change -- the decision is stated here rather than assumed.
const SD8_DOCUMENT_ID = '7f1c2a90-1c4b-4f0e-9f2a-2c0f5f3a11bd'

describe('draftToCreateRequest: source_document_id (EXTR-15-06)', () => {
  it('SD-8 a recorded document id crosses the wire; none omits the key entirely', () => {
    const withDocument = draftToCreateRequest(baseDraft, baseEntity, SD8_DOCUMENT_ID)
    expect(withDocument.source_document_id).toBe(SD8_DOCUMENT_ID)

    // Absent, not null: JSON.stringify drops `undefined`, so `in` is the only honest oracle
    // for "the key never crossed".
    const withoutDocument = draftToCreateRequest(baseDraft, baseEntity)
    expect('source_document_id' in withoutDocument).toBe(false)

    // Control: the mapper really ran in the second call. Without it the `in` check above
    // would also pass on an empty object.
    expect(withoutDocument.entity_id).toBe(baseEntity.id)
    expect(withoutDocument.invoice_number).toBe(baseDraft.number)
  })

  it('SD-8 the key follows the twelve legacy keys and precedes the 22 NRS header keys, createRequest\'s order', () => {
    // DRAFT-10 above pins the order when no document was recorded.
    const result = draftToCreateRequest(baseDraft, baseEntity, SD8_DOCUMENT_ID)

    expect(Object.keys(result).slice(0, 13)).toEqual([
      'entity_id',
      'invoice_number',
      'issue_date',
      'supplier_tin',
      'supplier_name',
      'buyer_tin',
      'buyer_name',
      'currency',
      'subtotal',
      'vat',
      'total',
      'line_items',
      'source_document_id',
    ])
    expect(Object.keys(result)).toHaveLength(35)
  })
})

// ============================================================================
// RED spec (EXTR-15-07, task-833, Mode A) — HO-6 / AC-6.
//
// EXTR-15-06 gave draftToCreateRequest the id as a third parameter, but nothing calls it
// that way: fileDraftInvoice (invoiceDraft.ts:217) still calls
// `draftToCreateRequest(draft, entity)`, so a hand-typed invoice filed after a hand-off
// crosses the wire with no provenance at all. The hand-off's recorded id has to reach
// `create`.
//
// The id is a FOURTH, optional parameter on fileDraftInvoice, mirroring
// draftToCreateRequest's own third — not a field on Draft (types.ts holds only what the
// operator types) and not a member of FileDraftDeps (deps are the transport; this is a
// value about the invoice). If EXTR-15-07 records it somewhere else, THIS spec is what
// has to change; the decision is stated rather than assumed.
//
// Declared as a TYPE and assigned, never called four-arg against the shipped export: TS
// accepts a 3-param function where a 4-param type is wanted, so this compiles against
// both the shipped and the widened signature. The red is therefore an assertion red, not
// a tsc red that would mask the one AC-1 owns in importRun.test.ts. Same idiom as
// documentRun.test.ts's `Refuse` alias (EXTR-15-04).
type FileDraft = (
  draft: Draft,
  entity: Pick<Entity, 'id' | 'name' | 'tin'>,
  deps: FileDraftDeps,
  sourceDocumentId?: string,
) => Promise<void>
const fileDraftWithDocument: FileDraft = fileDraftInvoice

const HO6_DOCUMENT_ID = '3c2b9d18-5a44-4e21-8f0b-6d7e1a9c4b02'

describe('fileDraftInvoice: the hand-off’s document reaches the create body (HO-6, AC-6)', () => {
  function capture() {
    const bodies: unknown[] = []
    const deps: FileDraftDeps = {
      create: async (input) => {
        bodies.push(input)
        return { id: 'inv-created-1' }
      },
      inFlight: { current: false },
      onPending: () => {},
      onError: () => {},
      onCreated: () => {},
    }
    return { bodies, deps }
  }

  it('HO-6: a recorded document id crosses the wire as source_document_id', async () => {
    const { bodies, deps } = capture()

    await fileDraftWithDocument(baseDraft, baseEntity, deps, HO6_DOCUMENT_ID)

    expect(bodies, 'create must have been called exactly once').toHaveLength(1)
    const body = bodies[0] as Record<string, unknown>
    // Control: the mapper really ran, so the key assertion below is not read off an
    // empty object.
    expect(body.entity_id).toBe(baseEntity.id)
    expect(body.invoice_number).toBe(baseDraft.number)
    expect(body.source_document_id).toBe(HO6_DOCUMENT_ID)
  })

  it('HO-6b: filing with no hand-off omits the key entirely — undefined, never null', async () => {
    // The negative leg, paired with the positive above. `in` is the only honest oracle:
    // JSON.stringify drops undefined, so a `toBeUndefined` would also pass on a body that
    // explicitly sent `source_document_id: undefined` and on one that sent null.
    const { bodies, deps } = capture()

    await fileDraftWithDocument(baseDraft, baseEntity, deps)

    expect(bodies).toHaveLength(1)
    const body = bodies[0] as Record<string, unknown>
    expect(body.entity_id).toBe(baseEntity.id)
    expect('source_document_id' in body).toBe(false)
  })

  it('HO-6c: the id does not leak between two filings from the same module', async () => {
    // Falsification: an impl that stashes the recorded id in module state instead of
    // threading the parameter would keep sending it on the next, unrelated filing.
    const { bodies, deps } = capture()

    await fileDraftWithDocument(baseDraft, baseEntity, deps, HO6_DOCUMENT_ID)
    await fileDraftWithDocument({ ...baseDraft, number: 'INV-2026-00483' }, baseEntity, deps)

    expect(bodies).toHaveLength(2)
    expect((bodies[0] as Record<string, unknown>).source_document_id).toBe(HO6_DOCUMENT_ID)
    expect('source_document_id' in (bodies[1] as Record<string, unknown>)).toBe(false)
  })
})

const F_TAKEN = 'This invoice number is already in the register for this company. Enter a different number.'

describe('fileSuppliedNumber: ordering + refusal (EXTR27-F1, F2)', () => {
  it('EXTR27-F1: a supplied number files once, affirms only on resolve, and posts only entity, document and number', async () => {
    const log: string[] = []
    const { promise, resolve } = deferred<{ id: string }>()
    const bodies: SupplyNumberRequest[] = []
    const deps: FileSuppliedNumberDeps = {
      supply: (req) => {
        bodies.push(req)
        return promise
      },
      inFlight: { current: false },
      onPending: (p) => log.push(`pending:${p}`),
      onError: (e) => log.push(`error:${e === null ? 'null' : e.message}`),
      onCreated: (id) => log.push(`created:${id}`),
    }

    const call1 = fileSuppliedNumber('N-1', baseEntity, 'd-1', deps)
    const call2 = fileSuppliedNumber('N-1', baseEntity, 'd-1', deps)

    expect(log).toEqual(['error:null', 'pending:true'])
    expect(bodies).toEqual([{ entity_id: baseEntity.id, document_id: 'd-1', invoice_number: 'N-1' }])

    resolve({ id: 'inv-9' })
    await call1
    await call2

    expect(log).toEqual(['error:null', 'pending:true', 'created:inv-9', 'pending:false'])
    expect(bodies, 'a re-entrant call must not reach supply() a second time').toHaveLength(1)
    expect(deps.inFlight.current).toBe(false)
  })

  it('EXTR27-F2: a refused supply lands on onError verbatim, never affirms, and the promise resolves', async () => {
    const err = new ApiError('http', F_TAKEN, 409)
    const supply = vi.fn(() => Promise.reject(err))
    const onPending = vi.fn()
    const onError = vi.fn()
    const onCreated = vi.fn()
    const inFlight = { current: false }

    await expect(
      fileSuppliedNumber('N-1', baseEntity, 'd-1', { supply, inFlight, onPending, onError, onCreated }),
    ).resolves.toBeUndefined()

    expect(onCreated).not.toHaveBeenCalled()
    expect(onError).toHaveBeenCalledWith(expect.objectContaining({ status: 409, message: F_TAKEN }))
    expect(onPending).toHaveBeenLastCalledWith(false)
    expect(inFlight.current).toBe(false)
  })
})


describe('draftTotals (TEST-03-04)', () => {
  const cases: Array<[string, Draft['items']]> = [
    ['one line', [draftLine({ desc: 'A', qty: 1, price: 20.93 })]],
    [
      'three lines, 3-decimal qty',
      [
        draftLine({ desc: 'A', qty: 1.005, price: 19.99 }),
        draftLine({ desc: 'B', qty: 2.125, price: 0.99 }),
        draftLine({ desc: 'C', qty: 0.333, price: 1000.005 }),
      ],
    ],
    ['empty items', []],
    ['NaN qty', [draftLine({ desc: 'A', qty: NaN, price: 10 })]],
  ]

  it('draftTotals agrees with draftToCreateRequest', () => {
    const wires = cases.map(([, items]) => draftToCreateRequest({ ...baseDraft, items }, baseEntity))
    // Two cases must file real figures, or an all-null draftTotals passes the table.
    expect(wires.filter((w) => w.total !== null)).toHaveLength(2)
    cases.forEach(([name, items], i) => {
      const wire = wires[i]!
      const { vatLabel: _label, ...money } = draftTotals(items)
      expect(money, name).toEqual({ subtotal: wire.subtotal, vat: wire.vat, total: wire.total })
    })
  })
})

describe('draftToCreateRequest: a blank hand-off draft (AUTH-10-07)', () => {
  it('a blank hand-off draft maps to an honest request', () => {
    const result = draftToCreateRequest({ ...startingDraft(emptyClient(), true), number: 'X' }, baseEntity)

    expect(result.invoice_number, 'control: the typed number crosses the wire').toBe('X')
    expect(result.issue_date).toBeNull()
    expect(result.buyer_name).toBeNull()
    expect(result.buyer_tin).toBeNull()
    expect(result.line_items, 'the one empty line crosses the wire').toHaveLength(1)
    expect(result.line_items[0]).toMatchObject({ description: null, quantity: '1', unit_price: '0' })
    expect(JSON.stringify(result), 'no NaN reaches the wire').not.toMatch(/NaN/)
  })

  it('a blank hand-off draft is refused by fileDraftGate until a number is typed', () => {
    const blank = startingDraft(emptyClient(), true)
    expect(fileDraftGate(blank, baseEntity)).toEqual({ canFile: false, reason: 'Invoice number is required' })
    expect(fileDraftGate({ ...blank, number: 'X' }, baseEntity)).toEqual({ canFile: true })
  })
})

// A categorised line: one unit at `price` unless `qty` is given.
const taxed = (price: number, tax_category = 'STANDARD_VAT', tax_percent = '7.5', qty = 1): DraftLine =>
  draftLine({ desc: 'x', qty, price, tax_category, tax_percent })

describe('draftLineTaxes: largest remainder (AC-1)', () => {
  it('draftLineTaxes_largestRemainderMeetsTheGroupTotal', () => {
    // Per-line rounding gives 0.01 x 3 = 0.03; the group total is round2(0.0225) = 0.02.
    const taxes = draftLineTaxes([taxed(0.1), taxed(0.1), taxed(0.1)])

    expect(taxes).toEqual(['0.01', '0.01', '0.00'])
  })

  it('draftLineTaxes_tiesGoToTheLowerLine', () => {
    // Exact 0.015 each, T = 0.03: the one leftover kobo goes to line 1.
    expect(draftLineTaxes([taxed(0.2), taxed(0.2)])).toEqual(['0.02', '0.01'])
  })

  it('draftLineTaxes_leftoverGoesToTheLargestRemainder', () => {
    // Exact 0.0015, 0.0045, 0.0075; T = round2(0.0135) = 0.01 goes to line 3, not line 1.
    expect(draftLineTaxes([taxed(0.02), taxed(0.06), taxed(0.1)])).toEqual(['0.00', '0.00', '0.01'])
  })

  it('draftLineTaxes_eachLineWithinAKobo', () => {
    const prices = [0.1, 1.37, 12.99, 100.01, 0.03, 45.55, 7.77]
    const taxes = draftLineTaxes(prices.map((p) => taxed(p)))

    // T = round2(167.82 x 0.075) = 12.59; three leftover kobo go to lines 1, 6 and 3 by remainder.
    expect(taxes).toEqual(['0.01', '0.10', '0.98', '7.50', '0.00', '3.42', '0.58'])
    const cents = taxes.map((t) => BigInt(t!.replace('.', '')))
    expect(cents.reduce((a, b) => a + b, 0n)).toBe(1259n)
    prices.forEach((p, i) => {
      const exactKobo = (BigInt(Math.round(p * 100)) * 75n) / 1000n
      expect(cents[i]! - exactKobo <= 1n && cents[i]! >= exactKobo, `line ${i + 1}`).toBe(true)
    })
  })

  it('draftLineTaxes_largeAmountsStayExact', () => {
    // Exact tax 74999999999.99925, which a float cannot hold at the kobo.
    expect(draftLineTaxes([taxed(999999999999.99)])).toEqual(['75000000000.00'])
  })

  it('draftLineTaxes_negativeLine', () => {
    expect(draftLineTaxes([taxed(-100)])).toEqual(['-7.50'])
  })

  it('draftLineTaxes_twoGroupsEachMeetTheirOwnTotal', () => {
    // Interleaved: 7.5% lines at 1, 3, 5 share T = 0.02; 5% lines at 2, 4 share T = 0.01.
    const taxes = draftLineTaxes([taxed(0.1), taxed(0.1, 'STANDARD_VAT', '5'), taxed(0.1), taxed(0.1, 'STANDARD_VAT', '5'), taxed(0.1)])

    expect(taxes).toEqual(['0.01', '0.01', '0.01', '0.00', '0.00'])
  })

  it('draftLineTaxes_groupsByCategoryAndPercent', () => {
    // Same percent, two categories: 2 lines (T = 0.02) and 1 line (T = 0.01). One group would give 0.02 in all.
    const taxes = draftLineTaxes([taxed(0.1), taxed(0.1), taxed(0.1, 'LOCAL_SALES_TAX')])

    expect(taxes).toEqual(['0.01', '0.01', '0.01'])
  })

  it('draftLineTaxes_mathsUseThePercentRoundedTo2dp', () => {
    expect(draftLineTaxes([taxed(100000, 'STANDARD_VAT', '7.504')])).toEqual(['7500.00'])
  })

  it('draftLineTaxes_halfUpAtTheKoboTie', () => {
    // Exact 0.015, 0.045, 0.01425, 0.0135: ties go up, the rest by distance.
    expect(draftLineTaxes([taxed(0.2)])).toEqual(['0.02'])
    expect(draftLineTaxes([taxed(0.6)])).toEqual(['0.05'])
    expect(draftLineTaxes([taxed(0.19)])).toEqual(['0.01'])
    expect(draftLineTaxes([taxed(0.18)])).toEqual(['0.01'])
  })

  it('draftLineTaxes_negativeTieRoundsAwayFromZero', () => {
    expect(draftLineTaxes([taxed(-0.2)])).toEqual(['-0.02'])
    expect(draftLineTaxes([taxed(-0.6)])).toEqual(['-0.05'])
    expect(draftLineTaxes([taxed(-0.19)])).toEqual(['-0.01'])
  })

  it('draftLineTaxes_negativeGroupFloorsTowardMinusInfinity', () => {
    // Pinned: T = -0.03 splits as -0.01, -0.02, because the floor of -0.015 is -0.02 and the
    // leftover kobo goes to the lower line.
    const taxes = draftLineTaxes([taxed(-0.2), taxed(-0.2)])

    expect(taxes).toEqual(['-0.01', '-0.02'])
    expect(BigInt(taxes[0]!.replace('.', '')) + BigInt(taxes[1]!.replace('.', ''))).toBe(-3n)
  })

  it('draftLineTaxes_mixedSignsShareOneGroupTotal', () => {
    // Exact 0.015 and -0.0075; T = round2(0.0075) = 0.01.
    expect(draftLineTaxes([taxed(0.2), taxed(-0.1)])).toEqual(['0.02', '-0.01'])
    expect(draftLineTaxes([taxed(100), taxed(-100)])).toEqual(['7.50', '-7.50'])
  })

  it('draftLineTaxes_zeroAndNegativeQuantitiesAndPrices', () => {
    const taxes = draftLineTaxes([
      taxed(50, 'STANDARD_VAT', '7.5', 0),
      taxed(0),
      taxed(50, 'STANDARD_VAT', '7.5', -2),
      taxed(-50, 'STANDARD_VAT', '7.5', -2),
    ])

    expect(taxes).toEqual(['0.00', '0.00', '-7.50', '7.50'])
  })

  it('draftLineTaxes_percentRoundsHalfUpAt2dp', () => {
    // 100000.00 x percent: '7.505' -> 7.51, '7.495' -> 7.50, '0.004' -> 0, '0.005' -> 0.01.
    expect(draftLineTaxes([taxed(100000, 'STANDARD_VAT', '7.505')])).toEqual(['7510.00'])
    expect(draftLineTaxes([taxed(100000, 'STANDARD_VAT', '7.495')])).toEqual(['7500.00'])
    expect(draftLineTaxes([taxed(100000, 'STANDARD_VAT', '0.004')])).toEqual(['0.00'])
    expect(draftLineTaxes([taxed(100000, 'STANDARD_VAT', '0.005')])).toEqual(['10.00'])
    expect(draftLineTaxes([taxed(100000, 'STANDARD_VAT', '75e-1')])).toEqual(['7500.00'])
  })

  it('draftLineTaxes_taxIsTakenOnTheRoundedLineTotal', () => {
    // Float 1.005 is 1.00499...; the exact line total rounds half-up to 1.01 and the tax follows it.
    expect(draftLineTaxes([taxed(1.005, 'STANDARD_VAT', '100')])).toEqual(['1.01'])
    // 0.005 rounds to 0.01 before the 99% rate: 0.0099 -> 0.01, not 0.00495 -> 0.00.
    expect(draftLineTaxes([taxed(0.005, 'STANDARD_VAT', '99')])).toEqual(['0.01'])
    // qty x price = 1.005 exactly.
    expect(draftLineTaxes([taxed(0.335, 'STANDARD_VAT', '100', 3)])).toEqual(['1.01'])
  })

  it('draftLineTaxes_hugeTieRoundsHalfUpExactly', () => {
    // 1234567890123420 kobo x 7.5% is exactly 92592591759256.5 kobo; a float product (9.2e17) lands 8 below the tie.
    expect(draftLineTaxes([taxed(12345678901234.2)])).toEqual(['925925917592.57'])
  })

  it('draftLineTaxes_exactPastTheFloatRange', () => {
    // Ten lines of 12345678901234.56: 1.2e16 kobo of base, past 2^53. Exact tax 92592591759259.2 each, T = 925925917592592.
    const taxes = draftLineTaxes(Array.from({ length: 10 }, () => taxed(12345678901234.56)))

    expect(taxes).toEqual(['925925917592.60', '925925917592.60', ...Array<string>(8).fill('925925917592.59')])
  })
})

describe('draftLineTaxes: groups and nulls (AC-2)', () => {
  it('draftLineTaxes_percentGroupsByValue', () => {
    // Merged: T = round2(0.0225) = 0.02. Apart, '7.5' would give 0.02 and '7.50' 0.01.
    const taxes = draftLineTaxes([taxed(0.1, 'STANDARD_VAT', '7.5'), taxed(0.1, 'STANDARD_VAT', '7.50'), taxed(0.1, 'STANDARD_VAT', '7.5')])

    expect(taxes).toEqual(['0.01', '0.01', '0.00'])
  })

  it('draftLineTaxes_nullWithoutCategoryPercentOrTotal', () => {
    const taxes = draftLineTaxes([
      taxed(0.1),
      taxed(0.1, ''),
      taxed(0.1, 'STANDARD_VAT', 'abc'),
      taxed(0.1, 'STANDARD_VAT', ''),
      draftLine({ desc: 'x', qty: NaN, price: 1, tax_category: 'STANDARD_VAT', tax_percent: '7.5' }),
      taxed(0.1),
    ])

    // The two valid lines form their own group: T = round2(0.015) = 0.02.
    expect(taxes).toEqual(['0.01', null, null, null, null, '0.01'])
  })

  it('draftLineTaxes_zeroRate', () => {
    expect(draftLineTaxes([taxed(100, 'ZERO_VAT', '0'), taxed(5.55, 'ZERO_VAT', '0.00')])).toEqual(['0.00', '0.00'])
  })

  it('draftLineTaxes_aPercentThatDoesNotParseGivesNoTax', () => {
    // Pinned: the field takes a bare decimal, so a typed '7.5%' or '7,5' files no line tax.
    const taxes = draftLineTaxes(['7.5%', '7,5', '.5', ' 7.5', '7.5 '].map((p) => taxed(100, 'STANDARD_VAT', p)))

    expect(taxes).toEqual([null, null, null, null, null])
  })

  it('draftLineTaxes_aLineWithoutATotalDoesNotJoinItsGroup', () => {
    // The unparseable line is not a member: the two valid lines still split T = 0.02.
    const taxes = draftLineTaxes([
      taxed(0.1),
      draftLine({ desc: 'x', qty: 1, price: Number.POSITIVE_INFINITY, tax_category: 'STANDARD_VAT', tax_percent: '7.5' }),
      taxed(0.1),
    ])

    expect(taxes).toEqual(['0.01', null, '0.01'])
  })

  it('draftLineTaxes_oneTaxPerLineAndInInputOrder', () => {
    const items = [taxed(10), taxed(10, 'ZERO_VAT', '0'), draftLine({ desc: 'x', qty: 1, price: 10 }), taxed(10, 'STANDARD_VAT', '5')]

    expect(draftLineTaxes(items)).toEqual(['0.75', '0.00', null, '0.50'])
    expect(draftLineTaxes([])).toEqual([])
  })
})

describe('draftTotals: category-aware VAT (AC-3)', () => {
  it('draftTotals_uncategorisedKeepsTheFlatRate', () => {
    const totals = draftTotals([draftLine({ desc: 'x', qty: 3, price: 1000.005 })])

    expect(totals).toEqual({ subtotal: '3000.02', vat: '225.00', total: '3225.02', vatLabel: 'VAT · 7.5%' })
  })

  it('draftTotals_categorisedSumsTheGroups', () => {
    const totals = draftTotals([taxed(1000), taxed(500, 'ZERO_VAT', '0')])

    // Flat 7.5% of 1500.00 would be 112.50.
    expect(totals).toEqual({ subtotal: '1500.00', vat: '75.00', total: '1575.00', vatLabel: 'VAT' })
  })

  it('draftTotals_uncategorisedLineAddsNoTax', () => {
    const totals = draftTotals([taxed(200), draftLine({ desc: 'x', qty: 1, price: 100 })])

    expect(totals).toEqual({ subtotal: '300.00', vat: '15.00', total: '315.00', vatLabel: 'VAT' })
  })

  it('draftTotals_aCategorisedLineWithoutAPercentFilesNoVat', () => {
    // Pinned: the label drops to VAT, the line has no tax, and VAT is 0.00 although the flat rate would give 112.50.
    const totals = draftTotals([taxed(1000, 'STANDARD_VAT', ''), draftLine({ desc: 'x', qty: 1, price: 500 })])

    expect(totals).toEqual({ subtotal: '1500.00', vat: '0.00', total: '1500.00', vatLabel: 'VAT' })
  })

  it('draftTotals_emptyAndUnparseableLinesStayNull', () => {
    expect(draftTotals([])).toEqual({ subtotal: null, vat: null, total: null, vatLabel: 'VAT · 7.5%' })
    const nan = draftLine({ desc: 'x', qty: NaN, price: 1, tax_category: 'STANDARD_VAT', tax_percent: '7.5' })
    expect(draftTotals([taxed(100), nan])).toEqual({ subtotal: null, vat: null, total: null, vatLabel: 'VAT' })
  })

  it('draftTotals_categorisedVatEqualsTheSumOfTheWireLineTaxes', () => {
    const draft: Draft = { ...baseDraft, items: [taxed(0.1), taxed(0.1), taxed(0.1), taxed(33.33, 'ZERO_VAT', '0'), draftLine({ desc: 'x', qty: 2, price: 7.77 })] }

    const wire = draftToCreateRequest(draft, baseEntity)
    const lineTaxKobo = wire.line_items.reduce((sum, l) => sum + BigInt((l.line_tax ?? '0').replace('.', '')), 0n)

    expect(draftTotals(draft.items)).toEqual({ subtotal: wire.subtotal, vat: wire.vat, total: wire.total, vatLabel: 'VAT' })
    expect(wire.vat).toBe('0.02')
    expect(lineTaxKobo).toBe(2n)
    expect(wire.total).toBe('49.19')
  })

  it('draftTotals_largeCategorisedTotalsStayExact', () => {
    const totals = draftTotals(Array.from({ length: 10 }, () => taxed(12345678901234.56)))

    expect(totals).toEqual({ subtotal: '123456789012345.60', vat: '9259259175925.92', total: '132716048188271.52', vatLabel: 'VAT' })
  })

  it('draftTotals_flatRateRoundsTheDeclaredSubtotalHalfUp', () => {
    // Exact 0.20 x 0.075 = 0.015 -> 0.02; 0.60 -> 0.045 -> 0.05; negative ties go away from zero.
    expect(draftTotals([draftLine({ desc: 'x', qty: 1, price: 0.2 })]).vat).toBe('0.02')
    expect(draftTotals([draftLine({ desc: 'x', qty: 1, price: 0.6 })]).vat).toBe('0.05')
    expect(draftTotals([draftLine({ desc: 'x', qty: 1, price: -0.2 })]).vat).toBe('-0.02')
  })
})

describe('draftToCreateRequest: NRS keys (AC-4)', () => {
  const HEADER_KEYS = [
    'invoice_kind', 'tax_currency_code', 'due_date', 'issue_time', 'tax_point_date', 'payment_status',
    'supplier_email', 'supplier_telephone', 'supplier_street', 'supplier_city', 'supplier_postal_zone', 'supplier_country',
    'supplier_state', 'supplier_lga', 'buyer_email', 'buyer_telephone', 'buyer_street', 'buyer_city',
    'buyer_postal_zone', 'buyer_country', 'buyer_state', 'buyer_lga',
  ]
  const LEGACY_KEYS = [
    'entity_id', 'invoice_number', 'issue_date', 'supplier_tin', 'supplier_name', 'buyer_tin', 'buyer_name',
    'currency', 'subtotal', 'vat', 'total', 'line_items',
  ]
  const LINE_KEYS = [
    'description', 'quantity', 'unit_price', 'line_total', 'line_tax', 'tax_category', 'hsn_code', 'isic_code',
    'product_category', 'service_category', 'sellers_item_identification', 'price_unit', 'tax_percent', 'base_quantity',
  ]

  it('draftToCreateRequest_sendsTheNrsHeaderKeys', () => {
    const draft: Draft = { ...baseDraft, due_date: '2027-01-31', issue_time: '09:05:00', buyer_email: '' }

    const result = draftToCreateRequest(draft, baseEntity) as unknown as Record<string, unknown>

    expect(Object.keys(result).sort()).toEqual([...LEGACY_KEYS, ...HEADER_KEYS].sort())
    expect(result.due_date).toBe('2027-01-31T00:00:00Z')
    expect(result.issue_time).toBe('09:05:00')
    expect(result.buyer_email).toBeNull()
    expect(result.tax_point_date).toBeNull()
    expect(result.tax_currency_code).toBe('NGN')
  })

  it('draftToCreateRequest_sendsEveryTypedHeaderValueAndNullsTheRest', () => {
    const typed = Object.fromEntries(HEADER_KEYS.map((k) => [k, `v-${k}`]))
    const draft: Draft = { ...baseDraft, ...typed, due_date: '2027-01-31', tax_point_date: '2027-01-15', buyer_lga: '' }

    const result = draftToCreateRequest(draft, baseEntity) as unknown as Record<string, unknown>

    for (const k of HEADER_KEYS) {
      if (k === 'due_date') expect(result[k]).toBe('2027-01-31T00:00:00Z')
      else if (k === 'tax_point_date') expect(result[k]).toBe('2027-01-15T00:00:00Z')
      else if (k === 'buyer_lga') expect(result[k]).toBeNull()
      else expect(result[k], k).toBe(`v-${k}`)
    }
  })

  it('draftToCreateRequest_eachHeaderKeyCarriesItsOwnDraftValueAndNullsWhenBlank', () => {
    const typed = Object.fromEntries(HEADER_KEYS.map((k) => [k, k.endsWith('_date') ? '2027-01-31' : `v-${k}`]))
    const wireOf = (draft: Draft) => draftToCreateRequest(draft, baseEntity) as unknown as Record<string, unknown>

    expect(HEADER_KEYS).toHaveLength(22)
    for (const blank of HEADER_KEYS) {
      const result = wireOf({ ...baseDraft, ...typed, [blank]: '' })
      for (const k of HEADER_KEYS) {
        if (k === blank) expect(result[k], `${k} blank`).toBeNull()
        else expect(result[k], `${k} while ${blank} is blank`).toBe(k.endsWith('_date') ? '2027-01-31T00:00:00Z' : `v-${k}`)
      }
    }
  })

  it('draftToCreateRequest_eachLineKeyCarriesItsOwnValueAndNullsWhenBlank', () => {
    const NRS = ['tax_category', 'hsn_code', 'isic_code', 'product_category', 'service_category', 'sellers_item_identification', 'price_unit', 'tax_percent'] as const
    const typed = Object.fromEntries(NRS.map((k) => [k, k === 'tax_percent' ? '7.5' : `v-${k}`]))
    const lineOf = (over: Partial<Record<(typeof NRS)[number], string>>) =>
      draftToCreateRequest({ ...baseDraft, items: [draftLine({ desc: 'x', qty: 1, price: 100, ...typed, ...over })] }, baseEntity).line_items[0]! as unknown as Record<string, unknown>

    expect(NRS).toHaveLength(8)
    for (const blank of NRS) {
      const line = lineOf({ [blank]: '' })
      for (const k of NRS) {
        if (k === blank) expect(line[k], `${k} blank`).toBeNull()
        else expect(line[k], `${k} while ${blank} is blank`).toBe(typed[k])
      }
    }
  })

  it('draftToCreateRequest_sendsTheNrsLineKeys', () => {
    const draft: Draft = {
      ...baseDraft,
      items: [
        draftLine({ desc: 'Widget', qty: 2, price: 100, tax_category: 'STANDARD_VAT', tax_percent: '7.5', hsn_code: '0101.21' }),
        draftLine({ desc: 'Plain', qty: 1, price: 50 }),
      ],
    }

    const result = draftToCreateRequest(draft, baseEntity)
    const [first, second] = result.line_items

    expect(result.line_items).toHaveLength(2)
    expect(Object.keys(first!).sort()).toEqual([...LINE_KEYS].sort())
    expect(first).toMatchObject({
      line_total: '200.00',
      line_tax: '15.00',
      tax_category: 'STANDARD_VAT',
      tax_percent: '7.5',
      hsn_code: '0101.21',
      isic_code: null,
      product_category: null,
      service_category: null,
      sellers_item_identification: null,
      price_unit: null,
      base_quantity: '1',
    })
    expect(Object.keys(second!).sort()).toEqual([...LINE_KEYS].sort())
    expect(second).toMatchObject({ line_tax: null, tax_category: null, tax_percent: null, hsn_code: null, base_quantity: '1' })
  })

  it('draftToCreateRequest_lineTaxIsTheGroupsLargestRemainderSplit', () => {
    const draft: Draft = { ...baseDraft, items: [taxed(0.1), taxed(0.1), taxed(0.1)] }

    const result = draftToCreateRequest(draft, baseEntity)

    expect(result.line_items.map((l) => l.line_tax)).toEqual(['0.01', '0.01', '0.00'])
    expect(result.subtotal).toBe('0.30')
    expect(result.vat).toBe('0.02')
    expect(result.total).toBe('0.32')
  })

  it('draftToCreateRequest_lineTotalsRoundHalfUpAndStayExact', () => {
    const lineTotal = (price: number, qty = 1) =>
      draftToCreateRequest({ ...baseDraft, items: [draftLine({ desc: 'x', qty, price })] }, baseEntity).line_items[0]!.line_total

    expect(lineTotal(0.005)).toBe('0.01')
    expect(lineTotal(0.004)).toBe('0.00')
    expect(lineTotal(-0.005)).toBe('-0.01')
    expect(lineTotal(1.005)).toBe('1.01')
    expect(lineTotal(0.1 + 0.2), '0.30000000000000004 as typed').toBe('0.30')
    expect(lineTotal(0.1, 3)).toBe('0.30')
    const sum = draftToCreateRequest({ ...baseDraft, items: [draftLine({ desc: 'a', qty: 1, price: 0.1 }), draftLine({ desc: 'b', qty: 1, price: 0.2 })] }, baseEntity)
    expect([sum.subtotal, sum.vat, sum.total]).toEqual(['0.30', '0.02', '0.32'])
  })

  it('draftToCreateRequest_aLineTaxIsNullWhenTheLineHasNoTotal', () => {
    const draft: Draft = { ...baseDraft, items: [draftLine({ desc: 'x', qty: NaN, price: 1, tax_category: 'STANDARD_VAT', tax_percent: '7.5' })] }

    const [line] = draftToCreateRequest(draft, baseEntity).line_items

    expect(line).toMatchObject({ line_total: null, line_tax: null, quantity: 'NaN', tax_category: 'STANDARD_VAT', tax_percent: '7.5' })
  })
})

describe('withSupplierDefaults (AC-5)', () => {
  it('withSupplierDefaults_fillsOnlyBlanks', () => {
    const draft: Draft = { ...baseDraft, supplier_email: 'typed', supplier_state: 'Lagos' }
    const entity = { email: 'co@x', street: 'S', city: null, state: 'Abia', telephone: '', country: 'NG' }

    const filled = withSupplierDefaults(draft, entity)

    expect(filled.supplier_email).toBe('typed')
    expect(filled.supplier_state).toBe('Lagos')
    expect(filled.supplier_street).toBe('S')
    expect(filled.supplier_country).toBe('NG')
    expect(filled.supplier_city).toBe('')
    expect(filled.supplier_telephone).toBe('')
    expect(filled.supplier_postal_zone).toBe('')
    expect(filled.supplier_lga).toBe('')
    expect(draft.supplier_street, 'the input draft is not mutated').toBe('')
    expect({ ...filled, supplier_street: '', supplier_country: '' }).toEqual(draft)
  })

  it('withSupplierDefaults_whitespaceIsContentNotBlank', () => {
    // Pinned: no trimming anywhere in the mapper, so a typed ' ' is kept and an entity ' ' is copied.
    const draft: Draft = { ...baseDraft, supplier_email: ' ', supplier_city: '' }

    const filled = withSupplierDefaults(draft, { email: 'co@x', city: '  ', street: '', telephone: undefined })

    expect(filled.supplier_email).toBe(' ')
    expect(filled.supplier_city).toBe('  ')
    expect(filled.supplier_street).toBe('')
    expect(filled.supplier_telephone).toBe('')
  })

  it('withSupplierDefaults_anEmptyEntityChangesNothingAndNeverTouchesOtherKeys', () => {
    const draft: Draft = { ...baseDraft, buyer_email: 'b@x', supplier_lga: 'L' }

    expect(withSupplierDefaults(draft, {})).toEqual(draft)
    expect(withSupplierDefaults(draft, { email: 'co@x', lga: 'Other' })).toEqual({ ...draft, supplier_email: 'co@x' })
  })
})

describe('applyBuyerRecord (AC-6)', () => {
  const BUYER_KEYS = [
    'buyer', 'invoice_kind', 'buyer_email', 'buyer_telephone', 'buyer_street', 'buyer_city',
    'buyer_postal_zone', 'buyer_country', 'buyer_state', 'buyer_lga',
  ] as const

  const record: Customer = {
    id: 'c-1',
    tin: '12345678-0001',
    name: 'Acme',
    contact_person: 'Ada',
    buyer_type: 'B2G',
    email: 'acme@x',
    telephone: '0800',
    street: '1 Main',
    city: 'Ikeja',
    postal_zone: '100001',
    country: 'NG',
    state: 'Lagos',
    lga: null,
    created_at: '2026-10-01T00:00:00Z',
  }

  it('applyBuyerRecord_setsTheTenBuyerKeys', () => {
    const draft: Draft = { ...baseDraft, buyer: 'Typed Ltd', buyerTin: '12345678-0001', buyer_street: 'typed street', buyer_lga: 'Old LGA' }

    const { draft: out, fill } = applyBuyerRecord(draft, record, {})

    expect(out.buyer).toBe('Acme')
    expect(out.invoice_kind).toBe('B2G')
    expect(out.buyer_email).toBe('acme@x')
    expect(out.buyer_telephone).toBe('0800')
    expect(out.buyer_street, 'a typed value is overwritten').toBe('1 Main')
    expect(out.buyer_city).toBe('Ikeja')
    expect(out.buyer_postal_zone).toBe('100001')
    expect(out.buyer_country).toBe('NG')
    expect(out.buyer_state).toBe('Lagos')
    expect(out.buyer_lga, 'a null field becomes ""').toBe('')
    expect(fill).toEqual({
      buyer: 'Acme', invoice_kind: 'B2G', buyer_email: 'acme@x', buyer_telephone: '0800', buyer_street: '1 Main',
      buyer_city: 'Ikeja', buyer_postal_zone: '100001', buyer_country: 'NG', buyer_state: 'Lagos', buyer_lga: '',
    })
    expect(out.buyerTin).toBe('12345678-0001')
    expect(out.number).toBe(draft.number)
    expect(Object.keys(out)).not.toContain('buyer_name')
  })

  it('applyBuyerRecord_noMatchClearsOnlyThePreviousFill', () => {
    const draft: Draft = { ...baseDraft, buyer: 'Acme', buyer_email: 'a@x', buyer_street: 'typed street', supplier_email: 'keep' }
    const lastFill = { buyer: 'Acme', buyer_email: 'a@x', buyer_street: 'Old St' }

    const { draft: out } = applyBuyerRecord(draft, null, lastFill)

    expect(out.buyer).toBe('')
    expect(out.buyer_email).toBe('')
    expect(out.buyer_street, 'a value the user changed is kept').toBe('typed street')
    expect(out.buyerTin).toBe(baseDraft.buyerTin)
    expect(out.supplier_email).toBe('keep')
    for (const k of BUYER_KEYS) if (k !== 'buyer' && k !== 'buyer_email' && k !== 'buyer_street') expect(out[k], k).toBe(draft[k])
  })

  it('applyBuyerRecord_aNewRecordReplacesTheLastFillAndTheReturnedFillClearsIt', () => {
    const other: Customer = { ...record, id: 'c-2', name: 'Beta', buyer_type: 'B2B', email: null, state: 'Abia' }
    const first = applyBuyerRecord({ ...baseDraft, buyerTin: 'typed-tin' }, record, {})
    const second = applyBuyerRecord(first.draft, other, first.fill)
    const cleared = applyBuyerRecord(second.draft, null, second.fill)

    expect(second.draft).toMatchObject({ buyer: 'Beta', invoice_kind: 'B2B', buyer_email: '', buyer_state: 'Abia', buyer_street: '1 Main' })
    expect(second.draft.buyerTin, 'the typed TIN is not replaced by the record TIN').toBe('typed-tin')
    expect(cleared.draft).toEqual({ ...baseDraft, buyerTin: 'typed-tin', buyer: '', invoice_kind: '', buyer_street: '', buyer_city: '', buyer_postal_zone: '', buyer_country: '', buyer_state: '', buyer_telephone: '', buyer_email: '', buyer_lga: '' })
    expect(cleared.fill, 'a second no-match has nothing left to clear').toEqual({})
    expect(applyBuyerRecord({ ...cleared.draft, buyer: 'Typed' }, null, cleared.fill).draft.buyer).toBe('Typed')
  })

  it('applyBuyerRecord_noMatchWithNoLastFillChangesNothing', () => {
    const draft: Draft = { ...baseDraft, buyer: 'Typed', buyer_email: 'a@x', invoice_kind: 'B2C' }

    const { draft: out, fill } = applyBuyerRecord(draft, null, {})

    expect(out).toEqual(draft)
    expect(out).not.toBe(draft)
    expect(fill).toEqual({})
  })

  it('applyBuyerRecord_aFillOfBlankNeverClearsATypedValue', () => {
    // buyer_lga was filled as '': the user then typed 'Ikeja', which differs from the fill, so it stays.
    const filled = applyBuyerRecord(baseDraft, record, {})
    const typed: Draft = { ...filled.draft, buyer_lga: 'Ikeja' }

    expect(applyBuyerRecord(typed, null, filled.fill).draft.buyer_lga).toBe('Ikeja')
  })

  it('applyBuyerRecord_doesNotMutateItsInputs', () => {
    const draft: Draft = { ...baseDraft, buyer: 'Typed' }
    const lastFill = { buyer: 'Typed' }

    applyBuyerRecord(draft, record, lastFill)
    applyBuyerRecord(draft, null, lastFill)

    expect(draft.buyer).toBe('Typed')
    expect(lastFill).toEqual({ buyer: 'Typed' })
  })
})

describe('buyerRequirement (AC-7)', () => {
  it('buyerRequirement_byKind', () => {
    const NINE = ['buyerTin', 'buyer', 'buyer_email', 'buyer_street', 'buyer_city', 'buyer_postal_zone', 'buyer_country', 'buyer_state', 'buyer_lga']

    for (const kind of ['B2B', 'B2G']) {
      const req = buyerRequirement(kind)
      expect(req.level, kind).toBe('required')
      expect([...req.keys].sort(), kind).toEqual([...NINE].sort())
    }
    expect(buyerRequirement('B2C')).toEqual({ level: 'optional', keys: [] })
    expect(buyerRequirement('')).toEqual({ level: 'choose', keys: [] })
  })

  it('buyerRequirement_aKindOutsideTheThreeIsChoose', () => {
    // Pinned: the match is exact, so a stored 'b2b' or ' B2B' reads as not chosen.
    for (const kind of ['b2b', ' B2B', 'B2B ', 'B2X']) expect(buyerRequirement(kind), kind).toEqual({ level: 'choose', keys: [] })
  })
})
