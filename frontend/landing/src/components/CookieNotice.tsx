import type { ConsentRecord } from '../consent'
import { SHARED_COOKIE_DOMAIN } from '../hubspot'

export type ConsentChoice = 'accept' | 'reject'

// Pure and presentational: no storage, no analytics, no window. A non-null `current`
// IS the reopened state and renders the leading .cn-setting line.
export function CookieNotice({
  current,
  suppressed,
  onChoose,
  privacyHref = '/privacy',
}: {
  current: ConsentRecord | null
  suppressed: boolean
  onChoose: (choice: ConsentChoice) => void
  privacyHref?: string
}) {
  return (
    <>
      <div
        role="region"
        aria-label="Cookie notice"
        aria-live="polite"
        className="cookie-note card-floating"
        inert={suppressed}
      >
        <div className="t-step">Cookies</div>
        {current ? (
          <p className="cn-setting">
            {current.analytics ? 'Analytics cookies are on.' : 'Analytics cookies are off.'}
          </p>
        ) : null}
        <p className="cn-body">
          We use Google Analytics to see how people find and use this page. That is the only non-essential cookie we
          set: no advertising, no remarketing, no data sold to anyone. Your choice applies to {SHARED_COOKIE_DOMAIN} and the Feature Library.
        </p>
        <a className="lnk cn-link" href={privacyHref}>
          Read the privacy &amp; cookie policy
        </a>
        <div className="cn-actions">
          <button type="button" data-consent="accept" onClick={() => onChoose('accept')}>
            Accept
          </button>
          <button type="button" data-consent="reject" onClick={() => onChoose('reject')}>
            Reject
          </button>
        </div>
      </div>
      <div aria-hidden="true" className="cn-spacer" />
    </>
  )
}
