// `node` environment (vitest.config.ts default) -- a static source scan, no DOM needed.
//
// ROUTE-01-05 AC-3, decision [one-writer-rule]. The seam's two writers (route.ts, and
// App.tsx's navigate() + the mount-alignment effect) must never read location.search --
// that is what makes "never echo the query string" structural rather than remembered.
// App.tsx:543's review-hash mirror is the deliberate, untouched counter-example: it DOES
// read location.search, and this file uses it as the control needle proving the scan can
// see a match at all (a typo'd regex reports a clean zero exactly like a real zero).

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
// the first one, which is wrong the moment a body contains a nested block. Neither body
// scanned below nests one today, but the extractor does not assume that.
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

    const navigateBody = findBody(appSrc, 'function navigate(view: View, id: string | null = null)')
    const alignmentCommentIdx = appSrc.indexOf('Aligns a boot URL that named no path')
    expect(alignmentCommentIdx, 'mount-alignment anchor comment not found -- App.tsx was restructured').toBeGreaterThan(-1)
    const alignmentBody = findBody(appSrc, 'useEffect(() => {', alignmentCommentIdx)

    // Floor: a broken anchor search silently returning an empty population would make the
    // loop below vacuously pass with nothing checked.
    const writerBodies = [
      { name: 'lib/route.ts (whole file)', body: routeSrc },
      { name: "App.tsx's navigate()", body: navigateBody },
      { name: "App.tsx's mount-alignment effect", body: alignmentBody },
    ]
    expect(writerBodies.length, 'the writer population must not be empty').toBe(3)
    for (const { name, body } of writerBodies) {
      expect(body.length, `${name}'s extracted body is empty -- the anchor is broken`).toBeGreaterThan(0)
      expect(containsLocationSearch(body), `${name} must never read location.search`).toBe(false)
    }

    // Control needle: the review-hash mirror is the deliberate, untouched counter-example
    // (decision [one-writer-rule]) that proves the scan is capable of seeing a match.
    const reviewMirrorAnchorIdx = appSrc.indexOf('the WRITE half')
    expect(reviewMirrorAnchorIdx, 'review-mirror anchor comment not found -- App.tsx was restructured').toBeGreaterThan(-1)
    const reviewMirrorBody = findBody(appSrc, 'useEffect(() => {', reviewMirrorAnchorIdx)
    expect(reviewMirrorBody.length, 'review-mirror control body is empty -- the anchor is broken').toBeGreaterThan(0)
    expect(
      containsLocationSearch(reviewMirrorBody),
      'control needle: App.tsx:543 must still read location.search, or the absence checks above prove nothing',
    ).toBe(true)
  })
})
