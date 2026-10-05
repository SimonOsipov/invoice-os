// Keeps every topology submit-driving site enumerated: invoice-surfaces.spec.ts's SUBMIT-SITE
// NOTE names TOPOLOGY_MANIFEST and its exact floor. Built on the TypeScript compiler API: a
// text needle cannot read transitionInvoice's third argument or expand a helper into its
// call sites.
//
// Needles (e2e/topology/*.spec.ts): submit-confirm testid clicks and
// transitionInvoice(..., 'queued') calls. A testid click inside a named helper is expanded to
// every call site of that helper, so each of submitSelected's callers counts on its own.
//
// The manifest is keyed on (file, "test:"+title or "helper:"+name, needle, ordinal-within-
// group), never by line: a line-pinned manifest reds on the next unrelated comment edit.
import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { basename, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'

const E2E_ROOT = dirname(fileURLToPath(import.meta.url))
const TOPOLOGY_DIR = join(E2E_ROOT, 'topology')

const INVOICE_SURFACES = 'invoice-surfaces.spec.ts'
const IMPORT_WIZARD = 'import-wizard.spec.ts'

function listSpecFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) out.push(...listSpecFiles(full))
    else if (entry.isFile() && entry.name.endsWith('.spec.ts')) out.push(full)
  }
  return out
}

function parseFile(filePath: string): ts.SourceFile {
  return ts.createSourceFile(filePath, readFileSync(filePath, 'utf8'), ts.ScriptTarget.Latest, true)
}

function lineOf(sourceFile: ts.SourceFile, node: ts.Node): number {
  return sourceFile.getLineAndCharacterOfPosition(node.getStart(sourceFile)).line + 1
}

// ---- attribution: which test() or which named helper function owns a given AST node ----

type Enclosing = { kind: 'test'; title: string } | { kind: 'helper'; name: string } | { kind: 'none' }

// Walks up the parent chain. A `test(...)` ancestor always wins, however deep, so a match
// inside an arrow function nested in a test (a page.on callback, for instance) still
// resolves to the test -- only a genuinely top-level named function stands in for the test
// when no test() ancestor exists at all (module-scope helpers like submitSelected).
function resolveEnclosing(node: ts.Node): Enclosing {
  let helper: string | undefined
  let n: ts.Node | undefined = node.parent
  while (n) {
    if (ts.isCallExpression(n) && ts.isIdentifier(n.expression) && n.expression.text === 'test') {
      const arg = n.arguments[0]
      if (arg && ts.isStringLiteralLike(arg)) return { kind: 'test', title: arg.text }
    }
    if (!helper && ts.isFunctionDeclaration(n) && n.name) helper = n.name.text
    n = n.parent
  }
  return helper ? { kind: 'helper', name: helper } : { kind: 'none' }
}

function enclosingLabel(e: Enclosing): string {
  if (e.kind === 'test') return `test:${e.title}`
  if (e.kind === 'helper') return `helper:${e.name}`
  return 'unattributed'
}

// ---- generic tree walk ----

function walk(node: ts.Node, visit: (n: ts.Node) => void): void {
  visit(node)
  ts.forEachChild(node, (child) => walk(child, visit))
}

interface RawMatch {
  file: string
  line: number
  needle: string
  enclosing: Enclosing
}

interface Match extends RawMatch {
  label: string
  ordinal: number
}

// Assigns ordinal-within-(file, label, needle) in source order. Sorting by (file, line)
// first makes the grouping order-correct regardless of which pass produced each entry --
// direct matches and expanded call-site matches alike.
function withOrdinals(matches: RawMatch[]): Match[] {
  const sorted = [...matches].sort((a, b) => a.file.localeCompare(b.file) || a.line - b.line)
  const seen = new Map<string, number>()
  return sorted.map((m) => {
    const label = enclosingLabel(m.enclosing)
    const key = `${m.file}||${label}||${m.needle}`
    const ordinal = (seen.get(key) ?? 0) + 1
    seen.set(key, ordinal)
    return { ...m, label, ordinal }
  })
}

