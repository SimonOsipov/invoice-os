// RMV-01-02: the specs that call the retiring single-document validate route are Playwright,
// which cannot run locally -- so the test-first oracle is this vitest guard, which reads the
// e2e sources as text. Mirrors rule-set.test.ts's walk + SELF_SRC self-exclusion.
import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const E2E_ROOT = dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = join(E2E_ROOT, '..')
// This file carries the needle and the /batch fixture; without the exclusion it fails against itself forever.
const SELF_SRC = join(E2E_ROOT, 'removalGuards.test.ts')

// The negative lookahead is the whole point: /api/validation/v1/validate/batch is fenced Out of
// Scope and survives. Two prior passes on this story filed the batch peer as a violation.
const BARE_ROUTE = /\/api\/validation\/v1\/validate(?!\/batch)/

// Surviving routes -- if the walk breaks, these go missing and the guard fails loudly
// instead of reporting a clean bill of health.
const CONTROL_NEEDLES = ['/api/invoice/v1/invoices/', '/api/validation/v1/rules/']

function listTsFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.name === 'node_modules' || entry.name.startsWith('.')) continue
    const full = join(dir, entry.name)
    if (entry.isDirectory()) out.push(...listTsFiles(full))
    else if (entry.name.endsWith('.ts')) out.push(full)
  }
  return out
}

function relPath(full: string): string {
  return relative(E2E_ROOT, full).split('\\').join('/')
}

type Hit = { file: string; line: number; text: string }

function scanText(src: string, label: string): Hit[] {
  return src
    .split('\n')
    .map((text, i) => ({ file: label, line: i + 1, text }))
    .filter((h) => BARE_ROUTE.test(h.text))
}

function scanFile(full: string): Hit[] {
  return scanText(readFileSync(full, 'utf8'), relPath(full))
}

function render(hits: Hit[]): string {
  return hits.map((h) => `  ${h.file}:${h.line}  ${h.text.trim()}`).join('\n')
}

