import { expect, test, type Page } from '@playwright/test'

import { collectErrors, signInAs } from '../personaSession'

// The Support Console, driven as the cross-tenant operator it is for (PERSONA-01-05,
// Backlog task-274). Persona `support` -> destination `support`.
//
// MOCK-ONLY, AND THAT LIMITS WHAT A GREEN RUN MEANS. This console has no backend: a grep
// for fetch/XMLHttpRequest/axios/WebSocket across frontend/support-console/src returns
// nothing, and its own session module says so in prose
// (frontend/support-console/src/session.ts:3-9 — "Deliberately NOT access control... a
// fabricated localStorage entry is enough to get in"). Every assertion below pins this
// console's CLIENT-SIDE BEHAVIOUR over the fixtures in src/data.tsx. It is not a contract
// test and says nothing about any server. Counts are read from the rendered UI rather than
// hardcoded, so a seed edit does not break these — but the entities are fiction, and when
// real endpoints land (M7: operator identity + a cross-tenant read path) these specs must
// be REWRITTEN against real data, not re-baselined.
//
// WHY A BROWSER IS THE ONLY HARNESS. Every frontend vitest project runs in `node` with no
// DOM (frontend/support-console/vitest.config.ts:5), so there is no component-test layer in
// which a click, a filter or a detail pane could be exercised at all. Same rationale and
// same suite as smoke/landing-nav.spec.ts and smoke.spec.ts:48-60. docs/e2e-convention.md's
// "Target surface" section is amended by [PERSONA-01-07] in this same PR — note it does not
// currently mention this console at all.
//
// PARALLEL-SAFE. Zero network calls and zero database contact, so smoke's
// `fullyParallel: true` needs no carve-out here: nothing this file does can be observed by
// another worker. Every mutation below lives in React state inside one page.
//
// ALREADY COVERED ELSEWHERE, DELIBERATELY NOT REPEATED HERE: smoke/apps.ts:53-60 (the
// ASComply mark, the default `Submissions ops` h1, and `CROSS-TENANT VIEW` on the landing
// screen), smoke.spec.ts:66-75 (a bare-URL visit redirects to the landing page),
// smoke/persona-boundaries.spec.ts (this console refuses the developer/firm/inhouse
// personas).
//
// DELIBERATELY NOT ASSERTED, because a fixture assertion earns its place only if a
// plausible CODE change can break it: the sidebar operator name (hardcoded in Sidebar.tsx,
// not read from the session, so it proves nothing about who signed in), the APP
// backpressure meter (literals), and the health cards' numeric values — their three `.mono`
// spans (status / value / unit) have no stable discriminator, an nth() index is exactly the
// brittle locator this repo avoids, and the status WORD asserted below is derived from the
// same live count, so nothing is lost.

// ASSERT THE <h1>, NOT THE CRUMB. TopBar renders CRUMB_BY_SCREEN inside <main>
// (TopBar.tsx:62), and here the crumb differs from the h1 on two screens by design —
// CRUMB_BY_SCREEN.audit is "Audit & evidence" against the h1 "Audit & evidence explorer",
// and .tenants is "Tenants" against "Tenants & entities" (data.tsx:99-105). Because the
// crumb is inside <main>, a getByText sweep would be ambiguous where the two agree and
// wrong where they do not. `getByRole('heading', { level: 1 })` is the screen's own claim
// about itself, and each screen renders exactly one. Do not "fix" this file toward the crumb.
//
// The nav LABEL differs from the h1 on four of the five screens (data.tsx:73-96 vs the
// screen components). Click the label, assert the heading. The sweep ends back on
// Submissions so the return leg is covered without restating what signInAs already waited for.
const SCREENS: { nav: string; h1: string }[] = [
  { nav: 'Rules', h1: 'Rules admin' },
  { nav: 'Audit', h1: 'Audit & evidence explorer' },
  { nav: 'Tenants', h1: 'Tenants & entities' },
  { nav: 'System health', h1: 'System health' },
  { nav: 'Submissions', h1: 'Submissions ops' },
]

// The filter chips, in render order (JOB_FILTERS, data.tsx:125). These are LABELS, not
// state keys — `accepted` renders as ACCEPTED here (helpers.ts:22), where the ops console
// renders the same state CLEARED. The two consoles are not symmetric, so nothing in this
// file may be shared with ops-console.spec.ts.
const CHIP_LABELS = ['ALL', 'QUEUED', 'SUBMITTING', 'PENDING', 'ACCEPTED', 'REJECTED', 'FAILED', 'DEAD-LETTER']

