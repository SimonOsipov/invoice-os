// The deployed v2 Ops Console: resolved values and layout relationships on the PR environment.
// The OPS-06 tests create, pin and delete a real blog post and restore the featured post; the rest reads mock-backed screens.
// Value reads copy a value from its source (token or prototype) and name it; screenshots are attached, never asserted.
import { randomBytes } from 'node:crypto'
import { test, expect, type Locator, type Page, type Route, type TestInfo } from '@playwright/test'
import { apiBase, provisionStaffAccount, rawFetch, signInSession, type StaffAccount } from '../api/client'
import { collectErrors } from '../personaSession'
import { CONSOLE_SESSION_KEY, consoleUrl, seedStaffSession } from '../staffSession'
import { resolveTarget } from '../targets'
import { expectedStatusDropper, type Dropper } from './consoleGate'
import { enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'

test.use({ viewport: { width: 1440, height: 900 } })

let staff: Promise<StaffAccount> | undefined
const staffAccount = (): Promise<StaffAccount> => (staff ??= provisionStaffAccount('reskin-ops'))
const signInOps = async (page: Page): Promise<unknown> => seedStaffSession(page, 'ops', await staffAccount())

// Resolved colours, each named by its v2 token (packages/design-tokens/v2/tokens/colors.css) or its prototype read.
const C = {
  surface: 'rgb(8, 47, 49)', // --surface #082f31
  surfacePanel: 'rgb(12, 60, 57)', // --surface-panel #0c3c39
  surfacePanelBorder: 'rgb(22, 71, 66)', // --surface-panel-border #164742
  surfaceForeground: 'rgb(247, 246, 237)', // --surface-foreground #f7f6ed
  accent: 'rgb(245, 188, 136)', // --accent #f5bc88 (peach)
  primary: 'rgb(7, 60, 61)', // --primary #073c3d
  white: 'rgb(255, 255, 255)', // --card / --primary-foreground / --destructive-foreground #fff
  background: 'rgb(250, 248, 242)', // --background #faf8f2
  sage: 'rgb(231, 236, 223)', // --sage #e7ecdf
  sageCardBorder: 'rgb(206, 218, 199)', // --sage-card-border #cedac7
  border: 'rgb(220, 231, 228)', // --border #dce7e4
  input: 'rgb(201, 217, 214)', // --input #c9d9d6
  muted: 'rgb(239, 246, 244)', // --muted #eff6f4
  ink: 'rgb(11, 48, 50)', // --ink #0b3032
  link: 'rgb(13, 93, 76)', // --link #0d5d4c
  destructive: 'rgb(181, 59, 59)', // --destructive #b53b3b
  mint: 'rgb(203, 241, 221)', // --mint #cbf1dd
  ring: 'rgb(56, 135, 126)', // --ring #38877e
  teal300: 'rgb(159, 200, 191)', // --teal-300 #9fc8bf
  liveDot: 'rgb(143, 220, 170)', // .asc-dark --status-green-text #8fdcaa (prototype Live dot)
  redIcon: 'rgb(244, 176, 176)', // .asc-dark --status-red-text #f4b0b0
  headerBg: 'rgba(250, 248, 242, 0.95)', // --header-bg
  greenBorder: 'rgba(29, 115, 67, 0.25)', // light --status-green-border
  primary10: 'rgba(7, 60, 61, 0.1)', // --primary-10
  transparent: 'rgba(0, 0, 0, 0)',
}
const R = { sm: '4px', md: '6px', btn: '7px', lg: '10px', circle: '50%' } // --radius-sm / -md / -btn / -lg; dots and avatars
const SHADOW_CARD = 'rgba(40, 83, 52, 0.21) 0px 14px 22px -16px' // --shadow-card
const SURFACE_RGB = [8, 47, 49] as const // --surface channels; scrims are colour-mixes of it

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

async function attachJson(testInfo: TestInfo, name: string, body: unknown): Promise<void> {
  await testInfo.attach(`${name}.json`, { body: JSON.stringify(body, null, 2), contentType: 'application/json' })
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
// Horizontal only: the header label is a text line, shorter than the 28px pin button.
const withinX = (outer: Rect, inner: Rect, what: string): string[] =>
  inner.x >= outer.x - 1 && inner.x + inner.width <= outer.x + outer.width + 1 ? [] : [`${what}: sticks out sideways`]
const apart = (a: Rect, b: Rect, what: string): string[] => (rectsOverlap(a, b) ? [`${what}: overlap`] : [])
const below = (above: Rect, lower: Rect, what: string): string[] => (lower.y >= above.y + above.height - 1 ? [] : [`${what}: not below`])
const leftOf = (a: Rect, b: Rect, what: string): string[] => (a.x + a.width <= b.x + 1 ? [] : [`${what}: not left of`])
const sameLeft = (a: Rect, b: Rect, what: string): string[] => (Math.abs(a.x - b.x) <= 1 ? [] : [`${what}: left edges ${a.x} vs ${b.x}`])
const centreY = (r: Rect): number => r.y + r.height / 2
const sameCentre = (a: Rect, b: Rect, what: string): string[] => (Math.abs(centreY(a) - centreY(b)) <= 1 ? [] : [`${what}: centres ${centreY(a)} vs ${centreY(b)}`])
const sameTop = (rs: Rect[], what: string): string[] => rs.flatMap((r, i) => (Math.abs(r.y - rs[0].y) <= 1 ? [] : [`${what} ${i}: top ${r.y} vs ${rs[0].y}`]))
const allApart = (rs: Rect[], what: string): string[] => rs.flatMap((a, i) => rs.slice(i + 1).flatMap((b, j) => apart(a, b, `${what} ${i}/${i + 1 + j}`)))

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

// Boxes of every match, after waiting for at least `min` of them.
async function boxList(page: Page, loc: Locator, what: string, min: number): Promise<Rect[]> {
  await expect.poll(() => loc.count(), { message: `${what}: fewer than ${min} matched`, timeout: 15_000 }).toBeGreaterThanOrEqual(min)
  const named = Object.fromEntries((await loc.all()).map((l, i) => [`${what} ${i}`, l]))
  return Object.values(await boxes(page, named))
}

// The shell scrolls inside the parent of `.ops-screen-pad`, so the app's `.pf-scroll` helper does not apply.
async function noSidewaysScroll(page: Page, label: string, inside: Locator = page.locator('.ops-screen-pad')): Promise<string[]> {
  const over = await inside.evaluate((el) => {
    const p = el.parentElement as HTMLElement
    return p.scrollWidth - p.clientWidth
  })
  return over <= 1 ? [] : [`${label}: scrolls sideways by ${over}px`]
}

// Polls every named property of one element to its exact value (a colour transition may still be running); returns the settled reading.
async function check(loc: Locator, what: string, want: Record<string, string>): Promise<Record<string, string>> {
  for (const [k, v] of Object.entries(want)) {
    await expect.poll(async () => (await styles(loc, [k]))[k], { message: `${what}: ${k}` }).toBe(v)
  }
  return styles(loc, Object.keys(want))
}

async function corners(loc: Locator, what: string, value: string): Promise<void> {
  expect(await radii(loc), `${what}: corners`).toEqual(Array(4).fill(value))
}

// Waits for a floor, then returns every match; no assertion sits behind a count check.
async function every(loc: Locator, what: string, min: number): Promise<Locator[]> {
  await expect.poll(() => loc.count(), { message: `${what}: fewer than ${min} matched`, timeout: 15_000 }).toBeGreaterThanOrEqual(min)
  return loc.all()
}

async function font(loc: Locator, what: string, want: { family: string; weight: string; size: string; spacing?: string }): Promise<void> {
  const got = await styles(loc, ['font-family', 'font-weight', 'font-size', 'letter-spacing'])
  expect(firstFamily(got['font-family']), `${what}: family`).toBe(want.family)
  expect(got['font-weight'], `${what}: weight`).toBe(want.weight)
  expect(got['font-size'], `${what}: size`).toBe(want.size)
  if (want.spacing) expect(got['letter-spacing'], `${what}: letter-spacing`).toBe(want.spacing)
}

const aside = (page: Page) => page.locator('aside.ops-sidebar')
const navButton = (page: Page, label: RegExp) => aside(page).locator('nav button.ops-nav', { hasText: label })
const header = (page: Page) => page.locator('main header')
const pad = (page: Page) => page.locator('.ops-screen-pad')
const envTrack = (page: Page) => header(page).getByRole('button', { name: 'SANDBOX', exact: true }).locator('xpath=..')
const banner = (page: Page) => header(page).locator('xpath=following-sibling::div[1]')
const firstRow = (page: Page) => page.locator('.ops-row').first()

type Screen = { key: string; nav: RegExp; h1: string }

const SCREENS: Screen[] = [
  { key: 'overview', nav: /^Overview/, h1: 'Overview' },
  { key: 'submissions', nav: /^Submissions/, h1: 'Submissions' },
  { key: 'evidence', nav: /^Evidence/, h1: 'Compliance evidence' },
  { key: 'api', nav: /^API/, h1: 'API & webhooks' },
  { key: 'billing', nav: /^Usage/, h1: 'Usage & billing' },
  { key: 'status', nav: /^Status/, h1: 'API status' },
]

const h1Of = (page: Page, s: Screen) => page.getByRole('heading', { level: 1, name: s.h1, exact: true })

// Click a nav row and wait until it is the active one and its screen's h1 has drawn.
async function openScreen(page: Page, s: Screen): Promise<void> {
  const button = navButton(page, s.nav)
  await button.click()
  await expect(button, `${s.key}: the clicked nav row never became the active one (weight 600)`).toHaveCSS('font-weight', '600')
  await expect(h1Of(page, s), `${s.key}: the screen drew no h1`).toBeVisible()
  await settle(page, pad(page))
}

async function open(page: Page, key: string): Promise<Screen> {
  const s = SCREENS.find((x) => x.key === key)
  if (!s) throw new Error(`no screen ${key}`)
  await signInOps(page)
  await openScreen(page, s)
  return s
}

// Reads the scrim (the drawer's preceding sibling) and the drawer; shared by the job and evidence drawers.
async function readDrawer(page: Page, drawer: Locator): Promise<Record<string, unknown>> {
  await settle(page, drawer)
  const panel = await check(drawer, 'drawer', {
    'background-color': C.background, // --bg-1 on the v2 ground (prototype drawer)
    'border-left-width': '1px',
    'border-left-color': C.input,
    'box-shadow': 'none',
  })
  const box = await drawer.boundingBox()
  const vw = page.viewportSize()!.width
  expect(box, 'drawer has no box').toBeTruthy()
  expect(Math.abs(box!.x + box!.width - vw), 'drawer right edge equals the viewport edge').toBeLessThanOrEqual(1)
  const scrim = drawer.locator('xpath=preceding-sibling::div[1]')
  const s = await styles(scrim, ['background-color', 'backdrop-filter'])
  const c = parseColor(s['background-color'])
  SURFACE_RGB.forEach((v, i) => expect(Math.abs([c.r, c.g, c.b][i] - v), `drawer scrim channel ${i}`).toBeLessThanOrEqual(1))
  expect(Math.abs(c.a - 0.32), 'drawer scrim alpha').toBeLessThanOrEqual(0.01)
  expect(s['backdrop-filter'], 'drawer scrim backdrop-filter').toBe('none')
  return { panel, scrim: s }
}

async function closeByScrim(drawer: Locator): Promise<void> {
  await drawer.locator('xpath=preceding-sibling::div[1]').click({ position: { x: 40, y: 300 } })
  await expect(drawer, 'the scrim click left the drawer open').toBeHidden()
}

const errorsOf = (errors: string[]) => `console errors:\n${errors.join('\n')}`

// ---------------------------------------------------------------- OPS-01

test('OPS-01 shell at 1440: aside, header, env switch, banner', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)
  await signInOps(page)
  await settle(page, aside(page), header(page))
  const measured: Record<string, unknown> = {}

  const className = await aside(page).getAttribute('class')
  expect(className, 'aside scope class').toContain('asc-dark')
  measured.aside = await check(aside(page), 'aside', { 'background-color': C.surface, 'border-right-color': C.surfacePanelBorder })

  const active = navButton(page, /^Overview/)
  await expect(active, 'the default view is Overview, so its row is the active one (weight 600)').toHaveCSS('font-weight', '600')
  measured.activeRow = await check(active, 'active nav row', { 'background-color': C.surfacePanel })
  await corners(active, 'active nav row', R.md)
  await check(active.locator('> span').nth(0), 'active nav bar', { 'background-color': C.accent })
  await check(active.locator('> span').nth(1), 'active nav icon', { color: C.accent })

  const eyebrow = await resolveColor(aside(page), '--eyebrow-on-dark')
  await check(aside(page).getByText('Console', { exact: true }), 'Console label', { color: eyebrow })
  await check(aside(page).getByText('Requests this month', { exact: true }), 'quota label', { color: eyebrow })

  const fg1 = await resolveColor(aside(page), '--fg-1')
  const fg2 = await resolveColor(aside(page), '--fg-2')
  const fg3 = await resolveColor(aside(page), '--fg-3')
  expect(fg1, 'control: aside --fg-1 differs from --fg-3').not.toBe(fg3)
  expect(fg2, 'control: aside --fg-2 differs from --fg-3').not.toBe(fg3)
  await check(active, 'active nav row text equals aside --fg-1', { color: fg1 })
  await check(navButton(page, /^Submissions/), 'idle nav row text equals aside --fg-2', { color: fg2 })
  const switcher = aside(page).locator('button[aria-haspopup="menu"]')
  const sw = await check(switcher, 'org switcher text equals aside --fg-1', { color: fg1 })
  const ratio = contrast((await styles(switcher, ['color']))['color'], (await styles(switcher, ['background-color']))['background-color'])
  expect(ratio, 'org switcher text contrast against its own fill').toBeGreaterThanOrEqual(4.5)

  const mark = aside(page).locator('img').first()
  await expect.poll(() => mark.evaluate((el) => (el as HTMLImageElement).naturalWidth), { message: 'brand mark never loaded' }).toBeGreaterThan(0)
  const src = await mark.evaluate((el) => (el as HTMLImageElement).currentSrc)
  expect(src, 'brand mark is the v2 mark').toMatch(/\/mark(-[\w-]+)?\.png/)
  expect(src, 'brand mark is not the v1 logo-mark').not.toContain('logo-mark')
  await corners(mark, 'brand mark', '0px') // the raster carries its own corners

  await corners(navButton(page, /^Submissions/).locator('span.mono'), 'Submissions nav badge', R.sm)
  await corners(aside(page).getByText('AO', { exact: true }), 'avatar', R.circle)
  await corners(aside(page).getByRole('button', { name: 'Sign out' }), 'Sign out', R.btn)

  measured.header = await check(header(page), 'main header', { 'background-color': C.headerBg, 'backdrop-filter': 'blur(18px)' }) // --header-blur 18px

  const track = envTrack(page)
  const amberBorder = await resolveColor(page.locator('main'), '--status-amber-border')
  const amberBg = await resolveColor(page.locator('main'), '--status-amber-bg')
  const amberText = await resolveColor(page.locator('main'), '--status-amber-text')
  const greenText = await resolveColor(page.locator('main'), '--status-green-text')
  for (const [name, v] of Object.entries({ amberBorder, amberBg, amberText, greenText })) {
    expect(v, `control: resolved ${name} is not transparent`).not.toBe(C.transparent)
  }
  measured.track = await check(track, 'env track', { 'background-color': C.sage, 'column-gap': '2px', 'border-top-color': amberBorder })
  await corners(track, 'env track', R.btn)

  const sandbox = header(page).getByRole('button', { name: 'SANDBOX', exact: true })
  const live = header(page).getByRole('button', { name: 'LIVE', exact: true })
  measured.sandbox = await check(sandbox, 'SANDBOX segment', {
    'background-color': C.primary,
    color: C.white,
    height: '28px',
    'padding-left': '13px',
  })
  await corners(sandbox, 'SANDBOX segment', R.sm)
  await check(sandbox.locator('> span').first(), 'SANDBOX dot', { 'background-color': C.accent })
  await corners(sandbox.locator('> span').first(), 'SANDBOX dot', R.circle)
  await check(live, 'LIVE segment (idle)', { 'background-color': C.transparent })
  await check(live.locator('> span').first(), 'LIVE dot (idle)', { 'background-color': greenText })

  const bn = banner(page)
  await check(bn, 'sandbox banner', { 'background-color': amberBg, 'border-bottom-color': amberBorder })
  await check(bn.locator('> span').nth(1), 'sandbox banner message', { color: amberText })

  const widths = await atWidths(page, 'OPS-01 shell layout', async () => {
    const r = await boxes(page, {
      aside: aside(page),
      header: header(page),
      track,
      sandbox,
      live,
      search: header(page).locator('.ops-header-search'),
      crumb: header(page).locator('> div').first(),
      mark,
      word: aside(page).getByText('ASComply', { exact: true }),
      quota: aside(page).getByText('Requests this month', { exact: true }).locator('xpath=../..'),
    })
    const navs = await boxList(page, aside(page).locator('nav button.ops-nav'), 'nav button', 7)
    const problems = [
      ...within(r.track, r.sandbox, 'SANDBOX in track'),
      ...within(r.track, r.live, 'LIVE in track'),
      ...apart(r.sandbox, r.live, 'SANDBOX/LIVE'),
      ...leftOf(r.sandbox, r.live, 'SANDBOX/LIVE'),
      ...sameCentre(r.sandbox, r.live, 'SANDBOX/LIVE'),
      ...within(r.header, r.track, 'track in header'),
      ...within(r.header, r.search, 'search in header'),
      ...apart(r.track, r.search, 'track/search'),
      ...leftOf(r.crumb, r.search, 'crumb/search'),
      ...leftOf(r.mark, r.word, 'brand mark/ASComply'),
      ...sameCentre(r.mark, r.word, 'brand mark/ASComply'),
      ...navs.flatMap((n, i) => within(r.aside, n, `nav ${i} in aside`)),
      ...below(navs[navs.length - 1], r.quota, 'quota card/last nav row'),
      ...(await noSidewaysScroll(page, 'OPS-01')),
    ]
    return { problems, rects: { ...r, navs } }
  })

  await attachJson(testInfo, 'ops-01-measurements', { ...measured, switcher: { ...sw, ratio }, src, widths })
  await attachShot(page, testInfo, 'overview')
  expect(errors, errorsOf(errors)).toEqual([])
})

