// AUDIT-08-04: the drawer shell + Form phase. Forked from MemberDrawer for structure/
// behaviour and RuleDrawer for the panel/band colour split (task-667 §2) -- FilterPopover
// and .pf-chip paint themselves --bg-2, so a --bg-2 panel here would be ground-on-ground,
// MemberDrawer's own documented defect in mirror image.
//
// Deliberately NOT built here (task-667 §8, owned by 05/06): confirmation block, counts,
// Prepare button, refusal rendering, `phase` state, abort, toast. `onToast` is threaded for
// 06 and stays undestructured -- there is nothing to hand it yet.

import { useCallback, useMemo, useState } from 'react'

import { useAsync } from '@invoice-os/api-client'

import { closeGlyph } from '../glyphs'
import { AUDIT_FILTER_DEFAULT, type AuditRange } from '../lib/auditFilters'
import { bundleRequestFor, getEvidenceBundlePreview, type EvidenceBundlePreview } from '../lib/evidenceBundle'
import { EVIDENCE_COPY } from '../lib/evidenceBundleView'
import type { Entity } from '../lib/portfolio'
import { useDismiss } from '../lib/useDismiss'
import type { PlatformCtx } from '../types'

import { DATE_PRESETS } from './AuditFilterCard'
import { FilterPopover } from './FilterPopover'

export interface EvidenceBundleDrawerProps {
  ctx: PlatformCtx
  base: string
  onClose: () => void
  onToast: (t: { kind: 'success' | 'error'; text: string }) => void
}

