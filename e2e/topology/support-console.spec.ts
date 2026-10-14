// The deployed v2 Support Console. Its two screens, Rules and Tenants, read real data; Rules also switches real rules.
// Resolved values are read at 1440; layout claims assert a relationship at every wide width.
// Screenshots are attached for the reviewer and never asserted.
import { randomUUID } from 'node:crypto'
import { test, expect, type Locator, type Page, type TestInfo } from '@playwright/test'
import { HOUSE_TENANT_ID, provisionStaffAccount, rawFetch, signInSession, type StaffAccount } from '../api/client'
import { collectErrors } from '../personaSession'
import { seedStaffSession } from '../staffSession'
import { enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'

test.use({ viewport: { width: 1440, height: 900 } })

let staff: Promise<StaffAccount> | undefined
const staffAccount = (): Promise<StaffAccount> => (staff ??= provisionStaffAccount('reskin-support'))

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
// Sideways only: a tall body scrolls, so a block below the fold is by design.
const withinX = (outer: Rect, inner: Rect, what: string): string[] =>
  inner.x >= outer.x - 1 && inner.x + inner.width <= outer.x + outer.width + 1 ? [] : [`${what}: sticks out sideways`]
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
  await seedStaffSession(page, 'support', await staffAccount())
  return errors
}

async function reloadRules(page: Page): Promise<void> {
  await page.reload()
  await expect(mainOf(page).getByRole('heading', { level: 1, name: 'Rules admin' })).toBeVisible()
}

const noErrors = (errors: string[], where: string): void => expect(errors, `console errors on ${where}:\n${errors.join('\n')}`).toEqual([])

