// QA gap-fill. The <meta name="description"> restates the hero's first sentence, reaches no
// rendered React tree, and is invisible to a screenshot — this is its only oracle short of a
// browser read on the deployed build.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const INDEX_HTML = fileURLToPath(new URL('../index.html', import.meta.url))

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
  // fails here instead of silently falsifying two docs.
  it('the Google Fonts tags stay on lines 13-16', () => {
    const lines = readFileSync(INDEX_HTML, 'utf8').split('\n')
    expect(lines[12], 'line 13').toContain('rel="preconnect" href="https://fonts.googleapis.com"')
    expect(lines[13], 'line 14').toContain('rel="preconnect" href="https://fonts.gstatic.com"')
    expect(lines[14], 'line 15').toContain('rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Inter')
    expect(lines[15], 'line 16').toContain('rel="stylesheet" href="https://fonts.googleapis.com/css2?family=IBM+Plex+Mono')
  })
})
