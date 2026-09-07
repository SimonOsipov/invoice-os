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
    const backfillAnchorIdx = appSrc.indexOf("Backfills the boot entry's stamp once the portfolio resolves")
    expect(backfillAnchorIdx, 'stamp-backfill anchor comment not found -- App.tsx was restructured').toBeGreaterThan(-1)
    const backfillBody = findBody(appSrc, 'useEffect(() => {', backfillAnchorIdx)

    // Floor: a broken anchor search silently returning an empty population would make the
    // loop below vacuously pass with nothing checked. setInvoiceQuery is in the population
    // because it is the one new writer that builds its own replaceState URL, and "clear the
    // query" is naturally written as "take the current URL and strip q=". switchClient is
    // in the population because it is a writer (the leaving-view scrub) -- ROUTE-02. The
    // review mirror joins here too (ROUTE-03-03): scoped to `view === 'create'` only, it is
    // no longer the deliberate counter-example -- the `?persona=` strip below takes over
    // that role.
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
      // ROUTE-06-02. A state-only stamp writer joins the population so a later
      // location.search read inside it fails; nothing else scans this body.
      { name: "App.tsx's stamp backfill", body: backfillBody },
    ]
    expect(writerBodies.length, 'the writer population must not be empty').toBe(10)
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
    // A second, narrower assertion on the same body: the fragment append this subtask
    // removes from the same effect body must not be mistaken for removing the read this
    // needle actually pins.
    expect(
      personaStripBody.includes('URLSearchParams(window.location.search)'),
      'the needle pins the query READ, not the fragment append -- it must survive 05\'s removal',
    ).toBe(true)
  })
})

// ROUTE-03-05 AC-3/AC-4. Deliberately duplicates the anchor extraction above rather than
// sharing it: the two tests must be independently falsifiable, and a shared helper would
// let a broken anchor take both down silently instead of pointing at which scan failed.
describe('ROUTE-03-05 AC-3: no writer in the population appends the fragment', () => {
  // Concatenated, not a literal: a literal would make this scanner's own source match
  // itself, so the shell AC-1 grep could never return a true zero.
  const LOCATION_HASH = 'location' + '.hash'
  function containsLocationHash(text: string): boolean {
    return text.includes(LOCATION_HASH)
  }

  it('guard_noAppWriterAppendsTheFragment', () => {
    const routeSrc = readSrc('src/lib/route.ts')
    const appSrc = readSrc('src/App.tsx')

    const navigateBody = findBody(appSrc, 'function navigate(view: View,')
    const alignmentCommentIdx = appSrc.indexOf('Aligns a boot URL that named no path')
    expect(alignmentCommentIdx, 'mount-alignment anchor comment not found -- App.tsx was restructured').toBeGreaterThan(-1)
    const alignmentBody = findBody(appSrc, 'useEffect(() => {', alignmentCommentIdx)
    const mirrorAnchorIdx = appSrc.indexOf('the WRITE half')
    expect(mirrorAnchorIdx, 'review mirror anchor comment not found -- App.tsx was restructured').toBeGreaterThan(-1)
    const mirrorBody = findBody(appSrc, 'useEffect(() => {', mirrorAnchorIdx)
    const backfillAnchorIdx = appSrc.indexOf("Backfills the boot entry's stamp once the portfolio resolves")
    expect(backfillAnchorIdx, 'stamp-backfill anchor comment not found -- App.tsx was restructured').toBeGreaterThan(-1)
    const backfillBody = findBody(appSrc, 'useEffect(() => {', backfillAnchorIdx)

    // Same ten bodies guard_theSeamsWriterNeverReadsLocationSearch scans above. This
    // population structurally EXCLUDES signOut and the persona strip -- neither is a
    // member -- so it is NOT the oracle for those two removal sites; the whole-file scan
    // (lib/route.test.ts's guard_noReviewHashSurvivesInTheApp) covers those.
    const writerBodies = [
      { name: 'lib/route.ts (whole file)', body: routeSrc },
      { name: "App.tsx's navigate()", body: navigateBody },
      { name: "App.tsx's mount-alignment effect", body: alignmentBody },
      { name: "App.tsx's setInvoiceQuery()", body: findBody(appSrc, 'function setInvoiceQuery(q: string)') },
      { name: "App.tsx's searchInvoices()", body: findBody(appSrc, 'function searchInvoices(q: string)') },
      { name: "App.tsx's setAuditInvoiceFilter()", body: findBody(appSrc, 'function setAuditInvoiceFilter(') },
      { name: "App.tsx's setSettingsTab()", body: findBody(appSrc, 'function setSettingsTab(t: SettingsTab)') },
      { name: "App.tsx's switchClient()", body: findBody(appSrc, 'function switchClient(id: string)') },
      { name: "App.tsx's review-path mirror", body: mirrorBody },
      // ROUTE-06-02. A state-only stamp writer joins the population so a later
      // location.search read inside it fails; nothing else scans this body.
      { name: "App.tsx's stamp backfill", body: backfillBody },
    ]
    expect(writerBodies.length, 'the writer population must not be empty').toBe(10)
    for (const { name, body } of writerBodies) {
      expect(body.length, `${name}'s extracted body is empty -- the anchor is broken`).toBeGreaterThan(0)
      expect(containsLocationHash(body), `${name} must never append a location fragment`).toBe(false)
    }
  })

  // AC-4's positive control: the persona strip is excluded from the population above, so
  // this is the only in-suite proof that removing ITS OWN fragment append leaves its
  // location.search read (routeWriterGuard's control needle) still matching.
  it('guard_theControlNeedleSurvivesTheFragmentRemoval', () => {
    const appSrc = readSrc('src/App.tsx')
    const personaStripAnchorIdx = appSrc.indexOf('Drop the consumed ?persona= from the URL')
    expect(personaStripAnchorIdx, 'persona-strip anchor comment not found -- App.tsx was restructured').toBeGreaterThan(-1)
    const personaStripBody = findBody(appSrc, 'useEffect(() => {', personaStripAnchorIdx)
    expect(personaStripBody.length, 'persona-strip control body is empty -- the anchor is broken').toBeGreaterThan(0)
    expect(
      personaStripBody.includes('URLSearchParams(window.location.search)'),
      'control needle: the persona strip must still read location.search after its fragment append is removed',
    ).toBe(true)
    expect(
      containsLocationHash(personaStripBody),
      'the persona strip must no longer append a location fragment',
    ).toBe(false)
  })
})