function describeMatch(m: Match): string {
  return `${m.file}:${m.line} [${m.label} / ${m.needle} #${m.ordinal}]`
}

// Peels a string literal through wrapper forms that don't change its value -- `as const`,
// `as T`, `satisfies T`, plain parens. QA's mutation battery (task-575) found `'queued' as
// const` a real, silent bypass: valid TS, identical at runtime, invisible to a bare
// ts.isStringLiteralLike(node) check.
function unwrapLiteral(node: ts.Node): ts.Node {
  while (ts.isParenthesizedExpression(node) || ts.isAsExpression(node) || ts.isSatisfiesExpression(node)) {
    node = node.expression
  }
  return node
}

// ==================================================================================
// Topology scan: e2e/topology/*.spec.ts -- submit-confirm testid clicks and
// transitionInvoice(..., 'queued'). Testid clicks found inside a named helper ARE expanded
// to every call site of that helper (AC-7).
// ==================================================================================

const SUBMIT_TESTIDS = ['batch-submit-confirm', 'detail-submit-confirm', 'review-bulk-confirm']

// page.getByTestId('X').click() -- a DIRECT chained call only. See KNOWN LIMITATIONS #1: a
// testid stored in a variable and clicked later is invisible to this check.
function testidClickLiteral(node: ts.Node): string | undefined {
  if (!ts.isCallExpression(node)) return undefined
  if (!ts.isPropertyAccessExpression(node.expression) || node.expression.name.text !== 'click') return undefined
  const inner = node.expression.expression
  if (!ts.isCallExpression(inner)) return undefined
  if (!ts.isPropertyAccessExpression(inner.expression) || inner.expression.name.text !== 'getByTestId') return undefined
  const arg = inner.arguments[0]
  return arg && ts.isStringLiteralLike(arg) ? arg.text : undefined
}

// transitionInvoice(token, id, target) -- reads the THIRD argument (AC-6); a raw needle on
// the call name alone cannot distinguish 'queued' from 'failed'.
function transitionInvoiceTarget(node: ts.Node): string | undefined {
  if (!ts.isCallExpression(node)) return undefined
  if (!ts.isIdentifier(node.expression) || node.expression.text !== 'transitionInvoice') return undefined
  const arg = node.arguments[2]
  const value = arg && unwrapLiteral(arg)
  return value && ts.isStringLiteralLike(value) ? value.text : undefined
}

// Every direct call site of `name` in this file, e.g. `submitSelected(page)` -- used to
// expand a helper-body match into one entry per caller (AC-7).
function callSites(sourceFile: ts.SourceFile, name: string): ts.CallExpression[] {
  const out: ts.CallExpression[] = []
  walk(sourceFile, (node) => {
    if (ts.isCallExpression(node) && ts.isIdentifier(node.expression) && node.expression.text === name) out.push(node)
  })
  return out
}

function scanTopologyFile(filePath: string): RawMatch[] {
  const file = basename(filePath)
  const sourceFile = parseFile(filePath)
  const direct: RawMatch[] = []
  const helperHits = new Map<string, { needle: string; helperName: string }>() // helperName -> testid needle

  walk(sourceFile, (node) => {
    const testid = testidClickLiteral(node)
    if (testid && SUBMIT_TESTIDS.includes(testid)) {
      const enclosing = resolveEnclosing(node)
      const needle = `click:${testid}`
      if (enclosing.kind === 'helper') {
        // Recorded once per helper name -- the expansion below walks ALL of that helper's
        // call sites, so the single in-body match must not also survive as its own entry.
        helperHits.set(enclosing.name, { needle, helperName: enclosing.name })
      } else {
        direct.push({ file, line: lineOf(sourceFile, node), needle, enclosing })
      }
      return
    }
    const target = transitionInvoiceTarget(node)
    if (target === 'queued') {
      direct.push({ file, line: lineOf(sourceFile, node), needle: 'transitionInvoice:queued', enclosing: resolveEnclosing(node) })
    }
  })

  const expanded: RawMatch[] = []
  for (const { needle, helperName } of helperHits.values()) {
    for (const site of callSites(sourceFile, helperName)) {
      expanded.push({ file, line: lineOf(sourceFile, site), needle, enclosing: resolveEnclosing(site) })
    }
  }

  return [...direct, ...expanded]
}

