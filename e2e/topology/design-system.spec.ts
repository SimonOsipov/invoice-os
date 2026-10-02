// The deployed proof of the v2/v1 split and of the landing frame's geometry; one topology spec on purpose, a recorded deviation from docs/e2e-convention.md.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { test, expect, type Locator, type Page, type TestInfo } from '@playwright/test'
import { collectErrors, signInAs } from '../personaSession'
import type { PersonaId } from '../personas'
import { seedConsent } from '../smoke/landingConsent'
import { resolveTarget } from '../targets'
import { rectsOverlap, WIDE_WIDTHS } from './layout'

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

test('landing frame columns share one left edge', async ({ page }, testInfo) => {
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
    }
    measured.push({ width, ...lefts })
    const all = Object.values(lefts)
    expect(Math.max(...all) - Math.min(...all), `${width}px: left edges ${JSON.stringify(lefts)}`).toBeLessThanOrEqual(1)
  }

  await attachJson(testInfo, 'left-edges.json', measured)
  expect(errors, `console errors on the column sweep:\n${errors.join('\n')}`).toEqual([])
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

    const navLinks = await page.getByRole('navigation', { name: 'Primary' }).locator('a').count()
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

  await attachJson(testInfo, 'breakpoint-edges.json', measured)
  expect(errors, `console errors on the breakpoint sweep:\n${errors.join('\n')}`).toEqual([])
})

test('landing hero under reduced motion', async ({ page }, testInfo) => {
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

  // Control: without the preference the same elements animate, so the reads above come from the media rule.
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  const motion = await page.evaluate(() => ({
    scan: getComputedStyle(document.querySelector('.hero-scan')!).display,
    rows: [...document.querySelectorAll('.hero-row')].map((el) => getComputedStyle(el).animationName),
  }))
  expect(motion.scan, '.hero-scan display without reduced motion').not.toBe('none')
  expect(motion.rows, '.hero-row animation-name without reduced motion').toHaveLength(6)
  for (const name of motion.rows) expect(name, '.hero-row animation-name without reduced motion').not.toBe('none')

  expect(errors, `console errors under reduced motion:\n${errors.join('\n')}`).toEqual([])
})
