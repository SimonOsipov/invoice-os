// Node-environment companion to Header.test.tsx. ENV_BANNER's `live` entry can never
// render while the LIVE segment is disabled, so its copy has no browser oracle.

import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import { ENV_BANNER } from './App'

const SRC = fileURLToPath(new URL('.', import.meta.url))
const APP_TSX = join(SRC, 'App.tsx')

const FORBIDDEN = ['legally-valid', 'legally valid', 'clearance evidence', 'sent to NRS', 'transmits to NRS', 'PRODUCTION · NRS']

// Test files are excluded — they carry the forbidden strings as fixtures.
function sourceFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) out.push(...sourceFiles(path))
    else if (/\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name)) out.push(path)
  }
  return out
}

describe('environment posture copy', () => {
  it('neither ENV_BANNER entry makes a forbidden claim', () => {
    for (const [name, entry] of Object.entries(ENV_BANNER)) {
      for (const field of ['msg', 'tag'] as const) {
        for (const phrase of FORBIDDEN) {
          expect(entry[field], `ENV_BANNER.${name}.${field} contains "${phrase}"`).not.toContain(phrase)
        }
      }
    }
  })

  // Layer 3 of the disabled-LIVE pattern: the only reason a keyboard/screen-reader
  // user can reach, since LIVE is out of tab order and its title never fires. Pins
  // the claim (simulated today, accreditation unlocks live), not the exact prose,
  // so a legitimate reword doesn't fail this alongside the forbidden-string checks.
  it('the sandbox banner states filing is simulated and names accreditation as what unlocks live filing', () => {
    const msg = ENV_BANNER.sandbox.msg.toLowerCase()

    expect(msg, 'no claim that filing today is simulated / not real').toMatch(/simulat|not filed with nrs/)
    expect(msg, 'no mention of accreditation as the unlock condition').toContain('accreditation')
  })

  it('no forbidden string anywhere in frontend/app/src', () => {
    const files = sourceFiles(SRC)
    expect(files.length, 'the source walk found nothing to scan').toBeGreaterThanOrEqual(20)

    for (const file of files) {
      const src = readFileSync(file, 'utf8').toLowerCase()
      for (const phrase of FORBIDDEN) {
        expect(src, `${file} contains "${phrase}"`).not.toContain(phrase.toLowerCase())
      }
    }
  })

  it("App.tsx pins useState(SANDBOX_DEFAULT) as the default's only call site", () => {
    const src = readFileSync(APP_TSX, 'utf8')

    expect(src).toContain('export const SANDBOX_DEFAULT = true')
    expect(src).toContain('useState(SANDBOX_DEFAULT)')
  })
})
