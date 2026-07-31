// RED-then-GREEN spec (LAND-02-01, U1-U9) — pins validateDemoForm's consent branch,
// splitName's contract and firstNameOf's post-refactor behaviour, plus the
// TAXPAYER_SIZE_OPTIONS/DEFAULT_TAXPAYER_SIZE constants, before the implementation
// lands. validateDemoForm/firstNameOf/splitName are stubs that throw 'not
// implemented', so U1-U8 RED via the thrown error; TAXPAYER_SIZE_OPTIONS is a
// deliberately-wrong placeholder array, so U9 RED via a genuine assertion mismatch.
import { describe, expect, it } from 'vitest'

import {
  DEFAULT_TAXPAYER_SIZE,
  TAXPAYER_SIZE_OPTIONS,
  firstNameOf,
  splitName,
  validateDemoForm,
  type DemoFormValues,
} from './demoForm'

describe('validateDemoForm — consent', () => {
  it('U1: consent:false sets errors.consent even when name/email/company are valid', () => {
    const values: DemoFormValues = { name: 'Ada Okafor', email: 'ada@okafor.ng', company: 'Okafor & Partners', consent: false }
    const errors = validateDemoForm(values)
    expect(errors.consent).toBeTruthy()
    expect(errors.name).toBeUndefined()
    expect(errors.email).toBeUndefined()
    expect(errors.company).toBeUndefined()
  })

  it('U2: consent:true adds no spurious error alongside otherwise-valid fields', () => {
    const values: DemoFormValues = { name: 'Ada Okafor', email: 'ada@okafor.ng', company: 'Okafor & Partners', consent: true }
    expect(validateDemoForm(values)).toEqual({})
  })

  it('U3: an empty name and consent:false surface BOTH errors.name and errors.consent', () => {
    const values: DemoFormValues = { name: '', email: 'ada@okafor.ng', company: 'Okafor & Partners', consent: false }
    const errors = validateDemoForm(values)
    expect(errors.name).toBeTruthy()
    expect(errors.consent).toBeTruthy()
  })
})

describe('splitName', () => {
  it('U4: splits a simple two-token name', () => {
    expect(splitName('Ada Okafor')).toEqual({ firstName: 'Ada', lastName: 'Okafor' })
  })

  it('U5: trims, collapses internal whitespace runs, and preserves a multi-token surname', () => {
    expect(splitName('  Ngozi   Adaeze  Balogun ')).toEqual({ firstName: 'Ngozi', lastName: 'Adaeze Balogun' })
  })

  it('U6: a single-token name has an empty lastName', () => {
    expect(splitName('Ada')).toEqual({ firstName: 'Ada', lastName: '' })
  })

  it('U7: an empty or whitespace-only name yields empty firstName and lastName', () => {
    expect(splitName('')).toEqual({ firstName: '', lastName: '' })
    expect(splitName('   ')).toEqual({ firstName: '', lastName: '' })
  })
})

describe('firstNameOf', () => {
  it('U8: keeps its observable behaviour after the splitName refactor', () => {
    expect(firstNameOf('')).toBe('there')
    expect(firstNameOf('Ada Okafor')).toBe('Ada')
  })
})

describe('TAXPAYER_SIZE_OPTIONS / DEFAULT_TAXPAYER_SIZE', () => {
  it('U9: is exactly the four turnover bands in enforcement order, with no bare Micro/Small/Medium/Large, and a valid default', () => {
    expect(TAXPAYER_SIZE_OPTIONS).toEqual(['Large ₦5bn+', 'Medium ₦1bn–₦5bn', 'Small ₦50m–₦1bn', 'Below ₦50m'])
    expect(TAXPAYER_SIZE_OPTIONS).not.toContain('Micro')
    expect(TAXPAYER_SIZE_OPTIONS).not.toContain('Small')
    expect(TAXPAYER_SIZE_OPTIONS).not.toContain('Medium')
    expect(TAXPAYER_SIZE_OPTIONS).not.toContain('Large')
    expect(TAXPAYER_SIZE_OPTIONS).toContain(DEFAULT_TAXPAYER_SIZE)
  })
})
