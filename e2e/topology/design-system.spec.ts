// The deployed proof of the v2/v1 split, the landing frame's geometry and the Problem, Solution and Platform sections; one topology spec on purpose, a recorded deviation from docs/e2e-convention.md.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { test, expect, type Locator, type Page, type TestInfo } from '@playwright/test'
import { collectErrors, signInAs } from '../personaSession'
import type { PersonaId } from '../personas'
import { seedConsent } from '../smoke/landingConsent'
import { resolveTarget } from '../targets'
import { enclosesRect, rectsOverlap, WIDE_WIDTHS, type Rect } from './layout'

const LANDING_URL = resolveTarget('LANDING_URL')

// Every family string goes through here, so a quoting mismatch cannot fake a pass.
function familyName(raw: string): string {
  return raw.replace(/["']/g, '').trim()
}

const firstFamily = (raw: string | null): string | null => (raw === null ? null : familyName(raw.split(',')[0]))

// The v1 --accent, from disk. Value text only; the trailing comment is not part of it.
function v1Accent(): string {
  const css = readFileSync(fileURLToPath(new URL('../../packages/design-tokens/tokens/colors.css', import.meta.url)), 'utf8')
  const m = css.match(/--accent:\s*([^;]+);/)
  if (!m) throw new Error('--accent not found in packages/design-tokens/tokens/colors.css')
  return m[1].trim()
}

type RawProbe = {
  url: string
  accent: string
  h1Family: string | null
  groundFamily: string | null
  groundBackground: string | null
  bodyFamily: string
  bodyBackground: string
  faces: { family: string; status: string }[]
  appCount: number
}

type Probe = {
  url: string
  accent: string
  h1Family: string | null
  groundFamily: string | null
  groundBackground: string | null
  bodyFamily: string | null
  bodyBackground: string
  faces: { family: string; status: string }[]
  appCount: number
}

// Fonts settled and two frames painted before any read.
async function probe(page: Page): Promise<Probe> {
  await expect(page.locator('h1:visible').first()).toBeVisible()
  const raw: RawProbe = await page.evaluate(async () => {
    await document.fonts.ready
    await new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r())))
    const visible = (el: Element) => {
      const r = el.getBoundingClientRect()
      return r.width > 0 && r.height > 0 && getComputedStyle(el).visibility === 'visible'
    }
    const h1 = [...document.querySelectorAll('h1')].find(visible) ?? null
    let ground: Element | null = h1
    while (ground) {
      const bg = getComputedStyle(ground).backgroundColor
      if (bg !== 'transparent' && bg !== 'rgba(0, 0, 0, 0)') break
      ground = ground.parentElement
    }
    return {
      url: location.href,
      accent: getComputedStyle(document.documentElement).getPropertyValue('--accent').trim(),
      h1Family: h1 ? getComputedStyle(h1).fontFamily : null,
      groundFamily: ground ? getComputedStyle(ground).fontFamily : null,
      groundBackground: ground ? getComputedStyle(ground).backgroundColor : null,
      bodyFamily: getComputedStyle(document.body).fontFamily,
      bodyBackground: getComputedStyle(document.body).backgroundColor,
      faces: [...document.fonts].map((f) => ({ family: f.family, status: f.status })),
      appCount: document.querySelectorAll('.asc-app').length,
    }
  })
  return {
    ...raw,
    h1Family: firstFamily(raw.h1Family),
    groundFamily: firstFamily(raw.groundFamily),
    bodyFamily: firstFamily(raw.bodyFamily),
    faces: raw.faces.map((f) => ({ family: familyName(f.family), status: f.status })),
  }
}

async function attachProbe(testInfo: TestInfo, name: string, p: Probe): Promise<void> {
  await testInfo.attach(`${name}-probe.json`, { body: JSON.stringify(p, null, 2), contentType: 'application/json' })
}

const families = (p: Probe, status?: string) =>
  new Set(p.faces.filter((f) => status === undefined || f.status === status).map((f) => f.family))

const SHOTS = [
  { width: 1440, height: 900 },
  { width: 390, height: 844 },
] as const

for (const path of ['/', '/privacy']) {
  test(`landing ${path} reads v2: Manrope, #f5bc88, cream ground, no app layer`, async ({ page }, testInfo) => {
    const errors = collectErrors(page)
    const res = await page.goto(`${LANDING_URL}${path}`)
    expect(res?.ok(), `${path} returned HTTP ${res?.status()}`).toBeTruthy()

    const p = await probe(page)
    await attachProbe(testInfo, `landing${path === '/' ? '' : path.replace('/', '-')}`, p)

    expect(p.h1Family, 'h1 first family').toBe('Manrope')
    expect(p.bodyFamily, 'body first family').toBe('Manrope')
    expect(p.accent.toLowerCase(), '--accent').toBe('#f5bc88')
    expect(p.bodyBackground, 'body background-color').toBe('rgb(250, 248, 242)')

    expect(p.appCount, 'the landing must carry no .asc-app element').toBe(0)
    expect(families(p, 'loaded'), 'a loaded Manrope face').toContain('Manrope')
    for (const banned of ['Inter', 'Fraunces', 'IBM Plex Mono']) {
      expect(families(p), `no ${banned} face on the landing`).not.toContain(banned)
    }

    // Artifacts only; never asserted.
    for (const { width, height } of SHOTS) {
      await page.setViewportSize({ width, height })
      await page.evaluate(async () => {
        await document.fonts.ready
        await new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r())))
      })
      const png = await page.screenshot()
      await testInfo.attach(`landing${path === '/' ? '' : path.replace('/', '-')}-${width}x${height}.png`, {
        body: png,
        contentType: 'image/png',
      })
    }

    expect(errors, `console errors on ${path}:\n${errors.join('\n')}`).toEqual([])
  })
}

