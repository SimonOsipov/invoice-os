import { describe, expect, it } from 'vitest'
import { TYPE_SENTENCE } from './copy'
import { IP_SENTENCE, MAPPING_SENTENCE, refusal, SIGN_UP_SENTENCE, SLOW_UPLOAD_SENTENCE, UNAVAILABLE_SENTENCE, UNREADABLE_SENTENCE } from './messages'

describe('refusal', () => {
  it('refusal_409SaysSignUpWithALink', () => {
    expect(refusal('run', 409, SIGN_UP_SENTENCE)).toEqual({ text: SIGN_UP_SENTENCE, signUp: true })
  })

  it('refusal_gatewayDaily429KeepsItsSentence', () => {
    expect(refusal('run', 429, IP_SENTENCE)).toEqual({ text: IP_SENTENCE, signUp: false })
  })

  it('refusal_armor429WithNoBodySaysTryInAnHour', () => {
    expect(refusal('run', 429, null).text).toBe('Too many checks from this network. Try again in an hour.')
  })

  it('refusal_408KeepsTheGatewaySentence', () => {
    expect(refusal('run', 408, 'The gateway said this.')).toEqual({ text: 'The gateway said this.', signUp: false })
    expect(refusal('mapping', 408, SLOW_UPLOAD_SENTENCE).text).toBe(SLOW_UPLOAD_SENTENCE)
  })

  it('refusal_408WithNoBodySaysTryAFasterConnection', () => {
    expect(refusal('mapping', 408, null).text).toBe('The upload took too long. Try again on a faster connection.')
  })

  it('refusal_413IsPlain', () => {
    expect(refusal('run', 413, 'file is over 15 MiB').text).toBe('This file is over 15 MiB. Choose a smaller file.')
  })

  it('refusal_400EmailIsPlain', () => {
    expect(refusal('run', 400, 'email is invalid').text).toBe('Enter a valid email address.')
  })

  it('refusal_400ConsentIsPlain', () => {
    expect(refusal('run', 400, 'consent_text must be 1 to 500 characters').text).toBe('Tick the box to get your results by email.')
  })

  it('refusal_400FormatIsPlain', () => {
    expect(refusal('mapping', 400, 'file must be a CSV or XLSX spreadsheet').text).toBe(TYPE_SENTENCE)
    expect(refusal('run', 400, 'file must be CSV, XLSX, PDF or DOCX').text).toBe('Choose a CSV, XLSX, PDF or DOCX file.')
  })

  it('refusal_400ImporterTextOnTheRunIsTheMappingSentence', () => {
    const r = refusal('run', 400, 'mapping is missing required field invoice_number')
    expect(r.text).toBe(MAPPING_SENTENCE)
    expect(r.text).not.toContain('invoice_number')
  })

  it('refusal_400OnADocumentRunIsTheFileSentenceNotTheMappingOne', () => {
    expect(refusal('document', 400, 'invalid request body').text).toBe(UNREADABLE_SENTENCE)
    expect(refusal('run', 400, 'invalid request body').text).toBe(MAPPING_SENTENCE)
  })

  it('refusal_400OnTheMappingRouteIsTheColumnsSentence', () => {
    expect(refusal('mapping', 400, 'invalid request body').text).toBe('We could not read the columns of this file.')
  })

  it('refusal_422KeepsTheGatewaySentence', () => {
    expect(refusal('run', 422, 'We could not read this file.').text).toBe('We could not read this file.')
  })

  it('refusal_networkAnd5xxAreUnavailable', () => {
    for (const [s, e] of [[null, null], [502, 'x'], [503, null], [418, null]] as const) {
      expect(refusal('run', s, e)).toEqual({ text: UNAVAILABLE_SENTENCE, signUp: false })
    }
  })

  it('refusal_anOversizedGatewayErrorFallsBack', () => {
    expect(refusal('run', 409, 'x'.repeat(301))).toEqual({ text: SIGN_UP_SENTENCE, signUp: true })
  })

  it('refusal_anEmptyOrBodylessGatewayErrorFallsBack', () => {
    expect(refusal('run', 422, null).text).toBe(UNREADABLE_SENTENCE)
    expect(refusal('run', 409, '')).toEqual({ text: SIGN_UP_SENTENCE, signUp: true })
    expect(refusal('run', 409, 'x'.repeat(300)).text).toBe('x'.repeat(300))
  })
})
