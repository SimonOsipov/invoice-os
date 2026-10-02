// The deployed proof that the landing reads v2 and the app and consoles read v1.
// Landing and v1 halves share one topology spec on purpose; this is a recorded deviation from
// docs/e2e-convention.md, which keeps gateway-free render checks in smoke.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { test, expect, type Page, type TestInfo } from '@playwright/test'
import { collectErrors, signInAs } from '../personaSession'
import type { PersonaId } from '../personas'
import { resolveTarget } from '../targets'

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