test('SUP-01 shell: sidebar, header, env switch and sandbox banner', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  const side = aside(page)
  const mn = mainOf(page)
  const header = mn.locator('header')
  const banner = mn.locator('header + div')
  const nav = side.locator('nav button.ops-nav')
  const rulesNav = nav.filter({ hasText: 'Rules' })
  const brand = side.locator('img')
  const crossCard = side.locator('div.ops-hide-narrow').first()
  const footer = side.locator('xpath=./div[last()]')
  const signOut = side.getByRole('button', { name: 'Sign out' })
  const search = header.locator('.ops-header-search')
  const sbx = header.getByRole('button', { name: 'SANDBOX', exact: true })
  const liv = header.getByRole('button', { name: 'LIVE', exact: true })
  const track = parent(sbx)
  const tag = kid(banner, 3)

  await settle(page, side, mn)

  await expect(side).toHaveClass(/\basc-dark\b/)
  await expectStyles(side, 'aside', { 'background-color': 'rgb(8, 47, 49)', 'border-right-color': 'rgb(22, 71, 66)' })
  await expectStyles(rulesNav, 'active nav row', { 'background-color': 'rgb(12, 60, 57)', radius: '6px' })
  await expectStyles(kid(rulesNav, 1), 'active nav bar', { 'background-color': 'rgb(245, 188, 136)', radius: '2px' })
  await expectStyles(kid(rulesNav, 2), 'active nav icon', { color: 'rgb(245, 188, 136)' })
  const eyebrow = await resolveColor(side, '--eyebrow-on-dark')
  const operations = side.getByText('Operations', { exact: true })
  await expectStyles(operations, 'Operations eyebrow', { color: eyebrow })
  await legible(operations, operations, 'Operations eyebrow')
  await expect(rulesNav.locator('span.mono'), 'the Rules nav holds no badge').toHaveCount(0)

  await expect(brand, 'one brand image in the sidebar').toHaveCount(1)
  await expect.poll(() => brand.evaluate((el) => (el as HTMLImageElement).naturalWidth), { message: 'brand image never loaded' }).toBeGreaterThan(0)
  const src = await brand.evaluate((el) => (el as HTMLImageElement).currentSrc)
  expect(src, 'brand image source').toMatch(/\/mark(-[\w-]+)?\.png/)
  expect(src, 'brand image is not the v1 logo-mark').not.toContain('logo-mark')
  await expectStyles(brand, 'brand image', { width: '26px', height: '26px', radius: '6px' })

  await expectStyles(crossCard, 'cross-tenant card', { radius: '6px' })
  await expectStyles(kid(crossCard, 1), 'cross-tenant tile', { radius: '6px' })
  await expectStyles(signOut, 'Sign out', { radius: '7px' })

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

  await attachShot(page, testInfo, 'shell')

  await atWidths(page, 'SUP-01 shell layout', async () => {
    const r = await boxes(page, {
      aside: side,
      img: brand,
      wordmark: side.getByText('ASComply', { exact: true }),
      footer,
      signOut,
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
    const footerPadRight = await footer.evaluate((el) => parseFloat(getComputedStyle(el).paddingRight))
    await noSidewaysScroll(page, 'SUP-01')
    return {
      problems: [
        ...within(r.aside, r.img, 'brand image in the aside'),
        ...leftOf(r.img, r.wordmark, 'brand image / wordmark'),
        ...sameCentreY(r.img, r.wordmark, 'brand image / wordmark'),
        ...navRects.flatMap((n, i) => within(r.aside, n, `nav button ${i} in the aside`)),
        ...below(navRects[navRects.length - 1], r.footer, 'footer / last nav button'),
        ...apart(navRects[navRects.length - 1], r.footer, 'last nav button / footer'),
        ...within(r.footer, r.signOut, 'Sign out in the footer'),
        ...within(r.aside, r.signOut, 'Sign out in the aside'),
        ...(near(r.signOut.x + r.signOut.width, r.footer.x + r.footer.width - footerPadRight, 1)
          ? []
          : [`Sign out right edge ${r.signOut.x + r.signOut.width} vs footer inner edge ${r.footer.x + r.footer.width - footerPadRight}`]),
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

const STAFF_TENANTS = '/api/tenancy/v1/staff/tenants'
// db.DemoTenants: the seeded 2222 in-house tenant (the house tenant is HOUSE_TENANT_ID).
const DEMO_2222_ID = '22222222-2222-2222-2222-222222222222'
const HOUSE_NAME = 'ASComply (dev)'

type LiveTenant = { id: string; name: string; kind: string; demo: boolean; active_members: number; invoice_count: number }
type LiveDetail = { members: { email: string | null }[]; invoices: { invoice_number: string }[] }

async function liveStaffGet<T>(path: string): Promise<T> {
  const res = await rawFetch(path, { headers: { Authorization: `Bearer ${await staffToken()}` } })
  expect(res.status, `GET ${path}`).toBe(200)
  return res.body as T
}

const tenantRow = (page: Page, id: string): Locator => mainOf(page).locator(`button[data-tenant-id="${id}"]`)
// Same text as kindLabel in frontend/support-console/src/tenantsApi.ts.
const kindText = (kind: string): string => (kind === 'firm' ? 'FIRM' : kind === 'in_house' ? 'IN-HOUSE' : kind.toUpperCase())

// Rects of the first `cap` matches: the shared DB list grows with every run.
async function rectsOfFirst(page: Page, loc: Locator, cap: number, what: string): Promise<Rect[]> {
  await settle(page, loc.first())
  const n = Math.min(await loc.count(), cap)
  expect(n, `${what}: none found`).toBeGreaterThan(0)
  const out: Rect[] = []
  for (let i = 0; i < n; i++) {
    const box = await loc.nth(i).boundingBox()
    if (!box) throw new Error(`${what} #${i} has no box`)
    out.push(box)
  }
  return out
}

async function tenantsReads(page: Page, testInfo: TestInfo): Promise<void> {
  const mn = mainOf(page)
  const live = (await liveStaffGet<{ tenants: LiveTenant[] }>(STAFF_TENANTS)).tenants
  const house = tenantRow(page, HOUSE_TENANT_ID)
  const demo2222 = tenantRow(page, DEMO_2222_ID)
  await expect(house, 'the house tenant row has loaded').toBeVisible()
  await expect(demo2222).toBeVisible()

  const demoTag = (row: Locator): Locator => row.getByText('DEMO', { exact: true })
  await expect(demoTag(house)).toBeVisible()
  await expect(demoTag(demo2222)).toBeVisible()
  const plain = live.find((t) => !t.demo)
  expect(plain, 'the live read holds a non-demo tenant').toBeDefined()
  const plainRow = tenantRow(page, plain!.id)
  await expect(plainRow).toBeVisible()
  await expect(demoTag(plainRow)).toHaveCount(0)

  const row2222 = live.find((t) => t.id === DEMO_2222_ID)
  expect(row2222, 'the live read holds the 2222 tenant').toBeDefined()
  const line = `${kindText(row2222!.kind)} · ${row2222!.active_members} active · ${row2222!.invoice_count} invoices`
  await expect(demo2222.getByText(line, { exact: true })).toBeVisible()

  const search = mn.getByLabel('Search tenants')
  await search.fill(HOUSE_NAME)
  await expect(house).toBeVisible()
  await expect(demo2222).toHaveCount(0)
  await search.fill('')
  await expect(demo2222).toBeVisible()

  const detail = await liveStaffGet<LiveDetail>(`${STAFF_TENANTS}/${HOUSE_TENANT_ID}`)
  await house.click()
  await expect(mn.getByRole('heading', { level: 2, name: HOUSE_NAME })).toBeVisible()
  const card = mn.locator('.ops-tenants-grid > div:nth-child(2)')
  await expect(kid(card, 2)).toContainText((await staffAccount()).email)
  await expect(kid(card, 3)).toContainText(detail.invoices.length === 0 ? 'No invoices yet.' : detail.invoices[0].invoice_number)

  await expect(mn.getByRole('button', { name: 'View jobs' }), 'View jobs is gone').toHaveCount(0)
  await expectStyles(parent(demoTag(house)), 'DEMO badge', { radius: '4px' })
  await attachShot(page, testInfo, 'tenants')

  await narrowCollapse(page)
}

// ceiling: one narrow width below the 1024px rule, not swept; sweep it if the breakpoint moves.
async function narrowCollapse(page: Page): Promise<void> {
  const entry = page.viewportSize()
  try {
    await page.setViewportSize({ width: 1000, height: 1080 })
    await expect
      .poll(
        async () => {
          try {
            const kids = await gridKids(page, '.ops-tenants-grid', 'tenants grid')
            return below(kids[0], kids[1], 'tenants detail under the list')
          } catch (err) {
            return [`read threw: ${String((err as Error).message).split('\n')[0]}`]
          }
        },
        { message: 'the tenants grid collapses to one column', timeout: 10_000 },
      )
      .toEqual([])
  } finally {
    if (entry) await page.setViewportSize(entry)
  }
}

async function tenantsLayout(page: Page): Promise<Read> {
  const mn = mainOf(page)
  const kids = await gridKids(page, '.ops-tenants-grid', 'tenants grid')
  const [listCol, detailCol] = kids
  const problems = [...apart(listCol, detailCol, 'tenants grid columns'), ...sameTops(kids, 'tenants grid')]

  const top = await boxes(page, { search: mn.getByLabel('Search tenants').locator('xpath=..'), list: mn.locator('.ops-tenants-grid > div:first-child > div:nth-child(2)') })
  problems.push(...within(listCol, top.search, 'search field in the list column'), ...below(top.search, top.list, 'list card below the search field'), ...within(listCol, top.list, 'list card in the list column'))

  const rowRects = await rectsOfFirst(page, mn.locator('button[data-tenant-id]'), 20, 'tenant row')
  for (const [i, r] of rowRects.entries()) problems.push(...within(top.list, r, `row ${i} in the list card`))

  // The house row is DEMO: its badge and text lines stay inside the row and the badge clears the name.
  const house = tenantRow(page, HOUSE_TENANT_ID)
  const text = kid(house, 2)
  const nameRow = kid(text, 1)
  const rowParts = await boxes(page, { row: house, name: kid(nameRow, 1), badge: kid(nameRow, 2), line2: kid(text, 2), line3: kid(text, 3) })
  problems.push(
    ...within(rowParts.row, rowParts.name, 'row name'),
    ...within(rowParts.row, rowParts.badge, 'DEMO badge in its row'),
    ...apart(rowParts.name, rowParts.badge, 'DEMO badge and name'),
    ...within(rowParts.row, rowParts.line2, 'row counts line'),
    ...within(rowParts.row, rowParts.line3, 'row activity line'),
  )

  const card = mn.locator('.ops-tenants-grid > div:nth-child(2)')
  const parts = await boxes(page, { card, header: kid(card, 1), members: kid(card, 2), invoices: kid(card, 3) })
  problems.push(
    ...within(parts.card, parts.header, 'detail header in the detail card'),
    ...within(parts.card, parts.members, 'Members in the detail card'),
    ...within(parts.card, parts.invoices, 'Recent invoices in the detail card'),
    ...below(parts.members, parts.invoices, 'Recent invoices under Members'),
  )

  const memberRows = kid(kid(card, 2), 2).locator('xpath=./div')
  const nMembers = Math.min(await memberRows.count(), 10)
  expect(nMembers, 'member rows').toBeGreaterThan(0)
  for (let i = 0; i < nMembers; i++) {
    const r = memberRows.nth(i)
    const m = await boxes(page, { row: r, block: kid(r, 2), role: kid(r, 3), status: kid(r, 4) })
    problems.push(
      ...within(m.row, m.role, `member ${i} role chip in its row`),
      ...within(m.row, m.status, `member ${i} status badge in its row`),
      ...within(m.row, m.block, `member ${i} name block in its row`),
      ...pairwiseApart([m.block, m.role, m.status], `member ${i} block, role, status`),
    )
  }

  const invRows = kid(kid(card, 3), 2).locator('xpath=./div')
  for (let i = 0, n = Math.min(await invRows.count(), 10); i < n; i++) {
    const r = invRows.nth(i)
    const v = await boxes(page, { row: r, number: kid(r, 1), status: kid(r, 2) })
    problems.push(...within(v.row, v.number, `invoice ${i} number in its row`), ...within(v.row, v.status, `invoice ${i} status in its row`), ...apart(v.number, v.status, `invoice ${i} number and status`))
  }

  await noSidewaysScroll(page, 'Tenants')
  return { problems, rects: { kids, rowParts, parts } }
}

const SCREENS: Screen[] = [
  {
    nav: 'Rules',
    h1: 'Rules admin',
    rowUp: 2,
    reads: async (page, testInfo) => {
      const mn = mainOf(page)
      await expect(mn.locator('[role="switch"]').first(), 'the rules have loaded').toBeVisible()
      const tags = mn.locator('span.mono').filter({ hasText: /^(DRAFT|IN FORCE|SCHEDULED|SUPERSEDED|RETIRED)$/ })
      await everyStyles(parent(tags), 1, 'version tag', { radius: '4px' })
      await expectStyles(mn.getByText(/^IN FORCE v\d+$/), 'IN FORCE badge', { radius: '4px' })
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
      await attachShot(page, testInfo, 'rules')
    },
    layout: async (page) => {
      const kids = await gridKids(page, '.ops-rules-grid', 'rules grid')
      await noSidewaysScroll(page, 'Rules')
      return { problems: [...apart(kids[0], kids[1], 'rules grid columns'), ...sameTops(kids, 'rules grid')], rects: { kids } }
    },
  },
  {
    nav: 'Tenants',
    h1: 'Tenants',
    rowUp: 2,
    reads: tenantsReads,
    layout: tenantsLayout,
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

test('SUP-04 rule drawer', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  const mn = mainOf(page)

  await openScreen(page, 'Rules', 'Rules admin')
  await expect(mn.locator('[role="switch"]').first()).toBeVisible()
  await mn.locator('.ops-row').first().click()
  const { drawer } = await expectDrawer(page, 'rule drawer')
  await expectStyles(drawer.getByRole('button', { name: 'Kill-switch' }), 'Kill-switch', { radius: '7px' })
  await attachShot(page, testInfo, 'rule-drawer')

  noErrors(errors, 'SUP-04 rule drawer')
})

test('SUP-04 kill confirm', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  const account = await staffAccount()
  const api = (await signInSession(account.email, account.password)).access_token
  if (!(await probeEnabled(api))) await rawFetch(`${STAFF_RULES}/${PROBE_RULE}`, { method: 'PATCH', headers: { Authorization: `Bearer ${api}` }, body: { enabled: true, reason: 'e2e restore' } })

  await reloadRules(page)
  const probeSwitch = mainOf(page).locator('.ops-row').filter({ hasText: PROBE_RULE }).getByRole('switch')
  await expect(probeSwitch).toBeVisible()
  await expect(probeSwitch).toHaveAttribute('aria-checked', 'true')
  await probeSwitch.click()
  const dialog = page.getByRole('dialog').filter({ hasText: 'Disable a live rule?' })
  await expectPanel(page, dialog, 'kill confirm')
  await expectStyles(dialog.locator('h3').locator('xpath=preceding-sibling::span[1]'), 'kill icon tile', { radius: '6px' })
  await expect(dialog.getByRole('textbox', { name: 'Reason' }), 'the confirm asks for a reason').toBeVisible()
  await expect(dialog.getByRole('button', { name: 'Disable rule' }), 'Disable rule waits for a reason').toBeDisabled()
  await expectStyles(dialog.getByRole('button', { name: 'Disable rule' }), 'Disable rule', { 'background-color': 'rgb(162, 50, 50)', color: 'rgb(255, 255, 255)', radius: '7px' })
  await attachShot(page, testInfo, 'kill-confirm')

  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog.locator('h3'), 'Cancel closes the confirm').toBeHidden()

  noErrors(errors, 'SUP-04 kill confirm')
})

// The probe is a global rule no other spec names (contract-validation.spec.ts).
const PROBE_RULE = 'no-duplicate-line-items'
const STAFF_RULES = '/api/validation/v1/staff/rules'

async function probeEnabled(token: string): Promise<boolean> {
  const res = await rawFetch(STAFF_RULES, { headers: { Authorization: `Bearer ${token}` } })
  expect(res.status, 'staff rules list').toBe(200)
  const probe = (res.body as { rules: { key: string; enabled: boolean }[] }).rules.find((r) => r.key === PROBE_RULE)
  if (!probe) throw new Error(`probe rule ${PROBE_RULE} is not in the version in force`)
  return probe.enabled
}

test('SUP-05 kill switch: real rows, reason-gated confirm, switch and restore', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  const account = await staffAccount()
  const api = (await signInSession(account.email, account.password)).access_token
  const mn = mainOf(page)

  try {
    await openScreen(page, 'Rules', 'Rules admin')
    const row = mn.locator('.ops-row').filter({ hasText: PROBE_RULE })
    const sw = row.getByRole('switch')
    await expect(sw, 'the probe row has its switch').toBeVisible()
    await expect(sw).toHaveAttribute('aria-checked', 'true')

    await sw.click()
    const off = page.getByRole('dialog').filter({ hasText: 'Disable a live rule?' })
    await expect(off.getByRole('button', { name: 'Disable rule' }), 'confirm waits for a reason').toBeDisabled()
    await off.getByRole('textbox', { name: 'Reason' }).fill('e2e kill switch')
    await off.getByRole('button', { name: 'Disable rule' }).click()
    await expect(sw, 'the switch reads off').toHaveAttribute('aria-checked', 'false')
    expect(await probeEnabled(api), 'the API agrees the rule is off').toBe(false)

    await expect(sw).toBeEnabled()
    await sw.click()
    const on = page.getByRole('dialog').filter({ hasText: 'Enable this rule?' })
    await expect(on.getByRole('button', { name: 'Enable rule' }), 'confirm waits for a reason').toBeDisabled()
    await on.getByRole('textbox', { name: 'Reason' }).fill('e2e restore')
    await on.getByRole('button', { name: 'Enable rule' }).click()
    await expect(sw, 'the switch reads on').toHaveAttribute('aria-checked', 'true')
    expect(await probeEnabled(api), 'the API agrees the rule is on').toBe(true)
  } finally {
    if (!(await probeEnabled(api))) await rawFetch(`${STAFF_RULES}/${PROBE_RULE}`, { method: 'PATCH', headers: { Authorization: `Bearer ${api}` }, body: { enabled: true, reason: 'e2e restore' } })
  }

  noErrors(errors, 'SUP-05 kill switch')
})

test('SUP-05 kill confirm layout', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)

  await openScreen(page, 'Rules', 'Rules admin')
  const sw = mainOf(page).locator('.ops-row').filter({ hasText: PROBE_RULE }).getByRole('switch')
  await expect(sw).toBeVisible()
  await sw.click()
  const dialog = page.getByRole('dialog').filter({ hasText: 'Disable a live rule?' })
  await expect(dialog).toBeVisible()

  await atWidths(page, 'SUP-05 kill confirm layout', async () => {
    const vp = page.viewportSize()!
    const r = await boxes(page, {
      dialog,
      reason: dialog.getByRole('textbox', { name: 'Reason' }),
      cancel: dialog.getByRole('button', { name: 'Cancel' }),
      confirm: dialog.getByRole('button', { name: 'Disable rule' }),
    })
    return {
      problems: [
        ...within({ x: 0, y: 0, width: vp.width, height: vp.height }, r.dialog, 'dialog in the viewport'),
        ...within(r.dialog, r.reason, 'Reason field in the dialog'),
        ...apart(r.reason, r.cancel, 'Reason / Cancel'),
        ...apart(r.reason, r.confirm, 'Reason / confirm'),
        ...apart(r.cancel, r.confirm, 'Cancel / confirm'),
      ],
      rects: r,
    }
  })

  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog, 'Cancel closes the confirm').toBeHidden()
  noErrors(errors, 'SUP-05 kill confirm layout')
})

// A draft always exists after this: opened if absent, with a probe rule so the publish diff has a row.
const STAFF_VERSIONS = '/api/validation/v1/staff/rule-versions'
const DRAFT_PROBE = 'e2e-desk-probe'
type StaffVersion = { version: number; state: string }

type ProbeRule = { key: string; message: string; when?: string }

async function ensureDraft(token: string, extra?: ProbeRule): Promise<number> {
  const headers = { Authorization: `Bearer ${token}` }
  const findDraft = async (): Promise<StaffVersion | undefined> => {
    const res = await rawFetch(STAFF_VERSIONS, { headers })
    expect(res.status, 'staff versions list').toBe(200)
    return (res.body as { versions: StaffVersion[] }).versions.find((v) => v.state === 'draft')
  }
  let draft = await findDraft()
  if (!draft) {
    const opened = await rawFetch(`${STAFF_VERSIONS}/draft`, { method: 'POST', headers })
    expect([201, 409], 'open a draft').toContain(opened.status)
    draft = await findDraft()
  }
  if (!draft) throw new Error('no draft after opening one')
  const probe: ProbeRule = { key: DRAFT_PROBE, message: 'e2e desk probe' }
  for (const r of [probe, ...(extra ? [extra] : [])]) {
    const rule = { type: r.when ? 'cel' : 'required', target: 'invoice.invoice_number', params: r.when ? { expr: 'true' } : {}, severity: 'info', when: r.when ?? null, enabled: false, ...r }
    const added = await rawFetch(`${STAFF_VERSIONS}/draft/rules`, { method: 'POST', headers, body: rule })
    expect([201, 409], `add draft rule ${r.key}`).toContain(added.status)
  }
  return draft.version
}

async function staffToken(): Promise<string> {
  const account = await staffAccount()
  return (await signInSession(account.email, account.password)).access_token
}

test('SUP-04 publish modal', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  const draft = await ensureDraft(await staffToken())

  await reloadRules(page)
  await mainOf(page).getByRole('button', { name: `Publish draft v${draft}` }).click()
  const dialog = page.getByRole('dialog').filter({ hasText: 'Publish draft → v' })
  await expectPanel(page, dialog, 'publish modal')
  await everyStyles(dialog.locator('span[style*="width: 22px"]'), 1, 'diff sign tile', { radius: '4px' })
  await attachShot(page, testInfo, 'publish-modal')

  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog, 'Cancel closes the modal').toBeHidden()

  noErrors(errors, 'SUP-04 publish modal')
})

