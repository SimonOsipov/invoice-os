import { describe, expect, it } from 'vitest'
import { FEATURES, GROUPS } from './content.ts'
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
    expect(feats).toBe(28)
  })

  it('parse_aPathOutsideTheBaseIsBaseRelative', () => {
    const f = FEATURES[0]
    const r = parseLibraryPath(`/${f.gid}/${f.id}`)
    expect(r.view).toBe('feature')
    if (r.view === 'feature') expect(r.feature).toBe(f)
  })

  it('parse_aBaseLookalikeIsHome', () => {
    for (const p of ['/libraryx', '/library/nope', '/Library/invoices', '/LIBRARY/rules', '/librarys/rules', '/library//rules', '/library/library/rules']) {
      expect(parseLibraryPath(p), p).toEqual({ view: 'home' })
    }
  })

  it('parse_aPrefixedPathReadsTheSameAsItsBaseRelativeOne', () => {
    expect(FEATURES.length).toBeGreaterThan(0)
    for (const f of FEATURES) expect(parseLibraryPath(`${LIBRARY_BASE}/${f.gid}/${f.id}`)).toEqual(parseLibraryPath(`/${f.gid}/${f.id}`))
  })

  it('parse_anUnknownPathIsHome', () => {
    for (const p of ['', '/nope', '/Invoices', '/invoices/nope', '/invoices/learns', '/invoices/import-files/x',
      '//invoices', '/invoices//', '/invoices?x=1']) {
      expect(parseLibraryPath(p), p).toEqual({ view: 'home' })
    }
  })

  it('path_everyViewIsUnderTheBase', () => {
    const paths = [`${LIBRARY_BASE}/`, ...GROUPS.map((g) => `${LIBRARY_BASE}/${g.id}`), ...FEATURES.map((f) => `${LIBRARY_BASE}/${f.gid}/${f.id}`)]
    expect(paths).toHaveLength(40)
    expect(new Set(paths).size).toBe(40)
    for (const p of paths) {
      expect(p.startsWith('/library/')).toBe(true)
      expect(p).not.toContain('/library/library')
      expect(libraryPath(parseLibraryPath(p))).toBe(p)
    }
  })
})
