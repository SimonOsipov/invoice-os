// RED-then-GREEN spec for LAND-04-02 (task-556). Transcribes every row of the
// architect's Test Specs table. SSR via renderToStaticMarkup, no jsdom — same
// idiom as DemoModal.render.test.tsx (vitest.config.ts: environment 'node').
//
// Measured SSR facts (task-556): React 19 emits no <!-- --> separators, so
// `{GA_RETENTION_MONTHS} months` renders as the literal "14 months". ASCII `'`
// escapes to &#x27; but the typographic curly quote does not, so every needle
// below is apostrophe-, ampersand- and quote-free.
//
// Privacy.tsx is currently a stub (placeholder constants, `<div />` body), so
// most rows below are expected to fail on their target assertion, not on
// import/collection — that is the point of this commit.
import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { ANALYTICS_DEFAULT_SENTENCE, GA_RETENTION_MONTHS, PRIVACY_CONTACT, PROSE_MAX_WIDTH, Privacy } from './Privacy'
import { CONSENT_TEXT } from './demoForm'
import { PRODUCTION_HOSTNAMES, submissionUrl } from '../hubspot'

const SRC_DIR = fileURLToPath(new URL('.', import.meta.url))
const PRIVACY_TSX = join(SRC_DIR, 'Privacy.tsx')
const INDEX_HTML = join(SRC_DIR, '..', '..', 'index.html')
const APP_SRC = join(SRC_DIR, '..', '..', '..', 'app', 'src')
const OPS_CONSOLE_SRC = join(SRC_DIR, '..', '..', '..', 'ops-console', 'src')
const SUPPORT_CONSOLE_SRC = join(SRC_DIR, '..', '..', '..', 'support-console', 'src')

// Finds the first tag matching `re`. Uses vitest's own `expect` rather than a
// thrown error so a miss is a failing assertion, not a collection error —
// what makes the currently-stubbed rows below a valid RED.
function extractTag(html: string, re: RegExp): string {
  const match = html.match(re)
  expect(match, `expected to find a tag matching ${re}`).not.toBeNull()
  return match![0]
}

// Same recursive walk as frontend/app/src/envPosture.test.ts. Test files are
// excluded — they would carry the forbidden strings as fixtures.
function sourceFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) out.push(...sourceFiles(path))
    else if (/\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name)) out.push(path)
  }
  return out
}

