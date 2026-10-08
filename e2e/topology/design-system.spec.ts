// The deployed proof of the v2 entries, the landing frame's geometry and the Problem, Solution, Platform, Coverage and Intelligence sections, and the whole-page bands, Solutions, Integrations, API, FAQ and closing panel; one topology spec on purpose, a recorded deviation from .claude/rules/e2e.md.
import { test, expect, type Locator, type Page, type TestInfo } from '@playwright/test'
import { provisionStaffAccount } from '../api/client'
import { collectErrors, signInAs } from '../personaSession'
import { seedConsent } from '../smoke/landingConsent'
import { seedStaffSession } from '../staffSession'
import { resolveTarget } from '../targets'
import { enclosesRect, rectsOverlap, WIDE_WIDTHS, type Rect } from './layout'

const LANDING_URL = resolveTarget('LANDING_URL')

// Every family string goes through here, so a quoting mismatch cannot fake a pass.
function familyName(raw: string): string {
  return raw.replace(/["']/g, '').trim()
}

const firstFamily = (raw: string | null): string | null => (raw === null ? null : familyName(raw.split(',')[0]))

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

test('app (firm) reads v2: Manrope h1 and ground, #f5bc88, no Fraunces or Inter, IBM Plex Mono loaded', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInAs(page, 'firm')

  const p = await probe(page)
  await attachProbe(testInfo, 'firm', p)

  expect(p.h1Family, 'h1 first family').toBe('Manrope')
  expect(p.groundFamily, 'ground first family').toBe('Manrope')
  expect(p.accent.toLowerCase(), '--accent').toBe('#f5bc88')
  for (const banned of ['Inter', 'Fraunces']) {
    expect(families(p), `no ${banned} face on the app`).not.toContain(banned)
  }
  expect(families(p, 'loaded'), 'a loaded IBM Plex Mono face').toContain('IBM Plex Mono')
  expect(p.appCount, 'at least one .asc-app element').toBeGreaterThanOrEqual(1)

  expect(errors, `console errors on app (firm):\n${errors.join('\n')}`).toEqual([])
})

test('ops console reads v2: Manrope h1 and ground, #f5bc88, no Fraunces or Inter, IBM Plex Mono loaded', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await seedStaffSession(page, 'ops', await provisionStaffAccount('design-ops'))

  const p = await probe(page)
  await attachProbe(testInfo, 'ops', p)

  expect(p.h1Family, 'h1 first family').toBe('Manrope')
  expect(p.groundFamily, 'ground first family').toBe('Manrope')
  expect(p.accent.toLowerCase(), '--accent').toBe('#f5bc88')
  for (const banned of ['Inter', 'Fraunces']) {
    expect(families(p), `no ${banned} face on the ops console`).not.toContain(banned)
  }
  expect(families(p, 'loaded'), 'a loaded IBM Plex Mono face').toContain('IBM Plex Mono')
  expect(p.appCount, 'at least one .asc-app element').toBeGreaterThanOrEqual(1)

  expect(errors, `console errors on ops console:\n${errors.join('\n')}`).toEqual([])
})

test('support console reads v2: Manrope h1 and ground, #f5bc88, no Fraunces or Inter, IBM Plex Mono loaded', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await seedStaffSession(page, 'support', await provisionStaffAccount('design-support'))

  const p = await probe(page)
  await attachProbe(testInfo, 'support', p)

  expect(p.h1Family, 'h1 first family').toBe('Manrope')
  expect(p.groundFamily, 'ground first family').toBe('Manrope')
  expect(p.accent.toLowerCase(), '--accent').toBe('#f5bc88')
  for (const banned of ['Inter', 'Fraunces']) {
    expect(families(p), `no ${banned} face on the support console`).not.toContain(banned)
  }
  expect(families(p, 'loaded'), 'a loaded IBM Plex Mono face').toContain('IBM Plex Mono')
  expect(p.appCount, 'at least one .asc-app element').toBeGreaterThanOrEqual(1)

  expect(errors, `console errors on support console:\n${errors.join('\n')}`).toEqual([])
})

type Measured = { tag: string; text: string; left: number; right: number; top: number; bottom: number; nav?: boolean }

