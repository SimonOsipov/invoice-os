// `node` environment (vitest.config.ts default) -- a static source scan, no DOM needed.
//
// ROUTE-01-05 AC-3, decision [one-writer-rule]. The seam's writers (route.ts, and App.tsx's
// navigate(), mount alignment, setInvoiceQuery, searchInvoices, setAuditInvoiceFilter,
// setSettingsTab, switchClient and -- since ROUTE-03-03 scopes it to the create view --
// the review-path mirror) must never read location.search -- that is what makes "never
// echo the query string" structural rather than remembered.
// App.tsx's `?persona=` strip is the deliberate, permanent counter-example: it DOES read
// location.search, and this file uses it as the control needle proving the scan can see a
// match at all (a typo'd regex reports a clean zero exactly like a real zero).

import { readFileSync } from 'node:fs'
import path from 'node:path'

import { describe, expect, it } from 'vitest'

function readSrc(relPath: string): string {
  return readFileSync(path.join(process.cwd(), relPath), 'utf8')
}

// The one helper both the needle and the absence assertions call, so the needle exercises
// the exact same code path as the checks it is meant to validate.
function containsLocationSearch(text: string): boolean {
  return /location\.search/.test(text)
}

// Brace-counting body extractor: a plain string search for the closing `}` would stop at
// the first one, which is wrong the moment a body contains a nested block. The
// mount-alignment body nests routeUrl's params object (ROUTE-04-02).
function bracedBodyFrom(src: string, openBraceIdx: number): string {
  let depth = 0
  let i = openBraceIdx
  for (; i < src.length; i++) {
    if (src[i] === '{') depth++
    else if (src[i] === '}') {
      depth--
      if (depth === 0) break
    }
  }
  expect(i, 'no matching closing brace found from the given offset').toBeLessThan(src.length)
  return src.slice(openBraceIdx + 1, i)
}

// Finds `marker` at or after `fromIndex`, then extracts the braced body of the FIRST `{`
// that follows it.
function findBody(src: string, marker: string, fromIndex = 0): string {
  const markerIdx = src.indexOf(marker, fromIndex)
  expect(markerIdx, `marker not found: ${JSON.stringify(marker)}`).toBeGreaterThan(-1)
  const braceIdx = src.indexOf('{', markerIdx)
  expect(braceIdx, `no opening brace found after marker: ${JSON.stringify(marker)}`).toBeGreaterThan(-1)
  return bracedBodyFrom(src, braceIdx)
}

describe('AC-3: the seam writers never read location.search', () => {
  it('guard_theSeamsWriterNeverReadsLocationSearch', () => {
    const routeSrc = readSrc('src/lib/route.ts')
    const appSrc = readSrc('src/App.tsx')

    // ROUTE-04-03: the anchor carries the comma, never the closing paren -- navigate now
    // takes `params?: RouteParams`, and `'function navigate(view: View)'` would no longer
    // be found. The comma also pins that a second parameter still exists.
    const navigateBody = findBody(appSrc, 'function navigate(view: View,')
    const alignmentCommentIdx = appSrc.indexOf('Aligns a boot URL that named no path')
    expect(alignmentCommentIdx, 'mount-alignment anchor comment not found -- App.tsx was restructured').toBeGreaterThan(-1)
    const alignmentBody = findBody(appSrc, 'useEffect(() => {', alignmentCommentIdx)
    const mirrorAnchorIdx = appSrc.indexOf('the WRITE half')
    expect(mirrorAnchorIdx, 'review mirror anchor comment not found -- App.tsx was restructured').toBeGreaterThan(-1)
    const mirrorBody = findBody(appSrc, 'useEffect(() => {', mirrorAnchorIdx)

    // Floor: a broken anchor search silently returning an empty population would make the
    // loop below vacuously pass with nothing checked. setInvoiceQuery is in the population
    // because it is the one new writer that builds its own replaceState URL, and "clear the
    // query" is naturally written as "take the current URL and strip q=". switchClient is
    // in the population because it is a writer (the leaving-view scrub) that reads
    // location.hash, never location.search -- ROUTE-02. The review mirror joins here too
    // (ROUTE-03-03): scoped to `view === 'create'` only, it is no longer the deliberate
    // counter-example -- the `?persona=` strip below takes over that role.
    const writerBodies = [
      { name: 'lib/route.ts (whole file)', body: routeSrc },
      { name: "App.tsx's navigate()", body: navigateBody },
      { name: "App.tsx's mount-alignment effect", body: alignmentBody },
      { name: "App.tsx's setInvoiceQuery()", body: findBody(appSrc, 'function setInvoiceQuery(q: string)') },
      { name: "App.tsx's searchInvoices()", body: findBody(appSrc, 'function searchInvoices(q: string)') },
      { name: "App.tsx's setAuditInvoiceFilter()", body: findBody(appSrc, 'function setAuditInvoiceFilter(') },
      // Keeps the leading `function `: 'setSettingsTab' alone matches the ctx object
      // literal first, and findBody would extract a slice of that object instead.
      { name: "App.tsx's setSettingsTab()", body: findBody(appSrc, 'function setSettingsTab(t: SettingsTab)') },
      { name: "App.tsx's switchClient()", body: findBody(appSrc, 'function switchClient(id: string)') },
      { name: "App.tsx's review-path mirror", body: mirrorBody },
    ]
    expect(writerBodies.length, 'the writer population must not be empty').toBe(9)
    for (const { name, body } of writerBodies) {
      expect(body.length, `${name}'s extracted body is empty -- the anchor is broken`).toBeGreaterThan(0)
      expect(containsLocationSearch(body), `${name} must never read location.search`).toBe(false)
    }

    // Control needle: the `?persona=` strip is the deliberate, permanent counter-example
    // (decision [one-writer-rule]) that proves the scan is capable of seeing a match.
    // Anchored on its own comment, not on the `URLSearchParams(...)` call text itself --
    // that same call also appears at the seat-token strip (App.tsx:1619), so anchoring on
    // the call would risk extracting the wrong body if either effect moves.
    const personaStripAnchorIdx = appSrc.indexOf('Drop the consumed ?persona= from the URL')
    expect(personaStripAnchorIdx, 'persona-strip anchor comment not found -- App.tsx was restructured').toBeGreaterThan(-1)
    const personaStripBody = findBody(appSrc, 'useEffect(() => {', personaStripAnchorIdx)
    expect(personaStripBody.length, 'persona-strip control body is empty -- the anchor is broken').toBeGreaterThan(0)
    expect(
      containsLocationSearch(personaStripBody),
      'control needle: the persona strip must still read location.search, or the absence checks above prove nothing',
    ).toBe(true)
    // A second, narrower assertion on the same body: subtask 05 removes this effect's own
    // `+ window.location.hash` append, which must not be mistaken for removing the read
    // this needle actually pins.
    expect(
      personaStripBody.includes('URLSearchParams(window.location.search)'),
      'the needle pins the query READ, not the fragment append -- it must survive 05\'s removal',
    ).toBe(true)
  })
})
