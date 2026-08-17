// RED-then-GREEN spec for LAND-04-02 (task-556). Transcribes every row of the
// architect's Test Specs table. SSR via renderToStaticMarkup, no jsdom — same
// idiom as DemoModal.render.test.tsx (vitest.config.ts: environment 'node').
//
// Measured SSR facts (task-556): React 19 emits no <!-- --> separators, so
// `{GA_RETENTION_MONTHS} months` renders as the literal "14 months". ASCII `'`
// escapes to &#x27; but the typographic curly quote does not, so every needle
// below is apostrophe-, ampersand- and quote-free.
//
// Adversarial coverage for the claims this file does not pin lives in
// Privacy.claims.test.tsx.
import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { ANALYTICS_DEFAULT_SENTENCE, GA_RETENTION_MONTHS, PRIVACY_CONTACT, PROSE_MAX_WIDTH, Privacy } from './Privacy'
import { CONSENT_TEXT } from './demoForm'
import { CookieNotice } from './CookieNotice'
import { PRODUCTION_HOSTNAMES, submissionUrl } from '../hubspot'
import { CONSENT_DEFAULT_ANALYTICS } from '../consent'

const SRC_DIR = fileURLToPath(new URL('.', import.meta.url))
const PRIVACY_TSX = join(SRC_DIR, 'Privacy.tsx')
const INDEX_HTML = join(SRC_DIR, '..', '..', 'index.html')
const APP_SRC = join(SRC_DIR, '..', '..', '..', 'app', 'src')
const OPS_CONSOLE_SRC = join(SRC_DIR, '..', '..', '..', 'ops-console', 'src')
const SUPPORT_CONSOLE_SRC = join(SRC_DIR, '..', '..', '..', 'support-console', 'src')

// Finds the first tag matching `re`. Uses vitest's own `expect` rather than a
// thrown error so a miss is a failing assertion, not a collection error.
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

// The D7 guard's list, hoisted at LAND-05-03 so the narrowing is assertable rather
// than buried in a loop. It is an instrument, not scaffolding: a preference centre
// still does not exist, and it was proven non-vacuous against a planted hit at
// LAND-05-01's QA. LAND-05-03 narrows it to the terms that stay wrong once the notice
// mounts; `banner` and `Reject` come off only because the rewritten copy names the
// real control.
const FORBIDDEN_WITHDRAWAL_TERMS: readonly string[] = ['preference centre', 'preference center']

