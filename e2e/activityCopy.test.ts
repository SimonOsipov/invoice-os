// The Documents-chip reason is hand-duplicated in topology/invoice-surfaces.spec.ts: e2e has
// no dependency on frontend/app, so the spec cannot import ACTIVITY_COPY. Same shape as
// envCopy.test.ts — read the app source from disk rather than invert the package graph.
// Without this the drift only surfaces as a deploy-gate failure, and the copy has already
// gone stale twice (document.reused at DOC-02, extraction.* at EXTR-08).
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const APP_SRC = 'frontend/app/src/lib/invoiceActivity.ts'
const SPEC_SRC = 'e2e/topology/invoice-surfaces.spec.ts'
const TESTID = 'activity-chip-documents-reason'

function read(rel: string): string {
  return readFileSync(join(REPO_ROOT, rel), 'utf8')
}

// The ACTIVITY_COPY.documentsInert literal.
function appCopy(src: string): string {
  const m = /documentsInert:\s*'((?:[^'\\]|\\.)*)'/.exec(src)
  if (m == null) throw new Error(`${APP_SRC}: no documentsInert string literal`)
  return m[1]
}

// The literal the spec asserts on the reason element: the first toHaveText argument after
// the testid is named.
function specCopy(src: string): string {
  const at = src.indexOf(TESTID)
  if (at < 0) throw new Error(`${SPEC_SRC}: never names ${TESTID}`)
  const m = /toHaveText\(\s*'((?:[^'\\]|\\.)*)'/.exec(src.slice(at))
  if (m == null) throw new Error(`${SPEC_SRC}: no toHaveText literal after ${TESTID}`)
  return m[1]
}

describe('activity copy duplicated into the topology spec', () => {
  it('activityCopy_documentsReasonLiteralMatchesTheApp', () => {
    const app = appCopy(read(APP_SRC))
    const spec = specCopy(read(SPEC_SRC))
    // Floor: a regex that matched an empty string would make the equality vacuous.
    expect(app.length).toBeGreaterThan(40)
    expect(spec.length).toBeGreaterThan(40)
    expect(spec).toBe(app)
  })
})