const RAIL_ROWS = 'button[aria-pressed]'
const VERSION_TAG = /^(DRAFT|IN FORCE|SCHEDULED|SUPERSEDED|RETIRED)$/

// Chip background and text tokens by tag; the tone is in Rules.tsx VERSION_TONE.
const TONE_TOKENS: Record<string, [string, string]> = {
  'IN FORCE': ['--status-green-bg', '--status-green-text'],
  DRAFT: ['--status-amber-bg', '--status-amber-text'],
  SUPERSEDED: ['--status-muted-bg', '--status-muted-text'],
  RETIRED: ['--status-muted-bg', '--status-muted-text'],
  SCHEDULED: ['--action-tint', '--action'],
}

test('SUP-06 versions rail layout', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  await openScreen(page, 'Rules', 'Rules admin')
  await expect(mainOf(page).locator('[role="switch"]').first()).toBeVisible()

  await atWidths(page, 'SUP-06 versions rail layout', async () => {
    const mn = mainOf(page)
    const { card } = await boxes(page, { card: mn.locator('.ops-rules-grid > div:first-child > div:first-child') })
    const rows = mn.locator(`.ops-rules-grid ${RAIL_ROWS}`)
    const rowRects = await rectsOf(page, rows, 'rail row')
    const chips = await rectsOf(page, rows.locator('xpath=./span[2]'), 'tag chip')
    const texts = await rectsOf(page, rows.locator('xpath=./span[1]'), 'version text')
    const kids = await gridKids(page, '.ops-rules-grid', 'rules grid')
    await noSidewaysScroll(page, 'Rules')
    return {
      problems: [
        ...rowRects.flatMap((r, i) => within(card, r, `rail row ${i} in the rail card`)),
        ...chips.flatMap((c, i) => [...apart(c, texts[i], `tag chip ${i} / version text`), ...within(rowRects[i], c, `tag chip ${i} in its row`)]),
        ...apart(kids[0], kids[1], 'rail / table columns'),
      ],
      rects: { card, rowRects, chips, texts, kids },
    }
  })

  noErrors(errors, 'SUP-06 versions rail layout')
})

