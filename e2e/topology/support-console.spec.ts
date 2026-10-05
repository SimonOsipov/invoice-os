// The deployed v2 Support Console. The console is mock-backed: this spec pins fixture behaviour and the v2 look, not a contract.
// Resolved values are read at 1440; layout claims assert a relationship at every wide width.
// Screenshots are attached for the reviewer and never asserted.
import { test, expect, type Locator, type Page, type TestInfo } from '@playwright/test'
import { collectErrors, signInAs } from '../personaSession'
import { enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'

test.use({ viewport: { width: 1440, height: 900 } })

const SHADOW_CARD = 'rgba(40, 83, 52, 0.21) 0px 14px 22px -16px'

const firstFamily = (raw: string): string => raw.split(',')[0].replace(/["']/g, '').trim()

type Rgb = { r: number; g: number; b: number; a: number }

// Chrome serialises a resolved colour as rgb()/rgba(), or as color(srgb ...) for a colour mix.
function parseColor(raw: string): Rgb {
  const alpha = (a: string | undefined): number => (a === undefined ? 1 : a.endsWith('%') ? parseFloat(a) / 100 : parseFloat(a))
  const rgb = raw.match(/^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:\s*[,/]\s*([\d.]+%?))?\s*\)$/)
  if (rgb) return { r: +rgb[1], g: +rgb[2], b: +rgb[3], a: alpha(rgb[4]) }
  const srgb = raw.match(/^color\(srgb\s+([\d.]+)\s+([\d.]+)\s+([\d.]+)(?:\s*\/\s*([\d.]+%?))?\s*\)$/)
  if (srgb) return { r: +srgb[1] * 255, g: +srgb[2] * 255, b: +srgb[3] * 255, a: alpha(srgb[4]) }
  throw new Error(`cannot parse the resolved colour ${JSON.stringify(raw)}`)
}

// WCAG 2.x contrast ratio of two opaque resolved colours.
function contrast(a: string, b: string): number {
  const lum = (raw: string): number => {
    const { r, g, b: blue } = parseColor(raw)
    const lin = (c: number): number => {
      const s = c / 255
      return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
    }
    return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(blue)
  }
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

// Fonts settled and two frames painted; the named elements and their ancestors finish their animations.
async function settle(page: Page, ...targets: Locator[]): Promise<void> {
  await page.evaluate(async () => {
    await document.fonts.ready
    await new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r())))
  })
  await settleAnimations(...targets)
}

// Computed values of one element, by CSS property name, in one evaluate.
function styles(loc: Locator, props: string[]): Promise<Record<string, string>> {
  return loc.evaluate((el, props) => {
    const cs = getComputedStyle(el)
    return Object.fromEntries(props.map((p) => [p, cs.getPropertyValue(p)]))
  }, props)
}

const CORNERS = ['border-top-left-radius', 'border-top-right-radius', 'border-bottom-right-radius', 'border-bottom-left-radius']

async function radii(loc: Locator): Promise<string[]> {
  return Object.values(await styles(loc, CORNERS))
}

// The resolved colour of a token inside `scope`: a probe span, so no custom-property string is compared.
function resolveColor(scope: Locator, token: string): Promise<string> {
  return scope.evaluate((el, token) => {
    const probe = document.createElement('span')
    probe.style.color = `var(${token})`
    el.appendChild(probe)
    const value = getComputedStyle(probe).color
    probe.remove()
    return value
  }, token)
}

async function attachShot(page: Page, testInfo: TestInfo, name: string): Promise<void> {
  await testInfo.attach(`${name}.png`, { body: await page.screenshot(), contentType: 'image/png' })
}

// Named boxes in one settled read; a locator with no box throws, which expect.poll retries.
async function boxes<K extends string>(page: Page, named: Record<K, Locator>): Promise<Record<K, Rect>> {
  await settle(page, ...Object.values<Locator>(named))
  const out = {} as Record<K, Rect>
  for (const k of Object.keys(named) as K[]) {
    const box = await named[k].boundingBox()
    if (!box) throw new Error(`${k} has no box`)
    out[k] = box
  }
  return out
}

const within = (outer: Rect, inner: Rect, what: string): string[] => (enclosesRect(outer, inner, 1) ? [] : [`${what}: sticks out`])
const apart = (a: Rect, b: Rect, what: string): string[] => (rectsOverlap(a, b) ? [`${what}: overlap`] : [])
const leftOf = (a: Rect, b: Rect, what: string): string[] => (a.x + a.width <= b.x + 1 ? [] : [`${what}: not left of`])
const below = (above: Rect, lower: Rect, what: string): string[] => (lower.y >= above.y + above.height - 1 ? [] : [`${what}: not below`])
const centreY = (r: Rect): number => r.y + r.height / 2
const sameCentreY = (a: Rect, b: Rect, what: string): string[] => (Math.abs(centreY(a) - centreY(b)) <= 1 ? [] : [`${what}: centres ${centreY(a)} vs ${centreY(b)}`])
const sameTops = (rs: Rect[], what: string): string[] => (rs.every((r) => Math.abs(r.y - rs[0].y) <= 1) ? [] : [`${what}: tops ${rs.map((r) => r.y).join(', ')}`])
const pairwiseApart = (rs: Rect[], what: string): string[] => rs.flatMap((a, i) => rs.slice(i + 1).flatMap((b, j) => apart(a, b, `${what} ${i}/${i + 1 + j}`)))

type Read = { problems: string[]; rects: Record<string, unknown> }

// Reads at every wide width until `read` reports no problem; the entry viewport is restored.
async function atWidths(page: Page, label: string, read: () => Promise<Read>): Promise<unknown[]> {
  const entry = page.viewportSize()
  const out: unknown[] = []
  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })
      let rects: unknown
      await expect
        .poll(
          async () => {
            // expect.poll does not retry a throw, so a throw becomes a problem line.
            try {
              const r = await read()
              rects = r.rects
              return r.problems
            } catch (err) {
              return [`read threw: ${String((err as Error).message).split('\n')[0]}`]
            }
          },
          { message: `${label} at ${width}px`, timeout: 10_000 },
        )
        .toEqual([])
      out.push({ width, ...(rects as object) })
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }
  return out
}

