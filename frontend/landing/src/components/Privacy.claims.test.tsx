// Adversarial coverage for LAND-04-02, added at QA after a mutation sweep of
// Privacy.render.test.tsx: deleting whole ledger claims from the page (C2, C6,
// C9, C13/E1, C16, C17), dropping a withdrawal qualification, retyping the
// hostname, or letting the rendered measure diverge from data-prose-max all
// left that file green.
//
// Needles are apostrophe-, ampersand- and quote-free: SSR escapes ASCII ' to
// &#x27; (Privacy.render.test.tsx header).
import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { PROSE_MAX_WIDTH, Privacy } from './Privacy'
import { Footer } from './Footer'
import { MARKETING_CONSENT_TEXT } from './MarketingConsent'
import { PRODUCT_EMAIL_NOTICE } from './RegisterModal'
import { sentryOptions } from '@invoice-os/monitoring'

const SRC_DIR = fileURLToPath(new URL('.', import.meta.url))
const PRIVACY_TSX = join(SRC_DIR, 'Privacy.tsx')

const html = renderToStaticMarkup(createElement(Privacy))

// One row per ledger claim that had no render assertion.
const LEDGER_NEEDLES: readonly (readonly [string, string])[] = [
  ['C1 consent-gated', 'It runs only if you have allowed analytics'],
  ['C2 not in the signed-in product', 'There is no analytics code anywhere inside the signed-in ASComply product'],
  ['C5 no identifying detail', 'We never send Google your name, your email address, your company'],
  ['C6 two cookies', 'Google Analytics sets two cookies on your device'],
  ['C6 the _ga names', 'One is named _ga; the other starts _ga_'],
  ['C7 transfer out of Nigeria and the EEA', 'outside Nigeria and outside the EEA'],
  ['C9 Google Signals off', 'We have Google Signals turned off'],
  ['C11 HubSpot receives the answers', 'Booking a demo sends your answers to HubSpot'],
  ['C12 the name is split', 'split from the single full name you typed'],
  ['C13 empties are dropped', 'Anything left empty is not sent at all'],
  ['C16 no browsing history to HubSpot', 'We do not send HubSpot your browsing history, the pages you visited'],
  ['C17 no advertising network', 'We run no advertising network on this site'],
  ['C18 the notice is the control', 'The cookie notice on this site is where you choose'],
  ['E1 optional answers are pre-selected', 'come with an answer already selected when the form opens'],
  ['E2 the separate marketing box is quoted from code', MARKETING_CONSENT_TEXT],
  ['E3 Google sets the _ga cookies', 'These two are set by Google, not by our own code'],
  ['lede: no third company', 'Your browser loads nothing on this site from, and sends nothing to, any other company'],
]

// AC7: each mechanism must state what it does NOT stop. Deleting either
// qualification left Privacy.render.test.tsx green.
const WITHDRAWAL_NEEDLES: readonly (readonly [string, string])[] = [
  ['W3 lead-in', 'Block or clear cookies for this site'],
  // The condition is load-bearing: unqualified, this claim is false under the
  // denied default (consent.ts CONSENT_DEFAULT_ANALYTICS), because no tag loads.
  [
    'W3 does not stop the measurement, once analytics is allowed',
    'If you have allowed analytics, it does not stop the measurement itself',
  ],
  // Without this the denied branch could be deleted and every other row stays green.
  [
    'W3 there is nothing to stop, while analytics is not allowed',
    'If you have not allowed analytics, Google is not measuring you at all, so there is nothing here to stop.',
  ],
  ['W4 does not stop fonts or the form', 'It does not affect the fonts and it does not affect the demo form'],
  ['W5 lead-in', 'Block googletagmanager.com with a content blocker'],
  ['W5 does not stop the fonts', 'It does not stop the fonts, which come from a different Google host'],
  ['W7 none of the four stops the fonts', 'None of the four stops Google Fonts'],
]

describe('AC1: every ledger claim survives on the page', () => {
  it.each(LEDGER_NEEDLES)('%s', (_id, needle) => {
    expect(html).toContain(needle)
  })
})