test('SUP-06 rules desk paint', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  const mn = mainOf(page)
  await openScreen(page, 'Rules', 'Rules admin')
  await expect(mn.locator('[role="switch"]').first()).toBeVisible()
  await settle(page, mn.locator(RAIL_ROWS).first())

  const rows = mn.locator(`.ops-rules-grid ${RAIL_ROWS}`)
  const seen: string[] = []
  for (const [i, row] of (await eachOf(rows, 1, 'rail row')).entries()) {
    const chip = kid(row, 2)
    const tag = (await kid(chip, 1).innerText()).trim()
    expect(tag, `rail row ${i} tag`).toMatch(VERSION_TAG)
    seen.push(tag)
    const [bg, fg] = TONE_TOKENS[tag]
    await expectStyles(chip, `${tag} chip`, { 'background-color': await resolveColor(mn, bg) })
    await expectStyles(kid(chip, 1), `${tag} chip text`, { color: await resolveColor(mn, fg) })
  }
  expect(seen, 'one version is in force').toContain('IN FORCE')

  const badgeOf = (tag: string): Locator => mn.getByText(new RegExp(`^${tag} v\\d+$`))
  const chipPaint = async (row: Locator): Promise<Want> => ({
    'background-color': (await styles(kid(row, 2), ['background-color']))['background-color'],
    color: (await styles(kid(kid(row, 2), 1), ['color']))['color'],
  })
  const selected = mn.locator(`.ops-rules-grid ${RAIL_ROWS}[aria-pressed="true"]`)
  await expect(selected, 'one row is selected').toHaveCount(1)
  await expectStyles(badgeOf('IN FORCE'), 'table badge of the version in force', await chipPaint(selected))

  const other = rows.filter({ hasNotText: 'IN FORCE' }).filter({ hasNotText: '· 0 rules' }).first()
  await expect(other, 'a version with rules apart from the one in force').toBeVisible()
  const otherTag = (await kid(kid(other, 2), 1).innerText()).trim()
  await other.click()
  await expect(badgeOf(otherTag), 'the table badge follows the selected version').toBeVisible()
  await expectStyles(badgeOf(otherTag), `table badge of ${otherTag}`, await chipPaint(selected))

  const sw = mn.locator('[role="switch"]').first()
  await expect(sw, 'every switch is locked off the version in force').toHaveAttribute('title', 'Only the version in force is switched')
  await expect(sw).toBeDisabled()
  await expectStyles(sw, 'disabled switch', { opacity: '0.45', cursor: 'not-allowed' })

  noErrors(errors, 'SUP-06 rules desk paint')
})

