import { describe, expect, it } from 'vitest'
import { blogPath, parseBlogPath, type Route } from './route'

describe('route', () => {
  it('parse_rootIsHome', () => expect(parseBlogPath('/')).toEqual({ view: 'home' }))
  it('parse_subscribeIsSubscribe', () => expect(parseBlogPath('/subscribe')).toEqual({ view: 'subscribe' }))
  it('parse_categoryIdOpensTheCategory', () =>
    expect(parseBlogPath('/category/compliance')).toEqual({ view: 'category', id: 'compliance' }))
  it('parse_oneSegmentIsAPost', () =>
    expect(parseBlogPath('/einvoicing-2026')).toEqual({ view: 'post', slug: 'einvoicing-2026' }))

  it('parse_aTrailingSlashIsAccepted', () => {
    expect(parseBlogPath('/subscribe/')).toEqual(parseBlogPath('/subscribe'))
    expect(parseBlogPath('/category/x/')).toEqual({ view: 'category', id: 'x' })
    expect(parseBlogPath('/a-post/')).toEqual({ view: 'post', slug: 'a-post' })
  })

  it('parse_otherShapesAreMissing', () => {
    for (const p of ['/category', '/category/', '/category/a/b', '/a/b', '/subscribe/x', '/what-changes/extra']) {
      expect(parseBlogPath(p), p).toEqual({ view: 'missing', path: p })
    }
  })

  it('parse_emptyPathIsHome', () => expect(parseBlogPath('')).toEqual({ view: 'home' }))

  it('path_aMissingRouteKeepsItsPath', () => expect(blogPath(parseBlogPath('/a/b'))).toBe('/a/b'))

  it('path_roundTripsEveryRoute', () => {
    const routes: Route[] = [
      { view: 'home' },
      { view: 'subscribe' },
      { view: 'category', id: 'compliance' },
      { view: 'post', slug: 'einvoicing-2026' },
    ]
    for (const r of routes) expect(parseBlogPath(blogPath(r)), r.view).toEqual(r)
    expect(blogPath({ view: 'home' })).toBe('/')
  })
})
