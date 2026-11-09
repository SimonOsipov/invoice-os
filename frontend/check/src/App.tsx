import { useEffect, useState } from 'react'
import { Footer } from './components/Footer'
import { chooseConsent, CookieNotice, readConsent, syncCheckConsent } from './analytics'
import { CHECK_H1, CHECK_INTRO } from './copy'
import { landingHref } from './links'

export function App() {
  const [consent, setConsent] = useState(() => readConsent())
  const [reopened, setReopened] = useState(false)

  // A choice made in another tab applies here when this tab is shown again.
  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState !== 'visible') return
      const next = syncCheckConsent()
      if (next) setConsent(next)
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => document.removeEventListener('visibilitychange', onVisible)
  }, [])

  return (
    <div className="check-page">
      <header className="check-header">
        <a href={landingHref('/')}>ASComply Africa</a>
      </header>
      <main className="check-main">
        <h1>{CHECK_H1}</h1>
        <p>{CHECK_INTRO}</p>
        <div className="check-steps" />
      </main>
      <Footer onCookieChoices={() => setReopened(true)} />
      {(consent === null || reopened) && (
        <CookieNotice
          current={consent}
          suppressed={false}
          privacyHref={landingHref('/privacy')}
          onChoose={(c) => {
            setConsent(chooseConsent(c))
            setReopened(false)
          }}
        />
      )}
      <style>{__COOKIE_NOTICE_CSS__}</style>
    </div>
  )
}
