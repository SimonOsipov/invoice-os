import { Button } from './ds/Button'
import { Section } from './ds/Section'

export const NOT_FOUND_COPY = {
  heading: 'Page not found',
  body: 'The page you are looking for does not exist or has moved.',
  home: 'Back to home',
  library: 'Browse the library',
} as const

export function NotFound({ libraryHref }: { libraryHref: string | null }) {
  return (
    <Section>
      <h1 className="t-h2" style={{ margin: '0 0 18px' }}>
        {NOT_FOUND_COPY.heading}
      </h1>
      <p className="t-body" style={{ margin: '0 0 28px' }}>
        {NOT_FOUND_COPY.body}
      </p>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 12 }}>
        <Button variant="primary" href="/">
          {NOT_FOUND_COPY.home}
        </Button>
        {libraryHref !== null && (
          <Button variant="outline" href={libraryHref}>
            {NOT_FOUND_COPY.library}
          </Button>
        )}
      </div>
    </Section>
  )
}