describe('AC7: each withdrawal mechanism states what it does not stop', () => {
  it.each(WITHDRAWAL_NEEDLES)('%s', (_id, needle) => {
    expect(html).toContain(needle)
  })
})

describe('AC9: the declared measure is the rendered measure', () => {
  it('the prose element carries max-width equal to PROSE_MAX_WIDTH, not just the data attribute', () => {
    // A mutation setting maxWidth to 900 while data-prose-max stayed 720 kept
    // every existing row green — LAND-04-05 compares the two in a browser.
    const tag = html.match(/<[^>]*data-testid="privacy-prose"[^>]*>/)?.[0]
    expect(tag, 'privacy-prose tag not found').toBeDefined()
    expect(tag).toContain(`max-width:${PROSE_MAX_WIDTH}px`)
    expect(tag).toContain(`data-prose-max="${PROSE_MAX_WIDTH}"`)
  })

  it('the prose block is nested inside the container, not a sibling', () => {
    // LAND-04-05 measures the prose column inside the container; swapping the
    // two testids keeps the presence assertions green.
    const container = html.indexOf('data-testid="privacy-container"')
    const prose = html.indexOf('data-testid="privacy-prose"')
    expect(container).toBeGreaterThan(-1)
    expect(prose).toBeGreaterThan(container)
    expect(html.slice(container, prose)).not.toContain('</div>')
  })
})

describe('AC1: the hostname is imported, never retyped', () => {
  it('Privacy.tsx imports PRODUCTION_HOSTNAMES and carries no hostname literal', () => {
    const src = readFileSync(PRIVACY_TSX, 'utf8')
    expect(src).toMatch(/import\s*\{[^}]*PRODUCTION_HOSTNAMES[^}]*\}\s*from\s*['"]\.\.\/hubspot['"]/)
    expect(src, 'the hostname is retyped instead of read from the allowlist').not.toContain('www.ascomply.com')
  })
})

describe('mobile: nothing can force horizontal scroll', () => {
  it('the page declares only max-widths, never a fixed width', () => {
    // 390px viewport - 32px container padding either side = 326px of content.
    // A `width:` would ignore that; a `max-width:` cannot.
    expect(html.match(/[^-]width:/g) ?? []).toHaveLength(0)
  })
})

describe('outline', () => {
  it('one h1 and no h3 — LAND-04-03 swaps out the sites only other h1', () => {
    expect(html.match(/<h1/g) ?? []).toHaveLength(1)
    expect(html.match(/<h3/g) ?? []).toHaveLength(0)
    expect((html.match(/<h2/g) ?? []).length).toBeGreaterThan(0)
  })
})

// T3-16, T3-19 and the docs sweep the mount falsifies. Oracles only: none of them
// pins wording this file invented.

describe('T3-16 (AC-9): the C18 needle is retargeted, not dropped', () => {
  it('a C18 row still exists and no longer pins a denial', () => {
    expect(LEDGER_NEEDLES.length, 'the ledger needle table shrank').toBeGreaterThanOrEqual(17)
    const rows = LEDGER_NEEDLES.filter(([id]) => id.startsWith('C18'))
    expect(rows.length, 'expected exactly one C18 row').toBe(1)

    const needle = rows[0][1]
    expect(needle.length, 'the C18 needle is empty').toBeGreaterThan(0)
    for (const denial of ['no privacy control', 'no notice, no toggle', 'is being built', 'does not exist yet']) {
      expect(needle.toLowerCase(), `C18 still pins a denial: "${needle}"`).not.toContain(denial)
    }
    // The it.each row above already asserts the needle is on the page, so deleting
    // the replacement sentence from Privacy.tsx turns C18 red.
    expect(html, `C18 needle is not on the page: "${needle}"`).toContain(needle)
  })
})

