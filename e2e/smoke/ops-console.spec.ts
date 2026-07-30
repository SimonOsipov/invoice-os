import { expect, test, type Page } from '@playwright/test'

import { collectErrors, signInAs } from '../personaSession'

// The Ops Console, driven as the integration developer it is for (PERSONA-01-05, Backlog
// task-274). Persona `developer` -> destination `ops`.
//
// MOCK-ONLY, AND THAT LIMITS WHAT A GREEN RUN MEANS. This console has no backend: a grep
// for fetch/XMLHttpRequest/axios/WebSocket across frontend/ops-console/src returns nothing,
// and its own session module says so in prose (frontend/ops-console/src/session.ts:8-12 —
// "Deliberately NOT access control... a fabricated localStorage entry is enough to get
// in"). Every assertion below pins this console's CLIENT-SIDE BEHAVIOUR over the fixtures
// in src/data.tsx. It is not a contract test and says nothing about any server. Counts are
// read from the rendered UI rather than hardcoded, so a seed edit does not break these —
// but the entities are fiction, and when real endpoints land (M7: operator identity + a
// cross-tenant read path) these specs must be REWRITTEN against real data, not
// re-baselined.
//
// WHY A BROWSER IS THE ONLY HARNESS. Every frontend vitest project runs in `node` with no
// DOM (frontend/ops-console/vitest.config.ts:5), so there is no component-test layer in
// which a click, a filter or a drawer could be exercised at all. Same rationale and same
// suite as smoke/landing-nav.spec.ts and smoke.spec.ts:48-60. docs/e2e-convention.md's
// "Target surface" section is amended by [PERSONA-01-07] in this same PR.
//
// PARALLEL-SAFE. Zero network calls and zero database contact, so smoke's
// `fullyParallel: true` needs no carve-out here: nothing this file does can be observed by
// another worker. Every mutation below lives in React state inside one page.
//
// ALREADY COVERED ELSEWHERE, DELIBERATELY NOT REPEATED HERE: smoke/apps.ts:42-46 (the
// ASComply mark and the default `Overview` h1), smoke.spec.ts:48-60 (the org switcher),
// smoke.spec.ts:66-75 (a bare-URL visit redirects to the landing page),
// smoke/persona-boundaries.spec.ts (this console refuses the support/firm/inhouse personas).
//
// DELIBERATELY NOT ASSERTED, because a fixture assertion earns its place only if a
// plausible CODE change can break it: the sidebar operator name (hardcoded in Sidebar.tsx,
// not read from the session, so it proves nothing about who signed in), the request-quota
// meter (literals), the Evidence table (static, derived once at import), the Status screen's
// uptime copy (literals), and the billing figures and sparkline paths (already unit-tested
// in charts.test.ts / helpers.test.ts — re-asserting them in a browser would duplicate the
// base of the pyramid, which docs/e2e-convention.md forbids).

// ASSERT THE <h1>, NOT THE CRUMB. TopBar renders CRUMB_BY_SCREEN inside <main> (TopBar.tsx:64)
// and the crumb differs from BOTH the nav label and the h1 on some screens by design
// (data.tsx:94-95), so a getByText sweep would be ambiguous where they agree and wrong where
// they do not. `getByRole('heading', { level: 1 })` is the screen's own claim about itself,
// and each screen renders exactly one. Do not "fix" this file toward the crumb.
//
// The nav LABEL likewise differs from the h1 on two screens — Evidence -> "Compliance
// evidence" and Status -> "API status" (data.tsx:81-92 vs the screen components). Click the
// label, assert the heading. The sweep ends back on Overview so the return leg is covered
// without restating what signInAs already waited for.
const SCREENS: { nav: string; h1: string }[] = [
  { nav: 'Submissions', h1: 'Submissions' },
  { nav: 'Evidence', h1: 'Compliance evidence' },
  { nav: 'API & webhooks', h1: 'API & webhooks' },
  { nav: 'Usage & billing', h1: 'Usage & billing' },
  { nav: 'Status', h1: 'API status' },
  { nav: 'Overview', h1: 'Overview' },
]

// The filter chips, in render order: 'all' plus JOB_FILTER_KEYS (data.tsx:124). These are
// LABELS, not state keys — `accepted` renders as CLEARED here (helpers.ts:21), where the
// support console renders the same state ACCEPTED. The two consoles are not symmetric, so
// nothing in this file may be shared with support-console.spec.ts.
const CHIP_LABELS = ['ALL', 'QUEUED', 'SUBMITTING', 'PENDING', 'CLEARED', 'REJECTED', 'FAILED', 'DEAD-LETTER']

const sidebar = (page: Page) => page.locator('aside.ops-sidebar')
const navButtons = (page: Page) => sidebar(page).locator('nav button.ops-nav')
const heading = (page: Page) => page.getByRole('heading', { level: 1 })
// Scoped to <main> so the drawers — fixed-position siblings of <main>, not children — can
// never satisfy a row or chip locator.
const chips = (page: Page) => page.getByRole('main').locator('button.ops-chip')
const rows = (page: Page) => page.getByRole('main').locator('.ops-row')

