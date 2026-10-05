// The deployed v2 app shell (RESKIN2-01): resolved values on the PR environment, element by element.
// jsdom has no cascade, so unit tests pin class names and token strings; this file pins what the cascade resolves.
// Token and corner reads are value reads (no pixel diff); layout claims assert the relationship.
// Screenshots are attached for the reviewer and never asserted.
import { test, expect, type Locator, type Page, type TestInfo } from '@playwright/test'
import { collectErrors, signInAs } from '../personaSession'
import { expectedStatusDropper, type Dropper } from './consoleGate'
import { assertPageDoesNotScrollSideways, enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'
import { APP_URL, GATEWAY_URL } from './targets'

test.use({ viewport: { width: 1440, height: 900 } })

const SHADOW_CARD = 'rgba(40, 83, 52, 0.21) 0px 14px 22px -16px'
const NOT_ACTIVE_BODY = JSON.stringify({ error: 'your membership in this workspace is not active' })
const ME_URL = `${GATEWAY_URL}/api/tenancy/v1/me`

const firstFamily = (raw: string): string => raw.split(',')[0].replace(/["']/g, '').trim()

// A copy of auth.spec.ts's gatedErrors: collectErrors minus the listed deliberate non-2xx answers.
function gatedErrors(page: Page, drops: Dropper[]): string[] {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    if (drops.some((drop) => drop(msg.text(), msg.location().url))) return
    errors.push(msg.text())
  })
  page.on('pageerror', (err) => errors.push(`pageerror: ${err.message}`))
  return errors
}

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

// Counts in-flight /api/ requests. `quiet` waits until none has been in flight for 300 ms, then until no
// loading text or audit skeleton row is left; it fails with a named message on timeout.
function trackApi(page: Page): { quiet: (where: string) => Promise<void> } {
  const inflight = new Set<unknown>()
  const isApi = (url: string) => new URL(url).pathname.includes('/api/')
  page.on('request', (r) => { if (isApi(r.url())) inflight.add(r) })
  const done = (r: { url(): string }) => inflight.delete(r)
  page.on('requestfinished', done)
  page.on('requestfailed', done)
  return {
    quiet: async (where) => {
      const deadline = Date.now() + 15_000
      let idleSince = Date.now()
      while (Date.now() - idleSince < 300) {
        if (Date.now() > deadline) throw new Error(`${where}: /api/ requests still in flight after 15s (${inflight.size})`)
        if (inflight.size > 0) idleSince = Date.now()
        await page.waitForTimeout(50)
      }
      const main = page.locator('main.pf-main')
      await expect(main.getByText(/^Loading .+…$/), `${where}: a loading label never went away`).toHaveCount(0, { timeout: 15_000 })
      await expect(main.getByTestId('audit-skeleton-row'), `${where}: audit skeleton rows never went away`).toHaveCount(0, { timeout: 15_000 })
    },
  }
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

const aside = (page: Page) => page.locator('aside.pf-sidebar')
const navButtons = (page: Page) => aside(page).locator('nav button.pf-nav')
const navButton = (page: Page, label: RegExp) => aside(page).locator('nav button.pf-nav', { hasText: label })

// Click a nav item and wait until it is the active one and its screen's heading has drawn.
async function openNav(page: Page, button: Locator): Promise<void> {
  await button.click()
  await expect(button, 'the clicked nav row never became the active one (weight 600)').toHaveCSS('font-weight', '600')
  await expect(page.locator('main.pf-main .pf-scroll h1:visible').first(), 'the screen drew no visible h1').toBeVisible()
}

const SHELL_DARK = { background: 'rgb(8, 47, 49)', edge: 'rgb(22, 71, 66)', activeBg: 'rgb(12, 60, 57)', peach: 'rgb(245, 188, 136)' }

async function readAside(page: Page): Promise<Record<string, unknown>> {
  const el = aside(page)
  const s = await styles(el, ['background-color', 'border-right-color', 'border-right-width'])
  const className = await el.getAttribute('class')
  expect(s['background-color'], 'aside background').toBe(SHELL_DARK.background)
  expect(s['border-right-color'], 'aside right edge colour').toBe(SHELL_DARK.edge)
  expect(s['border-right-width'], 'aside right edge width').toBe('1px')
  expect(className, 'aside scope class').toContain('asc-dark')

  const active = navButton(page, /^Overview/)
  await expect(active, 'the default view is Overview, so its row is the active one (weight 600)').toHaveCSS('font-weight', '600')
  const row = await styles(active, ['background-color'])
  const bar = await styles(active.locator('> span').nth(0), ['background-color'])
  const icon = await styles(active.locator('> span').nth(1), ['color'])
  expect(row['background-color'], 'active nav row background').toBe(SHELL_DARK.activeBg)
  expect(bar['background-color'], 'active nav bar').toBe(SHELL_DARK.peach)
  expect(icon.color, 'active nav icon').toBe(SHELL_DARK.peach)
  return { aside: s, className, activeRow: row, bar, icon }
}

test('AS-01 firm shell at 1440: aside, header, switch, colour fixes', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInAs(page, 'firm')
  await settle(page, aside(page), page.locator('main.pf-main'))
  const measured: Record<string, unknown> = { aside: await readAside(page) }

  const header = await styles(page.locator('main.pf-main > header'), ['background-color', 'backdrop-filter', 'border-bottom-color'])
  expect(header['background-color'], 'header background').toBe('rgba(250, 248, 242, 0.95)')
  expect(header['backdrop-filter'], 'header backdrop-filter').toBe('blur(18px)')
  expect(header['border-bottom-color'], 'header border').toBe('rgb(220, 231, 228)')

  const pill = page.getByTestId('env-pill')
  const track = await styles(pill, ['background-color', 'border-top-color', 'border-top-width'])
  expect(track['background-color'], 'switch track background').toBe('rgb(231, 236, 223)')
  expect(track['border-top-color'], 'sandbox track border').toBe('rgb(227, 203, 168)')
  expect(track['border-top-width'], 'track border width').toBe('1px')
  const sandbox = await styles(pill.locator('button').first(), ['background-color', ...CORNERS.slice(0, 1)])
  expect(sandbox['background-color'], 'SANDBOX segment background').toBe('rgb(7, 60, 61)')
  expect(sandbox['border-top-left-radius'], 'SANDBOX segment corner').toBe('4px')

  const signOut = page.getByRole('button', { name: 'Sign out' })
  expect(await radii(signOut), 'Sign out corners').toEqual(Array(4).fill('4px'))
  const mark = aside(page).locator('img').first()
  const markReading = await styles(mark, ['width', 'height', 'border-top-left-radius'])
  expect(markReading, 'brand mark size and corner').toEqual({ width: '20px', height: '20px', 'border-top-left-radius': '4px' })

  const fg1 = await resolveColor(aside(page), '--fg-1')
  const fg3 = await resolveColor(aside(page), '--fg-3')
  expect(fg1, 'the probe must resolve two distinct tokens, or the equality reads below prove nothing').not.toBe(fg3)
  const switcher = await styles(page.getByTestId('company-switcher'), ['color', 'background-color'])
  expect(switcher.color, 'company switcher text equals the aside --fg-1').toBe(fg1)
  const switcherRatio = contrast(switcher.color, switcher['background-color'])
  expect(switcherRatio, 'company switcher text contrast against its own fill').toBeGreaterThanOrEqual(4.5)

  const labels = aside(page).locator('nav .label')
  expect(await labels.count(), 'the firm sidebar draws two group labels').toBeGreaterThanOrEqual(2)
  const labelReadings: { color: string; ratio: number; scope: string; scopeColor: string }[] = []
  for (const label of await labels.all()) {
    const color = (await styles(label, ['color'])).color
    const scope = label.locator('span').nth(1)
    labelReadings.push({ color, ratio: contrast(color, SHELL_DARK.background), scope: (await scope.textContent()) ?? '', scopeColor: (await styles(scope, ['color'])).color })
  }
  for (const r of labelReadings) {
    expect(r.color, 'group label colour').toBe('rgb(191, 211, 199)')
    expect(r.ratio, 'group label contrast against the aside').toBeGreaterThanOrEqual(4.5)
    expect(r.scopeColor, `scope text ${r.scope} equals the aside --fg-3`).toBe(fg3)
  }
  expect(labelReadings[0].scope, 'the first group is the client scope').toContain('CLIENT')

  const badges = aside(page).locator('nav button.pf-nav .mono')
  await expect.poll(() => badges.count(), { message: 'the firm sidebar draws at least one nav badge once the counts load', timeout: 15_000 }).toBeGreaterThanOrEqual(1)
  const badgeCount = await badges.count()
  for (const badge of await badges.all()) expect(await radii(badge), 'nav badge corners').toEqual(Array(4).fill('4px'))

  const before = (await styles(signOut, ['background-color']))['background-color']
  await signOut.hover()
  await expect
    .poll(async () => (await styles(signOut, ['background-color']))['background-color'], { message: 'Sign out hover fill' })
    .toBe(SHELL_DARK.activeBg)
  expect(before, 'the rest fill differs from the hover fill, so the hover read is a change').not.toBe(SHELL_DARK.activeBg)
  await page.mouse.move(700, 450)

  Object.assign(measured, { header, track, sandbox, mark: markReading, fg1, fg3, switcher: { ...switcher, ratio: switcherRatio }, labels: labelReadings, badgeCount, signOutRest: before })
  await attachJson(testInfo, 'as-01-measurements', measured)
  await attachShot(page, testInfo, 'shell-firm')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AS-02 in-house shell at 1440: aside, active row, ERP chip', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInAs(page, 'inhouse')
  await settle(page, aside(page), page.locator('main.pf-main'))
  const measured = await readAside(page)

  const chip = page.getByTestId('company-chip').getByText('ERP', { exact: true }).locator('xpath=..')
  await expect(chip, 'the ?persona= sign-in is not a hand-off, so the company chip draws its ERP chip').toBeVisible()
  const chipRadii = await radii(chip)
  expect(chipRadii, 'ERP chip corners').toEqual(Array(4).fill('4px'))

  await attachJson(testInfo, 'as-02-measurements', { ...measured, erpChip: chipRadii })
  await attachShot(page, testInfo, 'shell-inhouse')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AS-03 header row fits at every wide width', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInAs(page, 'firm')
  const header = page.locator('main.pf-main > header')
  const parts: Record<string, Locator> = {
    crumb: header.locator('> div').first(),
    search: header.locator('.pf-header-search'),
    'env-pill': page.getByTestId('env-pill'),
    'new-invoice': header.getByRole('button', { name: 'New invoice' }),
  }
  const track = page.getByTestId('env-pill')
  const segments = track.locator('button')
  expect(await segments.count(), 'the switch draws two segments').toBe(2)

  const entry = page.viewportSize()
  const measured: Record<string, unknown>[] = []
  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 900 })
      const read = async (): Promise<{ problems: string[]; rects: Record<string, Rect> }> => {
        await settle(page, header, ...Object.values(parts), segments.first(), segments.last())
        const rects: Record<string, Rect> = {}
        const problems: string[] = []
        const outer = await header.boundingBox()
        if (!outer) return { problems: ['header has no box'], rects }
        for (const [name, loc] of Object.entries(parts)) {
          const box = await loc.boundingBox()
          if (!box) problems.push(`${name} has no box`)
          else rects[name] = box
        }
        const trackBox = await track.boundingBox()
        for (const [i, seg] of [segments.first(), segments.last()].entries()) {
          const box = await seg.boundingBox()
          if (!box || !trackBox) problems.push(`segment ${i} or the track has no box`)
          else {
            rects[`segment-${i}`] = box
            if (!enclosesRect(trackBox, box, 1)) problems.push(`segment ${i} sticks out of the track`)
          }
        }
        const names = Object.keys(parts).filter((n) => rects[n])
        for (const n of names) if (!enclosesRect(outer, rects[n], 1)) problems.push(`${n} sticks out of the header`)
        for (let i = 0; i < names.length; i++)
          for (let j = i + 1; j < names.length; j++)
            if (rectsOverlap(rects[names[i]], rects[names[j]])) problems.push(`${names[i]} overlaps ${names[j]}`)
        return { problems, rects }
      }
      await expect.poll(async () => (await read()).problems, { message: `header row at ${width}px`, timeout: 10_000 }).toEqual([])
      measured.push({ width, ...(await read()).rects })
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }
  await attachJson(testInfo, 'as-03-measurements', measured)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AS-04 environment banner: sandbox rendered, live by probe', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInAs(page, 'firm')
  await settle(page, page.locator('main.pf-main'))

  const banner = page.getByTestId('env-banner')
  const box = await styles(banner, ['background-color', 'border-bottom-color'])
  const text = await styles(banner.locator('span').nth(1), ['color'])
  expect(box['background-color'], 'sandbox banner background').toBe('rgb(244, 227, 200)')
  expect(box['border-bottom-color'], 'sandbox banner border').toBe('rgb(227, 203, 168)')
  expect(text.color, 'sandbox banner text').toBe('rgb(116, 84, 33)')

  // LIVE cannot render while its segment is disabled, so the live banner is read from a probe carrying its three tokens.
  const live = await page.locator('main.pf-main').evaluate((main) => {
    const probe = document.createElement('div')
    probe.style.cssText = 'background: var(--action-tint); border: 1px solid var(--teal-200); color: var(--action-soft)'
    main.appendChild(probe)
    const cs = getComputedStyle(probe)
    const out = { background: cs.backgroundColor, border: cs.borderTopColor, color: cs.color }
    probe.remove()
    return out
  })
  expect(live, 'live banner tokens').toEqual({ background: 'rgba(7, 60, 61, 0.1)', border: 'rgb(203, 241, 221)', color: 'rgb(12, 55, 56)' })

  await attachJson(testInfo, 'as-04-measurements', { sandbox: { ...box, text: text.color }, live })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AS-05 headings: Manrope 700 on a probe h1, Manrope on the dashboard h1, Plex Mono loaded', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInAs(page, 'firm')
  await expect(page.locator('main.pf-main .pf-scroll h1:visible').first(), 'the dashboard drew no h1').toBeVisible()
  await settle(page, page.locator('main.pf-main'))

  // Every screen h1 still carries an inline 600 (RESKIN2-02..06 own those files), so the 700 rule is read on an unstyled probe.
  const probe = await page.locator('main.pf-main .pf-scroll').evaluate(async (scroll) => {
    const h1 = document.createElement('h1')
    h1.textContent = 'probe'
    scroll.appendChild(h1)
    await document.fonts.ready
    const cs = getComputedStyle(h1)
    const out = { family: cs.fontFamily, weight: cs.fontWeight }
    h1.remove()
    return out
  })
  expect(firstFamily(probe.family), 'probe h1 family').toBe('Manrope')
  expect(probe.weight, 'probe h1 weight').toBe('700')

  const real = await styles(page.locator('main.pf-main .pf-scroll h1:visible').first(), ['font-family', 'font-weight'])
  expect(firstFamily(real['font-family']), 'dashboard h1 family').toBe('Manrope')

  const faces = await page.evaluate(() => [...document.fonts].map((f) => ({ family: f.family.replace(/["']/g, ''), status: f.status })))
  expect(
    faces.filter((f) => f.family === 'IBM Plex Mono' && f.status === 'loaded').length,
    'a loaded IBM Plex Mono face (the sidebar draws mono text, so the face is used)',
  ).toBeGreaterThan(0)

  await attachJson(testInfo, 'as-05-measurements', { probe, dashboardH1: real, faces })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

type Sweep = { scanned: number; unresolved: string[]; buttons: number; chips: number; badButtons: string[]; badChips: string[]; h1Family: string }

// One evaluate per screen: unresolved inline var(), and the corner of every visible .pf-btn / .pf-chip.
function sweepScreen(page: Page, plant: boolean): Promise<Sweep> {
  return page.evaluate((plant) => {
    const shell = document.querySelector('.pf-shell')
    if (!shell) throw new Error('no .pf-shell')
    let planted: HTMLElement | null = null
    if (plant) {
      planted = document.createElement('span')
      planted.setAttribute('style', 'color: var(--rk-undefined)')
      shell.appendChild(planted)
    }
    const unresolved: string[] = []
    let scanned = 0
    for (const el of shell.querySelectorAll<HTMLElement>('[style*="var("]')) {
      const names = [...(el.getAttribute('style') ?? '').matchAll(/var\(\s*(--[\w-]+)\s*\)/g)].map((m) => m[1])
      if (names.length === 0) continue
      scanned += 1
      const cs = getComputedStyle(el)
      for (const name of names) if (cs.getPropertyValue(name).trim() === '') unresolved.push(`${el.tagName.toLowerCase()}.${el.className} ${name}`)
    }
    planted?.remove()

    const visible = (el: Element) => {
      const r = el.getBoundingClientRect()
      return r.width > 0 && r.height > 0 && getComputedStyle(el).visibility === 'visible'
    }
    const corners = (el: Element) => {
      const cs = getComputedStyle(el)
      return [cs.borderTopLeftRadius, cs.borderTopRightRadius, cs.borderBottomRightRadius, cs.borderBottomLeftRadius]
    }
    const offenders = (sel: string, want: string) => {
      const all = [...shell.querySelectorAll(sel)].filter(visible)
      const bad = all.filter((el) => corners(el).some((c) => c !== want)).map((el) => `${el.tagName.toLowerCase()} "${(el.textContent ?? '').trim().slice(0, 24)}" ${corners(el).join('/')}`)
      return { count: all.length, bad }
    }
    const buttons = offenders('.pf-btn', '7px')
    const chips = offenders('.pf-chip', '4px')
    const h1 = [...document.querySelectorAll('main.pf-main .pf-scroll h1')].find(visible)
    return {
      scanned,
      unresolved,
      buttons: buttons.count,
      chips: chips.count,
      badButtons: buttons.bad,
      badChips: chips.bad,
      h1Family: h1 ? getComputedStyle(h1).fontFamily : '',
    }
  }, plant)
}

test('AS-06 every nav destination of both modes: no sideways scroll, no unresolved var(), v2 corners', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)
  const api = trackApi(page)
  const report: Record<string, unknown>[] = []
  let chipTotal = 0

  for (const [persona, floor] of [['firm', 10], ['inhouse', 8]] as const) {
    await test.step(`${persona} nav sweep`, async () => {
      await signInAs(page, persona)
      const nav = navButtons(page)
      const count = await nav.count()
      expect(count, `${persona} nav destinations`).toBeGreaterThanOrEqual(floor)
      for (let i = 0; i < count; i++) {
        const label = ((await nav.nth(i).innerText()) ?? '').replace(/\d+$/, '').trim()
        await openNav(page, nav.nth(i))
        const where = `${persona} / ${label}`
        await api.quiet(where)
        if (persona === 'firm' && /^Invoices/.test(label)) {
          await expect(page.getByTestId('invoices-pager'), `${where}: the pager must be on screen before the sweep, or the sweep runs on a skeleton`).toBeVisible()
        }
        await settle(page, page.locator('main.pf-main .pf-scroll'))
        const scroll = await assertPageDoesNotScrollSideways(page, where)

        if (i === 0) {
          const control = await sweepScreen(page, true)
          expect(control.unresolved.some((u) => u.includes('--rk-undefined')), `${where}: the planted --rk-undefined must be reported, or the scan is blind`).toBe(true)
        }
        const s = await sweepScreen(page, false)
        expect(s.scanned, `${where}: scanned elements`).toBeGreaterThanOrEqual(20)
        expect(s.unresolved, `${where}: inline var() with no value`).toEqual([])
        expect(firstFamily(s.h1Family), `${where}: h1 family`).toBe('Manrope')
        expect(s.buttons, `${where}: visible .pf-btn (the header's New invoice is one)`).toBeGreaterThanOrEqual(1)
        expect(s.badButtons, `${where}: .pf-btn corners`).toEqual([])
        expect(s.badChips, `${where}: .pf-chip corners`).toEqual([])
        chipTotal += s.chips
        report.push({ where, scroll, scanned: s.scanned, buttons: s.buttons, chips: s.chips })
      }
    })
  }

  expect(chipTotal, 'visible .pf-chip across the sweep, or the 4px read covers nothing').toBeGreaterThanOrEqual(1)
  await attachJson(testInfo, 'as-06-measurements', { chipTotal, screens: report })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AS-07 the held sign-in card: radius, fill, border, no shadow', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  let release!: () => void
  const gate = new Promise<void>((resolve) => (release = resolve))
  let held = false
  await page.route(ME_URL, async (route) => {
    const req = route.request()
    // Only the app's own /me is held; the landing and gateway calls of the sign-in pass through.
    if (req.method() !== 'GET' || held || !req.frame().url().startsWith(APP_URL)) return route.continue()
    held = true
    await gate
    await route.continue()
  })

  const signing = signInAs(page, 'firm')
  const signed = signing.then(() => null, (e: unknown) => e)
  try {
    await expect.poll(() => held, { timeout: 60_000, message: 'the first app GET /me was never held, so the card is not the in-flight one' }).toBe(true)
    const text = page.getByText('Opening your workspace…', { exact: true })
    await expect(text, 'the hand-off loading card is up while /me is held').toBeVisible()
    const card = text.locator('xpath=../..')
    await settle(page, card)
    const reading = await styles(card, ['border-top-left-radius', 'background-color', 'border-top-color', 'box-shadow'])
    expect(reading['border-top-left-radius'], 'card radius').toBe('10px')
    expect(reading['background-color'], 'card fill').toBe('rgb(255, 255, 255)')
    expect(reading['border-top-color'], 'card border').toBe('rgb(201, 217, 214)')
    expect(reading['box-shadow'], 'card shadow').toBe('none')

    await attachJson(testInfo, 'as-07-measurements', reading)
    await attachShot(page, testInfo, 'loading')
  } finally {
    release()
  }
  expect(await signed, 'the sign-in after the held /me').toBeNull()
  await page.unroute(ME_URL)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AS-08 the suspended card: radius and Sign out corner', async ({ page }, testInfo) => {
  const errors = gatedErrors(page, [expectedStatusDropper(page, 403, /\/api\//)])
  await signInAs(page, 'firm')

  // The app calls the gateway cross-origin: pass the preflight through, answer the real request with the 403.
  const origin = new URL(APP_URL).origin
  await page.route(`${GATEWAY_URL}/api/**`, (route) => {
    if (route.request().method() === 'OPTIONS') return route.continue()
    return route.fulfill({ status: 403, contentType: 'application/json', headers: { 'access-control-allow-origin': origin }, body: NOT_ACTIVE_BODY })
  })
  await navButton(page, /^Invoices/).click()

  const card = page.getByTestId('suspended-notice')
  await expect(card, 'the 403 with the not-active body renders the suspended card').toBeVisible()
  await settle(page, card)
  const radius = await radii(card)
  const signOut = await radii(card.getByRole('button', { name: 'Sign out' }))
  expect(radius, 'suspended card corners').toEqual(Array(4).fill('10px'))
  expect(signOut, 'suspended Sign out corners').toEqual(Array(4).fill('7px'))

  await attachJson(testInfo, 'as-08-measurements', { card: radius, signOut })
  await attachShot(page, testInfo, 'suspended')
  await page.unroute(`${GATEWAY_URL}/api/**`)
  expect(errors, `console errors beyond the deliberate 403:\n${errors.join('\n')}`).toEqual([])
})

test('AS-09 persona popover and trigger', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInAs(page, 'firm')
  const trigger = page.getByTestId('persona-trigger')
  await trigger.click()
  const popover = page.getByTestId('persona-popover')
  await expect(popover).toBeVisible()
  await expect(page.getByTestId('persona-row-list'), 'the roster drew, so the popover is not its loading state').toBeVisible()
  await settle(page, popover, trigger)

  const p = await styles(popover, ['background-color', 'box-shadow', ...CORNERS])
  expect(p['background-color'], 'popover background').toBe('rgb(255, 255, 255)')
  expect(p['border-top-left-radius'], 'popover radius').toBe('6px')
  expect(p['box-shadow'], 'popover shadow').toBe(SHADOW_CARD)
  expect(await popover.getAttribute('class'), 'popover scope class').toContain('asc-light')

  const t = await styles(trigger, ['color', 'background-color', 'border-top-left-radius'])
  const fg1 = await resolveColor(aside(page), '--fg-1')
  expect(t['border-top-left-radius'], 'trigger radius').toBe('7px')
  expect(t.color, 'trigger text equals the aside --fg-1').toBe(fg1)
  const ratio = contrast(t.color, t['background-color'])
  expect(ratio, 'trigger text contrast against its own fill').toBeGreaterThanOrEqual(4.5)

  await attachJson(testInfo, 'as-09-measurements', { popover: p, trigger: t, fg1, ratio })
  await attachShot(page, testInfo, 'persona-popover')
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AS-10 persona toast after a switch', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInAs(page, 'firm')
  const signedInAs = ((await page.getByTestId('persona-name').textContent()) ?? '').trim()
  expect(signedInAs, 'the signed-in persona name drew').not.toBe('')

  await page.getByTestId('persona-trigger').click()
  await expect(page.getByTestId('persona-row-list')).toBeVisible()
  // A blocked row is a div, so a suspended seeded member is never picked; the seeded names are not assumed.
  const others = page.locator('button[data-testid="persona-row"]').filter({ hasNotText: signedInAs })
  expect(await others.count(), 'at least one enabled member other than the signed-in one').toBeGreaterThanOrEqual(1)
  await others.first().click()

  const toast = page.getByTestId('persona-toast')
  await expect(toast).toBeVisible()
  await settle(page, toast)
  const t = await styles(toast, ['background-color', 'box-shadow', 'border-left-width', 'border-left-color', 'border-top-left-radius'])
  const title = await styles(page.getByTestId('persona-toast-title'), ['font-family'])
  const meta = await styles(page.getByTestId('persona-toast-meta'), ['font-family'])
  await attachShot(page, testInfo, 'persona-toast')

  expect(t['background-color'], 'toast background').toBe('rgb(255, 255, 255)')
  expect(t['box-shadow'], 'toast shadow').toBe(SHADOW_CARD)
  expect(t['border-left-width'], 'toast left edge width').toBe('3px')
  expect(t['border-left-color'], 'toast left edge colour').toBe('rgb(116, 84, 33)')
  expect(t['border-top-left-radius'], 'toast radius').toBe('6px')
  expect(firstFamily(title['font-family']), 'toast title family').toBe('Manrope')
  expect(firstFamily(meta['font-family']), 'toast meta family').toBe('IBM Plex Mono')

  await attachJson(testInfo, 'as-10-measurements', { switchedFrom: signedInAs, toast: t, title, meta })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('AS-11 entity modal, filter popover and pager', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInAs(page, 'firm')

  await openNav(page, navButton(page, /^Clients/))
  const add = page.getByRole('button', { name: /Add client/ })
  await expect(add, 'Add client is disabled until the client list resolves').toBeEnabled()
  await add.click()
  const panel = page.getByRole('dialog')
  await expect(panel).toBeVisible()
  const scrim = panel.locator('xpath=..')
  const input = panel.locator('input.pf-input').first()
  await input.focus()
  await settle(page, panel, scrim)
  const modal = await styles(panel, ['border-top-left-radius', 'box-shadow'])
  const scrimReading = await styles(scrim, ['background-color', 'backdrop-filter'])
  expect(modal['border-top-left-radius'], 'modal panel radius').toBe('10px')
  expect(modal['box-shadow'], 'modal panel shadow').toBe(SHADOW_CARD)
  const c = parseColor(scrimReading['background-color'])
  for (const [channel, want] of [['r', 8], ['g', 47], ['b', 49]] as const) {
    expect(Math.abs(c[channel] - want), `scrim ${channel} channel ${c[channel]} vs --surface ${want}`).toBeLessThanOrEqual(1)
  }
  expect(Math.abs(c.a - 0.55), 'scrim alpha within 0.01').toBeLessThanOrEqual(0.01)
  expect(scrimReading['backdrop-filter'], 'scrim backdrop-filter').toBe('blur(6px)')
  await expect
    .poll(async () => (await styles(input, ['border-top-color']))['border-top-color'], { message: 'the focused input border (--ring)' })
    .toBe('rgb(56, 135, 126)')
  await panel.getByRole('button', { name: 'Cancel' }).click()
  await expect(panel, 'Cancel closed the modal without a submit').toBeHidden()

  await openNav(page, navButton(page, /^Audit/))
  const actor = page.getByTestId('audit-actor-trigger')
  await expect(actor, 'the Actor trigger is disabled while the audit request is in flight').toBeEnabled()
  await actor.click()
  const filter = page.getByTestId('audit-actor-panel')
  await expect(filter).toBeVisible()
  await settle(page, filter, actor)
  const filterReading = await styles(filter, ['box-shadow', 'border-top-left-radius'])
  const actorRadius = await radii(actor)
  expect(filterReading['box-shadow'], 'filter panel shadow').toBe(SHADOW_CARD)
  expect(filterReading['border-top-left-radius'], 'filter panel radius').toBe('6px')
  expect(actorRadius, 'filter trigger corners').toEqual(Array(4).fill('7px'))
  await actor.click()

  await openNav(page, navButton(page, /^Invoices/))
  const pager = page.getByTestId('invoices-pager')
  await expect(pager, 'the pager renders only with rows, and the seeded firm tenant has invoices').toBeVisible()
  await settle(page, pager)
  const pagerButtons = pager.locator('button')
  expect(await pagerButtons.count(), 'the pager draws Previous and Next').toBe(2)
  const pagerRadii = await Promise.all((await pagerButtons.all()).map((b) => radii(b)))
  for (const r of pagerRadii) expect(r, 'pager button corners').toEqual(Array(4).fill('7px'))

  await attachJson(testInfo, 'as-11-measurements', { modal, scrim: scrimReading, filter: filterReading, actorRadius, pagerRadii })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})