test('OPS-01 org switcher: menu look and placement', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)
  await signInOps(page)
  const button = aside(page).locator('button[aria-haspopup="menu"]')
  await button.click()
  const menu = page.getByRole('menu')
  await expect(menu, 'the org menu never opened').toBeVisible()
  await settle(page, menu)
  expect(await menu.getAttribute('class'), 'menu scope class').toContain('asc-light')
  const look = await check(menu, 'org menu', { 'background-color': C.white, 'box-shadow': SHADOW_CARD })
  await corners(menu, 'org menu', R.md)

  const r = await boxes(page, { button, menu })
  expect(Math.abs(r.menu.x - r.button.x), 'menu left edge equals the button').toBeLessThanOrEqual(1)
  expect(Math.abs(r.menu.x + r.menu.width - (r.button.x + r.button.width)), 'menu right edge equals the button').toBeLessThanOrEqual(1)
  expect(below(r.button, r.menu, 'menu/button'), 'menu opens below the button').toEqual([])

  const fg1 = await resolveColor(menu, '--fg-1')
  for (const item of await every(menu.getByRole('menuitem'), 'menu items', 1)) await check(item, 'menu item text equals --fg-1', { color: fg1 })

  await attachJson(testInfo, 'ops-01-org-switcher', { look, rects: r })
  await attachShot(page, testInfo, 'org-switcher')
  await button.click()
  await expect(menu, 'the second click left the menu open').toBeHidden()
  expect(errors, errorsOf(errors)).toEqual([])
})

