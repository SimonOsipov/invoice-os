import { expect, test, type Page } from '@playwright/test'
import { resolveTarget } from '../targets'
import { enclosesRect, gaps, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from '../topology/layout'
import { seedConsent } from './landingConsent'

// Geometry of the registration window and the verify notice on the deployed landing.
// Opens the window and types; never submits, so no account is created.

const LANDING_URL = resolveTarget('LANDING_URL')

const PHONE = { width: 390, height: 667 } as const
const CREATE = 'Create an account'

function gate(page: Page): string[] {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(msg.text())
  })
  page.on('pageerror', (err) => errors.push(`pageerror: ${err.message}`))
  return errors
}

async function openLanding(page: Page, query = ''): Promise<string[]> {
  const errors = gate(page)
  await seedConsent(page, false)
  const res = await page.goto(`${LANDING_URL}/${query}`)
  expect(res?.ok(), `/${query} returned HTTP ${res?.status()}`).toBeTruthy()
  await expect(page.getByRole('banner')).toBeVisible()
  await page.evaluate(() => document.fonts.ready.then(() => true))
  return errors
}

async function openRegister(page: Page) {
  await page.setViewportSize({ width: 1440, height: 1080 })
  const errors = await openLanding(page)
  const entry = page.getByRole('banner').getByRole('button', { name: CREATE })
  await expect(entry, `the header "${CREATE}" entry is missing (is landing.VITE_REGISTRATION_OPEN on?)`).toBeVisible()
  await entry.click()
  const dialog = page.getByRole('dialog', { name: CREATE })
  await expect(dialog).toBeVisible()
  const card = dialog.locator(':scope > div')
  await expect(card).toHaveCount(1)
  return { errors, dialog, card }
}

