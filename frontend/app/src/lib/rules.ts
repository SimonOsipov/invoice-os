// The client-facing validation rule set (Rules screen).
//
// The structural difference from the internal Support Console's "Rules admin" is the
// whole point of this surface: there, one operator edits ONE global rule set and
// publishes versions of it. Here the golden ruleset is PUBLISHED BY ASComply and is
// read-only to the tenant — a client can only stack its own custom rules on top, and
// those evaluate after the golden ones. Nothing in this module can disable or edit a
// golden rule, and that is enforced by the types: only CustomRule carries `enabled`.
//
// Everything here is mock data + pure functions. There is no rules endpoint yet
// (§2.6 of the brief lists what the backend will need); the screen is deliberately
// shaped so that swapping these constants for a fetch changes no component.

export type RuleSeverity = 'error' | 'warn' | 'info'

/** One read-only label/value pair in the drawer's parameter block. */
export type RuleParam = { label: string; value: string }

/** The shape both halves of the table render through — golden and custom alike. */
export type Rule = {
  key: string
  type: string
  field: string
  severity: RuleSeverity
  message: string
  params: RuleParam[]
}

/** A tenant's own rule. `enabled` exists ONLY here: golden rules are always on. */
export type CustomRule = Rule & { enabled: boolean }

/**
 * A rule inferred from this tenant's own rejection history, offered for adoption.
 * Carries no `message` on purpose — an accepted suggestion is stamped with
 * SUGGESTION_MESSAGE instead, so a rule that has not been reviewed yet says so in
 * the Message column rather than looking like a rule somebody wrote.
 */
export type Suggestion = Omit<Rule, 'message'> & { derivation: string }

/** The published ruleset every tenant inherits. Versioned centrally, never per-tenant. */
export const GOLDEN_SET = { id: 'NG-MBS', version: 'v8' } as const

export const GOLDEN_RULES: Rule[] = [
  {
    key: 'buyer.tin.required',
    type: 'required',
    field: 'buyer.tin',
    severity: 'error',
    message: 'Buyer TIN is mandatory',
    params: [{ label: 'Applies when', value: 'always' }],
  },
  {
    key: 'buyer.tin.format',
    type: 'regex',
    field: 'buyer.tin',
    severity: 'error',
    message: 'TIN must match 00000000-0000',
    params: [
      { label: 'Pattern', value: '^\\d{8}-\\d{4}$' },
      { label: 'Flags', value: 'none' },
    ],
  },
  {
    key: 'vat.math',
    type: 'expression-CEL',
    field: 'totals.vat',
    severity: 'error',
    message: 'VAT must equal 7.5% of taxable base',
    params: [
      { label: 'CEL', value: 'totals.vat == round(totals.taxable * 0.075, 2)' },
      { label: 'Tolerance', value: '±0.01 NGN' },
    ],
  },
  {
    key: 'line.description.required',
    type: 'required',
    field: 'lines[].description',
    severity: 'error',
    message: 'Every line needs a description',
    params: [{ label: 'Applies when', value: 'every line' }],
  },
  {
    key: 'currency.enum',
    type: 'enum',
    field: 'header.currency',
    severity: 'error',
    message: 'Currency must be NGN, USD or EUR',
    params: [{ label: 'Allowed values', value: 'NGN, USD, EUR' }],
  },
  {
    key: 'invoice.no.unique',
    type: 'expression-CEL',
    field: 'header.invoice_no',
    severity: 'error',
    message: 'Invoice number must be unique per seller',
    params: [{ label: 'CEL', value: 'unique(header.invoice_no, seller.tin)' }],
  },
  {
    key: 'issue.date.sequence',
    type: 'date_rule',
    field: 'header.issue_date',
    severity: 'warn',
    message: 'Issue date must not precede prior invoice',
    params: [{ label: 'Constraint', value: 'issue_date >= prev.issue_date' }],
  },
]

export type GoldenVersion = {
  version: string
  meta: string
  tag: string
  kind: 'active' | 'superseded'
}

