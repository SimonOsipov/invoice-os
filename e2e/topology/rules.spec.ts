// The app Rules screen on the deployed build: the server's golden set, the admin's custom-rule
// journey (a warning rule on the active client only, D16/D24) and the screen's layout relations.
// The non-admin view has no browser spec (D18).
import { test, expect, type Locator, type Page } from '@playwright/test'
import {
  createCustomRule,
  createEntity,
  deleteCustomRule,
  getGoldenRules,
  listCustomRules,
  login,
  type CustomRule,
  type Entity,
  type Persona,
} from '../api/client'
import { freshTin } from '../api/fixtures'
import { browserToken, signInAs } from '../personaSession'
import { expectedStatusDropper, type Dropper } from './consoleGate'
import { assertSameHeight, enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'
import { shardTenants } from './targets'

const SHARD = shardTenants('rules.spec.ts')
const ADMIN: Persona = { ...SHARD.a, tenantId: SHARD.a.id }

// internal/validation maxDraftMessageRunes.
const MESSAGE_MAX_RUNES = 500
const CUSTOM_RULES_URL = /\/api\/invoice\/v1\/custom-rules(\/[^/?]+)?$/

const RUN = Date.now()
const LONG_PREFIX = `ENGI-15 long ${RUN} `
const LONG_MESSAGE = `${LONG_PREFIX}${'w'.repeat(150)} ${'word '.repeat(80)}`.slice(0, MESSAGE_MAX_RUNES)
const SHORT_MESSAGE = `ENGI-15 fixture ${RUN}`
// Long enough to wrap in the scope radio and the switcher.
const ENTITY_NAME = `ENGI-15 Rules ${RUN} Consolidated Holdings and Trading Partnership of West Africa`

let admin: string
let entity: Entity

test.beforeAll(async () => {
  expect(test.info().project.name, 'rules.spec.ts belongs to the invoice-surfaces shard').toBe('invoice-surfaces')
  admin = await login(ADMIN)
  entity = await createEntity(admin, { name: ENTITY_NAME, tin: freshTin() })
  for (const message of [SHORT_MESSAGE, LONG_MESSAGE]) {
    await createCustomRule(admin, {
      entity_id: entity.id,
      type: 'required',
      target: 'invoice_number',
      params: {},
      severity: 'warning',
      message,
      enabled: true,
    })
  }
})

// Every rule written for this spec's client is removed, whichever test wrote it.
test.afterAll(async () => {
  const mine = (await listCustomRules(admin)).filter((r: CustomRule) => r.entity_id === entity.id)
  for (const r of mine) await deleteCustomRule(admin, r.id).catch(() => undefined)
})

function collectErrors(page: Page, droppers: Dropper[] = []): string[] {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    if (droppers.some((drop) => drop(msg.text(), msg.location().url))) return
    errors.push(msg.text())
  })
  page.on('pageerror', (err) => errors.push(`pageerror: ${err.message}`))
  return errors
}

async function selectEntity(page: Page): Promise<void> {
  await page.getByTestId('company-switcher').click()
  await page.getByTestId('company-switcher-option').filter({ hasText: ENTITY_NAME }).click()
}

async function showRules(page: Page): Promise<void> {
  await page.locator('aside.pf-sidebar nav.pf-nav-list').getByRole('button', { name: /^Rules/ }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Rules', exact: true })).toBeVisible()
  await expect(page.getByTestId('rules-new')).toBeEnabled()
}

async function openRules(page: Page): Promise<void> {
  await signInAs(page, 'firm', { tenantId: SHARD.a.id })
  await selectEntity(page)
  await showRules(page)
}

async function reopenRules(page: Page): Promise<void> {
  await page.reload()
  await selectEntity(page)
  await expect(page.getByTestId('rules-new')).toBeEnabled()
}

const newButton = (page: Page) => page.getByTestId('rules-new')
const headerBar = (page: Page) => newButton(page).locator('xpath=../..')
const tableCard = (page: Page) => headerBar(page).locator('xpath=..')
const ruleRow = (page: Page, text: string) => tableCard(page).locator('.pf-row').filter({ hasText: text })

async function box(l: Locator): Promise<Rect | null> {
  await settleAnimations(l)
  return l.boundingBox()
}

// Runs `probe` at each WIDE_WIDTHS entry; the probe returns null when the relation holds, else a description.
async function atEachWidth(page: Page, label: string, probe: () => Promise<string | null>): Promise<void> {
  const entry = page.viewportSize()
  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })
      await expect.poll(() => page.evaluate(() => window.innerWidth), { timeout: 5_000 }).toBe(width)
      await expect
        .poll(async () => (await probe()) ?? 'ok', { message: `${label} at ${width}px`, timeout: 10_000 })
        .toBe('ok')
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }
}