// ---------------------------------------------------------------- OPS-02

test('OPS-02 Live environment: switch, track and banner', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)
  await signInOps(page)
  const live = header(page).getByRole('button', { name: 'LIVE', exact: true })
  const sandbox = header(page).getByRole('button', { name: 'SANDBOX', exact: true })
  await live.click()
  await settle(page, header(page), banner(page))
  const amberText = await resolveColor(page.locator('main'), '--status-amber-text')

  await check(live, 'LIVE segment (active)', { 'background-color': C.primary })
  await check(live.locator('> span').first(), 'LIVE dot (active)', { 'background-color': C.liveDot })
  await check(sandbox.locator('> span').first(), 'SANDBOX dot (idle) equals --status-amber-text', { 'background-color': amberText })
  await check(envTrack(page), 'Live track', { 'border-top-color': C.greenBorder })
  await check(banner(page), 'Live banner', { 'background-color': C.primary10, 'border-bottom-color': C.mint })
  await check(banner(page).locator('> span').nth(1), 'Live banner message', { color: C.primary })

  await attachShot(page, testInfo, 'live-env')
  expect(errors, errorsOf(errors)).toEqual([])
})

// ---------------------------------------------------------------- OPS-03

type Ctx = { page: Page; measured: Record<string, unknown>; fg3: string; ink: string }

// Card titles: 16 / 700 / -0.02em; panel titles: 14 / 700 (prototype; both `--ink`).
async function titles(ctx: Ctx, names: string[], size: '16px' | '14px'): Promise<void> {
  for (const name of names) {
    const t = pad(ctx.page).getByText(name, { exact: true })
    await expect(t, `title ${name} never drew`).toHaveCount(1)
    // 16px card titles: -0.02em; 14px panel titles: no tracking
    await font(t, `title ${name}`, { family: 'Manrope', weight: '700', size, ...(size === '16px' ? { spacing: '-0.32px' } : {}) })
    await check(t, `title ${name}`, { color: ctx.ink })
  }
}