// ==================================================================================
// Manifest -- every known topology submit-driving site, by (file, enclosing label, needle,
// ordinal-within-group).
// ==================================================================================

type ManifestEntry = readonly [file: string, label: string, needle: string, ordinal: number]

const TOPOLOGY_MANIFEST: ManifestEntry[] = [
  [
    INVOICE_SURFACES,
    'test:submission surface: batch-select and submit a validated invoice, badge advances to ACCEPTED, and its detail shows a real IRN and a rendered QR',
    'click:batch-submit-confirm',
    1,
  ],
  [INVOICE_SURFACES, 'test:submission surface: reject → fix → re-validate → resubmit → accept, entirely from the browser', 'click:batch-submit-confirm', 1],
  [INVOICE_SURFACES, 'test:submission surface: reject → fix → re-validate → resubmit → accept, entirely from the browser', 'click:batch-submit-confirm', 2],
  [
    INVOICE_SURFACES,
    'test:detail surface: a rejected invoice is edited back to draft with its reasons retained, then re-validated to green (Core AC 6)',
    'click:batch-submit-confirm',
    1,
  ],
  [INVOICE_SURFACES, 'test:register-confirm-stage: arm, a selection change disarms, re-arm sends exactly one POST', 'click:batch-submit-confirm', 1],
  [
    INVOICE_SURFACES,
    'test:detail surface: submit one invoice from its own page -- cancel sends nothing, confirm sends one, and the verdict lands without leaving',
    'click:detail-submit-confirm',
    1,
  ],
  [
    IMPORT_WIZARD,
    'test:INVCR-E2E-1 firm: mixed import -> filter by rule -> expand -> fix -> re-validate -> select -> submit, badges from a re-fetch',
    'click:review-bulk-confirm',
    1,
  ],
  [INVOICE_SURFACES, 'test:submission surface: a failed invoice is an honest dead end', 'transitionInvoice:queued', 1],
  [
    INVOICE_SURFACES,
    'test:resolve/unresolve loop: marking a failed invoice resolved drops it from needs-attention without re-driving it, and undo reverses that',
    'transitionInvoice:queued',
    1,
  ],
  // The state-strip geometry block's test.beforeAll -- a describe-level fixture, so
  // resolveEnclosing finds neither a test() nor a top-level function declaration. It drives one
  // invoice to `failed` (via queued) purely so the strip has five attributed nodes to measure;
  // no browser submit control is involved.
  [INVOICE_SURFACES, 'unattributed', 'transitionInvoice:queued', 1],
  // The activity-card geometry block's test.beforeAll (AUDIT-09-04), same shape as #1
  // above: it drives one invoice to `failed` (via queued) so the card has real audit rows
  // to measure. No browser submit control is involved.
  [INVOICE_SURFACES, 'unattributed', 'transitionInvoice:queued', 2],
  // The action-cluster geometry block's test.beforeAll (BUG-14-04), same shape as the two
  // above: it drives one invoice to `failed` (via queued) purely to get a can_edit-false
  // status to measure the cluster at. No browser submit control is involved.
  [INVOICE_SURFACES, 'unattributed', 'transitionInvoice:queued', 3],
  // AUDIT-09-08's deployed journey: the FIRS-rejection leg submits from the register via
  // submitSelected, so the helper's one click expands to this call site (AC-7).
  [INVOICE_SURFACES, 'test:detail surface: a FIRS rejection redens the final node only', 'click:batch-submit-confirm', 1],
  // AUDIT-09-09's rail-order guard: the fiscal-record card only mounts on `accepted` with a
  // real IRN, and a raw transitionInvoice never creates a submission job -- so pinning the
  // rail's three cards needs a real browser submit.
  [INVOICE_SURFACES, 'test:detail surface: the untouched rail order is unchanged', 'click:batch-submit-confirm', 1],
  // DV-01 and DV-02: raw transitions create no submission job, so nothing
  // reaches an adapter. DV-02 ends `accepted` in a finally.
  [INVOICE_SURFACES, 'test:DV-01 failed invoice detail, the none state and the resolve-outside fit', 'transitionInvoice:queued', 1],
  [INVOICE_SURFACES, 'test:DV-02 submitted invoice detail', 'transitionInvoice:queued', 1],
  // DV-03 submits from the register with MOCK_TIN_ACCEPT, so the mock APP adapter answers.
  [INVOICE_SURFACES, 'test:DV-03 accepted invoice detail and the XML modal', 'click:batch-submit-confirm', 1],
]