// The scroller is the last child of main; assertPageDoesNotScrollSideways reads the app's .pf-scroll instead.
async function noSidewaysScroll(page: Page, label: string): Promise<{ scrollWidth: number; clientWidth: number }> {
  const m = await page.locator('main > div:last-child').evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
  expect(m.scrollWidth - m.clientWidth, `${label}: the page column scrolls sideways`).toBeLessThanOrEqual(1)
  return m
}

// Rects of every match; no match, or a match with no box, fails.
async function rectsOf(page: Page, loc: Locator, what: string): Promise<Rect[]> {
  await settle(page, loc.first())
  const out: Rect[] = []
  const all = await loc.all()
  expect(all.length, `${what}: none found`).toBeGreaterThan(0)
  for (const [i, el] of all.entries()) {
    const box = await el.boundingBox()
    if (!box) throw new Error(`${what} #${i} has no box`)
    out.push(box)
  }
  return out
}

const kid = (loc: Locator, n: number): Locator => loc.locator(`xpath=./*[${n}]`)
const parent = (loc: Locator): Locator => loc.locator('xpath=..')

const near = (a: number, b: number, tol: number): boolean => Math.abs(a - b) <= tol

function sameColor(got: string, want: string): boolean {
  const g = parseColor(got)
  const w = parseColor(want)
  return near(g.r, w.r, 1) && near(g.g, w.g, 1) && near(g.b, w.b, 1) && near(g.a, w.a, 0.01)
}

type Want = Record<string, string>

// `radius` pins all four corners and `family` the first font family; any other key is a CSS property.
// A key ending in `color` compares channels, so a rgb()/color(srgb) serialisation difference cannot fail it.
async function expectStyles(loc: Locator, what: string, want: Want): Promise<void> {
  const { radius, family, ...css } = want
  if (radius !== undefined) expect(await radii(loc), `${what}: corners`).toEqual([radius, radius, radius, radius])
  const got = await styles(loc, [...Object.keys(css), ...(family === undefined ? [] : ['font-family'])])
  if (family !== undefined) expect(firstFamily(got['font-family']), `${what}: first family`).toBe(family)
  for (const [k, v] of Object.entries(css)) {
    if (k.endsWith('color')) expect(sameColor(got[k], v), `${what}: ${k} is ${got[k]}, want ${v}`).toBe(true)
    else expect(got[k], `${what}: ${k}`).toBe(v)
  }
}

async function eachOf(loc: Locator, floor: number, what: string): Promise<Locator[]> {
  await expect.poll(() => loc.count(), { message: `${what}: fewer than ${floor} found` }).toBeGreaterThanOrEqual(floor)
  return loc.all()
}

async function everyStyles(loc: Locator, floor: number, what: string, want: Want): Promise<void> {
  for (const [i, el] of (await eachOf(loc, floor, what)).entries()) await expectStyles(el, `${what} #${i}`, want)
}

const over = (top: Rgb, base: Rgb): Rgb => ({
  r: top.r * top.a + base.r * (1 - top.a),
  g: top.g * top.a + base.g * (1 - top.a),
  b: top.b * top.a + base.b * (1 - top.a),
  a: 1,
})
const rgbText = (c: Rgb): string => `rgb(${c.r}, ${c.g}, ${c.b})`

// Contrast of `text` against the background behind `box`: translucent layers composite outward to white, and the run's opacity folds in.
async function legible(text: Locator, box: Locator, what: string, opts: { opacityOne?: boolean } = {}): Promise<void> {
  const { color, opacity } = await text.evaluate((el) => {
    let o = 1
    for (let n: Element | null = el; n; n = n.parentElement) o *= Number(getComputedStyle(n).opacity)
    return { color: getComputedStyle(el).color, opacity: o }
  })
  const layers = await box.evaluate((el) => {
    const out: string[] = []
    for (let n: Element | null = el; n; n = n.parentElement) out.push(getComputedStyle(n).backgroundColor)
    return out
  })
  let bg: Rgb = { r: 255, g: 255, b: 255, a: 1 }
  for (const layer of layers.reverse()) bg = over(parseColor(layer), bg)
  const fg = parseColor(color)
  const ink = over({ ...fg, a: fg.a * opacity }, bg)
  if (opts.opacityOne) expect(opacity, `${what}: opacity`).toBe(1)
  expect(contrast(rgbText(ink), rgbText(bg)), `${what}: contrast`).toBeGreaterThanOrEqual(4.5)
}

// The scrim is a colour mix, so its channels are parsed, not compared as a string.
async function expectScrim(scrim: Locator, what: string): Promise<void> {
  const s = await styles(scrim, ['background-color', 'backdrop-filter'])
  const c = parseColor(s['background-color'])
  expect([c.r, c.g, c.b].map((v) => Math.round(v)), `${what}: scrim channels ${s['background-color']}`).toEqual([8, 47, 49])
  expect(near(c.a, 0.55, 0.01), `${what}: scrim alpha ${c.a}`).toBe(true)
  expect(s['backdrop-filter'], `${what}: scrim blur`).toBe('blur(6px)')
}

const aside = (page: Page): Locator => page.locator('aside.ops-sidebar')
const mainOf = (page: Page): Locator => page.locator('main')

async function openScreen(page: Page, nav: string, h1: string): Promise<void> {
  await aside(page).locator('nav button.ops-nav').filter({ hasText: nav }).click()
  await expect(mainOf(page).getByRole('heading', { level: 1, name: h1 })).toBeVisible()
}

async function startSupport(page: Page): Promise<string[]> {
  const errors = collectErrors(page)
  await signInAs(page, 'support')
  return errors
}

const noErrors = (errors: string[], where: string): void => expect(errors, `console errors on ${where}:\n${errors.join('\n')}`).toEqual([])