// Import bindings routinely span lines here (api/portfolio.spec.ts:23), so the seam scan
// matches the whole brace group rather than one line -- a per-line regex read clean with a
// live multi-line seam caller re-planted. rmv01_theSeamScanSeesAMultiLineImportOfValidate
// pins it. `validate (` also occurs in prose comments (invoice-surfaces.spec.ts:744, :808,
// :1664), so a call only counts in a file that actually imports the binding.
const CLIENT_IMPORT = /import\s*(?:type\s+)?\{([^}]*)\}\s*from\s*'\.{1,2}(?:\/api)?\/client'/g
const SEAM_BINDING = /(?<![A-Za-z0-9_$])validate(?![A-Za-z0-9_$])/
const SEAM_CALL = /(?<![A-Za-z0-9_.$])validate\s*\(/

function seamOffenders(label: string, src: string): Hit[] {
  const lines = src.split('\n')
  const hits: Hit[] = []
  for (const m of src.matchAll(CLIENT_IMPORT)) {
    const bind = SEAM_BINDING.exec(m[1])
    if (!bind) continue
    const at = m.index + m[0].indexOf(m[1]) + bind.index
    const line = src.slice(0, at).split('\n').length
    hits.push({ file: label, line, text: lines[line - 1] })
  }
  if (hits.length === 0) return []
  lines.forEach((text, i) => {
    if (SEAM_CALL.test(text)) hits.push({ file: label, line: i + 1, text })
  })
  return hits.sort((a, b) => a.line - b.line)
}

const WALKED = listTsFiles(E2E_ROOT).filter((f) => f !== SELF_SRC)
// Set from the measured population (78 after self-exclusion). A truncated file list read clean
// in subtask 01's QA; this floor is what stops that.
const FILE_FLOOR = 70

describe('RMV-01-02 removal guards: the single-document validate route', () => {
  it('rmv01_theWalkedFileListMeetsItsFloor', () => {
    expect(WALKED, 'the e2e .ts walk returned nothing').not.toEqual([])
    expect(WALKED.length, `walked ${WALKED.length} .ts files under e2e/ -- below the floor, the list is truncated`).toBeGreaterThanOrEqual(FILE_FLOOR)
    expect(WALKED.length, 'implausibly many files -- node_modules leaked into the walk').toBeLessThanOrEqual(500)
    expect(WALKED, 'this guard file must exclude itself').not.toContain(SELF_SRC)
  })

  it('rmv01_theWalkStillFindsTheSurvivingControlRoutes', () => {
    for (const needle of CONTROL_NEEDLES) {
      const carriers = WALKED.filter((f) => readFileSync(f, 'utf8').includes(needle)).map(relPath)
      expect(carriers, `control needle ${needle} found in no walked file -- the walk is broken, not the tree clean`).not.toEqual([])
    }
  })

  it('rmv01_theValidateRouteScanIsNotBlindToTheBatchPeer', () => {
    const fixture = [
      "  const { status } = await rawFetch('/api/validation/v1/validate/batch', {",
      '  return apiFetch(`${apiBase()}/api/validation/v1/validate`, {',
    ].join('\n')

    const hits = scanText(fixture, 'fixture')
    expect(hits.map((h) => h.line), 'the scan must match the bare route once and the /batch peer never').toEqual([2])

    // The two real files that prior passes misreported as offenders.
    const perf = WALKED.find((f) => relPath(f) === 'api/perf.spec.ts')
    expect(perf, 'api/perf.spec.ts missing from the walk').toBeDefined()
    expect(render(scanFile(perf!)), 'api/perf.spec.ts is a /batch caller and must never be reported').toBe('')

    const gatewayTest = join(REPO_ROOT, 'internal/gateway/gateway_test.go')
    expect(render(scanText(readFileSync(gatewayTest, 'utf8'), 'internal/gateway/gateway_test.go')), 'gateway_test.go is a /batch caller and must never be reported').toBe('')
  })

  it('rmv01_noE2ECallerOfTheSingleDocumentValidateRoute', () => {
    const offenders = WALKED.flatMap(scanFile)
    expect(
      offenders.map((h) => `${h.file}:${h.line}`),
      `e2e still references the retiring single-document route:\n${render(offenders)}`,
    ).toEqual([])
  })

  it('rmv01_theSeamScanSeesAMultiLineImportOfValidate', () => {
    const multiLine = "import {\n  login,\n  validate,\n} from './client'\n\nawait validate(token, {})\n"
    expect(seamOffenders('fixture', multiLine).map((h) => h.line), 'a multi-line import of the seam must be reported').toEqual([3, 6])

    // validateInvoice is the SURVIVING route's seam and must never be reported.
    const survivor = "import {\n  validateInvoice,\n} from '../api/client'\n\nawait validateInvoice(token, id)\n"
    expect(seamOffenders('fixture', survivor), 'validateInvoice is the surviving seam').toEqual([])

    // A real file that imports validateInvoice, to prove the survivor case is not fixture-only.
    const killSwitch = WALKED.find((f) => relPath(f) === 'api/validation.spec.ts')
    expect(killSwitch, 'api/validation.spec.ts missing from the walk').toBeDefined()
    expect(seamOffenders('api/validation.spec.ts', readFileSync(killSwitch!, 'utf8'))).toEqual([])
  })

  // The literal scan above cannot see a caller that reaches the route through the typed
  // client.ts seam. Both callers must go for the route to be dead.
  it('rmv01_noE2ECallerOfTheValidateSeamFromApiClient', () => {
    const offenders = WALKED.filter((f) => relPath(f) !== 'api/client.ts').flatMap((f) =>
      seamOffenders(relPath(f), readFileSync(f, 'utf8')),
    )
    expect(
      offenders.map((h) => `${h.file}:${h.line}`),
      `e2e still reaches the retiring route through client.ts's validate() seam:\n${render(offenders)}`,
    ).toEqual([])
  })
})