describe('Privacy SSR render (LAND-04-02)', () => {
  const html = renderToStaticMarkup(createElement(Privacy))

  it('D4: both processors are named', () => {
    expect(html).toContain('Google')
    expect(html).toContain('HubSpot')
  })

  it('D4: each processors location is stated', () => {
    expect(html).toContain('United States')
    expect(html).toContain('EU servers')
  })

  it('C7: the US claim is about processing, not the collection endpoint', () => {
    expect(html).toContain('Google LLC')
    expect(html).toContain('United States')
    expect(html).not.toContain('directly to servers')
  })

  it('C7 (NEW): the regional collection host is named', () => {
    expect(html).toContain('region1.google-analytics.com')
  })

  it('D5: the retention figure is the exported constant', () => {
    expect(html).toContain(`${GA_RETENTION_MONTHS} months`)
  })

  it('D5: the retention figure appears exactly once', () => {
    const matches = html.match(/\d+ months/g) ?? []
    expect(matches.length).toBe(1)
  })

  it('C8 (NEW): the retention constant is the operator-confirmed value', () => {
    // Non-vacuous half: the two rows above pass whatever this constant says.
    expect(GA_RETENTION_MONTHS).toBe(14)
  })

  it('D6: the consent sentence is quoted from demoForm, not retyped', () => {
    expect(html).toContain(CONSENT_TEXT)
  })

  it('D6: the component imports CONSENT_TEXT rather than duplicating it', () => {
    const src = readFileSync(PRIVACY_TSX, 'utf8')
    expect(src).toMatch(/import\s*\{[^}]*CONSENT_TEXT[^}]*\}\s*from\s*['"]\.\/demoForm['"]/)
  })

  it('D7: the withdrawal section names only mechanisms that exist', () => {
    expect(html).toContain('tools.google.com/dlpage/gaoptout')
    for (const forbidden of ['banner', 'Reject', 'preference centre', 'preference center']) {
      expect(html, `withdrawal section mentions "${forbidden}"`).not.toContain(forbidden)
    }
  })

  it('D7: the page says analytics is on until the visitor blocks it', () => {
    expect(html).toContain(ANALYTICS_DEFAULT_SENTENCE)
  })

  it('D7 (NEW): the analytics-on-by-default sentence is the reviewed wording', () => {
    // Without this pin, softening the constant changes both sides of the row
    // above and it stays green.
    expect(ANALYTICS_DEFAULT_SENTENCE).toBe('Analytics is on unless you turn it off in your browser.')
  })

  it('W4 (NEW): the opt-out is a real link, not just text', () => {
    expect(html).toContain('href="https://tools.google.com/dlpage/gaoptout"')
  })

  it('D13: the mock sign-in personas address never reaches the page (PERMANENT — never delete)', () => {
    expect(html).not.toContain('e.iroha@ascomply.com')
  })

  it('D13 (FLIPPED): the supplied contact address is on the page', () => {
    expect(html).toContain('sam@ascomply.com')
  })

  it('D13 (FLIPPED): the contact is a working mailto link built from the constant', () => {
    expect(html).toContain('href="mailto:sam@ascomply.com"')
  })

  it('C19 (NEW): the contact constant is the address the user supplied', () => {
    expect(PRIVACY_CONTACT).toBe('sam@ascomply.com')
  })

  it('C19 (NEW): the placeholder token never ships', () => {
    const src = readFileSync(PRIVACY_TSX, 'utf8')
    expect(src).not.toContain('NOT_YET_SUPPLIED')
  })

  it('D12: the prose measure is still the reviewed value', () => {
    expect(PROSE_MAX_WIDTH).toBe(720)
  })

  it('D12: the declared measure is published to the DOM for the browser spec', () => {
    const tag = extractTag(html, /<[^>]*data-testid="privacy-prose"[^>]*>/)
    expect(tag).toContain('data-prose-max="720"')
  })

  it('D14: both locators the browser spec depends on are present, exactly once each', () => {
    const containerMatches = html.match(/data-testid="privacy-container"/g) ?? []
    const proseMatches = html.match(/data-testid="privacy-prose"/g) ?? []
    expect(containerMatches.length, 'privacy-container testid missing').toBe(1)
    expect(proseMatches.length, 'privacy-prose testid missing').toBe(1)
  })

  it('C10: the font flow is disclosed', () => {
    expect(html).toContain('fonts')
    expect(html).toContain('no gate in front of it')
  })

  it('C10 (NEW): the fonts host named on the page is the host the site requests', () => {
    const indexHtml = readFileSync(INDEX_HTML, 'utf8')
    expect(indexHtml).toContain('fonts.googleapis.com')
    expect(html).toContain('fonts.googleapis.com')
  })

  it('C12: every answer the demo form sends is enumerated', () => {
    for (const term of ['work email', 'company', 'role', 'taxpayer size', 'monthly invoice volume']) {
      expect(html, `missing "${term}"`).toContain(term)
    }
  })

  it('C3 (NEW): the hostname on the page is the one the gate allowlists', () => {
    // Load-bearing: a second allowlisted host would make "and nowhere else" false.
    expect(PRODUCTION_HOSTNAMES.length, 'PRODUCTION_HOSTNAMES must have exactly one entry').toBe(1)
    expect(html).toContain(PRODUCTION_HOSTNAMES[0])
  })

  it('C15 (NEW): the HubSpot region named on the page is the region the code posts to', () => {
    expect(submissionUrl({ portalId: 'p', formGuid: 'g' })).toContain('api-eu1')
    expect(html).toContain('EU servers')
  })

  it('D8: the component logs nothing', () => {
    const src = readFileSync(PRIVACY_TSX, 'utf8')
    expect(src).not.toContain('console.')
  })

  it('control: non-vacuous render', () => {
    expect(html.length).toBeGreaterThan(0)
    expect(html).toContain('<h1')
  })
})

// The one cross-package assertion — it lives here because this is where the
// no-analytics-outside-landing claim is published to the public. Independent
// of Privacy.tsx's own content, so it does not require the GREEN commit.
describe('C2 (NEW): no analytics reference exists in the three signed-in SPAs', () => {
  const FORBIDDEN = ['gtag', 'googletagmanager', 'google-analytics', 'vite_ga_']
  // Present in every package's main.tsx — proves the scan actually reads file
  // contents rather than silently matching nothing.
  const CONTROL_NEEDLE = 'createroot'

  it.each([
    ['frontend/app/src', APP_SRC],
    ['frontend/ops-console/src', OPS_CONSOLE_SRC],
    ['frontend/support-console/src', SUPPORT_CONSOLE_SRC],
  ])('%s carries no analytics reference', (label, dir) => {
    const files = sourceFiles(dir)
    expect(files.length, `${label}: scan found nothing to read`).toBeGreaterThanOrEqual(20)

    const combined = files.map((f) => readFileSync(f, 'utf8').toLowerCase()).join('\n')
    expect(combined, `${label}: control needle "createRoot" not found — scan is not reading files`).toContain(
      CONTROL_NEEDLE,
    )
    for (const needle of FORBIDDEN) {
      expect(combined, `${label} references "${needle}"`).not.toContain(needle)
    }
  })
})