test('SUP-01 shell and Submissions: sidebar, header, env switch and sandbox banner', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  const side = aside(page)
  const mn = mainOf(page)
  const header = mn.locator('header')
  const banner = mn.locator('header + div')
  const nav = side.locator('nav button.ops-nav')
  const subm = nav.filter({ hasText: 'Submissions' })
  const rulesNav = nav.filter({ hasText: 'Rules' })
  const brand = side.locator('img')
  const crossCard = side.locator('div.ops-hide-narrow').first()
  const bpCard = side.locator('nav .ops-hide-narrow > div')
  const bpTrack = kid(bpCard, 2)
  const userBlock = side.locator('xpath=./div[last()]')
  const search = header.locator('.ops-header-search')
  const sbx = header.getByRole('button', { name: 'SANDBOX', exact: true })
  const liv = header.getByRole('button', { name: 'LIVE', exact: true })
  const track = parent(sbx)
  const tag = kid(banner, 3)

  await settle(page, side, mn)

  await expect(side).toHaveClass(/\basc-dark\b/)
  await expectStyles(side, 'aside', { 'background-color': 'rgb(8, 47, 49)', 'border-right-color': 'rgb(22, 71, 66)' })
  await expectStyles(subm, 'active nav row', { 'background-color': 'rgb(12, 60, 57)', radius: '6px' })
  await expectStyles(kid(subm, 1), 'active nav bar', { 'background-color': 'rgb(245, 188, 136)', radius: '2px' })
  await expectStyles(kid(subm, 2), 'active nav icon', { color: 'rgb(245, 188, 136)' })
  await expectStyles(rulesNav, 'idle nav row', { color: await resolveColor(side, '--fg-2') })
  const eyebrow = await resolveColor(side, '--eyebrow-on-dark')
  for (const [what, el] of [
    ['Operations', side.getByText('Operations', { exact: true })],
    ['APP backpressure', side.getByText('APP backpressure', { exact: true })],
  ] as const) {
    await expectStyles(el, `${what} eyebrow`, { color: eyebrow })
    await legible(el, el, `${what} eyebrow`)
  }
  await expectStyles(subm.locator('span.mono'), 'Submissions nav badge', { radius: '4px' })
  await expectStyles(rulesNav.locator('span.mono'), 'Rules nav badge', { radius: '4px' })

  await expect(brand, 'one brand image in the sidebar').toHaveCount(1)
  await expect.poll(() => brand.evaluate((el) => (el as HTMLImageElement).naturalWidth), { message: 'brand image never loaded' }).toBeGreaterThan(0)
  const src = await brand.evaluate((el) => (el as HTMLImageElement).currentSrc)
  expect(src, 'brand image source').toMatch(/\/mark(-[\w-]+)?\.png/)
  expect(src, 'brand image is not the v1 logo-mark').not.toContain('logo-mark')
  await expectStyles(brand, 'brand image', { width: '26px', height: '26px', radius: '6px' })

  await expectStyles(crossCard, 'cross-tenant card', { radius: '6px' })
  await expectStyles(kid(crossCard, 1), 'cross-tenant tile', { radius: '6px' })
  await expectStyles(bpCard, 'backpressure card', { radius: '6px' })
  await expectStyles(bpTrack, 'backpressure track', { radius: '2px' })
  await expectStyles(kid(bpTrack, 1), 'backpressure fill', { radius: '2px' })
  await expectStyles(kid(userBlock, 1), 'avatar', { radius: '50%' })
  await expectStyles(side.getByRole('button', { name: 'Sign out' }), 'Sign out', { radius: '7px' })

  await expectStyles(header, 'header', { 'background-color': 'rgba(250, 248, 242, 0.95)', 'backdrop-filter': 'blur(18px)', 'border-bottom-color': 'rgb(230, 232, 221)' })
  await expectStyles(search, 'header search', { radius: '7px', 'border-top-color': 'rgb(201, 217, 214)' })
  const fg3 = await resolveColor(mn, '--fg-3')
  for (const [what, el] of [
    ['search text', kid(search, 2)],
    ['search hint', kid(search, 3)],
  ] as const) {
    await expectStyles(el, what, { color: fg3 })
    await legible(el, search, what)
  }
  await expectStyles(track, 'env track', { 'background-color': 'rgb(231, 236, 223)', radius: '7px', 'column-gap': '2px', 'border-top-color': await resolveColor(mn, '--status-amber-border') })
  await expectStyles(sbx, 'SANDBOX active', { 'background-color': 'rgb(7, 60, 61)', color: 'rgb(255, 255, 255)', radius: '4px' })
  await expectStyles(kid(sbx, 1), 'SANDBOX dot', { 'background-color': 'rgb(245, 188, 136)', radius: '50%' })
  await expectStyles(liv, 'LIVE idle', { 'background-color': 'rgba(0, 0, 0, 0)' })
  await expectStyles(kid(liv, 1), 'LIVE dot', { 'background-color': await resolveColor(mn, '--status-green-text') })
  await expectStyles(banner, 'sandbox banner', { 'background-color': 'rgb(244, 227, 200)', 'border-bottom-color': 'rgb(227, 203, 168)' })
  await expectStyles(kid(banner, 2), 'sandbox banner message', { color: 'rgb(116, 84, 33)' })
  await expectStyles(tag, 'sandbox banner tag', { 'white-space': 'nowrap', opacity: '1' })
  await legible(tag, banner, 'sandbox banner tag', { opacityOne: true })

  await attachShot(page, testInfo, 'submissions')

  await atWidths(page, 'SUP-01 shell layout', async () => {
    const r = await boxes(page, {
      aside: side,
      img: brand,
      wordmark: side.getByText('ASComply', { exact: true }),
      bpCard,
      userBlock,
      header,
      track,
      search,
      crumb: kid(header, 1),
      banner,
      msg: kid(banner, 2),
      tag,
      sbx,
      liv,
    })
    const navRects = await rectsOf(page, nav, 'nav button')
    await noSidewaysScroll(page, 'SUP-01')
    return {
      problems: [
        ...within(r.aside, r.img, 'brand image in the aside'),
        ...leftOf(r.img, r.wordmark, 'brand image / wordmark'),
        ...sameCentreY(r.img, r.wordmark, 'brand image / wordmark'),
        ...navRects.flatMap((n, i) => within(r.aside, n, `nav button ${i} in the aside`)),
        ...below(navRects[navRects.length - 1], r.bpCard, 'backpressure card / last nav button'),
        ...within(r.aside, r.userBlock, 'user block in the aside'),
        ...within(r.track, r.sbx, 'SANDBOX in the track'),
        ...within(r.track, r.liv, 'LIVE in the track'),
        ...apart(r.sbx, r.liv, 'SANDBOX / LIVE'),
        ...leftOf(r.sbx, r.liv, 'SANDBOX / LIVE'),
        ...sameCentreY(r.sbx, r.liv, 'SANDBOX / LIVE'),
        ...within(r.header, r.track, 'track in the header'),
        ...within(r.header, r.search, 'search in the header'),
        ...apart(r.track, r.search, 'track / search'),
        ...leftOf(r.crumb, r.search, 'crumb / search'),
        ...within(r.banner, r.tag, 'banner tag in the banner'),
        ...leftOf(r.msg, r.tag, 'banner message / tag'),
        ...apart(r.msg, r.tag, 'banner message / tag'),
      ],
      rects: r,
    }
  })

  noErrors(errors, 'SUP-01')
})

