import { useCallback, useEffect, useRef, useState } from 'react'
import { Article } from './components/Article'
import { CategoryPage } from './components/CategoryPage'
import { Confirm } from './components/Confirm'
import { Header } from './components/Header'
import { Home } from './components/Home'
import { LoadFailed } from './components/LoadFailed'
import { NotFound } from './components/NotFound'
import { SearchResults } from './components/SearchResults'
import { Sidebar } from './components/Sidebar'
import { Subscribe } from './components/Subscribe'
import { chooseConsent, CookieNotice, readConsent, syncBlogConsent, trackBlogPageView } from './analytics'
import { confirmToken } from './confirmLink'
import { loadBlog, type BlogData } from './data'
import { privacyHref } from './links'
import { crumbsFor, resolveView, type BlogState } from './model'
import { blogPath, parseBlogPath, type Route } from './route'

const toTop = () => {
  const main = document.getElementById('blog-main')
  if (main) main.scrollTop = 0
  document.documentElement.scrollTop = 0
}

const HOME: Route = { view: 'home' }

export function App({ load = loadBlog }: { load?: () => Promise<BlogData> }) {
  const [route, setRoute] = useState<Route>(() => parseBlogPath(window.location.pathname))
  const [blog, setBlog] = useState<BlogState>({ status: 'loading' })
  const [attempt, setAttempt] = useState(0)
  const [q, setQ] = useState('')

  useEffect(() => {
    let live = true
    load().then(
      (data) => live && setBlog({ status: 'ready', data }),
      () => live && setBlog({ status: 'failed' }),
    )
    return () => {
      live = false
    }
  }, [load, attempt])

  const retry = () => {
    setBlog({ status: 'loading' })
    setAttempt((n) => n + 1)
  }

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
      setQ('')
      toTop()
    }
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  // A click on the current view adds no history entry; a non-canonical URL is corrected in place. Any navigation clears the search.
  const navigate = useCallback((next: Route) => {
    const path = blogPath(next)
    if (path !== blogPath(parseBlogPath(window.location.pathname))) window.history.pushState(null, '', path)
    else if (path !== window.location.pathname) window.history.replaceState(null, '', path)
    setRoute(next)
    setQ('')
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

  const view = resolveView(route, q, blog)
  const data = blog.status === 'ready' ? blog.data : null

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
      <Sidebar
        data={data}
        homeActive={route.view === 'home' && q.trim() === ''}
        activeCat={view.kind === 'category' ? view.cat.id : view.kind === 'post' ? view.post.category_id : null}
        onHome={() => navigate(HOME)}
        onCategory={(id) => navigate({ view: 'category', id })}
        onSubscribe={() => navigate({ view: 'subscribe' })}
        onCookieChoices={() => {
          const next = syncBlogConsent()
          if (next) setConsent(next)
          setReopened(true)
        }}
      />
      <main id="blog-main" style={{ flex: 1, minWidth: 0, overflowY: 'auto', position: 'relative' }}>
        <Header crumbs={crumbsFor(view, navigate)} q={q} onQ={setQ} />
        {view.kind === 'missing' && data && (
          <NotFound categories={data.categories} onCategory={(id) => navigate({ view: 'category', id })} />
        )}
        {view.kind === 'failed' && <LoadFailed onRetry={retry} />}
        {view.kind === 'confirm' && (
          <Confirm token={confirmToken()} onHome={() => navigate(HOME)} onSubscribe={() => navigate({ view: 'subscribe' })} />
        )}
        {data && view.kind === 'subscribe' && <Subscribe categories={data.categories} onHome={() => navigate(HOME)} />}
        {data && view.kind === 'home' && (
          <Home
            data={data}
            onPost={(slug) => navigate({ view: 'post', slug })}
            onCategory={(id) => navigate({ view: 'category', id })}
            onSubscribe={() => navigate({ view: 'subscribe' })}
          />
        )}
        {data && view.kind === 'category' && (
          <CategoryPage data={data} cat={view.cat} onPost={(slug) => navigate({ view: 'post', slug })} />
        )}
        {data && view.kind === 'search' && (
          <SearchResults
            data={data}
            q={q}
            onPost={(slug) => navigate({ view: 'post', slug })}
            onCategory={(id) => navigate({ view: 'category', id })}
          />
        )}
        {view.kind === 'post' && (
          <Article
            key={view.post.id}
            post={view.post}
            cat={view.cat}
            onCategory={(id) => navigate({ view: 'category', id })}
            onSubscribe={() => navigate({ view: 'subscribe' })}
          />
        )}
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
