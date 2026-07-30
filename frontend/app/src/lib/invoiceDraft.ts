// Pure wizard Draft -> POST /v1/invoices wire mapper (INVCR-01-02, task-278). Covered by
// invoiceDraft.test.ts (DRAFT-1..DRAFT-8). See task-278's implementation plan for the
// full architecture decision record; key points summarized here for the next reader:
//
// - `Entity`, never `Client`: Client.tin is LOSSY (buildClientForEntity does
//   `tin: e.tin ?? '—'`, lib/clients.ts:146), so a null entity.tin -- and AC-5's
//   `supplier_tin: null` rule -- is unrepresentable through Client. `Pick<…>`, not the
//   whole Entity, mirroring computedLineSum's own convention (invoices.ts:524-525).
// - `entity.id: string` (not nullable) makes an in-house (entityId===null) invoice
//   unrepresentable at the TYPE level, so this mapper stays total -- no null-entity
//   branch, no error return. THE CALLER GATES, precedent App.tsx:403 (`if (base == null
//   || !importFile || !entityId || …) return` before createImport). §14 puts in-house
//   filing out of scope; subtask 05 owns the honest refusal panel.
// - `entity.tin` crosses the wire VERBATIM, never re-hyphenated/repaired -- the C7
//   residual risk (QA Debate Log finding C7, story description): an entity whose TIN was
//   canonicalized to 12 bare digits by portfolio.ValidateTIN will false-positive
//   supplier-tin-format on every manual invoice. The fix is server-side
//   (task-293/INVCR-01-17), NOT here. DRAFT-3 pins the unrepaired pass-through
//   deliberately -- if it ever fails, that is a decision about C7, not a bug.
// - `quantity`/`unit_price` cross the wire VERBATIM (typed, user-entered values);
//   `subtotal`/`vat`/`total`/`line_total` are round2 half-up (DERIVED values) -- see GAP
//   2 in task-278's plan: PG stores these as numeric(14,2)/numeric(14,3) and rounds
//   anyway, so rounding client-side makes the wire body equal the row that gets stored
//   and judged. Rounding a user-typed price instead would be a client-side verdict on
//   money, forbidden by [server-truth].
// - `issue_date` crosses as RFC3339 (`` `${draft.date}T00:00:00Z` ``), never the bare
//   'YYYY-MM-DD' draft.date carries -- Go's `*time.Time` 400s on a bare date (GAP 1,
//   verified: `"2026-06-16"` -> `cannot parse "" as "T"` -> 400 `invalid request body`).
//   A blank draft.date maps to `issue_date: null`.
// - buyerAddress/wht/docType have no column and no wire field -- never emitted.
// - The money algorithm is the exact-decimal kit exported from ./invoices
//   (parseScaled/mulScaled/addScaled/renderScaled, [money-primitives-exported]) plus
//   round2() below -- bigint throughout, no float ever touches a money value. DRAFT-1's
//   oracle (3 x 1000.005 -> subtotal '3000.02') admits exactly ONE algorithm: naive float
//   `(3*1000.005).toFixed(2)` gives '3000.01' and exact-without-rounding gives
//   '3000.015'. `subtotal` is round2 of the EXACT sum of the products, not the sum of the
//   already-rounded line_totals, so both derive from the same products; `vat` is round2 of
//   the DECLARED (already-rounded) subtotal x 0.075, and `total` their sum. Do NOT reuse
//   computedLineSum -- its absent-quantity-weights-1 rule and no-round render belong to
//   the edit-form hint, not this mapper.
// - Two edges handled per task-278's plan rather than spec'd: an empty `items` array emits
//   `line_items: []` with subtotal/vat/total all `null` -- never '0.00', mirroring
//   computedLineSum's own stated rule; a non-finite qty/price (Number('abc') -> NaN via
//   App.tsx's updateItem) crosses the wire as its raw String() and makes that line's
//   line_total -- and every total -- null, mirroring payload.go's raw-string fallback.
//   INVCR-01-03 gave CreateForm working add/remove-line controls, so the NaN edge is now
//   reachable; the empty-items edge deliberately is not -- the remove control is disabled
//   at one remaining line, so the form cannot file a lineless invoice.
import { toApiError, type ApiError } from '@invoice-os/api-client'

import {
  addScaled,
  mulScaled,
  parseScaled,
  renderScaled,
  type InvoiceCreateInput,
  type LineItemCreateInput,
  type Scaled,
} from './invoices'
import type { Draft, LineItem } from '../types'
import type { Entity } from './portfolio'