test('SUP-02 Live: the env switch and the red banner', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  const mn = mainOf(page)
  const header = mn.locator('header')
  const banner = mn.locator('header + div')
  const sbx = header.getByRole('button', { name: 'SANDBOX', exact: true })
  const liv = header.getByRole('button', { name: 'LIVE', exact: true })
  const tag = kid(banner, 3)

  await liv.click()
  await settle(page, header, banner)

  await expectStyles(liv, 'LIVE active', { 'background-color': 'rgb(7, 60, 61)', color: 'rgb(255, 255, 255)' })
  await expectStyles(kid(liv, 1), 'LIVE dot', { 'background-color': 'rgb(143, 220, 170)' })
  await expectStyles(kid(sbx, 1), 'SANDBOX dot', { 'background-color': await resolveColor(mn, '--status-amber-text') })
  await expectStyles(parent(sbx), 'env track', { 'border-top-color': 'rgba(29, 115, 67, 0.25)' })
  await expectStyles(banner, 'live banner', { 'background-color': 'rgba(181, 59, 59, 0.08)', 'border-bottom-color': 'rgba(181, 59, 59, 0.28)' })
  await expectStyles(kid(banner, 2), 'live banner message', { color: 'rgb(162, 50, 50)' })
  await expectStyles(tag, 'live banner tag', { opacity: '1' })
  await legible(tag, banner, 'live banner tag', { opacityOne: true })

  await attachShot(page, testInfo, 'live-env')

  await atWidths(page, 'SUP-02 live banner layout', async () => {
    const r = await boxes(page, { banner, msg: kid(banner, 2), tag })
    return {
      problems: [...within(r.banner, r.tag, 'banner tag in the banner'), ...leftOf(r.msg, r.tag, 'banner message / tag'), ...apart(r.msg, r.tag, 'banner message / tag')],
      rects: r,
    }
  })

  noErrors(errors, 'SUP-02')
})

// Header clearances against the prototype: eyebrow 8px above the h1, header row 20px above the content.
async function expectHeader(page: Page, nav: string, h1Name: string, rowUp: number): Promise<void> {
  const h1 = mainOf(page).getByRole('heading', { level: 1, name: h1Name })
  await expectStyles(h1, `${nav} h1`, { family: 'Manrope', 'font-weight': '700', 'font-size': '24px', 'letter-spacing': '-0.96px', color: 'rgb(11, 48, 50)' })
  const m = await h1.evaluate((h, up) => {
    const eb = h.previousElementSibling
    let row: HTMLElement = h as HTMLElement
    for (let i = 0; i < up; i++) row = row.parentElement as HTMLElement
    const er = eb?.getBoundingClientRect()
    return {
      cls: eb?.className ?? '',
      ebMargin: eb ? getComputedStyle(eb).marginBottom : '',
      ebHeight: er?.height ?? -1,
      gap: er ? h.getBoundingClientRect().top - er.bottom : -1,
      rowMargin: getComputedStyle(row).marginBottom,
    }
  }, rowUp)
  expect(m.cls, `${nav}: the h1's previous sibling`).toMatch(/\beyebrow\b/)
  expect(m.ebMargin, `${nav}: eyebrow margin-bottom`).toBe('8px')
  expect(near(m.ebHeight, 10, 1), `${nav}: eyebrow height ${m.ebHeight}`).toBe(true)
  expect(near(m.gap, 8, 1), `${nav}: eyebrow-to-h1 gap ${m.gap}`).toBe(true)
  expect(m.rowMargin, `${nav}: header row margin-bottom`).toBe('20px')
  await expectStyles(mainOf(page).locator('.ops-screen-pad'), `${nav} page pad`, { 'padding-top': '24px', 'padding-left': '26px', 'padding-right': '26px', 'padding-bottom': '56px' })
}

// No blank band above the eyebrow: it starts at its title block's top, and the h1 sits one eyebrow plus 8px below that top.
async function titleBlockRead(page: Page, h1Name: string): Promise<Read> {
  const m = await mainOf(page)
    .getByRole('heading', { level: 1, name: h1Name })
    .evaluate((h) => {
      const eb = h.previousElementSibling as HTMLElement
      const block = eb.parentElement as HTMLElement
      const ebTop = eb.getBoundingClientRect().top
      const blockTop = block.getBoundingClientRect().top
      return { above: ebTop - blockTop, ebHeight: eb.getBoundingClientRect().height, h1Offset: h.getBoundingClientRect().top - blockTop }
    })
  const problems: string[] = []
  if (!near(m.above, 0, 1)) problems.push(`${h1Name}: ${m.above}px blank above the eyebrow`)
  if (!near(m.h1Offset, m.ebHeight + 8, 1)) problems.push(`${h1Name}: h1 sits ${m.h1Offset}px below the title block top, want eyebrow ${m.ebHeight} + 8`)
  return { problems, rects: { titleBlock: m } }
}

const CARD = { radius: '6px', 'border-top-width': '1px', 'border-top-style': 'solid', 'border-top-color': 'rgb(220, 231, 228)', 'box-shadow': 'none', 'background-color': 'rgb(255, 255, 255)' }
const FIGURE = (size: string): Want => ({ family: 'Manrope', 'font-weight': '700', 'font-size': size, 'font-variant-numeric': 'tabular-nums' })

// The focus ring lives on the .ops-field wrapper; the input itself draws none.
async function expectFieldRing(page: Page, input: Locator, what: string): Promise<void> {
  const field = parent(input)
  await expect(field, `${what}: the input sits in an .ops-field wrapper`).toHaveClass(/\bops-field\b/)
  await input.focus()
  await settle(page, field)
  await expectStyles(field, `${what} field`, { 'border-top-color': 'rgb(56, 135, 126)' })
  expect((await styles(field, ['box-shadow']))['box-shadow'], `${what} field ring`).toContain('rgb(56, 135, 126)')
  await expectStyles(input, `${what} input`, { 'box-shadow': 'none' })
}