const V1_SURFACES: { id: PersonaId; name: string }[] = [
  { id: 'firm', name: 'app (firm)' },
  { id: 'developer', name: 'ops console' },
  { id: 'support', name: 'support console' },
]

for (const { id, name } of V1_SURFACES) {
  test(`${name} reads v1: Fraunces h1, Inter ground, v1 accent, no Manrope`, async ({ page }, testInfo) => {
    const errors = collectErrors(page)
    await signInAs(page, id)

    const p = await probe(page)
    await attachProbe(testInfo, id, p)

    expect(p.h1Family, 'h1 first family').toBe('Fraunces')
    expect(p.groundFamily, 'ground first family').toBe('Inter')
    expect(p.accent, '--accent equals the v1 value on disk').toBe(v1Accent())
    expect(families(p), 'no Manrope face on a v1 surface').not.toContain('Manrope')
    // Positive control: the face list is not empty for want of a read.
    const loaded = families(p, 'loaded')
    expect(loaded.has('Inter') || loaded.has('Fraunces'), `a loaded Inter or Fraunces face; loaded: ${[...loaded].join(', ')}`).toBe(true)
    expect(p.appCount, 'at least one .asc-app element').toBeGreaterThanOrEqual(1)

    expect(errors, `console errors on ${name}:\n${errors.join('\n')}`).toEqual([])
  })
}

type Measured = { tag: string; text: string; left: number; right: number; top: number; bottom: number }

// Relationships, not pixel values.
async function assertHeaderRow(page: Page, testInfo: TestInfo, widths: number[]): Promise<void> {
  const errors = collectErrors(page)
  const res = await page.goto(`${LANDING_URL}/`)
  expect(res?.ok(), `/ returned HTTP ${res?.status()}`).toBeTruthy()

  for (const width of widths) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
    await expect(page.locator('header').first()).toBeVisible()
    const m = await page.evaluate(async () => {
      await document.fonts.ready
      await new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r())))
      const measure = (el: Element) => {
        const r = el.getBoundingClientRect()
        return {
          tag: el.tagName.toLowerCase(),
          text: (el.textContent ?? '').trim().slice(0, 40),
          left: r.left,
          right: r.right,
          top: r.top,
          bottom: r.bottom,
          shown: r.width > 0 && r.height > 0 && getComputedStyle(el).visibility === 'visible',
        }
      }
      const all = [...document.querySelectorAll('header *')].map(measure).filter((e) => e.shown)
      const row = [...document.querySelectorAll('header a, header button')].map(measure).filter((e) => e.shown)
      return { innerWidth: window.innerWidth, all, row }
    })
    const strip = ({ tag, text, left, right, top, bottom }: Measured & { shown?: boolean }): Measured => ({ tag, text, left, right, top, bottom })
    const all = m.all.map(strip)
    const row = m.row.map(strip).sort((a, b) => a.left - b.left)
    await testInfo.attach(`header-${width}.json`, {
      body: JSON.stringify({ width, innerWidth: m.innerWidth, all, row }, null, 2),
      contentType: 'application/json',
    })

    expect(all.length, `${width}px: visible header elements measured`).toBeGreaterThanOrEqual(3)
    for (const e of all) {
      expect(e.left, `${width}px: <${e.tag}> "${e.text}" left edge`).toBeGreaterThanOrEqual(-1)
      expect(e.right, `${width}px: <${e.tag}> "${e.text}" right edge`).toBeLessThanOrEqual(m.innerWidth + 1)
    }
    for (let i = 0; i < row.length - 1; i++) {
      expect(
        row[i].right,
        `${width}px: "${row[i].text}" overlaps "${row[i + 1].text}"`,
      ).toBeLessThanOrEqual(row[i + 1].left + 1)
    }
  }

  expect(errors, `console errors on the header sweep:\n${errors.join('\n')}`).toEqual([])
}

test('landing header row: inside the viewport and no overlap at 1440, 1240, 1080 and 834', async ({ page }, testInfo) => {
  await assertHeaderRow(page, testInfo, [1440, 1240, 1080, 834])
})

test('landing header row at 390: inside the viewport and no overlap', async ({ page }, testInfo) => {
  await assertHeaderRow(page, testInfo, [390])
})

// Resolved --header-h per width, from packages/design-tokens/v2/tokens/spacing.css (86; 73 at <=767px).
const FRAME_VIEWPORTS = [
  { width: 1440, height: 900, headerH: 86 },
  { width: 834, height: 1112, headerH: 86 },
  { width: 390, height: 844, headerH: 73 },
] as const

// Burger shows at <=1120px (landing.css .a-burger).
const BURGER_MAX = 1120

type Frame = { width: number; height: number }

async function settleFrame(page: Page, { width, height }: Frame): Promise<void> {
  await page.setViewportSize({ width, height })
  await page.evaluate(async () => {
    await document.fonts.ready
    await new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r())))
    window.scrollTo(0, 0)
  })
}

async function openLandingFrame(page: Page): Promise<void> {
  await seedConsent(page, false)
  const res = await page.goto(`${LANDING_URL}/`)
  expect(res?.ok(), `/ returned HTTP ${res?.status()}`).toBeTruthy()
  await expect(page.getByRole('banner')).toBeVisible()
}

const attachJson = (testInfo: TestInfo, name: string, body: unknown) =>
  testInfo.attach(name, { body: JSON.stringify(body, null, 2), contentType: 'application/json' })

