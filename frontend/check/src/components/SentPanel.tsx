import { useEffect, useRef } from 'react'
import {
  BOOK_DEMO,
  CHECK_ANOTHER,
  NEXT_STEP,
  SENT_FROM,
  SENT_LABEL,
  SENT_NOTE,
  SENT_TEXT,
  SIGN_UP,
  UPSELL_TEXT,
  UPSELL_TITLE,
} from '../copy'
import { Icon } from '../ds'
import { CHECK_GLYPHS } from '../glyphs'
import { landingHref } from '../links'

export function SentPanel({ email, onAnother }: { email: string; onAnother: () => void }) {
  // The sending heading unmounted; focus moves to this one.
  const heading = useRef<HTMLHeadingElement>(null)
  useEffect(() => heading.current?.focus(), [])
  const [before, after] = SENT_NOTE.split(SENT_FROM)
  return (
    <section className="check-card check-sent">
      <div className="check-sent-head" role="status">
        <span className="check-sent-mark" aria-hidden="true">
          <Icon paths={CHECK_GLYPHS.mail} size={26} strokeWidth={2} />
        </span>
        <div>
          <span className="check-mono check-sent-label">{SENT_LABEL}</span>
          <h2 ref={heading} tabIndex={-1} className="check-step-title check-sent-title">
            {SENT_TEXT(email)}
          </h2>
          <p className="check-sent-note">
            {before}
            <strong>{SENT_FROM}</strong>
            {after}
          </p>
        </div>
      </div>
      <div className="check-upsell">
        <span className="t-step check-upsell-step">{NEXT_STEP}</span>
        <p className="check-upsell-title">
          {UPSELL_TITLE[0]}
          <br />
          <span className="check-upsell-accent">{UPSELL_TITLE[1]}</span>
        </p>
        <p className="check-upsell-text">{UPSELL_TEXT}</p>
        <div className="check-upsell-actions">
          <a className="check-btn check-btn--next" href={landingHref('/?register')}>
            {SIGN_UP}
            <Icon paths={CHECK_GLYPHS['arrow-right']} size={16} strokeWidth={2} />
          </a>
          <a className="check-upsell-link" href={landingHref('/?demo')}>
            {BOOK_DEMO}
          </a>
        </div>
      </div>
      <button type="button" className="check-link" onClick={onAnother}>
        {CHECK_ANOTHER}
      </button>
    </section>
  )
}
