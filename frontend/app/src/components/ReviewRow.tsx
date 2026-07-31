// Review · Invoices tab — one row plus its row-expansion fix editor (INVCR-01-14,
// task-290, §7.3 "Row expansion — the fix loop, and the reason this screen exists").
// Split OUT of ReviewInvoicesTab.tsx (was a local `Row` sub-component there, task-286)
// because this subtask adds a second, independently-fetched child (the expansion panel)
// and the story's own Implementation Plan names this file explicitly.
//
// The whole-row click now TOGGLES EXPANSION instead of navigating to InvoiceDetail
// ([navigate-vs-expand], resolving the comment task-286 left on the chevron below): §7.3's
// row expansion IS the fix loop for this table, InvoiceDetail.tsx is out of scope for this
// subtask (§14), and subtask 16's own e2e AC-1 drives "expand a row → fix the field →
// re-validate" directly against this table, never a click-through. `ctx.openImportedInvoice`
// stays wired everywhere else (InvoicesList.tsx, the N=1 post-import route) — only this
// table's row disclosure changes.
//
// Field targeting is ENTIRELY server-driven (D8) via lib/reviewBatch.ts's fixCard/
// rowExpansionView, which read violation.path through the SHIPPED mbsPathToEditField
// (lib/invoices.ts) alone. No rule-key map is authored here. A `field: null` card (an
// unmappable path — `line_items`, `invoice_number`, any APP-only vocabulary) renders its
// `message` in full with no editor and no invented field name (AC-2).
//
// Only ONE row expands at a time (ReviewInvoicesTab.tsx owns `expandedId`) — this bounds
// the panel's own getInvoice fetch to at most one in flight, and matches the tab-switch
// reset convention already established for this table (task-287 QA Stage 4, cross-
// referenced on this subtask's own Implementation Notes): switching tabs unmounts this
// whole table, so an expanded row's draft is destroyed along with everything else, and
// that is the accepted behaviour, not a gap to patch.
//
// Save and Re-validate are TWO SEPARATE actions, mirroring InvoiceDetail.tsx's own
// Edit/Re-validate split (§10.5's ordering constraint: an edit demotes validated→draft
// FIRST, store.go:864-868, and only THEN does `can_revalidate` read true) rather than one
// combined button — Re-validate's disabled state is `!inv.can_revalidate` alone (AC-4),
// with no local override for "there are unsaved edits".

import { useState } from 'react'

import { ErrorState, Loading, useAsync } from '@invoice-os/api-client'

import { chevDownGlyph } from '../glyphs'
import { fmt, fmtDate } from '../lib/format'
import {
  diffLineItems,
  editInvoice,
  getInvoice,
  isRowSelectable,
  keepInvoiceAsIs,
  revalidateInvoice,
  type EditFieldKey,
  type InvoiceDetailRecord,
  type InvoiceEditInput,
  type InvoiceRecord,
} from '../lib/invoices'
import {
  canKeepAsIs,
  EDIT_FIELD_LABELS,
  fixEditPatch,
  rowExpansionView,
  ROW_EXPANSION_COPY,
  verdictPill,
  type FixCard,
} from '../lib/reviewBatch'
import { severityStyle } from '../lib/validationApi'
import type { PlatformCtx } from '../types'

// Decision 19's grid, minus the 40px `Ln` track (ReviewInvoicesTab.tsx's own file-header
// comment explains why): select-all · Invoice # (mono) · Buyer · Issue date · Total ·
// Verdict · chevron. Owned HERE (not ReviewInvoicesTab.tsx) because the row and the
// table's own head row must never disagree about the grid — a single export is what
// makes that structural rather than remembered.
export const REVIEW_GRID_COLUMNS = '26px 122px minmax(120px,1fr) 92px 114px 124px 22px'
export const REVIEW_GRID_GAP = 9

// `aria-describedby` target for the disabled Re-validate button's reason text. A module
// const, matching InvoiceDetail.tsx's own REVALIDATE_REASON_ID precedent — safe because
// at most one row is ever expanded at a time (ReviewInvoicesTab.tsx's `expandedId`), so
// this id cannot collide with itself in one document.
const REVALIDATE_REASON_ID = 'review-row-revalidate-blocked-reason-text'

