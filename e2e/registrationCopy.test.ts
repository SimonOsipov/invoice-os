// The free-mail refusal is hand-duplicated in api/registration.spec.ts: e2e cannot import Go.
// Without this the drift only surfaces as a deploy-gate failure.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const GO_SRC = 'internal/gateway/register.go'
const SPEC_SRC = 'e2e/api/registration.spec.ts'

function read(rel: string): string {
  return readFileSync(join(REPO_ROOT, rel), 'utf8')
}

// The writeError literal inside the isFreeMail branch.
function goCopy(src: string): string {
  const at = src.indexOf('isFreeMail(')
  if (at < 0) throw new Error(`${GO_SRC}: never calls isFreeMail`)
  const m = /writeError\([^,]+,[^,]+,\s*"((?:[^"\\]|\\.)*)"/.exec(src.slice(at))
  if (m == null) throw new Error(`${GO_SRC}: no writeError literal after isFreeMail`)
  return m[1]
}

function specCopy(src: string): string {
  const m = /const FREE_MAIL_REFUSED = '((?:[^'\\]|\\.)*)'/.exec(src)
  if (m == null) throw new Error(`${SPEC_SRC}: no FREE_MAIL_REFUSED string literal`)
  return m[1]
}

describe('free-mail refusal duplicated into the registration spec', () => {
  it('registrationCopy_freeMailLiteralMatchesTheGateway', () => {
    const go = goCopy(read(GO_SRC))
    const spec = specCopy(read(SPEC_SRC))
    // Floor: a regex that matched an empty string would make the equality vacuous.
    expect(go.length).toBeGreaterThan(40)
    expect(spec.length).toBeGreaterThan(40)
    expect(spec).toBe(go)
  })
})
