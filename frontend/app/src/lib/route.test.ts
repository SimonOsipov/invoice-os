import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { ROUTE_PATHS, routePath, parseRoute, seedFromPath } from './route'

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

describe('seedFromPath', () => {
  it('seed_isTotalAndFallsBackToTheDashboardTripleOnAnUnparseablePath', () => {
    expect(seedFromPath('/nonsense')).toEqual({ view: 'dashboard', invoiceId: null, jobId: null })
  })
})
