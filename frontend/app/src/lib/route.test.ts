import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { clampFilterText } from './invoices'
import { ROUTE_PATHS, routePath, parseRoute, parseLocation, routeUrl, reviewPath, parseReviewPath, reviewNavIds } from './route'

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
    expect(routeUrl('settings', { settingsTab: undefined })).toBe('/settings')
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

  it('serialize_idIsIgnoredForTheElevenViewsThatDoNotTakeOne', () => {
    expect(routePath('settings', 'x')).toBe('/settings')
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
  const ID_TAKING_VIEWS = new Set(['detail', 'extraction'])

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
    expect(parseRoute(path)).toBeNull() // three segments: parseRoute's strictness is unchanged
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
})
