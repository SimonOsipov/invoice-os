import { calloutPos, tourCallout, type Rect, type TourState, type Win } from '../tour'
import { Icon } from '../icons'
import { Button } from './Button'

type Props = {
  tour: TourState
  rect: Rect | null
  win: Win
  onBack: () => void
  onNext: () => void
  onWatch: () => void
  onClose: () => void
}

const EASE = '380ms var(--ease-out)'

export function TourOverlay({ tour, rect, win, onBack, onNext, onWatch, onClose }: Props) {
  const c = tourCallout(tour)
  const spot = rect ?? { x: win.w / 2, y: win.h / 2, w: 0, h: 0 }
  return (
    <div style={{ position: 'absolute', inset: 0, zIndex: 60 }}>
      <div style={{ position: 'absolute', inset: 0 }} />
      <div
        style={{
          position: 'absolute',
          left: spot.x,
          top: spot.y,
          width: spot.w,
          height: spot.h,
          borderRadius: 10,
          boxShadow: '0 0 0 9999px rgba(8,47,49,0.66), 0 0 0 2px var(--accent)',
          transition: `left ${EASE}, top ${EASE}, width ${EASE}, height ${EASE}`,
          pointerEvents: 'none',
        }}
      />
      <div
        style={{
          position: 'absolute',
          ...calloutPos(rect, tour.phase, win),
          width: 360,
          background: 'var(--card)',
          borderRadius: 8,
          padding: '22px 24px 20px',
          boxShadow: 'var(--shadow-elegant)',
          display: 'flex',
          flexDirection: 'column',
          gap: 12,
          transition: `left ${EASE}, top ${EASE}, bottom ${EASE}`,
        }}
      >
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <span className="t-step">{c.step}</span>
          <button
            type="button"
            className="lib-tour-x"
            aria-label="Close tour"
            onClick={onClose}
            style={{ background: 'none', border: 0, cursor: 'pointer', color: 'var(--muted-foreground)', display: 'inline-flex', padding: 2 }}
          >
            <Icon name="x" size={16} />
          </button>
        </div>
        <div style={{ fontSize: 19, fontWeight: 700, letterSpacing: '-0.02em', lineHeight: 1.25 }}>{c.title}</div>
        <div className="t-body-sm" style={{ color: 'var(--foreground)' }}>
          {c.text}
        </div>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10, marginTop: 6 }}>
          {c.showWatch && (
            <button
              type="button"
              onClick={onWatch}
              style={{ background: 'none', border: 0, padding: 0, cursor: 'pointer', fontSize: 13, fontWeight: 700, color: 'var(--link)', borderBottom: '1px solid var(--link)' }}
            >
              Watch demo
            </button>
          )}
          <div style={{ display: 'flex', gap: 8, marginLeft: 'auto' }}>
            <Button variant="outline" size="sm" disabled={c.backOff} onClick={onBack}>
              Back
            </Button>
            <Button variant="primary" size="sm" onClick={onNext}>
              {c.nextLabel}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