const SCREEN_TESTS: Record<string, (ctx: Ctx) => Promise<void>> = {
  async overview({ page, measured, fg3, ink }) {
    const cards = page.locator('.ops-kpi-strip > div')
    await expect(cards, 'the KPI strip draws six cards').toHaveCount(6)
    for (const name of ['API requests over time', 'Spend over time', 'Submission outcomes', 'Top rejection reasons', 'Clearance latency']) {
      await check(pad(page).getByText(name, { exact: true }), `card title ${name}`, { 'line-height': 'normal' })
    }
    for (const [i, card] of (await cards.all()).entries()) {
      await corners(card, `KPI ${i}`, R.md)
      await check(card, `KPI ${i}`, {
        'border-top-width': '1px',
        'border-top-style': 'solid',
        'border-top-color': C.border,
        'box-shadow': 'none',
        'background-color': C.white,
        'min-height': '124px',
      })
      await font(card.locator('.money').first(), `KPI ${i} figure`, { family: 'Manrope', weight: '700', size: '24px' })
      await check(card.locator('.money').first(), `KPI ${i} figure`, { 'font-variant-numeric': 'tabular-nums' })
      await check(card.locator('span.mono').first(), `KPI ${i} delta`, { 'font-weight': '600' })
    }

    const track = page.getByRole('button', { name: '30D', exact: true }).locator('xpath=..')
    await check(track, 'range track', { 'background-color': C.sage, 'border-top-color': C.sageCardBorder, 'column-gap': '2px' })
    await corners(track, 'range track', R.btn)
    const segs = track.locator('button')
    await expect(segs, 'the range track draws three buttons').toHaveCount(3)
    for (const [i, b] of (await segs.all()).entries()) await corners(b, `range button ${i}`, R.sm)
    await check(page.getByRole('button', { name: '30D', exact: true }), 'active range button', { 'background-color': C.primary })

    const fg4 = await resolveColor(pad(page), '--fg-4')
    expect(fg3, 'control: --fg-3 differs from --fg-4').not.toBe(fg4)
    const axis = page.locator('svg[viewBox="0 0 1000 240"] + div > span')
    for (const [i, l] of (await every(axis, 'axis labels', 2)).entries()) await check(l, `axis label ${i}`, { color: fg3 })
    for (const t of ['30d ago', 'today']) await check(pad(page).getByText(t, { exact: true }), `latency label ${t}`, { color: fg3 })

    for (const t of ['API requests over time', 'Submission outcomes']) await check(pad(page).getByText(t, { exact: true }), `title ${t}`, { 'margin-bottom': '4px' })
    await titles({ page, measured, fg3, ink }, ['API requests over time', 'Spend over time', 'Submission outcomes', 'Top rejection reasons', 'Clearance latency'], '16px')
    await corners(pad(page).getByText('ELEVATED', { exact: true }).locator('xpath=preceding-sibling::span[1]'), 'ELEVATED dot', R.circle)
    const barTrack = pad(page).getByText('Invalid buyer TIN', { exact: true }).locator('xpath=../following-sibling::div[1]')
    await corners(barTrack, 'rejection bar track', '2px')
    await corners(barTrack.locator('> div'), 'rejection bar fill', '0px')

    measured.widths = await atWidths(page, 'OPS-03 overview layout', async () => {
      const kpis = await boxList(page, cards, 'KPI card', 6)
      const parts: Record<string, Rect[]> = { label: [], money: [], svg: [] }
      const problems: string[] = [...allApart(kpis, 'KPI cards'), ...sameTop(kpis, 'KPI cards')]
      for (const [i, card] of (await cards.all()).entries()) {
        const inner = await boxes(page, { label: card.locator('.label').first(), money: card.locator('.money').first(), svg: card.locator('svg').first() })
        for (const k of ['label', 'money', 'svg'] as const) {
          parts[k].push(inner[k])
          problems.push(...within(kpis[i], inner[k], `KPI ${i} ${k}`))
        }
      }
      const r = await boxes(page, {
        h1: h1Of(page, SCREENS[0]),
        strip: page.locator('.ops-kpi-strip'),
        track,
        title: pad(page).getByText('API requests over time', { exact: true }),
      })
      const buttons = await boxList(page, segs, 'range button', 3)
      problems.push(...below(r.h1, r.strip, 'strip/h1'), ...sameLeft(r.h1, r.strip, 'h1/strip'), ...allApart(buttons, 'range buttons'), ...apart(r.track, r.title, 'range track/title'))
      buttons.forEach((b, i) => problems.push(...within(r.track, b, `range button ${i} in track`)))
      const grids = page.locator('.ops-overview-grid')
      await expect.poll(() => grids.count(), { message: 'the overview draws two chart grids' }).toBe(2)
      for (const [g, grid] of (await grids.all()).entries()) {
        const kids = await boxList(page, grid.locator('> div'), `grid ${g} child`, 2)
        problems.push(...allApart(kids, `grid ${g}`), ...sameTop(kids, `grid ${g}`))
      }
      problems.push(...(await noSidewaysScroll(page, 'overview')))
      return { problems, rects: { kpis, ...r, buttons } }
    })
  },

  async submissions({ page, measured, fg3 }) {
    const tiles = page.locator('.ops-sub-stats > div')
    for (const [i, t] of (await every(tiles, 'sub-stat tiles', 1)).entries()) {
      await corners(t, `sub-stat ${i}`, R.md)
      await check(t, `sub-stat ${i}`, { 'box-shadow': 'none' })
      await font(t.locator('.money'), `sub-stat ${i} figure`, { family: 'Manrope', weight: '700', size: '20px' })
    }
    const chips = page.locator('button.ops-chip')
    for (const [i, c] of (await every(chips, 'filter chips', 2)).entries()) await corners(c, `chip ${i}`, R.sm)
    // The first chip (All) is active; every other count must clear 4.5:1 on its own fill.
    for (const [i, c] of [...(await every(chips, 'filter chips', 2)).entries()].slice(1)) {
      await expect
        .poll(async () => contrast((await styles(c.locator('> span'), ['color'])).color, (await styles(c, ['background-color']))['background-color']), {
          message: `inactive chip ${i} count contrast`,
        })
        .toBeGreaterThanOrEqual(4.5)
    }
    const row = firstRow(page)
    await expect(row, 'Submissions drew no job row').toBeVisible()
    const pill = row.locator('> span').nth(3).locator('> span')
    await corners(pill, 'state pill', R.sm)
    await corners(pill.locator('> span').first(), 'state pill dot', R.circle)
    await check(page.locator('.ops-jobs-table:not(.ops-row)'), 'table head row', { 'background-color': C.muted })
    const redrive = page.getByRole('button', { name: /Re-drive all/ })
    await expect(redrive, 'the dead-letter callout draws no Re-drive all').toBeVisible()
    await check(redrive, 'Re-drive all', { 'background-color': C.destructive, color: C.white })

    measured.widths = await atWidths(page, 'OPS-03 submissions layout', async () => {
      const t = await boxList(page, tiles, 'sub-stat tile', 4)
      const c = await boxList(page, chips, 'chip', 2)
      const problems = [...allApart(t, 'sub-stat tiles'), ...allApart(c, 'chips'), ...c.flatMap((r, i) => sameCentre(c[0], r, `chip ${i}`)), ...(await noSidewaysScroll(page, 'submissions'))]
      return { problems, rects: { tiles: t, chips: c } }
    })

    const input = page.locator('input.ops-input')
    await input.focus()
    // Border and shadow each run a 120 ms transition, so both are polled in one read.
    await expect
      .poll(
        async () => {
          const s = await styles(input, ['border-top-color', 'box-shadow'])
          return { border: s['border-top-color'], ringShadow: s['box-shadow'].includes(C.ring) }
        },
        { message: 'focused input border colour and ring shadow' },
      )
      .toEqual({ border: C.ring, ringShadow: true })
    await input.fill('zz-no-match')
    const empty = page.getByText(/^No submissions match/)
    await expect(empty, 'the empty state never drew').toBeVisible()
    await check(empty, 'empty-state text equals --fg-3', { color: fg3 })
  },

  async evidence({ page }) {
    const row = firstRow(page)
    await expect(row, 'Evidence drew no bundle row').toBeVisible()
    await check(row.locator('> span').nth(1), 'IRN', { color: C.link })
    await corners(row.locator('> span').nth(5).locator('> span'), 'bundle badge', R.sm)
  },

  async api({ page, measured, ink, fg3 }) {
    const keys = page.locator('.ops-api-grid').first()
    const cards = keys.locator('> div')
    for (const [i, c] of (await every(cards, 'key cards', 2)).entries()) await corners(c, `key card ${i}`, R.md)
    for (const [i, b] of (await every(keys.getByRole('button', { name: 'Rotate' }), 'Rotate buttons', 2)).entries()) await corners(b, `Rotate ${i}`, R.btn)
    for (const tag of ['LIVE', 'SANDBOX']) {
      const t = keys.getByText(tag, { exact: true })
      await expect(t, `key tag ${tag}`).toHaveCount(1)
      await corners(t.locator('xpath=preceding-sibling::span[1]'), `${tag} tag dot`, R.circle)
    }
    const active = pad(page).getByText('ACTIVE', { exact: true })
    for (const [i, a] of (await every(active, 'ACTIVE pills', 1)).entries()) {
      await corners(a.locator('xpath=..'), `ACTIVE pill ${i}`, R.sm)
      await corners(a.locator('xpath=preceding-sibling::span[1]'), `ACTIVE dot ${i}`, R.circle)
    }
    await titles({ page, measured, fg3, ink }, ['API keys', 'Webhook endpoints'], '16px')
    await titles({ page, measured, fg3, ink }, ['Recent deliveries', 'Recent API requests'], '14px')
    await check(pad(page).getByText('Recent API requests', { exact: true }).locator('xpath=..'), 'requests header', { display: 'block' })
    await check(pad(page).getByText('Recent deliveries', { exact: true }).locator('xpath=..'), 'deliveries header', { display: 'flex' })

    measured.widths = await atWidths(page, 'OPS-03 api layout', async () => {
      const problems: string[] = []
      const grids = page.locator('.ops-api-grid')
      await expect.poll(() => grids.count(), { message: 'the API screen draws two grids' }).toBe(2)
      for (const [g, grid] of (await grids.all()).entries()) {
        problems.push(...allApart(await boxList(page, grid.locator('> div'), `api grid ${g} child`, 2), `api grid ${g}`))
      }
      problems.push(...(await noSidewaysScroll(page, 'api')))
      return { problems, rects: {} }
    })
  },

  async billing({ page, measured, ink, fg3 }) {
    await font(pad(page).locator('.money', { hasText: '48,214' }).first(), 'plan usage 48,214', { family: 'Manrope', weight: '700', size: '30px' })
    const kpis = pad(page).locator('.ops-billing-kpis .money')
    for (const [i, k] of (await every(kpis, 'billing KPI figures', 3)).entries()) await font(k, `billing KPI ${i}`, { family: 'Manrope', weight: '700', size: '18px' })
    await check(page.locator('.ops-billing-grid'), 'billing grid', { 'margin-bottom': '26px' })
    await corners(page.locator('.ops-invoice-table').nth(1).locator('> span').nth(3).locator('> span'), 'first status pill', R.sm)
    await titles({ page, measured, fg3, ink }, ['Itemized spend · July 2026', 'Invoices from ASComply'], '16px')

    measured.widths = await atWidths(page, 'OPS-03 billing layout', async () => {
      const grid = page.locator('.ops-billing-grid')
      const [g] = await boxList(page, grid, 'billing grid', 1)
      const k = await boxList(page, page.locator('.ops-billing-kpis > div'), 'billing KPI', 3)
      const c = await boxList(page, grid.locator('> div'), 'billing grid child', 2)
      const title = await boxes(page, { title: grid.locator('xpath=following-sibling::div[1]'), card: grid.locator('xpath=following-sibling::div[2]') })
      const problems = [...allApart(k, 'billing KPIs'), ...allApart(c, 'billing grid'), ...below(g, title.title, 'title/billing grid'), ...below(g, title.card, 'first card/billing grid'), ...(await noSidewaysScroll(page, 'billing'))]
      return { problems, rects: { grid: g, k, c, ...title } }
    })
  },

  async status({ page, measured, fg3, ink }) {
    await font(pad(page).getByText('99.98%', { exact: true }), '99.98%', { family: 'Manrope', weight: '700', size: '22px' })
    await check(pad(page).getByText('99.98%', { exact: true }), '99.98%', { 'font-variant-numeric': 'tabular-nums' })
    const overall = pad(page).locator('> div').nth(1)
    const card = pad(page).locator('> div').nth(2)
    await check(card, 'components card', { 'margin-bottom': '26px' })
    const badge = card.locator('> div').first().locator('> div').first().locator('> span').last()
    await corners(badge, 'component badge', R.sm)
    await corners(badge.locator('> span').first(), 'component badge dot', R.circle)
    for (const [i, l] of (await every(pad(page).locator('span.mono', { hasText: /^90 days ago$/ }), '90 days ago labels', 2)).entries()) await check(l, `90 days ago ${i}`, { color: fg3 })
    for (const [i, l] of (await every(pad(page).locator('span.mono', { hasText: /uptime$/ }), 'uptime labels', 2)).entries()) await check(l, `uptime ${i}`, { color: fg3 })
    await check(overall.locator('> span').first(), 'banner icon tile', { color: C.white })
    await titles({ page, measured, fg3, ink }, ['Incident history'], '16px')

    measured.widths = await atWidths(page, 'OPS-03 status layout', async () => {
      const r = await boxes(page, { banner: overall, card })
      const problems = [...below(r.banner, r.card, 'components card/banner'), ...(await noSidewaysScroll(page, 'status'))]
      return { problems, rects: r }
    })
  },
}

for (const s of SCREENS) {
  test(`OPS-03 ${s.key}: v2 header, clearances and elements`, async ({ page }, testInfo) => {
    test.setTimeout(120_000)
    const errors = collectErrors(page)
    await signInOps(page)
    await openScreen(page, s)
    const measured: Record<string, unknown> = {}

    // h1: Manrope 28 / 700 / -0.04em
    await font(h1Of(page, s), 'h1', { family: 'Manrope', weight: '700', size: '28px', spacing: '-1.12px' })
    await check(h1Of(page, s), 'h1', { color: C.ink })
    await check(pad(page), 'screen padding', { 'padding-top': '26px', 'padding-left': '28px', 'padding-right': '28px', 'padding-bottom': '56px' })
    const head = pad(page).locator('> div').first()
    await check(head, 'header row', { 'margin-bottom': '22px' })
    await check(head.locator('.eyebrow'), 'eyebrow', { 'margin-bottom': '10px' })

    const grow = await page.evaluate(() => {
      const all = [...document.querySelectorAll('main *')]
      return { scanned: all.length, growing: all.filter((el) => getComputedStyle(el).animationName.includes('opsGrow')).length }
    })
    expect(grow.scanned, 'control: the opsGrow scan covers the screen').toBeGreaterThan(20)
    expect(grow.growing, 'elements still running opsGrow').toBe(0)
    expect(await noSidewaysScroll(page, s.key), 'screen scrolls sideways').toEqual([])
    await attachShot(page, testInfo, s.key)

    const ctx: Ctx = { page, measured, fg3: await resolveColor(pad(page), '--fg-3'), ink: await resolveColor(pad(page), '--ink') }
    await SCREEN_TESTS[s.key](ctx)
    await attachJson(testInfo, `ops-03-${s.key}-measurements`, measured)
    expect(errors, errorsOf(errors)).toEqual([])
  })
}