test('SUP-06 rule drawer holds long content', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  const account = await staffAccount()
  const api = (await signInSession(account.email, account.password)).access_token
  const res = await rawFetch(STAFF_RULES, { headers: { Authorization: `Bearer ${api}` } })
  const rules = (res.body as { rules: { key: string; params: unknown }[] }).rules
  const longest = rules.reduce((a, b) => (JSON.stringify(b.params).length > JSON.stringify(a.params).length ? b : a))

  const mn = mainOf(page)
  await openScreen(page, 'Rules', 'Rules admin')
  await mn.getByText(longest.key, { exact: true }).click()
  const { drawer } = await expectDrawer(page, 'rule drawer')
  await noSidewaysScroll(page, 'the rule drawer page')
  const body = drawer.locator('xpath=./div[2]')
  const m = await body.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
  expect(m.scrollWidth - m.clientWidth, 'the rule drawer body scrolls sideways').toBeLessThanOrEqual(1)
  const { drawer: dBox, json } = await boxes(page, { drawer, json: drawer.locator('pre.ops-json') })
  expect(enclosesRect(dBox, json, 1), 'the JSON block sits inside the drawer').toBe(true)

  noErrors(errors, 'SUP-06 rule drawer holds long content')
})

// Never publishes: the API contract suite owns the one real publish. The draft is global, so a
// draft left by an earlier run or retry is reused and the unique rule key keeps this run's rows apart.
test('SUP-06 rules desk: versions, draft edit, test and publish confirm', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  const mn = mainOf(page)
  const key = `e2e-desk-ui-${randomUUID().slice(0, 8)}`
  await openScreen(page, 'Rules', 'Rules admin')
  await expect(mn.locator('[role="switch"]').first()).toBeVisible()

  const rows = mn.locator(`.ops-rules-grid ${RAIL_ROWS}`)
  // A regex: a string hasText is case-insensitive and would match every retired row's "never in force".
  await expect(rows.filter({ hasText: /IN FORCE/ }), 'one version in force').toHaveCount(1)

  const other = rows.filter({ hasNotText: /IN FORCE|DRAFT/ }).filter({ hasNotText: '· 0 rules' }).first()
  await other.click()
  const switches = mn.locator('[role="switch"]')
  await expect(switches.first(), 'switches are locked off the version in force').toBeDisabled()
  await expect(switches.last()).toBeDisabled()

  const newDraft = mn.getByRole('button', { name: 'New draft' })
  if (await newDraft.isEnabled()) await newDraft.click()
  await rows.filter({ hasText: /DRAFT/ }).click()
  const addRule = mn.getByRole('button', { name: 'Add rule' })
  await expect(addRule, 'the draft is selected').toBeVisible()

  await addRule.click()
  const drawer = page.locator('.ops-drawer')
  await drawer.getByLabel('Key', { exact: true }).fill(key)
  await drawer.getByLabel('Severity', { exact: true }).selectOption('info')
  await drawer.getByLabel('Target', { exact: true }).fill(key.replace(/-/g, '_'))
  await drawer.getByLabel('Message', { exact: true }).fill('e2e desk journey')
  await drawer.getByRole('button', { name: 'Save to draft' }).click()
  await expect(drawer, 'saving closes the drawer').toBeHidden()
  await expect(mn.getByText(key, { exact: true }), 'the rule is in the draft table').toBeVisible()

  await mn.getByRole('button', { name: 'Test draft' }).click()
  const testDlg = page.getByRole('dialog').filter({ hasText: 'Sample invoice' })
  await testDlg.getByRole('button', { name: 'Run test' }).click()
  const draftCol = parent(testDlg.getByText(/^Draft v\d+$/))
  const forceCol = parent(testDlg.getByText(/^In force v\d+$/))
  await expect(draftCol.getByText(key), 'the rule fires in the Draft column').toBeVisible()
  await expect(forceCol.getByText(key), 'the rule is not in the In force column').toHaveCount(0)
  await testDlg.getByRole('button', { name: 'Cancel' }).click()
  await expect(testDlg).toBeHidden()

  await mn.getByRole('button', { name: /^Publish draft v\d+$/ }).click()
  const publish = page.getByRole('dialog').filter({ hasText: 'Publish draft → v' })
  const diffRow = parent(parent(publish.getByText(key, { exact: true })))
  await expect(diffRow, 'the diff lists the rule as added').toContainText('ADDED')
  await expect(publish.getByLabel('Start date')).toHaveValue('')
  await expect(publish.getByRole('button', { name: /Publish v\d+$/ }), 'no date, no publish').toBeDisabled()
  await publish.getByRole('button', { name: 'Cancel' }).click()
  await expect(publish, 'Cancel closes the modal').toBeHidden()

  noErrors(errors, 'SUP-06 rules desk journey')
})

