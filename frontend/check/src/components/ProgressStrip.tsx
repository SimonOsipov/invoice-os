import type { Progress } from '../progress'

// Desktop list and phone strip; CSS shows one. The phone strip stays visible to assistive tech.
export function ProgressStrip({ progress }: { progress: Progress }) {
  const { items, phone } = progress
  return (
    <>
      <ol className="check-progress" aria-label="Check progress">
        {items.map((it) => (
          <li key={it.label} className={`is-${it.state}`} aria-current={it.state === 'current' ? 'step' : undefined}>
            <span className="check-dot" aria-hidden="true">
              {it.mark}
            </span>
            <span className="check-step-text">
              <span className="check-step-lbl">{it.label}</span>
              {it.skipNote && <span className="check-step-sub">{it.skipNote}</span>}
            </span>
          </li>
        ))}
      </ol>
      <div className="check-progress-m">
        <div className="check-progress-m-head">
          <span className="check-mono">
            Step {phone.no} of {phone.total}
          </span>
          <span className="check-progress-m-name">{phone.name}</span>
        </div>
        <div className="check-segs" style={{ gridTemplateColumns: `repeat(${phone.total}, minmax(0, 1fr))` }}>
          {Array.from({ length: phone.total }, (_, i) => (
            <span key={i} className={i < phone.filled ? 'is-filled' : undefined} />
          ))}
        </div>
      </div>
    </>
  )
}
