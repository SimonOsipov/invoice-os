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
//
// STUB (Mode A, RED-first): throws -- the executor implements the body in Stage 3, using
// the exported money kit (Scaled/parseScaled/mulScaled/addScaled/renderScaled) from
// ./invoices plus a new round2() half-up helper here ([money-primitives-exported]). Do
// NOT reuse computedLineSum -- its absent-quantity-weights-1 rule and no-round render
// belong to the edit-form hint, not this mapper.
import type { Draft } from '../types'
import type { Entity } from './portfolio'
import type { InvoiceCreateInput } from './invoices'

export function draftToCreateRequest(
  _draft: Draft,
  _entity: Pick<Entity, 'id' | 'name' | 'tin'>,
): InvoiceCreateInput {
  throw new Error('not implemented')
}