// The sidebar's dead-letter badge. It is the only `.mono` inside that nav button (the label
// span carries `ops-nav-label`), and it is not rendered at all when the count is zero
// (Sidebar.tsx:141) — which is what makes `toHaveCount(0)` a real post-condition rather
// than a text comparison. `{ name: 'Submissions' }` matches by substring, so it still finds
// the button when the badge has widened its accessible name to "Submissions 1".
const deadLetterBadge = (page: Page) => sidebar(page).getByRole('button', { name: 'Submissions' }).locator('.mono')

// The Submissions "Dead-letter" sub-stat tile — a second consumer of the same count,
// computed independently of the sidebar's (Submissions.tsx:17,30).
const deadLetterTile = (page: Page) =>
  page.getByRole('main').locator('.ops-sub-stats > div').filter({ hasText: 'Dead-letter' }).locator('.mono')

// A chip renders `LABEL` immediately followed by its count with no separator ("CLEARED3").
// No label contains a digit, so trimming a trailing run of digits splits it unambiguously —
// the same idiom personaSession.ts:68-71 uses for the app sidebar's badges. Reading the
// count from the chip rather than hardcoding it is the point: a data.tsx seed edit must not
// be able to turn these tests red, only a defect in the filter predicate can.
function parseChip(text: string): { label: string; count: number } {
  const m = text.trim().match(/^(.*?)(\d+)$/)
  expect(m, `filter chip does not render "LABEL<count>": "${text}"`).not.toBeNull()
  return { label: m![1].trim(), count: Number(m![2]) }
}

async function openSubmissions(page: Page): Promise<void> {
  await sidebar(page).getByRole('button', { name: 'Submissions' }).click()
  await expect(heading(page)).toHaveText('Submissions')
}

test('ops-console: every screen is reachable from the sidebar and renders its own h1', async ({ page }) => {
  const errors = collectErrors(page)
  await signInAs(page, 'developer')

  // A seventh screen must turn this red rather than being silently skipped by the loop.
  await expect(navButtons(page)).toHaveCount(SCREENS.length)

  for (const screen of SCREENS) {
    await sidebar(page).getByRole('button', { name: screen.nav }).click()
    await expect(heading(page), `clicking "${screen.nav}" should open the "${screen.h1}" screen`).toHaveText(screen.h1)
  }

  expect(errors, `console errors sweeping the ops console:\n${errors.join('\n')}`).toEqual([])
})

// The dead-letter loop is what this console is FOR, and it is a state mutation, not a
// render: `dlCount` is derived in App.tsx:92 and threaded to two independent consumers,
// and reDriveAll (App.tsx:110-114) rewrites the jobs array so all of them must fall
// together.
//
// THIS MUST BE THE FIRST INTERACTION ON THE SCREEN, AND IT OWNS ITS OWN PAGE. The callout
// (and with it the "Re-drive all" button) is gated on `filter === 'all' && !query`
// (charts.ts:24-26), so it disappears the moment any chip is clicked or anything is typed
// into the search box. It also must not be merged into the chip test for that reason.
// `dlCount` is React state with no persistence, so a page.reload() anywhere in here would
// silently undo the mutation — there is deliberately none.
test('ops-console: Re-drive all clears the dead-letter queue everywhere it is reported', async ({ page }) => {
  const errors = collectErrors(page)
  await signInAs(page, 'developer')
  await openSubmissions(page)

  // The count is READ from the sub-stat tile rather than the badge, because the tile always
  // renders (Submissions.tsx:26-31) and the badge does not: at zero the badge is absent, so
  // reading it there would hang to the test timeout instead of failing on the guard below.
  const tile = deadLetterTile(page)
  const n = Number(((await tile.textContent()) ?? '').trim())
  // Vacuity guard: with nothing in the dead-letter queue there is no callout, no button and
  // nothing to clear, so every assertion below would pass without exercising anything.
  expect(n, 'the dead-letter queue must be non-empty for this test to mean anything').toBeGreaterThan(0)

  // The sidebar badge agrees — a second consumer, counting the jobs array independently
  // (App.tsx:92 vs Submissions.tsx:17).
  const badge = deadLetterBadge(page)
  await expect(badge).toHaveText(String(n))

  // ...and so does the callout. The singular is ungrammatical at n=1 ("1 submissions in the
  // dead-letter queue") — that is a faithful port, Submissions.tsx:72 does not pluralise,
  // and the support console's equivalent does. Asserting the rendered string rather than a
  // tidied one is what keeps this a test of the product.
  //
  // An exact STRING, not an anchored regex: Playwright matches strings against normalized
  // text but regexes against raw textContent (elementText().full), so `^…$` would break
  // silently the day someone reflows that JSX across lines.
  await expect(page.getByText(`${n} submissions in the dead-letter queue`, { exact: true })).toBeVisible()

  await page.getByRole('button', { name: 'Re-drive all' }).click()

  // The toast FIRST: it self-clears after 3400ms (App.tsx:89), so any slower assertion
  // placed ahead of it would race. This console's Toast carries no role (Toast.tsx) —
  // unlike the support console's — so it is matched by its text, which is derived from the
  // number of rows actually transformed (App.tsx:111-113), not from the badge.
  await expect(page.getByText(`Re-drove ${n} dead-letter submissions`, { exact: true })).toBeVisible()

  // Every reporter of the count now agrees it is empty. The badge and the button are not
  // rendered at all at zero, so these are absence assertions, not "shows 0" assertions.
  await expect(badge).toHaveCount(0)
  await expect(page.getByText(/submissions in the dead-letter queue/)).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Re-drive all' })).toHaveCount(0)
  await expect(tile).toHaveText('0')

  expect(errors, `console errors re-driving the ops dead-letter queue:\n${errors.join('\n')}`).toEqual([])
})