// AC-14: can_submit / awaiting_approval needle matches are deliberately OUT of scope here --
// not manifested, not floored. Their population moves with every assertion this story
// rewrites (Stage 1 notes), and most are prose (test titles, comments) rather than call
// sites. They are inventoried once, by hand, for the PR body; this file does not re-derive
// that count on every run.
//
// AC-17: persona-surfaces.spec.ts's `approvals-bulk-submit` testid is the bulk APPROVE
// control (its bar reads "Approve N invoices?") -- not a submit site, and not one of
// SUBMIT_TESTIDS above. persona-surfaces.spec.ts is out of this scan's scope entirely: only
// the firm tenant (PERSONAS.A) was newly governed by this story, and persona-surfaces.spec.ts's
// governed tests already ran against an active policy before it (Stage 1 notes, "SCOPE").

// ==================================================================================
// Tests
// ==================================================================================

const topologyFiles = listSpecFiles(TOPOLOGY_DIR)

describe('firm-tenant submit-site sweep (task-575)', () => {
  it('walked a plausible file set (floor -- a broken walk must not read as clean)', () => {
    expect(topologyFiles.length, 'found no e2e/topology/*.spec.ts files -- the walk is broken').toBeGreaterThanOrEqual(5)
  })

  it('scanned its control-needle files', () => {
    const topologyNames = topologyFiles.map((f) => basename(f))
    expect(topologyNames, `${INVOICE_SURFACES} not found -- the topology walk is broken`).toContain(INVOICE_SURFACES)
    expect(topologyNames, `${IMPORT_WIZARD} not found -- the topology walk is broken`).toContain(IMPORT_WIZARD)
  })

  const topologyRaw = topologyFiles.flatMap(scanTopologyFile)
  const topologyMatches = withOrdinals(topologyRaw)

  describe('topology', () => {
    // AC-9 as literally worded said "fails below 7"; the story's own author confirmed that
    // was a miscount (7 was meant as "5 submitSelected callers + detail-submit-confirm +
    // review-bulk-confirm", forgetting it should then ADD the 2 transitionInvoice calls to
    // reach 9, not stop at 7). A floor below the measured population lets someone delete a
    // real submit site and stay green.
    //
    // Re-measured 2026-08-26 (AUDIT-12-06): 13 = 7 submitSelected callers
    // (batch-submit-confirm) + 1 detail-submit-confirm + 1 review-bulk-confirm + 4
    // transitionInvoice(..., 'queued'). 14 -> 13 is D-9 dropping the un-membered-actor
    // fixture's submit site (F-12): a sanctioned deletion, not a regression, so the floor
    // moves with it. Manifesting a site and flooring it are two steps and the suite only
    // enforces the first -- AUDIT-09-04 manifested its site and left the floor at 11 against
    // a population of 12, so an "exact" floor sat a whole site slack until AUDIT-09-08
    // corrected it.
    it('floor: at least 13 submit-driving sites (measured population -- see the comment above, not AC-9\'s literal "7")', () => {
      expect(topologyMatches.length, `found ${topologyMatches.length} submit-driving sites in e2e/topology/*.spec.ts, floor is 13`).toBeGreaterThanOrEqual(13)
    })

    it('every submit-driving call site is in the manifest', () => {
      const manifestKeys = new Set(TOPOLOGY_MANIFEST.map(([f, l, n, o]) => `${f}||${l}||${n}||${o}`))
      const unmanifested = topologyMatches.filter((m) => !manifestKeys.has(`${m.file}||${m.label}||${m.needle}||${m.ordinal}`))
      expect(unmanifested.map(describeMatch), 'unmanifested submit-driving site(s) -- add a verdict for each').toEqual([])
    })

    it('the manifest names no site that no longer resolves', () => {
      const foundKeys = new Set(topologyMatches.map((m) => `${m.file}||${m.label}||${m.needle}||${m.ordinal}`))
      const stale = TOPOLOGY_MANIFEST.filter(([f, l, n, o]) => !foundKeys.has(`${f}||${l}||${n}||${o}`))
      expect(stale.map(([f, l, n, o]) => `${f} [${l} / ${n} #${o}]`), 'manifest entry no longer resolves to a live site').toEqual([])
    })
  })
})

