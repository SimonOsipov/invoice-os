// ROUTE-01-07's static oracle. Playwright never runs pre-`ready` (dev-env.yml gates it on
// draft status), so this `node` vitest .test.ts is the ONLY thing that can catch an executor
// who wires 6 of 8 helpers before the PR leaves draft. Deliberately a .test.ts, not a
// .spec.ts -- see decision [no-new-e2e-spec-files] / [no-spec-file-count-guard].
import { describe, test, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

const E2E_DIR = join(import.meta.dirname, 'topology')
const read = (file: string) => readFileSync(join(E2E_DIR, file), 'utf-8')

// Locates a named function's body by its `function <name>` anchor, then matches braces from
// the opening `{` to its closing partner. None of the 8 bodies below contain a brace inside a
// string/regex literal, so plain char counting is safe -- re-verify this if a body grows one.
function extractFunctionBody(source: string, fnName: string): string | null {
  const anchor = new RegExp(`(?:async\\s+)?function\\s+${fnName}\\s*\\(`).exec(source)
  if (!anchor) return null
  const braceStart = source.indexOf('{', anchor.index)
  if (braceStart === -1) return null
  let depth = 0
  for (let i = braceStart; i < source.length; i++) {
    if (source[i] === '{') depth++
    else if (source[i] === '}') {
      depth--
      if (depth === 0) return source.slice(braceStart, i + 1)
    }
  }
  return null
}

// The 8 named nav helpers every capability spec funnels through (decision
// [no-new-e2e-spec-files]). NOTE: demo-persona.spec.ts declares its OWN file-local
// `goToInvoices`/`openInvoiceRow` (a different convention, same names) -- those are NOT in
// this list. They're covered as inline sites below instead, per the story's own Files split.
const NAMED_HELPERS = [
  { file: 'persona-surfaces.spec.ts', name: 'goTo' },
  { file: 'roles.spec.ts', name: 'goTo' },
  { file: 'workflows.spec.ts', name: 'goTo' },
  { file: 'invoice-surfaces.spec.ts', name: 'goToInvoices' },
  { file: 'invoice-surfaces.spec.ts', name: 'openInvoiceRow' },
  { file: 'invoice-surfaces.spec.ts', name: 'goToReports' },
  { file: 'audit.spec.ts', name: 'openAudit' },
  { file: 'portfolio.spec.ts', name: 'goToClients' },
]

describe('guard: every named nav helper (AC-1, AC-2, AC-5)', () => {
  test('guard_theWalkFindsTheHelpersItClaimsToScan', () => {
    const bodies = NAMED_HELPERS.map(({ file, name }) => extractFunctionBody(read(file), name))
    // Floor: a broken walk (renamed helper, moved file) must return fewer than 8, never a
    // silent zero read as "clean".
    expect(bodies.length).toBe(8)
    bodies.forEach((body, i) => {
      expect(body, `${NAMED_HELPERS[i].name} in ${NAMED_HELPERS[i].file} was not found`).toBeTruthy()
      expect(body!.length, `${NAMED_HELPERS[i].name}'s body read back empty`).toBeGreaterThan(20)
      // Positive control: every one of these helpers drives a click. A body that lost this
      // would mean the anchor matched the wrong span, not that the guard below is honest.
      expect(body, `${NAMED_HELPERS[i].name}'s body doesn't call .click() -- wrong span extracted`).toMatch(/\.click\(\)/)
    })
  })

  // RED today: no helper asserts toHaveURL yet. This is the point -- Playwright itself
  // can't see that gap before `ready`, so this scan is the only oracle that can.
  test('guard_everyNamedNavHelperAssertsTheUrl', () => {
    const results = NAMED_HELPERS.map(({ file, name }) => ({
      name,
      file,
      body: extractFunctionBody(read(file), name)!,
    }))
    results.forEach(({ name, file, body }) => {
      expect(body, `${name} (${file}) does not assert toHaveURL`).toMatch(/toHaveURL/)
    })
  })
})

// The 8 in-app navigations no helper wraps (re-derived by an independent sweep of
// `aside.pf-sidebar nav.pf-nav-list` and `getByRole('button', { name: /Overview|Invoices|...
// /Settings/ })` across e2e/ -- NOT copied from the plan's line numbers, which have drifted
// before). Located by exact source text + 1-based occurrence index, not by line number: the
// executor's own fix (adding a line after the click) shifts every later line number in the
// same file, so a line-number anchor would break on the very edit it's meant to police.
const INLINE_SITES = [
  { file: 'invoice-surfaces.spec.ts', needle: "await page.getByRole('button', { name: /Clients/ }).click()", occurrence: 1 },
  { file: 'invoice-surfaces.spec.ts', needle: "await page.getByRole('button', { name: /Overview/ }).click()", occurrence: 1 },
  { file: 'invoice-surfaces.spec.ts', needle: "await page.getByRole('button', { name: /Clients/ }).click()", occurrence: 2 },
  { file: 'invoice-surfaces.spec.ts', needle: "await page.getByRole('button', { name: /Customers/ }).click()", occurrence: 1 },
  {
    file: 'demo-persona.spec.ts',
    needle: "await page.locator('aside.pf-sidebar nav.pf-nav-list').getByRole('button', { name: /Invoices/ }).click()",
    occurrence: 1,
  },
  {
    file: 'demo-persona.spec.ts',
    needle: "await page.locator('aside.pf-sidebar nav.pf-nav-list').getByRole('button', { name: 'Settings' }).click()",
    occurrence: 1,
  },
  {
    file: 'demo-persona.spec.ts',
    needle: "await page.locator('aside.pf-sidebar nav.pf-nav-list').getByRole('button', { name: 'Settings' }).click()",
    occurrence: 2,
  },
  {
    file: 'import-wizard.spec.ts',
    needle: "await page.locator('aside.pf-sidebar nav.pf-nav-list').getByRole('button', { name: /Invoices/ }).click()",
    occurrence: 1,
  },
]

function nthIndexOf(haystack: string, needle: string, n: number): number {
  let idx = -1
  for (let i = 0; i < n; i++) {
    idx = haystack.indexOf(needle, idx + 1)
    if (idx === -1) return -1
  }
  return idx
}

// Weaker-but-honest by design, not full precision: this file's own convention blank-lines
// between steps, so "scan forward to the next blank line (capped at 12 lines)" finds the
// assertion that belongs to THIS click without an AST. It cannot tell a toHaveURL asserting
// the WRONG path from a correct one, and a step written without a blank-line break would slip
// past the cap -- every site below was read by hand (see the QA reply) to confirm neither
// applies today.
function windowAfter(source: string, anchorIndex: number, maxLines = 12): string {
  const rest = source.slice(anchorIndex).split('\n')
  const collected: string[] = []
  for (let i = 1; i < rest.length && i <= maxLines; i++) {
    if (rest[i].trim() === '') break
    collected.push(rest[i])
  }
  return collected.join('\n')
}

describe('guard: every inline nav click no helper wraps (AC-3)', () => {
  test('guard_theWalkFindsTheInlineSitesItClaimsToScan', () => {
    const indices = INLINE_SITES.map(({ file, needle, occurrence }) => nthIndexOf(read(file), needle, occurrence))
    // Floor: the independent sweep found exactly 8. A miss (renamed label, moved click) must
    // read as -1 here, never as a silently-passing zero-hit walk.
    expect(indices.length).toBe(8)
    indices.forEach((idx, i) => {
      expect(idx, `inline site #${i} (${INLINE_SITES[i].file}, occurrence ${INLINE_SITES[i].occurrence}) not found`).toBeGreaterThanOrEqual(0)
    })
  })

  // RED today: none of these 8 clicks is followed by a URL assertion yet.
  test('guard_everyInlineNavClickAssertsTheUrl', () => {
    INLINE_SITES.forEach(({ file, needle, occurrence }, i) => {
      const source = read(file)
      const idx = nthIndexOf(source, needle, occurrence)
      const window = windowAfter(source, idx)
      expect(window, `inline site #${i} (${file}, occurrence ${occurrence}) has no toHaveURL within ${12} lines of its click`).toMatch(
        /toHaveURL/,
      )
    })
  })
})