describe('T3-19 (AC-12): both branches of the W3 passage are pinned', () => {
  it('the denied branch has a needle of its own beside the allowed one', () => {
    const rows = WITHDRAWAL_NEEDLES.filter(([id]) => id.startsWith('W3'))
    expect(rows.length, 'expected a W3 lead-in plus both branches').toBeGreaterThanOrEqual(3)

    const allowed = rows.filter(([, needle]) => /if you have allowed analytics/i.test(needle))
    const denied = rows.filter(([, needle]) => /have not allowed analytics/i.test(needle))
    expect(allowed.length, 'the allowed branch lost its needle').toBe(1)
    expect(denied.length, 'the denied branch has no needle — deleting that sentence stays green').toBe(1)

    for (const [id, needle] of rows) {
      expect(html, `${id} is not on the page`).toContain(needle)
    }
  })
})

// T4-10, T4-12, T4-13, T4-14 (task-563). The control makes three already-shipped privacy
// sentences reachable rather than aspirational; these tie the two surfaces together so
// they cannot diverge again.

function privacyParagraphs(needle: string): string[] {
  return html.split('</p>').filter((segment) => segment.includes(needle))
}

// A split segment carries the heading above the paragraph too, so a needle matched off an
// <h2> would pass as if it were body copy. Trim to the paragraph's own open tag.
function paragraphBody(segment: string): string {
  const at = segment.lastIndexOf('<p')
  expect(at, 'no paragraph open tag in this segment').toBeGreaterThan(-1)
  return segment.slice(at)
}

describe('T4-10 (AC-11): the page describes reopening iff a reopen control renders', () => {
  const footerHtml = renderToStaticMarkup(createElement(Footer, { onBookDemo: () => undefined }))
  // C18's anchor. It is the sentence LAND-05-03 shipped and the control made honest — but
  // it describes WHERE you choose, not that you may choose AGAIN, so it cannot carry the
  // biconditional on its own. REOPEN_CLAIM does that.
  const CHOICE_ANCHOR = 'The cookie notice on this site is where you choose'
  const REOPEN_CLAIM = 'brings the notice back with the setting you last chose'

  const reopenControlRenders = (markup: string): boolean =>
    Array.from(markup.matchAll(/<button[^>]*>([^<]*)<\/button>/g)).some((m) => /cookie choices/i.test(m[1]))
  const reopenClaimRenders = (markup: string): boolean => markup.includes(REOPEN_CLAIM)

  it('control: both detectors fire on planted markup and stay quiet on shipped siblings', () => {
    expect(footerHtml.length).toBeGreaterThan(0)
    expect(html.length).toBeGreaterThan(0)
    expect(reopenControlRenders('<button type="button">Cookie choices</button>')).toBe(true)
    expect(reopenControlRenders('<button class="ios-link">Book a demo</button>')).toBe(false)
    expect(reopenClaimRenders(`<p>Cookie choices ${REOPEN_CLAIM}.</p>`)).toBe(true)
    expect(reopenClaimRenders(`<p>${CHOICE_ANCHOR}: Accept allows it.</p>`), 'the detector fires on the wrong sentence').toBe(
      false,
    )
  })

  it('the two are equal — delete the control OR delete the reopen sentence and this goes red', () => {
    expect(
      reopenControlRenders(footerHtml),
      'the page claims a reopen control the footer does not render, or the reverse',
    ).toBe(reopenClaimRenders(html))
  })

  it('the reopen sentence names the control and says where it is, in one paragraph', () => {
    const hits = privacyParagraphs(REOPEN_CLAIM)
    expect(hits.length, 'expected exactly one paragraph describing the reopen').toBe(1)
    const para = paragraphBody(hits[0])
    expect(para, 'the sentence does not name the control a visitor must find').toContain('Cookie choices')
    // The published copy promises the control is on EVERY page. App.render.test.tsx
    // sweeps every route the SPA serves so this promise cannot go stale silently.
    expect(para, 'the sentence does not say where the control is').toContain('at the foot of every page')
    expect(para, 'the choice anchor and the reopen sentence drifted apart').toContain(CHOICE_ANCHOR)
  })
})

