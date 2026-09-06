import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { clampFilterText } from './invoices'
import { ROUTE_PATHS, routePath, parseRoute, parseLocation, routeUrl } from './route'

const ALL_VIEWS = [
  'dashboard',
  'invoices',
  'rules',
  'workflows',
  'create',
  'detail',
  'clients',
  'customers',
  'reports',
  'settings',
  'approvals',
  'audit',
  'extraction',
] as const

// types.ts:208 — six members. The strip SettingsView renders is the *available* set and is
// one shorter in a firm workspace; this is the addressable set.
const ALL_SETTINGS_TABS = ['members', 'roles', 'connectors', 'api', 'signing', 'company'] as const

// A well-formed id and its upper-case twin: parse must preserve case, not normalise it.
const UUID = 'a1b2c3d4-e5f6-47a8-89ab-cdef01234567'
const UUID_UPPER = UUID.toUpperCase()

// Splits what routeUrl returns back into the two arguments parseLocation takes, the way a
// browser splits an href into pathname and search.
function splitUrl(url: string): [string, string] {
  const i = url.indexOf('?')
  return i === -1 ? [url, ''] : [url.slice(0, i), url.slice(i)]
}

function searchOf(params: Record<string, string>): string {
  return `?${new URLSearchParams(params).toString()}`
}

const APP_TSX = fileURLToPath(new URL('../App.tsx', import.meta.url))
const ROUTE_TS = fileURLToPath(new URL('./route.ts', import.meta.url))
const PACKAGE_JSON = fileURLToPath(new URL('../../package.json', import.meta.url))

// Both DOM-scan tests below call this -- a typo'd pattern would report a clean zero on
// route.ts exactly like a real zero, so the control needle over App.tsx must use it too.
function domTokenHits(src: string): number {
  return (src.match(/\b(?:window|document|history)\b/g) ?? []).length
}

describe('ROUTE_PATHS', () => {
  it('routeTable_isTotalOverTheThirteenViews', () => {
    expect(ALL_VIEWS.length).toBe(13)
    expect(Object.keys(ROUTE_PATHS).sort()).toEqual([...ALL_VIEWS].sort())
    expect(Object.keys(ROUTE_PATHS).length).toBe(13)
  })

  it('routeTable_everyPathIsDistinct', () => {
    expect(new Set(Object.values(ROUTE_PATHS)).size).toBe(13)
  })
})

describe('routePath / parseRoute round trip', () => {
  it('roundTrip_everyViewSerialisesAndParsesBackToItself', () => {
    expect(ALL_VIEWS.length).toBe(13)
    for (const v of ALL_VIEWS) {
      expect(parseRoute(routePath(v))).toBe(v)
    }
  })

  it('serialize_dashboardIsTheBareRoot', () => {
    expect(routePath('dashboard')).toBe('/')
  })
})

describe('parseRoute — strict, case-sensitive, exact', () => {
  it('parse_refusesADrillDownPathRatherThanDegradingToTheList', () => {
    expect(parseRoute('/invoices/a1b2c3d4-e5f6-47a8-89ab-cdef01234567')).toBeNull()
  })

  it('parse_refusesAnUnknownPathAndTheEmptyString', () => {
    expect(parseRoute('/nonsense')).toBeNull()
    expect(parseRoute('')).toBeNull()
    expect(parseRoute('/invoices/x/y')).toBeNull()
  })

  it('parse_isCaseSensitive', () => {
    expect(parseRoute('/Invoices')).toBeNull()
    expect(parseRoute('/AUDIT')).toBeNull()
    expect(parseRoute('/invoices')).toBe('invoices') // control needle: the lowercase form must still resolve
  })

  it('parse_toleratesExactlyOneTrailingSlash', () => {
    expect(parseRoute('/invoices/')).toBe('invoices')
    expect(parseRoute('/invoices//')).toBeNull()
  })
})

describe('the codec never touches the DOM', () => {
  it('codec_theDomScanCanSeeAMatch', () => {
    const src = readFileSync(APP_TSX, 'utf8')
    expect(domTokenHits(src)).toBeGreaterThan(0)
    expect(src).toMatch(/\bwindow\b/)
    expect(src).toMatch(/\bdocument\b/)
    expect(src).toMatch(/\bhistory\b/)
  })

  it('codec_neverTouchesTheDom', () => {
    const src = readFileSync(ROUTE_TS, 'utf8')
    expect(domTokenHits(src)).toBe(0)
  })
})

describe('frontend/app/package.json', () => {
  it('packageJson_runtimeDependenciesAreUnchanged', () => {
    const pkg = JSON.parse(readFileSync(PACKAGE_JSON, 'utf8')) as { dependencies?: Record<string, string> }
    expect(Object.keys(pkg.dependencies ?? {}).sort()).toEqual([
      '@invoice-os/api-client',
      '@invoice-os/design-tokens',
      'react',
      'react-dom',
    ])
  })
})

