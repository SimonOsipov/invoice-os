// RED specs (M3-08-02, task-57, F1-F12) — pin the entity form validation contract, the
// create/edit input mappers (incl. the diff-based toEntityUpdateInput, [A-k]), and the
// submit-error mapper before the executor implements the bodies in entityForm.ts.
//
// Every spec below currently fails because entityForm.ts's stub bodies throw `new
// Error('not implemented')` before returning anything — that IS the correct RED reason
// (assertion / not-implemented), not an import/compile/setup error. These are pure (no
// React, no fetch) functions — no vi.stubGlobal/mocking needed here, unlike
// portfolio.test.ts / authedFetch.test.ts. F12 calls entityFormFrom(original) to build
// its "unchanged" state per the spec's literal wording ("state deep-equals
// entityFormFrom(original)"); F9-F11 hand-construct `state` per their own Given columns
// so each test stays independent of entityFormFrom's (also-stubbed) behavior.
import { describe, expect, it } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import {
  emptyEntityForm,
  entityFormFrom,
  mapSubmitError,
  toEntityInput,
  toEntityUpdateInput,
  validateEntityForm,
  type EntityFormState,
} from './entityForm'
import type { Entity } from './portfolio'

function baseEntity(overrides: Partial<Entity> = {}): Entity {
  return {
    id: 'e1',
    name: 'Acme',
    tin: '0000000000',
    registration: null,
    sector: null,
    address: null,
    status: 'active',
    created_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

describe('validateEntityForm', () => {
  it('F1: an empty form is invalid with both name and tin errors set', () => {
    const result = validateEntityForm(emptyEntityForm())

    expect(result.valid).toBe(false)
    expect(result.errors.name).toBeTruthy()
    expect(result.errors.tin).toBeTruthy()
  })

  it('F2: name + tin both present is valid with no field errors', () => {
    const result = validateEntityForm({ ...emptyEntityForm(), name: 'Acme', tin: '0000000000' })

    expect(result.valid).toBe(true)
    expect(result.errors).toEqual({})
  })
})

describe('toEntityInput (create path)', () => {
  it('F3: trims all fields and omits empty optionals (registration/sector/address)', () => {
    const result = toEntityInput({
      name: '  Acme ',
      tin: ' 123 ',
      registration: '',
      sector: '',
      address: '',
    })

    expect(result).toEqual({ name: 'Acme', tin: '123' })
    expect(result).not.toHaveProperty('registration')
    expect(result).not.toHaveProperty('sector')
    expect(result).not.toHaveProperty('address')
  })
})

describe('entityFormFrom', () => {
  it('F4: maps a live Entity with all-null optionals to an all-string form state (never null/undefined)', () => {
    const entity = baseEntity({ tin: null, registration: null, sector: null, address: null })

    const result = entityFormFrom(entity)

    expect(result.name).toBe('Acme')
    expect(result.tin).toBe('')
    expect(result.registration).toBe('')
    expect(result.sector).toBe('')
    expect(result.address).toBe('')
    for (const value of Object.values(result)) {
      expect(typeof value).toBe('string')
    }
  })
})

describe('mapSubmitError', () => {
  it('F5: 409 duplicate TIN maps to a TIN-field "already registered" message', () => {
    const err = new ApiError('http', 'duplicate tin', 409)

    const result = mapSubmitError(err)

    expect(result).not.toBeNull()
    expect(result?.field).toBe('tin')
    expect(result?.message).toMatch(/already registered/i)
  })

  it('F6: 400 validation error maps to a TIN-field message containing the server text', () => {
    const err = new ApiError('http', 'tin must be a valid 10 or 12 digit number', 400)

    const result = mapSubmitError(err)

    expect(result).not.toBeNull()
    expect(result?.field).toBe('tin')
    expect(result?.message).toContain('tin must be a valid 10 or 12 digit number')
  })

  it('F7: 401 maps to null (seam already clearing the session; no inline error)', () => {
    const err = new ApiError('http', 'unauthorized', 401)

    expect(mapSubmitError(err)).toBeNull()
  })

  it('F8: a network error maps to a form-level message (no field)', () => {
    const err = new ApiError('network', 'offline', null)

    const result = mapSubmitError(err)

    expect(result).not.toBeNull()
    expect(result?.field).toBeUndefined()
    expect(result?.message).toBeTruthy()
  })
})

describe('toEntityUpdateInput (edit path, diff-based, [A-k])', () => {
  it('F9: a cleared optional (sector "Freight" -> "") is sent as {sector:""}, nothing else', () => {
    const original = baseEntity({ name: 'Acme', tin: '0000000000', sector: 'Freight', registration: null, address: null })
    const state: EntityFormState = { name: 'Acme', tin: '0000000000', registration: '', sector: '', address: '' }

    const result = toEntityUpdateInput(original, state)

    expect(result).toEqual({ sector: '' })
  })

  it('F10: a changed required field (name) is the only key in the diff; unchanged tin is omitted', () => {
    const original = baseEntity({ name: 'Acme', tin: '0000000000', registration: null, sector: null, address: null })
    const state: EntityFormState = { name: 'AcmeCorp', tin: '0000000000', registration: '', sector: '', address: '' }

    const result = toEntityUpdateInput(original, state)

    expect(result).toEqual({ name: 'AcmeCorp' })
    expect(result).not.toHaveProperty('tin')
  })

  it('F11: a null optional (sector) and an untouched empty-string form value normalize equal — no spurious clear', () => {
    const original = baseEntity({ name: 'Acme', tin: '0000000000', sector: null, registration: null, address: null })
    const state: EntityFormState = { name: 'Acme', tin: '0000000000', registration: '', sector: '', address: '' }

    const result = toEntityUpdateInput(original, state)

    expect(result).toEqual({})
  })

  it('F12: an untouched form (state deep-equals entityFormFrom(original)) yields an empty diff — caller skips the PATCH', () => {
    const original = baseEntity({
      name: 'Acme',
      tin: '0000000000',
      sector: 'Freight',
      registration: 'RC123456',
      address: '12 Marina Rd, Lagos',
    })
    const state = entityFormFrom(original)

    const result = toEntityUpdateInput(original, state)

    expect(result).toEqual({})
  })
})
