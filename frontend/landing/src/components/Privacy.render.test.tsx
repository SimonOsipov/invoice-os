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
import { LIBRARY_HOSTNAMES, SHARED_COOKIE_DOMAIN } from '../hubspot'
import { CONSENT_DEFAULT_ANALYTICS } from '../consent'
import { MARKETING_CONSENT_TEXT } from './MarketingConsent'
import {
  authSmtpHost,
  clientsSendConsentTime,
  crmTags,
  deliveryIsQueued,
  demoPostsToOurServerAfterHubSpot,
  registrantCompanyIsWorkspaceName,
  gatewayStampsRegisterConsentTime,
  hubspotCrmKeys,
  hubspotFormsKeys,
  resendContactKeys,
  resendSkipsUntickedDemoBooker,
} from './privacyCodeFacts.test.util'

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

  it('D4: all three processors are named', () => {
    expect(html).toContain('Google')
    expect(html).toContain('HubSpot')
    expect(html).toContain('Sentry')
  })

  it('D4: each processors location is stated', () => {
    expect(html).toContain('United States')
    expect(html).toContain('EU servers')
    expect(html).toContain('in the EU')
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
    expect(html).toContain('it is not behind any consent check')
    expect(html).not.toContain('it is the one flow on this site with no gate')
  })

  it('C10 (NEW): the fonts host named on the page is the host the site requests', () => {
    const indexHtml = readFileSync(INDEX_HTML, 'utf8')
    expect(indexHtml).toContain('https://fonts.googleapis.com/css2?family=Manrope')
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
    expect(carriesConsentQualifier(paragraphContaining('These two are set by Google, not by our own code'))).toBe(true)
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
    const lede = paragraphContaining(
      'Your browser loads nothing on this site from, and sends nothing to, any other company',
    )
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

// Every fact below is read from the Go and TS that sends it (privacyCodeFacts.test.util.ts).
// A key table is the page's words for each code key; its keys must equal the code's, so a new
// property forces both the table and the page to follow.
describe('AUTH-17-09: what goes to HubSpot and to Resend, and when', () => {
  const html = renderToStaticMarkup(createElement(Privacy))
  const plain = (s: string): string =>
    s.replace(/<[^>]*>/g, '').replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&').replace(/\s+/g, ' ').trim()
  const blocks = (markup: string): string[] =>
    markup
      .split(/<\/(?:p|li)>/)
      .map((seg) => {
        const at = Math.max(seg.lastIndexOf('<p'), seg.lastIndexOf('<li'))
        return at < 0 ? '' : plain(seg.slice(at))
      })
      .filter(Boolean)
  const sentences = (text: string): string[] => text.split(/(?<=[.!?])\s+/)
  const section = (headingRe: RegExp): string => {
    const hit = html.split(/(?=<h2[ >])/).filter((part) => headingRe.test(plain(/^<h2[^>]*>([\s\S]*?)<\/h2>/.exec(part)?.[1] ?? '')))
    expect(hit.length, `expected one h2 matching ${headingRe}`).toBe(1)
    return hit[0]
  }

  const HUBSPOT_WORDS: Record<string, RegExp> = {
    firstname: /first (?:and last )?name/i,
    lastname: /last name|first and last name/i,
    email: /e-?mail/i,
    company: /company/i,
    ascomply_contact_tags: /\btags?\b/i,
  }
  const FORMS_ONLY_WORDS: Record<string, RegExp> = {
    jobtitle: /\brole\b/i,
    company_size: /taxpayer size/i,
    monthly_invoice_volume: /monthly invoice volume/i,
  }
  const RESEND_WORDS: Record<string, RegExp> = {
    email: /e-?mail/i,
    first_name: /first (?:and last )?name/i,
    last_name: /last name|first and last name/i,
  }

  it('control: the word tables carry exactly the keys the code sends', () => {
    expect(Object.keys(HUBSPOT_WORDS).sort()).toEqual(hubspotCrmKeys())
    expect(Object.keys(RESEND_WORDS).sort()).toEqual(resendContactKeys())
    const formsOnly = hubspotFormsKeys().filter((k) => !hubspotCrmKeys().includes(k))
    expect(formsOnly.length, 'the forms-only list is empty').toBeGreaterThan(0)
    expect(Object.keys(FORMS_ONLY_WORDS).sort()).toEqual(formsOnly)
    expect(crmTags()).toEqual(['registered', 'demo request'])
  })

  it('the page names what HubSpot receives from a registrant and from a demo booker, and when', () => {
    const registrant = blocks(html).filter((b) => /hubspot/i.test(b) && /after you verify/i.test(b))
    expect(registrant.length, 'expected one HubSpot paragraph saying "after you verify"').toBe(1)
    for (const key of hubspotCrmKeys()) {
      expect(HUBSPOT_WORDS[key], `no page word for ${key}`).toBeDefined()
      expect(registrant[0], `the registrant paragraph does not name ${key}`).toMatch(HUBSPOT_WORDS[key])
    }
    expect(registrant[0], 'the registrant paragraph does not name the tag').toContain(`“${crmTags()[0]}”`)
    for (const [key, word] of Object.entries(FORMS_ONLY_WORDS)) {
      expect(registrant[0], `a registrant is not sent ${key}`).not.toMatch(word)
    }

    const demo = plain(section(/book a demo/i))
    for (const [key, word] of Object.entries(FORMS_ONLY_WORDS)) {
      expect(demo, `the demo list lost ${key}`).toMatch(word)
    }
    expect(demo, 'the demo section does not name the tag').toContain(`“${crmTags()[1]}”`)
    expect(demo, 'the demo section does not say when it is sent').toMatch(
      /(?:when|as soon as|once|after) you (?:submit|book|send)|on submit|when the form is (?:submitted|sent)/i,
    )
  })

  it('a registrant\'s company is the workspace name they typed, and the page says so', () => {
    expect(registrantCompanyIsWorkspaceName(), 'control: store.go no longer maps the workspace name to company').toBe(true)
    const registrant = blocks(html).filter((b) => /hubspot/i.test(b) && /after you verify/i.test(b))
    expect(registrant.length, 'control: the registrant HubSpot paragraph exists').toBe(1)
    expect(registrant[0], 'the page calls the workspace name "your company"').toMatch(/workspace name/i)
  })

  it('the registrant statements sit in an account section', () => {
    const account = plain(section(/account|regist/i))
    expect(account).toMatch(/after you verify/i)
    expect(account).toContain('Resend')
  })

  it('the page says every verified registrant is a Resend contact and only a tick makes marketing', () => {
    const resend = blocks(html).filter((b) => b.includes('Resend'))
    expect(resend.length, 'the page does not name Resend').toBeGreaterThan(0)
    const para = resend.find((b) => /\b(?:every|each)\b[^.]*\b(?:verified|registrant)/i.test(b))
    expect(para, 'no Resend paragraph says every verified registrant is a contact').toBeDefined()
    expect(para).toMatch(/product|service/i)
    const onlyTick = /\bonly\b[^;.]*\b(?:tick|ticked|ticks|box|checkbox)\b|\b(?:tick|ticked|ticks)\b[^;.]*\bonly\b/i
    expect(
      para!.split(/[;.]/).some((clause) => /\bmarketing\b/i.test(clause) && onlyTick.test(clause)),
      'no clause says only a tick makes marketing',
    ).toBe(true)
    expect(para).toMatch(/marketing/i)
    for (const key of resendContactKeys()) {
      expect(RESEND_WORDS[key], `no page word for ${key}`).toBeDefined()
      expect(para, `the Resend paragraph does not name ${key}`).toMatch(RESEND_WORDS[key])
    }
    for (const [key, word] of Object.entries(FORMS_ONLY_WORDS)) {
      expect(para, `Resend is not sent ${key}`).not.toMatch(word)
    }
    expect(para, 'Resend is not sent the company').not.toMatch(/company/i)
  })

  it('CF1: a demo booker is not promised marketing email, and Resend holds only a ticked one', () => {
    expect(resendSkipsUntickedDemoBooker(), 'worker.go no longer skips Resend for an unticked demo booker').toBe(true)
    const demo = plain(section(/book a demo/i))
      .replace(CONSENT_TEXT, '')
      .replace(MARKETING_CONSENT_TEXT, '')
    const resend = sentences(demo).filter((s) => s.includes('Resend'))
    expect(resend.length, 'the demo section does not say what reaches Resend').toBeGreaterThan(0)
    expect(
      resend.some((s) => /\b(?:unticked|not ticked|without (?:a|the) tick|leave it)\b/i.test(s) && /\b(?:not|never|neither|nor|no)\b/i.test(s)),
      'no statement that an unticked booker is not in Resend',
    ).toBe(true)
    const promise = sentences(demo).filter((s) =>
      /\b(?:you will|you'll|we will|we'll|you can expect)\b[^.]*\b(?:receive|get|hear|send|email)\b[^.]*\b(?:marketing|news|offers|newsletters?|promotions?)\b/i.test(s),
    )
    expect(promise, 'the demo section promises marketing email').toEqual([])
  })

  it('E2: the consent box adds you to no list; only the separate marketing box does, in Resend', () => {
    const demo = plain(section(/book a demo/i))
    expect(sentences(demo).some((s) => s.includes('Resend') && /\b(?:box|checkbox|tick)/i.test(s)), 'no sentence ties the tick to Resend').toBe(true)
    expect(sentences(demo).some((s) => /marketing/i.test(s) && /\b(?:box|checkbox|tick)/i.test(s)), 'no sentence names the marketing box').toBe(true)
    expect(demo, 'the consent box no longer says it adds you to no list').toMatch(/does not add you to (?:a|any) (?:marketing )?list/i)
  })

  it('E5: the page says the records in HubSpot, Resend and our own server can be deleted on request', () => {
    const write = blocks(html).find((b) => b.startsWith('Write to us'))
    expect(write, 'the "Write to us" item is gone').toBeDefined()
    for (const holder of [/hubspot/i, /resend/i, /our own (?:server|database)|our (?:server|database)|our own systems/i]) {
      expect(write, `the delete-on-request item does not name ${holder}`).toMatch(holder)
    }
    expect(write).toMatch(/delete/i)
    expect(write, 'a registrant record is not covered').toMatch(/regist/i)
  })

  it('C14: the page says the marketing sentence is stored with its time', () => {
    const quoted = blocks(html).filter((b) => b.includes(MARKETING_CONSENT_TEXT))
    expect(quoted.length, 'the page does not quote the marketing sentence').toBeGreaterThan(0)
    expect(quoted.length, 'the page quotes the marketing sentence for demo bookers and for registrants').toBeGreaterThanOrEqual(2)
    for (const b of quoted) expect(b, 'a quoted marketing sentence is not said to be stored with its time').toMatch(/stored|record/i)
    for (const b of quoted) expect(b.replace(MARKETING_CONSENT_TEXT, ''), 'a quoted marketing sentence is not said to be stored with its time').toMatch(/\btime\b|\bwhen you\b/i)
  })

  it('Resend already has a registrant\'s address at sign-up, so the page does not say nothing reaches Resend before verification', () => {
    expect(authSmtpHost(), 'control: the verification email no longer goes through Resend').toMatch(/resend\.com$/)
    const account = sentences(plain(section(/account|regist/i)))
    expect(account.length, 'control: the account section is empty').toBeGreaterThan(0)
    expect(
      account.filter((s) => /\b(?:nothing|no)\b[^.]*\bResend\b[^.]*\b(?:until|before)\b/i.test(s) && !/\b(?:else|other|more|apart|except|besides)\b/i.test(s)),
      'the page says nothing reaches Resend before verification; GoTrue mails the address through Resend at sign-up',
    ).toEqual([])
    expect(
      account.some((s) => /Resend/.test(s) && /verification|confirmation/i.test(s)),
      'no sentence says Resend delivers the verification email',
    ).toBe(true)
  })

  it('the server send to HubSpot follows the form and is queued, so the page does not say it happens at the same moment', () => {
    expect(demoPostsToOurServerAfterHubSpot(), 'control: DemoLeadForm no longer posts to HubSpot first').toBe(true)
    expect(deliveryIsQueued(), 'control: the delivery is no longer queued').toBe(true)
    const server = sentences(plain(section(/book a demo/i))).filter((s) => /our own server\b[^.]*\bHubSpot/i.test(s))
    expect(server.length, 'the demo section does not say what our server passes to HubSpot').toBeGreaterThan(0)
    expect(
      server.filter((s) => /same (?:moment|time)|at once|immediately|instantly|simultaneous/i.test(s)),
      'the page says the server send happens at the same moment',
    ).toEqual([])
  })

  it('no sentence says the marketing box is stored with the time you ticked it; the server stamps the submission', () => {
    expect(clientsSendConsentTime(), 'control: a client now sends its own consent time').toBe(false)
    expect(gatewayStampsRegisterConsentTime(), 'control: the gateway no longer stamps the consent time').toBe(true)
    const stored = blocks(html).filter((b) => b.includes(MARKETING_CONSENT_TEXT))
    expect(stored.length, 'control: the page quotes the marketing sentence').toBeGreaterThan(0)
    expect(stored.filter((b) => /time you ticked/i.test(b)), 'the page claims the time you ticked it').toEqual([])
    expect(stored.every((b) => /time you (?:submitted|sent|registered|signed up|booked)|when you (?:submitted|sent|registered|signed up|booked)/i.test(b))).toBe(true)
  })

  it('"HubSpot holds all of this" does not follow a paragraph about what Resend or our own server holds', () => {
    const paras = blocks(section(/book a demo/i))
    const at = paras.findIndex((b) => /EU servers/.test(b))
    expect(at, 'the EU-servers paragraph is gone').toBeGreaterThan(-1)
    expect(paras[at], 'the EU paragraph names HubSpot').toMatch(/HubSpot/)
    const before = paras.slice(0, at)
    expect(before.some((b) => b.includes('Resend')), 'control: a Resend paragraph precedes the EU paragraph').toBe(true)
    expect(before.some((b) => /our own server/.test(b)), 'control: an own-server paragraph precedes the EU paragraph').toBe(true)
    expect(paras[at], '"all of this" reaches the Resend and own-server paragraphs above it').not.toMatch(/\b(?:all of )?this\b/i)
  })
})

describe('the Library shares the landing\'s choice', () => {
  const html = renderToStaticMarkup(createElement(Privacy))
  const LANDING_ANALYTICS = readFileSync(join(SRC_DIR, '..', 'analytics.ts'), 'utf8')

  const text = (markup: string) =>
    markup
      .replace(/<[^>]+>/g, '')
      .replace(/&#x27;/g, "'")
      .replace(/&amp;/g, '&')
      .replace(/\s+/g, ' ')
      .trim()

  const LIB_HEADING = '>The Feature Library</h2>'
  const libraryParagraphs = (): string[] => {
    const at = html.indexOf(LIB_HEADING)
    const end = html.indexOf('<h2', at)
    return Array.from(html.slice(at, end).matchAll(/<p[^>]*>([\s\S]*?)<\/p>/g)).map((m) => text(m[1]))
  }

  // Pending PM approval of this copy.
  const LIBRARY_SECTION = [
    `Our Feature Library at ${LIBRARY_HOSTNAMES[0]} follows this policy too. One analytics choice covers this site and the Library: whatever you choose on either applies to both, and the other does not ask you again. Cookie choices, at the foot of this site's pages and of the Library's sidebar, brings the notice back on either, and a change made there applies to both.`,
    `If you allow analytics there, Google Analytics measures the Library in the same way and under the same property. Google also receives each Library page you view, when you choose Book the Demo, when you start the tour, and when you choose Open in Platform, with the feature or group it was for. The Library shares the _ga cookies with this site, so Google can tell that a visit to both came from the same browser. Choosing Reject on either site deletes those shared _ga cookies. If analytics was running, though, Google's script is still loaded into any of our pages you have open, and until you reload them it can send measurements of its own and re-create those cookies.`,
    'The Library has no forms, so it sends HubSpot nothing. It is typeset in the same Google fonts. When a Library page fails, your browser sends Sentry a report as described below; the Library does not send Sentry how long its pages take to load.',
  ]

  it('PL-01 the active-on sentence names both hosts from the lists', () => {
    const para = Array.from(html.matchAll(/<p[^>]*>([\s\S]*?)<\/p>/g))
      .map((m) => text(m[1]))
      .find((t) => t.includes('and nowhere else'))
    expect(para, 'the active-on paragraph is gone').toBeDefined()
    expect(para).toContain(PRODUCTION_HOSTNAMES[0])
    expect(para).toContain(LIBRARY_HOSTNAMES[0])
    const src = readFileSync(PRIVACY_TSX, 'utf8')
    expect(src).toMatch(/import\s*\{[^}]*LIBRARY_HOSTNAMES[^}]*\}\s*from\s*['"]\.\.\/hubspot['"]/)
    expect(src).not.toContain('library.ascomply.com')
  })

  it('PL-02 one Library section, between what it is not used for and Google Fonts', () => {
    expect(html.split(LIB_HEADING).length - 1, 'exactly one Library heading').toBe(1)
    const at = html.indexOf(LIB_HEADING)
    expect(at).toBeGreaterThan(html.indexOf('>What it is not used for</h2>'))
    expect(at).toBeLessThan(html.indexOf('>Google Fonts</h2>'))
    expect(libraryParagraphs()).toEqual(LIBRARY_SECTION)
  })

  it('PL-03 every library sender has its words on the page', () => {
    const senders = Array.from(
      LANDING_ANALYTICS.matchAll(/export function (trackLibrary\w+|trackTourStart|trackOpenInPlatform)\([^)]*\)[^{]*\{\s*send\('(\w+)'/g),
    ).map((m) => [m[1], m[2]] as const)
    const WORDS: Record<string, string> = {
      page_view: 'each Library page you view',
      demo_open: 'Book the Demo',
      tour_start: 'start the tour',
      open_in_platform: 'Open in Platform',
    }
    expect(senders.map(([, e]) => e).sort(), 'control: the four library events').toEqual(Object.keys(WORDS).sort())
    const section = libraryParagraphs().join(' ')
    for (const [fn, event] of senders) {
      expect(WORDS[event], `${fn} sends ${event}, which has no page words`).toBeDefined()
      expect(section, `${event} (${fn}) is not described in the Library section`).toContain(WORDS[event])
    }
  })

  it('PL-04 the attached-details sentence covers every parameter key', () => {
    const keys = new Set(Array.from(LANDING_ANALYTICS.matchAll(/send\('\w+', \{ (\w+):/g)).map((m) => m[1]))
    const target = LANDING_ANALYTICS.match(/trackOpenInPlatform\(target: ([^)]*)\)/)?.[1] ?? ''
    for (const m of target.matchAll(/(\w+): string/g)) keys.add(m[1])
    expect([...keys].sort()).toEqual(['cta_location', 'feature_id', 'form_name', 'group_id', 'percent_scrolled'])
    expect(text(html)).toContain('which feature or group you opened')
  })

  it('PL-05 the section states the one choice, the reopen on either site and the shared deletion', () => {
    const section = libraryParagraphs().join(' ')
    for (const needle of [
      'One analytics choice covers this site and the Library',
      'whatever you choose on either applies to both',
      'Cookie choices',
      'a change made there applies to both',
      'Choosing Reject on either site deletes those shared _ga cookies',
      'sends HubSpot nothing',
      'does not send Sentry how long',
    ]) {
      expect(section, `missing "${needle}"`).toContain(needle)
    }
    expect(section).not.toContain('ascomply.com as a whole')
    expect(section).not.toContain('either address')
  })

  it('PS-01 no sentence keeps the old separation or the old storage claims', () => {
    const page = text(html)
    expect(page.length, 'the page rendered no text').toBeGreaterThan(5000)
    expect(page, 'control: a shipped sentence is found by the same scan').toContain('Google Analytics sets two cookies on your device')
    for (const old of [
      'asks for your analytics choice itself',
      'keeps the answer for each address apart',
      'does not carry to the other',
      `are set for ${LIBRARY_HOSTNAMES[0]} only`,
      'Our own code sets no cookies at all',
      'though not as a cookie',
      'never sends anywhere',
    ]) {
      expect(page, `the page still says "${old}"`).not.toContain(old)
    }
  })

  it('PS-02 the record paragraph names the shared cookie from the constant', () => {
    const paras = Array.from(html.matchAll(/<p[^>]*>([\s\S]*?)<\/p>/g))
      .map((m) => text(m[1]))
      .filter((t) => t.includes('asc_consent'))
    expect(paras.length, 'exactly one paragraph names asc_consent').toBe(1)
    for (const needle of [
      'cookie of our own named asc_consent',
      `It is set for ${SHARED_COOKIE_DOMAIN}`,
      'our servers do not read it',
    ]) {
      expect(paras[0], `missing "${needle}"`).toContain(needle)
    }
  })

  it('PL-08 no sentence still scopes analytics to the landing alone', () => {
    expect(text(html)).not.toContain('It runs on this public site only.')
    const cookies = Array.from(html.matchAll(/<p[^>]*>([\s\S]*?)<\/p>/g))
      .map((m) => text(m[1]))
      .find((t) => t.includes('These two are set by Google, not by our own code'))
    expect(cookies).toContain('until then your cookie list for this site holds neither of them')
  })
})
