// Rendered contract of the static DS primitives (SSR, node). Modules load through import.meta.glob
// so a missing file or export fails the test that names it, not the whole run.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { createElement, type ComponentType, type ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { LANDING_SRC, classSelectors, stripSource } from '../../cssScan.test.util'
import { GLYPHS } from '../../icons'

type Props = Record<string, unknown>
type Tag = { name: string; attrs: Record<string, string>; raw: string }

const modules = import.meta.glob('./*.tsx')

async function load(file: string, name: string): Promise<ComponentType<Props>> {
  const loader = modules[`./${file}.tsx`]
  expect(loader, `components/ds/${file}.tsx must exist`).toBeDefined()
  const mod = (await loader()) as Record<string, unknown>
  expect(typeof mod[name], `${file}.tsx must export ${name}`).toBe('function')
  return mod[name] as ComponentType<Props>
}

async function render(file: string, name: string, props: Props = {}, children?: ReactNode): Promise<string> {
  const Comp = await load(file, name)
  return renderToStaticMarkup(createElement(Comp, props, children))
}

const TAG_RE = /<([a-zA-Z][\w-]*)((?:\s+[^\s=>/]+(?:="[^"]*")?)*)\s*\/?>/g

function tagsOf(html: string): Tag[] {
  return [...html.matchAll(TAG_RE)].map((m) => ({
    name: m[1],
    raw: m[0],
    attrs: Object.fromEntries([...m[2].matchAll(/\s+([^\s=>/]+)(?:="([^"]*)")?/g)].map((a) => [a[1], a[2] ?? ''])),
  }))
}

function rootTag(html: string): Tag {
  const tags = tagsOf(html)
  expect(tags.length, `no element in ${html}`).toBeGreaterThan(0)
  expect(html.startsWith(tags[0].raw), `markup must open with its root element: ${html}`).toBe(true)
  return tags[0]
}

// The element that directly follows the root's opening tag.
function firstChild(html: string): Tag | undefined {
  const [root, next] = tagsOf(html)
  return next && html.startsWith(root.raw + next.raw) ? next : undefined
}

const classesOf = (t: Tag) => (t.attrs.class ?? '').split(/\s+/).filter(Boolean)

function svgOf(html: string) {
  const svg = tagsOf(html).find((t) => t.name === 'svg')
  expect(svg, `no <svg> in ${html}`).toBeDefined()
  return { attrs: (svg as Tag).attrs, paths: [...html.matchAll(/<path d="([^"]*)"/g)].map((m) => m[1]) }
}

function leaf(html: string, cls: string) {
  const tag = tagsOf(html).find((t) => classesOf(t).includes(cls))
  expect(tag, `no .${cls} in ${html}`).toBeDefined()
  const at = html.indexOf((tag as Tag).raw) + (tag as Tag).raw.length
  return { attrs: (tag as Tag).attrs, text: html.slice(at, html.indexOf('<', at)) }
}

const para = (text: string) => createElement('p', null, text)

describe('Section', () => {
  it('DS-01 Section renders a section with the v2 band class per tone', async () => {
    const tones = ['cream', 'dark', 'dark2', 'sage', 'peach']
    expect(tones).toHaveLength(5)
    for (const tone of tones) {
      const html = await render('Section', 'Section', { tone, id: 'problem' }, para('child'))
      const root = rootTag(html)
      expect(root.name, tone).toBe('section')
      expect(root.attrs, tone).toEqual({ id: 'problem', class: `ds-section band-${tone}` })
      expect(html, tone).toMatch(/^<section[^>]*><div class="container"><p>child<\/p><\/div><\/section>$/)
    }
  })

  it('DS-02 Section without id or paddingBlock renders neither attribute', async () => {
    const bare = rootTag(await render('Section', 'Section', {}, para('x')))
    expect(bare.name).toBe('section')
    expect(bare.attrs).toEqual({ class: 'ds-section band-cream' })
    const padded = rootTag(await render('Section', 'Section', { paddingBlock: '30px' }, para('x')))
    expect(padded.attrs.style).toBe('padding-block:30px')
    expect(padded.attrs.id).toBeUndefined()
    const both = rootTag(await render('Section', 'Section', { tone: 'dark2', id: 'pricing', paddingBlock: 'var(--section-y)' }, para('x')))
    expect(both.attrs).toEqual({ id: 'pricing', class: 'ds-section band-dark2', style: 'padding-block:var(--section-y)' })
  })
})

describe('Eyebrow', () => {
  it('DS-03 Eyebrow reuses .t-eyebrow and marks the dark tone', async () => {
    const light = await render('Eyebrow', 'Eyebrow', { tone: 'light' }, 'Problem')
    const dark = await render('Eyebrow', 'Eyebrow', { tone: 'dark' }, 'Problem')
    expect(rootTag(light).name).toBe('span')
    expect(classesOf(rootTag(light))).toEqual(['t-eyebrow'])
    expect(rootTag(dark).name).toBe('span')
    expect(classesOf(rootTag(dark))).toEqual(['t-eyebrow', 'ds-eyebrow--dark'])
    expect(dark).toContain('>Problem</span>')
    expect(classesOf(rootTag(await render('Eyebrow', 'Eyebrow', {}, 'Problem'))), 'default tone is light').toEqual(['t-eyebrow'])
  })
})

describe('Button', () => {
  const EXPECTED: Record<string, string[]> = {
    primary: ['ds-btn', 'ds-btn--primary', 'ds-btn--md'],
    accent: ['ds-btn', 'ds-btn--accent', 'ds-btn--lg'],
    outline: ['ds-btn', 'ds-btn--outline', 'ds-btn--md'],
    ghostDark: ['ds-btn', 'ds-btn--ghostDark'],
    text: ['ds-btn', 'ds-btn--text'],
  }

  it('DS-04 Button renders a type=button button with its variant class and the DS default size', async () => {
    expect(Object.keys(EXPECTED)).toHaveLength(5)
    for (const [variant, classes] of Object.entries(EXPECTED)) {
      const html = await render('Button', 'Button', { variant }, 'Go')
      const root = rootTag(html)
      expect(root.name, variant).toBe('button')
      expect(root.attrs, variant).toEqual({ type: 'button', class: classes.join(' ') })
      expect(html, variant).toContain('Go</button>')
    }
    for (const size of ['sm', 'md', 'lg']) {
      const root = rootTag(await render('Button', 'Button', { variant: 'primary', size }, 'Go'))
      expect(classesOf(root), size).toEqual(['ds-btn', 'ds-btn--primary', `ds-btn--${size}`])
    }
    const dflt = rootTag(await render('Button', 'Button', {}, 'Go'))
    expect(classesOf(dflt), 'default variant').toEqual(EXPECTED.primary)
    for (const variant of ['ghostDark', 'text']) {
      const root = rootTag(await render('Button', 'Button', { variant, size: 'lg' }, 'Go'))
      expect(classesOf(root), `${variant} ignores an explicit size`).toEqual(['ds-btn', `ds-btn--${variant}`])
    }
    const accentSm = rootTag(await render('Button', 'Button', { variant: 'accent', size: 'sm' }, 'Go'))
    expect(classesOf(accentSm), 'accent honours an explicit size').toEqual(['ds-btn', 'ds-btn--accent', 'ds-btn--sm'])
    const outlineLg = rootTag(await render('Button', 'Button', { variant: 'outline', size: 'lg' }, 'Go'))
    expect(classesOf(outlineLg), 'outline honours an explicit size').toEqual(['ds-btn', 'ds-btn--outline', 'ds-btn--lg'])
  })

  it('DS-05 Button with href renders a link', async () => {
    const root = rootTag(await render('Button', 'Button', { href: '#platform', variant: 'ghostDark' }, 'Watch'))
    expect(root.name).toBe('a')
    expect(root.attrs).toEqual({ href: '#platform', class: 'ds-btn ds-btn--ghostDark' })
    expect(root.attrs.type).toBeUndefined()

    const props = { href: '/x', variant: 'primary', disabled: true, type: 'submit', target: '_blank', rel: 'noopener', 'aria-label': 'Open', className: 'extra', style: { width: '100%' } }
    const full = rootTag(await render('Button', 'Button', props, 'Go'))
    expect(full.name).toBe('a')
    expect(full.attrs).toEqual({ href: '/x', class: 'ds-btn ds-btn--primary ds-btn--md extra', style: 'width:100%', 'aria-label': 'Open', target: '_blank', rel: 'noopener' })
    expect('disabled' in full.attrs, 'an anchor carries no disabled attribute').toBe(false)
  })

  it('DS-06 Button forwards type, disabled, aria-label, className and style', async () => {
    const props = { type: 'submit', disabled: true, 'aria-label': 'Send', className: 'btn-block', style: { width: '100%' } }
    const root = rootTag(await render('Button', 'Button', props, 'Go'))
    expect(root.name).toBe('button')
    expect(root.attrs.type).toBe('submit')
    expect('disabled' in root.attrs, 'disabled attribute').toBe(true)
    expect(root.attrs['aria-label']).toBe('Send')
    expect(classesOf(root)).toEqual(['ds-btn', 'ds-btn--primary', 'ds-btn--md', 'btn-block'])
    expect(root.attrs.style).toContain('width:100%')

    const plain = rootTag(await render('Button', 'Button', { disabled: false, type: 'reset' }, 'Go'))
    expect('disabled' in plain.attrs, 'disabled={false} renders no attribute').toBe(false)
    expect(plain.attrs).toEqual({ type: 'reset', class: 'ds-btn ds-btn--primary ds-btn--md' })
  })

  it('DS-14 ghostDark draws the play circle', async () => {
    const html = await render('Button', 'Button', { variant: 'ghostDark' }, 'Watch')
    const child = firstChild(html)
    expect(child?.name).toBe('span')
    expect(child?.attrs).toEqual({ class: 'ds-btn-play', 'aria-hidden': 'true' })
    const svg = svgOf(html)
    expect(svg.attrs.width).toBe('13')
    expect(svg.attrs['stroke-width']).toBe('2')
    expect(GLYPHS.play.length).toBeGreaterThan(0)
    expect(svg.paths).toEqual([...GLYPHS.play])
    for (const variant of ['primary', 'accent', 'outline', 'text']) {
      expect(await render('Button', 'Button', { variant }, 'Go'), variant).not.toContain('ds-btn-play')
    }
    const link = await render('Button', 'Button', { variant: 'ghostDark', href: '#demo' }, 'Watch')
    expect(rootTag(link).name).toBe('a')
    expect(firstChild(link)?.attrs.class, 'a ghostDark link keeps the play circle').toBe('ds-btn-play')
  })
})

describe('Badge and TagPill', () => {
  it('DS-07 Badge renders its tone class and an aria-hidden dot', async () => {
    const tones = ['success', 'progress', 'development']
    expect(tones).toHaveLength(3)
    for (const tone of tones) {
      const html = await render('Badge', 'Badge', { tone, dot: true }, 'Live')
      const root = rootTag(html)
      expect(root.name, tone).toBe('span')
      expect(root.attrs, tone).toEqual({ class: `ds-badge ds-badge--${tone}` })
      const dot = firstChild(html)
      expect(dot?.name, tone).toBe('span')
      expect(dot?.attrs, tone).toEqual({ class: 'ds-badge-dot', 'aria-hidden': 'true' })
      expect(html, tone).toMatch(/<span class="ds-badge-dot" aria-hidden="true"><\/span>Live<\/span>$/)
    }
  })

  it('DS-08 Badge without dot renders no dot', async () => {
    const withDot = await render('Badge', 'Badge', { tone: 'success', dot: true }, 'Live')
    expect(withDot).toContain('ds-badge-dot')
    const html = await render('Badge', 'Badge', { tone: 'success' }, 'Live')
    expect(classesOf(rootTag(html))).toEqual(['ds-badge', 'ds-badge--success'])
    expect(html).toContain('Live')
    expect(html).not.toContain('ds-badge-dot')
  })

  it('DS-09 TagPill renders span.ds-tag', async () => {
    expect(await render('Badge', 'TagPill', {}, 'QR codes')).toBe('<span class="ds-tag">QR codes</span>')
  })
})

describe('IconTile', () => {
  it('DS-10 IconTile is a decorative sized tile holding the named glyph', async () => {
    const html = await render('IconTile', 'IconTile', { name: 'sparkles', tone: 'accent', size: 40 })
    const root = rootTag(html)
    expect(root.name).toBe('span')
    expect(classesOf(root)).toEqual(['ds-icontile', 'ds-icontile--accent'])
    expect(root.attrs['aria-hidden']).toBe('true')
    expect(root.attrs.style).toContain('width:40px')
    expect(root.attrs.style).toContain('height:40px')
    const svg = svgOf(html)
    expect(svg.attrs.width).toBe('20')
    expect(svg.attrs['stroke-width']).toBe('2')
    expect(GLYPHS.sparkles.length).toBeGreaterThan(0)
    expect(svg.paths).toEqual([...GLYPHS.sparkles])

    const dflt = await render('IconTile', 'IconTile', { name: 'shield-check' })
    expect(classesOf(rootTag(dflt)), 'default tone is primary').toEqual(['ds-icontile', 'ds-icontile--primary'])
    expect(rootTag(dflt).attrs.style, 'default size is 40').toBe('width:40px;height:40px')
    expect(svgOf(dflt).attrs.width).toBe('20')
    expect(GLYPHS['shield-check']).not.toEqual(GLYPHS.sparkles)
    expect(svgOf(dflt).paths, 'the glyph follows the name prop').toEqual([...GLYPHS['shield-check']])
  })

  it('DS-11 IconTile iconSize overrides the half-size default', async () => {
    const override = svgOf(await render('IconTile', 'IconTile', { name: 'sparkles', size: 48, iconSize: 30 }))
    expect(override.attrs.width, 'iconSize differs from half the tile').toBe('30')
    const half = svgOf(await render('IconTile', 'IconTile', { name: 'sparkles', size: 36 }))
    expect(half.attrs.width).toBe('18')
    const upper = svgOf(await render('IconTile', 'IconTile', { name: 'sparkles', size: 35 }))
    expect(upper.attrs.width, '17.5 rounds up').toBe('18')
    const lower = svgOf(await render('IconTile', 'IconTile', { name: 'sparkles', size: 33 }))
    expect(lower.attrs.width, '16.5 rounds up').toBe('17')
  })
})

describe('ChecklistItem', () => {
  it('DS-12 ChecklistItem renders an aria-hidden circle-check and its text', async () => {
    const html = await render('ChecklistItem', 'ChecklistItem', {}, 'Audit-ready records')
    const root = rootTag(html)
    expect(root.name).toBe('div')
    expect(root.attrs).toEqual({ class: 'ds-checklist-item' })
    const icon = firstChild(html)
    expect(icon?.name).toBe('span')
    expect(icon?.attrs).toEqual({ class: 'ds-checklist-icon', 'aria-hidden': 'true' })
    const svg = svgOf(html)
    expect(svg.attrs.width).toBe('17')
    expect(svg.attrs['stroke-width']).toBe('2')
    expect(GLYPHS['circle-check'].length).toBeGreaterThan(0)
    expect(svg.paths).toEqual([...GLYPHS['circle-check']])
    expect(html).toMatch(/<\/svg><\/span><span>Audit-ready records<\/span><\/div>$/)
  })
})

describe('Logo', () => {
  it('DS-13 Logo renders the v2 mark and the live wordmark', async () => {
    const html = await render('Logo', 'Logo')
    expect(tagsOf(html).some((t) => t.name === 'span' && t.attrs.class === 'ds-logo'), `no span.ds-logo in ${html}`).toBe(true)
    const img = html.match(/<img\b[^>]*>/)?.[0]
    expect(img, `no <img> in ${html}`).toBeDefined()
    const { attrs } = tagsOf(img as string)[0]
    expect(attrs.class).toBe('ds-logo-mark')
    expect(attrs.src).toMatch(/v2\/assets\/mark\.png(\?.*)?$/)
    expect(attrs.alt).toBe('')
    expect(attrs['aria-hidden']).toBe('true')
    expect(attrs.width).toBe('32')
    expect(attrs.height).toBe('32')
    const name = leaf(html, 'ds-logo-name')
    const region = leaf(html, 'ds-logo-region')
    expect(name.text).toBe('ASComply')
    expect(name.attrs.style).toBe('font-size:19px')
    expect(region.text).toBe('AFRICA')
    expect(region.attrs.style).toBe('font-size:9px')

    const small = await render('Logo', 'Logo', { size: 28 })
    expect(leaf(small, 'ds-logo-name').attrs.style).toBe('font-size:16px')
    expect(leaf(small, 'ds-logo-region').attrs.style).toBe('font-size:8px')
    const smallImg = tagsOf(small.match(/<img\b[^>]*>/)?.[0] as string)[0].attrs
    expect([smallImg.width, smallImg.height], 'the mark follows size').toEqual(['28', '28'])

    const below = await render('Logo', 'Logo', { size: 31 })
    expect(leaf(below, 'ds-logo-name').attrs.style, '17.98 rounds up').toBe('font-size:18px')
    expect(leaf(below, 'ds-logo-region').attrs.style, 'AFRICA drops to 8px under 32').toBe('font-size:8px')
    const large = await render('Logo', 'Logo', { size: 50 })
    expect(leaf(large, 'ds-logo-name').attrs.style).toBe('font-size:29px')
    expect(leaf(large, 'ds-logo-region').attrs.style, 'AFRICA stays 9px above 32').toBe('font-size:9px')
  })
})

describe('class contract with ds.css', () => {
  it('DS-15 every ds- class a primitive renders has a rule in ds.css', async () => {
    const renders = await Promise.all([
      ...['cream', 'dark', 'dark2', 'sage', 'peach'].map((tone) => render('Section', 'Section', { tone }, para('x'))),
      ...['light', 'dark'].map((tone) => render('Eyebrow', 'Eyebrow', { tone }, 'x')),
      ...['primary', 'accent', 'outline', 'ghostDark', 'text'].flatMap((variant) => [
        render('Button', 'Button', { variant }, 'x'),
        render('Button', 'Button', { variant, size: 'sm' }, 'x'),
        render('Button', 'Button', { variant, size: 'lg' }, 'x'),
      ]),
      ...['success', 'progress', 'development'].map((tone) => render('Badge', 'Badge', { tone, dot: true }, 'x')),
      render('Badge', 'TagPill', {}, 'x'),
      ...['primary', 'accent'].map((tone) => render('IconTile', 'IconTile', { name: 'sparkles', tone })),
      render('ChecklistItem', 'ChecklistItem', {}, 'x'),
      render('Logo', 'Logo'),
    ])
    const rendered = new Set(renders.flatMap((html) => tagsOf(html).flatMap(classesOf)).filter((c) => c.startsWith('ds-')))
    expect(rendered.size, 'the render matrix reaches the primitives').toBeGreaterThanOrEqual(25)
    expect(rendered.has('ds-btn--ghostDark')).toBe(true)

    const defined = new Set(classSelectors(stripSource('ds.css', readFileSync(join(LANDING_SRC, 'styles', 'ds.css'), 'utf8'))))
    expect([...rendered].filter((c) => !defined.has(c)).sort()).toEqual([])
  })
})