type Screen = {
  nav: string
  h1: string
  rowUp: number
  reads: (page: Page, testInfo: TestInfo) => Promise<void>
  layout: (page: Page) => Promise<Read>
}

async function gridKids(page: Page, grid: string, what: string): Promise<Rect[]> {
  const rs = await rectsOf(page, mainOf(page).locator(`${grid} > div`), what)
  expect(rs, `${what}: child count`).toHaveLength(2)
  return rs
}

const SCREENS: Screen[] = [
  {
    nav: 'Submissions',
    h1: 'Submissions ops',
    rowUp: 2,
    reads: async (page) => {
      const mn = mainOf(page)
      const stats = mn.locator('.ops-sub-stats > div')
      await expect(stats, 'stat tiles').toHaveCount(4)
      await everyStyles(stats, 4, 'stat tile', CARD)
      await everyStyles(stats.locator('.money'), 4, 'stat figure', FIGURE('20px'))
      const chips = mn.locator('button.ops-chip')
      await expect(chips, 'filter chips').toHaveCount(8)
      await everyStyles(chips, 8, 'chip', { radius: '4px' })
      for (const [i, chip] of (await chips.all()).entries()) await legible(chip.locator('span'), chip, `chip ${i} count`, { opacityOne: true })
      const row = mn.locator('.ops-row').first()
      const badge = kid(kid(row, 4), 1)
      await expectStyles(badge, 'state badge', { radius: '4px' })
      await expectStyles(kid(badge, 1), 'state badge dot', { radius: '50%' })
      await expectStyles(kid(parent(row), 1), 'table head row', { 'background-color': 'rgb(250, 248, 242)' })
      await expectStyles(mn.getByRole('button', { name: 'Re-drive all' }), 'Re-drive all', { 'background-color': 'rgb(162, 50, 50)', color: 'rgb(255, 255, 255)', radius: '7px' })
    },
    layout: async (page) => {
      const mn = mainOf(page)
      const tiles = await rectsOf(page, mn.locator('.ops-sub-stats > div'), 'stat tile')
      const chips = await rectsOf(page, mn.locator('button.ops-chip'), 'chip')
      const { h1 } = await boxes(page, { h1: mn.getByRole('heading', { level: 1, name: 'Submissions ops' }) })
      await noSidewaysScroll(page, 'Submissions')
      return {
        problems: [...pairwiseApart(tiles, 'stat tiles'), ...sameTops(tiles, 'stat tiles'), ...tiles.flatMap((t, i) => apart(t, h1, `stat tile ${i} / h1`)), ...pairwiseApart(chips, 'chips')],
        rects: { tiles, chips, h1 },
      }
    },
  },
  {
    nav: 'Rules',
    h1: 'Rules admin',
    rowUp: 2,
    reads: async (page, testInfo) => {
      const mn = mainOf(page)
      const tags = mn.locator('span.mono').filter({ hasText: /^(DRAFT|ACTIVE|ARCHIVED)$/ })
      await everyStyles(parent(tags), 4, 'version tag', { radius: '4px' })
      await expectStyles(parent(mn.getByText('Learned rules', { exact: true })).locator('span.mono'), 'learned-rules count', { radius: '4px' })
      await expectStyles(mn.getByText('EDITING DRAFT v9', { exact: true }), 'EDITING DRAFT v9', { radius: '4px' })
      const row = mn.locator('.ops-row').first()
      await expectStyles(kid(row, 2), 'type chip', { radius: '4px' })
      await expectStyles(kid(kid(row, 4), 1), 'severity badge', { radius: '4px' })
      const switches = mn.locator('[role="switch"]')
      for (const [i, sw] of (await eachOf(switches, 8, 'rule switch')).entries()) {
        const s = await styles(sw, ['border-top-left-radius', 'height'])
        expect(parseFloat(s['border-top-left-radius']), `switch ${i} is a pill`).toBeGreaterThanOrEqual(parseFloat(s['height']) / 2)
        await expectStyles(kid(sw, 1), `switch ${i} knob`, { radius: '50%', 'box-shadow': 'none' })
      }
      await expectStyles(mn.getByText('Rules', { exact: true }), 'Rules table title', { family: 'Manrope', 'font-weight': '700' })
      await everyStyles(mn.getByRole('button', { name: 'Promote to draft' }), 1, 'Promote to draft', { radius: '7px' })
      await attachShot(page, testInfo, 'rules')
    },
    layout: async (page) => {
      const kids = await gridKids(page, '.ops-rules-grid', 'rules grid')
      await noSidewaysScroll(page, 'Rules')
      return { problems: [...apart(kids[0], kids[1], 'rules grid columns'), ...sameTops(kids, 'rules grid')], rects: { kids } }
    },
  },
  {
    nav: 'Audit',
    h1: 'Audit & evidence explorer',
    rowUp: 2,
    reads: async (page, testInfo) => {
      const mn = mainOf(page)
      await expectStyles(parent(mn.getByText('APPEND-ONLY · IMMUTABLE')), 'APPEND-ONLY badge', { radius: '4px' })
      await everyStyles(mn.locator('button.ops-chip'), 4, 'audit chip', { radius: '4px' })
      const row = mn.locator('.ops-row').first()
      await expectStyles(kid(row, 2), 'audit icon tile', { radius: '4px' })
      await expectStyles(kid(kid(row, 5), 1), 'audit avatar', { radius: '50%' })
      await attachShot(page, testInfo, 'audit')
      const input = mn.getByLabel('Filter audit entries')
      await expectFieldRing(page, input, 'audit')
      await input.fill('zz-no-match')
      const none = mn.getByText('No audit entries match this filter.')
      await expect(none).toBeVisible()
      await expectStyles(none, 'audit empty text', { color: await resolveColor(mn, '--fg-3') })
    },
    layout: async (page) => {
      await noSidewaysScroll(page, 'Audit')
      return { problems: [], rects: {} }
    },
  },
  {
    nav: 'Tenants',
    h1: 'Tenants & entities',
    rowUp: 1,
    reads: async (page, testInfo) => {
      const mn = mainOf(page)
      const grid = mn.locator('.ops-tenants-grid')
      const first = grid.locator('button.ops-nav').first()
      const detail = grid.locator('xpath=./div[2]')
      await expectStyles(kid(kid(first, 2), 1), 'tenant row name', { family: 'Manrope' })
      await expectStyles(kid(first, 1), 'initials tile', { radius: '6px' })
      await expectStyles(kid(first, 3), 'status dot', { radius: '50%' })
      await expectStyles(kid(kid(detail, 1), 1), 'detail tile', { radius: '6px' })
      await expectStyles(mn.locator('h2'), 'tenant h2', { family: 'Manrope', 'font-weight': '700', 'font-size': '19px' })
      await expectStyles(mn.locator('.ops-tenant-kpis .money').first(), 'first KPI figure', { family: 'Manrope', 'font-weight': '700', 'font-size': '20px' })
      const members = mn.locator('.ops-tenant-split > div').first().locator('xpath=./div[2]/div')
      await everyStyles(members.locator('xpath=./span[3]'), 1, 'role tag', { radius: '4px' })
      await everyStyles(members.locator('xpath=./span[1]'), 1, 'member avatar', { radius: '50%' })
      await expectStyles(mn.getByRole('button', { name: 'View jobs' }), 'View jobs', { radius: '7px' })
      await expectStyles(mn.getByRole('button', { name: 'View-as (read-only)' }), 'View-as', { radius: '7px', 'background-color': 'rgb(7, 60, 61)', color: 'rgb(255, 255, 255)' })
      await attachShot(page, testInfo, 'tenants')
      const input = mn.getByLabel('Search tenants')
      await expectFieldRing(page, input, 'tenant search')
      await input.fill('zz-no-match')
      const none = mn.getByText('No tenant matches.')
      await expect(none).toBeVisible()
      await expectStyles(none, 'tenant empty text', { color: await resolveColor(mn, '--fg-3') })
    },
    layout: async (page) => {
      const kids = await gridKids(page, '.ops-tenants-grid', 'tenants grid')
      const kpis = await rectsOf(page, mainOf(page).locator('.ops-tenant-kpis > div'), 'KPI cell')
      await noSidewaysScroll(page, 'Tenants')
      return {
        problems: [...apart(kids[0], kids[1], 'tenants grid columns'), ...sameTops(kids, 'tenants grid'), ...pairwiseApart(kpis, 'KPI cells')],
        rects: { kids, kpis },
      }
    },
  },
  {
    nav: 'System health',
    h1: 'System health',
    rowUp: 2,
    reads: async (page, testInfo) => {
      const cards = mainOf(page).locator('.ops-health-grid > div')
      await expect(cards, 'health cards').toHaveCount(6)
      await everyStyles(cards, 6, 'health card', CARD)
      await everyStyles(cards.locator('.money'), 6, 'health figure', FIGURE('30px'))
      await everyStyles(cards.locator('xpath=./div[1]/span[2]/span[1]'), 6, 'health status dot', { radius: '50%' })
      await attachShot(page, testInfo, 'health')
    },
    layout: async (page) => {
      const cards = await rectsOf(page, mainOf(page).locator('.ops-health-grid > div'), 'health card')
      await noSidewaysScroll(page, 'System health')
      return { problems: [...pairwiseApart(cards, 'health cards'), ...sameTops(cards.slice(0, 3), 'first three health cards')], rects: { cards } }
    },
  },
]

