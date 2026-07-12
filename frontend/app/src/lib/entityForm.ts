// App-side entity form helpers (M3-08-02, task-57). STUB — the executor implements the
// bodies next; every export below throws so the RED specs in entityForm.test.ts (F1-F12)
// fail on a thrown/assertion mismatch, not an import or type error. These are pure (no
// React) functions consumed by EntityFormModal.tsx (M3-08-05) — no fetch/global mocking
// needed here, unlike portfolio.test.ts / authedFetch.test.ts.
//
// Contract (Obsidian M3-08 story, §6 System Design + [A-k]):
// - `emptyEntityForm()` — all-'' form state (name/tin/registration/sector/address).
// - `entityFormFrom(entity)` — live Entity -> form state; null optionals -> '' (inputs
//   stay controlled).
// - `validateEntityForm(state)` — presence-only (name + TIN required); server owns TIN
//   grammar/duplicate checks (surfaced via mapSubmitError from 400/409, not
//   re-implemented here, [A-g]). Returns `{valid, errors: {name?, tin?}}`.
// - `toEntityInput(state)` — CREATE path: trim every field; omit an empty optional
//   (registration/sector/address) entirely (the backend reads an absent field as
//   "unset on create").
// - `toEntityUpdateInput(original, state)` — EDIT path ([A-k], F6 fix-now, GATE verified
//   against the backend: a PATCH pointer-to-"" persists and clears — portfolio.go /
//   store.go:185-194): diff the trimmed `state` against `original` (null optionals on
//   `original` normalized to '' before comparing) and include a field IFF its trimmed
//   value differs from the normalized original — this sends an explicit `''` for a
//   CLEARED optional (so the backend clears it) while OMITTING an untouched field (so
//   the backend leaves it alone). An all-unchanged form yields `{}` (caller skips the
//   PATCH — avoids the backend's all-nil 400, portfolio.go:305-308).
// - `mapSubmitError(err)` — submit-time ApiError -> user-facing message. Per §6's submit
//   orchestration: 400 (invalid TIN) and 409 (duplicate TIN) both attach to the TIN
//   field (`{field:'tin', message}`); 401 -> null (the authedFetch seam has already
//   called signOut — the surface is unmounting to <SignIn>, no inline error needed);
//   network/500 -> a form-level message (`{message}`, no `field`).
//
// `ApiError` (as a runtime value) is referenced only by the real implementation (next,
// not this stub) — importing it unused here would fail noUnusedLocals under this app's
// strict tsconfig (mirrors portfolio.ts's / authedFetch.ts's stub rationale). Only the
// type-only imports below are referenced by this stub's signatures.
import type { ApiError } from '@invoice-os/api-client'
import type { Entity, EntityInput } from './portfolio'

export interface EntityFormState {
  name: string
  tin: string
  registration: string
  sector: string
  address: string
}

export interface EntityFormErrors {
  name?: string
  tin?: string
}

export interface EntityFormValidation {
  valid: boolean
  errors: EntityFormErrors
}

// `field` is present ('tin') for a field-anchored message (400/409); absent for a
// form-level message (network/500). `mapSubmitError` returns `null` for 401 (no inline
// error — the seam is already clearing the session).
export interface SubmitError {
  field?: 'tin'
  message: string
}

export function emptyEntityForm(): EntityFormState {
  throw new Error('not implemented')
}

export function entityFormFrom(_entity: Entity): EntityFormState {
  throw new Error('not implemented')
}

export function validateEntityForm(_state: EntityFormState): EntityFormValidation {
  throw new Error('not implemented')
}

export function toEntityInput(_state: EntityFormState): EntityInput {
  throw new Error('not implemented')
}

export function toEntityUpdateInput(_original: Entity, _state: EntityFormState): Partial<EntityInput> {
  throw new Error('not implemented')
}

export function mapSubmitError(_err: ApiError): SubmitError | null {
  throw new Error('not implemented')
}
