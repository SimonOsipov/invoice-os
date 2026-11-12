import { useEffect, useRef, useState } from 'react'
import { SENDING_NOTE, SENDING_TEXT, SENDING_TO, STAGE_MS, STAGES } from '../copy'
import { GLYPHS, Icon } from '../ds'

export function SendingStep({ stepNo, email }: { stepNo: string; email: string }) {
  const [stage, setStage] = useState(0)
  const heading = useRef<HTMLHeadingElement>(null)
  useEffect(() => heading.current?.focus(), [])
  useEffect(() => {
    const id = setInterval(() => setStage((s) => Math.min(s + 1, STAGES.length - 1)), STAGE_MS)
    return () => clearInterval(id)
  }, [])
  return (
    <section className="check-card check-sending" aria-live="polite" aria-busy="true">
      <div className="check-step-head">
        <span className="check-mono check-step-no">{stepNo}</span>
        <h2 ref={heading} tabIndex={-1} className="check-step-title">
          {SENDING_TEXT}
        </h2>
      </div>
      <div className="check-bar" aria-hidden="true">
        <i />
      </div>
      <ol className="check-stages">
        {STAGES.map((label, i) => (
          <li key={label} className={i < stage ? 'is-done' : i === stage ? 'is-now' : 'is-todo'}>
            <span className="check-sdot" aria-hidden="true">
              {i < stage && <Icon paths={GLYPHS.check} size={13} strokeWidth={2} />}
              {i === stage && <span className="check-spinner" />}
            </span>
            <span>{label}</span>
          </li>
        ))}
      </ol>
      <div className="check-sending-to">
        <span className="check-sending-addr">{SENDING_TO(email)}</span>
        <span className="check-sending-note">{SENDING_NOTE}</span>
      </div>
    </section>
  )
}
