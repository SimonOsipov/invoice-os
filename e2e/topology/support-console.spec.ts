// The deployed v2 Support Console. Its screens, Submissions, Rules, Audit, Tenants, System health and AI usage, read real data; Rules also switches real rules. The Blog is in blog-editor.spec.ts.
// Resolved values are read at 1440; layout claims assert a relationship at every wide width.
// Screenshots are attached for the reviewer and never asserted.
import { randomUUID } from 'node:crypto'
import { readFile } from 'node:fs/promises'
import { test, expect, type Locator, type Page, type TestInfo } from '@playwright/test'
import { unzipSync } from 'fflate'
import { apiBase, fleetHeaders, getAuditLog, HOUSE_TENANT_ID, login, PERSONAS, provisionStaffAccount, rawFetch, signInSession, type AuditEvent, type StaffAccount } from '../api/client'
import { collectErrors } from '../personaSession'
import { seedStaffSession } from '../staffSession'
import { enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'
import { scopeStrays } from './metricsScope'

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
const sameColumn = (a: Rect, b: Rect, what: string): string[] => (Math.abs(a.x - b.x) <= 1 && Math.abs(a.width - b.width) <= 1 ? [] : [`${what}: left/width ${a.x}/${a.width} vs ${b.x}/${b.width}`])
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
  // Runs before the nav click; its result reaches `reads` as `armed`.
  arm?: (page: Page) => Promise<unknown>
  reads: (page: Page, testInfo: TestInfo, armed?: unknown) => Promise<void>
  layout: (page: Page) => Promise<Read>
  /** Runs last: it may leave the screen. */
  after?: (page: Page) => Promise<void>
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
  const parts = await boxes(page, { card, header: kid(card, 1), text: kid(kid(card, 1), 2), viewJobs: kid(card, 1).getByRole('button', { name: 'View jobs' }), members: kid(card, 2), invoices: kid(card, 3) })
  problems.push(
    ...within(parts.card, parts.header, 'detail header in the detail card'),
    ...within(parts.header, parts.viewJobs, 'View jobs in the detail header'),
    ...leftOf(parts.text, parts.viewJobs, 'detail text block / View jobs'),
    ...apart(parts.text, parts.viewJobs, 'detail text block / View jobs'),
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

const STAFF_JOBS = '/api/submission/v1/staff/jobs'
// StaffJob in frontend/support-console/src/jobsApi.ts.
type LiveJob = { id: string; tenant_name: string; invoice_number: string }

// Rects of an element's children, in one read.
function childRects(loc: Locator): Promise<Rect[]> {
  return loc.evaluate((el) =>
    [...el.children].map((c) => {
      const r = c.getBoundingClientRect()
      return { x: r.x, y: r.y, width: r.width, height: r.height }
    }),
  )
}

const jobRows = (page: Page): Locator => mainOf(page).locator('button[data-job-id]')
// The tenant (1) and last-error (5) cells of a row; D27.
const TRUNCATING_CELLS = [1, 5]
const TRUNCATION = { 'white-space': 'nowrap', overflow: 'hidden', 'text-overflow': 'ellipsis' }
const ROW_CAP = 20

// One line per cell (1-based) of each row that does not carry the TRUNCATION styles.
function truncationBad(rows: Locator, cells: number[]): Promise<string[]> {
  return rows.evaluateAll(
    (els, args) =>
      els.flatMap((row, i) =>
        args.cells.flatMap((n) => {
          const cs = getComputedStyle(row.children[n - 1])
          return Object.entries(args.want)
            .filter(([k, v]) => cs.getPropertyValue(k) !== v)
            .map(([k, v]) => `row ${i} cell ${n}: ${k} is ${cs.getPropertyValue(k)}, want ${v}`)
        }),
      ),
    { cells, want: TRUNCATION },
  )
}

async function submissionsReads(page: Page, testInfo: TestInfo): Promise<void> {
  const rows = jobRows(page)
  await expect(rows.first(), 'the jobs have loaded').toBeVisible()
  const live = (await liveStaffGet<{ jobs: LiveJob[] }>(STAFF_JOBS)).jobs

  const first = (await rows.all()).slice(0, ROW_CAP)
  for (const [i, row] of first.entries()) {
    const id = await row.getAttribute('data-job-id')
    const job = live.find((j) => j.id === id)
    expect(job, `row ${i} is in the live read`).toBeDefined()
    await expect(kid(row, 1), `row ${i} tenant`).toHaveText(job!.tenant_name)
    await expect(kid(row, 2), `row ${i} invoice number`).toHaveText(job!.invoice_number)
  }

  expect(await truncationBad(rows, TRUNCATING_CELLS), 'every tenant and last-error cell truncates').toEqual([])
  await attachShot(page, testInfo, 'submissions')
}

// The select ends the filter bar: rightmost of `others` and flush with the bar's content edge.
async function selectAtRightEnd(filterBar: Locator, bar: Rect, select: Rect, others: Rect[]): Promise<string[]> {
  const padRight = await filterBar.evaluate((el) => parseFloat(getComputedStyle(el).paddingRight))
  const right = (r: Rect): number => r.x + r.width
  const problems = others.flatMap((o, i) => (right(o) > right(select) + 1 ? [`select: not rightmost, #${i} ends later`] : []))
  if (!near(right(select), right(bar) - padRight, 1)) problems.push(`select right ${right(select)} vs filter bar content edge ${right(bar) - padRight}`)
  return problems
}

async function submissionsLayout(page: Page): Promise<Read> {
  const mn = mainOf(page)
  const filterBar = parent(mn.getByRole('button', { name: 'ALL', exact: true }))
  const chips = filterBar.locator('button.ops-chip')
  const card = mn.locator('.ops-screen-pad > div:last-child')
  const head = kid(card, 1)
  const top = await boxes(page, { titleBlock: parent(mn.getByRole('heading', { level: 1, name: 'Submissions ops' })), filterBar, select: filterBar.locator('select'), card, head })
  const problems = [
    ...below(top.titleBlock, top.filterBar, 'filter bar below the header row'),
    ...below(top.filterBar, top.card, 'table card below the filter bar'),
    ...within(top.filterBar, top.select, 'tenant select in the filter bar'),
  ]

  const chipRects = await rectsOf(page, chips, 'state chip')
  problems.push(...(await selectAtRightEnd(filterBar, top.filterBar, top.select, chipRects)))
  problems.push(...sameTops(chipRects, 'state chips'), ...pairwiseApart([...chipRects, top.select], 'state chips and tenant select'))
  for (const [i, c] of chipRects.entries()) problems.push(...within(top.filterBar, c, `chip ${i} in the filter bar`))

  const headCells = await childRects(head)
  const rows = (await jobRows(page).all()).slice(0, ROW_CAP)
  expect(rows.length, 'job rows').toBeGreaterThan(0)
  for (const [i, row] of rows.entries()) {
    const box = await row.boundingBox()
    if (!box) throw new Error(`job row ${i} has no box`)
    const cells = await childRects(row)
    problems.push(...within(top.card, box, `row ${i} in the card`), ...pairwiseApart(cells, `row ${i} cells`))
    for (const [n, c] of cells.entries()) {
      problems.push(...within(box, c, `row ${i} cell ${n + 1} in its row`))
      if (!near(c.x, headCells[n].x, 1)) problems.push(`row ${i} cell ${n + 1}: left ${c.x} vs header cell ${headCells[n].x}`)
    }
  }

  await noSidewaysScroll(page, 'Submissions')
  return { problems, rects: { top, chipRects, headCells } }
}

// With a tenant chosen the Sentry link joins the filter bar beside the select; no job rows needed.
async function submissionsLinkLayout(page: Page): Promise<Read> {
  const mn = mainOf(page)
  const filterBar = parent(mn.getByRole('button', { name: 'ALL', exact: true }))
  const chips = filterBar.locator('button.ops-chip')
  const link = filterBar.getByRole('link', { name: 'Open in Sentry' })
  const top = await boxes(page, { filterBar, select: filterBar.locator('select'), link })
  const chipRects = await rectsOf(page, chips, 'state chip')
  const problems = [
    ...within(top.filterBar, top.link, 'Sentry link in the filter bar'),
    ...sameTops([top.link, top.select], 'Sentry link and tenant select'),
    ...pairwiseApart([top.link, top.select, ...chipRects], 'Sentry link, tenant select and state chips'),
    ...leftOf(top.link, top.select, 'Sentry link / tenant select'),
    ...(await selectAtRightEnd(filterBar, top.filterBar, top.select, [top.link, ...chipRects])),
  ]
  await noSidewaysScroll(page, 'Submissions with a tenant')
  return { problems, rects: { top, chipRects } }
}

// The house tenant is selected: View jobs opens Submissions with that tenant preset and no state filter.
async function viewJobsLands(page: Page): Promise<void> {
  const mn = mainOf(page)
  const request = page.waitForRequest((r) => r.url().includes('/staff/jobs') && r.url().includes(`tenant=${HOUSE_TENANT_ID}`))
  await mn.getByRole('button', { name: 'View jobs' }).click()
  await request
  await expect(mn.getByRole('heading', { level: 1, name: 'Submissions ops' })).toBeVisible()
  await expect(mn.locator('select[aria-label="Tenant"]'), 'the tenant select shows the house tenant').toHaveValue(HOUSE_TENANT_ID)
  await expect(mn.getByRole('button', { name: 'ALL', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await expect(mn.getByText('Loading jobs…')).toHaveCount(0)
  const live = (await liveStaffGet<{ jobs: LiveJob[] }>(`${STAFF_JOBS}?tenant=${HOUSE_TENANT_ID}`)).jobs
  await expect(jobRows(page), 'the list is the live house-tenant read').toHaveCount(live.length)
  if (live.length === 0) await expect(mn.getByText('No jobs in this state.')).toBeVisible()
  else await expect(jobRows(page).filter({ hasNotText: HOUSE_NAME }), "only the house tenant's jobs are listed").toHaveCount(0)
}

const STAFF_HEALTH = '/api/submission/v1/staff/health'
// internal/systemhealth JobStates
const JOB_STATES = ['queued', 'submitting', 'pending', 'accepted', 'rejected', 'failed', 'dead_lettered', 'cancelled']

type SeenService = { name: string; status: string; build?: string }
type SeenHealth = {
  fleet: { status: string; services: SeenService[] }
  jobs: Record<string, number>
  queue_depth: number
  // systemhealth.JobView: total and per owning service
  river_jobs: { total: { queue_depth: number; dead_lettered: number }; services: Record<string, { queue_depth: number; dead_lettered: number }> }
  trend: { services_down: number | null; queue_depth: number; dead_lettered: number; river_jobs: SeenHealth['river_jobs'] | null }[]
}

type SeenPanel = { key: string; available: boolean; series: { name: string; points: [number, number][] }[] }
// internal/systemhealth PanelData, DeploysSection, LimitsSection
type SeenMetrics = { range: string; service: string; connection_budget: number; panels: SeenPanel[]; limits: { available: boolean; services: { name: string }[] }; deploys: { available: boolean; revisions: { service: string }[] } }
type Armed = { health: SeenHealth; metrics: SeenMetrics; metricsUrl: URL; latest: { body: SeenHealth } }

// Armed before the nav click: the screen's own reads are the comparison, so a shard moving the queue cannot race them.
const armHealth = async (page: Page): Promise<Armed> => {
  const first = (path: string) => page.waitForResponse((r) => r.request().method() === 'GET' && new URL(r.url()).pathname === path, { timeout: 30_000 })
  const [h, m] = await Promise.all([first(STAFF_HEALTH), first('/api/submission/v1/staff/metrics')])
  const health = (await h.json()) as SeenHealth
  // `latest` follows every later health read: the screen renders the body it last got.
  const latest = { body: health }
  // r.json() settles out of order: the sequence is taken at arrival, and only a newer response writes.
  let seq = 0
  let written = 0
  page.on('response', (r) => {
    if (r.request().method() !== 'GET' || new URL(r.url()).pathname !== STAFF_HEALTH || r.status() !== 200) return
    const mine = ++seq
    void r.json().then((b: SeenHealth) => {
      if (mine > written) {
        written = mine
        latest.body = b
      }
    }, () => {})
  })
  return { health, metrics: (await m.json()) as SeenMetrics, metricsUrl: new URL(m.url()), latest }
}

const shortOf = (build?: string): string => (build ? build.slice(0, 7) : '—')
const nonNull = (trend: SeenHealth['trend'], key: 'services_down' | 'river_jobs'): number => trend.filter((p) => p[key] !== null).length

// systemhealth.Panels, in wire order.
const METRIC_KEYS = ['req_per_min', 'error_pct', 'latency_p50_ms', 'latency_p95_ms', 'cpu_pct', 'ram_pct', 'instances', 'cold_starts_per_min', 'sql_cpu_pct', 'sql_ram_pct', 'sql_disk_pct', 'sql_connections', 'latency_p95_fleet_ms', 'startup_latency_p95_ms']
// systemhealth.ConnectionBudget
const CONNECTION_BUDGET = 80
// systemhealth.MetricServices
const METRIC_SERVICES = ['app', 'auth', 'blog', 'dashboard', 'docling', 'gateway', 'invoice', 'landing', 'library', 'notifications', 'ops-console', 'portfolio', 'submission', 'support-console', 'tenancy', 'validation', 'worker']
// Terraform output dashboard_urls in invoice-os-infra.
const DASHBOARD_HREFS: Record<string, string> = {
  'Services dashboard': 'https://console.cloud.google.com/monitoring/dashboards/builder/27155b37-f324-478d-9e9d-c91ede1837f9?project=ascomply-prod',
  'Database and edge dashboard': 'https://console.cloud.google.com/monitoring/dashboards/builder/3c070f78-6025-432a-b178-5cde3fe08514?project=ascomply-prod',
}
const SENTRY_PROJECT = '4512190351474768'

type SeenPoints = [number, number][]
const panelSeries = (m: SeenMetrics, key: string): SeenPanel['series'] => m.panels.find((p) => p.key === key && p.available)?.series.filter((s) => s.points.length > 0) ?? []
// The series the SQL panels draw: the "database" series, else the first.
const sqlSeries = (m: SeenMetrics, key: string): SeenPoints => {
  const all = m.panels.find((p) => p.key === key && p.available)?.series ?? []
  return (all.find((s) => s.name === 'database') ?? all[0])?.points ?? []
}
const panelHasData = (m: SeenMetrics, key: string): boolean =>
  key === 'sql_util' ? ['sql_cpu_pct', 'sql_ram_pct', 'sql_disk_pct'].some((k) => sqlSeries(m, k).length > 0) : key === 'sql_connections' ? sqlSeries(m, key).length > 0 : panelSeries(m, key).length > 0
// Stat source has a point: the 5xx tile needs an error point with a request point on the same series and step.
const statHasData = (m: SeenMetrics, key: string): boolean => {
  if (key === 'req') return panelSeries(m, 'req_per_min').length > 0
  if (key === 'p95') return panelSeries(m, 'latency_p95_ms').length > 0
  if (key === 'connections') return sqlSeries(m, 'sql_connections').length > 0
  const req = new Map(panelSeries(m, 'req_per_min').map((s) => [s.name, new Set(s.points.map((p) => p[0]))]))
  return panelSeries(m, 'error_pct').some((s) => s.points.some((p) => req.get(s.name)?.has(p[0])))
}

// Every Cloud Run series, limits and deploy name is a fleet service, and is `only` when given.
function scopeMatch(m: SeenMetrics, what: string, only?: string): void {
  expect(scopeStrays(m, only ? [only] : METRIC_SERVICES), `${what}: every Cloud Run series, limits and deploy name is ${only ?? 'in METRIC_SERVICES'}`).toEqual([])
}

// Per panel key, the series names and point counts: evidence for the preview-scope check.
const drawSummary = (m: SeenMetrics) => Object.fromEntries(m.panels.map((p) => [p.key, p.series.map((s) => ({ name: s.name, points: s.points.length }))]))

// Series names a panel can draw: Cloud SQL panels name their own lines.
const servedNames = (m: SeenMetrics, key: string): string[] =>
  key === 'sql_util' ? ['CPU', 'RAM', 'Disk'] : key === 'sql_connections' ? ['connections'] : panelSeries(m, key).map((s) => s.name)

// Panels and stat tiles equal one metrics answer: No data iff no point, else a line.
async function metricsMatch(page: Page, m: SeenMetrics, what: string): Promise<void> {
  const mn = mainOf(page)
  await expect(mn.getByText('Loading metrics…'), `${what}: metrics have loaded`).toHaveCount(0)
  const panels = mn.locator('.ops-health-panels > [data-panel]')
  await expect(panels, `${what}: panels`).toHaveCount(PANEL_KEYS.length)
  for (const key of PANEL_KEYS) {
    const p = mn.locator(`[data-panel="${key}"]`)
    const has = panelHasData(m, key)
    await expect(p.locator('[data-no-data]'), `${what}: ${key} No data`).toHaveCount(has ? 0 : 1)
    if (!has) await expect(p.locator('[data-no-data]'), `${what}: ${key} note`).toHaveText('No data')
    const chart = p.locator('[data-chart]')
    if (has) {
      await expect(chart, `${what}: ${key} draws a line`).toHaveAttribute('data-drawn', /.+/)
      const drawn = ((await chart.getAttribute('data-drawn')) ?? '').split(',')
      expect(servedNames(m, key), `${what}: ${key} data-drawn names are served series`).toEqual(expect.arrayContaining(drawn))
    } else await expect(chart, `${what}: ${key} draws no chart`).toHaveCount(0)
  }
  for (const key of STAT_KEYS.filter((k) => k !== 'fleet')) {
    const t = mn.locator(`[data-tile="${key}"]`)
    const has = statHasData(m, key)
    await expect(t.getByText('NO DATA', { exact: true }), `${what}: ${key} NO DATA`).toHaveCount(has ? 0 : 1)
    if (key === 'p95') continue
    if (has) await expect(t.locator('.money'), `${what}: ${key} value`).not.toHaveText('—')
    else await expect(t.locator('.money'), `${what}: ${key} value`).toHaveText('—')
  }
  for (const key of MAX_TILES) {
    const max = mn.locator(`[data-tile="${key}"] [data-max]`)
    const has = statHasData(m, key)
    await expect(max, `${what}: ${key} max line`).toHaveCount(has ? 1 : 0)
    if (has) await expect(max, `${what}: ${key} max text`).toHaveText(MAX_TEXT[key])
  }
  // p95: fleet value and a worst line at All services; a picked service shows its own value alone
  const p95 = mn.locator('[data-tile="p95"]')
  const allServices = m.service === ''
  const p95Has = statHasData(m, 'p95')
  await expect(p95.locator('[data-worst]'), `${what}: p95 worst line`).toHaveCount(allServices && p95Has ? 1 : 0)
  const noValue = !p95Has || (allServices && panelSeries(m, 'latency_p95_fleet_ms').length === 0)
  if (noValue) await expect(p95.locator('.money'), `${what}: p95 value`).toHaveText('—')
  else await expect(p95.locator('.money'), `${what}: p95 value`).not.toHaveText('—')
}

// The armed answers drive the first comparison; the chip and the select each send a fresh read.
async function metricsReads(page: Page, testInfo: TestInfo, first: SeenMetrics, firstUrl: URL): Promise<void> {
  const mn = mainOf(page)
  expect(firstUrl.searchParams.get('range'), 'first metrics request range').toBe('1h')
  expect(firstUrl.searchParams.has('service'), 'first metrics request names no service').toBe(false)
  expect(first.panels.map((p) => p.key), 'metrics panel keys are the systemhealth panels').toEqual(METRIC_KEYS)
  expect(first.connection_budget, 'systemhealth.ConnectionBudget').toBe(CONNECTION_BUDGET)
  await expect(mn.getByRole('button', { name: '1h', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await metricsMatch(page, first, 'range 1h')
  // Cloud SQL series fill every step, so this holds even before the PR's own series land (4-minute lag).
  expect(await mn.locator('[data-panel] [data-no-data]').count(), 'some metric panel draws: all No data means run-preview lacks roles/monitoring.viewer on ascomply-preview, or the metrics path broke').toBeLessThan(PANEL_KEYS.length)
  scopeMatch(first, 'range 1h')
  // A wrong PreviewSuffix empties every PR-scoped read; the unscoped Cloud SQL panels would still pass above.
  expect(first.limits.available, 'Core AC 2: limits are available; unavailable means run-preview lacks roles/run.viewer on ascomply-preview or the PR suffix matched nothing').toBe(true)
  expect(first.limits.services.map((s) => s.name).sort(), 'Core AC 2: limits list the PR\'s own services (scripts/ci/preview.sh BACKENDS + SITES = systemhealth.MetricServices); missing names mean the PR suffix matched nothing').toEqual([...METRIC_SERVICES].sort())
  await expect(mn.locator('[data-tile="connections"]').getByText(`of ${CONNECTION_BUDGET}`, { exact: true }), 'connections tile shows the budget').toHaveCount(1)

  const read = (range: string, service: string | null) =>
    page.waitForResponse((r) => {
      const u = new URL(r.url())
      return r.request().method() === 'GET' && u.pathname === '/api/submission/v1/staff/metrics' && u.searchParams.get('range') === range && u.searchParams.get('service') === service
    })
  const sent = (range: string, service: string | null) =>
    page.waitForRequest((r) => {
      const u = new URL(r.url())
      return u.pathname === '/api/submission/v1/staff/metrics' && u.searchParams.get('range') === range && u.searchParams.get('service') === service
    })

  const [, six] = await Promise.all([sent('6h', null), read('6h', null), mn.getByRole('button', { name: '6h', exact: true }).click()]).then((r) => [r[0], r[1]] as const)
  await expect(mn.getByRole('button', { name: '6h', exact: true })).toHaveAttribute('aria-pressed', 'true')
  expect(six.status(), '6h read status').toBe(200)
  const sixBody = (await six.json()) as SeenMetrics
  scopeMatch(sixBody, 'range 6h')
  await metricsMatch(page, sixBody, 'range 6h')

  const drawn = METRIC_SERVICES.find((n) => panelSeries(first, 'req_per_min').some((s) => s.name === n))
  expect(drawn, 'Core AC 2 and 3: req_per_min draws a series for one of the PR\'s services; none means run-preview lacks roles/monitoring.viewer on ascomply-preview or the PR suffix matched nothing').toBeDefined()
  const name = drawn as string
  const select = mn.locator('select[aria-label="Service"]')
  const options = await select.locator('option').evaluateAll((els) => els.map((e) => (e as HTMLOptionElement).value))
  expect(options, 'the service select lists systemhealth.MetricServices after All services').toEqual(['', ...METRIC_SERVICES])
  const [, one] = await Promise.all([sent('6h', name), read('6h', name), select.selectOption(name)]).then((r) => [r[0], r[1]] as const)
  expect(one.status(), `${name} read status`).toBe(200)
  const oneBody = (await one.json()) as SeenMetrics
  await testInfo.attach('health-metrics-draw', { body: JSON.stringify({ fleet1h: drawSummary(first), narrowed: { service: name, panels: drawSummary(oneBody) } }, null, 2), contentType: 'application/json' })
  expect(oneBody.service, 'the answer names the selected service').toBe(name)
  scopeMatch(oneBody, `service ${name}`, name)
  expect(panelSeries(oneBody, 'req_per_min').map((s) => s.name), `service ${name} draws its own req_per_min series`).toEqual([name])
  await metricsMatch(page, oneBody, `service ${name}`)

  // Back to the default view: the fixture step clicks 6h and needs a changed range.
  await Promise.all([read('1h', name), mn.getByRole('button', { name: '1h', exact: true }).click()])
  await Promise.all([read('1h', null), select.selectOption('')])
  await expect(mn.getByRole('button', { name: '1h', exact: true })).toHaveAttribute('aria-pressed', 'true')
}

async function healthLinks(page: Page, seen: SeenHealth): Promise<void> {
  const mn = mainOf(page)
  const attrs = async (link: Locator, what: string, href: string): Promise<void> => {
    await expect(link, `${what} href`).toHaveAttribute('href', href)
    await expect(link, `${what} target`).toHaveAttribute('target', '_blank')
    await expect(link, `${what} rel`).toHaveAttribute('rel', 'noopener noreferrer')
  }
  for (const [label, href] of Object.entries(DASHBOARD_HREFS)) await attrs(mn.getByRole('link', { name: label }), label, href)
  if (seen.fleet.status === 'unknown') return
  expect(seen.fleet.services.length, 'services with links').toBeGreaterThan(0)
  for (const s of seen.fleet.services) {
    const row = mn.locator(`[data-service="${s.name}"]`)
    const link = row.getByRole('link')
    await attrs(link, `${s.name} Sentry link`, `https://as-comply.sentry.io/issues/?query=server_name:${s.name}&project=${SENTRY_PROJECT}&statsPeriod=14d`)
  }
}

// Polls until the locator matches what the latest health body implies; `want` is re-read on each try.
async function readsLatest(loc: Locator, want: () => string, what: string, mode: 'text' | 'count' = 'text'): Promise<void> {
  const got = async (): Promise<string> => (mode === 'count' ? String(await loc.count()) : ((await loc.textContent()) ?? ''))
  await expect.poll(async () => ((await got()) === want() ? 'match' : `${await got()} != ${want()}`), { message: what }).toBe('match')
}

const NO_JOBS_NOTE = (name: string): string => `${name} owns no background jobs. Pick submission, notifications, worker or All services to see them.`
const NO_PING_NOTE = (name: string): string => `${name} has no live ping. The live ping covers the backend services.`
const ROLLUP_DOWN = 'The gateway roll-up is unavailable.'

// Picks each service in turn: jobs tiles, Services rows and the All services tags follow the pick.
async function healthNarrowing(page: Page, latest: { body: SeenHealth }): Promise<void> {
  const mn = mainOf(page)
  const select = mn.locator('select[aria-label="Service"]')
  const tile = (key: string): Locator => mn.locator(`[data-tile="${key}"]`)
  const card = mn.locator('.ops-screen-pad > div:last-child')
  const jobsOwned = (name: string) => latest.body.river_jobs.services[name]
  // systemhealth.JobOwners and JobRunner: these three always serve totals
  for (const name of ['submission', 'notifications', 'worker']) expect(jobsOwned(name), `${name} totals are served`).toBeDefined()

  const pick = async (name: string): Promise<void> => {
    await select.selectOption(name)
    const at = name === '' ? 'All services' : name
    const totals = name === '' ? latest.body.river_jobs.total : jobsOwned(name)
    if (totals) {
      await readsLatest(tile('queue').locator('.money'), () => String((name === '' ? latest.body.river_jobs.total : jobsOwned(name)).queue_depth), `${at} queue value`)
      await readsLatest(tile('dead-lettered').locator('.money'), () => String((name === '' ? latest.body.river_jobs.total : jobsOwned(name)).dead_lettered), `${at} dead-lettered value`)
      await expect(mn.locator('[data-jobs-note]'), `${at} has no jobs note`).toHaveCount(0)
      await expect(mn.locator('.ops-health-jobs').getByText(/Loading/), `${at} jobs row is not loading`).toHaveCount(0)
    } else {
      await expect(mn.locator('[data-jobs-note]'), `${at} jobs note`).toHaveText(NO_JOBS_NOTE(name))
      await expect(mn.locator('[data-tile="queue"], [data-tile="dead-lettered"]'), `${at} has no jobs tiles`).toHaveCount(0)
    }
    await expect(card.getByText(/Loading/), `${at} Services card is not loading`).toHaveCount(0)

    // Rows follow the latest roll-up, re-read per try so a 60 s refresh cannot race them.
    const listed = (): string[] => {
      const r = latest.body.fleet
      return r.status === 'unknown' ? [] : r.services.map((s) => s.name).filter((n) => name === '' || n === name).sort()
    }
    const rows = mn.locator('[data-service]')
    await expect.poll(async () => JSON.stringify((await rows.evaluateAll((els) => els.map((e) => e.getAttribute('data-service')).sort()))), { message: `${at} Services row names` }).toBe(JSON.stringify(listed()))
    const known = latest.body.fleet.status !== 'unknown'
    // probedServices (auth, worker) are always in a known roll-up, so these picks cannot pass with zero rows
    if (known && (name === 'worker' || name === 'auth')) expect(listed(), `${at} is in the known roll-up`).toEqual([name])
    if (!known) {
      await expect(mn.getByText(ROLLUP_DOWN), `${at} roll-up unavailable copy`).toHaveCount(1)
      await expect(mn.locator('[data-ping-note]'), `${at} no ping note when the roll-up is unknown`).toHaveCount(0)
    } else if (listed().length === 0 && name !== '') {
      await expect(mn.locator('[data-ping-note]'), `${at} ping note`).toHaveText(NO_PING_NOTE(name))
    } else await expect(mn.locator('[data-ping-note]'), `${at} has no ping note`).toHaveCount(0)
    if (name === 'auth' && listed().length > 0) await expect(rows.getByRole('link'), 'auth row Sentry link').toHaveAttribute('aria-label', 'Open auth in Sentry')

    for (const key of SCOPED) await expect(tile(key).locator('[data-scope]'), `${key} tag at ${at}`).toHaveText('All services')
  }
  for (const name of ['submission', 'notifications', 'worker', 'auth', 'app', '']) await pick(name)
}

async function healthReads(page: Page, testInfo: TestInfo, armed?: unknown): Promise<void> {
  const mn = mainOf(page)
  const { health: seen, metrics, metricsUrl, latest } = await (armed as Promise<Armed>)
  const tile = (key: string): Locator => mn.locator(`[data-tile="${key}"]`)
  await expect(tile('fleet'), 'the health tiles have loaded').toBeVisible()

  const known = seen.fleet.status !== 'unknown'
  const up = seen.fleet.services.filter((s) => s.status === 'up').length
  await expect(tile('fleet').locator('.money')).toHaveText(known ? `${up}/${seen.fleet.services.length}` : '—')
  await readsLatest(tile('queue').locator('.money'), () => String(latest.body.river_jobs.total.queue_depth), 'queue value')
  await readsLatest(tile('dead-lettered').locator('.money'), () => String(latest.body.river_jobs.total.dead_lettered), 'dead-lettered value')
  const fleetWord = { ok: 'HEALTHY', degraded: 'DEGRADED', unknown: 'UNKNOWN' }[seen.fleet.status as 'ok' | 'degraded' | 'unknown']
  await expect(tile('fleet').getByText(fleetWord, { exact: true }), 'fleet status word').toHaveCount(1)
  await readsLatest(tile('dead-lettered').locator('[data-word]'), () => (latest.body.river_jobs.total.dead_lettered > 0 ? 'ATTENTION' : 'CLEAR'), 'dead-lettered status word')

  if (known) {
    expect(seen.fleet.services.length, 'the fleet read names services').toBeGreaterThan(0)
    for (const s of seen.fleet.services) {
      const row = mn.locator(`[data-service="${s.name}"]`)
      await expect(kid(row, 3), `${s.name} build`).toHaveText(shortOf(s.build))
      await expect(kid(row, 2), `${s.name} badge`).toHaveText(s.status === 'up' ? 'UP' : 'DOWN')
    }
  }
  await expect(mn.locator('[data-service]'), 'one row per service').toHaveCount(known ? seen.fleet.services.length : 0)

  const series: [string, 'services_down' | 'river_jobs'][] = [
    ['fleet', 'services_down'],
    ['queue', 'river_jobs'],
    ['dead-lettered', 'river_jobs'],
  ]
  for (const [key, field] of series) {
    if (field === 'river_jobs') await readsLatest(tile(key).locator('svg'), () => String(nonNull(latest.body.trend, field) >= 2 ? 1 : 0), `${key} sparkline count`, 'count')
    else await expect(tile(key).locator('svg'), `${key} sparkline`).toHaveCount(nonNull(seen.trend, field) >= 2 ? 1 : 0)
  }

  // Stable parts only: a failed gateway probe empties a build, and a read can be unknown with no services.
  const live = await liveStaffGet<SeenHealth>(STAFF_HEALTH)
  if (known && live.fleet.status !== 'unknown') {
    const names = (h: SeenHealth): string[] => h.fleet.services.map((s) => s.name).sort()
    expect(names(live).length, 'live fleet names services').toBeGreaterThan(0)
    expect(names(live), 'live fleet service names equal the screen read').toEqual(names(seen))
    const liveBuilds = new Map(live.fleet.services.map((s) => [s.name, s]))
    const both = seen.fleet.services.filter((s) => s.status === 'up' && s.build && liveBuilds.get(s.name)?.status === 'up' && liveBuilds.get(s.name)?.build)
    expect(both.length, 'services up with a build in both reads').toBeGreaterThan(0)
    for (const s of both) expect(liveBuilds.get(s.name)?.build, `${s.name} live build equals the screen read`).toBe(s.build)
  }
  expect(Object.keys(live.jobs).sort(), 'live job state keys').toEqual(Object.keys(seen.jobs).sort())
  expect(Object.keys(seen.jobs).sort(), 'job state keys are the systemhealth.JobStates').toEqual([...JOB_STATES].sort())

  await expect(mn.getByText(/APP latency|APP error rate|throughput|Recon backlog/i), 'no unmeasured tile').toHaveCount(0)
  await expectStyles(parent(mn.locator('[data-service]').first().getByText(/^(UP|DOWN)$/)), 'service badge', { radius: '4px' })

  await metricsReads(page, testInfo, metrics, metricsUrl)
  await healthNarrowing(page, latest)
  await healthLinks(page, seen)

  // Core AC 2: the fleet route grants the console no CORS.
  const fleet = await page.request.get(`${apiBase()}/healthz/fleet`, { headers: { ...fleetHeaders(), Origin: new URL(page.url()).origin } })
  expect([200, 503], '/healthz/fleet status').toContain(fleet.status())
  expect(Array.isArray((await fleet.json()).services), '/healthz/fleet carries services').toBe(true)
  expect(fleet.headers()['access-control-allow-origin'], '/healthz/fleet CORS header').toBeUndefined()

  await attachShot(page, testInfo, 'health')
}

const STAT_KEYS = ['fleet', 'req', 'errors', 'p95', 'connections']
const JOB_KEYS = ['queue', 'dead-lettered']
// Which tiles carry the All services tag (the fleet-wide ones).
const SCOPED = ['fleet', 'connections']
// Which tiles carry a status word: always, only NO DATA, or never.
const WORD_RULE: Record<string, 'always' | 'no-data' | 'never'> = { fleet: 'always', req: 'no-data', errors: 'always', p95: 'always', connections: 'always', queue: 'never', 'dead-lettered': 'always' }

// Which tiles carry a max line (the range tiles); the live-ping tiles never do.
const MAX_TILES = ['req', 'errors', 'p95', 'connections']
const MAX_TEXT: Record<string, RegExp> = {
  req: /^max (\d+(\.\d)?k?) req\/min$/,
  errors: /^max \d+(\.\d+)? %$/,
  p95: /^max (\d+ ms|\d+\.\d{2} s)$/,
  connections: new RegExp(`^max \\d+ of ${CONNECTION_BUDGET}$`),
}

// Reads one tile row by key: its tiles sit in order, share a top and stay apart; each tile holds its label, word, value and unit.
async function tileRowRead(page: Page, row: Locator, keys: string[], what: string): Promise<{ problems: string[]; rects: Rect[] }> {
  const tiles = row.locator(':scope > [data-tile]')
  expect(await tiles.evaluateAll((els) => els.map((e) => e.getAttribute('data-tile'))), `${what} tile keys`).toEqual(keys)
  const rects = await rectsOf(page, tiles, `${what} tile`)
  const problems = [...pairwiseApart(rects, `${what} tiles`), ...sameTops(rects, `${what} tiles`)]
  for (const [i, key] of keys.entries()) {
    const t = tiles.nth(i)
    const head = kid(t, 1)
    const word = t.locator('[data-word]')
    const tag = t.locator('[data-scope]')
    const hasWord = (await word.count()) > 0
    await expect(tag, `${key} tag`).toHaveCount(SCOPED.includes(key) ? 1 : 0)
    const rule = WORD_RULE[key]
    if (rule === 'always') expect(hasWord, `${key} has a status word`).toBe(true)
    if (rule === 'never') expect(hasWord, `${key} has no status word`).toBe(false)
    if (rule === 'no-data' && hasWord) await expect(word, `${key} word`).toHaveText('NO DATA')
    const valueRow = kid(t, 2)
    const r = await boxes(page, { tile: t, label: kid(head, 1), valueRow, value: kid(valueRow, 1), unit: kid(valueRow, 2) })
    problems.push(...within(r.tile, r.label, `${key} label`), ...within(r.tile, r.valueRow, `${key} value row`), ...within(r.tile, r.value, `${key} value`), ...within(r.tile, r.unit, `${key} unit`), ...apart(r.value, r.unit, `${key} value / unit`))
    if (hasWord) {
      const w = (await boxes(page, { word }))['word']
      problems.push(...within(r.tile, w, `${key} status word`), ...apart(r.label, w, `${key} label / status word`))
    }
    if (SCOPED.includes(key)) {
      await expect(tag, `${key} tag text`).toHaveText('All services')
      const g = (await boxes(page, { tag }))['tag']
      problems.push(...within(r.tile, g, `${key} tag`), ...apart(r.label, g, `${key} label / tag`))
      if (hasWord) problems.push(...apart(g, (await boxes(page, { word }))['word'], `${key} tag / status word`))
    }
    const worst = t.locator('[data-worst]')
    const hasWorst = (await worst.count()) > 0
    let worstBox: Rect | null = null
    if (hasWorst) {
      worstBox = (await boxes(page, { worst }))['worst']
      problems.push(...within(r.tile, worstBox, `${key} worst line`), ...below(r.valueRow, worstBox, `${key} worst line under the value row`))
      const clip = await worst.evaluate((el) => el.scrollWidth - el.clientWidth)
      if (clip > 0) problems.push(`${key} worst line: clipped by ${clip}px`)
    }
    const max = t.locator('[data-max]')
    const hasValue = (await t.locator('.money').innerText()) !== '—'
    await expect(max, `${key} max line`).toHaveCount(MAX_TILES.includes(key) && (hasValue || hasWorst) ? 1 : 0)
    const maxBox = (await max.count()) > 0 ? (await boxes(page, { max }))['max'] : null
    if (maxBox) {
      problems.push(...within(r.tile, maxBox, `${key} max line`), ...below(r.valueRow, maxBox, `${key} max line under the value row`), ...apart(maxBox, r.value, `${key} max line / value`), ...apart(maxBox, r.unit, `${key} max line / unit`))
      if (worstBox) problems.push(...below(maxBox, worstBox, `${key} worst line under the max line`))
      const clip = await max.evaluate((el) => el.scrollWidth - el.clientWidth)
      if (clip > 0) problems.push(`${key} max line: clipped by ${clip}px`)
    }
    const svg = t.locator('svg')
    if ((await svg.count()) > 0) {
      const v = (await boxes(page, { svg }))['svg']
      problems.push(...within(r.tile, v, `${key} sparkline`), ...below(r.valueRow, v, `${key} sparkline under the value row`))
      if (worstBox) problems.push(...below(worstBox, v, `${key} sparkline under the worst line`))
      if (maxBox) problems.push(...below(maxBox, v, `${key} sparkline under the max line`))
    }
  }
  return { problems, rects }
}

const PANEL_KEYS = ['req_per_min', 'error_pct', 'latency_p50_ms', 'latency_p95_ms', 'cpu_pct', 'ram_pct', 'instances', 'cold_starts_per_min', 'startup_latency_p95_ms', 'sql_util', 'sql_connections']

// The panels grid: panels apart, a row sharing one top, title row, word and content inside each panel; the grid between the Jobs row and the Services card.
async function panelsRead(page: Page, grid: Locator, jobs: Rect, card: Rect): Promise<{ problems: string[]; rects: Rect[] }> {
  const panels = grid.locator(':scope > [data-panel]')
  expect(await panels.evaluateAll((els) => els.map((e) => e.getAttribute('data-panel'))), 'panel keys').toEqual(PANEL_KEYS)
  const rects = await rectsOf(page, panels, 'panel')
  const gridBox = (await boxes(page, { grid })).grid
  const problems = [...pairwiseApart(rects, 'panels'), ...below(jobs, gridBox, 'panels grid below the Jobs row'), ...below(gridBox, card, 'Services card below the panels grid')]
  for (let i = 0; i + 1 < rects.length; i += 2) problems.push(...sameTops([rects[i], rects[i + 1]], `panel row ${i / 2}`))
  if (rects.length % 2 === 1) {
    const last = rects[rects.length - 1]
    problems.push(...sameColumn(rects[0], last, 'last panel aligns with the first column'), ...below(rects[rects.length - 2], last, 'last panel below the row above'))
  }
  for (const [i, key] of PANEL_KEYS.entries()) {
    const p = panels.nth(i)
    const titleRow = kid(p, 1)
    const word = p.locator('[data-word]')
    const noData = p.locator('[data-no-data]')
    const named: Record<string, Locator> = { titleRow, title: kid(titleRow, 1), content: kid(p, 2) }
    if ((await word.count()) > 0) named.word = word
    const r = await boxes(page, named)
    problems.push(...within(rects[i], r.titleRow, `${key} title row`), ...within(rects[i], r.title, `${key} title`), ...within(rects[i], r.content, `${key} chart`), ...below(r.titleRow, r.content, `${key} chart below the title row`))
    if (r.word) problems.push(...within(rects[i], r.word, `${key} word`), ...apart(r.title, r.word, `${key} title / word`))
    if ((await noData.count()) > 0) {
      await expect(noData, `${key} note`).toHaveText('No data')
      problems.push(...within(rects[i], (await boxes(page, { noData })).noData, `${key} No data`))
    }
  }
  return { problems, rects }
}

// fixture check: pins the chart, legend and marker layout of a wire-shaped body, not the metrics contract
async function fixturePanelsRead(page: Page): Promise<Read> {
  const panels = mainOf(page).locator('.ops-health-panels > [data-panel]')
  const rects = await rectsOf(page, panels, 'panel')
  const problems: string[] = []
  const legendCounts: Record<string, number> = { sql_util: 3, sql_connections: 1 }
  for (const [i, key] of PANEL_KEYS.entries()) {
    const p = panels.nth(i)
    const chart = kid(p, 2)
    const legend = kid(p, 3)
    const titleRow = kid(p, 1)
    const uplot = chart.locator('.uplot')
    await expect(chart, `${key} draws a chart`).toHaveAttribute('data-drawn', /.+/)
    await expect(uplot, `${key} uPlot root`).toHaveCount(1)
    const edges = async () => {
      const b = await boxes(page, { chart, uplot, titleRow })
      return Math.max(...[b.chart, b.uplot].flatMap((c) => [Math.abs(c.x - b.titleRow.x), Math.abs(c.x + c.width - (b.titleRow.x + b.titleRow.width))]))
    }
    await expect.poll(edges, { message: `${key} chart edges align with the title row` }).toBeLessThanOrEqual(1)
    const r = await boxes(page, { chart, legend, uplot })
    problems.push(...within(rects[i], r.chart, `${key} chart`), ...within(rects[i], r.uplot, `${key} uPlot`), ...below(r.chart, r.legend, `${key} legend below the chart`), ...within(rects[i], r.legend, `${key} legend`))

    const entries = legend.locator(':scope > *')
    const want = legendCounts[key] ?? 8
    await expect(entries, `${key} legend entries`).toHaveCount(want)
    const entryRects = await rectsOf(page, entries, `${key} legend entry`)
    problems.push(...pairwiseApart(entryRects, `${key} legend entries`))
    for (const [j, e] of entryRects.entries()) problems.push(...within(rects[i], e, `${key} legend entry ${j}`))
    if (want === 8) {
      await expect(legend.locator('[data-series]'), `${key} coloured entries`).toHaveCount(7)
      await expect(legend.locator('[data-series-other]'), `${key} folded entry`).toHaveText('+10 other services')
    }

    const over = chart.locator('.u-over')
    const marks = over.locator('[data-deploy]')
    await expect(marks, `${key} deploy markers`).toHaveCount(2)
    for (let j = 0; j < 2; j++) await expect(marks.nth(j), `${key} deploy marker ${j} title`).toHaveAttribute('title', /.+/)
    const markRects = await marks.evaluateAll((els) => els.map((e) => { const b = e.getBoundingClientRect(); return { x: b.x, y: b.y, width: b.width, height: b.height } }))
    // a marker is a 7 px target centred on its time, so one at an edge reaches 4 px past the plot
    const o = (await boxes(page, { over })).over
    const roomy = { x: o.x - 4, y: o.y, width: o.width + 8, height: o.height }
    for (const [j, m] of markRects.entries()) problems.push(...within(rects[i], m, `${key} deploy marker ${j}`), ...within(roomy, m, `${key} deploy marker ${j} in the plot`))
  }
  const word = (key: string): Locator => panels.nth(PANEL_KEYS.indexOf(key)).locator('[data-word]')
  await expect(word('error_pct'), 'error_pct word').toHaveText('HIGH')
  await expect(word('cpu_pct'), 'cpu_pct word').toHaveText('HIGH')
  await expect(word('latency_p95_ms'), 'latency_p95_ms word').toHaveText('PEAKED')
  await expect(word('ram_pct'), 'ram_pct has no word').toHaveCount(0)
  await expect(word('startup_latency_p95_ms'), 'startup_latency_p95_ms has no word').toHaveCount(0)
  return { problems, rects: { panels: rects } }
}

async function healthLayout(page: Page): Promise<Read> {
  const mn = mainOf(page)
  const problems: string[] = []

  const titleBlock = parent(mn.getByRole('heading', { level: 1, name: 'System health' }))
  const headRow = parent(titleBlock)
  const failed = mn.getByText('REFRESH FAILED', { exact: true })
  const headNamed: Record<string, Locator> = {
    headRow,
    titleBlock,
    stamp: headRow.getByText(/^CHECKED /),
    servicesLink: headRow.getByRole('link', { name: 'Services dashboard' }),
    databaseLink: headRow.getByRole('link', { name: 'Database and edge dashboard' }),
  }
  if ((await failed.count()) > 0) headNamed.failed = failed
  const head = await boxes(page, headNamed)
  const { headRow: headBox, ...headParts } = head
  for (const [k, r] of Object.entries(headParts)) problems.push(...within(headBox, r, `header ${k}`))
  problems.push(...pairwiseApart(Object.values(headParts), 'header title block, stamp and links'))

  const filterBar = parent(mn.getByRole('button', { name: '15m', exact: true }))
  const chips = filterBar.locator('button.ops-chip')
  const bar = await boxes(page, { filterBar, select: filterBar.locator('select') })
  const chipRects = await rectsOf(page, chips, 'range chip')
  problems.push(...below(head.headRow, bar.filterBar, 'filter bar below the header row'), ...within(bar.filterBar, bar.select, 'service select in the filter bar'))
  problems.push(...sameTops(chipRects, 'range chips'), ...pairwiseApart([...chipRects, bar.select], 'range chips and service select'))
  for (const [i, c] of chipRects.entries()) problems.push(...within(bar.filterBar, c, `range chip ${i} in the filter bar`))
  problems.push(...(await selectAtRightEnd(filterBar, bar.filterBar, bar.select, chipRects)))

  const statsRow = mn.locator('.ops-health-stats')
  const jobsRow = mn.locator('.ops-health-jobs')
  const stats = await tileRowRead(page, statsRow, STAT_KEYS, 'stat')
  const jobsNote = mn.locator('[data-jobs-note]')
  const noJobs = (await jobsNote.count()) > 0
  const jobs = noJobs ? { problems: [], rects: [] as Rect[] } : await tileRowRead(page, jobsRow, JOB_KEYS, 'jobs')
  const grids = await boxes(page, { stats: statsRow, jobs: noJobs ? jobsNote : jobsRow, card: mn.locator('.ops-screen-pad > div:last-child'), pad: mn.locator('.ops-screen-pad') })
  const panelsGrid = mn.locator('.ops-health-panels')
  const panelRead = (await panelsGrid.count()) > 0 ? await panelsRead(page, panelsGrid, grids.jobs, grids.card) : null
  if (noJobs) problems.push(...withinX(grids.pad, grids.jobs, 'jobs note in the content column'), ...below(grids.stats, grids.jobs, 'jobs note below the stat row'))
  problems.push(...(panelRead?.problems ?? []), ...stats.problems, ...jobs.problems, ...below(bar.filterBar, grids.stats, 'stat row below the filter bar'), ...below(grids.stats, grids.jobs, 'Jobs row below the stat row'), ...below(grids.jobs, grids.card, 'Services card below the Jobs row'))

  const rows = mn.locator('[data-service]')
  const nRows = Math.min(await rows.count(), 20)
  const pingNote = mn.locator('[data-ping-note]')
  if (nRows === 0 && (await pingNote.count()) > 0) {
    const n = (await boxes(page, { pingNote })).pingNote
    problems.push(...within(grids.card, n, 'no-ping note in the Services card'))
  } else if (nRows === 0) await expect(mn.getByText('The gateway roll-up is unavailable.'), 'no rows only when the roll-up is unavailable').toHaveCount(1)
  for (let i = 0; i < nRows; i++) {
    const row = rows.nth(i)
    const parts: Record<string, Locator> = { row, name: kid(row, 1), badge: kid(row, 2), build: kid(row, 3), sentry: kid(row, 4) }
    const p = await boxes(page, parts)
    const { row: rowBox, ...cells } = p
    problems.push(...within(grids.card, rowBox, `service row ${i} in the Services card`), ...pairwiseApart(Object.values(cells), `service ${i} name, badge, build and Sentry link`))
    for (const [k, r] of Object.entries(cells)) problems.push(...within(rowBox, r, `service ${i} ${k}`))
  }

  await noSidewaysScroll(page, 'System health')
  return { problems, rects: { head, bar, chipRects, stats: stats.rects, jobs: jobs.rects, panels: panelRead?.rects, grids } }
}

const STAFF_METRICS = /\/api\/submission\/v1\/staff\/metrics(\?|$)/
const FIXTURE_STEPS = 61
// frontend/support-console/src/metricsApi.ts METRIC_SERVICES
const METRIC_SERVICES_FIXTURE = ['app', 'auth', 'blog', 'dashboard', 'docling', 'gateway', 'invoice', 'landing', 'library', 'notifications', 'ops-console', 'portfolio', 'submission', 'support-console', 'tenancy', 'validation', 'worker']

// Wire-shaped body of systemhealth.Metrics: every Cloud Run panel full, 5xx over 1, p95 peaked then back under, CPU over 80, one gap, one deploy after the end.
function metricsFixture() {
  const end = Math.floor(Date.now() / 60_000) * 60
  const start = end - (FIXTURE_STEPS - 1) * 60
  const ts = Array.from({ length: FIXTURE_STEPS }, (_, i) => start + i * 60)
  const line = (f: (i: number) => number, gapAt?: number): [number, number][] => ts.flatMap((t, i): [number, number][] => (i === gapAt ? [] : [[t, f(i)]]))
  const names = METRIC_SERVICES_FIXTURE
  const per = (f: (i: number, n: number) => number) => names.map((name, n) => ({ name, points: line((i) => f(i, n), n === 1 ? 30 : undefined) }))
  const panel = (key: string, unit: string, series: { name: string; points: [number, number][] }[]) => ({ key, unit, available: true, series })
  const database = (f: (i: number) => number) => [{ name: 'database', points: line(f) }]
  return {
    range: '6h',
    service: '',
    start: new Date(start * 1000).toISOString(),
    end: new Date(end * 1000).toISOString(),
    step_seconds: 60,
    connection_budget: 80,
    panels: [
      panel('req_per_min', 'req/min', per((i, n) => 20 + n + (i % 7))),
      panel('error_pct', '%', per(() => 2)),
      panel('latency_p50_ms', 'ms', per(() => 120)),
      panel('latency_p95_ms', 'ms', per((i, n) => (i === 10 ? 1500 : n === 13 ? 640 : 400))), // n 13 is support-console, the latest worst
      panel('cpu_pct', '% of limit', per((i, n) => (n === 0 ? 85 : 30 + (i % 5)))),
      panel('ram_pct', '% of limit', per(() => 55)),
      panel('instances', 'count', per((i) => 1 + (i % 3))),
      panel('cold_starts_per_min', '/min', per((i) => (i % 9 === 0 ? 1 : 0))),
      panel('sql_cpu_pct', '%', database((i) => 20 + (i % 10))),
      panel('sql_ram_pct', '%', database(() => 60)),
      panel('sql_disk_pct', '%', database(() => 35)),
      panel('sql_connections', 'count', database(() => 23)),
      panel('latency_p95_fleet_ms', 'ms', [{ name: 'fleet', points: line(() => 180) }]), // FleetP95Key, FleetSeriesName
      panel('startup_latency_p95_ms', 'ms', per((i, n) => (n === 4 ? 3000 : 250 + (i % 5)))), // n 4 is docling, over 1000 ms: a wrongly judged word would show
    ],
    limits: { available: true, services: names.map((name) => ({ name, cpu: '1', memory: '512Mi', max_instances: 3 })) },
    deploys: {
      available: true,
      revisions: [
        { service: 'gateway', revision: 'gateway-00042', created_at: new Date((start + 1800) * 1000).toISOString() },
        { service: 'worker', revision: 'worker-00017', created_at: new Date((end + 120) * 1000).toISOString() },
      ],
    },
  }
}

// fixture check: the shared crosshair and the hover readout of the labelled metrics body
async function fixtureHover(page: Page): Promise<void> {
  const mn = mainOf(page)
  const panels = mn.locator('.ops-health-panels > [data-panel]')
  const chartOf = (key: string): Locator => kid(panels.nth(PANEL_KEYS.indexOf(key)), 2)
  const charts = mn.locator('[data-chart]')
  const readout = mn.locator('[data-readout]')
  const total = await charts.count()
  const overOf = async (key: string): Promise<Rect> => (await boxes(page, { over: chartOf(key).locator('.u-over') })).over
  const hoverAt = async (key: string, frac: number): Promise<void> => {
    const o = await overOf(key)
    await page.mouse.move(o.x + o.width * frac, o.y + o.height / 2)
  }
  const allCursors = async (): Promise<string[]> => charts.evaluateAll((els) => els.map((e) => e.getAttribute('data-cursor') ?? 'missing'))

  await page.mouse.move(1, 1)
  await hoverAt('req_per_min', 0.25)
  await expect.poll(async () => new Set(await allCursors()).size === 1 && (await allCursors())[0] !== '', { message: 'every panel shares one non-empty data-cursor' }).toBe(true)
  for (let i = 0; i < total; i++) await expect(charts.nth(i).locator('.u-cursor-x'), `panel ${i} crosshair shows`).not.toHaveClass(/u-off/)
  await expect(readout, 'only the hovered panel shows a readout').toHaveCount(1)
  await expect(chartOf('req_per_min').locator('[data-readout]')).toHaveCount(1)

  // crosshair colour: the resolved --fg-3 through a probe element
  const colours = await chartOf('req_per_min').evaluate((el) => {
    const probe = document.createElement('span')
    probe.style.color = 'var(--fg-3)'
    el.append(probe)
    const fg3 = getComputedStyle(probe).color
    probe.remove()
    return { fg3, line: getComputedStyle(el.querySelector('.u-cursor-x')!).borderRightColor }
  })
  expect(colours.line, 'crosshair uses --fg-3').toBe(colours.fg3)

  // step 30 of FIXTURE_STEPS (61) sits at the middle of the plot; auth (METRIC_SERVICES_FIXTURE[1]) has no point there (metricsFixture gapAt)
  await hoverAt('req_per_min', 0.5)
  const [min] = ((await chartOf('req_per_min').getAttribute('data-x')) ?? '').split(',').map(Number)
  const t = min + 30 * 60
  await expect.poll(allCursors, { message: 'the cursor reads step 30' }).toEqual(Array(total).fill(String(t)))
  await expect(readout.locator('[data-readout-time]'), 'readout time').toHaveText(`${new Date(t * 1000).toISOString().slice(11, 16)} UTC`)
  const coloured = await chartOf('req_per_min').locator('[data-series]').evaluateAll((els) => els.map((e) => e.getAttribute('data-series')))
  expect(coloured, 'seven coloured legend entries').toHaveLength(7)
  const want = [...coloured, ...METRIC_SERVICES_FIXTURE.filter((n) => !coloured.includes(n))]
  await expect.poll(() => readout.locator('[data-readout-row]').evaluateAll((els) => els.map((e) => e.getAttribute('data-readout-row'))), { message: 'readout rows: coloured in legend order, then grey' }).toEqual(want)
  await expect(readout.locator('[data-readout-row="auth"] .mono'), 'auth has no point at the gap').toHaveText('—')
  await hoverAt('error_pct', 0.5)
  const errRows = chartOf('error_pct').locator('[data-readout-row] .mono')
  await expect(errRows, 'error_pct rows').toHaveCount(METRIC_SERVICES_FIXTURE.length)
  expect(new Set(await errRows.allTextContents()), 'every error_pct row reads 2.00 %').toEqual(new Set(['2.00 %']))

  await atWidths(page, 'SUP-03 System health fixture readout layout', async () => {
    const problems: string[] = []
    for (const [key, frac] of [['req_per_min', 0.1], ['error_pct', 0.9]] as const) {
      await page.mouse.move(1, 1)
      await hoverAt(key, frac)
      const box = chartOf(key).locator('[data-readout]')
      await expect(box, `${key} readout`).toHaveCount(1)
      const r = (await boxes(page, { box })).box
      const x = (await boxes(page, { x: chartOf(key).locator('.u-cursor-x') })).x
      const view = page.viewportSize()!
      problems.push(...within({ x: 0, y: 0, width: view.width, height: view.height }, r, `${key} readout in the viewport`))
      if (r.x < x.x + x.width + 1 && x.x - 1 < r.x + r.width) problems.push(`${key} readout covers the crosshair`)
      const top = await box.evaluate((el) => {
        const b = el.getBoundingClientRect()
        const hit = document.elementFromPoint(b.x + b.width / 2, b.y + b.height / 2)
        return !!hit && el.contains(hit)
      })
      if (!top) problems.push(`${key} readout is not topmost at its centre`)
      const rows = await rectsOf(page, box.locator('[data-readout-row]'), `${key} readout row`)
      problems.push(...pairwiseApart(rows, `${key} readout rows`))
      for (const [i, row] of rows.entries()) problems.push(...within(r, row, `${key} readout row ${i}`))
      await noSidewaysScroll(page, `${key} hover`)
    }
    return { problems, rects: {} }
  })

  // leaving the panel area clears every crosshair and the readout
  const head = (await boxes(page, { h: mn.getByRole('heading', { level: 1, name: 'System health' }) })).h
  await page.mouse.move(head.x + head.width / 2, head.y + head.height / 2)
  await expect.poll(allCursors, { message: 'leaving empties data-cursor everywhere' }).toEqual(Array(total).fill(''))
  for (let i = 0; i < total; i++) await expect(charts.nth(i).locator('.u-cursor-x'), `panel ${i} crosshair hides`).toHaveClass(/u-off/)
  await expect(readout, 'the readout is gone').toHaveCount(0)
}

// fixture check: synced drag zoom, Reset zoom and the select colour on the labelled metrics body
async function fixtureZoom(page: Page): Promise<void> {
  const mn = mainOf(page)
  const panels = mn.locator('.ops-health-panels > [data-panel]')
  const charts = mn.locator('[data-chart]')
  const reqOver = kid(panels.nth(PANEL_KEYS.indexOf('req_per_min')), 2).locator('.u-over')
  const reset = mn.getByRole('button', { name: 'Reset zoom', exact: true })
  const windows = async (): Promise<string[]> => charts.evaluateAll((els) => els.map((e) => e.getAttribute('data-x') ?? 'missing'))
  const same = async (): Promise<string | null> => {
    const w = await windows()
    return w.length > 0 && new Set(w).size === 1 ? w[0] : null
  }
  const full = async (): Promise<[number, number]> => {
    await expect.poll(same, { message: 'every chart shares one window' }).not.toBeNull()
    return (await same())!.split(',').map(Number) as [number, number]
  }
  const drag = async (from: number, to: number, release = true): Promise<void> => {
    const o = (await boxes(page, { o: reqOver })).o
    const y = o.y + o.height / 2
    await page.mouse.move(o.x + o.width * from, y)
    await page.mouse.down()
    await page.mouse.move(o.x + o.width * ((from + to) / 2), y, { steps: 4 })
    await page.mouse.move(o.x + o.width * to, y, { steps: 4 })
    if (release) await page.mouse.up()
  }

  await expect(reset, 'no Reset zoom before a drag').toHaveCount(0)
  const [start, end] = await full()

  await drag(0.2, 0.6, false)
  const tint = await mn.evaluate((el) => {
    const probe = document.createElement('span')
    probe.style.backgroundColor = 'var(--action-tint)'
    el.append(probe)
    const want = getComputedStyle(probe).backgroundColor
    probe.remove()
    return { want, got: getComputedStyle(document.querySelector('.ops-health-panels .u-select')!).backgroundColor }
  })
  expect(tint.got, 'select box uses --action-tint').toBe(tint.want)
  await page.mouse.up()

  const zoomed = await full()
  expect(zoomed[0], 'zoom starts inside the range').toBeGreaterThan(start)
  expect(zoomed[1], 'zoom ends inside the range').toBeLessThan(end)
  await expect(reset).toHaveCount(1)
  await expect(mn.getByRole('button', { name: '6h', exact: true }), 'the range chip stays pressed while zoomed').toHaveAttribute('aria-pressed', 'true')

  await atWidths(page, 'SUP-03 System health fixture filter bar while zoomed', async () => {
    const problems: string[] = []
    const filterBar = parent(mn.getByRole('button', { name: '15m', exact: true }))
    const bar = await boxes(page, { filterBar, select: filterBar.locator('select'), reset })
    const chipRects = await rectsOf(page, filterBar.locator('button.ops-chip'), 'range chip')
    problems.push(...within(bar.filterBar, bar.reset, 'Reset zoom in the filter bar'), ...sameTops([...chipRects, bar.reset], 'chips and Reset zoom'))
    problems.push(...pairwiseApart([...chipRects, bar.reset, bar.select], 'chips, Reset zoom and select'))
    problems.push(...(await selectAtRightEnd(filterBar, bar.filterBar, bar.select, [...chipRects, bar.reset])))
    for (let i = 0; i < PANEL_KEYS.length; i++) {
      const marks = kid(panels.nth(i), 2).locator('[data-deploy]:visible')
      const titles = await marks.evaluateAll((els) => els.map((e) => e.getAttribute('title')))
      if (titles.length !== 1 || !titles[0]?.startsWith('gateway-00042 · ')) problems.push(`${PANEL_KEYS[i]} visible markers: ${titles.join(' | ')}`)
    }
    return { problems, rects: { bar: bar.filterBar, reset: bar.reset } }
  })

  await reset.click()
  await expect(reset).toHaveCount(0)
  await expect.poll(same, { message: 'Reset zoom restores the range' }).toBe(`${start},${end}`)

  await drag(0.2, 0.6)
  await expect(reset).toHaveCount(1)
  await mn.getByRole('button', { name: '1h', exact: true }).click()
  await expect(reset, 'a range chip clears the zoom').toHaveCount(0)
  await full()

  await drag(0.2, 0.6)
  await expect(reset).toHaveCount(1)
  await mn.locator('select[aria-label="Service"]').selectOption('app')
  await expect(reset, 'a service pick clears the zoom').toHaveCount(0)
  await full()
  await mn.locator('select[aria-label="Service"]').selectOption('')
  await mn.getByRole('button', { name: '6h', exact: true }).click()
  await full()
}

// fixture check: legend isolation, its readout and layout on the labelled metrics body
async function fixtureIsolate(page: Page): Promise<void> {
  const mn = mainOf(page)
  const panels = mn.locator('.ops-health-panels > [data-panel]')
  const chartOf = (key: string): Locator => kid(panels.nth(PANEL_KEYS.indexOf(key)), 2)
  const legendOf = (key: string): Locator => kid(panels.nth(PANEL_KEYS.indexOf(key)), 3)
  const cloud = PANEL_KEYS.filter((k) => !k.startsWith('sql_'))
  const drawn = async (key: string): Promise<string[]> => ((await chartOf(key).getAttribute('data-drawn')) ?? '').split(',').filter(Boolean)

  // worker: the highest req_per_min peak in metricsFixture (20 + n + i % 7, n 16), so colourMap gives it a slot in every Cloud Run panel
  const name = 'worker'
  await legendOf('req_per_min').locator(`[data-series="${name}"]`).click()
  for (const key of cloud) await expect.poll(() => drawn(key), { message: `${key} draws only ${name}` }).toEqual([name])
  await expect.poll(() => drawn('sql_util'), { message: 'sql_util keeps its lines' }).toEqual(['CPU', 'RAM', 'Disk'])
  await expect.poll(() => drawn('sql_connections'), { message: 'sql_connections keeps its line' }).toEqual(['connections'])
  await expect(legendOf('req_per_min').locator(`[data-series="${name}"]`)).toHaveAttribute('aria-pressed', 'true')
  await expect(legendOf('sql_util').locator('button'), 'SQL legend entries are not buttons').toHaveCount(0)
  const muted = await page.evaluate(() => {
    const probe = document.createElement('div')
    probe.style.background = 'var(--line-2)'
    document.body.appendChild(probe)
    const c = getComputedStyle(probe).backgroundColor
    probe.remove()
    return c
  })
  const others = legendOf('req_per_min').locator(`button[data-series]:not([data-series="${name}"])`)
  expect(await others.count(), 'other coloured entries stay').toBeGreaterThan(0)
  await expect(others.first()).toHaveAttribute('aria-pressed', 'false')
  await expect(kid(others.first(), 1), 'muted swatch').toHaveCSS('background-color', muted)
  await expect(kid(others.first(), 2), 'struck-through name').toHaveCSS('text-decoration-line', 'line-through')
  await expect(legendOf('req_per_min').locator('[data-series-other]'), 'folded entry struck through').toHaveCSS('text-decoration-line', 'line-through')
  await expect(legendOf('sql_util').locator('[data-series]').first(), 'SQL entry not muted').not.toHaveCSS('text-decoration-line', 'line-through')

  const o = (await boxes(page, { over: chartOf('req_per_min').locator('.u-over') })).over
  await page.mouse.move(1, 1)
  await page.mouse.move(o.x + o.width * 0.5, o.y + o.height / 2)
  const rows = chartOf('req_per_min').locator('[data-readout-row]')
  await expect.poll(() => rows.evaluateAll((els) => els.map((e) => e.getAttribute('data-readout-row'))), { message: 'readout lists only the isolated service' }).toEqual([name])
  await expect(chartOf('req_per_min').locator('[data-readout-time]')).toHaveCount(1)
  await page.mouse.move(1, 1)

  await atWidths(page, 'SUP-03 System health fixture legend layout, isolated', async () => fixturePanelsRead(page))

  await legendOf('req_per_min').locator(`[data-series="${name}"]`).click()
  for (const key of cloud) await expect.poll(async () => (await drawn(key)).length, { message: `${key} draws every series again` }).toBe(METRIC_SERVICES_FIXTURE.length)
}

// fixture check: pins layout of a wire-shaped body, not the metrics contract
async function healthFixtureLayout(page: Page): Promise<void> {
  const mn = mainOf(page)
  const cors = { 'access-control-allow-origin': new URL(page.url()).origin, 'access-control-allow-headers': 'authorization, content-type', 'access-control-allow-methods': 'GET, OPTIONS' }
  await page.route(STAFF_METRICS, (route) =>
    route.request().method() === 'OPTIONS' ? route.fulfill({ status: 204, headers: cors }) : route.fulfill({ status: 200, headers: cors, contentType: 'application/json', body: JSON.stringify(metricsFixture()) }),
  )
  const answered = page.waitForResponse((r) => STAFF_METRICS.test(new URL(r.url()).pathname + new URL(r.url()).search) && r.request().method() === 'GET')
  await mn.getByRole('button', { name: '6h', exact: true }).click()
  await answered
  const tile = (key: string): Locator => mn.locator(`[data-tile="${key}"]`)
  const word = (key: string): Locator => tile(key).locator('[data-word]')
  await expect(tile('req').locator('.money'), 'requests value').not.toHaveText('—')
  await expect(word('req'), 'requests carry no word').toHaveCount(0)
  await expect(word('errors')).toHaveText('HIGH')
  await expect(word('p95')).toHaveText('PEAKED')
  await expect(tile('p95').locator('.money'), 'p95 fleet value').toHaveText('180')
  await expect(tile('p95').locator('.money + span'), 'p95 unit').toHaveText('ms')
  await expect(tile('p95').locator('[data-worst]'), 'p95 worst line').toHaveText('worst support-console 640 ms')
  await expect(word('connections')).toHaveText('OK')
  // fixture arithmetic: req sums 17 series (476 + 17 * (i % 7)): last step 544, best 578; 5xx is 2 at every step; worst p95 peaks 1500 ms; connections is 23
  await expect(tile('req').locator('.money'), 'requests headline').toHaveText('544')
  await expect(tile('req').locator('[data-max]'), 'requests max').toHaveText('max 578 req/min')
  await expect(tile('errors').locator('[data-max]'), 'errors max').toHaveText('max 2.00 %')
  await expect(tile('p95').locator('[data-max]'), 'p95 max beside PEAKED').toHaveText('max 1.50 s')
  await expect(tile('connections').locator('[data-max]'), 'connections max').toHaveText('max 23 of 80')
  await expect(tile('fleet').locator('[data-max]'), 'fleet has no max line').toHaveCount(0)
  await expect(mn.locator('.ops-health-panels > [data-panel]'), 'the panels grid holds every panel').toHaveCount(PANEL_KEYS.length)
  await atWidths(page, 'SUP-03 System health fixture layout', async () => {
    const [base, charts] = [await healthLayout(page), await fixturePanelsRead(page)]
    return { problems: [...base.problems, ...charts.problems], rects: { base: base.rects, charts: charts.rects } }
  })
  await fixtureHover(page)
  await fixtureZoom(page)
  await fixtureIsolate(page)

  // app owns no job kind and has no live ping row: both notes show; the metrics route answers any query
  await mn.locator('select[aria-label="Service"]').selectOption('app')
  await expect(mn.locator('[data-tile="p95"] [data-worst]'), 'a picked service has no worst line').toHaveCount(0)
  await expect(mn.locator('[data-tile="p95"] [data-max]'), 'a picked service keeps the p95 max (fixture check)').toHaveText('max 1.50 s')
  await expect(mn.locator('[data-jobs-note]'), 'app jobs note').toHaveText('app owns no background jobs. Pick submission, notifications, worker or All services to see them.')
  await expect(mn.locator('[data-service]'), 'app has no service row').toHaveCount(0)
  const pinged = (await mn.locator('[data-ping-note]').count()) > 0
  if (!pinged) await expect(mn.getByText('The gateway roll-up is unavailable.'), 'app pass keeps the roll-up copy').toHaveCount(1)
  if (pinged) await expect(mn.locator('[data-ping-note]'), 'app ping note').toHaveText('app has no live ping. The live ping covers the backend services.')
  await expect(mn.locator('[data-tile="queue"], [data-tile="dead-lettered"]'), 'no jobs tiles for app').toHaveCount(0)
  await atWidths(page, 'SUP-03 System health fixture layout, app pick', async () => {
    const base = await healthLayout(page)
    return { problems: base.problems, rects: { base: base.rects } }
  })
  await mn.locator('select[aria-label="Service"]').selectOption('')
  await expect(mn.locator('[data-tile="queue"]'), 'All services restores the jobs tiles').toHaveCount(1)
}

const STAFF_AUDIT = '/api/invoice/v1/staff/audit-log'
const STAFF_CHANGES = '/api/invoice/v1/staff/staff-audit-log'
const staffTenantPath = (id: string): string => `/api/invoice/v1/staff/tenants/${id}`
// StaffRow, StaffChange: internal/audit/staff_read.go.
type LiveAuditRow = { id: string; tenant_id: string; tenant_name: string | null; event: string }
type LiveAuditPage = { events: LiveAuditRow[]; page: { has_more: boolean; next_cursor: string | null } }
type LiveChange = { id: string; actor: string; event: string; tenant_id: string | null; tenant_name: string | null; payload: { reason?: string } | null }

const auditRows = (page: Page): Locator => mainOf(page).locator('button[data-audit-id]')
// Cells of an audit row: time (1), tenant (2), actor (3), event (4), chevron (5).
const AUDIT_TRUNCATING_CELLS = [2, 3, 4]

// What the two view chips share with the Submissions state chips (chipStyle in Submissions.tsx).
const CHIP_PAINT = ['height', 'padding-left', 'padding-right', 'font-family', 'font-size', 'font-weight', 'letter-spacing', 'border-top-left-radius', 'border-top-width', 'border-top-color', 'background-color', 'color']

async function chipPaints(page: Page): Promise<{ on: Record<string, string>; off: Record<string, string> }> {
  const chips = mainOf(page).locator('button.ops-chip')
  const on = chips.and(page.locator('[aria-pressed="true"]')).first()
  const off = chips.and(page.locator('[aria-pressed="false"]')).first()
  await expect(on).toBeVisible()
  return { on: await styles(on, CHIP_PAINT), off: await styles(off, CHIP_PAINT) }
}

// Follows the cursor until every id is found: rows that land between the screen read and this one push them down the list.
async function liveAuditRows(ids: string[]): Promise<Map<string, LiveAuditRow>> {
  const found = new Map<string, LiveAuditRow>()
  let cursor: string | null = null
  for (let pages = 0; pages < 10 && found.size < ids.length; pages++) {
    const q = new URLSearchParams({ limit: '100' })
    if (cursor) q.set('cursor', cursor)
    const res: LiveAuditPage = await liveStaffGet<LiveAuditPage>(`${STAFF_AUDIT}?${q}`)
    for (const e of res.events) if (ids.includes(e.id)) found.set(e.id, e)
    if (!res.page.has_more) break
    cursor = res.page.next_cursor
  }
  return found
}

async function auditReads(page: Page, testInfo: TestInfo): Promise<void> {
  const mn = mainOf(page)
  const rows = auditRows(page)
  await expect(rows.first(), 'the audit entries have loaded').toBeVisible()

  // Matched by id, not count: the log grows with every spec.
  const first = (await rows.all()).slice(0, ROW_CAP)
  expect(first.length, 'audit rows on screen').toBeGreaterThan(0)
  const ids = await Promise.all(first.map((row) => row.getAttribute('data-audit-id') as Promise<string>))
  const live = await liveAuditRows(ids)
  for (const [i, row] of first.entries()) {
    const hit = live.get(ids[i])
    expect(hit, `row ${i} is in the live read`).toBeDefined()
    await expect(kid(row, 2), `row ${i} tenant`).toHaveText(hit!.tenant_name ?? hit!.tenant_id)
    await expect(kid(row, 4), `row ${i} event`).toHaveText(hit!.event)
  }
  expect(await truncationBad(rows, AUDIT_TRUNCATING_CELLS), 'every tenant, actor and event cell truncates').toEqual([])

  const form = mn.locator('form')
  for (const label of ['From', 'To']) {
    const input = form.getByLabel(label, { exact: true })
    await expect(input, `${label} is a date input`).toHaveAttribute('type', 'date')
  }
  await expect(form.locator('input[type="date"]'), 'the filter form holds two date inputs').toHaveCount(2)

  const audit = await chipPaints(page)
  await openScreen(page, 'Submissions', 'Submissions ops')
  const submissions = await chipPaints(page)
  expect(audit.on, 'the pressed view chip is painted as the pressed Submissions chip').toEqual(submissions.on)
  expect(audit.off, 'the idle view chip is painted as an idle Submissions chip').toEqual(submissions.off)
  await openScreen(page, 'Audit', 'Audit & evidence explorer')
  await expect(rows.first(), 'the audit entries reload').toBeVisible()
  await attachShot(page, testInfo, 'audit')
}

async function auditLayout(page: Page): Promise<Read> {
  const mn = mainOf(page)
  const titleBlock = parent(mn.getByRole('heading', { level: 1, name: 'Audit & evidence explorer' }))
  const headerRow = parent(titleBlock)
  const right = kid(headerRow, 2)
  const chips = mn.locator('button.ops-chip')
  const chipBar = parent(chips.first())
  const form = mn.locator('form')
  const card = parent(auditRows(page).first())
  const top = await boxes(page, {
    headerRow,
    titleBlock,
    right,
    pill: kid(right, 1),
    evidence: mn.getByRole('button', { name: 'Evidence bundle' }),
    chipBar,
    form,
    card,
    head: kid(card, 1),
  })
  const problems = [
    ...apart(top.titleBlock, top.right, 'title block / right group'),
    ...within(top.headerRow, top.titleBlock, 'title block in the header row'),
    ...within(top.headerRow, top.pill, 'pill in the header row'),
    ...within(top.headerRow, top.evidence, 'Evidence bundle in the header row'),
    ...apart(top.pill, top.evidence, 'pill / Evidence bundle'),
    ...sameCentreY(top.pill, top.evidence, 'pill / Evidence bundle'),
    ...below(top.headerRow, top.chipBar, 'view chips below the header row'),
    ...below(top.chipBar, top.form, 'filter form below the view chips'),
    ...below(top.form, top.card, 'card below the filter form'),
  ]

  const chipRects = await rectsOf(page, chips, 'view chip')
  problems.push(...sameTops(chipRects, 'view chips'), ...pairwiseApart(chipRects, 'view chips'))
  for (const [i, c] of chipRects.entries()) problems.push(...below(top.headerRow, c, `view chip ${i}`))

  const controls = await Promise.all([1, 2, 3, 4, 5].map(async (n) => (await boxes(page, { c: kid(form, n) })).c))
  problems.push(...pairwiseApart(controls, 'filter controls'))
  for (const [i, c] of controls.entries()) problems.push(...within(top.form, c, `filter control ${i + 1} in the form`))

  const headCells = await childRects(kid(card, 1))
  const rows = (await auditRows(page).all()).slice(0, ROW_CAP)
  expect(rows.length, 'audit rows').toBeGreaterThan(0)
  for (const [i, row] of rows.entries()) {
    const box = await row.boundingBox()
    if (!box) throw new Error(`audit row ${i} has no box`)
    const cells = await childRects(row)
    problems.push(...within(top.card, box, `row ${i} in the card`), ...pairwiseApart(cells, `row ${i} cells`))
    for (const [n, c] of cells.entries()) {
      problems.push(...within(box, c, `row ${i} cell ${n + 1} in its row`))
      if (!near(c.x, headCells[n].x, 1)) problems.push(`row ${i} cell ${n + 1}: left ${c.x} vs header cell ${headCells[n].x}`)
    }
  }

  const more = mn.getByRole('button', { name: 'Load more' })
  if ((await more.count()) > 0) problems.push(...below(top.card, (await boxes(page, { more })).more, 'Load more below the card'))

  await mn.getByRole('button', { name: 'STAFF CHANGES' }).click()
  const changeRows = mn.locator('button[data-change-id]')
  await expect(changeRows.first(), 'the staff changes have loaded').toBeVisible()
  const staffHead = await childRects(kid(parent(changeRows.first()), 1))
  for (const [i, row] of (await changeRows.all()).slice(0, ROW_CAP).entries()) {
    const cells = await childRects(row)
    problems.push(...pairwiseApart(cells, `staff row ${i} cells`))
    for (const [n, c] of cells.entries()) {
      if (!near(c.x, staffHead[n].x, 1)) problems.push(`staff row ${i} cell ${n + 1}: left ${c.x} vs header cell ${staffHead[n].x}`)
    }
  }
  await mn.getByRole('button', { name: 'TENANT LOG' }).click()

  await noSidewaysScroll(page, 'Audit')
  return { problems, rects: { top, chipRects, controls, headCells, staffHead } }
}

const STAFF_AI_CALLS = '/api/invoice/v1/staff/ai-calls'
const STAFF_AI_TOTALS = `${STAFF_AI_CALLS}/totals`
type LiveAiCall = { id: string; tenant_name: string; client: string; purpose: string; outcome: string }
type LiveAiTotal = { day: string; tenant_id: string; calls: number; cost: string; failed: number }
type LiveAiPage<K extends string, R> = { page: { has_more: boolean; next_cursor: string | null } } & Record<K, R[]>

const aiCallRows = (page: Page): Locator => mainOf(page).locator('div[data-call-id]')
const aiTotalRows = (page: Page): Locator => mainOf(page).locator('div[data-total-key]')
// Cells of a call row: time 1, tenant 2, client 3, purpose 4, model 5, in 6, out 7, cost 8, latency 9, try 10, outcome 11.
const AI_TRUNCATING_CELLS = [2, 4, 5, 11]
// Cells of a totals row: day 1, tenant 2, calls 3, tokens in 4, tokens out 5, cost 6, failed 7.
// Every button the screen may hold: the three view chips, Load more and a failed read's Retry.
const AI_BUTTONS = ['CALLS', 'FAILURES', 'DAILY TOTALS', 'Load more', 'Retry']

// Follows the cursor until every key is found: rows that land between the screen read and this one push them down the list.
async function liveAiRows<K extends string, R>(path: string, key: K, keyOf: (r: R) => string, keys: string[], extra = ''): Promise<Map<string, R>> {
  const found = new Map<string, R>()
  let cursor: string | null = null
  for (let pages = 0; pages < 10 && found.size < keys.length; pages++) {
    const q = new URLSearchParams(extra)
    q.set('limit', '100')
    if (cursor) q.set('cursor', cursor)
    const res: LiveAiPage<K, R> = await liveStaffGet<LiveAiPage<K, R>>(`${path}?${q}`)
    for (const r of res[key]) if (keys.includes(keyOf(r))) found.set(keyOf(r), r)
    if (!res.page.has_more) break
    cursor = res.page.next_cursor
  }
  return found
}

// The screen is read-only: no switch or input beyond the five filters, and no button outside the allowlist.
async function aiControlsRead(page: Page, what: string): Promise<void> {
  const mn = mainOf(page)
  await expect(mn.locator('[role="switch"], input[type="checkbox"], input[type="range"], input[type="number"]'), `${what}: no switch, checkbox, range or number input`).toHaveCount(0)
  // The screen's own content: the TopBar's Sandbox/Live switch is console-wide, not an AI control.
  const names = (await mn.locator('.ops-screen-pad').getByRole('button').allTextContents()).map((t) => t.trim())
  expect(names, `${what}: the view chips are in scope`).toEqual(expect.arrayContaining(['CALLS', 'FAILURES', 'DAILY TOTALS']))
  expect(names.filter((n) => !AI_BUTTONS.includes(n)), `${what}: buttons outside the allowlist`).toEqual([])
}

async function aiUsageReads(page: Page, testInfo: TestInfo): Promise<void> {
  const mn = mainOf(page)
  const calls = aiCallRows(page)
  await expect(calls.first(), 'the AI calls have loaded').toBeVisible()

  // Matched by id, not count: the table grows with every spec.
  const first = (await calls.all()).slice(0, ROW_CAP)
  const ids = await Promise.all(first.map((row) => row.getAttribute('data-call-id') as Promise<string>))
  const live = await liveAiRows<'calls', LiveAiCall>(STAFF_AI_CALLS, 'calls', (c) => c.id, ids)
  for (const [i, row] of first.entries()) {
    const hit = live.get(ids[i])
    expect(hit, `row ${i} is in the live read`).toBeDefined()
    await expect(kid(row, 2), `row ${i} tenant`).toHaveText(hit!.tenant_name)
    await expect(kid(row, 3), `row ${i} client`).toHaveText(hit!.client)
    await expect(kid(row, 4), `row ${i} purpose`).toHaveText(hit!.purpose)
    await expect(kid(row, 11), `row ${i} outcome`).toHaveText(hit!.outcome)
  }
  expect(await truncationBad(calls, AI_TRUNCATING_CELLS), 'every tenant, purpose, model and outcome cell truncates').toEqual([])
  await aiControlsRead(page, 'CALLS')

  await mn.getByRole('button', { name: 'FAILURES' }).click()
  await expect(mn.getByRole('button', { name: 'FAILURES' })).toHaveAttribute('aria-pressed', 'true')
  await expect(calls.first(), 'the failed calls have loaded').toBeVisible()
  const failedRows = (await calls.all()).slice(0, ROW_CAP)
  const failedIds = await Promise.all(failedRows.map((row) => row.getAttribute('data-call-id') as Promise<string>))
  const liveFailed = await liveAiRows<'calls', LiveAiCall>(STAFF_AI_CALLS, 'calls', (c) => c.id, failedIds, 'failed=true')
  for (const [i, row] of failedRows.entries()) {
    await expect(kid(row, 11), `failure ${i} outcome is not ok`).not.toHaveText('ok')
    expect(liveFailed.has(failedIds[i]), `failure ${i} is in a live failed=true read`).toBe(true)
  }
  await aiControlsRead(page, 'FAILURES')

  await mn.getByRole('button', { name: 'DAILY TOTALS' }).click()
  const totals = aiTotalRows(page)
  await expect(totals.first(), 'the daily totals have loaded').toBeVisible()
  const firstTotals = (await totals.all()).slice(0, ROW_CAP)
  const keys = await Promise.all(firstTotals.map((row) => row.getAttribute('data-total-key') as Promise<string>))
  const liveTotals = await liveAiRows<'totals', LiveAiTotal>(STAFF_AI_TOTALS, 'totals', (t) => `${t.day}|${t.tenant_id}`, keys)
  for (const [i, row] of firstTotals.entries()) {
    const hit = liveTotals.get(keys[i])
    expect(hit, `total ${i} is in the live read`).toBeDefined()
    await expect(kid(row, 3), `total ${i} calls`).toHaveText(hit!.calls.toLocaleString('en-US'))
    await expect(kid(row, 6), `total ${i} cost`).toHaveText(`$${hit!.cost}`)
    await expect(kid(row, 7), `total ${i} failed`).toHaveText(hit!.failed.toLocaleString('en-US'))
  }
  await aiControlsRead(page, 'DAILY TOTALS')

  await mn.getByRole('button', { name: 'CALLS' }).click()
  await expect(calls.first()).toBeVisible()
  const ai = await chipPaints(page)
  await openScreen(page, 'Submissions', 'Submissions ops')
  const submissions = await chipPaints(page)
  expect(ai.on, 'the pressed view chip is painted as the pressed Submissions chip').toEqual(submissions.on)
  expect(ai.off, 'the idle view chip is painted as an idle Submissions chip').toEqual(submissions.off)
  await openScreen(page, 'AI usage', 'AI usage')
  await mn.getByRole('button', { name: 'CALLS' }).click()
  await expect(calls.first(), 'the AI calls reload').toBeVisible()
  await attachShot(page, testInfo, 'ai-usage')
}

// Cells whose text must sit inside the cell: the header cells, and a row's cells at the fixed-width tracks.
async function textOverflows(cells: Locator, only: number[] | undefined, what: string): Promise<string[]> {
  const found = await cells.evaluateAll(
    (els, keep) =>
      els.flatMap((el, n) => {
        if (keep && !keep.includes(n)) return []
        const cs = getComputedStyle(el)
        const box = el.getBoundingClientRect()
        const range = document.createRange()
        range.selectNodeContents(el)
        const t = range.getBoundingClientRect()
        const inner = { left: box.left + parseFloat(cs.paddingLeft), right: box.right - parseFloat(cs.paddingRight) }
        const out = el.scrollWidth > el.clientWidth + 1 || t.left < inner.left - 0.5 || t.right > inner.right + 0.5
        return out ? [`${n + 1} "${el.textContent}": text ${t.left.toFixed(1)}..${t.right.toFixed(1)} vs cell ${inner.left.toFixed(1)}..${inner.right.toFixed(1)}`] : []
      }),
    only,
  )
  return found.map((f) => `${what}: cell ${f} overflows`)
}

// The header, chips, form and one card with its rows, in one settled read.
async function aiCardRead(page: Page, rows: Locator, what: string, fixed: number[]): Promise<{ problems: string[]; rects: Record<string, unknown> }> {
  const mn = mainOf(page)
  const chips = mn.locator('button.ops-chip')
  const form = mn.locator('form')
  const card = parent(rows.first())
  const top = await boxes(page, {
    titleBlock: parent(mn.getByRole('heading', { level: 1, name: 'AI usage' })),
    chipBar: parent(chips.first()),
    form,
    card,
  })
  const problems = [...below(top.titleBlock, top.chipBar, `${what}: chips below the header block`), ...below(top.chipBar, top.form, `${what}: form below the chips`), ...below(top.form, top.card, `${what}: card below the form`)]

  const chipRects = await rectsOf(page, chips, 'view chip')
  problems.push(...sameTops(chipRects, `${what}: view chips`), ...pairwiseApart(chipRects, `${what}: view chips`))

  const controls = await Promise.all([1, 2, 3, 4, 5].map(async (n) => (await boxes(page, { c: kid(form, n) })).c))
  problems.push(...pairwiseApart(controls, `${what}: filter controls`))
  for (const [i, c] of controls.entries()) problems.push(...within(top.form, c, `${what}: filter control ${i + 1} in the form`))

  const fit = await card.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
  if (fit.scrollWidth > fit.clientWidth + 1) problems.push(`${what}: the card scrolls sideways (${fit.scrollWidth} vs ${fit.clientWidth})`)

  const headCells = await childRects(kid(card, 1))
  const shown = (await rows.all()).slice(0, ROW_CAP)
  for (const [i, row] of shown.entries()) {
    const box = await row.boundingBox()
    if (!box) throw new Error(`${what} row ${i} has no box`)
    const cells = await childRects(row)
    problems.push(...within(top.card, box, `${what}: row ${i} in the card`), ...pairwiseApart(cells, `${what}: row ${i} cells`))
    for (const [n, c] of cells.entries()) {
      problems.push(...within(box, c, `${what}: row ${i} cell ${n + 1} in its row`))
      if (!near(c.x, headCells[n].x, 1)) problems.push(`${what}: row ${i} cell ${n + 1}: left ${c.x} vs header cell ${headCells[n].x}`)
    }
  }

  const headSpans = kid(card, 1).locator('> *')
  expect(await headSpans.count(), `${what}: header cells`).toBeGreaterThan(0)
  expect(await rows.first().locator('> *').count(), `${what}: first row cells`).toBeGreaterThan(0)
  problems.push(...(await textOverflows(headSpans, undefined, `${what} header`)), ...(await textOverflows(rows.first().locator('> *'), fixed, `${what} row 0`)))

  const more = mn.getByRole('button', { name: 'Load more' })
  if ((await more.count()) > 0) problems.push(...below(top.card, (await boxes(page, { more })).more, `${what}: Load more below the card`))
  return { problems, rects: { top, chipRects, controls, headCells } }
}

async function aiUsageLayout(page: Page): Promise<Read> {
  const mn = mainOf(page)
  // A poll retry starts from whichever view the last read left.
  await mn.getByRole('button', { name: 'CALLS' }).click()
  await expect(aiCallRows(page).first()).toBeVisible()
  const calls = await aiCardRead(page, aiCallRows(page), 'calls', [0, 2, 5, 6, 7, 8, 9])

  await mn.getByRole('button', { name: 'DAILY TOTALS' }).click()
  await expect(aiTotalRows(page).first()).toBeVisible()
  const totals = await aiCardRead(page, aiTotalRows(page), 'totals', [0, 2, 3, 4, 5, 6])
  await mn.getByRole('button', { name: 'CALLS' }).click()

  await noSidewaysScroll(page, 'AI usage')
  return { problems: [...calls.problems, ...totals.problems], rects: { calls: calls.rects, totals: totals.rects } }
}

const SCREENS: Screen[] = [
  {
    nav: 'Submissions',
    h1: 'Submissions ops',
    rowUp: 1,
    reads: submissionsReads,
    layout: submissionsLayout,
    after: async (page) => {
      const select = mainOf(page).locator('select[aria-label="Tenant"]')
      const value = await select.locator('option:not([value=""])').first().getAttribute('value')
      expect(value, 'a real tenant option').toBeTruthy()
      await select.selectOption(value!)
      await atWidths(page, 'SUP-03 Submissions Sentry link layout', () => submissionsLinkLayout(page))
    },
  },
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
    nav: 'Audit',
    h1: 'Audit & evidence explorer',
    rowUp: 2,
    reads: auditReads,
    layout: auditLayout,
  },
  {
    nav: 'Tenants',
    h1: 'Tenants',
    rowUp: 1,
    reads: tenantsReads,
    layout: tenantsLayout,
    after: viewJobsLands,
  },
  {
    nav: 'System health',
    h1: 'System health',
    rowUp: 2,
    arm: armHealth,
    reads: healthReads,
    layout: healthLayout,
    after: healthFixtureLayout,
  },
  {
    nav: 'AI usage',
    h1: 'AI usage',
    rowUp: 1,
    reads: aiUsageReads,
    layout: aiUsageLayout,
  },
]

for (const s of SCREENS) {
  test(`SUP-03 ${s.nav}: v2 header, eyebrow clearances and screen reads`, async ({ page }, testInfo) => {
    test.setTimeout(120_000)
    const errors = await startSupport(page)
    const armed = s.arm?.(page)
    armed?.catch(() => {}) // a timeout surfaces where `reads` awaits it, not as an unhandled rejection
    await openScreen(page, s.nav, s.h1)
    await settle(page, mainOf(page))

    await expectHeader(page, s.nav, s.h1, s.rowUp)
    await s.reads(page, testInfo, armed)

    await atWidths(page, `SUP-03 ${s.nav} title block`, () => titleBlockRead(page, s.h1))
    await atWidths(page, `SUP-03 ${s.nav} layout`, () => s.layout(page))
    await s.after?.(page)

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

// SUP-07: acts on seeded tenant-2222 dead-letter rows (D22) and cancels back to a terminal state in the same spec.
const SEEDED_DEAD = ['DEMO-2026-7004', 'DEMO-2026-7006']
// The reason of this run's tenant-2222 cancel, for the Audit test below; unset after a worker restart.
const cancel2222: { reason?: string } = {}
// audit.SupportActor, internal/audit/audit.go.
const SUPPORT_ACTOR = 'ASComply support'

async function cancelWhenQueued(token: string, id: string, reason: string): Promise<{ status: number; body: unknown }> {
  let res: { status: number; body: unknown } = { status: 0, body: undefined }
  await expect
    .poll(
      async () => {
        res = await rawFetch(`${STAFF_JOBS}/${id}/cancel`, { method: 'POST', headers: { Authorization: `Bearer ${token}` }, body: { reason } })
        return res.status
      },
      { message: 'cancel answers something other than 409', timeout: 60_000, intervals: [500, 1000, 2000] },
    )
    .not.toBe(409)
  return res
}

// The tenant's member-visible audit row that carries `reason`.
async function auditRowFor(event: string, reason: string): Promise<AuditEvent> {
  const token = await login(PERSONAS.B)
  let row: AuditEvent | undefined
  await expect
    .poll(
      async () => {
        row = (await getAuditLog(token, { event: [event] })).events.find((e) => (e.payload as { reason?: string } | null)?.reason === reason)
        return row !== undefined
      },
      { message: `the tenant audit log holds ${event} with the reason`, timeout: 15_000 },
    )
    .toBe(true)
  return row!
}

test('SUP-07 Submissions: re-drive a seeded dead-letter job, then cancel it', async ({ page }, testInfo) => {
  test.setTimeout(180_000)
  const errors = await startSupport(page)
  const mn = mainOf(page)
  const api = await staffToken()
  const redriveReason = `e2e re-drive ${randomUUID()}`
  const cancelReason = `e2e cancel ${randomUUID()}`
  let jobId: string | null = null
  let cancelled = false
  let redriveSent = false

  try {
    await openScreen(page, 'Submissions', 'Submissions ops')
    await mn.getByRole('button', { name: 'DEAD-LETTER', exact: true }).click()
    const listed = page.waitForResponse((r) => r.url().includes('/staff/jobs?') && r.url().includes('state=dead_lettered') && r.url().includes(`tenant=${DEMO_2222_ID}`))
    await mn.locator('select[aria-label="Tenant"]').selectOption(DEMO_2222_ID)
    await listed
    await expect(mn.getByRole('button', { name: 'DEAD-LETTER', exact: true })).toHaveAttribute('aria-pressed', 'true')

    const seededRow = (n: string): Locator => jobRows(page).filter({ hasText: n })
    await expect.poll(async () => (await seededRow(SEEDED_DEAD[0]).count()) + (await seededRow(SEEDED_DEAD[1]).count()), { message: 'a seeded dead-letter job is listed' }).toBeGreaterThan(0)
    const number = (await seededRow(SEEDED_DEAD[0]).count()) > 0 ? SEEDED_DEAD[0] : SEEDED_DEAD[1]
    const row = seededRow(number)
    jobId = await row.getAttribute('data-job-id')
    expect(jobId, 'the row names its job').toBeTruthy()
    await row.click()

    const { drawer } = await expectDrawer(page, 'job drawer')
    const redrive = drawer.getByRole('button', { name: 'Re-drive', exact: true })
    await expect(redrive).toBeVisible()
    await atWidths(page, 'SUP-07 job drawer layout', async () => {
      const meta = kid(kid(drawer, 2), 1)
      const footer = kid(drawer, 3)
      const r = await boxes(page, { drawer, meta, footer, redrive })
      const cells = await childRects(meta)
      return {
        problems: [...within(r.footer, r.redrive, 'Re-drive in the drawer footer'), ...within(r.drawer, r.footer, 'footer in the drawer'), ...pairwiseApart(cells, 'meta cell'), ...cells.flatMap((c, i) => within(r.meta, c, `meta cell ${i} in the grid`))],
        rects: r,
      }
    })
    await attachShot(page, testInfo, 'job-drawer')

    await redrive.click()
    const dialog = page.getByRole('dialog').filter({ hasText: `Re-drive ${number}?` })
    await expectPanel(page, dialog, 're-drive confirm')
    const confirm = dialog.getByRole('button', { name: 'Re-drive job' })
    await expect(confirm, 'Re-drive job waits for a reason').toBeDisabled()
    const reasonBox = dialog.getByRole('textbox', { name: 'Reason' })
    await atWidths(page, 'SUP-07 re-drive confirm layout', async () => {
      const vp = page.viewportSize()!
      const r = await boxes(page, { dialog, footer: kid(dialog, 2), reason: reasonBox, back: dialog.getByRole('button', { name: 'Back' }), confirm })
      return {
        problems: [
          ...within({ x: 0, y: 0, width: vp.width, height: vp.height }, r.dialog, 'dialog in the viewport'),
          ...within(r.dialog, r.reason, 'Reason field in the dialog'),
          ...within(r.footer, r.back, 'Back in the footer'),
          ...within(r.footer, r.confirm, 'Re-drive job in the footer'),
          ...apart(r.back, r.confirm, 'Back / Re-drive job'),
          ...apart(r.reason, r.footer, 'Reason / footer'),
        ],
        rects: r,
      }
    })

    await reasonBox.fill(redriveReason)
    redriveSent = true
    await confirm.click()
    await expect(page.getByRole('status').filter({ hasText: 'Job re-driven' }), 'the re-drive toast').toBeVisible()

    await expect
      .poll(async () => (await liveStaffGet<{ job: { state: string } }>(`${STAFF_JOBS}/${jobId}`)).job.state, { message: 'a fresh staff read no longer shows dead_lettered' })
      .not.toBe('dead_lettered')

    const redriven = await auditRowFor('submission.job.redriven', redriveReason)
    expect(redriven.actor, 'the customer sees the support actor').toBe(SUPPORT_ACTOR)

    const res = await cancelWhenQueued(api, jobId!, cancelReason)
    expect(res.status, `cancel answered ${JSON.stringify(res.body)}`).toBe(200)
    cancelled = true
    cancel2222.reason = cancelReason
    const done = (res.body as { job: { state: string; invoice_status: string } }).job
    expect(done.state).toBe('cancelled')
    expect(done.invoice_status).toBe('failed')
    const fresh = (await liveStaffGet<{ job: { state: string; invoice_status: string } }>(`${STAFF_JOBS}/${jobId}`)).job
    expect([fresh.state, fresh.invoice_status], 'a fresh read agrees').toEqual(['cancelled', 'failed'])

    const cancelRow = await auditRowFor('submission.job.cancelled', cancelReason)
    expect(cancelRow.actor, 'the customer sees the support actor').toBe(SUPPORT_ACTOR)
  } finally {
    // Leave no `queued` row for later serial-lane specs; before the re-drive a cancel would 409 for the whole window.
    if (jobId && redriveSent && !cancelled) await cancelWhenQueued(api, jobId, cancelReason).catch(() => undefined)
  }

  noErrors(errors, 'SUP-07 Submissions')
})

const metaValue = (drawer: Locator, label: string): Locator => drawer.locator('div.label').filter({ hasText: new RegExp(`^${label}$`) }).locator('xpath=following-sibling::div[1]')
const viewport = (page: Page): Rect => ({ x: 0, y: 0, ...page.viewportSize()! })

// Honeywell Group, the one seeded tenant-2222 company with invoices (db/seed.dev.sql); a spec-made company would sort anywhere.
const SEED_2222_TIN = '20665510-0001'

// Mirrors dayStart and dayEnd in frontend/support-console/src/auditApi.ts.
const BUNDLE_FROM = '2020-01-01'
const BUNDLE_TO = '2035-12-31'

type LivePreview = { entity: { id: string; name: string; tin: string | null }; filename: string; counts: Record<'invoices' | 'line_items' | 'status_transitions' | 'submissions' | 'exchange_attempts' | 'body_files', number> }
const PREVIEW_CELLS: [string, keyof LivePreview['counts']][] = [
  ['Invoices', 'invoices'],
  ['Line items', 'line_items'],
  ['Status changes', 'status_transitions'],
  ['Submissions', 'submissions'],
  ['Exchange attempts', 'exchange_attempts'],
  ['Body files', 'body_files'],
]

test('SUP-09 Audit: entry drawer, staff changes, the tenant link and an evidence download', async ({ page }, testInfo) => {
  test.setTimeout(240_000)
  const errors = await startSupport(page)
  const mn = mainOf(page)

  await openScreen(page, 'Audit', 'Audit & evidence explorer')
  const rows = auditRows(page)
  await expect(rows.first(), 'the audit entries have loaded').toBeVisible()

  // Entry drawer: the clicked row's own entry.
  const row = rows.first()
  const rowEvent = (await kid(row, 4).textContent()) ?? ''
  const rowTenant = (await kid(row, 2).textContent()) ?? ''
  await row.click()
  let { drawer } = await expectDrawer(page, 'entry drawer')
  await expect(drawer, 'the drawer names the row event').toContainText(rowEvent)
  await expect(metaValue(drawer, 'Tenant'), 'the drawer tenant is the row tenant').toHaveText(rowTenant)
  await atWidths(page, 'SUP-09 entry drawer layout', async () => {
    const body = kid(drawer, 2)
    const r = await boxes(page, { drawer, meta: kid(body, 1), pre: body.locator('pre') })
    const cells = await childRects(kid(body, 1))
    return {
      problems: [
        ...within(viewport(page), r.drawer, 'drawer in the viewport'),
        ...pairwiseApart(cells, 'meta cell'),
        ...cells.flatMap((c, i) => within(r.meta, c, `meta cell ${i} in the grid`)),
        ...withinX(r.drawer, r.pre, 'payload in the drawer'),
      ],
      rects: r,
    }
  })
  await attachShot(page, testInfo, 'entry-drawer')
  await drawer.getByRole('button', { name: 'Close' }).click()
  await expect(drawer, 'Close closes the entry drawer').toBeHidden()

  // Staff changes: the tenant-2222 cancel of the Submissions test.
  const changes = (await liveStaffGet<{ changes: LiveChange[] }>(STAFF_CHANGES)).changes
  const change = changes.find((c) => c.event === 'submission.job.cancelled' && c.tenant_id === DEMO_2222_ID && (!cancel2222.reason || c.payload?.reason === cancel2222.reason))
  expect(change, 'the staff change log holds the tenant-2222 cancel').toBeDefined()
  await mn.getByRole('button', { name: 'STAFF CHANGES' }).click()
  const changeRow = mn.locator(`button[data-change-id="${change!.id}"]`)
  await expect(changeRow, 'the staff changes view lists the cancel').toBeVisible()
  await expect(kid(changeRow, 2), 'staff member').toHaveText(change!.actor)
  await expect(kid(changeRow, 3), 'event').toHaveText('submission.job.cancelled')
  await expect(kid(changeRow, 4), 'tenant').toHaveText(change!.tenant_name ?? DEMO_2222_ID)
  await changeRow.click()
  ;({ drawer } = await expectDrawer(page, 'staff change drawer'))
  const openLog = drawer.getByRole('button', { name: 'Open in tenant log' })
  await atWidths(page, 'SUP-09 staff change drawer layout', async () => {
    const body = kid(drawer, 2)
    const r = await boxes(page, { drawer, meta: kid(body, 1), pre: body.locator('pre'), footer: kid(drawer, 3), openLog })
    const cells = await childRects(kid(body, 1))
    return {
      problems: [
        ...within(viewport(page), r.drawer, 'drawer in the viewport'),
        ...pairwiseApart(cells, 'meta cell'),
        ...cells.flatMap((c, i) => within(r.meta, c, `meta cell ${i} in the grid`)),
        ...withinX(r.drawer, r.pre, 'payload in the drawer'),
        ...within(r.drawer, r.footer, 'footer in the drawer'),
        ...within(r.footer, r.openLog, 'Open in tenant log in the footer'),
      ],
      rects: r,
    }
  })

  // The tenant link lands on the tenant's own row, written by support.
  await openLog.click()
  ;({ drawer } = await expectDrawer(page, 'linked entry drawer'))
  await expect(drawer, 'the linked entry is the cancel').toContainText('submission.job.cancelled')
  await expect(metaValue(drawer, 'Actor'), 'the customer sees the support actor').toHaveText(SUPPORT_ACTOR)
  if (cancel2222.reason) await expect(drawer.locator('pre'), 'the linked entry carries this run reason').toContainText(cancel2222.reason)
  await expect(mn.locator('select[aria-label="Tenant"]'), 'the link filters to tenant 2222').toHaveValue(DEMO_2222_ID)
  await drawer.getByRole('button', { name: 'Close' }).click()
  await expect(drawer).toBeHidden()

  // Evidence bundle: tenant 2222, its seeded company.
  await mn.getByRole('button', { name: 'Evidence bundle' }).click()
  ;({ drawer } = await expectDrawer(page, 'evidence drawer'))
  await drawer.getByLabel('Tenant', { exact: true }).selectOption(DEMO_2222_ID)
  const entities = (await liveStaffGet<{ entities: { id: string; name: string; tin: string | null }[] }>(`${staffTenantPath(DEMO_2222_ID)}/entities`)).entities
  expect(entities.length, 'tenant 2222 has a company').toBeGreaterThan(0)
  const company = entities.find((e) => e.tin === SEED_2222_TIN)
  expect(company, `tenant 2222 holds the seeded company ${SEED_2222_TIN}`).toBeDefined()
  await drawer.getByLabel('Company', { exact: true }).selectOption(company!.id)
  await drawer.getByLabel('From', { exact: true }).fill(BUNDLE_FROM)
  await drawer.getByLabel('To', { exact: true }).fill(BUNDLE_TO)

  const query = new URLSearchParams({ entity_id: company!.id, from: `${BUNDLE_FROM}T00:00:00.000Z`, to: `${BUNDLE_TO}T23:59:59.999Z` })
  const live = await liveStaffGet<LivePreview>(`${staffTenantPath(DEMO_2222_ID)}/evidence-bundle/preview?${query}`)
  expect(live.counts.invoices, `the seeded tenant-2222 company (${company!.name}) holds invoices in the period`).toBeGreaterThan(0)
  await expect(metaValue(drawer, 'TIN'), 'TIN cell').toHaveText(live.entity.tin ?? '—')
  for (const [label, key] of PREVIEW_CELLS) await expect(metaValue(drawer, label), `${label} cell`).toHaveText(String(live.counts[key]))

  const download = drawer.getByRole('button', { name: 'Download bundle' })
  await expect(download).toBeEnabled()
  await atWidths(page, 'SUP-09 evidence drawer layout', async () => {
    const body = kid(drawer, 2)
    const r = await boxes(page, {
      drawer,
      tenant: drawer.getByLabel('Tenant', { exact: true }),
      company: drawer.getByLabel('Company', { exact: true }),
      from: drawer.getByLabel('From', { exact: true }),
      to: drawer.getByLabel('To', { exact: true }),
      meta: kid(body, 4),
      footer: kid(drawer, 3),
      download,
    })
    const cells = await childRects(kid(body, 4))
    const fields = [r.tenant, r.company, r.from, r.to]
    return {
      problems: [
        ...within(viewport(page), r.drawer, 'drawer in the viewport'),
        ...pairwiseApart(fields, 'evidence fields'),
        ...fields.flatMap((f, i) => within(r.drawer, f, `evidence field ${i} in the drawer`)),
        ...pairwiseApart(cells, 'meta cell'),
        ...cells.flatMap((c, i) => within(r.meta, c, `meta cell ${i} in the grid`)),
        ...within(r.drawer, r.footer, 'footer in the drawer'),
        ...within(r.footer, r.download, 'Download bundle in the footer'),
      ],
      rects: r,
    }
  })
  await attachShot(page, testInfo, 'evidence-drawer')

  const [file] = await Promise.all([page.waitForEvent('download'), download.click()])
  expect(file.suggestedFilename(), 'download filename').toMatch(/^ASComply_evidence_.+\.zip$/)
  expect(file.suggestedFilename(), 'the download is named as the preview says').toBe(live.filename)
  const zip = unzipSync(new Uint8Array(await readFile((await file.path())!)))
  const manifest = JSON.parse(new TextDecoder().decode(zip['manifest.json'])) as { entity: LivePreview['entity']; counts: LivePreview['counts']; generated_by: unknown }
  expect(manifest.entity.tin, 'the zip manifest TIN is the preview TIN').toBe(live.entity.tin)
  expect(manifest.counts, 'the zip manifest counts are the preview counts').toEqual(live.counts)
  expect(manifest.generated_by, 'the zip is generated by support').toEqual({ name: SUPPORT_ACTOR, kind: 'raw' })
  await expect(page.getByRole('status').filter({ hasText: 'Bundle downloaded' })).toBeVisible()

  noErrors(errors, 'SUP-09 Audit')
})
