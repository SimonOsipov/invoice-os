// QA gap-fill. The <meta name="description"> restates the hero's first sentence, reaches no
// rendered React tree, and is invisible to a screenshot — this is its only oracle short of a
// browser read on the deployed build.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const INDEX_HTML = fileURLToPath(new URL('../index.html', import.meta.url))
const TYPOGRAPHY_CSS = fileURLToPath(
  new URL('../../../packages/design-tokens/v2/tokens/typography.css', import.meta.url),
)

function familyParams(html: string): string[] {
  return [...html.replaceAll('&amp;', '&').matchAll(/[?&]family=([^&"'\s]+)/g)].map((m) => m[1])
}

const META_DESCRIPTION =
  "ASComply Africa is the solution between your business and Nigeria's Merchant Buyer Solution. Create, validate, approve, archive, and transmit compliant invoices."

describe('index.html head copy', () => {
  it('the meta description names ASComply the solution, not a layer', () => {
    const html = readFileSync(INDEX_HTML, 'utf8')
    const match = html.match(/name="description"\s*\n\s*content="([^"]*)"/)
    expect(match, 'no <meta name="description"> with a content attribute').toBeTruthy()
    expect(match![1]).toBe(META_DESCRIPTION)
  })

  // docs/privacy-policy-claims.md and docs/analytics.md both cite the font tags by line
  // number, and nothing re-derives those ranges. Pin the lines so a reflow of this head
  // fails here instead of silently falsifying two docs. Cited as lines 11-13.
  it('the Google Fonts tags stay on lines 11-13 and request Manrope', () => {
    const importUrl = /@import\s+url\(\s*['"]([^'"]+)['"]\s*\)/.exec(
      readFileSync(TYPOGRAPHY_CSS, 'utf8').replace(/\/\*[\s\S]*?\*\//g, ''),
    )?.[1]
    expect(importUrl, 'typography.css has an @import url()').toContain('family=Manrope')

    const lines = readFileSync(INDEX_HTML, 'utf8').split('\n')
    expect(lines[10], 'line 11').toContain('rel="preconnect" href="https://fonts.googleapis.com"')
    expect(lines[11], 'line 12').toContain('rel="preconnect" href="https://fonts.gstatic.com" crossorigin')
    const href = /<link rel="stylesheet" href="([^"]+)"/.exec(lines[12])?.[1]
    expect(href, 'line 13 is a stylesheet link').toBeTruthy()
    expect(href!.replaceAll('&amp;', '&'), 'line 13 href equals the typography.css @import URL').toBe(importUrl)
  })

  it('index.html requests one font family, Manrope 400-800', () => {
    const planted = familyParams('<link href="https://x.test/css2?family=A:wght@1&amp;family=B+C&amp;display=swap">')
    expect(planted, 'control: every family= parameter is collected, display= is not').toEqual(['A:wght@1', 'B+C'])

    const families = familyParams(readFileSync(INDEX_HTML, 'utf8').replace(/<!--[\s\S]*?-->/g, ''))
    expect(families).toEqual(['Manrope:wght@400;500;600;700;800'])
  })
})