describe('parseRoute — adversarial', () => {
  // location.pathname never carries these in a real browser, but the function is exported
  // and pure, so a caller passing a full href by mistake gets null, not a silent match.
  it('parse_ignoresAPathnameArgumentCarryingAQueryStringOrHash', () => {
    expect(parseRoute('/invoices?x=1')).toBeNull()
    expect(parseRoute('/invoices#y')).toBeNull()
  })

  it('parse_rejectsLeadingOrTrailingWhitespace', () => {
    expect(parseRoute(' /invoices')).toBeNull()
    expect(parseRoute('/invoices ')).toBeNull()
  })

  it('parse_distinguishesAPathFromAnAdjacentRealPathThatPrefixesIt', () => {
    // /invoice and /invoices are both real routes (detail, invoices) -- neither may degrade to the other.
    expect(parseRoute('/invoice')).toBe('detail')
    expect(parseRoute('/invoices')).toBe('invoices')
    expect(parseRoute('/audi')).toBeNull() // prefix of /audit, not a route itself
  })

  it('parse_rejectsADoubleLeadingSlash', () => {
    expect(parseRoute('//invoices')).toBeNull()
  })

  it('routePath_isInjectiveNotMerelyDistinctToday', () => {
    expect(ALL_VIEWS.length).toBeGreaterThan(0)
    for (let i = 0; i < ALL_VIEWS.length; i++) {
      for (let j = i + 1; j < ALL_VIEWS.length; j++) {
        const a = ALL_VIEWS[i]
        const b = ALL_VIEWS[j]
        expect(routePath(a), `${a} and ${b} must not share a path`).not.toBe(routePath(b))
      }
    }
  })
})

describe('routeUrl — serialise', () => {
  it('routeUrl_withNoParamsEqualsRoutePathForAllThirteen', () => {
    expect(ALL_VIEWS.length).toBe(13)
    for (const v of ALL_VIEWS) {
      expect(routeUrl(v), `${v} must serialise to its shipped path`).toBe(routePath(v))
    }
  })

  it('routeUrl_emitsOnlyTheParamsTheViewOwns', () => {
    // invoices owns q, audit owns invoice, settings owns the tab segment; no other view owns anything.
    expect(routeUrl('audit', { q: 'acme', settingsTab: 'roles' })).toBe('/audit')
    expect(routeUrl('invoices', { auditInvoice: UUID, settingsTab: 'roles' })).toBe('/invoices')
    expect(routeUrl('settings', { q: 'acme', auditInvoice: UUID })).toBe('/settings')
    expect(routeUrl('dashboard', { q: 'acme', auditInvoice: UUID, settingsTab: 'roles' })).toBe('/')
    expect(routeUrl('reports', { q: 'acme', auditInvoice: UUID })).toBe('/reports')
    // Control needle: the emitter still emits when the view does own the param.
    expect(routeUrl('invoices', { q: 'acme' })).toBe('/invoices?q=acme')
  })

  it('routeUrl_serialisesTheOwnedParamOnTheOwningView', () => {
    expect(routeUrl('invoices', { q: 'acme' })).toBe('/invoices?q=acme')
    expect(routeUrl('audit', { auditInvoice: UUID })).toBe(`/audit?invoice=${UUID}`)
    // R2: the empty forms serialise to nothing at all.
    expect(routeUrl('invoices', { q: '' })).toBe('/invoices')
    expect(routeUrl('audit', { auditInvoice: null })).toBe('/audit')
  })

  it('routeUrl_omitsTheDefaultSettingsTab', () => {
    expect(ALL_SETTINGS_TABS.length).toBe(6)
    expect(routeUrl('settings')).toBe('/settings')
    expect(routeUrl('settings', { settingsTab: 'members' })).toBe('/settings')
    const nonDefault = ALL_SETTINGS_TABS.filter((t) => t !== 'members')
    expect(nonDefault.length).toBe(5)
    for (const t of nonDefault) {
      expect(routeUrl('settings', { settingsTab: t }), `${t} is addressable`).toBe(`/settings/${t}`)
    }
  })
})