export function EvidenceBundleDrawer({ ctx, base, onClose }: EvidenceBundleDrawerProps) {
  const [companyOpen, setCompanyOpen] = useState(false)
  const openCompany = useCallback(() => setCompanyOpen(true), [])
  const closeCompany = useCallback(() => setCompanyOpen(false), [])
  // Gated on `!companyOpen`, not `true`: FilterPopover registers its own window keydown
  // while its panel is open, and neither listener stops propagation, so an ungated hook here
  // would let one Escape close the popover AND the drawer (EB-04-10).
  useDismiss(!companyOpen, onClose)

  // {id, name}, not just the id: ctx.entities comes from a useAsync whose data goes null on
  // refetch, so a name looked up by id later would blank mid-session.
  const [company, setCompany] = useState<{ id: string; name: string } | null>(null)
  const [range, setRange] = useState<AuditRange>(AUDIT_FILTER_DEFAULT.range)

  // Sorted by name (AC-5, D-08-09) -- ctx.entities includes archived rows already.
  const companies = useMemo(() => [...ctx.entities].sort((a, b) => a.name.localeCompare(b.name)), [ctx.entities])
  const pickCompany = (e: Entity) => setCompany({ id: e.id, name: e.name })

  // `now` is captured per SELECTION (inside the memo), never per render: re-deriving it on
  // every render would drift a relative preset's `from` and refetch forever
  // (AuditView.tsx:82-84).
  const req = useMemo(() => bundleRequestFor(company?.id ?? null, range, new Date()), [company, range])
  const reqKey = JSON.stringify(req)

  // Fired for its call-count and argument shape alone (EB-04-7, EB-04-12). The landed/shown
  // staleness guard in task-667 §6 lands with subtask 05's confirm block, its only reader.
  useAsync<EvidenceBundlePreview>(
    () => (req ? getEvidenceBundlePreview(ctx.authedFetch, base, req) : Promise.reject(new Error('no request'))),
    { immediate: req != null, deps: [reqKey] },
  )

  return (
    <>
      <div
        data-testid="evidence-bundle-scrim"
        onClick={onClose}
        style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'oklch(20% .02 210 / 0.32)', animation: 'pfFade 160ms ease-out' }}
      />
      <div
        className="pf-drawer"
        role="dialog"
        aria-modal="true"
        aria-label={EVIDENCE_COPY.drawerTitle}
        data-testid="evidence-bundle-drawer"
        style={{
          position: 'fixed',
          top: 0,
          right: 0,
          bottom: 0,
          zIndex: 81,
          width: 560,
          maxWidth: '94vw',
          background: 'var(--bg-1)',
          borderLeft: '1px solid var(--line-2)',
          boxShadow: '-24px 0 48px -24px oklch(20% .02 210 / 0.3)',
          display: 'flex',
          flexDirection: 'column',
          animation: 'pfDrawer 200ms ease-out',
        }}
      >
        <div style={{ flex: 'none', padding: '18px 22px', borderBottom: '1px solid var(--line-1)', background: 'var(--bg-2)', display: 'flex', alignItems: 'flex-start', gap: 12 }}>
          <div style={{ flex: 1, minWidth: 0 }}>
            <div data-testid="evidence-bundle-title" style={{ fontSize: 15, fontWeight: 600, color: 'var(--fg-1)' }}>
              {EVIDENCE_COPY.drawerTitle}
            </div>
            <div data-testid="evidence-bundle-subtitle" style={{ marginTop: 3, fontSize: 12.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>
              {EVIDENCE_COPY.drawerSubtitle}
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="pf-btn"
            aria-label="Close"
            data-testid="evidence-bundle-close"
            style={{ flex: 'none', width: 30, height: 30, border: 0, background: 'var(--bg-3)', color: 'var(--fg-2)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}
          >
            {closeGlyph}
          </button>
        </div>

        <div data-testid="evidence-bundle-body" style={{ flex: 1, overflowY: 'auto', padding: '20px 22px' }}>
          <FilterPopover
            testId="evidence-company"
            label={EVIDENCE_COPY.companyLabel}
            summary={company?.name ?? EVIDENCE_COPY.companyPlaceholder}
            open={companyOpen}
            onOpen={openCompany}
            onClose={closeCompany}
          >
            <div style={{ width: 260, maxHeight: 380, overflowY: 'auto', padding: 6 }}>
              {companies.map((e) => (
                <button
                  key={e.id}
                  type="button"
                  data-testid={`evidence-company-row-${e.id}`}
                  aria-pressed={e.id === company?.id}
                  onClick={() => pickCompany(e)}
                  className="pf-menu-item"
                  style={{
                    display: 'block',
                    width: '100%',
                    textAlign: 'left',
                    border: 0,
                    background: e.id === company?.id ? 'var(--bg-3)' : 'transparent',
                    padding: '9px 12px',
                    fontFamily: 'var(--font-sans)',
                    fontSize: 13,
                    color: e.id === company?.id ? 'var(--action)' : 'var(--fg-1)',
                    cursor: 'pointer',
                  }}
                >
                  <span data-testid={`evidence-company-label-${e.id}`}>{e.name}</span>
                </button>
              ))}
            </div>
          </FilterPopover>
          <div data-testid="evidence-company-helper" style={{ marginTop: 8, fontSize: 11.5, lineHeight: 1.45, color: 'var(--fg-3)' }}>
            {EVIDENCE_COPY.companyHelper}
          </div>

          <div className="label" style={{ margin: '20px 0 6px' }}>
            {EVIDENCE_COPY.periodLabel}
          </div>
          <div data-testid="evidence-period-chips" style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
            {DATE_PRESETS.map(({ id, label }) => (
              <button
                key={id}
                type="button"
                data-testid={`evidence-period-${id}`}
                aria-pressed={range.preset === id}
                onClick={() => setRange({ preset: id })}
                className="pf-chip"
                style={{
                  height: 30,
                  padding: '0 12px',
                  // No borderRadius: .pf-chip forces --radius-pill with !important (app-layer.css:275).
                  fontFamily: 'var(--font-sans)',
                  fontSize: 12.5,
                  fontWeight: 500,
                  border: `1px solid ${range.preset === id ? 'var(--action)' : 'var(--line-2)'}`,
                  background: range.preset === id ? 'var(--action)' : 'var(--bg-2)',
                  color: range.preset === id ? 'var(--text-on-dark)' : 'var(--fg-2)',
                }}
              >
                {label}
              </button>
            ))}
          </div>
          {/* No Apply button -- Custom commits immediately. bundleRequestFor returns null
              until both dates are set, so nothing fires per keystroke (task-667 §4). */}
          {range.preset === 'custom' && (
            <div data-testid="evidence-period-custom-fields" style={{ marginTop: 10, display: 'flex', flexDirection: 'column', gap: 8 }}>
              <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 11.5, color: 'var(--fg-3)' }}>
                From
                <input
                  type="date"
                  data-testid="evidence-period-from"
                  className="pf-input"
                  value={range.from ?? ''}
                  onChange={(e) => setRange({ ...range, from: e.target.value })}
                />
              </label>
              <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 11.5, color: 'var(--fg-3)' }}>
                To
                <input
                  type="date"
                  data-testid="evidence-period-to"
                  className="pf-input"
                  value={range.to ?? ''}
                  onChange={(e) => setRange({ ...range, to: e.target.value })}
                />
              </label>
            </div>
          )}
        </div>

        <div
          data-testid="evidence-bundle-footer"
          style={{ flex: 'none', padding: '14px 22px', borderTop: '1px solid var(--line-1)', background: 'var(--bg-2)', display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 10 }}
        >
          <button
            type="button"
            data-testid="evidence-bundle-cancel"
            onClick={onClose}
            className="v2-btn v2-btn-ghost pf-btn"
            style={{ height: 36, fontSize: 13 }}
          >
            {EVIDENCE_COPY.cancelLabel}
          </button>
        </div>
      </div>
    </>
  )
}