// ---------------------------------------------------------------- OPS-04

test('OPS-04 job drawer: panel, scrim, badge, timeline, JSON', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)
  await open(page, 'submissions')
  await firstRow(page).click()
  const drawer = page.locator('.ops-drawer')
  await expect(drawer, 'the job drawer never opened').toBeVisible()
  const measured = await readDrawer(page, drawer)

  const badge = drawer.locator('> div').first().locator('> div').first().locator('> div').first().locator('> span').nth(1)
  await corners(badge, 'state badge', R.sm)
  await corners(badge.locator('> span').first(), 'state badge dot', R.circle)
  const dots = drawer.locator('span[style*="width: 11px"][style*="height: 11px"]')
  for (const [i, d] of (await every(dots, 'timeline dots', 4)).entries()) await corners(d, `timeline dot ${i}`, R.circle)
  const json = drawer.locator('pre.ops-json').first()
  await corners(json, 'JSON block', R.md)
  await check(json, 'JSON block', { 'background-color': C.surface })

  await attachJson(testInfo, 'ops-04-job-drawer', measured)
  await attachShot(page, testInfo, 'job-drawer')
  await closeByScrim(drawer)
  expect(errors, errorsOf(errors)).toEqual([])
})

test('OPS-04 red toast: Cancel draws an asc-dark toast with a red icon', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)
  await open(page, 'submissions')
  await firstRow(page).click()
  const drawer = page.locator('.ops-drawer')
  await expect(drawer, 'the job drawer never opened').toBeVisible()
  await drawer.getByRole('button', { name: 'Cancel', exact: true }).click()
  // One read, first: the toast clears itself after 3.4 s.
  const read = await page
    .getByText(/^Cancelled · /)
    .locator('xpath=..')
    .evaluate((el) => ({ cls: el.className, icon: getComputedStyle(el.firstElementChild as Element).color }))
  expect(read.cls, 'red toast scope class').toContain('asc-dark')
  expect(read.icon, 'red toast icon colour (.asc-dark --status-red-text)').toBe(C.redIcon)
  expect(errors, errorsOf(errors)).toEqual([])
})

test('OPS-04 evidence drawer: panel, scrim, QR tile', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)
  await open(page, 'evidence')
  await firstRow(page).click()
  const drawer = page.locator('.ops-drawer')
  await expect(drawer, 'the evidence drawer never opened').toBeVisible()
  const measured = await readDrawer(page, drawer)

  const qr = drawer.locator('svg[viewBox="0 0 64 64"]')
  const tile = qr.locator('xpath=..')
  await check(tile, 'QR tile', { 'background-color': C.surface })
  await corners(tile, 'QR tile', R.md)
  await check(qr.locator('rect').first(), 'QR first rect', { stroke: C.white })

  await attachJson(testInfo, 'ops-04-evidence-drawer', measured)
  await attachShot(page, testInfo, 'evidence-drawer')
  await closeByScrim(drawer)
  expect(errors, errorsOf(errors)).toEqual([])
})

test('OPS-04 rotate modal: panel, scrim, heading', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)
  await open(page, 'api')
  await pad(page).getByRole('button', { name: 'Rotate' }).first().click()
  const h3 = page.getByRole('heading', { level: 3, name: /^Rotate .+ key\?$/ })
  await expect(h3, 'the rotate modal never opened').toBeVisible()
  // The panel is three levels above the h3; its parent is the fixed scrim.
  const panel = h3.locator('xpath=../../..')
  const scrim = panel.locator('xpath=..')
  await settle(page, panel)
  expect((await styles(scrim, ['position']))['position'], 'the panel parent is the fixed scrim').toBe('fixed')

  const look = await check(panel, 'rotate panel', {
    'box-shadow': 'none',
    'background-color': C.white,
    'border-top-width': '1px',
    'border-top-color': C.input,
  })
  await corners(panel, 'rotate panel', R.lg)
  const box = await panel.boundingBox()
  const vp = page.viewportSize()!
  expect(box, 'rotate panel has no box').toBeTruthy()
  expect(within({ x: 0, y: 0, width: vp.width, height: vp.height }, box!, 'rotate panel in viewport'), 'rotate panel fits the viewport').toEqual([])

  const s = await styles(scrim, ['background-color', 'backdrop-filter'])
  const c = parseColor(s['background-color'])
  SURFACE_RGB.forEach((v, i) => expect(Math.abs([c.r, c.g, c.b][i] - v), `modal scrim channel ${i}`).toBeLessThanOrEqual(1))
  expect(Math.abs(c.a - 0.55), 'modal scrim alpha').toBeLessThanOrEqual(0.01)
  expect(s['backdrop-filter'], 'modal scrim backdrop-filter').toBe('blur(6px)')
  await font(h3, 'rotate heading', { family: 'Manrope', weight: '700', size: '18px' })

  await attachJson(testInfo, 'ops-04-rotate-modal', { look, scrim: s })
  await attachShot(page, testInfo, 'rotate-modal')
  await scrim.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(h3, 'Cancel left the rotate modal open').toBeHidden()
  expect(errors, errorsOf(errors)).toEqual([])
})

test('OPS-04 toast: the Rotate-key toast on --surface with the card shadow', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)
  await open(page, 'api')
  const rotate = async () => {
    await pad(page).getByRole('button', { name: 'Rotate' }).first().click()
    await page.getByRole('button', { name: 'Rotate key', exact: true }).click()
  }
  await rotate()
  const msg = page.getByText(/^Rotated /)
  // One read, first: the toast clears itself after 3.4 s.
  const read = await msg.locator('xpath=..').evaluate((el) => {
    const cs = getComputedStyle(el)
    const icon = getComputedStyle(el.firstElementChild as Element)
    return {
      cls: el.className,
      background: cs.backgroundColor,
      color: cs.color,
      shadow: cs.boxShadow,
      radii: [cs.borderTopLeftRadius, cs.borderTopRightRadius, cs.borderBottomRightRadius, cs.borderBottomLeftRadius],
      icon: icon.color,
    }
  })
  expect(read.cls, 'toast scope class').toContain('asc-dark')
  expect(read.background, 'toast background (--surface)').toBe(C.surface)
  expect(read.color, 'toast text (--surface-foreground)').toBe(C.surfaceForeground)
  expect(read.radii, 'toast corners').toEqual(Array(4).fill(R.md))
  expect(read.shadow, 'toast shadow (--shadow-card)').toBe(SHADOW_CARD)
  expect(read.icon, 'toast ok icon (--teal-300)').toBe(C.teal300)

  // The screenshot comes last, after the 200 ms rise; a toast that already cleared is raised again, and never re-read.
  if (!(await msg.isVisible())) await rotate()
  await expect(msg, 'the toast did not draw for the screenshot').toBeVisible()
  await settle(page, msg.locator('xpath=..'))
  await attachJson(testInfo, 'ops-04-toast', read)
  await attachShot(page, testInfo, 'toast')
  expect(errors, errorsOf(errors)).toEqual([])
})

// ---------------------------------------------------------------- OPS-05
// Fixture-free: reads whatever posts the PR environment holds.

test('OPS-05 blog list: header, filters and table hold together at every wide width', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInOps(page)
  await navButton(page, /^Blog/).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Blog', exact: true }), 'Blog drew no h1').toBeVisible()
  await expect(firstRow(page), 'the Blog list drew no post row').toBeVisible({ timeout: 15_000 })
  await settle(page, pad(page))

  const table = page.locator('[data-blog-table]')
  const cells = (row: Locator) => row.locator('xpath=./*')
  const widths = await atWidths(page, 'OPS-05 blog list', async () => {
    const r = await boxes(page, {
      title: pad(page).getByRole('heading', { level: 1, name: 'Blog', exact: true }).locator('xpath=..'),
      view: page.getByRole('link', { name: 'View blog' }),
      add: page.getByRole('button', { name: 'New post' }),
      select: page.getByLabel('Category', { exact: true }),
      search: page.locator('div.ops-input').filter({ has: page.getByPlaceholder('Search titles and summaries…') }),
    })
    const problems = [
      ...leftOf(r.title, r.view, 'title block / View blog'),
      ...leftOf(r.view, r.add, 'View blog / New post'),
      ...allApart([r.title, r.view, r.add], 'header'),
      ...leftOf(r.select, r.search, 'category select / search'),
      ...sameCentre(r.select, r.search, 'category select / search'),
      ...apart(r.select, r.search, 'category select / search'),
    ]

    const head = await boxList(page, cells(table.locator('> div').first()), 'header label', 6)
    const rowCount = Math.min(3, await page.locator('.ops-row').count())
    const rows: Rect[][] = []
    for (let i = 0; i < rowCount; i++) {
      const row = page.locator('.ops-row').nth(i)
      const cs = await boxList(page, cells(row), `row ${i} cell`, 6)
      rows.push(cs)
      cs.slice(0, 5).forEach((c, k) => problems.push(...sameLeft(c, head[k], `row ${i} cell ${k} / header label`)))
      problems.push(...withinX(head[5], cs[5], `row ${i} pin button / Pin column`))
      const lines = await boxList(page, cells(row).first().locator('> span'), `row ${i} Post line`, 2)
      lines.forEach((l, k) => problems.push(...within(cs[0], l, `row ${i} Post line ${k} / Post cell`)))
    }

    const over = await table.evaluate((el) => el.scrollWidth - el.clientWidth)
    if (over > 1) problems.push(`table frame scrolls sideways by ${over}px`)
    problems.push(...(await noSidewaysScroll(page, 'blog list')))
    return { problems, rects: { ...r, head, rows } }
  })
  await attachJson(testInfo, 'ops-05-blog-list', { widths })
  expect(errors, errorsOf(errors)).toEqual([])
})

