import { COMPANY_LINE } from '../copy'
import { Logo } from '../ds'
import { landingHref } from '../links'

export function Footer({ onCookieChoices }: { onCookieChoices: () => void }) {
  return (
    <footer className="check-foot">
      <div className="check-wrap check-foot-row">
        <div className="check-foot-id">
          <Logo size={24} />
          <span>{COMPANY_LINE}</span>
        </div>
        <span className="check-foot-links">
          <a href={landingHref('/privacy')} className="check-navlink">
            Privacy
          </a>
          <button type="button" className="check-link" onClick={onCookieChoices}>
            Cookie choices
          </button>
        </span>
      </div>
    </footer>
  )
}