// The live version's rule count is DERIVED, not typed in: a hand-written count is a
// second answer to "how many rules are running" that drifts the moment the seed set
// changes, and it would sit two inches from the table that contradicts it.
export const GOLDEN_VERSIONS: GoldenVersion[] = [
  { version: GOLDEN_SET.version, meta: `eff. 2026-06-01 · ${GOLDEN_RULES.length} rules`, tag: 'IN USE', kind: 'active' },
  { version: 'v7', meta: 'eff. 2026-04-15 · 40 rules', tag: 'SUPERSEDED', kind: 'superseded' },
]

/** Seed custom rules. Every client starts from its OWN copy of this list. */
export const SEED_CUSTOM_RULES: CustomRule[] = [
  {
    key: 'po.number.required',
    type: 'required',
    field: 'header.po_number',
    severity: 'error',
    message: 'Purchase-order number is required on every invoice',
    params: [{ label: 'Applies when', value: 'always' }],
    enabled: true,
  },
  {
    key: 'buyer.approved.list',
    type: 'enum',
    field: 'buyer.tin',
    severity: 'error',
    message: 'Buyer must be on the approved customer list',
    params: [
      { label: 'Source list', value: 'customers/approved' },
      { label: 'Entries', value: '142 buyer TINs' },
    ],
    enabled: true,
  },
  {
    key: 'cost.centre.required',
    type: 'required',
    field: 'lines[].cost_centre',
    severity: 'warn',
    message: 'Every line needs a cost centre',
    params: [{ label: 'Applies when', value: 'every line' }],
    enabled: true,
  },
  {
    key: 'invoice.value.cap',
    type: 'range',
    field: 'totals.gross',
    severity: 'warn',
    message: 'Invoices above ₦500M need director approval',
    params: [
      { label: 'Max', value: '₦500,000,000' },
      { label: 'On breach', value: 'warn + route to director approval' },
    ],
    enabled: true,
  },
  {
    key: 'wht.required.services',
    type: 'cross_field',
    field: 'lines[].wht',
    severity: 'warn',
    message: 'WHT expected on service lines',
    params: [
      { label: 'When', value: 'line.type == "service"' },
      { label: 'Require', value: 'line.wht > 0' },
    ],
    enabled: false,
  },
]

/** Inferred from this tenant's OWN rejections — never from the global corpus. */
export const SUGGESTED_RULES: Suggestion[] = [
  {
    key: 'buyer.email.format',
    type: 'regex',
    field: 'buyer.email',
    severity: 'warn',
    derivation: 'Derived from 9 rejections on your invoices',
    params: [
      { label: 'Pattern', value: '^[^@\\s]+@[^@\\s]+\\.[^@\\s]+$' },
      { label: 'Flags', value: 'none' },
    ],
  },
  {
    key: 'lines[].hsn.required',
    type: 'required',
    field: 'lines[].hsn',
    severity: 'error',
    derivation: 'Derived from 6 rejections on your invoices',
    params: [{ label: 'Applies when', value: 'every line' }],
  },
  {
    key: 'fx.rate.range',
    type: 'range',
    field: 'header.fx_rate',
    severity: 'info',
    derivation: 'Derived from 4 rejections on your USD invoices',
    params: [
      { label: 'Min', value: '0.5 × CBN mid-rate' },
      { label: 'Max', value: '1.5 × CBN mid-rate' },
    ],
  },
]

/** Stamped on any rule adopted from a suggestion until somebody reviews it. */
export const SUGGESTION_MESSAGE = 'Added from a suggestion — review before enabling'

/** Custom rules by client. Keyed by `customRulesKey`, so switching client swaps the set. */
export type CustomRuleStore = Record<string, CustomRule[]>

