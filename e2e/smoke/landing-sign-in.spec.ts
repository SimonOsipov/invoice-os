import { test, expect, type Locator, type Page } from '@playwright/test'
import { resolveTarget } from '../targets'
import { enclosesRect, gaps, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from '../topology/layout'
import { seedConsent } from './landingConsent'

// The sign-in dialog holds the form and no account chooser. Its card must stay inside the
// viewport, stay centred, and keep the form's controls inside it on a short phone. Geometry
// has no jsdom oracle; this is behaviour, not a visual diff.

const LANDING_URL = resolveTarget('LANDING_URL')

const VIEWPORTS = [
  { width: 390, height: 667 },
  { width: 1280, height: 800 },
  { width: 1440, height: 900 },
] as const

// A state in the app's shape (43 base64url characters): landing only checks the format.
const STATE = 'A'.repeat(43)

for (const viewport of VIEWPORTS) {
  test(`landing sign-in modal: usable at ${viewport.width}x${viewport.height}`, async ({ page }) => {
    const errors: string[] = []
    page.on('console', (msg) => {
      if (msg.type() === 'error') errors.push(msg.text())
    })
    page.on('pageerror', (err) => errors.push(`pageerror: ${err.message}`))
    await page.setViewportSize(viewport)
    await seedConsent(page, false)

    // signin=ready opens the modal with the form, as the app's start bounce does.
    await page.goto(`${LANDING_URL}/?state=${STATE}&signin=ready`)
    const dialog = page.getByRole('dialog', { name: 'Sign in', exact: true })
    await expect(dialog).toBeVisible()

    const card = dialog.locator(':scope > div')
    await expect(card).toHaveCount(1)
    const viewportRect: Rect = { x: 0, y: 0, width: viewport.width, height: viewport.height }
    // The entry animation moves the card; wait for its box to settle inside the viewport.
    await expect
      .poll(async () => {
        const box = await card.boundingBox()
        return box != null && enclosesRect(viewportRect, box)
      }, { message: `the card overflows ${viewport.width}x${viewport.height}` })
      .toBe(true)

    await settleAnimations(card)
    const cardBox = await card.boundingBox()
    expect(cardBox, 'the card has no box').not.toBeNull()
    const side = gaps(cardBox!, viewportRect)
    expect(Math.abs(side.left - side.right), `the card is off-centre at ${viewport.width}x${viewport.height}: ${JSON.stringify(side)}`).toBeLessThanOrEqual(1)

    // The card scrolls its own content, so x is the only axis a control must stay inside it on.
    const xOnly = (r: Rect): Rect => ({ x: r.x, y: 0, width: r.width, height: 1 })
    const controls = {
      'email input': dialog.getByLabel('Work email', { exact: true }),
      'password input': dialog.getByLabel('Password', { exact: true }),
      'Sign in button': dialog.getByRole('button', { name: 'Sign in →', exact: true }),
    }
    for (const [name, control] of Object.entries(controls)) {
      await control.scrollIntoViewIfNeeded()
      await expect(control, `the ${name} is not visible`).toBeVisible()
      const box = await control.boundingBox()
      expect(box, `the ${name} has no box`).not.toBeNull()
      expect(enclosesRect(xOnly(cardBox!), xOnly(box!), 0.5), `the ${name} leaves the card at ${viewport.width}x${viewport.height}: ${JSON.stringify({ cardBox, box })}`).toBe(true)
    }
    await expect(controls['Sign in button']).toBeInViewport()

    const overflow = await card.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
    expect(overflow.scrollWidth, `the card scrolls sideways at ${viewport.width}x${viewport.height}: ${JSON.stringify(overflow)}`).toBeLessThanOrEqual(overflow.clientWidth + 1)

    // The form is visible above, so these absences are not vacuous.
    await expect(dialog.locator('[data-persona]')).toHaveCount(0)
    await expect(dialog.getByText('Choose an account')).toHaveCount(0)

    expect(errors, `console errors on the landing page:\n${errors.join('\n')}`).toEqual([])
  })
}

const SIGN_IN_ROUTE = `${LANDING_URL}/?state=${STATE}&signin=ready`
const WIDTHS = [...WIDE_WIDTHS, 390] as const
const VIEWS = ['sign-in', 'forgot'] as const
// frontend/landing/src/passwordReset.ts RESET_SENT.
const RESET_SENT = 'If this address has an account, a reset link is on its way.'
// frontend/landing/src/components/SignInForm.tsx PEACH_NOTICE_STYLE: token --accent.
const PEACH = 'rgb(245, 188, 136)'

async function openSignIn(page: Page, view: (typeof VIEWS)[number], width: number) {
  await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
  await seedConsent(page, false)
  await page.goto(SIGN_IN_ROUTE)
  const dialog = page.getByRole('dialog', { name: 'Sign in', exact: true })
  await expect(dialog).toBeVisible()
  if (view === 'forgot') await dialog.getByRole('button', { name: 'Forgot password?', exact: true }).click()
  const card = dialog.locator(':scope > div')
  await expect(card).toHaveCount(1)
  await settleAnimations(card)
  return { dialog, card }
}

const rectOf = (l: Locator) => l.evaluate((el) => {
  const { x, y, width, height } = el.getBoundingClientRect()
  return { x, y, width, height }
})

test('landing sign-in window: the sign-up line is centred', async ({ page }) => {
  for (const view of VIEWS) {
    for (const width of WIDTHS) {
      const at = `${view} view at ${width}px`
      const { dialog, card } = await openSignIn(page, view, width)
      // getByText resolves to the row itself: its own text node holds the sentence, the button is a child.
      const row = dialog.getByText('New to ASComply?')
      await expect(row.getByRole('button', { name: 'Create an account', exact: true }), `${at}: the matched element is not the sign-up row`).toBeVisible()
      await expect(row, `${at}: the sign-up row is missing`).toBeVisible()
      const cardBox = (await card.boundingBox())!
      // The text extent is a Range over the row's children; a left-aligned line leaves left gap ~0 and right gap > 0.
      const { rowBox, textBox } = await row.evaluate((el) => {
        const r = document.createRange()
        r.selectNodeContents(el)
        const t = r.getBoundingClientRect()
        const b = el.getBoundingClientRect()
        return { rowBox: { x: b.x, width: b.width }, textBox: { x: t.x, y: t.y, width: t.width, height: t.height } }
      })
      const side = gaps(textBox, rowBox)
      expect(Math.abs(side.left - side.right), `${at}: the sign-up text is off-centre: ${JSON.stringify(side)}`).toBeLessThanOrEqual(1)
      expect(enclosesRect(cardBox, textBox, 1), `${at}: the sign-up text leaves the card: ${JSON.stringify({ cardBox, textBox })}`).toBe(true)
    }
  }
})

test('landing forgot view: the reset notice is peach, readable and clear of the controls', async ({ page }) => {
  // CORS: the gateway is another origin, so the stub answers the preflight as well.
  await page.route('**/auth/request-password-reset', (r) =>
    r.fulfill({
      status: r.request().method() === 'OPTIONS' ? 204 : 200,
      headers: { 'access-control-allow-origin': '*', 'access-control-allow-headers': '*', 'access-control-allow-methods': 'POST, OPTIONS' },
      body: r.request().method() === 'OPTIONS' ? '' : '{}',
    }),
  )
  for (const width of WIDTHS) {
    const at = `forgot view at ${width}px`
    const { dialog, card } = await openSignIn(page, 'forgot', width)
    await dialog.getByLabel('Work email', { exact: true }).fill('reset-check@example.com')
    const submit = dialog.getByRole('button', { name: 'Send reset link', exact: true })
    const back = dialog.getByRole('button', { name: 'Back to sign in', exact: true })
    await submit.click()
    const notice = dialog.getByRole('status').locator('p')
    await expect(notice, `${at}: the notice text`).toHaveText(RESET_SENT)

    const colours = await notice.evaluate((el) => {
      const parse = (c: string) => (c.match(/[\d.]+/g) ?? []).slice(0, 3).map(Number)
      const lum = (c: string) => {
        const [r, g, b] = parse(c).map((v) => {
          const x = v / 255
          return x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4
        })
        return 0.2126 * r + 0.7152 * g + 0.0722 * b
      }
      const cs = getComputedStyle(el)
      const [hi, lo] = [lum(cs.color), lum(cs.backgroundColor)].sort((a, b) => b - a)
      return { bg: cs.backgroundColor, ratio: (hi + 0.05) / (lo + 0.05) }
    })
    expect(colours.bg, `${at}: the notice background`).toBe(PEACH)
    expect(colours.ratio, `${at}: the notice contrast`).toBeGreaterThanOrEqual(4.5)

    const [cardBox, noticeBox, submitBox, backBox] = await Promise.all([rectOf(card), rectOf(notice), rectOf(submit), rectOf(back)])
    expect(enclosesRect(cardBox, noticeBox, 1), `${at}: the notice leaves the card: ${JSON.stringify({ cardBox, noticeBox })}`).toBe(true)
    expect(rectsOverlap(noticeBox, submitBox), `${at}: the notice overlaps Send reset link`).toBe(false)
    expect(rectsOverlap(noticeBox, backBox), `${at}: the notice overlaps Back to sign in`).toBe(false)
  }
})
