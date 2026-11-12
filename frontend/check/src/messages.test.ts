import { describe, expect, it } from 'vitest'
import { TYPE_SENTENCE } from './copy'
import { CEILING_SENTENCE, IP_SENTENCE, MAPPING_SENTENCE, offersAnotherFile, PAGE_LIMIT_SENTENCE, refusal, SLOW_UPLOAD_SENTENCE, TOO_MUCH_TEXT_SENTENCE, UNAVAILABLE_SENTENCE, UNREADABLE_SENTENCE } from './messages'

describe('refusal', () => {
  it('refusal_409MapsLikeAnyUnknownStatus', () => {
    expect(refusal('run', 409, 'This address has used its 3 free checks. Sign up to check every invoice.')).toEqual({ text: UNAVAILABLE_SENTENCE })
  })

  it('refusal_gatewayDaily429KeepsItsSentence', () => {
    expect(refusal('run', 429, IP_SENTENCE)).toEqual({ text: IP_SENTENCE })
  })

  it('refusal_armor429WithNoBodySaysTryInAnHour', () => {
    expect(refusal('run', 429, null).text).toBe('Too many checks from this network. Try again in an hour.')
  })

  it('refusal_408KeepsTheGatewaySentence', () => {
    expect(refusal('run', 408, 'The gateway said this.')).toEqual({ text: 'The gateway said this.' })
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

  it('refusal_400TooMuchTextKeepsItsSentence', () => {
    expect(refusal('document', 400, TOO_MUCH_TEXT_SENTENCE)).toEqual({ text: TOO_MUCH_TEXT_SENTENCE })
  })

  it('refusal_400PageLimitKeepsItsSentence', () => {
    expect(refusal('document', 400, PAGE_LIMIT_SENTENCE)).toEqual({ text: PAGE_LIMIT_SENTENCE })
  })

  it('refusal_400PageLimitSentenceIsTheWorkerLiteral', () => {
    expect(PAGE_LIMIT_SENTENCE).toBe('This document is over the 800-page limit. Try a shorter invoice.') // readinesscheck.PageLimitSentence
  })

  it('refusal_400OtherWordOnADocumentIsUnreadable', () => {
    expect(refusal('document', 400, 'invalid request body').text).toBe(UNREADABLE_SENTENCE)
  })

  it('refusal_503CeilingKeepsItsSentence', () => {
    for (const route of ['run', 'mapping'] as const) {
      expect(refusal(route, 503, CEILING_SENTENCE)).toEqual({ text: CEILING_SENTENCE })
    }
  })

  it('refusal_503OtherWordOrNoneIsUnavailable', () => {
    for (const [route, e] of [['run', 'busy'], ['mapping', 'unavailable'], ['run', null]] as const) {
      expect(refusal(route, 503, e).text).toBe(UNAVAILABLE_SENTENCE)
    }
  })

  it('refusal_400OnTheMappingRouteIsTheColumnsSentence', () => {
    expect(refusal('mapping', 400, 'invalid request body').text).toBe('We could not read the columns of this file.')
  })

  it('refusal_422KeepsTheGatewaySentence', () => {
    expect(refusal('run', 422, 'We could not read this file.').text).toBe('We could not read this file.')
  })

  it('refusal_networkAnd5xxAreUnavailable', () => {
    for (const [s, e] of [[null, null], [502, 'x'], [503, null], [418, null]] as const) {
      expect(refusal('run', s, e)).toEqual({ text: UNAVAILABLE_SENTENCE })
    }
  })

  it('refusal_anOversizedGatewayErrorFallsBack', () => {
    expect(refusal('run', 422, 'x'.repeat(301))).toEqual({ text: UNREADABLE_SENTENCE })
  })

  it('refusal_anEmptyOrBodylessGatewayErrorFallsBack', () => {
    expect(refusal('run', 422, null).text).toBe(UNREADABLE_SENTENCE)
    expect(refusal('run', 422, '')).toEqual({ text: UNREADABLE_SENTENCE })
    expect(refusal('run', 422, 'x'.repeat(300)).text).toBe('x'.repeat(300))
  })
})

describe('offersAnotherFile', () => {
  it.each([
    ['run 422', 'run', 422, null, true],
    ['run 408', 'run', 408, null, true],
    ['document 400 invalid request body', 'document', 400, 'invalid request body', true],
    ['run 400 too much text', 'run', 400, TOO_MUCH_TEXT_SENTENCE, false],
    ['run 413', 'run', 413, null, false],
    ['run 503', 'run', 503, null, false],
    ['run 429', 'run', 429, null, false],
    ['run network', 'run', null, null, false],
  ] as const)('refusal_offersAnotherFileOnlyForAnUnreadableOrSlowFile %s', (_n, route, status, error, want) => {
    expect(offersAnotherFile(refusal(route, status, error))).toBe(want)
  })
})
