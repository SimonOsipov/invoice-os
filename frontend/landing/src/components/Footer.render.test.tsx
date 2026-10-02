// @vitest-environment jsdom
// SSR contract of the v2 footer: brand block, Platform and Connect columns, handlers.
// jsdom serves the click and tab-order cases; renderToStaticMarkup runs unchanged under it.
import { act, createElement } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { Footer, PLATFORM_LINKS } from './Footer'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function noop() {}

// ASCII sub-needle: sidesteps the © and · glyphs and occurs exactly once.
const COPYRIGHT_ROW = '2026 ASComply Africa Limited'

const html = renderToStaticMarkup(createElement(Footer, { onBookDemo: noop }))

type Link = { label: string; href: string }

// The v2 file's footer Platform list (V470), retyped.
const V2_PLATFORM: Link[] = [
  { label: 'Invoice workflows', href: '#platform' },
  { label: 'Country roadmap', href: '#coverage' },
  { label: 'AI-supported intelligence', href: '#intelligence' },
  { label: 'Solutions for partners', href: '#solutions' },
]

function isOrderedSubsequence(list: readonly Link[], of: readonly Link[]): boolean {
  let at = 0
  for (const item of list) {
    while (at < of.length && !(of[at].label === item.label && of[at].href === item.href)) at++
    if (at === of.length) return false
    at++
  }
  return true
}

