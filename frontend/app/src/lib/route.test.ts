import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { clampFilterText } from './invoices'
import {
  ROUTE_PATHS,
  routePath,
  parseRoute,
  parseLocation,
  routeUrl,
  reviewPath,
  parseReviewPath,
  reviewNavIds,
  REVIEW_PATH_MAX_IDS,
} from './route'
import { MAX_RUN_FILES } from './importRun'

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
const ROUTING_DOC = fileURLToPath(new URL('../../../../docs/routing.md', import.meta.url))
const TYPES_TS = fileURLToPath(new URL('../types.ts', import.meta.url))

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
      expect(parseRoute(routePath(v))).toEqual({ view: v, id: null })
    }
  })

  it('serialize_dashboardIsTheBareRoot', () => {
    expect(routePath('dashboard')).toBe('/')
  })
})

describe('parseRoute — strict, case-sensitive, exact', () => {
  it('parse_refusesAnUnknownPathAndTheEmptyString', () => {
    expect(parseRoute('/nonsense')).toBeNull()
    expect(parseRoute('')).toBeNull()
    expect(parseRoute('/invoices/x/y')).toBeNull()
  })

  it('parse_isCaseSensitive', () => {
    expect(parseRoute('/Invoices')).toBeNull()
    expect(parseRoute('/AUDIT')).toBeNull()
    expect(parseRoute('/invoices')).toEqual({ view: 'invoices', id: null }) // control needle: the lowercase form must still resolve
  })

  it('parse_toleratesExactlyOneTrailingSlash', () => {
    expect(parseRoute('/invoices/')).toEqual({ view: 'invoices', id: null })
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
    expect(parseRoute('/invoice')).toEqual({ view: 'detail', id: null })
    expect(parseRoute('/invoices')).toEqual({ view: 'invoices', id: null })
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
  it('routeUrl_withNoParamsEqualsRoutePathExceptSettings', () => {
    expect(ALL_VIEWS.length).toBe(13)
    let compared = 0
    for (const v of ALL_VIEWS) {
      if (v === 'settings') continue
      expect(routeUrl(v), `${v} must serialise to its shipped path`).toBe(routePath(v))
      compared += 1
    }
    expect(compared, 'the twelve non-settings views must each have been compared').toBe(12)
    // settings is the one named exception, asserted rather than excused: the default tab
    // is emitted, so the URL is strictly longer than the path. Both literals are pinned --
    // an inequality alone would pass on any wrong value.
    expect(routeUrl('settings'), 'settings is the one view whose URL is not its path').not.toBe(
      routePath('settings'),
    )
    expect(routePath('settings')).toBe('/settings')
    expect(routeUrl('settings')).toBe('/settings/members')
  })

  it('routeUrl_emitsOnlyTheParamsTheViewOwns', () => {
    // invoices owns q, audit owns invoice, settings owns the tab segment; no other view owns anything.
    expect(routeUrl('audit', { q: 'acme', settingsTab: 'roles' })).toBe('/audit')
    expect(routeUrl('invoices', { auditInvoice: UUID, settingsTab: 'roles' })).toBe('/invoices')
    expect(routeUrl('settings', { q: 'acme', auditInvoice: UUID })).toBe('/settings/members')
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

  it('routeUrl_alwaysNamesTheSettingsTabIncludingTheDefault', () => {
    expect(ALL_SETTINGS_TABS.length).toBe(6)
    expect(routeUrl('settings')).toBe('/settings/members')
    expect(routeUrl('settings', { settingsTab: 'members' })).toBe('/settings/members')
    expect(routeUrl('settings', { settingsTab: undefined })).toBe('/settings/members')
    for (const t of ALL_SETTINGS_TABS) {
      expect(routeUrl('settings', { settingsTab: t }), `${t} is addressable`).toBe(`/settings/${t}`)
    }
    // Floor: the six are not five defaults plus members -- members is one of the six.
    const nonDefault = ALL_SETTINGS_TABS.filter((t) => t !== 'members')
    expect(nonDefault.length).toBe(5)
  })

  // ALL_SETTINGS_TABS is a local literal, so the `.length === 6` floor above asserts it
  // against itself: a seventh tab added to types.ts would leave every tab loop in this file
  // one short and silent. ROUTE_PATHS has such an anchor already
  // (routeTable_isTotalOverTheThirteenViews); the tab set had none.
  it('settingsTabs_theTestTableIsAnchoredToTheShippedUnion', () => {
    const src = readFileSync(TYPES_TS, 'utf8')
    expect(src.length, 'types.ts read back empty -- the path is broken').toBeGreaterThan(0)
    const line = src.split('\n').find((l) => l.startsWith('export type SettingsTab ='))
    expect(line, 'types.ts no longer declares `export type SettingsTab =` on one line').toBeTruthy()
    const shipped = (line ?? '').match(/'[a-z]+'/g)?.map((m) => m.slice(1, -1)) ?? []
    // Needle: a regex that matched nothing would make the set comparison below vacuous.
    expect(shipped.length, 'the union scan matched no member -- it would prove nothing').toBe(6)
    expect(shipped.sort(), 'the shipped union and this file\'s table must name the same tabs').toEqual(
      [...ALL_SETTINGS_TABS].sort(),
    )
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

  it('parseLocation_totalityFallsBackToDashboardOnAnythingParseRouteRefuses', () => {
    // parseRoute itself still refuses these; totality (D1) means the view that comes
    // out the other side is 'dashboard', never null.
    expect(parseLocation('/invoices/x/y', '').view).toBe('dashboard')
    expect(parseLocation('/settings/roles/extra', '').view).toBe('dashboard')
    expect(parseLocation('/settings//', '').view).toBe('dashboard')
    expect(parseLocation('/nonsense', '').view).toBe('dashboard')
    // Control needles: one segment still resolves, /settings is the only path taking
    // two, and a drill-down is no longer refused -- it is a legal `detail` route (ROUTE-02).
    expect(parseLocation('/invoices', '').view).toBe('invoices')
    expect(parseLocation('/settings/roles', '').view).toBe('settings')
    expect(parseLocation('/invoices/a1b2c3d4-e5f6-47a8-89ab-cdef01234567', '')).toMatchObject({
      view: 'detail',
      invoiceId: 'a1b2c3d4-e5f6-47a8-89ab-cdef01234567',
    })
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

describe('the codec — adversarial', () => {
  it('routeUrl_treatsAnAbsentAndAnExplicitlyUndefinedParamAlike', () => {
    expect(routeUrl('invoices', undefined)).toBe('/invoices')
    expect(routeUrl('invoices', {})).toBe('/invoices')
    expect(routeUrl('invoices', { q: undefined })).toBe('/invoices')
    expect(routeUrl('audit', { auditInvoice: undefined })).toBe('/audit')
    expect(routeUrl('settings', { settingsTab: undefined })).toBe(routeUrl('settings'))
    expect(routeUrl('settings', { settingsTab: undefined })).toBe('/settings/members')
    // Control needle: the emitter is not simply ignoring its second argument.
    expect(routeUrl('invoices', { q: 'acme' })).toBe('/invoices?q=acme')
  })

  it('roundTrip_aSearchTermThatIsItselfAQueryStringStaysData', () => {
    const q = `?invoice=${UUID}&foo=1`
    const url = routeUrl('invoices', { q })
    // Exact form: the embedded separators travel encoded, so the URL carries one param.
    expect(url).toBe(`/invoices?q=%3Finvoice%3D${UUID}%26foo%3D1`)
    const [pathname, search] = splitUrl(url)
    expect(parseLocation(pathname, search).q).toBe(q)
    // The embedded `invoice=` never becomes structure: the same search read under the view
    // that DOES own `invoice` still finds none.
    expect(parseLocation('/audit', search).auditInvoice).toBeNull()
    // Control needle: a genuine invoice param under /audit is found.
    expect(parseLocation('/audit', `?invoice=${UUID}`).auditInvoice).toBe(UUID)
  })

  it('roundTrip_survivesCharactersUrlSearchParamsEncodesSpecially', () => {
    const terms = ['%41', '100%', 'a\nb', 'a\tb', 'a&b', 'a=b', '?', '#', ' ', '+', '='] as const
    expect(terms.length).toBe(11)
    for (const q of terms) {
      const label = JSON.stringify(q)
      const url = routeUrl('invoices', { q })
      expect(url.indexOf('?'), `${label} must produce exactly one separator`).toBe(url.lastIndexOf('?'))
      expect(url.includes('#'), `${label} must not emit a raw hash`).toBe(false)
      const [pathname, search] = splitUrl(url)
      expect(parseLocation(pathname, search).q, label).toBe(q)
    }
    // A percent sign is encoded once, not passed through: a double decode would yield 'A'.
    expect(routeUrl('invoices', { q: '%41' })).toBe('/invoices?q=%2541')
  })

  it('parseLocation_refusesAPathnameThatIsAWholeUrlOrProtocolRelative', () => {
    const hostile = [
      'https://evil.example/audit',
      '//evil.example/audit',
      'http://evil.example/settings/roles',
      '//settings/roles',
      '//invoices',
    ] as const
    expect(hostile.length).toBe(5)
    // The same searches that DO resolve under a real path, so an empty result is the gate
    // working rather than the fixture proving nothing.
    const searches = [`?invoice=${UUID}`, '?q=acme'] as const
    expect(searches.length).toBe(2)
    for (const pathname of hostile) {
      for (const search of searches) {
        const label = `${pathname} ${search}`
        const parsed = parseLocation(pathname, search)
        expect(parsed.view, label).toBe('dashboard')
        expect(parsed.settingsTab, label).toBe('members')
        expect(parsed.q, label).toBe('')
        expect(parsed.auditInvoice, label).toBeNull()
      }
    }
    // Control needles: both searches carry a value on the path that owns them.
    expect(parseLocation('/audit', searches[0]).auditInvoice).toBe(UUID)
    expect(parseLocation('/invoices', searches[1]).q).toBe('acme')
  })

  it('parseLocation_matchesParamAndSegmentNamesCaseSensitively', () => {
    expect(parseLocation('/invoices', '?Q=acme').q).toBe('')
    expect(parseLocation('/audit', `?INVOICE=${UUID}`).auditInvoice).toBeNull()
    expect(parseLocation('/settings/ROLES', '').settingsTab).toBe('members')
    // Control needles: the lower-case forms all resolve.
    expect(parseLocation('/invoices', '?q=acme').q).toBe('acme')
    expect(parseLocation('/audit', `?invoice=${UUID}`).auditInvoice).toBe(UUID)
    expect(parseLocation('/settings/roles', '').settingsTab).toBe('roles')
  })

  it('parseLocation_acceptsAnInvoiceIdByShapeNotByRfcVariant', () => {
    // The audit reader parses ids with Go's uuid.Parse, which is shape-only too; tightening
    // to a version/variant check here would drop ids the server happily serves.
    const shaped = ['00000000-0000-0000-0000-000000000000', 'a1b2c3d4-e5f6-97a8-f9ab-cdef01234567'] as const
    expect(shaped.length).toBe(2)
    for (const id of shaped) {
      expect(parseLocation('/audit', `?invoice=${id}`).auditInvoice, id).toBe(id)
    }
    // Control needle: one hex digit short is still refused, so the check is not a no-op.
    expect(parseLocation('/audit', '?invoice=a1b2c3d4-e5f6-47a8-89ab-cdef0123456').auditInvoice).toBeNull()
  })

  it('parseLocation_readsASearchStringWithOrWithoutItsLeadingQuestionMark', () => {
    expect(parseLocation('/invoices', '?q=acme').q).toBe('acme')
    expect(parseLocation('/invoices', 'q=acme').q).toBe('acme')
    // A doubled prefix degrades to nothing rather than to a wrong value.
    expect(parseLocation('/invoices', '??q=acme').q).toBe('')
  })

  it('roundTrip_aTermAtTheByteCapSurvivesAndOneByteOverIsClamped', () => {
    const atCap = 'é'.repeat(100) // exactly 200 UTF-8 bytes
    expect(new TextEncoder().encode(atCap).length).toBe(200)
    const [p1, s1] = splitUrl(routeUrl('invoices', { q: atCap }))
    expect(parseLocation(p1, s1).q).toBe(atCap)

    // One ASCII byte over: the clamp lands exactly on the cap and drops only the extra.
    const [p2, s2] = splitUrl(routeUrl('invoices', { q: `${atCap}x` }))
    expect(parseLocation(p2, s2).q).toBe(atCap)

    // One multi-byte character over: the clamp retreats to the character boundary, so the
    // result is still atCap and carries no replacement character.
    const [p3, s3] = splitUrl(routeUrl('invoices', { q: 'é'.repeat(101) }))
    const parsed = parseLocation(p3, s3)
    expect(parsed.q).toBe(atCap)
    expect(parsed.q.includes('�')).toBe(false)
  })

  it('routeUrl_escapesAnUnvalidatedAuditIdRatherThanValidatingIt', () => {
    // Serialise trusts its caller; parse is the validating half. Anything injected is data.
    expect(routeUrl('audit', { auditInvoice: 'x&q=y' })).toBe('/audit?invoice=x%26q%3Dy')
    const [pathname, search] = splitUrl(routeUrl('audit', { auditInvoice: 'x&q=y' }))
    const parsed = parseLocation(pathname, search)
    expect(parsed.auditInvoice).toBeNull()
    expect(parsed.q).toBe('')
    // Control needle: a well-formed id makes the same round trip intact.
    const [p, s] = splitUrl(routeUrl('audit', { auditInvoice: UUID }))
    expect(parseLocation(p, s).auditInvoice).toBe(UUID)
  })
})
describe('parseRoute — drill-down (detail, extraction)', () => {
  const UUID = 'a1b2c3d4-e5f6-47a8-89ab-cdef01234567'

  it('parse_invoicesDrillDownParsesToDetailWithId', () => {
    expect(parseRoute(`/invoices/${UUID}`)).toEqual({ view: 'detail', id: UUID })
  })

  it('parse_extractionDrillDownParsesToExtractionWithId', () => {
    expect(parseRoute('/extraction/j1')).toEqual({ view: 'extraction', id: 'j1' })
  })

  it('parse_invoicesListStaysTheListNotADetail', () => {
    expect(parseRoute('/invoices')).toEqual({ view: 'invoices', id: null })
  })

  it('parse_refusesAThreeSegmentDrillDownPath', () => {
    // over-matching floor: a two-segment arm must not also swallow a third segment
    expect(parseRoute(`/invoices/${UUID}/edit`)).toBeNull()
  })

  it('parse_malformedPercentEscapeReturnsNullRatherThanThrowing', () => {
    expect(() => parseRoute('/invoices/%zz')).not.toThrow()
    expect(parseRoute('/invoices/%zz')).toBeNull()
  })
})

describe('routePath — drill-down id parameter', () => {
  const UUID = 'a1b2c3d4-e5f6-47a8-89ab-cdef01234567'

  it('serialize_detailEncodesTheIdOrFallsBackToTheBareFloor', () => {
    expect(routePath('detail', UUID)).toBe(`/invoices/${encodeURIComponent(UUID)}`)
    expect(routePath('detail', null)).toBe('/invoice')
  })

  it('serialize_idIsIgnoredForTheTenViewsThatDoNotTakeOne', () => {
    expect(routePath('settings', 'x')).toBe('/settings')
    // The count in the name is asserted, not decorative: a fourth id branch makes it wrong.
    expect(ALL_VIEWS.length).toBe(13)
    const ignoreTheId = ALL_VIEWS.filter((v) => routePath(v, 'x') === ROUTE_PATHS[v])
    expect(ignoreTheId.length, `views that ignore an id: ${ignoreTheId.join(', ')}`).toBe(10)
  })
})

describe('routePath / parseRoute round trip — id corpus', () => {
  // UUID, URN-prefixed UUID, space, slash, '#', plain string
  const ID_CORPUS = [
    'a1b2c3d4-e5f6-47a8-89ab-cdef01234567',
    'urn:uuid:a1b2c3d4-e5f6-47a8-89ab-cdef01234567',
    'has space',
    'a/b',
    'tag#1',
    'plain-string',
  ]
  const ID_TAKING_VIEWS = new Set(['detail', 'extraction', 'workflows'])

  it('roundTrip_idCorpusSurvivesForEveryViewAcrossAllIds', () => {
    expect(ALL_VIEWS.length).toBe(13)
    expect(ID_CORPUS.length).toBe(6)
    for (const v of ALL_VIEWS) {
      for (const id of ID_CORPUS) {
        const expected = { view: v, id: ID_TAKING_VIEWS.has(v) ? id : null }
        expect(parseRoute(routePath(v, id)), `${v} with id ${id}`).toEqual(expected)
      }
    }
  })

  it('roundTrip_aPolicyIdSurvivesSerialiseAndParse', () => {
    // The same corpus the two OLDER drill-downs use (there are three forms now), so the
    // slash and '#' entries tell an encoding bug from a plain pass-through.
    expect(ID_CORPUS.length).toBe(6)
    for (const id of ID_CORPUS) {
      expect(parseRoute(routePath('workflows', id)), `workflows with id ${id}`).toEqual({
        view: 'workflows',
        id,
      })
    }
  })
})

describe('the workflows drill-down — /workflows/:policyId (ROUTE-07-03)', () => {
  // The list and the drill-down share one view here, so segment count is the only
  // discriminator: /invoices/<id> parses to a different view than /invoices, this does not.
  it('parse_theListAndTheBuilderAreTwoAddresses', () => {
    expect(parseRoute('/workflows')).toEqual({ view: 'workflows', id: null })
    expect(parseRoute(`/workflows/${UUID}`)).toEqual({ view: 'workflows', id: UUID })
    expect(parseRoute('/workflows/')).toEqual({ view: 'workflows', id: null })
    expect(parseRoute('/workflows/a/b')).toBeNull()
  })

  // policyId is not on ParsedLocation until the codec lands; reading it off the runtime
  // entry list keeps this an assertion failure rather than a compile error.
  function policyIdOf(pathname: string): unknown {
    const found = Object.entries(parseLocation(pathname, '')).find(([k]) => k === 'policyId')
    return found === undefined ? undefined : found[1]
  }

  it('parseLocation_policyIdIsNonNullOnlyOnItsOwnView', () => {
    expect(policyIdOf(`/workflows/${UUID}`), 'the builder path must carry its id').toBe(UUID)
    const carryNothing = ['/workflows', `/invoices/${UUID}`, '/extraction/j1', '/settings/roles']
    expect(carryNothing.length).toBe(4)
    for (const pathname of carryNothing) {
      expect(policyIdOf(pathname), `${pathname} must carry no policyId`).toBeNull()
    }
  })

  // wireMirrors.test.ts's tsInterfaceKeys reads `export interface` only; ParsedLocation is a
  // type alias.
  function parsedLocationDeclaredKeys(): string[] {
    const body = /export type ParsedLocation = \{([^{}]*)\}/.exec(readFileSync(ROUTE_TS, 'utf8'))?.[1] ?? ''
    const keys: string[] = []
    for (const rawSeg of body.split(/[\n;]/)) {
      const seg = rawSeg.trim()
      if (!seg || seg.startsWith('//')) continue
      const m = /^([A-Za-z_][A-Za-z0-9_]*)\??\s*:/.exec(seg)
      if (m) keys.push(m[1])
    }
    return keys
  }

  it('parseLocation_returnsEightFieldsAndDeclaresEight', () => {
    const declared = parsedLocationDeclaredKeys()
    // Vacuity floor: a renamed type or a nested brace reads [] here, which would compare
    // nothing against nothing.
    expect(declared.length, `ParsedLocation declares: ${declared.join(', ')}`).toBe(8)
    expect([...declared].sort()).toEqual([
      'auditInvoice',
      'invoiceId',
      'jobId',
      'policyId',
      'q',
      'reviewBatchIds',
      'settingsTab',
      'view',
    ])
    const returned = Object.keys(parseLocation('/', ''))
    expect([...returned].sort(), 'a field is declared and left unreturned').toEqual([...declared].sort())
  })
})

describe('parseLocation — invoiceId / jobId field mapping', () => {
  it('parseLocation_isTotalAndFallsBackToTheDashboardTripleOnAnUnparseablePath', () => {
    expect(parseLocation('/nonsense', '')).toMatchObject({ view: 'dashboard', invoiceId: null, jobId: null })
  })

  // Gap found in mutation testing: nothing above exercises the actual field mapping --
  // forcing invoiceId to always be null still left every existing test green.
  it('parseLocation_extractsInvoiceIdFromADetailDrillDownAndLeavesJobIdNull', () => {
    expect(parseLocation('/invoices/abc', '')).toMatchObject({ view: 'detail', invoiceId: 'abc', jobId: null })
  })

  it('parseLocation_extractsJobIdFromAnExtractionDrillDownAndLeavesInvoiceIdNull', () => {
    expect(parseLocation('/extraction/j1', '')).toMatchObject({ view: 'extraction', invoiceId: null, jobId: 'j1' })
  })

  it('parseLocation_aPlainViewWithNoIdCarriesNeitherInvoiceIdNorJobId', () => {
    expect(parseLocation('/audit', '')).toMatchObject({ view: 'audit', invoiceId: null, jobId: null })
  })

  it('parseLocation_toleratesTheEmptyStringAndAControlCharacterWithoutThrowing', () => {
    expect(() => parseLocation('', '')).not.toThrow()
    expect(parseLocation('', '')).toMatchObject({ view: 'dashboard', invoiceId: null, jobId: null })
    expect(() => parseLocation('/invoices/ ', '')).not.toThrow()
  })
})

describe('parseRoute / routePath — adversarial ids', () => {
  it('parse_aBareSlashIdRoundTripsThroughEncodingRatherThanBeingMistakenForASegmentBoundary', () => {
    const path = routePath('detail', '/')
    expect(path).toBe('/invoices/%2F')
    expect(parseRoute(path)).toEqual({ view: 'detail', id: '/' })
  })

  it('parse_aWhitespaceOnlyIdSurvivesEncodeDecode', () => {
    const path = routePath('extraction', '   ')
    expect(parseRoute(path)).toEqual({ view: 'extraction', id: '   ' })
  })

  it('parse_aVeryLongIdSurvivesEncodeDecode', () => {
    const longId = 'x'.repeat(2000)
    const path = routePath('detail', longId)
    expect(parseRoute(path)).toEqual({ view: 'detail', id: longId })
  })

  it('parse_anAlreadyPercentEncodedIdDoesNotDoubleEncodeOrDoubleDecode', () => {
    // '%25' in the id is itself a percent-escape; a naive re-encode/decode pass would
    // collapse '%2F' -> '/' a second time and split the id into extra segments.
    const id = 'a%2Fb'
    const path = routePath('detail', id)
    expect(path).toBe('/invoices/a%252Fb')
    expect(parseRoute(path)).toEqual({ view: 'detail', id })
  })

})

describe('review path — /imports/:batchIds/review (ROUTE-03-01)', () => {
  // Six distinct canonical uuids: the first five are the cap, all six is one over it.
  const REVIEW_UUIDS = [
    'a1b2c3d4-e5f6-47a8-89ab-cdef01234567',
    'b2c3d4e5-f6a7-48b9-9abc-def012345678',
    'c3d4e5f6-a7b8-49ca-abcd-ef0123456789',
    'd4e5f6a7-b8c9-4adb-bcde-f01234567890',
    'e5f6a7b8-c9d0-4be1-cdef-012345678901',
    'f6a7b8c9-d0e1-4cf2-defa-123456789012',
  ]

  it('review_theSingleIdFormRoundTrips', () => {
    const [pathname, search] = splitUrl(routeUrl('create', { reviewBatchIds: [UUID] }))
    expect(parseLocation(pathname, search)).toMatchObject({ view: 'create', reviewBatchIds: [UUID] })
  })

  it('review_everyIdInARunRoundTripsNotJustTheFirst', () => {
    const ids = REVIEW_UUIDS.slice(0, 5) // the cap
    expect(ids.length).toBe(5)
    const [pathname, search] = splitUrl(routeUrl('create', { reviewBatchIds: ids }))
    expect(parseLocation(pathname, search).reviewBatchIds).toEqual(ids)
  })

  it('review_theEmittedSegmentCarriesARawComma', () => {
    const [a, b] = REVIEW_UUIDS
    const path = reviewPath([a, b])
    expect(path).toBe(`/imports/${a},${b}/review`)
    expect(path).not.toContain('%2C')
  })

  it('review_theOmittedBranchIsThePlainCreatePath', () => {
    // Drives both the empty-array and the fully-absent forms of the omitted branch.
    expect(routeUrl('create', { reviewBatchIds: [] })).toBe('/create')
    expect(routeUrl('create', {})).toBe('/create')
    expect(routeUrl('create')).toBe('/create')
    const parsed = parseLocation('/create', '')
    expect(parsed.view).toBe('create')
    expect(parsed.reviewBatchIds).toEqual([])
    // Control needle: a non-empty list does NOT take this branch.
    expect(routeUrl('create', { reviewBatchIds: [UUID] })).not.toBe('/create')
  })

  it('review_oneBadSegmentPoisonsTheWholeList', () => {
    const result = parseReviewPath(`/imports/${UUID},notauuid/review`)
    expect(result).toBeNull() // never a one-element array carrying just the good id
  })

  it('review_theCapIsARejectionNotATruncation', () => {
    expect(REVIEW_UUIDS.length).toBe(6) // one over the cap
    const result = parseReviewPath(`/imports/${REVIEW_UUIDS.join(',')}/review`)
    expect(result).toBeNull() // never a truncated five-element array
  })

  it('review_traversalAndSuffixesAreRejected', () => {
    const cases = [
      // Reachable only as a direct function call: a browser resolves '..' before the
      // address bar, pushState/replaceState, or the sessionStorage bootPath ever see it.
      // Never promote this case to a deep-link or e2e spec -- it would test nothing there.
      '/imports/../../etc/review',
      `/imports/${UUID}/extra/review`,
      '/imports//review',
      `/imports/${UUID}/Review`,
      // Non-discriminating: null whether or not '%2C' is decoded first. The raw-comma
      // rule is pinned by review_theEmittedSegmentCarriesARawComma and the round trips.
      '/imports/%2C/review',
    ]
    expect(cases.length).toBe(5)
    for (const pathname of cases) {
      expect(parseReviewPath(pathname), pathname).toBeNull()
    }
  })

  it('review_idCaseIsPreservedNotNormalised', () => {
    const result = parseReviewPath(`/imports/${UUID_UPPER}/review`)
    expect(result).toEqual([UUID_UPPER])
    expect(result?.[0]).not.toBe(UUID_UPPER.toLowerCase())
  })

  it('review_theGateIsThreeClauses', () => {
    expect(reviewNavIds('create', 'review', [UUID])).toEqual([UUID])
    expect(reviewNavIds('invoices', 'review', [UUID])).toEqual([])
    expect(reviewNavIds('create', 'form', [UUID])).toEqual([])
    expect(reviewNavIds('create', 'review', [])).toEqual([])
  })

  it('review_parseLocationStaysTotalAndParseRouteIsUnchanged', () => {
    const path = reviewPath([UUID])
    expect(parseRoute(path)).toBeNull() // 'imports' is not a drill-down segment
    // Discriminating: 'invoices' IS one, so the segment count is the only thing that can
    // reject this — widening parseRoute past two segments fails here.
    expect(parseRoute(`/invoices/${UUID}/review`)).toBeNull()
    const [pathname, search] = splitUrl(path)
    expect(parseLocation(pathname, search)).toMatchObject({ view: 'create', reviewBatchIds: [UUID] })
  })

  it('review_aTrailingSlashIsStrippedLikeEveryOtherRoute', () => {
    expect(parseReviewPath(`/imports/${UUID}/review/`)).toEqual([UUID])
    const parsed = parseLocation(`/imports/${UUID}/review/`, '')
    expect(parsed).toMatchObject({ view: 'create', reviewBatchIds: [UUID] })
  })

  it('review_capBoundaryBothSides', () => {
    expect(REVIEW_UUIDS.length).toBe(6)
    expect(parseReviewPath(`/imports/${REVIEW_UUIDS.slice(0, 5).join(',')}/review`)).toEqual(REVIEW_UUIDS.slice(0, 5))
    expect(parseReviewPath(`/imports/${REVIEW_UUIDS.join(',')}/review`)).toBeNull()
  })

  it('review_duplicateIdsAreAcceptedIndividually', () => {
    // No uniqueness rule in the spec: each segment is validated on its own.
    expect(parseReviewPath(`/imports/${UUID},${UUID}/review`)).toEqual([UUID, UUID])
  })

  it('review_aWhitespaceOrEncodedSegmentIsRejected', () => {
    expect(parseReviewPath(`/imports/ /review`)).toBeNull()
    expect(parseReviewPath(`/imports/%20${UUID}/review`)).toBeNull()
    expect(parseReviewPath(`/imports/${UUID} /review`)).toBeNull() // trailing space inside the segment
  })

  it('review_composedWithAnOwnedQueryStringIsUnreachableByConstruction', () => {
    // routeUrl's arms are sequential `if`s keyed on view: once 'create' matches and returns,
    // the 'invoices'-owned `q` branch below it can never run for the same call.
    const url = routeUrl('create', { reviewBatchIds: [UUID], q: 'anything' })
    expect(url).toBe(`/imports/${UUID}/review`)
    expect(url).not.toContain('?')
  })

  it('review_aNonCreateViewIgnoresReviewBatchIds', () => {
    expect(routeUrl('invoices', { reviewBatchIds: [UUID], q: 'x' })).toBe('/invoices?q=x')
    expect(parseLocation('/invoices', '?q=x').reviewBatchIds).toEqual([])
  })

  // ROUTE-03-02: the shipped drift guard (reviewBatch.test.ts's BULK-06-DRIFT), re-pointed
  // at the constant's real owner instead of removed.
  it('guard_theRunCapIsOneConstant', () => {
    expect(REVIEW_PATH_MAX_IDS).toBe(MAX_RUN_FILES)
  })
})

// ROUTE-03-02: the three codec `describe` blocks below moved here from reviewBatch.test.ts,
// retargeted from the retired hash-fragment form to the '/imports/…/review' path. Spec ids
// kept for traceability. Six of the eleven ported assertions duplicate specs already in the
// block above (traceability only, not new coverage) — see task-951's Implementation Notes.

describe('parseReviewHash / formatReviewHash (AC-4) — migrated to reviewPath/parseReviewPath (ROUTE-03-02, HASH-1/2)', () => {
  // formatReviewHash([id]) resolving to the old hash-fragment url (reviewBatch.test.ts:446)
  // is INVALIDATED, not ported: it pins the retired url shape ROUTE-00 Decision Log Q7
  // deliberately breaks. No path-form 'equivalent' invented — reviewPath's own byte shape is
  // already pinned by review_theEmittedSegmentCarriesARawComma above.
  it('HASH-1 (migrated): round-trips a uuid with case preserved verbatim (never lower-cased); an empty string and a foreign path are null', () => {
    expect(parseReviewPath(reviewPath([UUID_UPPER]))).toEqual([UUID_UPPER])
    // C1 (prefix check): defensive, unreachable in production — parseLocation gates the
    // '/imports/' prefix before ever calling parseReviewPath.
    expect(parseReviewPath('/somewhere-else')).toBeNull()
    expect(parseReviewPath('')).toBeNull()
  })

  it('HASH-2 (migrated): a malformed or non-uuid fragment is rejected — never a startsWith+slice that would accept a path-traversal-shaped tail', () => {
    expect(parseReviewPath('/imports/../../etc/review')).toBeNull()
    expect(parseReviewPath('/imports//review')).toBeNull()
    expect(parseReviewPath(`/imports/${UUID}/extra/review`)).toBeNull()
    expect(parseReviewPath(`/imports/${UUID}?x=1/review`)).toBeNull()
    // C1 (prefix check): defensive, unreachable in production — same reason as above.
    expect(parseReviewPath(`/IMPORTS/${UUID}/review`)).toBeNull()
  })
})

describe('reviewHash (AC-1, HASH-3) — migrated to reviewNavIds/reviewPath (ROUTE-03-02)', () => {
  // HASH-3: duplicate of review_theGateIsThreeClauses above (AC-1 traceability, not new
  // coverage) — the old reviewHash gate is now reviewNavIds.
  it('HASH-3 (migrated): the run is passed through ONLY on view=create + createStep=review with a non-empty id array, and cleared ([]) on every other combination — including view=invoices (the Finish / ← Invoices exit, where a lingering hash would bounce a reload straight back into review)', () => {
    expect(reviewNavIds('create', 'review', ['u-1'])).toEqual(['u-1'])
    expect(reviewNavIds('invoices', 'review', ['u-1'])).toEqual([])
    expect(reviewNavIds('create', 'form', ['u-1'])).toEqual([])
    expect(reviewNavIds('create', 'review', [])).toEqual([])
  })

  // HASH-3b: duplicate of review_theEmittedSegmentCarriesARawComma above (AC-1 traceability,
  // not new coverage) — the comma-join itself now belongs to reviewPath, not the gate.
  it('HASH-3b (migrated): a two-id run joins with a comma', () => {
    expect(reviewPath(['u-1', 'u-2'])).toBe('/imports/u-1,u-2/review')
  })
})

describe('parseReviewHash: widened to a run (BULK-01-06, AC-1) — migrated to parseReviewPath/reviewPath (ROUTE-03-02)', () => {
  // Six distinct canonical uuids, same corpus as REVIEW_UUIDS above.
  const RUN_IDS = [
    'a1b2c3d4-e5f6-47a8-89ab-cdef01234567',
    'b2c3d4e5-f6a7-48b9-9abc-def012345678',
    'c3d4e5f6-a7b8-49ca-abcd-ef0123456789',
    'd4e5f6a7-b8c9-4adb-bcde-f01234567890',
    'e5f6a7b8-c9d0-4be1-cdef-012345678901',
    'f6a7b8c9-d0e1-4cf2-defa-123456789012',
  ]

  // AC-3: the suite had no DIRECT parseReviewPath(single id) assertion before this — the
  // existing round trip goes through routeUrl+parseLocation, and the cap-boundary spec only
  // drives 5-vs-6. Real coverage, not a duplicate.
  it('BULK-06-1 (migrated — the direct single-id case the suite lacked, AC-3): a one-element run parses to a one-element array', () => {
    expect(parseReviewPath(`/imports/${RUN_IDS[0]}/review`)).toEqual([RUN_IDS[0]])
  })

  // Duplicate of review_everyIdInARunRoundTripsNotJustTheFirst above (AC-1 traceability only).
  it('BULK-06-2 (migrated): several ids parse IN ORDER', () => {
    const [a, b, c] = RUN_IDS
    expect(parseReviewPath(`/imports/${a},${b},${c}/review`)).toEqual([a, b, c])
  })

  // Duplicate of review_oneBadSegmentPoisonsTheWholeList above (AC-1 traceability only).
  it('BULK-06-3 (migrated): one bad segment poisons the WHOLE path, never a partial array', () => {
    expect(parseReviewPath(`/imports/${RUN_IDS[0]},notauuid/review`)).toBeNull()
  })

  // Duplicate of review_traversalAndSuffixesAreRejected above (AC-1 traceability only).
  it('BULK-06-4 (migrated): traversal and suffixes stay refused', () => {
    expect(parseReviewPath('/imports/../../etc/review')).toBeNull()
    expect(parseReviewPath(`/imports/${RUN_IDS[0]}/extra/review`)).toBeNull()
    expect(parseReviewPath('/imports//review')).toBeNull()
  })

  // Duplicate of review_theCapIsARejectionNotATruncation above (AC-1 traceability only).
  it('BULK-06-5 (migrated): the run is bounded at REVIEW_PATH_MAX_IDS — six ids is null, never a truncated five', () => {
    expect(parseReviewPath(`/imports/${RUN_IDS.join(',')}/review`)).toBeNull()
  })

  // formatReviewHash([a]) resolving to the old hash-fragment url (reviewBatch.test.ts:2527)
  // is INVALIDATED, not ported: retired url byte-shape, broken by ROUTE-00 Decision Log Q7.
  it('BULK-06-6 (migrated): parseReviewPath(reviewPath([a,b])) round-trips two ids', () => {
    const [a, b] = RUN_IDS
    expect(parseReviewPath(reviewPath([a, b]))).toEqual([a, b])
  })
})

// ROUTE-03-05 AC-1/AC-2: the whole-tree grep this AC runs by hand can't see route.test.ts
// itself (a literal NUL byte a few hundred lines below makes plain grep classify the file as
// binary and skip it) -- this in-process scan is what actually covers the six source files.
// Built concatenated, not as a literal: a literal would make this scanner's own source
// match itself, so the shell AC-1 grep could never return a true zero.
const REVIEW_FRAGMENT = '#' + 'review'
const LOCATION_HASH = 'location' + '.hash'

describe('ROUTE-03-05 AC-1: no retired review-hash fragment survives in the app', () => {
  it('guard_noReviewHashSurvivesInTheApp', () => {
    const files = [
      { name: 'App.tsx', path: APP_TSX },
      { name: 'lib/reviewBatch.ts', path: fileURLToPath(new URL('./reviewBatch.ts', import.meta.url)) },
      { name: 'lib/route.ts', path: ROUTE_TS },
      { name: 'types.ts', path: fileURLToPath(new URL('../types.ts', import.meta.url)) },
      { name: 'components/ReviewBatch.tsx', path: fileURLToPath(new URL('../components/ReviewBatch.tsx', import.meta.url)) },
      { name: 'lib/importApi.ts', path: fileURLToPath(new URL('./importApi.ts', import.meta.url)) },
    ]
    for (const { name, path } of files) {
      const src = readFileSync(path, 'utf8')
      // Floor: a broken path reads back '', which would make the absence checks below pass
      // on nothing read rather than a clean file -- M4-04 burned five instruments this way.
      expect(src.length, `${name} read back empty -- the path is broken`).toBeGreaterThan(0)
      expect(src.includes(REVIEW_FRAGMENT), `${name} still mentions the retired review-hash fragment`).toBe(false)
      expect(src.includes(LOCATION_HASH), `${name} still reads or writes the url fragment`).toBe(false)
    }
  })
})

// ROUTE-03-07 AC-3/AC-4: the two guards below are a PAIR by design. An absence guard alone
// would pass on a doc that deleted the review section instead of updating it -- the positive
// guard is what rules that out.
const PATHNAME_SEARCH_HASH = 'pathname + search' + ' + hash'

describe('ROUTE-03-07 AC-3: the routing doc names no retired scheme', () => {
  it('guard_theRoutingDocNamesNoRetiredScheme', () => {
    const src = readFileSync(ROUTING_DOC, 'utf8')
    // Floor: a broken path reads back '', which would make the absence checks below pass on
    // nothing read rather than a clean doc -- M4-04 burned five instruments this way.
    expect(src.length, 'docs/routing.md read back empty -- the path is broken').toBeGreaterThan(0)
    // Needle: proves .includes() can see a match on this file at all, so the absence checks
    // below aren't vacuous.
    expect(src.includes('routeUrl'), 'control needle: the doc must still discuss routeUrl, or this scan proves nothing').toBe(true)
    expect(src.includes(REVIEW_FRAGMENT), 'docs/routing.md still mentions the retired review-hash fragment').toBe(false)
    expect(src.includes(LOCATION_HASH), 'docs/routing.md still reads or writes the url fragment').toBe(false)
    expect(src.includes(PATHNAME_SEARCH_HASH), 'docs/routing.md still describes the retired pathname+search+hash rebuild').toBe(false)
  })
})

describe('ROUTE-03-07 AC-4: the routing doc names the shipped form', () => {
  it('guard_theRoutingDocNamesTheShippedForm', () => {
    const src = readFileSync(ROUTING_DOC, 'utf8')
    expect(src.length, 'docs/routing.md read back empty -- the path is broken').toBeGreaterThan(0)
    expect(src.includes('/imports/'), 'docs/routing.md no longer names the shipped review path').toBe(true)
    expect(src.includes(':batchIds'), 'docs/routing.md no longer names the shipped batchIds segment').toBe(true)
  })
})