// KNOWN LIMITATIONS -- read before trusting this guard as total. Each of these was
// considered while building this scanner and left open deliberately, not by oversight.
//
// 1. A getByTestId(...).click() CANNOT BE RESOLVED TO A SUBMIT STATICALLY UNLESS THE CHAIN
//    IS WRITTEN DIRECTLY. `testidClickLiteral` only matches `page.getByTestId('x').click()`
//    written as one chained expression. `const btn = page.getByTestId('x'); ...;
//    await btn.click()` -- exactly what invoice-surfaces.spec.ts's register-confirm-stage
//    test does with 'batch-submit-confirm', deliberately, as a locator held across several
//    assertions before the file re-arms and calls submitSelected() instead -- is invisible to
//    this scanner. It happens to be safe today because that confirmBtn is never itself
//    clicked, but a future test that DOES click through a held locator would not be
//    caught, floor or manifest.
//
// 2. HELPER EXPANSION IS ONE LEVEL DEEP AND NAME-DECLARATION ONLY. `callSites` matches a
//    bare `Identifier(...)` call against a `function name(...)` declaration. A helper
//    assigned to a const (`const submitSelected = async (page) => {...}`), called via a
//    namespace (`helpers.submitSelected(page)`), or itself calling a SECOND helper that
//    contains the testid click, would not be found or expanded.
//
// 3. STRING LITERALS ONLY. A testid built at runtime (`getByTestId(\`${prefix}-confirm\`)`)
//    or a transitionInvoice target passed through a variable rather than written inline
//    matches nothing here. Nothing in e2e/ does this today for the needles this file cares
//    about.
//
// 4. THE FLOOR IS EXACT, NOT SOFT -- it equals today's full measured population, 13 (7
//    submitSelected callers + 1 detail-submit-confirm + 1 review-bulk-confirm + 4
//    transitionInvoice), so deleting even ONE real site fails the floor directly,
//    independent of the manifest.
//
//    What remains genuinely unguarded: a single commit that removes one real site and adds
//    an unrelated one elsewhere (or edits an existing manifest entry's file/label/needle/
//    ordinal to point at a different, coincidentally-real, call site) leaves the count
//    unchanged and the manifest internally consistent -- both the floor and the per-site
//    manifest check (AC-12/13) pass. Nothing here verifies a manifest diff was itself an
//    intentional, reviewed change; that remains code review's job, not this scanner's.
//    Needing to bump the floor when a legitimate new test raises the population is
//    expected friction from this design, not a bug.
//
// 5. `can_submit` / `awaiting_approval` MATCHES ARE ENTIRELY OUT OF SCOPE (AC-14) -- neither
//    scanned, manifested, nor floored. Their population is dominated by prose (test titles,
//    comments already excluded at the AST level) and moves with nearly every assertion this
//    story's siblings write. Treating them as inventory-only, verified by hand for the PR
//    body, was a scope decision, not an oversight.
//
// If any of these is ever exploited, the fix belongs in code review and/or a follow-up to
// this file -- not in silently trusting a guard that has already been shown, in writing, to
// have a ceiling.
