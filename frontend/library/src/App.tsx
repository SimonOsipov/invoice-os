import { useCallback, useEffect, useState } from 'react'
import { GROUPS, TOUR } from './content'
import { Home } from './components/Home'
import { FeaturePage } from './components/FeaturePage'
import { GroupPage } from './components/GroupPage'
import { JourneyStepper } from './components/JourneyStepper'
import { Sidebar } from './components/Sidebar'
import { TourOverlay } from './components/TourOverlay'
import { chooseConsent, CookieNotice, readConsent } from './analytics'
import { demoHref, featurePlatformHref, groupPlatformHref, privacyHref } from './links'
import { libraryPath, parseLibraryPath, type Route } from './route'
import { TOUR_START, tourBack, tourButtonLabel, tourNext, tourStage, type TourState } from './tour'
import type { Feature, Group } from './types'
import { usePhone } from './phone'
import { useTourSpot } from './useTourSpot'

const toTop = () => {
  const main = document.getElementById('lib-main')
  if (main) main.scrollTop = 0
  document.documentElement.scrollTop = 0
}

export function App() {
  const [route, setRoute] = useState<Route>(() => parseLibraryPath(window.location.pathname))
  // Re-clicking the open feature remounts its page, so the demo restarts.
  const [visit, setVisit] = useState(0)
  const [consent, setConsent] = useState(() => readConsent())
  const [reopened, setReopened] = useState(false)
  const [tour, setTour] = useState<TourState | null>(null)
  const phone = usePhone()
  const tourOn = tour !== null && !phone

  useEffect(() => {
    const onPop = () => {
      setTour(null)
      setRoute(parseLibraryPath(window.location.pathname))
      toTop()
    }
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  // A click on the current view adds no history entry; a non-canonical URL is corrected in place.
  const navigate = useCallback((next: Route) => {
    const path = libraryPath(next)
    if (path !== libraryPath(parseLibraryPath(window.location.pathname))) window.history.pushState(null, '', path)
    else if (path !== window.location.pathname) window.history.replaceState(null, '', path)
    setRoute(next)
    toTop()
  }, [])

  const go = (next: Route) => {
    setTour(null)
    navigate(next)
  }
  const goHome = () => go({ view: 'home' })
  const goGroup = (group: Group) => go({ view: 'group', group })
  const goFeature = (feature: Feature) => {
    const group = GROUPS.find((g) => g.id === feature.gid)
    if (!group) return
    setVisit((v) => v + 1)
    go({ view: 'feature', group, feature })
  }
  const demo = demoHref()

  const startTour = () => setTour(TOUR_START)
  const toggleTour = () => setTour((t) => (t ? null : TOUR_START))
  const stepTo = (next: TourState | null) => {
    setTour(next)
    if (next?.phase === 'card') {
      const group = GROUPS.find((g) => g.id === TOUR[next.i].g)
      if (group) navigate({ view: 'group', group })
    }
  }
  const watch = (t: TourState) => {
    const feature = GROUPS.flatMap((g) => g.feats).find((f) => f.id === TOUR[t.i].f)
    if (feature) goFeature(feature)
  }
  const { rect, win } = useTourSpot(tourOn ? tour : null, libraryPath(route))

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
        route={route}
        demoHref={demo}
        onHome={goHome}
        onGroup={goGroup}
        onFeature={goFeature}
        tourLabel={tourButtonLabel(tourOn ? tour : null)}
        onTour={phone ? null : toggleTour}
        onCookieChoices={() => setReopened(true)}
      />
      <main id="lib-main" style={{ flex: 1, minWidth: 0, overflowY: 'auto', position: 'relative' }}>
        <JourneyStepper route={route} onGroup={goGroup} tourStage={tourOn ? tourStage(tour) : undefined} />
        {route.view === 'home' && <Home demoHref={demo} onGroup={goGroup} onTour={phone ? null : startTour} />}
        {route.view === 'group' && (
          <GroupPage
            group={route.group}
            openHref={groupPlatformHref(route.group)}
            onFeature={goFeature}
          />
        )}
        {route.view === 'feature' && (
          <FeaturePage
            key={`${route.feature.id}:${visit}`}
            group={route.group}
            feature={route.feature}
            openHref={featurePlatformHref(route.feature)}
            onGroup={goGroup}
            onFeature={goFeature}
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
      {tourOn && (
        <TourOverlay
          tour={tour}
          rect={rect}
          win={win}
          onBack={() => stepTo(tourBack(tour))}
          onNext={() => stepTo(tourNext(tour))}
          onWatch={() => watch(tour)}
          onClose={() => setTour(null)}
        />
      )}
    </div>
  )
}