test('landing registration window: the card is enclosed and centred at every width', async ({ page }, testInfo) => {
  const { errors, card } = await openRegister(page)

  const viewports = [...WIDE_WIDTHS.map((width) => ({ width, height: 1080 })), PHONE]
  const measured: Array<{ width: number; height: number; cardWidth: number; left: number; right: number }> = []
  for (const viewport of viewports) {
    await page.setViewportSize(viewport)
    const at = `${viewport.width}x${viewport.height}`
    await expect
      .poll(async () => {
        await settleAnimations(card)
        const box = await card.boundingBox()
        if (!box) return null
        const side = gaps(box, { x: 0, width: viewport.width })
        const fits = enclosesRect({ x: 0, y: 0, width: viewport.width, height: viewport.height }, box, 0.5)
        const above = box.y
        const below = viewport.height - (box.y + box.height)
        return fits && Math.abs(side.left - side.right) <= 1 && Math.abs(above - below) <= 1
      }, { message: `the registration card is not enclosed and centred (both axes) at ${at}` })
      .toBe(true)
    const box = (await card.boundingBox())!
    const side = gaps(box, { x: 0, width: viewport.width })
    measured.push({ ...viewport, cardWidth: box.width, left: side.left, right: side.right })
  }

  await testInfo.attach('register-card-fit.json', { body: JSON.stringify(measured, null, 2), contentType: 'application/json' })
  expect(measured.length, 'one reading per width').toBe(WIDE_WIDTHS.length + 1)
  expect(measured[0].width, 'the widest width is read first').toBe(2560)
  for (const m of measured) expect(m.cardWidth, `the card has no width at ${m.width}`).toBeGreaterThan(0)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('landing registration window: at 390x667 the card scrolls itself and the page behind does not', async ({ page }) => {
  const { errors, dialog, card } = await openRegister(page)
  await page.setViewportSize(PHONE)
  await settleAnimations(card)
  const scrollY0 = await page.evaluate(() => window.scrollY)

  // An empty submit fails validation before any request, and lengthens the card with its alerts.
  const submit = dialog.getByRole('button', { name: 'Create account →' })
  await submit.click()
  await expect(dialog.getByRole('alert').first()).toBeVisible()

  await submit.scrollIntoViewIfNeeded()
  const overflow = await card.evaluate((el) => ({ scrollHeight: el.scrollHeight, clientHeight: el.clientHeight }))
  expect(overflow.scrollHeight, 'the card has no overflow to scroll at 390x667').toBeGreaterThan(overflow.clientHeight + 1)

  const [cardBox, submitBox] = await Promise.all([card.boundingBox(), submit.boundingBox()])
  expect(cardBox, 'the card has no box').not.toBeNull()
  expect(submitBox, 'the submit has no box').not.toBeNull()
  expect(enclosesRect(cardBox!, submitBox!, 0.5), 'the card does not enclose the submit').toBe(true)
  await expect(submit).toBeInViewport()
  expect(await page.evaluate(() => window.scrollY), 'the page behind the window scrolled').toBe(scrollY0)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('landing registration window: a long name and a long email stay inside the card', async ({ page }, testInfo) => {
  const { errors, dialog, card } = await openRegister(page)
  // Unbroken text is the widest case an input can hold.
  await dialog.getByLabel('Work email').fill(`${'a'.repeat(120)}@${'b'.repeat(60)}.example.com`)
  await dialog.getByLabel('Password').fill('p'.repeat(120))
  await dialog.getByLabel('Your name').fill('N'.repeat(160))
  await dialog.getByLabel('Workspace name').fill('W'.repeat(160))

  const measured: unknown[] = []
  for (const viewport of [{ width: 1440, height: 1080 }, PHONE]) {
    await page.setViewportSize(viewport)
    await settleAnimations(card)
    const at = `${viewport.width}x${viewport.height}`
    const cardBox = (await card.boundingBox())!
    const fields = dialog.locator('input:not([type=radio]):not([type=checkbox])')
    const count = await fields.count()
    expect(count, `${at}: text fields in the window`).toBe(4)
    const boxes: Rect[] = []
    for (let i = 0; i < count; i++) {
      const b = await fields.nth(i).boundingBox()
      expect(b, `${at}: field ${i} has no box`).not.toBeNull()
      // The card scrolls its own height, so x is the axis a field must stay inside it on.
      expect(
        enclosesRect({ x: cardBox.x, y: 0, width: cardBox.width, height: 1 }, { x: b!.x, y: 0, width: b!.width, height: 1 }, 0.5),
        `${at}: field ${i} leaves the card: ${JSON.stringify({ cardBox, field: b })}`,
      ).toBe(true)
      boxes.push(b!)
    }
    const overflow = await card.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
    expect(overflow.scrollWidth, `${at}: the card scrolls sideways: ${JSON.stringify(overflow)}`).toBeLessThanOrEqual(overflow.clientWidth + 1)
    expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth), `${at}: the page scrolls sideways`).toBeLessThanOrEqual(0)
    measured.push({ ...viewport, cardBox, boxes, overflow })
  }

  await testInfo.attach('register-long-values.json', { body: JSON.stringify(measured, null, 2), contentType: 'application/json' })
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('landing registration window: the marketing box is unticked, named by its sentence and reached and toggled from the keyboard', async ({ page }) => {
  const { errors, dialog } = await openRegister(page)
  const box = dialog.getByRole('checkbox')
  await expect(box, 'the marketing box is the only checkbox').toHaveCount(1)
  await expect(box).not.toBeChecked()
  await expect(box, 'its name is its sentence alone').toHaveAccessibleName(/^Allow marketing communications: .+ unsubscribe at any time\.$/)
  await expect(dialog.getByText('We will email you about your account and the service.'), 'the notice is plain text').toBeVisible()
  await expect(dialog.getByText('We will email you about your account and the service.').locator('xpath=ancestor-or-self::*[self::label or self::button or self::a]')).toHaveCount(0)

  await dialog.getByLabel('Workspace name').focus()
  await page.keyboard.press('Tab')
  await expect(dialog.getByRole('radio').first(), 'Tab from the last field reaches the kind radios').toBeFocused()
  await page.keyboard.press('Tab')
  await expect(box, 'the next stop after the radios is the marketing box').toBeFocused()
  await page.keyboard.press('Space')
  await expect(box).toBeChecked()
  await page.keyboard.press('Space')
  await expect(box).not.toBeChecked()
  await page.keyboard.press('Tab')
  await expect(dialog.getByRole('button', { name: 'Create account →' }), 'the stop after the box is the submit').toBeFocused()
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

test('landing registration window: the marketing row, its box and the notice keep their relationships at 1440 and 390', async ({ page }) => {
  const { errors, dialog, card } = await openRegister(page)
  const email = dialog.getByLabel('Work email')
  const notice = dialog.getByText('We will email you about your account and the service.')
  const submit = dialog.getByRole('button', { name: 'Create account →' })

  for (const viewport of [{ width: 1440, height: 1080 }, PHONE]) {
    await page.setViewportSize(viewport)
    const at = `${viewport.width}px`
    await settleAnimations(card)
    await notice.scrollIntoViewIfNeeded()
    await page.evaluate(() => new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r()))))
    const m = await page.evaluate(() => {
      const rect = (r: DOMRect) => ({ x: r.x, y: r.y, width: r.width, height: r.height })
      const lines = (node: Node) => {
        const range = document.createRange()
        range.selectNodeContents(node)
        return Array.from(range.getClientRects()).filter((r) => r.width > 0)
      }
      const box = document.querySelector('[role="dialog"] input[type=checkbox]')!
      const row = box.closest('label')!
      // Only the label's own text nodes: a text run under the box must not be filtered out.
      const labelText = Array.from(row.childNodes).filter((n) => n.nodeType === Node.TEXT_NODE).flatMap(lines)
      const noticeLines = lines(Array.from(document.querySelectorAll('[role="dialog"] p')).find((p) => p.textContent?.startsWith('We will email you'))!)
      return {
        box: rect(box.getBoundingClientRect()),
        row: rect(row.getBoundingClientRect()),
        labelLines: labelText.length,
        textLeft: Math.min(...labelText.map((r) => r.left)),
        noticeLines: noticeLines.length,
        noticeTextMid: (Math.min(...noticeLines.map((r) => r.left)) + Math.max(...noticeLines.map((r) => r.right))) / 2,
      }
    })
    const [cardBox, emailBox, noticeBox, submitBox] = await Promise.all([card.boundingBox(), email.boundingBox(), notice.boundingBox(), submit.boundingBox()])
    for (const [name, b] of [['card', cardBox], ['email', emailBox], ['notice', noticeBox], ['submit', submitBox]] as const) {
      expect(b, `${at}: the ${name} has no box`).not.toBeNull()
    }
    expect(Math.abs(m.row.x - emailBox!.x), `${at}: marketing row left ${m.row.x} vs email input left ${emailBox!.x}`).toBeLessThanOrEqual(0.5)
    expect(m.labelLines, `${at}: the marketing label has no text to measure`).toBeGreaterThan(0)
    expect(m.noticeLines, `${at}: the notice has no text to measure`).toBeGreaterThan(0)
    expect(m.textLeft, `${at}: the checkbox overlaps its label text`).toBeGreaterThanOrEqual(m.box.x + m.box.width)
    // The notice's text, not its box: a full-width <p> is centred on the button whatever its text-align.
    const submitMid = submitBox!.x + submitBox!.width / 2
    expect(Math.abs(m.noticeTextMid - submitMid), `${at}: notice text centre ${m.noticeTextMid} vs submit centre ${submitMid}`).toBeLessThanOrEqual(1)
    expect(noticeBox!.y, `${at}: the notice is not below the submit`).toBeGreaterThanOrEqual(submitBox!.y + submitBox!.height - 1)
    for (const [name, b] of [['marketing row', m.row], ['notice', noticeBox!], ['submit', submitBox!]] as const) {
      expect(enclosesRect(cardBox!, b, 1), `${at}: the ${name} is not enclosed by the card`).toBe(true)
    }
  }
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

// The route for the 1121-1219px band, where the header entry is hidden.
test('landing sign-in window: its Create an account link opens the registration window at 1121', async ({ page }) => {
  await page.setViewportSize({ width: 1121, height: 900 })
  const errors = await openLanding(page)
  await expect(page.getByRole('banner').getByRole('button', { name: CREATE }), 'the header entry shows at 1121').toBeHidden()

  await page.getByRole('banner').getByRole('button', { name: 'Platform login' }).click()
  const signIn = page.getByRole('dialog', { name: 'Platform login' })
  await expect(signIn).toBeVisible()
  const link = signIn.getByRole('button', { name: CREATE })
  await expect(link, `the sign-in window has no "${CREATE}" link (is landing.VITE_REGISTRATION_OPEN on?)`).toBeVisible()
  await link.click()

  await expect(page.getByRole('dialog', { name: CREATE })).toBeVisible()
  await expect(signIn, 'the sign-in window stays open under the registration window').toHaveCount(0)
  expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
})

const NOTICES = [
  { query: '?verified=1', text: 'Your email address is verified' },
  { query: '?verify=failed', text: 'That link did not work' },
] as const

for (const notice of NOTICES) {
  test(`landing verify notice (${notice.query}): under the header, on its container edges, above the hero`, async ({ page }, testInfo) => {
    const errors = await openLanding(page, notice.query)
    const band = page.getByRole('status').filter({ hasText: notice.text })
    await expect(band, `no verify notice for ${notice.query}`).toBeVisible()

    const viewports = [...WIDE_WIDTHS.map((width) => ({ width, height: 1080 })), { width: 390, height: 844 }]
    const measured: unknown[] = []
    for (const viewport of viewports) {
      await page.setViewportSize(viewport)
      await page.evaluate(() => new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r()))))
      const at = `${viewport.width}px`
      const m = await page.evaluate(() => {
        const header = document.querySelector('header')!
        const container = header.querySelector('.container')!
        const cs = getComputedStyle(container)
        const c = container.getBoundingClientRect()
        const band = document.querySelector('[role=status]')!.getBoundingClientRect()
        return {
          headerBottom: header.getBoundingClientRect().bottom,
          contentLeft: c.left + parseFloat(cs.paddingLeft),
          contentRight: c.right - parseFloat(cs.paddingRight),
          band: { left: band.left, right: band.right, top: band.top, bottom: band.bottom },
          heroTop: document.querySelector('#top')!.getBoundingClientRect().top,
        }
      })
      measured.push({ ...viewport, ...m })
      expect(m.band.top, `${at}: band top vs header bottom`).toBeGreaterThanOrEqual(m.headerBottom - 1)
      expect(Math.abs(m.band.left - m.contentLeft), `${at}: band left ${m.band.left} vs header content left ${m.contentLeft}`).toBeLessThanOrEqual(1)
      expect(Math.abs(m.band.right - m.contentRight), `${at}: band right ${m.band.right} vs header content right ${m.contentRight}`).toBeLessThanOrEqual(1)
      expect(m.heroTop, `${at}: hero top vs band bottom`).toBeGreaterThanOrEqual(m.band.bottom - 1)
      const bandBox = (await band.boundingBox())!
      expect(rectsOverlap(bandBox, (await page.locator('#top').boundingBox())!), `${at}: the band overlaps the hero`).toBe(false)
    }

    await testInfo.attach('verify-notice.json', { body: JSON.stringify(measured, null, 2), contentType: 'application/json' })
    expect(measured.length, 'one reading per width').toBe(WIDE_WIDTHS.length + 1)
    expect(errors, `console errors:\n${errors.join('\n')}`).toEqual([])
  })
}
