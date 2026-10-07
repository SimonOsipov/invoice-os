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
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { PROSE_MAX_WIDTH, Privacy } from './Privacy'
import { Footer } from './Footer'
import { MARKETING_CONSENT_TEXT } from './MarketingConsent'
import { PRODUCT_EMAIL_NOTICE } from './RegisterModal'
import {
  crmTags,
  hubspotCrmKeys,
  hubspotFormsKeys,
  marketingColumns,
  resendContactKeys,
  resendSubscription,
} from './privacyCodeFacts.test.util'
import { sentryOptions } from '@invoice-os/monitoring'

const SRC_DIR = fileURLToPath(new URL('.', import.meta.url))
const PRIVACY_TSX = join(SRC_DIR, 'Privacy.tsx')
const DOCS = join(SRC_DIR, '..', '..', '..', '..', 'docs')

const html = renderToStaticMarkup(createElement(Privacy))

// One row per ledger claim that had no render assertion. docs/privacy-policy-claims.md
// is the authority for what each id means.
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
  ['E3 our code sets no cookies', 'Our own code sets no cookies at all'],
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

describe('AC11 + AC12: the docs this page is defended by', () => {
  it('the claim ledger exists and its heading counts every C row', () => {
    const ledger = readFileSync(join(DOCS, 'privacy-policy-claims.md'), 'utf8')
    const last = Number(/\(C1–C(\d+)\)/.exec(ledger)?.[1])
    expect(last, 'the Table 1 heading carries no (C1–Cn) range').toBeGreaterThanOrEqual(31)
    for (let n = 1; n <= last; n += 1) {
      expect(ledger, `ledger has no row C${n}`).toContain(`| C${n} |`)
    }
    expect(ledger, `a row C${last + 1} exists beyond the heading range`).not.toContain(`| C${last + 1} |`)
    expect(ledger, 'the Table 1 heading still ends at C22').not.toContain('(C1–C22)')
    expect(ledger).toContain('Privacy.tsx')
  })

  it('docs/analytics.md no longer says the measurement id is absent', () => {
    const analytics = readFileSync(join(DOCS, 'analytics.md'), 'utf8')
    expect(analytics).not.toContain('Measured absent')
    expect(analytics.match(/operator-confirmed 2026-08-16/g) ?? []).toHaveLength(2)
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

describe('AC-12: the claim ledger asserts nothing the mount makes false', () => {
  const ledger = readFileSync(join(DOCS, 'privacy-policy-claims.md'), 'utf8')
  // The ledger wraps its prose, so every needle is matched against a whitespace-
  // flattened copy.
  const flat = ledger.replace(/\s+/g, ' ')

  // Ten line-sites, not the five the plan named. gaCookies.ts becomes the repo's
  // first document.cookie writer and consentActions.ts writeConsent's first
  // production caller, which is what falsifies E3's evidence and C18/W2.
  const FALSIFIED: readonly (readonly [string, string])[] = [
    ['C18 and W2 — zero call sites', 'zero production call sites'],
    ['Table 2 lead — no control', 'There is no consent control on this site yet'],
    ['Table 2 lead — never called', 'is never called by anything that ships'],
    ['Table 2 lead — no way to allow', 'there is no way for a visitor to allow it either'],
    ['the rule behind the D7 guard', 'may not mention a notice, a banner, a Reject button or a preference centre'],
    ['forward instruction to this subtask', 'LAND-05-03 still rewrites this section'],
    ['W2 — no control of its own', 'The site has no privacy control of its own yet'],
    ['W5 — the quoted send() guard', 'if (!loaded) return'],
    ['E3 — the document.cookie grep', 'occurrences in `frontend/landing/src`'],
    ['E3 — nothing writes the key', 'and nothing writes it'],
    ['deliberate omissions — no description', 'No description of the consent notice'],
  ]

  it('control: the ledger read resolved and its stable markers survive', () => {
    expect(ledger.length).toBeGreaterThan(0)
    expect(ledger, 'the C18 row marker must never be renamed, only its cell text').toContain('| C18 |')
    expect(ledger).toContain('Privacy.tsx')
    expect(FALSIFIED.length).toBe(11)
  })

  it.each(FALSIFIED)('%s is corrected in the same commit as the mount', (_label, needle) => {
    expect(flat, `the ledger still asserts: "${needle}"`).not.toContain(needle)
  })

  it('the section this subtask discharges is recorded as closed, per the ledger own rule', () => {
    expect(flat).toContain('Closed at LAND-05-03')
  })
})

describe('AC-12: docs/analytics.md carries the OWED enhanced-measurement item', () => {
  const analytics = readFileSync(join(DOCS, 'analytics.md'), 'utf8')
  const items = Array.from(analytics.matchAll(/^(\d+)\. \*\*/gm))

  it('control: the doc read resolved and its checklist is numbered', () => {
    expect(analytics.length).toBeGreaterThan(0)
    expect(analytics).toContain('## Operator checklist')
    expect(items.length, 'no numbered checklist items found').toBeGreaterThan(0)
  })

  it('the stated count matches the list', () => {
    expect(analytics, 'the checklist header still says six').not.toContain('Six items.')
    expect(analytics).toContain('Seven items.')
    expect(items.length).toBe(7)
  })

  it('the new item is OPEN, never reported as done', () => {
    const idx = analytics.indexOf('\n7. **')
    expect(idx, 'no seventh checklist item').toBeGreaterThan(-1)
    const seventh = analytics.slice(idx)
    expect(seventh.toLowerCase()).toContain('enhanced measurement')
    // The tripwire: the row above pins exactly two `operator-confirmed 2026-08-16`
    // over this file, so wording item 7 as confirmed turns it red. That is the
    // mechanism that stops the owed item being reported as discharged on merge.
    expect(seventh, 'the owed item is worded as already confirmed').not.toContain('operator-confirmed 2026-08-16')
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
  const E3 = 'Our own code sets no cookies at all'

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
  })

  // The one exception to "the only thing our own code writes": inviteLink.ts, pinned by its own tests.
  const INVITE_CLAUSE = 'session storage on the invite page only and cleared when the tab closes'

  it('the page and the ledger row E6 both name the invite token as the one exception', () => {
    const para = paragraphBody(privacyParagraphs('asc_consent')[0]).replace(/\s+/g, ' ')
    expect(para, 'the page does not disclose the invite token').toContain(INVITE_CLAUSE)
    expect(para, 'the exception must sit before the only-thing clause').toMatch(/invite link(?:&#x27;|')s token[^.]*only thing our own code writes/)
    const e6 = (readFileSync(join(DOCS, 'privacy-policy-claims.md'), 'utf8').split('\n').find((l) => l.startsWith('| E6 |')) ?? '').replace(/\s+/g, ' ')
    expect(e6, 'control: E6 row missing').not.toBe('')
    expect(e6, 'E6 does not carry the invite exception').toContain(INVITE_CLAUSE)
    expect(e6, 'E6 says the invite token is the one exception').toMatch(/one thing our own code writes apart from the cookie expiries and the invite link's token/)
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

describe('T4-14 (AC-11): the ledger no longer forbids what the page now says', () => {
  const ledger = readFileSync(join(DOCS, 'privacy-policy-claims.md'), 'utf8')
  const flat = ledger.replace(/\s+/g, ' ')
  const PROHIBITION = 'the page must not tell a visitor they can change their answer at'
  const SURVIVOR = 'No cookie table and no per-category breakdown'

  it('control: the read resolved, a surviving bullet is found, and the scan finds a planted copy', () => {
    expect(ledger.length).toBeGreaterThan(0)
    expect(flat, 'the surviving deliberate-omission bullet is gone — wrong file or wrong section').toContain(SURVIVOR)
    expect(`x ${PROHIBITION} any time.`.replace(/\s+/g, ' '), 'the scan cannot find a planted copy').toContain(
      PROHIBITION,
    )
  })

  it('the prohibition bullet is deleted', () => {
    expect(flat, 'the ledger still forbids the sentence the page now carries').not.toContain(PROHIBITION)
  })

  it('C18 names the footer reopen control in its CLAIM cell, not merely in its evidence', () => {
    const row = ledger.split('\n').find((line) => line.includes('| C18 |'))
    expect(row, 'the C18 row marker must never be renamed, only its cell text').toBeDefined()

    // Column 2 is the CLAIM. Scanning the whole row passes on the evidence cell alone,
    // which already cites Footer.tsx — so the claim itself could silently revert.
    const cells = row!.split('|').map((cell) => cell.trim())
    expect(cells[1], 'the row shape changed: column 1 is no longer the id').toBe('C18')
    expect(cells.length, 'the C18 row lost a column').toBeGreaterThanOrEqual(5)
    // Control needle: the claim cell is the withdrawal claim and is not empty.
    expect(cells[2], 'control: the C18 claim cell is empty or moved').toContain('Withdrawal')
    expect(cells[2], 'the C18 CLAIM does not name the footer reopen control').toMatch(/cookie choices/i)
  })

  it('the section this subtask discharges is recorded as closed, per the ledger own rule', () => {
    expect(flat, 'the LAND-05-03 marker must survive').toContain('Closed at LAND-05-03')
    expect(flat, 'no Closed at LAND-05-04 marker').toContain('Closed at LAND-05-04')
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

describe('sentry: the ledger carries every new sentence', () => {
  const ledger = readFileSync(join(DOCS, 'privacy-policy-claims.md'), 'utf8')
  const flat = ledger.replace(/\s+/g, ' ')
  const rowOf = (id: string): string[] => ledger.split('\n').filter((line) => line.startsWith(`| ${id} |`))
  const NEW_IDS = Array.from({ length: 9 }, (_, i) => `C${23 + i}`)

  it('each new ledger row has a claim, a class and evidence', () => {
    expect(NEW_IDS).toHaveLength(9)
    for (const id of NEW_IDS) {
      const rows = rowOf(id)
      expect(rows.length, `expected exactly one ${id} row`).toBe(1)
      const cells = rows[0].split('|').map((cell) => cell.trim())
      expect(cells[1], `${id}: column 1 is not the id`).toBe(id)
      expect(cells[2], `${id} has no claim`).not.toBe('')
      expect(cells[3], `${id} has no class`).not.toBe('')
      expect(cells[4], `${id} has no evidence`).not.toBe('')
    }
    const order = NEW_IDS.map((id) => ledger.indexOf(`| ${id} |`))
    expect(order, 'C23 to C31 are out of order').toEqual([...order].sort((a, b) => a - b))
  })

  it('C17 names the Sentry host and transport', () => {
    const rows = rowOf('C17')
    expect(rows.length).toBe(1)
    expect(rows[0], 'control: C17 lost its HubSpot host').toContain('api-eu1.hsforms.com')
    expect(rows[0]).toContain('sentry.io')
    expect(rows[0]).toContain('fetch')
  })

  it('the ledger no longer counts what Sentry changed', () => {
    expect(flat, 'control: the ledger read resolved').toContain('Privacy.tsx')
    expect(flat).not.toContain('only three network senders')
    expect(flat).not.toContain('the two third parties that receive visitor data')
    expect(flat).not.toContain('exactly four external hosts')
    expect(flat).toContain('the three third parties')
  })

  it('each new ledger claim is a sentence the monitoring section makes', () => {
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
    expect(Object.keys(KEYS), 'one key per new row').toEqual(NEW_IDS)
    for (const id of NEW_IDS) {
      const claim = rowOf(id)[0]?.split('|')[2] ?? ''
      expect(claim, `${id}: claim cell missing`).not.toBe('')
      expect(page, `${id}: the page lacks "${KEYS[id]}"`).toContain(KEYS[id])
      expect(claim, `${id}: the claim does not say "${KEYS[id]}"`).toContain(KEYS[id])
    }
  })

  it('E3 and E6 carry the Sentry scope note', () => {
    const e3 = rowOf('E3')[0] ?? ''
    const e6 = rowOf('E6')[0] ?? ''
    expect(e3, 'control: E3 row missing').not.toBe('')
    expect(e3).toContain('Sentry')
    expect(flat, 'no ledger row may keep the old count').not.toContain('four network senders')
    expect(e6).toContain('the Sentry SDK started by `instrument.ts`')
  })

  it('C10, W3 and W7 carry the D-19 changes', () => {
    expect(flat, 'C10').toContain("Sentry's reports (C26) are the only other ungated flow")
    expect(flat, 'W3').toContain('is not being measured by Google at all')
    expect(flat, 'W3 old wording').not.toContain('is not being measured at all')
    const w7 = rowOf('W7')[0]?.split('|')[2] ?? ''
    expect(w7, 'control: W7 claim cell').toContain('stops Google Fonts')
    expect(w7, 'W7 claim must not cite Sentry; the closing note does not').not.toContain('Sentry')
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

describe('AUTH-17-09: the ledger matches the new HubSpot and Resend paths', () => {
  const ledger = readFileSync(join(DOCS, 'privacy-policy-claims.md'), 'utf8')
  const lines = ledger.split('\n').filter((line) => line.startsWith('| '))
  const cellsOf = (row: string): string[] => row.split('|').map((cell) => cell.trim())
  const claimOf = (row: string): string => cellsOf(row)[2]
  const evidenceOf = (row: string): string => cellsOf(row).at(-2) ?? ''
  const rowOf = (id: string): string => {
    const rows = lines.filter((line) => line.startsWith(`| ${id} |`))
    expect(rows.length, `expected exactly one ${id} row`).toBe(1)
    return rows[0]
  }
  const tick = (key: string): string => `\`${key}\``

  it('control: the ledger rows and the code facts resolved', () => {
    expect(lines.length).toBeGreaterThan(50)
    expect(hubspotCrmKeys()).toEqual(['ascomply_contact_tags', 'company', 'email', 'firstname', 'lastname'])
    expect(resendContactKeys()).toEqual(['email', 'first_name', 'last_name'])
    expect(crmTags()).toEqual(['registered', 'demo request'])
    expect(marketingColumns().length).toBeGreaterThan(0)
  })

  it('C11 says registrants go to HubSpot after verifying and demo bookers on submit, with both tags', () => {
    const row = rowOf('C11')
    expect(claimOf(row)).toMatch(/regist/i)
    expect(claimOf(row)).toMatch(/verif/i)
    expect(claimOf(row)).toMatch(/demo/i)
    for (const tag of crmTags()) expect(row, `C11 does not name the tag "${tag}"`).toContain(tag)
    expect(evidenceOf(row)).toContain('internal/notifications/worker.go')
    expect(evidenceOf(row)).toContain('internal/notifications/hubspot.go')
  })

  it('C12 names every property each source sends, tag property included', () => {
    const row = rowOf('C12')
    for (const key of [...hubspotCrmKeys(), ...hubspotFormsKeys()]) {
      expect(row, `C12 does not name ${key}`).toContain(tick(key))
    }
    expect(row).toMatch(/registrant/i)
    expect(row).toMatch(/demo/i)
  })

  it('C14 says our own server stores the marketing sentence with its time', () => {
    const row = rowOf('C14')
    expect(row).toContain('MARKETING_CONSENT_TEXT')
    for (const column of marketingColumns()) expect(row, `C14 does not name ${column}`).toContain(column)
    expect(claimOf(row)).toMatch(/\btime\b/i)
  })

  it('C16 still claims no browsing data and cites the CRM client', () => {
    expect(hubspotCrmKeys().filter((k) => /^(?:context|hutk|pageuri|pagename)$/i.test(k))).toEqual([])
    const row = rowOf('C16')
    expect(claimOf(row)).toContain('browsing history')
    expect(evidenceOf(row)).toContain('internal/notifications/hubspot.go')
  })

  it('E2 says only the separate marketing box adds you to a list', () => {
    const row = rowOf('E2')
    expect(claimOf(row), 'E2 does not name the marketing box').toMatch(/marketing (?:box|checkbox)/i)
    expect(claimOf(row) + evidenceOf(row), 'E2 does not name Resend').toContain('Resend')
    expect(evidenceOf(row), 'E2 cites communications: [] alone').toMatch(/internal\/notifications\/(?:worker|resend)\.go/)
  })

  it('E5 covers the records in HubSpot, Resend and our own server', () => {
    const row = rowOf('E5')
    expect(row).toContain('HubSpot')
    expect(row).toContain('Resend')
    expect(row).toMatch(/our own server|our server|contacts table|database/i)
  })

  it("E6 names the landing's network senders and their count", () => {
    const NUMBER_WORDS = ['zero', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine', 'ten']
    const walk = (dir: string): string[] =>
      readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
        const path = join(dir, entry.name)
        if (entry.isDirectory()) return walk(path)
        return entry.name.endsWith('.ts') && !entry.name.endsWith('.test.ts') ? [path] : []
      })
    const senders = walk(join(SRC_DIR, '..'))
      .filter((path) => /fetch\(|apiFetch/.test(readFileSync(path, 'utf8')))
      .map((path) => path.slice(path.lastIndexOf('/') + 1))
      .sort()
    expect(senders, 'control: the scan must see the demo request client').toContain('demoRequest.ts')
    expect(readFileSync(join(SRC_DIR, '..', 'instrument.ts'), 'utf8'), 'control: instrument.ts starts the SDK').toContain('initMonitoring')

    const e6 = (ledger.split('\n').find((line) => line.startsWith('| E6 |')) ?? '').replace(/\s+/g, ' ')
    expect(e6, 'control: E6 row missing').not.toBe('')
    const count = /\b([a-z]+) network senders\b/.exec(e6)
    expect(count, 'E6 states no sender count').not.toBeNull()
    expect(NUMBER_WORDS.indexOf(count![1]), `E6 says "${count![1]} network senders", the source has ${senders.length} plus the Sentry SDK`).toBe(senders.length + 1)
    for (const file of senders) expect(e6, `E6 does not name ${file}`).toContain(tick(file))

    const end = e6.indexOf('none reads', count!.index)
    expect(end, 'E6 no longer ends the sender list with "none reads the key"').toBeGreaterThan(-1)
    const named = Array.from(e6.slice(count!.index, end).matchAll(/`([A-Za-z]+\.ts)`/g), (m) => m[1]).filter((n) => n !== 'instrument.ts')
    expect(Array.from(new Set(named)).sort(), 'E6 names a sender the scan does not find, or misses one').toEqual(senders)
    expect(e6).toContain('the Sentry SDK started by `instrument.ts`')
  })

  it('the Resend row says "only a tick opts in" rests on the topic default the operator owes, which no code reads', () => {
    expect(readFileSync(join(SRC_DIR, '..', '..', '..', '..', 'internal/notifications/resend.go'), 'utf8'), 'control: the client now sets a topic default').not.toMatch(/default_subscription/)
    const rows = lines.filter((line) => /^\| C\d+ \|/.test(line) && claimOf(line).includes('Resend') && /only a ticked/i.test(claimOf(line)))
    expect(rows.length, 'control: no Resend row says only a tick makes a person marketing-eligible').toBeGreaterThan(0)
    for (const row of rows) {
      const evidence = evidenceOf(row)
      expect(cellsOf(row)[3], 'the class cell must say CODE').toMatch(/\bCODE\b/)
      expect(cellsOf(row)[3], 'OPERATOR-CONFIRMED claims U2 happened; it is owed').not.toContain('OPERATOR-CONFIRMED')
      expect(evidence, 'the evidence never names the topic default it depends on').toMatch(/opt_out/)
      expect(evidence, 'the evidence never names the operator step U2').toMatch(/\bU2\b/)
      expect(evidence, 'the evidence does not say U2 is owed or not yet confirmed').toMatch(/owed|not yet/i)
    }
  })

  it('the Resend ledger row cites the notifications clients', () => {
    const rows = lines.filter((line) => /^\| C\d+ \|/.test(line) && claimOf(line).includes('Resend'))
    expect(rows.length, 'no Table 1 row names Resend').toBeGreaterThan(0)
    for (const row of rows) {
      const id = Number(/^\| C(\d+) \|/.exec(row)![1])
      expect(id, 'the Resend row must be a new row after C31').toBeGreaterThan(31)
      expect(evidenceOf(row)).toContain('internal/notifications/resend.go')
      expect(evidenceOf(row)).toContain('internal/notifications/worker.go')
      for (const field of [...resendContactKeys(), resendSubscription()]) {
        expect(row, `the Resend row does not name ${field}`).toContain(field)
      }
      expect(claimOf(row)).toMatch(/registrant/i)
      expect(claimOf(row)).toMatch(/verif/i)
      expect(claimOf(row)).toMatch(/tick/i)
      expect(claimOf(row)).toMatch(/product|service/i)
    }
  })
})

describe('AUTH-17-09: no Resend region claim without a vendor citation', () => {
  const REGION =
    /\b(?:regions?|located|locations?|countr(?:y|ies)|EU|EEA|Europe|European|US|USA|U\.S\.|United States|America|Ireland|Frankfurt|Germany|data cent(?:er|re)s?)\b/
  const NO_CLAIM = /\bno\b[^.]*\b(?:region|location|country)\b|\b(?:region|location)\b[^.]*\b(?:not|never) (?:claimed|stated|named|given)\b|\bno claim\b/i
  const sentencesOf = (text: string): string[] => text.split(/(?<=[.!?])\s+/)
  const claimsRegion = (sentence: string): boolean => sentence.includes('Resend') && REGION.test(sentence) && !NO_CLAIM.test(sentence)

  const ledger = readFileSync(join(DOCS, 'privacy-policy-claims.md'), 'utf8')
  const rows = ledger.split('\n').filter((line) => line.startsWith('| ') && line.includes('Resend'))
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

    expect(rows.length, 'no ledger row names Resend, so there is nothing to check').toBeGreaterThan(0)
    for (const row of rows) {
      const cells = row.split('|').map((cell) => cell.trim())
      if (!cells.flatMap(sentencesOf).some(claimsRegion)) continue
      expect(cells.some((cell) => cell.includes('VENDOR-ASSERTED')), `a Resend region claim is not VENDOR-ASSERTED: ${row.slice(0, 60)}`).toBe(true)
      expect(row, 'a Resend region claim cites no Resend document').toMatch(/https?:\/\/\S*resend\.com/)
    }
  })
})