const DRAFT_ROW = `.ops-rules-grid ${RAIL_ROWS}`

async function openDraftView(page: Page, token: string, extra?: ProbeRule): Promise<number> {
  const draft = await ensureDraft(token, extra)
  await reloadRules(page)
  await mainOf(page).locator(DRAFT_ROW).filter({ hasText: /DRAFT/ }).click()
  await expect(mainOf(page).getByRole('button', { name: 'Add rule' }), 'the draft is selected').toBeVisible()
  return draft
}

test('SUP-07 draft controls layout', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  const draft = await openDraftView(page, await staffToken())
  const mn = mainOf(page)

  await atWidths(page, 'SUP-07 draft controls layout', async () => {
    const r = await boxes(page, {
      title: parent(mn.getByRole('heading', { level: 1, name: 'Rules admin' })),
      newDraft: mn.getByRole('button', { name: 'New draft' }),
      publish: mn.getByRole('button', { name: `Publish draft v${draft}` }),
      addRule: mn.getByRole('button', { name: 'Add rule' }),
      testDraft: mn.getByRole('button', { name: 'Test draft' }),
      badge: mn.getByText(new RegExp(`^DRAFT v${draft}$`)),
      count: mn.getByText(/^\d+ RULES$/),
    })
    await noSidewaysScroll(page, 'Rules')
    return {
      problems: [
        ...apart(r.newDraft, r.publish, 'New draft / Publish draft'),
        ...apart(r.title, r.newDraft, 'title block / New draft'),
        ...apart(r.title, r.publish, 'title block / Publish draft'),
        ...pairwiseApart([r.addRule, r.testDraft, r.badge, r.count], 'table bar'),
      ],
      rects: r,
    }
  })

  noErrors(errors, 'SUP-07 draft controls layout')
})