const sidebar = (page: Page) => page.locator('aside.ops-sidebar')
// Scoped to the sidebar's <nav>, because the TENANT ROWS reuse the `.ops-nav` class
// (Tenants.tsx:69) — a bare `.ops-nav` locator is ambiguous the moment that screen is open.
const navButtons = (page: Page) => sidebar(page).locator('nav button.ops-nav')
const heading = (page: Page) => page.getByRole('heading', { level: 1 })
// Scoped to <main> so the drawers — fixed-position siblings of <main>, not children — can
// never satisfy a row or chip locator, and so the tenant rows never satisfy a nav locator.
const chips = (page: Page) => page.getByRole('main').locator('button.ops-chip')
const rows = (page: Page) => page.getByRole('main').locator('.ops-row')

// The sidebar's dead-letter badge. Scoped to the Submissions button on purpose: the Rules
// button carries a `.mono` badge of its own (the learned-rules inbox, Sidebar.tsx:87), so an
// unscoped badge locator would resolve to two elements. It is not rendered at all when the
// count is zero, which is what makes `toHaveCount(0)` a real post-condition rather than a
// text comparison. `{ name: 'Submissions' }` matches by substring, so it still finds the
// button when the badge has widened its accessible name to "Submissions 2".
const deadLetterBadge = (page: Page) => sidebar(page).getByRole('button', { name: 'Submissions' }).locator('.mono')

// Two more consumers of the same count: the Submissions sub-stat tile
// (Submissions.tsx:33) and the System health card (data.tsx healthCards()).
const deadLetterTile = (page: Page) =>
  page.getByRole('main').locator('.ops-sub-stats > div').filter({ hasText: 'Dead-letter' }).locator('.mono')
const deadLetterCard = (page: Page) => page.getByRole('main').locator('.ops-health-grid > div').filter({ hasText: 'Dead-letter' })

// A chip renders `LABEL` immediately followed by its count with no separator ("ACCEPTED2").
// No label contains a digit, so trimming a trailing run of digits splits it unambiguously —
// the same idiom personaSession.ts:68-71 uses for the app sidebar's badges. Reading the
// count from the chip rather than hardcoding it is the point: a data.tsx seed edit must not
// be able to turn these tests red, only a defect in the filter predicate can.
//
// Duplicated from ops-console.spec.ts rather than hoisted: the two consoles' fixtures and
// labels differ (CLEARED vs ACCEPTED, one dead-letter job vs two, one callout pluralises
// and the other does not), and a shared helper would invite a shared assumption about
// shapes that are only incidentally alike.
function parseChip(text: string): { label: string; count: number } {
  const m = text.trim().match(/^(.*?)(\d+)$/)
  expect(m, `filter chip does not render "LABEL<count>": "${text}"`).not.toBeNull()
  return { label: m![1].trim(), count: Number(m![2]) }
}

async function goTo(page: Page, nav: string, h1: string): Promise<void> {
  await sidebar(page).getByRole('button', { name: nav }).click()
  await expect(heading(page)).toHaveText(h1)
}

test('support-console: every screen is reachable from the sidebar and renders its own h1', async ({ page }) => {
  const errors = collectErrors(page)
  await signInAs(page, 'support')

  // A sixth screen must turn this red rather than being silently skipped by the loop.
  await expect(navButtons(page)).toHaveCount(SCREENS.length)

  for (const screen of SCREENS) {
    await sidebar(page).getByRole('button', { name: screen.nav }).click()
    await expect(heading(page), `clicking "${screen.nav}" should open the "${screen.h1}" screen`).toHaveText(screen.h1)

    // The cross-tenant strip is the one piece of chrome that distinguishes this console
    // from the tenant-scoped ones. smoke/apps.ts:59 already asserts it on the LANDING
    // screen, so the only new claim is that it survives a screen change — asserted once,
    // after the first hop, not five times. It lives in Sidebar, outside the screen switch,
    // so this is a deliberately weak assertion: its value is catching a layout refactor
    // that moves it inside the switched region.
    if (screen.nav === SCREENS[0].nav) {
      await expect(sidebar(page).getByText('CROSS-TENANT VIEW')).toBeVisible()
    }
  }

  expect(errors, `console errors sweeping the support console:\n${errors.join('\n')}`).toEqual([])
})

