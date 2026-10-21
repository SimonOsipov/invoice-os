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
          We use Google Analytics to see how people use this page. Accept also turns on our HubSpot live chat, which
          sets its own cookies. No advertising, no data sold to anyone. Your choice applies to {SHARED_COOKIE_DOMAIN}, the Feature Library and the Blog.
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
