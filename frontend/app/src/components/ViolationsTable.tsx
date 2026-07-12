// Reusable violations table (M3-09-03) — the M4 deliverable. Playground-agnostic:
// imports ONLY from lib/validationApi + React so M4 can mount it unchanged (AC-1).
// Pure function of props (no state, no effects). Styling mirrors ClientsView.tsx's
// token usage (container/header/row + the severity-pill pattern at L109-111) so it
// reads as the same design language.
//
// Empty violations -> the clean-pass block (AC-5); this is the single source of that
// state, inherited by M4 without change. Non-empty -> a semantic <table>, columns
// Severity | Message | Rule key | Path | Rule-set version, in response order (backend
// pre-sorts by rule_key then path — do NOT re-sort here).

import { severityStyle, type Violation } from '../lib/validationApi'

export interface ViolationsTableProps {
  violations: Violation[]
  ruleSetVersion: number
}

export function ViolationsTable({ violations, ruleSetVersion }: ViolationsTableProps): React.JSX.Element {
  if (violations.length === 0) {
    return (
      <div
        style={{
          background: 'var(--status-green-bg)',
          border: '1px solid var(--status-green-border)',
          borderRadius: 8,
          padding: '14px 18px',
          fontSize: 13.5,
          color: 'var(--status-green-text)',
        }}
      >
        Passes all rules — no violations. Evaluated against rule-set v{ruleSetVersion}.
      </div>
    )
  }

  return (
    <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
      <table style={{ width: '100%', borderCollapse: 'collapse' }}>
        <thead>
          <tr style={{ background: 'var(--bg-1)' }}>
            <th style={{ textAlign: 'left', padding: '11px 18px', borderBottom: '1px solid var(--line-1)', fontSize: 11, fontWeight: 600, color: 'var(--fg-3)' }}>Severity</th>
            <th style={{ textAlign: 'left', padding: '11px 18px', borderBottom: '1px solid var(--line-1)', fontSize: 11, fontWeight: 600, color: 'var(--fg-3)' }}>Message</th>
            <th style={{ textAlign: 'left', padding: '11px 18px', borderBottom: '1px solid var(--line-1)', fontSize: 11, fontWeight: 600, color: 'var(--fg-3)' }}>Rule key</th>
            <th style={{ textAlign: 'left', padding: '11px 18px', borderBottom: '1px solid var(--line-1)', fontSize: 11, fontWeight: 600, color: 'var(--fg-3)' }}>Path</th>
            <th style={{ textAlign: 'left', padding: '11px 18px', borderBottom: '1px solid var(--line-1)', fontSize: 11, fontWeight: 600, color: 'var(--fg-3)' }}>Rule-set version</th>
          </tr>
        </thead>
        <tbody>
          {violations.map((v, i) => {
            const st = severityStyle(v.severity)
            return (
              <tr key={`${v.rule_key}-${v.path ?? ''}-${i}`}>
                <td style={{ padding: '10px 18px', borderBottom: '1px solid var(--line-1)' }}>
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, background: st.bg, border: `1px solid ${st.border}`, borderRadius: 999, padding: '3px 9px' }}>
                    <span style={{ width: 6, height: 6, borderRadius: 99, background: st.text }} />
                    <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: st.text }}>{st.label}</span>
                  </span>
                </td>
                <td style={{ padding: '10px 18px', borderBottom: '1px solid var(--line-1)', fontSize: 13, color: 'var(--fg-2)' }}>{v.message}</td>
                <td style={{ padding: '10px 18px', borderBottom: '1px solid var(--line-1)' }}>
                  <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>{v.rule_key}</span>
                </td>
                <td style={{ padding: '10px 18px', borderBottom: '1px solid var(--line-1)' }}>
                  <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>{v.path ?? '—'}</span>
                </td>
                <td style={{ padding: '10px 18px', borderBottom: '1px solid var(--line-1)', fontSize: 13, color: 'var(--fg-2)' }}>{ruleSetVersion}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