const headerHeightToken = (page: Page) =>
  page.evaluate(() => parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--header-h')))

async function box(loc: Locator, label: string) {
  const b = await loc.boundingBox()
  expect(b, `${label} has no box`).not.toBeNull()
  return b!
}

// The frame: header, hero, audience strip, footer.
const FRAME_ALL = 'header, header *, #top, #top *, [data-strip], [data-strip] *, footer, footer *'
const FRAME_DESCENDANTS = 'header *, #top *, [data-strip] *, footer *'

test('landing frame geometry at 1440, 834 and 390', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await openLandingFrame(page)
  const header = page.getByRole('banner')
  const burger = header.getByRole('button', { name: 'Menu' })
  const nav = page.getByRole('navigation', { name: 'Primary' })
  const login = header.getByRole('button', { name: 'Platform login' })
  const measured: unknown[] = []

  for (const vp of FRAME_VIEWPORTS) {
    const label = `${vp.width}px`
    await settleFrame(page, vp)

    const tokenH = await headerHeightToken(page)
    const headerBox = await box(header, `${label} header`)
    expect(tokenH, `${label}: --header-h`).toBe(vp.headerH)
    expect(Math.abs(headerBox.height - tokenH), `${label}: header height ${headerBox.height} vs --header-h ${tokenH}`).toBeLessThanOrEqual(0.5)
    expect(headerBox.y, `${label}: header top`).toBeCloseTo(0, 0)

    const wide = vp.width > BURGER_MAX
    if (wide) {
      await expect(burger, `${label}: burger`).toBeHidden()
      await expect(nav, `${label}: Primary nav`).toBeVisible()
      await expect(login, `${label}: Platform login`).toBeVisible()
    } else {
      await expect(burger, `${label}: burger`).toBeVisible()
      await expect(nav, `${label}: Primary nav`).toBeHidden()
      await expect(login, `${label}: Platform login`).toBeHidden()
    }
    await expect(header.getByRole('button', { name: 'Book a demo' }), `${label}: Book a demo`).toBeVisible()

    // clamp(54px, 5.55vw, 80px): --fs-h1 in packages/design-tokens/v2/tokens/typography.css.
    const h1 = page.locator('#top h1')
    const h1Size = await h1.evaluate((el) => parseFloat(getComputedStyle(el).fontSize))
    const h1Want = Math.min(80, Math.max(54, 0.0555 * vp.width))
    expect(Math.abs(h1Size - h1Want), `${label}: h1 font-size ${h1Size} vs clamp ${h1Want}`).toBeLessThanOrEqual(0.5)

    const shadow = await page.evaluate((sel) => {
      const probeEl = document.createElement('div')
      probeEl.style.boxShadow = 'var(--shadow-elegant)'
      document.body.appendChild(probeEl)
      const elegant = getComputedStyle(probeEl).boxShadow
      probeEl.remove()
      const matches = [...document.querySelectorAll(sel)].filter((el) => getComputedStyle(el).boxShadow === elegant)
      return { elegant, card: getComputedStyle(document.querySelector('.hero-card')!).boxShadow, matches: matches.map((el) => el.getAttribute('class') ?? '') }
    }, FRAME_DESCENDANTS)
    expect(shadow.elegant, `${label}: --shadow-elegant resolves`).not.toBe('none')
    expect(shadow.card, `${label}: .hero-card box-shadow`).toBe(shadow.elegant)
    expect(shadow.matches, `${label}: the only frame element with --shadow-elegant`).toHaveLength(1)
    expect(shadow.matches[0], `${label}: the shadow holder`).toContain('hero-card')

    // overflow-x: clip hides overflow from scrollWidth, so every element is walked as well.
    const overflow = await page.evaluate((sel) => {
      const doc = document.documentElement
      const els = [...document.querySelectorAll(sel)]
        .map((el) => ({ el, r: el.getBoundingClientRect() }))
        .filter(({ r }) => r.width > 0 && r.height > 0)
      return {
        innerWidth: window.innerWidth,
        scrollWidth: doc.scrollWidth,
        clientWidth: doc.clientWidth,
        walked: els.length,
        roots: ['header', '#top', '[data-strip]', 'footer'].filter((root) => !els.some(({ el }) => el.matches(root))),
        outside: els
          .filter(({ r }) => r.left < -1 || r.right > window.innerWidth + 1)
          .map(({ el, r }) => `<${el.tagName.toLowerCase()} class="${el.className}"> ${Math.round(r.left)}..${Math.round(r.right)}`),
      }
    }, FRAME_ALL)
    expect(overflow.walked, `${label}: frame elements walked`).toBeGreaterThan(0)
    expect(overflow.roots, `${label}: frame regions missing from the walk`).toEqual([])
    expect(overflow.scrollWidth, `${label}: scrollWidth vs clientWidth`).toBeLessThanOrEqual(overflow.clientWidth)
    expect(overflow.outside, `${label}: frame elements outside the viewport`).toEqual([])

    const h1Box = await box(h1, `${label} h1`)
    expect(h1Box.y, `${label}: h1 top vs header bottom`).toBeGreaterThanOrEqual(headerBox.y + headerBox.height - 1)

    const column = await box(page.locator('#top .split > div:first-child'), `${label} hero text column`)
    const card = await box(page.locator('#top .hero-card'), `${label} hero card`)
    if (wide) {
      expect(rectsOverlap(column, card), `${label}: hero text column overlaps the card`).toBe(false)
      expect(card.x, `${label}: card left vs text column right (beside, not stacked)`).toBeGreaterThanOrEqual(column.x + column.width - 1)
    } else {
      const cta = await box(page.locator('#top').getByRole('button', { name: 'Book a demo' }).locator('..'), `${label} CTA row`)
      expect(card.y, `${label}: card top vs CTA row bottom`).toBeGreaterThanOrEqual(cta.y + cta.height - 1)
    }

    measured.push({ width: vp.width, tokenH, headerBox, h1Size, h1Want, shadow, overflow, column, card })
  }

  // 1440 is within tolerance of the 80px ceiling and 834/390 sit on the floor; only a width between them reads the vw term.
  for (const width of [1100, 1280]) {
    await settleFrame(page, { width, height: 900 })
    const h1Size = await page.locator('#top h1').evaluate((el) => parseFloat(getComputedStyle(el).fontSize))
    const h1Want = Math.min(80, Math.max(54, 0.0555 * width))
    expect(h1Want, `${width}px: clamp is in its proportional range`).toBeGreaterThan(54)
    expect(h1Want, `${width}px: clamp is in its proportional range`).toBeLessThan(80)
    expect(Math.abs(h1Size - h1Want), `${width}px: h1 font-size ${h1Size} vs clamp ${h1Want}`).toBeLessThanOrEqual(0.5)
    measured.push({ width, h1Size, h1Want })
  }

  await attachJson(testInfo, 'frame-geometry.json', measured)
  expect(errors, `console errors on the frame sweep:\n${errors.join('\n')}`).toEqual([])
})

