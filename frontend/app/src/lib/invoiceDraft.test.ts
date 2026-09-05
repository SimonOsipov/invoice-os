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
import { describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import { draftToCreateRequest, fileDraftGate, fileDraftInvoice, type FileDraftDeps } from './invoiceDraft'
import { detailTarget, selectImported } from './importReport'
import type { Entity } from './portfolio'
import type { Draft } from '../types'

// buyerAddress/wht/docType left `Draft` in INVCR-01-03 (task-279) — the create flow was
// de-intersected from the mock verdict engine's `Validatable`, and none of the three has a
// column or a wire field. They are no longer expressible here at all.
const baseDraft: Draft = {
  number: 'INV-2026-00482',
  buyer: 'Beta Ltd',
  buyerTin: '00000000002',
  date: '2026-06-16',
  currency: 'NGN',
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

  it('DRAFT-9 [verbatim-typed-derived-rounded]: quantity/unit_price beyond 2dp cross verbatim, unlike the rounded derived fields', () => {
    // qty is numeric(14,3) and unit_price numeric(14,2), but the TYPED wire values are
    // not rounded to their column scale the way subtotal/vat/total/line_total are --
    // round2() must never touch item.qty/item.price. Both values below carry 3dp so a
    // round2-mutated implementation is forced to visibly diverge: half-up round2 would
    // turn quantity '2.567' into '2.57' and unit_price '1000.005' into '1000.01'.
    const draft: Draft = {
      ...baseDraft,
      items: [{ desc: 'x', qty: 2.567, price: 1000.005 }],
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
        { desc: 'a', qty: 2.5, price: 1500.25 },
        { desc: 'b', qty: 2.5, price: 1500.25 },
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
        { desc: 'valid line', qty: 2, price: 100 },
        { desc: 'corrupt line', qty: NaN, price: 50 },
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
    // match createRequest's declaration order (handlers.go:51-64). No other DRAFT-*
    // spec pins this: they all read named properties, which is order-independent.
    const draft: Draft = {
      ...baseDraft,
      items: [{ desc: 'a', qty: 2, price: 1500.25 }],
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
    ])
    expect(Object.keys(result.line_items[0])).toEqual([
      'description',
      'quantity',
      'unit_price',
      'line_total',
      'line_tax',
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
    expect(detailTarget(selectImported('u-1'))).toEqual({ kind: 'imported', invoiceId: 'u-1' })
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

// RED specs (EXTR-15-06, task-832, Mode A) -- SD-8 / AC-8.
//
// draftToCreateRequest takes two arguments today; the recorded document id arrives as a
// THIRD, optional one, so every shipped call site and every DRAFT-* fixture above keeps
// compiling. The cast below is what lets `pnpm typecheck` stay green while the assertions
// fail honestly -- DELETE IT once the signature widens.
//
// `Draft` was deliberately narrowed (types.ts:79-81) to fields that have a column and a wire
// key; a document id is recorded by the hand-off, not typed by the operator, so it does not
// belong on that type. If EXTR-15-07 records it on `Draft` instead, this spec is what has to
// change -- the decision is stated here rather than assumed.
const draftToCreateRequestWithDocument = draftToCreateRequest as unknown as (
  draft: Draft,
  entity: Pick<Entity, 'id' | 'name' | 'tin'>,
  sourceDocumentId?: string,
) => Record<string, unknown>

const SD8_DOCUMENT_ID = '7f1c2a90-1c4b-4f0e-9f2a-2c0f5f3a11bd'

describe('draftToCreateRequest: source_document_id (EXTR-15-06)', () => {
  it('SD-8 a recorded document id crosses the wire; none omits the key entirely', () => {
    const withDocument = draftToCreateRequestWithDocument(baseDraft, baseEntity, SD8_DOCUMENT_ID)
    expect(withDocument.source_document_id).toBe(SD8_DOCUMENT_ID)

    // Absent, not null: JSON.stringify drops `undefined`, so `in` is the only honest oracle
    // for "the key never crossed".
    const withoutDocument = draftToCreateRequestWithDocument(baseDraft, baseEntity)
    expect('source_document_id' in withoutDocument).toBe(false)

    // Control: the mapper really ran in the second call. Without it the `in` check above
    // would also pass on an empty object.
    expect(withoutDocument.entity_id).toBe(baseEntity.id)
    expect(withoutDocument.invoice_number).toBe(baseDraft.number)
  })

  it('SD-8 the key takes the LAST wire position, as invoiceDraft.ts:119-121 requires', () => {
    // The header note pins the return literal to createRequest's declaration order, and an
    // additive wire field is appended (handlers.go:235-240 states that convention for
    // getResponse). DRAFT-10 above still pins the 12-key order when no document was recorded.
    const result = draftToCreateRequestWithDocument(baseDraft, baseEntity, SD8_DOCUMENT_ID)

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
      'source_document_id',
    ])
  })
})