describe('T4-12 (AC-12): asc_consent is disclosed, and E3 is not softened to buy it', () => {
  const E3 = 'These two are set by Google, not by our own code'

  it('control: the splitter works and E3 sits in exactly one paragraph', () => {
    expect(html.split('</p>').length, 'the page rendered no paragraphs').toBeGreaterThan(1)
    expect(privacyParagraphs(E3).length, 'E3 must stay in exactly one paragraph').toBe(1)
  })

  it('E3 survives verbatim', () => {
    expect(html, 'ledger claim E3 was weakened or removed').toContain(E3)
  })

  it('the page names asc_consent, says the device holds it, and says it stops the notice returning', () => {
    expect(html, 'the consent record is not disclosed anywhere on the page').toContain('asc_consent')
    const hits = privacyParagraphs('asc_consent')
    expect(hits.length, 'expected exactly one paragraph naming asc_consent').toBe(1)
    const para = paragraphBody(hits[0])
    expect(para, 'asc_consent is named in a heading, not in body copy').toContain('asc_consent')
    expect(para, 'the disclosure does not say where the record is held').toMatch(/your (?:device|browser)/i)
    expect(para, 'the disclosure does not mention the notice').toMatch(/notice/i)
    expect(para, 'the disclosure does not say it is what stops the notice returning').toMatch(
      /again|back|reappear|return|every visit|each visit/i,
    )
    // applyChoice on Reject writes asc_consent and the cookie expiries, nothing else.
    // The page says so; without this the clause is the one sentence here nothing pins.
    expect(para, 'the disclosure drops what Reject writes').toMatch(/only thing our own code writes/i)
    expect(para, 'the Reject clause does not name Reject').toContain('Reject')
    expect(para, 'the record is not disclosed as a cookie of ours').toMatch(/cookie of our own/)
  })

  // The one exception to "the only thing our own code writes": inviteLink.ts, pinned by its own tests.
  const INVITE_CLAUSE = 'session storage on the invite page only and cleared when the tab closes'

  it('the page names the invite token as the one exception', () => {
    const para = paragraphBody(privacyParagraphs('asc_consent')[0]).replace(/\s+/g, ' ')
    expect(para, 'the page does not disclose the invite token').toContain(INVITE_CLAUSE)
    expect(para, 'the exception must sit before the only-thing clause').toMatch(/invite link(?:&#x27;|')s token[^.]*only thing our own code writes/)
  })
})

describe('T4-13 (AC-13): the page says WHY a reload matters after a Reject that follows an Accept', () => {
  const RELOAD = 'reload the page to clear that out too'

  it('control: the reload instruction is on the page, in exactly one paragraph', () => {
    expect(html, 'the reload instruction anchor is gone').toContain(RELOAD)
    expect(privacyParagraphs(RELOAD).length).toBe(1)
  })

  it('the residual is explained: the already-loaded script can re-create the _ga cookies', () => {
    const para = paragraphBody(privacyParagraphs(RELOAD)[0])
    expect(para).toContain(RELOAD)
    expect(para, 'no causal clause — the page still only instructs, it never explains').toMatch(/re-?creat/i)
    expect(para, 'the causal clause does not name what comes back').toContain('_ga')
  })
})

// pm-approved copy: change these strings only with a new approval.
const APPROVED_INTRO =
  'Three other companies receive information about your visit. Google serves the fonts this site is typeset in, and measures how the site is used if you have allowed analytics. HubSpot stores the answers you give if you book a demo. Sentry receives a report when a page fails, and measures how long pages take to load. Your browser loads nothing on this site from, and sends nothing to, any other company.'
const APPROVED_MONITORING =
  "We use Sentry to find out when something breaks or runs slowly. When a page on this site or in the signed-in ASComply product shows an error, your browser sends Sentry a report. Each page you open also sends Sentry how long it took to load. These reports say what went wrong or how long it took, which page it happened on (the address without anything after a '?'), and your browser, operating system and device type. This happens on every visit to the live site, whatever you chose on the cookie notice. It is how we keep the site working. It is not analytics, and it is not used to measure how you use the site. Sentry does not store your IP address. It sets no cookies and writes nothing to your browser's storage. It never receives what you type into the demo form. Sentry stores these reports in the EU. Our preview and test builds send Sentry nothing."
const APPROVED_FONTS =
  "This happens on every page of this site, every time, whatever you decide about analytics. It is not behind the analytics switch and it is not behind any consent check, and none of the controls further down stops it. Sentry's error and performance reports, described below, are the only other flow on this site with no consent gate."

// SSR escapes ASCII ' to &#x27;; copy is compared as a visitor reads it.
const plainText = (markup: string): string =>
  markup
    .replace(/<[^>]*>/g, '')
    .replace(/&#x27;/g, "'")
    .replace(/\s+/g, ' ')
    .trim()

const MONITORING_H2 = /<h2[^>]*>Error and performance monitoring<\/h2>/g

describe('sentry: the page discloses Sentry', () => {
  it('the lede is the approved text, in one paragraph', () => {
    const hits = privacyParagraphs('Three other companies receive information about your visit')
    expect(hits.length, 'expected exactly one lede paragraph').toBe(1)
    expect(plainText(paragraphBody(hits[0]))).toBe(APPROVED_INTRO)
    expect(html).not.toContain('Two other companies')
    expect(html).not.toContain('Your browser loads nothing on this site from any other company')
  })

  it('the monitoring section is the approved text, between HubSpot and withdrawal', () => {
    expect(html.match(MONITORING_H2) ?? [], 'expected exactly one monitoring h2').toHaveLength(1)
    const heading = html.search(MONITORING_H2)
    const booking = html.indexOf('>If you book a demo</h2>')
    const lastHubspot = html.indexOf('The form carries your answers and nothing else')
    const stop = html.indexOf('>How to stop being measured</h2>')
    expect(booking, 'control: the demo section anchor is gone').toBeGreaterThan(-1)
    expect(lastHubspot, 'control: the last HubSpot sentence anchor is gone').toBeGreaterThan(booking)
    expect(stop, 'control: the withdrawal section anchor is gone').toBeGreaterThan(-1)
    expect(heading).toBeGreaterThan(lastHubspot)
    expect(heading).toBeLessThan(stop)

    // One paragraph, then straight into the withdrawal heading.
    const after = html.slice(heading).replace(MONITORING_H2, '')
    const section = after.match(/^\s*<p[^>]*>([\s\S]*?)<\/p>\s*<h2[^>]*>How to stop being measured<\/h2>/)
    expect(section, 'the heading is not followed by exactly one paragraph and then the withdrawal heading').not.toBeNull()
    expect(plainText(section![1])).toBe(APPROVED_MONITORING)
  })

  it('the load-time sentence tracks the landing sample rate', () => {
    const rate = sentryOptions({
      service: 'landing',
      dsn: 'https://public@o1.ingest.de.sentry.io/1',
      release: 'r',
    })!.tracesSampleRate
    expect(typeof rate).toBe('number')
    expect(html.includes('Each page you open')).toBe(rate === 1)
    expect(html.includes('Some of the pages you open')).toBe(rate !== 1)
  })

  it('the fonts paragraph names the other ungated flow', () => {
    const hits = privacyParagraphs('not behind any consent check')
    expect(hits.length, 'expected exactly one fonts paragraph').toBe(1)
    const para = paragraphBody(hits[0])
    expect(para).toContain('Sentry')
    expect(para).toContain('no consent gate')
    expect(plainText(para)).toBe(APPROVED_FONTS)
  })

  it('the withdrawal passage no longer says a visitor who declined is not measured at all', () => {
    expect(html, 'control: the replacement sentence is missing').toContain(
      'If you have not allowed analytics, Google is not measuring you at all',
    )
    expect(html).not.toContain('you are not being measured at all')
    expect(html).not.toContain('it is the one flow on this site with no gate')
  })
})

describe('sentry: the page makes every new monitoring sentence', () => {
  it('each new monitoring claim is a sentence the page makes', () => {
    const KEYS: Record<string, string> = {
      C23: 'shows an error',
      C24: 'how long it took to load',
      C25: 'your browser, operating system and device type',
      C26: 'every visit to the live site',
      C27: 'not analytics',
      C28: 'does not store your IP address',
      C29: 'no cookies',
      C30: 'what you type into the demo form',
      C31: 'stores these reports in the EU',
    }
    const page = plainText(html)
    expect(Object.keys(KEYS), 'one key per new claim').toHaveLength(9)
    for (const [id, key] of Object.entries(KEYS)) {
      expect(page, `${id}: the page lacks "${key}"`).toContain(key)
    }
  })
})

// The page and the ledger describe what notifications sends to HubSpot and Resend.
// Facts come from the Go and TS that send them (privacyCodeFacts.test.util.ts), not from the story.
describe('AUTH-17-09: the page quotes the consent copy from code', () => {
  it('the page quotes the marketing sentence and the notice from code', () => {
    expect(MARKETING_CONSENT_TEXT.length, 'control: the marketing sentence is empty').toBeGreaterThan(0)
    expect(PRODUCT_EMAIL_NOTICE.length, 'control: the notice is empty').toBeGreaterThan(0)
    const page = plainText(html)
    expect(page, 'the page does not quote MARKETING_CONSENT_TEXT').toContain(MARKETING_CONSENT_TEXT)
    expect(page, 'the page does not quote PRODUCT_EMAIL_NOTICE').toContain(PRODUCT_EMAIL_NOTICE)

    const src = readFileSync(PRIVACY_TSX, 'utf8')
    expect(src).toMatch(/import\s*\{[^}]*MARKETING_CONSENT_TEXT[^}]*\}\s*from\s*['"]\.\/MarketingConsent['"]/)
    expect(src).toMatch(/import\s*\{[^}]*PRODUCT_EMAIL_NOTICE[^}]*\}\s*from\s*['"]\.\/RegisterModal['"]/)
    expect(src, 'the marketing sentence is retyped, not imported').not.toContain(MARKETING_CONSENT_TEXT)
    expect(src, 'the notice is retyped, not imported').not.toContain(PRODUCT_EMAIL_NOTICE)
  })
})

describe('AUTH-17-09: no Resend region claim without a vendor citation', () => {
  const REGION =
    /\b(?:regions?|located|locations?|countr(?:y|ies)|EU|EEA|Europe|European|US|USA|U\.S\.|United States|America|Ireland|Frankfurt|Germany|data cent(?:er|re)s?)\b/
  const NO_CLAIM = /\bno\b[^.]*\b(?:region|location|country)\b|\b(?:region|location)\b[^.]*\b(?:not|never) (?:claimed|stated|named|given)\b|\bno claim\b/i
  const sentencesOf = (text: string): string[] => text.split(/(?<=[.!?])\s+/)
  const claimsRegion = (sentence: string): boolean => sentence.includes('Resend') && REGION.test(sentence) && !NO_CLAIM.test(sentence)

  const pageText = html.replace(/<\/(?:p|li|h\d)>/g, '\n').replace(/<[^>]*>/g, '').replace(/&#x27;/g, "'")

  it('control: the detector fires on a region claim and stays quiet on shipped and meta sentences', () => {
    expect(claimsRegion('Resend stores your contacts in the EU.')).toBe(true)
    expect(claimsRegion('Resend does not store data in the US.')).toBe(true)
    expect(claimsRegion('The page makes no Resend region claim.')).toBe(false)
    expect(claimsRegion('HubSpot holds all of this on their EU servers.')).toBe(false)
    expect(claimsRegion('Resend receives your email address.')).toBe(false)
  })

  it('no Resend region claim without a vendor citation', () => {
    const pageResend = sentencesOf(pageText.replace(/\s*\n\s*/g, ' \n')).flatMap((s) => s.split('\n')).filter((s) => s.includes('Resend'))
    expect(pageResend.length, 'the page does not name Resend, so there is nothing to check').toBeGreaterThan(0)
    expect(pageResend.filter(claimsRegion), 'the page claims a Resend region').toEqual([])

  })
})