test('SUP-07 draft drawer layout', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  await openDraftView(page, await staffToken())
  const mn = mainOf(page)

  await mn.getByRole('button', { name: 'Add rule' }).click()
  const { drawer: fresh } = await expectDrawer(page, 'new rule drawer')
  await atWidths(page, 'SUP-07 draft drawer layout', async () => {
    const names = ['Key', 'Type', 'Severity', 'Target', 'Params', 'When', 'Message']
    const r = await boxes(page, { drawer: fresh, ...Object.fromEntries(names.map((n) => [n, parent(fresh.getByLabel(n, { exact: true }))])), enabled: fresh.getByLabel('Enabled'), save: fresh.getByRole('button', { name: 'Save to draft' }) })
    const fields = [...names.map((n) => r[n as keyof typeof r]), r.enabled]
    return {
      problems: [...fields.flatMap((f, i) => within(r.drawer, f, `field ${i} in the drawer`)), ...pairwiseApart(fields, 'fields'), ...within(r.drawer, r.save, 'Save in the drawer')],
      rects: r,
    }
  })
  await fresh.getByRole('button', { name: 'Close' }).click()

  await mn.getByText(DRAFT_PROBE, { exact: true }).click()
  const { drawer } = await expectDrawer(page, 'draft rule drawer')
  await atWidths(page, 'SUP-07 draft drawer footer', async () => {
    const r = await boxes(page, { drawer, remove: drawer.getByRole('button', { name: /Remove from draft/ }), save: drawer.getByRole('button', { name: 'Save to draft' }) })
    return { problems: [...within(r.drawer, r.remove, 'Remove in the drawer'), ...within(r.drawer, r.save, 'Save in the drawer'), ...apart(r.remove, r.save, 'Remove / Save')], rects: r }
  })

  noErrors(errors, 'SUP-07 draft drawer layout')
})

test('SUP-07 test modal layout', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  await openDraftView(page, await staffToken())

  await mainOf(page).getByRole('button', { name: 'Test draft' }).click()
  const dialog = page.getByRole('dialog').filter({ hasText: 'Sample invoice' })
  await expect(dialog).toBeVisible()
  await dialog.getByRole('button', { name: 'Run test' }).click()
  const draftHead = dialog.getByText(/^Draft v\d+$/)
  await expect(draftHead, 'the result columns show').toBeVisible()

  await atWidths(page, 'SUP-07 test modal layout', async () => {
    const vp = page.viewportSize()!
    const r = await boxes(page, {
      dialog,
      sample: dialog.getByLabel('Sample invoice'),
      draftCol: parent(draftHead),
      forceCol: parent(dialog.getByText(/^In force v\d+$/)),
    })
    return {
      problems: [
        ...within({ x: 0, y: 0, width: vp.width, height: vp.height }, r.dialog, 'dialog in the viewport'),
        ...within(r.dialog, r.sample, 'textarea in the dialog'),
        ...apart(r.draftCol, r.forceCol, 'result columns'),
      ],
      rects: r,
    }
  })

  noErrors(errors, 'SUP-07 test modal layout')
})

test('SUP-07 publish modal layout', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  const draft = await openDraftView(page, await staffToken())

  await mainOf(page).getByRole('button', { name: `Publish draft v${draft}` }).click()
  const dialog = page.getByRole('dialog').filter({ hasText: 'Publish draft → v' })
  await expect(dialog).toBeVisible()

  await atWidths(page, 'SUP-07 publish modal layout', async () => {
    const vp = page.viewportSize()!
    const r = await boxes(page, {
      dialog,
      date: parent(dialog.getByLabel('Start date')),
      cancel: dialog.getByRole('button', { name: 'Cancel' }),
      publish: dialog.getByRole('button', { name: `Publish v${draft}` }),
    })
    return {
      problems: [
        ...within({ x: 0, y: 0, width: vp.width, height: vp.height }, r.dialog, 'dialog in the viewport'),
        ...within(r.dialog, r.date, 'date field in the dialog'),
        ...apart(r.date, r.cancel, 'date / Cancel'),
        ...apart(r.date, r.publish, 'date / Publish'),
        ...apart(r.cancel, r.publish, 'Cancel / Publish'),
      ],
      rects: r,
    }
  })

  noErrors(errors, 'SUP-07 publish modal layout')
})

test('SUP-07 desk controls paint', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  const draft = await openDraftView(page, await staffToken())
  const mn = mainOf(page)
  await settle(page, mn.getByRole('button', { name: 'New draft' }))

  const publish = mn.getByRole('button', { name: `Publish draft v${draft}` })
  const [radius] = await radii(publish)
  const { height } = await styles(publish, ['height'])
  await expectStyles(mn.getByRole('button', { name: 'New draft' }), 'New draft (disabled: a draft exists)', { radius, height, opacity: '0.45', cursor: 'not-allowed' })
  const testDraft = mn.getByRole('button', { name: 'Test draft' })
  await expectStyles(mn.getByRole('button', { name: 'Add rule' }), 'Add rule', { radius, height: (await styles(testDraft, ['height']))['height'] })

  await mn.locator(DRAFT_ROW).filter({ hasText: /IN FORCE/ }).click()
  await mn.locator('[role="switch"]').first().click()
  const kill = page.getByRole('dialog').filter({ hasText: 'Disable a live rule?' })
  const [reasonRadius] = await radii(parent(kill.getByRole('textbox', { name: 'Reason' })))
  await kill.getByRole('button', { name: 'Cancel' }).click()
  await expect(kill).toBeHidden()

  await publish.click()
  const dialog = page.getByRole('dialog').filter({ hasText: 'Publish draft → v' })
  await expect(dialog).toBeVisible()
  await expectStyles(parent(dialog.getByLabel('Start date')), 'date field wrapper', { radius: reasonRadius })

  noErrors(errors, 'SUP-07 desk controls paint')
})