for (const s of SCREENS) {
  test(`SUP-03 ${s.nav}: v2 header, eyebrow clearances and screen reads`, async ({ page }, testInfo) => {
    test.setTimeout(120_000)
    const errors = await startSupport(page)
    await openScreen(page, s.nav, s.h1)
    await settle(page, mainOf(page))

    await expectHeader(page, s.nav, s.h1, s.rowUp)
    await s.reads(page, testInfo)

    await atWidths(page, `SUP-03 ${s.nav} title block`, () => titleBlockRead(page, s.h1))
    await atWidths(page, `SUP-03 ${s.nav} layout`, () => s.layout(page))

    noErrors(errors, `SUP-03 ${s.nav}`)
  })
}

// Seeded mismatches (RECON_ROWS in the console's data.tsx).
const RECON_ROW_COUNT = 4

// Every Reconcile whole inside its cell and the card. Empty array = fine.
// scrolled: below 1440 the card must scroll, and the buttons are read at the scroll end.
async function reconProblems(card: Locator, reconcile: Locator, scrolled = false): Promise<string[]> {
  const problems: string[] = []
  const m = await card.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
  if (scrolled) {
    if (m.scrollWidth <= m.clientWidth) problems.push(`recon card does not scroll at ${card.page().viewportSize()!.width}`)
  } else if (m.scrollWidth - m.clientWidth > 1) problems.push(`recon card overflows by ${m.scrollWidth - m.clientWidth}px`)
  try {
    if (scrolled) {
      await card.evaluate((el) => {
        el.scrollLeft = el.scrollWidth
      })
    }
    const cardBox = await card.boundingBox()
    const buttons = await reconcile.all()
    if (!cardBox) return [...problems, 'recon card has no box']
    if (buttons.length < RECON_ROW_COUNT) problems.push(`Reconcile buttons: ${buttons.length}, want ${RECON_ROW_COUNT}`)
    const where = scrolled ? ' at the scroll end' : ''
    for (const [i, btn] of buttons.entries()) {
      const box = await btn.boundingBox()
      const cell = await btn.evaluate((el) => {
        const r = el.parentElement!.getBoundingClientRect()
        return { x: r.x, y: r.y, width: r.width, height: r.height }
      })
      if (!box) problems.push(`Reconcile ${i} has no box`)
      else problems.push(...within(cell, box, `Reconcile ${i} in its cell${where}`), ...within(cardBox, box, `Reconcile ${i} in the card${where}`))
    }
  } finally {
    if (scrolled) {
      await card.evaluate((el) => {
        el.scrollLeft = 0
      })
    }
  }
  return problems
}