// The same loop the guard runs, exposed so the narrowed list can be proven non-vacuous
// against a planted string instead of being trusted.
function firstForbiddenHit(text: string, terms: readonly string[]): string | null {
  for (const term of terms) {
    if (text.includes(term)) return term
  }
  return null
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
    // Same helper the planted-hit control below runs, so the control proves THIS
    // loop still discriminates rather than a lookalike one.
    const hit = firstForbiddenHit(html, FORBIDDEN_WITHDRAWAL_TERMS)
    expect(hit, `withdrawal section mentions "${hit}"`).toBeNull()
  })

  it('D7: the page carries the default-state sentence', () => {
    expect(html).toContain(ANALYTICS_DEFAULT_SENTENCE)
  })

  it('D7 (NEW): the default-state sentence is the reviewed wording', () => {
    // Without this pin, softening the constant changes both sides of the row
    // above and it stays green.
    expect(ANALYTICS_DEFAULT_SENTENCE).toBe('Analytics is off unless you turn it on.')
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


// T1-7 (LAND-05-01). The two pins above compare ANALYTICS_DEFAULT_SENTENCE to
// itself, so the published page can state the opposite of the code's actual
// default and stay green. These tie the prose to CONSENT_DEFAULT_ANALYTICS.
//
// Two-sided and fail-closed on purpose: copy matching neither vocabulary, or
// both, fails rather than passes, so a reword cannot silently decouple the
// disclosure from the gate. It reads polarity, never a particular wording.
const CLAIMS_ON = [
  /\bis on\b/i,
  /\bon unless\b/i,
  /\bon by default\b/i,
  /\benabled by default\b/i,
  /\bturned on\b/i,
  /\bcounted from the moment\b/i,
]

const CLAIMS_OFF = [
  /\bis off\b/i,
  /\boff until\b/i,
  /\boff by default\b/i,
  /\bdisabled by default\b/i,
  /\bnothing is (?:sent|measured|collected|loaded)\b/i,
  /\bonly (?:runs|loads|starts|measures)\b/i,
  /\bonly (?:after|once|if|when) you\b/i,
  /\b(?:until|after) you (?:accept|agree|allow|choose|turn it on)\b/i,
  /\bopt[- ]in\b/i,
]

type DefaultClaim = 'on' | 'off' | 'unreadable' | 'contradictory'

function classifyDefaultClaim(text: string): DefaultClaim {
  const on = CLAIMS_ON.some((re) => re.test(text))
  const off = CLAIMS_OFF.some((re) => re.test(text))
  if (on && off) return 'contradictory'
  if (on) return 'on'
  if (off) return 'off'
  return 'unreadable'
}

describe('T1-7: the published default-state claim tracks CONSENT_DEFAULT_ANALYTICS', () => {
  const html = renderToStaticMarkup(createElement(Privacy))

  it('control: the classifier discriminates and is not answering everything the same way', () => {
    expect(classifyDefaultClaim('Analytics is on unless you turn it off in your browser.')).toBe('on')
    expect(classifyDefaultClaim('Analytics is off until you accept.')).toBe('off')
    expect(classifyDefaultClaim('Nothing is measured until you choose Accept.')).toBe('off')
    expect(classifyDefaultClaim('The demo form posts to HubSpot.')).toBe('unreadable')
    expect(classifyDefaultClaim('Analytics is on by default and off until you accept.')).toBe('contradictory')
  })

  it('control: the sentence being classified is the one the page actually renders', () => {
    expect(html.length).toBeGreaterThan(0)
    expect(html).toContain(ANALYTICS_DEFAULT_SENTENCE)
  })

  it('the page claims analytics is on by default if and only if the code default is granted', () => {
    const claim = classifyDefaultClaim(ANALYTICS_DEFAULT_SENTENCE)
    expect(claim, `no readable default in: "${ANALYTICS_DEFAULT_SENTENCE}"`).not.toBe('unreadable')
    expect(claim, `both defaults claimed in: "${ANALYTICS_DEFAULT_SENTENCE}"`).not.toBe('contradictory')
    expect(
      claim === 'on',
      `page claims "${claim}" by default, CONSENT_DEFAULT_ANALYTICS is ${CONSENT_DEFAULT_ANALYTICS}`,
    ).toBe(CONSENT_DEFAULT_ANALYTICS)
  })

  it('the shipped disclosure does not claim analytics is on by default', () => {
    expect(classifyDefaultClaim(ANALYTICS_DEFAULT_SENTENCE)).toBe('off')
  })
})

// T3-15, T3-17 and T3-18. Oracles only: the published wording still needs the user's
// line-by-line sign-off, so nothing below pins a sentence this file invented. Each spec
// ties the published claim to something the CODE decides — the control's own button
// labels, or the qualifier the page already uses six lines further down.
//
// Privacy renders in ISOLATION here (renderToStaticMarkup of the component, not the
// App tree), so the mounted notice never enters this markup: the guards below trip
// only on the privacy page's own prose, which is the point.

const NOTICE_DENIALS: readonly string[] = [
  'This site has no privacy control of its own yet',
  'no notice, no toggle, no settings page',
  'One that lets you choose is being built',
  'Until it ships',
]

// Wording the page ALREADY uses for the same condition (the cookies section). The
// classifier reads for a consent condition, never for a particular sentence.
const CONSENT_QUALIFIERS: readonly RegExp[] = [
  /\bif you have allowed analytics\b/i,
  /\bonce you have allowed analytics\b/i,
  /\bonly (?:if|once|after|when) you\b/i,
  /\bunless you (?:have )?(?:allowed|accepted|turned it on)\b/i,
  /\bafter you (?:allow|accept|choose)\b/i,
]

function carriesConsentQualifier(text: string): boolean {
  return CONSENT_QUALIFIERS.some((re) => re.test(text))
}

describe('T3-15/T3-17/T3-18: the page describes the control that now exists', () => {
  const html = renderToStaticMarkup(createElement(Privacy))
  const noticeHtml = renderToStaticMarkup(
    createElement(CookieNotice, { current: null, suppressed: false, onChoose: () => undefined }),
  )

  function paragraphContaining(needle: string): string {
    const hits = html.split('</p>').filter((segment) => segment.includes(needle))
    expect(hits.length, `expected exactly one paragraph containing "${needle}"`).toBe(1)
    return hits[0]
  }

  it('control: both renders resolved', () => {
    expect(html.length).toBeGreaterThan(0)
    expect(html).toContain('<h1')
    expect(noticeHtml.length).toBeGreaterThan(0)
    expect(noticeHtml).toContain('cookie-note')
  })

  it('control: the qualifier classifier discriminates on shipped copy', () => {
    // Positive: the cookies section already carries the condition this spec asks the
    // other two sentences to carry. Negative: an unrelated shipped sentence does not.
    expect(carriesConsentQualifier(paragraphContaining('Our own code sets no cookies at all'))).toBe(true)
    expect(carriesConsentQualifier('HubSpot holds all of this on their EU servers.')).toBe(false)
    expect(carriesConsentQualifier('Google measures how this site is used.')).toBe(false)
  })

  it('T3-15 (AC-9): the page no longer denies that a control exists', () => {
    for (const denial of NOTICE_DENIALS) {
      expect(html, `the page still denies the notice: "${denial}"`).not.toContain(denial)
    }
  })

  it('T3-15 (AC-9): the page names the control using the control own labels', () => {
    // Read out of CookieNotice, never retyped — the same technique as C3 (the
    // hostname comes from the allowlist) and D6 (the consent sentence comes from
    // demoForm). Relabel the buttons and this forces the copy to follow.
    const labels = Array.from(noticeHtml.matchAll(/<button[^>]*>([^<]+)<\/button>/g)).map((m) => m[1].trim())
    expect(labels.length, 'the notice rendered no buttons to read labels from').toBeGreaterThan(0)
    expect(labels).toEqual(['Accept', 'Reject'])
    for (const label of labels) {
      expect(html, `the page does not name the "${label}" control`).toContain(label)
    }
  })

  it('T3-17 (AC-10): the lede carries the consent qualifier', () => {
    // Anchored on the sentence Privacy.claims.test.tsx already pins for this
    // paragraph, so the anchor cannot vanish silently.
    const lede = paragraphContaining('Your browser loads nothing on this site from any other company')
    expect(carriesConsentQualifier(lede), `no consent condition in the lede: ${lede}`).toBe(true)
  })

  it('T3-17 (AC-10): the collection-endpoint sentence carries the consent qualifier', () => {
    const endpoint = paragraphContaining('region1.google-analytics.com')
    expect(carriesConsentQualifier(endpoint), `no consent condition in: ${endpoint}`).toBe(true)
  })

  it('T3-18 (AC-11): the forbidden-substring guard is narrowed, not deleted', () => {
    expect(FORBIDDEN_WITHDRAWAL_TERMS).toEqual(['preference centre', 'preference center'])
  })

  it('T3-18 (AC-11): the narrowed guard still finds a planted hit', () => {
    // Non-vacuity, through the SAME loop the guard runs. Without this the narrowing
    // could go all the way to an empty list and the D7 row would stay green.
    const narrowed = ['preference centre', 'preference center']
    expect(firstForbiddenHit('Manage this in our preference centre at any time.', narrowed)).toBe('preference centre')
    expect(firstForbiddenHit('Open the preference center to change it.', narrowed)).toBe('preference center')
    expect(firstForbiddenHit('There is a cookie notice with Accept and Reject.', narrowed)).toBeNull()
  })
})
