import { useCallback, useEffect, useRef, useState } from 'react'
import { Header } from './components/Header'
import { Sidebar } from './components/Sidebar'
import { chooseConsent, CookieNotice, readConsent, syncBlogConsent, trackBlogPageView } from './analytics'
import { privacyHref } from './links'
import { blogPath, parseBlogPath, type Route } from './route'

const toTop = () => {
  const main = document.getElementById('blog-main')
  if (main) main.scrollTop = 0
  document.documentElement.scrollTop = 0
}

const HOME: Route = { view: 'home' }

export function App() {
  const [route, setRoute] = useState<Route>(() => parseBlogPath(window.location.pathname))

  const [consent, setConsent] = useState(() => readConsent())
  const [reopened, setReopened] = useState(false)

  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState !== 'visible') return
      const next = syncBlogConsent()
      if (next) setConsent(next)
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => document.removeEventListener('visibilitychange', onVisible)
  }, [])

  useEffect(() => {
    const onPop = () => {
      setRoute(parseBlogPath(window.location.pathname))
      toTop()
    }
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  // A click on the current view adds no history entry; a non-canonical URL is corrected in place.
  const navigate = useCallback((next: Route) => {
    const path = blogPath(next)
    if (path !== blogPath(parseBlogPath(window.location.pathname))) window.history.pushState(null, '', path)
    else if (path !== window.location.pathname) window.history.replaceState(null, '', path)
    setRoute(next)
    toTop()
  }, [])

  // The first path is reported by config; later changes by path only (not StrictMode, re-clicks or corrections).
  const path = blogPath(route)
  const viewed = useRef(path)
  useEffect(() => {
    if (path === viewed.current) return
    viewed.current = path
    trackBlogPageView()
  }, [path])

  return (
    <div
      className="asc-app"
      style={{
        position: 'relative',
        height: '100vh',
        display: 'flex',
        background: 'var(--background)',
        fontFamily: 'var(--font-sans)',
        color: 'var(--ink)',
        overflow: 'hidden',
      }}
    >
      <Sidebar route={route} onHome={() => navigate(HOME)} onSubscribe={() => navigate({ view: 'subscribe' })}
        onCookieChoices={() => {
          const next = syncBlogConsent()
          if (next) setConsent(next)
          setReopened(true)
        }}
      />
      <main id="blog-main" style={{ flex: 1, minWidth: 0, overflowY: 'auto', position: 'relative' }}>
        <Header route={route} onNavigate={navigate} />
        {(consent === null || reopened) && (
          <CookieNotice
            current={consent}
            suppressed={false}
            privacyHref={privacyHref()}
            onChoose={(c) => {
              setConsent(chooseConsent(c))
              setReopened(false)
            }}
          />
        )}
      </main>
      <style>{__COOKIE_NOTICE_CSS__}</style>
    </div>
  )
}
