import { GROUPS } from '../content'
import type { Feature, Group } from '../types'
import { Icon } from '../icons'
import { thumbStep } from '../scene'
import { formatSeconds, STEP_SECONDS } from '../timing'
import { Button } from './Button'
import { ComingSoonPill } from './ComingSoonPill'
import { SceneView } from './SceneView'

type GroupPageProps = { group: Group; openHref: string | null; onFeature: (f: Feature) => void }

const dot = { width: 6, height: 6, borderRadius: '50%', background: 'var(--border)' }

export function GroupPage({ group, openHref, onFeature }: GroupPageProps) {
  return (
    <section style={{ padding: '56px 40px 80px', maxWidth: 1180, display: 'flex', flexDirection: 'column', gap: 40 }}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 18, maxWidth: 700 }}>
        <div className="t-eyebrow">{`Group ${group.n} of ${GROUPS.length}`}</div>
        <h2 className="t-h2" style={{ margin: 0, fontSize: 44 }}>
          {group.name}
        </h2>
        <p className="t-lead" style={{ margin: 0 }}>
          {group.intro}
        </p>
        {openHref && (
          <div style={{ marginTop: 4 }}>
            <Button variant="outline" size="sm" arrow href={openHref}>
              Open in Platform
            </Button>
          </div>
        )}
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(320px, 1fr))', gap: 20 }}>
        {group.feats.map((f) => (
          <button
            key={f.id}
            id={`fc-${f.id}`}
            type="button"
            className="lib-card"
            onClick={() => onFeature(f)}
            style={{
              textAlign: 'left',
              cursor: 'pointer',
              display: 'flex',
              flexDirection: 'column',
              padding: 0,
              overflow: 'hidden',
              background: 'var(--card)',
              border: '1px solid var(--border)',
              borderRadius: 6,
              color: 'var(--ink)',
            }}
          >
            <div style={{ position: 'relative', height: 192, background: 'var(--surface)', overflow: 'hidden' }}>
              <div
                style={{
                  position: 'absolute',
                  left: 20,
                  right: 20,
                  top: 16,
                  bottom: 44,
                  background: '#fff',
                  borderRadius: 6,
                  overflow: 'hidden',
                  display: 'flex',
                  flexDirection: 'column',
                  boxShadow: 'var(--shadow-soft)',
                }}
              >
                <div
                  style={{
                    flex: 'none',
                    display: 'flex',
                    alignItems: 'center',
                    gap: 5,
                    height: 22,
                    padding: '0 10px',
                    background: 'var(--muted)',
                    borderBottom: '1px solid var(--border)',
                  }}
                >
                  <span style={dot} />
                  <span style={dot} />
                  <span style={dot} />
                  <span
                    className="mono"
                    style={{
                      marginLeft: 6,
                      fontSize: 9,
                      color: 'var(--muted-foreground)',
                      whiteSpace: 'nowrap',
                      overflow: 'hidden',
                      textOverflow: 'ellipsis',
                    }}
                  >
                    {f.sc.win}
                  </span>
                </div>
                <div
                  style={{
                    flex: '1 1 0',
                    minHeight: 0,
                    padding: '8px 12px',
                    overflow: 'hidden',
                    display: 'flex',
                    flexDirection: 'column',
                    justifyContent: 'center',
                  }}
                >
                  <SceneView sc={f.sc} idx={thumbStep(f)} size="thumb" />
                </div>
              </div>
              <span
                style={{
                  position: 'absolute',
                  left: 20,
                  bottom: 7,
                  width: 30,
                  height: 30,
                  borderRadius: '50%',
                  background: 'var(--accent)',
                  color: 'var(--accent-foreground)',
                  display: 'grid',
                  placeItems: 'center',
                  boxShadow: '0 6px 16px -6px rgba(8,47,49,0.5)',
                }}
              >
                <Icon name="play" size={13} />
              </span>
              <span
                className="mono"
                style={{
                  position: 'absolute',
                  right: 20,
                  bottom: 13,
                  fontSize: 11,
                  fontWeight: 600,
                  color: 'var(--surface-foreground)',
                  background: 'rgba(8,47,49,0.82)',
                  borderRadius: 4,
                  padding: '2px 7px',
                }}
              >
                {formatSeconds(f.sc.steps.length * STEP_SECONDS)}
              </span>
            </div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 8, padding: '22px 24px 24px' }}>
              {f.status === 'soon' && <ComingSoonPill tone="light" style={{ alignSelf: 'flex-start' }} />}
              <span style={{ fontSize: 18, fontWeight: 700, letterSpacing: '-0.01em', lineHeight: 1.3 }}>{f.title}</span>
              <span className="t-body-sm">{f.short}</span>
            </div>
          </button>
        ))}
      </div>
    </section>
  )
}
