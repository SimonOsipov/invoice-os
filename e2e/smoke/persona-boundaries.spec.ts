import { test, expect } from '@playwright/test'
import { BOUNDARY_MATRIX } from '../personas'
import { collectErrors, expectRefused } from '../personaSession'

// The cross-persona boundary matrix (PERSONA-01-02, Backlog task-271): every destination
// handed a persona it does not admit, asserting the visitor is sent back to the landing
// page. The cells are DERIVED from BOUNDARY_MATRIX in ../personas — never hand-listed — so
// a fifth persona adds its three refusal rows here automatically, and G5 (personas.test.ts)
// keeps the matrix itself complete.
//
// ROUTING, NOT AUTHORISATION. Both console gates say so about themselves in prose —
// frontend/ops-console/src/session.ts:8-12 ("Deliberately NOT access control, and not a
// security boundary... a fabricated localStorage entry is enough to get in") and
// frontend/support-console/src/session.ts:5-9, which makes the same call for the same
// reason. Neither console has a backend to protect; both are mock data (data.tsx). A
// fabricated localStorage entry still gets in, and nothing here would notice. Real
// enforcement is a verified token (M7) on top of real identities (M8). A GREEN MATRIX IS
// NOT AN ACCESS-CONTROL PROOF — it is a proof about where the browser ends up.
//
// WHAT IS NEW HERE. Three specs already assert a redirect off a protected origin:
// smoke.spec.ts:66-75 (ops-console and support-console) and topology/auth.spec.ts:133-138
// (the app). All three visit a BARE url with no `?persona=` at all. This spec makes the
// claim none of them makes: a FOREIGN PERSONA PARAM IS NOT A CREDENTIAL — a present-but-
// wrong `?persona=` is refused exactly as an absent one is. Same code path in the product
// (operatorFromParam returns null for absent and for foreign alike; shouldAutoSignIn is
// false for both), different precondition, and only one of the two was pinned before.
//
// THE POSITIVE CONTROL. A redirect assertion alone would pass against a destination that
// renders for nobody, so this file does not stand on its own: smoke/apps.ts in THIS suite
// proves each destination draws its own discriminator when it admits the persona (landing's
// h1, the ops console's "Overview", the support console's "Submissions ops"). If a
// destination stopped admitting anyone, that spec goes red first. On top of that, each
// refusal below asserts the landing page's own h1 actually rendered — upgrading "the URL
// changed" to "the live landing application drew", which fails on a blank page, an error
// page, or a URL-prefix match that is not the landing SPA. That is the same discriminator
// smoke/apps.ts:24-28 uses, and landing has exactly one h1, so it is strict-mode safe.
//
// WHAT THIS SPEC DOES NOT PROVE. It does not prove that no frame of console chrome painted
// before the redirect fired, and no post-redirect assertion can: by the time waitForURL
// resolves the destination document is gone, so a `not.toBeVisible()` on a destination
// discriminator would run against the LANDING dom and could only ever fail if landing grew
// an h1 "Overview". Such an assertion is vacuous by construction and is deliberately absent
// here. That property is held instead by the three render gates, each of which returns null
// before its effect matters: frontend/app/src/App.tsx:889,
// frontend/ops-console/src/App.tsx:50-52, frontend/support-console/src/App.tsx:51-53.
//
// WHY ONLY THE 8 REFUSALS ([refusals-only-in-the-matrix]). The matrix's four accept cells
// are covered where they belong and are not re-driven here: the two console accepts by
// smoke/apps.ts and app-firm by topology/auth.spec.ts, both green today; app-inhouse is
// PERSONA-01-03's, still to come. G5 (personas.test.ts) is what keeps the matrix itself
// complete meanwhile — it pins all 12 rows whether or not a spec drives them.
//
// WHY SMOKE, NOT TOPOLOGY ([boundaries-in-smoke]). Every cell below is refused
// SYNCHRONOUSLY, in a useState initializer over the URL and localStorage, and the redirect
// fires from the first-commit effect — before any fetch. The app never mounts <Workspace>,
// so neither the sign-in mint nor /v1/me nor the entities/rollup reads ever run, and both
// consoles are pure mock data. Zero gateway contact, zero database reads or writes, so
// topology's `workers: 1` rationale (a shared, un-reset deployed database) does not apply
// and this file is safe under smoke's `fullyParallel: true`.

for (const { persona, destination } of BOUNDARY_MATRIX.filter((c) => c.verdict === 'refuses')) {
  test(`${destination}: refuses the ${persona} persona and returns it to the landing page`, async ({ page }) => {
    // Attached BEFORE the navigation inside expectRefused, so load-time errors are caught.
    // Each test gets Playwright's default fresh context, which is the mechanism that keeps
    // resolveBootSession() / loadOpsSession() empty — no session from another cell can leak
    // in and turn a refusal into an accept.
    const errors = collectErrors(page)

    await expectRefused(page, persona, destination)

    // expectRefused asserts the URL and nothing else, by design. The landing render check
    // belongs at the call site.
    const h1 = page.getByRole('heading', { level: 1 })
    await expect(h1).toBeVisible()
    await expect(h1).toContainText(/e-invoicing/i)

    expect(errors, `console errors refusing ${persona} at ${destination}:\n${errors.join('\n')}`).toEqual([])
  })
}
