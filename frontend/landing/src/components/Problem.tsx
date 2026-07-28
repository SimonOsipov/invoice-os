import { Icon } from '../icons'
import { PROBLEMS } from '../data'

// Same two-column split as Compliance: copy on the left, evidence on the right.
// The right column is a plain card grid — these are symptoms, not controls, so
// they get no hover (the prototype's rule for content cells).
export function Problem() {
  return (
    <section id="problem" style={{ borderBottom: '1px solid var(--line-1)' }}>
      <div
        className="ios-grid ios-2"
        style={{
          maxWidth: 1280,
          margin: '0 auto',
          padding: '88px 32px',
          display: 'grid',
          gridTemplateColumns: '0.92fr 1.08fr',
          gap: 64,
          alignItems: 'center',
        }}
      >
        <div>
          <div className="eyebrow" style={{ marginBottom: 14 }}>
            THE PROBLEM
          </div>
          <h2 style={{ fontSize: 38, lineHeight: 1.1, letterSpacing: '-0.03em', fontWeight: 600, margin: '0 0 18px' }}>
            The invoice is becoming a compliance checkpoint. Is your business ready?
          </h2>
          <p style={{ fontSize: 16, lineHeight: 1.65, color: 'var(--fg-2)', margin: '0 0 14px' }}>
            Nigeria's e-invoicing transition means businesses will need more than PDF invoices and manual approval
            chains.
          </p>
          <p style={{ fontSize: 16, lineHeight: 1.65, color: 'var(--fg-2)', margin: 0 }}>
            Many companies still manage invoices across accounting software, Excel files, emails, ERP systems and manual
            approvals. This creates errors, delays and audit risk.
          </p>
        </div>

        <div className="ios-grid ios-2" style={{ display: 'grid', gridTemplateColumns: 'repeat(2, minmax(0, 1fr))', gap: 12 }}>
          {PROBLEMS.map((p) => (
            <div
              key={p}
              style={{
                display: 'flex',
                alignItems: 'flex-start',
                gap: 12,
                background: 'var(--bg-2)',
                border: '1px solid var(--line-1)',
                borderRadius: 'var(--radius-md)',
                padding: 16,
              }}
            >
              <span style={{ flex: 'none', color: 'var(--status-amber-text)', display: 'inline-flex', marginTop: 1 }}>
                <Icon
                  paths={['m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z', 'M12 9v4', 'M12 17h.01']}
                  size={16}
                />
              </span>
              <span style={{ fontSize: 14, lineHeight: 1.5, color: 'var(--fg-2)' }}>{p}</span>
            </div>
          ))}
        </div>
      </div>
    </section>
  )
}
