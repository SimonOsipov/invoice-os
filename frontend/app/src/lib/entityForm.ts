// App-side entity form helpers (M3-08-02, task-57). These are pure (no React) functions
// consumed by EntityFormModal.tsx (M3-08-05) — no fetch/global mocking needed here,
// unlike portfolio.test.ts / authedFetch.test.ts.
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
// `mapSubmitError` only reads fields (`status`, `message`) off the `ApiError` instance
// the caller passes in — it never constructs one — so `ApiError` is imported type-only.
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
  return { name: '', tin: '', registration: '', sector: '', address: '' }
}

export function entityFormFrom(entity: Entity): EntityFormState {
  return {
    name: entity.name,
    tin: entity.tin ?? '',
    registration: entity.registration ?? '',
    sector: entity.sector ?? '',
    address: entity.address ?? '',
  }
}

export function validateEntityForm(state: EntityFormState): EntityFormValidation {
  const errors: EntityFormErrors = {}
  if (!state.name.trim()) errors.name = 'Name is required'
  if (!state.tin.trim()) errors.tin = 'TIN is required'
  return { valid: Object.keys(errors).length === 0, errors }
}

export function toEntityInput(state: EntityFormState): EntityInput {
  const input: EntityInput = { name: state.name.trim(), tin: state.tin.trim() }
  const registration = state.registration.trim()
  const sector = state.sector.trim()
  const address = state.address.trim()
  if (registration) input.registration = registration
  if (sector) input.sector = sector
  if (address) input.address = address
  return input
}

export function toEntityUpdateInput(original: Entity, state: EntityFormState): Partial<EntityInput> {
  const trimmed: EntityFormState = {
    name: state.name.trim(),
    tin: state.tin.trim(),
    registration: state.registration.trim(),
    sector: state.sector.trim(),
    address: state.address.trim(),
  }
  const normalized: EntityFormState = {
    name: original.name,
    tin: original.tin ?? '',
    registration: original.registration ?? '',
    sector: original.sector ?? '',
    address: original.address ?? '',
  }
  const diff: Partial<EntityInput> = {}
  if (trimmed.name !== normalized.name) diff.name = trimmed.name
  if (trimmed.tin !== normalized.tin) diff.tin = trimmed.tin
  if (trimmed.registration !== normalized.registration) diff.registration = trimmed.registration
  if (trimmed.sector !== normalized.sector) diff.sector = trimmed.sector
  if (trimmed.address !== normalized.address) diff.address = trimmed.address
  return diff
}

export function mapSubmitError(err: ApiError): SubmitError | null {
  if (err.status === 401) return null
  if (err.status === 409) return { field: 'tin', message: 'This TIN is already registered.' }
  if (err.status === 400) return { field: 'tin', message: err.message }
  return { message: 'Something went wrong. Please try again.' }
}