test('landing frame and section columns share one left edge', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await openLandingFrame(page)
  const measured: unknown[] = []

  for (const width of [...WIDE_WIDTHS, 834, 390]) {
    await settleFrame(page, { width, height: width === 390 ? 844 : 1080 })
    const lefts = {
      headerLogo: (await box(page.getByRole('banner').locator('.ds-logo'), `${width} header logo`)).x,
      heroEyebrow: (await box(page.locator('#top .t-eyebrow').first(), `${width} hero eyebrow`)).x,
      stripLabel: (await box(page.locator('[data-strip] > div').first(), `${width} strip label`)).x,
      footerLogo: (await box(page.locator('footer .ds-logo'), `${width} footer logo`)).x,
      problemEyebrow: (await box(page.locator('#problem .t-eyebrow'), `${width} problem eyebrow`)).x,
      problemH2: (await box(page.locator('#problem h2'), `${width} problem h2`)).x,
      solutionEyebrow: (await box(page.locator('#solution .t-eyebrow'), `${width} solution eyebrow`)).x,
      solutionH2: (await box(page.locator('#solution h2'), `${width} solution h2`)).x,
      platformEyebrow: (await box(page.locator('#platform .t-eyebrow'), `${width} platform eyebrow`)).x,
      platformH2: (await box(page.locator('#platform h2'), `${width} platform h2`)).x,
    }
    const aligns = await page.$$eval('#problem h2, #solution h2, #platform h2', (els) => els.map((el) => getComputedStyle(el).textAlign))
    measured.push({ width, ...lefts, aligns })
    expect(aligns, `${width}px: section h2 text-align`).toHaveLength(3)
    for (const a of aligns) expect(['start', 'left'], `${width}px: h2 text-align ${a}`).toContain(a)
    const all = Object.values(lefts)
    expect(Math.max(...all) - Math.min(...all), `${width}px: left edges ${JSON.stringify(lefts)}`).toBeLessThanOrEqual(1)
  }

  await attachJson(testInfo, 'left-edges.json', measured)
  expect(errors, `console errors on the column sweep:\n${errors.join('\n')}`).toEqual([])
})

const SECTIONS = '#problem, #problem *, #solution, #solution *, #platform, #platform *'

// Rendered boxes of every match, in document order.
const rectsOf = (page: Page, sel: string): Promise<Rect[]> =>
  page.$$eval(sel, (els) =>
    els
      .map((el) => el.getBoundingClientRect())
      .filter((r) => r.width > 0 && r.height > 0)
      .map((r) => ({ x: r.left, y: r.top, width: r.width, height: r.height })),
  )

// Cells sharing the first cell's top (±1) form the first row.
const columnsOf = (cells: Rect[]): number => cells.filter((c) => Math.abs(c.y - cells[0].y) <= 1).length

const besideOrBelow = (column: Rect, card: Rect, beside: boolean, label: string) => {
  if (beside) {
    expect(rectsOverlap(column, card), `${label}: card overlaps its column`).toBe(false)
    expect(card.x, `${label}: card left vs column right`).toBeGreaterThanOrEqual(column.x + column.width - 1)
  } else {
    expect(card.y, `${label}: card top vs column bottom`).toBeGreaterThanOrEqual(column.y + column.height - 1)
  }
}

type Overflow = { scrollWidth: number; clientWidth: number; innerWidth: number; roots: string[]; outside: string[] }

// Walks the sections' rendered elements outside the tablist, which scrolls inside itself.
function walkOverflow(page: Page, scope: string, roots: string[]): Promise<Overflow> {
  return page.evaluate(
    ({ scope, roots }) => {
      const doc = document.documentElement
      const els = [...document.querySelectorAll(scope)]
        .filter((el) => !el.closest('[role=tablist]'))
        .map((el) => ({ el, r: el.getBoundingClientRect() }))
        .filter(({ r }) => r.width > 0 && r.height > 0)
      return {
        scrollWidth: doc.scrollWidth,
        clientWidth: doc.clientWidth,
        innerWidth: window.innerWidth,
        roots: roots.filter((root) => !els.some(({ el }) => el.matches(root))),
        outside: els
          .filter(({ r }) => r.left < -1 || r.right > window.innerWidth + 1)
          .map(({ el, r }) => `<${el.tagName.toLowerCase()} class="${el.className}"> ${Math.round(r.left)}..${Math.round(r.right)}`),
      }
    },
    { scope, roots },
  )
}

