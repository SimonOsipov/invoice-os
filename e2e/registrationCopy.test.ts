// The free-mail refusal is hand-duplicated in api/registration.spec.ts, topology/auth.spec.ts and the landing's register.ts: e2e cannot import Go.
// Without this the drift only surfaces as a deploy-gate failure.
import { describe, expect, it } from 'vitest'
import { stripComments } from '@invoice-os/api-client/strip-comments'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const GO_SRC = 'internal/gateway/register.go'
const SPEC_SRCS = ['e2e/api/registration.spec.ts', 'e2e/topology/auth.spec.ts']
const LANDING_SRC = 'frontend/landing/src/register.ts'

// Go shares the `//` and `/* */` syntax, so the TS stripper reads register.go too.
function read(rel: string): string {
  return stripComments(readFileSync(join(REPO_ROOT, rel), 'utf8'))
}

// The first writeError literal of the isFreeMail guard body.
function goCopy(src: string): string {
  const m = /\bif isFreeMail\([^)]*\)\s*\{\s*writeError\([^,]+,[^,]+,\s*"((?:[^"\\]|\\.)*)"\s*\)/.exec(src)
  if (m == null) throw new Error(`${GO_SRC}: no isFreeMail guard opening with a writeError literal`)
  return m[1]
}

function specCopy(src: string, rel: string): string {
  const m = /^const FREE_MAIL_REFUSED = '((?:[^'\\]|\\.)*)'/m.exec(src)
  if (m == null) throw new Error(`${rel}: no FREE_MAIL_REFUSED string literal`)
  return m[1]
}

function landingCopy(src: string): string {
  const m = /^export const FREE_MAIL_REFUSED = '((?:[^'\\]|\\.)*)'/m.exec(src)
  if (m == null) throw new Error(`${LANDING_SRC}: no exported FREE_MAIL_REFUSED string literal`)
  return m[1]
}

describe('free-mail refusal duplicated into the registration spec', () => {
  it('registrationCopy_freeMailLiteralMatchesTheGateway', () => {
    const go = goCopy(read(GO_SRC))
    expect(go.length).toBeGreaterThan(40)
    expect(SPEC_SRCS.length).toBe(2)
    for (const rel of SPEC_SRCS) {
      const spec = specCopy(read(rel), rel)
      // Floor: a regex that matched an empty string would make the equality vacuous.
      expect(spec.length, rel).toBeGreaterThan(40)
      expect(spec, rel).toBe(go)
    }
  })

  it('registrationCopy_landingFreeMailLiteralMatchesTheGateway', () => {
    const go = goCopy(read(GO_SRC))
    const landing = landingCopy(read(LANDING_SRC))
    expect(go.length).toBeGreaterThan(40)
    expect(landing.length).toBeGreaterThan(40)
    expect(landing).toBe(go)
  })
})
