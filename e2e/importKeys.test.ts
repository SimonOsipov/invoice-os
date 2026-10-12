// IMPORT_KEYS and the steered AI answers must follow the field list (AC5).
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
// Generated from internal/invoicefields.ImportKeys() by the invoicefields Go test.
import { IMPORT_KEYS as LIST_IMPORT_KEYS, INVOICE_FIELDS } from '../frontend/app/src/lib/invoiceFields.gen'
import { AIR07_ROW3_ANSWER, EXTRACT_HEADER_KEYS, IMPORT_KEYS, nullExtractHeaderKeys, nullImportKeys } from './importFixtures'

const here = dirname(fileURLToPath(import.meta.url))

// The two spec files cannot be imported (Playwright registers their tests); read the literal with comments stripped.
function answerLiteral(file: string, name: string): string {
  const src = readFileSync(join(here, file), 'utf8')
  const start = src.indexOf(`const ${name}`)
  expect(start, `${name} in ${file}`).toBeGreaterThanOrEqual(0)
  const literal = src.slice(start, src.indexOf('\n}', start))
  return literal.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/.*$/gm, '')
}

describe('IMPORT_KEYS', () => {
  it('mirrors the generated import keys of the field list, in order', () => {
    expect(LIST_IMPORT_KEYS.length).toBeGreaterThan(0)
    expect(IMPORT_KEYS).toEqual([...LIST_IMPORT_KEYS])
  })

  it('nullImportKeys holds every import key as null', () => {
    expect(Object.keys(nullImportKeys())).toEqual(IMPORT_KEYS)
    expect(Object.values(nullImportKeys()).every((v) => v === null)).toBe(true)
  })

  it('AIR07_ROW3_ANSWER holds every import key', () => {
    expect(IMPORT_KEYS.length).toBeGreaterThan(0)
    for (const k of IMPORT_KEYS) expect(AIR07_ROW3_ANSWER, k).toHaveProperty(k)
  })

  it.each([
    ['topology/import-wizard.spec.ts', 'AIRL01_ANSWER'],
    ['api/contract-import.spec.ts', 'AIR07_API_ANSWER'],
  ])('%s %s spreads nullImportKeys()', (file, name) => {
    expect(answerLiteral(file, name)).toContain('...nullImportKeys()')
  })
})

describe('EXTRACT_HEADER_KEYS', () => {
  it('mirrors the extract header keys of the field list, in order', () => {
    const want = INVOICE_FIELDS.filter((f) => f.extract && !f.line).map((f) => f.key)
    expect(want.length).toBeGreaterThan(0)
    expect(EXTRACT_HEADER_KEYS).toEqual(want)
  })

  it('nullExtractHeaderKeys holds every key as null', () => {
    expect(Object.keys(nullExtractHeaderKeys())).toEqual(EXTRACT_HEADER_KEYS)
  })

  it('IMAGE_READ_ANSWER spreads nullExtractHeaderKeys()', () => {
    expect(answerLiteral('topology/import-wizard-2.spec.ts', 'IMAGE_READ_ANSWER')).toContain('...nullExtractHeaderKeys()')
  })
})