export function Row({
  r,
  checked,
  expanded,
  onToggleExpand,
  onToggle,
  ctx,
  base,
  onChanged,
}: {
  r: InvoiceRecord
  checked: boolean
  expanded: boolean
  onToggleExpand: () => void
  onToggle: () => void
  ctx: PlatformCtx
  base: string
  // Fired after a successful Save or Re-validate so the PARENT re-fetches this page
  // (moving this row's own verdict pill, which is derived from InvoiceRecord.status/
  // violations, not from anything local to this component) and the shell re-runs its
  // four count queries. ReviewInvoicesTab.tsx wires this to the SAME `onSubmitted` prop
  // its bulk-submit path already calls for the identical reason (an invoice's
  // server-side state moved) — a deliberate reuse, not a new callback, to stay inside
  // this subtask's file scope (ReviewBatch.tsx, which names and passes that prop, is
  // out of scope, §14). The name is now imprecise for this second caller; flagged as
  // copy/naming debt in task-290's notes rather than renamed here.
  onChanged: () => void
}) {
  // `kept_as_is_at` (INVCR-01-15, D6, task-291): now on InvoiceRecord/the list wire
  // for real (invoices.ts) -- passed through so a kept row's pill renders
  // KEPT · INVALID instead of the row's raw N RULES FAILED badge.
  const verdict = verdictPill({ status: r.status, violations: r.violations, kept_as_is_at: r.kept_as_is_at })
  const badge = verdict.badges[0]

  // Click-only row, matching the shipped InvoicesList.tsx precedent this table's OLD
  // (task-286) row already followed. Keyboard activation (role/tabIndex/onKeyDown) for
  // BOTH row surfaces is task-302 — no AC of this subtask covers it, and adding it only
  // here would preempt that ticket's own scope rather than close it; a fake `<a href>`
  // is not an option either way, since this SPA has no router.
  return (
    <>
      <div
        onClick={onToggleExpand}
        data-testid="review-row"
        aria-expanded={expanded}
        className="pf-row pf-list-row"
        style={{ display: 'grid', gridTemplateColumns: REVIEW_GRID_COLUMNS, gap: REVIEW_GRID_GAP, padding: '14px 18px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}
      >
        <input
          type="checkbox"
          data-testid="review-select"
          aria-label={`Select invoice ${r.invoice_number}`}
          checked={checked}
          disabled={!isRowSelectable(r.status)}
          // BOTH handlers stop propagation — the row's own onClick toggles expansion and
          // must never fire from a checkbox interaction.
          onClick={(e) => e.stopPropagation()}
          onChange={(e) => {
            e.stopPropagation()
            onToggle()
          }}
        />
        <span className="mono" style={{ fontSize: 12.5, fontWeight: 500, color: 'var(--fg-1)' }}>{r.invoice_number}</span>
        {/* Buyer name + TIN, InvoicesList.tsx:419-422's treatment verbatim: this is the
            compliance review surface and `buyer-tin-format` is a live rule, so a missing
            TIN is the single most useful thing this column can shout about. */}
        <span style={{ minWidth: 0 }}>
          <span style={{ display: 'block', fontSize: 13.5, fontWeight: 500, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{r.buyer_name ?? '—'}</span>
          <span className="mono" style={{ fontSize: 11, color: r.buyer_tin ? 'var(--fg-3)' : 'var(--status-red-text)' }}>{r.buyer_tin ?? 'TIN MISSING'}</span>
        </span>
        {/* NO `?? created_at` fallback, unlike InvoicesList.tsx:424 — that column is
            labelled "Date"; this one says "Issue date", and labelling a creation timestamp
            as the issue date is a small lie on a compliance screen. */}
        <span className="mono" style={{ fontSize: 12, color: 'var(--fg-3)' }}>{r.issue_date != null ? fmtDate(r.issue_date) : '—'}</span>
        <span className="money" style={{ fontSize: 13.5, fontWeight: 600, textAlign: 'right' }}>{r.total != null ? fmt(Number(r.total)) : '—'}</span>
        {/* The status badge is InvoicesList.tsx:425-433's markup verbatim, driven entirely
            by verdictPill(...).status — no colour and no label is authored here. The
            derived badge stacks BENEATH rather than beside it: 124px cannot hold both. */}
        {/* `data-testid="review-verdict"` sits on this OUTER span, not the inner status
            pill alone -- "the verdict pill" (this file's own doc comments, and the e2e
            suite's) means status label + badge TOGETHER: a kept/failing/advisory badge
            is a fact ABOUT the verdict, not a separate one. Scoping the testid to the
            inner span only would make 'KEPT'/'RULES FAILED'/'ADVISORY' structurally
            unreachable through this locator regardless of whether verdictPill computed
            them correctly (found live, INVCR-E2E-7: kept-as-is's badge never surfaced
            through `review-verdict` even though the wire and verdictPill were both
            already correct). */}
        <span data-testid="review-verdict" style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-start', gap: 4, minWidth: 0 }}>
          <span
            style={{ display: 'inline-flex', alignItems: 'center', gap: 6, background: verdict.status.bg, border: `1px solid ${verdict.status.border}`, borderRadius: 999, padding: '3px 9px' }}
          >
            <span style={{ width: 6, height: 6, borderRadius: 99, background: verdict.status.text }} />
            <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: verdict.status.text, letterSpacing: '0.04em' }}>{verdict.status.label}</span>
          </span>
          {badge != null && (
            <span
              className="mono"
              style={{ display: 'inline-flex', alignItems: 'center', background: badge.tone.bg, border: `1px solid ${badge.tone.border}`, borderRadius: 999, padding: '2px 8px', fontSize: 9.5, fontWeight: 600, color: badge.tone.text, letterSpacing: '0.04em' }}
            >
              {badge.label}
            </span>
          )}
        </span>
        {/* The row-disclosure indicator (INVCR-01-14) — the row itself is the click
            target (onClick above); this glyph is purely presentational, matching
            Sidebar.tsx's own switcher-chevron rotation idiom. Collapsed points right
            (task-286's original static orientation, unchanged), expanded points down. */}
        <span aria-hidden style={{ display: 'inline-flex', color: 'var(--fg-4)', pointerEvents: 'none', transform: expanded ? 'rotate(0deg)' : 'rotate(-90deg)', transition: 'transform 160ms' }}>
          {chevDownGlyph}
        </span>
      </div>
      {expanded && <ExpandedFixPanel invoiceId={r.id} ctx={ctx} base={base} onChanged={onChanged} />}
    </>
  )
}

// One fix-editor card (§7.3): severity pill, mono rule key, the server's message
// VERBATIM, an inline editor scoped to `card.field` (absent when unmappable — AC-2), and
// the mono expectation from `card.hint` (expected/actual, D9). No card-level Save — the
// PANEL below holds one shared draft and one Save action across however many editable
// cards this row has (mirrors InvoiceDetail's own one-PATCH-per-save discipline).
function FixCardView({
  card,
  value,
  onChange,
  disabled,
}: {
  card: FixCard
  value: string
  onChange: (v: string) => void
  disabled: boolean
}) {
  const st = severityStyle(card.severity)
  return (
    <div data-testid="review-fix-card" style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: 14, background: 'var(--bg-2)' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8, flexWrap: 'wrap' }}>
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, background: st.bg, border: `1px solid ${st.border}`, borderRadius: 999, padding: '3px 9px' }}>
          <span style={{ width: 6, height: 6, borderRadius: 99, background: st.text }} />
          <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: st.text }}>{st.label}</span>
        </span>
        <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>{card.ruleKey}</span>
      </div>
      <p style={{ fontSize: 13, color: 'var(--fg-2)', margin: '0 0 10px', lineHeight: 1.5 }}>{card.message}</p>
      {card.field != null && (
        <div style={{ marginBottom: card.hint != null ? 8 : 0 }}>
          <div className="label" style={{ marginBottom: 6 }}>{EDIT_FIELD_LABELS[card.field]}</div>
          <input
            className="pf-input"
            type="text"
            data-testid="review-fix-input"
            value={value}
            onChange={(e) => onChange(e.target.value)}
            disabled={disabled}
          />
        </div>
      )}
      {card.hint != null && (
        <div className="mono" data-testid="review-fix-hint" style={{ fontSize: 11, color: 'var(--fg-3)' }}>{card.hint}</div>
      )}
    </div>
  )
}

