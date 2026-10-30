// The Support Console Blog screen on the PR environment: layout relationships at every wide width, and real posts and categories written and deleted.
// The SUP-BLOG-06 tests create, pin, publish and delete their own blog posts and categories, and restore the featured post.
import { randomBytes } from 'node:crypto'
import { test, expect, type Locator, type Page, type Route, type TestInfo } from '@playwright/test'
import { apiBase, provisionStaffAccount, rawFetch, signInSession, type StaffAccount } from '../api/client'
import { shoot } from '../argos'
import { collectErrors } from '../personaSession'
import { CONSOLE_SESSION_KEY, consoleUrl, seedStaffSession } from '../staffSession'
import { resolveTarget } from '../targets'
import { expectedStatusDropper, type Dropper } from './consoleGate'
import { enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'

test.beforeAll(() => { expect(test.info().project.name, 'blog-editor.spec.ts belongs to the ui-surfaces shard').toBe('ui-surfaces') })

test.use({ viewport: { width: 1440, height: 900 } })

let staff: Promise<StaffAccount> | undefined
const staffAccount = (): Promise<StaffAccount> => (staff ??= provisionStaffAccount('blog-editor'))
const signInSupport = async (page: Page): Promise<unknown> => seedStaffSession(page, 'support', await staffAccount())

// Fonts settled and two frames painted; the named elements and their ancestors finish their animations.
async function settle(page: Page, ...targets: Locator[]): Promise<void> {
  await page.evaluate(async () => {
    await document.fonts.ready
    await new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r())))
  })
  await settleAnimations(...targets)
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

const aside = (page: Page) => page.locator('aside.ops-sidebar')
const navButton = (page: Page, label: RegExp) => aside(page).locator('nav button.ops-nav', { hasText: label })
const header = (page: Page) => page.locator('main header')
const pad = (page: Page) => page.locator('.ops-screen-pad')
const banner = (page: Page) => header(page).locator('xpath=following-sibling::div[1]')
const firstRow = (page: Page) => page.locator('.ops-row').first()

const errorsOf = (errors: string[]) => `console errors:\n${errors.join('\n')}`

// ---------------------------------------------------------------- SUP-BLOG-05
// Fixture-free: reads whatever posts the PR environment holds.