describe('parseLocation — the inverse of routeUrl', () => {
  it('roundTrip_aSearchTermWithReservedCharactersSurvives', () => {
    const terms = ['a&b=c#d eé', 'a+b'] as const
    expect(terms.length).toBe(2)
    for (const q of terms) {
      const url = routeUrl('invoices', { q })
      expect(url.startsWith('/invoices?'), `${q} must serialise into a query`).toBe(true)
      expect(url.includes('#'), 'routeUrl never emits a hash').toBe(false)
      const [pathname, search] = splitUrl(url)
      expect(parseLocation(pathname, search).q, `${q} must survive the round trip`).toBe(q)
    }
    // The reserved characters travel percent-encoded, not raw.
    expect(routeUrl('invoices', { q: 'a&b=c#d eé' })).toBe('/invoices?q=a%26b%3Dc%23d+e%C3%A9')
  })

  it('parseLocation_isTotalOverHostileInput', () => {
    const big = 'é'.repeat(5000) // 10 KB of UTF-8
    // Pathname choice is load-bearing: q is read only under /invoices, invoice only under /audit.
    const hostile: readonly (readonly [string, string])[] = [
      ['', ''],
      ['/', ''],
      ['/settings/', ''],
      ['/invoices', '?q='],
      ['/invoices', '?q'],
      ['/invoices', '?q=a&q=b'],
      ['/invoices', searchOf({ q: big })],
      ['/invoices', '?q=%E4'], // a lone byte: URLSearchParams yields U+FFFD and never throws
      ['/audit', '?invoice='],
      ['/audit', '?'],
    ]
    expect(hostile.length).toBe(10)
    for (const [pathname, search] of hostile) {
      const label = `${JSON.stringify(pathname)} ${JSON.stringify(search)}`
      const parsed = parseLocation(pathname, search)
      expect(typeof parsed.q, label).toBe('string')
      expect((ALL_SETTINGS_TABS as readonly string[]).includes(parsed.settingsTab), label).toBe(true)
      expect(parsed.auditInvoice === null || typeof parsed.auditInvoice === 'string', label).toBe(true)
    }
    // Repeated param: first wins. Valueless and absent are the empty string, never undefined.
    expect(parseLocation('/invoices', '?q=a&q=b').q).toBe('a')
    expect(parseLocation('/invoices', '?q=').q).toBe('')
    expect(parseLocation('/invoices', '?q').q).toBe('')
    expect(parseLocation('/invoices', '').q).toBe('')
    // Control needle: totality is not reached by returning an empty result for everything.
    expect(parseLocation('/invoices', '?q=acme').q).toBe('acme')
  })

  it('parseLocation_refusesADrillDownExactlyAsParseRouteDoes', () => {
    expect(parseLocation('/invoices/a1b2c3d4-e5f6-47a8-89ab-cdef01234567', '').view).toBeNull()
    expect(parseLocation('/invoices/x/y', '').view).toBeNull()
    expect(parseLocation('/settings/roles/extra', '').view).toBeNull()
    expect(parseLocation('/settings//', '').view).toBeNull()
    expect(parseLocation('/nonsense', '').view).toBeNull()
    // Control needles: one segment still resolves, and /settings is the only path taking two.
    expect(parseLocation('/invoices', '').view).toBe('invoices')
    expect(parseLocation('/settings/roles', '').view).toBe('settings')
  })

  it('parseLocation_clampsAnOversizedQueryAtAByteBoundary', () => {
    const q = 'é'.repeat(300) // 600 UTF-8 bytes
    const parsed = parseLocation('/invoices', searchOf({ q }))
    expect(parsed.q).toBe(clampFilterText(q))
    expect(new TextEncoder().encode(parsed.q).length).toBeLessThanOrEqual(200)
    expect(parsed.q.length).toBeGreaterThan(0)
    expect(parsed.q.includes('�'), 'a multi-byte character was split').toBe(false)
    // Control needle: a term under the cap comes back untouched.
    expect(parseLocation('/invoices', '?q=acme').q).toBe('acme')
  })

  it('parseLocation_dropsAMalformedInvoiceId', () => {
    const malformed = ['not-a-uuid', '123', '', `${UUID} `, ` ${UUID}`, `${UUID}/extra`, UUID.replace(/-/g, '')]
    expect(malformed.length).toBe(7)
    for (const bad of malformed) {
      const parsed = parseLocation('/audit', searchOf({ invoice: bad }))
      expect(parsed.auditInvoice, `must drop ${JSON.stringify(bad)}`).toBeNull()
    }
    // Control needles: a well-formed id comes back verbatim, in either case.
    expect(parseLocation('/audit', `?invoice=${UUID}`).auditInvoice).toBe(UUID)
    expect(parseLocation('/audit', `?invoice=${UUID_UPPER}`).auditInvoice).toBe(UUID_UPPER)
    // R1 on the read side: invoice is audit's param, q is the invoices list's.
    expect(parseLocation('/invoices', `?invoice=${UUID}`).auditInvoice).toBeNull()
    expect(parseLocation('/audit', '?q=acme').q).toBe('')
  })

  it('parseLocation_fallsBackToMembersForAnUnknownTab', () => {
    // constructor and toString are prototype keys: a `seg in table` lookup would accept both.
    const unknown = [
      '/settings',
      '/settings/',
      '/settings/nonsense',
      '/settings/MEMBERS',
      '/settings/constructor',
      '/settings/toString',
    ]
    expect(unknown.length).toBe(6)
    for (const pathname of unknown) {
      const parsed = parseLocation(pathname, '')
      expect(parsed.view, pathname).toBe('settings')
      expect(parsed.settingsTab, pathname).toBe('members')
    }
    // Control needles: every real tab resolves to itself through the codec.
    expect(parseLocation('/settings/roles', '').settingsTab).toBe('roles')
    expect(ALL_SETTINGS_TABS.length).toBe(6)
    for (const t of ALL_SETTINGS_TABS) {
      const [pathname, search] = splitUrl(routeUrl('settings', { settingsTab: t }))
      expect(parseLocation(pathname, search).settingsTab, t).toBe(t)
    }
  })
})
