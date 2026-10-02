// RED specs, RESKIN-01-04: the Lucide glyph set, the four path helpers and the v2 mark.
// Missing exports are read off the module namespace so they fail as assertions, not as tsc errors.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import * as iconsModule from './icons'
import { BrandMark, Icon } from './icons'
import { LANDING_SRC } from './cssScan.test.util'

type Glyphs = Record<string, readonly string[]>
type Helpers = {
  GLYPHS?: Glyphs
  circlePath?: (cx: number, cy: number, r: number) => string
  rectPath?: (x: number, y: number, w: number, h: number, rx?: number) => string
  linePath?: (x1: number, y1: number, x2: number, y2: number) => string
  polylinePath?: (...points: [number, number][]) => string
}
const mod = iconsModule as unknown as Helpers

function glyphs(): Glyphs {
  expect(mod.GLYPHS, 'icons.tsx must export GLYPHS').toBeDefined()
  return mod.GLYPHS as Glyphs
}
function helper<K extends Exclude<keyof Helpers, 'GLYPHS'>>(name: K): NonNullable<Helpers[K]> {
  expect(typeof mod[name], `icons.tsx must export ${name} as a function`).toBe('function')
  return mod[name] as NonNullable<Helpers[K]>
}

// The 33 epic names (P-22) plus play (D-27).
const EPIC_NAMES = [
  'activity', 'archive', 'building-2', 'chart-column', 'chart-line', 'check', 'check-check',
  'chevron-down', 'chevron-left', 'chevron-right', 'chevron-up', 'circle-check', 'contact',
  'file-check', 'file-search', 'file-text', 'globe', 'key-round', 'layout-dashboard', 'link',
  'loader-circle', 'menu', 'plug', 'send', 'settings', 'shield-check', 'sparkles', 'triangle-alert',
  'user-check', 'users', 'users-round', 'workflow', 'x',
]
const EXPECTED_NAMES = [...EPIC_NAMES, 'play'].sort()

describe('GLYPHS', () => {
  it('IC-01 GLYPHS holds exactly the epic glyph names plus play', () => {
    expect(EPIC_NAMES).toHaveLength(33)
    expect(Object.keys(glyphs()).sort()).toEqual(EXPECTED_NAMES)
  })

  it('IC-02 every glyph is non-empty path data', () => {
    const entries = Object.entries(glyphs())
    expect(entries.length).toBe(34)
    for (const [name, paths] of entries) {
      expect(paths.length, `${name} has no path`).toBeGreaterThanOrEqual(1)
      for (const d of paths) expect(d, `${name}: ${d}`).toMatch(/^[Mm][0-9MmLlHhVvCcSsQqTtAaZz .,-]*$/)
    }
    // planted control: the pattern rejects an empty string and a tag
    expect('').not.toMatch(/^[Mm][0-9MmLlHhVvCcSsQqTtAaZz .,-]*$/)
    expect('<path d="M0 0"/>').not.toMatch(/^[Mm][0-9MmLlHhVvCcSsQqTtAaZz .,-]*$/)
  })

  it('IC-03 Icon renders one path per glyph entry and no other shape', () => {
    const contact = glyphs().contact
    expect(contact, 'GLYPHS.contact').toBeDefined()
    expect(contact.length).toBe(5)
    const html = renderToStaticMarkup(createElement(Icon, { paths: [...contact] }))
    expect(html.match(/<path\b/g)?.length).toBe(contact.length)
    for (const tag of ['<circle', '<rect', '<line', '<polyline']) expect(html).not.toContain(tag)
  })

  it('IC-10 a polygon is a closed polyline', () => {
    expect(glyphs().play).toEqual(['M6 3L20 12L6 21L6 3Z'])
    // the polyline glyph stays open
    expect(glyphs()['user-check'].at(-1)).toBe('M16 11L18 13L22 9')
  })
})

describe('path helpers', () => {
  it('IC-04 circlePath draws a full circle from two arcs', () => {
    const circlePath = helper('circlePath')
    expect(circlePath(12, 12, 10)).toBe('M2 12a10 10 0 1 0 20 0a10 10 0 1 0 -20 0')
    // key-round's dot: fractional radius, off-centre
    expect(circlePath(16.5, 7.5, 0.5)).toBe('M16 7.5a0.5 0.5 0 1 0 1 0a0.5 0.5 0 1 0 -1 0')
  })

  it('IC-05 rectPath with a corner radius traces a closed rounded rect', () => {
    const rectPath = helper('rectPath')
    expect(rectPath(2, 3, 20, 5, 1)).toBe('M3 3h18a1 1 0 0 1 1 1v3a1 1 0 0 1 -1 1h-18a1 1 0 0 1 -1 -1v-3a1 1 0 0 1 1 -1Z')
    // non-square, different radius: w, h and rx are not interchangeable
    expect(rectPath(3, 4, 18, 18, 2)).toBe('M5 4h14a2 2 0 0 1 2 2v14a2 2 0 0 1 -2 2h-14a2 2 0 0 1 -2 -2v-14a2 2 0 0 1 2 -2Z')
  })

  it('IC-06 rectPath without a radius draws square corners', () => {
    const rectPath = helper('rectPath')
    expect(rectPath(3, 3, 7, 9)).toBe('M3 3h7v9h-7Z')
    expect(rectPath(3, 3, 7, 9, 0)).toBe('M3 3h7v9h-7Z')
  })

  it('IC-07 linePath and polylinePath', () => {
    expect(helper('linePath')(8, 6, 21, 6)).toBe('M8 6L21 6')
    expect(helper('polylinePath')([16, 11], [18, 13], [22, 9])).toBe('M16 11L18 13L22 9')
  })
})

describe('landing dependencies', () => {
  const LUCIDE = /lucide/i
  const lucideKeys = (pkg: Record<string, unknown>) =>
    ['dependencies', 'devDependencies', 'peerDependencies', 'optionalDependencies'].flatMap((field) =>
      Object.keys((pkg[field] as Record<string, string> | undefined) ?? {}).filter((k) => LUCIDE.test(k)),
    )

  it('IC-08 the landing declares no lucide dependency', () => {
    const pkg = JSON.parse(readFileSync(join(LANDING_SRC, '..', 'package.json'), 'utf8'))
    expect(Object.keys(pkg.dependencies)).toContain('react')
    expect(lucideKeys(pkg)).toEqual([])
    // planted control: the filter does catch a lucide package
    expect(lucideKeys({ dependencies: { 'lucide-react': '1' }, devDependencies: { lucide: '1' } })).toEqual(['lucide-react', 'lucide'])
  })
})

describe('BrandMark', () => {
  const attr = (tag: string, name: string) => tag.match(new RegExp(`\\s${name}="([^"]*)"`))?.[1]

  it('IC-09 BrandMark renders the v2 DS mark as rounded decoration', () => {
    const html = renderToStaticMarkup(createElement(BrandMark, { size: 22 }))
    const img = html.match(/<img\b[^>]*>/)?.[0]
    expect(img, `no <img> in ${html}`).toBeDefined()
    expect.soft(attr(img as string, 'src')).toMatch(/v2\/assets\/mark\.png(\?.*)?$/)
    expect.soft(attr(img as string, 'alt')).toBe('')
    expect.soft(attr(img as string, 'aria-hidden')).toBe('true')
    expect.soft(attr(img as string, 'width')).toBe('22')
    expect.soft(attr(img as string, 'height')).toBe('22')
    expect.soft(attr(img as string, 'style')).toContain('border-radius:var(--radius-md)')
  })
})