// The row-expansion panel: fetches this ONE invoice's full detail (InvoiceRecord list
// rows carry neither `can_revalidate`/`revalidate_blocked_reason` nor `line_items`),
// renders §7.3's fix loop off rowExpansionView, and owns the Save/Re-validate actions.
function ExpandedFixPanel({
  invoiceId,
  ctx,
  base,
  onChanged,
}: {
  invoiceId: string
  ctx: PlatformCtx
  base: string
  onChanged: () => void
}) {
  const detail = useAsync<InvoiceDetailRecord>(() => getInvoice(ctx.authedFetch, base, invoiceId), { deps: [invoiceId] })

  // The shared draft across every editable card in this row — keyed by field, not by
  // card, so two rules that happen to flag the SAME field (unlikely, but structurally
  // possible) show one consistent value rather than two editors that could disagree.
  // Only fields the operator actually touched are present; fixEditPatch reads exactly
  // this set (lib/reviewBatch.ts), so an untouched field can never reach the PATCH.
  const [draft, setDraft] = useState<Partial<Record<EditFieldKey, string>>>({})
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [revalidating, setRevalidating] = useState(false)
  const [revalidateError, setRevalidateError] = useState<string | null>(null)
  // Keep as-is (INVCR-01-15, D6, task-291) -- its own draft/loading/error triple,
  // parallel to Save's and Re-validate's above rather than sharing either: keeping is
  // a THIRD, independent action, not a variant of saving or revalidating.
  const [keepReason, setKeepReason] = useState('')
  const [keeping, setKeeping] = useState(false)
  const [keepError, setKeepError] = useState<string | null>(null)

  const inv = detail.data

  function fieldValue(field: EditFieldKey): string {
    return draft[field] ?? inv?.[field] ?? ''
  }

  function updateField(field: EditFieldKey, value: string) {
    setDraft((d) => ({ ...d, [field]: value }))
  }

  // Save is INDEPENDENT of Re-validate (§10.5's ordering constraint): store.go:864-868
  // demotes validated→draft on a successful edit, server-side — this handler never
  // calls revalidateInvoice itself, it only refetches so `can_revalidate` reflects the
  // new (post-demotion) state.
  async function handleSave() {
    if (inv == null || saving) return
    const patch: InvoiceEditInput = fixEditPatch(inv, draft)
    // The row-expansion editor never touches line items — diffing the SAME content it
    // loaded against itself always resolves `undefined` (INV-06-T7), so this can never
    // attach a `line_items` key (AC-5). Called anyway, not inlined as "always omit", so a
    // future rule that DOES map onto a line field inherits the same no-op-safe path
    // rather than a silent omission nobody re-examined.
    const lines = diffLineItems(inv.line_items ?? [], inv.line_items ?? [])
    if (lines !== undefined) patch.line_items = lines
    // Nothing changed — skip the PATCH (it would 400 on the backend's all-nil check),
    // matching InvoiceDetail.tsx's own no-op-save guard.
    if (Object.keys(patch).length === 0) return
    setSaving(true)
    setSaveError(null)
    try {
      await editInvoice(ctx.authedFetch, base, invoiceId, patch)
      setDraft({})
      detail.run()
      onChanged()
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : 'Something went wrong. Please try again.')
    } finally {
      setSaving(false)
    }
  }

  // AC-3/AC-4: targets THIS invoice only, gated on `!inv.can_revalidate` alone, read off
  // the wire — no client mirror of the draft-only rule.
  async function handleRevalidate() {
    if (inv == null || revalidating || !inv.can_revalidate) return
    setRevalidating(true)
    setRevalidateError(null)
    try {
      await revalidateInvoice(ctx.authedFetch, base, invoiceId)
      detail.run()
      onChanged()
    } catch (err) {
      setRevalidateError(err instanceof Error ? err.message : 'Something went wrong. Please try again.')
    } finally {
      setRevalidating(false)
    }
  }

  // canKeepAsIs (lib/reviewBatch.ts) is the WHOLE gate (KEEP-2): a trimmed-empty
  // reason both disables the button AND short-circuits here, so "no arm => no
  // request" holds even if a caller somehow bypassed the disabled attribute (a
  // stray Enter keypress, a test driving the handler directly). Re-keeping an
  // already-kept invoice is legal (a changed mind about the reason) -- there is no
  // "already kept" guard here, matching Store.KeepAsIs's own server-side contract.
  async function handleKeep() {
    if (inv == null || keeping || !canKeepAsIs(keepReason)) return
    setKeeping(true)
    setKeepError(null)
    try {
      await keepInvoiceAsIs(ctx.authedFetch, base, invoiceId, keepReason.trim())
      setKeepReason('')
      detail.run()
      onChanged()
    } catch (err) {
      setKeepError(err instanceof Error ? err.message : 'Something went wrong. Please try again.')
    } finally {
      setKeeping(false)
    }
  }

  if (detail.status === 'loading') {
    return (
      <div data-testid="review-row-expansion" style={{ padding: '16px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)' }}>
        <Loading label="Loading this invoice…" />
      </div>
    )
  }

  if (detail.status === 'error') {
    return (
      <div data-testid="review-row-expansion" style={{ padding: '16px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)' }}>
        {detail.error && <ErrorState error={detail.error} onRetry={detail.run} />}
      </div>
    )
  }

  // A row that exists in the list must exist as a detail record — this is a defensive
  // fallback, not a reachable UX state.
  if (inv == null) return null

  const view = rowExpansionView(inv, { can_revalidate: inv.can_revalidate, revalidate_blocked_reason: inv.revalidate_blocked_reason })
  const revalidateDisabled = view.revalidateDisabled || revalidating
  const hasEditableCard = view.cards.some((c) => c.field != null)
  // An UNSAVED, genuinely-changed draft (never a "field was focused" false positive --
  // this reuses fixEditPatch, the SAME diff Save itself sends) beside Re-validate: it
  // does NOT gate the button (AC-4 pins `disabled` to `!inv.can_revalidate` alone), it
  // only warns that clicking Re-validate now re-checks the invoice AS LAST SAVED, not
  // as currently typed -- product-advisor review, pre-push.
  const hasUnsavedEdit = Object.keys(fixEditPatch(inv, draft)).length > 0

  return (
    <div data-testid="review-row-expansion" style={{ padding: '16px 18px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-1)', display: 'flex', flexDirection: 'column', gap: 14 }}>
      {view.passing ? (
        <div
          data-testid="review-row-passing"
          style={{ padding: '12px 14px', borderRadius: 'var(--radius-md)', background: 'var(--status-green-bg)', border: '1px solid var(--status-green-border)', fontSize: 13, color: 'var(--status-green-text)' }}
        >
          {view.summary}
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          {/* AC-8/§10.12's trap: a warning-only invoice renders the ADVISORY label here,
              never "Failed rules" -- rowExpansionView already resolved which one, this
              never re-derives `blocking` itself. */}
          <div className="eyebrow">{view.sectionLabel}</div>
          {view.cards.map((card, i) => (
            <FixCardView
              key={`${card.ruleKey}-${card.field ?? 'unmapped'}-${i}`}
              card={card}
              value={card.field != null ? fieldValue(card.field) : ''}
              onChange={(v) => card.field != null && updateField(card.field, v)}
              disabled={saving}
            />
          ))}
          {saveError != null && (
            <div style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12, color: 'var(--status-red-text)' }}>
              {saveError}
            </div>
          )}
          {hasEditableCard && (
            <div>
              <button
                type="button"
                data-testid="review-fix-save"
                onClick={() => void handleSave()}
                disabled={saving}
                className="v2-btn v2-btn-primary pf-btn"
                style={{ height: 32, padding: '0 14px', fontSize: 13 }}
              >
                {saving ? ROW_EXPANSION_COPY.saving : ROW_EXPANSION_COPY.saveLabel}
              </button>
            </div>
          )}
        </div>
      )}

      <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-start', gap: 6, borderTop: view.passing ? undefined : '1px solid var(--line-2)', paddingTop: view.passing ? 0 : 4 }}>
        {/* The persisted reason, verbatim (INVCR-01-15, D6) — shown ABOVE the action
            row, before Keep as-is's own input, so the operator sees the existing
            triage decision before typing a new one. Amber, mirroring KEPT · INVALID's
            own tone (verdictPill, lib/reviewBatch.ts) rather than inventing a second
            colour for the same fact. */}
        {view.keptReason != null && (
          <div
            data-testid="review-kept-banner"
            style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-amber-bg)', border: '1px solid var(--status-amber-border)', fontSize: 12.5, color: 'var(--status-amber-text)', lineHeight: 1.5 }}
          >
            {ROW_EXPANSION_COPY.keptPrefix}
            {view.keptReason}
          </div>
        )}
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
          {/* Disabled-with-reason, following InvoiceDetail.tsx:308,:426-444's shipped
              pattern: the real `disabled` attribute, an inline mute (this codebase's
              `.v2-btn-ghost` has an unguarded `:hover` rule with no `:not(:disabled)`
              guard), the visible reason text below, and title/aria-describedby as
              additions to it, never the sole carrier. */}
          <button
            type="button"
            data-testid="review-revalidate"
            onClick={() => void handleRevalidate()}
            disabled={revalidateDisabled}
            title={view.revalidateReason ?? undefined}
            aria-describedby={view.revalidateReason != null ? REVALIDATE_REASON_ID : undefined}
            className="v2-btn v2-btn-ghost pf-btn"
            style={{
              height: 32,
              padding: '0 14px',
              fontSize: 13,
              ...(revalidateDisabled ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
            }}
          >
            {revalidating ? ROW_EXPANSION_COPY.revalidating : ROW_EXPANSION_COPY.revalidateLabel}
          </button>
          {/* Keep as-is (INVCR-01-15, D6): only offered while the row is actually
              blocking -- Store.KeepAsIs 409s a clean invoice (there is nothing to
              suppress), so this avoids a click that is guaranteed to 409. §7.3 puts
              "Keep as-is" beside Re-validate for a reason: it is only ever a
              fix-loop alternative, not a general-purpose action. */}
          {view.blocking && (
            <>
              <input
                type="text"
                data-testid="review-keep-reason"
                placeholder={ROW_EXPANSION_COPY.keepReasonPlaceholder}
                value={keepReason}
                onChange={(e) => setKeepReason(e.target.value)}
                disabled={keeping}
                className="pf-input"
                style={{ flex: '1 1 220px', minWidth: 160, height: 32, fontSize: 12.5 }}
              />
              <button
                type="button"
                data-testid="review-keep"
                onClick={() => void handleKeep()}
                disabled={keeping || !canKeepAsIs(keepReason)}
                className="v2-btn v2-btn-ghost pf-btn"
                style={{ height: 32, padding: '0 14px', fontSize: 13 }}
              >
                {keeping ? ROW_EXPANSION_COPY.keeping : ROW_EXPANSION_COPY.keepLabel}
              </button>
            </>
          )}
        </div>
        {/* The backend's copy, verbatim — never an SPA-authored fallback. */}
        {view.revalidateReason != null && (
          <div id={REVALIDATE_REASON_ID} data-testid="review-revalidate-reason" style={{ fontSize: 11.5, color: 'var(--fg-3)', lineHeight: 1.5 }}>
            {view.revalidateReason}
          </div>
        )}
        {keepError != null && (
          <div style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12, color: 'var(--status-red-text)' }}>
            {keepError}
          </div>
        )}
        {/* A WARNING, never a gate (product-advisor review, pre-push): AC-4 pins
            Re-validate's disabled state to `!inv.can_revalidate` alone, so an unsaved
            edit must not block the click — it only explains that the click re-checks
            the invoice as LAST SAVED, not as currently typed. Shown only while the
            button is actually clickable; a `can_revalidate:false` reason above already
            explains an unclickable one, and stacking two reasons would confuse rather
            than clarify. */}
        {hasUnsavedEdit && !revalidateDisabled && (
          <div data-testid="review-unsaved-hint" style={{ fontSize: 11.5, color: 'var(--status-amber-text)', lineHeight: 1.5 }}>
            {ROW_EXPANSION_COPY.unsavedHint}
          </div>
        )}
        {revalidateError != null && (
          <div style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12, color: 'var(--status-red-text)' }}>
            {revalidateError}
          </div>
        )}
        {/* §7.3's provenance/scope note (AC-7) — always rendered while expanded. */}
        <p data-testid="review-row-note" style={{ fontSize: 11.5, color: 'var(--fg-3)', margin: 0, lineHeight: 1.55 }}>
          {view.note}
        </p>
      </div>
    </div>
  )
}
