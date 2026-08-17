// task-575 (APPR-14-09) AC-4/5: activating the firm policy gates far more than the submit
// sites -- this proves the submit sites themselves stay enumerated as the policy-adjacent
// specs keep changing. Deployment-free vitest test, run by `pnpm --filter @invoice-os/e2e
// test:unit` (ci.yml), never a Playwright spec. Modelled on workspaceCoverage.test.ts (the
// control-needle + floor precedent) and api/no-db-access.test.ts (the source-scan
// precedent); built on the TypeScript compiler API rather than regex per
// topology/no-publish.test.ts's own history -- that guard shipped as regex, QA found three
// compiling bypasses, and it was rewritten on the AST. A raw text needle cannot read
// transitionInvoice's third argument (AC-6) or expand a helper into its call sites (AC-7);
// only the parser can.
//
// Two needle families, deliberately unequal treatment:
//   - API (e2e/api/*.spec.ts): `target: 'queued'` object properties and batch POSTs to
//     .../invoices/submissions. Each match is counted where it is written -- inside a
//     `test(...)`, or inside a named helper function if the site is a shared fixture
//     (createFailedInvoice). NEVER expanded to the helper's callers: AC-10's control needle
//     must resolve INSIDE createFailedInvoice by name, not be scattered across its four
//     callers.
//   - Topology (e2e/topology/*.spec.ts): submit-confirm testid clicks and
//     transitionInvoice(..., 'queued') calls. A testid click found inside a named helper IS
//     expanded to every call site of that helper (AC-7) -- submitSelected's one click line
//     is invisible to any per-caller regression unless each of its five callers counts on
//     its own (see KNOWN LIMITATIONS #1 for why this needed its own mechanism at all).
//
// The manifest is keyed on (file, "test:"+title or "helper:"+name, needle, ordinal-within-
// group) -- never file:line (AC-11). contract-invoice.spec.ts and invoice-surfaces.spec.ts
// drifted +6/+11/+64/+70 lines during this same story; a line-pinned manifest reds on the
// next unrelated comment edit and teaches everyone to bump the number without reading.
import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { basename, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'

const E2E_ROOT = dirname(fileURLToPath(import.meta.url))
const API_DIR = join(E2E_ROOT, 'api')
const TOPOLOGY_DIR = join(E2E_ROOT, 'topology')

const CONTRACT_INVOICE = 'contract-invoice.spec.ts'
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
// when no test() ancestor exists at all (module-scope helpers like createFailedInvoice and
// submitSelected).
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

// ==================================================================================
// API scan: e2e/api/*.spec.ts -- target:'queued' and batch POSTs to /invoices/submissions.
// Never expanded (see file header) -- each occurrence is counted where it is written.
// ==================================================================================

function isTargetQueued(node: ts.Node): boolean {
  return (
    ts.isPropertyAssignment(node) &&
    ts.isIdentifier(node.name) &&
    node.name.text === 'target' &&
    ts.isStringLiteralLike(node.initializer) &&
    node.initializer.text === 'queued'
  )
}

// rawFetch('.../invoices/submissions', { method: 'POST', ... }) -- the batch-submit door.
// method is checked explicitly so a hypothetical GET/DELETE on the same path is never
// counted as a submit.
function isBatchSubmitPost(node: ts.Node): boolean {
  if (!ts.isCallExpression(node)) return false
  if (!ts.isIdentifier(node.expression) || node.expression.text !== 'rawFetch') return false
  const url = node.arguments[0]
  if (!url || !ts.isStringLiteralLike(url) || !url.text.endsWith('/invoices/submissions')) return false
  const opts = node.arguments[1]
  if (!opts || !ts.isObjectLiteralExpression(opts)) return false
  return opts.properties.some(
    (p) =>
      ts.isPropertyAssignment(p) &&
      ts.isIdentifier(p.name) &&
      p.name.text === 'method' &&
      ts.isStringLiteralLike(p.initializer) &&
      p.initializer.text === 'POST',
  )
}

function scanApiFile(filePath: string): RawMatch[] {
  const file = basename(filePath)
  const sourceFile = parseFile(filePath)
  const out: RawMatch[] = []
  walk(sourceFile, (node) => {
    if (isTargetQueued(node)) {
      out.push({ file, line: lineOf(sourceFile, node), needle: 'target:queued', enclosing: resolveEnclosing(node) })
    } else if (isBatchSubmitPost(node)) {
      out.push({
        file,
        line: lineOf(sourceFile, node),
        needle: 'batch-post:/invoices/submissions',
        enclosing: resolveEnclosing(node),
      })
    }
  })
  return out
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
  return arg && ts.isStringLiteralLike(arg) ? arg.text : undefined
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

// Response-predicate OBSERVERS: page.waitForResponse(...) / page.on('request', ...) whose
// predicate references the submissions path. These do not drive a submit -- AC-16 excludes
// them from the floor and states the observer count is not the submit count. Reported by
// the STRING LITERAL's own line, matching how the story's notes cite them.
function scanObservers(filePath: string): number[] {
  const file = basename(filePath)
  const sourceFile = parseFile(filePath)
  const lines: number[] = []
  walk(sourceFile, (node) => {
    if (!ts.isCallExpression(node)) return
    const callee = node.expression
    const isWaitForResponse = ts.isPropertyAccessExpression(callee) && callee.name.text === 'waitForResponse'
    const isPageOnRequest =
      ts.isPropertyAccessExpression(callee) &&
      callee.name.text === 'on' &&
      node.arguments[0] !== undefined &&
      ts.isStringLiteralLike(node.arguments[0]) &&
      node.arguments[0].text === 'request'
    if (!isWaitForResponse && !isPageOnRequest) return
    for (const arg of node.arguments) {
      walk(arg, (n) => {
        if (ts.isStringLiteralLike(n) && n.text.endsWith('/invoices/submissions')) lines.push(lineOf(sourceFile, n))
      })
    }
  })
  void file
  return lines
}

// ==================================================================================
// Manifests -- every known submit-driving site, by (file, enclosing label, needle,
// ordinal-within-group). Re-measured 2026-08-18 against this story's own rewrites
// (task-575 Stage 1 notes); the Implementation Plan's line numbers and floor (22) predate
// subtasks 06/07/08 and are stale.
// ==================================================================================

type ManifestEntry = readonly [file: string, label: string, needle: string, ordinal: number]

const API_MANIFEST: ManifestEntry[] = [
  [CONTRACT_INVOICE, 'helper:createFailedInvoice', 'target:queued', 1],
  [CONTRACT_INVOICE, "test:a failed invoice's rejection_reasons and violations come back as arrays, never null", 'target:queued', 1],
  [
    CONTRACT_INVOICE,
    'test:validated -> queued is gated on the firm run: 409 while undecided (both doors), 200 once approveUntilClosed closes it',
    'target:queued',
    1,
  ],
  [
    CONTRACT_INVOICE,
    'test:validated -> queued is gated on the firm run: 409 while undecided (both doors), 200 once approveUntilClosed closes it',
    'target:queued',
    2,
  ],
  [CONTRACT_INVOICE, 'test:transition not-found (random UUID) -> 404 {error: string}', 'target:queued', 1],
  [CONTRACT_INVOICE, 'test:transition with no auth -> 401 {error: string}', 'target:queued', 1],
  [CONTRACT_INVOICE, 'test:POST /transitions {"target":"queued"} on a draft (demoted-from-validated) invoice is refused with 409', 'target:queued', 1],
  [CONTRACT_INVOICE, 'test:a preparer cannot drive a transition, and the refused invoice is unmoved', 'target:queued', 1],
  [CONTRACT_INVOICE, "test:a preparer's 403 is identical for a real invoice and a random UUID (no existence oracle)", 'target:queued', 1],
  [CONTRACT_INVOICE, "test:a preparer's 403 is identical for a real invoice and a random UUID (no existence oracle)", 'target:queued', 2],
  [CONTRACT_INVOICE, 'test:both transmit doors refuse a preparer with the same 403 body', 'target:queued', 1],
  [CONTRACT_INVOICE, "test:a preparer's submit_blocked_reason survives a queued invoice, where an admin's is null", 'target:queued', 1],
  [CONTRACT_INVOICE, 'test:journey: validate -> approve -> submit, via its own one-step policy', 'target:queued', 1],
  [
    CONTRACT_INVOICE,
    'test:rollup, the awaiting_approval list filter, and the enforcing transitions door agree on one self-seeded open run',
    'target:queued',
    1,
  ],
  [
    CONTRACT_INVOICE,
    'test:validated -> queued is gated on the firm run: 409 while undecided (both doors), 200 once approveUntilClosed closes it',
    'batch-post:/invoices/submissions',
    1,
  ],
  [CONTRACT_INVOICE, 'test:POST /invoices/submissions on a line-mutated (demoted) invoice skips it as not_validated', 'batch-post:/invoices/submissions', 1],
  [
    CONTRACT_INVOICE,
    'test:a preparer cannot batch-submit a validated invoice; an admin submitting the same invoice enqueues it',
    'batch-post:/invoices/submissions',
    1,
  ],
  [
    CONTRACT_INVOICE,
    'test:a preparer cannot batch-submit a validated invoice; an admin submitting the same invoice enqueues it',
    'batch-post:/invoices/submissions',
    2,
  ],
  [CONTRACT_INVOICE, 'test:both transmit doors refuse a preparer with the same 403 body', 'batch-post:/invoices/submissions', 1],
]

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
]

// AC-14: can_submit / awaiting_approval needle matches are deliberately OUT of scope here --
// not manifested, not floored. Their population moves with every assertion this story
// rewrites (Stage 1 notes), and most are prose (test titles, comments) rather than call
// sites. They are inventoried once, by hand, for the PR body; this file does not re-derive
// that count on every run.
//
// AC-17: persona-surfaces.spec.ts:830's `approvals-bulk-submit` testid is the bulk APPROVE
// control (its bar reads "Approve N invoices?") -- not a submit site, and not one of
// SUBMIT_TESTIDS above. persona-surfaces.spec.ts is out of this scan's scope entirely: only
// the firm tenant (PERSONAS.A) was newly governed by this story, and persona-surfaces.spec.ts's
// governed tests already ran against an active policy before it (Stage 1 notes, "SCOPE").
//
// AC-18: contract-invoice.spec.ts:755's test title contains `{"target":"queued"}` -- double-
// quoted JSON prose inside a string literal, not an object property. A regex loosened toward
// `target.*queued` would wrongly admit it; the AST only matches a real PropertyAssignment, so
// this near-miss is structurally invisible here without needing a deny-list entry.

// ==================================================================================
// Tests
// ==================================================================================

const apiFiles = listSpecFiles(API_DIR)
const topologyFiles = listSpecFiles(TOPOLOGY_DIR)

describe('firm-tenant submit-site sweep (task-575)', () => {
  it('walked a plausible file set (floor -- a broken walk must not read as clean)', () => {
    expect(apiFiles.length, 'found no e2e/api/*.spec.ts files -- the walk is broken').toBeGreaterThanOrEqual(5)
    expect(topologyFiles.length, 'found no e2e/topology/*.spec.ts files -- the walk is broken').toBeGreaterThanOrEqual(5)
  })

  it('scanned its control-needle files', () => {
    const apiNames = apiFiles.map((f) => basename(f))
    const topologyNames = topologyFiles.map((f) => basename(f))
    expect(apiNames, `${CONTRACT_INVOICE} not found -- the api walk is broken`).toContain(CONTRACT_INVOICE)
    expect(topologyNames, `${INVOICE_SURFACES} not found -- the topology walk is broken`).toContain(INVOICE_SURFACES)
    expect(topologyNames, `${IMPORT_WIZARD} not found -- the topology walk is broken`).toContain(IMPORT_WIZARD)
  })

  const apiRaw = apiFiles.flatMap(scanApiFile)
  const apiMatches = withOrdinals(apiRaw)

  const topologyRaw = topologyFiles.flatMap(scanTopologyFile)
  const topologyMatches = withOrdinals(topologyRaw)

  describe('api', () => {
    it('control needle: target:queued resolves inside createFailedInvoice, not pinned to a line', () => {
      const hit = apiMatches.some((m) => m.label === 'helper:createFailedInvoice' && m.needle === 'target:queued')
      expect(hit, 'target:queued not found inside createFailedInvoice -- the helper, or its inner call, moved').toBe(true)
    })

    it('floor: at least 18 submit-driving sites (AC-8: 13 target:queued + 5 batch POSTs)', () => {
      expect(apiMatches.length, `found ${apiMatches.length} submit-driving sites in e2e/api/*.spec.ts, floor is 18`).toBeGreaterThanOrEqual(18)
    })

    it('every submit-driving call site is in the manifest', () => {
      const manifestKeys = new Set(API_MANIFEST.map(([f, l, n, o]) => `${f}||${l}||${n}||${o}`))
      const unmanifested = apiMatches.filter((m) => !manifestKeys.has(`${m.file}||${m.label}||${m.needle}||${m.ordinal}`))
      expect(unmanifested.map(describeMatch), 'unmanifested submit-driving site(s) -- add a verdict for each').toEqual([])
    })

    it('the manifest names no site that no longer resolves', () => {
      const foundKeys = new Set(apiMatches.map((m) => `${m.file}||${m.label}||${m.needle}||${m.ordinal}`))
      const stale = API_MANIFEST.filter(([f, l, n, o]) => !foundKeys.has(`${f}||${l}||${n}||${o}`))
      expect(stale.map(([f, l, n, o]) => `${f} [${l} / ${n} #${o}]`), 'manifest entry no longer resolves to a live site').toEqual([])
    })
  })

  describe('topology', () => {
    it('floor: at least 7 submit-driving sites (AC-9: submit-confirm testids + 2 transitionInvoice(queued), not the 4 observers)', () => {
      expect(topologyMatches.length, `found ${topologyMatches.length} submit-driving sites in e2e/topology/*.spec.ts, floor is 7`).toBeGreaterThanOrEqual(7)
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

    // AC-16: the four response predicates are OBSERVERS, not submits -- named by the exact
    // lines the Stage 1 notes cite, so a fifth appearing (or one of these four vanishing)
    // is itself a signal something about the submissions wire changed shape.
    it('observers: exactly the four known response predicates, excluded from the floor above', () => {
      const observed = [
        ...scanObservers(join(TOPOLOGY_DIR, INVOICE_SURFACES)).map((line) => `${INVOICE_SURFACES}:${line}`),
        ...scanObservers(join(TOPOLOGY_DIR, IMPORT_WIZARD)).map((line) => `${IMPORT_WIZARD}:${line}`),
      ].sort()
      expect(
        observed,
        'the observer count is NOT the submit count -- these four watch the wire, they do not drive it',
      ).toEqual([`${IMPORT_WIZARD}:1184`, `${INVOICE_SURFACES}:1379`, `${INVOICE_SURFACES}:1671`, `${INVOICE_SURFACES}:242`].sort())
    })
  })
})

// KNOWN LIMITATIONS -- read before trusting this guard as total. Modelled on
// topology/no-publish.test.ts:261-300. Each of these was considered while building this
// scanner and left open deliberately, not by oversight.
//
// 1. A getByTestId(...).click() CANNOT BE RESOLVED TO A SUBMIT STATICALLY UNLESS THE CHAIN
//    IS WRITTEN DIRECTLY. `testidClickLiteral` only matches `page.getByTestId('x').click()`
//    written as one chained expression. `const btn = page.getByTestId('x'); ...;
//    await btn.click()` -- exactly what invoice-surfaces.spec.ts:1677 does with
//    'batch-submit-confirm', deliberately, as a locator held across several assertions
//    before the file re-arms and calls submitSelected() instead -- is invisible to this
//    scanner. It happens to be safe today because 1677's confirmBtn is never itself
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
// 4. THE FLOORS ARE DELIBERATELY SOFT, NOT EXACT. AC-8's api floor (18) equals the file's
//    full measured population -- any new test currently must raise it. AC-9's topology
//    floor (7) sits below the current measured population (9: 5 submitSelected callers + 1
//    detail-submit-confirm + 1 review-bulk-confirm + 2 transitionInvoice), giving two sites
//    of slack rather than pinning the exact count, per the story's own Implementation Notes.
//    A regression has to remove at least three real topology submit sites, not one, before
//    the floor alone catches it -- the manifest's per-site check (AC-12) is what catches a
//    single removed or added site; the floor exists only as the belt to that suspenders,
//    matching workspaceCoverage.test.ts's own precedent (floor 5 against a larger real
//    count).
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
