// The retired sign-in code step and its credential chrome stay out of the landing build input.
// Test files are not scanned: this file and SignInModal.dom.test.tsx carry the needles as fixtures.
/// <reference types="node" />
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const HERE = dirname(fileURLToPath(import.meta.url))

type Needle = string | RegExp

// Literals are case-sensitive: the landing ships "OAuth2" copy that must stay legal.
const NEEDLES: readonly Needle[] = [
  '481920', 'DEMO_CODE', 'si-otp', "Verify it's you", 'Verify & continue', 'Back to accounts',
  'Resend code', 'Signing in', 'Password reset is disabled', 'OAUTH2',
  'redirectTimer', 'maskedEmail',
  /\bOTP\b/, /6-digit/i, /one-time code/i, /demo code/i,
]

// The demo persona door: the chooser copy, its registry and its ?persona= URL builder. Case-sensitive.
const PERSONA_NEEDLES: readonly Needle[] = [
  'LANDING_PERSONAS', 'destUrl', '?persona=', 'Choose an account',
  'Amara Okafor', 'Emeka Iroha', 'Chinedu Okafor', 'Ngozi Balogun',
]

// One scan for both the planted control and the real tree. No `g` flag: RegExp.test must stay stateless.
// The source is read raw: a needle in a comment is a hit, because comments ship in the build input.
function scan(files: Readonly<Record<string, string>>, needles: readonly Needle[]): string[] {
  const hits: string[] = []
  for (const [file, src] of Object.entries(files)) {
    for (const n of needles) {
      if (typeof n === 'string' ? src.includes(n) : n.test(src)) hits.push(`${file}: ${String(n)}`)
    }
  }
  return hits
}
const codeStepHits = (files: Readonly<Record<string, string>>) => scan(files, NEEDLES)

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

  it('T01-11: the positive control resolves a real file', () => {
    const files = landingBuildInput()
    expect(files['components/Hero.tsx']).toContain('Africa moves.')
    expect(files['../index.html']).toBeDefined()
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

  it('T15-1: the scan reports a planted persona hit', () => {
    // Text order differs from needle order; one needle sits in a comment, which still counts.
    const planted = [
      "const a = 'Ngozi Balogun'",
      "const b = 'Chinedu Okafor'",
      "const c = 'Emeka Iroha'",
      "const d = 'Amara Okafor'",
      "const e = 'Choose an account'",
      "const f = `${base}?persona=${id}`",
      '// destUrl(p) builds it',
      'export const LANDING_PERSONAS = []',
    ].join('\n')
    expect(scan({ 'planted.tsx': planted }, PERSONA_NEEDLES)).toEqual([
      'planted.tsx: LANDING_PERSONAS',
      'planted.tsx: destUrl',
      'planted.tsx: ?persona=',
      'planted.tsx: Choose an account',
      'planted.tsx: Amara Okafor',
      'planted.tsx: Emeka Iroha',
      'planted.tsx: Chinedu Okafor',
      'planted.tsx: Ngozi Balogun',
    ])
    // Case-sensitive: lowercase variants are not hits.
    expect(scan({ 'lower.tsx': 'landing_personas desturl choose an account amara okafor' }, PERSONA_NEEDLES)).toEqual([])
  })

  it('T15-2: no persona needle in any non-test landing source file', () => {
    const files = landingBuildInput()
    expect(Object.keys(files).length, 'control: the walk found the build input').toBeGreaterThanOrEqual(25)
    const hits = scan(files, PERSONA_NEEDLES)
    expect(hits, hits.join('\n')).toEqual([])
  })
})
