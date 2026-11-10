import { landingHref } from '../links'

export function Footer({ onCookieChoices }: { onCookieChoices: () => void }) {
  return (
    <footer className="check-footer">
      <a href={landingHref('/privacy')}>Privacy</a>
      <button type="button" className="check-link" onClick={onCookieChoices}>
        Cookie choices
      </button>
    </footer>
  )
}
