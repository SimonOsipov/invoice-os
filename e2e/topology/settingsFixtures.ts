// Settings › Members fixtures -- the live membership directory, and the display copy behind
// every disabled control on both Settings tabs (Members and Roles both read from here).
//
// SEED_* -- db/seed.dev.sql, verbatim. A real membership row: identity, access role and
// status, read back over the wire by GET /api/tenancy/v1/memberships. A failure here is a
// BACKEND contract failure -- the seed, the query, the projection or RLS.
//
// UNBACKED_* -- display copy transcribed from frontend/app/src/lib/members.ts's
// MEMBER_UNBACKED, PROTECTED_ADMIN_NOTE and the drawer's danger-zone strings. Transcribed,
// never imported: this package has no dependency on frontend/app/src (e2e/tsconfig.json), and
// a second copy is what catches a one-sided edit.
//
// Split out of roles.spec.ts's old combined fixture module (APPR-04-07): the other half
// named a role store that no longer exists and was deleted, re-derived from scratch as
// roles.spec.ts's own SEED_*_ROLE_CARDS. This half was already correct against the live
// wire and moves verbatim.
//
// Collected by nothing: Playwright's topology config matches '**/*.spec.ts' and vitest
// matches '**/*.test.ts'. It IS typechecked (e2e/tsconfig.json includes `topology`).

// ---------------------------------------------------------------------------------------
// SEED — the live membership directory
// ---------------------------------------------------------------------------------------

/** One seeded membership row, as the roster table renders it. */
export interface SeededMember {
  /** `display_name`. Also the handle every locator in roles.spec.ts uses to find the row. */
  name: string
  /** `email`, rendered under the name in the Person cell. */
  email: string
  /** `accessRoleLabel(role)` — the Access role cell. Never the raw enum value. */
  accessRole: string
  /** `MemberStatusPill`'s label — the status cell, uppercased in the DOM. */
  pill: 'ACTIVE' | 'INVITED' | 'SUSPENDED'
}

// Order is deliberately NOT asserted: the list is `ORDER BY created_at, user_id`, and a row
// inserted later would sort after these however it was named. The SET and its size are the
// claim.
//
// A literal length IS pinned, against persona-surfaces.spec.ts's ban on literal counts over
// live lists, because this list cannot grow: no endpoint mints a membership (there is no
// invite), and PATCH writes `status` only. api/isolation.spec.ts already pins the exact
// user_id set for the same reason.
export const SEED_FIRM_MEMBERS: readonly SeededMember[] = [
  { name: 'Chinedu Okafor', email: 'c.okafor@okafor.ng', accessRole: 'Admin', pill: 'ACTIVE' },
  { name: 'Folake Adesina', email: 'f.adesina@okafor.ng', accessRole: 'Preparer', pill: 'ACTIVE' },
  { name: 'Musa Danjuma', email: 'm.danjuma@okafor.ng', accessRole: 'Reviewer', pill: 'ACTIVE' },
  { name: 'Chiamaka Nwosu', email: 'c.nwosu@okafor.ng', accessRole: 'Reviewer', pill: 'ACTIVE' },
  {
    name: 'Oluwaseyifunmi Adebanjo-Ogunleye',
    email: 'o.adebanjo-ogunleye@okaforandpartners.com.ng',
    accessRole: 'Preparer',
    pill: 'ACTIVE',
  },
  // The firm's suspended row, and the only seeded person holding no workflow role at all —
  // both of this tab's exceptional states on one line.
  { name: 'Halima Yusuf', email: 'h.yusuf@okafor.ng', accessRole: 'Reviewer', pill: 'SUSPENDED' },
]

// …0012 Adebayo Ogunlesi is suspended AND the sole `cfo` holder, which is what makes a seat
// unsignable while still naming a person.
export const SEED_INHOUSE_MEMBERS: readonly SeededMember[] = [
  { name: 'Ngozi Balogun', email: 'n.balogun@honeywell.ng', accessRole: 'Admin', pill: 'ACTIVE' },
  { name: 'Yetunde Fashola', email: 'y.fashola@honeywell.ng', accessRole: 'Reviewer', pill: 'ACTIVE' },
  { name: 'Emeka Uzowulu', email: 'e.uzowulu@honeywell.ng', accessRole: 'Reviewer', pill: 'ACTIVE' },
  { name: 'Tunde Adeyemi', email: 't.adeyemi@honeywell.ng', accessRole: 'Reviewer', pill: 'ACTIVE' },
  { name: 'Ibrahim Bello', email: 'i.bello@honeywell.ng', accessRole: 'Reviewer', pill: 'ACTIVE' },
  { name: 'Adebayo Ogunlesi', email: 'a.ogunlesi@honeywell.ng', accessRole: 'Reviewer', pill: 'SUSPENDED' },
  { name: 'Zainab Lawal', email: 'z.lawal@honeywell.ng', accessRole: 'Preparer', pill: 'ACTIVE' },
]

/**
 * The roster's column heads, in order. The trailing '' is the unlabelled `⋯` column.
 *
 * Pinned as an EXACT list because three columns were deleted rather than hidden — firm's
 * client scoping, in-house's department, and Last active. A "does not contain" sweep would
 * pass on a column re-added under a different label.
 */
export const MEMBERS_TABLE_HEADS: readonly string[] = ['Person', 'Access role', 'Workflow roles', 'Status', '']

// ---------------------------------------------------------------------------------------
// UNBACKED — the sentence each dead control states for itself
// ---------------------------------------------------------------------------------------

/**
 * `MEMBER_UNBACKED`, `PROTECTED_ADMIN_NOTE` and the drawer's suspend explanation. Every one
 * of these is rendered as VISIBLE text beside its control, not only as a `title`: a disabled
 * control is out of the tab order and `title` never fires on one in Chromium, so the visible
 * sibling is the only layer a screenshot, a keyboard user and an assertion can all reach.
 */
export const UNBACKED = {
  invite: 'There is no invite endpoint yet — nothing mints a token, tracks an expiry, or sends the email.',
  remove: 'Deleting a membership locks that person out on their next request, and nothing undoes it. That decision has not been taken.',
  role: "The membership endpoint writes status only. Changing someone's access role has no server call behind it.",
  department: 'A membership stores a name, an email, an access role and a status. There is no department column.',
  clientAccess: 'Client access is not stored per person — everyone in this workspace sees the same clients.',
} as const

/** The §9 last-admin lock, on the sole active admin's own Suspend. Derived from LIVE rows. */
export const PROTECTED_ADMIN_NOTE = "You're the only admin. Promote someone else first."

/**
 * What suspension actually does — and the copy `[suspend-copy-is-true]` flagged. It is
 * SUSPEND-ONLY: beside `Reactivate` it would assert the opposite of the button's effect, so
 * a suspended member's drawer must NOT carry it. Both halves are asserted.
 */
export const SUSPEND_EXPLANATION =
  'Removes their approver rights and keeps all history. Sign-in is not blocked yet. Their name stays on every invoice they touched.'

/** The drawer's amber note on a suspended person who is named in approval steps. */
export const SUSPENDED_STEPS_NOTE = 'They are suspended, so those steps will block until someone else holds this role.'