// 0.075 exactly, as a Scaled literal (75 x 10^-3) rather than parseScaled('0.075') so a
// constant needs no null branch. Same rate the server's own vat-standard-rate rule judges
// against (rate 0.075, tolerance 0.005, migrations/20260711121327_seed_mbs_v1.sql:29) --
// [declared-money-is-the-forms]: this is the form's own declaration, which the server then
// judges; agreeing with the rule is correct, not a bypass.
const VAT_RATE: Scaled = { u: 75n, s: 3 }

// Half-up, magnitude away from zero, to exactly 2dp -- the scale every column the four
// DERIVED fields land in is declared at (numeric(14,2), 20260714103137_invoices.sql:57-59
// / 20260714105151_line_items.sql:51-54), so the wire body equals the row that gets stored
// and judged. A value already at or below 2dp is returned untouched; renderScaled pads it.
function round2(value: Scaled): Scaled {
  if (value.s <= 2) return value
  const divisor = 10n ** BigInt(value.s - 2)
  const negative = value.u < 0n
  const magnitude = negative ? -value.u : value.u
  const truncated = magnitude / divisor
  const rounded = (magnitude % divisor) * 2n >= divisor ? truncated + 1n : truncated
  return { u: negative ? -rounded : rounded, s: 2 }
}

// '' is the ABSENT value, never content: a React input holds '', never null, so a field
// the operator left alone arrives here empty and must cross the wire as `null` (AC-2).
// No trimming -- the backend does not trim either (canonField, invoices.ts), and rewriting
// an operator's content is not this mapper's job.
function nullIfBlank(value: string): string | null {
  return value === '' ? null : value
}

// quantity x unit_price, EXACT. `null` when either side is non-numeric (String(NaN) ->
// 'NaN', which parseScaled refuses), which propagates to that line's line_total and to
// every total.
function lineAmount(item: Pick<LineItem, 'qty' | 'price'>): Scaled | null {
  const quantity = parseScaled(String(item.qty))
  const unitPrice = parseScaled(String(item.price))
  if (quantity === null || unitPrice === null) return null
  return mulScaled(quantity, unitPrice)
}

// Exact Σ of the line amounts, unrounded. `null` -- never a zero -- on an empty line set
// or any unparseable line.
function sumAmounts(amounts: ReadonlyArray<Scaled | null>): Scaled | null {
  if (amounts.length === 0) return null
  let sum: Scaled = { u: 0n, s: 0 }
  for (const amount of amounts) {
    if (amount === null) return null
    sum = addScaled(sum, amount)
  }
  return sum
}

// Key order below is the WIRE order -- apiFetch JSON.stringifies the body verbatim, so
// object-literal insertion order is what crosses the network. It follows createRequest's
// own field order (handlers.go:51-64) and InvoiceCreateInput's declaration order.
export function draftToCreateRequest(
  draft: Draft,
  entity: Pick<Entity, 'id' | 'name' | 'tin'>,
): InvoiceCreateInput {
  const amounts = draft.items.map(lineAmount)
  const lineItems: LineItemCreateInput[] = draft.items.map((item, index) => {
    const amount = amounts[index]
    return {
      description: nullIfBlank(item.desc),
      // VERBATIM, never padded or rounded: these are the values the operator typed.
      quantity: String(item.qty),
      unit_price: String(item.price),
      line_total: amount == null ? null : renderScaled(round2(amount)),
      line_tax: null,
    }
  })

  const exactSubtotal = sumAmounts(amounts)
  const subtotal = exactSubtotal === null ? null : round2(exactSubtotal)
  const vat = subtotal === null ? null : round2(mulScaled(subtotal, VAT_RATE))
  const total = subtotal === null || vat === null ? null : round2(addScaled(subtotal, vat))

  return {
    entity_id: entity.id,
    invoice_number: draft.number,
    // RFC3339, never the bare 'YYYY-MM-DD' draft.date carries -- see the header (GAP 1).
    issue_date: draft.date === '' ? null : `${draft.date}T00:00:00Z`,
    // VERBATIM, unrepaired -- C7. See the header.
    supplier_tin: entity.tin,
    supplier_name: entity.name,
    buyer_tin: nullIfBlank(draft.buyerTin),
    buyer_name: nullIfBlank(draft.buyer),
    currency: nullIfBlank(draft.currency),
    subtotal: subtotal === null ? null : renderScaled(subtotal),
    vat: vat === null ? null : renderScaled(vat),
    total: total === null ? null : renderScaled(total),
    line_items: lineItems,
  }
}

