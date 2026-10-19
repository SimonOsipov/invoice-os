import { Button } from './Button'

export function LoadFailed({ onRetry }: { onRetry: () => void }) {
  return (
    <section
      className="blog-px"
      style={{ padding: '56px 40px 80px', maxWidth: 900, display: 'flex', flexDirection: 'column', gap: 32 }}
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <div className="t-eyebrow">Blog</div>
        <h2 className="t-h2" style={{ margin: 0, fontSize: 36 }}>The blog did not load.</h2>
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        <p className="t-body" style={{ margin: 0 }}>Check your connection, then try again.</p>
        <div>
          <Button variant="outline" onClick={onRetry}>Try again</Button>
        </div>
      </div>
    </section>
  )
}
