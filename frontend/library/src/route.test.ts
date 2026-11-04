import { describe, expect, it } from 'vitest'
import { FEATURES, GROUPS, PAGE_PATHS } from './content.ts'
import { LIBRARY_BASE } from './analytics.ts'
import { libraryPath, parseLibraryPath } from './route.ts'

describe('route', () => {
  it('parse_rootIsHome', () => {
    expect(parseLibraryPath('/')).toEqual({ view: 'home' })
  })

  it('parse_stripsTheBase', () => {
    for (const p of [LIBRARY_BASE, `${LIBRARY_BASE}/`]) expect(parseLibraryPath(p), p).toEqual({ view: 'home' })
    let groups = 0
    for (const g of GROUPS) {
      for (const p of [`${LIBRARY_BASE}/${g.id}`, `${LIBRARY_BASE}/${g.id}/`]) {
        const r = parseLibraryPath(p)
        expect(r.view).toBe('group')
        if (r.view === 'group') {
          expect(r.group).toBe(g)
          groups++
        }
      }
    }
    expect(GROUPS).toHaveLength(11)
    expect(groups).toBe(22)
    let feats = 0
    for (const f of FEATURES) {
      const r = parseLibraryPath(`${LIBRARY_BASE}/${f.gid}/${f.id}`)
      expect(r.view).toBe('feature')
      if (r.view === 'feature') {
        expect(r.feature).toBe(f)
        expect(r.group.id).toBe(f.gid)
        feats++
      }
    }
    expect(feats).toBe(30)
  })

  it('parse_aPathOutsideTheBaseIsBaseRelative', () => {
    const f = FEATURES[0]
    const r = parseLibraryPath(`/${f.gid}/${f.id}`)
    expect(r.view).toBe('feature')
    if (r.view === 'feature') expect(r.feature).toBe(f)
  })

  it('parse_aPrefixedPathReadsTheSameAsItsBaseRelativeOne', () => {
    expect(FEATURES.length).toBeGreaterThan(0)
    for (const f of FEATURES) expect(parseLibraryPath(`${LIBRARY_BASE}/${f.gid}/${f.id}`)).toEqual(parseLibraryPath(`/${f.gid}/${f.id}`))
  })

  it('parse_anUnknownPathIsNotFound', () => {
    for (const p of ['/nope', '/Invoices', '/invoices/nope', '/invoices/learns', '/invoices/import-files/x', '/invoices?x=1',
      '/libraryx', '/library/nope', '/Library/invoices', '/LIBRARY/rules', '/librarys/rules', '/library/library/rules']) {
      expect(parseLibraryPath(p), p).toEqual({ view: 'not-found', path: p })
    }
  })

  it('parse_percentEncodedLetterIsTheSamePage', () => {
    const r = parseLibraryPath(`${LIBRARY_BASE}/%69nvoices`)
    expect(r.view).toBe('group')
    expect(r.view === 'group' && r.group.id).toBe('invoices')
  })

  it('parse_encodedBaseIsTheSamePage', () => {
    for (const p of ['/library%2Finvoices', '/%6Cibrary/invoices']) {
      const r = parseLibraryPath(p)
      expect(r.view === 'group' && r.group.id, p).toBe('invoices')
    }
  })

  it('parse_repeatedTrailingSlashIsTheGroup', () => {
    const r = parseLibraryPath(`${LIBRARY_BASE}/invoices//`)
    expect(r.view === 'group' && r.group.id).toBe('invoices')
  })

  it('parse_malformedEscapeIsNotFoundAndDoesNotThrow', () => {
    const p = `${LIBRARY_BASE}/%E0%A4%A`
    expect(() => parseLibraryPath(p)).not.toThrow()
    expect(parseLibraryPath(p)).toEqual({ view: 'not-found', path: p })
  })

  it('parse_homeStaysHome', () => {
    for (const p of ['', '/', '/library', '/library/']) expect(parseLibraryPath(p), p).toEqual({ view: 'home' })
  })

  it('parse_repeatedSlashesCollapse', () => {
    for (const [p, gid] of [['/library//invoices', 'invoices'], ['/library//rules', 'rules'], ['//invoices', 'invoices'],
      ['/invoices//', 'invoices'], ['/library/invoices//', 'invoices']]) {
      const r = parseLibraryPath(p)
      expect(r.view, p).toBe('group')
      if (r.view === 'group') expect(r.group.id).toBe(gid)
    }
  })

  it('path_notFoundKeepsItsPath', () => {
    expect(libraryPath({ view: 'not-found', path: '/library/nope' })).toBe('/library/nope')
  })

  it('content_pagePathsMatchEveryView', () => {
    expect(GROUPS.length).toBeGreaterThan(0)
    expect(FEATURES.length).toBeGreaterThan(0)
    const want = [...GROUPS.map((g) => libraryPath({ view: 'group', group: g })), ...FEATURES.map((f) => `${LIBRARY_BASE}/${f.gid}/${f.id}`)]
      .map((p) => p.slice(LIBRARY_BASE.length))
    expect([...PAGE_PATHS].sort()).toEqual(want.sort())
    expect(new Set(PAGE_PATHS).size).toBe(PAGE_PATHS.length)
  })

  it('path_everyViewIsUnderTheBase', () => {
    const paths = [`${LIBRARY_BASE}/`, ...GROUPS.map((g) => `${LIBRARY_BASE}/${g.id}`), ...FEATURES.map((f) => `${LIBRARY_BASE}/${f.gid}/${f.id}`)]
    expect(paths).toHaveLength(42)
    expect(new Set(paths).size).toBe(42)
    for (const p of paths) {
      expect(p.startsWith('/library/')).toBe(true)
      expect(p).not.toContain('/library/library')
      expect(libraryPath(parseLibraryPath(p))).toBe(p)
    }
  })
})
