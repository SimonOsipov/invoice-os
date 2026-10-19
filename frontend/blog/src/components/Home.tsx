import type { BlogData } from '../data'
import { Icon } from '../icons'
import { bandsOf, catOf, featuredOf, fmtDate } from '../model'
import { Button } from './Button'
import { Cover } from './Cover'
import { PostCard } from './PostCard'

type HomeProps = {
  data: BlogData
  onPost: (slug: string) => void
  onCategory: (id: string) => void
  onSubscribe: () => void
}

const GRID = { display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(300px, 1fr))', gap: 20 } as const

export function Home({ data, onPost, onCategory, onSubscribe }: HomeProps) {
  const featured = featuredOf(data.posts)
  return (
    <>
      <section className="blog-px" style={{ position: 'relative', padding: '72px 40px 64px', overflow: 'hidden' }}>
        <div className="hero-grid hero-grid-fade" style={{ position: 'absolute', inset: 0, opacity: 0.7 }} />
        <div style={{ position: 'relative', maxWidth: 1120, display: 'flex', flexDirection: 'column', gap: 48 }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 24, maxWidth: 820 }}>
            <div className="t-eyebrow">ASComply blog</div>
            <h1
              style={{
                margin: 0,
                fontSize: 'clamp(42px, 5vw, 68px)',
                lineHeight: 1.07,
                letterSpacing: '-0.05em',
                fontWeight: 700,
                color: 'var(--ink)',
              }}
            >
              News from the invoice
              <br />
              <span style={{ color: 'var(--teal)' }}>compliance desk.</span>
            </h1>
            <p className="t-lead" style={{ margin: 0, maxWidth: 600 }}>
              Product releases, e-invoicing regulation, customer stories and notes from the team building ASComply.
            </p>
          </div>
          {featured && (
            <div
              style={{
                display: 'flex',
                flexWrap: 'wrap',
                background: 'var(--card)',
                border: '1px solid var(--border)',
                borderRadius: 6,
                overflow: 'hidden',
              }}
            >
              <div style={{ flex: '1 1 420px', minHeight: 300, position: 'relative', background: 'var(--muted)' }}>
                <Cover post={featured.post} cat={catOf(data, featured.post.category_id)} />
              </div>
              <button
                type="button"
                onClick={() => onPost(featured.post.id)}
                style={{
                  flex: '1 1 380px',
                  textAlign: 'left',
                  cursor: 'pointer',
                  border: 0,
                  background: 'transparent',
                  color: 'var(--ink)',
                  padding: '40px 40px 36px',
                  display: 'flex',
                  flexDirection: 'column',
                  gap: 16,
                }}
              >
                <span className="t-step">
                  {featured.label} · {catOf(data, featured.post.category_id)?.name ?? ''} · {fmtDate(featured.post.publish_date)}
                </span>
                <span style={{ fontSize: 32, fontWeight: 700, letterSpacing: '-0.04em', lineHeight: 1.2 }}>
                  {featured.post.title}
                </span>
                <span className="t-body">{featured.post.summary}</span>
                <span
                  style={{
                    marginTop: 'auto',
                    paddingTop: 8,
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 6,
                    fontSize: 14,
                    fontWeight: 700,
                    color: 'var(--link)',
                  }}
                >
                  Read the post <Icon name="arrow-right" size={16} />
                </span>
              </button>
            </div>
          )}
        </div>
      </section>

      {bandsOf(data).map((band) => (
        <section key={band.cat.id} className="blog-px" style={{ background: band.bg, padding: '72px 40px' }}>
          <div style={{ maxWidth: 1120, display: 'flex', flexDirection: 'column', gap: 36 }}>
            <div
              style={{
                display: 'flex',
                alignItems: 'flex-end',
                justifyContent: 'space-between',
                gap: 24,
                flexWrap: 'wrap',
              }}
            >
              <div style={{ display: 'flex', flexDirection: 'column', gap: 14, maxWidth: 640 }}>
                <div className="t-eyebrow">{band.cat.name}</div>
                <h2 className="t-h2" style={{ margin: 0, fontSize: 36 }}>{band.cat.headline}</h2>
                <p className="t-body" style={{ margin: 0 }}>{band.cat.intro}</p>
              </div>
              <Button variant="outline" size="sm" arrow onClick={() => onCategory(band.cat.id)}>
                {band.allLabel}
              </Button>
            </div>
            <div style={GRID}>
              {band.posts.map((p) => (
                <PostCard key={p.id} post={p} cat={band.cat} cardBd={band.cardBd} onOpen={() => onPost(p.id)} />
              ))}
            </div>
          </div>
        </section>
      ))}

      <section className="blog-px" style={{ background: 'var(--peach-band)', padding: '72px 40px' }}>
        <div
          style={{
            maxWidth: 1120,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            gap: 32,
            flexWrap: 'wrap',
          }}
        >
          <div style={{ display: 'flex', flexDirection: 'column', gap: 14, maxWidth: 620 }}>
            <div className="t-eyebrow">Newsletter</div>
            <h2 className="t-h2" style={{ margin: 0, fontSize: 38 }}>
              New posts in your inbox.
              <br />
              <span className="t-hl-peach">Pick the topics you follow.</span>
            </h2>
          </div>
          <Button variant="primary" arrow onClick={onSubscribe}>
            Subscribe
          </Button>
        </div>
      </section>
    </>
  )
}
