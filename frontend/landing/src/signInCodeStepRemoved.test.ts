// The retired sign-in code step and its credential chrome stay out of the landing build input.
// Test files are not scanned: this file and SignInModal.dom.test.tsx carry the needles as fixtures.
/// <reference types="node" />
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const HERE = dirname(fileURLToPath(import.meta.url))

type Needle = string | RegExp

// Literals are case-sensitive: data.tsx and Developers.tsx ship "OAuth2" copy that must stay legal.
const NEEDLES: readonly Needle[] = [
  '481920', 'DEMO_CODE', 'si-otp', "Verify it's you", 'Verify & continue', 'Back to accounts',
  'Resend code', 'Signing in', 'Forgot password', 'Password reset is disabled', 'OAUTH2',
  'redirectTimer', 'maskedEmail',
  /\bOTP\b/, /6-digit/i, /one-time code/i, /demo code/i,
]

// One scan for both the planted control and the real tree. No `g` flag: RegExp.test must stay stateless.
function codeStepHits(files: Readonly<Record<string, string>>): string[] {
  const hits: string[] = []
  for (const [file, src] of Object.entries(files)) {
    for (const n of NEEDLES) {
      if (typeof n === 'string' ? src.includes(n) : n.test(src)) hits.push(`${file}: ${String(n)}`)
    }
  }
  return hits
}

// Keys are paths relative to src/; index.html is the only build input outside it.
function landingBuildInput(): Record<string, string> {
  const files: Record<string, string> = {}
  for (const rel of readdirSync(HERE, { recursive: true, encoding: 'utf8' })) {
    const abs = join(HERE, rel)
    if (/\.test\./.test(rel) || !statSync(abs).isFile()) continue
    files[rel] = readFileSync(abs, 'utf8')
  }
  files['../index.html'] = readFileSync(join(HERE, '..', 'index.html'), 'utf8')
  return files
}

describe('the sign-in code step is gone from the landing build input', () => {
  it('T01-10: population floor, test files excluded', () => {
    const keys = Object.keys(landingBuildInput())
    expect(keys.length).toBeGreaterThanOrEqual(25)
    expect(keys).toContain('../index.html')
    expect(keys.some((k) => /\.test\./.test(k))).toBe(false)
    expect(existsSync(join(HERE, 'signInCodeStepRemoved.test.ts'))).toBe(true)
    expect(keys).not.toContain('signInCodeStepRemoved.test.ts')
    expect(existsSync(join(HERE, 'components', 'SignInModal.dom.test.tsx'))).toBe(true)
    expect(keys).not.toContain('components/SignInModal.dom.test.tsx')
  })

  it('T01-11: control needles resolve real files', () => {
    const files = landingBuildInput()
    expect(files['components/SignInModal.tsx']).toContain('data-persona')
    expect(files['auth.ts']).toContain('export const LANDING_PERSONAS')
  })

  it('T01-12: the scan reports a planted hit', () => {
    expect(codeStepHits({ 'planted.tsx': "// OTP step\nexport const DEMO_CODE = '481920'" }))
      .toEqual(['planted.tsx: 481920', 'planted.tsx: DEMO_CODE', 'planted.tsx: /\\bOTP\\b/'])
    // Exact equality also proves the scan does not over-match (no 'demo code' hit on DEMO_CODE).
  })

  it('T01-13: no needle occurs in any non-test landing source file', () => {
    const hits = codeStepHits(landingBuildInput())
    expect(hits, hits.join('\n')).toEqual([])
  })
})
