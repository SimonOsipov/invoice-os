import { createElement, type ComponentProps } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { Button } from './components/Button'
import { ComingSoonPill } from './components/ComingSoonPill'
import { Logo } from './components/Logo'
import { GLYPHS, Icon } from './icons'

type Tag = { name: string; attrs: string; text: string }
const attr = (t: { attrs: string }, name: string) => t.attrs.match(new RegExp(`\\s${name}="([^"]*)"`))?.[1]
const style = (t: { attrs: string }): Record<string, string> =>
  Object.fromEntries(
    (attr(t, 'style') ?? '')
      .split(';')
      .filter(Boolean)
      .map((d) => [d.slice(0, d.indexOf(':')), d.slice(d.indexOf(':') + 1)]),
  )
const parse = (html: string): Tag[] =>
  [...html.matchAll(/<([a-z][a-z0-9]*)\b([^>]*)>([^<]*)/g)].map((m) => ({ name: m[1], attrs: ` ${m[2]}`, text: m[3].trim() }))
const only = (ts: Tag[], name: string) => {
  const hit = ts.filter((t) => t.name === name)
  expect(hit, `exactly one <${name}>`).toHaveLength(1)
  return hit[0]
}
const withText = (ts: Tag[], text: string) => {
  const hit = ts.filter((t) => t.text === text)
  expect(hit, `exactly one tag with text "${text}"`).toHaveLength(1)
  return hit[0]
}
const btn = (props: Omit<ComponentProps<typeof Button>, 'children'>, label: string) =>
  createElement(Button, { ...props, children: label })
const render = (el: Parameters<typeof renderToStaticMarkup>[0]) => parse(renderToStaticMarkup(el))

describe('library shell primitives', () => {
  it('SH-01 Icon draws a 24-grid stroke glyph at stroke 2', () => {
    const ts = render(createElement(Icon, { name: 'bell', size: 17 }))
    const svg = only(ts, 'svg')
    expect(['width', 'height', 'viewBox', 'fill', 'stroke', 'stroke-width', 'aria-hidden'].map((a) => attr(svg, a))).toEqual([
      '17', '17', '0 0 24 24', 'none', 'currentColor', '2', 'true',
    ])
    const paths = ts.filter((t) => t.name === 'path')
    expect(paths.map((p) => attr(p, 'd'))).toEqual([
      'M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9',
      'M10.3 21a1.94 1.94 0 0 0 3.4 0',
    ])
    expect(render(createElement(Icon, { name: 'pen-tool' })).filter((t) => t.name === 'path')).toHaveLength(4)
    expect(GLYPHS['pen-tool']).toHaveLength(4)
  })

  it('SH-02 Button renders each variant and size the prototype uses', () => {
    const cls = (props: Omit<ComponentProps<typeof Button>, 'children'>) =>
      attr(only(render(btn(props, 'Go')), 'button'), 'class')
    expect(cls({})).toBe('ds-btn ds-btn--primary ds-btn--md')
    expect(cls({ variant: 'primary', size: 'lg' })).toBe('ds-btn ds-btn--primary ds-btn--lg')
    expect(cls({ variant: 'outline', size: 'sm' })).toBe('ds-btn ds-btn--outline ds-btn--sm')
    expect(cls({ variant: 'outlineDark', size: 'sm' })).toBe('ds-btn ds-btn--outlineDark ds-btn--sm')
  })

  it('SH-03 a Button with href is a link, without href a button', () => {
    const link = render(btn({ href: 'https://l.example/?demo' }, 'Book'))
    const a = only(link, 'a')
    expect(attr(a, 'href')).toBe('https://l.example/?demo')
    expect(attr(a, 'type')).toBeUndefined()
    expect(link.some((t) => t.name === 'button')).toBe(false)

    const plain = render(btn({ style: { width: '100%' } }, 'Book'))
    expect(attr(only(plain, 'button'), 'type')).toBe('button')
    expect(plain.some((t) => t.name === 'a')).toBe(false)
    expect(style(only(plain, 'button')).width).toBe('100%')
  })

  it('SH-04 arrow puts arrow-right after the label', () => {
    const html = renderToStaticMarkup(btn({ arrow: true }, 'Take the tour'))
    expect(html.indexOf('Take the tour')).toBeGreaterThan(-1)
    expect(html.indexOf('Take the tour')).toBeLessThan(html.indexOf('<svg'))
    const ts = parse(html)
    const svg = only(ts, 'svg')
    expect([attr(svg, 'width'), attr(svg, 'height')]).toEqual(['16', '16'])
    expect(ts.filter((t) => t.name === 'path').map((p) => attr(p, 'd'))).toEqual(['M5 12h14', 'm12 5 7 7-7 7'])
    expect(renderToStaticMarkup(btn({}, 'Take the tour'))).not.toContain('<svg')
  })

  it('SH-05 Logo is the DS dark lockup at 28', () => {
    const ts = render(createElement(Logo, { size: 28 }))
    const img = only(ts, 'img')
    expect(['width', 'height', 'alt', 'aria-hidden'].map((a) => attr(img, a))).toEqual(['28', '28', '', 'true'])
    expect(style(img)['border-radius']).toBe('var(--radius-md)')
    expect(style(withText(ts, 'ASComply'))).toMatchObject({
      'font-size': '16px',
      color: 'var(--surface-foreground)',
      'font-family': 'var(--font-display)',
    })
    expect(style(withText(ts, 'AFRICA'))).toMatchObject({
      'font-size': '8px',
      color: 'var(--eyebrow-on-dark)',
      'text-transform': 'uppercase',
    })
  })

  it('SH-06 the Coming soon pill has the DS Badge shape in both tones', () => {
    const shape = {
      height: '26px',
      padding: '0 10px',
      'border-radius': 'var(--radius-sm)',
      'font-size': 'var(--fs-xs)',
      'white-space': 'nowrap',
    }
    const light = style(withText(render(createElement(ComingSoonPill, { tone: 'light' })), 'Coming soon'))
    expect(light).toMatchObject({
      ...shape,
      background: 'var(--status-progress-bg)',
      color: 'var(--status-progress-fg)',
      border: '1px solid var(--status-progress-border)',
    })
    const dark = style(withText(render(createElement(ComingSoonPill, { tone: 'dark' })), 'Coming soon'))
    expect(dark).toMatchObject({
      ...shape,
      background: 'var(--on-dark-10)',
      color: 'var(--surface-foreground)',
      border: '1px solid var(--on-dark-20)',
    })
  })
})