test('OPS-05 blog categories list: rows hold together at every wide width', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInOps(page)
  await navButton(page, /^Blog/).click()
  await expect(firstRow(page), 'the Blog list drew no post row').toBeVisible({ timeout: 15_000 })
  const tab = pad(page).getByRole('button', { name: /Categories/ })
  await tab.click()
  const rowsLoc = page.locator('[data-cat-row]')
  await expect(rowsLoc.first(), 'the Categories tab drew no category row').toBeVisible({ timeout: 15_000 })
  await settle(page, pad(page))

  const widths = await atWidths(page, 'OPS-05 blog categories list', async () => {
    const r = await boxes(page, {
      tabs: tab.locator('xpath=..'),
      note: page.locator('[data-cat-head] > span').first(),
      add: page.getByRole('button', { name: 'Add category' }),
      frame: page.locator('[data-blog-cats]'),
    })
    const problems = [
      ...below(r.tabs, r.frame, 'tab row / list frame'),
      ...leftOf(r.note, r.add, 'note / Add category'),
      ...sameCentre(r.note, r.add, 'note / Add category'),
      ...apart(r.note, r.add, 'note / Add category'),
    ]

    const rowCount = Math.min(3, await rowsLoc.count())
    const all: Rect[] = []
    for (let i = 0; i < rowCount; i++) {
      const row = rowsLoc.nth(i)
      const [box] = await boxList(page, row, `row ${i}`, 1)
      all.push(box)
      const cells = await boxList(page, row.locator('xpath=./div/*'), `row ${i} cell`, 5)
      const [num, tile, name, count, group] = cells
      problems.push(...leftOf(num, tile, `row ${i} number / tile`), ...leftOf(tile, name, `row ${i} tile / name cell`), ...leftOf(name, count, `row ${i} name cell / count`), ...leftOf(count, group, `row ${i} count / buttons`))
      cells.forEach((c, k) => problems.push(...within(box, c, `row ${i} cell ${k} / row`)))
      const buttons = await boxList(page, row.getByRole('button'), `row ${i} button`, 4)
      problems.push(...allApart(buttons, `row ${i} buttons`))
      buttons.forEach((b, k) => problems.push(...within(box, b, `row ${i} button ${k} / row`)))
      const lines = await boxList(page, row.locator('xpath=./div/*[3]/div'), `row ${i} name line`, 2)
      lines.forEach((l, k) => problems.push(...within(name, l, `row ${i} name cell line ${k} / name cell`)))
    }
    problems.push(...allApart(all, 'rows'))
    problems.push(...(await noSidewaysScroll(page, 'blog categories list')))
    return { problems, rects: { ...r, rows: all } }
  })
  await attachJson(testInfo, 'ops-05-blog-categories-list', { widths })
  expect(errors, errorsOf(errors)).toEqual([])
})

test('OPS-05 blog editor: the two panes fill the main column side by side at every wide width', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInOps(page)
  await navButton(page, /^Blog/).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Blog', exact: true }), 'Blog drew no h1').toBeVisible()
  await page.getByRole('button', { name: 'New post' }).click()
  const editor = page.locator('[data-blog-editor]')
  await expect(editor, 'New post opened no editor').toBeVisible()
  await settle(page, editor)

  const scroller = editor.locator('xpath=..')
  const panes = page.locator('[data-bscroll]')
  const fields = editor.locator('[data-blog-fields]')
  const widths = await atWidths(page, 'OPS-05 blog editor', async () => {
    const r = await boxes(page, { main: page.locator('main'), form: panes.nth(0), preview: panes.nth(1) })
    const banner_ = await boxes(page, { banner: banner(page) })
    const cells = await boxList(page, fields.locator('> label'), 'field cell', 3)
    const problems = [
      ...leftOf(r.form, r.preview, 'form pane / preview pane'),
      ...sameTop([r.form, r.preview], 'panes'),
      ...apart(r.form, r.preview, 'form pane / preview pane'),
      ...within(r.main, r.form, 'form pane in main column'),
      ...within(r.main, r.preview, 'preview pane in main column'),
      ...below(banner_.banner, r.form, 'form pane / env banner'),
      ...below(banner_.banner, r.preview, 'preview pane / env banner'),
      ...allApart(cells, 'Category / Author / Publish date'),
    ]
    const sc = await scroller.evaluate((el) => ({ overflowY: getComputedStyle(el).overflowY, over: el.scrollHeight - el.clientHeight }))
    if (sc.overflowY !== 'auto') problems.push(`main column scroller overflow-y is ${sc.overflowY}`)
    if (sc.over > 1) problems.push(`main column scrolls by ${sc.over}px`)
    problems.push(...(await noSidewaysScroll(page, 'blog editor', editor)))
    return { problems, rects: { ...r, ...banner_, cells } }
  })
  await attachJson(testInfo, 'ops-05-blog-editor', { widths })
  expect(errors, errorsOf(errors)).toEqual([])
})

test('OPS-05 blog preview: the article and the card fit the preview pane at every wide width', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInOps(page)
  await navButton(page, /^Blog/).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Blog', exact: true }), 'Blog drew no h1').toBeVisible()
  await page.getByRole('button', { name: 'New post' }).click()
  const editor = page.locator('[data-blog-editor]')
  await expect(editor, 'New post opened no editor').toBeVisible()
  await page.getByPlaceholder('Write a paragraph…').fill('A paragraph that the preview shows under both headings.')
  for (const text of ['What the mandate changes for invoicing teams', 'How to prepare before the deadline']) {
    await editor.getByRole('button', { name: 'Heading', exact: true }).click()
    await page.getByPlaceholder('Section heading').last().fill(text)
  }
  await settle(page, editor)

  const pane = page.locator('[data-bscroll]').nth(1)
  const entries = pane.locator('[data-preview-toc-entry]')
  const widths = await atWidths(page, 'OPS-05 blog preview article', async () => {
    const r = await boxes(page, { pane, cover: pane.locator('[data-preview-cover]') })
    const toc = await boxList(page, entries, 'TOC entry', 2)
    const problems = [...within(r.pane, r.cover, 'cover box in preview pane'), ...toc.flatMap((t, i) => within(r.pane, t, `TOC entry ${i} in preview pane`))]
    return { problems, rects: { ...r, toc } }
  })

  await pane.getByRole('button', { name: 'Card', exact: true }).click()
  const card = pane.locator('[data-preview-card]')
  await expect(card, 'the Card tab drew no card').toBeVisible()
  await settle(page, card)
  const cardWidths = await atWidths(page, 'OPS-05 blog preview card', async () => {
    const r = await boxes(page, { pane, card })
    return { problems: within(r.pane, r.card, 'card in preview pane'), rects: r }
  })
  await attachJson(testInfo, 'ops-05-blog-preview', { widths, cardWidths })
  expect(errors, errorsOf(errors)).toEqual([])
})

test('OPS-05 blog action bar: actions stay inside the bar at every wide width', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInOps(page)
  await navButton(page, /^Blog/).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Blog', exact: true }), 'Blog drew no h1').toBeVisible()
  await page.getByRole('button', { name: 'New post' }).click()
  const editor = page.locator('[data-blog-editor]')
  await expect(editor, 'New post opened no editor').toBeVisible()
  await page.getByPlaceholder('What structured e-invoicing changes for…').fill('Action bar probe, never saved')
  const bar = editor.locator('xpath=./div[1]')
  await expect(bar.getByText('UNSAVED CHANGES', { exact: true }), 'typing a title left the editor clean').toBeVisible()
  await settle(page, editor)

  const widths = await atWidths(page, 'OPS-05 blog action bar', async () => {
    const r = await boxes(page, {
      bar,
      save: bar.getByRole('button', { name: 'Save draft', exact: true }),
      publish: bar.getByRole('button', { name: 'Publish', exact: true }),
      badge: bar.getByText('DRAFT', { exact: true }),
      state: bar.getByText('UNSAVED CHANGES', { exact: true }),
    })
    const problems = [
      ...within(r.bar, r.save, 'Save draft in the bar'),
      ...within(r.bar, r.publish, 'Publish in the bar'),
      ...apart(r.save, r.publish, 'Save draft / Publish'),
      ...apart(r.save, r.badge, 'Save draft / status badge'),
      ...apart(r.publish, r.badge, 'Publish / status badge'),
      ...apart(r.save, r.state, 'Save draft / save state'),
      ...apart(r.publish, r.state, 'Publish / save state'),
    ]
    return { problems, rects: r }
  })

  await editor.getByRole('button', { name: 'All posts' }).click()
  const prompt = bar.getByText('You have unsaved changes.', { exact: true })
  await expect(prompt, 'Back with unsaved changes asked nothing').toBeVisible()
  const leaveWidths = await atWidths(page, 'OPS-05 blog leave confirm', async () => {
    const r = await boxes(page, {
      bar,
      prompt,
      keep: bar.getByRole('button', { name: 'Keep editing', exact: true }),
      discard: bar.getByRole('button', { name: 'Discard changes', exact: true }),
      badge: bar.getByText('DRAFT', { exact: true }),
      state: bar.getByText('UNSAVED CHANGES', { exact: true }),
    })
    const problems = [
      ...within(r.bar, r.prompt, 'leave prompt in the bar'),
      ...within(r.bar, r.keep, 'Keep editing in the bar'),
      ...within(r.bar, r.discard, 'Discard changes in the bar'),
      ...allApart([r.prompt, r.keep, r.discard, r.badge, r.state], 'leave confirm / badge / save state'),
    ]
    return { problems, rects: r }
  })
  await attachJson(testInfo, 'ops-05-blog-action-bar', { widths, leaveWidths })
  expect(errors, errorsOf(errors)).toEqual([])
})

