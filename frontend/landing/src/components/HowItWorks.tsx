import { Icon } from '../icons'
import { STEPS } from '../data'

export function HowItWorks() {
  return (
    <section id="how" style={{ borderBottom: '1px solid var(--line-1)' }}>
      <div style={{ maxWidth: 1280, margin: '0 auto', padding: '88px 32px' }}>
        <div
          style={{
            display: 'flex',
            alignItems: 'flex-end',
            justifyContent: 'space-between',
            marginBottom: 48,
            gap: 24,
            flexWrap: 'wrap',
          }}
        >
          <div>
            <div className="label" style={{ marginBottom: 14 }}>
              / 01 — HOW IT WORKS
            </div>
            <h2 style={{ fontSize: 40, lineHeight: 1.08, letterSpacing: '-0.03em', fontWeight: 600, margin: 0, maxWidth: 600 }}>
              From your accounting system to a compliant invoice in three steps.
            </h2>
          </div>
          <div style={{ maxWidth: 340, background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-xl)', padding: '20px 22px' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 9, marginBottom: 12 }}>
              <span
                style={{
                  flex: 'none',
                  width: 26,
                  height: 26,
                  borderRadius: 'var(--radius-md)',
                  background: 'var(--accent-tint)',
                  color: 'var(--accent)',
                  display: 'grid',
                  placeItems: 'center',
                }}
              >
                <Icon paths={['M12 2 2 7l10 5 10-5-10-5Z', 'm2 17 10 5 10-5', 'm2 12 10 5 10-5']} size={15} />
              </span>
              <span className="label">No rip-and-replace</span>
            </div>
            <p style={{ fontSize: 15, lineHeight: 1.6, color: 'var(--fg-2)', margin: 0 }}>
              ASComply sits beside your existing stack and handles the compliance work — nothing to migrate, nothing to
              switch off.
            </p>
          </div>
        </div>
        <div
          className="ios-grid ios-3"
          style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(3, 1fr)',
            gap: 1,
            background: 'var(--line-1)',
            border: '1px solid var(--line-1)',
            borderRadius: 'var(--radius-xl)',
            overflow: 'hidden',
          }}
        >
          {STEPS.map((s) => (
            <div key={s.num} style={{ background: 'var(--bg-2)', padding: '32px 28px 36px', display: 'flex', flexDirection: 'column' }}>
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 28 }}>
                <span className="mono" style={{ fontSize: 13, fontWeight: 600, color: 'var(--accent)' }}>
                  {s.num}
                </span>
                <span style={{ color: 'var(--fg-3)' }}>{s.glyph}</span>
              </div>
              <h3 style={{ fontSize: 21, letterSpacing: '-0.02em', fontWeight: 600, margin: '0 0 10px' }}>{s.title}</h3>
              <p style={{ fontSize: 14, lineHeight: 1.6, color: 'var(--fg-2)', margin: '0 0 18px', flex: 1 }}>{s.body}</p>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 7 }}>
                {s.points.map((p) => (
                  <div key={p} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    <span style={{ color: 'var(--accent)', flex: 'none' }}>
                      <Icon paths={['M20 6 9 17l-5-5']} />
                    </span>
                    <span style={{ fontSize: 13, color: 'var(--fg-2)' }}>{p}</span>
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>
      </div>
    </section>
  )
}