// Relationships, not pixel values.
async function assertHeaderRow(page: Page, testInfo: TestInfo, widths: number[]): Promise<void> {
  const errors = collectErrors(page)
  const res = await page.goto(`${LANDING_URL}/`)
  expect(res?.ok(), `/ returned HTTP ${res?.status()}`).toBeTruthy()

  // A tight row wraps a label rather than overlapping it, so the widest reading is the one-line height.
  let navLineHeight: number | undefined

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
          nav: Boolean(el.closest('nav')),
          shown: r.width > 0 && r.height > 0 && getComputedStyle(el).visibility === 'visible',
        }
      }
      const all = [...document.querySelectorAll('header *')].map(measure).filter((e) => e.shown)
      const row = [...document.querySelectorAll('header a, header button')].map(measure).filter((e) => e.shown)
      return { innerWidth: window.innerWidth, all, row }
    })
    const strip = ({ tag, text, left, right, top, bottom, nav }: Measured & { shown?: boolean }): Measured => ({ tag, text, left, right, top, bottom, nav })
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

    const login = row.find((e) => e.tag === 'button' && e.text === 'Platform login')
    const create = row.find((e) => e.tag === 'button' && e.text === CREATE_LABEL)
    expect(Boolean(login), `${width}px: Platform login shown`).toBe(width > BURGER_MAX)
    if (width > CREATE_MAX) {
      expect(create, `${width}px: the "${CREATE_LABEL}" entry is missing from the header (is landing.VITE_REGISTRATION_OPEN on?)`).toBeDefined()
      expect(login!.right, `${width}px: "${CREATE_LABEL}" left ${create!.left} is not right of Platform login right ${login!.right}`).toBeLessThanOrEqual(create!.left + 1)
      const centreGap = Math.abs((create!.top + create!.bottom) / 2 - (login!.top + login!.bottom) / 2)
      expect(centreGap, `${width}px: "${CREATE_LABEL}" and Platform login centre lines differ by ${centreGap}`).toBeLessThanOrEqual(1)
      const heightGap = Math.abs(create!.bottom - create!.top - (login!.bottom - login!.top))
      expect(heightGap, `${width}px: "${CREATE_LABEL}" wraps: its height differs from Platform login's by ${heightGap}`).toBeLessThanOrEqual(1)
    } else {
      expect(create, `${width}px: "${CREATE_LABEL}" must be hidden at or below ${CREATE_MAX}px`).toBeUndefined()
    }

    const navLinks = row.filter((e) => e.tag === 'a' && e.nav)
    expect(navLinks.length > 0, `${width}px: Primary nav links shown`).toBe(width > BURGER_MAX)
    if (navLinks.length > 0) {
      navLineHeight ??= Math.min(...navLinks.map((e) => e.bottom - e.top))
      for (const e of navLinks) {
        expect(e.bottom - e.top, `${width}px: nav link "${e.text}" wraps (one line is ${navLineHeight})`).toBeLessThanOrEqual(navLineHeight + 1)
      }
    }
  }

  expect(errors, `console errors on the header sweep:\n${errors.join('\n')}`).toEqual([])
}

