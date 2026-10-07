import { useCallback, useEffect, useState } from 'react'
import { GROUPS } from './content'
import { Home } from './components/Home'
import { GroupPage } from './components/GroupPage'
import { JourneyStepper } from './components/JourneyStepper'
import { Sidebar } from './components/Sidebar'
import { demoHref, groupPlatformHref } from './links'
import { libraryPath, parseLibraryPath, type Route } from './route'
import type { Feature, Group } from './types'

const toTop = () => {
  const main = document.getElementById('lib-main')
  if (main) main.scrollTop = 0
}

export function App() {
  const [route, setRoute] = useState<Route>(() => parseLibraryPath(window.location.pathname))

  useEffect(() => {
    const onPop = () => {
      setRoute(parseLibraryPath(window.location.pathname))
      toTop()
    }
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  // Compares canonical paths, so a click on the current view pushes nothing (D8).
  const navigate = useCallback((next: Route) => {
    const path = libraryPath(next)
    if (path !== libraryPath(parseLibraryPath(window.location.pathname))) window.history.pushState(null, '', path)
    setRoute(next)
    toTop()
  }, [])

  const goHome = () => navigate({ view: 'home' })
  const goGroup = (group: Group) => navigate({ view: 'group', group })
  const goFeature = (feature: Feature) => {
    const group = GROUPS.find((g) => g.feats.includes(feature))
    if (group) navigate({ view: 'feature', group, feature })
  }
  const goGroupId = (gid: string) => {
    const group = GROUPS.find((g) => g.id === gid)
    if (group) goGroup(group)
  }
  const demo = demoHref()
  // ceiling: inert until the tour ships
  const onTour = () => {}

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
      <Sidebar route={route} demoHref={demo} onHome={goHome} onGroup={goGroup} onFeature={goFeature} onTour={onTour} />
      <main id="lib-main" style={{ flex: 1, minWidth: 0, overflowY: 'auto', position: 'relative' }}>
        <JourneyStepper route={route} onGroup={goGroup} />
        {route.view === 'home' && <Home demoHref={demo} onGroup={goGroupId} onTour={onTour} />}
        {route.view === 'group' && (
          <GroupPage
            group={route.group}
            openHref={groupPlatformHref(route.group)}
            onFeature={(fid) => {
              const feature = route.group.feats.find((f) => f.id === fid)
              if (feature) navigate({ view: 'feature', group: route.group, feature })
            }}
          />
        )}
      </main>
    </div>
  )
}