// The manual form's one round trip, extracted out of the Workspace component because
// vitest runs environment:'node' with no jsdom and no Testing Library -- a handler living
// inside Workspace would have no oracle at all. Same "node-testable without jsdom"
// convention lib/importFlow.ts states for its own gates.
//
// `create` is typed `Promise<{id:string}>`, the structural minimum, so a spec fixture
// needn't build a 20-field InvoiceRecord (createInvoice's real InvoiceRecord return is
// structurally assignable to it).
export type FileDraftDeps = {
  create: (input: InvoiceCreateInput) => Promise<{ id: string }>
  inFlight: { current: boolean }
  onPending: (pending: boolean) => void
  onError: (error: ApiError | null) => void
  onCreated: (invoiceId: string) => void
}

// BODY ORDER IS THE CONTRACT (SUBMIT-1/2/3, task-279 plan §3) -- do not reorder:
//
//   1. the re-entrancy check and 2. the flag write happen SYNCHRONOUSLY, before the first
//      `await`: React batches state updates, so a fast double-click fires this handler
//      again before a `disabled` prop can re-render, and createRequest carries NO
//      idempotency key (that field is batch-submit-only), so two POSTs are two persisted,
//      separately-submittable invoices. A `filing` state flag CANNOT close this -- only a
//      ref read-and-written in the same synchronous tick can (SUBMIT-3).
//   3. onError(null) then 4. onPending(true) are the only observations that may land
//      before the response.
//   5. onCreated fires ONLY in the resolve branch. There is deliberately no `setStep` or
//      `setVerdict` dep for this function to affirm success with early: navigation to the
//      real invoice detail is the ONLY success surface the create flow has, so an
//      optimistic affirmation is unrepresentable rather than merely untested (Core AC 1).
//      SUBMIT-1 proves the ordering by asserting the observation log WHILE create()'s
//      promise is still open.
//
// NEVER REJECTS: every failure lands on onError as an ApiError and the returned promise
// still resolves (SUBMIT-2). The caller does `void fileDraftInvoice(...)`, so a rejection
// would surface as an unhandled promise rejection and the user would see nothing at all.
export async function fileDraftInvoice(
  draft: Draft,
  entity: Pick<Entity, 'id' | 'name' | 'tin'>,
  deps: FileDraftDeps,
): Promise<void> {
  if (deps.inFlight.current) return
  deps.inFlight.current = true
  deps.onError(null)
  deps.onPending(true)
  try {
    const created = await deps.create(draftToCreateRequest(draft, entity))
    deps.onCreated(created.id)
  } catch (err: unknown) {
    // Raw, unmapped: ApiError.message already carries the gateway's own {"error":…}
    // verbatim, so there is no status->copy table to drift out of date.
    deps.onError(toApiError(err))
  } finally {
    deps.inFlight.current = false
    deps.onPending(false)
  }
}

// Precedence: entity first (unresolvable in-app -- no picker on this screen), invoice
// number second (resolvable -- the field is editable) -- mirrors CreateMapping.tsx:
// 98-111's `!canFile -> !invNumMapped` ordering. 'Filing needs a linked entity' and
// 'Invoice number is required' are the exact copy CreateForm renders (task-279 plan
// §8.5, §2); the first is byte-identical to CreateMapping's own refusal, reused rather
// than re-authored -- mapping and form are different steps, so the two never both mount.
//
// Gates on the RESOLVED Entity, never on a Client.entityId: `active` is rebuilt from the
// live entity list by an effect, so there is a window where the id is non-null but not yet
// present in the fetched list, and gating on the id there renders an armed button that
// swallows the click. `null` covers all three honest refusals at once -- in-house (no
// business_entities row at all), the emptyClient() placeholder, and the no-gateway build.
//
// The blank-number branch is reachable only because this subtask made the field editable;
// it is the client-side half of the server's own 400 `invoice_number is required`. NO
// trim: '' is the mapper's absent sentinel and neither layer rewrites an operator's
// content ([no-trim-at-either-layer]).
export function fileDraftGate(
  draft: Draft,
  entity: Pick<Entity, 'id' | 'name' | 'tin'> | null,
): { canFile: true } | { canFile: false; reason: string } {
  if (entity === null) return { canFile: false, reason: 'Filing needs a linked entity' }
  if (draft.number === '') return { canFile: false, reason: 'Invoice number is required' }
  return { canFile: true }
}