test('ops-console: each filter chip re-filters the table, and its own count matches the rows it shows', async ({ page }) => {
  const errors = collectErrors(page)
  await signInAs(page, 'developer')
  await openSubmissions(page)

  await expect(chips(page)).toHaveCount(CHIP_LABELS.length)

  let allCount = 0
  for (let i = 0; i < CHIP_LABELS.length; i++) {
    const chip = chips(page).nth(i)
    // Indexed, never `getByRole('button', { name: 'ALL' })`: role-name matching is a
    // case-insensitive SUBSTRING match, and "Re-drive all" contains "all", so that locator
    // resolves to two elements while the callout is up.
    const { label, count } = parseChip((await chip.textContent()) ?? '')
    expect(label, `filter chip ${i}`).toBe(CHIP_LABELS[i])

    await chip.click()
    // The chip's own rendered count and the rows it produces are computed by two different
    // expressions over `jobs` (Submissions.tsx:106 vs :23) — agreeing is the claim.
    await expect(rows(page), `the ${label} chip reports ${count}`).toHaveCount(count)

    if (label === 'ALL') {
      allCount = count
      expect(allCount, 'the unfiltered table must have rows').toBeGreaterThan(0)
    }

    if (label === 'CLEARED') {
      // Right NUMBER is not right ROWS: a predicate that returned the wrong states in the
      // right quantity would pass everything above. Checked on CLEARED because the label
      // and the state key differ here (`accepted` -> CLEARED), so this also pins the
      // chip -> state mapping, not just the count.
      expect(count, 'CLEARED must select some rows').toBeGreaterThan(0)
      expect(count, 'CLEARED must select FEWER rows than ALL, or nothing was filtered').toBeLessThan(allCount)
      const texts = await rows(page).allTextContents()
      expect(texts.every((t) => t.includes('CLEARED')), `CLEARED filter returned: ${texts.join(' | ')}`).toBe(true)
    }
  }

  expect(errors, `console errors filtering ops submissions:\n${errors.join('\n')}`).toEqual([])
})

// Opening a row must open THAT row's drawer. The two derived facts below are what make this
// more than "a panel appeared": the idempotency key is built from the clicked row's id
// (helpers.ts:149) and the response payload is selected by the clicked row's state
// (helpers.ts:70-71). A drawer wired to the wrong job fails on both.
test('ops-console: opening a job row opens that job\'s drawer, with the payload derived from that row', async ({ page }) => {
  const errors = collectErrors(page)
  await signInAs(page, 'developer')
  await openSubmissions(page)

  // The dead-letter row is chosen by its rendered state, not by a hardcoded seed id, so the
  // expected payload below stays correct if data.tsx is reseeded. Matched on the state
  // PILL — an element whose whole text is the label — rather than `hasText`, which scans the
  // row's error column too and would pick the wrong row if a seeded error message ever
  // mentioned the dead-letter queue.
  const row = rows(page).filter({ has: page.getByText('DEAD-LETTER', { exact: true }) }).first()
  await expect(row).toBeVisible()
  const jobId = ((await row.locator('.mono').first().textContent()) ?? '').trim()
  expect(jobId, 'the first cell of a job row is its id').toMatch(/^sub_\w+$/)

  await row.click()

  const drawer = page.locator('.ops-drawer')
  await expect(drawer).toBeVisible()
  await expect(drawer).toContainText(jobId)
  await expect(drawer).toContainText('DEAD-LETTER')
  // Derived from the clicked row's id: `sub_` -> `idem_`, plus the checksum-ish suffix.
  await expect(drawer).toContainText(`${jobId.replace('sub_', 'idem_')}c3`)
  // Derived from the clicked row's STATE: only `dead-letter` produces this response body.
  await expect(drawer).toContainText('"code": "GATEWAY_TIMEOUT"')

  // Re-drive / Re-poll / Cancel are deliberately not clicked: they mutate the jobs array
  // and this test's claim is about resolution, not mutation.

  expect(errors, `console errors opening the ops job drawer:\n${errors.join('\n')}`).toEqual([])
})