const noSideScroll = (el: Element) => el.scrollWidth - el.clientWidth

async function openEditor(page: Page): Promise<Locator> {
  await newButton(page).click()
  const drawer = page.getByRole('dialog', { name: 'New custom rule' })
  await expect(drawer).toBeVisible()
  return drawer
}

// Core AC 1: the screen shows the server's version and rows.
test('the Rules screen reads the server', async ({ page }) => {
  test.setTimeout(90_000)
  const errors = collectErrors(page)
  await openRules(page)

  const golden = await getGoldenRules(await browserToken(page))
  const version = page.getByText(`v${golden.version}`, { exact: true })
  await expect(version).toBeVisible()
  await expect(version.locator('xpath=following-sibling::span').getByText('IN USE', { exact: true })).toBeVisible()
  await expect(page.getByText(new RegExp(`^GOLDEN v${golden.version} \\+ \\d+ CUSTOM$`))).toBeVisible()
  await expect(page.getByText('LOCKED', { exact: true })).toHaveCount(golden.rules.length)
  await expect(page.getByText('Suggested for you')).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// Core AC 2-5, 7, 8. The 400 is the refusal under test.
test('an admin writes, switches, edits and removes a custom rule', async ({ page }) => {
  test.setTimeout(180_000)
  const errors = collectErrors(page, [expectedStatusDropper(page, 400, CUSTOM_RULES_URL)])
  const message = `ENGI-15 journey ${RUN}`
  const edited = `${message} edited`
  try {
    await openRules(page)

    const drawer = await openEditor(page)
    await drawer.getByRole('combobox', { name: 'Field', exact: true }).selectOption({ label: 'Invoice number' })
    await drawer.getByRole('combobox', { name: 'Check', exact: true }).selectOption({ label: 'is filled in' })
    await drawer.getByRole('radio', { name: 'Warn' }).check()
    await drawer.getByTestId('rule-scope-client').check()
    await drawer.getByRole('textbox', { name: 'Message', exact: true }).fill(message)
    await drawer.getByTestId('rule-save').click()
    await expect(drawer).toBeHidden()
    const row = ruleRow(page, message)
    await expect(row).toBeVisible()
    await expect(row.getByText('CLIENT', { exact: true })).toBeVisible()

    const toggle = row.getByRole('switch')
    await expect(toggle).toHaveAttribute('aria-checked', 'true')
    await toggle.click()
    await expect(toggle).toHaveAttribute('aria-checked', 'false')
    await reopenRules(page)
    await expect(ruleRow(page, message).getByRole('switch')).toHaveAttribute('aria-checked', 'false')
    await ruleRow(page, message).getByRole('switch').click()
    await expect(ruleRow(page, message).getByRole('switch')).toHaveAttribute('aria-checked', 'true')

    await ruleRow(page, message).getByText('Custom rule', { exact: true }).click()
    await page.getByTestId('rule-edit').click()
    const edit = page.getByRole('dialog', { name: 'Edit custom rule' })
    await edit.getByRole('textbox', { name: 'Message', exact: true }).fill(edited)
    await edit.getByTestId('rule-save').click()
    await expect(edit).toBeHidden()
    await expect(ruleRow(page, edited)).toBeVisible()
    await reopenRules(page)
    await expect(ruleRow(page, edited)).toBeVisible()

    const bad = await openEditor(page)
    await bad.getByTestId('rule-mode-expression').click()
    await bad.getByRole('textbox', { name: 'Expression', exact: true }).fill('invoice.total > 5')
    await bad.getByRole('textbox', { name: 'Message', exact: true }).fill(`${message} bad`)
    await bad.getByTestId('rule-save').click()
    await expect(bad.getByTestId('rule-save-error')).toContainText('no such key')
    await bad.getByRole('button', { name: 'Cancel' }).click()
    await expect(bad).toBeHidden()

    await ruleRow(page, edited).getByText('Custom rule', { exact: true }).click()
    await page.getByTestId('rule-remove').click()
    await expect(ruleRow(page, edited)).toHaveCount(0)
    await reopenRules(page)
    await expect(ruleRow(page, edited)).toHaveCount(0)
  } finally {
    const mine = (await listCustomRules(admin)).filter((r) => r.message.startsWith(message))
    for (const r of mine) await deleteCustomRule(admin, r.id).catch(() => undefined)
  }

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// Core AC 2: New rule sits in the header bar, clear of the title pill and the count.
test('the header bar holds New rule', async ({ page }) => {
  test.setTimeout(90_000)
  const errors = collectErrors(page)
  await openRules(page)
  const bar = headerBar(page)
  const pill = bar.getByText(/^GOLDEN v\d+ \+ \d+ CUSTOM$/)
  const count = bar.getByText(/^\d+ RULES$/)

  await atEachWidth(page, 'New rule inside the header bar', async () => {
    const [b, btn, p, c] = await Promise.all([box(bar), box(newButton(page)), box(pill), box(count)])
    if (!b || !btn || !p || !c) return 'an element has no box'
    if (!enclosesRect(b, btn, 1)) return `New rule ${JSON.stringify(btn)} leaves the bar ${JSON.stringify(b)}`
    if (rectsOverlap(btn, p)) return 'New rule overlaps the title pill'
    if (rectsOverlap(btn, c)) return 'New rule overlaps the count'
    const centerGap = Math.abs(btn.y + btn.height / 2 - (c.y + c.height / 2))
    return centerGap <= 1 ? null : `New rule is ${centerGap}px off the count's line`
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// Core AC 3: the On column is reachable without scrolling the card sideways.
test('every switch is visible without scrolling', async ({ page }) => {
  test.setTimeout(90_000)
  const errors = collectErrors(page)
  await openRules(page)
  const card = tableCard(page)
  await expect(card.getByRole('switch').first()).toBeVisible()

  await atEachWidth(page, 'every custom switch inside the table card', async () => {
    const cardBox = await box(card)
    const switches = await card.getByRole('switch').all()
    if (!cardBox || switches.length < 2) return `card box ${cardBox ? 'ok' : 'missing'}, ${switches.length} switches`
    for (const sw of switches) {
      const b = await box(sw)
      if (!b || !enclosesRect(cardBox, b, 1)) return `switch ${JSON.stringify(b)} is outside the card ${JSON.stringify(cardBox)}`
    }
    const scrollLeft = await card.evaluate((el) => el.scrollLeft)
    return scrollLeft === 0 ? null : `the card is scrolled ${scrollLeft}px`
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// Core AC 2: the Source label stays in its column.
test('the source label fits its column', async ({ page }) => {
  test.setTimeout(90_000)
  const errors = collectErrors(page)
  await openRules(page)
  const labels = tableCard(page).getByText('CLIENT', { exact: true })
  await expect(labels.first()).toBeVisible()

  await atEachWidth(page, 'the CLIENT label inside its grid cell', async () => {
    const readings = await labels.evaluateAll((els) =>
      els.map((el) => {
        const range = document.createRange()
        range.selectNodeContents(el)
        const next = el.nextElementSibling?.getBoundingClientRect()
        return { textRight: range.getBoundingClientRect().right, cellRight: el.getBoundingClientRect().right, nextLeft: next?.left ?? null }
      }),
    )
    if (readings.length === 0) return 'no CLIENT label'
    for (const r of readings) {
      if (r.textRight > r.cellRight + 0.5) return `label text ends at ${r.textRight}, past its cell at ${r.cellRight}`
      if (r.nextLeft !== null && r.textRight > r.nextLeft + 0.5) return `label text ends at ${r.textRight}, inside the message cell at ${r.nextLeft}`
    }
    return null
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// Core AC 4, 5: a 500-rune message and a long expression stay inside their containers.
test('long text stays contained', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page, [expectedStatusDropper(page, 400, CUSTOM_RULES_URL)])
  await openRules(page)
  const card = tableCard(page)
  const row = ruleRow(page, LONG_PREFIX.trim())
  await expect(row).toBeVisible()

  await row.getByText('Custom rule', { exact: true }).click()
  const detail = page.getByRole('dialog', { name: /^Rule custom:/ })
  await expect(detail).toBeVisible()
  await atEachWidth(page, 'the 500-rune rule inside the card and its drawer', async () => {
    const [cardBox, rowBox, drawerBox, json] = await Promise.all([box(card), box(row), box(detail), box(detail.locator('pre.pf-json'))])
    if (!cardBox || !rowBox || !drawerBox || !json) return 'an element has no box'
    if (!enclosesRect(cardBox, rowBox, 1)) return `row ${JSON.stringify(rowBox)} leaves the card ${JSON.stringify(cardBox)}`
    if ((await card.evaluate((el) => el.scrollLeft)) !== 0) return 'the card is scrolled sideways'
    if ((await detail.evaluate(noSideScroll)) > 0) return 'the drawer scrolls sideways'
    return enclosesRect(drawerBox, json, 1) ? null : `the JSON block ${JSON.stringify(json)} leaves the drawer ${JSON.stringify(drawerBox)}`
  })
  await detail.getByRole('button', { name: 'Close' }).click()
  await expect(detail).toBeHidden()

  // 392 unbroken characters: the server's refusal echoes the missing key.
  const drawer = await openEditor(page)
  await drawer.getByTestId('rule-mode-expression').click()
  await drawer.getByRole('textbox', { name: 'Expression', exact: true }).fill(`invoice.${'x'.repeat(392)}`)
  await drawer.getByRole('textbox', { name: 'Message', exact: true }).fill(LONG_MESSAGE)
  await drawer.getByTestId('rule-save').click()
  const refusal = drawer.getByTestId('rule-save-error')
  await expect(refusal).toBeVisible()
  await atEachWidth(page, 'the long expression and the refusal inside the editor drawer', async () => {
    const [drawerBox, errBox, area] = await Promise.all([
      box(drawer),
      box(refusal),
      box(drawer.getByRole('textbox', { name: 'Expression', exact: true })),
    ])
    if (!drawerBox || !errBox || !area) return 'an element has no box'
    if ((await drawer.evaluate(noSideScroll)) > 0) return 'the drawer scrolls sideways'
    if (!enclosesRect(drawerBox, errBox, 1)) return 'the refusal row leaves the drawer'
    return enclosesRect(drawerBox, area, 1) ? null : 'the expression box leaves the drawer'
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// Core AC 3: every control of both editor tabs lies inside the drawer.
test('the editor fits its drawer', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)
  await openRules(page)
  const drawer = await openEditor(page)

  for (const mode of ['guided', 'expression'] as const) {
    await drawer.getByTestId(`rule-mode-${mode}`).click()
    await atEachWidth(page, `every ${mode} control inside the drawer`, async () => {
      const drawerBox = await box(drawer)
      const controls = await drawer.locator('input, select, textarea, button, label').all()
      if (!drawerBox || controls.length === 0) return 'no drawer or no controls'
      for (const c of controls) {
        const b = await c.boundingBox()
        if (b && !enclosesRect(drawerBox, b, 1)) return `a control ${JSON.stringify(b)} leaves the drawer ${JSON.stringify(drawerBox)}`
      }
      return (await drawer.evaluate(noSideScroll)) > 0 ? 'the drawer scrolls sideways' : null
    })
  }

  // Sibling controls stand the same height (Save/Cancel, the two tabs).
  await assertSameHeight(page, drawer.getByRole('button', { name: 'Cancel' }), drawer.getByTestId('rule-save'), 'Cancel and Save')
  await assertSameHeight(page, drawer.getByTestId('rule-mode-guided'), drawer.getByTestId('rule-mode-expression'), 'the editor tabs')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// Fixture check: page.route fails the toggle's PUT, so the row-error line draws; it pins the layout, not a contract.
test('(fixture check) a failed switch prints its error under its own row', async ({ page }) => {
  test.setTimeout(90_000)
  const sentence = `The rule could not be saved. ${'because the server said no '.repeat(8)}`
  const errors = collectErrors(page, [expectedStatusDropper(page, 500, CUSTOM_RULES_URL)])
  await page.route(CUSTOM_RULES_URL, async (route) => {
    if (route.request().method() !== 'PUT') return route.fallback()
    await route.fulfill({ status: 500, json: { error: sentence } })
  })
  await openRules(page)
  const row = ruleRow(page, SHORT_MESSAGE)
  await row.getByRole('switch').click()
  const failure = page.getByTestId('rule-row-error')
  await expect(failure).toContainText('The rule could not be saved.')

  await atEachWidth(page, 'the row error under its own row', async () => {
    const [r, e] = await Promise.all([box(row), box(failure)])
    if (!r || !e) return 'an element has no box'
    if (Math.abs(r.x - e.x) > 1 || Math.abs(r.width - e.width) > 1) return `error ${JSON.stringify(e)} is not the row's width ${JSON.stringify(r)}`
    return rectsOverlap(r, e) ? 'the error overlaps its row' : null
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