function expectNoOverflow(o: Overflow, label: string): void {
  expect(o.roots, `${label}: roots missing from the walk`).toEqual([])
  expect(o.scrollWidth, `${label}: scrollWidth vs clientWidth`).toBeLessThanOrEqual(o.clientWidth)
  expect(o.outside, `${label}: elements outside the viewport`).toEqual([])
}

// Computed values of a probe element carrying the given inline style, one per entry.
function probeStyles(page: Page, styles: Record<string, string>): Promise<Record<string, string>> {
  return page.evaluate((styles) => {
    const out: Record<string, string> = {}
    for (const [key, css] of Object.entries(styles)) {
      const el = document.createElement('span')
      el.style.cssText = `position:absolute;visibility:hidden;${css}`
      document.body.appendChild(el)
      const cs = getComputedStyle(el)
      out[key] = key.startsWith('color') ? cs.color : key.startsWith('background') ? cs.backgroundColor : cs.boxShadow
      el.remove()
    }
    return out
  }, styles)
}

const CHECK_END = '4 errors · 4 warnings · Not ready to submit'
const CHECK_OUTCOMES = ['FAIL', 'FAIL', 'FAIL', 'WARN', 'WARN', 'WARN', 'WARN', 'FAIL']
const checkFooter = (page: Page) => page.locator('#problem-check [data-check="footer"]')
const checkTags = (page: Page) => page.locator('#problem-check [data-check="row"] > span:last-child')

const SECTION_ROOTS = ['#problem', '#solution', '#platform']

test('landing sections at 1440, 834 and 390', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await openLandingFrame(page)
  const measured: unknown[] = []

  for (const vp of FRAME_VIEWPORTS) {
    const label = `${vp.width}px`
    const wide = vp.width === 1440
    await settleFrame(page, vp)

    const overflow = await walkOverflow(page, SECTIONS, SECTION_ROOTS)
    const tablist = (await rectsOf(page, '#platform [role=tablist]'))[0]
    expectNoOverflow(overflow, label)
    expect(tablist, `${label}: tablist has no box`).toBeDefined()
    expect(tablist.x, `${label}: tablist left`).toBeGreaterThanOrEqual(-1)
    expect(tablist.x + tablist.width, `${label}: tablist right`).toBeLessThanOrEqual(vp.width + 1)

    const cells = await rectsOf(page, '#solution .mod-cell')
    const grid = (await rectsOf(page, '#solution .mod-grid'))[0]
    const columns = columnsOf(cells)
    expect(cells.length, `${label}: module cells`).toBeGreaterThan(0)
    expect(columns, `${label}: module grid columns`).toBe(wide ? 4 : vp.width === 834 ? 2 : 1)
    for (const [i, c] of cells.entries()) {
      expect(enclosesRect(grid, c, 1), `${label}: cell ${i} outside the grid`).toBe(true)
      for (const d of cells.slice(i + 1)) expect(rectsOverlap(c, d), `${label}: cell ${i} overlaps a later cell`).toBe(false)
    }

    const problemColumn = (await rectsOf(page, '#problem .split > div:first-child'))[0]
    const problemCard = (await rectsOf(page, '#problem-check'))[0]
    besideOrBelow(problemColumn, problemCard, wide, `${label} problem`)
    const panelColumn = (await rectsOf(page, '#platform [role=tabpanel] .split > div:first-child'))[0]
    const panelCard = (await rectsOf(page, '#platform [role=tabpanel] .card'))[0]
    besideOrBelow(panelColumn, panelCard, wide, `${label} platform`)

    const tabsBox = (await rectsOf(page, '#platform .ds-tabs'))[0]
    const caps = await rectsOf(page, '#platform .cols3 > div')
    expect(caps, `${label}: capabilities`).toHaveLength(3)
    for (const c of caps) expect(c.y, `${label}: capability top vs tabs bottom`).toBeGreaterThanOrEqual(tabsBox.y + tabsBox.height - 1)
    for (const [i, c] of caps.entries()) {
      if (i === 0) continue
      if (wide) {
        expect(Math.abs(c.y - caps[0].y), `${label}: capability ${i} top vs first`).toBeLessThanOrEqual(1)
        expect(rectsOverlap(caps[i - 1], c), `${label}: capability ${i} overlaps the previous`).toBe(false)
      } else {
        expect(c.y, `${label}: capability ${i} top vs previous bottom`).toBeGreaterThanOrEqual(caps[i - 1].y + caps[i - 1].height - 1)
      }
    }

    // Resolved values against probes that read the same tokens.
    const want = await probeStyles(page, {
      color_teal: 'color: var(--teal)',
      color_hl: 'color: var(--highlight-on-dark-2)',
      background_surface: 'background: var(--surface)',
      shadow: 'box-shadow: var(--shadow-card)',
    })
    const h2s = await page.evaluate(() =>
      ['#problem', '#solution', '#platform'].map((id) => ({
        id,
        own: getComputedStyle(document.querySelector(`${id} h2`)!).color,
        second: getComputedStyle(document.querySelector(`${id} h2 span`)!).color,
      })),
    )
    const wantSecond = [want.color_teal, want.color_hl, want.color_teal]
    for (const [i, h] of h2s.entries()) {
      expect(h.second, `${label}: ${h.id} h2 second line colour`).toBe(wantSecond[i])
      expect(h.second, `${label}: ${h.id} h2 second line vs its own colour`).not.toBe(h.own)
    }
    const read = await page.evaluate(() => ({
      cellBackgrounds: [...document.querySelectorAll('#solution .mod-cell')].map((el) => getComputedStyle(el).backgroundColor),
      problemShadow: getComputedStyle(document.querySelector('#problem-check')!).boxShadow,
      platformShadow: getComputedStyle(document.querySelector('#platform [role=tabpanel] .card')!).boxShadow,
      panelPaddingLeft: getComputedStyle(document.querySelector('#platform [role=tabpanel]')!).paddingLeft,
    }))
    for (const bg of read.cellBackgrounds) expect(bg, `${label}: module cell background`).toBe(want.background_surface)
    expect(want.shadow, `${label}: --shadow-card resolves`).not.toBe('none')
    expect(read.problemShadow, `${label}: problem check shadow`).toBe(want.shadow)
    expect(read.platformShadow, `${label}: platform result card shadow`).toBe(want.shadow)
    // The clamp's two ends; 834 sits between them.
    if (vp.width !== 834) expect(read.panelPaddingLeft, `${label}: tabpanel padding-left`).toBe(wide ? '48px' : '24px')

    let tabs: unknown = null
    if (vp.width === 390) {
      tabs = await page.evaluate(() => {
        const list = document.querySelector('#platform [role=tablist]')!
        return {
          overflowX: getComputedStyle(list).overflowX,
          items: [...list.querySelectorAll('[role=tab]')].map((tab) => {
            const walker = document.createTreeWalker(tab, NodeFilter.SHOW_TEXT)
            const labelRects: number[] = []
            for (let n = walker.nextNode(); n; n = walker.nextNode()) {
              if (!n.textContent?.trim() || (n.parentElement && n.parentElement.closest('.ds-tab-step'))) continue
              const range = document.createRange()
              range.selectNodeContents(n)
              labelRects.push(range.getClientRects().length)
            }
            return { whiteSpace: getComputedStyle(tab).whiteSpace, labelRects }
          }),
        }
      })
      const t = tabs as { overflowX: string; items: { whiteSpace: string; labelRects: number[] }[] }
      expect(t.overflowX, `${label}: tablist overflow-x`).toBe('auto')
      expect(t.items.length, `${label}: tabs`).toBeGreaterThan(0)
      for (const [i, tab] of t.items.entries()) {
        expect(tab.whiteSpace, `${label}: tab ${i} white-space`).toBe('nowrap')
        expect(tab.labelRects, `${label}: tab ${i} label client rects`).toEqual([1])
      }
    }

    measured.push({ width: vp.width, overflow, tablist, columns, cells: cells.length, grid, problemColumn, problemCard, panelColumn, panelCard, caps, h2s, want, read, tabs })
  }

  await attachJson(testInfo, 'sections.json', measured)
  expect(errors, `console errors on the sections sweep:\n${errors.join('\n')}`).toEqual([])
})