// The dead-letter loop is what this console is FOR, and it is a state mutation, not a
// render: `dlCount` is derived ONCE in App.tsx:88 and threaded to three unrelated consumers
// on two different screens, and reDriveAll (App.tsx:109-113) rewrites the jobs array so all
// of them must fall together. The System health hop is the strongest assertion in either
// console — the card is rendered by a different component, on a different screen, from the
// same live number.
//
// `dlCount` is React state with no persistence, so a page.reload() anywhere in here would
// silently undo the mutation — there is deliberately none.
test('support-console: Re-drive all clears the dead-letter queue everywhere it is reported', async ({ page }) => {
  const errors = collectErrors(page)
  await signInAs(page, 'support') // lands on Submissions ops

  // The count is READ from the sub-stat tile rather than the badge, because the tile always
  // renders (Submissions.tsx:29-34) and the badge does not: at zero the badge is absent, so
  // reading it there would hang to the test timeout instead of failing on the guard below.
  const tile = deadLetterTile(page)
  const n = Number(((await tile.textContent()) ?? '').trim())
  // Vacuity guard: with nothing in the dead-letter queue there is no callout, no button and
  // nothing to clear, so every assertion below would pass without exercising anything.
  expect(n, 'the dead-letter queue must be non-empty for this test to mean anything').toBeGreaterThan(0)

  // The sidebar badge agrees — a second consumer, counting the jobs array independently
  // (App.tsx:88 vs Submissions.tsx:23-24).
  const badge = deadLetterBadge(page)
  await expect(badge).toHaveText(String(n))

  // ...and so does the callout. This one DOES pluralise (Submissions.tsx:89) where the ops
  // console's does not, so the expected plural is computed here rather than papered over
  // with `jobs?` — a regex that accepts both forms could not catch a broken plural.
  //
  // An exact STRING, not an anchored regex: Playwright matches strings against normalized
  // text but regexes against raw textContent (elementText().full), so `^…$` would break
  // silently the day someone reflows that JSX across lines.
  await expect(page.getByText(`${n} ${n === 1 ? 'job' : 'jobs'} in the dead-letter queue`, { exact: true })).toBeVisible()

  // Third consumer, on another screen.
  await goTo(page, 'System health', 'System health')
  await expect(deadLetterCard(page)).toHaveCount(1)
  await expect(deadLetterCard(page)).toContainText('ATTENTION')

  await goTo(page, 'Submissions', 'Submissions ops')
  await page.getByRole('button', { name: 'Re-drive all' }).click()

  // The toast FIRST: it self-clears after 3400ms (App.tsx:85), so any slower assertion
  // placed ahead of it would race. This console's Toast carries role="status" (Toast.tsx:28)
  // where the ops console's does not, and asserting the message and the accreditation tag on
  // the SAME element proves the tag belongs to this toast rather than merely being on the
  // page. The message's number comes from the rows actually transformed (App.tsx:110-112),
  // not from the badge.
  const toast = page.getByRole('status')
  await expect(toast).toContainText(`Re-drive queued \u00b7 ${n} dead-letter ${n === 1 ? 'job' : 'jobs'}`)
  await expect(toast).toContainText('AUDIT ON ACCREDITATION')

  // Every reporter of the count now agrees it is empty. The badge is not rendered at all at
  // zero, so that one is an absence assertion, not a "shows 0" assertion.
  await expect(badge).toHaveCount(0)
  await expect(page.getByText(/in the dead-letter queue/)).toHaveCount(0)
  await expect(tile).toHaveText('0')

  // ...including the card on the other screen, whose STATUS WORD is derived from the same
  // live count (data.tsx healthCards()) and so must flip with it.
  await goTo(page, 'System health', 'System health')
  await expect(deadLetterCard(page)).toContainText('CLEAR')
  await expect(deadLetterCard(page)).not.toContainText('ATTENTION')

  expect(errors, `console errors re-driving the support dead-letter queue:\n${errors.join('\n')}`).toEqual([])
})

test('support-console: each filter chip re-filters the table, tracks aria-pressed, and its count matches the rows it shows', async ({ page }) => {
  const errors = collectErrors(page)
  await signInAs(page, 'support')

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
    // expressions over `jobs` (Submissions.tsx:110 vs :25) — agreeing is the claim.
    await expect(rows(page), `the ${label} chip reports ${count}`).toHaveCount(count)

    // Unlike the ops console's, these chips carry aria-pressed — so the selection is also
    // exposed to assistive tech, and exactly one chip may claim it.
    await expect(chip).toHaveAttribute('aria-pressed', 'true')
    await expect(page.getByRole('main').locator('button.ops-chip[aria-pressed="true"]')).toHaveCount(1)

    if (label === 'ALL') {
      allCount = count
      expect(allCount, 'the unfiltered table must have rows').toBeGreaterThan(0)
    }

    if (label === 'ACCEPTED') {
      // Right NUMBER is not right ROWS: a predicate that returned the wrong states in the
      // right quantity would pass everything above.
      expect(count, 'ACCEPTED must select some rows').toBeGreaterThan(0)
      expect(count, 'ACCEPTED must select FEWER rows than ALL, or nothing was filtered').toBeLessThan(allCount)
      const texts = await rows(page).allTextContents()
      expect(texts.every((t) => t.includes('ACCEPTED')), `ACCEPTED filter returned: ${texts.join(' | ')}`).toBe(true)
    }
  }

  expect(errors, `console errors filtering support submissions:\n${errors.join('\n')}`).toEqual([])
})