// ---------------------------------------------------------------- OPS-06
// Writes one post of its own through the deployed console; reads nothing of any tenant.

const BLOG = '/api/notifications/v1/staff/blog'
const BLOG_POSTS = /\/api\/notifications\/v1\/staff\/blog\/posts(?:[/?]|$)/
const AUTH_REFRESH = /\/auth\/refresh(?:[/?]|$)/
const KEPT_KEY = 'invoice-os.ops-blog-draft' // frontend/ops-console/src/blog.ts KEPT_KEY
const BAD_IMAGE = 'Choose a JPG or PNG image'

type ServerPost = { id: string; title: string; status: string; featured: boolean; cover: { content_type: string } | null }

const staffToken = async (): Promise<string> => {
  const a = await staffAccount()
  return (await signInSession(a.email, a.password)).access_token
}
const bearer = (token: string) => ({ Authorization: `Bearer ${token}` })
const serverPosts = async (token: string): Promise<ServerPost[]> => {
  const res = await rawFetch(`${BLOG}/posts`, { headers: bearer(token) })
  if (res.status !== 200) throw new Error(`GET posts answered ${res.status}`)
  return (res.body as { posts: ServerPost[] }).posts
}

// Console errors, minus the deliberate non-2xx the test routes or provokes.
function blogErrors(page: Page, ...droppers: Dropper[]): string[] {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    if (droppers.some((drop) => drop(msg.text(), msg.location().url))) return
    errors.push(msg.text())
  })
  page.on('pageerror', (err) => {
    errors.push(`pageerror: ${err.message}`)
  })
  return errors
}

// Width of a JPEG from its SOF0/SOF1/SOF2 marker.
function jpegWidth(b: Buffer): number {
  if (b[0] !== 0xff || b[1] !== 0xd8) throw new Error('not a JPEG')
  let i = 2
  while (i + 9 < b.length) {
    if (b[i] !== 0xff) throw new Error(`no marker at byte ${i}`)
    const m = b[i + 1]
    if (m === 0xff) {
      i += 1
      continue
    }
    if (m >= 0xc0 && m <= 0xc2) return b.readUInt16BE(i + 7)
    i += 2 + b.readUInt16BE(i + 2)
  }
  throw new Error('no SOF marker')
}

// 401 or 500 for a cross-origin call, with the CORS header the browser needs to read it.
const fulfillStatus = (route: Route, status: number, body: unknown) =>
  route.fulfill({
    status,
    contentType: 'application/json',
    headers: { 'access-control-allow-origin': route.request().headers()['origin'] ?? '*' },
    body: JSON.stringify(body),
  })

const bar = (page: Page) => page.locator('[data-blog-editor]')
const editorLeft = (page: Page) => page.locator('[data-bscroll]').first()
const editorPreview = (page: Page) => page.locator('[data-bscroll]').nth(1)
const toastOf = (page: Page, text: string) => page.getByText(text, { exact: true })
// The toast's icon takes --status-red-text for the red tone and --teal-300 otherwise; read in one evaluate, as the toast times out.
async function expectRedToast(page: Page, text: string): Promise<void> {
  const toast = toastOf(page, text)
  await expect(toast, `no "${text}" toast`).toBeVisible()
  const tone = await toast.evaluate((el) => {
    const root = el.parentElement!
    const probe = document.createElement('span')
    probe.style.color = 'var(--status-red-text)'
    root.appendChild(probe)
    const red = getComputedStyle(probe).color
    probe.remove()
    return { icon: getComputedStyle(root.firstElementChild as Element).color, red }
  })
  expect(tone.icon, `the "${text}" toast is not red`).toBe(tone.red)
}
const titleInput = (page: Page) => page.getByPlaceholder(/^What structured e-invoicing changes for/)
const summaryInput = (page: Page) => page.getByPlaceholder(/^One or two sentences\./)

async function openNewPost(page: Page): Promise<void> {
  await navButton(page, /^Blog/).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Blog', exact: true }), 'Blog drew no h1').toBeVisible()
  await expect(page.locator('[data-blog-table]'), 'the Blog list never finished loading').toBeVisible({ timeout: 15_000 })
  await page.getByRole('button', { name: 'New post' }).click()
  await expect(titleInput(page), 'New post opened no editor').toBeVisible()
}