const decode = (s: string) => s.replace(/&#x27;/g, "'").replace(/&amp;/g, '&')

function attrsOf(openTag: string): Record<string, string> {
  return Object.fromEntries([...openTag.matchAll(/\s([^\s=>/]+)(?:="([^"]*)")?/g)].map((m) => [m[1], m[2] ?? '']))
}

// Both bounds are load-bearing: the bottom row follows Connect and holds controls of its own.
function connectSlice(markup: string): string {
  const start = markup.indexOf('>Connect<')
  expect(start, 'expected to find the Connect column heading').toBeGreaterThan(-1)
  const end = markup.indexOf(COPYRIGHT_ROW)
  expect(end, 'expected the bottom row to follow the Connect column').toBeGreaterThan(start)
  return markup.slice(start, end)
}

function platformSlice(markup: string): string {
  const start = markup.indexOf('>Platform<')
  expect(start, 'expected to find the Platform column heading').toBeGreaterThan(-1)
  const end = markup.indexOf('>Connect<')
  expect(end, 'expected the Connect column to follow Platform').toBeGreaterThan(start)
  return markup.slice(start, end)
}

const controlsOf = (slice: string) =>
  [...slice.matchAll(/<(a|button)\b([^>]*)>([\s\S]*?)<\/\1>/g)].map((m) => ({
    tag: m[1],
    attrs: attrsOf(m[2]),
    label: decode(m[3].replace(/<[^>]*>/g, '')),
  }))

describe('FT-01 brand block: Logo and the two-line tagline', () => {
  it('holds one ds-logo with a 28px img and exactly one <p class="t-body-sm"> reading the tagline', () => {
    expect(html.match(/class="ds-logo"/g)?.length, 'expected exactly one DS Logo').toBe(1)
    const img = /<img\b[^>]*>/.exec(html)?.[0]
    expect(img, 'expected the Logo mark <img>').toBeDefined()
    expect(attrsOf(img!).width, 'Logo size').toBe('28')
    expect(attrsOf(img!).height, 'Logo size').toBe('28')

    const paras = [...html.matchAll(/<p\b([^>]*)>([\s\S]*?)<\/p>/g)]
    expect(paras.length, 'the footer must hold exactly one <p>').toBe(1)
    expect(attrsOf(paras[0][1]).class).toBe('t-body-sm')
    expect(paras[0][2].replace(/<[^>]*>/g, '')).toBe('Clarity for every invoice. Confidence for your business.')
    expect(paras[0][2], 'D-16: one <br/> between the sentences, then a space').toMatch(
      /^Clarity for every invoice\.<br\/?> Confidence for your business\.$/,
    )
  })
})

type Style = Record<string, string>
const styleOf = (openTag: string): Style =>
  Object.fromEntries(
    (attrsOf(openTag).style ?? '')
      .split(';')
      .filter(Boolean)
      .map((d) => [d.slice(0, d.indexOf(':')), d.slice(d.indexOf(':') + 1)]),
  )
const tagsOf = (re: RegExp): string[] => [...html.matchAll(new RegExp(re.source, 'g'))].map((m) => m[1])
const oneTag = (re: RegExp): string => {
  const found = tagsOf(re)
  expect(found.length, `expected exactly one match for ${re}`).toBe(1)
  return found[0]
}

// Resolved values of the footer's inline layout against V459-483.
describe('FT-15 the footer layout carries the V459-483 values', () => {
  const COLUMN: Style = { display: 'grid', gap: '12px', 'align-content': 'start' }
  const expected: [string, () => string[], Style[]][] = [
    ['footer', () => [oneTag(/(<footer\b[^>]*>)/)], [{ background: 'var(--background)', 'border-top': '1px solid var(--header-border)' }]],
    ['container', () => [oneTag(/(<div class="container"[^>]*>)/)], [{ 'padding-block': '56px 32px' }]],
    [
      'top row',
      () => [oneTag(/<div class="container"[^>]*>(<div\b[^>]*>)/)],
      [{ display: 'flex', 'flex-wrap': 'wrap', 'justify-content': 'space-between', gap: '40px 64px', 'padding-bottom': '40px' }],
    ],
    ['brand block', () => [oneTag(/<div class="container"[^>]*><div\b[^>]*>(<div\b[^>]*>)/)], [{ display: 'grid', gap: '18px', 'max-width': '320px' }]],
    ['tagline', () => [oneTag(/(<p\b[^>]*>)/)], [{ margin: '0' }]],
    ['columns wrapper', () => [oneTag(/<\/p><\/div>(<div\b[^>]*>)/)], [{ display: 'flex', 'flex-wrap': 'wrap', gap: '40px 80px' }]],
    ['the two columns', () => tagsOf(/(<div\b[^>]*>)<span class="t-step">/), [COLUMN, COLUMN]],
    [
      'bottom row',
      () => [oneTag(/(<div\b[^>]*>)<span>[^<]*2026 ASComply Africa Limited/)],
      [
        {
          'padding-top': '24px',
          'border-top': '1px solid var(--border)',
          display: 'flex',
          'flex-wrap': 'wrap',
          'justify-content': 'space-between',
          gap: '12px 24px',
          'font-size': '13px',
          color: 'var(--muted-foreground)',
        },
      ],
    ],
    ['bottom group', () => [oneTag(/(<span\b[^>]*>)<a href="\/privacy"/)], [{ display: 'flex', 'flex-wrap': 'wrap', gap: '20px' }]],
    ['Privacy policy', () => [oneTag(/(<a href="\/privacy"[^>]*>)/)], [{ 'font-size': '13px' }]],
    ['Cookie choices', () => [oneTag(/(<button\b[^>]*>)Cookie choices/)], [{ 'font-size': '13px' }]],
  ]

  it.each(expected)('%s inline style', (_name, find, styles) => {
    const tags = find()
    expect(tags.length).toBe(styles.length)
    tags.forEach((tag, i) => expect(styleOf(tag)).toEqual(styles[i]))
  })

  it('the Platform and Connect headings are t-step spans and the container keeps the container class', () => {
    expect(tagsOf(/(<span class="t-step">)/g).length, 'two column headings').toBe(2)
    expect(html).toContain('<div class="container"')
    expect(html).toContain('>Platform</span>')
    expect(html).toContain('>Connect</span>')
  })
})

describe('FT-02 the Platform column is an in-order subsequence of V470', () => {
  it('PLATFORM_LINKS is a subsequence of V2_PLATFORM and the column renders exactly it', () => {
    expect(isOrderedSubsequence(V2_PLATFORM, V2_PLATFORM), 'control: V2_PLATFORM is its own subsequence').toBe(true)
    expect(
      isOrderedSubsequence(
        [
          { label: 'Country roadmap', href: '#coverage' },
          { label: 'Invoice workflows', href: '#platform' },
        ],
        V2_PLATFORM,
      ),
      'control: [Country roadmap, Invoice workflows] is out of order',
    ).toBe(false)

    expect(
      isOrderedSubsequence(PLATFORM_LINKS, V2_PLATFORM),
      `PLATFORM_LINKS is not an ordered subsequence of V470: ${JSON.stringify(PLATFORM_LINKS)}`,
    ).toBe(true)

    const rendered = controlsOf(platformSlice(html)).map((c) => ({ label: c.label, href: c.attrs.href }))
    expect(rendered).toEqual(PLATFORM_LINKS.map((l) => ({ label: l.label, href: l.href })))
  })
})

describe('FT-02b the Platform column renders PLATFORM_LINKS, whatever it holds', () => {
  const saved = [...PLATFORM_LINKS]
  const plant = (links: Link[]) => PLATFORM_LINKS.splice(0, PLATFORM_LINKS.length, ...links)
  afterEach(() => void plant(saved))

  it.each([
    ['', ''],
    ['/', '/'],
  ])('with hrefPrefix %j every entry is one prefixed <a class="a-link">, in order, and no button', (hrefPrefix, p) => {
    plant(V2_PLATFORM)
    expect(PLATFORM_LINKS.length, 'control: the fixture is in place').toBe(V2_PLATFORM.length)
    const slice = platformSlice(renderToStaticMarkup(createElement(Footer, { onBookDemo: noop, hrefPrefix })))
    const controls = controlsOf(slice)
    expect(controls.map((c) => ({ label: c.label, href: c.attrs.href }))).toEqual(
      V2_PLATFORM.map((l) => ({ label: l.label, href: `${p}${l.href}` })),
    )
    for (const c of controls) {
      expect(c.tag, `"${c.label}" is not an <a>`).toBe('a')
      expect(c.attrs.class, `"${c.label}" class`).toBe('a-link')
    }
  })

  it('an empty list renders the Platform heading and zero controls', () => {
    plant(V2_PLATFORM)
    const filled = platformSlice(renderToStaticMarkup(createElement(Footer, { onBookDemo: noop })))
    expect(controlsOf(filled).length, 'control: the reader sees planted links').toBe(V2_PLATFORM.length)

    plant([])
    const slice = platformSlice(renderToStaticMarkup(createElement(Footer, { onBookDemo: noop })))
    expect(slice.startsWith('>Platform<'), 'the heading is gone').toBe(true)
    expect(controlsOf(slice)).toEqual([])
  })
})

describe('FT-03 Connect holds three a-link buttons in order', () => {
  it('lists Book a demo, Open the cockpit, Contact ASComply, each <button type="button" class="a-link">, no <a>', () => {
    const controls = controlsOf(connectSlice(html))
    expect(controls.map((c) => c.label)).toEqual(['Book a demo', 'Open the cockpit', 'Contact ASComply'])
    for (const c of controls) {
      expect(c.tag, `"${c.label}" is not a <button>`).toBe('button')
      expect(c.attrs.type, `"${c.label}" type`).toBe('button')
      expect(c.attrs.class, `"${c.label}" class`).toBe('a-link')
    }
  })
})

describe('FT-04 / FT-05 the footer controls call their handlers', () => {
  let container: HTMLDivElement
  let root: ReturnType<typeof createRoot>
  let consoleError: ReturnType<typeof vi.spyOn>
  let windowErrors: unknown[]
  const onWindowError = (e: ErrorEvent) => {
    windowErrors.push(e.error ?? e.message)
    e.preventDefault()
  }

  beforeEach(() => {
    windowErrors = []
    window.addEventListener('error', onWindowError)
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
  })
  afterEach(() => {
    window.removeEventListener('error', onWindowError)
    act(() => root.unmount())
    container.remove()
    vi.restoreAllMocks()
  })

  const mount = (props: { onBookDemo: () => void; onSignIn?: () => void; onCookieChoices?: () => void }) =>
    act(async () => {
      root.render(createElement(Footer, props))
    })

  const buttonLabelled = (label: string) => {
    const found = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.trim() === label)
    expect(found, `expected a button labelled "${label}"`).toBeDefined()
    return found!
  }

  it('FT-04: Book a demo and Contact ASComply call onBookDemo, Open the cockpit calls onSignIn', async () => {
    const onBookDemo = vi.fn()
    const onSignIn = vi.fn()
    await mount({ onBookDemo, onSignIn })
    expect(onBookDemo, 'control: nothing fired on mount').not.toHaveBeenCalled()

    for (const label of ['Book a demo', 'Open the cockpit', 'Contact ASComply']) {
      const button = buttonLabelled(label)
      await act(async () => {
        button.click()
      })
    }
    expect(onBookDemo).toHaveBeenCalledTimes(2)
    expect(onSignIn).toHaveBeenCalledTimes(1)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it.each([
    ['Book a demo', 1, 0],
    ['Contact ASComply', 1, 0],
    ['Open the cockpit', 0, 1],
  ])('FT-04b: one click on "%s" calls onBookDemo %i time(s) and onSignIn %i time(s)', async (label, book, signIn) => {
    const onBookDemo = vi.fn()
    const onSignIn = vi.fn()
    await mount({ onBookDemo, onSignIn })
    const button = buttonLabelled(label)
    await act(async () => {
      button.click()
    })
    expect(onBookDemo).toHaveBeenCalledTimes(book)
    expect(onSignIn).toHaveBeenCalledTimes(signIn)
  })

  it('FT-04c: Cookie choices calls onCookieChoices once and neither Connect handler', async () => {
    const onBookDemo = vi.fn()
    const onSignIn = vi.fn()
    const onCookieChoices = vi.fn()
    await mount({ onBookDemo, onSignIn, onCookieChoices })
    const button = buttonLabelled('Cookie choices')
    await act(async () => {
      button.click()
    })
    expect(onCookieChoices).toHaveBeenCalledTimes(1)
    expect(onBookDemo).not.toHaveBeenCalled()
    expect(onSignIn).not.toHaveBeenCalled()
  })

  it('FT-04d: Cookie choices without onCookieChoices clicks without throwing or calling a Connect handler', async () => {
    const onBookDemo = vi.fn()
    const onSignIn = vi.fn()
    await mount({ onBookDemo, onSignIn })
    const button = buttonLabelled('Cookie choices')
    await act(async () => {
      button.click()
    })
    expect(windowErrors).toEqual([])
    expect(onBookDemo).not.toHaveBeenCalled()
    expect(onSignIn).not.toHaveBeenCalled()
  })

  it('FT-04e: the footer tab order is Platform links, Connect buttons, Privacy policy, Cookie choices last', async () => {
    const saved = [...PLATFORM_LINKS]
    PLATFORM_LINKS.splice(0, PLATFORM_LINKS.length, ...V2_PLATFORM)
    try {
      await mount({ onBookDemo: noop })
    } finally {
      PLATFORM_LINKS.splice(0, PLATFORM_LINKS.length, ...saved)
    }
    const stops = Array.from(container.querySelectorAll<HTMLElement>('a[href], button, [tabindex]'))
      .filter((el) => el.tabIndex >= 0)
      .map((el) => el.textContent?.trim())
    expect(stops).toEqual([
      ...V2_PLATFORM.map((l) => l.label),
      'Book a demo',
      'Open the cockpit',
      'Contact ASComply',
      'Privacy policy',
      'Cookie choices',
    ])
  })

  it('FT-05: without onSignIn, Open the cockpit clicks without throwing, logging or calling onBookDemo', async () => {
    const onBookDemo = vi.fn()
    await mount({ onBookDemo })
    const button = buttonLabelled('Open the cockpit')
    await act(async () => {
      expect(() => button.click()).not.toThrow()
    })
    expect(windowErrors, 'a throwing default surfaces as a window error event').toEqual([])
    expect(onBookDemo).not.toHaveBeenCalled()
    expect(consoleError).not.toHaveBeenCalled()
  })
})
