import { describe, expect, it } from 'vitest'
import { FEATURES, GROUPS } from './content.ts'
import { libraryPath, parseLibraryPath } from './route.ts'

describe('route', () => {
  it('parse_rootIsHome', () => {
    expect(parseLibraryPath('/')).toEqual({ view: 'home' })
  })

  it('parse_everyGroupIdOpensItsGroup', () => {
    let hits = 0
    for (const g of GROUPS) {
      for (const p of [`/${g.id}`, `/${g.id}/`]) {
        const r = parseLibraryPath(p)
        expect(r.view).toBe('group')
        if (r.view === 'group') {
          expect(r.group).toBe(g)
          hits++
        }
      }
    }
    expect(GROUPS).toHaveLength(11)
    expect(hits).toBe(22)
  })

  it('parse_everyFeatureUnderItsGroupOpensTheFeature', () => {
    let hits = 0
    for (const f of FEATURES) {
      const r = parseLibraryPath(`/${f.gid}/${f.id}`)
      expect(r.view).toBe('feature')
      if (r.view === 'feature') {
        expect(r.feature).toBe(f)
        expect(r.group.id).toBe(f.gid)
        hits++
      }
    }
    expect(hits).toBe(26)
  })

  it('parse_anUnknownPathIsHome', () => {
    for (const p of ['', '/nope', '/Invoices', '/invoices/nope', '/invoices/learns', '/invoices/import-files/x',
      '//invoices', '/invoices//', '/invoices?x=1']) {
      expect(parseLibraryPath(p), p).toEqual({ view: 'home' })
    }
  })

  it('path_roundTripsEveryView', () => {
    const paths = ['/', ...GROUPS.map((g) => `/${g.id}`), ...FEATURES.map((f) => `/${f.gid}/${f.id}`)]
    expect(paths).toHaveLength(38)
    expect(new Set(paths).size).toBe(38)
    for (const p of paths) expect(libraryPath(parseLibraryPath(p))).toBe(p)
  })
})