// Tenant lookup is the cross-tenant read this console exists to provide. Three separate
// claims: the search narrows the list, the selection SURVIVES being narrowed out of it
// (Tenants.tsx:38-40 — the detail pane keeps showing what the operator opened), and
// selecting re-resolves the whole record rather than just its name.
test('support-console: tenant search narrows the list, selection fills the detail pane, and view-as is audited', async ({ page }) => {
  const errors = collectErrors(page)
  await signInAs(page, 'support')
  await goTo(page, 'Tenants', 'Tenants & entities')

  // The tenant rows reuse the sidebar's `.ops-nav` class (Tenants.tsx:69); scoping to
  // <main> is what disambiguates them from the five nav buttons.
  const list = page.getByRole('main').locator('button.ops-nav')
  const listCount = await list.count()
  // Vacuity guard, not a fixture assertion: the test needs at least one tenant other than
  // the default selection to have anything to switch to.
  expect(listCount, 'the tenant list needs a second row to select').toBeGreaterThan(1)

  // The only <h2> in the whole console, so this is strict-mode safe.
  const detail = page.getByRole('heading', { level: 2 })
  const defaultName = ((await detail.textContent()) ?? '').trim()
  expect(defaultName, 'the detail pane opens on a tenant').not.toBe('')

  // Search by the target's TIN rather than a hardcoded name fragment: a TIN is unique by
  // construction, so "narrows to exactly one" is a claim about the predicate
  // (Tenants.tsx:37 matches on `name tin`), not about how the seed happens to be spelled.
  // The TIN is the row's only `.mono` span.
  const targetTin = ((await list.last().locator('.mono').first().textContent()) ?? '').trim()
  expect(targetTin, 'a tenant row renders its TIN').toMatch(/^\d+-\d+$/)

  await page.getByLabel('Search tenants').fill(targetTin)
  await expect(list).toHaveCount(1)
  await expect(list.first()).toContainText(targetTin)

  // The selection survives the narrowing: the pane still shows what the operator opened,
  // even though that tenant is no longer in the visible list.
  await expect(detail).toHaveText(defaultName)

  await list.first().click()
  // The name is read back from the pane rather than scraped out of the row by position —
  // the row's name span has no class, and an nth-child locator is the brittleness this
  // repo avoids. The claim is then made in both directions: the pane's name appears in the
  // clicked row, and the row's TIN appears in the pane. A pane wired to the wrong tenant
  // fails one of them.
  const selectedName = ((await detail.textContent()) ?? '').trim()
  expect(selectedName, 'selecting a different tenant must change the detail pane').not.toBe(defaultName)
  await expect(list.first()).toContainText(selectedName)
  await expect(list.first()).toHaveAttribute('aria-pressed', 'true')
  // The whole record re-resolved, not just the name: the TIN line is rendered from the
  // selected tenant's own fields plus its entity/plan summary (Tenants.tsx:102). Requiring
  // the "· " to follow is what makes this an assertion about the summary too. The row's own
  // TIN span carries no "TIN " prefix, so this resolves to the detail pane alone.
  const tinLine = page.getByRole('main').locator('.mono').filter({ hasText: `TIN ${targetTin} · ` })
  await expect(tinLine, 'the detail pane shows the selected tenant TIN and entity/plan summary').toHaveCount(1)

  // View-as is a cross-tenant read, which is why it is audited. Scoping to role=status is
  // mandatory: the tenant name is simultaneously in the list row and the <h2>, so a bare
  // getByText would be a strict-mode violation.
  await page.getByRole('button', { name: 'View-as (read-only)' }).click()
  const toast = page.getByRole('status')
  await expect(toast).toContainText(`Opened ${selectedName} in read-only view-as`)
  await expect(toast).toContainText('AUDIT ON ACCREDITATION')

  expect(errors, `console errors looking up a support tenant:\n${errors.join('\n')}`).toEqual([])
})
