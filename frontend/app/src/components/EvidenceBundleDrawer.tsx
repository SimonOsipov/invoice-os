// AUDIT-08-04: the drawer shell + Form phase. Forked from MemberDrawer for structure/
// behaviour and RuleDrawer for the panel/band colour split (task-667 §2) -- FilterPopover
// and .pf-chip paint themselves --bg-2, so a --bg-2 panel here would be ground-on-ground,
// MemberDrawer's own documented defect in mirror image.
//
// AUDIT-08-05 adds the confirmation block, refusal rendering and the disabled Prepare button.
// AUDIT-08-06 adds the build phases (Building/Ready/Failed), the abort and the download toast.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { ErrorState, toApiError, useAsync, type ApiError } from '@invoice-os/api-client'

import { closeGlyph } from '../glyphs'
import { AUDIT_FILTER_DEFAULT, type AuditRange } from '../lib/auditFilters'
import {
  bundleRequestFor,
  fetchEvidenceBundle,
  getEvidenceBundlePreview,
  type BundleRequest,
  type EvidenceBundlePreview,
} from '../lib/evidenceBundle'
import {
  bundleBasisLine,
  bundleBlockFor,
  bundleBlockReason,
  bundleManifestLines,
  bundlePeriodLabel,
  bundleReadyLine,
  bundleToastCopy,
  EVIDENCE_COPY,
} from '../lib/evidenceBundleView'
import type { Entity } from '../lib/portfolio'
import { useDismiss } from '../lib/useDismiss'
import type { PlatformCtx } from '../types'

import { DATE_PRESETS } from './AuditFilterCard'
import { FilterPopover } from './FilterPopover'

// id === data-testid, the shipped shape at AuditView.tsx:244.
const REASON_ID = 'evidence-bundle-reason'
const HELPER_ID = 'evidence-prepare-helper'

export interface EvidenceBundleDrawerProps {
  ctx: PlatformCtx
  base: string
  onClose: () => void
  onToast: (t: { kind: 'success' | 'error'; text: string; testId?: string }) => void
}

// One capture per build. Holding (req, preview) in the phase -- not re-reading them at
// download time -- is what makes the frozen triple structural: Building, Ready, the toast and
// Try again all read the same pair the confirmation block was built from.
type BuildPhase =
  | { kind: 'form' }
  | { kind: 'building'; req: BundleRequest; preview: EvidenceBundlePreview }
  | { kind: 'ready'; req: BundleRequest; preview: EvidenceBundlePreview; blob: Blob; filename: string }
  | { kind: 'failed'; req: BundleRequest; preview: EvidenceBundlePreview; error: ApiError }

