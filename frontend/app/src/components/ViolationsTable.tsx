// Reusable violations table. Imports ONLY from lib/validationApi + React, so any screen
// can mount it unchanged.
// Pure function of props (no state, no effects).
//
// Empty violations -> the clean-pass block (AC-5); this is the single source of that
// state, inherited by M4 without change. Non-empty -> a semantic <table>, columns
// Severity | Message | Rule key | Path, in response order (backend pre-sorts by rule_key
// then path — do NOT re-sort here).

import { severityStyle, violationLine, type LineTarget, type Violation } from '../lib/validationApi'

export interface ViolationsTableProps {
  violations: Violation[]
  ruleSetVersion: number
  onOpenLine?: (t: LineTarget) => void
  lineDisabled?: boolean
}

export function ViolationsTable({ violations, ruleSetVersion, onOpenLine, lineDisabled }: ViolationsTableProps): React.JSX.Element {
  if (violations.length === 0) {
    return (
      <div style={{ fontSize: 13, color: 'var(--fg-2)' }}>
        Passes all rules — no violations. Evaluated against rule-set v{ruleSetVersion}.
      </div>
    )
  }

  // overflowX below is a clip failsafe, not an affordance: the Compliance card is
  // overflow:hidden, so under the table's min-content the overflow would be unrecoverable.
  return (
    <div
      style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflowX: 'auto' }}
    >
      <table style={{ width: '100%', borderCollapse: 'collapse' }}>
        <thead>
          <tr style={{ background: 'var(--bg-1)' }}>
            <th className="label" style={{ textAlign: 'left', padding: '8px 12px', borderBottom: '1px solid var(--line-1)' }}>Severity</th>
            <th className="label" style={{ textAlign: 'left', padding: '8px 12px', borderBottom: '1px solid var(--line-1)' }}>Message</th>
            <th className="label" style={{ textAlign: 'left', padding: '8px 12px', borderBottom: '1px solid var(--line-1)' }}>Rule key</th>
            <th className="label" style={{ textAlign: 'left', padding: '8px 12px', borderBottom: '1px solid var(--line-1)' }}>Path</th>
          </tr>
        </thead>
        <tbody>
          {violations.map((v, i) => {
            const st = severityStyle(v.severity)
            const target = onOpenLine ? violationLine(v.path) : null
            return (
              <tr key={`${v.rule_key}-${v.path ?? ''}-${i}`}>
                <td style={{ padding: '10px 12px', borderBottom: '1px solid var(--line-1)' }}>
                  <span style={{ display: 'inline-flex', alignItems: 'center', background: st.bg, border: `1px solid ${st.border}`, borderRadius: 'var(--radius-sm)', padding: '2px 7px' }}>
                    <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: st.text }}>{st.label}</span>
                  </span>
                </td>
                {/* overflowWrap:'anywhere', not break-word: only `anywhere` shrinks the
                    min-content width table-layout:auto reads when sizing the table. */}
                <td style={{ padding: '10px 12px', borderBottom: '1px solid var(--line-1)', fontSize: 12.5, color: 'var(--fg-1)', overflowWrap: 'anywhere', lineHeight: 1.5 }}>{v.message}</td>
                <td style={{ padding: '10px 12px', borderBottom: '1px solid var(--line-1)', overflowWrap: 'anywhere', lineHeight: 1.4 }}>
                  <span className="mono" style={{ fontSize: 11, color: 'var(--fg-2)' }}>{v.rule_key}</span>
                </td>
                <td style={{ padding: '10px 12px', borderBottom: '1px solid var(--line-1)', overflowWrap: 'anywhere', lineHeight: 1.4 }}>
                  <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>{v.path ?? '—'}</span>
                  {target && onOpenLine && (
                    <button
                      type="button"
                      data-testid="violation-open-line"
                      title={`Open line ${target.line} in the editor`}
                      disabled={lineDisabled}
                      onClick={() => {
                        if (!lineDisabled) onOpenLine(target)
                      }}
                      className="v2-btn v2-btn-ghost pf-btn"
                      style={{
                        height: 24,
                        padding: '0 8px',
                        fontSize: 11.5,
                        marginLeft: 8,
                        ...(lineDisabled ? { background: 'transparent', opacity: 0.45, cursor: 'not-allowed', filter: 'none' } : null),
                      }}
                    >
                      Line {target.line}
                    </button>
                  )}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