test('landing problem check plays after scrolling into view at 1440, 834 and 390', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  const measured: unknown[] = []

  for (const vp of FRAME_VIEWPORTS) {
    const label = `${vp.width}px`
    await seedConsent(page, false)
    const res = await page.goto(`${LANDING_URL}/`)
    expect(res?.ok(), `${label}: / returned HTTP ${res?.status()}`).toBeTruthy()
    await settleFrame(page, vp)

    const card = page.locator('#problem-check')
    const before = await box(card, `${label} check card`)
    expect(before.y, `${label}: card top vs 0.7 x viewport height`).toBeGreaterThan(0.7 * vp.height)
    // A run that began at load is past step 1 after two steps; a retrying read would wait for step 1 to return.
    await page.waitForTimeout(1100)
    expect(await checkFooter(page).textContent(), `${label}: footer before entry`).toBe('Checking 1 of 8')
    expect(await checkTags(page).allTextContents(), `${label}: tags before entry`).toEqual(Array(8).fill('CHECKING'))

    await card.evaluate((el) => el.scrollIntoView({ block: 'center' }))
    await expect.poll(async () => checkFooter(page).textContent(), { intervals: [100], timeout: 10_000, message: `${label}: the run shows a step` }).toMatch(/^Checking [2-8] of 8$/)
    await expect(checkFooter(page), `${label}: footer at the end`).toHaveText(CHECK_END, { timeout: 10_000 })
    await expect(checkTags(page), `${label}: outcomes`).toHaveText(CHECK_OUTCOMES)

    const overflow = await walkOverflow(page, '#problem, #problem *', ['#problem'])
    expectNoOverflow(overflow, label)
    const cardBox = await box(card, `${label} card after`)
    const footerBox = await box(checkFooter(page), `${label} footer text`)
    const again = await box(card.getByRole('button', { name: 'Run check again' }), `${label} run again`)
    expect(enclosesRect(cardBox, footerBox, 1), `${label}: footer text outside the card`).toBe(true)
    expect(enclosesRect(cardBox, again, 1), `${label}: Run check again outside the card`).toBe(true)
    measured.push({ width: vp.width, cardTopBefore: before.y, overflow, cardBox, footerBox, again })
  }

  await attachJson(testInfo, 'problem-check.json', measured)
  expect(errors, `console errors on the problem check:\n${errors.join('\n')}`).toEqual([])
})

