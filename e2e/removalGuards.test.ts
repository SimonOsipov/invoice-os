// RMV-01-02: the specs that call the retiring single-document validate route are Playwright,
// which cannot run locally -- so the test-first oracle is this vitest guard, which reads the
// e2e sources as text. Mirrors rule-set.test.ts's walk + SELF_SRC self-exclusion.
import { describe, expect, it } from 'vitest'
import { execSync } from 'node:child_process'
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

// ---------------------------------------------------------------------------
// RMV-01-04 (task-819) -- AC 6's closing gate: nothing that served only the
// validation playground survives anywhere in the tree.
//
// Scope is `git ls-files`, not a filesystem walk: only TRACKED files count, so
// untracked build debris can neither inflate the population floor nor plant a
// phantom offender.
// ---------------------------------------------------------------------------

const SELF_REL = 'e2e/removalGuards.test.ts'

// Case-sensitivity is load-bearing on invoicePayload: /invoicepayload/i hits 94
// legitimate validInvoicePayload/badInvoicePayload lines in internal/validation's
// seed tests. rmv01_theAbsenceScanCanStillSeeAPlantedNeedle pins that both ways.
type Needle = { name: string; re: RegExp; plant: string }
const PATTERNS: readonly Needle[] = [
  { name: 'playground', re: /playground/i, plant: '// the Validation Playground screen' },
  { name: 'ValidationView', re: /\bValidationView\b/, plant: "{view === 'validation' && <ValidationView ctx={ctx} />}" },
  { name: 'NAV_VALIDATION', re: /\bNAV_VALIDATION\b/, plant: 'export const NAV_VALIDATION: NavDef = { id: 0 }' },
  { name: 'invoicePayload', re: /\binvoicePayload\b/, plant: 'const [invoicePayload, setInvoicePayload] = useState()' },
  { name: 'playgroundState', re: /\bplaygroundState\b/, plant: 'const playgroundState = load()' },
  { name: 'doValidate', re: /\bdoValidate\b/, plant: 'async function doValidate() {' },
  { name: 'validateResponseBody', re: /\bvalidateResponseBody\b/, plant: 'export type validateResponseBody = {' },
  { name: 'M3-09', re: /\bM3-09\b/, plant: '// Path/Target is rendered by the M3-09 UI' },
]

// The guard file necessarily carries every needle; a whole-file self-exclusion is
// the same rule rule-set.test.ts:18,51 applies. The one NARROW (file, pattern)
// pair is personas.test.ts's own NAV_VALIDATION absence guard, which must name the
// token to assert it is gone. rmv01_theNarrowExclusionIsStillEarned stops it going stale.
const PAIR_EXCLUSIONS: readonly { file: string; pattern: string }[] = [
  { file: 'e2e/personas.test.ts', pattern: 'NAV_VALIDATION' },
]

const SCOPE_EXT = /\.(ts|tsx|go|md|sql|yml|json)$/
const SCOPE_SKIP = /(^|\/)(node_modules|\.git|\.ralph|\.systemmap|dist|build)\//

const TRACKED: string[] = execSync('git ls-files -z', { cwd: REPO_ROOT, maxBuffer: 256 * 1024 * 1024 })
  .toString('utf8')
  .split('\0')
  .filter(Boolean)
  .filter((p) => SCOPE_EXT.test(p) && !SCOPE_SKIP.test(p))

// Measured on 02f98fa5 (subtasks 01-03 landed). Floors sit a few files below so
// ordinary churn does not trip them, but a truncated listing does -- subtask 01's
// guard read clean on a file list silently cut to 2 entries.
const TREES: readonly { label: string; prefixes: string[]; measured: number; floor: number }[] = [
  { label: 'frontend/app/src', prefixes: ['frontend/app/src/'], measured: 248, floor: 240 },
  { label: 'e2e', prefixes: ['e2e/'], measured: 82, floor: 75 },
  { label: 'cmd+internal+tools', prefixes: ['cmd/', 'internal/', 'tools/'], measured: 722, floor: 700 },
  { label: 'docs', prefixes: ['docs/'], measured: 17, floor: 15 },
  { label: 'migrations', prefixes: ['migrations/'], measured: 56, floor: 50 },
]
const TOTAL_MEASURED = 1291
const TOTAL_FLOOR = 1250

const SEED_MIGRATION = 'migrations/20260711121327_seed_mbs_v1.sql'

// (file, needle) pairs that MUST still be found -- a walk that broke reports a
// clean tree, which is indistinguishable from a clean tree.
const REPO_CONTROLS: readonly [string, string][] = [
  ['frontend/app/src/components/InvoiceDetail.tsx', 'ViolationsTable'],
  ['internal/validation/handlers.go', 'BatchValidateHandler'],
  ['e2e/personas.ts', 'SURFACES'],
  ['docs/e2e-convention.md', 'invoice-surfaces.spec.ts'],
  [SEED_MIGRATION, 'rule_set_versions'],
]

function readRepoFile(rel: string): string {
  return readFileSync(join(REPO_ROOT, rel), 'utf8')
}

function excluded(rel: string, pattern: string): boolean {
  if (rel === SELF_REL) return true
  return PAIR_EXCLUSIONS.some((x) => x.file === rel && x.pattern === pattern)
}

