// IMPORT_KEYS and the steered AI answers must follow the field list (AC5).
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
// Generated from internal/invoicefields.ImportKeys() by the invoicefields Go test.
import { IMPORT_KEYS as LIST_IMPORT_KEYS } from '../frontend/app/src/lib/invoiceFields.gen'
import { AIR07_ROW3_ANSWER, IMPORT_KEYS, nullImportKeys } from './importFixtures'

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