test('landing mobile menu at 834 and 390', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await openLandingFrame(page)
  const header = page.getByRole('banner')
  const burger = header.getByRole('button', { name: 'Menu' })
  const measured: unknown[] = []

  for (const vp of FRAME_VIEWPORTS.filter((v) => v.width <= BURGER_MAX)) {
    const label = `${vp.width}px`
    await settleFrame(page, vp)
    await burger.click()
    await expect(burger, `${label}: aria-expanded`).toHaveAttribute('aria-expanded', 'true')

    const menu = header.locator('.a-menu')
    await expect(menu, `${label}: menu`).toBeVisible()
    const headerBox = await box(header, `${label} header`)
    const menuBox = await box(menu, `${label} menu`)
    expect(Math.abs(menuBox.y - (headerBox.y + headerBox.height)), `${label}: menu top ${menuBox.y} vs header bottom ${headerBox.y + headerBox.height}`).toBeLessThanOrEqual(1)
    expect(menuBox.x, `${label}: menu left`).toBeGreaterThanOrEqual(-1)
    expect(menuBox.x + menuBox.width, `${label}: menu right`).toBeLessThanOrEqual(vp.width + 1)
    expect(menuBox.y + menuBox.height, `${label}: menu bottom`).toBeLessThanOrEqual(vp.height + 1)

    // CSS locator: the Primary nav is display:none below the burger edge.
    const navLinks = await page.locator('nav[aria-label="Primary"] a').count()
    expect(navLinks, `${label}: Primary nav links`).toBeGreaterThan(0)
    await expect(menu.locator('a'), `${label}: one menu link per nav link`).toHaveCount(navLinks)
    await expect(menu.getByRole('button', { name: 'Platform login' }), `${label}: Platform login in the menu`).toHaveCount(1)

    measured.push({ width: vp.width, headerBox, menuBox, navLinks })

    await page.keyboard.press('Escape')
    await expect(menu, `${label}: menu after Escape`).toHaveCount(0)
    await expect(burger, `${label}: aria-expanded after Escape`).toHaveAttribute('aria-expanded', 'false')
  }

  expect(measured.length, 'menu widths measured').toBeGreaterThan(0)
  await attachJson(testInfo, 'menu-geometry.json', measured)
  expect(errors, `console errors on the menu sweep:\n${errors.join('\n')}`).toEqual([])
})

test('landing breakpoint edges', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await openLandingFrame(page)
  const header = page.getByRole('banner')
  const burger = header.getByRole('button', { name: 'Menu' })
  const nav = page.getByRole('navigation', { name: 'Primary' })
  const login = header.getByRole('button', { name: 'Platform login' })
  const measured: Record<string, unknown> = {}

  for (const [width, collapsed] of [[BURGER_MAX, true], [BURGER_MAX + 1, false]] as const) {
    await settleFrame(page, { width, height: 900 })
    await expect(burger, `${width}px: burger`).toBeVisible({ visible: collapsed })
    await expect(nav, `${width}px: Primary nav`).toBeVisible({ visible: !collapsed })
    await expect(login, `${width}px: Platform login`).toBeVisible({ visible: !collapsed })
  }

  const heights: number[] = []
  for (const width of [767, 768]) {
    await settleFrame(page, { width, height: 900 })
    const tokenH = await headerHeightToken(page)
    const headerBox = await box(header, `${width} header`)
    expect(Math.abs(headerBox.height - tokenH), `${width}px: header height ${headerBox.height} vs --header-h ${tokenH}`).toBeLessThanOrEqual(0.5)
    heights.push(headerBox.height)
  }
  expect(heights[0], `header height at 767 (${heights[0]}) is smaller than at 768 (${heights[1]})`).toBeLessThan(heights[1])
  measured.headerHeights = { 767: heights[0], 768: heights[1] }

  const card = page.locator('#top .hero-card')
  const split = page.locator('#top .split')
  const column = page.locator('#top .split > div:first-child')

  await settleFrame(page, { width: 901, height: 900 })
  const beside = { card: await box(card, '901 card'), split: await box(split, '901 split'), column: await box(column, '901 text column') }
  expect(rectsOverlap(beside.column, beside.card), '901px: hero card overlaps the text column').toBe(false)
  expect(Math.abs(beside.card.y - (beside.split.y + 40)), `901px: card top ${beside.card.y} vs split top ${beside.split.y} + 40`).toBeLessThanOrEqual(1)

  await settleFrame(page, { width: 900, height: 900 })
  const cardBox = await box(card, '900 card')
  const cta = await box(page.locator('#top').getByRole('button', { name: 'Book a demo' }).locator('..'), '900 CTA row')
  expect(cardBox.y, '900px: card top vs CTA row bottom').toBeGreaterThanOrEqual(cta.y + cta.height - 1)
  expect(await card.evaluate((el) => getComputedStyle(el).marginTop), '900px: card margin-top').toBe('0px')
  measured.hero = { beside, stacked: { card: cardBox, cta } }

  const sectionEdges: Record<string, unknown> = {}
  for (const [width, cols] of [[1001, 4], [1000, 2], [561, 2], [560, 1]] as const) {
    await settleFrame(page, { width, height: 900 })
    const got = columnsOf(await rectsOf(page, '#solution .mod-cell'))
    expect(got, `${width}px: module grid columns`).toBe(cols)
    sectionEdges[`grid${width}`] = got
  }

  for (const [width, beside] of [[901, true], [900, false]] as const) {
    await settleFrame(page, { width, height: 900 })
    const problem = { column: (await rectsOf(page, '#problem .split > div:first-child'))[0], card: (await rectsOf(page, '#problem-check'))[0] }
    besideOrBelow(problem.column, problem.card, beside, `${width}px problem`)
    const platform = {
      column: (await rectsOf(page, '#platform [role=tabpanel] .split > div:first-child'))[0],
      card: (await rectsOf(page, '#platform [role=tabpanel] .card'))[0],
    }
    besideOrBelow(platform.column, platform.card, beside, `${width}px platform`)
    const caps = await rectsOf(page, '#platform .cols3 > div')
    expect(caps, `${width}px: capabilities`).toHaveLength(3)
    for (const [i, c] of caps.entries()) {
      if (i === 0) continue
      if (beside) expect(Math.abs(c.y - caps[0].y), `${width}px: capability ${i} top vs first`).toBeLessThanOrEqual(1)
      else expect(c.y, `${width}px: capability ${i} top vs previous bottom`).toBeGreaterThanOrEqual(caps[i - 1].y + caps[i - 1].height - 1)
    }
    sectionEdges[`splits${width}`] = { problem, platform, caps }
  }

  for (const [width, fills] of [[641, true], [640, false]] as const) {
    await settleFrame(page, { width, height: 900 })
    const strip = await page.evaluate(() => {
      const list = document.querySelector('#platform [role=tablist]')!
      return {
        list: list.getBoundingClientRect().width,
        overflowX: getComputedStyle(list).overflowX,
        tabs: [...list.querySelectorAll('[role=tab]')].map((t) => ({ grow: getComputedStyle(t).flexGrow, width: t.getBoundingClientRect().width })),
      }
    })
    expect(strip.tabs.length, `${width}px: tabs`).toBeGreaterThan(0)
    for (const [i, t] of strip.tabs.entries()) {
      expect(t.grow, `${width}px: tab ${i} flex-grow`).toBe(fills ? '1' : '0')
      if (fills) expect(Math.abs(t.width - strip.list / strip.tabs.length), `${width}px: tab ${i} width vs a share of the tablist`).toBeLessThanOrEqual(1)
    }
    if (!fills) expect(strip.overflowX, `${width}px: tablist overflow-x`).toBe('auto')
    sectionEdges[`tabs${width}`] = strip
  }
  measured.sections = sectionEdges

  await attachJson(testInfo, 'breakpoint-edges.json', measured)
  expect(errors, `console errors on the breakpoint sweep:\n${errors.join('\n')}`).toEqual([])
})