// BURGER_MAX + 1 is the narrowest width that shows the five nav links.
// Widest first (layout.ts); 1220 and 1219 straddle the entry's edge.
test('landing header row: inside the viewport and no overlap from 2560 to 834', async ({ page }, testInfo) => {
  await assertHeaderRow(page, testInfo, [...WIDE_WIDTHS, 1240, CREATE_MAX + 1, CREATE_MAX, BURGER_MAX + 1, 1080, 834])
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

// The header entry hides at <=1219px (landing.css .a-create).
const CREATE_MAX = 1219
const CREATE_LABEL = 'Create an account'

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
      coverageEyebrow: (await box(page.locator('#coverage .t-eyebrow'), `${width} coverage eyebrow`)).x,
      intelligenceEyebrow: (await box(page.locator('#intelligence .t-eyebrow'), `${width} intelligence eyebrow`)).x,
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

// Walks the sections' rendered elements outside `skip`, a scroller that scrolls inside itself.
function walkOverflow(page: Page, scope: string, roots: string[], skip = '[role=tablist]'): Promise<Overflow> {
  return page.evaluate(
    ({ scope, roots, skip }) => {
      const doc = document.documentElement
      const els = [...document.querySelectorAll(scope)]
        .filter((el) => !el.closest(skip))
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
    { scope, roots, skip },
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
  let measuredRing: unknown = null

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

      // A key press first, so the script focus matches :focus-visible.
      await page.keyboard.press('Shift')
      await page.evaluate(() => (document.querySelector('#platform [role=tablist] [role=tab]') as HTMLElement).focus())
      const ring = await page.evaluate(() => {
        const list = document.querySelector('#platform [role=tablist]')!
        const tab = document.activeElement as HTMLElement
        const cs = getComputedStyle(tab)
        const r = tab.getBoundingClientRect()
        const c = list.getBoundingClientRect()
        const grow = parseFloat(cs.outlineWidth) + parseFloat(cs.outlineOffset)
        return {
          isFirstTab: tab === list.querySelector('[role=tab]'),
          outlineStyle: cs.outlineStyle,
          grow,
          out: { left: r.left - grow, top: r.top - grow, right: r.right + grow, bottom: r.bottom + grow },
          clip: { left: c.left, top: c.top, right: c.right, bottom: c.bottom },
        }
      })
      expect(ring.isFirstTab, `${label}: keyboard focus on the first tab`).toBe(true)
      expect(ring.outlineStyle, `${label}: first tab focus outline style`).not.toBe('none')
      expect(ring.out.left, `${label}: focus ring left`).toBeGreaterThanOrEqual(ring.clip.left - 1)
      expect(ring.out.top, `${label}: focus ring top`).toBeGreaterThanOrEqual(ring.clip.top - 1)
      expect(ring.out.right, `${label}: focus ring right`).toBeLessThanOrEqual(ring.clip.right + 1)
      expect(ring.out.bottom, `${label}: focus ring bottom`).toBeLessThanOrEqual(ring.clip.bottom + 1)
      measuredRing = ring
    }

    measured.push({ width: vp.width, overflow, tablist, columns, cells: cells.length, grid, problemColumn, problemCard, panelColumn, panelCard, caps, h2s, want, read, tabs, ring: vp.width === 390 ? measuredRing : null })
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

  // 390x667 is the short phone: the menu with its extra item must still end inside the viewport.
  for (const vp of [...FRAME_VIEWPORTS, { width: 390, height: 667, headerH: 73 }].filter((v) => v.width <= BURGER_MAX)) {
    const label = `${vp.width}x${vp.height}`
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
    const create = menu.getByRole('button', { name: CREATE_LABEL })
    await expect(create, `${label}: exactly one "${CREATE_LABEL}" in the menu (is landing.VITE_REGISTRATION_OPEN on?)`).toHaveCount(1)
    await expect(menu.getByRole('button'), `${label}: the menu buttons, in order`).toHaveText(['Platform login', CREATE_LABEL])
    const loginBox = await box(menu.getByRole('button', { name: 'Platform login' }), `${label} menu login`)
    const createBox = await box(create, `${label} menu create`)
    expect(createBox.y, `${label}: "${CREATE_LABEL}" top vs Platform login bottom`).toBeGreaterThanOrEqual(loginBox.y + loginBox.height - 1)
    expect(enclosesRect(menuBox, createBox, 1), `${label}: "${CREATE_LABEL}" leaves the menu`).toBe(true)
    expect(createBox.y + createBox.height, `${label}: "${CREATE_LABEL}" bottom vs viewport`).toBeLessThanOrEqual(vp.height + 1)

    measured.push({ width: vp.width, headerBox, menuBox, loginBox, createBox, navLinks })

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

  const create = header.getByRole('button', { name: CREATE_LABEL })
  for (const [width, shown] of [[CREATE_MAX, false], [CREATE_MAX + 1, true]] as const) {
    await settleFrame(page, { width, height: 900 })
    await expect(create, `${width}px: "${CREATE_LABEL}" (a hidden entry at ${CREATE_MAX + 1} may mean landing.VITE_REGISTRATION_OPEN is off)`).toBeVisible({ visible: shown })
    await expect(burger, `${width}px: burger`).toBeHidden()
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

// 600:673 is the CoverageMap aspectRatio.
const MAP_RATIO = 600 / 673
const STEP_COUNT = 4

const edges = (r: Rect) => [r.x, r.y, r.x + r.width, r.y + r.height]
const sameBox = (a: Rect, b: Rect) => edges(a).every((e, i) => Math.abs(e - edges(b)[i]) <= 1)
const above = (upper: Rect, lower: Rect) => lower.y >= upper.y + upper.height - 1

type Panel = { visibility: string; box: Rect }

// Everything the geometry test reads about one step, in one evaluate so no scroll can separate the reads.
const readWorkspace = (page: Page) =>
  page.evaluate(() => {
    const rect = (el: Element) => {
      const r = el.getBoundingClientRect()
      return { x: r.left, y: r.top, width: r.width, height: r.height }
    }
    return {
      workspace: rect(document.querySelector('[data-intel-workspace]')!),
      panels: [...document.querySelectorAll('[data-intel-panel]')].map((el) => ({ visibility: getComputedStyle(el).visibility, box: rect(el) })),
    }
  })

test('landing coverage and intelligence geometry', async ({ page }, testInfo) => {
  test.setTimeout(180_000)
  const errors = collectErrors(page)
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await openLandingFrame(page)
  await expect(page.locator('html'), 'reduced motion drops smooth scrolling').toHaveCSS('scroll-behavior', 'auto')
  const measured: unknown[] = []

  for (const width of [...WIDE_WIDTHS, 901, 900, 834, 390]) {
    const label = `${width}px`
    const wide = width > 900
    await settleFrame(page, { width, height: width === 390 ? 844 : 900 })
    await page.locator('#coverage').evaluate((el) => el.scrollIntoView({ block: 'start' }))

    // (a) bands: the tone classes resolve the tokens the section files name.
    const probes = await probeStyles(page, {
      background_peach: 'background: var(--peach-band)',
      background_dark2: 'background: var(--surface-2)',
      color_copy: 'color: var(--text-copy)',
    })
    const bands = await page.evaluate(() => ({
      coverage: getComputedStyle(document.querySelector('#coverage')!).backgroundColor,
      intelligence: getComputedStyle(document.querySelector('#intelligence')!).backgroundColor,
    }))
    expect(bands.coverage, `${label}: #coverage background`).toBe(probes.background_peach)
    expect(bands.intelligence, `${label}: #intelligence background`).toBe(probes.background_dark2)

    // (b) map: breaks if CoverageMap drops its aspectRatio or the marker layer stops filling the box.
    const mapBox = (await rectsOf(page, '[data-cov-map] svg[role="img"]'))[0]
    const markers = (await rectsOf(page, '[data-cov-markers]'))[0]
    const panel = (await rectsOf(page, '[data-cov-panel]'))[0]
    expect(mapBox, `${label}: map box`).toBeDefined()
    expect(Math.abs(mapBox.width / mapBox.height / MAP_RATIO - 1), `${label}: map ratio ${mapBox.width}x${mapBox.height}`).toBeLessThanOrEqual(0.005)
    expect(sameBox(markers, mapBox), `${label}: marker layer ${JSON.stringify(markers)} vs map ${JSON.stringify(mapBox)}`).toBe(true)
    expect(enclosesRect(panel, mapBox, 1), `${label}: panel does not enclose the map`).toBe(true)

    // (c) columns, Coverage: breaks if the split loses its 900px stacking rule.
    const left = (await rectsOf(page, '#coverage .split > div:first-child'))[0]
    const roadmap = await rectsOf(page, '[data-roadmap] > div')
    expect(roadmap, `${label}: roadmap items`).toHaveLength(3)
    if (wide) {
      expect(rectsOverlap(left, panel), `${label}: left column overlaps the panel`).toBe(false)
      expect(panel.x, `${label}: panel left vs column right`).toBeGreaterThanOrEqual(left.x + left.width - 1)
      expect(Math.abs(panel.y - left.y), `${label}: panel top vs column top`).toBeLessThanOrEqual(1)
      expect(Math.abs(panel.height - left.height), `${label}: panel height vs column height`).toBeLessThanOrEqual(1)
    } else {
      expect(above(left, panel), `${label}: panel top vs column bottom`).toBe(true)
    }
    for (const [i, r] of roadmap.entries()) {
      if (i === 0) continue
      if (wide) {
        expect(Math.abs(r.y - roadmap[0].y), `${label}: roadmap ${i} top vs first`).toBeLessThanOrEqual(1)
        expect(rectsOverlap(roadmap[i - 1], r), `${label}: roadmap ${i} overlaps the previous`).toBe(false)
      } else {
        expect(above(roadmap[i - 1], r), `${label}: roadmap ${i} top vs previous bottom`).toBe(true)
      }
    }

    // (c) columns, Intelligence.
    await page.locator('#intelligence').evaluate((el) => el.scrollIntoView({ block: 'start' }))
    const aside = (await rectsOf(page, '[data-intel-workspace] > aside'))[0]
    const main = (await rectsOf(page, '[data-intel-workspace] > div'))[0]
    const text = (await rectsOf(page, '[data-intel-panel]:not([aria-hidden]) > div:first-child'))[0]
    const card = (await rectsOf(page, '[data-intel-panel]:not([aria-hidden]) > .card'))[0]
    if (wide) {
      expect(rectsOverlap(aside, main), `${label}: workspace aside overlaps main`).toBe(false)
      expect(Math.abs(aside.y - main.y), `${label}: aside top vs main top`).toBeLessThanOrEqual(1)
      expect(rectsOverlap(text, card), `${label}: panel text overlaps its card`).toBe(false)
    } else {
      expect(above(aside, main), `${label}: main top vs aside bottom`).toBe(true)
      expect(above(text, card), `${label}: panel card top vs text bottom`).toBe(true)
    }

    // (d) workspace height: breaks if the panels stop sharing one grid cell (gridArea: 1 / 1).
    const heights: number[] = []
    for (let i = 0; i < STEP_COUNT; i++) {
      await page.locator('[data-intel-step]').nth(i).click()
      const read = await readWorkspace(page)
      heights.push(read.workspace.height)
      expect(read.panels, `${label}: step ${i} panels`).toHaveLength(STEP_COUNT)
      expect(
        read.panels.map((p: Panel) => p.visibility),
        `${label}: step ${i} visibility`,
      ).toEqual(read.panels.map((_, j) => (j === i ? 'visible' : 'hidden')))
      for (const [j, p] of read.panels.entries()) expect(sameBox(p.box, read.panels[0].box), `${label}: step ${i} panel ${j} box`).toBe(true)
    }
    expect(Math.max(...heights) - Math.min(...heights), `${label}: workspace heights ${heights}`).toBeLessThanOrEqual(0.5)

    // (e) overflow: the step row scrolls inside itself, so its descendants are skipped.
    const overflow = await walkOverflow(page, '#coverage, #coverage *, #intelligence, #intelligence *', ['#coverage', '#intelligence'], '[data-intel-steps] *')
    expectNoOverflow(overflow, label)
    const steps = await page
      .locator('[data-intel-steps]')
      .evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth, overflowX: getComputedStyle(el).overflowX }))
    if (width === 390) {
      expect(steps.scrollWidth, '390px: control, the step row overflows').toBeGreaterThan(steps.clientWidth)
      expect(['auto', 'scroll'], '390px: control, the step row scrolls inside itself').toContain(steps.overflowX)
    }

    // (f) one-line titles and labels at 1440: breaks if a title or label loses its fit.
    const lines = await page.evaluate(() => {
      const lineOf = (el: Element) => {
        const cs = getComputedStyle(el)
        const lh = parseFloat(cs.lineHeight)
        return Number.isNaN(lh) ? parseFloat(cs.fontSize) * 1.2 : lh
      }
      // The text's own line boxes: a stretched grid row inflates the h3 box without adding a line.
      const titleLines = (el: Element) => {
        const range = document.createRange()
        range.selectNodeContents(el)
        const tops = [...range.getClientRects()].map((r) => r.top)
        return { rects: tops.length, spread: tops.length ? Math.max(...tops) - Math.min(...tops) : 0, line: lineOf(el) }
      }
      const oneLine = (el: Element) => ({ height: el.getBoundingClientRect().height, line: lineOf(el) })
      return {
        titles: [...document.querySelectorAll('[data-intel-panel] h3')].map(titleLines),
        labels: [...document.querySelectorAll('[data-intel-step]')].map((btn) => {
          const span = btn.querySelector(':scope > span:last-child')!
          return { ...oneLine(span), overhang: span.getBoundingClientRect().right - btn.getBoundingClientRect().right }
        }),
      }
    })
    if (width === 1440) {
      expect(lines.titles, `${label}: panel titles`).toHaveLength(STEP_COUNT)
      expect(lines.labels, `${label}: step labels`).toHaveLength(STEP_COUNT)
      for (const [i, t] of lines.titles.entries()) {
        expect(t.rects, `${label}: panel title ${i} has text lines`).toBeGreaterThanOrEqual(1)
        expect(t.spread, `${label}: panel title ${i} is one line`).toBeLessThanOrEqual(0.5 * t.line)
      }
      for (const [i, l] of lines.labels.entries()) {
        expect(l.height, `${label}: step label ${i} is one line`).toBeLessThanOrEqual(1.5 * l.line)
        expect(l.overhang, `${label}: step label ${i} right edge vs its button`).toBeLessThanOrEqual(1)
      }
    }

    // (g) panel body colour: breaks if the inline colour on the p goes and .band-dark2 .t-body wins.
    const body = await page.evaluate(() => {
      const channels = (c: string) => (c.match(/[\d.]+/g) ?? []).slice(0, 3).map(Number)
      const lum = (c: string) => {
        const [r, g, b] = channels(c).map((v) => {
          const s = v / 255
          return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
        })
        return 0.2126 * r + 0.7152 * g + 0.0722 * b
      }
      const bg = getComputedStyle(document.querySelector('[data-intel-workspace]')!).backgroundColor
      return [...document.querySelectorAll('[data-intel-panel] p.t-body')].map((p) => {
        const fg = getComputedStyle(p).color
        const [hi, lo] = [lum(fg), lum(bg)].sort((a, b) => b - a)
        return { fg, bg, ratio: (hi + 0.05) / (lo + 0.05) }
      })
    })
    expect(body, `${label}: panel bodies`).toHaveLength(STEP_COUNT)
    for (const [i, b] of body.entries()) {
      expect(b.fg, `${label}: panel body ${i} colour`).toBe(probes.color_copy)
      expect(b.ratio, `${label}: panel body ${i} contrast on the workspace`).toBeGreaterThanOrEqual(4.5)
    }

    measured.push({ width, bands, probes, mapBox, markers, panel, left, roadmap, aside, main, text, card, heights, overflow, steps, lines, body })
  }

  await attachJson(testInfo, 'coverage-intelligence-geometry.json', measured)
  expect(errors, `console errors on the coverage and intelligence sweep:\n${errors.join('\n')}`).toEqual([])
})

// Two successive reads agree once the scroll has settled.
async function stableScrollY(page: Page): Promise<number> {
  let last = -1
  await expect
    .poll(async () => {
      const y = await page.evaluate(() => new Promise<number>((r) => requestAnimationFrame(() => r(window.scrollY))))
      const settled = y === last
      last = y
      return settled
    })
    .toBe(true)
  return last
}

test('landing coverage markers by keyboard and pointer', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await openLandingFrame(page)
  await expect(page.locator('html'), 'reduced motion drops smooth scrolling').toHaveCSS('scroll-behavior', 'auto')
  // A card height change must not move scrollY by anchoring, or the Space check reads it as a scroll.
  await page.addStyleTag({ content: 'html { overflow-anchor: none }' })
  await settleFrame(page, { width: 1440, height: 900 })

  const marker = (name: string) => page.locator(`[data-cov-markers] g[aria-label="${name}"]`)
  const ring = (name: string) => marker(name).locator('.cov-marker-focus')
  const tab = (name: string) => page.locator('#coverage button[aria-pressed]').filter({ hasText: name })
  const title = page.locator('[data-cov-card] h3')

  // Breaks if the marker layer leaves the tab order or the card link stops preceding it.
  await page.locator('[data-cov-card] a').focus()
  await page.keyboard.press('Tab')
  await expect(marker('Nigeria'), 'Tab from the card link reaches Nigeria').toBeFocused()
  await expect(ring('Nigeria'), 'focused marker shows its ring').toHaveCSS('opacity', '1')
  await expect(ring('Kenya'), 'unfocused marker hides its ring').toHaveCSS('opacity', '0')
  await expect(ring('South Africa'), 'unfocused marker hides its ring').toHaveCSS('opacity', '0')

  await page.keyboard.press('Tab')
  await expect(marker('Kenya'), 'second Tab reaches Kenya').toBeFocused()
  await page.keyboard.press('Enter')
  await expect(tab('Kenya')).toHaveAttribute('aria-pressed', 'true')
  await expect(title).toHaveText('A country workflow for Kenya.')

  // Breaks if onKeyDown drops preventDefault for Space.
  await marker('South Africa').focus()
  const before = await stableScrollY(page)
  await page.keyboard.press('Space')
  await expect(tab('South Africa')).toHaveAttribute('aria-pressed', 'true')
  await expect(title).toHaveText('Prepare for what comes next.')
  const after = await stableScrollY(page)
  expect(after, 'Space on a marker must not scroll the page').toBe(before)

  // A real pointer click, not a keyboard activation.
  await tab('Nigeria').click()
  await expect(title).toHaveText('Our starting point. Your next step.')
  await marker('Kenya').click()
  await expect(tab('Kenya')).toHaveAttribute('aria-pressed', 'true')
  await expect(title).toHaveText('A country workflow for Kenya.')

  await attachJson(testInfo, 'coverage-markers.json', { scrollBeforeSpace: before, scrollAfterSpace: after })
  expect(errors, `console errors on the marker path:\n${errors.join('\n')}`).toEqual([])
})

// Bands, Solutions, Integrations, API, FAQ and closing panel.
const GE_WIDTHS = [...WIDE_WIDTHS, 1001, 1000, 901, 900, 834, 641, 640, 390]
const geFrame = (width: number): Frame => ({ width, height: width === 390 ? 844 : 900 })

// Band tokens from the hero down (AC 12), then the footer.
const BAND_TOKENS = [
  '--surface-dark',
  '--sage',
  '--surface-page',
  '--surface-dark',
  '--surface-page',
  '--peach-band',
  '--surface-2',
  '--surface-page',
  '--peach-band',
  '--surface-dark',
  '--surface-page',
  '--surface-page',
  '--surface-page',
]

test('landing v2 bands, top to bottom, and no horizontal overflow', async ({ page }, testInfo) => {
  test.setTimeout(180_000)
  const errors = collectErrors(page)
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await openLandingFrame(page)
  const bandWidths = [1440, 834, 390]
  const measured: unknown[] = []

  for (const width of GE_WIDTHS) {
    const label = `${width}px`
    await settleFrame(page, geFrame(width))

    if (bandWidths.includes(width)) {
      const probes = await probeStyles(
        page,
        Object.fromEntries([...new Set([...BAND_TOKENS, '--peach-card'])].map((t) => [`background_${t}`, `background: var(${t})`])),
      )
      const bands = await page.evaluate(() => {
        const rect = (el: Element) => {
          const r = el.getBoundingClientRect()
          return { top: r.top, bottom: r.bottom }
        }
        const sections = [...document.querySelectorAll('section')].filter((el) => !el.parentElement?.closest('section'))
        const closing = document.querySelector('[data-closing]')
        return {
          layers: [...sections, document.querySelector('footer')!].map((el) => ({
            ...rect(el),
            id: el.id,
            background: getComputedStyle(el).backgroundColor,
            hasClosing: el.contains(closing),
          })),
          closingBackground: closing ? getComputedStyle(closing).backgroundColor : null,
        }
      })
      measured.push({ width, bands })
      expect(bands.layers, `${label}: top-level sections plus the footer`).toHaveLength(BAND_TOKENS.length)
      bands.layers.forEach((layer, i) => {
        expect(layer.background, `${label}: band ${i} (#${layer.id}) background vs ${BAND_TOKENS[i]}`).toBe(probes[`background_${BAND_TOKENS[i]}`])
        if (i > 0) expect(layer.top, `${label}: band ${i} top vs band ${i - 1} bottom`).toBeGreaterThanOrEqual(bands.layers[i - 1].bottom - 1)
      })
      expect(bands.layers[11].hasClosing, `${label}: the 12th section holds [data-closing]`).toBe(true)
      expect(bands.closingBackground, `${label}: [data-closing] background vs --peach-card`).toBe(probes['background_--peach-card'])
    }

    // The tab scroller and the code block scroll inside themselves; their own boxes must fit.
    const roots = ['#solutions', '#integrations', '#api', '#faq', 'section:has([data-closing])']
    const walk = await walkOverflow(page, roots.flatMap((r) => [r, `${r} *`]).join(', '), roots, '[data-sol-tabs], #api pre')
    expectNoOverflow(walk, label)
    for (const sel of ['[data-sol-tabs]', '#api pre']) {
      const b = await box(page.locator(sel).first(), `${label}: ${sel}`)
      expect(b.x, `${label}: ${sel} left`).toBeGreaterThanOrEqual(-1)
      expect(b.x + b.width, `${label}: ${sel} right`).toBeLessThanOrEqual(walk.innerWidth + 1)
    }
    measured.push({ width, scrollWidth: walk.scrollWidth, clientWidth: walk.clientWidth })
  }

  await attachJson(testInfo, 'bands-overflow.json', measured)
  expect(errors, `console errors on the band sweep:\n${errors.join('\n')}`).toEqual([])
})

test('landing solutions panel height and tabs', async ({ page }, testInfo) => {
  test.setTimeout(180_000)
  const errors = collectErrors(page)
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await openLandingFrame(page)
  const measured: unknown[] = []
  const order = [
    [0, 'fin'],
    [1, 'firm'],
    [2, 'dev'],
    [0, 'fin'],
  ] as const

  for (const width of GE_WIDTHS) {
    const label = `${width}px`
    const wide = width > 900
    await settleFrame(page, geFrame(width))
    await page.locator('#solutions').evaluate((el) => el.scrollIntoView({ block: 'start' }))
    const heights: number[] = []

    for (const [index, id] of order) {
      const step = `${label}, tab ${id}`
      const tab = page.locator('[data-sol-tabs] [role="tab"]').nth(index)
      await tab.click()
      await expect(tab, `${step}: selected`).toHaveAttribute('aria-selected', 'true')
      const read = await page.evaluate(() => {
        const rect = (el: Element) => {
          const r = el.getBoundingClientRect()
          return { x: r.left, y: r.top, width: r.width, height: r.height }
        }
        const panels = [...document.querySelectorAll('[data-sol-panel]')].map((el) => ({
          id: el.getAttribute('data-sol-panel'),
          visibility: getComputedStyle(el).visibility,
          box: rect(el),
          sage: rect(el.children[0]),
          copy: rect(el.children[1]),
        }))
        return { container: rect(document.querySelector('[data-sol-panels]')!), panels }
      })
      measured.push({ width, id, read })
      heights.push(read.container.height)
      expect(read.panels.map((p) => p.id), `${step}: panel ids`).toEqual(['fin', 'firm', 'dev'])
      for (const p of read.panels) {
        expect(p.visibility, `${step}: panel ${p.id} visibility`).toBe(p.id === id ? 'visible' : 'hidden')
      }
      expect(sameBox(read.panels[0].box, read.panels[1].box) && sameBox(read.panels[1].box, read.panels[2].box), `${step}: panels share one box`).toBe(true)
      const shown = read.panels.find((p) => p.id === id)!
      if (wide) {
        expect(rectsOverlap(shown.sage, shown.copy), `${step}: sage box overlaps the copy column`).toBe(false)
        expect(Math.abs(shown.sage.y - shown.copy.y), `${step}: sage top vs copy top`).toBeLessThanOrEqual(1)
      } else {
        expect(above(shown.sage, shown.copy), `${step}: copy top vs sage bottom`).toBe(true)
      }
    }
    expect(Math.max(...heights) - Math.min(...heights), `${label}: panel container heights ${heights}`).toBeLessThanOrEqual(0.5)

    // Labels stay on one line and the focus ring has room inside the scroller.
    const scroller = page.locator('[data-sol-tabs]')
    await scroller.evaluate((el) => (el.scrollLeft = 0))
    const tabsRead = await page.evaluate(() => {
      const rect = (el: Element) => {
        const r = el.getBoundingClientRect()
        return { x: r.left, y: r.top, width: r.width, height: r.height }
      }
      return {
        scroller: rect(document.querySelector('[data-sol-tabs]')!),
        tablist: rect(document.querySelector('[data-sol-tabs] [role="tablist"]')!),
        buttons: [...document.querySelectorAll('[data-sol-tabs] .ds-seg-btn')].map((el) => ({
          box: rect(el),
          label: el.textContent,
          tall: el.scrollHeight > el.clientHeight,
          wide: el.scrollWidth > el.clientWidth,
        })),
      }
    })
    measured.push({ width, tabsRead })
    expect(tabsRead.buttons, `${label}: tab buttons`).toHaveLength(3)
    for (const b of tabsRead.buttons) {
      expect(b.tall || b.wide, `${label}: "${b.label}" overflows its button`).toBe(false)
      expect(enclosesRect(tabsRead.tablist, b.box, 1), `${label}: "${b.label}" outside the tablist`).toBe(true)
      expect(b.box.y, `${label}: "${b.label}" top inside the scroller`).toBeGreaterThanOrEqual(tabsRead.scroller.y + 4)
      expect(b.box.y + b.box.height, `${label}: "${b.label}" bottom inside the scroller`).toBeLessThanOrEqual(tabsRead.scroller.y + tabsRead.scroller.height - 4)
    }
    expect(tabsRead.buttons[0].box.x, `${label}: first button left inside the scroller`).toBeGreaterThanOrEqual(tabsRead.scroller.x + 4)

    if (width === 390) {
      const sizes = await scroller.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
      expect(sizes.scrollWidth, `${label}: the tab scroller scrolls`).toBeGreaterThan(sizes.clientWidth)
      await scroller.evaluate((el) => (el.scrollLeft = el.scrollWidth))
      const end = await rectsOf(page, '[data-sol-tabs], [data-sol-tabs] .ds-seg-btn:last-child')
      expect(end[1].x + end[1].width, `${label}: last button right vs scroller right`).toBeLessThanOrEqual(end[0].x + end[0].width - 4)
    }
  }

  await attachJson(testInfo, 'solutions.json', measured)
  expect(errors, `console errors on the Solutions sweep:\n${errors.join('\n')}`).toEqual([])
})

test('landing integrations and api columns', async ({ page }, testInfo) => {
  test.setTimeout(180_000)
  const errors = collectErrors(page)
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await openLandingFrame(page)
  const measured: unknown[] = []

  for (const width of GE_WIDTHS) {
    const label = `${width}px`
    const wide = width > 900
    await settleFrame(page, geFrame(width))

    const cards = await rectsOf(page, '[data-partner]')
    expect(cards, `${label}: partner cards`).toHaveLength(6)
    for (let i = 0; i < cards.length; i++) {
      for (let j = i + 1; j < cards.length; j++) {
        expect(rectsOverlap(cards[i], cards[j]), `${label}: partner ${i} overlaps partner ${j}`).toBe(false)
      }
    }
    const tops = cards.reduce<number[]>((acc, c) => (acc.some((t) => Math.abs(t - c.y) <= 1) ? acc : [...acc, c.y]), [])
    // 3 columns above 1000px, 2 up to 640px, then 1 (.cols6).
    const rows = width > 1000 ? 2 : width > 640 ? 3 : 6
    expect(tops, `${label}: distinct partner tops`).toHaveLength(rows)
    for (const c of cards) {
      const first = cards.find((o) => Math.abs(o.y - c.y) <= 1)!
      expect(Math.abs(c.height - first.height), `${label}: partner height vs its row`).toBeLessThanOrEqual(1)
    }

    const left = (await rectsOf(page, '#api .split > div:first-child'))[0]
    const panel = (await rectsOf(page, '[data-api-panel]'))[0]
    const pre = (await rectsOf(page, '[data-api-panel] pre'))[0]
    expect(panel, `${label}: api panel`).toBeDefined()
    besideOrBelow(left, panel, wide, `${label}: api panel`)
    expect(enclosesRect(panel, pre, 1), `${label}: api panel does not enclose its pre`).toBe(true)
    measured.push({ width, cards, tops, left, panel, pre })
  }

  await attachJson(testInfo, 'columns.json', measured)
  expect(errors, `console errors on the column sweep:\n${errors.join('\n')}`).toEqual([])
})

test('landing faq sticky and closing panel', async ({ page }, testInfo) => {
  test.setTimeout(180_000)
  const errors = collectErrors(page)
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await openLandingFrame(page)
  const measured: unknown[] = []

  for (const width of GE_WIDTHS) {
    const label = `${width}px`
    const wide = width > 900
    await settleFrame(page, geFrame(width))

    if ([1440, 834, 390].includes(width)) {
      const headerH = await headerHeightToken(page)
      const [split, aside] = await rectsOf(page, '#faq .split, [data-faq-aside]')
      const spare = split.height - aside.height
      if (width === 1440) expect(spare, `${label}: sticky room, split ${split.height} vs aside ${aside.height}`).toBeGreaterThan(8)
      const d = Math.min(80, spare / 2)
      await page.evaluate((y) => window.scrollBy(0, y), split.y - (headerH + 24) + d)
      await page.evaluate(() => new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r()))))
      const [s2, a2, list] = await rectsOf(page, '#faq .split, [data-faq-aside], [data-faq-list]')
      measured.push({ width, headerH, d, split: s2, aside: a2, list })
      if (wide) {
        expect(Math.abs(a2.y - (headerH + 24)), `${label}: aside top ${a2.y} vs header-h + 24`).toBeLessThanOrEqual(1)
        expect(rectsOverlap(a2, list), `${label}: aside overlaps the list`).toBe(false)
      } else {
        expect(Math.abs(a2.y - s2.y), `${label}: aside top vs split top`).toBeLessThanOrEqual(1)
        expect(above(a2, list), `${label}: list top vs aside bottom`).toBe(true)
      }
    }

    await page.locator('[data-closing]').evaluate((el) => el.scrollIntoView({ block: 'center' }))
    const section = (await rectsOf(page, 'section:has([data-closing]) > .container'))[0]
    const [closing, copy] = await rectsOf(page, '[data-closing], [data-closing] > div:first-child')
    const mark = page.locator('[data-closing] .cta-mark')
    expect(enclosesRect(section, closing, 1), `${label}: panel outside its container`).toBe(true)
    expect(enclosesRect(closing, copy, 1), `${label}: copy grid outside the panel`).toBe(true)
    if (wide) {
      await expect(mark, `${label}: .cta-mark display`).toHaveCSS('display', 'block')
      const m = await box(mark, `${label}: .cta-mark`)
      expect(m.x, `${label}: .cta-mark left vs panel left`).toBeGreaterThanOrEqual(closing.x)
      expect(m.x + m.width, `${label}: .cta-mark right vs panel right`).toBeLessThanOrEqual(closing.x + closing.width + 1)
      expect(Math.abs(m.y + m.height / 2 - (closing.y + closing.height / 2)), `${label}: .cta-mark centre vs panel centre`).toBeLessThanOrEqual(1)
    } else {
      await expect(mark, `${label}: .cta-mark display`).toHaveCSS('display', 'none')
    }
  }

  await attachJson(testInfo, 'faq-closing.json', measured)
  expect(errors, `console errors on the FAQ and closing sweep:\n${errors.join('\n')}`).toEqual([])
})
