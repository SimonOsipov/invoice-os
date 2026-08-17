import type { ConsentRecord } from '../consent'

export type ConsentChoice = 'accept' | 'reject'

// Pure and presentational: no storage, no analytics, no window. A non-null `current`
// IS the reopened state and renders the leading .cn-setting line.
export function CookieNotice({
  current,
  suppressed,
  onChoose,
}: {
  current: ConsentRecord | null
  suppressed: boolean
  onChoose: (choice: ConsentChoice) => void
}) {
  return (
    <>
      <div
        role="region"
        aria-label="Cookie notice"
        aria-live="polite"
        className="cookie-note"
        inert={suppressed}
      >
        <div className="eyebrow">Cookies</div>
        {current ? (
          <p className="cn-setting">
            {current.analytics ? 'Analytics cookies are on.' : 'Analytics cookies are off.'}
          </p>
        ) : null}
        <p className="cn-body">
          We use Google Analytics to see how people find and use this page. That is the only non-essential cookie we
          set: no advertising, no remarketing, no data sold to anyone.
        </p>
        <a className="lnk cn-link" href="/privacy">
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