test('landing hero and problem check under reduced motion', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await openLandingFrame(page)
  await settleFrame(page, { width: 1440, height: 900 })

  const read = await page.evaluate(() => ({
    scan: [...document.querySelectorAll('.hero-scan')].map((el) => getComputedStyle(el).display),
    rows: [...document.querySelectorAll('.hero-row')].map((el) => {
      const cs = getComputedStyle(el)
      return { animationName: cs.animationName, opacity: cs.opacity }
    }),
  }))
  await attachJson(testInfo, 'reduced-motion.json', read)

  expect(read.scan, '.hero-scan elements').toHaveLength(1)
  expect(read.scan[0], '.hero-scan display').toBe('none')
  expect(read.rows, '.hero-row elements').toHaveLength(6)
  for (const [i, row] of read.rows.entries()) {
    expect(row.animationName, `.hero-row ${i} animation-name`).toBe('none')
    expect(row.opacity, `.hero-row ${i} opacity`).toBe('1')
  }

  // The check card shows its end state at load, after scrolling to it and after a replay.
  const problem = page.locator('#problem-check')
  // One read, not a retrying one: a replayed run would reach the end text within the retry window.
  const expectEnd = async (when: string) => {
    expect(await checkFooter(page).textContent(), `reduced motion, ${when}: footer`).toBe(CHECK_END)
    expect(await checkTags(page).allTextContents(), `reduced motion, ${when}: outcomes`).toEqual(CHECK_OUTCOMES)
  }
  await expectEnd('at load')
  await problem.evaluate((el) => el.scrollIntoView({ block: 'center' }))
  await page.waitForTimeout(1100)
  await expectEnd('after scrolling into view')
  await problem.getByRole('button', { name: 'Run check again' }).click()
  await page.waitForTimeout(600)
  await expectEnd('after Run check again')

  await settleFrame(page, { width: 390, height: 844 })
  await problem.evaluate((el) => el.scrollIntoView({ block: 'center' }))
  const narrow = await walkOverflow(page, '#problem, #problem *', ['#problem'])
  expectNoOverflow(narrow, 'reduced motion, 390px')
  const card390 = await box(problem, '390 check card')
  const footer390 = await box(checkFooter(page), '390 footer text')
  expect(enclosesRect(card390, footer390, 1), '390px: footer text outside the card').toBe(true)
  await attachJson(testInfo, 'reduced-motion-check.json', { narrow, card390, footer390 })

  // Control: without the preference the same elements animate, so the reads above come from the media rule.
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  const motion = await page.evaluate(() => ({
    scan: getComputedStyle(document.querySelector('.hero-scan')!).display,
    rows: [...document.querySelectorAll('.hero-row')].map((el) => getComputedStyle(el).animationName),
  }))
  expect(motion.scan, '.hero-scan display without reduced motion').not.toBe('none')
  expect(motion.rows, '.hero-row animation-name without reduced motion').toHaveLength(6)
  for (const name of motion.rows) expect(name, '.hero-row animation-name without reduced motion').not.toBe('none')

  // Control: a fresh load without the preference starts the check at step one.
  await openLandingFrame(page)
  await settleFrame(page, { width: 1440, height: 900 })
  await expect(checkFooter(page), 'footer on a fresh load without reduced motion').toHaveText('Checking 1 of 8')

  expect(errors, `console errors under reduced motion:\n${errors.join('\n')}`).toEqual([])
})