test('SUP-03 Reconciliation: tables, meter and whole Reconcile buttons at 1440', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  const mn = mainOf(page)
  await mn.getByRole('button', { name: 'Reconciliation', exact: true }).click()
  const title = mn.getByText('State mismatches · internal vs APP')
  await expect(title).toBeVisible()
  await settle(page, mn)

  await expectStyles(title, 'recon title', { family: 'Manrope', 'font-weight': '700' })
  const reconcile = mn.getByRole('button', { name: 'Reconcile', exact: true })
  await everyStyles(reconcile, 4, 'Reconcile', { radius: '7px', 'border-top-color': 'rgb(170, 196, 189)' })
  await expectStyles(mn.locator('.money').filter({ hasText: /^82$/ }), 'rate figure', { family: 'Manrope', 'font-weight': '700', 'font-size': '30px' })
  const track = mn.locator('.ops-recon-grid div[style*="height: 6px"]')
  await expectStyles(track, 'meter track', { radius: '2px' })
  await expectStyles(kid(track, 1), 'meter fill', { radius: '2px' })
  await expectStyles(mn.getByRole('button', { name: 'Run sweep now' }), 'Run sweep now', { radius: '7px', 'border-top-color': 'rgb(170, 196, 189)' })

  // First paint at 1440: the card needs no scroll and each Reconcile sits whole in its cell and the card.
  const card = mn.locator('.ops-recon-grid > div').first()
  const first = await reconProblems(card, reconcile)
  expect(first, 'recon card and Reconcile buttons at first paint').toEqual([])

  await attachShot(page, testInfo, 'submissions-recon')

  await atWidths(page, 'SUP-03 Reconciliation layout', async () => {
    const kids = await gridKids(page, '.ops-recon-grid', 'recon grid')
    await noSidewaysScroll(page, 'Reconciliation')
    const width = page.viewportSize()!.width
    const recon = width >= 1440 ? await reconProblems(card, reconcile) : await reconProblems(card, reconcile, true)
    return { problems: [...apart(kids[0], kids[1], 'recon grid columns'), ...sameTops(kids, 'recon grid'), ...recon], rects: { kids } }
  })

  noErrors(errors, 'SUP-03 Reconciliation')
})

// The drawer, its scrim and its edge: shared by the job, rule and audit drawers.
async function expectDrawer(page: Page, what: string): Promise<{ drawer: Locator; scrim: Locator }> {
  const drawer = page.locator('.ops-drawer')
  await expect(drawer, `${what}: drawer opens`).toBeVisible()
  const scrim = drawer.locator('xpath=preceding-sibling::div[1]')
  await settle(page, drawer, scrim)
  await expectStyles(drawer, `${what} drawer`, { 'background-color': 'rgb(250, 248, 242)', 'border-left-width': '1px', 'border-left-color': 'rgb(201, 217, 214)', 'box-shadow': 'none' })
  const box = await drawer.boundingBox()
  const vw = page.viewportSize()!.width
  expect(box && near(box.x + box.width, vw, 1), `${what}: drawer right edge ${box && box.x + box.width} vs viewport ${vw}`).toBe(true)
  await expectScrim(scrim, what)
  return { drawer, scrim }
}

// A modal panel and its scrim: shared by the kill confirm and the publish modal.
async function expectPanel(page: Page, dialog: Locator, what: string): Promise<void> {
  const scrim = parent(dialog)
  await expect(dialog, `${what}: opens`).toBeVisible()
  await settle(page, dialog, scrim)
  await expectStyles(dialog, `${what} panel`, { radius: '10px', 'box-shadow': SHADOW_CARD, 'border-top-width': '1px', 'border-top-color': 'rgb(201, 217, 214)', 'background-color': 'rgb(255, 255, 255)' })
  const box = await dialog.boundingBox()
  const vp = page.viewportSize()!
  expect(box && enclosesRect({ x: 0, y: 0, width: vp.width, height: vp.height }, box, 1), `${what}: panel inside the viewport`).toBe(true)
  await expectScrim(scrim, what)
  await expectStyles(dialog.locator('h3'), `${what} h3`, { family: 'Manrope', 'font-weight': '700', 'font-size': '17px' })
}

test('SUP-04 job drawer: chrome, scrim dismissal and the dead-letter state', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  const mn = mainOf(page)

  await mn.locator('.ops-row').first().click()
  const { drawer, scrim } = await expectDrawer(page, 'job drawer')
  await expectStyles(drawer.getByRole('button', { name: 'Close' }), 'close button', { radius: '7px' })
  await expectStyles(drawer.getByRole('button', { name: 'Cancel', exact: true }), 'drawer Cancel', { radius: '7px', 'font-weight': '600' })
  const badge = parent(drawer.locator('span.mono').filter({ hasText: /^ACCEPTED$/ }))
  await expectStyles(badge, 'drawer state badge', { radius: '4px' })
  await expectStyles(kid(badge, 1), 'drawer state dot', { radius: '50%' })
  await everyStyles(drawer.locator('span[style*="width: 11px"]'), 4, 'timeline dot', { radius: '50%' })
  await expectStyles(drawer.locator('div[style*="grid-template-columns: 1fr 1fr"]').first(), 'meta grid', { radius: '6px' })
  await expectStyles(drawer.locator('pre.ops-json').first(), 'json block', { radius: '6px', 'background-color': 'rgb(8, 47, 49)' })
  await attachShot(page, testInfo, 'job-drawer')

  await scrim.click({ position: { x: 40, y: 300 } })
  await expect(drawer, 'a scrim click closes the drawer').toBeHidden()

  await mn.locator('.ops-row').filter({ hasText: 'DEAD-LETTER' }).first().click()
  await expect(drawer).toBeVisible()
  await expect(drawer.locator('span.mono').filter({ hasText: /^DEAD-LETTER$/ }).first(), 'dead-letter badge in the drawer').toBeVisible()
  await settle(page, drawer)
  await attachShot(page, testInfo, 'job-drawer-deadletter')

  noErrors(errors, 'SUP-04 job drawer')
})

test('SUP-04 red toast: Cancel in the job drawer', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)

  await mainOf(page).locator('.ops-row').first().click()
  await page.locator('.ops-drawer').getByRole('button', { name: 'Cancel', exact: true }).click()
  const toast = page.getByRole('status').filter({ hasText: 'Cancelled · ' })
  await expect(toast).toBeVisible()
  // The toast auto-dismisses: one evaluate reads both values.
  const t = await toast.evaluate((el) => ({ cls: el.className, icon: getComputedStyle(el.firstElementChild!).color }))
  expect(t.cls, 'red toast class').toMatch(/\basc-dark\b/)
  expect(sameColor(t.icon, 'rgb(244, 176, 176)'), `red toast icon ${t.icon}`).toBe(true)

  noErrors(errors, 'SUP-04 red toast')
})

