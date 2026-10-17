import { useCallback, useEffect, useState } from 'react'
import { Header } from './components/Header'
import { Sidebar } from './components/Sidebar'
import { blogPath, parseBlogPath, type Route } from './route'

const toTop = () => {
  const main = document.getElementById('blog-main')
  if (main) main.scrollTop = 0
  document.documentElement.scrollTop = 0
}

const HOME: Route = { view: 'home' }

export function App() {
  const [route, setRoute] = useState<Route>(() => parseBlogPath(window.location.pathname))

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
      <Sidebar route={route} onHome={() => navigate(HOME)} onSubscribe={() => navigate({ view: 'subscribe' })} />
      <main id="blog-main" style={{ flex: 1, minWidth: 0, overflowY: 'auto', position: 'relative' }}>
        <Header route={route} onNavigate={navigate} />
      </main>
    </div>
  )
}