export function EvidenceBundleDrawer({ ctx, base, onClose, onToast }: EvidenceBundleDrawerProps) {
  const [companyOpen, setCompanyOpen] = useState(false)
  const openCompany = useCallback(() => setCompanyOpen(true), [])
  const closeCompany = useCallback(() => setCompanyOpen(false), [])
  // {id, name}, not just the id: ctx.entities comes from a useAsync whose data goes null on
  // refetch, so a name looked up by id later would blank mid-session.
  const [company, setCompany] = useState<{ id: string; name: string } | null>(null)
  const [range, setRange] = useState<AuditRange>(AUDIT_FILTER_DEFAULT.range)

  const [phase, setPhase] = useState<BuildPhase>({ kind: 'form' })

  // `!companyOpen`: FilterPopover has its own window keydown and neither listener stops
  // propagation, so one Escape would close both it and the drawer (EB-04-10). `phase.kind`:
  // the popover only mounts in the form phase, and companyOpen stays true after a pick, so a
  // later phase must not inherit that gate (EB-06-4b).
  useDismiss(phase.kind !== 'form' || !companyOpen, onClose)
  // A ref, not state: the controller is not render data, and the unmount cleanup below cannot
  // reach a value that lives only in the closure of the render that created it.
  const buildRef = useRef<AbortController | null>(null)
  // Escape, the scrim and the header X all unmount mid-build, and the bytes then have nowhere
  // to land. EB-06-4b is the oracle.
  useEffect(() => () => buildRef.current?.abort(), [])

  // Sorted by name (AC-5, D-08-09) -- ctx.entities includes archived rows already.
  const companies = useMemo(() => [...ctx.entities].sort((a, b) => a.name.localeCompare(b.name)), [ctx.entities])
  const pickCompany = (e: Entity) => setCompany({ id: e.id, name: e.name })

  // `now` is captured per SELECTION (inside the memo), never per render: re-deriving it on
  // every render would drift a relative preset's `from` and refetch forever
  // (AuditView.tsx:82-84).
  const req = useMemo(() => bundleRequestFor(company?.id ?? null, range, new Date()), [company, range])
  const reqKey = JSON.stringify(req)

  // `req.from <= req.to` too: an inverted range 400s server-side (evidenceBundleView.ts:92
  // draws the same line), and EB-05-14 pins that the client catches it before spending a
  // network call the reason already answers.
  const preview = useAsync<EvidenceBundlePreview>(
    () => (req ? getEvidenceBundlePreview(ctx.authedFetch, base, req) : Promise.reject(new Error('no request'))),
    { immediate: req != null && req.from <= req.to, deps: [reqKey] },
  )

  // The response is held WITH the request that produced it. useAsync keeps its last `data`
  // when a deps change does NOT re-run it (immediate:false for a null request), so the block
  // would otherwise outlive the selection it describes. EB-05-12, EB-05-13 are the oracles.
  const [landed, setLanded] = useState<{ key: string; res: EvidenceBundlePreview } | null>(null)
  useEffect(() => {
    if (preview.data == null) return
    setLanded({ key: reqKey, res: preview.data })
    // `reqKey` is read, not tracked: useAsync only dispatches for the latest run
    // (async-state.ts:96,101), so preview.data is always the current key's response.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [preview.data])
  const shown = landed?.key === reqKey ? landed.res : null

  // `shown`, never preview.data: a block computed from a response whose request has been
  // abandoned is the exact failure AC-8b names (EB-05-12, EB-05-13).
  const block = bundleBlockFor(company?.id ?? null, req, shown)
  const reason = bundleBlockReason(block)
  const canPrepare = shown != null && block == null
  const describedBy =
    [reason != null ? REASON_ID : null, shown != null ? HELPER_ID : null].filter(Boolean).join(' ') || undefined

  // The pair travels as arguments, never re-read off the closure after the await: runBuild
  // resumes in a render that may already be gone, and Try again resumes later still.
  async function runBuild(r: BundleRequest, p: EvidenceBundlePreview) {
    buildRef.current?.abort() // a second build supersedes the first
    const ctrl = new AbortController()
    buildRef.current = ctrl
    setPhase({ kind: 'building', req: r, preview: p })
    try {
      const res = await fetchEvidenceBundle(ctx.getToken, base, r, p.filename, ctrl.signal)
      // The controller this build owns is the only discriminator that holds in both runtimes:
      // a stubbed fetch ignores `signal` and resolves anyway. EB-06-4's release rung is the
      // oracle for these two lines; every other rung passes without them.
      if (ctrl.signal.aborted) return
      setPhase({ kind: 'ready', req: r, preview: p, blob: res.blob, filename: res.filename })
    } catch (err) {
      if (ctrl.signal.aborted) return
      // toApiError, never a cast: an abort surfaces as a raw DOMException and an offline
      // failure as a raw TypeError, neither of which carries .status.
      setPhase({ kind: 'failed', req: r, preview: p, error: toApiError(err) })
    }
  }

  // req/shown are re-checked for narrowing only -- canPrepare already implies both.
  const startBuild = () => {
    if (!canPrepare || req == null || shown == null) return
    void runBuild(req, shown)
  }
  const cancelBuild = () => {
    buildRef.current?.abort()
    setPhase({ kind: 'form' })
  }
  const retryBuild = () => {
    if (phase.kind !== 'failed') return
    void runBuild(phase.req, phase.preview)
  }
  const onDownload = () => {
    if (phase.kind !== 'ready') return
    // AuditView.tsx:53-58 / ReviewUnreadableTab.tsx:48-53 minus the Blob construction -- these
    // bytes came off the wire. EB-06-7 pins one create, one click on an anchor pointing at
    // that URL, and one revoke of it; `a.href` would satisfy it too, so the local const is a
    // preference, not a tested claim.
    const url = URL.createObjectURL(phase.blob)
    const a = document.createElement('a')
    a.href = url
    a.download = phase.filename
    a.click()
    URL.revokeObjectURL(url)
    onToast({
      kind: 'success',
      text: bundleToastCopy({
        filename: phase.filename,
        invoices: phase.preview.counts.invoices,
        bytes: phase.blob.size,
        company: phase.preview.entity.name,
        period: bundlePeriodLabel(phase.preview.period),
      }),
      testId: 'evidence-bundle-toast',
    })
  }

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
          {phase.kind === 'form' ? (
            <>
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

              {shown != null && (
                <div
                  data-testid="evidence-confirm-block"
                  style={{
                    marginTop: 20,
                    background: 'var(--action-tint)',
                    border: '1px solid var(--teal-200)',
                    borderRadius: 'var(--radius-md)',
                    padding: '11px 14px',
                    display: 'flex',
                    flexDirection: 'column',
                    gap: 9,
                  }}
                >
                  <div className="label" data-testid="evidence-confirm-heading" style={{ color: 'var(--action)' }}>
                    {EVIDENCE_COPY.confirmHeading}
                  </div>

                  <div>
                    <div
                      data-testid="evidence-confirm-company"
                      style={{ fontSize: 15, fontWeight: 600, color: 'var(--fg-1)', wordBreak: 'break-word' }}
                    >
                      {shown.entity.name}
                    </div>
                    <div data-testid="evidence-confirm-period" style={{ marginTop: 2, fontSize: 12.5, color: 'var(--fg-2)' }}>
                      {bundlePeriodLabel(shown.period)}
                    </div>
                    {/* The server's basis, never a hardcoded claim (D-08-11). */}
                    <div
                      data-testid="evidence-confirm-basis"
                      style={{ marginTop: 4, fontSize: 11.5, lineHeight: 1.55, color: 'var(--fg-2)' }}
                    >
                      {bundleBasisLine(shown.period)}
                    </div>
                  </div>

                  <div style={{ borderTop: '1px solid var(--teal-200)', paddingTop: 9 }}>
                    <div className="label" data-testid="evidence-confirm-contents-heading" style={{ color: 'var(--action)' }}>
                      {EVIDENCE_COPY.contentsHeading}
                    </div>
                    <div
                      data-testid="evidence-confirm-contents"
                      style={{ marginTop: 6, display: 'flex', flexDirection: 'column', gap: 5 }}
                    >
                      {bundleManifestLines(shown).map((line) => (
                        <div
                          key={line.label}
                          data-testid="evidence-confirm-row"
                          style={{ display: 'flex', alignItems: 'baseline', gap: 10, fontSize: 11.5, lineHeight: 1.5 }}
                        >
                          <span data-testid="evidence-confirm-row-label" style={{ flex: 1, minWidth: 0, color: 'var(--fg-2)' }}>
                            {line.label}
                          </span>
                          {line.value != null && (
                            <span
                              data-testid="evidence-confirm-row-value"
                              className="mono"
                              style={{ flex: 'none', fontWeight: 600, color: 'var(--fg-1)' }}
                            >
                              {line.value}
                            </span>
                          )}
                        </div>
                      ))}
                    </div>
                  </div>

                  <div style={{ borderTop: '1px solid var(--teal-200)', paddingTop: 9 }}>
                    <div className="label" data-testid="evidence-confirm-filename-label">
                      {EVIDENCE_COPY.filenameLabel}
                    </div>
                    {/* break-all, not ellipsis: the whole name is the claim (AC-2). ReviewBatch.tsx:500. */}
                    <div
                      data-testid="evidence-confirm-filename"
                      className="mono"
                      style={{ marginTop: 4, fontSize: 12, color: 'var(--fg-1)', wordBreak: 'break-all' }}
                    >
                      {shown.filename}
                    </div>
                  </div>

                  <div data-testid="evidence-confirm-footnote" style={{ fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-2)' }}>
                    {EVIDENCE_COPY.confirmFooter}
                  </div>
                </div>
              )}

              {/* AuditView.tsx:285's shape. Suppressed when a refusal already speaks (§4). */}
              {block == null && shown == null && preview.error != null && (
                <div data-testid="evidence-bundle-error" style={{ marginTop: 16 }}>
                  <ErrorState error={preview.error} onRetry={preview.run} />
                </div>
              )}

              {/* Visible text, never a title=: a title on a DISABLED button is invisible in Chromium
                  (AUDIT-08's own [inved-02-scope] lesson, and APPR-16's two missed QA passes). */}
              {reason != null && (
                <div
                  id={REASON_ID}
                  data-testid={REASON_ID}
                  style={{ marginTop: 12, fontSize: 12, lineHeight: 1.5, color: 'var(--fg-2)' }}
                >
                  {reason}
                </div>
              )}

              {shown != null && (
                <div
                  id={HELPER_ID}
                  data-testid={HELPER_ID}
                  style={{ marginTop: 12, fontSize: 11.5, lineHeight: 1.45, color: 'var(--fg-3)' }}
                >
                  {EVIDENCE_COPY.prepareHelper}
                </div>
              )}
            </>
          ) : phase.kind === 'building' ? (
            <div data-testid="evidence-building">
              <div data-testid="evidence-building-title" style={{ fontSize: 15, fontWeight: 600, color: 'var(--fg-1)' }}>
                {EVIDENCE_COPY.buildingTitle}
              </div>
              {/* Indeterminate by construction (ImportProgress.tsx:101-119): a repeating gradient
                  twice its box's width, slid end to end. It encodes no position, so no number
                  can be wrong. A full-width block here, not that row's 28px inline pill. */}
              <div
                data-testid="evidence-building-bar"
                style={{
                  marginTop: 14,
                  height: 6,
                  borderRadius: 99,
                  overflow: 'hidden',
                  background: 'repeating-linear-gradient(115deg, var(--action) 0 6px, var(--action-tint) 6px 13px)',
                  backgroundSize: '200% 100%',
                  animation: 'shimmer 1.15s linear infinite',
                }}
              />
              <div
                data-testid="evidence-building-note"
                style={{ marginTop: 14, fontSize: 12.5, lineHeight: 1.55, color: 'var(--fg-2)' }}
              >
                {EVIDENCE_COPY.buildingNote}
              </div>
            </div>
          ) : phase.kind === 'ready' ? (
            <div data-testid="evidence-ready">
              <div data-testid="evidence-ready-title" style={{ fontSize: 15, fontWeight: 600, color: 'var(--fg-1)' }}>
                {EVIDENCE_COPY.readyTitle}
              </div>
              {/* The response's name, never the preview's: this is what lands in the downloads
                  folder. break-all, not ellipsis -- the whole name is the claim (EB-06-7b). */}
              <div
                data-testid="evidence-ready-filename"
                className="mono"
                style={{ marginTop: 10, fontSize: 12, color: 'var(--fg-1)', wordBreak: 'break-all' }}
              >
                {phase.filename}
              </div>
              <div
                data-testid="evidence-ready-line"
                className="mono"
                style={{ marginTop: 6, fontSize: 11, color: 'var(--fg-3)', letterSpacing: '0.04em' }}
              >
                {bundleReadyLine(phase.blob.size)}
              </div>
            </div>
          ) : (
            /* No onRetry: ErrorState's own button reads the literal 'Retry', which would sit
               next to a footer button reading Try again for the same action (st06-plan F7). */
            <div data-testid="evidence-bundle-failure">
              <ErrorState error={phase.error} />
            </div>
          )}
        </div>

        <div
          data-testid="evidence-bundle-footer"
          style={{ flex: 'none', padding: '14px 22px', borderTop: '1px solid var(--line-1)', background: 'var(--bg-2)', display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 10 }}
        >
          {phase.kind === 'form' ? (
            <>
              <button
                type="button"
                data-testid="evidence-bundle-prepare"
                disabled={!canPrepare}
                onClick={startBuild}
                aria-describedby={describedBy}
                className="v2-btn v2-btn-primary pf-btn"
                style={{
                  height: 36,
                  fontSize: 13,
                  // Spread ONLY when disabled; `filter:'none'` neutralises .v2-btn-primary:hover's
                  // unguarded brightness(1.22) (app-layer.css:213). InvoiceDetail.tsx:926.
                  ...(canPrepare ? null : { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed', filter: 'none' }),
                }}
              >
                {EVIDENCE_COPY.prepareLabel}
              </button>
              <button
                type="button"
                data-testid="evidence-bundle-cancel"
                onClick={onClose}
                className="v2-btn v2-btn-ghost pf-btn"
                style={{ height: 36, fontSize: 13 }}
              >
                {EVIDENCE_COPY.cancelLabel}
              </button>
            </>
          ) : phase.kind === 'building' ? (
            <button
              type="button"
              data-testid="evidence-building-cancel"
              onClick={cancelBuild}
              className="v2-btn v2-btn-ghost pf-btn"
              style={{ height: 36, fontSize: 13 }}
            >
              {EVIDENCE_COPY.cancelLabel}
            </button>
          ) : phase.kind === 'ready' ? (
            <>
              <button
                type="button"
                data-testid="evidence-ready-download"
                onClick={onDownload}
                className="v2-btn v2-btn-primary pf-btn"
                style={{ height: 36, fontSize: 13 }}
              >
                {EVIDENCE_COPY.downloadLabel}
              </button>
              <button
                type="button"
                data-testid="evidence-ready-start-another"
                onClick={() => setPhase({ kind: 'form' })}
                className="v2-btn v2-btn-ghost pf-btn"
                style={{ height: 36, fontSize: 13 }}
              >
                {EVIDENCE_COPY.startAnotherLabel}
              </button>
            </>
          ) : (
            <>
              <button
                type="button"
                data-testid="evidence-failed-retry"
                onClick={retryBuild}
                className="v2-btn v2-btn-primary pf-btn"
                style={{ height: 36, fontSize: 13 }}
              >
                {EVIDENCE_COPY.retryLabel}
              </button>
              <button
                type="button"
                data-testid="evidence-failed-cancel"
                onClick={onClose}
                className="v2-btn v2-btn-ghost pf-btn"
                style={{ height: 36, fontSize: 13 }}
              >
                {EVIDENCE_COPY.cancelLabel}
              </button>
            </>
          )}
        </div>
      </div>
    </>
  )
}
