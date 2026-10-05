// AUDIT-08-04: the drawer shell + Form phase. Forked from MemberDrawer for structure/
// behaviour and RuleDrawer for the panel colour.
//
// AUDIT-08-05 adds the confirmation block, refusal rendering and the disabled Prepare button.
// AUDIT-08-06 adds the build phases (Building/Ready/Failed), the abort and the download toast.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { ErrorState, toApiError, useAsync, type ApiError } from '@invoice-os/api-client'

import { dismissGlyph, downloadGlyph } from '../glyphs'
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
  onToast: (t: { kind: 'success' | 'error'; text: string; testId?: string; maxWidth?: number }) => void
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
  // propagation, so one Escape would close both it and the drawer. `phase.kind`: the popover
  // only mounts in the form phase, so a later phase must not inherit that gate.
  useDismiss(phase.kind !== 'form' || !companyOpen, onClose)
  // A ref, not state: the controller is not render data, and the unmount cleanup below cannot
  // reach a value that lives only in the closure of the render that created it.
  const buildRef = useRef<AbortController | null>(null)
  // Escape, the scrim and the header X all unmount mid-build, and the bytes then have nowhere
  // to land. EB-06-4b is the oracle.
  useEffect(() => () => buildRef.current?.abort(), [])

  // Sorted by name (AC-5, D-08-09) -- ctx.entities includes archived rows already.
  const companies = useMemo(() => [...ctx.entities].sort((a, b) => a.name.localeCompare(b.name)), [ctx.entities])
  const pickCompany = (e: Entity) => {
    setCompany({ id: e.id, name: e.name })
    setCompanyOpen(false)
  }

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
      // Clear of the open drawer panel.
      maxWidth: 440,
    })
  }

  return (
    <>
      <div
        data-testid="evidence-bundle-scrim"
        onClick={onClose}
        style={{
          position: 'fixed',
          inset: 0,
          zIndex: 80,
          background: 'color-mix(in srgb, var(--surface) 55%, transparent)',
          backdropFilter: 'blur(6px)',
          WebkitBackdropFilter: 'blur(6px)',
          animation: 'pfFade 160ms ease-out',
        }}
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
          display: 'flex',
          flexDirection: 'column',
          animation: 'pfDrawer 200ms ease-out',
        }}
      >
        <div style={{ flex: 'none', padding: '20px 24px 16px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'flex-start', gap: 14 }}>
          <div style={{ flex: 1, minWidth: 0 }}>
            <div data-testid="evidence-bundle-title" style={{ marginBottom: 4, fontSize: 19, fontWeight: 700, letterSpacing: '-0.02em', color: 'var(--fg-1)' }}>
              {EVIDENCE_COPY.drawerTitle}
            </div>
            <div data-testid="evidence-bundle-subtitle" style={{ fontSize: 13, lineHeight: 1.5, color: 'var(--fg-3)' }}>
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
            {dismissGlyph}
          </button>
        </div>

        <div data-testid="evidence-bundle-body" style={{ flex: 1, minHeight: 0, overflowY: 'auto', padding: '20px 24px 28px' }}>
          {phase.kind === 'form' ? (
            <>
              <FilterPopover
                testId="evidence-company"
                label={EVIDENCE_COPY.companyLabel}
                summary={company ? <span style={{ color: 'var(--fg-1)' }}>{company.name}</span> : EVIDENCE_COPY.companyPlaceholder}
                open={companyOpen}
                onOpen={openCompany}
                onClose={closeCompany}
                block
              >
                <div style={{ maxHeight: 380, overflowY: 'auto', padding: '4px 0' }}>
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
                        fontWeight: e.id === company?.id ? 600 : 500,
                        color: 'var(--fg-1)',
                        cursor: 'pointer',
                      }}
                    >
                      <span data-testid={`evidence-company-label-${e.id}`}>{e.name}</span>
                    </button>
                  ))}
                </div>
              </FilterPopover>
              <div data-testid="evidence-company-helper" style={{ marginTop: 8, fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-3)' }}>
                {EVIDENCE_COPY.companyHelper}
              </div>

              <div className="label" style={{ margin: '20px 0 7px' }}>
                {EVIDENCE_COPY.periodLabel}
              </div>
              <div data-testid="evidence-period-chips" style={{ display: 'flex', flexWrap: 'wrap', gap: 7 }}>
                {DATE_PRESETS.map(({ id, label }) => (
                  <button
                    key={id}
                    type="button"
                    data-testid={`evidence-period-${id}`}
                    aria-pressed={range.preset === id}
                    onClick={() => setRange({ preset: id })}
                    className="pf-btn"
                    style={{
                      height: 32,
                      padding: '0 13px',
                      fontFamily: 'var(--font-sans)',
                      fontSize: 12.5,
                      fontWeight: 500,
                      cursor: 'pointer',
                      border: `1px solid ${range.preset === id ? 'var(--action)' : 'var(--line-2)'}`,
                      background: range.preset === id ? 'var(--action)' : 'transparent',
                      color: range.preset === id ? 'var(--primary-foreground)' : 'var(--fg-2)',
                    }}
                  >
                    {label}
                  </button>
                ))}
              </div>
              {/* No Apply button -- Custom commits immediately. bundleRequestFor returns null
                  until both dates are set, so nothing fires per keystroke (task-667 §4). */}
              {range.preset === 'custom' && (
                <div data-testid="evidence-period-custom-fields" style={{ marginTop: 10, display: 'flex', gap: 10 }}>
                  <label style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 4 }}>
                    <span className="label">From</span>
                    <input
                      type="date"
                      data-testid="evidence-period-from"
                      className="pf-input"
                      style={{ height: 34, fontSize: 12.5 }}
                      value={range.from ?? ''}
                      onChange={(e) => setRange({ ...range, from: e.target.value })}
                    />
                  </label>
                  <label style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 4 }}>
                    <span className="label">To</span>
                    <input
                      type="date"
                      data-testid="evidence-period-to"
                      className="pf-input"
                      style={{ height: 34, fontSize: 12.5 }}
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
                    marginTop: 22,
                    background: 'var(--bg-2)',
                    border: '1px solid var(--line-2)',
                    borderRadius: 'var(--radius-md)',
                    padding: '17px 18px',
                  }}
                >
                  <div
                    className="mono"
                    data-testid="evidence-confirm-heading"
                    style={{ marginBottom: 10, fontSize: 9, fontWeight: 700, letterSpacing: '0.1em', color: 'var(--action)' }}
                  >
                    {EVIDENCE_COPY.confirmHeading}
                  </div>

                  <div style={{ marginBottom: 14 }}>
                    <div
                      data-testid="evidence-confirm-company"
                      style={{ marginBottom: 3, fontSize: 17, fontWeight: 700, letterSpacing: '-0.02em', color: 'var(--fg-1)', wordBreak: 'break-word' }}
                    >
                      {shown.entity.name}
                    </div>
                    <div data-testid="evidence-confirm-period" style={{ marginBottom: 8, fontSize: 13.5, color: 'var(--fg-2)' }}>
                      {bundlePeriodLabel(shown.period)}
                    </div>
                    {/* The server's basis, never a hardcoded claim (D-08-11). */}
                    <div data-testid="evidence-confirm-basis" style={{ fontSize: 12, lineHeight: 1.5, color: 'var(--fg-3)' }}>
                      {bundleBasisLine(shown.period)}
                    </div>
                  </div>

                  <div style={{ borderTop: '1px solid var(--line-1)', paddingTop: 13 }}>
                    <div
                      className="mono"
                      data-testid="evidence-confirm-contents-heading"
                      style={{ marginBottom: 8, fontSize: 9, fontWeight: 700, letterSpacing: '0.1em', color: 'var(--fg-3)' }}
                    >
                      {EVIDENCE_COPY.contentsHeading}
                    </div>
                    <div data-testid="evidence-confirm-contents" style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                      {bundleManifestLines(shown).map((line) => (
                        <div
                          key={line.label}
                          data-testid="evidence-confirm-row"
                          style={{ display: 'flex', alignItems: 'baseline', gap: 12 }}
                        >
                          <span
                            data-testid="evidence-confirm-row-label"
                            style={{ flex: 1, minWidth: 0, fontSize: 12, lineHeight: 1.45, color: 'var(--fg-1)' }}
                          >
                            {line.label}
                          </span>
                          {line.value != null && (
                            <span
                              data-testid="evidence-confirm-row-value"
                              className="mono"
                              style={{ flex: 'none', fontSize: 11, color: 'var(--fg-2)' }}
                            >
                              {line.value}
                            </span>
                          )}
                        </div>
                      ))}
                    </div>
                  </div>

                  <div style={{ marginTop: 14, paddingTop: 12, borderTop: '1px solid var(--line-1)' }}>
                    <div className="label" data-testid="evidence-confirm-filename-label" style={{ marginBottom: 4 }}>
                      {EVIDENCE_COPY.filenameLabel}
                    </div>
                    {/* break-all, not ellipsis: the whole name is the claim (AC-2). */}
                    <div
                      data-testid="evidence-confirm-filename"
                      className="mono"
                      style={{ fontSize: 11, fontWeight: 600, color: 'var(--fg-1)', wordBreak: 'break-all' }}
                    >
                      {shown.filename}
                    </div>
                  </div>

                  <div data-testid="evidence-confirm-footnote" style={{ marginTop: 12, fontSize: 11.5, color: 'var(--fg-3)' }}>
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
                  style={{ marginTop: 12, fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg-3)' }}
                >
                  {EVIDENCE_COPY.prepareHelper}
                </div>
              )}
            </>
          ) : phase.kind === 'building' ? (
            <div
              data-testid="evidence-building"
              style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: 22 }}
            >
              <div data-testid="evidence-building-title" style={{ marginBottom: 12, fontSize: 15, fontWeight: 600, color: 'var(--fg-1)' }}>
                {EVIDENCE_COPY.buildingTitle}
              </div>
              {/* Indeterminate: one childless fill that pulses, so it encodes no position. */}
              <div style={{ height: 6, borderRadius: 'var(--radius-sm)', background: 'var(--bg-3)', overflow: 'hidden', marginBottom: 12 }}>
                <div
                  data-testid="evidence-building-bar"
                  style={{ height: '100%', width: '100%', background: 'var(--action)', animation: 'pulse 1.2s linear infinite' }}
                />
              </div>
              <div data-testid="evidence-building-note" style={{ fontSize: 12.5, lineHeight: 1.5, color: 'var(--fg-2)' }}>
                {EVIDENCE_COPY.buildingNote}
              </div>
            </div>
          ) : phase.kind === 'ready' ? (
            <div
              data-testid="evidence-ready"
              style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: 16 }}
            >
              <div data-testid="evidence-ready-title" style={{ fontSize: 15, fontWeight: 600, color: 'var(--fg-1)' }}>
                {EVIDENCE_COPY.readyTitle}
              </div>
              {/* The response's name, never the preview's: this is what lands in the downloads
                  folder. break-all, not ellipsis -- the whole name is the claim (EB-06-7b). */}
              <div
                data-testid="evidence-ready-filename"
                className="mono"
                style={{ marginTop: 10, marginBottom: 6, fontSize: 11.5, fontWeight: 600, color: 'var(--fg-1)', wordBreak: 'break-all' }}
              >
                {phase.filename}
              </div>
              <div
                data-testid="evidence-ready-line"
                className="mono"
                style={{ fontSize: 9.5, color: 'var(--fg-3)', letterSpacing: '0.06em' }}
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
          style={{ flex: 'none', padding: '14px 24px', borderTop: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'flex-start', gap: 10 }}
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
                  height: 38,
                  // `filter: 'none'` neutralises .v2-btn-primary:hover's brightness(1.22).
                  ...(canPrepare ? null : { opacity: 0.45, cursor: 'not-allowed', filter: 'none' }),
                }}
              >
                {EVIDENCE_COPY.prepareLabel}
              </button>
              <button
                type="button"
                data-testid="evidence-bundle-cancel"
                onClick={onClose}
                className="v2-btn v2-btn-ghost pf-btn"
                style={{ height: 38 }}
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
              style={{ height: 38 }}
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
                style={{ height: 38, gap: 8 }}
              >
                <span style={{ display: 'inline-flex' }}>{downloadGlyph}</span>
                {EVIDENCE_COPY.downloadLabel}
              </button>
              <button
                type="button"
                data-testid="evidence-ready-start-another"
                onClick={() => setPhase({ kind: 'form' })}
                className="v2-btn v2-btn-ghost pf-btn"
                style={{ height: 38 }}
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
                style={{ height: 38 }}
              >
                {EVIDENCE_COPY.retryLabel}
              </button>
              <button
                type="button"
                data-testid="evidence-failed-cancel"
                onClick={onClose}
                className="v2-btn v2-btn-ghost pf-btn"
                style={{ height: 38 }}
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
