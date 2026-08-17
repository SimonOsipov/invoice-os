// T3-9 (no reload, no ga-disable), module-scope purity for the two new modules, and
// AC-13's mount wiring. Source-text and import-inertness only; the behaviour is in the
// jsdom files. Environment 'node' (vitest.config.ts).
/// <reference types="node" />
import { describe, expect, it } from 'vitest'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))

// Four files, not the whole tree: SignInModal.tsx:81 legitimately carries
// `window.location.href = dest`, so a tree-wide scan false-positives on shipped
// code. App.tsx is in the set because onChoose is where a reload would most
// naturally be smuggled back in.
const SCANNED: readonly (readonly [string, string])[] = [
  ['consentActions.ts', join(HERE, 'consentActions.ts')],
  ['analytics.ts', join(HERE, 'analytics.ts')],
  ['App.tsx', join(HERE, 'App.tsx')],
  ['components/CookieNotice.tsx', join(HERE, 'components', 'CookieNotice.tsx')],
]

// The property enforced is "these four files navigate nothing and disable nothing",
// not "these four literal spellings are absent". Measured at QA: the four dotted
// needles alone passed against nine working reloads — an aliased location, a computed
// member, document.location, a whole-object assignment, history.go(0), a programmatic
// anchor click and a concatenated ga-disable key. The receiver-free needles carry the
// guard; the dotted ones stay because they name the shapes USER DECISION 1 called out.
// A future legitimate navigation from one of these files must re-narrow this list with
// a stated reason rather than be waved through.
const FORBIDDEN: readonly (readonly [string, RegExp])[] = [
  ['location.reload', /location\.reload/],
  ['location.assign', /location\.assign/],
  ['location.href assignment', /location\.href\s*=/],
  ['ga-disable', /ga-disable/],
  ['reload, any receiver', /\breload\b/],
  ['assign, any receiver', /\bassign\b/],
  ['replace, any receiver', /\breplace\b/],
  // `const l = window.location` defeats every dotted needle above.
  ['location aliased to a binding', /=\s*(?:(?:window|document|self|top|globalThis)\.)?location\s*[;\n]/],
  ['whole-location assignment', /\blocation\s*=[^=]/],
  ['computed location member', /location\s*\[/],
  ['computed window member', /window\s*\[/],
  ['history navigation', /\bhistory\s*\./],
  // A self-href anchor clicked in script is a reload with no location reference.
  ['programmatic click', /\.click\s*\(/],
  // ga-disable assembled from fragments.
  ['ga- fragment', /ga-/],
  ['disable- fragment', /disable-/],
]

function readScanned(): { label: string; src: string }[] {
  const out: { label: string; src: string }[] = []
  for (const [label, path] of SCANNED) {
    expect(existsSync(path), `expected ${label} to exist at ${path}`).toBe(true)
    const src = readFileSync(path, 'utf8')
    // Population floor, per file: the control needle below proves ONE read
    // resolved, not four.
    expect(src.length, `${label}: read resolved to an empty file`).toBeGreaterThan(0)
    out.push({ label, src })
  }
  expect(out.length, 'the scan population shrank').toBe(SCANNED.length)
  return out
}

describe('T3-9: no page reload and no ga-disable (USER DECISION 1)', () => {
  it('control: each needle finds a planted hit', () => {
    expect(FORBIDDEN.length).toBe(15)
    const planted: Record<string, string> = {
      'location.reload': 'window.location.reload()',
      'location.assign': 'window.location.assign(dest)',
      'location.href assignment': 'window.location.href = dest',
      'ga-disable': "w['ga-disable-G-E409H76XYY'] = true",
      'reload, any receiver': 'const l = w.location; l.reload()',
      'assign, any receiver': 'const l = w.location; l.assign(dest)',
      'replace, any receiver': 'const l = w.location; l.replace(dest)',
      'location aliased to a binding': 'const l = window.location\n',
      'whole-location assignment': "window.location = '/'",
      'computed location member': "window.location['reload']()",
      'computed window member': "window['loc' + 'ation'].replace('/')",
      'history navigation': 'window.history.go(0)',
      'programmatic click': 'a.click()',
      'ga- fragment': "w['ga-' + 'disable-' + id] = true",
      'disable- fragment': "w['ga' + '-disable-' + id] = true",
    }
    for (const [label, re] of FORBIDDEN) {
      expect(planted[label], `${label}: no planted sample`).toBeDefined()
      expect(re.test(planted[label]), `${label}: planted hit not found`).toBe(true)
    }
  })

  it('control: the needles reject the shapes the four files legitimately carry', () => {
    // A read of location.href is not an assignment to it, and the pathname read
    // App.tsx already makes must stay legal.
    const legal = [
      'const here = window.location.href',
      'const privacy = isPrivacyPath(window.location.pathname)',
      "const hostname = opts?.hostname ?? window.location.hostname",
      "w.gtag('config', id)",
    ]
    for (const sample of legal) {
      for (const [label, re] of FORBIDDEN) {
        expect(re.test(sample), `${label} false-positives on shipped code: ${sample}`).toBe(false)
      }
    }
  })

  it('AC-5: none of the four files reloads the page or sets ga-disable', () => {
    const files = readScanned()
    // Durable control needle: present in App.tsx and analytics.ts today and after
    // the change, so it proves the reads resolved rather than the seam existing.
    const combined = files.map((f) => f.src).join('\n')
    expect(combined, 'control needle "trackDemoOpen" not found — the scan is not reading files').toContain(
      'trackDemoOpen',
    )
    for (const { label, src } of files) {
      for (const [needle, re] of FORBIDDEN) {
        expect(re.test(src), `${label} carries "${needle}"`).toBe(false)
      }
    }
  })
})

describe('the revocation seam is in analytics.ts, not a second copy of load state', () => {
  it('AC-2/AC-3: revoked, tagIsLoaded and setAnalyticsRevoked exist and gate send()', () => {
    const src = readFileSync(join(HERE, 'analytics.ts'), 'utf8')
    expect(src.length).toBeGreaterThan(0)
    expect(src).toContain('trackDemoOpen')

    expect(src).toMatch(/let revoked = false/)
    expect(src).toMatch(/export function tagIsLoaded\(\)/)
    expect(src).toMatch(/export function setAnalyticsRevoked\(/)
    // The single choke point. docs/privacy-policy-claims.md quotes this guard
    // verbatim under W5, so the two must be corrected together.
    expect(src).toMatch(/if\s*\(\s*!loaded\s*\|\|\s*revoked\s*\)\s*return/)
    // Load state stays one flag: no re-derivation from the injected script tag.
    expect(src).not.toContain('script[src*=googletagmanager]')
  })
})

describe('module-scope purity of the two new modules', () => {
  // Both enter App.tsx's import graph, and App.render.test.tsx / App.route.test.ts
  // run under environment 'node' with no document. A module-scope `const DOC =
  // document` crashes both at import; a default parameter is evaluated at call
  // time and is safe. Same shape as analytics.test.ts's own purity case.
  it('AC-8: importing gaCookies and consentActions under node touches no browser global', async () => {
    expect(globalThis.window).toBeUndefined()
    expect(globalThis.document).toBeUndefined()

    for (const [label, path, specifier] of [
      ['gaCookies.ts', join(HERE, 'gaCookies.ts'), './gaCookies'],
      ['consentActions.ts', join(HERE, 'consentActions.ts'), './consentActions'],
    ] as const) {
      expect(existsSync(path), `expected ${label} to exist at ${path}`).toBe(true)
      await expect(import(specifier), `${label} threw on import under node`).resolves.toBeDefined()
    }

    expect(globalThis.window).toBeUndefined()
    expect(globalThis.document).toBeUndefined()
  })
})

describe('AC-13: the mount and its suppression wiring in App.tsx', () => {
  const APP_SRC = readFileSync(join(HERE, 'App.tsx'), 'utf8')

  it('control: the App source read resolved', () => {
    expect(APP_SRC.length).toBeGreaterThan(0)
    expect(APP_SRC).toContain('onBookDemo')
  })

  it('AC-7: exactly one CookieNotice, and no hand-written spacer beside it', () => {
    // CookieNotice already emits .cn-spacer inside its own fragment, so a second
    // hand-written spacer would double the mobile scroll room.
    expect(Array.from(APP_SRC.matchAll(/<CookieNotice\b/g)).length, 'expected exactly one <CookieNotice>').toBe(1)
    expect(APP_SRC, 'App.tsx writes its own cn-spacer').not.toContain('cn-spacer')
  })

  it('AC-7: the notice mounts after Footer and before the modals', () => {
    // Load-bearing, not cosmetic: mounting it INSIDE <Footer> turns
    // e2e/smoke/landing-privacy.spec.ts:122 and :130 red at count 2, because both
    // scope a[href="/privacy"] to the contentinfo role. Last in flow also puts the
    // spacer's scroll room at the document end and the tab order after the footer.
    const footerIdx = APP_SRC.indexOf('<Footer')
    const noticeIdx = APP_SRC.indexOf('<CookieNotice')
    const signInIdx = APP_SRC.indexOf('{signInOpen &&')
    expect(footerIdx, 'expected a <Footer> element').toBeGreaterThan(-1)
    expect(signInIdx, 'expected the conditional SignInModal').toBeGreaterThan(-1)
    expect(noticeIdx, 'expected a <CookieNotice> element').toBeGreaterThan(-1)
    expect(noticeIdx, 'the notice must mount after <Footer>').toBeGreaterThan(footerIdx)
    expect(noticeIdx, 'the notice must mount before the modals').toBeLessThan(signInIdx)
  })

  it('AC-13: suppressed derives from both modal flags, each with exactly one true-setter', () => {
    const tag = APP_SRC.match(/<CookieNotice\b[\s\S]*?\/>/)
    expect(tag, 'expected a self-closing <CookieNotice .../> element').not.toBeNull()
    expect(tag![0]).toMatch(/suppressed=\{\s*signInOpen\s*\|\|\s*demoOpen\s*\}/)

    // Because suppressed derives from the two state variables rather than from the
    // call sites, no opening path can bypass it — but only while each flag keeps
    // one setter. A second setter would reintroduce the bypass silently.
    expect(Array.from(APP_SRC.matchAll(/setSignInOpen\(true\)/g)).length, 'signInOpen true-setters').toBe(1)
    expect(Array.from(APP_SRC.matchAll(/setDemoOpen\(true\)/g)).length, 'demoOpen true-setters').toBe(1)
  })
})