function residueHits(): Hit[] {
  const hits: Hit[] = []
  for (const rel of TRACKED) {
    if (rel === SELF_REL) continue
    const lines = readRepoFile(rel).split('\n')
    for (const n of PATTERNS) {
      if (excluded(rel, n.name)) continue
      lines.forEach((text, i) => {
        if (n.re.test(text)) hits.push({ file: `${rel} [${n.name}]`, line: i + 1, text })
      })
    }
  }
  return hits
}

describe('RMV-01-04 removal guards: no playground residue anywhere in the repo', () => {
  it('rmv01_theRepoWideWalkMeetsItsFloors', () => {
    expect(TRACKED, 'git ls-files returned nothing in scope').not.toEqual([])
    expect(TRACKED.length, `walked ${TRACKED.length} tracked files (measured ${TOTAL_MEASURED}) -- below the floor the listing is truncated`).toBeGreaterThanOrEqual(TOTAL_FLOOR)
    expect(TRACKED.length, 'implausibly many files -- an excluded tree leaked into the walk').toBeLessThanOrEqual(5000)

    for (const t of TREES) {
      const n = TRACKED.filter((p) => t.prefixes.some((pre) => p.startsWith(pre))).length
      expect(n, `${t.label}: walked ${n} files (measured ${t.measured}) -- below the floor the listing is truncated`).toBeGreaterThanOrEqual(t.floor)
    }

    // The scan skips this file, but the WALK must still see it -- otherwise a broken
    // listing and a working one look the same.
    expect(TRACKED, 'the walk must reach this guard file').toContain(SELF_REL)
    expect(excluded(SELF_REL, 'playground'), 'this guard file must exclude itself from the scan').toBe(true)
  })

  it('rmv01_theAbsenceScanCanStillSeeAPlantedNeedle', () => {
    // Every pattern, not one representative: a single rotted regex would otherwise
    // hide behind its neighbours in an all-absent scan.
    for (const n of PATTERNS) {
      expect(n.re.test(n.plant), `pattern ${n.name} no longer matches its own planted needle: ${n.plant}`).toBe(true)
    }

    // invoicePayload must stay case-sensitive: these are the shapes it would wrongly
    // claim, and they are live test helpers, not residue.
    const SEED_SHAPES = [
      'result, err := engine.Evaluate(validInvoicePayload(), rs)',
      'p := badInvoicePayload()',
      'func TestAuditNumber_InvoicePayloadKeysAreOnlyWidened(t *testing.T) {',
    ].join('\n')
    const invoicePayload = PATTERNS.find((n) => n.name === 'invoicePayload')!
    expect(invoicePayload.re.test(SEED_SHAPES), 'invoicePayload must not fire on validInvoicePayload/badInvoicePayload').toBe(false)

    // Not fixture-only: against the real seed tests the case-sensitive pattern is
    // silent while a loosened /i variant is not. Both halves matter -- the second
    // proves the first is a real discrimination, not an empty file.
    const seedSrc = ['internal/validation/seed_test.go', 'internal/validation/seed_adversarial_test.go']
      .map(readRepoFile)
      .join('\n')
    expect(seedSrc.length, 'the seed tests must have content to discriminate against').toBeGreaterThan(1000)
    expect(invoicePayload.re.test(seedSrc), 'the real seed tests must yield zero case-sensitive invoicePayload hits').toBe(false)
    expect(/invoicepayload/i.test(seedSrc), 'a loosened /i variant DOES hit the seed tests -- that is why the pattern is case-sensitive').toBe(true)

    // playground stays case-insensitive.
    expect(PATTERNS[0].re.test('Validation PLAYGROUND'), 'playground must match case-insensitively').toBe(true)
  })

  it('rmv01_theWalkStillFindsTheControlNeedles', () => {
    for (const [rel, needle] of REPO_CONTROLS) {
      expect(TRACKED, `control file ${rel} is missing from the walk`).toContain(rel)
      expect(readRepoFile(rel).includes(needle), `control needle ${needle} not found in ${rel} -- the walk is broken, not the tree clean`).toBe(true)
    }
  })

  it('rmv01_theSeedMigrationNeedsNoExclusion', () => {
    const src = readRepoFile(SEED_MIGRATION)
    const matched = PATTERNS.filter((n) => n.re.test(src)).map((n) => n.name)
    expect(matched, `${SEED_MIGRATION} matches a pattern -- either narrow the pattern or earn an exclusion`).toEqual([])
    expect(src.includes('rule_set_versions'), 'the seed migration was read but carries no rule_set_versions -- wrong file').toBe(true)
  })

  it('rmv01_theNarrowExclusionIsStillEarned', () => {
    for (const x of PAIR_EXCLUSIONS) {
      const src = readRepoFile(x.file)
      const n = PATTERNS.find((p) => p.name === x.pattern)!
      expect(n.re.test(src), `the (${x.file}, ${x.pattern}) exclusion no longer matches anything -- delete it before it hides a regression`).toBe(true)

      // And it must be earned for that ONE pattern only.
      const others = PATTERNS.filter((p) => p.name !== x.pattern && p.re.test(src)).map((p) => p.name)
      expect(others, `${x.file} is excluded for ${x.pattern} but also carries ${others.join(', ')}`).toEqual([])
    }
  })

  it('rmv01_noPlaygroundReferenceSurvivesInTheRepo', () => {
    const offenders = residueHits()
    expect(
      offenders.map((h) => `${h.file}:${h.line}`),
      `playground residue survives in the tree:\n${render(offenders)}`,
    ).toEqual([])
  })
})
