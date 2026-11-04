import { landingBase } from '../links'
import { Button } from './Button'

export function NotFoundView({ onHome }: { onHome: () => void }) {
  return (
    <section className="lib-px" style={{ padding: '56px 40px 80px', maxWidth: 1180, display: 'flex', flexDirection: 'column', gap: 40 }}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 18, maxWidth: 700 }}>
        <h1 className="t-h2" style={{ margin: 0, fontSize: 44 }}>
          Page not found
        </h1>
        <p className="t-lead" style={{ margin: 0 }}>
          The page you are looking for does not exist or has moved.
        </p>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 12, marginTop: 4 }}>
          <Button variant="primary" href={`${landingBase() ?? ''}/`}>
            Back to home
          </Button>
          <Button variant="outline" onClick={onHome}>
            Browse the library
          </Button>
        </div>
      </div>
    </section>
  )
}
