import { GROUPS, STAGES } from '../content'
import { Icon } from '../icons'
import type { Route } from '../route'
import type { Group } from '../types'

type JourneyStepperProps = { route: Route; onGroup: (g: Group) => void; tourStage?: number }

export function JourneyStepper({ route, onGroup, tourStage }: JourneyStepperProps) {
  return (
    <div
      data-testid="lib-stepper"
      className="lib-px"
      style={{
        position: 'sticky',
        top: 0,
        zIndex: 20,
        minHeight: 64,
        display: 'flex',
        flexWrap: 'wrap',
        alignItems: 'center',
        justifyContent: 'space-between',
        gap: '8px 24px',
        padding: '8px 40px',
        background: 'var(--header-bg)',
        backdropFilter: 'blur(18px)',
        WebkitBackdropFilter: 'blur(18px)',
        borderBottom: '1px solid var(--header-border)',
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 4, minWidth: 0 }}>
        <span className="t-meta" style={{ marginRight: 10, fontSize: 10 }}>
          INVOICE JOURNEY
        </span>
        {STAGES.map(([label, gid], i) => {
          const active = tourStage !== undefined ? i === tourStage : route.view !== 'home' && route.group.id === gid
          const group = GROUPS.find((g) => g.id === gid)
          return (
            <div key={gid} style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
              <button
                type="button"
                className="lib-stage"
                aria-current={active ? 'true' : undefined}
                onClick={() => group && onGroup(group)}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 7,
                  border: `1px solid ${active ? 'var(--tab-active-border)' : 'transparent'}`,
                  cursor: 'pointer',
                  borderRadius: 6,
                  padding: '6px 10px',
                  fontSize: '12.5px',
                  fontWeight: 700,
                  background: active ? 'var(--sage-panel)' : 'transparent',
                  color: active ? 'var(--tab-active-text)' : 'var(--muted-foreground)',
                }}
              >
                <span className="mono" style={{ fontSize: 10, opacity: 0.7 }}>
                  {String(i + 1).padStart(2, '0')}
                </span>
                <span>{label}</span>
              </button>
              {i < STAGES.length - 1 && (
                <span style={{ color: 'var(--input)', display: 'inline-flex' }}>
                  <Icon name="chevron-right" size={14} />
                </span>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