test('SUP-BLOG-05 blog list: header, filters and table hold together at every wide width', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInSupport(page)
  await navButton(page, /^Blog/).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Blog', exact: true }), 'Blog drew no h1').toBeVisible()
  await expect(firstRow(page), 'the Blog list drew no post row').toBeVisible({ timeout: 15_000 })
  await settle(page, pad(page))
  await shoot(page, 'support', 'blog', { mask: [page.locator('[data-blog-table] .ops-row')] })

  const table = page.locator('[data-blog-table]')
  const cells = (row: Locator) => row.locator('xpath=./*')
  const widths = await atWidths(page, 'SUP-BLOG-05 blog list', async () => {
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
  await attachJson(testInfo, 'sup-blog-05-blog-list', { widths })
  expect(errors, errorsOf(errors)).toEqual([])
})

test('SUP-BLOG-05 blog categories list: rows hold together at every wide width', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInSupport(page)
  await navButton(page, /^Blog/).click()
  await expect(firstRow(page), 'the Blog list drew no post row').toBeVisible({ timeout: 15_000 })
  const tab = pad(page).getByRole('button', { name: /Categories/ })
  await tab.click()
  const rowsLoc = page.locator('[data-cat-row]')
  await expect(rowsLoc.first(), 'the Categories tab drew no category row').toBeVisible({ timeout: 15_000 })
  await settle(page, pad(page))

  const widths = await atWidths(page, 'SUP-BLOG-05 blog categories list', async () => {
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
  await attachJson(testInfo, 'sup-blog-05-blog-categories-list', { widths })
  expect(errors, errorsOf(errors)).toEqual([])
})

test('SUP-BLOG-05 blog editor: the two panes fill the main column side by side at every wide width', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInSupport(page)
  await navButton(page, /^Blog/).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Blog', exact: true }), 'Blog drew no h1').toBeVisible()
  await page.getByRole('button', { name: 'New post' }).click()
  const editor = page.locator('[data-blog-editor]')
  await expect(editor, 'New post opened no editor').toBeVisible()
  await settle(page, editor)

  const scroller = editor.locator('xpath=..')
  const panes = page.locator('[data-bscroll]')
  const fields = editor.locator('[data-blog-fields]')
  const widths = await atWidths(page, 'SUP-BLOG-05 blog editor', async () => {
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
  await attachJson(testInfo, 'sup-blog-05-blog-editor', { widths })
  expect(errors, errorsOf(errors)).toEqual([])
})

test('SUP-BLOG-05 blog preview: the article and the card fit the preview pane at every wide width', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInSupport(page)
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
  const widths = await atWidths(page, 'SUP-BLOG-05 blog preview article', async () => {
    const r = await boxes(page, { pane, cover: pane.locator('[data-preview-cover]') })
    const toc = await boxList(page, entries, 'TOC entry', 2)
    const problems = [...within(r.pane, r.cover, 'cover box in preview pane'), ...toc.flatMap((t, i) => within(r.pane, t, `TOC entry ${i} in preview pane`))]
    return { problems, rects: { ...r, toc } }
  })

  await pane.getByRole('button', { name: 'Card', exact: true }).click()
  const card = pane.locator('[data-preview-card]')
  await expect(card, 'the Card tab drew no card').toBeVisible()
  await settle(page, card)
  const cardWidths = await atWidths(page, 'SUP-BLOG-05 blog preview card', async () => {
    const r = await boxes(page, { pane, card })
    return { problems: within(r.pane, r.card, 'card in preview pane'), rects: r }
  })
  await attachJson(testInfo, 'sup-blog-05-blog-preview', { widths, cardWidths })
  expect(errors, errorsOf(errors)).toEqual([])
})

test.describe('SUP-BLOG-05 blog categories fixture', () => {
  // The rows this test created; the afterEach deletes the post, then the category.
  let own: { catId: string | null; postId: string | null }
  test.beforeEach(() => {
    own = { catId: null, postId: null }
  })
  test.afterEach(async () => {
    const token = await staffToken()
    if (own.postId) await rawFetch(`${BLOG}/posts/${own.postId}`, { method: 'DELETE', headers: bearer(token) })
    if (own.catId) await rawFetch(`${BLOG}/categories/${own.catId}`, { method: 'DELETE', headers: bearer(token) })
  })

  test('SUP-BLOG-05 blog categories form and delete confirm: controls stay inside at every wide width', async ({ page }, testInfo) => {
    const errors = collectErrors(page)
    const token = await staffToken()
    const hex = randomBytes(4).toString('hex')
    const prefix = `e2e-cat-layout-${hex}-`
    const made = await rawFetch(`${BLOG}/categories`, {
      method: 'POST',
      headers: bearer(token),
      body: { name: prefix + 'x'.repeat(120), headline: 'Layout probe', intro: '', icon: 'globe', tone: 0 },
    })
    expect(made.status, 'create the long-named category').toBe(201)
    own.catId = (made.body as { id: string }).id
    const draft = await rawFetch(`${BLOG}/posts`, { method: 'POST', headers: bearer(token), body: { title: `e2e-cat-layout-post-${hex}`, category_id: own.catId, status: 'draft', body: [] } })
    expect(draft.status, 'create the draft post in it').toBe(201)
    own.postId = (draft.body as { id: string }).id

    await signInSupport(page)
    await navButton(page, /^Blog/).click()
    await expect(firstRow(page), 'the Blog list drew no post row').toBeVisible({ timeout: 15_000 })
    await pad(page).getByRole('button', { name: /Categories/ }).click()
    const row = page.locator('[data-cat-row]', { hasText: prefix })
    await expect(row, 'the Categories tab drew no row for the long-named category').toBeVisible({ timeout: 15_000 })
    const line = row.locator('xpath=./div[1]')
    const frame = page.locator('[data-blog-cats]')
    const form = page.locator('[data-cat-form]')
    const confirm = page.locator('[data-cat-confirm]')

    const nameWidths = await atWidths(page, 'SUP-BLOG-05 blog category long name', async () => {
      const [, , name, count] = await boxList(page, row.locator('xpath=./div[1]/*'), 'long-named row cell', 5)
      const nameLine = row.locator('xpath=./div[1]/*[3]/div').first()
      const lines = await boxList(page, row.locator('xpath=./div[1]/*[3]/div'), 'long name line', 2)
      const overflow = await nameLine.evaluate((el) => el.scrollWidth - el.clientWidth)
      const problems = [
        ...leftOf(name, count, 'name cell / count'),
        ...lines.flatMap((l, k) => within(name, l, `name line ${k} / name cell`)),
        ...(overflow <= 1 ? [] : [`the long name overflows its line by ${overflow}px`]),
        ...(await noSidewaysScroll(page, 'long-named category row')),
      ]
      return { problems, rects: { name, count, lines } }
    })

    const formProblems = async (host: Locator, hostLabel: string, submit: string): Promise<{ problems: string[]; rects: Record<string, unknown> }> => {
      const r = await boxes(page, {
        host,
        form,
        cancel: form.getByRole('button', { name: 'Cancel', exact: true }),
        submit: form.getByRole('button', { name: submit, exact: true }),
      })
      const fields = await boxList(page, form.getByRole('textbox'), 'form field', 3)
      const icons = await boxList(page, form.locator('button[aria-pressed]:not([aria-label="Colour"])'), 'icon button', 16)
      const colours = await boxList(page, form.getByRole('button', { name: 'Colour' }), 'colour button', 6)
      const problems = [
        ...within(r.host, r.form, `form / ${hostLabel}`),
        ...within(r.form, r.cancel, 'Cancel / form'),
        ...within(r.form, r.submit, `${submit} / form`),
        ...apart(r.cancel, r.submit, `Cancel / ${submit}`),
        ...below(fields[2], icons[0], 'Intro / icon picker'),
        ...below(icons[icons.length - 1], r.cancel, 'icon picker / Cancel'),
        ...below(colours[colours.length - 1], r.cancel, 'colour picker / Cancel'),
        ...allApart(fields, 'fields'),
        ...allApart(icons, 'icon buttons'),
        ...allApart(colours, 'colour buttons'),
        ...allApart([...icons, ...colours], 'pickers'),
        ...(await noSidewaysScroll(page, hostLabel)),
      ]
      for (const [k, b] of [...fields, ...icons, ...colours].entries()) problems.push(...within(r.form, b, `control ${k} / form`))
      return { problems, rects: { ...r, fields, icons, colours } }
    }

    await row.getByRole('button', { name: 'Delete' }).click()
    await expect(confirm, 'Delete opened no confirm').toBeVisible()
    await expect(confirm.getByRole('button', { name: 'Delete category', exact: true }), 'the confirm has no Delete category').toBeVisible()
    const confirmWidths = await atWidths(page, 'SUP-BLOG-05 blog category delete confirm', async () => {
      const r = await boxes(page, {
        row,
        line,
        confirm,
        text: confirm.locator('span').first(),
        select: confirm.locator('select'),
        cancel: confirm.getByRole('button', { name: 'Cancel', exact: true }),
        del: confirm.getByRole('button', { name: 'Delete category', exact: true }),
      })
      const problems = [
        ...within(r.row, r.confirm, 'confirm / row'),
        ...below(r.line, r.confirm, 'row line / confirm'),
        ...within(r.confirm, r.text, 'confirm text / confirm'),
        ...within(r.confirm, r.select, 'select / confirm'),
        ...within(r.confirm, r.cancel, 'Cancel / confirm'),
        ...within(r.confirm, r.del, 'Delete category / confirm'),
        ...allApart([r.text, r.select, r.cancel, r.del], 'confirm controls'),
        ...(await noSidewaysScroll(page, 'category delete confirm')),
      ]
      return { problems, rects: r }
    })
    await confirm.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(confirm, 'Cancel left the confirm open').toHaveCount(0)

    await row.getByRole('button', { name: 'Edit' }).click()
    await expect(form, 'Edit opened no form').toBeVisible()
    const editWidths = await atWidths(page, 'SUP-BLOG-05 blog category edit form', () => formProblems(row, 'row', 'Save changes'))
    await form.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(form, 'Cancel left the edit form open').toHaveCount(0)

    await page.getByRole('button', { name: 'Add category', exact: true }).click()
    await expect(form, 'Add category opened no form').toBeVisible()
    const addWidths = await atWidths(page, 'SUP-BLOG-05 blog category add form', () => formProblems(frame, 'list frame', 'Add category'))
    await form.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(form, 'Cancel left the add form open').toHaveCount(0)

    await attachJson(testInfo, 'sup-blog-05-blog-categories-form', { nameWidths, confirmWidths, editWidths, addWidths })
    expect(errors, errorsOf(errors)).toEqual([])
  })
})

test('SUP-BLOG-05 blog action bar: actions stay inside the bar at every wide width', async ({ page }, testInfo) => {
  const errors = collectErrors(page)
  await signInSupport(page)
  await navButton(page, /^Blog/).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Blog', exact: true }), 'Blog drew no h1').toBeVisible()
  await page.getByRole('button', { name: 'New post' }).click()
  const editor = page.locator('[data-blog-editor]')
  await expect(editor, 'New post opened no editor').toBeVisible()
  await page.getByPlaceholder('What structured e-invoicing changes for…').fill('Action bar probe, never saved')
  const bar = editor.locator('xpath=./div[1]')
  await expect(bar.getByText('UNSAVED CHANGES', { exact: true }), 'typing a title left the editor clean').toBeVisible()
  await settle(page, editor)

  const widths = await atWidths(page, 'SUP-BLOG-05 blog action bar', async () => {
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
  const leaveWidths = await atWidths(page, 'SUP-BLOG-05 blog leave confirm', async () => {
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
  await attachJson(testInfo, 'sup-blog-05-blog-action-bar', { widths, leaveWidths })
  expect(errors, errorsOf(errors)).toEqual([])
})

// ---------------------------------------------------------------- SUP-BLOG-06
// Writes posts and categories of its own through the deployed console; reads nothing of any tenant.

const BLOG = '/api/notifications/v1/staff/blog'
const BLOG_POSTS = /\/api\/notifications\/v1\/staff\/blog\/posts(?:[/?]|$)/
const BLOG_CATEGORIES = /\/api\/notifications\/v1\/staff\/blog\/categories(?:[/?]|$)/
const AUTH_REFRESH = /\/auth\/refresh(?:[/?]|$)/
const KEPT_KEY = 'invoice-os.support-blog-draft' // frontend/support-console/src/blog.ts KEPT_KEY
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

test.describe('SUP-BLOG-06 blog writes', () => {
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

  test('SUP-BLOG-06 blog journey: list → new post → blocks → preview → save draft → publish → unpublish → delete', async ({ page }) => {
    test.setTimeout(180_000)
    const errors = blogErrors(page, expectedStatusDropper(page, 422, BLOG_POSTS))
    track(page)
    const title = `e2e blog ${randomBytes(4).toString('hex')}`
    const summary = 'Written by the deployed blog journey.'
    await signInSupport(page)
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

  test('SUP-BLOG-06 blog renewed session: one 401 on save renews the session in place and the save completes', async ({ page }) => {
    const errors = blogErrors(page, expectedStatusDropper(page, 401, BLOG_POSTS))
    track(page)
    const title = `e2e blog ${randomBytes(4).toString('hex')}`
    await signInSupport(page)
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
    const stored = () => page.evaluate((key) => localStorage.getItem(key), CONSOLE_SESSION_KEY.support)
    const before = await stored()
    expect(before, 'no stored session before the save').not.toBeNull()
    await page.getByRole('button', { name: 'Save draft', exact: true }).click()
    await expect(toastOf(page, 'Draft saved')).toBeVisible()
    expect(refused, 'the first save was never refused').toBe(true)
    expect(new URL(page.url()).origin, 'the console left the support origin').toBe(new URL(consoleUrl('support')).origin)
    expect(await stored(), 'the stored session was not renewed').not.toBe(before)
    expect(await page.evaluate((key) => localStorage.getItem(key), KEPT_KEY), 'a kept copy was stored').toBeNull()
    expect(errors, errorsOf(errors)).toEqual([])
  })

  test('SUP-BLOG-06 blog lost session: a 401 on save keeps the post and offers it after sign-in', async ({ page }) => {
    test.setTimeout(120_000)
    const errors = blogErrors(page, expectedStatusDropper(page, 401, BLOG_POSTS), expectedStatusDropper(page, 401, AUTH_REFRESH))
    track(page)
    const title = `e2e blog ${randomBytes(4).toString('hex')}`
    const summary = 'Kept across a lost session.'
    const account = await staffAccount()
    await signInSupport(page)
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

    const supportOrigin = new URL(consoleUrl('support')).origin
    const stateOf = await page.context().storageState()
    const kept = stateOf.origins.find((o) => o.origin === supportOrigin)?.localStorage.find((e) => e.name === KEPT_KEY)
    expect(kept, 'no kept copy at the support origin').toBeDefined()
    expect((JSON.parse(kept!.value) as { draft: { title: string } }).draft.title, 'kept title').toBe(title)

    const pair = await signInSession(account.email, account.password)
    await page.context().addInitScript(
      ({ origin, key, value }) => {
        if (location.origin !== origin || sessionStorage.getItem('e2e.staff-reseeded')) return
        sessionStorage.setItem('e2e.staff-reseeded', '1')
        localStorage.setItem(key, value)
      },
      { origin: supportOrigin, key: CONSOLE_SESSION_KEY.support, value: JSON.stringify({ v: 2, token: pair.access_token, refresh_token: pair.refresh_token }) },
    )
    await page.goto(consoleUrl('support'))
    await expect(titleInput(page), 'the kept draft did not reopen on Blog').toHaveValue(title, { timeout: 30_000 })
    await expect(summaryInput(page)).toHaveValue(summary)
    await expect(bar(page).getByText('UNSAVED CHANGES', { exact: true })).toBeVisible()
    expect(await page.evaluate((key) => localStorage.getItem(key), KEPT_KEY), 'the kept copy outlived its reopening').toBeNull()

    await page.getByRole('button', { name: 'Save draft', exact: true }).click()
    await expect(toastOf(page, 'Draft saved')).toBeVisible()
    expect(await page.evaluate((key) => localStorage.getItem(key), KEPT_KEY), 'the kept copy came back').toBeNull()
    expect(errors, errorsOf(errors)).toEqual([])
  })

  test('SUP-BLOG-06 blog failed save: a 500 leaves the editor dirty with a red toast', async ({ page }) => {
    const errors = blogErrors(page, expectedStatusDropper(page, 500, BLOG_POSTS))
    track(page)
    const title = `e2e blog ${randomBytes(4).toString('hex')}`
    await signInSupport(page)
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

type ServerCat = { id: string; name: string; headline: string; icon: string; tone: number; post_count: number }

const serverCats = async (token: string): Promise<ServerCat[]> => {
  const res = await rawFetch(`${BLOG}/categories`, { headers: bearer(token) })
  if (res.status !== 200) throw new Error(`GET categories answered ${res.status}`)
  return (res.body as { categories: ServerCat[] }).categories
}

test.describe('SUP-BLOG-06 blog categories', () => {
  // The rows this test created; the afterEach deletes the posts, then the categories.
  let own: { cats: string[]; posts: string[] }
  test.beforeEach(() => {
    own = { cats: [], posts: [] }
  })
  test.afterEach(async () => {
    const token = await staffToken()
    for (const id of own.posts) await rawFetch(`${BLOG}/posts/${id}`, { method: 'DELETE', headers: bearer(token) })
    for (const id of own.cats) await rawFetch(`${BLOG}/categories/${id}`, { method: 'DELETE', headers: bearer(token) })
  })

  const trackCats = (page: Page) =>
    page.on('response', (res) => {
      if (res.request().method() !== 'POST' || !res.ok() || !/\/staff\/blog\/categories\/?$/.test(new URL(res.url()).pathname)) return
      void res.json().then(
        (c: { id?: string }) => {
          if (c.id) own.cats.push(c.id)
        },
        () => {},
      )
    })
  const rawCat = async (token: string, name: string): Promise<ServerCat> => {
    const res = await rawFetch(`${BLOG}/categories`, { method: 'POST', headers: bearer(token), body: { name, headline: 'Probe', intro: '', icon: 'globe', tone: 0 } })
    expect(res.status, `create category ${name}`).toBe(201)
    const cat = res.body as ServerCat
    own.cats.push(cat.id)
    return cat
  }
  const openCategories = async (page: Page): Promise<void> => {
    await signInSupport(page)
    await navButton(page, /^Blog/).click()
    await expect(page.locator('[data-blog-table]'), 'the Blog list never finished loading').toBeVisible({ timeout: 15_000 })
    const refreshed = page.waitForResponse((r) => r.request().method() === 'GET' && new URL(r.url()).pathname.endsWith('/staff/blog/categories'), { timeout: 15_000 })
    await pad(page).getByRole('button', { name: /Categories/ }).click()
    await refreshed
    await expect(page.locator('[data-blog-cats]'), 'the Categories tab drew no list').toBeVisible({ timeout: 15_000 })
  }
  const catRow = (page: Page, name: string) => page.locator('[data-cat-row]').filter({ has: page.getByText(name, { exact: true }) })
  const rowButton = (row: Locator, name: string) => row.locator('xpath=./div[1]').getByRole('button', { name, exact: true })
  const uiOrder = async (page: Page, ...names: string[]): Promise<string[]> => {
    const texts = await page.locator('[data-cat-row]').evaluateAll((rows) => rows.map((r) => r.textContent ?? ''))
    return names.filter((n) => texts.some((t) => t.includes(n))).sort((a, b) => texts.findIndex((t) => t.includes(a)) - texts.findIndex((t) => t.includes(b)))
  }
  const serverOrder = async (token: string, ...ids: string[]): Promise<string[]> => {
    const all = (await serverCats(token)).map((c) => c.id)
    return ids.sort((a, b) => all.indexOf(a) - all.indexOf(b))
  }
  const tabCount = async (page: Page): Promise<number> => {
    const text = (await pad(page).getByRole('button', { name: /Categories/ }).textContent()) ?? ''
    return Number(/(\d+)\s*$/.exec(text)?.[1])
  }
  const isMove = (res: { url(): string; request(): { method(): string } }) => res.request().method() === 'POST' && /\/categories\/[^/]+\/move$/.test(res.url())

  test('SUP-BLOG-06 blog categories: add → refuse blank and duplicate → edit → move → delete with posts moved → delete empty', async ({ page }) => {
    test.setTimeout(180_000)
    const errors = blogErrors(page, expectedStatusDropper(page, 422, BLOG_CATEGORIES), expectedStatusDropper(page, 409, BLOG_CATEGORIES))
    trackCats(page)
    const moves: string[] = []
    page.on('request', (req) => {
      if (req.method() === 'POST' && /\/categories\/[^/]+\/move$/.test(req.url())) moves.push(req.url())
    })
    const token = await staffToken()
    const hex = randomBytes(4).toString('hex')
    const nameA = `e2e cat A ${hex}`
    const nameB = `e2e cat B ${hex}`
    const find = async (name: string): Promise<ServerCat> => {
      const c = (await serverCats(token)).find((x) => x.name === name)
      if (!c) throw new Error(`the server holds no category ${name}`)
      return c
    }
    const note = page.locator('[data-cat-head]').getByRole('button', { name: 'Add category', exact: true })
    const form = page.locator('[data-cat-form]')
    const submit = form.getByRole('button', { name: 'Add category', exact: true })
    const confirm = page.locator('[data-cat-confirm]')
    const add = async (name: string, icon: string, tone: number): Promise<void> => {
      await note.click()
      await expect(form.getByText('New category', { exact: true }), 'Add category opened no New category form').toBeVisible()
      await form.getByLabel('Name', { exact: true }).fill(name)
      await form.getByRole('button', { name: icon, exact: true }).click()
      await form.getByRole('button', { name: 'Colour', exact: true }).nth(tone).click()
      await submit.click()
      await expect(toastOf(page, 'Category added'), `no "Category added" toast for ${name}`).toBeVisible()
      await expect(catRow(page, name), `${name} drew no row`).toBeVisible()
    }

    // 1
    await openCategories(page)
    const count = await tabCount(page)

    // 2
    await note.click()
    await expect(form.getByText('New category', { exact: true }), 'Add category opened no New category form').toBeVisible()
    await submit.click()
    await expect(form.getByText('Add a name.', { exact: true }), 'a blank name drew no "Add a name." under the form').toBeVisible()

    // 3
    const tone = (count % 6 + 2) % 6
    await form.getByLabel('Name', { exact: true }).fill(nameA)
    await form.getByRole('button', { name: 'handshake', exact: true }).click()
    await form.getByRole('button', { name: 'Colour', exact: true }).nth(tone).click()
    await submit.click()
    await expect(toastOf(page, 'Category added'), 'no "Category added" toast for A').toBeVisible()
    await expect(catRow(page, nameA), 'A drew no row').toBeVisible()
    expect(await tabCount(page), 'the tab count did not grow by one').toBe(count + 1)
    expect(await uiOrder(page, nameA), 'A is not listed').toEqual([nameA])
    const a = await find(nameA)
    expect(a.icon, 'the server did not keep the picked icon').toBe('handshake')
    expect(a.tone, 'the server did not keep the picked tone').toBe(tone)
    expect((await serverCats(token)).at(-1)?.id, 'A is not last on the server').toBe(a.id)

    // 4
    await add(nameB, 'archive', 0)
    const b = await find(nameB)
    expect((await serverCats(token)).at(-1)?.id, 'B is not last on the server').toBe(b.id)
    expect(await uiOrder(page, nameA, nameB), 'B is not below A').toEqual([nameA, nameB])

    // 5
    await note.click()
    await form.getByLabel('Name', { exact: true }).fill(nameA.toUpperCase())
    await submit.click()
    await expect(form.getByText('A category with this name already exists.', { exact: true }), 'a duplicate name drew no message under the form').toBeVisible()
    await form.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(form, 'Cancel left the form open').toHaveCount(0)

    // 6
    await rowButton(catRow(page, nameB), 'Edit').click()
    await expect(form.getByRole('button', { name: b.icon, exact: true }), 'the Edit form did not prefill B icon').toHaveAttribute('aria-pressed', 'true')
    await expect(form.getByRole('button', { name: 'Colour', exact: true }).nth(b.tone), 'the Edit form did not prefill B tone').toHaveAttribute('aria-pressed', 'true')
    const newTone = (b.tone + 1) % 6
    const headline = `Edited ${hex}`
    await form.getByLabel('Band headline', { exact: true }).fill(headline)
    await form.getByRole('button', { name: 'plug', exact: true }).click()
    await form.getByRole('button', { name: 'Colour', exact: true }).nth(newTone).click()
    await form.getByRole('button', { name: 'Save changes', exact: true }).click()
    await expect(toastOf(page, 'Category updated'), 'no "Category updated" toast').toBeVisible()
    await expect(catRow(page, nameB), 'B row does not show the new headline').toContainText(headline)
    const edited = await find(nameB)
    expect([edited.headline, edited.icon, edited.tone], 'the server did not keep the edit').toEqual([headline, 'plug', newTone])

    // 7
    const moved = page.waitForResponse(isMove)
    await rowButton(catRow(page, nameB), 'Move up').click()
    expect((await moved).status(), 'Move up failed').toBe(200)
    await expect.poll(() => uiOrder(page, nameA, nameB), 'B did not move above A').toEqual([nameB, nameA])
    expect(await serverOrder(token, a.id, b.id), 'the server did not move B above A').toEqual([b.id, a.id])
    await expect(rowButton(catRow(page, nameA), 'Move down'), 'A last row Move down is not dimmed').toHaveCSS('opacity', '0.35')
    expect(moves.length, 'one move so far').toBe(1)
    await rowButton(catRow(page, nameA), 'Move down').click()
    const movedBack = page.waitForResponse(isMove)
    await rowButton(catRow(page, nameB), 'Move down').click()
    expect((await movedBack).status(), 'Move down failed').toBe(200)
    expect(moves.length, 'the dimmed Move down sent a request').toBe(2)
    await expect.poll(() => uiOrder(page, nameA, nameB), 'B did not move back below A').toEqual([nameA, nameB])
    expect(await serverOrder(token, a.id, b.id), 'the server did not move B back below A').toEqual([a.id, b.id])

    // 8: the QA path. A post written from the Categories tab shows in the row count with no categories re-read.
    const postTitle = `e2e cat post ${hex}`
    const catReads: string[] = []
    page.on('request', (req) => {
      if (req.method() === 'GET' && new URL(req.url()).pathname.endsWith('/staff/blog/categories')) catReads.push(req.url())
    })
    await expect(catRow(page, nameA).locator('[data-cat-count]'), 'A starts with no posts').toHaveText('0 posts')
    const readsBefore = catReads.length
    await page.getByRole('button', { name: 'New post' }).click()
    await expect(titleInput(page), 'New post opened no editor').toBeVisible()
    await titleInput(page).fill(postTitle)
    // The wrapping label's text includes the option names, so getByLabel exact never matches the editor select.
    await page.locator('[data-blog-fields] label').filter({ has: page.getByText('Category', { exact: true }) }).locator('select').selectOption({ label: nameA })
    const createdPost = page.waitForResponse((r) => r.request().method() === 'POST' && BLOG_POSTS.test(r.url()))
    await page.getByRole('button', { name: 'Save draft', exact: true }).click()
    const made = await createdPost
    expect(made.status(), 'create the draft post in A').toBe(201)
    const postId = ((await made.json()) as { id: string }).id
    own.posts.push(postId)
    // The editor is dirty until the save settles; All posts then asks to leave.
    await expect(toastOf(page, 'Draft saved')).toBeVisible()
    await bar(page).getByRole('button', { name: 'All posts' }).click()
    await expect(page.locator('[data-blog-cats]'), 'All posts did not return to the Categories tab').toBeVisible()
    await expect(catRow(page, nameA).locator('[data-cat-count]'), 'the row did not count the new post').toHaveText('1 post')
    expect(catReads.length, 'the count came from a categories re-read').toBe(readsBefore)

    // 8b: a post another staff member wrote. The refused delete re-reads both lists, so the next confirm offers the move.
    const isRead = (path: string) => (r: { url(): string; request(): { method(): string } }) => r.request().method() === 'GET' && new URL(r.url()).pathname.endsWith(path)
    const other = await rawFetch(`${BLOG}/posts`, { method: 'POST', headers: bearer(token), body: { title: `e2e cat other ${hex}`, category_id: b.id, status: 'draft', body: [] } })
    expect(other.status, 'create the other staff post in B').toBe(201)
    const otherId = (other.body as { id: string }).id
    own.posts.push(otherId)
    await rowButton(catRow(page, nameB), 'Delete').click()
    await expect(confirm.getByText(`Delete ${nameB}? It has no posts.`, { exact: true }), 'B confirm should still say no posts').toBeVisible()
    const rereadCats = page.waitForResponse(isRead('/staff/blog/categories'))
    const rereadPosts = page.waitForResponse(isRead('/staff/blog/posts'))
    const refused = page.waitForResponse((r) => r.request().method() === 'DELETE' && /\/categories\/[^/]+$/.test(r.url()))
    await confirm.getByRole('button', { name: 'Delete category', exact: true }).click()
    expect((await refused).status(), 'the delete of a category with posts was not refused').toBe(422)
    await Promise.all([rereadCats, rereadPosts])
    await rowButton(catRow(page, nameB), 'Delete').click()
    await expect(confirm.getByText(`Delete ${nameB} and move its 1 post to:`, { exact: true }), 'the re-read did not reach the confirm').toBeVisible()
    await confirm.getByRole('button', { name: 'Cancel', exact: true }).click()
    expect((await rawFetch(`${BLOG}/posts/${otherId}`, { method: 'DELETE', headers: bearer(token) })).status, 'delete the other staff post').toBeLessThan(300)
    const backPosts = page.waitForResponse(isRead('/staff/blog/posts'))
    await pad(page).getByRole('button', { name: /Posts/ }).click()
    await expect(page.locator('[data-blog-table]'), 'the Posts tab drew no table').toBeVisible()
    await pad(page).getByRole('button', { name: /Categories/ }).click()
    await backPosts
    await expect(catRow(page, nameB).locator('[data-cat-count]'), 'B still counts the deleted post').toHaveText('0 posts')

    // 9
    await rowButton(catRow(page, nameA), 'Delete').click()
    await expect(confirm.getByText(`Delete ${nameA} and move its 1 post to:`, { exact: true }), 'no move-its-post confirm text').toBeVisible()
    await confirm.locator('select').selectOption({ label: nameB })
    await confirm.getByRole('button', { name: 'Delete category', exact: true }).click()
    await expect(toastOf(page, `${nameA} deleted. 1 post moved to ${nameB}`), 'no deleted-and-moved toast').toBeVisible()
    await expect(catRow(page, nameA), 'A still has a row').toHaveCount(0)
    expect((await serverCats(token)).some((c) => c.id === a.id), 'the server still holds A').toBe(false)
    const posts = (await rawFetch(`${BLOG}/posts`, { headers: bearer(token) })).body as { posts: { id: string; category_id: string | null }[] }
    expect(posts.posts.find((p) => p.id === postId)?.category_id, 'the post did not move to B').toBe(b.id)

    // 10
    await pad(page).getByRole('button', { name: /Posts/ }).click()
    await page.getByLabel('Search posts').fill(postTitle)
    await page.locator('.ops-row').filter({ hasText: postTitle }).click()
    await bar(page).getByRole('button', { name: 'Delete', exact: true }).click()
    await bar(page).getByRole('button', { name: 'Delete post', exact: true }).click()
    await expect(toastOf(page, 'Post deleted')).toBeVisible()
    await expect(page.locator('[data-blog-table]'), 'Delete post did not return to the list').toBeVisible()
    await pad(page).getByRole('button', { name: /Categories/ }).click()
    await expect(page.locator('[data-blog-cats]'), 'the Categories tab drew no list').toBeVisible()
    await rowButton(catRow(page, nameB), 'Delete').click()
    await expect(confirm.getByText(`Delete ${nameB}? It has no posts.`, { exact: true }), 'no empty-category confirm text').toBeVisible()
    await confirm.getByRole('button', { name: 'Delete category', exact: true }).click()
    await expect(toastOf(page, `${nameB} deleted`), 'no deleted toast').toBeVisible()
    await expect(catRow(page, nameB), 'B still has a row').toHaveCount(0)
    expect((await serverCats(token)).some((c) => c.id === b.id), 'the server still holds B').toBe(false)
    expect(errors, errorsOf(errors)).toEqual([])
  })

  test('SUP-BLOG-06 blog categories failed move: a 500 leaves the order and shows a red toast', async ({ page }) => {
    const errors = blogErrors(page, expectedStatusDropper(page, 500, BLOG_CATEGORIES))
    const token = await staffToken()
    const hex = randomBytes(4).toString('hex')
    const nameA = `e2e cat A ${hex}`
    const nameB = `e2e cat B ${hex}`
    const a = await rawCat(token, nameA)
    const b = await rawCat(token, nameB)
    await openCategories(page)
    await expect(catRow(page, nameB), 'B drew no row').toBeVisible()
    await page.route(new RegExp(`/categories/${b.id}/move$`), (route) => (route.request().method() === 'POST' ? fulfillStatus(route, 500, { error: 'internal error' }) : route.fallback()))
    await rowButton(catRow(page, nameB), 'Move up').click()
    await expectRedToast(page, 'internal error')
    expect(await uiOrder(page, nameA, nameB), 'the failed move reordered the list').toEqual([nameA, nameB])
    expect(await serverOrder(token, a.id, b.id), 'the failed move reordered the server').toEqual([a.id, b.id])
    expect(errors, errorsOf(errors)).toEqual([])
  })
})