test('SUP-07 draft drawer holds long content', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = await startSupport(page)
  const key = 'e2e-desk-long'
  await openDraftView(page, await staffToken(), { key, message: 'M'.repeat(500), when: `true${' && true'.repeat(37)}` })
  const mn = mainOf(page)

  await mn.getByText(key, { exact: true }).click()
  const { drawer } = await expectDrawer(page, 'draft drawer')
  await expect(drawer.getByLabel('Message')).toHaveValue('M'.repeat(500))
  await noSidewaysScroll(page, 'the draft drawer page')
  const m = await drawer.locator('xpath=./div[2]').evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
  expect(m.scrollWidth - m.clientWidth, 'the draft drawer body scrolls sideways').toBeLessThanOrEqual(1)
  const box = await boxes(page, { drawer })
  for (const name of ['Type', 'Severity', 'Target', 'Params', 'When', 'Message']) {
    const f = await boxes(page, { f: parent(drawer.getByLabel(name, { exact: true })) })
    expect(enclosesRect(box.drawer, f.f, 1), `${name} sits inside the drawer`).toBe(true)
  }

  noErrors(errors, 'SUP-07 draft drawer holds long content')
})

const STAFF_SYNCS = '/api/validation/v1/staff/code-list-syncs'

test('SUP-08 code-list panel and drawer layout', async ({ page }) => {
  test.setTimeout(120_000)
  const readLists = async () => {
    const res = await rawFetch(STAFF_SYNCS, { headers: { Authorization: `Bearer ${await staffToken()}` } })
    return (res.body as { lists: { list: string }[] }).lists
  }
  let lists = await readLists()
  for (let i = 0; i < 6 && lists.length === 0; i++) {
    await page.waitForTimeout(5_000)
    lists = await readLists()
  }
  const errors = await startSupport(page)
  const mn = mainOf(page)
  await openScreen(page, 'Rules', 'Rules admin')
  await expect(mn.locator('[role="switch"]').first()).toBeVisible()
  const panel = mn.locator('.ops-rules-grid > div:first-child > div:nth-child(2)')
  await expect(panel.getByText('Code-list syncs', { exact: true })).toBeVisible()

  if (lists.length === 0) {
    await expect(panel.getByText('No sync recorded yet.'), 'an empty sync log says so').toBeVisible()
    noErrors(errors, 'SUP-08 code-list panel and drawer layout')
    test.skip(true, 'no nrs_code_list_syncs rows on this env; the validation boot sync fills them from live NRS')
  }
  const rows = panel.locator('button')
  await expect(rows, 'one panel row per synced list').toHaveCount(lists.length)

  await atWidths(page, 'SUP-08 code-list panel layout', async () => {
    const { card, versions } = await boxes(page, { card: panel, versions: mn.locator('.ops-rules-grid > div:first-child > div:first-child') })
    const rowRects = await rectsOf(page, rows, 'panel row')
    const names = await rectsOf(page, rows.locator('xpath=./span[1]/span[1]'), 'list name')
    const chips = await rectsOf(page, rows.locator('xpath=./span[1]/span[2]'), 'status chip')
    const dates = await rectsOf(page, rows.locator('xpath=./span[2]'), 'sync date')
    const counts = await rectsOf(page, rows.locator('xpath=./span[3]'), 'counts')
    await noSidewaysScroll(page, 'Rules')
    return {
      problems: [
        ...rowRects.flatMap((r, i) => within(card, r, `panel row ${i} in the panel card`)),
        ...names.flatMap((n, i) => pairwiseApart([n, chips[i], dates[i], counts[i]], `panel row ${i} parts`)),
        ...apart(versions, card, 'versions card / panel'),
        ...(Math.abs(versions.x - card.x) <= 1 ? [] : [`versions card and panel are not left-aligned: ${versions.x} vs ${card.x}`]),
        ...below(versions, card, 'panel'),
      ],
      rects: { card, versions, rowRects, names, chips, dates, counts },
    }
  })

  const rail = mn.locator(`.ops-rules-grid ${RAIL_ROWS}[aria-pressed="false"]`).first()
  await expect(rail, 'a versions-rail row that is not selected').toBeVisible()
  const row = rows.first()
  const paint = ['padding', 'background-color']
  await expectStyles(row, 'panel row paint (D31)', await styles(rail, paint))
  await expectStyles(kid(kid(row, 1), 2), 'panel status chip', { radius: (await radii(kid(rail, 2)))[0] })

  await row.click()
  const { drawer } = await expectDrawer(page, 'code-list drawer')
  await expect(drawer.getByText('CODE LIST', { exact: true })).toBeVisible()
  await atWidths(page, 'SUP-08 code-list drawer layout', async () => {
    const d = await boxes(page, { drawer })
    const blocks = await rectsOf(page, drawer.locator('xpath=./div[2]/div'), 'sync block')
    const codes = await rectsOf(page, drawer.locator('xpath=./div[2]').locator('div.mono'), 'code block')
    await noSidewaysScroll(page, 'the code-list drawer page')
    const m = await drawer.locator('xpath=./div[2]').evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }), undefined, { timeout: 5_000 })
    return {
      problems: [
        ...blocks.flatMap((b, i) => withinX(d.drawer, b, `sync block ${i} in the drawer`)),
        ...codes.flatMap((c, i) => withinX(d.drawer, c, `code block ${i} in the drawer`)),
        ...(m.scrollWidth - m.clientWidth <= 1 ? [] : [`the drawer body scrolls sideways: ${m.scrollWidth} vs ${m.clientWidth}`]),
      ],
      rects: { drawer: d.drawer, blocks, codes },
    }
  })

  noErrors(errors, 'SUP-08 code-list panel and drawer layout')
})
