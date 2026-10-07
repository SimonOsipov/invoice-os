import { GROUPS } from '../content'
import { Icon, type GlyphName } from '../icons'
import { Button } from './Button'

type HomeProps = { demoHref: string | null; onGroup: (gid: string) => void; onTour: () => void }

export function Home({ demoHref, onGroup, onTour }: HomeProps) {
  return (
    <>
      <section style={{ position: 'relative', padding: '72px 40px 80px', overflow: 'hidden' }}>
        <div className="hero-grid hero-grid-fade" style={{ position: 'absolute', inset: 0, opacity: 0.7 }} />
        <div style={{ position: 'relative', maxWidth: 880, display: 'flex', flexDirection: 'column', gap: 24 }}>
          <div className="t-eyebrow">Feature library</div>
          <h1
            style={{
              margin: 0,
              fontSize: 'clamp(42px, 5vw, 68px)',
              lineHeight: 1.07,
              letterSpacing: '-0.05em',
              fontWeight: 700,
              color: 'var(--ink)',
            }}
          >
            Every step from invoice
            <br />
            <span style={{ color: 'var(--teal)' }}>to clearance.</span>
          </h1>
          <p className="t-lead" style={{ margin: 0, maxWidth: 600 }}>
            Short screen recordings and plain explanations of what ASComply does to an invoice, from import to FIRS clearance. Pick a group or take the tour.
          </p>
          <div style={{ display: 'flex', alignItems: 'center', gap: 14, flexWrap: 'wrap', marginTop: 4 }}>
            <Button variant="primary" arrow onClick={onTour}>
              Take the tour
            </Button>
          </div>
        </div>
      </section>

      <section style={{ background: 'var(--sage)', padding: '80px 40px' }}>
        <div style={{ maxWidth: 1120, display: 'flex', flexDirection: 'column', gap: 40 }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 16, maxWidth: 640 }}>
            <div className="t-eyebrow">The library</div>
            <h2 className="t-h2" style={{ margin: 0, fontSize: 40 }}>
              Eleven groups, one invoice workflow.
            </h2>
            <p className="t-body" style={{ margin: 0 }}>
              Each group has two or three demos recorded on the sample companies in the platform, with the steps captioned.
            </p>
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(300px, 1fr))', gap: 16 }}>
            {GROUPS.map((g) => (
              <button
                key={g.id}
                type="button"
                className="lib-card"
                onClick={() => onGroup(g.id)}
                style={{
                  textAlign: 'left',
                  cursor: 'pointer',
                  display: 'flex',
                  flexDirection: 'column',
                  gap: 14,
                  padding: 24,
                  background: 'var(--sage-card)',
                  border: '1px solid var(--sage-card-border)',
                  borderRadius: 6,
                  color: 'var(--ink)',
                }}
              >
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                  <span
                    style={{
                      width: 40,
                      height: 40,
                      borderRadius: 6,
                      background: 'var(--sage-panel)',
                      color: 'var(--primary)',
                      display: 'grid',
                      placeItems: 'center',
                    }}
                  >
                    <Icon name={g.icon as GlyphName} size={20} />
                  </span>
                  <span className="t-step">{g.n}</span>
                </div>
                <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                  <span style={{ fontSize: 18, fontWeight: 700, letterSpacing: '-0.01em' }}>{g.name}</span>
                  <span className="t-body-sm" style={{ color: 'var(--text-copy)' }}>
                    {g.one}
                  </span>
                </div>
                <span
                  className="mono"
                  style={{
                    fontSize: 11,
                    fontWeight: 700,
                    letterSpacing: '0.06em',
                    color: 'var(--tab-active-text)',
                    textTransform: 'uppercase',
                  }}
                >
                  {`${g.feats.length} demos`}
                </span>
              </button>
            ))}
          </div>
        </div>
      </section>

      <section style={{ background: 'var(--peach-band)', padding: '72px 40px' }}>
        <div
          style={{
            maxWidth: 1120,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            gap: 32,
            flexWrap: 'wrap',
          }}
        >
          <div style={{ display: 'flex', flexDirection: 'column', gap: 14, maxWidth: 620 }}>
            <div className="t-eyebrow">Book a demo</div>
            <h2 className="t-h2" style={{ margin: 0, fontSize: 38 }}>
              See it on your own invoices.
              <br />
              <span className="t-hl-peach">Start with a demo.</span>
            </h2>
          </div>
          {demoHref && (
            <Button variant="primary" size="lg" arrow href={demoHref}>
              Book the Demo
            </Button>
          )}
        </div>
      </section>
    </>
  )
}