test('SUP-04 rule and audit drawers', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  const mn = mainOf(page)

  await openScreen(page, 'Rules', 'Rules admin')
  await mn.locator('.ops-row').first().click()
  const { drawer } = await expectDrawer(page, 'rule drawer')
  await expectStyles(drawer.getByRole('button', { name: 'Kill-switch' }), 'Kill-switch', { radius: '7px' })
  const run = drawer.getByRole('button', { name: 'Run test' })
  await expectStyles(run, 'Run test', { radius: '7px', 'background-color': 'rgb(7, 60, 61)' })
  await expectStyles(drawer.getByText('No test run yet.'), 'no test yet', { color: await resolveColor(drawer, '--fg-3') })
  await attachShot(page, testInfo, 'rule-drawer')

  await run.click()
  const passed = parent(drawer.getByText('Rule passed'))
  await expect(passed, 'the Rule passed box').toBeVisible()
  await expectStyles(passed, 'Rule passed box', { radius: '6px' })

  await drawer.getByRole('button', { name: 'Close' }).click()
  await expect(drawer).toBeHidden()
  await mn.locator('.ops-row').filter({ hasText: 'line.qty.range' }).click()
  await expect(drawer).toBeVisible()
  await expect(drawer.getByRole('button', { name: 'Kill-switch' }), 'a disabled rule offers no kill-switch').toHaveCount(0)
  await settle(page, drawer)
  await attachShot(page, testInfo, 'rule-drawer-disabled')

  await drawer.getByRole('button', { name: 'Close' }).click()
  await expect(drawer).toBeHidden()
  await openScreen(page, 'Audit', 'Audit & evidence explorer')
  await mn.locator('.ops-row').first().click()
  const audit = await expectDrawer(page, 'audit drawer')
  await expectStyles(kid(audit.drawer, 2), 'audit drawer banner', { 'background-color': 'rgb(239, 246, 244)' })
  await attachShot(page, testInfo, 'audit-drawer')

  noErrors(errors, 'SUP-04 rule and audit drawers')
})

test('SUP-04 kill confirm', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)

  await openScreen(page, 'Rules', 'Rules admin')
  await mainOf(page).locator('[role="switch"]').first().click()
  const dialog = page.getByRole('dialog').filter({ hasText: 'Disable a live rule?' })
  await expectPanel(page, dialog, 'kill confirm')
  await expectStyles(dialog.locator('h3').locator('xpath=preceding-sibling::span[1]'), 'kill icon tile', { radius: '6px' })
  await expectStyles(parent(dialog.getByText('After NRS accreditation')), 'amber note', { radius: '6px' })
  await expectStyles(dialog.getByRole('button', { name: 'Disable rule' }), 'Disable rule', { 'background-color': 'rgb(162, 50, 50)', color: 'rgb(255, 255, 255)', radius: '7px' })
  await attachShot(page, testInfo, 'kill-confirm')

  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog.locator('h3'), 'Cancel closes the confirm').toBeHidden()

  noErrors(errors, 'SUP-04 kill confirm')
})

test('SUP-04 publish modal', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)

  await openScreen(page, 'Rules', 'Rules admin')
  await mainOf(page).getByRole('button', { name: 'Publish draft' }).click()
  const dialog = page.getByRole('dialog').filter({ hasText: 'Publish draft → v9' })
  await expectPanel(page, dialog, 'publish modal')
  await everyStyles(dialog.locator('span[style*="width: 22px"]'), 1, 'diff sign tile', { radius: '4px' })
  await attachShot(page, testInfo, 'publish-modal')

  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog, 'Cancel closes the modal').toBeHidden()

  noErrors(errors, 'SUP-04 publish modal')
})

test('SUP-04 re-drive toast, empty jobs and a clear health card', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  const mn = mainOf(page)

  await mn.getByRole('button', { name: 'Re-drive all' }).click()
  const toast = page.getByRole('status').filter({ hasText: 'Re-drive queued' })
  await expect(toast).toBeVisible()
  // The toast auto-dismisses: one evaluate reads every value before the shot.
  const t = await toast.evaluate((el) => {
    const cs = getComputedStyle(el)
    const icon = getComputedStyle(el.firstElementChild!)
    const tag = getComputedStyle(el.lastElementChild!)
    return {
      cls: el.className,
      background: cs.backgroundColor,
      color: cs.color,
      radii: ['border-top-left-radius', 'border-top-right-radius', 'border-bottom-right-radius', 'border-bottom-left-radius'].map((p) => cs.getPropertyValue(p)),
      shadow: cs.boxShadow,
      icon: icon.color,
      tag: tag.color,
      tagBorder: tag.borderLeftColor,
    }
  })
  expect(t.cls, 'toast class').toMatch(/\basc-dark\b/)
  for (const [what, got, want] of [
    ['background', t.background, 'rgb(8, 47, 49)'],
    ['colour', t.color, 'rgb(247, 246, 237)'],
    ['icon', t.icon, 'rgb(159, 200, 191)'],
    ['tag', t.tag, 'rgb(187, 205, 199)'],
    ['tag left border', t.tagBorder, 'rgb(22, 71, 66)'],
  ] as const) {
    expect(sameColor(got, want), `toast ${what} is ${got}, want ${want}`).toBe(true)
  }
  expect(t.radii, 'toast corners').toEqual(['6px', '6px', '6px', '6px'])
  expect(t.shadow, 'toast shadow').toBe(SHADOW_CARD)
  await settle(page, toast)
  await attachShot(page, testInfo, 'redrive-toast')

  await mn.locator('button.ops-chip').filter({ hasText: 'DEAD-LETTER' }).click()
  const none = mn.getByText('No jobs in this state.')
  await expect(none).toBeVisible()
  await expectStyles(none, 'empty jobs text', { color: await resolveColor(mn, '--fg-3') })
  await attachShot(page, testInfo, 'jobs-empty')

  await openScreen(page, 'System health', 'System health')
  await expect(mn.locator('.ops-health-grid > div').filter({ hasText: 'Dead-letter' }), 'the Dead-letter card is clear').toContainText('CLEAR')
  await settle(page, mn)
  await attachShot(page, testInfo, 'health-clear')

  noErrors(errors, 'SUP-04 re-drive')
})
