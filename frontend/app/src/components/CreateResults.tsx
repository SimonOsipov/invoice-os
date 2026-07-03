// Create flow · step "results" — verdict banner, issues-to-resolve (one-click fixes),
// passed checks, and the sticky invoice/approve panel. Ported from Platform.dc.html
// ~L549-594 + the verdict/issues/approveBtn slices of renderVals() (~L1356-1364).

import { amount, fmt } from '../lib/format'
import { crossGlyph, shieldGlyph, tickGlyph11, tickGlyph13, warnGlyph } from '../glyphs'
import { Icon } from '../icons'
import type { PlatformCtx, ValidationResult } from '../types'

const WHT_RE = /servic|consult|support|warehous|leasing/i

const lockGlyph = <Icon paths={['M19 11H5a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7a2 2 0 0 0-2-2Z', 'M7 11V7a5 5 0 0 1 10 0v4']} size={15} />

export function CreateResults({ ctx }: { ctx: PlatformCtx }) {
  const { draft } = ctx
  const v: ValidationResult = ctx.validation || { errors: [], warnings: [], passed: [] }

  const sub = amount(draft.items)
  const vat = sub * 0.075
  const whtServices = draft.items.filter((it) => WHT_RE.test(it.desc)).reduce((s, it) => s + it.qty * it.price, 0)
  const whtAmt = draft.wht ? whtServices * 0.05 : 0
  const total = sub + vat - whtAmt

  const passCount = v.passed.length
  const hasErr = v.errors.length > 0
  const hasWarn = v.warnings.length > 0
  const clear = !hasErr && !hasWarn

  const issues = [
    ...v.errors.map((e) => ({ ...e, iconBg: 'var(--status-red-bg)', iconColor: 'var(--status-red-text)', glyph: crossGlyph })),
    ...v.warnings.map((e) => ({ ...e, iconBg: 'var(--status-amber-bg)', iconColor: 'var(--status-amber-text)', glyph: warnGlyph })),
  ]

  const verdict = hasErr
    ? { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', iconBg: 'var(--status-red-text)', iconColor: '#fff', glyph: crossGlyph, title: 'Not compliant yet', sub: v.errors.length + ' error' + (v.errors.length > 1 ? 's' : '') + ' must be resolved before approval', titleColor: 'var(--status-red-text)', subColor: 'var(--fg-2)', score: passCount + '/16' }
    : hasWarn
      ? { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', iconBg: 'var(--status-amber-text)', iconColor: '#fff', glyph: warnGlyph, title: 'Review warnings', sub: v.warnings.length + ' warning to confirm before transmission', titleColor: 'var(--status-amber-text)', subColor: 'var(--fg-2)', score: passCount + '/16' }
      : { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', iconBg: 'var(--status-green-text)', iconColor: '#fff', glyph: tickGlyph11, title: 'Compliant — ready to approve', sub: 'All 16 MBS checks passed. Transmit-ready.', titleColor: 'var(--status-green-text)', subColor: 'var(--fg-2)', score: '16/16' }

  const approveBtn = clear
    ? { bg: 'var(--accent)', color: '#fff', cursor: 'pointer', label: 'Approve & archive', glyph: shieldGlyph }
    : { bg: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed', label: 'Resolve issues first', glyph: lockGlyph }
  const approveHint = clear ? 'Generates PDF + UBL, writes the audit log, and queues for FIRS transmit.' : 'Approval unlocks once all errors and warnings are cleared.'

  return (
    <div className="pf-create-grid" style={{ display: 'grid', gridTemplateColumns: '1fr 320px', gap: 16, alignItems: 'start' }}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        <div style={{ background: verdict.bg, border: `1px solid ${verdict.border}`, borderRadius: 8, padding: '18px 20px', display: 'flex', alignItems: 'center', gap: 14 }}>
          <span style={{ flex: 'none', width: 38, height: 38, borderRadius: 8, background: verdict.iconBg, color: verdict.iconColor, display: 'grid', placeItems: 'center' }}>{verdict.glyph}</span>
          <div style={{ flex: 1 }}>
            <div style={{ fontSize: 15, fontWeight: 600, color: verdict.titleColor }}>{verdict.title}</div>
            <div style={{ fontSize: 13, color: verdict.subColor }}>{verdict.sub}</div>
          </div>
          <span className="mono" style={{ fontSize: 12, fontWeight: 600, color: verdict.titleColor }}>{verdict.score}</span>
        </div>

        {issues.length > 0 && (
          <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
            <div style={{ padding: '12px 18px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
              <span style={{ fontSize: 14, fontWeight: 600 }}>Issues to resolve</span>
              <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
                {issues.length} OPEN
              </span>
            </div>
            {issues.map((e) => (
              <div key={e.id} style={{ display: 'flex', alignItems: 'flex-start', gap: 13, padding: '14px 18px', borderBottom: '1px solid var(--line-1)' }}>
                <span style={{ flex: 'none', width: 24, height: 24, borderRadius: 6, background: e.iconBg, color: e.iconColor, display: 'grid', placeItems: 'center', marginTop: 1 }}>{e.glyph}</span>
                <div style={{ flex: 1 }}>
                  <div style={{ fontSize: 13.5, fontWeight: 500 }}>{e.label}</div>
                  <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 2 }}>
                    {e.detail}
                  </div>
                </div>
                <button onClick={() => ctx.applyFix(e.patch)} className="v2-btn pf-btn" style={{ flex: 'none', height: 30, padding: '0 12px', fontSize: 12.5, background: 'var(--accent-tint)', color: 'var(--accent)' }}>
                  {e.fixLabel}
                </button>
              </div>
            ))}
          </div>
        )}

        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
          <div style={{ padding: '12px 18px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
            <span style={{ fontSize: 14, fontWeight: 600 }}>Passed checks</span>
            <span className="mono" style={{ fontSize: 11, color: 'var(--status-green-text)' }}>
              {passCount} / 16 PASSED
            </span>
          </div>
          <div style={{ padding: '6px 0' }}>
            {v.passed.map((p) => (
              <div key={p} style={{ display: 'flex', alignItems: 'center', gap: 11, padding: '8px 18px' }}>
                <span style={{ flex: 'none', width: 18, height: 18, borderRadius: 99, background: 'var(--status-green-bg)', color: 'var(--status-green-text)', display: 'grid', placeItems: 'center' }}>{tickGlyph13}</span>
                <span style={{ flex: 1, fontSize: 13, color: 'var(--fg-2)' }}>{p}</span>
                <span className="mono" style={{ fontSize: 10, color: 'var(--status-green-text)', fontWeight: 600 }}>
                  PASS
                </span>
              </div>
            ))}
          </div>
        </div>
      </div>
      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, padding: 20, position: 'sticky', top: 0 }}>
        <div className="label" style={{ marginBottom: 14 }}>
          Invoice
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 9, marginBottom: 18 }}>
          <div style={{ display: 'flex', justifyContent: 'space-between' }}>
            <span style={{ fontSize: 12.5, color: 'var(--fg-3)' }}>Buyer</span>
            <span style={{ fontSize: 12.5, fontWeight: 500, textAlign: 'right', maxWidth: 170 }}>{draft.buyer}</span>
          </div>
          <div style={{ display: 'flex', justifyContent: 'space-between' }}>
            <span style={{ fontSize: 12.5, color: 'var(--fg-3)' }}>TIN</span>
            <span className="mono" style={{ fontSize: 12, fontWeight: 500 }}>{draft.buyerTin}</span>
          </div>
          <div style={{ display: 'flex', justifyContent: 'space-between' }}>
            <span style={{ fontSize: 12.5, color: 'var(--fg-3)' }}>Total</span>
            <span className="money" style={{ fontSize: 13, fontWeight: 700 }}>{fmt(total)}</span>
          </div>
        </div>
        <button onClick={ctx.approve} className="v2-btn pf-btn" style={{ width: '100%', justifyContent: 'center', height: 42, marginBottom: 10, background: approveBtn.bg, color: approveBtn.color, cursor: approveBtn.cursor }}>
          <span style={{ display: 'inline-flex' }}>{approveBtn.glyph}</span> {approveBtn.label}
        </button>
        <button onClick={ctx.backToEdit} className="v2-btn v2-btn-ghost pf-btn" style={{ width: '100%', justifyContent: 'center', height: 38 }}>
          Back to edit
        </button>
        <p style={{ fontSize: 11.5, color: 'var(--fg-3)', textAlign: 'center', margin: '14px 0 0', lineHeight: 1.5 }}>{approveHint}</p>
      </div>
    </div>
  )
}