/**
 * The store key for a client. `entityId` is null whenever a workspace has no entity
 * resolved yet — an in-house tenant before its first entity (AC-3's bootstrap window)
 * or the emptyClient()/no-gateway placeholder — and every such read lands on the shared
 * 'workspace' bucket regardless of persona or of WHICH entity-less workspace it is.
 *
 * task-304 (INVCR-01-19) AC-9: before that story, in-house had no route to ever create
 * an entity, so `entityId` was null there PERMANENTLY and 'workspace' was its only key
 * for the life of the app. Now an in-house tenant can create its one entity mid-session
 * (Settings > Company, [entity-picker]) — `active.entityId` (and therefore this key)
 * CAN change from 'workspace' to a real id within a session. A rule added under
 * 'workspace' before that point becomes invisible once `rulesKey` moves to the real id
 * (App.tsx keeps both entries in `customRuleStore`; only the READ side stops finding the
 * old one). No fallback/migration is added for this: `customRuleStore` is plain React
 * state (App.tsx's `useState<CustomRuleStore>({})`), never persisted to localStorage or
 * anywhere else — it is wiped on every reload regardless, so the ONLY window in which
 * this can be observed at all is a single session, between adding a custom rule with no
 * entity yet and creating that entity, with no reload in between. Judged an acceptable
 * edge case: it is new-tenant-onboarding-only (unreachable again once an entity exists),
 * self-healing on the next reload, and building a merge-on-create migration for
 * in-memory state that never outlives the tab would be solving a persistence problem
 * that does not exist here.
 */
export function customRulesKey(entityId: string | null): string {
  return entityId ?? 'workspace'
}

/** A client not yet in the store has never been edited, so it reads the seed set. */
export function customRulesFor(store: CustomRuleStore, key: string): CustomRule[] {
  return store[key] ?? SEED_CUSTOM_RULES
}

/** Suggestions still on offer: one disappears the moment its key exists as a custom rule. */
export function openSuggestions(all: readonly Suggestion[], custom: readonly CustomRule[]): Suggestion[] {
  const taken = new Set(custom.map((r) => r.key))
  return all.filter((s) => !taken.has(s.key))
}

/** Adopting a suggestion always produces a DISABLED rule — never a live one (§2.6). */
export function ruleFromSuggestion(s: Suggestion): CustomRule {
  return {
    key: s.key,
    type: s.type,
    field: s.field,
    severity: s.severity,
    message: SUGGESTION_MESSAGE,
    params: s.params,
    enabled: false,
  }
}

// The three custom-rule reducers. Pure and total: each returns the input array
// untouched when the key does not apply, so a double-click (or a suggestion whose
// key is already present) can never append a duplicate row.
export function addSuggested(rules: readonly CustomRule[], s: Suggestion): CustomRule[] {
  if (rules.some((r) => r.key === s.key)) return rules as CustomRule[]
  return [...rules, ruleFromSuggestion(s)]
}

export function toggleCustom(rules: readonly CustomRule[], key: string): CustomRule[] {
  return rules.map((r) => (r.key === key ? { ...r, enabled: !r.enabled } : r))
}

export function removeCustom(rules: readonly CustomRule[], key: string): CustomRule[] {
  return rules.filter((r) => r.key !== key)
}

/** Tenant identifier used in the drawer's JSON `source` for a custom rule. */
export function tenantSlug(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

/** `ascomply/golden:NG-MBS@v8` — the same string for every tenant, because it is. */
export const GOLDEN_SOURCE_REF = `ascomply/golden:${GOLDEN_SET.id}@${GOLDEN_SET.version}`

/**
 * The drawer's JSON block, generated FROM the rule so it can never contradict the row
 * above it. Built through JSON.stringify rather than string interpolation: a message
 * or CEL expression containing a quote or backslash (`^\d{8}-\d{4}$` already does)
 * would otherwise emit JSON that does not parse.
 */
export function ruleJSON(rule: Rule, sourceRef: string, enabled: boolean): string {
  const params: Record<string, string> = {}
  for (const p of rule.params) params[tenantSlug(p.label).replace(/-/g, '_')] = p.value
  return JSON.stringify(
    {
      key: rule.key,
      type: rule.type,
      field: rule.field,
      severity: rule.severity,
      source: sourceRef,
      enabled,
      params,
      message: rule.message,
    },
    null,
    2,
  )
}