test.describe('OPS-06 blog writes', () => {
  // The post this test created and the featured post it found; the afterEach undoes both.
  let created: { id: string | null }
  let featuredBefore: string | null
  const track = (page: Page) =>
    page.on('response', (res) => {
      if (res.request().method() !== 'POST' || !res.ok() || !BLOG_POSTS.test(res.url())) return
      void res.json().then(
        (p: { id?: string }) => {
          if (p.id) created.id = p.id
        },
        () => {},
      )
    })

  test.beforeEach(async () => {
    created = { id: null }
    featuredBefore = (await serverPosts(await staffToken())).find((p) => p.featured)?.id ?? null
  })

  test.afterEach(async () => {
    const token = await staffToken()
    const posts = await serverPosts(token)
    const own = created.id ? posts.find((p) => p.id === created.id) : undefined
    if (own?.featured) await rawFetch(`${BLOG}/posts/${own.id}/featured`, { method: 'PUT', headers: bearer(token), body: { featured: false } })
    if (featuredBefore && posts.some((p) => p.id === featuredBefore && !p.featured)) {
      await rawFetch(`${BLOG}/posts/${featuredBefore}/featured`, { method: 'PUT', headers: bearer(token), body: { featured: true } })
    }
    if (own) await rawFetch(`${BLOG}/posts/${own.id}`, { method: 'DELETE', headers: bearer(token) })
  })

  test('OPS-06 blog journey: list → new post → blocks → preview → save draft → publish → unpublish → delete', async ({ page }) => {
    test.setTimeout(180_000)
    const errors = blogErrors(page, expectedStatusDropper(page, 422, BLOG_POSTS))
    track(page)
    const title = `e2e blog ${randomBytes(4).toString('hex')}`
    const summary = 'Written by the deployed blog journey.'
    await signInOps(page)
    await openNewPost(page)
    await titleInput(page).fill(title)

    await page.getByRole('button', { name: 'Publish', exact: true }).click()
    const empty = 'Add a summary. It shows on cards and under the title.'
    await expect(page.getByText(empty, { exact: true }), 'the 422 marked no Summary message').toBeVisible()
    await expectRedToast(page, 'Fix the highlighted fields to publish')
    await expect(summaryInput(page), 'Summary border (--destructive)').toHaveCSS('border-top-color', await resolveColor(bar(page), '--destructive'))

    await summaryInput(page).fill(summary)
    for (const label of ['Heading', 'Heading', 'List', 'Callout', 'Demo']) await page.getByRole('button', { name: label, exact: true }).click()
    const left = editorLeft(page)
    await left.getByPlaceholder('Section heading').nth(0).fill('First heading')
    await left.getByPlaceholder('Section heading').nth(1).fill('Second heading')
    await left.getByPlaceholder('One item per line').fill('First item\nSecond item')
    await left.getByRole('button', { name: 'Deadline', exact: true }).click()
    await left.getByPlaceholder('Callout title').fill('Filing window')
    await left.getByPlaceholder('Callout text').fill('Submit before the cut-off.')

    const preview = editorPreview(page)
    await expect(preview.getByText('On this page', { exact: true })).toBeVisible()
    for (const h of ['First heading', 'Second heading']) await expect(preview.getByRole('heading', { name: h, level: 2 })).toBeVisible()
    for (const item of ['First item', 'Second item']) await expect(preview.getByText(item, { exact: true })).toBeVisible()
    await expect(preview.getByText('Deadline', { exact: true })).toBeVisible()
    await expect(preview.getByText('Filing window', { exact: true })).toBeVisible()
    await expect(preview.getByText('FEATURE LIBRARY · DEMO', { exact: true })).toBeVisible()
    await preview.getByRole('button', { name: 'Card', exact: true }).click()
    await expect(preview.getByText('Read the post')).toBeVisible()

    const file = left.locator('input[type=file]')
    const bad = toastOf(page, BAD_IMAGE)
    await file.setInputFiles({ name: 'notes.txt', mimeType: 'text/plain', buffer: Buffer.from('not an image') })
    await expectRedToast(page, BAD_IMAGE)
    await expect(bad).toBeHidden({ timeout: 6_000 })
    await file.setInputFiles({ name: 'broken.png', mimeType: 'image/png', buffer: Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47]), Buffer.alloc(32)]) })
    await expectRedToast(page, BAD_IMAGE)
    await expect(left.getByRole('img', { name: 'Cover image' }), 'a rejected file left a thumbnail').toHaveCount(0)
    const png = await page.evaluate(() => {
      const c = document.createElement('canvas')
      c.width = 2000
      c.height = 1000
      const ctx = c.getContext('2d')!
      ctx.fillStyle = '#336699'
      ctx.fillRect(0, 0, 2000, 1000)
      ctx.fillStyle = '#ffcc00'
      ctx.fillRect(500, 250, 1000, 500)
      return c.toDataURL('image/png').split(',')[1]
    })
    await file.setInputFiles({ name: 'cover.png', mimeType: 'image/png', buffer: Buffer.from(png, 'base64') })
    await expect(left.getByRole('img', { name: 'Cover image' }), 'the 2000x1000 PNG drew no thumbnail').toBeVisible()

    await page.getByRole('button', { name: 'Save draft', exact: true }).click()
    await expect(toastOf(page, 'Draft saved')).toBeVisible()
    await expect(bar(page).getByText('ALL CHANGES SAVED', { exact: true })).toBeVisible()
    await expect.poll(() => created.id, 'the create response named no id').not.toBeNull()
    const id = created.id!
    await expect(page.getByText(`blog.ascomply.com/${id}`, { exact: true })).toBeVisible()

    const token = await staffToken()
    const saved = (await serverPosts(token)).find((p) => p.id === id)
    expect(saved?.status, 'saved post status').toBe('draft')
    expect(saved?.cover?.content_type, 'stored cover type').toBe('image/jpeg')
    const cover = await fetch(`${apiBase()}${BLOG}/posts/${id}/cover`, { headers: bearer(token) })
    expect(cover.status, 'GET cover').toBe(200)
    const width = jpegWidth(Buffer.from(await cover.arrayBuffer()))
    expect(width, 'stored cover width').toBeGreaterThan(0)
    expect(width, 'stored cover width').toBeLessThanOrEqual(1600)

    await page.getByRole('button', { name: 'Publish', exact: true }).click()
    await expect(toastOf(page, 'Published to blog.ascomply.com')).toBeVisible()
    await expect(bar(page).getByText('PUBLISHED', { exact: true })).toBeVisible()
    await expect(bar(page).getByRole('button', { name: 'Update post', exact: true })).toBeVisible()
    await page.getByRole('button', { name: 'Unpublish', exact: true }).click()
    await expect(toastOf(page, 'Unpublished. The post is now a draft')).toBeVisible()

    await titleInput(page).click()
    await titleInput(page).pressSequentially(' b')
    await bar(page).getByRole('button', { name: 'All posts' }).click()
    await expect(toastOf(page, 'You have unsaved changes.')).toBeVisible()
    await bar(page).getByRole('button', { name: 'Keep editing', exact: true }).click()
    await page.getByRole('button', { name: 'Save draft', exact: true }).click()
    await expect(toastOf(page, 'Draft saved')).toBeVisible()
    await bar(page).getByRole('button', { name: 'All posts' }).click()

    await page.getByLabel('Search posts').fill(title)
    const row = page.locator('.ops-row').filter({ hasText: title })
    await expect(row, 'the saved post is not in the list').toHaveCount(1)
    await row.getByRole('button', { name: 'Pin as featured post' }).click()
    await expect(toastOf(page, 'Pinned as the featured post')).toBeVisible()
    await expect(row.getByRole('button', { name: 'Unpin featured post' })).toBeVisible()
    await row.getByRole('button', { name: 'Unpin featured post' }).click()
    await expect(toastOf(page, 'Unpinned. The newest post leads the blog home')).toBeVisible()
    await expect(row.getByRole('button', { name: 'Pin as featured post' })).toBeVisible()

    await row.click()
    await bar(page).getByRole('button', { name: 'Delete', exact: true }).click()
    await expect(toastOf(page, 'Delete this post? It is removed from the blog.')).toBeVisible()
    await bar(page).getByRole('button', { name: 'Delete post', exact: true }).click()
    await expect(toastOf(page, 'Post deleted')).toBeVisible()
    await expect(page.locator('.ops-row').filter({ hasText: title }), 'the deleted post is still listed').toHaveCount(0)
    expect((await serverPosts(token)).some((p) => p.id === id), 'the server still holds the post').toBe(false)
    expect(errors, errorsOf(errors)).toEqual([])
  })

  test('OPS-06 blog renewed session: one 401 on save renews the session in place and the save completes', async ({ page }) => {
    const errors = blogErrors(page, expectedStatusDropper(page, 401, BLOG_POSTS))
    track(page)
    const title = `e2e blog ${randomBytes(4).toString('hex')}`
    await signInOps(page)
    await openNewPost(page)
    await titleInput(page).fill(title)
    await summaryInput(page).fill('Saved after one renewed session.')

    let refused = false
    await page.route(BLOG_POSTS, (route) => {
      if (route.request().method() !== 'POST' || refused) return route.fallback()
      refused = true
      return fulfillStatus(route, 401, { error: 'unauthorized' })
    })
    // The whole record: the refresh token rotates on every renewal, while an access token minted in the same second can repeat.
    const stored = () => page.evaluate((key) => localStorage.getItem(key), CONSOLE_SESSION_KEY.ops)
    const before = await stored()
    expect(before, 'no stored session before the save').not.toBeNull()
    await page.getByRole('button', { name: 'Save draft', exact: true }).click()
    await expect(toastOf(page, 'Draft saved')).toBeVisible()
    expect(refused, 'the first save was never refused').toBe(true)
    expect(new URL(page.url()).origin, 'the console left the ops origin').toBe(new URL(consoleUrl('ops')).origin)
    expect(await stored(), 'the stored session was not renewed').not.toBe(before)
    expect(await page.evaluate((key) => localStorage.getItem(key), KEPT_KEY), 'a kept copy was stored').toBeNull()
    expect(errors, errorsOf(errors)).toEqual([])
  })

  test('OPS-06 blog lost session: a 401 on save keeps the post and offers it after sign-in', async ({ page }) => {
    test.setTimeout(120_000)
    const errors = blogErrors(page, expectedStatusDropper(page, 401, BLOG_POSTS), expectedStatusDropper(page, 401, AUTH_REFRESH))
    track(page)
    const title = `e2e blog ${randomBytes(4).toString('hex')}`
    const summary = 'Kept across a lost session.'
    const account = await staffAccount()
    await signInOps(page)
    await openNewPost(page)
    await titleInput(page).fill(title)
    await summaryInput(page).fill(summary)

    const refuse = (route: Route) => (route.request().method() === 'POST' ? fulfillStatus(route, 401, { error: 'unauthorized' }) : route.fallback())
    await page.route(BLOG_POSTS, refuse)
    await page.route(AUTH_REFRESH, refuse)
    await page.getByRole('button', { name: 'Save draft', exact: true }).click()
    const landing = new URL(resolveTarget('LANDING_URL')).origin
    await page.waitForURL((u) => u.origin === landing, { timeout: 30_000 })
    await page.unrouteAll({ behavior: 'ignoreErrors' })

    const opsOrigin = new URL(consoleUrl('ops')).origin
    const stateOf = await page.context().storageState()
    const kept = stateOf.origins.find((o) => o.origin === opsOrigin)?.localStorage.find((e) => e.name === KEPT_KEY)
    expect(kept, 'no kept copy at the ops origin').toBeDefined()
    expect((JSON.parse(kept!.value) as { draft: { title: string } }).draft.title, 'kept title').toBe(title)

    const pair = await signInSession(account.email, account.password)
    await page.context().addInitScript(
      ({ origin, key, value }) => {
        if (location.origin !== origin || sessionStorage.getItem('e2e.staff-reseeded')) return
        sessionStorage.setItem('e2e.staff-reseeded', '1')
        localStorage.setItem(key, value)
      },
      { origin: opsOrigin, key: CONSOLE_SESSION_KEY.ops, value: JSON.stringify({ v: 2, token: pair.access_token, refresh_token: pair.refresh_token }) },
    )
    await page.goto(consoleUrl('ops'))
    await expect(titleInput(page), 'the kept draft did not reopen on Blog').toHaveValue(title, { timeout: 30_000 })
    await expect(summaryInput(page)).toHaveValue(summary)
    await expect(bar(page).getByText('UNSAVED CHANGES', { exact: true })).toBeVisible()
    expect(await page.evaluate((key) => localStorage.getItem(key), KEPT_KEY), 'the kept copy outlived its reopening').toBeNull()

    await page.getByRole('button', { name: 'Save draft', exact: true }).click()
    await expect(toastOf(page, 'Draft saved')).toBeVisible()
    expect(await page.evaluate((key) => localStorage.getItem(key), KEPT_KEY), 'the kept copy came back').toBeNull()
    expect(errors, errorsOf(errors)).toEqual([])
  })

  test('OPS-06 blog failed save: a 500 leaves the editor dirty with a red toast', async ({ page }) => {
    const errors = blogErrors(page, expectedStatusDropper(page, 500, BLOG_POSTS))
    track(page)
    const title = `e2e blog ${randomBytes(4).toString('hex')}`
    await signInOps(page)
    await openNewPost(page)
    await titleInput(page).fill(title)
    await page.route(BLOG_POSTS, (route) => (route.request().method() === 'POST' ? fulfillStatus(route, 500, { error: 'internal error' }) : route.fallback()))
    await page.getByRole('button', { name: 'Save draft', exact: true }).click()
    await expectRedToast(page, 'internal error')
    await expect(bar(page).getByText('UNSAVED CHANGES', { exact: true })).toBeVisible()
    expect((await serverPosts(await staffToken())).some((p) => p.title === title), 'the failed save created a post').toBe(false)
    expect(errors, errorsOf(errors)).toEqual([])
  })
})
