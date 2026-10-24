import { describe, expect, it } from 'vitest'
import { blogPath, parseBlogPath, type Route } from './route'

describe('route', () => {
  it('parse_rootIsHome', () => expect(parseBlogPath('/')).toEqual({ view: 'home' }))
  it('parse_subscribeIsSubscribe', () => expect(parseBlogPath('/subscribe')).toEqual({ view: 'subscribe' }))
  it('parse_subscribeConfirmIsConfirm', () => {
    expect(parseBlogPath('/subscribe/confirm')).toEqual({ view: 'confirm' })
    expect(parseBlogPath('/subscribe/confirm/')).toEqual({ view: 'confirm' })
  })
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

  it('path_aMissingRouteKeepsItsPath', () => expect(blogPath(parseBlogPath('/blog/a/b'))).toBe('/blog/a/b'))

  it('parse_stripsTheBase', () => {
    for (const p of ['/blog', '/blog/']) expect(parseBlogPath(p), p).toEqual({ view: 'home' })
    expect(parseBlogPath('/blog/einvoicing-2026')).toEqual({ view: 'post', slug: 'einvoicing-2026' })
    expect(parseBlogPath('/blog/category/compliance')).toEqual({ view: 'category', id: 'compliance' })
    expect(parseBlogPath('/blog/subscribe')).toEqual({ view: 'subscribe' })
    expect(parseBlogPath('/blog/subscribe/confirm')).toEqual({ view: 'confirm' })
    expect(parseBlogPath('/blog/a/b')).toEqual({ view: 'missing', path: '/a/b' })
  })

  it('parse_aPathOutsideTheBaseIsBaseRelative', () => {
    expect(parseBlogPath('/p')).toEqual({ view: 'post', slug: 'p' })
    expect(parseBlogPath('/category/c')).toEqual({ view: 'category', id: 'c' })
  })

  it('parse_aBaseLookalikeIsNotStripped', () => {
    expect(parseBlogPath('/blogger')).toEqual({ view: 'post', slug: 'blogger' })
    expect(parseBlogPath('/blog/blog')).toEqual({ view: 'post', slug: 'blog' })
    expect(parseBlogPath('/blogs/x')).toEqual({ view: 'missing', path: '/blogs/x' })
  })

  it('path_everyRouteIsUnderTheBase', () => {
    const routes: Route[] = [
      { view: 'home' },
      { view: 'subscribe' },
      { view: 'confirm' },
      { view: 'category', id: 'compliance' },
      { view: 'post', slug: 'einvoicing-2026' },
      { view: 'missing', path: '/a/b' },
    ]
    for (const r of routes) {
      expect(blogPath(r).startsWith('/blog/'), r.view).toBe(true)
      expect(parseBlogPath(blogPath(r)), r.view).toEqual(r)
    }
    expect(blogPath({ view: 'home' })).toBe('/blog/')
  })

  it('parse_baseEdgeInputsAreOneRouteAndRoundTrip', () => {
    expect(parseBlogPath('/blog//x')).toEqual({ view: 'missing', path: '//x' })
    expect(parseBlogPath('/BLOG/x')).toEqual({ view: 'missing', path: '/BLOG/x' })
    expect(parseBlogPath('/blog/blog/x')).toEqual({ view: 'missing', path: '/blog/x' })
    expect(parseBlogPath('/blog/subscribe/')).toEqual({ view: 'subscribe' })
    expect(parseBlogPath('/blog/category/c/')).toEqual({ view: 'category', id: 'c' })
    expect(parseBlogPath('/blog/category')).toEqual({ view: 'missing', path: '/category' })
    for (const p of ['/blog/blog/x', '/blog//x', '/blog/a/b', '/a/b', '/blog/category']) {
      const once = blogPath(parseBlogPath(p))
      expect(once, p).not.toContain('/blog/blog/blog')
      expect(parseBlogPath(once), p).toEqual(parseBlogPath(p))
      expect(blogPath(parseBlogPath(once)), p).toBe(once)
    }
    expect(blogPath(parseBlogPath('/a/b'))).toBe('/blog/a/b')
  })
})
